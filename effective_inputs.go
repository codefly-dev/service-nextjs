package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

//go:embed discovery/inputs.cjs
var nativeInputsScript string

// inputRequestLimit matches gRPC's default maximum received message size. A
// request the transport already accepted is one discovery answers: an oversized
// context yields the bare inventory, never an error, because Core propagates
// every status except Unimplemented to its caller.
const inputRequestLimit = 4 * 1024 * 1024

// inputResponseLimit bounds the declaration on the wire.
const inputResponseLimit = 512 * 1024

// nativeOutputLimit bounds what one containerized inspection may print.
const nativeOutputLimit = 128 * 1024

// nativePathBudget bounds observed paths per task. The inspector applies it too;
// this side re-applies it because container output is untrusted.
const nativePathBudget = 512

// nativeTaskTimeout bounds one task inspection. An ordinary Next.js application
// of roughly 540 source files traces in about 11 seconds, so a 10-second budget
// observed nothing on real applications while still passing fixture-sized
// conformance. The budget must clear real applications, not fixtures.
const nativeTaskTimeout = 60 * time.Second

// nativeDiscoveryBudget bounds every inspection for one request. It exceeds the
// per-task budget times the inspected task count, so a slow task cannot starve
// the tail; tasks that still do not fit are reported unobserved, never dropped
// in silence.
const nativeDiscoveryBudget = 8 * time.Minute

// nativeCleanupTimeout bounds container removal. Removal is operational hygiene
// on a busy daemon, so it is given real time and never fails the caller.
const nativeCleanupTimeout = 30 * time.Second

// Declaration markers. Each is an unresolved input, so a task carrying one can
// never be reused. They exist so that an observation which failed, ran out of
// budget, or was cut short is distinguishable from one that genuinely saw
// nothing beyond the declared configuration.
const (
	markerDynamicConsumption       = "discovery/unresolved-execution-and-dynamic-consumption"
	markerInspectionUnavailable    = "discovery/isolated-native-discovery-unavailable"
	markerObservationIncomplete    = "discovery/native-observation-incomplete"
	markerIsolationUnverified      = "discovery/native-discovery-cleanup-unverified"
	markerRuntimeClosureUnresolved = "discovery/runtime-service-closure-unresolved"
	markerTransportTruncated       = "discovery/declaration-truncated-for-transport"
)

// nodeLockfileNames lists every package-manager lockfile a Node workspace may
// carry. Declaration and classification read this one list, so a new entry
// cannot be declared while still being classified as ordinary source.
var nodeLockfileNames = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb"}

// nativeTaskResult is one inspection's output. Truncated reports that the
// inspector hit its path budget, so the list is a prefix of real consumption
// rather than the whole of it.
type nativeTaskResult struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// nativeInputs is everything isolated inspection established for one request.
type nativeInputs struct {
	// Tasks holds the workspace-relative paths observed for each task name.
	Tasks map[string][]string
	// Observed marks a task whose inspection ran and produced a path list.
	Observed map[string]bool
	// Incomplete marks a task whose observation is known partial: it failed, hit
	// the path budget, or never ran inside the request budget.
	Incomplete map[string]bool
	// Available reports whether isolated inspection could run at all.
	Available bool
	// CleanupVerified reports whether every container was confirmed removed.
	CleanupVerified bool
}

type inputProject struct {
	root, workspace, source, owner, module string
	settings                               Settings
	service                                *resources.Service
}

func (s *Service) inputProject(ctx context.Context) (*inputProject, error) {
	root := os.Getenv(agents.WorkDirEnvironment)
	if root == "" {
		root = s.Location
	}
	if root == "" {
		return nil, fmt.Errorf("service directory is not configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	service, err := resources.LoadServiceFromDir(ctx, root)
	if err != nil {
		return nil, err
	}
	project := &inputProject{root: root, workspace: root, owner: service.Name, service: service}
	if err := service.LoadSettingsFromSpec(&project.settings); err != nil {
		return nil, err
	}
	if s.Identity != nil {
		project.workspace = s.Identity.WorkspacePath
		project.module = s.Identity.Module
	} else {
		moduleDir, err := resources.FindUpFrom[resources.Module](ctx, root)
		if err != nil {
			return nil, err
		}
		if moduleDir != nil {
			module, err := resources.LoadModuleFromDir(ctx, *moduleDir)
			if err != nil {
				return nil, err
			}
			project.module = module.Name
			project.workspace = filepath.Dir(*moduleDir)
		}
		workspaceDir, err := resources.FindUpFrom[resources.Workspace](ctx, root)
		if err != nil {
			return nil, err
		}
		if workspaceDir != nil {
			project.workspace = *workspaceDir
		}
	}
	if project.module != "" {
		project.owner = project.module + "/" + service.Name
	}
	sourceDir := project.settings.NodeSourceDir()
	if !filepath.IsLocal(sourceDir) {
		return nil, fmt.Errorf("source directory escapes service")
	}
	project.source = filepath.Join(root, sourceDir)
	return project, nil
}

func (s *Service) GetEffectiveInputs(ctx context.Context, req *agentv0.GetEffectiveInputsRequest) (*agentv0.GetEffectiveInputsResponse, error) {
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if req == nil || req.GetSnapshot() == "" {
		return nil, status.Error(codes.InvalidArgument, "effective inputs require a snapshot")
	}
	if req.SchemaVersion != ciinputs.Version {
		return nil, status.Error(codes.Unimplemented, "unsupported effective input schema")
	}
	required, err := ciinputs.Required(nextValidationCapabilities())
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid validation inventory")
	}
	if _, err = ciinputs.Evaluate(nil, req, required); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid effective input context")
	}
	response := &agentv0.GetEffectiveInputsResponse{SchemaVersion: ciinputs.Version, Snapshot: req.Snapshot}
	for _, key := range required {
		response.Tasks = append(response.Tasks, &agentv0.TaskInputs{Task: &agentv0.TaskKey{Phase: key.Phase, Suite: key.Suite}})
	}
	// Two requests cannot be inspected: one naming a source state other than the
	// current worktree, and one whose context is larger than the transport bounds.
	// Both answer with the full inventory and no observations, so every task stays
	// conservative and runs. Refusing the call would instead fail the caller's
	// whole discovery, because Core degrades only on Unimplemented.
	if req.Revision != "" || proto.Size(req) > inputRequestLimit {
		return response, nil
	}
	project, err := s.inputProject(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot resolve effective input project")
	}
	native := discoverNativeInputs(ctx, project)
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	for _, task := range response.Tasks {
		if task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
			s.declareSBOMInputs(project, task, req.Context)
			continue
		}
		declareTaskInputs(project, task, req.Context, native)
	}
	boundResponse(response, project.owner)
	if _, err := ciinputs.Evaluate(response, req, required); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid discovered inputs")
	}
	return response, nil
}

// declareTaskInputs states what one non-SBOM task consumes. Every declaration
// carries the unresolved-consumption marker because framework observation does
// not establish dynamic or configuration-time reads, and additionally records
// whether isolated inspection ran, was verifiable, and covered the task.
func declareTaskInputs(project *inputProject, task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput, native nativeInputs) {
	name := inputTaskName(task.Task)
	task.Inputs = []*agentv0.EffectiveInput{
		marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, project.owner, markerDynamicConsumption),
	}
	if nativeInspectsPhase(task.Task.Phase) {
		switch {
		case !native.Available:
			task.Inputs = append(task.Inputs, marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, project.owner, markerInspectionUnavailable))
		case native.Incomplete[name]:
			task.Inputs = append(task.Inputs, marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, project.owner, markerObservationIncomplete))
		}
		if !native.CleanupVerified {
			task.Inputs = append(task.Inputs, marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, project.owner, markerIsolationUnverified))
		}
	}
	names := []string{filepath.Join(project.root, "service.codefly.yaml"), filepath.Join(project.source, "package.json")}
	for _, lock := range nodeLockfileNames {
		names = append(names, filepath.Join(project.source, lock))
	}
	for _, observed := range native.Tasks[name] {
		if filepath.IsLocal(observed) {
			names = append(names, filepath.Join(project.workspace, filepath.FromSlash(observed)))
		}
	}
	seen := map[string]bool{}
	for _, path := range names {
		appendInputPath(project, path, task, seen)
	}
	declareDependencies(project, task, contextInputs)
	finalizeTaskInputs(task, contextInputs)
}

// declareDependencies resolves service dependencies to their stable owner
// identity before declaring anything. Two declared entries can name the same
// owner — a bare name inheriting the module, and an explicit module/name pair —
// and Core validates uniqueness on the unresolved pair, so both reach this
// agent. Participation is therefore merged per resolved owner: emitting per
// declared entry would repeat an input identity, which Core rejects outright.
func declareDependencies(project *inputProject, task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput) {
	type participation struct{ runs, builds bool }
	startsDependencies := nextTaskStartsDependencies(task.Task)
	buildsArtifact := task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD
	owners := map[string]*participation{}
	var order []string
	for _, dep := range project.service.ServiceDependencies {
		module := dep.Module
		if module == "" {
			module = project.module
		}
		owner := dep.Name
		if module != "" {
			owner = module + "/" + dep.Name
		}
		entry := owners[owner]
		if entry == nil {
			entry = &participation{}
			owners[owner] = entry
			order = append(order, owner)
		}
		entry.runs = entry.runs || (startsDependencies && dep.Kind.Participates(resources.StageRun))
		entry.builds = entry.builds || (buildsArtifact && dep.Kind.Participates(resources.StageBuild))
	}
	for _, owner := range order {
		entry := owners[owner]
		if entry.runs {
			task.RuntimeServices = append(task.RuntimeServices, owner)
		}
		for _, in := range contextInputs {
			if in.Owner != owner || in.Path {
				continue
			}
			runtimeIdentity := entry.runs && in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION
			buildIdentity := entry.builds && (in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ARTIFACT || in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_GENERATED_CONTRACT)
			if runtimeIdentity || buildIdentity {
				task.Inputs = append(task.Inputs, proto.Clone(in).(*agentv0.EffectiveInput))
			}
		}
	}
	if task.Task.Suite == "smoke" {
		task.RuntimeServices = append(task.RuntimeServices, project.owner)
	}
	if nextTaskStartsStack(task.Task) {
		// A stack-starting suite needs the transitive runtime closure. This agent
		// reads only its own service declaration, so the list below is a subset;
		// saying so keeps it from being mistaken for the whole stack.
		task.Inputs = append(task.Inputs, marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, project.owner, markerRuntimeClosureUnresolved))
	}
	slices.Sort(task.RuntimeServices)
	task.RuntimeServices = slices.Compact(task.RuntimeServices)
}

func (s *Service) declareSBOMInputs(project *inputProject, task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput) {
	seen := map[string]bool{}
	declaration := filepath.Join(project.root, "service.codefly.yaml")
	lockfile := filepath.Join(project.source, "package-lock.json")
	appendInputPath(project, declaration, task, seen)
	appendInputPath(project, lockfile, task, seen)

	options := marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, project.owner, "sbom/options")
	optionsResolved := false
	for _, in := range contextInputs {
		if in.Kind != options.Kind || in.Owner != options.Owner || in.Name != options.Name {
			continue
		}
		optionsResolved = in.GetIdentity().GetKind() == agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED &&
			in.Identity.Namespace == "codefly.nextjs.sbom-options/v1" &&
			(in.Identity.Digest == "include-dev=false" || in.Identity.Digest == "include-dev=true")
		break
	}
	// Core sets this public identity only after verifying the provider artifact.
	artifact := os.Getenv("CODEFLY_PROVIDER_ARTIFACT_DIGEST")
	plugin := marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_PLUGIN, "codefly.dev/nextjs", "agent")
	toolchain := marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, "codefly.dev/nextjs", "sbom/executable")
	if strings.HasPrefix(artifact, "sha256:") && len(artifact) == 71 {
		plugin.Identity = &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "codefly.provider-artifact/v1", Digest: artifact}
		toolchain.Identity = &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "codefly.nextjs.sbom-toolchain/v1", Digest: artifact + "/" + runtime.GOOS + "/" + runtime.GOARCH}
	}
	task.Inputs = append(task.Inputs, options, plugin, toolchain)
	finalizeTaskInputs(task, contextInputs)
	// Builder.SBOM reads only the chosen package-lock.json and its include-dev
	// option. It resolves that lockfile under its own service location, joined
	// with the source directory it loads from the declaration already listed
	// here, so a changed source directory changes a declared input's identity.
	// Only the root can diverge, because discovery prefers the agent manager's
	// attachment directory over the loaded location.
	task.Complete = len(seen) == 2 && len(task.Inputs) == 5 && optionsResolved && s.inspectsBuilderTree(project)
}

// inspectsBuilderTree reports whether discovery inspected the same tree
// Builder.SBOM will read. An agent that has not been loaded has no location of
// its own and cannot disagree with discovery.
func (s *Service) inspectsBuilderTree(project *inputProject) bool {
	if s.Location == "" {
		return true
	}
	return sameDirectory(s.Location, project.root)
}

func sameDirectory(left, right string) bool {
	resolve := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		if physical, err := filepath.EvalSymlinks(absolute); err == nil {
			return physical
		}
		return absolute
	}
	a, b := resolve(left), resolve(right)
	return a != "" && a == b
}

// finalizeTaskInputs makes a declaration acceptable to Core by construction.
// Core rejects the entire response when a declared input shares an identity with
// resolved context but differs in any field, and when a declaration repeats an
// identity. Both are decided on (kind, owner, name), so reconciliation and
// de-duplication key on exactly that: the caller's resolved copy always wins,
// and an identity is declared at most once. Matching on anything else — a file
// mode read from the filesystem, say — lets the agent contradict context it
// meant to adopt, which fails the caller's whole discovery.
func finalizeTaskInputs(task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput) {
	supplied := make(map[string]*agentv0.EffectiveInput, len(contextInputs))
	for _, in := range contextInputs {
		supplied[effectiveInputKey(in)] = in
	}
	seen := make(map[string]bool, len(task.Inputs))
	kept := task.Inputs[:0]
	for _, in := range task.Inputs {
		key := effectiveInputKey(in)
		if seen[key] {
			continue
		}
		seen[key] = true
		if resolved, ok := supplied[key]; ok {
			in = proto.Clone(resolved).(*agentv0.EffectiveInput)
		}
		kept = append(kept, in)
	}
	task.Inputs = kept
}

// effectiveInputKey mirrors the identity Core uses to detect contradictions and
// duplicates. It must not drift from ciinputs' own key.
func effectiveInputKey(in *agentv0.EffectiveInput) string {
	return fmt.Sprintf("%d\x00%s\x00%s", in.Kind, in.Owner, in.Name)
}

func marker(kind agentv0.EffectiveInputKind, owner, name string) *agentv0.EffectiveInput {
	return &agentv0.EffectiveInput{Kind: kind, Owner: owner, Name: name}
}

// boundResponse keeps the declaration inside the transport budget without
// erasing the inventory or the scheduling contract. Observations are dropped
// from the largest tasks first, and only as far as needed; runtime services
// state which services a suite must have started, and no size condition makes
// that untrue, so they are surrendered only when nothing else is left.
func boundResponse(response *agentv0.GetEffectiveInputsResponse, owner string) {
	if proto.Size(response) <= inputResponseLimit {
		return
	}
	order := slices.Clone(response.Tasks)
	slices.SortFunc(order, func(a, b *agentv0.TaskInputs) int { return proto.Size(b) - proto.Size(a) })
	for _, task := range order {
		if proto.Size(response) <= inputResponseLimit {
			return
		}
		task.Inputs = []*agentv0.EffectiveInput{marker(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, owner, markerTransportTruncated)}
		task.Complete = false
	}
	for _, task := range order {
		if proto.Size(response) <= inputResponseLimit {
			return
		}
		task.RuntimeServices = nil
	}
}

func inputTaskName(task *agentv0.TaskKey) string {
	if task.Phase == agentv0.TaskPhase_TASK_PHASE_TEST {
		return "test/" + task.Suite
	}
	return task.Phase.String()
}

// nativeInspectsPhase reports which phases isolated inspection covers. The
// discovery loop and the declaration both read it here so they cannot drift.
func nativeInspectsPhase(phase agentv0.TaskPhase) bool {
	return phase == agentv0.TaskPhase_TASK_PHASE_TEST ||
		phase == agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD ||
		phase == agentv0.TaskPhase_TASK_PHASE_COMPILE
}

func suiteDependencyMode(task *agentv0.TaskKey) agentv0.TestDependencyMode {
	if task.Phase != agentv0.TaskPhase_TASK_PHASE_TEST {
		return agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE
	}
	for _, suite := range nextValidationCapabilities().Test.Suites {
		if suite.Name == task.Suite {
			return suite.DependencyMode
		}
	}
	return agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE
}

func nextTaskStartsDependencies(task *agentv0.TaskKey) bool {
	return suiteDependencyMode(task) != agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE
}

func nextTaskStartsStack(task *agentv0.TaskKey) bool {
	return suiteDependencyMode(task) == agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_STACK
}

func appendInputPath(project *inputProject, name string, task *agentv0.TaskInputs, seen map[string]bool) {
	name = filepath.Clean(name)
	relative, err := filepath.Rel(project.workspace, name)
	if err != nil || !filepath.IsLocal(relative) || seen[relative] || len(seen) >= nativePathBudget {
		return
	}
	seen[relative] = true
	info, err := os.Lstat(name)
	if err != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
		return
	}
	for ancestor := filepath.Dir(relative); ancestor != "."; ancestor = filepath.Dir(ancestor) {
		parent := filepath.Join(project.workspace, ancestor)
		if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			appendInputPath(project, parent, task, seen)
		}
	}
	in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, Owner: project.owner, Name: filepath.ToSlash(relative), Path: true, Mode: 0100644, Sensitive: true}
	if info.Mode()&0111 != 0 {
		in.Mode = 0100755
	}
	switch base := filepath.Base(name); {
	case base == "service.codefly.yaml", base == "package.json", base == "tsconfig.json", base == "jsconfig.json":
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION
	case slices.Contains(nodeLockfileNames, base):
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_LOCKFILE
	}
	if info.Mode()&os.ModeSymlink != 0 {
		in.Mode = 0120000
	}
	task.Inputs = append(task.Inputs, in)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(name)
		if err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(name), target)
			}
			appendInputPath(project, target, task, seen)
		}
	}
}

type inputOutput struct{ data []byte }

func (out *inputOutput) Write(p []byte) (int, error) {
	if len(out.data)+len(p) > nativeOutputLimit {
		return 0, fmt.Errorf("native discovery output exceeds budget")
	}
	out.data = append(out.data, p...)
	return len(p), nil
}

// usableImageReference rejects a configured value that is not an image
// reference. The value reaches the Docker CLI as a positional argument, so a
// flag-shaped or whitespace-bearing value would be parsed as something other
// than the image it is supposed to name.
func usableImageReference(image string) bool {
	if image == "" || strings.HasPrefix(image, "-") {
		return false
	}
	return !strings.ContainsAny(image, " \t\r\n\x00")
}

func discoverNativeInputs(ctx context.Context, project *inputProject) nativeInputs {
	native := nativeInputs{
		Tasks:           map[string][]string{},
		Observed:        map[string]bool{},
		Incomplete:      map[string]bool{},
		CleanupVerified: true,
	}
	source, err := filepath.Rel(project.workspace, project.source)
	if err != nil || !filepath.IsLocal(source) {
		return native
	}
	image := project.settings.RuntimeImage
	if image == "" {
		image = runtimeImage.FullName()
	}
	if !usableImageReference(image) {
		return native
	}
	// Resolve an installed image without pulling or running project code on the host.
	ctx, cancel := context.WithTimeout(ctx, nativeDiscoveryBudget)
	defer cancel()
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	id, err := inspect.Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(id)), "sha256:") {
		return native
	}
	dir, err := os.MkdirTemp("", "nextjs-inputs-")
	if err != nil {
		return native
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "inputs.cjs")
	if os.WriteFile(script, []byte(nativeInputsScript), 0644) != nil {
		return native
	}
	native.Available = true

	required, _ := ciinputs.Required(nextValidationCapabilities())
	for index, key := range required {
		if !nativeInspectsPhase(key.Phase) {
			continue
		}
		task := inputTaskName(&agentv0.TaskKey{Phase: key.Phase, Suite: key.Suite})
		if ctx.Err() != nil {
			native.Incomplete[task] = true
			continue
		}
		container := fmt.Sprintf("nextjs-inputs-%s-%d", filepath.Base(dir), index)
		result, ok, cleaned := runInputTask(ctx, project, source, strings.TrimSpace(string(id)), script, container, task)
		if !cleaned {
			// A container that cannot be confirmed removed leaves isolation
			// unverifiable for this request. That is an operational fault of the
			// host, so it is declared, never returned as a caller-visible error.
			native.CleanupVerified = false
		}
		if !ok {
			native.Incomplete[task] = true
			continue
		}
		native.Tasks[task] = result.Paths
		native.Observed[task] = true
		if result.Truncated {
			native.Incomplete[task] = true
		}
	}
	unionCompileObservations(&native)
	return native
}

// unionCompileObservations folds the production build's observation into the
// compile task, because Runtime.Build runs the typecheck and build scripts in
// one pass and so consumes both. The union applies only when both were
// observed: inheriting build paths for a compile task that was never inspected
// would declare consumption nothing established.
func unionCompileObservations(native *nativeInputs) {
	compile := agentv0.TaskPhase_TASK_PHASE_COMPILE.String()
	build := agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD.String()
	if !native.Observed[compile] {
		return
	}
	if !native.Observed[build] {
		native.Incomplete[compile] = true
		return
	}
	paths := append(slices.Clone(native.Tasks[compile]), native.Tasks[build]...)
	slices.Sort(paths)
	native.Tasks[compile] = slices.Compact(paths)
	if native.Incomplete[build] {
		native.Incomplete[compile] = true
	}
}

func runInputTask(ctx context.Context, project *inputProject, source, image, script, container, task string) (result nativeTaskResult, ok bool, cleaned bool) {
	ctx, cancel := context.WithTimeout(ctx, nativeTaskTimeout)
	defer cancel()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), nativeCleanupTimeout)
		defer cancel()
		// Killing the Docker client does not stop the container or its descendants.
		output, err := exec.CommandContext(cleanup, "docker", "rm", "--force", container).CombinedOutput()
		cleaned = err == nil || strings.Contains(string(output), "No such container:")
	}()
	cmd := exec.CommandContext(ctx, "docker", "run", "--pull=never", "--name", container,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit=64", "--memory=512m", "--cpus=1", "--user="+fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m,mode=1777",
		"--mount", "type=bind,src="+project.workspace+",dst=/workspace,readonly",
		"--mount", "type=bind,src="+script+",dst=/inputs.cjs,readonly",
		"--workdir", "/workspace/"+filepath.ToSlash(source), "--entrypoint", "/bin/sh", image,
		"-c", `node /inputs.cjs "$1" /tmp/inputs.json "$2" >/dev/null 2>/dev/null && cat /tmp/inputs.json`, "discovery", "/workspace/"+filepath.ToSlash(source), task)
	var output inputOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nativeTaskResult{}, false, false
	}
	if json.Unmarshal(output.data, &result) != nil {
		return nativeTaskResult{}, false, false
	}
	// Container output is untrusted; re-apply the budget the inspector applies.
	if len(result.Paths) > nativePathBudget {
		result.Paths = result.Paths[:nativePathBudget]
		result.Truncated = true
	}
	return result, true, false
}

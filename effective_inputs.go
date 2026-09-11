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

const inputResponseLimit = 512 * 1024
const nativeOutputLimit = 128 * 1024

type nativeInputs struct {
	Tasks map[string][]string `json:"tasks"`
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
	if proto.Size(req) > nativeOutputLimit {
		return nil, status.Error(codes.ResourceExhausted, "effective input request exceeds budget")
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
	if req.Revision != "" {
		return response, nil
	}
	project, err := s.inputProject(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot resolve effective input project")
	}
	native, ok, err := discoverNativeInputs(ctx, project)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "cannot clean up native discovery container")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	for _, task := range response.Tasks {
		if task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
			s.declareSBOMInputs(project, task, req.Context)
			continue
		}
		task.Inputs = []*agentv0.EffectiveInput{{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, Owner: project.owner, Name: "discovery/unresolved-execution-and-dynamic-consumption"}}
		if !ok {
			task.Inputs = append(task.Inputs, &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, Owner: project.owner, Name: "discovery/isolated-native-discovery-unavailable"})
		}
		names := []string{filepath.Join(project.root, "service.codefly.yaml"), filepath.Join(project.source, "package.json")}
		for _, lock := range []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb"} {
			names = append(names, filepath.Join(project.source, lock))
		}
		for _, name := range native.Tasks[inputTaskName(task.Task)] {
			if filepath.IsLocal(name) {
				names = append(names, filepath.Join(project.workspace, filepath.FromSlash(name)))
			}
		}
		seen := map[string]bool{}
		for _, name := range names {
			appendInputPath(project, name, task, req.Context, seen)
		}
		for _, dep := range project.service.ServiceDependencies {
			module := dep.Module
			if module == "" {
				module = project.module
			}
			owner := dep.Name
			if module != "" {
				owner = module + "/" + dep.Name
			}
			runtime := nextTaskStartsDependencies(task.Task) && dep.Kind.Participates(resources.StageRun)
			build := task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD && dep.Kind.Participates(resources.StageBuild)
			if runtime {
				task.RuntimeServices = append(task.RuntimeServices, owner)
			}
			for _, in := range req.Context {
				if in.Owner != owner || in.Path {
					continue
				}
				if runtime && in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION || build && (in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ARTIFACT || in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_GENERATED_CONTRACT) {
					task.Inputs = append(task.Inputs, proto.Clone(in).(*agentv0.EffectiveInput))
				}
			}
		}
		if task.Task.Suite == "smoke" {
			task.RuntimeServices = append(task.RuntimeServices, project.owner)
		}
		slices.Sort(task.RuntimeServices)
		task.RuntimeServices = slices.Compact(task.RuntimeServices)
	}
	// Incomplete observations must still fit the transport; the inventory is never truncated.
	if proto.Size(response) > inputResponseLimit {
		for _, task := range response.Tasks {
			task.Inputs = nil
			task.RuntimeServices = nil
			task.Complete = false
		}
	}
	if _, err := ciinputs.Evaluate(response, req, required); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid discovered inputs")
	}
	return response, nil
}

func (s *Service) declareSBOMInputs(project *inputProject, task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput) {
	seen := map[string]bool{}
	appendInputPath(project, filepath.Join(project.root, "service.codefly.yaml"), task, contextInputs, seen)
	appendInputPath(project, filepath.Join(project.source, "package-lock.json"), task, contextInputs, seen)
	options := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, Owner: project.owner, Name: "sbom/options"}
	for _, in := range contextInputs {
		if in.Kind == options.Kind && in.Owner == options.Owner && in.Name == options.Name {
			if in.GetIdentity().GetKind() == agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED && in.Identity.Namespace == "codefly.nextjs.sbom-options/v1" && (in.Identity.Digest == "include-dev=false" || in.Identity.Digest == "include-dev=true") {
				options = proto.Clone(in).(*agentv0.EffectiveInput)
			}
			break
		}
	}
	task.Inputs = append(task.Inputs, options)
	// Core sets this public identity only after verifying the provider artifact.
	artifact := os.Getenv("CODEFLY_PROVIDER_ARTIFACT_DIGEST")
	plugin := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_PLUGIN, Owner: "codefly.dev/nextjs", Name: "agent"}
	toolchain := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, Owner: "codefly.dev/nextjs", Name: "sbom/executable"}
	if strings.HasPrefix(artifact, "sha256:") && len(artifact) == 71 {
		plugin.Identity = &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "codefly.provider-artifact/v1", Digest: artifact}
		toolchain.Identity = &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "codefly.nextjs.sbom-toolchain/v1", Digest: artifact + "/" + runtime.GOOS + "/" + runtime.GOARCH}
	}
	task.Inputs = append(task.Inputs, plugin, toolchain)
	// Builder.SBOM reads only the chosen package-lock.json and its include-dev option.
	task.Complete = len(seen) == 2 && len(task.Inputs) == 5 && options.Identity != nil && s.Settings.NodeSourceDir() == project.settings.NodeSourceDir()
}

func inputTaskName(task *agentv0.TaskKey) string {
	if task.Phase == agentv0.TaskPhase_TASK_PHASE_TEST {
		return "test/" + task.Suite
	}
	return task.Phase.String()
}

func nextTaskStartsDependencies(task *agentv0.TaskKey) bool {
	if task.Phase != agentv0.TaskPhase_TASK_PHASE_TEST {
		return false
	}
	for _, suite := range nextValidationCapabilities().Test.Suites {
		if suite.Name == task.Suite {
			return suite.DependencyMode != agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE
		}
	}
	return false
}

func appendInputPath(project *inputProject, name string, task *agentv0.TaskInputs, contextInputs []*agentv0.EffectiveInput, seen map[string]bool) {
	name = filepath.Clean(name)
	relative, err := filepath.Rel(project.workspace, name)
	if err != nil || !filepath.IsLocal(relative) || seen[relative] || len(seen) >= 512 {
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
			appendInputPath(project, parent, task, contextInputs, seen)
		}
	}
	in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, Owner: project.owner, Name: filepath.ToSlash(relative), Path: true, Mode: 0100644, Sensitive: true}
	if info.Mode()&0111 != 0 {
		in.Mode = 0100755
	}
	switch filepath.Base(name) {
	case "service.codefly.yaml", "package.json", "tsconfig.json", "jsconfig.json":
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb":
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_LOCKFILE
	}
	if info.Mode()&os.ModeSymlink != 0 {
		in.Mode = 0120000
	}
	// Only caller-resolved identities carry a sensitivity classification and snapshot binding.
	for _, supplied := range contextInputs {
		if supplied.Path && supplied.Name == in.Name && supplied.Mode == in.Mode {
			in = proto.Clone(supplied).(*agentv0.EffectiveInput)
			break
		}
	}
	task.Inputs = append(task.Inputs, in)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(name)
		if err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(name), target)
			}
			appendInputPath(project, target, task, contextInputs, seen)
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

func discoverNativeInputs(ctx context.Context, project *inputProject) (result nativeInputs, ok bool, failure error) {
	source, err := filepath.Rel(project.workspace, project.source)
	if err != nil || !filepath.IsLocal(source) {
		return result, false, nil
	}
	image := project.settings.RuntimeImage
	if image == "" {
		image = runtimeImage.FullName()
	}
	// Resolve an installed image without pulling or running project code on the host.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	id, err := inspect.Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(id)), "sha256:") {
		return result, false, nil
	}
	dir, err := os.MkdirTemp("", "nextjs-inputs-")
	if err != nil {
		return result, false, nil
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "inputs.cjs")
	if os.WriteFile(script, []byte(nativeInputsScript), 0644) != nil {
		return result, false, nil
	}

	result.Tasks = map[string][]string{}
	required, _ := ciinputs.Required(nextValidationCapabilities())
	for index, key := range required {
		if ctx.Err() != nil {
			break
		}
		if key.Phase != agentv0.TaskPhase_TASK_PHASE_TEST && key.Phase != agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD && key.Phase != agentv0.TaskPhase_TASK_PHASE_COMPILE {
			continue
		}
		task := inputTaskName(&agentv0.TaskKey{Phase: key.Phase, Suite: key.Suite})
		container := fmt.Sprintf("nextjs-inputs-%s-%d", filepath.Base(dir), index)
		paths, discovered, err := runInputTask(ctx, project, source, strings.TrimSpace(string(id)), script, container, task)
		if err != nil {
			return result, false, err
		}
		if discovered {
			result.Tasks[task] = paths
		}
	}
	if paths, found := result.Tasks[agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD.String()]; found {
		key := agentv0.TaskPhase_TASK_PHASE_COMPILE.String()
		result.Tasks[key] = append(result.Tasks[key], paths...)
		slices.Sort(result.Tasks[key])
		result.Tasks[key] = slices.Compact(result.Tasks[key])
	}
	return result, true, nil
}

func runInputTask(ctx context.Context, project *inputProject, source, image, script, container, task string) (result []string, ok bool, failure error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Killing the Docker client does not stop the container or its descendants.
		output, err := exec.CommandContext(cleanup, "docker", "rm", "--force", container).CombinedOutput()
		if err != nil && !strings.Contains(string(output), "No such container:") {
			failure = fmt.Errorf("native discovery container cleanup failed")
		}
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
		return result, false, nil
	}
	if json.Unmarshal(output.data, &result) != nil {
		return nil, false, nil
	}
	return result, true, nil
}

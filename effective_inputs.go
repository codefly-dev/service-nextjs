package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

//go:embed discovery/inputs.cjs
var nativeInputsScript string

type nativeInputs struct {
	Production []string            `json:"production"`
	Compile    []string            `json:"compile"`
	Suites     map[string][]string `json:"suites"`
}

func (s *Service) GetEffectiveInputs(ctx context.Context, req *agentv0.GetEffectiveInputsRequest) (*agentv0.GetEffectiveInputsResponse, error) {
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
	// The loaded service graph and installed tools describe only the worktree.
	if req.Revision != "" {
		return response, nil
	}
	if s.Identity == nil || s.Location == "" {
		return response, nil
	}
	source, err := s.resolveSourceLocation(ctx)
	if err != nil {
		return response, nil
	}
	workspace := s.Identity.WorkspacePath
	owner := s.Identity.Unique()
	files := map[string]*agentv0.EffectiveInput{}
	addFile := func(name string) { discoverInputFile(workspace, owner, name, files, map[string]bool{}) }
	err = filepath.WalkDir(s.Location, func(name string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".next", ".codefly":
				return filepath.SkipDir
			}
			return nil
		}
		addFile(name)
		return nil
	})
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	native, nativeOK := discoverNativeInputs(ctx, source)
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	for _, paths := range append([][]string{native.Production, native.Compile}, suitePaths(native.Suites)...) {
		for _, name := range paths {
			addFile(name)
		}
	}
	for _, task := range response.Tasks {
		add := func(in *agentv0.EffectiveInput) { task.Inputs = append(task.Inputs, in) }
		unresolved := func(kind agentv0.EffectiveInputKind, name string) {
			add(&agentv0.EffectiveInput{Kind: kind, Owner: owner, Name: name})
		}
		for _, in := range files {
			add(proto.Clone(in).(*agentv0.EffectiveInput))
		}
		for _, in := range req.Context {
			if in.Kind == agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION && !nextTaskStartsDependencies(task.Task) {
				continue
			}
			// Files are observed in this snapshot; caller context cannot replace their content.
			if observed, exists := files[effectiveInputKey(in)]; exists {
				if observed.Sensitive && in.Sensitive && observed.Mode == in.Mode && in.Path {
					for i, declared := range task.Inputs {
						if effectiveInputKey(declared) == effectiveInputKey(in) {
							task.Inputs[i] = proto.Clone(in).(*agentv0.EffectiveInput)
							break
						}
					}
				}
				continue
			}
			add(proto.Clone(in).(*agentv0.EffectiveInput))
		}
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, "discovery/arbitrary-configuration-and-plugin-consumption")
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ENVIRONMENT, "discovery/ambient-environment-and-runtime-configuration")
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_TOOLCHAIN, "discovery/execution-backend-and-installed-tools")
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_PLUGIN, "discovery/resolved-agent-and-plugins")
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_GENERATOR, "discovery/generators-and-generated-inputs")
		unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_EXTERNAL, "discovery/dynamic-and-external-consumption")
		if err != nil {
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, "discovery/unreadable-source-tree")
		}
		if !nativeOK {
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, "discovery/native-discovery-unavailable")
		}
		if task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD {
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ARTIFACT, "discovery/image-recipe-context-and-prerequisites")
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_VALIDATION, "discovery/validation-prerequisites")
		}
		if task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_TEST {
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_FIXTURE, "discovery/dynamic-fixtures-and-invocation-selectors")
		}
		if nextTaskStartsDependencies(task.Task) {
			unresolved(agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, "discovery/transitive-runtime-implementation-closure")
			if s.Service != nil {
				for _, dep := range s.Service.ServiceDependencies {
					module := dep.Module
					if module == "" {
						module = s.Identity.Module
					}
					service := module + "/" + dep.Name
					task.RuntimeServices = append(task.RuntimeServices, service)
				}
			}
			if task.Task.Suite == "smoke" {
				task.RuntimeServices = append(task.RuntimeServices, owner)
			}
		}
	}
	for _, task := range response.Tasks {
		sort.Slice(task.Inputs, func(i, j int) bool { return effectiveInputKey(task.Inputs[i]) < effectiveInputKey(task.Inputs[j]) })
		sort.Strings(task.RuntimeServices)
	}
	if _, err = ciinputs.Evaluate(response, req, required); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "discovery contradicts execution context or contains invalid inputs")
	}
	return response, nil
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

func suitePaths(suites map[string][]string) [][]string {
	var paths [][]string
	for _, names := range suites {
		paths = append(paths, names)
	}
	return paths
}

func discoverNativeInputs(ctx context.Context, source string) (nativeInputs, bool) {
	var result nativeInputs
	dir, err := os.MkdirTemp("", "nextjs-inputs-")
	if err != nil {
		return result, false
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "inputs.cjs")
	output := filepath.Join(dir, "inputs.json")
	if os.WriteFile(script, []byte(nativeInputsScript), 0600) != nil {
		return result, false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script, source, output)
	cmd.Dir = source
	if cmd.Run() != nil {
		return result, false
	}
	data, err := os.ReadFile(output)
	if err != nil || json.Unmarshal(data, &result) != nil {
		return nativeInputs{}, false
	}
	return result, true
}

func effectiveInputKey(in *agentv0.EffectiveInput) string {
	return in.Kind.String() + "\x00" + in.Owner + "\x00" + in.Name
}

func discoverInputFile(workspace, owner, name string, inputs map[string]*agentv0.EffectiveInput, visiting map[string]bool) {
	name = filepath.Clean(name)
	relative, err := filepath.Rel(workspace, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || visiting[name] {
		return
	}
	visiting[name] = true
	info, err := os.Lstat(name)
	if err != nil {
		return
	}
	if info.IsDir() {
		err := filepath.WalkDir(name, func(child string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				discoverInputFile(workspace, owner, child, inputs, visiting)
			}
			return nil
		})
		if err != nil {
			in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, Owner: owner, Name: "discovery/unreadable-symlink-target"}
			inputs[effectiveInputKey(in)] = in
		}
		return
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return
	}
	in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SOURCE, Owner: owner, Name: filepath.ToSlash(relative), Path: true, Mode: 0100644}
	base := filepath.Base(name)
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb":
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_LOCKFILE
	case "service.codefly.yaml", ".npmrc", ".yarnrc.yml":
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION
		in.Sensitive = true
	}
	if strings.Contains(base, ".config.") || base == "tsconfig.json" || base == "jsconfig.json" {
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION
		in.Sensitive = true
	}
	if strings.HasPrefix(base, ".env") {
		in.Kind = agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ENVIRONMENT
		in.Sensitive = true
	}
	if info.Mode()&0111 != 0 {
		in.Mode = 0100755
	}
	if info.Mode()&os.ModeSymlink != 0 {
		in.Mode = 0120000
		link, err := os.Readlink(name)
		if err == nil {
			if !in.Sensitive {
				in.Identity = inputContentIdentity([]byte(link))
			}
			target := link
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(name), target)
			}
			discoverInputFile(workspace, owner, target, inputs, visiting)
		}
	} else if !in.Sensitive {
		physical, err := filepath.EvalSymlinks(name)
		if err != nil {
			return
		}
		physicalWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return
		}
		resolved, err := filepath.Rel(physicalWorkspace, physical)
		if err != nil || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
			return
		}
		if data, err := os.ReadFile(name); err == nil {
			in.Identity = inputContentIdentity(data)
		}
	}
	inputs[effectiveInputKey(in)] = in
}

func inputContentIdentity(data []byte) *agentv0.EffectiveIdentity {
	digest := sha256.Sum256(data)
	return &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_SHA256, Digest: hex.EncodeToString(digest[:])}
}

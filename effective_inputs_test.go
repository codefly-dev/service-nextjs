package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func inputsFixture(t *testing.T) (*Service, agentv0.AgentClient) {
	t.Helper()
	root := t.TempDir()
	service := NewService()
	service.Location = root
	service.Identity = &resources.ServiceIdentity{WorkspacePath: root, Module: "app", Name: "web"}
	t.Setenv(agents.WorkDirEnvironment, root)
	declaration := &resources.Service{Name: "web", Version: "0.0.0", ServiceDependencies: []*resources.ServiceDependency{{Name: "api"}}, Spec: map[string]any{"docker-image": "sha256:" + strings.Repeat("0", 64)}}
	require.NoError(t, declaration.SaveAtDir(context.Background(), root))
	inputWrite(t, root, "code/package.json", `{"scripts":{"test":"custom-runner"}}`)
	return service, inputClient(t, service)
}

func inputClient(t *testing.T, service *Service) agentv0.AgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	agentv0.RegisterAgentServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///inputs", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return agentv0.NewAgentClient(conn)
}

func inputWrite(t *testing.T, root, name, content string) {
	t.Helper()
	name = filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0755))
	require.NoError(t, os.WriteFile(name, []byte(content), 0644))
}

func inputTask(t *testing.T, response *agentv0.GetEffectiveInputsResponse, phase agentv0.TaskPhase, suite string) *agentv0.TaskInputs {
	t.Helper()
	for _, task := range response.Tasks {
		if task.Task.Phase == phase && task.Task.Suite == suite {
			return task
		}
	}
	t.Fatalf("missing task %v/%s", phase, suite)
	return nil
}

func pathInput(t *testing.T, task *agentv0.TaskInputs, name string) *agentv0.EffectiveInput {
	t.Helper()
	for _, in := range task.Inputs {
		if in.Path && in.Name == name {
			return in
		}
	}
	t.Fatalf("missing path %s", name)
	return nil
}

func inputContentIdentity(data []byte) *agentv0.EffectiveIdentity {
	sum := sha256.Sum256(data)
	return &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_SHA256, Digest: hex.EncodeToString(sum[:])}
}

func TestEffectiveInputsHeadlessCoreDiscovery(t *testing.T) {
	loaded, _ := inputsFixture(t)
	cold := NewService()
	client := inputClient(t, cold)
	info, err := client.GetAgentInformation(context.Background(), &agentv0.AgentInformationRequest{})
	require.NoError(t, err)
	require.Equal(t, []uint32{1}, info.EffectiveInputsVersions)
	require.True(t, proto.Equal(nextValidationCapabilities(), info.Validation))
	required, err := ciinputs.Required(info.Validation)
	require.NoError(t, err)
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "headless"}
	tasks, err := ciinputs.Discover(context.Background(), client, info, req, required)
	require.NoError(t, err)
	require.Len(t, tasks, len(required))
	for _, task := range tasks {
		require.NotNil(t, task.Declaration)
		name := "code/package.json"
		if task.Key.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
			name = "service.codefly.yaml"
		}
		require.NotEmpty(t, pathInput(t, task.Declaration, name))
		require.True(t, task.Conservative)
		require.False(t, task.CacheEligible)
	}
	require.Nil(t, cold.Identity)
	require.Empty(t, cold.currentSourceLocation())
	require.FileExists(t, filepath.Join(loaded.Location, "code/package.json"))
}

func TestEffectiveInputsDoesNotHashSecretsOrSymlinkTargets(t *testing.T) {
	service, client := inputsFixture(t)
	require.NoError(t, os.Remove(filepath.Join(service.Location, "code/package.json")))
	inputWrite(t, service.Location, "code/credentials/password", "123456")
	inputWrite(t, service.Location, "code/.ignored-secret", "another-secret")
	require.NoError(t, os.Symlink("credentials/password", filepath.Join(service.Location, "code/package.json")))
	response, err := client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "secret"})
	require.NoError(t, err)
	for _, task := range response.Tasks {
		if task.Task.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
			continue
		}
		for _, name := range []string{"code/package.json", "code/credentials/password"} {
			in := pathInput(t, task, name)
			require.True(t, in.Sensitive)
			require.Nil(t, in.Identity)
		}
	}
	data, err := proto.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(data), "123456")
	require.NotContains(t, string(data), inputContentIdentity([]byte("123456")).Digest)
	require.NotContains(t, string(data), "another-secret")
}

func TestEffectiveInputsLargeTreeFitsRPC(t *testing.T) {
	service, client := inputsFixture(t)
	for i := 0; i < 4000; i++ {
		inputWrite(t, service.Location, fmt.Sprintf("code/sources/module-%04d.ts", i), "export const value=1")
	}
	file, err := os.Create(filepath.Join(service.Location, "code/large-asset.bin"))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(8<<30))
	require.NoError(t, file.Close())
	response, err := client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "large"})
	require.NoError(t, err)
	require.Less(t, proto.Size(response), inputResponseLimit)
	require.Len(t, response.Tasks, 11)
}

func TestEffectiveInputsDependencyKindsAndFreshMetadata(t *testing.T) {
	service, client := inputsFixture(t)
	declaration, err := resources.LoadServiceFromDir(context.Background(), service.Location)
	require.NoError(t, err)
	for _, kind := range append(resources.DeclarableDependencyKinds(), resources.DependencyKindLegacy) {
		declaration.ServiceDependencies = []*resources.ServiceDependency{{Name: "producer", Kind: kind}}
		require.NoError(t, declaration.SaveAtDir(context.Background(), service.Location))
		implementation := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, Owner: "app/producer", Name: "implementation", Identity: inputContentIdentity([]byte("api"))}
		artifact := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ARTIFACT, Owner: "app/producer", Name: "artifact", Identity: inputContentIdentity([]byte("image"))}
		response, err := client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: string(kind) + "snapshot", Context: []*agentv0.EffectiveInput{implementation, artifact}})
		require.NoError(t, err)
		for _, suite := range []string{"unit", "pure", "integration", "e2e", "smoke"} {
			task := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_TEST, suite)
			if suite != "pure" && kind.Participates(resources.StageRun) {
				require.Contains(t, task.RuntimeServices, "app/producer")
				require.True(t, hasInput(task, implementation))
			} else {
				require.NotContains(t, task.RuntimeServices, "app/producer")
				require.False(t, hasInput(task, implementation))
			}
			if suite == "smoke" {
				require.Contains(t, task.RuntimeServices, "app/web")
			}
		}
		task := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
		require.Empty(t, task.RuntimeServices)
		require.False(t, hasInput(task, implementation))
		if kind.Participates(resources.StageBuild) {
			require.True(t, hasInput(task, artifact))
		} else {
			require.False(t, hasInput(task, artifact))
		}
	}
}

func TestEffectiveInputsProtectedContextAndHistory(t *testing.T) {
	_, client := inputsFixture(t)
	protected, err := ciinputs.Protect([]byte("01234567890123456789012345678901"), "key-v1", []byte("secret"))
	require.NoError(t, err)
	in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, Owner: "app/web", Name: "service.codefly.yaml", Path: true, Mode: 0100644, Sensitive: true, Identity: protected}
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "s", Context: []*agentv0.EffectiveInput{in}}
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	for _, task := range response.Tasks {
		require.True(t, proto.Equal(in, pathInput(t, task, in.Name)))
	}
	req.Revision = "HEAD~1"
	response, err = client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, req.Snapshot, response.Snapshot)
	for _, task := range response.Tasks {
		require.False(t, task.Complete)
		require.Empty(t, task.Inputs)
		require.Empty(t, task.RuntimeServices)
	}
}

func TestEffectiveInputsInvalidRequests(t *testing.T) {
	_, client := inputsFixture(t)
	for _, tc := range []struct {
		req  *agentv0.GetEffectiveInputsRequest
		code codes.Code
	}{
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 2, Snapshot: "s"}, codes.Unimplemented},
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 1}, codes.InvalidArgument},
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "s", Context: []*agentv0.EffectiveInput{{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ENVIRONMENT, Owner: "web", Name: "secret", Sensitive: true, Identity: inputContentIdentity([]byte("secret"))}}}, codes.InvalidArgument},
	} {
		_, err := client.GetEffectiveInputs(context.Background(), tc.req)
		require.Equal(t, tc.code, status.Code(err))
	}
}

func hasInput(task *agentv0.TaskInputs, want *agentv0.EffectiveInput) bool {
	for _, in := range task.Inputs {
		if proto.Equal(in, want) {
			return true
		}
	}
	return false
}

func TestEffectiveInputsOutputBudgets(t *testing.T) {
	output := inputOutput{}
	n, err := output.Write(make([]byte, nativeOutputLimit))
	require.NoError(t, err)
	require.Equal(t, nativeOutputLimit, n)
	n, err = output.Write([]byte("overflow"))
	require.Error(t, err)
	require.Zero(t, n)
	require.Len(t, output.data, nativeOutputLimit)
	_, client := inputsFixture(t)
	_, err = client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: strings.Repeat("s", nativeOutputLimit+1)})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "bounded-response", Context: []*agentv0.EffectiveInput{{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, Owner: "app/api", Name: "implementation", Identity: &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "fixture/v1", Digest: strings.Repeat("x", nativeOutputLimit-256)}}}}
	require.LessOrEqual(t, proto.Size(req), nativeOutputLimit)
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	require.LessOrEqual(t, proto.Size(response), inputResponseLimit)
	require.Len(t, response.Tasks, 11)
	for _, task := range response.Tasks {
		require.Empty(t, task.Inputs)
		require.False(t, task.Complete)
	}

}

func TestEffectiveInputsCompleteSBOMThroughCore(t *testing.T) {
	service, client := inputsFixture(t)
	t.Setenv("CODEFLY_PROVIDER_ARTIFACT_DIGEST", "sha256:"+strings.Repeat("a", 64))
	lock := `{"name":"web","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"web","version":"1.0.0"}}}`
	inputWrite(t, service.Location, "code/package-lock.json", lock)
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "before", Context: []*agentv0.EffectiveInput{{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION, Owner: "app/web", Name: "sbom/options", Identity: &agentv0.EffectiveIdentity{Kind: agentv0.EffectiveIdentityKind_EFFECTIVE_IDENTITY_KIND_VERSIONED, Namespace: "codefly.nextjs.sbom-options/v1", Digest: "include-dev=false"}}}}
	for _, file := range []struct {
		name string
		kind agentv0.EffectiveInputKind
	}{{"service.codefly.yaml", agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_CONFIGURATION}, {"code/package-lock.json", agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_LOCKFILE}} {
		data, err := os.ReadFile(filepath.Join(service.Location, file.name))
		require.NoError(t, err)
		identity, err := ciinputs.Protect([]byte("01234567890123456789012345678901"), "fixture/key-v1", data)
		require.NoError(t, err)
		req.Context = append(req.Context, &agentv0.EffectiveInput{Kind: file.kind, Owner: "app/web", Name: file.name, Path: true, Mode: 0100644, Sensitive: true, Identity: identity})
	}
	info, err := client.GetAgentInformation(context.Background(), &agentv0.AgentInformationRequest{})
	require.NoError(t, err)
	required, err := ciinputs.Required(info.Validation)
	require.NoError(t, err)
	discover := func() ciinputs.Task {
		tasks, err := ciinputs.Discover(context.Background(), client, info, req, required)
		require.NoError(t, err)
		for _, task := range tasks {
			if task.Key.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
				require.True(t, task.CacheEligible)
				return task
			}
		}
		t.Fatal("missing SBOM")
		return ciinputs.Task{}
	}
	before := discover()
	req.Context[2].Owner = "another/service"
	unrelated, err := ciinputs.Discover(context.Background(), client, info, req, required)
	require.NoError(t, err)
	for _, task := range unrelated {
		if task.Key.Phase == agentv0.TaskPhase_TASK_PHASE_SBOM {
			require.False(t, task.CacheEligible)
		}
	}
	req.Context[2].Owner = "app/web"
	builder := NewBuilder(service)
	bomBefore, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{})
	require.NoError(t, err)
	inputWrite(t, service.Location, "code/unit.test.ts", "test-only edit")
	req.Snapshot = "test-only"
	require.Equal(t, before.Identity, discover().Identity)
	bomAfter, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{})
	require.NoError(t, err)
	require.True(t, proto.Equal(bomBefore, bomAfter))
	inputWrite(t, service.Location, "code/package-lock.json", strings.ReplaceAll(lock, "1.0.0", "2.0.0"))
	data, err := os.ReadFile(filepath.Join(service.Location, "code/package-lock.json"))
	require.NoError(t, err)
	req.Context[2].Identity, err = ciinputs.Protect([]byte("01234567890123456789012345678901"), "fixture/key-v1", data)
	require.NoError(t, err)
	req.Snapshot = "lock-change"
	lockChanged := discover()
	require.NotEqual(t, before.Identity, lockChanged.Identity)
	bomAfter, err = builder.SBOM(context.Background(), &builderv0.SBOMRequest{})
	require.NoError(t, err)
	require.False(t, proto.Equal(bomBefore, bomAfter))
	t.Setenv("CODEFLY_PROVIDER_ARTIFACT_DIGEST", "sha256:"+strings.Repeat("b", 64))
	providerChanged := discover()
	require.NotEqual(t, lockChanged.Identity, providerChanged.Identity)
	req.Context[0].Identity.Digest = "include-dev=true"
	require.NotEqual(t, providerChanged.Identity, discover().Identity)
	require.NoError(t, os.Rename(filepath.Join(service.Location, "code"), filepath.Join(service.Location, "real-code")))
	require.NoError(t, os.Symlink("real-code", filepath.Join(service.Location, "code")))
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	sbom := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_SBOM, "")
	require.False(t, sbom.Complete)
	require.EqualValues(t, 0120000, pathInput(t, sbom, "code").Mode)
}

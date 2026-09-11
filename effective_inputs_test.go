package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
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
	service.Identity = &resources.ServiceIdentity{WorkspacePath: root, Module: "app", Name: "web"}
	service.Location = root
	service.Service = &resources.Service{ServiceDependencies: []*resources.ServiceDependency{{Name: "api"}}}
	service.setSourceLocation(filepath.Join(root, "code"))
	inputWrite(t, root, "code/package.json", `{ "scripts": {"test": "custom-runner"} }`)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	agentv0.RegisterAgentServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///inputs", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return service, agentv0.NewAgentClient(conn)
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

func TestEffectiveInputsRPCPreservesInventoryAndDependencyModes(t *testing.T) {
	service, client := inputsFixture(t)
	inputWrite(t, service.Location, "code/src/production.test.ts", "export const value = 1")
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "candidate", Context: []*agentv0.EffectiveInput{
		{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, Owner: "app/api", Name: "implementation", Identity: inputContentIdentity([]byte("api"))},
		{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, Owner: "app/database", Name: "implementation", Identity: inputContentIdentity([]byte("db"))},
		{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_GENERATED_CONTRACT, Owner: "app/api", Name: "contract", Identity: inputContentIdentity([]byte("contract"))},
	}}
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, req.Snapshot, response.Snapshot)
	required, err := ciinputs.Required(nextValidationCapabilities())
	require.NoError(t, err)
	require.Len(t, response.Tasks, len(required))
	evaluated, err := ciinputs.Evaluate(response, req, required)
	require.NoError(t, err)
	for _, task := range evaluated {
		require.True(t, task.Conservative)
		require.False(t, task.CacheEligible)
	}
	for _, suite := range []string{"unit", "pure", "integration", "e2e", "smoke"} {
		task := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_TEST, suite)
		if suite == "pure" {
			require.Empty(t, task.RuntimeServices)
		} else {
			require.Contains(t, task.RuntimeServices, "app/api")
		}
		if suite == "smoke" {
			require.Contains(t, task.RuntimeServices, "app/web")
		}
		for _, in := range task.Inputs {
			if in.Owner == "app/database" {
				require.NotEqual(t, "pure", suite)
			}
		}
	}
	artifact := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
	require.Empty(t, artifact.RuntimeServices)
	require.NotNil(t, pathInput(t, artifact, "code/src/production.test.ts").Identity)
	for _, in := range artifact.Inputs {
		require.NotEqual(t, agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, in.Kind)
	}
}

func TestEffectiveInputsRPCSnapshotChangesAndSecrets(t *testing.T) {
	service, client := inputsFixture(t)
	root := service.Location
	inputWrite(t, root, "code/fixture.txt", "one")
	inputWrite(t, root, "code/.env.local", "SECRET=do-not-emit")
	inputWrite(t, root, "service.codefly.yaml", "secret: do-not-emit")
	inputWrite(t, root, "code/package-lock.json", "{}")
	require.NoError(t, os.Symlink("fixture.txt", filepath.Join(root, "code/link")))
	discover := func(snapshot, revision string) *agentv0.GetEffectiveInputsResponse {
		response, err := client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: snapshot, Revision: revision})
		require.NoError(t, err)
		return response
	}
	before := discover("before", "")
	artifact := inputTask(t, before, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
	require.EqualValues(t, 0120000, pathInput(t, artifact, "code/link").Mode)
	require.Equal(t, inputContentIdentity([]byte("fixture.txt")), pathInput(t, artifact, "code/link").Identity)
	require.True(t, pathInput(t, artifact, "code/.env.local").Sensitive)
	require.Nil(t, pathInput(t, artifact, "code/.env.local").Identity)
	encoded, err := proto.Marshal(before)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "do-not-emit")
	for _, name := range []string{"code/fixture.txt", "code/package-lock.json", "code/next.config.js", "code/toolchain.lock", "code/packages/shared/index.ts"} {
		inputWrite(t, root, name, "changed")
		after := inputTask(t, discover(name, ""), agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
		if name == "code/next.config.js" {
			require.True(t, pathInput(t, after, name).Sensitive)
			require.Nil(t, pathInput(t, after, name).Identity)
		} else {
			require.Equal(t, inputContentIdentity([]byte("changed")), pathInput(t, after, name).Identity)
		}
	}
	require.NoError(t, os.Rename(filepath.Join(root, "code/fixture.txt"), filepath.Join(root, "code/moved.txt")))
	after := inputTask(t, discover("renamed", ""), agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
	for _, in := range after.Inputs {
		require.NotEqual(t, "code/fixture.txt", in.Name)
	}
	require.NotNil(t, pathInput(t, after, "code/moved.txt"))
	historical := discover("reference", "HEAD~1")
	for _, task := range historical.Tasks {
		require.False(t, task.Complete)
		require.Empty(t, task.Inputs)
	}
}

func TestEffectiveInputsRPCRejectsInvalidContextAndVersions(t *testing.T) {
	_, client := inputsFixture(t)
	for _, tc := range []struct {
		req  *agentv0.GetEffectiveInputsRequest
		code codes.Code
	}{
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 2, Snapshot: "s"}, codes.Unimplemented},
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 1}, codes.InvalidArgument},
		{&agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "s", Context: []*agentv0.EffectiveInput{{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ENVIRONMENT, Owner: "app/web", Name: "secret", Sensitive: true, Identity: inputContentIdentity([]byte("secret"))}}}, codes.InvalidArgument},
	} {
		_, err := client.GetEffectiveInputs(context.Background(), tc.req)
		require.Equal(t, tc.code, status.Code(err))
	}
}

func TestEffectiveInputsProtectedContextAndLegacyAdvertisement(t *testing.T) {
	service, client := inputsFixture(t)
	inputWrite(t, service.Location, "code/.env.local", "SECRET=private")
	protected, err := ciinputs.Protect([]byte("01234567890123456789012345678901"), "test/key-v1", []byte("SECRET=private"))
	require.NoError(t, err)
	in := &agentv0.EffectiveInput{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_ENVIRONMENT, Owner: "app/web", Name: "code/.env.local", Sensitive: true, Path: true, Mode: 0100644, Identity: protected}
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "protected", Context: []*agentv0.EffectiveInput{in}}
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	for _, task := range response.Tasks {
		require.True(t, proto.Equal(in, pathInput(t, task, in.Name)))
	}
	info, err := client.GetAgentInformation(context.Background(), &agentv0.AgentInformationRequest{})
	require.NoError(t, err)
	require.Empty(t, info.GetEffectiveInputsVersions())
	require.True(t, proto.Equal(nextValidationCapabilities(), info.Validation))
}

func TestEffectiveInputsSymlinkBoundaries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inputWrite(t, root, "target/value.ts", "target")
	inputWrite(t, outside, "secret", "must-not-be-hashed")
	require.NoError(t, os.Symlink("target", filepath.Join(root, "linked")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "external")))
	inputs := map[string]*agentv0.EffectiveInput{}
	for _, name := range []string{"linked", "external", "external/secret"} {
		discoverInputFile(root, "app/web", filepath.Join(root, name), inputs, map[string]bool{})
	}
	var names []string
	for _, in := range inputs {
		names = append(names, in.Name)
		require.NotEqual(t, inputContentIdentity([]byte("must-not-be-hashed")), in.Identity)
	}
	require.Contains(t, names, "linked")
	require.Contains(t, names, "target/value.ts")
	require.Contains(t, names, "external")
	require.NotContains(t, names, "external/secret")
}

func TestEffectiveInputsContentEdgesRemainTaskSpecific(t *testing.T) {
	_, client := inputsFixture(t)
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "before", Context: []*agentv0.EffectiveInput{
		{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_SERVICE_IMPLEMENTATION, Owner: "app/api", Name: "implementation", Identity: inputContentIdentity([]byte("api-v1"))},
		{Kind: agentv0.EffectiveInputKind_EFFECTIVE_INPUT_KIND_LIBRARY, Owner: "app/shared", Name: "library", Identity: inputContentIdentity([]byte("library-v1"))},
	}}
	before, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	req.Snapshot = "api-change"
	req.Context[0].Identity = inputContentIdentity([]byte("api-v2"))
	after, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	artifact := agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD
	test := agentv0.TaskPhase_TASK_PHASE_TEST
	require.True(t, proto.Equal(inputTask(t, before, artifact, ""), inputTask(t, after, artifact, "")))
	require.True(t, proto.Equal(inputTask(t, before, test, "pure"), inputTask(t, after, test, "pure")))
	for _, suite := range []string{"unit", "integration", "e2e", "smoke"} {
		require.False(t, proto.Equal(inputTask(t, before, test, suite), inputTask(t, after, test, suite)))
	}
	req.Snapshot = "library-change"
	req.Context[1].Identity = inputContentIdentity([]byte("library-v2"))
	library, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	require.False(t, proto.Equal(inputTask(t, after, artifact, ""), inputTask(t, library, artifact, "")))
	require.False(t, proto.Equal(inputTask(t, after, test, "pure"), inputTask(t, library, test, "pure")))
	req.Snapshot = "removed-edge"
	req.Context = req.Context[1:]
	removed, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	require.False(t, proto.Equal(inputTask(t, library, test, "integration"), inputTask(t, removed, test, "integration")))
}

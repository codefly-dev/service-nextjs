//go:build proto_companion_required

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

func TestSyncConformancePreservesProducersAndCombinesDependencyImports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	builder := syncConformanceBuilder(t)
	builder.DependencyEndpoints = []*basev0.Endpoint{
		syncConformanceEndpoint("accounts", `syntax = "proto3";
package shared;
import "google/api/annotations.proto";
message Request {}
message Response {}
service Accounts { rpc Get(Request) returns (Response) { option (google.api.http) = { get: "/accounts" }; } }
`),
		syncConformanceEndpoint("billing", `syntax = "proto3";
package shared;
message Request {}
message Response {}
service Billing { rpc Get(Request) returns (Response); }
`),
	}
	root := builder.Local("code/src/gen")
	writeGeneratedTestFile(t, root, "saas/accounts/v1/user_settings_pb.ts", "producer-owned")
	writeGeneratedTestFile(t, root, "manual.ts", "product-owned")
	writeGeneratedTestFile(t, root, "google/api/http_pb.ts", "producer-owned import")
	writeGeneratedTestFile(t, root, "mod_obsolete_grpc_pb.ts", "stale client")
	before, err := generatedFiles(root)
	require.NoError(t, err)

	dry, err := builder.Sync(ctx, &builderv0.SyncRequest{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, builderv0.SyncStatus_SUCCESS, dry.GetState().GetState(), dry.GetState().GetMessage())
	require.Contains(t, dry.GetChangedFiles(), "mod/frontend/code/src/gen/"+generatedPrivateDirectory+"/google/api/annotations_pb.ts")
	after, err := generatedFiles(root)
	require.NoError(t, err)
	require.Equal(t, before, after)

	response, err := builder.Sync(ctx, &builderv0.SyncRequest{})
	require.NoError(t, err)
	require.Equal(t, builderv0.SyncStatus_SUCCESS, response.GetState().GetState(), response.GetState().GetMessage())
	for _, name := range []string{"mod_accounts_grpc_pb.ts", "mod_billing_grpc_pb.ts", generatedPrivateDirectory + "/google/api/annotations_pb.ts", generatedOwnershipFile} {
		require.FileExists(t, filepath.Join(root, name))
	}
	require.NoFileExists(t, filepath.Join(root, "mod_obsolete_grpc_pb.ts"))
	for _, name := range []string{"saas/accounts/v1/user_settings_pb.ts", "manual.ts", "google/api/http_pb.ts"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, before[name].data, data)
	}

	code := builder.Local("code")
	writeGeneratedTestFile(t, code, "consumer.ts", `export { Accounts, RequestSchema } from "./src/gen/mod_accounts_grpc_pb";
export type { Request } from "./src/gen/mod_accounts_grpc_pb";
export { Billing } from "./src/gen/mod_billing_grpc_pb";
`)
	install := exec.CommandContext(ctx, "npm", "install", "--save-exact", "typescript@5.9.3", "@bufbuild/protobuf@2.12.0")
	install.Dir = code
	logs, err := install.CombinedOutput()
	require.NoError(t, err, "%s", logs)
	compile := exec.CommandContext(ctx, "node", "node_modules/typescript/bin/tsc", "--noEmit", "--target", "ES2020", "--moduleResolution", "bundler", "--module", "ESNext", "consumer.ts")
	compile.Dir = code
	logs, err = compile.CombinedOutput()
	require.NoError(t, err, "%s", logs)

	dry, err = builder.Sync(ctx, &builderv0.SyncRequest{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, builderv0.SyncStatus_SUCCESS, dry.GetState().GetState(), dry.GetState().GetMessage())
	require.Empty(t, dry.GetChangedFiles())

	builder.Base.Service.ServiceDependencies = builder.Base.Service.ServiceDependencies[1:]
	response, err = builder.Sync(ctx, &builderv0.SyncRequest{})
	require.NoError(t, err)
	require.Equal(t, builderv0.SyncStatus_SUCCESS, response.GetState().GetState(), response.GetState().GetMessage())
	require.NoFileExists(t, filepath.Join(root, "mod_accounts_grpc_pb.ts"))
	require.NoFileExists(t, filepath.Join(root, generatedPrivateDirectory, "google/api/annotations_pb.ts"))
	require.FileExists(t, filepath.Join(root, "saas/accounts/v1/user_settings_pb.ts"))
}

func TestSyncConformanceGenerationFailurePreservesEntireLiveTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	builder := syncConformanceBuilder(t)
	builder.DependencyEndpoints = []*basev0.Endpoint{
		syncConformanceEndpoint("accounts", `syntax = "proto3"; package shared; message Request {}`),
		syncConformanceEndpoint("billing", "invalid proto"),
	}
	root := builder.Local("code/src/gen")
	writeGeneratedTestFile(t, root, "mod_accounts_grpc_pb.ts", "existing client")
	writeGeneratedTestFile(t, root, "saas/accounts/v1/user_settings_pb.ts", "producer-owned")
	before, err := generatedFiles(root)
	require.NoError(t, err)
	response, err := builder.Sync(ctx, &builderv0.SyncRequest{})
	require.NoError(t, err)
	require.Equal(t, builderv0.SyncStatus_ERROR, response.GetState().GetState())
	require.NotEmpty(t, response.GetState().GetMessage())
	after, err := generatedFiles(root)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func syncConformanceBuilder(t *testing.T) *Builder {
	t.Helper()
	identity, _ := testIdentity(t, t.TempDir())
	builder := NewBuilder(NewService())
	_, err := builder.Load(context.Background(), &builderv0.LoadRequest{
		Identity: identity, CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	builder.Base.Service.ServiceDependencies = []*resources.ServiceDependency{
		{Module: "mod", Name: "accounts"},
		{Module: "mod", Name: "billing"},
	}
	return builder
}

func syncConformanceEndpoint(service, source string) *basev0.Endpoint {
	return &basev0.Endpoint{
		Module: "mod", Service: service, Name: "grpc", Api: "grpc",
		ApiDetails: &basev0.API{Value: &basev0.API_Grpc{Grpc: &basev0.GrpcAPI{Proto: []byte(source)}}},
	}
}

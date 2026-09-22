package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/agents/contract"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/network"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

func TestDependencyOnlyTestReceivesAcceptedAddressAtInit(t *testing.T) {
	ctx := context.Background()
	dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("accepted dependency"))
	}))
	t.Cleanup(dependency.Close)
	endpoint := &basev0.Endpoint{Module: "mod", Service: "api", Name: "http", Api: "http"}
	mapping := &basev0.NetworkMapping{Endpoint: endpoint, Instances: []*basev0.NetworkInstance{
		network.Native(endpoint, uint16(dependency.Listener.Addr().(*net.TCPAddr).Port)),
	}}
	runtime := dependencyTestRuntime(t)
	info, err := runtime.GetAgentInformation(ctx, nil)
	require.NoError(t, err)
	require.Contains(t, info.GetContract().GetCapabilities(), contract.RuntimeInitDependencyMappings)
	response, err := runtime.Init(ctx, &runtimev0.InitRequest{
		RuntimeContext:              resources.NewRuntimeContextNative(),
		DependenciesEndpoints:       []*basev0.Endpoint{endpoint},
		DependenciesNetworkMappings: []*basev0.NetworkMapping{mapping},
		Fixture:                     "dev-admin", Overrides: map[string]string{"TEST_INVOCATION_INPUT": "from-init"},
	})
	require.NoError(t, err)
	require.Equal(t, runtimev0.InitStatus_READY, response.GetStatus().GetState(), response.GetStatus().GetMessage())
	result, err := runtime.Test(ctx, &runtimev0.TestRequest{Suite: "unit"})
	require.NoError(t, err)
	require.Equal(t, runtimev0.TestRunResult_PASSED, result.GetResult().GetState(), result.String())
	require.EqualValues(t, 1, result.GetCounts().GetPassed())
	// A previous Start must not leave two values for the same endpoint key.
	require.NoError(t, runtime.EnvironmentVariables.AddEndpoints(ctx, []*basev0.NetworkMapping{mapping}, resources.NewNativeNetworkAccess()))
	variables, err := runtime.testEnvironment(ctx, "unit")
	require.NoError(t, err)
	key := resources.EndpointAsEnvironmentVariableKey(resources.EndpointInformationFromProto(endpoint))
	count := 0
	for _, variable := range variables {
		if variable.Key == key {
			count++
			require.Equal(t, mapping.Instances[0].GetAddress(), variable.ValueAsString())
		}
	}
	require.Equal(t, 1, count)
}

func TestDependencyTestRejectsMissingContextBeforeInstalling(t *testing.T) {
	for _, missing := range []string{"logical endpoint", "mapping", "native instance"} {
		t.Run(missing, func(t *testing.T) {
			ctx := context.Background()
			runtime := dependencyTestRuntime(t)
			endpoint := &basev0.Endpoint{Module: "mod", Service: "api", Name: "http", Api: "http"}
			request := &runtimev0.InitRequest{RuntimeContext: resources.NewRuntimeContextNative()}
			if missing != "logical endpoint" {
				request.DependenciesEndpoints = []*basev0.Endpoint{endpoint}
			}
			if missing == "native instance" {
				request.DependenciesNetworkMappings = []*basev0.NetworkMapping{{Endpoint: endpoint}}
			}
			response, err := runtime.Init(ctx, request)
			require.NoError(t, err)
			require.Equal(t, runtimev0.InitStatus_READY, response.GetStatus().GetState())
			_, err = runtime.testEnvironment(ctx, "pure")
			require.NoError(t, err, "pure tests deliberately request no runtime dependencies")
			result, err := runtime.Test(ctx, &runtimev0.TestRequest{Suite: "unit"})
			require.NoError(t, err)
			require.Equal(t, runtimev0.TestStatus_ERROR, result.GetStatus().GetState())
			require.Contains(t, result.GetStatus().GetMessage(), "test dependency")
			_, err = os.Stat(filepath.Join(runtime.sourceLocation, "package-lock.json"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func dependencyTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	service := &resources.Service{Name: "frontend", Version: "0.0.0", ServiceDependencies: []*resources.ServiceDependency{
		{Module: "mod", Name: "api", Endpoints: []*resources.EndpointReference{{Name: "http"}}},
	}}
	require.NoError(t, service.SaveAtDir(ctx, filepath.Join(root, "mod", "frontend")))
	source := filepath.Join(root, "mod", "frontend", "code")
	require.NoError(t, os.MkdirAll(source, 0o755))
	for _, name := range []string{"package.json", "dependency.test.cjs"} {
		contents, err := os.ReadFile(filepath.Join("testdata", "runtime-dependency-context", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(source, name), contents, 0o644))
	}
	runtime := NewRuntime(NewService())
	env, err := environment.Proto()
	require.NoError(t, err)
	_, err = runtime.Load(ctx, &runtimev0.LoadRequest{Identity: identity, Environment: env, DisableCatch: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := runtime.Destroy(ctx, &runtimev0.DestroyRequest{})
		require.NoError(t, err)
	})
	return runtime
}

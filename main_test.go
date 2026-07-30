package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/network"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestAgentInformationAdvertisesValidationContract(t *testing.T) {
	info, err := NewService().GetAgentInformation(context.Background(), nil)
	require.NoError(t, err)
	message := info.ProtoReflect()
	validationField := message.Descriptor().Fields().ByName("validation")
	if validationField == nil {
		t.Skip("published Core pin predates validation advertisement")
	}
	validation := message.Get(validationField).Message()
	for _, operation := range []string{"lint", "compile", "audit", "artifact_build"} {
		field := validation.Descriptor().Fields().ByName(protoreflect.Name(operation))
		require.NotNil(t, field)
		supported := validation.Get(field).Message().Descriptor().Fields().ByName("supported")
		require.True(t, validation.Get(field).Message().Get(supported).Bool(), operation)
	}
	testField := validation.Descriptor().Fields().ByName("test")
	testValidation := validation.Get(testField).Message()
	suitesField := testValidation.Descriptor().Fields().ByName("suites")
	suites := testValidation.Get(suitesField).List()
	require.Equal(t, 5, suites.Len())
	unit := suites.Get(0).Message()
	require.Equal(t, "unit", unit.Get(unit.Descriptor().Fields().ByName("name")).String())
	require.True(t, unit.Get(unit.Descriptor().Fields().ByName("default_suite")).Bool())
}

func testIdentity(t *testing.T, tmpDir string) (*basev0.ServiceIdentity, *resources.Environment) {
	t.Helper()
	ctx := context.Background()

	workspace := &resources.Workspace{Name: "test"}

	service := &resources.Service{Name: "frontend", Version: "0.0.0"}
	err := service.SaveAtDir(ctx, path.Join(tmpDir, fmt.Sprintf("mod/%s", service.Name)))
	require.NoError(t, err)
	service.WithModule("mod")

	mod := &resources.Module{Name: "mod"}
	err = mod.SaveToDir(ctx, path.Join(tmpDir, "mod"))
	require.NoError(t, err)

	identity := &basev0.ServiceIdentity{
		Name:                service.Name,
		Version:             service.Version,
		Module:              "mod",
		Workspace:           workspace.Name,
		WorkspacePath:       tmpDir,
		RelativeToWorkspace: fmt.Sprintf("mod/%s", service.Name),
	}

	env := resources.LocalEnvironment()
	env.NamingScope = strconv.Itoa(time.Now().Second())

	return identity, env
}

func TestParseNPMTestOutputCombinesVitestAndNodeCounts(t *testing.T) {
	output := `
 Test Files  44 passed (44)
      Tests  320 passed (320)
ℹ tests 2
ℹ pass 2
ℹ fail 0
ℹ skipped 0
`
	run, passed, failed, skipped := parseNPMTestOutput(output)
	require.EqualValues(t, 322, run)
	require.EqualValues(t, 322, passed)
	require.Zero(t, failed)
	require.Zero(t, skipped)
}

func TestInitAppliesRequestedRuntimeContextBeforeNetworkValidation(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	identity, environment := testIdentity(t, tmpDir)

	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	_, err = builder.Create(ctx, &builderv0.CreateRequest{})
	require.NoError(t, err)

	runtime := NewRuntime(NewService())
	environmentProto, err := environment.Proto()
	require.NoError(t, err)
	_, err = runtime.Load(ctx, &runtimev0.LoadRequest{
		Identity:     identity,
		Environment:  environmentProto,
		DisableCatch: true,
	})
	require.NoError(t, err)

	response, err := runtime.Init(ctx, &runtimev0.InitRequest{
		RuntimeContext: resources.NewRuntimeContextContainer(),
	})
	require.NoError(t, err, "runtime failures are returned in the typed init status")
	require.Equal(t, runtimev0.InitStatus_ERROR, response.GetStatus().GetState())
	require.NotNil(t, runtime.Runtime.RuntimeContext)
	require.Equal(t, resources.RuntimeContextContainer, runtime.Runtime.RuntimeContext.Kind)
}

func TestNodeDependencyCacheKeyTracksDependencyInputsOnly(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(path.Join(source, "package.json"), []byte(`{"name":"frontend"}`), 0o644))
	require.NoError(t, os.WriteFile(path.Join(source, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o644))

	first, err := nodeDependencyCacheKey(source, "linux-arm64")
	require.NoError(t, err)
	require.Contains(t, first, "node-modules-")

	require.NoError(t, os.WriteFile(path.Join(source, "page.tsx"), []byte("export default 1"), 0o644))
	afterSourceEdit, err := nodeDependencyCacheKey(source, "linux-arm64")
	require.NoError(t, err)
	require.Equal(t, first, afterSourceEdit)

	require.NoError(t, os.WriteFile(path.Join(source, "package-lock.json"), []byte(`{"lockfileVersion":3,"changed":true}`), 0o644))
	afterLockEdit, err := nodeDependencyCacheKey(source, "linux-arm64")
	require.NoError(t, err)
	require.NotEqual(t, first, afterLockEdit)

	otherPlatform, err := nodeDependencyCacheKey(source, "linux-amd64")
	require.NoError(t, err)
	require.NotEqual(t, afterLockEdit, otherPlatform)
}

func TestNodeDependencyCacheKeyRequiresExecutionPlatform(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(path.Join(source, "package.json"), []byte(`{"name":"frontend"}`), 0o644))

	_, err := nodeDependencyCacheKey(source, "")
	require.ErrorContains(t, err, "execution platform is required")
}

func TestRuntimeImagePinsMultiArchitectureCompanion(t *testing.T) {
	require.Equal(t, "codeflydev/node", runtimeImage.Name)
	require.Equal(t, "0.0.13", runtimeImage.Tag)
}

func TestExecutionProfilesAreExplicitOutsideLocal(t *testing.T) {
	settings := &Settings{Mode: "ssr"}

	local, err := settings.ExecutionProfileFor("local")
	require.NoError(t, err)
	require.Equal(t, NextExecutionDevelopment, local)

	_, err = settings.ExecutionProfileFor("production")
	require.ErrorContains(t, err, "requires spec.execution-profiles.production")

	settings.ExecutionProfiles = map[string]string{
		"local":      "development",
		"production": "production",
	}
	production, err := settings.ExecutionProfileFor("production")
	require.NoError(t, err)
	require.Equal(t, NextExecutionProduction, production)

	settings.ExecutionProfiles["production"] = "prod"
	_, err = settings.ExecutionProfileFor("production")
	require.ErrorContains(t, err, "expected development or production")
}

func TestStaticProductionProfileFailsClosed(t *testing.T) {
	settings := &Settings{
		Mode: "static",
		ExecutionProfiles: map[string]string{
			"production": "production",
		},
	}
	_, err := settings.ExecutionProfileFor("production")
	require.ErrorContains(t, err, "requires mode ssr")
}

func TestParseNPMTestOutputHandlesColorizedVitestSummary(t *testing.T) {
	// Vitest colorizes its summary when attached to a TTY/pty, attaching ANSI
	// escapes to the count tokens (e.g. "384 passed\x1b[39m").
	output := "\x1b[32m Tests \x1b[39m \x1b[1m384 passed\x1b[22m\x1b[39m (384)\n"
	run, passed, failed, skipped := parseNPMTestOutput(output)
	require.EqualValues(t, 384, run)
	require.EqualValues(t, 384, passed)
	require.Zero(t, failed)
	require.Zero(t, skipped)
}

func TestParseNPMTestOutputHandlesNodeTAPSummary(t *testing.T) {
	// node --test defaults to the TAP reporter when stdout is not a TTY.
	output := `
# tests 2
# suites 0
# pass 2
# fail 0
# cancelled 0
# skipped 0
# todo 0
`
	run, passed, failed, skipped := parseNPMTestOutput(output)
	require.EqualValues(t, 2, run)
	require.EqualValues(t, 2, passed)
	require.Zero(t, failed)
	require.Zero(t, skipped)
}

func TestBuilderCreate(t *testing.T) {
	wool.SetGlobalLogLevel(wool.DEBUG)
	ctx := context.Background()

	tmpDir := t.TempDir()
	identity, _ := testIdentity(t, tmpDir)

	builder := NewBuilder(NewService())

	// Load in creation mode (no interactive prompts)
	resp, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Contains(t, resp.GetGettingStarted(), "composition root")

	// Create the service
	createResp, err := builder.Create(ctx, &builderv0.CreateRequest{})
	require.NoError(t, err)
	require.NotNil(t, createResp)

	// Verify key files were scaffolded
	serviceDir := path.Join(tmpDir, "mod/frontend")

	// Core Next.js files
	assertFileExists(t, serviceDir, "code/package.json")
	assertFileExists(t, serviceDir, "code/.gitignore")
	assertFileExists(t, serviceDir, "code/tsconfig.json")
	assertFileExists(t, serviceDir, "code/next.config.ts")
	assertFileExists(t, serviceDir, "code/vitest.config.ts")

	// App pages
	assertFileExists(t, serviceDir, "code/src/app/layout.tsx")
	assertFileExists(t, serviceDir, "code/src/app/page.tsx")
	assertFileExists(t, serviceDir, "code/src/app/not-found.tsx")
	assertFileExists(t, serviceDir, "code/src/app/dashboard/page.tsx")
	assertFileExists(t, serviceDir, "code/src/app/login/page.tsx")

	// Lib
	assertFileExists(t, serviceDir, "code/src/lib/providers.tsx")
	assertFileExists(t, serviceDir, "code/src/lib/utils.ts")
	assertFileExists(t, serviceDir, "code/src/lib/constants.ts")
	assertFileExists(t, serviceDir, "code/src/lib/transforms/index.ts")
	assertFileExists(t, serviceDir, "code/src/lib/hooks/index.ts")
	assertFileExists(t, serviceDir, "code/packages/README.md")

	// The generic agent exposes an additive workspace seam but no competing
	// product plugin contract, scanner, or side-effect registry. Applications
	// such as SaaS Starter own their public SDK and explicit composition root.
	assertFileDoesNotExist(t, serviceDir, "code/src/lib/framework/plugin.ts")
	assertFileDoesNotExist(t, serviceDir, "code/src/plugins/index.ts")
	providersContent, err := os.ReadFile(path.Join(serviceDir, "code/src/lib/providers.tsx"))
	require.NoError(t, err)
	require.NotContains(t, string(providersContent), `@/plugins`)

	// Stores
	assertFileExists(t, serviceDir, "code/src/stores/ui-store.ts")

	// Tests
	assertFileExists(t, serviceDir, "code/src/lib/__tests__/utils.test.ts")
	assertFileExists(t, serviceDir, "code/src/lib/__tests__/transforms.test.ts")

	// Verify template variable was replaced (not "base_replacement")
	layoutContent, err := os.ReadFile(path.Join(serviceDir, "code/src/app/layout.tsx"))
	require.NoError(t, err)
	require.NotContains(t, string(layoutContent), "base_replacement")
	require.Contains(t, string(layoutContent), "frontend")

	// Verify endpoints were created
	require.NotNil(t, builder.HttpEndpoint)

	// Verify settings defaults
	require.Equal(t, "ssr", builder.Settings.Mode)
	require.True(t, builder.Settings.HotReload)
	require.Equal(t, "development", builder.Settings.ExecutionProfiles["local"])
}

func TestBuilderSettingsDefaults(t *testing.T) {
	// Verify that non-communicate mode sets sensible defaults
	wool.SetGlobalLogLevel(wool.DEBUG)
	ctx := context.Background()

	tmpDir := t.TempDir()
	identity, _ := testIdentity(t, tmpDir)

	builder := NewBuilder(NewService())

	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)

	_, err = builder.Create(ctx, &builderv0.CreateRequest{})
	require.NoError(t, err)

	// Default is SSR mode with standalone output
	serviceDir := path.Join(tmpDir, "mod/frontend")
	configContent, err := os.ReadFile(path.Join(serviceDir, "code/next.config.ts"))
	require.NoError(t, err)
	require.Contains(t, string(configContent), `"standalone"`)
	require.NotContains(t, string(configContent), `"export"`)
}

func TestBuilderOptionsUseSingleChoiceProtocol(t *testing.T) {
	builder := NewBuilder(NewService())
	builder.Builder.CreationMode = &builderv0.CreationMode{Communicate: true}

	questions := builder.Options()
	require.Len(t, questions, 3)
	require.NotNil(t, questions[0].GetChoice(), "deployment mode must be a CLI-compatible single choice")
	require.Equal(t, "ssr", questions[0].GetChoice().GetDefaultOption())
	require.NotNil(t, questions[1].GetChoice(), "auth provider must be a CLI-compatible single choice")
	require.Equal(t, "none", questions[1].GetChoice().GetDefaultOption())
	require.NotNil(t, questions[2].GetConfirm())
	for _, question := range questions {
		require.Nil(t, question.GetSelection(), "creation flow must not use unsupported multi-select questions")
	}
}

// TestCreateToRun exercises the full lifecycle: Create → npm install → Load → Init → Start → Stop → Destroy.
// This test requires npm to be installed and takes ~30s. Use: go test -tags runner -run TestCreateToRun -v
func TestCreateToRun(t *testing.T) {
	if os.Getenv("CODEFLY_TEST_RUNNER") == "" {
		t.Skip("skipping lifecycle test (set CODEFLY_TEST_RUNNER=1 to enable)")
	}

	wool.SetGlobalLogLevel(wool.DEBUG)
	ctx := context.Background()

	tmpDir := t.TempDir()
	identity, env := testIdentity(t, tmpDir)

	// 1. Create
	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)

	_, err = builder.Create(ctx, &builderv0.CreateRequest{})
	require.NoError(t, err)

	// 2. npm install
	serviceDir := path.Join(tmpDir, "mod/frontend")
	codeDir := path.Join(serviceDir, "code")
	require.DirExists(t, codeDir)

	// 3. Runtime Load
	runtime := NewRuntime(NewService())
	envProto, err := env.Proto()
	require.NoError(t, err)
	_, err = runtime.Load(ctx, &runtimev0.LoadRequest{
		Identity:     identity,
		Environment:  envProto,
		DisableCatch: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(runtime.Endpoints))

	// 4. Init with network mappings
	workspace := &resources.Workspace{Name: "test"}
	networkManager, err := network.NewRuntimeManager(ctx, nil)
	require.NoError(t, err)
	networkManager.WithTemporaryPorts()

	networkMappings, err := networkManager.GenerateNetworkMappings(ctx, env, workspace, runtime.Identity, runtime.Endpoints, resources.NewRuntimeContextNative())
	require.NoError(t, err)
	require.Equal(t, 1, len(networkMappings))

	_, err = runtime.Init(ctx, &runtimev0.InitRequest{
		RuntimeContext:          resources.NewRuntimeContextNative(),
		ProposedNetworkMappings: networkMappings,
	})
	require.NoError(t, err)

	defer func() {
		_, _ = runtime.Stop(ctx, &runtimev0.StopRequest{})
		_, _ = runtime.Destroy(ctx, &runtimev0.DestroyRequest{})
	}()

	// 5. Start
	_, err = runtime.Start(ctx, &runtimev0.StartRequest{})
	require.NoError(t, err)

	// 6. Verify HTTP endpoint
	instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, networkMappings, runtime.HttpEndpoint, resources.NewNativeNetworkAccess())
	require.NoError(t, err)

	address := fmt.Sprintf("http://%s:%d", instance.Host, instance.Port)
	client := http.Client{Timeout: 2 * time.Second}

	var lastErr error
	for i := 0; i < 30; i++ {
		resp, err := client.Get(address)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return
	}
	t.Fatalf("service not ready after 30s: %v", lastErr)
}

func assertFileExists(t *testing.T, base string, rel string) {
	t.Helper()
	full := path.Join(base, rel)
	_, err := os.Stat(full)
	require.NoError(t, err, "expected file to exist: %s", rel)
}

func assertFileDoesNotExist(t *testing.T, base string, rel string) {
	t.Helper()
	full := path.Join(base, rel)
	_, err := os.Stat(full)
	require.ErrorIs(t, err, os.ErrNotExist, "expected file not to exist: %s", rel)
}

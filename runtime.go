package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"

	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/wool"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/runners/javascript"
)

type Runtime struct {
	services.RuntimeServer

	*Service

	// internal
	runnerEnvironment runners.RunnerEnvironment
	runner            runners.Proc
	workspaceConfigs  []*basev0.Configuration
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
}

// SetRuntimeContext resolves the runtime context by checking available
// toolchains, falling back to container mode when the preferred mode is
// unavailable. Mirrors the go-grpc/python pattern so mode selection is
// consistent across all three codefly-ecosystem runtimes.
func (s *Runtime) SetRuntimeContext(_ context.Context, runtimeContext *basev0.RuntimeContext) error {
	s.Runtime.RuntimeContext = setNextjsRuntimeContext(runtimeContext)
	return nil
}

// setNextjsRuntimeContext picks native when npm is on PATH, nix when the
// caller explicitly asked for it, container otherwise. Keeps the decision
// local to this agent — the generic runner context helpers live in
// core/runners/<lang> and there is no node-specific one yet.
func setNextjsRuntimeContext(runtimeContext *basev0.RuntimeContext) *basev0.RuntimeContext {
	if runtimeContext.Kind == resources.RuntimeContextNix {
		return resources.NewRuntimeContextNix()
	}
	if runtimeContext.Kind == resources.RuntimeContextFree || runtimeContext.Kind == resources.RuntimeContextNative {
		if _, err := exec.LookPath("npm"); err == nil {
			return resources.NewRuntimeContextNative()
		}
	}
	return resources.NewRuntimeContextContainer()
}

// CreateRunnerEnvironment dispatches by mode. Called from Init after the
// network + config wiring is done so network mappings are available for
// Docker port bindings.
func (s *Runtime) CreateRunnerEnvironment(ctx context.Context) error {
	// Resolve the runtime image: settings override takes priority, else
	// use the codefly-built default. Override rejects :latest to keep
	// builds reproducible.
	image := runtimeImage
	if override := s.Settings.RuntimeImage; override != "" {
		parsed, perr := resources.ParsePinnedImage(override)
		if perr != nil {
			return s.Wool.Wrapf(perr, "invalid docker-image override in service.codefly.yaml")
		}
		s.Wool.Info("using docker-image override (not recommended)", wool.Field("image", parsed.FullName()))
		image = parsed
	}

	switch {
	case s.Runtime.IsContainerRuntime():
		dockerEnv, err := runners.NewDockerEnvironment(ctx, image, s.sourceLocation, s.UniqueWithWorkspace())
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker runner environment")
		}
		dockerEnv.WithPause()
		// Bind the HTTP endpoint's container port to the host so the
		// browser can reach `next dev` inside the container.
		instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
		if err == nil && instance != nil {
			dockerEnv.WithPort(ctx, uint16(instance.Port))
		}
		s.runnerEnvironment = dockerEnv
	case s.Runtime.IsNixRuntime():
		nixEnv, err := runners.NewNixEnvironment(ctx, s.sourceLocation)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create nix runner environment")
		}
		s.runnerEnvironment = nixEnv
	default:
		nativeEnv, err := runners.NewNativeEnvironment(ctx, s.sourceLocation)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create native runner environment")
		}
		s.runnerEnvironment = nativeEnv
	}

	allEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Wool.Wrapf(err, "cannot get environment variables")
	}
	s.runnerEnvironment.WithEnvironmentVariables(ctx, allEnvs...)
	return nil
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading base")
	}

	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if req.DisableCatch {
		s.Wool.DisableCatch()
	}

	s.Runtime.SetEnvironment(req.Environment)

	s.sourceLocation, err = s.LocalDirCreate(ctx, "%s", s.Settings.NodeSourceDir())
	if err != nil {
		return s.Runtime.LoadErrorf(err, "creating source location")
	}

	requirements.Localize(s.Location)

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading endpoints")
	}

	s.HttpEndpoint, err = resources.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "finding http endpoint")
	}

	// Register agent commands
	s.registerCommands()

	return s.Runtime.LoadResponse()
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogInitRequest(req)

	s.NetworkMappings = req.ProposedNetworkMappings

	// Networking
	net, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.Infof("HTTP will run on %s", net.Address)

	nm, err := resources.FindNetworkMapping(ctx, s.NetworkMappings, s.HttpEndpoint)
	if err != nil {
		return s.Runtime.InitError(err)
	}
	err = s.EnvironmentVariables.AddEndpoints(ctx, []*basev0.NetworkMapping{nm}, resources.NewNativeNetworkAccess())
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Workspace configurations (e.g. WorkOS API keys)
	s.workspaceConfigs = req.WorkspaceConfigurations
	err = s.EnvironmentVariables.AddConfigurations(ctx, req.WorkspaceConfigurations...)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Dependencies configurations
	confs := resources.FilterConfigurations(req.DependenciesConfigurations, resources.NewRuntimeContextNative())
	err = s.EnvironmentVariables.AddConfigurations(ctx, confs...)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Dispatch the runner environment by mode (native / docker / nix).
	// Mirrors the pattern already used by go-grpc and python-fastapi so a
	// plugin's mode is the single control point for where every spawn —
	// dev server, tests, Playwright, screenshot, cmdRoutes — actually runs.
	if err := s.CreateRunnerEnvironment(ctx); err != nil {
		return s.Runtime.InitError(err)
	}

	if err := s.runnerEnvironment.Init(ctx); err != nil {
		return s.Runtime.InitError(err)
	}

	if s.Settings.HotReload {
		dependencies := requirements.Clone()
		dependencies.Localize(s.Location)
		conf := services.NewWatchConfiguration(dependencies)
		err = s.SetupWatcher(ctx, conf, s.EventHandler)
		if err != nil {
			s.Wool.Warn("error in watcher", wool.ErrField(err))
		}
	}

	return s.Runtime.InitResponse()
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if s.Settings.IsStatic() {
		s.Wool.Forwardf("starting Next.js dev server (static mode)...")
	} else {
		s.Wool.Forwardf("starting Next.js dev server (SSR mode)...")
	}

	// Stop existing runner
	if s.runner != nil {
		err := s.runner.Stop(ctx)
		if err != nil {
			return s.Runtime.StartError(err)
		}
	}

	// Get port
	net, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
	if err != nil {
		return s.Runtime.StartError(err)
	}

	// Add dependency network mappings so the frontend can reach backend services
	err = s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings, resources.NewNativeNetworkAccess())
	if err != nil {
		return s.Runtime.StartError(err)
	}

	// Forward fixture env var so the FE can serve fixture data in dev mode
	s.Wool.Debug("setting fixture", wool.Field("fixture", req.Fixture))
	s.EnvironmentVariables.SetFixture(req.Fixture)

	// Collect NEXT_PUBLIC_ env vars for browser-accessible dependency endpoints
	var browserEnvs []*resources.EnvironmentVariable
	for _, mapping := range req.DependenciesNetworkMappings {
		ep := mapping.Endpoint
		if ep.Api == "rest" || ep.Api == "http" || ep.Api == "connect" {
			instance := resources.FilterNetworkInstance(ctx, mapping.Instances, resources.NewNativeNetworkAccess())
			if instance != nil {
				envName := fmt.Sprintf("NEXT_PUBLIC_%s_%s", strings.ToUpper(ep.Service), strings.ToUpper(ep.Api))
				s.Wool.Debug("injecting browser env", wool.Field("name", envName), wool.Field("address", instance.Address))
				browserEnvs = append(browserEnvs, resources.Env(envName, instance.Address))
			}
		}
	}

	// Map workspace configuration values to NEXT_PUBLIC_ browser env vars.
	// E.g., workos config with CLIENT_ID → NEXT_PUBLIC_WORKOS_CLIENT_ID
	for _, conf := range s.workspaceConfigs {
		for _, info := range conf.Infos {
			for _, val := range info.ConfigurationValues {
				if val.Secret {
					continue // Never expose secrets to the browser
				}
				// Only forward vars that start with the config name in uppercase
				// e.g., WORKOS_CLIENT_ID from the "workos" config
				prefix := strings.ToUpper(info.Name) + "_"
				if strings.HasPrefix(val.Key, prefix) || val.Key == "AUTH_PROVIDER" {
					envName := fmt.Sprintf("NEXT_PUBLIC_%s", val.Key)
					s.Wool.Debug("injecting workspace browser env", wool.Field("name", envName))
					browserEnvs = append(browserEnvs, resources.Env(envName, val.Value))
				}
			}
		}
	}

	// Run npm run dev with the assigned port
	proc, err := s.runnerEnvironment.NewProcess("npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", net.Port))
	if err != nil {
		return s.Runtime.StartErrorf(err, "cannot create npm process")
	}

	allEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.StartErrorf(err, "getting environment variables")
	}
	proc.WithEnvironmentVariables(ctx, allEnvs...)
	// Add NEXT_PUBLIC_ browser env vars
	proc.WithEnvironmentVariables(ctx, browserEnvs...)
	// Cap process fan-out. Next.js dev mode otherwise spawns jest-worker
	// pools for SWC transform + type-check, a webpack worker pool, and
	// node's libuv threadpool — legitimate in prod, but under a multi-
	// service dev stack this multiplies into hundreds of forks and has
	// been observed driving macOS past kern.maxprocperuid during
	// `codefly run` startup. NEXT_PRIVATE_WORKER=1 (previously set here)
	// is not a real Next.js knob and does nothing — the real caps are
	// UV_THREADPOOL_SIZE (libuv) and the experimental.cpus / workerThreads
	// options baked into next.config.ts.
	proc.WithEnvironmentVariables(ctx,
		resources.Env("UV_THREADPOOL_SIZE", "2"),
		resources.Env("NODE_OPTIONS", "--max-old-space-size=2048"),
		resources.Env("NEXT_TELEMETRY_DISABLED", "1"),
	)
	proc.WithOutput(s.Logger)

	s.runner = proc
	runningContext := s.Wool.Inject(context.Background())
	err = s.runner.Start(runningContext)
	if err != nil {
		return s.Runtime.StartErrorf(err, "starting next.js dev server")
	}

	// Wait for ready
	err = s.WaitForReady(ctx, net)
	if err != nil {
		// The dev server was Started above; stop it before bailing so a
		// readiness timeout doesn't leave an orphaned next.js process
		// holding the port.
		_ = s.runner.Stop(ctx)
		s.runner = nil
		return s.Runtime.StartError(err)
	}

	s.Wool.Forwardf("Next.js dev server running on port %d", net.Port)

	return s.Runtime.StartResponse()
}

func (s *Runtime) WaitForReady(ctx context.Context, net *basev0.NetworkInstance) error {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	address := net.Address
	s.Wool.Debug("waiting for Next.js to be ready", wool.Field("address", address))

	maxRetry := 30
	for retry := 0; retry < maxRetry; retry++ {
		resp, err := http.Get(address)
		if err == nil {
			resp.Body.Close()
			s.Wool.Debug("Next.js is ready!")
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return s.Wool.NewError("Next.js is not ready after 30 seconds")
}

func (s *Runtime) Information(ctx context.Context, req *runtimev0.InformationRequest) (*runtimev0.InformationResponse, error) {
	return s.Runtime.InformationResponse(ctx, req)
}

func (s *Runtime) Stop(ctx context.Context, req *runtimev0.StopRequest) (*runtimev0.StopResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if s.runner != nil {
		err := s.runner.Stop(ctx)
		if err != nil {
			return s.Runtime.StopError(err)
		}
	}

	if s.Watcher != nil {
		s.Watcher.Pause()
	}
	if s.Events != nil {
		close(s.Events)
		s.Events = nil
	}

	return s.Runtime.StopResponse()
}

func (s *Runtime) Destroy(ctx context.Context, req *runtimev0.DestroyRequest) (*runtimev0.DestroyResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	// Destroy was a no-op, so the runner environment was never torn down: in
	// container mode it leaked a paused `sleep infinity` container, and a
	// Destroy without a preceding Stop leaked the whole node process tree.
	// Shutdown stops AND removes all resources.
	if s.Watcher != nil {
		s.Watcher.Pause()
	}
	if s.Events != nil {
		close(s.Events)
		s.Events = nil
	}
	if s.runner != nil {
		_ = s.runner.Stop(ctx)
		s.runner = nil
	}
	if s.runnerEnvironment != nil {
		if err := s.runnerEnvironment.Shutdown(ctx); err != nil {
			return s.Runtime.DestroyError(err)
		}
		s.runnerEnvironment = nil
	}

	return s.Runtime.DestroyResponse()
}

func (s *Runtime) Test(ctx context.Context, req *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	// Map suite → npm script + runner kind. Default = "test" (vitest
	// unit tests). "e2e" expects a "test:e2e" script wired to
	// Playwright in package.json.
	npmScript := "test"
	runnerKind := "vitest"
	switch req.Suite {
	case "", "unit":
		npmScript = "test"
		runnerKind = "vitest"
	case "e2e":
		npmScript = "test:e2e"
		runnerKind = "playwright"
	case "integration":
		npmScript = "test:integration"
		runnerKind = "vitest"
	case "smoke":
		npmScript = "test:smoke"
		runnerKind = "vitest"
	default:
		npmScript = "test:" + req.Suite
		runnerKind = "vitest"
	}

	// Allocate a JSON output file under the project's .codefly cache.
	// Both vitest and playwright support writing JSON to disk via a
	// flag; capturing stdout would tangle with the runner's own
	// progress prints + coverage summary.
	cacheDir := filepath.Join(s.Service.sourceLocation, ".codefly", "test-output")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return s.Runtime.TestErrorf(err, "creating test cache dir")
	}
	jsonFile := filepath.Join(cacheDir, fmt.Sprintf("test-%d.json", time.Now().UnixNano()))
	defer os.Remove(jsonFile)

	// Args before `--` go to npm; args after go to the test runner.
	args := []string{"run", npmScript, "--"}

	// Reporter + output file. Playwright's flag is --output, not
	// --outputFile (jest/vitest convention). We thread JSON to disk
	// and parse from there.
	args = append(args, "--reporter=json")
	switch runnerKind {
	case "playwright":
		args = append(args, "--output="+jsonFile)
	default:
		args = append(args, "--outputFile="+jsonFile)
	}

	// Filter pattern. Vitest uses --testNamePattern (regex),
	// Playwright uses --grep.
	if pat := combineRegex(req.Filters); pat != "" {
		switch runnerKind {
		case "playwright":
			args = append(args, "--grep", pat)
		default:
			args = append(args, "--testNamePattern", pat)
		}
	}

	// Back-compat: target field still maps to a name pattern when
	// filters are not supplied (older clients).
	if req.Target != "" && len(req.Filters) == 0 {
		switch runnerKind {
		case "playwright":
			args = append(args, "--grep", req.Target)
		default:
			args = append(args, "--testNamePattern", req.Target)
		}
	}

	if req.Coverage {
		args = append(args, "--coverage")
	}

	args = append(args, req.ExtraArgs...)

	s.Wool.Info("running frontend tests",
		wool.Field("suite", req.Suite),
		wool.Field("runner", runnerKind),
		wool.Field("script", npmScript),
		wool.Field("args", args))

	testProc, err := s.runnerEnvironment.NewProcess("npm", args...)
	if err != nil {
		return s.Runtime.TestErrorf(err, "cannot create test process")
	}

	testEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.TestErrorf(err, "getting environment variables")
	}
	testProc.WithEnvironmentVariables(ctx, testEnvs...)
	testProc.WithOutput(s.Logger)

	started := time.Now()
	runErr := testProc.Run(ctx)
	duration := time.Since(started)

	// Read + parse the JSON regardless of runErr. A failed test run
	// produces non-zero exit code AND a complete JSON file; the
	// structured response carries the per-case detail.
	jsonBytes, _ := os.ReadFile(jsonFile) //nolint:gosec // path under sourceDir
	var run *javascript.StructuredTestRun
	switch runnerKind {
	case "playwright":
		run = javascript.ParsePlaywrightJSON(string(jsonBytes))
	default:
		run = javascript.ParseJestVitestJSON(string(jsonBytes), 0)
	}

	if run == nil || (len(run.Suites) == 0 && runErr != nil) {
		// Runner crashed before producing JSON — surface the raw
		// error rather than an empty structured response.
		return s.Runtime.TestErrorf(runErr, "test runner failed before producing JSON output")
	}

	s.Wool.Forwardf("Tests: %s", run.LegacyTestSummary().SummaryLine())
	return run.ToProtoResponse(runnerKind, req.Suite, duration), runErr
}

// combineRegex joins multiple filter patterns into a single OR-regex
// suitable for vitest --testNamePattern, jest --testNamePattern, or
// playwright --grep. Returns "" when no patterns are given so callers
// can omit the flag entirely.
func combineRegex(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	if len(patterns) == 1 {
		return patterns[0]
	}
	return "(" + strings.Join(patterns, "|") + ")"
}

// parseVitestOutput extracts test counts from vitest output.
func parseVitestOutput(output string) (run, passed, failed, skipped int32) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Tests") && (strings.Contains(line, "passed") || strings.Contains(line, "failed")) {
			// Vitest format: "Tests  4 passed (4)"
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "passed" && i > 0 {
					fmt.Sscanf(parts[i-1], "%d", &passed)
				}
				if part == "failed" && i > 0 {
					fmt.Sscanf(parts[i-1], "%d", &failed)
				}
				if part == "skipped" && i > 0 {
					fmt.Sscanf(parts[i-1], "%d", &skipped)
				}
			}
		}
	}
	run = passed + failed + skipped
	return
}

/* Details */

func (s *Runtime) EventHandler(event code.Change) error {
	s.Wool.Info("detected change requiring re-start", wool.Field("path", event.Path))
	s.Runtime.DesiredStart()
	return nil
}

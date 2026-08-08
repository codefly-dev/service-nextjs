package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/llmout"
	"github.com/codefly-dev/core/wool"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	dockerrun "github.com/codefly-dev/core/runners/dockerrun"
	"github.com/codefly-dev/core/runners/javascript"
)

type Runtime struct {
	services.RuntimeServer

	*Service

	// internal
	runnerEnvironment runners.RunnerEnvironment
	runner            runners.Proc
	workspaceConfigs  []*basev0.Configuration
	dependenciesMu    sync.Mutex
	executionProfile  NextExecutionProfile
	readinessTimeout  time.Duration
	packageManifest   *nodePackageManifest
	projectKind       nodeProjectKind
}

func NewRuntime(service *Service) *Runtime {
	return &Runtime{Service: service}
}

// SetRuntimeContext resolves the runtime context by checking available
// toolchains, falling back to container mode when the preferred mode is
// unavailable. Mirrors the go-grpc/python pattern so mode selection is
// consistent across all three codefly-ecosystem runtimes.
func (s *Runtime) SetRuntimeContext(_ context.Context, runtimeContext *basev0.RuntimeContext) error {
	s.Runtime.RuntimeContext = setNextjsRuntimeContext(runtimeContext)
	return nil
}

// setNextjsRuntimeContext resolves the runtime mode. An explicit Nix or
// Container request is honored as-is. Otherwise (Native / Free / nil — the
// AUTO case) it picks the best available environment: run LOCAL if it can,
// then NIX, then DOCKER. Local wins when the npm toolchain is on PATH; nix
// when the nix CLI is available; container is the universal fallback.
//
// A nil argument is treated as AUTO so a missing SetRuntimeContext call never
// panics here — an agent must never panic on host input.
func setNextjsRuntimeContext(runtimeContext *basev0.RuntimeContext) *basev0.RuntimeContext {
	kind := resources.RuntimeContextFree
	if runtimeContext != nil {
		kind = runtimeContext.Kind
	}
	switch kind {
	case resources.RuntimeContextNix:
		return resources.NewRuntimeContextNix()
	case resources.RuntimeContextContainer:
		return resources.NewRuntimeContextContainer()
	}
	// AUTO: local → nix → docker.
	if _, err := exec.LookPath("npm"); err == nil {
		return resources.NewRuntimeContextNative()
	}
	if _, err := exec.LookPath("nix"); err == nil {
		return resources.NewRuntimeContextNix()
	}
	return resources.NewRuntimeContextContainer()
}

// CreateRunnerEnvironment dispatches by mode. Called from Init after the
// network + config wiring is done so network mappings are available for
// Docker port bindings.
func (s *Runtime) CreateRunnerEnvironment(ctx context.Context) error {
	// AUTO fallback: if the host never delivered a runtime context (no
	// SetRuntimeContext call), resolve one now — local → nix → docker — rather
	// than letting the mode dispatch below nil-dereference. An agent must never
	// panic on missing host input.
	if s.Runtime.RuntimeContext == nil {
		s.Runtime.RuntimeContext = setNextjsRuntimeContext(nil)
		s.Wool.Info("no runtime context provided — auto-resolved",
			wool.Field("mode", s.Runtime.RuntimeContext.Kind))
	}

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
		// Mount the Codefly service root, then execute from the configured
		// source directory. Frontend tooling may legitimately consume service
		// metadata beside the source tree (for example service.codefly.yaml);
		// mounting only sourceLocation makes native and container behavior
		// differ. Both paths come from the loaded Codefly service definition,
		// never from plugin-specific parent-directory assumptions.
		dockerEnv, err := dockerrun.NewDockerEnvironment(ctx, image, s.Location, s.UniqueWithWorkspace())
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker runner environment")
		}
		dockerEnv.WithWorkDir(s.sourceLocation)
		if s.projectKind == nodeProjectNextJS {
			// The source bind mount is intentionally shared so edits remain visible
			// to Next.js. Its build/runtime state is not source: sharing `.next`
			// between independently namespaced runs causes their dev-server locks,
			// caches, and generated files to collide. Core owns and scopes the host
			// cache path; this plugin only declares the mutable container target.
			if _, err := dockerEnv.WithPersistentCacheMount(
				ctx,
				"next-build",
				filepath.Join(s.sourceLocation, ".next"),
			); err != nil {
				return s.Wool.Wrapf(err, "cannot isolate Next.js runtime state")
			}
		}
		// Lockfiles do not fully identify node_modules: optional native
		// packages such as esbuild and lightningcss are selected for the
		// execution platform. Never mount an amd64 cache into an arm64
		// container running the same checkout.
		dependencyCacheKey, err := nodeDependencyCacheKey(
			s.sourceLocation,
			"linux-"+runtime.GOARCH,
		)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot fingerprint Node dependencies")
		}
		if _, err := dockerEnv.WithPersistentCacheMount(
			ctx,
			dependencyCacheKey,
			filepath.Join(s.sourceLocation, "node_modules"),
		); err != nil {
			return s.Wool.Wrapf(err, "cannot isolate Node dependencies")
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
		// Provision the devShell (nodejs) when the project doesn't ship a
		// flake.nix, so NewNixEnvironment has something to materialize.
		if err := ensureNixFlake(s.sourceLocation); err != nil {
			return s.Wool.Wrapf(err, "cannot provision nix flake")
		}
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
	s.packageManifest, err = readNodePackageManifest(s.sourceLocation)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading Node.js package manifest")
	}
	s.projectKind = s.packageManifest.projectKind()
	if s.projectKind == nodeProjectNextJS {
		s.executionProfile, err = s.Settings.ExecutionProfileFor(req.GetEnvironment().GetName())
		if err != nil {
			return s.Runtime.LoadErrorf(err, "resolving Next.js execution profile")
		}
		s.readinessTimeout, err = s.Settings.ReadinessTimeoutFor(s.executionProfile)
		if err != nil {
			return s.Runtime.LoadErrorf(err, "resolving Next.js readiness timeout")
		}
		s.Wool.Info(
			"resolved Next.js execution profile",
			wool.Field("environment", req.GetEnvironment().GetName()),
			wool.Field("profile", s.executionProfile),
			wool.Field("readiness_timeout", s.readinessTimeout),
		)
	} else {
		// Generic Node.js packages use this agent for typed Code/Runtime
		// capabilities, but have no Next.js server lifecycle to configure.
		s.executionProfile = NextExecutionDevelopment
		s.readinessTimeout = developmentReadinessTimeout
		s.Wool.Info(
			"resolved generic Node.js execution profile",
			wool.Field("environment", req.GetEnvironment().GetName()),
			wool.Field("profile", s.projectKind),
		)
	}

	requirements.Localize(s.Location)

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading endpoints")
	}

	s.HttpEndpoint, err = resources.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		// Source-only checkouts can use Code/Tooling without declaring a
		// runnable HTTP endpoint. Init remains responsible for rejecting a
		// missing endpoint when the application lifecycle is requested.
		s.Wool.Debug("no HTTP endpoint found", wool.ErrField(err))
		s.HttpEndpoint = nil
	}

	// Register agent commands
	s.registerCommands()

	return s.Runtime.LoadResponse()
}

// dropNilConfigs returns the configurations with any nil entries removed. The
// runtime/init protos come from another process; a nil element must be skipped, not
// dereferenced — an agent must never panic on the shape of its inputs.
func dropNilConfigs(in []*basev0.Configuration) []*basev0.Configuration {
	out := make([]*basev0.Configuration, 0, len(in))
	for _, c := range in {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogInitRequest(req)

	if err := s.SetRuntimeContext(ctx, req.GetRuntimeContext()); err != nil {
		return s.Runtime.InitErrorf(err, "cannot set runtime context")
	}

	s.NetworkMappings = req.ProposedNetworkMappings

	// Source-only Node.js/TypeScript packages legitimately expose no HTTP
	// endpoint. Their typed test/build/lint capabilities still need a fully
	// initialized runner; the application Start boundary below remains
	// responsible for rejecting an absent endpoint.
	if s.HttpEndpoint != nil {
		net, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
		if err != nil {
			return s.Runtime.InitError(err)
		}

		s.Infof("HTTP will run on %s", net.Address)

		nm, err := resources.FindNetworkMapping(ctx, s.NetworkMappings, s.HttpEndpoint)
		if err != nil {
			return s.Runtime.InitError(err)
		}
		// A (nil, no-error) mapping must fail gracefully, never be wrapped into a
		// `[]{nil}` that derefs downstream (an agent must never panic).
		if nm == nil {
			return s.Runtime.InitError(fmt.Errorf("no network mapping resolved for the http endpoint"))
		}
		if err := s.EnvironmentVariables.AddEndpoints(ctx, []*basev0.NetworkMapping{nm}, resources.NewNativeNetworkAccess()); err != nil {
			return s.Runtime.InitError(err)
		}
	}

	// Workspace configurations (e.g. WorkOS API keys). Drop nil entries before the
	// env-var assembly — a nil from upstream must never panic the agent.
	s.workspaceConfigs = dropNilConfigs(req.WorkspaceConfigurations)
	if err := s.EnvironmentVariables.AddConfigurations(ctx, s.workspaceConfigs...); err != nil {
		return s.Runtime.InitError(err)
	}

	// Dependencies configurations (from saas/api & friends) — same nil discipline.
	confs := resources.FilterConfigurations(dropNilConfigs(req.DependenciesConfigurations), resources.NewRuntimeContextNative())
	if err := s.EnvironmentVariables.AddConfigurations(ctx, confs...); err != nil {
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

	// Dependency installation is an agent concern. A fresh CI checkout and a
	// newly generated service must not need provider-specific `npm ci` steps.
	if err := s.ensureNodeDependencies(ctx); err != nil {
		return s.Runtime.InitError(err)
	}

	if s.Settings.HotReload && s.executionProfile == NextExecutionDevelopment {
		dependencies := requirements.Clone()
		dependencies.Localize(s.Location)
		conf := services.NewWatchConfiguration(dependencies)
		if err := s.SetupWatcher(ctx, conf, s.EventHandler); err != nil {
			s.Wool.Warn("error in watcher", wool.ErrField(err))
		}
	}

	return s.Runtime.InitResponse()
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	if s.HttpEndpoint == nil {
		return s.Runtime.StartError(fmt.Errorf("Node.js application start requires an HTTP endpoint"))
	}

	s.Wool.Forwardf(
		"starting Next.js %s server (%s mode)...",
		s.executionProfile,
		s.Settings.Mode,
	)

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

	// Add per-service runtime overrides (--set <service>:KEY=VAL)
	s.EnvironmentVariables.AddOverrides(req.GetOverrides())

	// Collect NEXT_PUBLIC_ env vars for browser-accessible dependency endpoints
	var browserEnvs []*resources.EnvironmentVariable
	for _, mapping := range req.DependenciesNetworkMappings {
		// Never trust the shape of a proto we didn't build: a nil mapping or a
		// mapping with no endpoint must SKIP, never deref (an agent must never panic).
		if mapping == nil || mapping.Endpoint == nil {
			continue
		}
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
		if conf == nil {
			continue
		}
		for _, info := range conf.Infos {
			if info == nil {
				continue
			}
			for _, val := range info.ConfigurationValues {
				if val == nil {
					continue
				}
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

	allEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.StartErrorf(err, "getting environment variables")
	}
	commonRuntimeEnvs := []*resources.EnvironmentVariable{
		resources.Env("UV_THREADPOOL_SIZE", "2"),
		resources.Env("NODE_OPTIONS", "--max-old-space-size=2048"),
		resources.Env("NEXT_TELEMETRY_DISABLED", "1"),
	}
	if s.executionProfile == NextExecutionProduction {
		commonRuntimeEnvs = append(commonRuntimeEnvs, resources.Env("NODE_ENV", "production"))
		build, buildErr := s.runnerEnvironment.NewProcess("npm", "run", "build")
		if buildErr != nil {
			return s.Runtime.StartErrorf(buildErr, "cannot create Next.js production build process")
		}
		build.WithEnvironmentVariables(ctx, allEnvs...)
		build.WithEnvironmentVariables(ctx, browserEnvs...)
		build.WithEnvironmentVariables(ctx, commonRuntimeEnvs...)
		build.WithOutput(s.Logger)
		s.Wool.Forwardf("building immutable Next.js production output...")
		if buildErr = build.Run(ctx); buildErr != nil {
			return s.Runtime.StartErrorf(buildErr, "building Next.js production output")
		}
	}

	command := "npm"
	commandArgs := []string{
		"run",
		"dev",
		"--",
		"-p",
		fmt.Sprintf("%d", net.Port),
	}
	if s.executionProfile == NextExecutionProduction {
		launch, launchErr := prepareProductionServer(s.sourceLocation, net.Port)
		if launchErr != nil {
			return s.Runtime.StartErrorf(launchErr, "preparing Next.js production server")
		}
		command = launch.command
		commandArgs = launch.args
		commonRuntimeEnvs = append(commonRuntimeEnvs, launch.environment...)
	}
	proc, err := s.runnerEnvironment.NewProcess(command, commandArgs...)
	if err != nil {
		return s.Runtime.StartErrorf(err, "cannot create npm process")
	}
	proc.WithEnvironmentVariables(ctx, allEnvs...)
	proc.WithEnvironmentVariables(ctx, browserEnvs...)
	// Cap process fan-out. Next.js development otherwise spawns jest-worker
	// pools for SWC transform + type-check, a webpack worker pool, and
	// node's libuv threadpool. The same bounds keep local production builds
	// from exhausting a multi-service workstation.
	proc.WithEnvironmentVariables(ctx, commonRuntimeEnvs...)
	proc.WithOutput(s.Logger)

	s.runner = proc
	runningContext := s.Wool.Inject(context.Background())
	err = s.runner.Start(runningContext)
	if err != nil {
		return s.Runtime.StartErrorf(err, "starting Next.js %s server", s.executionProfile)
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

	s.Wool.Forwardf("Next.js %s server running on port %d", s.executionProfile, net.Port)

	return s.Runtime.StartResponse()
}

type productionServerLaunch struct {
	command     string
	args        []string
	environment []*resources.EnvironmentVariable
}

// prepareProductionServer selects the runtime that matches the build output.
// `next start` explicitly rejects `output: "standalone"` projects. For those
// projects Next emits a self-contained server, but its static and public
// assets still need to be staged beside that server just as the deployment
// Dockerfile does.
func prepareProductionServer(sourceLocation string, port uint32) (productionServerLaunch, error) {
	fallback := productionServerLaunch{
		command: "npm",
		args: []string{
			"run",
			"start",
			"--",
			"-p",
			fmt.Sprintf("%d", port),
		},
	}

	manifestPath := filepath.Join(sourceLocation, ".next", "required-server-files.json")
	content, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return fallback, nil
	}
	if err != nil {
		return productionServerLaunch{}, fmt.Errorf("read Next.js build manifest: %w", err)
	}
	var manifest struct {
		Config struct {
			Output string `json:"output"`
		} `json:"config"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return productionServerLaunch{}, fmt.Errorf("parse Next.js build manifest: %w", err)
	}
	if manifest.Config.Output != "standalone" {
		return fallback, nil
	}

	standaloneRoot := filepath.Join(sourceLocation, ".next", "standalone")
	serverPath := filepath.Join(standaloneRoot, "server.js")
	if _, err := os.Stat(serverPath); err != nil {
		return productionServerLaunch{}, fmt.Errorf("standalone build is missing server.js: %w", err)
	}
	for _, assets := range []struct {
		source      string
		destination string
	}{
		{
			source:      filepath.Join(sourceLocation, "public"),
			destination: filepath.Join(standaloneRoot, "public"),
		},
		{
			source:      filepath.Join(sourceLocation, ".next", "static"),
			destination: filepath.Join(standaloneRoot, ".next", "static"),
		},
	} {
		if err := stageGeneratedAssets(assets.source, assets.destination); err != nil {
			return productionServerLaunch{}, err
		}
	}

	return productionServerLaunch{
		command: "node",
		args:    []string{filepath.ToSlash(filepath.Join(".next", "standalone", "server.js"))},
		environment: []*resources.EnvironmentVariable{
			resources.Env("PORT", fmt.Sprintf("%d", port)),
			resources.Env("HOSTNAME", "0.0.0.0"),
		},
	}, nil
}

func stageGeneratedAssets(source, destination string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect Next.js production assets %s: %w", source, err)
	}
	if err := os.RemoveAll(destination); err != nil {
		return fmt.Errorf("replace staged Next.js production assets %s: %w", destination, err)
	}
	if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
		return fmt.Errorf("stage Next.js production assets from %s: %w", source, err)
	}
	return nil
}

func (s *Runtime) WaitForReady(ctx context.Context, net *basev0.NetworkInstance) error {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	address := net.Address
	timeout := s.readinessTimeout
	if timeout <= 0 {
		var err error
		timeout, err = s.Settings.ReadinessTimeoutFor(s.executionProfile)
		if err != nil {
			return s.Wool.Wrapf(err, "resolving Next.js readiness timeout")
		}
	}
	s.Wool.Debug(
		"waiting for Next.js to be ready",
		wool.Field("address", address),
		wool.Field("timeout", timeout),
	)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHTTPReady(ctx, client, address, timeout, 250*time.Millisecond); err != nil {
		return s.Wool.Wrapf(err, "Next.js readiness probe failed")
	}
	s.Wool.Debug("Next.js is ready!")
	return nil
}

func waitForHTTPReady(
	ctx context.Context,
	client *http.Client,
	address string,
	timeout time.Duration,
	pollInterval time.Duration,
) error {
	if client == nil {
		return errors.New("readiness HTTP client is required")
	}
	if timeout <= 0 {
		return errors.New("readiness timeout must be greater than zero")
	}
	if pollInterval <= 0 {
		return errors.New("readiness poll interval must be greater than zero")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return fmt.Errorf("build readiness request: %w", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			return nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness canceled: %w", ctx.Err())
		case <-timer.C:
			return fmt.Errorf("not ready after %s (last probe: %v)", timeout, lastErr)
		case <-ticker.C:
		}
	}
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

	// Cancel the watcher and let its Start goroutine's deferred close of Events
	// run exactly once — Stop/Destroy must not close Events itself, or it races
	// that goroutine into a "close of closed channel" panic.
	s.Base.StopWatcher()

	return s.Runtime.StopResponse()
}

func (s *Runtime) Destroy(ctx context.Context, req *runtimev0.DestroyRequest) (*runtimev0.DestroyResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	// Destroy was a no-op, so the runner environment was never torn down: in
	// container mode it leaked a paused `sleep infinity` container, and a
	// Destroy without a preceding Stop leaked the whole node process tree.
	// Shutdown stops AND removes all resources.
	// Cancel the watcher and let its Start goroutine's deferred close of Events
	// run exactly once — Stop/Destroy must not close Events itself, or it races
	// that goroutine into a "close of closed channel" panic.
	s.Base.StopWatcher()
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
	if req == nil {
		req = &runtimev0.TestRequest{}
	}

	// Map suite to the package-owned npm script, then derive the runner from
	// package.json. Source workspaces route every Node/TypeScript code unit
	// through this plugin; assuming every package is Next.js + Vitest corrupts
	// otherwise valid Jest, Playwright, and composite npm test commands.
	npmScript := "test"
	switch req.Suite {
	case "", "unit":
		npmScript = "test"
	case "e2e":
		npmScript = "test:e2e"
	case "integration":
		npmScript = "test:integration"
	case "smoke":
		npmScript = "test:smoke"
	default:
		npmScript = "test:" + req.Suite
	}
	manifest := s.packageManifest
	if manifest == nil {
		var err error
		manifest, err = readNodePackageManifest(s.sourceLocation)
		if err != nil {
			return s.Runtime.TestErrorf(err, "loading Node.js package manifest")
		}
	}
	if !manifest.hasScript(npmScript) {
		return s.Runtime.TestErrorf(fmt.Errorf("package.json has no %q script", npmScript), "selecting Node.js test script")
	}
	runnerKind := manifest.testRunner(npmScript)

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

	runnerArgs, reporterEnvs := nodeTestReporterConfiguration(runnerKind, jsonFile)

	if pat := combineRegex(req.Filters); pat != "" {
		switch runnerKind {
		case nodeTestPlaywright:
			runnerArgs = append(runnerArgs, "--grep", pat)
		case nodeTestVitest, nodeTestJest:
			runnerArgs = append(runnerArgs, "--testNamePattern", pat)
		default:
			return s.Runtime.TestErrorf(fmt.Errorf("test script %q has no recognized runner", npmScript), "cannot apply typed test filters")
		}
	}

	// Back-compat: target field still maps to a name pattern when
	// filters are not supplied (older clients).
	if req.Target != "" && len(req.Filters) == 0 {
		switch runnerKind {
		case nodeTestPlaywright:
			runnerArgs = append(runnerArgs, "--grep", req.Target)
		case nodeTestVitest, nodeTestJest:
			runnerArgs = append(runnerArgs, "--testNamePattern", req.Target)
		default:
			return s.Runtime.TestErrorf(fmt.Errorf("test script %q has no recognized runner", npmScript), "cannot apply typed test target")
		}
	}

	if req.Coverage {
		if runnerKind == nodeTestGeneric {
			return s.Runtime.TestErrorf(fmt.Errorf("test script %q has no recognized runner", npmScript), "cannot apply typed coverage")
		}
		runnerArgs = append(runnerArgs, "--coverage")
	}

	runnerArgs = append(runnerArgs, req.ExtraArgs...)
	args := []string{"run", npmScript}
	if len(runnerArgs) > 0 {
		args = append(args, "--")
		args = append(args, runnerArgs...)
	}

	s.Wool.Info("running Node.js tests",
		wool.Field("suite", req.Suite),
		wool.Field("runner", runnerKind),
		wool.Field("script", npmScript),
		wool.Field("args", args))

	testEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.TestErrorf(err, "getting environment variables")
	}
	// Reporter output is an agent-owned evidence path. Append it after project
	// env so a stale user value cannot redirect or discard the current run.
	testEnvs = append(testEnvs, reporterEnvs...)
	started := time.Now()
	attempt, err := s.runNodeTestAttempt(ctx, args, testEnvs, jsonFile)
	if err != nil {
		return s.Runtime.TestErrorf(err, "starting Node.js test runner")
	}
	var recoveryErr error
	if runnerKind == nodeTestPlaywright && attempt.runErr != nil {
		browsers := missingPlaywrightBrowsers(attempt.jsonBytes)
		if len(browsers) > 0 {
			s.Wool.Info("recovering missing Playwright browser assets",
				wool.Field("browsers", browsers))
			if err := s.installPlaywrightBrowsers(ctx, browsers); err != nil {
				recoveryErr = err
				s.Wool.Warn("automatic Playwright browser recovery failed", wool.ErrField(err))
			} else {
				attempt, err = s.runNodeTestAttempt(ctx, args, testEnvs, jsonFile)
				if err != nil {
					return s.Runtime.TestErrorf(err, "restarting Node.js test runner after Playwright recovery")
				}
			}
		}
	}
	duration := time.Since(started)
	runErr := attempt.runErr
	consoleOutput := attempt.consoleOutput
	jsonBytes := attempt.jsonBytes

	// Read + parse the JSON regardless of runErr. A failed test run
	// produces non-zero exit code AND a complete JSON file; the
	// structured response carries the per-case detail.
	if len(bytes.TrimSpace(jsonBytes)) == 0 {
		return s.completedConsoleTestResult(req.Suite, runnerKind, args, consoleOutput, duration, runErr)
	}
	var run *javascript.StructuredTestRun
	switch runnerKind {
	case nodeTestPlaywright:
		run = javascript.ParsePlaywrightJSON(string(jsonBytes))
	default:
		run = javascript.ParseJestVitestJSON(string(jsonBytes), 0)
	}

	if run == nil || (len(run.Suites) == 0 && runErr != nil) {
		// Some runner versions can emit a JSON envelope without case suites for
		// setup/global failures. The native summary remains authoritative enough
		// to preserve discovery and failure counts instead of laundering an
		// executed red suite into a zero-test UNKNOWN response.
		return s.completedConsoleTestResult(req.Suite, runnerKind, args, consoleOutput, duration, runErr)
	}

	s.Wool.Forwardf("Tests: %s", run.LegacyTestSummary().SummaryLine())
	response := run.ToProtoResponse(string(runnerKind), req.Suite, duration)
	if recoveryErr != nil {
		response.Output = fmt.Sprintf("automatic Playwright browser recovery failed: %v", recoveryErr)
	}
	return completedTestRPCResult(response, runErr)
}

// completedConsoleTestResult preserves the typed TestResponse contract when a
// package-owned runner cannot provide per-case JSON. Aggregates are still real
// execution evidence: callers must receive Counts and Result, not the legacy
// flat fields alone, or an executed red suite becomes an UNKNOWN zero-test run.
func (s *Runtime) completedConsoleTestResult(
	suite string,
	runnerKind nodeTestRunner,
	args []string,
	consoleOutput string,
	duration time.Duration,
	runErr error,
) (*runtimev0.TestResponse, error) {
	total, passed, failed, skipped := parseNPMTestOutput(consoleOutput)
	if total == 0 {
		if runErr == nil {
			runErr = fmt.Errorf("test script completed without machine-readable or parseable results")
		}
		return s.Runtime.TestErrorf(runErr, "test runner produced no results")
	}
	state := runtimev0.TestRunResult_PASSED
	message := "all tests passed"
	if failed > 0 {
		state = runtimev0.TestRunResult_FAILED
		message = fmt.Sprintf("%d test(s) failed", failed)
	} else if runErr != nil {
		state = runtimev0.TestRunResult_ERRORED
		message = "test command failed after producing aggregate results"
	}
	statusState := runtimev0.TestStatus_SUCCESS
	if state != runtimev0.TestRunResult_PASSED {
		statusState = runtimev0.TestStatus_ERROR
	}
	diagnostics := llmout.Compress("npm", args, consoleOutput)
	failures := []string(nil)
	if state != runtimev0.TestRunResult_PASSED && strings.TrimSpace(diagnostics) != "" {
		failures = append(failures, diagnostics)
	}
	response := &runtimev0.TestResponse{
		Status: &runtimev0.TestStatus{State: statusState, Message: message},
		Run: &runtimev0.TestRun{
			Runner:    string(runnerKind),
			SuiteName: suite,
			Duration:  durationpb.New(duration),
		},
		Result: &runtimev0.TestRunResult{State: state, Message: message},
		Counts: &runtimev0.TestCounts{
			Total:   total,
			Passed:  passed,
			Failed:  failed,
			Skipped: skipped,
		},
		Output:       diagnostics,
		TestsRun:     total,
		TestsPassed:  passed,
		TestsFailed:  failed,
		TestsSkipped: skipped,
		Failures:     failures,
	}
	s.Wool.Forwardf("Tests: %d passed, %d failed, %d skipped", passed, failed, skipped)
	// A completed test run communicates failures in its structured response.
	// Returning a non-nil RPC error alongside it makes gRPC discard the evidence.
	return completedTestRPCResult(response, runErr)
}

type nodeTestAttempt struct {
	jsonBytes     []byte
	consoleOutput string
	runErr        error
}

// runNodeTestAttempt owns one package-script execution and its machine-readable
// evidence. Keeping retry mechanics here guarantees the recovery attempt uses
// the exact same typed request, environment, and reporter contract.
func (s *Runtime) runNodeTestAttempt(
	ctx context.Context,
	args []string,
	envs []*resources.EnvironmentVariable,
	jsonFile string,
) (nodeTestAttempt, error) {
	if err := os.Remove(jsonFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nodeTestAttempt{}, fmt.Errorf("clear previous test report: %w", err)
	}
	testProc, err := s.runnerEnvironment.NewProcess("npm", args...)
	if err != nil {
		return nodeTestAttempt{}, fmt.Errorf("create test process: %w", err)
	}
	testProc.WithEnvironmentVariables(ctx, envs...)
	var consoleOutput bytes.Buffer
	testProc.WithOutput(io.MultiWriter(s.Logger, &consoleOutput))
	runErr := testProc.Run(ctx)
	jsonBytes, readErr := os.ReadFile(jsonFile) //nolint:gosec // agent-owned path under sourceDir
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nodeTestAttempt{}, fmt.Errorf("read test report: %w", readErr)
	}
	return nodeTestAttempt{
		jsonBytes:     jsonBytes,
		consoleOutput: consoleOutput.String(),
		runErr:        runErr,
	}, nil
}

// nodeTestReporterConfiguration maps each package-owned runner to its
// machine-readable output contract. Playwright's `--output` flag is deliberately
// absent: it controls the artifact directory, while the JSON reporter's file is
// selected by PLAYWRIGHT_JSON_OUTPUT_FILE.
func nodeTestReporterConfiguration(runner nodeTestRunner, jsonFile string) ([]string, []*resources.EnvironmentVariable) {
	switch runner {
	case nodeTestPlaywright:
		return []string{"--reporter=json"}, []*resources.EnvironmentVariable{
			resources.Env("PLAYWRIGHT_JSON_OUTPUT_FILE", jsonFile),
		}
	case nodeTestVitest:
		return []string{"--reporter=json", "--outputFile=" + jsonFile}, nil
	case nodeTestJest:
		return []string{"--json", "--outputFile=" + jsonFile}, nil
	default:
		return nil, nil
	}
}

func completedTestRPCResult(
	response *runtimev0.TestResponse,
	executionErr error,
) (*runtimev0.TestResponse, error) {
	if response == nil {
		return nil, executionErr
	}
	return response, nil
}

// Lint owns the JavaScript/TypeScript static-lint phase for every service made
// from this generic Next.js agent. CI only dispatches this RPC; it never needs
// to know that the current implementation uses an npm script.
func (s *Runtime) Lint(ctx context.Context, req *runtimev0.LintRequest) (*runtimev0.LintResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	if req == nil {
		req = &runtimev0.LintRequest{}
	}
	args := []string{"run", "lint"}
	if req.Target != "" {
		args = append(args, "--")
		args = append(args, req.Target)
	}
	output, err := s.runNPM(ctx, args...)
	compressed := llmout.Compress("npm", args, output)
	if err != nil {
		return s.Runtime.LintErrorf(err, "lint failed:\n%s", compressed)
	}
	return s.Runtime.LintResponse(compressed)
}

// Build is the native compile/typecheck phase. It is deliberately separate
// from Builder.Build, which owns the deployable image.
func (s *Runtime) Build(ctx context.Context, _ *runtimev0.BuildRequest) (*runtimev0.BuildResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	manifest := s.packageManifest
	if manifest == nil {
		var err error
		manifest, err = readNodePackageManifest(s.sourceLocation)
		if err != nil {
			return s.Runtime.BuildErrorf(err, "loading Node.js package manifest")
		}
	}
	scripts := manifest.validationScripts()
	if len(scripts) == 0 {
		return s.Runtime.BuildErrorf(
			fmt.Errorf("package.json declares neither a typecheck nor build script"),
			"selecting Node.js compile checks",
		)
	}

	var outputs []string
	for _, script := range scripts {
		args := []string{"run", script}
		output, err := s.runNPM(ctx, args...)
		compressed := llmout.Compress("npm", args, output)
		outputs = append(outputs, compressed)
		if err != nil {
			return s.Runtime.BuildErrorf(err, "native build failed during npm %s:\n%s", strings.Join(args, " "), compressed)
		}
	}
	return s.Runtime.BuildResponse(strings.Join(outputs, "\n"))
}

func (s *Runtime) ensureNodeDependencies(ctx context.Context) error {
	s.dependenciesMu.Lock()
	defer s.dependenciesMu.Unlock()
	if s.runnerEnvironment == nil {
		return fmt.Errorf("runner environment is not initialized")
	}
	if s.nodeDependenciesPresent(ctx) {
		return nil
	}
	args := []string{"install"}
	if _, err := os.Stat(filepath.Join(s.sourceLocation, "package-lock.json")); err == nil {
		args = []string{"ci"}
	}
	s.Wool.Info("installing Node.js dependencies", wool.Field("command", "npm "+strings.Join(args, " ")))
	proc, err := s.runnerEnvironment.NewProcess("npm", args...)
	if err != nil {
		return fmt.Errorf("create npm dependency process: %w", err)
	}
	proc.WithOutput(s.Logger)
	if err := proc.Run(ctx); err != nil {
		return fmt.Errorf("npm %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

// installPlaywrightBrowsers materializes only the engines proved missing by a
// structured Playwright report. Browser binaries are intentionally recovered
// from Test rather than eagerly downloading every engine during Runtime.Init.
func (s *Runtime) installPlaywrightBrowsers(ctx context.Context, browsers []string) error {
	args := append([]string{"exec", "--offline", "--", "playwright", "install"}, browsers...)
	s.Wool.Info("materializing Playwright browser assets",
		wool.Field("browsers", browsers),
		wool.Field("command", "npm "+strings.Join(args, " ")))
	proc, err := s.runnerEnvironment.NewProcess("npm", args...)
	if err != nil {
		return fmt.Errorf("create Playwright browser install process: %w", err)
	}
	proc.WithOutput(s.Logger)
	if err := proc.Run(ctx); err != nil {
		return fmt.Errorf("npm %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

// missingPlaywrightBrowsers extracts the exact engine set from Playwright's
// canonical missing-executable failures. It deliberately requires both the
// launch failure and install guidance, so ordinary browser-named assertions do
// not trigger dependency mutation. Each match is inspected locally to avoid a
// project matrix naming another browser elsewhere in the JSON report.
func missingPlaywrightBrowsers(report []byte) []string {
	lower := strings.ToLower(string(report))
	const missingExecutable = "executable doesn't exist"
	if !strings.Contains(lower, missingExecutable) {
		return nil
	}
	found := map[string]bool{}
	for searchFrom := 0; searchFrom < len(lower); {
		relative := strings.Index(lower[searchFrom:], missingExecutable)
		if relative < 0 {
			break
		}
		start := searchFrom + relative
		end := min(len(lower), start+1024)
		window := lower[start:end]
		if !strings.Contains(window, "playwright install") {
			searchFrom = start + len(missingExecutable)
			continue
		}
		for _, browser := range []string{"chromium", "firefox", "webkit"} {
			if strings.Contains(window, browser) {
				found[browser] = true
			}
		}
		searchFrom = start + len(missingExecutable)
	}
	var browsers []string
	for _, browser := range []string{"chromium", "firefox", "webkit"} {
		if found[browser] {
			browsers = append(browsers, browser)
		}
	}
	return browsers
}

func (s *Runtime) nodeDependenciesPresent(ctx context.Context) bool {
	if !s.Runtime.IsContainerRuntime() {
		info, err := os.Stat(filepath.Join(s.sourceLocation, "node_modules"))
		return err == nil && info.IsDir()
	}

	// The container has a platform-specific cache layered over the host
	// node_modules path. Inspect from inside the selected runtime; checking the
	// host would incorrectly treat macOS dependencies as valid for Linux.
	proc, err := s.runnerEnvironment.NewProcess(
		"node",
		"-e",
		"const fs=require('fs'); process.exit(fs.existsSync('node_modules') ? 0 : 1)",
	)
	if err != nil {
		return false
	}
	proc.WithOutput(io.Discard)
	return proc.Run(ctx) == nil
}

func nodeDependencyCacheKey(sourceLocation, executionPlatform string) (string, error) {
	if strings.TrimSpace(executionPlatform) == "" {
		return "", fmt.Errorf("Node dependency execution platform is required")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(executionPlatform))
	_, _ = hash.Write([]byte{0})
	found := false
	for _, name := range []string{"package.json", "npm-shrinkwrap.json", "package-lock.json"} {
		content, err := os.ReadFile(filepath.Join(sourceLocation, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		found = true
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	if !found {
		return "", fmt.Errorf("package.json or npm lockfile is required")
	}
	return fmt.Sprintf("node-modules-%x", hash.Sum(nil)[:12]), nil
}

func (s *Runtime) runNPM(ctx context.Context, args ...string) (string, error) {
	if s.runnerEnvironment == nil {
		return "", fmt.Errorf("runner environment is not initialized")
	}
	proc, err := s.runnerEnvironment.NewProcess("npm", args...)
	if err != nil {
		return "", fmt.Errorf("create npm process: %w", err)
	}
	var output bytes.Buffer
	// Return one bounded payload through the RPC. Streaming the same command to
	// the agent logger would make the CLI print every diagnostic twice.
	proc.WithOutput(&output)
	envs, err := s.EnvironmentVariables.All()
	if err != nil {
		return "", fmt.Errorf("get environment variables: %w", err)
	}
	proc.WithEnvironmentVariables(ctx, envs...)
	err = proc.Run(ctx)
	return output.String(), err
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

// ansiEscape matches ANSI CSI escape sequences (e.g. color codes) that runners
// emit when attached to a TTY/pty, so they can be stripped before tokenizing.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// parseVitestOutput extracts test counts from vitest output.
func parseVitestOutput(output string) (run, passed, failed, skipped int32) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(ansiEscape.ReplaceAllString(line, ""))
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

// parseNPMTestOutput combines Vitest, Playwright, and Node built-in test runner
// summaries. This is the truthful compatibility path for a consumer that
// composes multiple commands in its npm test script or a runner failure whose
// JSON envelope contains no cases.
func parseNPMTestOutput(output string) (run, passed, failed, skipped int32) {
	run, passed, failed, skipped = parseVitestOutput(output)
	playwrightRun, playwrightPassed, playwrightFailed, playwrightSkipped := parsePlaywrightOutput(output)
	var nodeRun, nodePassed, nodeFailed, nodeSkipped int32
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(ansiEscape.ReplaceAllString(line, ""))
		for _, item := range []struct {
			prefix string
			value  *int32
		}{
			// node --test spec reporter (TTY default)
			{"ℹ tests ", &nodeRun},
			{"ℹ pass ", &nodePassed},
			{"ℹ fail ", &nodeFailed},
			{"ℹ skipped ", &nodeSkipped},
			// node --test TAP reporter (non-TTY default)
			{"# tests ", &nodeRun},
			{"# pass ", &nodePassed},
			{"# fail ", &nodeFailed},
			{"# skipped ", &nodeSkipped},
		} {
			if strings.HasPrefix(line, item.prefix) {
				_, _ = fmt.Sscanf(strings.TrimPrefix(line, item.prefix), "%d", item.value)
			}
		}
	}
	return run + playwrightRun + nodeRun,
		passed + playwrightPassed + nodePassed,
		failed + playwrightFailed + nodeFailed,
		skipped + playwrightSkipped + nodeSkipped
}

var playwrightSummaryLine = regexp.MustCompile(`^(\d+)\s+(passed|failed|flaky|skipped|did not run)(?:\s+\([^)]*\))?$`)

// parsePlaywrightOutput recognizes only final Playwright aggregate lines. In
// particular, dependent tests reported as "did not run" remain part of the
// discovered total and map to the protocol's closest aggregate state, skipped.
func parsePlaywrightOutput(output string) (run, passed, failed, skipped int32) {
	var flaky, notRun int32
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(ansiEscape.ReplaceAllString(line, ""))
		match := playwrightSummaryLine.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		var count int32
		if _, err := fmt.Sscanf(match[1], "%d", &count); err != nil {
			continue
		}
		switch match[2] {
		case "passed":
			passed = max(passed, count)
		case "failed":
			failed = max(failed, count)
		case "flaky":
			// Flaky tests eventually passed; the JSON contract carries a
			// distinct Flaky count when available, while the aggregate fallback
			// treats them as executable passes.
			flaky = max(flaky, count)
		case "skipped":
			skipped = max(skipped, count)
		case "did not run":
			notRun = max(notRun, count)
		}
	}
	passed += flaky
	skipped += notRun
	return passed + failed + skipped, passed, failed, skipped
}

/* Details */

func (s *Runtime) EventHandler(event code.Change) error {
	s.Wool.Info("detected change requiring re-start", wool.Field("path", event.Path))
	s.Runtime.DesiredStart()
	return nil
}

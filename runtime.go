package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"

	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/wool"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
)

type Runtime struct {
	services.RuntimeServer

	*Service

	// internal
	nativeEnv *runners.NativeEnvironment
	runner    runners.Proc
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
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

	// Setup native runner
	s.nativeEnv, err = runners.NewNativeEnvironment(ctx, s.sourceLocation)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	err = s.nativeEnv.Init(ctx)
	if err != nil {
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

	// Run npm run dev with the assigned port
	proc, err := s.nativeEnv.NewProcess("npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", net.Port))
	if err != nil {
		return s.Runtime.StartErrorf(err, "cannot create npm process")
	}

	allEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.StartErrorf(err, "getting environment variables")
	}
	proc.WithEnvironmentVariables(ctx, allEnvs...)
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
		return s.Runtime.StartError(err)
	}

	s.Wool.Forwardf("Next.js dev server running on port %d", net.Port)

	return s.Runtime.StartResponse()
}

func (s *Runtime) WaitForReady(ctx context.Context, net *basev0.NetworkInstance) error {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	address := fmt.Sprintf("http://%s", net.Address)
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

	return s.Runtime.DestroyResponse()
}

func (s *Runtime) Test(ctx context.Context, req *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	return s.Runtime.TestResponse()
}

/* Details */

func (s *Runtime) EventHandler(event code.Change) error {
	s.Wool.Info("detected change requiring re-start", wool.Field("path", event.Path))
	s.Runtime.DesiredStart()
	return nil
}

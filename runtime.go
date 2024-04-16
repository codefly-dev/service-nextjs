package main

import (
	"context"
	"fmt"
	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/configurations"
	v0 "github.com/codefly-dev/core/generated/go/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/services/agent/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/services/runtime/v0"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
	"github.com/hashicorp/go-multierror"
	"os"
)

type Runtime struct {
	*Service

	// Internal
	port              uint16
	runner            runners.Proc
	runnerEnvironment runners.RunnerEnvironment
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	defer s.Wool.Catch()

	s.Runtime.Scope = req.Scope

	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Base.Runtime.LoadError(err)
	}

	s.EnvironmentVariables.SetEnvironment(req.Environment)

	s.sourceLocation = s.Local("src")

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Base.Runtime.LoadError(err)
	}

	s.httpEndpoint, err = configurations.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		return s.Base.Runtime.LoadError(err)
	}

	return s.Base.Runtime.LoadResponse()
}

func (s *Runtime) CreateRunnerEnvironment(ctx context.Context) error {
	s.Wool.Debug("creating runner environment in", wool.DirField(s.sourceLocation))
	if s.Runtime.Container() {
		s.Wool.Debug("running in container")

		dockerEnv, err := runners.NewDockerEnvironment(ctx, runtimeImage, s.sourceLocation, s.UniqueWithProject())
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker runner")
		}
		dockerEnv.WithPause()
		err = dockerEnv.Clear(ctx)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot clear the docker environment")
		}
		// Need to bind the ports
		instance, err := configurations.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.httpEndpoint, s.Runtime.Scope)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot find network instance")
		}
		dockerEnv.WithPort(ctx, uint16(instance.Port))
		modulesPath := s.DockerNodeModulesPath()
		_, err = shared.CheckDirectoryOrCreate(ctx, modulesPath)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker venv environment")
		}
		dockerEnv.WithMount(modulesPath, "/codefly/node_modules")
		s.runnerEnvironment = dockerEnv
	} else {
		s.Wool.Debug("running locally")
		localEnv, err := runners.NewLocalEnvironment(ctx, s.sourceLocation)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create local runner")
		}
		// HACK
		localEnv.WithEnvironmentVariables(configurations.Env("PATH", os.Getenv("PATH")))
		s.runnerEnvironment = localEnv
	}
	s.runnerEnvironment.WithEnvironmentVariables(s.EnvironmentVariables.All()...)
	return nil
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Runtime.LogInitRequest(req)

	s.NetworkMappings = req.ProposedNetworkMappings

	// Networking
	instance, err := s.Runtime.NetworkInstance(ctx, s.NetworkMappings, s.httpEndpoint)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.LogForward("will run on http://localhost:%d", instance.Port)
	s.port = uint16(instance.Port)

	err = s.CreateRunnerEnvironment(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// npm install
	s.LogForward("installing dependencies, may take a while")
	proc, err := s.runnerEnvironment.NewProcess("npm", "install")
	if err != nil {
		return s.Runtime.InitError(err)
	}

	err = proc.Run(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.NetworkMappings = req.ProposedNetworkMappings

	return s.Base.Runtime.InitResponse()
}

func (s *Runtime) DockerNodeModulesPath() string {
	return s.Local(".cache/container/node_modules")
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogStartRequest(req)

	err := s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings, v0.NetworkScope_Public)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("adding external endpoints"))
	}

	err = s.EnvironmentVariables.AddRestRoutes(ctx, req.DependenciesNetworkMappings, v0.NetworkScope_Public)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("adding rest routes"))
	}
	envs := s.EnvironmentVariables.All()
	s.Wool.Debug("environment variables", wool.Field("envs", envs))

	// Generate the .env.local
	s.Wool.Debug("copying special files")
	err = templates.CopyAndApplyTemplate(ctx, shared.Embed(specialFS),
		"templates/factory/special/env.local.tmpl",
		s.Local("src/.env.local"), envs)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("copying special files"))
	}

	// We have hot-reloading built-in
	if s.runner != nil {
		s.Wool.Debug("using built-in hot reloading")
		return s.Runtime.StartResponse()
	}

	runningContext := s.Wool.Inject(context.Background())
	proc, err := s.runnerEnvironment.NewProcess("npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.port))
	if err != nil {
		return s.Runtime.StartError(err, wool.InField("runner"))
	}
	proc.WithOutput(s.Logger)
	s.runner = proc
	err = s.runner.Start(runningContext)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("runner"))
	}

	return s.Runtime.StartResponse()
}

func (s *Runtime) Information(ctx context.Context, req *runtimev0.InformationRequest) (*runtimev0.InformationResponse, error) {
	return s.Runtime.InformationResponse(ctx, req)
}

func (s *Runtime) Stop(ctx context.Context, req *runtimev0.StopRequest) (*runtimev0.StopResponse, error) {
	defer s.Wool.Catch()
	var agg error
	s.Wool.Debug("stopping service")
	if s.runner != nil {
		err := s.runner.Stop(ctx)
		if err != nil {
			agg = multierror.Append(agg, err)
		}
	}
	s.Wool.Debug("runner stopped")
	err := s.Base.Stop()
	if err != nil {
		agg = multierror.Append(agg, err)
		s.Wool.Warn("error stopping runner", wool.ErrField(err))
	}
	if agg != nil {
		return s.Base.Runtime.StopError(agg)
	}
	return s.Runtime.StopResponse()
}

func (s *Runtime) Test(ctx context.Context, req *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (s *Runtime) Communicate(ctx context.Context, req *agentv0.Engage) (*agentv0.InformationRequest, error) {
	return s.Base.Communicate(ctx, req)
}

/* Details

 */

func (s *Runtime) EventHandler(event code.Change) error {
	s.Wool.Debug("got an event: %v")
	return nil
}

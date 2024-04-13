package main

import (
	"context"
	"fmt"
	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/configurations"
	v0 "github.com/codefly-dev/core/generated/go/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/services/agent/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/services/runtime/v0"
	"github.com/codefly-dev/core/runners"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
	"github.com/hashicorp/go-multierror"
)

type Runtime struct {
	*Service

	// Internal
	runner       runners.Runner
	otherRunners []runners.Runner
	port         uint16
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

func (s *Runtime) nativeInitRunner(ctx context.Context) (runners.Runner, error) {
	runner, err := runners.NewProcess(ctx, "npm", "install")
	if err != nil {
		return nil, err
	}
	runner.WithDir(s.sourceLocation)
	err = runner.WithOut(s.Logger)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

func (s *Runtime) dockerInitRunner(ctx context.Context) (runners.Runner, error) {
	runner, err := runners.NewDocker(ctx, runtimeImage)
	if err != nil {
		return nil, err
	}

	err = runner.Init(ctx)
	if err != nil {
		return nil, err
	}

	runner.WithMount(s.sourceLocation, "/app")
	runner.WithWorkDir("/app")
	runner.WithCommand("npm", "install")
	runner.WithOut(s.Logger)
	return runner, nil
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

	// npm install
	s.LogForward("installing dependencies, may take a while")
	var runner runners.Runner
	switch s.Runtime.Scope {
	case v0.NetworkScope_Native:
		runner, err = s.nativeInitRunner(ctx)
	case v0.NetworkScope_Container:
		runner, err = s.dockerInitRunner(ctx)
	}
	if runner == nil {
		return s.Base.Runtime.InitError(s.Wool.NewError("no runner found"))
	}
	err = runner.Run(ctx)
	if err != nil {
		return s.Base.Runtime.InitError(err)
	}

	s.otherRunners = append(s.otherRunners, runner)

	s.NetworkMappings = req.ProposedNetworkMappings

	return s.Base.Runtime.InitResponse()
}

func (s *Runtime) nativeStartRunner(ctx context.Context) (runners.Runner, error) {
	runner, err := runners.NewProcess(ctx, "npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.port))
	if err != nil {
		return nil, err
	}
	runner.WithDir(s.sourceLocation)
	err = runner.WithOut(s.Logger)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

func (s *Runtime) dockerStartRunner(ctx context.Context) (runners.Runner, error) {
	runner, err := runners.NewDocker(ctx, runtimeImage)
	if err != nil {
		return nil, err
	}

	err = runner.Init(ctx)
	if err != nil {
		return nil, err
	}

	runner.WithPort(runners.DockerPortMapping{Container: uint16(s.port), Host: uint16(s.port)})
	runner.WithName(s.Global())
	runner.WithMount(s.sourceLocation, "/app")
	runner.WithWorkDir("/app")
	runner.WithCommand("npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.port))
	runner.WithOut(s.Logger)
	return runner, nil
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
	var runner runners.Runner
	switch s.Runtime.Scope {
	case v0.NetworkScope_Native:
		runner, err = s.nativeStartRunner(ctx)
	case v0.NetworkScope_Container:
		runner, err = s.dockerStartRunner(ctx)
	}
	if runner == nil {
		return s.Base.Runtime.StartError(s.Wool.NewError("no runner found"))
	}
	err = runner.Start(runningContext)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("runner"))
	}
	s.runner = runner

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
		err := s.runner.Stop()
		if err != nil {
			agg = multierror.Append(agg, err)
		}
	}
	for _, run := range s.otherRunners {
		err := run.Stop()
		if err != nil {
			agg = multierror.Append(agg, err)
			s.Wool.Warn("error stopping runner", wool.ErrField(err))
		}
	}
	s.Wool.Debug("runner stopped")
	err := s.Base.Stop()
	if err != nil {
		if err != nil {
			agg = multierror.Append(agg, err)
			s.Wool.Warn("error stopping runner", wool.ErrField(err))
		}
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

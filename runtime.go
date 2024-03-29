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
)

type Runtime struct {
	*Service

	// Internal
	runner runners.Runner
	port   uint16
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
	runner.WithOut(s.Wool)
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
	runner.WithOut(s.Wool)
	return runner, nil
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Runtime.LogInitRequest(req)

	s.NetworkMappings = req.ProposedNetworkMappings

	// Networking
	instance, err := s.Runtime.NetworkInstance(s.NetworkMappings, s.httpEndpoint)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.LogForward("will run on http://localhost:%d", instance.Port)
	s.port = uint16(instance.Port)

	// npm install
	var runner runners.Runner
	switch s.Runtime.Scope {
	case v0.RuntimeScope_Native:
		runner, err = s.nativeInitRunner(ctx)
	case v0.RuntimeScope_Container:
		runner, err = s.dockerInitRunner(ctx)
	}
	if err != nil {
		return s.Base.Runtime.InitError(err)
	}
	err = runner.Run(ctx)
	if err != nil {
		return s.Base.Runtime.InitError(err)
	}

	s.NetworkMappings = req.ProposedNetworkMappings

	return s.Base.Runtime.InitResponse()
}

func (s *Runtime) nativeStartRunner(ctx context.Context) (runners.Runner, error) {
	runner, err := runners.NewProcess(ctx, "npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.port))
	if err != nil {
		return nil, err
	}
	runner.WithDir(s.sourceLocation)
	runner.WithOut(s.Wool)
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
	runner.WithOut(s.Wool)
	return runner, nil
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogStartRequest(req)

	err := s.EnvironmentVariables.AddPublicEndpoints(ctx, req.OtherNetworkMappings)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("adding external endpoints"))
	}

	err = s.EnvironmentVariables.AddPublicRestRoutes(ctx, req.OtherNetworkMappings)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("adding rest routes"))
	}
	envs := s.EnvironmentVariables.All()
	s.Wool.Focus("environment variables", wool.Field("envs", envs))

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
	case v0.RuntimeScope_Native:
		runner, err = s.nativeStartRunner(ctx)
	case v0.RuntimeScope_Container:
		runner, err = s.dockerStartRunner(ctx)
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

	s.Wool.Debug("stopping service")
	err := s.runner.Stop()
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot kill runner")
	}

	s.Wool.Debug("runner stopped")
	err = s.Base.Stop()
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot stop base")
	}
	return s.Runtime.StopResponse()
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

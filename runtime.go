package main

import (
	"context"
	"fmt"
	"github.com/codefly-dev/core/configurations"
	"github.com/codefly-dev/core/configurations/standards"
	basev0 "github.com/codefly-dev/core/generated/go/base/v0"

	"github.com/codefly-dev/core/agents/helpers/code"
	agentv0 "github.com/codefly-dev/core/generated/go/services/agent/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/services/runtime/v0"
	"github.com/codefly-dev/core/runners"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
)

type Runtime struct {
	*Service
	SourceLocation string

	Runner *runners.Runner

	NetworkMappings []*basev0.NetworkMapping

	Port int
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	defer s.Wool.Catch()

	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Base.Runtime.LoadError(err)
	}

	s.SourceLocation = s.Local("src")

	s.EnvironmentVariables = s.LoadEnvironmentVariables(req.Environment)

	err = s.LoadEndpoints(ctx)
	if err != nil {
		return s.Base.Runtime.LoadError(err)
	}
	s.Wool.Focus("loading runtime", wool.NullableField("endpoints", configurations.MakeEndpointSummary(s.Endpoints)))
	return s.Base.Runtime.LoadResponse()
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Wool.Focus("initialize runtime", wool.NullableField("dependency endpoints", configurations.MakeEndpointSummary(req.DependenciesEndpoints)))

	s.NetworkMappings = req.ProposedNetworkMappings

	net, err := configurations.GetMappingInstanceFor(s.NetworkMappings, standards.HTTP)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.Port = net.Port

	return s.Base.Runtime.InitResponse(s.NetworkMappings)
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("network mappings", wool.NullableField("other", configurations.MakeNetworkMappingSummary(req.OtherNetworkMappings)))

	envs := s.EnvironmentVariables.GetBase()

	publicNetworkMappings := configurations.ExtractPublicNetworkMappings(req.OtherNetworkMappings)

	s.Wool.Focus("public network mappings", wool.NullableField("public", configurations.MakeNetworkMappingSummary(publicNetworkMappings)))

	endpointEnvs, err := configurations.ExtractEndpointEnvironmentVariables(ctx, publicNetworkMappings)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("converting incoming network mappings"))
	}

	envs = append(envs, endpointEnvs...)

	restEnvs, err := configurations.ExtractRestRoutesEnvironmentVariables(ctx, publicNetworkMappings)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("converting incoming network mappings"))
	}

	envs = append(envs, restEnvs...)

	if err != nil {
		return s.Base.Runtime.StartError(err)
	}

	// Generate the .env.local
	s.Wool.Debug("copying special files")
	err = templates.CopyAndApplyTemplate(ctx, shared.Embed(specialFS),
		"templates/factory/special/env.local.tmpl",
		s.Local("src/.env.local"), envs)
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("copying special files"))
	}

	// We have hot-reloading built-in
	if s.Runner != nil {
		s.Wool.Debug("using built-in hot reloading")
		return s.Runtime.StartResponse()
	}

	runningContext := s.Wool.Inject(context.Background())
	runner, err := runners.NewRunner(runningContext, "npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.Port))
	if err != nil {
		return s.Base.Runtime.StartError(err, wool.InField("runner"))
	}
	s.Runner = runner
	s.Runner.WithDir(s.SourceLocation)

	err = s.Runner.Start()
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

	s.Wool.Debug("stopping service")
	err := s.Runner.Stop()
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

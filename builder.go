package main

import (
	"context"
	"embed"
	dockerhelpers "github.com/codefly-dev/core/agents/helpers/docker"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/configurations"
	"github.com/codefly-dev/core/configurations/standards"
	basev0 "github.com/codefly-dev/core/generated/go/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/services/builder/v0"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
)

type Builder struct {
	*Service
}

func NewBuilder() *Builder {
	return &Builder{
		Service: NewService(),
	}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()

	err := s.Builder.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return nil, err
	}

	s.sourceLocation, err = s.LocalDirCreate(ctx, "src")

	gettingStarted, err := templates.ApplyTemplateFrom(ctx, shared.Embed(factoryFS), "templates/factory/GETTING_STARTED.md", s.Information)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	s.Endpoints, err = s.Builder.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	s.httpEndpoint, err = configurations.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	return s.Builder.LoadResponse(gettingStarted)
}

func (s *Builder) Init(ctx context.Context, req *builderv0.InitRequest) (*builderv0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.In("Init").Debug("dependencies", wool.SliceCountField(req.DependenciesEndpoints))

	s.DependencyEndpoints = req.DependenciesEndpoints

	//hash, err := requirements.Hash(ctx)
	//if err != nil {
	//	return s.Builder.InitError(err)
	//}

	return s.Builder.InitResponse()
}

func (s *Builder) Update(ctx context.Context, req *builderv0.UpdateRequest) (*builderv0.UpdateResponse, error) {
	defer s.Wool.Catch()

	return &builderv0.UpdateResponse{}, nil
}

func (s *Builder) Sync(ctx context.Context, req *builderv0.SyncRequest) (*builderv0.SyncResponse, error) {
	defer s.Wool.Catch()

	return s.Builder.SyncResponse()
}

type DockerTemplating struct {
	Components []string
	Builder    string
	Runner     string
}

func (s *Builder) Build(ctx context.Context, req *builderv0.BuildRequest) (*builderv0.BuildResponse, error) {
	defer s.Wool.Catch()

	s.Wool.Debug("building docker image")
	ctx = s.Wool.Inject(ctx)

	dockerRequest, err := s.Builder.DockerBuildRequest(ctx, req)
	if err != nil {
		return nil, s.Wool.Wrapf(err, "can only do docker build request")
	}

	image := s.DockerImage(dockerRequest)

	s.Wool.In("Build").Debug("dependencies", wool.SliceCountField(s.DependencyEndpoints))

	docker := DockerTemplating{
		Builder:    runtimeImage.FullName(),
		Runner:     runtimeImage.FullName(),
		Components: requirements.All(),
	}

	err = shared.DeleteFile(ctx, s.Local("builder/Dockerfile"))
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot remove dockerfile")
	}

	err = s.Templates(ctx, docker, services.WithBuilder(builderFS))
	if err != nil {
		return s.Builder.BuildError(err)
	}

	builder, err := dockerhelpers.NewBuilder(dockerhelpers.BuilderConfiguration{
		Root:        s.Location,
		Dockerfile:  "builder/Dockerfile",
		Ignores:     []string{"node_modules", ".next", ".idea", "env.local"},
		Destination: image,
		Output:      s.Wool,
	})
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot create builder")
	}
	_, err = builder.Build(ctx)
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot build image")
	}
	s.Builder.WithDockerImages(image)
	return s.Builder.BuildResponse()
}

type LoadBalancer struct {
	Enabled bool
	Host    string
}

type Parameters struct {
	LoadBalancer
}

func (s *Builder) Deploy(ctx context.Context, req *builderv0.DeploymentRequest) (*builderv0.DeploymentResponse, error) {
	defer s.Wool.Catch()

	ctx = s.Wool.Inject(ctx)

	s.Builder.LogDeployRequest(req, s.Wool.Focus)

	err := s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings, basev0.NetworkScope_Public)
	if err != nil {
		return s.Base.Builder.DeployError(err)
	}

	err = s.EnvironmentVariables.AddRestRoutes(ctx, req.DependenciesNetworkMappings, basev0.NetworkScope_Public)
	if err != nil {
		return s.Base.Builder.DeployError(err)
	}

	err = s.EnvironmentVariables.AddConfigurations(req.DependenciesConfigurations...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	cm, err := services.EnvsAsConfigMapData(s.EnvironmentVariables.Configurations()...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	secrets, err := services.EnvsAsSecretData(s.EnvironmentVariables.Secrets()...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	params := services.DeploymentParameters{
		ConfigMap:  cm,
		SecretMap:  secrets,
		Parameters: Parameters{LoadBalancer{}},
	}

	if req.Deployment.LoadBalancer {
		inst, err := configurations.FindNetworkInstance(ctx, req.NetworkMappings, s.httpEndpoint, basev0.NetworkScope_Public)
		if err != nil {
			return s.Builder.DeployError(err)
		}
		params.Parameters = Parameters{LoadBalancer{Host: inst.Host, Enabled: true}}
	}
	var k *builderv0.KubernetesDeployment
	if k, err = s.Builder.KubernetesDeploymentRequest(ctx, req); err != nil {
		return s.Builder.DeployError(err)
	}

	err = s.Builder.KustomizeDeploy(ctx, req.Environment, k, deploymentFS, params)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	return s.Builder.DeployResponse()
}

func (s *Builder) Create(ctx context.Context, req *builderv0.CreateRequest) (*builderv0.CreateResponse, error) {
	defer s.Wool.Catch()

	err := s.CreateEndpoint(ctx)
	if err != nil {
		return s.Builder.CreateError(err)
	}

	ignore := shared.NewIgnore("node_modules", ".next", ".idea", "env.local")
	err = s.Templates(ctx, s.Information, services.WithFactory(factoryFS).WithPathSelect(ignore))

	if err != nil {
		return s.Builder.CreateError(err)
	}

	// Need to handle the case of pages/_aps.tsx
	err = templates.Copy(ctx, shared.Embed(specialFS),
		"templates/factory/special/pages/app.tsx",
		s.Local("src/pages/_app.tsx"))
	if err != nil {
		return s.Builder.CreateError(err)
	}

	return s.Builder.CreateResponse(ctx, s.Settings)
}

func (s *Builder) CreateEndpoint(ctx context.Context) error {
	endpoint := s.Base.Service.BaseEndpoint(standards.HTTP)
	endpoint.Visibility = configurations.VisibilityPublic
	http, err := configurations.LoadHTTPAPI(ctx)
	if err != nil {
		return s.Wool.Wrapf(err, "cannot load HTTP api")
	}
	s.httpEndpoint, err = configurations.NewAPI(ctx, endpoint, configurations.ToHTTPAPI(http))
	if err != nil {
		return s.Wool.Wrapf(err, "cannot create openapi api")
	}
	s.Endpoints = []*basev0.Endpoint{s.httpEndpoint}
	return nil
}

//go:embed templates/factory
var factoryFS embed.FS

//go:embed templates/builder
var builderFS embed.FS

//go:embed templates/deployment
var deploymentFS embed.FS

//go:embed templates/factory/special
var specialFS embed.FS

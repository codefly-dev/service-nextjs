package main

import (
	"context"
	"embed"
	"fmt"
	"os"

	dockerhelpers "github.com/codefly-dev/core/agents/helpers/docker"
	"github.com/codefly-dev/core/agents/communicate"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/agents/services/audit"
	"github.com/codefly-dev/core/agents/services/upgrade"
	proto "github.com/codefly-dev/core/companions/proto"
	v0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/standards"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
)

type Builder struct {
	services.BuilderServer
	*Service

	answers map[string]*agentv0.Answer
}

func NewBuilder() *Builder {
	return &Builder{
		Service: NewService(),
	}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return nil, err
	}

	s.Wool.Debug("base loaded", wool.Field("identity", s.Identity))

	if req.DisableCatch {
		s.Wool.DisableCatch()
	}

	s.sourceLocation = s.Local("%s", s.Settings.NodeSourceDir())

	requirements.Localize(s.Location)

	if req.CreationMode != nil {
		s.Builder.CreationMode = req.CreationMode
		s.Builder.GettingStarted, err = templates.ApplyTemplateFrom(ctx, shared.Embed(factoryFS), "templates/factory/GETTING_STARTED.md", s.Information)
		if err != nil {
			return s.Builder.LoadError(err)
		}
		return s.Builder.LoadResponse()
	}

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	s.HttpEndpoint, err = resources.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	return s.Builder.LoadResponse()
}

func (s *Builder) Init(ctx context.Context, req *builderv0.InitRequest) (*builderv0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Builder.LogInitRequest(req)

	s.DependencyEndpoints = req.DependenciesEndpoints

	return s.Builder.InitResponse()
}

func (s *Builder) Update(ctx context.Context, req *builderv0.UpdateRequest) (*builderv0.UpdateResponse, error) {
	defer s.Wool.Catch()

	return &builderv0.UpdateResponse{}, nil
}

func (s *Builder) Sync(ctx context.Context, req *builderv0.SyncRequest) (*builderv0.SyncResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	w := s.Wool

	// Generate TypeScript Connect-ES client code from dependency gRPC endpoints.
	// The proto companion runs buf with @bufbuild/protoc-gen-es and
	// @connectrpc/protoc-gen-connect-es to produce typed TypeScript clients.
	// The frontend uses Connect-web to call these services through the gateway.
	for _, dep := range s.Service.Service.ServiceDependencies {
		grpcEP, err := resources.FindGRPCEndpointFromService(ctx, dep, s.DependencyEndpoints)
		if err != nil {
			return s.Builder.SyncError(err)
		}
		if grpcEP == nil {
			continue
		}

		destination := s.Local("%s/src/gen", s.Settings.NodeSourceDir())
		w.Info("generating TypeScript Connect-ES client",
			wool.Field("dependency", dep.Name),
			wool.Field("destination", destination))

		err = proto.GenerateGRPC(ctx, languages.TYPESCRIPT, destination, dep.Unique(), grpcEP)
		if err != nil {
			return s.Builder.SyncError(err)
		}
	}

	return s.Builder.SyncResponse()
}

type DockerTemplating struct {
	NodeVersion string
	Static      bool
}

const NodeVersion = "24"

func (s *Builder) Build(ctx context.Context, req *builderv0.BuildRequest) (*builderv0.BuildResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	dockerRequest, err := s.Builder.DockerBuildRequest(ctx, req)
	if err != nil {
		return nil, s.Wool.Wrapf(err, "can only do docker build request")
	}

	image := s.DockerImage(dockerRequest)

	s.Wool.Debug("building docker image", wool.Field("image", image.FullName()))
	if !dockerhelpers.IsValidDockerImageName(image.Name) {
		return s.Builder.BuildError(fmt.Errorf("invalid docker image name: %s", image.Name))
	}

	docker := DockerTemplating{
		NodeVersion: NodeVersion,
		Static:      s.Settings.IsStatic(),
	}

	err = shared.DeleteFile(ctx, s.Local("builder/Dockerfile"))
	if err != nil {
		return s.Builder.BuildError(err)
	}

	err = s.Templates(ctx, docker, services.WithBuilder(builderFS))
	if err != nil {
		return s.Builder.BuildError(err)
	}

	builder, err := dockerhelpers.NewBuilder(dockerhelpers.BuilderConfiguration{
		Root:        s.Location,
		Dockerfile:  "builder/Dockerfile",
		Ignorefile:  "builder/dockerignore",
		Destination: image,
		Output:      s.Wool,
	})
	if err != nil {
		return s.Builder.BuildError(err)
	}
	_, err = builder.Build(ctx)
	if err != nil {
		return s.Builder.BuildError(err)
	}
	s.Builder.WithDockerImages(image)
	return s.Builder.BuildResponse()
}

// Audit scans the Next.js project for vulnerabilities (npm audit) and
// optionally reports outdated packages (npm outdated). Runs at the node
// source root (s.Settings.NodeSourceDir()).
func (s *Builder) Audit(ctx context.Context, req *builderv0.AuditRequest) (*builderv0.AuditResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	dir := s.Local("%s", s.Settings.NodeSourceDir())
	res, err := audit.Node(ctx, dir, req.IncludeOutdated)
	if err != nil {
		return s.Builder.AuditError(err)
	}
	return s.Builder.AuditResponse(res.Findings, res.Outdated, res.Tool, res.Language)
}

// Upgrade bumps npm dependencies in package.json (npm update by default,
// npm install <pkg>@latest for --major). --dry-run skips the write.
func (s *Builder) Upgrade(ctx context.Context, req *builderv0.UpgradeRequest) (*builderv0.UpgradeResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	dir := s.Local("%s", s.Settings.NodeSourceDir())
	res, err := upgrade.Node(ctx, dir, upgrade.Options{
		IncludeMajor: req.IncludeMajor,
		DryRun:       req.DryRun,
		Only:         req.Only,
	})
	if err != nil {
		return s.Builder.UpgradeError(err)
	}
	return s.Builder.UpgradeResponse(res.Changes, res.LockfileDiff)
}

func (s *Builder) Deploy(ctx context.Context, req *builderv0.DeploymentRequest) (*builderv0.DeploymentResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Builder.LogDeployRequest(req, s.Wool.Debug)

	s.EnvironmentVariables.SetRunning()

	var k *builderv0.KubernetesDeployment
	var err error
	if k, err = s.Builder.KubernetesDeploymentRequest(ctx, req); err != nil {
		return s.Builder.DeployError(err)
	}

	err = s.EnvironmentVariables.AddEndpoints(ctx,
		resources.LocalizeNetworkMapping(req.NetworkMappings, "localhost"),
		resources.NewContainerNetworkAccess())
	if err != nil {
		return s.Builder.DeployError(err)
	}

	err = s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings,
		resources.NewContainerNetworkAccess())
	if err != nil {
		return s.Builder.DeployError(err)
	}

	err = s.EnvironmentVariables.AddConfigurations(ctx, req.DependenciesConfigurations...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	confs, err := s.EnvironmentVariables.Configurations()
	if err != nil {
		return s.Builder.DeployError(err)
	}
	cm, err := services.EnvsAsConfigMapData(confs...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	secrets, err := services.EnvsAsSecretData(s.EnvironmentVariables.Secrets()...)
	if err != nil {
		return s.Builder.DeployError(err)
	}

	params := services.DeploymentParameters{
		ConfigMap: cm,
		SecretMap: secrets,
	}

	err = s.Builder.KustomizeDeploy(ctx, req.Environment, k, deploymentFS, params)
	if err != nil {
		return s.Builder.DeployError(err)
	}
	return s.Builder.DeployResponse()
}

func (s *Builder) Options() []*agentv0.Question {
	// Only present these questions during Create. Sync / Update / Build
	// load settings from service.codefly.yaml — re-prompting there would
	// block non-interactive CLI flows (codefly sync, CI, MCP) on an
	// unanswerable question.
	if s.Builder.CreationMode == nil {
		return nil
	}
	return []*agentv0.Question{
		communicate.NewSelection(
			&agentv0.Message{Name: Mode, Message: "Deployment mode?", Description: "SSR runs a Node.js server (dynamic apps, auth, API routes). Static exports plain HTML/CSS/JS (corporate sites, docs)."},
			&agentv0.Message{Name: "ssr", Message: "SSR (Server-Side Rendering)", Description: "Node.js server with server components, API routes, middleware"},
			&agentv0.Message{Name: "static", Message: "Static Export", Description: "Plain HTML/CSS/JS served from CDN or nginx"},
		),
		communicate.NewSelection(
			&agentv0.Message{Name: AuthProviderOption, Message: "Auth provider?", Description: "Choose an authentication provider. WorkOS provides hosted login UI, SSO, and user management via AuthKit."},
			&agentv0.Message{Name: "none", Message: "None (placeholder)", Description: "Scaffold placeholder auth — replace later with your provider of choice"},
			&agentv0.Message{Name: "workos", Message: "WorkOS AuthKit", Description: "Production-ready auth with SSO, social login, and hosted UI via WorkOS"},
		),
		communicate.NewConfirm(&agentv0.Message{Name: HotReload, Message: "Code hot-reload (Recommended)?", Description: "codefly can restart your service when code changes are detected"}, true),
	}
}

type CreateConfiguration struct {
	*services.Information
	WorkOS bool
}

func (s *Builder) Create(ctx context.Context, req *builderv0.CreateRequest) (*builderv0.CreateResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if s.Builder.CreationMode != nil && s.Builder.CreationMode.Communicate && s.answers != nil {
		selection, err := communicate.Selection(s.answers, Mode)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		if selection != nil && len(selection.Selected) > 0 {
			s.Settings.Mode = selection.Selected[0]
		}
		s.Settings.HotReload, err = communicate.Confirm(s.answers, HotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		authSelection, err := communicate.Selection(s.answers, AuthProviderOption)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		if authSelection != nil && len(authSelection.Selected) > 0 {
			s.Settings.AuthProvider = authSelection.Selected[0]
		}
	} else {
		// Defaults: SSR mode with hot-reload, no auth provider
		s.Settings.Mode = "ssr"
		s.Settings.HotReload = true
		s.Settings.AuthProvider = "none"
	}

	create := CreateConfiguration{
		Information: s.Information,
		WorkOS:      s.Settings.IsWorkOS(),
	}
	ignore := shared.NewIgnore("node_modules", ".next", "service.generation.codefly.yaml")

	err := s.Templates(ctx, create, services.WithFactory(factoryFS).WithPathSelect(ignore))
	if err != nil {
		return s.Builder.CreateError(err)
	}

	// For static mode, override next.config.ts to use "export" output
	if s.Settings.IsStatic() {
		configContent := []byte("import type { NextConfig } from \"next\";\n\nconst nextConfig: NextConfig = {\n  output: \"export\",\n};\n\nexport default nextConfig;\n")
		err = os.WriteFile(s.Local("%s/next.config.ts", s.Settings.NodeSourceDir()), configContent, 0644)
		if err != nil {
			return s.Builder.CreateError(err)
		}
	}

	err = s.CreateEndpoints(ctx)
	if err != nil {
		return s.Builder.CreateError(s.Wool.Wrapf(err, "cannot create endpoints"))
	}

	return s.Builder.CreateResponse(ctx, s.Settings)
}

func (s *Builder) CreateEndpoints(ctx context.Context) error {
	httpAPI, err := resources.LoadHTTPAPI(ctx)
	if err != nil {
		return s.Wool.Wrapf(err, "cannot load http api")
	}
	endpoint := s.Base.BaseEndpoint(standards.HTTP)
	endpoint.Visibility = resources.VisibilityPublic
	s.HttpEndpoint, err = resources.NewAPI(ctx, endpoint, resources.ToHTTPAPI(httpAPI))
	if err != nil {
		return s.Wool.Wrapf(err, "cannot create http endpoint")
	}
	s.Endpoints = []*v0.Endpoint{s.HttpEndpoint}
	return nil
}

func (s *Builder) Communicate(stream builderv0.Builder_CommunicateServer) error {
	asker := communicate.NewQuestionAsker(stream)
	answers, err := asker.RunSequence(s.Options())
	if err != nil {
		return err
	}
	s.answers = answers
	return nil
}

//go:embed all:templates/factory
var factoryFS embed.FS

//go:embed templates/builder
var builderFS embed.FS

//go:embed templates/deployment
var deploymentFS embed.FS

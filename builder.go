package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codefly-dev/core/agents/communicate"
	dockerhelpers "github.com/codefly-dev/core/agents/helpers/docker"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/agents/services/audit"
	"github.com/codefly-dev/core/agents/services/sbom"
	"github.com/codefly-dev/core/agents/services/upgrade"
	proto "github.com/codefly-dev/core/companions/proto"
	v0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/standards"
	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Builder struct {
	*services.DefaultBuilder
	*Service

	answers map[string]*agentv0.Answer

	relativeToWorkspace string
}

func NewBuilder(service *Service) *Builder {
	return &Builder{
		DefaultBuilder: services.NewDefaultBuilder(service.Builder),
		Service:        service,
	}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()
	response, err := s.Builder.LoadService(ctx, req, services.BuilderLoad{
		Settings:         s.Settings,
		Requirements:     requirements,
		FactoryTemplates: factoryFS,
		ResolveEndpoints: func(ctx context.Context, endpoints []*v0.Endpoint) error {
			endpoint, resolveErr := resources.FindHTTPEndpoint(ctx, endpoints)
			if resolveErr != nil {
				return resolveErr
			}
			s.HttpEndpoint = endpoint
			return nil
		},
	})
	s.sourceLocation = s.Local("%s", s.Settings.NodeSourceDir())
	if req.GetIdentity() != nil {
		s.relativeToWorkspace = serviceWorkspaceRelative(
			req.GetIdentity().GetWorkspacePath(),
			s.Location,
			req.GetIdentity().GetRelativeToWorkspace(),
		)
	}
	return response, err
}

func serviceWorkspaceRelative(workspaceRoot, serviceRoot, fallback string) string {
	if physicalWorkspace, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceRoot = physicalWorkspace
	}
	if physicalService, err := filepath.EvalSymlinks(serviceRoot); err == nil {
		serviceRoot = physicalService
	}
	relative, err := filepath.Rel(workspaceRoot, serviceRoot)
	if err == nil && relative != "." && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(filepath.Clean(fallback))
}

func (s *Builder) Init(ctx context.Context, req *builderv0.InitRequest) (*builderv0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Builder.LogInitRequest(req)

	s.DependencyEndpoints = req.DependenciesEndpoints

	return s.Builder.InitResponse()
}

func (s *Builder) Sync(ctx context.Context, req *builderv0.SyncRequest) (*builderv0.SyncResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	w := s.Wool

	destination := s.Local("%s/src/gen", s.Settings.NodeSourceDir())
	generateDestination := destination
	var temporary string
	if syncDryRun(req) {
		var err error
		temporary, err = os.MkdirTemp("", "codefly-nextjs-sync-*")
		if err != nil {
			return s.Builder.SyncError(err)
		}
		defer os.RemoveAll(temporary)
		generateDestination = filepath.Join(temporary, "gen")
	}

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

		w.Info("generating TypeScript Connect-ES client",
			wool.Field("dependency", dep.Name),
			wool.Field("destination", generateDestination))

		err = proto.GenerateGRPC(ctx, languages.TYPESCRIPT, generateDestination, dep.Unique(), grpcEP)
		if err != nil {
			return s.Builder.SyncError(err)
		}
	}

	response, err := s.Builder.SyncResponse()
	if err != nil || !syncDryRun(req) {
		return response, err
	}
	prefix := filepath.Join(s.relativeToWorkspace, s.Settings.NodeSourceDir(), "src", "gen")
	changed, err := changedGeneratedFiles(destination, generateDestination, prefix)
	if err != nil {
		return s.Builder.SyncError(err)
	}
	if err := setSyncChangedFiles(response, changed); err != nil {
		return s.Builder.SyncError(err)
	}
	return response, nil
}

func syncDryRun(request *builderv0.SyncRequest) bool {
	if request == nil {
		return false
	}
	message := request.ProtoReflect()
	field := message.Descriptor().Fields().ByName("dry_run")
	return field != nil && message.Get(field).Bool()
}

func setSyncChangedFiles(response *builderv0.SyncResponse, changed []string) error {
	message := response.ProtoReflect()
	field := message.Descriptor().Fields().ByName("changed_files")
	if field == nil || !field.IsList() {
		if len(changed) == 0 {
			return nil
		}
		return fmt.Errorf("sync response contract does not expose changed_files")
	}
	list := message.Mutable(field).List()
	for _, path := range changed {
		list.Append(protoreflect.ValueOfString(path))
	}
	return nil
}

type generatedFile struct {
	kind string
	data []byte
}

func changedGeneratedFiles(actualRoot, expectedRoot, workspacePrefix string) ([]string, error) {
	actual, err := generatedFiles(actualRoot)
	if err != nil {
		return nil, err
	}
	expected, err := generatedFiles(expectedRoot)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for path := range actual {
		paths[path] = true
	}
	for path := range expected {
		paths[path] = true
	}
	var changed []string
	for path := range paths {
		left, leftOK := actual[path]
		right, rightOK := expected[path]
		if leftOK && rightOK && left.kind == right.kind && bytes.Equal(left.data, right.data) {
			continue
		}
		changed = append(changed, filepath.ToSlash(filepath.Join(workspacePrefix, path)))
	}
	sort.Strings(changed)
	return changed, nil
}

func generatedFiles(root string) (map[string]generatedFile, error) {
	files := map[string]generatedFile{}
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = generatedFile{kind: "symlink", data: []byte(target)}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = generatedFile{kind: "file", data: data}
		return nil
	})
	return files, err
}

type DockerTemplating struct {
	NodeImage string
	Static    bool
}

// NodeImage matches the tested SaaS Starter frontend build substrate. Keeping
// the digest here makes agent-generated builds reproducible instead of silently
// changing whenever a floating node:24-alpine tag moves.
const NodeImage = "node:24.17.0-alpine3.23@sha256:7c70d1235c0b4c2bc9eeed5393d19f1bbdde6885ba0d58ba62bb385d7b0f3ff1"

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
		NodeImage: NodeImage,
		Static:    s.Settings.IsStatic(),
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
	return s.Builder.AuditResponse(req, res.Findings, res.Outdated, res.Tool, res.Language)
}

// SBOM inventories package-lock.json without installing packages or running
// lifecycle scripts. The lockfile, not ambient node_modules, is authoritative.
func (s *Builder) SBOM(ctx context.Context, req *builderv0.SBOMRequest) (*builderv0.SBOMResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	dir := s.Local("%s", s.Settings.NodeSourceDir())
	result, err := sbom.Node(ctx, dir, req.GetIncludeDevDependencies())
	if err != nil {
		return s.Builder.SBOMError(err)
	}
	return s.Builder.SBOMResponse(result.Bom, result.Tool, result.Language, result.SHA256)
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

	return s.Builder.DeployKustomize(ctx, req, services.KustomizeDeployment{
		EnvironmentVariables: s.EnvironmentVariables,
		Templates:            deploymentFS,
		Inputs: services.DeploymentInputs{
			OwnEndpoints:             true,
			DependencyEndpoints:      true,
			DependencyConfigurations: true,
		},
	})
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
		communicate.NewChoice(
			&agentv0.Message{Name: Mode, Message: "Deployment mode?", Description: "SSR runs a Node.js server (dynamic apps, auth, API routes). Static exports plain HTML/CSS/JS (corporate sites, docs)."},
			&agentv0.Message{Name: "ssr", Message: "SSR (Server-Side Rendering)", Description: "Node.js server with server components, API routes, middleware"},
			&agentv0.Message{Name: "static", Message: "Static Export", Description: "Plain HTML/CSS/JS served from CDN or nginx"},
		),
		communicate.NewChoice(
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
		selection, err := communicate.Choice(s.answers, Mode)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		if selection != nil && selection.Option != "" {
			s.Settings.Mode = selection.Option
		}
		s.Settings.HotReload, err = communicate.Confirm(s.answers, HotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		authSelection, err := communicate.Choice(s.answers, AuthProviderOption)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		if authSelection != nil && authSelection.Option != "" {
			s.Settings.AuthProvider = authSelection.Option
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

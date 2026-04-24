package main

import (
	"context"
	"embed"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/builders"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
)

// Agent version
var agent = shared.Must(resources.LoadFromFs[resources.Agent](shared.Embed(infoFS)))

// runtimeImage is the codefly-built Node runtime companion —
// node:22.12.0-alpine3.21 + codefly CLI + the shared dev toolbox.
// Built from core/companions/node/. Users CAN override via the nextjs
// settings (DockerImage field) but it's NOT recommended; the companion
// image is the mode-consistent default and gets rebuilt + pinned on
// every codefly release.
var runtimeImage = &resources.DockerImage{Name: "codeflydev/node", Tag: "0.0.11"}

var requirements = builders.NewDependencies(agent.Name,
	builders.NewDependency("service.codefly.yaml"),
	builders.NewDependency("code").WithPathSelect(shared.NewSelect("*.ts", "*.tsx", "*.js", "*.jsx", "*.css")),
)

type Settings struct {
	Mode         string `yaml:"mode"`          // "ssr" (default) or "static"
	HotReload    bool   `yaml:"hot-reload"`
	SourceDir    string `yaml:"source-dir"`    // Next.js source directory relative to service root. Default: "code"
	AuthProvider string `yaml:"auth-provider"` // "none" (default), "workos"

	// RuntimeImage overrides the codefly-built runtime image. Format:
	// "name:tag". :latest and untagged refs are rejected — pinning is
	// enforced. Leave empty to use codeflydev/node:<ver> (recommended).
	// Field named RuntimeImage (not DockerImage) to avoid colliding with
	// services.Base.DockerImage(req) which is the build-time image method.
	RuntimeImage string `yaml:"docker-image"`
}

// NodeSourceDir returns the configured source directory, defaulting to "code".
func (s *Settings) NodeSourceDir() string {
	if s.SourceDir != "" {
		return s.SourceDir
	}
	return "code"
}

// IsStatic returns true when the service should be built as a static export.
func (s *Settings) IsStatic() bool {
	return s.Mode == "static"
}

// IsWorkOS returns true when WorkOS AuthKit is the auth provider.
func (s *Settings) IsWorkOS() bool {
	return s.AuthProvider == "workos"
}

const HotReload = "hot-reload"
const Mode = "mode"
const AuthProviderOption = "auth-provider"

type Service struct {
	*services.Base

	// Endpoints
	HttpEndpoint *basev0.Endpoint

	// Settings
	*Settings

	sourceLocation string
}

func (s *Service) GetAgentInformation(ctx context.Context, _ *agentv0.AgentInformationRequest) (*agentv0.AgentInformation, error) {

	info := s.Information
	if info == nil {
		info = &services.Information{}
	}
	readme, err := templates.ApplyTemplateFrom(ctx, shared.Embed(readmeFS), "templates/agent/README.md", info)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &agentv0.AgentInformation{
		RuntimeRequirements: []*agentv0.Runtime{
			{Type: agentv0.Runtime_NPM},
		},
		Capabilities: []*agentv0.Capability{
			{Type: agentv0.Capability_BUILDER},
			{Type: agentv0.Capability_RUNTIME},
			{Type: agentv0.Capability_HOT_RELOAD},
		},
		Languages: []*agentv0.Language{
			{Type: agentv0.Language_TYPESCRIPT},
		},
		Protocols: []*agentv0.Protocol{
			{Type: agentv0.Protocol_HTTP},
		},
		ReadMe: readme,
	}, nil
}

func NewService() *Service {
	return &Service{
		Base:     services.NewServiceBase(context.Background(), agent.Of(resources.ServiceAgent)),
		Settings: &Settings{},
	}
}

func main() {
	svc := NewService()
	code := NewCode(svc)
	runtime := NewRuntime()
	agents.Serve(agents.PluginRegistration{
		Agent:   svc,
		Code:    code,
		Tooling: NewTooling(code, runtime),
		Runtime: runtime,
		Builder: NewBuilder(),
	})
}

//go:embed agent.codefly.yaml
var infoFS embed.FS

//go:embed templates/agent
var readmeFS embed.FS

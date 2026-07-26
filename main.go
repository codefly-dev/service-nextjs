package main

import (
	"context"
	"embed"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/builders"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	runnersbase "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/toolbox/lang"
)

// Agent version
var agent = shared.Must(resources.LoadFromFs[resources.Agent](shared.Embed(infoFS)))

// runtimeImage is the codefly-built Node runtime companion —
// node:24.16.0-alpine3.22 + codefly CLI + the shared dev toolbox.
// Built from core/companions/node/. Users CAN override via the nextjs
// settings (DockerImage field) but it's NOT recommended; the companion
// image is the mode-consistent default and gets rebuilt + pinned on
// every codefly release.
var runtimeImage = &resources.DockerImage{Name: "codeflydev/node", Tag: "0.0.12"}

var requirements = builders.NewDependencies(agent.Name,
	builders.NewDependency("service.codefly.yaml"),
	builders.NewDependency("code").WithPathSelect(shared.NewSelect("*.ts", "*.tsx", "*.js", "*.jsx", "*.css")),
)

type Settings struct {
	Mode              string            `yaml:"mode"` // "ssr" (default) or "static"
	HotReload         bool              `yaml:"hot-reload"`
	SourceDir         string            `yaml:"source-dir"`         // Next.js source directory relative to service root. Default: "code"
	AuthProvider      string            `yaml:"auth-provider"`      // "none" (default), "workos"
	ExecutionProfiles map[string]string `yaml:"execution-profiles"` // Codefly environment name → "development" or "production"

	// RuntimeImage overrides the codefly-built runtime image. Format:
	// "name:tag". :latest and untagged refs are rejected — pinning is
	// enforced. Leave empty to use codeflydev/node:<ver> (recommended).
	// Field named RuntimeImage (not DockerImage) to avoid colliding with
	// services.Base.DockerImage(req) which is the build-time image method.
	RuntimeImage string `yaml:"docker-image"`
}

type NextExecutionProfile string

const (
	NextExecutionDevelopment NextExecutionProfile = "development"
	NextExecutionProduction  NextExecutionProfile = "production"
)

// ExecutionProfileFor resolves runtime behavior from an explicit Codefly
// environment-to-profile mapping. Local remains development by default for
// backwards compatibility. Every non-local environment must opt into a real
// profile so a production run can never silently start `next dev`.
func (s *Settings) ExecutionProfileFor(environment string) (NextExecutionProfile, error) {
	if environment == "" {
		environment = "local"
	}
	value, configured := s.ExecutionProfiles[environment]
	if !configured {
		if environment == "local" {
			return NextExecutionDevelopment, nil
		}
		return "", fmt.Errorf(
			"Next.js environment %q requires spec.execution-profiles.%s to be development or production",
			environment,
			environment,
		)
	}
	switch NextExecutionProfile(value) {
	case NextExecutionDevelopment:
		return NextExecutionDevelopment, nil
	case NextExecutionProduction:
		if s.IsStatic() {
			return "", fmt.Errorf(
				"Next.js production runtime profile currently requires mode ssr; static services run from their built deployment image",
			)
		}
		return NextExecutionProduction, nil
	default:
		return "", fmt.Errorf(
			"invalid Next.js execution profile %q for environment %q; expected development or production",
			value,
			environment,
		)
	}
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

	advertisement := services.Advertisement{
		Backends: runnersbase.BackendSupport{
			Local:  func() bool { return languages.HasNodeRuntime(nil) },
			Nix:    true,
			Docker: true,
		},
		Toolchains: []agentv0.Toolchain_Type{agentv0.Toolchain_NPM},
		HotReload:  true,
		Languages:  []agentv0.Language_Type{agentv0.Language_TYPESCRIPT},
		Protocols:  []agentv0.Protocol_Type{agentv0.Protocol_HTTP},
		ReadMe:     readme,
		Validation: nextValidationCapabilities(),
	}.Build()
	return advertisement, nil
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
	runtime := NewRuntime(svc)
	tooling := NewTooling(code, runtime)
	agents.Serve(agents.PluginRegistration{
		Agent:   svc,
		Code:    code,
		Tooling: tooling,
		Toolbox: lang.NewValidationToolboxFromTooling(agent.Name, agent.Version, tooling),
		Runtime: runtime,
		Builder: NewBuilder(svc),
	})
}

//go:embed agent.codefly.yaml
var infoFS embed.FS

//go:embed templates/agent
var readmeFS embed.FS

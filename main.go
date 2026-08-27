package main

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
var runtimeImage = &resources.DockerImage{Name: "codeflydev/node", Tag: "0.0.13"}

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
	// ReadinessTimeout optionally overrides the profile-aware startup probe
	// deadline. Use a Go duration such as "90s" or "3m". Development defaults
	// longer because the first request can cold-compile the application.
	ReadinessTimeout string `yaml:"readiness-timeout,omitempty"`

	// RuntimeImage overrides the codefly-built runtime image. Format:
	// "name:tag". :latest and untagged refs are rejected — pinning is
	// enforced. Leave empty to use codeflydev/node:<ver> (recommended).
	// Field named RuntimeImage (not DockerImage) to avoid colliding with
	// services.Base.DockerImage(req) which is the build-time image method.
	RuntimeImage string `yaml:"docker-image"`

	// ConfigMounts project ConfigMaps as read-only files into the deployed
	// pod — the file-based config seam, as opposed to envFrom which only
	// exposes ConfigMap keys as environment variables. The ConfigMap is named,
	// not created here, so it can be supplied out-of-band per environment.
	ConfigMounts []ConfigMount `yaml:"config-mounts,omitempty"`

	// BuildArgs are Docker build arguments the application build needs. Each is
	// declared as an ARG in the generated Dockerfile and promoted to an ENV so
	// `next build` reads it — e.g. FRONTEND_SKIN_RUNTIME=1 to keep the SSR skin
	// resolver active instead of statically prerendering the compiled default.
	// They are carried in the emitted build recipe so the CLI-owned docker build
	// passes each with --build-arg.
	BuildArgs map[string]string `yaml:"build-args,omitempty"`

	// Environment declares plain container environment variables. Each entry is
	// fed into the env manager as KEY=VAL (no CODEFLY__ prefix) so it renders
	// into the deploy ConfigMap and reaches the container through envFrom — the
	// env-var seam, as opposed to ConfigMounts which projects files.
	Environment map[string]string `yaml:"environment,omitempty"`
}

// EnvironmentKeys returns the declared environment-variable names in sorted
// order so entries feed into the env manager in a stable order. The rendered
// ConfigMap is already deterministic — its template sorts map keys — so this
// only fixes the feed order, matching the BuildArgKeys convention.
func (s *Settings) EnvironmentKeys() []string {
	keys := make([]string, 0, len(s.Environment))
	for key := range s.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// BuildArgKeys returns the declared build-arg names in sorted order, so the
// generated Dockerfile and the emitted recipe are deterministic regardless of
// map iteration order.
func (s *Settings) BuildArgKeys() []string {
	keys := make([]string, 0, len(s.BuildArgs))
	for key := range s.BuildArgs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ConfigMount is the spec.config-mounts entry mapped onto core's typed
// services.ConfigMount for rendering.
type ConfigMount struct {
	// Name becomes the pod volume name and so must be a DNS-1123 label
	// (lowercase alphanumeric and '-'); left empty it is derived from ConfigMap.
	Name string `yaml:"name"`
	// ConfigMap is the ConfigMap projected into the pod. It is named, not
	// created here, so it can be supplied out-of-band per environment.
	ConfigMap string `yaml:"config-map"`
	// MountPath must be an absolute container path.
	MountPath string `yaml:"mount-path"`
	// Optional lets the pod start even when the ConfigMap is absent.
	Optional bool `yaml:"optional,omitempty"`
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

const (
	developmentReadinessTimeout = 2 * time.Minute
	productionReadinessTimeout  = 30 * time.Second
	maximumReadinessTimeout     = 10 * time.Minute
)

// ReadinessTimeoutFor returns a bounded, profile-aware process readiness
// deadline. A cold development server may compile its first route, while an
// immutable production build should start quickly. Operators can override the
// default without requiring a new agent build.
func (s *Settings) ReadinessTimeoutFor(profile NextExecutionProfile) (time.Duration, error) {
	configured := strings.TrimSpace(s.ReadinessTimeout)
	if configured == "" {
		if profile == NextExecutionProduction {
			return productionReadinessTimeout, nil
		}
		return developmentReadinessTimeout, nil
	}
	timeout, err := time.ParseDuration(configured)
	if err != nil {
		return 0, fmt.Errorf(
			"invalid Next.js readiness timeout %q: use a duration such as 90s or 3m",
			configured,
		)
	}
	if timeout <= 0 || timeout > maximumReadinessTimeout {
		return 0, fmt.Errorf(
			"invalid Next.js readiness timeout %q: expected a value greater than zero and at most %s",
			configured,
			maximumReadinessTimeout,
		)
	}
	return timeout, nil
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
	sourceMu       sync.Mutex
}

// resolveSourceLocation binds read-only Code and Tooling traffic to the real
// configured project source before Runtime.Load. Core owns declaration
// hydration, containment, and physical attachment resolution.
func (s *Service) resolveSourceLocation(ctx context.Context) (string, error) {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if s.sourceLocation != "" {
		return s.sourceLocation, nil
	}
	location, err := s.Base.ResolveSourceLocation(ctx, s.Settings, s.Settings.NodeSourceDir)
	if err != nil {
		return "", err
	}
	s.sourceLocation = location
	return location, nil
}

func (s *Service) currentSourceLocation() string {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	return s.sourceLocation
}

func (s *Service) setSourceLocation(location string) {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if physical, err := filepath.EvalSymlinks(location); err == nil {
		location = physical
	}
	s.sourceLocation = location
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
		Languages: []agentv0.Language_Type{
			agentv0.Language_TYPESCRIPT,
			agentv0.Language_JAVASCRIPT,
		},
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

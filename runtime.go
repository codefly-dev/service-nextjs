package main

import (
	"context"
	"fmt"
	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/builders"
	agentv0 "github.com/codefly-dev/core/generated/go/services/agent/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/services/runtime/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
	"github.com/hashicorp/go-multierror"
	"os"
	"path"
)

type Runtime struct {
	*Service

	// Internal
	port              uint16
	runner            runners.Proc
	runnerEnvironment runners.RunnerEnvironment
	cacheLocation     string
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogLoadRequest(req)

	s.Runtime.SetEnvironment(req.Environment)

	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Runtime.LoadError(err)
	}

	s.EnvironmentVariables.SetEnvironment(req.Environment)

	s.sourceLocation = s.Local("code")

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Runtime.LoadError(err)
	}

	s.HttpEndpoint, err = resources.FindHTTPEndpoint(ctx, s.Endpoints)
	if err != nil {
		return s.Runtime.LoadError(err)
	}

	return s.Runtime.LoadResponse()
}

func (s *Runtime) CreateRunnerEnvironment(ctx context.Context) error {
	s.Wool.Debug("creating runner environment in", wool.DirField(s.Identity.WorkspacePath))
	if s.Runtime.IsContainerRuntime() {
		dockerEnv, err := runners.NewDockerEnvironment(ctx, runtimeImage, s.Identity.WorkspacePath, s.UniqueWithWorkspace())
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker runner")
		}
		dockerEnv.WithPause()
		// Need to bind the ports
		instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewContainerNetworkAccess())
		if err != nil {
			return s.Wool.Wrapf(err, "cannot find network instance")
		}
		dockerEnv.WithPort(ctx, uint16(instance.Port))
		modulesPath := s.DockerNodeModulesPath()
		_, err = shared.CheckDirectoryOrCreate(ctx, modulesPath)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create docker venv environment")
		}

		dockerEnv.WithMount(modulesPath, path.Join(s.sourceLocation, "node_modules"))
		s.cacheLocation, err = s.LocalDirCreate(ctx, ".cache/container")

		if err != nil {
			return s.Wool.Wrapf(err, "cannot create cache location")
		}
		s.runnerEnvironment = dockerEnv
	} else {
		localEnv, err := runners.NewNativeEnvironment(ctx, s.sourceLocation)
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create local runner")
		}
		err = localEnv.WithBinary("node")
		if err != nil {
			return s.Wool.Wrapf(err, "cannot find node binary")
		}
		localEnv.WithEnvironmentVariables(resources.Env("PATH", os.Getenv("PATH")))
		s.cacheLocation, err = s.LocalDirCreate(ctx, ".cache/local")
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create cache location")
		}
		s.runnerEnvironment = localEnv
	}
	s.runnerEnvironment.WithEnvironmentVariables(s.EnvironmentVariables.All()...)
	return nil
}
func (s *Runtime) SetRuntimeContext(ctx context.Context, req *runtimev0.InitRequest) error {
	if req.RuntimeContext.Kind == resources.RuntimeContextFree || req.RuntimeContext.Kind == resources.RuntimeContextNative {
		if languages.HasNodeRuntime(nil) {
			s.Runtime.RuntimeContext = resources.NewRuntimeContextNative()
			return nil
		}
	}
	s.Runtime.RuntimeContext = resources.NewRuntimeContextContainer()
	return nil
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogInitRequest(req)

	err := s.SetRuntimeContext(ctx, req)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.Wool.Forwardf("starting execution environment in %s mode", s.Runtime.RuntimeContext.Kind)

	s.EnvironmentVariables.SetRuntimeContext(s.Runtime.RuntimeContext)

	s.NetworkMappings = req.ProposedNetworkMappings

	// Networking
	instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.Infof("will run on http://localhost:%d", instance.Port)
	s.port = uint16(instance.Port)

	err = s.CreateRunnerEnvironment(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	err = s.runnerEnvironment.Init(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	deps := builders.NewDependencies("package", builders.NewDependency(path.Join(s.sourceLocation, "package.json"))).WithCache(s.cacheLocation)
	depsUpdate, err := deps.Updated(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}
	if depsUpdate {
		s.Infof("update npm packages")
		// npm install
		s.Infof("installing dependencies, may take a while")
		proc, err := s.runnerEnvironment.NewProcess("npm", "install")
		if err != nil {
			return s.Runtime.InitError(err)
		}

		proc.WithDir(s.sourceLocation)
		err = proc.Run(ctx)
		if err != nil {
			return s.Runtime.InitError(err)
		}
		err = deps.UpdateCache(ctx)
	}

	s.NetworkMappings = req.ProposedNetworkMappings

	return s.Runtime.InitResponse()
}

func (s *Runtime) DockerNodeModulesPath() string {
	return s.Local(".cache/container/node_modules")
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogStartRequest(req)

	err := s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings, resources.NewPublicNetworkAccess())
	if err != nil {
		return s.Runtime.StartErrorf(err, "adding external endpoints")
	}

	err = s.EnvironmentVariables.AddRestRoutes(ctx, req.DependenciesNetworkMappings, resources.NewPublicNetworkAccess())
	if err != nil {
		return s.Runtime.StartErrorf(err, "adding rest routes")
	}

	envs := s.EnvironmentVariables.All()
	s.Wool.Debug("environment variables", wool.Field("envs", envs))

	// Generate the .env.local
	s.Wool.Debug("copying special files")
	err = templates.CopyAndApplyTemplate(ctx, shared.Embed(specialFS),
		"templates/factory/special/env.local.tmpl",
		s.Local("code/.env.local"), envs)
	if err != nil {
		return s.Runtime.StartErrorf(err, "copying special files")
	}

	// We have hot-reloading built-in
	if s.runner != nil {
		s.Wool.Debug("using built-in hot reloading")
		return s.Runtime.StartResponse()
	}

	runningContext := s.Wool.Inject(context.Background())
	proc, err := s.runnerEnvironment.NewProcess("npm", "run", "dev", "--", "-p", fmt.Sprintf("%d", s.port))
	if err != nil {
		return s.Runtime.StartErrorf(err, "runner")
	}
	proc.WithOutput(s.Logger)
	proc.WithDir(s.sourceLocation)

	s.runner = proc
	err = s.runner.Start(runningContext)
	if err != nil {
		return s.Runtime.StartErrorf(err, "runner")
	}

	return s.Runtime.StartResponse()
}

func (s *Runtime) Information(ctx context.Context, req *runtimev0.InformationRequest) (*runtimev0.InformationResponse, error) {
	return s.Runtime.InformationResponse(ctx, req)
}

func (s *Runtime) Stop(ctx context.Context, req *runtimev0.StopRequest) (*runtimev0.StopResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	var agg error
	s.Wool.Debug("stopping service")
	if s.runner != nil {
		err := s.runner.Stop(ctx)
		if err != nil {
			agg = multierror.Append(agg, err)
		}
	}
	s.Wool.Debug("runner stopped")
	if s.runnerEnvironment != nil {
		err := s.runnerEnvironment.Shutdown(ctx)
		if err != nil {
			agg = multierror.Append(agg, err)
		}
	}
	err := s.Base.Stop()
	if err != nil {
		agg = multierror.Append(agg, err)
		s.Wool.Warn("error stopping runner", wool.ErrField(err))
	}
	if agg != nil {
		return s.Runtime.StopError(agg)
	}
	return s.Runtime.StopResponse()
}

func (s *Runtime) Destroy(ctx context.Context, req *runtimev0.DestroyRequest) (*runtimev0.DestroyResponse, error) {
	defer s.Wool.Catch()

	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("Destroying service")

	// Remove cache
	s.Wool.Debug("removing cache")
	err := shared.EmptyDir(ctx, s.cacheLocation)
	if err != nil {
		return s.Runtime.DestroyError(err)
	}

	// Get the runner environment
	if s.Runtime.IsContainerRuntime() {
		s.Wool.Debug("Destroying in container mode")
		dockerEnv, err := runners.NewDockerEnvironment(ctx, runtimeImage, s.sourceLocation, s.UniqueWithWorkspace())
		if err != nil {
			return s.Runtime.DestroyError(err)
		}
		err = dockerEnv.Shutdown(ctx)
		if err != nil {
			return s.Runtime.DestroyError(err)
		}
	} else {

	}
	return s.Runtime.DestroyResponse()
}

func (s *Runtime) Test(ctx context.Context, req *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	return s.Runtime.TestResponse()
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

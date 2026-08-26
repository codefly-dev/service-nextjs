package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

// When the CLI sends an output_directory, Build renders the recipe there and
// returns a DockerBuildPlan the CLI can verify and buildx, instead of running
// docker build in-process. The test never needs a docker daemon.
func TestBuildEmitsRecipeWhenOutputDirectorySet(t *testing.T) {
	ctx := context.Background()
	identity, _ := testIdentity(t, t.TempDir())

	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)

	output := t.TempDir()
	// A leftover artifact in the output directory (e.g. an ignore file a prior
	// interrupted CLI build staged) must not leak into the digested recipe tree.
	require.NoError(t, os.WriteFile(filepath.Join(output, "Dockerfile.dockerignore"), []byte("stale"), 0o644))

	response, err := builder.Build(ctx, &builderv0.BuildRequest{
		OutputDirectory: output,
		BuildContext: &builderv0.BuildContext{
			Kind: &builderv0.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv0.DockerBuildContext{
					DockerRepository: "registry.example.com",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, builderv0.BuildStatus_SUCCESS, response.GetState().GetState())

	plan := response.GetResult().GetDockerBuildPlan()
	require.NotNil(t, plan)
	require.Equal(t, services.DockerBuildRecipeContractVersion, plan.GetContractVersion())
	require.Contains(t, plan.GetDigest(), "sha256:")

	require.Len(t, plan.GetRecipes(), 1)
	recipe := plan.GetRecipes()[0]
	require.Equal(t, "frontend", recipe.GetName())
	require.Equal(t, "Dockerfile", recipe.GetDockerfile())
	require.Equal(t, ".", recipe.GetContext())
	require.Equal(t, "dockerignore", recipe.GetDockerignore())
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, recipe.GetPlatforms())
	require.Contains(t, recipe.GetImage(), "registry.example.com/mod/frontend")

	// The recipe tree is exactly what the agent rendered: the stale file is gone
	// and never entered the plan inventory.
	require.NoFileExists(t, filepath.Join(output, "Dockerfile.dockerignore"))
	var inventory []string
	for _, f := range plan.GetFiles() {
		inventory = append(inventory, f.GetPath())
	}
	require.ElementsMatch(t, []string{"Dockerfile", "dockerignore"}, inventory)

	require.FileExists(t, filepath.Join(output, "Dockerfile"))
	require.FileExists(t, filepath.Join(output, "dockerignore"))

	dockerfile, err := os.ReadFile(filepath.Join(output, "Dockerfile"))
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), NodeImage)

	// The CLI verifies the emitted tree against the plan inventory before it
	// runs buildx; the emitted plan must pass that check.
	require.NoError(t, services.VerifyDockerBuildPlan(output, plan))
}

// Declared build-args must reach the CLI-owned docker build: the emitted recipe
// carries them (so the CLI passes each with --build-arg) and the rendered
// Dockerfile declares each as an ARG promoted to an ENV so `next build` reads it.
// Without this, FRONTEND_SKIN_RUNTIME never reaches the build and Next statically
// prerenders the compiled default.
func TestBuildRecipeCarriesDeclaredBuildArgs(t *testing.T) {
	ctx := context.Background()
	identity, _ := testIdentity(t, t.TempDir())

	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	builder.Settings.BuildArgs = map[string]string{"FRONTEND_SKIN_RUNTIME": "1"}

	output := t.TempDir()
	response, err := builder.Build(ctx, &builderv0.BuildRequest{
		OutputDirectory: output,
		BuildContext: &builderv0.BuildContext{
			Kind: &builderv0.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv0.DockerBuildContext{
					DockerRepository: "registry.example.com",
				},
			},
		},
	})
	require.NoError(t, err)

	plan := response.GetResult().GetDockerBuildPlan()
	require.NotNil(t, plan)
	require.Len(t, plan.GetRecipes(), 1)
	require.Equal(t, map[string]string{"FRONTEND_SKIN_RUNTIME": "1"}, plan.GetRecipes()[0].GetBuildArgs())

	dockerfile, err := os.ReadFile(filepath.Join(output, "Dockerfile"))
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), "ARG FRONTEND_SKIN_RUNTIME")
	require.Contains(t, string(dockerfile), "ENV FRONTEND_SKIN_RUNTIME=$FRONTEND_SKIN_RUNTIME")

	// The plan the CLI verifies must still match the emitted tree.
	require.NoError(t, services.VerifyDockerBuildPlan(output, plan))
}

// With no output_directory, Build keeps the legacy in-process path: it renders
// the Dockerfile into the service's builder/ dir and runs docker build; it never
// emits a recipe plan. An empty PATH makes the buildx probe fail fast, so the
// test exercises branch selection without a docker daemon.
func TestBuildWithoutOutputDirectoryUsesInProcessBuild(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ctx := context.Background()
	tmpDir := t.TempDir()
	identity, _ := testIdentity(t, tmpDir)

	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)

	response, err := builder.Build(ctx, &builderv0.BuildRequest{
		BuildContext: &builderv0.BuildContext{
			Kind: &builderv0.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv0.DockerBuildContext{
					DockerRepository: "registry.example.com",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Nil(t, response.GetResult().GetDockerBuildPlan())
	require.FileExists(t, filepath.Join(tmpDir, "mod", "frontend", "builder", "Dockerfile"))
}

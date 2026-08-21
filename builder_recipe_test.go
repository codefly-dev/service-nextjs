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
	require.NotEmpty(t, recipe.GetName())
	require.Equal(t, "Dockerfile", recipe.GetDockerfile())
	require.Equal(t, ".", recipe.GetContext())
	require.Equal(t, "dockerignore", recipe.GetDockerignore())
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, recipe.GetPlatforms())
	require.Contains(t, recipe.GetImage(), "registry.example.com/mod/frontend")

	require.FileExists(t, filepath.Join(output, "Dockerfile"))
	require.FileExists(t, filepath.Join(output, "dockerignore"))

	dockerfile, err := os.ReadFile(filepath.Join(output, "Dockerfile"))
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), NodeImage)

	// The CLI verifies the emitted tree against the plan inventory before it
	// runs buildx; the emitted plan must pass that check.
	require.NoError(t, services.VerifyDockerBuildPlan(output, plan))
}

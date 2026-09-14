package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/agents/services/sbom"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

func loadedBuilder(t *testing.T) *Builder {
	t.Helper()
	identity, _ := testIdentity(t, t.TempDir())
	builder := NewBuilder(NewService())
	_, err := builder.Load(context.Background(), &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	return builder
}

// buildPlanSubjects is what the CLI owes this agent: the images the emitted
// recipe declares, one subject per shipped platform.
func buildPlanSubjects(t *testing.T, builder *Builder) []*builderv0.ImageSubject {
	t.Helper()
	response, err := builder.Build(context.Background(), &builderv0.BuildRequest{
		OutputDirectory: t.TempDir(),
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
	return sbom.ExpectedFromBuildPlan("frontend", plan)
}

// The recipe ships both architectures, so image coverage is only satisfied by
// evidence for each of them.
func TestBuildPlanExpectsEveryShippedPlatform(t *testing.T) {
	subjects := buildPlanSubjects(t, loadedBuilder(t))

	platforms := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		require.Equal(t, "frontend", subject.GetService())
		require.Equal(t, "frontend", subject.GetRole())
		platforms = append(platforms, subject.GetPlatform())
	}
	require.ElementsMatch(t, []string{"linux/amd64", "linux/arm64"}, platforms)
}

// seedLockfile gives the source inventory something authoritative to read, so
// the source path genuinely succeeds rather than failing for want of a fixture.
func seedLockfile(t *testing.T, builder *Builder) {
	t.Helper()
	source := builder.Local("code")
	require.NoError(t, os.MkdirAll(source, 0o755))
	lock := `{
	  "name": "frontend",
	  "version": "0.0.0",
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"name": "frontend", "version": "0.0.0", "dependencies": {"left-pad": "1.3.0"}},
	    "node_modules/left-pad": {"version": "1.3.0", "license": "WTFPL"}
	  }
	}`
	require.NoError(t, os.WriteFile(filepath.Join(source, "package-lock.json"), []byte(lock), 0o644))
}

// A successful source inventory must still be rejected as image coverage: it is
// COMPLETE for what it describes, and that is exactly the response a caller
// could otherwise mistake for proof the shipped image was scanned.
func TestSourceSBOMIsScopedToSourceAndFailsImageCoverage(t *testing.T) {
	builder := loadedBuilder(t)
	seedLockfile(t, builder)
	subjects := buildPlanSubjects(t, builder)

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_COMPLETE, response.GetState().GetState())
	require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_SOURCE, response.GetScope())
	require.NotEmpty(t, response.GetBom().GetComponents())
	require.Empty(t, response.GetImages())

	require.Error(t, sbom.ValidateCoverage(subjects, response))
}

// This agent emits a build recipe and never runs buildx, so it has no digest of
// its own. It ships an image, so neither UNSUPPORTED nor a no-image reason
// would be true: the caller must resolve subjects from the build it ran.
func TestImageSBOMWithoutSubjectsIsAPreconditionFailure(t *testing.T) {
	builder := loadedBuilder(t)

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{
		Scope: builderv0.SBOMScope_SBOM_SCOPE_IMAGE,
	})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_ERROR, response.GetState().GetState())
	require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_IMAGE, response.GetScope())
	require.Equal(t,
		basev0.FailureCode_FAILURE_CODE_PRECONDITION_FAILED,
		response.GetState().GetFailure().GetCode(),
	)
	require.Empty(t, response.GetImages())
	require.Equal(t, builderv0.NoImageReason_NO_IMAGE_REASON_UNSPECIFIED, response.GetNoImageReason())

	require.Error(t, sbom.ValidateCoverage(buildPlanSubjects(t, builder), response))
}

// A scan that cannot run fails the whole response rather than returning partial
// or empty coverage.
func TestImageSBOMPropagatesScanFailureAsImageScopedError(t *testing.T) {
	builder := loadedBuilder(t)

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{
		Scope: builderv0.SBOMScope_SBOM_SCOPE_IMAGE,
		Subjects: []*builderv0.ImageSubject{
			{Role: "frontend", Service: "frontend", Platform: "linux/amd64"},
		},
	})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_ERROR, response.GetState().GetState())
	require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_IMAGE, response.GetScope())
	require.NotEmpty(t, response.GetState().GetMessage())
	require.Empty(t, response.GetImages())
}

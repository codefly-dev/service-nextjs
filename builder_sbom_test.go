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

// unresolvableDigest pins a reference to an immutable identity that no registry
// can serve, so resolution fails before any scanner is invoked.
const unresolvableDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func buildPlan(t *testing.T, builder *Builder) *builderv0.DockerBuildPlan {
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
	return plan
}

// buildPlanSubjects is what the emitted recipe declares, one subject per shipped
// platform. They name a tag and carry no digest, which is precisely why they
// cannot stand in for subjects resolved from the build the caller actually ran.
func buildPlanSubjects(t *testing.T, builder *Builder) []*builderv0.ImageSubject {
	t.Helper()
	return sbom.ExpectedFromBuildPlan("frontend", buildPlan(t, builder))
}

// The recipe ships both architectures, so image coverage is only satisfied by
// evidence for each of them.
func TestBuildPlanExpectsEveryShippedPlatform(t *testing.T) {
	builder := loadedBuilder(t)
	plan := buildPlan(t, builder)

	require.Len(t, plan.GetRecipes(), 1)
	require.ElementsMatch(t,
		[]string{"linux/amd64", "linux/arm64"},
		plan.GetRecipes()[0].GetPlatforms(),
	)

	platforms := make([]string, 0)
	for _, subject := range sbom.ExpectedFromBuildPlan("frontend", plan) {
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

	// An unset scope means source, and an explicit source request is the same
	// answer: both must reach the inventory rather than an unsupported scope.
	for _, scope := range []builderv0.SBOMScope{
		builderv0.SBOMScope_SBOM_SCOPE_UNSPECIFIED,
		builderv0.SBOMScope_SBOM_SCOPE_SOURCE,
	} {
		response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{Scope: scope})
		require.NoError(t, err)

		require.Equal(t, builderv0.SBOMStatus_COMPLETE, response.GetState().GetState())
		require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_SOURCE, response.GetScope())
		require.NotEmpty(t, response.GetBom().GetComponents())
		require.Empty(t, response.GetImages())

		require.Error(t, sbom.ValidateCoverage(subjects, response))
	}
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

// Evidence has to name the image that was shipped. A tag can be repushed
// between the build and this scan, so a subject carrying no immutable digest is
// refused outright rather than scanned and reported as coverage for whatever
// the tag resolves to today. The plan's own subjects are exactly that input.
func TestImageSBOMRejectsSubjectsWithoutAnImmutableDigest(t *testing.T) {
	builder := loadedBuilder(t)
	subjects := buildPlanSubjects(t, builder)
	require.NotEmpty(t, subjects)
	for _, subject := range subjects {
		require.Empty(t, subject.GetDigest())
		require.NotContains(t, subject.GetReference(), "@")
	}

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{
		Scope:    builderv0.SBOMScope_SBOM_SCOPE_IMAGE,
		Subjects: subjects,
	})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_ERROR, response.GetState().GetState())
	require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_IMAGE, response.GetScope())
	require.Equal(t,
		basev0.FailureCode_FAILURE_CODE_PRECONDITION_FAILED,
		response.GetState().GetFailure().GetCode(),
	)
	require.Empty(t, response.GetImages())
	require.Error(t, sbom.ValidateCoverage(subjects, response))
}

// A scan that cannot run fails the whole response rather than returning partial
// or empty coverage, and it resolves through the registry: a local-daemon scan
// binds evidence to an image ID that no deployment references.
func TestImageSBOMPropagatesScanFailureAndResolvesThroughTheRegistry(t *testing.T) {
	builder := loadedBuilder(t)

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{
		Scope: builderv0.SBOMScope_SBOM_SCOPE_IMAGE,
		Subjects: []*builderv0.ImageSubject{{
			Reference: "codefly.invalid/frontend@" + unresolvableDigest,
			Digest:    unresolvableDigest,
			Platform:  "linux/amd64",
			Role:      "frontend",
			Service:   "frontend",
		}},
	})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_ERROR, response.GetState().GetState())
	require.Equal(t, builderv0.SBOMScope_SBOM_SCOPE_IMAGE, response.GetScope())
	require.NotEmpty(t, response.GetState().GetMessage())
	require.Empty(t, response.GetImages())
	// Only the local-daemon path reports this, so without the assertion no test
	// distinguishes the two image sources.
	require.NotContains(t, response.GetState().GetMessage(), "resolve local image")
}

// An agent built before a scope existed must not answer a question it was not
// asked by returning the source inventory it happens to have.
func TestUnknownSBOMScopeIsUnsupported(t *testing.T) {
	builder := loadedBuilder(t)
	seedLockfile(t, builder)

	response, err := builder.SBOM(context.Background(), &builderv0.SBOMRequest{
		Scope: builderv0.SBOMScope(99),
	})
	require.NoError(t, err)

	require.Equal(t, builderv0.SBOMStatus_UNSUPPORTED, response.GetState().GetState())
	require.Empty(t, response.GetBom().GetComponents())
	require.Empty(t, response.GetImages())
}

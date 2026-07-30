package main

import (
	"context"
	"os"
	"testing"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

// TestManifestGuardRender is the render entry point the shared
// codefly-dev/.github manifest-guard workflow drives. It renders the plugin's
// restricted, secret-free Kubernetes bundle into CODEFLY_MANIFEST_DESTINATION
// with no cluster access, so the guard can verify determinism and boundary
// conformance across two independent renders. It is a no-op during ordinary
// `go test`, where the destination env var is unset.
func TestManifestGuardRender(t *testing.T) {
	destination := os.Getenv("CODEFLY_MANIFEST_DESTINATION")
	if destination == "" {
		t.Skip("run by the manifest guard")
	}
	profile := builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1
	require.Equal(t, profile.String(), os.Getenv("CODEFLY_MANIFEST_PROFILE"))

	ctx := context.Background()
	identity, _ := testIdentity(t, t.TempDir())

	// A fixed naming identity keeps two independent renders byte-identical;
	// the guard rejects any inventory that changes between runs.
	environment := resources.LocalEnvironment()
	environment.Name = os.Getenv("CODEFLY_MANIFEST_ENVIRONMENT")
	environment.NamingScope = ""
	environmentProto, err := environment.Proto()
	require.NoError(t, err)

	builder := NewBuilder(NewService())
	_, err = builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)

	response, err := builder.Deploy(ctx, &builderv0.DeploymentRequest{
		Environment: environmentProto,
		Deployment: &builderv0.Deployment{
			Kind: &builderv0.Deployment_Kubernetes{
				Kubernetes: &builderv0.KubernetesDeployment{
					Namespace:   os.Getenv("CODEFLY_MANIFEST_NAMESPACE"),
					Destination: destination,
					BuildContext: &builderv0.DockerBuildContext{
						DockerRepository: "registry.example.com",
						ImageDigest:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					},
					Profile: profile,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, builderv0.DeploymentStatus_SUCCESS, response.GetState().GetState())
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	agenttesting "github.com/codefly-dev/core/agents/testing"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

func TestDeploymentTemplates(t *testing.T) {
	agenttesting.AssertKustomizeTemplates(t, deploymentFS, nil)
}

func TestDeployProfiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX shell")
	}
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "kubectl"), []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	identity, environment := testIdentity(t, t.TempDir())
	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	environmentProto, err := environment.Proto()
	require.NoError(t, err)

	tests := []struct {
		name                    string
		profile                 builderv0.KubernetesOutputProfile
		validateServerSide      bool
		serverSideValidation    builderv0.KubernetesManifestValidation_Status
		restricted              bool
		secretReferences        map[string]*builderv0.KubernetesSecretKeyReference
		dependencyConfiguration []*basev0.Configuration
	}{
		{
			name:                 "ephemeral local apply",
			profile:              builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_EPHEMERAL_LOCAL_APPLY_V1,
			serverSideValidation: builderv0.KubernetesManifestValidation_STATUS_NOT_RUN,
			dependencyConfiguration: []*basev0.Configuration{{
				Origin: "module/dependency",
				Infos: []*basev0.ConfigurationInformation{{
					Name: "auth",
					ConfigurationValues: []*basev0.ConfigurationValue{{
						Key:    "CODEFLY_TEST_SECRET",
						Value:  "secret",
						Secret: true,
					}},
				}},
			}},
		},
		{
			name:                 "restricted portable",
			profile:              builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
			validateServerSide:   true,
			serverSideValidation: builderv0.KubernetesManifestValidation_STATUS_PASSED,
			restricted:           true,
			secretReferences: map[string]*builderv0.KubernetesSecretKeyReference{
				"CODEFLY_TEST_SECRET": {
					Name:     "external-secret",
					Key:      "password",
					Optional: true,
				},
			},
		},
		{
			name:                 "restricted portable without secret references",
			profile:              builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
			validateServerSide:   true,
			serverSideValidation: builderv0.KubernetesManifestValidation_STATUS_PASSED,
			restricted:           true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := t.TempDir()
			response, err := builder.Deploy(ctx, &builderv0.DeploymentRequest{
				Environment:                environmentProto,
				DependenciesConfigurations: test.dependencyConfiguration,
				Deployment: &builderv0.Deployment{
					Kind: &builderv0.Deployment_Kubernetes{
						Kubernetes: &builderv0.KubernetesDeployment{
							Namespace:   "codefly-test",
							Destination: destination,
							BuildContext: &builderv0.DockerBuildContext{
								DockerRepository: "registry.example.com",
								ImageDigest:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
							},
							Profile:              test.profile,
							ValidateServerSide:   test.validateServerSide,
							ValidationKubeconfig: filepath.Join(binDir, "kubeconfig"),
							ValidationContext:    "k3d-codefly-test",
							SecretReferences:     test.secretReferences,
						},
					},
				},
			})
			require.NoError(t, err)
			require.Equal(t, builderv0.DeploymentStatus_SUCCESS, response.GetState().GetState())
			output := response.GetDeployment().GetKubernetes()
			require.Equal(t, test.profile, output.GetProfile())
			require.Equal(t, services.KubernetesManifestContractVersion, output.GetContractVersion())
			require.Equal(t, builderv0.KubernetesManifestValidation_STATUS_PASSED, output.GetValidation().GetStaticValidation())
			require.Equal(t, test.serverSideValidation, output.GetValidation().GetServerSideValidation())
			require.Equal(t, test.restricted, output.GetValidation().GetRestricted())

			deployment := readDeploymentFile(t, destination, "base", "deployment.yaml")
			if test.profile == builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_EPHEMERAL_LOCAL_APPLY_V1 {
				namespace := readDeploymentFile(t, destination, "base", "namespace.yaml")
				require.Contains(t, namespace, "kind: Namespace")
				require.Contains(t, namespace, "app.kubernetes.io/managed-by: codefly")

				secret := readDeploymentFile(t, destination, "overlays", environment.Name, "secret.yaml")
				require.Contains(t, secret, "kind: Secret")
				require.Contains(t, secret, "c2VjcmV0")
				require.Contains(t, deployment, "name: frontend-secret")
				require.NotContains(t, deployment, "external-secret")
				return
			}

			requireNoDeploymentFile(t, destination, "base", "namespace.yaml")
			requireNoDeploymentFile(t, destination, "overlays", environment.Name, "secret.yaml")
			require.NotContains(t, readDeploymentFile(t, destination, "base", "kustomization.yaml"), "namespace.yaml")
			require.NotContains(t, readDeploymentFile(t, destination, "overlays", environment.Name, "kustomization.yaml"), "secret.yaml")
			require.Contains(t, deployment, "image: registry.example.com/mod/frontend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			require.NotContains(t, deployment, "name: frontend-secret")
			if len(test.secretReferences) == 0 {
				require.NotContains(t, deployment, "\n          env:\n")
				require.NotContains(t, deployment, "secretKeyRef:")
			} else {
				require.Contains(t, deployment, "name: CODEFLY_TEST_SECRET")
				require.Contains(t, deployment, "name: external-secret")
				require.Contains(t, deployment, "key: password")
				require.Contains(t, deployment, "optional: true")
			}

			require.NoError(t, filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				require.NotContains(t, string(content), "kind: Secret")
				require.NotContains(t, string(content), "kind: Namespace")
				require.NotContains(t, string(content), "stringData:")
				require.NotContains(t, string(content), "c2VjcmV0")
				return nil
			}))
		})
	}
}

func TestDeployRendersConfigMounts(t *testing.T) {
	identity, environment := testIdentity(t, t.TempDir())
	environmentProto, err := environment.Proto()
	require.NoError(t, err)

	deployment := deployConfigMounts(t, identity, environmentProto, []ConfigMount{{
		Name:      "skin",
		ConfigMap: "frontend-skin",
		MountPath: "/etc/codefly/skin",
		Optional:  true,
	}})

	require.Contains(t, deployment, "            - name: skin\n              mountPath: /etc/codefly/skin\n              readOnly: true\n")
	require.Contains(t, deployment, "        - name: skin\n          configMap:\n            name: frontend-skin\n            optional: true\n")
}

func TestDeployRendersSpecEnvironmentAsContainerEnv(t *testing.T) {
	ctx := context.Background()
	identity, environment := testIdentity(t, t.TempDir())
	environmentProto, err := environment.Proto()
	require.NoError(t, err)

	builder := NewBuilder(NewService())
	_, err = builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	builder.Settings.Environment = map[string]string{"FRONTEND_SKIN_DIR": "/etc/codefly/skin"}

	destination := t.TempDir()
	response, err := builder.Deploy(ctx, &builderv0.DeploymentRequest{
		Environment: environmentProto,
		Deployment: &builderv0.Deployment{
			Kind: &builderv0.Deployment_Kubernetes{
				Kubernetes: &builderv0.KubernetesDeployment{
					Namespace:   "codefly-test",
					Destination: destination,
					BuildContext: &builderv0.DockerBuildContext{
						DockerRepository: "registry.example.com",
						ImageDigest:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					},
					Profile: builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, builderv0.DeploymentStatus_SUCCESS, response.GetState().GetState())

	// The env var lands in the deploy ConfigMap, which the container consumes
	// through envFrom in the base deployment.
	configMap := readDeploymentFile(t, destination, "overlays", environment.Name, "configmap.yaml")
	require.Contains(t, configMap, "FRONTEND_SKIN_DIR: \"/etc/codefly/skin\"")

	deployment := readDeploymentFile(t, destination, "base", "deployment.yaml")
	require.Contains(t, deployment, "- configMapRef:\n                name: frontend-config")
}

func TestDeployConfigMountsDeriveVolumeNamesAndRenderDeterministically(t *testing.T) {
	identity, environment := testIdentity(t, t.TempDir())
	// A fixed naming identity keeps two independent renders comparable, matching
	// how the manifest guard checks determinism.
	environment.NamingScope = ""
	environmentProto, err := environment.Proto()
	require.NoError(t, err)

	mounts := []ConfigMount{
		{ConfigMap: "frontend-skin", MountPath: "/etc/codefly/skin"},
		{ConfigMap: "frontend-skin", MountPath: "/etc/codefly/skin-alt"},
		{Name: "theme", ConfigMap: "frontend-theme", MountPath: "/etc/codefly/theme", Optional: true},
	}

	deployment := deployConfigMounts(t, identity, environmentProto, mounts)

	// An empty name derives a DNS-1123 volume name from the ConfigMap, kept
	// unique when the same ConfigMap is mounted twice.
	require.Contains(t, deployment, "        - name: frontend-skin\n")
	require.Contains(t, deployment, "        - name: frontend-skin-2\n")
	require.Contains(t, deployment, "        - name: theme\n")
	// Optional defaults to false and still renders explicitly.
	require.Contains(t, deployment, "            optional: false\n")

	require.Equal(t, deployment, deployConfigMounts(t, identity, environmentProto, mounts),
		"config mount rendering must be deterministic across independent renders")
}

func deployConfigMounts(t *testing.T, identity *basev0.ServiceIdentity, environment *basev0.Environment, mounts []ConfigMount) string {
	t.Helper()
	ctx := context.Background()
	builder := NewBuilder(NewService())
	_, err := builder.Load(ctx, &builderv0.LoadRequest{
		Identity:     identity,
		CreationMode: &builderv0.CreationMode{Communicate: false},
	})
	require.NoError(t, err)
	builder.Settings.ConfigMounts = mounts

	destination := t.TempDir()
	response, err := builder.Deploy(ctx, &builderv0.DeploymentRequest{
		Environment: environment,
		Deployment: &builderv0.Deployment{
			Kind: &builderv0.Deployment_Kubernetes{
				Kubernetes: &builderv0.KubernetesDeployment{
					Namespace:   "codefly-test",
					Destination: destination,
					BuildContext: &builderv0.DockerBuildContext{
						DockerRepository: "registry.example.com",
						ImageDigest:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					},
					Profile: builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, builderv0.DeploymentStatus_SUCCESS, response.GetState().GetState())
	require.Equal(t, builderv0.KubernetesManifestValidation_STATUS_PASSED, response.GetDeployment().GetKubernetes().GetValidation().GetStaticValidation())
	return readDeploymentFile(t, destination, "base", "deployment.yaml")
}

func readDeploymentFile(t *testing.T, destination string, elements ...string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(append([]string{destination}, elements...)...))
	require.NoError(t, err)
	return string(content)
}

func requireNoDeploymentFile(t *testing.T, destination string, elements ...string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(append([]string{destination}, elements...)...))
	require.ErrorIs(t, err, os.ErrNotExist)
}

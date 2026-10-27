package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

// An application that links a package beside its source (`file:../../remote`)
// compiles that package's source, so the package's own locked dependencies
// must be installed where it lives: npm links it and installs nothing of its.
func TestRuntimeInstallsTheLockOfAPackageLinkedBesideTheSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	remote := filepath.Join(root, "mod", "remote")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	copyTestdata(t, filepath.Join("testdata", "linked-package", "remote"), remote)

	runtime := NewRuntime(NewService())
	env, err := environment.Proto()
	require.NoError(t, err)
	_, err = runtime.Load(ctx, &runtimev0.LoadRequest{Identity: identity, Environment: env, DisableCatch: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := runtime.Destroy(ctx, &runtimev0.DestroyRequest{})
		require.NoError(t, err)
	})
	ready, err := runtime.Init(ctx, &runtimev0.InitRequest{RuntimeContext: resources.NewRuntimeContextNative()})
	require.NoError(t, err)
	require.Equal(t, runtimev0.InitStatus_READY, ready.GetStatus().GetState(), ready.GetStatus().GetMessage())

	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	target, err := os.Readlink(filepath.Join(source, "node_modules", "remote"))
	require.NoError(t, err, "the application links the package")
	require.Equal(t, filepath.Clean(remote), filepath.Clean(filepath.Join(source, "node_modules", target)))
	require.FileExists(t, filepath.Join(remote, "node_modules", "leaf", "package.json"),
		"the linked package's own lock is installed where it lives")

	// An installed package is not reinstalled: npm ci would remove this.
	marker := filepath.Join(remote, "node_modules", ".kept")
	require.NoError(t, os.WriteFile(marker, nil, 0o644))
	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	require.FileExists(t, marker)
}

func TestExternalLinksNameOnlyPackagesOutsideTheSource(t *testing.T) {
	lock := &nodePackageLock{}
	require.NoError(t, json.Unmarshal([]byte(`{"lockfileVersion":3,"packages":{
		"": {"name":"app"},
		"node_modules/remote": {"resolved":"../fe-remote","link":true},
		"node_modules/again": {"resolved":"../fe-remote/","link":true},
		"node_modules/workspace": {"resolved":"packages/workspace","link":true},
		"node_modules/registry": {"version":"1.0.0","resolved":"https://registry.npmjs.org/registry/-/registry-1.0.0.tgz"},
		"node_modules/tarball": {"version":"1.0.0","resolved":"file:../fe-remote/vendor/kit.tgz"}
	}}`), lock))
	require.Equal(t, []string{"../fe-remote"}, lock.externalLinks())
	require.Nil(t, (*nodePackageLock)(nil).externalLinks())
}

func copyTestdata(t *testing.T, from, to string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(from, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(to, relative), 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, relative), data, 0o644)
	}))
}

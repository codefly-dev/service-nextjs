package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
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

	runtime := initializedNativeRuntime(t, ctx, identity, environment)

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

// A `file:` link whose target the lock reaches by a `..` chain is installed in
// the same place, so a lock that spells the link `file:../../remote` is not
// silently skipped for the prefix alone.
func TestRuntimeInstallsALinkSpelledWithTheFilePrefix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	remote := filepath.Join(root, "mod", "remote")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	copyTestdata(t, filepath.Join("testdata", "linked-package", "remote"), remote)
	writeAppLock(t, source, "file:../../remote")
	// The application's own install is not what this pins: npm resolved it
	// already, which is the state the linked install has to be reached from.
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node_modules"), 0o755))

	runtime := initializedNativeRuntime(t, ctx, identity, environment)

	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	require.FileExists(t, filepath.Join(remote, "node_modules", "leaf", "package.json"),
		"a `file:`-prefixed link is installed where it lives")
}

// A lock link that climbs out of the workspace is refused by name: installing
// it would write node_modules into, and run the lifecycle scripts of, a tree
// the service does not own.
func TestRuntimeRefusesALinkedPackageOutsideTheWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	writeAppLock(t, source, "../../../../escaped")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node_modules"), 0o755))

	runtime := initializedNativeRuntime(t, ctx, identity, environment)

	err := runtime.ensureNodeDependencies(ctx)
	require.ErrorContains(t, err, "linked package ../../../../escaped is refused")
	require.ErrorContains(t, err, "outside")
	physical, evalErr := filepath.EvalSymlinks(root)
	require.NoError(t, evalErr)
	require.ErrorContains(t, err, physical)
	require.NoDirExists(t, filepath.Join(filepath.Dir(physical), "escaped"))
}

func TestLinkedPackageTargetStaysUnderTheWorkspace(t *testing.T) {
	root := filepath.FromSlash("/ws")
	source := filepath.FromSlash("/ws/mod/frontend/code")

	target, err := linkedPackageTarget(source, root, filepath.FromSlash("../../remote"))
	require.NoError(t, err)
	require.Equal(t, filepath.FromSlash("/ws/mod/remote"), target)

	_, err = linkedPackageTarget(source, root, filepath.FromSlash("/elsewhere/repo"))
	require.ErrorContains(t, err, "absolute")
	require.ErrorContains(t, err, "/elsewhere/repo")

	_, err = linkedPackageTarget(source, root, filepath.FromSlash("../../../../escaped"))
	require.ErrorContains(t, err, "../../../../escaped")
	require.ErrorContains(t, err, "outside "+root)
}

func TestExternalLinksNameOnlyPackagesOutsideTheSource(t *testing.T) {
	lock := &nodePackageLock{}
	require.NoError(t, json.Unmarshal([]byte(`{"lockfileVersion":3,"packages":{
		"": {"name":"app"},
		"node_modules/remote": {"resolved":"../fe-remote","link":true},
		"node_modules/again": {"resolved":"../fe-remote/","link":true},
		"node_modules/prefixed": {"resolved":"file:../fe-remote","link":true},
		"node_modules/other": {"resolved":"file:../fe-other","link":true},
		"node_modules/workspace": {"resolved":"packages/workspace","link":true},
		"node_modules/scoped": {"resolved":"file:packages/scoped","link":true},
		"node_modules/registry": {"version":"1.0.0","resolved":"https://registry.npmjs.org/registry/-/registry-1.0.0.tgz"},
		"node_modules/tarball": {"version":"1.0.0","resolved":"file:../fe-remote/vendor/kit.tgz"}
	}}`), lock))
	// `file:../fe-remote` is the same directory as `../fe-remote`, and
	// `file:packages/scoped` is as much inside the project as `packages/workspace`.
	require.Equal(t, []string{"../fe-other", "../fe-remote"}, lock.externalLinks())
	require.Nil(t, (*nodePackageLock)(nil).externalLinks())
}

// initializedNativeRuntime loads and initializes the runtime against the
// fixture workspace, the state ensureNodeDependencies is reached from.
func initializedNativeRuntime(t *testing.T, ctx context.Context, identity *basev0.ServiceIdentity, environment *resources.Environment) *Runtime {
	t.Helper()
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
	return runtime
}

// writeAppLock rewrites the fixture application's lockfile so its one link
// resolves to resolved, the way npm would have recorded that declaration.
func writeAppLock(t *testing.T, source, resolved string) {
	t.Helper()
	lock := fmt.Sprintf(`{
  "name": "linked-package-app",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {"name": "linked-package-app", "dependencies": {"remote": %q}},
    "node_modules/remote": {"resolved": %q, "link": true}
  }
}
`, "file:"+strings.TrimPrefix(resolved, "file:"), resolved)
	require.NoError(t, os.WriteFile(filepath.Join(source, "package-lock.json"), []byte(lock), 0o644))
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

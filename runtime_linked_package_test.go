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

// THE REGRESSION TEST. The fleet's playgrounds are a workspace nested inside
// a checkout, linking a sibling that lives ABOVE that workspace root:
// service-pdf's `playground` web links `../../../client/pdf-viewer`, and
// module-robin's links `../../../../../modules/robin/frontend`. v0.0.168
// refused both, because the fixture it was written against always put the
// workspace above the linked package. The lockfile is the service's own
// source: where it says its package lives is where the package lives.
func TestRuntimeInstallsALinkedPackageAboveTheWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testNestedWorkspaceIdentity(t, root)
	source := filepath.Join(root, "playground", "mod", "frontend", "code")
	remote := filepath.Join(root, "remote")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	copyTestdata(t, filepath.Join("testdata", "linked-package", "remote"), remote)
	// From the source: code -> frontend -> mod -> playground -> root.
	writeAppLock(t, source, "../../../../remote")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node_modules"), 0o755))

	runtime := initializedNativeRuntime(t, ctx, identity, environment)

	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	require.FileExists(t, filepath.Join(remote, "node_modules", "leaf", "package.json"),
		"a package the lock places above the workspace is installed where it lives")
	require.Len(t, runtime.linkWarnings, 1, "and where it landed is named, once")
}

// The two layouts above, as the paths they actually are, through the function
// that decides. Neither may be refused again.
func TestLinkedPackageTargetFollowsTheLockAboveTheWorkspace(t *testing.T) {
	for _, layout := range []struct{ name, source, root, link, want string }{
		{
			"service-pdf playground web",
			"/repo/playground/services/web", "/repo/playground",
			"../../../client/pdf-viewer", "/repo/client/pdf-viewer",
		},
		{
			"module-robin playground web",
			"/repo/playground/modules/robin-playground/services/web", "/repo/playground",
			"../../../../../modules/robin/frontend", "/repo/modules/robin/frontend",
		},
	} {
		t.Run(layout.name, func(t *testing.T) {
			target, err := linkedPackageTarget(filepath.FromSlash(layout.source),
				filepath.FromSlash(layout.root), filepath.FromSlash(layout.link), true)
			require.NoError(t, err, "the lock says where its package lives")
			require.Equal(t, filepath.FromSlash(layout.want), target)
		})
	}
}

// A directory inside the workspace may be a symlink out of it, and npm writes
// where the filesystem leads: containment is judged on what the target
// resolves to, not on the path the lock spells.
func TestRuntimeRefusesALinkedPackageSymlinkedOutOfTheWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node_modules"), 0o755))
	// `mod/remote` is inside the workspace by name only.
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "mod", "remote")))

	runtime := initializedNativeRuntime(t, ctx, identity, environment)

	err := runtime.ensureNodeDependencies(ctx)
	require.ErrorContains(t, err, "linked package ../../remote is refused")
	physical, evalErr := filepath.EvalSymlinks(outside)
	require.NoError(t, evalErr)
	require.ErrorContains(t, err, physical)
	require.NoDirExists(t, filepath.Join(outside, "node_modules"))
}

// With no workspace to judge against, the lock is still the service's own
// source: the package is installed where the lock says, and the line says
// nothing bounded it. Skipping here would silently reinstate the
// `Module not found` failure this install exists to fix.
func TestRuntimeInstallsAndNamesALinkedPackageWithNoWorkspaceToBoundIt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	remote := filepath.Join(root, "mod", "remote")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	copyTestdata(t, filepath.Join("testdata", "linked-package", "remote"), remote)
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node_modules"), 0o755))

	runtime := initializedNativeRuntime(t, ctx, identity, environment)
	runtime.Identity.WorkspacePath = ""

	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	require.FileExists(t, filepath.Join(remote, "node_modules", "leaf", "package.json"))
	require.Len(t, runtime.linkWarnings, 1, "named once")

	// Init, Build and Test each ensure dependencies: one line, not three.
	require.NoError(t, runtime.ensureNodeDependencies(ctx))
	require.Len(t, runtime.linkWarnings, 1)
}

// A container run installs nothing, but a lock this agent refuses is refused
// in every run mode: targets are resolved before the container check, so a
// bad lock is not loud natively and silent in a container.
func TestContainerRunStillRefusesALockItCannotHonour(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identity, environment := testIdentity(t, root)
	source := filepath.Join(root, "mod", "frontend", "code")
	copyTestdata(t, filepath.Join("testdata", "linked-package", "app"), source)
	writeAppLock(t, source, "/elsewhere/repo")

	runtime := initializedNativeRuntime(t, ctx, identity, environment)
	runtime.Runtime.RuntimeContext = resources.NewRuntimeContextContainer()
	require.True(t, runtime.Runtime.IsContainerRuntime())

	err := runtime.ensureLinkedNodeDependencies(ctx)
	require.ErrorContains(t, err, "linked package /elsewhere/repo is refused")
	require.ErrorContains(t, err, "absolute")
	require.Empty(t, runtime.linkWarnings, "a refused lock is refused, not warned about")
}

func TestLinkedPackageRootFallsBackToTheSourceWithNoWorkspace(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "mod", "frontend", "code")
	require.NoError(t, os.MkdirAll(source, 0o755))

	runtime := NewRuntime(NewService())
	runtime.setSourceLocation(source)

	runtime.Identity = &resources.ServiceIdentity{WorkspacePath: workspace}
	root, bounded := runtime.linkedPackageRoot()
	require.True(t, bounded)
	require.Equal(t, physicalPath(workspace), root)

	runtime.Identity = &resources.ServiceIdentity{}
	root, bounded = runtime.linkedPackageRoot()
	require.False(t, bounded)
	require.Equal(t, physicalPath(source), root)

	// A source tree attached from outside the workspace bounds nothing above
	// itself either.
	runtime.Identity = &resources.ServiceIdentity{WorkspacePath: t.TempDir()}
	root, bounded = runtime.linkedPackageRoot()
	require.False(t, bounded)
	require.Equal(t, physicalPath(source), root)
}

// An absolute target is not the lock saying where its package lives: it names
// a directory with no relation to the source, bound or unbound.
func TestLinkedPackageTargetRefusesAnAbsoluteTarget(t *testing.T) {
	source := filepath.FromSlash("/ws/mod/frontend/code")
	root := filepath.FromSlash("/ws")
	for _, bounded := range []bool{true, false} {
		_, err := linkedPackageTarget(source, root, filepath.FromSlash("/elsewhere/repo"), bounded)
		require.ErrorContains(t, err, "absolute")
		require.ErrorContains(t, err, "/elsewhere/repo")
	}

	target, err := linkedPackageTarget(source, root, filepath.FromSlash("../../remote"), true)
	require.NoError(t, err)
	require.Equal(t, filepath.FromSlash("/ws/mod/remote"), target)
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

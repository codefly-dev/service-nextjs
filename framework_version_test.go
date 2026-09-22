package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// frameworkPins are the application dependencies whose resolved version decides
// what the generated service actually runs. They are pinned exactly, so the
// lockfile can never resolve a different version than the manifest states.
var frameworkPins = []string{"next", "react", "react-dom"}

type applicationManifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Overrides       map[string]string `json:"overrides"`
}

func (m applicationManifest) declared(name string) string {
	if version := m.Dependencies[name]; version != "" {
		return version
	}
	return m.DevDependencies[name]
}

func readApplicationManifest(t *testing.T, data []byte) applicationManifest {
	t.Helper()
	var manifest applicationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	return manifest
}

func readApplicationLock(t *testing.T, data []byte) *nodePackageLock {
	t.Helper()
	var lock nodePackageLock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse package-lock.json: %v", err)
	}
	return &lock
}

// The scaffold must ship its own lockfile. Without one the first `npm install`
// resolves the dependency graph at whatever moment it happens to run — and the
// container build, which runs `npm ci`, has nothing to install from at all.
func TestFactoryShipsALockfileThatPinsTheDeclaredFrameworkVersions(t *testing.T) {
	t.Parallel()

	packageData, err := fs.ReadFile(factoryFS, "templates/factory/code/package.json")
	if err != nil {
		t.Fatalf("read factory package.json: %v", err)
	}
	lockData, err := fs.ReadFile(factoryFS, "templates/factory/code/package-lock.json")
	if err != nil {
		t.Fatalf("factory scaffold ships no lockfile: %v", err)
	}

	manifest := readApplicationManifest(t, packageData)
	lock := readApplicationLock(t, lockData)
	if lock.LockfileVersion < 3 {
		t.Fatalf("lockfileVersion = %d, want at least 3", lock.LockfileVersion)
	}

	for _, name := range frameworkPins {
		declared := manifest.declared(name)
		if declared == "" {
			t.Fatalf("factory declares no %s", name)
		}
		if strings.ContainsAny(declared, "^~<>*|x ") {
			t.Fatalf("%s is declared as the range %q; the framework pin must be exact", name, declared)
		}
		if resolved := lock.resolvedVersion(name); resolved != declared {
			t.Fatalf("%s: manifest declares %q but the lockfile resolves %q", name, declared, resolved)
		}
	}

	// The Next.js ESLint rules ship with the framework, so a lint config left on
	// an older release reports against a Next.js the application no longer runs.
	if got, want := manifest.declared("eslint-config-next"), manifest.declared("next"); got != want {
		t.Fatalf("eslint-config-next = %q, want the Next.js version %q", got, want)
	}
}

// base/code and templates/factory/code are two copies of one application, and
// nothing else compares them. Their framework versions must not diverge, or the
// substrate Dependabot upgrades stops describing what the next scaffold ships.
func TestReferenceApplicationAndFactoryPinTheSameFrameworkVersions(t *testing.T) {
	t.Parallel()

	factoryData, err := fs.ReadFile(factoryFS, "templates/factory/code/package.json")
	if err != nil {
		t.Fatalf("read factory package.json: %v", err)
	}
	baseData, err := os.ReadFile(filepath.Join("base", "code", "package.json"))
	if err != nil {
		t.Fatalf("read reference package.json: %v", err)
	}
	baseLockData, err := os.ReadFile(filepath.Join("base", "code", "package-lock.json"))
	if err != nil {
		t.Fatalf("read reference package-lock.json: %v", err)
	}

	factory := readApplicationManifest(t, factoryData)
	base := readApplicationManifest(t, baseData)
	baseLock := readApplicationLock(t, baseLockData)

	for _, name := range append(frameworkPins, "eslint-config-next") {
		if factory.declared(name) != base.declared(name) {
			t.Fatalf("%s: factory declares %q, reference application declares %q",
				name, factory.declared(name), base.declared(name))
		}
		if declared := base.declared(name); baseLock.resolvedVersion(name) != declared {
			t.Fatalf("%s: reference lockfile resolves %q, want %q",
				name, baseLock.resolvedVersion(name), declared)
		}
	}

	// The overrides exist to hold the framework's own transitive dependencies at
	// a patched version; a copy that drifts ships the vulnerability the other
	// copy pinned away.
	for name, version := range factory.Overrides {
		if base.Overrides[name] != version {
			t.Fatalf("override %s: factory pins %q, reference application pins %q",
				name, version, base.Overrides[name])
		}
	}
	if len(base.Overrides) != len(factory.Overrides) {
		t.Fatalf("reference overrides = %v, factory overrides = %v", base.Overrides, factory.Overrides)
	}
}

// The served README records the pairing this substrate was validated against.
// Prose rots silently, so every version it names must be one the scaffold
// actually pins.
func TestServedReadmeRecordsOnlyVersionsTheScaffoldPins(t *testing.T) {
	t.Parallel()

	packageData, err := fs.ReadFile(factoryFS, "templates/factory/code/package.json")
	if err != nil {
		t.Fatalf("read factory package.json: %v", err)
	}
	manifest := readApplicationManifest(t, packageData)
	pinned := map[string]bool{}
	for _, name := range frameworkPins {
		pinned[manifest.declared(name)] = true
	}

	readme, err := fs.ReadFile(readmeFS, "templates/agent/README.md.tmpl")
	if err != nil {
		t.Fatalf("read served README: %v", err)
	}
	named := regexp.MustCompile(`\d+\.\d+\.\d+`).FindAllString(string(readme), -1)
	if len(named) == 0 {
		t.Fatal("served README records no framework version")
	}
	for _, version := range named {
		if !pinned[version] {
			t.Fatalf("served README names %s, which the scaffold does not pin (pinned: %v)", version, pinned)
		}
	}
	for _, name := range []string{"next", "react"} {
		if !strings.Contains(string(readme), manifest.declared(name)) {
			t.Fatalf("served README does not record the %s version %s", name, manifest.declared(name))
		}
	}
}

func TestFrameworkCommandReportsTheLockResolvedVersion(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A caret range that resolves to a different patch is exactly the case the
	// command exists for: the declared constraint does not name what runs.
	write("package.json", `{"dependencies":{"next":"^16.3.0","react":"19.2.8"}}`)

	service := NewService()
	service.sourceLocation = dir
	runtime := NewRuntime(service)

	unpinned, err := runtime.cmdFramework(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"next: declared ^16.3.0, resolved (not in lockfile)",
		"react: declared 19.2.8, resolved (not in lockfile)",
		"npm install",
	} {
		if !strings.Contains(unpinned, required) {
			t.Fatalf("unpinned report missing %q:\n%s", required, unpinned)
		}
	}

	write("package-lock.json", `{"lockfileVersion":3,"packages":{
		"":{"name":"code"},
		"node_modules/next":{"version":"16.3.6"},
		"node_modules/react":{"version":"19.2.8"}
	}}`)
	pinned, err := runtime.cmdFramework(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"next: declared ^16.3.0, resolved 16.3.6",
		"react: declared 19.2.8, resolved 19.2.8",
		"npm ci",
	} {
		if !strings.Contains(pinned, required) {
			t.Fatalf("pinned report missing %q:\n%s", required, pinned)
		}
	}
}

func TestReadNodePackageLockDistinguishesAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()
	lock, err := readNodePackageLock(dir)
	if err != nil || lock != nil {
		t.Fatalf("absent lockfile = (%v, %v), want (nil, nil)", lock, err)
	}
	if got := lock.resolvedVersion("next"); got != "" {
		t.Fatalf("resolved version without a lockfile = %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodePackageLock(dir); err == nil {
		t.Fatal("an unreadable lockfile must not be reported as an unpinned project")
	}
}

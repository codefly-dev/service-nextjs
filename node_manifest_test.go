package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodePackageManifestKeepsGenericTypeScriptOutOfNextJSProfile(t *testing.T) {
	dir := t.TempDir()
	content := []byte(`{
  "scripts": {"test": "vitest run", "typecheck": "tsc --noEmit"},
  "devDependencies": {"typescript": "5.9.3", "vitest": "3.2.7"}
}`)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := readNodePackageManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.projectKind(); got != nodeProjectGeneric {
		t.Fatalf("project kind = %q, want %q", got, nodeProjectGeneric)
	}
	if got := manifest.testRunner("test"); got != nodeTestVitest {
		t.Fatalf("test runner = %q, want %q", got, nodeTestVitest)
	}
	scripts := manifest.validationScripts()
	if len(scripts) != 1 || scripts[0] != "typecheck" {
		t.Fatalf("validation scripts = %v, want only typecheck", scripts)
	}
}

func TestNodePackageManifestRecognizesNextJSAndJestProjects(t *testing.T) {
	next := &nodePackageManifest{Dependencies: map[string]string{"next": "16.2.12"}}
	if got := next.projectKind(); got != nodeProjectNextJS {
		t.Fatalf("Next.js project kind = %q, want %q", got, nodeProjectNextJS)
	}

	jest := &nodePackageManifest{
		Scripts:         map[string]string{"test": "jest"},
		DevDependencies: map[string]string{"jest": "30.2.0"},
	}
	if got := jest.testRunner("test"); got != nodeTestJest {
		t.Fatalf("Jest runner = %q, want %q", got, nodeTestJest)
	}

	composite := &nodePackageManifest{Scripts: map[string]string{"test": "npm run test:unit && npm run test:contract"}}
	if got := composite.testRunner("test"); got != nodeTestGeneric {
		t.Fatalf("composite runner = %q, want %q", got, nodeTestGeneric)
	}
}

func TestNodePackageManifestRecognizesDeclaredPlaywrightRuntime(t *testing.T) {
	tests := []struct {
		name     string
		manifest *nodePackageManifest
		want     bool
	}{
		{
			name:     "test dependency",
			manifest: &nodePackageManifest{DevDependencies: map[string]string{"@playwright/test": "1.54.1"}},
			want:     true,
		},
		{
			name:     "package script",
			manifest: &nodePackageManifest{Scripts: map[string]string{"e2e": "bunx playwright test"}},
			want:     true,
		},
		{
			name:     "unrelated browser package",
			manifest: &nodePackageManifest{DevDependencies: map[string]string{"vitest": "3.2.7"}},
			want:     false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.manifest.usesPlaywright(); got != test.want {
				t.Fatalf("usesPlaywright = %v, want %v", got, test.want)
			}
		})
	}
}

package main_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/runners/testmatrix"
)

// TestNextjsLifecycle_Matrix validates that node is reachable across
// native, nix, and docker. The assertion is intentionally minimal: we
// just check `node --version` emits a v-prefixed version. Deeper
// lifecycle tests (npm install, next dev boot, port bind) require a
// real Next.js source tree — those belong in integration tests that
// materialize a fixture workspace, not this mode-parity smoke test.
func TestNextjsLifecycle_Matrix(t *testing.T) {
	dir, err := os.MkdirTemp("", "nextjs-matrix-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	// Provision the nix devShell (nodejs) into the work dir so the nix backend
	// has a flake to materialize — mirrors what the runtime does via
	// ensureNixFlake. Without it the nix subtest has no flake and fails.
	for _, f := range []string{"flake.nix", "flake.lock"} {
		data, rerr := os.ReadFile(filepath.Join("nix", f))
		if rerr != nil {
			t.Fatalf("read nix/%s: %v", f, rerr)
		}
		if werr := os.WriteFile(filepath.Join(dir, f), data, 0o644); werr != nil {
			t.Fatalf("write %s: %v", f, werr)
		}
	}

	img := &resources.DockerImage{Name: "node", Tag: "22-alpine"}

	testmatrix.ForEachEnvironment(t, dir,
		func(t *testing.T, env runners.RunnerEnvironment) {
			proc, err := env.NewProcess("node", "--version")
			if err != nil {
				t.Fatalf("NewProcess: %v", err)
			}
			var buf bytes.Buffer
			proc.WithOutput(&buf)
			if err := proc.Run(context.Background()); err != nil {
				t.Fatalf("node --version failed: %v", err)
			}
			out := strings.TrimSpace(buf.String())
			if !strings.HasPrefix(out, "v") {
				t.Fatalf("expected v-prefixed version, got %q", out)
			}
		},
		testmatrix.WithDockerImage(img),
	)
}

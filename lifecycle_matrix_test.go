package main_test

import (
	"bytes"
	"context"
	"os"
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

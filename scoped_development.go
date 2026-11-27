package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/codefly-dev/core/resources"
)

//go:embed templates/runtime/development-adapter.cjs
var developmentAdapter []byte

// Native and Nix use the application's real cwd. A Next deployment adapter
// applies the scope after Next resolves the project's own development config.
func (s *Runtime) developmentOutputEnvironment(delivered []*resources.EnvironmentVariable) ([]*resources.EnvironmentVariable, error) {
	if s.projectKind != nodeProjectNextJS || s.executionProfile != NextExecutionDevelopment || s.Runtime.IsContainerRuntime() {
		return nil, nil
	}
	fingerprint := sha256.Sum256(developmentAdapter)
	adapter := filepath.Join(resources.CodeflyHomeDir(), "agents", "nextjs-adapters", fmt.Sprintf("%x.cjs", fingerprint))
	// Keep the inode stable when another running server watches its config.
	if installed, err := os.ReadFile(adapter); err != nil || !bytes.Equal(installed, developmentAdapter) {
		if err := writeGeneratedFile(adapter, developmentAdapter); err != nil {
			return nil, fmt.Errorf("materialize Next.js development adapter: %w", err)
		}
	}
	scope := sha256.Sum256([]byte(s.UniqueWithWorkspace()))
	prior := os.Getenv("NEXT_ADAPTER_PATH")
	for _, variable := range delivered {
		if variable != nil && variable.Key == "NEXT_ADAPTER_PATH" {
			prior = variable.ValueAsString()
		}
	}
	return []*resources.EnvironmentVariable{
		resources.Env("NEXT_ADAPTER_PATH", adapter),
		resources.Env("CODEFLY_NEXT_ADAPTER_PATH", adapter),
		resources.Env("CODEFLY_NEXT_DELEGATE_ADAPTER", prior),
		resources.Env("CODEFLY_NEXT_OUTPUT_SCOPE", fmt.Sprintf("%x", scope[:16])),
	}, nil
}

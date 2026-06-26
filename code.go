package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corecode "github.com/codefly-dev/core/code"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
)

// ARCHITECTURE: Code implements the codefly Code gRPC service for NextJS/Node.
// It embeds DefaultCodeServer from core, which provides:
//   - File operations: ReadFile, WriteFile, CreateFile, DeleteFile, MoveFile, ListFiles, Search
//   - Git operations: GitLog, GitDiff, GitShow, GitBlame
//   - ShellExec: bounded process execution
//
// Node-specific overrides:
//   - get_project_info: reads package.json for module/version/dependencies
type Code struct {
	*corecode.DefaultCodeServer
	*Service
	initialized bool
}

func NewCode(svc *Service) *Code {
	c := &Code{
		Service:           svc,
		DefaultCodeServer: corecode.NewDefaultCodeServer("."),
	}
	return c
}

func (c *Code) sourceDir() string {
	if c.sourceLocation != "" {
		return c.sourceLocation
	}
	if wd := os.Getenv("CODEFLY_AGENT_WORKDIR"); wd != "" {
		return wd
	}
	return c.Location
}

func (c *Code) InitServer() {
	c.DefaultCodeServer = corecode.NewDefaultCodeServer(c.sourceDir(), nil)
	c.registerOverrides()
	c.initialized = true
}

func (c *Code) ensureInit() {
	if !c.initialized {
		c.InitServer()
	}
}

func (c *Code) registerOverrides() {
	c.Override("get_project_info", c.handleGetProjectInfo)
}

// Standalone gRPC RPCs.

func (c *Code) GetProjectInfo(ctx context.Context, req *codev0.GetProjectInfoRequest) (*codev0.GetProjectInfoResponse, error) {
	c.ensureInit()
	resp, err := c.handleGetProjectInfo(ctx, nil)
	if err != nil {
		return nil, err
	}
	return resp.GetGetProjectInfo(), nil
}

func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.ensureInit()
	return c.DefaultCodeServer.Execute(ctx, req)
}

// ── Handlers ────────────────────────────────────────────

func (c *Code) handleGetProjectInfo(_ context.Context, _ *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	srcDir := c.sourceDir()
	resp := &codev0.GetProjectInfoResponse{Language: "typescript"}

	// Read package.json for module name and version.
	data, err := os.ReadFile(filepath.Join(srcDir, "package.json"))
	if err == nil {
		resp.Module, resp.LanguageVersion = parsePackageJSON(string(data))
	}

	// File hashes for change detection.
	resp.FileHashes = computeTSFileHashes(srcDir)

	return &codev0.CodeResponse{Result: &codev0.CodeResponse_GetProjectInfo{
		GetProjectInfo: resp,
	}}, nil
}

// ── package.json parsing ────────────────────────────────

func parsePackageJSON(content string) (name, version string) {
	// Simple extraction without a JSON dependency.
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"name"`) {
			name = extractJSONStringValue(line)
		}
		if strings.HasPrefix(line, `"version"`) {
			version = extractJSONStringValue(line)
		}
	}
	return
}

func extractJSONStringValue(line string) string {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	v := strings.TrimSpace(parts[1])
	v = strings.Trim(v, `",`)
	return v
}

func computeTSFileHashes(srcDir string) map[string]string {
	hashes := make(map[string]string)
	filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		ext := filepath.Ext(name)
		if ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx" && name != "package.json" {
			return nil
		}
		// Skip node_modules etc.
		if strings.Contains(path, "node_modules") || strings.Contains(path, ".next") {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		data, err := os.ReadFile(path)
		if err == nil {
			h := sha256.Sum256(data)
			hashes[rel] = fmt.Sprintf("%x", h)
		}
		return nil
	})
	return hashes
}

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corecode "github.com/codefly-dev/core/code"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
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
	c.DefaultCodeServer = corecode.NewDefaultCodeServer(c.sourceDir())
	c.registerOverrides()
	c.initialized = true
}

func (c *Code) ensureInit() {
	if !c.initialized {
		c.InitServer()
	}
}

func (c *Code) registerOverrides() {
	c.SetSourceFixer(c.fixTypeScript)
	c.Override("get_project_info", c.handleGetProjectInfo)
}

func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.ensureInit()
	return c.DefaultCodeServer.Execute(ctx, req)
}

type nodePackageManifest struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type eslintJSONResult struct {
	Output   *string `json:"output"`
	Messages []struct {
		RuleID   *string `json:"ruleId"`
		Severity int     `json:"severity"`
		Message  string  `json:"message"`
		Line     int     `json:"line"`
		Column   int     `json:"column"`
	} `json:"messages"`
}

// fixTypeScript selects only project-local tools. Biome is preferred because
// one stdin invocation performs formatting, safe lint fixes, and import
// organization. Existing ESLint/Prettier projects use their configured tools
// without downloading executables during an agent edit.
func (c *Code) fixTypeScript(ctx context.Context, input corecode.FixInput) (corecode.FixResult, error) {
	manifest, err := c.readNodePackageManifest()
	if err != nil {
		return corecode.FixResult{}, err
	}
	env := c.runnerEnvironment(ctx)

	if manifest.hasDependency("@biomejs/biome") {
		args := []string{"exec", "--offline", "--", "biome", "check", "--stdin-file-path", input.Path, "--write"}
		if input.Mode == basev0.FixMode_FIX_MODE_AGGRESSIVE {
			args = append(args, "--unsafe")
		}
		fixed, diagnostics, runErr := runners.RunInput(ctx, env, c.sourceDir(), input.Content, "npm", args...)
		if runErr != nil && len(fixed) == 0 {
			return corecode.FixResult{}, fmt.Errorf("biome check: %w: %s", runErr, strings.TrimSpace(string(diagnostics)))
		}
		if err := rejectEmptySourceFix("biome", input.Content, fixed); err != nil {
			return corecode.FixResult{}, err
		}
		return corecode.FixResult{
			Content: fixed,
			Actions: []string{"biome check --write"},
			Output:  strings.TrimSpace(string(diagnostics)),
		}, nil
	}

	fixed := input.Content
	var actions []string
	var output []string
	if manifest.hasDependency("eslint") || manifest.Scripts["lint"] != "" {
		args := []string{"exec", "--offline", "--", "eslint", "--fix-dry-run", "--stdin", "--stdin-filename", input.Path, "--format", "json"}
		jsonOutput, diagnostics, runErr := runners.RunInput(ctx, env, c.sourceDir(), fixed, "npm", args...)
		parsed, parseErr := parseESLintFix(jsonOutput, fixed)
		if parseErr != nil {
			return corecode.FixResult{}, fmt.Errorf("eslint --fix-dry-run: %w (run error: %v): %s", parseErr, runErr, strings.TrimSpace(string(diagnostics)))
		}
		fixed = parsed.Content
		actions = append(actions, "eslint --fix-dry-run")
		output = append(output, parsed.Diagnostics...)
		if text := strings.TrimSpace(string(diagnostics)); text != "" {
			output = append(output, text)
		}
	}
	if manifest.hasDependency("prettier") {
		formatted, diagnostics, runErr := runners.RunInput(ctx, env, c.sourceDir(), fixed, "npm", "exec", "--offline", "--", "prettier", "--stdin-filepath", input.Path)
		if runErr != nil {
			return corecode.FixResult{}, fmt.Errorf("prettier: %w: %s", runErr, strings.TrimSpace(string(diagnostics)))
		}
		if err := rejectEmptySourceFix("prettier", fixed, formatted); err != nil {
			return corecode.FixResult{}, err
		}
		fixed = formatted
		actions = append(actions, "prettier")
		if text := strings.TrimSpace(string(diagnostics)); text != "" {
			output = append(output, text)
		}
	}
	if len(actions) == 0 {
		return corecode.FixResult{}, fmt.Errorf("no project-local Biome, ESLint, or Prettier fixer is configured")
	}
	return corecode.FixResult{Content: fixed, Actions: actions, Output: strings.Join(output, "\n")}, nil
}

// A formatter/linter is never allowed to turn a substantive source file into
// an empty file. This invariant is deliberately independent of process exit
// status: a broken stdin/stdout integration can exit successfully with no
// output, which must fail closed instead of being reported as a valid fix.
func rejectEmptySourceFix(tool string, original, fixed []byte) error {
	if len(bytes.TrimSpace(original)) > 0 && len(bytes.TrimSpace(fixed)) == 0 {
		return fmt.Errorf("%s returned empty output for a non-empty source file; refusing destructive fix", tool)
	}
	return nil
}

func (c *Code) readNodePackageManifest() (*nodePackageManifest, error) {
	data, err := os.ReadFile(filepath.Join(c.sourceDir(), "package.json"))
	if err != nil {
		return nil, fmt.Errorf("read package.json: %w", err)
	}
	var manifest nodePackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	return &manifest, nil
}

func (m *nodePackageManifest) hasDependency(name string) bool {
	return m.Dependencies[name] != "" || m.DevDependencies[name] != ""
}

type eslintFix struct {
	Content     []byte
	Diagnostics []string
}

func parseESLintFix(data, original []byte) (eslintFix, error) {
	var results []eslintJSONResult
	if err := json.Unmarshal(data, &results); err != nil {
		return eslintFix{}, err
	}
	if len(results) != 1 {
		return eslintFix{}, fmt.Errorf("expected one ESLint result, got %d", len(results))
	}
	result := eslintFix{Content: original}
	if results[0].Output != nil {
		result.Content = []byte(*results[0].Output)
	}
	if err := rejectEmptySourceFix("eslint", original, result.Content); err != nil {
		return eslintFix{}, err
	}
	for _, message := range results[0].Messages {
		rule := "eslint"
		if message.RuleID != nil && *message.RuleID != "" {
			rule = *message.RuleID
		}
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s:%d:%d: %s: %s", rule, message.Line, message.Column, eslintSeverity(message.Severity), message.Message))
	}
	return result, nil
}

func eslintSeverity(value int) string {
	if value >= 2 {
		return "error"
	}
	return "warning"
}

func (c *Code) runnerEnvironment(ctx context.Context) runners.RunnerEnvironment {
	var runtimeContext *basev0.RuntimeContext
	if c.Service.Base != nil && c.Service.Base.Runtime != nil {
		runtimeContext = c.Service.Base.Runtime.RuntimeContext
	}
	return runners.ResolveStandaloneEnvironment(ctx, c.sourceDir(), runtimeContext)
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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/codefly-dev/core/agents"
	corecode "github.com/codefly-dev/core/code"
	"github.com/codefly-dev/core/code/semantic"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
)

// ARCHITECTURE: Code implements the codefly Code gRPC service for NextJS/Node.
// It embeds TypeScriptCodeServer from core, which provides:
//   - File operations: ReadFile, WriteFile, CreateFile, DeleteFile, MoveFile, ListFiles, Search
//   - Git operations: GitLog, GitDiff, GitShow, GitBlame
//   - ShellExec: bounded process execution
//
// Node-specific behavior is limited to project-local source fixing. Typed
// project inspection stays in Core so every TypeScript agent exposes the same
// dependency, package, import, and failure contract.
type Code struct {
	*corecode.TypeScriptCodeServer
	*Service
	serverMu          sync.Mutex
	initializedSource string
}

func NewCode(svc *Service) *Code {
	c := &Code{
		Service:              svc,
		TypeScriptCodeServer: corecode.NewTypeScriptCodeServer(".", semanticOpts()),
	}
	return c
}

func (c *Code) sourceDir() string {
	if source := c.currentSourceLocation(); source != "" {
		return source
	}
	if wd := os.Getenv(agents.WorkDirEnvironment); wd != "" {
		return filepath.Join(wd, c.Settings.NodeSourceDir())
	}
	return filepath.Join(c.Location, c.Settings.NodeSourceDir())
}

func (c *Code) InitServer() {
	c.serverMu.Lock()
	defer c.serverMu.Unlock()
	c.initServer()
}

func (c *Code) initServer() {
	source := c.sourceDir()
	if c.TypeScriptCodeServer != nil && c.initializedSource == source {
		return
	}
	previous := c.TypeScriptCodeServer
	c.TypeScriptCodeServer = corecode.NewTypeScriptCodeServer(source, semanticOpts())
	c.SetSourceFixer(c.fixTypeScript)
	c.initializedSource = source
	if previous != nil {
		_ = previous.Close()
	}
}

func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.serverMu.Lock()
	defer c.serverMu.Unlock()
	if _, err := c.resolveSourceLocation(ctx); err != nil {
		return nil, fmt.Errorf("resolve Node source: %w", err)
	}
	c.initServer()
	response, err := c.TypeScriptCodeServer.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	if project := response.GetGetProjectInfo(); project != nil {
		project.Language = nodeProjectLanguage(project.GetSourceFiles())
	}
	return response, nil
}

// nodeProjectLanguage preserves TypeScript for mixed or TypeScript projects
// and reports JavaScript when the project contains only JavaScript sources.
// ARCHITECTURE: Core owns source discovery; this agent classifies the returned
// typed inventory instead of performing a second filesystem walk.
func nodeProjectLanguage(sourceFiles []*codev0.SourceFileInfo) string {
	hasJavaScript := false
	for _, source := range sourceFiles {
		switch strings.ToLower(filepath.Ext(source.GetPath())) {
		case ".ts", ".tsx", ".mts", ".cts":
			return "typescript"
		case ".js", ".jsx", ".mjs", ".cjs":
			hasJavaScript = true
		}
	}
	if hasJavaScript {
		return "javascript"
	}
	return "typescript"
}

type nodePackageManifest struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type nodeProjectKind string
type nodeTestRunner string

const (
	nodeProjectGeneric nodeProjectKind = "node"
	nodeProjectNextJS  nodeProjectKind = "nextjs"
	nodeTestGeneric    nodeTestRunner  = "npm"
	nodeTestJest       nodeTestRunner  = "jest"
	nodeTestPlaywright nodeTestRunner  = "playwright"
	nodeTestVitest     nodeTestRunner  = "vitest"
)

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
	return readNodePackageManifest(c.sourceDir())
}

func readNodePackageManifest(sourceDir string) (*nodePackageManifest, error) {
	data, err := os.ReadFile(filepath.Join(sourceDir, "package.json"))
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

func (m *nodePackageManifest) projectKind() nodeProjectKind {
	if m != nil && m.hasDependency("next") {
		return nodeProjectNextJS
	}
	return nodeProjectGeneric
}

func (m *nodePackageManifest) hasScript(name string) bool {
	return m != nil && strings.TrimSpace(m.Scripts[name]) != ""
}

func (m *nodePackageManifest) testRunner(scriptName string) nodeTestRunner {
	if m == nil {
		return nodeTestGeneric
	}
	// The script command is authoritative. A package that installs @playwright/test
	// for a separate e2e script still runs its unit `test` script through Vitest, so
	// an installed dependency must never override the runner the command itself names.
	command := strings.ToLower(m.Scripts[scriptName])
	switch {
	case strings.Contains(command, "playwright"):
		return nodeTestPlaywright
	case strings.Contains(command, "vitest"):
		return nodeTestVitest
	case strings.Contains(command, "jest"):
		return nodeTestJest
	}
	// The command names no runner (e.g. a bare wrapper script): fall back to the
	// installed test dependency.
	switch {
	case m.hasDependency("@playwright/test"):
		return nodeTestPlaywright
	case m.hasDependency("vitest"):
		return nodeTestVitest
	case m.hasDependency("jest"):
		return nodeTestJest
	default:
		return nodeTestGeneric
	}
}

func (m *nodePackageManifest) validationScripts() []string {
	var scripts []string
	for _, name := range []string{"typecheck", "build"} {
		if m.hasScript(name) {
			scripts = append(scripts, name)
		}
	}
	return scripts
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

// semanticOpts installs the tree-sitter source analyzer that core omits by
// default (so Go agents can build CGO-free). This agent forwards a semantic
// index through its Tooling and already releases with CGO_ENABLED=1, so it
// opts back in explicitly. See core/code.WithSemanticAnalyzer.
func semanticOpts() []corecode.ServerOption {
	return []corecode.ServerOption{corecode.WithSemanticAnalyzer(semantic.New())}
}

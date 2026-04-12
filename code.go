package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//   - list_symbols: extracts TS/JS symbols using a simple AST parser
//     (future: use typescript-language-server via LSP)
//   - get_project_info: reads package.json for module/version/dependencies
type Code struct {
	*corecode.DefaultCodeServer
	*Service
	initialized bool
}

func NewCode(svc *Service) *Code {
	c := &Code{
		Service:            svc,
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
	c.Override("list_symbols", c.handleListSymbols)
	c.Override("get_project_info", c.handleGetProjectInfo)
}

// Standalone gRPC RPCs.

func (c *Code) ListSymbols(ctx context.Context, req *codev0.ListSymbolsRequest) (*codev0.ListSymbolsResponse, error) {
	c.ensureInit()
	resp, err := c.handleListSymbols(ctx, &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ListSymbols{ListSymbols: req},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetListSymbols(), nil
}

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

func (c *Code) handleListSymbols(_ context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetListSymbols()
	file := ""
	if r != nil {
		file = r.File
	}

	srcDir := c.sourceDir()
	symbols, err := extractTSSymbols(srcDir, file)
	if err != nil {
		return &codev0.CodeResponse{Result: &codev0.CodeResponse_ListSymbols{ListSymbols: &codev0.ListSymbolsResponse{
			Status: &codev0.ListSymbolsStatus{State: codev0.ListSymbolsStatus_ERROR, Message: err.Error()},
		}}}, nil
	}

	return &codev0.CodeResponse{Result: &codev0.CodeResponse_ListSymbols{ListSymbols: &codev0.ListSymbolsResponse{
		Status:  &codev0.ListSymbolsStatus{State: codev0.ListSymbolsStatus_SUCCESS},
		Symbols: symbols,
	}}}, nil
}

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

// ── TS/JS Symbol Extraction ─────────────────────────────
//
// Simple regex-based extraction. Finds:
//   - export function Name(...)
//   - export const Name = ...
//   - export class Name
//   - export interface Name
//   - export type Name
//
// Future: replace with typescript-language-server (LSP) for full accuracy.

func extractTSSymbols(srcDir, file string) ([]*codev0.Symbol, error) {
	var files []string
	if file != "" {
		files = []string{filepath.Join(srcDir, file)}
	} else {
		// Walk all .ts/.tsx/.js/.jsx files.
		filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				name := info.Name()
				if name == "node_modules" || name == ".next" || name == ".git" || name == "dist" || name == "build" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" {
				files = append(files, path)
			}
			return nil
		})
	}

	var allSymbols []*codev0.Symbol
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(data)
		lines := strings.Split(content, "\n")
		rel, _ := filepath.Rel(srcDir, f)

		syms := parseTSFile(rel, lines)

		// Enrich with hashes.
		for _, sym := range syms {
			enrichTSSymbolHashes(sym, lines)
		}

		allSymbols = append(allSymbols, syms...)
	}
	return allSymbols, nil
}

func parseTSFile(relPath string, lines []string) []*codev0.Symbol {
	var symbols []*codev0.Symbol
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip comments and empty lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// export function Name(...) or function Name(...)
		if idx := strings.Index(trimmed, "function "); idx >= 0 {
			name := extractIdentifier(trimmed[idx+len("function "):])
			if name != "" && name != "(" {
				sym := &codev0.Symbol{
					Name:      name,
					Kind:      codev0.SymbolKind_SYMBOL_KIND_FUNCTION,
					Signature: extractSignature(trimmed),
					Location: &codev0.Location{
						File: relPath, Line: int32(i + 1), EndLine: findBlockEnd(lines, i),
					},
				}
				symbols = append(symbols, sym)
			}
		}

		// export class Name or class Name
		if idx := strings.Index(trimmed, "class "); idx >= 0 {
			name := extractIdentifier(trimmed[idx+len("class "):])
			if name != "" {
				sym := &codev0.Symbol{
					Name:      name,
					Kind:      codev0.SymbolKind_SYMBOL_KIND_CLASS,
					Signature: trimmed,
					Location: &codev0.Location{
						File: relPath, Line: int32(i + 1), EndLine: findBlockEnd(lines, i),
					},
				}
				symbols = append(symbols, sym)
			}
		}

		// export interface Name or interface Name
		if idx := strings.Index(trimmed, "interface "); idx >= 0 {
			name := extractIdentifier(trimmed[idx+len("interface "):])
			if name != "" {
				sym := &codev0.Symbol{
					Name:      name,
					Kind:      codev0.SymbolKind_SYMBOL_KIND_INTERFACE,
					Signature: trimmed,
					Location: &codev0.Location{
						File: relPath, Line: int32(i + 1), EndLine: findBlockEnd(lines, i),
					},
				}
				symbols = append(symbols, sym)
			}
		}

		// export type Name or type Name =
		if idx := strings.Index(trimmed, "type "); idx >= 0 && strings.Contains(trimmed, "=") {
			name := extractIdentifier(trimmed[idx+len("type "):])
			if name != "" {
				sym := &codev0.Symbol{
					Name: name,
					Kind: codev0.SymbolKind_SYMBOL_KIND_STRUCT, // closest to "type alias"
					Location: &codev0.Location{
						File: relPath, Line: int32(i + 1), EndLine: int32(i + 1),
					},
				}
				symbols = append(symbols, sym)
			}
		}

		// export const Name = ... (arrow functions, components)
		if strings.Contains(trimmed, "const ") && strings.Contains(trimmed, "=") {
			idx := strings.Index(trimmed, "const ")
			name := extractIdentifier(trimmed[idx+len("const "):])
			if name != "" && isExported(trimmed) {
				kind := codev0.SymbolKind_SYMBOL_KIND_VARIABLE
				// Arrow function: const Name = (...) =>
				if strings.Contains(trimmed, "=>") || strings.Contains(trimmed, "function") {
					kind = codev0.SymbolKind_SYMBOL_KIND_FUNCTION
				}
				sym := &codev0.Symbol{
					Name: name,
					Kind: kind,
					Location: &codev0.Location{
						File: relPath, Line: int32(i + 1), EndLine: findBlockEnd(lines, i),
					},
				}
				symbols = append(symbols, sym)
			}
		}
	}
	return symbols
}

func extractIdentifier(s string) string {
	s = strings.TrimSpace(s)
	var name strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' {
			name.WriteRune(c)
		} else {
			break
		}
	}
	return name.String()
}

func extractSignature(line string) string {
	// For function declarations, take up to the opening brace.
	if idx := strings.Index(line, "{"); idx > 0 {
		return strings.TrimSpace(line[:idx])
	}
	return strings.TrimSpace(line)
}

func isExported(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "export ")
}

func findBlockEnd(lines []string, start int) int32 {
	depth := 0
	for i := start; i < len(lines); i++ {
		for _, c := range lines[i] {
			if c == '{' {
				depth++
			}
			if c == '}' {
				depth--
				if depth == 0 {
					return int32(i + 1)
				}
			}
		}
	}
	return int32(start + 1) // fallback: single line
}

func enrichTSSymbolHashes(sym *codev0.Symbol, lines []string) {
	if sym.Location == nil {
		return
	}
	sym.QualifiedName = sym.Name // TS doesn't have packages like Go

	if sym.Signature != "" {
		h := sha256.Sum256([]byte(sym.Signature))
		sym.SignatureHash = hex.EncodeToString(h[:8])
	}

	start := int(sym.Location.Line)
	end := int(sym.Location.EndLine)
	if start > 0 && end > 0 && end <= len(lines) {
		body := strings.Join(lines[start-1:end], "\n")
		normalized := normalizeBody(body)
		if normalized != "" {
			h := sha256.Sum256([]byte(normalized))
			sym.BodyHash = hex.EncodeToString(h[:8])
		}
	}
}

func normalizeBody(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
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

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/agents"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
	"github.com/codefly-dev/core/resources"
)

func TestProjectInfoCarriesDeclaredDependenciesAndSourceImportsAcrossCodeAndTooling(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
  "name": "checkout-ui",
  "dependencies": {"express": "^5.1.0"}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "src", "server.ts"),
		[]byte("import express from 'express';\nexport const app = express();\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	service := NewService()
	service.sourceLocation = dir
	server := NewCode(service)
	codeResponse, err := server.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_GetProjectInfo{GetProjectInfo: &codev0.GetProjectInfoRequest{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNodeProjectInfo(t, codeResponse.GetGetProjectInfo().GetModule(), codeResponse.GetGetProjectInfo().GetDependencies(), codeResponse.GetGetProjectInfo().GetSourceFiles())
	if got := codeResponse.GetGetProjectInfo().GetLanguage(); got != "typescript" {
		t.Fatalf("code project language = %q, want typescript", got)
	}

	toolingResponse, err := NewTooling(server, nil).GetProjectInfo(context.Background(), &toolingv0.GetProjectInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if toolingResponse.GetFailure() != nil {
		t.Fatalf("tooling project info failure = %+v", toolingResponse.GetFailure())
	}
	if toolingResponse.GetModule() != "checkout-ui" || len(toolingResponse.GetDependencies()) != 1 ||
		toolingResponse.GetLanguage() != "typescript" ||
		toolingResponse.GetDependencies()[0].GetName() != "express" || toolingResponse.GetDependencies()[0].GetVersion() != "^5.1.0" ||
		len(toolingResponse.GetSourceFiles()) != 1 || toolingResponse.GetSourceFiles()[0].GetPath() != "src/server.ts" ||
		len(toolingResponse.GetSourceFiles()[0].GetImports()) != 1 || toolingResponse.GetSourceFiles()[0].GetImports()[0] != "express" {
		t.Fatalf("tooling project info = %+v", toolingResponse)
	}
	semantic, err := NewTooling(server, nil).GetSemanticIndex(context.Background(), &toolingv0.GetSemanticIndexRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if semantic.GetFailure() != nil || semantic.GetIndex().GetState() != basev0.SemanticIndexState_SEMANTIC_INDEX_STATE_COMPLETE || len(semantic.GetIndex().GetFiles()) != 1 || len(semantic.GetIndex().GetSymbols()) == 0 {
		t.Fatalf("semantic index = %+v", semantic)
	}
}

func TestProjectInfoLoadsAttachedDeclarationBeforeRuntime(t *testing.T) {
	physical := t.TempDir()
	if err := os.WriteFile(filepath.Join(physical, "package.json"), []byte(`{"name":"preload-ui"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "index.ts"), []byte("export const ready = true;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	serviceRoot := t.TempDir()
	agentDefinition := &resources.Agent{Kind: resources.ServiceAgent, Publisher: "codefly.dev", Name: "nextjs", Version: "v0.0.0"}
	declaration := &resources.Service{
		Name: "source", Version: "0.0.0", Agent: agentDefinition,
		Spec: map[string]any{"source-dir": "attached"},
	}
	declaration.WithDir(serviceRoot)
	if err := declaration.Save(t.Context()); err != nil {
		t.Fatalf("save service declaration: %v", err)
	}
	if err := os.Symlink(physical, filepath.Join(serviceRoot, "attached")); err != nil {
		t.Fatalf("attach source: %v", err)
	}
	t.Setenv(agents.WorkDirEnvironment, serviceRoot)

	service := NewService()
	server := NewCode(service)
	response, err := server.Execute(t.Context(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_GetProjectInfo{GetProjectInfo: &codev0.GetProjectInfoRequest{}},
	})
	if err != nil {
		t.Fatalf("pre-runtime project info: %v", err)
	}
	if got := response.GetGetProjectInfo().GetModule(); got != "preload-ui" {
		t.Fatalf("module = %q, want attached project", got)
	}
	semantic, err := NewTooling(server, nil).GetSemanticIndex(t.Context(), &toolingv0.GetSemanticIndexRequest{})
	if err != nil {
		t.Fatalf("pre-runtime semantic index: %v", err)
	}
	if semantic.GetFailure() != nil || semantic.GetIndex().GetState() != basev0.SemanticIndexState_SEMANTIC_INDEX_STATE_COMPLETE ||
		len(semantic.GetIndex().GetFiles()) != 1 || len(semantic.GetIndex().GetSymbols()) == 0 {
		t.Fatalf("pre-runtime semantic index = %+v, want complete attached source", semantic)
	}
	physicalResolved, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	if service.currentSourceLocation() != physicalResolved || service.Settings.SourceDir != "attached" {
		t.Fatalf("source binding = %q settings=%+v, want physical attached source", service.currentSourceLocation(), service.Settings)
	}
}

func TestProjectInfoReportsJavaScriptForJavaScriptOnlyProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"payments","main":"index.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("module.exports = { charge() {} };\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewService()
	service.sourceLocation = dir
	server := NewCode(service)
	codeResponse, err := server.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_GetProjectInfo{GetProjectInfo: &codev0.GetProjectInfoRequest{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := codeResponse.GetGetProjectInfo().GetLanguage(); got != "javascript" {
		t.Fatalf("code project language = %q, want javascript", got)
	}

	toolingResponse, err := NewTooling(server, nil).GetProjectInfo(context.Background(), &toolingv0.GetProjectInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolingResponse.GetLanguage(); got != "javascript" {
		t.Fatalf("tooling project language = %q, want javascript", got)
	}
}

func assertNodeProjectInfo(t *testing.T, module string, dependencies []*codev0.Dependency, sourceFiles []*codev0.SourceFileInfo) {
	t.Helper()
	if module != "checkout-ui" || len(dependencies) != 1 || dependencies[0].GetName() != "express" ||
		dependencies[0].GetVersion() != "^5.1.0" || !dependencies[0].GetDirect() {
		t.Fatalf("code project module/dependencies = %q/%+v", module, dependencies)
	}
	if len(sourceFiles) != 1 || sourceFiles[0].GetPath() != "src/server.ts" ||
		len(sourceFiles[0].GetImports()) != 1 || sourceFiles[0].GetImports()[0] != "express" {
		t.Fatalf("code project source files = %+v", sourceFiles)
	}
}

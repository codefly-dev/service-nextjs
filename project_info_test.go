package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
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

	toolingResponse, err := NewTooling(server, nil).GetProjectInfo(context.Background(), &toolingv0.GetProjectInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if toolingResponse.GetFailure() != nil {
		t.Fatalf("tooling project info failure = %+v", toolingResponse.GetFailure())
	}
	if toolingResponse.GetModule() != "checkout-ui" || len(toolingResponse.GetDependencies()) != 1 ||
		toolingResponse.GetDependencies()[0].GetName() != "express" || toolingResponse.GetDependencies()[0].GetVersion() != "^5.1.0" ||
		len(toolingResponse.GetSourceFiles()) != 1 || toolingResponse.GetSourceFiles()[0].GetPath() != "src/server.ts" ||
		len(toolingResponse.GetSourceFiles()[0].GetImports()) != 1 || toolingResponse.GetSourceFiles()[0].GetImports()[0] != "express" {
		t.Fatalf("tooling project info = %+v", toolingResponse)
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

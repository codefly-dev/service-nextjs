package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
)

func TestParseESLintFixUsesFixedOutputAndDiagnostics(t *testing.T) {
	data := []byte(`[{"output":"const value = 1;\n","messages":[{"ruleId":"no-unused-vars","severity":2,"message":"unused","line":1,"column":7}]}]`)
	result, err := parseESLintFix(data, []byte("const value=1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != "const value = 1;\n" || len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0], "no-unused-vars") {
		t.Fatalf("parsed result = %+v", result)
	}
}

func TestNextFixerUsesProjectLocalESLintWithoutWritingOnDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"eslint"},"devDependencies":{"eslint":"^9"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "const value=1\n"
	if err := os.WriteFile(filepath.Join(dir, "sample.ts"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeNPM := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '[{\"output\":\"const value = 1;\\n\",\"messages\":[]}]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte(fakeNPM), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	svc := NewService()
	svc.sourceLocation = dir
	server := NewCode(svc)
	response, err := server.Execute(context.Background(), &codev0.CodeRequest{Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{
		File: "sample.ts", DryRun: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fix := response.GetFix()
	if !fix.GetSuccess() || !fix.GetChanged() || fix.GetWrote() || fix.GetContent() != "const value = 1;\n" {
		t.Fatalf("Next fixer = %+v failure=%+v", fix, response.GetFailure())
	}
	written, _ := os.ReadFile(filepath.Join(dir, "sample.ts"))
	if string(written) != original {
		t.Fatalf("dry-run changed source: %q", written)
	}
}

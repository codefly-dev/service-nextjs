package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChangedGeneratedFilesDetectsAddsChangesAndRemovals(t *testing.T) {
	actual := t.TempDir()
	expected := t.TempDir()
	writeGeneratedTestFile(t, actual, "same_grpc_pb.ts", "same")
	writeGeneratedTestFile(t, expected, "same_grpc_pb.ts", "same")
	writeGeneratedTestFile(t, actual, "changed_grpc_pb.ts", "old")
	writeGeneratedTestFile(t, expected, "changed_grpc_pb.ts", "new")
	writeGeneratedTestFile(t, actual, "removed_grpc_pb.ts", "stale")
	writeGeneratedTestFile(t, expected, "added_grpc_pb.ts", "generated")
	writeGeneratedTestFile(t, actual, "saas/accounts/v1/user_settings_pb.ts", "producer-owned")
	writeGeneratedTestFile(t, expected, "saas/accounts/v1/user_settings_pb.ts", "different but producer-owned")

	changed, err := changedGeneratedFiles(actual, expected, "module/services/frontend/code/src/gen")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"module/services/frontend/code/src/gen/added_grpc_pb.ts",
		"module/services/frontend/code/src/gen/changed_grpc_pb.ts",
		"module/services/frontend/code/src/gen/removed_grpc_pb.ts",
	}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed files = %v, want %v", changed, want)
	}
}

func TestChangedGeneratedFilesTreatsMissingTreesAsEmpty(t *testing.T) {
	root := t.TempDir()
	expected := filepath.Join(root, "expected")
	writeGeneratedTestFile(t, expected, "client_grpc_pb.ts", "generated")
	changed, err := changedGeneratedFiles(filepath.Join(root, "missing"), expected, "code/src/gen")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{"code/src/gen/client_grpc_pb.ts"}) {
		t.Fatalf("changed files = %v", changed)
	}
}

func TestCleanDependencyGeneratedFilesPreservesProducerOwnedTrees(t *testing.T) {
	root := t.TempDir()
	writeGeneratedTestFile(t, root, "users_accounts_grpc_pb.ts", "owned")
	writeGeneratedTestFile(t, root, "saas/accounts/v1/user_settings_pb.ts", "producer-owned")
	writeGeneratedTestFile(t, root, "manual.ts", "product-owned")

	if err := cleanDependencyGeneratedFiles(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "users_accounts_grpc_pb.ts")); !os.IsNotExist(err) {
		t.Fatalf("owned dependency client still exists: %v", err)
	}
	for _, relative := range []string{"saas/accounts/v1/user_settings_pb.ts", "manual.ts"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("preserve %s: %v", relative, err)
		}
	}
}

func TestServiceWorkspaceRelativePrefersPhysicalRepositoryPath(t *testing.T) {
	root := t.TempDir()
	physicalModule := filepath.Join(root, "module")
	service := filepath.Join(physicalModule, "services", "frontend")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	logicalModules := filepath.Join(root, "modules")
	if err := os.MkdirAll(logicalModules, 0o755); err != nil {
		t.Fatal(err)
	}
	logicalModule := filepath.Join(logicalModules, "saas-starter")
	if err := os.Symlink(physicalModule, logicalModule); err != nil {
		t.Skipf("cannot create symlink fixture: %v", err)
	}
	service = filepath.Join(logicalModule, "services", "frontend")
	if got := serviceWorkspaceRelative(root, service, "modules/saas-starter/services/frontend"); got != "module/services/frontend" {
		t.Fatalf("physical relative path = %q", got)
	}
	if got := serviceWorkspaceRelative(root, filepath.Dir(root), "fallback/service"); got != "fallback/service" {
		t.Fatalf("out-of-workspace fallback = %q", got)
	}
}

func writeGeneratedTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

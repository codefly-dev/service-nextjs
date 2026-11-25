package main

import (
	"context"
	"fmt"

	corecode "github.com/codefly-dev/core/code"
	"github.com/codefly-dev/core/failures"
	basev1 "github.com/codefly-dev/core/generated/go/codefly/base/v1"
	codev1 "github.com/codefly-dev/core/generated/go/codefly/services/code/v1"
	runtimev1 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v1"
	toolingv1 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v1"
)

// ARCHITECTURE: Tooling implements the codefly Tooling gRPC service for
// NextJS/Node. It delegates project metadata and semantic projection to Code;
// project bytes remain inside Codefly and Mind receives typed facts only.
type Tooling struct {
	toolingv1.UnimplementedToolingServer
	code    *Code
	runtime *Runtime
}

func (t *Tooling) Fix(ctx context.Context, req *toolingv1.FixRequest) (*toolingv1.FixResponse, error) {
	response, err := t.code.Execute(ctx, &codev1.CodeRequest{Operation: &codev1.CodeRequest_Fix{Fix: &codev1.FixRequest{
		File: req.GetFile(), Mode: req.GetMode(), DryRun: req.GetDryRun(),
	}}})
	if err != nil {
		return nil, fmt.Errorf("tooling fix: %w", err)
	}
	fix := response.GetFix()
	if fix == nil {
		return &toolingv1.FixResponse{Success: false, Failure: failures.Ensure(response.GetFailure(), basev1.FailureCode_FAILURE_CODE_INTERNAL, "tooling.fix", "code service returned no fix result")}, nil
	}
	return &toolingv1.FixResponse{
		Success: fix.Success, Content: fix.Content, Actions: fix.Actions,
		Failure: failures.Clone(response.GetFailure()), Changed: fix.Changed,
		BeforeSha256: fix.BeforeSha256, AfterSha256: fix.AfterSha256,
		Wrote: fix.Wrote, Output: fix.Output,
	}, nil
}

func (t *Tooling) ApplyEdit(ctx context.Context, req *toolingv1.ApplyEditRequest) (*toolingv1.ApplyEditResponse, error) {
	response, err := t.code.Execute(ctx, &codev1.CodeRequest{Operation: &codev1.CodeRequest_ApplyEdit{ApplyEdit: &codev1.ApplyEditRequest{
		File: req.GetFile(), Find: req.GetFind(), Replace: req.GetReplace(),
		FixMode: req.GetFixMode(), DryRun: req.GetDryRun(),
	}}})
	if err != nil {
		return nil, fmt.Errorf("tooling apply_edit: %w", err)
	}
	edit := response.GetApplyEdit()
	if edit == nil {
		return &toolingv1.ApplyEditResponse{Success: false, Failure: failures.Ensure(response.GetFailure(), basev1.FailureCode_FAILURE_CODE_INTERNAL, "tooling.apply-edit", "code service returned no apply-edit result")}, nil
	}
	return &toolingv1.ApplyEditResponse{
		Success: edit.Success, Content: edit.Content, Strategy: edit.Strategy,
		FixActions: edit.FixActions, Failure: failures.Clone(response.GetFailure()),
		Changed: edit.Changed, BeforeSha256: edit.BeforeSha256, AfterSha256: edit.AfterSha256,
		Wrote: edit.Wrote, Output: edit.Output,
	}, nil
}

func (t *Tooling) Build(ctx context.Context, _ *toolingv1.BuildRequest) (*toolingv1.BuildResponse, error) {
	resp, err := t.runtime.Build(ctx, &runtimev1.BuildRequest{})
	if err != nil {
		return nil, fmt.Errorf("tooling build: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev1.BuildStatus_SUCCESS
	return &toolingv1.BuildResponse{Success: success, Output: resp.GetOutput(), Failure: failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev1.FailureCode_FAILURE_CODE_PROCESS_FAILED, "tooling.build", nextToolingFailureSummary("tooling build", resp.GetOutput()))}, nil
}

func (t *Tooling) Test(ctx context.Context, req *toolingv1.TestRequest) (*toolingv1.TestResponse, error) {
	runtimeReq := &runtimev1.TestRequest{Target: req.GetPath(), Verbose: req.GetVerbose()}
	resp, err := t.runtime.Test(ctx, runtimeReq)
	if err != nil {
		return nil, fmt.Errorf("tooling test: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev1.TestStatus_SUCCESS
	return &toolingv1.TestResponse{
		Success:      success,
		Output:       resp.GetOutput(),
		TestsRun:     resp.GetTestsRun(),
		TestsPassed:  resp.GetTestsPassed(),
		TestsFailed:  resp.GetTestsFailed(),
		TestsSkipped: resp.GetTestsSkipped(),
		CoveragePct:  resp.GetCoveragePct(),
		Failures:     resp.GetFailures(),
		Failure:      failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev1.FailureCode_FAILURE_CODE_VALIDATION_FAILED, "tooling.test", nextToolingFailureSummary("tooling test", resp.GetOutput())),
	}, nil
}

func (t *Tooling) Lint(ctx context.Context, req *toolingv1.LintRequest) (*toolingv1.LintResponse, error) {
	resp, err := t.runtime.Lint(ctx, &runtimev1.LintRequest{Target: req.GetFile()})
	if err != nil {
		return nil, fmt.Errorf("tooling lint: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev1.LintStatus_SUCCESS
	return &toolingv1.LintResponse{Success: success, Output: resp.GetOutput(), Failure: failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev1.FailureCode_FAILURE_CODE_VALIDATION_FAILED, "tooling.lint", nextToolingFailureSummary("tooling lint", resp.GetOutput()))}, nil
}

func NewTooling(code *Code, runtime *Runtime) *Tooling {
	return &Tooling{code: code, runtime: runtime}
}

// GetSemanticIndex delegates the body-free projection to Core through the
// production Next.js Code server.
func (t *Tooling) GetSemanticIndex(ctx context.Context, req *toolingv1.GetSemanticIndexRequest) (*toolingv1.GetSemanticIndexResponse, error) {
	return corecode.NewSourceTooling(t.code).GetSemanticIndex(ctx, req)
}

func (t *Tooling) GetProjectInfo(ctx context.Context, req *toolingv1.GetProjectInfoRequest) (*toolingv1.GetProjectInfoResponse, error) {
	response, err := t.code.Execute(ctx, &codev1.CodeRequest{Operation: &codev1.CodeRequest_GetProjectInfo{GetProjectInfo: &codev1.GetProjectInfoRequest{}}})
	if err != nil {
		return nil, fmt.Errorf("tooling get_project_info: %w", err)
	}
	resp := response.GetGetProjectInfo()
	if resp == nil {
		return &toolingv1.GetProjectInfoResponse{Failure: failures.Ensure(response.GetFailure(), basev1.FailureCode_FAILURE_CODE_INTERNAL, "tooling.get-project-info", "code service returned no project-info result")}, nil
	}
	packages := make([]*toolingv1.PackageInfo, 0, len(resp.GetPackages()))
	for _, pkg := range resp.GetPackages() {
		packages = append(packages, &toolingv1.PackageInfo{
			Name: pkg.GetName(), RelativePath: pkg.GetRelativePath(), Files: append([]string(nil), pkg.GetFiles()...),
			Imports: append([]string(nil), pkg.GetImports()...), Doc: pkg.GetDoc(),
		})
	}
	dependencies := make([]*toolingv1.Dependency, 0, len(resp.GetDependencies()))
	for _, dependency := range resp.GetDependencies() {
		dependencies = append(dependencies, &toolingv1.Dependency{
			Name: dependency.GetName(), Version: dependency.GetVersion(), Direct: dependency.GetDirect(),
		})
	}
	sourceFiles := make([]*toolingv1.SourceFileInfo, 0, len(resp.GetSourceFiles()))
	for _, file := range resp.GetSourceFiles() {
		sourceFiles = append(sourceFiles, &toolingv1.SourceFileInfo{
			Path: file.GetPath(), Imports: append([]string(nil), file.GetImports()...),
		})
	}
	return &toolingv1.GetProjectInfoResponse{
		Module: resp.GetModule(), Language: resp.GetLanguage(), LanguageVersion: resp.GetLanguageVersion(),
		Packages: packages, Dependencies: dependencies, FileHashes: resp.GetFileHashes(), SourceFiles: sourceFiles,
		Failure: failures.Clone(response.GetFailure()),
	}, nil
}

func nextToolingFailureSummary(operation, output string) string {
	if output == "" {
		return operation + " failed without structured status"
	}
	return output
}

package main

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/failures"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
)

// ARCHITECTURE: Tooling implements the codefly Tooling gRPC service for NextJS/Node.
// Delegates to Code for project metadata. Semantic code intelligence belongs
// to Mind, not this plugin contract.
type Tooling struct {
	toolingv0.UnimplementedToolingServer
	code    *Code
	runtime *Runtime
}

func (t *Tooling) Fix(ctx context.Context, req *toolingv0.FixRequest) (*toolingv0.FixResponse, error) {
	response, err := t.code.Execute(ctx, &codev0.CodeRequest{Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{
		File: req.GetFile(), Mode: req.GetMode(), DryRun: req.GetDryRun(),
	}}})
	if err != nil {
		return nil, fmt.Errorf("tooling fix: %w", err)
	}
	fix := response.GetFix()
	if fix == nil {
		return &toolingv0.FixResponse{Success: false, Failure: failures.Ensure(response.GetFailure(), basev0.FailureCode_FAILURE_CODE_INTERNAL, "tooling.fix", "code service returned no fix result")}, nil
	}
	return &toolingv0.FixResponse{
		Success: fix.Success, Content: fix.Content, Actions: fix.Actions,
		Failure: failures.Clone(response.GetFailure()), Changed: fix.Changed,
		BeforeSha256: fix.BeforeSha256, AfterSha256: fix.AfterSha256,
		Wrote: fix.Wrote, Output: fix.Output,
	}, nil
}

func (t *Tooling) ApplyEdit(ctx context.Context, req *toolingv0.ApplyEditRequest) (*toolingv0.ApplyEditResponse, error) {
	response, err := t.code.Execute(ctx, &codev0.CodeRequest{Operation: &codev0.CodeRequest_ApplyEdit{ApplyEdit: &codev0.ApplyEditRequest{
		File: req.GetFile(), Find: req.GetFind(), Replace: req.GetReplace(),
		FixMode: req.GetFixMode(), DryRun: req.GetDryRun(),
	}}})
	if err != nil {
		return nil, fmt.Errorf("tooling apply_edit: %w", err)
	}
	edit := response.GetApplyEdit()
	if edit == nil {
		return &toolingv0.ApplyEditResponse{Success: false, Failure: failures.Ensure(response.GetFailure(), basev0.FailureCode_FAILURE_CODE_INTERNAL, "tooling.apply-edit", "code service returned no apply-edit result")}, nil
	}
	return &toolingv0.ApplyEditResponse{
		Success: edit.Success, Content: edit.Content, Strategy: edit.Strategy,
		FixActions: edit.FixActions, Failure: failures.Clone(response.GetFailure()),
		Changed: edit.Changed, BeforeSha256: edit.BeforeSha256, AfterSha256: edit.AfterSha256,
		Wrote: edit.Wrote, Output: edit.Output,
	}, nil
}

func (t *Tooling) Build(ctx context.Context, _ *toolingv0.BuildRequest) (*toolingv0.BuildResponse, error) {
	resp, err := t.runtime.Build(ctx, &runtimev0.BuildRequest{})
	if err != nil {
		return nil, fmt.Errorf("tooling build: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev0.BuildStatus_SUCCESS
	return &toolingv0.BuildResponse{Success: success, Output: resp.GetOutput(), Failure: failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev0.FailureCode_FAILURE_CODE_PROCESS_FAILED, "tooling.build", nextToolingFailureSummary("tooling build", resp.GetOutput()))}, nil
}

func (t *Tooling) Test(ctx context.Context, req *toolingv0.TestRequest) (*toolingv0.TestResponse, error) {
	runtimeReq := &runtimev0.TestRequest{Target: req.GetPath(), Verbose: req.GetVerbose()}
	resp, err := t.runtime.Test(ctx, runtimeReq)
	if err != nil {
		return nil, fmt.Errorf("tooling test: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev0.TestStatus_SUCCESS
	return &toolingv0.TestResponse{
		Success:      success,
		Output:       resp.GetOutput(),
		TestsRun:     resp.GetTestsRun(),
		TestsPassed:  resp.GetTestsPassed(),
		TestsFailed:  resp.GetTestsFailed(),
		TestsSkipped: resp.GetTestsSkipped(),
		CoveragePct:  resp.GetCoveragePct(),
		Failures:     resp.GetFailures(),
		Failure:      failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev0.FailureCode_FAILURE_CODE_VALIDATION_FAILED, "tooling.test", nextToolingFailureSummary("tooling test", resp.GetOutput())),
	}, nil
}

func (t *Tooling) Lint(ctx context.Context, req *toolingv0.LintRequest) (*toolingv0.LintResponse, error) {
	resp, err := t.runtime.Lint(ctx, &runtimev0.LintRequest{Target: req.GetFile()})
	if err != nil {
		return nil, fmt.Errorf("tooling lint: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev0.LintStatus_SUCCESS
	return &toolingv0.LintResponse{Success: success, Output: resp.GetOutput(), Failure: failures.ForOutcome(success, resp.GetStatus().GetFailure(), basev0.FailureCode_FAILURE_CODE_VALIDATION_FAILED, "tooling.lint", nextToolingFailureSummary("tooling lint", resp.GetOutput()))}, nil
}

func NewTooling(code *Code, runtime *Runtime) *Tooling {
	return &Tooling{code: code, runtime: runtime}
}

func (t *Tooling) GetProjectInfo(ctx context.Context, req *toolingv0.GetProjectInfoRequest) (*toolingv0.GetProjectInfoResponse, error) {
	response, err := t.code.Execute(ctx, &codev0.CodeRequest{Operation: &codev0.CodeRequest_GetProjectInfo{GetProjectInfo: &codev0.GetProjectInfoRequest{}}})
	if err != nil {
		return nil, fmt.Errorf("tooling get_project_info: %w", err)
	}
	resp := response.GetGetProjectInfo()
	if resp == nil {
		return &toolingv0.GetProjectInfoResponse{Failure: failures.Ensure(response.GetFailure(), basev0.FailureCode_FAILURE_CODE_INTERNAL, "tooling.get-project-info", "code service returned no project-info result")}, nil
	}
	packages := make([]*toolingv0.PackageInfo, 0, len(resp.GetPackages()))
	for _, pkg := range resp.GetPackages() {
		packages = append(packages, &toolingv0.PackageInfo{
			Name: pkg.GetName(), RelativePath: pkg.GetRelativePath(), Files: append([]string(nil), pkg.GetFiles()...),
			Imports: append([]string(nil), pkg.GetImports()...), Doc: pkg.GetDoc(),
		})
	}
	dependencies := make([]*toolingv0.Dependency, 0, len(resp.GetDependencies()))
	for _, dependency := range resp.GetDependencies() {
		dependencies = append(dependencies, &toolingv0.Dependency{
			Name: dependency.GetName(), Version: dependency.GetVersion(), Direct: dependency.GetDirect(),
		})
	}
	sourceFiles := make([]*toolingv0.SourceFileInfo, 0, len(resp.GetSourceFiles()))
	for _, file := range resp.GetSourceFiles() {
		sourceFiles = append(sourceFiles, &toolingv0.SourceFileInfo{
			Path: file.GetPath(), Imports: append([]string(nil), file.GetImports()...),
		})
	}
	return &toolingv0.GetProjectInfoResponse{
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

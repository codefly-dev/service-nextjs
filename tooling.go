package main

import (
	"context"
	"fmt"

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

func (t *Tooling) Build(ctx context.Context, _ *toolingv0.BuildRequest) (*toolingv0.BuildResponse, error) {
	resp, err := t.runtime.Build(ctx, &runtimev0.BuildRequest{})
	if err != nil {
		return nil, fmt.Errorf("tooling build: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev0.BuildStatus_SUCCESS
	return &toolingv0.BuildResponse{Success: success, Output: resp.GetOutput()}, nil
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
	}, nil
}

func (t *Tooling) Lint(ctx context.Context, req *toolingv0.LintRequest) (*toolingv0.LintResponse, error) {
	resp, err := t.runtime.Lint(ctx, &runtimev0.LintRequest{Target: req.GetFile()})
	if err != nil {
		return nil, fmt.Errorf("tooling lint: %w", err)
	}
	success := resp.GetStatus().GetState() == runtimev0.LintStatus_SUCCESS
	return &toolingv0.LintResponse{Success: success, Output: resp.GetOutput()}, nil
}

func NewTooling(code *Code, runtime *Runtime) *Tooling {
	return &Tooling{code: code, runtime: runtime}
}

func (t *Tooling) GetProjectInfo(ctx context.Context, req *toolingv0.GetProjectInfoRequest) (*toolingv0.GetProjectInfoResponse, error) {
	resp, err := t.code.GetProjectInfo(ctx, &codev0.GetProjectInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("tooling get_project_info: %w", err)
	}
	return &toolingv0.GetProjectInfoResponse{
		Module:          resp.Module,
		LanguageVersion: resp.LanguageVersion,
	}, nil
}

package main

import (
	"context"
	"fmt"

	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
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

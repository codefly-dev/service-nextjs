package main

import (
	"context"
	"fmt"

	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
)

// ARCHITECTURE: Tooling implements the codefly Tooling gRPC service for NextJS/Node.
// Delegates to Code for analysis ops (ListSymbols, GetProjectInfo).
//
// Mind's ingestion pipeline calls ToolingClient.ListSymbols() via the
// ToolingCodeClient adapter. The hash fields (body_hash, signature_hash,
// qualified_name) flow through from Code → Tooling → Mind.
type Tooling struct {
	toolingv0.UnimplementedToolingServer
	code    *Code
	runtime *Runtime
}

func NewTooling(code *Code, runtime *Runtime) *Tooling {
	return &Tooling{code: code, runtime: runtime}
}

func (t *Tooling) ListSymbols(ctx context.Context, req *toolingv0.ListSymbolsRequest) (*toolingv0.ListSymbolsResponse, error) {
	resp, err := t.code.Execute(ctx, &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ListSymbols{ListSymbols: &codev0.ListSymbolsRequest{File: req.File}},
	})
	if err != nil {
		return nil, fmt.Errorf("tooling list_symbols: %w", err)
	}
	ls := resp.GetListSymbols()
	if ls == nil {
		return &toolingv0.ListSymbolsResponse{}, nil
	}
	return &toolingv0.ListSymbolsResponse{Symbols: codeSymbolsToTooling(ls.Symbols)}, nil
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

// ── Type converters ─────────────────────────────────────

func codeSymbolsToTooling(symbols []*codev0.Symbol) []*toolingv0.Symbol {
	var out []*toolingv0.Symbol
	for _, s := range symbols {
		ts := &toolingv0.Symbol{
			Name: s.Name, Kind: toolingv0.SymbolKind(s.Kind),
			Signature: s.Signature, Documentation: s.Documentation, Parent: s.Parent,
			QualifiedName: s.QualifiedName,
			BodyHash:      s.BodyHash,
			SignatureHash: s.SignatureHash,
		}
		if s.Location != nil {
			ts.Location = &toolingv0.Location{
				File: s.Location.File, Line: s.Location.Line, Column: s.Location.Column,
				EndLine: s.Location.EndLine, EndColumn: s.Location.EndColumn,
			}
		}
		ts.Children = codeSymbolsToTooling(s.Children)
		out = append(out, ts)
	}
	return out
}

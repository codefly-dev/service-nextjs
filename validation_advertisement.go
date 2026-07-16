package main

import agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"

func nextValidationCapabilities() *agentv0.ValidationCapabilities {
	workspace := []agentv0.ValidationScope{agentv0.ValidationScope_VALIDATION_SCOPE_WORKSPACE}
	return &agentv0.ValidationCapabilities{
		Lint: &agentv0.ValidationOperationCapability{
			Supported: true,
			Scopes: []agentv0.ValidationScope{
				agentv0.ValidationScope_VALIDATION_SCOPE_WORKSPACE,
				agentv0.ValidationScope_VALIDATION_SCOPE_PACKAGE,
				agentv0.ValidationScope_VALIDATION_SCOPE_FILE,
			},
			SupportsFix: true,
		},
		Compile: validationOperation(workspace),
		Test: &agentv0.TestValidationCapability{
			Supported: true,
			Scopes:    workspace,
			Suites: []*agentv0.TestSuiteCapability{{
				Name:           "unit",
				DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE,
				DefaultSuite:   true,
			}},
		},
		Audit:         validationOperation(workspace),
		ArtifactBuild: validationOperation(workspace),
		Sbom:          validationOperation(workspace),
		Sync:          validationOperation(workspace),
	}
}

func validationOperation(scopes []agentv0.ValidationScope) *agentv0.ValidationOperationCapability {
	return &agentv0.ValidationOperationCapability{Supported: true, Scopes: scopes}
}

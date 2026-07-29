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
		},
		Compile: validationOperation(workspace),
		Test: &agentv0.TestValidationCapability{
			Supported: true,
			Scopes:    workspace,
			Suites: []*agentv0.TestSuiteCapability{
				{
					// A frontend's useful unit is its headless application
					// capability, not an isolated React function. Start the
					// declared dependency graph so Vitest can exercise the
					// real service APIs, databases, queues, and fixtures with
					// ordinary unit-test ergonomics.
					Name:           "unit",
					DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
					DefaultSuite:   true,
				},
				{
					// Pure remains an explicit fast path for packages that
					// genuinely have no runtime boundary.
					Name:           "pure",
					DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE,
				},
				{
					Name:           "integration",
					DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
				},
				{
					Name:           "e2e",
					DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
				},
				{
					Name:           "smoke",
					DependencyMode: agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_STACK,
				},
			},
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

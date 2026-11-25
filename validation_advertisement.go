package main

import agentv1 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v1"

func nextValidationCapabilities() *agentv1.ValidationCapabilities {
	workspace := []agentv1.ValidationScope{agentv1.ValidationScope_VALIDATION_SCOPE_WORKSPACE}
	return &agentv1.ValidationCapabilities{
		Lint: &agentv1.ValidationOperationCapability{
			Supported: true,
			Scopes: []agentv1.ValidationScope{
				agentv1.ValidationScope_VALIDATION_SCOPE_WORKSPACE,
				agentv1.ValidationScope_VALIDATION_SCOPE_PACKAGE,
				agentv1.ValidationScope_VALIDATION_SCOPE_FILE,
			},
		},
		Compile: validationOperation(workspace),
		Test: &agentv1.TestValidationCapability{
			Supported: true,
			Scopes:    workspace,
			Suites: []*agentv1.TestSuiteCapability{
				{
					// A frontend's useful unit is its headless application
					// capability, not an isolated React function. Start the
					// declared dependency graph so Vitest can exercise the
					// real service APIs, databases, queues, and fixtures with
					// ordinary unit-test ergonomics.
					Name:           "unit",
					DependencyMode: agentv1.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
					DefaultSuite:   true,
				},
				{
					// Pure remains an explicit fast path for packages that
					// genuinely have no runtime boundary.
					Name:           "pure",
					DependencyMode: agentv1.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE,
				},
				{
					Name:           "integration",
					DependencyMode: agentv1.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
				},
				{
					Name:           "e2e",
					DependencyMode: agentv1.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
				},
				{
					Name:           "smoke",
					DependencyMode: agentv1.TestDependencyMode_TEST_DEPENDENCY_MODE_START_STACK,
				},
			},
		},
		Audit:         validationOperation(workspace),
		ArtifactBuild: validationOperation(workspace),
		Sbom:          validationOperation(workspace),
		Sync:          validationOperation(workspace),
	}
}

func validationOperation(scopes []agentv1.ValidationScope) *agentv1.ValidationOperationCapability {
	return &agentv1.ValidationOperationCapability{Supported: true, Scopes: scopes}
}

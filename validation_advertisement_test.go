package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
)

func TestFrontendTestSuitesDeclareTheirProductionGraph(t *testing.T) {
	capabilities := nextValidationCapabilities().GetTest()
	require.True(t, capabilities.GetSupported())

	suites := make(map[string]*agentv0.TestSuiteCapability)
	for _, suite := range capabilities.GetSuites() {
		suites[suite.GetName()] = suite
	}

	require.Equal(
		t,
		agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
		suites["unit"].GetDependencyMode(),
	)
	require.True(t, suites["unit"].GetDefaultSuite())
	require.Equal(
		t,
		agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE,
		suites["pure"].GetDependencyMode(),
	)
	require.Equal(
		t,
		agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
		suites["integration"].GetDependencyMode(),
	)
	require.Equal(
		t,
		agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_DEPENDENCIES,
		suites["e2e"].GetDependencyMode(),
	)
	require.Equal(
		t,
		agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_START_STACK,
		suites["smoke"].GetDependencyMode(),
	)
}

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
)

// Core derives the required CI task set from this inventory, so a capability it
// cannot turn into a valid task key fails effective-input discovery for the
// whole agent rather than just that phase.
func TestAdvertisedCapabilitiesAreValidTaskKeys(t *testing.T) {
	required, err := ciinputs.Required(nextValidationCapabilities())
	require.NoError(t, err)
	require.NotEmpty(t, required)

	_, err = ciinputs.Evaluate(nil, &agentv0.GetEffectiveInputsRequest{
		SchemaVersion: ciinputs.Version,
		Snapshot:      "capabilities",
	}, required)
	require.NoError(t, err)
}

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

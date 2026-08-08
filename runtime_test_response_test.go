package main

import (
	"errors"
	"testing"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/stretchr/testify/require"
)

func TestCompletedTestRPCResultPreservesStructuredFailures(t *testing.T) {
	executionErr := errors.New("test process exited 1")
	response := &runtimev0.TestResponse{}

	got, err := completedTestRPCResult(response, executionErr)

	require.NoError(t, err)
	require.Same(t, response, got)
}

func TestCompletedTestRPCResultPreservesPreReportCrashes(t *testing.T) {
	executionErr := errors.New("test runner crashed")

	got, err := completedTestRPCResult(nil, executionErr)

	require.Nil(t, got)
	require.ErrorIs(t, err, executionErr)
}

func TestPlaywrightReporterUsesItsJSONFileContract(t *testing.T) {
	args, envs := nodeTestReporterConfiguration(nodeTestPlaywright, "/tmp/result.json")

	require.Equal(t, []string{"--reporter=json"}, args)
	require.Len(t, envs, 1)
	require.Equal(t, "PLAYWRIGHT_JSON_OUTPUT_FILE", envs[0].Key)
	require.Equal(t, "/tmp/result.json", envs[0].Value)
}

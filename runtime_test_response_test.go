package main

import (
	"errors"
	"strings"
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

func TestMissingPlaywrightBrowsersSelectsOnlyProvenEngines(t *testing.T) {
	report := []byte(`{
  "config": {"projects": [{"name": "chromium"}, {"name": "firefox"}, {"name": "webkit"}]},
  "suites": [{"specs": [{"tests": [
    {"results": [{"error": {"message": "browserType.launch: Executable doesn't exist at /cache/firefox-1495/firefox\\nPlease run: npx playwright install firefox"}}]},
    {"results": [{"error": {"message": "browserType.launch: Executable doesn't exist at /cache/webkit-2215/pw_run.sh\\nPlease run: npx playwright install webkit"}}]}
  ]}]}]
}`)

	require.Equal(t, []string{"firefox", "webkit"}, missingPlaywrightBrowsers(report))
}

func TestMissingPlaywrightBrowsersRejectsOrdinaryFailures(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"error":"chromium assertion failed"}`),
		[]byte(`{"error":"Executable doesn't exist at /tmp/chromium"}`),
		[]byte(`{"error":"run playwright install chromium after updating dependencies"}`),
		[]byte(`{"error":"Executable doesn't exist at /tmp/chromium","other":"` +
			strings.Repeat("x", 1100) + ` playwright install chromium"}`),
	}
	for _, report := range tests {
		require.Empty(t, missingPlaywrightBrowsers(report))
	}
}

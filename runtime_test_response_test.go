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

package main

import (
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

func TestNextRuntimeEnvironmentDisablesDevWatcherPolling(t *testing.T) {
	envs := resources.EnvironmentVariableAsStrings(nextRuntimeEnvironment(NextExecutionDevelopment))

	// An orphaned `next dev` whose watcher falls back to polling busy-spins a
	// CPU core. Development must force the polling fallback off so a leak stays
	// cheap.
	require.Contains(t, envs, "WATCHPACK_POLLING=false")
	require.Contains(t, envs, "CHOKIDAR_USEPOLLING=false")

	// The resource caps still apply.
	require.Contains(t, envs, "UV_THREADPOOL_SIZE=2")
	require.Contains(t, envs, "NEXT_TELEMETRY_DISABLED=1")
}

func TestNextRuntimeEnvironmentProductionOmitsWatcherControls(t *testing.T) {
	envs := resources.EnvironmentVariableAsStrings(nextRuntimeEnvironment(NextExecutionProduction))

	// `next start` / the standalone server do not run a file watcher, so the
	// polling controls are irrelevant there — but NODE_ENV must be set.
	require.Contains(t, envs, "NODE_ENV=production")
	require.NotContains(t, envs, "WATCHPACK_POLLING=false")
	require.NotContains(t, envs, "CHOKIDAR_USEPOLLING=false")
}

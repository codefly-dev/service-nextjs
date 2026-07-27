package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareProductionServerUsesNextStartForStandardBuild(t *testing.T) {
	source := t.TempDir()

	launch, err := prepareProductionServer(source, 3100)
	require.NoError(t, err)
	require.Equal(t, "npm", launch.command)
	require.Equal(t, []string{"run", "start", "--", "-p", "3100"}, launch.args)
	require.Empty(t, launch.environment)
}

func TestPrepareProductionServerStagesStandaloneOutput(t *testing.T) {
	source := t.TempDir()
	writeProductionTestFile(t, source, ".next/required-server-files.json", `{"config":{"output":"standalone"}}`)
	writeProductionTestFile(t, source, ".next/standalone/server.js", "server")
	writeProductionTestFile(t, source, ".next/static/chunks/app.js", "chunk")
	writeProductionTestFile(t, source, "public/logo.svg", "logo")
	writeProductionTestFile(t, source, ".next/standalone/public/stale.txt", "stale")
	writeProductionTestFile(t, source, ".next/standalone/.next/static/stale.txt", "stale")

	launch, err := prepareProductionServer(source, 3200)
	require.NoError(t, err)
	require.Equal(t, "node", launch.command)
	require.Equal(t, []string{".next/standalone/server.js"}, launch.args)
	require.Len(t, launch.environment, 2)
	require.Equal(t, "PORT", launch.environment[0].Key)
	require.Equal(t, "3200", launch.environment[0].Value)
	require.Equal(t, "HOSTNAME", launch.environment[1].Key)
	require.Equal(t, "0.0.0.0", launch.environment[1].Value)
	require.FileExists(t, filepath.Join(source, ".next/standalone/.next/static/chunks/app.js"))
	require.FileExists(t, filepath.Join(source, ".next/standalone/public/logo.svg"))
	require.NoFileExists(t, filepath.Join(source, ".next/standalone/public/stale.txt"))
	require.NoFileExists(t, filepath.Join(source, ".next/standalone/.next/static/stale.txt"))
}

func TestPrepareProductionServerFailsClosedWhenStandaloneServerIsMissing(t *testing.T) {
	source := t.TempDir()
	writeProductionTestFile(t, source, ".next/required-server-files.json", `{"config":{"output":"standalone"}}`)

	_, err := prepareProductionServer(source, 3200)
	require.ErrorContains(t, err, "standalone build is missing server.js")
}

func TestPrepareProductionServerRejectsMalformedBuildManifest(t *testing.T) {
	source := t.TempDir()
	writeProductionTestFile(t, source, ".next/required-server-files.json", "{")

	_, err := prepareProductionServer(source, 3200)
	require.ErrorContains(t, err, "parse Next.js build manifest")
}

func writeProductionTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

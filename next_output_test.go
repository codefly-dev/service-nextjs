package main

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Turbopack emits hashed package aliases for Sentry's server dependencies.
// Execute the route over HTTP: importing its lazy page module is not sufficient.
func TestNextTurbopackStandaloneRelocation(t *testing.T) {
	if os.Getenv("CODEFLY_TEST_NEXT_BUILD") != "1" {
		t.Skip("set CODEFLY_TEST_NEXT_BUILD=1 for real Turbopack/Sentry production HTTP")
	}
	root := physicalPath(t.TempDir())
	app := filepath.Join(root, "source")
	runtime := filepath.Join(root, "runtime")
	buildInputWrite(t, app, "package.json", `{"name":"relocation-fixture","private":true,"scripts":{"build":"next build"},"dependencies":{"next":"16.4.1","react":"19.2.4","react-dom":"19.2.4","@sentry/nextjs":"10.75.2"}}`)
	buildInputWrite(t, app, "next.config.mjs", `import {withSentryConfig} from '@sentry/nextjs'; export default withSentryConfig({output:'standalone',experimental:{cpus:1}}, {silent:true,telemetry:false,routeManifestInjection:false});`)
	buildInputWrite(t, app, "app/layout.js", `export default function Layout({children}) { return <html><body>{children}</body></html>; }`)
	buildInputWrite(t, app, "app/auth/callback/page.js", `import * as Sentry from '@sentry/nextjs'; export const dynamic='force-dynamic'; export default function Page() { Sentry.getClient(); return <p>relocated-sentry-page</p>; }`)
	buildInputWrite(t, app, "instrumentation.js", `import * as Sentry from '@sentry/nextjs'; export async function register() {} export const onRequestError=Sentry.captureRequestError;`)
	helper, err := fs.ReadFile(builderFS, "templates/builder/context/next-build.cjs.tmpl")
	require.NoError(t, err)
	buildInputWrite(t, root, "next-build.cjs", string(helper))
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
		cmd.Dir = app
		cmd.Env = append(os.Environ(), "NODE_ENV=production", "NEXT_TELEMETRY_DISABLED=1", "npm_config_cache="+filepath.Join(root, "npm-cache"))
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return string(out)
	}
	t.Logf("build/runtime Node: %s", strings.TrimSpace(run("node", "--version")))
	run("npm", "install", "--include=dev", "--no-audit", "--no-fund")
	t.Log(run("node", filepath.Join(root, "next-build.cjs"), app, runtime, "standalone"))
	aliases, err := filepath.Glob(filepath.Join(app, ".next/standalone/.next/node_modules/require-in-the-middle-*"))
	require.NoError(t, err)
	require.NotEmpty(t, aliases, "real Turbopack build must exercise the traced package alias")
	for _, alias := range aliases {
		link, err := os.Readlink(alias)
		require.NoError(t, err)
		require.False(t, filepath.IsAbs(link), "framework alias should be relocatable: %s", link)
		before, err := os.ReadFile(filepath.Join(alias, "index.js"))
		require.NoError(t, err)
		after, err := os.ReadFile(filepath.Join(runtime, ".next/node_modules", filepath.Base(alias), "index.js"))
		require.NoError(t, err)
		require.Equal(t, before, after, "relocation must preserve the package bytes")
		t.Logf("generated alias %s -> %s", filepath.Base(alias), link)
	}
	// Remove the original path from resolution, without deleting its evidence.
	require.NoError(t, os.Rename(app, filepath.Join(root, "source-withheld")))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(t.Context())
	server := exec.CommandContext(ctx, "node", "server.js")
	server.Dir = runtime
	server.Env = append(os.Environ(), "NODE_ENV=production", "NEXT_TELEMETRY_DISABLED=1", "NODE_PATH=", "HOSTNAME=127.0.0.1", "PORT="+port)
	logPath := filepath.Join(root, "production-server.log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	server.Stdout, server.Stderr = logFile, logFile
	require.NoError(t, server.Start())
	t.Cleanup(func() {
		cancel()
		_ = server.Wait()
		_ = logFile.Close()
		log, _ := os.ReadFile(logPath)
		t.Logf("production server: %s", log)
	})
	client := &http.Client{Timeout: 10 * time.Second}
	var response *http.Response
	require.Eventually(t, func() bool {
		response, err = client.Get("http://" + address + "/auth/callback")
		return err == nil
	}, time.Minute, 100*time.Millisecond, "standalone server must answer without its source tree")
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
	require.Contains(t, string(body), "relocated-sentry-page")
	for _, alias := range aliases {
		info, err := os.Lstat(filepath.Join(runtime, ".next/node_modules", filepath.Base(alias)))
		require.NoError(t, err)
		require.True(t, info.IsDir(), "traced package must be materialized, not linked to the build stage")
	}
	t.Log("production GET /auth/callback: 200 with source withheld")
}

// This opt-in test installs the real framework and runs its actual production
// compiler. Recipe unit tests alone cannot establish the standalone layout.
func TestNextConfiguredStandaloneOutput(t *testing.T) {
	if os.Getenv("CODEFLY_TEST_NEXT_BUILD") != "1" {
		t.Skip("set CODEFLY_TEST_NEXT_BUILD=1 for real npm/Next build")
	}
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "custom_nested"}[nested], func(t *testing.T) {
			root := physicalPath(t.TempDir())
			source := filepath.Join(root, "source")
			app := source
			dist := ".next"
			relative := ""
			if nested {
				app = filepath.Join(source, "services", "web")
				dist = ".next-build"
				relative = "services/web"
			}
			manifest := map[string]any{"name": "output-fixture", "private": true, "scripts": map[string]string{"build": "next build --webpack"}, "dependencies": map[string]string{"next": "16.4.1", "react": "19.2.4", "react-dom": "19.2.4"}}
			writeManifest := func() {
				data, err := json.Marshal(manifest)
				require.NoError(t, err)
				buildInputWrite(t, app, "package.json", string(data))
			}
			writeManifest()
			buildInputWrite(t, app, "pages/index.js", `export default function Page() {return <p>actual standalone fixture</p>}`)
			buildInputWrite(t, app, "public/asset.txt", "public asset")
			config := `export default {output:'standalone',distDir:process.env.NODE_ENV==='production'?'` + dist + `':'.next',outputFileTracingRoot:`
			tracing, _ := json.Marshal(source)
			config += string(tracing) + `,experimental:{cpus:1}};`
			buildInputWrite(t, app, "next.config.mjs", config)
			helper, err := fs.ReadFile(builderFS, "templates/builder/context/next-build.cjs.tmpl")
			require.NoError(t, err)
			buildInputWrite(t, root, "next-build.cjs", string(helper))
			run := func(args ...string) (string, error) {
				cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
				cmd.Dir = app
				cmd.Env = append(os.Environ(), "NODE_ENV=production", "NEXT_TELEMETRY_DISABLED=1", "npm_config_cache="+filepath.Join(root, "npm-cache"))
				data, err := cmd.CombinedOutput()
				return string(data), err
			}
			// Production config is loaded only by the build-stage helper, after a normal
			// application install. No host Next installation supplies this fixture.
			out, err := run("npm", "install", "--include=dev", "--no-audit", "--no-fund")
			require.NoError(t, err, "%s", out)
			runtime := filepath.Join(root, "runtime")
			out, err = run("node", filepath.Join(root, "next-build.cjs"), source, runtime, "standalone")
			require.NoError(t, err, "%s", out)
			require.FileExists(t, filepath.Join(runtime, relative, "server.js"))
			require.FileExists(t, filepath.Join(runtime, relative, "public/asset.txt"))
			require.DirExists(t, filepath.Join(runtime, relative, dist, "static"))
			data, err := os.ReadFile(filepath.Join(app, dist, "required-server-files.json"))
			require.NoError(t, err)
			var metadata struct {
				RelativeAppDir string `json:"relativeAppDir"`
			}
			require.NoError(t, json.Unmarshal(data, &metadata))
			require.Equal(t, relative, metadata.RelativeAppDir)
			t.Logf("real Next output %s standalone/%sserver.js", dist, strings.TrimSuffix(relative, "/")+map[bool]string{false: "", true: "/"}[nested])
			// A pre-existing staged output cannot be mistaken for this build, and is
			// preserved on refusal rather than recursively deleting admitted inputs.
			out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "stale-runtime"), "standalone")
			require.Error(t, err)
			require.Contains(t, out, "distDir overlaps staged input")
			require.FileExists(t, filepath.Join(app, dist, "required-server-files.json"))
			require.NoDirExists(t, filepath.Join(root, "stale-runtime"))
			require.NoError(t, os.RemoveAll(filepath.Join(app, dist))) // this test's own generated output
			manifest["scripts"] = map[string]string{"build": "node -e \"process.exit(0)\""}
			writeManifest()
			out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "missing-runtime"), "standalone")
			require.Error(t, err)
			require.Contains(t, out, "required-server-files.json")
			require.NoDirExists(t, filepath.Join(root, "missing-runtime"))
			if nested {
				buildInputWrite(t, app, "tamper-output.cjs", `const fs=require('fs');const p='.next-build/required-server-files.json';const m=JSON.parse(fs.readFileSync(p));m.relativeAppDir='wrong';fs.writeFileSync(p,JSON.stringify(m));`)
				manifest["scripts"] = map[string]string{"build": "next build --webpack && node tamper-output.cjs"}
				writeManifest()
				out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "mismatch-runtime"), "standalone")
				require.Error(t, err)
				require.Contains(t, out, "fresh Next output metadata disagrees")
				require.NoDirExists(t, filepath.Join(root, "mismatch-runtime"))
				require.NoError(t, os.RemoveAll(filepath.Join(app, dist)))
			}
			buildInputWrite(t, app, "next.config.mjs", `export default {};`)
			out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "nonstandalone-runtime"), "standalone")
			require.Error(t, err)
			require.Contains(t, out, "hosted Next output must be standalone")
			// Even a supported config cannot designate the application's source root
			// for cleanup. Its source remains intact after the refusal.
			buildInputWrite(t, app, "next.config.mjs", `export default {output:'standalone',distDir:'.'};`)
			out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "overlap-runtime"), "standalone")
			require.Error(t, err)
			require.FileExists(t, filepath.Join(app, "pages/index.js"))
			if !nested {
				buildInputWrite(t, app, "next.config.mjs", `export default {output:'export',experimental:{cpus:1}};`)
				manifest["scripts"] = map[string]string{"build": "next build --webpack"}
				writeManifest()
				out, err = run("node", filepath.Join(root, "next-build.cjs"), source, filepath.Join(root, "export-runtime"), "export")
				require.NoError(t, err, "%s", out)
				require.FileExists(t, filepath.Join(root, "export-runtime/index.html"))
			}

		})
	}
}

// A successful build script is not evidence that a pre-existing static export
// came from this build. Exercise the real Next config loader with a no-op script.
func TestNextExportRejectsStaleOutput(t *testing.T) {
	if os.Getenv("CODEFLY_TEST_NEXT_BUILD") != "1" {
		t.Skip("set CODEFLY_TEST_NEXT_BUILD=1 for real npm/Next config")
	}
	root := physicalPath(t.TempDir())
	app := filepath.Join(root, "source")
	buildInputWrite(t, app, "package.json", `{"name":"export-freshness-fixture","private":true,"scripts":{"build":"node -e \"require('fs').writeFileSync('build-invoked','yes')\""},"dependencies":{"next":"16.4.1","react":"19.2.4","react-dom":"19.2.4"}}`)
	helper, err := fs.ReadFile(builderFS, "templates/builder/context/next-build.cjs.tmpl")
	require.NoError(t, err)
	buildInputWrite(t, root, "next-build.cjs", string(helper))
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
		cmd.Dir = app
		cmd.Env = append(os.Environ(), "NODE_ENV=production", "NEXT_TELEMETRY_DISABLED=1", "npm_config_cache="+filepath.Join(root, "npm-cache"))
		data, err := cmd.CombinedOutput()
		return string(data), err
	}
	out, err := run("npm", "install", "--include=dev", "--no-audit", "--no-fund")
	require.NoError(t, err, "%s", out)
	for _, test := range []struct{ name, dist, export string }{{"default", ".next", "out"}, {"custom", ".export-build", ".export-build"}} {
		t.Run(test.name, func(t *testing.T) {
			t.Cleanup(func() { _ = os.Remove(filepath.Join(app, "build-invoked")) })
			buildInputWrite(t, app, "next.config.mjs", `export default {output:'export',distDir:'`+test.dist+`'};`)
			sentinel := "stale export must survive refusal"
			buildInputWrite(t, app, filepath.Join(test.export, "index.html"), sentinel)
			runtime := filepath.Join(root, "runtime-"+test.name)
			out, err := run("node", filepath.Join(root, "next-build.cjs"), app, runtime, "export")
			require.Error(t, err, "stale export accepted: %s", out)
			require.Contains(t, out, "overlaps staged input")
			data, err := os.ReadFile(filepath.Join(app, test.export, "index.html"))
			require.NoError(t, err)
			require.Equal(t, sentinel, string(data))
			require.NoDirExists(t, runtime)
			require.NoFileExists(t, filepath.Join(app, "build-invoked"))
			// After removing only this fixture's stale export, a successful no-op still
			// cannot publish output: the exact current export destination must exist.
			require.NoError(t, os.RemoveAll(filepath.Join(app, test.export)))
			out, err = run("node", filepath.Join(root, "next-build.cjs"), app, runtime, "export")
			require.Error(t, err)
			require.Contains(t, out, test.export)
			require.NoDirExists(t, runtime)
			require.FileExists(t, filepath.Join(app, "build-invoked"))
			require.NoError(t, os.Remove(filepath.Join(app, "build-invoked")))
		})
	}
}

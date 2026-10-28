package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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

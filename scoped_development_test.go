package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimev1 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v1"
	"github.com/codefly-dev/core/network"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

// Both runners receive the same physical application. Its own dev command,
// asynchronous config and edits must be visible while both scopes serve.
func TestOverlappingNextDevelopmentScopes(t *testing.T) {
	if os.Getenv("CODEFLY_TEST_RUNNER") == "" {
		t.Skip("set CODEFLY_TEST_RUNNER to start real Next.js")
	}
	for _, mode := range []string{"native", "nix"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			t.Setenv(resources.CodeflyHomeEnv, t.TempDir())
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			id, env := testIdentity(t, root)
			dir := filepath.Join(root, id.RelativeToWorkspace)
			writeProductionTestFile(t, dir, "service.codefly.yaml", "name: frontend\nversion: 0.0.0\nendpoints:\n  - {name: http, api: http}\n")
			shipped, err := readNodePackageManifest("base/code")
			require.NoError(t, err)
			deps := map[string]string{"greeting": "file:../greeting"}
			for _, name := range []string{"next", "react", "react-dom"} {
				deps[name] = shipped.Dependencies[name]
			}
			script := "next dev"
			if mode == "nix" {
				script += " --webpack"
			}
			manifest, err := json.Marshal(map[string]any{"name": "shared-scope", "private": true, "scripts": map[string]string{"dev": script}, "dependencies": deps})
			require.NoError(t, err)
			writeProductionTestFile(t, dir, "code/package.json", string(manifest))
			writeProductionTestFile(t, dir, "greeting/package.json", `{"name":"greeting","version":"1.0.0","main":"index.js"}`)
			writeProductionTestFile(t, dir, "greeting/index.js", "module.exports = 'from the linked package'")
			// Next lets the app explicitly select its own adapter. A platform
			// adapter supplied through the environment also remains effective.
			applicationAdapter := filepath.Join(dir, "code", "application-adapter.cjs")
			if mode == "nix" {
				writeProductionTestFile(t, dir, "code/application-adapter.cjs", `module.exports={name:'app',async modifyConfig(config, ctx) {
  config.basePath = '/application';
  return require(process.env.CODEFLY_NEXT_ADAPTER_PATH).modifyConfig(config, ctx);
}}`)
			} else {
				writeProductionTestFile(t, dir, "code/application-adapter.cjs", `module.exports={name:'app',modifyConfig(config) { return {...config, basePath:'/application'} }}`)
				t.Setenv("NEXT_ADAPTER_PATH", applicationAdapter)
			}
			config := `import path from 'node:path';
export default async () => ({distDir:'.custom-next', turbopack:{root:path.resolve('..')}`
			if mode == "nix" {
				config += `, adapterPath:path.resolve('application-adapter.cjs')`
			}
			writeProductionTestFile(t, dir, "code/next.config.mjs", config+"});")

			page := func(label string) {
				writeProductionTestFile(t, dir, "code/pages/index.js", "import greeting from 'greeting'; export default function Page(){return greeting + ': "+label+"'}")
			}
			page("first render")
			ws, err := resources.LoadWorkspaceFromDir(ctx, root)
			require.NoError(t, err)
			ports, err := network.NewRuntimeManager(ctx, nil)
			require.NoError(t, err)
			ports.WithTemporaryPorts()
			var urls []string
			for i := 0; i < 2; i++ {
				env.NamingScope = fmt.Sprintf("scope-%d", i)
				rt := NewRuntime(NewService())
				proto, err := env.Proto()
				require.NoError(t, err)
				loaded, err := rt.Load(ctx, &runtimev1.LoadRequest{Identity: id, Environment: proto, DisableCatch: true})
				require.NoError(t, err)
				require.Equal(t, runtimev1.LoadStatus_READY, loaded.GetStatus().GetState(), "%v", loaded)
				kind := resources.NewRuntimeContextNative()
				if mode == "nix" {
					kind = resources.NewRuntimeContextNix()
				}
				maps, err := ports.GenerateNetworkMappings(ctx, env, ws, rt.Identity, rt.Endpoints, kind)
				require.NoError(t, err)
				initialized, err := rt.Init(ctx, &runtimev1.InitRequest{RuntimeContext: kind, ProposedNetworkMappings: maps})
				require.NoError(t, err)
				require.Equal(t, runtimev1.InitStatus_READY, initialized.GetStatus().GetState(), "%v", initialized)
				t.Cleanup(func() {
					_, _ = rt.Stop(ctx, &runtimev1.StopRequest{})
					_, _ = rt.Destroy(ctx, &runtimev1.DestroyRequest{})
				})
				start, err := rt.Start(ctx, &runtimev1.StartRequest{})
				require.NoError(t, err)
				require.Equal(t, runtimev1.StartStatus_STARTED, start.GetStatus().GetState(), "%v", start)
				endpoint, err := resources.FindNetworkInstanceInNetworkMappings(ctx, maps, rt.HttpEndpoint, resources.NewNativeNetworkAccess())
				require.NoError(t, err)
				urls = append(urls, strings.TrimRight(endpoint.Address, "/")+"/application")
			}
			require.NotEqual(t, urls[0], urls[1])
			locks, err := filepath.Glob(filepath.Join(dir, "code", ".custom-next", "codefly", "*", "dev", "lock"))
			require.NoError(t, err)
			require.Len(t, locks, 2)
			client := &http.Client{Timeout: 5 * time.Second}
			assertPages := func(want string) {
				for _, url := range urls {
					require.Eventually(t, func() bool {
						res, err := client.Get(url)
						if err != nil {
							return false
						}
						defer res.Body.Close()
						body, err := io.ReadAll(res.Body)
						return err == nil && res.StatusCode == http.StatusOK && strings.Contains(string(body), want)
					}, 30*time.Second, 100*time.Millisecond)
				}
			}
			assertPages("from the linked package: first render")
			page("edited live")
			assertPages("from the linked package: edited live")
		})
	}
}

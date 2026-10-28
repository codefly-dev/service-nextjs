package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

func buildInputWrite(t *testing.T, root, name, data string) {
	t.Helper()
	target := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, []byte(data), 0644))
}
func buildInputGraph(t *testing.T) (string, string) {
	t.Helper()
	root := physicalPath(t.TempDir())
	app := filepath.Join(root, "parent", "services", "web")
	writeRecipePackage(t, app)
	writeRecipePackage(t, filepath.Join(root, "parent"))
	writeRecipePackage(t, filepath.Join(root, "remote"))
	buildInputWrite(t, app, "service.codefly.yaml", "name: web\n")
	buildInputWrite(t, app, "package.json", `{"name":"web","dependencies":{"parent":"file:../..","remote":"file:../../../remote","archive":"file:../../../remote/vendor/kit.tgz"}}`)
	buildInputWrite(t, root, "parent/package.json", `{"name":"parent","devDependencies":{"leaf":"file:../leaf"}}`)
	buildInputWrite(t, root, "leaf/package.json", `{"name":"leaf","version":"1.0.0","peerDependencies":{"react":"*"}}`)
	buildInputWrite(t, root, "remote/vendor/kit.tgz", "archive bytes")
	buildInputWrite(t, root, "remote/scripts/check.mjs", "source script")
	buildInputWrite(t, root, "unrelated/sentinel", "must not project")
	buildInputWrite(t, app, "node_modules/ambient/index.js", "host install")
	buildInputWrite(t, app, ".next/dev-sentinel", "host dev")
	buildInputWrite(t, app, ".next-build/required-server-files.json", `{"stale":true}`)
	buildInputWrite(t, app, ".next-build/standalone/server.js", "host production")
	buildInputWrite(t, app, ".dockerignore", "private\n!private/keep\n")
	buildInputWrite(t, app, "private/token", "excluded")
	buildInputWrite(t, app, "private/keep", "included")
	return root, app
}
func TestDeclaredBuildProjection(t *testing.T) {
	root, app := buildInputGraph(t)
	output := filepath.Join(root, "parent", "builder")
	buildInputWrite(t, output, "caller.txt", "preserved")
	inputs, relative, err := stageBuildInputs(t.Context(), app, app, output)
	require.NoError(t, err)
	require.Equal(t, "parent/services/web", relative)
	require.Equal(t, []string{filepath.Join(root, "parent"), filepath.Join(root, "remote"), app}, inputs.installs)
	for _, name := range []string{"parent/package.json", "parent/services/web/package.json", "remote/scripts/check.mjs", "remote/vendor/kit.tgz", "leaf/package.json", "parent/services/web/private/keep"} {
		require.FileExists(t, filepath.Join(output, "context", "src", name))
	}
	for _, name := range []string{"unrelated", "parent/builder", "parent/services/web/node_modules", "parent/services/web/.next", "parent/services/web/.next-build", "parent/services/web/private/token"} {
		require.NoFileExists(t, filepath.Join(output, "context", "src", name))
		require.NoDirExists(t, filepath.Join(output, "context", "src", name))
	}
	before, err := os.ReadFile(filepath.Join(app, "package.json"))
	require.NoError(t, err)
	_, _, err = stageBuildInputs(t.Context(), app, app, output)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(app, "package.json"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.FileExists(t, filepath.Join(output, "caller.txt"))
	buildInputWrite(t, output, "Dockerfile", "FROM scratch\nCOPY src /src\n")
	plan, err := services.BuildDockerBuildPlan(output, []*builderv0.DockerBuildRecipe{{Name: "web", Image: "example/web:1", Dockerfile: "Dockerfile", Context: "context", ContextRoot: builderv0.RecipeContextRoot_RECIPE_CONTEXT_ROOT_OUTPUT}})
	require.NoError(t, err)
	require.NoError(t, services.VerifyDockerBuildPlan(output, plan))
	buildInputWrite(t, output, "context/src/injected", "extra")
	require.Error(t, services.VerifyDockerBuildPlan(output, plan))
}
func TestDeclaredBuildProjectionRefusals(t *testing.T) {
	for _, scenario := range []string{"cycle", "missing", "absolute", "symlink", "app symlink", "source output", "ancestor output", "collision", "ignored manifest", "missing lock"} {
		t.Run(scenario, func(t *testing.T) {
			root, app := buildInputGraph(t)
			output := filepath.Join(root, "output")
			switch scenario {
			case "cycle":
				buildInputWrite(t, root, "leaf/package.json", `{"name":"leaf","dependencies":{"web":"file:../parent/services/web"}}`)
				writeRecipePackage(t, filepath.Join(root, "leaf"))
				buildInputWrite(t, root, "leaf/package.json", `{"name":"leaf","dependencies":{"web":"file:../parent/services/web"}}`)
			case "missing":
				require.NoError(t, os.Remove(filepath.Join(root, "remote/vendor/kit.tgz")))
			case "absolute":
				data, _ := json.Marshal(map[string]any{"dependencies": map[string]string{"leaf": "file:" + filepath.Join(root, "leaf")}})
				buildInputWrite(t, app, "package.json", string(data))
			case "symlink":
				require.NoError(t, os.Symlink(filepath.Join(root, "leaf/package.json"), filepath.Join(app, "escape")))
			case "app symlink":
				alias := filepath.Join(root, "parent", "services", "alias")
				require.NoError(t, os.Symlink(app, alias))
				app = alias
			case "source output":
				output = app
			case "ancestor output":
				output = root
			case "collision":
				buildInputWrite(t, output, "context/unowned", "preserve")
			case "ignored manifest":
				buildInputWrite(t, app, ".dockerignore", "package.json\n")
			case "missing lock":
				require.NoError(t, os.Remove(filepath.Join(root, "parent/package-lock.json")))
			}
			service := app
			if scenario == "app symlink" {
				service = filepath.Join(root, "parent", "services")
			}
			_, _, err := stageBuildInputs(t.Context(), app, service, output)
			require.Error(t, err)
			require.FileExists(t, filepath.Join(root, "parent/services/web/package.json"))
		})
	}
}

// Real consumer graph transport, opt-in only because the source fixture is
// supplied externally. It does not install into or modify that source.
func TestSelectedConsumerBuildProjection(t *testing.T) {
	root := os.Getenv("CODEFLY_NEXT_CONSUMER_SOURCE")
	if root == "" {
		t.Skip("requires exact selected consumer source")
	}
	for _, solution := range []string{"wiki", "proposals"} {
		t.Run(solution, func(t *testing.T) {
			app := filepath.Join(root, "solutions", solution, "services", "web")
			output := t.TempDir()
			inputs, relative, err := stageBuildInputs(t.Context(), app, app, output)
			require.NoError(t, err)
			require.Equal(t, "solutions/"+solution+"/services/web", relative)
			require.FileExists(t, filepath.Join(output, "context/src/packages/solution-remote/package.json"))
			require.NoDirExists(t, filepath.Join(output, "context/src/solutions", solution, "services/web/.next-build"))
			require.NoDirExists(t, filepath.Join(output, "context/src/solutions/wiki/services/backend"))
			require.Equal(t, app, inputs.installs[len(inputs.installs)-1])
			t.Logf("app %s installs %v", relative, inputs.installs)
		})
	}
}

func TestDeclaredNPMInstallOrder(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm unavailable")
	}
	root := physicalPath(t.TempDir())
	app := filepath.Join(root, "parent", "web")
	buildInputWrite(t, root, "leaf/package.json", `{"name":"leaf","version":"1.0.0","main":"index.js"}`)
	buildInputWrite(t, root, "leaf/index.js", `module.exports = 42`)
	buildInputWrite(t, root, "parent/package.json", `{"name":"parent","version":"1.0.0","main":"index.js","dependencies":{"leaf":"file:../leaf","kit":"file:../vendor/kit-1.0.0.tgz"}}`)
	buildInputWrite(t, root, "parent/index.js", `module.exports = require('leaf')`)
	buildInputWrite(t, root, "parent/.npmrc", "install-links=true\n")
	buildInputWrite(t, root, "kit/package.json", `{"name":"kit","version":"1.0.0","main":"index.js"}`)
	buildInputWrite(t, root, "kit/index.js", `module.exports = 7`)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "vendor"), 0755))
	buildInputWrite(t, app, "package.json", `{"name":"web","version":"1.0.0","dependencies":{"parent":"file:.."}}`)
	buildInputWrite(t, app, ".npmrc", "install-links=false\n")
	buildInputWrite(t, app, "service.codefly.yaml", "name: web\n")
	run := func(dir string, args ...string) {
		cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(root, "npm-cache"))
		data, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", data)
	}
	run(filepath.Join(root, "kit"), "npm", "pack", "--offline", "--pack-destination", filepath.Join(root, "vendor"))
	for _, dir := range []string{filepath.Join(root, "parent"), app} {
		run(dir, "npm", "install", "--package-lock-only", "--ignore-scripts", "--offline")
	}
	output := t.TempDir()
	inputs, _, err := stageBuildInputs(t.Context(), app, app, output)
	require.NoError(t, err)
	for _, dir := range inputs.installs {
		rel, _ := filepath.Rel(inputs.common, dir)
		run(filepath.Join(output, "context/src", rel), "npm", "ci", "--offline")
	}
	rel, _ := filepath.Rel(inputs.common, app)
	staged := filepath.Join(output, "context/src", rel)
	run(staged, "node", "-e", `if (require('parent') !== 42 || require('../node_modules/kit') !== 7) process.exit(1)`)
	require.NoDirExists(t, filepath.Join(app, "node_modules"))
	require.NoDirExists(t, filepath.Join(output, "context/src/kit"))
	info, err := os.Lstat(filepath.Join(output, "context/src/parent/node_modules/leaf"))
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink)
	// The parent is physically above the app, but the dependency edge is one-way.
	require.Equal(t, 2, len(inputs.installs))
	require.True(t, strings.HasSuffix(inputs.installs[0], "parent"))
}

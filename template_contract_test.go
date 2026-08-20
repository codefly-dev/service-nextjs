package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"text/template"
)

func TestFactoryTemplateUsesExplicitApplicationOwnedComposition(t *testing.T) {
	t.Parallel()

	packageData, err := fs.ReadFile(factoryFS, "templates/factory/code/package.json")
	if err != nil {
		t.Fatalf("read factory package.json: %v", err)
	}
	var manifest struct {
		Workspaces      []string          `json:"workspaces"`
		Scripts         map[string]string `json:"scripts"`
		Overrides       map[string]string `json:"overrides"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(packageData, &manifest); err != nil {
		t.Fatalf("parse factory package.json: %v", err)
	}
	if len(manifest.Workspaces) != 1 || manifest.Workspaces[0] != "packages/*" {
		t.Fatalf("workspaces = %v, want [packages/*]", manifest.Workspaces)
	}
	if manifest.Scripts["typecheck"] != "tsc --noEmit" {
		t.Fatalf("typecheck script = %q", manifest.Scripts["typecheck"])
	}
	if manifest.Scripts["lint"] != "eslint" {
		t.Fatalf("lint must remain read-only, got %q", manifest.Scripts["lint"])
	}
	if manifest.Scripts["fix"] != "biome check --write ." || manifest.Scripts["format"] != "biome format --write ." {
		t.Fatalf("safe fix scripts = fix:%q format:%q", manifest.Scripts["fix"], manifest.Scripts["format"])
	}
	if manifest.DevDependencies["@biomejs/biome"] != "^2.5.4" {
		t.Fatalf("Biome version = %q", manifest.DevDependencies["@biomejs/biome"])
	}
	if manifest.DevDependencies["babel-plugin-react-compiler"] != "1.0.0" {
		t.Fatalf("React Compiler version = %q", manifest.DevDependencies["babel-plugin-react-compiler"])
	}
	for name, version := range map[string]string{
		"next":      "16.2.12",
		"react":     "19.2.8",
		"react-dom": "19.2.8",
	} {
		if manifest.Dependencies[name] != version {
			t.Fatalf("%s version = %q, want %q", name, manifest.Dependencies[name], version)
		}
	}
	for _, unused := range []string{"next-themes", "shadcn"} {
		if _, ok := manifest.Dependencies[unused]; ok {
			t.Fatalf("unused dependency %q must not ship in the factory", unused)
		}
	}
	biomeConfig, err := fs.ReadFile(factoryFS, "templates/factory/code/biome.json")
	if err != nil || !strings.Contains(string(biomeConfig), "schemas/2.5.4/schema.json") {
		t.Fatalf("Biome config is missing or unpinned: err=%v content=%s", err, biomeConfig)
	}
	if manifest.Overrides["postcss"] != "8.5.19" {
		t.Fatalf("postcss override = %q", manifest.Overrides["postcss"])
	}
	if manifest.Overrides["sharp"] != "0.35.3" {
		t.Fatalf("sharp override = %q", manifest.Overrides["sharp"])
	}

	for _, legacy := range []string{
		"templates/factory/code/src/lib/framework/plugin.ts",
		"templates/factory/code/src/plugins/index.ts",
	} {
		if _, err := fs.Stat(factoryFS, legacy); err == nil {
			t.Fatalf("legacy plugin path still embedded: %s", legacy)
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat %s: %v", legacy, err)
		}
	}

	providers, err := fs.ReadFile(factoryFS, "templates/factory/code/src/lib/providers.tsx")
	if err != nil {
		t.Fatalf("read providers template: %v", err)
	}
	providerSource := string(providers)
	for _, forbidden := range []string{"@/plugins", "plugins.reduce", "self-register", "next-themes", "<script"} {
		if strings.Contains(providerSource, forbidden) {
			t.Fatalf("providers template contains legacy composition %q", forbidden)
		}
	}
	nextConfig, err := fs.ReadFile(factoryFS, "templates/factory/code/next.config.ts")
	if err != nil || !strings.Contains(string(nextConfig), "reactCompiler: true") {
		t.Fatalf("Next config must enable React Compiler: err=%v content=%s", err, nextConfig)
	}

	packageGuide, err := fs.ReadFile(factoryFS, "templates/factory/code/packages/README.md")
	if err != nil {
		t.Fatalf("read package guide: %v", err)
	}
	for _, required := range []string{"explicit", "application", "Do not add filesystem scanning"} {
		if !strings.Contains(string(packageGuide), required) {
			t.Fatalf("package guide does not document %q", required)
		}
	}
}

func TestBuilderTemplateInstallsWorkspaceGraphReproducibly(t *testing.T) {
	t.Parallel()

	dockerfile, err := fs.ReadFile(builderFS, "templates/builder/Dockerfile.tmpl")
	if err != nil {
		t.Fatalf("read Dockerfile template: %v", err)
	}
	source := string(dockerfile)
	for _, required := range []string{
		"FROM {{.NodeImage}} AS base",
		"COPY code/packages ./packages",
		"COPY service.codefly.yaml /service.codefly.yaml",
		"RUN npm ci",
		"RUN rm -rf node_modules",
		"RUN mkdir -p public && npm run build",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Dockerfile template missing %q", required)
		}
	}
	if strings.Index(source, "COPY code/packages ./packages") > strings.Index(source, "RUN npm ci") {
		t.Fatal("workspace packages must be copied before npm ci")
	}
	sourceCopy := strings.LastIndex(source, "COPY code/ .")
	cleanInstall := strings.Index(source, "RUN rm -rf node_modules")
	dependencyCopy := strings.LastIndex(source, "COPY --from=deps /app/node_modules ./node_modules")
	build := strings.Index(source, "RUN mkdir -p public && npm run build")
	if sourceCopy < 0 || !(sourceCopy < cleanInstall && cleanInstall < dependencyCopy && dependencyCopy < build) {
		t.Fatal("builder must replace host node_modules with the clean dependency layer before build")
	}
	if strings.Contains(source, "node:{{.NodeVersion}}") {
		t.Fatal("Dockerfile template must not use the floating Node major tag")
	}
	for _, productSpecific := range []string{"CODEFLY_SDK_JS_COMMIT", "/sdk-js"} {
		if strings.Contains(source, productSpecific) {
			t.Fatalf("generic Dockerfile template contains product-specific dependency %q", productSpecific)
		}
	}
	if !strings.Contains(NodeImage, "@sha256:") {
		t.Fatalf("NodeImage is not digest-pinned: %q", NodeImage)
	}
	rendered := &bytes.Buffer{}
	parsed, err := template.New("Dockerfile").Parse(source)
	if err != nil {
		t.Fatalf("parse Dockerfile template: %v", err)
	}
	if err := parsed.Execute(rendered, DockerTemplating{NodeImage: NodeImage}); err != nil {
		t.Fatalf("render Dockerfile template: %v", err)
	}
	if !strings.HasPrefix(rendered.String(), "FROM "+NodeImage+" AS base") {
		t.Fatal("rendered Dockerfile does not use the pinned Node image")
	}
}

func TestHealthProbePathIsScaffoldedAsARouteHandler(t *testing.T) {
	t.Parallel()

	deployment, err := fs.ReadFile(deploymentFS, "templates/deployment/kustomize/base/deployment.yaml.tmpl")
	if err != nil {
		t.Fatalf("read deployment template: %v", err)
	}
	const probePath = "/api/healthz"
	if !strings.Contains(string(deployment), "path: "+probePath) {
		t.Fatalf("deployment template no longer probes %s", probePath)
	}

	// App Router maps src/app/<segments>/route.ts to /<segments>, so the probe
	// path must resolve to a scaffolded route handler or a healthy server 404s.
	routeFile := "templates/factory/code/src/app" + strings.TrimSuffix(probePath, "/") + "/route.ts"
	route, err := fs.ReadFile(factoryFS, routeFile)
	if err != nil {
		t.Fatalf("probe path %s has no scaffolded route (%s): %v", probePath, routeFile, err)
	}
	if !strings.Contains(string(route), "export function GET()") {
		t.Fatalf("health route %s must export a GET handler, got:\n%s", routeFile, route)
	}
}

func TestDeploymentIdentityMatchesContainerIdentity(t *testing.T) {
	t.Parallel()

	deployment, err := fs.ReadFile(deploymentFS, "templates/deployment/kustomize/base/deployment.yaml.tmpl")
	if err != nil {
		t.Fatalf("read deployment template: %v", err)
	}
	source := string(deployment)
	for _, required := range []string{"runAsUser: 1001", "runAsGroup: 1001", "fsGroup: 1001"} {
		if !strings.Contains(source, required) {
			t.Fatalf("deployment template missing %q", required)
		}
	}
	if strings.Contains(source, "runAsUser: 1000\n") || strings.Contains(source, "runAsGroup: 1000\n") {
		t.Fatal("deployment identity drifted from the Dockerfile nextjs:nodejs user")
	}
}

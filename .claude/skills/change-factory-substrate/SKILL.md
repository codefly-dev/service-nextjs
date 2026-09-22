---
name: change-factory-substrate
description: Change the Next.js application this agent ships — the scaffold in templates/factory/code, the reference app in base/code, the Dockerfile in templates/builder, or the kustomize bundle in templates/deployment. Use when adding or upgrading a dependency, changing a package.json script, adding a route or config file to the generated app, or editing a Dockerfile stage, because one application-level change moves several artifacts that no single test ties together and the two copies of the app have already drifted apart once.
---

# Changing what this agent ships

This repo holds the generated application twice, deliberately and awkwardly:

- `templates/factory/code/**` is **embedded** (`//go:embed all:templates/factory`)
  and written into a new service by `Builder.Create`. This is what users get.
- `base/code/**` is **not embedded and referenced by no Go code**. It is the
  reference application consumed by codefly's generation mechanism, configured
  by `base/service.generation.codefly.yaml`.

Dependabot watches both (`.github/dependabot.yml` lists `/base/code` and
`/templates/factory/code` as npm directories). **Only the framework versions and
the overrides are compared** — by
`TestReferenceApplicationAndFactoryPinTheSameFrameworkVersions`. Everything else
has already drifted: `base/` carries a healthz route test and
`src/lib/connect/transport.ts`; `templates/factory/code` carries `biome.json` and
the biome scripts. Assume nothing else propagates on its own.

Both trees carry `package-lock.json`, and both are shipped state: the factory's
is embedded and written into every new service, so a scaffold is installable with
`npm ci` before anyone has run `npm install`. A manifest edit that does not
regenerate both lockfiles leaves the scaffold resolving a version it does not
declare.

## What one change touches

| You change | Also change | Caught by |
| --- | --- | --- |
| a dependency or script in the app | both `base/code/package.json` and `templates/factory/code/package.json`, then `npm install --package-lock-only` in each | `TestFactoryTemplateUsesExplicitApplicationOwnedComposition` — factory only |
| a framework version (`next`, `react`, `react-dom`, `eslint-config-next`) or an override | both manifests, both lockfiles, and the version named in `templates/agent/README.md.tmpl` | `TestFactoryShipsALockfileThatPinsTheDeclaredFrameworkVersions`, `TestReferenceApplicationAndFactoryPinTheSameFrameworkVersions`, `TestServedReadmeRecordsOnlyVersionsTheScaffoldPins` |
| a file in the generated app | both trees; `.tmpl` suffix only if it needs `{{ .Service.* }}` | nothing compares the trees |
| a scaffolded route | the contract test naming it, e.g. `TestHealthProbePathIsScaffoldedAsARouteHandler` | that test |
| `templates/builder/Dockerfile.tmpl` | the digest-pinned base image in `builder.go` if the Node major moves | `TestBuilderTemplateInstallsWorkspaceGraphReproducibly`, `TestBuilderTemplateRendersDeclaredBuildArgsBeforeBuild` |
| `templates/deployment/**` | identity must still match the container's | `TestDeploymentIdentityMatchesContainerIdentity`, `deployment_test.go`, and the manifest guard |
| a dotfile in the factory tree | nothing — but confirm `all:` still picks it up | `TestBuilderCreate` |

## Rules the templates encode

- **Workspace composition is explicit and application-owned.** The factory
  `package.json` declares its workspaces; the Dockerfile installs the whole
  graph with `npm ci` before building, so a build is reproducible from the
  lockfile the scaffold ships. A change that makes the install depend on network
  resolution at build time breaks the contract the test asserts.
- **Build args are rendered before `npm run build`**, not after. Next.js inlines
  public configuration at build time; an arg that arrives later is silently
  absent from the bundle.
- **Static mode is a different Dockerfile branch** (`{{if .Static}}`, nginx over
  `/app/out`) and `Builder.Create` overwrites `next.config.ts` with
  `output: "export"`. Changing one mode's scaffold does not change the other's.
- **Images stay pinned.** The build substrate is pinned by digest in
  `builder.go`; the runtime companion (`codeflydev/node`) is pinned in `main.go`
  and is built in core, not here. `Settings.RuntimeImage` rejects `:latest`.

## Verifying

`go test ./...` covers the contract tests above. Deployment changes additionally
need the manifest guard — see the `verify-changes` skill. A scaffold change is
only really proven by creating a service from it:
`CODEFLY_TEST_RUNNER=1 go test ./... -run '^TestCreateToRun$' -count=1`.

If you touched only one of the two trees on purpose, say so in the PR and say
why. Silent divergence is how the current drift arrived.

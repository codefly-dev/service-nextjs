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
| `templates/builder/Dockerfile.tmpl` | the digest-pinned base image in `builder.go` if the Node major moves | `TestBuilderTemplateInstallsWorkspaceGraphReproducibly`, `TestBuilderTemplateRendersDeclaredBuildArgsBeforeBuild`, `TestRenderedDockerfilePinsEveryExternalImageByDigest` |
| a `FROM` in that template | nothing else — but it must name a digest or an earlier stage, in **both** `{{if .Static}}` arms | `TestRenderedDockerfilePinsEveryExternalImageByDigest` renders both arms; `TestBuilderTemplateRecordsTheResolvedOSPackageSetInBothRunners` checks each carries the apk record |
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
- **Images stay pinned, and every pin has a named refresh path.** Three pins,
  three different owners:

  | Pin | Lives in | Refreshed by |
  | --- | --- | --- |
  | static runner, `nginx:<ver>-alpine@sha256:` | `templates/builder/Dockerfile.tmpl` | Dependabot's `docker` group — `.github/dependabot.yml` already lists `/templates/builder`, and the fetcher's filename pattern matches `Dockerfile.tmpl`, so it rewrites tag and digest together |
  | build substrate, `NodeImage` | `builder.go` (a Go const) | **nobody — by hand.** Dependabot's docker ecosystem cannot see a Go constant, and `FROM {{.NodeImage}}` has no literal tag for it to parse |
  | runtime companion, `codeflydev/node` | `main.go` | built and released in core, bumped here when core releases one |

  `Settings.RuntimeImage` rejects `:latest`. Pin digests to the **multi-arch
  index**, not a single platform: the recipe declares `linux/amd64` and
  `linux/arm64`, and a per-platform digest fails the build on the other arch.
  Resolve one with
  `docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'`.
- **The apk layer is recorded, not pinned.** `libc6-compat` is a virtual name
  that resolves to gcompat plus its dependencies, and Alpine's package index
  keeps only the current build of each, so `=<version>` fails the build instead
  of aging. The deps stage writes `apk info -v | sort` to
  `/codefly/apk-packages.txt` and both runner arms copy it, so the OS packages
  behind a pushed image stay readable from that image.

## Verifying

`go test ./...` covers the contract tests above. Deployment changes additionally
need the manifest guard — see the `verify-changes` skill. A scaffold change is
only really proven by creating a service from it:
`CODEFLY_TEST_RUNNER=1 go test ./... -run '^TestCreateToRun$' -count=1`.

If you touched only one of the two trees on purpose, say so in the PR and say
why. Silent divergence is how the current drift arrived.

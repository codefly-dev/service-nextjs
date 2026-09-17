# Working in codefly-dev/service-nextjs

`github.com/codefly-dev/service-nextjs` (Go 1.27.0) is the codefly **service
agent** for Next.js and Node frontends. `main()` registers one service as Agent,
Builder, Runtime, Code and Tooling with `agents.Serve`, and GoReleaser ships it
as the binary core downloads. It owns what is Next.js-specific: the scaffolded
application (`templates/factory`), the Docker build *recipe* (`templates/builder`),
the kustomize bundle (`templates/deployment`), the development/production run
modes, vitest and Playwright execution, and effective-input discovery.

It does **not** own: the gRPC contracts and plugin server, the resource model,
runner environments, port and endpoint resolution, the generic TypeScript code
server, npm audit/SBOM/upgrade, or proto→TypeScript generation. Those are
`codefly-dev/core`, the single first-party dependency. Executing a Docker build
is the CLI's (`codefly-dev/cli`) — `Builder.Build` emits a plan and refuses an
empty `output_directory`. When the change you need belongs to one of them, it
gets made there.

## How to behave

Fleet standard — [handbook#68](https://github.com/obin-ai/handbook/issues/68).

- **A gap in the tooling is a bug in the tooling — never a reason to reach
  around it.** This agent hands the CLI a build plan precisely because
  execution is not its job. When a run needs a step `codefly` cannot express,
  the answer is a capability fixed in the tool that owns it and named in the
  PR — never a hand-run `docker build`, a hand-started `next`, or a
  hand-assembled environment. Not as a "workaround", not "just this once", not
  "until the capability lands".
- **Never hack. Provide the best fix, even when it spans repos.** The fix
  living in `codefly-dev/core` or `codefly-dev/cli` is not a reason to absorb
  it here. Open the PR there and consume the reviewed result. When it genuinely
  cannot be fixed now, the deliverable is a precise issue against that owner
  plus an explicitly labelled stopgap — never an unlabelled one.
- **Classify every change that makes something work**, in the PR body: a *fix*
  at the place that owns the behaviour, or a *hack*. A hack does not become a
  fix by working, by being small, by being local, or by the real fix belonging
  elsewhere.
- **Never hardcode what the system resolves.** Ports, endpoints and injected
  environment come from core's network and configuration flow; the source
  directory comes from `Settings.SourceDir`; images are pinned constants.
  `Settings.RuntimeImage` rejects `:latest` on purpose — pinning is enforced,
  so do not reach around it. A value you type is true on one machine for ten
  minutes, and it fails quietly: a runtime missing a credential can skip
  registration *silently*, so a service boots, serves, and is simply absent.
- **Diagnose, do not pattern-match.** "It started working when I set X" is not
  a diagnosis — set X back and confirm it breaks. Do not trust an error message
  before checking its claim. Be equally suspicious of a *pass*: several tests
  here skip themselves when their prerequisite is absent (below), so green can
  mean "never ran".
- **Say what you did not verify.** Unverified is not the same as working.
  `go test ./...` exercises neither tagged suite nor the manifest guard, and it
  drops a lifecycle arm whose backend is missing from the host without failing.
  If you could not run something, the PR says so.

## Build and test

Derived from `.github/workflows/ci.yml`, which delegates to the reusable
`codefly-dev/core/.github/workflows/go-service-ci.yml@main`. CI reads the Go
version from `go.mod` (1.27.0) and caches nothing.

```bash
go build ./...          # CI: build
go vet ./...            # CI: vet
go mod tidy -diff       # CI: fails if go.mod/go.sum are untidy
go test ./...           # CI: the untagged suite (`-v`), also the release gate
```

CI runs three more jobs that the untagged suite does **not** cover. All are
runnable locally and none is optional before touching what it guards:

| Command | Guards | Needs |
| --- | --- | --- |
| `go test -tags ciinputs_conformance -run TestEffectiveInputsNative -count=1 -timeout 600s ./...` | native discovery against real Next.js/vitest containers | Docker, and `node:22-alpine` **already pulled** — discovery resolves an installed tag and never pulls |
| `go test -tags proto_companion_required -run '^TestSyncConformance' -count=1 -v .` | `Builder.Sync` against the real proto companion | Docker, the companion image |
| the `manifest-guard` workflow (`codefly-dev/.github`) | renders the Kubernetes bundle twice, byte-compares, and scans the tree for Git/GitHub/Argo ownership | its render entry point is `TestManifestGuardRender` |

**A skip is not a pass.** `TestManifestGuardRender` skips unless
`CODEFLY_MANIFEST_DESTINATION` is set; `TestCreateToRun` skips unless
`CODEFLY_TEST_RUNNER=1`; `TestNextjsLifecycle_Matrix` needs Docker and Nix.
A green `go test ./...` is silent about all three.

**Never mock.** Tests drive a real gRPC loopback, the real embed FS, and real
containers. If a boundary is hard to reach, reach it anyway.

## Where things live

| Path | Owns |
| --- | --- |
| `main.go` | `Settings`, the advertisement, plugin registration, pinned runtime image |
| `builder.go` | Load/Init/Sync/Build-recipe/Audit/SBOM/Deploy/Create, and the template embeds |
| `runtime.go` | run modes (native/nix/docker), readiness probing, Test and Lint |
| `code.go`, `tooling.go` | Node-specific source fixing and `package.json` reading; the Tooling facade |
| `effective_inputs.go`, `discovery/` | `Agent.GetEffectiveInputs` and the sandboxed native inspector |
| `templates/` | `factory` (scaffold), `builder` (Dockerfile), `deployment` (kustomize), `agent` (served README) |
| `base/` | the reference application `templates/factory/code` mirrors; not embedded |

The discovery contract — sandbox guarantees, budgets, and every incompleteness
marker — is specified in [`discovery/README.md`](discovery/README.md). Read it
rather than re-deriving it from `effective_inputs.go`.

## Rules that bite

- **This agent never runs a container build.** `Builder.Build` renders the
  recipe and a `DockerBuildPlan` into the caller's `output_directory`; buildx
  selection is advertised back to the CLI.
- **The Kubernetes side must stay manifest-only.** Git, GitHub, pull-request,
  Argo/Flux and repository/revision concepts are rejected by the ownership scan
  in any `.go`/`.sh`/`.yaml`/`.ts`/`Dockerfile` outside tests and `.github/`.
  Deployment renders must be byte-identical across two runs.
- **`base/` and `templates/factory/code` are two copies of one application.**
  An application-level change lands in both, or the next scaffold diverges from
  the substrate Dependabot is upgrading.
- **Images are pinned deliberately** — `codeflydev/node` in `main.go`, the
  build substrate by digest in `builder.go`. Bump them, never float them.

## Procedures

Step-by-step procedures live in `.claude/skills/`, loaded on demand rather than
carried here:

- `verify-changes` — running the gates CI runs, including the three the plain
  suite hides, before pushing.
- `change-factory-substrate` — one application-level change moves four
  artifacts; which contract test catches which half.
- `release-agent` — cutting a version, and why the tag is never made by hand.

## Workflow

- Branch and PR; never commit to `main`. Conventional Commits for the title.
- `agent_context_test.go` holds the meta-invariants — this file's length budget,
  the `CLAUDE.md` pointer, and each skill's frontmatter contract. It runs under
  `go test ./...`, so CI enforces it with no workflow change.
- Keep this file under ~150 lines (hard cap 200). Push depth into a nested
  `AGENTS.md` beside what it describes, or into `.claude/skills/`.
- `CLAUDE.md` is a pointer to this file. Keep one canonical source.
- Treat this file as code: the PR that changes a process updates it.

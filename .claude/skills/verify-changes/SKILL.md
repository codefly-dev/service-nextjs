---
name: verify-changes
description: Run the gates CI runs for codefly-dev/service-nextjs before pushing, including the three that a plain `go test ./...` hides — the ciinputs_conformance and proto_companion_required suites and the Kubernetes manifest guard. Use when finishing a change here, when deciding what "tested" means for the surface you touched, when CI is red on a job you cannot reproduce locally, or when a test passed and you need to know whether it actually ran rather than skipped itself for a missing Docker image or environment variable.
---

# Verifying a change to this agent

CI is `.github/workflows/ci.yml`. It delegates most of itself to
`codefly-dev/core/.github/workflows/go-service-ci.yml@v0.19.4` and
`codefly-dev/.github/.github/workflows/plugin-manifest-guard.yml@main`, so the
commands are not visible in this repo. They are below. Run the ones that cover
what you touched, and say in the PR which you did not run.

## Always

```bash
go build ./...
go vet ./...
go mod tidy -diff     # fails, rather than rewriting, when go.mod/go.sum drift
go test ./...
```

`go mod tidy -diff` is the one people miss: a dependency added by hand passes
build and test, then fails CI.

## The three gates the plain suite does not cover

### Native effective-input discovery

Covers `effective_inputs.go` and `discovery/inputs.cjs`.

```bash
docker pull node:22-alpine
go test -tags ciinputs_conformance -run TestEffectiveInputsNative -count=1 -timeout 600s ./...
```

The pull is not optional and not a convenience: discovery resolves an
**already-installed** tag to its local image ID and never pulls. Without the
image present the inspection cannot run, and the observation comes back marked
incomplete rather than failing loudly. CI pins the image by digest and retags it
for exactly this reason.

### Sync against the real proto companion

Covers `Builder.Sync` and anything touching `code/src/gen` ownership.

```bash
go test -tags proto_companion_required -run '^TestSyncConformance' -count=1 -v .
```

Needs Docker and pulls the companion image. The untagged
`TestProtoCompanionMatchesConnectESToolset` in `sync_drift_test.go` only checks
the companion's version floor — it proves nothing about generation.

### The Kubernetes manifest guard

Covers `Builder.Deploy` and everything under `templates/deployment/`.

The workflow renders the bundle twice into separate trees, requires them to be
byte-identical, and scans the source and both renders for ownership concepts a
manifest-only plugin may not have — Git, GitHub, pull requests, Argo CD/Flux,
repository or revision binding. Its render entry point here is
`TestManifestGuardRender`, which **skips silently** during an ordinary run. To
drive it locally the way the workflow does:

```bash
out=$(mktemp -d)
CODEFLY_MANIFEST_DESTINATION="$out/run-1" \
CODEFLY_MANIFEST_ENVIRONMENT=guard \
CODEFLY_MANIFEST_NAMESPACE=guard \
CODEFLY_MANIFEST_PROFILE=KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1 \
  go test ./... -run '^TestManifestGuardRender$' -count=1
```

Render a second time into `run-2` and `diff -r` the trees to reproduce the
determinism check. Non-determinism usually means a name or annotation derived
from something that varies — a timestamp, a map iteration, a temp path.

The ownership scan reads `.go`, `.sh`, `.yaml`, `.ts`, `Dockerfile` and similar,
skipping `.github/`, `*_test.*` and `testdata/`. Markdown is not scanned, so
prose may discuss Git freely; a shipped script or template may not. A violation
is a real design finding — a manifest producer that knows about a repository is
the thing the guard exists to prevent. Do **not** reach for `exclude-paths`: it
accepts only release-automation and fixture paths, and using it to hide runtime
source is the hack this repo's rules forbid.

## A skip is not a pass

### Actual native agent ownership

Core v0.19.10 supplies the authenticated-v2 native writer and identity-bound
stop/recovery APIs. This agent consumes them through its existing native runner;
already-installed binaries retain the Core they were built with.

After changing Core or the native lifecycle, build this checkout through
`codefly agent build --native-only` with a disposable OS `HOME` and
`CODEFLY_HOME`. Select the intended Core graph with `agent deps --pin` for a
published version; a source-adjacent `go.work` proves only that local source
graph and requires a packager that honours the CLI's prepared workfile.
Use an isolated source packager installation, never overwrite a shared agent.
Then qualify those exact installed bytes:

```bash
SERVICE_NEXTJS_NATIVE_AGENT_HOME=/absolute/disposable/codefly-home \
SERVICE_NEXTJS_QUALIFICATION_CLI=/absolute/reviewed/codefly \
  go test -run '^TestNativeAgentOwnershipLifecycle$' -count=1 -v .
```

This opt-in gate loads the installed agent through Core's authenticated manager
and drives real Next.js applications through gRPC Load/Init/Start/Stop/restart.
It checks that Start's RPC lifetime does not own the workload, ordinary Stop
removes the registered group, and the actual writer records authenticated-v2
ownership. It kills only the exact disposable agent owner, recovers its orphan
through CLI preview/expect, and verifies an independently launched sibling's
process births remain unchanged. Core owns authentication, process inspection,
signalling and registry removal; this agent adds no recovery mechanism.
The test retains the manager's authenticated original launch identity before
workload actions, compares the current full identity before signaling, and binds
the later recovery receipt to that retained birth. Its stale-owner regression
uses a real disposable process and requires refusal without SIGKILL delivery.

The test isolates OS HOME (Core's registry) from the supplied installation home.
It needs npm, Node, network access for the fixture's pinned dependencies, and a
CLI implementing the reviewed recovery contract. It bypasses sandbox/principal
policy explicitly. It does not qualify Linux when run on macOS, Docker, Nix,
full CLI composition, or a published release. Record binary/SBOM/CLI hashes and
embedded Core metadata with the result; a local manifest version is not proof
of publication. Missing opt-in inputs produce a skip, never qualification.
The ownership proof covers the tracked npm process group. Next.js 16.4.1 can
briefly start a detached telemetry flush helper even with telemetry disabled;
that helper is outside this group contract. Fixture-home deletion retries handle
its filesystem race without claiming or signalling an unrelated process.

These exclude themselves and report success:

| Test | Runs only when |
| --- | --- |
| `TestManifestGuardRender` | `CODEFLY_MANIFEST_DESTINATION` is set |
| `TestCreateToRun` (`main_test.go`) | `CODEFLY_TEST_RUNNER=1` |
| each `TestNextjsLifecycle_Matrix` arm | that backend exists on the host — core's matrix calls `t.Skip` for a missing one, so a host without Docker or Nix still reports the test as passed |
| symlink and ESLint fixtures | on a POSIX shell with symlink support |

`go test ./... -v | grep -c SKIP` before claiming a surface is covered.

## Reporting

The PR body says which gates ran. "Unverified" is an acceptable answer and a
required one when the environment could not run a suite; a green untagged run
described as "tests pass" is not.

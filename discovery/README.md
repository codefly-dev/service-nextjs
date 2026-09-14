# Effective-input discovery

The agent advertises Core v1 `Agent.GetEffectiveInputs`. Discovery resolves the
service declaration from `CODEFLY_AGENT_WORKDIR` before runtime loading, without
changing the runtime's cached settings or identity. Validation capabilities remain
the authority for the full phase/suite inventory. Service dependencies are read
from that declaration for each current-worktree request; their Core dependency
kinds distinguish runtime requirements from build prerequisites.

Native inspection requires Docker and the configured runtime image already
installed. The image is resolved to its local immutable image ID. Discovery does
not pull images, run host Node, forward the host environment, or initialize the
application runtime. It runs in a disposable container with a read-only workspace
and root filesystem, no network, no capabilities, a bounded temporary filesystem,
and CPU, process and memory limits. The container is removed on cancellation as
well as completion. No Docker socket or host home directory is mounted.

Next.js production, TypeScript compiler and named Vitest suite observations remain
separate. Configuration for each task runs in a separate container to avoid one
framework's environment loading affecting another. Supported inspection includes
`next build`, `tsc --noEmit`, and bare `vitest`/`vitest run` suite scripts without
pre-hooks. Compiler reads include its parsed configuration and program. Next.js
route discovery and its bundled tracer retain production imports with test-like
names; the tracer receives JSX/TypeScript transformed by TypeScript. Vitest uses
its native test specification collection with its configuration loader and cache
configured for the read-only inspection environment. Unsupported scripts and
failed task inspections do not remove required tasks. A task whose inspection
failed, ran out of budget, or exceeded the path budget declares
`discovery/native-observation-incomplete`, so an observation that saw nothing is
never mistaken for a clean one. When inspection cannot run at all the task
declares `discovery/isolated-native-discovery-unavailable`, and when a container
cannot be confirmed removed it declares
`discovery/native-discovery-cleanup-unverified`. Container cleanup is an
operational fault of the host and never fails the call.

Native paths supplement only their corresponding task. Known package metadata
and lockfiles are shared; the service tree and installed dependencies are never
walked or hashed wholesale. All observed filesystem entries, including symlinks
and consumed in-workspace file targets, are sensitive and unresolved by default.
Only matching caller-resolved context carries an identity and its sensitivity
classification. These are caller-supplied snapshot identities, not new content
attestations by the agent. The agent does not generate hashes of potentially
secret content or use filename rules to decide what is safe to hash.

Native output is limited to 128 KiB and 512 paths per task. Past the path budget
the inspector reports a bounded prefix and marks the observation partial; it does
not discard the observation, because an ordinary application exceeds the budget
and losing the list entirely is indistinguishable from having observed nothing.
One task inspection is given 60 seconds and the whole request 8 minutes, sized so
a real application is inspected rather than only a fixture.

The response is limited to 512 KiB. An oversized declaration drops observations
from the largest tasks first, only as far as needed, and retains the task
inventory and each task's runtime services; a truncated task declares
`discovery/declaration-truncated-for-transport`. Runtime services state which
services a suite must have started, and no size condition makes that untrue.

A request larger than the transport bound, and one naming a source state other
than the current worktree, both answer with the full inventory and no
observations, leaving every task conservative. Neither returns an error: Core
degrades only on `Unimplemented`, so any other status fails the caller's whole
discovery rather than that one observation.

Native framework observations do not establish dynamic consumption, effective
execution configuration, transitive dependencies or generation/artifact closure.
Those tasks remain incomplete and cannot enable reuse or production exclusion. A
stack-starting suite additionally declares
`discovery/runtime-service-closure-unresolved`, because this agent reads only its
own service declaration and its runtime-service list is therefore a subset of the
stack. Missing images, image references that are not image references, and
nonreproducible execution environments leave inspection unresolved.

The ordinary RPC tests cover headless Core discovery, dependency kinds, protected
context, symlink targets, request validation, a 4,000-file tree with an 8 GiB
sparse asset, a repository-sized resolved context, dependencies that resolve to
one owner, resolved context whose file mode disagrees with the worktree, and a
declaration too large for the transport. Native conformance uses real containerized Next.js and Vitest, builds
the production artifact, checks task separation and verifies that hostile
configuration cannot modify source or inherit the host environment. CI runs it:

```
go test -tags ciinputs_conformance -run TestEffectiveInputsNative -count=1 -timeout 300s ./...
```

SBOM has a separate complete declaration because its implementation reads only
`package-lock.json` and the include-dev option, without invoking project code.
It consumes the service configuration that selects the source directory, the
lockfile, and a caller context input with kind CONFIGURATION, service owner,
name `sbom/options`, and VERSIONED identity namespace
`codefly.nextjs.sbom-options/v1`, digest `include-dev=false` or `include-dev=true`.
Core's verified `CODEFLY_PROVIDER_ARTIFACT_DIGEST` supplies the agent implementation
identity; that artifact plus the platform identifies the in-process Go toolchain.
Reuse requires resolved identities for every input. Unresolved file identities
and a missing provider identity prevent reuse. Completeness also requires that
discovery inspected the same tree `Builder.SBOM` reads; the source directory
itself is not compared, because the declaration that selects it is already a
declared input, so changing it changes that input's identity. Symlinked SBOM
inputs remain incomplete. No secret values or
unkeyed hashes of project files are produced by discovery.

The tests exercise complete SBOM discovery through Core's real Agent client and
compare actual Builder.SBOM outputs before and after source/lockfile changes.
Full native completeness for the other phases and an immutable agent publication
with evidence for codefly-dev/core#445 are still required for all #114 acceptance
cases.

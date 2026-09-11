# Effective-input discovery status

The agent implements Core v1 `Agent.GetEffectiveInputs` directly on `Service`.
The validation advertisement remains the authority for phases and named suites.
Discovery never starts services, runs builds/tests, installs dependencies, or
uses a previous `.next` trace. It does load project configuration/plugins, which
can execute arbitrary project code. Native discovery has a 30-second deadline;
its stdout/stderr are discarded because configuration diagnostics can contain
secrets.

For the current worktree, discovery records source-tree content and modes,
lockfiles, symlink text and in-workspace targets. Next.js loads production-build
configuration, discovers page directories/extensions, and uses its bundled NFT
tracer. TypeScript reads the configured compiler program. Bare `vitest` and
`vitest run` scripts use Vitest's native configured test specification discovery.
Wrapper commands, script flags and other runners are unsupported. Discovered
paths supplement the conservative source-tree inventory; they never justify
excluding other files. In particular, NFT alone does not cover JSX compilation,
webpack/Turbopack plugins, configuration-time reads or dynamic fixture reads.

Every declaration is currently incomplete. Explicit unresolved entries identify
missing execution toolchain/agent resolution, ambient environment, generators,
dynamic/external consumption, invocation selectors, image context/recipe and
prerequisites. Caller-resolved context is retained, with service implementation
identities restricted to suites that start services. Direct runtime requirements
preserve `unit`, `pure`, `integration`, `e2e`, and `smoke` semantics; discovery does
not claim to resolve the transitive runtime graph. Secret-bearing environment
and service configuration files have unresolved identities. No protection key is
invented and no raw execution environment is serialized.

Historical revisions return the full task inventory with empty, incomplete
inputs: installed dependencies, configuration and the loaded service graph have
not been reproduced at that revision. External symlink targets, broken links,
submodules, attached source trees, and arbitrary shared/external inputs are not
claims of complete coverage. Reference and candidate requests are never combined
or answered from a previous request's trace.

`effective_inputs_versions` remains unadvertised until complete native discovery
and the production-exclusion acceptance cases are implemented. Existing clients
retain legacy conservative behavior. Direct RPC callers can inspect the partial
declarations, but Core must select every task and disable reuse.

Run the RPC tests with `go test ./...`. The native conformance fixture installs
pinned Next.js and Vitest dependencies and runs a real Vitest suite:

```
go test -tags ciinputs_conformance -run TestEffectiveInputsNative -count=1 -timeout 300s ./...
```

This is partial adoption of #114, not completion of core#445. Remaining acceptance
work includes trustworthy complete production/test discovery, task-specific
fixture/configuration/selector coverage, dependency and generated-input closure,
reproducible historical discovery, protected execution identities, and an
immutable agent release through #113. No immutable release has been published
for this implementation.

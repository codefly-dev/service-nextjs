---
name: release-agent
description: Cut a release of the service-nextjs agent binary, or work out why a published version is not what codefly downloads. Use when bumping the version in agent.codefly.yaml, pinning a new codefly-dev/core release, tagging, or diagnosing a GoReleaser run — the version, the tag and the archive filename form a contract with core's downloader, and the release build needs CGO and a cross toolchain that an ordinary `go build` does not.
---

# Releasing this agent

A release is a **tag on `main`**, and `codefly publish` is what makes it.
`.github/workflows/releaser.yml` fires on `v*`, runs `go test -v ./...`, then
runs GoReleaser inside `ghcr.io/goreleaser/goreleaser-cross`. The tag is the
trigger; nothing dispatches that workflow.

## The contract

Three things must agree, and only the first two are visible in the diff:

1. `agent.codefly.yaml` → `version: 0.0.N`.
2. The tag → `v0.0.N`, annotated, on the commit that carries that version.
3. The archive → `service-nextjs_{version}_{os}_{arch}.tar.gz` containing a
   binary named `service-nextjs`. This is `name_template` in `.goreleaser.yaml`
   and it matches core's downloader (`agents/manager/downloader_url.go`).
   Changing it makes every published version unreachable, silently — core
   fails to find the download, not to parse it.

## The walk

```bash
# from a clean, up-to-date clone of main
codefly publish patch            # or minor / major; --dry-run prints the version
```

`publish` is the whole release, not half of it: it bumps `agent.codefly.yaml`,
lands that bump on main through a release pull request, runs release-grade
agent CI against the bumped version, then tags the commit main ends up
carrying, pushes that tag, and creates the GitHub release with the loader
archives and SBOMs. Pre-flight aborts cleanly, with no side effects, when the
tree is dirty, the branch is not `main`, it is behind origin, or the tag
already exists.

**Never `git tag` by hand.** A hand tag skips every gate above — that is how a
release shipped from a commit whose CI had never run. If the release pull
request merges but the tag push then fails, re-run `publish`: it recognizes the
untagged release commit and finishes it rather than bumping again. Never start
a second `publish` while one is still waiting, or the re-run opens the NEXT
bump instead of tagging the one on main.

Never move a published tag: the downloader resolves a version to an artifact,
so re-pointing a tag changes what an already-pinned consumer gets.

Pin core in its own PR before releasing — the version bump is `publish`'s to
write, not yours:

```bash
go get github.com/codefly-dev/core@vX.Y.Z && go mod tidy
go build ./... && go vet ./... && go mod tidy -diff && go test ./...   # see verify-changes
```

## Why the release build is not `go build`

CGO is mandatory. Core's authoritative source inspection uses real tree-sitter
grammars, so every target in `.goreleaser.yaml` sets `CGO_ENABLED=1` with a
per-platform cross compiler (`o64-clang`, `aarch64-linux-gnu-gcc`, …). That is
what `goreleaser-cross` supplies and what a plain runner does not.

Disabling CGO to make a build succeed does not produce a smaller release — it
produces an agent whose parser cannot work in production. If a release build
fails on the toolchain, fix the toolchain or the image pin; do not remove the
requirement.

## When a release looks wrong

- **Core downloads an old version.** Compare the three items above before
  suspecting core. A version bumped in `agent.codefly.yaml` but never tagged
  publishes nothing, and leaves no failure anywhere.
- **The version bumped but no tag exists.** Re-run `codefly publish`; it
  finishes the untagged release commit. Do not tag it by hand, and do not bump
  again.
- **The release job fails in `test`.** The release gate is untagged
  `go test -v ./...` — the same suite as CI, so a failure here is a real
  regression that the merge should not have carried.
- **A consumer breaks after a core bump.** The pin is a claim that this agent
  was verified against that core. If you did not run the conformance suites
  against it, say so in the PR rather than implying the pin was proven.

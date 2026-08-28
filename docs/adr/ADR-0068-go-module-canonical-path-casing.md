# ADR-0068: Go Module Canonical Path Casing

## Status

Accepted

## Context

ADR-0034 decided the Go module path `github.com/AkiraShimizu0/workcairn/go` (all lowercase) as part of the WorkCairn rename. At that time the GitHub repository itself had not yet been renamed. PHASE PB-2.1 completed the actual GitHub repository rename to the mixed-case slug `WorkCairn` (`origin` is now `git@github.com:AkiraShimizu0/WorkCairn.git`), leaving the Go module path's casing (`workcairn`, lowercase) inconsistent with the repository's own canonical path (`WorkCairn`, mixed case).

This is not a branding question — it is a Go module identity question. Go module paths are literal, case-sensitive strings; `github.com/AkiraShimizu0/workcairn/go` and `github.com/AkiraShimizu0/WorkCairn/go` are two different module identities as far as the Go toolchain and any module proxy are concerned, even though GitHub's own git-over-HTTP routing treats the two URLs as the same repository.

**Investigated before changing, not guessed**: a repository-wide inventory found the lowercase module path referenced in exactly 252 tracked files — `go.mod` (1), all Go source (`245` files, `821` import-statement occurrences), `Makefile` and the two release scripts (`-X module.Var=value` ldflags, 3 occurrences), and three documentation files (`docs/ProductNaming.md`, `docs/PublicReleaseChecklist.md`, `docs/adr/ADR-0034-...md`). No JSON Contract fixture, Vault persistence identifier, or `workspace-*` protocol identifier references the Go module path at all — those are separate, unrelated identifier families (confirmed by the same inventory: zero `.json` fixture hits).

**No technical blocker found.** Go module paths support mixed-case segments natively (the toolchain's module cache uses a well-documented `!`-escape convention purely for on-disk cache directory naming — this does not affect `go build`, `go vet`, `go test`, or any part of this repository's own build). `go.sum` only records checksums for this module's *dependencies* (`golang.org/x/text`), never for the module's own path, so a change to the module's own identity cannot produce a `go.sum` diff. This repository is distributed via `git clone` + `make go-build`, not `go get`/module-proxy consumption (confirmed in `README.md`'s own install instructions), so there is no live network-resolution risk to verify before this Checkpoint's own build/test gates already prove correctness locally.

## Decision

Align the Go module's own path casing to the GitHub repository's canonical casing: `module github.com/AkiraShimizu0/WorkCairn/go`. Updated, mechanically and exhaustively, every internal import statement (245 Go files, 821 occurrences) and every ldflags `-X` package-qualified reference (`Makefile`, `scripts/check-release-matrix.sh`, `scripts/package-release.sh`) to match. Updated `docs/ProductNaming.md` and `docs/PublicReleaseChecklist.md` (living reference docs, meant to track current state) to the new value.

**`docs/adr/ADR-0034-workcairn-brand-and-living-company-dashboard.md` was deliberately left unedited.** It is an Accepted ADR recording what was decided at that time (lowercase, before the GitHub rename existed); per this repository's own established convention (`docs/ProductNaming.md`'s "Intentionally unchanged identifiers": "Accepted ADRとMigration History内の当時の名称"), historical ADR text is not retroactively edited when a later decision supersedes it — a new ADR records the correction instead, exactly as this document does.

No JSON Contract identifier, Vault path/marker (`.workspace-os/...`, `workspace-os-task-metadata:v1`, etc.), or `workspace-command.v1`/`workspace-interaction.v1`/`workspace.setup` protocol identifier was touched — none of them reference the Go module path, and this Checkpoint's own inventory confirmed that directly rather than assuming it.

## Consequences

- `go build ./...`, `go vet ./...`, `gofmt -l .`, the full test suite (including `-race`), `make public-beta-smoke`, `make v1-release-gate`, and `make public-beta-browser-gate` all pass unchanged after the rename — this is a same-repository, same-build-graph identifier substitution, not a behavioral change.
- `go.sum` is untouched (verified via `go mod tidy` producing no diff).
- A fresh `v1.0.0-beta.1` release archive was built and verified against the new module path; the packaged binaries report the correct version/commit.
- This closes the module-path/GitHub-slug casing inconsistency PHASE PB-2.1 identified and explicitly deferred as a "report, don't guess" finding. There is no remaining known naming or module-path inconsistency in the repository as of this ADR.
- Because this repository has not yet tagged or pushed `v1.0.0-beta.1`, this is not a breaking change to any published consumer — there is no external importer of the old lowercase path to migrate.

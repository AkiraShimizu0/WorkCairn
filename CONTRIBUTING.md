English | [日本語](CONTRIBUTING.ja.md)

# Contributing to WorkCairn

This guide is for developers contributing code to WorkCairn. If you're looking to use the product, see the [README](README.md); if you're deploying or operating it, see the [Operator Guide](docs/OperatorGuide.md).

## Before you start

Read [AGENTS.md](AGENTS.md), [docs/CONSTITUTION.md](docs/CONSTITUTION.md), and [docs/Architecture.md](docs/Architecture.md) — they're the source of truth for this repository's non-negotiable rules and current structure. Check `docs/adr/` for any decision related to what you're touching.

## Issues and Discussions

- **Issues** — bugs and concrete feature requests.
- **Discussions** — questions, ideas, and anything not yet a specific proposal.
- **Security reports** — never in a public Issue; see [SECURITY.md](SECURITY.md) (GitHub Private Vulnerability Reporting).

## Development environment

- Go 1.23+, a POSIX shell, `make`, and `tar`.
- No real Vault, `.env`, or real Provider API key is needed to build or test.
- Browser tests additionally need Node.js 20+ — test-only, never part of the product build.

## Branches and commits

Branch from `main`. Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`, ...) — `CHANGELOG.md` is generated from this history, so keep the summary line accurate and scoped to the change.

## Build and test

These are the commands that actually exist in this repository's `Makefile` — run them from source rather than guessing at others.

```bash
make go-build                          # build the three binaries into bin/
cd go && go test -count=1 ./...        # unit tests
cd go && go test -race -count=1 ./...  # race detector
cd go && go vet ./...
gofmt -l .                             # must print nothing
make public-beta-smoke                 # fast end-to-end smoke: Mock Provider + temporary Vault
make v1-release-gate                   # build + full test + race + vet + gofmt + release matrix + git diff --check
```

If you touched the UI, also see [Browser tests](#browser-tests).

## Architecture rules

The full rules live in [AGENTS.md](AGENTS.md) and [docs/CONSTITUTION.md](docs/CONSTITUTION.md) — read those before changing anything structural. In short:

- **Go only.** Product code, build, test, and release tooling are Go. The one exception is the test-only Playwright browser harness (ADR-0043), which never enters the Go module or a product binary.
- **Kernel coordinates, Domains decide.** The Kernel handles Service registration and Command coordination only — no business rules, no storage format, no Provider config.
- **Event-driven.** Business Events are facts, not log entries; Audit, Notification, and Metrics subscribe to them rather than being written to directly.
- **Adapters at the edges.** Vault, filesystem, HTTP, and LLM Provider access are all Adapters — Core stays neutral to all of them.
- **Task lifecycle has one owner.** Task state changes and Task lifecycle Events come only from `TaskService`.
- **Runners call Providers, nothing else.** A Runner doesn't touch Task state, approval, retry, audit, or Deliverable storage.
- **Explicit approval before effects.** No Task start, external call, or persistence begins without it.
- **No hidden retry or fallback.** A missing credential, timeout, or ambiguous state gets surfaced, not silently retried or routed around.
- **Reuse existing primitives first.** Check the repository, then the Go standard library, then an already-adopted dependency, before writing something new.
- **Business rules stay WorkCairn-owned.** Task lifecycle, Approval, Recovery, and similar product logic aren't delegated to an external library.

## JSON Contract compatibility

JSON Contract v1 (and the Prompt/Markdown/migration fixtures that go with it) is a stable, language-neutral boundary. Additive, backward-compatible changes are the default; anything breaking needs a new contract version, a migration plan, updated fixtures, and an ADR.

## Security and credentials

- Never commit `.env` or any real credential.
- Never put a real API key or credential in a test fixture.
- Tests don't read from Keychain or a headless-local credential file — use the Fake Provider.
- Tests don't call a real Provider API — use the existing Fake Runner / Mock HTTP server.
- Tests don't run against a real Vault — use a temporary directory.

## UI changes

If you touch `go/internal/httpapi/web/`, verify:

- Light mode and dark mode both render correctly (colors should reference the existing CSS variables in `styles.css`, not a hardcoded value).
- No optional value renders as the literal text `null` or `undefined`.
- Copy stays platform-neutral where the product itself is.
- The relevant browser tests pass — see [Browser tests](#browser-tests).

Some information (individual AI employee names, for example) is intentionally hidden in the current Public UI. If a change would reintroduce it, confirm the design intent first — check `docs/adr/` and recent Public Beta ADRs rather than assuming it was an oversight.

## Browser tests

`tests/browser/` is a Playwright-based, test-only harness (see [AGENTS.md](AGENTS.md) for the ADR-0043 boundary). Set up once:

```bash
make public-beta-browser-setup
```

Then, following [AGENTS.md](AGENTS.md)'s staged validation:

```bash
make check-ui-fast                # chromium-desktop, @critical only -- while iterating
make check-ui-changed AREA=<tag>  # chromium-desktop, one tagged area -- once that area is done
make check-ui-full                # full Chromium + WebKit iPhone suite -- once, before a commit candidate
```

Don't run the full suite on every iteration — it's slow, and meant for the end of a change rather than the middle of one.

## ADRs

An ADR is expected for a change that introduces new persistent state, a JSON Contract change, an ownership change, retry/fallback semantics, a Provider boundary change, a security boundary change, or a storage architecture change. A small bug fix usually doesn't need one. Start from `docs/adr/ADR-template.md`. Accepted ADRs aren't rewritten to match later branding or wording — a new decision gets recorded in a new ADR instead.

## Documentation

- [README.md](README.md) / [README.ja.md](README.ja.md) are for general users — what the product does, not how it's built. Don't introduce internal terminology there.
- [docs/OperatorGuide.md](docs/OperatorGuide.md) is for people running WorkCairn — deployment and operational detail.
- [docs/Architecture.md](docs/Architecture.md) and `docs/adr/` are for contributors — current structure and past design decisions.

If your change alters usage instructions, update the README/Operator Guide in the same change; if it's a significant design decision, record it as an ADR.

## Pull request checklist

1. Explain the change's reason and scope.
2. State any contract or failure-semantics changes explicitly.
3. Run the relevant tests, race test, `go vet`, `gofmt`, and `git diff --check` — plus the relevant browser tests if you touched the UI.
4. Confirm no real API or real Vault was used anywhere in testing.
5. Confirm the diff has no generated artifact, secret, or local path — `bin/`, `dist/`, `node_modules/`, `test-results/`, `.env`, Vault data; see `.gitignore`.

## Releases

Tagging, creating a GitHub Release, and force-pushing to `main` are maintainer-controlled operations — not something a contributor's PR should do.

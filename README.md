English | [日本語](README.ja.md)

# WorkCairn

WorkCairn is a local-first product that runs your own AI company from natural-language requests. An AI employee plans the work, another executes it, a third reviews it independently, and revises it if needed. You're only asked to answer real questions and approve what actually matters.

Candidate version: `v1.0.0-beta.1`. The product runtime, build, release, and distribution are all Go only. The only exception is a separate, test-only browser acceptance harness (ADR-0043) that uses Node/Playwright to drive an actual browser — it never ships in the product archive.

## 1. What is WorkCairn

WorkCairn isn't a company simulation. There's no payroll or morale to manage — instead, it shows you who made something, who reviewed it, and who's fixing it if it needs work. Most of the time it just says `Your company is working. No action needed.` and doesn't ask you to micromanage.

## 2. What you can do

- Turn a natural-language request into a work plan, then approve it (after answering any clarifying questions) to apply it to a Project and its Tasks
- Have Tasks executed, independently reviewed by a different AI employee, and revised and re-reviewed if changes are requested
- See employees, assignments, and the maker → reviewer → revision flow in `Company View`
- See exactly what you're delegating — as an Autonomy Contract — at the moment you approve a Workflow
- Review deliverables, independent review results, revisions, approvals, and external publications from a durable, saved record
- See what the company handled on its own versus what needed your judgment, as CEO Attention
- Trust that nothing happens before approval, and that partial failures are never hidden
- Keep deliverables, review evidence, revision intent, and execution history stored locally
- Use read-only diagnostics and a narrow, explicit recovery path grounded only in confirmed evidence

## 3. How it works

```text
Describe the request in natural language
→ answer any clarifying questions
→ review and approve the proposed plan
→ Project / Tasks are created
→ approve the work (Workflow)
→ a Task runs, then gets reviewed
→ move to the next Task if it's accepted
→ revise and re-review if changes are requested
→ review the finished deliverable and its execution record
```

The UI doesn't implement this flow itself — it's a thin client that displays whatever "next action" the underlying Interaction Session reports. Task state, and the record of how it changed, is owned by a single internal component (TaskService) — nothing else touches it.

The general-purpose daemon can only run the operations this flow needs. Individual Task operations, reviews, revisions, the scheduler, and external publishing exist as operator-only tools and internal processes, but aren't reachable from the general Web UI.

## 4. Supported environment

The initial Public Beta target is **macOS on Apple Silicon (arm64)**.

| OS / architecture | Status | Verified so far |
|---|---|---|
| macOS / arm64 | Beta Tier 1 | Build, full test suite, race tests, and native CLI/daemon smoke tests all pass |
| macOS / amd64 | Release candidate | Cross-builds successfully; needs native smoke testing on an Intel Mac before distribution |
| Linux / amd64 | Release candidate | Cross-builds successfully; needs native filesystem/daemon smoke testing before distribution |
| Linux / arm64 | Release candidate | Cross-builds successfully; needs native filesystem/daemon smoke testing before distribution |
| Windows | Not supported | Vault writes rely on file locking that isn't implemented on Windows |

You'll need Go 1.23+, `make`, a POSIX shell, and `tar`. If you're using a release archive instead, you don't need the Go toolchain at all.

## 5. Installation

Start with an empty, temporary directory rather than a real Vault.

### Build from source

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
cd workcairn
make go-build
bin/workcairn version
```

### Install from a release archive

Verify the checksum first — `shasum` on macOS, `sha256sum` on Linux.

```bash
shasum -a 256 -c workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
cd workcairn_v1.0.0-beta.1_darwin_arm64
bin/workcairn version
```

## 6. Running WorkCairn

```bash
bin/workcairn-daemon
```

The first run opens a native macOS folder picker. Create a new, empty `WorkCairn` folder inside iCloud Drive (recommended, not required — any local folder works too) and the Local Web UI opens once you select it. The location is saved to Application Support and reused on every restart. You can't select an existing personal Obsidian Vault, your home folder, or the iCloud Drive root itself.

A setup wizard walks you through explicitly approving a starter team, and connects Claude through Keychain from a native macOS screen — you never have to type a model ID or pick a route yourself. From there, `会社を始める` (Start the company) takes you to your first request.

See [9. Safety and approval](#9-safety-and-approval) for what to set up before sending a natural-language request, and the [Public Beta Quickstart](docs/PublicBetaQuickstart.md) / [macOS First-run Acceptance](docs/PublicBetaFirstRunAcceptance.md) for a full first-run walkthrough.

## 7. Main CLI

- `workcairn-daemon`: the daemon Public Beta users run — handles requests and serves the Local Web UI
- `workcairn`: an operator CLI for explicitly planning, approving, executing, inspecting, and recovering work
- `workcairn-core`: an external process boundary that speaks a fixed JSON contract (JSON Contract v1)

`workcairn` also has detailed operator subcommands (inspecting the Organization, creating Projects/Tasks, recovery, and more). Day-to-day use is fully covered by the daemon's Web UI — you shouldn't normally need these. See the [Operator Guide](docs/OperatorGuide.md) for details.

## 8. Daemon options

These are the flags you're likely to use as a Public Beta user.

| Flag | Default | Description |
|---|---|---|
| `--vault` | (empty — resolved via the folder picker) | Explicitly set the Vault location. Advanced use. |
| `--listen` | `127.0.0.1:8787` | Explicitly set the address the daemon listens on. Advanced use. |
| `--local-network` | `false` | Allows access from another device on the same trusted local network. WorkCairn automatically selects an appropriate private local-network address, and requires pairing to connect. |
| `--claude-credential-source` | `automatic` | Where Claude's credential comes from (`automatic` / `environment` / `keychain` / `headless-local`). Leave this at the default unless you have a specific reason not to. |

If you pass both `--listen` and `--local-network`, the explicit `--listen` address wins over automatic selection. `--local-network` alone auto-selects a private local-network address. With neither flag, only connections from the same machine (`127.0.0.1`) are accepted.

`--local-network` is not for exposing WorkCairn to the internet — it doesn't support TLS, remote authentication, or port forwarding, and is meant only for a trusted device on the same local network. Operator-level flags not listed here (like `--provider-timeout`) are documented in the [Operator Guide](docs/OperatorGuide.md).

## 9. Safety and approval

- You can review what's about to happen before it happens — actions with side effects require explicit human approval
- The same request arriving twice never runs the work twice, and different requests are never confused with each other
- What's actually done, and what's still unconfirmed, is explained from a durable record — never guessed
- The employee you're delegating to, whether review is required, how many revisions are allowed, and any execution limits are all fixed and shown to you as part of what you approve
- A failure after publishing or after a deliverable is saved is never hidden, and completed work is never silently deleted
- If a state is ambiguous, WorkCairn never guesses and re-runs it — it asks you to confirm recovery instead
- Before real use, WorkCairn asks you to try a temporary Vault first and have an external backup ready

You don't need a credential just to reach the UI. Generating a plan or running a Task requires connecting Claude from `Settings → AI Connections → Connect Claude on this Mac`. Whatever you enter there is stored only in macOS Keychain — it never reaches the browser, the Vault, a Command, or a log. WorkCairn never auto-loads a `.env` file, and you don't need to configure a model ID either — the route resolves automatically, and if nothing is connected, WorkCairn stops before sending anything rather than silently switching providers.

If something exits abnormally or shows `attention_required`, don't guess and retry — see the [Recovery Guide](docs/Recovery.md).

## 10. Data storage

WorkCairn itself persists deliverables, review records, revision intent, and execution/audit history inside your Vault — that's its primary behavior, not something layered on top. Obsidian is an **optional viewer**, not a required dependency. You can open the same dedicated folder (visible in Finder) with Obsidian's `Open folder as vault` to browse the same deliverables and human-readable history. WorkCairn works normally even if you never open Obsidian at all.

WorkCairn itself is not a backup product. Try it with a temporary Vault before real use, and keep a backup made outside WorkCairn. See the [Operator Guide](docs/OperatorGuide.md) and [Recovery Guide](docs/Recovery.md) for details.

## 11. Repository structure

```text
go/            WorkCairn's Go source
go/cmd/        Entry points for the CLI, daemon, and core binaries
go/internal/   WorkCairn's internal domains, services, adapters, and runtime
docs/          Design, operations, and release documentation
docs/adr/      Architecture Decision Records
fixtures/      Test inputs and fixed test data
tests/         Browser and integration tests
scripts/       Build, release, and verification scripts
.ai/           Working context for AI development agents
AGENTS.md      Rules AI agents follow when working in this repo
README.md      English README (this file)
README.ja.md   Japanese README
CHANGELOG.md   Change history
SECURITY.md    How to report a vulnerability
VERSION        Source of truth for the release version
Makefile       Build, test, and release commands
```

## 12. Current limitations

- Remote authentication, TLS, internet exposure, and push notifications aren't implemented
- No durable queue, automatic resume, event replay, or automatic reconciliation
- The scheduler and single-target WordPress publishing exist as operator-only tools, hidden from the general Public Beta UI
- Windows isn't supported for Vault writes
- Connecting from another device (like an iPhone) over `--local-network` is an available feature, but not a required Public Beta target

## 13. Documentation

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Release Notes](docs/ReleaseNotes.md)
- [Public Beta Release Checklist](docs/PublicReleaseChecklist.md)
- [Product Naming](docs/ProductNaming.md)
- [Operator Guide](docs/OperatorGuide.md)
- [System Overview](docs/SystemOverview.md)
- [Architecture](docs/Architecture.md)
- [HTTP Command API](docs/HTTPAPI.md)
- [Go Only Release Gate](docs/GoOnlyReleaseGate.md)
- [Security Policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Migration History](docs/MigrationHistory.md)

## Development and verification

```bash
make public-beta-smoke
make public-beta-browser-setup # first time only: test-only Node / Chromium / WebKit
make public-beta-browser-gate
make v1-release-gate
```

The browser gate is an independent acceptance pass that drives the actual daemon and its embedded UI. Node/Playwright are test-only and never ship in the product runtime or release archive.

`public-beta-smoke` uses only a temporary Vault and a mock AI provider to verify Task execution, deliverables/audit trail, review/revision branches, and request completion end to end. `v1-release-gate` builds all 3 binaries for all 4 targets, and runs the full Go test suite, race tests, `vet`, `gofmt`, and a repository-content guard.

A release archive can be built using `VERSION` as the default:

```bash
make release-package RELEASE_GOOS=darwin RELEASE_GOARCH=arm64 \
  BUILD_DATE=2026-08-10T00:00:00Z
```

## License

[MIT License](LICENSE)

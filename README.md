English | [日本語](README.ja.md)

# WorkCairn

WorkCairn takes a natural-language request, plans the work, carries it out, and has it checked by an independent reviewer before it's done. If something needs fixing, it gets revised and checked again. You're only asked to answer real questions and approve what actually matters — you don't have to manage the details.

It's currently in Public Beta and runs locally on your own Mac.

## 1. What is WorkCairn

Instead of you tracking every step yourself, WorkCairn keeps track of the work and tells you who did what, who checked it, and what's still being fixed. You're not asked to micromanage — you step in only when a decision, a review, or an approval is actually needed.

## 2. What you can do

- Turn a request into a plan, then approve it (after answering any clarifying questions) to get it under way
- Have work carried out, checked by an independent reviewer, and revised and re-checked if changes are requested
- See who's working on what, and how work is handed off between them, in `Company View`
- See exactly what you're delegating before you approve it
- Look back at results, review outcomes, revisions, approvals, and anything published externally, from a saved record
- See what was handled on its own versus what needed your judgment
- Trust that nothing happens before you approve it, and that problems are never hidden
- Keep results, review notes, and a record of what happened stored locally
- Get a narrow, explicit recovery path — grounded only in what's actually confirmed — if something goes wrong

## 3. How to use it

### Install

Start with an empty, temporary data folder — not any folder you already use for other files.

**Build from source:**

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
cd workcairn
make go-build
bin/workcairn version
```

**Or download a release** from [GitHub Releases](https://github.com/AkiraShimizu0/WorkCairn/releases) — look for `workcairn_<version>_darwin_arm64.dmg` and its matching `.sha256`. Verify the checksum first:

```bash
shasum -a 256 -c workcairn_<version>_darwin_arm64.dmg.sha256
```

Then open the DMG in Finder and use the binaries from the mounted volume in Terminal:

```bash
open workcairn_<version>_darwin_arm64.dmg
/Volumes/workcairn_<version>_darwin_arm64/bin/workcairn version
```

The DMG is Developer ID signed and notarized, so no `xattr` removal, right-click override, or permanent change to your Gatekeeper security settings is needed.

### Run it

```bash
bin/workcairn-daemon
```

The first run opens a native macOS folder picker. Create a new, empty folder in a normal local location on your Mac — this becomes WorkCairn's dedicated data folder, and an ordinary local folder is all you need. iCloud Drive works too if you'd rather use it, but it's just one option, not a requirement. The app opens once you select the folder, and the location is remembered for next time. You can't select an existing personal Obsidian vault, your home folder, or the iCloud Drive root itself.

A setup screen walks you through approving a starter team and connecting Claude — you never have to type a model ID or pick a route yourself. From there, `会社を始める` (Start) takes you to your first request.

See the [Public Beta Quickstart](docs/PublicBetaQuickstart.md) for a full first-run walkthrough.

## 4. What you decide

- Whether to approve the proposed plan for a request
- Whether to approve the work itself once you know what it involves
- What to do when a reviewer asks for changes — WorkCairn revises and re-checks, but stops if it needs more direction from you
- Whether to publish anything externally — that's always a separate approval

## 5. Safety

- Nothing with a side effect happens before you approve it
- The same request arriving twice never runs the work twice
- What's actually done, and what's still unconfirmed, is explained from a saved record — never guessed
- What you're delegating, whether review is required, and any limits are all shown to you as part of what you approve
- A failure after something is published or saved is never hidden, and finished work is never silently deleted
- If something is unclear, WorkCairn asks you to confirm what happened rather than guessing and retrying
- Before real use, try it with a temporary data folder first and keep a backup ready

You don't need any credentials just to look around. Actually generating a plan or doing work requires connecting Claude from `Settings → AI Connections`. Whatever you enter there is stored only in macOS Keychain — it never reaches your browser, your files, or a log. You don't need to configure a model either; if nothing is connected, WorkCairn stops before sending anything rather than silently trying something else.

If something exits abnormally or asks for your attention unexpectedly, don't guess and retry — see the [Recovery Guide](docs/Recovery.md).

## 6. Data storage

WorkCairn stores results, review outcomes, and a record of what happened directly in its own data folder — an ordinary folder on your Mac, not a database or a hidden system location. WorkCairn manages this data locally on its own; Obsidian, iCloud Drive, and other external storage or viewing services are not required. Right now, first-run setup requires choosing an empty, dedicated data folder — no existing personal Obsidian vault, iCloud Drive, or Obsidian install of any kind is needed, and the location is remembered after that.

Obsidian is a completely optional way to browse that same folder, for anyone who wants to: no Obsidian account is required, and you can open the folder with `Open folder as vault` any time, but WorkCairn works fully without it.

WorkCairn is not a backup tool. Try it with a temporary data folder before real use, and keep a backup made some other way. See the [Operator Guide](docs/OperatorGuide.md) and [Recovery Guide](docs/Recovery.md) for details.

## 7. Supported environment

The initial Public Beta target is **macOS on Apple Silicon (arm64)**.

| OS / architecture | Status |
|---|---|
| macOS / arm64 | Supported |
| macOS / amd64, Linux / amd64, Linux / arm64 | Builds successfully; not yet verified on real hardware |
| Windows | Not supported |

You'll need Go 1.23+, `make`, and a POSIX shell to build from source. A downloaded release doesn't need the Go toolchain at all.

## 8. Current limitations

- No remote access over the internet, and no encrypted (TLS) connections
- Connecting from another device (like a phone) works only over your local network, and isn't required for normal use
- No automatic backups — see [6. Data storage](#6-data-storage)
- Some scheduling and one external publishing integration (WordPress) exist but aren't part of the main flow yet
- Windows isn't supported

## 9. For developers

WorkCairn's product code, build, and release process are all Go. A separate, test-only browser test suite uses Node/Playwright to drive a real browser, but never ships in the product.

```text
go/            WorkCairn's Go source
go/cmd/        Entry points for the CLI, daemon, and core binaries
go/internal/   Internal domains, services, adapters, and runtime
docs/          Design, operations, and release documentation
docs/adr/      Design decision records
fixtures/      Test inputs and fixed test data
tests/         Browser and integration tests
scripts/       Build, release, and verification scripts
```

There are three binaries: `workcairn-daemon` (what you run day to day), `workcairn` (an operator CLI for advanced/manual operations), and `workcairn-core` (a fixed JSON interface for other programs). The daemon accepts `--vault <path>` — the current CLI option for setting the WorkCairn data folder's location — and `--local-network` to allow another device on your network to connect; see the [Operator Guide](docs/OperatorGuide.md) for the rest.

```bash
make v1-release-gate     # build + full test suite + checks
make public-beta-smoke   # quick end-to-end check with a temporary data folder
```

## 10. Documentation

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Operator Guide](docs/OperatorGuide.md)
- [Architecture](docs/Architecture.md) and [design decision records](docs/adr/)
- [Release Notes](docs/ReleaseNotes.md)
- [Security Policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT License](LICENSE)

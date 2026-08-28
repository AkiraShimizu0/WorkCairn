# ADR-0069: `--local-network` Flag Rename (formerly `--mobile`)

## Status

Accepted

## Context

ADR-0031 and ADR-0032 built a Local Web UI reachable from a paired device on a trusted LAN, motivated at the time by the primary use case of reaching the daemon from an iPhone. The resulting CLI surface named that capability after the use case: `workcairn-daemon --mobile`.

Read from source (`cmd/workcairn-daemon/main.go`, `internal/httpapi/handler.go`, `internal/httpapi/local_access.go`) rather than assumed, the flag's actual behavior has never been mobile-device-specific:

- It expands the server's bind scope from loopback-only (`NewServer`, `validateLoopbackAddress`) to any private or link-local IPv4/IPv6 address (`NewLocalNetworkServer`, `validateLocalNetworkAddress`) — nothing in that validation checks for a phone, only that the address isn't publicly routable.
- Its address auto-discovery (`discoverMobileAddress`, now `discoverLocalNetworkAddress`) walks local network interfaces for a private/link-local IPv4 address — again, device-agnostic.
- It additionally requires pairing-code authentication (`httpapi.NewLocalAccess`/`EnableLocalAccess`) — a security control, not a device check.
- The internal Go identifiers this flag wires into were already named `LocalAccess`, `NewLocalNetworkServer`, `EnableLocalAccess` — "local network," not "mobile" — before this ADR. Only the outward CLI flag name, one function name (`discoverMobileAddress`), a handful of error/log strings, and one HTTP same-origin intent-header value (`X-Workspace-Intent: mobile-ui.v1`) still said "mobile."

Given the WorkCairn product policy already settled in PHASE PB-2.1/PB-2.3 (iPhone is an available feature, not a required Public Beta target; the primary supported platform is the user's own Mac), keeping a flag literally named `--mobile` for a general "reach this daemon from another device on your network" capability was actively misleading — it suggested a phone-specific feature that doesn't exist as such.

No Public Beta tag has been pushed yet, so there is no external consumer of `--mobile` to keep compatible with.

## Decision

Renamed `--mobile` to `--local-network`, with no deprecated alias — compatibility was explicitly not required for this Checkpoint, and an alias would keep the misleading name alive.

- CLI flag: `--mobile` (bool) → `--local-network` (bool), same default (`false`), same underlying behavior.
- `cmd/workcairn-daemon/main.go`: `discoverMobileAddress` → `discoverLocalNetworkAddress`; the `var mobile bool` → `localNetwork`; the startup message ("WorkCairn mobile UI: ...") → "WorkCairn local network UI: ...".
- `internal/httpapi/handler.go`: `validateLocalNetworkAddress`'s error text ("mobile listen address must be...") → "local network listen address must be...". The function itself was already correctly named.
- `internal/httpapi/local_access.go`: the same-origin intent header value `X-Workspace-Intent: mobile-ui.v1` → `local-network-ui.v1`, updated together with its one client-side setter in `app.js` and the one test harness that sends it directly (`tests/browser/support/actions.mjs`). This value is a same-origin CSRF-style marker, checked only in-process and never persisted — not a JSON Contract v1, Vault, or Command Ledger field — so renaming it carries no migration cost.
- `app.js`'s `approval_reference` free-text audit label (`mobile-ui:<session>:v<version>`) → `local-network-ui:<session>:v<version>` for the same reason: human-readable audit text, not a parsed/validated contract value (confirmed by grep — nothing server-side matches against this prefix).
- `cmd/workcairn-daemon/main.go`'s flag-parsing was extracted from the package-global `flag.CommandLine`/`flag.Parse()` into a `parseFlags(args []string, output io.Writer) (*daemonFlags, error)` using a fresh `flag.NewFlagSet` per call — a pure, behavior-preserving refactor (same defaults, same validation, same exit-code-2-on-bad-flag behavior from `main()`) that makes flag parsing independently unit-testable without cross-test global-flag collisions.

**`--listen`/`--local-network` precedence is unchanged, not reinvented.** If `--listen` is explicitly passed (detected via `flagSet.Visit`, now `config.listenWasSet`), that exact address is used regardless of `--local-network`. If `--local-network` is set and `--listen` was not explicitly passed, the address is auto-discovered. This is the same precedence the original `--mobile` flag already implemented; this ADR only renames it.

**Security semantics are unchanged.** Default remains loopback-only. `--local-network` still only ever binds to loopback/private/link-local addresses (`validateLocalNetworkAddress` rejects anything else), still requires pairing-code authentication, still prints "Trusted local network only; do not expose this address to the internet." TLS, remote authentication, port forwarding, and internet exposure remain explicitly unsupported — the rename does not change what the daemon will or won't do, only what the capability is called.

**Left unchanged (historical record, not rename targets):**
- ADR-0031, ADR-0032: Accepted ADRs describing the decision at the time (iPhone as the motivating use case). Per this repository's established convention, historical ADR text is not retroactively edited when a later decision refines it — this ADR records the correction instead.
- CSS classes `.mobile-visible`/`.mobile-hidden` and related responsive-layout selectors in `app.js`/`styles.css`: these describe viewport-responsive design (narrow vs. wide screen), an unrelated, ongoing concept — not the renamed server flag. Not touched.
- Apple's fixed PWA meta tag names (`apple-mobile-web-app-capable`, etc.) in `index.html`: WebKit/iOS API surface WorkCairn doesn't control the naming of. Not touched.
- `internal/httpapi/executor.go`'s `"Mobile Documents"` path constant: part of the real on-disk macOS iCloud Drive folder name (`~/Library/Mobile Documents/com~apple~CloudDocs/`), unrelated to this flag.

## Consequences

- `workcairn-daemon --mobile` is no longer recognized; running it now fails with Go's standard "flag provided but not defined" error and exit code 2, exactly like any other unknown flag — there is no silent fallback and no deprecated alias.
- `docs/OperatorGuide.md`, `docs/PublicBetaQuickstart.md`, `README.md`, and other public-facing docs referencing the daemon's local-network flag were updated to `--local-network` in the same Checkpoint (PHASE PB-2.4) that produced this ADR.
- New tests (`cmd/workcairn-daemon/main_test.go`) cover: `-mobile` is rejected, `-local-network` sets the expected config, the default remains loopback-only, explicit `-listen` is honored and recorded, and `--help` lists `-local-network` but never `-mobile`. Existing tests (`internal/httpapi/handler_test.go`'s `TestServerRejectsNonLoopbackExposure`) already cover the unsafe-address-rejection security boundary and were not touched by this rename.
- JSON Contract v1, Vault format, `workspace-command.v1`/`workspace-interaction.v1`, and `.workspace-os/*` persistence identifiers are entirely unaffected — none of them ever referenced "mobile."

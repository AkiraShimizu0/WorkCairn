# ADR-0078: Bilingual Product UI Architecture

## Status

Accepted

## Context

The embedded Local Web UI is currently Japanese-first. Static copy is concentrated in `go/internal/httpapi/web/index.html`, while dynamic copy and canonical-value presentation maps live in the single `app.js`. There is no locale setting, no translation catalog, and no use of `navigator.language`. The Settings dialog already exists, browser-local presentation state already uses `localStorage`, and all server contracts continue to expose canonical state, role, operation, failure, and evidence values independently of display labels.

The first bilingual slice must support Japanese and English without translating canonical JSON, user content, Provider output, Task or Project titles, Review evidence, IDs, or command payloads. UI language must also remain separate from the language requested for AI-generated work: changing the interface must never silently change a Prompt or Provider request.

## Decision

### Closed locale and ownership

The UI owns a closed locale type with exactly `ja` and `en`. `ja` remains the deterministic default for compatibility. The browser does not infer a locale from `navigator.language`, operating-system settings, Workspace contents, or Provider output. An absent preference selects `ja`; an invalid stored value is removed and selects `ja` through this documented validation rule.

The preference is presentation-only and device/browser-local. It is stored under one new `localStorage` key, `workcairn.ui-locale`. It is not written to Application Support `workspace.json`, the Vault, Command Ledger, Interaction state, cookies, or an HTTP endpoint. It carries no secret or workspace authority.

### Selection surfaces and switching

A self-describing `日本語 / English` selector is available before pairing/First Run and in Settings. Selecting a locale is an explicit Human action. The selection updates `document.documentElement.lang` and reloads the same page so every static and dynamic surface starts from one locale; it never submits a command.

Pending Command continuity remains governed by the existing `sessionStorage` record and active Session/navigation continuity by their existing `localStorage` entries. If an unsent composer draft is non-empty, locale switching requires an explicit confirmation that the draft will be discarded; the draft is not newly persisted. No automatic switch occurs while the Human is typing.

Locale switching is disabled while the browser owns any unresolved non-GET request. The client maintains one in-memory reload-unsafe request count around the central request boundary: it increments before each non-GET fetch and decrements only after that fetch settles. This covers pairing, read-only prepare/plan POSTs, local setup, synchronous Apply, and the pre-acceptance phase of an asynchronous Command without changing any HTTP payload.

The existing pending-Command record is written to `sessionStorage` before Command submission, so its presence is explicitly **not** evidence that the daemon accepted the Command. For an asynchronous Command POST, locale reload becomes eligible only after the POST has returned its accepted response and the reload-unsafe count has returned to zero; later GET monitoring remains recoverable from the existing pending record. This prevents a presentation change from abandoning an indeterminate request while preserving reload during durable background work.

The new locale is written and read back successfully before reload. If preference storage cannot be read, startup uses the documented `ja` default and shows a browser-local preference warning after initialization. If a write or read-back fails, the current locale remains active, no reload or command occurs, and the UI reports the local preference error. Storage failure never creates a server-side fallback.

### Catalog and rendering contract

UI-owned copy moves into one code-owned `/assets/i18n.js` catalog module with identical key sets for `ja` and `en`. The embedded asset handler adds this exact filename with JavaScript content type and the existing `no-store`/same-origin CSP policy; it does not add a wildcard route or daemon API. Static HTML uses translation keys for text and translatable attributes (`aria-label`, `title`, `placeholder`); dynamic rendering uses a single `t(key, parameters)` function. Interpolation parameters are inserted only through `textContent`/DOM attributes, never as HTML.

The initial document keeps the interactive root hidden under a bootstrap marker until the selected catalog has passed validation and all static translation keys and attributes have been applied. Only then is the root revealed. This prevents an English selection from briefly exposing Japanese controls. The self-describing selector, the fixed bilingual catalog-failure message, and a minimal bilingual no-script notice are the only bootstrap-copy allow-list outside the catalogs.

Startup validation requires:

- exact catalog key equality between `ja` and `en`;
- string values only;
- identical named interpolation placeholders for the same key;
- no missing or extra key.

Named interpolation uses the closed `{name}` syntax, where `name` matches `[a-z][a-z0-9_]*`. Catalog validation extracts placeholders rather than comparing translated text. At each `t` call, the provided parameter keys must exactly equal the selected entry's placeholder keys. An unknown translation key, missing parameter, extra parameter, or invalid parameter value is a translation-contract failure: the app enters the same non-interactive bilingual fatal state instead of displaying a token or partial fallback.

Validation failure does not silently fall back to Japanese or mix languages. The app replaces its interactive surface with a fixed bilingual fatal configuration message and exposes no command control. Unknown canonical server values continue to use the existing safe literal/unknown presentation behavior; that is not a translation fallback.

Selected locale controls UI-owned date/time formatting through the fixed mapping `ja → ja-JP`, `en → en-US`. Canonical timestamps are unchanged.

### Canonical data and output-language boundary

The following remain byte-for-byte canonical and are never translated in storage or transport:

- `workspace-command.v1`, `workspace-interaction.v1`, HTTP payloads and response fields;
- Domain state, role, operation, action, failure, recovery, and evidence values;
- IDs, digests, versions, paths, and timestamps;
- Human-authored requests, clarification answers, Project/Task titles, Deliverables, Review/Revision evidence, and Provider output.

Closed canonical values may be mapped to locale-specific display labels at the final browser presentation boundary, as `stateLabel`, `roleLabel`, Recovery label tables, and Attention label tables already do today. Unknown values are never guessed or rewritten.

`attention.Item.Summary` is an existing Japanese server-owned projection. The English UI does not parse or machine-translate it. In the initial bilingual slice, Japanese continues to show `Summary`; English renders a deterministic UI-owned explanation from the typed `Type`, `Action.Kind`, `Action.Operation`, `EntityType`, and IDs, using a generic typed explanation where those fields do not distinguish a narrower cause. This preserves a fully English UI without changing ADR-0065's HTTP contract or treating Japanese prose as authority.

UI locale is never added to a CEO request, metadata, Prompt, command digest, or Provider configuration. A future AI-output-language preference, if justified, requires its own typed contract and ADR; it must not reuse `workcairn.ui-locale`.

### Verification and rollout

The existing full Browser Gate continues to run with the default `ja`, preserving the current product contract. A focused bilingual Browser Gate covers both `ja` and `en` on Chromium desktop and the critical switching/First Run path on WebKit iPhone. It verifies:

- selector availability before pairing and in Settings;
- persisted locale across reload without server mutation;
- `html[lang]`, locale-specific static/dynamic copy, labels, placeholders, and date formatting;
- invalid stored locale normalization;
- explicit draft-discard confirmation;
- locale switching is blocked during every non-GET request, including a Command's pre-acceptance window;
- a pre-submit pending record is not treated as daemon acceptance, while monitoring after an accepted response remains reload-safe;
- preference read/write failure keeps the current locale and submits no command;
- the interactive root is not revealed before catalog validation and translation application;
- no locale field in command/Interaction HTTP payloads;
- pending-command monitoring survives a locale reload;
- user/Provider/canonical evidence remains unchanged;
- catalog key/placeholder parity and no UI-owned Japanese/English literals outside the catalog allow-list.

The first implementation changes the embedded UI plus the exact static-asset allow-list and its tests needed to serve `i18n.js`. It adds no daemon preference API, Vault migration, Application Support write, JSON Contract change, Provider call, Keychain access, or output-language setting.

## Consequences

- Japanese behavior remains the default and existing Browser tests remain valid.
- English becomes an explicit, persistent browser-local presentation choice rather than an environment guess.
- Missing translations fail visibly and safely instead of producing a mixed-language interface.
- Locale changes cannot alter work semantics or AI output language.
- Moving the current several hundred UI-owned literals into catalogs is a mechanical but broad diff; implementation and focused review must be separate Checkpoints, with no opportunistic UI redesign.

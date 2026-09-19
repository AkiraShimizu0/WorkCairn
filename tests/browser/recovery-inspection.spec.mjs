import { expect, test } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  completeFirstRunFast,
  ensureRequestList,
  openSessionFromList,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Guided Recovery Inspection (M-RECOVERY-4C/4D.1): a read-only addition to
// the existing per-Session Command diagnostics ("詳細を確認"/"処理を再確認").
// No Recovery Plan/apply, no automatic repair, no retry, no new Provider/
// Keychain path -- only GET /v1/projects/{project}/recovery-inspection,
// appended after Command diagnostics, never replacing them.

const STORAGE_SESSION = "workcairn.active-session";

// pausableRoute intercepts one URL pattern and holds every matching request
// open until release() is called. waitUntilReached() resolves only once a
// matching request has actually arrived at the handler -- tests must await
// it before release() so a test can never pass having sent zero requests.
// An optional `respond` (with optional `status`, default 200) fulfills the
// request directly on release instead of passing it through to whatever
// route/backend would otherwise handle it -- used whenever a test needs a
// deterministic response body/status rather than relying on route-chaining
// behavior.
function pausableRoute(page, urlPattern, { match, respond, status = 200 } = {}) {
  let requestCount = 0;
  let resolveReached;
  const reached = new Promise((resolve) => { resolveReached = resolve; });
  let releaseGate;
  const gate = new Promise((resolve) => { releaseGate = resolve; });
  page.route(urlPattern, async (route) => {
    if (match && !match(route.request())) {
      await route.continue();
      return;
    }
    requestCount += 1;
    resolveReached();
    await gate;
    if (respond) {
      await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(respond) });
      return;
    }
    await route.continue();
  });
  return {
    waitUntilReached: () => reached,
    release: () => releaseGate(),
    requestCount: () => requestCount,
  };
}

// queuedPausableRoute is pausableRoute's multi-request counterpart: instead
// of one shared gate, EACH matching request gets its own independent
// release -- required whenever a test must control two or more requests to
// the exact same URL independently (e.g. an old, invalidated inspection's
// delayed response vs. a new one's, both targeting the same Command URL).
// next() resolves, in arrival order, to {release(respond)} for each request
// that reaches the handler. receivedCount() is a running total, independent
// of how many entries next() has already consumed.
function queuedPausableRoute(page, urlPattern, { match } = {}) {
  const arrived = [];
  const waiters = [];
  let totalReceived = 0;
  page.route(urlPattern, async (route) => {
    if (match && !match(route.request())) {
      await route.continue();
      return;
    }
    totalReceived += 1;
    let releaseGate;
    const gate = new Promise((resolve) => { releaseGate = resolve; });
    const entry = { release: (respond) => releaseGate(respond) };
    const waiter = waiters.shift();
    if (waiter) waiter(entry);
    else arrived.push(entry);
    const respond = await gate;
    if (respond) {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(respond) });
    } else {
      await route.continue();
    }
  });
  return {
    next: () => {
      const existing = arrived.shift();
      if (existing) return Promise.resolve(existing);
      return new Promise((resolve) => waiters.push(resolve));
    },
    receivedCount: () => totalReceived,
  };
}

async function jsonRoute(page, urlPattern, body, status = 200) {
  await page.route(urlPattern, async (route) => {
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  });
}

// captureAppPollInterval installs an init script -- re-injected by
// Playwright on every navigation/reload of this page, exactly matching
// mountAttentionSession's own reload -- that intercepts app.js's own
// module-level 5000ms poll interval registration and exposes the captured
// callback on window.__pollTick, so a test can invoke that exact interval
// tick deterministically and prove it fired, instead of inferring firing
// from a fixed real-time wait. Every other setTimeout/setInterval in the
// page (toast, monitorAcceptedCommand polling, first-run polling) is
// untouched: only the literal 5000ms registration is captured, and the
// real setInterval is never called for it (the real timer never fires on
// its own -- only explicit firePollTick() calls do).
async function captureAppPollInterval(page) {
  await page.addInitScript(() => {
    const realSetInterval = window.setInterval.bind(window);
    window.__pollTick = null;
    window.setInterval = (handler, timeout, ...args) => {
      if (timeout === 5000 && window.__pollTick === null) {
        window.__pollTick = () => handler(...args);
        return 0;
      }
      return realSetInterval(handler, timeout, ...args);
    };
  });
}

// firePollTick invokes the captured poll interval callback directly and
// returns its promise WITHOUT awaiting it here -- callers that need to
// observe in-flight behavior (e.g. a silent refresh whose own fetch is held
// by a paused route) control awaiting themselves; callers that only need to
// prove a tick was a fast no-op can await the call directly.
function firePollTick(page) {
  return page.evaluate(() => {
    if (typeof window.__pollTick !== "function") {
      throw new Error("poll interval was not captured -- captureAppPollInterval must run before the page loads app.js");
    }
    return window.__pollTick();
  });
}

// firePollTickExpectingNoFetch fires a tick that is expected to resolve
// promptly as a structural no-op (the single-flight guard should skip it
// before any fetch). Races against a short real-time bound so a regression
// (the tick actually starting a fetch that a test is holding open) fails
// fast with a clear message instead of hanging the whole test until
// Playwright's own default timeout.
async function firePollTickExpectingNoFetch(page, timeoutMs = 3000) {
  let timer;
  try {
    await Promise.race([
      firePollTick(page),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("poll tick did not resolve promptly -- the single-flight guard may not be skipping it")), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

// installClickHandlerCapture installs an init script (re-injected by
// Playwright on every navigation/reload, exactly matching
// mountAttentionSession's own reload) that overrides
// EventTarget.prototype.addEventListener so every "click" listener a button
// registers is ALSO stashed on that element's own __wcLastClickHandler
// property -- purely additive test instrumentation, page-scoped, discarded
// when the page closes at the end of the test. The real addEventListener is
// still called with the exact same arguments, so app.js's own event
// registration and dispatch behavior is completely unchanged; this only
// gives the test a way to later retrieve (from wrapOnClickCompletion) the
// handler a specific button already has, since app.js registers listeners
// via addEventListener (button()/node()'s "onkey" convention), not the
// single-slot `element.onclick` property.
async function installClickHandlerCapture(page) {
  await page.addInitScript(() => {
    const realAddEventListener = EventTarget.prototype.addEventListener;
    EventTarget.prototype.addEventListener = function (type, listener, options) {
      if (type === "click" && typeof listener === "function") {
        this.__wcLastClickHandler = listener;
      }
      return realAddEventListener.call(this, type, listener, options);
    };
  });
}

// wrapOnClickCompletion swaps a button element's currently-registered click
// handler (captured by installClickHandlerCapture) for a persistent
// wrapper, installed once per (element, key) pair, that on EVERY invocation:
// - saves and calls the original handler with the identical `this`/event a
//   real click would have used
// - never changes its return/throw semantics (the original's own return
//   value is returned, and any throw propagates unchanged -- `finally`
//   runs either way)
// - pushes a fresh, independently-awaitable completion Promise onto a FIFO
//   queue at window[key], resolved from that invocation's own `finally`
//   once the original (always `async () => ...` in this codebase)
//   handler's own Promise has fully settled
// A single element can legitimately be clicked more than once in one test
// (a genuine click, later followed by a duplicate-click attempt) -- each
// invocation gets its OWN entry in the queue rather than sharing one
// Promise, so awaiting entry N never races with, or is pre-empted by,
// entry N+1 resolving first. Calling this again for a key already wrapped
// on this element is a no-op (idempotent). No production file is touched:
// this instrumentation exists only in this page instance, added by the
// test right before it needs to observe a click's full completion, and
// never writes to app.js's own module-private `state` object.
async function wrapOnClickCompletion(page, elementHandle, key) {
  await page.evaluate(({ el, key }) => {
    if (el.__wcWrappedKey === key) return;
    const original = el.__wcLastClickHandler;
    if (typeof original !== "function") {
      throw new Error("wrapOnClickCompletion: no captured click handler on this element -- installClickHandlerCapture must run before the page loads app.js");
    }
    el.removeEventListener("click", original);
    window[key] = [];
    const wrapped = async function wrappedOnClick(event) {
      let resolveDone;
      const done = new Promise((resolve) => { resolveDone = resolve; });
      window[key].push(done);
      try {
        return await original.call(this, event);
      } finally {
        resolveDone();
      }
    };
    el.__wcLastClickHandler = wrapped;
    el.__wcWrappedKey = key;
    el.addEventListener("click", wrapped);
  }, { el: elementHandle, key });
}

// awaitOnClickCompletion awaits the completion Promise at `window[key][index]`
// -- the `index`-th click of the button wrapped under `key`, 0 for the
// first click, 1 for the second, etc. -- the deterministic replacement for
// a fixed `page.waitForTimeout` after releasing a paused response: this
// resolves exactly when that specific click's own async handler (including
// its own `finally`, e.g. inspectCommands' ownership release) has fully
// settled, never before, and never conflated with any other click's own
// completion on the same button.
async function awaitOnClickCompletion(page, key, index = 0) {
  await page.evaluate(({ key, index }) => {
    const entry = (window[key] || [])[index];
    if (!entry) throw new Error(`awaitOnClickCompletion: no completion recorded yet for "${key}" at index ${index}`);
    return entry;
  }, { key, index });
}

// awaitOnClickCompletionExpectingNoFetch is awaitOnClickCompletion bounded
// by a short real-time race, used only for a click that is EXPECTED to take
// the fast, synchronous-guard-return path (e.g. inspectCommands' own
// `if (state.inspectionActive) return;` at its very first line, before any
// await): a correctly-guarded duplicate click's wrapped handler settles
// within microtasks. A regression that lets the duplicate click reach a
// real (paused) fetch would instead hang this await indefinitely; the bound
// turns that into a fast, clearly-labeled failure instead of stalling the
// whole test until Playwright's own default timeout.
async function awaitOnClickCompletionExpectingNoFetch(page, key, index, timeoutMs = 3000) {
  let timer;
  try {
    await Promise.race([
      awaitOnClickCompletion(page, key, index),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`onclick completion for "${key}"[${index}] did not resolve promptly -- a guard may not be skipping this duplicate click`)), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

// mountAttentionSession pairs, then fully synthesizes one Interaction
// Session (record + next + empty work-report/conversation) via route
// mocking, sets it as the active Session in localStorage, and reloads so
// the app's own initialize()/refreshCurrent() picks it up through the real
// client code paths -- no real Provider call, no real Vault state, fully
// deterministic next/record shapes for preflight and validation testing.
// An optional beforeReload hook runs immediately before the final reload --
// used by tests that must register a request listener (or reset counters)
// exactly at that boundary, so unrelated first-run/setup traffic from
// completeFirstRunFast is never counted.
async function mountAttentionSession(page, daemon, { sessionId, record, next, beforeReload } = {}) {
  await pairThroughUI(page, daemon);
  await completeFirstRunFast(page);
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, { version: "workspace-interaction.v1", ok: true, result: record });
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/next`, { version: "workspace-interaction.v1", ok: true, result: next });
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/work-report`, { version: "workspace-interaction.v1", ok: false, error: { code: "WORK_REPORT_NOT_FOUND" } }, 404);
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/conversation`, { version: "workspace-interaction.v1", ok: true, result: { session_id: sessionId, entries: [] } });
  // refreshCurrent's own restoreDurableFailure(record, next) fetches every
  // strict-preflight-validated next.commands reference immediately on
  // mount, before any test body has a chance to register its own
  // Command-specific mock -- give each a benign default (no failure) so
  // mount itself never depends on a real Ledger record existing; tests
  // that need a specific Command response register their own page.route
  // for that command_id afterward, which Playwright resolves to the
  // most-recently-registered matching handler.
  for (const reference of Array.isArray(next?.commands) ? next.commands : []) {
    await jsonRoute(page, `**/v1/commands/${encodeURIComponent(reference.command_id)}**`, commandRecord({ commandId: reference.command_id }));
  }
  await page.evaluate((id) => localStorage.setItem("workcairn.active-session", id), sessionId);
  if (beforeReload) await beforeReload();
  await page.reload();
  await expect(page.locator("#request-detail-view")).toBeVisible();
}

// mountArchivedSession is mountAttentionSession's counterpart for an
// archived Session detail view (record.archived === true) -- used only by
// the post-unarchive foreground-refresh test below. Same rationale: fully
// synthesized via route mocking, no real Provider/Vault state.
async function mountArchivedSession(page, daemon, { sessionId, record, next }) {
  await pairThroughUI(page, daemon);
  await completeFirstRunFast(page);
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, { version: "workspace-interaction.v1", ok: true, result: record });
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/next`, { version: "workspace-interaction.v1", ok: true, result: next });
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/work-report`, { version: "workspace-interaction.v1", ok: false, error: { code: "WORK_REPORT_NOT_FOUND" } }, 404);
  await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/conversation`, { version: "workspace-interaction.v1", ok: true, result: { session_id: sessionId, entries: [] } });
  await page.evaluate((id) => localStorage.setItem("workcairn.active-session", id), sessionId);
  await page.reload();
  await expect(page.locator("#request-detail-view")).toBeVisible();
}

function baseRecord(sessionId, version = 3) {
  return { session_id: sessionId, version, state: "workflow_attention_required", archived: false, created_at: "2026-09-01T00:00:00Z", turns: [] };
}

function eligibleNext(sessionId, overrides = {}) {
  return {
    kind: "inspect_workflow_recovery",
    session_id: sessionId,
    expected_version: 3,
    approval_required: false,
    required_fields: [],
    project_name: "Recovery Inspection Test Project",
    commands: [{ scope: "project", project_name: "Recovery Inspection Test Project", command_id: "CMD-WORKFLOW-001" }],
    ...overrides,
  };
}

const RECOVERY_URL = (project) => `**/v1/projects/${encodeURIComponent(project)}/recovery-inspection`;
const COMMANDS_URL = "**/v1/commands/**";

function commandRecord({ commandId = "CMD-WORKFLOW-001", failure = null } = {}) {
  return {
    version: "workspace-command.v1", ok: true,
    result: { schema_version: 1, command_id: commandId, operation: "workflow.reviewed.execute", project_name: "Recovery Inspection Test Project", aggregate_id: commandId, state: failure ? "partial_failure" : "succeeded", version: 1, result: {}, failure },
  };
}

function healthyRecoveryView(project) {
  return { schema_version: 1, project_name: project, healthy: true, task_count: 0, findings: [] };
}

function unhealthyRecoveryView(project) {
  return {
    schema_version: 1, project_name: project, healthy: false, task_count: 1,
    findings: [{ id: "RECOVERY-001", kind: "task_completion_pending", severity: "warning", certainty: "confirmed", related_id: "TASK-001", recoverable: true, recommended_action: "complete_task" }],
  };
}

// ---------------------------------------------------------------------
// Production path -- no route mock, real daemon, real ProcessExecutor,
// real projector, temporary Vault.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: production path through real daemon and temporary Vault @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("bounded_acceptance_request_changes");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.locator("#bounded-acceptance-toggle").check();
    await page.locator("#composer-input").fill("限定確認モードで紹介文を作ってください");
    await page.locator("#composer-send").click();
    await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 45_000 });
    await approvePlanAndExecute(page);
    await expect(page.locator("#composer-input")).toHaveValue(/限定確認を終了しました/, { timeout: 45_000 });

    expect(environment.provider.calls).toHaveLength(3);
    expect(environment.provider.calls.map((call) => call.structured)).toEqual([true, false, true]);
    expect(environment.provider.calls.map((call) => call.fixture)).toEqual([
      "ceo_intent_success", "task_execution_success", "review_request_changes",
    ]);
    const callsBefore = JSON.stringify(environment.provider.calls);

    // Seed one recognized residual file directly at the Vault root -- the
    // same location and pattern (.artifact.*.tmp) the backend's own
    // TestGetRecoveryInspectionResidualFindingOmitsRelatedIDWhenEmpty test
    // uses -- no daemon restart needed since recovery-inspection reads the
    // Vault live per request.
    const residualName = ".artifact.m-recovery-4c-browser.tmp";
    await writeFile(join(environment.vaultRoot, residualName), "stale partial write");

    // M-RECOVERY-4D.1 item 14: the canonical Command-reference count comes
    // directly from the real Next() response's own commands.length -- never
    // from a DOM row-count proxy, which silently under-counts any reference
    // whose fetch succeeds without a failure (zero rows rendered for it).
    const sessionId = await page.evaluate(() => localStorage.getItem("workcairn.active-session"));
    const canonicalNext = await page.evaluate(async (id) => {
      const response = await fetch(`/v1/interactions/${encodeURIComponent(id)}/next`);
      return response.json();
    }, sessionId);
    const canonicalCommandCount = canonicalNext.result.commands.length;
    expect(canonicalCommandCount).toBeGreaterThan(0);

    const commandGets = [];
    const recoveryGets = [];
    page.on("request", (request) => {
      if (request.method() !== "GET") return;
      const url = request.url();
      if (url.includes("/v1/commands/")) commandGets.push(url);
      if (url.includes("/recovery-inspection")) recoveryGets.push(url);
    });

    const entry = page.getByRole("button", { name: "処理を再確認" });
    await expect(entry).toBeVisible();
    await entry.click();

    // The bounded-acceptance Request Changes stop's own Command references
    // (outer workflow + child) genuinely carry a failure (that stop IS the
    // failure) -- inspectCommands' success path renders the real failures
    // table for them, not the zero-failures "記録を確認しました" copy.
    await expect(page.locator("#active-card")).toContainText("REVIEWED_WORKFLOW_BOUNDED_STOP", { timeout: 15_000 });
    await expect(page.locator("#active-card")).toContainText("復旧診断", { timeout: 15_000 });
    await expect(page.locator("#active-card")).toContainText("一時的な残留状態があります");
    await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();

    const bodyText = await page.locator("#active-card").innerText();
    expect(bodyText).not.toContain(residualName);
    expect(bodyText).not.toContain(environment.vaultRoot);
    expect(bodyText).not.toContain("Deliverables/");
    expect(bodyText).not.toContain(".workspace-os");

    // Every canonical Command reference is fetched exactly once -- compared
    // directly against Next()'s own authoritative count, independent of how
    // many of them happen to carry a failure and render a row.
    expect(commandGets.length).toBe(canonicalCommandCount);
    expect(recoveryGets.length).toBe(1);

    expect(environment.provider.calls).toHaveLength(3);
    expect(JSON.stringify(environment.provider.calls)).toBe(callsBefore);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Eligible mode through a second real (non-bounded) Provider-failure
// scenario: next.kind for a stalled Reviewed Workflow is
// inspect_workflow_recovery regardless of which failure caused the stop,
// so this "処理を再確認" path also fetches canonical next.commands and
// attempts Recovery Inspection -- confirmed by directly observing this
// scenario's actual rendered output before writing this test's assertions
// (a Recovery block appears here, same as the production-path test).
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: real Provider-failure remembered-error path is eligible and appends Recovery @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "Provider failureのInspection連携を確認する成果物を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。連携を確認します。");
    await approvePlanAndExecute(page);
    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });

    const commandGets = [];
    const recoveryGets = [];
    page.on("request", (request) => {
      if (request.method() !== "GET") return;
      const url = request.url();
      if (url.includes("/v1/commands/")) commandGets.push(url);
      if (url.includes("/recovery-inspection")) recoveryGets.push(url);
    });

    const entry = page.getByRole("button", { name: "処理を再確認" });
    await expect(entry).toBeVisible();
    await entry.click();

    const card = page.locator("#active-card");
    await expect(card).toContainText("PROVIDER_RATE_LIMITED", { timeout: 15_000 });
    await expect(card).toContainText("現在、復旧が必要な項目はありません", { timeout: 15_000 });
    expect(commandGets.length).toBeGreaterThan(0);
    expect(recoveryGets.length).toBe(1);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Ineligible mode: a synthetic Next kind outside the eligible set, paired
// with a pre-seeded remembered error carrying a command_id -- exercises
// the single workspace-Command fallback; Recovery GET must never happen
// from this path since request.projectName is always "" for ineligible
// mode.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: ineligible remembered-error path fetches exactly one Command, zero Recovery @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-INELIGIBLE-001";
    const record = baseRecord(sessionId, 5);
    record.state = "workflow_attention_required"; // bypasses restoreError's staleness discard, independent of next.kind
    const next = { kind: "done", session_id: sessionId, expected_version: 5, approval_required: false, required_fields: [] };
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.evaluate(({ id, version }) => {
      localStorage.setItem(`workcairn.last-error.${id}`, JSON.stringify({
        session_id: id, session_version: version, title: "処理を完了できませんでした",
        code: "PROVIDER_RATE_LIMITED", stage: "review_provider", command_id: "CMD-INELIGIBLE-001",
        request_id: "", substage: "", category: "", http_status: 0,
        parse_failure_reason: "", parse_failure_field: "", recovery_required: false, details: null,
      }));
    }, { id: sessionId, version: 5 });
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, { version: "workspace-interaction.v1", ok: true, result: record });
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/next`, { version: "workspace-interaction.v1", ok: true, result: next });
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/work-report`, { version: "workspace-interaction.v1", ok: false, error: { code: "WORK_REPORT_NOT_FOUND" } }, 404);
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/conversation`, { version: "workspace-interaction.v1", ok: true, result: { session_id: sessionId, entries: [] } });
    await jsonRoute(page, "**/v1/commands/CMD-INELIGIBLE-001**", commandRecord({ commandId: "CMD-INELIGIBLE-001" }));
    await page.evaluate((id) => localStorage.setItem("workcairn.active-session", id), sessionId);
    await page.reload();
    await expect(page.locator("#request-detail-view")).toBeVisible();

    const commandGets = [];
    const recoveryGets = [];
    page.on("request", (request) => {
      if (request.method() !== "GET") return;
      const url = request.url();
      if (url.includes("/v1/commands/")) commandGets.push(url);
      if (url.includes("/recovery-inspection")) recoveryGets.push(url);
    });

    const entry = page.getByRole("button", { name: "処理を再確認" });
    await expect(entry).toBeVisible();
    await entry.click();

    await expect(page.locator("#active-card")).toContainText(/記録を確認しました|処理記録を取得できませんでした/, { timeout: 15_000 });
    expect(commandGets.length).toBe(1);
    expect(recoveryGets.length).toBe(0);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Eligible preflight: malformed next.commands / next.project_name /
// record-next mismatch, each independently rejected with zero fetches --
// across BOTH the restore/reload path (restoreDurableFailure, now routed
// through the same strict buildInspectionRequest("eligible", ...) preflight
// as a CEO-initiated inspection, M-RECOVERY-4D.1 item 1) and the
// subsequent user-click path. The request listener is registered before
// mountAttentionSession's OWN pairing/first-run traffic even starts, and
// counters are reset at the mount's final reload boundary (via
// beforeReload) so only the traffic under test -- restore-on-reload, then
// click -- is counted.
// ---------------------------------------------------------------------

const ELIGIBLE_MALFORMED_CASES = [
  ["missing commands key", (next) => { delete next.commands; }],
  ["null commands", (next) => { next.commands = null; }],
  ["string commands", (next) => { next.commands = "not-an-array"; }],
  ["object commands", (next) => { next.commands = {}; }],
  ["empty array commands", (next) => { next.commands = []; }],
  ["malformed non-empty commands (missing command_id)", (next) => { next.commands = [{ scope: "project", project_name: "Recovery Inspection Test Project" }]; }],
  ["missing project_name", (next) => { delete next.project_name; }],
  ["blank project_name", (next) => { next.project_name = "   "; }],
  ["session mismatch", (next) => { next.session_id = "SESSION-DIFFERENT-999"; }],
  ["bad expected_version (zero)", (next) => { next.expected_version = 0; }],
  ["bad expected_version (non-integer)", (next) => { next.expected_version = 1.5; }],
];

for (const [label, corrupt] of ELIGIBLE_MALFORMED_CASES) {
  test(`Guided Recovery Inspection: eligible preflight rejects ${label} across restore, reload, and click with zero fetches @recovery`, async ({ page }) => {
    const environment = await startBrowserEnvironment("happy_path");
    try {
      const sessionId = "SESSION-ELIGIBLE-" + Math.random().toString(36).slice(2, 8).toUpperCase();
      const next = eligibleNext(sessionId);
      corrupt(next);

      const commandGets = [];
      const recoveryGets = [];
      page.on("request", (request) => {
        if (request.method() !== "GET") return;
        const url = request.url();
        if (url.includes("/v1/commands/")) commandGets.push(url);
        if (url.includes("/recovery-inspection")) recoveryGets.push(url);
      });

      await mountAttentionSession(page, environment.daemon, {
        sessionId, record: baseRecord(sessionId), next,
        beforeReload: () => { commandGets.length = 0; recoveryGets.length = 0; },
      });

      // Restore/reload path: restoreDurableFailure's own strict preflight
      // must reject before any Command GET, proven by observing zero
      // fetches from the moment the malformed `next` was loaded.
      expect(commandGets.length).toBe(0);
      expect(recoveryGets.length).toBe(0);

      const entry = page.getByRole("button", { name: "詳細を確認" });
      await expect(entry).toBeVisible();
      await entry.click();

      await expect(page.locator("#active-card")).toContainText("確認できる処理記録がありません", { timeout: 10_000 });
      await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();
      // User-click path: still zero -- the total across both paths.
      expect(commandGets.length).toBe(0);
      expect(recoveryGets.length).toBe(0);
    } finally {
      await environment.stop();
    }
  });
}

// A bad record.version (case that must be exercised via record, not next)
// gets its own test since it corrupts baseRecord rather than eligibleNext.
test("Guided Recovery Inspection: eligible preflight rejects bad record version across restore, reload, and click with zero fetches @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-BADVERSION-001";
    const record = baseRecord(sessionId, 0);
    const next = eligibleNext(sessionId);
    const commandGets = [];
    page.on("request", (request) => { if (request.method() === "GET" && request.url().includes("/v1/commands/")) commandGets.push(request.url()); });
    await mountAttentionSession(page, environment.daemon, {
      sessionId, record, next,
      beforeReload: () => { commandGets.length = 0; },
    });
    expect(commandGets.length).toBe(0);
    const entry = page.getByRole("button", { name: "詳細を確認" });
    await expect(entry).toBeVisible();
    await entry.click();
    await expect(page.locator("#active-card")).toContainText("確認できる処理記録がありません", { timeout: 10_000 });
    expect(commandGets.length).toBe(0);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Command reference scope validation: workspace project_name key variants.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: workspace-scope reference with project_name key absent is accepted @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-WORKSPACE-OK-001";
    const next = eligibleNext(sessionId, { commands: [{ scope: "workspace", command_id: "CMD-WORKSPACE-001" }] });
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await jsonRoute(page, "**/v1/commands/CMD-WORKSPACE-001**", commandRecord({ commandId: "CMD-WORKSPACE-001" }));
    await jsonRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { version: "workspace-command.v1", ok: true, result: healthyRecoveryView("Recovery Inspection Test Project") });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
  } finally {
    await environment.stop();
  }
});

const WORKSPACE_PROJECT_NAME_REJECTED = [
  ["null", null], ["false", false], ["zero", 0], ["empty string", ""], ["number", 42],
];
for (const [label, value] of WORKSPACE_PROJECT_NAME_REJECTED) {
  test(`Guided Recovery Inspection: workspace-scope reference with project_name=${label} is rejected across restore, reload, and click @recovery`, async ({ page }) => {
    const environment = await startBrowserEnvironment("happy_path");
    try {
      const sessionId = "SESSION-WORKSPACE-BAD-" + Math.random().toString(36).slice(2, 8).toUpperCase();
      const next = eligibleNext(sessionId, { commands: [{ scope: "workspace", project_name: value, command_id: "CMD-WORKSPACE-BAD" }] });
      const commandGets = [];
      page.on("request", (request) => { if (request.method() === "GET" && request.url().includes("/v1/commands/")) commandGets.push(request.url()); });
      await mountAttentionSession(page, environment.daemon, {
        sessionId, record: baseRecord(sessionId), next,
        beforeReload: () => { commandGets.length = 0; },
      });
      expect(commandGets.length).toBe(0);
      await page.getByRole("button", { name: "詳細を確認" }).click();
      await expect(page.locator("#active-card")).toContainText("確認できる処理記録がありません", { timeout: 10_000 });
      expect(commandGets.length).toBe(0);
    } finally {
      await environment.stop();
    }
  });
}

// ---------------------------------------------------------------------
// Command / Recovery success and failure, deterministic pause/release.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: Command success and failure both reach the common terminal renderer @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-COMMAND-FAIL-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await page.route("**/v1/commands/CMD-WORKFLOW-001**", async (route) => {
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ version: "workspace-command.v1", ok: false, error: { code: "SOME_INTERNAL_DETAIL_THAT_MUST_NOT_LEAK" } }) });
    });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    const card = page.locator("#active-card");
    await expect(card).toContainText("処理記録を取得できませんでした", { timeout: 10_000 });
    await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();
    const text = await card.innerText();
    expect(text).not.toContain("SOME_INTERNAL_DETAIL_THAT_MUST_NOT_LEAK");
    // Loading text is fully gone -- replaceChildren, not append.
    expect(text).not.toContain("記録を確認しています");
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: stale Command response released after close never commits @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-STALE-COMMAND-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    const gate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { respond: commandRecord() });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await gate.waitUntilReached();
    // "閉じる" only exists once the inspection reaches its terminal
    // render, which is exactly what's still paused -- "close" here means
    // navigating away from the detail view entirely (the same
    // invalidation point closeActionSurface-based navigation uses).
    await ensureRequestList(page);
    // Deterministic completion boundary instead of a fixed sleep: wait for
    // the actual network response to finish before asserting its absence
    // from the DOM.
    const responseSettled = page.waitForResponse((response) => response.url().includes("/v1/commands/CMD-WORKFLOW-001"));
    gate.release();
    await responseSettled;
    const text = await page.locator("body").innerText().catch(() => "");
    expect(text).not.toContain("記録を確認しました");
    expect(text).not.toContain("処理記録を取得できませんでした");
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: Recovery HTTP failure preserves Command diagnostics @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-RECOVERY-HTTP-FAIL-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
    await page.route(RECOVERY_URL("Recovery Inspection Test Project"), async (route) => {
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ version: "workspace-command.v1", ok: false, error: { code: "RECOVERY_INSPECTION_FAILED" } }) });
    });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    const card = page.locator("#active-card");
    await expect(card).toContainText("記録を確認しました", { timeout: 10_000 });
    await expect(card).toContainText("復旧診断を取得できませんでした", { timeout: 10_000 });
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: stale Recovery response released after close never appends @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-STALE-RECOVERY-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
    const gate = pausableRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { respond: healthyRecoveryView("Recovery Inspection Test Project") });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
    await gate.waitUntilReached();
    await page.getByRole("button", { name: "閉じる" }).click();
    const responseSettled = page.waitForResponse((response) => response.url().includes("/recovery-inspection"));
    gate.release();
    await responseSettled;
    const text = await page.locator("#active-card").innerText().catch(() => "");
    expect(text).not.toContain("復旧診断");
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Recovery response closed validator: schema, Project match, unique IDs,
// extra-field non-exposure.
// ---------------------------------------------------------------------

const RECOVERY_INVALID_CASES = [
  ["wrong schema_version", (v) => { v.schema_version = 2; }],
  ["missing healthy", (v) => { delete v.healthy; }],
  ["healthy/findings mismatch", (v) => { v.healthy = true; v.findings = [{ id: "RECOVERY-001", kind: "task_completion_pending", severity: "warning", certainty: "confirmed", recoverable: true, recommended_action: "complete_task" }]; }],
  ["negative task_count", (v) => { v.task_count = -1; }],
  ["findings not array", (v) => { v.findings = {}; }],
  ["unknown finding kind", (v) => { v.healthy = false; v.findings = [{ id: "RECOVERY-001", kind: "unknown_kind", severity: "warning", certainty: "confirmed", recoverable: false, recommended_action: "none" }]; }],
  ["unknown severity", (v) => { v.healthy = false; v.findings = [{ id: "RECOVERY-001", kind: "task_completion_pending", severity: "urgent", certainty: "confirmed", recoverable: true, recommended_action: "complete_task" }]; }],
  ["recoverable/action mismatch", (v) => { v.healthy = false; v.findings = [{ id: "RECOVERY-001", kind: "task_completion_pending", severity: "warning", certainty: "confirmed", recoverable: true, recommended_action: "none" }]; }],
  ["duplicate finding id", (v) => { v.healthy = false; v.findings = [
    { id: "RECOVERY-001", kind: "task_completion_pending", severity: "warning", certainty: "confirmed", recoverable: true, recommended_action: "complete_task" },
    { id: "RECOVERY-001", kind: "residual_temporary_state", severity: "warning", certainty: "confirmed", recoverable: false, recommended_action: "none" },
  ]; }],
  ["related_id null", (v) => { v.healthy = false; v.findings = [{ id: "RECOVERY-001", kind: "task_completion_pending", severity: "warning", certainty: "confirmed", related_id: null, recoverable: true, recommended_action: "complete_task" }]; }],
];

for (const [label, corrupt] of RECOVERY_INVALID_CASES) {
  test(`Guided Recovery Inspection: Recovery validator rejects ${label} and keeps Command diagnostics @recovery`, async ({ page }) => {
    const environment = await startBrowserEnvironment("happy_path");
    try {
      const sessionId = "SESSION-RECOVERY-INVALID-" + Math.random().toString(36).slice(2, 8).toUpperCase();
      const next = eligibleNext(sessionId);
      await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
      await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
      const view = unhealthyRecoveryView("Recovery Inspection Test Project");
      view.healthy = true; view.findings = [];
      corrupt(view);
      await jsonRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { version: "workspace-command.v1", ok: true, result: view });
      await page.getByRole("button", { name: "詳細を確認" }).click();
      const card = page.locator("#active-card");
      await expect(card).toContainText("記録を確認しました", { timeout: 10_000 });
      await expect(card).toContainText("復旧診断の形式を確認できませんでした", { timeout: 10_000 });
    } finally {
      await environment.stop();
    }
  });
}

test("Guided Recovery Inspection: Project mismatch response is rejected @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-PROJECT-MISMATCH-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
    const mismatched = healthyRecoveryView("A Completely Different Project");
    await jsonRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { version: "workspace-command.v1", ok: true, result: mismatched });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    const card = page.locator("#active-card");
    await expect(card).toContainText("記録を確認しました", { timeout: 10_000 });
    await expect(card).toContainText("復旧診断の形式を確認できませんでした", { timeout: 10_000 });
    const text = await card.innerText();
    expect(text).not.toContain("A Completely Different Project");
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: extra unknown fields in a valid response are ignored, never exposed @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-EXTRA-FIELD-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
    const view = unhealthyRecoveryView("Recovery Inspection Test Project");
    view.findings[0].unexpected_marker = "RAW_MARKER_MUST_NOT_APPEAR_IN_DOM";
    view.unexpected_top_level = "RAW_MARKER_MUST_NOT_APPEAR_IN_DOM";
    await jsonRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { version: "workspace-command.v1", ok: true, result: view });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    const card = page.locator("#active-card");
    await expect(card).toContainText("復旧診断", { timeout: 10_000 });
    const text = await card.innerText();
    expect(text).not.toContain("RAW_MARKER_MUST_NOT_APPEAR_IN_DOM");
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Duplicate click, single-flight ownership.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: duplicate click starts no additional fetch @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-DUPLICATE-CLICK-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
    const gate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { respond: commandRecord() });
    // Two native clicks on the same captured DOM node, dispatched
    // synchronously within one page.evaluate call -- the truest
    // reproduction of "the handler ran twice in a row," independent of
    // whether the button is still Playwright-actionable (it is not, once
    // the first click's synchronous inspectCommands start already cleared
    // the quick replies) or which element now occupies its old screen
    // coordinates.
    const handle = await page.getByRole("button", { name: "詳細を確認" }).elementHandle();
    await page.evaluate((el) => { el.click(); el.click(); }, handle);
    await gate.waitUntilReached();
    gate.release();
    await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
    expect(gate.requestCount()).toBe(1);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Session switch / same-Session reopen.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: stale response after Session switch never commits @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionIdA = "SESSION-SWITCH-A-001";
    const sessionIdB = "SESSION-SWITCH-B-001";
    const nextA = eligibleNext(sessionIdA);
    await mountAttentionSession(page, environment.daemon, { sessionId: sessionIdA, record: baseRecord(sessionIdA), next: nextA });
    const nextB = { kind: "done", session_id: sessionIdB, expected_version: 1, approval_required: false, required_fields: [] };
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionIdB)}`, { version: "workspace-interaction.v1", ok: true, result: baseRecord(sessionIdB, 1) });
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionIdB)}/next`, { version: "workspace-interaction.v1", ok: true, result: nextB });
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionIdB)}/work-report`, { version: "workspace-interaction.v1", ok: false, error: { code: "WORK_REPORT_NOT_FOUND" } }, 404);
    await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionIdB)}/conversation`, { version: "workspace-interaction.v1", ok: true, result: { session_id: sessionIdB, entries: [] } });

    const gate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { respond: commandRecord() });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await gate.waitUntilReached();
    // Switch Session entirely (navigating away invalidates the pending
    // inspection immediately). page.reload() itself is the deterministic
    // completion boundary for the navigation -- no fixed sleep needed; the
    // OLD page's own pending fetch belongs to a since-discarded document,
    // so releasing it afterward can have no observable effect regardless.
    await page.evaluate((id) => localStorage.setItem("workcairn.active-session", id), sessionIdB);
    await page.reload();
    gate.release();
    const text = await page.locator("#active-card").innerText().catch(() => "");
    expect(text).not.toContain("記録を確認しました");
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: closing and reopening the same Session's inspection never lets old content leak into the new panel @recovery", async ({ page }) => {
  await installClickHandlerCapture(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-REOPEN-001";
    const next = eligibleNext(sessionId);
    const record = { ...baseRecord(sessionId), request: "Reopen Same Session Test Request" };
    await mountAttentionSession(page, environment.daemon, { sessionId, record, next });
    // A session-list row for this synthetic Session, so the "reopen" step
    // below can genuinely click it -- selectSession()'s own
    // invalidateInspection() call, entirely within the same page/JS realm
    // as the first inspection's still-pending fetch, exactly like a real
    // close-then-reopen from the request list.
    await jsonRoute(page, "**/v1/interactions", [record]);

    const queue = queuedPausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { match: (request) => request.method() === "GET" });

    // First inspection: wrap its button's own onclick so its FULL async
    // completion (including inspectCommands' own `finally`) can be awaited
    // deterministically later, instead of inferring "long enough elapsed"
    // from a fixed wait. Paused before it can render anything.
    const firstButton = page.getByRole("button", { name: "詳細を確認" });
    const firstHandle = await firstButton.elementHandle();
    await wrapOnClickCompletion(page, firstHandle, "firstInspectionDone");
    await firstHandle.click();
    const firstRequest = await queue.next();

    // "Close, then reopen the same Session" via its own list row.
    // selectSession()'s own refreshCurrent() also runs restoreDurableFailure
    // again (now strict-preflight-routed, M-RECOVERY-4D.1 item 1), which
    // fetches the identical Command URL -- give that one an immediate
    // benign answer so the reopen's own render can complete.
    await openSessionFromList(page, "Reopen Same Session Test Request");
    const restoreRequest = await queue.next();
    restoreRequest.release(commandRecord());
    await expect(page.getByRole("button", { name: "詳細を確認" })).toBeVisible({ timeout: 10_000 });

    // "Reopen": start a second inspection on the same Session.
    await page.getByRole("button", { name: "詳細を確認" }).click();
    const secondRequest = await queue.next();

    // The first (now-stale) response carries a distinctive marker; it must
    // never reach the second panel, even though it targets the identical
    // Command URL as the second. Release it, then await the FIRST click's
    // own async handler (including its `finally`) fully settling -- not a
    // fixed wait -- before asserting the marker never landed.
    firstRequest.release({
      version: "workspace-command.v1", ok: true,
      result: { schema_version: 1, command_id: "CMD-WORKFLOW-001", operation: "workflow.reviewed.execute", project_name: "Recovery Inspection Test Project", aggregate_id: "CMD-WORKFLOW-001", state: "partial_failure", version: 1, result: {}, failure: { code: "STALE_FIRST_INSPECTION_MARKER", stage: "x" } },
    });
    await awaitOnClickCompletion(page, "firstInspectionDone");
    const staleText = await page.locator("#active-card").innerText().catch(() => "");
    expect(staleText).not.toContain("STALE_FIRST_INSPECTION_MARKER");

    // The second inspection's own (distinct) response completes normally,
    // and its owner/quick-replies render correctly.
    secondRequest.release(commandRecord());
    await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
    await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: an old invalidated inspection's delayed response never disturbs a new owner's activity flag @recovery", async ({ page }) => {
  await installClickHandlerCapture(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-STALE-FINALLY-001";
    const next = eligibleNext(sessionId);
    const record = { ...baseRecord(sessionId), request: "Stale Finally Test Request" };
    await mountAttentionSession(page, environment.daemon, { sessionId, record, next });
    await jsonRoute(page, "**/v1/interactions", [record]);

    // Request accounting: the queue's arrival order gives each request an
    // unambiguous identity -- #1 is A's own inspection GET, #2 is the
    // reopen's restoreDurableFailure GET (a legitimate, strict-preflight-
    // validated restore fetch, M-RECOVERY-4D.1 item 1 -- never a
    // duplicate), #3 is B's own inspection GET, and a correctly-guarded
    // duplicate click on B must never produce a #4.
    const queue = queuedPausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { match: (request) => request.method() === "GET" });

    // 1-2. Wrap A's button completion, then click it.
    const aButton = page.getByRole("button", { name: "詳細を確認" });
    const aHandle = await aButton.elementHandle();
    await wrapOnClickCompletion(page, aHandle, "aInspectionDone");
    await aHandle.click();
    // 3. A's own inspection GET (request #1) reaches the pause barrier.
    const oldRequest = await queue.next();

    // 4-5. Invalidate A's ownership and reopen the same Session via its own
    // list row -- selectSession()'s own invalidateInspection() call, in the
    // same page/JS realm as A's still-pending fetch.
    await openSessionFromList(page, "Stale Finally Test Request");
    // The reopen's own refreshCurrent() also runs restoreDurableFailure
    // again (request #2, strict-preflight-routed) -- give it an immediate
    // benign answer so the reopen's own render can complete.
    const restoreRequest = await queue.next();
    restoreRequest.release(commandRecord());
    const detailButton = page.getByRole("button", { name: "詳細を確認" });
    await expect(detailButton).toBeVisible({ timeout: 10_000 });

    // 6-8. Wrap B's button completion, click it, and confirm B's own
    // inspection GET (request #3) reaches the pause barrier -- kept paused.
    const bHandle = await detailButton.elementHandle();
    await wrapOnClickCompletion(page, bHandle, "bInspectionDone");
    await bHandle.click();
    const newRequest = await queue.next();
    expect(queue.receivedCount()).toBe(3);

    // 9-10. Release A's long-stale response and await A's own async
    // handler (including its `finally` --
    // `if (inspectionSequence === state.inspectionSequence) state.inspectionActive = false;`)
    // fully settling -- not merely the network response event -- before
    // proceeding. A sequence mismatch must leave B's inspectionActive alone.
    oldRequest.release(commandRecord());
    await awaitOnClickCompletion(page, "aInspectionDone", 0);

    // 11. B's own request is still paused at this point (we have not
    // released `newRequest`), so B is still the active, in-flight owner.

    // 12-13. Duplicate-click B via the same (now-hidden, since B's own
    // start already cleared quick replies) button element -- the SAME
    // "bInspectionDone" wrapper (wrapOnClickCompletion is idempotent per
    // key on this element) records this as its own, independent second
    // entry (index 1) in the completion queue, never sharing or racing
    // with B's still-pending first click (index 0). If A's finally had
    // wrongly reset inspectionActive, this duplicate call would proceed
    // past the `if (state.inspectionActive) return;` guard and reach a
    // real fetch against the still-paused route, hanging rather than
    // resolving -- a fast, bounded failure instead of a hang to the full
    // test timeout. A correctly-guarded call resolves within microtasks
    // (the guard is the function's first statement, before any await),
    // with zero additional Command GETs.
    await page.evaluate((el) => { el.click(); }, bHandle);
    await awaitOnClickCompletionExpectingNoFetch(page, "bInspectionDone", 1);
    expect(queue.receivedCount()).toBe(3);

    // 14-16. Release B's own (first click's) response and await ITS own
    // async handler completion (index 0), then confirm its terminal UI
    // renders correctly.
    newRequest.release(commandRecord());
    await awaitOnClickCompletion(page, "bInspectionDone", 0);
    await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
    expect(queue.receivedCount()).toBe(3);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Explicit refresh vs. silent poll single-flight and priority --
// deterministic via a captured interval callback (M-RECOVERY-4D.1 item 5),
// never a fixed 6s/11s real-time wait: every tick is fired explicitly by
// the test and its absence of network effect is proven directly, not
// inferred from an elapsed window.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: explicit refresh in flight makes every silent poll tick a structural no-op @recovery", async ({ page }) => {
  await captureAppPollInterval(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-SINGLE-FLIGHT-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });

    // #refresh-button lives in the request-list pane header, hidden while
    // a detail view is showing -- "状態を更新" (renderAttention's own
    // quick reply, also silent=false/explicit) is the reachable explicit
    // trigger from this screen.
    const gate = pausableRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, {
      match: (request) => request.method() === "GET",
      respond: { version: "workspace-interaction.v1", ok: true, result: baseRecord(sessionId) },
    });
    await page.getByRole("button", { name: "状態を更新" }).click();
    await gate.waitUntilReached();
    expect(gate.requestCount()).toBe(1);

    // Fire several poll ticks explicitly while the explicit refresh's own
    // request is still held open -- each must resolve promptly (the
    // single-flight guard `if (silent && state.refreshBarrier) return;`
    // short-circuits before any fetch); a regression would instead reach
    // this same paused gate and hang firePollTickExpectingNoFetch's bound.
    await firePollTickExpectingNoFetch(page);
    await firePollTickExpectingNoFetch(page);
    await firePollTickExpectingNoFetch(page);
    expect(gate.requestCount()).toBe(1);

    gate.release();
    // Only the explicit refresh's own result commits -- the attention
    // screen re-renders successfully with no interference from any tick.
    await expect(page.getByRole("button", { name: "詳細を確認" })).toBeVisible({ timeout: 10_000 });
    expect(gate.requestCount()).toBe(1);
  } finally {
    await environment.stop();
  }
});

test("Guided Recovery Inspection: silent poll in flight collapses repeated ticks into one request, then allows a fresh one @recovery", async ({ page }) => {
  await captureAppPollInterval(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-SILENT-COLLAPSE-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });

    const gate = pausableRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, {
      match: (request) => request.method() === "GET",
      respond: { version: "workspace-interaction.v1", ok: true, result: baseRecord(sessionId) },
    });

    // First tick fired explicitly and deliberately not awaited here: its
    // own fetch is what the gate holds open below.
    const firstTick = firePollTick(page);
    await gate.waitUntilReached();
    expect(gate.requestCount()).toBe(1);

    // Further ticks while the first is still in flight must all be
    // structural no-ops -- bounded so a regression fails fast.
    await firePollTickExpectingNoFetch(page);
    await firePollTickExpectingNoFetch(page);
    expect(gate.requestCount()).toBe(1);

    gate.release();
    await firstTick;

    // Once the in-flight refresh has fully settled, the next tick may
    // start a genuinely new silent refresh -- proven by a real increase in
    // the (already-released, so no longer pausing) gate's request count.
    const countAfterFirstSettles = gate.requestCount();
    await firePollTick(page);
    expect(gate.requestCount()).toBe(countAfterFirstSettles + 1);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Silent context-change coverage (M-RECOVERY-4D.1 item 9): while an open
// (terminal-reached) eligible inspection still holds surface ownership, a
// silent poll's freshly-fetched record/next is compared against the
// context captured at inspection start. A legitimate change in any single
// context field invalidates the surface (it is re-rendered fresh); no
// change at all leaves it completely untouched.
// ---------------------------------------------------------------------

const SILENT_CONTEXT_CHANGE_CASES = [
  ["record version changes", { record: (r) => { r.version += 1; } }, true],
  ["expected version changes", { next: (n) => { n.expected_version += 1; } }, true],
  ["next kind changes", { next: (n) => { n.kind = "done"; delete n.commands; delete n.project_name; } }, true],
  ["Session changes", {
    record: (r) => { r.session_id = "SESSION-CONTEXT-CHANGED-999"; },
    next: (n) => { n.session_id = "SESSION-CONTEXT-CHANGED-999"; },
  }, true],
  ["context unchanged", {}, false],
];

for (const [label, mutators, expectInvalidation] of SILENT_CONTEXT_CHANGE_CASES) {
  test(`Guided Recovery Inspection: silent poll where ${label} ${expectInvalidation ? "invalidates" : "preserves"} an open inspection @recovery`, async ({ page }) => {
    await captureAppPollInterval(page);
    const environment = await startBrowserEnvironment("happy_path");
    try {
      const sessionId = "SESSION-CONTEXT-" + Math.random().toString(36).slice(2, 8).toUpperCase();
      const next = eligibleNext(sessionId);
      await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });
      await jsonRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", commandRecord());
      await jsonRoute(page, RECOVERY_URL("Recovery Inspection Test Project"), { version: "workspace-command.v1", ok: true, result: healthyRecoveryView("Recovery Inspection Test Project") });
      await page.getByRole("button", { name: "詳細を確認" }).click();
      await expect(page.locator("#active-card")).toContainText("記録を確認しました", { timeout: 10_000 });
      await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();

      const mutatedRecord = JSON.parse(JSON.stringify(baseRecord(sessionId)));
      const mutatedNext = JSON.parse(JSON.stringify(next));
      if (mutators.record) mutators.record(mutatedRecord);
      if (mutators.next) mutators.next(mutatedNext);
      await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, { version: "workspace-interaction.v1", ok: true, result: mutatedRecord });
      await jsonRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}/next`, { version: "workspace-interaction.v1", ok: true, result: mutatedNext });

      await firePollTick(page);

      if (expectInvalidation) {
        // The stale terminal's "閉じる" is gone -- the surface was cleared
        // and re-rendered fresh from the legitimately different polled state.
        await expect(page.getByRole("button", { name: "閉じる" })).toHaveCount(0, { timeout: 10_000 });
      } else {
        // Nothing about the terminal changed: same owner, same content.
        await expect(page.getByRole("button", { name: "閉じる" })).toBeVisible();
        await expect(page.locator("#active-card")).toContainText("記録を確認しました");
      }
    } finally {
      await environment.stop();
    }
  });
}

// ---------------------------------------------------------------------
// Silent failure race (M-RECOVERY-4D.1 item 10): a silent refresh's own
// request fails while an inspection's Command fetch is separately paused --
// the failure must invalidate the inspection and show the generic
// connection error immediately; the inspection's later, delayed response
// must never overwrite that error UI.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: a silent refresh failure invalidates an in-flight inspection and is never overwritten by its delayed response @recovery", async ({ page }) => {
  await captureAppPollInterval(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-SILENT-FAILURE-RACE-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });

    const commandGate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { respond: commandRecord() });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await commandGate.waitUntilReached();

    const recordGate = pausableRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, {
      match: (request) => request.method() === "GET",
      status: 500,
      respond: { version: "workspace-interaction.v1", ok: false, error: { code: "SOME_INTERNAL_DETAIL" } },
    });
    const tickDone = firePollTick(page);
    await recordGate.waitUntilReached();
    recordGate.release();
    await tickDone;

    // The silent refresh's own failure path invalidates the surface and
    // renders showError()'s own quick replies ("依頼一覧へ" is unique to
    // that path, unlike "状態を更新" which both this and renderAttention's
    // own quick replies share) -- proven before the delayed Command
    // response is released.
    const backToListButton = page.getByRole("button", { name: "依頼一覧へ", exact: true });
    await expect(backToListButton).toBeVisible({ timeout: 10_000 });

    const commandResponseSettled = page.waitForResponse((response) => response.url().includes("/v1/commands/CMD-WORKFLOW-001"));
    commandGate.release();
    await commandResponseSettled;

    // The error UI must not have been overwritten by the stale Command
    // diagnostics.
    await expect(backToListButton).toBeVisible();
    const text = await page.locator("body").innerText();
    expect(text).not.toContain("記録を確認しました");
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Global busy non-interference: an inspection's OWN local Command failure
// must never disturb a genuine, concurrently-held global busy owner
// (M-RECOVERY-4D.1 item 7) -- exercised via the real "AI Connections"
// status-refresh flow (setBusy(true)/setBusy(false) around GET
// /v1/provider-status), not merely asserted as hidden throughout.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: inspection local failure never disturbs a genuine global busy owner @recovery", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-GLOBAL-BUSY-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });

    const inspectionGate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", {
      status: 500,
      respond: { version: "workspace-command.v1", ok: false, error: { code: "SOME_FAILURE" } },
    });
    const providerStatusGate = pausableRoute(page, "**/v1/provider-status");

    await page.getByRole("button", { name: "詳細を確認" }).click();
    await inspectionGate.waitUntilReached();

    await page.locator("#settings-button").click();
    await expect(page.locator("#settings-dialog")).toBeVisible();
    await page.locator("#provider-status-refresh").click();
    await providerStatusGate.waitUntilReached();

    const busyOverlay = page.locator("#busy-overlay");
    await expect(busyOverlay).toBeVisible();

    // Fail the inspection's own Command fetch -- it must render its local
    // failure terminal (never touching setBusy) without disturbing the
    // still-in-flight global busy owner.
    const inspectionResponseSettled = page.waitForResponse((response) => response.url().includes("/v1/commands/CMD-WORKFLOW-001"));
    inspectionGate.release();
    await inspectionResponseSettled;
    await expect(page.locator("#active-card")).toContainText("処理記録を取得できませんでした", { timeout: 10_000 });
    await expect(busyOverlay).toBeVisible();

    // Only the busy owner's own completion clears it.
    providerStatusGate.release();
    await expect(busyOverlay).toBeHidden({ timeout: 10_000 });
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Post-unarchive foreground refresh (M-RECOVERY-4D.1 items 3/4): the
// explicit refresh confirmUnarchiveSession() now issues must never be
// skipped by the background-poll single-flight guard, and a still-in-flight
// stale poll's response must never overwrite the just-unarchived state.
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: post-unarchive foreground refresh is never skipped by an in-flight background poll @recovery", async ({ page }) => {
  await captureAppPollInterval(page);
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-UNARCHIVE-RACE-001";
    const archivedRecord = { ...baseRecord(sessionId, 4), state: "action_completed", archived: true };
    const doneNext = { kind: "done", session_id: sessionId, expected_version: 4, approval_required: false, required_fields: [] };
    await mountArchivedSession(page, environment.daemon, { sessionId, record: archivedRecord, next: doneNext });
    const unarchiveButton = page.locator("#quick-replies").getByRole("button", { name: "元に戻す" });
    await expect(unarchiveButton).toBeVisible();

    // Both a stale background poll and the unarchive command's own
    // foreground refresh will fetch the exact same record URL -- a queued
    // gate lets the test release them independently, in either order.
    const queue = queuedPausableRoute(page, `**/v1/interactions/${encodeURIComponent(sessionId)}`, {
      match: (request) => request.method() === "GET",
    });
    await jsonRoute(page, "**/v1/commands", { version: "workspace-command.v1", ok: true });
    await jsonRoute(page, "**/v1/commands/**", {
      version: "workspace-command.v1", ok: true,
      result: { schema_version: 1, command_id: "CMD-UNARCHIVE-MONITOR", operation: "interaction.unarchive", aggregate_id: "CMD-UNARCHIVE-MONITOR", state: "succeeded", version: 1, result: {}, failure: null },
    });

    // Stale background poll starts first and reaches the (paused) record
    // GET -- deliberately not released until after the foreground
    // unarchive refresh has already committed, below.
    const stalePollDone = firePollTick(page);
    const staleRequest = await queue.next();

    // Human unarchive action: must issue its OWN explicit refresh request
    // despite the poll's barrier still being occupied -- proving the fix
    // for the `refreshCurrent(true)` -> `refreshCurrent()` single-flight bug.
    await unarchiveButton.click();
    const foregroundRequest = await queue.next();

    // Foreground result commits first.
    const freshRecord = { ...baseRecord(sessionId, 5), state: "action_completed", archived: false };
    foregroundRequest.release({ version: "workspace-interaction.v1", ok: true, result: freshRecord });
    await expect(page.locator("#composer-input")).toHaveValue("完了しました", { timeout: 10_000 });
    await expect(page.getByRole("button", { name: "元に戻す" })).toHaveCount(0);

    // Old stale poll result released afterward must never overwrite it --
    // refreshSequence supersedes it structurally.
    staleRequest.release({ version: "workspace-interaction.v1", ok: true, result: archivedRecord });
    await stalePollDone;
    await expect(page.locator("#composer-input")).toHaveValue("完了しました");
    await expect(page.getByRole("button", { name: "元に戻す" })).toHaveCount(0);
    await expect(page.locator(".archived-badge")).toHaveCount(0);
  } finally {
    await environment.stop();
  }
});

// ---------------------------------------------------------------------
// Mobile navigation-away invalidation. Only chromium-desktop overrides the
// viewport to a phone size; webkit-iphone already runs at its project's own
// default iPhone 13 viewport and must not be resized (M-RECOVERY-4D.1 item 13).
// ---------------------------------------------------------------------

test("Guided Recovery Inspection: mobile navigation away invalidates a pending inspection @recovery @mobile", async ({ page }, testInfo) => {
  if (testInfo.project.name !== "webkit-iphone") {
    await page.setViewportSize({ width: 375, height: 812 });
  }
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const sessionId = "SESSION-MOBILE-NAV-001";
    const next = eligibleNext(sessionId);
    await mountAttentionSession(page, environment.daemon, { sessionId, record: baseRecord(sessionId), next });

    const gate = pausableRoute(page, "**/v1/commands/CMD-WORKFLOW-001**", { respond: commandRecord() });
    await page.getByRole("button", { name: "詳細を確認" }).click();
    await gate.waitUntilReached();

    await ensureRequestList(page);
    const responseSettled = page.waitForResponse((response) => response.url().includes("/v1/commands/CMD-WORKFLOW-001"));
    gate.release();
    await responseSettled;

    // No stale Command diagnostics text leaked into whatever is now shown,
    // no lingering inspection quick replies, and the active card is hidden
    // or empty.
    const bodyText = await page.locator("body").innerText();
    expect(bodyText).not.toContain("記録を確認しました");
    const activeCardHidden = await page.locator("#active-card").isHidden();
    const activeCardText = activeCardHidden ? "" : (await page.locator("#active-card").innerText()).trim();
    expect(activeCardHidden || activeCardText === "").toBeTruthy();
    await expect(page.getByRole("button", { name: "閉じる" })).toHaveCount(0);
  } finally {
    await environment.stop();
  }
});

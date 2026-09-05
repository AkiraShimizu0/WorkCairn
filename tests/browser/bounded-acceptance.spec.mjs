import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  approvePlanAndExecute,
  completeFirstRunFast,
  openNewRequestFromUI,
  pairThroughUI,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Bounded Provider Acceptance (ADR-0072): a CEO-visible, default-OFF toggle
// on the new-request draft screen that opts a Session into a closed,
// durable execution bound -- Plan generation at most once, exactly 1 Task,
// Review at most once, at most 3 total Provider calls, no Revision/Recovery
// ever offered. This scenario exercises the Request Changes stop (the
// profile's own defining guarantee: Go declines to create a Revision even
// though the Session otherwise looks exactly like any other stalled
// Review). A second, minimal Approve-path scenario below only proves a
// bounded Session can still reach ordinary completion -- it intentionally
// does not duplicate every assertion the first scenario already covers. A
// third scenario proves the toggle's OFF state has a real wire-level
// consequence (no execution_profile field at all), not just a hidden UI
// default.

// assertBoundedStopComposerIdle asserts the two specific Revision-recovery
// button labels used elsewhere in the product (renderRevisionRecovery,
// app.js) never appear for a bounded stop, and that the composer itself is
// in the same disabled/idle state produced whenever `state.lastError` is
// set with no Revision Recovery Next() offered (composerCapabilities,
// app.js) -- checked at both the immediate post-stop point and after
// reload, since both are durable properties of the same terminal state,
// not just a momentary rendering artifact.
async function assertBoundedStopComposerIdle(page) {
  const composer = page.locator("#thread-composer");
  const composerInput = page.locator("#composer-input");
  await expect(composer).toHaveAttribute("data-mode", "idle");
  expect(await composerInput.evaluate((element) => element.readOnly)).toBe(true);
  await expect(page.getByRole("button", { name: "この指摘を踏まえて修正を続ける", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "必要な部分だけ続ける", exact: true })).toHaveCount(0);
}

// assertReviewEvidencePreserved opens the request-detail side panel's
// "Task・Review・Revision" block (renderDetails, app.js) -- the durable,
// server-fetched Review verdict evidence for the stalled Task, independent
// of the failure entry's own guidance copy -- and asserts the Request
// Changes verdict is still visible there.
async function assertReviewEvidencePreserved(page) {
  const panel = page.locator("#details-panel");
  if (!(await panel.evaluate((element) => element.open))) {
    await panel.locator("> summary").click();
  }
  await expect(page.locator("#details-content")).toContainText("Request Changes");
}

test("Bounded Acceptance: default OFF, toggle sends execution_profile, Request Changes stops before Revision with no recovery action @bounded", async ({ page }) => {
  const environment = await startBrowserEnvironment("bounded_acceptance_request_changes");
  const startCommands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try {
      const body = request.postDataJSON();
      if (body?.operation === "interaction.start") startCommands.push(body);
    } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);

    // Default OFF on a fresh draft.
    const toggle = page.locator("#bounded-acceptance-toggle");
    const constraints = page.locator("#bounded-acceptance-constraints");
    await expect(page.locator("#bounded-acceptance-row")).toBeVisible();
    await expect(toggle).not.toBeChecked();
    await expect(constraints).toBeHidden();

    // Turning it on shows the fixed constraint text before anything is sent.
    await toggle.check();
    await expect(constraints).toBeVisible();
    await expect(constraints).toContainText("Plan 1回");
    await expect(constraints).toContainText("Task 1件");
    await expect(constraints).toContainText("Review 1回");
    await expect(constraints).toContainText("最大3回");
    await expect(constraints).toContainText("Revision");
    await expect(constraints).toContainText("Recovery");

    await page.locator("#composer-input").fill("限定確認モードで紹介文を作ってください");
    await page.locator("#composer-send").click();

    // The Plan approval screen shows the bounded profile read-only (no
    // control to change it) with the same fixed constraint text.
    await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 45_000 });
    await expect(page.locator("#active-card")).toContainText("限定確認モード（変更不可）", { timeout: 10_000 });
    await expect(page.locator("#active-card")).toContainText("最大3回");

    // execution_profile actually reached the wire, exactly once, with no
    // clarification round (this fixture's intent has 0 CEOQuestions -- one
    // Plan generation attempt).
    expect(startCommands).toHaveLength(1);
    expect(startCommands[0].payload.execution_profile).toBe("bounded_acceptance");

    await approvePlanAndExecute(page);

    // The bounded-specific stop copy appears -- never a generic failure,
    // never a false success -- and no Revision/Recovery action is ever
    // offered alongside it. It shows in the (disabled, readonly) composer
    // immediately after the failing command, and durably in the timeline
    // as a system/failure entry once the conversation projection includes
    // it (both before and after reload).
    const composer = page.locator("#composer-input");
    const timeline = page.locator("#activity-timeline");
    await expect(composer).toHaveValue(/限定確認を終了しました/, { timeout: 45_000 });
    await assertBoundedStopComposerIdle(page);
    await expect(page.getByRole("button", { name: "処理を再確認" }).or(page.getByRole("button", { name: "状態を更新" }))).toBeVisible();
    await assertReviewEvidencePreserved(page);

    // Exactly 3 Provider calls, in exact order (Plan=structured,
    // Task=unstructured, Review=structured) -- a 4th call, a Revision, or
    // an extra Task/Review would show up here directly, not merely be
    // absent from a fixture that happens to only script 3 responses.
    expect(environment.provider.calls).toHaveLength(3);
    expect(environment.provider.calls.map((call) => call.structured)).toEqual([true, false, true]);
    expect(environment.provider.calls.map((call) => call.fixture)).toEqual([
      "ceo_intent_success", "task_execution_success", "review_request_changes",
    ]);

    // Reload: Profile and Review evidence survive, RecoveryRequired stays
    // false -- the same bounded-stop guidance text (now in the timeline's
    // own durable failure entry, restored from server evidence, not
    // guessed) and the same absence of a Revision/Recovery action.
    await page.reload();
    await expect(timeline).toContainText("限定確認を終了しました", { timeout: 20_000 });
    await assertBoundedStopComposerIdle(page);
    await assertReviewEvidencePreserved(page);

    // No further Provider call happened as a side effect of reload/replay.
    expect(environment.provider.calls).toHaveLength(3);

    // A brand new draft resets the toggle to OFF -- it never carries over
    // from the previous (bounded) Session.
    await openNewRequestFromUI(page);
    await expect(page.locator("#bounded-acceptance-toggle")).not.toBeChecked();
    await expect(page.locator("#bounded-acceptance-constraints")).toBeHidden();
  } finally {
    await environment.stop();
  }
});

// Minimal Approve-path scenario (per the Checkpoint's own "if needed, add a
// minimal Approve completion scenario too" note): proves a bounded Session
// can still reach ordinary Task+Review completion, using the same fixture
// shape as the request-changes scenario above minus the Request Changes
// verdict. This intentionally does not re-assert the toggle/UI-copy
// behavior already covered above.
test("Bounded Acceptance: Approve verdict reaches Task+Review completion @bounded", async ({ page }) => {
  const environment = await startBrowserEnvironment("bounded_acceptance_approve");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.locator("#bounded-acceptance-toggle").check();
    await page.locator("#composer-input").fill("限定確認モードで紹介文を作ってください（承認まで）");
    await page.locator("#composer-send").click();
    await waitForPlanOrClarification(page);
    await approvePlanAndExecute(page);
    await expect(page.locator("#activity-timeline")).toContainText("完了", { timeout: 45_000 });
  } finally {
    await environment.stop();
  }
});

// PB-3an.2d item 2: the toggle's default OFF state must have a real
// wire-level consequence, not just a hidden UI default -- a standard
// (non-bounded) Session's interaction.start payload must never carry the
// execution_profile key at all, confirmed by directly inspecting the
// actual captured request body (the same page.on("request") +
// postDataJSON() mechanism as the first scenario above), not by reading
// any UI text.
test("Bounded Acceptance: standard profile never sends an execution_profile field @bounded", async ({ page }) => {
  const environment = await startBrowserEnvironment("bounded_acceptance_approve");
  const startCommands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try {
      const body = request.postDataJSON();
      if (body?.operation === "interaction.start") startCommands.push(body);
    } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await expect(page.locator("#bounded-acceptance-toggle")).not.toBeChecked();

    await page.locator("#composer-input").fill("通常モードで紹介文を作ってください");
    await page.locator("#composer-send").click();
    await waitForPlanOrClarification(page);

    expect(startCommands).toHaveLength(1);
    expect(Object.prototype.hasOwnProperty.call(startCommands[0].payload, "execution_profile")).toBe(false);
  } finally {
    await environment.stop();
  }
});

import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  completeFirstRunFast,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Progress Intelligence v1 -- No-Progress Recovery: CEO request -> Plan
// approval -> Task execution -> QA Request Changes -> Revision (Deliverable
// unchanged) -> QA repeats the same structural finding (different wording
// each time) -> Revision again (still unchanged) -> QA repeats a third time
// -> CompoundProgressPolicy escalates as NO_PROGRESS_DETECTED (before the
// Revision Guard's own hard count would have stopped it for the same
// reason) -> Recovery UI displayed -> CEO gives an additional instruction
// -> a new Command -> completion. The parallel-branch variant of this
// scenario is covered by a Go integration test instead
// (TestInteractionRecoverRevisionParallelBranchNoProgressThenRecoverySucceedsWithoutReExecutingOtherBranches)
// -- scripting genuinely concurrent Provider call ordering through a fixed
// flat-array fixture is unreliable, and this single-branch scenario already
// exercises the full CEO-visible Recovery UX end to end. Revision Limit
// Recovery's own UI proof (revision-limit-recovery.spec.mjs) covers the
// shared Recovery screen's Deliverable/Review viewer and composer
// mechanics in full detail; this test focuses on what is different for the
// No-Progress stop: the classification, its CEO-facing copy, and that it
// reaches Recovery before the Revision Guard would have.

test("No-Progress Recovery: the same finding repeats with an unchanged Deliverable, then one additional instruction completes the work @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("no_progress_v1");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "品質確認が停滞したときの検知を確認する紹介文を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。初めてWorkCairnを使う人向けです。");
    await approvePlanAndExecute(page);

    // The workflow stops as No-Progress specifically -- never a plain
    // success, never the Revision Limit copy, never a bare generic
    // failure: the dedicated No-Progress copy appears.
    await expect(page.locator("#activity-timeline")).toContainText(
      "成果に十分な変化がなく、同じ品質上の指摘が続いています",
      { timeout: 45_000 },
    );
    await expect(page.locator("#activity-timeline")).not.toContainText("品質確認で修正上限に達しました");

    // Latest result (Deliverable) and latest QA finding are both viewable
    // through the same shared Recovery screen Revision Limit Recovery uses
    // -- no separate No-Progress-specific Recovery UI was built.
    const recoveryCard = page.locator("#active-card");
    await expect(recoveryCard).toBeVisible();
    await expect(recoveryCard).toContainText("成果物・Review");
    await expect(recoveryCard).toContainText("Review: Request Changes");
    await expect(recoveryCard).toContainText("要件が不足しています");

    // Composer accepts an optional additional instruction only in this one
    // state.
    const composer = page.locator("#composer-input");
    await expect(composer).toHaveAttribute("placeholder", /追加の指示/);
    await expect(page.locator("#thread-composer")).toHaveAttribute("data-mode", "recovery");
    await composer.fill("同じ指摘が続いていますが、この観点は無視して続けてください");

    const continueButton = page.getByRole("button", { name: "この指摘を踏まえて修正を続ける" });
    await expect(continueButton).toBeVisible();
    const commandsBeforeRecovery = commands.length;
    await continueButton.click();

    // Recovery is a brand-new explicit Command, never a replay of the
    // Command that hit the No-Progress stop, and never an automatic retry
    // WorkCairn triggered on its own.
    await expect.poll(() => commands.length).toBeGreaterThan(commandsBeforeRecovery);
    const recoveryCommand = commands.find((command) => command.operation === "interaction.workflow.recover_revision");
    expect(recoveryCommand).toBeTruthy();
    expect(recoveryCommand.payload.additional_guidance).toBe("同じ指摘が続いていますが、この観点は無視して続けてください");
    const failedWorkflowCommand = commands.find((command) => command.operation === "interaction.plan.approve_and_execute");
    expect(recoveryCommand.command_id).not.toBe(failedWorkflowCommand?.command_id);

    // Only the necessary work proceeds and it reaches a real completion.
    await expect(page.locator("#activity-timeline")).toContainText("依頼が完了しました", { timeout: 45_000 });
    await expect(page.locator("#composer-input")).toHaveValue("完了しました");
  } finally {
    await environment.stop();
  }
});

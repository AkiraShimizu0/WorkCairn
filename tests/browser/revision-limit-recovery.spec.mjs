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

// Revision Limit Recovery (ADR-TBD): CEO request -> Plan approval -> Task
// execution -> QA Request Changes -> Revision -> Request Changes again ->
// MaxRevisionCount reached -> Recovery UI displayed -> CEO gives an
// additional instruction -> a new Command (never a replay of the failed
// one) -> only the stalled Task is revised again -> completion. The
// parallel-branch variant of this scenario (A/B/C branches, only one
// hitting the limit) is covered by a Go integration test instead
// (TestInteractionRecoverRevisionParallelBranchLimitThenRecoverySucceedsWithoutReExecutingOtherBranches)
// -- scripting genuinely concurrent Provider call ordering through a fixed
// flat-array fixture is unreliable, and this single-branch scenario already
// exercises the full CEO-visible Recovery UX end to end.

test("Revision Limit Recovery: request changes to the limit, then one additional instruction completes the work @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("revision_limit_recovery");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "品質確認の修正上限からの復旧を確認する紹介文を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。初めてWorkCairnを使う人向けです。");
    await approvePlanAndExecute(page);

    // The workflow genuinely stops -- never a plain success, never a bare
    // generic failure: the dedicated Recovery copy appears.
    await expect(page.locator("#activity-timeline")).toContainText("品質確認で修正上限に達しました", { timeout: 45_000 });

    // Latest result (Deliverable) and latest QA finding are both viewable,
    // reusing the existing evidence panel -- never a raw error string.
    const recoveryCard = page.locator("#active-card");
    await expect(recoveryCard).toBeVisible();
    await expect(recoveryCard).toContainText("成果物・Review");
    await expect(recoveryCard).toContainText("Review: Request Changes");
    await expect(recoveryCard).toContainText("注意点の記載が不足しています。");

    // The Deliverable details are collapsed by default (<details>); open it
    // before the viewer toggle inside becomes reachable.
    await recoveryCard.locator(".artifact-detail summary").click();
    await expect(recoveryCard.getByRole("button", { name: "成果物を見る" })).toBeVisible();

    // The Deliverable viewer opens and shows the latest committed content --
    // never Task title, request text, or Plan as a fallback body.
    await recoveryCard.getByRole("button", { name: "成果物を見る" }).click();
    await expect(recoveryCard).toContainText("会社に任せたい仕事を入力してください");

    // Composer accepts an optional additional instruction only in this one
    // state -- and only now (not throughout the whole conversation).
    const composer = page.locator("#composer-input");
    await expect(composer).toHaveAttribute("placeholder", /追加の指示/);
    await expect(page.locator("#thread-composer")).toHaveAttribute("data-mode", "recovery");
    await composer.fill("この指摘は無視して、読みやすさを優先してください");

    const continueButton = page.getByRole("button", { name: "この指摘を踏まえて修正を続ける" });
    await expect(continueButton).toBeVisible();
    const commandsBeforeRecovery = commands.length;
    await continueButton.click();

    // Recovery is a brand-new explicit Command, never a replay of the
    // Command that hit the limit.
    await expect.poll(() => commands.length).toBeGreaterThan(commandsBeforeRecovery);
    const recoveryCommand = commands.find((command) => command.operation === "interaction.workflow.recover_revision");
    expect(recoveryCommand).toBeTruthy();
    expect(recoveryCommand.payload.additional_guidance).toBe("この指摘は無視して、読みやすさを優先してください");
    const failedWorkflowCommand = commands.find((command) => command.operation === "interaction.plan.approve_and_execute");
    expect(recoveryCommand.command_id).not.toBe(failedWorkflowCommand?.command_id);

    // Only the necessary work proceeds -- one more Revision, one more
    // Review -- and it reaches a real completion, not a fabricated success.
    await expect(page.locator("#activity-timeline")).toContainText("依頼が完了しました", { timeout: 45_000 });
    await expect(page.locator("#composer-input")).toHaveValue("完了しました");

    // No duplicate "processing live status" entry lingers after completion.
    await expect(page.locator(".msg-live-status")).toHaveCount(0);
  } finally {
    await environment.stop();
  }
});

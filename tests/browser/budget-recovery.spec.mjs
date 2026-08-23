import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  approvePlanAndExecute,
  completeFirstRunFast,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

test("Budget stop preserves completed branches and explicitly continues only the created Revision before Synthesis @critical @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("budget_recovery_continuation", { providerFixtureMaxCalls: 6 });
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "市場・競合・顧客を調べ、結果をまとめてください");
    await waitForPlanOrClarification(page);
    await expect(page.locator(".msg-embed-plan .msg-attach-list li")).toHaveCount(4);
    await approvePlanAndExecute(page);

    await expect(page.locator("#activity-timeline")).toContainText(
      "この依頼は設定された実行上限に達したため、自動処理を停止しました",
      { timeout: 45_000 },
    );
    // Exact Provider-call arithmetic for the Budget stop (and, below, for
    // the Recovery continuation) is Go's own responsibility --
    // TestInteractionBudgetContinuationResumesCreatedRevisionThenSynthesisWithoutReExecutingCompletedBranches
    // (go/internal/process/interaction_recover_revision_test.go) asserts
    // the exact call counts and per-branch replay-safety with more rigor
    // than this Browser layer can. This spec keeps only what a Browser Gate
    // can uniquely prove: the CEO-visible UI/UX contract.
    expect(commands.filter((command) => command.operation === "interaction.workflow.recover_revision")).toHaveLength(0);

    // Canonical results from the completed sibling branches remain visible,
    // while the Recovery card shows the stopped lineage's latest committed
    // Deliverable and Request Changes Review (the new Revision Task itself
    // has not run yet and is not used as fake evidence).
    await expect(page.locator("#activity-timeline")).toContainText("市場動向を調査する");
    await expect(page.locator("#activity-timeline")).toContainText("競合サービスを調査する");
    await expect(page.locator("#activity-timeline")).toContainText("顧客傾向を分析する");
    const recoveryCard = page.locator("#active-card");
    await expect(recoveryCard).toContainText("完了済みの成果を保ったまま停止した作業だけ続けます");
    await expect(recoveryCard).toContainText("Review: Request Changes");
    await expect(recoveryCard).toContainText("判断根拠が不足しています");

    const composer = page.locator("#composer-input");
    await expect(page.locator("#thread-composer")).toHaveAttribute("data-mode", "recovery");
    await composer.fill("既存の成果を保ち、指摘箇所だけ直してください");
    const continueButton = page.getByRole("button", { name: "必要な部分だけ続ける" });
    await expect(continueButton).toBeVisible();

    // Two synchronous click events model an impatient duplicate tap. The
    // first immediately establishes client single-flight; the second cannot
    // mint another Command. Backend Ledger/CAS safety is covered by Go tests.
    await continueButton.evaluate((element) => { element.click(); element.click(); });
    await expect(page.locator(".msg-live-status")).toContainText("停止した作業だけ続けています");
    await expect(page.getByRole("button", { name: "必要な部分だけ続ける" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });

    // The double-click above models an impatient duplicate tap; this proves
    // the client's own single-flight guard sent exactly one recover_revision
    // Command (not that the backend is safe under concurrency -- that is a
    // CAS/Ledger property Go owns, see the double-Command race test in
    // internal/process). It's a genuine UI/UX behavior, kept here.
    const recoveryCommands = commands.filter((command) => command.operation === "interaction.workflow.recover_revision");
    expect(recoveryCommands).toHaveLength(1);
    expect(recoveryCommands[0].payload.additional_guidance).toBe("既存の成果を保ち、指摘箇所だけ直してください");
    // Whether A/B/C were re-executed, and the exact revision/synthesis call
    // counts, are Go's responsibility (see the comment above). What this
    // Browser Gate can uniquely confirm is that the CEO-visible outcome is
    // correct: the completed branches' own titles are still present (not
    // replaced or duplicated in the timeline) and Synthesis completes.
    await expect(page.locator("#activity-timeline")).toContainText("市場動向を調査する");
    await expect(page.locator("#activity-timeline")).toContainText("競合サービスを調査する");
    await expect(page.locator("#activity-timeline")).toContainText("顧客傾向を分析する");
    await expect(page.locator("#activity-timeline")).toContainText("3つの結果を統合する");
    await expect(page.locator(".msg-live-status")).toHaveCount(0);
    await expect(page.locator("#composer-input")).toHaveValue("完了しました");
  } finally {
    await environment.stop();
  }
});

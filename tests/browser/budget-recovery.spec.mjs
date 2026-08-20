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
    expect(environment.provider.calls).toHaveLength(7); // CEO Intent + exactly six guarded Workflow calls.
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

    const recoveryCommands = commands.filter((command) => command.operation === "interaction.workflow.recover_revision");
    expect(recoveryCommands).toHaveLength(1);
    expect(recoveryCommands[0].payload.additional_guidance).toBe("既存の成果を保ち、指摘箇所だけ直してください");
    expect(environment.provider.calls).toHaveLength(11);
    for (const name of ["task_execution_budget_branch_a", "task_execution_budget_branch_b", "task_execution_budget_branch_c"]) {
      expect(environment.provider.calls.filter((call) => call.fixture === name)).toHaveLength(1);
    }
    expect(environment.provider.calls.filter((call) => call.fixture === "task_execution_budget_revision")).toHaveLength(1);
    expect(environment.provider.calls.filter((call) => call.fixture === "task_execution_budget_synthesis")).toHaveLength(1);
    await expect(page.locator("#activity-timeline")).toContainText("3つの結果を統合する");
    await expect(page.locator(".msg-live-status")).toHaveCount(0);
    await expect(page.locator("#composer-input")).toHaveValue("完了しました");
  } finally {
    await environment.stop();
  }
});

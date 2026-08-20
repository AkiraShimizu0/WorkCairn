import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  approvePlanAndExecute,
  completeFirstRunFast,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// ADR-0051 production-wiring proof at the UI layer: a single CEO approval
// reaches a Plan with an independent-branch + Synthesis shape, and
// WorkCairn decides on its own to run the independent branches concurrently
// before letting Synthesis run -- the CEO never sees or chooses a
// "parallel"/"sequential"/"concurrency" option anywhere. Exact concurrency
// bounds are Go-tested (service/process packages); this test only confirms
// the real product path -- single approval, all Tasks/Synthesis reaching
// completion, canonical Conversation Projection reflecting every Task, and
// no lingering ephemeral processing state -- actually works end to end
// through the real daemon, HTTP, and UI.
test("single approval automatically parallelizes independent Tasks then Synthesis @critical @conversation", async ({ page }) => {
  const workflowCopy = "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。";
  const environment = await startBrowserEnvironment("parallel_synthesis");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "市場調査・競合調査・顧客分析を並行して行い、販売戦略へ統合してください");

    await waitForPlanOrClarification(page);
    await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });

    // The Plan lists all four proposed Tasks (three independent branches +
    // one Synthesis) without the CEO ever being asked to choose how they
    // run -- no "parallel"/"sequential"/"concurrency" control exists
    // anywhere in this view.
    const planItems = page.locator(".msg-embed-plan .msg-attach-list li");
    await expect(planItems).toHaveCount(4);
    await expect(page.locator(".msg-embed-plan")).toContainText("市場動向を調査する");
    await expect(page.locator(".msg-embed-plan")).toContainText("競合サービスを調査する");
    await expect(page.locator(".msg-embed-plan")).toContainText("既存顧客の傾向を分析する");
    await expect(page.locator(".msg-embed-plan")).toContainText("3つの調査結果を統合し販売戦略を作成する");
    for (const label of ["並列", "sequential", "concurrency", "同時実行"]) {
      await expect(page.locator(".msg-embed-plan")).not.toContainText(label);
    }
    // The minimal additive dependency hint (this Checkpoint) surfaces the
    // fan-out/fan-in shape in plain language without a DAG diagram.
    await expect(page.locator(".msg-embed-plan")).toContainText("他の作業と並行して進められます");
    await expect(page.locator(".msg-embed-plan")).toContainText("3件の作業結果をまとめます");

    // Exactly one explicit approval, the same single interaction.plan.approve_and_execute
    // Command ADR-0049 already established -- no per-Task approval, no
    // separate "run in parallel?" approval.
    await approvePlanAndExecute(page);
    expect(commands.filter((command) => command.operation === "interaction.plan.approve_and_execute")).toHaveLength(1);
    // No per-Task approval, no separate "run in parallel?" approval, and no
    // legacy two-step interaction.plan.apply/interaction.workflow.execute
    // pair -- the only Commands the browser ever sends are setup, the
    // initial request, and this single approval.
    const workflowRelatedOperations = ["interaction.plan.apply", "interaction.workflow.execute", "workflow.reviewed.execute"];
    expect(commands.filter((command) => workflowRelatedOperations.includes(command.operation))).toHaveLength(0);

    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 60_000 });

    // Every Provider call this Checkpoint's fixture scripted was actually
    // consumed: the CEO Plan Intent, four Task executions, and four Review
    // Approvals -- proving all three branches and Synthesis genuinely ran,
    // not just the first Task.
    expect(environment.provider.calls).toHaveLength(9);
    expect(environment.provider.calls.filter((call) => call.structured)).toHaveLength(5);
    expect(environment.provider.calls.filter((call) => !call.structured)).toHaveLength(4);

    // Canonical Conversation Projection (ADR-0047, unmodified this
    // Checkpoint) already reflects every Task's assignment and completion
    // from real canonical Events -- including Synthesis, which only
    // appears once its dependencies exist as canonical evidence.
    await expect(page.locator("#activity-timeline")).toContainText("市場動向を調査する");
    await expect(page.locator("#activity-timeline")).toContainText("競合サービスを調査する");
    await expect(page.locator("#activity-timeline")).toContainText("既存顧客の傾向を分析する");
    await expect(page.locator("#activity-timeline")).toContainText("3つの調査結果を統合し販売戦略を作成する");
    await expect(page.locator("#proof-of-work")).toContainText("4件の仕事");

    // No fabricated "並列実行中です"-style canonical Turn was ever written
    // to Go, and once the terminal state lands, the ephemeral live-status
    // sentence used while running must not remain in the timeline.
    await expect(page.locator("#activity-timeline")).not.toContainText(workflowCopy);
    await expect(page.locator("#activity-timeline")).not.toContainText("並列実行中");

    // Detail can be closed and reopened without a stale/partial view --
    // the completed Synthesis result stays reachable, not just the first
    // three branches.
    await page.reload();
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#activity-timeline")).toContainText("3つの調査結果を統合し販売戦略を作成する");
  } finally {
    await environment.stop();
  }
});

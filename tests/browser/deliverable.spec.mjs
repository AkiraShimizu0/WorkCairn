import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  completeFirstRunFast,
  openNewRequestFromUI,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Canonical Deliverable viewer: Markdown/table rendering reached through
// the timeline's own "成果物を見る" entry point.

test("deliverable viewer expands inline at wide width and renders a Markdown table as a table @critical @deliverable", async ({ page }) => {
  const environment = await startBrowserEnvironment("deliverable_markdown_table");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "Markdown table gate用の依頼です");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });

    const toggle = page.getByRole("button", { name: "成果物を見る" }).first();
    await expect(toggle).toBeVisible({ timeout: 15_000 });
    await toggle.click();
    const viewer = page.locator(".deliverable-viewer").first();
    await expect(viewer).toBeVisible();

    // Wide inline display, never a small popup/modal.
    await expect(page.locator("dialog[open]")).toHaveCount(0);
    const viewerBox = await viewer.boundingBox();
    const threadBox = await page.locator("#thread-scroll").boundingBox();
    expect(viewerBox.width).toBeGreaterThan(threadBox.width * 0.85);

    // The Markdown table renders as an actual table, not raw pipe text.
    const table = viewer.locator("table.md-table");
    await expect(table).toBeVisible();
    await expect(table.locator("th")).toHaveCount(2);
    await expect(table.locator("td")).toHaveCount(4);
    await expect(viewer).toContainText("項目");
    await expect(viewer).toContainText("内容");
    await expect(viewer).not.toContainText("|---|---|");
  } finally {
    await environment.stop();
  }
});

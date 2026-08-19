import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  assertComposerBottomStable,
  completeFirstRunFast,
  composerMetrics,
  ensureRequestDetail,
  generatePlanThroughClarification,
  openNewRequestFromUI,
  pairThroughUI,
  seedTimelineMessages,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// WebKit/mobile-specific layout risk that has no meaningful desktop
// equivalent (this file's tests are inherently mobile-shaped).

test("composer stays pinned on mobile and plan tasks show canonical titles @mobile", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await ensureRequestDetail(page);
    await startRequest(page, "りんごについて100文字程度で説明して");
    const mobileBottom = await assertComposerBottomStable(page, "mobile short");

    await seedTimelineMessages(page, 24, "mobile-long");
    const mobileLongBottom = await assertComposerBottomStable(page, "mobile long");
    expect(Math.abs(mobileLongBottom - mobileBottom)).toBeLessThanOrEqual(2);

    await generatePlanThroughClarification(page);
    await expect(page.locator(".msg-embed-plan .msg-attach-task-title")).not.toHaveText(/^x$/i);
    await expect(page.locator(".msg-embed-plan")).toContainText("佐藤 葵");
    await assertComposerBottomStable(page, "mobile plan");
  } finally {
    await environment.stop();
  }
});

import { expect, test } from "@playwright/test";
import { join } from "node:path";
import { startBrowserEnvironment, pathExists } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  assertComposerBottomStable,
  completeFirstRunFast,
  composerMetrics,
  ensureRequestDetail,
  ensureRequestList,
  expectTextOnceInTimeline,
  generatePlanThroughClarification,
  openNewRequestFromUI,
  pairThroughUI,
  seedTimelineMessages,
  startRequest,
  switchSessionFilter,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Core conversation UX: request submission, clarification, plan, approval,
// workflow progress, timeline projection, navigation, and general shell UI.
// This is the primary Chromium-desktop business-logic suite.

test("Public Beta browser happy path survives polling, reload, and daemon restart @critical @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "初めての利用者向けにWorkCairnの紹介文を作り、別のAIで確認してください");

    await expect(page.getByRole("button", { name: "進め方の作成を承認" })).toHaveCount(0);

    const phase = await waitForPlanOrClarification(page);
    if (phase === "clarification") {
      await expect(page.getByRole("button", { name: "後で回答する" })).toHaveCount(0);
      const clarificationAnswer = "はい。初めてWorkCairnを使う人向けです。";
      await answerClarificationIfNeeded(page, clarificationAnswer);
      // The question WorkCairn actually asked must stay visible after the
      // CEO's own answer is recorded -- both are canonical Conversation
      // Projection entries (ADR-0047/CP3+), never a UI-local fabrication or
      // a one-sided echo of only the answer.
      await expect(page.locator("#activity-timeline")).toContainText("読者は初めてWorkCairnを使う人ですか");
      await expect(page.locator("#activity-timeline")).toContainText(clarificationAnswer);
    }

    await expect(page.locator(".msg-embed-plan")).toBeVisible();
    expect(commands.filter((command) => command.operation === "interaction.plan.generate")).toHaveLength(0);
    expect(commands.filter((command) => command.operation === "interaction.workflow.execute")).toHaveLength(0);

    await approvePlanAndExecute(page);
    expect(commands.filter((command) => command.operation === "interaction.plan.approve_and_execute")).toHaveLength(1);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    expect(environment.provider.calls.map((call) => call.fixture)).toEqual([
      "ceo_intent_clarification", "ceo_intent_success", "task_execution_success",
      "review_request_changes", "revision_success", "re_review_approve"
    ]);
    expect(environment.provider.calls.map((call) => call.structured)).toEqual([true, true, false, true, false, true]);

    await expect(page.locator("#activity-timeline")).toContainText("修正をお願い");
    await expect(page.locator("#activity-timeline")).toContainText("対応案:");
    await expect(page.locator("#activity-timeline")).toContainText("修正が完了しました");
    await expect(page.locator("#activity-timeline")).toContainText("依頼が完了しました");
    await expect(page.locator("#activity-timeline")).not.toContainText("任せて進んだ仕事");
    await expect(page.locator("#proof-of-work")).toContainText("2件の仕事");
    await expect(page.locator("#proof-of-work")).toContainText("Review: Approve");

    // review approved must show the canonical review_summary text, never
    // an internal status/error value as the message body.
    await expect(page.locator("#activity-timeline")).toContainText("利用開始の案内が追加され、要件を満たしています。");
    const companyFactTexts = await page.locator("#activity-timeline .msg-company-fact-copy").allTextContents();
    for (const text of companyFactTexts) {
      expect(text.trim()).not.toMatch(/^error$/i);
    }

    // The completion screen offers no quick-reply buttons -- the sole
    // entry point for a new request is the request list's own icon-only "新規作成".
    await expect(page.getByRole("button", { name: "完了を確認" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "新しい仕事を依頼" })).toHaveCount(0);

    // The completed Deliverable is reachable directly from its own
    // "成果物を作成しました" message via the existing read-only Task
    // evidence projection -- not a UI-composed copy of the Deliverable.
    const deliverableToggle = page.locator("#activity-timeline").getByRole("button", { name: "成果物を見る" }).first();
    await expect(deliverableToggle).toBeVisible();
    await deliverableToggle.click();
    await expect(page.locator("#activity-timeline .deliverable-viewer").first()).toContainText("WorkCairn紹介文");

    await page.reload();
    await expect(page.locator("#workspace-view")).toBeVisible();
    await ensureRequestDetail(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible();
    await expect(page.locator("#activity-timeline")).toContainText("依頼が完了しました");
    await expect(page.locator("#activity-timeline")).toContainText("読者は初めてWorkCairnを使う人ですか");
    await expect(page.locator("#activity-timeline")).toContainText("はい。初めてWorkCairnを使う人向けです。");
    await expect(page.getByRole("button", { name: "完了を確認" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "新しい仕事を依頼" })).toHaveCount(0);

    const restarted = await environment.restartDaemon();
    await pairThroughUI(page, restarted);
    await ensureRequestDetail(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible();
    await expect(page.locator("#proof-of-work")).toContainText("2件の仕事");
    await expect(page.locator("#activity-timeline")).toContainText("読者は初めてWorkCairnを使う人ですか");
    await expect(page.locator("#activity-timeline")).toContainText("はい。初めてWorkCairnを使う人向けです。");
    expect(await pathExists(join(environment.vaultRoot, "プロジェクト", "Browser Acceptance Project", "Deliverables", "TASK-001.md"))).toBeTruthy();
    expect(await pathExists(join(environment.vaultRoot, "プロジェクト", "Browser Acceptance Project", "Reviews", "TASK-001.review.json"))).toBeTruthy();
    expect(await pathExists(join(environment.vaultRoot, "プロジェクト", "Browser Acceptance Project", "Revisions", "TASK-002.revision.md"))).toBeTruthy();
  } finally {
    await environment.stop();
  }
});

test("composer stays pinned to the requests pane bottom across conversation shapes @critical @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "りんごについて100文字程度で説明して");
    const shortBottom = await assertComposerBottomStable(page, "short conversation");

    await seedTimelineMessages(page, 20, "long");
    const longBottom = await assertComposerBottomStable(page, "long conversation");
    expect(Math.abs(longBottom - shortBottom)).toBeLessThanOrEqual(2);

    await page.evaluate(() => {
      const timeline = document.querySelector("#activity-timeline");
      const article = document.createElement("article");
      article.className = "msg msg-user";
      const body = document.createElement("div");
      body.className = "msg-body msg-body-user";
      const paragraph = document.createElement("p");
      paragraph.className = "msg-text";
      paragraph.textContent = "長文".repeat(400);
      body.appendChild(paragraph);
      article.appendChild(body);
      timeline?.appendChild(article);
    });
    const longTextBottom = await assertComposerBottomStable(page, "long message");
    expect(Math.abs(longTextBottom - shortBottom)).toBeLessThanOrEqual(2);

    await generatePlanThroughClarification(page);
    await expect(page.locator(".msg-embed-plan .msg-attach-task-title")).not.toHaveText(/^x$/i);
    await expect(page.locator(".msg-embed-plan .msg-attach-task-title")).toContainText("紹介文");
    const planBottom = await assertComposerBottomStable(page, "plan visible");
    expect(Math.abs(planBottom - shortBottom)).toBeLessThanOrEqual(2);

    await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible();
    const quickReplyBottom = await assertComposerBottomStable(page, "quick replies visible");
    expect(Math.abs(quickReplyBottom - shortBottom)).toBeLessThanOrEqual(2);

    await page.setViewportSize({ width: 1280, height: 640 });
    await page.waitForTimeout(150);
    await assertComposerBottomStable(page, "after resize");
  } finally {
    await environment.stop();
  }
});

test("conversation projection renders canonical chat categories @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "初めての利用者向けにWorkCairnの紹介文を作り、別のAIで確認してください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });

    const timeline = page.locator("#activity-timeline");
    await expect(timeline.locator(".msg-user").first()).toContainText("あなた");
    await expect(timeline).toContainText("成果物を作成しました");
    await expect(timeline.locator(".msg-directed").filter({ hasText: "修正をお願い" })).toBeVisible();
    await expect(timeline.locator(".msg-directed .msg-mention").first()).toContainText("@");
    await expect(timeline.locator(".msg-directed").filter({ hasText: "修正をお願い" })).toContainText("対応案:");
    await expect(timeline).toContainText("レビューが完了しました");
    await expect(timeline).toContainText("依頼が完了しました");
    await expect(timeline).not.toContainText("任せて進んだ仕事");

    // Pins companyFactText()/directedCommunicationText()'s mapping from
    // canonical review_summary/review_issues to rendered text (Public Beta
    // Conversation UX Fix, item 2). These strings come from the
    // browser_acceptance_v1 fixture's re_review_request_changes and
    // re_review_approve scenarios, so this exercises the real app.js
    // functions end-to-end in a real browser, not a mocked value.
    await expect(timeline.locator(".msg-directed").filter({ hasText: "修正をお願い" })).toContainText(
      "初めての利用者が次に何をすればよいかが不明確です。",
    );
    await expect(timeline.locator(".msg-directed").filter({ hasText: "修正をお願い" })).toContainText(
      "最初の依頼を入力する案内を追記してください。",
    );
    await expect(timeline.locator(".msg-company-fact-copy").filter({ hasText: "レビューが完了しました" })).toContainText(
      "利用開始の案内が追加され、要件を満たしています。",
    );

    const allMessageBodyTexts = await timeline
      .locator(".msg-company-fact-copy, .msg-directed .msg-text")
      .allTextContents();
    for (const text of allMessageBodyTexts) {
      const trimmed = text.trim();
      expect(trimmed).not.toMatch(/^error$/i);
      for (const rawKind of ["review_approved", "review_request_changes", "deliverable_ready", "task_completed", "revision_completed"]) {
        expect(trimmed).not.toBe(rawKind);
      }
    }
  } finally {
    await environment.stop();
  }
});

test("UI refinement: composer, settings, branding, themes @conversation @mobile", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("happy_path");
  const isMobileProject = testInfo.project.name.includes("iphone");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await ensureRequestDetail(page);

    await expect(page.locator("body")).not.toContainText("Your AI company");
    await expect(page.locator("body")).not.toContainText("YOUR COMPANY");

    const settings = page.getByRole("button", { name: "設定" });
    await expect(settings).toBeVisible();
    await settings.click();
    await expect(page.locator("#settings-dialog")).toBeVisible();
    await page.locator("#settings-dialog button[data-close-dialog]").first().click();
    await expect(page.locator("#settings-dialog")).toBeHidden();

    const composerResize = await page.locator("#composer-input").evaluate((element) => getComputedStyle(element).resize);
    expect(composerResize).toBe("none");

    const send = page.locator("#composer-send");
    await expect(send).toHaveAttribute("aria-label", "送信");
    await expect(send.locator(".composer-send-icon")).toHaveText("↑");

    await page.emulateMedia({ colorScheme: "dark" });
    await expect(page.locator("#workspace-view")).toBeVisible();
    await expect(page.locator(".thread-composer")).toBeVisible();

    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.getByRole("button", { name: "送信" })).toBeEnabled();

    if (isMobileProject) {
      const menu = page.locator("#menu-button");
      await menu.click();
      await page.locator("#nav-employees-home").click();
      await expect(page.getByRole("heading", { name: "対応が必要" })).toBeVisible();
      await expect(page.locator(".employee-status-section")).toBeHidden();
    } else {
      await page.emulateMedia({ colorScheme: "light" });
      await expect(page.locator("#workspace-view")).toBeVisible();
      await expect(page.locator(".employee-status-section")).toBeHidden();
    }

    if (!isMobileProject) {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.emulateMedia({ colorScheme: "light", reducedMotion: "no-preference" });
      await page.waitForTimeout(200);
      const menu = page.locator("#menu-button");
      await expect(menu).toBeVisible();
      await menu.click();
      await page.locator("#nav-employees-home").click();
      await expect(page.getByRole("heading", { name: "対応が必要" })).toBeVisible();
      await expect(page.locator(".employee-status-section")).toBeHidden();
    }
  } finally {
    await environment.stop();
  }
});

test("UI refinement round 2: composer, sequential clarifications @conversation @mobile", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("clarification_three");
  const isMobileProject = testInfo.project.name.includes("iphone");
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const q2 = "Browser Gate質問2：希望する文体は？";
  const q3 = "Browser Gate質問3：掲載先はどこですか？";
  const a1 = "初めてWorkCairnを使う人向けです。";
  const a2 = "やさしい説明文でお願いします。";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "clarification presentation gate用の短い成果物を作ってください");
    await expect(page.locator('#composer-input[placeholder="回答を入力..."]')).toBeVisible({ timeout: 45_000 });

    const timeline = page.locator("#activity-timeline");
    const composerInput = page.locator("#composer-input");

    await expect(timeline).toContainText(q1);
    await expect(timeline).not.toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toBeEditable();
    await expect(composerInput).toHaveValue("");
    await expectTextOnceInTimeline(page, q1);

    await composerInput.fill(a1);
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    await expectTextOnceInTimeline(page, q2);

    await composerInput.fill(a2);
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q2);
    await expect(timeline).toContainText(a2);
    await expect(timeline).toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    await expectTextOnceInTimeline(page, q3);

    await page.waitForTimeout(5500);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).toContainText(a2);
    await expect(timeline.getByText(q3)).toHaveCount(1);
    await expect(timeline.getByText(q1)).toHaveCount(1);

    if (!isMobileProject) {
      await expect(page.locator(".employee-status-section")).toBeHidden();

      const layoutRatio = await page.locator(".workspace-layout").evaluate((element) => {
        const columns = getComputedStyle(element).gridTemplateColumns.split(" ");
        const left = parseFloat(columns[0]);
        const right = parseFloat(columns[1]);
        return right / (left + right);
      });
      expect(layoutRatio).toBeGreaterThan(0.62);
    } else {
      await expect(page.locator(".employee-status-section")).toBeHidden();
    }

    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.getByRole("button", { name: "送信" })).toBeEnabled();
  } finally {
    await environment.stop();
  }
});

test("selected request detail persists through polling, completion, and failure @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "初めての利用者向けにWorkCairnの紹介文を作り、別のAIで確認してください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);

    await page.waitForTimeout(5500);
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#activity-timeline")).toBeVisible();

    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    await expect(page.locator("#request-detail-view")).toBeVisible();
    const isMobile = await page.locator("#menu-button").isVisible();
    if (isMobile) {
      await expect(page.locator("#request-list-view")).toBeHidden();
    } else {
      await expect(page.locator("#request-list-view")).toBeHidden();
      await expect(page.locator("#request-detail-view")).toBeVisible();
      await expect(page.locator(".requests-pane")).toHaveClass(/has-detail/);
    }
  } finally {
    await environment.stop();
  }
});

// TestClarificationAnswersCommitIncrementally is the browser-level
// regression for the WorkCairn clarification UX semantic gap: each
// composer submission durably commits its own answer immediately (no
// batching in local UI state), reload restores exactly the answered
// history plus the single pending question, and Plan generation (a real
// Provider call) only runs once after the final answer -- never once per
// question.
test("clarification answers commit incrementally: durable per-answer Turns, reload restoration, single Plan regeneration @critical @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("clarification_three");
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const q2 = "Browser Gate質問2：希望する文体は？";
  const q3 = "Browser Gate質問3：掲載先はどこですか？";
  const a1 = "初めてWorkCairnを使う人向けです。";
  const a2 = "やさしい説明文でお願いします。";
  const a3 = "社内ブログに掲載します。";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "clarification incremental gate用の短い成果物を作ってください");
    await expect(page.locator('#composer-input[placeholder="回答を入力..."]')).toBeVisible({ timeout: 45_000 });

    const timeline = page.locator("#activity-timeline");
    const composerInput = page.locator("#composer-input");

    // 1/2. Only Q1 is shown; Q2/Q3 are not revealed yet.
    await expect(timeline).toContainText(q1);
    await expect(timeline).not.toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    // 14. The composer is always exactly one input/send pair.
    await expect(page.locator("#composer-input")).toHaveCount(1);
    await expect(page.locator("#composer-send")).toHaveCount(1);
    // Initial CEO Plan Intent generation: exactly 1 Provider call so far.
    expect(environment.provider.calls.length).toBe(1);

    // 3. Send A1 from the composer.
    await composerInput.fill(a1);
    await page.locator("#composer-send").click();
    // 4. Q1/A1 remain in history. 5. Q2 is the only next pending question.
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    await expect(page.locator("#composer-input")).toHaveCount(1);
    // Answering Q1 alone must not trigger Plan regeneration.
    expect(environment.provider.calls.length).toBe(1);

    // 15. Polling (every 5s) must not lose the current pending question.
    await page.waitForTimeout(5500);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(timeline).toContainText(q2);
    await expect(page.locator("#composer-input")).toHaveCount(1);
    expect(environment.provider.calls.length).toBe(1);

    // 6/7. Reload restores Q1/A1/Q2 -- durable Turns, not client state.
    await page.reload();
    await ensureRequestDetail(page);
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    await expect(page.locator("#composer-input")).toHaveCount(1);
    expect(environment.provider.calls.length).toBe(1);

    // 8. Send A2.
    await composerInput.fill(a2);
    await page.locator("#composer-send").click();
    // 9. Q1,A1,Q2,A2,Q3 now visible in that order (checked precisely below).
    await expect(timeline).toContainText(q3);
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");
    await expect(composerInput).toHaveValue("");
    expect(environment.provider.calls.length).toBe(1);

    // 10. Send A3 (final answer).
    await composerInput.fill(a3);
    await page.locator("#composer-send").click();
    // 11. Clarification is complete; Plan generation is reached.
    await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });
    await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 60_000 });
    // 13. Exactly one additional Provider call for the final Plan
    // regeneration -- never one per answered question.
    expect(environment.provider.calls.length).toBe(2);

    // 12. Final Conversation Projection order: Q1,A1,Q2,A2,Q3,A3.
    const clarificationNodes = page.locator("#activity-timeline article.msg-clarification, #activity-timeline article.msg-user");
    const texts = await clarificationNodes.allTextContents();
    const expectedOrder = [q1, a1, q2, a2, q3, a3];
    const observedOrder = texts
      .map((text) => expectedOrder.findIndex((needle) => text.includes(needle)))
      .filter((index) => index !== -1);
    expect(observedOrder).toEqual([0, 1, 2, 3, 4, 5]);
  } finally {
    await environment.stop();
  }
});

test("new request draft opens empty without flashing prior session content @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const sessionAText = "session A flash gate用の依頼です";
  const sessionBText = "session B flash gate用の新しい依頼です";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, sessionAText);
    await expect(page.locator(".thread-title")).toContainText("session A flash gate");
    await expect(page.locator("#activity-timeline")).toContainText(sessionAText);

    await openNewRequestFromUI(page);
    await expect(page.locator(".thread-title")).toContainText("新しい依頼");
    await expect(page.locator("#activity-timeline")).not.toContainText(sessionAText);
    await expect(page.locator("#activity-timeline")).toContainText("依頼内容を入力してください");
    await expect(page.locator("#composer-input")).toHaveValue("");
    await expect(page.locator("#composer-input")).toHaveAttribute("placeholder", "依頼内容を入力...");

    await page.waitForTimeout(5500);
    await expect(page.locator(".thread-title")).toContainText("新しい依頼");
    await expect(page.locator("#activity-timeline")).not.toContainText(sessionAText);

    await page.locator("#composer-input").fill(sessionBText);
    await page.locator("#composer-send").click();
    await expect(page.locator("#activity-timeline")).toContainText(sessionBText, { timeout: 45_000 });
    await expect(page.locator(".thread-title")).toContainText("session B flash gate");
    await expect(page.locator("#activity-timeline")).not.toContainText(sessionAText);
  } finally {
    await environment.stop();
  }
});

test("desktop detail view hides request list body until explicit back navigation @conversation", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.includes("iphone"), "Desktop-only list/detail toggle");
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "detail list toggle gate用の依頼です");
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#request-list-view")).toBeHidden();
    await expect(page.locator("#session-list")).toBeHidden();
    await page.locator("#back-to-list-button").click();
    await expect(page.locator("#request-list-view")).toBeVisible();
    await expect(page.locator("#request-detail-view")).toBeHidden();
  } finally {
    await environment.stop();
  }
});

test("icon-only request list header exposes accessible new and refresh actions @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.locator("#back-to-list-button").click();
    const create = page.locator("#new-request-button");
    const refresh = page.locator("#refresh-button");
    await expect(create).toHaveAttribute("aria-label", "新規作成");
    await expect(refresh).toHaveAttribute("aria-label", "更新");
    await expect(create).not.toContainText("新規作成");
    await expect(refresh).not.toContainText("更新");
    await expect(create.locator(".icon-action-svg")).toBeVisible();
    await expect(refresh.locator(".icon-action-svg")).toBeVisible();
  } finally {
    await environment.stop();
  }
});

test("composer state copy appears once across clarification, workflow, plan, failure, and completion @critical @conversation @failure", async ({ page }) => {
  const workflowCopy = "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。";
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const clarificationEnv = await startBrowserEnvironment("clarification_three");
  try {
    await pairThroughUI(page, clarificationEnv.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "composer dedupe gate用の短い成果物を作ってください");
    await expect(page.locator('#composer-input[placeholder="回答を入力..."]')).toBeVisible({ timeout: 45_000 });
    await expectTextOnceInTimeline(page, q1);
    await expect(page.locator("#composer-status")).toHaveClass(/visually-hidden/);
    await expect(page.locator("#composer-status")).toBeEmpty();
  } finally {
    await clarificationEnv.stop();
  }

  const happyEnv = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, happyEnv.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "composer dedupe happy path gate用の依頼です");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });
    await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible();
    await expect(page.locator("#composer-input")).toHaveValue("");
    await expect(page.locator("#quick-replies")).toContainText("この内容で進める");
    await approvePlanAndExecute(page);
    const composerInput = page.locator("#composer-input");
    // workflowCopy is now the ephemeral live-status TIMELINE sentence
    // (never duplicated into the composer, which shows only the short
    // "仕事を進めています" label already asserted inside
    // approvePlanAndExecute). Once the workflow reaches a terminal state
    // and canonical entries replace it, it must not remain in the timeline.
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    await expect(composerInput).toHaveValue("完了しました");
    await expect(page.locator("#activity-timeline")).not.toContainText(workflowCopy);
  } finally {
    await happyEnv.stop();
  }

  const failureEnv = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, failureEnv.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "composer dedupe failure gate用の依頼です");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。安全な表示を確認します。");
    await approvePlanAndExecute(page);
    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });
    await expect(page.locator("#composer-input")).toHaveValue("判断が必要です");
    await expect(page.locator("#quick-replies")).toContainText("処理を再確認");
  } finally {
    await failureEnv.stop();
  }
});

test("processing live feedback appears in the timeline while a request is submitted and clears without duplication @critical @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "処理中feedback gate用の依頼です");
    const timeline = page.locator("#activity-timeline");
    const composerInput = page.locator("#composer-input");
    // The pending window is transient (Mock Provider delay only), so check
    // both conditions from one atomic snapshot rather than two sequential
    // Playwright assertions -- otherwise the state can legitimately move on
    // to the terminal question between the two separate polls.
    await page.waitForFunction(() => {
      const timelineText = document.querySelector("#activity-timeline")?.textContent || "";
      return timelineText.includes("依頼内容を確認して、進め方を整理しています");
    }, { timeout: 10_000 });
    // The composer shows no processing/"考え中" text at all -- disabled and
    // blank, never a paraphrase of the timeline's live-status sentence.
    await expect(composerInput).toHaveValue("");
    await expect(composerInput).not.toBeEditable();
    for (const phrase of ["進め方を整理しています", "依頼内容を確認して", "処理中", "整理しています"]) {
      expect(await composerInput.inputValue()).not.toContain(phrase);
    }
    await waitForPlanOrClarification(page);
    // Once the terminal question/Plan lands, the ephemeral sentence must
    // not remain alongside it.
    await expect(timeline).not.toContainText("依頼内容を確認して、進め方を整理しています");
  } finally {
    await environment.stop();
  }
});

test("clarification answer processing shows live feedback in the timeline and clears once the next result lands @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("clarification_three");
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const q2 = "Browser Gate質問2：希望する文体は？";
  const q3 = "Browser Gate質問3：掲載先はどこですか？";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "回答処理feedback gate用の依頼です");
    const composerInput = page.locator("#composer-input");
    const timeline = page.locator("#activity-timeline");
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...", { timeout: 45_000 });
    await expect(timeline).toContainText(q1);

    // Q1/Q2 answers never reach the Provider (see the incremental
    // clarification round), so they resolve too fast to reliably observe a
    // live-status frame -- send them to reach Q3, the final answer that
    // does trigger a real Provider round-trip with an observable delay.
    await composerInput.fill("読者は初めての利用者です。");
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q2, { timeout: 15_000 });
    await composerInput.fill("やさしい文体でお願いします。");
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q3, { timeout: 15_000 });

    await composerInput.fill("社内ブログに掲載します。");
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText("回答を確認して、続きの進め方を整理しています", { timeout: 10_000 });
    // The composer shows no processing/"考え中" text at all -- disabled and
    // blank, never a paraphrase of the timeline's live-status sentence.
    await expect(composerInput).toHaveValue("");
    await expect(composerInput).not.toBeEditable();
    for (const phrase of ["続きを整理しています", "回答を確認して", "処理中", "整理しています"]) {
      expect(await composerInput.inputValue()).not.toContain(phrase);
    }
    await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });
    await expect(timeline).not.toContainText("回答を確認して、続きの進め方を整理しています");
  } finally {
    await environment.stop();
  }
});

test("real-time workflow entries appear in an open detail without returning to the session list @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "リアルタイム反映gate用の依頼です");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);
    // The whole rest of this test deliberately never navigates back to the
    // session list -- Task assignment, Deliverable, Review, and completion
    // must all reach this open detail view purely through polling/monitor.
    const timeline = page.locator("#activity-timeline");
    await expect(timeline).toContainText("を割り当てました", { timeout: 30_000 });
    await expect(timeline).toContainText("成果物を作成しました", { timeout: 30_000 });
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    await expect(page.locator("#request-detail-view")).toBeVisible();
  } finally {
    await environment.stop();
  }
});

test("Dark Mode Plan embed keeps readable contrast instead of white-on-white @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await page.emulateMedia({ colorScheme: "dark" });
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "Dark Mode Plan contrast gate用の依頼です");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });
    const attach = page.locator(".msg-embed-plan .msg-attach").first();
    await expect(attach).toBeVisible();
    const background = await attach.evaluate((element) => getComputedStyle(element).backgroundColor);
    const nameNode = attach.locator(".msg-attach-list strong").first();
    const textColor = await nameNode.evaluate((element) => getComputedStyle(element).color);
    // The exact dark --surface-strong value the fix integrates with,
    // locking in the regression (previously a hardcoded near-white).
    expect(background).toBe("rgb(29, 36, 32)");
    expect(background).not.toBe("rgb(255, 255, 255)");
    expect(textColor).not.toBe(background);
  } finally {
    await environment.stop();
  }
});

// .button.primary and .button.danger keep a solid semantic-color
// background (var(--green)/var(--red)) in dark mode, which goes pastel-
// light there by design (the same variables read fine as text-on-dark-
// surface elsewhere). Paired with the buttons' own fixed white text, that
// used to be unreadable. .button.danger has no live call site in the
// current UI, so it and the reachable .button.primary/new-request-button
// are all checked the same way: read the real cascade the browser computes
// for each class combination, rather than only exercising what a user can
// currently click through to.
// WCAG relative-luminance contrast ratio -- checks actual readability (the
// original bug was near-white text on a light-pastel background, two
// visually distinct but still unreadable colors, so a plain
// color !== background check would not have caught it) without pinning to
// any specific theme RGB value.
function contrastRatio({ color, background }) {
  const luminance = (rgb) => {
    const [r, g, b] = rgb.match(/[\d.]+/g).map(Number).map((channel) => {
      const c = channel / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const lightest = Math.max(luminance(color), luminance(background));
  const darkest = Math.min(luminance(color), luminance(background));
  return (lightest + 0.05) / (darkest + 0.05);
}

test("primary, danger, and new-request buttons keep readable contrast in both themes @conversation", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const readColors = (classNames) => page.evaluate((names) => {
    const probe = document.createElement("button");
    probe.className = names;
    probe.textContent = "probe";
    probe.style.position = "fixed";
    probe.style.left = "-9999px";
    document.body.appendChild(probe);
    const style = getComputedStyle(probe);
    const result = { color: style.color, background: style.backgroundColor };
    probe.remove();
    return result;
  }, classNames);
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await ensureRequestList(page);
    const newRequestButton = page.locator("#new-request-button");
    await expect(newRequestButton).toBeVisible();

    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? "dark" : "light" });
      const label = dark ? "dark" : "light";
      expect(contrastRatio(await readColors("button primary")), `primary in ${label} mode`).toBeGreaterThan(3);
      // .button.danger has no live call site in the current UI (grep
      // confirms) -- checked directly via the real cascade so it stays
      // covered even though no user flow currently renders it.
      expect(contrastRatio(await readColors("button danger")), `danger in ${label} mode`).toBeGreaterThan(3);
      const newRequestColors = await newRequestButton.evaluate((element) => {
        const style = getComputedStyle(element);
        return { color: style.color, background: style.backgroundColor };
      });
      const newRequestRatio = contrastRatio(newRequestColors);
      if (dark) {
        // The bug this Checkpoint fixes: dark mode previously paired
        // near-white text with a light-pastel background here.
        expect(newRequestRatio, `new-request-button in dark mode`).toBeGreaterThan(3);
      } else {
        // Light mode is untouched by this Checkpoint (the fix lives only
        // inside the dark-mode media block) and already runs close to this
        // floor before any of these changes -- just guard against a future
        // edit accidentally collapsing it toward unreadable, not against
        // this pre-existing, out-of-scope borderline value.
        expect(newRequestRatio, `new-request-button in light mode`).toBeGreaterThan(2);
      }
    }
  } finally {
    await page.emulateMedia({ colorScheme: null });
    await environment.stop();
  }
});

test("session list scrollbar does not overlap the row menu button once the list overflows @conversation", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.includes("iphone"), "Scrollbar overlap is a desktop pointer-scrollbar concern; mobile uses touch overlay scrolling with no reserved gutter");
  const environment = await startBrowserEnvironment("session_list_overflow");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    for (let index = 0; index < 16; index += 1) {
      await startRequest(page, `Scroll overlap gate ${index}`);
      // This fixture's Plan always resolves with no clarification question
      // (see session_list_overflow), so wait directly for the Plan button
      // rather than waitForPlanOrClarification's generic helper -- that
      // helper first waits out a full 45s clarification-selector timeout
      // per call when clarification never appears, which is far too slow
      // repeated 16 times in one test.
      await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 15_000 });
    }
    await ensureRequestList(page);
    const list = page.locator("#session-list");
    await expect(list.locator(".session-row")).toHaveCount(16, { timeout: 20_000 });
    const overflowing = await list.evaluate((element) => element.scrollHeight > element.clientHeight);
    expect(overflowing).toBeTruthy();

    const overlap = await list.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const scrollbarWidth = element.offsetWidth - element.clientWidth;
      const contentRightEdge = rect.right - scrollbarWidth;
      const buttons = [...element.querySelectorAll(".session-menu-button")];
      return buttons.some((button) => button.getBoundingClientRect().right > contentRightEdge + 1);
    });
    expect(overlap).toBeFalsy();

    await switchSessionFilter(page, "削除済み");
    await expect(page.locator("#session-list")).toBeVisible();
  } finally {
    await environment.stop();
  }
});

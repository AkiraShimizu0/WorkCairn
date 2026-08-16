import { expect, test } from "@playwright/test";
import { join } from "node:path";
import { pathExists, startBrowserEnvironment } from "./support/harness.mjs";

async function pairThroughUI(page, daemon) {
  let forcedRemoteStatus = false;
  const statusRoute = async (route) => {
    if (!forcedRemoteStatus) {
      forcedRemoteStatus = true;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ mode: "trusted_lan", authenticated: false, local_setup_available: false })
      });
      return;
    }
    await route.continue();
  };
  await page.route("**/v1/local-access/status", statusRoute);
  await page.goto(daemon.baseURL);
  await expect(page.locator("#pairing-view")).toBeVisible();
  await page.locator("#pairing-code").fill(daemon.pairingCode);
  await page.getByRole("button", { name: "このMacと接続" }).click();
  await expect(page.locator("#workspace-view")).toBeVisible();
  const cookies = await page.context().cookies(daemon.baseURL);
  expect(cookies.some((cookie) => cookie.name === "workspace_local_access" && cookie.httpOnly && cookie.sameSite === "Strict")).toBeTruthy();
  await page.unroute("**/v1/local-access/status", statusRoute);
}

async function completeFirstRun(page) {
  await expect(page.locator("#setup-dialog")).toBeVisible();
  await expect(page.locator("#setup-content")).toContainText("Product Manager");
  await expect(page.locator("#setup-content")).toContainText("Content Writer");
  await expect(page.locator("#setup-content")).toContainText("QA Engineer");
  await page.getByRole("button", { name: "最初のAIチームを確認" }).click();
  await expect(page.locator("#setup-content")).toContainText("最小のAIチームを作成しますか？");
  await page.getByRole("button", { name: "承認してセットアップ" }).click();
  await expect(page.getByRole("button", { name: "会社を始める" })).toBeVisible();
  await page.getByRole("button", { name: "会社を始める" }).click();
  await expect(page.locator("#request-detail-view")).toBeVisible();
  await expect(page.locator("#composer-input")).toBeVisible();
}

async function startRequest(page, requestText) {
  const composer = page.locator("#composer-input");
  let draftReady = await composer.isVisible()
    && (await composer.getAttribute("placeholder")) === "依頼内容を入力...";
  if (!draftReady) {
    const newRequest = page.getByRole("button", { name: "＋ 新規作成" });
    if (await newRequest.isVisible()) {
      await newRequest.click();
    } else {
      const menu = page.locator("#menu-button");
      if (await menu.isVisible()) {
        await menu.click();
        await page.locator("#nav-new-request").click();
      }
    }
  }
  await expect(page.locator("#request-detail-view")).toBeVisible();
  await composer.fill(requestText);
  await page.locator("#composer-send").click();
  await expect(page.locator("#composer-status")).toBeVisible();
}

async function waitForPlanOrClarification(page) {
  try {
    await page.waitForSelector('#composer-input[placeholder="回答を入力..."]', { timeout: 45_000 });
    return "clarification";
  } catch {}
  await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 45_000 });
  return "plan";
}

async function answerClarificationIfNeeded(page, answerText = "はい。初めてWorkCairnを使う人向けです。") {
  const clarification = page.locator('#composer-input[placeholder="回答を入力..."]');
  if (!(await clarification.count())) return false;
  await clarification.fill(answerText);
  await expect(clarification).toHaveValue(answerText);
  await expect(page.locator("#composer-send")).toBeEnabled();
  await page.locator("#composer-send").click();
  await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 60_000 });
  return true;
}

async function approvePlanAndExecute(page) {
  await expect(page.locator(".msg-embed-plan")).toBeVisible();
  await expect(page.getByRole("button", { name: "進め方の作成を承認" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "実行内容を確認" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "承認して実行" })).toHaveCount(0);
  const approve = page.getByRole("button", { name: "この内容で進める" });
  await expect(approve).toHaveCount(1);
  await approve.click();
  await expect(page.locator("#composer-status")).toContainText("Makerの成果物作成");
  await expect(approve).toHaveCount(0);
}

async function ensureRequestDetail(page) {
  const detail = page.locator("#request-detail-view");
  try {
    await expect(detail).toBeVisible({ timeout: 8_000 });
    return;
  } catch {}
  const menu = page.locator("#menu-button");
  if (await menu.isVisible()) {
    await menu.click();
    const current = page.locator("#nav-current-request");
    if (await current.isVisible()) {
      await current.click();
      await expect(detail).toBeVisible();
      return;
    }
    await page.locator("#nav-request-list").click();
    await page.locator("#session-list .session-item").first().click();
    await expect(detail).toBeVisible();
    return;
  }
  await page.locator("#session-list .session-item").first().click();
  await expect(detail).toBeVisible();
}

async function approvePlanAndWorkflow(page) {
  await approvePlanAndExecute(page);
}

test("Public Beta browser happy path survives polling, reload, and daemon restart", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "初めての利用者向けにWorkCairnの紹介文を作り、別のAIで確認してください");

    await expect(page.locator("#composer-status")).toContainText("進め方を準備しています", { timeout: 20_000 });
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
    // entry point for a new request is the request list's own "＋ 新規作成".
    await expect(page.getByRole("button", { name: "完了を確認" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "新しい仕事を依頼" })).toHaveCount(0);

    // The completed Deliverable is reachable directly from its own
    // "成果物を作成しました" message via the existing read-only Task
    // evidence projection -- not a UI-composed copy of the Deliverable.
    const deliverableToggle = page.locator("#activity-timeline").getByRole("button", { name: "成果物を見る" }).first();
    await expect(deliverableToggle).toBeVisible();
    await deliverableToggle.click();
    await expect(page.locator("#activity-timeline .deliverable-preview").first()).toContainText("WorkCairn紹介文");

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

test("typed Provider failure is restored from durable Ledger evidence", async ({ browser, page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  let freshContext;
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "Provider failureの安全な表示を確認する成果物を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。安全な表示を確認します。");
    await approvePlanAndExecute(page);

    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });
    await expect(page.locator("#activity-timeline")).toContainText("review_provider");
    await expect(page.locator("#activity-timeline")).toContainText("req_browser_rate_limit_001");
    await expect(page.locator("#activity-timeline")).not.toContainText("sanitized fixture failure");

    const workflowCommand = commands.find((command) => command.operation === "interaction.plan.approve_and_execute");
    expect(workflowCommand?.command_id).toBeTruthy();
    const ledger = await page.evaluate(async (commandID) => {
      const response = await fetch(`/v1/commands/${encodeURIComponent(commandID)}?scope=workspace`);
      const payload = await response.json();
      return payload.result;
    }, workflowCommand.command_id);
    expect(ledger.state).toBe("partial_failure");
    expect(ledger.failure.details.code).toBe("PROVIDER_RATE_LIMITED");
    expect(ledger.failure.details.provider.request_id).toBe("req_browser_rate_limit_001");

    freshContext = await browser.newContext();
    await freshContext.addInitScript(() => {
      try { Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined }); } catch {}
    });
    const freshPage = await freshContext.newPage();
    await pairThroughUI(freshPage, environment.daemon);
    await ensureRequestDetail(freshPage);
    await expect(freshPage.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED");
    await expect(freshPage.locator("#activity-timeline")).toContainText("review_provider");
    await expect(freshPage.locator("#activity-timeline")).toContainText("req_browser_rate_limit_001");
    await expect(freshPage.locator("#activity-timeline")).toContainText("自動retryせず");
    const detailsToggle = freshPage.locator("#activity-timeline").getByRole("button", { name: "ⓘ エラーの詳細" }).last();
    await detailsToggle.click();
    const copyButton = freshPage.locator("#activity-timeline").getByRole("button", { name: "診断情報をコピー" }).last();
    await expect(copyButton).toBeVisible();
    await copyButton.click();
    await expect(freshPage.locator("#toast")).toContainText(/コピー|詳細を選択/);
  } finally {
    if (freshContext) await freshContext.close();
    await environment.stop();
  }
});

test("Claude connection always leaves in-flight state on terminal outcome", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  let attempt = 0;
  const providerStatusRoute = async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        version: "workspace-provider-status.v1", ok: true,
        result: { version: "workspace-provider-status.v1", provider: "anthropic", configured: false, selection_mode: "automatic", missing: ["credential"], invalid: [] }
      })
    });
  };
  const connectRoute = async (route) => {
    attempt += 1;
    await new Promise((resolve) => setTimeout(resolve, 250));
    if (attempt === 1) {
      await route.fulfill({
        status: 422,
        contentType: "application/json",
        body: JSON.stringify({
          version: "workspace-provider-status.v1", ok: false,
          error: {
            code: "PROVIDER_CONNECTION_SETUP_FAILED", stage: "provider_connection_setup",
            details: { substage: "keychain_write", category: "keychain_setup_timeout" }
          }
        })
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        version: "workspace-provider-status.v1", ok: true,
        result: { version: "workspace-provider-status.v1", provider: "anthropic", configured: true, selection_mode: "automatic", missing: [], invalid: [] }
      })
    });
  };
  try {
    await page.route("**/v1/provider-status", providerStatusRoute);
    await page.route("**/v1/local-setup/claude", connectRoute);
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("#setup-dialog")).toBeVisible();

    const connect = page.locator("#setup-content").getByRole("button", { name: "MacでClaudeを接続" });
    await connect.click();
    await expect(page.locator("#busy-overlay")).toBeVisible();
    await expect(page.locator("#busy-overlay")).toBeHidden();
    await expect(page.locator("#setup-content")).toContainText("Claudeの接続設定を完了できませんでした");

    await connect.click();
    await expect(page.locator("#busy-overlay")).toBeVisible();
    await expect(page.locator("#busy-overlay")).toBeHidden();
    await expect(page.locator("#setup-content")).toContainText("Connected");
    expect(attempt).toBe(2);
  } finally {
    await environment.stop();
  }
});

async function generatePlanThroughClarification(page) {
  await waitForPlanOrClarification(page);
  await answerClarificationIfNeeded(page);
  await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 20_000 });
}

async function composerMetrics(page) {
  return page.evaluate(() => {
    const composer = document.querySelector("#thread-composer");
    const detail = document.querySelector("#request-detail-view");
    const pane = document.querySelector("#requests-pane");
    const anchor = detail && !detail.hidden ? detail : pane;
    if (!composer || !anchor || anchor.hidden) return null;
    const composerRect = composer.getBoundingClientRect();
    const anchorRect = anchor.getBoundingClientRect();
    if (!composerRect.height || !anchorRect.height) return null;
    return {
      composerBottom: composerRect.bottom,
      anchorBottom: anchorRect.bottom,
      viewportBottom: window.innerHeight,
      composerDelta: Math.abs(anchorRect.bottom - composerRect.bottom),
    };
  });
}

async function seedTimelineMessages(page, count, prefix = "seed") {
  await page.evaluate(({ count, prefix }) => {
    const timeline = document.querySelector("#activity-timeline");
    if (!timeline) return;
    for (let index = 0; index < count; index += 1) {
      const article = document.createElement("article");
      article.className = "msg msg-system";
      article.textContent = `${prefix} ${index} `.repeat(24);
      timeline.appendChild(article);
    }
  }, { count, prefix });
}

async function assertComposerBottomStable(page, label, tolerance = 2) {
  const metrics = await composerMetrics(page);
  expect(metrics, `${label}: composer metrics missing`).not.toBeNull();
  expect(metrics.composerDelta, `${label}: composer should hug requests pane bottom`).toBeLessThanOrEqual(tolerance);
  return metrics.composerBottom;
}

test("composer stays pinned to the requests pane bottom across conversation shapes", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
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

test("composer stays pinned on mobile and plan tasks show canonical titles", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
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

test("failure technical details do not move the composer", async ({ page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await ensureRequestDetail(page);
    await startRequest(page, "Provider failureの安全な表示を確認する成果物を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。安全な表示を確認します。");
    await approvePlanAndExecute(page);
    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });

    const before = await assertComposerBottomStable(page, "failure before details");
    const toggle = page.locator("#activity-timeline .msg-info-toggle").last();
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect(page.locator("#activity-timeline .msg-technical-panel").last()).toBeVisible();
    const after = await assertComposerBottomStable(page, "failure after details");
    expect(Math.abs(after - before)).toBeLessThanOrEqual(2);
  } finally {
    await environment.stop();
  }
});

test("conversation projection renders canonical chat categories", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
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

test("employee visual section stays separate from selected request chat", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    const menu = page.locator("#menu-button");
    const isMobile = await menu.isVisible();
    if (isMobile) {
      await menu.click();
      await page.locator("#nav-employees-home").click();
    }
    await expect(page.locator(".shared-office")).toBeVisible();
    await expect(page.locator(".shared-office-station").first()).toBeVisible();
    await expect(page.getByRole("heading", { name: "社内の動き" })).toBeVisible();
    await startRequest(page, "りんごについて100文字程度で説明して");
    await expect(page.locator("#activity-timeline")).toBeVisible();
    if (isMobile) {
      await expect(page.locator(".shared-office-stations")).toBeHidden();
    } else {
      await expect(page.locator(".shared-office")).toBeVisible();
    }
  } finally {
    await environment.stop();
  }
});

test("UI refinement: composer, settings, branding, themes, and office visual", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("happy_path");
  const isMobileProject = testInfo.project.name.includes("iphone");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
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
      await expect(page.getByRole("heading", { name: "AI会社の様子" })).toBeVisible();
      await expect(page.locator(".employee-compact-row").first()).toBeVisible();
      await expect(page.locator(".station-scene").first()).toBeHidden();
    } else {
      await page.emulateMedia({ colorScheme: "light" });
      await expect(page.locator("#workspace-view")).toBeVisible();
      await expect(page.locator(".shared-office-station").first()).toBeVisible();
      await expect(page.locator(".station-scene").first()).toBeVisible();
      await expect(page.locator(".office-worker").first()).toBeVisible();
    }

    if (!isMobileProject) {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.emulateMedia({ colorScheme: "light", reducedMotion: "no-preference" });
      await page.waitForTimeout(200);
      const menu = page.locator("#menu-button");
      await expect(menu).toBeVisible();
      await menu.click();
      await page.locator("#nav-employees-home").click();
      await expect(page.getByRole("heading", { name: "AI会社の様子" })).toBeVisible();
      await expect(page.locator(".employee-compact-row").first()).toBeVisible();
      await expect(page.locator(".station-scene").first()).toBeHidden();
    }
  } finally {
    await environment.stop();
  }
});

test("UI refinement round 2: composer, sequential clarifications, shared office", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("clarification_three");
  const isMobileProject = testInfo.project.name.includes("iphone");
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const q2 = "Browser Gate質問2：希望する文体は？";
  const q3 = "Browser Gate質問3：掲載先はどこですか？";
  const a1 = "初めてWorkCairnを使う人向けです。";
  const a2 = "やさしい説明文でお願いします。";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "clarification presentation gate用の短い成果物を作ってください");
    await expect(page.locator('#composer-input[placeholder="回答を入力..."]')).toBeVisible({ timeout: 45_000 });

    const timeline = page.locator("#activity-timeline");
    const composerStatus = page.locator("#composer-status");
    const composerInput = page.locator("#composer-input");

    await expect(timeline).toContainText(q1);
    await expect(timeline).not.toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    // composer-status names the single next unanswered question directly
    // (Next() only ever names one question -- see the incremental
    // clarification commit design), not a "質問 N / M" progress counter.
    await expect(composerStatus).toContainText(q1);
    await expect(composerStatus).not.toContainText("回答を入力");
    await expect(composerInput).toHaveAttribute("placeholder", "回答を入力...");

    await composerInput.fill(a1);
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerStatus).toContainText(q2);

    await composerInput.fill(a2);
    await page.locator("#composer-send").click();
    await expect(timeline).toContainText(q2);
    await expect(timeline).toContainText(a2);
    await expect(timeline).toContainText(q3);
    await expect(composerStatus).toContainText(q3);

    await page.waitForTimeout(5500);
    await expect(composerStatus).toContainText(q3);
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).toContainText(a2);
    await expect(timeline.getByText(q3)).toHaveCount(1);
    await expect(timeline.getByText(q1)).toHaveCount(1);

    if (!isMobileProject) {
      await expect(page.locator(".shared-office")).toHaveCount(1);
      await expect(page.locator(".office-zone")).toHaveCount(0);
      await expect(page.locator(".shared-office-station")).toHaveCount(3);
      await expect(page.locator(".office-worker.pose-idle").first()).toBeVisible();

      const layoutRatio = await page.locator(".workspace-layout").evaluate((element) => {
        const columns = getComputedStyle(element).gridTemplateColumns.split(" ");
        const left = parseFloat(columns[0]);
        const right = parseFloat(columns[1]);
        return right / (left + right);
      });
      expect(layoutRatio).toBeGreaterThan(0.58);

      await page.emulateMedia({ colorScheme: "light" });
      const workerTone = await page.locator(".worker-body").first().evaluate((element) => getComputedStyle(element).backgroundColor);
      expect(workerTone).not.toBe("rgba(0, 0, 0, 0)");

      await page.emulateMedia({ colorScheme: "dark" });
      const darkWorkerTone = await page.locator(".worker-head").first().evaluate((element) => getComputedStyle(element).backgroundColor);
      expect(darkWorkerTone).not.toBe("rgba(0, 0, 0, 0)");
    } else {
      await expect(page.locator(".employee-compact-row")).toHaveCount(3);
    }

    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.getByRole("button", { name: "送信" })).toBeEnabled();
  } finally {
    await environment.stop();
  }
});

test("selected request detail persists through polling, completion, and failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "初めての利用者向けにWorkCairnの紹介文を作り、別のAIで確認してください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);

    await page.waitForTimeout(5500);
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#activity-timeline")).toBeVisible();

    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#request-list-view")).toBeHidden();
  } finally {
    await environment.stop();
  }
});

test("selected request detail persists after provider failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "Provider failureの安全な表示を確認する成果物を作ってください");
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page, "はい。安全な表示を確認します。");
    await approvePlanAndExecute(page);
    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });

    await page.waitForTimeout(5500);
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED");
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
test("clarification answers commit incrementally: durable per-answer Turns, reload restoration, single Plan regeneration", async ({ page }) => {
  const environment = await startBrowserEnvironment("clarification_three");
  const q1 = "Browser Gate質問1：対象読者は誰ですか？";
  const q2 = "Browser Gate質問2：希望する文体は？";
  const q3 = "Browser Gate質問3：掲載先はどこですか？";
  const a1 = "初めてWorkCairnを使う人向けです。";
  const a2 = "やさしい説明文でお願いします。";
  const a3 = "社内ブログに掲載します。";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await startRequest(page, "clarification incremental gate用の短い成果物を作ってください");
    await expect(page.locator('#composer-input[placeholder="回答を入力..."]')).toBeVisible({ timeout: 45_000 });

    const timeline = page.locator("#activity-timeline");
    const composerStatus = page.locator("#composer-status");
    const composerInput = page.locator("#composer-input");

    // 1/2. Only Q1 is shown; Q2/Q3 are not revealed yet.
    await expect(timeline).toContainText(q1);
    await expect(timeline).not.toContainText(q2);
    await expect(timeline).not.toContainText(q3);
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
    await expect(composerStatus).toContainText(q2);
    await expect(page.locator("#composer-input")).toHaveCount(1);
    // Answering Q1 alone must not trigger Plan regeneration.
    expect(environment.provider.calls.length).toBe(1);

    // 15. Polling (every 5s) must not lose the current pending question.
    await page.waitForTimeout(5500);
    await expect(composerStatus).toContainText(q2);
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    expect(environment.provider.calls.length).toBe(1);

    // 6/7. Reload restores Q1/A1/Q2 -- durable Turns, not client state.
    await page.reload();
    await ensureRequestDetail(page);
    await expect(timeline).toContainText(q1);
    await expect(timeline).toContainText(a1);
    await expect(timeline).toContainText(q2);
    await expect(timeline).not.toContainText(q3);
    await expect(composerStatus).toContainText(q2);
    await expect(page.locator("#composer-input")).toHaveCount(1);
    expect(environment.provider.calls.length).toBe(1);

    // 8. Send A2.
    await composerInput.fill(a2);
    await page.locator("#composer-send").click();
    // 9. Q1,A1,Q2,A2,Q3 now visible in that order (checked precisely below).
    await expect(timeline).toContainText(q3);
    await expect(composerStatus).toContainText(q3);
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

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
  await expect(page.locator("#new-request-inline")).toBeVisible();
}

async function startRequest(page, requestText) {
  await page.locator("#new-request-inline #request-text").fill(requestText);
  await page.getByRole("button", { name: /依頼する$/ }).click();
  await expect(page.locator("#new-request-inline")).toContainText("この依頼を開始してよろしいですか？");
  await page.getByRole("button", { name: "依頼を開始" }).click();
  await expect(page.getByRole("button", { name: "進め方の作成を承認" })).toBeVisible();
}

async function ensureRequestDetail(page) {
  const detail = page.locator("#request-detail-view");
  if (await detail.isVisible()) return;
  const menu = page.locator("#menu-button");
  if (await menu.isVisible()) {
    await menu.click();
    const current = page.locator("#nav-current-request");
    if (await current.isVisible()) {
      await current.click();
      return;
    }
    await page.locator("#nav-request-list").click();
    await page.locator("#session-list .session-item").first().click();
    return;
  }
  await page.locator("#session-list .session-item").first().click();
}

async function approvePlanAndWorkflow(page) {
  await expect(page.locator("#activity-timeline")).toContainText("この進め方で開始しますか？");
  await expect(page.locator("#activity-timeline")).not.toContainText("PROPOSED-");
  await page.getByRole("button", { name: "この進め方で始める" }).click();
  await expect(page.getByRole("button", { name: "実行内容を確認" })).toBeVisible();

  const limit = page.locator("#max-tasks");
  await limit.fill("8");
  await limit.focus();
  await page.waitForTimeout(5_500);
  await expect(limit).toHaveValue("8");
  expect(await limit.evaluate((element) => document.activeElement === element)).toBeTruthy();
  await page.getByRole("button", { name: "実行内容を確認" }).click();
  await expect(page.getByRole("button", { name: "承認して実行" })).toBeVisible();
  const approve = page.getByRole("button", { name: "承認して実行" });
  await approve.evaluate((element) => { element.click(); element.click(); });
  await expect(page.locator("#activity-timeline")).toContainText("仕事を進めています");
  await expect(page.getByRole("button", { name: "承認して実行" })).toHaveCount(0);
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

    const generate = page.getByRole("button", { name: "進め方の作成を承認" });
    await generate.evaluate((element) => { element.click(); element.click(); });
    await expect(page.locator("#activity-timeline")).toContainText("進め方を考えています");
    await expect(generate).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "確認したいことがあります" })).toBeVisible();
    expect(commands.filter((command) => command.operation === "interaction.plan.generate")).toHaveLength(1);

    const clarification = page.locator('textarea[name="answer-0"], #composer-input');
    await clarification.fill("途中まで入力した回答");
    await clarification.focus();
    await page.waitForTimeout(10_500);
    await expect(clarification).toHaveValue("途中まで入力した回答");
    expect(await clarification.evaluate((element) => document.activeElement === element)).toBeTruthy();
    await clarification.fill("はい。初めてWorkCairnを使う人向けです。");
    await page.getByRole("button", { name: "回答を送信" }).click();
    await expect(page.getByRole("button", { name: "進め方の作成を承認" })).toBeVisible();
    await page.getByRole("button", { name: "進め方の作成を承認" }).click();
    await expect(page.getByRole("button", { name: "この進め方で始める" })).toBeVisible();

    await approvePlanAndWorkflow(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible({ timeout: 45_000 });
    expect(commands.filter((command) => command.operation === "interaction.workflow.execute")).toHaveLength(1);
    expect(environment.provider.calls.map((call) => call.fixture)).toEqual([
      "ceo_intent_clarification", "ceo_intent_success", "task_execution_success",
      "review_request_changes", "revision_success", "re_review_approve"
    ]);
    expect(environment.provider.calls.map((call) => call.structured)).toEqual([true, true, false, true, false, true]);

    await expect(page.locator("#activity-timeline")).toContainText("修正をお願い");
    await expect(page.locator("#activity-timeline")).toContainText("修正しました");
    await expect(page.locator("#proof-of-work")).toContainText("2件の仕事");
    await expect(page.locator("#proof-of-work")).toContainText("Review: Approve");

    await page.reload();
    await expect(page.locator("#workspace-view")).toBeVisible();
    await ensureRequestDetail(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible();
    await expect(page.locator("#activity-timeline")).toContainText("完了しました");

    const restarted = await environment.restartDaemon();
    await pairThroughUI(page, restarted);
    await ensureRequestDetail(page);
    await expect(page.getByRole("heading", { name: "すべての仕事とReviewが完了しています" })).toBeVisible();
    await expect(page.locator("#proof-of-work")).toContainText("2件の仕事");
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
    await page.getByRole("button", { name: "進め方の作成を承認" }).click();
    await expect(page.getByRole("button", { name: "この進め方で始める" })).toBeVisible();
    await approvePlanAndWorkflow(page);

    await expect(page.locator("#activity-timeline")).toContainText("PROVIDER_RATE_LIMITED", { timeout: 30_000 });
    await expect(page.locator("#activity-timeline")).toContainText("review_provider");
    await expect(page.locator("#activity-timeline")).toContainText("req_browser_rate_limit_001");
    await expect(page.locator("#activity-timeline")).not.toContainText("sanitized fixture failure");

    const workflowCommand = commands.find((command) => command.operation === "interaction.workflow.execute");
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
    await freshPage.locator("#activity-timeline").getByRole("button", { name: "詳細をコピー" }).click();
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

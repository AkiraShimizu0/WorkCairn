import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  assertComposerBottomStable,
  completeFirstRunFast,
  composerMetrics,
  ensureRequestDetail,
  openNewRequestFromUI,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Typed FailureEnvelope surfacing: Provider failures, structured
// diagnostics, and composer/timeline behavior while a request fails.

test("typed Provider failure is restored from durable Ledger evidence @critical @failure", async ({ browser, page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  let freshContext;
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
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

test("failure technical details do not move the composer @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
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

test("selected request detail persists after provider failure @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("provider_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
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

test("steps.description failure lists indexed structured field shapes in diagnostics @failure", async ({ page }) => {
  const environment = await startBrowserEnvironment("ceo_plan_steps_description_failure");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.evaluate(() => {
      window.__lastCopied = "";
      if (navigator.clipboard?.writeText) {
        const original = navigator.clipboard.writeText.bind(navigator.clipboard);
        navigator.clipboard.writeText = async (text) => {
          window.__lastCopied = text;
          return original(text);
        };
      }
    });
    await startRequest(page, "steps.description shape diagnostic gate用の依頼です");
    const isMobile = await page.locator("#menu-button").isVisible();
    if (isMobile) {
      const menu = page.locator("#menu-button");
      await menu.click();
      await page.locator("#nav-request-list").click();
    }
    const sessionButton = page.locator("#session-list .session-item").filter({ hasText: "steps.description shape diagnostic gate用の依頼です" });
    await expect(sessionButton).toBeVisible({ timeout: 45_000 });
    await sessionButton.click();
    const composerFooter = page.locator("#request-detail-view .thread-footer");
    await expect(composerFooter.getByRole("button", { name: "ⓘ エラーの詳細" })).toBeVisible({ timeout: 15_000 });

    const detailsToggle = composerFooter.getByRole("button", { name: "ⓘ エラーの詳細" }).last();
    await detailsToggle.click();
    const panel = composerFooter.locator(".msg-technical-panel").last();
    await expect(panel).toContainText("Structured field shapes");
    await expect(panel).toContainText("steps.0.description");
    await expect(panel).toContainText("steps.1.description");
    await expect(panel).toContainText("present, string, blank");
    await expect(panel).toContainText("missing");
    const panelText = await panel.innerText();
    expect(panelText.indexOf("steps.0.description")).toBeLessThan(panelText.indexOf("steps.1.description"));
    await expect(panel).not.toContainText("Steps Shape Gate");
    await expect(panel).not.toContainText("required_role");

    const copyButton = composerFooter.getByRole("button", { name: "診断情報をコピー" }).last();
    await copyButton.click();
    const copied = await page.evaluate(() => window.__lastCopied || "");
    expect(copied).toContain("steps.0.description");
    expect(copied).toContain("steps.1.description");
    expect(copied.indexOf("steps.0.description")).toBeLessThan(copied.indexOf("steps.1.description"));
    expect(copied).not.toContain("Steps Shape Gate");
    expect(copied).not.toContain("required_role");
  } finally {
    await environment.stop();
  }
});

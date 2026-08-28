import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  completeFirstRun,
  ensureRequestDetail,
  pairThroughUI,
} from "./support/actions.mjs";

// First-run Setup Wizard and AI connection UI -- the only file that
// exercises Setup through the full UI walk (completeFirstRun) as the
// thing under test, rather than as unavoidable per-test scaffolding.

test("Claude connection always leaves in-flight state on terminal outcome @setup", async ({ page }) => {
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

    const connect = page.locator("#setup-content").getByRole("button", { name: "Claudeを接続" });
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

test("employee role labels render in natural Japanese while canonical roles stay internal-only @setup @office", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRun(page);
    await ensureRequestDetail(page);
    await expect(page.locator("body")).toContainText("企画担当");
    await expect(page.locator("body")).toContainText("コンテンツ担当");
    await expect(page.locator("body")).toContainText("品質確認担当");
  } finally {
    await environment.stop();
  }
});

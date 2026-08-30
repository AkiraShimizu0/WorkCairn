import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  completeFirstRun,
  completeFirstRunFast,
  ensureRequestDetail,
  pairThroughUI,
} from "./support/actions.mjs";

// First-run Setup Wizard and AI connection UI -- the only file that
// exercises Setup through the full UI walk (completeFirstRun) as the
// thing under test, rather than as unavoidable per-test scaffolding.

// overrideStorageKind intercepts /v1/workspace-status, lets the request
// reach the real daemon via route.fetch() (no recursion -- fetch() bypasses
// this same handler), and patches only result.storage_kind before
// fulfilling with the otherwise-untouched canonical envelope. This is a
// presentation-only substitution: it never changes canonical backend state,
// only what one already-fixture-covered kind this page render sees.
async function overrideStorageKind(page, kind) {
  await page.route("**/v1/workspace-status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    if (payload.result) payload.result.storage_kind = kind;
    await route.fulfill({ response, body: JSON.stringify(payload) });
  });
}

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
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("#setup-dialog")).toBeVisible();

    const connect = page.locator("#setup-content").getByRole("button", { name: "Claudeを接続" });
    await connect.click();
    await expect(page.locator("#busy-overlay")).toBeVisible();
    const darkBusyBackground = await page.locator(".busy-card").evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkBusyBackground).not.toBe("rgb(255, 255, 255)");
    await expect(page.locator("#busy-overlay")).toBeHidden();
    await expect(page.locator("#setup-content")).toContainText("Claudeの接続設定を完了できませんでした");

    await connect.click();
    await expect(page.locator("#busy-overlay")).toBeVisible();
    await expect(page.locator("#busy-overlay")).toBeHidden();
    await expect(page.locator("#setup-content")).toContainText("Connected");
    expect(attempt).toBe(2);
  } finally {
    await page.emulateMedia({ colorScheme: null });
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

test("settings dialog never renders a literal null and matches the active theme @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);

    const settingsButton = page.locator("#settings-button");
    await expect(settingsButton).toBeVisible();
    await settingsButton.click();
    await expect(page.locator("#settings-dialog")).toBeVisible();

    // providerSetupFailureNode() returns null whenever there is no setup
    // error -- the common case exercised here -- so this is the exact
    // condition that previously stringified to a literal "null" node.
    const settingsText = await page.locator("#settings-dialog").innerText();
    expect(settingsText).not.toMatch(/\bnull\b/);
    expect(settingsText).not.toMatch(/\bundefined\b/);

    const lightBackground = await page.locator(".connection-card").evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(lightBackground).toBe("rgb(255, 255, 255)");

    await page.locator('button[data-close-dialog]').first().click();
    await expect(page.locator("#settings-dialog")).toBeHidden();

    await page.emulateMedia({ colorScheme: "dark" });
    await settingsButton.click();
    await expect(page.locator("#settings-dialog")).toBeVisible();

    const darkSettingsText = await page.locator("#settings-dialog").innerText();
    expect(darkSettingsText).not.toMatch(/\bnull\b/);
    expect(darkSettingsText).not.toMatch(/\bundefined\b/);

    const darkCardBackground = await page.locator(".connection-card").evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkCardBackground).not.toBe("rgb(255, 255, 255)");
    const darkStorageBackground = await page.locator(".storage-card").evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkStorageBackground).not.toBe("rgb(255, 255, 255)");

    const cardTextColor = await page.locator(".connection-card p").first().evaluate((element) => getComputedStyle(element).color);
    expect(cardTextColor).not.toBe("rgb(255, 255, 255)");
    expect(cardTextColor).not.toBe(darkCardBackground);
  } finally {
    await page.emulateMedia({ colorScheme: null });
    await environment.stop();
  }
});

test("storage copy uses optional data-folder language, not Vault/iCloud/Obsidian as prerequisites @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await expect(page.locator("#setup-dialog")).toBeVisible();

    // Step 1 (storage) describes a data folder, does not recommend iCloud
    // Drive, and names Obsidian (when mentioned at all) as optional.
    const storageStep = page.locator("#setup-content .setup-step").first();
    await expect(storageStep).toContainText("データフォルダ");
    await expect(storageStep).not.toContainText("推奨");
    const storageStepText = await storageStep.innerText();
    expect(storageStepText).not.toMatch(/Obsidianが必要/);

    // Team-approval screen (still pre-approval): dedicated data-folder
    // wording, never the general-facing "専用Vault" framing.
    await page.getByRole("button", { name: "最初のAIチームを確認" }).click();
    await expect(page.locator("#setup-content")).toContainText("専用データフォルダ");
    await expect(page.locator("#setup-content")).not.toContainText("専用Vault");

    // Explicit approval boundary, fixed against the canonical read model
    // (not just a screen transition): organization_ready is false before
    // the explicit approval click and true only after the Command
    // actually commits.
    const fetchJSON = (path) => page.evaluate(async (url) => {
      const response = await fetch(url, { headers: { Accept: "application/json" } });
      return (await response.json()).result;
    }, path);

    await expect(page.locator("#setup-dialog")).toBeVisible();
    const beforeApproval = await fetchJSON("/v1/workspace-status");
    expect(beforeApproval.organization_ready).toBe(false);

    await page.getByRole("button", { name: "承認してセットアップ" }).click();
    // Waiting on the wizard's own post-approval affordance -- rather than a
    // fixed sleep -- is what guarantees the Command has already reached a
    // terminal state by the time the read models below are re-fetched.
    await expect(page.locator("#setup-content")).not.toContainText("最小のAIチームを作成しますか？");
    await expect(page.getByRole("button", { name: "会社を始める" })).toBeVisible();

    const afterApproval = await fetchJSON("/v1/workspace-status");
    expect(afterApproval.organization_ready).toBe(true);
    const organization = await fetchJSON("/v1/organization");
    expect(organization.inventory.employees.length).toBeGreaterThan(0);

    await page.getByRole("button", { name: "会社を始める" }).click();
    await expect(page.locator("#setup-dialog")).toBeHidden();

    // Settings storage card: data-folder language. pairThroughUI simulates
    // a remote (non-Mac-loopback) device, so local setup is unavailable
    // here -- the card must fall back to a neutral "can't open on this
    // device" message, never an Obsidian-specific instruction.
    await page.locator("#settings-button").click();
    await expect(page.locator("#settings-dialog")).toBeVisible();
    const storageCard = page.locator(".storage-card");
    await expect(storageCard).toContainText("データフォルダ");
    await expect(storageCard).not.toContainText("Obsidianで見る場合");
    await expect(storageCard).toContainText("この端末では保存先フォルダを直接開けません。");
  } finally {
    await environment.stop();
  }
});

test("Setup storage step renders correct copy for temporary, iCloud Drive, and dedicated-local storage kinds @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    const cases = [
      {
        kind: "temporary",
        expectText: ["一時的なWorkCairnデータフォルダ", "Acceptance／test専用です", "iCloud DriveもObsidianも任意です"],
        rejectText: ["Temporary Vault"],
      },
      {
        kind: "icloud_drive",
        expectText: ["WorkCairn専用データフォルダ", "iCloud Drive上の任意の保存先です", "Obsidianは不要"],
        rejectText: ["iCloud DriveのWorkCairn専用Vault"],
      },
      {
        kind: "dedicated_local",
        expectText: ["WorkCairn専用のローカルデータフォルダ", "Mac上の通常のローカル保存先です", "iCloud DriveもObsidianも不要です"],
        rejectText: ["ローカルVault"],
      },
    ];
    for (const testCase of cases) {
      await overrideStorageKind(page, testCase.kind);
      // Direct loopback connect (no pairing needed, same as the Claude
      // connection test above) reaches Setup fresh with the overridden kind.
      await page.goto(environment.daemon.baseURL);
      await expect(page.locator("#setup-dialog")).toBeVisible();
      const storageStep = page.locator("#setup-content .setup-step").first();
      for (const text of testCase.expectText) await expect(storageStep).toContainText(text);
      const storageStepText = await storageStep.innerText();
      for (const text of testCase.rejectText) expect(storageStepText).not.toContain(text);
      await page.unroute("**/v1/workspace-status");
    }
  } finally {
    await environment.stop();
  }
});

test("local Mac reveal path shows dedicated-local copy, mocks reveal-workspace, and states Obsidian as optional @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await overrideStorageKind(page, "dedicated_local");
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("#setup-dialog")).toBeVisible();
    await completeFirstRunFast(page);

    let revealCalls = 0;
    await page.route("**/v1/local-setup/reveal-workspace", async (route) => {
      revealCalls += 1;
      // Mocked entirely at the network layer -- the real handler (which
      // would open a real Finder window) is never reached.
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ version: "workcairn-workspace-status.v1", ok: true, result: { revealed: true } }),
      });
    });

    await page.locator("#settings-button").click();
    await expect(page.locator("#settings-dialog")).toBeVisible();
    const storageCard = page.locator(".storage-card");
    await expect(storageCard).toContainText("WorkCairn専用のローカルデータフォルダ");
    await expect(storageCard).toContainText("Mac上の通常のローカル保存先です");
    await expect(storageCard).not.toContainText("ローカルVault");

    const revealButton = storageCard.getByRole("button", { name: "会社データを見る" });
    await expect(revealButton).toBeVisible();
    await revealButton.click();

    const toastLocator = page.locator("#toast");
    await expect(toastLocator).toContainText("Finderに会社データを表示しました");
    await expect(toastLocator).toContainText("Obsidianを使う場合は「Open folder as vault」を選べます");
    await expect(toastLocator).not.toContainText("Obsidianでは「Open folder as vault」を選んでください");
    expect(revealCalls).toBe(1);
  } finally {
    await page.unroute("**/v1/workspace-status");
    await page.unroute("**/v1/local-setup/reveal-workspace");
    await environment.stop();
  }
});

test("first-run Setup Wizard renders correctly in dark mode without a literal null @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await expect(page.locator("#setup-dialog")).toBeVisible();

    // Light mode baseline: confirm this test's assertions describe the
    // pre-existing appearance before checking anything changes in dark mode.
    const lightStepBackground = await page.locator(".setup-step").first().evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(lightStepBackground).toBe("rgb(255, 255, 255)");
    const setupText = await page.locator("#setup-content").innerText();
    expect(setupText).not.toMatch(/\bnull\b/);
    expect(setupText).not.toMatch(/\bundefined\b/);

    await page.emulateMedia({ colorScheme: "dark" });
    const darkStepBackground = await page.locator(".setup-step").first().evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkStepBackground).not.toBe("rgb(255, 255, 255)");
    // renderSetupWizard() shares the same providerSetupFailureNode()
    // replaceChildren() call shape as Settings -- exercise it directly here
    // too, not just via the Settings dialog covered by the test above.
    const darkSetupText = await page.locator("#setup-content").innerText();
    expect(darkSetupText).not.toMatch(/\bnull\b/);
    expect(darkSetupText).not.toMatch(/\bundefined\b/);
  } finally {
    await page.emulateMedia({ colorScheme: null });
    await environment.stop();
  }
});

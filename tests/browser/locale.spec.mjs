import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import { completeFirstRunFast, openNewRequestFromUI, pairThroughUI } from "./support/actions.mjs";

async function forcePairing(page) {
  const statusRoute = (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ mode: "trusted_lan", authenticated: false, local_setup_available: false }),
  });
  await page.route("**/v1/local-access/status", statusRoute);
  return statusRoute;
}

test("explicit English selection persists before pairing and never enters request payloads @critical", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const postBodies = [];
  page.on("request", (request) => {
    if (request.method() === "POST") postBodies.push(request.postData() || "");
  });
  try {
    const statusRoute = await forcePairing(page);
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("html")).toHaveAttribute("lang", "ja");
    await expect(page.locator("#pairing-view h1")).toHaveText("WorkCairnと接続");

    await page.locator("#locale-select").selectOption("en");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    await expect(page.locator("#pairing-view h1")).toHaveText("Connect to WorkCairn");
    await expect(page.locator("#locale-select")).toHaveValue("en");
    expect(await page.evaluate(() => localStorage.getItem("workcairn.ui-locale"))).toBe("en");

    await page.locator("#pairing-code").fill(environment.daemon.pairingCode);
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await page.unroute("**/v1/local-access/status", statusRoute);
    await expect(page.locator("#setup-dialog")).toBeVisible();
    await expect(page.locator("#setup-heading")).toHaveText("Welcome to WorkCairn");
    await page.locator("#setup-dialog [data-close-dialog]").click();
    await page.locator("#settings-button").click();
    await expect(page.locator("#settings-locale-select")).toHaveValue("en");
    await expect(page.locator("#settings-dialog")).toContainText("Display language");
    const englishBody = (await page.locator("body").innerText()).replaceAll("日本語", "");
    expect(englishBody).not.toMatch(/[぀-ヿ㐀-鿿]/u);
    expect(postBodies.every((body) => !body.includes('"locale"'))).toBe(true);
  } finally {
    await environment.stop();
  }
});

test("invalid stored locale is removed and deterministic Japanese remains active", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await page.addInitScript(() => localStorage.setItem("workcairn.ui-locale", "auto"));
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("html")).toHaveAttribute("lang", "ja");
    await expect(page.locator("#locale-select")).toHaveValue("ja");
    expect(await page.evaluate(() => localStorage.getItem("workcairn.ui-locale"))).toBeNull();
  } finally {
    await environment.stop();
  }
});

test("locale change requires confirmation before discarding an unsent draft @setup", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await openNewRequestFromUI(page);
    await page.locator("#composer-input").fill("保存していない依頼");
    page.once("dialog", (dialog) => dialog.dismiss());
    await page.locator("#locale-select").selectOption("en");
    await expect(page.locator("html")).toHaveAttribute("lang", "ja");
    await expect(page.locator("#locale-select")).toHaveValue("ja");
    await expect(page.locator("#composer-input")).toHaveValue("保存していない依頼");
  } finally {
    await environment.stop();
  }
});

test("locale controls stay disabled throughout a non-GET request", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  let releasePair;
  const pairGate = new Promise((resolve) => { releasePair = resolve; });
  try {
    const statusRoute = await forcePairing(page);
    await page.route("**/v1/local-access/pair", async (route) => {
      await pairGate;
      await route.continue();
    });
    await page.goto(environment.daemon.baseURL);
    await page.locator("#pairing-code").fill(environment.daemon.pairingCode);
    await page.getByRole("button", { name: "接続する" }).click();
    await expect(page.locator("#locale-select")).toBeDisabled();
    releasePair();
    await expect(page.locator("#setup-dialog")).toBeVisible();
    await page.unroute("**/v1/local-access/status", statusRoute);
    await expect(page.locator("#locale-select")).toBeEnabled();
  } finally {
    releasePair?.();
    await environment.stop();
  }
});

test("catalog mismatch fails closed with bilingual non-interactive output", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await page.route("**/assets/i18n.js", async (route) => {
      const response = await route.fetch();
      const original = await response.text();
      await route.fulfill({
        response,
        body: original.replace('"static.menu.open":', '"static.menu.open_broken":'),
      });
    });
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator(".i18n-fatal")).toContainText("The display-language configuration could not be loaded");
    await expect(page.locator("button")).toHaveCount(0);
  } finally {
    await environment.stop();
  }
});

test("locale preference read and write failures remain browser-local and fail safely", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await page.addInitScript(() => {
      const originalGet = Storage.prototype.getItem;
      Storage.prototype.getItem = function getItem(key) {
        if (key === "workcairn.ui-locale") throw new DOMException("blocked", "SecurityError");
        return originalGet.call(this, key);
      };
    });
    await page.goto(environment.daemon.baseURL);
    await expect(page.locator("html")).toHaveAttribute("lang", "ja");
    await expect(page.locator("#toast")).toContainText("表示言語の保存設定を読み込めませんでした");

    await page.evaluate(() => {
      const originalSet = Storage.prototype.setItem;
      Storage.prototype.setItem = function setItem(key, value) {
        if (key === "workcairn.ui-locale") throw new DOMException("blocked", "QuotaExceededError");
        return originalSet.call(this, key, value);
      };
    });
    await page.locator("#locale-select").selectOption("en");
    await expect(page.locator("html")).toHaveAttribute("lang", "ja");
    await expect(page.locator("#locale-select")).toHaveValue("ja");
    await expect(page.locator("#toast")).toContainText("表示言語を保存できませんでした");
  } finally {
    await environment.stop();
  }
});

test("accepted pending-command state survives a locale reload", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  let releaseStatus;
  const statusGate = new Promise((resolve) => { releaseStatus = resolve; });
  const commandID = "CMD-LOCALE-PENDING-0001";
  try {
    await page.route(`**/v1/commands/${commandID}*`, async (route) => {
      await statusGate;
      await route.abort();
    });
    await page.goto(environment.daemon.baseURL);
    await page.evaluate(({ commandID }) => {
      sessionStorage.setItem("workcairn.pending-command", JSON.stringify({
        [commandID]: {
          version: "workspace-command.v1",
          command_id: commandID,
          operation: "interaction.start",
          approved: true,
          payload: { session_id: "SESSION-LOCALE-PENDING-0001" },
        },
      }));
    }, { commandID });
    await page.locator("#locale-select").selectOption("en");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    const pending = await page.evaluate(() => sessionStorage.getItem("workcairn.pending-command"));
    expect(pending).toContain(commandID);
  } finally {
    releaseStatus?.();
    await environment.stop();
  }
});

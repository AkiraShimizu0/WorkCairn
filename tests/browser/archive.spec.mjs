import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  answerClarificationIfNeeded,
  approvePlanAndExecute,
  archiveSessionFromList,
  completeFirstRunFast,
  ensureRequestList,
  expectArchivedDetail,
  openNewRequestFromUI,
  openSessionFromList,
  openSessionRowMenu,
  pairThroughUI,
  startRequest,
  switchSessionFilter,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

// Archive / unarchive lifecycle: list visibility, detail persistence,
// concurrent command isolation, and failure handling.

test("archive lifecycle hides active sessions, preserves detail, and restores on unarchive @critical @archive @mobile", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  const requestText = "Archive UI browser gate用の依頼です";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, requestText);
    await waitForPlanOrClarification(page);

    await ensureRequestList(page);
    const titlePattern = new RegExp(requestText);
    await expect(page.locator("#session-filter-active")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1);

    await archiveSessionFromList(page, titlePattern);
    await expect(page.locator("#toast")).toContainText("依頼一覧から非表示にしました");
    expect(commands.filter((command) => command.operation === "interaction.archive")).toHaveLength(1);
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(0);

    await switchSessionFilter(page, "削除済み");
    await expect(page.locator("#session-filter-archived")).toHaveAttribute("aria-selected", "true");
    const archivedRow = page.locator(".session-row").filter({ hasText: requestText });
    await expect(archivedRow).toHaveCount(1);
    await archivedRow.locator(".session-item").click();
    await expectArchivedDetail(page);
    await expect(page.locator("#activity-timeline")).toContainText(requestText);

    await page.getByRole("button", { name: "元に戻す" }).first().click();
    await expect(page.locator("#toast")).toContainText("依頼を一覧に戻しました");
    expect(commands.filter((command) => command.operation === "interaction.unarchive")).toHaveLength(1);
    await expect(page.locator(".archived-badge")).toHaveCount(0);
    await expect(page.locator("#session-filter-active")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1);
    await switchSessionFilter(page, "削除済み");
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(0);
    await switchSessionFilter(page, "依頼");
  } finally {
    await environment.stop();
  }
});

test("archive confirmation cancel keeps the session active @archive", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  const requestText = "Archive cancel browser gate用の依頼です";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, requestText);
    await ensureRequestList(page);
    const titlePattern = new RegExp(requestText);
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1, { timeout: 45_000 });
    await openSessionRowMenu(page, titlePattern);
    await page.getByRole("menuitem", { name: "履歴から削除" }).click();
    await page.getByRole("button", { name: "キャンセル", exact: true }).click();
    expect(commands.filter((command) => command.operation === "interaction.archive")).toHaveLength(0);
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1);
    await expect(page.locator(".session-archive-confirm")).toHaveCount(0);
  } finally {
    await environment.stop();
  }
});

test("archive and unarchive failures keep canonical list state @archive @failure", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.includes("iphone"), "Command-route failure assertions run on desktop where archive controls stay in the stable list pane");
  const environment = await startBrowserEnvironment("happy_path");
  const requestText = "Archive failure browser gate用の依頼です";
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  let archiveAttempts = 0;
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, requestText);
    await ensureRequestList(page);
    const titlePattern = new RegExp(requestText);
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1, { timeout: 45_000 });

    await page.route("**/v1/commands", async (route) => {
      let body = null;
      try { body = route.request().postDataJSON(); } catch {}
      if (body?.operation === "interaction.archive") {
        archiveAttempts += 1;
        await route.fulfill({
          status: 422,
          contentType: "application/json",
          body: JSON.stringify({
            version: "workspace-command.v1",
            ok: false,
            error: { code: "INTERACTION_VERSION_CONFLICT", stage: "interaction_archive", recovery_required: true },
          }),
        });
        return;
      }
      await route.continue();
    });

    await openSessionRowMenu(page, titlePattern);
    await expect(page.getByRole("menuitem", { name: "履歴から削除" })).toBeVisible({ timeout: 10_000 });
    await page.getByRole("menuitem", { name: "履歴から削除" }).click();
    await page.getByRole("button", { name: "履歴から削除", exact: true }).click();
    expect(archiveAttempts).toBe(1);
    await expect(page.locator("#toast")).not.toContainText("依頼一覧から非表示にしました");
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1);

    await page.unroute("**/v1/commands");
    await archiveSessionFromList(page, titlePattern);
    await switchSessionFilter(page, "削除済み");
    await openSessionFromList(page, titlePattern);
    await expectArchivedDetail(page);

    await page.route("**/v1/commands", async (route) => {
      let body = null;
      try { body = route.request().postDataJSON(); } catch {}
      if (body?.operation === "interaction.unarchive") {
        await route.fulfill({
          status: 422,
          contentType: "application/json",
          body: JSON.stringify({
            version: "workspace-command.v1",
            ok: false,
            error: { code: "INTERACTION_INVALID_STATE", stage: "interaction_unarchive", recovery_required: true },
          }),
        });
        return;
      }
      await route.continue();
    });
    await page.getByRole("button", { name: "元に戻す" }).first().click();
    await expect(page.locator("#toast")).not.toContainText("依頼を一覧に戻しました");
    await expectArchivedDetail(page);
    await switchSessionFilter(page, "削除済み");
    await expect(page.locator(".session-row").filter({ hasText: requestText })).toHaveCount(1);
  } finally {
    await environment.stop();
  }
});

test("archive filter and detail survive polling and reload without stale flashes @archive", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  const requestText = "Archive navigation browser gate用の依頼です";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, requestText);
    await ensureRequestList(page);
    const titlePattern = new RegExp(requestText);
    await openSessionFromList(page, titlePattern);
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#activity-timeline")).toContainText(requestText);
    await page.waitForTimeout(5200);
    await expect(page.locator("#request-detail-view")).toBeVisible();
    await expect(page.locator("#activity-timeline")).toContainText(requestText);
    await expect(page.locator(".thread-title")).not.toContainText("新しい依頼");

    await archiveSessionFromList(page, titlePattern);
    await switchSessionFilter(page, "削除済み");
    await openSessionFromList(page, titlePattern);
    await expectArchivedDetail(page);
    const archivedTitle = await page.locator(".thread-title").innerText();

    await expect(page.locator("#composer-input")).toHaveValue(/削除済みの依頼です/);
    await page.reload();
    await expect(page.locator("#workspace-view")).toBeVisible();
    await expect(page.locator("#session-filter-archived")).toHaveAttribute("aria-selected", "true", { timeout: 30_000 });
    await expectArchivedDetail(page);
    await expect(page.locator("#activity-timeline")).toContainText(requestText);
    await page.waitForTimeout(5200);
    await expectArchivedDetail(page);
    await expect(page.locator(".thread-title")).toHaveText(archivedTitle);

    await switchSessionFilter(page, "依頼");
    await expect(page.locator(".archived-badge")).toHaveCount(0);
    await expect(page.locator("#activity-timeline")).not.toContainText(requestText);
    await openNewRequestFromUI(page);
    await expect(page.locator(".thread-title")).toContainText("新しい依頼");
    await expect(page.locator("#activity-timeline")).not.toContainText(requestText);
  } finally {
    await environment.stop();
  }
});

test("archive on another session succeeds while workflow command monitors independently @critical @archive", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.includes("iphone"), "Concurrent command assertions run on desktop where list and archive controls stay stable");
  const environment = await startBrowserEnvironment("happy_path");
  const commands = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().endsWith("/v1/commands")) return;
    try { commands.push(request.postDataJSON()); } catch {}
  });
  const requestA = "Concurrent archive gate session A";
  const requestB = "Concurrent archive gate session B";
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, requestB);
    await waitForPlanOrClarification(page);
    await ensureRequestList(page);
    await expect(page.locator(".session-row").filter({ hasText: requestB })).toHaveCount(1, { timeout: 45_000 });

    await openNewRequestFromUI(page);
    await startRequest(page, requestA);
    await waitForPlanOrClarification(page);
    await answerClarificationIfNeeded(page);
    await approvePlanAndExecute(page);

    await ensureRequestList(page);
    await archiveSessionFromList(page, new RegExp(requestB));
    expect(commands.filter((command) => command.operation === "interaction.archive")).toHaveLength(1);
    await expect(page.locator("#toast")).toContainText("依頼一覧から非表示にしました");
    await expect(page.locator(".session-row").filter({ hasText: requestB })).toHaveCount(0);

    await openSessionFromList(page, new RegExp(requestA));
    await expect(page.locator("#activity-timeline")).toContainText(requestA);
    await expect(page.locator("#toast")).not.toContainText("同じ処理を実行中");
    await expect(page.locator("#activity-timeline")).not.toContainText("INTERACTION_VERSION_CONFLICT");
    const workflowCommands = commands.filter((command) => command.operation === "interaction.plan.approve_and_execute");
    expect(workflowCommands.length).toBeGreaterThanOrEqual(1);
    const archiveCommand = commands.find((command) => command.operation === "interaction.archive");
    expect(archiveCommand?.command_id).toBeTruthy();
    expect(workflowCommands.some((command) => command.command_id === archiveCommand?.command_id)).toBeFalsy();
    // Session A's workflow may still be running or may have already
    // finished by the time we reopen it -- either way the composer must be
    // in one of these two well-defined states, never stuck blank-but-idle
    // or showing stale garbage. While running it shows no processing text
    // at all (see the composer/timeline role split); once done it shows
    // the completion copy.
    const composerInput = page.locator("#composer-input");
    const composerMode = await page.locator("#thread-composer").getAttribute("data-mode");
    expect(["running", "idle"]).toContain(composerMode);
    if (composerMode === "running") {
      await expect(composerInput).toHaveValue("");
    } else {
      await expect(composerInput).toHaveValue("完了しました");
    }
  } finally {
    await environment.stop();
  }
});

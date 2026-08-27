import { expect } from "@playwright/test";

// Shared Browser Gate action helpers used across the split spec files.
// Extracted verbatim from the original single-file public-beta.spec.mjs --
// one copy, imported everywhere, instead of N divergent per-file copies.

export async function openNewRequestFromUI(page) {
  const listButton = page.locator("#new-request-button");
  if (await listButton.isVisible()) {
    await listButton.click();
    return;
  }
  const back = page.locator("#back-to-list-button");
  if (await back.isVisible()) {
    await back.click();
    await expect(page.locator("#new-request-button")).toBeVisible();
    await listButton.click();
    return;
  }
  const menu = page.locator("#menu-button");
  await expect(menu).toBeVisible();
  await menu.click();
  await page.locator("#nav-new-request").click();
}

export async function expectTextOnceInTimeline(page, text) {
  const footer = page.locator(".thread-footer");
  const timeline = page.locator("#activity-timeline");
  await expect(timeline).toContainText(text);
  await expect(footer.locator("#composer-input")).not.toHaveValue(text);
}

export async function pairThroughUI(page, daemon) {
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
  await page.getByRole("button", { name: "接続する" }).click();
  await expect(page.locator("#workspace-view")).toBeVisible();
  const cookies = await page.context().cookies(daemon.baseURL);
  expect(cookies.some((cookie) => cookie.name === "workspace_local_access" && cookie.httpOnly && cookie.sameSite === "Strict")).toBeTruthy();
  await page.unroute("**/v1/local-access/status", statusRoute);
}

export async function completeFirstRun(page) {
  await expect(page.locator("#setup-dialog")).toBeVisible();
  // First-run Setup is user-facing presentation UI (read-only, no Role text
  // input, no canonical-string matching performed by the CEO here), so it
  // uses the same Japanese presentation label as everywhere else in the
  // product -- never the raw canonical Organization role name.
  await expect(page.locator("#setup-content")).toContainText("企画担当");
  await expect(page.locator("#setup-content")).toContainText("コンテンツ担当");
  await expect(page.locator("#setup-content")).toContainText("品質確認担当");
  await expect(page.locator("#setup-content")).not.toContainText("Product Manager");
  await expect(page.locator("#setup-content")).not.toContainText("Content Writer");
  await expect(page.locator("#setup-content")).not.toContainText("QA Engineer");
  await page.getByRole("button", { name: "最初のAIチームを確認" }).click();
  await expect(page.locator("#setup-content")).toContainText("最小のAIチームを作成しますか？");
  await page.getByRole("button", { name: "承認してセットアップ" }).click();
  await expect(page.getByRole("button", { name: "会社を始める" })).toBeVisible();
  await page.getByRole("button", { name: "会社を始める" }).click();
  await expect(page.locator("#request-detail-view")).toBeVisible();
  await expect(page.locator("#composer-input")).toBeVisible();
}

// completeFirstRunFast reaches the exact same post-setup state as
// completeFirstRun (organization_ready, #setup-dialog never opens) without
// walking the Setup Wizard's own UI -- it submits the same
// `workspace.setup` Command the UI's own "承認してセットアップ" button
// sends (see executeNextCommand in app.js), waits for it to reach a
// terminal Ledger state, then reloads so the app re-evaluates
// workspace-status fresh. This is for tests where Setup Wizard is
// unavoidable per-test scaffolding, not the thing under test -- the Setup
// Wizard's own UI (labels, approval copy, button flow) is still exercised
// for real by the dedicated tests in setup.spec.mjs, which must keep using
// completeFirstRun.
export async function completeFirstRunFast(page) {
  await page.evaluate(async () => {
    const commandID = `CMD-FAST-SETUP-${Math.random().toString(36).slice(2)}`;
    const response = await fetch("/v1/commands", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Workspace-Intent": "mobile-ui.v1", Prefer: "respond-async" },
      body: JSON.stringify({
        version: "workspace-command.v1",
        command_id: commandID,
        operation: "workspace.setup",
        approved: true,
        payload: { current_time: new Date().toISOString() },
      }),
    });
    if (!response.ok) throw new Error(`workspace.setup POST failed: ${response.status} ${await response.text()}`);
    const deadline = Date.now() + 15_000;
    let lastSeen = "";
    while (Date.now() < deadline) {
      const statusResponse = await fetch(`/v1/commands/${encodeURIComponent(commandID)}?scope=workspace`);
      const bodyText = await statusResponse.text();
      lastSeen = `${statusResponse.status} ${bodyText}`;
      if (statusResponse.ok) {
        const record = JSON.parse(bodyText).result;
        if (record?.state === "succeeded") return;
        if (record?.state === "failed" || record?.state === "partial_failure") {
          throw new Error(`workspace.setup did not succeed: ${JSON.stringify(record.failure)}`);
        }
      }
      await new Promise((resolveWait) => setTimeout(resolveWait, 200));
    }
    throw new Error(`workspace.setup timed out, last status: ${lastSeen}`);
  });
  await page.reload();
  await expect(page.locator("#setup-dialog")).toBeHidden();
  await expect(page.locator("#workspace-view")).toBeVisible();
  // The real "会社を始める" button does more than close the dialog -- it
  // calls openNewRequestDraft() so the CEO lands straight on a blank
  // request draft (see app.js). A plain reload instead defaults to the
  // employees_home nav (no session exists yet), so reach the same
  // post-setup state explicitly to match completeFirstRun's postcondition.
  await openNewRequestFromUI(page);
  await expect(page.locator("#request-detail-view")).toBeVisible();
  await expect(page.locator("#composer-input")).toBeVisible();
}

export async function startRequest(page, requestText) {
  const composer = page.locator("#composer-input");
  let draftReady = await composer.isVisible()
    && (await composer.getAttribute("placeholder")) === "依頼内容を入力...";
  if (!draftReady) {
    await openNewRequestFromUI(page);
  }
  await expect(page.locator("#request-detail-view")).toBeVisible();
  await composer.fill(requestText);
  await page.locator("#composer-send").click();
  await expect(page.locator("#composer-input")).toBeVisible();
}

export async function waitForPlanOrClarification(page) {
  try {
    await page.waitForSelector('#composer-input[placeholder="回答を入力..."]', { timeout: 45_000 });
    return "clarification";
  } catch {}
  await expect(page.getByRole("button", { name: "この内容で進める" })).toBeVisible({ timeout: 45_000 });
  return "plan";
}

export async function answerClarificationIfNeeded(page, answerText = "はい。初めてWorkCairnを使う人向けです。") {
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

export async function approvePlanAndExecute(page) {
  await expect(page.locator(".msg-embed-plan")).toBeVisible();
  await expect(page.getByRole("button", { name: "進め方の作成を承認" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "実行内容を確認" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "承認して実行" })).toHaveCount(0);
  const approve = page.getByRole("button", { name: "この内容で進める" });
  await expect(approve).toHaveCount(1);
  await approve.click();
  // The composer enters the disabled "running" mode with no processing
  // text at all -- the fuller "Makerの成果物作成..." sentence lives once
  // in the timeline as an ephemeral live-status entry, never duplicated
  // here (data-mode is the precise signal since the pre-approval
  // "approve_workflow" state is already blank/disabled too).
  await expect(page.locator("#thread-composer")).toHaveAttribute("data-mode", "running", { timeout: 20_000 });
  await expect(page.locator("#composer-input")).toHaveValue("");
  await expect(approve).toHaveCount(0);
}

export async function ensureRequestDetail(page) {
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

export async function generatePlanThroughClarification(page) {
  await waitForPlanOrClarification(page);
  await answerClarificationIfNeeded(page);
  await expect(page.locator(".msg-embed-plan")).toBeVisible({ timeout: 20_000 });
}

export async function composerMetrics(page) {
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

export async function seedTimelineMessages(page, count, prefix = "seed") {
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

export async function assertComposerBottomStable(page, label, tolerance = 2) {
  const metrics = await composerMetrics(page);
  expect(metrics, `${label}: composer metrics missing`).not.toBeNull();
  expect(metrics.composerDelta, `${label}: composer should hug requests pane bottom`).toBeLessThanOrEqual(tolerance);
  return metrics.composerBottom;
}

export async function ensureRequestList(page) {
  const menu = page.locator("#menu-button");
  if (await menu.isVisible()) {
    await menu.click();
    await page.locator("#nav-request-list").click();
    await expect(page.locator("#nav-drawer")).toBeHidden();
    await expect(page.locator("#request-detail-view")).toHaveClass(/mobile-hidden/);
  } else if (await page.locator("#request-list-view").isHidden()) {
    await page.locator("#back-to-list-button").click();
  }
  await expect(page.locator("#request-list-view")).toBeVisible();
  await expect(page.getByRole("tab", { name: "依頼" })).toBeVisible();
}

export async function switchSessionFilter(page, label) {
  await ensureRequestList(page);
  await page.getByRole("tab", { name: label, exact: true }).click();
}

export async function openSessionRowMenu(page, titlePattern) {
  await ensureRequestList(page);
  const row = page.locator(".session-row").filter({ hasText: titlePattern });
  const menuButton = row.locator(".session-menu-button");
  for (let attempt = 0; attempt < 3; attempt += 1) {
    if (await menuButton.isVisible()) await menuButton.click();
    else await menuButton.evaluate((element) => element.click());
    try {
      await expect(page.getByRole("menuitem", { name: "履歴から削除" })).toBeVisible({ timeout: 3000 });
      return;
    } catch (error) {
      if (attempt === 2) throw error;
    }
  }
}

export async function openSessionFromList(page, titlePattern) {
  await ensureRequestList(page);
  const sessionButton = page.locator(".session-row").filter({ hasText: titlePattern }).locator(".session-item");
  await sessionButton.evaluate((element) => element.click());
  if (await page.locator("#menu-button").isVisible()) {
    await expect(page.locator("#request-detail-view")).not.toHaveClass(/mobile-hidden/, { timeout: 15_000 });
  }
}

export async function expectArchivedDetail(page) {
  await expect(page.locator(".archived-badge")).toContainText("削除済み");
  await expect(page.locator("#composer-input")).toHaveValue(/削除済みの依頼です/);
}

export async function archiveSessionFromList(page, titlePattern) {
  await openSessionRowMenu(page, titlePattern);
  const menuItem = page.getByRole("menuitem", { name: "履歴から削除" });
  await expect(menuItem).toBeVisible({ timeout: 10_000 });
  await menuItem.click();
  await expect(page.locator(".session-archive-confirm")).toBeVisible();
  await page.getByRole("button", { name: "履歴から削除", exact: true }).click();
}

// openDetailPaneDeliverableViewer opens the request-detail panel's nested
// disclosures (#details-panel -> .artifact-detail -> "成果物を見る") down
// to the same .deliverable-viewer component the timeline uses --
// unification means this is the identical component, not a parallel one,
// so every assertion below reuses the same selectors as the timeline test.
//
// #details-panel is a genuine, visible, user-openable <details> disclosure
// living inside #thread-scroll (see the Detail Pane Visibility round) --
// real pointer clicks and toBeVisible() are meaningful here.
export async function openDetailPaneDeliverableViewer(page) {
  const panel = page.locator("#details-panel");
  await expect(panel).toBeVisible();
  if (!(await panel.evaluate((element) => element.open))) {
    await panel.locator("> summary").click();
  }
  const artifact = panel.locator(".artifact-detail").first();
  await expect(artifact).toBeVisible();
  if (!(await artifact.evaluate((element) => element.open))) {
    await artifact.locator("> summary").first().click();
  }
  const toggle = artifact.getByRole("button", { name: "成果物を見る" });
  await expect(toggle).toBeVisible();
  await toggle.click();
  const viewer = artifact.locator(".deliverable-viewer");
  await expect(viewer).toBeVisible();
  // fillDeliverableViewer's fetch is async and detached from the click
  // above, so wait for its actual result content to land.
  await expect(viewer.locator(".md-heading, .warning")).toHaveCount(1, { timeout: 15_000 });
  return viewer;
}

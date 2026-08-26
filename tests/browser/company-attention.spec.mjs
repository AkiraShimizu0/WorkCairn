import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  completeFirstRunFast,
  pairThroughUI,
  startRequest,
  waitForPlanOrClarification,
} from "./support/actions.mjs";

const ATTENTION_FIXTURE = [
  {
    type: "approval_required",
    entity_type: "interaction",
    entity_id: "SESSION-APPROVAL-001",
    project_name: "Browser Approval Project",
    summary: "Plan生成に承認が必要です。",
    action: { kind: "approve", operation: "interaction.plan.generate" },
    observed_at: "2026-08-26T09:00:00Z",
  },
  {
    type: "human_input_required",
    entity_type: "interaction",
    entity_id: "SESSION-ANSWER-001",
    summary: "CEOへの確認質問への回答待ちです。",
    action: { kind: "answer", operation: "interaction.answer" },
    observed_at: "2026-08-26T09:05:00Z",
  },
  {
    type: "interaction_attention_required",
    entity_type: "interaction",
    entity_id: "SESSION-INSPECT-001",
    project_name: "Browser Inspect Project",
    summary: "Workflowが対応を必要とする状態です。",
    action: { kind: "inspect", operation: "interaction.workflow.inspect" },
    observed_at: "2026-08-26T09:10:00Z",
  },
  {
    type: "routine_recovery_required",
    entity_type: "routine",
    entity_id: "ROUTINE-1",
    responsibility_id: "RESP-1",
    summary: "Routine ROUTINE-1 はActiveですが、次回実行予定のScheduleが見つかりません。",
    action: { kind: "reconcile", operation: "routine-reconcile" },
    observed_at: "2026-08-26T09:15:00Z",
  },
  {
    type: "future_attention_type",
    entity_type: "interaction",
    entity_id: "SESSION-FUTURE-001",
    summary: "Future attention type should degrade safely.",
    action: { kind: "future_action" },
    observed_at: "2026-08-26T09:20:00Z",
  },
];

async function showCompanyView(page) {
  const menu = page.locator("#menu-button");
  if (await menu.isVisible()) {
    await menu.click();
    await page.locator("#nav-employees-home").click();
  }
  await expect(page.getByRole("heading", { name: "対応が必要" })).toBeVisible();
}

async function mockAttentionFeed(page, items) {
  await page.route("**/v1/attention", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        version: "workcairn-attention.v1",
        ok: true,
        result: items,
      }),
    });
  });
}

test("company attention empty state renders as a normal Company View section @office @critical", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await showCompanyView(page);
    await expect(page.locator("#company-attention")).toContainText("現在、対応が必要な項目はありません");
    await page.emulateMedia({ colorScheme: "dark" });
    const emptyText = page.locator("#company-attention .empty");
    await expect(emptyText).toBeVisible();
    const color = await emptyText.evaluate((element) => getComputedStyle(element).color);
    const background = await page.locator(".attention-item, #company-attention").first().evaluate((element) => {
      const section = element.closest("#company-attention");
      return getComputedStyle(section).backgroundColor;
    });
    expect(color).not.toBe("rgb(255, 255, 255)");
    expect(background).not.toBe("rgb(255, 255, 255)");
  } finally {
    await environment.stop();
  }
});

test("company attention renders backend ordering and typed labels without client-side ranking @office", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await mockAttentionFeed(page, ATTENTION_FIXTURE);
    await page.reload();
    await expect(page.locator("#workspace-view")).toBeVisible();
    await showCompanyView(page);
    const items = page.locator("#company-attention .attention-item");
    await expect(items).toHaveCount(ATTENTION_FIXTURE.length);
    for (let index = 0; index < ATTENTION_FIXTURE.length; index += 1) {
      await expect(items.nth(index)).toHaveAttribute("data-attention-index", String(index));
      await expect(items.nth(index)).toHaveAttribute("data-attention-type", ATTENTION_FIXTURE[index].type);
    }
    await expect(items.nth(0)).toContainText("承認が必要");
    await expect(items.nth(0)).toContainText("Browser Approval Project");
    await expect(items.nth(1)).toContainText("回答が必要");
    await expect(items.nth(2)).toContainText("確認が必要");
    await expect(items.nth(3)).toContainText("Routineの修復が必要");
    await expect(items.nth(3)).toContainText("RESP-1");
    await expect(items.nth(3)).not.toContainText("依頼を開く");
    await expect(items.nth(4)).toContainText("future_attention_type");
    await expect(items.nth(4)).toContainText("future_action");
    await expect(items.nth(4)).not.toContainText("依頼を開く");
    await expect(page.getByRole("button", { name: "依頼を開く" })).toHaveCount(3);
  } finally {
    await environment.stop();
  }
});

test("company attention API failure stays section-local @office", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await page.route("**/v1/attention", async (route) => {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({
          version: "workcairn-attention.v1",
          ok: false,
          error: { code: "ATTENTION_INSPECTION_FAILED" },
        }),
      });
    });
    await page.reload();
    await expect(page.locator("#workspace-view")).toBeVisible();
    await showCompanyView(page);
    await expect(page.locator("#company-attention .attention-section-warning")).toContainText("対応が必要な項目を読み込めませんでした");
    await expect(page.locator(".office-room, .employee-compact-row").first()).toBeVisible();
  } finally {
    await environment.stop();
  }
});

test("company attention shows human_input_required from live backend after clarification starts @office @critical", async ({ page }) => {
  const environment = await startBrowserEnvironment("clarification_three");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    await startRequest(page, "company attention gate用の短い成果物を作ってください");
    await waitForPlanOrClarification(page);
    await showCompanyView(page);
    const attention = page.locator("#company-attention .attention-item").first();
    await expect(attention).toBeVisible({ timeout: 45_000 });
    await expect(attention).toContainText("回答が必要");
    await expect(attention).toContainText("依頼 ·");
    await expect(attention.getByRole("button", { name: "依頼を開く" })).toBeVisible();
  } finally {
    await environment.stop();
  }
});

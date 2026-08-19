import { expect, test } from "@playwright/test";
import { startBrowserEnvironment } from "./support/harness.mjs";
import {
  completeFirstRunFast,
  openNewRequestFromUI,
  pairThroughUI,
  startRequest,
} from "./support/actions.mjs";

// AI office visual: room/employee presentation, responsive compact
// fallback, and separation from the selected-request chat.

test("employee visual section stays separate from selected request chat @office @mobile", async ({ page }) => {
  const environment = await startBrowserEnvironment("happy_path");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    const menu = page.locator("#menu-button");
    const isMobile = await menu.isVisible();
    if (isMobile) {
      await menu.click();
      await page.locator("#nav-employees-home").click();
      await expect(page.locator(".employee-compact-row").first()).toBeVisible();
    } else {
      await expect(page.locator(".office-room")).toBeVisible();
      await expect(page.locator(".room-character").first()).toBeVisible();
    }
    await expect(page.getByRole("heading", { name: "社内の動き" })).toBeVisible();
    await startRequest(page, "りんごについて100文字程度で説明して");
    await expect(page.locator("#activity-timeline")).toBeVisible();
    if (isMobile) {
      await expect(page.locator(".office-room-characters")).toBeHidden();
    } else {
      await expect(page.locator(".office-room")).toBeVisible();
    }
  } finally {
    await environment.stop();
  }
});

test("office room visual: single room, characters, poses, themes, and mobile fallback @office @mobile", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("happy_path");
  const isMobileProject = testInfo.project.name.includes("iphone");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    if (isMobileProject) {
      const menu = page.locator("#menu-button");
      await menu.click();
      await page.locator("#nav-employees-home").click();
      await expect(page.locator(".office-room-compact .employee-compact-row")).toHaveCount(3);
      await expect(page.locator(".office-room-characters").first()).toBeHidden();
      return;
    }

    await expect(page.locator(".office-room")).toHaveCount(1);
    await expect(page.locator(".office-zone")).toHaveCount(0);
    await expect(page.locator(".office-room-svg")).toHaveCount(1);
    await expect(page.locator(".room-character")).toHaveCount(3);
    await expect(page.locator(".room-character .char-head").first()).toBeVisible();
    await expect(page.locator(".room-character .char-leg-left").first()).toBeVisible();
    await expect(page.locator(".room-character.pose-idle").first()).toBeVisible();

    await page.emulateMedia({ colorScheme: "dark" });
    const darkHead = await page.locator(".char-head").first().evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkHead).not.toBe("rgba(0, 0, 0, 0)");

    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.locator(".room-character").first()).toBeVisible();
  } finally {
    await environment.stop();
  }
});

test("office room visual exposes room scene, characters, poses, and compact fallback @office @mobile", async ({ page }, testInfo) => {
  const environment = await startBrowserEnvironment("happy_path");
  const isMobileProject = testInfo.project.name.includes("iphone");
  try {
    await pairThroughUI(page, environment.daemon);
    await completeFirstRunFast(page);
    if (isMobileProject) {
      await page.locator("#menu-button").click();
      await page.locator("#nav-employees-home").click();
      await expect(page.locator(".office-room-compact .employee-compact-row")).toHaveCount(3);
      await expect(page.locator(".office-room-characters").first()).toBeHidden();
      return;
    }
    await expect(page.locator(".office-room")).toHaveCount(1);
    await expect(page.locator(".office-room-scene")).toBeVisible();
    await expect(page.locator(".room-character")).toHaveCount(3);
    await expect(page.locator(".char-head").first()).toBeVisible();
    await expect(page.locator(".char-leg").first()).toBeVisible();
    await expect(page.locator(".char-eye").first()).toBeVisible();
    await page.emulateMedia({ colorScheme: "dark" });
    const darkHeadTone = await page.locator(".char-head").first().evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(darkHeadTone).not.toBe("rgba(0, 0, 0, 0)");
    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.locator(".room-character").first()).toBeVisible();
  } finally {
    await environment.stop();
  }
});

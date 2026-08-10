import { expect, test, type Page } from "@playwright/test";

const mobile = { width: 390, height: 844 } as const;

function collectBrowserErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(`console: ${message.text()}`);
  });
  page.on("pageerror", (error) => errors.push(`page: ${error.message}`));
  return errors;
}

async function expectNoHorizontalOverflow(page: Page) {
  expect(
    await page.evaluate(() => ({
      document: document.documentElement.scrollWidth <= innerWidth,
      main: (() => {
        const main = document.querySelector("#main-content");
        return main !== null && main.scrollWidth <= main.clientWidth;
      })(),
    })),
  ).toEqual({ document: true, main: true });
}

test("keeps every primary page named and inside the mobile viewport", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize(mobile);
  const routes = [
    ["captures", "Captures"],
    ["environments", "Environments"],
    ["accounts", "Accounts"],
    ["extensions", "Plugins"],
    ["policies/approvals", "Policy & approvals"],
    ["quality", "Quality"],
    ["settings", "Settings"],
  ] as const;

  for (const [route, heading] of routes) {
    await page.goto(`/?preview=1#${route}`);
    await expect(page.getByRole("heading", { exact: true, name: heading })).toBeVisible();
    await expect(page.locator(".primary-nav a, .settings-link")).toHaveCount(7);
    for (const link of await page.locator(".primary-nav a, .settings-link").all()) {
      expect((await link.getAttribute("aria-label")) ?? (await link.textContent())?.trim()).not.toBe("");
    }
    await expectNoHorizontalOverflow(page);
  }
  expect(errors).toEqual([]);
});

test("presents high-cardinality Capture and Conversation data without a desktop-width table", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize(mobile);
  await page.goto("/?preview=1#captures");
  await expect(page.locator(".capture-table")).toHaveCount(2);
  await expect(page.locator(".capture-table").first()).toHaveCSS("display", "block");
  await expect(page.locator(".capture-table thead").first()).toHaveCSS("display", "none");
  await expect(page.locator(".capture-table-panel-running tbody tr")).toHaveCount(8);
  await expect(page.getByText("project", { exact: true })).toBeVisible();
  await expect(page.getByText("machine-pr", { exact: true })).toBeHidden();
  await expect(page.locator(".capture-table-panel-running tbody tr").first()).toContainText("gateway");
  await expect(page.locator(".capture-table-panel-running tbody tr").first()).toContainText("Work");
  await expectNoHorizontalOverflow(page);

  await page.getByRole("link", { name: "Browse conversations" }).click();
  await expect(page.locator(".conversation-table")).toHaveCSS("display", "block");
  await expect(page.locator(".conversation-link").first()).toBeVisible();
  await expectNoHorizontalOverflow(page);
  expect(errors).toEqual([]);
});

test("keeps the Request inspector readable on a narrow screen", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize(mobile);
  await page.goto("/?preview=1#captures/requests");
  await page.locator('[data-conversation-key="capture-run:run-preview"] .conversation-link').click();

  await expect(page.getByRole("heading", { name: "Run conversation" })).toBeVisible();
  const completedTurn = page.locator("#run-turn-exchange-preview");
  await expect(completedTurn.getByText("Inspect the current package and summarize the failing test.")).toBeVisible();
  await expect(completedTurn.getByText("Proposed by the model", { exact: true })).toBeVisible();
  const turnMap = page.getByRole("navigation", { name: "Jump to a turn" });
  await expect(turnMap).toBeVisible();
  await expect(turnMap.getByRole("button")).toHaveCount(27);
  await expect(turnMap.locator('[aria-current="step"]')).toHaveCount(1);
  await expect(turnMap).toHaveCSS("position", "sticky");
  expect(await turnMap.locator("ol").evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  await expect(page.locator(".conversation-turn")).toHaveCount(27);
  await expect(page.locator("#run-turn-exchange-preview-pending").getByText("Waiting for the response", { exact: true })).toBeVisible();
  await expectNoHorizontalOverflow(page);
  expect(errors).toEqual([]);
});

test("moves keyboard focus to the destination heading after navigation", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#captures");
  const environments = page.getByRole("link", { name: "Environments" });
  await environments.focus();
  await page.keyboard.press("Enter");
  const heading = page.getByRole("heading", { exact: true, name: "Environments" });
  await expect(heading).toBeFocused();

  await page.getByRole("link", { name: "Open" }).last().focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Work" })).toBeFocused();
  expect(errors).toEqual([]);
});

test("keeps keyboard focus distinct from location state and names the revoke boundary", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#captures");
  await expect(page.getByRole("heading", { exact: true, name: "Captures" })).toBeVisible();
  const currentLocation = page.locator('.primary-nav [aria-current="page"]');
  const environments = page.getByRole("link", { name: "Environments" });
  await environments.focus();
  await expect(environments).toBeFocused();
  await expect(environments).not.toHaveAttribute("aria-current", "page");
  await expect(currentLocation).toHaveText("Captures");
  expect(await environments.evaluate((element) => ({
    style: getComputedStyle(element).outlineStyle,
    width: getComputedStyle(element).outlineWidth,
  }))).toEqual({ style: "solid", width: "2px" });

  const manualCapture = page.getByRole("link", { name: /Editor Manual capture/u });
  await manualCapture.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Editor" })).toBeFocused();
  const revoke = page.getByRole("button", { name: "Revoke login" });
  await revoke.focus();
  await page.keyboard.press("Enter");
  const confirmation = page.getByRole("group", { name: "Revoke Editor?" });
  await expect(confirmation).toContainText("nothing is deleted");
  await expect(confirmation.getByRole("button", { name: "Cancel" })).toBeFocused();
  expect(errors).toEqual([]);
});

test("keeps the Chinese mobile workspace navigable and free of raw locale keys", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize(mobile);
  await page.goto("/?preview=1#captures");
  await page.getByRole("button", { name: "中文" }).click();
  await expect(page.getByRole("heading", { exact: true, name: "捕获" })).toBeVisible();
  await expect(page.locator("body")).not.toContainText(/(?:captures|workspace|environmentDetail)\.[a-z]/u);
  await expectNoHorizontalOverflow(page);
  expect(errors).toEqual([]);
});

test("fails malformed deep links closed while keeping recovery navigation available", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#captures/invalid%2Fidentifier");
  await expect(page.getByRole("alert")).toBeVisible();
  await page.getByRole("link", { name: "Environments" }).click();
  await expect(page.getByRole("heading", { name: "Environments" })).toBeVisible();
  expect(errors).toEqual([]);
});

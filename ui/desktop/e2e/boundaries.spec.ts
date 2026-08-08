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

test("presents high-cardinality Capture and Request data without a desktop-width table", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize(mobile);
  await page.goto("/?preview=1#captures");
  await expect(page.locator(".capture-table")).toHaveCSS("display", "block");
  await expect(page.locator(".capture-table thead")).toHaveCSS("display", "none");
  await expect(page.getByText("project", { exact: true })).toBeVisible();
  await expect(page.getByText("machine-pr", { exact: true })).toBeVisible();
  await expectNoHorizontalOverflow(page);

  await page.getByRole("tab", { name: "Requests" }).click();
  await expect(page.locator(".request-table")).toHaveCSS("display", "block");
  await expect(page.getByRole("link", { name: /Claude request/u })).toBeVisible();
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

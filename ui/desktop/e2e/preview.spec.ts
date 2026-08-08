import { expect, test, type Page } from "@playwright/test";

function collectBrowserErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(`console: ${message.text()}`);
  });
  page.on("pageerror", (error) => errors.push(`page: ${error.message}`));
  return errors;
}

async function openPreview(page: Page, route = "captures") {
  await page.goto(`/?preview=1#${route}`);
  await expect(page.getByRole("banner").getByText("Ready", { exact: true })).toBeVisible();
}

test("opens the Environment-first workspace with one clear navigation focus", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);

  await expect(page.getByRole("heading", { exact: true, name: "Captures" })).toBeVisible();
  await expect(page.locator(".primary-nav a, .settings-link")).toHaveCount(7);
  await expect(page.locator('[aria-current="page"]')).toHaveCount(2);
  await expect(page.locator('.primary-nav [aria-current="page"]')).toHaveCount(1);
  await expect(page.getByRole("table")).toContainText("claude");
  await expect(page.getByRole("table")).toContainText("Editor");
  await expect(page.locator(".brand-icon")).toHaveCount(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
});

test("switches a running Capture without changing its stable identity", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);
  await page.getByRole("link", { name: /claude Managed run/u }).click();

  await expect(page.getByRole("heading", { name: "claude" })).toBeVisible();
  await expect(page.getByText("/Users/example/project", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Current Environment")).toHaveValue("work");
  await page.getByLabel("Current Environment").selectOption("system_transparent");
  await expect(page.getByLabel("Current Environment")).toHaveValue("system_transparent");
  await expect(page.getByRole("status")).toContainText(/applied|new requests/iu);
  await expect(page.getByText("claude", { exact: true }).first()).toBeVisible();
  expect(errors).toEqual([]);
});

test("creates a transparent manual capture without delivering a Root", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);
  await page.getByRole("button", { name: "New capture" }).click();

  const dialog = page.getByRole("dialog", { name: "Create manual capture" });
  await dialog.getByLabel("Name").fill("Design editor");
  await dialog.getByLabel("Environment").selectOption("system_transparent");
  await dialog.getByRole("button", { name: "Review proxy details" }).click();
  await expect(dialog.getByText(/does not deliver a Root certificate/iu)).toBeVisible();
  await dialog.getByRole("button", { name: "Create app proxy" }).click();

  await expect(page.getByRole("heading", { name: "Proxy credentials ready" })).toBeVisible();
  await expect(page.getByText("Proxy", { exact: true })).toBeVisible();
  await expect(page.getByText("Username", { exact: true })).toBeVisible();
  await expect(page.getByText("Password", { exact: true })).toBeVisible();
  await expect(page.getByText("Root certificate", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("table")).toContainText("Design editor");
  expect(errors).toEqual([]);
});

test("reviews impact before atomically publishing an Environment", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "environments");
  await page.getByRole("button", { name: "New" }).click();

  const dialog = page.getByRole("dialog", { name: "Create Environment" });
  await dialog.getByLabel("Name").fill("Review lab");
  await dialog.getByLabel("Stable ID").fill("review-lab");
  await dialog.getByRole("button", { name: /Claude/u }).click();
  await dialog.getByLabel("Authentication").selectOption("anthropic-work");
  await dialog.getByRole("button", { name: "Review changes" }).click();
  let impact = dialog.getByRole("group", { name: "Impact preview" });
  await expect(impact).toBeVisible();
  await expect(impact).toContainText("Restart");
  await impact.getByRole("button", { name: "Back" }).click();
  await dialog.getByLabel("Name").fill("Review lab revised");
  await dialog.getByRole("button", { name: "Review changes" }).click();
  impact = dialog.getByRole("group", { name: "Impact preview" });
  await expect(impact).toBeVisible();
  await dialog.getByRole("button", { name: "Publish revision" }).click();

  await expect(page.getByRole("heading", { name: "Review lab revised" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "https://api.anthropic.com" })).toBeVisible();
  await expect(page.getByText("Anthropic Messages", { exact: true })).toBeVisible();
  await expect(page.getByText("Managed account", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("connects a managed account without ever rendering the credential", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  const secret = "sk-preview-secret-must-not-render";
  await openPreview(page, "accounts");
  await page.getByRole("button", { name: "Add account" }).click();

  const dialog = page.getByRole("dialog", { name: "Connect an account" });
  await dialog.getByRole("button", { name: /OpenAI/u }).click();
  await dialog.getByLabel("Name").fill("OpenAI Preview");
  await dialog.getByLabel("API key").fill(secret);
  await dialog.getByRole("button", { name: "Connect account" }).click();

  await expect(page.getByRole("table")).toContainText("OpenAI Preview");
  await expect(page.getByRole("table")).toContainText("Ready");
  await expect(page.locator("body")).not.toContainText(secret);
  expect(errors).toEqual([]);
});

test("opens a request through its frozen Environment to the exact attempt", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "captures/requests");
  await page.getByRole("link", { name: /Claude request/u }).click();

  await expect(page.getByRole("heading", { name: "Request trace" })).toBeVisible();
  await expect(page.getByText("work · r3", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-endpoint", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-messages", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-official", { exact: true })).toBeVisible();
  await expect(page.getByText("attempt-preview", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("makes open network policy explicit and resolves the approval inbox", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "policies/approvals");

  const policy = page.getByRole("radiogroup", { name: "Default network mode" });
  await expect(policy.getByRole("radio", { name: "Ask" })).toHaveAttribute("aria-checked", "true");
  await policy.getByRole("radio", { name: "Open" }).click();
  await expect(policy.getByRole("radio", { name: "Open" })).toHaveAttribute("aria-checked", "true");
  await expect(page.getByText(/explicit visible operator rule/iu)).toBeVisible();

  await expect(page.getByText("example.com:443", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Allow this connection once" }).click();
  await expect(page.getByText("Nothing is waiting", { exact: true })).toBeVisible();
  await expect(page.locator(".pending-link strong")).toHaveText("0");
  expect(errors).toEqual([]);
});

test("holds network authoritatively and changes locale without losing context", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);
  await page.getByRole("button", { name: "Hold network" }).click();
  await expect(page.getByRole("button", { name: "Resume network" })).toBeVisible();
  await page.getByRole("button", { name: "中文" }).click();
  await expect(page.getByRole("heading", { exact: true, name: "捕获" })).toBeVisible();
  await expect(page.getByRole("link", { name: "环境" })).toBeVisible();
  expect(errors).toEqual([]);
});

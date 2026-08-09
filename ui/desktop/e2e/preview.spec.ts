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

test("keeps the current Capture choice separate from the next-run workspace default", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);
  await page.getByRole("link", { name: /claude Managed run/u }).click();

  await expect(page.getByLabel("Current Environment")).toHaveValue("work");
  await expect(page.getByText("Used by future runs in this workspace")).toBeVisible();
  await page.getByRole("button", { name: "Clear default" }).click();
  await expect(page.getByLabel("Current Environment")).toHaveValue("work");
  await expect(page.getByText("This changes only the current run")).toBeVisible();
  await page.getByRole("button", { name: "Use for future runs" }).click();
  await expect(page.getByText("Used by future runs in this workspace")).toBeVisible();
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
  await dialog.getByText("Authentication", { exact: true }).locator("..").getByRole("combobox").selectOption("anthropic-work");
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

test("changes an existing passthrough Environment to a managed account", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "environments/work");

  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page
    .getByLabel("Authentication for https://api.anthropic.com")
    .selectOption("anthropic-work");
  await page.getByRole("button", { name: "Review changes" }).click();

  const impact = page.getByRole("group", { name: "Impact preview" });
  await expect(impact).toBeVisible();
  await impact.getByRole("button", { name: "Publish revision" }).click();
  await expect(page.getByText("Managed account", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText(
    "The local runtime returned an incompatible response.",
  );
  expect(errors).toEqual([]);
});

test("creates a useful Claude inspection Environment without requiring another account", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "environments");
  await page.getByRole("button", { name: "New" }).click();

  const dialog = page.getByRole("dialog", { name: "Create Environment" });
  await dialog.getByLabel("Name").fill("Claude inspection");
  await dialog.getByLabel("Stable ID").fill("claude-inspection");
  await expect(dialog.getByText("Authentication", { exact: true }).locator("..").getByRole("combobox")).toHaveValue("");
  await dialog.getByRole("button", { name: "Review changes" }).click();
  await dialog.getByRole("button", { name: "Publish revision" }).click();

  await expect(page.getByRole("heading", { name: "Claude inspection" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "https://api.anthropic.com" })).toBeVisible();
  await expect(page.getByText("Observe, no changes", { exact: true })).toBeVisible();
  await expect(page.getByText("Client login", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("connects a Claude OAuth account without ever rendering the credential", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  const secret = "oauth-preview-secret-must-not-render";
  await openPreview(page, "accounts");
  await page.getByRole("button", { name: "Add account" }).click();

  const dialog = page.getByRole("dialog", { name: "Connect an account" });
  await dialog.getByRole("button", { name: /Claude OAuth token/u }).click();
  await dialog.getByLabel("Name").fill("Claude OAuth Preview");
  await dialog.getByLabel("Claude OAuth token").fill(secret);
  await expect(dialog).toContainText("does not refresh it automatically");
  await dialog.getByRole("button", { name: "Connect account" }).click();

  await expect(page.getByRole("table")).toContainText("Claude OAuth Preview");
  await expect(page.getByRole("table")).toContainText("Claude OAuth token");
  await expect(page.getByRole("table")).toContainText("Ready");
  await expect(page.locator("body")).not.toContainText(secret);

  const row = page.getByRole("row").filter({ hasText: "Claude OAuth Preview" });
  await row.getByRole("button", { name: "Delete" }).click();
  const deleteDialog = page.getByRole("dialog", { name: "Delete Claude OAuth Preview?" });
  await expect(deleteDialog).toContainText("permanently removes the stored credential");
  await deleteDialog.getByRole("button", { name: "Delete account" }).click();
  await expect(page.getByRole("table")).not.toContainText("Claude OAuth Preview");
  expect(errors).toEqual([]);
});

test("opens a request through its frozen Environment to the exact attempt", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "captures/requests");
  await expect(page.getByText("Succeeded", { exact: true }).first()).toBeVisible();
  await expect(page.locator(".request-link small").first()).toHaveCSS("display", "block");
  await expect(page.locator(".request-table")).not.toContainText("captures.state.succeeded");
  await page.getByRole("link", { name: /Claude request/u }).first().click();

  await expect(page.getByRole("heading", { name: "Request trace" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to capture" })).toBeVisible();
  await expect(page.getByText("Request 2 of 2", { exact: true })).toBeVisible();
  await expect(page.getByText("work · r3", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-endpoint · r2", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-messages · r2", { exact: true })).toBeVisible();
  await expect(page.getByText("claude-official · r2", { exact: true })).toBeVisible();
  await expect(page.getByText("anthropic-work", { exact: true })).toBeVisible();
  await expect(page.getByText("r4", { exact: true })).toBeVisible();
  await expect(page.getByText("7", { exact: true })).toBeVisible();
  await expect(page.getByText("https://api.anthropic.com", { exact: true })).toBeVisible();
  await expect(page.getByText("egress-preview", { exact: true })).toBeVisible();
  await expect(page.getByText("attempt-preview", { exact: true })).toBeVisible();
  await expect(page.getByText(/Completed .* 384 out .* 192 in/u)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Request snapshot" })).toBeVisible();
  await expect(page.getByText("Inspect the current package and summarize the failing test.")).toBeVisible();
  await expect(page.getByText("System context marker", { exact: false })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Inspection plan" })).toBeVisible();
  await expect(page.getByText("Read the package manifest", { exact: true })).toBeVisible();
  await expect(page.getByText("pnpm test", { exact: true })).toBeVisible();
  await expect(page.getByText("Image reference not loaded: Remote diagram", { exact: true })).toBeVisible();
  await expect(page.locator(".markdown-evidence img")).toHaveCount(0);
  await page.locator(".context-disclosure > summary").click();
  await expect(page.getByText(/System context marker/iu)).toBeVisible();
  await expect(page.getByText("read_file", { exact: true })).toBeVisible();
  await expect(page.getByText("Proposed by the model", { exact: true })).toBeVisible();
  await expect(page.getByText("120", { exact: true })).toBeVisible();
  await page.getByTitle("exchange-preview-earlier").click();
  await expect(page.getByText("Request 1 of 2", { exact: true })).toBeVisible();
  await expect(page.getByText("List the packages in this workspace.", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText("/Users/");
  expect(errors).toEqual([]);
});

test("makes monitor policy explicit and resolves the approval inbox", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "policies/approvals");

  const policy = page.getByRole("radiogroup", { name: "Default network mode" });
  await expect(policy.getByRole("radio", { name: "Monitor" })).toHaveAttribute("aria-checked", "true");
  await policy.getByRole("radio", { name: "Ask" }).click();
  await expect(policy.getByRole("radio", { name: "Ask" })).toHaveAttribute("aria-checked", "true");
  await policy.getByRole("radio", { name: "Monitor" }).click();
  await expect(policy.getByRole("radio", { name: "Monitor" })).toHaveAttribute("aria-checked", "true");
  await expect(page.getByText(/recording connection and egress evidence/iu)).toBeVisible();

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

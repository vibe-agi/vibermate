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
  await expect(page.locator('[aria-current="page"]')).toHaveCount(1);
  await expect(page.locator('.primary-nav [aria-current="page"]')).toHaveCount(1);
  const running = page.locator(".capture-table-panel-running");
  await expect(running).toContainText("Running now · 8 captures");
  await expect(running).toContainText("claude");
  await expect(running).toContainText("Editor");
  await expect(page.locator(".capture-table-panel-history")).toContainText("History · 3 captures");
  await expect(page.locator(".brand-icon")).toHaveCount(8);
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
  await expect(page.locator(".capture-table-panel-running table")).toContainText("Design editor");
  expect(errors).toEqual([]);
});

test("revokes one manual proxy login with its current state tag and keeps evidence", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page);
  await page.getByRole("link", { name: /Editor Manual capture/u }).click();

  await expect(page.getByRole("heading", { name: "Editor" })).toBeVisible();
  await expect(page.getByText("Manual app source", { exact: true })).toBeVisible();
  await expect(page.getByText(/each Exchange remains a separate conversation/iu)).toBeVisible();
  await page.getByRole("button", { name: "Revoke login" }).click();
  const confirmation = page.getByRole("group", { name: "Revoke Editor?" });
  await expect(confirmation).toContainText("Saved conversations and activity evidence remain available");
  await expect(confirmation.getByRole("button", { name: "Cancel" })).toBeFocused();
  await confirmation.getByRole("button", { name: "Cancel" }).click();
  await expect(confirmation).toHaveCount(0);

  await page.getByRole("button", { name: "Revoke login" }).click();
  await page.getByRole("group", { name: "Revoke Editor?" }).getByRole("button", { name: "Revoke login" }).click();
  await expect(page.getByRole("status")).toContainText("Proxy login revoked");
  await expect(page.getByText("Revoked", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Revoke login" })).toHaveCount(0);

  await page.getByRole("link", { name: "Back to captures" }).click();
  await expect(page.locator(".capture-table-panel-running")).not.toContainText("Editor");
  await expect(page.locator(".capture-table-panel-history")).toContainText("Editor");
  await expect(page.locator(".capture-table-panel-history")).toContainText("Revoked");
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

test("adds a second upstream Endpoint and keeps account choices endpoint-scoped", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "environments/work");

  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByLabel("Add an endpoint").selectOption("target.codex.official");
  await page.locator(".endpoint-catalog-adder").getByRole("button", { exact: true, name: "Add" }).click();

  const anthropic = page.getByLabel("Authentication for https://api.anthropic.com");
  const chatGPT = page.getByLabel("Authentication for https://chatgpt.com");
  await expect(anthropic.locator("option")).toHaveCount(2);
  await expect(chatGPT.locator("option")).toHaveCount(1);
  await expect(chatGPT).toHaveValue("");
  await page.getByRole("button", { name: "Review changes" }).click();
  await page.getByRole("group", { name: "Impact preview" }).getByRole("button", { name: "Publish revision" }).click();

  await expect(page.getByRole("heading", { name: "https://chatgpt.com" })).toBeVisible();
  expect(errors).toEqual([]);
});

test("creates a useful Claude inspection Environment without requiring another account", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "environments");
  await page.getByRole("button", { name: "New" }).click();

  const dialog = page.getByRole("dialog", { name: "Create Environment" });
  await dialog.getByLabel("Name").fill("Claude inspection");
  await dialog.getByLabel("Stable ID").fill("claude-inspection");
  await expect(dialog.getByLabel("Tool policy")).toHaveValue("observe");
  await expect(dialog).toContainText(
    "Default. Tools continue without interruption; request evidence still records what happened.",
  );
  await dialog.getByLabel("Tool policy").selectOption("review");
  await expect(dialog).toContainText(
    "Unproven actions wait for approval. Verified structured file actions inside this workspace continue automatically.",
  );
  await expect(dialog.getByText("Authentication", { exact: true }).locator("..").getByRole("combobox")).toHaveValue("");
  await dialog.getByRole("button", { name: "Review changes" }).click();
  await dialog.getByRole("button", { name: "Publish revision" }).click();

  await expect(page.getByRole("heading", { name: "Claude inspection" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "https://api.anthropic.com" })).toBeVisible();
  await expect(page.getByText("Observe, no changes", { exact: true })).toBeVisible();
  await expect(page.getByText("Client login", { exact: true })).toBeVisible();
  await expect(page.getByText("Review unproven actions", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("connects a Claude OAuth account without ever rendering the credential", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  const secret = "oauth-preview-secret-must-not-render";
  await openPreview(page, "accounts");
  await page.locator(".page-heading").getByRole("button", { name: "Add account" }).click();

  const dialog = page.getByRole("dialog", { name: "Connect an account" });
  await dialog.getByLabel("Upstream Endpoint").selectOption("target.claude.official");
  await dialog.getByRole("button", { name: /Claude OAuth token/u }).click();
  await dialog.getByLabel("Name").fill("Claude OAuth Preview");
  await dialog.getByLabel("Claude OAuth token").fill(secret);
  await expect(dialog).toContainText("does not refresh it automatically");
  await dialog.getByRole("button", { name: "Connect account" }).click();

  const endpoint = page.locator(".endpoint-account-group").filter({ hasText: "Anthropic API" });
  await expect(endpoint.getByRole("table")).toContainText("Claude OAuth Preview");
  await expect(endpoint.getByRole("table")).toContainText("Claude OAuth token");
  await expect(endpoint.getByRole("table")).toContainText("Ready");
  await expect(page.locator("body")).not.toContainText(secret);

  const row = page.getByRole("row").filter({ hasText: "Claude OAuth Preview" });
  await row.getByRole("button", { name: "Delete" }).click();
  const deleteDialog = page.getByRole("dialog", { name: "Delete Claude OAuth Preview?" });
  await expect(deleteDialog).toContainText("permanently removes the stored credential");
  await deleteDialog.getByRole("button", { name: "Delete account" }).click();
  await expect(endpoint.getByRole("table")).not.toContainText("Claude OAuth Preview");
  expect(errors).toEqual([]);
});

test("creates an upstream Endpoint before placing an account beneath it", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "accounts");
  await page.getByRole("button", { name: "Add Endpoint" }).click();

  const endpointDialog = page.getByRole("dialog", { name: "Add an upstream Endpoint" });
  await endpointDialog.getByLabel("Name").fill("Team relay");
  await endpointDialog.getByLabel("Origin").fill("https://relay.example.com");
  await endpointDialog.getByLabel("Protocol family").selectOption("openai_compatible");
  await endpointDialog.getByRole("button", { name: "Create Endpoint" }).click();

  const endpoint = page.locator(".endpoint-account-group").filter({ hasText: "Team relay" });
  await expect(endpoint).toContainText("https://relay.example.com");
  await expect(endpoint).toContainText("No accounts on this Endpoint.");
  await endpoint.getByRole("button", { name: "Add account" }).click();

  const accountDialog = page.getByRole("dialog", { name: "Connect an account" });
  await expect(accountDialog.getByLabel("Upstream Endpoint")).toHaveValue(/target\.custom\.openai\./u);
  await expect(accountDialog.getByRole("button", { name: /OpenAI API key/u })).toHaveAttribute("aria-pressed", "true");
  await accountDialog.getByLabel("Name").fill("Team OpenAI");
  await accountDialog.getByLabel("API key").fill("preview-secret-never-render");
  await accountDialog.getByRole("button", { name: "Connect account" }).click();

  await expect(endpoint.getByRole("table")).toContainText("Team OpenAI");
  await expect(page.locator("body")).not.toContainText("preview-secret-never-render");
  expect(errors).toEqual([]);
});

test("opens one captured conversation entry and inspects every turn", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "captures/requests");
  await expect(page.getByRole("heading", { name: "Conversations" })).toBeVisible();
  await expect(page.getByText("3 turns", { exact: true })).toBeVisible();
  await expect(page.locator('[data-conversation-key="capture-run:run-preview"]')).toHaveCount(1);
  await expect(page.locator(".conversation-link small").first()).toHaveCSS("display", "block");
  await page.locator('[data-conversation-key="capture-run:run-preview"] .conversation-link').click();

  await expect(page.getByRole("heading", { name: "Run conversation" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to capture" })).toBeVisible();
  const runContext = page.locator(".run-context");
  await runContext.locator("summary").click();
  await expect(runContext.getByText("work · r3", { exact: true })).toBeVisible();
  await expect(runContext.getByText("claude-endpoint · r2", { exact: true })).toBeVisible();
  await expect(runContext.getByText("claude-messages · r2", { exact: true })).toBeVisible();
  await expect(runContext.getByText("claude-official · r2", { exact: true })).toBeVisible();

  const turnMap = page.getByRole("navigation", { name: "Jump to a turn" });
  await expect(turnMap).toBeVisible();
  await expect(turnMap.getByRole("button")).toHaveCount(27);
  await expect(turnMap.locator('[aria-current="step"]')).toHaveCount(1);
  await expect(turnMap).toHaveCSS("position", "sticky");
  const turns = page.locator(".conversation-turn");
  await expect(turns).toHaveCount(27);
  const firstTurn = await turns.first().boundingBox();
  const lastTurn = await turns.last().boundingBox();
  expect(firstTurn).not.toBeNull();
  expect(lastTurn).not.toBeNull();
  expect(lastTurn!.y).toBeGreaterThan(firstTurn!.y);

  const completedTurn = page.locator("#run-turn-exchange-preview");
  await expect(completedTurn).toContainText("Inspect the current package and summarize the failing test.");
  await expect(completedTurn.getByRole("heading", { name: "Inspection plan" })).toBeVisible();
  await expect(completedTurn.getByText("Read the package manifest", { exact: true })).toBeVisible();
  await expect(completedTurn.getByText("pnpm test", { exact: true })).toBeVisible();
  await expect(completedTurn.getByText("Image reference not loaded: Remote diagram", { exact: true })).toBeVisible();
  await expect(completedTurn.locator(".markdown-evidence img")).toHaveCount(0);

  const evidence = completedTurn.locator(".turn-evidence");
  await evidence.locator("summary").click();
  await expect(evidence.getByText("anthropic-work", { exact: true })).toBeVisible();
  await expect(evidence.getByText("https://api.anthropic.com", { exact: true })).toBeVisible();
  await expect(evidence.getByText("egress-preview", { exact: true })).toBeVisible();
  await expect(evidence.getByText("attempt-preview", { exact: true })).toBeVisible();
  await expect(evidence.getByText(/Completed .* 384 out .* 192 in/u)).toBeVisible();

  await completedTurn.getByRole("button", { name: "View full snapshot" }).click();
  await expect(completedTurn.getByText("Full client snapshot · total 3 · inherited 2", { exact: true })).toBeVisible();
  await expect(completedTurn.getByText("Provider reasoning state forwarded opaquely; content not recorded · 96 bytes", { exact: true })).toBeVisible();
  await completedTurn.locator(".context-disclosure > summary").click();
  await expect(completedTurn.getByText(/System context marker/iu)).toBeVisible();
  const wrappedEvidence = completedTurn.locator(".context-disclosure .markdown-evidence pre");
  await expect(wrappedEvidence).toBeVisible();
  expect(await wrappedEvidence.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);

  const pendingTurn = page.locator("#run-turn-exchange-preview-pending");
  await expect(pendingTurn.getByText("Review the latest runtime change and summarize any remaining risk.", { exact: true })).toBeVisible();
  await expect(pendingTurn.getByText("Waiting for the response", { exact: true })).toBeVisible();
  await expect(pendingTurn.getByText("In progress", { exact: true })).toBeVisible();

  const checkpointTurn = page.locator("#run-turn-exchange-preview-earlier");
  await expect(checkpointTurn.getByText("Full client checkpoint · showing the latest message · total 4", { exact: true })).toBeVisible();
  await expect(checkpointTurn.getByText("List the packages in this workspace.", { exact: true })).toBeVisible();
  await expect(checkpointTurn.getByText(/System context marker/iu)).toHaveCount(0);
  await checkpointTurn.getByRole("button", { name: "View full snapshot" }).click();
  await expect(checkpointTurn.getByText(/System context marker/iu)).toHaveCount(0);
  await checkpointTurn.locator(".context-disclosure > summary").click();
  await expect(checkpointTurn.getByText(/System context marker/iu)).toBeVisible();
  await checkpointTurn.getByRole("button", { name: "Hide prior context" }).click();
  await expect(checkpointTurn.getByText(/System context marker/iu)).toHaveCount(0);

  await turns.nth(12).evaluate((element) => element.scrollIntoView({ block: "start" }));
  await expect.poll(async () => page.evaluate(() => {
    const main = document.querySelector("#main-content");
    const map = document.querySelector(".turn-map");
    if (!(main instanceof HTMLElement) || !(map instanceof HTMLElement)) return -1;
    return Math.round(map.getBoundingClientRect().top - main.getBoundingClientRect().top);
  })).toBe(8);
  await expect.poll(async () => turnMap.locator('[aria-current="step"]').getAttribute("aria-label"))
    .toContain("13");

  await turnMap.getByRole("button", { name: /Approval expired/u }).click();
  await expect(page.locator(".conversation-turn:focus")).toContainText("Approval expired");
  await expect(turnMap.getByRole("button", { name: /Approval expired/u })).toHaveAttribute("aria-current", "step");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true);
  await expect(page.locator("body")).not.toContainText("/Users/");
  expect(errors).toEqual([]);
});

test("keeps a large Turn map bounded and follows growth only while the reader stays near the bottom", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "captures/requests");
  await page.locator('[data-conversation-key="capture-run:run-preview"] .conversation-link').click();
  await expect(page.locator(".conversation-turn")).toHaveCount(27);

  const main = page.locator("#main-content");
  await main.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
    element.dispatchEvent(new Event("scroll"));
  });
  await page.locator(".run-conversation").evaluate((element) => {
    const appended = document.createElement("article");
    appended.className = "conversation-turn";
    appended.style.minHeight = "420px";
    appended.textContent = "Appended latest turn";
    element.append(appended);
  });
  await expect.poll(async () => main.evaluate((element) =>
    Math.round(element.scrollHeight - element.scrollTop - element.clientHeight),
  )).toBeLessThanOrEqual(1);

  await main.evaluate((element) => {
    element.scrollTop = Math.max(0, element.scrollHeight - element.clientHeight - 600);
    element.dispatchEvent(new Event("scroll"));
  });
  const readingPosition = await main.evaluate((element) => element.scrollTop);
  await page.locator(".run-conversation").evaluate((element) => {
    const appended = document.createElement("article");
    appended.className = "conversation-turn";
    appended.style.minHeight = "360px";
    appended.textContent = "Another appended turn";
    element.append(appended);
  });
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  expect(await main.evaluate((element) => element.scrollTop)).toBe(readingPosition);

  const mapList = page.getByRole("navigation", { name: "Jump to a turn" }).locator("ol");
  await mapList.evaluate((list) => {
    for (let index = 0; index < 300; index += 1) {
      const item = document.createElement("li");
      const button = document.createElement("button");
      button.className = "turn-dot turn-dot-succeeded";
      button.type = "button";
      item.append(button);
      list.append(item);
    }
  });
  expect(await mapList.evaluate((element) => ({
    bounded: element.clientHeight <= 34,
    scrollable: element.scrollHeight > element.clientHeight,
    noHorizontalOverflow: element.scrollWidth <= element.clientWidth + 1,
  }))).toEqual({ bounded: true, scrollable: true, noHorizontalOverflow: true });
  expect(errors).toEqual([]);
});

test("keeps an early unsupported request diagnosable without retained content", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await openPreview(page, "captures/requests");
  const row = page.getByRole("row").filter({ hasText: "Unsupported request content" });
  await expect(row).toBeVisible();
  await row.getByRole("link").click();

  await expect(page.getByRole("heading", { name: "Run conversation" })).toBeVisible();
  await expect(page.getByText("Unsupported request content", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("ViberMate stopped before sending this request upstream.", { exact: true })).toBeVisible();
  await expect(page.getByText("messages · $.messages[2].content[0].type", { exact: true })).toBeVisible();
  await expect(page.getByText("The local runtime returned an incompatible response.", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Content was not recorded", { exact: true })).toHaveCount(0);
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

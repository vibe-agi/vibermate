import { expect, test, type Page } from "@playwright/test";

function collectBrowserErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(`console: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => errors.push(`page: ${error.message}`));
  return errors;
}

test("opens the explicit browser preview with one current focus", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#overview");

  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByText("Development preview data")).toBeVisible();
  await expect(page.locator(".focus-stage")).toHaveCount(1);
  await expect(page.locator(".primary-nav a")).toHaveCount(8);
  await expect(page.getByRole("link", { name: /^Pending/ })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Network access confirmation required" }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= globalThis.innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
});

test("decides the recognized-client Root handoff from the shared queue", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#overview");
  await page.getByRole("link", { name: /^Policy/ }).click();
  const clientApproval = page.locator(
    '[data-approval-id="approval-client-root-sample"]',
  );
  await clientApproval.getByRole("link", { name: "Open approval" }).click();

  await expect(
    page.getByRole("heading", {
      name: "Allow a recognized client to use the local Root?",
    }),
  ).toBeVisible();
  await expect(clientApproval.getByText("Client trust")).toBeVisible();
  await expect(
    page.getByText("/Applications/Claude.app/Contents/MacOS/claude"),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Launch without the VibeMate Root" })
    .click();
  await expect(page.locator(".pending-link span")).toHaveText("2");
  await expect(
    page.getByRole("heading", {
      name: "Allow a recognized client to use the local Root?",
    }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("uses authoritative hold state before saying disconnection is safe", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#overview");
  await page.getByRole("button", { name: "Enter offline hold" }).click();

  const focus = page.locator(".focus-stage");
  await expect(
    focus.getByRole("heading", { name: "Safe to disconnect" }),
  ).toBeVisible();
  await expect(focus.getByText("Yes", { exact: true })).toBeVisible();
  await focus.getByRole("button", { name: "Resume online" }).click();
  await expect(
    page.getByRole("button", { name: "Enter offline hold" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("lists existing tools and creates the next one without asking for a name", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#access");
  const directory = page.locator(".access-directory-list");
  await expect(directory.getByText("Work Claude", { exact: true })).toBeVisible();
  await expect(directory.locator("li")).toHaveCount(1);

  await page.getByRole("button", { name: /^Work Claude/u }).click();

  await expect(
    page.getByRole("heading", { name: "Accounts and routes" }),
  ).toBeVisible();
  const currentPath = page.getByRole("list", {
    name: "Where new requests go",
  });
  await expect(currentPath).toBeVisible();
  await expect(currentPath.getByText("Work Claude", { exact: true })).toBeVisible();
  await expect(currentPath.getByText("Demo route", { exact: true })).toBeVisible();
  await expect(
    currentPath.getByText("example-model", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("New requests", { exact: true })).toBeVisible();
  const liveRoute = page.locator(".access-current-path");
  await expect(
    liveRoute.getByText("Fingerprint: Pass through current client (default)", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("OpenAI-compatible service", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("https://gateway.example/v1")).toBeVisible();
  const launch = page.locator(".access-launch-panel");
  await expect(
    launch.getByRole("heading", { name: "Start Work Claude" }),
  ).toBeVisible();
  await expect(
    launch.getByText("vibermate run -- claude", { exact: true }),
  ).toBeVisible();
  await expect(launch.getByText("Desktop only", { exact: true })).toBeVisible();
  await expect(
    page
      .locator(".access-upstream-section")
      .getByRole("button", { name: "Add" }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "Apply Access" }),
  ).toHaveCount(0);
  await expect(page.getByLabel("API Key")).toBeEnabled();
  await expect(page.getByLabel("Access ID")).toHaveCount(0);
  await expect(page.getByLabel("Expected revision")).toHaveCount(0);

  const originalRoute = page
    .locator(".access-upstream-list > li")
    .filter({ hasText: "Use the tool's current login" });
  await originalRoute
    .getByRole("button", { name: "Set as current route" })
    .click();
  await expect(
    currentPath.getByText("Observe only", { exact: true }),
  ).toBeVisible();
  await expect(
    currentPath.getByText("Keep the tool's selected model", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByLabel("API Key")).toHaveCount(0);

  await page.getByRole("button", { name: "Add AI Access" }).click();
  await expect(page.getByLabel("Client protocol")).toHaveValue(
    "openai-responses",
  );
  await expect(page.getByLabel("Client API address")).toHaveValue(
    "https://chatgpt.com",
  );
  await expect(
    page.getByRole("button", { name: /^ChatGPT sign-in/u }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: /^OpenAI API key/u }).click();
  await expect(page.getByLabel("Client API address")).toHaveValue(
    "https://api.openai.com",
  );
  await page.getByRole("button", { name: /^ChatGPT sign-in/u }).click();
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Codex");
  await expect(
    page.getByRole("button", { name: /^Use the tool's current login/u }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByText(
      "Supported model requests keep their original shape and streaming. VibeMate records operational evidence, sends only to the exact original service, and stops unsupported request types locally.",
    ),
  ).toBeVisible();
  expect(
    await page.evaluate(() => {
      const review = document.querySelector(".access-review");
      const actions = document.querySelector(".access-create-actions");
      return (
        review !== null &&
        actions !== null &&
        Boolean(
          review.compareDocumentPosition(actions) &
            Node.DOCUMENT_POSITION_FOLLOWING,
        )
      );
    }),
  ).toBe(true);
  await expect(page.getByLabel("API Key")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Save and enable" }),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: /^Use another model service/u })
    .click();
  await page
    .getByRole("button", { name: /^OpenAI-compatible service/u })
    .click();
  await expect(page.getByLabel("Model", { exact: true })).toHaveValue("");
  await expect(
    page.getByRole("button", { name: "Save and enable" }),
  ).toBeDisabled();
  await page
    .locator(".access-destination-options")
    .getByRole("button", { name: /^OpenAI API/u })
    .click();
  await page.getByLabel("API Key").fill("preview-provider-key");
  await expect(page.getByLabel("Access ID")).toHaveCount(0);
  await expect(page.getByLabel("Expected revision")).toHaveCount(0);
  await page.getByRole("button", { name: "Save and enable" }).click();

  await expect(directory.locator("li")).toHaveCount(2);
  await expect(directory.getByText("Work Claude", { exact: true })).toBeVisible();
  await expect(
    directory.getByText("Codex", { exact: true }),
  ).toBeVisible();
  const personal = directory.getByRole("button", {
    name: /^Codex/u,
  });
  await expect(personal).toContainText("Codex CLI setup");
  await expect(personal).toContainText("Configured");
  await expect(
    page.getByText("The connection was saved and enabled."),
  ).toBeVisible();
  await expect(
    launch.getByRole("heading", { name: "Start Codex" }),
  ).toBeVisible();
  await expect(
    launch.getByText("vibermate run -- codex", { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => ({
      local: globalThis.localStorage.length,
      session: globalThis.sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
  expect(errors).toEqual([]);
});

test("stops new Access traffic and restores it without losing configuration", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#access");
  await page.getByRole("button", { name: /^Work Claude/u }).click();

  await page.getByRole("button", { name: "Disable" }).click();
  await expect(
    page.getByText(
      "New requests will stop. Requests already in progress may finish.",
    ),
  ).toBeVisible();
  await page.getByRole("button", { name: "Disable Access" }).click();

  await expect(page.getByText("This Access is disabled")).toBeVisible();
  await expect(page.getByRole("button", { name: "Enable" })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Accounts and routes" }),
  ).toBeVisible();
  await expect(page.getByText("https://gateway.example/v1")).toBeVisible();

  await page.getByRole("button", { name: "Enable" }).click();
  await expect(page.getByRole("button", { name: "Disable" })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Start Work Claude" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("opens the evidence trail from the current request path", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#access");
  await page.getByRole("button", { name: /^Work Claude/u }).click();
  await page.getByRole("link", { name: "View activity" }).click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("keeps all eight production routes addressable without simulating features", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  const routes = [
    ["overview", "Overview"],
    ["dashboards", "Dashboards"],
    ["access", "AI Access"],
    ["activity", "Activity"],
    ["quality", "Quality"],
    ["extensions", "Extensions"],
    ["policies/approvals", "Policy"],
    ["settings", "Settings"],
  ] as const;
  for (const [route, heading] of routes) {
    await page.goto(`/?preview=1#${route}`);
    await expect(
      page.getByRole("heading", { level: 1, name: heading }),
    ).toBeVisible();
  }

  await page.goto("/?preview=1#dashboards");
  await expect(page.getByText("Not available in this build")).toBeVisible();
  await page.getByRole("link", { name: "Return to Overview" }).click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

  await page.goto("/?preview=1#/approvals?selected=approval-network-sample");
  await expect(page.getByRole("heading", { name: "Policy" })).toBeVisible();
  await expect(page).toHaveURL(
    /#policies\/approvals\?selected=approval-network-sample$/u,
  );
  await page.goBack();
  await expect(
    page.getByRole("heading", { level: 1, name: "Overview" }),
  ).toBeVisible();
  await page.goto("/?preview=1#/system");
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await expect(page).toHaveURL(/#settings$/u);

  await page.goto("/?preview=1#/not-a-real-route");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page).toHaveURL(/#overview$/u);

  await page.goto("/?preview=1#overview?unexpected=discarded");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page).toHaveURL(/\?preview=1#overview$/u);

  await page.goto("/?preview=1#/overview/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page).toHaveURL(/\?preview=1#overview$/u);
  expect(errors).toEqual([]);
});

test("keeps terminal installation behind the native Desktop boundary", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#settings");

  const panel = page.locator(".terminal-command-panel");
  await expect(
    panel.getByRole("heading", { name: "Terminal command" }),
  ).toBeVisible();
  await expect(panel.getByText("Desktop only", { exact: true })).toBeVisible();
  await expect(
    panel.getByText(
      "Terminal setup is available only in the installed Desktop app.",
    ),
  ).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "Install terminal command" }),
  ).toHaveCount(0);
  await expect(panel.getByLabel("Command path")).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("opens every frozen ICM route without changing its object locator", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  const routes = [
    ["#overview", "Overview", "Overview", false],
    ["#access", "AI Access", "AI Access", false],
    ["#access/claude/routing", "Access routing", "AI Access", true],
    ["#activity/requests/ex204", "Request evidence", "Activity", false],
    ["#extensions/discover", "Discover extensions", "Extensions", true],
    ["#extensions/installed", "Installed extensions", "Extensions", true],
    [
      "#extensions/detail/prompt-polish",
      "Extension details",
      "Extensions",
      true,
    ],
    ["#quality/sites", "Quality sites", "Quality", true],
    ["#dashboards/system", "System dashboard", "Dashboards", true],
    ["#activity/requests", "Requests", "Activity", false],
    ["#policies/approvals", "Policy", "Policy", false],
    ["#settings/recovery", "Recovery settings", "Settings", true],
    ["#extensions/develop", "Develop extensions", "Extensions", true],
    [
      "#dashboards/extensions/agent-actions",
      "Extension dashboard",
      "Dashboards",
      true,
    ],
  ] as const;

  for (const [hash, heading, navigation, unavailable] of routes) {
    await page.goto(`/?preview=1${hash}`);
    await expect(
      page.getByRole("heading", { level: 1, name: heading }),
    ).toBeVisible();
    await expect(page.locator(".nav-item[aria-current='page']")).toContainText(
      navigation,
    );
    expect(
      await page.evaluate(() => ({
        hash: globalThis.location.hash,
        search: globalThis.location.search,
      })),
    ).toEqual({ hash, search: "?preview=1" });
    if (unavailable) {
      await expect(page.getByText("Not available in this build")).toBeVisible();
    } else if (hash === "#activity/requests/ex204") {
      await expect(page.getByText("provider_transport_failed")).toBeVisible();
      await expect(page.getByText("attempt-ex204-1")).toBeVisible();
      await expect(page.getByText("Not available in this build")).toHaveCount(0);
    } else if (hash === "#activity/requests") {
      await expect(page.getByText("Not available in this build")).toHaveCount(0);
    }
  }

  await page.reload();
  await expect(
    page.getByRole("heading", { level: 1, name: "Extension dashboard" }),
  ).toBeVisible();
  expect(await page.evaluate(() => globalThis.location.hash)).toBe(
    "#dashboards/extensions/agent-actions",
  );
  expect(errors).toEqual([]);
});

test("loads canonical Activity request summaries without raw evidence", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#activity/requests");

  const activityPanel = page.locator(".activity-panel");
  const rows = activityPanel.locator(".activity-table tbody tr");
  await expect(rows).toHaveCount(10);
  await expect(page.getByText("exchange-preview-5", { exact: true })).toBeVisible();
  await activityPanel.getByRole("button", { name: "Next page" }).click();
  await expect(
    page.getByText("exchange-preview-history-06", { exact: true }),
  ).toBeVisible();
  await activityPanel.getByRole("button", { name: "Previous page" }).click();
  await page.getByRole("button", { name: "Load more" }).click();
  await expect(activityPanel.locator(".compact-count")).toHaveText("40");
  await expect(page.getByText("reviewed", { exact: true })).toHaveClass(
    /neutral/u,
  );
  await expect(page.getByRole("button", { name: "Load more" })).toHaveCount(0);

  const rendered = await page.locator("#main-content").innerText();
  for (const forbidden of [
    "Access applied",
    "Approval requested",
    "Provider credential replaced",
    "Reason",
    "Where in the request",
    "raw_provider_reason",
  ]) {
    expect(rendered).not.toContain(forbidden);
  }
  expect(errors).toEqual([]);
});

test("keeps eight captured agents and high-volume traffic operable in dense tables", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/?preview=1#activity");

  const traffic = page.locator(".traffic-panel");
  await traffic.getByRole("tab", { name: /Captured programs\s*8/u }).click();
  await expect(traffic.locator(".traffic-table tbody tr")).toHaveCount(8);

  await traffic.getByRole("tab", { name: /Connections\s*36/u }).click();
  const rows = traffic.locator(".traffic-table tbody tr");
  await expect(rows).toHaveCount(10);
  await expect(rows.first()).toContainText("api.anthropic.com:443");
  await traffic.getByRole("button", { name: "Next page" }).click();
  await expect(rows).toHaveCount(10);
  await expect(rows.first()).toContainText("api.openai.com:443");

  const heights = await rows.evaluateAll((items) =>
    items.map((item) => item.getBoundingClientRect().height),
  );
  expect(Math.max(...heights)).toBeLessThanOrEqual(44);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= globalThis.innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
});

test("keeps the exact approval selected across navigation and reload", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#overview");
  await page.getByRole("button", { name: "Review 3 pending" }).click();

  await expect(page).toHaveURL(
    /#policies\/approvals\?selected=approval-network-sample$/u,
  );
  const networkApproval = page.locator(
    '[data-approval-id="approval-network-sample"]',
  );
  await expect(networkApproval).toHaveAttribute("data-selected", "true");
  await expect(networkApproval).toBeFocused();

  const toolApproval = page.locator(
    '[data-approval-id="approval-tool-sample"]',
  );
  await toolApproval.getByRole("link", { name: "Open approval" }).click();
  await expect(page).toHaveURL(
    /#policies\/approvals\?selected=approval-tool-sample$/u,
  );
  await expect(toolApproval).toBeFocused();
  const toolDecision = toolApproval.getByRole("button", {
    name: "Refuse these tool calls",
  });
  await toolDecision.focus();
  await page.waitForTimeout(2_100);
  await expect(toolDecision).toBeFocused();

  await page.reload();
  await expect(page).toHaveURL(
    /#policies\/approvals\?selected=approval-tool-sample$/u,
  );
  await expect(toolApproval).toBeFocused();

  await page.goBack();
  await expect(page).toHaveURL(
    /#policies\/approvals\?selected=approval-network-sample$/u,
  );
  await expect(networkApproval).toBeFocused();
  expect(errors).toEqual([]);
});

test("fails closed for invalid or vanished approval locators", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#policies/approvals?selected=%20unsafe%20");
  await expect(
    page.getByRole("heading", { name: "This link cannot be opened safely" }),
  ).toBeVisible();
  await expect(page.getByText("unsafe", { exact: true })).toHaveCount(0);
  await page
    .getByRole("link", { name: "Return to the approval queue" })
    .click();
  await expect(page).toHaveURL(/#policies\/approvals$/u);

  await page.goto(
    "/?preview=1#policies/approvals?selected=approval-no-longer-here",
  );
  await expect(
    page.getByRole("heading", {
      name: "This approval changed or is no longer available",
    }),
  ).toBeVisible();
  await expect(page.locator("[data-selected='true']")).toHaveCount(0);
  await page.getByRole("link", { name: "Return to the current queue" }).click();
  await expect(page).toHaveURL(/#policies\/approvals$/u);

  await page.goto("/?preview=1#access/%20unsafe%20/routing");
  await expect(
    page.getByRole("heading", { name: "This link cannot be opened safely" }),
  ).toBeVisible();
  await expect(page.locator(".locator-notice")).toBeFocused();
  await expect(page.getByText("unsafe", { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => globalThis.location.hash)).toBe(
    "#access/%20unsafe%20/routing",
  );
  await page
    .getByRole("link", { name: "Return to the current section" })
    .click();
  await expect(page).toHaveURL(/#access$/u);

  await page.goto("/?preview=1#extensions/discover?selected=not-allowed");
  await expect(
    page.getByRole("heading", { name: "This link cannot be opened safely" }),
  ).toBeVisible();
  await expect(page.locator(".locator-notice")).toBeFocused();
  await expect(page.getByText("not-allowed", { exact: true })).toHaveCount(0);

  await page.goto("/?preview=1#access/%E0%A4%A/routing");
  await expect(
    page.getByRole("heading", { name: "This link cannot be opened safely" }),
  ).toBeVisible();
  await expect(page.locator(".locator-notice")).toBeFocused();
  expect(await page.evaluate(() => globalThis.location.hash)).toBe(
    "#access/%E0%A4%A/routing",
  );
  expect(errors).toEqual([]);
});

test("restores the nested content scroll on browser history", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 1100, height: 420 });
  await page.goto("/?preview=1#activity");
  const content = page.locator("#main-content");
  const activityScroll = await content.evaluate((element) => {
    element.scrollTop = element.scrollHeight - element.clientHeight;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
    return element.scrollTop;
  });
  expect(activityScroll).toBeGreaterThan(0);

  await page.getByRole("link", { name: /^Policy/ }).click();
  await expect
    .poll(() => content.evaluate((element) => element.scrollTop))
    .toBe(0);
  await page.goBack();
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity" }),
  ).toBeVisible();
  await expect
    .poll(() => content.evaluate((element) => element.scrollTop))
    .toBe(activityScroll);

  await page.getByRole("button", { name: "Refresh" }).click();
  await expect
    .poll(() => content.evaluate((element) => element.scrollTop))
    .toBe(activityScroll);
  expect(errors).toEqual([]);
});

test("groups three terminals by stable workspace and switches later requests", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#activity");

  await expect(
    page.getByRole("heading", { name: "Machines & workspaces" }),
  ).toBeVisible();
  await expect(page.getByText("3 tools in workspace")).toBeVisible();
  await expect(page.getByText("alice", { exact: true })).toHaveCount(2);
  await expect(page.getByText("bob", { exact: true })).toBeVisible();
  await expect(
    page.getByText(
      "1 request is finishing on the previous route. New requests already use the selected route.",
    ),
  ).toBeVisible();

  const route = page.getByLabel("Route for new requests");
  await expect(
    route.locator('option[value="original-passthrough"]'),
  ).toBeDisabled();
  await expect(
    page.getByText(
      "Stop this workspace's running tools before switching between the client's current login and VibeMate-managed credentials. Start them again after selecting the route.",
    ),
  ).toBeVisible();
  await route.selectOption("work-secondary");
  await expect(route).toHaveValue("work-secondary");
  await expect(
    page.getByText("AI Access work · model gpt-5.6-sol · account 002"),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("hands off a manual app proxy once and keeps a secret-free observation card", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#activity");

  const panel = page.locator(".manual-capture-panel");
  await expect(
    panel.getByRole("heading", { name: "Manual app capture" }),
  ).toBeVisible();
  await panel.getByRole("button", { name: "Create app proxy" }).click();
  await panel.getByLabel("Name", { exact: true }).fill("Project terminal");
  await panel.getByRole("button", { name: "Review proxy details" }).click();

  await expect(panel.getByText("Confirm before creation")).toBeVisible();
  await expect(
    panel.getByRole("heading", { name: "Project terminal" }),
  ).toBeFocused();
  await expect(panel.getByText("AA:BB:CC:DD:EE:FF")).toBeVisible();
  await expect(panel.getByText("Shown once")).toHaveCount(0);
  await panel.getByRole("button", { name: "Create this proxy" }).click();

  const proxy = panel.getByLabel("Proxy address with password");
  await expect(proxy).toHaveValue(/capture:manual_/u);
  await expect(
    panel.getByRole("heading", { name: "Project terminal" }),
  ).toBeFocused();
  await expect(
    panel.getByRole("button", {
      name: "Copy Proxy address with password",
    }),
  ).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "Copy Shell setup" }),
  ).toBeVisible();
  await expect(panel.getByText("Shown once")).toBeVisible();
  await panel.getByRole("button", { name: "I've saved it" }).click();

  await expect(proxy).toHaveCount(0);
  await expect(panel.getByText("Shown once")).toHaveCount(0);
  await expect(panel.getByText("Project terminal")).toBeVisible();
  await expect(panel.getByText("Waiting for traffic")).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "Rotate password" }),
  ).toBeVisible();
  await expect(panel.getByRole("button", { name: "Revoke" })).toBeVisible();
  await expect(
    panel.locator(".manual-capture-list > li").filter({
      hasText: "Project terminal",
    }),
  ).toBeFocused();
  expect(
    await page.evaluate(() => ({
      local: globalThis.localStorage.length,
      session: globalThis.sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
  expect(errors).toEqual([]);
});

test("keeps the current route when keyboard users skip to main content", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#activity");

  const skip = page.getByRole("link", { name: "Skip to main content" });
  await skip.focus();
  await page.keyboard.press("Enter");

  await expect(
    page.getByRole("heading", { level: 1, name: "Activity" }),
  ).toBeVisible();
  await expect(page.locator("#main-content")).toBeFocused();
  await expect(page).toHaveURL(/#activity$/u);
  expect(errors).toEqual([]);
});

test("keeps top-level navigation in browser history", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.goto("/?preview=1#overview");
  await page.getByRole("link", { name: "Activity" }).click();
  await page.getByRole("link", { name: /^Policy/ }).click();

  await page.goBack();
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity" }),
  ).toBeVisible();
  await page.goForward();
  await expect(page.getByRole("heading", { name: "Policy" })).toBeVisible();
  expect(errors).toEqual([]);
});

test("keeps the current task usable in a narrow browser", async ({ page }) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?preview=1#overview");

  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Pending/ })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Enter offline hold" }),
  ).toBeVisible();
  await expect(page.locator(".focus-stage")).toHaveCount(1);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= globalThis.innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
});

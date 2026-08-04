import { expect, test, type Locator, type Page } from "@playwright/test";

const controlOrigin = "http://127.0.0.1:43123";
const instanceId = "browser-boundary-runtime";
const expiresAt = "2099-01-01T00:00:00Z";
const occurredAt = "2026-08-03T00:00:00Z";
const repeatingCursor = "cGFnZS0x";

interface ActivityRecordFixture {
  readonly accessId: string;
  readonly id: string;
  readonly occurredAt: string;
  readonly status: string;
}

interface DesktopApiOptions {
  readonly activity?: ActivityRecordFixture;
  readonly failFirstSource?: boolean;
  readonly holdInitialDashboard?: boolean;
  readonly repeatActivityCursor?: boolean;
}

interface DesktopApiControl {
  readonly releaseInitialDashboard: () => void;
}

const offline = {
  state: "online",
  revision: 1,
  since: occurredAt,
  activeActions: 0,
  enteringActions: 0,
  activeEgress: 0,
  queuedRequests: 0,
  heldBytes: 0,
  safeToDisconnect: false,
  activeByKind: {},
  queuedByKind: {},
} as const;

const status = {
  generation: instanceId,
  ready: true,
  apiVersion: "v1",
  statusKey: "runtime.state.initialized",
  runtime: {
    state: "initialized",
    instanceId,
    host: "desktop",
    schemaRevision: 26,
    storage: "healthy",
    accessProjection: {
      state: "healthy",
      unavailableAccessCount: 0,
    },
    offlineHold: offline,
    startedAt: occurredAt,
  },
} as const;

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

async function installDesktopApi(
  page: Page,
  options: DesktopApiOptions = {},
): Promise<DesktopApiControl> {
  const readToken = Buffer.alloc(32, 1).toString("base64url");
  const writeToken = Buffer.alloc(32, 2).toString("base64url");
  await page.addInitScript(
    ({ baseUrl, bootstrapExpiresAt, bootstrapInstanceId, read, write }) => {
      const target = window as typeof window & {
        __TAURI_INTERNALS__: {
          invoke: (command: string, args?: unknown) => Promise<unknown>;
          transformCallback: (
            callback: (payload: unknown) => void,
            once?: boolean,
          ) => number;
        };
      };
      target.__TAURI_INTERNALS__ = {
        invoke: async (command) => {
          switch (command) {
            case "plugin:event|listen":
              return 1;
            case "plugin:event|unlisten":
              return null;
            case "load_navigation_state":
              return null;
            case "save_navigation_state":
              return null;
            case "take_control_session":
              return {
                schema: "vibermate-app-session-v1",
                baseUrl,
                readToken: read,
                writeToken: write,
                instanceId: bootstrapInstanceId,
                expiresAt: bootstrapExpiresAt,
              };
            default:
              throw new Error(`Unexpected native command: ${command}`);
          }
        },
        // These boundary cases do not emit a runtime-exit event; they still
        // model the native listener registration that production installs
        // before consuming its one-shot control session.
        transformCallback: () => 1,
      };
    },
    {
      baseUrl: controlOrigin,
      bootstrapExpiresAt: expiresAt,
      bootstrapInstanceId: instanceId,
      read: readToken,
      write: writeToken,
    },
  );

  let releaseInitialDashboard = () => undefined;
  const initialDashboardGate = new Promise<void>((resolve) => {
    releaseInitialDashboard = resolve;
  });
  let activityPage = 0;
  await page.route(`${controlOrigin}/**`, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const corsHeaders = {
      "Access-Control-Allow-Headers":
        "Accept, Authorization, Content-Type, Idempotency-Key, If-Match",
      "Access-Control-Allow-Methods": "GET, POST, PUT, OPTIONS",
      "Access-Control-Allow-Origin": "http://127.0.0.1:1420",
      "Access-Control-Allow-Private-Network": "true",
    };
    if (request.method() === "OPTIONS") {
      await route.fulfill({ headers: corsHeaders, status: 204 });
      return;
    }

    if (
      options.holdInitialDashboard === true &&
      url.pathname !== "/api/v1/auth/sessions/current"
    ) {
      await initialDashboardGate;
    }

    const fulfill = async (body: unknown, responseStatus = 200) => {
      await route.fulfill({
        body: JSON.stringify(body),
        headers: {
          ...corsHeaders,
          "Content-Type":
            responseStatus >= 400
              ? "application/problem+json"
              : "application/json",
        },
        status: responseStatus,
      });
    };
    if (url.pathname === "/api/v1/auth/sessions/current") {
      await fulfill({
        schema: "vibermate-app-session-state-v1",
        revision: 1,
        expiresAt,
      });
      return;
    }
    if (url.pathname === "/api/v1/status") {
      await fulfill(status);
      return;
    }
    if (url.pathname === "/api/v1/offline-hold") {
      await fulfill(offline);
      return;
    }
    if (url.pathname === "/api/v1/activities") {
      activityPage += 1;
      const activity =
        options.activity ??
        ({
          id: `activity-${activityPage}`,
          occurredAt,
          accessId: "work",
          status: "succeeded",
        } satisfies ActivityRecordFixture);
      await fulfill({
        items: [activity],
        ...(options.repeatActivityCursor === true
          ? { nextCursor: repeatingCursor }
          : {}),
      });
      return;
    }
    if (
      options.failFirstSource === true &&
      url.pathname === "/api/v1/approvals"
    ) {
      await fulfill(
        {
          type: "urn:vibermate:error:runtime-unavailable",
          title: "Runtime unavailable",
          status: 503,
          code: "runtime_unavailable",
        },
        503,
      );
      return;
    }
    if (
      url.pathname === "/api/v1/approvals" ||
      url.pathname === "/api/v1/capture-runs" ||
      url.pathname === "/api/v1/connections" ||
      url.pathname === "/api/v1/egress-attempts"
    ) {
      await fulfill({ items: [] });
      return;
    }
    await fulfill(
      {
        type: "urn:vibermate:error:not-found",
        title: "Not found",
        status: 404,
        code: "not_found",
      },
      404,
    );
  });

  return { releaseInitialDashboard };
}

async function expectNoHorizontalOverflow(locators: readonly Locator[]) {
  for (const locator of locators) {
    await expect(locator).toHaveCount(1);
    expect(
      await locator.evaluate(
        (element) => element.scrollWidth <= element.clientWidth + 1,
      ),
    ).toBe(true);
  }
}

for (const boundary of [
  { locale: "en-US", width: 390 },
  { locale: "zh-CN", width: 920 },
] as const) {
  test(`keeps maximum Activity text inside every ${boundary.width}px ${boundary.locale} boundary`, async ({
    page,
  }) => {
    const errors = collectBrowserErrors(page);
    await page.setViewportSize({ width: boundary.width, height: 900 });
    const activity = {
      id: "x".repeat(512),
      occurredAt,
      accessId: "a".repeat(128),
      status: "s".repeat(128),
    };
    await installDesktopApi(page, { activity });
    await page.goto("/#activity/requests");
    if (boundary.locale === "zh-CN") {
      await page.getByRole("button", { name: "简体中文" }).click();
    }

    const row = page.locator(".activity-list li");
    await expect(row).toContainText(activity.id);
    await expect(row).toContainText(activity.accessId);
    await expect(row).toContainText(activity.status);
    await expectNoHorizontalOverflow([
      page.locator("html"),
      page.locator("#main-content"),
      page.locator(".activity-list"),
      row,
    ]);
    expect(errors).toEqual([]);
  });
}

test("keeps all five Policy actions horizontally reachable at 390px in both locales", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?preview=1#policies/approvals");
  const actions = page
    .locator('[data-approval-id="approval-network-sample"]')
    .locator(".button-row > *");
  await expect(actions).toHaveCount(5);

  for (const localeButton of [undefined, "简体中文"] as const) {
    if (localeButton !== undefined) {
      await page.getByRole("button", { name: localeButton }).click();
    }
    await expectNoHorizontalOverflow([
      page.locator("html"),
      page
        .locator('[data-approval-id="approval-network-sample"]')
        .locator(".button-row"),
    ]);
    for (const action of await actions.all()) {
      await expect(action).toBeVisible();
      const box = await action.boundingBox();
      expect(box).not.toBeNull();
      expect(box?.x ?? -1).toBeGreaterThanOrEqual(0);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(390);
    }
  }
  expect(errors).toEqual([]);
});

test("stops a cyclic Activity cursor safely and leaves Refresh usable at 390px", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await installDesktopApi(page, { repeatActivityCursor: true });
  await page.goto("/#activity/requests");
  await page.getByRole("button", { name: "Load more" }).click();

  await expect(
    page.getByText("Older paging stopped at the safety limit"),
  ).toBeVisible();
  await expect(
    page.getByText("Refreshing re-anchors this bounded view"),
  ).toBeVisible();
  const refresh = page.getByRole("button", { name: "Refresh" });
  await expect(refresh).toBeVisible();
  await expect(refresh).toBeEnabled();
  await refresh.click();
  await expect(
    page.getByText("Older paging stopped at the safety limit"),
  ).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Load more" })).toBeEnabled();
  expect(errors).toEqual([]);
});

test("labels initial metrics as Waiting without presenting zero evidence", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  const api = await installDesktopApi(page, { holdInitialDashboard: true });
  await page.goto("/#overview");

  const metricValues = page.locator(".decision-metric strong");
  await expect(metricValues).toHaveCount(4);
  await expect(metricValues).toHaveText([
    "Waiting…",
    "Waiting…",
    "Waiting…",
    "Waiting…",
  ]);
  await expect(metricValues).not.toContainText(["0", "0/0"]);
  api.releaseInitialDashboard();
  await expect(metricValues.first()).toHaveText("Ready");
  expect(errors).toEqual([]);
});

test("distinguishes unavailable information from a stale update", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 920, height: 900 });
  await installDesktopApi(page, { failFirstSource: true });
  await page.goto("/#overview");

  const alert = page.getByRole("alert");
  await expect(alert).toContainText("Some information is unavailable");
  await expect(alert).not.toContainText("last update");
  const approvalMetric = page.getByRole("button", {
    name: /Pending decisions/u,
  });
  await expect(approvalMetric).toContainText("Unavailable · decisions");
  await expect(approvalMetric).not.toContainText("Stale since");
  expect(
    errors.filter(
      (error) =>
        !error.includes(
          "Failed to load resource: the server responded with a status of 503",
        ),
    ),
  ).toEqual([]);
});

test("keeps Retry and Back usable for a missing Exchange in both locales", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?preview=1#activity/requests/exchange-missing");

  await expect(page.getByRole("alert")).toContainText(
    "This request evidence was not found.",
  );
  const retry = page.getByRole("button", { name: "Try again" });
  await expect(retry).toBeEnabled();
  await retry.click();
  await expect(page.getByRole("alert")).toContainText(
    "This request evidence was not found.",
  );

  await page.getByRole("button", { name: "简体中文" }).click();
  await expect(page.getByRole("alert")).toContainText("未找到这条请求证据。");
  await expect(page.getByRole("button", { name: "重试" })).toBeEnabled();
  const back = page.getByRole("link", { name: "返回全部请求" });
  await expect(back).toBeVisible();
  await back.click();
  await expect(page.getByRole("heading", { level: 1, name: "请求" })).toBeVisible();
  expect(errors).toEqual([]);
});

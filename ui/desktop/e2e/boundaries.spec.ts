import { expect, test, type Locator, type Page } from "@playwright/test";
import enUS from "../src/generated/locales/en-US.json" with { type: "json" };
import zhCN from "../src/generated/locales/zh-CN.json" with { type: "json" };

const controlOrigin = "http://127.0.0.1:43123";
const instanceId = "browser-boundary-runtime";
const expiresAt = "2099-01-01T00:00:00Z";
const occurredAt = "2026-08-03T00:00:00Z";
const repeatingCursor = "cGFnZS0x";

interface ActivityRecordFixture {
  readonly access: {
    readonly applicationRevision: number;
    readonly displayName: string;
    readonly id: string;
  };
  readonly id: string;
  readonly kind: "exchange";
  readonly occurredAt: string;
  readonly parentRefs: {
    readonly accessId: string;
    readonly captureRunId: string;
    readonly connectionId: string;
    readonly exchangeId: string;
    readonly ingressProfileId: string;
  };
  readonly source: {
    readonly displayName: string;
    readonly kind: "capture_run";
    readonly recognition: "configured";
  };
  readonly status: "succeeded" | "pending" | "failed" | "canceled";
  readonly title: string;
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
      const id = `activity-${activityPage}`;
      const activity =
        options.activity ??
        ({
          access: {
            applicationRevision: 1,
            displayName: "Work",
            id: "work",
          },
          id,
          kind: "exchange",
          occurredAt,
          parentRefs: {
            accessId: "work",
            captureRunId: "run-boundary",
            connectionId: `connection-${activityPage}`,
            exchangeId: id,
            ingressProfileId: "capture-run/run-boundary",
          },
          source: {
            displayName: "claude",
            kind: "capture_run",
            recognition: "configured",
          },
          status: "succeeded",
          title: "claude",
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
      access: {
        applicationRevision: 1,
        displayName: "A".repeat(256),
        id: "a".repeat(128),
      },
      id: "x".repeat(512),
      kind: "exchange" as const,
      occurredAt,
      parentRefs: {
        accessId: "a".repeat(128),
        captureRunId: "r".repeat(128),
        connectionId: "c".repeat(512),
        exchangeId: "x".repeat(512),
        ingressProfileId: `capture-run/${"r".repeat(128)}`,
      },
      source: {
        displayName: "S".repeat(256),
        kind: "capture_run" as const,
        recognition: "configured" as const,
      },
      status: "failed" as const,
      title: "T".repeat(256),
    };
    await installDesktopApi(page, { activity });
    await page.goto("/#activity/requests");
    if (boundary.locale === "zh-CN") {
      await page.getByRole("button", { name: zhCN["locale.zh-CN"] }).click();
    }

    const panel = page.locator(".activity-panel");
    const tableScroll = panel.locator(".compact-table-scroll");
    const row = panel.locator(".activity-table tbody tr");
    await expect(row).toContainText(activity.id);
    await expect(row).toContainText(activity.access.displayName);
    await expect(row).toContainText(
      boundary.locale === "zh-CN"
        ? zhCN["activity.status.failed"]
        : enUS["activity.status.failed"],
    );
    await expectNoHorizontalOverflow([
      page.locator("html"),
      page.locator("#main-content"),
      panel,
    ]);
    await expect(tableScroll).toBeVisible();
    expect(
      await tableScroll.evaluate(
        (element) => element.getBoundingClientRect().right <= innerWidth + 1,
      ),
    ).toBe(true);
    expect(errors).toEqual([]);
  });
}

test("keeps common Policy actions compact and reveals persistent rules on demand at 390px", async ({
  page,
}) => {
  const errors = collectBrowserErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?preview=1#policies/approvals");
  const approval = page.locator(
    '[data-approval-id="approval-network-sample"]',
  );
  const quickActions = approval.locator(".approval-quick-actions button");
  await expect(quickActions).toHaveCount(2);
  await expect(approval.locator(".approval-rule-actions")).toHaveCount(0);
  await approval.getByRole("link", { name: "Open approval" }).click();
  await expect(approval.locator(".approval-rule-actions button")).toHaveCount(2);

  for (const localeButton of [undefined, zhCN["locale.zh-CN"]] as const) {
    if (localeButton !== undefined) {
      await page.getByRole("button", { name: localeButton }).click();
    }
    await expectNoHorizontalOverflow([
      page.locator("html"),
      page.locator("#main-content"),
      approval,
    ]);
    for (const action of await approval
      .locator(
        ".approval-quick-actions button, .approval-more-link, .approval-rule-actions button",
      )
      .all()) {
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

  const heading = page.getByRole("heading", { name: "Overview" });
  await expect(heading).toBeVisible();
  const initialHeadingBox = await heading.boundingBox();
  expect(initialHeadingBox).not.toBeNull();
  await expect(page.getByRole("alert")).toHaveCount(0);
  const approvalMetric = page.getByRole("button", {
    name: /Pending decisions/u,
  });
  await expect(approvalMetric).toContainText("Unavailable · decisions");
  await expect(approvalMetric).not.toContainText("Stale since");
  await page.waitForTimeout(2_200);
  const refreshedHeadingBox = await heading.boundingBox();
  expect(refreshedHeadingBox?.y).toBe(initialHeadingBox?.y);
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

  await page.getByRole("button", { name: zhCN["locale.zh-CN"] }).click();
  await expect(page.getByRole("alert")).toContainText(
    zhCN["error.exchange_not_found"],
  );
  await expect(
    page.getByRole("button", { name: zhCN["common.retry"] }),
  ).toBeEnabled();
  const back = page.getByRole("link", {
    name: zhCN["activity.detail.back"],
  });
  await expect(back).toBeVisible();
  await back.click();
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: zhCN["navigation.task.activityRequests"],
    }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

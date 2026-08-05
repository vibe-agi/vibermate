import { QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  buildAccessApplyInput,
  initialAccessForm,
} from "../src/access-form.ts";
import type { ControlClient } from "../src/control-client.ts";
import {
  DashboardQueryRuntime,
  dashboardQueryKeys,
  maximumRetainedActivityRecords,
  useDashboardQueryRuntime,
} from "../src/dashboard-runtime.ts";
import type {
  AccessDetail,
  AccessApplyInput,
  ActivityRecord,
  ApprovalChoice,
  ApprovalView,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
} from "../src/control-types.ts";

const offline: OfflineHoldSnapshot = {
  activeActions: 0,
  activeByKind: {},
  activeEgress: 0,
  enteringActions: 0,
  heldBytes: 0,
  queuedByKind: {},
  queuedRequests: 0,
  revision: 1,
  safeToDisconnect: false,
  since: "2026-08-03T00:00:00Z",
  state: "online",
};

const status: StatusResponse = {
  apiVersion: "v1",
  generation: "runtime-instance",
  ready: true,
  runtime: {
    accessProjection: {
      state: "healthy",
      unavailableAccessCount: 0,
    },
    host: "desktop",
    instanceId: "runtime-instance",
    offlineHold: offline,
    schemaRevision: 7,
    startedAt: "2026-08-03T00:00:00Z",
    state: "initialized",
    storage: "healthy",
  },
  statusKey: "runtime.state.initialized",
};

function activity(
  id: string,
  occurredAt: string,
  statusValue = "succeeded",
): ActivityRecord {
  return {
    accessId: "work",
    id,
    occurredAt,
    status: statusValue,
  };
}

function numberedActivity(sequence: number): ActivityRecord {
  return activity(
    `exchange-${sequence}`,
    new Date(Date.parse("2026-08-03T08:00:00Z") + sequence * 1_000).toISOString(),
  );
}

function activityRange(
  newestSequence: number,
  count: number,
): readonly ActivityRecord[] {
  return Array.from({ length: count }, (_, index) =>
    numberedActivity(newestSequence - index),
  );
}

function accessDetail(
  accessId: string,
  statusValue: AccessDetail["access"]["status"] = "enabled",
): AccessDetail {
  const profileId = `${accessId}-profile`;
  const accountId = `${accessId}-account`;
  const endpointId = `${accessId}-endpoint`;
  const targetId = `${accessId}-target`;
  const routeSetId = `${accessId}-routes`;
  const egressPolicyId = `${accessId}-egress`;
  return {
    revision: 1,
    access: {
      id: accessId,
      name: "Work",
      description: "",
      status: statusValue,
      agentEndpointId: endpointId,
      defaultRouteSetId: routeSetId,
      profileIds: [profileId],
      egressPolicyId,
    },
    agentEndpoint: {
      id: endpointId,
      clientOrigin: "https://api.example.test",
      clientDialect: "anthropic-messages",
    },
    profiles: [
      {
        id: profileId,
        kind: "managed",
        credentialSource: "managed_account",
        processingMode: "managed",
        name: "Primary upstream",
        description: "",
        backendDialect: "openai-chat",
        targetId,
        upstreamWireProfileRef: "follow-client",
        defaultModelPolicy: { mode: "fixed", fixedModel: "model" },
        accountBindingIds: [accountId],
        defaultAccountBindingId: accountId,
      },
    ],
    providerTargets: [
      {
        id: targetId,
        profileId,
        origin: "https://provider.example.test/v1",
        protocol: "openai-chat",
        capabilities: ["messages", "streaming", "tool_calls"],
      },
    ],
    accountBindings: [
      {
        id: accountId,
        profileId,
        label: "Primary account",
        authDriverRef: "static_header",
        enabled: true,
        secretHandling: "preserve_existing",
      },
    ],
    routeSets: [
      {
        id: routeSetId,
        candidateProfileIds: [profileId],
        fallback: "disabled",
      },
    ],
    egressPolicy: { id: egressPolicyId, mode: "direct" },
    pluginPlan: { mode: "pass_through", bindingIds: [] },
  };
}

function clientFixture(): ControlClient {
  let currentOffline = offline;
  return {
    close: vi.fn(),
    accesses: vi.fn(async () => ({ items: [] })),
    access: vi.fn(async (accessId: string) => accessDetail(accessId)),
    addAccessCandidate: vi.fn(async () => ({
      outcome: "committed" as const,
      applicationState: "active" as const,
      planHash: "c".repeat(64),
      revision: 2,
      candidate: {
        credentialId: "work-secondary-account",
        profileId: "work-secondary-profile",
      },
    })),
    accessPlan: vi.fn(async (accessId: string) => ({
      accessId,
      accountBindings: [
        { id: `${accessId}-account`, profileId: `${accessId}-profile` },
      ],
      planHash: "a".repeat(64),
      profiles: [`${accessId}-profile`],
      revision: 1,
    })),
    activities: vi.fn(async () => ({ items: [] })),
    applyAccess: vi.fn(async (_accessId: string, _input: AccessApplyInput) => ({
      outcome: "committed" as const,
      applicationState: "active" as const,
      planHash: "b".repeat(64),
      revision: 2,
    })),
    updateAccessStatus: vi.fn(async (_accessId, expectedRevision, status) =>
      status === "disabled"
        ? {
            outcome: "committed" as const,
            applicationState: "inactive" as const,
            revision: expectedRevision + 1,
          }
        : {
            outcome: "committed" as const,
            applicationState: "active" as const,
            planHash: "b".repeat(64),
            revision: expectedRevision + 1,
          },
    ),
    previewAccessDeletion: vi.fn<ControlClient["previewAccessDeletion"]>(
      async (accessId, expectedRevision) => ({
        accessId,
        name: accessId,
        revision: expectedRevision,
        status: "disabled" as const,
        workspaceBindingCount: 0,
        activeCaptureRunCount: 0,
        proxyClientBindingCount: 0,
        exclusiveSecretCount: 0,
        sharedSecretCount: 0,
        impactToken: "A".repeat(43),
        blockers: [],
      }),
    ),
    deleteAccess: vi.fn<ControlClient["deleteAccess"]>(
      async (_accessId, expectedRevision) => ({
        outcome: "deleted" as const,
        revision: expectedRevision,
      }),
    ),
    approvals: vi.fn(async () => ({ items: [] })),
    captureRuns: vi.fn(async () => ({ items: [] })),
    manualCaptureContext: vi.fn(async () => ({
      confirmationToken: `ctx_${"A".repeat(43)}`,
      proxyAddress: "http://127.0.0.1:32123",
      root: {
        kind: "local_path" as const,
        derSha256: "a".repeat(64),
        fingerprint: "AA:BB:CC",
        pemPath: "/private/vibermate/root.pem",
      },
      defaultTemporarySeconds: 86_400,
      maxTemporarySeconds: 604_800,
    })),
    manualCaptures: vi.fn(async () => ({ items: [] })),
    manualCapture: vi.fn(async () => {
      throw new Error("Manual capture fixture is unavailable");
    }),
    createManualCapture: vi.fn(async () => {
      throw new Error("Manual capture fixture is unavailable");
    }),
    rotateManualCapture: vi.fn(async () => {
      throw new Error("Manual capture fixture is unavailable");
    }),
    revokeManualCapture: vi.fn(async () => undefined),
    connections: vi.fn(async () => ({ items: [] })),
    credential: vi.fn(
      async (
        _accessId: string,
        profileId: string,
        credentialId: string,
      ): Promise<CredentialView> => ({
        credentialId,
        profileId,
        secretRevision: 0,
        secretState: "missing",
      }),
    ),
    decideApproval: vi.fn(
      async (
        approval: ApprovalView,
        _choice: ApprovalChoice,
      ): Promise<ApprovalView> => approval,
    ),
    egressAttempts: vi.fn(async () => ({ items: [] })),
    exchange: vi.fn(async (exchangeId: string) => ({
      id: exchangeId,
      accessId: "work",
      status: "succeeded",
      processingTrace: {
        attemptIds: [],
        pluginRunIds: [],
        result: "succeeded",
      },
    })),
    enterOfflineHold: vi.fn(async () => {
      currentOffline = {
        ...offline,
        revision: 2,
        safeToDisconnect: true,
        state: "held" as const,
      };
      return currentOffline;
    }),
    offlineHold: vi.fn(async () => currentOffline),
    replaceCredentialSecret: vi.fn(
      async (
        _accessId: string,
        profileId: string,
        credentialId: string,
      ): Promise<CredentialView> => ({
        credentialId,
        profileId,
        secretRevision: 1,
        secretState: "configured",
      }),
    ),
    resumeOfflineHold: vi.fn(async () => {
      currentOffline = { ...offline, revision: 3 };
      return currentOffline;
    }),
    selectAccessCandidate: vi.fn(async () => ({
      outcome: "committed" as const,
      applicationState: "active" as const,
      planHash: "c".repeat(64),
      revision: 2,
    })),
    status: vi.fn(async () => status),
  };
}

function renderDashboard(model: DashboardQueryRuntime) {
  return renderHook(() => useDashboardQueryRuntime(model), {
    wrapper: ({ children }: { readonly children: ReactNode }) => (
      <StrictMode>
        <QueryClientProvider client={model.queryClient}>
          {children}
        </QueryClientProvider>
      </StrictMode>
    ),
  });
}

afterEach(() => {
  onlineManager.setOnline(true);
  vi.useRealTimers();
});

describe("TanStack Query dashboard runtime", () => {
  it("loads durable inactive Access detail without asking for an active plan", async () => {
    const client = clientFixture();
    vi.mocked(client.access).mockResolvedValue(
      accessDetail("draft-access", "draft"),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );

    const loading = dashboard.result.current.actions.loadAccess("draft-access");
    await act(async () => {
      await loading;
    });
    const result = await loading;

    expect(result?.detail.access.status).toBe("draft");
    expect(result?.plan).toBeUndefined();
    expect(client.access).toHaveBeenCalledWith(
      "draft-access",
      expect.any(AbortSignal),
    );
    expect(client.accessPlan).not.toHaveBeenCalled();

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps enabled durable detail visible when its active plan is unavailable", async () => {
    const client = clientFixture();
    vi.mocked(client.accessPlan).mockRejectedValue(
      new Error("projection unavailable"),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );

    const loading = dashboard.result.current.actions.loadAccess("work");
    await act(async () => {
      await loading;
    });
    const result = await loading;

    expect(result?.detail.access.status).toBe("enabled");
    expect(result?.plan).toBeUndefined();
    expect(client.accessPlan).toHaveBeenCalledWith(
      "work",
      expect.any(AbortSignal),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("owns seven independent snapshots and writes hold results through Query", async () => {
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );
    expect(
      model.queryClient.getQueryCache().findAll({
        queryKey: dashboardQueryKeys.root,
      }),
    ).toHaveLength(7);
    for (const read of [
      client.status,
      client.offlineHold,
      client.approvals,
      client.captureRuns,
      client.connections,
      client.egressAttempts,
    ]) {
      expect(read).toHaveBeenCalledWith(expect.any(AbortSignal));
    }
    expect(client.activities).toHaveBeenCalledWith(
      undefined,
      expect.any(AbortSignal),
    );

    await act(() => dashboard.result.current.actions.enterOfflineHold());
    await waitFor(() =>
      expect(dashboard.result.current.state.offline?.state).toBe("held"),
    );
    expect(client.enterOfflineHold).toHaveBeenCalledWith(
      1,
      expect.any(AbortSignal),
    );
    expect(client.status).toHaveBeenCalledTimes(2);
    expect(client.activities).toHaveBeenCalledTimes(2);
    expect(client.connections).toHaveBeenCalledTimes(2);
    expect(client.egressAttempts).toHaveBeenCalledTimes(2);
    expect(client.offlineHold).toHaveBeenCalledTimes(1);
    expect(client.approvals).toHaveBeenCalledTimes(1);
    expect(client.captureRuns).toHaveBeenCalledTimes(1);

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps stale source evidence while healthy queries continue", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date("2026-08-03T08:00:00Z"));
    const client = clientFixture();
    let captureAvailable = true;
    vi.mocked(client.captureRuns).mockImplementation(async () => {
      if (!captureAvailable) {
        throw new Error("capture unavailable");
      }
      return {
        items: [
          {
            catalogRevision: 1,
            clientAdapterState: "generic",
            clientRecognition: "unknown",
            createdAt: "2026-08-03T07:00:00Z",
            cwd: "/tmp",
            executableLabel: "client",
            expiresAt: "2026-08-03T09:00:00Z",
            id: "run-1",
            observation: "observed",
            recognition: "unknown",
            state: "attached",
          },
        ],
      };
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.captureRuns).toHaveLength(1),
    );
    const firstCaptureFreshness =
      dashboard.result.current.state.refreshedAtBySource.captureRuns;

    captureAvailable = false;
    vi.setSystemTime(new Date("2026-08-03T08:00:01Z"));
    await act(() => dashboard.result.current.actions.refresh());
    await waitFor(() =>
      expect(dashboard.result.current.state.unavailableSources).toEqual([
        "captureRuns",
      ]),
    );

    expect(dashboard.result.current.state.errorKey).toBe(
      "error.dashboard_partial",
    );
    expect(dashboard.result.current.state.captureRuns).toHaveLength(1);
    expect(dashboard.result.current.state.refreshedAtBySource.captureRuns).toBe(
      firstCaptureFreshness,
    );
    expect(dashboard.result.current.state.refreshedAtBySource.status).toBe(
      "2026-08-03T08:00:01.000Z",
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps a failed source unavailable until its background refresh succeeds", async () => {
    const client = clientFixture();
    let captureAttempt = 0;
    let finishCaptureRefresh:
      | ((page: { readonly items: readonly [] }) => void)
      | undefined;
    vi.mocked(client.captureRuns).mockImplementation(() => {
      captureAttempt += 1;
      if (captureAttempt === 1) {
        return Promise.reject(new Error("capture unavailable"));
      }
      return new Promise((resolve) => {
        finishCaptureRefresh = resolve;
      });
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.unavailableSources).toContain(
        "captureRuns",
      ),
    );

    let refresh: Promise<void> | undefined;
    act(() => {
      refresh = dashboard.result.current.actions.refresh();
    });
    await waitFor(() => expect(client.captureRuns).toHaveBeenCalledTimes(2));
    expect(dashboard.result.current.state.unavailableSources).toContain(
      "captureRuns",
    );

    finishCaptureRefresh?.({ items: [] });
    await act(async () => refresh);
    await waitFor(() =>
      expect(dashboard.result.current.state.unavailableSources).not.toContain(
        "captureRuns",
      ),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("continues loopback reads while the browser reports offline", async () => {
    onlineManager.setOnline(false);
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );
    expect(client.status).toHaveBeenCalledTimes(1);

    dashboard.unmount();
    await model.dispose();
  });

  it("does not let one pending source block healthy snapshots", async () => {
    const client = clientFixture();
    vi.mocked(client.approvals).mockRejectedValue(
      new Error("approvals unavailable"),
    );
    vi.mocked(client.captureRuns).mockImplementation(
      (signal) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => reject(new DOMException("aborted", "AbortError")),
            { once: true },
          );
        }),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );
    expect(dashboard.result.current.state.loading).toBe(true);
    expect(dashboard.result.current.state.captureRuns).toEqual([]);
    expect(
      dashboard.result.current.state.refreshedAtBySource.status,
    ).toBeDefined();
    expect(
      dashboard.result.current.state.refreshedAtBySource.captureRuns,
    ).toBeUndefined();
    await waitFor(() =>
      expect(dashboard.result.current.state.unavailableSources).toEqual([
        "approvals",
      ]),
    );
    expect(dashboard.result.current.state.errorKey).toBe(
      "error.dashboard_partial",
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("never retains a credential secret in Query or Mutation cache", async () => {
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );

    await act(() =>
      dashboard.result.current.actions.replaceCredentialSecret(
        {
          accessId: "work",
          credentialId: "work-account",
          profileId: "work-profile",
        },
        "provider-secret-value",
      ),
    );

    const retained = JSON.stringify({
      mutations: model.queryClient
        .getMutationCache()
        .getAll()
        .map((mutation) => mutation.state.variables),
      queries: model.queryClient
        .getQueryCache()
        .getAll()
        .map((query) => ({ data: query.state.data, key: query.queryKey })),
    });
    expect(retained).not.toContain("provider-secret-value");
    expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
      "work",
      "work-profile",
      "work-account",
      0,
      "provider-secret-value",
      expect.any(AbortSignal),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("does not keep a successful credential write pending on hanging reads", async () => {
    const client = clientFixture();
    let statusReads = 0;
    let activityReads = 0;
    vi.mocked(client.status).mockImplementation((signal) => {
      statusReads++;
      if (statusReads === 1) {
        return Promise.resolve(status);
      }
      return new Promise((_resolve, reject) => {
        signal?.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      });
    });
    vi.mocked(client.activities).mockImplementation((_cursor, signal) => {
      activityReads++;
      if (activityReads === 1) {
        return Promise.resolve({ items: [] });
      }
      return new Promise((_resolve, reject) => {
        signal?.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      });
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );

    let outcome: unknown;
    await act(async () => {
      outcome = await Promise.race([
        dashboard.result.current.actions.replaceCredentialSecret(
          {
            accessId: "work",
            credentialId: "work-account",
            profileId: "work-profile",
          },
          "short-lived-provider-secret",
        ),
        new Promise((resolve) => {
          setTimeout(() => resolve("timed-out"), 250);
        }),
      ]);
    });

    expect(outcome).toEqual({
      credentialId: "work-account",
      profileId: "work-profile",
      secretRevision: 1,
      secretState: "configured",
    });
    await waitFor(() => {
      expect(client.status).toHaveBeenCalledTimes(2);
      expect(client.activities).toHaveBeenCalledTimes(2);
    });
    expect(dashboard.result.current.state.busy).toBe(false);
    expect(
      JSON.stringify(
        model.queryClient
          .getMutationCache()
          .getAll()
          .map((mutation) => mutation.state.variables),
      ),
    ).not.toContain("short-lived-provider-secret");

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps the complete Access apply payload out of Mutation cache", async () => {
    const client = clientFixture();
    let finishApply:
      | ((result: {
          readonly outcome: "committed";
          readonly revision: number;
          readonly applicationState: "active";
          readonly planHash: string;
        }) => void)
      | undefined;
    vi.mocked(client.applyAccess).mockImplementation(
      () =>
        new Promise((resolve) => {
          finishApply = resolve;
        }),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.status?.ready).toBe(true),
    );
    const input = buildAccessApplyInput({
      ...initialAccessForm,
      accessId: "work",
      mode: "managed",
      fixedModel: "example-model",
      name: "Private workspace marker",
      providerOrigin: "https://private-provider.invalid/v1",
      routeName: "Primary route",
    });

    let applying: Promise<unknown> | undefined;
    act(() => {
      applying = dashboard.result.current.actions.applyAccess("work", input);
    });
    await waitFor(() => expect(client.applyAccess).toHaveBeenCalledTimes(1));

    const retained = JSON.stringify({
      mutations: model.queryClient
        .getMutationCache()
        .getAll()
        .map((mutation) => mutation.state.variables),
      queries: model.queryClient
        .getQueryCache()
        .getAll()
        .map((query) => ({ data: query.state.data, key: query.queryKey })),
    });
    expect(retained).not.toContain("Private workspace marker");
    expect(retained).not.toContain("https://private-provider.invalid/v1");
    expect(retained).not.toContain("secret://provider/work-account");
    expect(client.applyAccess).toHaveBeenCalledWith(
      "work",
      input,
      expect.any(AbortSignal),
    );

    finishApply?.({
      outcome: "committed",
      applicationState: "active",
      planHash: "b".repeat(64),
      revision: 2,
    });
    let result: unknown;
    await act(async () => {
      result = await applying;
    });
    expect(result).toEqual({
      outcome: "committed",
      applicationState: "active",
      planHash: "b".repeat(64),
      revision: 2,
    });
    expect(client.credential).not.toHaveBeenCalled();
    expect(client.status).toHaveBeenCalledTimes(2);
    expect(client.activities).toHaveBeenCalledTimes(2);

    dashboard.unmount();
    await model.dispose();
  });

  it("orders and deduplicates explicit Activity pages while retaining them across head refreshes", async () => {
    const client = clientFixture();
    const tailCursor = "dGFpbC0x";
    let head = {
      items: [
        activity("exchange-middle", "2026-08-03T08:02:00Z"),
        activity("exchange-new", "2026-08-03T08:03:00Z"),
      ],
      nextCursor: tailCursor,
    };
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return head;
      }
      expect(cursor).toBe(tailCursor);
      return {
        items: [
          activity("exchange-middle", "2026-08-03T08:02:00Z"),
          activity("exchange-old", "2026-08-03T08:01:00Z"),
        ],
      };
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);

    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual(["exchange-new", "exchange-middle", "exchange-old"]),
    );
    expect(client.activities).toHaveBeenCalledWith(
      tailCursor,
      expect.any(AbortSignal),
    );
    expect(dashboard.result.current.state.activitiesHasMore).toBe(false);

    head = {
      items: [
        activity("exchange-latest", "2026-08-03T08:04:00Z"),
        activity("exchange-new", "2026-08-03T08:03:00Z"),
      ],
      nextCursor: tailCursor,
    };
    await act(() => dashboard.result.current.actions.refresh());
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual([
        "exchange-latest",
        "exchange-new",
        "exchange-middle",
        "exchange-old",
      ]),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps the shifted head boundary and continues from the deepest loaded cursor", async () => {
    const client = clientFixture();
    const oldHeadCursor = "Y3Vyc29yLTUx";
    const shiftedHeadCursor = "Y3Vyc29yLTUz";
    const deepestCursor = "Y3Vyc29yLTQ5";
    let head = {
      items: [
        activity("exchange-100", "2026-08-03T08:01:00Z"),
        activity("exchange-99", "2026-08-03T08:00:59Z"),
        activity("exchange-52", "2026-08-03T08:00:52Z"),
        activity("exchange-51", "2026-08-03T08:00:51Z"),
      ],
      nextCursor: oldHeadCursor,
    };
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return head;
      }
      if (cursor === oldHeadCursor) {
        return {
          items: [
            activity("exchange-50", "2026-08-03T08:00:50Z"),
            activity("exchange-49", "2026-08-03T08:00:49Z"),
          ],
          nextCursor: deepestCursor,
        };
      }
      if (cursor === deepestCursor) {
        return {
          items: [activity("exchange-48", "2026-08-03T08:00:48Z")],
        };
      }
      throw new Error(`unexpected Activity cursor ${String(cursor)}`);
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    head = {
      items: [
        activity("exchange-102", "2026-08-03T08:01:02Z"),
        activity("exchange-101", "2026-08-03T08:01:01Z"),
        activity("exchange-100", "2026-08-03T08:01:00Z"),
        activity("exchange-99", "2026-08-03T08:00:59Z"),
        activity("exchange-53", "2026-08-03T08:00:53Z"),
      ],
      nextCursor: shiftedHeadCursor,
    };
    await act(() => dashboard.result.current.actions.refresh());

    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual([
        "exchange-102",
        "exchange-101",
        "exchange-100",
        "exchange-99",
        "exchange-53",
        "exchange-52",
        "exchange-51",
        "exchange-50",
        "exchange-49",
      ]),
    );
    expect(dashboard.result.current.state.activitiesHasMore).toBe(true);

    await act(() => dashboard.result.current.actions.refresh());
    await waitFor(() =>
      expect(dashboard.result.current.state.activities).toHaveLength(9),
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(client.activities).toHaveBeenLastCalledWith(
      deepestCursor,
      expect.any(AbortSignal),
    );
    expect(
      vi.mocked(client.activities).mock.calls.some(
        ([cursor]) => cursor === shiftedHeadCursor,
      ),
    ).toBe(false);
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toContain("exchange-48"),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("resets to a non-overlapping head and resumes from its cursor", async () => {
    const client = clientFixture();
    const oldHeadCursor = "b2xkLWhlYWQ";
    const oldDeepestCursor = "b2xkLWRlZXA";
    const newHeadCursor = "bmV3LWhlYWQ";
    let head = {
      items: [numberedActivity(100), numberedActivity(99)],
      nextCursor: oldHeadCursor,
    };
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return head;
      }
      if (cursor === oldHeadCursor) {
        return {
          items: [numberedActivity(98)],
          nextCursor: oldDeepestCursor,
        };
      }
      if (cursor === newHeadCursor) {
        return { items: [numberedActivity(198)] };
      }
      throw new Error(`unexpected Activity cursor ${String(cursor)}`);
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await waitFor(() =>
      expect(dashboard.result.current.state.activities).toHaveLength(3),
    );

    head = {
      items: [numberedActivity(200), numberedActivity(199)],
      nextCursor: newHeadCursor,
    };
    await act(() => dashboard.result.current.actions.refresh());
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual(["exchange-200", "exchange-199"]),
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(client.activities).toHaveBeenLastCalledWith(
      newHeadCursor,
      expect.any(AbortSignal),
    );
    expect(
      vi.mocked(client.activities).mock.calls.some(
        ([cursor]) => cursor === oldDeepestCursor,
      ),
    ).toBe(false);
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual(["exchange-200", "exchange-199", "exchange-198"]),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("caps a continuous polled interval and stops before skipping deeper records", async () => {
    const client = clientFixture();
    const firstCursor = "Zmlyc3QtdGFpbA";
    const deeperCursor = "ZGVlcGVyLXRhaWw";
    let head = {
      items: activityRange(50, 50),
      nextCursor: firstCursor,
    };
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return head;
      }
      if (cursor === firstCursor) {
        return {
          items: [numberedActivity(0)],
          nextCursor: deeperCursor,
        };
      }
      throw new Error(`unexpected Activity cursor ${String(cursor)}`);
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activities).toHaveLength(50),
    );
    await act(() => dashboard.result.current.actions.loadMoreActivities());

    for (const newest of [99, 148, 197, 246, 295]) {
      head = { items: activityRange(newest, 50), nextCursor: `head-${newest}` };
      await act(() => dashboard.result.current.actions.refresh());
      await waitFor(() =>
        expect(dashboard.result.current.state.activities[0]?.id).toBe(
          `exchange-${newest}`,
        ),
      );
      expect(
        dashboard.result.current.state.activities.length,
      ).toBeLessThanOrEqual(maximumRetainedActivityRecords);
    }

    expect(dashboard.result.current.state.activities).toHaveLength(
      maximumRetainedActivityRecords,
    );
    expect(dashboard.result.current.state.activities[0]?.id).toBe(
      "exchange-295",
    );
    expect(dashboard.result.current.state.activities.at(-1)?.id).toBe(
      "exchange-96",
    );
    expect(dashboard.result.current.state.activitiesHasMore).toBe(false);
    expect(
      dashboard.result.current.state.activitiesPagingSafetyStopped,
    ).toBe(true);
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(
      vi.mocked(client.activities).mock.calls.some(
        ([cursor]) => cursor === deeperCursor,
      ),
    ).toBe(false);

    dashboard.unmount();
    await model.dispose();
  });

  it("serializes a simultaneous tail success and head refresh without lost updates", async () => {
    const client = clientFixture();
    const oldHeadCursor = "cmFjZS1vbGQ";
    const shiftedHeadCursor = "cmFjZS1zaGlmdGVk";
    const deepestCursor = "cmFjZS1kZWVw";
    let headReads = 0;
    let releaseHead:
      | ((value: {
          readonly items: readonly ActivityRecord[];
          readonly nextCursor: string;
        }) => void)
      | undefined;
    let releaseTail:
      | ((value: {
          readonly items: readonly ActivityRecord[];
          readonly nextCursor: string;
        }) => void)
      | undefined;
    vi.mocked(client.activities).mockImplementation((cursor) => {
      if (cursor === undefined) {
        headReads += 1;
        if (headReads === 1) {
          return Promise.resolve({
            items: [
              numberedActivity(100),
              numberedActivity(99),
              numberedActivity(52),
              numberedActivity(51),
            ],
            nextCursor: oldHeadCursor,
          });
        }
        return new Promise((resolve) => {
          releaseHead = resolve;
        });
      }
      if (cursor === oldHeadCursor) {
        return new Promise((resolve) => {
          releaseTail = resolve;
        });
      }
      if (cursor === deepestCursor) {
        return Promise.resolve({ items: [numberedActivity(49)] });
      }
      return Promise.reject(new Error("unexpected Activity cursor"));
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );
    expect(
      dashboard.result.current.state.activitiesPagingSafetyStopped,
    ).toBe(false);

    let loading: Promise<void> | undefined;
    let refreshing: Promise<void> | undefined;
    act(() => {
      loading = dashboard.result.current.actions.loadMoreActivities();
      refreshing = dashboard.result.current.actions.refresh();
    });
    await waitFor(() => {
      expect(releaseHead).toBeDefined();
      expect(releaseTail).toBeDefined();
    });
    act(() => {
      releaseTail?.({
        items: [numberedActivity(50)],
        nextCursor: deepestCursor,
      });
      releaseHead?.({
        items: [
          numberedActivity(102),
          numberedActivity(101),
          numberedActivity(100),
          numberedActivity(99),
          numberedActivity(53),
        ],
        nextCursor: shiftedHeadCursor,
      });
    });
    await act(async () => {
      await Promise.all([loading, refreshing]);
    });
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual([
        "exchange-102",
        "exchange-101",
        "exchange-100",
        "exchange-99",
        "exchange-53",
        "exchange-52",
        "exchange-51",
        "exchange-50",
      ]),
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(client.activities).toHaveBeenLastCalledWith(
      deepestCursor,
      expect.any(AbortSignal),
    );
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toContain("exchange-49"),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("stops a successful Activity cursor cycle", async () => {
    const client = clientFixture();
    const cursorA = "Y3ljbGUtYQ";
    const cursorB = "Y3ljbGUtYg";
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return { items: [numberedActivity(3)], nextCursor: cursorA };
      }
      if (cursor === cursorA) {
        return { items: [numberedActivity(2)], nextCursor: cursorB };
      }
      if (cursor === cursorB) {
        return { items: [numberedActivity(1)], nextCursor: cursorA };
      }
      throw new Error("unexpected Activity cursor");
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(false),
    );
    expect(
      dashboard.result.current.state.activitiesPagingSafetyStopped,
    ).toBe(true);
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(
      vi.mocked(client.activities).mock.calls.filter(
        ([cursor]) => cursor === cursorA,
      ),
    ).toHaveLength(1);

    dashboard.unmount();
    await model.dispose();
  });

  it("bridges every head seen while a tail request is in flight", async () => {
    const client = clientFixture();
    const oldHeadCursor = "Y3Vyc29yLTUx";
    const shiftedHeadCursor = "Y3Vyc29yLTUz";
    const deepestCursor = "Y3Vyc29yLTUw";
    let head = {
      items: [
        activity("exchange-100", "2026-08-03T08:01:00Z"),
        activity("exchange-52", "2026-08-03T08:00:52Z"),
        activity("exchange-51", "2026-08-03T08:00:51Z"),
      ],
      nextCursor: oldHeadCursor,
    };
    let releaseTail:
      | ((value: {
          readonly items: readonly ActivityRecord[];
          readonly nextCursor: string;
        }) => void)
      | undefined;
    vi.mocked(client.activities).mockImplementation((cursor) => {
      if (cursor === undefined) {
        return Promise.resolve(head);
      }
      if (cursor === oldHeadCursor) {
        return new Promise((resolve) => {
          releaseTail = resolve;
        });
      }
      if (cursor === deepestCursor) {
        return Promise.resolve({ items: [] });
      }
      return Promise.reject(new Error("unexpected Activity cursor"));
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );

    let loading: Promise<void> | undefined;
    act(() => {
      loading = dashboard.result.current.actions.loadMoreActivities();
    });
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesLoadingMore).toBe(true),
    );
    head = {
      items: [
        activity("exchange-102", "2026-08-03T08:01:02Z"),
        activity("exchange-101", "2026-08-03T08:01:01Z"),
        activity("exchange-100", "2026-08-03T08:01:00Z"),
        activity("exchange-53", "2026-08-03T08:00:53Z"),
      ],
      nextCursor: shiftedHeadCursor,
    };
    await act(() => dashboard.result.current.actions.refresh());
    expect(dashboard.result.current.state.activitiesLoadingMore).toBe(true);
    await waitFor(() =>
      expect(
        dashboard.result.current.state.activities.map(({ id }) => id),
      ).toEqual([
        "exchange-102",
        "exchange-101",
        "exchange-100",
        "exchange-53",
        "exchange-52",
        "exchange-51",
      ]),
    );

    releaseTail?.({
      items: [activity("exchange-50", "2026-08-03T08:00:50Z")],
      nextCursor: deepestCursor,
    });
    await act(async () => {
      await loading;
    });
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesLoadingMore).toBe(false),
    );
    expect(
      dashboard.result.current.state.activities.map(({ id }) => id),
    ).toEqual([
      "exchange-102",
      "exchange-101",
      "exchange-100",
      "exchange-53",
      "exchange-52",
      "exchange-51",
      "exchange-50",
    ]);
    await act(() => dashboard.result.current.actions.loadMoreActivities());
    expect(client.activities).toHaveBeenLastCalledWith(
      deepestCursor,
      expect.any(AbortSignal),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("bounds concurrent Activity page reads to one request", async () => {
    const client = clientFixture();
    const tailCursor = "dGFpbC0x";
    let releaseTail: ((value: { readonly items: readonly ActivityRecord[] }) => void) | undefined;
    vi.mocked(client.activities).mockImplementation((cursor) => {
      if (cursor === undefined) {
        return Promise.resolve({
          items: [activity("exchange-new", "2026-08-03T08:03:00Z")],
          nextCursor: tailCursor,
        });
      }
      return new Promise((resolve) => {
        releaseTail = resolve;
      });
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );

    let first: Promise<void> | undefined;
    let second: Promise<void> | undefined;
    act(() => {
      first = dashboard.result.current.actions.loadMoreActivities();
      second = dashboard.result.current.actions.loadMoreActivities();
    });
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesLoadingMore).toBe(true),
    );
    expect(
      vi.mocked(client.activities).mock.calls.filter(([cursor]) => cursor === tailCursor),
    ).toHaveLength(1);
    releaseTail?.({
      items: [activity("exchange-old", "2026-08-03T08:01:00Z")],
    });
    await act(async () => {
      await Promise.all([first, second]);
    });
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesLoadingMore).toBe(false),
    );

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps loaded Activity summaries and exposes a retryable page error", async () => {
    const client = clientFixture();
    const tailCursor = "dGFpbC0x";
    let tailAttempts = 0;
    vi.mocked(client.activities).mockImplementation(async (cursor) => {
      if (cursor === undefined) {
        return {
          items: [activity("exchange-new", "2026-08-03T08:03:00Z")],
          nextCursor: tailCursor,
        };
      }
      tailAttempts += 1;
      if (tailAttempts === 1) {
        throw new Error("tail unavailable");
      }
      return {
        items: [activity("exchange-old", "2026-08-03T08:01:00Z")],
      };
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const dashboard = renderDashboard(model);
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesHasMore).toBe(true),
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await waitFor(() =>
      expect(dashboard.result.current.state.activitiesLoadMoreErrorKey).toBe(
        "error.network_unavailable",
      ),
    );
    expect(dashboard.result.current.state.activities).toHaveLength(1);
    expect(dashboard.result.current.state.unavailableSources).not.toContain(
      "activities",
    );

    await act(() => dashboard.result.current.actions.loadMoreActivities());
    await waitFor(() =>
      expect(dashboard.result.current.state.activities).toHaveLength(2),
    );
    expect(
      dashboard.result.current.state.activitiesLoadMoreErrorKey,
    ).toBeUndefined();

    dashboard.unmount();
    await model.dispose();
  });

  it("keeps identical query keys isolated between desktop sessions", async () => {
    const firstClient = clientFixture();
    const secondClient = clientFixture();
    vi.mocked(firstClient.status).mockResolvedValue({
      ...status,
      generation: "first-session",
    });
    vi.mocked(secondClient.status).mockResolvedValue({
      ...status,
      generation: "second-session",
    });
    const firstModel = new DashboardQueryRuntime(firstClient, 60_000);
    const secondModel = new DashboardQueryRuntime(secondClient, 60_000);
    const firstDashboard = renderDashboard(firstModel);
    const secondDashboard = renderDashboard(secondModel);

    await waitFor(() => {
      expect(firstDashboard.result.current.state.status?.generation).toBe(
        "first-session",
      );
      expect(secondDashboard.result.current.state.status?.generation).toBe(
        "second-session",
      );
    });
    firstDashboard.unmount();
    await firstModel.dispose();

    expect(
      secondModel.queryClient.getQueryData<StatusResponse>(
        dashboardQueryKeys.status,
      )?.generation,
    ).toBe("second-session");

    secondDashboard.unmount();
    await secondModel.dispose();
  });
});

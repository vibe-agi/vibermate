import { afterEach, describe, expect, it, vi } from "vitest";
import { buildAccessApplyInput, initialAccessForm } from "../src/access-form.ts";
import {
  ControlContractError,
  ControlProblem,
  createControlClient,
  type ControlClient,
  type DesktopSession,
} from "../src/control-client.ts";
import type {
  ApprovalView,
  ConnectionRecord,
  WorkspaceRouteBinding,
} from "../src/control-types.ts";

const sessionStatePath = "/api/v1/auth/sessions/current";
const sessionRenewalPath = "/api/v1/auth/sessions/refresh";
const fixedNow = Date.parse("2026-08-03T08:00:00Z");
const requestTimeoutMilliseconds = 10_000;
const maximumResponseBytes = 2 * 1024 * 1024;

type FetchCall = {
  readonly url: URL;
  readonly init: RequestInit;
};

function capability(fill: number): string {
  const bytes = new Uint8Array(32).fill(fill);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

function session(expiresAt = "2099-07-30T00:00:00Z"): DesktopSession {
  return {
    schema: "vibermate-app-session-v1",
    baseUrl: "http://127.0.0.1:43127",
    readToken: capability(0x11),
    writeToken: capability(0x22),
    instanceId: "runtime-instance",
    expiresAt,
  };
}

function sessionState(expiresAt: string, revision = 1) {
  return {
    schema: "vibermate-app-session-state-v1",
    revision,
    expiresAt,
  };
}

function sessionRotation(
  expiresAt: string,
  revision = 2,
  readToken = capability(0x33),
  writeToken = capability(0x44),
) {
  return {
    schema: "vibermate-app-session-rotation-v1",
    revision,
    readToken,
    writeToken,
    expiresAt,
  };
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: {
      "Cache-Control": "no-store",
      "Content-Type": "application/json",
    },
  });
}

function problemResponse(status: number, reasonCode: string): Response {
  return new Response(
    JSON.stringify({
      type: `urn:vibermate:error:${reasonCode.replaceAll("_", "-")}`,
      title: status === 409 ? "Conflict" : "Request failed",
      status,
      code: reasonCode,
    }),
    {
      status,
      headers: { "Content-Type": "application/problem+json" },
    },
  );
}

function withSessionState(
  bootstrap: DesktopSession,
  handler: (url: URL, init: RequestInit) => Promise<Response> | Response,
  calls: FetchCall[] = [],
) {
  return vi.fn(
    async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = new URL(String(input));
      calls.push({ url, init });
      if (url.pathname === sessionStatePath) {
        return jsonResponse(sessionState(bootstrap.expiresAt));
      }
      return handler(url, init);
    },
  );
}

function heldSnapshot(revision = 2): Record<string, unknown> {
  return {
    state: "held",
    revision,
    since: "2026-08-03T08:00:00Z",
    activeActions: 0,
    enteringActions: 0,
    activeEgress: 0,
    queuedRequests: 0,
    heldBytes: 0,
    safeToDisconnect: true,
    activeByKind: {},
    queuedByKind: {},
  };
}

function statusResponse(
  runtimeOverrides: Record<string, unknown> = {},
  responseOverrides: Record<string, unknown> = {},
): Record<string, unknown> {
  const runtime = {
    state: "initialized",
    instanceId: "runtime-instance",
    host: "desktop",
    schemaRevision: 1,
    storage: "healthy",
    accessProjection: {
      state: "healthy",
      unavailableAccessCount: 0,
    },
    offlineHold: heldSnapshot(),
    startedAt: "2026-08-03T08:00:00Z",
    ...runtimeOverrides,
  };
  return {
    generation: "runtime-instance",
    ready: true,
    apiVersion: "v1",
    statusKey: `runtime.state.${String(runtime.state)}`,
    runtime,
    ...responseOverrides,
  };
}

function onlineSnapshot(revision = 3): Record<string, unknown> {
  return {
    ...heldSnapshot(revision),
    state: "online",
    safeToDisconnect: false,
  };
}

function activityRecord(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    id: "exchange-1",
    occurredAt: "2026-08-03T16:00:00+08:00",
    accessId: "work",
    status: "failed",
    ...overrides,
  };
}

function exchangeDetail(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    id: "exchange-1",
    accessId: "work",
    status: "failed",
    processingTrace: {
      egressProxyId: "direct",
      pluginRunIds: [],
      attemptIds: ["attempt-1"],
      result: "provider_transport_failed",
    },
    ...overrides,
  };
}

function approvalView(overrides: Partial<ApprovalView> = {}): ApprovalView {
  return {
    id: "approval-1",
    revision: 1,
    kind: "network_ask",
    state: "pending",
    risk: "medium",
    titleKey: "approval.networkAsk.title",
    summaryKey: "approval.networkAsk.summary",
    aggregateKey: "network:api.example.com:443",
    target: { host: "api.example.com", port: 443 },
    subjectRefs: ["connection-1"],
    subjectLabels: ["api.example.com:443"],
    requestCount: 1,
    waiterCount: 1,
    choices: [
      {
        decision: "allow-once",
        scope: "request",
        labelKey: "approval.networkAsk.choice.allowOnce",
      },
      {
        decision: "allow-once",
        scope: "host_port",
        labelKey: "approval.networkAsk.choice.allowHostPort",
      },
      {
        decision: "deny",
        scope: "request",
        labelKey: "approval.networkAsk.choice.denyOnce",
      },
      {
        decision: "deny",
        scope: "host_port",
        labelKey: "approval.networkAsk.choice.denyHostPort",
      },
    ],
    createdAt: "2026-08-03T08:00:00Z",
    expiresAt: "2026-08-03T08:05:00.123456789Z",
    ...overrides,
  };
}

function decidedApproval(overrides: Partial<ApprovalView> = {}): ApprovalView {
  return approvalView({
    revision: 2,
    state: "allowed",
    resolvedAt: "2026-08-03T08:01:00Z",
    decision: "allow-once",
    decisionScope: "request",
    ...overrides,
  });
}

function connectionRecord(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    sequence: 1,
    connectionId: "connection-1",
    sourceConfidence: "unknown",
    requestedHost: "api.example.com",
    routeHost: "api.example.com",
    ip: "2001:db8::1",
    port: 443,
    decision: "allow",
    ruleId: "network-rule-1",
    credentialBindingId: "credential-1",
    egressScope: "network",
    egressSource: "network_rule",
    egressRuleId: "egress-rule-1",
    egressSelectorRunId: "selector-run-1",
    egressProxyId: "direct",
    egressPolicyRevision: 1,
    decryption: "mitm",
    phase: "closed",
    bytesUp: 123,
    bytesDown: 456,
    startedAt: "2026-08-03T08:00:00Z",
    endedAt: "2026-08-03T08:00:01Z",
    outcome: "completed",
    ...overrides,
  };
}

function askedConnectionRecord(): ConnectionRecord {
  return {
    sequence: 2,
    connectionId: "connection-asked",
    sourceConfidence: "unknown",
    requestedHost: "ask.example.com",
    port: 443,
    decision: "ask",
    ruleId: "ask-rule-1",
    credentialBindingId: "credential-1",
    egressScope: "access",
    egressSource: "access_plugin",
    egressRuleId: "egress-rule-1",
    egressSelectorRunId: "selector-run-1",
    egressProxyId: "direct",
    egressPolicyRevision: 1,
    decryption: "blind",
    phase: "asked",
    bytesUp: 0,
    bytesDown: 0,
    startedAt: "2026-08-03T08:00:00Z",
  };
}

function egressAttemptRecord(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    sequence: 1,
    id: "attempt-1",
    connectionId: "connection-1",
    purpose: "blind_tunnel",
    payloadClass: "opaque_tunnel",
    parent: { kind: "blind_connection", id: "parent-1" },
    caller: "core",
    targetOrigin: "https://api.example.com:443",
    decision: {
      policyId: "network-policy-1",
      policyRevision: 1,
      authority: "network",
      ruleId: "network-rule-1",
      proxyId: "direct",
    },
    reusedTransport: false,
    startedAt: "2026-08-03T08:00:00Z",
    terminal: true,
    outcome: "completed",
    bytesOut: 123,
    bytesIn: 456,
    completedAt: "2026-08-03T08:00:01Z",
    ...overrides,
  };
}

function captureRunRecord(): Record<string, unknown> {
  return {
    id: "capture-1",
    executableLabel: "agent",
    cwd: "/tmp/project",
    state: "created",
    observation: "waiting_for_traffic",
    recognition: "unknown",
    clientAdapterState: "generic",
    clientRecognition: "unknown",
    catalogRevision: 1,
    createdAt: "2026-08-03T08:00:00Z",
    expiresAt: "2026-08-03T08:05:00Z",
  };
}

function accessApplyInput() {
  return buildAccessApplyInput({
    ...initialAccessForm,
    accessId: "work",
    mode: "managed",
    fixedModel: "example-model",
    name: "Work",
    providerOrigin: "https://gateway.example/v1",
    routeName: "Primary route",
  });
}

function accessDirectoryItem(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    accessId: "alpha",
    name: "Alpha",
    description: "Primary Access",
    status: "enabled",
    revision: 1,
    clientOrigin: "https://api.anthropic.com",
    clientDialect: "anthropic-messages",
    ...overrides,
  };
}

function accessDetail(
  accessId = "work",
  status: "draft" | "enabled" | "disabled" = "enabled",
): Record<string, unknown> {
  const endpointId = `${accessId}-agent`;
  const profileId = `${accessId}-openai`;
  const targetId = `${accessId}-target`;
  const accountId = `${accessId}-account`;
  const routeSetId = `${accessId}-routes`;
  const egressPolicyId = `${accessId}-egress`;
  return {
    revision: 4,
    access: {
      id: accessId,
      name: "Work",
      description: "Team Access",
      status,
      agentEndpointId: endpointId,
      defaultRouteSetId: routeSetId,
      profileIds: [profileId],
      egressPolicyId,
    },
    agentEndpoint: {
      id: endpointId,
      clientOrigin: "https://api.anthropic.com",
      clientDialect: "anthropic-messages",
    },
    profiles: [
      {
        id: profileId,
        kind: "managed",
        credentialSource: "managed_account",
        processingMode: "managed",
        name: "Work OpenAI",
        description: "Primary provider",
        backendDialect: "openai-chat",
        targetId,
        upstreamWireProfileRef: "follow-client",
        defaultModelPolicy: {
          mode: "fixed",
          fixedModel: "dashscope:glm-5",
        },
        accountBindingIds: [accountId],
        defaultAccountBindingId: accountId,
      },
    ],
    providerTargets: [
      {
        id: targetId,
        profileId,
        origin: "https://api.openai.com/v1",
        protocol: "openai-chat",
        capabilities: ["messages", "streaming", "tool_calls"],
      },
    ],
    accountBindings: [
      {
        id: accountId,
        profileId,
        label: "Work",
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

function originalAccessDetail(accessId = "work"): Record<string, unknown> {
  const payload = accessDetail(accessId);
  const endpoint = payload.agentEndpoint as Record<string, unknown>;
  const binding = payload.access as Record<string, unknown>;
  const routeSets = payload.routeSets as Record<string, unknown>[];
  binding.profileIds = ["original-passthrough"];
  payload.profiles = [
    {
      id: "original-passthrough",
      kind: "original_passthrough",
      credentialSource: "client_passthrough",
      processingMode: "observe_only",
      name: "Current client login",
      description: "",
      backendDialect: endpoint.clientDialect,
      targetId: "original-client-origin",
      upstreamWireProfileRef: "follow-client",
      defaultModelPolicy: { mode: "passthrough" },
      accountBindingIds: [],
      defaultAccountBindingId: "",
    },
  ];
  payload.providerTargets = [
    {
      id: "original-client-origin",
      profileId: "original-passthrough",
      origin: endpoint.clientOrigin,
      protocol: endpoint.clientDialect,
      capabilities: ["messages", "streaming", "tool_calls"],
    },
  ];
  payload.accountBindings = [];
  routeSets[0]!.candidateProfileIds = ["original-passthrough"];
  return payload;
}

function accessPlanSummary(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    accessId: "work",
    revision: 1,
    planHash: "a".repeat(64),
    profiles: ["original-passthrough", "work-openai"],
    accountBindings: [{ id: "work-account", profileId: "work-openai" }],
    ...overrides,
  };
}

function credentialView(
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    credentialId: "work-account",
    profileId: "work-openai",
    secretState: "configured",
    secretRevision: 1,
    ...overrides,
  };
}

function workspaceRouteBinding(
  overrides: Partial<WorkspaceRouteBinding> = {},
): WorkspaceRouteBinding {
  const machineId = capability(0x55);
  return {
    id: capability(0x66),
    accessId: "work",
    machineId,
    machineShortId: machineId.slice(0, 10),
    machineDisplayName: `Local machine ${machineId.slice(0, 10)}`,
    machineRegistrationRevision: 1,
    workspaceId: capability(0x77),
    workspaceLabel: "vibermate",
    workspaceEvidence: "local_launcher",
    profileId: "work-openai",
    revision: 1,
    state: "active",
    activeRunCount: 1,
    activeRuns: [
      {
        runId: "run-1",
        clientLabel: "claude",
        localUserLabel: "alice",
        state: "active",
        startedAt: "2026-08-03T08:00:00Z",
        lastActivityAt: "2026-08-03T08:00:01Z",
      },
    ],
    pinnedRequestCount: 0,
    approvedProfiles: [
      {
        profileId: "work-openai",
        kind: "managed",
        label: "Primary",
        modelPresentation: "gpt-5.6-sol",
        authPresentation: "vibermate_account",
        authLabel: "001",
        available: true,
      },
      {
        profileId: "work-backup",
        kind: "managed",
        label: "Backup",
        modelPresentation: "claude-sonnet-4-5",
        authPresentation: "vibermate_account",
        authLabel: "002",
        available: true,
      },
    ],
    updatedAt: "2026-08-03T08:00:00Z",
    ...overrides,
  };
}

const manualCaptureTag = `"mc_${"A".repeat(43)}"`;

function manualCaptureContext(overrides: Record<string, unknown> = {}) {
  return {
    confirmationToken: `ctx_${"B".repeat(43)}`,
    proxyAddress: "http://127.0.0.1:32123",
    root: {
      kind: "local_path",
      derSha256: "a".repeat(64),
      fingerprint: "AA:BB:CC:DD",
      pemPath: "/private/vibermate/root.pem",
    },
    defaultTemporarySeconds: 86_400,
    maxTemporarySeconds: 604_800,
    ...overrides,
  };
}

function manualCaptureRecord(overrides: Record<string, unknown> = {}) {
  return {
    id: "capture-one",
    ingressProfileId: "manual-capture/capture-one",
    displayName: "Project terminal",
    clientClass: "cli",
    lifetime: "temporary",
    state: "active",
    observation: "waiting_for_traffic",
    createdAt: "2026-08-03T08:00:00Z",
    updatedAt: "2026-08-03T08:00:00Z",
    expiresAt: "2026-08-04T08:00:00Z",
    ...overrides,
  };
}

function manualCaptureGrant(overrides: Record<string, unknown> = {}) {
  return {
    capture: manualCaptureRecord(),
    proxyAddress: "http://127.0.0.1:32123",
    proxyUsername: "capture",
    proxyPassword: `manual_${"C".repeat(43)}`,
    root: manualCaptureContext().root,
    ...overrides,
  };
}

function manualJSONResponse(
  value: unknown,
  status = 200,
  stateTag?: string,
): Response {
  const headers = new Headers({
    "Cache-Control": "no-store",
    "Content-Type": "application/json",
  });
  if (stateTag !== undefined) {
    headers.set("ETag", stateTag);
  }
  return new Response(JSON.stringify(value), { status, headers });
}

afterEach(() => {
  vi.useRealTimers();
});

describe("Desktop control client", () => {
  it("uses the closed ManualCapture contract without exposing numeric state", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (url, init) => {
        if (
          url.pathname === "/api/v1/manual-captures/context" &&
          init.method === "GET"
        ) {
          return manualJSONResponse(manualCaptureContext());
        }
        if (
          url.pathname === "/api/v1/manual-captures" &&
          init.method === "GET"
        ) {
          return manualJSONResponse({ items: [manualCaptureRecord()] });
        }
        if (
          url.pathname === "/api/v1/manual-captures/capture-one" &&
          init.method === "GET"
        ) {
          return manualJSONResponse(manualCaptureRecord(), 200, manualCaptureTag);
        }
        if (
          url.pathname === "/api/v1/manual-captures" &&
          init.method === "POST"
        ) {
          return manualJSONResponse(
            manualCaptureGrant(),
            201,
            manualCaptureTag,
          );
        }
        if (url.pathname.endsWith("/actions/rotate-credential")) {
          return manualJSONResponse(
            manualCaptureGrant(),
            200,
            `"mc_${"D".repeat(43)}"`,
          );
        }
        if (url.pathname.endsWith("/actions/revoke")) {
          return new Response(null, {
            status: 204,
            headers: { "Cache-Control": "no-store" },
          });
        }
        throw new Error(`unexpected ManualCapture request ${init.method} ${url}`);
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(client.manualCaptureContext()).resolves.toEqual(
      manualCaptureContext(),
    );
    await expect(client.manualCaptures()).resolves.toEqual({
      items: [manualCaptureRecord()],
    });
    await expect(client.manualCapture("capture-one")).resolves.toEqual({
      capture: manualCaptureRecord(),
      stateTag: manualCaptureTag,
    });
    await expect(
      client.createManualCapture({
        displayName: "Project terminal",
        clientClass: "cli",
        lifetime: "temporary",
        expiresInSeconds: 86_400,
        confirmationToken: `ctx_${"B".repeat(43)}`,
      }),
    ).resolves.toEqual({
      grant: manualCaptureGrant(),
      stateTag: manualCaptureTag,
    });
    await expect(
      client.rotateManualCapture("capture-one", manualCaptureTag),
    ).resolves.toEqual({
      grant: manualCaptureGrant(),
      stateTag: `"mc_${"D".repeat(43)}"`,
    });
    await expect(
      client.revokeManualCapture("capture-one", manualCaptureTag),
    ).resolves.toBeUndefined();

    const manualCalls = calls.filter(({ url }) =>
      url.pathname.startsWith("/api/v1/manual-captures"),
    );
    expect(manualCalls).toHaveLength(6);
    for (const call of manualCalls) {
      const headers = new Headers(call.init.headers);
      expect(headers.get("Idempotency-Key")).toBeNull();
      expect(headers.get("Authorization")).toMatch(/^Bearer /u);
      if (
        call.url.pathname.endsWith("/actions/rotate-credential") ||
        call.url.pathname.endsWith("/actions/revoke")
      ) {
        expect(headers.get("If-Match")).toBe(manualCaptureTag);
      } else {
        expect(headers.get("If-Match")).toBeNull();
      }
    }
    const createBody = String(
      manualCalls.find(
        ({ url, init }) =>
          url.pathname === "/api/v1/manual-captures" && init.method === "POST",
      )?.init.body,
    );
    expect(createBody).not.toContain("revision");
    expect(createBody).not.toContain("route");
  });

  it("never retries a ManualCapture mutation after a lost response", async () => {
    const bootstrap = session();
    let attempts = 0;
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url, init) => {
        expect(url.pathname).toBe("/api/v1/manual-captures");
        expect(init.method).toBe("POST");
        attempts += 1;
        throw new TypeError("response was lost after commit");
      }),
    );

    await expect(
      client.createManualCapture({
        displayName: "Project terminal",
        clientClass: "cli",
        lifetime: "until_revoked",
        confirmationToken: `ctx_${"B".repeat(43)}`,
      }),
    ).rejects.toThrow("response was lost after commit");
    expect(attempts).toBe(1);
  });

  it.each([
    {
      name: "a missing state tag",
      response: manualJSONResponse(manualCaptureRecord()),
    },
    {
      name: "a numeric-looking state tag",
      response: manualJSONResponse(manualCaptureRecord(), 200, '"revision-1"'),
    },
    {
      name: "a cacheable response",
      response: new Response(JSON.stringify(manualCaptureRecord()), {
        status: 200,
        headers: {
          "Cache-Control": "private",
          "Content-Type": "application/json",
          ETag: manualCaptureTag,
        },
      }),
    },
  ])("rejects $name on ManualCapture detail", async ({ response }) => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => response),
    );

    await expect(client.manualCapture("capture-one")).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it.each(["relative/root.pem", "/private/../root.pem"])(
    "rejects the non-canonical ManualCapture Root path %s",
    async (pemPath) => {
      const bootstrap = session();
      const context = manualCaptureContext();
      const client = await createControlClient(
        bootstrap,
        withSessionState(bootstrap, () =>
          manualJSONResponse({
            ...context,
            root: { ...context.root, pemPath },
          }),
        ),
      );

      await expect(client.manualCaptureContext()).rejects.toBeInstanceOf(
        ControlContractError,
      );
    },
  );

  it("reads and CAS-updates one stable machine workspace route", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (url) => {
        if (url.pathname === "/api/v1/workspace-route-bindings") {
          return jsonResponse({ items: [workspaceRouteBinding()] });
        }
        if (url.pathname.startsWith("/api/v1/workspace-route-bindings/")) {
          return jsonResponse(
            workspaceRouteBinding({
              profileId: "work-backup",
              revision: 2,
            }),
          );
        }
        return jsonResponse(statusResponse());
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    const page = await client.workspaceRouteBindings?.();
    const updated = await client.updateWorkspaceRouteBinding?.(
      capability(0x66),
      1,
      "work-backup",
    );

    expect(page?.items[0]?.activeRuns[0]?.localUserLabel).toBe("alice");
    expect(updated?.profileId).toBe("work-backup");
    expect(calls[2]?.init.method).toBe("PATCH");
    expect(new Headers(calls[2]?.init.headers).get("If-Match")).toBe("1");
    expect(calls[2]?.url.pathname).toBe(
      `/api/v1/workspace-route-bindings/${capability(0x66)}`,
    );
    expect(calls[2]?.init.body).toBe(
      JSON.stringify({ profileId: "work-backup" }),
    );
  });

  it("rejects a CaptureRun audit projection missing adapter evidence state", async () => {
    const bootstrap = session();
    const fetchImplementation = withSessionState(bootstrap, (url) => {
      if (url.pathname === "/api/v1/capture-runs") {
        return jsonResponse({
          items: [
            {
              id: "run-contract-drift",
              executableLabel: "agent",
              cwd: "/tmp",
              state: "created",
              observation: "waiting_for_traffic",
              recognition: "unknown",
              createdAt: "2026-08-03T08:00:00Z",
              expiresAt: "2026-08-03T08:01:00Z",
            },
          ],
        });
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(client.captureRuns()).rejects.toBeInstanceOf(ControlContractError);
  });

  it("inspects the current generation, then uses distinct capabilities and CAS headers", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (_url, init) =>
        jsonResponse(init.method === "GET" ? statusResponse() : heldSnapshot()),
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await client.status();
    await client.enterOfflineHold(1);

    expect(calls).toHaveLength(3);
    const inspectionHeaders = new Headers(calls[0]?.init.headers);
    const readHeaders = new Headers(calls[1]?.init.headers);
    const writeHeaders = new Headers(calls[2]?.init.headers);
    expect(calls[0]?.url.pathname).toBe(sessionStatePath);
    expect(inspectionHeaders.get("Authorization")).toBe(
      `Bearer ${bootstrap.readToken}`,
    );
    expect(readHeaders.get("Authorization")).toBe(`Bearer ${bootstrap.readToken}`);
    expect(writeHeaders.get("Authorization")).toBe(`Bearer ${bootstrap.writeToken}`);
    expect(writeHeaders.get("If-Match")).toBe("1");
    expect(writeHeaders.get("Idempotency-Key")).toMatch(
      /^[A-Za-z0-9_-]{16,128}$/u,
    );
    expect(calls[2]?.url.origin).toBe(bootstrap.baseUrl);
    expect(calls[2]?.url.href).not.toContain(bootstrap.writeToken);
    expect(calls[2]?.init.credentials).toBe("omit");
    expect(calls[2]?.init.redirect).toBe("error");
  });

  it("closes one session idempotently, aborts active work, and refuses future requests", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (_url, init) =>
        new Promise<Response>((_resolve, reject) => {
          const signal = init.signal;
          if (signal?.aborted === true) {
            reject(signal.reason);
            return;
          }
          signal?.addEventListener(
            "abort",
            () => reject(signal.reason),
            { once: true },
          );
        }),
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    const pending = client.status();
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    client.close();
    client.close();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(client.status()).rejects.toMatchObject({ name: "AbortError" });
    expect(calls).toHaveLength(2);
  });

  it("renews a short session before expiry and uses both new capabilities", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const newReadToken = capability(0x33);
    const newWriteToken = capability(0x44);
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (url, init) => {
        if (url.pathname === sessionRenewalPath) {
          return jsonResponse(
            sessionRotation(rotatedExpiry, 2, newReadToken, newWriteToken),
          );
        }
        return jsonResponse(
          init.method === "GET" ? statusResponse() : heldSnapshot(),
        );
      },
      calls,
    );
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );

    currentTime += 1_100;
    await client.status();
    await client.enterOfflineHold(1);

    const renewal = calls.find((call) => call.url.pathname === sessionRenewalPath);
    const status = calls.find((call) => call.url.pathname === "/api/v1/status");
    const mutation = calls.find(
      (call) => call.url.pathname === "/api/v1/offline-hold/actions/enter",
    );
    expect(new Headers(renewal?.init.headers).get("Authorization")).toBe(
      `Bearer ${bootstrap.writeToken}`,
    );
    expect(new Headers(renewal?.init.headers).get("If-Match")).toBe("1");
    expect(new Headers(status?.init.headers).get("Authorization")).toBe(
      `Bearer ${newReadToken}`,
    );
    expect(new Headers(mutation?.init.headers).get("Authorization")).toBe(
      `Bearer ${newWriteToken}`,
    );
  });

  it("coalesces concurrent requests into one session rotation", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const newReadToken = capability(0x35);
    const newWriteToken = capability(0x46);
    let renewals = 0;
    let releaseRenewal = () => {};
    const renewalGate = new Promise<void>((resolve) => {
      releaseRenewal = resolve;
    });
    let rotationObserved = () => {};
    const observed = new Promise<void>((resolve) => {
      rotationObserved = resolve;
    });
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      async (url) => {
        if (url.pathname === sessionRenewalPath) {
          renewals += 1;
          rotationObserved();
          await renewalGate;
          return jsonResponse(
            sessionRotation(rotatedExpiry, 2, newReadToken, newWriteToken),
          );
        }
        return jsonResponse(statusResponse());
      },
      calls,
    );
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    const pending = Promise.all(Array.from({ length: 16 }, () => client.status()));
    await observed;
    expect(renewals).toBe(1);
    releaseRenewal();
    await pending;

    expect(renewals).toBe(1);
    const statusCalls = calls.filter((call) => call.url.pathname === "/api/v1/status");
    expect(statusCalls).toHaveLength(16);
    expect(
      statusCalls.every(
        (call) =>
          new Headers(call.init.headers).get("Authorization") ===
          `Bearer ${newReadToken}`,
      ),
    ).toBe(true);
  });

  it("retries a lost renewal response with exactly the same command", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const newReadToken = capability(0x37);
    const newWriteToken = capability(0x48);
    const renewalCalls: FetchCall[] = [];
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        if (renewalCalls.length === 1) {
          throw new TypeError("response connection was lost after commit");
        }
        return jsonResponse(
          sessionRotation(rotatedExpiry, 2, newReadToken, newWriteToken),
        );
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await client.status();

    expect(renewalCalls).toHaveLength(2);
    for (const header of ["Authorization", "If-Match", "Idempotency-Key"]) {
      expect(new Headers(renewalCalls[0]?.init.headers).get(header)).toBe(
        new Headers(renewalCalls[1]?.init.headers).get(header),
      );
    }
    expect(new Headers(renewalCalls[0]?.init.headers).get("Authorization")).toBe(
      `Bearer ${bootstrap.writeToken}`,
    );
    expect(new Headers(renewalCalls[0]?.init.headers).get("If-Match")).toBe("1");
  });

  it("requires exact 200 success statuses for session and ordinary reads", async () => {
    const bootstrap = session();
    await expect(
      createControlClient(
        bootstrap,
        vi.fn(async () =>
          jsonResponse(sessionState(bootstrap.expiresAt), 201),
        ),
      ),
    ).rejects.toBeInstanceOf(ControlContractError);

    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(statusResponse(), 201)),
    );
    await expect(client.status()).rejects.toBeInstanceOf(ControlContractError);
  });

  it("replays an unexpected renewal success status with the same command", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        return jsonResponse(
          sessionRotation(rotatedExpiry),
          renewalCalls.length === 1 ? 201 : 200,
        );
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await client.status();

    expect(renewalCalls).toHaveLength(2);
    expect(
      new Headers(renewalCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(renewalCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it("replays an unresolved renewal key after the old session expires", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        if (renewalCalls.length <= 2) {
          throw new TypeError("two renewal receipts were lost");
        }
        return jsonResponse(sessionRotation(rotatedExpiry));
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await expect(client.status()).rejects.toThrow(/renewal receipts/u);
    currentTime = fixedNow + 2_100;
    await expect(client.status()).resolves.toEqual(statusResponse());

    expect(renewalCalls).toHaveLength(3);
    const keys = renewalCalls.map((call) =>
      new Headers(call.init.headers).get("Idempotency-Key"),
    );
    expect(keys[0]).toMatch(/^[A-Za-z0-9_-]{16,128}$/u);
    expect(new Set(keys).size).toBe(1);
  });

  it("does not create a new renewal command after session expiry", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url) => {
        if (url.pathname === sessionRenewalPath) {
          renewalCalls.push({ url, init: {} });
          return jsonResponse(
            sessionRotation(new Date(fixedNow + 20_000).toISOString()),
          );
        }
        return jsonResponse(statusResponse());
      }),
      () => currentTime,
    );
    currentTime = fixedNow + 2_000;

    await expect(client.status()).rejects.toThrow(/session is expired/u);
    expect(renewalCalls).toHaveLength(0);
  });

  it("does not replay renewal after the client owner closes", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        markStarted();
        return new Promise<Response>(() => undefined);
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    const pending = client.status();
    await started;
    client.close();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(client.status()).rejects.toMatchObject({ name: "AbortError" });
    expect(renewalCalls).toHaveLength(1);
  });

  it("releases a renewal key after an authoritative Problem", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        return renewalCalls.length === 1
          ? problemResponse(409, "revision_conflict")
          : jsonResponse(sessionRotation(rotatedExpiry));
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await expect(client.status()).rejects.toBeInstanceOf(ControlProblem);
    await expect(client.status()).resolves.toEqual(statusResponse());

    expect(renewalCalls).toHaveLength(2);
    expect(
      new Headers(renewalCalls[0]?.init.headers).get("Idempotency-Key"),
    ).not.toBe(new Headers(renewalCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it.each([
    [
      "unknown fields",
      {
        ...(sessionRotation(new Date(fixedNow + 20_000).toISOString()) as object),
        baseUrl: "http://127.0.0.1:43127",
      },
    ],
    [
      "schema",
      {
        ...(sessionRotation(new Date(fixedNow + 20_000).toISOString()) as object),
        schema: "vibermate-app-session-rotation-v2",
      },
    ],
    [
      "revision",
      sessionRotation(new Date(fixedNow + 20_000).toISOString(), 1),
    ],
    [
      "reused capability",
      sessionRotation(
        new Date(fixedNow + 20_000).toISOString(),
        2,
        capability(0x11),
        capability(0x44),
      ),
    ],
    [
      "expiry",
      sessionRotation(new Date(fixedNow - 1).toISOString()),
    ],
  ])("rejects a malformed rotation %s without publishing it", async (_name, rotation) => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    let ordinaryRequests = 0;
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        return jsonResponse(rotation);
      }
      ordinaryRequests += 1;
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await expect(client.status()).rejects.toBeInstanceOf(ControlContractError);

    expect(renewalCalls).toHaveLength(2);
    expect(ordinaryRequests).toBe(0);
    expect(
      renewalCalls.every(
        (call) =>
          new Headers(call.init.headers).get("Authorization") ===
            `Bearer ${bootstrap.writeToken}` &&
          new Headers(call.init.headers).get("If-Match") === "1",
      ),
    ).toBe(true);
  });

  it.each([
    [401, "unauthorized"],
    [409, "revision_conflict"],
  ])("does not retry a %i renewal failure", async (status, reasonCode) => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const bootstrap = session(initialExpiry);
    let renewals = 0;
    let ordinaryRequests = 0;
    const fetchImplementation = withSessionState(bootstrap, (url) => {
      if (url.pathname === sessionRenewalPath) {
        renewals += 1;
        return problemResponse(status, reasonCode);
      }
      ordinaryRequests += 1;
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;

    await expect(client.status()).rejects.toEqual(
      expect.objectContaining<Partial<ControlProblem>>({ status, reasonCode }),
    );
    expect(renewals).toBe(1);
    expect(ordinaryRequests).toBe(0);
  });

  it("applies one absolute deadline to bootstrap fetch and cleans its timer", async () => {
    vi.useFakeTimers();
    let requestSignal: AbortSignal | undefined;
    const fetchImplementation = vi.fn(
      (_input: RequestInfo | URL, init: RequestInit = {}) => {
        requestSignal = init.signal ?? undefined;
        return new Promise<Response>(() => undefined);
      },
    );

    const pending = createControlClient(session(), fetchImplementation);
    const rejected = expect(pending).rejects.toEqual(
      expect.objectContaining({ name: "TimeoutError" }),
    );
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);

    await rejected;
    expect(requestSignal?.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("combines caller abort with the request deadline and removes its listener", async () => {
    const bootstrap = session();
    let requestSignal: AbortSignal | undefined;
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(bootstrap, (_url, init) => {
      requestSignal = init.signal ?? undefined;
      markStarted();
      return new Promise<Response>(() => undefined);
    });
    const client = await createControlClient(bootstrap, fetchImplementation);
    vi.useFakeTimers();
    const owner = new AbortController();
    const removeListener = vi.spyOn(owner.signal, "removeEventListener");
    const reason = new DOMException("route disposed", "AbortError");

    const pending = client.status(owner.signal);
    await started;
    owner.abort(reason);

    await expect(pending).rejects.toBe(reason);
    expect(requestSignal?.aborted).toBe(true);
    expect(removeListener).toHaveBeenCalledWith("abort", expect.any(Function));
    expect(vi.getTimerCount()).toBe(0);
  });

  it("times out and cancels a deferred response body", async () => {
    const bootstrap = session();
    const cancel = vi.fn();
    let markPulled: () => void = () => undefined;
    const pulled = new Promise<void>((resolve) => {
      markPulled = resolve;
    });
    const body = new ReadableStream<Uint8Array>({
      cancel,
      pull: () => markPulled(),
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () =>
          new Response(body, {
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    vi.useFakeTimers();

    const pending = client.status();
    await pulled;
    const rejected = expect(pending).rejects.toEqual(
      expect.objectContaining({ name: "TimeoutError" }),
    );
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);

    await rejected;
    expect(cancel).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("cancels a chunked response immediately after it crosses two MiB", async () => {
    const bootstrap = session();
    const cancel = vi.fn();
    let pull = 0;
    const body = new ReadableStream<Uint8Array>(
      {
        cancel,
        pull: (controller) => {
          controller.enqueue(
            new Uint8Array(pull++ === 0 ? maximumResponseBytes : 1),
          );
        },
      },
      { highWaterMark: 0 },
    );
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () =>
          new Response(body, {
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );

    await expect(client.status()).rejects.toBeInstanceOf(ControlContractError);
    expect(cancel).toHaveBeenCalledOnce();
    expect(pull).toBe(2);
  });

  it.each([
    ["missing", () => null],
    [
      "errored",
      () =>
        new ReadableStream<Uint8Array>({
          pull: (controller) => controller.error(new Error("raw stream failure")),
        }),
    ],
  ])("rejects a %s response body as a contract error", async (_name, makeBody) => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () =>
          new Response(makeBody(), {
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );

    await expect(client.status()).rejects.toBeInstanceOf(ControlContractError);
  });

  it("retains a timed-out ordinary mutation for an explicit same-key retry", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => {
        mutations += 1;
        if (mutations === 1) {
          markStarted();
          return new Promise<Response>(() => undefined);
        }
        return jsonResponse(heldSnapshot());
      }, calls),
    );
    vi.useFakeTimers();

    const pending = client.enterOfflineHold(1);
    await started;
    const rejected = expect(pending).rejects.toEqual(
      expect.objectContaining({ name: "TimeoutError" }),
    );
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);

    await rejected;
    expect(mutations).toBe(1);

    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each([
    [
      "a lost transport receipt",
      () => {
        throw new TypeError("ordinary mutation receipt was lost");
      },
    ],
    ["an unexpected 2xx status", () => jsonResponse(heldSnapshot(), 201)],
  ])("replays %s for an ordinary mutation with the same key", async (_name, first) => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          return mutations === 1 ? first() : jsonResponse(heldSnapshot());
        },
        calls,
      ),
    );

    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it("keeps a settled command available to an overlapping identity registration", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveFirstResponse: (response: Response) => void = () => undefined;
    const firstResponse = new Promise<Response>((resolve) => {
      resolveFirstResponse = resolve;
    });
    let markFirstStarted: () => void = () => undefined;
    const firstStarted = new Promise<void>((resolve) => {
      markFirstStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markFirstStarted();
            return firstResponse;
          }
          throw new Error("overlapping registration issued a new command");
        },
        calls,
      ),
    );
    const originalDigest = globalThis.crypto.subtle.digest.bind(
      globalThis.crypto.subtle,
    );
    let digests = 0;
    let releaseSecondDigest: () => void = () => undefined;
    const secondDigestGate = new Promise<void>((resolve) => {
      releaseSecondDigest = resolve;
    });
    let markSecondDigestStarted: () => void = () => undefined;
    const secondDigestStarted = new Promise<void>((resolve) => {
      markSecondDigestStarted = resolve;
    });
    const digestSpy = vi
      .spyOn(globalThis.crypto.subtle, "digest")
      .mockImplementation(async (algorithm, data) => {
        digests += 1;
        if (digests === 2) {
          markSecondDigestStarted();
          await secondDigestGate;
        }
        return originalDigest(algorithm, data);
      });

    try {
      const first = client.enterOfflineHold(1);
      await firstStarted;
      const overlapping = client.enterOfflineHold(1);
      await secondDigestStarted;

      resolveFirstResponse(jsonResponse(heldSnapshot()));
      await expect(first).resolves.toEqual(heldSnapshot());
      releaseSecondDigest();

      await expect(overlapping).resolves.toEqual(heldSnapshot());
      expect(mutations).toBe(1);
      expect(
        calls.filter(({ url }) => url.pathname.endsWith("/actions/enter")),
      ).toHaveLength(1);
    } finally {
      digestSpy.mockRestore();
      releaseSecondDigest();
    }
  });

  it("returns a concurrent success when another caller's replay loses its receipt", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let rejectReplay: (error: unknown) => void = () => undefined;
    const replayResponse = new Promise<Response>((_resolve, reject) => {
      rejectReplay = reject;
    });
    let markReplayStarted: () => void = () => undefined;
    const replayStarted = new Promise<void>((resolve) => {
      markReplayStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            throw new TypeError("first receipt was lost");
          }
          if (mutations === 2) {
            markReplayStarted();
            return replayResponse;
          }
          if (mutations === 3) {
            return jsonResponse(heldSnapshot());
          }
          throw new Error("concurrent success should settle both callers");
        },
        calls,
      ),
    );

    const replaying = client.enterOfflineHold(1);
    await replayStarted;
    const concurrent = client.enterOfflineHold(1);
    await expect(concurrent).resolves.toEqual(heldSnapshot());
    rejectReplay(new TypeError("replay receipt was also lost"));

    await expect(replaying).resolves.toEqual(heldSnapshot());
    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(3);
    expect(
      new Set(
        mutationCalls.map((call) =>
          new Headers(call.init.headers).get("Idempotency-Key"),
        ),
      ).size,
    ).toBe(1);
  });

  it("retains a caller-aborted ordinary mutation without hidden replay", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markStarted();
            return new Promise<Response>(() => undefined);
          }
          return jsonResponse(heldSnapshot());
        },
        calls,
      ),
    );
    const caller = new AbortController();
    const reason = new DOMException("route disposed", "AbortError");

    const first = client.enterOfflineHold(1, caller.signal);
    await started;
    caller.abort(reason);

    await expect(first).rejects.toBe(reason);
    expect(mutations).toBe(1);
    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it("never accepts a previously observed wire Problem as an abort authority", async () => {
    const bootstrap = session();
    const source = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () =>
        problemResponse(409, "revision_conflict"),
      ),
    );
    const observed = await source.status().catch((error: unknown) => error);
    expect(observed).toBeInstanceOf(ControlProblem);
    if (!(observed instanceof ControlProblem)) {
      throw new Error("wire Problem was not exposed as a ControlProblem");
    }
    source.close();

    const calls: FetchCall[] = [];
    let ordinaryMutations = 0;
    let accessMutations = 0;
    let markOrdinaryStarted: () => void = () => undefined;
    const ordinaryStarted = new Promise<void>((resolve) => {
      markOrdinaryStarted = resolve;
    });
    let markAccessStarted: () => void = () => undefined;
    const accessStarted = new Promise<void>((resolve) => {
      markAccessStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        (url) => {
          if (url.pathname.endsWith("/actions/enter")) {
            ordinaryMutations += 1;
            if (ordinaryMutations === 1) {
              markOrdinaryStarted();
              return new Promise<Response>(() => undefined);
            }
            return jsonResponse(heldSnapshot());
          }
          if (url.pathname.endsWith("/actions/apply")) {
            accessMutations += 1;
            if (accessMutations === 1) {
              markAccessStarted();
              return new Promise<Response>(() => undefined);
            }
            return jsonResponse({
              outcome: "committed",
              revision: 1,
              applicationState: "active",
              planHash: "a".repeat(64),
            });
          }
          throw new Error("unexpected control path");
        },
        calls,
      ),
    );

    const ordinaryAbort = new AbortController();
    const ordinary = client.enterOfflineHold(1, ordinaryAbort.signal);
    await ordinaryStarted;
    ordinaryAbort.abort(observed);
    await expect(ordinary).rejects.toBe(observed);
    expect(ordinaryMutations).toBe(1);
    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const accessAbort = new AbortController();
    const access = client.applyAccess(
      "work",
      accessApplyInput(),
      accessAbort.signal,
    );
    await accessStarted;
    accessAbort.abort(observed);
    await expect(access).rejects.toBe(observed);
    expect(accessMutations).toBe(1);
    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    for (const suffix of ["/actions/enter", "/actions/apply"]) {
      const mutationCalls = calls.filter(({ url }) =>
        url.pathname.endsWith(suffix),
      );
      expect(mutationCalls).toHaveLength(2);
      expect(
        new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
      ).toBe(
        new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"),
      );
    }
  });

  it("safely settles a concurrent timeout from a same-key success", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveFirstResponse: (response: Response) => void = () => undefined;
    const firstResponse = new Promise<Response>((resolve) => {
      resolveFirstResponse = resolve;
    });
    let markFirstStarted: () => void = () => undefined;
    const firstStarted = new Promise<void>((resolve) => {
      markFirstStarted = resolve;
    });
    let markSecondStarted: () => void = () => undefined;
    const secondStarted = new Promise<void>((resolve) => {
      markSecondStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markFirstStarted();
            return firstResponse;
          }
          if (mutations === 2) {
            markSecondStarted();
            return new Promise<Response>(() => undefined);
          }
          throw new Error("authoritative success should settle the retry");
        },
        calls,
      ),
    );
    vi.useFakeTimers();

    const first = client.enterOfflineHold(1);
    await firstStarted;
    const second = client.enterOfflineHold(1);
    await secondStarted;
    resolveFirstResponse(jsonResponse(heldSnapshot()));

    await expect(first).resolves.toEqual(heldSnapshot());
    const timedOut = expect(second).rejects.toMatchObject({
      name: "TimeoutError",
    });
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);
    await timedOut;

    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());
    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it("safely settles a concurrent abort from a same-key Problem", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveFirstResponse: (response: Response) => void = () => undefined;
    const firstResponse = new Promise<Response>((resolve) => {
      resolveFirstResponse = resolve;
    });
    let markFirstStarted: () => void = () => undefined;
    const firstStarted = new Promise<void>((resolve) => {
      markFirstStarted = resolve;
    });
    let markSecondStarted: () => void = () => undefined;
    const secondStarted = new Promise<void>((resolve) => {
      markSecondStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markFirstStarted();
            return firstResponse;
          }
          if (mutations === 2) {
            markSecondStarted();
            return new Promise<Response>(() => undefined);
          }
          return problemResponse(409, "revision_conflict");
        },
        calls,
      ),
    );
    const caller = new AbortController();
    const abort = new DOMException("caller left", "AbortError");

    const first = client.enterOfflineHold(1);
    await firstStarted;
    const second = client.enterOfflineHold(1, caller.signal);
    await secondStarted;
    resolveFirstResponse(problemResponse(409, "revision_conflict"));
    caller.abort(abort);
    await expect(second).rejects.toBe(abort);
    await expect(first).rejects.toBeInstanceOf(ControlProblem);
    await expect(client.enterOfflineHold(1)).rejects.toMatchObject({
      reasonCode: "revision_conflict",
      status: 409,
    });

    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(3);
    expect(
      new Set(
        mutationCalls.map((call) =>
          new Headers(call.init.headers).get("Idempotency-Key"),
        ),
      ).size,
    ).toBe(1);
  });

  it("withholds a same-key Problem until a later concurrent success wins", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveSuccess: (response: Response) => void = () => undefined;
    const successResponse = new Promise<Response>((resolve) => {
      resolveSuccess = resolve;
    });
    let resolveProblem: (response: Response) => void = () => undefined;
    const problem = new Promise<Response>((resolve) => {
      resolveProblem = resolve;
    });
    let markFirstStarted: () => void = () => undefined;
    const firstStarted = new Promise<void>((resolve) => {
      markFirstStarted = resolve;
    });
    let markSecondStarted: () => void = () => undefined;
    const secondStarted = new Promise<void>((resolve) => {
      markSecondStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markFirstStarted();
            return successResponse;
          }
          markSecondStarted();
          return problem;
        },
        calls,
      ),
    );

    const first = client.enterOfflineHold(1);
    await firstStarted;
    const second = client.enterOfflineHold(1);
    await secondStarted;
    resolveProblem(problemResponse(409, "revision_conflict"));
    let problemCallerSettled = false;
    void second.then(
      () => {
        problemCallerSettled = true;
      },
      () => {
        problemCallerSettled = true;
      },
    );
    await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
    expect(problemCallerSettled).toBe(false);

    resolveSuccess(jsonResponse(heldSnapshot()));

    await expect(first).resolves.toEqual(heldSnapshot());
    await expect(second).resolves.toEqual(heldSnapshot());
    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it.each([
    ["first", "revision_conflict"],
    ["second", "unauthorized"],
  ] as const)(
    "waits for all same-key Problems when the %s response arrives first",
    async (firstArrival, expectedReasonCode) => {
      const bootstrap = session();
      const calls: FetchCall[] = [];
      let mutations = 0;
      let resolveFirst: (response: Response) => void = () => undefined;
      const firstResponse = new Promise<Response>((resolve) => {
        resolveFirst = resolve;
      });
      let resolveSecond: (response: Response) => void = () => undefined;
      const secondResponse = new Promise<Response>((resolve) => {
        resolveSecond = resolve;
      });
      let markFirstStarted: () => void = () => undefined;
      const firstStarted = new Promise<void>((resolve) => {
        markFirstStarted = resolve;
      });
      let markSecondStarted: () => void = () => undefined;
      const secondStarted = new Promise<void>((resolve) => {
        markSecondStarted = resolve;
      });
      const client = await createControlClient(
        bootstrap,
        withSessionState(
          bootstrap,
          () => {
            mutations += 1;
            if (mutations === 1) {
              markFirstStarted();
              return firstResponse;
            }
            if (mutations === 2) {
              markSecondStarted();
              return secondResponse;
            }
            return jsonResponse(heldSnapshot());
          },
          calls,
        ),
      );

      const first = client.enterOfflineHold(1);
      await firstStarted;
      const second = client.enterOfflineHold(1);
      await secondStarted;
      let firstSettled = false;
      let secondSettled = false;
      void first.then(
        () => {
          firstSettled = true;
        },
        () => {
          firstSettled = true;
        },
      );
      void second.then(
        () => {
          secondSettled = true;
        },
        () => {
          secondSettled = true;
        },
      );

      if (firstArrival === "first") {
        resolveFirst(problemResponse(409, "revision_conflict"));
      } else {
        resolveSecond(problemResponse(401, "unauthorized"));
      }
      await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
      expect(firstArrival === "first" ? firstSettled : secondSettled).toBe(false);

      if (firstArrival === "first") {
        resolveSecond(problemResponse(401, "unauthorized"));
      } else {
        resolveFirst(problemResponse(409, "revision_conflict"));
      }
      const [firstError, secondError] = await Promise.all([
        first.catch((error: unknown) => error),
        second.catch((error: unknown) => error),
      ]);
      expect(firstError).toMatchObject({ reasonCode: expectedReasonCode });
      expect(secondError).toMatchObject({ reasonCode: expectedReasonCode });
      expect(firstError).not.toBe(secondError);

      await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());
      const mutationCalls = calls.filter(({ url }) =>
        url.pathname.endsWith("/actions/enter"),
      );
      expect(mutationCalls).toHaveLength(3);
      const firstKey = new Headers(mutationCalls[0]?.init.headers).get(
        "Idempotency-Key",
      );
      expect(firstKey).toBe(
        new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"),
      );
      expect(firstKey).not.toBe(
        new Headers(mutationCalls[2]?.init.headers).get("Idempotency-Key"),
      );
    },
  );

  it.each(["problem-first", "ambiguity-first"] as const)(
    "retains a same-key Problem plus ambiguity in %s order for explicit replay",
    async (order) => {
      const bootstrap = session();
      const calls: FetchCall[] = [];
      let mutations = 0;
      let resolveProblem: (response: Response) => void = () => undefined;
      const problemResponseGate = new Promise<Response>((resolve) => {
        resolveProblem = resolve;
      });
      let markProblemStarted: () => void = () => undefined;
      const problemStarted = new Promise<void>((resolve) => {
        markProblemStarted = resolve;
      });
      let markAmbiguousStarted: () => void = () => undefined;
      const ambiguousStarted = new Promise<void>((resolve) => {
        markAmbiguousStarted = resolve;
      });
      const client = await createControlClient(
        bootstrap,
        withSessionState(
          bootstrap,
          () => {
            mutations += 1;
            if (mutations === 1) {
              markProblemStarted();
              return problemResponseGate;
            }
            if (mutations === 2) {
              markAmbiguousStarted();
              return new Promise<Response>(() => undefined);
            }
            return jsonResponse(heldSnapshot());
          },
          calls,
        ),
      );
      const caller = new AbortController();
      const abort = new DOMException("caller left", "AbortError");

      const problemCaller = client.enterOfflineHold(1);
      await problemStarted;
      const ambiguousCaller = client.enterOfflineHold(1, caller.signal);
      await ambiguousStarted;
      let problemSettled = false;
      void problemCaller.then(
        () => {
          problemSettled = true;
        },
        () => {
          problemSettled = true;
        },
      );

      if (order === "problem-first") {
        resolveProblem(problemResponse(409, "revision_conflict"));
        await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
        expect(problemSettled).toBe(false);
        caller.abort(abort);
      } else {
        caller.abort(abort);
        await expect(ambiguousCaller).rejects.toBe(abort);
        resolveProblem(problemResponse(409, "revision_conflict"));
      }

      await expect(ambiguousCaller).rejects.toBe(abort);
      await expect(problemCaller).rejects.toMatchObject({
        reasonCode: "revision_conflict",
        status: 409,
      });
      await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

      const mutationCalls = calls.filter(({ url }) =>
        url.pathname.endsWith("/actions/enter"),
      );
      expect(mutationCalls).toHaveLength(3);
      const keys = mutationCalls.map((call) =>
        new Headers(call.init.headers).get("Idempotency-Key"),
      );
      expect(new Set(keys).size).toBe(1);
    },
  );

  it("keeps a retained success receipt isolated from caller mutation", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveSuccess: (response: Response) => void = () => undefined;
    const successResponse = new Promise<Response>((resolve) => {
      resolveSuccess = resolve;
    });
    let markSuccessStarted: () => void = () => undefined;
    const successStarted = new Promise<void>((resolve) => {
      markSuccessStarted = resolve;
    });
    let markAmbiguousStarted: () => void = () => undefined;
    const ambiguousStarted = new Promise<void>((resolve) => {
      markAmbiguousStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markSuccessStarted();
            return successResponse;
          }
          if (mutations === 2) {
            markAmbiguousStarted();
            return new Promise<Response>(() => undefined);
          }
          throw new Error("retained success should satisfy explicit retry");
        },
        calls,
      ),
    );
    const caller = new AbortController();
    const abort = new DOMException("caller left", "AbortError");

    const successful = client.enterOfflineHold(1);
    await successStarted;
    const ambiguous = client.enterOfflineHold(1, caller.signal);
    await ambiguousStarted;
    resolveSuccess(jsonResponse(heldSnapshot()));
    const firstReceipt = await successful;
    const mutableReceipt = firstReceipt as {
      since: string;
      activeByKind: Record<string, number>;
    };
    mutableReceipt.since = "2026-08-03T09:00:00Z";
    mutableReceipt.activeByKind.provider = 1;
    caller.abort(abort);
    await expect(ambiguous).rejects.toBe(abort);

    const replayedReceipt = await client.enterOfflineHold(1);
    expect(replayedReceipt).toEqual(heldSnapshot());
    expect(replayedReceipt).not.toBe(firstReceipt);
    expect(
      calls.filter(({ url }) => url.pathname.endsWith("/actions/enter")),
    ).toHaveLength(2);
  });

  it("keeps a retained Problem receipt isolated from caller mutation", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let resolveProblem: (response: Response) => void = () => undefined;
    const problemResponseGate = new Promise<Response>((resolve) => {
      resolveProblem = resolve;
    });
    let markProblemStarted: () => void = () => undefined;
    const problemStarted = new Promise<void>((resolve) => {
      markProblemStarted = resolve;
    });
    let markAmbiguousStarted: () => void = () => undefined;
    const ambiguousStarted = new Promise<void>((resolve) => {
      markAmbiguousStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations === 1) {
            markProblemStarted();
            return problemResponseGate;
          }
          if (mutations === 2) {
            markAmbiguousStarted();
            return new Promise<Response>(() => undefined);
          }
          return problemResponse(409, "revision_conflict");
        },
        calls,
      ),
    );
    const caller = new AbortController();
    const abort = new DOMException("caller left", "AbortError");

    const problemCaller = client.enterOfflineHold(1);
    const observedProblem = problemCaller.catch((error: unknown) => error);
    await problemStarted;
    const ambiguous = client.enterOfflineHold(1, caller.signal);
    await ambiguousStarted;
    resolveProblem(problemResponse(409, "revision_conflict"));
    caller.abort(abort);
    await expect(ambiguous).rejects.toBe(abort);
    const firstProblem = await observedProblem;
    expect(firstProblem).toBeInstanceOf(ControlProblem);
    const mutableProblem = firstProblem as {
      status: number;
      reasonCode: string;
      messageKey: string;
    };
    mutableProblem.status = 500;
    mutableProblem.reasonCode = "caller_corrupted";
    mutableProblem.messageKey = "error.caller_corrupted";

    const replayedProblem = await client
      .enterOfflineHold(1)
      .catch((error: unknown) => error);
    expect(replayedProblem).toBeInstanceOf(ControlProblem);
    expect(replayedProblem).toMatchObject({
      status: 409,
      reasonCode: "revision_conflict",
      messageKey: "error.revision_conflict",
    });
    expect(replayedProblem).not.toBe(firstProblem);
    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(3);
    expect(
      new Set(
        mutationCalls.map((call) =>
          new Headers(call.init.headers).get("Idempotency-Key"),
        ),
      ).size,
    ).toBe(1);
  });

  it("does not replay an ordinary mutation after the client owner closes", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          markStarted();
          return new Promise<Response>(() => undefined);
        },
        calls,
      ),
    );

    const pending = client.enterOfflineHold(1);
    await started;
    client.close();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(client.enterOfflineHold(1)).rejects.toMatchObject({
      name: "AbortError",
    });
    expect(
      calls.filter(({ url }) => url.pathname.endsWith("/actions/enter")),
    ).toHaveLength(1);
  });

  it("releases an ordinary mutation key after an authoritative Problem", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          return mutations === 1
            ? problemResponse(409, "revision_conflict")
            : jsonResponse(heldSnapshot());
        },
        calls,
      ),
    );

    await expect(client.enterOfflineHold(1)).rejects.toBeInstanceOf(
      ControlProblem,
    );
    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(2);
    expect(
      new Headers(mutationCalls[0]?.init.headers).get("Idempotency-Key"),
    ).not.toBe(new Headers(mutationCalls[1]?.init.headers).get("Idempotency-Key"));
  });

  it("bounds unresolved ordinary mutation commands", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let recoverFirst = false;
    let markStarted: () => void = () => undefined;
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        (_url, init) => {
          mutations += 1;
          if (
            recoverFirst &&
            new Headers(init.headers).get("If-Match") === "1"
          ) {
            return jsonResponse(heldSnapshot());
          }
          markStarted();
          return new Promise<Response>(() => undefined);
        },
        calls,
      ),
    );

    for (let revision = 1; revision <= 16; revision += 1) {
      let resolveStarted: () => void = () => undefined;
      const started = new Promise<void>((resolve) => {
        resolveStarted = resolve;
      });
      markStarted = resolveStarted;
      const caller = new AbortController();
      const pending = client.enterOfflineHold(revision, caller.signal);
      await started;
      caller.abort(new DOMException("caller stopped", "AbortError"));
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    }

    await expect(client.enterOfflineHold(17)).rejects.toThrow(
      /too many unresolved mutation commands/u,
    );
    expect(mutations).toBe(16);

    recoverFirst = true;
    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());

    const firstCommandCalls = calls.filter(
      ({ url, init }) =>
        url.pathname.endsWith("/actions/enter") &&
        new Headers(init.headers).get("If-Match") === "1",
    );
    expect(firstCommandCalls).toHaveLength(2);
    expect(
      new Headers(firstCommandCalls[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(
      new Headers(firstCommandCalls[1]?.init.headers).get("Idempotency-Key"),
    );
    expect(mutations).toBe(17);
  });

  it("bounds active callers for one mutation identity and preserves explicit recovery", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let mutations = 0;
    let markAllStarted: () => void = () => undefined;
    const allStarted = new Promise<void>((resolve) => {
      markAllStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          mutations += 1;
          if (mutations <= 16) {
            if (mutations === 16) {
              markAllStarted();
            }
            return new Promise<Response>(() => undefined);
          }
          return jsonResponse(heldSnapshot());
        },
        calls,
      ),
    );
    const callers = Array.from({ length: 16 }, () => new AbortController());
    const reasons = callers.map(
      (_, index) => new DOMException(`caller ${index} left`, "AbortError"),
    );
    const pending = callers.map((caller) =>
      client.enterOfflineHold(1, caller.signal),
    );
    await allStarted;

    await expect(client.enterOfflineHold(1)).rejects.toThrow(
      /too many active mutation calls/u,
    );
    callers.forEach((caller, index) => caller.abort(reasons[index]));
    await Promise.all(
      pending.map((call, index) => expect(call).rejects.toBe(reasons[index])),
    );

    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());
    const mutationCalls = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/enter"),
    );
    expect(mutationCalls).toHaveLength(17);
    expect(
      new Set(
        mutationCalls.map((call) =>
          new Headers(call.init.headers).get("Idempotency-Key"),
        ),
      ).size,
    ).toBe(1);
  });

  it("aborts identity hashing on close and prioritizes closure over capacity", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          throw new Error("a hashing mutation must not dispatch");
        },
        calls,
      ),
    );
    let digests = 0;
    let markAllStarted: () => void = () => undefined;
    const allStarted = new Promise<void>((resolve) => {
      markAllStarted = resolve;
    });
    const digestSpy = vi
      .spyOn(globalThis.crypto.subtle, "digest")
      .mockImplementation(async () => {
        digests += 1;
        if (digests === 16) {
          markAllStarted();
        }
        return new Promise<ArrayBuffer>(() => undefined);
      });

    try {
      const pending = Array.from({ length: 16 }, () =>
        client.enterOfflineHold(1),
      );
      await allStarted;
      client.close();

      const settled = await Promise.allSettled(pending);
      expect(
        settled.every(
          (result) =>
            result.status === "rejected" && result.reason?.name === "AbortError",
        ),
      ).toBe(true);
      await expect(client.enterOfflineHold(1)).rejects.toMatchObject({
        name: "AbortError",
      });
      expect(
        calls.filter(({ url }) => url.pathname.endsWith("/actions/enter")),
      ).toHaveLength(0);
    } finally {
      client.close();
      digestSpy.mockRestore();
    }
  });

  it("recovers one timed-out rotation with the same idempotency command", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const renewalCalls: FetchCall[] = [];
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      if (url.pathname === sessionRenewalPath) {
        renewalCalls.push({ url, init });
        if (renewalCalls.length === 1) {
          markStarted();
          return new Promise<Response>(() => undefined);
        }
        return jsonResponse(sessionRotation(rotatedExpiry));
      }
      return jsonResponse(statusResponse());
    });
    const client = await createControlClient(
      bootstrap,
      fetchImplementation,
      () => currentTime,
    );
    currentTime += 1_100;
    vi.useFakeTimers();

    const pending = client.status();
    await started;
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);
    await pending;

    expect(renewalCalls).toHaveLength(2);
    for (const header of ["Authorization", "If-Match", "Idempotency-Key"]) {
      expect(new Headers(renewalCalls[0]?.init.headers).get(header)).toBe(
        new Headers(renewalCalls[1]?.init.headers).get(header),
      );
    }
    expect(vi.getTimerCount()).toBe(0);
  });

  it("rejects malformed current-session metadata during bootstrap", async () => {
    const bootstrap = session();
    const fetchImplementation = vi.fn(async () =>
      jsonResponse({
        ...sessionState(bootstrap.expiresAt),
        instanceId: bootstrap.instanceId,
      }),
    );

    await expect(
      createControlClient(bootstrap, fetchImplementation),
    ).rejects.toBeInstanceOf(ControlContractError);
  });

  it("never reads or writes Web Storage and never logs session material", async () => {
    const storageMethods = ["getItem", "setItem", "removeItem", "clear"] as const;
    const storageSpies = storageMethods.map((method) =>
      vi.spyOn(Storage.prototype, method),
    );
    const consoleMethods = ["debug", "info", "log", "warn", "error"] as const;
    const consoleSpies = consoleMethods.map((method) =>
      vi.spyOn(console, method).mockImplementation(() => {}),
    );
    const bootstrap = session();
    const fetchImplementation = withSessionState(
      bootstrap,
      () => jsonResponse(statusResponse()),
    );

    try {
      const client = await createControlClient(bootstrap, fetchImplementation);
      await client.status();
      expect(storageSpies.every((spy) => spy.mock.calls.length === 0)).toBe(true);
      expect(consoleSpies.every((spy) => spy.mock.calls.length === 0)).toBe(true);
    } finally {
      for (const spy of [...storageSpies, ...consoleSpies]) {
        spy.mockRestore();
      }
    }
  });

  it("constructs the complete bounded M0 aggregate without a secret value", async () => {
    const bootstrap = session();
    const fetchImplementation = withSessionState(bootstrap, () =>
      jsonResponse({
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "a".repeat(64),
      }),
    );
    const client = await createControlClient(bootstrap, fetchImplementation);
    const input = buildAccessApplyInput({
      ...initialAccessForm,
      accessId: "work",
      mode: "managed",
      fixedModel: "example-model",
      name: "Work",
      providerOrigin: "https://gateway.example/v1",
      routeName: "Primary route",
    });

    await client.applyAccess("work", input);

    const mutationCall = fetchImplementation.mock.calls.find(
      ([input]) => new URL(String(input)).pathname.includes("/actions/apply"),
    );
    const init = mutationCall?.[1] as RequestInit;
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(body).toEqual(input);
    expect(JSON.stringify(body)).toContain("secret://provider/work-account");
    expect(JSON.stringify(body)).not.toContain("provider-secret-value");
    expect(input.providerTargets[0]?.capabilities).toEqual([
      "messages",
      "streaming",
      "tool_calls",
    ]);
    expect(input.pluginPlan.bindingIds).toEqual([]);
  });

  it("preserves a known Access commit when its active projection is unavailable", async () => {
    const bootstrap = session();
    const fetchImplementation = withSessionState(bootstrap, () =>
      jsonResponse({
        outcome: "committed",
        revision: 1,
        applicationState: "unavailable",
      }),
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toEqual({
      outcome: "committed",
      revision: 1,
      applicationState: "unavailable",
    });
  });

  it("adds and selects one provider candidate through the incremental mutation paths", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      (url) =>
        url.pathname.endsWith("/actions/add-candidate")
          ? jsonResponse(
              {
                outcome: "committed",
                revision: 5,
                applicationState: "active",
                planHash: "a".repeat(64),
                candidate: {
                  profileId: "work-relay-profile",
                  credentialId: "work-relay-account",
                },
              },
              201,
            )
          : jsonResponse({
              outcome: "committed",
              revision: 6,
              applicationState: "active",
              planHash: "b".repeat(64),
            }),
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);
    const candidate = {
      name: "Relay A",
      provider: "anthropic-compatible" as const,
      baseUrl: "https://relay.example/v1",
      model: "claude-sonnet-4-5",
      authDriverRef: "static_header" as const,
      upstreamPresentation: "follow-client" as const,
    };

    await expect(
      client.addAccessCandidate("work", 4, candidate),
    ).resolves.toMatchObject({
      revision: 5,
      candidate: {
        profileId: "work-relay-profile",
        credentialId: "work-relay-account",
      },
    });
    await expect(
      client.selectAccessCandidate("work", "work-relay-profile", 5),
    ).resolves.toMatchObject({ revision: 6 });

    const addCall = calls.find(({ url }) =>
      url.pathname.endsWith("/api/v1/accesses/work/actions/add-candidate"),
    );
    expect(addCall?.init.method).toBe("POST");
    expect(JSON.parse(String(addCall?.init.body))).toEqual(candidate);
    expect(new Headers(addCall?.init.headers).get("If-Match")).toBe("4");
    expect(
      new Headers(addCall?.init.headers).get("Idempotency-Key"),
    ).toBeTruthy();

    const selectCall = calls.find(({ url }) =>
      url.pathname.endsWith(
        "/api/v1/accesses/work/profiles/work-relay-profile/actions/select-candidate",
      ),
    );
    expect(selectCall?.init.method).toBe("POST");
    expect(selectCall?.init.body).toBeUndefined();
    expect(new Headers(selectCall?.init.headers).get("If-Match")).toBe("5");
    expect(
      new Headers(selectCall?.init.headers).get("Idempotency-Key"),
    ).toBeTruthy();
  });

  it("replays a lost Access receipt with the exact same command", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        if (applies === 1) {
          throw new TypeError("response was lost");
        }
        return jsonResponse({
          outcome: "committed",
          revision: 1,
          applicationState: "active",
          planHash: "a".repeat(64),
        });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    for (const header of ["Authorization", "If-Match", "Idempotency-Key"]) {
      expect(new Headers(mutations[0]?.init.headers).get(header)).toBe(
        new Headers(mutations[1]?.init.headers).get(header),
      );
    }
  });

  it("captures an Access revision before any asynchronous replay boundary", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const input = accessApplyInput();
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        if (applies === 1) {
          (input as { expectedRevision: number }).expectedRevision = 7;
          throw new TypeError("response was lost after caller mutation");
        }
        return jsonResponse({
          outcome: "committed",
          revision: 1,
          applicationState: "active",
          planHash: "a".repeat(64),
        });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(client.applyAccess("work", input)).resolves.toMatchObject({
      outcome: "committed",
      revision: 1,
    });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    expect(String(mutations[0]?.init.body)).toContain(
      '"expectedRevision":0',
    );
    for (const mutation of mutations) {
      expect(new Headers(mutation.init.headers).get("If-Match")).toBe("0");
    }
  });

  it("replays an invalid Access success receipt before accepting the commit", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        return jsonResponse(
          applies === 1
            ? {
                outcome: "committed",
                revision: 1,
                applicationState: "active",
                planHash: "a".repeat(64),
                leakedField: "must-not-be-accepted",
              }
            : {
                outcome: "committed",
                revision: 1,
                applicationState: "active",
                planHash: "a".repeat(64),
              },
        );
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it.each([
    [
      "unexpected success status",
      () =>
        jsonResponse(
          {
            outcome: "committed",
            revision: 1,
            applicationState: "active",
            planHash: "a".repeat(64),
          },
          201,
        ),
    ],
    [
      "non-exact success media type",
      () =>
        new Response(
          JSON.stringify({
            outcome: "committed",
            revision: 1,
            applicationState: "active",
            planHash: "a".repeat(64),
          }),
          { headers: { "Content-Type": "application/json-evil" } },
        ),
    ],
  ])("replays an Access receipt with %s", async (_name, firstResponse) => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        return applies === 1
          ? firstResponse()
          : jsonResponse({
              outcome: "committed",
              revision: 1,
              applicationState: "active",
              planHash: "a".repeat(64),
            });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("replays a non-closed Access problem before accepting it as authoritative", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        if (applies === 1) {
          return new Response(
            JSON.stringify({
              type: "urn:vibermate:error:revision-conflict",
              title: "Conflict",
              status: 409,
              code: "revision_conflict",
              extra: "not-closed",
            }),
            {
              status: 409,
              headers: { "Content-Type": "application/problem+json" },
            },
          );
        }
        return problemResponse(409, "revision_conflict");
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).rejects.toEqual(
      expect.objectContaining<Partial<ControlProblem>>({
        status: 409,
        reasonCode: "revision_conflict",
      }),
    );

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("retains a timed-out Access command for an explicit same-key retry", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        if (applies === 1) {
          markStarted();
          return new Promise<Response>(() => undefined);
        }
        return jsonResponse({
          outcome: "committed",
          revision: 1,
          applicationState: "active",
          planHash: "a".repeat(64),
        });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);
    vi.useFakeTimers();

    const first = client.applyAccess("work", accessApplyInput());
    await started;
    const rejected = expect(first).rejects.toEqual(
      expect.objectContaining({ name: "TimeoutError" }),
    );
    await vi.advanceTimersByTimeAsync(requestTimeoutMilliseconds);
    await rejected;
    expect(applies).toBe(1);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
    expect(vi.getTimerCount()).toBe(0);
  });

  it("retains a caller-aborted Access command without a hidden replay", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        if (applies === 1) {
          markStarted();
          return new Promise<Response>(() => undefined);
        }
        return jsonResponse({
          outcome: "committed",
          revision: 1,
          applicationState: "active",
          planHash: "a".repeat(64),
        });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);
    const caller = new AbortController();
    const reason = new DOMException("route disposed", "AbortError");

    const first = client.applyAccess("work", accessApplyInput(), caller.signal);
    await started;
    caller.abort(reason);

    await expect(first).rejects.toBe(reason);
    expect(applies).toBe(1);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("does not replay Access after the client owner closes", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let markStarted: () => void = () => undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        markStarted();
        return new Promise<Response>(() => undefined);
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    const pending = client.applyAccess("work", accessApplyInput());
    await started;
    client.close();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(
      calls.filter(({ url }) => url.pathname.endsWith("/actions/apply")),
    ).toHaveLength(1);
  });

  it("does not allocate Access commands for calls aborted before dispatch", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          applies += 1;
          return jsonResponse({
            outcome: "committed",
            revision: 1,
            applicationState: "active",
            planHash: "a".repeat(64),
          });
        },
        calls,
      ),
    );

    for (let index = 0; index < 16; index += 1) {
      const caller = new AbortController();
      const reason = new DOMException(`caller ${index} left`, "AbortError");
      const pending = client.applyAccess(
        `work-${index}`,
        accessApplyInput(),
        caller.signal,
      );
      caller.abort(reason);
      await expect(pending).rejects.toBe(reason);
    }
    expect(applies).toBe(0);
    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });
    expect(applies).toBe(1);
  });

  it("bounds Access registrations while a shared renewal is blocked", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const bootstrap = session(initialExpiry);
    const calls: FetchCall[] = [];
    let markRenewalStarted = (): void => undefined;
    const renewalStarted = new Promise<void>((resolve) => {
      markRenewalStarted = resolve;
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        (url) => {
          if (url.pathname === sessionRenewalPath) {
            markRenewalStarted();
            return new Promise<Response>(() => undefined);
          }
          throw new Error("Access must not dispatch before renewal completes");
        },
        calls,
      ),
      () => currentTime,
    );
    currentTime += 1_100;

    const pending = Array.from({ length: 16 }, (_, index) =>
      client.applyAccess(`work-${index}`, accessApplyInput()),
    );
    await renewalStarted;
    await expect(
      client.applyAccess("work-over-capacity", accessApplyInput()),
    ).rejects.toThrow(/too many active mutation calls/u);
    expect(
      calls.filter(({ url }) => url.pathname.endsWith("/actions/apply")),
    ).toHaveLength(0);

    client.close();
    const settled = await Promise.allSettled(pending);
    expect(
      settled.every(
        (result) =>
          result.status === "rejected" && result.reason?.name === "AbortError",
      ),
    ).toBe(true);
  });

  it("keeps one Access authority across session renewal", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const calls: FetchCall[] = [];
    let applies = 0;
    let releaseRenewal = (): void => undefined;
    const renewalGate = new Promise<void>((resolve) => {
      releaseRenewal = resolve;
    });
    let markRenewalStarted = (): void => undefined;
    const renewalStarted = new Promise<void>((resolve) => {
      markRenewalStarted = resolve;
    });
    let resolveFirstApply: (response: Response) => void = () => undefined;
    const firstApplyResponse = new Promise<Response>((resolve) => {
      resolveFirstApply = resolve;
    });
    let markFirstApplyStarted = (): void => undefined;
    const firstApplyStarted = new Promise<void>((resolve) => {
      markFirstApplyStarted = resolve;
    });
    let resolveSecondApply: (response: Response) => void = () => undefined;
    const secondApplyResponse = new Promise<Response>((resolve) => {
      resolveSecondApply = resolve;
    });
    let markSecondApplyStarted = (): void => undefined;
    const secondApplyStarted = new Promise<void>((resolve) => {
      markSecondApplyStarted = resolve;
    });
    const committed = () =>
      jsonResponse({
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "a".repeat(64),
      });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        async (url) => {
          if (url.pathname === sessionRenewalPath) {
            markRenewalStarted();
            await renewalGate;
            return jsonResponse(sessionRotation(rotatedExpiry));
          }
          applies += 1;
          if (applies === 1) {
            markFirstApplyStarted();
            return firstApplyResponse;
          }
          markSecondApplyStarted();
          return secondApplyResponse;
        },
        calls,
      ),
      () => currentTime,
    );

    const first = client.applyAccess("work", accessApplyInput());
    await firstApplyStarted;
    currentTime += 1_100;
    const overlapping = client.applyAccess("work", accessApplyInput());
    await renewalStarted;
    releaseRenewal();
    await secondApplyStarted;

    resolveFirstApply(problemResponse(401, "control_unauthorized"));
    resolveSecondApply(committed());
    await expect(first).resolves.toMatchObject({
      outcome: "committed",
      revision: 1,
    });
    await expect(overlapping).resolves.toMatchObject({
      outcome: "committed",
      revision: 1,
    });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
    expect(new Headers(mutations[0]?.init.headers).get("Authorization")).toBe(
      `Bearer ${bootstrap.writeToken}`,
    );
    expect(new Headers(mutations[1]?.init.headers).get("Authorization")).toBe(
      `Bearer ${capability(0x44)}`,
    );
  });

  it("retains an ambiguous Access key after a stale-token Problem", async () => {
    let currentTime = fixedNow;
    const initialExpiry = new Date(fixedNow + 2_000).toISOString();
    const rotatedExpiry = new Date(fixedNow + 20_000).toISOString();
    const bootstrap = session(initialExpiry);
    const newWriteToken = capability(0x4a);
    const calls: FetchCall[] = [];
    let applies = 0;
    const committed = () =>
      jsonResponse({
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "a".repeat(64),
      });
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        (url, init) => {
          if (url.pathname === sessionRenewalPath) {
            return jsonResponse(
              sessionRotation(
                rotatedExpiry,
                2,
                capability(0x49),
                newWriteToken,
              ),
            );
          }
          if (url.pathname === "/api/v1/status") {
            return jsonResponse(statusResponse());
          }
          applies += 1;
          if (applies <= 2) {
            throw new TypeError("Access commit receipt was lost");
          }
          const authorization = new Headers(init.headers).get("Authorization");
          return authorization === `Bearer ${bootstrap.writeToken}`
            ? problemResponse(401, "control_unauthorized")
            : committed();
        },
        calls,
      ),
      () => currentTime,
    );
    const originalDigest = globalThis.crypto.subtle.digest.bind(
      globalThis.crypto.subtle,
    );
    let digests = 0;
    let releaseSecondDigest = (): void => undefined;
    const secondDigestGate = new Promise<void>((resolve) => {
      releaseSecondDigest = resolve;
    });
    let markSecondDigestStarted = (): void => undefined;
    const secondDigestStarted = new Promise<void>((resolve) => {
      markSecondDigestStarted = resolve;
    });
    const digestSpy = vi
      .spyOn(globalThis.crypto.subtle, "digest")
      .mockImplementation(async (algorithm, data) => {
        digests += 1;
        if (digests === 2) {
          markSecondDigestStarted();
          await secondDigestGate;
        }
        return originalDigest(algorithm, data);
      });

    try {
      await expect(
        client.applyAccess("work", accessApplyInput()),
      ).rejects.toThrow(/commit receipt was lost/u);

      const staleRetry = client.applyAccess("work", accessApplyInput());
      await secondDigestStarted;
      currentTime += 1_100;
      await client.status();
      releaseSecondDigest();
      await expect(staleRetry).rejects.toMatchObject({
        status: 401,
        reasonCode: "control_unauthorized",
      });

      await expect(
        client.applyAccess("work", accessApplyInput()),
      ).resolves.toMatchObject({ outcome: "committed", revision: 1 });
    } finally {
      releaseSecondDigest();
      digestSpy.mockRestore();
    }

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(4);
    expect(
      new Set(
        mutations.map((call) =>
          new Headers(call.init.headers).get("Idempotency-Key"),
        ),
      ).size,
    ).toBe(1);
    expect(new Headers(mutations[2]?.init.headers).get("Authorization")).toBe(
      `Bearer ${bootstrap.writeToken}`,
    );
    expect(new Headers(mutations[3]?.init.headers).get("Authorization")).toBe(
      `Bearer ${newWriteToken}`,
    );
  });

  it("releases an Access key after an authoritative problem", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let applies = 0;
    const fetchImplementation = withSessionState(
      bootstrap,
      () => {
        applies += 1;
        return applies === 1
          ? problemResponse(409, "revision_conflict")
          : jsonResponse({
              outcome: "committed",
              revision: 1,
              applicationState: "active",
              planHash: "a".repeat(64),
            });
      },
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).rejects.toBeInstanceOf(ControlProblem);
    await expect(
      client.applyAccess("work", accessApplyInput()),
    ).resolves.toMatchObject({ outcome: "committed", revision: 1 });

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/apply"),
    );
    expect(mutations).toHaveLength(2);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).not.toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("writes a credential only through the scoped write-only action", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      () =>
        jsonResponse({
          credentialId: "work-account",
          profileId: "work-openai",
          secretState: "configured",
          secretRevision: 1,
        }),
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    await client.replaceCredentialSecret(
      "work",
      "work-openai",
      "work-account",
      0,
      "provider-secret-value",
    );

    const request = calls.find((call) => call.url.pathname.endsWith("replace-secret"));
    expect(request?.url.pathname).toBe(
      "/api/v1/accesses/work/profiles/work-openai/credentials/work-account/actions/replace-secret",
    );
    const headers = new Headers(request?.init.headers);
    expect(headers.get("If-Match")).toBe("0");
    expect(headers.get("Authorization")).toBe(`Bearer ${bootstrap.writeToken}`);
    expect(JSON.parse(String(request?.init.body))).toEqual({
      secret: "provider-secret-value",
    });
  });

  it("replays a stale credential replacement receipt with the same opaque key", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    let replacements = 0;
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          replacements += 1;
          return jsonResponse(
            credentialView({ secretRevision: replacements === 1 ? 1 : 2 }),
          );
        },
        calls,
      ),
    );

    await expect(
      client.replaceCredentialSecret(
        "work",
        "work-openai",
        "work-account",
        1,
        "provider-secret-value",
      ),
    ).resolves.toEqual(credentialView({ secretRevision: 2 }));

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/replace-secret"),
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[0]?.init.body).toBe(mutations[1]?.init.body);
    const firstKey = new Headers(mutations[0]?.init.headers).get(
      "Idempotency-Key",
    );
    expect(firstKey).toMatch(/^[A-Za-z0-9_-]{16,128}$/u);
    expect(firstKey).not.toContain("provider-secret-value");
    expect(firstKey).toBe(
      new Headers(mutations[1]?.init.headers).get("Idempotency-Key"),
    );
  });

  it("rejects an overflowing credential revision before sending its secret", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => jsonResponse(credentialView({ secretRevision: 1 })),
        calls,
      ),
    );

    await expect(
      client.replaceCredentialSecret(
        "work",
        "work-openai",
        "work-account",
        Number.MAX_SAFE_INTEGER,
        "must-not-be-sent",
      ),
    ).rejects.toBeInstanceOf(ControlContractError);
    expect(
      calls.some(({ url }) => url.pathname.endsWith("/actions/replace-secret")),
    ).toBe(false);
  });

  it("loads an empty Access directory with the read capability", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => jsonResponse({ items: [] }),
        calls,
      ),
    );

    await expect(client.accesses()).resolves.toEqual({ items: [] });

    const request = calls.find(
      ({ url }) => url.pathname === "/api/v1/accesses",
    );
    expect(request?.url.href).toBe(`${bootstrap.baseUrl}/api/v1/accesses`);
    expect(request?.init.method).toBe("GET");
    expect(request?.init.body).toBeUndefined();
    const headers = new Headers(request?.init.headers);
    expect(headers.get("Authorization")).toBe(`Bearer ${bootstrap.readToken}`);
    expect(headers.has("If-Match")).toBe(false);
    expect(headers.has("Idempotency-Key")).toBe(false);
  });

  it("loads multiple Access directory entries in canonical accessId order", async () => {
    const bootstrap = session();
    const items = [
      accessDirectoryItem(),
      accessDirectoryItem({
        accessId: "personal",
        name: "Personal",
        description: "",
        status: "draft",
        revision: 2,
        clientOrigin: "https://api.openai.com",
        clientDialect: "openai-responses",
      }),
      accessDirectoryItem({
        accessId: "work",
        name: "Work",
        description: "Team Access",
        status: "disabled",
        revision: 3,
        clientOrigin: "https://work.example.com",
        clientDialect: "openai-chat",
      }),
    ];
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse({ items })),
    );

    await expect(client.accesses()).resolves.toEqual({ items });
  });

  it("uses the server's UTF-8 identifier order instead of locale collation", async () => {
    const bootstrap = session();
    const items = [
      accessDirectoryItem({ accessId: "\ue000", name: "Private use" }),
      accessDirectoryItem({ accessId: "😀", name: "Emoji" }),
    ];
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse({ items })),
    );

    await expect(client.accesses()).resolves.toEqual({ items });
  });

  it.each<{ name: string; payload: unknown }>([
    {
      name: "duplicate accessId entries",
      payload: {
        items: [accessDirectoryItem(), accessDirectoryItem()],
      },
    },
    {
      name: "entries outside canonical accessId order",
      payload: {
        items: [
          accessDirectoryItem({ accessId: "work" }),
          accessDirectoryItem({ accessId: "personal" }),
        ],
      },
    },
    {
      name: "an unknown page field",
      payload: { items: [], nextCursor: "invented" },
    },
    {
      name: "an unknown item field",
      payload: {
        items: [accessDirectoryItem({ credentialValue: "must-not-cross" })],
      },
    },
    {
      name: "an invalid accessId",
      payload: {
        items: [accessDirectoryItem({ accessId: " alpha" })],
      },
    },
    {
      name: "a whitespace-only name",
      payload: { items: [accessDirectoryItem({ name: " " })] },
    },
    {
      name: "an unknown status",
      payload: { items: [accessDirectoryItem({ status: "archived" })] },
    },
    {
      name: "a zero revision",
      payload: { items: [accessDirectoryItem({ revision: 0 })] },
    },
    {
      name: "an unsafe revision",
      payload: {
        items: [
          accessDirectoryItem({ revision: Number.MAX_SAFE_INTEGER + 1 }),
        ],
      },
    },
    {
      name: "a client origin containing a path",
      payload: {
        items: [
          accessDirectoryItem({ clientOrigin: "https://api.example.com/v1" }),
        ],
      },
    },
    {
      name: "a cleartext client origin",
      payload: {
        items: [
          accessDirectoryItem({ clientOrigin: "http://127.0.0.1:23333" }),
        ],
      },
    },
    {
      name: "an unknown client dialect",
      payload: {
        items: [accessDirectoryItem({ clientDialect: "unknown-chat" })],
      },
    },
    {
      name: "more than 1,024 entries",
      payload: {
        items: Array.from({ length: 1_025 }, (_, index) =>
          accessDirectoryItem({
            accessId: `access-${String(index).padStart(4, "0")}`,
          }),
        ),
      },
    },
  ])("rejects an Access directory with $name", async ({ payload }) => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url, init) => {
        expect(url.pathname).toBe("/api/v1/accesses");
        expect(url.search).toBe("");
        expect(init.method).toBe("GET");
        expect(new Headers(init.headers).get("Authorization")).toBe(
          `Bearer ${bootstrap.readToken}`,
        );
        return jsonResponse(payload);
      }),
    );

    await expect(client.accesses()).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it("loads a secret-free Access detail through the encoded read path", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const accessId = "team alpha/primary";
    const payload = accessDetail(accessId);
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(payload), calls),
    );

    await expect(client.access(accessId)).resolves.toEqual(payload);

    const request = calls.find(
      ({ url }) => url.pathname !== sessionStatePath,
    );
    expect(request?.url.pathname).toBe(
      "/api/v1/accesses/team%20alpha%2Fprimary",
    );
    expect(request?.url.search).toBe("");
    expect(request?.init.method).toBe("GET");
    expect(request?.init.body).toBeUndefined();
    const headers = new Headers(request?.init.headers);
    expect(headers.get("Authorization")).toBe(`Bearer ${bootstrap.readToken}`);
    expect(headers.has("If-Match")).toBe(false);
    expect(headers.has("Idempotency-Key")).toBe(false);

    const bindings = (payload.accountBindings as Record<string, unknown>[]);
    expect(Object.keys(bindings[0]!).sort()).toEqual([
      "authDriverRef",
      "enabled",
      "id",
      "label",
      "profileId",
      "secretHandling",
    ]);
    expect(JSON.stringify(payload)).not.toContain("secret://");
  });

  it("loads the exact current-login profile without an account binding", async () => {
    const bootstrap = session();
    const payload = originalAccessDetail();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(payload)),
    );

    await expect(client.access("work")).resolves.toEqual(payload);
    expect(payload.accountBindings).toEqual([]);
  });

  it("rejects a current-login profile that asks for managed credentials", async () => {
    const bootstrap = session();
    const payload = originalAccessDetail();
    const profiles = payload.profiles as Record<string, unknown>[];
    profiles[0]!.credentialSource = "managed_account";
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(payload)),
    );

    await expect(client.access("work")).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it.each(["draft", "disabled"] as const)(
    "loads a durable %s Access even though it has no active plan",
    async (status) => {
      const bootstrap = session();
      const payload = accessDetail("work", status);
      const client = await createControlClient(
        bootstrap,
        withSessionState(bootstrap, () => jsonResponse(payload)),
      );

      await expect(client.access("work")).resolves.toEqual(payload);
    },
  );

  it("rejects an invalid Access path identity before dispatch", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => {
          throw new Error("an invalid Access ID must not dispatch");
        },
        calls,
      ),
    );

    await expect(client.access(" work")).rejects.toBeInstanceOf(
      ControlContractError,
    );
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.pathname).toBe(sessionStatePath);
  });

  it.each<{
    name: string;
    mutate: (payload: Record<string, unknown>) => void;
  }>([
    {
      name: "an unknown top-level field",
      mutate: (payload) => {
        payload.unexpected = true;
      },
    },
    {
      name: "a path/body identity mismatch",
      mutate: (payload) => {
        (payload.access as Record<string, unknown>).id = "personal";
      },
    },
    {
      name: "a credential secret value",
      mutate: (payload) => {
        const bindings = payload.accountBindings as Record<string, unknown>[];
        bindings[0]!.secret = "must-not-cross";
      },
    },
    {
      name: "an internal credential SecretRef",
      mutate: (payload) => {
        const bindings = payload.accountBindings as Record<string, unknown>[];
        bindings[0]!.secretRef = "secret://provider/work-account";
      },
    },
    {
      name: "an unsafe revision",
      mutate: (payload) => {
        payload.revision = Number.MAX_SAFE_INTEGER + 1;
      },
    },
    {
      name: "an overlong Access name",
      mutate: (payload) => {
        (payload.access as Record<string, unknown>).name = "a".repeat(257);
      },
    },
    {
      name: "a cleartext client origin",
      mutate: (payload) => {
        (payload.agentEndpoint as Record<string, unknown>).clientOrigin =
          "http://127.0.0.1:23333";
      },
    },
    {
      name: "a non-loopback cleartext provider origin",
      mutate: (payload) => {
        const targets = payload.providerTargets as Record<string, unknown>[];
        targets[0]!.origin = "http://provider.example.com/v1";
      },
    },
    {
      name: "an unknown provider capability",
      mutate: (payload) => {
        const targets = payload.providerTargets as Record<string, unknown>[];
        targets[0]!.capabilities = ["messages", "telepathy"];
      },
    },
    {
      name: "a model policy carrying fields from another mode",
      mutate: (payload) => {
        const profiles = payload.profiles as Record<string, unknown>[];
        profiles[0]!.defaultModelPolicy = {
          mode: "fixed",
          fixedModel: "dashscope:glm-5",
          mappingRef: "mapping-1",
        };
      },
    },
    {
      name: "a mismatched AgentEndpoint relationship",
      mutate: (payload) => {
        (payload.agentEndpoint as Record<string, unknown>).id = "other-agent";
      },
    },
    {
      name: "a dangling account/profile relationship",
      mutate: (payload) => {
        const bindings = payload.accountBindings as Record<string, unknown>[];
        bindings[0]!.profileId = "missing-profile";
      },
    },
    {
      name: "more than 64 route sets",
      mutate: (payload) => {
        payload.routeSets = Array.from({ length: 65 }, (_, index) => ({
          id: `routes-${index}`,
          candidateProfileIds: ["work-openai"],
          fallback: "disabled",
        }));
      },
    },
    {
      name: "an unknown egress mode",
      mutate: (payload) => {
        (payload.egressPolicy as Record<string, unknown>).mode = "proxy";
      },
    },
    {
      name: "an unknown plugin-plan mode",
      mutate: (payload) => {
        (payload.pluginPlan as Record<string, unknown>).mode = "execute";
      },
    },
  ])("rejects an Access detail with $name", async ({ mutate }) => {
    const bootstrap = session();
    const payload = accessDetail();
    mutate(payload);
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url, init) => {
        expect(url.pathname).toBe("/api/v1/accesses/work");
        expect(init.method).toBe("GET");
        expect(new Headers(init.headers).get("Authorization")).toBe(
          `Bearer ${bootstrap.readToken}`,
        );
        return jsonResponse(payload);
      }),
    );

    await expect(client.access("work")).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it("loads active plan metadata through the read capability", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const fetchImplementation = withSessionState(
      bootstrap,
      () =>
        jsonResponse({
          accessId: "work",
          revision: 4,
          planHash: "a".repeat(64),
          profiles: ["original-passthrough", "work-openai"],
          accountBindings: [{ id: "work-account", profileId: "work-openai" }],
        }),
      calls,
    );
    const client = await createControlClient(bootstrap, fetchImplementation);

    const plan = await client.accessPlan("work");

    expect(plan.revision).toBe(4);
    const request = calls.find((call) => call.url.pathname.endsWith("/plan"));
    const headers = new Headers(request?.init.headers);
    expect(headers.get("Authorization")).toBe(`Bearer ${bootstrap.readToken}`);
    expect(headers.has("If-Match")).toBe(false);
  });

  it("accepts the one uncredentialed Core original route", async () => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () =>
        jsonResponse({
          accessId: "work",
          revision: 4,
          planHash: "a".repeat(64),
          profiles: ["original-passthrough"],
          accountBindings: [],
        }),
      ),
    );

    await expect(client.accessPlan("work")).resolves.toEqual({
      accessId: "work",
      revision: 4,
      planHash: "a".repeat(64),
      profiles: ["original-passthrough"],
      accountBindings: [],
    });
  });

  it("rejects ambient authorities and preserves stable problem codes", async () => {
    await expect(
      createControlClient({
        ...session(),
        baseUrl: "http://localhost:43127",
      }),
    ).rejects.toThrow(/literal IPv4 loopback/u);

    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () =>
        problemResponse(409, "revision_conflict"),
      ),
    );
    await expect(client.enterOfflineHold(2)).rejects.toEqual(
      expect.objectContaining<Partial<ControlProblem>>({
        status: 409,
        reasonCode: "revision_conflict",
        messageKey: "error.revision_conflict",
      }),
    );
  });

  it("rejects legacy or expanded problem bodies", async () => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => {
        return new Response(
          JSON.stringify({
            type: "urn:vibermate:error:revision-conflict",
            title: "Conflict",
            status: 409,
            code: "revision_conflict",
            messageKey: "error.revision_conflict",
          }),
          {
            status: 409,
            headers: { "Content-Type": "application/problem+json" },
          },
        );
      }),
    );

    await expect(client.enterOfflineHold(2)).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it("accepts every closed singleton success response and reuses mutation validators", async () => {
    const bootstrap = session();
    const applyInput = accessApplyInput();
    const plan = {
      accessId: "work",
      revision: 1,
      planHash: "a".repeat(64),
      profiles: ["original-passthrough", "work-openai"],
      accountBindings: [{ id: "work-account", profileId: "work-openai" }],
    };
    const fetchImplementation = withSessionState(bootstrap, (url, init) => {
      switch (url.pathname) {
        case "/api/v1/status":
          return jsonResponse(statusResponse());
        case "/api/v1/offline-hold":
          return jsonResponse(heldSnapshot());
        case "/api/v1/offline-hold/actions/enter":
          return jsonResponse(heldSnapshot());
        case "/api/v1/offline-hold/actions/resume":
          return jsonResponse(onlineSnapshot());
        case "/api/v1/accesses/work/actions/apply":
          return jsonResponse({
            outcome: "committed",
            revision: applyInput.expectedRevision + 1,
            applicationState: "active",
            planHash: "a".repeat(64),
          });
        case "/api/v1/accesses/work/plan":
          return jsonResponse(plan);
        case "/api/v1/accesses/work/profiles/work-openai/credentials/work-account":
          return jsonResponse({
            credentialId: "work-account",
            profileId: "work-openai",
            secretState: "unavailable",
            secretRevision: 0,
          });
        case "/api/v1/accesses/work/profiles/work-openai/credentials/work-account/actions/replace-secret":
          expect(init.method).toBe("POST");
          return jsonResponse({
            credentialId: "work-account",
            profileId: "work-openai",
            secretState: "configured",
            secretRevision: 1,
          });
        default:
          return jsonResponse(statusResponse());
      }
    });
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(client.status()).resolves.toEqual(statusResponse());
    await expect(client.offlineHold()).resolves.toEqual(heldSnapshot());
    await expect(client.enterOfflineHold(1)).resolves.toEqual(heldSnapshot());
    await expect(client.resumeOfflineHold(2)).resolves.toEqual(onlineSnapshot());
    await expect(client.applyAccess("work", applyInput)).resolves.toEqual({
      outcome: "committed",
      revision: applyInput.expectedRevision + 1,
      applicationState: "active",
      planHash: "a".repeat(64),
    });
    await expect(client.accessPlan("work")).resolves.toEqual(plan);
    await expect(
      client.credential("work", "work-openai", "work-account"),
    ).resolves.toEqual({
      credentialId: "work-account",
      profileId: "work-openai",
      secretState: "unavailable",
      secretRevision: 0,
    });
    await expect(
      client.replaceCredentialSecret(
        "work",
        "work-openai",
        "work-account",
        0,
        "secret",
      ),
    ).resolves.toEqual({
      credentialId: "work-account",
      profileId: "work-openai",
      secretState: "configured",
      secretRevision: 1,
    });
  });

  it("accepts the closed activity, approval, connection, egress, and capture wire shapes", async () => {
    const bootstrap = session();
    const pendingApproval = approvalView();
    const resolvedApproval = decidedApproval();
    const fetchImplementation = withSessionState(bootstrap, (url) => {
      switch (url.pathname) {
        case "/api/v1/activities":
          expect(url.searchParams.get("limit")).toBe("50");
          expect(url.searchParams.get("cursor")).toBe(
            "cHJldmlldy1wYWdlLTI",
          );
          return jsonResponse({
            items: [activityRecord({ status: "reviewed" })],
            nextCursor: "b3BhcXVlLW5leHQ",
          });
        case "/api/v1/exchanges/exchange-1":
          expect(url.search).toBe("");
          return jsonResponse(exchangeDetail());
        case "/api/v1/approvals":
          if (url.pathname.endsWith("/actions/decide")) {
            return jsonResponse(resolvedApproval);
          }
          return jsonResponse({ items: [pendingApproval] });
        case "/api/v1/connections":
          return jsonResponse({
            items: [connectionRecord(), askedConnectionRecord()],
          });
        case "/api/v1/egress-attempts":
          return jsonResponse({ items: [egressAttemptRecord()] });
        case "/api/v1/capture-runs":
          return jsonResponse({ items: [captureRunRecord()] });
        default:
          if (url.pathname.endsWith("/actions/decide")) {
            return jsonResponse(resolvedApproval);
          }
          return jsonResponse(statusResponse());
      }
    });
    const client = await createControlClient(bootstrap, fetchImplementation);

    await expect(
      client.activities("cHJldmlldy1wYWdlLTI"),
    ).resolves.toEqual({
      items: [activityRecord({ status: "reviewed" })],
      nextCursor: "b3BhcXVlLW5leHQ",
    });
    await expect(client.exchange("exchange-1")).resolves.toEqual(
      exchangeDetail(),
    );
    await expect(client.approvals()).resolves.toEqual({ items: [pendingApproval] });
    await expect(client.connections()).resolves.toEqual({
      items: [connectionRecord(), askedConnectionRecord()],
    });
    await expect(client.egressAttempts()).resolves.toEqual({
      items: [egressAttemptRecord()],
    });
    await expect(client.captureRuns()).resolves.toEqual({
      items: [captureRunRecord()],
    });
    await expect(
      client.decideApproval(pendingApproval, pendingApproval.choices[0]!),
    ).resolves.toEqual(resolvedApproval);
  });

  it.each([
    ["identity", decidedApproval({ id: "approval-other" })],
    ["revision", decidedApproval({ revision: 1 })],
    ["state", approvalView()],
    [
      "decision",
      decidedApproval({
        state: "denied",
        decision: "deny",
        terminalReason: "user_denied",
      }),
    ],
    ["scope", decidedApproval({ decisionScope: "host_port" })],
  ])("rejects an approval receipt with mismatched %s", async (_name, payload) => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const pending = approvalView();
    const client = await createControlClient(
      bootstrap,
      withSessionState(
        bootstrap,
        () => jsonResponse(payload),
        calls,
      ),
    );

    await expect(
      client.decideApproval(pending, pending.choices[0]!),
    ).rejects.toBeInstanceOf(ControlContractError);

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/decide"),
    );
    expect(mutations).toHaveLength(2);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("accepts an approval receipt with its complete authorization context unchanged", async () => {
    const bootstrap = session();
    const binding = {
      exchangeId: "exchange-1",
      accessId: "work",
      planRevision: 7,
      planHash: "a".repeat(64),
      requestCount: 2,
      waiterCount: 1,
    } satisfies Partial<ApprovalView>;
    const pending = approvalView(binding);
    const resolved = decidedApproval(binding);
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(resolved)),
    );

    await expect(
      client.decideApproval(pending, pending.choices[0]!),
    ).resolves.toEqual(resolved);
  });

  it.each<{
    readonly name: string;
    readonly overrides: Partial<ApprovalView>;
  }>([
    {
      name: "aggregate identity",
      overrides: { aggregateKey: "network:other.example.com:443" },
    },
    { name: "exchange binding", overrides: { exchangeId: "exchange-2" } },
    { name: "Access binding", overrides: { accessId: "personal" } },
    { name: "plan revision", overrides: { planRevision: 8 } },
    { name: "plan hash", overrides: { planHash: "b".repeat(64) } },
    {
      name: "target",
      overrides: { target: { host: "other.example.com", port: 443 } },
    },
    { name: "subject references", overrides: { subjectRefs: ["connection-2"] } },
    {
      name: "subject labels",
      overrides: { subjectLabels: ["other.example.com:443"] },
    },
    { name: "risk presentation", overrides: { risk: "high" } },
    {
      name: "title presentation",
      overrides: { titleKey: "approval.networkAsk.changedTitle" },
    },
    {
      name: "summary presentation",
      overrides: { summaryKey: "approval.networkAsk.changedSummary" },
    },
    {
      name: "choices",
      overrides: {
        choices: approvalView().choices.map((choice, index) =>
          index === 0
            ? { ...choice, labelKey: "approval.networkAsk.choice.changed" }
            : choice,
        ),
      },
    },
    { name: "request count", overrides: { requestCount: 3 } },
    { name: "waiter count", overrides: { waiterCount: 2 } },
    {
      name: "creation time",
      overrides: { createdAt: "2026-08-03T07:59:00Z" },
    },
    {
      name: "expiry time",
      overrides: { expiresAt: "2026-08-03T08:06:00Z" },
    },
  ])("rejects an approval receipt that replaces its $name", async ({ overrides }) => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const binding = {
      exchangeId: "exchange-1",
      accessId: "work",
      planRevision: 7,
      planHash: "a".repeat(64),
      requestCount: 2,
      waiterCount: 1,
    } satisfies Partial<ApprovalView>;
    const pending = approvalView(binding);
    const payload = decidedApproval({ ...binding, ...overrides });
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(payload), calls),
    );

    await expect(
      client.decideApproval(pending, pending.choices[0]!),
    ).rejects.toBeInstanceOf(ControlContractError);

    const mutations = calls.filter(({ url }) =>
      url.pathname.endsWith("/actions/decide"),
    );
    expect(mutations).toHaveLength(2);
    expect(
      new Headers(mutations[0]?.init.headers).get("Idempotency-Key"),
    ).toBe(new Headers(mutations[1]?.init.headers).get("Idempotency-Key"));
  });

  it("binds a denied approval receipt to its exact choice", async () => {
    const bootstrap = session();
    const pending = approvalView();
    const choice = pending.choices[2]!;
    const denied = decidedApproval({
      state: "denied",
      decision: "deny",
      decisionScope: "request",
      terminalReason: "user_denied",
    });
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(denied)),
    );

    await expect(client.decideApproval(pending, choice)).resolves.toEqual(denied);
  });

  it("rejects an overflowing approval revision before sending a decision", async () => {
    const bootstrap = session();
    const calls: FetchCall[] = [];
    const pending = approvalView({ revision: Number.MAX_SAFE_INTEGER });
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse(decidedApproval()), calls),
    );

    await expect(
      client.decideApproval(pending, pending.choices[0]!),
    ).rejects.toBeInstanceOf(ControlContractError);
    expect(
      calls.some(({ url }) => url.pathname.endsWith("/actions/decide")),
    ).toBe(false);
  });

  it.each<{
    readonly name: string;
    readonly path: string;
    readonly payload: unknown;
    readonly invoke: (client: ControlClient) => Promise<unknown>;
  }>([
    {
      name: "an extra status response field",
      path: "/api/v1/status",
      payload: statusResponse({}, { readToken: "secret" }),
      invoke: (client) => client.status(),
    },
    {
      name: "an extra runtime status field",
      path: "/api/v1/status",
      payload: statusResponse({ rawEnvironment: "secret" }),
      invoke: (client) => client.status(),
    },
    {
      name: "a mismatched status generation",
      path: "/api/v1/status",
      payload: statusResponse({}, { generation: "another-generation" }),
      invoke: (client) => client.status(),
    },
    {
      name: "a self-consistent status from another native generation",
      path: "/api/v1/status",
      payload: statusResponse(
        { instanceId: "another-generation" },
        { generation: "another-generation" },
      ),
      invoke: (client) => client.status(),
    },
    {
      name: "a mismatched status localization key",
      path: "/api/v1/status",
      payload: statusResponse({}, { statusKey: "runtime.state.stopped" }),
      invoke: (client) => client.status(),
    },
    {
      name: "ready status for a degraded runtime",
      path: "/api/v1/status",
      payload: statusResponse({ state: "degraded", storage: "unavailable" }),
      invoke: (client) => client.status(),
    },
    {
      name: "a healthy projection with unavailable accesses",
      path: "/api/v1/status",
      payload: statusResponse({
        accessProjection: { state: "healthy", unavailableAccessCount: 1 },
      }),
      invoke: (client) => client.status(),
    },
    {
      name: "stopped runtime state without a stop timestamp",
      path: "/api/v1/status",
      payload: statusResponse({ state: "stopped" }, { ready: false }),
      invoke: (client) => client.status(),
    },
    {
      name: "stop-failed runtime state with an invented reason",
      path: "/api/v1/status",
      payload: statusResponse(
        { state: "stop_failed", stopReasonCode: "raw_shutdown_error" },
        { ready: false },
      ),
      invoke: (client) => client.status(),
    },
    {
      name: "a non-RFC3339 runtime start timestamp",
      path: "/api/v1/status",
      payload: statusResponse({ startedAt: "2026-08-03 08:00:00" }),
      invoke: (client) => client.status(),
    },
    {
      name: "an extra offline-hold field",
      path: "/api/v1/offline-hold",
      payload: { ...heldSnapshot(), queuedBodies: ["secret"] },
      invoke: (client) => client.offlineHold(),
    },
    {
      name: "an unknown offline-hold state",
      path: "/api/v1/offline-hold/actions/enter",
      payload: { ...heldSnapshot(), state: "paused" },
      invoke: (client) => client.enterOfflineHold(1),
    },
    {
      name: "an offline mutation response that did not advance revision",
      path: "/api/v1/offline-hold/actions/enter",
      payload: heldSnapshot(1),
      invoke: (client) => client.enterOfflineHold(1),
    },
    {
      name: "a fractional offline-hold counter",
      path: "/api/v1/offline-hold/actions/resume",
      payload: { ...onlineSnapshot(), activeActions: 0.5 },
      invoke: (client) => client.resumeOfflineHold(2),
    },
    {
      name: "an unknown offline-hold egress kind",
      path: "/api/v1/offline-hold",
      payload: {
        ...onlineSnapshot(),
        activeEgress: 1,
        activeByKind: { dns: 1 },
      },
      invoke: (client) => client.offlineHold(),
    },
    {
      name: "offline-hold kind totals inconsistent with the aggregate",
      path: "/api/v1/offline-hold/actions/enter",
      payload: {
        ...heldSnapshot(),
        activeEgress: 1,
        safeToDisconnect: false,
      },
      invoke: (client) => client.enterOfflineHold(1),
    },
    {
      name: "an unsafe offline-hold disconnect claim",
      path: "/api/v1/offline-hold/actions/enter",
      payload: { ...heldSnapshot(), safeToDisconnect: false },
      invoke: (client) => client.enterOfflineHold(1),
    },
    {
      name: "an empty entering transition",
      path: "/api/v1/offline-hold/actions/enter",
      payload: {
        ...heldSnapshot(),
        state: "entering",
        safeToDisconnect: false,
      },
      invoke: (client) => client.enterOfflineHold(1),
    },
    {
      name: "a stale probe reason on an online hold",
      path: "/api/v1/offline-hold/actions/resume",
      payload: { ...onlineSnapshot(), lastProbeReason: "tls_rejected" },
      invoke: (client) => client.resumeOfflineHold(2),
    },
    {
      name: "a non-RFC3339 offline-hold timestamp",
      path: "/api/v1/offline-hold",
      payload: { ...heldSnapshot(), since: "2026-08-03T08:00:00" },
      invoke: (client) => client.offlineHold(),
    },
    {
      name: "an extra Access apply response field",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "a".repeat(64),
        requestBody: "secret",
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "a non-committed successful Access apply outcome",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "indeterminate",
        revision: 1,
        applicationState: "active",
        planHash: "a".repeat(64),
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an Access apply revision that did not advance CAS once",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 2,
        applicationState: "active",
        planHash: "a".repeat(64),
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an unsafe Access apply revision",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: Number.MAX_SAFE_INTEGER + 1,
        applicationState: "active",
        planHash: "a".repeat(64),
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "a noncanonical Access apply plan hash",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "A".repeat(64),
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an active Access apply response without its exact plan hash",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 1,
        applicationState: "active",
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an unavailable Access apply response that claims an active plan hash",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 1,
        applicationState: "unavailable",
        planHash: "a".repeat(64),
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an unknown Access application state",
      path: "/api/v1/accesses/work/actions/apply",
      payload: {
        outcome: "committed",
        revision: 1,
        applicationState: "withdrawn",
      },
      invoke: (client) => client.applyAccess("work", accessApplyInput()),
    },
    {
      name: "an extra Access plan field",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({ providerOrigins: ["https://secret.example"] }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "a mismatched Access plan identity",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({ accessId: "personal" }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "duplicate Access plan profiles",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({ profiles: ["work-openai", "work-openai"] }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "a dangling Access plan account binding",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({
        accountBindings: [{ id: "work-account", profileId: "missing-profile" }],
      }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "a credential bound to the Core original route",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({
        profiles: ["original-passthrough"],
        accountBindings: [
          { id: "work-account", profileId: "original-passthrough" },
        ],
      }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "an unreferenced Access plan profile",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({
        profiles: ["work-openai", "work-secondary"],
      }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "an extra nested Access plan binding field",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({
        accountBindings: [
          {
            id: "work-account",
            profileId: "work-openai",
            secretRef: "secret://provider/work-account",
          },
        ],
      }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "too many Access plan profiles",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({
        profiles: Array.from({ length: 65 }, (_, index) => `profile-${index}`),
      }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "an unsafe Access plan revision",
      path: "/api/v1/accesses/work/plan",
      payload: accessPlanSummary({ revision: Number.MAX_SAFE_INTEGER + 1 }),
      invoke: (client) => client.accessPlan("work"),
    },
    {
      name: "an extra credential response field",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ secret: "secret" }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "a mismatched credential identity",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ credentialId: "other-account" }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "an unknown credential state",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ secretState: "locked" }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "a configured credential without a revision",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ secretRevision: 0 }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "a missing credential with a revision",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ secretState: "missing" }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "an unsafe credential revision",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account",
      payload: credentialView({ secretRevision: Number.MAX_SAFE_INTEGER + 1 }),
      invoke: (client) =>
        client.credential("work", "work-openai", "work-account"),
    },
    {
      name: "a missing credential returned after replacement",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account/actions/replace-secret",
      payload: credentialView({ secretState: "missing", secretRevision: 0 }),
      invoke: (client) =>
        client.replaceCredentialSecret(
          "work",
          "work-openai",
          "work-account",
          0,
          "secret",
        ),
    },
    {
      name: "an unavailable zero-revision replacement",
      path: "/api/v1/accesses/work/profiles/work-openai/credentials/work-account/actions/replace-secret",
      payload: credentialView({
        secretState: "unavailable",
        secretRevision: 0,
      }),
      invoke: (client) =>
        client.replaceCredentialSecret(
          "work",
          "work-openai",
          "work-account",
          0,
          "secret",
        ),
    },
  ])("rejects $name", async ({ path, payload, invoke }) => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url) => {
        expect(url.pathname).toBe(path);
        return jsonResponse(payload);
      }),
    );

    await expect(invoke(client)).rejects.toBeInstanceOf(ControlContractError);
  });

  it.each<{
    readonly name: string;
    readonly path: string;
    readonly payload: unknown;
    readonly invoke: (client: ControlClient) => Promise<unknown>;
  }>([
    {
      name: "an extra activity page field",
      path: "/api/v1/activities",
      payload: { items: [activityRecord()], rawRequest: "secret" },
      invoke: (client) => client.activities(),
    },
    {
      name: "an extra Exchange detail field",
      path: "/api/v1/exchanges/exchange-1",
      payload: { ...exchangeDetail(), rawPrompt: "secret" },
      invoke: (client) => client.exchange("exchange-1"),
    },
    {
      name: "a mismatched Exchange detail identity",
      path: "/api/v1/exchanges/exchange-1",
      payload: exchangeDetail({ id: "exchange-other" }),
      invoke: (client) => client.exchange("exchange-1"),
    },
    {
      name: "an extra Exchange processing trace field",
      path: "/api/v1/exchanges/exchange-1",
      payload: exchangeDetail({
        processingTrace: {
          pluginRunIds: [],
          attemptIds: [],
          result: "failed",
          rawBody: "secret",
        },
      }),
      invoke: (client) => client.exchange("exchange-1"),
    },
    {
      name: "duplicate Exchange attempt identities",
      path: "/api/v1/exchanges/exchange-1",
      payload: exchangeDetail({
        processingTrace: {
          pluginRunIds: [],
          attemptIds: ["attempt-1", "attempt-1"],
          result: "failed",
        },
      }),
      invoke: (client) => client.exchange("exchange-1"),
    },
    {
      name: "an extra activity record field",
      path: "/api/v1/activities",
      payload: { items: [{ ...activityRecord(), rawBody: "secret" }] },
      invoke: (client) => client.activities(),
    },
    {
      name: "a non-RFC3339 activity timestamp",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ occurredAt: "2026-08-03" })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "an impossible activity calendar date",
      path: "/api/v1/activities",
      payload: {
        items: [activityRecord({ occurredAt: "2026-02-30T08:00:00Z" })],
      },
      invoke: (client) => client.activities(),
    },
    {
      name: "a legacy activity sequence field",
      path: "/api/v1/activities",
      payload: {
        items: [activityRecord({ sequence: 1 })],
      },
      invoke: (client) => client.activities(),
    },
    {
      name: "a legacy activity kind field",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ kind: "exchange.completed" })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "legacy raw diagnosis fields",
      path: "/api/v1/activities",
      payload: {
        items: [
          activityRecord({
            reasonCode: "provider_rejected_request",
            diagnosis: { providerStatus: 429 },
          }),
        ],
      },
      invoke: (client) => client.activities(),
    },
    {
      name: "legacy raw transport evidence",
      path: "/api/v1/activities",
      payload: {
        items: [
          activityRecord({
            transport: { clientOfferedAlpn: ["h2", "http/1.1"] },
          }),
        ],
      },
      invoke: (client) => client.activities(),
    },
    {
      name: "an empty activity status",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ status: "" })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "a control character in an activity status",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ status: "failed\nsecret" })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "a bidirectional format character in an activity status",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ status: "failed\u202ereviewed" })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "an overlong activity status",
      path: "/api/v1/activities",
      payload: { items: [activityRecord({ status: "x".repeat(129) })] },
      invoke: (client) => client.activities(),
    },
    {
      name: "more than two hundred activity summaries",
      path: "/api/v1/activities",
      payload: {
        items: Array.from({ length: 201 }, (_, index) =>
          activityRecord({ id: `exchange-${index}` }),
        ),
      },
      invoke: (client) => client.activities(),
    },
    {
      name: "a padded activity cursor",
      path: "/api/v1/activities",
      payload: { items: [activityRecord()], nextCursor: "b3BhcXVl==" },
      invoke: (client) => client.activities(),
    },
    {
      name: "a noncanonical activity cursor",
      path: "/api/v1/activities",
      payload: { items: [activityRecord()], nextCursor: "AB" },
      invoke: (client) => client.activities(),
    },
    {
      name: "an extra approval page field",
      path: "/api/v1/approvals",
      payload: { items: [approvalView()], nextCursor: "leak" },
      invoke: (client) => client.approvals(),
    },
    {
      name: "an extra nested approval choice field",
      path: "/api/v1/approvals",
      payload: {
        items: [
          {
            ...approvalView(),
            choices: approvalView().choices.map((choice, index) =>
              index === 0 ? { ...choice, rawArguments: "secret" } : choice,
            ),
          },
        ],
      },
      invoke: (client) => client.approvals(),
    },
    {
      name: "an out-of-range approval target port",
      path: "/api/v1/approvals",
      payload: {
        items: [
          { ...approvalView(), target: { host: "api.example.com", port: 65_536 } },
        ],
      },
      invoke: (client) => client.approvals(),
    },
    {
      name: "an unknown approval state",
      path: "/api/v1/approvals",
      payload: { items: [{ ...approvalView(), state: "approved" }] },
      invoke: (client) => client.approvals(),
    },
    {
      name: "terminal fields on a pending approval",
      path: "/api/v1/approvals",
      payload: {
        items: [
          {
            ...approvalView(),
            resolvedAt: "2026-08-03T08:01:00Z",
            decision: "allow-once",
            decisionScope: "request",
          },
        ],
      },
      invoke: (client) => client.approvals(),
    },
    {
      name: "an extra singleton approval decision field",
      path: "/api/v1/approvals/approval-1/actions/decide",
      payload: { ...decidedApproval(), rawArguments: "secret" },
      invoke: (client) => {
        const pending = approvalView();
        return client.decideApproval(pending, pending.choices[0]!);
      },
    },
    {
      name: "an extra connection page field",
      path: "/api/v1/connections",
      payload: { items: [connectionRecord()], rawHeaders: "secret" },
      invoke: (client) => client.connections(),
    },
    {
      name: "a padded connection cursor",
      path: "/api/v1/connections",
      payload: { items: [connectionRecord()], nextCursor: "djE6MQ==" },
      invoke: (client) => client.connections(),
    },
    {
      name: "an extra connection record field",
      path: "/api/v1/connections",
      payload: { items: [{ ...connectionRecord(), requestPath: "/private" }] },
      invoke: (client) => client.connections(),
    },
    {
      name: "a zero connection port",
      path: "/api/v1/connections",
      payload: { items: [connectionRecord({ port: 0 })] },
      invoke: (client) => client.connections(),
    },
    {
      name: "an unsafe connection byte count",
      path: "/api/v1/connections",
      payload: {
        items: [connectionRecord({ bytesDown: Number.MAX_SAFE_INTEGER + 1 })],
      },
      invoke: (client) => client.connections(),
    },
    {
      name: "a malformed IPv6 connection address",
      path: "/api/v1/connections",
      payload: { items: [connectionRecord({ ip: "1::2::3" })] },
      invoke: (client) => client.connections(),
    },
    {
      name: "inconsistent terminal connection evidence",
      path: "/api/v1/connections",
      payload: { items: [connectionRecord({ phase: "connected" })] },
      invoke: (client) => client.connections(),
    },
    {
      name: "an extra egress page field",
      path: "/api/v1/egress-attempts",
      payload: { items: [egressAttemptRecord()], requestBody: "secret" },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "an extra nested egress parent field",
      path: "/api/v1/egress-attempts",
      payload: {
        items: [
          egressAttemptRecord({
            parent: {
              kind: "blind_connection",
              id: "parent-1",
              rawRequest: "secret",
            },
          }),
        ],
      },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "an incomplete nested egress decision",
      path: "/api/v1/egress-attempts",
      payload: {
        items: [
          egressAttemptRecord({
            decision: {
              policyId: "network-policy-1",
              policyRevision: 1,
              authority: "network",
              ruleId: "network-rule-1",
            },
          }),
        ],
      },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "a mismatched egress purpose authority",
      path: "/api/v1/egress-attempts",
      payload: {
        items: [
          egressAttemptRecord({
            decision: {
              policyId: "access-policy-1",
              policyRevision: 1,
              authority: "access",
              ruleId: "access-rule-1",
              proxyId: "direct",
            },
          }),
        ],
      },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "an egress target URL with a path",
      path: "/api/v1/egress-attempts",
      payload: {
        items: [egressAttemptRecord({ targetOrigin: "https://api.example.com/v1" })],
      },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "terminal evidence on a live egress attempt",
      path: "/api/v1/egress-attempts",
      payload: { items: [egressAttemptRecord({ terminal: false })] },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "an unsafe egress byte count",
      path: "/api/v1/egress-attempts",
      payload: {
        items: [egressAttemptRecord({ bytesOut: Number.MAX_SAFE_INTEGER + 1 })],
      },
      invoke: (client) => client.egressAttempts(),
    },
    {
      name: "an extra capture page field",
      path: "/api/v1/capture-runs",
      payload: { items: [captureRunRecord()], rawEnvironment: "secret" },
      invoke: (client) => client.captureRuns(),
    },
    {
      name: "more than fifty capture records",
      path: "/api/v1/capture-runs",
      payload: { items: Array.from({ length: 51 }, captureRunRecord) },
      invoke: (client) => client.captureRuns(),
    },
  ])("rejects $name", async ({ path, payload, invoke }) => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, (url) => {
        expect(url.pathname).toBe(path);
        return jsonResponse(payload);
      }),
    );

    await expect(invoke(client)).rejects.toBeInstanceOf(ControlContractError);
  });

  it("rejects a null collection as an invalid wire response", async () => {
    const bootstrap = session();
    const client = await createControlClient(
      bootstrap,
      withSessionState(bootstrap, () => jsonResponse({ items: null })),
    );

    await expect(client.activities()).rejects.toEqual(
      expect.objectContaining<Partial<ControlContractError>>({
        reasonCode: "control_contract_invalid",
        messageKey: "error.control_invalid_response",
      }),
    );
  });
});

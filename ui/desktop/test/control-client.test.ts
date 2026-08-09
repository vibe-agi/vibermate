import { describe, expect, it, vi } from "vitest";
import {
  ControlContractError,
  ControlProblem,
  createControlClient,
  type DesktopSession,
} from "../src/control-client.ts";
import type { EnvironmentRecord } from "../src/control-types.ts";

const sessionStatePath = "/api/v1/auth/sessions/current";
const digest = "1".repeat(64);
const draftDigest = "2".repeat(64);

function capability(fill: number): string {
  const bytes = new Uint8Array(32).fill(fill);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/u, "");
}

function session(): DesktopSession {
  return {
    schema: "vibermate-app-session-v1",
    baseUrl: "http://127.0.0.1:43127",
    readToken: capability(0x11),
    writeToken: capability(0x22),
    instanceId: "runtime-instance",
    expiresAt: "2099-08-08T00:00:00Z",
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

function environment(overrides: Partial<EnvironmentRecord> = {}): EnvironmentRecord {
  return {
    id: "work",
    name: "Work",
    state: "active",
    revision: 3,
    digest,
    systemOwned: false,
    clientEndpoints: [],
    pluginBindings: [],
    budgetPolicy: { id: "", revision: 0 },
    egressPolicy: { id: "", revision: 0, mode: "" },
    contentRecording: { mode: "full", retentionDays: 30 },
    ...overrides,
  };
}

function sessionAwareFetch(
  handler: (url: URL, init: RequestInit) => Response | Promise<Response>,
) {
  return vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input));
    if (url.pathname === sessionStatePath) {
      return jsonResponse({
        schema: "vibermate-app-session-state-v1",
        revision: 1,
        expiresAt: session().expiresAt,
      });
    }
    return handler(url, init);
  });
}

describe("Environment-first desktop control client", () => {
  it("preserves a missing Environment draft as a typed control Problem", async () => {
    const fetch = sessionAwareFetch((url) => {
      expect(url.pathname).toBe("/api/v1/environments/work/draft");
      return new Response(
        JSON.stringify({
          type: "urn:vibermate:error:environment-draft-not-found",
          title: "Not Found",
          status: 404,
          code: "environment_draft_not_found",
        }) + "\n",
        {
          status: 404,
          headers: {
            "Cache-Control": "no-store",
            "Content-Type": "application/problem+json",
          },
        },
      );
    });
    const client = await createControlClient(session(), fetch);

    const error = await client.environmentDraft("work").catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(ControlProblem);
    expect(error).toMatchObject({
      status: 404,
      reasonCode: "environment_draft_not_found",
      messageKey: "error.environment_draft_not_found",
    });
  });

  it("uses the read capability and accepts only the new Environment directory", async () => {
    const fetch = sessionAwareFetch((url, init) => {
      expect(url.pathname).toBe("/api/v1/environments");
      expect(new Headers(init.headers).get("Authorization")).toBe(
        `Bearer ${session().readToken}`,
      );
      return jsonResponse({ items: [environment()] });
    });
    const client = await createControlClient(session(), fetch);

    await expect(client.environments()).resolves.toEqual({
      items: [environment()],
    });
  });

  it("accepts the Core-owned transparent Environment before bytewise user IDs", async () => {
    const transparent = environment({
      id: "system_transparent",
      name: "Transparent",
      revision: 1,
      systemOwned: true,
      contentRecording: { mode: "off", retentionDays: 0 },
    });
    const inspect = environment({ id: "claude-inspect", name: "Claude inspect" });
    const client = await createControlClient(
      session(),
      sessionAwareFetch(() => jsonResponse({ items: [transparent, inspect] })),
    );

    await expect(client.environments()).resolves.toEqual({
      items: [transparent, inspect],
    });
  });

  it("rejects a user Environment placed before the Core-owned directory entry", async () => {
    const transparent = environment({
      id: "system_transparent",
      name: "Transparent",
      revision: 1,
      systemOwned: true,
      contentRecording: { mode: "off", retentionDays: 0 },
    });
    const inspect = environment({ id: "claude-inspect", name: "Claude inspect" });
    const client = await createControlClient(
      session(),
      sessionAwareFetch(() => jsonResponse({ items: [inspect, transparent] })),
    );

    await expect(client.environments()).rejects.toBeInstanceOf(ControlContractError);
  });

  it("sets and clears the next-run Environment for one exact machine and workspace", async () => {
    const machineId = capability(0x31);
    const workspaceId = capability(0x32);
    const record = {
      machineId,
      workspaceId,
      environmentId: "work",
      environmentName: "Work",
      revision: 1,
      updatedAt: "2026-08-08T09:30:00Z",
    } as const;
    const calls: Array<{ url: URL; init: RequestInit }> = [];
    const fetch = sessionAwareFetch((url, init) => {
      calls.push({ url, init });
      if (init.method === "PUT") return jsonResponse(record);
      if (init.method === "DELETE") return new Response(null, { status: 204 });
      return jsonResponse(record);
    });
    const client = await createControlClient(session(), fetch);

    await expect(
      client.setWorkspaceEnvironmentDefault(machineId, workspaceId, 0, "work"),
    ).resolves.toEqual(record);
    await expect(
      client.workspaceEnvironmentDefault(machineId, workspaceId),
    ).resolves.toEqual(record);
    await expect(
      client.clearWorkspaceEnvironmentDefault(machineId, workspaceId, 1),
    ).resolves.toBeUndefined();

    const expectedPath = `/api/v1/machines/${machineId}/workspaces/${workspaceId}/environment-default`;
    expect(calls.map(({ url }) => url.pathname)).toEqual([
      expectedPath,
      expectedPath,
      expectedPath,
    ]);
    expect(new Headers(calls[0]?.init.headers).get("If-Match")).toBe("0");
    expect(calls[0]?.init.body).toBe(JSON.stringify({ environmentId: "work" }));
    expect(new Headers(calls[2]?.init.headers).get("If-Match")).toBe("1");
    expect(calls[2]?.init.body).toBeUndefined();
  });

  it("creates and reads managed ProviderAccounts without accepting secret material in responses", async () => {
    const account = {
      id: "claude-oauth",
      displayName: "Claude OAuth",
      kind: "claude_oauth_token",
      realmId: "anthropic.official",
      state: "active",
      revision: 1,
      credentialState: "ready",
      credentialEpoch: 1,
    } as const;
    const calls: Array<{ url: URL; init: RequestInit }> = [];
    const fetch = sessionAwareFetch((url, init) => {
      calls.push({ url, init });
      if (init.method === "POST") return jsonResponse(account, 201);
      return jsonResponse({ items: [account] });
    });
    const client = await createControlClient(session(), fetch);

    await expect(
      client.createProviderAccount({
        id: account.id,
        displayName: account.displayName,
        kind: account.kind,
        secret: "oauth-control-only",
      }),
    ).resolves.toEqual(account);
    await expect(client.providerAccounts()).resolves.toEqual({ items: [account] });
    expect(calls.map(({ url }) => url.pathname)).toEqual([
      "/api/v1/provider-accounts",
      "/api/v1/provider-accounts",
    ]);
    expect(calls[0]?.init.body).toBe(
      JSON.stringify({
        id: account.id,
        displayName: account.displayName,
        kind: account.kind,
        secret: "oauth-control-only",
      }),
    );
    expect(new Headers(calls[0]?.init.headers).get("If-Match")).toBe("0");
  });

  it("deletes an unreferenced ProviderAccount with a credential-epoch CAS", async () => {
    const result = {
      deleted: false,
      referenceCount: 1,
      references: [{
        environmentId: "work",
        environmentName: "Work",
        environmentRevision: 3,
        routeId: "claude-official",
        routeRevision: 2,
      }],
    } as const;
    const calls: Array<{ url: URL; init: RequestInit }> = [];
    const fetch = sessionAwareFetch((url, init) => {
      calls.push({ url, init });
      return jsonResponse(result);
    });
    const client = await createControlClient(session(), fetch);

    await expect(client.deleteProviderAccount("anthropic-work", 7)).resolves.toEqual(result);
    expect(calls[0]?.url.pathname).toBe("/api/v1/provider-accounts/anthropic-work");
    expect(calls[0]?.init.method).toBe("DELETE");
    expect(calls[0]?.init.body).toBeUndefined();
    expect(new Headers(calls[0]?.init.headers).get("If-Match")).toBe("7");
  });

  it("rejects a ProviderAccount response that leaks a secret-shaped field", async () => {
    const fetch = sessionAwareFetch(() =>
      jsonResponse({
        items: [
          {
            id: "anthropic-work",
            displayName: "Anthropic Work",
            kind: "anthropic_api_key",
            realmId: "anthropic.official",
            state: "active",
            revision: 1,
            credentialState: "ready",
            credentialEpoch: 1,
            secretReference: "secret://provider-account/anthropic-work",
          },
        ],
      }),
    );
    const client = await createControlClient(session(), fetch);

    await expect(client.providerAccounts()).rejects.toBeInstanceOf(
      ControlContractError,
    );
  });

  it("accepts the canonical default-port origin emitted by the Go authority", async () => {
    const canonical = environment({
      clientEndpoints: [
        {
          id: "endpoint.claude.official",
          revision: 1,
          clientOrigin: "https://api.anthropic.com",
          protocolPlans: [
            {
              id: "plan.claude.official",
              revision: 1,
              clientProtocol: "anthropic_messages",
              clientAdapterPolicy: { id: "adapter.claude.official", revision: 1 },
              mode: "managed",
              upstreamPlan: {
                routes: [
                  {
                    id: "route.claude.official",
                    revision: 1,
                    providerTarget: {
                      id: "target.claude.official",
                      revision: 1,
                      origin: "https://api.anthropic.com",
                      realmId: "anthropic.official",
                      capabilities: ["messages", "streaming"],
                    },
                    backendProtocol: "anthropic_messages",
                    accountPolicy: {
                      revision: 1,
                      mode: "client_passthrough",
                      allowedRealmIds: ["anthropic.official"],
                      preferredAccountId: "",
                      candidateAccountIds: [],
                      accountRevisions: {},
                      failoverPolicy: "off",
                    },
                    modelPolicy: {
                      revision: 1,
                      mode: "passthrough",
                      fixedModel: "",
                    },
                    wireProfileRef: "follow-client",
                    pluginBindings: [],
                  },
                ],
                defaultRouteId: "route.claude.official",
                routeSet: {
                  id: "routes.claude.official",
                  revision: 1,
                  candidateRouteIds: ["route.claude.official"],
                },
              },
              pluginBindings: [],
            },
          ],
        },
      ],
    });
    const fetch = sessionAwareFetch(() => jsonResponse({ items: [canonical] }));
    const client = await createControlClient(session(), fetch);

    await expect(client.environments()).resolves.toEqual({ items: [canonical] });
  });

  it("preserves the typed draft impact including restart-required captures", async () => {
    const fetch = sessionAwareFetch((url) => {
      expect(url.pathname).toBe(
        "/api/v1/environments/work/draft/actions/preview",
      );
      return jsonResponse({
        environmentId: "work",
        baseRevision: 3,
        draftRevision: 2,
        candidateDigest: draftDigest,
        classification: "restart_required",
        hotSwitchCount: 0,
        reconnectRequiredCount: 0,
        restartRequiredCount: 1,
        affected: [
          {
            captureKind: "managed_run",
            captureId: "run-one",
            classification: "restart_required",
          },
        ],
      });
    });
    const client = await createControlClient(session(), fetch);

    const impact = await client.previewEnvironmentDraft("work", 2);
    expect(impact.restartRequiredCount).toBe(1);
    expect(impact.affected[0]?.classification).toBe("restart_required");
  });

  it("keeps the typed Capture key intact and sends switch CAS authority", async () => {
    const calls: Array<{ url: URL; init: RequestInit }> = [];
    const fetch = sessionAwareFetch((url, init) => {
      calls.push({ url, init });
      if (init.method === "GET") {
        return jsonResponse({
          captureKey: "managed_run:same-id",
          captureId: "same-id",
          captureKind: "managed_run",
          environmentId: "work",
          revision: 4,
          source: "launch",
          updatedAt: "2026-08-08T08:00:00Z",
        });
      }
      return jsonResponse({
        assignment: {
          captureKey: "managed_run:same-id",
          captureId: "same-id",
          captureKind: "managed_run",
          environmentId: "personal",
          revision: 5,
          source: "operator_switch",
          updatedAt: "2026-08-08T08:01:00Z",
        },
        boundary: "reconnect_required",
        closedConnections: ["connection-one"],
        applied: true,
      });
    });
    const client = await createControlClient(session(), fetch);

    await client.captureAssignment("managed_run:same-id");
    const result = await client.switchCaptureEnvironment(
      "managed_run:same-id",
      4,
      "personal",
    );
    expect(result.boundary).toBe("reconnect_required");
    expect(calls.map(({ url }) => url.pathname)).toEqual([
      "/api/v1/captures/managed_run%3Asame-id/environment-assignment",
      "/api/v1/captures/managed_run%3Asame-id/environment-assignment",
    ]);
    const mutationHeaders = new Headers(calls[1]?.init.headers);
    expect(mutationHeaders.get("If-Match")).toBe("4");
    expect(mutationHeaders.get("Idempotency-Key")).toMatch(
      /^[A-Za-z0-9_-]{32}$/u,
    );
    expect(calls[1]?.init.body).toBe(JSON.stringify({ environmentId: "personal" }));
  });

  it("accepts base64url Capture IDs that begin with an underscore", async () => {
    const runId = "_qrF8c7WA75MqClA2xoiPqyS8eg";
    const capture = {
      key: `managed_run:${runId}`,
      id: runId,
      kind: "managed_run",
      displayName: "claude",
      state: "finished",
      observation: "waiting_for_traffic",
      createdAt: "2026-08-08T08:00:00Z",
      updatedAt: "2026-08-08T08:00:01Z",
      managedRun: {
        executableLabel: "claude",
        cwd: "/Users/example/project",
        canonicalExecutablePath: "/Users/example/.local/bin/claude",
        recognition: "recognized",
        expiresAt: "2026-08-08T08:02:00Z",
      },
    } as const;
    const fetch = sessionAwareFetch(() => jsonResponse({ items: [capture] }));
    const client = await createControlClient(session(), fetch);

    await expect(client.captures()).resolves.toEqual({ items: [capture] });
  });

  it("requests ManualCapture authority in one explicit Environment", async () => {
    const fetch = sessionAwareFetch((url) => {
      expect(url.pathname).toBe("/api/v1/manual-captures/context");
      expect(url.searchParams.get("environmentId")).toBe("work");
      return jsonResponse({
        confirmationToken: `ctx_${"A".repeat(43)}`,
        proxyAddress: "http://127.0.0.1:43180",
        environmentId: "work",
        environmentRevision: 3,
        environmentDigest: digest,
        launchAuthorityDigest: "3".repeat(64),
        protectedAuthorities: ["api.anthropic.com:443"],
        managedCredentialAuthorities: [],
        defaultTemporarySeconds: 3_600,
        maxTemporarySeconds: 86_400,
      });
    });
    const client = await createControlClient(session(), fetch);

    const context = await client.manualCaptureContext("work");
    expect(context.environmentId).toBe("work");
    expect(context.environmentRevision).toBe(3);
  });

  it("fails closed on legacy projection casing instead of accepting a split contract", async () => {
    const fetch = sessionAwareFetch(() =>
      jsonResponse({
        generation: "runtime-generation",
        ready: true,
        apiVersion: "v1",
        statusKey: "status.ready",
        runtime: {
          state: "initialized",
          instanceId: "runtime-instance",
          host: "desktop",
          schemaRevision: 1,
          storage: "healthy",
          environmentProjection: {
            State: "healthy",
            UnavailableEnvironments: null,
          },
          offlineHold: {
            state: "online",
            revision: 1,
            since: "2026-08-08T08:00:00Z",
            activeActions: 0,
            enteringActions: 0,
            activeEgress: 0,
            queuedRequests: 0,
            heldBytes: 0,
            safeToDisconnect: true,
            activeByKind: {},
            queuedByKind: {},
          },
          startedAt: "2026-08-08T08:00:00Z",
        },
      }),
    );
    const client = await createControlClient(session(), fetch);

    await expect(client.status()).rejects.toBeInstanceOf(ControlContractError);
  });
});

import { describe, expect, it, vi } from "vitest";
import { buildAccessApplyInput, initialAccessForm } from "../src/access-form.ts";
import {
  ControlContractError,
  ControlProblem,
  createControlClient,
  type DesktopSession,
} from "../src/control-client.ts";

function capability(fill: number): string {
  const bytes = new Uint8Array(32).fill(fill);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

function session(): DesktopSession {
  return {
    schema: "vibermate-app-session-v1",
    baseUrl: "http://127.0.0.1:43127",
    readToken: capability(0x11),
    writeToken: capability(0x22),
    instanceId: "runtime-instance",
    expiresAt: "2099-07-30T00:00:00Z",
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

describe("Desktop control client", () => {
  it("uses distinct read and write capabilities with CAS mutation headers", async () => {
    const requests: { readonly url: URL; readonly init: RequestInit }[] = [];
    const fetchImplementation = vi.fn(
      async (input: RequestInfo | URL, init: RequestInit = {}) => {
        requests.push({ url: new URL(String(input)), init });
        if (init.method === "GET") {
          return jsonResponse({ ready: true });
        }
        return jsonResponse({
          state: "held",
          revision: 2,
          since: "2026-07-29T00:00:00Z",
          activeActions: 0,
          enteringActions: 0,
          activeEgress: 0,
          queuedRequests: 0,
          heldBytes: 0,
          safeToDisconnect: true,
          activeByKind: {},
          queuedByKind: {},
        });
      },
    );
    const bootstrap = session();
    const client = createControlClient(bootstrap, fetchImplementation);

    await client.status();
    await client.enterOfflineHold(1);

    expect(requests).toHaveLength(2);
    const readHeaders = new Headers(requests[0]?.init.headers);
    const writeHeaders = new Headers(requests[1]?.init.headers);
    expect(readHeaders.get("Authorization")).toBe(`Bearer ${bootstrap.readToken}`);
    expect(writeHeaders.get("Authorization")).toBe(`Bearer ${bootstrap.writeToken}`);
    expect(writeHeaders.get("If-Match")).toBe("1");
    expect(writeHeaders.get("Idempotency-Key")).toMatch(/^[A-Za-z0-9_-]{16,128}$/u);
    expect(requests[1]?.url.origin).toBe(bootstrap.baseUrl);
    expect(requests[1]?.url.href).not.toContain(bootstrap.writeToken);
    expect(requests[1]?.init.credentials).toBe("omit");
    expect(requests[1]?.init.redirect).toBe("error");
  });

  it("constructs the complete bounded M0 aggregate without a secret value", async () => {
    const fetchImplementation = vi.fn(
      async (_input: RequestInfo | URL, init: RequestInit = {}) =>
        jsonResponse({
          outcome: "committed",
          revision: 1,
          planHash: "a".repeat(64),
          requestBody: init.body,
        }),
    );
    const client = createControlClient(session(), fetchImplementation);
    const input = buildAccessApplyInput({
      ...initialAccessForm,
      accessId: "work",
      name: "Work",
    });

    await client.applyAccess("work", input);

    const init = fetchImplementation.mock.calls[0]?.[1] as RequestInit;
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

  it("writes a credential only through the scoped write-only action", async () => {
    const requests: { readonly url: URL; readonly init: RequestInit }[] = [];
    const fetchImplementation = vi.fn(
      async (input: RequestInfo | URL, init: RequestInit = {}) => {
        requests.push({ url: new URL(String(input)), init });
        return jsonResponse({
          credentialId: "work-account",
          profileId: "work-openai",
          secretState: "configured",
          secretRevision: 1,
        });
      },
    );
    const client = createControlClient(session(), fetchImplementation);

    await client.replaceCredentialSecret(
      "work",
      "work-openai",
      "work-account",
      0,
      "provider-secret-value",
    );

    expect(requests).toHaveLength(1);
    expect(requests[0]?.url.pathname).toBe(
      "/api/v1/accesses/work/profiles/work-openai/credentials/work-account/actions/replace-secret",
    );
    const headers = new Headers(requests[0]?.init.headers);
    expect(headers.get("If-Match")).toBe("0");
    expect(headers.get("Authorization")).toBe(`Bearer ${session().writeToken}`);
    expect(JSON.parse(String(requests[0]?.init.body))).toEqual({
      secret: "provider-secret-value",
    });
  });

  it("loads active plan metadata through the read capability", async () => {
    const requests: { readonly url: URL; readonly init: RequestInit }[] = [];
    const fetchImplementation = vi.fn(
      async (input: RequestInfo | URL, init: RequestInit = {}) => {
        requests.push({ url: new URL(String(input)), init });
        return jsonResponse({
          accessId: "work",
          revision: 4,
          planHash: "a".repeat(64),
          profiles: ["work-openai"],
          accountBindings: [
            { id: "work-account", profileId: "work-openai" },
          ],
        });
      },
    );
    const bootstrap = session();
    const client = createControlClient(bootstrap, fetchImplementation);

    const plan = await client.accessPlan("work");

    expect(plan.revision).toBe(4);
    expect(requests[0]?.url.pathname).toBe(
      "/api/v1/accesses/work/plan",
    );
    const headers = new Headers(requests[0]?.init.headers);
    expect(headers.get("Authorization")).toBe(
      `Bearer ${bootstrap.readToken}`,
    );
    expect(headers.has("If-Match")).toBe(false);
  });

  it("rejects ambient authorities and preserves stable problem codes", async () => {
    expect(() =>
      createControlClient({
        ...session(),
        baseUrl: "http://localhost:43127",
      }),
    ).toThrow(/literal IPv4 loopback/u);

    const client = createControlClient(
      session(),
      async () =>
        new Response(
          JSON.stringify({
            status: 409,
            reasonCode: "revision_conflict",
            messageKey: "error.revision_conflict",
          }),
          {
            status: 409,
            headers: { "Content-Type": "application/problem+json" },
          },
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

  it("rejects a null collection as an invalid wire response", async () => {
    const client = createControlClient(
      session(),
      async () => jsonResponse({ items: null }),
    );

    await expect(client.activities()).rejects.toEqual(
      expect.objectContaining<Partial<ControlContractError>>({
        reasonCode: "control_contract_invalid",
        messageKey: "error.control_invalid_response",
      }),
    );
  });
});

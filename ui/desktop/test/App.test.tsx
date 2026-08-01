import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import { Dashboard } from "../src/App.tsx";
import { ControlProblem } from "../src/control-client.ts";
import type { ControlClient } from "../src/control-client.ts";
import { DashboardModel } from "../src/dashboard-model.ts";
import approvalSamples from "../src/generated/samples/approvals.json" with { type: "json" };
import captureRunSamples from "../src/generated/samples/capture-runs.json" with { type: "json" };
import connectionSamples from "../src/generated/samples/connections.json" with { type: "json" };
import egressSamples from "../src/generated/samples/egress-attempts.json" with { type: "json" };
import type {
  AccessApplyInput,
  ApprovalChoice,
  ApprovalKind,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  EgressAttemptRecord,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
} from "../src/control-types.ts";
import { createI18n } from "../src/i18n.ts";

const offline: OfflineHoldSnapshot = {
  state: "online",
  revision: 1,
  since: "2026-07-29T00:00:00Z",
  activeActions: 0,
  enteringActions: 0,
  activeEgress: 0,
  queuedRequests: 0,
  heldBytes: 0,
  safeToDisconnect: false,
  activeByKind: {},
  queuedByKind: {},
};

const status: StatusResponse = {
  generation: "runtime-instance",
  ready: true,
  apiVersion: "v1",
  statusKey: "runtime.state.initialized",
  runtime: {
    state: "initialized",
    instanceId: "runtime-instance",
    host: "desktop",
    schemaRevision: 7,
    storage: "healthy",
    accessProjection: {
      state: "healthy",
      unavailableAccessCount: 0,
    },
    offlineHold: offline,
    startedAt: "2026-07-29T00:00:00Z",
  },
};

// The shapes the window renders come from the runtime itself. A hand-typed
// fixture can keep passing after the runtime stops sending the field it
// describes, which is exactly the failure this window had.
const samples = approvalSamples as readonly ApprovalView[];

function sampleOfKind(kind: ApprovalKind): ApprovalView {
  const found = samples.find((candidate) => candidate.kind === kind);
  if (found === undefined) {
    throw new Error(`no ${kind} sample is generated`);
  }
  return found;
}

const approval = sampleOfKind("tool_intent");
const networkAsk = sampleOfKind("network_ask");

function clientFixture() {
  return {
    status: vi.fn(async (_signal?: AbortSignal) => status),
    offlineHold: vi.fn(async (_signal?: AbortSignal) => offline),
    enterOfflineHold: vi.fn(async (_revision: number, _signal?: AbortSignal) => ({
      ...offline,
      state: "held" as const,
      revision: 2,
    })),
    resumeOfflineHold: vi.fn(async (_revision: number, _signal?: AbortSignal) => ({
      ...offline,
      revision: 2,
    })),
    activities: vi.fn(async (_signal?: AbortSignal) => ({
      items: [
        {
          sequence: 1,
          id: "activity-id",
          occurredAt: "2026-07-29T00:00:00Z",
          kind: "access.applied",
          accessId: "work",
          subjectId: "1",
          status: "succeeded",
        },
      ],
    })),
    approvals: vi.fn(async (_signal?: AbortSignal) => ({ items: [approval] })),
    captureRuns: vi.fn(async (_signal?: AbortSignal) => ({
      items: captureRunSamples as readonly CaptureRunRecord[],
    })),
    connections: vi.fn(async (_signal?: AbortSignal) => ({
      items: connectionSamples as readonly ConnectionRecord[],
    })),
    egressAttempts: vi.fn(async (_signal?: AbortSignal) => ({
      items: egressSamples as readonly EgressAttemptRecord[],
    })),
    decideApproval: vi.fn(
      async (
        _approval: ApprovalView,
        _choice: ApprovalChoice,
        _signal?: AbortSignal,
      ) => ({ ...approval, state: "denied" as const }),
    ),
    applyAccess: vi.fn(
      async (
        _accessId: string,
        _input: AccessApplyInput,
        _signal?: AbortSignal,
      ) => ({
      outcome: "committed" as const,
      revision: 1,
        planHash: "b".repeat(64),
      }),
    ),
    accessPlan: vi.fn(async (accessId: string) => ({
      accessId,
      revision: 4,
      planHash: "c".repeat(64),
      profiles: [`${accessId}-openai`],
      accountBindings: [
        {
          id: `${accessId}-account`,
          profileId: `${accessId}-openai`,
        },
      ],
    })),
    credential: vi.fn(async (): Promise<CredentialView> => ({
      credentialId: "work-account",
      profileId: "work-openai",
      secretState: "missing",
      secretRevision: 0,
    })),
    replaceCredentialSecret: vi.fn(async () => ({
      credentialId: "work-account",
      profileId: "work-openai",
      secretState: "configured" as const,
      secretRevision: 1,
    })),
  } satisfies ControlClient;
}

describe("Desktop dashboard", () => {
  it("drives hold, approval, and complete Access mutations through one client", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Ready")).toBeTruthy();
    expect(screen.getByText("read_file, list_directory")).toBeTruthy();
    expect(screen.queryByText("raw-secret-tool-arguments")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Enter offline hold" }));
    await waitFor(() => expect(client.enterOfflineHold).toHaveBeenCalledWith(1, expect.any(AbortSignal)));

    fireEvent.click(
      screen.getByRole("button", { name: "Refuse these tool calls" }),
    );
    await waitFor(() =>
      expect(client.decideApproval).toHaveBeenCalledWith(
        approval,
        {
          decision: "deny",
          scope: "request",
          labelKey: "approval.toolIntent.choice.deny",
        },
        expect.any(AbortSignal),
      ),
    );

    fireEvent.change(screen.getByLabelText("Access ID"), {
      target: { value: "work" },
    });
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Work" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply Access" }));
    await waitFor(() => expect(client.applyAccess).toHaveBeenCalledTimes(1));
    const [accessId, input] = client.applyAccess.mock.calls[0] ?? [];
    expect(accessId).toBe("work");
    expect(input?.agentEndpoint.clientDialect).toBe("anthropic-messages");
    expect(input?.profiles[0]?.backendDialect).toBe("openai-chat");
    expect(input?.profiles[0]?.transportProfileRef).toBe(
      "observed-client-strict-h1",
    );
    expect(input?.accountBindings[0]?.secretRef).toBe(
      "secret://provider/work-account",
    );
    expect(input?.pluginPlan.bindingIds).toEqual([]);
    expect(await screen.findByText("Access revision 1 is active.")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Provider API key"), {
      target: { value: "provider-secret-value" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save credential" }));
    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        "work",
        "work-openai",
        "work-account",
        0,
        "provider-secret-value",
        expect.any(AbortSignal),
      ),
    );
    expect(await screen.findByText("Configured")).toBeTruthy();
    expect(
      (screen.getByLabelText("Provider API key") as HTMLInputElement).value,
    ).toBe("");
  });

  it("switches the complete user copy catalog without changing runtime data", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );
    expect(await screen.findByText("Status")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "简体中文" }));
    expect(await screen.findByText("状态")).toBeTruthy();
    expect(screen.getByText("runtime-instance")).toBeTruthy();
  });

  it("loads the active revision before editing an existing Access", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accessPlan.mockResolvedValue({
      accessId: "existing",
      revision: 4,
      planHash: "d".repeat(64),
      profiles: ["persisted-profile"],
      accountBindings: [
        {
          id: "persisted-account",
          profileId: "persisted-profile",
        },
      ],
    });
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Ready")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Access ID"), {
      target: { value: "existing" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Load active Access" }),
    );
    await waitFor(() =>
      expect(client.accessPlan).toHaveBeenCalledWith(
        "existing",
        expect.any(AbortSignal),
      ),
    );
    expect(
      (screen.getByLabelText("Expected revision") as HTMLInputElement).value,
    ).toBe("4");
    expect(client.credential).toHaveBeenCalledWith(
      "existing",
      "persisted-profile",
      "persisted-account",
      expect.any(AbortSignal),
    );

    fireEvent.change(screen.getByLabelText("Provider API key"), {
      target: { value: "replacement-provider-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save credential" }));
    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        "existing",
        "persisted-profile",
        "persisted-account",
        0,
        "replacement-provider-secret",
        expect.any(AbortSignal),
      ),
    );
  });

  it("replaces an existing credential without reapplying its Access", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.credential.mockResolvedValue({
      credentialId: "work-account",
      profileId: "work-openai",
      secretState: "unavailable",
      secretRevision: 7,
    });
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Ready")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Access ID"), {
      target: { value: "work" },
    });
    expect(
      (screen.getByLabelText("Provider API key") as HTMLInputElement).disabled,
    ).toBe(true);
    fireEvent.click(
      screen.getByRole("button", { name: "Load active Access" }),
    );
    await waitFor(() =>
      expect(client.accessPlan).toHaveBeenCalledWith(
        "work",
        expect.any(AbortSignal),
      ),
    );
    fireEvent.change(screen.getByLabelText("Provider API key"), {
      target: { value: "replacement-provider-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save credential" }));

    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        "work",
        "work-openai",
        "work-account",
        7,
        "replacement-provider-secret",
        expect.any(AbortSignal),
      ),
    );
    expect(client.applyAccess).not.toHaveBeenCalled();
    expect(
      (screen.getByLabelText("Provider API key") as HTMLInputElement).value,
    ).toBe("");
  });
});

describe("the ApprovalCenter and a connection question", () => {
  function askingClient() {
    const client = clientFixture();
    client.approvals.mockResolvedValue({ items: [networkAsk] });
    return client;
  }

  it("names the connection rather than describing it as a tool call", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("api.example.com:443")).toBeTruthy();
    expect(screen.getByText("Destination")).toBeTruthy();
  });

  it("says how many connections one answer is answering for", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(
      await screen.findByText("3 connections are waiting on this answer"),
    ).toBeTruthy();
  });

  it("offers exactly the choices the runtime declared", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("api.example.com:443")).toBeTruthy();
    for (const choice of networkAsk.choices) {
      expect(
        screen.getByRole("button", { name: i18n.t(choice.labelKey) }),
      ).toBeTruthy();
    }
  });

  it("sends the scope of the choice that was taken", async () => {
    const i18n = await createI18n("en-US");
    const client = askingClient();
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("api.example.com:443")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Always allow this host and port" }),
    );
    await waitFor(() =>
      expect(client.decideApproval).toHaveBeenCalledWith(
        networkAsk,
        {
          decision: "allow-once",
          scope: "host_port",
          labelKey: "approval.networkAsk.choice.allowHostPort",
        },
        expect.any(AbortSignal),
      ),
    );
  });

  it("reports a stale answer rather than retrying it", async () => {
    const i18n = await createI18n("en-US");
    const client = askingClient();
    client.decideApproval.mockRejectedValue(
      new ControlProblem(409, "revision_conflict", "error.revision_conflict"),
    );
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("api.example.com:443")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Refuse this connection" }),
    );
    await waitFor(() => expect(client.decideApproval).toHaveBeenCalledTimes(1));
    expect(
      await screen.findByText("The state changed. Refresh and try again."),
    ).toBeTruthy();
  });
});

describe("the audit panels", () => {
  it("shows what connected where, and whether it was read", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("files.example.com:443")).toBeTruthy();
    expect(screen.getByText("Forwarded without reading")).toBeTruthy();
    expect(screen.getByText("2048 sent · 16384 received")).toBeTruthy();
  });

  it("distinguishes a refused connection from an allowed one", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("unknown.example.com:443")).toBeTruthy();
    expect(screen.getByText("Refused · default.ask")).toBeTruthy();
    expect(screen.getByText("Allowed · allow.files")).toBeTruthy();
  });

  it("shows where each request actually went", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(
      await screen.findByText("https://api.anthropic.com:443"),
    ).toBeTruthy();
    expect(screen.getByText("Model request")).toBeTruthy();
    // An attempt that has not finished has no outcome and no final counts to
    // report, so it says so rather than reporting a zero.
    expect(screen.getByText("Still going")).toBeTruthy();
    expect(screen.getByText("Completed")).toBeTruthy();
  });

  it("renders no request content, because the records carry none", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("files.example.com:443")).toBeTruthy();
    const rendered = container.textContent ?? "";
    for (const forbidden of ["/v1/messages", "Authorization", "sk-", "Bearer"]) {
      expect(rendered.includes(forbidden)).toBe(false);
    }
  });
});

describe("a failure a person can act on", () => {
  const failed = {
    sequence: 2,
    id: "activity-failed",
    occurredAt: "2026-08-02T10:00:00Z",
    kind: "exchange.completed",
    accessId: "work",
    subjectId: "exchange-1",
    status: "failed",
    reasonCode: "invalid_exchange_request",
    diagnosis: {
      clientPath: "$.messages[1].role",
    },
  };

  it("says where in the request it failed", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.activities.mockResolvedValue({ items: [failed] });
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("$.messages[1].role")).toBeTruthy();
    expect(screen.getByText("invalid_exchange_request")).toBeTruthy();
    expect(screen.getByText("Where in the request")).toBeTruthy();
  });

  it("shows nothing extra when there is nothing to diagnose", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Ready")).toBeTruthy();
    expect(screen.queryByText("Where in the request")).toBeNull();
  });
});

describe("what is captured", () => {
  it("says whether anything has actually gone through a run", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("claude")).toBeTruthy();
    expect(screen.getByText("Seen going through vibermate")).toBeTruthy();
    expect(screen.getByText("Nothing seen yet")).toBeTruthy();
  });

  it("warns about a client this build has no evidence for", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardModel(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("codex")).toBeTruthy();
    expect(
      screen.getByText(
        "This build has no release evidence for this version, so it was " +
          "started without a trust root and its requests will fail to connect.",
      ),
    ).toBeTruthy();
  });

  it("says nothing about a client it does recognize", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.captureRuns.mockResolvedValue({
      items: [
        {
          id: "run-verified",
          executableLabel: "claude",
          cwd: "/tmp",
          state: "attached",
          observation: "observed",
          recognition: "verified",
          createdAt: "2026-08-02T10:00:00Z",
          expiresAt: "2026-08-02T11:00:00Z",
        },
      ],
    });
    const model = new DashboardModel(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("claude")).toBeTruthy();
    expect(screen.queryByText("Client")).toBeNull();
  });
});

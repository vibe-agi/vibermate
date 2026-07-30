import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import { Dashboard } from "../src/App.tsx";
import type { ControlClient } from "../src/control-client.ts";
import { DashboardModel } from "../src/dashboard-model.ts";
import type {
  AccessApplyInput,
  ApprovalView,
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

const approval: ApprovalView = {
  id: "approval-safe-view",
  revision: 1,
  kind: "tool-intent",
  state: "pending",
  risk: "high",
  titleKey: "approval.toolIntent.title",
  summaryKey: "approval.toolIntent.summary",
  exchangeId: "exchange-id",
  accessId: "work",
  planRevision: 1,
  planHash: "a".repeat(64),
  toolCallIds: ["call-safe-id"],
  toolNames: ["read_file"],
  choices: [
    { decision: "allow-once", scope: "request" },
    { decision: "deny", scope: "request" },
  ],
  createdAt: "2026-07-29T00:00:00Z",
  expiresAt: "2099-07-30T00:00:00Z",
};

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
    decideApproval: vi.fn(
      async (
        _approval: ApprovalView,
        _decision: "allow-once" | "deny",
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
    expect(screen.getByText("read_file")).toBeTruthy();
    expect(screen.queryByText("raw-secret-tool-arguments")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Enter offline hold" }));
    await waitFor(() => expect(client.enterOfflineHold).toHaveBeenCalledWith(1, expect.any(AbortSignal)));

    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    await waitFor(() =>
      expect(client.decideApproval).toHaveBeenCalledWith(
        approval,
        "deny",
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

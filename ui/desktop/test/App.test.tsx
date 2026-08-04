import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory } from "@tanstack/react-router";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import {
  DashboardRouterProvider,
  createDashboardRouter,
} from "../src/app-router.tsx";
import { ControlProblem } from "../src/control-client.ts";
import type { ControlClient } from "../src/control-client.ts";
import { DashboardQueryRuntime } from "../src/dashboard-runtime.ts";
import approvalSamples from "../src/generated/samples/approvals.json" with { type: "json" };
import captureRunSamples from "../src/generated/samples/capture-runs.json" with { type: "json" };
import connectionSamples from "../src/generated/samples/connections.json" with { type: "json" };
import egressSamples from "../src/generated/samples/egress-attempts.json" with { type: "json" };
import type {
  AccessApplyInput,
  AccessApplyResponse,
  AccessDetail,
  AccessDirectoryItem,
  AccessDirectoryPage,
  AccessPlanSummary,
  ActivityRecord,
  ApprovalChoice,
  ApprovalKind,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  EgressAttemptRecord,
  ExchangeDetail,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
  WorkspaceRouteBinding,
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
const clientRootAsk = sampleOfKind("client_root_ask");

function Dashboard({
  initialEntry = "/overview",
  model,
  persistNavigation,
}: {
  readonly initialEntry?: string;
  readonly model: DashboardQueryRuntime;
  readonly persistNavigation?: (locator: string) => Promise<void>;
}) {
  const [router] = useState(() =>
    createDashboardRouter(
      createMemoryHistory({ initialEntries: [initialEntry] }),
      { model, preview: false },
    ),
  );
  return (
    <DashboardRouterProvider
      model={model}
      {...(persistNavigation === undefined ? {} : { persistNavigation })}
      router={router}
    />
  );
}

async function waitForDashboard(): Promise<void> {
  expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
}

async function openView(name: RegExp | string): Promise<void> {
  const link = screen.getByRole("link", { name });
  fireEvent.click(link);
  await waitFor(() => expect(link.getAttribute("aria-current")).toBe("page"));
}

interface AccessUpstreamFixture {
  readonly accountId: string;
  readonly accountLabel: string;
  readonly fixedModel: string;
  readonly name: string;
  readonly origin: string;
  readonly profileId: string;
  readonly protocol: AccessDetail["providerTargets"][number]["protocol"];
  readonly targetId: string;
}

const workAccess: AccessDirectoryItem = {
  accessId: "work",
  name: "Work Claude",
  description: "Primary work connection",
  status: "enabled",
  revision: 4,
  clientOrigin: "https://api.anthropic.com",
  clientDialect: "anthropic-messages",
};

const workspaceRoute: WorkspaceRouteBinding = {
  id: "Z".repeat(43),
  accessId: "work",
  machineId: "M".repeat(43),
  machineShortId: "M".repeat(10),
  machineDisplayName: `Local machine ${"M".repeat(10)}`,
  machineRegistrationRevision: 1,
  workspaceId: "W".repeat(43),
  workspaceLabel: "vibermate",
  workspaceEvidence: "local_launcher",
  profileId: "work-primary",
  revision: 1,
  state: "active",
  activeRunCount: 2,
  activeRuns: [
    {
      runId: "run-alice",
      clientLabel: "claude",
      localUserLabel: "alice",
      state: "active",
      startedAt: "2026-08-03T08:00:00Z",
      lastActivityAt: "2026-08-03T08:00:01Z",
    },
    {
      runId: "run-bob",
      clientLabel: "claude",
      localUserLabel: "bob",
      state: "idle",
      startedAt: "2026-08-03T08:00:00Z",
      lastActivityAt: "2026-08-03T08:00:01Z",
    },
  ],
  pinnedRequestCount: 0,
  approvedProfiles: [
    {
      profileId: "work-primary",
      label: "001",
      modelPresentation: "gpt-5.6-sol",
      authPresentation: "vibermate_account",
      authLabel: "001",
      available: true,
    },
    {
      profileId: "work-backup",
      label: "002",
      modelPresentation: "claude-sonnet-4-5",
      authPresentation: "vibermate_account",
      authLabel: "002",
      available: true,
    },
  ],
  updatedAt: "2026-08-03T08:00:00Z",
};

function accessDetailFixture(
  item: AccessDirectoryItem,
  upstreams: readonly AccessUpstreamFixture[] = [
    {
      accountId: `${item.accessId}-account`,
      accountLabel: item.name,
      fixedModel: "dashscope:glm-5",
      name: item.name,
      origin: "http://127.0.0.1:23333/v1",
      profileId: `${item.accessId}-openai`,
      protocol: "openai-chat",
      targetId: `${item.accessId}-target`,
    },
  ],
): AccessDetail {
  return {
    revision: item.revision,
    access: {
      id: item.accessId,
      name: item.name,
      description: item.description,
      status: item.status,
      agentEndpointId: `${item.accessId}-agent`,
      defaultRouteSetId: `${item.accessId}-routes`,
      profileIds: upstreams.map(({ profileId }) => profileId),
      egressPolicyId: `${item.accessId}-egress`,
    },
    agentEndpoint: {
      id: `${item.accessId}-agent`,
      clientOrigin: item.clientOrigin,
      clientDialect: item.clientDialect,
    },
    profiles: upstreams.map((upstream) => ({
      id: upstream.profileId,
      name: upstream.name,
      description: `${upstream.name} configuration`,
      backendDialect: upstream.protocol,
      targetId: upstream.targetId,
      transportProfileRef: "observed-client-strict-h1",
      defaultModelPolicy: {
        mode: "fixed",
        fixedModel: upstream.fixedModel,
      },
      accountBindingIds: [upstream.accountId],
      defaultAccountBindingId: upstream.accountId,
    })),
    providerTargets: upstreams.map((upstream) => ({
      id: upstream.targetId,
      profileId: upstream.profileId,
      origin: upstream.origin,
      protocol: upstream.protocol,
      capabilities: ["messages", "streaming", "tool_calls"],
    })),
    accountBindings: upstreams.map((upstream) => ({
      id: upstream.accountId,
      profileId: upstream.profileId,
      label: upstream.accountLabel,
      authDriverRef: "static_header",
      enabled: true,
      secretHandling: "preserve_existing",
    })),
    routeSets: [
      {
        id: `${item.accessId}-routes`,
        candidateProfileIds: upstreams.map(({ profileId }) => profileId),
        fallback: "disabled",
      },
    ],
    egressPolicy: {
      id: `${item.accessId}-egress`,
      mode: "direct",
    },
    pluginPlan: {
      mode: "pass_through",
      bindingIds: [],
    },
  };
}

function accessPlanFixture(detail: AccessDetail): AccessPlanSummary {
  return {
    accessId: detail.access.id,
    revision: detail.revision,
    planHash: "c".repeat(64),
    profiles: detail.profiles.map(({ id }) => id),
    accountBindings: detail.accountBindings.map(({ id, profileId }) => ({
      id,
      profileId,
    })),
  };
}

function clientFixture() {
  const workDetail = accessDetailFixture(workAccess);
  return {
    close: vi.fn(),
    status: vi.fn(async (_signal?: AbortSignal) => status),
    offlineHold: vi.fn(async (_signal?: AbortSignal) => offline),
    enterOfflineHold: vi.fn(
      async (_revision: number, _signal?: AbortSignal) => ({
        ...offline,
        state: "held" as const,
        revision: 2,
      }),
    ),
    resumeOfflineHold: vi.fn(
      async (_revision: number, _signal?: AbortSignal) => ({
        ...offline,
        revision: 2,
      }),
    ),
    activities: vi.fn(async (_cursor?: string, _signal?: AbortSignal) => ({
      items: [
        {
          id: "exchange-id",
          occurredAt: "2026-07-29T00:00:00Z",
          accessId: "work",
          status: "succeeded",
        },
      ],
    })),
    exchange: vi.fn(async (exchangeId: string): Promise<ExchangeDetail> => ({
      id: exchangeId,
      accessId: "work",
      status: "failed",
      processingTrace: {
        pluginRunIds: [],
        attemptIds: ["attempt-1"],
        result: "provider_transport_failed",
      },
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
    accesses: vi.fn(
      async (_signal?: AbortSignal): Promise<AccessDirectoryPage> => ({
        items: [workAccess],
      }),
    ),
    access: vi.fn(
      async (
        accessId: string,
        _signal?: AbortSignal,
      ): Promise<AccessDetail> => {
        if (accessId !== workAccess.accessId) {
          throw new Error("Access detail fixture is unavailable");
        }
        return workDetail;
      },
    ),
    addAccessCandidate: vi.fn(async () => ({
      outcome: "committed" as const,
      revision: 5,
      applicationState: "active" as const,
      planHash: "d".repeat(64),
      candidate: {
        profileId: "work-anthropic-secondary",
        credentialId: "work-anthropic-secondary-account",
      },
    })),
    selectAccessCandidate: vi.fn(async () => ({
      outcome: "committed" as const,
      revision: 5,
      applicationState: "active" as const,
      planHash: "d".repeat(64),
    })),
    applyAccess: vi.fn(
      async (
        _accessId: string,
        _input: AccessApplyInput,
        _signal?: AbortSignal,
      ): Promise<AccessApplyResponse> => ({
        outcome: "committed" as const,
        revision: 1,
        applicationState: "active" as const,
        planHash: "b".repeat(64),
      }),
    ),
    accessPlan: vi.fn(
      async (accessId: string): Promise<AccessPlanSummary> => {
        if (accessId !== workAccess.accessId) {
          throw new Error("Access plan fixture is unavailable");
        }
        return accessPlanFixture(workDetail);
      },
    ),
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

async function configureOpenAIDestination(
  apiKey = "provider-secret-value",
  name?: string,
): Promise<void> {
  const destination = screen.getByRole("button", {
    name: /^OpenAI API/u,
  });
  fireEvent.click(destination);
  await waitFor(() =>
    expect(destination.getAttribute("aria-pressed")).toBe("true"),
  );
  if (name !== undefined) {
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: name },
    });
  }
  fireEvent.change(screen.getByLabelText("API Key"), {
    target: { value: apiKey },
  });
}

describe("Desktop dashboard", () => {
  it("offers only canonical Router locations to the Desktop host", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    const persistNavigation = vi.fn(async (_locator: string) => undefined);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} persistNavigation={persistNavigation} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await waitFor(() =>
      expect(persistNavigation).toHaveBeenCalledWith("overview"),
    );
    await openView("Activity");
    await waitFor(() =>
      expect(persistNavigation).toHaveBeenCalledWith("activity"),
    );
    await openView(/^Policy/);
    await waitFor(() =>
      expect(persistNavigation).toHaveBeenCalledWith("policies/approvals"),
    );
  });

  it("drives hold, approval, and complete Access mutations through one client", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accesses.mockResolvedValue({ items: [] });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    expect(screen.getByText("read_file, list_directory")).toBeTruthy();
    expect(screen.queryByText("raw-secret-tool-arguments")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Enter offline hold" }));
    await waitFor(() =>
      expect(client.enterOfflineHold).toHaveBeenCalledWith(
        1,
        expect.any(AbortSignal),
      ),
    );

    await openView(/^Policy/);
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

    await openView("AI Access");
    await screen.findByText("No AI access has been configured yet.");
    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    expect(
      screen.getByRole("button", { name: /^Claude Code/u }).getAttribute(
        "aria-pressed",
      ),
    ).toBe("true");
    await configureOpenAIDestination("provider-secret-value", "Work");
    fireEvent.click(screen.getByRole("button", { name: "Save and enable" }));
    await waitFor(() => expect(client.applyAccess).toHaveBeenCalledTimes(1));
    const [accessId, input] = client.applyAccess.mock.calls[0] ?? [];
    expect(accessId).toMatch(/^access-/u);
    expect(input?.access.id).toBe(accessId);
    expect(input?.agentEndpoint.clientDialect).toBe("anthropic-messages");
    expect(input?.profiles[0]?.backendDialect).toBe("openai-chat");
    expect(input?.profiles[0]?.transportProfileRef).toBe(
      "observed-client-strict-h1",
    );
    expect(input?.accountBindings[0]?.secretRef).toBe(
      `secret://provider/${accessId}-account`,
    );
    expect(input?.pluginPlan.bindingIds).toEqual([]);
    expect(screen.queryByLabelText("Access ID")).toBeNull();
    expect(screen.queryByText(accessId ?? "")).toBeNull();
    expect(
      await screen.findByText("The connection was saved and enabled."),
    ).toBeTruthy();

    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        accessId,
        `${accessId}-openai`,
        `${accessId}-account`,
        0,
        "provider-secret-value",
        expect.any(AbortSignal),
      ),
    );
    expect(
      await screen.findByText("Configured", { selector: ".credential-state" }),
    ).toBeTruthy();
    expect(
      (screen.getByLabelText("API Key") as HTMLInputElement).value,
    ).toBe("");
  });

  it("keeps a committed Access visible when its active plan is unavailable", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accesses.mockResolvedValue({ items: [] });
    client.applyAccess.mockImplementation(
      async (): Promise<AccessApplyResponse> => ({
        outcome: "committed",
        revision: 1,
        applicationState: "unavailable",
      }),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await screen.findByText("No AI access has been configured yet.");
    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    await configureOpenAIDestination("provider-secret-value", "Work");
    fireEvent.click(screen.getByRole("button", { name: "Save and enable" }));

    expect(
      await screen.findByText(
        "The connection was saved, but VibeMate needs to restart before it can be used.",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByText("The connection was saved and enabled."),
    ).toBeNull();
    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledTimes(1),
    );
  });

  it("keeps the API Key available for an explicit retry when saving it fails", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accesses.mockResolvedValue({ items: [] });
    client.replaceCredentialSecret
      .mockRejectedValueOnce(new Error("credential write unavailable"))
      .mockResolvedValueOnce({
        credentialId: "created-account",
        profileId: "created-profile",
        secretState: "configured",
        secretRevision: 1,
      });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await screen.findByText("No AI access has been configured yet.");
    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    await configureOpenAIDestination("retry-this-key", "Work");
    fireEvent.click(screen.getByRole("button", { name: "Save and enable" }));

    expect(
      await screen.findByText(
        "The connection was saved, but the API Key was not",
      ),
    ).toBeTruthy();
    expect((screen.getByLabelText("API Key") as HTMLInputElement).value).toBe(
      "retry-this-key",
    );
    fireEvent.click(screen.getByRole("button", { name: "Save API Key" }));
    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledTimes(2),
    );
    await waitFor(() =>
      expect(
        screen.queryByText(
          "The connection was saved, but the API Key was not",
        ),
      ).toBeNull(),
    );
    expect((screen.getByLabelText("API Key") as HTMLInputElement).value).toBe(
      "",
    );
  });

  it("keeps an unrelated capture outage off the AI Access setup page", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accesses.mockResolvedValue({ items: [] });
    client.captureRuns.mockRejectedValue(new Error("capture unavailable"));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    expect(
      await screen.findByText("No AI access has been configured yet."),
    ).toBeTruthy();
    await waitFor(() => expect(client.captureRuns).toHaveBeenCalled());
    expect(screen.getByText("Set up and manage AI connections")).toBeTruthy();
    expect(
      screen.queryByText(/Some information is unavailable/u),
    ).toBeNull();
    expect(screen.queryByText("Current information is unavailable.")).toBeNull();
  });

  it("locks the new Access form while an apply result is in flight", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.accesses.mockResolvedValue({ items: [] });
    let finishApply: ((result: AccessApplyResponse) => void) | undefined;
    client.applyAccess.mockImplementation(
      () =>
        new Promise<AccessApplyResponse>((resolve) => {
          finishApply = resolve;
        }),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await screen.findByText("No AI access has been configured yet.");
    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    const name = screen.getByLabelText("Name") as HTMLInputElement;
    await configureOpenAIDestination("provider-secret-value", "Work");
    fireEvent.click(screen.getByRole("button", { name: "Save and enable" }));

    await waitFor(() => expect(client.applyAccess).toHaveBeenCalledTimes(1));
    expect(name.disabled).toBe(true);
    expect(
      (
        screen.getByRole("button", {
          name: "Save and enable",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    await act(async () => {
      finishApply?.({
        outcome: "committed",
        revision: 1,
        applicationState: "active",
        planHash: "b".repeat(64),
      });
    });
    expect(
      await screen.findByText("The connection was saved and enabled."),
    ).toBeTruthy();
  });

  it("never presents internal Access IDs or revision controls", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    const existing = await screen.findByRole("button", {
      name: /^Work Claude/u,
    });
    expect(screen.queryByLabelText("Access ID")).toBeNull();
    expect(screen.queryByLabelText("Expected revision")).toBeNull();
    expect(screen.queryByText("work", { exact: true })).toBeNull();

    fireEvent.click(existing);
    await waitFor(() =>
      expect(client.access).toHaveBeenCalledWith(
        "work",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() => expect(client.accessPlan).toHaveBeenCalledTimes(1));
    expect(client.access.mock.invocationCallOrder[0]).toBeLessThan(
      client.accessPlan.mock.invocationCallOrder[0] ?? Number.MAX_SAFE_INTEGER,
    );
    expect(screen.queryByLabelText("Access ID")).toBeNull();
    expect(screen.queryByLabelText("Expected revision")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    expect(
      (screen.getByLabelText("Client protocol") as HTMLSelectElement).value,
    ).toBe("openai-responses");
    expect(
      (screen.getByLabelText("Client API address") as HTMLInputElement).value,
    ).toBe("https://api.openai.com");
    await configureOpenAIDestination("personal-provider-secret", "Personal");
    expect(screen.queryByLabelText("Access ID")).toBeNull();
    expect(screen.queryByLabelText("Expected revision")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save and enable" }));
    await waitFor(() => expect(client.applyAccess).toHaveBeenCalledTimes(1));
    const generatedId = client.applyAccess.mock.calls[0]?.[0];
    const createdInput = client.applyAccess.mock.calls[0]?.[1];
    expect(generatedId).toMatch(/^access-/u);
    expect(createdInput?.agentEndpoint).toMatchObject({
      clientDialect: "openai-responses",
      clientOrigin: "https://api.openai.com",
    });
    expect(document.body.textContent).not.toContain(generatedId);
    expect(screen.queryByLabelText("Access ID")).toBeNull();
    expect(screen.queryByLabelText("Expected revision")).toBeNull();
  });

  it("opens an existing tool instead of creating a competing client API address", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await screen.findByRole("button", { name: /^Work Claude/u });
    fireEvent.click(screen.getByRole("button", { name: "Add AI Access" }));
    fireEvent.click(
      screen.getByRole("button", {
        name: /^Claude Code.*Already added as Work Claude/u,
      }),
    );
    await waitFor(() =>
      expect(client.access).toHaveBeenCalledWith(
        "work",
        expect.any(AbortSignal),
      ),
    );
    expect(await screen.findByRole("heading", { name: "Work Claude" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save and enable" })).toBeNull();
    expect(client.applyAccess).not.toHaveBeenCalled();
  });

  it("switches the complete user copy catalog without exposing runtime identity", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );
    await waitForDashboard();
    await openView("Settings");
    expect(screen.getByText("Status")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "简体中文" }));
    expect(await screen.findByText("状态")).toBeTruthy();
    expect(screen.queryByText("runtime-instance")).toBeNull();
  });

  it("lists multiple Accesses and automatically loads a protected existing one", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const existingAccess: AccessDirectoryItem = {
      accessId: "existing",
      name: "Existing Access",
      description: "Persisted complete configuration",
      status: "enabled",
      revision: 4,
      clientOrigin: "https://api.anthropic.com",
      clientDialect: "anthropic-messages",
    };
    const personalAccess: AccessDirectoryItem = {
      accessId: "personal",
      name: "Personal Access",
      description: "A separate personal connection",
      status: "enabled",
      revision: 2,
      clientOrigin: "https://api.openai.com",
      clientDialect: "openai-responses",
    };
    const existingDetail = accessDetailFixture(existingAccess, [
      {
        accountId: "persisted-account",
        accountLabel: "Primary account",
        fixedModel: "gpt-5.4",
        name: "Primary OpenAI",
        origin: "https://primary.example/v1",
        profileId: "persisted-profile",
        protocol: "openai-chat",
        targetId: "persisted-target",
      },
      {
        accountId: "fallback-account",
        accountLabel: "Fallback account",
        fixedModel: "gpt-5-mini",
        name: "Fallback Responses",
        origin: "https://fallback.example/v1",
        profileId: "fallback-profile",
        protocol: "openai-responses",
        targetId: "fallback-target",
      },
    ]);
    client.accesses.mockResolvedValue({
      items: [existingAccess, personalAccess],
    });
    client.access.mockResolvedValue(existingDetail);
    client.accessPlan.mockResolvedValue(accessPlanFixture(existingDetail));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("AI Access");
    const existing = await screen.findByRole("button", {
      name: /^Existing Access/u,
    });
    expect(
      screen.getByRole("button", { name: /^Personal Access/u }),
    ).toBeTruthy();
    fireEvent.click(existing);
    await waitFor(() =>
      expect(client.access).toHaveBeenCalledWith(
        "existing",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(client.accessPlan).toHaveBeenCalledWith(
        "existing",
        expect.any(AbortSignal),
      ),
    );
    expect(client.access.mock.invocationCallOrder[0]).toBeLessThan(
      client.accessPlan.mock.invocationCallOrder[0] ?? Number.MAX_SAFE_INTEGER,
    );
    await waitFor(() =>
      expect(client.credential).toHaveBeenCalledWith(
        "existing",
        "persisted-profile",
        "persisted-account",
        expect.any(AbortSignal),
      ),
    );
    expect(screen.queryByLabelText("Name")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Apply Access" }),
    ).toBeNull();
    expect(
      screen.getByRole("heading", { name: "Accounts and routes" }),
    ).toBeTruthy();
    expect(
      document.querySelectorAll(".access-upstream-list li"),
    ).toHaveLength(2);
    expect(screen.getByText("Primary OpenAI")).toBeTruthy();
    expect(screen.getByText("https://primary.example/v1")).toBeTruthy();
    expect(screen.getByText("Model: gpt-5.4")).toBeTruthy();
    expect(screen.getByText("Account: Primary account")).toBeTruthy();
    expect(screen.getByText("Fallback Responses")).toBeTruthy();
    expect(screen.getByText("https://fallback.example/v1")).toBeTruthy();
    expect(screen.getByText("Model: gpt-5-mini")).toBeTruthy();
    expect(screen.getByText("Account: Fallback account")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("API Key"), {
      target: { value: "replacement-provider-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save API Key" }));
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
    expect(client.applyAccess).not.toHaveBeenCalled();
  });

  it.each([
    [
      "draft",
      "This Access is a draft",
      "Its configuration is saved but has not been enabled, so it is not handling traffic.",
    ],
    [
      "disabled",
      "This Access is disabled",
      "Its configuration and history are preserved, but new traffic will not use it. Enablement editing is not available in this build yet.",
    ],
  ] as const)(
    "renders a saved %s Access without loading an active plan or credential",
    async (statusValue, inactiveTitle, inactiveDetail) => {
      const i18n = await createI18n("en-US");
      const client = clientFixture();
      const item: AccessDirectoryItem = {
        ...workAccess,
        name: statusValue === "draft" ? "Draft Access" : "Disabled Access",
        status: statusValue,
      };
      client.accesses.mockResolvedValue({ items: [item] });
      client.access.mockResolvedValue(accessDetailFixture(item));
      const model = new DashboardQueryRuntime(client, 60_000);
      render(
        <I18nextProvider i18n={i18n}>
          <Dashboard initialEntry="/access" model={model} />
        </I18nextProvider>,
      );

      await waitForDashboard();
      fireEvent.click(
        await screen.findByRole("button", {
          name: new RegExp(`^${item.name}`, "u"),
        }),
      );

      expect(await screen.findByText(inactiveTitle)).toBeTruthy();
      expect(screen.getByText(inactiveDetail)).toBeTruthy();
      expect(client.access).toHaveBeenCalledWith(
        item.accessId,
        expect.any(AbortSignal),
      );
      expect(client.accessPlan).not.toHaveBeenCalled();
      expect(client.credential).not.toHaveBeenCalled();
      expect(
        screen.getByRole("heading", { name: "Accounts and routes" }),
      ).toBeTruthy();
      expect(screen.getByText("http://127.0.0.1:23333/v1")).toBeTruthy();
      expect(screen.queryByLabelText("API Key")).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Add account or route" }),
      ).toBeNull();
    },
  );

  it("adds a named provider route, saves its key, and only then makes it current", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Claude/u }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Add account or route" }),
    );
    expect(
      screen.getByRole("button", { name: /^Anthropic official/u }).getAttribute(
        "aria-pressed",
      ),
    ).toBe("true");
    expect(
      screen.getByRole("button", { name: /^OpenAI-compatible service/u }),
    ).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Route name"), {
      target: { value: "Personal Anthropic" },
    });
    fireEvent.change(screen.getByLabelText("Model name"), {
      target: { value: "claude-sonnet-4-5" },
    });
    fireEvent.change(screen.getByLabelText("API Key / Token"), {
      target: { value: "personal-provider-secret" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add and use this route" }),
    );

    await waitFor(() =>
      expect(client.addAccessCandidate).toHaveBeenCalledWith(
        "work",
        4,
        {
          model: "claude-sonnet-4-5",
          name: "Personal Anthropic",
          provider: "anthropic",
        },
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        "work",
        "work-anthropic-secondary",
        "work-anthropic-secondary-account",
        0,
        "personal-provider-secret",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(client.selectAccessCandidate).toHaveBeenCalledWith(
        "work",
        "work-anthropic-secondary",
        5,
        expect.any(AbortSignal),
      ),
    );
    expect(client.addAccessCandidate.mock.invocationCallOrder[0]).toBeLessThan(
      client.replaceCredentialSecret.mock.invocationCallOrder.at(-1) ??
        Number.MAX_SAFE_INTEGER,
    );
    expect(
      client.replaceCredentialSecret.mock.invocationCallOrder.at(-1),
    ).toBeLessThan(
      client.selectAccessCandidate.mock.invocationCallOrder[0] ??
        Number.MAX_SAFE_INTEGER,
    );
    expect(client.applyAccess).not.toHaveBeenCalled();
  });

  it("recovers a staged route after restart without adding it a second time", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const stagedDetail = accessDetailFixture(workAccess, [
      {
        accountId: "work-primary-account",
        accountLabel: "Work account",
        fixedModel: "claude-sonnet-4-5",
        name: "Work account",
        origin: "https://api.anthropic.com",
        profileId: "work-primary",
        protocol: "anthropic-messages",
        targetId: "work-primary-target",
      },
      {
        accountId: "work-staged-account",
        accountLabel: "Relay A",
        fixedModel: "claude-sonnet-4-5",
        name: "Relay A",
        origin: "https://relay.example/v1",
        profileId: "work-staged",
        protocol: "anthropic-messages",
        targetId: "work-staged-target",
      },
    ]);
    const detailWithStagedRoute: AccessDetail = {
      ...stagedDetail,
      accountBindings: stagedDetail.accountBindings.map((binding) =>
        binding.id === "work-staged-account"
          ? { ...binding, enabled: false }
          : binding,
      ),
      routeSets: stagedDetail.routeSets.map((routeSet) => ({
        ...routeSet,
        candidateProfileIds: ["work-primary"],
      })),
    };
    client.access.mockResolvedValue(detailWithStagedRoute);
    client.accessPlan.mockResolvedValue(accessPlanFixture(detailWithStagedRoute));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Claude/u }),
    );
    expect(await screen.findByText("Needs key/token")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Finish setup" }),
    );
    expect(screen.getByLabelText("Route name").getAttribute("disabled")).not.toBeNull();
    expect(screen.getByLabelText("Route name").getAttribute("value")).toBe(
      "Relay A",
    );
    fireEvent.change(screen.getByLabelText("API Key / Token"), {
      target: { value: "staged-provider-secret" },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: "Save key/token and use this route",
      }),
    );

    await waitFor(() =>
      expect(client.replaceCredentialSecret).toHaveBeenCalledWith(
        "work",
        "work-staged",
        "work-staged-account",
        0,
        "staged-provider-secret",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(client.selectAccessCandidate).toHaveBeenCalledWith(
        "work",
        "work-staged",
        4,
        expect.any(AbortSignal),
      ),
    );
    expect(client.addAccessCandidate).not.toHaveBeenCalled();
  });

  it("does not select a newly added route until its key has been saved", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.replaceCredentialSecret.mockRejectedValue(
      new Error("secret store unavailable"),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Claude/u }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Add account or route" }),
    );
    expect(screen.getByLabelText("Route name").getAttribute("value")).toBe(
      "Anthropic account 2",
    );
    expect(screen.getByLabelText("Model name").getAttribute("value")).toBe(
      "claude-sonnet-4-5",
    );
    fireEvent.change(screen.getByLabelText("API Key / Token"), {
      target: { value: "unsaved-provider-secret" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Add and use this route" }),
    );

    expect(
      await screen.findByText("The API key/token was not saved"),
    ).toBeTruthy();
    expect(client.addAccessCandidate).toHaveBeenCalledTimes(1);
    expect(client.selectAccessCandidate).not.toHaveBeenCalled();
    expect(screen.getByLabelText("API Key / Token").getAttribute("value")).toBe(
      "unsaved-provider-secret",
    );
    expect(
      screen.getByRole("button", {
        name: "Save key/token and use this route",
      }),
    ).toBeTruthy();
  });

  it("offers OpenAI account routes for a Codex Access without Claude-only choices", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const codexAccess: AccessDirectoryItem = {
      ...workAccess,
      accessId: "codex-work",
      clientDialect: "openai-responses",
      clientOrigin: "https://api.openai.com",
      name: "Work Codex",
    };
    const codexDetail = accessDetailFixture(codexAccess, [
      {
        accountId: "codex-work-account",
        accountLabel: "OpenAI account 1",
        fixedModel: "gpt-5",
        name: "OpenAI account 1",
        origin: "https://api.openai.com/v1",
        profileId: "codex-work-profile",
        protocol: "openai-chat",
        targetId: "codex-work-target",
      },
    ]);
    client.accesses.mockResolvedValue({ items: [codexAccess] });
    client.access.mockResolvedValue(codexDetail);
    client.accessPlan.mockResolvedValue(accessPlanFixture(codexDetail));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/access" model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Codex/u }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Add account or route" }),
    );

    expect(
      screen.getByRole("button", { name: /^OpenAI official/u }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /^OpenAI-compatible service/u }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: /^Anthropic official/u }),
    ).toBeNull();
    expect(screen.getByLabelText("Route name").getAttribute("value")).toBe(
      "OpenAI account 2",
    );
    expect(screen.getByLabelText("Model name").getAttribute("value")).toBe(
      "gpt-5",
    );
  });

  it("does not present failed credential metadata as a missing credential", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.credential.mockRejectedValue(
      new Error("credential metadata unavailable"),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("AI Access");
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Claude/u }),
    );
    await waitFor(() =>
      expect(client.credential).toHaveBeenCalledWith(
        "work",
        "work-openai",
        "work-account",
        expect.any(AbortSignal),
      ),
    );

    expect(
      await screen.findByText("Current information is unavailable."),
    ).toBeTruthy();
    expect(screen.queryByText("Missing")).toBeNull();
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
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("AI Access");
    fireEvent.click(
      await screen.findByRole("button", { name: /^Work Claude/u }),
    );
    await waitFor(() =>
      expect(client.accessPlan).toHaveBeenCalledWith(
        "work",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(
        (screen.getByLabelText("API Key") as HTMLInputElement)
          .disabled,
      ).toBe(false),
    );
    fireEvent.change(screen.getByLabelText("API Key"), {
      target: { value: "replacement-provider-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save API Key" }));

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
      (screen.getByLabelText("API Key") as HTMLInputElement).value,
    ).toBe("");
  });

  it("does not present a first-read failure as an empty approval queue", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.approvals.mockRejectedValue(new Error("approvals unavailable"));
    const model = new DashboardQueryRuntime(client, 60_000);
    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await waitFor(() =>
      expect(container.querySelector(".pending-link span")?.textContent).toBe(
        "—",
      ),
    );
    await openView(/^Policy/);
    expect(screen.getByText("Current information is unavailable.")).toBeTruthy();
    expect(
      screen.queryByText("Nothing is waiting for your decision."),
    ).toBeNull();
  });

  it("does not present a stale empty approval snapshot as authoritative", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.approvals
      .mockResolvedValueOnce({ items: [] })
      .mockRejectedValue(new Error("approvals unavailable"));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(
      screen.getByText("Nothing is waiting for your decision."),
    ).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(
      await screen.findByText(
        "Showing the last update; current information could not be refreshed.",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByText("Nothing is waiting for your decision."),
    ).toBeNull();
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
    const model = new DashboardQueryRuntime(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(screen.getByText("api.example.com:443")).toBeTruthy();
    expect(screen.getByText("Destination")).toBeTruthy();
  });

  it("says how many connections one answer is answering for", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(
      screen.getByText("3 connections are waiting on this answer"),
    ).toBeTruthy();
  });

  it("offers exactly the choices the runtime declared", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(askingClient(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(screen.getByText("api.example.com:443")).toBeTruthy();
    for (const choice of networkAsk.choices) {
      expect(
        screen.getByRole("button", { name: i18n.t(choice.labelKey) }),
      ).toBeTruthy();
    }
  });

  it("sends the scope of the choice that was taken", async () => {
    const i18n = await createI18n("en-US");
    const client = askingClient();
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(screen.getByText("api.example.com:443")).toBeTruthy();
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
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(screen.getByText("api.example.com:443")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Refuse this connection" }),
    );
    await waitFor(() => expect(client.decideApproval).toHaveBeenCalledTimes(1));
    expect(
      await screen.findByText("The state changed. Refresh and try again."),
    ).toBeTruthy();
  });

  it("names a recognized client Root handoff instead of calling it a tool", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.approvals.mockResolvedValue({ items: [clientRootAsk] });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView(/^Policy/);
    expect(
      screen.getByText("Allow a recognized client to use the local Root?"),
    ).toBeTruthy();
    expect(screen.getByText("Signed application")).toBeTruthy();
    expect(
      screen.getByText("/Applications/Claude.app/Contents/MacOS/claude"),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", {
        name: "Allow this launch to use the VibeMate Root",
      }),
    ).toBeTruthy();
    expect(screen.queryByText("Tools")).toBeNull();
  });
});

describe("the audit panels", () => {
  it("shows what connected where, and whether it was read", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("Activity");
    expect(screen.getByText("files.example.com:443")).toBeTruthy();
    expect(screen.getByText("Forwarded without reading")).toBeTruthy();
    expect(screen.getByText("2048 sent · 16384 received")).toBeTruthy();
  });

  it("distinguishes a refused connection from an allowed one", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("Activity");
    expect(screen.getByText("unknown.example.com:443")).toBeTruthy();
    expect(screen.getByText("Refused · default.ask")).toBeTruthy();
    expect(screen.getByText("Allowed · allow.files")).toBeTruthy();
  });

  it("shows where each request actually went", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("Activity");
    expect(screen.getByText("https://api.anthropic.com:443")).toBeTruthy();
    expect(screen.getByText("Model request")).toBeTruthy();
    // An attempt that has not finished has no outcome and no final counts to
    // report, so it says so rather than reporting a zero.
    expect(screen.getByText("Still going")).toBeTruthy();
    expect(screen.getByText("Completed")).toBeTruthy();
  });

  it("renders no request content, because the records carry none", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    await waitForDashboard();
    await openView("Activity");
    expect(screen.getByText("files.example.com:443")).toBeTruthy();
    const rendered = container.textContent ?? "";
    for (const forbidden of [
      "/v1/messages",
      "Authorization",
      "sk-",
      "Bearer",
    ]) {
      expect(rendered.includes(forbidden)).toBe(false);
    }
  });
});

describe("canonical Activity request summaries", () => {
  const summary: ActivityRecord = {
    id: "exchange-failed",
    occurredAt: "2026-08-02T10:00:00Z",
    accessId: "work",
    status: "reviewed",
  };

  it("renders the real requests route and treats unknown status as neutral raw text", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.activities.mockResolvedValue({ items: [summary] });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests" model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Exchange exchange-failed")).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "Exchange exchange-failed" })
        .getAttribute("href"),
    ).toBe("/activity/requests/exchange-failed");
    expect(screen.getByText("work")).toBeTruthy();
    const unknown = screen.getByText("reviewed");
    expect(unknown.classList.contains("neutral")).toBe(true);
    expect(screen.getByRole("heading", { name: "Requests" })).toBeTruthy();
  });

  it("loads another deduplicated page without rendering legacy raw evidence", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const cursor = "dGFpbC0x";
    client.activities.mockImplementation(async (requestedCursor) => {
      if (requestedCursor === undefined) {
        return { items: [summary], nextCursor: cursor };
      }
      return {
        items: [
          summary,
          {
            accessId: "personal",
            id: "exchange-older",
            occurredAt: "2026-08-02T09:00:00Z",
            status: "failed",
            reasonCode: "raw_provider_reason",
            diagnosis: { clientPath: "$.secret" },
          } as ActivityRecord & {
            readonly diagnosis: { readonly clientPath: string };
            readonly reasonCode: string;
          },
        ],
      };
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests" model={model} />
      </I18nextProvider>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Exchange exchange-older")).toBeTruthy();
    expect(screen.getAllByText("Exchange exchange-failed")).toHaveLength(1);
    expect(client.activities).toHaveBeenCalledWith(
      cursor,
      expect.any(AbortSignal),
    );
    expect(container.textContent).not.toContain("raw_provider_reason");
    expect(container.textContent).not.toContain("$.secret");
  });

  it("shows an honest paging safety notice only on the requests route", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    const cursor = "c2FmZXR5LWN5Y2xl";
    client.activities.mockImplementation(async (requestedCursor) =>
      requestedCursor === undefined
        ? { items: [summary], nextCursor: cursor }
        : {
            items: [
              {
                accessId: "personal",
                id: "exchange-older",
                occurredAt: "2026-08-02T09:00:00Z",
                status: "failed",
              },
            ],
            nextCursor: cursor,
          },
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests" model={model} />
      </I18nextProvider>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Exchange exchange-older")).toBeTruthy();
    expect(
      screen.getByText("Older paging stopped at the safety limit"),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "Older records may still exist on the server. Refreshing re-anchors this bounded view to the latest window.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();

    fireEvent.click(
      screen.getByRole("link", { name: /^Activity$/u }),
    );
    await waitFor(() =>
      expect(
        screen.queryByText("Older paging stopped at the safety limit"),
      ).toBeNull(),
    );
  });

  it("links an honest empty requests page to AI Access", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.activities.mockResolvedValue({ items: [] });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests" model={model} />
      </I18nextProvider>,
    );

    expect(
      await screen.findByText("No request summaries are available yet."),
    ).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Open AI Access" }).getAttribute("href"),
    ).toBe("/access");
    expect(screen.queryByText(/real.?time/iu)).toBeNull();
  });

  it("shows source failure without claiming the request list is empty", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.activities.mockRejectedValue(new Error("activity unavailable"));
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests" model={model} />
      </I18nextProvider>,
    );

    expect(
      await screen.findByText("Current information is unavailable."),
    ).toBeTruthy();
    expect(
      screen.queryByText("No request summaries are available yet."),
    ).toBeNull();
  });

  it("loads the evidence-backed dynamic request detail", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.exchange.mockResolvedValue({
      id: "exchange-failed",
      accessId: "work",
      status: "failed",
      processingTrace: {
        egressProxyId: "company-proxy",
        pluginRunIds: ["plugin-run-1"],
        attemptIds: ["attempt-1", "attempt-2"],
        result: "provider_transport_failed",
      },
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <Dashboard
          initialEntry="/activity/requests/exchange-failed"
          model={model}
        />
      </I18nextProvider>,
    );

    expect(await screen.findByText("provider_transport_failed")).toBeTruthy();
    expect(screen.getByText("company-proxy")).toBeTruthy();
    expect(screen.getByText("attempt-1")).toBeTruthy();
    expect(screen.getByText("attempt-2")).toBeTruthy();
    expect(screen.getByText("plugin-run-1")).toBeTruthy();
    expect(client.exchange).toHaveBeenCalledWith(
      "exchange-failed",
      expect.any(AbortSignal),
    );
    expect(screen.getByRole("heading", { name: "Request evidence" })).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Back to all requests" }).getAttribute("href"),
    ).toBe("/activity/requests");
    expect(container.textContent).not.toContain("Authorization");
    expect(container.textContent).not.toContain("rawBody");
  });

  it("shows a retryable not-found boundary for a missing request", async () => {
    const i18n = await createI18n("en-US");
    const client = clientFixture();
    client.exchange.mockRejectedValue(
      new ControlProblem(404, "exchange_not_found", "error.exchange_not_found"),
    );
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity/requests/missing" model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("This request evidence was not found.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("what is captured", () => {
  it("lists a started workspace before its first request chooses an Access", async () => {
    const i18n = await createI18n("en-US");
    const pendingRun: CaptureRunRecord = {
      ...(captureRunSamples[0] as CaptureRunRecord),
      id: "run-before-first-request",
      localUserLabel: "alice",
      machineId: "M".repeat(43),
      workspaceId: "W".repeat(43),
      workspaceLabel: "vibermate",
      workspaceEvidence: "local_launcher",
      state: "created",
      observation: "waiting_for_traffic",
    };
    const client = Object.assign(clientFixture(), {
      captureRuns: vi.fn(async () => ({ items: [pendingRun] })),
      workspaceRouteBindings: vi.fn(async () => ({ items: [] })),
      updateWorkspaceRouteBinding: vi.fn(),
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity" model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Machines & workspaces")).toBeTruthy();
    expect(screen.getByText("vibermate")).toBeTruthy();
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText("Waiting for first request")).toBeTruthy();
    expect(screen.queryByRole("combobox", {
      name: "Route for new requests",
    })).toBeNull();
  });

  it("counts one workspace when it has routes for multiple Accesses", async () => {
    const i18n = await createI18n("en-US");
    const client = Object.assign(clientFixture(), {
      workspaceRouteBindings: vi.fn(async () => ({
        items: [
          workspaceRoute,
          {
            ...workspaceRoute,
            id: "Y".repeat(43),
            accessId: "personal",
          },
        ],
      })),
      updateWorkspaceRouteBinding: vi.fn(),
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity" model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("1 workspace")).toBeTruthy();
    expect(screen.getAllByText("vibermate")).toHaveLength(2);
    expect(screen.getByText(/AI Access work/u)).toBeTruthy();
    expect(screen.getByText(/AI Access personal/u)).toBeTruthy();
  });

  it("groups concurrent tools by stable machine and workspace and switches new requests", async () => {
    const i18n = await createI18n("en-US");
    const updateWorkspaceRouteBinding = vi.fn(
      async (
        _bindingId: string,
        _expectedRevision: number,
        profileId: string,
      ): Promise<WorkspaceRouteBinding> => ({
        ...workspaceRoute,
        profileId,
        revision: 2,
      }),
    );
    const client = Object.assign(clientFixture(), {
      workspaceRouteBindings: vi.fn(async () => ({
        items: [workspaceRoute],
      })),
      updateWorkspaceRouteBinding,
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard initialEntry="/activity" model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("Machines & workspaces")).toBeTruthy();
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText("bob")).toBeTruthy();
    const route = screen.getByRole("combobox", {
      name: "Route for new requests",
    });
    fireEvent.change(route, { target: { value: "work-backup" } });

    await waitFor(() =>
      expect(updateWorkspaceRouteBinding).toHaveBeenCalledWith(
        workspaceRoute.id,
        1,
        "work-backup",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect((route as HTMLSelectElement).value).toBe("work-backup"),
    );
  });

  it("says whether anything has actually gone through a run", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("claude")).toBeTruthy();
    expect(screen.getByText("Seen going through vibermate")).toBeTruthy();
    expect(screen.getByText("Nothing seen yet")).toBeTruthy();
  });

  it("warns in plain language about an unverified app version", async () => {
    const i18n = await createI18n("en-US");
    const model = new DashboardQueryRuntime(clientFixture(), 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("codex")).toBeTruthy();
    expect(
      screen.getByText(
        "VibeMate has not verified this exact app version, so it was started " +
          "without connection access and its requests will fail.",
      ),
    ).toBeTruthy();
  });

  it("shows verified adapter and recognition evidence separately", async () => {
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
          clientAdapterState: "verified",
          clientRecognition: "verified",
          catalogRevision: 4,
          createdAt: "2026-08-02T10:00:00Z",
          expiresAt: "2026-08-02T11:00:00Z",
        },
      ],
    });
    const model = new DashboardQueryRuntime(client, 60_000);
    render(
      <I18nextProvider i18n={i18n}>
        <Dashboard model={model} />
      </I18nextProvider>,
    );

    expect(await screen.findByText("claude")).toBeTruthy();
    expect(screen.getByText("Verified adapter")).toBeTruthy();
    expect(
      screen.getByText(
        "VibeMate has tested and verified this exact app version.",
      ),
    ).toBeTruthy();
  });
});

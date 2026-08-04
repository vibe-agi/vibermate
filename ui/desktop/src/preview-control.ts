import {
  compareResourceIds,
  ControlProblem,
  type ControlClient,
} from "./control-client.ts";
import { buildAccessApplyInput } from "./access-form.ts";
import type {
  AccessApplyInput,
  AccessAddCandidateInput,
  AccessAddCandidateResponse,
  AccessApplyResponse,
  AccessPlanSummary,
  ActivityRecord,
  ApprovalChoice,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  CredentialView,
  EgressAttemptRecord,
  ExchangeDetail,
  OfflineHoldSnapshot,
  StatusResponse,
  WorkspaceRouteBinding,
} from "./control-types.ts";
import approvalSamples from "./generated/samples/approvals.json" with { type: "json" };
import captureRunSamples from "./generated/samples/capture-runs.json" with { type: "json" };
import connectionSamples from "./generated/samples/connections.json" with { type: "json" };
import egressSamples from "./generated/samples/egress-attempts.json" with { type: "json" };

const previewStartedAt = "2026-08-02T08:42:00Z";
const previewActivityCursor = "cHJldmlldy1wYWdlLTI";

const previewWorkAccess = buildAccessApplyInput({
  accessId: "work",
  name: "Work Claude",
  description: "Primary work connection",
  expectedRevision: "3",
  clientOrigin: "https://api.anthropic.com",
  clientDialect: "anthropic-messages",
  providerDialect: "openai-chat",
  authDriverRef: "static_header",
  providerOrigin: "http://127.0.0.1:23333",
  fixedModel: "dashscope:glm-5",
  routeName: "Work relay",
});

const previewActivities: readonly ActivityRecord[] = [
  {
    id: "exchange-preview-5",
    occurredAt: "2026-08-02T10:01:03Z",
    accessId: "work",
    status: "pending",
  },
  {
    id: "exchange-preview-4",
    occurredAt: "2026-08-02T10:01:00Z",
    accessId: "work",
    status: "succeeded",
  },
  {
    id: "exchange-preview-3",
    occurredAt: "2026-08-02T10:00:03Z",
    accessId: "work",
    status: "failed",
  },
  {
    id: "exchange-preview-2",
    occurredAt: "2026-08-02T09:58:00Z",
    accessId: "personal",
    status: "reviewed",
  },
  {
    id: "exchange-preview-1",
    occurredAt: "2026-08-02T09:55:00Z",
    accessId: "work",
    status: "canceled",
  },
];

function initialOfflineHold(): OfflineHoldSnapshot {
  return {
    state: "online",
    revision: 8,
    since: "2026-08-02T08:42:00Z",
    activeActions: 1,
    enteringActions: 0,
    activeEgress: 1,
    queuedRequests: 0,
    heldBytes: 0,
    safeToDisconnect: false,
    activeByKind: { exchange: 1 },
    queuedByKind: {},
  };
}

class PreviewControlClient implements ControlClient {
  #offline = initialOfflineHold();
  #approvals: ApprovalView[] = (
    approvalSamples as readonly ApprovalView[]
  ).map((item) => ({
      ...item,
      subjectRefs: [...item.subjectRefs],
      subjectLabels: [...item.subjectLabels],
      choices: item.choices.map((choice) => ({ ...choice })),
    }));
  #accesses = new Map([
    ["work", { input: previewWorkAccess, revision: 4 }],
  ]);
  #credentials = new Map<string, CredentialView>([
    [
      "work-openai\u0000work-account",
      {
        credentialId: "work-account",
        profileId: "work-openai",
        secretState: "configured",
        secretRevision: 2,
      },
    ],
  ]);
  #workspaceRoutes: WorkspaceRouteBinding[] = [
    {
      id: "Z".repeat(43),
      accessId: "work",
      machineId: "M".repeat(43),
      machineShortId: "M".repeat(10),
      machineDisplayName: "Null's MacBook",
      machineRegistrationRevision: 1,
      workspaceId: "W".repeat(43),
      workspaceLabel: "vibermate",
      workspaceEvidence: "local_launcher",
      profileId: "work-openai",
      revision: 3,
      state: "active",
      activeRunCount: 3,
      activeRuns: [
        {
          runId: "run-preview-alice-1",
          clientLabel: "Claude Code",
          localUserLabel: "alice",
          state: "active",
          startedAt: "2026-08-02T09:54:00Z",
          lastActivityAt: "2026-08-02T10:01:03Z",
        },
        {
          runId: "run-preview-alice-2",
          clientLabel: "Claude Code",
          localUserLabel: "alice",
          state: "idle",
          startedAt: "2026-08-02T09:57:00Z",
          lastActivityAt: "2026-08-02T10:00:12Z",
        },
        {
          runId: "run-preview-bob-1",
          clientLabel: "Claude Code",
          localUserLabel: "bob",
          state: "active",
          startedAt: "2026-08-02T09:59:00Z",
          lastActivityAt: "2026-08-02T10:01:01Z",
        },
      ],
      pinnedRequestCount: 1,
      approvedProfiles: [
        {
          profileId: "work-openai",
          label: "001 · Work relay",
          modelPresentation: "dashscope:glm-5",
          authPresentation: "vibermate_account",
          authLabel: "001",
          available: true,
        },
        {
          profileId: "work-secondary",
          label: "002 · Backup relay",
          modelPresentation: "gpt-5.6-sol",
          authPresentation: "vibermate_account",
          authLabel: "002",
          available: true,
        },
      ],
      updatedAt: "2026-08-02T10:01:00Z",
    },
  ];

  close(): void {
    // Preview owns no capability or transport; closing remains idempotent.
  }

  async status(_signal?: AbortSignal): Promise<StatusResponse> {
    return {
      generation: "preview-runtime-8f2a",
      ready: true,
      apiVersion: "v1",
      statusKey: "runtime.state.initialized",
      runtime: {
        state: "initialized",
        instanceId: "preview-runtime-8f2a",
        host: "desktop",
        schemaRevision: 26,
        storage: "healthy",
        accessProjection: {
          state: "healthy",
          unavailableAccessCount: 0,
        },
        offlineHold: this.#offline,
        startedAt: previewStartedAt,
      },
    };
  }

  async offlineHold(_signal?: AbortSignal): Promise<OfflineHoldSnapshot> {
    return this.#offline;
  }

  async enterOfflineHold(
    expectedRevision: number,
    _signal?: AbortSignal,
  ): Promise<OfflineHoldSnapshot> {
    this.#requireOfflineRevision(expectedRevision);
    this.#offline = {
      ...this.#offline,
      state: "held",
      revision: this.#offline.revision + 1,
      since: new Date().toISOString(),
      activeActions: 0,
      activeEgress: 0,
      queuedRequests: 2,
      heldBytes: 8192,
      safeToDisconnect: true,
      activeByKind: {},
      queuedByKind: { exchange: 2 },
    };
    return this.#offline;
  }

  async resumeOfflineHold(
    expectedRevision: number,
    _signal?: AbortSignal,
  ): Promise<OfflineHoldSnapshot> {
    this.#requireOfflineRevision(expectedRevision);
    this.#offline = {
      ...initialOfflineHold(),
      revision: this.#offline.revision + 1,
      since: new Date().toISOString(),
    };
    return this.#offline;
  }

  async activities(cursor?: string, _signal?: AbortSignal) {
    if (cursor === undefined) {
      return {
        items: previewActivities.slice(0, 3),
        nextCursor: previewActivityCursor,
      };
    }
    if (cursor === previewActivityCursor) {
      return { items: previewActivities.slice(3) };
    }
    throw new Error("Preview Activity cursor is invalid");
  }

  async exchange(
    exchangeId: string,
    _signal?: AbortSignal,
  ): Promise<ExchangeDetail> {
    const record = previewActivities.find((item) => item.id === exchangeId);
    if (record === undefined && exchangeId === "ex204") {
      return {
        id: exchangeId,
        accessId: "work",
        status: "failed",
        processingTrace: {
          egressProxyId: "direct",
          pluginRunIds: [],
          attemptIds: ["attempt-ex204-1"],
          result: "provider_transport_failed",
        },
      };
    }
    if (record === undefined) {
      throw new ControlProblem(
        404,
        "exchange_not_found",
        "error.exchange_not_found",
      );
    }
    return {
      id: record.id,
      accessId: record.accessId,
      status: record.status,
      processingTrace: {
        egressProxyId: "direct",
        pluginRunIds:
          exchangeId === "exchange-preview-4"
            ? ["plugin-run-preview-polish"]
            : [],
        attemptIds: [`attempt-${exchangeId}`],
        result:
          record.status === "failed"
            ? "provider_transport_failed"
            : record.status,
      },
    };
  }

  async approvals(_signal?: AbortSignal) {
    return { items: this.#approvals.filter((item) => item.state === "pending") };
  }

  async captureRuns(_signal?: AbortSignal) {
    return {
      items: captureRunSamples as readonly CaptureRunRecord[],
    };
  }

  async workspaceRouteBindings(_signal?: AbortSignal) {
    return {
      items: this.#workspaceRoutes.map((binding) => ({
        ...binding,
        activeRuns: binding.activeRuns.map((run) => ({ ...run })),
        approvedProfiles: binding.approvedProfiles.map((profile) => ({
          ...profile,
        })),
      })),
    };
  }

  async updateWorkspaceRouteBinding(
    bindingId: string,
    expectedRevision: number,
    profileId: string,
    _signal?: AbortSignal,
  ): Promise<WorkspaceRouteBinding> {
    const index = this.#workspaceRoutes.findIndex(
      (binding) => binding.id === bindingId,
    );
    const current = this.#workspaceRoutes[index];
    if (
      current === undefined ||
      current.revision !== expectedRevision ||
      !current.approvedProfiles.some(
        (profile) => profile.profileId === profileId && profile.available,
      )
    ) {
      throw new Error("Preview workspace route changed");
    }
    const updated: WorkspaceRouteBinding = {
      ...current,
      profileId,
      revision: current.revision + 1,
      updatedAt: new Date().toISOString(),
    };
    this.#workspaceRoutes[index] = updated;
    return updated;
  }

  async connections(_signal?: AbortSignal) {
    return {
      items: connectionSamples as readonly ConnectionRecord[],
    };
  }

  async egressAttempts(_signal?: AbortSignal) {
    return {
      items: egressSamples as readonly EgressAttemptRecord[],
    };
  }

  async decideApproval(
    approval: ApprovalView,
    choice: ApprovalChoice,
    _signal?: AbortSignal,
  ): Promise<ApprovalView> {
    const index = this.#approvals.findIndex((item) => item.id === approval.id);
    const current = this.#approvals[index];
    if (
      current === undefined ||
      current.revision !== approval.revision ||
      !current.choices.some(
        (candidate) =>
          candidate.decision === choice.decision &&
          candidate.scope === choice.scope,
      )
    ) {
      throw new Error("Preview approval changed before it was decided");
    }
    const resolved: ApprovalView = {
      ...current,
      revision: current.revision + 1,
      state: choice.decision === "deny" ? "denied" : "allowed",
      decision: choice.decision,
      decisionScope: choice.scope,
      resolvedAt: new Date().toISOString(),
    };
    this.#approvals[index] = resolved;
    return resolved;
  }

  async accesses(_signal?: AbortSignal) {
    return {
      items: [...this.#accesses.entries()]
        .sort(([left], [right]) => compareResourceIds(left, right))
        .map(([accessId, entry]) => ({
          accessId,
          name: entry.input.access.name,
          description: entry.input.access.description,
          status: entry.input.access.status,
          revision: entry.revision,
          clientOrigin: entry.input.agentEndpoint.clientOrigin,
          clientDialect: entry.input.agentEndpoint.clientDialect,
        })),
    };
  }

  async access(accessId: string, _signal?: AbortSignal) {
    const entry = this.#accesses.get(accessId);
    if (entry === undefined) {
      throw new ControlProblem(
        404,
        "access_not_configured",
        "error.access_not_configured",
      );
    }
    const input = entry.input;
    return {
      revision: entry.revision,
      access: input.access,
      agentEndpoint: input.agentEndpoint,
      profiles: input.profiles,
      providerTargets: input.providerTargets,
      accountBindings: input.accountBindings.map(
        ({ secretRef: _secretRef, ...binding }) => ({
          ...binding,
          secretHandling: "preserve_existing" as const,
        }),
      ),
      routeSets: input.routeSets.map((routeSet) => ({
        ...routeSet,
        fallback: "disabled" as const,
      })),
      egressPolicy: input.egressPolicy,
      pluginPlan: input.pluginPlan,
    };
  }

  async addAccessCandidate(
    accessId: string,
    expectedRevision: number,
    candidate: AccessAddCandidateInput,
    _signal?: AbortSignal,
  ): Promise<AccessAddCandidateResponse> {
    const entry = this.#accesses.get(accessId);
    if (entry === undefined || entry.revision !== expectedRevision) {
      throw new Error("Preview Access revision changed");
    }
    const sequence = expectedRevision + 1;
    const profileId = `${accessId}-route-${sequence}`;
    const targetId = `${profileId}-target`;
    const credentialId = `${profileId}-account`;
    const origin =
      candidate.provider === "anthropic"
        ? "https://api.anthropic.com"
        : candidate.provider === "openai"
          ? "https://api.openai.com/v1"
          : candidate.baseUrl;
    const backendDialect = candidate.provider.startsWith("anthropic")
      ? "anthropic-messages"
      : "openai-chat";
    const input: AccessApplyInput = {
      ...entry.input,
      expectedRevision,
      access: {
        ...entry.input.access,
        profileIds: [...entry.input.access.profileIds, profileId],
      },
      profiles: [
        ...entry.input.profiles,
        {
          id: profileId,
          name: candidate.name,
          description: "",
          backendDialect,
          targetId,
          transportProfileRef: "observed-client-strict-h1",
          defaultModelPolicy: {
            mode: "fixed",
            fixedModel: candidate.model,
          },
          accountBindingIds: [credentialId],
          defaultAccountBindingId: credentialId,
        },
      ],
      providerTargets: [
        ...entry.input.providerTargets,
        {
          id: targetId,
          profileId,
          origin,
          protocol: backendDialect,
          capabilities: ["messages", "streaming", "tool_calls"],
        },
      ],
      accountBindings: [
        ...entry.input.accountBindings,
        {
          id: credentialId,
          profileId,
          label: candidate.name,
          secretRef: `secret://provider/${credentialId}`,
          authDriverRef:
            candidate.authDriverRef ??
            (candidate.provider.startsWith("anthropic")
              ? "anthropic_api_key"
              : "static_header"),
          enabled: true,
        },
      ],
      // A new candidate stays outside the active route set until its secret is
      // saved and the explicit select action succeeds.
      routeSets: entry.input.routeSets,
    };
    this.#accesses.set(accessId, { input, revision: sequence });
    this.#credentials.set(this.#credentialKey(profileId, credentialId), {
      credentialId,
      profileId,
      secretState: "missing",
      secretRevision: 0,
    });
    return {
      outcome: "committed",
      revision: sequence,
      applicationState: "active",
      planHash: "7f".repeat(32),
      candidate: { credentialId, profileId },
    };
  }

  async applyAccess(
    accessId: string,
    input: AccessApplyInput,
    _signal?: AbortSignal,
  ) {
    const currentRevision = this.#accesses.get(accessId)?.revision ?? 0;
    if (
      input.access.id !== accessId ||
      input.expectedRevision !== currentRevision
    ) {
      throw new Error("Preview Access revision changed");
    }
    const revision = input.expectedRevision + 1;
    this.#accesses.set(accessId, { input, revision });
    const binding = input.accountBindings[0];
    if (binding !== undefined) {
      this.#credentials.set(this.#credentialKey(binding.profileId, binding.id), {
        credentialId: binding.id,
        profileId: binding.profileId,
        secretState: "missing",
        secretRevision: 0,
      });
    }
    return {
      outcome: "committed" as const,
      revision,
      applicationState: "active" as const,
      planHash: "7f".repeat(32),
    };
  }

  async accessPlan(accessId: string, _signal?: AbortSignal): Promise<AccessPlanSummary> {
    const entry = this.#accesses.get(accessId);
    if (entry === undefined) {
      throw new Error("Preview Access does not exist");
    }
    return {
      accessId,
      revision: entry.revision,
      planHash: "7f".repeat(32),
      profiles: entry.input.profiles.map(({ id }) => id),
      accountBindings: entry.input.accountBindings.map(({ id, profileId }) => ({
        id,
        profileId,
      })),
    };
  }

  async credential(
    _accessId: string,
    profileId: string,
    credentialId: string,
    _signal?: AbortSignal,
  ): Promise<CredentialView> {
    return (
      this.#credentials.get(this.#credentialKey(profileId, credentialId)) ?? {
        credentialId,
        profileId,
        secretState: "missing",
        secretRevision: 0,
      }
    );
  }

  async replaceCredentialSecret(
    _accessId: string,
    profileId: string,
    credentialId: string,
    expectedRevision: number,
    _secret: string,
    _signal?: AbortSignal,
  ): Promise<CredentialView> {
    const key = this.#credentialKey(profileId, credentialId);
    const current = this.#credentials.get(key) ?? {
      credentialId,
      profileId,
      secretState: "missing" as const,
      secretRevision: 0,
    };
    if (expectedRevision !== current.secretRevision) {
      throw new Error("Preview credential revision changed");
    }
    const credential: CredentialView = {
      credentialId,
      profileId,
      secretState: "configured",
      secretRevision: expectedRevision + 1,
    };
    this.#credentials.set(key, credential);
    return credential;
  }

  async selectAccessCandidate(
    accessId: string,
    profileId: string,
    expectedRevision: number,
    _signal?: AbortSignal,
  ): Promise<AccessApplyResponse> {
    const entry = this.#accesses.get(accessId);
    if (
      entry === undefined ||
      entry.revision !== expectedRevision ||
      !entry.input.profiles.some(({ id }) => id === profileId)
    ) {
      throw new Error("Preview Access candidate changed");
    }
    const revision = expectedRevision + 1;
    const input: AccessApplyInput = {
      ...entry.input,
      expectedRevision,
      routeSets: entry.input.routeSets.map((routeSet) => ({
        ...routeSet,
        candidateProfileIds: [
          profileId,
          ...routeSet.candidateProfileIds.filter(
            (candidateId) => candidateId !== profileId,
          ),
        ],
      })),
    };
    this.#accesses.set(accessId, { input, revision });
    return {
      outcome: "committed",
      revision,
      applicationState: "active",
      planHash: "7f".repeat(32),
    };
  }

  #credentialKey(profileId: string, credentialId: string): string {
    return `${profileId}\u0000${credentialId}`;
  }

  #requireOfflineRevision(expectedRevision: number): void {
    if (expectedRevision !== this.#offline.revision) {
      throw new Error("Preview offline state changed");
    }
  }
}

export async function connectPreviewControl(): Promise<ControlClient> {
  return new PreviewControlClient();
}

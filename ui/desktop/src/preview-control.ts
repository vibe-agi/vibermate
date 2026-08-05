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
  AccessDeletionPreview,
  AccessDeletionResponse,
  AccessPlanSummary,
  AccessStatus,
  ActivityRecord,
  ApprovalChoice,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  CredentialView,
  EgressAttemptRecord,
  ExchangeDetail,
  ManualCaptureContext,
  ManualCaptureCreateInput,
  ManualCaptureGrantStateTag,
  ManualCapturePage,
  ManualCaptureRecord,
  ManualCaptureStateTag,
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

const previewManualCaptureContext: ManualCaptureContext = {
  confirmationToken: `ctx_${"A".repeat(43)}`,
  proxyAddress: "http://127.0.0.1:32123",
  root: {
    kind: "local_path",
    derSha256: "a".repeat(64),
    fingerprint: "AA:BB:CC:DD:EE:FF",
    pemPath: "/Users/demo/Library/Application Support/VibeMate/root.pem",
  },
  defaultTemporarySeconds: 86_400,
  maxTemporarySeconds: 604_800,
};

const previewWorkAccess = buildAccessApplyInput({
  mode: "managed",
  accessId: "work",
  name: "Work Claude",
  description: "Primary work connection",
  expectedRevision: "3",
  clientOrigin: "https://api.anthropic.com",
  clientDialect: "anthropic-messages",
  providerDialect: "openai-chat",
  authDriverRef: "static_header",
  providerOrigin: "https://gateway.example/v1",
  fixedModel: "example-model",
  routeName: "Demo route",
  upstreamPresentation: "follow-client",
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
  ...Array.from({ length: 35 }, (_, index): ActivityRecord => ({
    id: `exchange-preview-history-${String(index + 1).padStart(2, "0")}`,
    occurredAt: new Date(
      Date.parse("2026-08-02T09:54:00Z") - index * 47_000,
    ).toISOString(),
    accessId: index % 4 === 0 ? "personal" : "work",
    status:
      index % 11 === 0
        ? "failed"
        : index % 7 === 0
          ? "canceled"
          : "succeeded",
  })),
];

const previewWorkspaces = [
  "payments-api",
  "desktop-app",
  "documentation",
  "mobile-client",
  "infra",
  "research",
  "release",
  "support-tools",
] as const;

const previewCaptureRuns: readonly CaptureRunRecord[] = Array.from(
  { length: 8 },
  (_, index) => {
    const samples = captureRunSamples as readonly CaptureRunRecord[];
    const base = samples[index % samples.length]!;
    const workspace = previewWorkspaces[index]!;
    return {
      ...base,
      id: `run-preview-${index + 1}`,
      executableLabel: index % 3 === 1 ? "codex" : "claude",
      cwd: `/Users/example/${workspace}`,
      workspaceLabel: workspace,
      processId: 4_200 + index,
      createdAt: new Date(
        Date.parse("2026-08-02T10:08:00Z") - index * 71_000,
      ).toISOString(),
    };
  },
);

const previewConnections: readonly ConnectionRecord[] = Array.from(
  { length: 36 },
  (_, index) => {
    const samples = connectionSamples as readonly ConnectionRecord[];
    const base = samples[index % samples.length]!;
    const source = previewCaptureRuns[index % previewCaptureRuns.length]!;
    return {
      ...base,
      sequence: index + 1,
      connectionId: `connection-preview-${index + 1}`,
      ingressId: source.id,
      sourceLabel: `${source.executableLabel} · ${source.workspaceLabel}`,
      requestedHost:
        index % 3 === 0
          ? "api.anthropic.com"
          : index % 3 === 1
            ? "api.openai.com"
            : `service-${index + 1}.example.com`,
      startedAt: new Date(
        Date.parse("2026-08-02T10:10:00Z") - index * 19_000,
      ).toISOString(),
    };
  },
);

const previewEgressAttempts: readonly EgressAttemptRecord[] = Array.from(
  { length: 36 },
  (_, index) => {
    const samples = egressSamples as readonly EgressAttemptRecord[];
    const base = samples[index % samples.length]!;
    const connection = previewConnections[index]!;
    return {
      ...base,
      sequence: index + 1,
      id: `egress-preview-${index + 1}`,
      connectionId: connection.connectionId,
      targetOrigin: `https://${connection.requestedHost}:${connection.port}`,
      startedAt: connection.startedAt,
    };
  },
);

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
    [
      "work",
      {
        input: previewWorkAccess,
        revision: 4,
        status: "enabled" as AccessStatus,
      },
    ],
  ]);
  #deletionTokens = new Map<string, string>();
  #retiredAccesses = new Set<string>();
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
  #manualCaptures = new Map<string, ManualCaptureRecord>();
  #nextManualCapture = 1;
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
          kind: "managed",
          label: "001 · Demo route",
          modelPresentation: "example-model",
          authPresentation: "vibermate_account",
          authLabel: "001",
          available: true,
        },
        {
          profileId: "work-secondary",
          kind: "managed",
          label: "002 · Backup relay",
          modelPresentation: "gpt-5.6-sol",
          authPresentation: "vibermate_account",
          authLabel: "002",
          available: true,
        },
        {
          profileId: "original-passthrough",
          kind: "original_passthrough",
          label: "Current client login",
          modelPresentation: "passthrough",
          authPresentation: "client_auth",
          authLabel: "Claude Code login",
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
        items: previewActivities.slice(0, 20),
        nextCursor: previewActivityCursor,
      };
    }
    if (cursor === previewActivityCursor) {
      return { items: previewActivities.slice(20) };
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
      items: previewCaptureRuns,
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
      items: previewConnections,
    };
  }

  async egressAttempts(_signal?: AbortSignal) {
    return {
      items: previewEgressAttempts,
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
          status: entry.status,
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
    const originalProfileId = "original-passthrough";
    const originalTargetId = "original-client-origin";
    const defaultRouteSetId = "default-route";
    const managedProfiles = input.profiles.map((profile) => ({
      ...profile,
      kind: "managed" as const,
      credentialSource: "managed_account" as const,
      processingMode: "managed" as const,
    }));
    const originalProfile = {
      id: originalProfileId,
      kind: "original_passthrough" as const,
      credentialSource: "client_passthrough" as const,
      processingMode: "observe_only" as const,
      name: "Current client login",
      description: "",
      backendDialect: input.agentEndpoint.clientDialect,
      targetId: originalTargetId,
      upstreamWireProfileRef: "follow-client",
      defaultModelPolicy: { mode: "passthrough" as const },
      accountBindingIds: [] as string[],
      defaultAccountBindingId: "",
    };
    const routeSets =
      input.routeSets.length === 0
        ? [
            {
              id: defaultRouteSetId,
              candidateProfileIds: [originalProfileId],
              fallback: "disabled" as const,
            },
          ]
        : input.routeSets.map((routeSet) => ({
            ...routeSet,
            candidateProfileIds: routeSet.candidateProfileIds.includes(
              originalProfileId,
            )
              ? [...routeSet.candidateProfileIds]
              : [...routeSet.candidateProfileIds, originalProfileId],
            fallback: "disabled" as const,
          }));
    return {
      revision: entry.revision,
      access: {
        ...input.access,
        status: entry.status,
        defaultRouteSetId:
          input.access.defaultRouteSetId || defaultRouteSetId,
        profileIds: [...input.access.profileIds, originalProfileId],
      },
      agentEndpoint: input.agentEndpoint,
      profiles: [...managedProfiles, originalProfile],
      providerTargets: [
        ...input.providerTargets,
        {
          id: originalTargetId,
          profileId: originalProfileId,
          origin: input.agentEndpoint.clientOrigin,
          protocol: input.agentEndpoint.clientDialect,
          capabilities: ["messages", "streaming", "tool_calls"] as const,
        },
      ],
      accountBindings: input.accountBindings.map(
        ({ secretRef: _secretRef, ...binding }) => ({
          ...binding,
          secretHandling: "preserve_existing" as const,
        }),
      ),
      routeSets,
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
          upstreamWireProfileRef: "follow-client",
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
    this.#accesses.set(accessId, {
      input,
      revision: sequence,
      status: entry.status,
    });
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
    if (this.#retiredAccesses.has(accessId)) {
      throw new ControlProblem(409, "access_retired", "error.access_retired");
    }
    const currentRevision = this.#accesses.get(accessId)?.revision ?? 0;
    if (
      input.access.id !== accessId ||
      input.expectedRevision !== currentRevision
    ) {
      throw new Error("Preview Access revision changed");
    }
    const revision = input.expectedRevision + 1;
    this.#accesses.set(accessId, { input, revision, status: "enabled" });
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

  async updateAccessStatus(
    accessId: string,
    expectedRevision: number,
    status: Extract<AccessStatus, "enabled" | "disabled">,
    _signal?: AbortSignal,
  ): Promise<AccessApplyResponse> {
    const entry = this.#accesses.get(accessId);
    if (
      entry === undefined ||
      entry.revision !== expectedRevision ||
      entry.status === status ||
      (entry.status !== "enabled" && entry.status !== "disabled")
    ) {
      throw new Error("Preview Access status changed");
    }
    const revision = expectedRevision + 1;
    this.#accesses.set(accessId, { ...entry, revision, status });
    if (status === "disabled") {
      return {
        outcome: "committed",
        revision,
        applicationState: "inactive",
      };
    }
    return {
      outcome: "committed",
      revision,
      applicationState: "active",
      planHash: "7f".repeat(32),
    };
  }

  async previewAccessDeletion(
    accessId: string,
    expectedRevision: number,
    _signal?: AbortSignal,
  ): Promise<AccessDeletionPreview> {
    const entry = this.#accesses.get(accessId);
    if (entry === undefined || entry.revision !== expectedRevision) {
      throw new Error("Preview Access deletion revision changed");
    }
    const routes = this.#workspaceRoutes.filter(
      (binding) => binding.accessId === accessId,
    );
    const activeCaptureRunCount = routes.reduce(
      (total, binding) => total + binding.activeRuns.length,
      0,
    );
    const blockers = [
      ...(entry.status === "disabled" ? [] : ["disable_access_first" as const]),
      ...(activeCaptureRunCount === 0 ? [] : ["active_capture_runs" as const]),
      ...(routes.length === 0
        ? []
        : ["confirm_workspace_retirement" as const]),
    ];
    const impactToken = `${"A".repeat(42)}${expectedRevision % 10}`;
    this.#deletionTokens.set(accessId, impactToken);
    return {
      accessId,
      name: entry.input.access.name,
      revision: expectedRevision,
      status: entry.status,
      workspaceBindingCount: routes.length,
      activeCaptureRunCount,
      proxyClientBindingCount: 0,
      exclusiveSecretCount: new Set(
        entry.input.accountBindings.map(({ secretRef }) => secretRef),
      ).size,
      sharedSecretCount: 0,
      impactToken,
      blockers,
    };
  }

  async deleteAccess(
    accessId: string,
    expectedRevision: number,
    impactToken: string,
    retireWorkspaceBindings: boolean,
    _signal?: AbortSignal,
  ): Promise<AccessDeletionResponse> {
    const entry = this.#accesses.get(accessId);
    const routes = this.#workspaceRoutes.filter(
      (binding) => binding.accessId === accessId,
    );
    if (
      entry === undefined ||
      entry.revision !== expectedRevision ||
      entry.status !== "disabled" ||
      this.#deletionTokens.get(accessId) !== impactToken ||
      routes.some((binding) => binding.activeRuns.length !== 0) ||
      (routes.length !== 0 && !retireWorkspaceBindings)
    ) {
      throw new Error("Preview Access deletion changed or is blocked");
    }
    const credentialKeys = new Set(
      entry.input.accountBindings.map(({ id, profileId }) =>
        this.#credentialKey(profileId, id),
      ),
    );
    for (const key of credentialKeys) {
      this.#credentials.delete(key);
    }
    this.#workspaceRoutes = this.#workspaceRoutes.filter(
      (binding) => binding.accessId !== accessId,
    );
    this.#accesses.delete(accessId);
    this.#deletionTokens.delete(accessId);
    this.#retiredAccesses.add(accessId);
    return { outcome: "deleted", revision: expectedRevision };
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
      profiles: [
        "original-passthrough",
        ...entry.input.profiles.map(({ id }) => id),
      ],
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
      (profileId !== "original-passthrough" &&
        !entry.input.profiles.some(({ id }) => id === profileId))
    ) {
      throw new Error("Preview Access candidate changed");
    }
    const revision = expectedRevision + 1;
    const input: AccessApplyInput = {
      ...entry.input,
      expectedRevision,
      routeSets: entry.input.routeSets.map((routeSet) => ({
        ...routeSet,
        candidateProfileIds:
          profileId === "original-passthrough"
            ? [profileId]
            : [
                profileId,
                ...routeSet.candidateProfileIds.filter(
                  (candidateId) =>
                    candidateId !== profileId &&
                    candidateId !== "original-passthrough",
                ),
              ],
      })),
    };
    this.#accesses.set(accessId, {
      input,
      revision,
      status: entry.status,
    });
    return {
      outcome: "committed",
      revision,
      applicationState: "active",
      planHash: "7f".repeat(32),
    };
  }

  async manualCaptureContext(
    _signal?: AbortSignal,
  ): Promise<ManualCaptureContext> {
    return structuredClone(previewManualCaptureContext);
  }

  async manualCaptures(_signal?: AbortSignal): Promise<ManualCapturePage> {
    return {
      items: [...this.#manualCaptures.values()]
        .map((item) => structuredClone(item))
        .sort((left, right) => right.createdAt.localeCompare(left.createdAt)),
    };
  }

  async manualCapture(
    manualCaptureId: string,
    _signal?: AbortSignal,
  ): Promise<ManualCaptureStateTag> {
    const capture = this.#manualCaptures.get(manualCaptureId);
    if (capture === undefined) {
      throw new ControlProblem(
        404,
        "manual_capture_not_found",
        "error.manual_capture_not_found",
      );
    }
    return {
      capture: structuredClone(capture),
      stateTag: this.#manualCaptureStateTag(capture),
    };
  }

  async createManualCapture(
    input: ManualCaptureCreateInput,
    _signal?: AbortSignal,
  ): Promise<ManualCaptureGrantStateTag> {
    const sequence = this.#nextManualCapture++;
    const id = `preview-manual-${sequence}`;
    const now = new Date(Date.parse(previewStartedAt) + sequence * 1_000);
    const capture: ManualCaptureRecord = {
      id,
      ingressProfileId: `manual-capture/${id}`,
      displayName: input.displayName,
      clientClass: input.clientClass,
      lifetime: input.lifetime,
      state: "active",
      observation: "waiting_for_traffic",
      createdAt: now.toISOString(),
      updatedAt: now.toISOString(),
      ...(input.expiresInSeconds === undefined
        ? {}
        : {
            expiresAt: new Date(
              now.getTime() + input.expiresInSeconds * 1_000,
            ).toISOString(),
          }),
    };
    this.#manualCaptures.set(id, capture);
    return this.#manualCaptureGrant(capture, sequence);
  }

  async rotateManualCapture(
    manualCaptureId: string,
    stateTag: string,
    _signal?: AbortSignal,
  ): Promise<ManualCaptureGrantStateTag> {
    const current = await this.manualCapture(manualCaptureId);
    if (current.stateTag !== stateTag || current.capture.state !== "active") {
      throw new ControlProblem(
        409,
        "manual_capture_conflict",
        "error.manual_capture_conflict",
      );
    }
    const sequence = this.#nextManualCapture++;
    const updated: ManualCaptureRecord = {
      ...current.capture,
      updatedAt: new Date(
        Date.parse(current.capture.updatedAt) + 1_000,
      ).toISOString(),
    };
    this.#manualCaptures.set(manualCaptureId, updated);
    return this.#manualCaptureGrant(updated, sequence);
  }

  async revokeManualCapture(
    manualCaptureId: string,
    stateTag: string,
    _signal?: AbortSignal,
  ): Promise<void> {
    const current = await this.manualCapture(manualCaptureId);
    if (current.stateTag !== stateTag || current.capture.state !== "active") {
      throw new ControlProblem(
        409,
        "manual_capture_conflict",
        "error.manual_capture_conflict",
      );
    }
    this.#manualCaptures.set(manualCaptureId, {
      ...current.capture,
      state: "revoked",
      updatedAt: new Date(
        Date.parse(current.capture.updatedAt) + 1_000,
      ).toISOString(),
    });
  }

  #credentialKey(profileId: string, credentialId: string): string {
    return `${profileId}\u0000${credentialId}`;
  }

  #manualCaptureGrant(
    capture: ManualCaptureRecord,
    sequence: number,
  ): ManualCaptureGrantStateTag {
    return {
      grant: {
        capture: structuredClone(capture),
        proxyAddress: previewManualCaptureContext.proxyAddress,
        proxyUsername: "capture",
        proxyPassword: `manual_${String(sequence).padStart(43, "A")}`,
        root: structuredClone(previewManualCaptureContext.root),
      },
      stateTag: this.#manualCaptureStateTag(capture),
    };
  }

  #manualCaptureStateTag(capture: ManualCaptureRecord): string {
    const encoded = btoa(`${capture.id}:${capture.updatedAt}:${capture.state}`)
      .replaceAll("+", "-")
      .replaceAll("/", "_")
      .replace(/=+$/u, "");
    return `"mc_${encoded.padEnd(43, "A").slice(0, 43)}"`;
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

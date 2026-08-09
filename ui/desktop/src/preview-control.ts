import type { ControlClient } from "./control-client.ts";
import { compareResourceIds, ControlProblem } from "./control-client.ts";
import type {
  ActivityPage,
  ActivityQuery,
  ApprovalChoice,
  ApprovalPage,
  ApprovalView,
  CaptureAssignment,
  CaptureAssignmentSwitchResult,
  CapturePage,
  CaptureRecord,
  ConnectionPage,
  ConnectionRuleSet,
  ConnectionRuleSetInput,
  EgressAttemptPage,
  EnvironmentDraft,
  EnvironmentDraftInput,
  EnvironmentImpact,
  EnvironmentPage,
  EnvironmentPublishResult,
  EnvironmentRecord,
  ExchangeDetail,
  ManualCaptureContext,
  ManualCaptureCreateInput,
  ManualCaptureGrant,
  ManualCaptureGrantStateTag,
  ManualCapturePage,
  ManualCaptureRecord,
  ManualCaptureStateTag,
  OfflineHoldSnapshot,
  ProviderAccountCreateInput,
  ProviderAccountCredentialInput,
  ProviderAccountDeleteResult,
  ProviderAccountPage,
  ProviderAccountRecord,
  StatusResponse,
  WorkspaceEnvironmentDefault,
} from "./control-types.ts";

const timestamp = "2026-08-08T08:00:00.000Z";
const laterTimestamp = "2026-08-08T08:01:00.000Z";
const transparentDigest = "1".repeat(64);
const workDigest = "2".repeat(64);
const draftDigest = "3".repeat(64);
const launchAuthorityDigest = "4".repeat(64);

const emptyOffline: OfflineHoldSnapshot = {
  state: "online",
  revision: 1,
  since: timestamp,
  activeActions: 0,
  enteringActions: 0,
  activeEgress: 0,
  queuedRequests: 0,
  heldBytes: 0,
  safeToDisconnect: true,
  activeByKind: {},
  queuedByKind: {},
};

const transparentEnvironment: EnvironmentRecord = {
  id: "system_transparent",
  name: "Transparent",
  state: "active",
  revision: 1,
  digest: transparentDigest,
  systemOwned: true,
  clientEndpoints: [],
  pluginBindings: [],
  budgetPolicy: { id: "", revision: 0 },
  egressPolicy: { id: "", revision: 0, mode: "" },
  contentRecording: { mode: "off", retentionDays: 0 },
};

const workEnvironment: EnvironmentRecord = {
  id: "work",
  name: "Work",
  state: "active",
  revision: 3,
  digest: workDigest,
  systemOwned: false,
  clientEndpoints: [
    {
      id: "claude-endpoint",
      revision: 2,
      clientOrigin: "https://api.anthropic.com",
      protocolPlans: [
        {
          id: "claude-messages",
          revision: 2,
          clientProtocol: "anthropic_messages",
          clientAdapterPolicy: { id: "claude-adapter", revision: 1 },
          mode: "original_passthrough",
          upstreamPlan: {
            routes: [
              {
                id: "claude-official",
                revision: 2,
                providerTarget: {
                  id: "anthropic-official",
                  revision: 1,
                  origin: "https://api.anthropic.com",
                  realmId: "anthropic.official",
                  capabilities: ["messages"],
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
                modelPolicy: { revision: 1, mode: "preserve", fixedModel: "" },
                wireProfileRef: "claude-default",
                pluginBindings: [],
              },
            ],
            defaultRouteId: "claude-official",
            routeSet: {
              id: "claude-routes",
              revision: 1,
              candidateRouteIds: ["claude-official"],
            },
          },
          pluginBindings: [],
        },
      ],
    },
  ],
  pluginBindings: [],
  budgetPolicy: { id: "work-budget", revision: 1 },
  egressPolicy: { id: "work-egress", revision: 1, mode: "direct" },
  contentRecording: { mode: "full", retentionDays: 30 },
};

const managedCapture: CaptureRecord = {
  key: "managed_run:run-preview",
  id: "run-preview",
  kind: "managed_run",
  displayName: "claude",
  state: "running",
  observation: "observed",
  createdAt: timestamp,
  updatedAt: laterTimestamp,
  managedRun: {
    executableLabel: "claude",
    cwd: "/Users/example/project",
    canonicalExecutablePath: "/Users/example/.local/bin/claude",
    localUserLabel: "example",
    machineId: "machine-preview",
    machineRegistrationRevision: 1,
    workspaceId: "workspace-preview",
    workspaceLabel: "project",
    workspaceEvidence: "canonical_path",
    workspaceDerivationRevision: 1,
    processId: 42,
    recognition: "recognized",
    expiresAt: "2026-08-08T10:00:00.000Z",
    firstObservedAt: laterTimestamp,
  },
};

const baseManualCapture: ManualCaptureRecord = {
  id: "manual-preview",
  displayName: "Editor",
  clientClass: "desktop_app",
  lifetime: "until_revoked",
  state: "active",
  observation: "waiting_for_traffic",
  createdAt: timestamp,
  updatedAt: timestamp,
};

function manualCaptureRecord(record: ManualCaptureRecord): CaptureRecord {
  return {
    key: `manual_capture:${record.id}`,
    id: record.id,
    kind: "manual_capture",
    displayName: record.displayName,
    state: record.state,
    observation: record.observation,
    createdAt: record.createdAt,
    updatedAt: record.updatedAt,
    manualCapture: {
      clientClass: record.clientClass,
      lifetime: record.lifetime,
      credentialRevision: 1,
      ...(record.expiresAt === undefined ? {} : { expiresAt: record.expiresAt }),
      ...(record.lastObservedAt === undefined
        ? {}
        : { lastObservedAt: record.lastObservedAt }),
    },
  };
}

const frozenEnvironment = {
  id: "work",
  revision: 3,
  digest: workDigest,
  clientEndpointId: "claude-endpoint",
  clientEndpointRevision: 2,
  protocolPlanId: "claude-messages",
  protocolPlanRevision: 2,
  routeId: "claude-official",
  routeRevision: 2,
  accountId: "anthropic-work",
  accountRevision: 4,
  credentialEpoch: 7,
} as const;

const previewExchange: ExchangeDetail = {
  id: "exchange-preview",
  status: "succeeded",
  environment: frozenEnvironment,
  parentRefs: {
    captureRunId: "run-preview",
    connectionId: "connection-preview",
    exchangeId: "exchange-preview",
  },
  processingTrace: {
    pluginRunIds: [],
    attempts: [{
      sequence: 1,
      id: "egress-preview",
      purpose: "provider_attempt",
      payloadClass: "client_semantic",
      parent: { kind: "upstream_attempt", id: "attempt-preview", exchangeId: "exchange-preview" },
      caller: "core",
      targetOrigin: "https://api.anthropic.com",
      decision: { authority: "environment", policyId: "egress.work", policyRevision: 1 },
      reusedTransport: false,
      startedAt: "2026-08-08T08:00:00.000Z",
      terminal: true,
      outcome: "completed",
      bytesOut: 384,
      bytesIn: 192,
      completedAt: "2026-08-08T08:00:01.000Z",
    }],
    result: "completed",
  },
  content: {
    state: "recorded",
    mode: "full",
    recordedAt: "2026-08-08T02:00:00Z",
    expiresAt: "2026-09-07T02:00:00Z",
    request: {
      requestedModel: "claude-sonnet",
      effectiveModel: "claude-sonnet",
      maxOutputTokens: 4096,
      stream: true,
      messages: [{
        role: "system",
        blocks: [{
          kind: "text",
          availability: "recorded",
          text: "System context marker: this long-running agent context is available for forensic inspection but should not dominate the initial view.",
          originalSize: 127,
        }],
      }, {
        role: "user",
        blocks: [{
          kind: "text",
          availability: "recorded",
          text: "Inspect the current package and summarize the failing test.",
          originalSize: 59,
        }],
      }],
      tools: [{ name: "read_file" }],
    },
    response: {
      id: "response-preview",
      requestedModel: "claude-sonnet",
      effectiveModel: "claude-sonnet",
      reportedModel: "claude-sonnet",
      stopReason: "tool_use",
      blocks: [{
        kind: "text",
        availability: "recorded",
        text: "## Inspection plan\n\n- Read the package manifest\n- Run the focused test\n\nUse `pnpm test` before changing code.\n\n![Remote diagram](https://example.invalid/diagram.png)",
        originalSize: 164,
      }, {
        kind: "tool_call",
        availability: "recorded",
        originalSize: 32,
        callId: "tool-preview",
        toolName: "read_file",
        arguments: { path: "~/Code/project/package.json" },
      }],
      usage: {
        inputUncached: { known: true, tokens: 120, source: "provider" },
        cacheWrite: { known: false },
        cacheRead: { known: true, tokens: 48, source: "provider" },
        output: { known: true, tokens: 16, source: "provider" },
        reasoning: { known: false },
      },
    },
  },
};

const earlierPreviewExchange: ExchangeDetail = {
  ...previewExchange,
  id: "exchange-preview-earlier",
  status: "failed",
  parentRefs: {
    ...previewExchange.parentRefs,
    exchangeId: "exchange-preview-earlier",
  },
  processingTrace: {
    pluginRunIds: [],
    attempts: [{
      ...previewExchange.processingTrace.attempts[0]!,
      id: "egress-preview-earlier",
      parent: { kind: "upstream_attempt", id: "attempt-preview-earlier", exchangeId: "exchange-preview-earlier" },
    }],
    result: "tool_decision_expired",
  },
  content: {
    ...previewExchange.content,
    recordedAt: timestamp,
    request: {
      ...previewExchange.content.request!,
      messages: [{
        role: "user",
        blocks: [{
          kind: "text",
          availability: "recorded",
          text: "List the packages in this workspace.",
          originalSize: 36,
        }],
      }],
    },
    response: {
      ...previewExchange.content.response!,
      id: "response-preview-earlier",
      stopReason: "end_turn",
      blocks: [{
        kind: "text",
        availability: "recorded",
        text: "The workspace contains the desktop app and runtime packages.",
        originalSize: 57,
      }],
    },
  },
};

const pendingApproval: ApprovalView = {
  id: "approval-preview",
  revision: 1,
  kind: "network_ask",
  state: "pending",
  risk: "network",
  titleKey: "approval.networkAsk.title",
  summaryKey: "approval.networkAsk.summary",
  aggregateKey: "network:example.com:443",
  environmentId: "work",
  environmentRevision: 3,
  environmentDigest: workDigest,
  routeId: "claude-official",
  routeRevision: 2,
  target: { host: "example.com", port: 443 },
  subjectRefs: ["connection-preview"],
  subjectLabels: ["example.com:443"],
  requestCount: 1,
  waiterCount: 1,
  choices: [
    {
      decision: "allow-once",
      scope: "request",
      labelKey: "approval.networkAsk.choice.allowOnce",
    },
    {
      decision: "deny",
      scope: "request",
      labelKey: "approval.networkAsk.choice.denyOnce",
    },
  ],
  createdAt: timestamp,
  expiresAt: "2026-08-08T08:10:00.000Z",
};

const previewAccount: ProviderAccountRecord = {
  id: "anthropic-work",
  displayName: "Anthropic Work",
  kind: "anthropic_api_key",
  realmId: "anthropic.official",
  state: "active",
  revision: 1,
  credentialState: "ready",
  credentialEpoch: 1,
};

function clone<T>(value: T): T {
  return structuredClone(value);
}

class PreviewControlClient implements ControlClient {
  private closed = false;
  private offline = clone(emptyOffline);
  private readonly environmentRecords = new Map<string, EnvironmentRecord>([
    [transparentEnvironment.id, clone(transparentEnvironment)],
    [workEnvironment.id, clone(workEnvironment)],
  ]);
  private readonly historicalEnvironments = new Map<string, EnvironmentRecord>([
    [`${transparentEnvironment.id}@${transparentEnvironment.revision}`, clone(transparentEnvironment)],
    [`${workEnvironment.id}@${workEnvironment.revision}`, clone(workEnvironment)],
  ]);
  private readonly drafts = new Map<string, EnvironmentDraft>();
  private readonly accountRecords = new Map<string, ProviderAccountRecord>([
    [previewAccount.id, clone(previewAccount)],
  ]);
  private readonly manualRecords = new Map<string, ManualCaptureRecord>([
    [baseManualCapture.id, clone(baseManualCapture)],
  ]);
  private readonly captureAssignments = new Map<string, CaptureAssignment>([
    [managedCapture.key, {
      captureKey: managedCapture.key,
      captureId: managedCapture.id,
      captureKind: managedCapture.kind,
      environmentId: "work",
      revision: 1,
      source: "launch",
      updatedAt: timestamp,
    }],
    [`manual_capture:${baseManualCapture.id}`, {
      captureKey: `manual_capture:${baseManualCapture.id}`,
      captureId: baseManualCapture.id,
      captureKind: "manual_capture",
      environmentId: "system_transparent",
      revision: 1,
      source: "manual_create",
      updatedAt: timestamp,
    }],
  ]);
  private readonly workspaceDefaults = new Map<string, WorkspaceEnvironmentDefault>([
    ["machine-preview:workspace-preview", {
      machineId: "machine-preview",
      workspaceId: "workspace-preview",
      environmentId: "work",
      environmentName: "Work",
      revision: 1,
      updatedAt: timestamp,
    }],
  ]);
  private approval = clone(pendingApproval);
  private rules: ConnectionRuleSet = {
    revision: 1,
    rules: [],
    mode: "monitor",
  };

  close(): void {
    this.closed = true;
  }

  private requireOpen(): void {
    if (this.closed) throw new DOMException("Preview control closed", "AbortError");
  }

  async status(): Promise<StatusResponse> {
    this.requireOpen();
    return {
      generation: "preview-generation",
      ready: true,
      apiVersion: "v1",
      statusKey: "status.ready",
      runtime: {
        state: "initialized",
        instanceId: "preview-instance",
        host: "desktop",
        schemaRevision: 1,
        storage: "healthy",
        environmentProjection: {
          state: "healthy",
          unavailableEnvironments: null,
        },
        offlineHold: clone(this.offline),
        startedAt: timestamp,
      },
    };
  }

  async offlineHold(): Promise<OfflineHoldSnapshot> {
    this.requireOpen();
    return clone(this.offline);
  }

  async enterOfflineHold(expectedRevision: number): Promise<OfflineHoldSnapshot> {
    this.requireOpen();
    this.requireRevision(this.offline.revision, expectedRevision);
    this.offline = { ...this.offline, state: "held", revision: expectedRevision + 1, since: laterTimestamp };
    return clone(this.offline);
  }

  async resumeOfflineHold(expectedRevision: number): Promise<OfflineHoldSnapshot> {
    this.requireOpen();
    this.requireRevision(this.offline.revision, expectedRevision);
    this.offline = { ...this.offline, state: "online", revision: expectedRevision + 1, since: laterTimestamp };
    return clone(this.offline);
  }

  async environments(): Promise<EnvironmentPage> {
    this.requireOpen();
    return {
      items: [...this.environmentRecords.values()]
        .sort((left, right) => {
          if (left.systemOwned !== right.systemOwned) {
            return left.systemOwned ? -1 : 1;
          }
          return compareResourceIds(left.id, right.id);
        })
        .map(clone),
    };
  }

  async providerAccounts(): Promise<ProviderAccountPage> {
    this.requireOpen();
    return {
      items: [...this.accountRecords.values()]
        .sort((left, right) => left.id.localeCompare(right.id))
        .map(clone),
    };
  }

  async providerAccount(accountId: string): Promise<ProviderAccountRecord> {
    this.requireOpen();
    const account = this.accountRecords.get(accountId);
    if (account === undefined) throw this.notFound();
    return clone(account);
  }

  async createProviderAccount(
    input: ProviderAccountCreateInput,
  ): Promise<ProviderAccountRecord> {
    this.requireOpen();
    if (this.accountRecords.has(input.id)) throw this.conflict();
    const account: ProviderAccountRecord = {
      id: input.id,
      displayName: input.displayName,
      kind: input.kind,
      realmId:
        input.kind === "anthropic_api_key" || input.kind === "claude_oauth_token"
          ? "anthropic.official"
          : "openai.platform",
      state: "active",
      revision: 1,
      credentialState: "ready",
      credentialEpoch: 1,
    };
    this.accountRecords.set(account.id, account);
    return clone(account);
  }

  async replaceProviderAccountCredential(
    accountId: string,
    expectedCredentialEpoch: number,
    _input: ProviderAccountCredentialInput,
  ): Promise<ProviderAccountRecord> {
    this.requireOpen();
    const account = this.accountRecords.get(accountId);
    if (account === undefined) throw this.notFound();
    this.requireRevision(account.credentialEpoch, expectedCredentialEpoch);
    const updated = {
      ...account,
      credentialState: "ready" as const,
      credentialEpoch: expectedCredentialEpoch + 1,
    };
    this.accountRecords.set(accountId, updated);
    return clone(updated);
  }

  async deleteProviderAccount(
    accountId: string,
    expectedCredentialEpoch: number,
  ): Promise<ProviderAccountDeleteResult> {
    this.requireOpen();
    const account = this.accountRecords.get(accountId);
    if (account === undefined) throw this.notFound();
    this.requireRevision(account.credentialEpoch, expectedCredentialEpoch);
    const references = [...this.environmentRecords.values()]
      .flatMap((environment) =>
        environment.clientEndpoints.flatMap((endpoint) =>
          endpoint.protocolPlans.flatMap((plan) =>
            plan.upstreamPlan.routes
              .filter((route) => route.accountPolicy.candidateAccountIds.includes(accountId))
              .map((route) => ({
                environmentId: environment.id,
                environmentName: environment.name,
                environmentRevision: environment.revision,
                routeId: route.id,
                routeRevision: route.revision,
              })),
          ),
        ),
      )
      .sort((left, right) => `${left.environmentId}\u0000${left.routeId}`.localeCompare(`${right.environmentId}\u0000${right.routeId}`));
    if (references.length !== 0) return { deleted: false, referenceCount: references.length, references };
    this.accountRecords.delete(accountId);
    return { deleted: true, referenceCount: 0, references: [] };
  }

  async environment(environmentId: string): Promise<EnvironmentRecord> {
    this.requireOpen();
    return clone(this.requireEnvironment(environmentId));
  }

  async environmentRevision(environmentId: string, revision: number): Promise<EnvironmentRecord> {
    this.requireOpen();
    const value = this.historicalEnvironments.get(`${environmentId}@${revision}`);
    if (value === undefined) throw this.notFound();
    return clone(value);
  }

  async environmentDraft(environmentId: string): Promise<EnvironmentDraft> {
    this.requireOpen();
    const existing = this.drafts.get(environmentId);
    if (existing !== undefined) return clone(existing);
    throw new ControlProblem(
      404,
      "environment_draft_not_found",
      "error.environment_draft_not_found",
    );
  }

  async saveEnvironmentDraft(
    environmentId: string,
    expectedBaseRevision: number,
    input: EnvironmentDraftInput,
  ): Promise<EnvironmentDraft> {
    this.requireOpen();
    const current = this.environmentRecords.get(environmentId);
    if (current === undefined) {
      this.requireRevision(0, expectedBaseRevision);
    } else {
      this.requireRevision(current.revision, expectedBaseRevision);
    }
    const previous = this.drafts.get(environmentId);
    this.requireRevision(previous?.draftRevision ?? 0, input.expectedDraftRevision);
    const draft: EnvironmentDraft = {
      environmentId,
      baseRevision: current?.revision ?? 0,
      draftRevision: input.expectedDraftRevision + 1,
      candidateDigest: draftDigest,
      candidate: {
        id: environmentId,
        name: input.name,
        state: input.state,
        revision: current?.revision ?? 0,
        digest: draftDigest,
        systemOwned: current?.systemOwned ?? false,
        clientEndpoints: clone(input.clientEndpoints),
        pluginBindings: clone(input.pluginBindings),
        budgetPolicy: clone(input.budgetPolicy),
        egressPolicy: clone(input.egressPolicy),
        contentRecording: clone(input.contentRecording),
      },
    };
    this.drafts.set(environmentId, draft);
    return clone(draft);
  }

  async previewEnvironmentDraft(environmentId: string, draftRevision: number): Promise<EnvironmentImpact> {
    this.requireOpen();
    return clone(this.impact(environmentId, draftRevision));
  }

  async publishEnvironmentDraft(environmentId: string, draftRevision: number): Promise<EnvironmentPublishResult> {
    this.requireOpen();
    const draft = this.requireDraft(environmentId, draftRevision);
    const impact = this.impact(environmentId, draftRevision);
    const committed: EnvironmentRecord = {
      ...clone(draft.candidate),
      revision: draft.baseRevision + 1,
      digest: draft.candidateDigest,
    };
    this.environmentRecords.set(environmentId, committed);
    this.historicalEnvironments.set(`${environmentId}@${committed.revision}`, clone(committed));
    this.drafts.delete(environmentId);
    return { outcome: "committed", environment: clone(committed), impact };
  }

  async captures(): Promise<CapturePage> {
    this.requireOpen();
    return { items: this.captureRecords().sort((left, right) => left.key.localeCompare(right.key)) };
  }

  async capture(captureKey: string): Promise<CaptureRecord> {
    this.requireOpen();
    const record = this.captureRecords().find((candidate) => candidate.key === captureKey);
    if (record === undefined) throw this.notFound();
    return clone(record);
  }

  async captureAssignment(captureKey: string): Promise<CaptureAssignment> {
    this.requireOpen();
    const assignment = this.captureAssignments.get(captureKey);
    if (assignment === undefined) throw this.notFound();
    return clone(assignment);
  }

  async switchCaptureEnvironment(
    captureKey: string,
    expectedRevision: number,
    environmentId: string,
  ): Promise<CaptureAssignmentSwitchResult> {
    this.requireOpen();
    this.requireEnvironment(environmentId);
    const current = this.captureAssignments.get(captureKey);
    if (current === undefined) throw this.notFound();
    this.requireRevision(current.revision, expectedRevision);
    if (current.environmentId === environmentId) {
      return { assignment: clone(current), boundary: "no_change", closedConnections: [], applied: true };
    }
    const assignment: CaptureAssignment = {
      ...current,
      environmentId,
      revision: expectedRevision + 1,
      source: "operator_switch",
      updatedAt: laterTimestamp,
    };
    this.captureAssignments.set(captureKey, assignment);
    return { assignment: clone(assignment), boundary: "hot_switch", closedConnections: [], applied: true };
  }

  async workspaceEnvironmentDefault(
    machineId: string,
    workspaceId: string,
  ): Promise<WorkspaceEnvironmentDefault | undefined> {
    this.requireOpen();
    return clone(this.workspaceDefaults.get(`${machineId}:${workspaceId}`));
  }

  async setWorkspaceEnvironmentDefault(
    machineId: string,
    workspaceId: string,
    expectedRevision: number,
    environmentId: string,
  ): Promise<WorkspaceEnvironmentDefault> {
    this.requireOpen();
    const environment = this.requireEnvironment(environmentId);
    const key = `${machineId}:${workspaceId}`;
    const current = this.workspaceDefaults.get(key);
    this.requireRevision(current?.revision ?? 0, expectedRevision);
    const record: WorkspaceEnvironmentDefault = {
      machineId,
      workspaceId,
      environmentId,
      environmentName: environment.name,
      revision: expectedRevision + 1,
      updatedAt: laterTimestamp,
    };
    this.workspaceDefaults.set(key, record);
    return clone(record);
  }

  async clearWorkspaceEnvironmentDefault(
    machineId: string,
    workspaceId: string,
    expectedRevision: number,
  ): Promise<void> {
    this.requireOpen();
    const key = `${machineId}:${workspaceId}`;
    const current = this.workspaceDefaults.get(key);
    if (current === undefined) throw this.notFound();
    this.requireRevision(current.revision, expectedRevision);
    this.workspaceDefaults.delete(key);
  }

  async activities(query?: ActivityQuery): Promise<ActivityPage> {
    this.requireOpen();
    const items = [
      { detail: previewExchange, occurredAt: laterTimestamp },
      { detail: earlierPreviewExchange, occurredAt: timestamp },
    ].map(({ detail, occurredAt }) => ({
      id: detail.id,
      occurredAt,
      kind: "exchange" as const,
      title: "Claude request",
      status: detail.status,
      ...(detail.processingTrace.result === "completed" ? {} : { reasonCode: detail.processingTrace.result }),
      source: { kind: "capture_run" as const, displayName: "claude", recognition: "verified" as const },
      environment: detail.environment,
      parentRefs: detail.parentRefs,
    })).filter((item) =>
      (query?.captureRunId === undefined || query.captureRunId === item.parentRefs.captureRunId) &&
      (query?.environmentId === undefined || query.environmentId === item.environment.id));
    return { items: clone(items) };
  }

  async exchange(exchangeId: string): Promise<ExchangeDetail> {
    this.requireOpen();
    if (exchangeId === previewExchange.id) return clone(previewExchange);
    if (exchangeId === earlierPreviewExchange.id) return clone(earlierPreviewExchange);
    throw this.notFound();
  }

  async approvals(): Promise<ApprovalPage> {
    this.requireOpen();
    return { items: this.approval.state === "pending" ? [clone(this.approval)] : [] };
  }

  async decideApproval(approval: ApprovalView, choice: ApprovalChoice): Promise<ApprovalView> {
    this.requireOpen();
    this.requireRevision(this.approval.revision, approval.revision);
    this.approval = {
      ...this.approval,
      revision: approval.revision + 1,
      state: choice.decision === "deny" ? "denied" : "allowed",
      waiterCount: 0,
      decision: choice.decision,
      decisionScope: choice.scope,
      resolvedAt: laterTimestamp,
    };
    return clone(this.approval);
  }

  async manualCaptureContext(environmentId: string): Promise<ManualCaptureContext> {
    this.requireOpen();
    const environment = this.requireEnvironment(environmentId);
    return {
      confirmationToken: `ctx_${"A".repeat(43)}`,
      proxyAddress: "http://127.0.0.1:43180",
      environmentId,
      environmentRevision: environment.revision,
      environmentDigest: environment.digest,
      launchAuthorityDigest,
      protectedAuthorities: environment.clientEndpoints.map((endpoint) => new URL(endpoint.clientOrigin).host),
      managedCredentialAuthorities: [],
      ...(environment.clientEndpoints.length === 0
        ? {}
        : {
            root: {
              kind: "local_path" as const,
              derSha256: "5".repeat(64),
              fingerprint: "55:55:55:55",
              pemPath: "/Users/example/Library/Application Support/ViberMate/root.pem",
            },
          }),
      defaultTemporarySeconds: 3_600,
      maxTemporarySeconds: 86_400,
    };
  }

  async manualCaptures(): Promise<ManualCapturePage> {
    this.requireOpen();
    return { items: [...this.manualRecords.values()].map(clone) };
  }

  async manualCapture(manualCaptureId: string): Promise<ManualCaptureStateTag> {
    this.requireOpen();
    const capture = this.manualRecords.get(manualCaptureId);
    if (capture === undefined) throw this.notFound();
    return { capture: clone(capture), stateTag: this.stateTag(manualCaptureId) };
  }

  async createManualCapture(input: ManualCaptureCreateInput): Promise<ManualCaptureGrantStateTag> {
    this.requireOpen();
    const environment = this.requireEnvironment(input.environmentId);
    const id = `manual-${this.manualRecords.size + 1}`;
    const capture: ManualCaptureRecord = {
      id,
      displayName: input.displayName,
      clientClass: input.clientClass,
      lifetime: input.lifetime,
      state: "active",
      observation: "waiting_for_traffic",
      createdAt: laterTimestamp,
      updatedAt: laterTimestamp,
      ...(input.lifetime === "temporary"
        ? { expiresAt: "2026-08-08T09:00:00.000Z" }
        : {}),
    };
    this.manualRecords.set(id, capture);
    const key = `manual_capture:${id}`;
    this.captureAssignments.set(key, {
      captureKey: key,
      captureId: id,
      captureKind: "manual_capture",
      environmentId: environment.id,
      revision: 1,
      source: "manual_create",
      updatedAt: laterTimestamp,
    });
    return {
      grant: this.grant(capture, environment),
      stateTag: this.stateTag(id),
    };
  }

  async rotateManualCapture(manualCaptureId: string): Promise<ManualCaptureGrantStateTag> {
    this.requireOpen();
    const capture = this.manualRecords.get(manualCaptureId);
    if (capture === undefined) throw this.notFound();
    const assignment = this.captureAssignments.get(`manual_capture:${manualCaptureId}`);
    if (assignment === undefined) throw this.notFound();
    return {
      grant: this.grant(capture, this.requireEnvironment(assignment.environmentId)),
      stateTag: this.stateTag(manualCaptureId),
    };
  }

  async revokeManualCapture(manualCaptureId: string): Promise<void> {
    this.requireOpen();
    const capture = this.manualRecords.get(manualCaptureId);
    if (capture === undefined) throw this.notFound();
    this.manualRecords.set(manualCaptureId, { ...capture, state: "revoked", updatedAt: laterTimestamp });
  }

  async connections(): Promise<ConnectionPage> {
    this.requireOpen();
    return {
      items: [{
        sequence: 1,
        connectionId: "connection-preview",
        ingressId: "run-preview",
        sourceLabel: "claude",
        sourceConfidence: "verified",
        environmentId: "work",
        environmentName: "Work",
        environmentRevision: 3,
        clientEndpointId: "claude-endpoint",
        clientEndpointRevision: 2,
        requestedHost: "api.anthropic.com",
        observedSni: "api.anthropic.com",
        routeHost: "api.anthropic.com",
        ip: "203.0.113.1",
        port: 443,
        decision: "allow",
        ruleId: "work-default",
        egressScope: "environment",
        egressSource: "environment_default",
        egressPolicyRevision: 1,
        decryption: "mitm",
        phase: "closed",
        bytesUp: 512,
        bytesDown: 1_024,
        startedAt: timestamp,
        endedAt: laterTimestamp,
        outcome: "completed",
      }],
    };
  }

  async egressAttempts(): Promise<EgressAttemptPage> {
    this.requireOpen();
    return {
      items: [{
        sequence: 1,
        id: "attempt-preview",
        connectionId: "connection-preview",
        purpose: "provider_attempt",
        payloadClass: "client_semantic",
        parent: { kind: "exchange", id: previewExchange.id, exchangeId: previewExchange.id },
        caller: "core",
        targetOrigin: "https://api.anthropic.com:443",
        decision: { policyId: "work-egress", policyRevision: 1, authority: "environment" },
        reusedTransport: false,
        startedAt: timestamp,
        terminal: true,
        outcome: "completed",
        bytesOut: 512,
        bytesIn: 1_024,
        completedAt: laterTimestamp,
      }],
    };
  }

  async connectionRules(): Promise<ConnectionRuleSet> {
    this.requireOpen();
    return clone(this.rules);
  }

  async replaceConnectionRules(expectedRevision: number, input: ConnectionRuleSetInput): Promise<ConnectionRuleSet> {
    this.requireOpen();
    this.requireRevision(this.rules.revision, expectedRevision);
    this.rules = { revision: expectedRevision + 1, rules: clone(input.rules), mode: input.mode };
    return clone(this.rules);
  }

  private captureRecords(): CaptureRecord[] {
    return [clone(managedCapture), ...[...this.manualRecords.values()].map((record) => manualCaptureRecord(record))];
  }

  private impact(environmentId: string, draftRevision: number): EnvironmentImpact {
    const draft = this.requireDraft(environmentId, draftRevision);
    const affected = [...this.captureAssignments.values()]
      .filter((assignment) => assignment.environmentId === environmentId)
      .map((assignment) => ({ captureKind: assignment.captureKind, captureId: assignment.captureId, classification: "hot_switch" as const }));
    return {
      environmentId,
      baseRevision: draft.baseRevision,
      draftRevision,
      candidateDigest: draft.candidateDigest,
      classification: "hot_switch",
      hotSwitchCount: affected.length,
      reconnectRequiredCount: 0,
      restartRequiredCount: 0,
      affected,
    };
  }

  private requireDraft(environmentId: string, revision: number): EnvironmentDraft {
    const draft = this.drafts.get(environmentId);
    if (draft === undefined || draft.draftRevision !== revision) throw this.conflict();
    return draft;
  }

  private requireEnvironment(environmentId: string): EnvironmentRecord {
    const environment = this.environmentRecords.get(environmentId);
    if (environment === undefined) throw this.notFound();
    return environment;
  }

  private grant(capture: ManualCaptureRecord, environment: EnvironmentRecord): ManualCaptureGrant {
    return {
      capture: clone(capture),
      proxyAddress: "http://127.0.0.1:43180",
      proxyUsername: `manual:${capture.id}`,
      proxyPassword: "preview-secret",
      environmentId: environment.id,
      assignmentRevision: this.captureAssignments.get(`manual_capture:${capture.id}`)?.revision ?? 1,
      launchAuthorityDigest,
      protectedAuthorities: environment.clientEndpoints.map((endpoint) => new URL(endpoint.clientOrigin).host),
      managedCredentialAuthorities: [],
      ...(environment.clientEndpoints.length === 0
        ? {}
        : {
            root: {
              kind: "local_path" as const,
              derSha256: "5".repeat(64),
              fingerprint: "55:55:55:55",
              pemPath: "/Users/example/Library/Application Support/ViberMate/root.pem",
            },
          }),
    };
  }

  private stateTag(id: string): string {
    return `"mc_${id.padEnd(43, "A").slice(0, 43)}"`;
  }

  private requireRevision(actual: number, expected: number): void {
    if (actual !== expected) throw this.conflict();
  }

  private conflict(): ControlProblem {
    return new ControlProblem(409, "revision_conflict", "error.revision_conflict");
  }

  private notFound(): ControlProblem {
    return new ControlProblem(404, "not_found", "error.not_found");
  }
}

export async function connectPreviewControl(): Promise<ControlClient> {
  return new PreviewControlClient();
}

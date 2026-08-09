export type RuntimeState =
  | "starting"
  | "initialized"
  | "degraded"
  | "stopping"
  | "stopped"
  | "stop_failed";

export type OfflineHoldState =
  | "unbound"
  | "online"
  | "entering"
  | "held"
  | "probing"
  | "releasing"
  | "stopping";

export interface OfflineHoldSnapshot {
  readonly state: OfflineHoldState;
  readonly revision: number;
  readonly since: string;
  readonly activeActions: number;
  readonly enteringActions: number;
  readonly activeEgress: number;
  readonly queuedRequests: number;
  readonly heldBytes: number;
  readonly safeToDisconnect: boolean;
  readonly activeByKind: Readonly<Record<string, number>>;
  readonly queuedByKind: Readonly<Record<string, number>>;
  readonly lastProbeReason?: string;
}

export interface RuntimeStatus {
  readonly state: RuntimeState;
  readonly instanceId: string;
  readonly host: "desktop";
  readonly schemaRevision: number;
  readonly storage: "healthy" | "unavailable";
  readonly environmentProjection: {
    readonly state: "unrestored" | "healthy" | "unavailable";
    readonly unavailableEnvironments: readonly string[] | null;
  };
  readonly offlineHold: OfflineHoldSnapshot;
  readonly startedAt: string;
  readonly stoppedAt?: string;
  readonly stopReasonCode?: string;
}

export interface StatusResponse {
  readonly generation: string;
  readonly ready: boolean;
  readonly apiVersion: "v1";
  readonly statusKey: string;
  readonly runtime: RuntimeStatus;
}

export type EnvironmentState = "active" | "disabled";
export type ClientProtocol =
  | "anthropic_messages"
  | "openai_responses"
  | "openai_chat";
export type EnvironmentPlanMode = "original_passthrough" | "managed";
export type EnvironmentAccountMode = "client_passthrough" | "managed";
export type EnvironmentFailoverPolicy = "off" | "account_scoped_safe";

export interface EnvironmentPluginBinding {
  readonly id: string;
  readonly revision: number;
  readonly pluginId: string;
}

export interface EnvironmentBudgetPolicy {
  readonly id: string;
  readonly revision: number;
}

export interface EnvironmentEgressPolicy {
  readonly id: string;
  readonly revision: number;
  readonly mode: string;
}

export interface EnvironmentClientAdapterPolicy {
  readonly id: string;
  readonly revision: number;
}

export interface EnvironmentRouteSet {
  readonly id: string;
  readonly revision: number;
  readonly candidateRouteIds: readonly string[];
}

export interface EnvironmentProviderTarget {
  readonly id: string;
  readonly revision: number;
  readonly origin: string;
  readonly realmId: string;
  readonly capabilities: readonly string[];
}

export interface EnvironmentModelPolicy {
  readonly revision: number;
  readonly mode: string;
  readonly fixedModel: string;
}

export interface EnvironmentRouteAccountPolicy {
  readonly revision: number;
  readonly mode: EnvironmentAccountMode;
  readonly allowedRealmIds: readonly string[];
  readonly preferredAccountId: string;
  readonly candidateAccountIds: readonly string[];
  readonly accountRevisions: Readonly<Record<string, number>>;
  readonly failoverPolicy: EnvironmentFailoverPolicy;
}

export interface EnvironmentUpstreamRoute {
  readonly id: string;
  readonly revision: number;
  readonly providerTarget: EnvironmentProviderTarget;
  readonly backendProtocol: string;
  readonly accountPolicy: EnvironmentRouteAccountPolicy;
  readonly modelPolicy: EnvironmentModelPolicy;
  readonly wireProfileRef: string;
  readonly pluginBindings: readonly EnvironmentPluginBinding[];
}

export interface EnvironmentUpstreamPlan {
  readonly routes: readonly EnvironmentUpstreamRoute[];
  readonly defaultRouteId: string;
  readonly routeSet: EnvironmentRouteSet;
}

export interface EnvironmentProtocolPlan {
  readonly id: string;
  readonly revision: number;
  readonly clientProtocol: ClientProtocol;
  readonly clientAdapterPolicy: EnvironmentClientAdapterPolicy;
  readonly mode: EnvironmentPlanMode;
  readonly upstreamPlan: EnvironmentUpstreamPlan;
  readonly pluginBindings: readonly EnvironmentPluginBinding[];
}

export interface EnvironmentClientEndpoint {
  readonly id: string;
  readonly revision: number;
  readonly clientOrigin: string;
  readonly protocolPlans: readonly EnvironmentProtocolPlan[];
}

export interface EnvironmentRecord {
  readonly id: string;
  readonly name: string;
  readonly state: EnvironmentState;
  readonly revision: number;
  readonly digest: string;
  readonly systemOwned: boolean;
  readonly clientEndpoints: readonly EnvironmentClientEndpoint[];
  readonly pluginBindings: readonly EnvironmentPluginBinding[];
  readonly budgetPolicy: EnvironmentBudgetPolicy;
  readonly egressPolicy: EnvironmentEgressPolicy;
  readonly contentRecording: EnvironmentContentRecordingPolicy;
  readonly policySet: EnvironmentPolicySet;
}

export type EnvironmentContentRecordingMode = "full" | "metadata_only" | "off";

export interface EnvironmentContentRecordingPolicy {
  readonly mode: EnvironmentContentRecordingMode;
  readonly retentionDays: number;
}

export type EnvironmentToolPolicyMode = "observe" | "review" | "strict";

export interface EnvironmentPolicySet {
  readonly toolMode: EnvironmentToolPolicyMode;
}

export interface EnvironmentPage {
  readonly items: readonly EnvironmentRecord[];
}

export type ProviderAccountKind =
  | "anthropic_api_key"
  | "claude_oauth_token"
  | "openai_api_key";
export type ProviderAccountState = "active" | "disabled";
export type ProviderCredentialState =
  | "ready"
  | "disabled"
  | "credential_missing"
  | "credential_unavailable";

export interface ProviderAccountRecord {
  readonly id: string;
  readonly displayName: string;
  readonly kind: ProviderAccountKind;
  readonly realmId: string;
  readonly state: ProviderAccountState;
  readonly revision: number;
  readonly credentialState: ProviderCredentialState;
  readonly credentialEpoch: number;
}

export interface ProviderAccountPage {
  readonly items: readonly ProviderAccountRecord[];
}

export interface ProviderAccountCreateInput {
  readonly id: string;
  readonly displayName: string;
  readonly kind: ProviderAccountKind;
  readonly secret: string;
}

export interface ProviderAccountCredentialInput {
  readonly secret: string;
}

export interface ProviderAccountReference {
  readonly environmentId: string;
  readonly environmentName: string;
  readonly environmentRevision: number;
  readonly routeId: string;
  readonly routeRevision: number;
}

export interface ProviderAccountDeleteResult {
  readonly deleted: boolean;
  readonly referenceCount: number;
  readonly references: readonly ProviderAccountReference[];
}

export interface EnvironmentDraft {
  readonly environmentId: string;
  readonly baseRevision: number;
  readonly draftRevision: number;
  readonly candidateDigest: string;
  readonly candidate: EnvironmentRecord;
}

export interface EnvironmentDraftInput {
  readonly expectedDraftRevision: number;
  readonly name: string;
  readonly state: EnvironmentState;
  readonly clientEndpoints: readonly EnvironmentClientEndpoint[];
  readonly pluginBindings: readonly EnvironmentPluginBinding[];
  readonly budgetPolicy: EnvironmentBudgetPolicy;
  readonly egressPolicy: EnvironmentEgressPolicy;
  readonly contentRecording: EnvironmentContentRecordingPolicy;
  readonly policySet: EnvironmentPolicySet;
}

export type EnvironmentCompatibility =
  | "hot_switch"
  | "reconnect_required"
  | "restart_required";

export interface EnvironmentImpactCapture {
  readonly captureKind: CaptureKind;
  readonly captureId: string;
  readonly classification: EnvironmentCompatibility;
}

export interface EnvironmentImpact {
  readonly environmentId: string;
  readonly baseRevision: number;
  readonly draftRevision: number;
  readonly candidateDigest: string;
  readonly classification: EnvironmentCompatibility;
  readonly hotSwitchCount: number;
  readonly reconnectRequiredCount: number;
  readonly restartRequiredCount: number;
  readonly affected: readonly EnvironmentImpactCapture[];
}

export interface EnvironmentPublishResult {
  readonly outcome: "committed";
  readonly environment: EnvironmentRecord;
  readonly impact: EnvironmentImpact;
}

export type CaptureKind = "managed_run" | "manual_capture";

export interface ManagedRunCapture {
  readonly executableLabel: string;
  readonly cwd: string;
  readonly canonicalExecutablePath: string;
  readonly localUserLabel?: string;
  readonly machineId?: string;
  readonly machineRegistrationRevision?: number;
  readonly workspaceId?: string;
  readonly workspaceLabel?: string;
  readonly workspaceEvidence?: string;
  readonly workspaceDerivationRevision?: number;
  readonly processId?: number;
  readonly recognition: string;
  readonly expiresAt: string;
  readonly firstObservedAt?: string;
}

export interface ManualCaptureSummary {
  readonly clientClass: ManualCaptureClientClass;
  readonly lifetime: ManualCaptureLifetime;
  readonly credentialRevision: number;
  readonly expiresAt?: string;
  readonly lastObservedAt?: string;
}

export interface CaptureRecord {
  readonly key: string;
  readonly id: string;
  readonly kind: CaptureKind;
  readonly displayName: string;
  readonly state: string;
  readonly observation: string;
  readonly createdAt: string;
  readonly updatedAt: string;
  readonly managedRun?: ManagedRunCapture;
  readonly manualCapture?: ManualCaptureSummary;
}

export interface CapturePage {
  readonly items: readonly CaptureRecord[];
}

export interface CaptureAssignment {
  readonly captureKey: string;
  readonly captureId: string;
  readonly captureKind: CaptureKind;
  readonly environmentId: string;
  readonly revision: number;
  readonly source:
    | "launch"
    | "manual_create"
    | "workspace_default"
    | "operator_switch"
    | "system_transparent";
  readonly updatedAt: string;
}

export type CaptureAssignmentBoundary =
  | "no_change"
  | "hot_switch"
  | "reconnect_required"
  | "restart_required";

export interface CaptureAssignmentSwitchResult {
  readonly assignment: CaptureAssignment;
  readonly boundary: CaptureAssignmentBoundary;
  readonly closedConnections: readonly string[];
  readonly applied: boolean;
  readonly reasonCode?: "capture_restart_required";
}

export interface WorkspaceEnvironmentDefault {
	readonly machineId: string;
	readonly workspaceId: string;
	readonly environmentId: string;
	readonly environmentName: string;
	readonly revision: number;
	readonly updatedAt: string;
}

export type ActivityStatus = "succeeded" | "failed" | "canceled";

export interface FrozenEnvironmentRef {
  readonly id: string;
  readonly revision: number;
  readonly digest: string;
  readonly clientEndpointId: string;
  readonly clientEndpointRevision: number;
  readonly protocolPlanId: string;
  readonly protocolPlanRevision: number;
  readonly routeId: string;
  readonly routeRevision: number;
  readonly accountId?: string;
  readonly accountRevision?: number;
  readonly credentialEpoch?: number;
}

export interface ActivityParentRefs {
  readonly captureRunId?: string;
  readonly manualCaptureId?: string;
  readonly connectionId?: string;
  readonly exchangeId: string;
}

export interface ActivityRecord {
  readonly id: string;
  readonly occurredAt: string;
  readonly kind: "exchange";
  readonly title: string;
  readonly status: ActivityStatus;
  readonly reasonCode?: string;
  readonly source: {
    readonly kind: "capture_run" | "manual_proxy" | "system_proxy";
    readonly displayName: string;
    readonly recognition: "verified" | "configured" | "unknown";
  };
  readonly environment: FrozenEnvironmentRef;
  readonly parentRefs: ActivityParentRefs;
}

export interface ActivityPage {
  readonly items: readonly ActivityRecord[];
  readonly nextCursor?: string;
}

export interface ActivityQuery {
  readonly cursor?: string;
  readonly captureRunId?: string;
  readonly environmentId?: string;
}

export interface ExchangeDetail {
  readonly id: string;
  readonly status: ActivityStatus;
  readonly environment: FrozenEnvironmentRef;
  readonly parentRefs: ActivityParentRefs;
  readonly diagnosis?: {
    readonly providerStatus?: number;
    readonly providerField?: string;
    readonly clientField?: string;
    readonly clientPath?: string;
  };
  readonly processingTrace: {
    readonly egressProxyId?: string;
    readonly pluginRunIds: readonly string[];
    readonly attempts: readonly EgressAttemptRecord[];
    readonly result: string;
  };
  readonly content: ExchangeContentDetail;
}

export interface ExchangeContentBlock {
  readonly kind: "text" | "refusal" | "tool_call" | "tool_result" | "provider_extension";
  readonly availability: "recorded" | "omitted";
  readonly text?: string;
  readonly originalSize: number;
  readonly callId?: string;
  readonly toolName?: string;
  readonly arguments?: Readonly<Record<string, unknown>>;
  readonly toolError?: boolean;
}

export interface ExchangeContentMessage {
  readonly role: "system" | "developer" | "user" | "assistant" | "tool";
  readonly blocks: readonly ExchangeContentBlock[];
}

export interface ExchangeUsageValue {
  readonly known: boolean;
  readonly tokens?: number;
  readonly source?: string;
}

export interface ExchangeContentDetail {
  readonly state: "recorded" | "not_recorded";
  readonly mode?: "full" | "metadata_only";
  readonly recordedAt?: string;
  readonly expiresAt?: string;
  readonly request?: {
    readonly requestedModel: string;
    readonly effectiveModel: string;
    readonly maxOutputTokens: number;
    readonly stream: boolean;
    readonly messages: readonly ExchangeContentMessage[];
    readonly tools: readonly { readonly name: string; readonly namespace?: string }[];
  };
  readonly response?: {
    readonly id: string;
    readonly requestedModel: string;
    readonly effectiveModel: string;
    readonly reportedModel: string;
    readonly stopReason: "end_turn" | "max_tokens" | "tool_use" | "stop_sequence";
    readonly blocks: readonly ExchangeContentBlock[];
    readonly usage: {
      readonly inputUncached: ExchangeUsageValue;
      readonly cacheWrite: ExchangeUsageValue;
      readonly cacheRead: ExchangeUsageValue;
      readonly output: ExchangeUsageValue;
      readonly reasoning: ExchangeUsageValue;
    };
  };
}

export type ApprovalState = "pending" | "allowed" | "denied" | "canceled" | "expired";
export type ApprovalKind = "tool_intent" | "network_ask" | "client_root_ask";
export type ApprovalDecision = "allow-once" | "deny";
export type ApprovalScope = "request" | "host_port";

export interface ApprovalChoice {
  readonly decision: ApprovalDecision;
  readonly scope: ApprovalScope;
  readonly labelKey: string;
}

export interface ApprovalTarget {
  readonly host: string;
  readonly port: number;
}

export interface ApprovalView {
  readonly id: string;
  readonly revision: number;
  readonly kind: ApprovalKind;
  readonly state: ApprovalState;
  readonly risk: string;
  readonly titleKey: string;
  readonly summaryKey: string;
  readonly aggregateKey: string;
  readonly exchangeId?: string;
  readonly environmentId?: string;
  readonly environmentRevision?: number;
  readonly environmentDigest?: string;
  readonly routeId?: string;
  readonly routeRevision?: number;
  readonly target?: ApprovalTarget;
  readonly subjectRefs: readonly string[];
  readonly subjectLabels: readonly string[];
  readonly requestCount: number;
  readonly waiterCount: number;
  readonly choices: readonly ApprovalChoice[];
  readonly createdAt: string;
  readonly expiresAt: string;
  readonly resolvedAt?: string;
  readonly decision?: ApprovalDecision;
  readonly decisionScope?: ApprovalScope;
  readonly terminalReason?: string;
}

export interface ApprovalPage {
  readonly items: readonly ApprovalView[];
}

export type ConnectionDecision = "allow" | "deny" | "ask";
export type ConnectionEgressScope = "environment" | "network";
export type ConnectionEgressSource =
  | "environment_rule"
  | "environment_plugin"
  | "environment_default"
  | "network_rule"
  | "network_default";
export type ConnectionDecryption = "none" | "blind" | "mitm";
export type ConnectionPhase = "attempted" | "asked" | "decided" | "connected" | "closed" | "failed";
export type ConnectionOutcome = "completed" | "denied" | "canceled" | "failed";
export type SourceConfidence = "unknown" | "configured" | "verified";

export interface ConnectionRecord {
  readonly sequence: number;
  readonly connectionId: string;
  readonly ingressId?: string;
  readonly sourceLabel?: string;
  readonly sourceConfidence: SourceConfidence;
  readonly environmentId?: string;
  readonly environmentName?: string;
  readonly environmentRevision?: number;
  readonly clientEndpointId?: string;
  readonly clientEndpointRevision?: number;
  readonly requestedHost: string;
  readonly observedSni?: string;
  readonly routeHost?: string;
  readonly ip?: string;
  readonly port: number;
  readonly decision?: ConnectionDecision;
  readonly ruleId?: string;
  readonly credentialBindingId?: string;
  readonly egressScope?: ConnectionEgressScope;
  readonly egressSource?: ConnectionEgressSource;
  readonly egressRuleId?: string;
  readonly egressSelectorRunId?: string;
  readonly egressProxyId?: string;
  readonly egressPolicyRevision?: number;
  readonly decryption: ConnectionDecryption;
  readonly phase: ConnectionPhase;
  readonly bytesUp: number;
  readonly bytesDown: number;
  readonly startedAt: string;
  readonly endedAt?: string;
  readonly outcome?: ConnectionOutcome;
  readonly errorClass?: string;
}

export interface ConnectionPage {
  readonly items: readonly ConnectionRecord[];
  readonly nextCursor?: string;
}

export type EgressPurpose =
  | "provider_attempt"
  | "route_operation"
  | "original_origin"
  | "agent_probe"
  | "blind_tunnel"
  | "auxiliary_llm"
  | "language_transform"
  | "plugin_catalog_sync"
  | "plugin_artifact_fetch"
  | "update";
export type EgressOutcome = "completed" | "failed" | "canceled";

export interface EgressAttemptRecord {
  readonly sequence: number;
  readonly id: string;
  readonly connectionId?: string;
  readonly purpose: EgressPurpose;
  readonly payloadClass: "none" | "control" | "client_data" | "client_semantic" | "opaque_tunnel" | "runtime";
  readonly parent: { readonly kind: string; readonly id?: string; readonly exchangeId?: string };
  readonly caller: "core" | "plugin";
  readonly callerId?: string;
  readonly targetOrigin: string;
  readonly decision: {
    readonly policyId?: string;
    readonly policyRevision?: number;
    readonly authority: "environment" | "network" | "runtime";
    readonly ruleId?: string;
    readonly proxyId?: string;
  };
  readonly reusedTransport: boolean;
  readonly startedAt: string;
  readonly terminal: boolean;
  readonly outcome?: EgressOutcome;
  readonly errorClass?: string;
  readonly bytesOut: number;
  readonly bytesIn: number;
  readonly completedAt?: string;
}

export interface EgressAttemptPage {
  readonly items: readonly EgressAttemptRecord[];
  readonly nextCursor?: string;
}

export interface ConnectionRule {
  readonly id: string;
  readonly priority: number;
  readonly decision: ConnectionDecision;
  readonly match: string;
  readonly host?: string;
  readonly port?: number;
}

export interface ConnectionRuleSet {
  readonly revision: number;
  readonly rules: readonly ConnectionRule[];
  readonly mode: ConnectionPolicyMode;
}

export interface ConnectionRuleSetInput {
  readonly rules: readonly ConnectionRule[];
  readonly mode: ConnectionPolicyMode;
}

export type ConnectionPolicyMode = "monitor" | "ask_unknown" | "deny_unknown";

export type ManualCaptureClientClass = "cli" | "desktop_app" | "other";
export type ManualCaptureLifetime = "temporary" | "until_revoked";
export type ManualCaptureState = "active" | "revoked" | "expired";
export type ManualCaptureObservation = "waiting_for_traffic" | "observed";

export interface ManualCaptureRoot {
  readonly kind: "local_path";
  readonly derSha256: string;
  readonly fingerprint: string;
  readonly pemPath: string;
}

export interface ManualCaptureContext {
  readonly confirmationToken: string;
  readonly proxyAddress: string;
  readonly environmentId: string;
  readonly environmentRevision: number;
  readonly environmentDigest: string;
  readonly launchAuthorityDigest: string;
  readonly protectedAuthorities: readonly string[];
  readonly managedCredentialAuthorities: readonly string[];
  readonly root?: ManualCaptureRoot;
  readonly defaultTemporarySeconds: number;
  readonly maxTemporarySeconds: number;
}

export interface ManualCaptureRecord {
  readonly id: string;
  readonly displayName: string;
  readonly clientClass: ManualCaptureClientClass;
  readonly lifetime: ManualCaptureLifetime;
  readonly state: ManualCaptureState;
  readonly observation: ManualCaptureObservation;
  readonly createdAt: string;
  readonly updatedAt: string;
  readonly expiresAt?: string;
  readonly lastObservedAt?: string;
}

export interface ManualCapturePage { readonly items: readonly ManualCaptureRecord[] }

export interface ManualCaptureCreateInput {
  readonly environmentId: string;
  readonly displayName: string;
  readonly clientClass: ManualCaptureClientClass;
  readonly lifetime: ManualCaptureLifetime;
  readonly expiresInSeconds?: number;
  readonly confirmationToken: string;
}

export interface ManualCaptureGrant {
  readonly capture: ManualCaptureRecord;
  readonly proxyAddress: string;
  readonly proxyUsername: string;
  readonly proxyPassword: string;
  readonly environmentId: string;
  readonly assignmentRevision: number;
  readonly launchAuthorityDigest: string;
  readonly protectedAuthorities: readonly string[];
  readonly managedCredentialAuthorities: readonly string[];
  readonly root?: ManualCaptureRoot;
}

export interface ManualCaptureStateTag {
  readonly capture: ManualCaptureRecord;
  readonly stateTag: string;
}

export interface ManualCaptureGrantStateTag {
  readonly grant: ManualCaptureGrant;
  readonly stateTag: string;
}

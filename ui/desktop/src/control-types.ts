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
  readonly accessProjection: {
    readonly state: "healthy" | "unavailable";
    readonly unavailableAccessCount: number;
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

export interface ActivityRecord {
  readonly id: string;
  readonly occurredAt: string;
  readonly accessId: string;
  readonly status: string;
}

export interface ActivityPage {
  readonly items: readonly ActivityRecord[];
  readonly nextCursor?: string;
}

export interface ExchangeProcessingTrace {
  readonly upstreamProfileId?: string;
  readonly credentialId?: string;
  readonly egressProxyId?: string;
  readonly pluginRunIds: readonly string[];
  readonly attemptIds: readonly string[];
  readonly result: string;
}

export interface ExchangeDetail {
  readonly id: string;
  readonly accessId: string;
  readonly status: string;
  readonly processingTrace: ExchangeProcessingTrace;
}

export type ApprovalState =
  | "pending"
  | "allowed"
  | "denied"
  | "canceled"
  | "expired";

export type ApprovalKind =
  | "tool_intent"
  | "network_ask"
  | "client_root_ask";

export type ApprovalDecision = "allow-once" | "deny";

/**
 * A scope says how far an answer reaches. `request` answers only the question
 * in front of the person; `host_port` also writes a connection rule for
 * exactly the host and port that were asked about.
 */
export type ApprovalScope = "request" | "host_port";

export interface ApprovalChoice {
  readonly decision: ApprovalDecision;
  readonly scope: ApprovalScope;
  /** The sentence a person reads before taking this choice. */
  readonly labelKey: string;
}

/** The connection a question is about, when it is about one. */
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
  /** Present only on a question decided against a resolved Access plan. */
  readonly exchangeId?: string;
  readonly accessId?: string;
  readonly planRevision?: number;
  readonly planHash?: string;
  readonly target?: ApprovalTarget;
  readonly subjectRefs: readonly string[];
  readonly subjectLabels: readonly string[];
  /** How many callers this one question is answering for. */
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

export interface AccessApplyInput {
  readonly expectedRevision: number;
  readonly access: {
    readonly id: string;
    readonly name: string;
    readonly description: string;
    readonly status: "enabled";
    readonly agentEndpointId: string;
    readonly defaultRouteSetId: string;
    readonly profileIds: readonly string[];
    readonly egressPolicyId: string;
  };
  readonly agentEndpoint: {
    readonly id: string;
    readonly clientOrigin: string;
    readonly clientDialect: "anthropic-messages" | "openai-responses";
  };
  readonly profiles: readonly {
    readonly id: string;
    readonly name: string;
    readonly description: string;
    readonly backendDialect: "anthropic-messages" | "openai-chat";
    readonly targetId: string;
    readonly upstreamWireProfileRef: "follow-client" | "claude-code";
    readonly defaultModelPolicy: {
      readonly mode: "fixed";
      readonly fixedModel: string;
    };
    readonly accountBindingIds: readonly string[];
    readonly defaultAccountBindingId: string;
  }[];
  readonly providerTargets: readonly {
    readonly id: string;
    readonly profileId: string;
    readonly origin: string;
    readonly protocol: "anthropic-messages" | "openai-chat";
    readonly capabilities: readonly ["messages", "streaming", "tool_calls"];
  }[];
  readonly accountBindings: readonly {
    readonly id: string;
    readonly profileId: string;
    readonly label: string;
    readonly secretRef: string;
    readonly authDriverRef: "anthropic_api_key" | "static_header";
    readonly enabled: true;
  }[];
  readonly routeSets: readonly {
    readonly id: string;
    readonly candidateProfileIds: readonly string[];
  }[];
  readonly egressPolicy: {
    readonly id: string;
    readonly mode: "direct";
  };
  readonly pluginPlan: {
    readonly mode: "pass_through";
    readonly bindingIds: readonly [];
  };
}

export type AccessStatus = "draft" | "enabled" | "disabled";

export interface AccessDirectoryItem {
  readonly accessId: string;
  readonly name: string;
  readonly description: string;
  readonly status: AccessStatus;
  readonly revision: number;
  readonly clientOrigin: string;
  readonly clientDialect:
    | "anthropic-messages"
    | "openai-chat"
    | "openai-responses";
}

export interface AccessDirectoryPage {
  readonly items: readonly AccessDirectoryItem[];
}

export type AccessDialect =
  | "anthropic-messages"
  | "openai-chat"
  | "openai-responses";

export type AccessModelPolicy =
  | {
      readonly mode: "passthrough";
    }
  | {
      readonly mode: "fixed";
      readonly fixedModel: string;
    }
  | {
      readonly mode: "map";
      readonly mappingRef: string;
    };

/**
 * The durable Access configuration safe to expose to the desktop UI.
 * Credential values and their internal SecretRefs intentionally have no
 * representation here; credential changes use the scoped secret action.
 */
export interface AccessDetail {
  readonly revision: number;
  readonly access: {
    readonly id: string;
    readonly name: string;
    readonly description: string;
    readonly status: AccessStatus;
    readonly agentEndpointId: string;
    readonly defaultRouteSetId: string;
    readonly profileIds: readonly string[];
    readonly egressPolicyId: string;
  };
  readonly agentEndpoint: {
    readonly id: string;
    readonly clientOrigin: string;
    readonly clientDialect: AccessDialect;
  };
  readonly profiles: readonly {
    readonly id: string;
    readonly kind: "original_passthrough" | "managed";
    readonly credentialSource: "client_passthrough" | "managed_account";
    readonly processingMode: "observe_only" | "managed";
    readonly name: string;
    readonly description: string;
    readonly backendDialect: AccessDialect;
    readonly targetId: string;
    readonly upstreamWireProfileRef: string;
    readonly defaultModelPolicy: AccessModelPolicy;
    readonly accountBindingIds: readonly string[];
    readonly defaultAccountBindingId: string;
  }[];
  readonly providerTargets: readonly {
    readonly id: string;
    readonly profileId: string;
    readonly origin: string;
    readonly protocol: AccessDialect;
    readonly capabilities: readonly (
      | "messages"
      | "streaming"
      | "tool_calls"
    )[];
  }[];
  readonly accountBindings: readonly {
    readonly id: string;
    readonly profileId: string;
    readonly label: string;
    readonly authDriverRef: string;
    readonly enabled: boolean;
    readonly secretHandling: "preserve_existing";
  }[];
  readonly routeSets: readonly {
    readonly id: string;
    readonly candidateProfileIds: readonly string[];
    readonly fallback: "disabled" | "pre_first_byte_idempotent_only";
  }[];
  readonly egressPolicy: {
    readonly id: string;
    readonly mode: "direct";
  };
  readonly pluginPlan: {
    readonly mode: "pass_through";
    readonly bindingIds: readonly string[];
  };
}

export type AccessApplyResponse =
  | {
      readonly outcome: "committed";
      readonly revision: number;
      readonly applicationState: "active";
      readonly planHash: string;
    }
  | {
      readonly outcome: "committed";
      readonly revision: number;
      readonly applicationState: "unavailable";
    };

/**
 * One provider route added to an existing tool. The preset is deliberately
 * closed: the server owns protocol, authentication-header, and SecretRef
 * selection, while the browser supplies only values a person can review.
 */
export type AccessCandidateProvider =
  | "anthropic"
  | "anthropic-compatible"
  | "openai"
  | "openai-compatible";

export type AccessAddCandidateInput =
  | {
      readonly name: string;
      readonly provider: "anthropic";
      readonly model: string;
      readonly authDriverRef?: "anthropic_api_key";
      readonly upstreamPresentation: "follow-client" | "claude-code";
    }
  | {
      readonly name: string;
      readonly provider: "anthropic-compatible";
      readonly baseUrl: string;
      readonly model: string;
      readonly authDriverRef?: "anthropic_api_key" | "static_header";
      readonly upstreamPresentation: "follow-client" | "claude-code";
    }
  | {
      readonly name: string;
      readonly provider: "openai";
      readonly model: string;
      readonly authDriverRef?: "static_header";
      readonly upstreamPresentation: "follow-client" | "claude-code";
    }
  | {
      readonly name: string;
      readonly provider: "openai-compatible";
      readonly baseUrl: string;
      readonly model: string;
      readonly authDriverRef?: "anthropic_api_key" | "static_header";
      readonly upstreamPresentation: "follow-client" | "claude-code";
    };

export type AccessAddCandidateResponse = AccessApplyResponse & {
  readonly candidate: {
    readonly profileId: string;
    readonly credentialId: string;
  };
};

export interface AccessPlanSummary {
  readonly accessId: string;
  readonly revision: number;
  readonly planHash: string;
  readonly profiles: readonly string[];
  readonly accountBindings: readonly {
    readonly id: string;
    readonly profileId: string;
  }[];
}

export interface CredentialView {
  readonly credentialId: string;
  readonly profileId: string;
  readonly secretState: "configured" | "missing" | "unavailable";
  readonly secretRevision: number;
}

export type ConnectionDecision = "allow" | "deny" | "ask";

export type ConnectionEgressScope = "access" | "network";

export type ConnectionEgressSource =
  | "access_rule"
  | "access_plugin"
  | "access_default"
  | "network_rule"
  | "network_default";

export type ConnectionDecryption = "none" | "blind" | "mitm";

export type ConnectionPhase =
  | "attempted"
  | "asked"
  | "decided"
  | "connected"
  | "closed"
  | "failed";

export type ConnectionOutcome =
  | "completed"
  | "denied"
  | "canceled"
  | "failed";

export type SourceConfidence = "unknown" | "configured" | "verified";

/**
 * One connection as the runtime recorded it. Design 06 4.1 bounds this: it
 * says who connected where and how much crossed, never what was said, so
 * there is no path, header, or body here and none in the record behind it.
 */
export interface ConnectionRecord {
  readonly sequence: number;
  readonly connectionId: string;
  readonly ingressId?: string;
  readonly sourceLabel?: string;
  readonly sourceConfidence: SourceConfidence;
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
  | "profile_operation"
  | "original_origin"
  | "agent_probe"
  | "blind_tunnel"
  | "auxiliary_llm"
  | "language_transform"
  | "plugin_catalog_sync"
  | "plugin_artifact_fetch"
  | "update";

export type EgressOutcome = "completed" | "failed" | "canceled";

/** Where one request actually went, and how much crossed. */
export interface EgressAttemptRecord {
  readonly sequence: number;
  readonly id: string;
  readonly connectionId?: string;
  readonly purpose: EgressPurpose;
  readonly payloadClass: string;
  readonly parent: {
    readonly kind: string;
    readonly id?: string;
    readonly exchangeId?: string;
  };
  readonly caller: string;
  readonly callerId?: string;
  /** An origin: scheme, host, and port. Never a URL. */
  readonly targetOrigin: string;
  readonly decision: {
    readonly policyId?: string;
    readonly policyRevision?: number;
    readonly authority: string;
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

export type CaptureRunState =
  | "created"
  | "attached"
  | "finished"
  | "expired"
  | "revoked";

export type CaptureRunObservation = "waiting_for_traffic" | "observed";

/**
 * Whether this build has release evidence for the client that was launched.
 *
 * `unverified` is a client the catalog knows at a version it does not, and
 * whose publisher was not confirmed: it was started without a trust root and
 * its requests will fail to connect. `recognized` is the same missing version
 * evidence with a verified publisher, started with a trust root because the
 * person allowed it once — it is not a claim that this version was tested.
 */
export type CaptureRunRecognition =
  | "unknown"
  | "unverified"
  | "recognized"
  | "verified";

export type CaptureRunAdapterState = "verified" | "generic" | "failed";

export interface CaptureRunAdapterEvidence {
  readonly id: string;
  readonly revision: number;
  readonly version: string;
  readonly catalogRevision: number;
  readonly source: "prelaunch_digest_catalog";
  readonly installShape: string;
  readonly launchRecipe: string;
}

export interface CaptureRunRecord {
  readonly id: string;
  readonly executableLabel: string;
  readonly cwd: string;
  /** Untrusted launcher environment label for display only. */
  readonly localUserLabel?: string;
  readonly machineId?: string;
  readonly workspaceId?: string;
  readonly workspaceLabel?: string;
  readonly workspaceEvidence?: "local_launcher" | "registered_companion";
  readonly processId?: number;
  readonly state: CaptureRunState;
  readonly observation: CaptureRunObservation;
  /** Compatibility field on the Desktop audit projection. */
  readonly recognition: CaptureRunRecognition;
  readonly clientAdapterState: CaptureRunAdapterState;
  readonly clientRecognition: CaptureRunRecognition;
  readonly catalogRevision: number;
  readonly clientAdapter?: CaptureRunAdapterEvidence;
  readonly clientAdapterReason?: string;
  readonly createdAt: string;
  readonly expiresAt: string;
}

export interface CaptureRunPage {
  readonly items: readonly CaptureRunRecord[];
}

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
  readonly root: ManualCaptureRoot;
  readonly defaultTemporarySeconds: number;
  readonly maxTemporarySeconds: number;
}

export interface ManualCaptureRecord {
  readonly id: string;
  readonly ingressProfileId: string;
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

export interface ManualCapturePage {
  readonly items: readonly ManualCaptureRecord[];
}

export interface ManualCaptureCreateInput {
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
  readonly root: ManualCaptureRoot;
}

export interface ManualCaptureStateTag {
  readonly capture: ManualCaptureRecord;
  /** Opaque concurrency token. It is not a product version. */
  readonly stateTag: string;
}

export interface ManualCaptureGrantStateTag {
  readonly grant: ManualCaptureGrant;
  /** Opaque concurrency token. It is not a product version. */
  readonly stateTag: string;
}

export type WorkspaceRouteState =
  | "active"
  | "workspace_route_unavailable";

export type WorkspaceRouteAuthPresentation =
  | "vibermate_account"
  | "client_oauth"
  | "client_auth"
  | "none";

export interface WorkspaceRouteRunSummary {
  readonly runId: string;
  readonly clientLabel: string;
  /** Untrusted launcher environment label for display only. */
  readonly localUserLabel?: string;
  readonly state: "active" | "idle";
  readonly startedAt: string;
  readonly lastActivityAt: string;
}

export interface WorkspaceRouteProfileOption {
  readonly profileId: string;
  readonly kind: "original_passthrough" | "managed";
  readonly label: string;
  readonly modelPresentation: string;
  readonly authPresentation: WorkspaceRouteAuthPresentation;
  readonly authLabel: string;
  readonly available: boolean;
}

export interface WorkspaceRouteBinding {
  readonly id: string;
  readonly accessId: string;
  readonly machineId: string;
  readonly machineShortId: string;
  readonly machineDisplayName: string;
  readonly machineRegistrationRevision: number;
  readonly workspaceId: string;
  readonly workspaceLabel: string;
  readonly workspaceEvidence: "local_launcher" | "registered_companion";
  readonly profileId: string;
  readonly revision: number;
  readonly state: WorkspaceRouteState;
  readonly activeRunCount: number;
  readonly activeRuns: readonly WorkspaceRouteRunSummary[];
  readonly pinnedRequestCount: number;
  readonly approvedProfiles: readonly WorkspaceRouteProfileOption[];
  readonly updatedAt: string;
}

export interface WorkspaceRouteBindingPage {
  readonly items: readonly WorkspaceRouteBinding[];
}

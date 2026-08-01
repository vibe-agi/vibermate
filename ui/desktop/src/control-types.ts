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
  readonly sequence: number;
  readonly id: string;
  readonly occurredAt: string;
  readonly kind: string;
  readonly accessId?: string;
  readonly subjectId: string;
  readonly status: string;
  readonly reasonCode?: string;
  readonly diagnosis?: ActivityDiagnosis;
  readonly transport?: TransportEvidence;
}

/**
 * What a failed request can say about itself without saying what it
 * contained: an HTTP status, a field name, and a path of field names and
 * indices. No value, credential, or provider text appears here.
 */
export interface ActivityDiagnosis {
  readonly providerStatus?: number;
  readonly providerField?: string;
  readonly clientField?: string;
  readonly clientPath?: string;
}

export interface TransportProfileEvidence {
  readonly ref: string;
  readonly revision: number;
  readonly source: "observed_client" | "named_profile" | "standard";
}

export interface TransportEvidence {
  readonly requested: TransportProfileEvidence;
  readonly effective?: TransportProfileEvidence;
  readonly fallbackChain: readonly TransportProfileEvidence[];
  readonly fallbackReason?: string;
  readonly clientOfferedAlpn: readonly string[];
  readonly downstreamNegotiatedAlpn?: string;
  readonly upstreamOfferedAlpn: readonly string[];
  readonly upstreamNegotiatedAlpn?: string;
  readonly httpTransport?: "http1" | "http2";
}

export interface ActivityPage {
  readonly items: readonly ActivityRecord[];
  readonly nextBeforeSequence?: number;
}

export type ApprovalState =
  | "pending"
  | "allowed"
  | "denied"
  | "canceled"
  | "expired";

export type ApprovalKind = "tool_intent" | "network_ask";

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
    readonly clientDialect: "anthropic-messages";
  };
  readonly profiles: readonly {
    readonly id: string;
    readonly name: string;
    readonly description: string;
    readonly backendDialect: "openai-chat";
    readonly targetId: string;
    readonly transportProfileRef: "observed-client-strict-h1";
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
    readonly protocol: "openai-chat";
    readonly capabilities: readonly ["messages", "streaming", "tool_calls"];
  }[];
  readonly accountBindings: readonly {
    readonly id: string;
    readonly profileId: string;
    readonly label: string;
    readonly secretRef: string;
    readonly authDriverRef: "static_header";
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

export interface AccessApplyResponse {
  readonly outcome: "committed";
  readonly revision: number;
  readonly planHash: string;
}

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

export type ConnectionDecision = "allow" | "deny";

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
 * `unverified` is a client the catalog knows at a version it does not: it was
 * started without a trust root and its requests will fail to connect.
 */
export type CaptureRunRecognition = "unknown" | "unverified" | "verified";

export interface CaptureRunRecord {
  readonly id: string;
  readonly executableLabel: string;
  readonly cwd: string;
  readonly processId?: number;
  readonly state: CaptureRunState;
  readonly observation: CaptureRunObservation;
  readonly recognition: CaptureRunRecognition;
  readonly createdAt: string;
  readonly expiresAt: string;
}

export interface CaptureRunPage {
  readonly items: readonly CaptureRunRecord[];
}

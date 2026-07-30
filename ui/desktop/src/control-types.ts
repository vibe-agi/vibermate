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
  readonly transport?: TransportEvidence;
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

export interface ApprovalView {
  readonly id: string;
  readonly revision: number;
  readonly kind: "tool-intent";
  readonly state: ApprovalState;
  readonly risk: string;
  readonly titleKey: string;
  readonly summaryKey: string;
  readonly exchangeId: string;
  readonly accessId: string;
  readonly planRevision: number;
  readonly planHash: string;
  readonly toolCallIds: readonly string[];
  readonly toolNames: readonly string[];
  readonly choices: readonly {
    readonly decision: "allow-once" | "deny";
    readonly scope: "request";
  }[];
  readonly createdAt: string;
  readonly expiresAt: string;
  readonly resolvedAt?: string;
  readonly decision?: "allow-once" | "deny";
  readonly decisionScope?: "request";
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

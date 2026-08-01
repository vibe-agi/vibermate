import type {
  AccessApplyInput,
  AccessApplyResponse,
  AccessPlanSummary,
  ActivityPage,
  ApprovalPage,
  ApprovalChoice,
  ApprovalView,
  ConnectionPage,
  EgressAttemptPage,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
} from "./control-types.ts";

const capabilityBytes = 32;
const maximumResponseBytes = 2 * 1024 * 1024;

export interface DesktopSession {
  readonly schema: "vibermate-app-session-v1";
  readonly baseUrl: string;
  readonly readToken: string;
  readonly writeToken: string;
  readonly instanceId: string;
  readonly expiresAt: string;
}

export class ControlProblem extends Error {
  readonly status: number;
  readonly reasonCode: string;
  readonly messageKey: string;

  constructor(status: number, reasonCode: string, messageKey: string) {
    super(`Control API failed with status ${status} and reason ${reasonCode}`);
    this.name = "ControlProblem";
    this.status = status;
    this.reasonCode = reasonCode;
    this.messageKey = messageKey;
  }
}

export class ControlContractError extends Error {
  readonly reasonCode = "control_contract_invalid";
  readonly messageKey = "error.control_invalid_response";

  constructor() {
    super("Desktop control response did not match its wire contract");
    this.name = "ControlContractError";
  }
}

export interface ControlClient {
  status(signal?: AbortSignal): Promise<StatusResponse>;
  offlineHold(signal?: AbortSignal): Promise<OfflineHoldSnapshot>;
  enterOfflineHold(
    expectedRevision: number,
    signal?: AbortSignal,
  ): Promise<OfflineHoldSnapshot>;
  resumeOfflineHold(
    expectedRevision: number,
    signal?: AbortSignal,
  ): Promise<OfflineHoldSnapshot>;
  activities(signal?: AbortSignal): Promise<ActivityPage>;
  approvals(signal?: AbortSignal): Promise<ApprovalPage>;
  connections(signal?: AbortSignal): Promise<ConnectionPage>;
  egressAttempts(signal?: AbortSignal): Promise<EgressAttemptPage>;
  decideApproval(
    approval: ApprovalView,
    choice: ApprovalChoice,
    signal?: AbortSignal,
  ): Promise<ApprovalView>;
  applyAccess(
    accessId: string,
    input: AccessApplyInput,
    signal?: AbortSignal,
  ): Promise<AccessApplyResponse>;
  accessPlan(
    accessId: string,
    signal?: AbortSignal,
  ): Promise<AccessPlanSummary>;
  credential(
    accessId: string,
    profileId: string,
    credentialId: string,
    signal?: AbortSignal,
  ): Promise<CredentialView>;
  replaceCredentialSecret(
    accessId: string,
    profileId: string,
    credentialId: string,
    expectedRevision: number,
    secret: string,
    signal?: AbortSignal,
  ): Promise<CredentialView>;
}

type Fetch = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

export function createControlClient(
  session: DesktopSession,
  fetchImplementation: Fetch = globalThis.fetch.bind(globalThis),
  now: () => number = Date.now,
): ControlClient {
  const baseUrl = validateSession(session, now);

  async function request<T>(
    method: "GET" | "POST" | "PUT",
    path: string,
    body: unknown,
    expectedRevision: number | undefined,
    signal: AbortSignal | undefined,
  ): Promise<T> {
    const destination = new URL(path, `${baseUrl}/`);
    if (destination.origin !== baseUrl || !destination.pathname.startsWith("/api/v1/")) {
      throw new Error("Desktop control request escaped the bootstrap origin");
    }
    const headers = new Headers({
      Accept: "application/json, application/problem+json",
      Authorization: `Bearer ${
        method === "GET" ? session.readToken : session.writeToken
      }`,
    });
    let encodedBody: string | undefined;
    if (body !== undefined) {
      encodedBody = JSON.stringify(body);
      headers.set("Content-Type", "application/json");
    }
    if (expectedRevision !== undefined) {
      headers.set("If-Match", String(expectedRevision));
      headers.set("Idempotency-Key", createIdempotencyKey());
    }
    const requestOptions: RequestInit = {
      method,
      headers,
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      referrerPolicy: "no-referrer",
    };
    if (encodedBody !== undefined) {
      requestOptions.body = encodedBody;
    }
    if (signal !== undefined) {
      requestOptions.signal = signal;
    }
    const response = await fetchImplementation(destination, requestOptions);
    const payload = await readBoundedResponse(response);
    if (!response.ok) {
      throw decodeProblem(response.status, payload);
    }
    const contentType = response.headers.get("Content-Type") ?? "";
    if (!contentType.toLowerCase().startsWith("application/json")) {
      throw new ControlContractError();
    }
    try {
      return JSON.parse(payload) as T;
    } catch {
      throw new ControlContractError();
    }
  }

  return {
    status: (signal) =>
      request<StatusResponse>("GET", "/api/v1/status", undefined, undefined, signal),
    offlineHold: (signal) =>
      request<OfflineHoldSnapshot>(
        "GET",
        "/api/v1/offline-hold",
        undefined,
        undefined,
        signal,
      ),
    enterOfflineHold: (revision, signal) =>
      request<OfflineHoldSnapshot>(
        "POST",
        "/api/v1/offline-hold/actions/enter",
        undefined,
        revision,
        signal,
      ),
    resumeOfflineHold: (revision, signal) =>
      request<OfflineHoldSnapshot>(
        "POST",
        "/api/v1/offline-hold/actions/resume",
        undefined,
        revision,
        signal,
      ),
    activities: async (signal) =>
      requireItemsPage<ActivityPage>(
        await request<unknown>(
          "GET",
          "/api/v1/activities?limit=50",
          undefined,
          undefined,
          signal,
        ),
      ),
    approvals: async (signal) =>
      requireItemsPage<ApprovalPage>(
        await request<unknown>(
          "GET",
          "/api/v1/approvals?state=pending&limit=50",
          undefined,
          undefined,
          signal,
        ),
      ),
    connections: async (signal) =>
      requireItemsPage<ConnectionPage>(
        await request<unknown>(
          "GET",
          "/api/v1/connections?limit=50",
          undefined,
          undefined,
          signal,
        ),
      ),
    egressAttempts: async (signal) =>
      requireItemsPage<EgressAttemptPage>(
        await request<unknown>(
          "GET",
          "/api/v1/egress-attempts?limit=50",
          undefined,
          undefined,
          signal,
        ),
      ),
    decideApproval: (approval, choice, signal) =>
      request<ApprovalView>(
        "POST",
        `/api/v1/approvals/${encodeURIComponent(approval.id)}/actions/decide`,
        {
          decision: choice.decision,
          scope: choice.scope,
          ...(choice.decision === "deny"
            ? { reasonCode: "user_denied" }
            : {}),
        },
        approval.revision,
        signal,
      ),
    applyAccess: (accessId, input, signal) =>
      request<AccessApplyResponse>(
        "PUT",
        `/api/v1/accesses/${encodeURIComponent(accessId)}/actions/apply`,
        input,
        input.expectedRevision,
        signal,
      ),
    accessPlan: (accessId, signal) =>
      request<AccessPlanSummary>(
        "GET",
        `/api/v1/accesses/${encodeURIComponent(accessId)}/plan`,
        undefined,
        undefined,
        signal,
      ),
    credential: (accessId, profileId, credentialId, signal) =>
      request<CredentialView>(
        "GET",
        credentialPath(accessId, profileId, credentialId),
        undefined,
        undefined,
        signal,
      ),
    replaceCredentialSecret: (
      accessId,
      profileId,
      credentialId,
      expectedRevision,
      secret,
      signal,
    ) =>
      request<CredentialView>(
        "POST",
        `${credentialPath(accessId, profileId, credentialId)}/actions/replace-secret`,
        { secret },
        expectedRevision,
        signal,
      ),
  };
}

function credentialPath(
  accessId: string,
  profileId: string,
  credentialId: string,
): string {
  return `/api/v1/accesses/${encodeURIComponent(accessId)}/profiles/${encodeURIComponent(
    profileId,
  )}/credentials/${encodeURIComponent(credentialId)}`;
}

function validateSession(session: DesktopSession, now: () => number): string {
  if (
    session.schema !== "vibermate-app-session-v1" ||
    session.instanceId.length === 0 ||
    !validCapability(session.readToken) ||
    !validCapability(session.writeToken) ||
    session.readToken === session.writeToken
  ) {
    throw new Error("Desktop control session is invalid");
  }
  const expiresAt = Date.parse(session.expiresAt);
  if (!Number.isFinite(expiresAt) || expiresAt <= now()) {
    throw new Error("Desktop control session is expired");
  }
  const parsed = new URL(session.baseUrl);
  if (
    parsed.protocol !== "http:" ||
    parsed.hostname !== "127.0.0.1" ||
    parsed.port === "" ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.pathname !== "/" ||
    parsed.search !== "" ||
    parsed.hash !== "" ||
    parsed.origin !== session.baseUrl
  ) {
    throw new Error("Desktop control base URL is not literal IPv4 loopback");
  }
  return parsed.origin;
}

function validCapability(value: string): boolean {
  if (!/^[A-Za-z0-9_-]+$/u.test(value)) {
    return false;
  }
  try {
    const normalized = value.replaceAll("-", "+").replaceAll("_", "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    return atob(padded).length === capabilityBytes;
  } catch {
    return false;
  }
}

function createIdempotencyKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24));
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

async function readBoundedResponse(response: Response): Promise<string> {
  const declared = Number(response.headers.get("Content-Length") ?? "0");
  if (declared > maximumResponseBytes) {
    throw new ControlContractError();
  }
  const payload = await response.text();
  if (new TextEncoder().encode(payload).byteLength > maximumResponseBytes) {
    throw new ControlContractError();
  }
  return payload;
}

function requireItemsPage<T>(value: unknown): T {
  if (
    value === null ||
    typeof value !== "object" ||
    !Array.isArray((value as { readonly items?: unknown }).items)
  ) {
    throw new ControlContractError();
  }
  return value as T;
}

function decodeProblem(status: number, payload: string): ControlProblem {
  try {
    const problem = JSON.parse(payload) as {
      status?: unknown;
      reasonCode?: unknown;
      messageKey?: unknown;
    };
    if (
      problem.status === status &&
      typeof problem.reasonCode === "string" &&
      problem.reasonCode.length > 0 &&
      typeof problem.messageKey === "string" &&
      problem.messageKey.length > 0
    ) {
      return new ControlProblem(status, problem.reasonCode, problem.messageKey);
    }
  } catch {
    // The stable fallback below deliberately excludes the response payload.
  }
  return new ControlProblem(status, "invalid_control_response", "error.unknown");
}

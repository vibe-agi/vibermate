import type {
  AccessDetail,
  AccessDirectoryPage,
  AccessAddCandidateInput,
  AccessAddCandidateResponse,
  AccessApplyInput,
  AccessApplyResponse,
  AccessDeletionPreview,
  AccessDeletionResponse,
  AccessPlanSummary,
  AccessStatus,
  ActivityPage,
  ExchangeDetail,
  ApprovalPage,
  ApprovalChoice,
  ApprovalView,
  CaptureRunPage,
  ConnectionPage,
  EgressAttemptPage,
  CredentialView,
  ManualCaptureContext,
  ManualCaptureCreateInput,
  ManualCaptureGrant,
  ManualCaptureGrantStateTag,
  ManualCapturePage,
  ManualCaptureRecord,
  ManualCaptureStateTag,
  OfflineHoldSnapshot,
  StatusResponse,
  WorkspaceRouteBinding,
  WorkspaceRouteBindingPage,
} from "./control-types.ts";

const capabilityBytes = 32;
const maximumControlRequestBytes = 1 * 1024 * 1024;
const maximumResponseBytes = 2 * 1024 * 1024;
const maximumActiveMutationCalls = 16;
const maximumUnresolvedMutations = 16;
const requestTimeoutMilliseconds = 10_000;
const sessionStatePath = "/api/v1/auth/sessions/current";
const sessionRenewalPath = "/api/v1/auth/sessions/refresh";
const sessionStateSchema = "vibermate-app-session-state-v1";
const sessionRotationSchema = "vibermate-app-session-rotation-v1";
const maximumRenewalLeadMilliseconds = 5 * 60 * 1_000;
const minimumRenewalLeadMilliseconds = 1_000;

export interface DesktopSession {
  readonly schema: "vibermate-app-session-v1";
  readonly baseUrl: string;
  readonly readToken: string;
  readonly writeToken: string;
  readonly instanceId: string;
  readonly expiresAt: string;
}

interface ActiveDesktopSession extends DesktopSession {
  readonly revision: number;
  readonly expiresAtMilliseconds: number;
  readonly renewAfterMilliseconds: number;
}

interface SessionStateResponse {
  readonly schema: typeof sessionStateSchema;
  readonly revision: number;
  readonly expiresAt: string;
}

interface PendingSessionRenewal {
  readonly revision: number;
  readonly idempotencyKey: string;
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

// A wire Problem must remain distinguishable from any Error supplied by a
// caller as AbortSignal.reason. This wrapper never crosses the public client
// boundary, so even a previously observed ControlProblem cannot forge it.
class ControlWireProblem extends Error {
  readonly problem: ControlProblem;

  constructor(problem: ControlProblem) {
    super(problem.message);
    this.name = "ControlWireProblem";
    this.problem = problem;
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

type MutationAuthority =
  | {
      readonly kind: "success";
      readonly payload: unknown;
    }
  | {
      readonly kind: "problem";
      readonly status: number;
      readonly reasonCode: string;
      readonly messageKey: string;
    };

interface PendingMutationCommand {
  readonly idempotencyKey: string;
  activeCallers: number;
  activeAttempts: number;
  hasAmbiguousOutcome: boolean;
  requiresExplicitSettlement: boolean;
  authority?: MutationAuthority;
  readonly progressWaiters: Set<() => void>;
}

export interface ControlClient {
  close(): void;
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
  activities(cursor?: string, signal?: AbortSignal): Promise<ActivityPage>;
  exchange(exchangeId: string, signal?: AbortSignal): Promise<ExchangeDetail>;
  approvals(signal?: AbortSignal): Promise<ApprovalPage>;
  captureRuns(signal?: AbortSignal): Promise<CaptureRunPage>;
  manualCaptureContext(signal?: AbortSignal): Promise<ManualCaptureContext>;
  manualCaptures(signal?: AbortSignal): Promise<ManualCapturePage>;
  manualCapture(
    manualCaptureId: string,
    signal?: AbortSignal,
  ): Promise<ManualCaptureStateTag>;
  createManualCapture(
    input: ManualCaptureCreateInput,
    signal?: AbortSignal,
  ): Promise<ManualCaptureGrantStateTag>;
  rotateManualCapture(
    manualCaptureId: string,
    stateTag: string,
    signal?: AbortSignal,
  ): Promise<ManualCaptureGrantStateTag>;
  revokeManualCapture(
    manualCaptureId: string,
    stateTag: string,
    signal?: AbortSignal,
  ): Promise<void>;
  workspaceRouteBindings?(
    signal?: AbortSignal,
  ): Promise<WorkspaceRouteBindingPage>;
  updateWorkspaceRouteBinding?(
    bindingId: string,
    expectedRevision: number,
    profileId: string,
    signal?: AbortSignal,
  ): Promise<WorkspaceRouteBinding>;
  connections(signal?: AbortSignal): Promise<ConnectionPage>;
  egressAttempts(signal?: AbortSignal): Promise<EgressAttemptPage>;
  decideApproval(
    approval: ApprovalView,
    choice: ApprovalChoice,
    signal?: AbortSignal,
  ): Promise<ApprovalView>;
  accesses(signal?: AbortSignal): Promise<AccessDirectoryPage>;
  access(accessId: string, signal?: AbortSignal): Promise<AccessDetail>;
  addAccessCandidate(
    accessId: string,
    expectedRevision: number,
    input: AccessAddCandidateInput,
    signal?: AbortSignal,
  ): Promise<AccessAddCandidateResponse>;
  applyAccess(
    accessId: string,
    input: AccessApplyInput,
    signal?: AbortSignal,
  ): Promise<AccessApplyResponse>;
  updateAccessStatus(
    accessId: string,
    expectedRevision: number,
    status: Extract<AccessStatus, "enabled" | "disabled">,
    signal?: AbortSignal,
  ): Promise<AccessApplyResponse>;
  previewAccessDeletion(
    accessId: string,
    expectedRevision: number,
    signal?: AbortSignal,
  ): Promise<AccessDeletionPreview>;
  deleteAccess(
    accessId: string,
    expectedRevision: number,
    impactToken: string,
    retireWorkspaceBindings: boolean,
    signal?: AbortSignal,
  ): Promise<AccessDeletionResponse>;
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
  selectAccessCandidate(
    accessId: string,
    profileId: string,
    expectedRevision: number,
    signal?: AbortSignal,
  ): Promise<AccessApplyResponse>;
}

type Fetch = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

export async function createControlClient(
  session: DesktopSession,
  fetchImplementation: Fetch = globalThis.fetch.bind(globalThis),
  now: () => number = Date.now,
): Promise<ControlClient> {
  const owner = new AbortController();
  const validatedBootstrap = validateSession(session, now);
  const unresolvedMutations = new Map<string, PendingMutationCommand>();
  let activeMutationCalls = 0;
  let mutationRegistrationsInFlight = 0;

  const cleanupSettledMutationCommands = (): void => {
    // Identity calculation is asynchronous. A settled entry remains available
    // until every caller that started registration before settlement has had a
    // chance to join it. The counter retains no command or credential material.
    if (mutationRegistrationsInFlight !== 0) {
      return;
    }
    for (const [identity, command] of unresolvedMutations) {
      if (
        command.activeCallers === 0 &&
        command.activeAttempts === 0 &&
        command.authority !== undefined &&
        !command.requiresExplicitSettlement
      ) {
        unresolvedMutations.delete(identity);
      }
    }
  };
  const throwIfMutationAborted = (
    signal: AbortSignal | undefined,
  ): void => {
    if (owner.signal.aborted) {
      throw abortReason(owner.signal);
    }
    if (signal?.aborted === true) {
      throw abortReason(signal);
    }
  };

  async function requestWithCapability<T>(
    method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
    path: string,
    encodedBody: string | undefined,
    expectedRevision: number | undefined,
    idempotencyKey: string | undefined,
    token: string,
    signal: AbortSignal | undefined,
    expectedSuccessStatus: number,
    opaqueStateTag?: string,
    observeResponse?: (response: Response) => void,
    readRevision?: number,
  ): Promise<T> {
    const destination = new URL(path, `${validatedBootstrap.baseUrl}/`);
    if (
      destination.origin !== validatedBootstrap.baseUrl ||
      !destination.pathname.startsWith("/api/v1/")
    ) {
      throw new Error("Desktop control request escaped the bootstrap origin");
    }
    if (
      (expectedRevision === undefined) !== (idempotencyKey === undefined) ||
      (opaqueStateTag !== undefined &&
        (expectedRevision !== undefined ||
          idempotencyKey !== undefined ||
          readRevision !== undefined ||
          !validManualCaptureStateTag(opaqueStateTag))) ||
      (readRevision !== undefined &&
        (method !== "GET" ||
          expectedRevision !== undefined ||
          idempotencyKey !== undefined ||
          opaqueStateTag !== undefined ||
          !positiveInteger(readRevision)))
    ) {
      throw new Error("Desktop control mutation headers are incomplete");
    }
    const headers = new Headers({
      Accept: "application/json, application/problem+json",
      Authorization: `Bearer ${token}`,
    });
    if (encodedBody !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    if (expectedRevision !== undefined && idempotencyKey !== undefined) {
      headers.set("If-Match", String(expectedRevision));
      headers.set("Idempotency-Key", idempotencyKey);
    }
    if (readRevision !== undefined) {
      headers.set("If-Match", String(readRevision));
    }
    if (opaqueStateTag !== undefined) {
      headers.set("If-Match", opaqueStateTag);
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
    const requestAbort = createRequestAbort(signal, owner.signal);
    requestOptions.signal = requestAbort.signal;
    try {
      if (requestAbort.signal.aborted) {
        throw abortReason(requestAbort.signal);
      }
      const response = await waitForAbort(
        fetchImplementation(destination, requestOptions),
        requestAbort.signal,
      );
      const payload =
        response.status === 204 && response.body === null
          ? ""
          : await readBoundedResponse(response, requestAbort.signal);
      if (!response.ok) {
        if (!exactControlContentType(response, "application/problem+json")) {
          throw new ControlContractError();
        }
        throw new ControlWireProblem(decodeProblem(response.status, payload));
      }
      if (response.status !== expectedSuccessStatus) {
        throw new ControlContractError();
      }
      if (expectedSuccessStatus === 204) {
        if (payload !== "" || response.headers.get("Content-Type") !== null) {
          throw new ControlContractError();
        }
        observeResponse?.(response);
        return undefined as T;
      }
      if (!exactControlContentType(response, "application/json")) {
        throw new ControlContractError();
      }
      try {
        const result = JSON.parse(payload) as T;
        observeResponse?.(response);
        return result;
      } catch {
        throw new ControlContractError();
      }
    } finally {
      requestAbort.dispose();
    }
  }

  let statePayload: unknown;
  try {
    statePayload = await requestWithCapability<unknown>(
      "GET",
      sessionStatePath,
      undefined,
      undefined,
      undefined,
      session.readToken,
      undefined,
      200,
    );
  } catch (error) {
    if (error instanceof ControlWireProblem) {
      throw exposeControlWireProblem(error);
    }
    throw error;
  }
  const state = requireSessionState(statePayload, now);
  let activeSession = activateBootstrapSession(
    session,
    validatedBootstrap.baseUrl,
    state,
    now,
  );
  let renewalInFlight: Promise<void> | undefined;
  let pendingSessionRenewal: PendingSessionRenewal | undefined;

  async function renewSession(
    current: ActiveDesktopSession,
  ): Promise<ActiveDesktopSession> {
    let command = pendingSessionRenewal;
    if (command !== undefined && command.revision !== current.revision) {
      throw new Error("Desktop control renewal revision is unresolved");
    }
    if (command === undefined) {
      // Once the old capability has expired, only an already-issued command is
      // recoverable. Never create a fresh command with an expired capability.
      if (now() >= current.expiresAtMilliseconds) {
        throw new Error("Desktop control session is expired");
      }
      command = {
        revision: current.revision,
        idempotencyKey: createIdempotencyKey(),
      };
      pendingSessionRenewal = command;
    }
    const settle = (rotated: ActiveDesktopSession): ActiveDesktopSession => {
      if (pendingSessionRenewal === command) {
        pendingSessionRenewal = undefined;
      }
      return rotated;
    };
    const settleProblem = (): void => {
      if (pendingSessionRenewal === command) {
        pendingSessionRenewal = undefined;
      }
    };
    const attempt = async (): Promise<ActiveDesktopSession> => {
      const payload = await requestWithCapability<unknown>(
        "POST",
        sessionRenewalPath,
        undefined,
        current.revision,
        command.idempotencyKey,
        current.writeToken,
        undefined,
        200,
      );
      return requireSessionRotation(payload, current, now);
    };
    try {
      return settle(await attempt());
    } catch (error) {
      // A transport failure or an invalid success response is ambiguous: the
      // server may already have committed. Its bounded replay contract exists
      // specifically so the identical command can recover that response.
      if (error instanceof ControlWireProblem) {
        settleProblem();
        throw exposeControlWireProblem(error);
      }
      if (owner.signal.aborted) {
        throw error;
      }
      try {
        return settle(await attempt());
      } catch (replayError) {
        if (replayError instanceof ControlWireProblem) {
          settleProblem();
          throw exposeControlWireProblem(replayError);
        }
        throw replayError;
      }
    }
  }

  async function currentSession(
    signal: AbortSignal | undefined,
  ): Promise<ActiveDesktopSession> {
    if (owner.signal.aborted) {
      throw abortReason(owner.signal);
    }
    if (signal?.aborted === true) {
      throw abortReason(signal);
    }
    const currentTime = now();
    let pending = renewalInFlight;
    if (pending === undefined) {
      const canReplayExpiredRenewal =
        pendingSessionRenewal?.revision === activeSession.revision;
      if (
        currentTime >= activeSession.expiresAtMilliseconds &&
        !canReplayExpiredRenewal
      ) {
        throw new Error("Desktop control session is expired");
      }
      if (currentTime < activeSession.renewAfterMilliseconds) {
        return activeSession;
      }
      pending = renewSession(activeSession).then((rotated) => {
        // This is the only assignment of capability-bearing state. Validation
        // completes before either new token becomes observable to a request.
        activeSession = rotated;
      });
      renewalInFlight = pending;
      const clearRenewal = () => {
        if (renewalInFlight === pending) {
          renewalInFlight = undefined;
        }
      };
      void pending.then(clearRenewal, clearRenewal);
    }
    // A caller may stop waiting, but it must not cancel or unpublish the shared
    // rotation needed by other requests in this authenticated session.
    await (signal === undefined ? pending : waitForAbort(pending, signal));
    if (now() >= activeSession.expiresAtMilliseconds) {
      throw new Error("Desktop control session is expired");
    }
    return activeSession;
  }

  async function requestRead<T>(
    path: string,
    signal: AbortSignal | undefined,
  ): Promise<T> {
    const current = await currentSession(signal);
    try {
      return await requestWithCapability<T>(
        "GET",
        path,
        undefined,
        undefined,
        undefined,
        current.readToken,
        signal,
        200,
      );
    } catch (error) {
      if (error instanceof ControlWireProblem) {
        throw exposeControlWireProblem(error);
      }
      throw error;
    }
  }

  async function requestRevisionedRead<T>(
    path: string,
    expectedRevision: number,
    signal: AbortSignal | undefined,
  ): Promise<T> {
    if (!positiveInteger(expectedRevision)) {
      throw new ControlContractError();
    }
    const current = await currentSession(signal);
    try {
      return await requestWithCapability<T>(
        "GET",
        path,
        undefined,
        undefined,
        undefined,
        current.readToken,
        signal,
        200,
        undefined,
        undefined,
        expectedRevision,
      );
    } catch (error) {
      if (error instanceof ControlWireProblem) {
        throw exposeControlWireProblem(error);
      }
      throw error;
    }
  }

  async function requestManualRead<T>(
    path: string,
    signal: AbortSignal | undefined,
    decode: (value: unknown) => T,
    requireStateTag: boolean,
  ): Promise<{ readonly value: T; readonly stateTag?: string }> {
    const current = await currentSession(signal);
    let stateTag: string | undefined;
    try {
      const payload = await requestWithCapability<unknown>(
        "GET",
        path,
        undefined,
        undefined,
        undefined,
        current.readToken,
        signal,
        200,
        undefined,
        (response) => {
          stateTag = requireManualCaptureHeaders(response, requireStateTag);
        },
      );
      return {
        value: decode(payload),
        ...(stateTag === undefined ? {} : { stateTag }),
      };
    } catch (error) {
      if (error instanceof ControlWireProblem) {
        throw exposeControlWireProblem(error);
      }
      throw error;
    }
  }

  // ManualCapture create and credential rotation return a one-time secret.
  // They intentionally bypass the generic idempotent mutation replay plane:
  // a lost response is ambiguous and must never trigger an automatic retry.
  async function requestManualMutation<T>(
    path: string,
    body: unknown,
    stateTag: string | undefined,
    signal: AbortSignal | undefined,
    expectedSuccessStatus: 200 | 201 | 204,
    decode: (value: unknown) => T,
    requireResponseStateTag: boolean,
  ): Promise<{ readonly value: T; readonly stateTag?: string }> {
    throwIfMutationAborted(signal);
    if (stateTag !== undefined && !validManualCaptureStateTag(stateTag)) {
      throw new ControlContractError();
    }
    const encodedBody = body === undefined ? undefined : encodeControlBody(body);
    throwIfMutationAborted(signal);
    if (activeMutationCalls >= maximumActiveMutationCalls) {
      throw new Error("Desktop control has too many active mutation calls");
    }
    activeMutationCalls += 1;
    try {
      const current = await currentSession(signal);
      let responseStateTag: string | undefined;
      try {
        const payload = await requestWithCapability<unknown>(
          "POST",
          path,
          encodedBody,
          undefined,
          undefined,
          current.writeToken,
          signal,
          expectedSuccessStatus,
          stateTag,
          (response) => {
            responseStateTag = requireManualCaptureHeaders(
              response,
              requireResponseStateTag,
            );
          },
        );
        return {
          value: decode(payload),
          ...(responseStateTag === undefined
            ? {}
            : { stateTag: responseStateTag }),
        };
      } catch (error) {
        if (error instanceof ControlWireProblem) {
          throw exposeControlWireProblem(error);
        }
        throw error;
      }
    } finally {
      activeMutationCalls -= 1;
    }
  }

  async function mutationAttempt(
    method: "POST" | "PUT" | "PATCH" | "DELETE",
    path: string,
    encodedBody: string | undefined,
    expectedRevision: number,
    idempotencyKey: string,
    writeToken: string,
    signal: AbortSignal | undefined,
    expectedSuccessStatus: number,
  ): Promise<unknown> {
    return requestWithCapability<unknown>(
      method,
      path,
      encodedBody,
      expectedRevision,
      idempotencyKey,
      writeToken,
      signal,
      expectedSuccessStatus,
    );
  }

  async function requestMutation<T>(
    method: "POST" | "PUT" | "PATCH" | "DELETE",
    path: string,
    body: unknown,
    expectedRevision: number,
    signal: AbortSignal | undefined,
    decode: (value: unknown) => T,
    expectedSuccessStatus = 200,
  ): Promise<T> {
    throwIfMutationAborted(signal);
    if (
      !nonNegativeInteger(expectedRevision) ||
      expectedRevision >= Number.MAX_SAFE_INTEGER
    ) {
      throw new ControlContractError();
    }
    const encodedBody = encodeControlBody(body);
    // Body encoding may invoke a caller-owned toJSON implementation. Recheck
    // closed state before capacity so close remains authoritative even
    // when it happens reentrantly during that synchronous boundary.
    throwIfMutationAborted(signal);
    cleanupSettledMutationCommands();
    if (activeMutationCalls >= maximumActiveMutationCalls) {
      throw new Error("Desktop control has too many active mutation calls");
    }
    activeMutationCalls += 1;
    try {
      return await requestMutationWithinLimit(
        method,
        path,
        encodedBody,
        expectedRevision,
        signal,
        decode,
        expectedSuccessStatus,
      );
    } finally {
      activeMutationCalls -= 1;
      cleanupSettledMutationCommands();
    }
  }

  async function requestMutationWithinLimit<T>(
    method: "POST" | "PUT" | "PATCH" | "DELETE",
    path: string,
    encodedBody: string | undefined,
    expectedRevision: number,
    signal: AbortSignal | undefined,
    decode: (value: unknown) => T,
    expectedSuccessStatus: number,
  ): Promise<T> {
    mutationRegistrationsInFlight += 1;
    let registration:
      | {
          readonly current: ActiveDesktopSession;
          readonly command: PendingMutationCommand;
          readonly explicitlySettling: boolean;
        }
      | undefined;
    try {
      const current = await currentSession(signal);
      const commandIdentity = await mutationCommandIdentity(
        method,
        path,
        expectedRevision,
        encodedBody,
        signal,
        owner.signal,
      );
      if (owner.signal.aborted) {
        throw abortReason(owner.signal);
      }
      if (signal?.aborted === true) {
        throw abortReason(signal);
      }
      let command = unresolvedMutations.get(commandIdentity);
      if (command === undefined) {
        if (unresolvedMutations.size >= maximumUnresolvedMutations) {
          throw new Error(
            "Desktop control has too many unresolved mutation commands",
          );
        }
        command = {
          idempotencyKey: createIdempotencyKey(),
          activeCallers: 0,
          activeAttempts: 0,
          hasAmbiguousOutcome: false,
          requiresExplicitSettlement: false,
          progressWaiters: new Set(),
        };
        unresolvedMutations.set(commandIdentity, command);
      }
      const explicitlySettling = command.requiresExplicitSettlement;
      if (explicitlySettling) {
        // This invocation explicitly observes or retries the command after an
        // earlier caller could not determine its outcome.
        command.requiresExplicitSettlement = false;
      }
      // Join the command before publishing registration completion. A settled
      // command cannot be swept between identity resolution and this increment.
      command.activeCallers += 1;
      registration = {
        current,
        command,
        explicitlySettling,
      };
    } finally {
      mutationRegistrationsInFlight -= 1;
      cleanupSettledMutationCommands();
    }
    if (registration === undefined) {
      throw new Error("Desktop control mutation registration is unavailable");
    }
    const { current, command, explicitlySettling } = registration;

    const authoritativeResult = (): T => {
      const authority = command.authority;
      if (authority === undefined) {
        throw new Error("Desktop control mutation authority is unavailable");
      }
      if (authority.kind === "problem") {
        throw new ControlProblem(
          authority.status,
          authority.reasonCode,
          authority.messageKey,
        );
      }
      return decode(cloneMutationAuthorityPayload(authority.payload));
    };
    const guardedAuthoritativeResult = (): T => {
      try {
        return authoritativeResult();
      } catch (error) {
        if (!(error instanceof ControlProblem)) {
          command.requiresExplicitSettlement = true;
        }
        throw error;
      }
    };
    const hasSuccessAuthority = (): boolean =>
      command.authority?.kind === "success";
    const notifyProgress = (): void => {
      const waiters = Array.from(command.progressWaiters);
      command.progressWaiters.clear();
      for (const resolve of waiters) {
        resolve();
      }
    };
    let attemptActive = false;
    const beginAttempt = (): void => {
      command.activeAttempts += 1;
      attemptActive = true;
    };
    const finishAttempt = (): void => {
      if (!attemptActive) {
        return;
      }
      attemptActive = false;
      command.activeAttempts -= 1;
      notifyProgress();
    };
    const awaitAuthoritativeResult = async (): Promise<T> => {
      while (
        command.authority?.kind !== "success" &&
        command.activeAttempts > 0
      ) {
        try {
          await waitForMutationProgress(command, signal, owner.signal);
        } catch (error) {
          command.requiresExplicitSettlement = true;
          throw error;
        }
      }
      return guardedAuthoritativeResult();
    };
    const settleSuccess = (payload: unknown, result: T): T => {
      if (command.authority?.kind === "success") {
        finishAttempt();
        return guardedAuthoritativeResult();
      }
      // A validated success proves that the command committed. It outranks a
      // conflicting Problem from another same-key request, because issuing a
      // new key after observed commit could duplicate the mutation.
      command.authority = {
        kind: "success",
        payload: cloneMutationAuthorityPayload(payload),
      };
      finishAttempt();
      return result;
    };
    const settleProblem = async (error: ControlProblem): Promise<T> => {
      // A Problem is only a candidate while another same-key attempt can still
      // prove commit. Among Problems, retain the first receipt deterministically.
      command.authority ??= {
        kind: "problem",
        status: error.status,
        reasonCode: error.reasonCode,
        messageKey: error.messageKey,
      };
      finishAttempt();
      return awaitAuthoritativeResult();
    };
    const observePreIdempotencyProblem = async (
      error: ControlWireProblem,
    ): Promise<T> => {
      // Authentication is enforced before the server's idempotency cache. A
      // retired capability therefore says nothing about a command whose prior
      // response was ambiguous. Keep its key recoverable; a concurrent success
      // may still provide the authority for every same-key caller.
      finishAttempt();
      while (
        command.authority?.kind !== "success" &&
        command.activeAttempts > 0
      ) {
        try {
          await waitForMutationProgress(command, signal, owner.signal);
        } catch (waitError) {
          markAmbiguous();
          throw waitError;
        }
      }
      if (hasSuccessAuthority()) {
        return guardedAuthoritativeResult();
      }
      markAmbiguous();
      throw exposeControlWireProblem(error);
    };
    const shouldPreserveAmbiguousProblem = (
      error: ControlWireProblem,
    ): boolean =>
      command.hasAmbiguousOutcome &&
      error.problem.status === 401 &&
      error.problem.reasonCode === "control_unauthorized";
    const recordAmbiguousOutcome = (): void => {
      command.hasAmbiguousOutcome = true;
    };
    const markAmbiguous = (): void => {
      recordAmbiguousOutcome();
      command.requiresExplicitSettlement = true;
    };
    const releaseCaller = (): void => {
      command.activeCallers -= 1;
      cleanupSettledMutationCommands();
    };
    try {
      if (hasSuccessAuthority()) {
        return guardedAuthoritativeResult();
      }
      if (command.authority?.kind === "problem" && !explicitlySettling) {
        return awaitAuthoritativeResult();
      }
      beginAttempt();
      try {
        const payload = await mutationAttempt(
          method,
          path,
          encodedBody,
          expectedRevision,
          command.idempotencyKey,
          current.writeToken,
          signal,
          expectedSuccessStatus,
        );
        const result = decode(payload);
        return settleSuccess(payload, result);
      } catch (error) {
        if (error instanceof ControlWireProblem) {
          if (shouldPreserveAmbiguousProblem(error)) {
            return observePreIdempotencyProblem(error);
          }
          return settleProblem(error.problem);
        }
        recordAmbiguousOutcome();
        if (!immediateMutationReplayAllowed(error, signal, owner.signal)) {
          markAmbiguous();
          finishAttempt();
          throw error;
        }
        if (hasSuccessAuthority()) {
          finishAttempt();
          return guardedAuthoritativeResult();
        }
        try {
          const payload = await mutationAttempt(
            method,
            path,
            encodedBody,
            expectedRevision,
            command.idempotencyKey,
            current.writeToken,
            signal,
            expectedSuccessStatus,
          );
          const result = decode(payload);
          return settleSuccess(payload, result);
        } catch (replayError) {
          if (replayError instanceof ControlWireProblem) {
            if (hasSuccessAuthority()) {
              finishAttempt();
              return guardedAuthoritativeResult();
            }
            if (shouldPreserveAmbiguousProblem(replayError)) {
              return observePreIdempotencyProblem(replayError);
            }
            return settleProblem(replayError.problem);
          }
          recordAmbiguousOutcome();
          if (
            !immediateMutationReplayAllowed(
              replayError,
              signal,
              owner.signal,
            )
          ) {
            markAmbiguous();
            finishAttempt();
            throw replayError;
          }
          if (hasSuccessAuthority()) {
            finishAttempt();
            return guardedAuthoritativeResult();
          }
          markAmbiguous();
          finishAttempt();
          throw replayError;
        }
      }
    } finally {
      if (attemptActive) {
        markAmbiguous();
        finishAttempt();
      }
      releaseCaller();
    }
  }

  async function requestAccessApply(
    accessId: string,
    input: AccessApplyInput,
    signal: AbortSignal | undefined,
  ): Promise<AccessApplyResponse> {
    const path = `/api/v1/accesses/${encodeURIComponent(accessId)}/actions/apply`;
    const expectedRevision = input.expectedRevision;
    return requestMutation<AccessApplyResponse>(
      "PUT",
      path,
      input,
      expectedRevision,
      signal,
      (value) => requireAccessApplyResponse(value, expectedRevision),
    );
  }

  return {
    close: () => {
      if (owner.signal.aborted) {
        return;
      }
      activeSession = {
        ...activeSession,
        expiresAtMilliseconds: 0,
        readToken: "",
        renewAfterMilliseconds: 0,
        writeToken: "",
      };
      pendingSessionRenewal = undefined;
      unresolvedMutations.clear();
      owner.abort(
        new DOMException("Desktop control session was closed", "AbortError"),
      );
    },
    status: async (signal) =>
      requireStatusResponse(
        await requestRead<unknown>("/api/v1/status", signal),
        session.instanceId,
      ),
    offlineHold: async (signal) =>
      requireOfflineHoldSnapshot(
        await requestRead<unknown>("/api/v1/offline-hold", signal),
      ),
    enterOfflineHold: async (revision, signal) =>
      requestMutation<OfflineHoldSnapshot>(
        "POST",
        "/api/v1/offline-hold/actions/enter",
        undefined,
        revision,
        signal,
        (value) => requireOfflineHoldSnapshot(value, revision),
      ),
    resumeOfflineHold: async (revision, signal) =>
      requestMutation<OfflineHoldSnapshot>(
        "POST",
        "/api/v1/offline-hold/actions/resume",
        undefined,
        revision,
        signal,
        (value) => requireOfflineHoldSnapshot(value, revision),
      ),
    activities: async (cursor, signal) => {
      if (cursor !== undefined && !validOpaqueCursor(cursor)) {
        throw new ControlContractError();
      }
      const query = new URLSearchParams({ limit: "50" });
      if (cursor !== undefined) {
        query.set("cursor", cursor);
      }
      return requireActivityPage(
        await requestRead<unknown>(
          `/api/v1/activities?${query.toString()}`,
          signal,
        ),
      );
    },
    exchange: async (exchangeId, signal) => {
      if (!validRouteIdentity(exchangeId)) {
        throw new ControlContractError();
      }
      return requireExchangeDetail(
        await requestRead<unknown>(
          `/api/v1/exchanges/${encodeURIComponent(exchangeId)}`,
          signal,
        ),
        exchangeId,
      );
    },
    approvals: async (signal) =>
      requireApprovalPage(
        await requestRead<unknown>(
          "/api/v1/approvals?state=pending&limit=50",
          signal,
        ),
      ),
    captureRuns: async (signal) =>
      requireCaptureRunPage(
        await requestRead<unknown>("/api/v1/capture-runs?limit=50", signal),
      ),
    manualCaptureContext: async (signal) =>
      (
        await requestManualRead(
          "/api/v1/manual-captures/context",
          signal,
          requireManualCaptureContext,
          false,
        )
      ).value,
    manualCaptures: async (signal) =>
      (
        await requestManualRead(
          "/api/v1/manual-captures",
          signal,
          requireManualCapturePage,
          false,
        )
      ).value,
    manualCapture: async (manualCaptureId, signal) => {
      if (!validResourceId(manualCaptureId)) {
        throw new ControlContractError();
      }
      const result = await requestManualRead(
        `/api/v1/manual-captures/${encodeURIComponent(manualCaptureId)}`,
        signal,
        (value) => requireManualCaptureRecord(value, manualCaptureId),
        true,
      );
      if (result.stateTag === undefined) {
        throw new ControlContractError();
      }
      return { capture: result.value, stateTag: result.stateTag };
    },
    createManualCapture: async (input, signal) => {
      if (!validManualCaptureCreateInput(input)) {
        throw new ControlContractError();
      }
      const result = await requestManualMutation(
        "/api/v1/manual-captures",
        input,
        undefined,
        signal,
        201,
        requireManualCaptureGrant,
        true,
      );
      if (result.stateTag === undefined) {
        throw new ControlContractError();
      }
      return { grant: result.value, stateTag: result.stateTag };
    },
    rotateManualCapture: async (manualCaptureId, stateTag, signal) => {
      if (!validResourceId(manualCaptureId)) {
        throw new ControlContractError();
      }
      const result = await requestManualMutation(
        `/api/v1/manual-captures/${encodeURIComponent(manualCaptureId)}/actions/rotate-credential`,
        undefined,
        stateTag,
        signal,
        200,
        (value) => requireManualCaptureGrant(value, manualCaptureId),
        true,
      );
      if (result.stateTag === undefined) {
        throw new ControlContractError();
      }
      return { grant: result.value, stateTag: result.stateTag };
    },
    revokeManualCapture: async (manualCaptureId, stateTag, signal) => {
      if (!validResourceId(manualCaptureId)) {
        throw new ControlContractError();
      }
      await requestManualMutation(
        `/api/v1/manual-captures/${encodeURIComponent(manualCaptureId)}/actions/revoke`,
        undefined,
        stateTag,
        signal,
        204,
        (value) => {
          if (value !== undefined) {
            throw new ControlContractError();
          }
        },
        false,
      );
    },
    workspaceRouteBindings: async (signal) =>
      requireWorkspaceRouteBindingPage(
        await requestRead<unknown>(
          "/api/v1/workspace-route-bindings?limit=50",
          signal,
        ),
      ),
    updateWorkspaceRouteBinding: async (
      bindingId,
      expectedRevision,
      profileId,
      signal,
    ) => {
      if (
        !validRouteIdentity(bindingId) ||
        !positiveInteger(expectedRevision) ||
        !validResourceId(profileId)
      ) {
        throw new ControlContractError();
      }
      return requestMutation<WorkspaceRouteBinding>(
        "PATCH",
        `/api/v1/workspace-route-bindings/${encodeURIComponent(bindingId)}`,
        { profileId },
        expectedRevision,
        signal,
        (value) =>
          requireWorkspaceRouteBinding(
            value,
            bindingId,
            expectedRevision + 1,
            profileId,
          ),
      );
    },
    connections: async (signal) =>
      requireConnectionPage(
        await requestRead<unknown>("/api/v1/connections?limit=50", signal),
      ),
    egressAttempts: async (signal) =>
      requireEgressAttemptPage(
        await requestRead<unknown>("/api/v1/egress-attempts?limit=50", signal),
      ),
    decideApproval: async (approval, choice, signal) =>
      requestMutation<ApprovalView>(
        "POST",
        `/api/v1/approvals/${encodeURIComponent(approval.id)}/actions/decide`,
        {
          decision: choice.decision,
          scope: choice.scope,
          ...(choice.decision === "deny" ? { reasonCode: "user_denied" } : {}),
        },
        approval.revision,
        signal,
        (value) => requireApprovalDecisionResponse(value, approval, choice),
      ),
    accesses: async (signal) =>
      requireAccessDirectoryPage(
        await requestRead<unknown>("/api/v1/accesses", signal),
      ),
    access: async (accessId, signal) => {
      if (!validResourceId(accessId)) {
        throw new ControlContractError();
      }
      return requireAccessDetail(
        await requestRead<unknown>(
          `/api/v1/accesses/${encodeURIComponent(accessId)}`,
          signal,
        ),
        accessId,
      );
    },
    addAccessCandidate: async (
      accessId,
      expectedRevision,
      input,
      signal,
    ) => {
      if (!validResourceId(accessId) || !nonNegativeInteger(expectedRevision)) {
        throw new ControlContractError();
      }
      return requestMutation<AccessAddCandidateResponse>(
        "POST",
        `/api/v1/accesses/${encodeURIComponent(accessId)}/actions/add-candidate`,
        input,
        expectedRevision,
        signal,
        (value) =>
          requireAccessAddCandidateResponse(value, expectedRevision),
        201,
      );
    },
    applyAccess: requestAccessApply,
    updateAccessStatus: async (
      accessId,
      expectedRevision,
      status,
      signal,
    ) => {
      if (
        !validResourceId(accessId) ||
        !positiveInteger(expectedRevision) ||
        (status !== "enabled" && status !== "disabled")
      ) {
        throw new ControlContractError();
      }
      return requestMutation<AccessApplyResponse>(
        "PATCH",
        `/api/v1/accesses/${encodeURIComponent(accessId)}`,
        { status },
        expectedRevision,
        signal,
        (value) => requireAccessApplyResponse(value, expectedRevision),
      );
    },
    previewAccessDeletion: async (accessId, expectedRevision, signal) => {
      if (!validResourceId(accessId) || !positiveInteger(expectedRevision)) {
        throw new ControlContractError();
      }
      return requireAccessDeletionPreview(
        await requestRevisionedRead<unknown>(
          `/api/v1/accesses/${encodeURIComponent(accessId)}/deletion-preview`,
          expectedRevision,
          signal,
        ),
        accessId,
        expectedRevision,
      );
    },
    deleteAccess: async (
      accessId,
      expectedRevision,
      impactToken,
      retireWorkspaceBindings,
      signal,
    ) => {
      if (
        !validResourceId(accessId) ||
        !positiveInteger(expectedRevision) ||
        !validAccessDeletionImpactToken(impactToken) ||
        typeof retireWorkspaceBindings !== "boolean"
      ) {
        throw new ControlContractError();
      }
      return requestMutation<AccessDeletionResponse>(
        "DELETE",
        `/api/v1/accesses/${encodeURIComponent(accessId)}`,
        { impactToken, retireWorkspaceBindings },
        expectedRevision,
        signal,
        (value) => requireAccessDeletionResponse(value, expectedRevision),
      );
    },
    accessPlan: async (accessId, signal) =>
      requireAccessPlanSummary(
        await requestRead<unknown>(
          `/api/v1/accesses/${encodeURIComponent(accessId)}/plan`,
          signal,
        ),
        accessId,
      ),
    credential: async (accessId, profileId, credentialId, signal) =>
      requireCredentialView(
        await requestRead<unknown>(
          credentialPath(accessId, profileId, credentialId),
          signal,
        ),
        profileId,
        credentialId,
        false,
      ),
    replaceCredentialSecret: async (
      accessId,
      profileId,
      credentialId,
      expectedRevision,
      secret,
      signal,
    ) =>
      requestMutation<CredentialView>(
        "POST",
        `${credentialPath(accessId, profileId, credentialId)}/actions/replace-secret`,
        { secret },
        expectedRevision,
        signal,
        (value) =>
          requireCredentialReplacementResponse(
            value,
            profileId,
            credentialId,
            expectedRevision,
          ),
      ),
    selectAccessCandidate: async (
      accessId,
      profileId,
      expectedRevision,
      signal,
    ) => {
      if (
        !validResourceId(accessId) ||
        !validResourceId(profileId) ||
        !nonNegativeInteger(expectedRevision)
      ) {
        throw new ControlContractError();
      }
      return requestMutation<AccessApplyResponse>(
        "POST",
        `/api/v1/accesses/${encodeURIComponent(accessId)}/profiles/${encodeURIComponent(profileId)}/actions/select-candidate`,
        undefined,
        expectedRevision,
        signal,
        (value) => requireAccessApplyResponse(value, expectedRevision),
      );
    },
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

interface ValidatedDesktopSession {
  readonly baseUrl: string;
  readonly expiresAtMilliseconds: number;
}

function validateSession(
  session: DesktopSession,
  now: () => number,
): ValidatedDesktopSession {
  if (
    session.schema !== "vibermate-app-session-v1" ||
    session.instanceId.length === 0 ||
    session.instanceId.trim() !== session.instanceId ||
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
  let parsed: URL;
  try {
    parsed = new URL(session.baseUrl);
  } catch {
    throw new Error("Desktop control base URL is not literal IPv4 loopback");
  }
  const port = Number(parsed.port);
  if (
    parsed.protocol !== "http:" ||
    parsed.hostname !== "127.0.0.1" ||
    !Number.isSafeInteger(port) ||
    port <= 0 ||
    port > 65_535 ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.pathname !== "/" ||
    parsed.search !== "" ||
    parsed.hash !== "" ||
    parsed.origin !== session.baseUrl
  ) {
    throw new Error("Desktop control base URL is not literal IPv4 loopback");
  }
  return {
    baseUrl: parsed.origin,
    expiresAtMilliseconds: expiresAt,
  };
}

function requireSessionState(
  value: unknown,
  now: () => number,
): SessionStateResponse {
  if (
    !hasExactFields(value, ["schema", "revision", "expiresAt"]) ||
    value.schema !== sessionStateSchema ||
    !validRevision(value.revision) ||
    typeof value.expiresAt !== "string" ||
    !futureTimestamp(value.expiresAt, now)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as SessionStateResponse;
}

function activateBootstrapSession(
  bootstrap: DesktopSession,
  baseUrl: string,
  state: SessionStateResponse,
  now: () => number,
): ActiveDesktopSession {
  const identified: DesktopSession = {
    schema: bootstrap.schema,
    baseUrl,
    readToken: bootstrap.readToken,
    writeToken: bootstrap.writeToken,
    instanceId: bootstrap.instanceId,
    expiresAt: state.expiresAt,
  };
  const validated = validateSession(identified, now);
  return {
    ...identified,
    revision: state.revision,
    expiresAtMilliseconds: validated.expiresAtMilliseconds,
    renewAfterMilliseconds: renewalTime(
      now(),
      validated.expiresAtMilliseconds,
    ),
  };
}

function requireSessionRotation(
  value: unknown,
  current: ActiveDesktopSession,
  now: () => number,
): ActiveDesktopSession {
  if (
    !hasExactFields(value, [
      "schema",
      "revision",
      "readToken",
      "writeToken",
      "expiresAt",
    ]) ||
    value.schema !== sessionRotationSchema ||
    value.revision !== current.revision + 1 ||
    !validRevision(value.revision) ||
    typeof value.readToken !== "string" ||
    typeof value.writeToken !== "string" ||
    typeof value.expiresAt !== "string" ||
    !futureTimestamp(value.expiresAt, now) ||
    !validCapability(value.readToken) ||
    !validCapability(value.writeToken) ||
    new Set([
      current.readToken,
      current.writeToken,
      value.readToken,
      value.writeToken,
    ]).size !== 4
  ) {
    throw new ControlContractError();
  }
  const identified: DesktopSession = {
    schema: current.schema,
    baseUrl: current.baseUrl,
    readToken: value.readToken,
    writeToken: value.writeToken,
    instanceId: current.instanceId,
    expiresAt: value.expiresAt,
  };
  const validated = validateSession(identified, now);
  if (validated.baseUrl !== current.baseUrl) {
    throw new ControlContractError();
  }
  return {
    ...identified,
    revision: value.revision,
    expiresAtMilliseconds: validated.expiresAtMilliseconds,
    renewAfterMilliseconds: renewalTime(
      now(),
      validated.expiresAtMilliseconds,
    ),
  };
}

function renewalTime(now: number, expiresAt: number): number {
  const lifetime = expiresAt - now;
  const lead = Math.min(
    maximumRenewalLeadMilliseconds,
    Math.max(minimumRenewalLeadMilliseconds, Math.floor(lifetime / 5)),
  );
  return Math.max(now, expiresAt - lead);
}

function validRevision(value: unknown): value is number {
  return (
    typeof value === "number" &&
    Number.isSafeInteger(value) &&
    value >= 1 &&
    value < Number.MAX_SAFE_INTEGER
  );
}

function futureTimestamp(value: string, now: () => number): boolean {
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/u.test(value) ||
    !validTimestamp(value)
  ) {
    return false;
  }
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) && parsed > now();
}

function hasExactFields(
  value: unknown,
  fields: readonly string[],
): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const keys = Object.keys(value);
  return (
    keys.length === fields.length &&
    fields.every((field) => Object.hasOwn(value, field))
  );
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

function encodeControlBody(body: unknown): string | undefined {
  if (body === undefined) {
    return undefined;
  }
  const encoded = JSON.stringify(body);
  if (
    encoded === undefined ||
    new TextEncoder().encode(encoded).byteLength > maximumControlRequestBytes
  ) {
    throw new ControlContractError();
  }
  return encoded;
}

async function mutationCommandIdentity(
  method: "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  expectedRevision: number,
  encodedBody: string | undefined,
  callerSignal: AbortSignal | undefined,
  ownerSignal: AbortSignal,
): Promise<string> {
  // The pending map must never retain a credential body. Hash the complete,
  // length-safe command identity and clear the local byte copy when the digest
  // settles or its bounded wait aborts; the encoded string stays call-local.
  const material = JSON.stringify([
    "vibermate:desktop-control-mutation:v1",
    method,
    path,
    expectedRevision,
    encodedBody ?? null,
  ]);
  const bytes = new TextEncoder().encode(material);
  const digestAbort = createRequestAbort(callerSignal, ownerSignal);
  try {
    if (digestAbort.signal.aborted) {
      throw abortReason(digestAbort.signal);
    }
    const digest = new Uint8Array(
      await waitForAbort(
        globalThis.crypto.subtle.digest("SHA-256", bytes),
        digestAbort.signal,
      ),
    );
    return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join(
      "",
    );
  } finally {
    digestAbort.dispose();
    bytes.fill(0);
  }
}

function exposeControlWireProblem(error: ControlWireProblem): ControlProblem {
  return new ControlProblem(
    error.problem.status,
    error.problem.reasonCode,
    error.problem.messageKey,
  );
}

function cloneMutationAuthorityPayload(payload: unknown): unknown {
  try {
    return globalThis.structuredClone(payload);
  } catch {
    // Control responses are parsed JSON and therefore cloneable. Treat any
    // violation as an ambiguous success receipt rather than retaining or
    // sharing an object whose ownership cannot be proven.
    throw new ControlContractError();
  }
}

function immediateMutationReplayAllowed(
  error: unknown,
  callerSignal: AbortSignal | undefined,
  ownerSignal: AbortSignal,
): boolean {
  if (ownerSignal.aborted || callerSignal?.aborted === true) {
    return false;
  }
  if (error === null || typeof error !== "object" || !("name" in error)) {
    return true;
  }
  return error.name !== "AbortError" && error.name !== "TimeoutError";
}

function createIdempotencyKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24));
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

interface RequestAbort {
  readonly signal: AbortSignal;
  dispose(): void;
}

function createRequestAbort(
  callerSignal: AbortSignal | undefined,
  ownerSignal: AbortSignal,
): RequestAbort {
  const controller = new AbortController();
  const forwardCallerAbort = () => {
    if (!controller.signal.aborted && callerSignal !== undefined) {
      controller.abort(abortReason(callerSignal));
    }
  };
  const forwardOwnerAbort = () => {
    if (!controller.signal.aborted) {
      controller.abort(abortReason(ownerSignal));
    }
  };
  if (callerSignal?.aborted === true) {
    forwardCallerAbort();
  } else {
    callerSignal?.addEventListener("abort", forwardCallerAbort, { once: true });
  }
  if (ownerSignal.aborted) {
    forwardOwnerAbort();
  } else {
    ownerSignal.addEventListener("abort", forwardOwnerAbort, { once: true });
  }
  const timeout = controller.signal.aborted
    ? undefined
    : globalThis.setTimeout(() => {
        controller.abort(
          new DOMException("Desktop control request timed out", "TimeoutError"),
        );
      }, requestTimeoutMilliseconds);
  return {
    dispose: () => {
      if (timeout !== undefined) {
        globalThis.clearTimeout(timeout);
      }
      callerSignal?.removeEventListener("abort", forwardCallerAbort);
      ownerSignal.removeEventListener("abort", forwardOwnerAbort);
    },
    signal: controller.signal,
  };
}

function waitForAbort<T>(operation: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) {
    return Promise.reject(abortReason(signal));
  }
  return new Promise<T>((resolve, reject) => {
    const cleanup = () => signal.removeEventListener("abort", onAbort);
    const onAbort = () => {
      cleanup();
      reject(abortReason(signal));
    };
    signal.addEventListener("abort", onAbort, { once: true });
    void operation.then(
      (value) => {
        cleanup();
        resolve(value);
      },
      (error: unknown) => {
        cleanup();
        reject(error);
      },
    );
  });
}

function waitForMutationProgress(
  command: PendingMutationCommand,
  callerSignal: AbortSignal | undefined,
  ownerSignal: AbortSignal,
): Promise<void> {
  if (ownerSignal.aborted) {
    return Promise.reject(abortReason(ownerSignal));
  }
  if (callerSignal?.aborted === true) {
    return Promise.reject(abortReason(callerSignal));
  }
  return new Promise<void>((resolve, reject) => {
    let settled = false;
    const dispose = () => {
      command.progressWaiters.delete(onProgress);
      callerSignal?.removeEventListener("abort", onCallerAbort);
      ownerSignal.removeEventListener("abort", onOwnerAbort);
    };
    const settle = (error?: unknown) => {
      if (settled) {
        return;
      }
      settled = true;
      dispose();
      if (error === undefined) {
        resolve();
      } else {
        reject(error);
      }
    };
    const onProgress = () => settle();
    const onCallerAbort = () => {
      if (callerSignal !== undefined) {
        settle(abortReason(callerSignal));
      }
    };
    const onOwnerAbort = () => settle(abortReason(ownerSignal));
    command.progressWaiters.add(onProgress);
    callerSignal?.addEventListener("abort", onCallerAbort, { once: true });
    ownerSignal.addEventListener("abort", onOwnerAbort, { once: true });
    if (ownerSignal.aborted) {
      onOwnerAbort();
    } else if (callerSignal?.aborted === true) {
      onCallerAbort();
    }
  });
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("Desktop control request aborted", "AbortError");
}

async function readBoundedResponse(
  response: Response,
  signal: AbortSignal,
): Promise<string> {
  const body = response.body;
  if (body === null) {
    throw new ControlContractError();
  }
  const declaredHeader = response.headers.get("Content-Length");
  if (declaredHeader !== null) {
    const declared = Number(declaredHeader);
    if (
      !/^\d+$/u.test(declaredHeader) ||
      !Number.isSafeInteger(declared) ||
      declared > maximumResponseBytes
    ) {
      void body.cancel().catch(() => undefined);
      throw new ControlContractError();
    }
  }

  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let payload = "";
  let received = 0;
  try {
    while (true) {
      const chunk = await waitForAbort(reader.read(), signal);
      if (chunk.done) {
        break;
      }
      if (chunk.value === undefined) {
        throw new ControlContractError();
      }
      received += chunk.value.byteLength;
      if (received > maximumResponseBytes) {
        void reader.cancel().catch(() => undefined);
        throw new ControlContractError();
      }
      payload += decoder.decode(chunk.value, { stream: true });
    }
    payload += decoder.decode();
    return payload;
  } catch (error) {
    if (signal.aborted) {
      void reader.cancel(abortReason(signal)).catch(() => undefined);
      throw abortReason(signal);
    }
    if (error instanceof ControlContractError) {
      throw error;
    }
    throw new ControlContractError();
  } finally {
    try {
      reader.releaseLock();
    } catch {
      // An aborted read may still be settling after cancellation. The response
      // and its reader are no longer retained by the request either way.
    }
  }
}

const maximumDashboardPageItems = 50;
const maximumActivityPageItems = 200;
const maximumIdentityBytes = 512;
const maximumHostBytes = 1024;
const maximumUint32 = 0xffff_ffff;
const maximumResourceIdBytes = 128;
const maximumEndpointProfiles = 64;
const maximumAccountBindings = 128;

const runtimeStates = new Set([
  "starting",
  "initialized",
  "degraded",
  "stopping",
  "stopped",
  "stop_failed",
]);
const storageStates = new Set(["healthy", "unavailable"]);
const projectionStates = new Set(["healthy", "unavailable"]);
const offlineHoldStates = new Set([
  "unbound",
  "online",
  "entering",
  "held",
  "probing",
  "releasing",
  "stopping",
]);
const offlineEgressKinds = new Set([
  "provider",
  "opaque",
  "auxiliary",
  "plugin",
  "update",
  "blind_tunnel",
]);
const offlineProbeReasons = new Set([
  "transport_unavailable",
  "tls_rejected",
  "canceled",
  "probe_failed",
]);
const credentialStates = new Set([
  "configured",
  "missing",
  "unavailable",
]);

function requireStatusResponse(
  value: unknown,
  expectedInstanceId: string,
): StatusResponse {
  if (
    !hasClosedFields(value, [
      "generation",
      "ready",
      "apiVersion",
      "statusKey",
      "runtime",
    ]) ||
    !validIdentity(value.generation) ||
    value.generation !== expectedInstanceId ||
    typeof value.ready !== "boolean" ||
    value.apiVersion !== "v1" ||
    !validRuntimeStatus(value.runtime) ||
    value.generation !== value.runtime.instanceId ||
    value.statusKey !== `runtime.state.${String(value.runtime.state)}` ||
    (value.ready && value.runtime.state !== "initialized")
  ) {
    throw new ControlContractError();
  }
  return value as unknown as StatusResponse;
}

function validRuntimeStatus(value: unknown): value is Record<string, unknown> {
  if (
    !hasClosedFields(
      value,
      [
        "state",
        "instanceId",
        "host",
        "schemaRevision",
        "storage",
        "accessProjection",
        "offlineHold",
        "startedAt",
      ],
      ["stoppedAt", "stopReasonCode"],
    ) ||
    !runtimeStates.has(String(value.state)) ||
    !validIdentity(value.instanceId) ||
    value.host !== "desktop" ||
    !nonNegativeInteger(value.schemaRevision) ||
    !storageStates.has(String(value.storage)) ||
    !validAccessProjection(value.accessProjection) ||
    !validOfflineHoldSnapshot(value.offlineHold) ||
    !validTimestamp(value.startedAt) ||
    (value.stoppedAt !== undefined && !validTimestamp(value.stoppedAt)) ||
    (value.stopReasonCode !== undefined && !validIdentity(value.stopReasonCode))
  ) {
    return false;
  }
  if (
    value.state === "initialized" &&
    (value.storage !== "healthy" || value.accessProjection.state !== "healthy")
  ) {
    return false;
  }
  if (
    value.state === "degraded" &&
    value.storage !== "unavailable" &&
    value.accessProjection.state !== "unavailable"
  ) {
    return false;
  }
  switch (value.state) {
    case "stopped":
      return (
        typeof value.stoppedAt === "string" &&
        Date.parse(value.stoppedAt) >= Date.parse(value.startedAt) &&
        value.stopReasonCode === undefined
      );
    case "stop_failed":
      return (
        value.stoppedAt === undefined &&
        value.stopReasonCode === "shutdown_failed"
      );
    default:
      return value.stoppedAt === undefined && value.stopReasonCode === undefined;
  }
}

function validAccessProjection(
  value: unknown,
): value is Record<string, unknown> {
  return (
    hasClosedFields(value, ["state", "unavailableAccessCount"]) &&
    projectionStates.has(String(value.state)) &&
    nonNegativeInteger(value.unavailableAccessCount) &&
    (value.state === "healthy"
      ? value.unavailableAccessCount === 0
      : value.unavailableAccessCount > 0)
  );
}

function requireOfflineHoldSnapshot(
  value: unknown,
  expectedRevision?: number,
): OfflineHoldSnapshot {
  if (
    !validOfflineHoldSnapshot(value) ||
    (expectedRevision !== undefined &&
      (!nonNegativeInteger(expectedRevision) ||
        !nonNegativeInteger(value.revision) ||
        value.revision <= expectedRevision))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as OfflineHoldSnapshot;
}

function validOfflineHoldSnapshot(
  value: unknown,
): value is Record<string, unknown> {
  if (
    !hasClosedFields(
      value,
      [
        "state",
        "revision",
        "since",
        "activeActions",
        "enteringActions",
        "activeEgress",
        "queuedRequests",
        "heldBytes",
        "safeToDisconnect",
        "activeByKind",
        "queuedByKind",
      ],
      ["lastProbeReason"],
    ) ||
    !offlineHoldStates.has(String(value.state)) ||
    !nonNegativeInteger(value.revision) ||
    !validTimestamp(value.since) ||
    !nonNegativeInteger(value.activeActions) ||
    !nonNegativeInteger(value.enteringActions) ||
    !nonNegativeInteger(value.activeEgress) ||
    !nonNegativeInteger(value.queuedRequests) ||
    !nonNegativeInteger(value.heldBytes) ||
    typeof value.safeToDisconnect !== "boolean" ||
    !validOfflineEgressCounts(value.activeByKind) ||
    !validOfflineEgressCounts(value.queuedByKind) ||
    (value.lastProbeReason !== undefined &&
      !offlineProbeReasons.has(String(value.lastProbeReason)))
  ) {
    return false;
  }
  const activeCount = sumSafeIntegers(Object.values(value.activeByKind));
  const queuedCount = sumSafeIntegers(Object.values(value.queuedByKind));
  if (
    activeCount === undefined ||
    queuedCount === undefined ||
    activeCount !== value.activeEgress ||
    queuedCount !== value.queuedRequests ||
    value.enteringActions > value.activeActions ||
    (value.queuedRequests === 0 && value.heldBytes !== 0) ||
    value.safeToDisconnect !==
      (value.state === "held" &&
        value.activeEgress === 0 &&
        value.enteringActions === 0)
  ) {
    return false;
  }
  if (value.state === "unbound") {
    return (
      value.revision === 0 &&
      value.activeActions === 0 &&
      value.enteringActions === 0 &&
      value.activeEgress === 0 &&
      value.queuedRequests === 0 &&
      value.heldBytes === 0 &&
      value.lastProbeReason === undefined
    );
  }
  if (value.revision === 0) {
    return false;
  }
  if (
    value.state === "online" &&
    (value.queuedRequests !== 0 || value.heldBytes !== 0)
  ) {
    return false;
  }
  if (
    (value.state === "held" || value.state === "probing") &&
    (value.activeEgress !== 0 || value.enteringActions !== 0)
  ) {
    return false;
  }
  if (
    value.state === "entering" &&
    value.activeEgress === 0 &&
    value.enteringActions === 0
  ) {
    return false;
  }
  return (
    value.lastProbeReason === undefined ||
    value.state === "held" ||
    value.state === "probing" ||
    value.state === "stopping"
  );
}

function validOfflineEgressCounts(
  value: unknown,
): value is Record<string, number> {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).length <= offlineEgressKinds.size &&
    Object.entries(value).every(
      ([kind, count]) =>
        offlineEgressKinds.has(kind) && positiveInteger(count),
    )
  );
}

function sumSafeIntegers(values: readonly number[]): number | undefined {
  let total = 0;
  for (const value of values) {
    total += value;
    if (!Number.isSafeInteger(total)) {
      return undefined;
    }
  }
  return total;
}

function requireAccessApplyResponse(
  value: unknown,
  expectedRevision: number,
): AccessApplyResponse {
  if (
    !nonNegativeInteger(expectedRevision) ||
    expectedRevision >= Number.MAX_SAFE_INTEGER ||
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value)
  ) {
    throw new ControlContractError();
  }
  const response = value as Record<string, unknown>;
  if (
    response.outcome !== "committed" ||
    !positiveInteger(response.revision) ||
    response.revision !== expectedRevision + 1
  ) {
    throw new ControlContractError();
  }
  if (response.applicationState === "active") {
    if (!hasClosedFields(response, [
      "outcome",
      "revision",
      "applicationState",
      "planHash",
    ])) {
      throw new ControlContractError();
    }
    if (!validPlanHash(response.planHash)) {
      throw new ControlContractError();
    }
  } else if (
    (response.applicationState !== "inactive" &&
      response.applicationState !== "unavailable") ||
    !hasClosedFields(response, ["outcome", "revision", "applicationState"])
  ) {
    throw new ControlContractError();
  }
  return response as unknown as AccessApplyResponse;
}

const accessDeletionBlockers = new Set([
  "disable_access_first",
  "active_capture_runs",
  "confirm_workspace_retirement",
  "proxy_client_bindings",
]);

function validAccessDeletionImpactToken(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9_-]{43}$/u.test(value);
}

function requireAccessDeletionPreview(
  value: unknown,
  expectedAccessId: string,
  expectedRevision: number,
): AccessDeletionPreview {
  if (
    !hasClosedFields(value, [
      "accessId",
      "name",
      "revision",
      "status",
      "workspaceBindingCount",
      "activeCaptureRunCount",
      "proxyClientBindingCount",
      "exclusiveSecretCount",
      "sharedSecretCount",
      "impactToken",
      "blockers",
    ]) ||
    value.accessId !== expectedAccessId ||
    value.revision !== expectedRevision ||
    !validDisplayLabel(value.name, 256, false) ||
    !accessStatuses.has(String(value.status)) ||
    !nonNegativeInteger(value.workspaceBindingCount) ||
    !nonNegativeInteger(value.activeCaptureRunCount) ||
    (value.workspaceBindingCount === 0 && value.activeCaptureRunCount !== 0) ||
    !nonNegativeInteger(value.proxyClientBindingCount) ||
    !nonNegativeInteger(value.exclusiveSecretCount) ||
    !nonNegativeInteger(value.sharedSecretCount) ||
    !validAccessDeletionImpactToken(value.impactToken) ||
    !Array.isArray(value.blockers) ||
    value.blockers.length > accessDeletionBlockers.size ||
    new Set(value.blockers).size !== value.blockers.length ||
    !value.blockers.every(
      (blocker) =>
        typeof blocker === "string" && accessDeletionBlockers.has(blocker),
    ) ||
    (value.status === "disabled") !==
      !value.blockers.includes("disable_access_first") ||
    (value.activeCaptureRunCount > 0) !==
      value.blockers.includes("active_capture_runs") ||
    (value.workspaceBindingCount > 0) !==
      value.blockers.includes("confirm_workspace_retirement") ||
    (value.proxyClientBindingCount > 0) !==
      value.blockers.includes("proxy_client_bindings")
  ) {
    throw new ControlContractError();
  }
  return value as unknown as AccessDeletionPreview;
}

function requireAccessDeletionResponse(
  value: unknown,
  expectedRevision: number,
): AccessDeletionResponse {
  if (
    !hasClosedFields(value, ["outcome", "revision"]) ||
    value.outcome !== "deleted" ||
    value.revision !== expectedRevision
  ) {
    throw new ControlContractError();
  }
  return value as unknown as AccessDeletionResponse;
}

function requireAccessAddCandidateResponse(
  value: unknown,
  expectedRevision: number,
): AccessAddCandidateResponse {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value)
  ) {
    throw new ControlContractError();
  }
  const response = value as Record<string, unknown>;
  if (
    !hasClosedFields(response.candidate, ["profileId", "credentialId"]) ||
    !validResourceId(response.candidate.profileId) ||
    !validResourceId(response.candidate.credentialId)
  ) {
    throw new ControlContractError();
  }
  const base = { ...response };
  delete base.candidate;
  requireAccessApplyResponse(base, expectedRevision);
  return response as unknown as AccessAddCandidateResponse;
}

const accessStatuses = new Set(["draft", "enabled", "disabled"]);
const accessDialects = new Set([
  "anthropic-messages",
  "openai-chat",
  "openai-responses",
]);
const accessProviderCapabilities = new Set([
  "messages",
  "streaming",
  "tool_calls",
]);
const accessFallbackPolicies = new Set([
  "disabled",
  "pre_first_byte_idempotent_only",
]);
const accessProfileKinds = new Set(["original_passthrough", "managed"]);
const accessCredentialSources = new Set([
  "client_passthrough",
  "managed_account",
]);
const accessProcessingModes = new Set(["observe_only", "managed"]);
const maximumAccessDirectoryItems = 1_024;
const maximumAccessNameBytes = 256;
const maximumAccessDescriptionBytes = 4_096;
const maximumAccessOriginBytes = 2_048;
const maximumAccessModelNameBytes = 256;
const maximumAccessRouteSets = 64;
const maximumAccessCapabilities = 64;
const maximumAccessPluginBindings = 128;

// Go orders persisted UTF-8 identifiers by Unicode scalar value. Keep the
// browser's closed contract independent of the user's locale; localeCompare
// can otherwise reject a correctly ordered directory on some machines.
export function compareResourceIds(left: string, right: string): number {
  const leftScalars = Array.from(left, (character) =>
    character.codePointAt(0),
  );
  const rightScalars = Array.from(right, (character) =>
    character.codePointAt(0),
  );
  const sharedLength = Math.min(leftScalars.length, rightScalars.length);
  for (let index = 0; index < sharedLength; index += 1) {
    const difference = leftScalars[index]! - rightScalars[index]!;
    if (difference !== 0) {
      return difference;
    }
  }
  return leftScalars.length - rightScalars.length;
}

function requireAccessDirectoryPage(value: unknown): AccessDirectoryPage {
  if (
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumAccessDirectoryItems
  ) {
    throw new ControlContractError();
  }
  let previousAccessId = "";
  const seen = new Set<string>();
  for (const item of value.items) {
    if (
      !hasClosedFields(item, [
        "accessId",
        "name",
        "description",
        "status",
        "revision",
        "clientOrigin",
        "clientDialect",
      ]) ||
      !validResourceId(item.accessId) ||
      !validTrimmedString(item.name, maximumAccessNameBytes, false) ||
      !validTrimmedString(
        item.description,
        maximumAccessDescriptionBytes,
        true,
      ) ||
      !accessStatuses.has(String(item.status)) ||
      !positiveInteger(item.revision) ||
      !validClientOrigin(item.clientOrigin) ||
      !accessDialects.has(String(item.clientDialect)) ||
      seen.has(item.accessId) ||
      (previousAccessId !== "" &&
        compareResourceIds(previousAccessId, item.accessId) >= 0)
    ) {
      throw new ControlContractError();
    }
    seen.add(item.accessId);
    previousAccessId = item.accessId;
  }
  return value as unknown as AccessDirectoryPage;
}

function requireAccessDetail(
  value: unknown,
  expectedAccessId: string,
): AccessDetail {
  if (
    !hasClosedFields(value, [
      "revision",
      "access",
      "agentEndpoint",
      "profiles",
      "providerTargets",
      "accountBindings",
      "routeSets",
      "egressPolicy",
      "pluginPlan",
    ]) ||
    !positiveInteger(value.revision) ||
    !validAccessDetailBinding(value.access, expectedAccessId) ||
    !validAccessDetailEndpoint(
      value.agentEndpoint,
      value.access.agentEndpointId,
    ) ||
    !Array.isArray(value.profiles) ||
    value.profiles.length === 0 ||
    value.profiles.length > maximumEndpointProfiles ||
    !Array.isArray(value.providerTargets) ||
    value.providerTargets.length === 0 ||
    value.providerTargets.length > maximumEndpointProfiles ||
    !Array.isArray(value.accountBindings) ||
    value.accountBindings.length > maximumAccountBindings ||
    !Array.isArray(value.routeSets) ||
    value.routeSets.length === 0 ||
    value.routeSets.length > maximumAccessRouteSets ||
    !validAccessDetailEgressPolicy(
      value.egressPolicy,
      value.access.egressPolicyId,
    ) ||
    !validAccessDetailPluginPlan(value.pluginPlan)
  ) {
    throw new ControlContractError();
  }

  const profiles = new Map<string, Record<string, unknown>>();
  for (const profile of value.profiles) {
    if (!validAccessDetailProfile(profile) || profiles.has(profile.id)) {
      throw new ControlContractError();
    }
    profiles.set(profile.id, profile);
  }
  if (
    profiles.size !== value.access.profileIds.length ||
    !value.access.profileIds.every((profileId) => profiles.has(profileId))
  ) {
    throw new ControlContractError();
  }

  const targets = new Map<string, Record<string, unknown>>();
  for (const target of value.providerTargets) {
    if (!validAccessDetailTarget(target) || targets.has(target.id)) {
      throw new ControlContractError();
    }
    const profile = profiles.get(target.profileId);
    if (
      profile === undefined ||
      profile.targetId !== target.id ||
      profile.backendDialect !== target.protocol ||
      (profile.kind === "original_passthrough" &&
        (target.origin !==
          (value.agentEndpoint as Record<string, unknown>).clientOrigin ||
          target.protocol !==
            (value.agentEndpoint as Record<string, unknown>).clientDialect))
    ) {
      throw new ControlContractError();
    }
    targets.set(target.id, target);
  }
  if (
    targets.size !== profiles.size ||
    Array.from(profiles.values()).some(
      (profile) => !targets.has(String(profile.targetId)),
    )
  ) {
    throw new ControlContractError();
  }

  const accountBindings = new Map<string, Record<string, unknown>>();
  for (const binding of value.accountBindings) {
    if (
      !validAccessDetailAccountBinding(binding) ||
      accountBindings.has(binding.id) ||
      !profiles.has(binding.profileId)
    ) {
      throw new ControlContractError();
    }
    accountBindings.set(binding.id, binding);
  }
  const referencedAccountIds = new Set<string>();
  for (const profile of profiles.values()) {
    const accountBindingIds = profile.accountBindingIds as string[];
    for (const bindingId of accountBindingIds) {
      const binding = accountBindings.get(bindingId);
      if (
        binding === undefined ||
        binding.profileId !== profile.id ||
        referencedAccountIds.has(bindingId)
      ) {
        throw new ControlContractError();
      }
      referencedAccountIds.add(bindingId);
    }
    if (profile.kind === "managed") {
      const defaultBinding = accountBindings.get(
        String(profile.defaultAccountBindingId),
      );
      if (
        defaultBinding === undefined ||
        defaultBinding.profileId !== profile.id
      ) {
        throw new ControlContractError();
      }
    } else if (
      accountBindingIds.length !== 0 ||
      profile.defaultAccountBindingId !== ""
    ) {
      throw new ControlContractError();
    }
  }
  if (referencedAccountIds.size !== accountBindings.size) {
    throw new ControlContractError();
  }

  const routeSets = new Set<string>();
  for (const routeSet of value.routeSets) {
    if (
      !validAccessDetailRouteSet(routeSet, profiles) ||
      routeSets.has(routeSet.id)
    ) {
      throw new ControlContractError();
    }
    routeSets.add(routeSet.id);
  }
  if (!routeSets.has(value.access.defaultRouteSetId)) {
    throw new ControlContractError();
  }

  return value as unknown as AccessDetail;
}

function validAccessDetailBinding(
  value: unknown,
  expectedAccessId: string,
): value is Record<string, unknown> & {
  defaultRouteSetId: string;
  profileIds: string[];
} {
  return (
    hasClosedFields(value, [
      "id",
      "name",
      "description",
      "status",
      "agentEndpointId",
      "defaultRouteSetId",
      "profileIds",
      "egressPolicyId",
    ]) &&
    validResourceId(value.id) &&
    value.id === expectedAccessId &&
    validTrimmedString(value.name, maximumAccessNameBytes, false) &&
    validTrimmedString(
      value.description,
      maximumAccessDescriptionBytes,
      true,
    ) &&
    accessStatuses.has(String(value.status)) &&
    validResourceId(value.agentEndpointId) &&
    validResourceId(value.defaultRouteSetId) &&
    validUniqueResourceIds(value.profileIds, maximumEndpointProfiles) &&
    validResourceId(value.egressPolicyId)
  );
}

function validAccessDetailEndpoint(
  value: unknown,
  expectedEndpointId: unknown,
): boolean {
  return (
    hasClosedFields(value, ["id", "clientOrigin", "clientDialect"]) &&
    validResourceId(value.id) &&
    value.id === expectedEndpointId &&
    validClientOrigin(value.clientOrigin) &&
    accessDialects.has(String(value.clientDialect))
  );
}

function validAccessDetailProfile(
  value: unknown,
): value is Record<string, unknown> & {
  id: string;
  accountBindingIds: string[];
  defaultAccountBindingId: string;
  kind: "original_passthrough" | "managed";
} {
  return (
    hasClosedFields(value, [
      "id",
      "kind",
      "credentialSource",
      "processingMode",
      "name",
      "description",
      "backendDialect",
      "targetId",
      "upstreamWireProfileRef",
      "defaultModelPolicy",
      "accountBindingIds",
      "defaultAccountBindingId",
    ]) &&
    validResourceId(value.id) &&
    accessProfileKinds.has(String(value.kind)) &&
    accessCredentialSources.has(String(value.credentialSource)) &&
    accessProcessingModes.has(String(value.processingMode)) &&
    validTrimmedString(value.name, maximumAccessNameBytes, false) &&
    validTrimmedString(
      value.description,
      maximumAccessDescriptionBytes,
      true,
    ) &&
    accessDialects.has(String(value.backendDialect)) &&
    validResourceId(value.targetId) &&
    validResourceId(value.upstreamWireProfileRef) &&
    validAccessDetailModelPolicy(value.defaultModelPolicy) &&
    validUniqueResourceIdsAllowEmpty(
      value.accountBindingIds,
      maximumAccountBindings,
    ) &&
    ((value.kind === "original_passthrough" &&
      value.credentialSource === "client_passthrough" &&
      value.processingMode === "observe_only" &&
      value.upstreamWireProfileRef === "follow-client" &&
      (value.defaultModelPolicy as Record<string, unknown>).mode ===
        "passthrough" &&
      value.accountBindingIds.length === 0 &&
      value.defaultAccountBindingId === "") ||
      (value.kind === "managed" &&
        value.credentialSource === "managed_account" &&
        value.processingMode === "managed" &&
        value.accountBindingIds.length > 0 &&
        validResourceId(value.defaultAccountBindingId) &&
        value.accountBindingIds.includes(value.defaultAccountBindingId)))
  );
}

function validAccessDetailModelPolicy(value: unknown): boolean {
  if (!hasClosedFields(value, ["mode"], ["fixedModel", "mappingRef"])) {
    return false;
  }
  switch (value.mode) {
    case "passthrough":
      return hasClosedFields(value, ["mode"]);
    case "fixed":
      return (
        hasClosedFields(value, ["mode", "fixedModel"]) &&
        validTrimmedString(
          value.fixedModel,
          maximumAccessModelNameBytes,
          false,
        )
      );
    case "map":
      return (
        hasClosedFields(value, ["mode", "mappingRef"]) &&
        validResourceId(value.mappingRef)
      );
    default:
      return false;
  }
}

function validAccessDetailTarget(
  value: unknown,
): value is Record<string, unknown> & { id: string; profileId: string } {
  return (
    hasClosedFields(value, [
      "id",
      "profileId",
      "origin",
      "protocol",
      "capabilities",
    ]) &&
    validResourceId(value.id) &&
    validResourceId(value.profileId) &&
    validProviderOrigin(value.origin) &&
    accessDialects.has(String(value.protocol)) &&
    Array.isArray(value.capabilities) &&
    value.capabilities.length > 0 &&
    value.capabilities.length <= maximumAccessCapabilities &&
    value.capabilities.every((capability) =>
      accessProviderCapabilities.has(String(capability)),
    ) &&
    new Set(value.capabilities).size === value.capabilities.length
  );
}

function validAccessDetailAccountBinding(
  value: unknown,
): value is Record<string, unknown> & { id: string; profileId: string } {
  return (
    hasClosedFields(value, [
      "id",
      "profileId",
      "label",
      "authDriverRef",
      "enabled",
      "secretHandling",
    ]) &&
    validResourceId(value.id) &&
    validResourceId(value.profileId) &&
    validTrimmedString(value.label, maximumAccessNameBytes, false) &&
    validResourceId(value.authDriverRef) &&
    typeof value.enabled === "boolean" &&
    value.secretHandling === "preserve_existing"
  );
}

function validAccessDetailRouteSet(
  value: unknown,
  profiles: ReadonlyMap<string, unknown>,
): value is Record<string, unknown> & { id: string } {
  return (
    hasClosedFields(value, ["id", "candidateProfileIds", "fallback"]) &&
    validResourceId(value.id) &&
    validUniqueResourceIds(
      value.candidateProfileIds,
      maximumEndpointProfiles,
    ) &&
    value.candidateProfileIds.every((profileId) => profiles.has(profileId)) &&
    accessFallbackPolicies.has(String(value.fallback)) &&
    (value.fallback !== "pre_first_byte_idempotent_only" ||
      value.candidateProfileIds.length >= 2)
  );
}

function validAccessDetailEgressPolicy(
  value: unknown,
  expectedPolicyId: unknown,
): boolean {
  return (
    hasClosedFields(value, ["id", "mode"]) &&
    validResourceId(value.id) &&
    value.id === expectedPolicyId &&
    value.mode === "direct"
  );
}

function validAccessDetailPluginPlan(value: unknown): boolean {
  return (
    hasClosedFields(value, ["mode", "bindingIds"]) &&
    value.mode === "pass_through" &&
    validUniqueResourceIdsAllowEmpty(
      value.bindingIds,
      maximumAccessPluginBindings,
    )
  );
}

function validUniqueResourceIdsAllowEmpty(
  value: unknown,
  maximumItems: number,
): value is string[] {
  return (
    Array.isArray(value) &&
    value.length <= maximumItems &&
    value.every(validResourceId) &&
    new Set(value).size === value.length
  );
}

function requireAccessPlanSummary(
  value: unknown,
  expectedAccessId: string,
): AccessPlanSummary {
  if (
    !hasClosedFields(value, [
      "accessId",
      "revision",
      "planHash",
      "profiles",
      "accountBindings",
    ]) ||
    !validResourceId(value.accessId) ||
    value.accessId !== expectedAccessId ||
    !positiveInteger(value.revision) ||
    !validPlanHash(value.planHash) ||
    !validUniqueResourceIds(value.profiles, maximumEndpointProfiles) ||
    !Array.isArray(value.accountBindings) ||
    value.accountBindings.length > maximumAccountBindings
  ) {
    throw new ControlContractError();
  }
  const profileIds = new Set(value.profiles);
  const bindingIds = new Set<string>();
  const referencedProfiles = new Set<string>();
  for (const binding of value.accountBindings) {
    if (
      !hasClosedFields(binding, ["id", "profileId"]) ||
      !validResourceId(binding.id) ||
      !validResourceId(binding.profileId) ||
      !profileIds.has(binding.profileId) ||
      bindingIds.has(binding.id)
    ) {
      throw new ControlContractError();
    }
    bindingIds.add(binding.id);
    referencedProfiles.add(binding.profileId);
  }
  const unboundProfiles = value.profiles.filter(
    (profileId) => !referencedProfiles.has(profileId),
  );
  if (
    referencedProfiles.has(originalPassthroughProfileId) ||
    unboundProfiles.length !== 1 ||
    unboundProfiles[0] !== originalPassthroughProfileId
  ) {
    throw new ControlContractError();
  }
  return value as unknown as AccessPlanSummary;
}

const originalPassthroughProfileId = "original-passthrough";

function validUniqueResourceIds(
  value: unknown,
  maximumItems: number,
): value is string[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= maximumItems &&
    value.every(validResourceId) &&
    new Set(value).size === value.length
  );
}

function requireCredentialView(
  value: unknown,
  expectedProfileId: string,
  expectedCredentialId: string,
  replacement: boolean,
): CredentialView {
  if (
    !hasClosedFields(value, [
      "credentialId",
      "profileId",
      "secretState",
      "secretRevision",
    ]) ||
    !validResourceId(value.credentialId) ||
    value.credentialId !== expectedCredentialId ||
    !validResourceId(value.profileId) ||
    value.profileId !== expectedProfileId ||
    !credentialStates.has(String(value.secretState)) ||
    !nonNegativeInteger(value.secretRevision) ||
    (value.secretState === "configured" && value.secretRevision === 0) ||
    (value.secretState === "missing" && value.secretRevision !== 0) ||
    (replacement &&
      (value.secretState === "missing" || value.secretRevision === 0))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as CredentialView;
}

function requireCredentialReplacementResponse(
  value: unknown,
  expectedProfileId: string,
  expectedCredentialId: string,
  expectedRevision: number,
): CredentialView {
  const response = requireCredentialView(
    value,
    expectedProfileId,
    expectedCredentialId,
    true,
  );
  if (
    !nonNegativeInteger(expectedRevision) ||
    expectedRevision >= Number.MAX_SAFE_INTEGER ||
    response.secretRevision !== expectedRevision + 1
  ) {
    throw new ControlContractError();
  }
  return response;
}

function validResourceId(value: unknown): value is string {
  return validTrimmedString(value, maximumResourceIdBytes, false);
}

function validPlanHash(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
}

function requireActivityPage(value: unknown): ActivityPage {
  if (
    !hasClosedFields(value, ["items"], ["nextCursor"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumActivityPageItems ||
    !value.items.every(validActivityRecord) ||
    (value.nextCursor !== undefined && !validOpaqueCursor(value.nextCursor))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ActivityPage;
}

function validActivityRecord(value: unknown): boolean {
  if (!hasClosedFields(value, ["id", "occurredAt", "accessId", "status"])) {
    return false;
  }
  return (
    validRouteIdentity(value.id) &&
    validTimestamp(value.occurredAt) &&
    validResourceId(value.accessId) &&
    validActivityStatus(value.status)
  );
}

function validActivityStatus(value: unknown): value is string {
  return (
    validTrimmedString(value, 128, false) &&
    !/\p{C}/u.test(value)
  );
}

function requireExchangeDetail(
  value: unknown,
  expectedExchangeId: string,
): ExchangeDetail {
  if (
    !hasClosedFields(value, ["id", "accessId", "status", "processingTrace"]) ||
    !validRouteIdentity(value.id) ||
    value.id !== expectedExchangeId ||
    !validResourceId(value.accessId) ||
    !validActivityStatus(value.status) ||
    !validExchangeProcessingTrace(value.processingTrace)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ExchangeDetail;
}

function validExchangeProcessingTrace(value: unknown): boolean {
  if (
    !hasClosedFields(
      value,
      ["pluginRunIds", "attemptIds", "result"],
      ["upstreamProfileId", "credentialId", "egressProxyId"],
    ) ||
    !validIdentityArray(value.pluginRunIds, 500, true) ||
    new Set(value.pluginRunIds).size !== value.pluginRunIds.length ||
    !validIdentityArray(value.attemptIds, 500, true) ||
    new Set(value.attemptIds).size !== value.attemptIds.length ||
    !validIdentity(value.result)
  ) {
    return false;
  }
  return [
    value.upstreamProfileId,
    value.credentialId,
    value.egressProxyId,
  ].every((field) => field === undefined || validIdentity(field));
}

const approvalStates = new Set([
  "pending",
  "allowed",
  "denied",
  "canceled",
  "expired",
]);
const approvalDecisions = new Set(["allow-once", "deny"]);
const approvalScopes = new Set(["request", "host_port"]);

type ApprovalPresentation = {
  readonly risk: string;
  readonly titleKey: string;
  readonly summaryKey: string;
  readonly choices: readonly {
    readonly decision: string;
    readonly scope: string;
    readonly labelKey: string;
  }[];
};

function approvalPresentation(kind: unknown): ApprovalPresentation | undefined {
  switch (kind) {
    case "tool_intent":
      return {
        risk: "high",
        titleKey: "approval.toolIntent.title",
        summaryKey: "approval.toolIntent.summary",
        choices: [
          {
            decision: "allow-once",
            scope: "request",
            labelKey: "approval.toolIntent.choice.allowOnce",
          },
          {
            decision: "deny",
            scope: "request",
            labelKey: "approval.toolIntent.choice.deny",
          },
        ],
      };
    case "network_ask":
      return {
        risk: "medium",
        titleKey: "approval.networkAsk.title",
        summaryKey: "approval.networkAsk.summary",
        choices: [
          {
            decision: "allow-once",
            scope: "request",
            labelKey: "approval.networkAsk.choice.allowOnce",
          },
          {
            decision: "allow-once",
            scope: "host_port",
            labelKey: "approval.networkAsk.choice.allowHostPort",
          },
          {
            decision: "deny",
            scope: "request",
            labelKey: "approval.networkAsk.choice.denyOnce",
          },
          {
            decision: "deny",
            scope: "host_port",
            labelKey: "approval.networkAsk.choice.denyHostPort",
          },
        ],
      };
    case "client_root_ask":
      return {
        risk: "high",
        titleKey: "approval.clientRootAsk.title",
        summaryKey: "approval.clientRootAsk.summary",
        choices: [
          {
            decision: "allow-once",
            scope: "request",
            labelKey: "approval.clientRootAsk.choice.allowOnce",
          },
          {
            decision: "deny",
            scope: "request",
            labelKey: "approval.clientRootAsk.choice.denyOnce",
          },
        ],
      };
    default:
      return undefined;
  }
}

function requireApprovalPage(value: unknown): ApprovalPage {
  if (
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every(
      (approval) => validApprovalView(approval) && approval.state === "pending",
    )
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ApprovalPage;
}

function requireApprovalView(value: unknown): ApprovalView {
  if (!validApprovalView(value)) {
    throw new ControlContractError();
  }
  return value as unknown as ApprovalView;
}

function requireApprovalDecisionResponse(
  value: unknown,
  expectedApproval: ApprovalView,
  expectedChoice: ApprovalChoice,
): ApprovalView {
  const response = requireApprovalView(value);
  const expectedState =
    expectedChoice.decision === "allow-once"
      ? "allowed"
      : expectedChoice.decision === "deny"
        ? "denied"
        : undefined;
  if (
    expectedState === undefined ||
    !validApprovalView(expectedApproval) ||
    expectedApproval.state !== "pending" ||
    !expectedApproval.choices.some((choice) =>
      sameApprovalChoice(choice, expectedChoice),
    ) ||
    !positiveInteger(expectedApproval.revision) ||
    expectedApproval.revision >= Number.MAX_SAFE_INTEGER ||
    !sameApprovalAuthorizationContext(response, expectedApproval) ||
    response.revision !== expectedApproval.revision + 1 ||
    response.state !== expectedState ||
    response.decision !== expectedChoice.decision ||
    response.decisionScope !== expectedChoice.scope ||
    (expectedChoice.decision === "deny" &&
      response.terminalReason !== "user_denied")
  ) {
    throw new ControlContractError();
  }
  return response;
}

function sameApprovalAuthorizationContext(
  response: ApprovalView,
  expected: ApprovalView,
): boolean {
  return (
    response.id === expected.id &&
    response.kind === expected.kind &&
    response.risk === expected.risk &&
    response.titleKey === expected.titleKey &&
    response.summaryKey === expected.summaryKey &&
    response.aggregateKey === expected.aggregateKey &&
    sameOptionalApprovalScalar(response, expected, "exchangeId") &&
    sameOptionalApprovalScalar(response, expected, "accessId") &&
    sameOptionalApprovalScalar(response, expected, "planRevision") &&
    sameOptionalApprovalScalar(response, expected, "planHash") &&
    sameApprovalTarget(response, expected) &&
    sameStringArray(response.subjectRefs, expected.subjectRefs) &&
    sameStringArray(response.subjectLabels, expected.subjectLabels) &&
    response.requestCount === expected.requestCount &&
    response.waiterCount === expected.waiterCount &&
    response.choices.length === expected.choices.length &&
    response.choices.every((choice, index) => {
      const expectedChoice = expected.choices[index];
      return (
        expectedChoice !== undefined &&
        sameApprovalChoice(choice, expectedChoice)
      );
    }) &&
    response.createdAt === expected.createdAt &&
    response.expiresAt === expected.expiresAt
  );
}

function sameOptionalApprovalScalar(
  response: ApprovalView,
  expected: ApprovalView,
  field: "exchangeId" | "accessId" | "planRevision" | "planHash",
): boolean {
  return (
    Object.hasOwn(response, field) === Object.hasOwn(expected, field) &&
    response[field] === expected[field]
  );
}

function sameApprovalTarget(
  response: ApprovalView,
  expected: ApprovalView,
): boolean {
  if (
    Object.hasOwn(response, "target") !== Object.hasOwn(expected, "target")
  ) {
    return false;
  }
  if (response.target === undefined || expected.target === undefined) {
    return response.target === expected.target;
  }
  return (
    response.target.host === expected.target.host &&
    response.target.port === expected.target.port
  );
}

function sameStringArray(
  response: readonly string[],
  expected: readonly string[],
): boolean {
  return (
    response.length === expected.length &&
    response.every((value, index) => value === expected[index])
  );
}

function sameApprovalChoice(
  response: ApprovalChoice,
  expected: ApprovalChoice,
): boolean {
  return (
    response.decision === expected.decision &&
    response.scope === expected.scope &&
    response.labelKey === expected.labelKey
  );
}

function validApprovalView(value: unknown): value is Record<string, unknown> {
  if (
    !hasClosedFields(
      value,
      [
        "id",
        "revision",
        "kind",
        "state",
        "risk",
        "titleKey",
        "summaryKey",
        "aggregateKey",
        "subjectRefs",
        "subjectLabels",
        "requestCount",
        "waiterCount",
        "choices",
        "createdAt",
        "expiresAt",
      ],
      [
        "exchangeId",
        "accessId",
        "planRevision",
        "planHash",
        "target",
        "resolvedAt",
        "decision",
        "decisionScope",
        "terminalReason",
      ],
    ) ||
    !validIdentity(value.id) ||
    !positiveInteger(value.revision) ||
    !approvalStates.has(String(value.state)) ||
    !validIdentity(value.aggregateKey) ||
    !validIdentityArray(value.subjectRefs, 128, false) ||
    !validIdentityArray(value.subjectLabels, 128, false) ||
    value.subjectRefs.length !== value.subjectLabels.length ||
    !positiveUint32(value.requestCount) ||
    !positiveUint32(value.waiterCount) ||
    value.waiterCount > value.requestCount ||
    !validTimestamp(value.createdAt) ||
    !validTimestamp(value.expiresAt) ||
    Date.parse(value.createdAt) >= Date.parse(value.expiresAt)
  ) {
    return false;
  }

  const presentation = approvalPresentation(value.kind);
  if (
    presentation === undefined ||
    value.risk !== presentation.risk ||
    value.titleKey !== presentation.titleKey ||
    value.summaryKey !== presentation.summaryKey ||
    !validApprovalChoices(value.choices, presentation.choices)
  ) {
    return false;
  }
  if (
    (value.exchangeId !== undefined && !validIdentity(value.exchangeId)) ||
    (value.accessId !== undefined && !validIdentity(value.accessId)) ||
    (value.planRevision !== undefined && !positiveInteger(value.planRevision)) ||
    (value.planHash !== undefined &&
      (typeof value.planHash !== "string" ||
        !/^[a-f0-9]{64}$/u.test(value.planHash))) ||
    (value.resolvedAt !== undefined && !validTimestamp(value.resolvedAt)) ||
    (value.decision !== undefined &&
      !approvalDecisions.has(String(value.decision))) ||
    (value.decisionScope !== undefined &&
      !approvalScopes.has(String(value.decisionScope))) ||
    (value.terminalReason !== undefined && !validIdentity(value.terminalReason))
  ) {
    return false;
  }

  const hasPlanBinding = [
    value.exchangeId,
    value.accessId,
    value.planRevision,
    value.planHash,
  ].some((field) => field !== undefined);
  if (
    (value.kind === "tool_intent" || hasPlanBinding) &&
    (value.exchangeId === undefined ||
      value.accessId === undefined ||
      value.planRevision === undefined ||
      value.planHash === undefined)
  ) {
    return false;
  }

  if (value.kind === "network_ask") {
    if (!validApprovalTarget(value.target)) {
      return false;
    }
  } else if (value.target !== undefined) {
    return false;
  }

  const validScope =
    value.decisionScope === "request" ||
    (value.kind === "network_ask" && value.decisionScope === "host_port");
  switch (value.state) {
    case "pending":
      return (
        value.resolvedAt === undefined &&
        value.decision === undefined &&
        value.decisionScope === undefined &&
        value.terminalReason === undefined
      );
    case "allowed":
      return (
        validResolvedTime(value) &&
        value.decision === "allow-once" &&
        validScope &&
        value.terminalReason === undefined
      );
    case "denied":
      return (
        validResolvedTime(value) &&
        value.decision === "deny" &&
        validScope &&
        value.terminalReason !== undefined
      );
    case "canceled":
    case "expired":
      return (
        validResolvedTime(value) &&
        value.decision === undefined &&
        value.decisionScope === undefined &&
        value.terminalReason !== undefined
      );
    default:
      return false;
  }
}

function validApprovalChoices(
  value: unknown,
  expected: ApprovalPresentation["choices"],
): boolean {
  return (
    Array.isArray(value) &&
    value.length === expected.length &&
    value.every((choice, index) => {
      const wanted = expected[index];
      return (
        wanted !== undefined &&
        hasClosedFields(choice, ["decision", "scope", "labelKey"]) &&
        choice.decision === wanted.decision &&
        choice.scope === wanted.scope &&
        choice.labelKey === wanted.labelKey
      );
    })
  );
}

function validApprovalTarget(value: unknown): boolean {
  return (
    hasClosedFields(value, ["host", "port"]) &&
    typeof value.host === "string" &&
    value.host.length > 0 &&
    new TextEncoder().encode(value.host).byteLength <= 253 &&
    value.host.toLowerCase() === value.host &&
    !/[ \t\r\n]/u.test(value.host) &&
    validPort(value.port)
  );
}

function validResolvedTime(value: Record<string, unknown>): boolean {
  return (
    typeof value.resolvedAt === "string" &&
    typeof value.createdAt === "string" &&
    Date.parse(value.resolvedAt) >= Date.parse(value.createdAt)
  );
}

const connectionSourceConfidences = new Set([
  "unknown",
  "configured",
  "verified",
]);
const connectionDecisions = new Set(["allow", "deny", "ask"]);
const connectionDecryptions = new Set(["none", "blind", "mitm"]);
const connectionPhases = new Set([
  "attempted",
  "asked",
  "decided",
  "connected",
  "closed",
  "failed",
]);
const connectionOutcomes = new Set([
  "completed",
  "denied",
  "canceled",
  "failed",
]);
const connectionEgressScopes = new Set(["access", "network"]);
const connectionEgressSources = new Set([
  "access_rule",
  "access_plugin",
  "access_default",
  "network_rule",
  "network_default",
]);

function requireConnectionPage(value: unknown): ConnectionPage {
  if (
    !hasClosedFields(value, ["items"], ["nextCursor"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every(validConnectionRecord) ||
    (value.nextCursor !== undefined && !validCursor(value.nextCursor))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ConnectionPage;
}

function validConnectionRecord(value: unknown): boolean {
  if (
    !hasClosedFields(
      value,
      [
        "sequence",
        "connectionId",
        "sourceConfidence",
        "requestedHost",
        "port",
        "decryption",
        "phase",
        "bytesUp",
        "bytesDown",
        "startedAt",
      ],
      [
        "ingressId",
        "sourceLabel",
        "observedSni",
        "routeHost",
        "ip",
        "decision",
        "ruleId",
        "credentialBindingId",
        "egressScope",
        "egressSource",
        "egressRuleId",
        "egressSelectorRunId",
        "egressProxyId",
        "egressPolicyRevision",
        "endedAt",
        "outcome",
        "errorClass",
      ],
    ) ||
    !positiveInteger(value.sequence) ||
    !validIdentity(value.connectionId) ||
    !connectionSourceConfidences.has(String(value.sourceConfidence)) ||
    !validHost(value.requestedHost) ||
    !validPort(value.port) ||
    !connectionDecryptions.has(String(value.decryption)) ||
    !connectionPhases.has(String(value.phase)) ||
    !nonNegativeInteger(value.bytesUp) ||
    !nonNegativeInteger(value.bytesDown) ||
    !validTimestamp(value.startedAt) ||
    (value.decision !== undefined &&
      !connectionDecisions.has(String(value.decision))) ||
    (value.endedAt !== undefined && !validTimestamp(value.endedAt)) ||
    (value.outcome !== undefined &&
      !connectionOutcomes.has(String(value.outcome)))
  ) {
    return false;
  }

  for (const field of [
    "ingressId",
    "sourceLabel",
    "ruleId",
    "credentialBindingId",
    "egressRuleId",
    "egressSelectorRunId",
    "egressProxyId",
    "errorClass",
  ] as const) {
    if (value[field] !== undefined && !validIdentity(value[field])) {
      return false;
    }
  }
  for (const field of ["observedSni", "routeHost"] as const) {
    if (value[field] !== undefined && !validHost(value[field])) {
      return false;
    }
  }
  if (value.ip !== undefined && !validIpAddress(value.ip)) {
    return false;
  }
  if (
    (value.sourceConfidence === "verified" ||
      value.sourceConfidence === "configured") &&
    (value.ingressId === undefined || value.sourceLabel === undefined)
  ) {
    return false;
  }
  if (!validConnectionDecisionState(value) || !validConnectionEgress(value)) {
    return false;
  }
  return validConnectionTerminalState(value);
}

function validConnectionDecisionState(value: Record<string, unknown>): boolean {
  switch (value.decision) {
    case undefined:
      return (
        (value.phase === "attempted" || value.phase === "failed") &&
        value.ruleId === undefined
      );
    case "allow":
      return (
        value.ruleId !== undefined &&
        value.routeHost !== undefined &&
        value.decryption !== "none"
      );
    case "deny":
      return value.ruleId !== undefined;
    case "ask":
      return (
        value.ruleId !== undefined &&
        (value.phase === "asked" ||
          value.phase === "closed" ||
          value.phase === "failed")
      );
    default:
      return false;
  }
}

function validConnectionEgress(value: Record<string, unknown>): boolean {
  const fields = [
    value.egressScope,
    value.egressSource,
    value.egressRuleId,
    value.egressSelectorRunId,
    value.egressProxyId,
    value.egressPolicyRevision,
  ];
  if (fields.every((field) => field === undefined)) {
    return true;
  }
  if (
    !connectionEgressScopes.has(String(value.egressScope)) ||
    !connectionEgressSources.has(String(value.egressSource)) ||
    !positiveInteger(value.egressPolicyRevision)
  ) {
    return false;
  }
  return value.egressScope === "access"
    ? value.egressSource === "access_rule" ||
        value.egressSource === "access_plugin" ||
        value.egressSource === "access_default"
    : value.egressSource === "network_rule" ||
        value.egressSource === "network_default";
}

function validConnectionTerminalState(value: Record<string, unknown>): boolean {
  const terminal =
    value.phase === "closed" ||
    value.phase === "failed" ||
    (value.phase === "decided" && value.decision === "deny");
  if (!terminal) {
    return (
      value.endedAt === undefined &&
      value.outcome === undefined &&
      value.errorClass === undefined
    );
  }
  if (
    typeof value.endedAt !== "string" ||
    typeof value.startedAt !== "string" ||
    Date.parse(value.endedAt) < Date.parse(value.startedAt)
  ) {
    return false;
  }
  switch (value.outcome) {
    case "completed":
      return value.phase === "closed" && value.errorClass === undefined;
    case "denied":
      return (
        value.phase === "decided" &&
        value.decision === "deny" &&
        value.errorClass !== undefined
      );
    case "canceled":
      return value.phase === "closed" && value.errorClass !== undefined;
    case "failed":
      return value.phase === "failed" && value.errorClass !== undefined;
    default:
      return false;
  }
}

const egressPurposes = new Set([
  "provider_attempt",
  "profile_operation",
  "original_origin",
  "agent_probe",
  "blind_tunnel",
  "auxiliary_llm",
  "language_transform",
  "plugin_catalog_sync",
  "plugin_artifact_fetch",
  "update",
]);
const egressPayloadClasses = new Set([
  "none",
  "control",
  "client_data",
  "client_semantic",
  "opaque_tunnel",
  "runtime",
]);
const egressParentKinds = new Set([
  "upstream_attempt",
  "client_operation",
  "original_request",
  "blind_connection",
  "runtime_action",
]);
const egressCallers = new Set(["core", "plugin"]);
const egressAuthorities = new Set(["access", "network", "runtime"]);
const egressOutcomes = new Set(["completed", "failed", "canceled"]);

function requireEgressAttemptPage(value: unknown): EgressAttemptPage {
  if (
    !hasClosedFields(value, ["items"], ["nextCursor"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every(validEgressAttemptRecord) ||
    (value.nextCursor !== undefined && !validCursor(value.nextCursor))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as EgressAttemptPage;
}

function validEgressAttemptRecord(value: unknown): boolean {
  if (
    !hasClosedFields(
      value,
      [
        "sequence",
        "id",
        "purpose",
        "payloadClass",
        "parent",
        "caller",
        "targetOrigin",
        "decision",
        "reusedTransport",
        "startedAt",
        "terminal",
        "bytesOut",
        "bytesIn",
      ],
      ["connectionId", "callerId", "outcome", "errorClass", "completedAt"],
    ) ||
    !positiveInteger(value.sequence) ||
    !validIdentity(value.id) ||
    !egressPurposes.has(String(value.purpose)) ||
    !egressPayloadClasses.has(String(value.payloadClass)) ||
    !validEgressParent(value.parent) ||
    !egressCallers.has(String(value.caller)) ||
    !validTargetOrigin(value.targetOrigin) ||
    !validEgressDecision(value.decision) ||
    typeof value.reusedTransport !== "boolean" ||
    !validTimestamp(value.startedAt) ||
    typeof value.terminal !== "boolean" ||
    !nonNegativeInteger(value.bytesOut) ||
    !nonNegativeInteger(value.bytesIn) ||
    (value.connectionId !== undefined && !validIdentity(value.connectionId)) ||
    (value.callerId !== undefined && !validIdentity(value.callerId)) ||
    (value.outcome !== undefined && !egressOutcomes.has(String(value.outcome))) ||
    (value.errorClass !== undefined && !validIdentity(value.errorClass)) ||
    (value.completedAt !== undefined && !validTimestamp(value.completedAt))
  ) {
    return false;
  }
  if (
    (value.caller === "core" && value.callerId !== undefined) ||
    (value.caller === "plugin" && value.callerId === undefined) ||
    ((value.purpose === "plugin_catalog_sync" ||
      value.purpose === "plugin_artifact_fetch") &&
      value.caller !== "core")
  ) {
    return false;
  }
  if (
    !validEgressPurposeRelations(value) ||
    !validEgressPayloadForPurpose(value.purpose, value.payloadClass)
  ) {
    return false;
  }
  if (validEgressParent(value.parent)) {
    const parentId = value.parent.id;
    const exchangeId = value.parent.exchangeId;
    if (
      (typeof parentId === "string" && value.id.includes(parentId)) ||
      (typeof exchangeId === "string" && value.id.includes(exchangeId))
    ) {
      return false;
    }
  }
  if (!value.terminal) {
    return (
      value.outcome === undefined &&
      value.errorClass === undefined &&
      value.completedAt === undefined &&
      value.bytesOut === 0 &&
      value.bytesIn === 0
    );
  }
  return (
    value.outcome !== undefined &&
    typeof value.completedAt === "string" &&
    typeof value.startedAt === "string" &&
    Date.parse(value.completedAt) >= Date.parse(value.startedAt)
  );
}

function validEgressParent(value: unknown): value is Record<string, unknown> {
  return (
    hasClosedFields(value, ["kind", "id"], ["exchangeId"]) &&
    egressParentKinds.has(String(value.kind)) &&
    validIdentity(value.id) &&
    (value.exchangeId === undefined || validIdentity(value.exchangeId))
  );
}

function validEgressDecision(value: unknown): value is Record<string, unknown> {
  return (
    hasClosedFields(value, [
      "policyId",
      "policyRevision",
      "authority",
      "ruleId",
      "proxyId",
    ]) &&
    validIdentity(value.policyId) &&
    positiveInteger(value.policyRevision) &&
    egressAuthorities.has(String(value.authority)) &&
    validIdentity(value.ruleId) &&
    validIdentity(value.proxyId)
  );
}

function validEgressPurposeRelations(value: Record<string, unknown>): boolean {
  if (!validEgressParent(value.parent) || !validEgressDecision(value.decision)) {
    return false;
  }
  const noExchange = value.parent.exchangeId === undefined;
  switch (value.purpose) {
    case "provider_attempt":
      return (
        value.parent.kind === "upstream_attempt" &&
        value.parent.exchangeId !== undefined &&
        value.decision.authority === "access"
      );
    case "profile_operation":
      return (
        value.parent.kind === "client_operation" &&
        noExchange &&
        value.connectionId !== undefined &&
        value.decision.authority === "access"
      );
    case "original_origin":
    case "agent_probe":
      return (
        value.parent.kind === "original_request" &&
        noExchange &&
        value.connectionId !== undefined &&
        value.decision.authority === "network"
      );
    case "blind_tunnel":
      return (
        value.parent.kind === "blind_connection" &&
        noExchange &&
        value.connectionId !== undefined &&
        value.decision.authority === "network"
      );
    case "auxiliary_llm":
    case "language_transform":
    case "plugin_catalog_sync":
    case "plugin_artifact_fetch":
    case "update":
      return (
        value.parent.kind === "runtime_action" &&
        noExchange &&
        value.connectionId === undefined &&
        value.decision.authority === "runtime"
      );
    default:
      return false;
  }
}

function validEgressPayloadForPurpose(purpose: unknown, payload: unknown): boolean {
  switch (purpose) {
    case "provider_attempt":
    case "profile_operation":
      return true;
    case "original_origin":
    case "agent_probe":
      return payload !== "client_data" && payload !== "client_semantic";
    case "blind_tunnel":
      return payload === "opaque_tunnel";
    case "auxiliary_llm":
    case "language_transform":
    case "plugin_catalog_sync":
    case "plugin_artifact_fetch":
    case "update":
      return payload === "runtime";
    default:
      return false;
  }
}

function hasClosedFields(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const allowed = new Set([...required, ...optional]);
  const keys = Object.keys(value);
  return (
    required.every((field) => Object.hasOwn(value, field)) &&
    keys.every((field) => allowed.has(field))
  );
}

function validIdentity(value: unknown): value is string {
  return validTrimmedString(value, maximumIdentityBytes, false);
}

function validRouteIdentity(value: unknown): value is string {
  return validIdentity(value) && !/[\/\\\p{C}]/u.test(value);
}

function validTrimmedString(
  value: unknown,
  maximumBytes: number,
  allowEmpty: boolean,
): value is string {
  return (
    typeof value === "string" &&
    (allowEmpty || value.length > 0) &&
    value.trim() === value &&
    new TextEncoder().encode(value).byteLength <= maximumBytes &&
    validUnicodeString(value) &&
    !/\p{Cc}/u.test(value)
  );
}

function validUnicodeString(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const low = value.charCodeAt(index + 1);
      if (low < 0xdc00 || low > 0xdfff) {
        return false;
      }
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function validIdentityArray(
  value: unknown,
  maximumItems: number,
  allowEmpty: boolean,
): value is string[] {
  return (
    Array.isArray(value) &&
    (allowEmpty || value.length > 0) &&
    value.length <= maximumItems &&
    value.every(validIdentity)
  );
}

function nonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function positiveUint32(value: unknown): value is number {
  return positiveInteger(value) && value <= maximumUint32;
}

function validPort(value: unknown): value is number {
  return positiveInteger(value) && value <= 65_535;
}

function validHost(value: unknown): value is string {
  return (
    validTrimmedString(value, maximumHostBytes, false) &&
    !value.includes(":") &&
    !value.includes("/")
  );
}

function validIpAddress(value: unknown): value is string {
  if (!validTrimmedString(value, maximumHostBytes, false)) {
    return false;
  }
  const ipv4Parts = value.split(".");
  if (ipv4Parts.length === 4) {
    return ipv4Parts.every(
      (part) =>
        /^(?:0|[1-9]\d{0,2})$/u.test(part) &&
        Number(part) >= 0 &&
        Number(part) <= 255,
    );
  }
  if (!value.includes(":") || !/^[A-Fa-f0-9:.]+$/u.test(value)) {
    return false;
  }
  try {
    const parsed = new URL(`http://[${value}]/`);
    return parsed.hostname.startsWith("[") && parsed.hostname.endsWith("]");
  } catch {
    return false;
  }
}

function validCursor(value: unknown): value is string {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]{1,128}$/u.test(value)) {
    return false;
  }
  try {
    const normalized = value.replaceAll("-", "+").replaceAll("_", "/");
    const decoded = atob(
      normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "="),
    );
    if (!/^v1:[1-9]\d*$/u.test(decoded)) {
      return false;
    }
    const sequence = Number(decoded.slice(3));
    const canonical = btoa(decoded)
      .replaceAll("+", "-")
      .replaceAll("/", "_")
      .replace(/=+$/u, "");
    return positiveInteger(sequence) && canonical === value;
  } catch {
    return false;
  }
}

function validOpaqueCursor(value: unknown): value is string {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]{1,128}$/u.test(value)) {
    return false;
  }
  try {
    const normalized = value.replaceAll("-", "+").replaceAll("_", "/");
    const decoded = atob(
      normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "="),
    );
    const canonical = btoa(decoded)
      .replaceAll("+", "-")
      .replaceAll("/", "_")
      .replace(/=+$/u, "");
    return canonical === value;
  } catch {
    return false;
  }
}

function validClientOrigin(value: unknown): value is string {
  if (
    !validTrimmedString(value, maximumAccessOriginBytes, false) ||
    !value.startsWith("https://") ||
    value.endsWith("/") ||
    value.includes("\\")
  ) {
    return false;
  }
  try {
    const parsed = new URL(value);
    return (
      parsed.protocol === "https:" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.pathname === "/" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      validCanonicalOriginHost(parsed.hostname)
    );
  } catch {
    return false;
  }
}

function validProviderOrigin(value: unknown): value is string {
  if (
    !validTrimmedString(value, maximumAccessOriginBytes, false) ||
    value.endsWith("/") ||
    value.includes("\\") ||
    value.includes("%")
  ) {
    return false;
  }
  const match = /^(https?):\/\/([^/]+)(\/.*)?$/u.exec(value);
  const authority = match?.[2];
  if (authority === undefined || authority !== authority.toLowerCase()) {
    return false;
  }
  const rawPath = match?.[3] ?? "";
  if (
    rawPath.includes("//") ||
    rawPath.split("/").some((segment) => segment === "." || segment === "..")
  ) {
    return false;
  }
  try {
    const parsed = new URL(value);
    if (
      parsed.username !== "" ||
      parsed.password !== "" ||
      parsed.search !== "" ||
      parsed.hash !== "" ||
      !validCanonicalOriginHost(parsed.hostname)
    ) {
      return false;
    }
    if (parsed.protocol === "https:") {
      return true;
    }
    return parsed.protocol === "http:" && validLoopbackLiteral(authority);
  } catch {
    return false;
  }
}

function validCanonicalOriginHost(hostname: string): boolean {
  const host =
    hostname.startsWith("[") && hostname.endsWith("]")
      ? hostname.slice(1, -1)
      : hostname;
  if (host === "" || host !== host.toLowerCase() || host.endsWith(".")) {
    return false;
  }
  const ipv4Parts = host.split(".");
  if (ipv4Parts.length === 4 && ipv4Parts.every((part) => /^\d+$/u.test(part))) {
    return ipv4Parts.every(
      (part) =>
        /^(?:0|[1-9]\d{0,2})$/u.test(part) && Number(part) <= 255,
    );
  }
  if (host.includes(":")) {
    return /^[a-f0-9:.]+$/u.test(host);
  }
  return (
    host.length <= 253 &&
    host.split(".").every(
      (label) =>
        label.length > 0 &&
        label.length <= 63 &&
        /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/u.test(label),
    )
  );
}

function validLoopbackLiteral(authority: string): boolean {
  const rawHost = authority.startsWith("[")
    ? authority.slice(0, authority.indexOf("]") + 1)
    : authority.split(":", 1)[0]!;
  if (rawHost === "[::1]") {
    return true;
  }
  const parts = rawHost.split(".");
  return (
    parts.length === 4 &&
    parts[0] === "127" &&
    parts.every(
      (part) =>
        /^(?:0|[1-9]\d{0,2})$/u.test(part) && Number(part) <= 255,
    )
  );
}

function validTargetOrigin(value: unknown): value is string {
  if (typeof value !== "string" || !/^https?:\/\/[^\s/?#]+$/u.test(value)) {
    return false;
  }
  try {
    const parsed = new URL(value);
    return (
      (parsed.protocol === "https:" || parsed.protocol === "http:") &&
      parsed.hostname.length > 0 &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.pathname === "/" &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

const captureRunStates = new Set([
  "created",
  "attached",
  "finished",
  "expired",
  "revoked",
]);
const captureRunRecognitions = new Set([
  "unknown",
  "unverified",
  "recognized",
  "verified",
]);
const captureRunAdapterStates = new Set(["verified", "generic", "failed"]);

function validManualCaptureStateTag(value: unknown): value is string {
  return typeof value === "string" && /^"mc_[A-Za-z0-9_-]{43}"$/u.test(value);
}

function requireManualCaptureHeaders(
  response: Response,
  requireStateTag: boolean,
): string | undefined {
  if (response.headers.get("Cache-Control") !== "no-store") {
    throw new ControlContractError();
  }
  const stateTag = response.headers.get("ETag") ?? undefined;
  if (
    (requireStateTag && !validManualCaptureStateTag(stateTag)) ||
    (!requireStateTag && stateTag !== undefined)
  ) {
    throw new ControlContractError();
  }
  return stateTag;
}

function validManualCaptureProxyAddress(value: unknown): value is string {
  if (!validTrimmedString(value, maximumAccessOriginBytes, false)) {
    return false;
  }
  try {
    const parsed = new URL(value);
    return (
      parsed.protocol === "http:" &&
      parsed.hostname === "127.0.0.1" &&
      parsed.port !== "" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.pathname === "/" &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

function validManualCaptureRoot(value: unknown): boolean {
  return (
    hasClosedFields(value, ["kind", "derSha256", "fingerprint", "pemPath"]) &&
    value.kind === "local_path" &&
    typeof value.derSha256 === "string" &&
    /^[a-f0-9]{64}$/u.test(value.derSha256) &&
    validTrimmedString(value.fingerprint, 128, false) &&
    validManualCaptureLocalPath(value.pemPath)
  );
}

function validManualCaptureLocalPath(value: unknown): value is string {
  if (!validTrimmedString(value, 4_096, false)) {
    return false;
  }
  const normalized = value.replaceAll("\\", "/");
  let relative: string;
  if (/^[A-Za-z]:\//u.test(normalized)) {
    relative = normalized.slice(3);
  } else if (/^\/[^/]/u.test(normalized)) {
    relative = normalized.slice(1);
  } else {
    const unc = /^\/\/[^/]+\/[^/]+\/(.+)$/u.exec(normalized);
    if (unc?.[1] === undefined) {
      return false;
    }
    relative = unc[1];
  }
  return (
    relative.length > 0 &&
    !relative.endsWith("/") &&
    relative.split("/").every((segment) =>
      segment.length > 0 && segment !== "." && segment !== ".."
    )
  );
}

function validManualCaptureConfirmationToken(value: unknown): value is string {
  return typeof value === "string" && /^ctx_[A-Za-z0-9_-]{43}$/u.test(value);
}

function requireManualCaptureContext(value: unknown): ManualCaptureContext {
  if (
    !hasClosedFields(value, [
      "confirmationToken",
      "proxyAddress",
      "root",
      "defaultTemporarySeconds",
      "maxTemporarySeconds",
    ]) ||
    !validManualCaptureConfirmationToken(value.confirmationToken) ||
    !validManualCaptureProxyAddress(value.proxyAddress) ||
    !validManualCaptureRoot(value.root) ||
    !positiveInteger(value.defaultTemporarySeconds) ||
    !positiveInteger(value.maxTemporarySeconds) ||
    value.defaultTemporarySeconds < 60 ||
    value.defaultTemporarySeconds > value.maxTemporarySeconds ||
    value.maxTemporarySeconds > 7 * 24 * 60 * 60
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ManualCaptureContext;
}

function validManualCaptureCreateInput(
  value: ManualCaptureCreateInput,
): boolean {
  if (
    !hasClosedFields(
      value,
      ["displayName", "clientClass", "lifetime", "confirmationToken"],
      ["expiresInSeconds"],
    ) ||
    !validTrimmedString(value.displayName, 128, false) ||
    !["cli", "desktop_app", "other"].includes(value.clientClass) ||
    !["temporary", "until_revoked"].includes(value.lifetime) ||
    !validManualCaptureConfirmationToken(value.confirmationToken)
  ) {
    return false;
  }
  return value.lifetime === "temporary"
    ? positiveInteger(value.expiresInSeconds) &&
        value.expiresInSeconds >= 60 &&
        value.expiresInSeconds <= 7 * 24 * 60 * 60
    : value.expiresInSeconds === undefined;
}

function validManualCaptureRecord(
  value: unknown,
  expectedId?: string,
): value is ManualCaptureRecord {
  if (
    !hasClosedFields(
      value,
      [
        "id",
        "ingressProfileId",
        "displayName",
        "clientClass",
        "lifetime",
        "state",
        "observation",
        "createdAt",
        "updatedAt",
      ],
      ["expiresAt", "lastObservedAt"],
    ) ||
    !validResourceId(value.id) ||
    (expectedId !== undefined && value.id !== expectedId) ||
    value.ingressProfileId !== `manual-capture/${value.id}` ||
    !validTrimmedString(value.displayName, 128, false) ||
    !["cli", "desktop_app", "other"].includes(String(value.clientClass)) ||
    !["temporary", "until_revoked"].includes(String(value.lifetime)) ||
    !["active", "revoked", "expired"].includes(String(value.state)) ||
    !["waiting_for_traffic", "observed"].includes(String(value.observation)) ||
    !validTimestamp(value.createdAt) ||
    !validTimestamp(value.updatedAt) ||
    (value.expiresAt !== undefined && !validTimestamp(value.expiresAt)) ||
    (value.lastObservedAt !== undefined && !validTimestamp(value.lastObservedAt))
  ) {
    return false;
  }
  if (
    (value.lifetime === "temporary") !== (value.expiresAt !== undefined) ||
    (value.observation === "observed") !== (value.lastObservedAt !== undefined)
  ) {
    return false;
  }
  const createdAt = Date.parse(value.createdAt);
  const updatedAt = Date.parse(value.updatedAt);
  const expiresAt =
    value.expiresAt === undefined ? undefined : Date.parse(value.expiresAt);
  const observedAt =
    value.lastObservedAt === undefined
      ? undefined
      : Date.parse(value.lastObservedAt);
  return (
    updatedAt >= createdAt &&
    (expiresAt === undefined || expiresAt >= createdAt) &&
    (observedAt === undefined ||
      (observedAt >= createdAt && observedAt <= updatedAt))
  );
}

function requireManualCaptureRecord(
  value: unknown,
  expectedId?: string,
): ManualCaptureRecord {
  if (!validManualCaptureRecord(value, expectedId)) {
    throw new ControlContractError();
  }
  return value;
}

function requireManualCapturePage(value: unknown): ManualCapturePage {
  if (
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every((item) => validManualCaptureRecord(item)) ||
    new Set(value.items.map((item) => (item as ManualCaptureRecord).id)).size !==
      value.items.length
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ManualCapturePage;
}

function requireManualCaptureGrant(
  value: unknown,
  expectedId?: string,
): ManualCaptureGrant {
  if (
    !hasClosedFields(value, [
      "capture",
      "proxyAddress",
      "proxyUsername",
      "proxyPassword",
      "root",
    ]) ||
    !validManualCaptureRecord(value.capture, expectedId) ||
    value.capture.state !== "active" ||
    !validManualCaptureProxyAddress(value.proxyAddress) ||
    value.proxyUsername !== "capture" ||
    typeof value.proxyPassword !== "string" ||
    !/^manual_[A-Za-z0-9_-]{43}$/u.test(value.proxyPassword) ||
    !validManualCaptureRoot(value.root)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ManualCaptureGrant;
}

function requireCaptureRunPage(value: unknown): CaptureRunPage {
  if (
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every(validCaptureRun)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as CaptureRunPage;
}

function validCaptureRun(value: unknown): boolean {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const run = value as Record<string, unknown>;
  const allowed = new Set([
    "id",
    "executableLabel",
    "cwd",
    "localUserLabel",
    "machineId",
    "workspaceId",
    "workspaceLabel",
    "workspaceEvidence",
    "processId",
    "state",
    "observation",
    "recognition",
    "clientAdapterState",
    "clientRecognition",
    "catalogRevision",
    "clientAdapter",
    "clientAdapterReason",
    "createdAt",
    "expiresAt",
  ]);
  if (Object.keys(run).some((key) => !allowed.has(key))) {
    return false;
  }
  if (
    !nonEmptyString(run.id) ||
    !nonEmptyString(run.executableLabel) ||
    !nonEmptyString(run.cwd) ||
    !captureRunStates.has(String(run.state)) ||
    (run.observation !== "waiting_for_traffic" && run.observation !== "observed") ||
    !captureRunRecognitions.has(String(run.recognition)) ||
    !captureRunAdapterStates.has(String(run.clientAdapterState)) ||
    !captureRunRecognitions.has(String(run.clientRecognition)) ||
    run.recognition !== run.clientRecognition ||
    !positiveInteger(run.catalogRevision) ||
    !validTimestamp(run.createdAt) ||
    !validTimestamp(run.expiresAt) ||
    Date.parse(run.expiresAt as string) < Date.parse(run.createdAt as string) ||
    (run.processId !== undefined && !positiveInteger(run.processId)) ||
    (run.localUserLabel !== undefined &&
      !validDisplayLabel(run.localUserLabel, 128, true)) ||
    (run.clientAdapterReason !== undefined &&
      (typeof run.clientAdapterReason !== "string" ||
        !/^[a-z0-9_]{1,128}$/u.test(run.clientAdapterReason)))
  ) {
    return false;
  }
  const workspaceFields = [
    run.machineId,
    run.workspaceId,
    run.workspaceLabel,
    run.workspaceEvidence,
  ];
  if (
    workspaceFields.some((field) => field !== undefined) &&
    (!validOpaqueIdentity(run.machineId) ||
      !validOpaqueIdentity(run.workspaceId) ||
      !validDisplayLabel(run.workspaceLabel, 120, false) ||
      (run.workspaceEvidence !== "local_launcher" &&
        run.workspaceEvidence !== "registered_companion"))
  ) {
    return false;
  }
  const adapter = validCaptureRunAdapter(run.clientAdapter, run.catalogRevision);
  if (run.clientAdapterState === "verified") {
    return run.clientRecognition === "verified" && adapter;
  }
  return run.clientAdapter === undefined;
}

const workspaceRouteStates = new Set([
  "active",
  "workspace_route_unavailable",
]);
const workspaceRouteAuthPresentations = new Set([
  "vibermate_account",
  "client_oauth",
  "client_auth",
  "none",
]);
const workspaceRouteRunStates = new Set(["active", "idle"]);

function requireWorkspaceRouteBindingPage(
  value: unknown,
): WorkspaceRouteBindingPage {
  if (
    !hasExactFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems ||
    !value.items.every((item) => validWorkspaceRouteBinding(item))
  ) {
    throw new ControlContractError();
  }
  return value as unknown as WorkspaceRouteBindingPage;
}

function requireWorkspaceRouteBinding(
  value: unknown,
  expectedId?: string,
  expectedRevision?: number,
  expectedProfileId?: string,
): WorkspaceRouteBinding {
  if (
    !validWorkspaceRouteBinding(value) ||
    (expectedId !== undefined && value.id !== expectedId) ||
    (expectedRevision !== undefined && value.revision !== expectedRevision) ||
    (expectedProfileId !== undefined && value.profileId !== expectedProfileId)
  ) {
    throw new ControlContractError();
  }
  return value;
}

function validWorkspaceRouteBinding(
  value: unknown,
): value is WorkspaceRouteBinding {
  if (
    !hasExactFields(value, [
      "id",
      "accessId",
      "machineId",
      "machineShortId",
      "machineDisplayName",
      "machineRegistrationRevision",
      "workspaceId",
      "workspaceLabel",
      "workspaceEvidence",
      "profileId",
      "revision",
      "state",
      "activeRunCount",
      "activeRuns",
      "pinnedRequestCount",
      "approvedProfiles",
      "updatedAt",
    ]) ||
    !validOpaqueIdentity(value.id) ||
    !validResourceId(value.accessId) ||
    !validOpaqueIdentity(value.machineId) ||
    value.machineShortId !== value.machineId.slice(0, 10) ||
    !validDisplayLabel(value.machineDisplayName, 256, false) ||
    !positiveInteger(value.machineRegistrationRevision) ||
    !validOpaqueIdentity(value.workspaceId) ||
    !validDisplayLabel(value.workspaceLabel, 120, false) ||
    (value.workspaceEvidence !== "local_launcher" &&
      value.workspaceEvidence !== "registered_companion") ||
    !validResourceId(value.profileId) ||
    !positiveInteger(value.revision) ||
    !workspaceRouteStates.has(String(value.state)) ||
    !nonNegativeInteger(value.activeRunCount) ||
    !Array.isArray(value.activeRuns) ||
    value.activeRuns.length > maximumActivityPageItems ||
    value.activeRunCount !== value.activeRuns.length ||
    !value.activeRuns.every(validWorkspaceRouteRun) ||
    !nonNegativeInteger(value.pinnedRequestCount) ||
    !Array.isArray(value.approvedProfiles) ||
    value.approvedProfiles.length > maximumEndpointProfiles ||
    !value.approvedProfiles.every(validWorkspaceRouteProfile) ||
    !validTimestamp(value.updatedAt)
  ) {
    return false;
  }
  const profileIds = value.approvedProfiles.map(({ profileId }) => profileId);
  if (new Set(profileIds).size !== profileIds.length) {
    return false;
  }
  return (
    value.state === "workspace_route_unavailable" ||
    profileIds.includes(value.profileId)
  );
}

function validWorkspaceRouteRun(value: unknown): boolean {
  if (
    !hasClosedFields(
      value,
      ["runId", "clientLabel", "state", "startedAt", "lastActivityAt"],
      ["localUserLabel"],
    ) ||
    !validRouteIdentity(value.runId) ||
    !validDisplayLabel(value.clientLabel, 256, false) ||
    (value.localUserLabel !== undefined &&
      !validDisplayLabel(value.localUserLabel, 128, true)) ||
    !workspaceRouteRunStates.has(String(value.state)) ||
    !validTimestamp(value.startedAt) ||
    !validTimestamp(value.lastActivityAt)
  ) {
    return false;
  }
  return Date.parse(value.lastActivityAt) >= Date.parse(value.startedAt);
}

function validWorkspaceRouteProfile(value: unknown): boolean {
  return (
    hasExactFields(value, [
      "profileId",
      "kind",
      "label",
      "modelPresentation",
      "authPresentation",
      "authLabel",
      "available",
    ]) &&
    validResourceId(value.profileId) &&
    (value.kind === "original_passthrough" || value.kind === "managed") &&
    validDisplayLabel(value.label, 256, false) &&
    validDisplayLabel(value.modelPresentation, 256, false) &&
    workspaceRouteAuthPresentations.has(String(value.authPresentation)) &&
    validDisplayLabel(value.authLabel, 256, true) &&
    typeof value.available === "boolean"
  );
}

function validOpaqueIdentity(value: unknown): value is string {
  return typeof value === "string" && validCapability(value);
}

function validDisplayLabel(
  value: unknown,
  maximumBytes: number,
  allowEmpty: boolean,
): value is string {
  return (
    typeof value === "string" &&
    (allowEmpty || value.length > 0) &&
    value.trim() === value &&
    new TextEncoder().encode(value).byteLength <= maximumBytes &&
    validUnicodeString(value) &&
    !/[\u0000\r\n]/u.test(value)
  );
}

function validCaptureRunAdapter(
  value: unknown,
  catalogRevision: unknown,
): boolean {
  if (value === undefined) {
    return false;
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const adapter = value as Record<string, unknown>;
  const keys = Object.keys(adapter).sort();
  const expectedKeys = [
    "catalogRevision",
    "id",
    "installShape",
    "launchRecipe",
    "revision",
    "source",
    "version",
  ];
  return (
    keys.length === expectedKeys.length &&
    keys.every((key, index) => key === expectedKeys[index]) &&
    nonEmptyString(adapter.id) &&
    positiveInteger(adapter.revision) &&
    nonEmptyString(adapter.version) &&
    adapter.catalogRevision === catalogRevision &&
    adapter.source === "prelaunch_digest_catalog" &&
    nonEmptyString(adapter.installShape) &&
    nonEmptyString(adapter.launchRecipe)
  );
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function positiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function validTimestamp(value: unknown): value is string {
  if (typeof value !== "string") {
    return false;
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/u.exec(
    value,
  );
  if (match === null) {
    return false;
  }
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const offsetHour = match[7] === undefined ? 0 : Number(match[7]);
  const offsetMinute = match[8] === undefined ? 0 : Number(match[8]);
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > daysInMonth(year, month) ||
    hour > 23 ||
    minute > 59 ||
    second > 59 ||
    offsetHour > 23 ||
    offsetMinute > 59
  ) {
    return false;
  }
  return Number.isFinite(Date.parse(value));
}

function daysInMonth(year: number, month: number): number {
  switch (month) {
    case 2:
      return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
    case 4:
    case 6:
    case 9:
    case 11:
      return 30;
    default:
      return 31;
  }
}

function exactControlContentType(response: Response, expected: string): boolean {
  const value = response.headers.get("Content-Type");
  return value !== null && value.toLowerCase() === expected;
}

function decodeProblem(status: number, payload: string): ControlProblem {
  try {
    const problem = JSON.parse(payload) as Record<string, unknown>;
    const keys = Object.keys(problem).sort();
    const requiredKeys = ["code", "status", "title", "type"];
    const keysAreClosed =
      (keys.length === requiredKeys.length &&
        keys.every((key, index) => key === requiredKeys[index])) ||
      (keys.length === requiredKeys.length + 1 &&
        keys.every(
          (key, index) =>
            key === [...requiredKeys, "operationId"].sort()[index],
        ));
    if (
      keysAreClosed &&
      problem.status === status &&
      nonEmptyString(problem.title) &&
      nonEmptyString(problem.code) &&
      /^[a-z][a-z0-9_]*$/u.test(problem.code) &&
      problem.type ===
        `urn:vibermate:error:${problem.code.replaceAll("_", "-")}` &&
      (problem.operationId === undefined || nonEmptyString(problem.operationId))
    ) {
      return new ControlProblem(status, problem.code, `error.${problem.code}`);
    }
  } catch {
    // The stable fallback below deliberately excludes the response payload.
  }
  throw new ControlContractError();
}

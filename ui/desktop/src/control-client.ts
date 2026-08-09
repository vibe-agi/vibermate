import type {
  ActivityQuery,
  ActivityPage,
  ActivityStatus,
  ApprovalPage,
  ApprovalChoice,
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
  ExchangeReadOptions,
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
  environments(signal?: AbortSignal): Promise<EnvironmentPage>;
  providerAccounts(signal?: AbortSignal): Promise<ProviderAccountPage>;
  providerAccount(accountId: string, signal?: AbortSignal): Promise<ProviderAccountRecord>;
  createProviderAccount(input: ProviderAccountCreateInput, signal?: AbortSignal): Promise<ProviderAccountRecord>;
  replaceProviderAccountCredential(accountId: string, expectedCredentialEpoch: number, input: ProviderAccountCredentialInput, signal?: AbortSignal): Promise<ProviderAccountRecord>;
  deleteProviderAccount(accountId: string, expectedCredentialEpoch: number, signal?: AbortSignal): Promise<ProviderAccountDeleteResult>;
  environment(environmentId: string, signal?: AbortSignal): Promise<EnvironmentRecord>;
  environmentRevision(environmentId: string, revision: number, signal?: AbortSignal): Promise<EnvironmentRecord>;
  environmentDraft(environmentId: string, signal?: AbortSignal): Promise<EnvironmentDraft>;
  saveEnvironmentDraft(environmentId: string, expectedBaseRevision: number, input: EnvironmentDraftInput, signal?: AbortSignal): Promise<EnvironmentDraft>;
  previewEnvironmentDraft(environmentId: string, draftRevision: number, signal?: AbortSignal): Promise<EnvironmentImpact>;
  publishEnvironmentDraft(environmentId: string, draftRevision: number, signal?: AbortSignal): Promise<EnvironmentPublishResult>;
  captures(signal?: AbortSignal): Promise<CapturePage>;
  capture(captureKey: string, signal?: AbortSignal): Promise<CaptureRecord>;
  captureAssignment(captureKey: string, signal?: AbortSignal): Promise<CaptureAssignment>;
  switchCaptureEnvironment(captureKey: string, expectedRevision: number, environmentId: string, signal?: AbortSignal): Promise<CaptureAssignmentSwitchResult>;
  workspaceEnvironmentDefault(machineId: string, workspaceId: string, signal?: AbortSignal): Promise<WorkspaceEnvironmentDefault | undefined>;
  setWorkspaceEnvironmentDefault(machineId: string, workspaceId: string, expectedRevision: number, environmentId: string, signal?: AbortSignal): Promise<WorkspaceEnvironmentDefault>;
  clearWorkspaceEnvironmentDefault(machineId: string, workspaceId: string, expectedRevision: number, signal?: AbortSignal): Promise<void>;
  activities(query?: ActivityQuery, signal?: AbortSignal): Promise<ActivityPage>;
  exchange(exchangeId: string, options?: ExchangeReadOptions): Promise<ExchangeDetail>;
  approvals(signal?: AbortSignal): Promise<ApprovalPage>;
  manualCaptureContext(environmentId: string, signal?: AbortSignal): Promise<ManualCaptureContext>;
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
  connections(signal?: AbortSignal): Promise<ConnectionPage>;
  egressAttempts(signal?: AbortSignal): Promise<EgressAttemptPage>;
  connectionRules(signal?: AbortSignal): Promise<ConnectionRuleSet>;
  replaceConnectionRules(expectedRevision: number, input: ConnectionRuleSetInput, signal?: AbortSignal): Promise<ConnectionRuleSet>;
  decideApproval(
    approval: ApprovalView,
    choice: ApprovalChoice,
    signal?: AbortSignal,
  ): Promise<ApprovalView>;
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
    environments: async (signal) =>
      requireEnvironmentPage(
        await requestRead<unknown>("/api/v1/environments", signal),
      ),
    providerAccounts: async (signal) =>
      requireProviderAccountPage(
        await requestRead<unknown>("/api/v1/provider-accounts", signal),
      ),
    providerAccount: async (accountId, signal) => {
      requireResourceId(accountId);
      return requireProviderAccountRecord(
        await requestRead<unknown>(
          `/api/v1/provider-accounts/${encodeURIComponent(accountId)}`,
          signal,
        ),
        accountId,
      );
    },
    createProviderAccount: async (input, signal) => {
      if (!validProviderAccountCreateInput(input)) throw new ControlContractError();
      return requestMutation<ProviderAccountRecord>(
        "POST",
        "/api/v1/provider-accounts",
        input,
        0,
        signal,
        requireProviderAccountRecord,
        201,
      );
    },
    replaceProviderAccountCredential: async (accountId, expectedCredentialEpoch, input, signal) => {
      requireResourceId(accountId);
      if (!nonNegativeInteger(expectedCredentialEpoch) || !validProviderAccountCredentialInput(input)) {
        throw new ControlContractError();
      }
      return requestMutation<ProviderAccountRecord>(
        "PUT",
        `/api/v1/provider-accounts/${encodeURIComponent(accountId)}/credential`,
        input,
        expectedCredentialEpoch,
        signal,
        (value) => requireProviderAccountRecord(value, accountId),
      );
    },
    deleteProviderAccount: async (accountId, expectedCredentialEpoch, signal) => {
      requireResourceId(accountId);
      if (!nonNegativeInteger(expectedCredentialEpoch)) throw new ControlContractError();
      return requestMutation<ProviderAccountDeleteResult>(
        "DELETE",
        `/api/v1/provider-accounts/${encodeURIComponent(accountId)}`,
        undefined,
        expectedCredentialEpoch,
        signal,
        requireProviderAccountDeleteResult,
      );
    },
    environment: async (environmentId, signal) => {
      requireResourceId(environmentId);
      return requireEnvironmentRecord(
        await requestRead<unknown>(
          `/api/v1/environments/${encodeURIComponent(environmentId)}`,
          signal,
        ),
        environmentId,
      );
    },
    environmentRevision: async (environmentId, revision, signal) => {
      requireResourceId(environmentId);
      if (!positiveInteger(revision)) throw new ControlContractError();
      return requireEnvironmentRecord(
        await requestRead<unknown>(
          `/api/v1/environments/${encodeURIComponent(environmentId)}/revisions/${revision}`,
          signal,
        ),
        environmentId,
        revision,
      );
    },
    environmentDraft: async (environmentId, signal) => {
      requireResourceId(environmentId);
      return requireEnvironmentDraft(
        await requestRead<unknown>(
          `/api/v1/environments/${encodeURIComponent(environmentId)}/draft`,
          signal,
        ),
        environmentId,
      );
    },
    saveEnvironmentDraft: async (environmentId, expectedBaseRevision, input, signal) => {
      requireResourceId(environmentId);
      if (!nonNegativeInteger(expectedBaseRevision) || !validEnvironmentDraftInput(input)) {
        throw new ControlContractError();
      }
      return requestMutation<EnvironmentDraft>(
        "PUT",
        `/api/v1/environments/${encodeURIComponent(environmentId)}/draft`,
        input,
        expectedBaseRevision,
        signal,
        (value) => requireEnvironmentDraft(value, environmentId),
      );
    },
    previewEnvironmentDraft: async (environmentId, draftRevision, signal) => {
      requireResourceId(environmentId);
      if (!positiveInteger(draftRevision)) throw new ControlContractError();
      return requestMutation<EnvironmentImpact>(
        "POST",
        `/api/v1/environments/${encodeURIComponent(environmentId)}/draft/actions/preview`,
        undefined,
        draftRevision,
        signal,
        (value) => requireEnvironmentImpact(value, environmentId, draftRevision),
      );
    },
    publishEnvironmentDraft: async (environmentId, draftRevision, signal) => {
      requireResourceId(environmentId);
      if (!positiveInteger(draftRevision)) throw new ControlContractError();
      return requestMutation<EnvironmentPublishResult>(
        "POST",
        `/api/v1/environments/${encodeURIComponent(environmentId)}/draft/actions/publish`,
        undefined,
        draftRevision,
        signal,
        (value) => requireEnvironmentPublishResult(value, environmentId, draftRevision),
      );
    },
    captures: async (signal) =>
      requireCapturePage(
        await requestRead<unknown>("/api/v1/captures?limit=50", signal),
      ),
    capture: async (captureKey, signal) => {
      requireCaptureKey(captureKey);
      return requireCaptureRecord(
        await requestRead<unknown>(
          `/api/v1/captures/${encodeURIComponent(captureKey)}`,
          signal,
        ),
        captureKey,
      );
    },
    captureAssignment: async (captureKey, signal) => {
      requireCaptureKey(captureKey);
      return requireCaptureAssignment(
        await requestRead<unknown>(
          `/api/v1/captures/${encodeURIComponent(captureKey)}/environment-assignment`,
          signal,
        ),
        captureKey,
      );
    },
    switchCaptureEnvironment: async (captureKey, expectedRevision, environmentId, signal) => {
      requireCaptureKey(captureKey);
      requireResourceId(environmentId);
      if (!positiveInteger(expectedRevision)) throw new ControlContractError();
      return requestMutation<CaptureAssignmentSwitchResult>(
        "PATCH",
        `/api/v1/captures/${encodeURIComponent(captureKey)}/environment-assignment`,
        { environmentId },
        expectedRevision,
        signal,
        (value) => requireCaptureAssignmentSwitch(value, captureKey, expectedRevision),
      );
    },
    workspaceEnvironmentDefault: async (machineId, workspaceId, signal) => {
      requireResourceId(machineId);
      requireResourceId(workspaceId);
      try {
        return requireWorkspaceEnvironmentDefault(
          await requestRead<unknown>(workspaceDefaultPath(machineId, workspaceId), signal),
          machineId,
          workspaceId,
        );
      } catch (error) {
        if (error instanceof ControlProblem && error.reasonCode === "workspace_environment_default_not_found") {
          return undefined;
        }
        throw error;
      }
    },
    setWorkspaceEnvironmentDefault: async (machineId, workspaceId, expectedRevision, environmentId, signal) => {
      requireResourceId(machineId);
      requireResourceId(workspaceId);
      requireResourceId(environmentId);
      if (!nonNegativeInteger(expectedRevision)) throw new ControlContractError();
      return requestMutation<WorkspaceEnvironmentDefault>(
        "PUT",
        workspaceDefaultPath(machineId, workspaceId),
        { environmentId },
        expectedRevision,
        signal,
        (value) => requireWorkspaceEnvironmentDefault(value, machineId, workspaceId, environmentId),
      );
    },
    clearWorkspaceEnvironmentDefault: async (machineId, workspaceId, expectedRevision, signal) => {
      requireResourceId(machineId);
      requireResourceId(workspaceId);
      if (!positiveInteger(expectedRevision)) throw new ControlContractError();
      await requestMutation<void>(
        "DELETE",
        workspaceDefaultPath(machineId, workspaceId),
        undefined,
        expectedRevision,
        signal,
        (value) => {
          if (value !== undefined) throw new ControlContractError();
        },
        204,
      );
    },
    activities: async (options, signal) => {
      if (!validActivityQuery(options)) throw new ControlContractError();
      const query = new URLSearchParams({ kind: "exchange", limit: "50" });
      if (options?.cursor !== undefined) query.set("cursor", options.cursor);
      if (options?.captureRunId !== undefined) query.set("captureRunId", options.captureRunId);
      if (options?.environmentId !== undefined) query.set("environmentId", options.environmentId);
      return requireActivityPage(
        await requestRead<unknown>(`/api/v1/activities?${query.toString()}`, signal),
      );
    },
    exchange: async (exchangeId, options) => {
      if (!validRouteIdentity(exchangeId)) {
        throw new ControlContractError();
      }
      if (options !== undefined &&
        (typeof options !== "object" ||
          (options.contentView !== undefined && options.contentView !== "incremental" && options.contentView !== "full"))) {
        throw new ControlContractError();
      }
      const contentView = options?.contentView ?? "incremental";
      return requireExchangeDetail(
        await requestRead<unknown>(
          `/api/v1/exchanges/${encodeURIComponent(exchangeId)}?contentView=${contentView}`,
          options?.signal,
        ),
        exchangeId,
        contentView,
      );
    },
    approvals: async (signal) =>
      requireApprovalPage(
        await requestRead<unknown>(
          "/api/v1/approvals?state=pending&limit=50",
          signal,
        ),
      ),
    manualCaptureContext: async (environmentId, signal) => {
      requireResourceId(environmentId);
      return (
      (
        await requestManualRead(
          `/api/v1/manual-captures/context?environmentId=${encodeURIComponent(environmentId)}`,
          signal,
          (value) => requireManualCaptureContext(value, environmentId),
          false,
        )
      ).value
      );
    },
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
    connections: async (signal) =>
      requireConnectionPage(
        await requestRead<unknown>("/api/v1/connections?limit=50", signal),
      ),
    egressAttempts: async (signal) =>
      requireEgressAttemptPage(
        await requestRead<unknown>("/api/v1/egress-attempts?limit=50", signal),
      ),
    connectionRules: async (signal) =>
      requireConnectionRuleSet(
        await requestRead<unknown>("/api/v1/policies/connections", signal),
      ),
    replaceConnectionRules: async (expectedRevision, input, signal) => {
      if (!positiveInteger(expectedRevision) || !validConnectionRuleSetInput(input)) {
        throw new ControlContractError();
      }
      return requestMutation<ConnectionRuleSet>(
        "PATCH",
        "/api/v1/policies/connections",
        input,
        expectedRevision,
        signal,
        (value) => requireConnectionRuleSet(value, expectedRevision + 1),
      );
    },
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
  };
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
const maximumHostBytes = 1_024;
const maximumCollectionItems = 256;
const maximumUint32 = 0xffff_ffff;

const runtimeStates = new Set([
  "starting",
  "initialized",
  "degraded",
  "stopping",
  "stopped",
  "stop_failed",
]);
const storageStates = new Set(["healthy", "unavailable"]);
const environmentProjectionStates = new Set([
  "unrestored",
  "healthy",
  "unavailable",
]);
const offlineHoldStates = new Set([
  "unbound",
  "online",
  "entering",
  "held",
  "probing",
  "releasing",
  "stopping",
]);
const offlineProbeReasons = new Set([
  "startup",
  "operator_resume",
  "operator_probe",
]);

function requireStatusResponse(
  value: unknown,
  expectedInstanceId: string,
): StatusResponse {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "generation",
      "ready",
      "apiVersion",
      "statusKey",
      "runtime",
    ]) ||
    !validIdentity(value.generation) ||
    typeof value.ready !== "boolean" ||
    value.apiVersion !== "v1" ||
    !validIdentity(value.statusKey) ||
    !validRuntimeStatus(value.runtime, expectedInstanceId)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as StatusResponse;
}

function validRuntimeStatus(
  value: unknown,
  expectedInstanceId: string,
): value is Record<string, unknown> {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "state",
      "instanceId",
      "host",
      "schemaRevision",
      "storage",
      "environmentProjection",
      "offlineHold",
      "startedAt",
    ], ["stoppedAt", "stopReasonCode"]) ||
    !runtimeStates.has(String(value.state)) ||
    value.instanceId !== expectedInstanceId ||
    value.host !== "desktop" ||
    !nonNegativeInteger(value.schemaRevision) ||
    !storageStates.has(String(value.storage)) ||
    !validEnvironmentProjection(value.environmentProjection) ||
    !validOfflineHoldSnapshot(value.offlineHold) ||
    !validTimestamp(value.startedAt) ||
    (value.stoppedAt !== undefined && !validTimestamp(value.stoppedAt)) ||
    (value.stopReasonCode !== undefined && !validIdentity(value.stopReasonCode))
  ) {
    return false;
  }
  const projection = value.environmentProjection as Record<string, unknown>;
  if (
    value.state === "initialized" &&
    (value.storage !== "healthy" || projection.state !== "healthy")
  ) {
    return false;
  }
  if (
    value.state === "degraded" &&
    value.storage !== "unavailable" &&
    projection.state !== "unavailable"
  ) {
    return false;
  }
  return true;
}

function validEnvironmentProjection(value: unknown): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["state", "unavailableEnvironments"]) ||
    !environmentProjectionStates.has(String(value.state)) ||
    !(
      value.unavailableEnvironments === null ||
      validUniqueResourceIds(value.unavailableEnvironments, maximumCollectionItems, true)
    )
  ) {
    return false;
  }
  const unavailable = value.unavailableEnvironments;
  return value.state === "unavailable"
    ? Array.isArray(unavailable) && unavailable.length > 0
    : unavailable === null || unavailable.length === 0;
}

function requireOfflineHoldSnapshot(
  value: unknown,
  previousRevision?: number,
): OfflineHoldSnapshot {
  if (
    !validOfflineHoldSnapshot(value) ||
    (previousRevision !== undefined && value.revision < previousRevision)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as OfflineHoldSnapshot;
}

function validOfflineHoldSnapshot(
  value: unknown,
): value is Record<string, unknown> & { revision: number } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
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
    ], ["lastProbeReason"]) ||
    !offlineHoldStates.has(String(value.state)) ||
    !nonNegativeInteger(value.revision) ||
    !validTimestamp(value.since) ||
    !nonNegativeInteger(value.activeActions) ||
    !nonNegativeInteger(value.enteringActions) ||
    !nonNegativeInteger(value.activeEgress) ||
    !nonNegativeInteger(value.queuedRequests) ||
    !Number.isSafeInteger(value.heldBytes) ||
    Number(value.heldBytes) < 0 ||
    typeof value.safeToDisconnect !== "boolean" ||
    !validCountRecord(value.activeByKind) ||
    !validCountRecord(value.queuedByKind) ||
    (value.lastProbeReason !== undefined &&
      !offlineProbeReasons.has(String(value.lastProbeReason)))
  ) {
    return false;
  }
  const activeTotal = sumSafeIntegers(Object.values(value.activeByKind));
  const queuedTotal = sumSafeIntegers(Object.values(value.queuedByKind));
  return activeTotal === value.activeEgress && queuedTotal === value.queuedRequests;
}

function validCountRecord(value: unknown): value is Record<string, number> {
  return (
    isRecord(value) &&
    Object.keys(value).length <= maximumCollectionItems &&
    Object.entries(value).every(
      ([key, count]) => validIdentity(key) && nonNegativeInteger(count),
    )
  );
}

function sumSafeIntegers(values: readonly number[]): number | undefined {
  let total = 0;
  for (const value of values) {
    total += value;
    if (!Number.isSafeInteger(total)) return undefined;
  }
  return total;
}

function requireEnvironmentPage(value: unknown): EnvironmentPage {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems
  ) {
    throw new ControlContractError();
  }
  let previous: (Record<string, unknown> & { id: string }) | undefined;
  for (const item of value.items) {
    if (
      !validEnvironmentRecord(item, false) ||
      (previous !== undefined && compareEnvironmentDirectoryRecords(previous, item) >= 0)
    ) {
      throw new ControlContractError();
    }
    previous = item;
  }
  return value as unknown as EnvironmentPage;
}

// Core owns system_transparent and deliberately keeps it at the top of the
// Environment directory. User-owned Environments are bytewise-ID ordered
// within the second partition. This is the wire order produced by the runtime,
// not a presentation-only reorder performed after validation.
function compareEnvironmentDirectoryRecords(
  left: Record<string, unknown> & { id: string },
  right: Record<string, unknown> & { id: string },
): number {
  if (left.systemOwned !== right.systemOwned) {
    return left.systemOwned === true ? -1 : 1;
  }
  return compareResourceIds(left.id, right.id);
}

const providerAccountKinds = new Set([
  "anthropic_api_key",
  "claude_oauth_token",
  "openai_api_key",
]);
const providerCredentialStates = new Set([
  "ready",
  "disabled",
  "credential_missing",
  "credential_unavailable",
]);

function requireProviderAccountPage(value: unknown): ProviderAccountPage {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["items"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumDashboardPageItems
  ) {
    throw new ControlContractError();
  }
  let previous = "";
  for (const item of value.items) {
    if (
      !validProviderAccountRecord(item) ||
      (previous !== "" && compareResourceIds(previous, item.id) >= 0)
    ) {
      throw new ControlContractError();
    }
    previous = item.id;
  }
  return value as unknown as ProviderAccountPage;
}

function requireProviderAccountRecord(
  value: unknown,
  expectedId?: string,
): ProviderAccountRecord {
  if (
    !validProviderAccountRecord(value) ||
    (expectedId !== undefined && value.id !== expectedId)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as ProviderAccountRecord;
}

function validProviderAccountRecord(
  value: unknown,
): value is Record<string, unknown> & { id: string } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "id",
      "displayName",
      "kind",
      "realmId",
      "state",
      "revision",
      "credentialState",
      "credentialEpoch",
    ]) ||
    !validResourceId(value.id) ||
    !validDisplayLabel(value.displayName, 256, false) ||
    !providerAccountKinds.has(String(value.kind)) ||
    !validResourceId(value.realmId) ||
    (value.state !== "active" && value.state !== "disabled") ||
    !positiveInteger(value.revision) ||
    !providerCredentialStates.has(String(value.credentialState)) ||
    !nonNegativeInteger(value.credentialEpoch)
  ) {
    return false;
  }
  return value.credentialState === "ready"
    ? positiveInteger(value.credentialEpoch)
    : value.credentialState === "credential_unavailable"
      ? true
      : value.credentialEpoch === 0;
}

function validProviderAccountCreateInput(
  value: ProviderAccountCreateInput,
): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["id", "displayName", "kind", "secret"]) &&
    validResourceId(value.id) &&
    validDisplayLabel(value.displayName, 256, false) &&
    providerAccountKinds.has(value.kind) &&
    validSecretInput(value.secret)
  );
}

function validProviderAccountCredentialInput(
  value: ProviderAccountCredentialInput,
): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["secret"]) &&
    validSecretInput(value.secret)
  );
}

function requireProviderAccountDeleteResult(
  value: unknown,
): ProviderAccountDeleteResult {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["deleted", "referenceCount", "references"]) ||
    typeof value.deleted !== "boolean" ||
    !nonNegativeInteger(value.referenceCount) ||
    !Array.isArray(value.references) ||
    value.references.length > maximumDashboardPageItems ||
    value.referenceCount < value.references.length ||
    (value.deleted !== (value.referenceCount === 0))
  ) {
    throw new ControlContractError();
  }
  let previous = "";
  for (const reference of value.references) {
    if (
      !isRecord(reference) ||
      !hasClosedFields(reference, [
        "environmentId",
        "environmentName",
        "environmentRevision",
        "routeId",
        "routeRevision",
      ]) ||
      !validResourceId(reference.environmentId) ||
      !validDisplayLabel(reference.environmentName, 256, false) ||
      !positiveInteger(reference.environmentRevision) ||
      !validResourceId(reference.routeId) ||
      !positiveInteger(reference.routeRevision)
    ) {
      throw new ControlContractError();
    }
    const key = `${reference.environmentId}\u0000${reference.routeId}`;
    if (previous !== "" && previous >= key) throw new ControlContractError();
    previous = key;
  }
  return value as unknown as ProviderAccountDeleteResult;
}

function validSecretInput(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    new TextEncoder().encode(value).length <= 64 * 1024 &&
    !/[\0\r\n]/u.test(value)
  );
}

function requireEnvironmentRecord(
  value: unknown,
  expectedId?: string,
  expectedRevision?: number,
): EnvironmentRecord {
  if (
    !validEnvironmentRecord(value, false) ||
    (expectedId !== undefined && value.id !== expectedId) ||
    (expectedRevision !== undefined && value.revision !== expectedRevision)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as EnvironmentRecord;
}

function validEnvironmentRecord(
  value: unknown,
  allowUncommitted: boolean,
): value is Record<string, unknown> & { id: string; revision: number } {
  return (
    isRecord(value) &&
    hasClosedFields(value, [
      "id",
      "name",
      "state",
      "revision",
      "digest",
      "systemOwned",
      "clientEndpoints",
      "pluginBindings",
      "budgetPolicy",
      "egressPolicy",
      "contentRecording",
      "policySet",
    ]) &&
    validResourceId(value.id) &&
    value.systemOwned === (value.id === "system_transparent") &&
    validDisplayLabel(value.name, 256, false) &&
    (value.state === "active" || value.state === "disabled") &&
    (allowUncommitted ? nonNegativeInteger(value.revision) : positiveInteger(value.revision)) &&
    validDigest(value.digest) &&
    typeof value.systemOwned === "boolean" &&
    validEnvironmentEndpoints(value.clientEndpoints) &&
    validPluginBindings(value.pluginBindings) &&
    validSimplePolicy(value.budgetPolicy, false) &&
    validSimplePolicy(value.egressPolicy, true) &&
    validContentRecordingPolicy(value.contentRecording) &&
    validEnvironmentPolicySet(value.policySet)
  );
}

function validContentRecordingPolicy(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["mode", "retentionDays"]) &&
    (value.mode === "full" || value.mode === "metadata_only" || value.mode === "off") &&
    nonNegativeInteger(value.retentionDays) &&
    (value.mode === "off"
      ? value.retentionDays === 0
      : value.retentionDays >= 1 && value.retentionDays <= 3650)
  );
}

function validEnvironmentPolicySet(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["toolMode"]) &&
    (value.toolMode === "observe" ||
      value.toolMode === "review" ||
      value.toolMode === "strict")
  );
}

function validEnvironmentEndpoints(value: unknown): boolean {
  if (!Array.isArray(value) || value.length > maximumCollectionItems) return false;
  const ids = new Set<string>();
  const origins = new Set<string>();
  return value.every((endpoint) => {
    if (
      !isRecord(endpoint) ||
      !hasClosedFields(endpoint, ["id", "revision", "clientOrigin", "protocolPlans"]) ||
      !validResourceId(endpoint.id) ||
      !positiveInteger(endpoint.revision) ||
      !validClientOrigin(endpoint.clientOrigin) ||
      !Array.isArray(endpoint.protocolPlans) ||
      endpoint.protocolPlans.length === 0 ||
      endpoint.protocolPlans.length > 16 ||
      ids.has(endpoint.id) ||
      origins.has(endpoint.clientOrigin)
    ) return false;
    ids.add(endpoint.id);
    origins.add(endpoint.clientOrigin);
    const planIds = new Set<string>();
    return endpoint.protocolPlans.every((plan) => {
      if (!validProtocolPlan(plan) || planIds.has(plan.id)) return false;
      planIds.add(plan.id);
      return true;
    });
  });
}

function validProtocolPlan(
  value: unknown,
): value is Record<string, unknown> & { id: string } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "id",
      "revision",
      "clientProtocol",
      "clientAdapterPolicy",
      "mode",
      "upstreamPlan",
      "pluginBindings",
    ]) ||
    !validResourceId(value.id) ||
    !positiveInteger(value.revision) ||
    !new Set(["anthropic_messages", "openai_responses", "openai_chat"]).has(String(value.clientProtocol)) ||
    !validSimplePolicy(value.clientAdapterPolicy, false) ||
    !new Set(["original_passthrough", "managed"]).has(String(value.mode)) ||
    !validPluginBindings(value.pluginBindings)
  ) return false;
  return validUpstreamPlan(value.upstreamPlan, String(value.mode));
}

function validUpstreamPlan(value: unknown, mode: string): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["routes", "defaultRouteId", "routeSet"]) ||
    !Array.isArray(value.routes) ||
    value.routes.length === 0 ||
    value.routes.length > 64 ||
    !validResourceId(value.defaultRouteId) ||
    !isRecord(value.routeSet) ||
    !hasClosedFields(value.routeSet, ["id", "revision", "candidateRouteIds"]) ||
    !validResourceId(value.routeSet.id) ||
    !positiveInteger(value.routeSet.revision) ||
    !validUniqueResourceIds(value.routeSet.candidateRouteIds, 64, false)
  ) return false;
  const routeIds = new Set<string>();
  for (const route of value.routes) {
    if (!validUpstreamRoute(route, mode) || routeIds.has(route.id)) return false;
    routeIds.add(route.id);
  }
  return (
    routeIds.has(String(value.defaultRouteId)) &&
    value.routeSet.candidateRouteIds.every((id) => routeIds.has(id))
  );
}

function validUpstreamRoute(
  value: unknown,
  planMode: string,
): value is Record<string, unknown> & { id: string } {
  return (
    isRecord(value) &&
    hasClosedFields(value, [
      "id",
      "revision",
      "providerTarget",
      "backendProtocol",
      "accountPolicy",
      "modelPolicy",
      "wireProfileRef",
      "pluginBindings",
    ]) &&
    validResourceId(value.id) &&
    positiveInteger(value.revision) &&
    validProviderTarget(value.providerTarget) &&
    validIdentity(value.backendProtocol) &&
    validRouteAccountPolicy(value.accountPolicy, planMode) &&
    validModelPolicy(value.modelPolicy) &&
    validTrimmedString(value.wireProfileRef, 256, true) &&
    validPluginBindings(value.pluginBindings)
  );
}

function validProviderTarget(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["id", "revision", "origin", "realmId", "capabilities"]) &&
    validResourceId(value.id) &&
    positiveInteger(value.revision) &&
    validProviderOrigin(value.origin) &&
    validIdentity(value.realmId) &&
    validIdentityArray(value.capabilities, 64, true)
  );
}

function validRouteAccountPolicy(value: unknown, planMode: string): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "revision",
      "mode",
      "allowedRealmIds",
      "preferredAccountId",
      "candidateAccountIds",
      "accountRevisions",
      "failoverPolicy",
    ]) ||
    !positiveInteger(value.revision) ||
    !new Set(["client_passthrough", "managed"]).has(String(value.mode)) ||
    !validIdentityArray(value.allowedRealmIds, 64, true) ||
    typeof value.preferredAccountId !== "string" ||
    !validUniqueResourceIds(value.candidateAccountIds, 64, true) ||
    !isRecord(value.accountRevisions) ||
    Object.keys(value.accountRevisions).length > 64 ||
    !Object.entries(value.accountRevisions).every(([id, revision]) => validResourceId(id) && positiveInteger(revision)) ||
    !new Set(["off", "account_scoped_safe"]).has(String(value.failoverPolicy))
  ) return false;
  if (value.mode === "client_passthrough") {
    return (
      (planMode === "original_passthrough" || planMode === "managed") &&
      value.preferredAccountId === "" &&
      value.candidateAccountIds.length === 0 &&
      Object.keys(value.accountRevisions).length === 0
    );
  }
  const candidateAccountIds = value.candidateAccountIds as string[];
  const accountRevisions = value.accountRevisions as Record<string, unknown>;
  return (
    planMode === "managed" &&
    validResourceId(value.preferredAccountId) &&
    candidateAccountIds.includes(value.preferredAccountId) &&
    candidateAccountIds.every((id) => positiveInteger(accountRevisions[id])) &&
    Object.keys(accountRevisions).every((id) => candidateAccountIds.includes(id))
  );
}

function validModelPolicy(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["revision", "mode", "fixedModel"]) &&
    positiveInteger(value.revision) &&
    validIdentity(value.mode) &&
    validTrimmedString(value.fixedModel, 256, true)
  );
}

function validPluginBindings(value: unknown): boolean {
  if (!Array.isArray(value) || value.length > 128) return false;
  const ids = new Set<string>();
  return value.every(
    (binding) =>
      isRecord(binding) &&
      hasClosedFields(binding, ["id", "revision", "pluginId"]) &&
      validResourceId(binding.id) &&
      positiveInteger(binding.revision) &&
      validResourceId(binding.pluginId) &&
      !ids.has(binding.id) &&
      (ids.add(binding.id), true),
  );
}

function validSimplePolicy(value: unknown, hasMode: boolean): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, hasMode ? ["id", "revision", "mode"] : ["id", "revision"]) &&
    (value.id === "" || validResourceId(value.id)) &&
    nonNegativeInteger(value.revision) &&
    (!hasMode || validTrimmedString(value.mode, 128, true))
  );
}

function requireEnvironmentDraft(value: unknown, expectedId: string): EnvironmentDraft {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["environmentId", "baseRevision", "draftRevision", "candidateDigest", "candidate"]) ||
    value.environmentId !== expectedId ||
    !nonNegativeInteger(value.baseRevision) ||
    !positiveInteger(value.draftRevision) ||
    !validDigest(value.candidateDigest) ||
    !validEnvironmentRecord(value.candidate, true) ||
    value.candidate.id !== expectedId ||
    value.candidate.digest !== value.candidateDigest
  ) throw new ControlContractError();
  return value as unknown as EnvironmentDraft;
}

function validEnvironmentDraftInput(value: unknown): value is EnvironmentDraftInput {
  return (
    isRecord(value) &&
    hasClosedFields(value, [
      "expectedDraftRevision",
      "name",
      "state",
      "clientEndpoints",
      "pluginBindings",
      "budgetPolicy",
      "egressPolicy",
      "contentRecording",
      "policySet",
    ]) &&
    nonNegativeInteger(value.expectedDraftRevision) &&
    validDisplayLabel(value.name, 256, false) &&
    (value.state === "active" || value.state === "disabled") &&
    validEnvironmentEndpoints(value.clientEndpoints) &&
    validPluginBindings(value.pluginBindings) &&
    validSimplePolicy(value.budgetPolicy, false) &&
    validSimplePolicy(value.egressPolicy, true) &&
    validContentRecordingPolicy(value.contentRecording) &&
    validEnvironmentPolicySet(value.policySet)
  );
}

function requireEnvironmentImpact(
  value: unknown,
  environmentId: string,
  draftRevision: number,
): EnvironmentImpact {
  const classes = new Set(["hot_switch", "reconnect_required", "restart_required"]);
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "environmentId",
      "baseRevision",
      "draftRevision",
      "candidateDigest",
      "classification",
      "hotSwitchCount",
      "reconnectRequiredCount",
      "restartRequiredCount",
      "affected",
    ]) ||
    value.environmentId !== environmentId ||
    !nonNegativeInteger(value.baseRevision) ||
    value.draftRevision !== draftRevision ||
    !validDigest(value.candidateDigest) ||
    !classes.has(String(value.classification)) ||
    !nonNegativeInteger(value.hotSwitchCount) ||
    !nonNegativeInteger(value.reconnectRequiredCount) ||
    !nonNegativeInteger(value.restartRequiredCount) ||
    !Array.isArray(value.affected) ||
    value.affected.length > maximumCollectionItems ||
    !value.affected.every(
      (capture) =>
        isRecord(capture) &&
        hasClosedFields(capture, ["captureKind", "captureId", "classification"]) &&
        validCaptureKind(capture.captureKind) &&
        validResourceId(capture.captureId) &&
        classes.has(String(capture.classification)),
    ) ||
    value.hotSwitchCount + value.reconnectRequiredCount + value.restartRequiredCount !== value.affected.length
  ) throw new ControlContractError();
  return value as unknown as EnvironmentImpact;
}

function requireEnvironmentPublishResult(
  value: unknown,
  environmentId: string,
  draftRevision: number,
): EnvironmentPublishResult {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["outcome", "environment", "impact"]) ||
    value.outcome !== "committed"
  ) throw new ControlContractError();
  const environment = requireEnvironmentRecord(value.environment, environmentId);
  const impact = requireEnvironmentImpact(value.impact, environmentId, draftRevision);
  if (environment.digest !== impact.candidateDigest || environment.revision <= impact.baseRevision) {
    throw new ControlContractError();
  }
  return value as unknown as EnvironmentPublishResult;
}

function requireCaptureKey(value: unknown): asserts value is string {
  if (typeof value !== "string" || !/^(managed_run|manual_capture):[A-Za-z0-9_.:-]{1,128}$/.test(value)) {
    throw new ControlContractError();
  }
}

function validCaptureKind(value: unknown): value is "managed_run" | "manual_capture" {
  return value === "managed_run" || value === "manual_capture";
}

function requireCapturePage(value: unknown): CapturePage {
  if (!isRecord(value) || !hasClosedFields(value, ["items"]) || !Array.isArray(value.items) || value.items.length > maximumDashboardPageItems) {
    throw new ControlContractError();
  }
  let previous = "";
  for (const item of value.items) {
    if (!validCaptureRecord(item) || (previous !== "" && previous >= item.key)) throw new ControlContractError();
    previous = item.key;
  }
  return value as unknown as CapturePage;
}

function requireCaptureRecord(value: unknown, expectedKey?: string): CaptureRecord {
  if (!validCaptureRecord(value) || (expectedKey !== undefined && value.key !== expectedKey)) throw new ControlContractError();
  return value as unknown as CaptureRecord;
}

function validCaptureRecord(
  value: unknown,
): value is Record<string, unknown> & { key: string } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["key", "id", "kind", "displayName", "state", "observation", "createdAt", "updatedAt"], ["managedRun", "manualCapture"]) ||
    typeof value.key !== "string" ||
    !validCaptureId(value.id) ||
    !validCaptureKind(value.kind) ||
    value.key !== `${value.kind}:${value.id}` ||
    !validDisplayLabel(value.displayName, 256, false) ||
    !validIdentity(value.state) ||
    !validIdentity(value.observation) ||
    !validTimestamp(value.createdAt) ||
    !validTimestamp(value.updatedAt)
  ) return false;
  if (value.kind === "managed_run") {
    return value.manualCapture === undefined && validManagedRunCapture(value.managedRun);
  }
  return value.managedRun === undefined && validManualCaptureSummary(value.manualCapture);
}

function validManagedRunCapture(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["executableLabel", "cwd", "canonicalExecutablePath", "recognition", "expiresAt"], [
      "localUserLabel", "machineId", "machineRegistrationRevision", "workspaceId", "workspaceLabel",
      "workspaceEvidence", "workspaceDerivationRevision", "processId", "firstObservedAt",
    ]) &&
    validDisplayLabel(value.executableLabel, 256, false) &&
    validAbsoluteLocalPath(value.cwd) &&
    validAbsoluteLocalPath(value.canonicalExecutablePath) &&
    validIdentity(value.recognition) &&
    validTimestamp(value.expiresAt) &&
    optionalString(value.localUserLabel, 256) &&
    optionalIdentity(value.machineId) &&
    optionalPositiveInteger(value.machineRegistrationRevision) &&
    optionalIdentity(value.workspaceId) &&
    optionalString(value.workspaceLabel, 512) &&
    optionalIdentity(value.workspaceEvidence) &&
    optionalPositiveInteger(value.workspaceDerivationRevision) &&
    (value.processId === undefined || positiveInteger(value.processId)) &&
    (value.firstObservedAt === undefined || validTimestamp(value.firstObservedAt))
  );
}

function validManualCaptureSummary(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["clientClass", "lifetime", "credentialRevision"], ["expiresAt", "lastObservedAt"]) &&
    validManualCaptureClientClass(value.clientClass) &&
    validManualCaptureLifetime(value.lifetime) &&
    positiveInteger(value.credentialRevision) &&
    (value.expiresAt === undefined || validTimestamp(value.expiresAt)) &&
    (value.lastObservedAt === undefined || validTimestamp(value.lastObservedAt))
  );
}

function requireCaptureAssignment(value: unknown, expectedKey?: string): CaptureAssignment {
  if (!validCaptureAssignment(value) || (expectedKey !== undefined && value.captureKey !== expectedKey)) throw new ControlContractError();
  return value as unknown as CaptureAssignment;
}

function validCaptureAssignment(
  value: unknown,
): value is Record<string, unknown> & { captureKey: string; revision: number } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["captureKey", "captureId", "captureKind", "environmentId", "revision", "source", "updatedAt"]) ||
    typeof value.captureKey !== "string" ||
    !validCaptureId(value.captureId) ||
    !validCaptureKind(value.captureKind) ||
    value.captureKey !== `${value.captureKind}:${value.captureId}` ||
    !validResourceId(value.environmentId) ||
    !positiveInteger(value.revision) ||
    !new Set(["launch", "manual_create", "workspace_default", "operator_switch", "system_transparent"]).has(String(value.source)) ||
    !validTimestamp(value.updatedAt)
  ) return false;
  return true;
}

function requireCaptureAssignmentSwitch(
  value: unknown,
  captureKey: string,
  previousRevision: number,
): CaptureAssignmentSwitchResult {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["assignment", "boundary", "closedConnections", "applied"], ["reasonCode"]) ||
    !validCaptureAssignment(value.assignment) ||
    value.assignment.captureKey !== captureKey ||
    value.assignment.revision < previousRevision ||
    !new Set(["no_change", "hot_switch", "reconnect_required", "restart_required"]).has(String(value.boundary)) ||
    !validIdentityArray(value.closedConnections, maximumCollectionItems, true) ||
    typeof value.applied !== "boolean" ||
    (value.reasonCode !== undefined && value.reasonCode !== "capture_restart_required")
  ) throw new ControlContractError();
  if (
    (value.boundary === "restart_required") !== (value.applied === false) ||
    (value.boundary === "restart_required") !== (value.reasonCode === "capture_restart_required") ||
    (value.boundary !== "reconnect_required" && value.closedConnections.length !== 0)
  ) throw new ControlContractError();
  return value as unknown as CaptureAssignmentSwitchResult;
}

function workspaceDefaultPath(machineId: string, workspaceId: string): string {
  return `/api/v1/machines/${encodeURIComponent(machineId)}/workspaces/${encodeURIComponent(workspaceId)}/environment-default`;
}

function requireWorkspaceEnvironmentDefault(
  value: unknown,
  machineId: string,
  workspaceId: string,
  environmentId?: string,
): WorkspaceEnvironmentDefault {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["machineId", "workspaceId", "environmentId", "environmentName", "revision", "updatedAt"]) ||
    value.machineId !== machineId ||
    value.workspaceId !== workspaceId ||
    (environmentId !== undefined && value.environmentId !== environmentId) ||
    !validResourceId(value.environmentId) ||
    !validDisplayLabel(value.environmentName, 256, false) ||
    !positiveInteger(value.revision) ||
    !validTimestamp(value.updatedAt)
  ) {
    throw new ControlContractError();
  }
  return value as unknown as WorkspaceEnvironmentDefault;
}

function validActivityQuery(value: unknown): value is ActivityQuery | undefined {
  return (
    value === undefined ||
    (isRecord(value) &&
      hasClosedFields(value, [], ["cursor", "captureRunId", "environmentId"]) &&
      (value.cursor === undefined || validOpaqueCursor(value.cursor)) &&
      (value.captureRunId === undefined || validResourceId(value.captureRunId)) &&
      (value.environmentId === undefined || validResourceId(value.environmentId)))
  );
}

function requireActivityPage(value: unknown): ActivityPage {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["items"], ["nextCursor"]) ||
    !Array.isArray(value.items) ||
    value.items.length > maximumActivityPageItems ||
    !value.items.every(validActivityRecord) ||
    (value.nextCursor !== undefined && !validOpaqueCursor(value.nextCursor))
  ) throw new ControlContractError();
  return value as unknown as ActivityPage;
}

function validActivityRecord(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["id", "occurredAt", "kind", "title", "status", "source", "environment", "parentRefs"], ["reasonCode"]) &&
    validRouteIdentity(value.id) &&
    validTimestamp(value.occurredAt) &&
    value.kind === "exchange" &&
    validDisplayLabel(value.title, 512, false) &&
    validActivityStatus(value.status) &&
    (value.reasonCode === undefined || validIdentity(value.reasonCode)) &&
    validActivitySource(value.source) &&
    validFrozenEnvironmentRef(value.environment) &&
    validActivityParentRefs(value.parentRefs, value.id)
  );
}

function validActivitySource(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["kind", "displayName", "recognition"]) &&
    new Set(["capture_run", "manual_proxy", "system_proxy"]).has(String(value.kind)) &&
    validDisplayLabel(value.displayName, 512, false) &&
    new Set(["verified", "configured", "unknown"]).has(String(value.recognition))
  );
}

function validFrozenEnvironmentRef(value: unknown): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "id", "revision", "digest", "clientEndpointId", "clientEndpointRevision",
      "protocolPlanId", "protocolPlanRevision", "routeId", "routeRevision",
    ], ["accountId", "accountRevision", "credentialEpoch"]) ||
    !validResourceId(value.id) ||
    !positiveInteger(value.revision) ||
    !validDigest(value.digest) ||
    !validResourceId(value.clientEndpointId) ||
    !positiveInteger(value.clientEndpointRevision) ||
    !validResourceId(value.protocolPlanId) ||
    !positiveInteger(value.protocolPlanRevision) ||
    !validResourceId(value.routeId) ||
    !positiveInteger(value.routeRevision)
  ) return false;
  const account = [value.accountId, value.accountRevision, value.credentialEpoch];
  return account.every((field) => field === undefined) ||
    (validResourceId(value.accountId) && positiveInteger(value.accountRevision) && positiveInteger(value.credentialEpoch));
}

function validActivityParentRefs(value: unknown, exchangeId: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["exchangeId"], ["captureRunId", "manualCaptureId", "connectionId"]) &&
    value.exchangeId === exchangeId &&
    optionalIdentity(value.captureRunId) &&
    optionalIdentity(value.manualCaptureId) &&
    optionalIdentity(value.connectionId) &&
    !(value.captureRunId !== undefined && value.manualCaptureId !== undefined)
  );
}

function validActivityStatus(value: unknown): value is ActivityStatus {
  return value === "succeeded" || value === "failed" || value === "canceled";
}

function requireExchangeDetail(
  value: unknown,
  expectedId: string,
  expectedView: "incremental" | "full",
): ExchangeDetail {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["id", "status", "environment", "parentRefs", "processingTrace", "content"], ["diagnosis"]) ||
    value.id !== expectedId ||
    !validActivityStatus(value.status) ||
    !validFrozenEnvironmentRef(value.environment) ||
    !validActivityParentRefs(value.parentRefs, expectedId) ||
    (value.diagnosis !== undefined && !validExchangeDiagnosis(value.diagnosis)) ||
    !validExchangeTrace(value.processingTrace) ||
    !validExchangeContent(value.content, expectedView)
  ) throw new ControlContractError();
  return value as unknown as ExchangeDetail;
}

function validExchangeDiagnosis(value: unknown): boolean {
  if (!isRecord(value) || !hasClosedFields(
    value,
    [],
    ["providerStatus", "providerField", "clientField", "clientPath"],
  ) || Object.keys(value).length === 0) return false;
  return (
    (value.providerStatus === undefined ||
      (typeof value.providerStatus === "number" && Number.isInteger(value.providerStatus) &&
        value.providerStatus >= 100 && value.providerStatus <= 599)) &&
    optionalIdentity(value.providerField) &&
    optionalIdentity(value.clientField) &&
    (value.clientPath === undefined ||
      (typeof value.clientPath === "string" && value.clientPath.length > 0 && value.clientPath.length <= 256 &&
        /^[A-Za-z0-9$._\-\[\]]+$/u.test(value.clientPath)))
  );
}

function validExchangeContent(value: unknown, expectedView: "incremental" | "full"): boolean {
  if (!isRecord(value) || value.state === "not_recorded") {
    return isRecord(value) && hasClosedFields(value, ["state"]) && value.state === "not_recorded";
  }
  return (
    value.state === "recorded" &&
    hasClosedFields(
      value,
      ["state", "mode", "recordedAt", "expiresAt", "requestProjection", "request"],
      ["response"],
    ) &&
    (value.mode === "full" || value.mode === "metadata_only") &&
    validTimestamp(value.recordedAt) &&
    validTimestamp(value.expiresAt) &&
    validExchangeRequestProjection(value.requestProjection, expectedView) &&
    validExchangeContentRequest(value.request, value.mode, value.requestProjection) &&
    (value.response === undefined || validExchangeContentResponse(value.response, value.mode))
  );
}

function validExchangeRequestProjection(
  value: unknown,
  expectedView: "incremental" | "full",
): boolean {
  if (!isRecord(value) || !hasClosedFields(value, [
    "view",
    "relationship",
    "inheritedMessageCount",
    "totalMessageCount",
    "fullSnapshotAvailable",
  ]) || value.view !== expectedView ||
    (value.relationship !== "checkpoint" && value.relationship !== "incremental" &&
      value.relationship !== "same_transcript") ||
    !nonNegativeInteger(value.inheritedMessageCount) ||
    !nonNegativeInteger(value.totalMessageCount) || value.totalMessageCount < 1 ||
    value.inheritedMessageCount > value.totalMessageCount ||
    typeof value.fullSnapshotAvailable !== "boolean" ||
    value.fullSnapshotAvailable !== (value.inheritedMessageCount > 0)) return false;
  switch (value.relationship) {
    case "checkpoint": return value.inheritedMessageCount === 0;
    case "incremental": return value.inheritedMessageCount > 0 &&
      value.inheritedMessageCount < value.totalMessageCount;
    case "same_transcript": return value.inheritedMessageCount === value.totalMessageCount;
  }
}

function validExchangeContentRequest(
  value: unknown,
  mode: "full" | "metadata_only",
  projection: unknown,
): boolean {
  if (!isRecord(projection)) return false;
  const displayedMessageCount = projection.view === "full"
    ? projection.totalMessageCount
    : Number(projection.totalMessageCount) - Number(projection.inheritedMessageCount);
  return (
    isRecord(value) &&
    hasClosedFields(value, ["requestedModel", "effectiveModel", "maxOutputTokens", "stream", "messages", "tools"]) &&
    validIdentity(value.requestedModel) &&
    validIdentity(value.effectiveModel) &&
    nonNegativeInteger(value.maxOutputTokens) &&
    typeof value.stream === "boolean" &&
    Array.isArray(value.messages) &&
    value.messages.length === displayedMessageCount &&
    value.messages.length <= maximumCollectionItems &&
    value.messages.every((message) => validExchangeContentMessage(message, mode)) &&
    Array.isArray(value.tools) &&
    value.tools.length <= maximumCollectionItems &&
    value.tools.every((tool) => isRecord(tool) &&
      hasClosedFields(tool, ["name"], ["namespace"]) &&
      validIdentity(tool.name) && optionalIdentity(tool.namespace))
  );
}

function validExchangeContentResponse(value: unknown, mode: "full" | "metadata_only"): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["id", "requestedModel", "effectiveModel", "reportedModel", "stopReason", "blocks", "usage"]) &&
    validIdentity(value.id) && validIdentity(value.requestedModel) &&
    validIdentity(value.effectiveModel) && validIdentity(value.reportedModel) &&
    (value.stopReason === "end_turn" || value.stopReason === "max_tokens" ||
      value.stopReason === "tool_use" || value.stopReason === "stop_sequence") &&
    Array.isArray(value.blocks) && value.blocks.length > 0 &&
    value.blocks.length <= maximumCollectionItems &&
    value.blocks.every((block) => validExchangeContentBlock(block, mode)) &&
    validExchangeUsage(value.usage)
  );
}

function validExchangeContentMessage(value: unknown, mode: "full" | "metadata_only"): boolean {
  return (
    isRecord(value) && hasClosedFields(value, ["role", "blocks"]) &&
    (value.role === "system" || value.role === "developer" || value.role === "user" ||
      value.role === "assistant" || value.role === "tool") &&
    Array.isArray(value.blocks) && value.blocks.length > 0 &&
    value.blocks.length <= maximumCollectionItems &&
    value.blocks.every((block) => validExchangeContentBlock(block, mode))
  );
}

function validExchangeContentBlock(value: unknown, mode: "full" | "metadata_only"): boolean {
  if (!isRecord(value) || !hasClosedFields(
    value,
    ["kind", "availability", "originalSize"],
    ["text", "callId", "toolName", "arguments", "toolError"],
  )) return false;
  const expectedAvailability = mode === "full" && value.kind !== "provider_extension" ? "recorded" : "omitted";
  if ((value.kind !== "text" && value.kind !== "refusal" && value.kind !== "tool_call" &&
      value.kind !== "tool_result" && value.kind !== "provider_extension") ||
    value.availability !== expectedAvailability || !nonNegativeInteger(value.originalSize) ||
    (value.text !== undefined && typeof value.text !== "string") ||
    !optionalIdentity(value.callId) || !optionalIdentity(value.toolName) ||
    (value.arguments !== undefined && !isRecord(value.arguments)) ||
    (value.toolError !== undefined && typeof value.toolError !== "boolean")) return false;
  if ((mode === "metadata_only" || value.kind === "provider_extension") &&
    (value.text !== undefined || value.arguments !== undefined)) return false;
  return true;
}

function validExchangeUsage(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["inputUncached", "cacheWrite", "cacheRead", "output", "reasoning"]) &&
    [value.inputUncached, value.cacheWrite, value.cacheRead, value.output, value.reasoning].every(validExchangeUsageValue);
}

function validExchangeUsageValue(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["known"], ["tokens", "source"]) &&
    typeof value.known === "boolean" &&
    (value.known
      ? nonNegativeInteger(value.tokens) && validIdentity(value.source)
      : value.tokens === undefined && value.source === undefined);
}

function validExchangeTrace(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["pluginRunIds", "attempts", "result"], ["egressProxyId"]) &&
    optionalIdentity(value.egressProxyId) &&
    validIdentityArray(value.pluginRunIds, maximumCollectionItems, true) &&
    Array.isArray(value.attempts) &&
    value.attempts.length <= maximumCollectionItems &&
    value.attempts.every(validEgressAttempt) &&
    validIdentity(value.result)
  );
}

const approvalStates = new Set(["pending", "allowed", "denied", "canceled", "expired"]);
const approvalDecisions = new Set(["allow-once", "deny"]);
const approvalScopes = new Set(["request", "host_port"]);

function requireApprovalPage(value: unknown): ApprovalPage {
  if (!isRecord(value) || !hasClosedFields(value, ["items"]) || !Array.isArray(value.items) || value.items.length > maximumDashboardPageItems || !value.items.every(validApprovalView)) {
    throw new ControlContractError();
  }
  return value as unknown as ApprovalPage;
}

function validApprovalView(
  value: unknown,
): value is Record<string, unknown> & {
  id: string;
  revision: number;
  decision?: string;
  decisionScope?: string;
} {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "id", "revision", "kind", "state", "risk", "titleKey", "summaryKey", "aggregateKey",
      "subjectRefs", "subjectLabels", "requestCount", "waiterCount", "choices", "createdAt", "expiresAt",
    ], [
      "exchangeId", "environmentId", "environmentRevision", "environmentDigest", "routeId", "routeRevision",
      "target", "resolvedAt", "decision", "decisionScope", "terminalReason",
    ]) ||
    !validResourceId(value.id) ||
    !positiveInteger(value.revision) ||
    !new Set(["tool_intent", "network_ask", "client_root_ask"]).has(String(value.kind)) ||
    !approvalStates.has(String(value.state)) ||
    !validIdentity(value.risk) ||
    !validIdentity(value.titleKey) ||
    !validIdentity(value.summaryKey) ||
    !validIdentity(value.aggregateKey) ||
    !validIdentityArray(value.subjectRefs, maximumCollectionItems, true) ||
    !validStringArray(value.subjectLabels, maximumCollectionItems, 512) ||
    value.subjectRefs.length !== value.subjectLabels.length ||
    !positiveInteger(value.requestCount) ||
    !nonNegativeInteger(value.waiterCount) ||
    !Array.isArray(value.choices) ||
    value.choices.length === 0 ||
    value.choices.length > 8 ||
    !value.choices.every(validApprovalChoice) ||
    !validTimestamp(value.createdAt) ||
    !validTimestamp(value.expiresAt)
  ) {
    return false;
  }
  if (
    !optionalIdentity(value.exchangeId) ||
    !optionalIdentity(value.environmentId) ||
    !optionalPositiveInteger(value.environmentRevision) ||
    (value.environmentDigest !== undefined && !validDigest(value.environmentDigest)) ||
    !optionalIdentity(value.routeId) ||
    !optionalPositiveInteger(value.routeRevision) ||
    (value.target !== undefined && !validApprovalTarget(value.target)) ||
    (value.resolvedAt !== undefined && !validTimestamp(value.resolvedAt)) ||
    (value.decision !== undefined && !approvalDecisions.has(String(value.decision))) ||
    (value.decisionScope !== undefined && !approvalScopes.has(String(value.decisionScope))) ||
    !optionalIdentity(value.terminalReason)
  ) return false;
  const environment = [value.environmentId, value.environmentRevision, value.environmentDigest];
  const route = [value.routeId, value.routeRevision];
  if (!allOrNone(environment) || !allOrNone(route) || (route[0] !== undefined && environment[0] === undefined)) return false;
  return value.state === "pending"
    ? value.resolvedAt === undefined && value.decision === undefined && value.decisionScope === undefined
    : value.resolvedAt !== undefined;
}

function validApprovalChoice(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["decision", "scope", "labelKey"]) && approvalDecisions.has(String(value.decision)) && approvalScopes.has(String(value.scope)) && validIdentity(value.labelKey);
}

function validApprovalTarget(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["host", "port"]) && validHost(value.host) && validPort(value.port);
}

function requireApprovalDecisionResponse(
  value: unknown,
  expected: ApprovalView,
  choice: ApprovalChoice,
): ApprovalView {
  if (!validApprovalView(value) || value.id !== expected.id || value.revision <= expected.revision || value.decision !== choice.decision || value.decisionScope !== choice.scope) {
    throw new ControlContractError();
  }
  return value as unknown as ApprovalView;
}

const connectionPhases = new Set(["attempted", "asked", "decided", "connected", "closed", "failed"]);

function requireConnectionPage(value: unknown): ConnectionPage {
  if (!isRecord(value) || !hasClosedFields(value, ["items"], ["nextCursor"]) || !Array.isArray(value.items) || value.items.length > maximumActivityPageItems || !value.items.every(validConnectionRecord) || (value.nextCursor !== undefined && !validOpaqueCursor(value.nextCursor))) {
    throw new ControlContractError();
  }
  return value as unknown as ConnectionPage;
}

function validConnectionRecord(value: unknown): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "sequence", "connectionId", "sourceConfidence", "requestedHost", "port", "decryption", "phase",
      "bytesUp", "bytesDown", "startedAt",
    ], [
      "ingressId", "sourceLabel", "environmentId", "environmentName", "environmentRevision", "clientEndpointId",
      "clientEndpointRevision", "observedSni", "routeHost", "ip", "decision", "ruleId", "credentialBindingId",
      "egressScope", "egressSource", "egressRuleId", "egressSelectorRunId", "egressProxyId", "egressPolicyRevision",
      "endedAt", "outcome", "errorClass",
    ]) ||
    !positiveInteger(value.sequence) ||
    !validIdentity(value.connectionId) ||
    !new Set(["unknown", "configured", "verified"]).has(String(value.sourceConfidence)) ||
    !validHost(value.requestedHost) ||
    !validPort(value.port) ||
    !new Set(["none", "blind", "mitm"]).has(String(value.decryption)) ||
    !connectionPhases.has(String(value.phase)) ||
    !nonNegativeInteger(value.bytesUp) ||
    !nonNegativeInteger(value.bytesDown) ||
    !validTimestamp(value.startedAt)
  ) return false;
  const optionals: [unknown, (candidate: unknown) => boolean][] = [
    [value.ingressId, validIdentity], [value.sourceLabel, (v) => validTrimmedString(v, 512, false)],
    [value.environmentId, validResourceId], [value.environmentName, (v) => validDisplayLabel(v, 256, false)],
    [value.environmentRevision, positiveInteger], [value.clientEndpointId, validResourceId],
    [value.clientEndpointRevision, positiveInteger], [value.observedSni, validHost], [value.routeHost, validHost],
    [value.ip, validIpAddress], [value.ruleId, validIdentity], [value.credentialBindingId, validIdentity],
    [value.egressRuleId, validIdentity], [value.egressSelectorRunId, validIdentity], [value.egressProxyId, validIdentity],
    [value.egressPolicyRevision, positiveInteger], [value.endedAt, validTimestamp], [value.errorClass, validIdentity],
  ];
  if (optionals.some(([candidate, validator]) => candidate !== undefined && !validator(candidate))) return false;
  const environment = [value.environmentId, value.environmentName, value.environmentRevision, value.clientEndpointId, value.clientEndpointRevision];
  if (!allOrNone(environment)) return false;
  if (value.decision !== undefined && !new Set(["allow", "deny", "ask"]).has(String(value.decision))) return false;
  if (value.egressScope !== undefined && !new Set(["environment", "network"]).has(String(value.egressScope))) return false;
  if (value.egressSource !== undefined && !new Set(["environment_rule", "environment_plugin", "environment_default", "network_rule", "network_default"]).has(String(value.egressSource))) return false;
  if ((value.egressScope === undefined) !== (value.egressSource === undefined)) return false;
  if (value.outcome !== undefined && !new Set(["completed", "denied", "canceled", "failed"]).has(String(value.outcome))) return false;
  return true;
}

function requireEgressAttemptPage(value: unknown): EgressAttemptPage {
  if (!isRecord(value) || !hasClosedFields(value, ["items"], ["nextCursor"]) || !Array.isArray(value.items) || value.items.length > maximumActivityPageItems || !value.items.every(validEgressAttempt) || (value.nextCursor !== undefined && !validOpaqueCursor(value.nextCursor))) {
    throw new ControlContractError();
  }
  return value as unknown as EgressAttemptPage;
}

function validEgressAttempt(value: unknown): boolean {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "sequence", "id", "purpose", "payloadClass", "parent", "caller", "targetOrigin", "decision",
      "reusedTransport", "startedAt", "terminal", "bytesOut", "bytesIn",
    ], ["connectionId", "callerId", "outcome", "errorClass", "completedAt"]) ||
    !positiveInteger(value.sequence) ||
    !validIdentity(value.id) ||
    !new Set(["provider_attempt", "route_operation", "original_origin", "agent_probe", "blind_tunnel", "auxiliary_llm", "language_transform", "plugin_catalog_sync", "plugin_artifact_fetch", "update"]).has(String(value.purpose)) ||
    !new Set(["none", "control", "client_data", "client_semantic", "opaque_tunnel", "runtime"]).has(String(value.payloadClass)) ||
    !validEgressParent(value.parent) ||
    !new Set(["core", "plugin"]).has(String(value.caller)) ||
    !validTargetOrigin(value.targetOrigin) ||
    !validEgressDecision(value.decision) ||
    typeof value.reusedTransport !== "boolean" ||
    !validTimestamp(value.startedAt) ||
    typeof value.terminal !== "boolean" ||
    !nonNegativeInteger(value.bytesOut) ||
    !nonNegativeInteger(value.bytesIn) ||
    !optionalIdentity(value.connectionId) ||
    !optionalIdentity(value.callerId) ||
    !optionalIdentity(value.errorClass)
  ) return false;
  return value.terminal
    ? new Set(["completed", "failed", "canceled"]).has(String(value.outcome)) && validTimestamp(value.completedAt)
    : value.outcome === undefined && value.completedAt === undefined && value.errorClass === undefined && value.bytesOut === 0 && value.bytesIn === 0;
}

function validEgressParent(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["kind"], ["id", "exchangeId"]) && validIdentity(value.kind) && optionalIdentity(value.id) && optionalIdentity(value.exchangeId);
}

function validEgressDecision(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["authority"], ["policyId", "policyRevision", "ruleId", "proxyId"]) &&
    new Set(["environment", "network", "runtime"]).has(String(value.authority)) &&
    optionalIdentity(value.policyId) &&
    optionalPositiveInteger(value.policyRevision) &&
    optionalIdentity(value.ruleId) &&
    optionalIdentity(value.proxyId) &&
    ((value.policyId === undefined) === (value.policyRevision === undefined))
  );
}

function requireConnectionRuleSet(value: unknown, expectedRevision?: number): ConnectionRuleSet {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["revision", "rules", "mode"]) ||
    !positiveInteger(value.revision) ||
    (expectedRevision !== undefined && value.revision !== expectedRevision) ||
    !Array.isArray(value.rules) ||
    value.rules.length > maximumCollectionItems ||
    !value.rules.every(validConnectionRule) ||
    !validConnectionPolicyMode(value.mode)
  ) throw new ControlContractError();
  return value as unknown as ConnectionRuleSet;
}

function validConnectionRuleSetInput(value: unknown): value is ConnectionRuleSetInput {
  return isRecord(value) && hasClosedFields(value, ["rules", "mode"]) && Array.isArray(value.rules) && value.rules.length <= maximumCollectionItems && value.rules.every(validConnectionRule) && validConnectionPolicyMode(value.mode);
}

function validConnectionPolicyMode(value: unknown): boolean {
  return new Set(["monitor", "ask_unknown", "deny_unknown"]).has(String(value));
}

function validConnectionRule(value: unknown): boolean {
  if (!isRecord(value) || !hasClosedFields(value, ["id", "priority", "decision", "match"], ["host", "port"]) || !validResourceId(value.id) || !nonNegativeInteger(value.priority) || value.priority > maximumUint32 || !new Set(["allow", "deny", "ask"]).has(String(value.decision)) || !new Set(["exact_host", "exact_host_port"]).has(String(value.match))) return false;
  if (!validHost(value.host)) return false;
  return value.match === "exact_host" ? value.port === undefined : validPort(value.port);
}

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

function requireManualCaptureContext(value: unknown, expectedEnvironmentId: string): ManualCaptureContext {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "confirmationToken", "proxyAddress", "environmentId", "environmentRevision", "environmentDigest",
      "launchAuthorityDigest", "protectedAuthorities", "managedCredentialAuthorities", "defaultTemporarySeconds", "maxTemporarySeconds",
    ], ["root"]) ||
    !validConfirmationToken(value.confirmationToken) ||
    !validManualProxyAddress(value.proxyAddress) ||
    value.environmentId !== expectedEnvironmentId ||
    !positiveInteger(value.environmentRevision) ||
    !validDigest(value.environmentDigest) ||
    !validDigest(value.launchAuthorityDigest) ||
    !validStringArray(value.protectedAuthorities, maximumCollectionItems, maximumHostBytes) ||
    !validStringArray(value.managedCredentialAuthorities, maximumCollectionItems, maximumHostBytes) ||
    !positiveInteger(value.defaultTemporarySeconds) ||
    !positiveInteger(value.maxTemporarySeconds) ||
    value.defaultTemporarySeconds > value.maxTemporarySeconds ||
    (value.root !== undefined && !validManualCaptureRoot(value.root))
  ) throw new ControlContractError();
  return value as unknown as ManualCaptureContext;
}

function validManualCaptureCreateInput(value: unknown): value is ManualCaptureCreateInput {
  return (
    isRecord(value) &&
    hasClosedFields(value, ["environmentId", "displayName", "clientClass", "lifetime", "confirmationToken"], ["expiresInSeconds"]) &&
    validResourceId(value.environmentId) &&
    validDisplayLabel(value.displayName, 256, false) &&
    validManualCaptureClientClass(value.clientClass) &&
    validManualCaptureLifetime(value.lifetime) &&
    validConfirmationToken(value.confirmationToken) &&
    (value.expiresInSeconds === undefined || positiveInteger(value.expiresInSeconds)) &&
    ((value.lifetime === "temporary") === (value.expiresInSeconds !== undefined))
  );
}

function validManualCaptureClientClass(value: unknown): boolean {
  return value === "cli" || value === "desktop_app" || value === "other";
}

function validManualCaptureLifetime(value: unknown): boolean {
  return value === "temporary" || value === "until_revoked";
}

function validManualCaptureRecord(
  value: unknown,
): value is Record<string, unknown> & { id: string } {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, ["id", "displayName", "clientClass", "lifetime", "state", "observation", "createdAt", "updatedAt"], ["expiresAt", "lastObservedAt"]) ||
    !validResourceId(value.id) ||
    !validDisplayLabel(value.displayName, 256, false) ||
    !validManualCaptureClientClass(value.clientClass) ||
    !validManualCaptureLifetime(value.lifetime) ||
    !new Set(["active", "revoked", "expired"]).has(String(value.state)) ||
    !new Set(["waiting_for_traffic", "observed"]).has(String(value.observation)) ||
    !validTimestamp(value.createdAt) ||
    !validTimestamp(value.updatedAt) ||
    (value.expiresAt !== undefined && !validTimestamp(value.expiresAt)) ||
    (value.lastObservedAt !== undefined && !validTimestamp(value.lastObservedAt))
  ) return false;
  return value.lifetime === "temporary" ? value.expiresAt !== undefined : value.expiresAt === undefined;
}

function requireManualCaptureRecord(value: unknown, expectedId?: string): ManualCaptureRecord {
  if (!validManualCaptureRecord(value) || (expectedId !== undefined && value.id !== expectedId)) throw new ControlContractError();
  return value as unknown as ManualCaptureRecord;
}

function requireManualCapturePage(value: unknown): ManualCapturePage {
  if (!isRecord(value) || !hasClosedFields(value, ["items"]) || !Array.isArray(value.items) || value.items.length > maximumDashboardPageItems || !value.items.every(validManualCaptureRecord)) throw new ControlContractError();
  return value as unknown as ManualCapturePage;
}

function requireManualCaptureGrant(value: unknown, expectedId?: string): ManualCaptureGrant {
  if (
    !isRecord(value) ||
    !hasClosedFields(value, [
      "capture", "proxyAddress", "proxyUsername", "proxyPassword", "environmentId", "assignmentRevision",
      "launchAuthorityDigest", "protectedAuthorities", "managedCredentialAuthorities",
    ], ["root"]) ||
    !validManualCaptureRecord(value.capture) ||
    (expectedId !== undefined && value.capture.id !== expectedId) ||
    !validManualProxyAddress(value.proxyAddress) ||
    !validTrimmedString(value.proxyUsername, 512, false) ||
    !validTrimmedString(value.proxyPassword, 2_048, false) ||
    !validResourceId(value.environmentId) ||
    !positiveInteger(value.assignmentRevision) ||
    !validDigest(value.launchAuthorityDigest) ||
    !validStringArray(value.protectedAuthorities, maximumCollectionItems, maximumHostBytes) ||
    !validStringArray(value.managedCredentialAuthorities, maximumCollectionItems, maximumHostBytes) ||
    (value.root !== undefined && !validManualCaptureRoot(value.root))
  ) throw new ControlContractError();
  return value as unknown as ManualCaptureGrant;
}

function validManualCaptureRoot(value: unknown): boolean {
  return isRecord(value) && hasClosedFields(value, ["kind", "derSha256", "fingerprint", "pemPath"]) && value.kind === "local_path" && validDigest(value.derSha256) && validTrimmedString(value.fingerprint, 128, false) && validAbsoluteLocalPath(value.pemPath);
}

function validManualProxyAddress(value: unknown): boolean {
  if (typeof value !== "string") return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" && parsed.hostname === "127.0.0.1" && parsed.port !== "" && parsed.username === "" && parsed.password === "" && parsed.pathname === "/" && parsed.search === "" && parsed.hash === "";
  } catch {
    return false;
  }
}

function validConfirmationToken(value: unknown): boolean {
  return typeof value === "string" && /^ctx_[A-Za-z0-9_-]{43}$/.test(value);
}

export function compareResourceIds(left: string, right: string): number {
  const leftBytes = new TextEncoder().encode(left);
  const rightBytes = new TextEncoder().encode(right);
  const length = Math.min(leftBytes.length, rightBytes.length);
  for (let index = 0; index < length; index += 1) {
    if (leftBytes[index] !== rightBytes[index]) return leftBytes[index]! - rightBytes[index]!;
  }
  return leftBytes.length - rightBytes.length;
}

function requireResourceId(value: unknown): asserts value is string {
  if (!validResourceId(value)) throw new ControlContractError();
}

function validResourceId(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(value);
}

function validCaptureId(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9_.:-]{1,128}$/.test(value);
}

function validDigest(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

function hasClosedFields(
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[] = [],
): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((field) => Object.prototype.hasOwnProperty.call(value, field)) && Object.keys(value).every((field) => allowed.has(field));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validIdentity(value: unknown): value is string {
  return validTrimmedString(value, maximumIdentityBytes, false);
}

function validRouteIdentity(value: unknown): value is string {
  return validTrimmedString(value, maximumIdentityBytes, false);
}

function optionalIdentity(value: unknown): boolean {
  return value === undefined || validIdentity(value);
}

function optionalString(value: unknown, maxBytes: number): boolean {
  return value === undefined || validTrimmedString(value, maxBytes, false);
}

function optionalPositiveInteger(value: unknown): boolean {
  return value === undefined || positiveInteger(value);
}

function validTrimmedString(value: unknown, maximumBytes: number, allowEmpty: boolean): value is string {
  return typeof value === "string" && (allowEmpty || value.length > 0) && value.trim() === value && new TextEncoder().encode(value).length <= maximumBytes && validUnicodeString(value);
}

function validDisplayLabel(value: unknown, maximumBytes: number, allowEmpty: boolean): value is string {
  return validTrimmedString(value, maximumBytes, allowEmpty) && !/[\u0000-\u001f\u007f]/u.test(value);
}

function validUnicodeString(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) return false;
  }
  return true;
}

function validIdentityArray(value: unknown, maximum: number, allowEmpty: boolean): value is string[] {
  return Array.isArray(value) && (allowEmpty || value.length > 0) && value.length <= maximum && value.every(validIdentity) && new Set(value).size === value.length;
}

function validStringArray(value: unknown, maximum: number, maximumBytes: number): value is string[] {
  return Array.isArray(value) && value.length <= maximum && value.every((item) => validTrimmedString(item, maximumBytes, false));
}

function validUniqueResourceIds(value: unknown, maximum: number, allowEmpty: boolean): value is string[] {
  return Array.isArray(value) && (allowEmpty || value.length > 0) && value.length <= maximum && value.every(validResourceId) && new Set(value).size === value.length;
}

function allOrNone(values: readonly unknown[]): boolean {
  return values.every((value) => value === undefined) || values.every((value) => value !== undefined);
}

function nonNegativeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0;
}

function positiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) > 0;
}

function validTimestamp(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && Number.isFinite(Date.parse(value));
}

function validPort(value: unknown): value is number {
  return positiveInteger(value) && value <= 65_535;
}

function validHost(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= maximumHostBytes && value.trim() === value && !/[\s/?#@]/u.test(value);
}

function validIpAddress(value: unknown): value is string {
  if (typeof value !== "string" || value.length === 0 || value.length > 64) return false;
  return /^(?:\d{1,3}\.){3}\d{1,3}$/.test(value) || /^[0-9A-Fa-f:]+$/.test(value);
}

function validOpaqueCursor(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 2_048 && /^[A-Za-z0-9_=-]+$/.test(value);
}

function validClientOrigin(value: unknown): value is string {
  if (!validCanonicalHTTPSOrigin(value)) return false;
  const host = new URL(value).hostname;
  return !/^\[.*\]$/u.test(host) && !/^(?:\d{1,3}\.){3}\d{1,3}$/u.test(host);
}

function validCanonicalHTTPSOrigin(value: unknown): value is string {
  if (typeof value !== "string") return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.pathname === "/" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      value === parsed.origin;
  } catch {
    return false;
  }
}

function validProviderOrigin(value: unknown): value is string {
  return validCanonicalHTTPSOrigin(value);
}

function validTargetOrigin(value: unknown): value is string {
  if (typeof value !== "string") return false;
  try {
    const parsed = new URL(value);
    if (
      !new Set(["http:", "https:"]).has(parsed.protocol) ||
      parsed.username !== "" ||
      parsed.password !== "" ||
      parsed.pathname !== "/" ||
      parsed.search !== "" ||
      parsed.hash !== ""
    ) {
      return false;
    }
    if (value === parsed.origin) return true;
    const defaultPort = parsed.protocol === "https:" ? "443" : "80";
    return value === `${parsed.protocol}//${parsed.hostname}:${defaultPort}`;
  } catch {
    return false;
  }
}

function validAbsoluteLocalPath(value: unknown): value is string {
  return typeof value === "string" && value.startsWith("/") && value.length <= 4_096 && value.trim() === value && !value.includes("\u0000");
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
      validIdentity(problem.title) &&
      validIdentity(problem.code) &&
      /^[a-z][a-z0-9_]*$/u.test(problem.code) &&
      problem.type ===
        `urn:vibermate:error:${problem.code.replaceAll("_", "-")}` &&
      (problem.operationId === undefined || validIdentity(problem.operationId))
    ) {
      return new ControlProblem(status, problem.code, `error.${problem.code}`);
    }
  } catch {
    // The stable fallback deliberately excludes the response payload.
  }
  throw new ControlContractError();
}

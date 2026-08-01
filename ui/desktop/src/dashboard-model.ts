import {
  ControlContractError,
  ControlProblem,
  type ControlClient,
} from "./control-client.ts";
import type {
  AccessApplyInput,
  AccessApplyResponse,
  AccessPlanSummary,
  ActivityRecord,
  ApprovalChoice,
  ApprovalView,
  ConnectionRecord,
  EgressAttemptRecord,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
} from "./control-types.ts";

export interface DashboardState {
  readonly loading: boolean;
  readonly busy: boolean;
  readonly status: StatusResponse | undefined;
  readonly offline: OfflineHoldSnapshot | undefined;
  readonly activities: readonly ActivityRecord[];
  readonly approvals: readonly ApprovalView[];
  readonly connections: readonly ConnectionRecord[];
  readonly egressAttempts: readonly EgressAttemptRecord[];
  readonly errorKey: string | undefined;
  readonly activeRevision: number | undefined;
  readonly credential: CredentialView | undefined;
}

type Listener = (state: DashboardState) => void;

export class DashboardModel {
  readonly #client: ControlClient;
  readonly #pollInterval: number;
  readonly #owner = new AbortController();
  readonly #listeners = new Set<Listener>();
  #state: DashboardState = {
    loading: true,
    busy: false,
    status: undefined,
    offline: undefined,
    activities: [],
    approvals: [],
    connections: [],
    egressAttempts: [],
    errorKey: undefined,
    activeRevision: undefined,
    credential: undefined,
  };
  #started = false;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #refreshing: Promise<void> | undefined;

  constructor(client: ControlClient, pollInterval = 2_000) {
    if (!Number.isFinite(pollInterval) || pollInterval <= 0) {
      throw new Error("Dashboard polling interval must be positive");
    }
    this.#client = client;
    this.#pollInterval = pollInterval;
  }

  snapshot(): DashboardState {
    return this.#state;
  }

  subscribe(listener: Listener): () => void {
    this.#listeners.add(listener);
    listener(this.#state);
    return () => {
      this.#listeners.delete(listener);
    };
  }

  start(): void {
    if (this.#started || this.#owner.signal.aborted) {
      return;
    }
    this.#started = true;
    void this.#poll();
  }

  stop(): void {
    this.#owner.abort();
    if (this.#timer !== undefined) {
      clearTimeout(this.#timer);
      this.#timer = undefined;
    }
    this.#listeners.clear();
  }

  async refresh(): Promise<void> {
    if (this.#refreshing !== undefined) {
      return this.#refreshing;
    }
    this.#refreshing = this.#runRefresh();
    try {
      await this.#refreshing;
    } finally {
      this.#refreshing = undefined;
    }
  }

  async enterOfflineHold(): Promise<void> {
    const revision = this.#state.offline?.revision;
    if (revision === undefined) {
      return;
    }
    await this.#mutation(async () => {
      await this.#client.enterOfflineHold(revision, this.#owner.signal);
    });
  }

  async resumeOfflineHold(): Promise<void> {
    const revision = this.#state.offline?.revision;
    if (revision === undefined) {
      return;
    }
    await this.#mutation(async () => {
      await this.#client.resumeOfflineHold(revision, this.#owner.signal);
    });
  }

  async decideApproval(
    approval: ApprovalView,
    choice: ApprovalChoice,
  ): Promise<void> {
    await this.#mutation(async () => {
      await this.#client.decideApproval(
        approval,
        choice,
        this.#owner.signal,
      );
    });
  }

  async applyAccess(
    accessId: string,
    input: AccessApplyInput,
  ): Promise<AccessApplyResponse | undefined> {
    let result: AccessApplyResponse | undefined;
    await this.#mutation(async () => {
      result = await this.#client.applyAccess(
        accessId,
        input,
        this.#owner.signal,
      );
      this.#setState({
        ...this.#state,
        activeRevision: result.revision,
        credential: undefined,
      });
      const profileId = input.profiles[0]?.id;
      const credentialId = input.accountBindings[0]?.id;
      if (profileId === undefined || credentialId === undefined) {
        throw new Error("Access input has no credential binding");
      }
      const credential = await this.#client.credential(
        accessId,
        profileId,
        credentialId,
        this.#owner.signal,
      );
      this.#setState({ ...this.#state, credential });
    });
    return result;
  }

  async loadAccess(
    accessId: string,
  ): Promise<AccessPlanSummary | undefined> {
    if (accessId.length === 0) {
      return undefined;
    }
    let result: AccessPlanSummary | undefined;
    await this.#mutation(async () => {
      const plan = await this.#client.accessPlan(
        accessId,
        this.#owner.signal,
      );
      result = plan;
      const credentialBinding = plan.accountBindings[0];
      if (credentialBinding === undefined) {
        throw new ControlContractError();
      }
      const credential = await this.#client.credential(
        accessId,
        credentialBinding.profileId,
        credentialBinding.id,
        this.#owner.signal,
      );
      this.#setState({
        ...this.#state,
        activeRevision: plan.revision,
        credential,
      });
    });
    return result;
  }

  async replaceCredentialSecret(
    accessId: string,
    profileId: string,
    credentialId: string,
    secret: string,
  ): Promise<CredentialView | undefined> {
    if (secret.length === 0) {
      return undefined;
    }
    let result: CredentialView | undefined;
    await this.#mutation(async () => {
      const current = await this.#client.credential(
        accessId,
        profileId,
        credentialId,
        this.#owner.signal,
      );
      result = await this.#client.replaceCredentialSecret(
        accessId,
        profileId,
        credentialId,
        current.secretRevision,
        secret,
        this.#owner.signal,
      );
      this.#setState({ ...this.#state, credential: result });
    });
    return result;
  }

  async #poll(): Promise<void> {
    await this.refresh();
    if (this.#owner.signal.aborted) {
      return;
    }
    this.#timer = setTimeout(() => {
      void this.#poll();
    }, this.#pollInterval);
  }

  async #runRefresh(): Promise<void> {
    try {
      const [status, offline, activities, approvals, connections, egress] =
        await Promise.all([
          this.#client.status(this.#owner.signal),
          this.#client.offlineHold(this.#owner.signal),
          this.#client.activities(this.#owner.signal),
          this.#client.approvals(this.#owner.signal),
          this.#client.connections(this.#owner.signal),
          this.#client.egressAttempts(this.#owner.signal),
        ]);
      this.#setState({
        ...this.#state,
        loading: false,
        status,
        offline,
        activities: [...activities.items],
        approvals: [...approvals.items],
        connections: [...connections.items],
        egressAttempts: [...egress.items],
        errorKey: undefined,
      });
    } catch (error) {
      if (!this.#owner.signal.aborted) {
        this.#setState({
          ...this.#state,
          loading: false,
          errorKey: errorKey(error),
        });
      }
    }
  }

  async #mutation(operation: () => Promise<void>): Promise<void> {
    if (this.#state.busy || this.#owner.signal.aborted) {
      return;
    }
    this.#setState({ ...this.#state, busy: true, errorKey: undefined });
    try {
      await operation();
      const inFlightRefresh = this.#refreshing;
      if (inFlightRefresh !== undefined) {
        await inFlightRefresh;
      }
      await this.refresh();
    } catch (error) {
      if (!this.#owner.signal.aborted) {
        this.#setState({ ...this.#state, errorKey: errorKey(error) });
      }
    } finally {
      if (!this.#owner.signal.aborted) {
        this.#setState({ ...this.#state, busy: false });
      }
    }
  }

  #setState(state: DashboardState): void {
    this.#state = state;
    for (const listener of this.#listeners) {
      listener(state);
    }
  }
}

function errorKey(error: unknown): string {
  if (error instanceof ControlContractError) {
    return error.messageKey;
  }
  if (
    error instanceof ControlProblem &&
    /^error\.[a-z0-9_.-]+$/u.test(error.messageKey)
  ) {
    return error.messageKey;
  }
  return "error.network_unavailable";
}

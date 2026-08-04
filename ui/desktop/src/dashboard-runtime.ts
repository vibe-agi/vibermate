import {
  QueryClient,
  skipToken,
  useMutation,
  useQueries,
  useQuery,
} from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ControlContractError,
  ControlProblem,
  type ControlClient,
} from "./control-client.ts";
import type {
  AccessDetail,
  AccessDirectoryPage,
  AccessAddCandidateInput,
  AccessAddCandidateResponse,
  AccessApplyInput,
  AccessApplyResponse,
  AccessPlanSummary,
  ActivityPage,
  ActivityRecord,
  ApprovalChoice,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  CredentialView,
  EgressAttemptRecord,
  ExchangeDetail,
  OfflineHoldSnapshot,
  StatusResponse,
  WorkspaceRouteBinding,
  WorkspaceRouteBindingPage,
} from "./control-types.ts";

export const dashboardSources = [
  "status",
  "offline",
  "activities",
  "approvals",
  "captureRuns",
  "connections",
  "egressAttempts",
] as const;

export type DashboardSource = (typeof dashboardSources)[number];

export interface DashboardState {
  readonly loading: boolean;
  readonly busy: boolean;
  readonly status: StatusResponse | undefined;
  readonly offline: OfflineHoldSnapshot | undefined;
  readonly activities: readonly ActivityRecord[];
  readonly activitiesHasMore: boolean;
  readonly activitiesLoadingMore: boolean;
  readonly activitiesLoadMoreErrorKey: string | undefined;
  readonly activitiesPagingSafetyStopped: boolean;
  readonly approvals: readonly ApprovalView[];
  readonly captureRuns: readonly CaptureRunRecord[];
  readonly connections: readonly ConnectionRecord[];
  readonly egressAttempts: readonly EgressAttemptRecord[];
  readonly errorKey: string | undefined;
  readonly refreshedAt: string | undefined;
  readonly refreshedAtBySource: Readonly<
    Partial<Record<DashboardSource, string>>
  >;
  readonly unavailableSources: readonly DashboardSource[];
}

export interface AccessCredentialCoordinates {
  readonly accessId: string;
  readonly profileId: string;
  readonly credentialId: string;
}

export interface AccessLoadResult {
  readonly detail: AccessDetail;
  readonly plan?: AccessPlanSummary;
}

export interface DashboardActions {
  readonly refresh: () => Promise<void>;
  readonly loadMoreActivities: () => Promise<void>;
  readonly enterOfflineHold: () => Promise<void>;
  readonly resumeOfflineHold: () => Promise<void>;
  readonly decideApproval: (
    approval: ApprovalView,
    choice: ApprovalChoice,
  ) => Promise<void>;
  readonly loadAccess: (
    accessId: string,
  ) => Promise<AccessLoadResult | undefined>;
  readonly addAccessCandidate: (
    accessId: string,
    expectedRevision: number,
    input: AccessAddCandidateInput,
  ) => Promise<AccessAddCandidateResponse | undefined>;
  readonly applyAccess: (
    accessId: string,
    input: AccessApplyInput,
  ) => Promise<AccessApplyResponse | undefined>;
  readonly replaceCredentialSecret: (
    coordinates: AccessCredentialCoordinates,
    secret: string,
  ) => Promise<CredentialView | undefined>;
  readonly selectAccessCandidate: (
    accessId: string,
    profileId: string,
    expectedRevision: number,
  ) => Promise<AccessApplyResponse | undefined>;
}

export interface DashboardRuntimeState {
  readonly actions: DashboardActions;
  readonly state: DashboardState;
}

export const dashboardQueryKeys = {
  root: ["vibermate", "dashboard"] as const,
  status: ["vibermate", "dashboard", "status"] as const,
  offline: ["vibermate", "dashboard", "offline"] as const,
  activities: ["vibermate", "dashboard", "activities"] as const,
  approvals: ["vibermate", "dashboard", "approvals"] as const,
  captureRuns: ["vibermate", "dashboard", "capture-runs"] as const,
  manualCaptures: ["vibermate", "dashboard", "manual-captures"] as const,
  connections: ["vibermate", "dashboard", "connections"] as const,
  egressAttempts: ["vibermate", "dashboard", "egress-attempts"] as const,
  workspaceRoutes: ["vibermate", "dashboard", "workspace-routes"] as const,
  accesses: ["vibermate", "dashboard", "accesses"] as const,
};

const mutationKey = ["vibermate", "control-command"] as const;
const activityPageMutationKey = [
  "vibermate",
  "dashboard",
  "activities",
  "load-more",
] as const;
let nextDashboardSessionKey = 1;
const accessPlanKey = (accessId: string) =>
  ["vibermate", "access", accessId, "plan"] as const;
const accessDetailKey = (accessId: string) =>
  ["vibermate", "access", accessId, "detail"] as const;
export const credentialQueryKey = ({
  accessId,
  credentialId,
  profileId,
}: AccessCredentialCoordinates) =>
  [
    "vibermate",
    "access",
    accessId,
    "profile",
    profileId,
    "credential",
    credentialId,
  ] as const;
export const exchangeDetailQueryKey = (exchangeId: string) =>
  ["vibermate", "exchange", exchangeId, "detail"] as const;

/**
 * Session-scoped dependencies for one authenticated Desktop control session.
 * Server snapshots live in this QueryClient; the model owns no parallel copy.
 */
export class DashboardQueryRuntime {
  readonly client: ControlClient;
  readonly pollInterval: number;
  readonly queryClient: QueryClient;
  readonly sessionKey: number;

  constructor(client: ControlClient, pollInterval = 2_000) {
    if (!Number.isFinite(pollInterval) || pollInterval <= 0) {
      throw new Error("Dashboard polling interval must be positive");
    }
    this.client = client;
    this.pollInterval = pollInterval;
    this.sessionKey = nextDashboardSessionKey++;
    this.queryClient = new QueryClient({
      defaultOptions: {
        mutations: {
          networkMode: "always",
          retry: false,
        },
        queries: {
          networkMode: "always",
          refetchOnReconnect: false,
          refetchOnWindowFocus: true,
          retry: false,
        },
      },
    });
  }

  async dispose(): Promise<void> {
    await this.queryClient.cancelQueries();
    this.queryClient.clear();
  }
}

type DashboardCommand =
  | { readonly kind: "enter-offline"; readonly revision: number }
  | { readonly kind: "resume-offline"; readonly revision: number }
  | {
      readonly kind: "decide-approval";
      readonly approval: ApprovalView;
      readonly choice: ApprovalChoice;
    }
  | { readonly kind: "load-access"; readonly accessId: string }
  | {
      readonly kind: "add-access-candidate";
      readonly accessId: string;
      readonly expectedRevision: number;
    }
  | {
      readonly kind: "apply-access";
      readonly accessId: string;
      readonly coordinates: AccessCredentialCoordinates;
    }
  | {
      readonly kind: "replace-credential";
      readonly coordinates: AccessCredentialCoordinates;
    }
  | {
      readonly kind: "select-access-candidate";
      readonly accessId: string;
      readonly expectedRevision: number;
      readonly profileId: string;
    };

interface TransientCommandInput {
  readonly accessCandidateInput?: AccessAddCandidateInput;
  readonly accessApplyInput?: AccessApplyInput;
  readonly credentialSecret?: string;
}

type DashboardCommandResult =
  | AccessAddCandidateResponse
  | AccessApplyResponse
  | AccessLoadResult
  | ApprovalView
  | CredentialView
  | OfflineHoldSnapshot;

export const maximumRetainedActivityRecords = 200;
const maximumRetainedActivityCursors = 64;

interface ActivityFeed {
  readonly generation: number;
  readonly nextCursor: string | undefined;
  readonly pendingCursor: string | undefined;
  readonly records: readonly ActivityRecord[];
  readonly requestedCursors: readonly string[];
  readonly stopped: boolean;
}

interface ActivityPageRequest {
  readonly cursor: string;
  readonly generation: number;
}

type ActivityFeedAction =
  | { readonly kind: "head"; readonly page: ActivityPage }
  | {
      readonly kind: "page";
      readonly page: ActivityPage;
      readonly request: ActivityPageRequest;
    }
  | { readonly kind: "page-failed"; readonly request: ActivityPageRequest }
  | { readonly kind: "page-started"; readonly request: ActivityPageRequest }
  | { readonly kind: "reset" };

function emptyActivityFeed(generation = 0): ActivityFeed {
  return {
    generation,
    nextCursor: undefined,
    pendingCursor: undefined,
    records: [],
    requestedCursors: [],
    stopped: false,
  };
}

function activityFeedFromHead(
  page: ActivityPage,
  generation: number,
): ActivityFeed {
  const records = mergeActivityRecords([page.items]).slice(
    0,
    maximumRetainedActivityRecords,
  );
  const stopped =
    records.length >= maximumRetainedActivityRecords &&
    page.nextCursor !== undefined;
  return {
    generation,
    nextCursor: stopped ? undefined : page.nextCursor,
    pendingCursor: undefined,
    records,
    requestedCursors: [],
    stopped,
  };
}

function reduceActivityFeed(
  current: ActivityFeed,
  action: ActivityFeedAction,
): ActivityFeed {
  switch (action.kind) {
    case "reset":
      return emptyActivityFeed(current.generation + 1);
    case "head": {
      if (current.records.length === 0) {
        return activityFeedFromHead(action.page, current.generation);
      }
      const visibleIds = new Set(current.records.map(({ id }) => id));
      const overlaps = action.page.items.some(({ id }) => visibleIds.has(id));
      if (!overlaps) {
        return activityFeedFromHead(action.page, current.generation + 1);
      }
      if (
        current.requestedCursors.length === 0 &&
        current.pendingCursor === undefined &&
        !current.stopped
      ) {
        return activityFeedFromHead(action.page, current.generation);
      }
      const merged = mergeActivityRecords([action.page.items, current.records]);
      const exceedsRecordLimit =
        merged.length > maximumRetainedActivityRecords;
      const stopped =
        current.stopped ||
        exceedsRecordLimit ||
        (merged.length >= maximumRetainedActivityRecords &&
          current.nextCursor !== undefined);
      return {
        ...current,
        nextCursor: stopped ? undefined : current.nextCursor,
        records: merged.slice(0, maximumRetainedActivityRecords),
        stopped,
      };
    }
    case "page-started":
      if (
        current.generation !== action.request.generation ||
        current.stopped ||
        current.nextCursor !== action.request.cursor ||
        current.pendingCursor !== undefined
      ) {
        return current;
      }
      return { ...current, pendingCursor: action.request.cursor };
    case "page-failed":
      return current.generation === action.request.generation &&
        current.pendingCursor === action.request.cursor
        ? { ...current, pendingCursor: undefined }
        : current;
    case "page": {
      if (
        current.generation !== action.request.generation ||
        current.pendingCursor !== action.request.cursor
      ) {
        return current;
      }
      if (current.stopped || current.nextCursor !== action.request.cursor) {
        return { ...current, pendingCursor: undefined };
      }
      if (current.requestedCursors.includes(action.request.cursor)) {
        return {
          ...current,
          nextCursor: undefined,
          pendingCursor: undefined,
          stopped: true,
        };
      }
      const requestedCursors = [
        ...current.requestedCursors,
        action.request.cursor,
      ];
      const merged = mergeActivityRecords([current.records, action.page.items]);
      const exceedsRecordLimit =
        merged.length > maximumRetainedActivityRecords;
      const cursorCycle =
        action.page.nextCursor !== undefined &&
        requestedCursors.includes(action.page.nextCursor);
      const stopped =
        exceedsRecordLimit ||
        cursorCycle ||
        (requestedCursors.length >= maximumRetainedActivityCursors &&
          action.page.nextCursor !== undefined) ||
        (merged.length >= maximumRetainedActivityRecords &&
          action.page.nextCursor !== undefined);
      return {
        ...current,
        nextCursor: stopped ? undefined : action.page.nextCursor,
        pendingCursor: undefined,
        records: merged.slice(0, maximumRetainedActivityRecords),
        requestedCursors,
        stopped,
      };
    }
  }
}

function queryDefaults(model: DashboardQueryRuntime) {
  return {
    networkMode: "always" as const,
    refetchInterval: model.pollInterval,
    refetchIntervalInBackground: false,
    refetchOnReconnect: false,
    refetchOnWindowFocus: true,
    retry: false,
    staleTime: model.pollInterval,
  };
}

export function useDashboardQueryRuntime(
  model: DashboardQueryRuntime,
): DashboardRuntimeState {
  const defaults = queryDefaults(model);
  const owner = useRef(new AbortController());
  const commandInFlight = useRef(false);
  const activityPageInFlight = useRef(false);
  const accessCandidateInput = useRef<AccessAddCandidateInput | undefined>(
    undefined,
  );
  const accessApplyInput = useRef<AccessApplyInput | undefined>(undefined);
  const credentialSecret = useRef("");
  const activityFeedRef = useRef<ActivityFeed>(emptyActivityFeed());
  const [activityFeed, setActivityFeed] = useState(activityFeedRef.current);
  const updateActivityFeed = useCallback((
    action: ActivityFeedAction,
  ): ActivityFeed => {
    const next = reduceActivityFeed(activityFeedRef.current, action);
    activityFeedRef.current = next;
    setActivityFeed(next);
    return next;
  }, []);
  const activitiesQuery = useQuery({
    ...defaults,
    queryFn: ({ signal }) => model.client.activities(undefined, signal),
    queryKey: dashboardQueryKeys.activities,
  });
  const queries = useQueries({
    queries: [
      {
        ...defaults,
        queryFn: ({ signal }) => model.client.status(signal),
        queryKey: dashboardQueryKeys.status,
      },
      {
        ...defaults,
        queryFn: ({ signal }) => model.client.offlineHold(signal),
        queryKey: dashboardQueryKeys.offline,
      },
      {
        ...defaults,
        queryFn: async ({ signal }) => [
          ...(await model.client.approvals(signal)).items,
        ],
        queryKey: dashboardQueryKeys.approvals,
      },
      {
        ...defaults,
        queryFn: async ({ signal }) => [
          ...(await model.client.captureRuns(signal)).items,
        ],
        queryKey: dashboardQueryKeys.captureRuns,
      },
      {
        ...defaults,
        queryFn: async ({ signal }) => [
          ...(await model.client.connections(signal)).items,
        ],
        queryKey: dashboardQueryKeys.connections,
      },
      {
        ...defaults,
        queryFn: async ({ signal }) => [
          ...(await model.client.egressAttempts(signal)).items,
        ],
        queryKey: dashboardQueryKeys.egressAttempts,
      },
    ],
  });
  const [
    statusQuery,
    offlineQuery,
    approvalsQuery,
    captureRunsQuery,
    connectionsQuery,
    egressAttemptsQuery,
  ] = queries;

  useEffect(() => {
    updateActivityFeed({ kind: "reset" });
  }, [model, updateActivityFeed]);

  useEffect(() => {
    if (activitiesQuery.data !== undefined) {
      updateActivityFeed({ kind: "head", page: activitiesQuery.data });
    }
  }, [activitiesQuery.data, activitiesQuery.dataUpdatedAt, updateActivityFeed]);

  useEffect(() => {
    if (owner.current.signal.aborted) {
      owner.current = new AbortController();
    }
    const currentOwner = owner.current;
    return () => {
      accessCandidateInput.current = undefined;
      accessApplyInput.current = undefined;
      credentialSecret.current = "";
      currentOwner.abort();
      void model.queryClient.cancelQueries();
    };
  }, [model]);

  const activityPage = useMutation({
    gcTime: 0,
    mutationFn: ({ cursor }: ActivityPageRequest) =>
      model.client.activities(cursor, owner.current.signal),
    mutationKey: activityPageMutationKey,
    onError: (_error, request) => {
      updateActivityFeed({ kind: "page-failed", request });
    },
    onSuccess: (page, request) => {
      updateActivityFeed({ kind: "page", page, request });
    },
    retry: false,
  });

  const command = useMutation({
    mutationFn: (input: DashboardCommand) => {
      const transientAccessCandidateInput = accessCandidateInput.current;
      const transientAccessApplyInput = accessApplyInput.current;
      const transientCredentialSecret = credentialSecret.current;
      accessCandidateInput.current = undefined;
      accessApplyInput.current = undefined;
      credentialSecret.current = "";
      return executeCommand(
        model,
        input,
        owner.current.signal,
        transientAccessCandidateInput,
        transientAccessApplyInput,
        transientCredentialSecret,
      );
    },
    gcTime: 0,
    mutationKey,
    onSuccess: (result, input) => {
      switch (input.kind) {
        case "enter-offline":
        case "resume-offline":
          model.queryClient.setQueryData(
            dashboardQueryKeys.offline,
            result as OfflineHoldSnapshot,
          );
          invalidateQueriesInBackground(model.queryClient, [
            dashboardQueryKeys.status,
            dashboardQueryKeys.activities,
            dashboardQueryKeys.connections,
            dashboardQueryKeys.egressAttempts,
          ]);
          break;
        case "decide-approval":
          model.queryClient.setQueryData<readonly ApprovalView[]>(
            dashboardQueryKeys.approvals,
            (current) =>
              current?.filter(
                (approval) => approval.id !== input.approval.id,
              ) ?? [],
          );
          invalidateQueriesInBackground(model.queryClient, [
            dashboardQueryKeys.approvals,
            dashboardQueryKeys.activities,
            dashboardQueryKeys.connections,
            dashboardQueryKeys.offline,
            dashboardQueryKeys.egressAttempts,
          ]);
          break;
        case "apply-access":
        case "add-access-candidate":
        case "select-access-candidate":
          void Promise.all([
            model.queryClient.invalidateQueries({
              queryKey: dashboardQueryKeys.accesses,
              refetchType: "active",
            }),
            model.queryClient.invalidateQueries({
              queryKey: accessPlanKey(input.accessId),
              refetchType: "active",
            }),
            model.queryClient.invalidateQueries({
              queryKey: accessDetailKey(input.accessId),
              refetchType: "active",
            }),
            invalidateQueries(model.queryClient, [
              dashboardQueryKeys.status,
              dashboardQueryKeys.activities,
            ]),
          ]).catch(() => undefined);
          if (input.kind === "apply-access") {
            void model.queryClient.invalidateQueries({
              queryKey: credentialQueryKey(input.coordinates),
              refetchType: "active",
            });
          }
          break;
        case "replace-credential":
          invalidateQueriesInBackground(model.queryClient, [
            dashboardQueryKeys.status,
            dashboardQueryKeys.activities,
          ]);
          break;
        case "load-access":
          break;
      }
    },
    retry: false,
  });

  const runCommand = useCallback(
    async <Result>(
      input: DashboardCommand,
      transient: TransientCommandInput = {},
    ): Promise<Result | undefined> => {
      if (commandInFlight.current || owner.current.signal.aborted) {
        return undefined;
      }
      commandInFlight.current = true;
      accessCandidateInput.current = transient.accessCandidateInput;
      accessApplyInput.current = transient.accessApplyInput;
      credentialSecret.current = transient.credentialSecret ?? "";
      try {
        return (await command.mutateAsync(input)) as Result;
      } catch {
        return undefined;
      } finally {
        accessCandidateInput.current = undefined;
        accessApplyInput.current = undefined;
        credentialSecret.current = "";
        commandInFlight.current = false;
      }
    },
    [command],
  );

  const loadMoreActivities = useCallback(async (): Promise<void> => {
    if (activityPageInFlight.current || owner.current.signal.aborted) {
      return;
    }
    const current = activityFeedRef.current;
    if (current.stopped || current.nextCursor === undefined) {
      return;
    }
    const request: ActivityPageRequest = {
      cursor: current.nextCursor,
      generation: current.generation,
    };
    const armed = updateActivityFeed({ kind: "page-started", request });
    if (armed.pendingCursor !== request.cursor) {
      return;
    }
    activityPageInFlight.current = true;
    try {
      await activityPage.mutateAsync(request);
    } catch {
      // The rendered tail remains usable and exposes a retryable page error.
    } finally {
      activityPageInFlight.current = false;
    }
  }, [activityPage, updateActivityFeed]);

  const actions = useMemo<DashboardActions>(
    () => ({
      addAccessCandidate: (accessId, expectedRevision, input) =>
        runCommand<AccessAddCandidateResponse>(
          {
            accessId,
            expectedRevision,
            kind: "add-access-candidate",
          },
          { accessCandidateInput: input },
        ),
      applyAccess: (accessId, input) => {
        const profileId = input.profiles[0]?.id;
        const credentialId = input.accountBindings[0]?.id;
        if (profileId === undefined || credentialId === undefined) {
          return Promise.resolve(undefined);
        }
        return runCommand<AccessApplyResponse>(
          {
            accessId,
            coordinates: { accessId, credentialId, profileId },
            kind: "apply-access",
          },
          { accessApplyInput: input },
        );
      },
      decideApproval: async (approval, choice) => {
        await runCommand<ApprovalView>({
          approval,
          choice,
          kind: "decide-approval",
        });
      },
      enterOfflineHold: async () => {
        const revision = model.queryClient.getQueryData<OfflineHoldSnapshot>(
          dashboardQueryKeys.offline,
        )?.revision;
        if (revision !== undefined) {
          await runCommand<OfflineHoldSnapshot>({
            kind: "enter-offline",
            revision,
          });
        }
      },
      loadAccess: (accessId) =>
        accessId.length === 0
          ? Promise.resolve(undefined)
          : runCommand<AccessLoadResult>({ accessId, kind: "load-access" }),
      loadMoreActivities,
      refresh: async () => {
        command.reset();
        if (!activityPageInFlight.current) {
          activityPage.reset();
        }
        await model.queryClient.refetchQueries({
          queryKey: dashboardQueryKeys.root,
          type: "active",
        });
      },
      replaceCredentialSecret: (coordinates, secret) =>
        secret.length === 0
          ? Promise.resolve(undefined)
          : runCommand<CredentialView>(
              {
                coordinates,
                kind: "replace-credential",
              },
              { credentialSecret: secret },
            ),
      selectAccessCandidate: (accessId, profileId, expectedRevision) =>
        runCommand<AccessApplyResponse>({
          accessId,
          expectedRevision,
          kind: "select-access-candidate",
          profileId,
        }),
      resumeOfflineHold: async () => {
        const revision = model.queryClient.getQueryData<OfflineHoldSnapshot>(
          dashboardQueryKeys.offline,
        )?.revision;
        if (revision !== undefined) {
          await runCommand<OfflineHoldSnapshot>({
            kind: "resume-offline",
            revision,
          });
        }
      },
    }),
    [activityPage, command, loadMoreActivities, model, runCommand],
  );

  const sourceQueries = [
    { query: statusQuery, source: "status" },
    { query: offlineQuery, source: "offline" },
    { query: activitiesQuery, source: "activities" },
    { query: approvalsQuery, source: "approvals" },
    { query: captureRunsQuery, source: "captureRuns" },
    { query: connectionsQuery, source: "connections" },
    { query: egressAttemptsQuery, source: "egressAttempts" },
  ] as const;
  const loading = sourceQueries.some(({ query }) => query.isPending);
  const unavailableSources = sourceQueries
    .filter(({ query }) => query.error !== null)
    .map(({ source }) => source);
  const refreshedAtBySource: Partial<Record<DashboardSource, string>> = {};
  for (const { query, source } of sourceQueries) {
    if (query.dataUpdatedAt > 0) {
      refreshedAtBySource[source] = new Date(query.dataUpdatedAt).toISOString();
    }
  }
  const refreshedAtMilliseconds = Math.max(
    0,
    ...sourceQueries.map(({ query }) => query.dataUpdatedAt),
  );
  const queryError = sourceQueries.find(({ query }) => query.error !== null)
    ?.query.error;
  const state = useMemo<DashboardState>(
    () => ({
      activities: activityFeed.records,
      activitiesHasMore:
        !activityFeed.stopped && activityFeed.nextCursor !== undefined,
      activitiesLoadingMore: activityPage.isPending,
      activitiesLoadMoreErrorKey:
        activityPage.error === null
          ? undefined
          : controlErrorKey(activityPage.error),
      activitiesPagingSafetyStopped: activityFeed.stopped,
      approvals: approvalsQuery.data ?? [],
      busy: command.isPending,
      captureRuns: captureRunsQuery.data ?? [],
      connections: connectionsQuery.data ?? [],
      egressAttempts: egressAttemptsQuery.data ?? [],
      errorKey:
        command.error !== null
          ? controlErrorKey(command.error)
          : unavailableSources.length === 0
            ? undefined
            : unavailableSources.length === dashboardSources.length
              ? controlErrorKey(queryError)
              : "error.dashboard_partial",
      loading,
      offline: offlineQuery.data,
      refreshedAt:
        refreshedAtMilliseconds === 0
          ? undefined
          : new Date(refreshedAtMilliseconds).toISOString(),
      refreshedAtBySource,
      status: statusQuery.data,
      unavailableSources,
    }),
    [
      activityFeed,
      activityPage.error,
      activityPage.isPending,
      approvalsQuery.data,
      captureRunsQuery.data,
      command.error,
      command.isPending,
      connectionsQuery.data,
      egressAttemptsQuery.data,
      loading,
      offlineQuery.data,
      queryError,
      refreshedAtBySource,
      refreshedAtMilliseconds,
      statusQuery.data,
      unavailableSources,
    ],
  );
  return { actions, state };
}

function mergeActivityRecords(
  collections: readonly (readonly ActivityRecord[])[],
): readonly ActivityRecord[] {
  const records = new Map<
    string,
    { readonly record: ActivityRecord; readonly position: number }
  >();
  let position = 0;
  for (const collection of collections) {
    for (const record of collection) {
      if (!records.has(record.id)) {
        records.set(record.id, { position, record });
      }
      position += 1;
    }
  }
  return [...records.values()]
    .sort(
      (left, right) =>
        Date.parse(right.record.occurredAt) -
          Date.parse(left.record.occurredAt) || left.position - right.position,
    )
    .map(({ record }) => record);
}

export function useCredentialMetadata(
  model: DashboardQueryRuntime,
  coordinates: AccessCredentialCoordinates | undefined,
) {
  const defaults = queryDefaults(model);
  return useQuery({
    ...defaults,
    queryFn:
      coordinates === undefined
        ? skipToken
        : ({ signal }) =>
            model.client.credential(
              coordinates.accessId,
              coordinates.profileId,
              coordinates.credentialId,
              signal,
            ),
    queryKey:
      coordinates === undefined
        ? (["vibermate", "access", "credential", "inactive"] as const)
        : credentialQueryKey(coordinates),
    refetchInterval: coordinates === undefined ? false : model.pollInterval,
  });
}

export function useAccessDirectory(model: DashboardQueryRuntime) {
  const defaults = queryDefaults(model);
  return useQuery<AccessDirectoryPage>({
    ...defaults,
    queryFn: ({ signal }) => model.client.accesses(signal),
    queryKey: dashboardQueryKeys.accesses,
  });
}

interface WorkspaceRouteUpdate {
  readonly binding: WorkspaceRouteBinding;
  readonly profileId: string;
}

export function useWorkspaceRoutes(model: DashboardQueryRuntime) {
  const defaults = queryDefaults(model);
  const owner = useRef(new AbortController());
  const list = model.client.workspaceRouteBindings;
  const updateBinding = model.client.updateWorkspaceRouteBinding;
  useEffect(() => {
    if (owner.current.signal.aborted) {
      owner.current = new AbortController();
    }
    const current = owner.current;
    return () => current.abort();
  }, [model]);
  const query = useQuery<WorkspaceRouteBindingPage>({
    ...defaults,
    queryFn:
      list === undefined
        ? skipToken
        : ({ signal }) => list.call(model.client, signal),
    queryKey: dashboardQueryKeys.workspaceRoutes,
  });
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: async ({ binding, profileId }: WorkspaceRouteUpdate) => {
      if (updateBinding === undefined) {
        throw new ControlContractError();
      }
      return updateBinding.call(
        model.client,
        binding.id,
        binding.revision,
        profileId,
        owner.current.signal,
      );
    },
    onSuccess: (updated) => {
      model.queryClient.setQueryData<WorkspaceRouteBindingPage>(
        dashboardQueryKeys.workspaceRoutes,
        (current) => ({
          items:
            current?.items.map((item) =>
              item.id === updated.id ? updated : item,
            ) ?? [updated],
        }),
      );
    },
    retry: false,
  });
  const update = useCallback(
    async (
      binding: WorkspaceRouteBinding,
      profileId: string,
    ): Promise<boolean> => {
      if (
        mutation.isPending ||
        profileId === binding.profileId ||
        !binding.approvedProfiles.some(
          (profile) => profile.profileId === profileId && profile.available,
        )
      ) {
        return false;
      }
      try {
        await mutation.mutateAsync({ binding, profileId });
        return true;
      } catch {
        return false;
      }
    },
    [mutation],
  );
  return {
    enabled: list !== undefined && updateBinding !== undefined,
    errorKey:
      query.error !== null
        ? controlErrorKey(query.error)
        : mutation.error !== null
          ? controlErrorKey(mutation.error)
          : undefined,
    items: query.data?.items ?? [],
    loading: query.isPending,
    pending: mutation.isPending,
    update,
  };
}

/** A completed Exchange is immutable, so detail reads do not join the global
 * polling loop. Window focus or an explicit retry can still revalidate it.
 */
export function useExchangeDetail(
  model: DashboardQueryRuntime,
  exchangeId: string,
) {
  const defaults = queryDefaults(model);
  return useQuery<ExchangeDetail>({
    ...defaults,
    queryFn: ({ signal }) => model.client.exchange(exchangeId, signal),
    queryKey: exchangeDetailQueryKey(exchangeId),
    refetchInterval: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

async function executeCommand(
  model: DashboardQueryRuntime,
  input: DashboardCommand,
  signal: AbortSignal,
  accessCandidateInput: AccessAddCandidateInput | undefined,
  accessApplyInput: AccessApplyInput | undefined,
  credentialSecret: string,
): Promise<DashboardCommandResult> {
  switch (input.kind) {
    case "enter-offline":
      return model.client.enterOfflineHold(input.revision, signal);
    case "resume-offline":
      return model.client.resumeOfflineHold(input.revision, signal);
    case "decide-approval":
      return model.client.decideApproval(input.approval, input.choice, signal);
    case "load-access": {
      const detail = await model.queryClient.fetchQuery({
        networkMode: "always",
        queryFn: ({ signal: querySignal }) =>
          model.client.access(input.accessId, querySignal),
        queryKey: accessDetailKey(input.accessId),
        retry: false,
        staleTime: 0,
      });
      if (detail.access.status !== "enabled") {
        return { detail };
      }
      try {
        const plan = await model.queryClient.fetchQuery({
          networkMode: "always",
          queryFn: ({ signal: querySignal }) =>
            model.client.accessPlan(input.accessId, querySignal),
          queryKey: accessPlanKey(input.accessId),
          retry: false,
          staleTime: 0,
        });
        const detailProfileIds = new Set(
          detail.profiles.map(({ id }) => id),
        );
        const detailBindings = new Map(
          detail.accountBindings.map((binding) => [binding.id, binding]),
        );
        if (
          plan.revision !== detail.revision ||
          plan.profiles.length !== detailProfileIds.size ||
          plan.profiles.some((profileId) => !detailProfileIds.has(profileId)) ||
          plan.accountBindings.length !== detailBindings.size ||
          plan.accountBindings.some(({ id, profileId }) => {
            const binding = detailBindings.get(id);
            return binding === undefined || binding.profileId !== profileId;
          })
        ) {
          throw new ControlContractError();
        }
        return { detail, plan };
      } catch {
        // Durable configuration is still useful when the active projection is
        // unavailable. Do not turn a saved draft/detail into a missing Access.
        return { detail };
      }
    }
    case "add-access-candidate": {
      if (accessCandidateInput === undefined) {
        throw new Error("Access candidate input is missing");
      }
      return model.client.addAccessCandidate(
        input.accessId,
        input.expectedRevision,
        accessCandidateInput,
        signal,
      );
    }
    case "apply-access": {
      if (accessApplyInput === undefined) {
        throw new Error("Access apply input is missing");
      }
      const result = await model.client.applyAccess(
        input.accessId,
        accessApplyInput,
        signal,
      );
      return result;
    }
    case "replace-credential": {
      if (credentialSecret.length === 0) {
        throw new Error("Credential replacement secret is missing");
      }
      const current = await fetchCredential(model, input.coordinates);
      const credential = await model.client.replaceCredentialSecret(
        input.coordinates.accessId,
        input.coordinates.profileId,
        input.coordinates.credentialId,
        current.secretRevision,
        credentialSecret,
        signal,
      );
      model.queryClient.setQueryData(
        credentialQueryKey(input.coordinates),
        credential,
      );
      return credential;
    }
    case "select-access-candidate":
      return model.client.selectAccessCandidate(
        input.accessId,
        input.profileId,
        input.expectedRevision,
        signal,
      );
  }
}

function fetchCredential(
  model: DashboardQueryRuntime,
  coordinates: AccessCredentialCoordinates,
): Promise<CredentialView> {
  return model.queryClient.fetchQuery({
    networkMode: "always",
    queryFn: ({ signal }) =>
      model.client.credential(
        coordinates.accessId,
        coordinates.profileId,
        coordinates.credentialId,
        signal,
      ),
    queryKey: credentialQueryKey(coordinates),
    retry: false,
    staleTime: 0,
  });
}

function invalidateQueries(
  queryClient: QueryClient,
  queryKeys: readonly (readonly unknown[])[],
): Promise<void> {
  return Promise.all(
    queryKeys.map((queryKey) =>
      queryClient.invalidateQueries({ queryKey, refetchType: "active" }),
    ),
  ).then(() => undefined);
}

function invalidateQueriesInBackground(
  queryClient: QueryClient,
  queryKeys: readonly (readonly unknown[])[],
): void {
  void invalidateQueries(queryClient, queryKeys).catch(() => undefined);
}

export function controlErrorKey(error: unknown): string {
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

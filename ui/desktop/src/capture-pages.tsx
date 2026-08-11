import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  EmptyState,
  InlineProblem,
  LoadingRows,
  PageHeading,
  SectionHeading,
  useDashboardModel,
} from "./App.tsx";
import { BrandIcon } from "./brand-icons.tsx";
import { controlErrorKey, dashboardQueryKeys } from "./dashboard-runtime.ts";
import { dashboardRoutePaths, dashboardTaskRoutePaths } from "./navigation.ts";
import { requestResultKey } from "./request-result.ts";
import type {
  ActivityRecord,
  CaptureRecord,
  EnvironmentRecord,
  ManualCaptureContext,
  ManualCaptureGrant,
} from "./control-types.ts";

export function CapturesRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const [creating, setCreating] = useState(false);
  const captures = useQuery({
    queryKey: dashboardQueryKeys.captures,
    queryFn: ({ signal }) => model.client.captures(signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  const environments = useQuery({
    queryKey: dashboardQueryKeys.environments,
    queryFn: ({ signal }) => model.client.environments(signal),
    placeholderData: (previous) => previous,
  });
  const records = captures.data?.items ?? [];
  const running = records.filter(isLiveCapture).sort(compareCaptureActivity);
  const history = records.filter((capture) => !isLiveCapture(capture)).sort(compareCaptureActivity);

  return (
    <div className="page capture-page">
      <PageHeading
        actions={
          <div className="page-actions">
            <Link className="quiet-button" search={{}} to={dashboardTaskRoutePaths.captureRequests}>
              {t("captures.conversations")}
            </Link>
            <button className="primary-action" onClick={() => setCreating(true)} type="button">
              {t("captures.add")}
            </button>
          </div>
        }
        description={t("captures.description")}
        title={t("captures.title")}
      />
      {captures.isPending && captures.data === undefined ? (
        <section className="data-panel"><LoadingRows /></section>
      ) : captures.isError && captures.data === undefined ? (
        <section className="data-panel"><InlineProblem message={t(controlErrorKey(captures.error))} /></section>
      ) : records.length === 0 ? (
        <section className="data-panel">
          <EmptyState
            action={<button onClick={() => setCreating(true)} type="button">{t("captures.add")}</button>}
            description={t("captures.empty.description")}
            title={t("captures.empty.title")}
          />
        </section>
      ) : (
        <>
          <CaptureSection
            emptyDescription={t("captures.running.empty")}
            environments={environments.data?.items ?? []}
            kind="running"
            records={running}
            title={t("captures.running.title", { count: running.length })}
          />
          <CaptureSection
            environments={environments.data?.items ?? []}
            kind="history"
            records={history}
            title={t("captures.history.title", { count: history.length })}
          />
        </>
      )}

      {creating && (
        <ManualCaptureDialog
          environments={environments.data?.items ?? []}
          onClose={() => setCreating(false)}
        />
      )}
    </div>
  );
}

function CaptureSection({
  emptyDescription,
  environments,
  kind,
  records,
  title,
}: {
  readonly emptyDescription?: string;
  readonly environments: readonly EnvironmentRecord[];
  readonly kind: "running" | "history";
  readonly records: readonly CaptureRecord[];
  readonly title: string;
}) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");
  const [visibleLimit, setVisibleLimit] = useState(25);
  const normalizedFilter = filter.trim().toLocaleLowerCase();
  const matchedRecords = kind === "history" && normalizedFilter.length > 0
    ? records.filter((capture) => captureMatches(capture, normalizedFilter))
    : records;
  const visibleRecords = kind === "history"
    ? matchedRecords.slice(0, visibleLimit)
    : matchedRecords;
  const sectionAction = kind === "history" && records.length > 8
    ? <label className="table-filter">
        <SearchIcon />
        <span className="visually-hidden">{t("captures.history.filter")}</span>
        <input
          aria-label={t("captures.history.filter")}
          onChange={(event) => {
            setFilter(event.target.value);
            setVisibleLimit(25);
          }}
          placeholder={t("captures.history.filter")}
          type="search"
          value={filter}
        />
      </label>
    : undefined;
  return (
    <section aria-label={title} className={`data-panel capture-table-panel capture-table-panel-${kind}`}>
      <SectionHeading action={sectionAction} title={title} />
      {records.length === 0 ? (
        <p className="capture-section-empty">{emptyDescription ?? t("captures.history.empty")}</p>
      ) : matchedRecords.length === 0 ? (
        <p className="capture-section-empty">{t("captures.history.noMatch")}</p>
      ) : (
        <>
          <div className="table-scroll">
            <table className="data-table responsive-table capture-table">
              <thead>
                <tr>
                  <th>{t("captures.column.state")}</th>
                  <th>{t("captures.column.agent")}</th>
                  <th>{t("captures.column.workspace")}</th>
                  <th>{t("captures.column.machine")}</th>
                  <th>{t("captures.column.environment")}</th>
                  <th>{t("captures.column.observation")}</th>
                  <th className="align-right">{t("captures.column.updated")}</th>
                </tr>
              </thead>
              <tbody>
                {visibleRecords.map((capture) => (
                  <CaptureRow capture={capture} environments={environments} key={capture.key} />
                ))}
              </tbody>
            </table>
          </div>
          {kind === "history" && (records.length > 25 || filter.length > 0) && (
            <footer className="table-pagination">
              <span>{t("captures.history.showing", { visible: visibleRecords.length, total: matchedRecords.length })}</span>
              {visibleRecords.length < matchedRecords.length && (
                <button className="quiet-button" onClick={() => setVisibleLimit((value) => value + 25)} type="button">
                  {t("captures.history.more")}
                </button>
              )}
            </footer>
          )}
        </>
      )}
    </section>
  );
}

function CaptureRow({
  capture,
  environments,
}: {
  readonly capture: CaptureRecord;
  readonly environments: readonly EnvironmentRecord[];
}) {
  const { t, i18n } = useTranslation();
  const model = useDashboardModel();
  const assignment = useQuery({
    queryKey: dashboardQueryKeys.captureAssignment(capture.key),
    queryFn: ({ signal }) => model.client.captureAssignment(capture.key, signal),
    placeholderData: (previous) => previous,
  });
  const environment = environments.find((item) => item.id === assignment.data?.environmentId);
  const managed = capture.managedRun;
  const workspace = managed?.workspaceLabel ?? shortPath(managed?.cwd) ?? t("captures.value.manual");
  const machine = managed?.machineId?.slice(0, 10) ?? t("captures.value.local");
  const icon = captureBrand(capture);

  return (
    <tr>
      <td data-label={t("captures.column.state")}><StatePill label={t(`captures.state.${capture.state}`)} state={capture.state} /></td>
      <td data-label={t("captures.column.agent")}>
        <Link
          className="row-link agent-cell"
          params={{ captureKey: capture.key }}
          search={{}}
          to={dashboardTaskRoutePaths.captureDetail}
        >
          {icon !== undefined && <BrandIcon name={icon} />}
          <span>
            <strong>{capture.displayName}</strong>
            <small>{capture.kind === "managed_run" ? t("captures.kind.managed") : t("captures.kind.manual")}</small>
          </span>
        </Link>
      </td>
      <td data-label={t("captures.column.workspace")} title={managed?.cwd}>{workspace}</td>
      <td data-label={t("captures.column.machine")} title={managed?.machineId}>{machine}</td>
      <td data-label={t("captures.column.environment")}>{environment?.name ?? assignment.data?.environmentId ?? t("common.unavailable")}</td>
      <td data-label={t("captures.column.observation")}>{t(`captures.observation.${capture.observation}`)}</td>
      <td className="align-right" data-label={t("captures.column.updated")}><time dateTime={capture.updatedAt}>{formatRelative(capture.updatedAt, i18n.language)}</time></td>
    </tr>
  );
}

export function CaptureDetailRoutePage({ captureKey }: { readonly captureKey: string }) {
  const { t, i18n } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [switchNotice, setSwitchNotice] = useState<string>();
  const [confirmingRevoke, setConfirmingRevoke] = useState(false);
  const [revokeNotice, setRevokeNotice] = useState<{
    readonly kind: "error" | "status";
    readonly key: string;
  }>();
  const capture = useQuery({
    queryKey: dashboardQueryKeys.capture(captureKey),
    queryFn: ({ signal }) => model.client.capture(captureKey, signal),
  });
  const assignment = useQuery({
    queryKey: dashboardQueryKeys.captureAssignment(captureKey),
    queryFn: ({ signal }) => model.client.captureAssignment(captureKey, signal),
  });
  const environments = useQuery({
    queryKey: dashboardQueryKeys.environments,
    queryFn: ({ signal }) => model.client.environments(signal),
  });
  const manualCaptureId = capture.data?.kind === "manual_capture"
    ? capture.data.id
    : undefined;
  const manualAuthority = useQuery({
    queryKey: dashboardQueryKeys.manualCapture(manualCaptureId ?? "unavailable"),
    queryFn: ({ signal }) => {
      if (manualCaptureId === undefined) {
        throw new Error("Manual Capture identity is unavailable");
      }
      return model.client.manualCapture(manualCaptureId, signal);
    },
    enabled: manualCaptureId !== undefined,
    refetchInterval: (query) => query.state.data?.capture.state === "active"
      ? model.pollInterval
      : false,
  });
  const machineId = capture.data?.managedRun?.machineId;
  const workspaceId = capture.data?.managedRun?.workspaceId;
  const workspaceDefault = useQuery({
    queryKey: dashboardQueryKeys.workspaceDefault(machineId ?? "", workspaceId ?? ""),
    queryFn: async ({ signal }) =>
      (await model.client.workspaceEnvironmentDefault(
        machineId ?? "",
        workspaceId ?? "",
        signal,
      )) ?? null,
    enabled: machineId !== undefined && workspaceId !== undefined,
  });
  const activities = useQuery({
    queryKey: [...dashboardQueryKeys.capture(captureKey), "activities"],
    queryFn: ({ signal }) =>
      model.client.activities(
        capture.data?.kind === "managed_run"
          ? { captureRunId: capture.data.id }
          : capture.data?.kind === "manual_capture"
            ? { manualCaptureId: capture.data.id }
            : undefined,
        signal,
      ),
    enabled: capture.data !== undefined,
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  const switchEnvironment = useMutation({
    mutationFn: (environmentId: string) => {
      if (assignment.data === undefined) {
        throw new Error("Capture assignment is unavailable");
      }
      return model.client.switchCaptureEnvironment(
        captureKey,
        assignment.data.revision,
        environmentId,
      );
    },
    onError: (error) => setSwitchNotice(controlErrorKey(error)),
    onSuccess: (result) => {
      if (!result.applied) {
        setSwitchNotice("captures.switch.restartRequired");
        return;
      }
      setSwitchNotice(
        result.boundary === "reconnect_required"
          ? "captures.switch.reconnected"
          : "captures.switch.applied",
      );
      queryClient.setQueryData(
        dashboardQueryKeys.captureAssignment(captureKey),
        result.assignment,
      );
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.captures });
    },
  });
  const updateWorkspaceDefault = useMutation({
    mutationFn: async () => {
      if (machineId === undefined || workspaceId === undefined || assignment.data === undefined) {
        throw new Error("Workspace identity is unavailable");
      }
      if (workspaceDefault.data?.environmentId === assignment.data.environmentId) {
        await model.client.clearWorkspaceEnvironmentDefault(
          machineId,
          workspaceId,
          workspaceDefault.data.revision,
        );
        return undefined;
      }
      return model.client.setWorkspaceEnvironmentDefault(
        machineId,
        workspaceId,
        workspaceDefault.data?.revision ?? 0,
        assignment.data.environmentId,
      );
    },
    onError: (error) => setSwitchNotice(controlErrorKey(error)),
    onSuccess: (record) => {
      if (machineId !== undefined && workspaceId !== undefined) {
        queryClient.setQueryData(
          dashboardQueryKeys.workspaceDefault(machineId, workspaceId),
          record ?? null,
        );
      }
      setSwitchNotice(
        record === undefined
          ? "captureDetail.environment.defaultCleared"
          : "captureDetail.environment.defaultSaved",
      );
    },
  });
  const revokeManualCapture = useMutation({
    mutationFn: async () => {
      if (manualCaptureId === undefined || manualAuthority.data === undefined) {
        throw new Error("Manual Capture authority is unavailable");
      }
      await model.client.revokeManualCapture(
        manualCaptureId,
        manualAuthority.data.stateTag,
      );
    },
    onMutate: () => setRevokeNotice(undefined),
    onError: async (error) => {
      setConfirmingRevoke(false);
      setRevokeNotice({ kind: "error", key: controlErrorKey(error) });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.captures }),
        queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.capture(captureKey) }),
        ...(manualCaptureId === undefined
          ? []
          : [queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.manualCapture(manualCaptureId) })]),
      ]);
    },
    onSuccess: async () => {
      setConfirmingRevoke(false);
      setRevokeNotice({ kind: "status", key: "manualCapture.revoke.succeeded" });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.captures }),
        queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.capture(captureKey) }),
        ...(manualCaptureId === undefined
          ? []
          : [queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.manualCapture(manualCaptureId) })]),
      ]);
    },
  });

  if (capture.isPending || assignment.isPending || environments.isPending) {
    return <div className="page"><LoadingRows count={7} /></div>;
  }
  const detailError = capture.error ?? assignment.error ?? environments.error;
  if (detailError !== null) {
    return (
      <div className="page">
        <InlineProblem message={t(controlErrorKey(detailError))} />
      </div>
    );
  }
  if (capture.data === undefined || assignment.data === undefined || environments.data === undefined) {
    return <div className="page"><InlineProblem message={t("error.runtime_unavailable")} /></div>;
  }
  const currentEnvironment = environments.data.items.find(
    (item) => item.id === assignment.data.environmentId,
  );
  const managed = capture.data.managedRun;
  const manual = capture.data.manualCapture;
  const authoritativeManual = manualAuthority.data?.capture;
  const captureState = authoritativeManual?.state ?? capture.data.state;
  const captureObservation = authoritativeManual?.observation ?? capture.data.observation;
  const captureUpdatedAt = authoritativeManual?.updatedAt ?? capture.data.updatedAt;
  const activeManualCapture = capture.data.kind === "manual_capture" && captureState === "active";
  const visibleActivities = (activities.data?.items ?? []).filter((item) =>
    capture.data.kind === "managed_run"
      ? item.parentRefs.captureRunId === capture.data.id
      : item.parentRefs.manualCaptureId === capture.data.id,
  );

  return (
    <div className="page capture-detail-page">
      <PageHeading
        actions={
          <Link className="quiet-button" search={{}} to={dashboardRoutePaths.captures}>
            {t("captureDetail.back")}
          </Link>
        }
        eyebrow={t("captureDetail.eyebrow")}
        title={capture.data.displayName}
        {...(capture.data.kind === "managed_run"
          ? managed?.cwd === undefined ? {} : { description: managed.cwd }
          : { description: t("captures.kind.manual") })}
      />
      <section className="data-panel capture-control-panel">
        <div className="capture-runtime-bar">
          <StatePill label={t(`captures.state.${captureState}`)} state={captureState} />
          <span>{t(capture.data.kind === "managed_run" ? "captureDetail.source.managed.short" : "captureDetail.source.manual.short")}</span>
          <span title={managed?.cwd}>{managed?.workspaceLabel ?? shortPath(managed?.cwd) ?? t("captures.value.manual")}</span>
          <span title={managed?.machineId}>{managed?.machineId?.slice(0, 10) ?? t("captures.value.local")}</span>
          <span>{t(`captures.observation.${captureObservation}`)}</span>
          <time dateTime={captureUpdatedAt}>{formatRelative(captureUpdatedAt, i18n.language)}</time>
        </div>
        <div className="capture-environment-control">
          <div>
            <strong>{t("captureDetail.environment.title")}</strong>
            <small>
              {currentEnvironment?.systemOwned
                ? t("captureDetail.environment.transparent")
                : t("captureDetail.environment.semantic", { revision: assignment.data.revision })}
            </small>
            {managed?.machineId !== undefined && managed.workspaceId !== undefined && !currentEnvironment?.systemOwned && (
              <small>
                {workspaceDefault.data?.environmentId === assignment.data.environmentId
                  ? t("captureDetail.environment.futureDefault")
                  : t("captureDetail.environment.futureDifferent")}
              </small>
            )}
          </div>
          <select
            aria-label={t("captureDetail.environment.current")}
            disabled={!isLiveCapture({ ...capture.data, state: captureState }) || switchEnvironment.isPending}
            onChange={(event) => switchEnvironment.mutate(event.target.value)}
            value={assignment.data.environmentId}
          >
            {environments.data.items
              .filter((item) => item.state === "active")
              .map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
          {managed?.machineId !== undefined && managed.workspaceId !== undefined && (
            currentEnvironment?.systemOwned ? (
              <Link className="quiet-button" search={{}} to={dashboardRoutePaths.environments}>
                {t("captureDetail.environment.configureInspection")}
              </Link>
            ) : (
              <button
                className="quiet-button"
                disabled={updateWorkspaceDefault.isPending || workspaceDefault.isPending}
                onClick={() => updateWorkspaceDefault.mutate()}
                type="button"
              >
                {workspaceDefault.data?.environmentId === assignment.data.environmentId
                  ? t("captureDetail.environment.clearDefault")
                  : t("captureDetail.environment.useForFuture")}
              </button>
            )
          )}
        </div>
        {switchNotice !== undefined && <p className="inline-notice" role="status">{t(switchNotice)}</p>}
        <div className="capture-source-row">
          <div className="capture-source-copy">
            <CaptureSourceIcon kind={capture.data.kind} />
            <div>
              <strong>{t(capture.data.kind === "managed_run" ? "captureDetail.source.managed.title" : "captureDetail.source.manual.title")}</strong>
              <small>{t(capture.data.kind === "managed_run" ? "captureDetail.source.managed.description" : "captureDetail.source.manual.description")}</small>
            </div>
          </div>
          {activeManualCapture && manualAuthority.data !== undefined && !confirmingRevoke && (
            <button className="danger-text" onClick={() => setConfirmingRevoke(true)} type="button">
              {t("manualCapture.revoke.action")}
            </button>
          )}
        </div>
        {activeManualCapture && manualAuthority.isError && (
          <div className="capture-authority-error">
            <InlineProblem message={t(controlErrorKey(manualAuthority.error))} />
            <button className="quiet-button" onClick={() => void manualAuthority.refetch()} type="button">{t("common.retry")}</button>
          </div>
        )}
        {confirmingRevoke && (
          <div aria-labelledby="revoke-capture-title" className="capture-revoke-confirm" role="group">
            <div>
              <strong id="revoke-capture-title">{t("manualCapture.revoke.confirmTitle", { name: capture.data.displayName })}</strong>
              <p>{t("manualCapture.revoke.confirmDetail")}</p>
            </div>
            <div>
              <button autoFocus disabled={revokeManualCapture.isPending} onClick={() => setConfirmingRevoke(false)} type="button">{t("common.cancel")}</button>
              <button className="danger-action" disabled={revokeManualCapture.isPending} onClick={() => revokeManualCapture.mutate()} type="button">{t("manualCapture.revoke.confirmAction")}</button>
            </div>
          </div>
        )}
        {revokeNotice?.kind === "error" && <InlineProblem message={t(revokeNotice.key)} />}
        {revokeNotice?.kind === "status" && <p className="inline-notice inline-success" role="status">{t(revokeNotice.key)}</p>}
        <details className="capture-run-details">
          <summary>{t(capture.data.kind === "managed_run" ? "captureDetail.identity.details" : "captureDetail.identity.manualDetails")}</summary>
          <dl className="facts-list">
            {capture.data.kind === "managed_run" ? <>
              <dt>{t("captureDetail.identity.executable")}</dt>
              <dd title={managed?.canonicalExecutablePath}>{managed?.canonicalExecutablePath ?? t("common.notApplicable")}</dd>
              <dt>{t("captureDetail.identity.recognition")}</dt>
              <dd>{managed?.recognition ?? t("common.notApplicable")}</dd>
              <dt>{t("captureDetail.identity.user")}</dt>
              <dd>{managed?.localUserLabel ?? t("common.unavailable")}</dd>
              <dt>{t("captureDetail.identity.process")}</dt>
              <dd>{managed?.processId ?? t("common.notApplicable")}</dd>
              <dt>{t("captureDetail.machine")}</dt>
              <dd>{managed?.machineId ?? t("common.unavailable")}</dd>
            </> : <>
              <dt>{t("manualCapture.clientClass.label")}</dt>
              <dd>{manual === undefined ? t("common.unavailable") : t(`manualCapture.clientClass.${manual.clientClass}`)}</dd>
              <dt>{t("manualCapture.lifetime.label")}</dt>
              <dd>{manual === undefined ? t("common.unavailable") : t(`manualCapture.lifetime.${manual.lifetime}`)}</dd>
              <dt>{t("captureDetail.identity.credentialRevision")}</dt>
              <dd>{manual?.credentialRevision ?? t("common.unavailable")}</dd>
              <dt>{t("captureDetail.identity.lastObserved")}</dt>
              <dd>{manual?.lastObservedAt === undefined ? t("common.notApplicable") : formatRelative(manual.lastObservedAt, i18n.language)}</dd>
              <dt>{t("captureDetail.identity.captureId")}</dt>
              <dd><code>{capture.data.id}</code></dd>
            </>}
          </dl>
        </details>
      </section>

      <section className="data-panel request-list-panel">
        <SectionHeading
          action={<Link search={{}} to={dashboardTaskRoutePaths.captureRequests}>{t("captureDetail.requests.all")}</Link>}
          title={t("captureDetail.requests.title")}
        />
        {activities.isPending && activities.data === undefined ? (
          <LoadingRows count={4} />
        ) : visibleActivities.length === 0 ? (
          <EmptyState description={currentEnvironment?.systemOwned ? t("captureDetail.requests.empty.transparent") : t("captureDetail.requests.empty.description")} title={t("captureDetail.requests.empty.title")} />
        ) : (
          <ConversationTable filterable={visibleActivities.length > 8} items={visibleActivities} />
        )}
      </section>
    </div>
  );
}

export function RequestsRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const activities = useInfiniteQuery({
    queryKey: [...dashboardQueryKeys.activities, "conversations"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => model.client.activities({
      limit: 50,
      ...(pageParam === "" ? {} : { cursor: pageParam }),
    }, signal),
    getNextPageParam: (page) => page.nextCursor,
    refetchInterval: model.pollInterval,
  });
  const activityItems = useMemo(() => {
    const unique = new Map<string, ActivityRecord>();
    for (const page of activities.data?.pages ?? []) {
      for (const item of page.items) unique.set(item.id, item);
    }
    return [...unique.values()];
  }, [activities.data?.pages]);
  return (
    <div className="page request-page">
      <PageHeading
        actions={<Link className="quiet-button" search={{}} to={dashboardRoutePaths.captures}>{t("requests.back")}</Link>}
        description={t("requests.description")}
        title={t("requests.title")}
      />
      <section className="data-panel">
        {activities.isPending && activities.data === undefined ? <LoadingRows /> :
          activities.isError && activities.data === undefined ? <InlineProblem message={t(controlErrorKey(activities.error))} /> :
            activityItems.length === 0 ? <EmptyState description={t("requests.empty.description")} title={t("requests.empty.title")} /> : <>
              <ConversationTable filterable items={activityItems} />
              {activities.hasNextPage && (
                <footer className="table-pagination">
                  <span>{t("requests.loaded", { count: activityItems.length })}</span>
                  <button className="quiet-button" disabled={activities.isFetchingNextPage} onClick={() => void activities.fetchNextPage()} type="button">
                    {t(activities.isFetchingNextPage ? "requests.loadingMore" : "requests.loadMore")}
                  </button>
                </footer>
              )}
            </>}
      </section>
    </div>
  );
}

interface ConversationSummary {
  readonly key: string;
  readonly latest: ActivityRecord;
  readonly turnCount: number;
  readonly captureRunId?: string;
}

// A CaptureRun is the narrowest conversation boundary the runtime currently
// proves. Manual/system proxy traffic stays exchange-scoped until an explicit
// AgentSession authority exists; titles or nearby timestamps must never merge it.
function summarizeConversations(items: readonly ActivityRecord[]): readonly ConversationSummary[] {
  const grouped = new Map<string, ActivityRecord[]>();
  for (const item of items) {
    const key = item.parentRefs.captureRunId === undefined
      ? `exchange:${item.id}`
      : `capture-run:${item.parentRefs.captureRunId}`;
    const current = grouped.get(key);
    if (current === undefined) grouped.set(key, [item]);
    else current.push(item);
  }
  return [...grouped.entries()].map(([key, turns]) => {
    const ordered = [...turns].sort(
      (left, right) => Date.parse(left.occurredAt) - Date.parse(right.occurredAt),
    );
    const latest = ordered.at(-1)!;
    return {
      key,
      latest,
      turnCount: ordered.length,
      ...(latest.parentRefs.captureRunId === undefined
        ? {}
        : { captureRunId: latest.parentRefs.captureRunId }),
    };
  }).sort(
    (left, right) => Date.parse(right.latest.occurredAt) - Date.parse(left.latest.occurredAt),
  );
}

function ConversationTable({
  filterable = false,
  items,
}: {
  readonly filterable?: boolean;
  readonly items: readonly ActivityRecord[];
}) {
  const { t, i18n } = useTranslation();
  const [filter, setFilter] = useState("");
  const conversations = summarizeConversations(items);
  const normalizedFilter = filter.trim().toLocaleLowerCase();
  const filteredConversations = normalizedFilter.length === 0
    ? conversations
    : conversations.filter((conversation) => conversationMatches(conversation, normalizedFilter));
  return (
    <>
      {filterable && conversations.length > 8 && (
        <div className="table-toolbar">
          <label className="table-filter">
            <SearchIcon />
            <span className="visually-hidden">{t("requests.filter")}</span>
            <input aria-label={t("requests.filter")} onChange={(event) => setFilter(event.target.value)} placeholder={t("requests.filter")} type="search" value={filter} />
          </label>
          <span>{t("requests.showing", { visible: filteredConversations.length, total: conversations.length })}</span>
        </div>
      )}
      {filteredConversations.length === 0 ? (
        <p className="capture-section-empty">{t("requests.noMatch")}</p>
      ) : (
        <div className="table-scroll">
          <table className="data-table responsive-table conversation-table">
            <thead><tr><th>{t("requests.column.status")}</th><th>{t("requests.column.conversation")}</th><th>{t("requests.column.turns")}</th><th>{t("requests.column.environment")}</th><th className="align-right">{t("requests.column.updated")}</th></tr></thead>
            <tbody>{filteredConversations.map((conversation) => {
              const item = conversation.latest;
              return <tr data-conversation-key={conversation.key} key={conversation.key}>
                <td data-label={t("requests.column.status")}><StatePill label={t(requestResultKey(item.reasonCode, item.status))} state={item.status} /></td>
                <td data-label={t("requests.column.conversation")}><Link className="row-link conversation-link" params={{ exchangeId: item.id }} search={{}} to={dashboardTaskRoutePaths.activityRequest}><strong>{item.source.displayName}</strong><small title={conversation.captureRunId}>{conversation.captureRunId ?? item.id}</small></Link></td>
                <td data-label={t("requests.column.turns")}>{t("requests.turns", { count: conversation.turnCount })}</td>
                <td data-label={t("requests.column.environment")}>{item.environment.id} <small>r{item.environment.revision}</small></td>
                <td className="align-right" data-label={t("requests.column.updated")}><time dateTime={item.occurredAt}>{formatRelative(item.occurredAt, i18n.language)}</time></td>
              </tr>
            })}</tbody>
          </table>
        </div>
      )}
    </>
  );
}

function conversationMatches(
  conversation: ConversationSummary,
  normalizedFilter: string,
): boolean {
  const item = conversation.latest;
  return [
    conversation.key,
    conversation.captureRunId,
    item.id,
    item.source.displayName,
    item.environment.id,
    item.status,
    item.reasonCode,
  ].some((value) => value?.toLocaleLowerCase().includes(normalizedFilter) === true);
}

function ManualCaptureDialog({
  environments,
  onClose,
}: {
  readonly environments: readonly EnvironmentRecord[];
  readonly onClose: () => void;
}) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [environmentId, setEnvironmentId] = useState(
    environments.find((item) => item.state === "active")?.id ?? "system_transparent",
  );
  const [context, setContext] = useState<ManualCaptureContext>();
  const [grant, setGrant] = useState<ManualCaptureGrant>();
  const [errorKey, setErrorKey] = useState<string>();
  const review = useMutation({
    mutationFn: () => model.client.manualCaptureContext(environmentId),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (value) => { setContext(value); setErrorKey(undefined); },
  });
  const create = useMutation({
    mutationFn: async () => {
      if (context === undefined) throw new Error("Manual Capture context is unavailable");
      return model.client.createManualCapture({
        environmentId,
        displayName: name.trim(),
        clientClass: "desktop_app",
        lifetime: "until_revoked",
        confirmationToken: context.confirmationToken,
      });
    },
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (value) => {
      setGrant(value.grant);
      setErrorKey(undefined);
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.captures });
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (name.trim().length === 0) return;
    if (context === undefined) review.mutate(); else create.mutate();
  };

  return (
    <div className="modal-backdrop">
      <section aria-labelledby="manual-capture-title" aria-modal="true" className="modal" role="dialog">
        <header><div><p className="eyebrow">{t("manualCapture.eyebrow")}</p><h2 id="manual-capture-title">{grant === undefined ? t("manualCapture.create.title") : t("manualCapture.delivery.title")}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button"><CloseIcon /></button></header>
        {grant === undefined ? (
          <form onSubmit={submit}>
            <label><span>{t("manualCapture.name")}</span><input autoFocus maxLength={120} onChange={(event) => { setName(event.target.value); setContext(undefined); }} value={name} /></label>
            <label><span>{t("manualCapture.environment")}</span><select onChange={(event) => { setEnvironmentId(event.target.value); setContext(undefined); }} value={environmentId}>{environments.filter((item) => item.state === "active").map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
            {context !== undefined && <div className="review-box"><strong>{t("manualCapture.review.title")}</strong><p>{context.root === undefined ? t("manualCapture.review.transparent") : t("manualCapture.review.semantic", { count: context.protectedAuthorities.length })}</p></div>}
            {errorKey !== undefined && <InlineProblem message={t(errorKey)} />}
            <footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={review.isPending || create.isPending || name.trim().length === 0} type="submit">{context === undefined ? t("manualCapture.review.action") : t("manualCapture.create.action")}</button></footer>
          </form>
        ) : (
          <div className="credential-delivery">
            <p>{t("manualCapture.delivery.description")}</p>
            <CredentialLine label={t("manualCapture.delivery.proxy")} value={grant.proxyAddress} />
            <CredentialLine label={t("manualCapture.delivery.username")} value={grant.proxyUsername} />
            <CredentialLine label={t("manualCapture.delivery.password")} value={grant.proxyPassword} />
            {grant.root !== undefined && <CredentialLine label={t("manualCapture.delivery.root")} value={grant.root.pemPath} />}
            <p className="boundary-copy">{t("manualCapture.delivery.once")}</p>
            <footer><button className="primary-action" onClick={onClose} type="button">{t("common.done")}</button></footer>
          </div>
        )}
      </section>
    </div>
  );
}

function CredentialLine({ label, value }: { readonly label: string; readonly value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return <div className="credential-line"><span>{label}</span><code>{value}</code><button onClick={() => void navigator.clipboard.writeText(value).then(() => setCopied(true))} type="button">{copied ? t("common.copied") : t("common.copy")}</button></div>;
}

function StatePill({ label, state }: { readonly label: string; readonly state: string }) {
  return <span className={`state-pill state-${state}`}><i />{label}</span>;
}

function SearchIcon() {
  return <svg aria-hidden="true" className="inline-icon" fill="none" viewBox="0 0 16 16"><circle cx="7" cy="7" r="4.25" /><path d="m10.25 10.25 3 3" /></svg>;
}

function CloseIcon() {
  return <svg aria-hidden="true" className="inline-icon" fill="none" viewBox="0 0 16 16"><path d="m4 4 8 8M12 4l-8 8" /></svg>;
}

function CaptureSourceIcon({ kind }: { readonly kind: CaptureRecord["kind"] }) {
  return kind === "managed_run"
    ? <svg aria-hidden="true" className="capture-source-icon" fill="none" viewBox="0 0 20 20"><path d="M4 4.5h12v9H4zM7 16h6M10 13.5V16" /><path d="m7.5 8 1.75 1.75L12.75 7" /></svg>
    : <svg aria-hidden="true" className="capture-source-icon" fill="none" viewBox="0 0 20 20"><path d="M6 5.5h8v9H6zM8 3v2.5M12 3v2.5M8 14.5V17M12 14.5V17M3.5 8H6M14 8h2.5M3.5 12H6M14 12h2.5" /></svg>;
}

function isLiveCapture(capture: CaptureRecord): boolean {
  return capture.state === "active" ||
    capture.state === "attached" ||
    capture.state === "created" ||
    capture.state === "running";
}

function compareCaptureActivity(left: CaptureRecord, right: CaptureRecord): number {
  return Date.parse(right.updatedAt) - Date.parse(left.updatedAt) ||
    left.displayName.localeCompare(right.displayName);
}

function captureMatches(capture: CaptureRecord, normalizedFilter: string): boolean {
  return [
    capture.displayName,
    capture.id,
    capture.kind,
    capture.state,
    capture.managedRun?.workspaceLabel,
    capture.managedRun?.cwd,
    capture.managedRun?.machineId,
    capture.manualCapture?.clientClass,
    capture.manualCapture?.lifetime,
  ].some((value) => value?.toLocaleLowerCase().includes(normalizedFilter) === true);
}

function captureBrand(capture: CaptureRecord): "claude-code" | "codex" | undefined {
  const value = `${capture.displayName} ${capture.managedRun?.executableLabel ?? ""}`.toLocaleLowerCase("en-US");
  if (value.includes("claude")) return "claude-code";
  if (value.includes("codex")) return "codex";
  return undefined;
}

function shortPath(value: string | undefined): string | undefined {
  if (value === undefined) return undefined;
  const parts = value.split("/").filter(Boolean);
  return parts.at(-1) ?? value;
}

function formatRelative(value: string, locale: string): string {
  const milliseconds = Date.parse(value);
  if (!Number.isFinite(milliseconds)) return value;
  const seconds = Math.round((milliseconds - Date.now()) / 1_000);
  if (Math.abs(seconds) < 60) return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(hours, "hour");
  const date = new Date(milliseconds);
  const now = new Date();
  return new Intl.DateTimeFormat(locale, date.getFullYear() === now.getFullYear()
    ? { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }
    : { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
    .format(milliseconds);
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
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

  return (
    <div className="page capture-page">
      <PageHeading
        actions={
          <button className="primary-action" onClick={() => setCreating(true)} type="button">
            {t("captures.add")}
          </button>
        }
        description={t("captures.description")}
        title={t("captures.title")}
      />
      <div className="page-tabs" role="tablist" aria-label={t("captures.tabs.label")}>
        <Link activeOptions={{ exact: true }} aria-selected="true" role="tab" search={{}} to={dashboardRoutePaths.captures}>
          {t("captures.tabs.runs")}
        </Link>
        <Link activeOptions={{ exact: true }} aria-selected="false" role="tab" search={{}} to={dashboardTaskRoutePaths.captureRequests}>
          {t("captures.tabs.requests")}
        </Link>
      </div>

      <section className="data-panel capture-table-panel">
        <SectionHeading
          title={t("captures.active.title", { count: records.length })}
        />
        {captures.isPending && captures.data === undefined ? (
          <LoadingRows />
        ) : captures.isError && captures.data === undefined ? (
          <InlineProblem message={t(controlErrorKey(captures.error))} />
        ) : records.length === 0 ? (
          <EmptyState
            action={<button onClick={() => setCreating(true)} type="button">{t("captures.add")}</button>}
            description={t("captures.empty.description")}
            title={t("captures.empty.title")}
          />
        ) : (
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
                {records.map((capture) => (
                  <CaptureRow
                    capture={capture}
                    environments={environments.data?.items ?? []}
                    key={capture.key}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {creating && (
        <ManualCaptureDialog
          environments={environments.data?.items ?? []}
          onClose={() => setCreating(false)}
        />
      )}
    </div>
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
        capture.data?.kind === "managed_run" ? { captureRunId: capture.data.id } : undefined,
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
  const visibleActivities = (activities.data?.items ?? []).filter((item) =>
    capture.data.kind === "managed_run"
      ? item.parentRefs.captureRunId === capture.data.id
      : item.parentRefs.manualCaptureId === capture.data.id,
  );

  return (
    <div className="page capture-detail-page">
      <PageHeading
        eyebrow={t("captureDetail.eyebrow")}
        title={capture.data.displayName}
        {...(capture.data.kind === "managed_run"
          ? managed?.cwd === undefined ? {} : { description: managed.cwd }
          : { description: t("captures.kind.manual") })}
      />
      <section className="capture-summary-strip">
        <SummaryFact label={t("captureDetail.state")} value={t(`captures.state.${capture.data.state}`)} />
        <SummaryFact label={t("captureDetail.workspace")} value={managed?.workspaceLabel ?? shortPath(managed?.cwd) ?? t("common.unavailable")} {...(managed?.cwd === undefined ? {} : { title: managed.cwd })} />
        <SummaryFact label={t("captureDetail.machine")} value={managed?.machineId?.slice(0, 10) ?? t("captures.value.local")} {...(managed?.machineId === undefined ? {} : { title: managed.machineId })} />
        <SummaryFact label={t("captureDetail.observation")} value={t(`captures.observation.${capture.data.observation}`)} />
        <SummaryFact label={t("captureDetail.updated")} value={formatRelative(capture.data.updatedAt, i18n.language)} />
      </section>

      <div className="detail-grid">
        <section className="data-panel environment-assignment-panel">
          <SectionHeading title={t("captureDetail.environment.title")} />
          <div className="routing-spine">
            <SpineNode label={t("captureDetail.spine.capture")} value={capture.data.displayName} />
            <span aria-hidden="true" />
            <SpineNode
              label={t("captureDetail.spine.environment")}
              value={currentEnvironment?.name ?? assignment.data.environmentId}
            />
            <span aria-hidden="true" />
            <SpineNode
              label={t("captureDetail.spine.routes")}
              value={t("captureDetail.spine.routeCount", {
                count: countRoutes(currentEnvironment),
              })}
            />
          </div>
          <label className="field-row">
            <span>{t("captureDetail.environment.current")}</span>
            <select
              disabled={switchEnvironment.isPending}
              onChange={(event) => switchEnvironment.mutate(event.target.value)}
              value={assignment.data.environmentId}
            >
              {environments.data.items
                .filter((item) => item.state === "active")
                .map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </label>
          <p className="muted-copy">
            {currentEnvironment?.systemOwned
              ? t("captureDetail.environment.transparent")
              : t("captureDetail.environment.semantic", { revision: assignment.data.revision })}
          </p>
          {managed?.machineId !== undefined && managed.workspaceId !== undefined && (
            currentEnvironment?.systemOwned ? (
              <Link className="inline-action" search={{}} to={dashboardRoutePaths.environments}>
                {t("captureDetail.environment.configureInspection")}
              </Link>
            ) : (
              <div className="workspace-default-row">
                <span>
                  {workspaceDefault.data?.environmentId === assignment.data.environmentId
                    ? t("captureDetail.environment.futureDefault")
                    : t("captureDetail.environment.futureDifferent")}
                </span>
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
              </div>
            )
          )}
          {switchNotice !== undefined && <p className="inline-notice" role="status">{t(switchNotice)}</p>}
        </section>

        <section className="data-panel identity-panel">
          <SectionHeading title={t("captureDetail.identity.title")} />
          <dl className="facts-list">
            <dt>{t("captureDetail.identity.executable")}</dt>
            <dd title={managed?.canonicalExecutablePath}>{managed?.canonicalExecutablePath ?? t("common.notApplicable")}</dd>
            <dt>{t("captureDetail.identity.recognition")}</dt>
            <dd>{managed?.recognition ?? t("common.notApplicable")}</dd>
            <dt>{t("captureDetail.identity.user")}</dt>
            <dd>{managed?.localUserLabel ?? t("common.unavailable")}</dd>
            <dt>{t("captureDetail.identity.process")}</dt>
            <dd>{managed?.processId ?? t("common.notApplicable")}</dd>
          </dl>
        </section>
      </div>

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
          <RequestTable items={visibleActivities.slice(0, 20)} />
        )}
      </section>
    </div>
  );
}

export function RequestsRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const activities = useQuery({
    queryKey: dashboardQueryKeys.activities,
    queryFn: ({ signal }) => model.client.activities(undefined, signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  return (
    <div className="page request-page">
      <PageHeading description={t("requests.description")} title={t("requests.title")} />
      <div className="page-tabs" role="tablist" aria-label={t("captures.tabs.label")}>
        <Link activeOptions={{ exact: true }} aria-selected="false" role="tab" search={{}} to={dashboardRoutePaths.captures}>{t("captures.tabs.runs")}</Link>
        <Link activeOptions={{ exact: true }} aria-selected="true" role="tab" search={{}} to={dashboardTaskRoutePaths.captureRequests}>{t("captures.tabs.requests")}</Link>
      </div>
      <section className="data-panel">
        {activities.isPending && activities.data === undefined ? <LoadingRows /> :
          activities.data?.items.length === 0 ? <EmptyState description={t("requests.empty.description")} title={t("requests.empty.title")} /> :
            activities.data !== undefined ? <RequestTable items={activities.data.items} /> :
              <InlineProblem message={t(controlErrorKey(activities.error))} />}
      </section>
    </div>
  );
}

function RequestTable({ items }: { readonly items: readonly import("./control-types.ts").ActivityRecord[] }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="table-scroll">
      <table className="data-table responsive-table request-table">
        <thead><tr><th>{t("requests.column.status")}</th><th>{t("requests.column.request")}</th><th>{t("requests.column.environment")}</th><th>{t("requests.column.route")}</th><th className="align-right">{t("requests.column.occurred")}</th></tr></thead>
        <tbody>{items.map((item) => (
          <tr key={item.id}>
            <td data-label={t("requests.column.status")}><StatePill label={t(requestResultKey(item.reasonCode, item.status))} state={item.status} /></td>
            <td data-label={t("requests.column.request")}><Link className="row-link request-link" params={{ exchangeId: item.id }} search={{}} to={dashboardTaskRoutePaths.activityRequest}><strong>{item.title}</strong><small>{item.id}</small></Link></td>
            <td data-label={t("requests.column.environment")}>{item.environment.id} <small>r{item.environment.revision}</small></td>
            <td data-label={t("requests.column.route")}>{item.environment.routeId}</td>
            <td className="align-right" data-label={t("requests.column.occurred")}><time dateTime={item.occurredAt}>{formatRelative(item.occurredAt, i18n.language)}</time></td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
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
        <header><div><p className="eyebrow">{t("manualCapture.eyebrow")}</p><h2 id="manual-capture-title">{grant === undefined ? t("manualCapture.create.title") : t("manualCapture.delivery.title")}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header>
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

function SummaryFact({ label, title, value }: { readonly label: string; readonly title?: string; readonly value: string | number }) {
  return <div><span>{label}</span><strong title={title}>{value}</strong></div>;
}

function SpineNode({ label, value }: { readonly label: string; readonly value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function StatePill({ label, state }: { readonly label: string; readonly state: string }) {
  return <span className={`state-pill state-${state}`}><i />{label}</span>;
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

function countRoutes(environment: EnvironmentRecord | undefined): number {
  return environment?.clientEndpoints.reduce(
    (total, endpoint) => total + endpoint.protocolPlans.reduce(
      (plans, plan) => plans + plan.upstreamPlan.routes.length,
      0,
    ),
    0,
  ) ?? 0;
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
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(milliseconds);
}

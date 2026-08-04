import {
  type ChangeEvent,
  type FormEvent,
  type InputHTMLAttributes,
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Link,
  Outlet,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  accessAppPresetDefaults,
  applyAccessAppPreset,
  buildAccessApplyInput,
  clientOriginIdentity,
  credentialCoordinates,
  initialAccessForm,
  newAccessForm,
  type AccessAppPreset,
  type AccessFormValues,
  validAccessForm,
} from "./access-form.ts";
import {
  DashboardQueryRuntime,
  controlErrorKey,
  type AccessCredentialCoordinates,
  type AccessLoadResult,
  type DashboardActions,
  type DashboardSource,
  type DashboardState,
  useCredentialMetadata,
  useAccessDirectory,
  useDashboardQueryRuntime,
  useExchangeDetail,
  useWorkspaceRoutes,
} from "./dashboard-runtime.ts";
import type {
  AccessAddCandidateInput,
  AccessCandidateProvider,
  AccessDetail,
  AccessDirectoryItem,
  ActivityRecord,
  ApprovalView,
  CaptureRunRecord,
  ConnectionRecord,
  EgressAttemptRecord,
  OfflineHoldSnapshot,
  StatusResponse,
  WorkspaceRouteBinding,
} from "./control-types.ts";
import { compareResourceIds } from "./control-client.ts";
import type { SupportedLocale } from "./i18n.ts";
import { ManualCapturePanel } from "./manual-capture-panel.tsx";
import {
  inspectTerminalCommand,
  installTerminalCommand,
  refreshTerminalCommand,
  removeTerminalCommand,
  type TerminalCommandStatus,
} from "./desktop-host.ts";
import {
  dashboardRoutePaths,
  dashboardTaskRoutePaths,
  type DashboardNavigation,
  type DashboardView,
  viewFromPathname,
} from "./navigation.ts";

interface DashboardRuntime {
  readonly actions: DashboardActions;
  readonly model: DashboardQueryRuntime;
  readonly navigation: DashboardNavigation;
  readonly state: DashboardState;
  readonly preview: boolean;
}

type SourceAvailability = "loading" | "ready" | "stale" | "unavailable";

function sourceAvailability(
  state: DashboardState,
  source: DashboardSource,
): SourceAvailability {
  if (state.unavailableSources.includes(source)) {
    return state.refreshedAtBySource[source] === undefined
      ? "unavailable"
      : "stale";
  }
  if (state.refreshedAtBySource[source] !== undefined) {
    return "ready";
  }
  return "loading";
}

function sourceIsRelevantToView(
  source: DashboardSource,
  view: DashboardView,
): boolean {
  if (view === "overview") {
    return true;
  }
  if (view === "activity") {
    return ["activities", "captureRuns", "connections", "egressAttempts"].includes(
      source,
    );
  }
  if (view === "policy") {
    return source === "approvals";
  }
  return false;
}

const DashboardRuntimeContext = createContext<DashboardRuntime | undefined>(
  undefined,
);

function useDashboardRuntime(): DashboardRuntime {
  const runtime = useContext(DashboardRuntimeContext);
  if (runtime === undefined) {
    throw new Error("dashboard route rendered outside its runtime shell");
  }
  return runtime;
}

export function DashboardShell({
  model,
  preview,
}: {
  readonly model: DashboardQueryRuntime;
  readonly preview: boolean;
}) {
  const { t, i18n } = useTranslation();
  const { actions, state } = useDashboardQueryRuntime(model);
  const pathname = useRouterState({
    select: (routerState) => routerState.location.pathname,
  });
  const view = viewFromPathname(pathname);
  const navigate = useNavigate();
  const openView = useCallback<DashboardNavigation["openView"]>(
    (next, options) => {
      if (next === "policy") {
        const selected = options?.selectedApprovalId;
        void navigate({
          ...(options?.replace === undefined
            ? {}
            : { replace: options.replace }),
          search: selected === undefined ? {} : { selected },
          to: dashboardRoutePaths.policy,
        });
        return;
      }
      void navigate({
        ...(options?.replace === undefined ? {} : { replace: options.replace }),
        search: {},
        to: dashboardRoutePaths[next],
      });
    },
    [navigate],
  );
  const runtime = useMemo<DashboardRuntime>(
    () => ({ actions, model, navigation: { openView }, preview, state }),
    [actions, model, openView, preview, state],
  );

  const changeLocale = (locale: SupportedLocale) => {
    document.documentElement.lang = locale;
    void i18n.changeLanguage(locale);
  };

  const approvalAvailability = sourceAvailability(state, "approvals");
  const captureAvailability = sourceAvailability(state, "captureRuns");
  const offlineAvailability = sourceAvailability(state, "offline");
  const statusAvailability = sourceAvailability(state, "status");
  const holdState = state.offline?.state ?? "unbound";
  const holdAction = holdState === "held" ? "resume" : "enter";
  const holdDisabled =
    state.busy ||
    offlineAvailability !== "ready" ||
    (holdAction === "enter" ? holdState !== "online" : holdState !== "held");
  const relevantUnavailableSources = state.unavailableSources.filter((source) =>
    sourceIsRelevantToView(source, view),
  );
  const partialErrorKey = relevantUnavailableSources.some(
    (source) => state.refreshedAtBySource[source] === undefined,
  )
    ? "error.dashboard_partial_unavailable"
    : "error.dashboard_partial";
  const errorMessage =
    state.errorKey === "error.dashboard_partial"
      ? relevantUnavailableSources.length === 0
        ? undefined
        : t(partialErrorKey, {
            sources: relevantUnavailableSources
              .map((source) => t(`dashboard.source.${source}`))
              .join(", "),
          })
      : state.errorKey === undefined
        ? undefined
        : t(state.errorKey);
  return (
    <DashboardRuntimeContext.Provider value={runtime}>
      <div className="app-shell">
        <a
          className="skip-link"
          href="#main-content"
          onClick={(event) => {
            event.preventDefault();
            document.getElementById("main-content")?.focus();
          }}
        >
          {t("navigation.skip")}
        </a>
        <aside className="sidebar">
          <Link className="brand" search={{}} to={dashboardRoutePaths.overview}>
            <span className="brand-mark" aria-hidden="true">
              {t("app.mark")}
            </span>
            <span className="brand-name">{t("app.title")}</span>
            <span className={`host-badge ${preview ? "preview" : ""}`}>
              {t(preview ? "host.preview" : "host.desktop")}
            </span>
          </Link>
          <nav className="primary-nav" aria-label={t("navigation.primary")}>
            <NavGroup label={t("navigation.group.daily")}>
              <NavItem current={view} icon="overview" id="overview" />
              <NavItem current={view} icon="access" id="access" />
              <NavItem current={view} icon="activity" id="activity" />
            </NavGroup>
            <NavGroup label={t("navigation.group.analysis")}>
              <NavItem current={view} icon="quality" id="quality" />
              <NavItem current={view} icon="dashboards" id="dashboards" />
            </NavGroup>
            <NavGroup label={t("navigation.group.extensionsSecurity")}>
              <NavItem current={view} icon="extensions" id="extensions" />
              <NavItem
                {...(approvalAvailability === "ready"
                  ? { count: state.approvals.length }
                  : {})}
                current={view}
                icon="policy"
                id="policy"
              />
            </NavGroup>
            <NavGroup label={t("navigation.group.system")}>
              <NavItem current={view} icon="settings" id="settings" />
            </NavGroup>
          </nav>
          <p className="sidebar-note">
            {t(preview ? "host.preview.note" : "host.desktop.note")}
          </p>
        </aside>

        <header className="topbar">
          <span
            className={`top-status ${state.status?.ready ? "online" : "attention"}`}
          >
            <span className="state-dot" aria-hidden="true" />
            <strong>
              {state.status === undefined
                ? t(
                    statusAvailability === "unavailable"
                      ? "common.data.unavailable"
                      : "app.loading",
                  )
                : t(state.status.statusKey)}
            </strong>
          </span>
          <span className="top-context">
            {view === "access"
              ? t("topbar.access")
              : captureAvailability === "ready"
              ? t("topbar.capture", {
                  observed: state.captureRuns.filter(
                    (run) => run.observation === "observed",
                  ).length,
                  total: state.captureRuns.length,
                })
              : t(`common.data.${captureAvailability}`)}
          </span>
          <span className="topbar-spacer" />
          <button
            className="quiet-button hold-action"
            disabled={holdDisabled}
            onClick={() =>
              void (holdAction === "resume"
                ? actions.resumeOfflineHold()
                : actions.enterOfflineHold())
            }
            type="button"
          >
            <span className="hold-action-label">
              {t(
                holdAction === "resume"
                  ? "offlineHold.resume.action"
                  : "offlineHold.enter.action",
              )}
            </span>
            <span aria-hidden="true" className="hold-action-compact">
              {t(
                holdAction === "resume"
                  ? "offlineHold.resume.compact"
                  : "offlineHold.enter.compact",
              )}
            </span>
          </button>
          <Link
            className={`pending-link ${state.approvals.length === 0 ? "empty" : ""}`}
            search={
              state.approvals[0] === undefined
                ? {}
                : { selected: state.approvals[0].id }
            }
            to={dashboardRoutePaths.policy}
          >
            {t("topbar.pending")}
            <span>
              {approvalAvailability === "ready" ? state.approvals.length : "—"}
            </span>
          </Link>
          <div className="header-actions">
            <fieldset className="locale-switcher">
              <legend>{t("locale.label")}</legend>
              {(["en-US", "zh-CN"] as const).map((locale) => (
                <button
                  aria-label={t(`locale.${locale}`)}
                  aria-pressed={i18n.language === locale}
                  key={locale}
                  onClick={() => changeLocale(locale)}
                  title={t(`locale.${locale}`)}
                  type="button"
                >
                  <span className="locale-label-full">
                    {t(`locale.${locale}`)}
                  </span>
                  <span aria-hidden="true" className="locale-label-compact">
                    {locale === "en-US" ? "EN" : "中"}
                  </span>
                </button>
              ))}
            </fieldset>
            <button
              className="icon-button"
              disabled={state.busy}
              onClick={() => void actions.refresh()}
              aria-label={t("common.refresh.action")}
              title={t("common.refresh.action")}
              type="button"
            >
              <span aria-hidden="true">↻</span>
            </button>
          </div>
        </header>

        <main className="main-content" id="main-content" tabIndex={-1}>
          {preview && (
            <div className="preview-banner" role="status">
              <strong>{t("host.preview.banner.title")}</strong>
              <span>{t("host.preview.banner.detail")}</span>
            </div>
          )}
          {errorMessage !== undefined && (
            <div className="error-banner" role="alert">
              {errorMessage}
            </div>
          )}
          <Outlet />
        </main>

        <footer className="app-footer">
          <span>{t(preview ? "host.preview" : "host.desktop")}</span>
          <span>{t("footer.coverage")}</span>
        </footer>
      </div>
    </DashboardRuntimeContext.Provider>
  );
}

export function OverviewRoutePage() {
  const { actions, navigation, state } = useDashboardRuntime();
  return (
    <OverviewPage
      actions={actions}
      onOpen={navigation.openView}
      state={state}
    />
  );
}

export function AccessRoutePage() {
  const { t } = useTranslation();
  const { actions, model, state } = useDashboardRuntime();
  return (
    <>
      <PageHeading
        description={t("page.access.description")}
        title={t("page.access.title")}
      />
      <AccessPanel actions={actions} busy={state.busy} model={model} />
    </>
  );
}

export function DashboardsRoutePage() {
  return <UnavailablePage feature="dashboards" />;
}

export function QualityRoutePage() {
  return <UnavailablePage feature="quality" />;
}

export function ExtensionsRoutePage() {
  return <UnavailablePage feature="extensions" />;
}

export function PolicyRoutePage({
  selectedApprovalId,
}: {
  readonly selectedApprovalId: string | undefined;
}) {
  const { t } = useTranslation();
  const { actions, state } = useDashboardRuntime();
  const approvalAvailability = sourceAvailability(state, "approvals");
  const selectionMissing =
    selectedApprovalId !== undefined &&
    approvalAvailability === "ready" &&
    !state.approvals.some((approval) => approval.id === selectedApprovalId);
  return (
    <>
      <PageHeading
        description={t("page.policy.description")}
        title={t("page.policy.title")}
      />
      {selectionMissing && (
        <LocatorNotice
          action={t("approval.selection.missing.action")}
          detail={t("approval.selection.missing.detail")}
          role="status"
          title={t("approval.selection.missing.title")}
          to={dashboardRoutePaths.policy}
        />
      )}
      <ApprovalPanel
        actions={actions}
        approvals={state.approvals}
        availability={approvalAvailability}
        busy={state.busy}
        selectedApprovalId={selectedApprovalId}
      />
    </>
  );
}

export function InvalidPolicyLocatorRoutePage() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeading
        description={t("page.policy.description")}
        title={t("page.policy.title")}
      />
      <LocatorNotice
        action={t("navigation.invalid.action")}
        detail={t("navigation.invalid.detail")}
        role="alert"
        title={t("navigation.invalid.title")}
        to={dashboardRoutePaths.policy}
      />
    </>
  );
}

export function InvalidDashboardLocatorRoutePage() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeading
        description={t("page.overview.description")}
        title={t("page.overview.title")}
      />
      <LocatorNotice
        action={t("page.unavailable.exit")}
        detail={t("navigation.invalid.detail")}
        role="alert"
        title={t("navigation.invalid.title")}
        to={dashboardRoutePaths.overview}
      />
    </>
  );
}

export type UnavailableDashboardTask =
  | "accessRouting"
  | "activityRequest"
  | "dashboardExtension"
  | "dashboardSystem"
  | "extensionDetail"
  | "extensionDevelop"
  | "extensionDiscover"
  | "extensionInstalled"
  | "qualitySites"
  | "settingsRecovery";

const unavailableTaskParent = {
  accessRouting: "access",
  activityRequest: "activity",
  dashboardExtension: "dashboards",
  dashboardSystem: "dashboards",
  extensionDetail: "extensions",
  extensionDevelop: "extensions",
  extensionDiscover: "extensions",
  extensionInstalled: "extensions",
  qualitySites: "quality",
  settingsRecovery: "settings",
} as const satisfies Record<UnavailableDashboardTask, DashboardView>;

/**
 * Gives a frozen ICM locator a stable, honest route before its server control
 * contract exists. Dynamic locator values are deliberately not reflected in
 * the document, so an arbitrary URL cannot become a content or secret echo.
 */
export function UnavailableTaskRoutePage({
  invalid = false,
  task,
}: {
  readonly invalid?: boolean;
  readonly task: UnavailableDashboardTask;
}) {
  const { t } = useTranslation();
  const parent = unavailableTaskParent[task];
  return (
    <>
      <PageHeading
        description={t(`page.${parent}.description`)}
        title={t(`navigation.task.${task}`)}
      />
      {invalid ? (
        <LocatorNotice
          action={t("navigation.invalid.sectionAction")}
          detail={t("navigation.invalid.detail")}
          role="alert"
          title={t("navigation.invalid.title")}
          to={dashboardRoutePaths[parent]}
        />
      ) : (
        <section className="panel boundary-page" role="status">
          <h2>{t("page.unavailable.title")}</h2>
          <p>{t("page.task.unavailable")}</p>
          <p className="boundary-note">{t("page.unavailable.boundary")}</p>
          <Link
            className="route-action"
            search={{}}
            to={dashboardRoutePaths[parent]}
          >
            {t("page.task.exit", {
              destination: t(`navigation.${parent}`),
            })}
          </Link>
        </section>
      )}
    </>
  );
}

export function ActivityRoutePage() {
  const { t } = useTranslation();
  const { model, state } = useDashboardRuntime();
  return (
    <>
      <PageHeading
        description={t("page.activity.description")}
        title={t("page.activity.title")}
      />
      <div className="activity-layout">
        <ActivityPanel
          activities={state.activities}
          availability={sourceAvailability(state, "activities")}
          emptyAccessAction
        />
        <WorkspaceRoutePanel runs={state.captureRuns} />
        <ManualCapturePanel model={model} />
        <CapturePanel
          availability={sourceAvailability(state, "captureRuns")}
          runs={state.captureRuns}
        />
        <ConnectionPanel
          availability={sourceAvailability(state, "connections")}
          connections={state.connections}
        />
        <EgressPanel
          attempts={state.egressAttempts}
          availability={sourceAvailability(state, "egressAttempts")}
        />
      </div>
    </>
  );
}

interface PendingWorkspaceGroup {
  readonly key: string;
  readonly machineShortId: string;
  readonly workspaceLabel: string;
  readonly runs: readonly CaptureRunRecord[];
}

function pendingWorkspaceGroups(
  runs: readonly CaptureRunRecord[],
  bindings: readonly WorkspaceRouteBinding[],
): readonly PendingWorkspaceGroup[] {
  const boundScopes = new Set(
    bindings.map(({ machineId, workspaceId }) => `${machineId}\u0000${workspaceId}`),
  );
  const groups = new Map<string, PendingWorkspaceGroup>();
  for (const run of runs) {
    if (
      (run.state !== "created" && run.state !== "attached") ||
      run.machineId === undefined ||
      run.workspaceId === undefined ||
      run.workspaceLabel === undefined
    ) {
      continue;
    }
    const key = `${run.machineId}\u0000${run.workspaceId}`;
    if (boundScopes.has(key)) {
      continue;
    }
    const current = groups.get(key);
    groups.set(key, {
      key,
      machineShortId: run.machineId.slice(0, 10),
      workspaceLabel: run.workspaceLabel,
      runs: [...(current?.runs ?? []), run],
    });
  }
  return [...groups.values()].sort((left, right) =>
    left.workspaceLabel.localeCompare(right.workspaceLabel),
  );
}

function WorkspaceRoutePanel({
  runs,
}: {
  readonly runs: readonly CaptureRunRecord[];
}) {
  const { t } = useTranslation();
  const { model } = useDashboardRuntime();
  const routes = useWorkspaceRoutes(model);
  if (!routes.enabled) {
    return null;
  }
  const pendingGroups = pendingWorkspaceGroups(runs, routes.items);
  const groupCount = new Set([
    ...routes.items.map(
      ({ machineId, workspaceId }) => `${machineId}\u0000${workspaceId}`,
    ),
    ...pendingGroups.map(({ key }) => key),
  ]).size;
  return (
    <section className="panel workspace-route-panel">
      <div className="section-heading workspace-route-heading">
        <div>
          <p className="eyebrow">{t("workspaceRoutes.eyebrow")}</p>
          <h2>{t("workspaceRoutes.title")}</h2>
          <p>{t("workspaceRoutes.description")}</p>
        </div>
        <span className="workspace-route-count">
          {t("workspaceRoutes.count", { count: groupCount })}
        </span>
      </div>
      {routes.errorKey !== undefined && (
        <p className="boundary-note" role="alert">
          {t(routes.errorKey)}
        </p>
      )}
      {groupCount === 0 ? (
        <p className="empty-state">
          {t(
            routes.loading
              ? "common.data.loading"
              : "workspaceRoutes.empty",
          )}
        </p>
      ) : (
        <ol className="workspace-route-list">
          {routes.items.map((binding) => (
            <WorkspaceRouteCard
              binding={binding}
              key={binding.id}
              pending={routes.pending}
              update={routes.update}
            />
          ))}
          {pendingGroups.map((group) => (
            <PendingWorkspaceRouteCard group={group} key={group.key} />
          ))}
        </ol>
      )}
      <p className="workspace-route-boundary">
        {t("workspaceRoutes.userLabelBoundary")}
      </p>
    </section>
  );
}

function PendingWorkspaceRouteCard({
  group,
}: {
  readonly group: PendingWorkspaceGroup;
}) {
  const { t } = useTranslation();
  return (
    <li className="workspace-route-card pending">
      <div className="workspace-route-card-heading">
        <div>
          <p className="workspace-route-machine">
            {t("workspaceRoutes.machine.local")} · {group.machineShortId}
          </p>
          <h3>{group.workspaceLabel}</h3>
        </div>
        <span className="workspace-route-status active">
          {t("workspaceRoutes.activeRuns", { count: group.runs.length })}
        </span>
      </div>
      <div className="workspace-route-body">
        <div className="workspace-route-assignment">
          <label>{t("workspaceRoutes.route.label")}</label>
          <div className="workspace-route-awaiting">
            {t("workspaceRoutes.route.awaitingAccess")}
          </div>
          <p>{t("workspaceRoutes.route.awaitingAccessDetail")}</p>
        </div>
        <div className="workspace-route-runs">
          <p className="workspace-route-section-label">
            {t("workspaceRoutes.runs.label")}
          </p>
          <ul>
            {group.runs.map((run) => (
              <li key={run.id}>
                <span className="workspace-route-run-dot" />
                <span>
                  <strong>
                    {run.localUserLabel ?? t("workspaceRoutes.user.unknown")}
                  </strong>
                  {" · "}
                  {run.executableLabel}
                </span>
                <span>
                  {t(
                    run.state === "attached"
                      ? "workspaceRoutes.runState.active"
                      : "workspaceRoutes.runState.idle",
                  )}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </li>
  );
}

function WorkspaceRouteCard({
  binding,
  pending,
  update,
}: {
  readonly binding: WorkspaceRouteBinding;
  readonly pending: boolean;
  readonly update: (
    binding: WorkspaceRouteBinding,
    profileId: string,
  ) => Promise<boolean>;
}) {
  const { t } = useTranslation();
  const selected = binding.approvedProfiles.find(
    ({ profileId }) => profileId === binding.profileId,
  );
  const selectedIsAvailable = selected?.available === true;
  return (
    <li
      className={
        binding.state === "active"
          ? "workspace-route-card"
          : "workspace-route-card unavailable"
      }
    >
      <div className="workspace-route-card-heading">
        <div>
          <p className="workspace-route-machine">
            {binding.machineDisplayName} · {binding.machineShortId}
          </p>
          <h3>{binding.workspaceLabel}</h3>
        </div>
        <span
          className={
            binding.activeRunCount > 0
              ? "workspace-route-status active"
              : "workspace-route-status"
          }
        >
          {t("workspaceRoutes.activeRuns", {
            count: binding.activeRunCount,
          })}
        </span>
      </div>
      <div className="workspace-route-body">
        <div className="workspace-route-assignment">
          <label htmlFor={`workspace-route-${binding.id}`}>
            {t("workspaceRoutes.route.label")}
          </label>
          <select
            disabled={pending || binding.approvedProfiles.length === 0}
            id={`workspace-route-${binding.id}`}
            onChange={(event) => {
              void update(binding, event.currentTarget.value);
            }}
            value={binding.profileId}
          >
            {!selectedIsAvailable && (
              <option disabled value={binding.profileId}>
                {t("workspaceRoutes.route.unavailable")}
              </option>
            )}
            {binding.approvedProfiles.map((profile) => (
              <option
                disabled={!profile.available}
                key={profile.profileId}
                value={profile.profileId}
              >
                {profile.label} · {profile.modelPresentation} · {profile.authLabel}
              </option>
            ))}
          </select>
          <p>
            {selected === undefined
              ? t("workspaceRoutes.route.repair")
              : t("workspaceRoutes.route.summary", {
                  access: binding.accessId,
                  account: selected.authLabel,
                  model: selected.modelPresentation,
                })}
          </p>
        </div>
        <div className="workspace-route-runs">
          <p className="workspace-route-section-label">
            {t("workspaceRoutes.runs.label")}
          </p>
          {binding.activeRuns.length === 0 ? (
            <p>{t("workspaceRoutes.runs.empty")}</p>
          ) : (
            <ul>
              {binding.activeRuns.map((run) => (
                <li key={run.runId}>
                  <span className="workspace-route-run-dot" />
                  <span>
                    <strong>
                      {run.localUserLabel ?? t("workspaceRoutes.user.unknown")}
                    </strong>
                    {" · "}
                    {run.clientLabel}
                  </span>
                  <span>{t(`workspaceRoutes.runState.${run.state}`)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      {binding.pinnedRequestCount > 0 && (
        <p className="workspace-route-pin-note">
          {t("workspaceRoutes.pinned", {
            count: binding.pinnedRequestCount,
          })}
        </p>
      )}
    </li>
  );
}

export function ActivityRequestsRoutePage() {
  const { t } = useTranslation();
  const { actions, state } = useDashboardRuntime();
  return (
    <>
      <PageHeading
        description={t("activity.requests.description")}
        title={t("navigation.task.activityRequests")}
      />
      <ActivityPanel
        activities={state.activities}
        availability={sourceAvailability(state, "activities")}
        emptyAccessAction
        hasMore={state.activitiesHasMore}
        loadMoreErrorKey={state.activitiesLoadMoreErrorKey}
        loadingMore={state.activitiesLoadingMore}
        onLoadMore={actions.loadMoreActivities}
        pagingSafetyStopped={state.activitiesPagingSafetyStopped}
        title={t("activity.requests.listTitle")}
      />
    </>
  );
}

export function ActivityRequestRoutePage({
  exchangeId,
}: {
  readonly exchangeId: string;
}) {
  const { t } = useTranslation();
  const { model } = useDashboardRuntime();
  const detail = useExchangeDetail(model, exchangeId);
  const record = detail.data;
  const knownStatus =
    record !== undefined &&
    (record.status === "succeeded" ||
      record.status === "pending" ||
      record.status === "failed" ||
      record.status === "canceled");
  return (
    <>
      <PageHeading
        description={t("activity.detail.description")}
        title={t("navigation.task.activityRequest")}
      />
      <section
        aria-busy={detail.isPending}
        className="panel request-detail-panel"
      >
        {detail.isPending ? (
          <p className="empty-state">{t("app.loading")}</p>
        ) : detail.error !== null ? (
          <div className="request-detail-error" role="alert">
            <h2>{t("activity.detail.unavailable.title")}</h2>
            <p>{t(controlErrorKey(detail.error))}</p>
            <button
              className="secondary-action"
              onClick={() => void detail.refetch()}
              type="button"
            >
              {t("activity.detail.retry")}
            </button>
          </div>
        ) : record === undefined ? null : (
          <>
            <div className="request-detail-heading">
              <h2>{t("activity.request.summary", { id: record.id })}</h2>
              <span
                className={`activity-status ${knownStatus ? record.status : "neutral"}`}
              >
                {knownStatus
                  ? t(`activity.status.${record.status}`)
                  : record.status}
              </span>
            </div>
            <dl className="request-detail-grid">
              <div>
                <dt>{t("activity.access.label")}</dt>
                <dd className="identifier">{record.accessId}</dd>
              </div>
              <div>
                <dt>{t("activity.detail.result.label")}</dt>
                <dd className="identifier">
                  {record.processingTrace.result}
                </dd>
              </div>
              {record.processingTrace.upstreamProfileId === undefined ? null : (
                <div>
                  <dt>{t("activity.detail.profile.label")}</dt>
                  <dd className="identifier">
                    {record.processingTrace.upstreamProfileId}
                  </dd>
                </div>
              )}
              {record.processingTrace.credentialId === undefined ? null : (
                <div>
                  <dt>{t("activity.detail.credential.label")}</dt>
                  <dd className="identifier">
                    {record.processingTrace.credentialId}
                  </dd>
                </div>
              )}
              {record.processingTrace.egressProxyId === undefined ? null : (
                <div>
                  <dt>{t("activity.detail.proxy.label")}</dt>
                  <dd className="identifier">
                    {record.processingTrace.egressProxyId}
                  </dd>
                </div>
              )}
            </dl>
            <div className="request-trace-section">
              <h3>{t("activity.detail.attempts.title")}</h3>
              {record.processingTrace.attemptIds.length === 0 ? (
                <p className="empty-state">{t("activity.detail.attempts.empty")}</p>
              ) : (
                <ol className="identifier-list">
                  {record.processingTrace.attemptIds.map((attemptId) => (
                    <li className="identifier" key={attemptId}>
                      {attemptId}
                    </li>
                  ))}
                </ol>
              )}
            </div>
            <div className="request-trace-section">
              <h3>{t("activity.detail.plugins.title")}</h3>
              {record.processingTrace.pluginRunIds.length === 0 ? (
                <p className="empty-state">{t("activity.detail.plugins.empty")}</p>
              ) : (
                <ol className="identifier-list">
                  {record.processingTrace.pluginRunIds.map((pluginRunId) => (
                    <li className="identifier" key={pluginRunId}>
                      {pluginRunId}
                    </li>
                  ))}
                </ol>
              )}
            </div>
          </>
        )}
        <Link
          className="route-action request-detail-back"
          search={{}}
          to={dashboardTaskRoutePaths.activityRequests}
        >
          {t("activity.detail.back")}
        </Link>
      </section>
    </>
  );
}

export function SettingsRoutePage() {
  const { t } = useTranslation();
  const { actions, preview, state } = useDashboardRuntime();
  return (
    <>
      <PageHeading
        description={t("page.settings.description")}
        title={t("page.settings.title")}
      />
      <div className="system-layout">
        <StatusPanel
          availability={sourceAvailability(state, "status")}
          status={state.status}
        />
        <OfflinePanel
          actions={actions}
          availability={sourceAvailability(state, "offline")}
          busy={state.busy}
          snapshot={state.offline}
        />
        <TerminalCommandPanel preview={preview} />
      </div>
    </>
  );
}

type TerminalCommandMutation = "install" | "refresh" | "remove";

function TerminalCommandPanel({ preview }: { readonly preview: boolean }) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<TerminalCommandStatus>();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (preview) {
      return;
    }
    let active = true;
    setFailed(false);
    void inspectTerminalCommand().then(
      (next) => {
        if (active) {
          setStatus(next);
        }
      },
      () => {
        if (active) {
          setFailed(true);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [preview]);

  const mutate = async (operation: TerminalCommandMutation) => {
    setBusy(true);
    setFailed(false);
    try {
      const next = await (operation === "install"
        ? installTerminalCommand()
        : operation === "refresh"
          ? refreshTerminalCommand()
          : removeTerminalCommand());
      setStatus(next);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  const commandPath =
    status?.state === "current" ? status.targetPath : status?.sourcePath;
  const copyCommand = async () => {
    if (commandPath === undefined) {
      return;
    }
    try {
      await navigator.clipboard.writeText(shellQuoted(commandPath));
      setCopied(true);
      globalThis.setTimeout(() => setCopied(false), 2_000);
    } catch {
      setFailed(true);
    }
  };

  return (
    <section className="panel terminal-command-panel">
      <div className="section-heading">
        <div>
          <h2>{t("terminalCommand.title")}</h2>
          <p>{t("terminalCommand.description")}</p>
        </div>
        <span className={`terminal-command-state ${status?.state ?? "loading"}`}>
          {preview
            ? t("terminalCommand.state.desktopOnly")
            : failed
              ? t("terminalCommand.state.unavailable")
              : status === undefined
                ? t("common.data.loading")
                : t(`terminalCommand.state.${status.state}`)}
        </span>
      </div>
      {preview ? (
        <p className="boundary-note">{t("terminalCommand.preview")}</p>
      ) : (
        <>
          {commandPath !== undefined && (
            <div className="terminal-command-copy-row">
              <label htmlFor="terminal-command-path">
                {t("terminalCommand.command.label")}
              </label>
              <div>
                <input
                  id="terminal-command-path"
                  readOnly
                  spellCheck={false}
                  value={shellQuoted(commandPath)}
                />
                <button className="secondary" onClick={() => void copyCommand()} type="button">
                  {t(copied ? "common.copied" : "common.copy.action")}
                </button>
              </div>
              <p>{t("terminalCommand.command.help")}</p>
            </div>
          )}
          {status !== undefined && (
            <dl className="terminal-command-paths">
              <div>
                <dt>{t("terminalCommand.source.label")}</dt>
                <dd>{status.sourcePath}</dd>
              </div>
              <div>
                <dt>{t("terminalCommand.target.label")}</dt>
                <dd>{status.targetPath}</dd>
              </div>
            </dl>
          )}
          {failed && (
            <p className="inline-error" role="alert">
              {t("terminalCommand.error")}
            </p>
          )}
          <div className="button-row">
            {status?.state === "not_installed" && (
              <button disabled={busy} onClick={() => void mutate("install")} type="button">
                {t("terminalCommand.install.action")}
              </button>
            )}
            {status?.state === "source_updated" && (
              <button disabled={busy} onClick={() => void mutate("refresh")} type="button">
                {t("terminalCommand.refresh.action")}
              </button>
            )}
            {status !== undefined &&
              ["current", "source_updated", "source_missing", "target_missing"].includes(
                status.state,
              ) && (
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => void mutate("remove")}
                  type="button"
                >
                  {t("terminalCommand.remove.action")}
                </button>
              )}
          </div>
          <p className="boundary-note">{t("terminalCommand.boundary")}</p>
        </>
      )}
    </section>
  );
}

function shellQuoted(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function NavGroup({
  children,
  label,
}: {
  readonly children: ReactNode;
  readonly label: string;
}) {
  return (
    <div className="nav-group">
      <span className="nav-group-label">{label}</span>
      {children}
    </div>
  );
}

function NavItem({
  count,
  current,
  icon,
  id,
}: {
  readonly count?: number;
  readonly current: DashboardView;
  readonly icon: DashboardView;
  readonly id: DashboardView;
}) {
  const { t } = useTranslation();
  const label = t(`navigation.${id}`);
  return (
    <Link
      aria-current={current === id ? "page" : undefined}
      className="nav-item"
      search={{}}
      to={dashboardRoutePaths[id]}
      title={label}
    >
      <NavIcon name={icon} />
      <span className="nav-label">{label}</span>
      {count !== undefined && count > 0 && (
        <span className="nav-count">{count}</span>
      )}
    </Link>
  );
}

function NavIcon({ name }: { readonly name: DashboardView }) {
  const paths: Record<DashboardView, ReactNode> = {
    overview: <path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z" />,
    dashboards: <path d="M4 19V9M10 19V5M16 19v-7M22 19H2" />,
    access: (
      <path d="M4 7h10M14 7l-3-3M14 7l-3 3M20 17H10M10 17l3-3M10 17l3 3" />
    ),
    activity: <path d="M5 4v16M5 7h8M5 12h14M5 17h10" />,
    quality: <path d="M4 16l5-5 4 3 7-8M4 20h16" />,
    extensions: <path d="M8 3h8v5h5v8h-5v5H8v-5H3V8h5z" />,
    policy: (
      <path d="M12 3l8 3v5c0 5-3.2 8.2-8 10-4.8-1.8-8-5-8-10V6zM9 12l2 2 4-5" />
    ),
    settings: (
      <path d="M12 8a4 4 0 100 8 4 4 0 000-8zM12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6L7 7M17 17l1.4 1.4M18.4 5.6L17 7M7 17l-1.4 1.4" />
    ),
  };
  return (
    <svg aria-hidden="true" className="nav-icon" viewBox="0 0 24 24">
      {paths[name]}
    </svg>
  );
}

function PageHeading({
  action,
  description,
  title,
}: {
  readonly action?: ReactNode;
  readonly description: string;
  readonly title: string;
}) {
  return (
    <div className="page-heading">
      <div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {action}
    </div>
  );
}

function LocatorNotice({
  action,
  detail,
  role,
  title,
  to,
}: {
  readonly action: string;
  readonly detail: string;
  readonly role: "alert" | "status";
  readonly title: string;
  readonly to: (typeof dashboardRoutePaths)[DashboardView];
}) {
  const notice = useRef<HTMLElement>(null);
  useEffect(() => notice.current?.focus(), []);
  return (
    <section
      className="panel locator-notice"
      ref={notice}
      role={role}
      tabIndex={-1}
    >
      <h2>{title}</h2>
      <p>{detail}</p>
      <Link className="route-action" replace search={{}} to={to}>
        {action}
      </Link>
    </section>
  );
}

function UnavailablePage({
  feature,
}: {
  readonly feature: "dashboards" | "quality" | "extensions";
}) {
  const { t } = useTranslation();
  return (
    <>
      <PageHeading
        description={t(`page.${feature}.description`)}
        title={t(`page.${feature}.title`)}
      />
      <section className="panel boundary-page" role="status">
        <h2>{t("page.unavailable.title")}</h2>
        <p>{t(`page.${feature}.unavailable`)}</p>
        <p className="boundary-note">{t("page.unavailable.boundary")}</p>
        <Link
          className="route-action"
          search={{}}
          to={dashboardRoutePaths.overview}
        >
          {t("page.unavailable.exit")}
        </Link>
      </section>
    </>
  );
}

function OverviewPage({
  actions,
  onOpen,
  state,
}: {
  readonly actions: DashboardActions;
  readonly onOpen: DashboardNavigation["openView"];
  readonly state: DashboardState;
}) {
  const { t } = useTranslation();
  return (
    <>
      <PageHeading
        action={
          <button
            className="primary-action"
            onClick={() => onOpen("access")}
            type="button"
          >
            {t("overview.access.action")}
          </button>
        }
        description={t("page.overview.description")}
        title={t("page.overview.title")}
      />
      <RuntimeSummary
        offlineAvailability={sourceAvailability(state, "offline")}
        snapshot={state.offline}
        status={state.status}
        statusAvailability={sourceAvailability(state, "status")}
      />
      <DecisionMetricStrip onOpen={onOpen} state={state} />
      <FocusStage actions={actions} onOpen={onOpen} state={state} />
      <section
        className="overview-evidence"
        aria-labelledby="overview-evidence-title"
      >
        <div className="section-heading">
          <div>
            <h2 id="overview-evidence-title">{t("overview.evidence.title")}</h2>
            <p>{t("overview.evidence.description")}</p>
          </div>
          <button
            className="text-button"
            onClick={() => onOpen("activity")}
            type="button"
          >
            {t("overview.activity.action")}
          </button>
        </div>
        <div className="overview-evidence-grid">
          <CapturePanel
            availability={sourceAvailability(state, "captureRuns")}
            runs={state.captureRuns.slice(0, 2)}
          />
          <EgressPanel
            attempts={state.egressAttempts.slice(0, 2)}
            availability={sourceAvailability(state, "egressAttempts")}
          />
        </div>
      </section>
    </>
  );
}

function RuntimeSummary({
  offlineAvailability,
  snapshot,
  status,
  statusAvailability,
}: {
  readonly offlineAvailability: SourceAvailability;
  readonly snapshot: OfflineHoldSnapshot | undefined;
  readonly status: StatusResponse | undefined;
  readonly statusAvailability: SourceAvailability;
}) {
  const { t } = useTranslation();
  const ready = status?.ready === true;
  const holdState = snapshot?.state ?? "unbound";
  return (
    <section className={`runtime-summary ${ready ? "online" : "attention"}`}>
      <div className="runtime-summary-copy">
        <span className="state-dot" aria-hidden="true" />
        <div>
          <h2>
            {status === undefined
              ? t(
                  statusAvailability === "unavailable"
                    ? "common.data.unavailable"
                    : "app.loading",
                )
              : t(status.statusKey)}
          </h2>
          <p>
            {t(ready ? "overview.runtime.ready" : "overview.runtime.attention")}
          </p>
        </div>
      </div>
      <dl className="runtime-summary-facts">
        <div>
          <dt>{t("offlineHold.title")}</dt>
          <dd>
            {snapshot === undefined
              ? t(`common.data.${offlineAvailability}`)
              : t(`offlineHold.state.${holdState}`)}
          </dd>
        </div>
        <div>
          <dt>{t("offlineHold.safe.label")}</dt>
          <dd>
            {t(
              snapshot?.safeToDisconnect
                ? "offlineHold.safe.yes"
                : "offlineHold.safe.no",
            )}
          </dd>
        </div>
      </dl>
    </section>
  );
}

function DecisionMetricStrip({
  onOpen,
  state,
}: {
  readonly onOpen: DashboardNavigation["openView"];
  readonly state: DashboardState;
}) {
  const { t, i18n } = useTranslation();
  const observed = state.captureRuns.filter(
    (run) => run.observation === "observed",
  ).length;
  const completeEgress = state.egressAttempts.filter(
    (attempt) => attempt.terminal && attempt.outcome === "completed",
  ).length;
  const timeFormatter = new Intl.DateTimeFormat(i18n.language, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
  const metrics: readonly {
    label: string;
    value: string;
    source: DashboardSource;
    sourceLabel: string;
    view: DashboardView;
    tone?: string;
  }[] = [
    {
      label: t("overview.metric.runtime"),
      value: t(
        state.status?.ready
          ? "runtime.state.initialized"
          : "runtime.state.degraded",
      ),
      source: "status",
      sourceLabel: t("overview.metric.source.status"),
      view: "settings",
      tone: state.status?.ready ? "allow" : "ask",
    },
    {
      label: t("overview.metric.capture"),
      value: t("overview.metric.capture.value", {
        observed,
        total: state.captureRuns.length,
      }),
      source: "captureRuns",
      sourceLabel: t("overview.metric.source.capture"),
      view: "activity",
    },
    {
      label: t("overview.metric.pending"),
      value: String(state.approvals.length),
      source: "approvals",
      sourceLabel: t("overview.metric.source.approvals"),
      view: "policy",
      tone: state.approvals.length > 0 ? "ask" : "allow",
    },
    {
      label: t("overview.metric.egress"),
      value: `${completeEgress}/${state.egressAttempts.length}`,
      source: "egressAttempts",
      sourceLabel: t("overview.metric.source.egress"),
      view: "activity",
    },
  ];
  return (
    <section
      className="decision-metrics"
      aria-label={t("overview.metric.group")}
    >
      {metrics.map((metric) => {
        const refreshedAt = state.refreshedAtBySource[metric.source];
        const unavailable = state.unavailableSources.includes(metric.source);
        const freshness =
          refreshedAt === undefined
            ? t(
                unavailable
                  ? "overview.metric.evidence.unavailable"
                  : "overview.metric.evidence.loading",
                { source: metric.sourceLabel },
              )
            : t(
                unavailable
                  ? "overview.metric.evidence.stale"
                  : "overview.metric.evidence",
                {
                  source: metric.sourceLabel,
                  time: timeFormatter.format(new Date(refreshedAt)),
                },
              );
        const value =
          refreshedAt === undefined
            ? t(
                unavailable
                  ? "overview.metric.value.unavailable"
                  : "overview.metric.value.loading",
              )
            : metric.value;
        return (
          <button
            className={`decision-metric ${refreshedAt === undefined ? "" : (metric.tone ?? "")} ${unavailable ? "stale" : ""}`}
            key={metric.label}
            onClick={() => onOpen(metric.view)}
            type="button"
          >
            <span className="decision-metric-label">{metric.label}</span>
            <strong>{value}</strong>
            <span>{freshness}</span>
          </button>
        );
      })}
    </section>
  );
}

function FocusStage({
  actions,
  onOpen,
  state,
}: {
  readonly actions: DashboardActions;
  readonly onOpen: DashboardNavigation["openView"];
  readonly state: DashboardState;
}) {
  const { t, i18n } = useTranslation();
  const hold = state.offline;
  const approval = state.approvals[0];
  const attempt = state.egressAttempts[0];
  const offlineAvailability = sourceAvailability(state, "offline");
  if (
    hold !== undefined &&
    hold.state !== "online" &&
    hold.state !== "unbound"
  ) {
    return (
      <section className="focus-stage" aria-labelledby="focus-stage-title">
        <div className="focus-stage-main">
          <span className="focus-kicker ask">{t("offlineHold.title")}</span>
          <h2 id="focus-stage-title">{t(`offlineHold.state.${hold.state}`)}</h2>
          <p>
            {t("offlineHold.focus.detail", {
              active: hold.activeEgress,
              queued: hold.queuedRequests,
            })}
          </p>
        </div>
        <dl className="focus-facts">
          <div>
            <dt>{t("offlineHold.safe.label")}</dt>
            <dd>
              {t(
                hold.safeToDisconnect
                  ? "offlineHold.safe.yes"
                  : "offlineHold.safe.no",
              )}
            </dd>
          </div>
          <div>
            <dt>{t("offlineHold.heldBytes.label")}</dt>
            <dd>{hold.heldBytes}</dd>
          </div>
        </dl>
        <div className="focus-action">
          {hold.state === "held" ? (
            <button
              disabled={offlineAvailability !== "ready"}
              onClick={() => void actions.resumeOfflineHold()}
              type="button"
            >
              {t("offlineHold.resume.action")}
            </button>
          ) : (
            <span>{t("offlineHold.focus.wait")}</span>
          )}
        </div>
      </section>
    );
  }
  if (approval !== undefined) {
    const formatter = new Intl.DateTimeFormat(i18n.language, {
      dateStyle: "medium",
      timeStyle: "short",
    });
    return (
      <section className="focus-stage" aria-labelledby="focus-stage-title">
        <div className="focus-stage-main">
          <span className="focus-kicker ask">
            {t("overview.focus.pending")}
          </span>
          <h2 id="focus-stage-title">{t(approval.titleKey)}</h2>
          <p>{t(approval.summaryKey)}</p>
        </div>
        <dl className="focus-facts">
          <div>
            <dt>{t(subjectLabelKey(approval))}</dt>
            <dd>{approvalSubject(approval)}</dd>
          </div>
          <div>
            <dt>{t("approval.expiresAt.label")}</dt>
            <dd>{formatter.format(new Date(approval.expiresAt))}</dd>
          </div>
        </dl>
        <div className="focus-action">
          <button
            onClick={() =>
              onOpen("policy", { selectedApprovalId: approval.id })
            }
            type="button"
          >
            {t("overview.focus.review", { count: state.approvals.length })}
          </button>
          <span>{t("overview.focus.boundary")}</span>
        </div>
      </section>
    );
  }
  if (attempt === undefined) {
    return null;
  }
  return (
    <section className="focus-stage" aria-labelledby="focus-stage-title">
      <div className="focus-stage-main">
        <span className="focus-kicker">{t("overview.focus.latestEgress")}</span>
        <h2 id="focus-stage-title">{attempt.targetOrigin}</h2>
        <p>{t(`egress.purpose.${attempt.purpose}`)}</p>
      </div>
      <dl className="focus-facts">
        <div>
          <dt>{t("egress.outcome.label")}</dt>
          <dd>
            {attempt.terminal && attempt.outcome !== undefined
              ? t(`egress.outcome.${attempt.outcome}`)
              : t("egress.outcome.inFlight")}
          </dd>
        </div>
        <div>
          <dt>{t("egress.bytes.label")}</dt>
          <dd>
            {t("egress.bytes.value", {
              out: attempt.bytesOut,
              in: attempt.bytesIn,
            })}
          </dd>
        </div>
      </dl>
      <div className="focus-action">
        <button onClick={() => onOpen("activity")} type="button">
          {t("overview.activity.action")}
        </button>
      </div>
    </section>
  );
}

function StatusPanel({
  availability,
  status,
}: {
  readonly availability: SourceAvailability;
  readonly status: StatusResponse | undefined;
}) {
  const { t } = useTranslation();
  return (
    <section className="panel status-panel">
      <div className="section-heading">
        <h2>{t("status.title")}</h2>
        <span
          className={`state-dot ${status?.ready ? "online" : "attention"}`}
        />
      </div>
      {status === undefined ? (
        <p className="empty-state">{t(`common.data.${availability}`)}</p>
      ) : (
        <p className="hero-state">{t(status.statusKey)}</p>
      )}
    </section>
  );
}

function OfflinePanel({
  actions,
  availability,
  busy,
  snapshot,
}: {
  readonly actions: DashboardActions;
  readonly availability: SourceAvailability;
  readonly busy: boolean;
  readonly snapshot: OfflineHoldSnapshot | undefined;
}) {
  const { t } = useTranslation();
  const state = snapshot?.state ?? "unbound";
  return (
    <section className="panel offline-panel">
      <div className="section-heading">
        <h2>{t("offlineHold.title")}</h2>
        <span className={`hold-badge ${state}`}>
          {snapshot === undefined
            ? t(`common.data.${availability}`)
            : t(`offlineHold.state.${state}`)}
        </span>
      </div>
      {snapshot !== undefined && (
        <dl className="metrics compact">
          <div>
            <dt>{t("offlineHold.activeEgress.label")}</dt>
            <dd>{snapshot.activeEgress}</dd>
          </div>
          <div>
            <dt>{t("offlineHold.queuedRequests.label")}</dt>
            <dd>{snapshot.queuedRequests}</dd>
          </div>
        </dl>
      )}
      <div className="button-row">
        <button
          disabled={busy || availability !== "ready" || state !== "online"}
          onClick={() => void actions.enterOfflineHold()}
          type="button"
        >
          {t("offlineHold.enter.action")}
        </button>
        <button
          className="secondary"
          disabled={busy || availability !== "ready" || state !== "held"}
          onClick={() => void actions.resumeOfflineHold()}
          type="button"
        >
          {t("offlineHold.resume.action")}
        </button>
      </div>
    </section>
  );
}

const defaultClientOrigins: Record<
  AccessFormValues["clientDialect"],
  string
> = {
  "anthropic-messages": accessAppPresetDefaults.claude.clientOrigin,
  "openai-responses": accessAppPresetDefaults.codex.clientOrigin,
};

function accessPresetForItem(item: AccessDirectoryItem): AccessAppPreset {
  const origin = clientOriginIdentity(item.clientOrigin);
  if (
    item.clientDialect === "anthropic-messages" &&
    origin === clientOriginIdentity(accessAppPresetDefaults.claude.clientOrigin)
  ) {
    return "claude";
  }
  if (
    item.clientDialect === "openai-responses" &&
    origin === clientOriginIdentity(accessAppPresetDefaults.codex.clientOrigin)
  ) {
    return "codex";
  }
  return "custom";
}

type AccessDestinationPreset =
  | "anthropic"
  | "anthropic-compatible"
  | "openai"
  | "custom";

const accessDestinationDefaults = {
  anthropic: {
    authDriverRef: "anthropic_api_key",
    fixedModel: "claude-sonnet-4-5",
    providerDialect: "anthropic-messages",
    providerOrigin: "https://api.anthropic.com",
  },
  "anthropic-compatible": {
    authDriverRef: "anthropic_api_key",
    fixedModel: "claude-sonnet-4-5",
    providerDialect: "anthropic-messages",
    providerOrigin: "",
  },
  openai: {
    authDriverRef: "static_header",
    fixedModel: "gpt-5",
    providerDialect: "openai-chat",
    providerOrigin: "https://api.openai.com/v1",
  },
  custom: {
    authDriverRef: "static_header",
    fixedModel: "gpt-5",
    providerDialect: "openai-chat",
    providerOrigin: "",
  },
} as const satisfies Record<
  AccessDestinationPreset,
  Pick<
    AccessFormValues,
    "authDriverRef" | "fixedModel" | "providerDialect" | "providerOrigin"
  >
>;

interface AccessCandidateFormValues {
  readonly authDriverRef: "anthropic_api_key" | "static_header";
  readonly baseUrl: string;
  readonly model: string;
  readonly name: string;
  readonly provider: AccessCandidateProvider;
  readonly upstreamPresentation: "follow-client" | "claude-code";
}

const initialAccessCandidateForm: AccessCandidateFormValues = {
  authDriverRef: "anthropic_api_key",
  baseUrl: "",
  model: "",
  name: "",
  provider: "anthropic",
  upstreamPresentation: "follow-client",
};

function validProviderBaseUrl(value: string): boolean {
  try {
    const parsed = new URL(value.trim());
    const loopbackHTTP =
      parsed.protocol === "http:" &&
      (parsed.hostname === "::1" ||
        parsed.hostname === "[::1]" ||
        (() => {
          const octets = parsed.hostname.split(".");
          return (
            octets.length === 4 &&
            octets[0] === "127" &&
            octets.every(
              (octet) =>
                /^\d{1,3}$/u.test(octet) && Number(octet) <= 255,
            )
          );
        })());
    return (
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      (parsed.protocol === "https:" || loopbackHTTP)
    );
  } catch {
    return false;
  }
}

function compatibleEndpointPreview(
  baseUrl: string,
  provider: "anthropic-compatible" | "openai-compatible",
): string | undefined {
  if (baseUrl.length === 0) {
    return undefined;
  }
  const base = baseUrl.trim().replace(/\/+$/u, "");
  if (provider === "anthropic-compatible") {
    if (base.endsWith("/v1/messages")) {
      return base;
    }
    return base.endsWith("/v1") ? `${base}/messages` : `${base}/v1/messages`;
  }
  return base.endsWith("/chat/completions")
    ? base
    : `${base}/chat/completions`;
}

function validAccessCandidateForm(values: AccessCandidateFormValues): boolean {
  const requiresBaseUrl =
    values.provider === "anthropic-compatible" ||
    values.provider === "openai-compatible";
  return (
    values.name.trim() === values.name &&
    values.name.length > 0 &&
    values.model.trim() === values.model &&
    values.model.length > 0 &&
    (values.upstreamPresentation === "follow-client" ||
      values.upstreamPresentation === "claude-code") &&
    (!requiresBaseUrl || validProviderBaseUrl(values.baseUrl))
  );
}

function accessCandidateInput(
  values: AccessCandidateFormValues,
): AccessAddCandidateInput {
  if (!validAccessCandidateForm(values)) {
    throw new Error("Access candidate form is incomplete");
  }
  switch (values.provider) {
    case "anthropic":
      return {
        model: values.model,
        name: values.name,
        provider: "anthropic",
        upstreamPresentation: values.upstreamPresentation,
      };
    case "anthropic-compatible":
      return {
        ...(values.authDriverRef === "anthropic_api_key"
          ? {}
          : { authDriverRef: values.authDriverRef }),
        baseUrl: values.baseUrl.trim().replace(/\/+$/u, ""),
        model: values.model,
        name: values.name,
        provider: "anthropic-compatible",
        upstreamPresentation: values.upstreamPresentation,
      };
    case "openai":
      return {
        model: values.model,
        name: values.name,
        provider: "openai",
        upstreamPresentation: values.upstreamPresentation,
      };
    case "openai-compatible":
      return {
        ...(values.authDriverRef === "static_header"
          ? {}
          : { authDriverRef: values.authDriverRef }),
        baseUrl: values.baseUrl.trim().replace(/\/+$/u, ""),
        model: values.model,
        name: values.name,
        provider: "openai-compatible",
        upstreamPresentation: values.upstreamPresentation,
      };
  }
}

function activeAccessProfileId(detail: AccessDetail): string | undefined {
  const defaultRouteSet =
    detail.routeSets.find(({ id }) => id === detail.access.defaultRouteSetId) ??
    detail.routeSets[0];
  return defaultRouteSet?.candidateProfileIds[0];
}

function candidateProviderForTarget(
  target: AccessDetail["providerTargets"][number],
): AccessCandidateProvider {
  let origin: string | undefined;
  try {
    origin = new URL(target.origin).origin;
  } catch {
    // A malformed durable target is already surfaced by the control contract;
    // keep the visible route categorized as a compatible service here.
  }
  if (target.protocol === "anthropic-messages") {
    return origin === "https://api.anthropic.com"
      ? "anthropic"
      : "anthropic-compatible";
  }
  return origin === "https://api.openai.com"
    ? "openai"
    : "openai-compatible";
}

function AccessAppIcon({ preset }: { readonly preset: AccessAppPreset }) {
  if (preset === "claude") {
    return (
      <svg
        aria-hidden="true"
        className="access-app-icon"
        viewBox="0 0 32 32"
      >
        <path d="M16 3v10M16 19v10M3 16h10M19 16h10M6.8 6.8l7.1 7.1M18.1 18.1l7.1 7.1M25.2 6.8l-7.1 7.1M13.9 18.1l-7.1 7.1" />
      </svg>
    );
  }
  if (preset === "codex") {
    return (
      <svg
        aria-hidden="true"
        className="access-app-icon"
        viewBox="0 0 32 32"
      >
        <path d="M16 4.5a7.2 7.2 0 0 1 6.2 3.6 7.2 7.2 0 0 1 4.6 10.7 7.2 7.2 0 0 1-8.1 8.2 7.2 7.2 0 0 1-10.9-4.5A7.2 7.2 0 0 1 9.1 10 7.2 7.2 0 0 1 16 4.5Z" />
        <path d="m11.2 13.2 4.8-2.8 4.8 2.8v5.6L16 21.6l-4.8-2.8Z" />
      </svg>
    );
  }
  return (
    <svg aria-hidden="true" className="access-app-icon" viewBox="0 0 32 32">
      <path d="M6 9h20M6 16h20M6 23h20" />
      <circle cx="12" cy="9" r="2.5" />
      <circle cx="21" cy="16" r="2.5" />
      <circle cx="15" cy="23" r="2.5" />
    </svg>
  );
}

function AccessProviderIcon({
  provider,
}: {
  readonly provider: AccessCandidateProvider | AccessDestinationPreset;
}) {
  return (
    <AccessAppIcon
      preset={provider.startsWith("anthropic") ? "claude" : "codex"}
    />
  );
}

function defaultCredentialCoordinates(
  detail: AccessDetail,
): AccessCredentialCoordinates | undefined {
  const activeProfileId = activeAccessProfileId(detail);
  const profiles = [
    ...detail.profiles.filter(({ id }) => id === activeProfileId),
    ...detail.profiles.filter(({ id }) => id !== activeProfileId),
  ];
  for (const profile of profiles) {
    const binding = detail.accountBindings.find(
      ({ enabled, id, profileId }) =>
        enabled &&
        id === profile.defaultAccountBindingId &&
        profileId === profile.id,
    );
    if (binding !== undefined) {
      return {
        accessId: detail.access.id,
        profileId: profile.id,
        credentialId: binding.id,
      };
    }
  }
  return undefined;
}

interface PendingAccessCandidate {
  readonly coordinates: AccessCredentialCoordinates;
  readonly expectedRevision: number;
  readonly phase: "secret" | "activate";
}

function AccessPanel({
  actions,
  busy,
  model,
}: {
  readonly actions: DashboardActions;
  readonly busy: boolean;
  readonly model: DashboardQueryRuntime;
}) {
  const { t } = useTranslation();
  const directory = useAccessDirectory(model);
  const [form, setForm] = useState<AccessFormValues>(initialAccessForm);
  const [accessPreset, setAccessPreset] =
    useState<AccessAppPreset>("claude");
  const [destinationPreset, setDestinationPreset] =
    useState<AccessDestinationPreset>();
  const [automaticName, setAutomaticName] = useState(true);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [secret, setSecret] = useState("");
  const [credentialSaveFailed, setCredentialSaveFailed] = useState(false);
  const [candidateEditorOpen, setCandidateEditorOpen] = useState(false);
  const [candidateForm, setCandidateForm] =
    useState<AccessCandidateFormValues>(initialAccessCandidateForm);
  const [candidateAutomaticName, setCandidateAutomaticName] = useState(true);
  const [candidateAutomaticModel, setCandidateAutomaticModel] = useState(true);
  const [candidateSecret, setCandidateSecret] = useState("");
  const [candidateSaveFailed, setCandidateSaveFailed] =
    useState<"add" | "secret" | "activate">();
  const [pendingCandidate, setPendingCandidate] =
    useState<PendingAccessCandidate>();
  const [creating, setCreating] = useState(false);
  const [selectedAccessId, setSelectedAccessId] = useState<string>();
  const [pendingAccess, setPendingAccess] = useState<AccessDirectoryItem>();
  const [selectionState, setSelectionState] =
    useState<"idle" | "loading" | "ready" | "unavailable">("idle");
  const [activeRevision, setActiveRevision] = useState<number>();
  const [lastApplicationState, setLastApplicationState] =
    useState<"active" | "unavailable">();
  const [activePlanAvailable, setActivePlanAvailable] = useState<boolean>();
  const [loadedDetail, setLoadedDetail] = useState<AccessDetail>();
  const [loadedCredential, setLoadedCredential] =
    useState<AccessCredentialCoordinates>();
  const [loadedProfileCount, setLoadedProfileCount] = useState(0);
  const serverItems = directory.data?.items ?? [];
  const visibleItems =
    pendingAccess === undefined ||
    serverItems.some(({ accessId }) => accessId === pendingAccess.accessId)
      ? serverItems
      : [...serverItems, pendingAccess].sort((left, right) =>
          compareResourceIds(left.accessId, right.accessId),
        );
  const candidateClientOrigin = clientOriginIdentity(form.clientOrigin);
  const conflictingAccess = creating
    ? visibleItems.find(
        ({ clientOrigin }) =>
          candidateClientOrigin !== undefined &&
          clientOriginIdentity(clientOrigin) === candidateClientOrigin,
      )
    : undefined;
  const accessIdentity = useRef({
    accessId: "",
    generation: 0,
  });
  const credentialCoordinatesWithAccess = loadedCredential;
  const credentialQuery = useCredentialMetadata(
    model,
    credentialCoordinatesWithAccess,
  );

  const setField =
    (field: keyof AccessFormValues) =>
    (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      if (field === "name") {
        setAutomaticName(false);
      }
      setForm((current) => ({ ...current, [field]: event.target.value }));
    };

  const setClientDialect = (event: ChangeEvent<HTMLSelectElement>) => {
    const dialect = event.target.value;
    if (dialect !== "anthropic-messages" && dialect !== "openai-responses") {
      return;
    }
    setAccessPreset("custom");
    setForm((current) => {
      const knownOrigin = Object.values(defaultClientOrigins).includes(
        current.clientOrigin,
      );
      return {
        ...current,
        clientDialect: dialect,
        clientOrigin: knownOrigin
          ? defaultClientOrigins[dialect]
          : current.clientOrigin,
      };
    });
  };

  const beginAccessGeneration = (accessId: string): number => {
    const generation = accessIdentity.current.generation + 1;
    accessIdentity.current = {
      accessId,
      generation,
    };
    return generation;
  };

  const resetDetail = () => {
    setSecret("");
    setCredentialSaveFailed(false);
    setCandidateEditorOpen(false);
    setCandidateForm(initialAccessCandidateForm);
    setCandidateAutomaticName(true);
    setCandidateAutomaticModel(true);
    setCandidateSecret("");
    setCandidateSaveFailed(undefined);
    setPendingCandidate(undefined);
    setActiveRevision(undefined);
    setLastApplicationState(undefined);
    setActivePlanAvailable(undefined);
    setLoadedDetail(undefined);
    setLoadedCredential(undefined);
    setLoadedProfileCount(0);
    setSelectionState("idle");
  };

  const currentAccessIdentity = (
    accessId: string,
    generation: number,
  ): boolean =>
    accessIdentity.current.accessId === accessId &&
    accessIdentity.current.generation === generation;

  const acceptLoadedAccess = (
    accessId: string,
    generation: number,
    result: AccessLoadResult,
  ): boolean => {
    if (!currentAccessIdentity(accessId, generation)) {
      return false;
    }
    setActiveRevision(result.detail.revision);
    setLastApplicationState(undefined);
    setActivePlanAvailable(
      result.detail.access.status === "enabled"
        ? result.plan !== undefined
        : undefined,
    );
    setLoadedDetail(result.detail);
    setLoadedCredential(
      result.plan === undefined
        ? undefined
        : defaultCredentialCoordinates(result.detail),
    );
    setLoadedProfileCount(result.detail.profiles.length);
    setSelectionState("ready");
    return true;
  };

  const reloadAccess = async (
    accessId: string,
    generation: number,
  ): Promise<AccessLoadResult | undefined> => {
    const result = await actions.loadAccess(accessId);
    if (result === undefined || !acceptLoadedAccess(accessId, generation, result)) {
      return undefined;
    }
    return result;
  };

  const selectAccess = async (item: AccessDirectoryItem) => {
    const accessId = item.accessId;
    const identityGeneration = beginAccessGeneration(accessId);
    setCreating(false);
    setPendingAccess(undefined);
    setSelectedAccessId(accessId);
    setSecret("");
    setCredentialSaveFailed(false);
    setCandidateEditorOpen(false);
    setCandidateForm(initialAccessCandidateForm);
    setCandidateAutomaticName(true);
    setCandidateAutomaticModel(true);
    setCandidateSecret("");
    setCandidateSaveFailed(undefined);
    setPendingCandidate(undefined);
    setActiveRevision(item.revision);
    setLastApplicationState(undefined);
    setLoadedCredential(undefined);
    setLoadedProfileCount(0);
    setSelectionState("loading");
    if ((await reloadAccess(accessId, identityGeneration)) !== undefined) {
      return;
    }
    if (currentAccessIdentity(accessId, identityGeneration)) {
      setSelectionState("unavailable");
    }
  };

  const beginCreate = () => {
    const configuredOrigins = new Set(
      visibleItems
        .map(({ clientOrigin }) => clientOriginIdentity(clientOrigin))
        .filter((identity) => identity !== undefined),
    );
    const preferredPreset: AccessAppPreset = !configuredOrigins.has(
      clientOriginIdentity(defaultClientOrigins["anthropic-messages"]) ?? "",
    )
      ? "claude"
      : !configuredOrigins.has(
            clientOriginIdentity(defaultClientOrigins["openai-responses"]) ??
              "",
          )
        ? "codex"
        : "custom";
    const next = newAccessForm(accessAppPresetDefaults[preferredPreset]);
    beginAccessGeneration(next.accessId);
    resetDetail();
    setPendingAccess(undefined);
    setSelectedAccessId(undefined);
    setAccessPreset(preferredPreset);
    setDestinationPreset(undefined);
    setAutomaticName(true);
    setAdvancedOpen(preferredPreset === "custom");
    setForm({
      ...next,
      fixedModel: "",
      name: t(`access.preset.${preferredPreset}.defaultName`),
      providerOrigin: "",
      routeName: "",
    });
    setCreating(true);
  };

  const configuredAccessForPreset = (
    preset: Exclude<AccessAppPreset, "custom">,
  ): AccessDirectoryItem | undefined => {
    const presetOrigin = clientOriginIdentity(
      accessAppPresetDefaults[preset].clientOrigin,
    );
    return visibleItems.find(
      ({ clientOrigin }) =>
        presetOrigin !== undefined &&
        clientOriginIdentity(clientOrigin) === presetOrigin,
    );
  };

  const chooseAccessPreset = (preset: AccessAppPreset) => {
    if (preset !== "custom") {
      const configured = configuredAccessForPreset(preset);
      if (configured !== undefined) {
        void selectAccess(configured);
        return;
      }
    }
    setDestinationPreset(undefined);
    setAccessPreset(preset);
    setAdvancedOpen(preset === "custom");
    setForm((current) => ({
      ...applyAccessAppPreset(current, preset),
      fixedModel: "",
      name: automaticName
        ? t(`access.preset.${preset}.defaultName`)
        : current.name,
      providerOrigin: "",
      routeName: "",
    }));
  };

  const chooseDestinationPreset = (preset: AccessDestinationPreset) => {
    setDestinationPreset(preset);
    setForm((current) => ({
      ...current,
      ...accessDestinationDefaults[preset],
      routeName: t(`access.destination.${preset}.defaultRouteName`),
    }));
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (
      destinationPreset === undefined ||
      secret.length === 0 ||
      !validAccessForm(form) ||
      conflictingAccess !== undefined
    ) {
      return;
    }
    const submittedForm = form;
    const submittedSecret = secret;
    const accessId = submittedForm.accessId;
    const identityGeneration = accessIdentity.current.generation;
    const input = buildAccessApplyInput(submittedForm);
    const result = await actions.applyAccess(
      accessId,
      input,
    );
    if (
      result !== undefined &&
      currentAccessIdentity(accessId, identityGeneration)
    ) {
      const coordinates = {
        accessId,
        ...credentialCoordinates(submittedForm),
      };
      const credential = await actions.replaceCredentialSecret(
        coordinates,
        submittedSecret,
      );
      if (!currentAccessIdentity(accessId, identityGeneration)) {
        return;
      }
      if (credential !== undefined) {
        setSecret("");
      }
      setCredentialSaveFailed(credential === undefined);
      const created: AccessDirectoryItem = {
        accessId,
        name: submittedForm.name,
        description: submittedForm.description,
        status: "enabled",
        revision: result.revision,
        clientOrigin: input.agentEndpoint.clientOrigin,
        clientDialect: submittedForm.clientDialect,
      };
      setPendingAccess(created);
      setSelectedAccessId(accessId);
      setCreating(false);
      setSelectionState("ready");
      setActiveRevision(result.revision);
      setLastApplicationState(result.applicationState);
      setActivePlanAvailable(result.applicationState === "active");
      setLoadedDetail(undefined);
      setLoadedCredential(coordinates);
      setLoadedProfileCount(1);
      setForm((current) =>
        current.accessId !== accessId
          ? current
          : {
              ...current,
              expectedRevision: String(result.revision),
            },
      );
    }
  };

  const submitCredential = async (event: FormEvent) => {
    event.preventDefault();
    if (credentialCoordinatesWithAccess === undefined || secret.length === 0) {
      return;
    }
    const coordinates = credentialCoordinatesWithAccess;
    const identityGeneration = accessIdentity.current.generation;
    const submittedSecret = secret;
    const result = await actions.replaceCredentialSecret(
      coordinates,
      submittedSecret,
    );
    if (
      result !== undefined &&
      currentAccessIdentity(coordinates.accessId, identityGeneration)
    ) {
      setSecret((current) => (current === submittedSecret ? "" : current));
      setCredentialSaveFailed(false);
    }
  };
  const activeCredential =
    loadedCredential === undefined ? undefined : credentialQuery.data;
  const credentialUnavailable = credentialQuery.error !== null;
  const selectedAccess = visibleItems.find(
    ({ accessId }) => accessId === selectedAccessId,
  );
  const selectedDetail =
    loadedDetail?.access.id === selectedAccess?.accessId
      ? loadedDetail
      : undefined;
  const selectedStatus = selectedDetail?.access.status ?? selectedAccess?.status;
  const selectedRouteSet =
    selectedDetail?.routeSets.find(
      ({ id }) => id === selectedDetail.access.defaultRouteSetId,
    ) ?? selectedDetail?.routeSets[0];
  const selectedCandidateProfileIds = new Set(
    selectedRouteSet?.candidateProfileIds ?? [],
  );
  const selectedActiveProfileId = selectedRouteSet?.candidateProfileIds[0];
  const selectedActiveProfile = selectedDetail?.profiles.find(
    ({ id }) => id === selectedActiveProfileId,
  );
  const creationDestinationPresets: readonly AccessDestinationPreset[] =
    accessPreset === "codex"
      ? ["openai", "custom"]
      : ["anthropic", "anthropic-compatible", "openai", "custom"];
  const candidateProviderPresets: readonly AccessCandidateProvider[] =
    selectedDetail?.agentEndpoint.clientDialect === "openai-responses"
      ? ["openai", "openai-compatible"]
      : [
          "anthropic",
          "anthropic-compatible",
          "openai",
          "openai-compatible",
        ];
  const displayAccessName = (item: AccessDirectoryItem): string =>
    /^\d+$/u.test(item.name)
      ? t(
          `access.preset.${accessPresetForItem(item)}.defaultName`,
        )
      : item.name;
  const defaultCandidateName = (provider: AccessCandidateProvider): string =>
    t(`access.candidates.defaultName.${provider}`, {
      number: (selectedDetail?.profiles.length ?? 0) + 1,
    });
  const defaultCandidateModel = (provider: AccessCandidateProvider): string =>
    provider.startsWith("anthropic") ? "claude-sonnet-4-5" : "gpt-5";

  const closeCandidateEditor = () => {
    setCandidateEditorOpen(false);
    setCandidateForm(initialAccessCandidateForm);
    setCandidateAutomaticName(true);
    setCandidateAutomaticModel(true);
    setCandidateSecret("");
    setCandidateSaveFailed(undefined);
    setPendingCandidate(undefined);
  };

  const beginCandidateAdd = () => {
    const provider =
      selectedDetail?.agentEndpoint.clientDialect === "openai-responses"
        ? "openai"
        : "anthropic";
    setCandidateForm({
      ...initialAccessCandidateForm,
      authDriverRef:
        provider === "openai" ? "static_header" : "anthropic_api_key",
      model: defaultCandidateModel(provider),
      name: defaultCandidateName(provider),
      provider,
    });
    setCandidateAutomaticName(true);
    setCandidateAutomaticModel(true);
    setCandidateSecret("");
    setCandidateSaveFailed(undefined);
    setPendingCandidate(undefined);
    setCandidateEditorOpen(true);
  };

  const beginCandidateSetup = (profileId: string) => {
    if (selectedDetail === undefined) {
      return;
    }
    const profile = selectedDetail.profiles.find(({ id }) => id === profileId);
    const target = selectedDetail.providerTargets.find(
      ({ profileId: targetProfileId }) => targetProfileId === profileId,
    );
    const binding = selectedDetail.accountBindings.find(
      ({ id, profileId: bindingProfileId }) =>
        bindingProfileId === profileId &&
        id === profile?.defaultAccountBindingId,
    );
    if (profile === undefined || target === undefined || binding === undefined) {
      return;
    }
    const provider = candidateProviderForTarget(target);
    const compatible =
      provider === "anthropic-compatible" ||
      provider === "openai-compatible";
    setCandidateForm({
      authDriverRef:
        binding.authDriverRef === "static_header"
          ? "static_header"
          : "anthropic_api_key",
      baseUrl: compatible ? target.origin : "",
      model:
        profile.defaultModelPolicy.mode === "fixed"
          ? profile.defaultModelPolicy.fixedModel
          : "",
      name: profile.name,
      provider,
      upstreamPresentation:
        profile.upstreamWireProfileRef === "claude-code"
          ? "claude-code"
          : "follow-client",
    });
    setCandidateAutomaticName(false);
    setCandidateAutomaticModel(false);
    setCandidateSecret("");
    setCandidateSaveFailed(undefined);
    setPendingCandidate({
      coordinates: {
        accessId: selectedDetail.access.id,
        credentialId: binding.id,
        profileId,
      },
      expectedRevision: selectedDetail.revision,
      phase: "secret",
    });
    setCandidateEditorOpen(true);
  };

  const selectCandidate = async (
    profileId: string,
    expectedRevision = selectedDetail?.revision,
  ): Promise<boolean> => {
    if (selectedDetail === undefined || expectedRevision === undefined) {
      return false;
    }
    const accessId = selectedDetail.access.id;
    const identityGeneration = accessIdentity.current.generation;
    setCandidateSaveFailed(undefined);
    const result = await actions.selectAccessCandidate(
      accessId,
      profileId,
      expectedRevision,
    );
    if (
      result === undefined ||
      !currentAccessIdentity(accessId, identityGeneration)
    ) {
      setCandidateSaveFailed("activate");
      return false;
    }
    setActiveRevision(result.revision);
    setLastApplicationState(result.applicationState);
    const loaded = await reloadAccess(accessId, identityGeneration);
    if (loaded === undefined) {
      setCandidateSaveFailed("activate");
      return false;
    }
    return true;
  };

  const submitCandidate = async (event: FormEvent) => {
    event.preventDefault();
    if (
      selectedDetail === undefined ||
      selectedDetail.access.status !== "enabled" ||
      (!validAccessCandidateForm(candidateForm) && pendingCandidate === undefined)
    ) {
      return;
    }
    const accessId = selectedDetail.access.id;
    const identityGeneration = accessIdentity.current.generation;
    let candidate = pendingCandidate;
    if (candidate === undefined) {
      if (candidateSecret.length === 0) {
        return;
      }
      const added = await actions.addAccessCandidate(
        accessId,
        selectedDetail.revision,
        accessCandidateInput(candidateForm),
      );
      if (
        added === undefined ||
        !currentAccessIdentity(accessId, identityGeneration)
      ) {
        setCandidateSaveFailed("add");
        return;
      }
      candidate = {
        coordinates: {
          accessId,
          credentialId: added.candidate.credentialId,
          profileId: added.candidate.profileId,
        },
        expectedRevision: added.revision,
        phase: "secret",
      };
      setPendingCandidate(candidate);
      setActiveRevision(added.revision);
      setLastApplicationState(added.applicationState);
    }
    if (candidate.phase === "secret") {
      if (candidateSecret.length === 0) {
        return;
      }
      const credential = await actions.replaceCredentialSecret(
        candidate.coordinates,
        candidateSecret,
      );
      if (
        credential === undefined ||
        !currentAccessIdentity(accessId, identityGeneration)
      ) {
        setCandidateSaveFailed("secret");
        await reloadAccess(accessId, identityGeneration);
        return;
      }
      setCandidateSecret("");
      candidate = { ...candidate, phase: "activate" };
      setPendingCandidate(candidate);
    }
    if (
      !(await selectCandidate(
        candidate.coordinates.profileId,
        candidate.expectedRevision,
      ))
    ) {
      setPendingCandidate({ ...candidate, phase: "activate" });
      return;
    }
    closeCandidateEditor();
  };

  return (
    <div className={`access-layout ${creating ? "creating" : ""}`}>
      {!creating && (
        <section className="panel access-directory-panel">
        <div className="section-heading">
          <div>
            <h2>{t("access.directory.title")}</h2>
            <p>{t("access.directory.description")}</p>
          </div>
          <button disabled={busy} onClick={beginCreate} type="button">
            {t("access.create.action")}
          </button>
        </div>
        {directory.isPending && visibleItems.length === 0 ? (
          <p className="empty-state">{t("common.data.loading")}</p>
        ) : directory.error !== null && visibleItems.length === 0 ? (
          <p className="empty-state">{t("common.data.unavailable")}</p>
        ) : visibleItems.length === 0 ? (
          <div className="access-directory-empty">
            <p>{t("access.directory.empty")}</p>
            <button disabled={busy} onClick={beginCreate} type="button">
              {t("access.directory.emptyAction")}
            </button>
          </div>
        ) : (
          <ul className="access-directory-list">
            {visibleItems.map((item) => (
              <li key={item.accessId}>
                <button
                  aria-pressed={item.accessId === selectedAccessId}
                  className={
                    item.accessId === selectedAccessId ? "selected" : ""
                  }
                  disabled={busy}
                  onClick={() => void selectAccess(item)}
                  type="button"
                >
                  <span className="access-directory-entry">
                    <span
                      className={`access-app-mark ${accessPresetForItem(item)}`}
                    >
                      <AccessAppIcon
                        preset={accessPresetForItem(item)}
                      />
                    </span>
                    <span className="access-directory-copy">
                      <span className="access-directory-name">
                        <strong>{displayAccessName(item)}</strong>
                        <span className={`access-status ${item.status}`}>
                          {t(`access.status.${item.status}`)}
                        </span>
                      </span>
                      <span>
                        {t(
                          `access.preset.${accessPresetForItem(item)}.directoryDescription`,
                        )}
                      </span>
                    </span>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
        </section>
      )}

      {creating ? (
        <section className="panel access-panel access-create-panel">
          <div className="section-heading">
            <div>
              <h2>{t("access.create.title")}</h2>
              <p>{t("access.create.description")}</p>
            </div>
          </div>
          <form className="access-form" onSubmit={(event) => void submit(event)}>
            <div className="form-action wide-action access-create-actions">
              <button
                className="secondary"
                disabled={busy}
                onClick={() => {
                  setCreating(false);
                  resetDetail();
                }}
                type="button"
              >
                {t("common.cancel.action")}
              </button>
              <button
                disabled={
                  busy ||
                  destinationPreset === undefined ||
                  secret.length === 0 ||
                  !validAccessForm(form) ||
                  conflictingAccess !== undefined
                }
                type="submit"
              >
                {t("access.create.submit")}
              </button>
            </div>
            <fieldset className="access-app-picker wide-action">
              <legend>{t("access.preset.title")}</legend>
              <p>{t("access.preset.description")}</p>
              <div className="access-app-options">
                {(["claude", "codex", "custom"] as const).map((preset) => {
                  const configured =
                    preset === "custom"
                      ? undefined
                      : configuredAccessForPreset(preset);
                  return (
                    <button
                      aria-pressed={
                        configured === undefined && accessPreset === preset
                      }
                      className={`access-app-option ${preset}`}
                      disabled={busy}
                      key={preset}
                      onClick={() => chooseAccessPreset(preset)}
                      type="button"
                    >
                      <span className={`access-app-mark ${preset}`}>
                        <AccessAppIcon preset={preset} />
                      </span>
                      <span className="access-app-option-copy">
                        <strong>{t(`access.preset.${preset}.name`)}</strong>
                        <span>{t(`access.preset.${preset}.description`)}</span>
                        {configured !== undefined && (
                          <span className="access-app-configured">
                            {t("access.preset.configured", {
                              name: configured.name,
                            })}
                          </span>
                        )}
                      </span>
                    </button>
                  );
                })}
              </div>
            </fieldset>
            <div className="access-create-step wide-action">
              <span aria-hidden="true">2</span>
              <div>
                <strong>{t("access.destination.stepTitle")}</strong>
                <p>{t("access.destination.stepDescription")}</p>
              </div>
            </div>
            {accessPreset === "claude" && (
              <p className="access-destination-note wide-action">
                {t("access.destination.claudeNotice")}
              </p>
            )}
            <div className="access-destination-options wide-action">
              {creationDestinationPresets.map((preset) => (
                <button
                  aria-pressed={destinationPreset === preset}
                  className="access-destination-option"
                  disabled={busy}
                  key={preset}
                  onClick={() => chooseDestinationPreset(preset)}
                  type="button"
                >
                  <span className={`access-service-mark ${preset}`} aria-hidden="true">
                    <AccessProviderIcon provider={preset} />
                  </span>
                  <span>
                    <strong>{t(`access.destination.${preset}.name`)}</strong>
                    <small>{t(`access.destination.${preset}.description`)}</small>
                  </span>
                </button>
              ))}
            </div>
            {destinationPreset !== undefined && (
              <div className="access-service-fields wide-action">
                {(destinationPreset === "anthropic-compatible" ||
                  destinationPreset === "custom") && (
                  <LabeledInput
                    disabled={busy}
                    label={t("access.service.address.label")}
                    onChange={setField("providerOrigin")}
                    placeholder={t("access.service.address.placeholder")}
                    required
                    type="url"
                    value={form.providerOrigin}
                  />
                )}
                {(destinationPreset === "anthropic-compatible" ||
                  destinationPreset === "custom") && (
                  <p className="access-candidate-endpoint">
                    {compatibleEndpointPreview(
                      form.providerOrigin,
                      destinationPreset === "anthropic-compatible"
                        ? "anthropic-compatible"
                        : "openai-compatible",
                    ) === undefined
                      ? t(
                          `access.candidates.endpoint.${
                            destinationPreset === "anthropic-compatible"
                              ? "anthropic-compatible"
                              : "openai-compatible"
                          }.help`,
                        )
                      : t("access.candidates.endpoint.preview", {
                          endpoint: compatibleEndpointPreview(
                            form.providerOrigin,
                            destinationPreset === "anthropic-compatible"
                              ? "anthropic-compatible"
                              : "openai-compatible",
                          ),
                        })}
                  </p>
                )}
                <LabeledInput
                  disabled={busy}
                  label={t("access.candidates.name.label")}
                  onChange={setField("routeName")}
                  required
                  value={form.routeName}
                />
                <div className="field">
                  <label htmlFor="access-fixed-model">
                    {t("access.fixedModel.label")}
                  </label>
                  <input
                    aria-describedby="access-fixed-model-help"
                    disabled={busy}
                    id="access-fixed-model"
                    onChange={setField("fixedModel")}
                    placeholder={t(
                      destinationPreset === "anthropic" ||
                        destinationPreset === "anthropic-compatible"
                        ? "access.candidates.model.placeholder"
                        : "access.fixedModel.placeholder",
                    )}
                    required
                    value={form.fixedModel}
                  />
                  <small id="access-fixed-model-help">
                    {t("access.fixedModel.help")}
                  </small>
                </div>
                <label className="field">
                  <span>{t("access.candidates.presentation.label")}</span>
                  <select
                    disabled={busy}
                    onChange={(event) =>
                      setForm((current) => ({
                        ...current,
                        upstreamPresentation:
                          event.target.value === "claude-code"
                            ? "claude-code"
                            : "follow-client",
                      }))
                    }
                    value={form.upstreamPresentation}
                  >
                    <option value="follow-client">
                      {t("access.candidates.presentation.followClient")}
                    </option>
                    <option value="claude-code">
                      {t("access.candidates.presentation.claudeCode")}
                    </option>
                  </select>
                  <small>{t("access.candidates.presentation.help")}</small>
                </label>
                {destinationPreset === "anthropic-compatible" && (
                  <label className="field">
                    <span>{t("access.candidates.auth.label")}</span>
                    <select
                      disabled={busy}
                      onChange={(event) =>
                        setForm((current) => ({
                          ...current,
                          authDriverRef:
                            event.target.value === "static_header"
                              ? "static_header"
                              : "anthropic_api_key",
                        }))
                      }
                      value={form.authDriverRef}
                    >
                      <option value="anthropic_api_key">
                        {t("access.candidates.auth.xApiKey")}
                      </option>
                      <option value="static_header">
                        {t("access.candidates.auth.bearer")}
                      </option>
                    </select>
                    <small>
                      {t("access.candidates.auth.help.anthropic")}
                    </small>
                  </label>
                )}
                <div className="field">
                  <label htmlFor="access-service-key">
                    {t(
                      destinationPreset === "anthropic-compatible"
                        ? "access.candidates.secret.label"
                        : "access.serviceKey.label",
                    )}
                  </label>
                  <input
                    aria-describedby="access-service-key-help"
                    autoComplete="off"
                    disabled={busy}
                    id="access-service-key"
                    onChange={(event) => setSecret(event.target.value)}
                    required
                    type="password"
                    value={secret}
                  />
                  <small id="access-service-key-help">
                    {t("access.serviceKey.help")}
                  </small>
                </div>
              </div>
            )}
            {destinationPreset !== undefined && validAccessForm(form) && (
              <div className="access-review wide-action">
                <div className="access-create-step">
                  <span aria-hidden="true">3</span>
                  <div>
                    <strong>{t("access.review.title")}</strong>
                    <p>{t("access.review.description")}</p>
                  </div>
                </div>
                <div className="access-review-route">
                  <span className={`access-app-mark ${accessPreset}`}>
                    <AccessAppIcon preset={accessPreset} />
                  </span>
                  <strong>{form.name}</strong>
                  <span aria-hidden="true">→</span>
                  <span className="access-review-vibermate">{t("app.title")}</span>
                  <span aria-hidden="true">→</span>
                  <strong>
                    {t(`access.destination.${destinationPreset}.shortName`)}
                  </strong>
                </div>
                <p>
                  {t("access.review.model", { model: form.fixedModel })}
                </p>
              </div>
            )}
            <details
              className="access-advanced wide-action"
              onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
              open={advancedOpen}
            >
              <summary>
                <span>{t("access.advanced.title")}</span>
                <small>{t("access.advanced.description")}</small>
              </summary>
              <div className="access-advanced-grid">
                <LabeledInput
                  disabled={busy}
                  label={t("access.name.label")}
                  onChange={setField("name")}
                  required
                  value={form.name}
                />
                <LabeledInput
                  disabled={busy}
                  label={t("access.clientOrigin.label")}
                  onChange={(event) => {
                    setAccessPreset("custom");
                    setForm((current) => ({
                      ...current,
                      clientOrigin: event.target.value,
                    }));
                  }}
                  required
                  type="url"
                  value={form.clientOrigin}
                />
                <label className="field">
                  <span>{t("access.protocol.label")}</span>
                  <select
                    disabled={busy}
                    onChange={setClientDialect}
                    value={form.clientDialect}
                  >
                    <option value="anthropic-messages">
                      {t("access.dialect.anthropic-messages")}
                    </option>
                    <option value="openai-responses">
                      {t("access.dialect.openai-responses")}
                    </option>
                  </select>
                </label>
                <LabeledInput
                  disabled={busy}
                  label={t("access.providerOrigin.label")}
                  onChange={(event) => {
                    setDestinationPreset("custom");
                    setForm((current) => ({
                      ...current,
                      providerOrigin: event.target.value,
                    }));
                  }}
                  required
                  type="url"
                  value={form.providerOrigin}
                />
                <label className="field">
                  <span>{t("access.description.label")}</span>
                  <textarea
                    disabled={busy}
                    onChange={setField("description")}
                    value={form.description}
                  />
                </label>
              </div>
            </details>
            {conflictingAccess !== undefined && (
              <div className="boundary-note wide-action" role="alert">
                <strong>{t("access.create.originConflict.title")}</strong>
                <span>
                  {t("access.create.originConflict.detail", {
                    name: conflictingAccess.name,
                  })}
                </span>
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => void selectAccess(conflictingAccess)}
                  type="button"
                >
                  {t("access.create.originConflict.action", {
                    name: conflictingAccess.name,
                  })}
                </button>
              </div>
            )}
          </form>
        </section>
      ) : selectedAccess === undefined ? (
        <section className="panel access-selection-empty">
          <h2>{t("access.selection.title")}</h2>
          <p>{t("access.selection.description")}</p>
        </section>
      ) : (
        <section className="panel access-panel">
          <div className="section-heading">
            <div>
              <h2>{displayAccessName(selectedAccess)}</h2>
              <p>
                {t(
                  `access.preset.${accessPresetForItem(selectedAccess)}.directoryDescription`,
                )}
              </p>
            </div>
            <span className={`access-status ${selectedStatus}`}>
              {t(`access.status.${selectedStatus}`)}
            </span>
          </div>
          {activeRevision !== undefined && lastApplicationState !== undefined && (
            <span
              className={`success-note ${
                lastApplicationState === "unavailable" ? "unavailable" : ""
              }`}
              role="status"
            >
              {t(
                lastApplicationState === "unavailable"
                  ? "access.apply.committedUnavailable"
                  : "access.apply.succeeded",
                { revision: activeRevision },
              )}
            </span>
          )}
          {credentialSaveFailed && (
            <div className="boundary-note" role="alert">
              <strong>{t("credential.saveFailed.title")}</strong>
              <span>{t("credential.saveFailed.detail")}</span>
            </div>
          )}
          {selectedAccess.description.length > 0 && (
            <div className="access-description">
              <h3>{t("access.description.label")}</h3>
              <p>{selectedAccess.description}</p>
            </div>
          )}
          <details className="access-technical-details">
            <summary>
              <span>{t("access.technical.title")}</span>
              <small>{t("access.technical.description")}</small>
            </summary>
            <dl className="access-summary">
              <div>
                <dt>{t("access.clientOrigin.label")}</dt>
                <dd className="identifier">{selectedAccess.clientOrigin}</dd>
              </div>
              <div>
                <dt>{t("access.protocol.label")}</dt>
                <dd>{t(`access.dialect.${selectedAccess.clientDialect}`)}</dd>
              </div>
              <div>
                <dt>{t("access.upstreams.label")}</dt>
                <dd>
                  {selectionState === "ready"
                    ? t("access.upstreams.count", {
                        count: loadedProfileCount,
                      })
                    : t(
                        `common.data.${selectionState === "idle" ? "loading" : selectionState}`,
                      )}
                </dd>
              </div>
            </dl>
          </details>
          {selectionState === "unavailable" && (
            <div className="boundary-note" role="alert">
              <strong>{t("access.detail.unavailable.title")}</strong>
              <span>{t("access.detail.unavailable.detail")}</span>
            </div>
          )}
          {selectionState === "ready" &&
            selectedStatus === "enabled" &&
            activePlanAvailable === false &&
            lastApplicationState === undefined && (
              <div className="boundary-note" role="alert">
                <strong>{t("access.detail.applicationUnavailable.title")}</strong>
                <span>{t("access.detail.applicationUnavailable.detail")}</span>
              </div>
            )}
          {selectionState === "ready" && selectedStatus !== "enabled" && (
            <div className="boundary-note">
              <strong>{t(`access.detail.inactive.${selectedStatus}.title`)}</strong>
              <span>{t(`access.detail.inactive.${selectedStatus}.detail`)}</span>
            </div>
          )}
          {selectedDetail !== undefined && (
            <section className="access-upstream-section">
              <div className="access-route-heading">
                <div>
                  <h3>{t("access.candidates.title")}</h3>
                  <p>{t("access.candidates.description")}</p>
                </div>
                {selectedDetail.access.status === "enabled" && (
                  <button
                    className="secondary"
                    disabled={busy || candidateEditorOpen}
                    onClick={beginCandidateAdd}
                    type="button"
                  >
                    {t("access.candidates.add.action")}
                  </button>
                )}
              </div>
              {candidateSaveFailed !== undefined && (
                <div className="boundary-note" role="alert">
                  <strong>
                    {t(`access.candidates.error.${candidateSaveFailed}.title`)}
                  </strong>
                  <span>
                    {t(`access.candidates.error.${candidateSaveFailed}.detail`)}
                  </span>
                </div>
              )}
              <ul className="access-upstream-list">
                {selectedDetail.profiles.map((profile) => {
                  const target = selectedDetail.providerTargets.find(
                    ({ id }) => id === profile.targetId,
                  );
                  const accounts = selectedDetail.accountBindings.filter(
                    ({ profileId }) => profileId === profile.id,
                  );
                  const model =
                    profile.defaultModelPolicy.mode === "fixed"
                      ? profile.defaultModelPolicy.fixedModel
                      : t(
                          `access.modelPolicy.${profile.defaultModelPolicy.mode}`,
                        );
                  const active = profile.id === selectedActiveProfileId;
                  const ready = selectedCandidateProfileIds.has(profile.id);
                  const provider =
                    target === undefined
                      ? "openai-compatible"
                      : candidateProviderForTarget(target);
                  const official =
                    provider === "anthropic" || provider === "openai";
                  const anthropicProvider = provider.startsWith("anthropic");
                  return (
                    <li
                      className={`${active ? "active" : ""} ${
                        ready ? "" : "incomplete"
                      }`}
                      key={profile.id}
                    >
                      <div className="access-route-title">
                        <span
                          className={`access-service-mark ${
                            anthropicProvider ? "anthropic" : "openai"
                          }`}
                          aria-hidden="true"
                        >
                          <AccessProviderIcon provider={provider} />
                        </span>
                        <div>
                          <strong>{profile.name}</strong>
                          <span>
                            {t(`access.candidates.provider.${provider}.name`)}
                          </span>
                        </div>
                        <span
                          className={`access-route-state ${
                            active ? "active" : ready ? "ready" : "incomplete"
                          }`}
                        >
                          {t(
                            active
                              ? "access.candidates.state.active"
                              : ready
                                ? "access.candidates.state.ready"
                                : "access.candidates.state.incomplete",
                          )}
                        </span>
                      </div>
                      <div className="access-route-facts">
                        <span>{t("access.upstreams.model", { model })}</span>
                        <span>
                          {t("access.candidates.presentation.summary", {
                            presentation: t(
                              profile.upstreamWireProfileRef === "claude-code"
                                ? "access.candidates.presentation.claudeCode"
                                : "access.candidates.presentation.followClient",
                            ),
                          })}
                        </span>
                        <span>
                          {t("access.candidates.account", {
                            name:
                              accounts.map(({ label }) => label).join(", ") ||
                              profile.name,
                          })}
                        </span>
                        {target !== undefined && !official && (
                          <span className="identifier">{target.origin}</span>
                        )}
                      </div>
                      {!active && (
                        <div className="access-route-action">
                          <button
                            className="secondary"
                            disabled={
                              busy ||
                              candidateEditorOpen ||
                              selectedDetail.access.status !== "enabled"
                            }
                            onClick={() =>
                              ready
                                ? void selectCandidate(profile.id)
                                : beginCandidateSetup(profile.id)
                            }
                            type="button"
                          >
                            {t(
                              ready
                                ? "access.candidates.select.action"
                                : "access.candidates.finish.action",
                            )}
                          </button>
                        </div>
                      )}
                    </li>
                  );
                })}
              </ul>
              {candidateEditorOpen && (
                <form
                  className="access-candidate-form"
                  onSubmit={(event) => void submitCandidate(event)}
                >
                  <div className="access-candidate-form-heading">
                    <div>
                      <h4>{t("access.candidates.form.title")}</h4>
                      <p>{t("access.candidates.form.description")}</p>
                    </div>
                    <button
                      aria-label={t("common.cancel.action")}
                      className="secondary compact-action"
                      disabled={busy}
                      onClick={closeCandidateEditor}
                      type="button"
                    >
                      {t("common.cancel.action")}
                    </button>
                  </div>
                  <fieldset className="access-candidate-provider-picker">
                    <legend>{t("access.candidates.provider.title")}</legend>
                    <div className="access-candidate-provider-options">
                      {candidateProviderPresets.map((provider) => (
                        <button
                          aria-pressed={candidateForm.provider === provider}
                          className="access-candidate-provider"
                          disabled={busy || pendingCandidate !== undefined}
                          key={provider}
                          onClick={() =>
                            setCandidateForm((current) => ({
                              ...current,
                              authDriverRef: provider.startsWith("openai")
                                ? "static_header"
                                : "anthropic_api_key",
                              name: candidateAutomaticName
                                ? defaultCandidateName(provider)
                                : current.name,
                              model: candidateAutomaticModel
                                ? defaultCandidateModel(provider)
                                : current.model,
                              provider,
                            }))
                          }
                          type="button"
                        >
                          <span
                            className={`access-service-mark ${
                              provider.startsWith("anthropic")
                                ? "anthropic"
                                : "openai"
                            }`}
                            aria-hidden="true"
                          >
                            <AccessProviderIcon provider={provider} />
                          </span>
                          <span>
                            <strong>
                              {t(`access.candidates.provider.${provider}.name`)}
                            </strong>
                            <small>
                              {t(
                                `access.candidates.provider.${provider}.description`,
                              )}
                            </small>
                          </span>
                        </button>
                      ))}
                    </div>
                  </fieldset>
                  <div className="access-candidate-fields">
                    <LabeledInput
                      disabled={busy || pendingCandidate !== undefined}
                      label={t("access.candidates.name.label")}
                      onChange={(event) =>
                        {
                          setCandidateAutomaticName(false);
                          setCandidateForm((current) => ({
                            ...current,
                            name: event.target.value,
                          }));
                        }
                      }
                      required
                      value={candidateForm.name}
                    />
                    {(candidateForm.provider === "anthropic-compatible" ||
                      candidateForm.provider === "openai-compatible") && (
                      <>
                        <LabeledInput
                          disabled={busy || pendingCandidate !== undefined}
                          label={t("access.candidates.baseUrl.label")}
                          onChange={(event) =>
                            setCandidateForm((current) => ({
                              ...current,
                              baseUrl: event.target.value,
                            }))
                          }
                          placeholder={t("access.candidates.baseUrl.placeholder")}
                          required
                          type="url"
                          value={candidateForm.baseUrl}
                        />
                        <p className="access-candidate-endpoint">
                          {compatibleEndpointPreview(
                            candidateForm.baseUrl,
                            candidateForm.provider,
                          ) === undefined
                            ? t(
                                `access.candidates.endpoint.${candidateForm.provider}.help`,
                              )
                            : t("access.candidates.endpoint.preview", {
                                endpoint: compatibleEndpointPreview(
                                  candidateForm.baseUrl,
                                  candidateForm.provider,
                                ),
                              })}
                        </p>
                        <label className="field">
                          <span>{t("access.candidates.auth.label")}</span>
                          <select
                            disabled={busy || pendingCandidate !== undefined}
                            onChange={(event) =>
                              setCandidateForm((current) => ({
                                ...current,
                                authDriverRef:
                                  event.target.value === "static_header"
                                    ? "static_header"
                                    : "anthropic_api_key",
                              }))
                            }
                            value={candidateForm.authDriverRef}
                          >
                            <option value="anthropic_api_key">
                              {t("access.candidates.auth.xApiKey")}
                            </option>
                            <option value="static_header">
                              {t("access.candidates.auth.bearer")}
                            </option>
                          </select>
                          <small>
                            {t(
                              candidateForm.provider ===
                                "anthropic-compatible"
                                ? "access.candidates.auth.help.anthropic"
                                : "access.candidates.auth.help.openai",
                            )}
                          </small>
                        </label>
                      </>
                    )}
                    <LabeledInput
                      disabled={busy || pendingCandidate !== undefined}
                      label={t("access.candidates.model.label")}
                      onChange={(event) =>
                        {
                          setCandidateAutomaticModel(false);
                          setCandidateForm((current) => ({
                            ...current,
                            model: event.target.value,
                          }));
                        }
                      }
                      placeholder={t(
                        candidateForm.provider.startsWith("anthropic")
                          ? "access.candidates.model.placeholder"
                          : "access.fixedModel.placeholder",
                      )}
                      required
                      value={candidateForm.model}
                    />
                    <label className="field">
                      <span>{t("access.candidates.presentation.label")}</span>
                      <select
                        disabled={busy || pendingCandidate !== undefined}
                        onChange={(event) =>
                          setCandidateForm((current) => ({
                            ...current,
                            upstreamPresentation:
                              event.target.value === "claude-code"
                                ? "claude-code"
                                : "follow-client",
                          }))
                        }
                        value={candidateForm.upstreamPresentation}
                      >
                        <option value="follow-client">
                          {t("access.candidates.presentation.followClient")}
                        </option>
                        <option value="claude-code">
                          {t("access.candidates.presentation.claudeCode")}
                        </option>
                      </select>
                      <small>{t("access.candidates.presentation.help")}</small>
                    </label>
                    {pendingCandidate?.phase !== "activate" && (
                      <LabeledInput
                        autoComplete="off"
                        disabled={busy}
                        label={t("access.candidates.secret.label")}
                        onChange={(event) =>
                          setCandidateSecret(event.target.value)
                        }
                        required
                        type="password"
                        value={candidateSecret}
                      />
                    )}
                  </div>
                  <p className="access-candidate-privacy">
                    {t("access.candidates.secret.help")}
                  </p>
                  <div className="form-action access-candidate-actions">
                    <button
                      disabled={
                        busy ||
                        (pendingCandidate?.phase !== "activate" &&
                          candidateSecret.length === 0) ||
                        (pendingCandidate === undefined &&
                          !validAccessCandidateForm(candidateForm))
                      }
                      type="submit"
                    >
                      {t(
                        pendingCandidate?.phase === "activate"
                          ? "access.candidates.activate.retry"
                          : pendingCandidate === undefined
                            ? "access.candidates.add.submit"
                            : "access.candidates.secret.retry",
                      )}
                    </button>
                  </div>
                </form>
              )}
            </section>
          )}
          {selectedStatus === "enabled" && (
            <form
              className="credential-form"
              onSubmit={(event) => void submitCredential(event)}
            >
            <div className="credential-copy">
              <h3>{t("credential.title")}</h3>
              <p>{t("credential.description")}</p>
              {selectedActiveProfile !== undefined && (
                <p className="credential-active-route">
                  {t("credential.activeRoute", {
                    name: selectedActiveProfile.name,
                  })}
                </p>
              )}
              <span
                className={`credential-state ${
                  activeCredential?.secretState ??
                  (loadedCredential === undefined ? "missing" : "unavailable")
                }`}
              >
                {activeCredential === undefined && loadedCredential !== undefined
                  ? t(
                      credentialUnavailable
                        ? "common.data.unavailable"
                        : "common.data.loading",
                    )
                  : t(
                      `credential.state.${activeCredential?.secretState ?? "missing"}`,
                    )}
                {activeCredential !== undefined && credentialUnavailable && (
                  <span className="credential-revision">
                    {t("common.data.stale")}
                  </span>
                )}
              </span>
            </div>
            <LabeledInput
              autoComplete="off"
              disabled={loadedCredential === undefined}
              label={t("credential.secret.label")}
              onChange={(event) => setSecret(event.target.value)}
              required
              spellCheck={false}
              type="password"
              value={secret}
            />
            <div className="form-action">
              <button
                disabled={
                  busy || loadedCredential === undefined || secret.length === 0
                }
                type="submit"
              >
                {t("credential.replace.action")}
              </button>
            </div>
            </form>
          )}
        </section>
      )}
    </div>
  );
}

function LabeledInput({
  label,
  ...properties
}: {
  readonly label: string;
} & InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="field">
      <span>{label}</span>
      <input {...properties} />
    </label>
  );
}

function ApprovalPanel({
  actions,
  approvals,
  availability,
  busy,
  selectedApprovalId,
}: {
  readonly actions: DashboardActions;
  readonly approvals: readonly ApprovalView[];
  readonly availability: SourceAvailability;
  readonly busy: boolean;
  readonly selectedApprovalId: string | undefined;
}) {
  const { t, i18n } = useTranslation();
  const selectedApproval = useRef<HTMLLIElement>(null);
  const focusedSelection = useRef<string | undefined>(undefined);
  const formatter = useMemo(
    () =>
      new Intl.DateTimeFormat(i18n.language, {
        dateStyle: "medium",
        timeStyle: "short",
      }),
    [i18n.language],
  );
  useEffect(() => {
    if (selectedApproval.current === null) {
      focusedSelection.current = undefined;
      return;
    }
    if (focusedSelection.current !== selectedApprovalId) {
      selectedApproval.current.focus();
      focusedSelection.current = selectedApprovalId;
    }
  }, [approvals, selectedApprovalId]);
  return (
    <section className="panel list-panel">
      <h2>{t("approvals.title")}</h2>
      {approvals.length === 0 ? (
        <p className="empty-state">
          {t(
            availability === "ready"
              ? "approval.empty"
              : `common.data.${availability}`,
          )}
        </p>
      ) : (
        <ol className="record-list">
          {approvals.map((approval) => {
            const selected = approval.id === selectedApprovalId;
            return (
              <li
                className={selected ? "selected-record" : undefined}
                data-approval-id={approval.id}
                data-selected={selected ? "true" : undefined}
                key={approval.id}
                ref={selected ? selectedApproval : undefined}
                tabIndex={selected ? -1 : undefined}
              >
                <h3>{t(approval.titleKey)}</h3>
                <p>{t(approval.summaryKey)}</p>
                <dl className="inline-details">
                  <div>
                    <dt>{t(subjectLabelKey(approval))}</dt>
                    <dd>{approvalSubject(approval)}</dd>
                  </div>
                  {approval.requestCount > 1 ? (
                    <div>
                      <dt>{t("approval.waiting.label")}</dt>
                      <dd>
                        {t("approval.waiting.value", {
                          count: approval.requestCount,
                        })}
                      </dd>
                    </div>
                  ) : null}
                  <div>
                    <dt>{t("approval.expiresAt.label")}</dt>
                    <dd>{formatter.format(new Date(approval.expiresAt))}</dd>
                  </div>
                </dl>
                <div className="button-row">
                  <Link
                    className="route-action"
                    search={{ selected: approval.id }}
                    to={dashboardRoutePaths.policy}
                  >
                    {t("approval.open.action")}
                  </Link>
                  {/*
                  The window offers exactly what the runtime declared. A
                  hard-coded button can offer an answer the runtime refuses,
                  or hide one it allows.
                */}
                  {approval.choices.map((choice) => (
                    <button
                      className={
                        choice.decision === "deny" ? "danger" : undefined
                      }
                      disabled={busy || availability !== "ready"}
                      key={`${choice.decision}:${choice.scope}`}
                      onClick={() =>
                        void actions.decideApproval(approval, choice)
                      }
                      type="button"
                    >
                      {t(choice.labelKey)}
                    </button>
                  ))}
                </div>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}

/**
 * A question is named in the terms it is about: a host and port for a
 * connection, tool names for a tool call.
 */
function approvalSubject(approval: ApprovalView): string {
  if (approval.target !== undefined) {
    return `${approval.target.host}:${approval.target.port}`;
  }
  return approval.subjectLabels.join(", ");
}

function subjectLabelKey(approval: ApprovalView): string {
  if (approval.target !== undefined) {
    return "approval.target.label";
  }
  return approval.kind === "client_root_ask"
    ? "approval.clientRootAsk.signedPath.label"
    : "approval.tools.label";
}

/**
 * What is captured, and whether anything has actually come through it.
 *
 * "Is my client going through vibermate" had no answer in the window before
 * this. A run that was launched but has seen no traffic is a different state
 * from one that never started, and a client this build has no release
 * evidence for will never connect at all — it says so here rather than
 * failing later with a transport error nobody can explain.
 */
function CapturePanel({
  availability,
  runs,
}: {
  readonly availability: SourceAvailability;
  readonly runs: readonly CaptureRunRecord[];
}) {
  const { t } = useTranslation();
  return (
    <section className="panel list-panel">
      <h2>{t("capture.title")}</h2>
      {runs.length === 0 ? (
        <p className="empty-state">
          {t(
            availability === "ready"
              ? "capture.empty"
              : `common.data.${availability}`,
          )}
        </p>
      ) : (
        <ol className="record-list">
          {runs.map((run) => (
            <li key={run.id}>
              <h3>{run.executableLabel}</h3>
              <dl className="inline-details">
                <div>
                  <dt>{t("capture.state.label")}</dt>
                  <dd>{t(`capture.state.${run.state}`)}</dd>
                </div>
                <div>
                  <dt>{t("capture.observation.label")}</dt>
                  <dd>{t(`capture.observation.${run.observation}`)}</dd>
                </div>
                <div>
                  <dt>{t("capture.adapterState.label")}</dt>
                  <dd
                    className={
                      run.clientAdapterState === "failed"
                        ? "attention"
                        : undefined
                    }
                  >
                    {t(`capture.adapterState.${run.clientAdapterState}`)}
                  </dd>
                </div>
                <div>
                  <dt>{t("capture.recognition.label")}</dt>
                  <dd
                    className={
                      run.clientRecognition === "verified"
                        ? undefined
                        : "attention"
                    }
                  >
                    {t(`capture.recognition.${run.clientRecognition}`)}
                  </dd>
                </div>
                {run.clientAdapterReason !== undefined && (
                  <div>
                    <dt>{t("capture.adapterReason.label")}</dt>
                    <dd className="identifier">{run.clientAdapterReason}</dd>
                  </div>
                )}
              </dl>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

/**
 * What connected where. Design 06 4.1 is what makes this screen possible
 * without decrypting anything: the record says who connected, where, whether
 * it was refused, whether it was read or forwarded blind, and how much
 * crossed. It never says what was sent, and neither does this panel.
 */
function ConnectionPanel({
  availability,
  connections,
}: {
  readonly availability: SourceAvailability;
  readonly connections: readonly ConnectionRecord[];
}) {
  const { t } = useTranslation();
  return (
    <section className="panel list-panel">
      <h2>{t("connections.title")}</h2>
      {connections.length === 0 ? (
        <p className="empty-state">
          {t(
            availability === "ready"
              ? "connections.empty"
              : `common.data.${availability}`,
          )}
        </p>
      ) : (
        <ol className="record-list">
          {connections.map((connection) => (
            <li key={connection.connectionId}>
              <h3>
                {connection.requestedHost}:{connection.port}
              </h3>
              <dl className="inline-details">
                <div>
                  <dt>{t("connections.source.label")}</dt>
                  <dd>
                    {connection.sourceLabel ?? t("connections.source.unknown")}
                    {" · "}
                    {t(`connections.confidence.${connection.sourceConfidence}`)}
                  </dd>
                </div>
                <div>
                  <dt>{t("connections.decision.label")}</dt>
                  <dd>
                    {connection.decision === undefined
                      ? t("connections.decision.undecided")
                      : t(`connections.decision.${connection.decision}`)}
                    {connection.ruleId === undefined
                      ? ""
                      : ` · ${connection.ruleId}`}
                  </dd>
                </div>
                <div>
                  <dt>{t("connections.decryption.label")}</dt>
                  <dd>
                    {t(`connections.decryption.${connection.decryption}`)}
                  </dd>
                </div>
                <div>
                  <dt>{t("connections.bytes.label")}</dt>
                  <dd>
                    {t("connections.bytes.value", {
                      up: connection.bytesUp,
                      down: connection.bytesDown,
                    })}
                  </dd>
                </div>
              </dl>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

/**
 * Where each request actually went. A connection can carry several requests,
 * so an attempt is a separate fact: the last one must not overwrite the
 * first one's destination.
 */
function EgressPanel({
  attempts,
  availability,
}: {
  readonly attempts: readonly EgressAttemptRecord[];
  readonly availability: SourceAvailability;
}) {
  const { t } = useTranslation();
  return (
    <section className="panel list-panel">
      <h2>{t("egress.title")}</h2>
      {attempts.length === 0 ? (
        <p className="empty-state">
          {t(
            availability === "ready"
              ? "egress.empty"
              : `common.data.${availability}`,
          )}
        </p>
      ) : (
        <ol className="record-list">
          {attempts.map((attempt) => (
            <li key={attempt.id}>
              <h3>{attempt.targetOrigin}</h3>
              <dl className="inline-details">
                <div>
                  <dt>{t("egress.purpose.label")}</dt>
                  <dd>{t(`egress.purpose.${attempt.purpose}`)}</dd>
                </div>
                <div>
                  <dt>{t("egress.outcome.label")}</dt>
                  <dd>
                    {attempt.terminal && attempt.outcome !== undefined
                      ? t(`egress.outcome.${attempt.outcome}`)
                      : t("egress.outcome.inFlight")}
                    {attempt.errorClass === undefined
                      ? ""
                      : ` · ${attempt.errorClass}`}
                  </dd>
                </div>
                <div>
                  <dt>{t("egress.bytes.label")}</dt>
                  <dd>
                    {t("egress.bytes.value", {
                      out: attempt.bytesOut,
                      in: attempt.bytesIn,
                    })}
                  </dd>
                </div>
              </dl>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function ActivityPanel({
  activities,
  availability,
  emptyAccessAction = false,
  hasMore = false,
  loadMoreErrorKey,
  loadingMore = false,
  onLoadMore,
  pagingSafetyStopped = false,
  title,
}: {
  readonly activities: readonly ActivityRecord[];
  readonly availability: SourceAvailability;
  readonly emptyAccessAction?: boolean;
  readonly hasMore?: boolean;
  readonly loadMoreErrorKey?: string | undefined;
  readonly loadingMore?: boolean;
  readonly onLoadMore?: () => Promise<void>;
  readonly pagingSafetyStopped?: boolean;
  readonly title?: string;
}) {
  const { t, i18n } = useTranslation();
  const formatter = useMemo(
    () =>
      new Intl.DateTimeFormat(i18n.language, {
        dateStyle: "medium",
        timeStyle: "short",
      }),
    [i18n.language],
  );
  return (
    <section className="panel list-panel">
      <h2>{title ?? t("activity.title")}</h2>
      {activities.length === 0 ? (
        <div className="empty-state activity-empty">
          <p>
            {t(
              availability === "ready"
                ? "activity.empty"
                : `common.data.${availability}`,
            )}
          </p>
          {availability === "ready" && emptyAccessAction ? (
            <Link className="route-action" search={{}} to={dashboardRoutePaths.access}>
              {t("activity.empty.action")}
            </Link>
          ) : null}
        </div>
      ) : (
        <ol className="record-list activity-list">
          {activities.map((record) => {
            const knownStatus =
              record.status === "succeeded" ||
              record.status === "pending" ||
              record.status === "failed" ||
              record.status === "canceled";
            return (
              <li key={record.id}>
                <div>
                  <Link
                    className="activity-request-link"
                    params={{ exchangeId: record.id }}
                    search={{}}
                    to={dashboardTaskRoutePaths.activityRequest}
                  >
                    <h3>{t("activity.request.summary", { id: record.id })}</h3>
                  </Link>
                  <dl className="inline-details activity-summary">
                    <div>
                      <dt>{t("activity.access.label")}</dt>
                      <dd>{record.accessId}</dd>
                    </div>
                    <div>
                      <dt>{t("activity.occurredAt.label")}</dt>
                      <dd>{formatter.format(new Date(record.occurredAt))}</dd>
                    </div>
                  </dl>
                </div>
                <span
                  className={`activity-status ${knownStatus ? record.status : "neutral"}`}
                >
                  {knownStatus
                    ? t(`activity.status.${record.status}`)
                    : record.status}
                </span>
              </li>
            );
          })}
        </ol>
      )}
      {activities.length > 0 &&
      (hasMore || loadingMore || loadMoreErrorKey !== undefined) ? (
        <div className="activity-pagination">
          {loadMoreErrorKey === undefined ? null : (
            <p role="alert">{t("activity.loadMore.error")}</p>
          )}
          {hasMore || loadingMore ? (
            <button
              className="secondary-action"
              disabled={loadingMore || onLoadMore === undefined}
              onClick={() => void onLoadMore?.()}
              type="button"
            >
              {t(
                loadingMore
                  ? "activity.loadMore.loading"
                  : "activity.loadMore.action",
              )}
            </button>
          ) : null}
        </div>
      ) : null}
      {activities.length > 0 && pagingSafetyStopped ? (
        <div className="boundary-note activity-paging-notice" role="status">
          <strong>{t("activity.pagingSafetyStopped.title")}</strong>
          <span>{t("activity.pagingSafetyStopped.detail")}</span>
        </div>
      ) : null}
    </section>
  );
}

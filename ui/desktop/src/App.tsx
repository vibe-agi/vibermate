import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import {
  createContext,
  type ReactNode,
  useContext,
  useMemo,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
  controlErrorKey,
  dashboardQueryKeys,
  type DashboardQueryRuntime,
} from "./dashboard-runtime.ts";
import {
  dashboardRoutePaths,
  type DashboardView,
  viewFromPathname,
} from "./navigation.ts";

const DashboardModelContext = createContext<DashboardQueryRuntime | undefined>(
  undefined,
);

export function useDashboardModel(): DashboardQueryRuntime {
  const value = useContext(DashboardModelContext);
  if (value === undefined) {
    throw new Error("Dashboard model is unavailable");
  }
  return value;
}

type NavigationIcon =
  | "captures"
  | "environments"
  | "accounts"
  | "extensions"
  | "policy"
  | "quality"
  | "settings";

const navigationGroups: readonly {
  readonly labelKey: string;
  readonly items: readonly {
    readonly icon: NavigationIcon;
    readonly labelKey: string;
    readonly view: DashboardView;
  }[];
}[] = [
  {
    labelKey: "workspace.nav.group.run",
    items: [
      { icon: "captures", labelKey: "workspace.nav.captures", view: "captures" },
    ],
  },
  {
    labelKey: "workspace.nav.group.resources",
    items: [
      {
        icon: "environments",
        labelKey: "workspace.nav.environments",
        view: "environments",
      },
      { icon: "accounts", labelKey: "workspace.nav.accounts", view: "accounts" },
      {
        icon: "extensions",
        labelKey: "workspace.nav.extensions",
        view: "extensions",
      },
    ],
  },
  {
    labelKey: "workspace.nav.group.governance",
    items: [
      { icon: "policy", labelKey: "workspace.nav.policy", view: "policy" },
      { icon: "quality", labelKey: "workspace.nav.quality", view: "quality" },
    ],
  },
];

export function DashboardShell({
  model,
  preview,
}: {
  readonly model: DashboardQueryRuntime;
  readonly preview: boolean;
}) {
  const { i18n, t } = useTranslation();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const view = viewFromPathname(pathname);
  const queryClient = useQueryClient();
  const [commandError, setCommandError] = useState<string>();

  const status = useQuery({
    queryKey: dashboardQueryKeys.status,
    queryFn: ({ signal }) => model.client.status(signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  const offline = useQuery({
    queryKey: dashboardQueryKeys.offline,
    queryFn: ({ signal }) => model.client.offlineHold(signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  const approvals = useQuery({
    queryKey: dashboardQueryKeys.approvals,
    queryFn: ({ signal }) => model.client.approvals(signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });

  const hold = useMutation({
    mutationFn: async () => {
      const snapshot = offline.data;
      if (snapshot === undefined) {
        throw new Error("Offline Hold state is unavailable");
      }
      return snapshot.state === "held"
        ? model.client.resumeOfflineHold(snapshot.revision)
        : model.client.enterOfflineHold(snapshot.revision);
    },
    onError: (error) => setCommandError(controlErrorKey(error)),
    onSuccess: (snapshot) => {
      setCommandError(undefined);
      queryClient.setQueryData(dashboardQueryKeys.offline, snapshot);
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.status });
    },
  });

  const ready = status.data?.ready === true;
  const unavailable =
    status.isError || offline.isError || approvals.isError || commandError !== undefined;
  const pendingCount = approvals.data?.items.length ?? 0;
  const holdState = offline.data?.state;

  return (
    <DashboardModelContext.Provider value={model}>
      <div className="app-frame">
        <aside className="sidebar" aria-label={t("workspace.nav.label")}>
          <div className="product-lockup">
            <span className="product-mark" aria-hidden="true">{t("app.mark")}</span>
            <strong>{t("app.name")}</strong>
            {preview && <span className="preview-badge">{t("workspace.preview")}</span>}
          </div>
          <nav className="primary-nav">
            {navigationGroups.map((group) => (
              <div className="nav-group" key={group.labelKey}>
                <p>{t(group.labelKey)}</p>
                {group.items.map((item) => (
                  <Link
                    aria-current={view === item.view ? "page" : undefined}
                    className="nav-link"
                    key={item.view}
                    search={{}}
                    to={dashboardRoutePaths[item.view]}
                  >
                    <AppIcon name={item.icon} />
                    <span>{t(item.labelKey)}</span>
                  </Link>
                ))}
              </div>
            ))}
          </nav>
          <Link
            aria-current={view === "settings" ? "page" : undefined}
            className="nav-link settings-link"
            search={{}}
            to={dashboardRoutePaths.settings}
          >
            <AppIcon name="settings" />
            <span>{t("workspace.nav.settings")}</span>
          </Link>
        </aside>

        <div className="workspace-shell">
          <header className="command-bar">
            <div className="runtime-summary" aria-live="polite">
              <span className={`status-dot ${ready ? "ok" : "attention"}`} />
              <strong>{ready ? t("workspace.runtime.ready") : t("workspace.runtime.starting")}</strong>
              <span className="runtime-detail">
                {unavailable
                  ? t("workspace.runtime.partial")
                  : t("workspace.runtime.current")}
              </span>
            </div>
            <div className="command-actions">
              <button
                className={holdState === "held" ? "hold-active" : "quiet-button"}
                disabled={hold.isPending || offline.data === undefined}
                onClick={() => hold.mutate()}
                type="button"
              >
                {holdState === "held"
                  ? t("workspace.hold.resume")
                  : t("workspace.hold.enter")}
              </button>
              <Link className="pending-link" search={{}} to={dashboardRoutePaths.policy}>
                <span>{t("workspace.pending")}</span>
                <strong>{pendingCount}</strong>
              </Link>
              <div className="locale-switch" aria-label={t("workspace.locale.label")}>
                <button
                  aria-pressed={i18n.language === "en-US"}
                  onClick={() => void i18n.changeLanguage("en-US")}
                  type="button"
                >
                  {t("locale.en-US.short")}
                </button>
                <button
                  aria-pressed={i18n.language === "zh-CN"}
                  onClick={() => void i18n.changeLanguage("zh-CN")}
                  type="button"
                >
                  {t("locale.zh-CN")}
                </button>
              </div>
              <button
                aria-label={t("workspace.refresh")}
                className="icon-button"
                onClick={() => void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.root })}
                type="button"
              >
                <RefreshIcon />
              </button>
            </div>
          </header>
          <main id="main-content" tabIndex={-1}>
            <Outlet />
          </main>
        </div>
      </div>
    </DashboardModelContext.Provider>
  );
}

export function PageHeading({
  actions,
  description,
  eyebrow,
  title,
}: {
  readonly actions?: ReactNode;
  readonly description?: string;
  readonly eyebrow?: string;
  readonly title: string;
}) {
  return (
    <header className="page-heading">
      <div>
        {eyebrow !== undefined && <p className="eyebrow">{eyebrow}</p>}
        <h1 tabIndex={-1}>{title}</h1>
        {description !== undefined && <p>{description}</p>}
      </div>
      {actions !== undefined && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function EmptyState({
  action,
  description,
  title,
}: {
  readonly action?: ReactNode;
  readonly description: string;
  readonly title: string;
}) {
  return (
    <div className="empty-state">
      <strong>{title}</strong>
      <p>{description}</p>
      {action}
    </div>
  );
}

export function LoadingRows({ count = 5 }: { readonly count?: number }) {
  return (
    <div aria-label="" className="loading-rows">
      {Array.from({ length: count }, (_, index) => (
        <span key={index} />
      ))}
    </div>
  );
}

export function InlineProblem({ message }: { readonly message: string }) {
  return <p className="inline-problem" role="alert">{message}</p>;
}

export function SectionHeading({
  action,
  title,
}: {
  readonly action?: ReactNode;
  readonly title: string;
}) {
  return (
    <div className="section-heading">
      <h2>{title}</h2>
      {action}
    </div>
  );
}

function AppIcon({ name }: { readonly name: NavigationIcon }) {
  const paths = useMemo<Record<NavigationIcon, ReactNode>>(
    () => ({
      captures: <><path d="M4 6h16M6 10h12M8 14h8M10 18h4" /></>,
      environments: <><rect x="4" y="4" width="16" height="16" rx="3" /><path d="M8 9h8M8 13h5" /></>,
      accounts: <><circle cx="12" cy="8" r="3" /><path d="M5.5 19c.8-4 3-6 6.5-6s5.7 2 6.5 6" /></>,
      extensions: <path d="M9 3v4H5v4H3v4h4v4h4v2h4v-4h4v-4h2V9h-4V5h-4v4H9V3Z" />,
      policy: <><path d="M12 3 5 6v5c0 4.4 2.8 8.1 7 10 4.2-1.9 7-5.6 7-10V6l-7-3Z" /><path d="m9 12 2 2 4-5" /></>,
      quality: <><path d="M4 18 9 12l4 3 7-9" /><path d="M4 21h16" /></>,
      settings: <><circle cx="12" cy="12" r="3" /><path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1" /></>,
    }),
    [],
  );
  return (
    <svg aria-hidden="true" className="app-icon" fill="none" viewBox="0 0 24 24">
      {paths[name]}
    </svg>
  );
}

function RefreshIcon() {
  return (
    <svg aria-hidden="true" fill="none" viewBox="0 0 24 24">
      <path d="M20 6v5h-5M4 18v-5h5M6.1 8.5A7 7 0 0 1 18.5 7M5.5 17A7 7 0 0 0 18 15.5" />
    </svg>
  );
}

import { parseHref } from "@tanstack/history";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  Navigate,
  RouterProvider,
  createBrowserHistory,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  type RouterHistory,
  useRouterState,
} from "@tanstack/react-router";
import { useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { DashboardShell, InlineProblem } from "./App.tsx";
import {
  CaptureDetailRoutePage,
  CapturesRoutePage,
  RequestsRoutePage,
} from "./capture-pages.tsx";
import {
  EnvironmentDetailRoutePage,
  EnvironmentsRoutePage,
} from "./environment-pages.tsx";
import {
  AccountsRoutePage,
  ExchangeRoutePage,
  ExtensionsRoutePage,
  PolicyRoutePage,
  QualityRoutePage,
} from "./governance-pages.tsx";
import type { DashboardQueryRuntime } from "./dashboard-runtime.ts";
import { dashboardRoutePaths, dashboardTaskRoutePaths } from "./navigation.ts";
import { navigationLocatorFromLocation } from "./navigation-state.ts";
import { SettingsRoutePage } from "./settings-page.tsx";

interface DashboardRouterContext {
  readonly model: DashboardQueryRuntime;
  readonly persistNavigation?: (locator: string) => Promise<void>;
  readonly preview: boolean;
}

interface EmptySearch { readonly invalid?: true }
interface PolicySearch { readonly invalid?: true; readonly selected?: string }

const maximumEntityIDBytes = 512;
const unsafeEntityID = /[\\/\p{Cc}]/u;

function validEntityID(value: string): boolean {
  return value.length > 0 &&
    value.trim() === value &&
    !unsafeEntityID.test(value) &&
    new TextEncoder().encode(value).length <= maximumEntityIDBytes;
}

function validateEmptySearch(search: Record<string, unknown>): EmptySearch {
  return Object.keys(search).length === 0 ? {} : { invalid: true };
}

function validatePolicySearch(search: Record<string, unknown>): PolicySearch {
  if (Object.keys(search).some((key) => key !== "selected")) return { invalid: true };
  if (search.selected === undefined) return {};
  return typeof search.selected === "string" && validEntityID(search.selected)
    ? { selected: search.selected }
    : { invalid: true };
}

function canonicalizeEmptySearch(to: string) {
  return ({ search }: { readonly search: EmptySearch }) => {
    if (search.invalid === true) throw redirect({ replace: true, search: {}, to });
  };
}

function InvalidLocator() {
  const { t } = useTranslation();
  return <div className="page"><InlineProblem message={t("error.invalidLocator")} /></div>;
}

const rootRoute = createRootRouteWithContext<DashboardRouterContext>()({
  component: function RootRoute() {
    const { model, persistNavigation, preview } = rootRoute.useRouteContext();
    const location = useRouterState({ select: (state) => state.location });
    useEffect(() => {
      const locator = navigationLocatorFromLocation(location);
      if (persistNavigation !== undefined && locator !== undefined) {
        void persistNavigation(locator).catch(() => undefined);
      }
    }, [location, persistNavigation]);
    useEffect(() => {
      let frame = 0;
      let attempts = 0;
      const focusHeading = () => {
        const heading = document.querySelector<HTMLElement>("#main-content h1");
        if (heading !== null) {
          heading.focus({ preventScroll: true });
          return;
        }
        attempts += 1;
        if (attempts < 30) frame = requestAnimationFrame(focusHeading);
      };
      frame = requestAnimationFrame(focusHeading);
      return () => cancelAnimationFrame(frame);
    }, [location.pathname]);
    return <DashboardShell model={model} preview={preview} />;
  },
  notFoundComponent: () => <Navigate replace search={{}} to={dashboardRoutePaths.captures} />,
});

const indexRoute = createRoute({
  beforeLoad: () => { throw redirect({ replace: true, search: {}, to: dashboardRoutePaths.captures }); },
  getParentRoute: () => rootRoute,
  path: "/",
});

const capturesRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.captures),
  component: CapturesRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.captures,
  validateSearch: validateEmptySearch,
});

const captureDetailRoute = createRoute({
  component: function CaptureDetailRoute() {
    const { captureKey } = captureDetailRoute.useParams();
    const { invalid } = captureDetailRoute.useSearch();
    return invalid === true || captureKey === null
      ? <InvalidLocator />
      : <CaptureDetailRoutePage captureKey={captureKey} />;
  },
  getParentRoute: () => rootRoute,
  params: { parse: ({ captureKey }) => ({ captureKey: validEntityID(captureKey) ? captureKey : null }) },
  path: dashboardTaskRoutePaths.captureDetail,
  validateSearch: validateEmptySearch,
});

const captureRequestsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardTaskRoutePaths.captureRequests),
  component: RequestsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.captureRequests,
  validateSearch: validateEmptySearch,
});

const exchangeRoute = createRoute({
  component: function ExchangeDetailRoute() {
    const { exchangeId } = exchangeRoute.useParams();
    const { invalid } = exchangeRoute.useSearch();
    return invalid === true || exchangeId === null
      ? <InvalidLocator />
      : <ExchangeRoutePage exchangeId={exchangeId} />;
  },
  getParentRoute: () => rootRoute,
  params: { parse: ({ exchangeId }) => ({ exchangeId: validEntityID(exchangeId) ? exchangeId : null }) },
  path: dashboardTaskRoutePaths.activityRequest,
  validateSearch: validateEmptySearch,
});

const environmentsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.environments),
  component: EnvironmentsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.environments,
  validateSearch: validateEmptySearch,
});

const environmentDetailRoute = createRoute({
  component: function EnvironmentDetailRoute() {
    const { environmentId } = environmentDetailRoute.useParams();
    const { invalid } = environmentDetailRoute.useSearch();
    return invalid === true || environmentId === null
      ? <InvalidLocator />
      : <EnvironmentDetailRoutePage environmentId={environmentId} />;
  },
  getParentRoute: () => rootRoute,
  params: { parse: ({ environmentId }) => ({ environmentId: validEntityID(environmentId) ? environmentId : null }) },
  path: dashboardTaskRoutePaths.environmentDetail,
  validateSearch: validateEmptySearch,
});

const environmentRevisionRoute = createRoute({
  component: function EnvironmentRevisionRoute() {
    const { environmentId, environmentRevision } = environmentRevisionRoute.useParams();
    const { invalid } = environmentRevisionRoute.useSearch();
    return invalid === true || environmentId === null || environmentRevision === null
      ? <InvalidLocator />
      : <EnvironmentDetailRoutePage environmentId={environmentId} revision={environmentRevision} />;
  },
  getParentRoute: () => rootRoute,
  params: {
    parse: ({ environmentId, environmentRevision }) => ({
      environmentId: validEntityID(environmentId) ? environmentId : null,
      environmentRevision: /^[1-9][0-9]*$/u.test(environmentRevision)
        ? Number.parseInt(environmentRevision, 10)
        : null,
    }),
  },
  path: dashboardTaskRoutePaths.environmentRevision,
  validateSearch: validateEmptySearch,
});

const accountsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.accounts),
  component: AccountsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.accounts,
  validateSearch: validateEmptySearch,
});
const extensionsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.extensions),
  component: ExtensionsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.extensions,
  validateSearch: validateEmptySearch,
});
const qualityRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.quality),
  component: QualityRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.quality,
  validateSearch: validateEmptySearch,
});
const policyRoute = createRoute({
  component: function Policy() {
    const search = policyRoute.useSearch();
    return search.invalid === true
      ? <InvalidLocator />
      : <PolicyRoutePage selectedApprovalId={search.selected} />;
  },
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.policy,
  validateSearch: validatePolicySearch,
});
const settingsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.settings),
  component: function Settings() {
    return <SettingsRoutePage preview={settingsRoute.useRouteContext().preview} />;
  },
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.settings,
  validateSearch: validateEmptySearch,
});

const invalidLocatorRoute = createRoute({ component: InvalidLocator, getParentRoute: () => rootRoute, path: "/__invalid-locator" });
const routeTree = rootRoute.addChildren([
  indexRoute,
  capturesRoute,
  captureDetailRoute,
  captureRequestsRoute,
  exchangeRoute,
  environmentsRoute,
  environmentDetailRoute,
  environmentRevisionRoute,
  accountsRoute,
  extensionsRoute,
  policyRoute,
  qualityRoute,
  settingsRoute,
  invalidLocatorRoute,
]);

export function createDashboardRouter(history: RouterHistory, context: DashboardRouterContext) {
  return createRouter({
    context,
    defaultPreload: "intent",
    history,
    notFoundMode: "root",
    routeTree,
    scrollRestoration: true,
    scrollToTopSelectors: ["#main-content"],
  });
}

function canonicalizeDashboardHash(): void {
  const currentHash = window.location.hash;
  if (!currentHash.startsWith("#")) return;
  const original = currentHash.slice(1);
  let external = original.startsWith("/") ? original.slice(1) : original;
  const nestedHashIndex = external.indexOf("#");
  const primary = nestedHashIndex === -1 ? external : external.slice(0, nestedHashIndex);
  const nestedHash = nestedHashIndex === -1 ? "" : external.slice(nestedHashIndex);
  const queryIndex = primary.indexOf("?");
  const path = queryIndex === -1 ? primary : primary.slice(0, queryIndex);
  const search = queryIndex === -1 ? "" : primary.slice(queryIndex);
  external = `${path.replace(/\/+$/u, "")}${search}${nestedHash}`;
  if (external !== original) {
    window.history.replaceState(window.history.state, "", `${window.location.pathname}${window.location.search}${external.length === 0 ? "" : `#${external}`}`);
  }
}

export function createDesktopHashHistory(): RouterHistory {
  canonicalizeDashboardHash();
  return createBrowserHistory({
    createHref: (href) => `${window.location.pathname}${window.location.search}#${href.startsWith("/") ? href.slice(1) : href}`,
    parseLocation: () => {
      canonicalizeDashboardHash();
      const hashParts = window.location.hash.split("#").slice(1);
      const externalRouteHref = hashParts[0] ?? "";
      const routeHref = externalRouteHref.length === 0 ? "/" : externalRouteHref.startsWith("/") ? externalRouteHref : `/${externalRouteHref}`;
      try { decodeURI(routeHref); } catch { return parseHref("/__invalid-locator", window.history.state); }
      const routeHash = hashParts.slice(1).join("#");
      return parseHref(`${routeHref}${routeHash.length === 0 ? "" : `#${routeHash}`}`, window.history.state);
    },
  });
}

const unboundModel = undefined as unknown as DashboardQueryRuntime;
export const dashboardRouter = createDashboardRouter(createDesktopHashHistory(), { model: unboundModel, preview: false });
export type DashboardRouter = typeof dashboardRouter;

export function DashboardRouterProvider({
  model,
  persistNavigation,
  preview = false,
  router,
}: {
  readonly model: DashboardQueryRuntime;
  readonly persistNavigation?: (locator: string) => Promise<void>;
  readonly preview?: boolean;
  readonly router?: DashboardRouter;
}) {
  const activeRouter = useMemo(
    () =>
      router ??
      createDashboardRouter(createDesktopHashHistory(), {
        model,
        preview,
        ...(persistNavigation === undefined ? {} : { persistNavigation }),
      }),
    [model, persistNavigation, preview, router],
  );
  return (
    <QueryClientProvider client={model.queryClient} key={model.sessionKey}>
      <RouterProvider
        context={{
          model,
          preview,
          ...(persistNavigation === undefined ? {} : { persistNavigation }),
        }}
        router={activeRouter}
      />
    </QueryClientProvider>
  );
}

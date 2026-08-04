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
import { QueryClientProvider } from "@tanstack/react-query";
import { parseHref } from "@tanstack/history";
import { useEffect } from "react";
import type { DashboardQueryRuntime } from "./dashboard-runtime.ts";
import {
  AccessRoutePage,
  ActivityRequestsRoutePage,
  ActivityRequestRoutePage,
  ActivityRoutePage,
  DashboardShell,
  DashboardsRoutePage,
  ExtensionsRoutePage,
  InvalidDashboardLocatorRoutePage,
  InvalidPolicyLocatorRoutePage,
  OverviewRoutePage,
  PolicyRoutePage,
  QualityRoutePage,
  SettingsRoutePage,
  UnavailableTaskRoutePage,
} from "./App.tsx";
import { dashboardRoutePaths, dashboardTaskRoutePaths } from "./navigation.ts";
import { navigationLocatorFromLocation } from "./navigation-state.ts";

interface DashboardRouterContext {
  readonly model: DashboardQueryRuntime;
  readonly persistNavigation?: (locator: string) => Promise<void>;
  readonly preview: boolean;
}

interface PolicySearch {
  readonly invalid?: true;
  readonly selected?: string;
}

const maximumApprovalIdBytes = 512;
const maximumAccessIdBytes = 128;
const maximumRouteLocatorBytes = 512;
const unsafeApprovalId = /\p{Cc}/u;
const unsafeRouteLocator = /[\/\\\p{C}]/u;

function validatePolicySearch(search: Record<string, unknown>): PolicySearch {
  if (Object.keys(search).some((key) => key !== "selected")) {
    return { invalid: true };
  }
  const selected = search.selected;
  if (selected === undefined) {
    return {};
  }
  if (
    typeof selected !== "string" ||
    selected.length === 0 ||
    selected.trim() !== selected ||
    unsafeApprovalId.test(selected) ||
    new TextEncoder().encode(selected).length > maximumApprovalIdBytes
  ) {
    return { invalid: true };
  }
  return { selected };
}

function safeLegacyPolicySearch(search: Record<string, unknown>): PolicySearch {
  const validated = validatePolicySearch(search);
  return validated.invalid === true ? {} : validated;
}

interface EmptyTaskSearch {
  readonly invalid?: true;
}

function validateEmptyTaskSearch(
  search: Record<string, unknown>,
): EmptyTaskSearch {
  return Object.keys(search).length === 0 ? {} : { invalid: true };
}

function canonicalizeEmptySearch(
  to:
    | (typeof dashboardRoutePaths)[keyof typeof dashboardRoutePaths]
    | typeof dashboardTaskRoutePaths.activityRequests,
) {
  return ({ search }: { readonly search: EmptyTaskSearch }) => {
    if (search.invalid === true) {
      throw redirect({ replace: true, search: {}, to });
    }
  };
}

function validRouteLocator(value: string, maximumBytes: number): boolean {
  return (
    value.length > 0 &&
    value.trim() === value &&
    !unsafeRouteLocator.test(value) &&
    new TextEncoder().encode(value).length <= maximumBytes
  );
}

const rootRoute = createRootRouteWithContext<DashboardRouterContext>()({
  component: function RootRoute() {
    const { model, persistNavigation, preview } = rootRoute.useRouteContext();
    const location = useRouterState({
      select: (state) => state.location,
    });
    useEffect(() => {
      if (persistNavigation === undefined) {
        return;
      }
      const locator = navigationLocatorFromLocation(location);
      if (locator !== undefined) {
        void persistNavigation(locator).catch(() => undefined);
      }
    }, [location, persistNavigation]);
    return <DashboardShell model={model} preview={preview} />;
  },
  notFoundComponent: () => (
    <Navigate replace search={{}} to={dashboardRoutePaths.overview} />
  ),
});

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({
      replace: true,
      search: {},
      to: dashboardRoutePaths.overview,
    });
  },
});

const overviewRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.overview),
  component: OverviewRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.overview,
  validateSearch: validateEmptyTaskSearch,
});
const dashboardsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.dashboards),
  component: DashboardsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.dashboards,
  validateSearch: validateEmptyTaskSearch,
});
const accessRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.access),
  component: AccessRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.access,
  validateSearch: validateEmptyTaskSearch,
});
const activityRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.activity),
  component: ActivityRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.activity,
  validateSearch: validateEmptyTaskSearch,
});
const qualityRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.quality),
  component: QualityRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.quality,
  validateSearch: validateEmptyTaskSearch,
});
const extensionsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.extensions),
  component: ExtensionsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.extensions,
  validateSearch: validateEmptyTaskSearch,
});
const policyRoute = createRoute({
  component: function PolicyRoute() {
    const { invalid, selected } = policyRoute.useSearch();
    if (invalid === true) {
      return <InvalidPolicyLocatorRoutePage />;
    }
    return <PolicyRoutePage selectedApprovalId={selected} />;
  },
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.policy,
  validateSearch: validatePolicySearch,
});
const settingsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(dashboardRoutePaths.settings),
  component: SettingsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardRoutePaths.settings,
  validateSearch: validateEmptyTaskSearch,
});

const invalidLocatorRoute = createRoute({
  component: InvalidDashboardLocatorRoutePage,
  getParentRoute: () => rootRoute,
  path: "/__invalid-locator",
});

const accessRoutingRoute = createRoute({
  component: function AccessRoutingRoute() {
    const { accessId } = accessRoutingRoute.useParams();
    const { invalid } = accessRoutingRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true || accessId === null}
        task="accessRouting"
      />
    );
  },
  getParentRoute: () => rootRoute,
  params: {
    parse: ({ accessId }) => ({
      accessId: validRouteLocator(accessId, maximumAccessIdBytes)
        ? accessId
        : null,
    }),
  },
  path: dashboardTaskRoutePaths.accessRouting,
  validateSearch: validateEmptyTaskSearch,
});

const activityRequestsRoute = createRoute({
  beforeLoad: canonicalizeEmptySearch(
    dashboardTaskRoutePaths.activityRequests,
  ),
  component: ActivityRequestsRoutePage,
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.activityRequests,
  validateSearch: validateEmptyTaskSearch,
});

const activityRequestRoute = createRoute({
  component: function ActivityRequestRoute() {
    const { exchangeId } = activityRequestRoute.useParams();
    const { invalid } = activityRequestRoute.useSearch();
    return (
      invalid === true || exchangeId === null ? (
        <UnavailableTaskRoutePage invalid task="activityRequest" />
      ) : (
        <ActivityRequestRoutePage exchangeId={exchangeId} />
      )
    );
  },
  getParentRoute: () => rootRoute,
  params: {
    parse: ({ exchangeId }) => ({
      exchangeId: validRouteLocator(exchangeId, maximumRouteLocatorBytes)
        ? exchangeId
        : null,
    }),
  },
  path: dashboardTaskRoutePaths.activityRequest,
  validateSearch: validateEmptyTaskSearch,
});

const extensionDiscoverRoute = createRoute({
  component: function ExtensionDiscoverRoute() {
    const { invalid } = extensionDiscoverRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="extensionDiscover"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.extensionDiscover,
  validateSearch: validateEmptyTaskSearch,
});

const extensionInstalledRoute = createRoute({
  component: function ExtensionInstalledRoute() {
    const { invalid } = extensionInstalledRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="extensionInstalled"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.extensionInstalled,
  validateSearch: validateEmptyTaskSearch,
});

const extensionDetailRoute = createRoute({
  component: function ExtensionDetailRoute() {
    const { extensionId } = extensionDetailRoute.useParams();
    const { invalid } = extensionDetailRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true || extensionId === null}
        task="extensionDetail"
      />
    );
  },
  getParentRoute: () => rootRoute,
  params: {
    parse: ({ extensionId }) => ({
      extensionId: validRouteLocator(extensionId, maximumRouteLocatorBytes)
        ? extensionId
        : null,
    }),
  },
  path: dashboardTaskRoutePaths.extensionDetail,
  validateSearch: validateEmptyTaskSearch,
});

const extensionDevelopRoute = createRoute({
  component: function ExtensionDevelopRoute() {
    const { invalid } = extensionDevelopRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="extensionDevelop"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.extensionDevelop,
  validateSearch: validateEmptyTaskSearch,
});

const qualitySitesRoute = createRoute({
  component: function QualitySitesRoute() {
    const { invalid } = qualitySitesRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="qualitySites"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.qualitySites,
  validateSearch: validateEmptyTaskSearch,
});

const dashboardSystemRoute = createRoute({
  component: function DashboardSystemRoute() {
    const { invalid } = dashboardSystemRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="dashboardSystem"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.dashboardSystem,
  validateSearch: validateEmptyTaskSearch,
});

const dashboardExtensionRoute = createRoute({
  component: function DashboardExtensionRoute() {
    const { dashboardId } = dashboardExtensionRoute.useParams();
    const { invalid } = dashboardExtensionRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true || dashboardId === null}
        task="dashboardExtension"
      />
    );
  },
  getParentRoute: () => rootRoute,
  params: {
    parse: ({ dashboardId }) => ({
      dashboardId: validRouteLocator(dashboardId, maximumRouteLocatorBytes)
        ? dashboardId
        : null,
    }),
  },
  path: dashboardTaskRoutePaths.dashboardExtension,
  validateSearch: validateEmptyTaskSearch,
});

const settingsRecoveryRoute = createRoute({
  component: function SettingsRecoveryRoute() {
    const { invalid } = settingsRecoveryRoute.useSearch();
    return (
      <UnavailableTaskRoutePage
        invalid={invalid === true}
        task="settingsRecovery"
      />
    );
  },
  getParentRoute: () => rootRoute,
  path: dashboardTaskRoutePaths.settingsRecovery,
  validateSearch: validateEmptyTaskSearch,
});

const approvalsRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/approvals",
  beforeLoad: ({ search }) => {
    throw redirect({
      replace: true,
      search: safeLegacyPolicySearch(search),
      to: dashboardRoutePaths.policy,
    });
  },
});
const policyRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/policy",
  beforeLoad: ({ search }) => {
    throw redirect({
      replace: true,
      search: safeLegacyPolicySearch(search),
      to: dashboardRoutePaths.policy,
    });
  },
});
const policiesRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/policies",
  beforeLoad: ({ search }) => {
    throw redirect({
      replace: true,
      search: safeLegacyPolicySearch(search),
      to: dashboardRoutePaths.policy,
    });
  },
});
const systemRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/system",
  beforeLoad: () => {
    throw redirect({
      replace: true,
      search: {},
      to: dashboardRoutePaths.settings,
    });
  },
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  overviewRoute,
  dashboardsRoute,
  accessRoute,
  activityRoute,
  qualityRoute,
  extensionsRoute,
  policyRoute,
  settingsRoute,
  invalidLocatorRoute,
  accessRoutingRoute,
  activityRequestsRoute,
  activityRequestRoute,
  extensionDiscoverRoute,
  extensionInstalledRoute,
  extensionDetailRoute,
  extensionDevelopRoute,
  qualitySitesRoute,
  dashboardSystemRoute,
  dashboardExtensionRoute,
  settingsRecoveryRoute,
  approvalsRedirectRoute,
  policyRedirectRoute,
  policiesRedirectRoute,
  systemRedirectRoute,
]);

export function createDashboardRouter(
  history: RouterHistory,
  context: DashboardRouterContext,
) {
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

const unboundModel = undefined as unknown as DashboardQueryRuntime;
function canonicalizeDashboardHash(): void {
  const currentHash = window.location.hash;
  if (!currentHash.startsWith("#")) {
    return;
  }
  const original = currentHash.slice(1);
  let external = original.startsWith("/") ? original.slice(1) : original;
  const nestedHashIndex = external.indexOf("#");
  const primary =
    nestedHashIndex === -1 ? external : external.slice(0, nestedHashIndex);
  const nestedHash =
    nestedHashIndex === -1 ? "" : external.slice(nestedHashIndex);
  const queryIndex = primary.indexOf("?");
  const path = queryIndex === -1 ? primary : primary.slice(0, queryIndex);
  const search = queryIndex === -1 ? "" : primary.slice(queryIndex);
  external = `${path.replace(/\/+$/u, "")}${search}${nestedHash}`;
  if (external === original) {
    return;
  }
  window.history.replaceState(
    window.history.state,
    "",
    `${window.location.pathname}${window.location.search}${
      external.length === 0 ? "" : `#${external}`
    }`,
  );
}

export function createDesktopHashHistory(): RouterHistory {
  canonicalizeDashboardHash();
  return createBrowserHistory({
    createHref: (href) => {
      const externalHref = href.startsWith("/") ? href.slice(1) : href;
      return `${window.location.pathname}${window.location.search}#${externalHref}`;
    },
    parseLocation: () => {
      canonicalizeDashboardHash();
      const hashParts = window.location.hash.split("#").slice(1);
      const externalRouteHref = hashParts[0] ?? "";
      const routeHref =
        externalRouteHref.length === 0
          ? "/"
          : externalRouteHref.startsWith("/")
            ? externalRouteHref
            : `/${externalRouteHref}`;
      const routeHash = hashParts.slice(1).join("#");
      try {
        decodeURI(routeHref);
      } catch {
        return parseHref("/__invalid-locator", window.history.state);
      }
      return parseHref(
        `${routeHref}${routeHash.length === 0 ? "" : `#${routeHash}`}`,
        window.history.state,
      );
    },
  });
}

export const dashboardRouter = createDashboardRouter(
  createDesktopHashHistory(),
  {
    model: unboundModel,
    preview: false,
  },
);

export type DashboardRouter = ReturnType<typeof createDashboardRouter>;

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof dashboardRouter;
  }
}

export function DashboardRouterProvider({
  model,
  persistNavigation,
  preview = false,
  router = dashboardRouter,
}: {
  readonly model: DashboardQueryRuntime;
  readonly persistNavigation?: (locator: string) => Promise<void>;
  readonly preview?: boolean;
  readonly router?: DashboardRouter;
}) {
  return (
    <QueryClientProvider client={model.queryClient} key={model.sessionKey}>
      <RouterProvider
        context={{
          model,
          preview,
          ...(persistNavigation === undefined ? {} : { persistNavigation }),
        }}
        router={router}
      />
    </QueryClientProvider>
  );
}

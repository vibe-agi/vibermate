export type DashboardView =
  | "overview"
  | "dashboards"
  | "access"
  | "activity"
  | "quality"
  | "extensions"
  | "policy"
  | "settings";

export const dashboardRoutePaths = {
  overview: "/overview",
  dashboards: "/dashboards",
  access: "/access",
  activity: "/activity",
  quality: "/quality",
  extensions: "/extensions",
  policy: "/policies/approvals",
  settings: "/settings",
} as const satisfies Record<DashboardView, `/${string}`>;

export const dashboardTaskRoutePaths = {
  accessRouting: "/access/$accessId/routing",
  activityRequest: "/activity/requests/$exchangeId",
  activityRequests: "/activity/requests",
  dashboardExtension: "/dashboards/extensions/$dashboardId",
  dashboardSystem: "/dashboards/system",
  extensionDetail: "/extensions/detail/$extensionId",
  extensionDevelop: "/extensions/develop",
  extensionDiscover: "/extensions/discover",
  extensionInstalled: "/extensions/installed",
  policyApprovals: dashboardRoutePaths.policy,
  qualitySites: "/quality/sites",
  settingsRecovery: "/settings/recovery",
} as const;

export interface DashboardNavigation {
  readonly openView: (
    view: DashboardView,
    options?: {
      readonly selectedApprovalId?: string;
      readonly replace?: boolean;
    },
  ) => void;
}

export function viewFromPathname(pathname: string): DashboardView {
  const firstSegment = pathname.split("/").filter(Boolean)[0];
  switch (firstSegment) {
    case "overview":
    case "dashboards":
    case "access":
    case "activity":
    case "quality":
    case "extensions":
    case "settings":
      return firstSegment;
    case "policies":
      return "policy";
    default:
      return "overview";
  }
}

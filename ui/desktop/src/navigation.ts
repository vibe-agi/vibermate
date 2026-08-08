export type DashboardView =
  | "captures"
  | "environments"
  | "accounts"
  | "extensions"
  | "policy"
  | "quality"
  | "settings";

export const dashboardRoutePaths = {
  captures: "/captures",
  environments: "/environments",
  accounts: "/accounts",
  extensions: "/extensions",
  policy: "/policies/approvals",
  quality: "/quality",
  settings: "/settings",
} as const satisfies Record<DashboardView, `/${string}`>;

export const dashboardTaskRoutePaths = {
  captureDetail: "/captures/$captureKey",
  captureRequests: "/captures/requests",
  activityRequest: "/activity/requests/$exchangeId",
  environmentDetail: "/environments/$environmentId",
  environmentRevision:
    "/environments/$environmentId/revisions/$environmentRevision",
  policyApprovals: dashboardRoutePaths.policy,
} as const;

export function viewFromPathname(pathname: string): DashboardView {
  const firstSegment = pathname.split("/").filter(Boolean)[0];
  switch (firstSegment) {
    case "captures":
    case "activity":
      return "captures";
    case "environments":
    case "accounts":
    case "extensions":
    case "quality":
    case "settings":
      return firstSegment;
    case "policies":
      return "policy";
    default:
      return "captures";
  }
}

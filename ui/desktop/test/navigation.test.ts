import { describe, expect, it } from "vitest";
import {
  dashboardRoutePaths,
  dashboardTaskRoutePaths,
  viewFromPathname,
} from "../src/navigation.ts";

describe("dashboard navigation contract", () => {
  it("uses the design spelling for the canonical policy task", () => {
    expect(dashboardRoutePaths.policy).toBe("/policies/approvals");
    expect(dashboardTaskRoutePaths.policyApprovals).toBe("/policies/approvals");
  });

  it.each([
    ["/overview", "overview"],
    ["/access", "access"],
    ["/access/claude/routing", "access"],
    ["/activity/requests/ex204", "activity"],
    ["/extensions/detail/prompt-polish", "extensions"],
    ["/quality/sites", "quality"],
    ["/dashboards/extensions/agent-actions", "dashboards"],
    ["/policies/approvals", "policy"],
    ["/settings/recovery", "settings"],
  ] as const)("maps %s to the correct top-level view", (pathname, view) => {
    expect(viewFromPathname(pathname)).toBe(view);
  });

  it("matches complete path segments only", () => {
    expect(viewFromPathname("/accessibility")).toBe("overview");
    expect(viewFromPathname("/policy")).toBe("overview");
  });
});

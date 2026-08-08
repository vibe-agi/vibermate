import { describe, expect, it } from "vitest";
import {
  dashboardRoutePaths,
  dashboardTaskRoutePaths,
  viewFromPathname,
} from "../src/navigation.ts";

describe("Environment-first navigation contract", () => {
  it("keeps runtime, resources, governance, and settings as the complete public IA", () => {
    expect(dashboardRoutePaths).toEqual({
      captures: "/captures",
      environments: "/environments",
      accounts: "/accounts",
      extensions: "/extensions",
      policy: "/policies/approvals",
      quality: "/quality",
      settings: "/settings",
    });
  });

  it.each([
    ["/captures", "captures"],
    ["/captures/managed_run:run-1", "captures"],
    ["/activity/requests/ex-1", "captures"],
    ["/environments/work", "environments"],
    ["/accounts", "accounts"],
    ["/extensions", "extensions"],
    ["/quality", "quality"],
    ["/policies/approvals", "policy"],
    ["/settings", "settings"],
  ] as const)("maps %s to %s", (pathname, view) => {
    expect(viewFromPathname(pathname)).toBe(view);
  });

  it("does not retain Access/Profile compatibility routes", () => {
    expect(Object.values(dashboardRoutePaths)).not.toContain("/access");
    expect(Object.values(dashboardTaskRoutePaths).join(" ")).not.toMatch(/access|profile/ui);
    expect(viewFromPathname("/access")).toBe("captures");
  });
});

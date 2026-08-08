import { describe, expect, it } from "vitest";
import {
  navigationLocatorFromLocation,
  validNavigationLocator,
} from "../src/navigation-state.ts";

describe("persisted Environment-first navigation", () => {
  it.each([
    "captures",
    "captures/requests",
    "captures/managed_run%3Arun-1",
    "activity/requests/ex-204",
    "environments",
    "environments/work",
    "environments/work/revisions/3",
    "accounts",
    "extensions",
    "quality",
    "policies/approvals",
    "policies/approvals?selected=approval-network-sample",
    "settings",
  ])("accepts %s", (locator) => expect(validNavigationLocator(locator)).toBe(true));

  it.each([
    "",
    "/captures",
    "overview",
    "access",
    "profiles",
    "captures/%2Fescape",
    "environments/%00unsafe",
    "environments/work/revisions/0",
    "environments/work/revisions/latest",
    "policies/approvals?unknown=value",
    "policies/approvals?selected=secret%3Aprovider",
    "captures#nested",
  ])("rejects %s", (locator) => expect(validNavigationLocator(locator)).toBe(false));

  it("serializes only canonical Router state", () => {
    expect(navigationLocatorFromLocation({
      pathname: "/environments/work/revisions/3",
      searchStr: "",
    })).toBe("environments/work/revisions/3");
    expect(navigationLocatorFromLocation({
      pathname: "/policies/approvals",
      searchStr: "?selected=approval-network-sample",
    })).toBe("policies/approvals?selected=approval-network-sample");
    expect(navigationLocatorFromLocation({
      pathname: "/__invalid-locator",
      searchStr: "",
    })).toBeUndefined();
  });
});

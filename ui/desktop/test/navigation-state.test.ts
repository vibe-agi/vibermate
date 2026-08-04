import { describe, expect, it } from "vitest";
import {
  navigationLocatorFromLocation,
  validNavigationLocator,
} from "../src/navigation-state.ts";

describe("persisted dashboard navigation", () => {
  it.each([
    "overview",
    "access",
    "access/claude/routing",
    "activity/requests/ex204",
    "extensions/discover",
    "extensions/installed",
    "extensions/detail/prompt-polish",
    "quality/sites",
    "dashboards/system",
    "activity/requests",
    "policies/approvals",
    "policies/approvals?selected=approval-network-sample",
    "settings/recovery",
    "extensions/develop",
    "dashboards/extensions/agent-actions",
  ])("accepts the canonical locator %s", (locator) => {
    expect(validNavigationLocator(locator)).toBe(true);
  });

  it.each([
    "",
    "/overview",
    "policy",
    "approvals",
    "policies",
    "access/%20unsafe%20/routing",
    "access/%2F/routing",
    "access/%E0%A4%A/routing",
    "extensions/discover?selected=not-allowed",
    "policies/approvals?unknown=value",
    "policies/approvals?selected=one&selected=two",
    "policies/approvals?selected=%00value",
    "policies/approvals?selected=secret%3A%2F%2Fprovider%2Fwork",
    "overview#nested",
    "not-a-real-route",
  ])("rejects the unsafe or non-canonical locator %s", (locator) => {
    expect(validNavigationLocator(locator)).toBe(false);
  });

  it("serializes only a complete canonical Router location", () => {
    expect(
      navigationLocatorFromLocation({
        pathname: "/policies/approvals",
        searchStr: "?selected=approval-network-sample",
      }),
    ).toBe("policies/approvals?selected=approval-network-sample");
    expect(
      navigationLocatorFromLocation({
        pathname: "/extensions/discover",
        searchStr: "?selected=not-allowed",
      }),
    ).toBeUndefined();
    expect(
      navigationLocatorFromLocation({
        pathname: "/__invalid-locator",
        searchStr: "",
      }),
    ).toBeUndefined();
  });
});

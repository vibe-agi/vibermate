import { describe, expect, it } from "vitest";
import {
  buildAccessApplyInput,
  initialAccessForm,
  newAccessForm,
  validAccessForm,
} from "../src/access-form.ts";

describe("Access form defaults", () => {
  it("starts with the client's current login and follow-client presentation", () => {
    const form = {
      ...newAccessForm(),
      name: "Work",
    };

    expect(form.mode).toBe("current-login");
    expect(form.providerOrigin).toBe("");
    expect(form.fixedModel).toBe("");
    expect(form.routeName).toBe("");
    expect(form.upstreamPresentation).toBe("follow-client");
    expect(validAccessForm(form)).toBe(true);
  });

  it("accepts an explicitly selected provider while preserving follow-client", () => {
    const form = {
      ...initialAccessForm,
      accessId: "work",
      mode: "managed" as const,
      fixedModel: "example-model",
      name: "Work",
      providerOrigin: "https://gateway.example/v1",
      routeName: "Primary route",
    };

    expect(validAccessForm(form)).toBe(true);
    expect(form.upstreamPresentation).toBe("follow-client");
  });

  it("builds a current-login Access without a provider or credential", () => {
    const input = buildAccessApplyInput({
      ...initialAccessForm,
      accessId: "work",
      name: "Work",
    });

    expect(input.access.defaultRouteSetId).toBe("");
    expect(input.access.profileIds).toEqual([]);
    expect(input.profiles).toEqual([]);
    expect(input.providerTargets).toEqual([]);
    expect(input.accountBindings).toEqual([]);
    expect(input.routeSets).toEqual([]);
    expect(input.agentEndpoint.clientOrigin).toBe(
      "https://api.anthropic.com",
    );
  });
});

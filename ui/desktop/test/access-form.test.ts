import { describe, expect, it } from "vitest";
import {
  initialAccessForm,
  newAccessForm,
  validAccessForm,
} from "../src/access-form.ts";

describe("Access form defaults", () => {
  it("does not silently select a provider, model, or route", () => {
    const form = newAccessForm();

    expect(form.providerOrigin).toBe("");
    expect(form.fixedModel).toBe("");
    expect(form.routeName).toBe("");
    expect(form.upstreamPresentation).toBe("follow-client");
    expect(validAccessForm(form)).toBe(false);
  });

  it("accepts an explicitly selected provider while preserving follow-client", () => {
    const form = {
      ...initialAccessForm,
      accessId: "work",
      fixedModel: "example-model",
      name: "Work",
      providerOrigin: "https://gateway.example/v1",
      routeName: "Primary route",
    };

    expect(validAccessForm(form)).toBe(true);
    expect(form.upstreamPresentation).toBe("follow-client");
  });
});

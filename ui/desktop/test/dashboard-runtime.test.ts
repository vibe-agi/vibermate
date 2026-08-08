import { describe, expect, it } from "vitest";
import {
  ControlContractError,
  ControlProblem,
  type ControlClient,
} from "../src/control-client.ts";
import {
  DashboardQueryRuntime,
  controlErrorKey,
  dashboardQueryKeys,
} from "../src/dashboard-runtime.ts";

const inertClient = { close: () => undefined } as unknown as ControlClient;

describe("Dashboard query ownership", () => {
  it("owns one query cache per authenticated Desktop session", async () => {
    const first = new DashboardQueryRuntime(inertClient, 1_000);
    const second = new DashboardQueryRuntime(inertClient, 1_000);
    expect(first.sessionKey).not.toBe(second.sessionKey);
    expect(first.queryClient).not.toBe(second.queryClient);
    expect(dashboardQueryKeys.environment("work")).toEqual([
      "vibermate",
      "desktop",
      "environment",
      "work",
    ]);
    await first.dispose();
    await second.dispose();
  });

  it("rejects an unsafe polling cadence", () => {
    expect(() => new DashboardQueryRuntime(inertClient, 499)).toThrow(
      /polling interval/u,
    );
  });

  it("maps typed control failures without exposing transport details", () => {
    expect(controlErrorKey(new ControlProblem(409, "conflict", "error.conflict"))).toBe("error.conflict");
    expect(controlErrorKey(new ControlContractError())).toBe("error.control_invalid_response");
    expect(controlErrorKey(new Error("secret transport detail"))).toBe("error.runtime_unavailable");
  });
});

import { QueryClient } from "@tanstack/react-query";
import { ControlContractError, ControlProblem, type ControlClient } from "./control-client.ts";

let nextDashboardSessionKey = 1;

export const dashboardQueryKeys = {
  root: ["vibermate", "desktop"] as const,
  status: ["vibermate", "desktop", "status"] as const,
  offline: ["vibermate", "desktop", "offline"] as const,
  environments: ["vibermate", "desktop", "environments"] as const,
  accounts: ["vibermate", "desktop", "provider-accounts"] as const,
  captures: ["vibermate", "desktop", "captures"] as const,
  activities: ["vibermate", "desktop", "activities"] as const,
  approvals: ["vibermate", "desktop", "approvals"] as const,
  manualCaptures: ["vibermate", "desktop", "manual-captures"] as const,
  environment: (environmentId: string) =>
    ["vibermate", "desktop", "environment", environmentId] as const,
  environmentDraft: (environmentId: string) =>
    ["vibermate", "desktop", "environment", environmentId, "draft"] as const,
  capture: (captureKey: string) =>
    ["vibermate", "desktop", "capture", captureKey] as const,
  captureAssignment: (captureKey: string) =>
    ["vibermate", "desktop", "capture", captureKey, "assignment"] as const,
  exchange: (exchangeId: string) =>
    ["vibermate", "desktop", "exchange", exchangeId] as const,
};

/**
 * Owns server projections for exactly one authenticated Desktop session.
 * Server data lives in QueryClient; no second mutable dashboard store exists.
 */
export class DashboardQueryRuntime {
  readonly client: ControlClient;
  readonly pollInterval: number;
  readonly queryClient: QueryClient;
  readonly sessionKey: number;

  constructor(client: ControlClient, pollInterval = 2_000) {
    if (!Number.isFinite(pollInterval) || pollInterval < 500) {
      throw new Error("Dashboard polling interval is invalid");
    }
    this.client = client;
    this.pollInterval = pollInterval;
    this.sessionKey = nextDashboardSessionKey++;
    this.queryClient = new QueryClient({
      defaultOptions: {
        mutations: { networkMode: "always", retry: false },
        queries: {
          networkMode: "always",
          refetchOnReconnect: false,
          refetchOnWindowFocus: true,
          retry: false,
          staleTime: pollInterval,
        },
      },
    });
  }

  async dispose(): Promise<void> {
    await this.queryClient.cancelQueries();
    this.queryClient.clear();
  }
}

export function controlErrorKey(error: unknown): string {
  if (error instanceof ControlProblem || error instanceof ControlContractError) {
    return error.messageKey;
  }
  if (error instanceof DOMException && error.name === "AbortError") {
    return "error.request_canceled";
  }
  return "error.runtime_unavailable";
}

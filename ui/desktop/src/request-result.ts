import type { ActivityStatus } from "./control-types.ts";

const reasonKeys = {
  unsupported_client_input: "requests.reason.unsupported_client_input",
  tool_decision_expired: "requests.reason.tool_decision_expired",
  tool_decision_rejected: "requests.reason.tool_decision_rejected",
  tool_decision_unavailable: "requests.reason.tool_decision_unavailable",
} as const;

export function requestReasonKey(value: string | undefined): string | undefined {
  if (value !== undefined && value in reasonKeys) {
    return reasonKeys[value as keyof typeof reasonKeys];
  }
  return undefined;
}

export function requestResultKey(
  value: string | undefined,
  fallback: ActivityStatus,
): string {
  return requestReasonKey(value) ?? `requests.state.${fallback}`;
}

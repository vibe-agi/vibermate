export type BootstrapFailureMessageKey =
  | "app.bootstrap.failed"
  | "app.bootstrap.failure.runtime_unavailable"
  | "app.bootstrap.failure.runtime_already_active"
  | "app.bootstrap.failure.secret_store_unavailable"
  | "app.bootstrap.failure.storage_schema_newer"
  | "app.bootstrap.failure.storage_unavailable";

export type DesktopRuntimeFailureReason = "daemon_exited";
export type DesktopRuntimeFailureMessageKey =
  "app.runtime.failure.daemon_exited";
export type DesktopFailureMessageKey =
  | BootstrapFailureMessageKey
  | DesktopRuntimeFailureMessageKey;

export interface DesktopRuntimeFailure {
  readonly reason: DesktopRuntimeFailureReason;
}

export class DesktopBootstrapProblem extends Error {
  readonly messageKey: BootstrapFailureMessageKey;

  constructor(messageKey: BootstrapFailureMessageKey) {
    super("Desktop bootstrap failed at a classified local boundary");
    this.name = "DesktopBootstrapProblem";
    this.messageKey = messageKey;
  }
}

export class DesktopRuntimeProblem extends Error {
  readonly messageKey: DesktopRuntimeFailureMessageKey;
  readonly reason: DesktopRuntimeFailureReason;

  constructor(reason: DesktopRuntimeFailureReason) {
    super("Desktop runtime stopped at a classified local boundary");
    this.name = "DesktopRuntimeProblem";
    this.reason = reason;
    this.messageKey = "app.runtime.failure.daemon_exited";
  }
}

export function bootstrapFailureMessageKey(
  error: unknown,
): DesktopFailureMessageKey {
  return error instanceof DesktopBootstrapProblem ||
    error instanceof DesktopRuntimeProblem
    ? error.messageKey
    : "app.bootstrap.failed";
}

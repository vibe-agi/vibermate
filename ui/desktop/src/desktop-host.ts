import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import {
  DesktopBootstrapProblem,
  DesktopRuntimeProblem,
  type BootstrapFailureMessageKey,
  type DesktopRuntimeFailure,
} from "./bootstrap-problem.ts";
import {
  createControlClient,
  type ControlClient,
  type DesktopSession,
} from "./control-client.ts";
import {
  navigationStateSchema,
  type PersistedNavigationState,
  validNavigationLocator,
} from "./navigation-state.ts";

let nativeSessionPayload: Promise<unknown> | undefined;
let nativeRuntimeListener: Promise<UnlistenFn> | undefined;
let nativeRuntimeUnlisten: UnlistenFn | undefined;
let nativeRuntimeExitRevision = 0;
const runtimeFailureSubscribers = new Set<
  (failure: DesktopRuntimeFailure) => void
>();
let lastQueuedNavigation: string | undefined;
let navigationWriteTail: Promise<void> = Promise.resolve();
const navigationRestoreTimeoutMilliseconds = 1_000;
const navigationRestoreTimedOut = Symbol("navigation-restore-timed-out");
// Native startup first requires a five-second typed progress frame, then grants
// storage/runtime initialization up to two minutes, followed by the bounded
// loopback exchange. Leave scheduling margin without outliving a stuck host
// command indefinitely.
const nativeSessionTimeoutMilliseconds = 130_000;
const desktopRuntimeEventName = "vibermate-desktop-runtime";
const desktopRuntimeEventSchema = "vibermate-desktop-runtime-event-v1";
const terminalCommandSchema = "vibermate-terminal-command/v1";

export type TerminalCommandState =
  | "not_installed"
  | "current"
  | "source_updated"
  | "source_missing"
  | "target_missing"
  | "unowned_target"
  | "conflict";

export interface TerminalCommandStatus {
  readonly schema: typeof terminalCommandSchema;
  readonly state: TerminalCommandState;
  readonly sourcePath: string;
  readonly targetPath: string;
  readonly detail?: string;
}

export async function connectDesktopControl(): Promise<ControlClient> {
  await ensureDesktopRuntimeListener();
  const exitRevision = nativeRuntimeExitRevision;
  let payload: unknown;
  try {
    payload = await waitForNativeSession(takeNativeSessionPayload());
  } catch (error) {
    const problem = classifiedNativeStartupProblem(error);
    if (problem !== undefined) {
      throw problem;
    }
    throw error;
  }
  const client = await createControlClient(decodeSession(payload));
  if (exitRevision !== nativeRuntimeExitRevision) {
    client.close();
    throw new DesktopRuntimeProblem("daemon_exited");
  }
  return client;
}

export function observeDesktopRuntimeFailure(
  subscriber: (failure: DesktopRuntimeFailure) => void,
): () => void {
  runtimeFailureSubscribers.add(subscriber);
  return () => runtimeFailureSubscribers.delete(subscriber);
}

export function disposeDesktopRuntimeObservation(): void {
  runtimeFailureSubscribers.clear();
  const unlisten = nativeRuntimeUnlisten;
  nativeRuntimeListener = undefined;
  nativeRuntimeUnlisten = undefined;
  try {
    unlisten?.();
  } catch {
    // Page teardown must not surface host bridge details or block Webview exit.
  }
}

async function ensureDesktopRuntimeListener(): Promise<void> {
  if (nativeRuntimeListener !== undefined) {
    await nativeRuntimeListener;
    return;
  }
  const attempt = listen<unknown>(desktopRuntimeEventName, ({ payload }) => {
    if (!validDesktopRuntimeEvent(payload)) {
      return;
    }
    nativeRuntimeExitRevision += 1;
    nativeSessionPayload = undefined;
    const failure: DesktopRuntimeFailure = { reason: "daemon_exited" };
    for (const subscriber of [...runtimeFailureSubscribers]) {
      try {
        subscriber(failure);
      } catch {
        // One view callback cannot suppress the process-lifecycle boundary.
      }
    }
  });
  nativeRuntimeListener = attempt;
  void attempt.then(
    (unlisten) => {
      if (nativeRuntimeListener === attempt) {
        nativeRuntimeUnlisten = unlisten;
      } else {
        try {
          unlisten();
        } catch {
          // The Webview is already tearing down.
        }
      }
    },
    () => {
      if (nativeRuntimeListener === attempt) {
        nativeRuntimeListener = undefined;
      }
    },
  );
  await attempt;
}

function validDesktopRuntimeEvent(payload: unknown): boolean {
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) {
    return false;
  }
  const candidate = payload as Record<string, unknown>;
  return (
    Object.keys(candidate).length === 2 &&
    candidate.schema === desktopRuntimeEventSchema &&
    candidate.reason === "daemon_exited"
  );
}

const nativeStartupMessageKeys: Readonly<
  Record<string, BootstrapFailureMessageKey>
> = {
  "Desktop runtime could not be started":
    "app.bootstrap.failure.runtime_unavailable",
  "Desktop secret storage is unavailable":
    "app.bootstrap.failure.secret_store_unavailable",
  "Desktop storage schema requires a newer app":
    "app.bootstrap.failure.storage_schema_newer",
  "Desktop storage could not be opened":
    "app.bootstrap.failure.storage_unavailable",
};

function classifiedNativeStartupProblem(
  error: unknown,
): DesktopBootstrapProblem | undefined {
  const message =
    typeof error === "string"
      ? error
      : error instanceof Error
        ? error.message
        : undefined;
  if (message === undefined) {
    return undefined;
  }
  const messageKey = nativeStartupMessageKeys[message];
  return messageKey === undefined
    ? undefined
    : new DesktopBootstrapProblem(messageKey);
}

export async function restoreDesktopNavigation(): Promise<boolean> {
  if (location.hash.length > 1) {
    return false;
  }
  let timeout: ReturnType<typeof globalThis.setTimeout> | undefined;
  try {
    const payload = await Promise.race([
      invoke<unknown>("load_navigation_state"),
      new Promise<typeof navigationRestoreTimedOut>((resolve) => {
        timeout = globalThis.setTimeout(
          () => resolve(navigationRestoreTimedOut),
          navigationRestoreTimeoutMilliseconds,
        );
      }),
    ]);
    if (payload === navigationRestoreTimedOut) {
      return false;
    }
    const navigation = decodeNavigationState(payload);
    if (navigation === undefined) {
      return false;
    }
    history.replaceState(
      history.state,
      "",
      `${location.pathname}${location.search}#${navigation.locator}`,
    );
    return true;
  } catch {
    return false;
  } finally {
    if (timeout !== undefined) {
      globalThis.clearTimeout(timeout);
    }
  }
}

export function persistDesktopNavigation(locator: string): Promise<void> {
  if (!validNavigationLocator(locator) || locator === lastQueuedNavigation) {
    return navigationWriteTail;
  }
  lastQueuedNavigation = locator;
  const attempt = navigationWriteTail
    .catch(() => undefined)
    .then(() =>
      invoke<void>("save_navigation_state", {
        navigationState: {
          schema: navigationStateSchema,
          locator,
        } satisfies PersistedNavigationState,
      }),
    );
  navigationWriteTail = attempt.then(
    () => undefined,
    () => {
      if (lastQueuedNavigation === locator) {
        lastQueuedNavigation = undefined;
      }
    },
  );
  return attempt;
}

export async function inspectTerminalCommand(): Promise<TerminalCommandStatus> {
  return decodeTerminalCommandStatus(
    await invoke<unknown>("inspect_terminal_command"),
  );
}

export async function installTerminalCommand(): Promise<TerminalCommandStatus> {
  return decodeTerminalCommandStatus(
    await invoke<unknown>("install_terminal_command"),
  );
}

export async function refreshTerminalCommand(): Promise<TerminalCommandStatus> {
  return decodeTerminalCommandStatus(
    await invoke<unknown>("refresh_terminal_command"),
  );
}

export async function removeTerminalCommand(): Promise<TerminalCommandStatus> {
  return decodeTerminalCommandStatus(
    await invoke<unknown>("remove_terminal_command"),
  );
}

function takeNativeSessionPayload(): Promise<unknown> {
  if (nativeSessionPayload !== undefined) {
    return nativeSessionPayload;
  }
  const attempt = invoke<unknown>("take_control_session");
  nativeSessionPayload = attempt;
  // A rejected native command did not hand a session to this WebView and may
  // be retried. Once a payload is delivered it stays process-local so a later
  // control-API inspection failure does not consume the one-shot command again.
  void attempt.catch(() => {
    if (nativeSessionPayload === attempt) {
      nativeSessionPayload = undefined;
    }
  });
  return attempt;
}

function waitForNativeSession(payload: Promise<unknown>): Promise<unknown> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const timeout = globalThis.setTimeout(() => {
      if (!settled) {
        settled = true;
        reject(
          new DOMException(
            "Desktop shell control session delivery timed out",
            "TimeoutError",
          ),
        );
      }
    }, nativeSessionTimeoutMilliseconds);
    const settle = (complete: (value: unknown) => void, value: unknown) => {
      if (!settled) {
        settled = true;
        globalThis.clearTimeout(timeout);
        complete(value);
      }
    };
    void payload.then(
      (value) => settle(resolve, value),
      (error: unknown) => settle(reject, error),
    );
  });
}

function decodeSession(payload: unknown): DesktopSession {
  if (payload === null || typeof payload !== "object") {
    throw new Error("Desktop shell returned an invalid control session");
  }
  const candidate = payload as Record<string, unknown>;
  const fields = [
    "schema",
    "baseUrl",
    "readToken",
    "writeToken",
    "instanceId",
    "expiresAt",
  ] as const;
  if (Object.keys(candidate).length !== fields.length) {
    throw new Error("Desktop shell returned an invalid control session");
  }
  for (const field of fields) {
    if (typeof candidate[field] !== "string") {
      throw new Error("Desktop shell returned an incomplete control session");
    }
  }
  return candidate as unknown as DesktopSession;
}

function decodeNavigationState(
  payload: unknown,
): PersistedNavigationState | undefined {
  if (payload === null) {
    return undefined;
  }
  if (typeof payload !== "object") {
    throw new Error("Desktop shell returned invalid navigation state");
  }
  const candidate = payload as Record<string, unknown>;
  if (
    Object.keys(candidate).length !== 2 ||
    candidate.schema !== navigationStateSchema ||
    typeof candidate.locator !== "string" ||
    !validNavigationLocator(candidate.locator)
  ) {
    throw new Error("Desktop shell returned invalid navigation state");
  }
  return candidate as unknown as PersistedNavigationState;
}

function decodeTerminalCommandStatus(payload: unknown): TerminalCommandStatus {
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) {
    throw new Error("Desktop shell returned invalid terminal command status");
  }
  const candidate = payload as Record<string, unknown>;
  const required = ["schema", "state", "sourcePath", "targetPath"] as const;
  const allowed = new Set([...required, "detail"]);
  if (
    Object.keys(candidate).some((field) => !allowed.has(field)) ||
    Object.keys(candidate).length < required.length ||
    required.some((field) => typeof candidate[field] !== "string") ||
    candidate.schema !== terminalCommandSchema ||
    !validTerminalCommandState(candidate.state) ||
    !validAbsoluteTerminalPath(candidate.sourcePath) ||
    !validAbsoluteTerminalPath(candidate.targetPath) ||
    (candidate.detail !== undefined &&
      (typeof candidate.detail !== "string" || candidate.detail.length > 4096))
  ) {
    throw new Error("Desktop shell returned invalid terminal command status");
  }
  return candidate as unknown as TerminalCommandStatus;
}

function validTerminalCommandState(value: unknown): value is TerminalCommandState {
  return [
    "not_installed",
    "current",
    "source_updated",
    "source_missing",
    "target_missing",
    "unowned_target",
    "conflict",
  ].includes(String(value));
}

function validAbsoluteTerminalPath(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.startsWith("/") &&
    value.length <= 4096 &&
    !/[\u0000\r\n]/u.test(value)
  );
}

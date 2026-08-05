import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tauri = vi.hoisted(() => ({
  invoke: vi.fn(),
}));
const tauriEvent = vi.hoisted(() => ({
  listen: vi.fn(),
}));

vi.mock("@tauri-apps/api/core", () => tauri);
vi.mock("@tauri-apps/api/event", () => tauriEvent);

let connectDesktopControl: typeof import("../src/desktop-host.ts").connectDesktopControl;
let disposeDesktopRuntimeObservation: typeof import("../src/desktop-host.ts").disposeDesktopRuntimeObservation;
let observeDesktopRuntimeFailure: typeof import("../src/desktop-host.ts").observeDesktopRuntimeFailure;
let persistDesktopNavigation: typeof import("../src/desktop-host.ts").persistDesktopNavigation;
let restoreDesktopNavigation: typeof import("../src/desktop-host.ts").restoreDesktopNavigation;
let inspectTerminalCommand: typeof import("../src/desktop-host.ts").inspectTerminalCommand;
let installTerminalCommand: typeof import("../src/desktop-host.ts").installTerminalCommand;
let refreshTerminalCommand: typeof import("../src/desktop-host.ts").refreshTerminalCommand;
let removeTerminalCommand: typeof import("../src/desktop-host.ts").removeTerminalCommand;
let runtimeEventHandler:
  | ((event: { readonly payload: unknown }) => void)
  | undefined;
let runtimeUnlisten: ReturnType<typeof vi.fn>;

function capability(fill: number): string {
  const bytes = new Uint8Array(32).fill(fill);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/u, "");
}

const bootstrap = {
  schema: "vibermate-app-session-v1",
  baseUrl: "http://127.0.0.1:43127",
  readToken: capability(0x51),
  writeToken: capability(0x52),
  instanceId: "runtime-instance",
  expiresAt: "2099-07-30T00:00:00Z",
} as const;

beforeEach(async () => {
  vi.resetModules();
  runtimeEventHandler = undefined;
  runtimeUnlisten = vi.fn();
  tauriEvent.listen.mockImplementation(
    async (
      eventName: string,
      handler: (event: { readonly payload: unknown }) => void,
    ) => {
      expect(eventName).toBe("vibermate-desktop-runtime");
      runtimeEventHandler = handler;
      return runtimeUnlisten;
    },
  );
  ({
    connectDesktopControl,
    disposeDesktopRuntimeObservation,
    observeDesktopRuntimeFailure,
    persistDesktopNavigation,
    restoreDesktopNavigation,
    inspectTerminalCommand,
    installTerminalCommand,
    refreshTerminalCommand,
    removeTerminalCommand,
  } = await import("../src/desktop-host.ts"));
  history.replaceState(null, "", "/");
});

afterEach(() => {
  disposeDesktopRuntimeObservation();
  vi.useRealTimers();
  vi.restoreAllMocks();
  tauri.invoke.mockReset();
  tauriEvent.listen.mockReset();
});

describe("Desktop host connection", () => {
  it("does not publish a ControlClient until current session metadata is read", async () => {
    tauri.invoke.mockResolvedValue(bootstrap);
    const fetchImplementation = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async (input, init) => {
        expect(new URL(String(input)).pathname).toBe(
          "/api/v1/auth/sessions/current",
        );
        expect(new Headers(init?.headers).get("Authorization")).toBe(
          `Bearer ${bootstrap.readToken}`,
        );
        return new Response(
          JSON.stringify({
            schema: "vibermate-app-session-state-v1",
            revision: 1,
            expiresAt: bootstrap.expiresAt,
          }),
          { headers: { "Content-Type": "application/json" } },
        );
      });

    const client = await connectDesktopControl();

    expect(client).toEqual(
      expect.objectContaining({ status: expect.any(Function) }),
    );
    expect(tauriEvent.listen).toHaveBeenCalledOnce();
    expect(tauri.invoke).toHaveBeenCalledWith("take_control_session");
    expect(tauriEvent.listen.mock.invocationCallOrder[0]).toBeLessThan(
      tauri.invoke.mock.invocationCallOrder[0] ?? Number.MAX_SAFE_INTEGER,
    );
    expect(fetchImplementation).toHaveBeenCalledOnce();
  });

  it("closes the session generation on a closed native runtime event and reconnects fresh", async () => {
    const replacement = {
      ...bootstrap,
      instanceId: "replacement-runtime-instance",
      readToken: capability(0x61),
      writeToken: capability(0x62),
    };
    tauri.invoke
      .mockResolvedValueOnce(bootstrap)
      .mockResolvedValueOnce(replacement);
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_input, init) => {
      const authorization = new Headers(init?.headers).get("Authorization");
      const expiresAt = authorization?.includes(replacement.readToken)
        ? replacement.expiresAt
        : bootstrap.expiresAt;
      return new Response(
        JSON.stringify({
          schema: "vibermate-app-session-state-v1",
          revision: 1,
          expiresAt,
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    });
    const observed = vi.fn();
    const unsubscribe = observeDesktopRuntimeFailure(observed);

    const first = await connectDesktopControl();
    runtimeEventHandler?.({
      payload: {
        schema: "vibermate-desktop-runtime-event-v1",
        reason: "daemon_exited",
      },
    });

    expect(observed).toHaveBeenCalledOnce();
    expect(observed).toHaveBeenCalledWith({ reason: "daemon_exited" });
    first.close();
    await expect(first.status()).rejects.toMatchObject({ name: "AbortError" });
    await expect(connectDesktopControl()).resolves.toEqual(
      expect.objectContaining({ close: expect.any(Function) }),
    );
    expect(tauri.invoke).toHaveBeenCalledTimes(2);
    expect(tauriEvent.listen).toHaveBeenCalledOnce();
    unsubscribe();
  });

  it("ignores open-ended runtime event payloads and cleans up view observation", async () => {
    tauri.invoke.mockResolvedValue(bootstrap);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          schema: "vibermate-app-session-state-v1",
          revision: 1,
          expiresAt: bootstrap.expiresAt,
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );
    const observed = vi.fn();
    const unsubscribe = observeDesktopRuntimeFailure(observed);
    await connectDesktopControl();

    runtimeEventHandler?.({
      payload: {
        schema: "vibermate-desktop-runtime-event-v1",
        reason: "daemon_exited",
        stderr: "token=must-not-cross-the-boundary",
      },
    });
    expect(observed).not.toHaveBeenCalled();

    unsubscribe();
    runtimeEventHandler?.({
      payload: {
        schema: "vibermate-desktop-runtime-event-v1",
        reason: "daemon_exited",
      },
    });
    expect(observed).not.toHaveBeenCalled();

    disposeDesktopRuntimeObservation();
    expect(runtimeUnlisten).toHaveBeenCalledOnce();
  });

  it("rejects extra native bootstrap fields before making a network request", async () => {
    tauri.invoke.mockResolvedValue({
      ...bootstrap,
      persistedToken: "forbidden",
    });
    const fetchImplementation = vi.spyOn(globalThis, "fetch");

    await expect(connectDesktopControl()).rejects.toThrow(
      /invalid control session/u,
    );

    expect(fetchImplementation).not.toHaveBeenCalled();
  });

  it("reuses the delivered native session when control inspection is retried", async () => {
    tauri.invoke.mockResolvedValue(bootstrap);
    const fetchImplementation = vi
      .spyOn(globalThis, "fetch")
      .mockRejectedValueOnce(new TypeError("loopback temporarily unavailable"))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            schema: "vibermate-app-session-state-v1",
            revision: 1,
            expiresAt: bootstrap.expiresAt,
          }),
          { headers: { "Content-Type": "application/json" } },
        ),
      );

    await expect(connectDesktopControl()).rejects.toThrow(
      "loopback temporarily unavailable",
    );
    const client = await connectDesktopControl();

    expect(client).toEqual(
      expect.objectContaining({ status: expect.any(Function) }),
    );
    expect(tauri.invoke).toHaveBeenCalledTimes(1);
    expect(tauri.invoke).toHaveBeenCalledWith("take_control_session");
    expect(fetchImplementation).toHaveBeenCalledTimes(2);
  });

  it("allows retry when the native command itself did not deliver a session", async () => {
    tauri.invoke
      .mockRejectedValueOnce(new Error("native bridge temporarily unavailable"))
      .mockResolvedValueOnce(bootstrap);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          schema: "vibermate-app-session-state-v1",
          revision: 1,
          expiresAt: bootstrap.expiresAt,
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(connectDesktopControl()).rejects.toThrow(
      "native bridge temporarily unavailable",
    );
    await expect(connectDesktopControl()).resolves.toEqual(
      expect.objectContaining({ status: expect.any(Function) }),
    );

    expect(tauri.invoke).toHaveBeenCalledTimes(2);
  });

  it("maps only closed native startup diagnoses to localized problem keys", async () => {
    tauri.invoke.mockRejectedValue(
      "Desktop storage schema requires a newer app",
    );

    await expect(connectDesktopControl()).rejects.toMatchObject({
      name: "DesktopBootstrapProblem",
      messageKey: "app.bootstrap.failure.storage_schema_newer",
    });
  });

  it("classifies an already active runtime without exposing native detail", async () => {
    tauri.invoke.mockRejectedValue("Desktop runtime is already active");

    await expect(connectDesktopControl()).rejects.toMatchObject({
      name: "DesktopBootstrapProblem",
      messageKey: "app.bootstrap.failure.runtime_already_active",
    });
  });

  it("times out one native wait but reuses its late one-shot delivery", async () => {
    vi.useFakeTimers();
    let deliverSession: ((payload: unknown) => void) | undefined;
    tauri.invoke.mockReturnValue(
      new Promise((resolve) => {
        deliverSession = resolve;
      }),
    );
    const fetchImplementation = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          schema: "vibermate-app-session-state-v1",
          revision: 1,
          expiresAt: bootstrap.expiresAt,
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );

    const first = connectDesktopControl();
    const rejected = expect(first).rejects.toEqual(
      expect.objectContaining({ name: "TimeoutError" }),
    );
    await vi.advanceTimersByTimeAsync(130_000);
    await rejected;
    expect(tauri.invoke).toHaveBeenCalledTimes(1);
    expect(fetchImplementation).not.toHaveBeenCalled();

    deliverSession?.(bootstrap);
    await expect(connectDesktopControl()).resolves.toEqual(
      expect.objectContaining({ status: expect.any(Function) }),
    );

    expect(tauri.invoke).toHaveBeenCalledTimes(1);
    expect(fetchImplementation).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("allows the native two-phase startup deadline to complete", async () => {
    vi.useFakeTimers();
    let deliverSession: ((payload: unknown) => void) | undefined;
    tauri.invoke.mockReturnValue(
      new Promise((resolve) => {
        deliverSession = resolve;
      }),
    );
    const fetchImplementation = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          schema: "vibermate-app-session-state-v1",
          revision: 1,
          expiresAt: bootstrap.expiresAt,
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );

    const connection = connectDesktopControl();
    await vi.advanceTimersByTimeAsync(125_000);
    expect(fetchImplementation).not.toHaveBeenCalled();
    deliverSession?.(bootstrap);

    await expect(connection).resolves.toEqual(
      expect.objectContaining({ status: expect.any(Function) }),
    );
    expect(tauri.invoke).toHaveBeenCalledTimes(1);
    expect(fetchImplementation).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("Managed terminal command bridge", () => {
  const status = {
    schema: "vibermate-terminal-command/v1",
    state: "current",
    sourcePath: "/Applications/ViberMate.app/Contents/MacOS/vibermate",
    targetPath: "/Users/example/.local/bin/vibermate",
  } as const;

  it("exposes only four fixed native operations with a closed status", async () => {
    tauri.invoke.mockResolvedValue(status);

    await expect(inspectTerminalCommand()).resolves.toEqual(status);
    await expect(installTerminalCommand()).resolves.toEqual(status);
    await expect(refreshTerminalCommand()).resolves.toEqual(status);
    await expect(removeTerminalCommand()).resolves.toEqual(status);

    expect(tauri.invoke).toHaveBeenNthCalledWith(1, "inspect_terminal_command");
    expect(tauri.invoke).toHaveBeenNthCalledWith(2, "install_terminal_command");
    expect(tauri.invoke).toHaveBeenNthCalledWith(3, "refresh_terminal_command");
    expect(tauri.invoke).toHaveBeenNthCalledWith(4, "remove_terminal_command");
  });

  it.each([
    { ...status, sourcePath: "relative/vibermate" },
    { ...status, state: "trusted" },
    { ...status, receiptPath: "/private/forbidden" },
    { ...status, detail: "x".repeat(4097) },
  ])("rejects an unsafe native terminal status", async (unsafeStatus) => {
    tauri.invoke.mockResolvedValue(unsafeStatus);

    await expect(inspectTerminalCommand()).rejects.toThrow(
      /invalid terminal command status/u,
    );
  });
});

describe("Desktop navigation host", () => {
  it("lets an explicit launch locator override the saved location", async () => {
    history.replaceState(null, "", "/#access/claude/routing");

    await expect(restoreDesktopNavigation()).resolves.toBe(false);

    expect(tauri.invoke).not.toHaveBeenCalled();
    expect(location.hash).toBe("#access/claude/routing");
  });

  it("restores a strict canonical locator without changing outer search", async () => {
    history.replaceState({ host: "state" }, "", "/?shell=1");
    tauri.invoke.mockResolvedValue({
      schema: "vibermate-navigation-state-v1",
      locator: "policies/approvals?selected=approval-network-sample",
    });

    await expect(restoreDesktopNavigation()).resolves.toBe(true);

    expect(tauri.invoke).toHaveBeenCalledWith("load_navigation_state");
    expect(location.search).toBe("?shell=1");
    expect(location.hash).toBe(
      "#policies/approvals?selected=approval-network-sample",
    );
    expect(history.state).toEqual({ host: "state" });
  });

  it("writes through the restored locator once before deduplicating it", async () => {
    const locator = "settings/recovery";
    tauri.invoke
      .mockResolvedValueOnce({
        schema: "vibermate-navigation-state-v1",
        locator,
      })
      .mockResolvedValue(undefined);

    await expect(restoreDesktopNavigation()).resolves.toBe(true);
    await persistDesktopNavigation(locator);
    await persistDesktopNavigation(locator);

    expect(tauri.invoke).toHaveBeenCalledTimes(2);
    expect(tauri.invoke).toHaveBeenNthCalledWith(1, "load_navigation_state");
    expect(tauri.invoke).toHaveBeenNthCalledWith(2, "save_navigation_state", {
      navigationState: {
        schema: "vibermate-navigation-state-v1",
        locator,
      },
    });
  });

  it("ignores missing, malformed, or non-canonical saved state", async () => {
    for (const payload of [
      null,
      {
        schema: "vibermate-navigation-state-v1",
        locator: "policy",
      },
      {
        schema: "vibermate-navigation-state-v1",
        locator: "overview",
        token: "must-not-pass",
      },
    ]) {
      vi.resetModules();
      ({ restoreDesktopNavigation } = await import("../src/desktop-host.ts"));
      history.replaceState(null, "", "/");
      tauri.invoke.mockResolvedValueOnce(payload);

      await expect(restoreDesktopNavigation()).resolves.toBe(false);
      expect(location.hash).toBe("");
    }
  });

  it("bounds a stalled restore and ignores its late result", async () => {
    vi.useFakeTimers();
    let resolveLoad: ((payload: unknown) => void) | undefined;
    tauri.invoke.mockReturnValue(
      new Promise((resolve) => {
        resolveLoad = resolve;
      }),
    );

    const restored = restoreDesktopNavigation();
    await vi.advanceTimersByTimeAsync(1_000);

    await expect(restored).resolves.toBe(false);
    expect(location.hash).toBe("");

    resolveLoad?.({
      schema: "vibermate-navigation-state-v1",
      locator: "settings/recovery",
    });
    await vi.runAllTimersAsync();
    expect(location.hash).toBe("");
  });

  it("serializes, validates, and deduplicates navigation writes", async () => {
    tauri.invoke.mockResolvedValue(undefined);

    await Promise.all([
      persistDesktopNavigation("access/claude/routing"),
      persistDesktopNavigation("access/claude/routing"),
    ]);
    await persistDesktopNavigation("policy");
    await persistDesktopNavigation("settings/recovery");

    expect(tauri.invoke).toHaveBeenCalledTimes(2);
    expect(tauri.invoke).toHaveBeenNthCalledWith(1, "save_navigation_state", {
      navigationState: {
        schema: "vibermate-navigation-state-v1",
        locator: "access/claude/routing",
      },
    });
    expect(tauri.invoke).toHaveBeenNthCalledWith(2, "save_navigation_state", {
      navigationState: {
        schema: "vibermate-navigation-state-v1",
        locator: "settings/recovery",
      },
    });
  });

  it("can retry a navigation write that the native host rejected", async () => {
    tauri.invoke
      .mockRejectedValueOnce(new Error("navigation store unavailable"))
      .mockResolvedValueOnce(undefined);

    await expect(
      persistDesktopNavigation("extensions/discover"),
    ).rejects.toThrow("navigation store unavailable");
    await expect(
      persistDesktopNavigation("extensions/discover"),
    ).resolves.toBeUndefined();

    expect(tauri.invoke).toHaveBeenCalledTimes(2);
  });

  it("does not let a pending route overwrite a quick return to the current route", async () => {
    let finishPending: (() => void) | undefined;
    tauri.invoke
      .mockResolvedValueOnce(undefined)
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            finishPending = resolve;
          }),
      )
      .mockResolvedValueOnce(undefined);
    await persistDesktopNavigation("overview");

    const pending = persistDesktopNavigation("activity");
    await vi.waitFor(() => expect(tauri.invoke).toHaveBeenCalledTimes(2));
    const returned = persistDesktopNavigation("overview");
    finishPending?.();
    await Promise.all([pending, returned]);

    expect(tauri.invoke).toHaveBeenCalledTimes(3);
    expect(tauri.invoke).toHaveBeenLastCalledWith("save_navigation_state", {
      navigationState: {
        schema: "vibermate-navigation-state-v1",
        locator: "overview",
      },
    });
  });
});

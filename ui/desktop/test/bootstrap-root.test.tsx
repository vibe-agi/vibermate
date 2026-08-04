import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import {
  BootstrapRoot,
  DesktopRenderErrorBoundary,
} from "../src/bootstrap-root.tsx";
import {
  DesktopBootstrapProblem,
  type DesktopRuntimeFailure,
} from "../src/bootstrap-problem.ts";
import type { ControlClient } from "../src/control-client.ts";
import { createI18n } from "../src/i18n.ts";
import { connectPreviewControl } from "../src/preview-control.ts";
import { createDesktopRootErrorOptions } from "../src/startup-failure.ts";

async function renderBootstrap(connect: () => Promise<ControlClient>) {
  const i18n = await createI18n("en-US");
  location.hash = "#overview";
  return render(
    <StrictMode>
      <I18nextProvider i18n={i18n}>
        <BootstrapRoot connect={connect} preview />
      </I18nextProvider>
    </StrictMode>,
  );
}

describe("BootstrapRoot", () => {
  it("consumes a one-shot desktop connection only once under StrictMode", async () => {
    const client = await connectPreviewControl();
    const close = vi.spyOn(client, "close");
    const connect = vi.fn(async () => client);

    await renderBootstrap(connect);

    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
    expect(connect).toHaveBeenCalledTimes(1);
    expect(close).not.toHaveBeenCalled();
  });

  it("closes the claimed control capability on a real root unmount", async () => {
    const client = await connectPreviewControl();
    const close = vi.spyOn(client, "close");
    const connect = vi.fn(async () => client);

    const view = await renderBootstrap(connect);
    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
    expect(close).not.toHaveBeenCalled();

    view.unmount();

    await waitFor(() => expect(close).toHaveBeenCalledOnce());
  });

  it("starts exactly one new connection attempt when retrying", async () => {
    const client = await connectPreviewControl();
    const connect = vi
      .fn<() => Promise<ControlClient>>()
      .mockRejectedValueOnce(new Error("desktop bootstrap failed"))
      .mockResolvedValueOnce(client);

    await renderBootstrap(connect);
    const retry = await screen.findByRole("button", {
      name: "Try again",
    });
    expect(screen.getByRole("alert").contains(retry)).toBe(true);
    expect(document.activeElement).toBe(retry);
    fireEvent.click(retry);

    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
    await waitFor(() => expect(connect).toHaveBeenCalledTimes(2));
  });

  it("shows a localized closed diagnosis without reflecting native details", async () => {
    const connect = vi.fn<() => Promise<ControlClient>>().mockRejectedValue(
      new DesktopBootstrapProblem(
        "app.bootstrap.failure.storage_schema_newer",
      ),
    );

    await renderBootstrap(connect);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("created by a newer VibeMate version");
    expect(alert.textContent).not.toContain("database");
    expect(
      (screen.getByRole("button", { name: "Try again" }) as HTMLButtonElement)
        .disabled,
    ).toBe(false);
  });

  it("announces the pending desktop connection as busy", async () => {
    const connect = vi.fn(
      () => new Promise<ControlClient>(() => undefined),
    );

    await renderBootstrap(connect);

    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-busy")).toBe("true");
    expect(status.textContent).toContain("Starting the local runtime");
  });

  it("does not replace an established desktop session when props rerender", async () => {
    const i18n = await createI18n("en-US");
    const firstConnect = vi.fn(async () => connectPreviewControl());
    const replacementConnect = vi.fn(async () => connectPreviewControl());
    location.hash = "#overview";
    const { rerender } = render(
      <StrictMode>
        <I18nextProvider i18n={i18n}>
          <BootstrapRoot connect={firstConnect} preview />
        </I18nextProvider>
      </StrictMode>,
    );
    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);

    rerender(
      <StrictMode>
        <I18nextProvider i18n={i18n}>
          <BootstrapRoot connect={replacementConnect} preview />
        </I18nextProvider>
      </StrictMode>,
    );

    await waitFor(() => expect(firstConnect).toHaveBeenCalledTimes(1));
    expect(replacementConnect).not.toHaveBeenCalled();
  });

  it("closes a crashed session and waits for an explicit localized restart", async () => {
    const i18n = await createI18n("en-US");
    const firstClient = await connectPreviewControl();
    const secondClient = await connectPreviewControl();
    const closeFirst = vi.spyOn(firstClient, "close");
    const connect = vi
      .fn<() => Promise<ControlClient>>()
      .mockResolvedValueOnce(firstClient)
      .mockResolvedValueOnce(secondClient);
    const subscribers = new Set<
      (failure: DesktopRuntimeFailure) => void
    >();
    const observeRuntimeFailure = vi.fn(
      (subscriber: (failure: DesktopRuntimeFailure) => void) => {
        subscribers.add(subscriber);
        return () => subscribers.delete(subscriber);
      },
    );
    location.hash = "#overview";
    const { unmount } = render(
      <StrictMode>
        <I18nextProvider i18n={i18n}>
          <BootstrapRoot
            connect={connect}
            observeRuntimeFailure={observeRuntimeFailure}
            preview
          />
        </I18nextProvider>
      </StrictMode>,
    );
    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);

    act(() => {
      for (const subscriber of subscribers) {
        subscriber({ reason: "daemon_exited" });
      }
    });

    const restart = await screen.findByRole("button", {
      name: "Restart local runtime",
    });
    expect(screen.getByRole("alert").textContent).toContain(
      "previous control session was closed",
    );
    expect(closeFirst).toHaveBeenCalled();
    expect(connect).toHaveBeenCalledTimes(1);

    fireEvent.click(restart);
    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
    await waitFor(() => expect(connect).toHaveBeenCalledTimes(2));

    unmount();
    expect(subscribers.size).toBe(0);
  });
});

describe("DesktopRenderErrorBoundary", () => {
  it("shows localized safe copy and a keyboard-usable reload action", async () => {
    const sensitiveFailure = "token=private-value should never be shown";
    const i18n = await createI18n("zh-CN");
    const reload = vi.fn();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const rootOptions = createDesktopRootErrorOptions(
      document.createElement("div"),
      vi.fn(),
    );

    function BrokenDashboard(): never {
      throw new Error(sensitiveFailure);
    }

    render(
      <DesktopRenderErrorBoundary
        actionLabel={i18n.t("app.bootstrap.retry")}
        message={i18n.t("app.bootstrap.failed")}
        onReload={reload}
      >
        <BrokenDashboard />
      </DesktopRenderErrorBoundary>,
      {
        onCaughtError: rootOptions.onCaughtError,
        onRecoverableError: rootOptions.onRecoverableError,
      },
    );

    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("无法启动本机桌面运行时。");
    expect(alert.textContent).not.toContain(sensitiveFailure);
    const action = screen.getByRole("button", { name: "重试" });
    action.focus();
    expect(document.activeElement).toBe(action);
    fireEvent.click(action);
    expect(reload).toHaveBeenCalledOnce();
    expect(consoleError).not.toHaveBeenCalled();
  });
});

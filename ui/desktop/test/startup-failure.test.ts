import { describe, expect, it, vi } from "vitest";
import {
  createDesktopRootErrorOptions,
  renderStaticStartupFailure,
} from "../src/startup-failure.ts";

describe("renderStaticStartupFailure", () => {
  it("replaces startup content with a safe alert and reload button", () => {
    const root = document.createElement("div");
    root.textContent = "private-locator?token=should-disappear";
    document.body.append(root);
    const reload = vi.fn();

    try {
      renderStaticStartupFailure(root, reload);

      expect(root.getAttribute("role")).toBe("alert");
      expect(root.getAttribute("aria-live")).toBe("assertive");
      expect(root.textContent).toContain(
        "The local desktop runtime could not be started.",
      );
      expect(root.textContent).not.toContain("private-locator");
      const action = root.querySelector("button");
      expect(action).toBeInstanceOf(HTMLButtonElement);
      if (!(action instanceof HTMLButtonElement)) {
        throw new Error("startup reload action is missing");
      }
      action.focus();
      expect(document.activeElement).toBe(action);
      action.click();
      expect(reload).toHaveBeenCalledOnce();
    } finally {
      root.remove();
    }
  });

  it("uses the generated Chinese fallback before i18n can initialize", () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["zh-Hans"]);
    const root = document.createElement("div");
    document.body.append(root);

    try {
      renderStaticStartupFailure(root, vi.fn());

      expect(document.documentElement.lang).toBe("zh-CN");
      expect(root.textContent).toContain("无法启动本机桌面运行时。");
      expect(root.textContent).toContain("重试");
    } finally {
      root.remove();
    }
  });

  it("uses the same first-language priority as normal locale detection", () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue([
      "fr-FR",
      "zh-Hans",
    ]);
    const root = document.createElement("div");
    document.body.append(root);

    try {
      renderStaticStartupFailure(root, vi.fn());

      expect(document.documentElement.lang).toBe("en-US");
      expect(root.textContent).toContain(
        "The local desktop runtime could not be started.",
      );
      expect(root.textContent).toContain("Try again");
    } finally {
      root.remove();
    }
  });

  it("suppresses raw React errors and defers an uncaught DOM fallback", async () => {
    const secret = "token=private-render-secret";
    const root = document.createElement("div");
    root.textContent = "React tree is still committing";
    document.body.append(root);
    const detach = vi.fn();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const options = createDesktopRootErrorOptions(root, detach);

    try {
      options.onCaughtError(new Error(secret), { componentStack: secret });
      options.onRecoverableError(new Error(secret), { componentStack: secret });
      options.onUncaughtError(new Error(secret), { componentStack: secret });

      expect(detach).not.toHaveBeenCalled();
      expect(root.textContent).toBe("React tree is still committing");
      await new Promise<void>((resolve) => queueMicrotask(resolve));

      expect(detach).toHaveBeenCalledOnce();
      expect(root.getAttribute("role")).toBe("alert");
      expect(root.textContent).not.toContain(secret);
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      root.remove();
    }
  });
});

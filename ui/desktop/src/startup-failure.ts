import type { RootOptions } from "react-dom/client";
import english from "./generated/locales/en-US.json";
import simplifiedChinese from "./generated/locales/zh-CN.json";
import { detectLocale, type SupportedLocale } from "./locale.ts";

function fallbackCopy(): {
  readonly catalog: typeof english;
  readonly locale: SupportedLocale;
} {
  let locale: SupportedLocale = "en-US";
  try {
    locale = detectLocale(navigator.languages);
  } catch {
    // Browser locale lookup is optional evidence; English remains the fallback.
  }
  return locale === "zh-CN"
    ? { catalog: simplifiedChinese, locale: "zh-CN" }
    : { catalog: english, locale: "en-US" };
}

type RequiredRootErrorOptions = {
  readonly onCaughtError: NonNullable<RootOptions["onCaughtError"]>;
  readonly onRecoverableError: NonNullable<RootOptions["onRecoverableError"]>;
  readonly onUncaughtError: NonNullable<RootOptions["onUncaughtError"]>;
};

export function ensureDesktopRoot(): HTMLElement {
  const existing = document.getElementById("root");
  if (existing !== null) {
    return existing;
  }
  const root = document.createElement("div");
  root.id = "root";
  (document.body ?? document.documentElement).prepend(root);
  return root;
}

export function reloadDesktopPage(): void {
  location.reload();
}

export function createDesktopRootErrorOptions(
  root: HTMLElement,
  detach: () => void,
): RequiredRootErrorOptions {
  let fallbackScheduled = false;
  return {
    // React 19 otherwise logs the complete Error object, including its message
    // and stack, even when an Error Boundary handled the render failure.
    onCaughtError: () => undefined,
    onRecoverableError: () => undefined,
    onUncaughtError: () => {
      if (fallbackScheduled) {
        return;
      }
      fallbackScheduled = true;
      // The callback runs during React's error commit. Detach and replace the
      // tree only after that commit finishes, never by synchronously re-entering
      // the root from its own error callback.
      queueMicrotask(() => {
        try {
          detach();
        } catch {
          // The safe DOM fallback does not depend on React detaching cleanly.
        }
        try {
          renderStaticStartupFailure(root);
        } catch {
          // A host DOM failure leaves no exception detail to reflect or log.
        }
      });
    },
  };
}

export function renderStaticStartupFailure(
  root: HTMLElement,
  reload: () => void = reloadDesktopPage,
): void {
  const { catalog, locale } = fallbackCopy();
  document.documentElement.lang = locale;
  const message = document.createElement("p");
  message.textContent = catalog["app.bootstrap.failed"];

  const action = document.createElement("button");
  action.type = "button";
  action.textContent = catalog["app.bootstrap.retry"];
  action.addEventListener("click", () => {
    try {
      reload();
    } catch {
      // Keep the safe failure screen intact if the host refuses navigation.
    }
  });

  const mark = document.createElement("div");
  mark.className = "brand-mark centered-mark";
  mark.setAttribute("aria-hidden", "true");
  mark.textContent = "VM";

  const content = document.createElement("main");
  content.className = "centered-message";
  content.append(mark, message, action);

  root.setAttribute("aria-live", "assertive");
  root.setAttribute("role", "alert");
  root.removeAttribute("aria-busy");
  root.replaceChildren(content);
  action.focus();
}

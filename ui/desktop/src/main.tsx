import { StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import {
  createDesktopRootErrorOptions,
  ensureDesktopRoot,
  reloadDesktopPage,
  renderStaticStartupFailure,
} from "./startup-failure.ts";
import "./styles.css";

async function bootstrapDesktop(): Promise<void> {
  const root = ensureDesktopRoot();
  const [desktopHost, localization, previewMode] = await Promise.all([
    import("./desktop-host.ts"),
    import("./i18n.ts"),
    import("./preview-mode.ts"),
  ]);
  globalThis.addEventListener(
    "pagehide",
    desktopHost.disposeDesktopRuntimeObservation,
    { once: true },
  );
  const locale = localization.detectLocale(navigator.languages);
  document.documentElement.lang = locale;
  const i18n = await localization.createI18n(locale);
  const preview =
    import.meta.env.DEV &&
    previewMode.previewModeRequested(true, location.search);
  if (!preview) {
    await desktopHost.restoreDesktopNavigation();
  }
  // The Router singleton is created by this module graph. Import it only after
  // the Desktop Host has had one chance to install a restored canonical hash.
  const { BootstrapRoot, DesktopRenderErrorBoundary } = await import(
    "./bootstrap-root.tsx"
  );
  const connect =
    import.meta.env.DEV && preview
      ? async () => {
          const { connectPreviewControl } = await import("./preview-control.ts");
          return connectPreviewControl();
        }
      : desktopHost.connectDesktopControl;

  let applicationRoot: Root | undefined;
  applicationRoot = createRoot(
    root,
    createDesktopRootErrorOptions(root, () => applicationRoot?.unmount()),
  );
  applicationRoot.render(
    <StrictMode>
      <DesktopRenderErrorBoundary
        actionLabel={i18n.t("app.bootstrap.retry")}
        message={i18n.t("app.bootstrap.failed")}
        onReload={reloadDesktopPage}
      >
        <I18nextProvider i18n={i18n}>
          <BootstrapRoot
            connect={connect}
            {...(preview
              ? {}
              : {
                  observeRuntimeFailure:
                    desktopHost.observeDesktopRuntimeFailure,
                  persistNavigation: desktopHost.persistDesktopNavigation,
                })}
            preview={preview}
          />
        </I18nextProvider>
      </DesktopRenderErrorBoundary>
    </StrictMode>,
  );
}

void bootstrapDesktop().catch(() => {
  try {
    renderStaticStartupFailure(ensureDesktopRoot());
  } catch {
    // The static, non-sensitive index.html placeholder remains the last resort.
  }
});

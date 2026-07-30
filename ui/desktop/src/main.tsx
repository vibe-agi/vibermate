import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { BootstrapRoot } from "./App.tsx";
import { connectDesktopControl } from "./desktop-host.ts";
import { createI18n, detectLocale } from "./i18n.ts";
import "./styles.css";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("Desktop root element is missing");
}

const locale = detectLocale(navigator.languages);
document.documentElement.lang = locale;
const i18n = await createI18n(locale);

createRoot(root).render(
  <StrictMode>
    <I18nextProvider i18n={i18n}>
      <BootstrapRoot connect={connectDesktopControl} />
    </I18nextProvider>
  </StrictMode>,
);

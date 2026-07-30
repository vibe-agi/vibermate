import i18next, { type i18n } from "i18next";
import { initReactI18next } from "react-i18next";
import english from "./generated/locales/en-US.json";
import simplifiedChinese from "./generated/locales/zh-CN.json";

export type SupportedLocale = "en-US" | "zh-CN";

export function detectLocale(languages: readonly string[]): SupportedLocale {
  for (const language of languages) {
    const normalized = language.toLowerCase().replaceAll("_", "-");
    if (normalized === "zh-cn" || normalized.startsWith("zh-hans")) {
      return "zh-CN";
    }
    if (normalized.length > 0) {
      return "en-US";
    }
  }
  return "en-US";
}

export async function createI18n(
  locale: SupportedLocale,
): Promise<i18n> {
  const instance = i18next.createInstance();
  await instance.use(initReactI18next).init({
    fallbackLng: "en-US",
    interpolation: { escapeValue: false },
    lng: locale,
    resources: {
      "en-US": { translation: english },
      "zh-CN": { translation: simplifiedChinese },
    },
    returnEmptyString: false,
    supportedLngs: ["en-US", "zh-CN"],
  });
  return instance;
}

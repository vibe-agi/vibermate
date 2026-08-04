import i18next, { type i18n } from "i18next";
import { initReactI18next } from "react-i18next";
import english from "./generated/locales/en-US.json";
import simplifiedChinese from "./generated/locales/zh-CN.json";
import { type SupportedLocale } from "./locale.ts";

export { detectLocale, type SupportedLocale } from "./locale.ts";

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

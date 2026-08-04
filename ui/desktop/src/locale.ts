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

import { describe, expect, it } from "vitest";
import { createI18n, detectLocale } from "../src/i18n.ts";

describe("Desktop locales", () => {
  it("detects only the two supported production locales", () => {
    expect(detectLocale(["zh-Hans-SG", "en-US"])).toBe("zh-CN");
    expect(detectLocale(["fr-FR"])).toBe("en-US");
    expect(detectLocale([])).toBe("en-US");
  });

  it("renders synchronized parameterized messages", async () => {
    const english = await createI18n("en-US");
    const chinese = await createI18n("zh-CN");
    expect(english.t("captures.active.title", { count: 2 })).toBe("2 captures");
    expect(chinese.t("captures.active.title", { count: 2 })).toContain("2");
  });

  it("keeps the semantic-observation boundary explicit in both locales", async () => {
    const english = await createI18n("en-US");
    const chinese = await createI18n("zh-CN");

    expect(english.t("requests.empty.description")).toMatch(
      /only when an exact semantic endpoint is used/u,
    );
    expect(chinese.t("requests.empty.description")).toMatch(
      /精确语义端点/u,
    );
  });
});

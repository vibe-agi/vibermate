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
    expect(english.t("access.apply.succeeded", { revision: 7 })).toContain("7");
    expect(chinese.t("access.apply.succeeded", { revision: 7 })).toContain("7");
    expect(english.t("access.apply.succeeded")).not.toBe("");
    expect(chinese.t("access.apply.succeeded")).not.toBe("");
  });
});

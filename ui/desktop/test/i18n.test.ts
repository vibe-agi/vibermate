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
    expect(english.t("access.apply.succeeded")).not.toBe("");
    expect(chinese.t("access.apply.succeeded")).not.toBe("");
  });

  it("explains bounded Activity paging without claiming the server is exhausted", async () => {
    const english = await createI18n("en-US");
    const chinese = await createI18n("zh-CN");

    expect(english.t("activity.pagingSafetyStopped.detail")).toMatch(
      /may still exist.*Refreshing.*latest window/u,
    );
    expect(chinese.t("activity.pagingSafetyStopped.detail")).toMatch(
      /可能仍有.*刷新.*最新窗口/u,
    );
  });
});

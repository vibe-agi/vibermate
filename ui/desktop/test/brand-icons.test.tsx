import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BrandIcon, type BrandIconName } from "../src/brand-icons.tsx";

describe("BrandIcon", () => {
  it.each<readonly [BrandIconName, boolean]>([
    ["anthropic", true],
    ["claude-code", false],
    ["codex", false],
    ["openai", true],
  ])("renders the selected %s asset without accessible duplication", (name, monochrome) => {
    const { container } = render(<BrandIcon name={name} />);
    const icon = container.querySelector<HTMLImageElement>(
      `[data-brand-icon="${name}"]`,
    );

    expect(icon).not.toBeNull();
    expect(icon?.alt).toBe("");
    expect(icon?.getAttribute("aria-hidden")).toBe("true");
    expect(icon?.draggable).toBe(false);
    expect(icon?.classList.contains("monochrome")).toBe(monochrome);
    expect(screen.queryByRole("img")).toBeNull();
  });
});

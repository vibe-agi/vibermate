import anthropicIcon from "./assets/brand-icons/anthropic.svg";
import claudeCodeIcon from "./assets/brand-icons/claude-code.svg";
import codexIcon from "./assets/brand-icons/codex.svg";
import openAIIcon from "./assets/brand-icons/openai.svg";

export type BrandIconName =
  | "anthropic"
  | "claude-code"
  | "codex"
  | "openai";

const brandIconSources = {
  anthropic: anthropicIcon,
  "claude-code": claudeCodeIcon,
  codex: codexIcon,
  openai: openAIIcon,
} satisfies Readonly<Record<BrandIconName, string>>;

const monochromeBrandIcons = new Set<BrandIconName>(["anthropic", "openai"]);

export function BrandIcon({ name }: { readonly name: BrandIconName }) {
  const monochrome = monochromeBrandIcons.has(name);
  return (
    <img
      alt=""
      aria-hidden="true"
      className={`brand-icon${monochrome ? " monochrome" : ""}`}
      data-brand-icon={name}
      draggable={false}
      src={brandIconSources[name]}
    />
  );
}

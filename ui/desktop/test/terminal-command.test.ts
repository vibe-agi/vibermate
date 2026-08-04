import { describe, expect, it } from "vitest";
import {
  shellQuoted,
  terminalRunCommand,
} from "../src/terminal-command.ts";

describe("terminal command formatting", () => {
  it("quotes the Desktop-owned path and keeps the client choice fixed", () => {
    expect(
      terminalRunCommand("/Applications/Vibe Mate's.app/vibermate", "claude"),
    ).toBe(
      `'` +
        `/Applications/Vibe Mate'"'"'s.app/vibermate` +
        `' run -- claude`,
    );
    expect(terminalRunCommand("/opt/VibeMate/vibermate", "codex")).toBe(
      "'/opt/VibeMate/vibermate' run -- codex",
    );
  });

  it("quotes a standalone path without turning it into shell syntax", () => {
    expect(shellQuoted("/tmp/a b/vibermate")).toBe("'/tmp/a b/vibermate'");
  });
});

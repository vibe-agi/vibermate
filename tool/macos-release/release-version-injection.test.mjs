import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const ldflag =
  "github.com/vibe-agi/vibermate/internal/productbuild.releaseVersion";

test("every distributed Go runtime receives the admitted product version", async () => {
  for (const path of [
    "ui/flutter_app/tool/build_macos_distribution.sh",
    "ui/flutter_app/tool/build_macos_app.sh",
    "tool/linux-release/build-linux-distributions.sh",
    "tool/docker/build-local.sh",
  ]) {
    const source = await readFile(new URL(path, root), "utf8");
    assert.match(source, new RegExp(ldflag.replaceAll(".", "\\."), "u"), path);
  }
});

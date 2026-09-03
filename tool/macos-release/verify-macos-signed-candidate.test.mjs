import assert from "node:assert/strict";
import {
  mkdir,
  mkdtemp,
  realpath,
  rm,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import test from "node:test";
import {
  createCanonicalTemporaryDirectory,
} from "./verify-macos-signed-candidate.mjs";

test("certificate extraction canonicalizes a symlinked temporary-directory parent", async (context) => {
  const fixture = await realpath(
    await mkdtemp(resolve(tmpdir(), "vibermate-canonical-temp-test-")),
  );
  context.after(() => rm(fixture, { recursive: true, force: true }));
  const canonicalParent = resolve(fixture, "canonical");
  const aliasedParent = resolve(fixture, "alias");
  await mkdir(canonicalParent);
  await symlink(canonicalParent, aliasedParent, "dir");

  const directory = await createCanonicalTemporaryDirectory(
    "certificate-",
    aliasedParent,
  );

  assert.equal(directory, await realpath(directory));
  assert.equal(dirname(directory), canonicalParent);
});

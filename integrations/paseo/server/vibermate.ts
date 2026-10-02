import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { z } from "zod";
import { profileSchema, type Connection } from "../shared/contracts.ts";

const run = promisify(execFile);
const catalogSchema = z.object({
  schema: z.literal("vibermate-capture-environments/v1"),
  items: z.array(profileSchema).max(1024),
}).strict();

export async function profiles(connection: Connection, signal?: AbortSignal) {
  const args = ["profiles", "--json"];
  if (connection.server) args.push("--server", connection.server);
  let stdout: string;
  try {
    ({ stdout } = await run(connection.cli, args, {
      timeout: 5000, maxBuffer: 128 * 1024, signal,
      // Never inherit a conversation's capture authority into the catalog query.
      env: { ...process.env, VIBERMATE_PASEO_ENVIRONMENT_ID: undefined },
    }));
  } catch {
    throw new Error("Cannot read ViberMate profiles. Check the CLI path, open the local app or log in on this host, and use a build with 'profiles --json'.");
  }
  const page = catalogSchema.parse(JSON.parse(stdout));
  if (new Set(page.items.map((item) => item.id)).size !== page.items.length)
    throw new Error("ViberMate returned duplicate profile identities.");
  return page.items;
}

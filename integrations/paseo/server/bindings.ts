import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { isAbsolute, join } from "node:path";
import { bindingSchema, type Binding } from "../shared/contracts.ts";

// One atomic file per conversation avoids a daemon-wide write lock or database.
export class Bindings {
  readonly directory: string;
  constructor(directory: string) {
    if (!isAbsolute(directory)) throw new Error("Binding storage must be an absolute directory.");
    this.directory = directory;
  }
  private file(agentId: string) {
    return join(this.directory, createHash("sha256").update(agentId).digest("hex") + ".json");
  }
  async get(agentId: string): Promise<Binding | null> {
    let raw: string;
    try { raw = await readFile(this.file(agentId), "utf8"); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
      throw new Error("ViberMate conversation binding could not be read.");
    }
    const binding = bindingSchema.parse(JSON.parse(raw));
    if (binding.agentId !== agentId) throw new Error("ViberMate conversation binding identity does not match.");
    return binding;
  }
  async put(value: Binding) {
    const binding = bindingSchema.parse(value);
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const target = this.file(binding.agentId);
    const temporary = target + "." + randomUUID();
    try {
      await writeFile(temporary, JSON.stringify(binding) + "\n", { mode: 0o600, flag: "wx" });
      await rename(temporary, target);
    } finally {
      await rm(temporary, { force: true });
    }
  }
}

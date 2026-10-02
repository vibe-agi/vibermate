import { randomUUID } from "node:crypto";
import type { Binding, Pending, Profile } from "../shared/contracts.ts";
import type { Bindings } from "./bindings.ts";

type Waiting = {
  value: Pending;
  server: string;
  resolve(binding: Binding): void;
  reject(error: Error): void;
  committing: boolean;
  bindings: Pick<Bindings, "put">;
};

export class Selections {
  private waiting = new Map<string, Waiting>();
  pending(): Pending[] { return [...this.waiting.values()].map((item) => item.value); }

  async request(input: {
    agentId: string; provider: string; cwd: string; profiles: Profile[]; server: string;
    bindings: Pick<Bindings, "put">;
  }, signal: AbortSignal, timeout = 20_000): Promise<Binding> {
    if (signal.aborted) throw new Error("Conversation startup was canceled.");
    if (input.profiles.length === 0) throw new Error("No active ViberMate profiles are available to this user.");
    if ([...this.waiting.values()].some((item) => item.value.agentId === input.agentId))
      throw new Error("This conversation is already waiting for a profile choice.");
    const id = randomUUID();
    // Paseo bounds before hooks at 30 seconds. Leave time for catalog I/O and cleanup.
    const value: Pending = { id, agentId: input.agentId, provider: input.provider, cwd: input.cwd,
      profiles: input.profiles, expiresAt: Date.now() + timeout };
    let timer: ReturnType<typeof setTimeout>;
    const abort = () => this.cancel(id, "Conversation startup was canceled.");
    try {
      return await new Promise<Binding>((resolve, reject) => {
        this.waiting.set(id, { value, server: input.server, resolve, reject, committing: false, bindings: input.bindings });
        signal.addEventListener("abort", abort, { once: true });
        timer = setTimeout(() => this.cancel(id, "Profile selection timed out. Retry creating the conversation."), timeout);
      });
    } finally {
      clearTimeout(timer!);
      signal.removeEventListener("abort", abort);
      this.waiting.delete(id);
    }
  }

  async choose(id: string, environmentId: string) {
    const item = this.waiting.get(id);
    if (!item || item.committing || Date.now() >= item.value.expiresAt)
      throw new Error("This profile selection has already ended.");
    if (!item.value.profiles.some((profile) => profile.id === environmentId))
      throw new Error("Choose one of the profiles offered for this conversation.");
    item.committing = true;
    const binding: Binding = { agentId: item.value.agentId, environmentId, server: item.server };
    try {
      await item.bindings.put(binding);
      if (this.waiting.get(id) !== item) throw new Error("Conversation startup has ended.");
      item.resolve(binding);
      this.waiting.delete(id);
      return { agentId: binding.agentId };
    } catch (error) {
      item.reject(new Error("The profile choice could not be saved; the agent was not started."));
      this.waiting.delete(id);
      throw error;
    }
  }

  cancel(id: string, message = "Profile selection was canceled; the agent was not started.") {
    const item = this.waiting.get(id);
    if (item) {
      this.waiting.delete(id);
      item.reject(new Error(message));
    }
    return {};
  }
  close() { for (const id of this.waiting.keys()) this.cancel(id, "ViberMate plugin stopped."); }
}

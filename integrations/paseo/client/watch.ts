import type { PluginClientContext } from "@getpaseo/plugin/client";
import { pendingRpc } from "../shared/contracts.ts";

export function watchSelections(client: Pick<PluginClientContext, "rpc" | "openScreen">) {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout>;
  let activeSelection: string | null = null;
  async function poll() {
    try {
      const { items } = await client.rpc(pendingRpc, {});
      if (stopped) return;
      if (activeSelection && items.some((item) => item.id === activeSelection)) return;
      const next = items[0];
      if (next) {
        activeSelection = next.id;
        // Entry code runs while the mobile sidebar is closed. Opening a screen
        // gives Paseo a mounted surface for its native modal on every device.
        client.openScreen({ screenId: "profiles", params: { selection: next.id } });
      } else activeSelection = null;
    } catch {
      // Reconnects retry; polling never chooses a profile or admits an agent.
    } finally {
      // ponytail: one poll/second per connected client; use a Paseo push RPC if
      // the public plugin API gains one and host connection counts warrant it.
      if (!stopped) timer = setTimeout(poll, 1000);
    }
  }
  void poll();
  return () => { stopped = true; clearTimeout(timer); };
}

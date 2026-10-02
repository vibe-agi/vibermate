import { homedir } from "node:os";
import { join } from "node:path";
import type { PluginServerContext } from "@getpaseo/plugin/server";
import { Bindings } from "./server/bindings.ts";
import { Selections } from "./server/selections.ts";
import { profiles } from "./server/vibermate.ts";
import { cancelRpc, chooseRpc, connectionSettings, pendingRpc, profilesRpc, type Connection } from "./shared/contracts.ts";

export default function contribute(server: PluginServerContext) {
  const settings = server.registerSettings(connectionSettings);
  const stores = new Map<string, Bindings>();
  const selections = new Selections();
  async function connection() {
    const state = await settings.read();
    if (state.status !== "ready") throw new Error("ViberMate connection settings are invalid.");
    return state.values;
  }
  function store(config: Connection) {
    const directory = config.stateDirectory || join(process.env.PASEO_HOME || join(homedir(), ".paseo"), "plugin-data", "vibermate", "bindings");
    let result = stores.get(directory);
    if (!result) { result = new Bindings(directory); stores.set(directory, result); }
    return result;
  }
  server.handle(pendingRpc, () => ({ items: selections.pending() }));
  server.handle(chooseRpc, ({ id, environmentId }) => selections.choose(id, environmentId));
  server.handle(cancelRpc, ({ id }) => selections.cancel(id));
  server.handle(profilesRpc, async () => ({ items: await profiles(await connection()) }));

  const remove = server.before("agent.session_open", async ({ request }, { signal }) => {
    if (request.provider !== "claude" && request.provider !== "codex") return request;
    const config = await connection();
    if (!config.enabled) return request;
    if (request.purpose === "history") return request;
    const bindings = store(config);
    let binding = await bindings.get(request.agentId);
    if (!binding) {
      const choices = await profiles(config, signal);
      binding = await selections.request({ agentId: request.agentId, provider: request.provider,
        cwd: request.cwd, profiles: choices, server: config.server, bindings }, signal);
    }
    return { ...request, env: { ...request.env,
      VIBERMATE_PASEO_ENVIRONMENT_ID: binding.environmentId,
      VIBERMATE_PASEO_SERVER: binding.server,
      VIBERMATE_PASEO_CLI: config.cli,
    } };
  });
  return () => { remove(); selections.close(); };
}

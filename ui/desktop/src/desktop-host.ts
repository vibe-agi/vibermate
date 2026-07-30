import { invoke } from "@tauri-apps/api/core";
import {
  createControlClient,
  type ControlClient,
  type DesktopSession,
} from "./control-client.ts";

export async function connectDesktopControl(): Promise<ControlClient> {
  const payload = await invoke<unknown>("take_control_session");
  return createControlClient(decodeSession(payload));
}

function decodeSession(payload: unknown): DesktopSession {
  if (payload === null || typeof payload !== "object") {
    throw new Error("Desktop shell returned an invalid control session");
  }
  const candidate = payload as Record<string, unknown>;
  for (const field of [
    "schema",
    "baseUrl",
    "readToken",
    "writeToken",
    "instanceId",
    "expiresAt",
  ]) {
    if (typeof candidate[field] !== "string") {
      throw new Error("Desktop shell returned an incomplete control session");
    }
  }
  return candidate as unknown as DesktopSession;
}

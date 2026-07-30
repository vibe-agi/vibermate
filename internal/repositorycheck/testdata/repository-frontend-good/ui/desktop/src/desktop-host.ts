import { invoke } from "@tauri-apps/api/core";

export async function connect() {
  return invoke("take_control_session");
}

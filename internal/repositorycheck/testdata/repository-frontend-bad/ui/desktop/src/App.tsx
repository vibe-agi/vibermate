import { invoke } from "@tauri-apps/api/core";

export function App() {
  localStorage.setItem("capability", "unsafe");
  void invoke("unsafe");
  return <button title="Approve this action">Approve</button>;
}

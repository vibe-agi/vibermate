import { useEffect, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { usePaseo, useRpc, type PluginScreenProps } from "@getpaseo/plugin/client";
import { Modal } from "@getpaseo/plugin/client/react-native";
import { cancelRpc, chooseRpc, pendingRpc, type Pending } from "../shared/contracts";

export function ProfileSelection({ theme, params, navigation }: PluginScreenProps) {
  const paseo = usePaseo();
  const readPending = useRpc(pendingRpc);
  const choose = useRpc(chooseRpc);
  const cancel = useRpc(cancelRpc);
  const [items, setItems] = useState<Pending[]>([]);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [finished, setFinished] = useState(false);
  const [chosenAgent, setChosenAgent] = useState<string | null>(null);
  const current = items.find((item) => item.id === params.selection) ?? (!params.selection ? items[0] : undefined);
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const page = await readPending({});
        if (!stopped) setItems(page.items);
      } catch (error) {
        if (!stopped) setError(error instanceof Error ? error.message : "Cannot read pending conversations.");
      } finally { if (!stopped) timer = setTimeout(refresh, 1000); }
    }
    void refresh();
    return () => { stopped = true; clearTimeout(timer); };
  }, [readPending]);
  useEffect(() => { setSelected(""); setError(""); setFinished(false); setChosenAgent(null); }, [params.selection]);
  useEffect(() => {
    if (!chosenAgent || !navigation) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const deadline = Date.now() + 120_000;
    const agent = paseo.agents.ref(chosenAgent);
    async function openWhenReady() {
      try {
        await agent.refresh();
        if (!stopped && agent.current()) {
          const pending = await readPending({});
          if (!stopped && pending.items.length === 0) navigation!.openAgent({ agentId: chosenAgent! });
          return;
        }
      } catch { /* Agent registration follows the profile decision. */ }
      if (!stopped && Date.now() < deadline) timer = setTimeout(openWhenReady, 1000);
    }
    void openWhenReady();
    return () => { stopped = true; clearTimeout(timer); };
  }, [chosenAgent, navigation, paseo, readPending]);

  async function confirm() {
    if (!current || !selected || busy) return;
    setBusy(true);
    try {
      const result = await choose({ id: current.id, environmentId: selected });
      setFinished(true);
      setChosenAgent(result.agentId);
    } catch (error) { setError(error instanceof Error ? error.message : "Cannot save the profile choice."); }
    finally { setBusy(false); }
  }
  async function dismiss() {
    if (!current || busy) return;
    setBusy(true);
    try { await cancel({ id: current.id }); setFinished(true); }
    catch (error) { setError(error instanceof Error ? error.message : "Cannot cancel startup."); }
    finally { setBusy(false); }
  }
  const text = { color: theme.colors.foreground };
  return <View style={{ flex: 1, padding: 20, gap: 12, backgroundColor: theme.colors.surface0 }}>
    <Text style={text}>{finished ? "Profile choice handled. Return to your conversation." : current ? "Waiting for your profile choice." : "No conversations are waiting. If selection timed out, retry creating the conversation."}</Text>
    {error ? <Text accessibilityRole="alert" style={{ color: theme.colors.statusDanger }}>{error}</Text> : null}
    <Modal title="Choose ViberMate profile" open={!!current && !finished} onOpenChange={(open) => { if (!open) void dismiss(); }}>
      <Modal.Content>
        {current ? <View style={{ gap: 12 }}>
          <Text style={text}>{current.provider} · {current.cwd}</Text>
          <Text style={{ color: theme.colors.foregroundMuted }}>This choice belongs to this conversation. Select within 20 seconds; cancellation or timeout stops startup.</Text>
          {current.profiles.map((profile) => <Pressable key={profile.id} accessibilityRole="radio" accessibilityState={{ checked: selected === profile.id, disabled: busy }} accessibilityLabel={`${profile.name} (${profile.id})`} disabled={busy} onPress={() => setSelected(profile.id)} style={{ padding: 14, borderRadius: 8, borderWidth: 1, borderColor: selected === profile.id ? theme.colors.accent : theme.colors.border, backgroundColor: theme.colors.surface1 }}>
            <Text style={text}>{profile.name}</Text>
            <Text style={{ color: theme.colors.foregroundMuted }}>{profile.id}</Text>
          </Pressable>)}
          {error ? <Text accessibilityRole="alert" style={{ color: theme.colors.statusDanger }}>{error}</Text> : null}
          <Pressable accessibilityRole="button" accessibilityState={{ disabled: !selected || busy }} disabled={!selected || busy} onPress={() => void confirm()} style={{ padding: 14, borderRadius: 8, backgroundColor: theme.colors.accent, opacity: !selected || busy ? 0.5 : 1 }}>
            <Text style={{ color: theme.colors.accentForeground, textAlign: "center" }}>{busy ? "Saving…" : "Use this profile"}</Text>
          </Pressable>
          <Pressable accessibilityRole="button" disabled={busy} onPress={() => void dismiss()} style={{ padding: 14 }}><Text style={{ ...text, textAlign: "center" }}>Cancel startup</Text></Pressable>
        </View> : null}
      </Modal.Content>
    </Modal>
  </View>;
}

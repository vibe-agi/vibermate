import { useState } from "react";
import { Text } from "react-native";
import { useRpc, useSettings, type PluginSurfaceProps } from "@getpaseo/plugin/client";
import { SettingsAction, SettingsInput, SettingsSection, SettingsSwitch } from "@getpaseo/plugin/client/ui";
import { connectionSettings, profilesRpc, type Connection } from "../shared/contracts";

export function ConnectionSettings({ theme }: PluginSurfaceProps) {
  const settings = useSettings(connectionSettings);
  const readProfiles = useRpc(profilesRpc);
  const [draft, setDraft] = useState<Connection | null>(null);
  const [result, setResult] = useState("");
  const [checking, setChecking] = useState(false);
  if (settings.status !== "ready") {
    return <Text style={{ color: theme.colors.foreground }}>ViberMate settings: {settings.status}</Text>;
  }
  const values = draft ?? settings.values;
  const change = (patch: Partial<Connection>) => setDraft({ ...values, ...patch });
  async function saveAndCheck() {
    setChecking(true);
    setResult("");
    try {
      if (settings.status !== "ready" || !await settings.save(values, settings.revision)) return;
      setDraft(null);
      const page = await readProfiles({});
      setResult(`${page.items.length} profiles available on this host.`);
    } catch (error) {
      setResult(error instanceof Error ? error.message : "Cannot read ViberMate profiles.");
    } finally { setChecking(false); }
  }
  return <SettingsSection title="ViberMate connection" info="Configure ViberMate on the machine running this Paseo host. Phone clients use this same connection.">
    <SettingsInput label="ViberMate CLI" initialValue={values.cli} onChangeText={(cli) => change({ cli })} placeholder="/absolute/path/to/vibermate" />
    <SettingsInput label="Server" hint="Leave empty for the local Mac app. For a remote server, run vibermate login on this host first." initialValue={values.server} onChangeText={(server) => change({ server })} placeholder="https://runtime.example.com" />
    <SettingsInput label="Binding directory" hint="Optional absolute path. Use a different directory for each Paseo daemon on the same machine." initialValue={values.stateDirectory} onChangeText={(stateDirectory) => change({ stateDirectory })} />
    <SettingsSwitch label="Choose a profile for conversations" hint="Configure the Claude/Codex launcher commands in the plugin README before enabling." value={values.enabled} onValueChange={(enabled) => change({ enabled })} />
    <SettingsAction label="Save connection" actionLabel={checking ? "Checking…" : "Save and check"} disabled={checking || settings.saving} onPress={() => void saveAndCheck()} />
    {settings.saveError ? <Text style={{ color: theme.colors.statusDanger }}>{settings.saveError}</Text> : null}
    {result ? <Text style={{ color: theme.colors.foreground }}>{result}</Text> : null}
  </SettingsSection>;
}

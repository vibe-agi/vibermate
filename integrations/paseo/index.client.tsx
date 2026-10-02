import type { PluginClientContext, PluginSidebarItemProps } from "@getpaseo/plugin/client";
import { SidebarRow } from "@getpaseo/plugin/client/ui";
import { ProfileSelection } from "./client/selection";
import { ConnectionSettings } from "./client/settings";
import { watchSelections } from "./client/watch";

function Sidebar({ openScreen }: PluginSidebarItemProps) {
  return <SidebarRow icon="Waypoints" label="ViberMate" onPress={() => openScreen({ screenId: "profiles" })} />;
}

export default function contribute(client: PluginClientContext) {
  client.addSettingsScreen({ id: "connection", title: "ViberMate", icon: "Waypoints", Component: ConnectionSettings });
  client.addScreen({ id: "profiles", title: "ViberMate profile", Component: ProfileSelection });
  client.addSidebarHeaderItem({ id: "profiles", title: "ViberMate", Component: Sidebar });
  return watchSelections(client);
}

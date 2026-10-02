import { defineRpc, defineSettings } from "@getpaseo/plugin";
import { z } from "zod";

export const profileSchema = z.object({
  id: z.string().min(1).max(128),
  name: z.string().min(1).max(1024),
}).strict();
export type Profile = z.infer<typeof profileSchema>;

export const serverSchema = z.string().max(2048).refine((value) => {
  if (value === "") return true;
  try {
    const url = new URL(value);
    return (url.protocol === "https:" || url.protocol === "http:") &&
      !url.username && !url.password && !!url.hostname &&
      (url.pathname === "" || url.pathname === "/") && !url.search && !url.hash;
  } catch { return false; }
}, "Use an http(s):// server origin, or leave empty for the local ViberMate app.").transform((value) => {
  if (!value) return "";
  const url = new URL(value);
  return `${url.protocol}//${url.hostname}:${url.port || (url.protocol === "https:" ? "443" : "80")}`;
});

export const connectionSettings = defineSettings({
  id: "connection", scope: "host", version: 1,
  schema: z.object({
    enabled: z.boolean().default(false),
    cli: z.string().min(1).max(4096).default("vibermate"),
    server: serverSchema.default(""),
    // An explicit directory keeps multiple Paseo daemons on one machine separate.
    stateDirectory: z.string().max(4096).refine((value) => !value || value.startsWith("/") && !value.includes("\0"), "Use an absolute directory on the daemon host.").default(""),
  }).strict(),
});
export type Connection = z.infer<typeof connectionSettings.schema>;

export const bindingSchema = z.object({
  agentId: z.string().min(1).max(256),
  environmentId: z.string().min(1).max(128),
  server: serverSchema,
}).strict();
export type Binding = z.infer<typeof bindingSchema>;

export const pendingSchema = z.object({
  id: z.string(), agentId: z.string(), provider: z.string(), cwd: z.string(),
  expiresAt: z.number(), profiles: z.array(profileSchema),
}).strict();
export type Pending = z.infer<typeof pendingSchema>;

export const pendingRpc = defineRpc({
  name: "selection.pending", input: z.object({}).strict(),
  output: z.object({ items: z.array(pendingSchema) }).strict(),
});
export const chooseRpc = defineRpc({
  name: "selection.choose",
  input: z.object({ id: z.string(), environmentId: z.string() }).strict(),
  output: z.object({ agentId: z.string() }).strict(),
});
export const cancelRpc = defineRpc({
  name: "selection.cancel", input: z.object({ id: z.string() }).strict(),
  output: z.object({}).strict(),
});
export const profilesRpc = defineRpc({
  name: "profiles.list", input: z.object({}).strict(),
  output: z.object({ items: z.array(profileSchema) }).strict(),
});

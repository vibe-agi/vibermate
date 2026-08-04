export type ManagedRunClient = "claude" | "codex";

/**
 * Formats one fixed managed-client launch without asking a shell to interpret
 * the Desktop-owned executable path. The client value is a closed product
 * choice, not a route, Access, account, or model selector.
 */
export function terminalRunCommand(
  commandPath: string,
  client: ManagedRunClient,
): string {
  return `${shellQuoted(commandPath)} run -- ${client}`;
}

export function shellQuoted(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

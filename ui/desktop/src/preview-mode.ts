export function previewModeRequested(
  development: boolean,
  search: string,
): boolean {
  return (
    development && new URLSearchParams(search).get("preview") === "1"
  );
}

import { dashboardRoutePaths } from "./navigation.ts";

export const navigationStateSchema = "vibermate-navigation-state-v1";

export interface PersistedNavigationState {
  readonly schema: typeof navigationStateSchema;
  readonly locator: string;
}

export interface DashboardLocationSnapshot {
  readonly pathname: string;
  readonly searchStr: string;
}

const maximumLocatorBytes = 2_048;
const maximumEntityIdBytes = 512;
const unsafeValue = /[\\/\\\p{Cc}]/u;

const staticPaths = new Set([
  ...Object.values(dashboardRoutePaths).map((path) => path.slice(1)),
  "captures/requests",
]);

const dynamicPatterns = [
  ["captures", maximumEntityIdBytes],
  ["activity/requests", maximumEntityIdBytes],
  ["environments", maximumEntityIdBytes],
] as const;

function decodeSafeValue(raw: string, maximumBytes: number): boolean {
  let value: string;
  try {
    value = decodeURIComponent(raw);
  } catch {
    return false;
  }
  return (
    value.length > 0 &&
    value.trim() === value &&
    !value.toLocaleLowerCase("en-US").startsWith("secret:") &&
    !unsafeValue.test(value) &&
    new TextEncoder().encode(value).length <= maximumBytes
  );
}

function validPath(path: string): boolean {
  if (staticPaths.has(path)) {
    return true;
  }
  const segments = path.split("/");
  if (
    segments.length === 4 &&
    segments[0] === "environments" &&
    segments[2] === "revisions"
  ) {
    return (
      decodeSafeValue(segments[1] ?? "", maximumEntityIdBytes) &&
      /^[1-9][0-9]*$/u.test(segments[3] ?? "")
    );
  }
  return dynamicPatterns.some(([prefix, maximumBytes]) => {
    const prefixSegments = prefix.split("/");
    if (segments.length !== prefixSegments.length + 1) {
      return false;
    }
    return (
      prefixSegments.every((part, index) => segments[index] === part) &&
      decodeSafeValue(segments.at(-1) ?? "", maximumBytes)
    );
  });
}

function validPolicySearch(search: string): boolean {
  if (!search.startsWith("selected=") || search.includes("&")) {
    return false;
  }
  return decodeSafeValue(
    search.slice("selected=".length),
    maximumEntityIdBytes,
  );
}

export function validNavigationLocator(locator: string): boolean {
  if (
    locator.length === 0 ||
    locator.startsWith("/") ||
    locator.trim() !== locator ||
    locator.includes("#") ||
    locator.includes("\\") ||
    /\p{Cc}/u.test(locator) ||
    new TextEncoder().encode(locator).length > maximumLocatorBytes
  ) {
    return false;
  }
  const question = locator.indexOf("?");
  const path = question === -1 ? locator : locator.slice(0, question);
  const search = question === -1 ? "" : locator.slice(question + 1);
  if (!validPath(path)) {
    return false;
  }
  if (search.length === 0) {
    return question === -1;
  }
  return path === "policies/approvals" && validPolicySearch(search);
}

export function navigationLocatorFromLocation(
  location: DashboardLocationSnapshot,
): string | undefined {
  if (
    !location.pathname.startsWith("/") ||
    (location.searchStr.length > 0 && !location.searchStr.startsWith("?"))
  ) {
    return undefined;
  }
  const locator = `${location.pathname.slice(1)}${location.searchStr}`;
  return validNavigationLocator(locator) ? locator : undefined;
}

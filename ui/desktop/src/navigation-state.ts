import { dashboardRoutePaths, dashboardTaskRoutePaths } from "./navigation.ts";

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
const maximumAccessIdBytes = 128;
const maximumEntityIdBytes = 512;
const unsafeValue = /[\/\\\p{Cc}]/u;

const staticPaths = new Set(
  [
    ...Object.values(dashboardRoutePaths),
    ...Object.values(dashboardTaskRoutePaths),
  ]
    .filter((path) => !path.includes("$"))
    .map((path) => path.slice(1)),
);

const dynamicPaths = [
  [dashboardTaskRoutePaths.accessRouting, maximumAccessIdBytes],
  [dashboardTaskRoutePaths.activityRun, maximumEntityIdBytes],
  [dashboardTaskRoutePaths.activityRequest, maximumEntityIdBytes],
  [dashboardTaskRoutePaths.extensionDetail, maximumEntityIdBytes],
  [dashboardTaskRoutePaths.dashboardExtension, maximumEntityIdBytes],
] as const;

function decodeSafeValue(
  raw: string,
  maximumBytes: number,
): string | undefined {
  let decoded: string;
  try {
    decoded = decodeURIComponent(raw);
  } catch {
    return undefined;
  }
  if (
    decoded.length === 0 ||
    decoded.trim() !== decoded ||
    decoded.toLocaleLowerCase("en-US").startsWith("secret:") ||
    unsafeValue.test(decoded) ||
    new TextEncoder().encode(decoded).length > maximumBytes
  ) {
    return undefined;
  }
  return decoded;
}

function validPath(path: string): boolean {
  if (staticPaths.has(path)) {
    return true;
  }
  const segments = path.split("/");
  return dynamicPaths.some(([pattern, maximumBytes]) => {
    const patternSegments = pattern.slice(1).split("/");
    if (segments.length !== patternSegments.length) {
      return false;
    }
    let locator: string | undefined;
    for (const [index, patternSegment] of patternSegments.entries()) {
      if (patternSegment.startsWith("$")) {
        locator = segments[index];
      } else if (segments[index] !== patternSegment) {
        return false;
      }
    }
    return (
      locator !== undefined &&
      decodeSafeValue(locator, maximumBytes) !== undefined
    );
  });
}

function validPolicySearch(search: string): boolean {
  if (!search.startsWith("selected=") || search.includes("&")) {
    return false;
  }
  return (
    decodeSafeValue(search.slice("selected=".length), maximumEntityIdBytes) !==
    undefined
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

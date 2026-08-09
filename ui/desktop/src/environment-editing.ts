import type {
  EnvironmentClientEndpoint,
  EnvironmentPlanMode,
  ProviderAccountRecord,
} from "./control-types.ts";

export function assignRouteAccount(
  endpoints: readonly EnvironmentClientEndpoint[],
  endpointID: string,
  planID: string,
  routeID: string,
  account?: ProviderAccountRecord,
): readonly EnvironmentClientEndpoint[] {
  return endpoints.map((endpoint) => {
    if (endpoint.id !== endpointID) return endpoint;
    let endpointChanged = false;
    const protocolPlans = endpoint.protocolPlans.map((plan) => {
      if (plan.id !== planID) return plan;
      const desiredPlanMode: EnvironmentPlanMode =
        account === undefined ? "original_passthrough" : "managed";
      let planChanged = plan.mode !== desiredPlanMode;
      const routes = plan.upstreamPlan.routes.map((route) => {
        if (route.id !== routeID) return route;

        const desiredAccountPolicy = accountPolicy(
          route.providerTarget.realmId,
          route.accountPolicy.revision,
          account,
        );
        const accountChanged = !sameAccountPolicy(
          route.accountPolicy,
          desiredAccountPolicy,
        );
        const ownershipModeChanged = plan.mode !== desiredPlanMode;
        const desiredModelMode =
          desiredPlanMode === "original_passthrough" ||
          ownershipModeChanged ||
          route.modelPolicy.mode === "preserve"
            ? desiredPlanMode === "original_passthrough"
              ? "preserve"
              : "passthrough"
            : route.modelPolicy.mode;
        const modelChanged = route.modelPolicy.mode !== desiredModelMode;
        if (!accountChanged && !modelChanged) return route;

        planChanged = true;
        return {
          ...route,
          revision: route.revision + 1,
          accountPolicy: accountChanged
            ? { ...desiredAccountPolicy, revision: route.accountPolicy.revision + 1 }
            : route.accountPolicy,
          modelPolicy: modelChanged
            ? {
                ...route.modelPolicy,
                revision: route.modelPolicy.revision + 1,
                mode: desiredModelMode,
                fixedModel: "",
              }
            : route.modelPolicy,
        };
      });
      if (!planChanged) return plan;
      endpointChanged = true;
      return {
        ...plan,
        revision: plan.revision + 1,
        mode: desiredPlanMode,
        upstreamPlan: { ...plan.upstreamPlan, routes },
      };
    });
    return endpointChanged
      ? { ...endpoint, revision: endpoint.revision + 1, protocolPlans }
      : endpoint;
  });
}

function accountPolicy(
  realm: string,
  revision: number,
  account?: ProviderAccountRecord,
) {
  return account === undefined
    ? {
        revision,
        mode: "client_passthrough" as const,
        allowedRealmIds: [realm],
        preferredAccountId: "",
        candidateAccountIds: [],
        accountRevisions: {},
        failoverPolicy: "off" as const,
      }
    : {
        revision,
        mode: "managed" as const,
        allowedRealmIds: [realm],
        preferredAccountId: account.id,
        candidateAccountIds: [account.id],
        accountRevisions: { [account.id]: account.revision },
        failoverPolicy: "off" as const,
      };
}

function sameAccountPolicy(
  current: EnvironmentClientEndpoint["protocolPlans"][number]["upstreamPlan"]["routes"][number]["accountPolicy"],
  desired: ReturnType<typeof accountPolicy>,
): boolean {
  return (
    current.mode === desired.mode &&
    current.preferredAccountId === desired.preferredAccountId &&
    current.failoverPolicy === desired.failoverPolicy &&
    sameStrings(current.allowedRealmIds, desired.allowedRealmIds) &&
    sameStrings(current.candidateAccountIds, desired.candidateAccountIds) &&
    sameRevisions(current.accountRevisions, desired.accountRevisions)
  );
}

function sameStrings(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function sameRevisions(
  left: Readonly<Record<string, number>>,
  right: Readonly<Record<string, number>>,
): boolean {
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  return (
    sameStrings(leftKeys.sort(), rightKeys.sort()) &&
    leftKeys.every((key) => left[key] === right[key])
  );
}

import { describe, expect, it } from "vitest";
import { assignRouteAccount } from "../src/environment-editing.ts";
import type {
  EnvironmentClientEndpoint,
  ProviderAccountRecord,
} from "../src/control-types.ts";

const anthropicWork: ProviderAccountRecord = {
  id: "anthropic-work",
  displayName: "Anthropic Work",
  upstreamEndpointId: "target.claude.official",
  kind: "anthropic_api_key",
  realmId: "anthropic.official",
  state: "active",
  revision: 4,
  credentialState: "ready",
  credentialEpoch: 2,
};

const anthropicBackup: ProviderAccountRecord = {
  ...anthropicWork,
  id: "anthropic-backup",
  displayName: "Anthropic Backup",
  revision: 7,
};

function passthroughEndpoint(): EnvironmentClientEndpoint {
  return {
    id: "endpoint.claude.official",
    revision: 1,
    clientOrigin: "https://api.anthropic.com",
    protocolPlans: [
      {
        id: "plan.claude.official",
        revision: 1,
        clientProtocol: "anthropic_messages",
        clientAdapterPolicy: { id: "adapter.claude.official", revision: 1 },
        mode: "original_passthrough",
        upstreamPlan: {
          routes: [
            {
              id: "route.claude.official",
              revision: 1,
              providerTarget: {
                id: "target.claude.official",
                revision: 1,
                origin: "https://api.anthropic.com",
                realmId: "anthropic.official",
                capabilities: ["messages", "streaming", "tool_calls"],
              },
              backendProtocol: "anthropic_messages",
              accountPolicy: {
                revision: 1,
                mode: "client_passthrough",
                preferredAccountId: "",
                candidateAccountIds: [],
                accountRevisions: {},
                failoverPolicy: "off",
              },
              modelPolicy: { revision: 1, mode: "preserve", fixedModel: "" },
              wireProfileRef: "follow-client",
              pluginBindings: [],
            },
          ],
          defaultRouteId: "route.claude.official",
          routeSet: {
            id: "routes.claude.official",
            revision: 1,
            candidateRouteIds: ["route.claude.official"],
          },
        },
        pluginBindings: [],
      },
    ],
  };
}

function assign(
  endpoints: readonly EnvironmentClientEndpoint[],
  account?: ProviderAccountRecord,
) {
  return assignRouteAccount(
    endpoints,
    "endpoint.claude.official",
    "plan.claude.official",
    "route.claude.official",
    account,
  );
}

describe("Environment route account editing", () => {
  it("atomically advances the full parent chain when enabling a managed account", () => {
    const result = assign([passthroughEndpoint()], anthropicWork);
    const endpoint = result[0]!;
    const plan = endpoint.protocolPlans[0]!;
    const route = plan.upstreamPlan.routes[0]!;

    expect(endpoint.revision).toBe(2);
    expect(plan.revision).toBe(2);
    expect(plan.mode).toBe("managed");
    expect(route.revision).toBe(2);
    expect(route.accountPolicy).toEqual({
      revision: 2,
      mode: "managed",
      preferredAccountId: "anthropic-work",
      candidateAccountIds: ["anthropic-work"],
      accountRevisions: { "anthropic-work": 4 },
      failoverPolicy: "off",
    });
    expect(route.modelPolicy).toEqual({
      revision: 2,
      mode: "passthrough",
      fixedModel: "",
    });
  });

  it("changes managed accounts without inventing a model-policy revision", () => {
    const first = assign([passthroughEndpoint()], anthropicWork);
    const result = assign(first, anthropicBackup);
    const endpoint = result[0]!;
    const plan = endpoint.protocolPlans[0]!;
    const route = plan.upstreamPlan.routes[0]!;

    expect(endpoint.revision).toBe(3);
    expect(plan.revision).toBe(3);
    expect(route.revision).toBe(3);
    expect(route.accountPolicy.revision).toBe(3);
    expect(route.accountPolicy.preferredAccountId).toBe("anthropic-backup");
    expect(route.accountPolicy.accountRevisions).toEqual({
      "anthropic-backup": 7,
    });
    expect(route.modelPolicy.revision).toBe(2);
  });

  it("does not advance revisions when the selected account is unchanged", () => {
    const first = assign([passthroughEndpoint()], anthropicWork);
    const result = assign(first, anthropicWork);

    expect(result[0]).toBe(first[0]);
  });

  it("restores the identity-preserving route when returning to client login", () => {
    const first = assign([passthroughEndpoint()], anthropicWork);
    const result = assign(first);
    const endpoint = result[0]!;
    const plan = endpoint.protocolPlans[0]!;
    const route = plan.upstreamPlan.routes[0]!;

    expect(endpoint.revision).toBe(3);
    expect(plan.revision).toBe(3);
    expect(plan.mode).toBe("original_passthrough");
    expect(route.revision).toBe(3);
    expect(route.accountPolicy).toEqual({
      revision: 3,
      mode: "client_passthrough",
      preferredAccountId: "",
      candidateAccountIds: [],
      accountRevisions: {},
      failoverPolicy: "off",
    });
    expect(route.modelPolicy).toEqual({
      revision: 3,
      mode: "preserve",
      fixedModel: "",
    });
  });
});

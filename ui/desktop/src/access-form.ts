import type { AccessApplyInput } from "./control-types.ts";

export interface AccessFormValues {
  readonly accessId: string;
  readonly name: string;
  readonly description: string;
  readonly expectedRevision: string;
  readonly clientOrigin: string;
  readonly providerOrigin: string;
  readonly fixedModel: string;
}

export interface CredentialCoordinates {
  readonly profileId: string;
  readonly credentialId: string;
}

export const initialAccessForm: AccessFormValues = {
  accessId: "",
  name: "",
  description: "",
  expectedRevision: "0",
  clientOrigin: "https://api.anthropic.com",
  providerOrigin: "https://model8.run",
  fixedModel: "gpt-4.1-mini",
};

export function validAccessForm(values: AccessFormValues): boolean {
  const revision = Number(values.expectedRevision);
  return (
    values.accessId.trim() === values.accessId &&
    values.accessId.length > 0 &&
    values.name.trim() === values.name &&
    values.name.length > 0 &&
    Number.isSafeInteger(revision) &&
    revision >= 0 &&
    values.clientOrigin.length > 0 &&
    values.providerOrigin.length > 0 &&
    values.fixedModel.length > 0
  );
}

export function buildAccessApplyInput(
  values: AccessFormValues,
): AccessApplyInput {
  if (!validAccessForm(values)) {
    throw new Error("Access form is incomplete");
  }
  const prefix = values.accessId;
  const endpointId = `${prefix}-agent`;
  const { profileId, credentialId: accountId } =
    credentialCoordinates(values);
  const targetId = `${prefix}-target`;
  const routeSetId = `${prefix}-routes`;
  const egressPolicyId = `${prefix}-egress`;
  return {
    expectedRevision: Number(values.expectedRevision),
    access: {
      id: prefix,
      name: values.name,
      description: values.description,
      status: "enabled",
      agentEndpointId: endpointId,
      defaultRouteSetId: routeSetId,
      profileIds: [profileId],
      egressPolicyId,
    },
    agentEndpoint: {
      id: endpointId,
      clientOrigin: values.clientOrigin,
      clientDialect: "anthropic-messages",
    },
    profiles: [
      {
        id: profileId,
        name: values.name,
        description: values.description,
        backendDialect: "openai-chat",
        targetId,
        transportProfileRef: "observed-client-strict-h1",
        defaultModelPolicy: {
          mode: "fixed",
          fixedModel: values.fixedModel,
        },
        accountBindingIds: [accountId],
        defaultAccountBindingId: accountId,
      },
    ],
    providerTargets: [
      {
        id: targetId,
        profileId,
        origin: values.providerOrigin,
        protocol: "openai-chat",
        capabilities: ["messages", "streaming", "tool_calls"],
      },
    ],
    accountBindings: [
      {
        id: accountId,
        profileId,
        label: values.name,
        secretRef: `secret://provider/${accountId}`,
        authDriverRef: "static_header",
        enabled: true,
      },
    ],
    routeSets: [
      {
        id: routeSetId,
        candidateProfileIds: [profileId],
      },
    ],
    egressPolicy: {
      id: egressPolicyId,
      mode: "direct",
    },
    pluginPlan: {
      mode: "pass_through",
      bindingIds: [],
    },
  };
}

export function credentialCoordinates(
  values: Pick<AccessFormValues, "accessId">,
): CredentialCoordinates {
  return {
    profileId: `${values.accessId}-openai`,
    credentialId: `${values.accessId}-account`,
  };
}

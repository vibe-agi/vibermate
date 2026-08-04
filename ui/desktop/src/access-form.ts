import type { AccessApplyInput } from "./control-types.ts";

export interface AccessFormValues {
  readonly accessId: string;
  readonly name: string;
  readonly description: string;
  readonly expectedRevision: string;
  readonly clientOrigin: string;
  readonly clientDialect: "anthropic-messages" | "openai-responses";
  readonly providerDialect: "anthropic-messages" | "openai-chat";
  readonly authDriverRef: "anthropic_api_key" | "static_header";
  readonly providerOrigin: string;
  readonly fixedModel: string;
  readonly routeName: string;
  readonly upstreamPresentation: "follow-client" | "claude-code";
}

export type AccessAppPreset = "claude" | "codex" | "custom";

export const accessAppPresetDefaults = {
  claude: {
    clientOrigin: "https://api.anthropic.com",
    clientDialect: "anthropic-messages",
  },
  codex: {
    clientOrigin: "https://api.openai.com",
    clientDialect: "openai-responses",
  },
  custom: {
    clientOrigin: "",
    clientDialect: "anthropic-messages",
  },
} as const satisfies Record<
  AccessAppPreset,
  Pick<AccessFormValues, "clientDialect" | "clientOrigin">
>;

export function applyAccessAppPreset(
  values: AccessFormValues,
  preset: AccessAppPreset,
): AccessFormValues {
  return {
    ...values,
    ...accessAppPresetDefaults[preset],
  };
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
  clientDialect: "anthropic-messages",
  providerDialect: "openai-chat",
  authDriverRef: "static_header",
  providerOrigin: "http://127.0.0.1:23333/v1",
  fixedModel: "dashscope:glm-5",
  routeName: "Primary account",
  upstreamPresentation: "follow-client",
};

export function newAccessForm(
  client: Partial<
    Pick<AccessFormValues, "clientDialect" | "clientOrigin">
  > = {},
): AccessFormValues {
  return {
    ...initialAccessForm,
    ...client,
    accessId: `access-${globalThis.crypto.randomUUID()}`,
  };
}

export function validAccessForm(values: AccessFormValues): boolean {
  const revision = Number(values.expectedRevision);
  return (
    values.accessId.trim() === values.accessId &&
    values.accessId.length > 0 &&
    values.name.trim() === values.name &&
    values.name.length > 0 &&
    Number.isSafeInteger(revision) &&
    revision >= 0 &&
    clientOriginIdentity(values.clientOrigin) !== undefined &&
    (values.clientDialect === "anthropic-messages" ||
      values.clientDialect === "openai-responses") &&
    (values.providerDialect === "anthropic-messages" ||
      values.providerDialect === "openai-chat") &&
    (values.authDriverRef === "anthropic_api_key" ||
      values.authDriverRef === "static_header") &&
    validProviderOrigin(values.providerOrigin) &&
    values.fixedModel.length > 0 &&
    values.routeName.trim() === values.routeName &&
    values.routeName.length > 0 &&
    (values.upstreamPresentation === "follow-client" ||
      values.upstreamPresentation === "claude-code")
  );
}

function validProviderOrigin(value: string): boolean {
  try {
    const parsed = new URL(value.trim());
    const loopbackHTTP =
      parsed.protocol === "http:" &&
      (parsed.hostname === "::1" ||
        parsed.hostname === "[::1]" ||
        (() => {
          const octets = parsed.hostname.split(".");
          return (
            octets.length === 4 &&
            octets[0] === "127" &&
            octets.every(
              (octet) =>
                /^\d{1,3}$/u.test(octet) && Number(octet) <= 255,
            )
          );
        })());
    return (
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      (parsed.protocol === "https:" || loopbackHTTP)
    );
  } catch {
    return false;
  }
}

export function clientOriginIdentity(value: string): string | undefined {
  try {
    const parsed = new URL(value);
    const port = parsed.port === "" ? 443 : Number(parsed.port);
    const valid =
      parsed.protocol === "https:" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      parsed.pathname === "/" &&
      parsed.hostname.length > 0 &&
      value.trim() === value &&
      Number.isInteger(port) &&
      port > 0 &&
      port <= 65_535;
    return valid ? `${parsed.hostname.toLowerCase()}:${port}` : undefined;
  } catch {
    return undefined;
  }
}

export function canonicalClientOrigin(value: string): string | undefined {
  if (clientOriginIdentity(value) === undefined) {
    return undefined;
  }
  return new URL(value).origin;
}

export function buildAccessApplyInput(
  values: AccessFormValues,
): AccessApplyInput {
  if (!validAccessForm(values)) {
    throw new Error("Access form is incomplete");
  }
  const clientOrigin = canonicalClientOrigin(values.clientOrigin);
  if (clientOrigin === undefined) {
    throw new Error("Client API address is invalid");
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
      clientOrigin,
      clientDialect: values.clientDialect,
    },
    profiles: [
      {
        id: profileId,
        name: values.routeName,
        description: "",
        backendDialect: values.providerDialect,
        targetId,
        upstreamWireProfileRef: values.upstreamPresentation,
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
        origin: values.providerOrigin.trim().replace(/\/+$/u, ""),
        protocol: values.providerDialect,
        capabilities: ["messages", "streaming", "tool_calls"],
      },
    ],
    accountBindings: [
      {
        id: accountId,
        profileId,
        label: values.routeName,
        secretRef: `secret://provider/${accountId}`,
        authDriverRef: values.authDriverRef,
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

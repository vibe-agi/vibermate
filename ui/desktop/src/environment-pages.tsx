import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { type FormEvent, type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ControlProblem,
  type ControlClient,
} from "./control-client.ts";
import {
  EmptyState,
  InlineProblem,
  LoadingRows,
  PageHeading,
  SectionHeading,
  useDashboardModel,
} from "./App.tsx";
import { BrandIcon } from "./brand-icons.tsx";
import { controlErrorKey, dashboardQueryKeys } from "./dashboard-runtime.ts";
import { dashboardTaskRoutePaths } from "./navigation.ts";
import type {
  ClientProtocol,
  EnvironmentClientEndpoint,
  EnvironmentCompatibility,
  EnvironmentDraftInput,
  EnvironmentContentRecordingMode,
  EnvironmentImpact,
  EnvironmentRecord,
  ProviderAccountRecord,
} from "./control-types.ts";

type EnvironmentTemplate = "claude" | "codex";

export function EnvironmentsRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const [creating, setCreating] = useState(false);
  const environments = useQuery({
    queryKey: dashboardQueryKeys.environments,
    queryFn: ({ signal }) => model.client.environments(signal),
    placeholderData: (previous) => previous,
  });
  return (
    <div className="page environments-page">
      <PageHeading
        actions={<button className="primary-action" onClick={() => setCreating(true)} type="button">{t("environments.add")}</button>}
        description={t("environments.description")}
        title={t("environments.title")}
      />
      <section className="data-panel">
        {environments.isPending && environments.data === undefined ? <LoadingRows /> :
          environments.isError && environments.data === undefined ? <InlineProblem message={t(controlErrorKey(environments.error))} /> :
            environments.data?.items.length === 0 ? <EmptyState description={t("environments.empty.description")} title={t("environments.empty.title")} /> :
              <div className="environment-grid">{environments.data?.items.map((environment) => <EnvironmentCard environment={environment} key={environment.id} />)}</div>}
      </section>
      {creating && <NewEnvironmentDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

function EnvironmentCard({ environment }: { readonly environment: EnvironmentRecord }) {
  const { t } = useTranslation();
  const endpointCount = environment.clientEndpoints.length;
  const routeCount = countRoutes(environment);
  return (
    <article className={`environment-card${environment.systemOwned ? " system" : ""}`}>
      <div className="environment-card-title"><div><h2>{environment.name}</h2><p>{environment.systemOwned ? t("environments.system") : environment.id}</p></div><span className={`state-badge state-${environment.state}`}>{t(`environments.state.${environment.state}`)}</span></div>
      <div className="environment-card-metrics"><span><strong>{endpointCount}</strong>{t("environments.endpoints", { count: endpointCount })}</span><span><strong>{routeCount}</strong>{t("environments.routes", { count: routeCount })}</span><span><strong>r{environment.revision}</strong>{t("environments.revision")}</span></div>
      <p className="environment-card-copy">{environment.systemOwned ? t("environments.transparent.description") : endpointCount === 0 ? t("environments.observe.description") : t("environments.semantic.description")}</p>
      <Link className="full-row-action" params={{ environmentId: environment.id }} search={{}} to={dashboardTaskRoutePaths.environmentDetail}>{environment.systemOwned ? t("environments.inspect") : t("environments.open")}</Link>
    </article>
  );
}

export function EnvironmentDetailRoutePage({
  environmentId,
  revision,
}: {
  readonly environmentId: string;
  readonly revision?: number;
}) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const environment = useQuery({
    queryKey: revision === undefined ? dashboardQueryKeys.environment(environmentId) : [...dashboardQueryKeys.environment(environmentId), "revision", revision],
    queryFn: ({ signal }) => revision === undefined ? model.client.environment(environmentId, signal) : model.client.environmentRevision(environmentId, revision, signal),
  });
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState<string>();
  const [state, setState] = useState<"active" | "disabled">();
  const [endpoints, setEndpoints] = useState<readonly EnvironmentClientEndpoint[]>();
	const [recordingMode, setRecordingMode] = useState<EnvironmentContentRecordingMode>();
	const [retentionDays, setRetentionDays] = useState<number>();
  const [impact, setImpact] = useState<EnvironmentImpact>();
  const [draftRevision, setDraftRevision] = useState<number>();
  const [errorKey, setErrorKey] = useState<string>();
  const accounts = useQuery({
    queryKey: dashboardQueryKeys.accounts,
    queryFn: ({ signal }) => model.client.providerAccounts(signal),
    placeholderData: (previous) => previous,
  });

  const current = environment.data;
  const candidateName = name ?? current?.name ?? "";
  const candidateState = state ?? current?.state ?? "active";
  const candidateEndpoints = endpoints ?? current?.clientEndpoints ?? [];
	const candidateRecordingMode = recordingMode ?? current?.contentRecording.mode ?? "full";
	const candidateRetentionDays = candidateRecordingMode === "off" ? 0 : retentionDays ?? current?.contentRecording.retentionDays ?? 30;
  const saveDraft = useMutation({
    mutationFn: async () => {
      if (current === undefined || current.systemOwned) throw new Error("Environment is not editable");
      const expectedDraftRevision = await currentDraftRevision(
        model.client,
        current.id,
      );
      const input: EnvironmentDraftInput = {
        expectedDraftRevision,
        name: candidateName.trim(),
        state: candidateState,
        clientEndpoints: candidateEndpoints,
        pluginBindings: current.pluginBindings,
        budgetPolicy: current.budgetPolicy,
        egressPolicy: current.egressPolicy,
		contentRecording: { mode: candidateRecordingMode, retentionDays: candidateRetentionDays },
      };
      const draft = await model.client.saveEnvironmentDraft(current.id, current.revision, input);
      const preview = await model.client.previewEnvironmentDraft(current.id, draft.draftRevision);
      return { draft, preview };
    },
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: ({ draft, preview }) => { setDraftRevision(draft.draftRevision); setImpact(preview); setErrorKey(undefined); },
  });
  const publish = useMutation({
    mutationFn: async () => {
      if (draftRevision === undefined) throw new Error("Environment draft is unavailable");
      return model.client.publishEnvironmentDraft(environmentId, draftRevision);
    },
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (result) => {
      queryClient.setQueryData(dashboardQueryKeys.environment(environmentId), result.environment);
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.environments });
      setImpact(undefined); setDraftRevision(undefined); setEditing(false); setName(undefined); setState(undefined); setEndpoints(undefined); setRecordingMode(undefined); setRetentionDays(undefined); setErrorKey(undefined);
    },
  });

  if (environment.isPending) return <div className="page"><LoadingRows count={8} /></div>;
  if (current === undefined) return <div className="page"><InlineProblem message={t(controlErrorKey(environment.error))} /></div>;
  const historical = revision !== undefined;

  return (
    <div className="page environment-detail-page">
      <PageHeading
        actions={!current.systemOwned && !historical ? <button className={editing ? "quiet-button" : "primary-action"} onClick={() => setEditing((value) => !value)} type="button">{editing ? t("common.cancel") : t("environments.edit")}</button> : undefined}
        description={historical ? t("environmentDetail.historical", { revision }) : current.id}
        eyebrow={t("environmentDetail.eyebrow")}
        title={current.name}
      />
      {editing && <section className="data-panel environment-editor"><SectionHeading title={t("environmentDetail.edit.title")} /><div className="compact-form-row"><label><span>{t("environmentDetail.name")}</span><input maxLength={256} onChange={(event) => setName(event.target.value)} value={candidateName} /></label><label><span>{t("environmentDetail.state")}</span><select onChange={(event) => setState(event.target.value as "active" | "disabled")} value={candidateState}><option value="active">{t("environments.state.active")}</option><option value="disabled">{t("environments.state.disabled")}</option></select></label><label><span>{t("environmentDetail.recording.mode")}</span><select onChange={(event) => setRecordingMode(event.target.value as EnvironmentContentRecordingMode)} value={candidateRecordingMode}><option value="full">{t("environmentDetail.recording.full")}</option><option value="metadata_only">{t("environmentDetail.recording.metadata")}</option><option value="off">{t("environmentDetail.recording.off")}</option></select></label>{candidateRecordingMode !== "off" && <label><span>{t("environmentDetail.recording.retention")}</span><input max={3650} min={1} onChange={(event) => setRetentionDays(Number(event.target.value))} type="number" value={candidateRetentionDays} /></label>}</div><p className="field-help">{t("environmentDetail.recording.disclosure")}</p><div className="template-actions"><span>{t("environmentDetail.endpoints.add")}</span><button onClick={() => setEndpoints(addEndpoint(candidateEndpoints, "claude"))} type="button"><BrandIcon name="claude-code" />{t("environmentDetail.endpoints.claude")}</button><button onClick={() => setEndpoints(addEndpoint(candidateEndpoints, "codex"))} type="button"><BrandIcon name="codex" />{t("environmentDetail.endpoints.codex")}</button></div><RouteAccountSelectors accounts={accounts.data?.items ?? []} endpoints={candidateEndpoints} onChange={setEndpoints} />{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<div className="editor-actions"><button className="primary-action" disabled={saveDraft.isPending || candidateName.trim().length === 0} onClick={() => saveDraft.mutate()} type="button">{saveDraft.isPending ? t("common.checking") : t("environmentDetail.review")}</button></div></section>}

      <section className="data-panel plan-panel">
        <SectionHeading title={t("environmentDetail.plan.title")} />
        {current.clientEndpoints.length === 0 ? <EmptyState description={current.systemOwned ? t("environments.transparent.description") : t("environmentDetail.plan.empty.description")} title={t("environmentDetail.plan.empty.title")} /> : <div className="endpoint-list">{current.clientEndpoints.map((endpoint) => <EndpointView endpoint={endpoint} key={endpoint.id} />)}</div>}
      </section>

      <div className="detail-grid">
        <section className="data-panel"><SectionHeading title={t("environmentDetail.boundaries.title")} /><dl className="facts-list"><dt>{t("environmentDetail.boundaries.recording")}</dt><dd>{t(`environmentDetail.recording.${current.contentRecording.mode === "metadata_only" ? "metadata" : current.contentRecording.mode}`)}{current.contentRecording.mode === "off" ? "" : ` · ${t("environmentDetail.recording.days", { count: current.contentRecording.retentionDays })}`}</dd><dt>{t("environmentDetail.boundaries.budget")}</dt><dd>{current.budgetPolicy.id || t("common.default")}</dd><dt>{t("environmentDetail.boundaries.egress")}</dt><dd>{current.egressPolicy.mode || t("common.default")}</dd><dt>{t("environmentDetail.boundaries.plugins")}</dt><dd>{t("common.count", { count: current.pluginBindings.length })}</dd></dl></section>
        <section className="data-panel"><SectionHeading title={t("environmentDetail.evidence.title")} /><dl className="facts-list"><dt>{t("environments.revision")}</dt><dd>{current.revision}</dd><dt>{t("environmentDetail.digest")}</dt><dd><code title={current.digest}>{shortDigest(current.digest)}</code></dd><dt>{t("environmentDetail.systemOwned")}</dt><dd>{current.systemOwned ? t("common.yes") : t("common.no")}</dd></dl></section>
      </div>

      {impact !== undefined && <ImpactDialog impact={impact} onCancel={() => setImpact(undefined)} onPublish={() => publish.mutate()} publishing={publish.isPending} />}
    </div>
  );
}

function EndpointView({ endpoint }: { readonly endpoint: EnvironmentClientEndpoint }) {
  const { t } = useTranslation();
  return <article className="endpoint-card"><header><div><p className="eyebrow">{t("environmentDetail.endpoint")}</p><h3>{endpoint.clientOrigin}</h3></div><span>r{endpoint.revision}</span></header>{endpoint.protocolPlans.map((plan) => <div className="protocol-row" key={plan.id}><div><strong>{protocolLabel(plan.clientProtocol, t)}</strong><small>{plan.mode === "managed" ? t("environmentDetail.mode.managed") : t("environmentDetail.mode.passthrough")}</small></div><span aria-hidden="true" className="route-arrow">→</span><div className="route-stack">{plan.upstreamPlan.routes.map((route) => <div key={route.id}><strong>{route.providerTarget.origin}</strong><small>{route.accountPolicy.mode === "client_passthrough" ? t("environmentDetail.account.client") : t("environmentDetail.account.managed")}</small></div>)}</div></div>)}</article>;
}

function NewEnvironmentDialog({ onClose }: { readonly onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [id, setID] = useState("");
  const [template, setTemplate] = useState<EnvironmentTemplate>("claude");
  const [accountID, setAccountID] = useState("");
	const [recordingMode, setRecordingMode] = useState<EnvironmentContentRecordingMode>("full");
  const [impact, setImpact] = useState<EnvironmentImpact>();
  const [draftRevision, setDraftRevision] = useState<number>();
  const [errorKey, setErrorKey] = useState<string>();
  const accounts = useQuery({
    queryKey: dashboardQueryKeys.accounts,
    queryFn: ({ signal }) => model.client.providerAccounts(signal),
    placeholderData: (previous) => previous,
  });
  const canonicalID = id.trim() || slug(name);
  const compatibleAccounts = compatibleProviderAccounts(accounts.data?.items ?? [], template);
  const selectedAccount = compatibleAccounts.find((account) => account.id === accountID);
  const input = useMemo(() => newEnvironmentInput(name.trim(), template, selectedAccount, recordingMode), [name, selectedAccount, template, recordingMode]);
  const review = useMutation({
    mutationFn: async () => {
      const expectedDraftRevision = await currentDraftRevision(
        model.client,
        canonicalID,
      );
      const draft = await model.client.saveEnvironmentDraft(canonicalID, 0, {
        ...input,
        expectedDraftRevision,
      });
      const preview = await model.client.previewEnvironmentDraft(canonicalID, draft.draftRevision);
      return { draft, preview };
    },
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: ({ draft, preview }) => { setDraftRevision(draft.draftRevision); setImpact(preview); setErrorKey(undefined); },
  });
  const publish = useMutation({
    mutationFn: () => model.client.publishEnvironmentDraft(canonicalID, draftRevision ?? 0),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.environments });
      onClose();
      void navigate({ params: { environmentId: result.environment.id }, search: {}, to: dashboardTaskRoutePaths.environmentDetail });
    },
  });
  const submit = (event: FormEvent) => { event.preventDefault(); if (name.trim() !== "" && canonicalID !== "") review.mutate(); };
  const chooseTemplate = (next: EnvironmentTemplate) => { setTemplate(next); setAccountID(""); };
  return <div className="modal-backdrop"><section aria-labelledby="new-environment-title" aria-modal="true" className="modal environment-modal" role="dialog"><header><div><p className="eyebrow">{t("environments.new.eyebrow")}</p><h2 id="new-environment-title">{t("environments.new.title")}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><form onSubmit={submit}><label><span>{t("environmentDetail.name")}</span><input autoFocus maxLength={256} onChange={(event) => setName(event.target.value)} value={name} /></label><label><span>{t("environments.new.id")}</span><input maxLength={128} onChange={(event) => setID(event.target.value)} placeholder={slug(name)} value={id} /></label><fieldset><legend>{t("environments.new.template")}</legend><div className="template-picker"><TemplateChoice active={template === "claude"} description={t("environments.new.claude.description")} icon={<BrandIcon name="claude-code" />} label={t("environments.new.claude")} onClick={() => chooseTemplate("claude")} /><TemplateChoice active={template === "codex"} description={t("environments.new.codex.description")} icon={<BrandIcon name="codex" />} label={t("environments.new.codex")} onClick={() => chooseTemplate("codex")} /></div></fieldset><label><span>{t("environments.new.account")}</span><select onChange={(event) => setAccountID(event.target.value)} value={accountID}><option value="">{t("environmentDetail.account.client")}</option>{compatibleAccounts.map((account) => <option key={account.id} value={account.id}>{account.displayName}</option>)}</select><small className="field-help">{compatibleAccounts.length === 0 ? t("environments.new.account.none") : t("environments.new.account.help")}</small></label><label><span>{t("environmentDetail.recording.mode")}</span><select onChange={(event) => setRecordingMode(event.target.value as EnvironmentContentRecordingMode)} value={recordingMode}><option value="full">{t("environmentDetail.recording.full")}</option><option value="metadata_only">{t("environmentDetail.recording.metadata")}</option><option value="off">{t("environmentDetail.recording.off")}</option></select><small className="field-help">{t("environmentDetail.recording.disclosure")}</small></label>{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={review.isPending || name.trim() === "" || canonicalID === ""} type="submit">{t("environmentDetail.review")}</button></footer></form>{impact !== undefined && <ImpactDialog impact={impact} onCancel={() => setImpact(undefined)} onPublish={() => publish.mutate()} publishing={publish.isPending} />}</section></div>;
}

function TemplateChoice({ active, description, icon, label, onClick }: { readonly active: boolean; readonly description: string; readonly icon?: ReactNode; readonly label: string; readonly onClick: () => void }) {
  return <button aria-pressed={active} className="template-choice" onClick={onClick} type="button"><span>{icon}<strong>{label}</strong></span><small>{description}</small></button>;
}

function ImpactDialog({ impact, onCancel, onPublish, publishing }: { readonly impact: EnvironmentImpact; readonly onCancel: () => void; readonly onPublish: () => void; readonly publishing: boolean }) {
  const { t } = useTranslation();
  return <div className="impact-review" role="group" aria-label={t("environmentImpact.title")}><header><div><p className="eyebrow">{t("environmentImpact.eyebrow")}</p><h3>{t("environmentImpact.title")}</h3></div><CompatibilityBadge value={impact.classification} /></header><div className="impact-counts"><span><strong>{impact.hotSwitchCount}</strong>{t("environmentImpact.hot")}</span><span><strong>{impact.reconnectRequiredCount}</strong>{t("environmentImpact.reconnect")}</span><span><strong>{impact.restartRequiredCount}</strong>{t("environmentImpact.restart")}</span></div><p>{t(`environmentImpact.description.${impact.classification}`)}</p><footer><button onClick={onCancel} type="button">{t("common.back")}</button><button className="primary-action" disabled={publishing} onClick={onPublish} type="button">{publishing ? t("common.publishing") : t("environmentImpact.publish")}</button></footer></div>;
}

function CompatibilityBadge({ value }: { readonly value: EnvironmentCompatibility }) { const { t } = useTranslation(); return <span className={`compatibility-badge compatibility-${value}`}>{t(`environmentImpact.${value}`)}</span>; }

function RouteAccountSelectors({ accounts, endpoints, onChange }: { readonly accounts: readonly ProviderAccountRecord[]; readonly endpoints: readonly EnvironmentClientEndpoint[]; readonly onChange: (value: readonly EnvironmentClientEndpoint[]) => void }) {
  const { t } = useTranslation();
  return <div className="route-account-selectors">{endpoints.flatMap((endpoint) => endpoint.protocolPlans.flatMap((plan) => plan.upstreamPlan.routes.map((route) => {
    const compatible = compatibleAccountsForRealm(accounts, route.providerTarget.realmId);
    return <label key={`${endpoint.id}:${plan.id}:${route.id}`}><span>{t("environmentDetail.account.route", { origin: route.providerTarget.origin })}</span><select onChange={(event) => onChange(setRouteAccount(endpoints, endpoint.id, plan.id, route.id, compatible.find((account) => account.id === event.target.value)))} value={route.accountPolicy.preferredAccountId}><option value="">{t("environmentDetail.account.client")}</option>{compatible.map((account) => <option disabled={account.state !== "active" || account.credentialState !== "ready"} key={account.id} value={account.id}>{account.displayName}</option>)}</select></label>;
  })))}</div>;
}

function newEnvironmentInput(name: string, template: EnvironmentTemplate, account: ProviderAccountRecord | undefined, recordingMode: EnvironmentContentRecordingMode): EnvironmentDraftInput {
  return { expectedDraftRevision: 0, name, state: "active", clientEndpoints: [endpointTemplate(template, account)], pluginBindings: [], budgetPolicy: { id: "", revision: 0 }, egressPolicy: { id: "", revision: 0, mode: "" }, contentRecording: { mode: recordingMode, retentionDays: recordingMode === "off" ? 0 : 30 } };
}

function addEndpoint(current: readonly EnvironmentClientEndpoint[], template: EnvironmentTemplate): readonly EnvironmentClientEndpoint[] {
  const candidate = endpointTemplate(template);
  return current.some((item) => item.clientOrigin === candidate.clientOrigin) ? current : [...current, candidate];
}

function endpointTemplate(template: EnvironmentTemplate, account?: ProviderAccountRecord): EnvironmentClientEndpoint {
  const codex = template === "codex";
  const token = codex ? "codex" : "claude";
  const origin = codex ? "https://chatgpt.com" : "https://api.anthropic.com";
  const protocol: ClientProtocol = codex ? "openai_responses" : "anthropic_messages";
  const realm = codex ? "openai.chatgpt" : "anthropic.official";
  const routeId = `route.${token}.official`;
  return { id: `endpoint.${token}.official`, revision: 1, clientOrigin: origin, protocolPlans: [{ id: `plan.${token}.official`, revision: 1, clientProtocol: protocol, clientAdapterPolicy: { id: `adapter.${token}.official`, revision: 1 }, mode: account === undefined ? "original_passthrough" : "managed", upstreamPlan: { routes: [{ id: routeId, revision: 1, providerTarget: { id: `target.${token}.official`, revision: 1, origin, realmId: realm, capabilities: ["messages", "streaming", "tool_calls"] }, backendProtocol: protocol, accountPolicy: accountPolicy(realm, account), modelPolicy: { revision: 1, mode: account === undefined ? "preserve" : "passthrough", fixedModel: "" }, wireProfileRef: "follow-client", pluginBindings: [] }], defaultRouteId: routeId, routeSet: { id: `routes.${token}.official`, revision: 1, candidateRouteIds: [routeId] } }, pluginBindings: [] }] };
}

function accountPolicy(realm: string, account?: ProviderAccountRecord) {
  return account === undefined ? { revision: 1, mode: "client_passthrough" as const, allowedRealmIds: [realm], preferredAccountId: "", candidateAccountIds: [], accountRevisions: {}, failoverPolicy: "off" as const } : { revision: 1, mode: "managed" as const, allowedRealmIds: [realm], preferredAccountId: account.id, candidateAccountIds: [account.id], accountRevisions: { [account.id]: account.revision }, failoverPolicy: "off" as const };
}

function compatibleProviderAccounts(accounts: readonly ProviderAccountRecord[], template: EnvironmentTemplate): readonly ProviderAccountRecord[] {
  return compatibleAccountsForRealm(accounts, template === "claude" ? "anthropic.official" : "openai.chatgpt").filter((account) => account.state === "active" && account.credentialState === "ready");
}

function compatibleAccountsForRealm(accounts: readonly ProviderAccountRecord[], realm: string): readonly ProviderAccountRecord[] {
  return accounts.filter((account) => account.realmId === realm);
}

function setRouteAccount(endpoints: readonly EnvironmentClientEndpoint[], endpointID: string, planID: string, routeID: string, account?: ProviderAccountRecord): readonly EnvironmentClientEndpoint[] {
  return endpoints.map((endpoint) => endpoint.id !== endpointID ? endpoint : { ...endpoint, protocolPlans: endpoint.protocolPlans.map((plan) => plan.id !== planID ? plan : { ...plan, upstreamPlan: { ...plan.upstreamPlan, routes: plan.upstreamPlan.routes.map((route) => route.id !== routeID ? route : { ...route, revision: route.revision + 1, accountPolicy: { ...accountPolicy(route.providerTarget.realmId, account), revision: route.accountPolicy.revision + 1 } }) } }) });
}

function protocolLabel(protocol: ClientProtocol, t: (key: string) => string): string { return t(`environmentDetail.protocol.${protocol}`); }
function countRoutes(environment: EnvironmentRecord): number { return environment.clientEndpoints.reduce((total, endpoint) => total + endpoint.protocolPlans.reduce((plans, plan) => plans + plan.upstreamPlan.routes.length, 0), 0); }
function shortDigest(value: string): string { return value.length <= 18 ? value : `${value.slice(0, 10)}…${value.slice(-6)}`; }
function slug(value: string): string { return value.toLocaleLowerCase("en-US").normalize("NFKD").replace(/[^a-z0-9._-]+/gu, "-").replace(/^-+|-+$/gu, "").slice(0, 64); }

async function currentDraftRevision(
  client: ControlClient,
  environmentId: string,
): Promise<number> {
  try {
    return (await client.environmentDraft(environmentId)).draftRevision;
  } catch (error) {
    if (
      error instanceof ControlProblem &&
      error.reasonCode === "environment_draft_not_found"
    ) {
      return 0;
    }
    throw error;
  }
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  EmptyState,
  InlineProblem,
  LoadingRows,
  PageHeading,
  SectionHeading,
  useDashboardModel,
} from "./App.tsx";
import { controlErrorKey, dashboardQueryKeys } from "./dashboard-runtime.ts";
import { BrandIcon } from "./brand-icons.tsx";
import type {
  ApprovalChoice,
  ApprovalView,
  ConnectionRuleSet,
  ProviderAccountKind,
  ProviderAccountRecord,
} from "./control-types.ts";

type PolicyMode = "monitor" | "ask" | "block";

export function PolicyRoutePage({ selectedApprovalId }: { readonly selectedApprovalId?: string }) {
  const { t, i18n } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [errorKey, setErrorKey] = useState<string>();
  const approvals = useQuery({
    queryKey: dashboardQueryKeys.approvals,
    queryFn: ({ signal }) => model.client.approvals(signal),
    refetchInterval: model.pollInterval,
    placeholderData: (previous) => previous,
  });
  const rules = useQuery({
    queryKey: [...dashboardQueryKeys.root, "connection-rules"],
    queryFn: ({ signal }) => model.client.connectionRules(signal),
    placeholderData: (previous) => previous,
  });
  const replace = useMutation({
    mutationFn: (mode: PolicyMode) => {
      if (rules.data === undefined) throw new Error("Connection rules are unavailable");
      return model.client.replaceConnectionRules(
        rules.data.revision,
        policyInput(rules.data, mode),
      );
    },
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (value) => {
      setErrorKey(undefined);
      queryClient.setQueryData([...dashboardQueryKeys.root, "connection-rules"], value);
    },
  });
  const decide = useMutation({
    mutationFn: ({ approval, choice }: { readonly approval: ApprovalView; readonly choice: ApprovalChoice }) => model.client.decideApproval(approval, choice),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: () => { setErrorKey(undefined); void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.approvals }); },
  });
  const policyMode = modeOf(rules.data);
  const pending = approvals.data?.items ?? [];

  return (
    <div className="page policy-page">
      <PageHeading description={t("policy.description")} title={t("policy.title")} />
      <section className="data-panel policy-mode-panel">
        <div><p className="eyebrow">{t("policy.mode.eyebrow")}</p><h2>{t("policy.mode.title")}</h2><p>{t(`policy.mode.description.${policyMode}`)}</p></div>
        <div aria-label={t("policy.mode.label")} className="segmented-control" role="radiogroup">
          {(["monitor", "ask", "block"] as const).map((mode) => <button aria-checked={policyMode === mode} disabled={replace.isPending || rules.data === undefined} key={mode} onClick={() => replace.mutate(mode)} role="radio" type="button">{t(`policy.mode.${mode}`)}</button>)}
        </div>
      </section>
      {errorKey !== undefined && <InlineProblem message={t(errorKey)} />}
      <section className="data-panel approvals-panel">
        <SectionHeading title={t("policy.approvals.title", { count: pending.length })} />
        {approvals.isPending && approvals.data === undefined ? <LoadingRows count={4} /> : approvals.isError && approvals.data === undefined ? <InlineProblem message={t(controlErrorKey(approvals.error))} /> : pending.length === 0 ? <EmptyState description={t("policy.approvals.empty.description")} title={t("policy.approvals.empty.title")} /> : <div className="approval-list">{pending.map((approval) => <ApprovalRow approval={approval} busy={decide.isPending} highlighted={approval.id === selectedApprovalId} key={approval.id} onDecide={(choice) => decide.mutate({ approval, choice })} locale={i18n.language} />)}</div>}
      </section>
      <section className="data-panel rules-panel">
        <SectionHeading title={t("policy.rules.title")} />
        {rules.data === undefined ? <LoadingRows count={3} /> : rules.data.rules.length === 0 ? <EmptyState description={t("policy.rules.empty.description")} title={t("policy.rules.empty.title")} /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>{t("policy.rules.column.decision")}</th><th>{t("policy.rules.column.target")}</th><th>{t("policy.rules.column.source")}</th></tr></thead><tbody>{rules.data.rules.map((rule) => <tr key={rule.id}><td><span className={`decision decision-${rule.decision}`}>{t(`policy.decision.${rule.decision}`)}</span></td><td>{`${rule.host ?? ""}${rule.port === undefined ? "" : `:${rule.port}`}`}</td><td><code>{rule.id}</code></td></tr>)}</tbody></table></div>}
      </section>
    </div>
  );
}

function ApprovalRow({ approval, busy, highlighted, locale, onDecide }: { readonly approval: ApprovalView; readonly busy: boolean; readonly highlighted: boolean; readonly locale: string; readonly onDecide: (choice: ApprovalChoice) => void }) {
  const { t } = useTranslation();
  return <article className={`approval-row${highlighted ? " highlighted" : ""}`} data-approval-id={approval.id}><div className="approval-copy"><span className={`risk-dot risk-${approval.risk}`} /><div><p>{approval.kind === "network_ask" ? `${approval.target?.host ?? ""}:${approval.target?.port ?? ""}` : approval.subjectLabels.join(", ")}</p><strong>{t(approval.titleKey)}</strong><small>{t(approval.summaryKey)}</small></div></div><div className="approval-meta"><span>{approval.waiterCount > 1 ? t("policy.approvals.waiting", { count: approval.waiterCount }) : t(`policy.kind.${approval.kind}`)}</span><time dateTime={approval.expiresAt}>{new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(Date.parse(approval.expiresAt))}</time></div><div className="approval-actions">{approval.choices.map((choice) => <button className={choice.decision === "deny" ? "danger-text" : choice.scope === "host_port" ? "quiet-button" : "primary-action"} disabled={busy} key={`${choice.decision}:${choice.scope}`} onClick={() => onDecide(choice)} type="button">{t(choice.labelKey)}</button>)}</div></article>;
}

export function ExchangeRoutePage({ exchangeId }: { readonly exchangeId: string }) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const exchange = useQuery({ queryKey: dashboardQueryKeys.exchange(exchangeId), queryFn: ({ signal }) => model.client.exchange(exchangeId, signal) });
  if (exchange.isPending) return <div className="page"><LoadingRows count={8} /></div>;
  if (exchange.data === undefined) return <div className="page"><InlineProblem message={t(controlErrorKey(exchange.error))} /></div>;
  const detail = exchange.data;
  return <div className="page exchange-page"><PageHeading description={detail.id} eyebrow={t("exchange.eyebrow")} title={t("exchange.title")} /><section className="trace-layout"><div className="trace-rail"><TraceStep label={t("exchange.trace.capture")} value={detail.parentRefs.captureRunId ?? detail.parentRefs.manualCaptureId ?? t("common.unavailable")} /><TraceStep label={t("exchange.trace.environment")} value={`${detail.environment.id} · r${detail.environment.revision}`} /><TraceStep label={t("exchange.trace.endpoint")} value={detail.environment.clientEndpointId} /><TraceStep label={t("exchange.trace.protocol")} value={detail.environment.protocolPlanId} /><TraceStep label={t("exchange.trace.route")} value={detail.environment.routeId} /><TraceStep label={t("exchange.trace.attempts")} value={String(detail.processingTrace.attemptIds.length)} /></div><section className="data-panel exchange-inspector"><SectionHeading title={t("exchange.inspector.title")} /><dl className="facts-list"><dt>{t("exchange.status")}</dt><dd>{detail.status}</dd><dt>{t("exchange.result")}</dt><dd>{detail.processingTrace.result}</dd><dt>{t("exchange.account")}</dt><dd>{detail.environment.accountId ?? t("exchange.account.client")}</dd><dt>{t("exchange.snapshot")}</dt><dd><code>{detail.environment.digest}</code></dd></dl><SectionHeading title={t("exchange.attempts.title")} />{detail.processingTrace.attemptIds.length === 0 ? <EmptyState description={t("exchange.attempts.empty.description")} title={t("exchange.attempts.empty.title")} /> : <ol className="attempt-list">{detail.processingTrace.attemptIds.map((id, index) => <li key={id}><span>{index + 1}</span><code>{id}</code></li>)}</ol>}</section></section></div>;
}

function TraceStep({ label, value }: { readonly label: string; readonly value: string }) { return <div><span>{label}</span><strong>{value}</strong></div>; }

type AccountDialogState =
  | { readonly mode: "create" }
  | { readonly mode: "replace"; readonly account: ProviderAccountRecord };

export function AccountsRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const [dialog, setDialog] = useState<AccountDialogState>();
  const accounts = useQuery({
    queryKey: dashboardQueryKeys.accounts,
    queryFn: ({ signal }) => model.client.providerAccounts(signal),
    placeholderData: (previous) => previous,
  });
  const records = accounts.data?.items ?? [];
  return (
    <div className="page accounts-page">
      <PageHeading
        actions={<button className="primary-action" onClick={() => setDialog({ mode: "create" })} type="button">{t("accounts.add")}</button>}
        description={t("accounts.description")}
        title={t("accounts.title")}
      />
      <section className="data-panel account-table-panel">
        <SectionHeading title={t("accounts.connected", { count: records.length })} />
        {accounts.isPending && accounts.data === undefined ? <LoadingRows count={4} /> : accounts.isError && accounts.data === undefined ? <InlineProblem message={t(controlErrorKey(accounts.error))} /> : records.length === 0 ? <EmptyState action={<button onClick={() => setDialog({ mode: "create" })} type="button">{t("accounts.add")}</button>} description={t("accounts.empty.description")} title={t("accounts.empty.title")} /> : <div className="table-scroll"><table className="data-table responsive-table account-table"><thead><tr><th>{t("accounts.column.account")}</th><th>{t("accounts.column.provider")}</th><th>{t("accounts.column.credential")}</th><th>{t("accounts.column.revision")}</th><th className="align-right">{t("accounts.column.action")}</th></tr></thead><tbody>{records.map((account) => <tr key={account.id}><td data-label={t("accounts.column.account")}><div className="agent-cell"><BrandIcon name={account.kind === "anthropic_api_key" ? "anthropic" : "openai"} /><span><strong>{account.displayName}</strong><small>{account.id}</small></span></div></td><td data-label={t("accounts.column.provider")}>{t(`accounts.kind.${account.kind}`)}</td><td data-label={t("accounts.column.credential")}><span className={`state-pill account-health-${account.credentialState}`}>{t(`accounts.health.${account.credentialState}`)}</span></td><td data-label={t("accounts.column.revision")}>r{account.revision} · {t("accounts.credentialEpoch", { epoch: account.credentialEpoch })}</td><td className="align-right" data-label={t("accounts.column.action")}><button className="quiet-button" onClick={() => setDialog({ mode: "replace", account })} type="button">{t("accounts.updateKey")}</button></td></tr>)}</tbody></table></div>}
      </section>
      <p className="resource-boundary">{t("accounts.boundary")}</p>
      {dialog !== undefined && <ProviderAccountDialog dialog={dialog} onClose={() => setDialog(undefined)} />}
    </div>
  );
}

function ProviderAccountDialog({ dialog, onClose }: { readonly dialog: AccountDialogState; readonly onClose: () => void }) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<ProviderAccountKind>(dialog.mode === "replace" ? dialog.account.kind : "anthropic_api_key");
  const [displayName, setDisplayName] = useState(dialog.mode === "replace" ? dialog.account.displayName : "");
  const [secret, setSecret] = useState("");
  const [errorKey, setErrorKey] = useState<string>();
  const save = useMutation({
    mutationFn: () => dialog.mode === "create"
      ? model.client.createProviderAccount({ id: newProviderAccountID(kind), displayName: displayName.trim(), kind, secret })
      : model.client.replaceProviderAccountCredential(dialog.account.id, dialog.account.credentialEpoch, { secret }),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: () => {
      setSecret("");
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.accounts });
      onClose();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (secret.length > 0 && (dialog.mode === "replace" || displayName.trim().length > 0)) save.mutate();
  };
  return <div className="modal-backdrop"><section aria-labelledby="provider-account-title" aria-modal="true" className="modal account-modal" role="dialog"><header><div><p className="eyebrow">{t("accounts.dialog.eyebrow")}</p><h2 id="provider-account-title">{t(dialog.mode === "create" ? "accounts.dialog.create" : "accounts.dialog.replace", { name: dialog.mode === "replace" ? dialog.account.displayName : "" })}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><form onSubmit={submit}>{dialog.mode === "create" && <><fieldset><legend>{t("accounts.dialog.provider")}</legend><div className="provider-picker"><button aria-pressed={kind === "anthropic_api_key"} onClick={() => setKind("anthropic_api_key")} type="button"><BrandIcon name="anthropic" /><span><strong>{t("accounts.provider.anthropic")}</strong><small>{t("accounts.kind.anthropic_api_key")}</small></span></button><button aria-pressed={kind === "openai_api_key"} onClick={() => setKind("openai_api_key")} type="button"><BrandIcon name="openai" /><span><strong>{t("accounts.provider.openai")}</strong><small>{t("accounts.kind.openai_api_key")}</small></span></button></div></fieldset><label><span>{t("accounts.dialog.name")}</span><input autoFocus maxLength={256} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("accounts.dialog.namePlaceholder")} value={displayName} /></label></>}<label><span>{t("accounts.dialog.secret")}</span><input autoComplete="off" autoFocus={dialog.mode === "replace"} onChange={(event) => setSecret(event.target.value)} spellCheck={false} type="password" value={secret} /></label><p className="credential-boundary">{t("accounts.dialog.secretBoundary")}</p>{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={save.isPending || secret.length === 0 || (dialog.mode === "create" && displayName.trim().length === 0)} type="submit">{save.isPending ? t("accounts.dialog.saving") : t(dialog.mode === "create" ? "accounts.dialog.connect" : "accounts.dialog.update")}</button></footer></form></section></div>;
}

function newProviderAccountID(kind: ProviderAccountKind): string {
  const provider = kind === "anthropic_api_key" ? "anthropic" : "openai";
  return `account.${provider}.${globalThis.crypto.randomUUID().toLowerCase()}`;
}

export function ExtensionsRoutePage() { return <DeferredResourcePage kind="extensions" />; }
export function QualityRoutePage() { return <DeferredResourcePage kind="quality" />; }

function DeferredResourcePage({ kind }: { readonly kind: "extensions" | "quality" }) {
  const { t } = useTranslation();
  return <div className="page"><PageHeading description={t(`${kind}.description`)} title={t(`${kind}.title`)} /><section className="data-panel deferred-resource"><EmptyState description={t(`${kind}.deferred.description`)} title={t(`${kind}.deferred.title`)} /></section></div>;
}

function modeOf(rules: ConnectionRuleSet | undefined): PolicyMode {
  if (rules?.mode === "deny_unknown") return "block";
  if (rules?.mode === "ask_unknown") return "ask";
  return "monitor";
}

function policyInput(current: ConnectionRuleSet, mode: PolicyMode) {
  return {
    rules: current.rules,
    mode: mode === "monitor" ? "monitor" as const : mode === "ask" ? "ask_unknown" as const : "deny_unknown" as const,
  };
}

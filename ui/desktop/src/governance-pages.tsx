import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
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
import type {
  ApprovalChoice,
  ApprovalView,
  ConnectionDecision,
  ConnectionRuleSet,
} from "./control-types.ts";

type PolicyMode = "open" | "ask" | "block";
const allowAllRuleID = "operator.allow-all";

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
          {(["open", "ask", "block"] as const).map((mode) => <button aria-checked={policyMode === mode} disabled={replace.isPending || rules.data === undefined} key={mode} onClick={() => replace.mutate(mode)} role="radio" type="button">{t(`policy.mode.${mode}`)}</button>)}
        </div>
      </section>
      {errorKey !== undefined && <InlineProblem message={t(errorKey)} />}
      <section className="data-panel approvals-panel">
        <SectionHeading title={t("policy.approvals.title", { count: pending.length })} />
        {approvals.isPending && approvals.data === undefined ? <LoadingRows count={4} /> : approvals.isError && approvals.data === undefined ? <InlineProblem message={t(controlErrorKey(approvals.error))} /> : pending.length === 0 ? <EmptyState description={t("policy.approvals.empty.description")} title={t("policy.approvals.empty.title")} /> : <div className="approval-list">{pending.map((approval) => <ApprovalRow approval={approval} busy={decide.isPending} highlighted={approval.id === selectedApprovalId} key={approval.id} onDecide={(choice) => decide.mutate({ approval, choice })} locale={i18n.language} />)}</div>}
      </section>
      <section className="data-panel rules-panel">
        <SectionHeading title={t("policy.rules.title")} />
        {rules.data === undefined ? <LoadingRows count={3} /> : rules.data.rules.length === 0 ? <EmptyState description={t("policy.rules.empty.description")} title={t("policy.rules.empty.title")} /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>{t("policy.rules.column.decision")}</th><th>{t("policy.rules.column.target")}</th><th>{t("policy.rules.column.source")}</th></tr></thead><tbody>{rules.data.rules.filter((rule) => rule.id !== allowAllRuleID).map((rule) => <tr key={rule.id}><td><span className={`decision decision-${rule.decision}`}>{t(`policy.decision.${rule.decision}`)}</span></td><td>{rule.host === undefined ? t("policy.rules.all") : `${rule.host}${rule.port === undefined ? "" : `:${rule.port}`}`}</td><td><code>{rule.id}</code></td></tr>)}</tbody></table></div>}
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

export function AccountsRoutePage() { return <DeferredResourcePage kind="accounts" />; }
export function ExtensionsRoutePage() { return <DeferredResourcePage kind="extensions" />; }
export function QualityRoutePage() { return <DeferredResourcePage kind="quality" />; }

function DeferredResourcePage({ kind }: { readonly kind: "accounts" | "extensions" | "quality" }) {
  const { t } = useTranslation();
  return <div className="page"><PageHeading description={t(`${kind}.description`)} title={t(`${kind}.title`)} /><section className="data-panel deferred-resource"><EmptyState description={t(`${kind}.deferred.description`)} title={t(`${kind}.deferred.title`)} /></section></div>;
}

function modeOf(rules: ConnectionRuleSet | undefined): PolicyMode {
  if (rules?.rules.some((rule) => rule.id === allowAllRuleID && rule.decision === "allow" && rule.match === "any")) return "open";
  return rules?.default.decision === "deny" ? "block" : "ask";
}

function policyInput(current: ConnectionRuleSet, mode: PolicyMode) {
  const rules = current.rules.filter((rule) => rule.id !== allowAllRuleID);
  if (mode === "open") rules.push({ id: allowAllRuleID, priority: 4_294_967_295, decision: "allow", match: "any" });
  const decision: ConnectionDecision = mode === "block" ? "deny" : "ask";
  return { rules, default: { id: `default.${decision}`, priority: 0, decision, match: "any" } };
}

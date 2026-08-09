import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
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
  ActivityRecord,
  ConnectionRuleSet,
  ExchangeContentBlock,
  ExchangeContentDetail,
  ExchangeContentMessage,
  ExchangeContentView,
  ExchangeDetail,
  EgressAttemptRecord,
  ProviderAccountKind,
  ProviderAccountRecord,
} from "./control-types.ts";
import { dashboardTaskRoutePaths } from "./navigation.ts";
import { requestReasonKey, requestResultKey } from "./request-result.ts";

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
  const [contentView, setContentView] = useState<ExchangeContentView>("incremental");
  const exchange = useQuery({
    queryKey: dashboardQueryKeys.exchange(exchangeId, contentView),
    queryFn: ({ signal }) => model.client.exchange(exchangeId, { contentView, signal }),
    placeholderData: (previous) => previous,
  });
  const captureRunId = exchange.data?.parentRefs.captureRunId;
  const runRequests = useQuery({
    enabled: captureRunId !== undefined,
    queryKey: [...dashboardQueryKeys.activities, "capture-run", captureRunId ?? "unavailable"],
    queryFn: ({ signal }) => captureRunId === undefined
      ? Promise.resolve({ items: [] })
      : model.client.activities({ captureRunId }, signal),
    placeholderData: (previous) => previous,
  });
  if (exchange.isPending) return <div className="page"><LoadingRows count={8} /></div>;
  if (exchange.data === undefined) return <div className="page"><InlineProblem message={t(controlErrorKey(exchange.error))} /></div>;
  const detail = exchange.data;
  const displayedContentView = detail.content.requestProjection?.view ?? contentView;
  const viewTransitionPending = exchange.isFetching && displayedContentView !== contentView;
  const response = detail.content.response;
  const attempts = detail.processingTrace.attempts;
  const captureKey = detail.parentRefs.captureRunId !== undefined
    ? `managed_run:${detail.parentRefs.captureRunId}`
    : detail.parentRefs.manualCaptureId !== undefined
      ? `manual_capture:${detail.parentRefs.manualCaptureId}`
      : undefined;
  const resultReasonKey = requestReasonKey(detail.processingTrace.result);
  return (
    <div className="page exchange-page">
      <PageHeading
        actions={captureKey === undefined
          ? <Link className="back-link" search={{}} to={dashboardTaskRoutePaths.captureRequests}>{t("exchange.back.requests")}</Link>
          : <Link className="back-link" params={{ captureKey }} search={{}} to={dashboardTaskRoutePaths.captureDetail}>{t("exchange.back.capture")}</Link>}
        description={detail.id}
        eyebrow={t("exchange.eyebrow")}
        title={t("exchange.title")}
      />
      {captureRunId !== undefined && <RequestSequence currentId={detail.id} items={runRequests.data?.items ?? []} />}
      <section className="trace-layout">
        <aside aria-label={t("exchange.trace.title")} className="trace-rail">
          <TraceStep label={t("exchange.trace.capture")} value={detail.parentRefs.captureRunId ?? detail.parentRefs.manualCaptureId ?? t("common.unavailable")} />
          <TraceStep label={t("exchange.trace.environment")} value={`${detail.environment.id} · r${detail.environment.revision}`} />
          <TraceStep label={t("exchange.trace.endpoint")} value={`${detail.environment.clientEndpointId} · r${detail.environment.clientEndpointRevision}`} />
          <TraceStep label={t("exchange.trace.protocol")} value={`${detail.environment.protocolPlanId} · r${detail.environment.protocolPlanRevision}`} />
          <TraceStep label={t("exchange.trace.route")} value={`${detail.environment.routeId} · r${detail.environment.routeRevision}`} />
          <TraceStep label={t("exchange.trace.attempts")} value={String(attempts.length)} />
          <TraceStep
            label={t("exchange.trace.result")}
            value={resultReasonKey === undefined
              ? detail.processingTrace.result
              : t(resultReasonKey)}
          />
        </aside>
        <div className="exchange-inspector-stack">
          <section className="data-panel exchange-inspector">
            <div className="exchange-inspector-heading">
              <SectionHeading title={t("exchange.inspector.title")} />
              {detail.content.state === "recorded" && <div className="exchange-inspector-actions">
                <span className="recording-state">{t(`exchange.content.mode.${detail.content.mode ?? "full"}`)}</span>
                {detail.content.requestProjection?.fullSnapshotAvailable === true && <button
                  className="quiet-button request-view-toggle"
                  disabled={viewTransitionPending}
                  onClick={() => setContentView(displayedContentView === "full" ? "incremental" : "full")}
                  type="button"
                >{t(displayedContentView === "full" ? "exchange.content.view.incremental" : "exchange.content.view.full")}</button>}
              </div>}
            </div>
            {detail.diagnosis !== undefined && <RequestDiagnosis diagnosis={detail.diagnosis} result={detail.processingTrace.result} />}
            {detail.content.state === "not_recorded" && detail.diagnosis !== undefined
              ? null
              : detail.content.state === "not_recorded" || detail.content.request === undefined
              ? <EmptyState description={t("exchange.content.empty.description")} title={t("exchange.content.empty.title")} />
              : <div className="conversation-evidence">
                  <div className="content-retention">
                    <span>{t("exchange.content.expires")}</span>
                    <time dateTime={detail.content.expiresAt}>{detail.content.expiresAt === undefined ? t("common.unavailable") : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(Date.parse(detail.content.expiresAt))}</time>
                  </div>
                  {detail.content.requestProjection !== undefined && <RequestProjectionStrip projection={detail.content.requestProjection} visibleMessages={detail.content.request.messages.length} />}
                  <RequestConversation messages={detail.content.request.messages} />
                  {response !== undefined && <article className="exchange-message exchange-message-assistant"><header><span>{t("exchange.role.assistant")}</span><small>{response.reportedModel} · {t(`exchange.stop.${response.stopReason}`)}</small></header><ExchangeBlocks blocks={response.blocks} /><UsageSummary usage={response.usage} /></article>}
                </div>}
          </section>
          <section className="data-panel exchange-facts">
            <dl className="facts-list">
              <dt>{t("exchange.status")}</dt><dd>{detail.status}</dd>
              <dt>{t("exchange.account")}</dt><dd>{detail.environment.accountId ?? t("exchange.account.client")}</dd>
              <dt>{t("exchange.account.revision")}</dt><dd>{detail.environment.accountRevision === undefined ? t("common.unavailable") : `r${detail.environment.accountRevision}`}</dd>
              <dt>{t("exchange.account.credentialEpoch")}</dt><dd>{detail.environment.credentialEpoch ?? t("common.unavailable")}</dd>
              <dt>{t("exchange.snapshot")}</dt><dd><code title={detail.environment.digest}>{shortValue(detail.environment.digest)}</code></dd>
            </dl>
            <SectionHeading title={t("exchange.attempts.title")} />
            {attempts.length === 0
              ? <p className="muted-copy">{t("exchange.attempts.empty.description")}</p>
              : <ol className="attempt-list">{attempts.map((attempt) => <AttemptEvidence attempt={attempt} key={attempt.id} />)}</ol>}
          </section>
        </div>
      </section>
    </div>
  );
}

function RequestProjectionStrip({ projection, visibleMessages }: {
  readonly projection: NonNullable<ExchangeContentDetail["requestProjection"]>;
  readonly visibleMessages: number;
}) {
  const { t } = useTranslation();
  const key = projection.view === "full"
    ? "exchange.content.projection.full"
    : projection.relationship === "incremental"
      ? "exchange.content.projection.incremental"
      : projection.relationship === "same_transcript"
      ? "exchange.content.projection.replay"
      : "exchange.content.projection.checkpoint";
  return <div className={`request-projection request-projection-${projection.relationship}`}>
    <span className="request-projection-node" />
    <strong>{t(key, {
      inherited: projection.inheritedMessageCount,
      total: projection.totalMessageCount,
      visible: visibleMessages,
    })}</strong>
  </div>;
}

function RequestDiagnosis({ diagnosis, result }: {
  readonly diagnosis: NonNullable<ExchangeDetail["diagnosis"]>;
  readonly result: string;
}) {
  const { t } = useTranslation();
  const reasonKey = requestReasonKey(result);
  const clientLocation = [diagnosis.clientField, diagnosis.clientPath].filter((value) => value !== undefined).join(" · ");
  const providerLocation = diagnosis.providerField;
  return (
    <div className="request-diagnosis" role="status">
      <div>
        <strong>{reasonKey === undefined ? result : t(reasonKey)}</strong>
        <p>{diagnosis.clientField === undefined && diagnosis.clientPath === undefined
          ? t("exchange.diagnosis.provider.description")
          : t("exchange.diagnosis.client.description")}</p>
      </div>
      <dl>
        {clientLocation === "" ? null : <><dt>{t("exchange.diagnosis.client.location")}</dt><dd><code>{clientLocation}</code></dd></>}
        {diagnosis.providerStatus === undefined ? null : <><dt>{t("exchange.diagnosis.provider.status")}</dt><dd>{diagnosis.providerStatus}</dd></>}
        {providerLocation === undefined ? null : <><dt>{t("exchange.diagnosis.provider.field")}</dt><dd><code>{providerLocation}</code></dd></>}
      </dl>
    </div>
  );
}

function RequestSequence({ currentId, items }: { readonly currentId: string; readonly items: readonly ActivityRecord[] }) {
  const { t, i18n } = useTranslation();
  const ordered = [...items].sort((left, right) => Date.parse(left.occurredAt) - Date.parse(right.occurredAt));
  const currentIndex = ordered.findIndex((item) => item.id === currentId);
  return (
    <nav aria-label={t("exchange.runRequests.label")} className="request-sequence">
      <div className="request-sequence-heading">
        <span>{t("exchange.runRequests.title")}</span>
        <small>{t("exchange.runRequests.position", { current: currentIndex < 0 ? "–" : currentIndex + 1, total: ordered.length })}</small>
      </div>
      <ol>
        {ordered.map((item, index) => (
          <li key={item.id}>
            <Link
              aria-current={item.id === currentId ? "page" : undefined}
              params={{ exchangeId: item.id }}
              search={{}}
              title={item.id}
              to={dashboardTaskRoutePaths.activityRequest}
            >
              <span>{index + 1}</span>
              <strong>{t(requestResultKey(item.reasonCode, item.status))}</strong>
              <time dateTime={item.occurredAt}>{new Intl.DateTimeFormat(i18n.language, { hour: "2-digit", minute: "2-digit" }).format(Date.parse(item.occurredAt))}</time>
            </Link>
          </li>
        ))}
      </ol>
      {ordered.length === 0 && <span className="request-sequence-empty">{t("exchange.runRequests.loading")}</span>}
    </nav>
  );
}

function AttemptEvidence({ attempt }: { readonly attempt: EgressAttemptRecord }) {
  const { t } = useTranslation();
  return (
    <li className="attempt-evidence">
      <span>{attempt.sequence}</span>
      <div>
        <strong>{t(`egress.purpose.${attempt.purpose}`)}</strong>
        <code title={attempt.targetOrigin}>{attempt.targetOrigin}</code>
        <small><code>{attempt.id}</code>{attempt.parent.id === undefined ? null : <><span> · </span><code>{attempt.parent.id}</code></>}</small>
        <small>{attempt.terminal ? t(`egress.outcome.${attempt.outcome ?? "failed"}`) : t("egress.outcome.inFlight")} · {t("egress.bytes.value", { out: attempt.bytesOut, in: attempt.bytesIn })}</small>
        {attempt.errorClass !== undefined && <small className="attempt-error">{attempt.errorClass}</small>}
      </div>
    </li>
  );
}

function TraceStep({ label, value }: { readonly label: string; readonly value: string }) { return <div><span>{label}</span><strong>{value}</strong></div>; }

function ExchangeMessage({ message }: { readonly message: ExchangeContentMessage }) {
  const { t } = useTranslation();
  return <article className={`exchange-message exchange-message-${message.role}`}><header><span>{t(`exchange.role.${message.role}`)}</span></header><ExchangeBlocks blocks={message.blocks} /></article>;
}

function RequestConversation({ messages }: { readonly messages: readonly ExchangeContentMessage[] }) {
  const { t } = useTranslation();
  const context = messages.filter((message) => message.role === "system" || message.role === "developer");
  const conversation = messages.filter((message) => message.role !== "system" && message.role !== "developer");
  const visibleStart = Math.max(0, conversation.length - 6);
  const earlier = conversation.slice(0, visibleStart);
  const recent = conversation.slice(visibleStart);
  return <>
    {context.length > 0 && <DeferredMessages
      className="context-disclosure"
      messages={context}
      summary={t("exchange.context.summary", { count: context.length })}
    />}
    {earlier.length > 0 && <DeferredMessages
      className="history-disclosure"
      messages={earlier}
      summary={t("exchange.history.summary", { count: earlier.length })}
    />}
    {recent.map((message, index) => <ExchangeMessage key={`${message.role}:recent:${index}`} message={message} />)}
  </>;
}

function DeferredMessages({ className, messages, summary }: { readonly className: string; readonly messages: readonly ExchangeContentMessage[]; readonly summary: string }) {
  const [open, setOpen] = useState(false);
  return <details className={`message-disclosure ${className}`} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{summary}<span>{open ? "−" : "+"}</span></summary>
    {open && <div>{messages.map((message, index) => <ExchangeMessage key={`${message.role}:deferred:${index}`} message={message} />)}</div>}
  </details>;
}

function ExchangeBlocks({ blocks }: { readonly blocks: readonly ExchangeContentBlock[] }) {
  const { t } = useTranslation();
  return <div className="exchange-blocks">{blocks.map((block, index) => {
    const key = `${block.kind}:${block.callId ?? index}`;
    if (block.kind === "provider_extension") return <div className="omitted-content provider-extension" key={key}>{t("exchange.content.providerExtension", { bytes: block.originalSize })}</div>;
    if (block.availability === "omitted") return <div className="omitted-content" key={key}>{t("exchange.content.omitted", { bytes: block.originalSize })}</div>;
    if (block.kind === "tool_call") return <details className="tool-evidence tool-call" key={key}><summary><strong>{block.toolName}</strong><span>{t("exchange.tool.proposed")}</span></summary>{block.arguments !== undefined && <pre>{JSON.stringify(block.arguments, null, 2)}</pre>}</details>;
    if (block.kind === "tool_result") return <details className={`tool-evidence tool-result${block.toolError === true ? " failed" : ""}`} key={key}><summary><strong>{t("exchange.tool.result")}</strong><span>{block.toolError === true ? t("exchange.tool.failed") : t("exchange.tool.reported")}</span></summary><pre>{block.text}</pre></details>;
    return block.kind === "refusal"
      ? <MarkdownEvidence className="model-refusal" key={key} text={block.text ?? ""} />
      : <MarkdownEvidence key={key} text={block.text ?? ""} />;
  })}</div>;
}

function MarkdownEvidence({ className, text }: { readonly className?: string; readonly text: string }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const clipped = text.length > 12_000;
  const rendered = clipped && !expanded ? `${text.slice(0, 12_000)}\n\n…` : text;
  return <div className={`markdown-evidence${className === undefined ? "" : ` ${className}`}`}>
    <ReactMarkdown
      components={{
        a: ({ children, ...properties }) => <a {...properties} rel="noreferrer" target="_blank">{children}</a>,
        img: ({ alt }) => <span className="markdown-image-placeholder">{t("exchange.markdown.image", { alt: alt ?? "" })}</span>,
      }}
      remarkPlugins={[remarkGfm]}
      skipHtml
    >{rendered}</ReactMarkdown>
    {clipped && <button className="markdown-expand" onClick={() => setExpanded((value) => !value)} type="button">{t(expanded ? "exchange.markdown.less" : "exchange.markdown.more")}</button>}
  </div>;
}

function UsageSummary({ usage }: { readonly usage: NonNullable<ExchangeContentDetail["response"]>["usage"] }) {
  const { t } = useTranslation();
  const values = [["input", usage.inputUncached], ["cacheRead", usage.cacheRead], ["cacheWrite", usage.cacheWrite], ["output", usage.output], ["reasoning", usage.reasoning]] as const;
  return <dl className="usage-strip">{values.map(([key, value]) => <div key={key}><dt>{t(`exchange.usage.${key}`)}</dt><dd>{value.known ? value.tokens : t("common.unavailable")}</dd></div>)}</dl>;
}

function shortValue(value: string): string { return value.length <= 22 ? value : `${value.slice(0, 12)}…${value.slice(-6)}`; }

type AccountDialogState =
  | { readonly mode: "create" }
  | { readonly mode: "replace"; readonly account: ProviderAccountRecord }
  | { readonly mode: "delete"; readonly account: ProviderAccountRecord };

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
        {accounts.isPending && accounts.data === undefined ? <LoadingRows count={4} /> : accounts.isError && accounts.data === undefined ? <InlineProblem message={t(controlErrorKey(accounts.error))} /> : records.length === 0 ? <EmptyState action={<button onClick={() => setDialog({ mode: "create" })} type="button">{t("accounts.add")}</button>} description={t("accounts.empty.description")} title={t("accounts.empty.title")} /> : <div className="table-scroll"><table className="data-table responsive-table account-table"><thead><tr><th>{t("accounts.column.account")}</th><th>{t("accounts.column.provider")}</th><th>{t("accounts.column.credential")}</th><th>{t("accounts.column.revision")}</th><th className="align-right">{t("accounts.column.action")}</th></tr></thead><tbody>{records.map((account) => <tr key={account.id}><td data-label={t("accounts.column.account")}><div className="agent-cell"><BrandIcon name={account.kind === "openai_api_key" ? "openai" : "anthropic"} /><span><strong>{account.displayName}</strong><small>{account.id}</small></span></div></td><td data-label={t("accounts.column.provider")}>{t(`accounts.kind.${account.kind}`)}</td><td data-label={t("accounts.column.credential")}><span className={`state-pill account-health-${account.credentialState}`}>{t(`accounts.health.${account.credentialState}`)}</span></td><td data-label={t("accounts.column.revision")}>r{account.revision} · {t("accounts.credentialEpoch", { epoch: account.credentialEpoch })}</td><td className="align-right" data-label={t("accounts.column.action")}><div className="account-actions"><button className="quiet-button" onClick={() => setDialog({ mode: "replace", account })} type="button">{t("accounts.updateKey")}</button><button className="danger-text" onClick={() => setDialog({ mode: "delete", account })} type="button">{t("accounts.delete")}</button></div></td></tr>)}</tbody></table></div>}
      </section>
      <p className="resource-boundary">{t("accounts.boundary")}</p>
      {dialog !== undefined && (dialog.mode === "delete" ? <DeleteProviderAccountDialog account={dialog.account} onClose={() => setDialog(undefined)} /> : <ProviderAccountDialog dialog={dialog} onClose={() => setDialog(undefined)} />)}
    </div>
  );
}

function ProviderAccountDialog({ dialog, onClose }: { readonly dialog: Exclude<AccountDialogState, { readonly mode: "delete" }>; readonly onClose: () => void }) {
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
  return <div className="modal-backdrop"><section aria-labelledby="provider-account-title" aria-modal="true" className="modal account-modal" role="dialog"><header><div><p className="eyebrow">{t("accounts.dialog.eyebrow")}</p><h2 id="provider-account-title">{t(dialog.mode === "create" ? "accounts.dialog.create" : "accounts.dialog.replace", { name: dialog.mode === "replace" ? dialog.account.displayName : "" })}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><form onSubmit={submit}>{dialog.mode === "create" && <><fieldset><legend>{t("accounts.dialog.provider")}</legend><div className="provider-picker"><button aria-pressed={kind === "anthropic_api_key"} onClick={() => setKind("anthropic_api_key")} type="button"><BrandIcon name="anthropic" /><span><strong>{t("accounts.provider.anthropic")}</strong><small>{t("accounts.kind.anthropic_api_key")}</small></span></button><button aria-pressed={kind === "claude_oauth_token"} onClick={() => setKind("claude_oauth_token")} type="button"><BrandIcon name="anthropic" /><span><strong>{t("accounts.provider.claude")}</strong><small>{t("accounts.kind.claude_oauth_token")}</small></span></button><button aria-pressed={kind === "openai_api_key"} onClick={() => setKind("openai_api_key")} type="button"><BrandIcon name="openai" /><span><strong>{t("accounts.provider.openai")}</strong><small>{t("accounts.kind.openai_api_key")}</small></span></button></div></fieldset><label><span>{t("accounts.dialog.name")}</span><input autoFocus maxLength={256} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("accounts.dialog.namePlaceholder")} value={displayName} /></label></>}<label><span>{t(kind === "claude_oauth_token" ? "accounts.dialog.oauthToken" : "accounts.dialog.apiKey")}</span><input autoComplete="off" autoFocus={dialog.mode === "replace"} onChange={(event) => setSecret(event.target.value)} spellCheck={false} type="password" value={secret} /></label><p className="credential-boundary">{t(kind === "claude_oauth_token" ? "accounts.dialog.oauthBoundary" : "accounts.dialog.secretBoundary")}</p>{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={save.isPending || secret.length === 0 || (dialog.mode === "create" && displayName.trim().length === 0)} type="submit">{save.isPending ? t("accounts.dialog.saving") : t(dialog.mode === "create" ? "accounts.dialog.connect" : "accounts.dialog.update")}</button></footer></form></section></div>;
}

function DeleteProviderAccountDialog({ account, onClose }: { readonly account: ProviderAccountRecord; readonly onClose: () => void }) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [references, setReferences] = useState<readonly { readonly environmentId: string; readonly environmentName: string; readonly environmentRevision: number; readonly routeId: string; readonly routeRevision: number }[]>();
  const [referenceCount, setReferenceCount] = useState(0);
  const [errorKey, setErrorKey] = useState<string>();
  const remove = useMutation({
    mutationFn: () => model.client.deleteProviderAccount(account.id, account.credentialEpoch),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: (result) => {
      if (!result.deleted) {
        setReferences(result.references);
        setReferenceCount(result.referenceCount);
        return;
      }
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.accounts });
      onClose();
    },
  });
  return <div className="modal-backdrop"><section aria-labelledby="delete-provider-account-title" aria-modal="true" className="modal account-modal" role="dialog"><header><div><p className="eyebrow">{t("accounts.delete.eyebrow")}</p><h2 id="delete-provider-account-title">{t("accounts.delete.title", { name: account.displayName })}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><div className="modal-body"><p>{t("accounts.delete.description")}</p>{references !== undefined && references.length > 0 && <div className="inline-problem"><strong>{t("accounts.delete.blocked")}</strong><ul>{references.map((reference) => <li key={`${reference.environmentId}\u0000${reference.routeId}`}>{t("accounts.delete.reference", { environment: reference.environmentName, revision: reference.environmentRevision, route: reference.routeId })}</li>)}</ul>{referenceCount > references.length && <p>{t("accounts.delete.more", { count: referenceCount - references.length })}</p>}</div>}{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}</div><footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={remove.isPending || (references !== undefined && references.length > 0)} onClick={() => remove.mutate()} type="button">{remove.isPending ? t("accounts.delete.deleting") : t("accounts.delete.confirm")}</button></footer></section></div>;
}

function newProviderAccountID(kind: ProviderAccountKind): string {
  const provider = kind === "openai_api_key" ? "openai" : kind === "claude_oauth_token" ? "claude" : "anthropic";
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

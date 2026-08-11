import { useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
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
  ExchangeDetail,
  EgressAttemptRecord,
  ProviderAccountKind,
  ProviderAccountRecord,
  UpstreamEndpointKind,
  UpstreamEndpointRecord,
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
  const exchange = useQuery({
    queryKey: dashboardQueryKeys.exchange(exchangeId, "incremental"),
    queryFn: ({ signal }) => model.client.exchange(exchangeId, { contentView: "incremental", signal }),
    refetchInterval: (query) => query.state.data?.status === "pending" ? model.pollInterval : false,
  });
  const captureRunId = exchange.data?.parentRefs.captureRunId;
  const runRequests = useInfiniteQuery({
    enabled: captureRunId !== undefined,
    queryKey: [...dashboardQueryKeys.activities, "capture-run", captureRunId ?? "unavailable"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => captureRunId === undefined
      ? Promise.resolve({ items: [] })
      : model.client.activities({
          captureRunId,
          limit: 50,
          ...(pageParam === "" ? {} : { cursor: pageParam }),
        }, signal),
    getNextPageParam: (page) => page.nextCursor,
    refetchInterval: model.pollInterval,
  });
  const runRequestItems = useMemo(() => {
    const unique = new Map<string, ActivityRecord>();
    for (const page of runRequests.data?.pages ?? []) {
      for (const item of page.items) unique.set(item.id, item);
    }
    return [...unique.values()].sort((left, right) => Date.parse(left.occurredAt) - Date.parse(right.occurredAt));
  }, [runRequests.data?.pages]);
  const detailQueries = useQueries({
    queries: runRequestItems.map((item) => ({
      queryKey: dashboardQueryKeys.exchange(item.id, "incremental"),
      queryFn: ({ signal }) => model.client.exchange(item.id, { contentView: "incremental", signal }),
      refetchInterval: item.status === "pending" ? model.pollInterval : false,
    })),
  });
  useFollowLatest(captureRunId !== undefined);
  if (exchange.isPending) return <div className="page"><LoadingRows count={8} /></div>;
  if (exchange.data === undefined) return <div className="page"><InlineProblem message={t(controlErrorKey(exchange.error))} /></div>;
  const detail = exchange.data;
  const captureKey = detail.parentRefs.captureRunId !== undefined
    ? `managed_run:${detail.parentRefs.captureRunId}`
    : detail.parentRefs.manualCaptureId !== undefined
      ? `manual_capture:${detail.parentRefs.manualCaptureId}`
      : undefined;
  const turns = runRequestItems.map((summary, index) => ({ summary, query: detailQueries[index]! }));
  return (
    <div className="page run-conversation-page">
      <PageHeading
        actions={captureKey === undefined
          ? <Link className="back-link" search={{}} to={dashboardTaskRoutePaths.captureRequests}><BackIcon />{t("exchange.back.requests")}</Link>
          : <Link className="back-link" params={{ captureKey }} search={{}} to={dashboardTaskRoutePaths.captureDetail}><BackIcon />{t("exchange.back.capture")}</Link>}
        description={detail.id}
        eyebrow={t("exchange.eyebrow")}
        title={t("exchange.run.title")}
      />
      <RunContext detail={detail} />
      {captureRunId === undefined ? <SingleExchangeTurn current detail={detail} index={0} summary={undefined} /> : <>
        <TurnMap currentId={detail.id} items={runRequestItems} />
        {runRequests.hasNextPage && <button className="quiet-button load-older-turns" disabled={runRequests.isFetchingNextPage} onClick={() => void runRequests.fetchNextPage()} type="button">{t(runRequests.isFetchingNextPage ? "exchange.run.loadingOlder" : "exchange.run.loadOlder")}</button>}
        {runRequests.isError && runRequests.data === undefined && <InlineProblem message={t(controlErrorKey(runRequests.error))} />}
        <section aria-label={t("exchange.run.conversation")} className="run-conversation">
          {turns.map(({ summary, query }, index) => query.data === undefined
            ? <TurnLoading error={query.error} index={index} key={summary.id} summary={summary} />
            : <SingleExchangeTurn current={summary.id === detail.id} detail={query.data} index={index} key={summary.id} summary={summary} />)}
        </section>
      </>}
    </div>
  );
}

function RunContext({ detail }: { readonly detail: ExchangeDetail }) {
  const { t } = useTranslation();
  return <details className="run-context">
    <summary><span><strong>{t("exchange.run.context")}</strong><small>{detail.environment.id} · r{detail.environment.revision} · {detail.environment.routeId}</small></span><span aria-hidden="true" className="run-context-chevron"><ChevronIcon /></span></summary>
    <dl>
      <div><dt>{t("exchange.trace.capture")}</dt><dd>{detail.parentRefs.captureRunId ?? detail.parentRefs.manualCaptureId ?? t("common.unavailable")}</dd></div>
      <div><dt>{t("exchange.trace.environment")}</dt><dd>{detail.environment.id} · r{detail.environment.revision}</dd></div>
      <div><dt>{t("exchange.trace.endpoint")}</dt><dd>{detail.environment.clientEndpointId} · r{detail.environment.clientEndpointRevision}</dd></div>
      <div><dt>{t("exchange.trace.protocol")}</dt><dd>{detail.environment.protocolPlanId} · r{detail.environment.protocolPlanRevision}</dd></div>
      <div><dt>{t("exchange.trace.route")}</dt><dd>{detail.environment.routeId} · r{detail.environment.routeRevision}</dd></div>
    </dl>
  </details>;
}

function useFollowLatest(enabled: boolean): void {
  useEffect(() => {
    if (!enabled) return;
    const root = document.getElementById("main-content");
    const conversation = root?.querySelector<HTMLElement>(".run-conversation");
    if (root === null || root === undefined || conversation === null || conversation === undefined) return;
    const threshold = () => Math.max(96, root.clientHeight * 0.12);
    const nearBottom = () => root.scrollHeight - root.scrollTop - root.clientHeight <= threshold();
    let following = nearBottom();
    let observedScrollHeight = root.scrollHeight;
    const follow = () => {
      // A browser may deliver the scroll event caused by the preceding
      // scroll-to-bottom after new content has already changed scrollHeight.
      // Judge that event against the last observed content height, otherwise
      // growth itself can look like the reader deliberately moved away.
      const wasNearBottom = observedScrollHeight - root.scrollTop - root.clientHeight <= threshold();
      observedScrollHeight = root.scrollHeight;
      if (!following && !wasNearBottom) return;
      root.scrollTo({ top: root.scrollHeight, behavior: "auto" });
      following = true;
    };
    const onScroll = () => {
      following = nearBottom();
    };
    root.addEventListener("scroll", onScroll, { passive: true });
    const observer = new ResizeObserver(follow);
    observer.observe(conversation);
    follow();
    return () => {
      root.removeEventListener("scroll", onScroll);
      observer.disconnect();
    };
  }, [enabled]);
}

function TurnMap({ currentId, items }: { readonly currentId: string; readonly items: readonly ActivityRecord[] }) {
  const { t, i18n } = useTranslation();
  const [activeId, setActiveId] = useState(currentId);
  const listRef = useRef<HTMLOListElement>(null);
  useEffect(() => setActiveId(currentId), [currentId]);
  useEffect(() => {
    const root = document.getElementById("main-content");
    if (root === null) return;
    let frame = 0;
    const sync = () => {
      frame = 0;
      const rootBounds = root.getBoundingClientRect();
      const mapBounds = root.querySelector<HTMLElement>(".turn-map")?.getBoundingClientRect();
      const anchor = Math.max(rootBounds.top, mapBounds?.bottom ?? rootBounds.top) + 12;
      const turns = items.flatMap((item) => {
        const element = document.getElementById(`run-turn-${item.id}`);
        return element === null ? [] : [{ id: item.id, bounds: element.getBoundingClientRect() }];
      });
      const atAnchor = turns.find(({ bounds }) => bounds.top <= anchor && bounds.bottom > anchor);
      const visible = turns.filter(({ bounds }) => bounds.bottom > rootBounds.top && bounds.top < rootBounds.bottom);
      const nearest = visible.sort((left, right) => Math.abs(left.bounds.top - anchor) - Math.abs(right.bounds.top - anchor))[0];
      const next = atAnchor?.id ?? nearest?.id;
      if (next !== undefined) setActiveId(next);
    };
    const schedule = () => {
      if (frame === 0) frame = globalThis.requestAnimationFrame(sync);
    };
    root.addEventListener("scroll", schedule, { passive: true });
    globalThis.addEventListener("resize", schedule);
    schedule();
    return () => {
      root.removeEventListener("scroll", schedule);
      globalThis.removeEventListener("resize", schedule);
      if (frame !== 0) globalThis.cancelAnimationFrame(frame);
    };
  }, [items]);
  useEffect(() => {
    const list = listRef.current;
    const active = list?.querySelector<HTMLElement>('[aria-current="step"]')?.closest<HTMLElement>("li");
    if (list === null || active === null || active === undefined) return;
    const itemTop = active.offsetTop;
    const itemBottom = itemTop + active.offsetHeight;
    if (itemTop < list.scrollTop) list.scrollTop = itemTop;
    else if (itemBottom > list.scrollTop + list.clientHeight) list.scrollTop = itemBottom - list.clientHeight;
  }, [activeId]);
  const locate = (id: string) => {
    setActiveId(id);
    const target = document.getElementById(`run-turn-${id}`);
    target?.scrollIntoView({ behavior: globalThis.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" });
    target?.focus({ preventScroll: true });
  };
  return <nav aria-label={t("exchange.run.map.label")} className="turn-map">
    <div><strong>{t("exchange.run.map.title")}</strong><span>{t("exchange.run.map.position", { current: Math.max(1, items.findIndex((item) => item.id === activeId) + 1), count: items.length })}</span></div>
    <ol ref={listRef}>{items.map((item, index) => <li key={item.id}><button
      aria-current={item.id === activeId ? "step" : undefined}
      aria-label={t("exchange.run.map.turn", { index: index + 1, status: t(requestResultKey(item.reasonCode, item.status)) })}
      className={`turn-dot turn-dot-${item.status}`}
      data-turn-id={item.id}
      onClick={() => locate(item.id)}
      title={`${index + 1} · ${t(requestResultKey(item.reasonCode, item.status))} · ${new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "short" }).format(Date.parse(item.occurredAt))}`}
      type="button"
    /></li>)}</ol>
  </nav>;
}

function BackIcon() {
  return <svg aria-hidden="true" fill="none" viewBox="0 0 16 16"><path d="m9.5 3.5-4.5 4.5 4.5 4.5M5 8h7" /></svg>;
}

function ChevronIcon() {
  return <svg aria-hidden="true" fill="none" viewBox="0 0 16 16"><path d="m4 6 4 4 4-4" /></svg>;
}

function TurnLoading({ error, index, summary }: { readonly error: unknown; readonly index: number; readonly summary: ActivityRecord }) {
  const { t } = useTranslation();
  return <article className="conversation-turn" id={`run-turn-${summary.id}`} tabIndex={-1}>
    <header className="turn-heading"><span>{t("exchange.run.turn", { index: index + 1 })}</span><span className={`state-pill state-${summary.status}`}>{t(requestResultKey(summary.reasonCode, summary.status))}</span></header>
    {error === null ? <LoadingRows count={2} /> : <InlineProblem message={t(controlErrorKey(error))} />}
  </article>;
}

function SingleExchangeTurn({ current, detail, index, summary }: { readonly current: boolean; readonly detail: ExchangeDetail; readonly index: number; readonly summary: ActivityRecord | undefined }) {
  const { t, i18n } = useTranslation();
  const model = useDashboardModel();
  const [showFull, setShowFull] = useState(false);
  const full = useQuery({
    enabled: showFull && detail.content.requestProjection?.fullSnapshotAvailable === true,
    queryKey: dashboardQueryKeys.exchange(detail.id, "full"),
    queryFn: ({ signal }) => model.client.exchange(detail.id, { contentView: "full", signal }),
  });
  const displayed = showFull && full.data !== undefined ? full.data : detail;
  const request = displayed.content.request;
  const response = displayed.content.response;
  const projection = displayed.content.requestProjection;
  const checkpointCanExpand = projection?.relationship === "checkpoint" &&
    projection.totalMessageCount > 1;
  const canExpandSnapshot = projection?.fullSnapshotAvailable === true || checkpointCanExpand;
  const expandedSnapshot = projection?.view === "full" ||
    (showFull && projection?.relationship === "checkpoint");
  const reasonKey = requestReasonKey(displayed.processingTrace.result);
  return <article aria-current={current ? "step" : undefined} className={`conversation-turn${current ? " current-turn" : ""}`} id={`run-turn-${detail.id}`} tabIndex={-1}>
    <header className="turn-heading">
      <div><span>{t("exchange.run.turn", { index: index + 1 })}</span>{summary === undefined ? null : <time dateTime={summary.occurredAt}>{new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "short" }).format(Date.parse(summary.occurredAt))}</time>}</div>
      <span className={`state-pill state-${displayed.status}`}>{t(requestResultKey(summary?.reasonCode, displayed.status))}</span>
    </header>
    {displayed.content.state === "recorded" && request !== undefined ? <div className="turn-content">
      {projection !== undefined && <div className="turn-projection-row"><RequestProjectionStrip projection={projection} visibleMessages={request.messages.length} />{canExpandSnapshot && <button className="text-button" disabled={full.isFetching} onClick={() => setShowFull((value) => !value)} type="button">{t(showFull ? projection.relationship === "checkpoint" ? "exchange.content.view.compact" : "exchange.content.view.incremental" : "exchange.content.view.full")}</button>}</div>}
      <RequestConversation expandedSnapshot={expandedSnapshot} messages={request.messages} projection={projection} />
      {response === undefined ? <div aria-live="polite" className="turn-pending"><span /><div><strong>{t("exchange.run.waiting.title")}</strong><p>{t("exchange.run.waiting.description")}</p></div></div> : <article className="exchange-message exchange-message-assistant"><header><span>{t("exchange.role.assistant")}</span><small>{response.reportedModel} · {t(`exchange.stop.${response.stopReason}`)}</small></header><ExchangeBlocks blocks={response.blocks} /><UsageSummary usage={response.usage} /></article>}
    </div> : displayed.diagnosis === undefined ? <p className="muted-copy turn-empty">{t("exchange.content.empty.description")}</p> : null}
    {displayed.diagnosis !== undefined && <RequestDiagnosis diagnosis={displayed.diagnosis} result={displayed.processingTrace.result} />}
    <details className="turn-evidence"><summary>{t("exchange.run.evidence")}</summary><div>
      <dl className="facts-list">
        <dt>{t("exchange.status")}</dt><dd>{reasonKey === undefined ? displayed.processingTrace.result : t(reasonKey)}</dd>
        <dt>{t("exchange.account")}</dt><dd>{displayed.environment.accountId ?? t("exchange.account.client")}</dd>
        <dt>{t("exchange.snapshot")}</dt><dd><code title={displayed.environment.digest}>{shortValue(displayed.environment.digest)}</code></dd>
      </dl>
      <div><h3>{t("exchange.attempts.title")}</h3>{displayed.processingTrace.attempts.length === 0 ? <p className="muted-copy">{t("exchange.attempts.empty.description")}</p> : <ol className="attempt-list">{displayed.processingTrace.attempts.map((attempt) => <AttemptEvidence attempt={attempt} key={attempt.id} />)}</ol>}</div>
    </div></details>
  </article>;
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

function ExchangeMessage({ message }: { readonly message: ExchangeContentMessage }) {
  const { t } = useTranslation();
  return <article className={`exchange-message exchange-message-${message.role}`}><header><span>{t(`exchange.role.${message.role}`)}</span></header><ExchangeBlocks blocks={message.blocks} /></article>;
}

function RequestConversation({ expandedSnapshot, messages, projection }: {
  readonly expandedSnapshot: boolean;
  readonly messages: readonly ExchangeContentMessage[];
  readonly projection: ExchangeContentDetail["requestProjection"];
}) {
  const { t } = useTranslation();
  if (!expandedSnapshot) {
    // An incremental presentation already contains only the exact suffix.
    // A checkpoint is a complete rewritten client transcript, so only its
    // final message belongs in the default conversation view. The complete
    // frozen snapshot remains available through the explicit evidence action.
    const visible = projection?.relationship === "checkpoint"
      ? messages.slice(-1)
      : messages;
    return <>{visible.map((message, index) => <ExchangeMessage key={`${message.role}:current:${index}`} message={message} />)}</>;
  }
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
    if (block.kind === "reasoning") return <details className="provider-reasoning" key={key}><summary>{t("exchange.content.reasoning")}</summary><MarkdownEvidence text={block.text ?? ""} /></details>;
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
  | { readonly mode: "create"; readonly endpointId?: string }
  | { readonly mode: "replace"; readonly account: ProviderAccountRecord }
  | { readonly mode: "delete"; readonly account: ProviderAccountRecord };

export function AccountsRoutePage() {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const [dialog, setDialog] = useState<AccountDialogState>();
  const [endpointDialog, setEndpointDialog] = useState(false);
  const endpoints = useQuery({
    queryKey: dashboardQueryKeys.endpoints,
    queryFn: ({ signal }) => model.client.upstreamEndpoints(signal),
    placeholderData: (previous) => previous,
  });
  const accounts = useQuery({
    queryKey: dashboardQueryKeys.accounts,
    queryFn: ({ signal }) => model.client.providerAccounts(signal),
    placeholderData: (previous) => previous,
  });
  const records = accounts.data?.items ?? [];
  const endpointRecords = endpoints.data?.items ?? [];
  const unknownAccounts = records.filter((account) => !endpointRecords.some((endpoint) => endpoint.id === account.upstreamEndpointId));
  return (
    <div className="page accounts-page">
      <PageHeading
        actions={<div className="heading-actions"><button onClick={() => setEndpointDialog(true)} type="button">{t("accounts.endpoint.add")}</button><button className="primary-action" onClick={() => setDialog({ mode: "create" })} type="button">{t("accounts.add")}</button></div>}
        description={t("accounts.description")}
        title={t("accounts.title")}
      />
      <section className="data-panel endpoint-account-panel">
        <SectionHeading title={t("accounts.inventory", { endpoints: endpointRecords.length, accounts: records.length })} />
        {endpoints.isPending && endpoints.data === undefined ? <LoadingRows count={5} /> : endpoints.isError && endpoints.data === undefined ? <InlineProblem message={t(controlErrorKey(endpoints.error))} /> : endpointRecords.length === 0 ? <EmptyState action={<button onClick={() => setEndpointDialog(true)} type="button">{t("accounts.endpoint.add")}</button>} description={t("accounts.endpoint.empty.description")} title={t("accounts.endpoint.empty.title")} /> : <div className="endpoint-account-list">{endpointRecords.map((endpoint) => <EndpointAccountGroup accounts={records.filter((account) => account.upstreamEndpointId === endpoint.id)} endpoint={endpoint} key={endpoint.id} onAccount={(account) => setDialog({ mode: "replace", account })} onAdd={() => setDialog({ mode: "create", endpointId: endpoint.id })} onDelete={(account) => setDialog({ mode: "delete", account })} />)}</div>}
        {unknownAccounts.length > 0 && <InlineProblem message={t("accounts.endpoint.orphaned", { count: unknownAccounts.length })} />}
      </section>
      <p className="resource-boundary">{t("accounts.boundary")}</p>
      {dialog !== undefined && (dialog.mode === "delete" ? <DeleteProviderAccountDialog account={dialog.account} onClose={() => setDialog(undefined)} /> : <ProviderAccountDialog dialog={dialog} onClose={() => setDialog(undefined)} />)}
      {endpointDialog && <UpstreamEndpointDialog onClose={() => setEndpointDialog(false)} />}
    </div>
  );
}

function EndpointAccountGroup({ accounts, endpoint, onAccount, onAdd, onDelete }: { readonly accounts: readonly ProviderAccountRecord[]; readonly endpoint: UpstreamEndpointRecord; readonly onAccount: (account: ProviderAccountRecord) => void; readonly onAdd: () => void; readonly onDelete: (account: ProviderAccountRecord) => void }) {
  const { t } = useTranslation();
  return <section aria-labelledby={`endpoint-${endpoint.id}`} className="endpoint-account-group"><header><div className="endpoint-identity"><BrandIcon name={endpoint.realmId.startsWith("anthropic") ? "anthropic" : "openai"} /><span><strong id={`endpoint-${endpoint.id}`}>{endpoint.displayName}</strong><small>{endpoint.origin} · {endpoint.id}</small></span></div><div className="endpoint-group-actions"><span className={`state-pill state-${endpoint.state}`}>{t(`accounts.endpoint.state.${endpoint.state}`)}</span><span>{t("accounts.connected", { count: accounts.length })}</span><button className="quiet-button" disabled={endpoint.state !== "active"} onClick={onAdd} type="button">{t("accounts.add")}</button></div></header>{accounts.length === 0 ? <p className="endpoint-empty">{t("accounts.endpoint.noAccounts")}</p> : <div className="table-scroll"><table className="data-table responsive-table account-table"><thead><tr><th>{t("accounts.column.account")}</th><th>{t("accounts.column.provider")}</th><th>{t("accounts.column.credential")}</th><th>{t("accounts.column.revision")}</th><th className="align-right">{t("accounts.column.action")}</th></tr></thead><tbody>{accounts.map((account) => <tr key={account.id}><td data-label={t("accounts.column.account")}><div className="agent-cell"><BrandIcon name={account.kind === "openai_api_key" ? "openai" : "anthropic"} /><span><strong>{account.displayName}</strong><small>{account.id}</small></span></div></td><td data-label={t("accounts.column.provider")}>{t(`accounts.kind.${account.kind}`)}</td><td data-label={t("accounts.column.credential")}><span className={`state-pill account-health-${account.credentialState}`}>{t(`accounts.health.${account.credentialState}`)}</span></td><td data-label={t("accounts.column.revision")}>r{account.revision} · {t("accounts.credentialEpoch", { epoch: account.credentialEpoch })}</td><td className="align-right" data-label={t("accounts.column.action")}><div className="account-actions"><button className="quiet-button" onClick={() => onAccount(account)} type="button">{t("accounts.updateKey")}</button><button className="danger-text" onClick={() => onDelete(account)} type="button">{t("accounts.delete")}</button></div></td></tr>)}</tbody></table></div>}</section>;
}

function UpstreamEndpointDialog({ onClose }: { readonly onClose: () => void }) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [displayName, setDisplayName] = useState("");
  const [origin, setOrigin] = useState("");
  const [kind, setKind] = useState<UpstreamEndpointKind>("anthropic");
  const [errorKey, setErrorKey] = useState<string>();
  const create = useMutation({
    mutationFn: () => model.client.createUpstreamEndpoint({ id: newUpstreamEndpointID(kind), displayName: displayName.trim(), origin: origin.trim(), kind }),
    onError: (error) => setErrorKey(controlErrorKey(error)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dashboardQueryKeys.endpoints });
      onClose();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (displayName.trim() !== "" && origin.trim() !== "") create.mutate();
  };
  return <div className="modal-backdrop"><section aria-labelledby="upstream-endpoint-title" aria-modal="true" className="modal account-modal" role="dialog"><header><div><p className="eyebrow">{t("accounts.endpoint.dialog.eyebrow")}</p><h2 id="upstream-endpoint-title">{t("accounts.endpoint.dialog.title")}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><form onSubmit={submit}><label><span>{t("accounts.endpoint.dialog.name")}</span><input autoFocus maxLength={256} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("accounts.endpoint.dialog.namePlaceholder")} value={displayName} /></label><label><span>{t("accounts.endpoint.dialog.origin")}</span><input onChange={(event) => setOrigin(event.target.value)} placeholder="https://relay.example.com" spellCheck={false} type="url" value={origin} /></label><label><span>{t("accounts.endpoint.dialog.protocol")}</span><select onChange={(event) => setKind(event.target.value as UpstreamEndpointKind)} value={kind}><option value="anthropic">{t("accounts.endpoint.kind.anthropic")}</option><option value="openai_compatible">{t("accounts.endpoint.kind.openai_compatible")}</option></select></label><p className="credential-boundary">{t("accounts.endpoint.dialog.boundary")}</p>{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={create.isPending || displayName.trim() === "" || origin.trim() === ""} type="submit">{create.isPending ? t("accounts.dialog.saving") : t("accounts.endpoint.dialog.create")}</button></footer></form></section></div>;
}

function ProviderAccountDialog({ dialog, onClose }: { readonly dialog: Exclude<AccountDialogState, { readonly mode: "delete" }>; readonly onClose: () => void }) {
  const { t } = useTranslation();
  const model = useDashboardModel();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<ProviderAccountKind>(dialog.mode === "replace" ? dialog.account.kind : "anthropic_api_key");
  const [endpointID, setEndpointID] = useState(dialog.mode === "replace" ? dialog.account.upstreamEndpointId : dialog.endpointId ?? "");
  const [displayName, setDisplayName] = useState(dialog.mode === "replace" ? dialog.account.displayName : "");
  const [secret, setSecret] = useState("");
  const [errorKey, setErrorKey] = useState<string>();
  const endpoints = useQuery({
    queryKey: dashboardQueryKeys.endpoints,
    queryFn: ({ signal }) => model.client.upstreamEndpoints(signal),
    placeholderData: (previous) => previous,
  });
  const selectedEndpoint = endpoints.data?.items.find((endpoint) => endpoint.id === endpointID);
  useEffect(() => {
    if (selectedEndpoint !== undefined && !selectedEndpoint.accountKinds.includes(kind)) {
      setKind(selectedEndpoint.accountKinds[0] ?? "openai_api_key");
    }
  }, [kind, selectedEndpoint]);
  const save = useMutation({
    mutationFn: () => dialog.mode === "create"
      ? model.client.createProviderAccount({ id: newProviderAccountID(kind), displayName: displayName.trim(), upstreamEndpointId: endpointID, kind, secret })
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
    if (secret.length > 0 && (dialog.mode === "replace" || (displayName.trim().length > 0 && selectedEndpoint?.accountKinds.includes(kind) === true))) save.mutate();
  };
  return <div className="modal-backdrop"><section aria-labelledby="provider-account-title" aria-modal="true" className="modal account-modal" role="dialog"><header><div><p className="eyebrow">{t("accounts.dialog.eyebrow")}</p><h2 id="provider-account-title">{t(dialog.mode === "create" ? "accounts.dialog.create" : "accounts.dialog.replace", { name: dialog.mode === "replace" ? dialog.account.displayName : "" })}</h2></div><button aria-label={t("common.close")} className="icon-button" onClick={onClose} type="button">×</button></header><form onSubmit={submit}>{dialog.mode === "create" && <><label><span>{t("accounts.dialog.endpoint")}</span><select autoFocus onChange={(event) => { const id = event.target.value; const endpoint = endpoints.data?.items.find((item) => item.id === id); setEndpointID(id); if (endpoint !== undefined && !endpoint.accountKinds.includes(kind)) setKind(endpoint.accountKinds[0] ?? "openai_api_key"); }} value={endpointID}><option value="">{t("accounts.dialog.endpointPlaceholder")}</option>{endpoints.data?.items.map((endpoint) => <option disabled={endpoint.state !== "active"} key={endpoint.id} value={endpoint.id}>{endpoint.displayName} · {endpoint.origin}</option>)}</select></label>{selectedEndpoint !== undefined && <fieldset><legend>{t("accounts.dialog.credentialType")}</legend><div className="provider-picker">{selectedEndpoint.accountKinds.map((accountKind) => <button aria-pressed={kind === accountKind} key={accountKind} onClick={() => setKind(accountKind)} type="button"><BrandIcon name={accountKind === "openai_api_key" ? "openai" : "anthropic"} /><span><strong>{t(`accounts.kind.${accountKind}`)}</strong><small>{selectedEndpoint.displayName}</small></span></button>)}</div></fieldset>}<label><span>{t("accounts.dialog.name")}</span><input maxLength={256} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("accounts.dialog.namePlaceholder")} value={displayName} /></label></>}<label><span>{t(kind === "claude_oauth_token" ? "accounts.dialog.oauthToken" : "accounts.dialog.apiKey")}</span><input autoComplete="off" autoFocus={dialog.mode === "replace"} onChange={(event) => setSecret(event.target.value)} spellCheck={false} type="password" value={secret} /></label><p className="credential-boundary">{t(kind === "claude_oauth_token" ? "accounts.dialog.oauthBoundary" : "accounts.dialog.secretBoundary")}</p>{errorKey !== undefined && <InlineProblem message={t(errorKey)} />}<footer><button onClick={onClose} type="button">{t("common.cancel")}</button><button className="primary-action" disabled={save.isPending || secret.length === 0 || (dialog.mode === "create" && (displayName.trim().length === 0 || selectedEndpoint?.accountKinds.includes(kind) !== true))} type="submit">{save.isPending ? t("accounts.dialog.saving") : t(dialog.mode === "create" ? "accounts.dialog.connect" : "accounts.dialog.update")}</button></footer></form></section></div>;
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

function newUpstreamEndpointID(kind: UpstreamEndpointKind): string {
  const protocol = kind === "anthropic" ? "anthropic" : "openai";
  return `target.custom.${protocol}.${globalThis.crypto.randomUUID().toLowerCase()}`;
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

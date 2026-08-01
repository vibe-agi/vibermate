import {
  type ChangeEvent,
  type FormEvent,
  type InputHTMLAttributes,
  useEffect,
  useMemo,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
  buildAccessApplyInput,
  credentialCoordinates,
  initialAccessForm,
  type AccessFormValues,
  type CredentialCoordinates,
  validAccessForm,
} from "./access-form.ts";
import type { ControlClient } from "./control-client.ts";
import { DashboardModel, type DashboardState } from "./dashboard-model.ts";
import type {
  ActivityRecord,
  ApprovalView,
  ConnectionRecord,
  EgressAttemptRecord,
  CredentialView,
  OfflineHoldSnapshot,
  StatusResponse,
} from "./control-types.ts";
import type { SupportedLocale } from "./i18n.ts";

export interface BootstrapRootProps {
  readonly connect: () => Promise<ControlClient>;
}

export function BootstrapRoot({ connect }: BootstrapRootProps) {
  const { t } = useTranslation();
  const [client, setClient] = useState<ControlClient>();
  const [failed, setFailed] = useState(false);
  const model = useMemo(
    () => (client === undefined ? undefined : new DashboardModel(client)),
    [client],
  );

  useEffect(() => {
    let active = true;
    void connect().then(
      (connected) => {
        if (active) {
          setClient(connected);
        }
      },
      () => {
        if (active) {
          setFailed(true);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [connect]);

  if (failed) {
    return <CenteredMessage message={t("app.bootstrap.failed")} />;
  }
  if (model === undefined) {
    return <CenteredMessage message={t("app.loading")} />;
  }
  return <Dashboard model={model} />;
}

export interface DashboardProps {
  readonly model: DashboardModel;
}

export function Dashboard({ model }: DashboardProps) {
  const { t, i18n } = useTranslation();
  const [state, setState] = useState<DashboardState>(model.snapshot());

  useEffect(() => {
    const unsubscribe = model.subscribe(setState);
    model.start();
    return () => {
      unsubscribe();
      model.stop();
    };
  }, [model]);

  const changeLocale = (locale: SupportedLocale) => {
    document.documentElement.lang = locale;
    void i18n.changeLanguage(locale);
  };

  return (
    <main className="shell">
      <header className="masthead">
        <div>
          <p className="eyebrow">{t("runtime.state.initialized")}</p>
          <h1>{t("app.title")}</h1>
        </div>
        <div className="header-actions">
          <fieldset className="locale-switcher">
            <legend>{t("locale.label")}</legend>
            {(["en-US", "zh-CN"] as const).map((locale) => (
              <button
                aria-pressed={i18n.language === locale}
                key={locale}
                onClick={() => changeLocale(locale)}
                type="button"
              >
                {t(`locale.${locale}`)}
              </button>
            ))}
          </fieldset>
          <button
            className="secondary"
            disabled={state.busy}
            onClick={() => void model.refresh()}
            type="button"
          >
            {t("common.refresh.action")}
          </button>
        </div>
      </header>

      {state.errorKey !== undefined && (
        <div className="error-banner" role="alert">
          {t(state.errorKey)}
        </div>
      )}

      <div className="dashboard-grid">
        <StatusPanel status={state.status} />
        <OfflinePanel
          busy={state.busy}
          model={model}
          snapshot={state.offline}
        />
      </div>

      <AccessPanel
        activeRevision={state.activeRevision}
        busy={state.busy}
        credential={state.credential}
        model={model}
      />

      <div className="dashboard-grid lower-grid">
        <ApprovalPanel
          approvals={state.approvals}
          busy={state.busy}
          model={model}
        />
        <ActivityPanel activities={state.activities} />
      </div>

      <div className="dashboard-grid lower-grid">
        <ConnectionPanel connections={state.connections} />
        <EgressPanel attempts={state.egressAttempts} />
      </div>
    </main>
  );
}

function CenteredMessage({ message }: { readonly message: string }) {
  return (
    <main className="centered-message">
      <div className="pulse" aria-hidden="true" />
      <p>{message}</p>
    </main>
  );
}

function StatusPanel({
  status,
}: {
  readonly status: StatusResponse | undefined;
}) {
  const { t } = useTranslation();
  return (
    <section className="panel status-panel">
      <div className="section-heading">
        <h2>{t("status.title")}</h2>
        <span className={`state-dot ${status?.ready ? "online" : "attention"}`} />
      </div>
      {status !== undefined && (
        <>
          <p className="hero-state">{t(status.statusKey)}</p>
          <dl className="metrics">
            <div>
              <dt>{t("status.schemaRevision.label")}</dt>
              <dd>{status.runtime.schemaRevision}</dd>
            </div>
            <div>
              <dt>{t("status.instance.label")}</dt>
              <dd className="identifier">{status.generation}</dd>
            </div>
          </dl>
        </>
      )}
    </section>
  );
}

function OfflinePanel({
  busy,
  model,
  snapshot,
}: {
  readonly busy: boolean;
  readonly model: DashboardModel;
  readonly snapshot: OfflineHoldSnapshot | undefined;
}) {
  const { t } = useTranslation();
  const state = snapshot?.state ?? "unbound";
  return (
    <section className="panel offline-panel">
      <div className="section-heading">
        <h2>{t("offlineHold.title")}</h2>
        <span className={`hold-badge ${state}`}>{t(`offlineHold.state.${state}`)}</span>
      </div>
      {snapshot !== undefined && (
        <dl className="metrics compact">
          <div>
            <dt>{t("offlineHold.activeEgress.label")}</dt>
            <dd>{snapshot.activeEgress}</dd>
          </div>
          <div>
            <dt>{t("offlineHold.queuedRequests.label")}</dt>
            <dd>{snapshot.queuedRequests}</dd>
          </div>
          <div>
            <dt>{t("common.revision.label")}</dt>
            <dd>{snapshot.revision}</dd>
          </div>
        </dl>
      )}
      <div className="button-row">
        <button
          disabled={busy || state !== "online"}
          onClick={() => void model.enterOfflineHold()}
          type="button"
        >
          {t("offlineHold.enter.action")}
        </button>
        <button
          className="secondary"
          disabled={busy || state !== "held"}
          onClick={() => void model.resumeOfflineHold()}
          type="button"
        >
          {t("offlineHold.resume.action")}
        </button>
      </div>
    </section>
  );
}

function AccessPanel({
  activeRevision,
  busy,
  credential,
  model,
}: {
  readonly activeRevision: number | undefined;
  readonly busy: boolean;
  readonly credential: CredentialView | undefined;
  readonly model: DashboardModel;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState<AccessFormValues>(initialAccessForm);
  const [secret, setSecret] = useState("");
  const [loadedCredential, setLoadedCredential] =
    useState<CredentialCoordinates>();

  const setField =
    (field: keyof AccessFormValues) =>
    (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      setForm((current) => ({ ...current, [field]: event.target.value }));
    };

  const setAccessID = (event: ChangeEvent<HTMLInputElement>) => {
    setSecret("");
    setLoadedCredential(undefined);
    setForm((current) => ({
      ...current,
      accessId: event.target.value,
      expectedRevision: "0",
    }));
  };

  const load = async () => {
    if (form.accessId.length === 0) {
      return;
    }
    const result = await model.loadAccess(form.accessId);
    const binding = result?.accountBindings[0];
    if (result !== undefined && binding !== undefined) {
      setLoadedCredential({
        profileId: binding.profileId,
        credentialId: binding.id,
      });
      setForm((current) => ({
        ...current,
        expectedRevision: String(result.revision),
      }));
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!validAccessForm(form)) {
      return;
    }
    const result = await model.applyAccess(
      form.accessId,
      buildAccessApplyInput(form),
    );
    if (result !== undefined) {
      setLoadedCredential(credentialCoordinates(form));
      setForm((current) => ({
        ...current,
        expectedRevision: String(result.revision),
      }));
    }
  };

  const submitCredential = async (event: FormEvent) => {
    event.preventDefault();
    if (loadedCredential === undefined || secret.length === 0) {
      return;
    }
    const result = await model.replaceCredentialSecret(
      form.accessId,
      loadedCredential.profileId,
      loadedCredential.credentialId,
      secret,
    );
    if (result !== undefined) {
      setSecret("");
    }
  };
  const activeCredential =
    loadedCredential === undefined ? undefined : credential;

  return (
    <section className="panel access-panel">
      <div className="section-heading">
        <div>
          <h2>{t("access.config.title")}</h2>
          <p>{t("access.config.description")}</p>
        </div>
        {activeRevision !== undefined && (
          <span className="success-note">
            {t("access.apply.succeeded", { revision: activeRevision })}
          </span>
        )}
      </div>
      <form className="access-form" onSubmit={(event) => void submit(event)}>
        <LabeledInput
          label={t("access.id.label")}
          onChange={setAccessID}
          required
          value={form.accessId}
        />
        <LabeledInput
          label={t("access.name.label")}
          onChange={setField("name")}
          required
          value={form.name}
        />
        <LabeledInput
          label={t("access.expectedRevision.label")}
          min="0"
          onChange={setField("expectedRevision")}
          required
          type="number"
          value={form.expectedRevision}
        />
        <LabeledInput
          label={t("access.clientOrigin.label")}
          onChange={setField("clientOrigin")}
          required
          type="url"
          value={form.clientOrigin}
        />
        <LabeledInput
          label={t("access.providerOrigin.label")}
          onChange={setField("providerOrigin")}
          required
          type="url"
          value={form.providerOrigin}
        />
        <LabeledInput
          label={t("access.fixedModel.label")}
          onChange={setField("fixedModel")}
          required
          value={form.fixedModel}
        />
        <label className="field wide">
          <span>{t("access.description.label")}</span>
          <textarea onChange={setField("description")} value={form.description} />
        </label>
        <div className="form-action">
          <button
            className="secondary"
            disabled={busy || form.accessId.length === 0}
            onClick={() => void load()}
            type="button"
          >
            {t("access.load.action")}
          </button>
          <button disabled={busy || !validAccessForm(form)} type="submit">
            {t("access.apply.action")}
          </button>
        </div>
      </form>
      <form
        className="credential-form"
        onSubmit={(event) => void submitCredential(event)}
      >
        <div className="credential-copy">
          <h3>{t("credential.title")}</h3>
          <p>{t("credential.description")}</p>
          <span
            className={`credential-state ${
              activeCredential?.secretState ?? "missing"
            }`}
          >
            {t(
              `credential.state.${
                activeCredential?.secretState ?? "missing"
              }`,
            )}
            {activeCredential !== undefined && (
              <span className="credential-revision">
                {t("credential.revision", {
                  revision: activeCredential.secretRevision,
                })}
              </span>
            )}
          </span>
        </div>
        <LabeledInput
          autoComplete="off"
          disabled={loadedCredential === undefined}
          label={t("credential.secret.label")}
          onChange={(event) => setSecret(event.target.value)}
          required
          spellCheck={false}
          type="password"
          value={secret}
        />
        <div className="form-action">
          <button
            disabled={
              busy ||
              loadedCredential === undefined ||
              secret.length === 0
            }
            type="submit"
          >
            {t("credential.replace.action")}
          </button>
        </div>
      </form>
    </section>
  );
}

function LabeledInput({
  label,
  ...properties
}: {
  readonly label: string;
} & InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="field">
      <span>{label}</span>
      <input {...properties} />
    </label>
  );
}

function ApprovalPanel({
  approvals,
  busy,
  model,
}: {
  readonly approvals: readonly ApprovalView[];
  readonly busy: boolean;
  readonly model: DashboardModel;
}) {
  const { t, i18n } = useTranslation();
  const formatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "short" }),
    [i18n.language],
  );
  return (
    <section className="panel list-panel">
      <h2>{t("approvals.title")}</h2>
      {approvals.length === 0 ? (
        <p className="empty-state">{t("approval.empty")}</p>
      ) : (
        <ol className="record-list">
          {approvals.map((approval) => (
            <li key={approval.id}>
              <h3>{t(approval.titleKey)}</h3>
              <p>{t(approval.summaryKey)}</p>
              <dl className="inline-details">
                <div>
                  <dt>{t(subjectLabelKey(approval))}</dt>
                  <dd>{approvalSubject(approval)}</dd>
                </div>
                {approval.requestCount > 1 ? (
                  <div>
                    <dt>{t("approval.waiting.label")}</dt>
                    <dd>
                      {t("approval.waiting.value", {
                        count: approval.requestCount,
                      })}
                    </dd>
                  </div>
                ) : null}
                <div>
                  <dt>{t("approval.expiresAt.label")}</dt>
                  <dd>{formatter.format(new Date(approval.expiresAt))}</dd>
                </div>
              </dl>
              <div className="button-row">
                {/*
                  The window offers exactly what the runtime declared. A
                  hard-coded button can offer an answer the runtime refuses,
                  or hide one it allows.
                */}
                {approval.choices.map((choice) => (
                  <button
                    className={choice.decision === "deny" ? "danger" : undefined}
                    disabled={busy}
                    key={`${choice.decision}:${choice.scope}`}
                    onClick={() => void model.decideApproval(approval, choice)}
                    type="button"
                  >
                    {t(choice.labelKey)}
                  </button>
                ))}
              </div>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

/**
 * A question is named in the terms it is about: a host and port for a
 * connection, tool names for a tool call.
 */
function approvalSubject(approval: ApprovalView): string {
  if (approval.target !== undefined) {
    return `${approval.target.host}:${approval.target.port}`;
  }
  return approval.subjectLabels.join(", ");
}

function subjectLabelKey(approval: ApprovalView): string {
  return approval.target === undefined
    ? "approval.tools.label"
    : "approval.target.label";
}

/**
 * What connected where. Design 06 4.1 is what makes this screen possible
 * without decrypting anything: the record says who connected, where, whether
 * it was refused, whether it was read or forwarded blind, and how much
 * crossed. It never says what was sent, and neither does this panel.
 */
function ConnectionPanel({
  connections,
}: {
  readonly connections: readonly ConnectionRecord[];
}) {
  const { t } = useTranslation();
  return (
    <section className="panel list-panel">
      <h2>{t("connections.title")}</h2>
      {connections.length === 0 ? (
        <p className="empty-state">{t("connections.empty")}</p>
      ) : (
        <ol className="record-list">
          {connections.map((connection) => (
            <li key={connection.connectionId}>
              <h3>
                {connection.requestedHost}:{connection.port}
              </h3>
              <dl className="inline-details">
                <div>
                  <dt>{t("connections.source.label")}</dt>
                  <dd>
                    {connection.sourceLabel ?? t("connections.source.unknown")}
                    {" · "}
                    {t(`connections.confidence.${connection.sourceConfidence}`)}
                  </dd>
                </div>
                <div>
                  <dt>{t("connections.decision.label")}</dt>
                  <dd>
                    {connection.decision === undefined
                      ? t("connections.decision.undecided")
                      : t(`connections.decision.${connection.decision}`)}
                    {connection.ruleId === undefined
                      ? ""
                      : ` · ${connection.ruleId}`}
                  </dd>
                </div>
                <div>
                  <dt>{t("connections.decryption.label")}</dt>
                  <dd>{t(`connections.decryption.${connection.decryption}`)}</dd>
                </div>
                <div>
                  <dt>{t("connections.bytes.label")}</dt>
                  <dd>
                    {t("connections.bytes.value", {
                      up: connection.bytesUp,
                      down: connection.bytesDown,
                    })}
                  </dd>
                </div>
              </dl>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

/**
 * Where each request actually went. A connection can carry several requests,
 * so an attempt is a separate fact: the last one must not overwrite the
 * first one's destination.
 */
function EgressPanel({
  attempts,
}: {
  readonly attempts: readonly EgressAttemptRecord[];
}) {
  const { t } = useTranslation();
  return (
    <section className="panel list-panel">
      <h2>{t("egress.title")}</h2>
      {attempts.length === 0 ? (
        <p className="empty-state">{t("egress.empty")}</p>
      ) : (
        <ol className="record-list">
          {attempts.map((attempt) => (
            <li key={attempt.id}>
              <h3>{attempt.targetOrigin}</h3>
              <dl className="inline-details">
                <div>
                  <dt>{t("egress.purpose.label")}</dt>
                  <dd>{t(`egress.purpose.${attempt.purpose}`)}</dd>
                </div>
                <div>
                  <dt>{t("egress.outcome.label")}</dt>
                  <dd>
                    {attempt.terminal && attempt.outcome !== undefined
                      ? t(`egress.outcome.${attempt.outcome}`)
                      : t("egress.outcome.inFlight")}
                    {attempt.errorClass === undefined
                      ? ""
                      : ` · ${attempt.errorClass}`}
                  </dd>
                </div>
                <div>
                  <dt>{t("egress.bytes.label")}</dt>
                  <dd>
                    {t("egress.bytes.value", {
                      out: attempt.bytesOut,
                      in: attempt.bytesIn,
                    })}
                  </dd>
                </div>
              </dl>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function ActivityPanel({
  activities,
}: {
  readonly activities: readonly ActivityRecord[];
}) {
  const { t, i18n } = useTranslation();
  const formatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "short" }),
    [i18n.language],
  );
  return (
    <section className="panel list-panel">
      <h2>{t("activity.title")}</h2>
      {activities.length === 0 ? (
        <p className="empty-state">{t("activity.empty")}</p>
      ) : (
        <ol className="record-list activity-list">
          {activities.map((record) => (
            <li key={record.id}>
              <div>
                <h3>{t(`activity.kind.${record.kind}`)}</h3>
                <p>{formatter.format(new Date(record.occurredAt))}</p>
              </div>
              <span className={`activity-status ${record.status}`}>
                {t(`activity.status.${record.status}`)}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

import { useQuery } from "@tanstack/react-query";
import {
  type FormEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ControlProblem } from "./control-client.ts";
import {
  controlErrorKey,
  dashboardQueryKeys,
  type DashboardQueryRuntime,
} from "./dashboard-runtime.ts";
import type {
  ManualCaptureClientClass,
  ManualCaptureContext,
  ManualCaptureCreateInput,
  ManualCaptureGrant,
  ManualCaptureLifetime,
  ManualCapturePage,
  ManualCaptureRecord,
} from "./control-types.ts";

interface ManualCaptureForm {
  readonly displayName: string;
  readonly clientClass: ManualCaptureClientClass;
  readonly lifetime: ManualCaptureLifetime;
  readonly hours: string;
}

interface PendingAction {
  readonly kind: "rotate" | "revoke";
  readonly capture: ManualCaptureRecord;
}

const initialForm: ManualCaptureForm = {
  displayName: "",
  clientClass: "cli",
  lifetime: "temporary",
  hours: "24",
};

export function ManualCapturePanel({
  model,
}: {
  readonly model: DashboardQueryRuntime;
}) {
  const { t, i18n } = useTranslation();
  const owner = useRef(new AbortController());
  const [form, setForm] = useState(initialForm);
  const [creating, setCreating] = useState(false);
  const [review, setReview] = useState<ManualCaptureContext>();
  const [delivery, setDelivery] = useState<ManualCaptureGrant>();
  const [pendingAction, setPendingAction] = useState<PendingAction>();
  const [focusCaptureID, setFocusCaptureID] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [errorKey, setErrorKey] = useState<string>();
  const [copied, setCopied] = useState<"proxy" | "shell">();
  const confirmationHeading = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    const current = owner.current;
    return () => current.abort();
  }, []);

  useEffect(() => {
    if (pendingAction !== undefined) {
      confirmationHeading.current?.focus();
    }
  }, [pendingAction]);

  const captures = useQuery({
    queryKey: dashboardQueryKeys.manualCaptures,
    queryFn: ({ signal }) => model.client.manualCaptures(signal),
    networkMode: "always",
    refetchInterval: model.pollInterval,
    refetchIntervalInBackground: false,
    refetchOnReconnect: false,
    refetchOnWindowFocus: true,
    retry: false,
    staleTime: model.pollInterval,
  });

  const expirySeconds = useMemo(() => {
    const hours = Number(form.hours);
    return Number.isSafeInteger(hours) && hours > 0 ? hours * 60 * 60 : 0;
  }, [form.hours]);

  const resetCreate = () => {
    setCreating(false);
    setReview(undefined);
    setForm(initialForm);
    setErrorKey(undefined);
  };

  const updateForm = (next: Partial<ManualCaptureForm>) => {
    setForm((current) => ({ ...current, ...next }));
    setReview(undefined);
    setErrorKey(undefined);
  };

  const reviewCreate = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) {
      return;
    }
    if (!validDisplayName(form.displayName)) {
      setErrorKey("error.invalid_manual_capture");
      return;
    }
    if (form.lifetime === "temporary" && expirySeconds === 0) {
      setErrorKey("manualCapture.error.lifetime");
      return;
    }
    setBusy(true);
    setErrorKey(undefined);
    try {
      const context = await model.client.manualCaptureContext(owner.current.signal);
      if (
        form.lifetime === "temporary" &&
        expirySeconds > context.maxTemporarySeconds
      ) {
        setErrorKey("manualCapture.error.lifetime");
        return;
      }
      setReview(context);
    } catch (error) {
      setErrorKey(controlErrorKey(error));
    } finally {
      setBusy(false);
    }
  };

  const create = async () => {
    if (busy || review === undefined) {
      return;
    }
    const input: ManualCaptureCreateInput = {
      displayName: form.displayName,
      clientClass: form.clientClass,
      lifetime: form.lifetime,
      ...(form.lifetime === "temporary"
        ? { expiresInSeconds: expirySeconds }
        : {}),
      confirmationToken: review.confirmationToken,
    };
    setBusy(true);
    setErrorKey(undefined);
    try {
      const result = await model.client.createManualCapture(
        input,
        owner.current.signal,
      );
      publishManualCapture(model, result.grant.capture);
      setDelivery(result.grant);
      setCopied(undefined);
      resetCreate();
      void captures.refetch();
    } catch (error) {
      setErrorKey(
        error instanceof ControlProblem
          ? controlErrorKey(error)
          : "manualCapture.error.createOutcomeUnknown",
      );
      void captures.refetch();
    } finally {
      setBusy(false);
    }
  };

  const applyPendingAction = async () => {
    if (busy || pendingAction === undefined) {
      return;
    }
    setBusy(true);
    setErrorKey(undefined);
    try {
      const current = await model.client.manualCapture(
        pendingAction.capture.id,
        owner.current.signal,
      );
      if (pendingAction.kind === "rotate") {
        const result = await model.client.rotateManualCapture(
          current.capture.id,
          current.stateTag,
          owner.current.signal,
        );
        publishManualCapture(model, result.grant.capture);
        setDelivery(result.grant);
        setCopied(undefined);
      } else {
        await model.client.revokeManualCapture(
          current.capture.id,
          current.stateTag,
          owner.current.signal,
        );
        // DELETE confirms the state transition but carries no representation.
        // Publish only that confirmed state bit; the background read restores
        // server-owned timestamps and other observation fields.
        publishManualCapture(model, {
          ...current.capture,
          state: "revoked",
        });
        setFocusCaptureID(current.capture.id);
      }
      setPendingAction(undefined);
      void captures.refetch();
    } catch (error) {
      setErrorKey(
        error instanceof ControlProblem
          ? controlErrorKey(error)
          : pendingAction.kind === "rotate"
            ? "manualCapture.error.rotateOutcomeUnknown"
            : "manualCapture.error.revokeOutcomeUnknown",
      );
      void captures.refetch();
    } finally {
      setBusy(false);
    }
  };

  const copy = async (kind: "proxy" | "shell", value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(kind);
      setErrorKey(undefined);
    } catch {
      setErrorKey("manualCapture.error.copy");
    }
  };

  const records = captures.data?.items ?? [];
  return (
    <section className="panel manual-capture-panel">
      <div className="section-heading manual-capture-heading">
        <div>
          <p className="eyebrow">{t("manualCapture.eyebrow")}</p>
          <h2>{t("manualCapture.title")}</h2>
          <p>{t("manualCapture.description")}</p>
        </div>
        {!creating && review === undefined && delivery === undefined && (
          <button
            className="primary-action"
            disabled={busy || delivery !== undefined}
            onClick={() => {
              setCreating(true);
              setFocusCaptureID(undefined);
              setErrorKey(undefined);
            }}
            type="button"
          >
            {t("manualCapture.create.action")}
          </button>
        )}
      </div>

      {errorKey !== undefined && (
        <div className="boundary-note manual-capture-error" role="alert">
          {t(errorKey)}
        </div>
      )}

      {delivery !== undefined && (
        <CredentialDelivery
          copied={copied}
          grant={delivery}
          onCopy={(kind, value) => void copy(kind, value)}
          onDismiss={() => {
            setFocusCaptureID(delivery.capture.id);
            setDelivery(undefined);
            setCopied(undefined);
          }}
        />
      )}

      {creating && review === undefined && delivery === undefined && (
        <form className="manual-capture-form" onSubmit={reviewCreate}>
          <label>
            <span>{t("manualCapture.name.label")}</span>
            <input
              autoComplete="off"
              autoFocus
              maxLength={128}
              onChange={(event) => updateForm({ displayName: event.target.value })}
              placeholder={t("manualCapture.name.placeholder")}
              required
              value={form.displayName}
            />
          </label>
          <label>
            <span>{t("manualCapture.clientClass.label")}</span>
            <select
              onChange={(event) =>
                updateForm({
                  clientClass: event.target.value as ManualCaptureClientClass,
                })
              }
              value={form.clientClass}
            >
              {(["cli", "desktop_app", "other"] as const).map((clientClass) => (
                <option key={clientClass} value={clientClass}>
                  {t(`manualCapture.clientClass.${clientClass}`)}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>{t("manualCapture.lifetime.label")}</span>
            <select
              onChange={(event) =>
                updateForm({
                  lifetime: event.target.value as ManualCaptureLifetime,
                })
              }
              value={form.lifetime}
            >
              <option value="temporary">
                {t("manualCapture.lifetime.temporary")}
              </option>
              <option value="until_revoked">
                {t("manualCapture.lifetime.until_revoked")}
              </option>
            </select>
          </label>
          {form.lifetime === "temporary" && (
            <label>
              <span>{t("manualCapture.hours.label")}</span>
              <input
                inputMode="numeric"
                max={168}
                min={1}
                onChange={(event) => updateForm({ hours: event.target.value })}
                required
                type="number"
                value={form.hours}
              />
            </label>
          )}
          <p className="manual-capture-boundary">
            {t("manualCapture.routeNeutral")}
          </p>
          <div className="button-row manual-capture-form-actions">
            <button disabled={busy} type="submit">
              {t("manualCapture.review.action")}
            </button>
            <button className="secondary" onClick={resetCreate} type="button">
              {t("common.cancel.action")}
            </button>
          </div>
        </form>
      )}

      {review !== undefined && delivery === undefined && (
        <ReviewTicket
          context={review}
          form={form}
          onBack={() => setReview(undefined)}
          onCreate={() => void create()}
          busy={busy}
        />
      )}

      {pendingAction !== undefined && delivery === undefined && (
        <div className="manual-capture-confirm">
          <h3 ref={confirmationHeading} tabIndex={-1}>
            {t(`manualCapture.${pendingAction.kind}.confirmTitle`, {
              name: pendingAction.capture.displayName,
            })}
          </h3>
          <p>{t(`manualCapture.${pendingAction.kind}.confirmDetail`)}</p>
          <div className="button-row">
            <button
              className={pendingAction.kind === "revoke" ? "danger" : undefined}
              disabled={busy}
              onClick={() => void applyPendingAction()}
              type="button"
            >
              {t(`manualCapture.${pendingAction.kind}.confirmAction`)}
            </button>
            <button
              className="secondary"
              disabled={busy}
              onClick={() => {
                setFocusCaptureID(pendingAction.capture.id);
                setPendingAction(undefined);
              }}
              type="button"
            >
              {t("common.cancel.action")}
            </button>
          </div>
        </div>
      )}

      {!creating &&
        review === undefined &&
        delivery === undefined &&
        pendingAction === undefined && (
        <ManualCaptureList
          busy={busy || delivery !== undefined}
          focusRecordID={focusCaptureID}
          locale={i18n.language}
          loading={captures.isPending}
          onAction={(kind, capture) => {
            setFocusCaptureID(undefined);
            setPendingAction({ kind, capture });
          }}
          records={records}
          unavailable={captures.error !== null}
        />
      )}
    </section>
  );
}

function ReviewTicket({
  busy,
  context,
  form,
  onBack,
  onCreate,
}: {
  readonly busy: boolean;
  readonly context: ManualCaptureContext;
  readonly form: ManualCaptureForm;
  readonly onBack: () => void;
  readonly onCreate: () => void;
}) {
  const { t } = useTranslation();
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  const lifetime =
    form.lifetime === "until_revoked"
      ? t("manualCapture.lifetime.until_revoked")
      : t("manualCapture.lifetime.hours", { count: Number(form.hours) });
  return (
    <div className="manual-capture-ticket review-ticket">
      <div className="ticket-spine" aria-hidden="true">
        <span>↗</span>
      </div>
      <div className="ticket-body">
        <div className="ticket-heading">
          <div>
            <p>{t("manualCapture.review.eyebrow")}</p>
            <h3 ref={heading} tabIndex={-1}>
              {form.displayName}
            </h3>
          </div>
          <span>{t(`manualCapture.clientClass.${form.clientClass}`)}</span>
        </div>
        <dl>
          <div>
            <dt>{t("manualCapture.proxy.label")}</dt>
            <dd>{context.proxyAddress}</dd>
          </div>
          <div>
            <dt>{t("manualCapture.rootFingerprint.label")}</dt>
            <dd>{context.root.fingerprint}</dd>
          </div>
          <div>
            <dt>{t("manualCapture.rootPath.label")}</dt>
            <dd>{context.root.pemPath}</dd>
          </div>
          <div>
            <dt>{t("manualCapture.lifetime.label")}</dt>
            <dd>{lifetime}</dd>
          </div>
        </dl>
        <p className="ticket-boundary">{t("manualCapture.review.boundary")}</p>
        <div className="button-row">
          <button disabled={busy} onClick={onCreate} type="button">
            {t("manualCapture.create.confirmAction")}
          </button>
          <button className="secondary" disabled={busy} onClick={onBack} type="button">
            {t("manualCapture.review.backAction")}
          </button>
        </div>
      </div>
    </div>
  );
}

function CredentialDelivery({
  copied,
  grant,
  onCopy,
  onDismiss,
}: {
  readonly copied: "proxy" | "shell" | undefined;
  readonly grant: ManualCaptureGrant;
  readonly onCopy: (kind: "proxy" | "shell", value: string) => void;
  readonly onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), [grant.capture.id]);
  const proxy = proxyURL(grant);
  const shell = shellSetup(grant, proxy);
  return (
    <div className="manual-capture-ticket delivery-ticket">
      <div className="ticket-spine" aria-hidden="true">
        <span>✓</span>
      </div>
      <div className="ticket-body">
        <div className="ticket-heading">
          <div>
            <p>{t("manualCapture.delivery.eyebrow")}</p>
            <h3 ref={heading} tabIndex={-1}>
              {grant.capture.displayName}
            </h3>
          </div>
          <span>{t("manualCapture.delivery.once")}</span>
        </div>
        <p className="delivery-warning">{t("manualCapture.delivery.warning")}</p>
        <CopyRow
          copied={copied === "proxy"}
          label={t("manualCapture.delivery.proxy.label")}
          onCopy={() => onCopy("proxy", proxy)}
          value={proxy}
        />
        <CopyRow
          copied={copied === "shell"}
          label={t("manualCapture.delivery.shell.label")}
          onCopy={() => onCopy("shell", shell)}
          value={shell}
        />
        <button className="primary-action" onClick={onDismiss} type="button">
          {t("manualCapture.delivery.dismiss")}
        </button>
      </div>
    </div>
  );
}

function CopyRow({
  copied,
  label,
  onCopy,
  value,
}: {
  readonly copied: boolean;
  readonly label: string;
  readonly onCopy: () => void;
  readonly value: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="manual-capture-copy-row">
      <label>
        <span>{label}</span>
        <textarea
          readOnly
          rows={value.includes("\n") ? 4 : 2}
          spellCheck={false}
          value={value}
          wrap="off"
        />
      </label>
      <button onClick={onCopy} type="button">
        <span className="visually-hidden">
          {t(
            copied
              ? "manualCapture.copy.copied"
              : "manualCapture.copy.action",
            { label },
          )}
        </span>
        <span aria-hidden="true">
          {t(copied ? "common.copied" : "common.copy.action")}
        </span>
      </button>
    </div>
  );
}

function ManualCaptureList({
  busy,
  focusRecordID,
  loading,
  locale,
  onAction,
  records,
  unavailable,
}: {
  readonly busy: boolean;
  readonly focusRecordID: string | undefined;
  readonly loading: boolean;
  readonly locale: string;
  readonly onAction: (
    kind: PendingAction["kind"],
    capture: ManualCaptureRecord,
  ) => void;
  readonly records: readonly ManualCaptureRecord[];
  readonly unavailable: boolean;
}) {
  const { t } = useTranslation();
  const focusRecord = useRef<HTMLLIElement>(null);
  const canFocusRecord =
    focusRecordID !== undefined &&
    records.some((capture) => capture.id === focusRecordID);
  useEffect(() => {
    if (canFocusRecord) {
      focusRecord.current?.focus();
    }
  }, [canFocusRecord, focusRecordID]);
  if (loading) {
    return <p className="empty-state">{t("common.data.loading")}</p>;
  }
  if (unavailable && records.length === 0) {
    return <p className="empty-state">{t("common.data.unavailable")}</p>;
  }
  if (records.length === 0) {
    return <p className="empty-state">{t("manualCapture.empty")}</p>;
  }
  return (
    <ol className="manual-capture-list">
      {records.map((capture) => (
        <li
          className={capture.state === "active" ? undefined : "inactive"}
          key={capture.id}
          ref={capture.id === focusRecordID ? focusRecord : undefined}
          tabIndex={capture.id === focusRecordID ? -1 : undefined}
        >
          <div className="manual-capture-card-heading">
            <div>
              <h3>{capture.displayName}</h3>
              <p>{t(`manualCapture.clientClass.${capture.clientClass}`)}</p>
            </div>
            <span className={`manual-capture-state ${capture.state}`}>
              {t(`manualCapture.state.${capture.state}`)}
            </span>
          </div>
          <div
            className={`manual-capture-observation ${capture.observation}`}
          >
            <span aria-hidden="true" />
            <div>
              <strong>
                {t(`manualCapture.observation.${capture.observation}`)}
              </strong>
              <small>
                {capture.lastObservedAt === undefined
                  ? t("manualCapture.observation.waitingDetail")
                  : t("manualCapture.observation.observedDetail", {
                      time: formatDate(capture.lastObservedAt, locale),
                    })}
              </small>
            </div>
          </div>
          <dl className="manual-capture-meta">
            <div>
              <dt>{t("manualCapture.lifetime.label")}</dt>
              <dd>
                {capture.expiresAt === undefined
                  ? t("manualCapture.lifetime.until_revoked")
                  : t("manualCapture.expiresAt", {
                      time: formatDate(capture.expiresAt, locale),
                    })}
              </dd>
            </div>
          </dl>
          {capture.state === "active" && (
            <div className="button-row manual-capture-card-actions">
              <button
                disabled={busy}
                onClick={() => onAction("rotate", capture)}
                type="button"
              >
                {t("manualCapture.rotate.action")}
              </button>
              <button
                className="danger"
                disabled={busy}
                onClick={() => onAction("revoke", capture)}
                type="button"
              >
                {t("manualCapture.revoke.action")}
              </button>
            </div>
          )}
        </li>
      ))}
    </ol>
  );
}

function publishManualCapture(
  model: DashboardQueryRuntime,
  capture: ManualCaptureRecord,
) {
  model.queryClient.setQueryData<ManualCapturePage>(
    dashboardQueryKeys.manualCaptures,
    (current) => {
      const records = current?.items ?? [];
      const found = records.some((record) => record.id === capture.id);
      return {
        items: found
          ? records.map((record) =>
              record.id === capture.id ? capture : record,
            )
          : [capture, ...records],
      };
    },
  );
}

function proxyURL(grant: ManualCaptureGrant): string {
  const parsed = new URL(grant.proxyAddress);
  parsed.username = grant.proxyUsername;
  parsed.password = grant.proxyPassword;
  return parsed.toString();
}

function shellSetup(grant: ManualCaptureGrant, proxy: string): string {
  return [
    `export HTTPS_PROXY=${shellQuote(proxy)}`,
    `export HTTP_PROXY=${shellQuote(proxy)}`,
    `export NODE_EXTRA_CA_CERTS=${shellQuote(grant.root.pemPath)}`,
    `export SSL_CERT_FILE=${shellQuote(grant.root.pemPath)}`,
  ].join("\n");
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function validDisplayName(value: string): boolean {
  return (
    value.length > 0 &&
    value.trim() === value &&
    new TextEncoder().encode(value).byteLength <= 128 &&
    !/\p{Cc}/u.test(value)
  );
}

function formatDate(value: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

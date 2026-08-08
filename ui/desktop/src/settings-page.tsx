import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
  InlineProblem,
  LoadingRows,
  PageHeading,
  SectionHeading,
} from "./App.tsx";
import {
  inspectTerminalCommand,
  installTerminalCommand,
  refreshTerminalCommand,
  removeTerminalCommand,
  type TerminalCommandState,
  type TerminalCommandStatus,
} from "./desktop-host.ts";

const terminalCommandQueryKey = ["vibermate", "desktop", "terminal-command"] as const;

export function SettingsRoutePage({ preview }: { readonly preview: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const command = useQuery({
    enabled: !preview,
    queryFn: inspectTerminalCommand,
    queryKey: terminalCommandQueryKey,
    retry: false,
  });
  const change = useMutation({
    mutationFn: async (action: "install" | "refresh" | "remove") => {
      switch (action) {
        case "install":
          return installTerminalCommand();
        case "refresh":
          return refreshTerminalCommand();
        case "remove":
          return removeTerminalCommand();
      }
    },
    onSuccess: (value) => queryClient.setQueryData(terminalCommandQueryKey, value),
  });

  return (
    <div className="page settings-page">
      <PageHeading
        description={t("settings.description")}
        title={t("settings.title")}
      />
      <section className="data-panel terminal-command-panel">
        <SectionHeading title={t("terminalCommand.title")} />
        <p className="section-copy">{t("terminalCommand.description")}</p>
        {preview ? (
          <TerminalCommandPreview />
        ) : command.isPending ? (
          <LoadingRows count={3} />
        ) : command.data === undefined ? (
          <InlineProblem message={t("terminalCommand.error")} />
        ) : (
          <TerminalCommandControl
            busy={change.isPending}
            onAction={(action) => change.mutate(action)}
            status={change.data ?? command.data}
          />
        )}
        {change.isError && <InlineProblem message={t("terminalCommand.error")} />}
      </section>
      <section className="data-panel settings-boundary">
        <SectionHeading title={t("settings.capture.title")} />
        <p>{t("settings.capture.description")}</p>
        <code>{t("settings.capture.command")}</code>
        <small>{t("settings.capture.default")}</small>
      </section>
    </div>
  );
}

function TerminalCommandPreview() {
  const { t } = useTranslation();
  return (
    <div className="terminal-command-state">
      <span className="state-badge">{t("terminalCommand.state.desktopOnly")}</span>
      <p>{t("terminalCommand.preview")}</p>
    </div>
  );
}

function TerminalCommandControl({
  busy,
  onAction,
  status,
}: {
  readonly busy: boolean;
  readonly onAction: (action: "install" | "refresh" | "remove") => void;
  readonly status: TerminalCommandStatus;
}) {
  const { t } = useTranslation();
  const installed = status.state === "current" || status.state === "source_updated";
  return (
    <div className="terminal-command-control">
      <div className="terminal-command-summary">
        <span className={`state-badge terminal-${status.state}`}>
          {t(`terminalCommand.state.${status.state}`)}
        </span>
        <strong>{installed ? status.targetPath : t("terminalCommand.command.notInstalled")}</strong>
        <p>{commandStateDescription(status.state, t)}</p>
      </div>
      <div className="terminal-command-actions">
        {!installed && (
          <button
            className="primary-action"
            disabled={busy || status.state === "unowned_target" || status.state === "conflict"}
            onClick={() => onAction("install")}
            type="button"
          >
            {t("terminalCommand.install.action")}
          </button>
        )}
        {status.state === "source_updated" && (
          <button className="primary-action" disabled={busy} onClick={() => onAction("refresh")} type="button">
            {t("terminalCommand.refresh.action")}
          </button>
        )}
        {installed && (
          <button className="quiet-button" disabled={busy} onClick={() => onAction("remove")} type="button">
            {t("terminalCommand.remove.action")}
          </button>
        )}
      </div>
      {(status.state === "unowned_target" || status.state === "conflict") && (
        <div className="terminal-command-manual">
          <p>{status.detail ?? t("terminalCommand.error")}</p>
          <code>ln -s &apos;{status.sourcePath}&apos; &apos;{status.targetPath}&apos;</code>
        </div>
      )}
      <p className="boundary-note">{t("terminalCommand.boundary")}</p>
    </div>
  );
}

function commandStateDescription(
  state: TerminalCommandState,
  t: (key: string) => string,
): string {
  return t(`terminalCommand.stateDescription.${state}`);
}

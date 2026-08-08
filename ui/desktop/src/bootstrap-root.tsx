import {
  Component,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { DashboardRouterProvider } from "./app-router.tsx";
import {
  bootstrapFailureMessageKey,
  type DesktopFailureMessageKey,
  type DesktopRuntimeFailure,
} from "./bootstrap-problem.ts";
import type { ControlClient } from "./control-client.ts";
import { DashboardQueryRuntime } from "./dashboard-runtime.ts";

export interface BootstrapRootProps {
  readonly connect: () => Promise<ControlClient>;
  readonly observeRuntimeFailure?: (
    subscriber: (failure: DesktopRuntimeFailure) => void,
  ) => () => void;
  readonly persistNavigation?: (locator: string) => Promise<void>;
  readonly preview?: boolean;
}

export interface DesktopRenderErrorBoundaryProps {
  readonly actionLabel: string;
  readonly children: ReactNode;
  readonly message: string;
  readonly onReload: () => void;
}

interface DesktopRenderErrorBoundaryState {
  readonly failed: boolean;
}

export class DesktopRenderErrorBoundary extends Component<
  DesktopRenderErrorBoundaryProps,
  DesktopRenderErrorBoundaryState
> {
  override state: DesktopRenderErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): DesktopRenderErrorBoundaryState {
    return { failed: true };
  }

  private readonly reload = () => {
    try {
      this.props.onReload();
    } catch {
      // A refused host navigation must not displace the safe local fallback.
    }
  };

  override render() {
    if (!this.state.failed) {
      return this.props.children;
    }
    return (
      <main className="centered-message" role="alert">
        <div className="brand-mark centered-mark" aria-hidden="true">
          VM
        </div>
        <p>{this.props.message}</p>
        <button autoFocus onClick={this.reload} type="button">
          {this.props.actionLabel}
        </button>
      </main>
    );
  }
}

export function BootstrapRoot({
  connect,
  observeRuntimeFailure,
  persistNavigation,
  preview = false,
}: BootstrapRootProps) {
  const { t } = useTranslation();
  const [client, setClient] = useState<ControlClient>();
  const [failureKey, setFailureKey] = useState<DesktopFailureMessageKey>();
  const [attempt, setAttempt] = useState(0);
  const clientRef = useRef<ControlClient | undefined>(undefined);
  const models = useRef(new Set<DashboardQueryRuntime>());
  const modelRef = useRef<DashboardQueryRuntime | undefined>(undefined);
  const rootLeases = useRef(0);
  const lifecycleEpoch = useRef(0);
  const connectionAttempt = useRef<
    | {
        readonly attempt: number;
        readonly connect: BootstrapRootProps["connect"];
        readonly promise: Promise<ControlClient>;
      }
    | undefined
  >(undefined);
  const model = useMemo(() => {
    if (client === undefined) {
      return undefined;
    }
    const created = new DashboardQueryRuntime(client);
    models.current.add(created);
    return created;
  }, [client]);
  modelRef.current = model;

  useEffect(() => {
    rootLeases.current += 1;
    return () => {
      rootLeases.current -= 1;
      globalThis.setTimeout(() => {
        if (rootLeases.current === 0) {
          const owned = clientRef.current;
          clientRef.current = undefined;
          owned?.close();
          for (const ownedModel of models.current) {
            void ownedModel.dispose();
          }
          models.current.clear();
        }
      }, 0);
    };
  }, []);

  useEffect(() => {
    if (observeRuntimeFailure === undefined) {
      return;
    }
    return observeRuntimeFailure(() => {
      lifecycleEpoch.current += 1;
      connectionAttempt.current = undefined;
      clientRef.current?.close();
      clientRef.current = undefined;
      const ownedModel = modelRef.current;
      if (ownedModel !== undefined) {
        models.current.delete(ownedModel);
        void ownedModel.dispose();
      }
      setClient(undefined);
      setFailureKey("app.runtime.failure.daemon_exited");
    });
  }, [observeRuntimeFailure]);

  useEffect(() => {
    if (client !== undefined || failureKey !== undefined) {
      return;
    }
    let active = true;
    const epoch = lifecycleEpoch.current;
    let pending = connectionAttempt.current;
    if (pending?.attempt !== attempt || pending.connect !== connect) {
      pending = {
        attempt,
        connect,
        // Deferring invocation also turns a synchronous host exception into
        // the same rejected bootstrap result as an asynchronous one.
        promise: Promise.resolve().then(connect),
      };
      connectionAttempt.current = pending;
    }
    void pending.promise.then(
      (connected) => {
        if (active && lifecycleEpoch.current === epoch) {
          const previous = clientRef.current;
          if (previous !== undefined && previous !== connected) {
            previous.close();
          }
          clientRef.current = connected;
          setClient(connected);
        } else if (lifecycleEpoch.current !== epoch) {
          connected.close();
        } else {
          // StrictMode may retire one effect while a second effect is already
          // waiting on the same one-shot promise. Defer orphan cleanup until
          // every continuation for this turn had a chance to claim the client.
          queueMicrotask(() => {
            if (
              lifecycleEpoch.current === epoch &&
              clientRef.current !== connected
            ) {
              connected.close();
            }
          });
        }
      },
      (error) => {
        if (active && lifecycleEpoch.current === epoch) {
          setFailureKey(bootstrapFailureMessageKey(error));
        }
      },
    );
    return () => {
      active = false;
    };
  }, [attempt, client, connect, failureKey]);

  if (failureKey !== undefined) {
    const runtimeStopped =
      failureKey === "app.runtime.failure.daemon_exited";
    return (
      <CenteredMessage
        actionLabel={t(
          runtimeStopped ? "app.runtime.restart" : "app.bootstrap.retry",
        )}
        message={t(failureKey)}
        onAction={() => {
          setFailureKey(undefined);
          setAttempt((current) => current + 1);
        }}
      />
    );
  }
  if (model === undefined) {
    return <CenteredMessage message={t("app.loading")} />;
  }
  return (
    <DashboardRouterProvider
      model={model}
      {...(persistNavigation === undefined ? {} : { persistNavigation })}
      preview={preview}
    />
  );
}

function CenteredMessage({
  actionLabel,
  message,
  onAction,
}: {
  readonly actionLabel?: string;
  readonly message: string;
  readonly onAction?: () => void;
}) {
  const { t } = useTranslation();
  const actionable = actionLabel !== undefined && onAction !== undefined;
  return (
    <main
      aria-busy={actionable ? undefined : true}
      aria-live={actionable ? "assertive" : "polite"}
      className="centered-message"
      role={actionable ? "alert" : "status"}
    >
      <div className="brand-mark centered-mark" aria-hidden="true">
        {t("app.mark")}
      </div>
      <p>{message}</p>
      {actionable && (
        <button autoFocus onClick={onAction} type="button">
          {actionLabel}
        </button>
      )}
    </main>
  );
}

use std::error::Error;
use std::fmt::{Display, Formatter};
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use reqwest::header::{AUTHORIZATION, CACHE_CONTROL, CONTENT_TYPE};
use serde::{Deserialize, Serialize};
use tauri::{Emitter, Manager, State};
use tauri_plugin_shell::ShellExt;
use tauri_plugin_shell::process::{Command, CommandChild, CommandEvent};
use tokio::sync::watch;

mod navigation_state;

const DESCRIPTOR_SCHEMA: &str = "vibermate-daemon-bootstrap-v1";
const FAILURE_SCHEMA: &str = "vibermate-daemon-failure-v1";
const PROGRESS_SCHEMA: &str = "vibermate-daemon-progress-v1";
const PROGRESS_RUNTIME_STARTING: &str = "runtime_starting";
const SESSION_SCHEMA: &str = "vibermate-app-session-v1";
const MAXIMUM_BOOTSTRAP_BYTES: usize = 16 * 1024;
const MAXIMUM_BOOTSTRAP_FRAMES: usize = 2;
const CAPABILITY_BYTES: usize = 32;
const SIDECAR_PROGRESS_TIMEOUT: Duration = Duration::from_secs(5);
const SIDECAR_READY_TIMEOUT: Duration = Duration::from_secs(120);
const SIDECAR_SESSION_EXCHANGE_TIMEOUT: Duration = Duration::from_secs(5);
const SIDECAR_STARTUP_CANCEL_TIMEOUT: Duration = Duration::from_secs(5);
// An ordinary, preventable exit gets a short background grace period. The Go
// daemon still owns its deeper component deadlines, but the native shell must
// never make a person wait for those deadlines before the window disappears.
const SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(2);
// Cocoa can deliver applicationWillTerminate without an earlier preventable
// ExitRequested event. RunEvent::Exit is on the AppKit thread and cannot be
// deferred, so it only permits a very small final grace before force-reaping
// the owned child.
const SIDECAR_TERMINAL_EXIT_TIMEOUT: Duration = Duration::from_millis(250);
const DESKTOP_RUNTIME_EVENT: &str = "vibermate-desktop-runtime";
const DESKTOP_RUNTIME_EVENT_SCHEMA: &str = "vibermate-desktop-runtime-event-v1";
const SIDECAR_EXIT_REASON: &str = "daemon_exited";
const TERMINAL_COMMAND_SCHEMA: &str = "vibermate-terminal-command/v1";
const MAXIMUM_TERMINAL_COMMAND_BYTES: usize = 16 * 1024;

// This is the Webview asset origin sent by Tauri, not a user-facing deep-link
// scheme. Development and packaged origins are intentionally never co-enabled.
#[cfg(debug_assertions)]
const WEBVIEW_ORIGIN: &str = "http://127.0.0.1:1420";
#[cfg(not(debug_assertions))]
const WEBVIEW_ORIGIN: &str = "tauri://localhost";

#[derive(Debug)]
struct ShellError(&'static str);

impl Display for ShellError {
    fn fmt(&self, formatter: &mut Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(self.0)
    }
}

impl Error for ShellError {}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct DaemonDescriptor {
    schema: String,
    instance_id: String,
    pid: u32,
    base_url: String,
    api_versions: Vec<String>,
    event_versions: Vec<String>,
    bootstrap_nonce: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct DaemonProgress {
    schema: String,
    phase: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct DaemonFailure {
    schema: String,
    reason: String,
}

enum BootstrapFrame {
    Progress(DaemonProgress),
    Descriptor(DaemonDescriptor),
    Failure(DaemonFailure),
}

#[derive(Deserialize)]
struct BootstrapEnvelope {
    schema: String,
}

#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct ControlSession {
    schema: String,
    base_url: String,
    read_token: String,
    write_token: String,
    instance_id: String,
    expires_at: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct TerminalCommandStatus {
    schema: String,
    state: String,
    source_path: String,
    target_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    detail: String,
}

#[derive(Clone, Copy)]
enum TerminalCommandOperation {
    Status,
    Install,
    Refresh,
    Remove,
}

impl TerminalCommandOperation {
    fn argument(self) -> &'static str {
        match self {
            Self::Status => "status",
            Self::Install => "install",
            Self::Refresh => "refresh",
            Self::Remove => "remove",
        }
    }
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct DesktopRuntimeEvent {
    schema: &'static str,
    reason: &'static str,
}

trait KillTarget {
    fn kill_target(self);
}

impl KillTarget for CommandChild {
    fn kill_target(self) {
        let _ = self.kill();
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum StartupOutcome {
    Pending,
    Ready,
    Failed(&'static str),
    Stopped,
}

#[derive(Default)]
struct StartupCompletion {
    finished: Mutex<bool>,
    changed: Condvar,
}

impl StartupCompletion {
    fn mark(&self) {
        let mut finished = self
            .finished
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        *finished = true;
        self.changed.notify_all();
    }

    fn wait(&self, timeout: Duration) -> bool {
        let finished = self
            .finished
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        self.changed
            .wait_timeout_while(finished, timeout, |value| !*value)
            .map(|(value, _)| *value)
            .unwrap_or(false)
    }
}

struct StartingGeneration<C> {
    attempt_id: u64,
    child: Option<C>,
    cancel: watch::Sender<bool>,
    outcome: watch::Sender<StartupOutcome>,
    completion: Arc<StartupCompletion>,
}

enum GenerationPhase<C, S> {
    Idle,
    Starting(StartingGeneration<C>),
    ReadyUndelivered {
        attempt_id: u64,
        session: ControlSession,
        sidecar: S,
    },
    RunningDelivered {
        attempt_id: u64,
        sidecar: S,
    },
    Stopping {
        attempt_id: Option<u64>,
    },
    Stopped,
}

enum SessionDecision {
    Start {
        attempt_id: u64,
        cancellation: watch::Receiver<bool>,
        outcome: watch::Receiver<StartupOutcome>,
        completion: Arc<StartupCompletion>,
    },
    Wait {
        attempt_id: u64,
        outcome: watch::Receiver<StartupOutcome>,
    },
    Deliver(ControlSession),
    Refuse(ShellError),
}

enum GenerationStopWork<C, S> {
    Empty,
    Starting {
        child: Option<C>,
        completion: Arc<StartupCompletion>,
    },
    Running(S),
}

impl<C, S> GenerationStopWork<C, S>
where
    C: KillTarget,
    S: SidecarStopTarget,
{
    fn stop(self, startup_timeout: Duration, sidecar_timeout: Duration) {
        match self {
            Self::Empty => {}
            Self::Starting { child, completion } => {
                if let Some(child) = child {
                    child.kill_target();
                }
                let _ = completion.wait(startup_timeout);
            }
            Self::Running(sidecar) => stop_sidecar_bounded(sidecar, sidecar_timeout),
        }
    }

    fn stop_terminal(self) {
        match self {
            Self::Empty => {}
            Self::Starting { child, .. } => {
                if let Some(child) = child {
                    child.kill_target();
                }
            }
            Self::Running(sidecar) => {
                stop_sidecar_bounded(sidecar, SIDECAR_TERMINAL_EXIT_TIMEOUT);
            }
        }
    }
}

struct GenerationLifecycle<C, S> {
    phase: Mutex<GenerationPhase<C, S>>,
    next_attempt: AtomicU64,
}

impl<C, S> Default for GenerationLifecycle<C, S> {
    fn default() -> Self {
        Self {
            phase: Mutex::new(GenerationPhase::Idle),
            next_attempt: AtomicU64::new(1),
        }
    }
}

impl<C, S> GenerationLifecycle<C, S> {
    fn begin_session(&self) -> SessionDecision {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
            GenerationPhase::Idle => {
                let attempt_id = self.next_attempt.fetch_add(1, Ordering::Relaxed);
                let (cancel, cancellation) = watch::channel(false);
                let (outcome_sender, outcome) = watch::channel(StartupOutcome::Pending);
                let completion = Arc::new(StartupCompletion::default());
                *phase = GenerationPhase::Starting(StartingGeneration {
                    attempt_id,
                    child: None,
                    cancel,
                    outcome: outcome_sender,
                    completion: Arc::clone(&completion),
                });
                SessionDecision::Start {
                    attempt_id,
                    cancellation,
                    outcome,
                    completion,
                }
            }
            GenerationPhase::Starting(starting) => {
                let attempt_id = starting.attempt_id;
                let outcome = starting.outcome.subscribe();
                *phase = GenerationPhase::Starting(starting);
                SessionDecision::Wait {
                    attempt_id,
                    outcome,
                }
            }
            GenerationPhase::ReadyUndelivered {
                attempt_id,
                session,
                sidecar,
            } => {
                *phase = GenerationPhase::RunningDelivered {
                    attempt_id,
                    sidecar,
                };
                SessionDecision::Deliver(session)
            }
            running @ GenerationPhase::RunningDelivered { .. } => {
                *phase = running;
                SessionDecision::Refuse(ShellError("Control session was already consumed"))
            }
            stopping @ GenerationPhase::Stopping { .. } => {
                *phase = stopping;
                SessionDecision::Refuse(ShellError("Desktop generation is stopping"))
            }
            GenerationPhase::Stopped => {
                *phase = GenerationPhase::Stopped;
                SessionDecision::Refuse(ShellError("Desktop generation is unavailable"))
            }
        }
    }

    fn spawn_for_attempt<E, F>(&self, attempt_id: u64, spawn: F) -> Result<E, ShellError>
    where
        F: FnOnce() -> Result<(E, C), ShellError>,
    {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let GenerationPhase::Starting(starting) = &mut *phase else {
            return Err(ShellError("Desktop generation startup was cancelled"));
        };
        if starting.attempt_id != attempt_id {
            return Err(ShellError("Desktop generation startup was superseded"));
        }
        if starting.child.is_some() {
            return Err(ShellError("Desktop generation state is inconsistent"));
        }
        // Process creation and publication are one transition. Exit cannot
        // observe Starting-without-a-child after the OS process exists.
        let (events, child) = spawn()?;
        starting.child = Some(child);
        Ok(events)
    }

    fn deliver_attempt(&self, attempt_id: u64) -> Result<ControlSession, ShellError> {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
            GenerationPhase::ReadyUndelivered {
                attempt_id: current,
                session,
                sidecar,
            } if current == attempt_id => {
                *phase = GenerationPhase::RunningDelivered {
                    attempt_id,
                    sidecar,
                };
                Ok(session)
            }
            running @ GenerationPhase::RunningDelivered {
                attempt_id: current,
                ..
            } if current == attempt_id => {
                *phase = running;
                Err(ShellError("Control session was already consumed"))
            }
            previous => {
                *phase = previous;
                Err(ShellError(
                    "Desktop generation ended before session delivery",
                ))
            }
        }
    }

    fn complete_attempt<F>(&self, attempt_id: u64, session: ControlSession, build: F) -> bool
    where
        F: FnOnce(C) -> S,
    {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
            GenerationPhase::Starting(mut starting) if starting.attempt_id == attempt_id => {
                let Some(child) = starting.child.take() else {
                    *phase = GenerationPhase::Idle;
                    let _ = starting.outcome.send(StartupOutcome::Failed(
                        "Desktop generation state is inconsistent",
                    ));
                    return false;
                };
                *phase = GenerationPhase::ReadyUndelivered {
                    attempt_id,
                    session,
                    sidecar: build(child),
                };
                let _ = starting.outcome.send(StartupOutcome::Ready);
                true
            }
            GenerationPhase::Stopping {
                attempt_id: Some(stopping_attempt),
            } if stopping_attempt == attempt_id => {
                *phase = GenerationPhase::Stopping {
                    attempt_id: Some(stopping_attempt),
                };
                false
            }
            previous => {
                *phase = previous;
                false
            }
        }
    }

    fn fail_attempt(&self, attempt_id: u64, error: ShellError)
    where
        C: KillTarget,
    {
        let mut child = None;
        {
            let mut phase = self
                .phase
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner());
            match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
                GenerationPhase::Starting(mut starting) if starting.attempt_id == attempt_id => {
                    child = starting.child.take();
                    *phase = GenerationPhase::Idle;
                    let _ = starting.outcome.send(StartupOutcome::Failed(error.0));
                }
                previous => *phase = previous,
            }
        }
        if let Some(child) = child {
            child.kill_target();
        }
    }

    fn begin_stop(&self) -> GenerationStopWork<C, S> {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
            GenerationPhase::Idle => {
                *phase = GenerationPhase::Stopping { attempt_id: None };
                GenerationStopWork::Empty
            }
            GenerationPhase::Starting(mut starting) => {
                let attempt_id = starting.attempt_id;
                let _ = starting.cancel.send(true);
                let _ = starting.outcome.send(StartupOutcome::Stopped);
                let child = starting.child.take();
                let completion = Arc::clone(&starting.completion);
                *phase = GenerationPhase::Stopping {
                    attempt_id: Some(attempt_id),
                };
                GenerationStopWork::Starting { child, completion }
            }
            GenerationPhase::ReadyUndelivered { sidecar, .. }
            | GenerationPhase::RunningDelivered { sidecar, .. } => {
                *phase = GenerationPhase::Stopping { attempt_id: None };
                GenerationStopWork::Running(sidecar)
            }
            stopping @ GenerationPhase::Stopping { .. } => {
                *phase = stopping;
                GenerationStopWork::Empty
            }
            GenerationPhase::Stopped => {
                *phase = GenerationPhase::Stopped;
                GenerationStopWork::Empty
            }
        }
    }

    fn finish_stop(&self) {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if matches!(*phase, GenerationPhase::Stopping { .. }) {
            *phase = GenerationPhase::Stopped;
        }
    }

    fn sidecar_terminated(&self, attempt_id: u64) -> bool {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
            GenerationPhase::ReadyUndelivered {
                attempt_id: current,
                ..
            }
            | GenerationPhase::RunningDelivered {
                attempt_id: current,
                ..
            } if current == attempt_id => {
                // The retained sidecar is already terminal. Returning to Idle
                // invalidates the one-shot session and lets an explicit UI
                // restart create a fresh process incarnation.
                *phase = GenerationPhase::Idle;
                true
            }
            previous => {
                *phase = previous;
                false
            }
        }
    }

    fn force_stop(&self)
    where
        C: KillTarget,
        S: KillTarget,
    {
        let (child, sidecar) = {
            let mut phase = self
                .phase
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner());
            let (child, sidecar) = match std::mem::replace(&mut *phase, GenerationPhase::Stopped) {
                GenerationPhase::Starting(mut starting) => {
                    let _ = starting.cancel.send(true);
                    let _ = starting.outcome.send(StartupOutcome::Stopped);
                    (starting.child.take(), None)
                }
                GenerationPhase::ReadyUndelivered { sidecar, .. }
                | GenerationPhase::RunningDelivered { sidecar, .. } => (None, Some(sidecar)),
                _ => (None, None),
            };
            (child, sidecar)
        };
        if let Some(child) = child {
            child.kill_target();
        }
        if let Some(sidecar) = sidecar {
            sidecar.kill_target();
        }
    }
}

#[derive(Default)]
struct ShellState {
    generation: GenerationLifecycle<CommandChild, SidecarProcess>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum ExitRequestDecision {
    Start(i32),
    Wait,
    Allow,
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
enum ExitPhase {
    #[default]
    Running,
    Stopping {
        code: i32,
    },
    Ready {
        code: i32,
    },
    Exited,
}

#[derive(Default)]
struct ExitCoordinator {
    phase: Mutex<ExitPhase>,
}

impl ExitCoordinator {
    fn request(&self, requested_code: Option<i32>) -> ExitRequestDecision {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match *phase {
            ExitPhase::Running => {
                let code = requested_code.unwrap_or(0);
                *phase = ExitPhase::Stopping { code };
                ExitRequestDecision::Start(code)
            }
            ExitPhase::Stopping { .. } => ExitRequestDecision::Wait,
            ExitPhase::Ready { code } if requested_code == Some(code) => ExitRequestDecision::Allow,
            ExitPhase::Ready { .. } => ExitRequestDecision::Wait,
            ExitPhase::Exited => ExitRequestDecision::Allow,
        }
    }

    fn begin_terminal_exit(&self) -> ExitRequestDecision {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        match *phase {
            ExitPhase::Running => {
                *phase = ExitPhase::Stopping { code: 0 };
                ExitRequestDecision::Start(0)
            }
            ExitPhase::Stopping { .. } => ExitRequestDecision::Wait,
            ExitPhase::Ready { .. } | ExitPhase::Exited => ExitRequestDecision::Allow,
        }
    }

    fn stop_finished(&self) -> Option<i32> {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let ExitPhase::Stopping { code } = *phase else {
            return None;
        };
        *phase = ExitPhase::Ready { code };
        Some(code)
    }

    fn mark_exited(&self) {
        let mut phase = self
            .phase
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        *phase = ExitPhase::Exited;
    }
}

#[derive(Default)]
struct SidecarTermination {
    terminated: Mutex<bool>,
    changed: Condvar,
}

impl SidecarTermination {
    fn mark(&self) {
        if let Ok(mut terminated) = self.terminated.lock() {
            *terminated = true;
            self.changed.notify_all();
        }
    }

    fn wait(&self, timeout: Duration) -> bool {
        let Ok(terminated) = self.terminated.lock() else {
            return false;
        };
        self.changed
            .wait_timeout_while(terminated, timeout, |value| !*value)
            .map(|(value, _)| *value)
            .unwrap_or(false)
    }
}

struct SidecarProcess {
    child: CommandChild,
    termination: Arc<SidecarTermination>,
}

impl SidecarProcess {
    fn force_kill(self) {
        let _ = self.child.kill();
    }
}

impl KillTarget for SidecarProcess {
    fn kill_target(self) {
        self.force_kill();
    }
}

trait SidecarStopTarget {
    fn wait_for_termination(&self, timeout: Duration) -> bool;
    fn request_termination(&self) -> bool;
    fn kill(self);
}

impl SidecarStopTarget for SidecarProcess {
    fn wait_for_termination(&self, timeout: Duration) -> bool {
        self.termination.wait(timeout)
    }

    fn request_termination(&self) -> bool {
        #[cfg(unix)]
        {
            let pid = self.child.pid();
            // The PID comes directly from the retained packaged sidecar handle.
            (unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) } == 0)
        }
        #[cfg(not(unix))]
        false
    }

    fn kill(self) {
        self.force_kill();
    }
}

fn stop_sidecar_bounded<T: SidecarStopTarget>(sidecar: T, timeout: Duration) {
    if sidecar.wait_for_termination(Duration::ZERO) {
        return;
    }
    if sidecar.request_termination() && sidecar.wait_for_termination(timeout) {
        return;
    }
    sidecar.kill();
}

async fn wait_for_startup(mut outcome: watch::Receiver<StartupOutcome>) -> Result<(), ShellError> {
    loop {
        match *outcome.borrow_and_update() {
            StartupOutcome::Pending => {}
            StartupOutcome::Ready => return Ok(()),
            StartupOutcome::Failed(message) => return Err(ShellError(message)),
            StartupOutcome::Stopped => return Err(ShellError("Desktop generation is stopping")),
        }
        outcome
            .changed()
            .await
            .map_err(|_| ShellError("Desktop generation startup ended unexpectedly"))?;
    }
}

async fn wait_for_cancellation(cancellation: &mut watch::Receiver<bool>) {
    loop {
        if *cancellation.borrow_and_update() {
            return;
        }
        if cancellation.changed().await.is_err() {
            return;
        }
    }
}

impl ShellState {
    async fn take_session(
        self: &Arc<Self>,
        app: tauri::AppHandle,
        label: &str,
    ) -> Result<ControlSession, ShellError> {
        if label != "main" {
            return Err(ShellError(
                "Control session is not available to this Webview",
            ));
        }

        let (attempt_id, outcome) = match self.generation.begin_session() {
            SessionDecision::Start {
                attempt_id,
                cancellation,
                outcome,
                completion,
            } => {
                let owner = Arc::clone(self);
                let attempt_app = app.clone();
                drop(tauri::async_runtime::spawn(async move {
                    run_generation_attempt(
                        owner,
                        attempt_app,
                        attempt_id,
                        cancellation,
                        completion,
                    )
                    .await;
                }));
                (attempt_id, outcome)
            }
            SessionDecision::Wait {
                attempt_id,
                outcome,
            } => (attempt_id, outcome),
            SessionDecision::Deliver(session) => return Ok(session),
            SessionDecision::Refuse(error) => return Err(error),
        };
        wait_for_startup(outcome).await?;
        self.generation.deliver_attempt(attempt_id)
    }

    fn begin_stop(&self) -> GenerationStopWork<CommandChild, SidecarProcess> {
        self.generation.begin_stop()
    }

    fn finish_stop(&self) {
        self.generation.finish_stop();
    }

    fn sidecar_terminated(&self, attempt_id: u64) -> bool {
        self.generation.sidecar_terminated(attempt_id)
    }

    fn force_stop(&self) {
        self.generation.force_stop();
    }
}

#[tauri::command]
async fn take_control_session(
    window: tauri::WebviewWindow,
    app: tauri::AppHandle,
    state: State<'_, Arc<ShellState>>,
) -> Result<ControlSession, String> {
    Arc::clone(state.inner())
        .take_session(app, window.label())
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn inspect_terminal_command(
    window: tauri::WebviewWindow,
    app: tauri::AppHandle,
) -> Result<TerminalCommandStatus, String> {
    require_main_webview(&window)?;
    run_terminal_command(&app, TerminalCommandOperation::Status)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn install_terminal_command(
    window: tauri::WebviewWindow,
    app: tauri::AppHandle,
) -> Result<TerminalCommandStatus, String> {
    require_main_webview(&window)?;
    run_terminal_command(&app, TerminalCommandOperation::Install)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn refresh_terminal_command(
    window: tauri::WebviewWindow,
    app: tauri::AppHandle,
) -> Result<TerminalCommandStatus, String> {
    require_main_webview(&window)?;
    run_terminal_command(&app, TerminalCommandOperation::Refresh)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn remove_terminal_command(
    window: tauri::WebviewWindow,
    app: tauri::AppHandle,
) -> Result<TerminalCommandStatus, String> {
    require_main_webview(&window)?;
    run_terminal_command(&app, TerminalCommandOperation::Remove)
        .await
        .map_err(|error| error.to_string())
}

fn require_main_webview(window: &tauri::WebviewWindow) -> Result<(), String> {
    if window.label() != "main" {
        return Err("Terminal command is not available to this Webview".to_string());
    }
    Ok(())
}

async fn run_terminal_command(
    app: &tauri::AppHandle,
    operation: TerminalCommandOperation,
) -> Result<TerminalCommandStatus, ShellError> {
    let output = app
        .shell()
        .sidecar("vibermate")
        .map_err(|_| ShellError("Packaged terminal command is unavailable"))?
        .arg("terminal-command")
        .arg(operation.argument())
        .arg("--json")
        .output()
        .await
        .map_err(|_| ShellError("Packaged terminal command could not be inspected"))?;
    if !output.status.success() {
        return Err(ShellError("Managed terminal command could not be changed"));
    }
    decode_terminal_command_status(&output.stdout)
}

fn decode_terminal_command_status(payload: &[u8]) -> Result<TerminalCommandStatus, ShellError> {
    if payload.is_empty() || payload.len() > MAXIMUM_TERMINAL_COMMAND_BYTES {
        return Err(ShellError(
            "Managed terminal command returned invalid status",
        ));
    }
    let status: TerminalCommandStatus = serde_json::from_slice(payload)
        .map_err(|_| ShellError("Managed terminal command returned invalid status"))?;
    let valid_state = matches!(
        status.state.as_str(),
        "not_installed"
            | "current"
            | "source_updated"
            | "source_missing"
            | "target_missing"
            | "unowned_target"
            | "conflict"
    );
    if status.schema != TERMINAL_COMMAND_SCHEMA
        || !valid_state
        || status.source_path.is_empty()
        || status.target_path.is_empty()
        || !Path::new(&status.source_path).is_absolute()
        || !Path::new(&status.target_path).is_absolute()
        || status.source_path.len() > 4096
        || status.target_path.len() > 4096
        || status.detail.len() > 4096
    {
        return Err(ShellError(
            "Managed terminal command returned invalid status",
        ));
    }
    Ok(status)
}

fn generation_command(app: &tauri::AppHandle) -> Result<Command, ShellError> {
    let app_cache = absolute_utf8_path(
        app.path()
            .app_cache_dir()
            .map_err(|_| ShellError("App cache directory is unavailable"))?,
    )?;
    let app_data = absolute_utf8_path(
        app.path()
            .app_data_dir()
            .map_err(|_| ShellError("App data directory is unavailable"))?,
    )?;
    let command = app
        .shell()
        .sidecar("vibermated")
        .map_err(|_| ShellError("Packaged Desktop sidecar is unavailable"))?
        .arg(format!("--app-cache-dir={app_cache}"))
        .arg(format!("--data-dir={app_data}"))
        .arg(format!("--webview-origin={WEBVIEW_ORIGIN}"))
        .arg("--parent-lifetime-fd=0")
        .arg("--bootstrap-fd=1");
    Ok(command)
}

async fn run_generation_attempt(
    state: Arc<ShellState>,
    app: tauri::AppHandle,
    attempt_id: u64,
    mut cancellation: watch::Receiver<bool>,
    completion: Arc<StartupCompletion>,
) {
    let result = async {
        let command = generation_command(&app)?;
        let mut events = state.generation.spawn_for_attempt(attempt_id, || {
            command
                .spawn()
                .map_err(|_| ShellError("Desktop sidecar could not be started"))
        })?;
        let startup = async {
            let descriptor = receive_descriptor(&mut events).await?;
            validate_descriptor(&descriptor)?;
            exchange_session(&descriptor).await
        };
        let session = tokio::select! {
            biased;
            () = wait_for_cancellation(&mut cancellation) => {
                Err(ShellError("Desktop generation startup was cancelled"))
            }
            result = startup => result,
        }?;
        Ok((session, events))
    }
    .await;

    match result {
        Ok((session, events)) => {
            let monitor_owner = Arc::downgrade(&state);
            let monitor_app = app.clone();
            let _ = state
                .generation
                .complete_attempt(attempt_id, session, move |child| SidecarProcess {
                    child,
                    termination: monitor_sidecar(events, monitor_owner, monitor_app, attempt_id),
                });
        }
        Err(error) => state.generation.fail_attempt(attempt_id, error),
    }
    completion.mark();
}

fn monitor_sidecar(
    mut events: tauri::async_runtime::Receiver<CommandEvent>,
    owner: std::sync::Weak<ShellState>,
    app: tauri::AppHandle,
    attempt_id: u64,
) -> Arc<SidecarTermination> {
    let termination = Arc::new(SidecarTermination::default());
    let observed = Arc::clone(&termination);
    tauri::async_runtime::spawn(async move {
        loop {
            match events.recv().await {
                Some(CommandEvent::Terminated(_)) | None => {
                    observed.mark();
                    if owner
                        .upgrade()
                        .is_some_and(|state| state.sidecar_terminated(attempt_id))
                    {
                        // The event intentionally contains no process status,
                        // stderr, path, argv, environment, or capability data.
                        let _ = app.emit_to(
                            "main",
                            DESKTOP_RUNTIME_EVENT,
                            DesktopRuntimeEvent {
                                schema: DESKTOP_RUNTIME_EVENT_SCHEMA,
                                reason: SIDECAR_EXIT_REASON,
                            },
                        );
                    }
                    return;
                }
                _ => {}
            }
        }
    });
    termination
}

async fn receive_descriptor(
    events: &mut tauri::async_runtime::Receiver<CommandEvent>,
) -> Result<DaemonDescriptor, ShellError> {
    receive_descriptor_with_timeouts(events, SIDECAR_PROGRESS_TIMEOUT, SIDECAR_READY_TIMEOUT).await
}

async fn receive_descriptor_with_timeouts(
    events: &mut tauri::async_runtime::Receiver<CommandEvent>,
    progress_timeout: Duration,
    ready_timeout: Duration,
) -> Result<DaemonDescriptor, ShellError> {
    let mut reader = BootstrapFrameReader::default();
    let first = tokio::time::timeout(
        progress_timeout,
        receive_bootstrap_frame(events, &mut reader),
    )
    .await
    .map_err(|_| ShellError("Desktop bootstrap progress deadline exceeded"))??;
    match first {
        BootstrapFrame::Progress(progress) => validate_progress(&progress)?,
        BootstrapFrame::Descriptor(_) | BootstrapFrame::Failure(_) => {
            return Err(ShellError(
                "Desktop bootstrap progress contract did not match",
            ));
        }
    }
    let second = tokio::time::timeout(ready_timeout, receive_bootstrap_frame(events, &mut reader))
        .await
        .map_err(|_| ShellError("Desktop bootstrap readiness deadline exceeded"))??;
    let descriptor = match second {
        BootstrapFrame::Descriptor(descriptor) => descriptor,
        BootstrapFrame::Failure(failure) => return Err(shell_error_for_failure(&failure)?),
        BootstrapFrame::Progress(_) => {
            return Err(ShellError(
                "Desktop bootstrap descriptor contract did not match",
            ));
        }
    };
    if reader.buffer.iter().any(|byte| !byte.is_ascii_whitespace()) {
        return Err(ShellError("Desktop bootstrap contained trailing output"));
    }
    Ok(descriptor)
}

#[derive(Default)]
struct BootstrapFrameReader {
    buffer: Vec<u8>,
    bytes: usize,
    frames: usize,
}

async fn receive_bootstrap_frame(
    events: &mut tauri::async_runtime::Receiver<CommandEvent>,
    reader: &mut BootstrapFrameReader,
) -> Result<BootstrapFrame, ShellError> {
    loop {
        if let Some(newline) = reader.buffer.iter().position(|byte| *byte == b'\n') {
            reader.frames += 1;
            if reader.frames > MAXIMUM_BOOTSTRAP_FRAMES {
                return Err(ShellError("Desktop bootstrap exceeded its frame limit"));
            }
            let payload: Vec<u8> = reader.buffer.drain(..=newline).collect();
            return decode_bootstrap_frame(&payload);
        }
        match events.recv().await {
            Some(CommandEvent::Stdout(bytes)) => {
                reader.bytes = reader
                    .bytes
                    .checked_add(bytes.len())
                    .ok_or(ShellError("Desktop bootstrap exceeded its size limit"))?;
                if reader.bytes > MAXIMUM_BOOTSTRAP_BYTES {
                    return Err(ShellError("Desktop bootstrap exceeded its size limit"));
                }
                reader.buffer.extend_from_slice(&bytes);
            }
            Some(CommandEvent::Stderr(_)) => {
                // Sidecar stderr stays outside the Webview capability boundary.
            }
            Some(CommandEvent::Terminated(_)) | None => {
                return Err(ShellError("Desktop sidecar exited before bootstrap"));
            }
            _ => {}
        }
    }
}

fn decode_bootstrap_frame(payload: &[u8]) -> Result<BootstrapFrame, ShellError> {
    let envelope: BootstrapEnvelope = serde_json::from_slice(payload)
        .map_err(|_| ShellError("Desktop bootstrap was not valid JSON"))?;
    match envelope.schema.as_str() {
        PROGRESS_SCHEMA => serde_json::from_slice(payload)
            .map(BootstrapFrame::Progress)
            .map_err(|_| ShellError("Desktop bootstrap progress was not valid JSON")),
        DESCRIPTOR_SCHEMA => serde_json::from_slice(payload)
            .map(BootstrapFrame::Descriptor)
            .map_err(|_| ShellError("Desktop bootstrap descriptor was not valid JSON")),
        FAILURE_SCHEMA => serde_json::from_slice(payload)
            .map(BootstrapFrame::Failure)
            .map_err(|_| ShellError("Desktop bootstrap failure was not valid JSON")),
        _ => Err(ShellError("Desktop bootstrap schema did not match")),
    }
}

fn shell_error_for_failure(failure: &DaemonFailure) -> Result<ShellError, ShellError> {
    if failure.schema != FAILURE_SCHEMA {
        return Err(ShellError(
            "Desktop bootstrap failure contract did not match",
        ));
    }
    match failure.reason.as_str() {
        "storage_schema_newer" => Ok(ShellError("Desktop storage schema requires a newer app")),
        "storage_unavailable" => Ok(ShellError("Desktop storage could not be opened")),
        "secret_store_unavailable" => Ok(ShellError("Desktop secret storage is unavailable")),
        "runtime_already_active" => Ok(ShellError("Desktop runtime is already active")),
        "runtime_unavailable" => Ok(ShellError("Desktop runtime could not be started")),
        _ => Err(ShellError(
            "Desktop bootstrap failure contract did not match",
        )),
    }
}

fn validate_progress(progress: &DaemonProgress) -> Result<(), ShellError> {
    if progress.schema != PROGRESS_SCHEMA || progress.phase != PROGRESS_RUNTIME_STARTING {
        return Err(ShellError(
            "Desktop bootstrap progress contract did not match",
        ));
    }
    Ok(())
}

fn validate_descriptor(descriptor: &DaemonDescriptor) -> Result<(), ShellError> {
    if descriptor.schema != DESCRIPTOR_SCHEMA
        || descriptor.instance_id.is_empty()
        || descriptor.pid == 0
        || descriptor.api_versions.len() != 1
        || descriptor.api_versions[0] != "v1"
        || !descriptor.event_versions.is_empty()
        || !valid_capability(&descriptor.bootstrap_nonce)
    {
        return Err(ShellError("Desktop bootstrap contract did not match"));
    }
    validate_loopback_base_url(&descriptor.base_url)?;
    Ok(())
}

async fn exchange_session(descriptor: &DaemonDescriptor) -> Result<ControlSession, ShellError> {
    let client = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(SIDECAR_SESSION_EXCHANGE_TIMEOUT)
        .build()
        .map_err(|_| ShellError("Loopback bootstrap client could not be created"))?;
    let response = client
        .post(format!("{}/api/v1/auth/sessions", descriptor.base_url))
        .header(
            AUTHORIZATION,
            format!("Bootstrap {}", descriptor.bootstrap_nonce),
        )
        .send()
        .await
        .map_err(|_| ShellError("Desktop session exchange failed"))?;
    if response.status() != reqwest::StatusCode::CREATED
        || !header_starts_with(&response, CACHE_CONTROL, "no-store")
        || !header_starts_with(&response, CONTENT_TYPE, "application/json")
    {
        return Err(ShellError("Desktop bootstrap capability was rejected"));
    }
    let payload = response
        .bytes()
        .await
        .map_err(|_| ShellError("Desktop session response could not be read"))?;
    if payload.len() > MAXIMUM_BOOTSTRAP_BYTES {
        return Err(ShellError("Desktop session exceeded its size limit"));
    }
    let session: ControlSession = serde_json::from_slice(&payload)
        .map_err(|_| ShellError("Desktop session was not valid JSON"))?;
    validate_session(&session, descriptor)?;
    Ok(session)
}

fn validate_session(
    session: &ControlSession,
    descriptor: &DaemonDescriptor,
) -> Result<(), ShellError> {
    if session.schema != SESSION_SCHEMA
        || session.base_url != descriptor.base_url
        || session.instance_id != descriptor.instance_id
        || session.expires_at.is_empty()
        || !valid_capability(&session.read_token)
        || !valid_capability(&session.write_token)
        || session.read_token == session.write_token
    {
        return Err(ShellError("Desktop control session contract did not match"));
    }
    validate_loopback_base_url(&session.base_url)
}

fn validate_loopback_base_url(raw: &str) -> Result<(), ShellError> {
    let parsed =
        reqwest::Url::parse(raw).map_err(|_| ShellError("Desktop control URL was invalid"))?;
    let port = parsed
        .port()
        .ok_or(ShellError("Desktop control URL omitted its port"))?;
    if parsed.scheme() != "http"
        || parsed.host_str() != Some("127.0.0.1")
        || !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.path() != "/"
        || parsed.query().is_some()
        || parsed.fragment().is_some()
        || raw != format!("http://127.0.0.1:{port}")
    {
        return Err(ShellError(
            "Desktop control URL was not literal IPv4 loopback",
        ));
    }
    Ok(())
}

fn valid_capability(value: &str) -> bool {
    URL_SAFE_NO_PAD
        .decode(value)
        .is_ok_and(|decoded| decoded.len() == CAPABILITY_BYTES)
}

fn absolute_utf8_path(path: impl AsRef<Path>) -> Result<String, ShellError> {
    let path = path.as_ref();
    if !path.is_absolute() {
        return Err(ShellError("Desktop runtime path was not absolute"));
    }
    path.to_str()
        .map(str::to_owned)
        .ok_or(ShellError("Desktop runtime path was not valid UTF-8"))
}

fn header_starts_with(
    response: &reqwest::Response,
    name: reqwest::header::HeaderName,
    prefix: &str,
) -> bool {
    response
        .headers()
        .get(name)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.to_ascii_lowercase().starts_with(prefix))
}

fn navigation_locator_from_main_url(url: &reqwest::Url) -> Option<&str> {
    let expected = reqwest::Url::parse(WEBVIEW_ORIGIN).ok()?;
    if url.scheme() != expected.scheme()
        || url.host_str() != expected.host_str()
        || url.port() != expected.port()
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return None;
    }
    url.fragment()
        .filter(|locator| navigation_state::valid_navigation_locator(locator))
}

fn flush_main_window_navigation(app: &tauri::AppHandle) {
    let Some(store) = app.try_state::<Arc<navigation_state::NavigationStateStore>>() else {
        return;
    };
    let locator = app
        .get_webview_window("main")
        .and_then(|window| window.url().ok())
        .and_then(|url| navigation_locator_from_main_url(&url).map(str::to_owned));
    // Navigation restoration is optional. Refusing an unsafe locator or an I/O
    // failure must not keep the application alive during an explicit close.
    let _ = store.close_with_fragment(locator.as_deref());
}

fn destroy_main_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        // The Webview owns authenticated control-plane HTTP connections. A
        // hidden window keeps its polling requests alive and makes the daemon
        // wait for the UI that is simultaneously waiting for the daemon.
        let _ = window.destroy();
    }
}

#[cfg(unix)]
fn install_termination_signal_forwarder(app: tauri::AppHandle) -> Result<(), ShellError> {
    // Tauri setup runs on the AppKit thread, outside Tokio's entered reactor.
    // Entering the owned runtime here keeps registration synchronous and
    // fail-closed while the actual wait remains asynchronous.
    let mut termination = tauri::async_runtime::block_on(async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
    })
    .map_err(|_| ShellError("Desktop termination signal handler is unavailable"))?;
    drop(tauri::async_runtime::spawn(async move {
        // AppHandle::exit enters Tauri's ExitRequested path. Keeping this
        // forwarder one-shot prevents a repeated SIGTERM from bypassing the
        // coordinator while its bounded sidecar drain is in progress.
        if termination.recv().await.is_some() {
            app.exit(0);
        }
    }));
    Ok(())
}

#[cfg(not(unix))]
fn install_termination_signal_forwarder(_app: tauri::AppHandle) -> Result<(), ShellError> {
    Ok(())
}

pub fn run() {
    let state = Arc::new(ShellState::default());
    let exit_state = Arc::clone(&state);
    let exit_coordinator = Arc::new(ExitCoordinator::default());
    let run_exit_coordinator = Arc::clone(&exit_coordinator);
    let application = tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_shell::init())
        .manage(Arc::clone(&state))
        .setup(move |app| {
            install_termination_signal_forwarder(app.handle().clone())?;
            let app_data = app
                .path()
                .app_data_dir()
                .map_err(|_| ShellError("App data directory is unavailable"))?;
            app.manage(Arc::new(navigation_state::NavigationStateStore::new(
                app_data,
            )));
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            take_control_session,
            inspect_terminal_command,
            install_terminal_command,
            refresh_terminal_command,
            remove_terminal_command,
            navigation_state::load_navigation_state,
            navigation_state::save_navigation_state,
        ])
        .build(tauri::generate_context!())
        .expect("ViberMate Desktop shell could not be built");
    application.run(move |handle, event| match event {
        tauri::RunEvent::WindowEvent {
            label,
            event: tauri::WindowEvent::CloseRequested { .. },
            ..
        } if label == "main" => flush_main_window_navigation(handle),
        tauri::RunEvent::ExitRequested { code, api, .. } => {
            match run_exit_coordinator.request(code) {
                ExitRequestDecision::Start(_) => {
                    api.prevent_exit();
                    flush_main_window_navigation(handle);
                    destroy_main_window(handle);
                    let background_handle = handle.clone();
                    let background_state = Arc::clone(&exit_state);
                    let background_coordinator = Arc::clone(&run_exit_coordinator);
                    let stop_work = exit_state.begin_stop();
                    drop(tauri::async_runtime::spawn_blocking(move || {
                        stop_work.stop(SIDECAR_STARTUP_CANCEL_TIMEOUT, SIDECAR_SHUTDOWN_TIMEOUT);
                        background_state.finish_stop();
                        if let Some(code) = background_coordinator.stop_finished() {
                            background_handle.exit(code);
                        }
                    }));
                }
                ExitRequestDecision::Wait => {
                    api.prevent_exit();
                    destroy_main_window(handle);
                }
                ExitRequestDecision::Allow => {}
            }
        }
        tauri::RunEvent::Exit => {
            flush_main_window_navigation(handle);
            destroy_main_window(handle);
            match run_exit_coordinator.begin_terminal_exit() {
                ExitRequestDecision::Start(_) => {
                    let stop_work = exit_state.begin_stop();
                    stop_work.stop_terminal();
                    exit_state.finish_stop();
                    let _ = run_exit_coordinator.stop_finished();
                }
                // A preventable exit already owns the graceful stop. This
                // terminal AppKit callback cannot wait for it; returning also
                // closes the parent-lifetime pipe watched by the daemon.
                ExitRequestDecision::Wait => {}
                ExitRequestDecision::Allow => {}
            }
            run_exit_coordinator.mark_exited();
            exit_state.force_stop();
        }
        _ => {}
    });
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
    use std::sync::{Barrier, mpsc};
    use std::thread;

    use super::*;

    fn capability(fill: u8) -> String {
        URL_SAFE_NO_PAD.encode([fill; CAPABILITY_BYTES])
    }

    fn descriptor(base_url: String) -> DaemonDescriptor {
        DaemonDescriptor {
            schema: DESCRIPTOR_SCHEMA.to_owned(),
            instance_id: "runtime-instance".to_owned(),
            pid: 41,
            base_url,
            api_versions: vec!["v1".to_owned()],
            event_versions: vec![],
            bootstrap_nonce: capability(0x11),
        }
    }

    fn control_session() -> ControlSession {
        ControlSession {
            schema: SESSION_SCHEMA.to_owned(),
            base_url: "http://127.0.0.1:43127".to_owned(),
            read_token: capability(0x22),
            write_token: capability(0x33),
            instance_id: "runtime-instance".to_owned(),
            expires_at: "2026-07-30T00:00:00Z".to_owned(),
        }
    }

    #[test]
    fn terminal_command_status_is_closed_bounded_and_absolute() {
        let payload = serde_json::to_vec(&serde_json::json!({
            "schema": TERMINAL_COMMAND_SCHEMA,
            "state": "current",
            "sourcePath": "/Applications/ViberMate.app/Contents/MacOS/vibermate",
            "targetPath": "/Users/example/.local/bin/vibermate"
        }))
        .expect("encode terminal command status");
        let decoded =
            decode_terminal_command_status(&payload).expect("decode terminal command status");
        assert_eq!(decoded.state, "current");
        assert_eq!(decoded.detail, "");

        for invalid in [
            serde_json::json!({
                "schema": TERMINAL_COMMAND_SCHEMA,
                "state": "current",
                "sourcePath": "relative/vibermate",
                "targetPath": "/Users/example/.local/bin/vibermate"
            }),
            serde_json::json!({
                "schema": TERMINAL_COMMAND_SCHEMA,
                "state": "installed_and_trusted",
                "sourcePath": "/Applications/ViberMate.app/Contents/MacOS/vibermate",
                "targetPath": "/Users/example/.local/bin/vibermate"
            }),
            serde_json::json!({
                "schema": TERMINAL_COMMAND_SCHEMA,
                "state": "current",
                "sourcePath": "/Applications/ViberMate.app/Contents/MacOS/vibermate",
                "targetPath": "/Users/example/.local/bin/vibermate",
                "receiptPath": "/private/forbidden"
            }),
        ] {
            let payload = serde_json::to_vec(&invalid).expect("encode invalid status");
            assert!(decode_terminal_command_status(&payload).is_err());
        }
        assert!(
            decode_terminal_command_status(&vec![b'x'; MAXIMUM_TERMINAL_COMMAND_BYTES + 1])
                .is_err()
        );
    }

    #[test]
    fn terminal_command_operations_have_no_caller_controlled_arguments() {
        assert_eq!(TerminalCommandOperation::Status.argument(), "status");
        assert_eq!(TerminalCommandOperation::Install.argument(), "install");
        assert_eq!(TerminalCommandOperation::Refresh.argument(), "refresh");
        assert_eq!(TerminalCommandOperation::Remove.argument(), "remove");
    }

    fn progress_frame() -> Vec<u8> {
        let mut payload = serde_json::to_vec(&serde_json::json!({
            "schema": PROGRESS_SCHEMA,
            "phase": PROGRESS_RUNTIME_STARTING,
        }))
        .expect("encode progress frame");
        payload.push(b'\n');
        payload
    }

    fn descriptor_frame() -> Vec<u8> {
        let mut payload = serde_json::to_vec(&serde_json::json!({
            "schema": DESCRIPTOR_SCHEMA,
            "instanceId": "runtime-instance",
            "pid": 41,
            "baseUrl": "http://127.0.0.1:43127",
            "apiVersions": ["v1"],
            "eventVersions": [],
            "bootstrapNonce": capability(0x11),
        }))
        .expect("encode descriptor frame");
        payload.push(b'\n');
        payload
    }

    fn failure_frame(reason: &str) -> Vec<u8> {
        let mut payload = serde_json::to_vec(&serde_json::json!({
            "schema": FAILURE_SCHEMA,
            "reason": reason,
        }))
        .expect("encode failure frame");
        payload.push(b'\n');
        payload
    }

    struct FakeChild {
        killed: Arc<AtomicBool>,
    }

    impl KillTarget for FakeChild {
        fn kill_target(self) {
            self.killed.store(true, Ordering::SeqCst);
        }
    }

    impl SidecarStopTarget for FakeChild {
        fn wait_for_termination(&self, _timeout: Duration) -> bool {
            false
        }

        fn request_termination(&self) -> bool {
            true
        }

        fn kill(self) {
            self.kill_target();
        }
    }

    #[derive(Debug, Eq, PartialEq)]
    enum StopAction {
        Wait(Duration),
        Terminate,
        Kill,
    }

    struct IgnoringSidecar {
        actions: Arc<Mutex<Vec<StopAction>>>,
    }

    impl SidecarStopTarget for IgnoringSidecar {
        fn wait_for_termination(&self, timeout: Duration) -> bool {
            self.actions
                .lock()
                .expect("lock stop actions")
                .push(StopAction::Wait(timeout));
            false
        }

        fn request_termination(&self) -> bool {
            self.actions
                .lock()
                .expect("lock stop actions")
                .push(StopAction::Terminate);
            true
        }

        fn kill(self) {
            self.actions
                .lock()
                .expect("lock stop actions")
                .push(StopAction::Kill);
        }
    }

    struct GracefulSidecar {
        killed: Arc<AtomicBool>,
        requested: mpsc::SyncSender<()>,
        termination: Arc<SidecarTermination>,
    }

    impl SidecarStopTarget for GracefulSidecar {
        fn wait_for_termination(&self, timeout: Duration) -> bool {
            self.termination.wait(timeout)
        }

        fn request_termination(&self) -> bool {
            self.requested.send(()).is_ok()
        }

        fn kill(self) {
            self.killed.store(true, Ordering::SeqCst);
        }
    }

    #[tokio::test]
    async fn descriptor_deadline_kills_a_child_when_the_channel_stays_open() {
        let (sender, mut events) = tauri::async_runtime::channel(1);
        let killed = Arc::new(AtomicBool::new(false));
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let SessionDecision::Start {
            attempt_id,
            outcome,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("first request did not own startup");
        };
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("publish fake child");

        let error = match receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        {
            Ok(_) => panic!("descriptor deadline unexpectedly succeeded"),
            Err(error) => error,
        };
        lifecycle.fail_attempt(attempt_id, error);
        completion.mark();
        let Err(error) = wait_for_startup(outcome).await else {
            panic!("descriptor deadline unexpectedly succeeded");
        };
        assert_eq!(
            error.to_string(),
            "Desktop bootstrap progress deadline exceeded"
        );
        assert!(killed.load(Ordering::SeqCst));
        drop(sender);
    }

    #[tokio::test]
    async fn descriptor_deadline_does_not_disclose_stderr_or_a_nonce() {
        let (sender, mut events) = tauri::async_runtime::channel(1);
        let nonce = capability(0x55);
        sender
            .send(CommandEvent::Stderr(
                format!("sensitive stderr nonce={nonce}").into_bytes(),
            ))
            .await
            .expect("send sidecar stderr");

        let error = match receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        {
            Ok(_) => panic!("descriptor deadline unexpectedly succeeded"),
            Err(error) => error,
        };

        assert_eq!(
            error.to_string(),
            "Desktop bootstrap progress deadline exceeded"
        );
        assert!(!error.to_string().contains(&nonce));
        drop(sender);
    }

    #[tokio::test]
    async fn progress_opens_a_separate_runtime_readiness_deadline() {
        let (sender, mut events) = tauri::async_runtime::channel(2);
        sender
            .send(CommandEvent::Stdout(progress_frame()))
            .await
            .expect("send bootstrap progress");
        let delayed_sender = sender.clone();
        let delayed = tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(30)).await;
            delayed_sender
                .send(CommandEvent::Stdout(descriptor_frame()))
                .await
                .expect("send delayed descriptor");
        });

        let received = receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        .expect("progress should open the readiness deadline");
        assert_eq!(received.instance_id, "runtime-instance");
        delayed.await.expect("join delayed descriptor sender");
    }

    #[tokio::test]
    async fn descriptor_without_typed_progress_is_rejected() {
        let (sender, mut events) = tauri::async_runtime::channel(1);
        sender
            .send(CommandEvent::Stdout(descriptor_frame()))
            .await
            .expect("send premature descriptor");

        let error = receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        .expect_err("descriptor without progress was accepted");
        assert_eq!(
            error.to_string(),
            "Desktop bootstrap progress contract did not match"
        );
    }

    #[tokio::test]
    async fn typed_startup_failure_exposes_only_a_closed_diagnosis() {
        let (sender, mut events) = tauri::async_runtime::channel(2);
        sender
            .send(CommandEvent::Stdout(progress_frame()))
            .await
            .expect("send bootstrap progress");
        sender
            .send(CommandEvent::Stdout(failure_frame("storage_schema_newer")))
            .await
            .expect("send bootstrap failure");

        let error = receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        .expect_err("typed startup failure produced a descriptor");
        assert_eq!(
            error.to_string(),
            "Desktop storage schema requires a newer app"
        );

        let (sender, mut events) = tauri::async_runtime::channel(2);
        sender
            .send(CommandEvent::Stdout(progress_frame()))
            .await
            .expect("send bootstrap progress");
        sender
            .send(CommandEvent::Stdout(failure_frame(
                "runtime_already_active",
            )))
            .await
            .expect("send bootstrap failure");
        let error = receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        .expect_err("active runtime failure produced a descriptor");
        assert_eq!(error.to_string(), "Desktop runtime is already active");

        let (sender, mut events) = tauri::async_runtime::channel(2);
        sender
            .send(CommandEvent::Stdout(progress_frame()))
            .await
            .expect("send bootstrap progress");
        sender
            .send(CommandEvent::Stdout(failure_frame(
                "raw_database_detail_that_must_not_escape",
            )))
            .await
            .expect("send invalid bootstrap failure");
        let error = receive_descriptor_with_timeouts(
            &mut events,
            Duration::from_millis(10),
            Duration::from_millis(100),
        )
        .await
        .expect_err("open-ended startup failure was accepted");
        assert_eq!(
            error.to_string(),
            "Desktop bootstrap failure contract did not match"
        );
        assert!(!error.to_string().contains("raw_database_detail"));
    }

    #[tokio::test]
    async fn simultaneous_invokes_share_one_startup_and_only_one_gets_the_session() {
        let lifecycle = Arc::new(GenerationLifecycle::<FakeChild, FakeChild>::default());
        let barrier = Arc::new(Barrier::new(3));
        let invoke = |lifecycle: Arc<GenerationLifecycle<FakeChild, FakeChild>>,
                      barrier: Arc<Barrier>| {
            thread::spawn(move || {
                barrier.wait();
                lifecycle.begin_session()
            })
        };
        let first = invoke(Arc::clone(&lifecycle), Arc::clone(&barrier));
        let second = invoke(Arc::clone(&lifecycle), Arc::clone(&barrier));
        barrier.wait();
        let first = first.join().expect("join first invoke");
        let second = second.join().expect("join second invoke");
        let (attempt_id, first_outcome, completion, second_outcome) = match (first, second) {
            (
                SessionDecision::Start {
                    attempt_id,
                    outcome,
                    completion,
                    ..
                },
                SessionDecision::Wait {
                    attempt_id: waiting_id,
                    outcome: waiter,
                },
            )
            | (
                SessionDecision::Wait {
                    attempt_id: waiting_id,
                    outcome: waiter,
                },
                SessionDecision::Start {
                    attempt_id,
                    outcome,
                    completion,
                    ..
                },
            ) if attempt_id == waiting_id => (attempt_id, outcome, completion, waiter),
            _ => panic!("concurrent requests did not share one attempt"),
        };
        let spawn_count = Arc::new(AtomicUsize::new(0));
        let killed = Arc::new(AtomicBool::new(false));
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                spawn_count.fetch_add(1, Ordering::SeqCst);
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("spawn shared attempt");
        assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
        completion.mark();

        wait_for_startup(first_outcome)
            .await
            .expect("first waiter sees ready");
        wait_for_startup(second_outcome)
            .await
            .expect("second waiter sees ready");
        assert_eq!(spawn_count.load(Ordering::SeqCst), 1);
        lifecycle
            .deliver_attempt(attempt_id)
            .expect("first waiter receives the session");
        let error = match lifecycle.deliver_attempt(attempt_id) {
            Ok(_) => panic!("second delivery was not refused"),
            Err(error) => error,
        };
        assert_eq!(error.to_string(), "Control session was already consumed");
        assert!(!killed.load(Ordering::SeqCst));
        lifecycle.force_stop();
    }

    #[tokio::test]
    async fn failed_attempt_returns_error_and_the_next_invoke_retries() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let first_killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id: first_id,
            outcome: first_outcome,
            completion: first_completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("first invoke did not start");
        };
        lifecycle
            .spawn_for_attempt(first_id, || {
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&first_killed),
                    },
                ))
            })
            .expect("spawn first attempt");
        lifecycle.fail_attempt(first_id, ShellError("synthetic startup failure"));
        first_completion.mark();
        let error = wait_for_startup(first_outcome)
            .await
            .expect_err("failed startup must reach its waiter");
        assert_eq!(error.to_string(), "synthetic startup failure");
        assert!(first_killed.load(Ordering::SeqCst));

        let SessionDecision::Start {
            attempt_id: second_id,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("fresh invoke did not retry from Idle");
        };
        assert_ne!(second_id, first_id);
        lifecycle.fail_attempt(second_id, ShellError("end retry fixture"));
        completion.mark();
    }

    #[test]
    fn dropping_every_waiter_does_not_cancel_the_owned_startup() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id,
            outcome,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("first invoke did not start");
        };
        let SessionDecision::Wait {
            attempt_id: waiting_id,
            outcome: second_outcome,
        } = lifecycle.begin_session()
        else {
            panic!("second invoke did not wait");
        };
        assert_eq!(waiting_id, attempt_id);
        drop(outcome);
        drop(second_outcome);

        lifecycle
            .spawn_for_attempt(attempt_id, || {
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("owner task still spawns");
        assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
        completion.mark();
        assert!(matches!(
            lifecycle.begin_session(),
            SessionDecision::Deliver(_)
        ));
        assert!(!killed.load(Ordering::SeqCst));
        lifecycle.force_stop();
    }

    #[tokio::test]
    async fn exit_while_starting_cancels_kills_and_waits_for_the_owner() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id,
            mut cancellation,
            outcome,
            completion,
        } = lifecycle.begin_session()
        else {
            panic!("first invoke did not start");
        };
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("spawn pending child");

        let work = lifecycle.begin_stop();
        assert!(cancellation.changed().await.is_ok());
        assert!(*cancellation.borrow_and_update());
        assert_eq!(
            wait_for_startup(outcome)
                .await
                .expect_err("stopping attempt must reject its waiter")
                .to_string(),
            "Desktop generation is stopping"
        );
        completion.mark();
        work.stop(Duration::from_millis(10), Duration::from_millis(10));
        assert!(killed.load(Ordering::SeqCst));
        lifecycle.finish_stop();
        assert!(matches!(
            lifecycle.begin_session(),
            SessionDecision::Refuse(_)
        ));
    }

    #[test]
    fn startup_completion_racing_exit_never_leaves_an_unowned_child() {
        for _ in 0..32 {
            let lifecycle = Arc::new(GenerationLifecycle::<FakeChild, FakeChild>::default());
            let killed = Arc::new(AtomicBool::new(false));
            let SessionDecision::Start {
                attempt_id,
                completion,
                ..
            } = lifecycle.begin_session()
            else {
                panic!("first invoke did not start");
            };
            lifecycle
                .spawn_for_attempt(attempt_id, || {
                    Ok((
                        (),
                        FakeChild {
                            killed: Arc::clone(&killed),
                        },
                    ))
                })
                .expect("spawn racing child");
            let barrier = Arc::new(Barrier::new(3));
            let completing_lifecycle = Arc::clone(&lifecycle);
            let completing_barrier = Arc::clone(&barrier);
            let completing = thread::spawn(move || {
                completing_barrier.wait();
                let committed =
                    completing_lifecycle
                        .complete_attempt(attempt_id, control_session(), |child| child);
                completion.mark();
                committed
            });
            let stopping_lifecycle = Arc::clone(&lifecycle);
            let stopping_barrier = Arc::clone(&barrier);
            let stopping = thread::spawn(move || {
                stopping_barrier.wait();
                stopping_lifecycle.begin_stop()
            });
            barrier.wait();
            let _ = completing.join().expect("join completion racer");
            let work = stopping.join().expect("join stop racer");
            work.stop(Duration::from_millis(10), Duration::ZERO);
            lifecycle.finish_stop();
            assert!(killed.load(Ordering::SeqCst));
        }
    }

    #[tokio::test]
    async fn lost_delivery_response_does_not_start_a_second_daemon() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let spawn_count = Arc::new(AtomicUsize::new(0));
        let killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id,
            outcome,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("first invoke did not start");
        };
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                spawn_count.fetch_add(1, Ordering::SeqCst);
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("spawn generation");
        assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
        completion.mark();
        wait_for_startup(outcome).await.expect("startup succeeds");

        let lost_response = lifecycle
            .deliver_attempt(attempt_id)
            .expect("session was not delivered");
        drop(lost_response);
        let error = match lifecycle.deliver_attempt(attempt_id) {
            Ok(_) => panic!("response loss started or delivered another generation"),
            Err(error) => error,
        };
        assert_eq!(error.to_string(), "Control session was already consumed");
        assert_eq!(spawn_count.load(Ordering::SeqCst), 1);
        lifecycle.force_stop();
        assert!(killed.load(Ordering::SeqCst));
    }

    #[test]
    fn unexpected_exit_invalidates_ready_and_delivered_generations() {
        for deliver in [false, true] {
            let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
            let killed = Arc::new(AtomicBool::new(false));
            let SessionDecision::Start {
                attempt_id,
                completion,
                ..
            } = lifecycle.begin_session()
            else {
                panic!("fixture did not start a generation");
            };
            lifecycle
                .spawn_for_attempt(attempt_id, || {
                    Ok((
                        (),
                        FakeChild {
                            killed: Arc::clone(&killed),
                        },
                    ))
                })
                .expect("spawn fixture generation");
            assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
            completion.mark();
            if deliver {
                lifecycle
                    .deliver_attempt(attempt_id)
                    .expect("deliver fixture session");
            }

            assert!(lifecycle.sidecar_terminated(attempt_id));
            assert!(!lifecycle.sidecar_terminated(attempt_id));
            let SessionDecision::Start {
                attempt_id: restarted,
                completion,
                ..
            } = lifecycle.begin_session()
            else {
                panic!("unexpected exit did not permit an explicit restart");
            };
            assert_ne!(restarted, attempt_id);
            lifecycle.fail_attempt(restarted, ShellError("end restart fixture"));
            completion.mark();
            assert!(!killed.load(Ordering::SeqCst));
        }
    }

    #[tokio::test]
    async fn exit_between_ready_and_delivery_never_spawns_a_replacement() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let spawn_count = Arc::new(AtomicUsize::new(0));
        let killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id,
            outcome,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("fixture did not start a generation");
        };
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                spawn_count.fetch_add(1, Ordering::SeqCst);
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("spawn fixture generation");
        assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
        completion.mark();
        assert!(lifecycle.sidecar_terminated(attempt_id));
        wait_for_startup(outcome)
            .await
            .expect("the attempt reached readiness before it exited");

        let error = match lifecycle.deliver_attempt(attempt_id) {
            Ok(_) => panic!("a terminal attempt delivered a stale session"),
            Err(error) => error,
        };
        assert_eq!(
            error.to_string(),
            "Desktop generation ended before session delivery"
        );
        assert_eq!(spawn_count.load(Ordering::SeqCst), 1);

        let SessionDecision::Start {
            attempt_id: restarted,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("a later explicit invocation could not restart");
        };
        assert_ne!(restarted, attempt_id);
        lifecycle.fail_attempt(restarted, ShellError("end restart fixture"));
        completion.mark();
    }

    #[test]
    fn intentional_stop_does_not_become_an_unexpected_exit() {
        let lifecycle = GenerationLifecycle::<FakeChild, FakeChild>::default();
        let killed = Arc::new(AtomicBool::new(false));
        let SessionDecision::Start {
            attempt_id,
            completion,
            ..
        } = lifecycle.begin_session()
        else {
            panic!("fixture did not start a generation");
        };
        lifecycle
            .spawn_for_attempt(attempt_id, || {
                Ok((
                    (),
                    FakeChild {
                        killed: Arc::clone(&killed),
                    },
                ))
            })
            .expect("spawn fixture generation");
        assert!(lifecycle.complete_attempt(attempt_id, control_session(), |child| child));
        completion.mark();
        lifecycle
            .deliver_attempt(attempt_id)
            .expect("deliver fixture session");

        let stop = lifecycle.begin_stop();
        assert!(!lifecycle.sidecar_terminated(attempt_id));
        stop.stop(Duration::ZERO, Duration::ZERO);
        lifecycle.finish_stop();
        let SessionDecision::Refuse(error) = lifecycle.begin_session() else {
            panic!("stopped generation became restartable without an explicit app lifecycle");
        };
        assert_eq!(error.to_string(), "Desktop generation is unavailable");
        assert!(killed.load(Ordering::SeqCst));
    }

    #[test]
    fn runtime_exit_event_contains_only_the_closed_failure_contract() {
        let encoded = serde_json::to_value(DesktopRuntimeEvent {
            schema: DESKTOP_RUNTIME_EVENT_SCHEMA,
            reason: SIDECAR_EXIT_REASON,
        })
        .expect("serialize runtime event");
        assert_eq!(
            encoded,
            serde_json::json!({
                "schema": "vibermate-desktop-runtime-event-v1",
                "reason": "daemon_exited",
            })
        );
    }

    #[test]
    fn bounded_stop_kills_a_sidecar_that_ignores_sigterm() {
        let actions = Arc::new(Mutex::new(Vec::new()));
        let timeout = Duration::from_millis(7);

        stop_sidecar_bounded(
            IgnoringSidecar {
                actions: Arc::clone(&actions),
            },
            timeout,
        );

        assert_eq!(
            *actions.lock().expect("lock stop actions"),
            [
                StopAction::Wait(Duration::ZERO),
                StopAction::Terminate,
                StopAction::Wait(timeout),
                StopAction::Kill,
            ]
        );
    }

    #[test]
    fn repeated_exit_waits_for_the_owned_graceful_sidecar_drain() {
        let coordinator = Arc::new(ExitCoordinator::default());
        assert_eq!(coordinator.request(Some(0)), ExitRequestDecision::Start(0));
        let termination = Arc::new(SidecarTermination::default());
        let killed = Arc::new(AtomicBool::new(false));
        let (requested, request) = mpsc::sync_channel(1);
        let stopping_coordinator = Arc::clone(&coordinator);
        let stopping = thread::spawn({
            let termination = Arc::clone(&termination);
            let killed = Arc::clone(&killed);
            move || {
                stop_sidecar_bounded(
                    GracefulSidecar {
                        killed,
                        requested,
                        termination,
                    },
                    Duration::from_secs(2),
                );
                stopping_coordinator.stop_finished()
            }
        });

        request
            .recv_timeout(Duration::from_secs(1))
            .expect("graceful stop did not request sidecar termination");
        assert_eq!(coordinator.request(Some(0)), ExitRequestDecision::Wait);
        assert_eq!(coordinator.request(Some(19)), ExitRequestDecision::Wait);
        assert_eq!(coordinator.begin_terminal_exit(), ExitRequestDecision::Wait);
        termination.mark();
        assert_eq!(stopping.join().expect("join graceful stop"), Some(0));
        assert!(!killed.load(Ordering::SeqCst));
        assert_eq!(coordinator.request(Some(0)), ExitRequestDecision::Allow);
        assert_eq!(
            coordinator.begin_terminal_exit(),
            ExitRequestDecision::Allow
        );
    }

    #[test]
    fn repeated_exit_requests_start_one_stop_and_preserve_the_first_code() {
        let coordinator = ExitCoordinator::default();

        assert_eq!(
            coordinator.request(Some(17)),
            ExitRequestDecision::Start(17)
        );
        assert_eq!(coordinator.request(Some(29)), ExitRequestDecision::Wait);
        assert_eq!(coordinator.request(None), ExitRequestDecision::Wait);
        assert_eq!(coordinator.stop_finished(), Some(17));
        assert_eq!(coordinator.request(Some(29)), ExitRequestDecision::Wait);
        assert_eq!(coordinator.request(Some(17)), ExitRequestDecision::Allow);
        assert_eq!(coordinator.stop_finished(), None);

        coordinator.mark_exited();
        assert_eq!(coordinator.request(None), ExitRequestDecision::Allow);
        assert_eq!(
            coordinator.begin_terminal_exit(),
            ExitRequestDecision::Allow
        );
    }

    #[test]
    fn terminal_exit_starts_the_same_bounded_stop_when_no_request_preceded_it() {
        let coordinator = ExitCoordinator::default();

        assert_eq!(
            coordinator.begin_terminal_exit(),
            ExitRequestDecision::Start(0)
        );
        assert_eq!(coordinator.stop_finished(), Some(0));
    }

    #[test]
    fn terminal_exit_force_reaps_an_unresponsive_sidecar_within_the_ui_budget() {
        let actions = Arc::new(Mutex::new(Vec::new()));

        GenerationStopWork::<FakeChild, IgnoringSidecar>::Running(IgnoringSidecar {
            actions: Arc::clone(&actions),
        })
        .stop_terminal();

        assert_eq!(
            *actions.lock().expect("lock terminal stop actions"),
            [
                StopAction::Wait(Duration::ZERO),
                StopAction::Terminate,
                StopAction::Wait(SIDECAR_TERMINAL_EXIT_TIMEOUT),
                StopAction::Kill,
            ]
        );
        assert!(SIDECAR_TERMINAL_EXIT_TIMEOUT <= Duration::from_millis(250));
    }

    #[test]
    fn main_window_exit_uses_only_a_safe_fragment_from_the_app_origin() {
        let safe = reqwest::Url::parse(&format!(
            "{WEBVIEW_ORIGIN}/?ambient=ignored#activity/requests/ex204"
        ))
        .expect("parse safe Webview URL");
        assert_eq!(
            navigation_locator_from_main_url(&safe),
            Some("activity/requests/ex204"),
        );

        for raw in [
            "https://example.test/#captures".to_owned(),
            format!("{WEBVIEW_ORIGIN}/#not-a-real-route"),
            format!("{WEBVIEW_ORIGIN}/#captures?body=prompt-text"),
            format!("{WEBVIEW_ORIGIN}/#policies/approvals?selected=secret%3A%2F%2Fprovider%2Fwork"),
        ] {
            let url = reqwest::Url::parse(&raw).expect("parse refused Webview URL");
            assert_eq!(navigation_locator_from_main_url(&url), None, "{raw}");
        }
    }

    #[test]
    fn loopback_url_validation_rejects_aliases_and_ambient_authorities() {
        for value in [
            "http://localhost:43127",
            "http://127.0.0.1",
            "http://127.0.0.1:43127/",
            "http://127.0.0.1:43127/path",
            "http://user@127.0.0.1:43127",
            "http://127.0.0.1:43127?query=yes",
            "https://127.0.0.1:43127",
            "http://[::1]:43127",
        ] {
            assert!(validate_loopback_base_url(value).is_err(), "{value}");
        }
        assert!(validate_loopback_base_url("http://127.0.0.1:43127").is_ok());
    }

    #[tokio::test]
    async fn bootstrap_exchange_uses_exact_route_and_returns_bounded_session() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind test listener");
        let address = listener.local_addr().expect("read test listener address");
        let base_url = format!("http://{address}");
        let descriptor = descriptor(base_url.clone());
        let nonce = descriptor.bootstrap_nonce.clone();
        let session_payload = serde_json::json!({
            "schema": SESSION_SCHEMA,
            "baseUrl": base_url,
            "readToken": capability(0x22),
            "writeToken": capability(0x33),
            "instanceId": descriptor.instance_id,
            "expiresAt": "2026-07-30T00:00:00Z"
        })
        .to_string();
        let server = thread::spawn(move || {
            let (mut connection, _) = listener.accept().expect("accept bootstrap request");
            let mut request = [0_u8; 8192];
            let count = connection
                .read(&mut request)
                .expect("read bootstrap request");
            let request = String::from_utf8_lossy(&request[..count]);
            assert!(request.starts_with("POST /api/v1/auth/sessions HTTP/1.1\r\n"));
            assert!(
                request
                    .to_ascii_lowercase()
                    .contains(&format!("authorization: bootstrap {nonce}").to_ascii_lowercase())
            );
            let response = format!(
                "HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                session_payload.len(),
                session_payload,
            );
            connection
                .write_all(response.as_bytes())
                .expect("write bootstrap response");
        });

        let session = exchange_session(&descriptor)
            .await
            .expect("exchange control session");
        assert_eq!(session.base_url, descriptor.base_url);
        assert_eq!(session.instance_id, descriptor.instance_id);
        assert_ne!(session.read_token, session.write_token);
        server.join().expect("join bootstrap test server");
    }

    #[test]
    fn main_webview_capability_and_csp_remain_narrow() {
        let capability: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/main.json"))
                .expect("parse main capability");
        assert_eq!(capability["webviews"], serde_json::json!(["main"]));
        assert_eq!(
            capability["permissions"],
            serde_json::json!([
                "core:default",
                "allow-take-control-session",
                "allow-inspect-terminal-command",
                "allow-install-terminal-command",
                "allow-refresh-terminal-command",
                "allow-remove-terminal-command",
                "allow-load-navigation-state",
                "allow-save-navigation-state"
            ])
        );

        let configuration: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json"))
                .expect("parse Tauri configuration");
        assert!(configuration["app"]["security"]["csp"].is_null());
        assert_eq!(configuration["build"]["devUrl"], "http://127.0.0.1:1420",);
        assert_eq!(configuration["build"]["frontendDist"], "../dist");
        #[cfg(debug_assertions)]
        assert_eq!(WEBVIEW_ORIGIN, "http://127.0.0.1:1420");
        #[cfg(not(debug_assertions))]
        assert_eq!(WEBVIEW_ORIGIN, "tauri://localhost");
        assert_eq!(
            configuration["bundle"]["resources"]["binaries/vibermate-build-manifest.json"],
            "vibermate-build-manifest.json"
        );
        let document = include_str!("../../index.html");
        assert!(document.contains("http://127.0.0.1:*"));
        assert!(document.contains("aria-live=\"polite\""));
        assert!(document.contains("<p>ViberMate…</p>"));
        assert!(!document.contains("connect-src 'self' http://localhost"));
        assert!(!document.contains("unsafe-eval"));
    }
}

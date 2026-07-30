use std::error::Error;
use std::fmt::{Display, Formatter};
use std::path::Path;
use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use reqwest::header::{AUTHORIZATION, CACHE_CONTROL, CONTENT_TYPE};
use serde::{Deserialize, Serialize};
use tauri::{Manager, State};
use tauri_plugin_shell::ShellExt;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};

const DESCRIPTOR_SCHEMA: &str = "vibermate-daemon-bootstrap-v1";
const SESSION_SCHEMA: &str = "vibermate-app-session-v1";
const MAXIMUM_BOOTSTRAP_BYTES: usize = 16 * 1024;
const CAPABILITY_BYTES: usize = 32;
const SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(30);

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

#[derive(Deserialize)]
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

#[derive(Default)]
struct ShellState {
    session: Mutex<Option<ControlSession>>,
    sidecar: Mutex<Option<SidecarProcess>>,
}

impl ShellState {
    fn install(&self, session: ControlSession, sidecar: SidecarProcess) -> Result<(), ShellError> {
        let mut session_slot = self
            .session
            .lock()
            .map_err(|_| ShellError("Desktop session state is unavailable"))?;
        let mut child_slot = self
            .sidecar
            .lock()
            .map_err(|_| ShellError("Desktop sidecar state is unavailable"))?;
        if session_slot.is_some() || child_slot.is_some() {
            return Err(ShellError("Desktop generation was already installed"));
        }
        *session_slot = Some(session);
        *child_slot = Some(sidecar);
        Ok(())
    }

    fn take_session(&self, label: &str) -> Result<ControlSession, ShellError> {
        if label != "main" {
            return Err(ShellError(
                "Control session is not available to this Webview",
            ));
        }
        self.session
            .lock()
            .map_err(|_| ShellError("Desktop session state is unavailable"))?
            .take()
            .ok_or(ShellError("Control session was already consumed"))
    }

    fn stop_sidecar(&self) {
        let sidecar = self.sidecar.lock().ok().and_then(|mut slot| slot.take());
        if let Some(sidecar) = sidecar {
            sidecar.stop();
        }
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
    fn stop(self) {
        if self.termination.wait(Duration::ZERO) {
            return;
        }
        #[cfg(unix)]
        {
            let pid = self.child.pid();
            // The PID comes directly from the retained packaged sidecar handle.
            let signaled = unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) } == 0;
            if signaled && self.termination.wait(SIDECAR_SHUTDOWN_TIMEOUT) {
                return;
            }
        }
        let _ = self.child.kill();
    }
}

#[tauri::command]
fn take_control_session(
    window: tauri::WebviewWindow,
    state: State<'_, Arc<ShellState>>,
) -> Result<ControlSession, String> {
    state
        .take_session(window.label())
        .map_err(|error| error.to_string())
}

async fn start_generation(
    app: &tauri::AppHandle,
) -> Result<(ControlSession, SidecarProcess), ShellError> {
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
        .arg("--bootstrap-fd=1");
    let (mut events, child) = command
        .spawn()
        .map_err(|_| ShellError("Desktop sidecar could not be started"))?;

    let result = async {
        let descriptor = receive_descriptor(&mut events).await?;
        validate_descriptor(&descriptor)?;
        exchange_session(&descriptor).await
    }
    .await;
    match result {
        Ok(session) => {
            let termination = monitor_sidecar(events);
            Ok((session, SidecarProcess { child, termination }))
        }
        Err(error) => {
            let _ = child.kill();
            Err(error)
        }
    }
}

fn monitor_sidecar(
    mut events: tauri::async_runtime::Receiver<CommandEvent>,
) -> Arc<SidecarTermination> {
    let termination = Arc::new(SidecarTermination::default());
    let observed = Arc::clone(&termination);
    tauri::async_runtime::spawn(async move {
        loop {
            match events.recv().await {
                Some(CommandEvent::Terminated(_)) | None => {
                    observed.mark();
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
    let mut buffer = Vec::new();
    loop {
        match events.recv().await {
            Some(CommandEvent::Stdout(bytes)) => {
                buffer.extend_from_slice(&bytes);
                if buffer.len() > MAXIMUM_BOOTSTRAP_BYTES {
                    return Err(ShellError("Desktop bootstrap exceeded its size limit"));
                }
                if let Some(newline) = buffer.iter().position(|byte| *byte == b'\n') {
                    if buffer[newline + 1..]
                        .iter()
                        .any(|byte| !byte.is_ascii_whitespace())
                    {
                        return Err(ShellError("Desktop bootstrap contained trailing output"));
                    }
                    return serde_json::from_slice(&buffer[..newline])
                        .map_err(|_| ShellError("Desktop bootstrap was not valid JSON"));
                }
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
        .timeout(Duration::from_secs(5))
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

pub fn run() {
    let state = Arc::new(ShellState::default());
    let setup_state = Arc::clone(&state);
    let exit_state = Arc::clone(&state);
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
            let (session, sidecar) =
                tauri::async_runtime::block_on(start_generation(&app.handle().clone()))?;
            setup_state.install(session, sidecar)?;
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![take_control_session])
        .build(tauri::generate_context!())
        .expect("VibeMate Desktop shell could not be built");
    application.run(move |_handle, event| {
        if matches!(
            event,
            tauri::RunEvent::Exit | tauri::RunEvent::ExitRequested { .. }
        ) {
            exit_state.stop_sidecar();
        }
    });
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};
    use std::net::TcpListener;
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
                    .contains(&format!("authorization: bootstrap {}", nonce).to_ascii_lowercase())
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
            serde_json::json!(["core:default", "allow-take-control-session"])
        );

        let configuration: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json"))
                .expect("parse Tauri configuration");
        assert!(configuration["app"]["security"]["csp"].is_null());
        assert_eq!(configuration["build"]["devUrl"], WEBVIEW_ORIGIN);
        assert_eq!(
            configuration["bundle"]["resources"]["binaries/vibermate-build-manifest.json"],
            "vibermate-build-manifest.json"
        );
        let document = include_str!("../../index.html");
        assert!(document.contains("http://127.0.0.1:*"));
        assert!(!document.contains("connect-src 'self' http://localhost"));
        assert!(!document.contains("unsafe-eval"));
    }
}

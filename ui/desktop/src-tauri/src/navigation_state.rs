use std::fs::{self, File, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};
use tauri::{State, WebviewWindow};

#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};

const NAVIGATION_STATE_SCHEMA: &str = "vibermate-navigation-state-v1";
const NAVIGATION_STATE_FILE: &str = "navigation-state-v1.json";
const MAXIMUM_LOCATOR_BYTES: usize = 2_048;
const MAXIMUM_STATE_BYTES: usize = 4_096;
const MAXIMUM_ENTITY_ID_BYTES: usize = 512;

const STATIC_LOCATORS: &[&str] = &[
    "captures",
    "captures/requests",
    "environments",
    "accounts",
    "extensions",
    "policies/approvals",
    "activity/requests",
    "quality",
    "settings",
];

#[derive(Clone, Debug, Deserialize, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct NavigationState {
    schema: String,
    locator: String,
}

impl NavigationState {
    fn validate(&self) -> bool {
        self.schema == NAVIGATION_STATE_SCHEMA && valid_navigation_locator(&self.locator)
    }
}

pub(crate) struct NavigationStateStore {
    directory: PathBuf,
    sequence: AtomicU64,
    operation: Mutex<StoreOperation>,
}

#[derive(Default)]
struct StoreOperation {
    closing: bool,
}

impl NavigationStateStore {
    pub(crate) fn new(app_data_directory: PathBuf) -> Self {
        Self {
            directory: app_data_directory.join("ui-state"),
            sequence: AtomicU64::new(1),
            operation: Mutex::new(StoreOperation::default()),
        }
    }

    fn load(&self) -> Result<Option<NavigationState>, &'static str> {
        let _operation = self
            .operation
            .lock()
            .map_err(|_| "Navigation state store is unavailable")?;
        if !private_directory(&self.directory, false)? {
            return Ok(None);
        }
        let path = self.directory.join(NAVIGATION_STATE_FILE);
        let metadata = match fs::symlink_metadata(&path) {
            Ok(metadata) => metadata,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(_) => return Err("Navigation state could not be inspected"),
        };
        if !private_regular_file(&metadata) || metadata.len() > MAXIMUM_STATE_BYTES as u64 {
            return Ok(None);
        }
        let mut options = OpenOptions::new();
        options.read(true);
        #[cfg(unix)]
        options.custom_flags(libc::O_NOFOLLOW);
        let file = match options.open(&path) {
            Ok(file) => file,
            Err(_) => return Ok(None),
        };
        let mut encoded = Vec::new();
        file.take(MAXIMUM_STATE_BYTES as u64 + 1)
            .read_to_end(&mut encoded)
            .map_err(|_| "Navigation state could not be read")?;
        if encoded.len() > MAXIMUM_STATE_BYTES {
            return Ok(None);
        }
        let navigation: NavigationState = match serde_json::from_slice(&encoded) {
            Ok(navigation) => navigation,
            Err(_) => return Ok(None),
        };
        Ok(navigation.validate().then_some(navigation))
    }

    fn save(&self, navigation: &NavigationState) -> Result<(), &'static str> {
        let encoded = encode_navigation(navigation)?;
        let operation = self
            .operation
            .lock()
            .map_err(|_| "Navigation state store is unavailable")?;
        if operation.closing {
            return Err("Navigation state store is closing");
        }
        self.commit(&encoded)
    }

    /// Closes the Webview write boundary and, when the current main-window
    /// fragment is a safe canonical locator, commits it as the final state.
    /// Taking the same operation lock as `save` makes this a fence: an older
    /// command either finishes before this write, or observes `closing` and is
    /// refused afterwards.
    pub(crate) fn close_with_fragment(&self, fragment: Option<&str>) -> Result<bool, &'static str> {
        let mut operation = self
            .operation
            .lock()
            .map_err(|_| "Navigation state store is unavailable")?;
        if operation.closing {
            return Ok(false);
        }
        operation.closing = true;
        let Some(locator) = fragment.filter(|locator| valid_navigation_locator(locator)) else {
            return Ok(false);
        };
        let navigation = NavigationState {
            schema: NAVIGATION_STATE_SCHEMA.to_owned(),
            locator: locator.to_owned(),
        };
        let encoded = encode_navigation(&navigation)?;
        self.commit(&encoded)?;
        Ok(true)
    }

    fn commit(&self, encoded: &[u8]) -> Result<(), &'static str> {
        if !private_directory(&self.directory, true)? {
            return Err("Navigation state directory is unavailable");
        }
        let destination = self.directory.join(NAVIGATION_STATE_FILE);
        inspect_existing_destination(&destination)?;
        let sequence = self.sequence.fetch_add(1, Ordering::Relaxed);
        let temporary = self.directory.join(format!(
            ".navigation-state-v1.{}.{}.tmp",
            std::process::id(),
            sequence,
        ));
        let result = self.write_atomic(&temporary, &destination, encoded);
        if result.is_err() {
            let _ = fs::remove_file(&temporary);
        }
        result
    }

    fn write_atomic(
        &self,
        temporary: &Path,
        destination: &Path,
        encoded: &[u8],
    ) -> Result<(), &'static str> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        options.mode(0o600).custom_flags(libc::O_NOFOLLOW);
        let mut file = options
            .open(temporary)
            .map_err(|_| "Navigation state temporary file could not be created")?;
        file.write_all(encoded)
            .map_err(|_| "Navigation state could not be written")?;
        file.sync_all()
            .map_err(|_| "Navigation state could not be synchronized")?;
        drop(file);
        fs::rename(temporary, destination)
            .map_err(|_| "Navigation state could not be committed")?;
        #[cfg(unix)]
        File::open(&self.directory)
            .and_then(|directory| directory.sync_all())
            .map_err(|_| "Navigation state directory could not be synchronized")?;
        Ok(())
    }
}

fn encode_navigation(navigation: &NavigationState) -> Result<Vec<u8>, &'static str> {
    if !navigation.validate() {
        return Err("Navigation state was invalid");
    }
    let mut encoded =
        serde_json::to_vec(navigation).map_err(|_| "Navigation state could not be encoded")?;
    encoded.push(b'\n');
    if encoded.len() > MAXIMUM_STATE_BYTES {
        return Err("Navigation state exceeded its size limit");
    }
    Ok(encoded)
}

fn private_directory(path: &Path, create: bool) -> Result<bool, &'static str> {
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound && !create => return Ok(false),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            #[cfg(unix)]
            {
                let mut builder = fs::DirBuilder::new();
                builder.recursive(true).mode(0o700);
                builder
                    .create(path)
                    .map_err(|_| "Navigation state directory could not be created")?;
            }
            #[cfg(not(unix))]
            fs::create_dir_all(path)
                .map_err(|_| "Navigation state directory could not be created")?;
            fs::symlink_metadata(path)
                .map_err(|_| "Navigation state directory could not be inspected")?
        }
        Err(_) => return Err("Navigation state directory could not be inspected"),
    };
    if !metadata.file_type().is_dir() || !private_metadata(&metadata, 0o700) {
        return Err("Navigation state directory was not private");
    }
    Ok(true)
}

fn private_regular_file(metadata: &fs::Metadata) -> bool {
    metadata.file_type().is_file() && private_metadata(metadata, 0o600)
}

#[cfg(unix)]
fn private_metadata(metadata: &fs::Metadata, expected_mode: u32) -> bool {
    metadata.uid() == unsafe { libc::geteuid() }
        && metadata.permissions().mode() & 0o777 == expected_mode
}

#[cfg(not(unix))]
fn private_metadata(_metadata: &fs::Metadata, _expected_mode: u32) -> bool {
    // The current production target is macOS. A future Windows release must
    // add owner/DACL and reparse-point checks before enabling this store.
    false
}

fn inspect_existing_destination(path: &Path) -> Result<(), &'static str> {
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(_) => return Err("Navigation state could not be inspected"),
    };
    if !private_regular_file(&metadata) || metadata.len() > MAXIMUM_STATE_BYTES as u64 {
        return Err("Navigation state destination was unsafe");
    }

    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    options.custom_flags(libc::O_NOFOLLOW);
    let file = options
        .open(path)
        .map_err(|_| "Navigation state destination could not be read")?;
    let mut encoded = Vec::new();
    file.take(MAXIMUM_STATE_BYTES as u64 + 1)
        .read_to_end(&mut encoded)
        .map_err(|_| "Navigation state destination could not be read")?;
    if encoded.len() > MAXIMUM_STATE_BYTES {
        return Err("Navigation state destination was unsafe");
    }
    let has_future_schema = serde_json::from_slice::<serde_json::Value>(&encoded)
        .map(|value| {
            value
                .get("schema")
                .and_then(serde_json::Value::as_str)
                .is_some_and(|schema| schema != NAVIGATION_STATE_SCHEMA)
        })
        .unwrap_or(false);
    if has_future_schema {
        return Err("Navigation state schema is newer than this application");
    }
    Ok(())
}

pub(crate) fn valid_navigation_locator(locator: &str) -> bool {
    if locator.is_empty()
        || locator.starts_with('/')
        || locator.trim() != locator
        || locator.contains(['#', '\\'])
        || locator.chars().any(char::is_control)
        || locator.len() > MAXIMUM_LOCATOR_BYTES
    {
        return false;
    }
    let (path, search) = match locator.split_once('?') {
        Some((path, search)) => (path, Some(search)),
        None => (locator, None),
    };
    if !valid_navigation_path(path) {
        return false;
    }
    match search {
        None => true,
        Some(search) => {
            path == "policies/approvals"
                && !search.contains('&')
                && search
                    .strip_prefix("selected=")
                    .and_then(|value| decode_safe_value(value, MAXIMUM_ENTITY_ID_BYTES))
                    .is_some()
        }
    }
}

fn valid_navigation_path(path: &str) -> bool {
    if STATIC_LOCATORS.contains(&path) {
        return true;
    }
    let segments: Vec<&str> = path.split('/').collect();
    match segments.as_slice() {
        ["captures", capture_key]
        | ["activity", "requests", capture_key]
        | ["environments", capture_key] => {
            decode_safe_value(capture_key, MAXIMUM_ENTITY_ID_BYTES).is_some()
        }
        ["environments", environment_id, "revisions", revision] => {
            decode_safe_value(environment_id, MAXIMUM_ENTITY_ID_BYTES).is_some()
                && valid_positive_revision(revision)
        }
        _ => false,
    }
}

fn valid_positive_revision(value: &str) -> bool {
    value
        .as_bytes()
        .first()
        .is_some_and(|first| *first >= b'1' && *first <= b'9')
        && value.as_bytes().iter().all(u8::is_ascii_digit)
}

fn decode_safe_value(raw: &str, maximum_bytes: usize) -> Option<String> {
    let bytes = percent_decode(raw)?;
    if bytes.is_empty() || bytes.len() > maximum_bytes {
        return None;
    }
    let decoded = String::from_utf8(bytes).ok()?;
    if decoded.trim() != decoded
        || decoded.to_ascii_lowercase().starts_with("secret:")
        || decoded.contains(['/', '\\'])
        || decoded.chars().any(char::is_control)
    {
        return None;
    }
    Some(decoded)
}

fn percent_decode(raw: &str) -> Option<Vec<u8>> {
    let raw = raw.as_bytes();
    let mut decoded = Vec::with_capacity(raw.len());
    let mut index = 0;
    while index < raw.len() {
        if raw[index] == b'%' {
            let high = hexadecimal(*raw.get(index + 1)?)?;
            let low = hexadecimal(*raw.get(index + 2)?)?;
            decoded.push((high << 4) | low);
            index += 3;
        } else {
            decoded.push(raw[index]);
            index += 1;
        }
    }
    Some(decoded)
}

fn hexadecimal(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        b'A'..=b'F' => Some(value - b'A' + 10),
        _ => None,
    }
}

fn require_main_window(window: &WebviewWindow) -> Result<(), String> {
    if window.label() == "main" {
        Ok(())
    } else {
        Err("Navigation state is not available to this Webview".into())
    }
}

#[tauri::command]
pub(crate) async fn load_navigation_state(
    window: WebviewWindow,
    store: State<'_, Arc<NavigationStateStore>>,
) -> Result<Option<NavigationState>, String> {
    require_main_window(&window)?;
    let store = Arc::clone(store.inner());
    tauri::async_runtime::spawn_blocking(move || store.load())
        .await
        .map_err(|_| "Navigation state task failed".to_owned())?
        .map_err(str::to_owned)
}

#[tauri::command]
pub(crate) async fn save_navigation_state(
    window: WebviewWindow,
    store: State<'_, Arc<NavigationStateStore>>,
    navigation_state: NavigationState,
) -> Result<(), String> {
    require_main_window(&window)?;
    let store = Arc::clone(store.inner());
    tauri::async_runtime::spawn_blocking(move || store.save(&navigation_state))
        .await
        .map_err(|_| "Navigation state task failed".to_owned())?
        .map_err(str::to_owned)
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicU64, Ordering};

    use super::*;

    static TEST_SEQUENCE: AtomicU64 = AtomicU64::new(1);

    struct TestDirectory(PathBuf);

    impl TestDirectory {
        fn new() -> Self {
            let path = std::env::temp_dir().join(format!(
                "vibermate-navigation-state-{}-{}",
                std::process::id(),
                TEST_SEQUENCE.fetch_add(1, Ordering::Relaxed),
            ));
            Self(path)
        }

        fn store(&self) -> NavigationStateStore {
            NavigationStateStore::new(self.0.clone())
        }
    }

    impl Drop for TestDirectory {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    fn navigation(locator: &str) -> NavigationState {
        NavigationState {
            schema: NAVIGATION_STATE_SCHEMA.into(),
            locator: locator.into(),
        }
    }

    #[test]
    fn canonical_icm_and_top_level_locators_are_closed() {
        for locator in [
            "captures",
            "captures/manual%3Aclaude",
            "captures/requests",
            "activity/requests/ex204",
            "environments",
            "environments/work",
            "environments/work/revisions/3",
            "accounts",
            "extensions",
            "policies/approvals",
            "policies/approvals?selected=approval-network-sample",
            "quality",
            "settings",
        ] {
            assert!(valid_navigation_locator(locator), "{locator}");
        }
        for locator in [
            "",
            "/captures",
            "policy",
            "access/claude/routing",
            "captures/%2F",
            "captures/%E0%A4%A",
            "environments/work/revisions/0",
            "environments/work/revisions/latest",
            "extensions?selected=not-allowed",
            "policies/approvals?selected=one&selected=two",
            "policies/approvals?selected=secret%3A%2F%2Fprovider%2Fwork",
            "captures#nested",
        ] {
            assert!(!valid_navigation_locator(locator), "{locator}");
        }
    }

    #[test]
    fn private_atomic_store_round_trips_one_main_window_locator() {
        let directory = TestDirectory::new();
        let store = directory.store();
        assert_eq!(store.load().expect("load missing state"), None);

        let expected = navigation("activity/requests/ex204");
        store.save(&expected).expect("save navigation state");
        assert_eq!(store.load().expect("load navigation state"), Some(expected));
        let state_path = store.directory.join(NAVIGATION_STATE_FILE);
        let encoded = fs::read_to_string(state_path).expect("read stored navigation state");
        assert!(!encoded.contains("token"));
        assert!(!encoded.contains("secret://"));
        assert!(
            fs::read_dir(&store.directory)
                .expect("read state directory")
                .all(|entry| !entry
                    .expect("read directory entry")
                    .file_name()
                    .to_string_lossy()
                    .ends_with(".tmp"))
        );
        #[cfg(unix)]
        {
            assert_eq!(
                fs::metadata(&store.directory)
                    .expect("read directory metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                0o700,
            );
            assert_eq!(
                fs::metadata(store.directory.join(NAVIGATION_STATE_FILE))
                    .expect("read file metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                0o600,
            );
        }
    }

    #[test]
    fn corrupt_state_can_be_repaired_but_future_state_is_preserved() {
        let directory = TestDirectory::new();
        let store = directory.store();
        store
            .save(&navigation("captures"))
            .expect("save initial state");
        let state_path = store.directory.join(NAVIGATION_STATE_FILE);
        let future_state =
            br#"{"schema":"future-navigation-v2","locator":"settings","extra":true}"#;
        fs::write(&state_path, future_state).expect("write future state");
        #[cfg(unix)]
        fs::set_permissions(&state_path, fs::Permissions::from_mode(0o600))
            .expect("restore private test mode");

        assert_eq!(store.load().expect("ignore future state"), None);
        assert!(store.save(&navigation("settings")).is_err());
        assert_eq!(
            fs::read(&state_path).expect("read preserved future state"),
            future_state,
        );

        fs::write(
            &state_path,
            br#"{"schema":"vibermate-navigation-state-v1","locator":"settings"#,
        )
        .expect("write corrupt current state");
        #[cfg(unix)]
        fs::set_permissions(&state_path, fs::Permissions::from_mode(0o600))
            .expect("restore private test mode");
        assert_eq!(store.load().expect("ignore corrupt current state"), None);
        store
            .save(&navigation("settings"))
            .expect("repair corrupt current state");
        assert_eq!(
            store.load().expect("load repaired state"),
            Some(navigation("settings")),
        );
    }

    #[test]
    fn close_flushes_the_last_safe_fragment_and_fences_late_webview_writes() {
        let directory = TestDirectory::new();
        let store = directory.store();
        store
            .save(&navigation("captures"))
            .expect("save state before close");

        assert_eq!(store.close_with_fragment(Some("settings")), Ok(true));
        assert_eq!(
            store.load().expect("load close-flushed state"),
            Some(navigation("settings")),
        );
        assert!(store.save(&navigation("activity/requests/ex204")).is_err());
        assert_eq!(
            store.load().expect("load state after refused late write"),
            Some(navigation("settings")),
        );
        assert_eq!(store.close_with_fragment(Some("captures")), Ok(false));
    }

    #[test]
    fn unsafe_close_fragment_is_refused_without_replacing_safe_state() {
        for unsafe_fragment in [
            "not-a-real-route",
            "captures?body=prompt-text",
            "policies/approvals?selected=secret%3A%2F%2Fprovider%2Fwork",
            "activity/requests/ex204?session=capability",
        ] {
            let directory = TestDirectory::new();
            let store = directory.store();
            store
                .save(&navigation("captures"))
                .expect("save safe state before close");

            assert_eq!(store.close_with_fragment(Some(unsafe_fragment)), Ok(false));
            assert!(store.save(&navigation("environments")).is_err());
            assert_eq!(
                store.load().expect("load preserved safe state"),
                Some(navigation("captures")),
                "{unsafe_fragment}",
            );
        }
    }

    #[cfg(unix)]
    #[test]
    fn symlink_or_non_private_state_is_never_followed() {
        use std::os::unix::fs::symlink;

        let directory = TestDirectory::new();
        let store = directory.store();
        store
            .save(&navigation("captures"))
            .expect("create private store");
        let state_path = store.directory.join(NAVIGATION_STATE_FILE);
        let outside = directory.0.join("outside.json");
        fs::write(&outside, "do not replace").expect("write outside sentinel");
        fs::remove_file(&state_path).expect("remove test state");
        symlink(&outside, &state_path).expect("install test symlink");

        assert_eq!(store.load().expect("ignore symlink"), None);
        assert!(store.save(&navigation("environments")).is_err());
        assert_eq!(
            fs::read_to_string(&outside).expect("read outside sentinel"),
            "do not replace",
        );
        fs::remove_file(&state_path).expect("remove test symlink");
        store
            .save(&navigation("environments"))
            .expect("save after removing symlink");
        assert_eq!(
            store.load().expect("load safe state"),
            Some(navigation("environments")),
        );

        fs::set_permissions(&state_path, fs::Permissions::from_mode(0o644))
            .expect("make test state non-private");
        assert_eq!(store.load().expect("ignore non-private state"), None);
        assert!(store.save(&navigation("captures")).is_err());
    }
}

fn main() {
    let manifest = tauri_build::AppManifest::new().commands(&[
        "take_control_session",
        "inspect_terminal_command",
        "install_terminal_command",
        "refresh_terminal_command",
        "remove_terminal_command",
        "load_navigation_state",
        "save_navigation_state",
    ]);
    let attributes = tauri_build::Attributes::new().app_manifest(manifest);
    tauri_build::try_build(attributes)
        .expect("could not build the Desktop ACL and application metadata");
}

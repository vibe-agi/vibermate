fn main() {
    let manifest = tauri_build::AppManifest::new().commands(&["take_control_session"]);
    let attributes = tauri_build::Attributes::new().app_manifest(manifest);
    tauri_build::try_build(attributes)
        .expect("could not build the Desktop ACL and application metadata");
}

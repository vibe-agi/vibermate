/// Builds the complete environment inherited by the Desktop sidecar.
///
/// Packaged acceptance uses `CFFIXED_USER_HOME` to isolate Foundation-backed
/// App data. Security.framework also observes that override, so forwarding it
/// would detach the sidecar from the logged-in user's Keychain. The sidecar
/// already receives explicit cache and data paths and must use the real login
/// environment for host-protected credentials.
Map<String, String> desktopDaemonEnvironment(Map<String, String> parent) {
  return Map<String, String>.of(parent)..remove('CFFIXED_USER_HOME');
}

import '../../core/api/control_models.dart';

/// The AI services each agent ViberMate knows sends its model requests to.
/// Null for an agent it does not know, which could use any of them.
Set<String>? agentServiceHosts(CaptureRecord capture) {
  final label = capture.managedRun?.executableLabel.toLowerCase() ?? '';
  final adapter = capture.managedRun?.clientAdapter?.id ?? '';
  if (adapter == 'codex-cli' || label.contains('codex')) {
    return const {'api.openai.com', 'chatgpt.com'};
  }
  if (adapter == 'claude-code' || label.contains('claude')) {
    return const {'api.anthropic.com'};
  }
  return null;
}

/// The client adapter id of a run's agent, also when ViberMate could not
/// check the program and so recorded no adapter.
String? agentClientId(CaptureRecord capture) {
  final adapter = capture.managedRun?.clientAdapter?.id;
  if (adapter != null) return adapter;
  final label = capture.managedRun?.executableLabel.toLowerCase() ?? '';
  if (label.contains('codex')) return 'codex-cli';
  if (label.contains('claude')) return 'claude-code';
  return null;
}

/// Who publishes a run's agent, for "signed by" wording.
String? agentPublisher(CaptureRecord capture) =>
    switch (agentClientId(capture)) {
      'claude-code' => 'Anthropic',
      'codex-cli' => 'OpenAI',
      _ => null,
    };

/// Whether a run's agent cannot connect through ViberMate: ViberMate could
/// not check this copy of a known agent, so the launcher gave it no
/// certificate, while its routing set decrypts the agent's service. Every
/// request it makes to that service then fails its TLS handshake.
///
/// This mirrors the issuer, which delivers the certificate only to checked
/// builds and only when the routing set protects some service.
bool agentCannotConnect(CaptureRecord capture, EnvironmentRecord? environment) {
  final managed = capture.managedRun;
  // A session that delivered a request evidently connected.
  if (managed == null ||
      managed.recognition != 'unverified' ||
      capture.observation == 'observed' ||
      environment == null) {
    return false;
  }
  final hosts = agentServiceHosts(capture);
  return environment.clientEndpoints.any(
    (endpoint) => hosts == null || hosts.contains(endpoint.clientOrigin.host),
  );
}

/// The routing set a session runs on, from its assignment when loaded and
/// otherwise from the session list.
EnvironmentRecord? captureEnvironment(
  CaptureRecord capture,
  List<EnvironmentRecord> environments, {
  String? assignedId,
}) {
  final id = assignedId ?? capture.environment?.id;
  if (id == null) return null;
  return environments.where((value) => value.id == id).firstOrNull;
}

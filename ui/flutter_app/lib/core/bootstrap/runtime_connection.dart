import '../api/control_api.dart';
import 'terminal_command_contract.dart';

final class RuntimeLoginRequired implements Exception {
  const RuntimeLoginRequired(this.reason);

  final String reason;

  @override
  String toString() => reason;
}

final class RuntimeConnectionException implements Exception {
  const RuntimeConnectionException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// The exact origin from which the browser-loaded Runtime Server workbench
/// may call its same-origin management API.
///
/// HTTP is deliberate, not a fallback: private and development networks may
/// run without a certificate. The caller must surface [encrypted] so an HTTP
/// operator never mistakes the transport for TLS.
final class RuntimeServerLocation {
  const RuntimeServerLocation._({
    required this.baseUrl,
    required this.encrypted,
  });

  factory RuntimeServerLocation.fromPageUri(Uri page) {
    if ((page.scheme != 'http' && page.scheme != 'https') ||
        page.host.isEmpty ||
        page.userInfo.isNotEmpty ||
        (page.hasPort && page.port == 0)) {
      throw const RuntimeConnectionException('server_transport_unsupported');
    }
    final baseUrl = Uri(
      scheme: page.scheme,
      host: page.host,
      port: page.hasPort ? page.port : null,
    );
    return RuntimeServerLocation._(
      baseUrl: baseUrl,
      encrypted: page.scheme == 'https',
    );
  }

  final Uri baseUrl;
  final bool encrypted;

  String get displayLabel => baseUrl.toString();
}

final class RuntimeConnection {
  const RuntimeConnection({
    required this.api,
    required this.terminalCommands,
    required this.close,
    required this.isClosed,
    required this.serverManagement,
    required this.terminalManagement,
    required this.targetLabel,
    this.exitCode,
  });

  final ControlApi api;
  final TerminalCommandService terminalCommands;
  final Future<void> Function() close;
  final bool Function() isClosed;
  final bool serverManagement;
  final bool terminalManagement;
  final String targetLabel;
  final Future<int>? exitCode;
}

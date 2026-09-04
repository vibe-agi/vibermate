import '../api/control_api.dart';
import 'root_trust_installer_contract.dart';
import 'terminal_command_contract.dart';

final class RuntimeLoginRequired implements Exception {
  const RuntimeLoginRequired(this.reason);

  final String reason;

  @override
  String toString() => reason;
}

enum RuntimeLoginMode { signIn, setup, recover }

final class RuntimeLoginAttempt {
  const RuntimeLoginAttempt.signIn({
    required this.username,
    required this.password,
  }) : mode = RuntimeLoginMode.signIn,
       recoveryKey = '';

  const RuntimeLoginAttempt.setup({
    required this.recoveryKey,
    required this.username,
    required this.password,
  }) : mode = RuntimeLoginMode.setup;

  const RuntimeLoginAttempt.recover({
    required this.recoveryKey,
    required this.password,
  }) : mode = RuntimeLoginMode.recover,
       username = '';

  final RuntimeLoginMode mode;
  final String recoveryKey;
  final String username;
  final String password;
}

enum RuntimeWebRole { owner, member }

final class RuntimeWebPrincipal {
  const RuntimeWebPrincipal({
    required this.id,
    required this.username,
    required this.role,
  });

  final String id;
  final String username;
  final RuntimeWebRole role;

  bool get owner => role == RuntimeWebRole.owner;
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
    required this.rootTrustManagement,
    required this.targetLabel,
    this.rootTrustInstaller,
    this.exitCode,
    this.webSessionEnded,
    this.webPrincipal,
    this.changePassword,
    this.signOut,
  });

  final ControlApi api;
  final TerminalCommandService terminalCommands;
  final Future<void> Function() close;
  final bool Function() isClosed;
  final bool serverManagement;
  final bool terminalManagement;
  final bool rootTrustManagement;
  final String targetLabel;
  final RootTrustInstaller? rootTrustInstaller;
  final Future<int>? exitCode;
  final Future<void>? webSessionEnded;
  final RuntimeWebPrincipal? webPrincipal;
  final Future<void> Function(String currentPassword, String newPassword)?
  changePassword;
  final Future<void> Function()? signOut;
}

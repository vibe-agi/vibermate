import 'dart:io' show Platform;

import 'desktop_runtime.dart';
import 'runtime_connection.dart';
import 'root_trust_installer.dart';
import 'terminal_command.dart';

const bool platformRuntimeRequiresLogin = false;

String platformRuntimeTargetLabel() => 'This Mac';

bool platformRuntimeUsesPlaintext() => false;

Future<RuntimeConnection> connectPlatformRuntime({
  String? accessKey,
  String? daemonPath,
}) async {
  final DesktopRuntime runtime;
  try {
    runtime = await DesktopRuntime.start(daemonPath: daemonPath);
  } on DesktopRuntimeException catch (error) {
    if (error.message == 'Packaged Desktop sidecar is unavailable') {
      throw const RuntimeConnectionException('desktop_sidecar_unavailable');
    }
    throw RuntimeConnectionException(
      error.reason ?? 'desktop_runtime_unavailable',
    );
  }
  return RuntimeConnection(
    api: runtime.api,
    terminalCommands: PackagedTerminalCommandService(),
    close: runtime.close,
    isClosed: () => runtime.isClosed,
    serverManagement: true,
    terminalManagement: true,
    rootTrustManagement: Platform.isMacOS,
    rootTrustInstaller: const PlatformRootTrustInstaller(),
    // The authoritative connectable IP arrives from /api/v1/server/access.
    // Until then, show a neutral local label instead of advertising a .local
    // hostname that may not resolve from another machine.
    targetLabel: platformRuntimeTargetLabel(),
    exitCode: runtime.exitCode,
  );
}

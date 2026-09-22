import 'dart:io' show Platform, Directory;
import 'package:file_selector/file_selector.dart';

import 'desktop_runtime.dart';
import 'desktop_storage.dart';
import 'runtime_connection.dart';
import 'root_trust_installer.dart';
import 'terminal_command.dart';

const bool platformRuntimeRequiresLogin = false;

String platformRuntimeTargetLabel() => 'This Mac';

bool platformRuntimeUsesPlaintext() => false;

Future<RuntimeConnection> connectPlatformRuntime({
  RuntimeLoginAttempt? login,
  String? daemonPath,
}) async {
  final DesktopRuntime runtime;
  try {
    runtime = await DesktopRuntime.start(daemonPath: daemonPath);
  } on DesktopStorageFailure catch (error) {
    throw RuntimeConnectionException(error.code);
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
    chooseStorageDirectory: () async {
      final selected = await getDirectoryPath();
      if (selected == null) return null;
      final parent = await Directory(selected).resolveSymbolicLinks();
      return '$parent/ViberMate';
    },
    prepareStorageMove: runtime.prepareStorageMove,
    moveStorage: runtime.moveStorage,
    storageNotice: runtime.storageNotice,
  );
}

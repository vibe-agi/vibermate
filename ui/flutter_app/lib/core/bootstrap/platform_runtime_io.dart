import 'dart:io';

import 'desktop_runtime.dart';
import 'runtime_connection.dart';
import 'terminal_command.dart';

const bool platformRuntimeRequiresLogin = false;

String platformRuntimeTargetLabel() => 'This Mac';

bool platformRuntimeUsesPlaintext() => false;

Future<RuntimeConnection> connectPlatformRuntime({String? accessKey}) async {
  final runtime = await DesktopRuntime.start();
  return RuntimeConnection(
    api: runtime.api,
    terminalCommands: PackagedTerminalCommandService(),
    close: runtime.close,
    isClosed: () => runtime.isClosed,
    serverManagement: true,
    terminalManagement: true,
    targetLabel: '${Platform.localHostname}:9666',
    exitCode: runtime.exitCode,
  );
}

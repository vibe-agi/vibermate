import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('revoked or expired Web Session returns a member to sign in', (
    tester,
  ) async {
    final sessionEnded = Completer<void>();
    final api = PreviewControlApi();
    var closed = false;
    var connections = 0;

    await tester.pumpWidget(
      ViberMateApp(
        previewMode: false,
        preferChinese: false,
        preferencesStore: MemoryWorkbenchPreferencesStore(
          encoded: const WorkbenchPreferences().encode(),
        ),
        runtimeConnector: ({RuntimeLoginAttempt? login}) async {
          connections += 1;
          return RuntimeConnection(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            close: () async {
              if (closed) return;
              closed = true;
              await api.close();
            },
            isClosed: () => closed,
            serverManagement: false,
            terminalManagement: false,
            rootTrustManagement: false,
            targetLabel: 'https://runtime.example.test',
            webSessionEnded: sessionEnded.future,
            webPrincipal: const RuntimeWebPrincipal(
              id: 'user.member',
              username: 'alice',
              role: RuntimeWebRole.member,
            ),
            changePassword: (_, _) async {},
            signOut: () async {},
          );
        },
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('My activity'), findsWidgets);

    sessionEnded.complete();
    await tester.pumpAndSettle();

    expect(find.text('Connect to this Runtime Server'), findsOneWidget);
    expect(find.text('Your session ended. Sign in again.'), findsOneWidget);
    expect(connections, 1);
    expect(closed, isTrue);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

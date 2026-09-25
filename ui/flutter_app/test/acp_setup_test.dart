import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/acp_setup.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets('ACP editor config preserves argument boundaries: $language', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(390, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copy = AppCopy.forLanguage(language);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: SingleChildScrollView(
              child: ACPSetupGuide(
                copy: copy,
                program: '/Applications/My App/vibermate',
                serverURL: '',
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      Map<String, dynamic> document() =>
          jsonDecode(
                tester
                    .widget<SelectableText>(
                      find.byKey(const Key('acp-editor-json')),
                    )
                    .data!,
              )
              as Map<String, dynamic>;
      Map<String, dynamic> config(String key) =>
          document()[key] as Map<String, dynamic>;
      expect(config('agent_servers')['vibermate-codex'], {
        'type': 'custom',
        'command': '/Applications/My App/vibermate',
        'args': ['acp', '--', 'codex-acp'],
        'env': <String, String>{},
      });
      await tester.enterText(
        find.byKey(const Key('acp-agent')),
        '/agents/My Agent/codex-acp',
      );
      await tester.enterText(
        find.byKey(const Key('acp-server')),
        'https://runtime.example:9666',
      );
      await tester.ensureVisible(find.byType(SwitchListTile));
      await tester.tap(find.byType(Switch));
      await tester.pumpAndSettle();
      expect(config('agent_servers')['vibermate-codex']['args'], [
        'acp',
        '--server',
        'https://runtime.example:9666',
        '--record-content',
        '--',
        '/agents/My Agent/codex-acp',
      ]);
      await tester.ensureVisible(find.text('Zed'));
      await tester.tap(find.text('Zed'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('JetBrains').last);
      await tester.pumpAndSettle();
      expect(
        config('agent_servers')['vibermate-codex'].containsKey('type'),
        isFalse,
      );
      await tester.tap(find.text('JetBrains'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('VS Code (ACP Client)').last);
      await tester.pumpAndSettle();
      expect(document().containsKey('agent_servers'), isFalse);
      expect(config('acp.agents')['vibermate-codex'], {
        'command': '/Applications/My App/vibermate',
        'args': [
          'acp',
          '--server',
          'https://runtime.example:9666',
          '--record-content',
          '--',
          '/agents/My Agent/codex-acp',
        ],
        'env': <String, String>{},
      });
      await tester.tap(find.text('Codex'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Cursor CLI').last);
      await tester.pumpAndSettle();
      expect(config('acp.agents')['vibermate-cursor-cli']['args'], [
        'acp',
        '--server',
        'https://runtime.example:9666',
        '--record-content',
        '--',
        'agent',
        'acp',
      ]);
      expect(find.text(copy('acp.boundary')), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }
}

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/acp_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/acp_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final width in [390.0, 1100.0]) {
      testWidgets(
        'ACP sessions and metadata-only are clear: $language $width',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 950));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final copy = AppCopy.forLanguage(language);
          const record = ACPRecord(
            runId: 'run-test',
            mode: 'metadata_only',
            expired: false,
            agentName: 'Codex ACP',
            agentVersion: '1.10.0',
            protocolVersion: 1,
            sessions: [
              ACPSession(id: 'one', cwd: '/workspace/one', operation: 'new'),
              ACPSession(id: 'two', cwd: '/workspace/two', operation: 'load'),
            ],
            prompts: [
              ACPPrompt(
                sequence: 1,
                sessionId: 'one',
                state: 'completed',
                stopReason: 'end_turn',
                userText: '',
                agentText: '',
                toolCalls: 0,
              ),
            ],
            incomplete: false,
            finished: true,
            exitCode: 0,
          );
          await tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.dark(),
              home: Scaffold(
                body: ACPObservationView(
                  record: record,
                  copy: copy,
                  running: false,
                ),
              ),
            ),
          );
          await tester.pumpAndSettle();
          expect(find.text(copy('acp.boundary')), findsOneWidget);
          expect(find.text(copy('acp.metadata_only')), findsOneWidget);
          expect(find.textContaining('1.10.0'), findsOneWidget);
          expect(find.textContaining('end_turn'), findsOneWidget);
          await tester.tap(find.byKey(const Key('acp-session-selector')));
          await tester.pumpAndSettle();
          await tester.tap(find.text('two').last);
          await tester.pumpAndSettle();
          expect(find.text('/workspace/two'), findsOneWidget);
          expect(find.text(copy('acp.no_prompts')), findsOneWidget);
          expect(tester.takeException(), isNull);
        },
      );
    }
  }
}

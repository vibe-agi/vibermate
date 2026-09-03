import 'package:flutter/material.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/bootstrap/platform_runtime.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';

void main() {
  test(
    'missing desktop sidecar maps to a stable repair-required error',
    () async {
      await expectLater(
        connectPlatformRuntime(daemonPath: '/missing/vibermated'),
        throwsA(
          isA<RuntimeConnectionException>().having(
            (error) => error.message,
            'message',
            'desktop_sidecar_unavailable',
          ),
        ),
      );
    },
    skip: kIsWeb,
  );

  testWidgets('Preview workbench starts with real capture hierarchy', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.text('RUNNING NOW'), findsOneWidget);
    expect(find.text('HISTORY'), findsOneWidget);
    expect(find.text('Claude Code'), findsWidgets);

    await tester.tap(find.text('Claude Code').first);
    await tester.pumpAndSettle();
    expect(find.text('Capture conversation'), findsOneWidget);
    expect(
      find.byKey(const Key('capture-conversation-selector')),
      findsOneWidget,
    );

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    expect(find.text('Traffic policies'), findsWidgets);
    expect(find.text('Work'), findsWidgets);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'missing packaged sidecar gives repair guidance instead of retry',
    (tester) async {
      const rawPath = '/Applications/ViberMate.app/Contents/MacOS/vibermated';
      for (final scenario in const [
        (
          chinese: false,
          message: 'ViberMate is incomplete. Reinstall or rebuild the App.',
          retry: 'Retry',
        ),
        (
          chinese: true,
          message: 'ViberMate 安装不完整。请重新安装或重新构建 App。',
          retry: '重试',
        ),
      ]) {
        await tester.pumpWidget(
          ViberMateApp(
            previewMode: false,
            preferChinese: scenario.chinese,
            preferencesStore: MemoryWorkbenchPreferencesStore(),
            runtimeConnector: ({String? accessKey}) async {
              throw const RuntimeConnectionException(
                'desktop_sidecar_unavailable',
              );
            },
          ),
        );
        await tester.pumpAndSettle();

        expect(find.text(scenario.message), findsOneWidget);
        expect(find.textContaining(rawPath), findsNothing);
        expect(find.text(scenario.retry), findsNothing);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      }
    },
  );

  testWidgets('Root reset startup failure has bilingual recovery guidance', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    for (final scenario in const [
      (chinese: false, text: 'Root replacement could not recover safely.'),
      (chinese: true, text: '根证书更换无法安全恢复。'),
    ]) {
      await tester.pumpWidget(
        ViberMateApp(
          previewMode: false,
          preferChinese: scenario.chinese,
          preferencesStore: MemoryWorkbenchPreferencesStore(),
          runtimeConnector: ({String? accessKey}) async {
            throw const RuntimeConnectionException('root_reset_failed');
          },
        ),
      );
      await tester.pumpAndSettle();

      expect(find.textContaining(scenario.text), findsOneWidget);
      expect(find.textContaining('root_reset_failed'), findsNothing);
      expect(find.byIcon(Icons.refresh), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    }
  });
}

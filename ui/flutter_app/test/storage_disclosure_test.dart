import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets(
    'local storage change previews its target and requires confirmation',
    (tester) async {
      final calls = <String>[];
      await tester.pumpWidget(
        ViberMateApp(
          previewMode: false,
          preferChinese: false,
          preferencesStore: const DiscardWorkbenchPreferencesStore(),
          runtimeConnector: ({login}) async {
            final api = PreviewControlApi();
            return RuntimeConnection(
              api: api,
              terminalCommands: PreviewTerminalCommandService(),
              close: api.close,
              isClosed: () => false,
              serverManagement: false,
              terminalManagement: true,
              rootTrustManagement: false,
              targetLabel: 'Test Mac',
              chooseStorageDirectory: () async =>
                  '/Volumes/Local disk/ViberMate',
              prepareStorageMove: (path) async {
                calls.add('prepare:$path');
              },
              moveStorage: (path) async {
                calls.add('move:$path');
              },
            );
          },
        ),
      );
      await tester.pumpAndSettle();
      await _openSafetySettings(tester);
      await tester.pumpAndSettle();
      final change = find.byKey(const Key('storage-change-directory'));
      await tester.scrollUntilVisible(
        change,
        240,
        scrollable: _safetyScrollable(),
      );
      await tester.pumpAndSettle();
      await tester.ensureVisible(change);
      await tester.pumpAndSettle();
      await tester.tap(change);
      await tester.pumpAndSettle();
      expect(find.text('/Volumes/Local disk/ViberMate'), findsOneWidget);
      expect(find.textContaining('original folder is kept'), findsOneWidget);
      await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      await tester.pumpAndSettle();
      expect(calls, isEmpty);
      await tester.tap(change);
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Move and restart'));
      await tester.pumpAndSettle();
      expect(calls, [
        'prepare:/Volumes/Local disk/ViberMate',
        'move:/Volumes/Local disk/ViberMate',
      ]);
      expect(
        find.text(
          'Storage location changed. The original folder is kept as a backup.',
        ),
        findsOneWidget,
      );
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'Web storage paths stay server-owned and cannot open a browser picker',
    (tester) async {
      await tester.pumpWidget(
        ViberMateApp(
          previewMode: false,
          preferChinese: false,
          preferencesStore: const DiscardWorkbenchPreferencesStore(),
          runtimeConnector: ({login}) async {
            final api = PreviewControlApi();
            return RuntimeConnection(
              api: api,
              terminalCommands: PreviewTerminalCommandService(),
              close: api.close,
              isClosed: () => false,
              serverManagement: true,
              terminalManagement: false,
              rootTrustManagement: false,
              targetLabel: 'team.example',
            );
          },
        ),
      );
      await tester.pumpAndSettle();
      await _openSafetySettings(tester);
      final panel = find.byKey(const Key('storage-disclosure-panel'));
      await Scrollable.ensureVisible(tester.element(panel), alignment: 0.4);
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('storage-change-directory')), findsNothing);
      expect(
        find.textContaining('not a folder on your browser'),
        findsOneWidget,
      );
    },
  );

  // INV-STORE-DISCLOSED is a release gate: the recording mode, the location,
  // the retention period and the absence of at-rest database encryption must be
  // visible in Settings. A product that quietly stores plaintext is no more
  // honest than one that claims encryption it does not have.
  testWidgets('Settings discloses that the archive is not encrypted at rest', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await _openSafetySettings(tester);

    final panel = find.byKey(const Key('storage-disclosure-panel'));
    await tester.scrollUntilVisible(
      panel,
      240,
      scrollable: _safetyScrollable(),
    );
    expect(panel, findsOneWidget);
    expect(
      find.text('/Users/mira/Library/Application Support/io.vibermate.desktop'),
      findsOneWidget,
    );
    expect(
      find.text(
        '/Users/mira/Library/Application Support/io.vibermate.desktop/runtime.db',
      ),
      findsOneWidget,
    );
    expect(find.byTooltip('Copy Data directory'), findsOneWidget);
    expect(
      find.textContaining('not encrypted at rest'),
      findsOneWidget,
      reason: 'the disclosure must state the absence plainly',
    );
  });

  // The disclosure used to say no credential value is stored, full stop. That
  // is true only of credential *headers*: bodies, tool arguments and query
  // strings are retained verbatim, which is the point of a forensic archive.
  // A user who pastes an API key into a prompt has it kept, and the panel that
  // claims otherwise is the one they would have read before doing it.
  testWidgets(
    'Storage disclosure names what is retained, not just what is removed',
    (tester) async {
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await _openSafetySettings(tester);

      final panel = find.byKey(const Key('storage-disclosure-panel'));
      await tester.scrollUntilVisible(
        panel,
        240,
        scrollable: _safetyScrollable(),
      );
      expect(
        find.textContaining('stored as sent'),
        findsOneWidget,
        reason: 'the disclosure must state that bodies are retained verbatim',
      );
    },
  );

  testWidgets('Storage disclosure is present in Simplified Chinese', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await _openSafetySettings(tester);

    final panel = find.byKey(const Key('storage-disclosure-panel'));
    await tester.scrollUntilVisible(
      panel,
      240,
      scrollable: _safetyScrollable(),
    );
    expect(panel, findsOneWidget);
    expect(find.textContaining('未加密'), findsOneWidget);
  });

  testWidgets('storage snapshot previews and cleans only expired evidence', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openSafetySettings(tester);

    final capacity = find.byKey(const Key('storage-capacity'));
    await tester.scrollUntilVisible(
      capacity,
      240,
      scrollable: _safetyScrollable(),
    );
    expect(find.text('192.0 MiB'), findsOneWidget);
    expect(find.bySemanticsLabel('Database file: 192.0 MiB'), findsOneWidget);
    expect(find.text('8.0 MiB'), findsOneWidget);
    expect(find.text('128.0 MiB'), findsOneWidget);
    expect(find.text('16.0 MiB'), findsOneWidget);
    expect(find.textContaining('warning below 1.0 GiB'), findsOneWidget);

    final cleanup = find.byKey(const Key('storage-cleanup-expired'));
    await Scrollable.ensureVisible(tester.element(cleanup), alignment: 0.5);
    await tester.pumpAndSettle();
    await tester.tap(cleanup);
    await tester.pumpAndSettle();
    expect(find.text('Clean expired evidence?'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('deletion-confirm-dialog')),
        matching: find.textContaining('3 semantic Exchanges'),
      ),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('deletion-confirm')));
    await tester.pumpAndSettle();
    expect(
      find.text('No expired evidence is waiting for cleanup.'),
      findsOneWidget,
    );
    expect(find.text('Expired evidence was cleaned.'), findsOneWidget);

    final clear = find.byKey(const Key('storage-clear-archive'));
    await Scrollable.ensureVisible(tester.element(clear), alignment: 0.5);
    await tester.pumpAndSettle();
    await tester.tap(clear);
    await tester.pumpAndSettle();
    expect(find.textContaining('20 Captures'), findsOneWidget);
    expect(find.textContaining('3055 Raw HTTP boundaries'), findsOneWidget);
  });

  testWidgets('390px Chinese storage capacity and cleanup stay operable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();
    await _openSafetySettings(tester);

    final cleanup = find.byKey(const Key('storage-cleanup-expired'));
    await Scrollable.ensureVisible(tester.element(cleanup), alignment: 0.5);
    await tester.pumpAndSettle();
    expect(find.textContaining('12 条 Raw HTTP'), findsOneWidget);
    await tester.tap(cleanup);
    await tester.pumpAndSettle();
    expect(find.text('清理过期证据？'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  // The disclosure grew when its claim was narrowed, and a copy change is
  // exactly the kind of edit that overflows a panel without anyone noticing:
  // every assertion above passes on a clipped layout. A widget test fails on a
  // RenderFlex overflow, so rendering the panel at the narrow end of the
  // window range is the assertion.
  for (final size in const [Size(820, 620), Size(700, 560)]) {
    for (final chinese in const [false, true]) {
      testWidgets('Storage disclosure lays out at ${size.width.toInt()}x'
          '${size.height.toInt()} in ${chinese ? 'zh' : 'en'}', (tester) async {
        tester.view.physicalSize = size;
        tester.view.devicePixelRatio = 1.0;
        addTearDown(tester.view.reset);

        await tester.pumpWidget(
          ViberMateApp(previewMode: true, preferChinese: chinese),
        );
        await tester.pumpAndSettle();
        await _openSafetySettings(tester);

        final panel = find.byKey(const Key('storage-disclosure-panel'));
        await tester.scrollUntilVisible(
          panel,
          240,
          scrollable: _safetyScrollable(),
        );
        expect(panel, findsOneWidget);
      });
    }
  }
}

Future<void> _openSafetySettings(WidgetTester tester) async {
  await tester.tap(find.byIcon(Icons.settings_outlined).first);
  await tester.pumpAndSettle();
  final picker = find.byKey(const Key('settings-section-picker'));
  if (picker.evaluate().isNotEmpty) {
    await tester.tap(picker);
    await tester.pumpAndSettle();
    final label = tester
        .widget<SettingsView>(find.byType(SettingsView))
        .copy('settings.tab.safety');
    await tester.tap(find.text(label).hitTestable().last);
    await tester.pumpAndSettle();
    return;
  }
  final tab = find.byKey(const Key('settings-tab-safety'));
  await tester.ensureVisible(tab);
  await tester.pumpAndSettle();
  await tester.tap(tab);
  await tester.pumpAndSettle();
}

Finder _safetyScrollable() => find
    .descendant(
      of: find.byKey(const Key('settings-safety-scroll')),
      matching: find.byType(Scrollable),
    )
    .first;

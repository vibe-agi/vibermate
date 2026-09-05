import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test(
    'controller restores valid selectors and persists the latest view',
    () async {
      const restored = WorkbenchPreferences(
        language: AppLanguage.simplifiedChinese,
        theme: WorkbenchTheme.dark,
        section: WorkbenchSection.environments,
        selectedCaptureKey: 'managed_run:run-3',
        selectedEnvironmentId: 'research',
        selectedEndpointId: 'target.orbit.relay',
      );
      final store = MemoryWorkbenchPreferencesStore(encoded: restored.encode());
      final controller = WorkbenchController(
        api: PreviewControlApi(),
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: () async {},
        initialPreferences: restored,
        preferencesStore: store,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      expect(controller.language, AppLanguage.simplifiedChinese);
      expect(controller.theme, WorkbenchTheme.dark);
      expect(controller.section, WorkbenchSection.environments);
      expect(controller.selectedCaptureKey, 'managed_run:run-3');
      expect(controller.selectedEnvironmentId, 'research');
      expect(controller.selectedEndpointId, 'target.orbit.relay');

      controller.selectSection(WorkbenchSection.network);
      controller.setLanguage(AppLanguage.english);
      controller.setTheme(WorkbenchTheme.light);
      await controller.flushPreferences();
      final persisted = WorkbenchPreferences.decode(store.encoded!);
      expect(persisted.section, WorkbenchSection.network);
      expect(persisted.language, AppLanguage.english);
      expect(persisted.theme, WorkbenchTheme.light);
      expect(persisted.selectedCaptureKey, 'managed_run:run-3');
    },
  );

  test(
    'stale resource selectors reconcile to current runtime authority',
    () async {
      const stale = WorkbenchPreferences(
        selectedCaptureKey: 'managed_run:missing',
        selectedEnvironmentId: 'environment.missing',
        selectedEndpointId: 'target.missing',
      );
      final store = MemoryWorkbenchPreferencesStore(encoded: stale.encode());
      final controller = WorkbenchController(
        api: PreviewControlApi(),
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: () async {},
        initialPreferences: stale,
        preferencesStore: store,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.flushPreferences();
      expect(controller.selectedCaptureKey, isNot('managed_run:missing'));
      expect(controller.selectedEnvironmentId, isNot('environment.missing'));
      expect(controller.selectedEndpointId, isNot('target.missing'));
      expect(
        WorkbenchPreferences.decode(store.encoded!),
        controller.currentPreferences,
      );
    },
  );

  test('controller restores an exact frozen Environment revision', () async {
    const restored = WorkbenchPreferences(
      section: WorkbenchSection.environments,
      selectedEnvironmentId: 'work',
      selectedEnvironmentRevision: 7,
    );
    final store = MemoryWorkbenchPreferencesStore(encoded: restored.encode());
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
      initialPreferences: restored,
      preferencesStore: store,
    );
    addTearDown(controller.dispose);

    await controller.initialize();
    expect(controller.inspectingHistoricalEnvironment, isTrue);
    expect(controller.historicalEnvironment!.id, 'work');
    expect(controller.historicalEnvironment!.revision, 7);
    await controller.flushPreferences();
    expect(
      WorkbenchPreferences.decode(store.encoded!).selectedEnvironmentRevision,
      7,
    );
  });

  test('future preference schema remains read-only and visible', () async {
    final store = MemoryWorkbenchPreferencesStore(
      encoded: const WorkbenchPreferences().encode().replaceFirst('/v2', '/v3'),
    );
    final loaded = await loadWorkbenchPreferences(
      store,
      fallbackLanguage: AppLanguage.english,
    );
    final controller = WorkbenchController(
      api: PreviewControlApi(),
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: () async {},
      initialPreferences: loaded.value,
      preferencesStore: store,
      preferencesWritable: loaded.writable,
      initialPreferencesIssue: loaded.issue,
    );
    addTearDown(controller.dispose);

    controller.setLanguage(AppLanguage.simplifiedChinese);
    await controller.flushPreferences();
    expect(store.encoded, contains('/v3'));
    expect(
      controller.preferenceWarning,
      WorkbenchPreferencesIssue.futureSchema.copyKey,
    );
  });

  testWidgets(
    'app restores language and section before showing the workbench',
    (tester) async {
      final store = MemoryWorkbenchPreferencesStore(
        encoded: const WorkbenchPreferences(
          language: AppLanguage.simplifiedChinese,
          theme: WorkbenchTheme.dark,
          section: WorkbenchSection.settings,
        ).encode(),
      );
      await tester.pumpWidget(
        ViberMateApp(
          previewMode: true,
          preferChinese: false,
          preferencesStore: store,
        ),
      );
      await _pumpUntil(tester, find.text('设置'));

      expect(find.text('管理这套 Runtime 的偏好、接入、安全与网络行为'), findsOneWidget);
      expect(find.text('工作台偏好'), findsOneWidget);
      expect(find.byKey(const Key('settings-tab-safety')), findsOneWidget);
      expect(find.text('自动'), findsOneWidget);
      final app = tester.widget<MaterialApp>(find.byType(MaterialApp));
      expect(app.themeMode, ThemeMode.dark);
      expect(app.darkTheme!.brightness, Brightness.dark);
      final scaffoldBrightness = find
          .byType(Scaffold)
          .evaluate()
          .map((element) => Theme.of(element).brightness)
          .toList();
      expect(scaffoldBrightness, [Brightness.dark]);

      await tester.tap(find.text('亮色'));
      await tester.pumpAndSettle();
      final lightContext = tester.element(find.byType(Scaffold).first);
      expect(Theme.of(lightContext).brightness, Brightness.light);
      expect(
        WorkbenchPreferences.decode(store.encoded!).theme,
        WorkbenchTheme.light,
      );

      tester.binding.platformDispatcher.platformBrightnessTestValue =
          Brightness.dark;
      addTearDown(
        tester.binding.platformDispatcher.clearPlatformBrightnessTestValue,
      );
      await tester.tap(find.text('自动'));
      await tester.pumpAndSettle();
      final systemContext = tester.element(find.byType(Scaffold).first);
      expect(Theme.of(systemContext).brightness, Brightness.dark);
      expect(
        tester.widget<MaterialApp>(find.byType(MaterialApp)).themeMode,
        ThemeMode.system,
      );
      expect(
        WorkbenchPreferences.decode(store.encoded!).theme,
        WorkbenchTheme.system,
      );

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump(const Duration(seconds: 1));
    },
  );

  testWidgets('app defaults to system theme and follows platform brightness', (
    tester,
  ) async {
    tester.binding.platformDispatcher.platformBrightnessTestValue =
        Brightness.dark;
    addTearDown(
      tester.binding.platformDispatcher.clearPlatformBrightnessTestValue,
    );
    await tester.pumpWidget(
      ViberMateApp(
        previewMode: true,
        preferChinese: false,
        preferencesStore: MemoryWorkbenchPreferencesStore(),
      ),
    );
    await _pumpUntil(tester, find.text('RUNNING NOW'));

    final app = tester.widget<MaterialApp>(find.byType(MaterialApp));
    expect(app.themeMode, ThemeMode.system);
    final context = tester.element(find.byType(Scaffold).first);
    expect(Theme.of(context).brightness, Brightness.dark);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump(const Duration(seconds: 1));
  });

  testWidgets('light endpoint editor protocol checklist uses light surfaces', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1000, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final store = MemoryWorkbenchPreferencesStore(
      encoded: const WorkbenchPreferences(
        theme: WorkbenchTheme.light,
        section: WorkbenchSection.routes,
      ).encode(),
    );
    await tester.pumpWidget(
      ViberMateApp(
        previewMode: true,
        preferChinese: false,
        preferencesStore: store,
      ),
    );
    await _pumpUntil(tester, find.byKey(const Key('endpoints-add')));

    await tester.tap(find.byKey(const Key('endpoints-add')));
    await tester.pumpAndSettle();
    final dialogContext = tester.element(find.byType(AlertDialog));
    expect(Theme.of(dialogContext).brightness, Brightness.light);
    expect(
      Theme.of(dialogContext).dialogTheme.backgroundColor,
      ViberColors.light.panel,
    );

    final protocols = find.byKey(const Key('endpoint-editor-protocols'));
    final decoration =
        tester.widget<DecoratedBox>(protocols).decoration as BoxDecoration;
    expect(decoration.color, ViberColors.light.input);
    expect((decoration.border! as Border).top.color, ViberColors.light.divider);
    final responses = find.byKey(
      const Key('endpoint-editor-protocol-openai_responses'),
    );
    expect(tester.widget<CompactCheckboxOption>(responses).value, isFalse);
    await tester.tap(responses);
    await tester.pumpAndSettle();
    expect(tester.widget<CompactCheckboxOption>(responses).value, isTrue);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump(const Duration(seconds: 1));
  });

  testWidgets('390px Chinese frozen Environment remains read-only and usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final store = MemoryWorkbenchPreferencesStore(
      encoded: const WorkbenchPreferences(
        language: AppLanguage.simplifiedChinese,
        section: WorkbenchSection.environments,
        selectedEnvironmentId: 'work',
        selectedEnvironmentRevision: 7,
      ).encode(),
    );
    await tester.pumpWidget(
      ViberMateApp(
        previewMode: true,
        preferChinese: false,
        preferencesStore: store,
      ),
    );
    await _pumpUntil(
      tester,
      find.byKey(const Key('environment-history-banner')),
    );

    expect(find.text('冻结证据'), findsOneWidget);
    expect(find.text('返回当前修订'), findsOneWidget);
    expect(find.byKey(const Key('environment-edit')), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const Key('environment-history-current')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-history-banner')), findsNothing);
    expect(find.byKey(const Key('environment-edit')), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump(const Duration(seconds: 1));
  });
}

Future<void> _pumpUntil(WidgetTester tester, Finder finder) async {
  for (var attempt = 0; attempt < 100; attempt += 1) {
    await tester.pump(const Duration(milliseconds: 20));
    if (finder.evaluate().isNotEmpty) return;
  }
  fail('Timed out waiting for $finder');
}

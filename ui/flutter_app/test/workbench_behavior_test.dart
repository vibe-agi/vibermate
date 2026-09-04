import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/account_selector_editor.dart';
import 'package:vibermate_app/features/workbench/conversation_timeline.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

/// Opens a Conversation the way the product does now: through the Capture that
/// owns it. A top-level Conversations section used to be a second path to the
/// same timeline; it was retired because every Conversation belongs to a
/// Capture, so these tests reach the timeline the only way a user can.
///
/// Selecting a Capture already selects its main Conversation, and the directory
/// is a row list when the window is wide and a select field when it is not, so
/// this covers all three shapes rather than assuming one.
Future<void> openCaptureConversation(
  WidgetTester tester, {
  required String capture,
  String? conversationLabel,
}) async {
  final captureRow = find.byKey(Key('capture-row-$capture'));
  await tester.ensureVisible(captureRow);
  await tester.tap(captureRow);
  await tester.pumpAndSettle();

  // A Capture selects the preferred main Conversation itself. Native client
  // Session IDs deliberately define Conversation identity, so a behavior test
  // must not reach back to the retired capture_run:<id>:main synthetic key.
  if (conversationLabel == null) return;

  final pane = find.byKey(const Key('capture-conversation-pane'));
  final label = find.descendant(
    of: pane,
    matching: find.text(conversationLabel),
  );
  final row = find.ancestor(
    of: label.first,
    matching: find.byWidgetPredicate(
      (widget) =>
          widget.key is ValueKey<String> &&
          (widget.key! as ValueKey<String>).value.startsWith(
            'capture-conversation-',
          ),
    ),
  );
  await tester.ensureVisible(row.first);
  await tester.tap(row.first);
  await tester.pumpAndSettle();
}

/// Brings a Turn into view. The timeline is a virtualized list, so a Turn that
/// was mounted a moment ago can be gone after scrolling elsewhere, and
/// ensureVisible throws on a Turn that is not currently built.
Future<void> ensureTurnVisible(WidgetTester tester, Finder turn) async {
  if (turn.evaluate().isEmpty) {
    await tester.scrollUntilVisible(
      turn,
      -160,
      scrollable: find
          .descendant(
            of: find.byKey(const Key('conversation-timeline-scroll')),
            matching: find.byType(Scrollable),
          )
          .first,
    );
  }
  await tester.ensureVisible(turn);
  await tester.pumpAndSettle();
}

double paintedFormSurfaceHeight(WidgetTester tester, Finder field) {
  final fieldWidth = tester.getSize(field).width;
  final painters = find.descendant(
    of: field,
    matching: find.byType(CustomPaint),
  );
  final matchingHeights = <double>[];
  for (var index = 0; index < painters.evaluate().length; index += 1) {
    final size = tester.getSize(painters.at(index));
    if ((size.width - fieldWidth).abs() < 0.01) {
      matchingHeights.add(size.height);
    }
  }
  expect(
    matchingHeights,
    isNotEmpty,
    reason: 'the form field must paint a full-width decorated surface',
  );
  return matchingHeights.reduce((left, right) => left > right ? left : right);
}

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;

  testWidgets('desktop select keeps popup rows compact and aligned', (
    tester,
  ) async {
    String? selected = 'one';
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Center(
            child: SizedBox(
              width: 180,
              child: CompactSelectField<String>(
                initialValue: selected,
                decoration: const InputDecoration(labelText: 'Route account'),
                items: const [
                  DropdownMenuItem(value: 'one', child: Text('Account one')),
                  DropdownMenuItem(value: 'two', child: Text('Account two')),
                ],
                onChanged: (value) => selected = value,
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final field = find.byType(CompactSelectField<String>);
    final fieldSize = tester.getSize(field);
    expect(fieldSize.height, ViberMetrics.controlHeight);
    final anchor = tester.widget<MenuAnchor>(
      find.descendant(of: field, matching: find.byType(MenuAnchor)),
    );
    expect(
      anchor.style?.maximumSize?.resolve({})?.height,
      ViberMetrics.compactControlHeight * 2 + 4,
    );
    expect(
      Theme.of(tester.element(field)).textTheme.labelLarge?.fontSize,
      ViberType.control,
    );
    await tester.tap(field);
    await tester.pumpAndSettle();

    final rows = find.byType(MenuItemButton);
    expect(rows, findsNWidgets(2));
    for (var index = 0; index < 2; index += 1) {
      expect(
        tester.getSize(rows.at(index)).height,
        ViberMetrics.compactControlHeight,
      );
      expect(tester.getSize(rows.at(index)).width, fieldSize.width - 4);
    }
    expect(
      Theme.of(tester.element(rows.first)).textTheme.bodySmall?.fontSize,
      ViberType.supporting,
    );

    await tester.tap(find.text('Account two').last);
    await tester.pumpAndSettle();
    expect(selected, 'two');
    expect(find.byType(MenuItemButton), findsNothing);
    expect(find.text('Account two'), findsOneWidget);
  });

  testWidgets('desktop select popup keeps its dark surface and text contrast', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: Scaffold(
          body: Center(
            child: SizedBox(
              width: 260,
              child: CompactSelectField<String>(
                initialValue: 'fixed',
                items: const [
                  DropdownMenuItem(
                    value: 'fixed',
                    child: Text('Fixed account'),
                  ),
                  DropdownMenuItem(
                    value: 'javascript',
                    child: Text('JavaScript selector'),
                  ),
                ],
                onChanged: (_) {},
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final field = find.byType(CompactSelectField<String>);
    final colors = ViberColors.dark;
    final anchor = tester.widget<MenuAnchor>(
      find.descendant(of: field, matching: find.byType(MenuAnchor)),
    );
    expect(anchor.style?.backgroundColor?.resolve({}), colors.panel);

    await tester.tap(field);
    await tester.pumpAndSettle();
    final rows = find.byType(MenuItemButton);
    expect(rows, findsNWidgets(2));
    for (var index = 0; index < 2; index += 1) {
      expect(
        tester
            .widget<MenuItemButton>(rows.at(index))
            .style
            ?.foregroundColor
            ?.resolve({}),
        colors.text,
      );
    }
  });

  testWidgets('desktop text and select fields share one compact metric', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Center(
            child: SizedBox(
              width: 360,
              child: Row(
                children: [
                  const Expanded(
                    child: TextField(
                      key: Key('metric-text-field'),
                      decoration: InputDecoration(labelText: 'Retention'),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: CompactSelectField<String>(
                      key: const Key('metric-select-field'),
                      initialValue: 'observe',
                      decoration: const InputDecoration(
                        labelText: 'Tool decisions',
                      ),
                      items: const [
                        DropdownMenuItem(
                          value: 'observe',
                          child: Text('Observe'),
                        ),
                      ],
                      onChanged: (_) {},
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      tester.getSize(find.byKey(const Key('metric-text-field'))).height,
      ViberMetrics.controlHeight,
    );
    expect(
      tester.getSize(find.byKey(const Key('metric-select-field'))).height,
      ViberMetrics.controlHeight,
    );
    for (final key in const ['metric-text-field', 'metric-select-field']) {
      expect(
        paintedFormSurfaceHeight(tester, find.byKey(Key(key))),
        ViberMetrics.controlHeight,
        reason: '$key must paint the full standard form-control surface',
      );
    }
  });

  testWidgets('Capture filter clears and older pages remain reachable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(900, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(dashboardCaptureLimit: 2);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull, reason: 'initial Capture layout');

    final search = find.byType(TextField).first;
    await tester.enterText(search, 'not-loaded-yet');
    await tester.pump();
    expect(tester.takeException(), isNull, reason: 'filtered Capture layout');
    expect(find.text('No loaded captures match this filter.'), findsOneWidget);
    expect(find.byKey(const Key('captures-load-more')), findsOneWidget);

    await tester.tap(find.byTooltip('Clear capture filter'));
    await tester.pump();
    expect(tester.takeException(), isNull, reason: 'cleared Capture layout');
    expect(tester.widget<TextField>(search).controller!.text, isEmpty);
    expect(find.text('No loaded captures match this filter.'), findsNothing);

    await tester.tap(find.byKey(const Key('captures-load-more')));
    await tester.pumpAndSettle();
    expect(controller.data!.captures, hasLength(9));
    expect(controller.data!.captureNextCursor, isNull);
    expect(find.byKey(const Key('captures-load-more')), findsNothing);
    expect(tester.takeException(), isNull, reason: 'loaded Capture layout');

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
    controller.dispose();
  });

  testWidgets('empty Captures leads a novice to the normal Agent launch path', (
    tester,
  ) async {
    addTearDown(() => tester.binding.setSurfaceSize(null));
    for (final scenario in const [
      (
        size: Size(1180, 760),
        language: AppLanguage.english,
        empty: 'No captures yet.',
        detail: 'Start Codex or Claude through ViberMate from Terminal.',
        settings: 'Terminal command',
      ),
      (
        size: Size(390, 760),
        language: AppLanguage.simplifiedChinese,
        empty: '还没有运行记录。',
        detail: '先从终端通过 ViberMate 启动 Codex 或 Claude。',
        settings: '终端命令',
      ),
    ]) {
      await tester.binding.setSurfaceSize(scenario.size);
      final api = PreviewControlApi(seedCaptures: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: false,
        closeRuntime: api.close,
        initialPreferences: WorkbenchPreferences(language: scenario.language),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text(scenario.empty), findsOneWidget);
      expect(find.text(scenario.detail), findsOneWidget);
      final nextAction = find.byKey(
        const Key('capture-empty-open-terminal-settings'),
      );
      expect(nextAction, findsOneWidget);
      await tester.tap(nextAction);
      await tester.pumpAndSettle();

      expect(controller.section, WorkbenchSection.settings);
      expect(find.text(scenario.settings), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
      await tester.pump();
    }
  });

  testWidgets('the Capture directory resizes, collapses, and reopens', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    final capturePane = find.byKey(const Key('capture-master-pane'));
    final captureDivider = find.byKey(const Key('capture-master-divider'));
    expect(tester.getSize(capturePane).width, ViberMetrics.masterPaneWidth);
    final drag = await tester.startGesture(tester.getCenter(captureDivider));
    await drag.moveBy(const Offset(16, 0));
    await tester.pump();
    await drag.moveBy(const Offset(32, 0));
    await tester.pump();
    await drag.moveBy(const Offset(16, 0));
    await drag.up();
    await tester.pumpAndSettle();
    expect(
      tester.getSize(capturePane).width,
      greaterThan(ViberMetrics.masterPaneWidth),
    );

    await tester.tap(find.byKey(const Key('capture-directory-toggle')));
    await tester.pumpAndSettle();
    expect(capturePane, findsNothing);
    await tester.tap(find.byKey(const Key('capture-directory-toggle')));
    await tester.pumpAndSettle();
    expect(capturePane, findsOneWidget);

    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump(const Duration(seconds: 1));
  });

  testWidgets('800px title bar keeps every global command operable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(800, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('offline-hold-command')), findsOneWidget);
    expect(find.byKey(const Key('approval-attention')), findsOneWidget);
    expect(find.byIcon(Icons.science_outlined), findsOneWidget);
    expect(find.byIcon(Icons.refresh), findsOneWidget);
    expect(
      tester.getSize(find.byKey(const Key('offline-hold-command'))).height,
      ViberMetrics.controlHeight,
    );
    expect(
      tester.getSize(find.byKey(const Key('approval-attention'))).height,
      ViberMetrics.controlHeight,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('keyboard navigation and screen-reader authority stay explicit', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(800, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final semantics = tester.ensureSemantics();
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.bySemanticsLabel(RegExp(r'^Traffic\s+⌘1$')), findsOneWidget);
    expect(
      find.bySemanticsLabel(
        RegExp(r'^Prepare to disconnect · Online operation$'),
      ),
      findsOneWidget,
    );
    final scaffoldContext = tester.element(find.byType(Scaffold).first);
    final theme = Theme.of(scaffoldContext);
    final colors = theme.brightness == Brightness.dark
        ? ViberColors.dark
        : ViberColors.light;
    expect(theme.focusColor, isNot(colors.selection));
    final focusedSide = theme.outlinedButtonTheme.style?.side?.resolve({
      WidgetState.focused,
    });
    final restingSide = theme.outlinedButtonTheme.style?.side?.resolve({});
    expect(focusedSide?.color, colors.focus);
    expect(focusedSide?.width, 1.5);
    expect(restingSide?.color, colors.divider);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.digit3);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pumpAndSettle();
    // Retiring Conversations moved every later section up one slot.
    expect(find.byKey(const Key('environment-master-pane')), findsOneWidget);

    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    expect(FocusManager.instance.primaryFocus, isNotNull);
    expect(tester.takeException(), isNull);
    semantics.dispose();
  });

  testWidgets('Offline protection requires review before traffic changes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    final command = find.byKey(const Key('offline-hold-command'));
    expect(command, findsOneWidget);
    expect(
      find.bySemanticsLabel(
        RegExp(r'^Prepare to disconnect · Online operation$'),
      ),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('offline-confirmation')), findsOneWidget);
    expect(find.text('Prepare to disconnect?'), findsOneWidget);
    expect(find.byKey(const Key('offline-confirm-action')), findsOneWidget);
    await tester.tap(find.text('Cancel').last);
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(
        RegExp(r'^Prepare to disconnect · Online operation$'),
      ),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(RegExp(r'^Resume online · Safe to disconnect$')),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.text('Resume external work?'), findsOneWidget);
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(
        RegExp(r'^Prepare to disconnect · Online operation$'),
      ),
      findsOneWidget,
    );
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('workbench navigation is grouped into three user task areas', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      runtimeTarget: 'server.local:9666',
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('workbench-area-traffic')), findsOneWidget);
    expect(find.byKey(const Key('workbench-area-insights')), findsOneWidget);
    expect(
      find.byKey(const Key('workbench-area-configuration')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('workbench-tab-captures')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-network')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-environments')), findsNothing);

    await tester.tap(find.byKey(const Key('workbench-area-configuration')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('workbench-tab-environments')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-routes')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-code-library')), findsOneWidget);
    expect(find.byKey(const Key('workbench-settings-nav')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-captures')), findsNothing);
    expect(find.byType(TabBar), findsOneWidget);
    final taskTabAnchor = tester
        .getTopLeft(find.byKey(const Key('workbench-tab-environments')))
        .dx;

    await tester.tap(find.byKey(const Key('workbench-settings-nav')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('workbench-tab-environments')), findsNothing);
    expect(find.byKey(const Key('workbench-tab-routes')), findsNothing);
    expect(find.byKey(const Key('workbench-tab-code-library')), findsNothing);
    expect(find.byType(TabBar), findsOneWidget);
    expect(
      tester.getTopLeft(find.byKey(const Key('settings-tab-general'))).dx,
      taskTabAnchor,
    );

    await tester.tap(find.byKey(const Key('workbench-area-configuration')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('workbench-tab-environments')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-routes')), findsOneWidget);
    expect(find.byKey(const Key('workbench-tab-code-library')), findsOneWidget);
    await tester.binding.setSurfaceSize(const Size(390, 760));
    await tester.pumpAndSettle();
    expect(find.byType(TabBar), findsOneWidget);
    expect(
      tester.getTopLeft(find.byKey(const Key('workbench-tab-environments'))).dx,
      taskTabAnchor,
    );
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('startup failure keeps raw diagnostics behind technical details', (
    tester,
  ) async {
    final controller = WorkbenchController(
      api: _FailingDashboardApi(),
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      closeRuntime: () async {},
    );
    addTearDown(controller.dispose);
    await controller.refresh();

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.text(
        'ViberMate Runtime is unavailable. No retained evidence was changed. Check the Runtime process, then try again.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('startup-secret'), findsNothing);
    await tester.tap(find.text('Technical details'));
    await tester.pumpAndSettle();
    expect(find.textContaining('startup-secret'), findsOneWidget);
    expect(find.text('Retry'), findsOneWidget);
  });

  testWidgets('390px Chinese Offline protection stays operable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('offline-settings-panel')), findsOneWidget);
    expect(find.text('断网保护'), findsOneWidget);
    expect(find.text('联网运行'), findsOneWidget);
    final action = find.byKey(const Key('offline-settings-action'));
    await tester.ensureVisible(action);
    await tester.tap(action);
    await tester.pumpAndSettle();
    expect(find.text('准备断网？'), findsOneWidget);
    expect(find.byKey(const Key('offline-confirm-action')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'Terminal command install and removal require review and preserve ownership boundaries',
    (tester) async {
      String? copiedCommand;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedCommand =
                (call.arguments as Map<Object?, Object?>)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1000, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.settings_outlined).first);
      await tester.pumpAndSettle();
      final panel = find.byKey(const Key('terminal-command-panel'));
      expect(panel, findsOneWidget);
      await tester.ensureVisible(panel);
      expect(find.text('Set up'), findsOneWidget);
      expect(find.byKey(const Key('terminal-command-install')), findsOneWidget);
      expect(
        tester
            .widget<IconButton>(
              find.byKey(const Key('managed-run-copy-claude')),
            )
            .onPressed,
        isNull,
      );

      await tester.tap(find.byKey(const Key('terminal-command-install')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('terminal-command-confirmation')),
        findsOneWidget,
      );
      expect(find.text('Install vibermate for Terminal?'), findsOneWidget);
      expect(
        find.textContaining('No existing object will be replaced.'),
        findsOneWidget,
      );
      await tester.tap(find.text('Cancel').last);
      await tester.pumpAndSettle();
      expect(find.text('Set up'), findsOneWidget);

      await tester.tap(find.byKey(const Key('terminal-command-install')));
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('terminal-command-confirm-action')),
      );
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('terminal-command-confirmation')),
        findsNothing,
      );
      expect(
        find.descendant(of: panel, matching: find.text('Ready')),
        findsOneWidget,
      );
      expect(find.text('Terminal command installed.'), findsOneWidget);
      final copyClaude = find.byKey(const Key('managed-run-copy-claude'));
      await tester.scrollUntilVisible(
        copyClaude,
        -120,
        scrollable: find
            .descendant(
              of: find.byKey(const Key('settings-scroll')),
              matching: find.byType(Scrollable),
            )
            .first,
      );
      await tester.tap(copyClaude);
      await tester.pumpAndSettle();
      expect(copiedCommand, 'vibermate run -- claude');
      expect(find.text('Claude command copied'), findsOneWidget);

      final remove = find.byKey(const Key('terminal-command-remove'));
      await tester.tap(find.byKey(const Key('settings-tab-proxy')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('settings-tab-general')));
      await tester.pumpAndSettle();
      await tester.scrollUntilVisible(
        remove,
        -120,
        scrollable: find
            .descendant(
              of: find.byKey(const Key('settings-scroll')),
              matching: find.byType(Scrollable),
            )
            .first,
      );
      await tester.tap(remove);
      await tester.pumpAndSettle();
      expect(find.text('Remove the Terminal command?'), findsOneWidget);
      await tester.tap(
        find.byKey(const Key('terminal-command-confirm-action')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Set up'), findsOneWidget);
      expect(find.text('Owned Terminal command removed.'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese Terminal command review stays usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();
    final panel = find.byKey(const Key('terminal-command-panel'));
    await tester.ensureVisible(panel);
    expect(find.text('终端命令'), findsOneWidget);
    expect(find.text('需要设置'), findsOneWidget);
    final install = find.byKey(const Key('terminal-command-install'));
    await tester.drag(find.byType(ListView).last, const Offset(0, -120));
    await tester.pumpAndSettle();
    await tester.tap(install);
    await tester.pumpAndSettle();
    expect(find.text('为终端安装 vibermate？'), findsOneWidget);
    expect(
      find.byKey(const Key('terminal-command-confirm-action')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    '390px missing Terminal link offers one safe repair and hides diagnostics',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      const rawDiagnostic =
          'the terminal command recorded by the app is missing';
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(
          initial: const TerminalCommandStatus(
            state: TerminalCommandState.targetMissing,
            sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
            targetPath: '/Users/preview/.local/bin/vibermate',
            detail: rawDiagnostic,
          ),
        ),
        previewMode: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          language: AppLanguage.simplifiedChinese,
          section: WorkbenchSection.settings,
        ),
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      final panel = find.byKey(const Key('terminal-command-panel'));
      await tester.ensureVisible(panel);
      expect(find.text('需要修复'), findsOneWidget);
      expect(find.text('一键修复'), findsOneWidget);
      expect(find.byKey(const Key('terminal-command-remove')), findsNothing);
      expect(find.text(rawDiagnostic), findsNothing);

      final details = find.byKey(const Key('terminal-command-details-toggle'));
      await tester.scrollUntilVisible(
        details,
        120,
        scrollable: find.byType(Scrollable).last,
      );
      await tester.pumpAndSettle();
      await tester.tap(details);
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('terminal-command-technical-details')),
        findsOneWidget,
      );
      expect(find.text(rawDiagnostic), findsOneWidget);

      final repair = find.byKey(const Key('terminal-command-repair'));
      await tester.scrollUntilVisible(
        repair,
        -120,
        scrollable: find.byType(Scrollable).last,
      );
      await tester.pumpAndSettle();
      await tester.tap(repair);
      await tester.pumpAndSettle();
      expect(find.text('修复终端命令？'), findsOneWidget);
      expect(find.textContaining('不会替换任何现有对象'), findsOneWidget);
      await tester.tap(
        find.byKey(const Key('terminal-command-confirm-action')),
      );
      await tester.pumpAndSettle();
      expect(
        find.descendant(of: panel, matching: find.text('可用')),
        findsOneWidget,
      );
      expect(find.text('终端命令已修复，可以使用。'), findsOneWidget);
      expect(tester.takeException(), isNull);

      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Settings gives Runtime User management its own tab', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      runtimeTarget: 'server.local:9666',
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
      ),
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
    expect(find.byKey(const Key('settings-tab-general')), findsOneWidget);
    expect(find.byKey(const Key('settings-tab-users')), findsOneWidget);
    expect(find.byKey(const Key('server-runtime-access')), findsNothing);
    await tester.tap(find.byKey(const Key('settings-tab-users')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('server-runtime-access')), findsOneWidget);
    expect(find.text('http://192.168.1.44:9666/'), findsOneWidget);
    expect(find.text('Manage this Runtime in a browser'), findsOneWidget);
    expect(find.text('Terminal command'), findsNothing);
    expect(
      find.text('vibermate login --server 192.168.1.44:9666'),
      findsOneWidget,
    );
    expect(
      find.text('vibermate run --server 192.168.1.44:9666 -- codex'),
      findsOneWidget,
    );
    final command = tester.widget<Text>(
      find.text('vibermate run --server 192.168.1.44:9666 -- codex'),
    );
    expect(command.maxLines, isNot(1));
    expect(command.overflow, isNot(TextOverflow.ellipsis));
    expect(controller.serverAccess?.requiresRuntimeUserLogin, isTrue);
    expect(controller.runtimeUsers?.single.username, 'alice');
    expect(controller.runtimeUsage, isNull);
    expect(
      find.byKey(const Key('runtime-user-row-user.preview.alice')),
      findsOneWidget,
    );
    expect(find.text('18 turns'), findsNothing);

    final add = find.byKey(const Key('runtime-user-add'));
    await tester.drag(
      find.byKey(const Key('runtime-users-settings-scroll')),
      const Offset(0, -220),
    );
    await tester.pumpAndSettle();
    await tester.ensureVisible(add);
    await tester.pumpAndSettle();
    await tester.tap(add);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('runtime-user-dialog')), findsOneWidget);
    await tester.enterText(
      find.byKey(const Key('runtime-user-username')),
      'bob',
    );
    await tester.enterText(
      find.byKey(const Key('runtime-user-password')),
      'test-password',
    );
    await tester.pump();
    final create = find.byKey(const Key('runtime-user-create'));
    expect(tester.widget<FilledButton>(create).onPressed, isNotNull);
    await tester.tap(create);
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('runtime-user-dialog')), findsNothing);
    expect(
      controller.runtimeUsers?.map((user) => user.username),
      contains('bob'),
    );
    expect(find.text('bob'), findsOneWidget);
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Settings publishes reusable network exit revisions', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      terminalManagement: false,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
        language: AppLanguage.simplifiedChinese,
      ),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('settings-tab-general')), findsOneWidget);
    expect(find.byKey(const Key('settings-tab-proxy')), findsOneWidget);
    await tester.tap(find.byKey(const Key('settings-tab-proxy')));
    await tester.pumpAndSettle();
    expect(find.text('网络出口方案'), findsOneWidget);
    expect(
      find.byKey(const Key('egress-profile-row-profile.direct')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('egress-profile-edit-profile.direct')),
      findsNothing,
    );

    await tester.tap(find.byKey(const Key('egress-profile-add')));
    await tester.pumpAndSettle();
    final profileDialog = find.byKey(
      const Key('settings-egress-profile-dialog'),
    );
    expect(profileDialog, findsOneWidget);
    expect(find.text('新建网络出口方案'), findsNWidgets(2));
    expect(find.text('方案名称'), findsOneWidget);
    await tester.enterText(
      find.byKey(const Key('egress-profile-display-name')),
      '团队代理',
    );
    await tester.tap(find.byKey(const Key('egress-profile-proxy-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-proxy-socks5')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('egress-profile-proxy-endpoint')),
      '127.0.0.1:1080',
    );
    await tester.tap(find.byKey(const Key('egress-profile-resolver-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-resolver-doh')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-doh-preset')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-doh-cloudflare')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-doh-transport')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-doh-via-proxy')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-publish')));
    await tester.pumpAndSettle();

    var catalog = await api.egressProfiles();
    final created = catalog.items.singleWhere(
      (profile) => profile.id != EgressProfileRevision.direct.id,
    );
    expect(created.revision, 1);
    expect(created.policy.proxy.endpoint, '127.0.0.1:1080');
    expect(created.policy.resolver.dohUrl, 'https://1.1.1.1/dns-query');
    expect(created.policy.resolver.transport, 'proxy');
    expect(find.textContaining('团队代理 · r1'), findsOneWidget);

    await tester.tap(find.byKey(Key('egress-profile-edit-${created.id}')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('egress-profile-display-name')),
      '团队代理 2',
    );
    await tester.tap(find.byKey(const Key('egress-profile-publish')));
    await tester.pumpAndSettle();
    catalog = await api.egressProfiles();
    final updated = catalog.items.singleWhere(
      (profile) => profile.id == created.id,
    );
    expect(updated.revision, 2);
    expect(await api.egressProfileRevision(created.id, 1), created);
    expect(find.textContaining('团队代理 2 · r2'), findsOneWidget);
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px usage ranks and searches 20 Runtime Users', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    for (var index = 2; index <= 20; index += 1) {
      await api.createRuntimeUser(
        username: 'user${index.toString().padLeft(2, '0')}',
        password: 'test-password',
      );
    }
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      runtimeTarget: 'server.local:9666',
      closeRuntime: api.close,
      clock: () => DateTime.utc(2026, 8, 25, 12),
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    final navigation = find.byKey(const Key('usage-dashboard-nav'));
    expect(navigation, findsOneWidget);
    await tester.tap(navigation);
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('usage-dashboard')), findsOneWidget);
    expect(find.text('Server'), findsOneWidget);
    expect(find.byKey(const Key('usage-total-api-calls')), findsOneWidget);
    expect(find.byKey(const Key('usage-active-runs')), findsOneWidget);
    expect(find.byKey(const Key('usage-input-tokens')), findsOneWidget);
    expect(find.byKey(const Key('usage-output-tokens')), findsOneWidget);
    expect(find.byKey(const Key('usage-range-30')), findsNothing);
    expect(find.byKey(const Key('usage-range-90')), findsNothing);
    expect(find.byKey(const Key('usage-range-365')), findsNothing);
    expect(find.byKey(const Key('usage-team-heatmap')), findsOneWidget);
    expect(
      find.byKey(const Key('usage-team-day-month-2026-08')),
      findsOneWidget,
    );
    expect(controller.usageRangeDays, 365);
    expect(
      DateTime.parse(
        controller.runtimeUsage!.period.until,
      ).difference(DateTime.parse(controller.runtimeUsage!.period.from)).inDays,
      365,
    );
    final callsMetric = find.byKey(const Key('usage-metric-api-calls'));
    final tokensMetric = find.byKey(const Key('usage-metric-tokens'));
    expect(
      tester.getSize(callsMetric).width,
      tester.getSize(tokensMetric).width,
    );
    expect(tester.getSize(callsMetric).height, ViberMetrics.controlHeight);
    expect(tester.getSize(tokensMetric).height, ViberMetrics.controlHeight);
    String cellSemantics(String date) => tester
        .widgetList<Semantics>(
          find.descendant(
            of: find.byKey(Key('usage-team-day-$date')),
            matching: find.byType(Semantics),
          ),
        )
        .map((widget) => widget.properties.label ?? '')
        .join(' ');
    expect(cellSemantics('2026-08-24'), contains('no retained API call'));
    expect(cellSemantics('2026-08-25'), contains('complete evidence'));
    await tester.tap(find.byKey(const Key('usage-metric-tokens')));
    await tester.pump();
    expect(cellSemantics('2026-08-25'), contains('incomplete evidence'));
    expect(
      find.byKey(const Key('usage-team-day-month-2026-01')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('usage-user-user.preview.alice-day-month-2026-01')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('usage-ranking')), findsOneWidget);
    expect(find.byKey(const Key('usage-ranking-scroll')), findsOneWidget);
    expect(find.byKey(const Key('usage-ranking-count')), findsOneWidget);
    expect(
      find.byKey(const Key('usage-user-user.preview.alice')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('usage-user-activity-user.preview.alice')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('usage-user-user.preview.20')), findsNothing);

    await tester.enterText(
      find.byKey(const Key('usage-user-search')),
      'user20',
    );
    await tester.pump();
    expect(find.byKey(const Key('usage-user-user.preview.20')), findsOneWidget);
    expect(
      find.byKey(const Key('usage-user-activity-user.preview.20')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('usage-user-user.preview.alice')),
      findsNothing,
    );
    await tester.enterText(find.byKey(const Key('usage-user-search')), '');
    await tester.pump();
    final alice = find.byKey(const Key('usage-user-user.preview.alice'));
    await tester.ensureVisible(alice);
    await tester.pumpAndSettle();
    await tester.tap(alice);
    await tester.pumpAndSettle();
    expect(
      tester
          .getTopLeft(
            find.byKey(const Key('usage-user-evidence-user.preview.alice')),
          )
          .dy,
      lessThan(300),
    );
    expect(
      find.byKey(const Key('usage-user-heatmap-user.preview.alice')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('usage groups the same Workspace across Capture Runs', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      runtimeTarget: 'server.local:9666',
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('usage-dashboard-nav')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('usage-dimension-content-workspaces')),
      findsOneWidget,
    );
    final workspace = find.byKey(
      const Key(
        'usage-workspace-user.preview.alice-workspace.preview.vibermate',
      ),
    );
    expect(workspace, findsOneWidget);
    expect(
      find.descendant(
        of: workspace,
        matching: find.textContaining('2 Captures · 12 calls'),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('usage drill-down shows one grouping dimension at a time', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      runtimeTarget: 'server.local:9666',
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('usage-dashboard-nav')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('usage-dimension-content-workspaces')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('usage-model-user.preview.alice-0')),
      findsNothing,
    );
    expect(
      find.byKey(const Key('usage-session-user.preview.alice-0')),
      findsNothing,
    );

    final models = find.byKey(const Key('usage-dimension-models'));
    await tester.ensureVisible(models);
    await tester.pumpAndSettle();
    await tester.tap(models);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('usage-dimension-content-models')),
      findsOneWidget,
    );
    expect(find.text('gpt-5.6-sol'), findsOneWidget);
    expect(find.text('dashscope:deepseek-v4-flash-0731'), findsOneWidget);
    expect(
      find.byKey(const Key('usage-dimension-content-workspaces')),
      findsNothing,
    );

    final sessions = find.byKey(const Key('usage-dimension-sessions'));
    await tester.tap(sessions);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('usage-dimension-content-sessions')),
      findsOneWidget,
    );
    expect(find.textContaining('01a02deb'), findsOneWidget);
    expect(find.text('gpt-5.6-sol'), findsNothing);
    expect(
      find.byKey(const Key('usage-dimension-content-workspaces')),
      findsNothing,
    );
    expect(tester.takeException(), isNull);

    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'wide workbench exposes dense timeline and endpoint-owned accounts',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      expect(find.text('Capture conversation'), findsOneWidget);
      // The Capture header is an aggregate over every proven Conversation,
      // independent of how many rows the selected timeline has paged in.
      expect(find.textContaining('224 turns'), findsOneWidget);
      expect(
        find.byKey(const Key('capture-aggregate-summary')),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('conversation-load-earlier')));
      await tester.pumpAndSettle();
      expect(find.textContaining('224 turns'), findsOneWidget);
      expect(find.byKey(const Key('conversation-load-earlier')), findsNothing);
      expect(tester.takeException(), isNull);

      await _openUpstreamServices(tester);
      expect(find.text('Upstream services'), findsWidgets);
      expect(find.text('Anthropic · Work'), findsOneWidget);
      expect(find.text('Orbit · Team Pool'), findsNothing);

      await tester.tap(find.text('Orbit Relay · Tokyo').first);
      await tester.pumpAndSettle();
      expect(find.text('Orbit · Team Pool'), findsOneWidget);
      expect(find.text('Anthropic · Work'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('Capture keeps its Environment identity read-only after launch', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    final currentScope = find.byKey(const Key('capture-environment-scope'));
    expect(currentScope, findsOneWidget);
    expect(
      find.descendant(of: currentScope, matching: find.text('Work')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('capture-environment-readonly')),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: currentScope,
        matching: find.byType(CompactSelectField<String>),
      ),
      findsNothing,
    );

    final finishedCapture = find.byKey(
      const Key('capture-row-managed_run:run-8'),
    );
    await tester.ensureVisible(finishedCapture);
    await tester.tap(finishedCapture);
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('capture-environment-readonly')),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: find.byKey(const Key('capture-environment-scope')),
        matching: find.byType(CompactSelectField<String>),
      ),
      findsNothing,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'Capture surfaces exact client version and compatibility evidence',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      final evidence = find.byKey(
        const Key('capture-client-compatibility-managed_run:run-1'),
      );
      expect(evidence, findsOneWidget);
      expect(
        find.descendant(of: evidence, matching: find.text('2.1.220')),
        findsOneWidget,
      );
      expect(
        find.descendant(
          of: evidence,
          matching: find.text('Exact release verified'),
        ),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'running Capture offers the latest published Environment for its next Turn',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      final current = await api.environmentRevision('work', 7);
      final draft = await api.saveEnvironmentDraft(
        environmentId: current.id,
        expectedBaseRevision: current.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          current,
          expectedDraftRevision: 0,
          name: current.name,
        ),
      );
      await api.publishEnvironmentDraft('work', draft.draftRevision);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      final action = find.byKey(const Key('capture-environment-apply-latest'));
      expect(action, findsOneWidget);
      expect(
        find.textContaining('Started r7 · using r7 · published r8'),
        findsOneWidget,
      );

      await tester.tap(action);
      await tester.pumpAndSettle();

      expect(action, findsNothing);
      expect(controller.selectedAssignment?.launchEnvironmentRevision, 7);
      expect(controller.selectedAssignment?.environmentRevision, 8);
      expect(find.textContaining('Started r7 · using r8'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
      await api.close();
    },
  );

  testWidgets('390px Chinese keeps the runtime Environment control compact', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Claude Code').first);
    await tester.pumpAndSettle();

    expect(find.text('流量策略'), findsOneWidget);
    expect(find.byKey(const Key('capture-environment-scope')), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('capture-environment-scope')),
        matching: find.byType(CompactSelectField<String>),
      ),
      findsNothing,
    );
    expect(
      find.byKey(const Key('capture-environment-readonly')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'Manual Capture exposes independent Exchanges without inventing Turns',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(
        find.byKey(const Key('capture-row-manual_capture:manual-figma')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Independent exchanges'), findsOneWidget);
      expect(find.textContaining('24 exchanges'), findsOneWidget);
      expect(find.text('24 turns'), findsNothing);
      expect(find.bySemanticsLabel('Turn map'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'a Capture Conversation preserves boundaries and expands real Exchange evidence',
    (tester) async {
      String? copiedEvidence;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedEvidence =
                (call.arguments as Map<Object?, Object?>)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await openCaptureConversation(tester, capture: 'managed_run:run-1');
      expect(find.textContaining('195 Turns'), findsWidgets);

      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await ensureTurnVisible(tester, turn);
      final turnCard = find.byKey(
        const Key('conversation-turn-card-run-1-exchange-222'),
      );
      final collapsedDecoration =
          tester.widget<Container>(turnCard).decoration! as BoxDecoration;
      expect(collapsedDecoration.borderRadius, ViberMetrics.surfaceRadius);
      final turnNode = find.byKey(
        const Key('conversation-turn-node-run-1-exchange-222'),
      );
      final turnLabel = find
          .descendant(
            of: turn,
            matching: find.byWidgetPredicate(
              (widget) =>
                  widget is Text &&
                  RegExp(r'^Turn \d+$').hasMatch(widget.data ?? ''),
            ),
          )
          .first;
      expect(
        (tester.getCenter(turnNode).dy - tester.getCenter(turnLabel).dy).abs(),
        lessThan(4),
      );
      final requestPreview = find.byKey(
        const Key('conversation-turn-preview-run-1-exchange-222'),
      );
      expect(requestPreview, findsOneWidget);
      expect(
        tester.widget<Text>(requestPreview).data,
        'Continue with the next verified implementation step.',
      );
      await tester.tap(turn);
      await tester.pumpAndSettle();
      final expandedDecoration =
          tester.widget<Container>(turnCard).decoration! as BoxDecoration;
      expect(expandedDecoration.borderRadius, ViberMetrics.surfaceRadius);
      expect(
        find.text('Continue with the next verified implementation step.'),
        findsNWidgets(2),
      );
      expect(
        find.text(
          'The runtime evidence is consistent; continue with the next bounded change.',
        ),
        findsOneWidget,
      );
      final thinkingBlock = find.byKey(
        const Key('thinking-block-response-run-1-exchange-222-0'),
      );
      expect(thinkingBlock, findsOneWidget);
      expect(
        find.byKey(const Key('thinking-block-response-run-1-exchange-222-1')),
        findsNothing,
      );
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.text('Reasoning evidence'),
        ),
        findsOneWidget,
      );
      expect(find.text('Reasoning summary'), findsNothing);
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.text('Plaintext evidence'),
        ),
        findsOneWidget,
      );
      expect(find.text('Opaque Thinking evidence'), findsNothing);
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.textContaining('Inspect the evidence boundary.'),
        ),
        findsOneWidget,
      );
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.byType(ExpansionTile),
        ),
        findsNothing,
      );
      final thinkingDisclosure = find.byKey(
        const Key('toggle-thinking-block-response-run-1-exchange-222-0'),
      );
      expect(thinkingDisclosure, findsOneWidget);
      await tester.ensureVisible(thinkingDisclosure);
      await tester.pumpAndSettle();
      await tester.tap(thinkingDisclosure);
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.textContaining('Inspect the evidence boundary.'),
        ),
        findsNothing,
      );
      await tester.tap(thinkingDisclosure);
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.textContaining('Inspect the evidence boundary.'),
        ),
        findsOneWidget,
      );
      final thinkingToggle = find.byKey(
        const Key('toggle-thinking-response-run-1-exchange-222-0'),
      );
      expect(thinkingToggle, findsOneWidget);
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.text('Show full Thinking'),
        ),
        findsOneWidget,
      );
      await tester.ensureVisible(thinkingToggle);
      await tester.pumpAndSettle();
      await tester.tap(thinkingToggle);
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: thinkingBlock,
          matching: find.text('Show first 15 lines'),
        ),
        findsOneWidget,
      );
      final copyMessage = find.byKey(
        const Key('copy-message-run-1-exchange-222-0'),
      );
      await Scrollable.ensureVisible(
        tester.element(copyMessage),
        alignment: 0.2,
      );
      await tester.pumpAndSettle();
      await tester.tap(copyMessage);
      await tester.pump();
      expect(
        copiedEvidence,
        'Continue with the next verified implementation step.',
      );
      final copyResponse = find.byKey(
        const Key('copy-response-run-1-exchange-222'),
      );
      await tester.ensureVisible(copyResponse);
      await tester.tap(copyResponse);
      await tester.pump();
      expect(copiedEvidence, contains('The runtime evidence is consistent'));
      final clientEvidence = find.byKey(
        const Key('exchange-client-evidence-run-1-exchange-222'),
      );
      expect(clientEvidence, findsOneWidget);
      expect(
        find.descendant(of: clientEvidence, matching: find.text('Claude Code')),
        findsOneWidget,
      );
      expect(
        find.descendant(
          of: clientEvidence,
          matching: find.textContaining('session claude-s…un-1'),
        ),
        findsOneWidget,
      );
      expect(find.text('request-run-1-222'), findsNothing);
      await tester.ensureVisible(clientEvidence);
      await tester.pumpAndSettle();
      await tester.tap(clientEvidence);
      await tester.pumpAndSettle();
      expect(find.text('Resumable session'), findsOneWidget);
      expect(find.text('Resume command'), findsOneWidget);
      expect(
        find.text("claude --resume 'claude-session-run-1'"),
        findsOneWidget,
      );
      expect(find.text('Session ID'), findsOneWidget);
      expect(find.text('claude-session-run-1'), findsOneWidget);
      expect(find.text('Request ID'), findsOneWidget);
      expect(find.text('request-run-1-222'), findsOneWidget);
      expect(find.text('Skill'), findsOneWidget);
      expect(find.text('code-review'), findsOneWidget);
      await tester.tap(clientEvidence);
      await tester.pumpAndSettle();
      expect(find.text('Resumable session'), findsNothing);
      final turnSummary = find.byKey(
        const Key('conversation-turn-summary-run-1-exchange-222'),
      );
      expect(turnSummary, findsOneWidget);
      expect(
        (tester.widget<Text>(turnSummary).data ?? ''),
        contains('input 1240'),
      );
      // The top-level instruction parameter is per-request configuration, so it
      // is shown as its own section and is not counted as a conversation turn.
      final systemSection = find.byKey(
        const Key('exchange-system-run-1-exchange-222'),
      );
      expect(systemSection, findsOneWidget);
      await tester.ensureVisible(systemSection);
      // It is collapsed by default because instructions are long, and expanding
      // it reveals the recorded text unchanged.
      expect(find.textContaining('You are an interactive agent'), findsNothing);
      await tester.tap(systemSection);
      await tester.pumpAndSettle();
      expect(
        find.textContaining('You are an interactive agent'),
        findsOneWidget,
      );
      expect(find.text('Frozen routing and attempt evidence'), findsOneWidget);
      final evidenceTitle = find.text('Frozen routing and attempt evidence');
      final evidenceSummary = find.byKey(
        const Key('exchange-evidence-summary-run-1-exchange-222'),
      );
      expect(
        (tester.getCenter(evidenceTitle).dy -
                tester.getCenter(evidenceSummary).dy)
            .abs(),
        lessThan(3),
      );
      expect(
        tester.getBottomRight(turnCard).dy -
            tester.getBottomRight(evidenceSummary).dy,
        lessThan(46),
      );
      expect(find.text('Raw HTTP'), findsOneWidget);
      expect(find.text('Bearer ••••••••'), findsNothing);
      final rawEvidence = find.byKey(
        const Key('exchange-raw-run-1-exchange-222'),
      );
      await Scrollable.ensureVisible(
        tester.element(rawEvidence),
        alignment: 0.5,
      );
      await tester.pumpAndSettle();
      await tester.tap(rawEvidence);
      await tester.pumpAndSettle();
      expect(find.text('3 boundary messages'), findsOneWidget);
      final rawReveal = find.byKey(
        const Key('raw-reveal-raw-preview-run-1-exchange-222'),
      );
      await Scrollable.ensureVisible(tester.element(rawReveal), alignment: 0.5);
      await tester.pumpAndSettle();
      await tester.tap(rawReveal);
      await tester.pumpAndSettle();
      final rawPayload = find.byKey(
        const Key('raw-revealed-raw-preview-run-1-exchange-222'),
      );
      expect(rawPayload, findsOneWidget);
      expect(
        find.descendant(
          of: rawPayload,
          matching: find.textContaining('Authorization: [redacted 108B'),
        ),
        findsOneWidget,
      );
      expect(
        find.text('{"model":"claude-sonnet-4-5","stream":true}'),
        findsOneWidget,
      );
      final copyRaw = find.byKey(
        const Key('copy-raw-raw-preview-run-1-exchange-222'),
      );
      await tester.ensureVisible(copyRaw);
      await tester.tap(copyRaw);
      await tester.pump();
      expect(copiedEvidence, contains('Authorization: [redacted 108B'));
      expect(copiedEvidence, isNot(contains('Bearer')));
      expect(
        copiedEvidence,
        contains('{"model":"claude-sonnet-4-5","stream":true}'),
      );
      expect(
        find.ancestor(
          of: find.text('Continue with the next verified implementation step.'),
          matching: find.byType(SingleChildScrollView),
        ),
        findsNothing,
      );

      // The ordinal is incidental — it encoded how many Activities one page
      // happened to load. The property is that an earlier Turn in the map can
      // be selected, so take the first marker the map offers.
      final mapTurns = find
          .byWidgetPredicate(
            (widget) =>
                widget.key is ValueKey<String> &&
                (widget.key! as ValueKey<String>).value.startsWith(
                  'conversation-map-turn-',
                ),
          )
          .hitTestable();
      // A marker from the middle of the map: the ones at the edges can be
      // clipped, so a tap lands outside their interactive area.
      final previousTurn = mapTurns.at(mapTurns.evaluate().length ~/ 2);
      expect(previousTurn, findsOneWidget);
      final marker = find.descendant(
        of: previousTurn,
        matching: find.byType(AnimatedContainer),
      );
      // The property is that selecting a Turn in the map marks it, not that the
      // mark is a particular number of pixels on a particular ordinal: both of
      // those encoded which page of Activities happened to be loaded.
      final restingWidth = tester.getSize(marker).width;
      await tester.tap(previousTurn);
      await tester.pumpAndSettle();
      expect(tester.getSize(marker).width, greaterThan(restingWidth));

      final timelineScrollable = find
          .descendant(
            of: find.byKey(const Key('conversation-timeline-scroll')),
            matching: find.byType(Scrollable),
          )
          .first;
      final timelineState = tester.state<ScrollableState>(timelineScrollable);
      timelineState.position.jumpTo(0);
      await tester.pumpAndSettle();
      final scrollLatest = find.byKey(const Key('conversation-scroll-latest'));
      expect(scrollLatest, findsOneWidget);
      expect(
        find.descendant(
          of: scrollLatest,
          matching: find.text('Scroll to latest'),
        ),
        findsOneWidget,
      );
      timelineState.position.jumpTo(timelineState.position.maxScrollExtent);
      await tester.pumpAndSettle();
      // Scrolling to the end must mark the last Turn in the map. Which ordinal
      // that is depends on the page of Activities loaded, so it is derived
      // rather than pinned.
      final latestKey = tester.allWidgets
          .map((widget) => widget.key)
          .whereType<ValueKey<String>>()
          .map((key) => key.value)
          .where((value) => value.startsWith('conversation-map-turn-'))
          .reduce(
            (left, right) =>
                int.parse(left.split('-').last) >=
                    int.parse(right.split('-').last)
                ? left
                : right,
          );
      final latestSelectedMarker = find.descendant(
        of: find.byKey(ValueKey(latestKey)),
        matching: find.byType(AnimatedContainer),
      );
      expect(tester.getSize(latestSelectedMarker).width, 22);

      final fullSnapshot = find.byKey(
        const Key('exchange-full-run-1-exchange-222'),
      );
      await tester.ensureVisible(fullSnapshot);
      await tester.tap(fullSnapshot);
      await tester.pumpAndSettle();
      expect(find.text('System context'), findsOneWidget);
      expect(find.textContaining('You are operating inside'), findsNothing);
      expect(find.text('Inspect the current workspace.'), findsOneWidget);
      final systemContext = find.byKey(
        const Key('system-context-run-1-exchange-222-0'),
      );
      await tester.ensureVisible(systemContext);
      await tester.tap(systemContext);
      await tester.pumpAndSettle();
      expect(find.textContaining('You are operating inside'), findsOneWidget);
      final wrappingCheck = find.textContaining('WRAPPING-CHECK');
      expect(wrappingCheck, findsOneWidget);
      final followingParagraph = find.textContaining('hello');
      expect(followingParagraph, findsOneWidget);
      final codeBlock = find.byKey(const Key('markdown-code-block'));
      final precedingParagraph = find.text('Inspect the current workspace.');
      final beforeGap =
          tester.getTopLeft(codeBlock).dy -
          tester.getBottomLeft(precedingParagraph).dy;
      final afterGap =
          tester.getTopLeft(followingParagraph).dy -
          tester.getBottomLeft(codeBlock).dy;
      expect(afterGap, greaterThanOrEqualTo(10));
      expect(afterGap, greaterThan(beforeGap + 2));
      expect(
        find.ancestor(
          of: wrappingCheck,
          matching: find.byWidgetPredicate(
            (widget) =>
                widget is SingleChildScrollView &&
                widget.scrollDirection == Axis.horizontal,
          ),
        ),
        findsNothing,
      );
      final multiBlockMessage = find.byKey(
        const ValueKey('message-content-run-1-exchange-222-1'),
      );
      final multiBlockToggles = find.descendant(
        of: multiBlockMessage,
        matching: find.byWidgetPredicate(
          (widget) =>
              widget.key is ValueKey<String> &&
              (widget.key! as ValueKey<String>).value.startsWith(
                'toggle-long-message-run-1-exchange-222-1',
              ),
        ),
      );
      expect(multiBlockToggles, findsOneWidget);
      // "Go to Capture" was the flat Conversations list's bridge back to the
      // owning Capture. Reaching a Conversation now means already being there,
      // so the affordance retired with the section.
      expect(
        find.byKey(const Key('capture-environment-scope')),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('failed Turn explains the failing boundary and next action', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');
    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-219'));
    await ensureTurnVisible(tester, turn);
    await tester.tap(turn);
    await tester.pumpAndSettle();

    expect(
      find.text('Upstream service did not respond in time.'),
      findsOneWidget,
    );
    expect(
      find.text(
        'Check the upstream service and network path, then retry the Agent request.',
      ),
      findsOneWidget,
    );
    expect(
      find.textContaining('provider_response_idle · 504 · upstream'),
      findsOneWidget,
    );
  });

  testWidgets('390px Chinese failed Turn stays actionable', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');
    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-219'));
    await ensureTurnVisible(tester, turn);
    await tester.tap(turn);
    await tester.pumpAndSettle();

    expect(find.text('上游服务未能及时响应。'), findsOneWidget);
    expect(find.text('检查上游服务与网络路径，然后重试 Agent 请求。'), findsOneWidget);
    expect(find.textContaining('provider_response_idle'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('malformed Agent exchange reports zero upstream attempts', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');
    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-218'));
    await ensureTurnVisible(tester, turn);
    await tester.tap(turn);
    await tester.pumpAndSettle();

    expect(
      find.text(
        'ViberMate rejected the Agent request before contacting an upstream service.',
      ),
      findsOneWidget,
    );
    expect(
      find.text(
        'Check the listed request field and client protocol, then retry.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('unsupported_client_input'), findsOneWidget);
    expect(
      tester
          .widget<Text>(
            find.byKey(
              const Key('exchange-evidence-summary-run-1-exchange-218'),
            ),
          )
          .data,
      contains('0 attempts'),
    );
  });

  testWidgets(
    'checkpoint evidence does not offer a nonexistent full snapshot',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      addTearDown(api.close);
      final page = await api.activities(captureRunId: 'run-1', limit: 224);
      final checkpoint = page.items.singleWhere(
        (activity) => activity.id == 'run-1-exchange-1',
      );
      expect(await controller.loadExchangeDetail(checkpoint.id), isNotNull);

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: EvidenceConversationTimeline(
              controller: controller,
              activities: [checkpoint],
              copy: AppCopy.forLanguage(AppLanguage.english),
            ),
          ),
        ),
      );
      for (var frame = 0; frame < 20; frame += 1) {
        await tester.pump(const Duration(milliseconds: 20));
      }

      expect(
        find.byKey(const Key('exchange-evidence-summary-run-1-exchange-1')),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('exchange-full-run-1-exchange-1')),
        findsNothing,
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('Account Selector sample failure preserves safe diagnosis', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: AccountSelectorEditorDialog(
          selectorId: 'selector.test',
          initial: const AccountSelectorPolicy(
            javaScript: 'selection.accountId = accounts[0].id;',
          ),
          copy: AppCopy.forLanguage(AppLanguage.english),
          testSelector: ({required policy, required sample}) async =>
              throw const ControlProblem(
                status: 422,
                reasonCode: 'account_selector_test_failed',
                messageKey: 'error.account_selector_test_failed',
                detail: 'compile JavaScript: SyntaxError at line 1',
              ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(
      find.byKey(const Key('account-selector-test-selector.test')),
    );
    await tester.pumpAndSettle();

    expect(
      find.textContaining(
        'Fix the JavaScript or sample values, then run it again.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('SyntaxError at line 1'), findsOneWidget);
    expect(find.textContaining('Control problem 422'), findsNothing);
  });

  testWidgets('390px Chinese Account Selector failure stays actionable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: AccountSelectorEditorDialog(
          selectorId: 'selector.test',
          initial: const AccountSelectorPolicy(
            javaScript: 'selection.accountId = accounts[0].id;',
          ),
          copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
          testSelector: ({required policy, required sample}) async =>
              throw const ControlProblem(
                status: 422,
                reasonCode: 'account_selector_test_failed',
                messageKey: 'error.account_selector_test_failed',
                detail: 'compile JavaScript: SyntaxError at line 1',
              ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(
      find.byKey(const Key('account-selector-test-selector.test')),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('请修正 JavaScript 或样例值'), findsOneWidget);
    expect(find.textContaining('SyntaxError at line 1'), findsOneWidget);
    expect(find.textContaining('Control problem 422'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'Raw Transform evidence copies one complete attempt to Code Library',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await openCaptureConversation(tester, capture: 'managed_run:run-1');
      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await ensureTurnVisible(tester, turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();
      final raw = find.byKey(const Key('exchange-raw-run-1-exchange-222'));
      await Scrollable.ensureVisible(tester.element(raw), alignment: 0.5);
      await tester.pumpAndSettle();
      await tester.tap(raw);
      await tester.pumpAndSettle();

      final copySample = find.byKey(
        const Key('copy-transform-sample-run-1-exchange-222'),
      );
      await tester.ensureVisible(copySample);
      await tester.tap(copySample);
      await tester.pumpAndSettle();

      expect(find.text('Script library'), findsWidgets);
      expect(
        find.byKey(const Key('code-library-captured-sample')),
        findsOneWidget,
      );
      expect(find.textContaining('run-1-exchange-222'), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'Raw evidence exports a redacted diagnostic without retained content',
    (tester) async {
      String? copiedDiagnostic;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedDiagnostic =
                (call.arguments as Map<Object?, Object?>)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await openCaptureConversation(tester, capture: 'managed_run:run-1');
      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await ensureTurnVisible(tester, turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();
      final raw = find.byKey(const Key('exchange-raw-run-1-exchange-222'));
      await Scrollable.ensureVisible(tester.element(raw), alignment: 0.5);
      await tester.pumpAndSettle();
      await tester.tap(raw);
      await tester.pumpAndSettle();

      final export = find.byKey(
        const Key('copy-redacted-diagnostic-run-1-exchange-222'),
      );
      await tester.ensureVisible(export);
      await tester.tap(export);
      await tester.pump();

      final report = jsonDecode(copiedDiagnostic!) as Map<String, Object?>;
      expect(report['schema'], 'vibermate.redacted-diagnostic/v1');
      expect(report['exchange'], isA<Map<String, Object?>>());
      expect(report['rawEvidence'], isA<Map<String, Object?>>());
      expect(copiedDiagnostic, contains('bodySha256'));
      expect(copiedDiagnostic, isNot(contains('Authorization')));
      expect(copiedDiagnostic, isNot(contains('Bearer')));
      expect(
        copiedDiagnostic,
        isNot(contains('{"model":"claude-sonnet-4-5","stream":true}')),
      );
      expect(copiedDiagnostic, isNot(contains('/Users/')));
      expect(copiedDiagnostic, isNot(contains('rawQuery')));
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Codex resume keeps Turns in one Conversation and switches exact Sessions',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1600, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      final capture = find.byKey(const Key('capture-row-managed_run:run-2'));
      await tester.ensureVisible(capture);
      await tester.tap(capture);
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('capture-session-selector')), findsOneWidget);
      expect(find.text('View Session'), findsOneWidget);
      expect(
        find.text('Filters evidence only; does not switch the client session.'),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('capture-conversation-pane')),
        findsOneWidget,
      );
      expect(find.textContaining('12 Turns'), findsWidgets);
      final visibleConversations = find.byWidgetPredicate(
        (widget) =>
            widget.key is ValueKey<String> &&
            (widget.key! as ValueKey<String>).value.startsWith(
              'capture-conversation-client_session:codex:',
            ),
      );
      expect(visibleConversations, findsOneWidget);

      final sessionField = find.byWidgetPredicate(
        (widget) =>
            widget.key is ValueKey<String> &&
            (widget.key! as ValueKey<String>).value.startsWith(
              'capture-session-select:',
            ),
      );
      expect(sessionField, findsOneWidget);
      await tester.tap(sessionField);
      await tester.pumpAndSettle();
      final primarySession = find.textContaining('primary');
      final resumedSession = find.textContaining('resumed');
      expect(primarySession, findsOneWidget);
      expect(resumedSession, findsWidgets);
      await tester.tap(primarySession);
      await tester.pumpAndSettle();
      expect(
        find.byKey(
          const ValueKey(
            'capture-session-select:codex:codex-session-run-2-primary',
          ),
        ),
        findsOneWidget,
      );
      expect(visibleConversations, findsOneWidget);
      expect(find.textContaining('12 Turns'), findsWidgets);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('expanding the latest Turn follows its measured tail', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');

    final latest = find.byKey(
      const Key('conversation-turn-run-1-exchange-224'),
    );
    await tester.ensureVisible(latest);
    await tester.tap(latest);
    await tester.pumpAndSettle();

    final timelineScrollable = find
        .descendant(
          of: find.byKey(const Key('conversation-timeline-scroll')),
          matching: find.byType(Scrollable),
        )
        .first;
    final timelineState = tester.state<ScrollableState>(timelineScrollable);
    timelineState.position.jumpTo(timelineState.position.maxScrollExtent);
    await tester.pumpAndSettle();
    await tester.tap(latest);
    await tester.pumpAndSettle();

    expect(
      timelineState.position.maxScrollExtent - timelineState.position.pixels,
      lessThan(4),
    );
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('long captured content defaults to 15 lines and can expand', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');

    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-220'));
    await ensureTurnVisible(tester, turn);
    await tester.tap(turn);
    await tester.pumpAndSettle();

    // Each collapsible region owns its trigger key, so the selector names the
    // block it expands rather than every long block in the timeline. The
    // assertion is still that exactly one block in this Turn is collapsible.
    final toggle = find.byWidgetPredicate(
      (widget) =>
          widget.key is ValueKey<String> &&
          (widget.key! as ValueKey<String>).value.startsWith('toggle-long-'),
    );
    expect(toggle, findsOneWidget);
    expect(find.text('Show all content'), findsOneWidget);
    await tester.ensureVisible(toggle);
    await tester.pumpAndSettle();
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(find.text('Show first 15 lines'), findsOneWidget);
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(find.text('Show all content'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'named Agent conversations are flat and never mix with the main stream',
    (tester) async {
      // Wide enough for the Conversation directory to be a row list: this test
      // is about Agent boundaries, not about the narrow layout.
      await tester.binding.setSurfaceSize(const Size(1600, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await openCaptureConversation(
        tester,
        capture: 'managed_run:run-1',
        conversationLabel: 'reviewer',
      );

      expect(find.text('reviewer'), findsWidgets);
      expect(find.textContaining('16 Turns'), findsWidgets);
      expect(
        find.byKey(const Key('conversation-subconversations')),
        findsNothing,
      );
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Turn evidence opens the exact frozen Environment as read-only authority',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await openCaptureConversation(tester, capture: 'managed_run:run-1');
      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await ensureTurnVisible(tester, turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();

      final inspect = find.byKey(
        const Key('exchange-environment-run-1-exchange-222'),
      );
      await tester.ensureVisible(inspect);
      expect(find.text('Inspect r7'), findsNothing);
      expect(
        tester.widget<IconButton>(inspect).tooltip,
        'Inspect frozen traffic policy r7',
      );
      await tester.tap(inspect);
      await tester.pumpAndSettle();

      expect(
        find.byKey(const Key('environment-history-banner')),
        findsOneWidget,
      );
      expect(find.text('Revision 7'), findsOneWidget);
      expect(find.text('Frozen evidence'), findsOneWidget);
      expect(find.byKey(const Key('environment-edit')), findsNothing);
      expect(
        find.byKey(const Key('environment-history-current')),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese Conversation timeline expands without overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await openCaptureConversation(tester, capture: 'managed_run:run-1');
    expect(find.textContaining('vibermate run'), findsOneWidget);
    expect(
      find.byWidgetPredicate(
        (widget) =>
            widget.key is ValueKey<String> &&
            (widget.key! as ValueKey<String>).value.startsWith(
              'conversation-map-turn-',
            ),
      ),
      findsNothing,
    );
    expect(
      find.byKey(const Key('conversation-compact-position')),
      findsOneWidget,
    );

    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-224'));
    await tester.ensureVisible(turn);
    await tester.pumpAndSettle();
    expect(find.text('正在等待最终响应…'), findsOneWidget);

    final rawTurn = find.byKey(
      const Key('conversation-turn-run-1-exchange-222'),
    );
    await ensureTurnVisible(tester, rawTurn);
    await tester.tap(rawTurn);
    await tester.pumpAndSettle();
    final thinkingBlock = find.byKey(
      const Key('thinking-block-response-run-1-exchange-222-0'),
    );
    expect(thinkingBlock, findsOneWidget);
    expect(
      find.descendant(of: thinkingBlock, matching: find.text('推理证据')),
      findsOneWidget,
    );
    expect(find.text('推理摘要'), findsNothing);
    expect(
      find.descendant(of: thinkingBlock, matching: find.text('明文证据')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: thinkingBlock, matching: find.text('展开完整 Thinking')),
      findsOneWidget,
    );
    expect(tester.getTopLeft(thinkingBlock).dx, greaterThanOrEqualTo(0));
    expect(tester.getBottomRight(thinkingBlock).dx, lessThanOrEqualTo(390));
    final rawSection = find.byKey(const Key('exchange-raw-run-1-exchange-222'));
    // ensureVisible instead of a fixed scroll offset: the claim is that the Raw
    // section is reachable and lands on screen at 390 px, not that it sits at
    // one particular pixel, and adding a section above it must not break that.
    await tester.ensureVisible(rawSection);
    await tester.pumpAndSettle();
    expect(tester.getCenter(rawSection).dy, lessThan(740));
    await tester.tap(rawSection);
    await tester.pumpAndSettle();
    expect(find.text('3 条边界消息'), findsOneWidget);
    final rawReveal = find.byKey(
      const Key('raw-reveal-raw-preview-run-1-exchange-222'),
    );
    await tester.ensureVisible(rawReveal);
    await tester.pumpAndSettle();
    await tester.tap(rawReveal);
    await tester.pumpAndSettle();
    final revealed = find.byKey(
      const Key('raw-revealed-raw-preview-run-1-exchange-222'),
    );
    await tester.ensureVisible(revealed);
    expect(revealed, findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px manual capture revoke is confirmed and preserves evidence', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.text('RUNNING NOW'), findsOneWidget);
    expect(
      tester.takeException(),
      isNull,
      reason: 'the compact Capture directory must fit',
    );
    await tester.tap(find.text('Figma Desktop').first);
    await tester.pumpAndSettle();
    expect(find.text('Revoke proxy login'), findsOneWidget);
    expect(find.text('Independent exchanges'), findsOneWidget);
    expect(
      tester.takeException(),
      isNull,
      reason: 'the compact Capture header must fit before confirmation',
    );

    await tester.tap(find.text('Revoke proxy login'));
    await tester.pumpAndSettle();
    expect(find.text('Stop this proxy login?'), findsOneWidget);
    expect(
      find.text(
        'New connections using this login will stop. Conversation and Activity evidence remain available.',
      ),
      findsOneWidget,
    );
    expect(
      tester.takeException(),
      isNull,
      reason: 'the compact revoke confirmation must fit',
    );

    await tester.tap(find.text('Revoke login'));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Proxy login revoked. Conversation and Activity evidence were retained.',
      ),
      findsOneWidget,
    );
    expect(find.text('Independent exchanges'), findsOneWidget);
    expect(find.text('Revoke proxy login'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'manual capture creation reviews Environment authority and delivers the password once',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('manual-capture-create')));
      await tester.pumpAndSettle();
      expect(find.text('Create a dedicated proxy login'), findsOneWidget);
      expect(
        find.text(
          "ViberMate decrypts and records the listed Agent API origins. Original Destination keeps the client's URL, authentication, and model unchanged.",
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('manual-capture-environment')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Work').last);
      await tester.pumpAndSettle();
      expect(
        find.text(
          "ViberMate decrypts and records the listed Agent API origins. Original Destination keeps the client's URL, authentication, and model unchanged.",
        ),
        findsOneWidget,
      );
      expect(
        find.text('api.anthropic.com:443  ·  api.openai.com:443'),
        findsOneWidget,
      );

      await tester.enterText(
        find.byKey(const Key('manual-capture-name')),
        'Browser Lab',
      );
      await tester.tap(find.byKey(const Key('manual-capture-create-confirm')));
      await tester.pumpAndSettle();

      expect(find.text('Proxy login created'), findsOneWidget);
      expect(find.text('preview-password-1'), findsOneWidget);
      expect(
        find.text('/Users/mira/Library/Application Support/ViberMate/root.pem'),
        findsOneWidget,
      );
      expect(
        find.text(
          'Copy these values now. The password is shown once and cannot be recovered later.',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
      await tester.pumpAndSettle();
      expect(find.text('Browser Lab'), findsWidgets);
      expect(
        find.text('Manual Capture created with a dedicated proxy login.'),
        findsOneWidget,
      );
      expect(find.text('preview-password-1'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'manual capture credential rotation requires confirmation and retains evidence',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Figma Desktop').first);
      await tester.pumpAndSettle();
      expect(find.text('Independent exchanges'), findsOneWidget);
      await tester.tap(find.byKey(const Key('manual-capture-rotate')));
      await tester.pumpAndSettle();
      expect(find.text('Rotate this proxy credential?'), findsOneWidget);
      expect(find.text('preview-password-3'), findsNothing);

      await tester.tap(find.byKey(const Key('manual-capture-rotate-confirm')));
      await tester.pumpAndSettle();
      expect(find.text('Proxy credential rotated'), findsOneWidget);
      expect(find.text('preview-password-3'), findsOneWidget);

      await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Proxy credential rotated. Conversation and Activity evidence were retained.',
        ),
        findsOneWidget,
      );
      expect(find.text('Independent exchanges'), findsOneWidget);
      expect(find.text('preview-password-3'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese manual capture creation has no overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('manual-capture-create')));
    await tester.pumpAndSettle();
    expect(find.text('创建专属代理登录'), findsOneWidget);
    expect(
      find.text('ViberMate 会解密并记录下列 Agent API 入口；“原始目的地”保持客户端 URL、鉴权和模型不变。'),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    await tester.enterText(
      find.byKey(const Key('manual-capture-name')),
      '窄屏客户端',
    );
    await tester.tap(find.byKey(const Key('manual-capture-create-confirm')));
    await tester.pumpAndSettle();
    expect(find.text('代理登录已创建'), findsOneWidget);
    expect(find.text('preview-password-1'), findsOneWidget);
    expect(find.text('Root 路径'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
    await tester.pumpAndSettle();
    expect(find.text('窄屏客户端'), findsWidgets);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Simplified Chinese directory stays usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    expect(find.text('正在运行'.toUpperCase()), findsOneWidget);
    expect(find.text('历史记录'.toUpperCase()), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'network approval requires confirmation before the real decision',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      expect(find.text('1 pending'), findsOneWidget);
      await tester.tap(find.byKey(const Key('approval-attention')));
      await tester.pumpAndSettle();

      expect(find.text('Network access confirmation required'), findsOneWidget);
      await tester.tap(
        find.byKey(
          const Key('approval-approval-network-github-allow-once-request'),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('approval-confirmation')), findsOneWidget);
      expect(find.text('Network access confirmation required'), findsOneWidget);
      await tester.tap(find.byKey(const Key('approval-confirm-action')));
      await tester.pumpAndSettle();

      expect(
        find.text('Decision applied. Waiting work was allowed to continue.'),
        findsOneWidget,
      );
      expect(find.text('Nothing is waiting'), findsOneWidget);
      expect(find.byKey(const Key('approval-attention')), findsNothing);
      expect(find.text('Network access confirmation required'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    '390px Chinese approval keeps temporary decisions visible and permanent rules secondary',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: true),
      );
      await tester.pumpAndSettle();
      await _openNetwork(tester);

      expect(find.text('本次允许该连接'), findsOneWidget);
      expect(find.text('本次拒绝该连接'), findsOneWidget);
      expect(find.text('永久规则…'), findsOneWidget);
      expect(find.text('始终允许该主机和端口'), findsNothing);
      await tester.tap(
        find.byKey(
          const Key('approval-approval-network-github-permanent-menu'),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.text('始终允许该主机和端口'), findsOneWidget);
      expect(find.text('始终拒绝该主机和端口'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('network evidence uses real cursor pagination', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openNetwork(tester);

    await tester.tap(find.byKey(const Key('network-tab-connections')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-connections')),
        matching: find.text('10'),
      ),
      findsOneWidget,
    );
    await tester.ensureVisible(find.byKey(const Key('connections-load-more')));
    await tester.tap(find.byKey(const Key('connections-load-more')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-connections')),
        matching: find.text('18'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('connections-load-more')), findsNothing);

    await tester.ensureVisible(find.byKey(const Key('network-tab-egress')));
    await tester.tap(find.byKey(const Key('network-tab-egress')));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const Key('egress-load-more')));
    await tester.tap(find.byKey(const Key('egress-load-more')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-egress')),
        matching: find.text('22'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('egress-load-more')), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('expanded network evidence uses a fixed-column evidence table', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openNetwork(tester);

    await tester.tap(find.byKey(const Key('network-tab-egress')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-1')));
    await tester.pumpAndSettle();

    final details = find.byKey(const Key('egress-evidence-table-egress-1'));
    expect(details, findsOneWidget);
    final tableFinder = find.descendant(
      of: details,
      matching: find.byKey(const Key('evidence-table-2-groups')),
    );
    expect(tableFinder, findsOneWidget);
    final table = tester.widget<Table>(tableFinder);
    expect(table.children, isNotEmpty);
    expect(table.children.every((row) => row.children.length == 4), isTrue);
    expect(
      find.descendant(of: details, matching: find.text('Attempt ID')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: details, matching: find.text('ATTEMPT ID')),
      findsNothing,
    );
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'network decisions stay neutral and loaded evidence can be filtered',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      await _openNetwork(tester);

      final allow = find.byKey(
        const Key('approval-approval-network-github-allow-once-request'),
      );
      final deny = find.byKey(
        const Key('approval-approval-network-github-deny-request'),
      );
      expect(tester.widget(allow), isA<OutlinedButton>());
      expect(tester.widget(deny), isA<OutlinedButton>());
      expect(
        find.byKey(
          const Key('approval-approval-network-github-allow-once-host_port'),
        ),
        findsNothing,
      );
      expect(
        find.byKey(
          const Key('approval-approval-network-github-deny-host_port'),
        ),
        findsNothing,
      );
      await tester.tap(
        find.byKey(
          const Key('approval-approval-network-github-permanent-menu'),
        ),
      );
      await tester.pumpAndSettle();
      expect(
        find.byKey(
          const Key('approval-approval-network-github-allow-once-host_port'),
        ),
        findsOneWidget,
      );
      expect(
        find.byKey(
          const Key('approval-approval-network-github-deny-host_port'),
        ),
        findsOneWidget,
      );
      await tester.tapAt(const Offset(1100, 700));
      await tester.pumpAndSettle();
      expect(find.text('approval-network-github'), findsNothing);
      await tester.tap(
        find.byKey(const Key('approval-technical-approval-network-github')),
      );
      await tester.pumpAndSettle();
      expect(find.text('approval-network-github'), findsOneWidget);

      await tester.tap(find.byKey(const Key('network-tab-connections')));
      await tester.pumpAndSettle();
      final filter = find.descendant(
        of: find.byKey(const Key('connections-filter')),
        matching: find.byType(TextField),
      );
      await tester.enterText(filter, 'not-a-loaded-host.example');
      await tester.pumpAndSettle();
      expect(
        find.text('No loaded evidence matches this filter.'),
        findsOneWidget,
      );
      expect(
        find.text(
          'This filters loaded evidence only. Load more to search older records.',
        ),
        findsWidgets,
      );
      await tester.tap(
        find.descendant(
          of: find.byKey(const Key('connections-filter')),
          matching: find.byIcon(Icons.close),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('connections-load-more')), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('rule removal stays a draft until atomic save is confirmed', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openNetwork(tester);

    await tester.ensureVisible(find.byKey(const Key('network-tab-rules')));
    await tester.tap(find.byKey(const Key('network-tab-rules')));
    await tester.pumpAndSettle();
    expect(
      tester.getSize(find.byKey(const Key('rules-mode'))).height,
      ViberMetrics.controlHeight,
    );
    expect(find.text('Rule set revision 3  ·  2'), findsOneWidget);
    expect(find.text('allow-anthropic'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rule-remove-allow-anthropic')));
    await tester.pumpAndSettle();
    expect(find.text('allow-anthropic'), findsNothing);
    expect(find.text('Unsaved draft'), findsOneWidget);
    expect(find.text('Rule set revision 3  ·  1'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rules-save')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('rules-save-confirmation')), findsOneWidget);
    expect(find.text('Rule set revision 3  ·  1'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rules-save-confirm')));
    await tester.pumpAndSettle();
    expect(find.text('Connection rule set saved atomically.'), findsOneWidget);
    expect(find.text('Rule set revision 4  ·  1'), findsOneWidget);
    expect(find.text('Unsaved draft'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    '390px Chinese network workbench and rule editor do not overflow',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: true),
      );
      await tester.pumpAndSettle();
      await _openNetwork(tester);

      expect(find.text('连接'), findsWidgets);
      final approvalsTab = find.byKey(const Key('network-tab-approvals'));
      final approvalsLabel = find.descendant(
        of: approvalsTab,
        matching: find.text('审批'),
      );
      expect(
        (tester.getCenter(approvalsTab).dy -
                tester.getCenter(approvalsLabel).dy)
            .abs(),
        lessThan(1.5),
      );
      final egressTab = find.byKey(const Key('network-tab-egress'));
      await tester.ensureVisible(egressTab);
      await tester.tap(egressTab);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('egress-1')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('evidence-table-1-groups')), findsOneWidget);
      expect(tester.takeException(), isNull);

      final rulesTab = find.byKey(const Key('network-tab-rules'));
      final tabStrip = find.ancestor(
        of: rulesTab,
        matching: find.byType(ListView),
      );
      await tester.drag(tabStrip, const Offset(-120, 0));
      await tester.pumpAndSettle();
      await tester.tap(rulesTab);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('rules-mode')), findsOneWidget);

      await tester.tap(find.byKey(const Key('rules-add')));
      await tester.pumpAndSettle();
      expect(find.text('添加规则'), findsWidgets);
      expect(find.byKey(const Key('rule-editor-id')), findsOneWidget);
      final ruleField = tester.widget<TextField>(
        find.descendant(
          of: find.byKey(const Key('rule-editor-id')),
          matching: find.byType(TextField),
        ),
      );
      expect(ruleField.textAlignVertical, TextAlignVertical.center);
      final ruleEditable = tester.widget<EditableText>(
        find.descendant(
          of: find.byKey(const Key('rule-editor-id')),
          matching: find.byType(EditableText),
        ),
      );
      expect(ruleEditable.style.fontSize, ViberType.control);
      for (final key in const [
        Key('rule-editor-id'),
        Key('rule-editor-priority'),
        Key('rule-editor-decision'),
        Key('rule-editor-match'),
        Key('rule-editor-host'),
        Key('rule-editor-port'),
      ]) {
        expect(
          tester.getSize(find.byKey(key)).height,
          ViberMetrics.controlHeight,
          reason: '$key must share the desktop control height',
        );
      }
      expect(tester.takeException(), isNull);

      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('rule-editor-id')), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Environment edit reviews impact and publishes only Endpoint-owned Account authority',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-edit')), findsNothing);
      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('environment-editor-page')), findsOneWidget);
      expect(find.byType(AlertDialog), findsNothing);
      expect(
        tester.getSize(find.byKey(const Key('environment-editor-frame'))).width,
        greaterThan(ViberMetrics.dialogWideWidth),
      );

      final nameField = find.byKey(const Key('environment-editor-name'));
      final stateField = find.byKey(const Key('environment-editor-state'));
      final toolField = find.byKey(const Key('environment-editor-tool-mode'));
      final recordingField = find.byKey(
        const Key('environment-editor-recording'),
      );
      final retentionField = find.byKey(
        const Key('environment-editor-retention'),
      );
      for (final field in [nameField, stateField]) {
        expect(
          tester.getSize(field).height,
          ViberMetrics.controlHeight,
          reason: 'adjacent Environment controls share one height',
        );
      }
      expect(
        (tester.getTopLeft(nameField).dy - tester.getTopLeft(stateField).dy)
            .abs(),
        lessThan(1),
      );
      await tester.tap(find.byKey(const Key('environment-tab-runtime')));
      await tester.pumpAndSettle();
      for (final field in [toolField, recordingField, retentionField]) {
        expect(tester.getSize(field).height, ViberMetrics.controlHeight);
      }
      expect(
        (tester.getTopLeft(toolField).dy - tester.getTopLeft(recordingField).dy)
            .abs(),
        lessThan(1),
      );
      expect(
        (tester.getTopLeft(recordingField).dy -
                tester.getTopLeft(retentionField).dy)
            .abs(),
        lessThan(1),
      );
      await tester.tap(find.byKey(const Key('environment-tab-traffic')));
      await tester.pumpAndSettle();

      final accountField = find.byKey(
        const Key(
          'environment-route-account-anthropic-direct-fixed:anthropic-work',
        ),
      );
      final accountDropdown = tester.widget<CompactSelectField<String>>(
        accountField,
      );
      final accountIds = accountDropdown.items
          .map((item) => item.value)
          .toList(growable: false);
      expect(
        accountIds,
        containsAll(['fixed:anthropic-work', 'fixed:anthropic-lab']),
      );
      expect(accountIds, isNot(contains('fixed:orbit-team')));
      expect(accountIds, isNot(contains('fixed:openai-work')));
      final accountMenu = tester.widget<MenuAnchor>(
        find.descendant(of: accountField, matching: find.byType(MenuAnchor)),
      );
      expect(
        accountMenu.style?.maximumSize?.resolve({})?.height,
        lessThanOrEqualTo(240),
      );

      final modelField = find.byKey(
        const Key('environment-route-model-anthropic-direct'),
      );
      expect(modelField, findsOneWidget);
      await tester.ensureVisible(modelField);
      await tester.pumpAndSettle();
      await tester.tap(modelField);
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-model-selector')),
        findsOneWidget,
      );
      final modelDialog = find.byKey(const Key('environment-model-selector'));
      expect(
        find.descendant(
          of: modelDialog,
          matching: find.text('models.dev · anthropic_messages'),
        ),
        findsOneWidget,
      );
      expect(
        find.descendant(
          of: modelDialog,
          matching: find.text('https://api.anthropic.com/v1/models'),
        ),
        findsOneWidget,
      );
      expect(
        find.descendant(
          of: modelDialog,
          matching: find.text(
            'Account: Anthropic · Work · Anthropic API key · X-Api-Key',
          ),
        ),
        findsOneWidget,
      );

      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-model-selector')), findsNothing);
      expect(find.byKey(const Key('environment-editor-form')), findsOneWidget);

      await tester.ensureVisible(modelField);
      await tester.pumpAndSettle();
      await tester.tap(modelField);
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-model-requested-0')),
        'claude-sonnet-4-5',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(
          const Key('environment-model-requested-0-option-claude-sonnet-4-5'),
        ),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-model-upstream-0')),
        '20250929',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(
          const Key(
            'environment-model-upstream-0-option-claude-sonnet-4-5-20250929',
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-model-add')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-model-requested-1')),
        'claude-haiku-4-5',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(
          const Key('environment-model-requested-1-option-claude-haiku-4-5'),
        ),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-model-upstream-1')),
        ' dashscope:deepseek-v4-flash-0731 ',
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-model-save')));
      await tester.pumpAndSettle();
      expect(find.text('2 mappings'), findsOneWidget);

      await tester.enterText(
        find.byKey(const Key('environment-editor-name')),
        'Work reviewed',
      );
      final runtimeTab = find.byKey(const Key('environment-tab-runtime'));
      await tester.ensureVisible(runtimeTab);
      await tester.pumpAndSettle();
      await tester.tap(runtimeTab);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-editor-tool-mode')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Ask before tools run').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-editor-recording')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Metadata only').last);
      await tester.pumpAndSettle();
      final trafficTab = find.byKey(const Key('environment-tab-traffic'));
      await tester.ensureVisible(trafficTab);
      await tester.pumpAndSettle();
      await tester.tap(trafficTab);
      await tester.pumpAndSettle();
      await tester.ensureVisible(accountField);
      await tester.pumpAndSettle();
      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.textContaining('Anthropic · Lab').last);
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-impact-review')),
        findsOneWidget,
      );
      expect(find.text('Future Captures only'), findsWidgets);
      expect(
        find.text('6 RUNNING CAPTURES KEEP THEIR CURRENT REVISION'),
        findsOneWidget,
      );
      expect(find.text('r7'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.text('Work reviewed'), findsWidgets);
      expect(find.text('Revision 8'), findsOneWidget);
      expect(
        find.text(
          'Traffic policy published from the reviewed draft and impact boundary.',
        ),
        findsOneWidget,
      );
      expect(find.text('Anthropic · Lab'), findsOneWidget);

      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      expect(
        find.descendant(of: modelField, matching: find.text('2 mappings')),
        findsOneWidget,
      );
      await tester.ensureVisible(modelField);
      await tester.pumpAndSettle();
      await tester.tap(modelField);
      await tester.pumpAndSettle();
      expect(
        tester
            .widget<TextField>(
              find.byKey(const Key('environment-model-upstream-0')),
            )
            .controller
            ?.text,
        ' dashscope:deepseek-v4-flash-0731 ',
      );
      expect(
        tester
            .widget<TextField>(
              find.byKey(const Key('environment-model-requested-0')),
            )
            .controller
            ?.text,
        'claude-haiku-4-5',
      );
      expect(
        tester
            .widget<TextField>(
              find.byKey(const Key('environment-model-upstream-1')),
            )
            .controller
            ?.text,
        'claude-sonnet-4-5-20250929',
      );
      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-model-selector')), findsNothing);
      expect(find.byKey(const Key('environment-editor-form')), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Environment detail fills a wide pane and built-in state uses a precise lock marker',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1800, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      final systemMarker = find.byKey(
        const Key('environment-system-marker-system_transparent'),
      );
      expect(systemMarker, findsOneWidget);
      expect(
        tester.widget<Icon>(systemMarker).icon,
        Icons.lock_outline_rounded,
      );

      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      final clientPlan = find.byKey(
        const Key('environment-client-plan-claude-client'),
      );
      expect(clientPlan, findsOneWidget);
      expect(tester.getSize(clientPlan).width, greaterThan(1200));
      expect(1800 - tester.getTopRight(clientPlan).dx, lessThanOrEqualTo(16));
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Endpoint discovery failure explains authentication and keeps manual mappings usable',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(
        upstreamModelFailure: const ControlProblem(
          status: 502,
          reasonCode: 'model_catalog_authentication_rejected',
          messageKey: 'error.model_catalog_authentication_rejected',
        ),
      );
      addTearDown(api.close);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final modelField = find.byKey(
        const Key('environment-route-model-anthropic-direct'),
      );
      await tester.ensureVisible(modelField);
      await tester.pumpAndSettle();
      await tester.tap(modelField);
      await tester.pumpAndSettle();

      expect(
        find.textContaining('Upstream model discovery unavailable.'),
        findsOneWidget,
      );
      expect(
        find.textContaining('The upstream service rejected this account'),
        findsOneWidget,
      );
      expect(
        find.textContaining('This account sends X-Api-Key.'),
        findsOneWidget,
      );
      expect(
        find.textContaining('You can still enter exact model IDs manually.'),
        findsOneWidget,
      );

      await tester.enterText(
        find.byKey(const Key('environment-model-requested-0')),
        'claude-manual-alias',
      );
      await tester.enterText(
        find.byKey(const Key('environment-model-upstream-0')),
        'relay/custom:manual-model',
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-model-save')));
      await tester.pumpAndSettle();
      expect(find.text('1 mappings'), findsOneWidget);
      expect(find.byKey(const Key('environment-editor-form')), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
      controller.dispose();
    },
  );

  testWidgets('390px Chinese Environment impact stays usable', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();
    expect(
      find.text('每种客户端协议和入口都可以保留原始目标，或把请求发往上游服务，并使用该服务所属的账号。'),
      findsOneWidget,
    );

    final modelField = find.byKey(
      const Key('environment-route-model-anthropic-direct'),
    );
    await tester.ensureVisible(modelField);
    await tester.pumpAndSettle();
    await tester.tap(modelField);
    await tester.pumpAndSettle();
    final modelDialog = find.byKey(const Key('environment-model-selector'));
    expect(modelDialog, findsOneWidget);
    expect(tester.getSize(modelDialog).width, lessThanOrEqualTo(342));
    await tester.enterText(
      find.byKey(const Key('environment-model-requested-0')),
      'claude-mobile-alias',
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-model-upstream-0')),
      'relay-model-custom',
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-model-save')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-model-selector')), findsNothing);
    expect(find.text('1 条映射'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.enterText(
      find.byKey(const Key('environment-editor-name')),
      '工作环境',
    );
    await tester.tap(find.byKey(const Key('environment-review')));
    await tester.pumpAndSettle();
    expect(find.text('发布后会改变什么'), findsOneWidget);
    expect(find.text('6 条运行中记录保持当前修订'), findsOneWidget);
    expect(find.byKey(const Key('environment-publish')), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const Key('environment-impact-back')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    '390px Environment freezes one published network profile revision',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      final profile = await api.publishEgressProfile(
        id: 'profile.team',
        expectedRevision: 0,
        displayName: 'Team proxy',
        policy: const TrafficEgressPolicy(
          proxy: TrafficProxyPolicy(kind: 'socks5', endpoint: '127.0.0.1:1080'),
          resolver: TrafficResolverPolicy(
            kind: 'doh',
            transport: 'proxy',
            dohUrl: 'https://1.1.1.1/dns-query',
          ),
        ),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.environments,
          language: AppLanguage.simplifiedChinese,
        ),
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();

      final networkPath = find.byKey(
        const Key('environment-egress-claude-client-plan'),
      );
      await tester.ensureVisible(networkPath);
      await tester.pumpAndSettle();
      expect(
        find.descendant(of: networkPath, matching: find.text('直连 · 系统 DNS')),
        findsOneWidget,
      );
      await tester.tap(networkPath);
      await tester.pumpAndSettle();

      final dialog = find.byKey(
        const Key('environment-egress-profile-dialog-claude-client-plan'),
      );
      expect(dialog, findsOneWidget);
      expect(tester.getSize(dialog).width, lessThanOrEqualTo(342));
      await tester.tap(
        find.byKey(Key('environment-egress-profile-${profile.id}-1')),
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('environment-egress-profile-save')),
      );
      await tester.pumpAndSettle();
      expect(dialog, findsNothing);
      expect(
        find.descendant(
          of: networkPath,
          matching: find.textContaining('Team proxy · r1'),
        ),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);

      await api.publishEgressProfile(
        id: profile.id,
        expectedRevision: 1,
        displayName: 'Team proxy',
        policy: const TrafficEgressPolicy.direct(),
      );

      await tester.ensureVisible(find.byKey(const Key('environment-review')));
      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final reopened = find.byKey(
        const Key('environment-egress-claude-client-plan'),
      );
      await tester.ensureVisible(reopened);
      expect(
        find.descendant(
          of: reopened,
          matching: find.textContaining('Team proxy · r1'),
        ),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Environment freezes one ordered Code Library revision', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    await api.createCodeLibraryCollection(
      id: 'privacy',
      displayName: 'Privacy',
    );
    final revision = await api.publishCodeLibraryTransform(
      id: 'transform.home-redaction',
      expectedRevision: 0,
      collectionId: 'privacy',
      displayName: 'Home redaction',
      policy: const TrafficTransformPolicy(
        requestJavaScript: 'context.model = JSON.parse(request.body).model;',
        responseJavaScript:
            'response.headers["x-original-model"] = [context.model];',
      ),
    );
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.environments,
        language: AppLanguage.simplifiedChinese,
      ),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();

    final transform = find.byKey(
      const Key('environment-transform-claude-client-plan'),
    );
    await tester.ensureVisible(transform);
    await tester.pumpAndSettle();
    expect(
      find.descendant(of: transform, matching: find.text('未配置')),
      findsOneWidget,
    );
    await tester.tap(transform);
    await tester.pumpAndSettle();

    final dialog = find.byKey(
      const Key('environment-transform-pipeline-dialog-claude-client-plan'),
    );
    expect(dialog, findsOneWidget);
    expect(tester.getSize(dialog).width, lessThanOrEqualTo(342));
    await tester.tap(
      find.byKey(
        Key(
          'environment-transform-pipeline-add-${revision.id}-${revision.revision}',
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(
        const Key('environment-transform-pipeline-save-claude-client-plan'),
      ),
    );
    await tester.pumpAndSettle();
    expect(dialog, findsNothing);
    expect(
      find.descendant(
        of: transform,
        matching: find.text('Home redaction · r1'),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    await api.publishCodeLibraryTransform(
      id: revision.id,
      expectedRevision: revision.revision,
      collectionId: revision.collectionId,
      displayName: revision.displayName,
      policy: const TrafficTransformPolicy.disabled(),
    );

    await tester.ensureVisible(find.byKey(const Key('environment-review')));
    await tester.tap(find.byKey(const Key('environment-review')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-publish')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();
    final reopened = find.byKey(
      const Key('environment-transform-claude-client-plan'),
    );
    await tester.ensureVisible(reopened);
    expect(
      find.descendant(of: reopened, matching: find.text('Home redaction · r1')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'Environment publishes and reopens one frozen Account Selector revision',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      await api.createCodeLibraryCollection(
        id: 'routing',
        displayName: 'Routing',
      );
      final revision = await api.publishCodeLibraryAccountSelector(
        id: 'selector.workspace',
        expectedRevision: 0,
        collectionId: 'routing',
        displayName: 'Workspace account',
        policy: const AccountSelectorPolicy(
          javaScript: 'selection.accountId = accounts[0].id;',
        ),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.environments,
          language: AppLanguage.english,
        ),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-create')));
      await tester.pumpAndSettle();
      final clientFlow = find.byKey(
        const Key('environment-client-plan-target'),
      );
      await tester.ensureVisible(clientFlow);
      await tester.tap(clientFlow);
      await tester.pumpAndSettle();
      final anthropicOption = find.text('https://api.anthropic.com').last;
      expect(anthropicOption.hitTestable(), findsOneWidget);
      await tester.tap(anthropicOption);
      await tester.pumpAndSettle();
      final destination = find.byKey(const Key('environment-destination-kind'));
      await tester.ensureVisible(destination);
      await tester.tap(
        find.descendant(
          of: destination,
          matching: find.text('Upstream service'),
        ),
      );
      await tester.pumpAndSettle();
      final endpointCatalog = find.byKey(
        const Key('environment-endpoint-catalog'),
      );
      await tester.ensureVisible(endpointCatalog);
      await tester.tap(endpointCatalog);
      await tester.pumpAndSettle();
      await tester.tap(find.textContaining('Anthropic API').last);
      await tester.pumpAndSettle();
      final initialAccount = find.byKey(
        const Key('environment-endpoint-account-target.anthropic.official'),
      );
      await tester.ensureVisible(initialAccount);
      expect(find.text('Account selection'), findsOneWidget);
      expect(
        find.text('Published account selection rules: 1. Open to choose.'),
        findsOneWidget,
      );
      final initialDropdown = tester.widget<CompactSelectField<String>>(
        initialAccount,
      );
      expect(
        initialDropdown.items.map((item) => item.value),
        contains('javascript:selector.workspace:1'),
      );
      await tester.tap(initialAccount);
      await tester.pumpAndSettle();
      await tester.tap(
        find.text('JavaScript rule · Workspace account · r1').last,
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();
      expect(
        find.text('Runs once per Turn against 2 frozen upstream accounts.'),
        findsOneWidget,
      );
      await tester.tap(find.text('Cancel').last);
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final fixed = find.byKey(
        const Key(
          'environment-route-account-anthropic-direct-fixed:anthropic-work',
        ),
      );
      await tester.ensureVisible(fixed);
      await tester.tap(fixed);
      await tester.pumpAndSettle();
      await tester.tap(
        find.text('JavaScript rule · Workspace account · r1').last,
      );
      await tester.pumpAndSettle();
      expect(
        find.byKey(
          const Key(
            'environment-route-account-anthropic-direct-javascript:selector.workspace:1',
          ),
        ),
        findsOneWidget,
      );
      expect(
        find.text('Runs once per Turn against 2 frozen upstream accounts.'),
        findsOneWidget,
      );

      await api.publishCodeLibraryAccountSelector(
        id: revision.id,
        expectedRevision: revision.revision,
        collectionId: revision.collectionId,
        displayName: revision.displayName,
        policy: const AccountSelectorPolicy(
          javaScript: 'selection.accountId = accounts[accounts.length - 1].id;',
        ),
      );
      await tester.ensureVisible(find.byKey(const Key('environment-review')));
      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final reopened = find.byKey(
        const Key(
          'environment-route-account-anthropic-direct-javascript:selector.workspace:1',
        ),
      );
      await tester.ensureVisible(reopened);
      expect(reopened, findsOneWidget);
      expect(
        find.descendant(
          of: reopened,
          matching: find.textContaining('Workspace account · r1'),
        ),
        findsOneWidget,
      );
      await tester.tap(find.text('Cancel').last);
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      controller.selectSection(WorkbenchSection.captures);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Claude Code').first);
      await tester.pumpAndSettle();
      expect(controller.selectedCapture?.managedRun?.executableLabel, 'claude');
      expect(controller.selectedAssignment?.environmentId, 'work');
      await tester.tap(
        find.byKey(const Key('capture-environment-apply-latest')),
      );
      await tester.pumpAndSettle();
      expect(controller.selectedAssignment?.environmentRevision, 8);
      expect(
        controller.data?.environments
            .singleWhere((environment) => environment.id == 'work')
            .routes
            .singleWhere((route) => route.id == 'anthropic-direct')
            .accountPolicy
            .mode,
        'javascript',
      );
      expect(
        find.textContaining('JavaScript rule · Workspace account · r1'),
        findsWidgets,
      );
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px launch environment overlay survives publish and reopen', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-tab-runtime')));
    await tester.pumpAndSettle();
    final launch = find.byKey(const Key('environment-launch-edit'));
    await tester.ensureVisible(launch);
    await tester.tap(launch);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('environment-launch-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-name-0')),
      'TEAM_CONTEXT',
    );
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-value-0')),
      'research',
    );
    await tester.tap(find.byKey(const Key('environment-launch-add-delete')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-launch-delete-name-0')),
      'OLD_CONTEXT',
    );
    await tester.tap(find.byKey(const Key('environment-launch-save')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(of: launch, matching: find.text('设置 1 · 删除 1')),
      findsOneWidget,
    );

    await tester.ensureVisible(find.byKey(const Key('environment-review')));
    await tester.tap(find.byKey(const Key('environment-review')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-publish')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-tab-runtime')));
    await tester.pumpAndSettle();
    final reopened = find.byKey(const Key('environment-launch-edit'));
    await tester.ensureVisible(reopened);
    expect(
      find.descendant(of: reopened, matching: find.text('设置 1 · 删除 1')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('new Capture-only Environment is published at revision one', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-create')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-page')), findsOneWidget);
    expect(find.byType(AlertDialog), findsNothing);
    expect(
      tester.getSize(find.byKey(const Key('environment-create-frame'))).width,
      greaterThan(ViberMetrics.dialogWideWidth),
    );
    final createToolMode = find.byKey(
      const Key('environment-create-tool-mode'),
    );
    final createRecording = find.byKey(
      const Key('environment-create-recording'),
    );
    final clientFlowComposer = find.byKey(
      const Key('environment-client-flow-composer'),
    );
    final captureOnlyState = find.byKey(
      const Key('environment-capture-only-state'),
    );
    expect(clientFlowComposer, findsOneWidget);
    expect(captureOnlyState, findsOneWidget);
    expect(
      tester.getSize(clientFlowComposer).height,
      lessThanOrEqualTo(108),
      reason: 'the empty client-flow composer should remain one compact unit',
    );
    final captureOnlySize = tester.getSize(captureOnlyState);
    expect(
      captureOnlySize.height,
      lessThanOrEqualTo(32),
      reason:
          'capture-only guidance is a quiet status row, not another card '
          '(available width: ${captureOnlySize.width})',
    );
    final createName = find.byKey(const Key('environment-create-name'));
    final createId = find.byKey(const Key('environment-create-id'));
    expect(find.text('Command ID'), findsOneWidget);
    expect(
      find.text(
        'Used by vibermate run --env <ID>; stays stable after publish.',
      ),
      findsOneWidget,
    );
    expect(find.text('Requests from'), findsOneWidget);
    expect(find.text('Send to'), findsOneWidget);
    for (final field in [createName, createId]) {
      expect(tester.getSize(field).height, ViberMetrics.controlHeight);
    }
    expect(
      tester.getTopLeft(createName).dy,
      tester.getTopLeft(createId).dy,
      reason: 'identity controls in the same row share one baseline',
    );
    expect(
      tester.getBottomLeft(createName).dy,
      tester.getBottomLeft(createId).dy,
      reason: 'identity controls in the same row share one height',
    );
    final clientFlow = find.byKey(const Key('environment-client-plan-target'));
    final destination = find.byKey(const Key('environment-destination-kind'));
    expect(clientFlow, findsOneWidget);
    expect(destination, findsOneWidget);
    expect(
      [tester.getSize(clientFlow).height, tester.getSize(destination).height],
      [ViberMetrics.controlHeight, ViberMetrics.controlHeight],
      reason: 'adjacent controls use the shared desktop control height',
    );
    expect(
      tester.getTopLeft(clientFlow).dy,
      tester.getTopLeft(destination).dy,
      reason: 'client flow and destination controls align at the top',
    );
    expect(
      tester.getBottomLeft(clientFlow).dy,
      tester.getBottomLeft(destination).dy,
      reason: 'client flow and destination controls align at the bottom',
    );
    expect(find.byKey(const Key('environment-endpoint-catalog')), findsNothing);
    expect(
      find.text('Capture-only · Requests are forwarded unchanged.'),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('environment-tab-runtime')));
    await tester.pumpAndSettle();
    for (final field in [
      createToolMode,
      createRecording,
      find.byKey(const Key('environment-create-retention')),
    ]) {
      expect(tester.getSize(field).height, ViberMetrics.controlHeight);
    }
    await tester.tap(find.byKey(const Key('environment-tab-traffic')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-create-name')),
      'Local Observe',
    );
    expect(find.text('local-observe'), findsOneWidget);

    await tester.tap(find.byKey(const Key('environment-create-review')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-impact')), findsOneWidget);
    expect(
      find.text('0 RUNNING CAPTURES KEEP THEIR CURRENT REVISION'),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('environment-create-publish')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('environment-row-local-observe')),
      findsOneWidget,
    );
    expect(find.text('Revision 1'), findsOneWidget);
    expect(
      find.text(
        "No Client Flow is configured. New Captures keep each request's Original Destination and record connection metadata.",
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px novice flow makes direct capture an explicit destination', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-create')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-tab-traffic')), findsOneWidget);
    expect(find.byKey(const Key('environment-tab-runtime')), findsOneWidget);
    expect(find.byKey(const Key('environment-create-tool-mode')), findsNothing);
    await tester.tap(find.byKey(const Key('environment-tab-runtime')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-create-tool-mode')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('environment-launch-edit')), findsOneWidget);
    await tester.tap(find.byKey(const Key('environment-tab-traffic')));
    await tester.pumpAndSettle();
    final clientFlow = find.byKey(const Key('environment-client-plan-target'));
    await tester.ensureVisible(clientFlow);
    await tester.tap(clientFlow);
    await tester.pumpAndSettle();
    expect(
      find.byKey(
        const Key(
          'environment-client-plan-option-anthropic_messages-https://api.anthropic.com',
        ),
      ),
      findsOneWidget,
    );
    expect(
      find.byKey(
        const Key(
          'environment-client-plan-option-openai_responses-https://api.openai.com',
        ),
      ),
      findsOneWidget,
    );
    expect(
      find.byKey(
        const Key(
          'environment-client-plan-option-openai_responses-https://chatgpt.com',
        ),
      ),
      findsOneWidget,
    );
    expect(
      find.byKey(
        const Key(
          'environment-client-plan-option-openai_chat-https://api.openai.com',
        ),
      ),
      findsNothing,
      reason: 'OpenAI Chat is an upstream backend, not a proven client edge.',
    );
    final anthropicOption = find.text('https://api.anthropic.com').last;
    expect(anthropicOption.hitTestable(), findsOneWidget);
    await tester.tap(anthropicOption);
    await tester.pumpAndSettle();

    final destination = find.byKey(const Key('environment-destination-kind'));
    expect(destination, findsOneWidget);
    expect(tester.getTopLeft(destination).dx, greaterThanOrEqualTo(0));
    expect(tester.getBottomRight(destination).dx, lessThanOrEqualTo(390));
    expect(find.text('原服务'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('environment-create-form')),
        matching: find.textContaining('ViberMate 仍会抓包'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('environment-endpoint-catalog')), findsNothing);

    expect(find.text('使用原始目标'), findsOneWidget);
    expect(find.text('添加上游路由'), findsNothing);
    await tester.tap(
      find.descendant(of: destination, matching: find.text('上游服务')),
    );
    await tester.pumpAndSettle();
    expect(find.text('添加上游路由'), findsOneWidget);
    expect(find.text('使用原始目标'), findsNothing);
    await tester.tap(
      find.descendant(of: destination, matching: find.text('原服务')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-use-original')));
    await tester.pumpAndSettle();
    expect(
      find.byWidgetPredicate(
        (widget) =>
            widget.key is ValueKey<String> &&
            (widget.key! as ValueKey<String>).value.startsWith(
              'environment-original-destination-',
            ),
      ),
      findsOneWidget,
    );
    expect(find.textContaining('必须选择该 Endpoint 的账号'), findsNothing);

    await tester.enterText(
      find.byKey(const Key('environment-create-name')),
      '直接抓包',
    );
    await tester.enterText(
      find.byKey(const Key('environment-create-id')),
      'direct-capture',
    );

    final review = find.byKey(const Key('environment-create-review'));
    await tester.ensureVisible(review);
    await tester.tap(review);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-impact')), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('one multi-protocol Endpoint can join independent client flows', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-create')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-create-name')),
      'Multi protocol relay',
    );

    final clientFlow = find.byKey(const Key('environment-client-plan-target'));
    final catalog = find.byKey(const Key('environment-endpoint-catalog'));

    await tester.tap(clientFlow);
    await tester.pumpAndSettle();
    final anthropicOption = find.text('https://api.anthropic.com').last;
    expect(anthropicOption.hitTestable(), findsOneWidget);
    await tester.tap(anthropicOption);
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(
        of: find.byKey(const Key('environment-destination-kind')),
        matching: find.text('Upstream service'),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(catalog);
    await tester.pumpAndSettle();
    await tester.tap(
      find.text('Orbit Relay · Tokyo · https://tokyo.orbitrelay.example').last,
    );
    await tester.pumpAndSettle();
    final firstAdd = find.byKey(const Key('environment-add-endpoint'));
    expect(
      tester
          .widget<CompactSelectField<String>>(
            find.byKey(
              const Key('environment-endpoint-account-target.orbit.relay'),
            ),
          )
          .initialValue,
      'fixed:orbit-team',
    );
    expect(tester.widget<OutlinedButton>(firstAdd).onPressed, isNotNull);
    await tester.tap(firstAdd);
    await tester.pumpAndSettle();

    await tester.tap(clientFlow);
    await tester.pumpAndSettle();
    final responsesOption = find.text('https://api.openai.com').last;
    expect(responsesOption.hitTestable(), findsOneWidget);
    await tester.tap(responsesOption);
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(
        of: find.byKey(const Key('environment-destination-kind')),
        matching: find.text('Upstream service'),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(catalog);
    await tester.pumpAndSettle();
    await tester.tap(
      find.text('Orbit Relay · Tokyo · https://tokyo.orbitrelay.example').last,
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-add-endpoint')));
    await tester.pumpAndSettle();

    final clientFlows = find.byType(ExpansionTile);
    expect(clientFlows, findsNWidgets(2));
    Finder modelButtonWithin(Finder flow) => find.descendant(
      of: flow,
      matching: find.byWidgetPredicate(
        (widget) =>
            widget is OutlinedButton &&
            widget.key is ValueKey<String> &&
            (widget.key! as ValueKey<String>).value.startsWith(
              'environment-route-model-',
            ),
      ),
    );
    final firstModel = modelButtonWithin(clientFlows.first);
    expect(firstModel, findsOneWidget);
    await tester.ensureVisible(firstModel);
    await tester.tap(firstModel);
    await tester.pumpAndSettle();
    expect(find.text('models.dev · anthropic_messages'), findsOneWidget);
    expect(
      find.text('https://tokyo.orbitrelay.example/v1/models'),
      findsOneWidget,
    );
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();

    await tester.ensureVisible(clientFlows.last);
    await tester.tap(clientFlows.last);
    await tester.pumpAndSettle();
    final secondModel = modelButtonWithin(clientFlows.last);
    expect(secondModel, findsOneWidget);
    await tester.ensureVisible(secondModel);
    await tester.tap(secondModel);
    await tester.pumpAndSettle();
    expect(find.text('models.dev · openai_responses'), findsOneWidget);
    expect(
      find.text('https://tokyo.orbitrelay.example/v1/models'),
      findsOneWidget,
    );
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    final review = find.byKey(const Key('environment-create-review'));
    await tester.ensureVisible(review);
    await tester.tap(review);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-impact')), findsOneWidget);
    await tester.tap(find.byKey(const Key('environment-create-publish')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-row-multi-protocol-relay')),
      findsOneWidget,
    );
    expect(find.text('2 upstream routes'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'new Environment can combine official and relay upstream Endpoints',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-create')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-create-name')),
        'Multi upstream',
      );

      final clientFlow = find.byKey(
        const Key('environment-client-plan-target'),
      );
      await tester.tap(clientFlow);
      await tester.pumpAndSettle();
      final anthropicOption = find.text('https://api.anthropic.com').last;
      expect(anthropicOption.hitTestable(), findsOneWidget);
      await tester.tap(anthropicOption);
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byKey(const Key('environment-destination-kind')),
          matching: find.text('Upstream service'),
        ),
      );
      await tester.pumpAndSettle();

      final catalog = find.byKey(const Key('environment-endpoint-catalog'));
      await tester.tap(catalog);
      await tester.pumpAndSettle();
      await tester.tap(
        find.text('Anthropic API · https://api.anthropic.com').last,
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();

      await tester.tap(catalog);
      await tester.pumpAndSettle();
      await tester.tap(
        find
            .text('Orbit Relay · Tokyo · https://tokyo.orbitrelay.example')
            .last,
      );
      await tester.pumpAndSettle();
      final relayAccount = find.byKey(
        const Key('environment-endpoint-account-target.orbit.relay'),
      );
      final relayDropdown = tester.widget<CompactSelectField<String>>(
        relayAccount,
      );
      expect(
        relayDropdown.items.map((item) => item.value),
        orderedEquals(['fixed:orbit-team']),
      );
      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();

      expect(find.text('2 routes'), findsOneWidget);
      expect(find.text('Anthropic API'), findsWidgets);
      expect(find.text('Orbit Relay · Tokyo'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-create-review')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-create-impact')),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('environment-create-publish')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-row-multi-upstream')),
        findsOneWidget,
      );
      expect(find.text('2 upstream routes'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Environment adds multiple upstream Endpoints without changing the current default',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-row-research')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();

      final catalog = find.byKey(const Key('environment-endpoint-catalog'));
      await tester.ensureVisible(catalog);
      await tester.pumpAndSettle();
      await tester.tap(catalog);
      await tester.pumpAndSettle();
      await tester.tap(find.textContaining('Anthropic API').last);
      await tester.pumpAndSettle();

      final accountField = find.byKey(
        const Key('environment-endpoint-account-target.anthropic.official'),
      );
      final accountDropdown = tester.widget<CompactSelectField<String>>(
        accountField,
      );
      final accountIds = accountDropdown.items
          .map((item) => item.value)
          .toList(growable: false);
      expect(
        accountIds,
        containsAll(['fixed:anthropic-work', 'fixed:anthropic-lab']),
      );
      expect(accountIds, isNot(contains('fixed:openai-work')));
      expect(accountIds, isNot(contains('fixed:orbit-team')));

      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.textContaining('Anthropic · Work').last);
      await tester.pumpAndSettle();

      // A catalog choice is still pending until it is added to the draft.
      // Reviewing must never silently publish an Environment without it.
      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-endpoint-pending-error')),
        findsOneWidget,
      );
      expect(find.byKey(const Key('environment-impact-review')), findsNothing);

      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-endpoint-pending-error')),
        findsNothing,
      );
      expect(find.textContaining('Anthropic · Work'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(find.text('Future Captures only'), findsWidgets);
      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.text('Revision 4'), findsOneWidget);
      expect(find.text('2 upstream routes'), findsOneWidget);
      expect(find.text('Default'), findsOneWidget);
      expect(find.text('Candidate'), findsOneWidget);

      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      expect(find.textContaining('Anthropic · Work'), findsWidgets);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('Environment Endpoint removal stays a reviewed draft', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('environment-client-endpoint-codex-client')),
      findsOneWidget,
    );
    final removeEndpoint = find.byKey(
      const Key('environment-remove-endpoint-codex-client'),
    );
    await tester.ensureVisible(removeEndpoint);
    await tester.tap(removeEndpoint);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-client-endpoint-codex-client')),
      findsNothing,
    );

    final review = find.byKey(const Key('environment-review'));
    await tester.ensureVisible(review);
    await tester.tap(review);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-impact-review')), findsOneWidget);
    await tester.tap(find.byKey(const Key('environment-publish')));
    await tester.pumpAndSettle();
    expect(find.text('https://api.openai.com'), findsNothing);
    expect(find.text('Revision 8'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('Endpoint-owned Account can be created, rotated, and safely deleted', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openUpstreamServices(tester);

    final addAccount = find.byKey(const Key('accounts-add'));
    expect(1180 - tester.getTopRight(addAccount).dx, lessThanOrEqualTo(14));
    await tester.tap(find.byKey(const Key('endpoints-add')));
    await tester.pumpAndSettle();
    expect(
      tester.getSize(find.byKey(const Key('endpoint-editor-frame'))).width,
      ViberMetrics.dialogCompactWidth,
    );
    for (final key in const [
      Key('endpoint-editor-name'),
      Key('endpoint-editor-origin'),
    ]) {
      final field = find.byKey(key);
      expect(
        tester.getSize(field).height,
        ViberMetrics.controlHeight,
        reason: '$key must use the shared desktop control height',
      );
      expect(
        paintedFormSurfaceHeight(tester, field),
        ViberMetrics.controlHeight,
        reason: '$key must paint the shared form-control surface',
      );
    }
    final endpointField = tester.widget<TextField>(
      find.descendant(
        of: find.byKey(const Key('endpoint-editor-name')),
        matching: find.byType(TextField),
      ),
    );
    expect(endpointField.textAlignVertical, TextAlignVertical.center);
    final endpointEditable = tester.widget<EditableText>(
      find.descendant(
        of: find.byKey(const Key('endpoint-editor-name')),
        matching: find.byType(EditableText),
      ),
    );
    expect(endpointEditable.style.fontSize, ViberType.control);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('endpoint-editor-name')), findsNothing);

    await tester.tap(find.byKey(const Key('endpoints-add')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('endpoint-editor-protocol-anthropic_messages')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('endpoint-editor-name')),
      'Team Relay',
    );
    await tester.enterText(
      find.byKey(const Key('endpoint-editor-origin')),
      'http://spark-2a59:8888',
    );
    await tester.pump();
    expect(
      find.text(
        'HTTP is limited to local or private-network peers. Conversations and credentials are sent without transport encryption.',
      ),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('endpoint-editor-save')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Upstream service created. Upstream accounts can now be added to it.',
      ),
      findsOneWidget,
    );
    expect(find.text('Team Relay'), findsWidgets);

    await tester.tap(find.byKey(const Key('accounts-add')));
    await tester.pumpAndSettle();
    expect(
      tester.getSize(find.byKey(const Key('account-editor-frame'))).width,
      ViberMetrics.dialogStandardWidth,
    );
    expect(find.text('Team Relay'), findsWidgets);
    for (final key in const [
      Key('account-editor-kind'),
      Key('account-editor-name'),
      Key('account-editor-secret'),
    ]) {
      final field = find.byKey(key);
      expect(
        tester.getSize(field).height,
        ViberMetrics.controlHeight,
        reason: '$key must use the shared desktop control height',
      );
      expect(
        paintedFormSurfaceHeight(tester, field),
        ViberMetrics.controlHeight,
        reason: '$key must paint the shared form-control surface',
      );
    }
    expect(
      tester
          .widget<CompactSelectField<String>>(
            find.byKey(const Key('account-editor-kind')),
          )
          .decoration
          .labelText,
      isNull,
    );
    for (final key in const [
      Key('account-editor-name'),
      Key('account-editor-secret'),
    ]) {
      expect(
        tester
            .widget<TextField>(
              find.descendant(
                of: find.byKey(key),
                matching: find.byType(TextField),
              ),
            )
            .decoration
            ?.labelText,
        isNull,
      );
    }
    expect(
      tester
          .widget<Text>(find.byKey(const Key('account-editor-auth-transport')))
          .data,
      'X-Api-Key',
    );
    await tester.tap(find.byKey(const Key('account-editor-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Bearer token').last);
    await tester.pumpAndSettle();
    expect(
      tester
          .widget<Text>(find.byKey(const Key('account-editor-auth-transport')))
          .data,
      'Authorization: Bearer',
    );
    await tester.tap(find.byKey(const Key('account-editor-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Anthropic API key').last);
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-editor-name')),
      'Team Primary',
    );
    await tester.enterText(
      find.byKey(const Key('account-editor-secret')),
      'sk-ant-preview-one',
    );
    await tester.tap(find.byKey(const Key('account-header-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-header-set-name-0')),
      'X-Team',
    );
    await tester.enterText(
      find.byKey(const Key('account-header-set-value-0')),
      'team-a',
    );
    await tester.tap(find.byKey(const Key('account-header-add-delete')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-header-delete-name-0')),
      'X-Legacy',
    );
    for (final key in const [
      Key('account-header-set-name-0'),
      Key('account-header-set-value-0'),
      Key('account-header-delete-name-0'),
    ]) {
      final field = find.byKey(key);
      expect(
        tester.getSize(field).height,
        ViberMetrics.controlHeight,
        reason: '$key must use the shared desktop control height',
      );
      expect(
        paintedFormSurfaceHeight(tester, field),
        ViberMetrics.controlHeight,
        reason: '$key must paint the shared form-control surface',
      );
    }
    await tester.tap(find.byKey(const Key('account-editor-save')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Account connected. Use it when a traffic policy sends requests to this service.',
      ),
      findsOneWidget,
    );
    final policyNextStep = find.text('Go to traffic policies');
    expect(policyNextStep, findsOneWidget);
    expect(find.text('Team Primary'), findsOneWidget);
    expect(
      find.textContaining('Anthropic API key · X-Api-Key'),
      findsOneWidget,
    );
    expect(find.textContaining('Set 1 · Delete 1'), findsOneWidget);

    await tester.tap(policyNextStep);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create')), findsOneWidget);
    await _openUpstreamServices(tester);
    expect(find.text('Team Primary'), findsOneWidget);

    await tester.tap(find.byIcon(Icons.key_outlined));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-editor-secret')),
      'sk-ant-preview-two',
    );
    expect(find.textContaining('X-Team'), findsOneWidget);
    await tester.tap(find.byKey(const Key('account-header-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-header-set-name-0')),
      'X-Team',
    );
    await tester.enterText(
      find.byKey(const Key('account-header-set-value-0')),
      'team-b',
    );
    await tester.tap(find.byKey(const Key('account-editor-save')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Credential replaced with its previous epoch as the CAS boundary.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('Credential version 2'), findsOneWidget);

    // Targeted by key, not by icon: the Endpoint itself now offers a delete
    // with the same icon, and an icon is not an identity.
    await tester.tap(
      find
          .byWidgetPredicate(
            (widget) =>
                widget is IconButton &&
                widget.key is ValueKey<String> &&
                (widget.key! as ValueKey<String>).value.startsWith(
                  'account-delete-',
                ),
          )
          .first,
    );
    await tester.pumpAndSettle();
    expect(find.text('Delete Team Primary?'), findsOneWidget);
    await tester.tap(find.byKey(const Key('account-delete-confirm')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Upstream account and credential deleted. Captured evidence was not removed.',
      ),
      findsOneWidget,
    );
    expect(find.text('Team Primary'), findsNothing);
    expect(find.text('No accounts yet'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px private HTTP Endpoint editor remains usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();
    await _openUpstreamServices(tester);

    await tester.tap(find.byKey(const Key('endpoints-add')));
    await tester.pumpAndSettle();
    for (final protocol in const <String, List<String>>{
      'anthropic_messages': ['Anthropic Messages', 'POST /v1/messages'],
      'openai_responses': ['OpenAI Responses', 'POST /v1/responses'],
      'openai_chat': ['OpenAI Chat', 'POST /v1/chat/completions'],
    }.entries) {
      final option = find.byKey(
        Key('endpoint-editor-protocol-${protocol.key}'),
      );
      expect(option, findsOneWidget);
      expect(tester.widget<CompactCheckboxOption>(option).value, isFalse);
      expect(tester.getSize(option).height, 32);
      expect(
        tester
            .getSize(
              find.descendant(of: option, matching: find.byType(Checkbox)),
            )
            .height,
        ViberMetrics.controlHeight,
      );
      final label = find.descendant(
        of: option,
        matching: find.text(protocol.value[0]),
      );
      final detail = find.descendant(
        of: option,
        matching: find.text(protocol.value[1]),
      );
      expect(label, findsOneWidget);
      expect(detail, findsOneWidget);
      expect(
        (tester.getCenter(label).dy - tester.getCenter(detail).dy).abs(),
        lessThan(1),
        reason: 'Protocol and HTTP route should read as one aligned row.',
      );
    }
    await tester.tap(find.byKey(const Key('endpoint-editor-save')));
    await tester.pumpAndSettle();
    expect(find.text('请至少选择一种上游协议。'), findsOneWidget);
    await tester.tap(
      find.byKey(const Key('endpoint-editor-protocol-anthropic_messages')),
    );
    await tester.tap(
      find.byKey(const Key('endpoint-editor-protocol-openai_responses')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('endpoint-editor-name')),
      'spark',
    );
    await tester.enterText(
      find.byKey(const Key('endpoint-editor-origin')),
      'http://spark–2a59:8888',
    );
    await tester.pumpAndSettle();

    // An en dash is visually close to the ASCII hostname hyphen. The editor
    // must reject it locally instead of sending a request that the runtime
    // later reports as a generic invalid Endpoint.
    await tester.tap(find.byKey(const Key('endpoint-editor-save')));
    await tester.pumpAndSettle();
    expect(
      find.text('请输入不含路径、查询、片段或显式默认端口的精确 HTTPS 地址，或受信任的本机/私网 HTTP 地址。'),
      findsOneWidget,
    );

    await tester.enterText(
      find.byKey(const Key('endpoint-editor-origin')),
      'http://spark-2a59:8888',
    );
    await tester.pumpAndSettle();

    expect(find.text('HTTP 仅允许连接本机或私网对端；对话与凭据将以明文传输。'), findsOneWidget);
    final save = find.byKey(const Key('endpoint-editor-save'));
    await tester.ensureVisible(save);
    expect(tester.widget<FilledButton>(save).onPressed, isNotNull);
    await tester.tap(save);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('endpoint-editor-name')), findsNothing);
    expect(find.text('Anthropic Messages'), findsOneWidget);
    expect(find.text('OpenAI Responses'), findsOneWidget);
    expect(find.text('OpenAI Chat'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('referenced Account deletion shows exact blocking routes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openUpstreamServices(tester);

    await tester.tap(find.byKey(const Key('account-delete-anthropic-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('account-delete-confirm')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'The runtime refused deletion because traffic policy routes still reference this account.',
      ),
      findsOneWidget,
    );
    expect(find.text('Work r7 · route anthropic-direct'), findsOneWidget);
    expect(find.text('Anthropic · Work'), findsWidgets);
    expect(tester.takeException(), isNull);

    await tester.tap(find.text('Confirm').last);
    await tester.pumpAndSettle();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

Future<void> _openNetwork(WidgetTester tester) async {
  await tester.tap(find.byKey(const Key('workbench-area-traffic')));
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const Key('workbench-tab-network')));
  await tester.pumpAndSettle();
  expect(find.byKey(const Key('network-tab-approvals')), findsOneWidget);
}

Future<void> _openUpstreamServices(WidgetTester tester) async {
  await tester.tap(find.byKey(const Key('workbench-area-configuration')));
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const Key('workbench-tab-routes')));
  await tester.pumpAndSettle();
  expect(find.byKey(const Key('endpoints-add')), findsOneWidget);
}

final class _FailingDashboardApi implements ControlApi {
  @override
  Future<DashboardData> loadDashboard() =>
      Future.error(StateError('startup-secret'));

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

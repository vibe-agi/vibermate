import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
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
      ViberMetrics.controlHeight * 2 + 4,
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
      expect(tester.getSize(rows.at(index)).height, ViberMetrics.controlHeight);
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

  testWidgets('wide evidence directories resize, collapse, and reopen', (
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

    await tester.tap(find.byIcon(Icons.forum_outlined).first);
    await tester.pumpAndSettle();
    final conversationPane = find.byKey(const Key('conversation-master-pane'));
    expect(conversationPane, findsOneWidget);
    await tester.tap(find.byKey(const Key('conversation-directory-toggle')));
    await tester.pumpAndSettle();
    expect(conversationPane, findsNothing);
    await tester.tap(find.byKey(const Key('conversation-directory-toggle')));
    await tester.pumpAndSettle();
    expect(conversationPane, findsOneWidget);
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

    expect(find.bySemanticsLabel(RegExp(r'^Captures\s+⌘1$')), findsOneWidget);
    expect(
      find.bySemanticsLabel(RegExp(r'^Hold network · Online$')),
      findsOneWidget,
    );
    final scaffoldContext = tester.element(find.byType(Scaffold).first);
    expect(
      Theme.of(scaffoldContext).focusColor,
      isNot(ViberColors.light.selection),
    );
    final theme = Theme.of(scaffoldContext);
    final focusedSide = theme.outlinedButtonTheme.style?.side?.resolve({
      WidgetState.focused,
    });
    final restingSide = theme.outlinedButtonTheme.style?.side?.resolve({});
    expect(focusedSide?.color, ViberColors.light.focus);
    expect(focusedSide?.width, 1.5);
    expect(restingSide?.color, ViberColors.light.divider);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.digit2);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pumpAndSettle();
    expect(find.text('CONVERSATIONS'), findsOneWidget);

    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    expect(FocusManager.instance.primaryFocus, isNotNull);
    expect(tester.takeException(), isNull);
    semantics.dispose();
  });

  testWidgets('Offline hold requires review before global traffic changes', (
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
      find.bySemanticsLabel(RegExp(r'^Hold network · Online$')),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('offline-confirmation')), findsOneWidget);
    expect(find.text('Enter offline hold?'), findsOneWidget);
    expect(find.byKey(const Key('offline-confirm-action')), findsOneWidget);
    await tester.tap(find.text('Cancel').last);
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(RegExp(r'^Hold network · Online$')),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(RegExp(r'^Resume online · Safe offline$')),
      findsOneWidget,
    );

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.text('Resume external work?'), findsOneWidget);
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(
      find.bySemanticsLabel(RegExp(r'^Hold network · Online$')),
      findsOneWidget,
    );
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Chinese Offline hold evidence stays operable', (
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
    expect(find.text('断网保持'), findsOneWidget);
    expect(find.text('在线'), findsOneWidget);
    final action = find.byKey(const Key('offline-settings-action'));
    await tester.ensureVisible(action);
    await tester.tap(action);
    await tester.pumpAndSettle();
    expect(find.text('进入断网保持？'), findsOneWidget);
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
        find.descendant(of: panel, matching: find.text('Ready')),
        findsOneWidget,
      );
      expect(find.text('Terminal command installed.'), findsOneWidget);
      final copyClaude = find.byKey(const Key('managed-run-copy-claude'));
      await tester.ensureVisible(copyClaude);
      await tester.tap(copyClaude);
      await tester.pumpAndSettle();
      expect(copiedCommand, 'vibermate run -- claude');
      expect(find.text('Claude command copied'), findsOneWidget);

      final remove = find.byKey(const Key('terminal-command-remove'));
      await tester.ensureVisible(remove);
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
      await tester.ensureVisible(details);
      await tester.drag(find.byType(ListView).last, const Offset(0, -80));
      await tester.pumpAndSettle();
      await tester.tap(details);
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('terminal-command-technical-details')),
        findsOneWidget,
      );
      expect(find.text(rawDiagnostic), findsOneWidget);

      final repair = find.byKey(const Key('terminal-command-repair'));
      await tester.ensureVisible(repair);
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

      await tester.tap(find.byIcon(Icons.hub_outlined).first);
      await tester.pumpAndSettle();
      expect(find.text('Endpoints & accounts'), findsWidgets);
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

  testWidgets(
    'Capture exposes one runtime Environment and keeps finished assignments read-only',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      final currentScope = find.byKey(const Key('capture-environment-scope'));
      expect(currentScope, findsOneWidget);
      expect(find.byKey(const Key('workspace-default-scope')), findsNothing);
      expect(
        find.descendant(of: currentScope, matching: find.text('Work')),
        findsOneWidget,
      );

      await tester.tap(
        find.descendant(
          of: currentScope,
          matching: find.byType(CompactSelectField<String>),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Research').last);
      await tester.pumpAndSettle();

      expect(
        find.descendant(of: currentScope, matching: find.text('Research')),
        findsOneWidget,
      );
      expect(
        find.text('Environment changed · existing traffic can switch hot.'),
        findsOneWidget,
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
      expect(find.byKey(const Key('workspace-default-scope')), findsNothing);
      expect(tester.takeException(), isNull);
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

    expect(find.text('Environment'), findsOneWidget);
    expect(find.byKey(const Key('capture-environment-scope')), findsOneWidget);
    expect(find.byKey(const Key('workspace-default-scope')), findsNothing);
    expect(
      find.descendant(
        of: find.byKey(const Key('capture-environment-scope')),
        matching: find.byType(CompactSelectField<String>),
      ),
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
    'global Conversations preserves boundaries and expands real Exchange evidence',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.forum_outlined).first);
      await tester.pumpAndSettle();
      expect(find.text('CONVERSATIONS'), findsOneWidget);
      await tester.enterText(
        find.byKey(const Key('conversation-filter')),
        'capture_run:run-1:main',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('conversation-row-capture_run:run-1:main')),
      );
      await tester.pumpAndSettle();
      expect(find.text('195 turns'), findsOneWidget);

      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await tester.ensureVisible(turn);
      final turnCard = find.byKey(
        const Key('conversation-turn-card-run-1-exchange-222'),
      );
      final collapsedDecoration =
          tester.widget<Container>(turnCard).decoration! as BoxDecoration;
      expect(collapsedDecoration.borderRadius, ViberMetrics.surfaceRadius);
      final turnNode = find.byKey(
        const Key('conversation-turn-node-run-1-exchange-222'),
      );
      final turnLabel = find.descendant(
        of: turn,
        matching: find.text('Turn 193'),
      );
      expect(
        (tester.getCenter(turnNode).dy - tester.getCenter(turnLabel).dy).abs(),
        lessThan(4),
      );
      await tester.tap(turn);
      await tester.pumpAndSettle();
      final expandedDecoration =
          tester.widget<Container>(turnCard).decoration! as BoxDecoration;
      expect(expandedDecoration.borderRadius, ViberMetrics.surfaceRadius);
      expect(
        find.text('Continue with the next verified implementation step.'),
        findsOneWidget,
      );
      expect(
        find.text(
          'The runtime evidence is consistent; continue with the next bounded change.',
        ),
        findsOneWidget,
      );
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
      await tester.ensureVisible(rawEvidence);
      await tester.tap(rawEvidence);
      await tester.pumpAndSettle();
      expect(find.text('1 boundary messages'), findsOneWidget);
      final rawReveal = find.byKey(
        const Key('raw-reveal-raw-preview-run-1-exchange-222'),
      );
      await tester.ensureVisible(rawReveal);
      await tester.tap(rawReveal);
      await tester.pumpAndSettle();
      final rawPayload = find.byKey(
        const Key('raw-revealed-raw-preview-run-1-exchange-222'),
      );
      expect(rawPayload, findsOneWidget);
      expect(
        find.descendant(
          of: rawPayload,
          matching: find.textContaining('Authorization: Bearer'),
        ),
        findsOneWidget,
      );
      expect(
        find.text('{"model":"claude-sonnet-4-5","stream":true}'),
        findsOneWidget,
      );
      expect(
        find.ancestor(
          of: find.text('Continue with the next verified implementation step.'),
          matching: find.byType(SingleChildScrollView),
        ),
        findsNothing,
      );

      final previousTurn = find.byKey(const Key('conversation-map-turn-192'));
      expect(previousTurn, findsOneWidget);
      await tester.tap(previousTurn);
      await tester.pumpAndSettle();
      final selectedMarker = find.descendant(
        of: previousTurn,
        matching: find.byType(AnimatedContainer),
      );
      expect(tester.getSize(selectedMarker).width, 22);

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
      final latestTurn = find.byKey(const Key('conversation-map-turn-195'));
      final latestSelectedMarker = find.descendant(
        of: latestTurn,
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
      final followingParagraph = find.text('hello');
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
      expect(
        find.descendant(
          of: find.byKey(const Key('conversation-capture-context')),
          matching: find.text('Go to Capture'),
        ),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('conversation-capture-context')));
      await tester.pumpAndSettle();
      expect(find.text('Capture conversation'), findsOneWidget);
      expect(
        find.byKey(const Key('capture-environment-scope')),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('long captured content defaults to 15 lines and can expand', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.forum_outlined).first);
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('conversation-filter')),
      'capture_run:run-1:main',
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('conversation-row-capture_run:run-1:main')),
    );
    await tester.pumpAndSettle();

    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-220'));
    await tester.ensureVisible(turn);
    await tester.tap(turn);
    await tester.pumpAndSettle();

    final toggle = find.byKey(const Key('toggle-long-exchange-content'));
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
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.forum_outlined).first);
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('conversation-filter')),
        'reviewer',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(
          const Key('conversation-row-capture_run:run-1:agent:reviewer'),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('reviewer'), findsWidgets);
      expect(find.textContaining('Agent conversation'), findsWidgets);
      expect(find.text('16 turns'), findsOneWidget);
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

      await tester.tap(find.byIcon(Icons.forum_outlined).first);
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('conversation-filter')),
        'capture_run:run-1:main',
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('conversation-row-capture_run:run-1:main')),
      );
      await tester.pumpAndSettle();
      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await tester.ensureVisible(turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();

      final inspect = find.byKey(
        const Key('exchange-environment-run-1-exchange-222'),
      );
      await tester.ensureVisible(inspect);
      expect(find.text('Inspect r7'), findsNothing);
      expect(
        tester.widget<IconButton>(inspect).tooltip,
        'Inspect frozen Environment r7',
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

    await tester.tap(find.byIcon(Icons.forum_outlined).first);
    await tester.pumpAndSettle();
    expect(find.text('对话'.toUpperCase()), findsOneWidget);
    await tester.enterText(
      find.byKey(const Key('conversation-filter')),
      'capture_run:run-1:main',
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('conversation-row-capture_run:run-1:main')),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('vibermate run'), findsOneWidget);

    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-224'));
    await tester.ensureVisible(turn);
    await tester.pumpAndSettle();
    expect(find.text('正在等待最终响应…'), findsOneWidget);

    final rawTurn = find.byKey(
      const Key('conversation-turn-run-1-exchange-222'),
    );
    await tester.ensureVisible(rawTurn);
    await tester.tap(rawTurn);
    await tester.pumpAndSettle();
    final rawSection = find.byKey(const Key('exchange-raw-run-1-exchange-222'));
    final timeline = tester.widget<ListView>(
      find.byKey(const Key('conversation-timeline-scroll')),
    );
    final timelineScroll = timeline.controller!;
    timelineScroll.jumpTo(
      (timelineScroll.offset + 220).clamp(
        0,
        timelineScroll.position.maxScrollExtent,
      ),
    );
    await tester.pumpAndSettle();
    expect(tester.getCenter(rawSection).dy, lessThan(740));
    await tester.tap(rawSection);
    await tester.pumpAndSettle();
    expect(find.text('1 条边界消息'), findsOneWidget);
    final rawReveal = find.byKey(
      const Key('raw-reveal-raw-preview-run-1-exchange-222'),
    );
    await tester.ensureVisible(rawReveal);
    await tester.tap(rawReveal);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('raw-revealed-raw-preview-run-1-exchange-222')),
      findsOneWidget,
    );
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
    await tester.tap(find.text('Figma Desktop').first);
    await tester.pumpAndSettle();
    expect(find.text('Revoke proxy login'), findsOneWidget);
    expect(find.text('Independent exchanges'), findsOneWidget);

    await tester.tap(find.text('Revoke proxy login'));
    await tester.pumpAndSettle();
    expect(find.text('Stop this proxy login?'), findsOneWidget);
    expect(
      find.text(
        'New connections using this login will stop. Conversation and Activity evidence remain available.',
      ),
      findsOneWidget,
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
          'This Environment observes a transparent proxy flow and does not deliver a Root.',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('manual-capture-environment')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Work').last);
      await tester.pumpAndSettle();
      expect(
        find.text(
          'This Environment decrypts selected Agent endpoints. Install the delivered Root only in this client.',
        ),
        findsOneWidget,
      );
      expect(find.text('api.anthropic.com  ·  api.openai.com'), findsOneWidget);

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
    expect(find.text('此 Environment 仅观察透明代理流量，不会交付 Root。'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.enterText(
      find.byKey(const Key('manual-capture-name')),
      '窄屏客户端',
    );
    await tester.tap(find.byKey(const Key('manual-capture-create-confirm')));
    await tester.pumpAndSettle();
    expect(find.text('代理登录已创建'), findsOneWidget);
    expect(find.text('preview-password-1'), findsOneWidget);
    expect(find.text('Root 路径'), findsNothing);
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

      expect(find.text('网络控制'), findsOneWidget);
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

      final nameField = find.byKey(const Key('environment-editor-name'));
      final stateField = find.byKey(const Key('environment-editor-state'));
      final toolField = find.byKey(const Key('environment-editor-tool-mode'));
      final recordingField = find.byKey(
        const Key('environment-editor-recording'),
      );
      final retentionField = find.byKey(
        const Key('environment-editor-retention'),
      );
      for (final field in [
        nameField,
        stateField,
        toolField,
        recordingField,
        retentionField,
      ]) {
        expect(tester.getSize(field).height, ViberMetrics.controlHeight);
      }
      expect(
        (tester.getTopLeft(nameField).dy - tester.getTopLeft(stateField).dy)
            .abs(),
        lessThan(1),
      );
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

      final accountField = find.byKey(
        const Key('environment-route-account-anthropic-direct-anthropic-work'),
      );
      final accountDropdown = tester.widget<CompactSelectField<String>>(
        accountField,
      );
      final accountIds = accountDropdown.items
          .map((item) => item.value)
          .toList(growable: false);
      expect(accountIds, containsAll(['anthropic-work', 'anthropic-lab']));
      expect(accountIds, isNot(contains('orbit-team')));
      expect(accountIds, isNot(contains('openai-work')));
      final accountMenu = tester.widget<MenuAnchor>(
        find.descendant(of: accountField, matching: find.byType(MenuAnchor)),
      );
      expect(
        accountMenu.style?.maximumSize?.resolve({})?.height,
        lessThanOrEqualTo(240),
      );

      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-editor-form')), findsNothing);
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-editor-form')), findsOneWidget);

      await tester.enterText(
        find.byKey(const Key('environment-editor-name')),
        'Work reviewed',
      );
      await tester.tap(find.byKey(const Key('environment-editor-tool-mode')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Ask before tools run').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-editor-recording')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Metadata only').last);
      await tester.pumpAndSettle();
      await tester.ensureVisible(accountField);
      await tester.pumpAndSettle();
      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Anthropic · Lab').last);
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-impact-review')),
        findsOneWidget,
      );
      expect(find.text('Hot switch'), findsWidgets);
      expect(find.text('6 RUNNING CAPTURES'), findsOneWidget);
      expect(find.text('r7'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.text('Work reviewed'), findsWidgets);
      expect(find.text('Revision 8'), findsOneWidget);
      expect(
        find.text(
          'Environment published from the reviewed draft and impact boundary.',
        ),
        findsOneWidget,
      );
      expect(find.text('Anthropic · Lab'), findsOneWidget);
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
    expect(find.text('每条 Route 只能选择其 Endpoint 所属的账号。'), findsOneWidget);

    await tester.enterText(
      find.byKey(const Key('environment-editor-name')),
      '工作环境',
    );
    await tester.tap(find.byKey(const Key('environment-review')));
    await tester.pumpAndSettle();
    expect(find.text('发布前的运行影响'), findsOneWidget);
    expect(find.text('6 个运行中 CAPTURE'), findsOneWidget);
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

  testWidgets('new observation-only Environment is published at revision one', (
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
    final createToolMode = find.byKey(
      const Key('environment-create-tool-mode'),
    );
    final createRecording = find.byKey(
      const Key('environment-create-recording'),
    );
    for (final field in [
      find.byKey(const Key('environment-create-name')),
      find.byKey(const Key('environment-create-id')),
      createToolMode,
      createRecording,
      find.byKey(const Key('environment-create-retention')),
    ]) {
      expect(tester.getSize(field).height, ViberMetrics.controlHeight);
    }
    expect(
      find.byKey(const Key('environment-endpoint-catalog')),
      findsOneWidget,
    );
    expect(
      find.text(
        'Observation-only Environment. No semantic client Endpoint is configured.',
      ),
      findsOneWidget,
    );
    await tester.enterText(
      find.byKey(const Key('environment-create-name')),
      'Local Observe',
    );
    expect(find.text('local-observe'), findsOneWidget);

    await tester.tap(find.byKey(const Key('environment-create-review')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-impact')), findsOneWidget);
    expect(find.text('0 RUNNING CAPTURES'), findsOneWidget);
    await tester.tap(find.byKey(const Key('environment-create-publish')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('environment-row-local-observe')),
      findsOneWidget,
    );
    expect(find.text('Revision 1'), findsOneWidget);
    expect(
      find.text(
        'This Environment changes policy and evidence settings while leaving semantic routing transparent.',
      ),
      findsOneWidget,
    );
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
        orderedEquals(['orbit-team']),
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
      expect(accountIds, containsAll(['anthropic-work', 'anthropic-lab']));
      expect(accountIds, isNot(contains('openai-work')));
      expect(accountIds, isNot(contains('orbit-team')));

      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Anthropic · Work').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();
      expect(find.text('Anthropic · Work'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(find.text('Hot switch'), findsWidgets);
      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.text('Revision 4'), findsOneWidget);
      expect(find.text('2 upstream routes'), findsOneWidget);
      expect(find.text('Default'), findsOneWidget);
      expect(find.text('Candidate'), findsOneWidget);
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

  testWidgets(
    'Endpoint-owned Account can be created, rotated, and safely deleted',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byIcon(Icons.hub_outlined).first);
      await tester.pumpAndSettle();

      final addAccount = find.byKey(const Key('accounts-add'));
      expect(1180 - tester.getTopRight(addAccount).dx, lessThanOrEqualTo(14));
      await tester.tap(find.byKey(const Key('endpoints-add')));
      await tester.pumpAndSettle();
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
      await tester.enterText(
        find.byKey(const Key('endpoint-editor-name')),
        'Team Relay',
      );
      await tester.enterText(
        find.byKey(const Key('endpoint-editor-origin')),
        'https://relay.team.example',
      );
      await tester.tap(find.byKey(const Key('endpoint-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Upstream Endpoint created. Accounts can now be added to it.',
        ),
        findsOneWidget,
      );
      expect(find.text('Team Relay'), findsWidgets);

      await tester.tap(find.byKey(const Key('accounts-add')));
      await tester.pumpAndSettle();
      expect(find.text('Team Relay'), findsWidgets);
      await tester.enterText(
        find.byKey(const Key('account-editor-name')),
        'Team Primary',
      );
      await tester.enterText(
        find.byKey(const Key('account-editor-secret')),
        'sk-ant-preview-one',
      );
      await tester.tap(find.byKey(const Key('account-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text('Account created under the selected upstream Endpoint.'),
        findsOneWidget,
      );
      expect(find.text('Team Primary'), findsOneWidget);

      await tester.tap(find.byIcon(Icons.key_outlined));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('account-editor-secret')),
        'sk-ant-preview-two',
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

      await tester.tap(find.byIcon(Icons.delete_outline));
      await tester.pumpAndSettle();
      expect(find.text('Delete Team Primary?'), findsOneWidget);
      await tester.tap(find.byKey(const Key('account-delete-confirm')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Account and credential deleted. Captured evidence was not removed.',
        ),
        findsOneWidget,
      );
      expect(find.text('Team Primary'), findsNothing);
      expect(find.text('No accounts yet'), findsOneWidget);
      expect(find.text('Accounts belong to this Endpoint.'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('referenced Account deletion shows exact blocking routes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byIcon(Icons.hub_outlined).first);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('account-delete-anthropic-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('account-delete-confirm')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'The runtime refused deletion because Environment routes still reference this Account.',
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
  await tester.tap(find.byIcon(Icons.security_outlined));
  await tester.pumpAndSettle();
  expect(find.byKey(const Key('network-tab-approvals')), findsOneWidget);
}

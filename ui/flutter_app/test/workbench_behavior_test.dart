import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
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
    // Retiring Conversations moved every later section up one slot.
    expect(find.byKey(const Key('environment-master-pane')), findsOneWidget);

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
    expect(find.text('Terminal command'), findsNothing);
    expect(
      find.textContaining('vibermate login --server server.local:9666'),
      findsOneWidget,
    );
    expect(
      find.textContaining('vibermate run --server server.local:9666 -- codex'),
      findsOneWidget,
    );
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
    expect(find.byKey(const Key('usage-total-turns')), findsOneWidget);
    expect(find.byKey(const Key('usage-active-runs')), findsOneWidget);
    expect(find.byKey(const Key('usage-input-tokens')), findsOneWidget);
    expect(find.byKey(const Key('usage-output-tokens')), findsOneWidget);
    expect(find.byKey(const Key('usage-ranking')), findsOneWidget);
    expect(find.byKey(const Key('usage-ranking-scroll')), findsOneWidget);
    expect(find.byKey(const Key('usage-ranking-count')), findsOneWidget);
    expect(
      find.byKey(const Key('usage-user-user.preview.alice')),
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
      find.byKey(const Key('usage-user-user.preview.alice')),
      findsNothing,
    );
    await tester.enterText(find.byKey(const Key('usage-user-search')), '');
    await tester.pump();
    await tester.tap(find.byKey(const Key('usage-user-user.preview.alice')));
    await tester.pumpAndSettle();
    expect(
      tester
          .getTopLeft(
            find.byKey(const Key('usage-user-evidence-user.preview.alice')),
          )
          .dy,
      lessThan(300),
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
        matching: find.textContaining('2 Captures · 12 turns'),
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
    await tester.drag(
      find.byKey(const Key('usage-dashboard-scroll')),
      const Offset(0, -700),
    );
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
    'Capture freezes its launch Environment for the whole managed run',
    (tester) async {
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
      expect(find.text('1 boundary messages'), findsOneWidget);
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

    await openCaptureConversation(tester, capture: 'managed_run:run-1');
    expect(find.textContaining('vibermate run'), findsOneWidget);

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
    expect(find.text('1 条边界消息'), findsOneWidget);
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
          'Environment published from the reviewed draft and impact boundary.',
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
        find.textContaining('Endpoint model discovery unavailable.'),
        findsOneWidget,
      );
      expect(
        find.textContaining('Endpoint rejected this Account authentication'),
        findsOneWidget,
      );
      expect(
        find.textContaining('This Account sends X-Api-Key.'),
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
      find.text(
        '每条客户端流量可直连原始目标，也可改发到一个或多个上游 Endpoint；每条 Route 只能使用其 Endpoint 所属的账号。',
      ),
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
    expect(find.text('6 个运行中 CAPTURE 保持当前修订'), findsOneWidget);
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
      find.byKey(const Key('environment-destination-kind')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('environment-endpoint-catalog')), findsNothing);
    expect(
      find.text(
        'Capture-only Environment. ViberMate records traffic and forwards it to each client’s original destination.',
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
    await tester.tap(
      find.byKey(
        const Key(
          'environment-client-plan-option-anthropic_messages-https://api.anthropic.com',
        ),
      ),
    );
    await tester.pumpAndSettle();

    final destination = find.byKey(const Key('environment-destination-kind'));
    expect(destination, findsOneWidget);
    expect(tester.getTopLeft(destination).dx, greaterThanOrEqualTo(0));
    expect(tester.getBottomRight(destination).dx, lessThanOrEqualTo(390));
    expect(find.text('直连原服务'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('environment-create-form')),
        matching: find.textContaining('ViberMate 仍会抓包'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('environment-endpoint-catalog')), findsNothing);

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
    await tester.tap(
      find.byKey(
        const Key(
          'environment-client-plan-option-anthropic_messages-https://api.anthropic.com',
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(
        of: find.byKey(const Key('environment-destination-kind')),
        matching: find.text('Upstream Endpoint'),
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

    await tester.tap(clientFlow);
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(
        const Key(
          'environment-client-plan-option-openai_responses-https://api.openai.com',
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(
        of: find.byKey(const Key('environment-destination-kind')),
        matching: find.text('Upstream Endpoint'),
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
      await tester.tap(
        find.byKey(
          const Key(
            'environment-client-plan-option-anthropic_messages-https://api.anthropic.com',
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byKey(const Key('environment-destination-kind')),
          matching: find.text('Upstream Endpoint'),
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
      expect(find.text('Anthropic · Work'), findsWidgets);

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
      expect(find.text('Anthropic · Work'), findsWidgets);
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
          'Upstream Endpoint created. Accounts can now be added to it.',
        ),
        findsOneWidget,
      );
      expect(find.text('Team Relay'), findsWidgets);

      await tester.tap(find.byKey(const Key('accounts-add')));
      await tester.pumpAndSettle();
      expect(find.text('Team Relay'), findsWidgets);
      expect(
        tester
            .widget<Text>(
              find.byKey(const Key('account-editor-auth-transport')),
            )
            .data,
        'X-Api-Key',
      );
      await tester.tap(find.byKey(const Key('account-editor-kind')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Bearer token').last);
      await tester.pumpAndSettle();
      expect(
        tester
            .widget<Text>(
              find.byKey(const Key('account-editor-auth-transport')),
            )
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
      await tester.tap(find.byKey(const Key('account-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text('Account created under the selected upstream Endpoint.'),
        findsOneWidget,
      );
      expect(find.text('Team Primary'), findsOneWidget);
      expect(
        find.textContaining('Anthropic API key · X-Api-Key'),
        findsOneWidget,
      );

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

  testWidgets('390px private HTTP Endpoint editor remains usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byIcon(Icons.hub_outlined).first);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('endpoints-add')));
    await tester.pumpAndSettle();
    for (final protocol in const [
      'anthropic_messages',
      'openai_responses',
      'openai_chat',
    ]) {
      final option = find.byKey(Key('endpoint-editor-protocol-$protocol'));
      expect(option, findsOneWidget);
      expect(tester.widget<CheckboxListTile>(option).value, isFalse);
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

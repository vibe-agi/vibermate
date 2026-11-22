import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  Future<WorkbenchController> open(
    WidgetTester tester, {
    AppLanguage language = AppLanguage.english,
  }) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
      initialPreferences: WorkbenchPreferences(language: language),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump(const Duration(milliseconds: 300));
    return controller;
  }

  Future<void> close(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
    await tester.pump();
  }

  testWidgets('a filter that matches nothing says so, never "No sessions"', (
    tester,
  ) async {
    final controller = await open(tester);
    expect(controller.data!.captureNextCursor, isNull);
    expect(find.text('Filter by agent or project'), findsOneWidget);

    await tester.enterText(find.byKey(const Key('capture-filter')), 'zzz');
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('capture-filter-no-match')), findsOneWidget);
    expect(find.text('No sessions match “zzz”.'), findsOneWidget);
    expect(
      find.text(
        'The filter looks at agent names, project folders and devices.',
      ),
      findsOneWidget,
      reason: 'everything is loaded, so there is no load-older hint',
    );
    expect(find.text('No sessions yet'), findsNothing);
    expect(find.text('Open Terminal setup'), findsNothing);
    expect(find.byKey(const Key('setup-guide')), findsNothing);
    expect(find.text('Clear filter'), findsOneWidget);
    expect(find.text('Search requests for “zzz”'), findsOneWidget);

    // Search opens with the text filled in and already searched.
    await tester.tap(find.byKey(const Key('capture-filter-search-requests')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    final query = find.descendant(
      of: find.byType(Dialog),
      matching: find.byType(TextField),
    );
    expect(query, findsWidgets);
    expect(tester.widget<TextField>(query.first).controller!.text, 'zzz');
    await tester.tapAt(const Offset(4, 4));
    await tester.pump(const Duration(milliseconds: 300));

    await tester.tap(find.byKey(const Key('capture-filter-clear')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('capture-filter-no-match')), findsNothing);
    expect(
      find.byKey(Key('capture-row-${controller.data!.captures.first.key}')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('the no-match state reads in Chinese', (tester) async {
    final controller = await open(
      tester,
      language: AppLanguage.simplifiedChinese,
    );
    await tester.enterText(find.byKey(const Key('capture-filter')), 'zzz');
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('没有符合“zzz”的运行记录。'), findsOneWidget);
    expect(find.text('清除筛选'), findsOneWidget);
    expect(find.text('在请求记录中搜索“zzz”'), findsOneWidget);
    expect(find.text('按 Agent 或项目筛选'), findsOneWidget);
    await close(tester, controller);
  });
}

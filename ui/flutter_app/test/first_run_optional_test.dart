import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/routing_set_name.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';

void main() {
  Future<WorkbenchController> open(
    WidgetTester tester, {
    PreviewFreshStage stage = PreviewFreshStage.start,
    AppLanguage language = AppLanguage.english,
  }) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewFixture.freshInstall(stage: stage);
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      closeRuntime: fixture.api.close,
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

  test('only the built-in set gets the localized name', () async {
    final english = AppCopy.forLanguage(AppLanguage.english);
    final chinese = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
    final api = PreviewControlApi();
    addTearDown(api.close);
    final environments = (await api.loadDashboard()).environments;
    final builtIn = environments.singleWhere((value) => value.systemOwned);
    expect(builtIn.id, builtInRoutingSetId);
    expect(builtIn.name, 'System Transparent', reason: 'stored name unchanged');
    expect(environmentDisplayName(builtIn, english), 'Forward as-is');
    expect(environmentDisplayName(builtIn, chinese), '原样转发');
    final work = environments.singleWhere((value) => value.id == 'work');
    expect(environmentDisplayName(work, english), 'Work');
    expect(
      routingSetDisplayName(
        'system_transparent',
        'System Transparent',
        english,
      ),
      'Forward as-is',
    );
    expect(routingSetDisplayName('work', 'Work', english), 'Work');
    expect(routingSetDisplayName('work', null, english), 'work');
  });

  testWidgets('Accounts says an account is not needed to start', (
    tester,
  ) async {
    final controller = await open(tester);
    controller.selectSection(WorkbenchSection.providerAccounts);
    await tester.pump(const Duration(milliseconds: 300));

    expect(find.text('No accounts yet'), findsOneWidget);
    expect(find.text('Add your first upstream account'), findsNothing);
    expect(
      find.textContaining(
        "You don't need one to start: Claude Code and Codex keep using their "
        'own sign-in.',
      ),
      findsOneWidget,
    );
    expect(
      find.textContaining('Accounts are used through a routing set'),
      findsOneWidget,
    );
    expect(
      find.ancestor(
        of: find.text('Add upstream account'),
        matching: find.byWidgetPredicate((widget) => widget is OutlinedButton),
      ),
      findsOneWidget,
      reason: 'the body button stays outlined; the header keeps the teal one',
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('Routing names the built-in set and says when it is used', (
    tester,
  ) async {
    final controller = await open(tester);
    controller.selectSection(WorkbenchSection.environments);
    await tester.pump(const Duration(milliseconds: 300));

    expect(find.byKey(const Key('environment-title')), findsOneWidget);
    expect(
      tester.widget<Text>(find.byKey(const Key('environment-title'))).data,
      'Forward as-is',
    );
    expect(find.text('System Transparent'), findsNothing);
    expect(
      find.text(
        'Used when you start an agent without --env. Each agent keeps its '
        'own service and login, and its requests are recorded.',
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: find.byKey(const Key('environment-row-system_transparent')),
        matching: find.text('Forward as-is'),
      ),
      findsOneWidget,
    );
    await close(tester, controller);
  });

  testWidgets('the session header and proxy login use the same name', (
    tester,
  ) async {
    final controller = await open(
      tester,
      stage: PreviewFreshStage.waiting,
      language: AppLanguage.simplifiedChinese,
    );
    final capture = controller.data!.captures.single;
    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    expect(
      find.descendant(
        of: find.byKey(const Key('capture-environment-readonly')),
        matching: find.text('原样转发'),
      ),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('manual-capture-create')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    expect(
      find.descendant(
        of: find.byKey(const Key('manual-capture-environment')),
        matching: find.text('原样转发'),
      ),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('manual-capture-environment-description')),
      findsOneWidget,
    );
    expect(find.text('System Transparent'), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });
}

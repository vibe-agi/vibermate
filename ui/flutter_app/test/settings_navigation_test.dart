import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

Future<void> tapVisible(WidgetTester tester, Finder target) async {
  await tester.ensureVisible(target);
  await tester.pumpAndSettle();
  await tester.tap(target);
  await tester.pumpAndSettle();
}

Future<WorkbenchController> mountSettings(
  WidgetTester tester, {
  required bool server,
  required bool terminal,
  bool chinese = false,
  bool dark = false,
  PreviewControlApi? api,
}) async {
  final runtime = api ?? PreviewControlApi();
  final controller = WorkbenchController(
    api: runtime,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: false,
    serverManagement: server,
    terminalManagement: terminal,
    closeRuntime: runtime.close,
    initialPreferences: WorkbenchPreferences(
      section: WorkbenchSection.settings,
      language: chinese ? AppLanguage.simplifiedChinese : AppLanguage.english,
    ),
  );
  addTearDown(controller.dispose);
  await controller.initialize();
  await tester.pumpWidget(
    MaterialApp(
      theme: dark ? ViberTheme.dark() : ViberTheme.light(),
      home: WorkbenchShell(controller: controller),
    ),
  );
  await tester.pumpAndSettle();
  return controller;
}

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;

  for (final server in [false, true]) {
    for (final terminal in [false, true]) {
      testWidgets('Settings tabs match capabilities ($server, $terminal)', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(1200, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final controller = await mountSettings(
          tester,
          server: server,
          terminal: terminal,
        );
        final keys = [
          'general',
          if (server || terminal) 'access',
          if (server) 'users',
          'safety',
          'proxy',
        ];
        final tabs = tester.widget<DefaultTabController>(
          find.descendant(
            of: find.byType(SettingsView),
            matching: find.byType(DefaultTabController),
          ),
        );
        expect(tabs.length, keys.length);
        expect(
          find.byKey(const Key('settings-tab-users')),
          server ? findsOneWidget : findsNothing,
        );
        for (var index = 0; index < keys.length; index++) {
          await tapVisible(
            tester,
            find.byKey(Key('settings-tab-${keys[index]}')),
          );
          expect(controller.settingsTab, index);
          expect(tester.takeException(), isNull);
        }
        controller.selectSettingsTab(keys.length);
        controller.selectSettingsTab(-1);
        expect(controller.settingsTab, keys.length - 1);

        controller.openRuntimeUsersSettings();
        await tester.pumpAndSettle();
        expect(controller.settingsTab, server ? 2 : keys.length - 1);
        expect(
          find.byKey(const Key('runtime-users-panel')),
          server ? findsOneWidget : findsNothing,
        );
        controller.openAccessSettings();
        await tester.pumpAndSettle();
        expect(
          controller.settingsTab,
          server || terminal ? 1 : keys.length - 1,
        );
        expect(find.byKey(const Key('runtime-users-panel')), findsNothing);
        expect(
          find.byKey(const Key('server-runtime-access')),
          server ? findsOneWidget : findsNothing,
        );
        expect(
          find.byKey(const Key('terminal-command-panel')),
          terminal ? findsOneWidget : findsNothing,
        );
        expect(tester.takeException(), isNull);
        controller.dispose();
        await tester.pumpWidget(const SizedBox.shrink());
      });
    }
  }

  for (final chinese in [false, true]) {
    for (final dark in [false, true]) {
      testWidgets('390px Users and Access links ($chinese, $dark)', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(390, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final controller = await mountSettings(
          tester,
          server: true,
          terminal: true,
          chinese: chinese,
          dark: dark,
        );
        await tapVisible(tester, find.byKey(const Key('settings-tab-access')));
        await tapVisible(
          tester,
          find.byKey(const Key('settings-access-manage-users')),
        );
        expect(controller.settingsTab, 2);
        expect(find.byKey(const Key('runtime-users-panel')), findsOneWidget);
        expect(find.byKey(const Key('terminal-command-panel')), findsNothing);
        expect(find.byKey(const Key('server-runtime-access')), findsNothing);

        controller.selectSection(WorkbenchSection.captures);
        await tester.pumpAndSettle();
        controller.selectSection(WorkbenchSection.settings);
        await tester.pumpAndSettle();
        expect(controller.settingsTab, 2);
        expect(find.byKey(const Key('runtime-users-panel')), findsOneWidget);

        await tapVisible(
          tester,
          find.byKey(const Key('settings-users-open-access')),
        );
        expect(controller.settingsTab, 1);
        expect(find.byKey(const Key('server-runtime-access')), findsOneWidget);
        expect(find.byKey(const Key('runtime-users-panel')), findsNothing);
        expect(tester.takeException(), isNull);
        controller.dispose();
        await tester.pumpWidget(const SizedBox.shrink());
      });
    }
  }

  testWidgets('wide Settings panes start at the left content inset', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1800, 1000));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final controller = await mountSettings(
      tester,
      server: true,
      terminal: true,
    );
    for (final pane in [
      (tab: 'access', panel: 'terminal-command-panel'),
      (tab: 'users', panel: 'runtime-users-panel'),
    ]) {
      await tapVisible(tester, find.byKey(Key('settings-tab-${pane.tab}')));
      final scroll = tester.getRect(
        find.byKey(Key('settings-${pane.tab}-scroll')),
      );
      final panel = tester.getRect(find.byKey(Key(pane.panel)));
      expect(panel.left, closeTo(scroll.left + 14, 0.01));
      expect(panel.width, lessThanOrEqualTo(1080));
    }
    expect(tester.takeException(), isNull);
    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('User management resets, disables and re-enables a member', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(900, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final member = await api.createRuntimeUser(
      username: 'bob',
      password: 'test-password',
    );
    final controller = await mountSettings(
      tester,
      server: true,
      terminal: true,
      api: api,
    );
    await tapVisible(tester, find.byKey(const Key('settings-tab-users')));
    await tapVisible(
      tester,
      find.byKey(Key('runtime-user-reset-${member.id}')),
    );
    await tester.enterText(
      find.byKey(const Key('runtime-user-new-password')),
      'replacement-password',
    );
    await tester.enterText(
      find.byKey(const Key('runtime-user-confirm-password')),
      'replacement-password',
    );
    await tester.pump();
    await tapVisible(
      tester,
      find.byKey(const Key('runtime-user-password-save')),
    );
    expect(find.byKey(const Key('runtime-user-password-dialog')), findsNothing);

    final row = find.byKey(Key('runtime-user-row-${member.id}'));
    await tapVisible(
      tester,
      find.descendant(of: row, matching: find.byTooltip('Disable')),
    );
    expect(find.text('Disable this runtime user?'), findsOneWidget);
    await tapVisible(tester, find.widgetWithText(FilledButton, 'Disable'));
    expect(
      controller.runtimeUsers!
          .firstWhere((user) => user.id == member.id)
          .active,
      isFalse,
    );
    expect(find.byKey(Key('runtime-user-reset-${member.id}')), findsNothing);
    await tapVisible(
      tester,
      find.descendant(
        of: row,
        matching: find.byTooltip('Enable · sign in again to reconnect'),
      ),
    );
    expect(
      controller.runtimeUsers!
          .firstWhere((user) => user.id == member.id)
          .active,
      isTrue,
    );
    expect(find.byKey(Key('runtime-user-reset-${member.id}')), findsOneWidget);
    expect(tester.takeException(), isNull);
    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
  });
}

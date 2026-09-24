import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/api/control_models.dart';
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

Future<void> openSettingsTab(WidgetTester tester, String tab) async {
  final picker = find.byKey(const Key('settings-section-picker'));
  if (picker.evaluate().isEmpty) {
    await tapVisible(tester, find.byKey(Key('settings-tab-$tab')));
    return;
  }
  await tester.tap(picker);
  await tester.pumpAndSettle();
  final option = switch (tab) {
    'general' => 'preferences',
    'proxy' => 'networkExits',
    _ => tab,
  };
  final label = tester.widget<SettingsView>(find.byType(SettingsView)).copy(
    switch (option) {
      'preferences' => 'settings.tab.preferences',
      'access' => 'settings.tab.access',
      'users' => 'settings.tab.users',
      'safety' => 'settings.tab.safety',
      _ => 'settings.tab.proxy',
    },
  );
  await tester.tap(find.text(label).hitTestable().last);
  await tester.pumpAndSettle();
}

Future<WorkbenchController> mountSettings(
  WidgetTester tester, {
  required bool server,
  required bool terminal,
  bool chinese = false,
  bool dark = false,
  bool rootTrust = false,
  String target = 'This Mac',
  PreviewControlApi? api,
}) async {
  final runtime = api ?? PreviewControlApi();
  final controller = WorkbenchController(
    api: runtime,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: false,
    serverManagement: server,
    terminalManagement: terminal,
    rootTrustManagement: rootTrust,
    runtimeTarget: target,
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
        await openSettingsTab(tester, 'access');
        expect(find.byKey(const Key('server-runtime-access')), findsNothing);
        await tapVisible(
          tester,
          find.byKey(const ValueKey('settings-remote-guide-false')),
        );
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

  testWidgets('Web guide uses its connected DNS origin and HTTPS state', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final controller = await mountSettings(
      tester,
      server: true,
      terminal: false,
      target: 'https://runtime.example.test:8443',
    );
    await openSettingsTab(tester, 'access');
    expect(
      find.text('vibermate login --server https://runtime.example.test:8443'),
      findsOneWidget,
    );
    expect(find.text('https://runtime.example.test:8443/'), findsOneWidget);
    expect(find.text('TLS'), findsOneWidget);
    expect(find.text('HTTP'), findsNothing);
    expect(find.byKey(const Key('terminal-command-panel')), findsNothing);
    expect(find.byKey(const Key('runtime-users-panel')), findsNothing);
    expect(tester.takeException(), isNull);
    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final rootTrust in [false, true]) {
    testWidgets('Safety separates HTTPS from one Proxy CA panel ($rootTrust)', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final controller = await mountSettings(
        tester,
        server: true,
        terminal: rootTrust,
        rootTrust: rootTrust,
        target: 'https://runtime.example.test:9666',
      );
      await openSettingsTab(tester, 'safety');
      expect(
        find.byKey(const Key('server-connection-settings-panel')),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('root-ca-settings-panel')),
        rootTrust ? findsOneWidget : findsNothing,
      );
      expect(
        find.byKey(const Key('runtime-root-ca-settings-panel')),
        rootTrust ? findsNothing : findsOneWidget,
      );
      expect(find.byKey(const Key('runtime-root-ca-download')), findsNothing);
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  testWidgets('Safety reports automatic HTTPS lifecycle without another tab', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final controller = await mountSettings(
      tester,
      server: true,
      terminal: false,
      target: 'https://runtime.example.test:9666',
    );
    controller.serverAccess = const RuntimeServerAccess(
      transport: 'https',
      authentication: 'runtime_user_password',
      sessionPolicy: 'reusable_until_logout_disable_or_expiry',
      targets: ['runtime.example.test:9666'],
      tls: RuntimeServerTLS(
        mode: 'automatic_tls',
        state: 'renewing',
        serverName: 'runtime.example.test',
        challenge: 'tls_alpn_01',
        issuer: 'Example Public CA',
        notAfter: '2026-12-01T00:00:00Z',
      ),
    );
    await openSettingsTab(tester, 'safety');
    expect(find.textContaining('renewing the certificate'), findsOneWidget);
    await tapVisible(
      tester,
      find.byKey(const Key('server-connection-details')),
    );
    expect(find.byKey(const Key('server-tls-details')), findsOneWidget);
    expect(find.text('Automatic public HTTPS'), findsOneWidget);
    expect(find.text('TLS-ALPN-01 (port 443)'), findsOneWidget);
    expect(find.text('Example Public CA'), findsOneWidget);
    expect(tester.takeException(), isNull);
    controller.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final local in [false, true]) {
    testWidgets('HTTP warning matches address scope ($local)', (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final controller = await mountSettings(
        tester,
        server: true,
        terminal: false,
        target: local ? 'http://127.0.0.1:9666' : 'http://192.0.2.10:9666',
      );
      await openSettingsTab(tester, 'access');
      final notices = tester.widgetList<InlineNotice>(
        find.descendant(
          of: find.byKey(const Key('server-runtime-access')),
          matching: find.byType(InlineNotice),
        ),
      );
      expect(notices, hasLength(1));
      expect(notices.single.error, !local);
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  testWidgets('Missing access data does not display made-up commands', (
    tester,
  ) async {
    final controller = await mountSettings(
      tester,
      server: true,
      terminal: false,
    );
    controller.serverAccess = null;
    await openSettingsTab(tester, 'access');
    expect(find.textContaining('vibermate login --server'), findsNothing);
    expect(find.textContaining('http://This Mac'), findsNothing);
    expect(find.text('Retry'), findsOneWidget);
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

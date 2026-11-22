import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/root_trust_installer_contract.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/egress_profile_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;

  Future<WorkbenchController> open(
    WidgetTester tester, {
    Size size = const Size(1440, 900),
    PreviewFreshStage stage = PreviewFreshStage.command,
    AppLanguage language = AppLanguage.english,
    bool server = true,
    String runtimeTarget = 'This Mac',
    RootTrustInstaller Function(PreviewControlApi api)? installer,
  }) async {
    await tester.binding.setSurfaceSize(size);
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewFixture.freshInstall(stage: stage);
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      serverManagement: server,
      runtimeTarget: runtimeTarget,
      rootTrustManagement: true,
      rootTrustInstaller:
          installer?.call(fixture.api) ??
          PreviewRootTrustInstaller(fixture.api),
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
    await tester.pump();
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

  Future<void> settle(WidgetTester tester) async {
    for (var frame = 0; frame < 6; frame++) {
      await tester.pump(const Duration(milliseconds: 100));
    }
  }

  testWidgets('Open Terminal setup leads with starting an agent', (
    tester,
  ) async {
    final controller = await open(tester, size: const Size(1024, 768));
    // The guide hidden, the empty list offers Terminal setup.
    await tester.tap(find.byKey(const Key('setup-hide')));
    await settle(tester);
    await tester.tap(
      find.byKey(const Key('capture-empty-open-terminal-settings')),
    );
    await settle(tester);

    expect(controller.section, WorkbenchSection.settings);
    expect(controller.remoteConnectionGuideRequested, isFalse);
    // On the first screen: the Terminal command and both run commands.
    for (final key in const [
      'terminal-command-panel',
      'managed-run-command-claude',
      'managed-run-command-codex',
    ]) {
      final rect = tester.getRect(find.byKey(Key(key)));
      expect(rect.top, greaterThanOrEqualTo(0), reason: key);
      expect(rect.bottom, lessThanOrEqualTo(768), reason: key);
    }
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    expect(find.text('vibermate run -- codex'), findsOneWidget);
    // Said once: the section line says where the agent's requests appear,
    // and the step's title where to run the commands.
    expect(
      find.text('In Terminal, go to your project folder and run one of these:'),
      findsNothing,
    );
    expect(
      find.text('2. Copy a command and run it in your project folder'),
      findsNothing,
    );
    expect(
      find.text('3. Copy a command and run it in your project folder'),
      findsOne,
    );
    expect(find.byKey(const Key('access-launch-detail')), findsNothing);
    expect(
      find.text('Using ViberMate on this Mac needs no sign-in or domain.'),
      findsOne,
    );
    // A shell alias is not enough: the agent must start by its name.
    expect(
      find.text(
        'Claude Code or Codex must already be installed and start when you '
        'type claude or codex in Terminal.',
      ),
      findsOneWidget,
    );
    // Codex's certificate is its own step, between the command and the
    // launch commands.
    expect(find.byKey(const Key('access-codex-requirement')), findsOneWidget);
    expect(
      tester.getRect(find.byKey(const Key('access-codex-requirement'))).top,
      lessThan(
        tester.getRect(find.byKey(const Key('managed-run-command-claude'))).top,
      ),
    );
    // Server, browser and proxy details are folded under the other ways.
    expect(find.text('Other ways to connect'), findsOneWidget);
    expect(find.byKey(const Key('server-runtime-access')), findsNothing);
    expect(find.byKey(const Key('server-deployment-guide')), findsNothing);
    expect(find.text('SOCKS5 proxy'), findsNothing);
    expect(find.text('Availability unknown'), findsNothing);
    expect(find.text('VLESS engine availability unknown'), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('before the command is set up, the page says so once', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.start);
    await tester.tap(find.byKey(const Key('setup-hide')));
    await settle(tester);
    controller.openAccessSettings();
    await settle(tester);
    expect(find.text('1. Set up the vibermate command'), findsOne);
    expect(
      find.text('Set it up once, then copy an Agent launch command below.'),
      findsOne,
    );
    expect(
      find.text(
        'Set up the vibermate command first, then copy a command below.',
      ),
      findsNothing,
    );
    expect(
      tester
          .widget<TextButton>(find.byKey(const Key('managed-run-copy-claude')))
          .onPressed,
      isNull,
    );
    await close(tester, controller);
  });

  testWidgets('trusting the certificate here is confirmed in place', (
    tester,
  ) async {
    final controller = await open(tester);
    controller.openAccessSettings();
    await settle(tester);
    final button = find.byKey(const Key('access-install-trust'));
    await tester.ensureVisible(button);
    await settle(tester);
    await tester.tap(button);
    await settle(tester);
    await tester.tap(find.byKey(const Key('root-ca-install-continue')));
    await settle(tester);
    // Step 2 stays, ticked, and the steps keep their numbers.
    final step = find.byKey(const Key('access-step-trust'));
    expect(step, findsOne);
    expect(
      find.descendant(
        of: step,
        matching: find.byWidgetPredicate(
          (widget) =>
              widget is StatusPill && widget.label == 'Installed and trusted',
        ),
      ),
      findsOne,
    );
    expect(
      find.descendant(
        of: step,
        matching: find.text("Codex: this Mac trusts ViberMate's certificate."),
      ),
      findsOne,
    );
    expect(find.byKey(const Key('access-install-trust')), findsNothing);
    expect(
      find.text('3. Copy a command and run it in your project folder'),
      findsOne,
    );
    await close(tester, controller);
  });

  testWidgets('a cancelled macOS prompt says so on the page', (tester) async {
    final controller = await open(
      tester,
      installer: (_) => const _RefusingInstaller(),
    );
    controller.openAccessSettings();
    await settle(tester);
    final button = find.byKey(const Key('access-install-trust'));
    await tester.ensureVisible(button);
    await settle(tester);
    await tester.tap(button);
    await settle(tester);
    await tester.tap(find.byKey(const Key('root-ca-install-continue')));
    await settle(tester);
    final error = find.byKey(const Key('access-root-ca-error'));
    expect(error, findsOne);
    expect(
      find.descendant(
        of: find.byKey(const Key('access-step-trust')),
        matching: error,
      ),
      findsOne,
    );
    expect(find.byKey(const Key('access-install-trust')), findsOne);
    await close(tester, controller);
  });

  testWidgets('a link to the remote guide brings it into view', (tester) async {
    final controller = await open(tester, size: const Size(1440, 600));
    controller.openAccessSettings(focus: AccessFocus.remote);
    await settle(tester);
    final guide = tester.getRect(
      find.byKey(const ValueKey('settings-remote-guide-true')),
    );
    expect(guide.top, greaterThanOrEqualTo(0));
    expect(guide.top, lessThan(600));
    // The page scrolled to it: it starts far below the first screen.
    final scroll = tester
        .state<ScrollableState>(
          find
              .descendant(
                of: find.byKey(const Key('settings-access-scroll')),
                matching: find.byType(Scrollable),
              )
              .first,
        )
        .position;
    expect(scroll.pixels, greaterThan(0));
    await close(tester, controller);
  });

  testWidgets('Technical details says what the SOCKS5 listener is for', (
    tester,
  ) async {
    final controller = await open(tester);
    controller.openAccessSettings();
    await settle(tester);
    final toggle = find.byKey(const Key('access-technical-details-toggle'));
    await tester.ensureVisible(toggle);
    await settle(tester);
    await tester.tap(toggle);
    await settle(tester);
    expect(
      find.text(
        'For apps you point at ViberMate by hand with a proxy login. Claude '
        "Code and Codex don't need it.",
      ),
      findsOne,
    );
    expect(
      tester.getRect(find.byKey(const Key('access-socks-purpose'))).top,
      lessThan(tester.getRect(find.text('SOCKS5 proxy')).top),
    );
    await close(tester, controller);
  });

  testWidgets('every section is still on the page, folded away', (
    tester,
  ) async {
    final controller = await open(tester);
    controller.openAccessSettings();
    await settle(tester);
    final scroll = find
        .descendant(
          of: find.byKey(const Key('settings-access-scroll')),
          matching: find.byType(Scrollable),
        )
        .first;
    final editor = find.byKey(const Key('access-editor-section'));
    await tester.scrollUntilVisible(editor, 200, scrollable: scroll);
    expect(find.text('Use from an editor (optional)'), findsOneWidget);
    expect(find.byKey(const Key('acp-setup-guide')), findsNothing);
    await tester.tap(editor);
    await settle(tester);
    expect(find.byKey(const Key('acp-setup-guide')), findsOneWidget);

    final other = find.byKey(const ValueKey('access-other-section-false'));
    await tester.scrollUntilVisible(other, 200, scrollable: scroll);
    await tester.ensureVisible(other);
    await settle(tester);
    await tester.tap(find.text('Other ways to connect'));
    await settle(tester);
    expect(find.byKey(const Key('server-deployment-guide')), findsOneWidget);
    expect(
      find.text('Run ViberMate on a server with your own domain'),
      findsOneWidget,
    );
    expect(
      find.byKey(const ValueKey('settings-remote-guide-false')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('only the Users pane opens the remote guide', (tester) async {
    // The Mac app's Server listens on 127.0.0.1.
    final controller = await open(
      tester,
      runtimeTarget: 'http://127.0.0.1:9666',
    );
    controller.openAccessSettings(focus: AccessFocus.remote);
    await settle(tester);
    expect(controller.remoteConnectionGuideRequested, isTrue);
    expect(find.byKey(const Key('server-runtime-access')), findsOneWidget);
    // The Mac app's Server listens on this Mac only: the section is for a
    // browser here, and says what another computer needs instead.
    expect(find.text('Open in a browser on this Mac'), findsOneWidget);
    expect(find.byKey(const Key('server-access-loopback')), findsOneWidget);
    expect(find.text('Manage who can sign in'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('server-runtime-access')),
        matching: find.textContaining('vibermate login --server'),
      ),
      findsNothing,
    );
    // Leaving Settings forgets the request.
    controller.selectSection(WorkbenchSection.captures);
    expect(controller.remoteConnectionGuideRequested, isFalse);
    controller.openAccessSettings();
    await settle(tester);
    expect(find.byKey(const Key('server-runtime-access')), findsNothing);
    // So does going back to the setup guide from Settings.
    controller.openAccessSettings(focus: AccessFocus.remote);
    controller.showSetupGuide();
    expect(controller.remoteConnectionGuideRequested, isFalse);
    await close(tester, controller);
  });

  testWidgets('Use a routing set instead opens Routing', (tester) async {
    final controller = await open(tester);
    controller.openAccessSettings();
    await settle(tester);
    final link = find.byKey(const Key('managed-run-use-routing-set'));
    await tester.ensureVisible(link);
    await settle(tester);
    expect(find.text('Use a routing set instead'), findsOneWidget);
    await tester.tap(link);
    await settle(tester);
    expect(controller.section, WorkbenchSection.environments);
    await close(tester, controller);
  });

  testWidgets('the page reads in Chinese at phone width', (tester) async {
    final controller = await open(
      tester,
      size: const Size(390, 844),
      language: AppLanguage.simplifiedChinese,
    );
    controller.openAccessSettings();
    await settle(tester);
    expect(find.text('启动 Claude Code 或 Codex'), findsWidgets);
    expect(find.text('其他接入方式'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('the VLESS engine line moved to the network exit editor', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1200, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              onPressed: () => showEgressProfileEditor(
                context: context,
                copy: copy,
                vlessEngineInstalled: false,
              ),
              child: const Text('open'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.text('VLESS engine not installed'), findsNothing);
    await tester.tap(find.byKey(const Key('egress-profile-proxy-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-proxy-vless')));
    await tester.pumpAndSettle();
    expect(find.text('VLESS engine not installed'), findsOneWidget);
  });

  test('Preferences no longer promises transparent capture', () {
    expect(
      AppCopy.forLanguage(AppLanguage.english)(
        'settings.section_detail.general',
      ),
      'Appearance, language and updates',
    );
    expect(
      AppCopy.forLanguage(AppLanguage.simplifiedChinese)(
        'settings.section_detail.general',
      ),
      '外观、语言与更新',
    );
  });
}

final class _RefusingInstaller implements RootTrustInstaller {
  const _RefusingInstaller();

  @override
  Future<void> install(RootCAMaterial material) async =>
      throw const RootTrustInstallerException(
        RootTrustInstallerFailure.userCancelled,
      );
}

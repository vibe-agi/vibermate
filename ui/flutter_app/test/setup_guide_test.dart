import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';

void main() {
  // The preview Runtime behind the guide that open() last showed.
  late PreviewControlApi api;

  Future<WorkbenchController> open(
    WidgetTester tester, {
    PreviewFreshStage stage = PreviewFreshStage.start,
    Size size = const Size(1440, 900),
    AppLanguage language = AppLanguage.english,
    bool dark = false,
    bool rootTrusted = false,
  }) async {
    await tester.binding.setSurfaceSize(size);
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewFixture.freshInstall(stage: stage);
    api = fixture.api;
    if (rootTrusted) api.setRootTrustForPreview(installed: true);
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      rootTrustManagement: true,
      rootTrustInstaller: PreviewRootTrustInstaller(fixture.api),
      closeRuntime: fixture.api.close,
      initialPreferences: WorkbenchPreferences(language: language),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: dark ? ViberTheme.dark() : ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    // The guide reads the certificate when it first shows. The step 3
    // spinner never settles, so pump fixed frames instead of settling.
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

  Finder inStep(String step, Finder finder) => find.descendant(
    of: find.byKey(Key('setup-step-$step')),
    matching: finder,
  );

  testWidgets('a fresh install shows the guide with step 1 current', (
    tester,
  ) async {
    final controller = await open(tester);

    expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
    expect(find.text('Connect Claude Code or Codex'), findsOneWidget);
    expect(
      find.byKey(const Key('capture-empty-guide-placeholder')),
      findsOneWidget,
      reason: 'the list column keeps its toolbar and says only this',
    );
    expect(find.text('No sessions yet'), findsOneWidget);
    expect(find.text('Open Terminal setup'), findsNothing);
    expect(find.byKey(const Key('evidence-search-open')), findsOneWidget);
    expect(find.byKey(const Key('manual-capture-create')), findsOneWidget);

    final action = find.byKey(const Key('setup-command-action'));
    expect(action, findsOneWidget);
    expect(inStep('command', find.text('Set up command')), findsOneWidget);
    expect(
      tester
          .widget<TextButton>(
            find.descendant(
              of: find.byKey(const Key('setup-command-claude')),
              matching: find.byType(TextButton),
            ),
          )
          .onPressed,
      isNull,
      reason: 'the launch commands wait for the Terminal command',
    );
    expect(find.byKey(const Key('setup-waiting')), findsNothing);
    expect(controller.selectedCaptureKey, isNull);

    // Setting up the command, with the existing confirmation, ticks step 1.
    await tester.tap(action);
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('terminal-command-confirmation')), findsOne);
    await tester.tap(find.byKey(const Key('terminal-command-confirm-action')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(inStep('command', find.text('Done')), findsOneWidget);
    expect(find.byKey(const Key('setup-waiting')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('with the command ready, step 2 waits and shows the Codex line', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.command);

    expect(inStep('command', find.text('Done')), findsOneWidget);
    expect(
      find.text('Waiting for your agent… This page updates by itself.'),
      findsOneWidget,
    );
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    expect(find.text('vibermate run -- codex'), findsOneWidget);
    final requirement = find.byKey(const Key('setup-codex-requirement'));
    expect(requirement, findsOneWidget);
    expect(
      find.descendant(
        of: requirement,
        matching: find.text('Install and trust'),
      ),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('setup-install-trust')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.rootCAStatus!.installed, isTrue);
    expect(requirement, findsNothing);
    // The requirement reads as met instead of vanishing.
    final ready = find.byKey(const Key('setup-codex-ready'));
    expect(ready, findsOneWidget);
    expect(
      find.descendant(
        of: ready,
        matching: find.text("Codex: this Mac trusts ViberMate's certificate."),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a trusted certificate hides the Codex requirement', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.trusted);
    expect(find.byKey(const Key('setup-codex-requirement')), findsNothing);
    expect(
      find.byKey(const Key('setup-codex-ready')),
      findsNothing,
      reason: 'nothing was done here to confirm',
    );
    expect(find.byKey(const Key('setup-waiting')), findsOneWidget);
    await close(tester, controller);
  });

  testWidgets('trusting the certificate turns the failure into a retry', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.failed);
    await tester.tap(find.byKey(const Key('launch-stopped-install-trust')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.rootCAStatus!.installed, isTrue);

    final retry = find.byKey(const Key('setup-root-trusted-retry'));
    expect(retry, findsOneWidget);
    expect(
      find.descendant(
        of: retry,
        matching: find.text(
          "This Mac now trusts ViberMate's certificate. Run the Codex command "
          'again.',
        ),
      ),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('setup-root-trusted-copy-again')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('setup-launch-failed')), findsNothing);
    expect(inStep('start', find.text("Didn't start")), findsNothing);
    final row = find.byKey(
      Key('capture-outcome-${controller.data!.captures.single.key}'),
    );
    expect(row, findsOneWidget, reason: 'the row keeps its badge');

    // A Codex launch that stops after the trust is explained as usual.
    api.addFreshRun(codex: true, phase: PreviewFreshRunPhase.neverStarted);
    await controller.refresh();
    await tester.pump(const Duration(milliseconds: 300));
    expect(retry, findsNothing);
    expect(find.byKey(const Key('setup-launch-failed')), findsOneWidget);
    expect(inStep('start', find.text("Didn't start")), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a Mac that already trusted the certificate keeps the failure', (
    tester,
  ) async {
    final controller = await open(
      tester,
      stage: PreviewFreshStage.failed,
      rootTrusted: true,
    );
    expect(find.byKey(const Key('setup-launch-failed')), findsOneWidget);
    expect(find.byKey(const Key('setup-root-trusted-retry')), findsNothing);
    expect(find.byKey(const Key('setup-codex-ready')), findsNothing);
    expect(inStep('start', find.text("Didn't start")), findsOneWidget);
    expect(find.byKey(const Key('launch-stopped-copy-again')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a running agent waits for its first request, unselected', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.waiting);

    expect(inStep('start', find.text('Claude Code started.')), findsOneWidget);
    expect(find.byKey(const Key('setup-prompt-waiting')), findsOneWidget);
    expect(
      inStep(
        'prompt',
        find.text('Waiting for your agent… This page updates by itself.'),
      ),
      findsOneWidget,
    );
    expect(controller.selectedCaptureKey, isNull);
    final row = find.byKey(
      Key('capture-row-${controller.data!.captures.single.key}'),
    );
    expect(row, findsOneWidget);

    // Opening the row keeps a way back to the guide.
    await tester.tap(row);
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('setup-guide-pane')), findsNothing);
    final back = find.byKey(const Key('setup-back'));
    expect(back, findsOneWidget);
    expect(find.text('Back to setup guide'), findsOneWidget);
    await tester.tap(back);
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
    expect(controller.selectedCaptureKey, isNull);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a launch that never started says so in step 2', (tester) async {
    final controller = await open(tester, stage: PreviewFreshStage.failed);

    final failure = find.byKey(const Key('setup-launch-failed'));
    expect(failure, findsOneWidget);
    expect(
      find.descendant(
        of: failure,
        matching: find.text('This launch stopped before Codex started'),
      ),
      findsOneWidget,
    );
    expect(inStep('start', find.text("Didn't start")), findsOneWidget);
    expect(
      find.byKey(const Key('launch-stopped-install-trust')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('setup-codex-requirement')),
      findsNothing,
      reason: 'one Install and trust button, in the failure',
    );
    expect(find.byKey(const Key('setup-waiting')), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('the first request opens its session with the connected notice', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.observed);
    expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
    expect(controller.selectedCaptureKey, isNull);

    await controller.refresh();
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));

    final capture = controller.data!.captures.single;
    expect(capture.observation, 'observed');
    expect(controller.selectedCaptureKey, capture.key);
    expect(find.byKey(const Key('setup-guide-pane')), findsNothing);
    expect(find.byKey(const Key('setup-back')), findsNothing);
    expect(find.byKey(const Key('setup-connected-notice')), findsOneWidget);
    expect(
      find.text(
        'Connected. Every request from Claude Code appears below as it happens.',
      ),
      findsOneWidget,
    );
    final days = controller.data!.environments
        .singleWhere((value) => value.systemOwned)
        .contentRecording
        .retentionDays;
    expect(
      find.textContaining('on this Mac for $days days'),
      findsOneWidget,
      reason: 'the retention comes from the routing set',
    );
    expect(find.byKey(const Key('setup-connected-manage')), findsOneWidget);

    await tester.tap(find.byKey(const Key('setup-connected-dismiss')));
    await tester.pump();
    expect(find.byKey(const Key('setup-connected-notice')), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('at phone width the first request opens its session too', (
    tester,
  ) async {
    final controller = await open(
      tester,
      stage: PreviewFreshStage.observed,
      size: const Size(390, 844),
    );
    expect(find.byKey(const Key('setup-guide')), findsOneWidget);

    await controller.refresh();
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    final capture = controller.data!.captures.single;
    expect(controller.selectedCaptureKey, capture.key);
    // The guide gives way to the session and its notice, not to a bare row.
    expect(find.byKey(const Key('setup-guide')), findsNothing);
    expect(find.byKey(const Key('setup-connected-notice')), findsOneWidget);
    expect(find.textContaining('30 days'), findsOneWidget);
    expect(find.byTooltip('Back'), findsOneWidget);

    await tester.tap(find.byTooltip('Back'));
    await tester.pump(const Duration(milliseconds: 300));
    expect(
      find.byKey(Key('capture-row-${capture.key}')),
      findsOneWidget,
      reason: 'the back arrow still returns to the list',
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a session opened from the guide keeps its place as it connects', (
    tester,
  ) async {
    final controller = await open(tester, stage: PreviewFreshStage.observed);
    final capture = controller.data!.captures.single;
    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    final back = find.byKey(const Key('setup-back'));
    expect(back, findsOneWidget);
    final header = find.byKey(const Key('capture-directory-toggle'));
    final before = tester.getTopLeft(header);

    await controller.refresh();
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.data!.captures.single.observation, 'observed');
    expect(find.byKey(const Key('setup-connected-notice')), findsOneWidget);
    expect(back, findsOneWidget);
    expect(tester.getTopLeft(header), before, reason: 'the header stays put');

    // Going back asks for the guide again, now with every step done.
    await tester.tap(back);
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
    expect(inStep('prompt', find.text('Done')), findsOneWidget);

    // Once the notice is dismissed, the session no longer offers the way back.
    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.tap(find.byKey(const Key('setup-connected-dismiss')));
    controller.hideSetupGuide();
    await tester.pump(const Duration(milliseconds: 300));
    expect(back, findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('Hide guide lasts for the session; Settings shows it again', (
    tester,
  ) async {
    final controller = await open(tester);
    await tester.tap(find.byKey(const Key('setup-hide')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byKey(const Key('setup-guide')), findsNothing);
    expect(find.text('No sessions yet'), findsOneWidget);
    expect(
      find.byKey(const Key('capture-empty-open-terminal-settings')),
      findsOneWidget,
      reason: 'the hidden guide leaves today\'s empty state in place',
    );

    controller.openAccessSettings();
    await tester.pump(const Duration(milliseconds: 300));
    final show = find.byKey(const Key('settings-show-setup-guide'));
    expect(show, findsOneWidget);
    await tester.tap(show);
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.section, WorkbenchSection.captures);
    expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets(
    'a filter replaces the guide; clearing it brings the guide back',
    (tester) async {
      final controller = await open(tester, stage: PreviewFreshStage.waiting);
      await tester.enterText(find.byKey(const Key('capture-filter')), 'zzz');
      await tester.pump(const Duration(milliseconds: 300));
      expect(find.byKey(const Key('setup-guide')), findsNothing);
      expect(find.byKey(const Key('capture-filter-no-match')), findsOneWidget);
      expect(find.text('Open Terminal setup'), findsNothing);
      await tester.enterText(find.byKey(const Key('capture-filter')), '');
      await tester.pump(const Duration(milliseconds: 300));
      expect(find.byKey(const Key('setup-guide-pane')), findsOneWidget);
      await close(tester, controller);
    },
  );

  testWidgets(
    'at phone width the guide heads the list with full-width buttons',
    (tester) async {
      final controller = await open(
        tester,
        stage: PreviewFreshStage.failed,
        size: const Size(390, 844),
      );
      final guide = find.byKey(const Key('setup-guide'));
      expect(guide, findsOneWidget);
      expect(find.byKey(const Key('setup-guide-pane')), findsNothing);
      final button = find.byKey(const Key('launch-stopped-install-trust'));
      await tester.ensureVisible(button);
      await tester.pump();
      final failure = find.byKey(const Key('setup-launch-failed'));
      expect(
        tester.getSize(button).width,
        // The notice keeps 12 px of padding inside its 1 px border.
        moreOrLessEquals(tester.getSize(failure).width - 2 * 13, epsilon: 1),
      );
      // The failed run still follows under its section label.
      expect(find.text('HISTORY'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await close(tester, controller);
    },
  );

  testWidgets('the guide reads in Chinese and in the dark theme', (
    tester,
  ) async {
    final controller = await open(
      tester,
      stage: PreviewFreshStage.command,
      language: AppLanguage.simplifiedChinese,
      dark: true,
    );
    expect(find.text('接入 Claude Code 或 Codex'), findsOneWidget);
    expect(find.text('设置 vibermate 命令'), findsOneWidget);
    expect(find.text('启动 Claude Code 或 Codex'), findsOneWidget);
    expect(find.text('发送第一条消息'), findsOneWidget);
    expect(find.text('正在等待 Agent……此页面会自动更新。'), findsOneWidget);
    expect(find.text('安装并信任'), findsOneWidget);
    expect(find.text('还没有运行记录'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a Server owner gets the login and --server commands', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi.freshInstall();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewFixture.freshInstall().terminalCommands,
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      closeRuntime: api.close,
      runtimeTarget: '192.168.1.44:9666',
      webPrincipal: const RuntimeWebPrincipal(
        id: 'user.preview.owner',
        username: 'owner',
        role: RuntimeWebRole.owner,
      ),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('Install vibermate and sign in'), findsOneWidget);
    expect(find.byKey(const Key('setup-command-login')), findsOneWidget);
    expect(find.byKey(const Key('setup-command-action')), findsNothing);
    expect(find.textContaining('vibermate run --server'), findsNWidgets(2));
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('the guide and the toolbar both connect another app', (
    tester,
  ) async {
    final controller = await open(tester);
    expect(find.byTooltip('Connect another app'), findsOneWidget);
    await tester.tap(find.byKey(const Key('setup-connect-other-app')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(
      find.descendant(
        of: find.byType(AlertDialog),
        matching: find.text('Connect another app'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('manual-capture-intro')), findsOneWidget);
    expect(find.text('App name'), findsOneWidget);
    expect(
      tester
          .widget<TextField>(
            find.descendant(
              of: find.byKey(const Key('manual-capture-name')),
              matching: find.byType(TextField),
            ),
          )
          .controller!
          .text,
      isEmpty,
      reason: 'the example is a placeholder, nothing is prefilled',
    );
    expect(find.text('e.g. Figma Desktop'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pump(const Duration(milliseconds: 300));
    await close(tester, controller);
  });

  testWidgets('a Web owner\'s empty list leads with the access guide', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi.freshInstall();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewFixture.freshInstall().terminalCommands,
      previewMode: false,
      serverManagement: true,
      terminalManagement: false,
      closeRuntime: api.close,
      runtimeTarget: '192.168.1.44:9666',
      webPrincipal: const RuntimeWebPrincipal(
        id: 'user.preview.owner',
        username: 'owner',
        role: RuntimeWebRole.owner,
      ),
    );
    await controller.initialize();
    controller.hideSetupGuide();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump(const Duration(milliseconds: 300));
    expect(
      tester.widget(
        find.byKey(const Key('capture-empty-open-terminal-settings')),
      ),
      isA<FilledButton>(),
    );
    expect(
      tester.widget(find.byKey(const Key('capture-empty-create-manual'))),
      isA<OutlinedButton>(),
    );
    expect(
      find.descendant(
        of: find.byKey(const Key('capture-empty-create-manual')),
        matching: find.text('Connect another app'),
      ),
      findsOneWidget,
    );
    await close(tester, controller);
  });
}

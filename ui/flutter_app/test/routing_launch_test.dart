import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;

  Future<(WorkbenchController, List<String>)> open(
    WidgetTester tester, {
    bool fresh = false,
    bool commandReady = true,
    AppLanguage language = AppLanguage.english,
    Size size = const Size(1440, 900),
  }) async {
    await tester.binding.setSurfaceSize(size);
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final copied = <String>[];
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied.add((call.arguments as Map)['text'] as String);
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
    final fixture = fresh
        ? PreviewFixture.freshInstall(stage: PreviewFreshStage.command)
        : PreviewFixture(
            api: PreviewControlApi(),
            terminalCommands: PreviewTerminalCommandService(
              initial: previewFreshTerminalCommand(
                commandReady
                    ? PreviewFreshStage.command
                    : PreviewFreshStage.start,
              ),
            ),
          );
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
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
    return (controller, copied);
  }

  Future<void> settle(WidgetTester tester) async {
    for (var frame = 0; frame < 6; frame++) {
      await tester.pump(const Duration(milliseconds: 100));
    }
  }

  Future<void> close(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
    await tester.pump();
  }

  Future<void> openRoutingSet(
    WidgetTester tester,
    WorkbenchController controller,
    String id,
  ) async {
    controller.selectSection(WorkbenchSection.environments);
    await settle(tester);
    await tester.tap(find.byKey(Key('environment-row-$id')));
    await settle(tester);
  }

  testWidgets('a routing set says how to start an agent on it', (tester) async {
    final (controller, copied) = await open(tester);
    await openRoutingSet(tester, controller, 'work');

    final launch = find.byKey(const Key('environment-launch'));
    expect(launch, findsOneWidget);
    expect(
      find.descendant(
        of: launch,
        matching: find.text('Start with this routing set'),
      ),
      findsOneWidget,
    );
    expect(find.text('vibermate run --env work -- claude'), findsOneWidget);
    expect(find.text('vibermate run --env work -- codex'), findsOneWidget);
    final copy = find.byKey(const Key('environment-launch-copy-codex'));
    expect(
      find.descendant(of: copy, matching: find.text('Copy')),
      findsOneWidget,
    );
    await tester.tap(copy);
    await tester.pump();
    expect(copied, ['vibermate run --env work -- codex']);
    // An enabled set wears no green pill; it is the normal case.
    expect(
      find.descendant(
        of: find.byKey(const Key('environment-title')).first,
        matching: find.text('Active'),
      ),
      findsNothing,
    );
    expect(
      find.byWidgetPredicate(
        (widget) => widget is StatusPill && widget.label == 'Active',
      ),
      findsNothing,
    );
    expect(find.byKey(const Key('environment-disabled')), findsNothing);
    await tester.pump(const Duration(seconds: 3));
    await close(tester, controller);
  });

  testWidgets('before the command is set up, the card leads to its setup', (
    tester,
  ) async {
    final (controller, _) = await open(tester, commandReady: false);
    await openRoutingSet(tester, controller, 'work');
    final launch = find.byKey(const Key('environment-launch'));
    expect(
      find.descendant(
        of: launch,
        matching: find.text(
          'Set up the vibermate command first, then copy a command below.',
        ),
      ),
      findsOne,
    );
    // One instruction, not two: the lead line waits for the command.
    expect(
      find.descendant(
        of: launch,
        matching: find.text(
          'In Terminal, go to your project folder and run one of these:',
        ),
      ),
      findsNothing,
    );
    expect(
      tester
          .widget<TextButton>(
            find.byKey(const Key('environment-launch-copy-claude')),
          )
          .onPressed,
      isNull,
    );
    final setUp = find.byKey(const Key('environment-launch-set-up-command'));
    expect(
      find.descendant(of: setUp, matching: find.text('Open Terminal setup')),
      findsOne,
    );
    await tester.tap(setUp);
    await settle(tester);
    expect(controller.section, WorkbenchSection.settings);
    expect(find.byKey(const Key('terminal-command-panel')), findsOne);
    expect(find.byKey(const Key('terminal-command-install')), findsOne);
    await close(tester, controller);
  });

  testWidgets('the built-in set starts without --env', (tester) async {
    final (controller, _) = await open(tester);
    await openRoutingSet(tester, controller, 'system_transparent');
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    expect(find.text('vibermate run -- codex'), findsOneWidget);
    expect(find.textContaining('vibermate run --env'), findsNothing);
    expect(find.byKey(const Key('environment-builtin-description')), findsOne);
    await close(tester, controller);
  });

  testWidgets('a disabled set says so and offers no command', (tester) async {
    final (controller, _) = await open(tester);
    final current = controller.data!;
    controller.data = DashboardData(
      status: current.status,
      captures: current.captures,
      captureNextCursor: current.captureNextCursor,
      environments: [
        for (final environment in current.environments)
          environment.id == 'research'
              ? environment.copyWith(state: 'disabled')
              : environment,
      ],
      endpoints: current.endpoints,
      accounts: current.accounts,
    );
    await openRoutingSet(tester, controller, 'research');
    expect(find.byKey(const Key('environment-launch')), findsNothing);
    expect(find.byKey(const Key('environment-disabled')), findsOneWidget);
    expect(
      find.byKey(const Key('environment-disabled-research')),
      findsOneWidget,
      reason: 'the list marks it too',
    );
    expect(find.byKey(const Key('environment-live-sessions')), findsOneWidget);
    await close(tester, controller);
  });

  testWidgets('Send requests through changes the commands', (tester) async {
    final (controller, copied) = await open(tester);
    controller.openAccessSettings();
    await settle(tester);
    expect(find.text('1. Set up the vibermate command'), findsOneWidget);
    expect(
      find.text("2. Codex only: trust ViberMate's certificate"),
      findsOneWidget,
    );
    expect(
      find.text('3. Copy a command and run it in your project folder'),
      findsOneWidget,
    );
    expect(find.text('Send requests through'), findsOneWidget);
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    Text detail() =>
        tester.widget<Text>(find.byKey(const Key('access-launch-detail')));
    expect(
      detail().data,
      'To use your accounts and scripts, choose a routing set above.',
    );
    expect(
      find.byKey(const Key('managed-run-use-routing-set')),
      findsNothing,
      reason: 'the choice itself is the way to a routing set',
    );

    final select = find.byKey(const Key('access-routing-set'));
    await tester.ensureVisible(select);
    await settle(tester);
    await tester.tap(select);
    await settle(tester);
    await tester.tap(find.byKey(const Key('access-routing-set-work')));
    await settle(tester);
    expect(find.text('vibermate run --env work -- claude'), findsOneWidget);
    expect(find.text('vibermate run --env work -- codex'), findsOneWidget);
    // The line says what the chosen set does, not to choose one.
    expect(
      detail().data,
      'Requests follow the Work routing set: its accounts, scripts and '
      'network exits apply.',
    );
    await tester.tap(find.byKey(const Key('managed-run-copy-claude')));
    await tester.pump();
    expect(copied.last, 'vibermate run --env work -- claude');
    expect(controller.launchRoutingSetId, 'work');

    await tester.tap(select);
    await settle(tester);
    await tester.tap(find.byKey(const Key('access-routing-set-built-in')));
    await settle(tester);
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    expect(find.text('vibermate run -- codex'), findsOneWidget);
    await tester.pump(const Duration(seconds: 3));
    await close(tester, controller);
  });

  testWidgets('without another routing set, the choice stays out of the way', (
    tester,
  ) async {
    final (controller, _) = await open(tester, fresh: true);
    controller.openAccessSettings();
    await settle(tester);
    expect(find.text('Send requests through'), findsNothing);
    expect(find.text('Use a routing set instead'), findsOneWidget);
    expect(find.text('vibermate run -- claude'), findsOneWidget);
    // Trusted, the certificate step goes and the copy step is second.
    controller.rootCAStatus = controller.rootCAStatus!.copyWith(
      certificatePresent: 'present',
      trustDecision: 'trusted',
    );
    controller.notifyListeners();
    await settle(tester);
    expect(find.byKey(const Key('access-step-trust')), findsNothing);
    expect(
      find.text('2. Copy a command and run it in your project folder'),
      findsOneWidget,
    );
    await close(tester, controller);
  });
}

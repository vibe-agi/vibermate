import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';

void main() {
  Future<(WorkbenchController, PreviewControlApi)> open(
    WidgetTester tester, {
    PreviewFreshStage stage = PreviewFreshStage.command,
    PreviewFreshArrival? arrive,
  }) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewFixture.freshInstall(stage: stage, arrive: arrive);
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      rootTrustManagement: true,
      rootTrustInstaller: PreviewRootTrustInstaller(fixture.api),
      closeRuntime: fixture.api.close,
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
    return (controller, fixture.api);
  }

  Future<void> close(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
    await tester.pump();
  }

  testWidgets('a launch that never started is marked and explained', (
    tester,
  ) async {
    final (controller, _) = await open(tester, stage: PreviewFreshStage.failed);
    final capture = controller.data!.captures.single;
    expect(capture.stoppedBeforeStart, isTrue);

    final badge = find.byKey(Key('capture-outcome-${capture.key}'));
    expect(badge, findsOneWidget);
    expect(tester.widget<Text>(badge).data, "Didn't start");
    final context = tester.element(badge);
    // The ledger's Failed colour; red stays for Blocked and Can't connect.
    expect(
      tester.widget<Text>(badge).style?.color,
      context.viberColors.warning,
    );

    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.selectedCaptureLaunchIncomplete, isTrue);
    expect(controller.selectedAssignment, isNotNull);
    final message = find.byKey(const Key('capture-launch-stopped'));
    expect(message, findsOneWidget);
    expect(
      find.descendant(
        of: message,
        matching: find.text('This launch stopped before Codex started'),
      ),
      findsOneWidget,
    );
    final install = find.byKey(const Key('capture-launch-install-trust'));
    expect(install, findsOneWidget);
    expect(find.byKey(const Key('capture-launch-copy-again')), findsNothing);
    expect(find.byKey(const Key('capture-delete')), findsOneWidget);

    // Once the certificate is trusted, running the command again is all
    // that is left.
    await tester.tap(install);
    await tester.pump(const Duration(milliseconds: 300));
    expect(install, findsNothing);
    expect(find.byKey(const Key('capture-launch-copy-again')), findsOneWidget);
    expect(find.text('Copy command again'), findsOneWidget);
    // Only causes that leave a record are named; a program Terminal cannot
    // find stops before the session exists.
    expect(
      find.text(
        'Terminal shows why. Fix it, then run the command again. You can '
        'delete this record.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining("couldn't find"), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a running session with no request yet says it is waiting', (
    tester,
  ) async {
    final (controller, _) = await open(
      tester,
      stage: PreviewFreshStage.waiting,
    );
    final capture = controller.data!.captures.single;
    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    // A short title, with what to do as its detail.
    expect(find.text('Waiting for the first request'), findsOneWidget);
    expect(
      find.text(
        'Type a prompt in Claude Code; it appears here within a few seconds.',
      ),
      findsOneWidget,
    );
    expect(find.text('No requests yet'), findsNothing);
    expect(find.byKey(Key('capture-outcome-${capture.key}')), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('arrive=failed adds the stopped launch within ten seconds', (
    tester,
  ) async {
    final (controller, _) = await open(
      tester,
      arrive: PreviewFreshArrival.failedLaunch,
    );
    expect(controller.data!.captures, isEmpty);
    await tester.pump(const Duration(seconds: 10));
    await tester.pump(const Duration(milliseconds: 300));
    final capture = controller.data!.captures.single;
    expect(capture.stoppedBeforeStart, isTrue);
    expect(find.byKey(Key('capture-outcome-${capture.key}')), findsOneWidget);
    expect(find.byKey(const Key('setup-launch-failed')), findsOneWidget);
    await close(tester, controller);
  });

  testWidgets('a newer run replaces a selected launch that never started', (
    tester,
  ) async {
    final (controller, api) = await open(
      tester,
      stage: PreviewFreshStage.failed,
    );
    final failed = controller.data!.captures.single;
    await controller.selectCapture(failed.key);
    await tester.pump(const Duration(milliseconds: 300));

    api.addFreshRun(codex: true, phase: PreviewFreshRunPhase.waiting);
    await tester.pump(const Duration(seconds: 5));
    await tester.pump(const Duration(milliseconds: 300));
    final running = controller.data!.captures.firstWhere(
      (capture) => capture.running,
    );
    expect(controller.selectedCaptureKey, running.key);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a stopped launch the person opened stays open', (tester) async {
    final (controller, api) = await open(
      tester,
      stage: PreviewFreshStage.failed,
    );
    // A newer run that was already listed when the stopped one was opened.
    api.addFreshRun(codex: true, phase: PreviewFreshRunPhase.waiting);
    await controller.refresh();
    await tester.pump(const Duration(milliseconds: 300));
    final failed = controller.data!.captures.singleWhere(
      (capture) => capture.stoppedBeforeStart,
    );
    await tester.tap(find.byKey(Key('capture-row-${failed.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.selectedCaptureKey, failed.key);

    // Polls keep it open: only a run that appears afterwards replaces it.
    await tester.pump(const Duration(seconds: 6));
    await tester.pump(const Duration(milliseconds: 300));
    expect(controller.selectedCaptureKey, failed.key);
    expect(find.byKey(const Key('capture-launch-stopped')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  Future<String?> copyAgain(
    WidgetTester tester,
    WorkbenchController controller,
    String runId,
  ) async {
    String? copied;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied = (call.arguments as Map)['text'] as String;
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
    await controller.refresh();
    await controller.selectCapture('managed_run:$runId');
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    final button = find.byKey(const Key('capture-launch-copy-again'));
    expect(button, findsOneWidget);
    expect(find.text('Copy command again'), findsOneWidget);
    await tester.tap(button);
    await tester.pump(const Duration(milliseconds: 300));
    return copied;
  }

  testWidgets('copy again repeats the program that was launched', (
    tester,
  ) async {
    final (controller, api) = await open(tester);
    final id = api.addFreshRun(
      codex: false,
      phase: PreviewFreshRunPhase.neverStarted,
      program: 'aider',
    );
    expect(
      await copyAgain(tester, controller, id),
      'vibermate run -- aider',
      reason: 'never the Claude Code command for another program',
    );
    expect(find.text('Copied'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('copy again keeps the routing set the launch asked for', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewFixture.freshInstall().terminalCommands,
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
    await tester.pump(const Duration(milliseconds: 300));
    final id = api.addFreshRun(
      codex: false,
      phase: PreviewFreshRunPhase.neverStarted,
      environmentId: 'work',
    );
    expect(
      await copyAgain(tester, controller, id),
      'vibermate run --env work -- claude',
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('a waiting proxy login keeps its own empty state', (
    tester,
  ) async {
    final (controller, _) = await open(tester, stage: PreviewFreshStage.start);
    final context = await controller.loadManualCaptureContext(
      'system_transparent',
    );
    final created = await controller.createManualCapture(
      context: context!,
      displayName: 'Figma',
      clientClass: 'desktop_app',
      lifetime: 'until_revoked',
    );
    expect(created, isNotNull);
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    final waiting = controller.selectedCapture!;
    expect(waiting.isManual, isTrue);
    expect(waiting.running, isTrue);
    expect(waiting.observation, 'waiting_for_traffic');
    expect(waiting.stoppedBeforeStart, isFalse);
    controller.selectCaptureDetailMode(CaptureDetailMode.requests);
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
    // Running and waiting like an agent, but nobody types a prompt in a
    // proxy login: its list keeps the plain empty state.
    expect(find.text('No requests yet'), findsOneWidget);
    expect(find.text('Waiting for the first request'), findsNothing);
    expect(find.textContaining('Type a prompt in'), findsNothing);
    expect(find.byKey(const Key('capture-launch-stopped')), findsNothing);
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });
}

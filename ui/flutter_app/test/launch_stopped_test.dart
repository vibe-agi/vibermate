import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
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
    expect(tester.widget<Text>(badge).style?.color, context.viberColors.danger);

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
    expect(
      find.text(
        'Waiting for the first request. Type a prompt in Claude Code; it '
        'appears here within a few seconds.',
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

  testWidgets('a waiting proxy login keeps its own empty state', (
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
    final dashboard = controller.data!;
    final manual = dashboard.captures.firstWhere((value) => value.isManual);
    final waiting = CaptureRecord(
      key: manual.key,
      id: manual.id,
      kind: manual.kind,
      displayName: manual.displayName,
      state: manual.state,
      observation: 'waiting_for_traffic',
      createdAt: manual.createdAt,
      updatedAt: manual.updatedAt,
      manualCapture: ManualCaptureSummary(
        clientClass: manual.manualCapture!.clientClass,
        lifetime: manual.manualCapture!.lifetime,
        credentialRevision: manual.manualCapture!.credentialRevision,
      ),
    );
    expect(waiting.stoppedBeforeStart, isFalse);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump(const Duration(milliseconds: 300));
    expect(
      find.textContaining('Type a prompt in ${manual.displayName}'),
      findsNothing,
    );
    await close(tester, controller);
  });
}

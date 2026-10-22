import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/runtime_storage.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/bootstrap/root_trust_installer.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/runtime_connection_guide.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test(
    'storage refresh owns reentrant notification demand until fresh read',
    () async {
      final fixture = PreviewControlApi();
      final location = await fixture.storageLocation();
      final api = _QueuedCleanupStatisticsApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      Future<void>? coalesced;
      var requested = false;
      var completed = false;
      controller.addListener(() {
        if (controller.storageLocationLoading && !requested) {
          requested = true;
          coalesced = controller.refreshStorageLocation().then((_) {
            completed = true;
          });
        }
      });
      final first = controller.refreshStorageLocation();
      expect(api.reads, hasLength(1));
      api.reads.first.complete(location);
      await Future<void>.delayed(Duration.zero);
      expect(api.reads, hasLength(2));
      expect(completed, isFalse);
      api.reads.last.complete(location);
      await first;
      await coalesced;
      expect(completed, isTrue);
      expect(controller.storageLocationLoading, isFalse);
    },
  );
  testWidgets('incomplete zero-count storage preview keeps cleanup available', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _CompletedCleanupApi(fixture)..completed = true;
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    controller.data = await fixture.loadDashboard();
    controller.storageLocation = await api.storageLocation();
    controller.settingsTab = controller.settingsDestinations.indexOf(
      SettingsDestination.safety,
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: SettingsView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    final cleanup = find.byKey(const Key('storage-cleanup-expired'));
    await tester.scrollUntilVisible(
      find.byKey(const Key('storage-cleanup-preview')),
      240,
      scrollable: find
          .descendant(
            of: find.byKey(const Key('settings-safety-scroll')),
            matching: find.byType(Scrollable),
          )
          .first,
    );
    expect(cleanup, findsOneWidget);
    expect(
      find.text('No expired evidence is waiting for cleanup.'),
      findsNothing,
    );
  });
  testWidgets(
    'pre-acceptance cleanup certificate cannot complete a newer cleanup',
    (tester) async {
      final fixture = PreviewControlApi();
      final snapshots = _CompletedCleanupApi(fixture);
      final pendingLocation = await snapshots.storageLocation();
      snapshots.completed = true;
      final oldCompletedLocation = await snapshots.storageLocation();
      final freshCompletedLocation = await snapshots.storageLocation();
      final api = _QueuedCleanupStatisticsApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      controller.storageLocation = pendingLocation;
      final refresh = controller.refreshStorageLocation();
      expect(api.reads, hasLength(1));

      final outcome = await controller.cleanupExpiredEvidence();
      expect(outcome.deleted, isTrue);
      expect(controller.storageCleanupRunning, isFalse);
      expect(controller.storageLocationLoading, isTrue);
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
      // Concurrent demand must coalesce with the acceptance refresh.
      unawaited(controller.refreshStorageLocation());
      unawaited(controller.refreshStorageLocation());
      api.reads.first.complete(oldCompletedLocation);
      await tester.pump();

      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
      expect(controller.storageLocation, same(pendingLocation));
      expect(controller.previousStorageLocation, isNull);
      expect(controller.storageLocation!.cleanupPreview.cleanupPending, isTrue);
      expect(controller.storageLocationLoading, isTrue);
      expect(api.reads, hasLength(2));
      api.reads.last.complete(freshCompletedLocation);
      await refresh;

      expect(controller.storageLocation, same(freshCompletedLocation));
      expect(controller.previousStorageLocation, same(pendingLocation));
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_partial',
      );
      expect(controller.storageLocationLoading, isFalse);
      expect(controller.storageLocationFailed, isFalse);
      expect(api.reads, hasLength(2));
    },
  );
  testWidgets(
    'obsolete storage error drains a fresh read and preserves failure recovery',
    (tester) async {
      final fixture = PreviewControlApi();
      final pendingLocation = await _CompletedCleanupApi(
        fixture,
      ).storageLocation();
      final api = _QueuedCleanupStatisticsApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      controller.storageLocation = pendingLocation;
      final refresh = controller.refreshStorageLocation();
      await controller.cleanupExpiredEvidence();
      api.reads.first.completeError(StateError('obsolete statistics failed'));
      await tester.pump();

      expect(controller.storageLocationFailed, isFalse);
      expect(controller.storageLocationLoading, isTrue);
      expect(controller.storageLocation, same(pendingLocation));
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
      expect(api.reads, hasLength(2));
      api.reads.last.completeError(StateError('fresh statistics failed'));
      await refresh;
      expect(controller.storageLocationFailed, isTrue);
      expect(controller.storageLocationLoading, isFalse);
      expect(controller.storageLocation, same(pendingLocation));
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );

      final retry = controller.refreshStorageLocation();
      expect(controller.storageLocationLoading, isTrue);
      expect(controller.storageLocationFailed, isFalse);
      api.reads.last.complete(pendingLocation);
      await retry;
      expect(controller.storageLocationLoading, isFalse);
      expect(controller.storageLocationFailed, isFalse);
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
    },
  );
  for (final disposeDuringFreshRead in [false, true]) {
    testWidgets(
      'disposed storage refresh does not deliver or drain further reads '
      'fresh=$disposeDuringFreshRead',
      (tester) async {
        final fixture = PreviewControlApi();
        final snapshots = _CompletedCleanupApi(fixture);
        final pendingLocation = await snapshots.storageLocation();
        snapshots.completed = true;
        final completedLocation = await snapshots.storageLocation();
        final api = _QueuedCleanupStatisticsApi(fixture);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
        );
        addTearDown(controller.dispose);
        controller.storageLocation = pendingLocation;
        final refresh = controller.refreshStorageLocation();
        await controller.cleanupExpiredEvidence();
        if (disposeDuringFreshRead) {
          api.reads.first.complete(completedLocation);
          await tester.pump();
          expect(api.reads, hasLength(2));
          unawaited(controller.refreshStorageLocation());
        }
        controller.dispose();
        api.reads.last.completeError(
          StateError('statistics returned after disposal'),
        );
        await refresh;
        await controller.refreshStorageLocation();

        expect(api.reads, hasLength(disposeDuringFreshRead ? 2 : 1));
        expect(controller.storageLocation, same(pendingLocation));
        expect(controller.previousStorageLocation, isNull);
        expect(controller.storageLocationFailed, isFalse);
        expect(
          controller.storageCleanupNotice,
          'settings.storage.cleanup_pending',
        );
      },
    );
  }
  test('cleanup acceptance does not await held optional statistics', () async {
    final fixture = PreviewControlApi();
    final api = _HeldCleanupStatisticsApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    controller.data = await fixture.loadDashboard();
    final pending = controller.cleanupExpiredEvidence();
    await api.entered.future;
    try {
      final outcome = await pending.timeout(const Duration(milliseconds: 100));
      expect(outcome.deleted, isTrue);
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
    } finally {
      api.returned.complete(await fixture.storageLocation());
      await pending;
    }
  });
  test(
    'completed managed cleanup replaces pending notice only with certificate',
    () async {
      final fixture = PreviewControlApi();
      final api = _CompletedCleanupApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      controller.data = await fixture.loadDashboard();
      await controller.cleanupExpiredEvidence();
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_pending',
      );
      api.completed = true;
      await controller.refreshStorageLocation();
      expect(
        controller.storageCleanupNotice,
        'settings.storage.cleanup_partial',
      );
      expect(controller.storageLocationFailed, isFalse);
    },
  );
  test('pending cleanup never reports physical completion', () async {
    final fixture = PreviewControlApi();
    final controller = WorkbenchController(
      api: _PendingCleanupApi(fixture),
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    controller.data = await fixture.loadDashboard();
    final outcome = await controller.cleanupExpiredEvidence();
    expect(outcome.deleted, isTrue);
    expect(controller.storageCleanupNotice, 'settings.storage.cleanup_pending');
    expect(controller.storageCleanupError, isNull);
  });
  for (final clear in [false, true]) {
    test(
      'explicit deletion invalidates late nested inspection $clear',
      () async {
        final fixture = PreviewControlApi();
        final api = _DeletionInspectionApi(fixture);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        final target = controller.data!.captures.firstWhere((c) => !c.running);
        await controller.selectCapture(target.key);
        final id = controller.selectedCapturePage!.items.first.id;
        final pending = controller.loadToolResults(id, 'call', cursor: 'old');
        final expected = expectLater(pending, throwsA(isA<ControlProblem>()));
        final outcome = clear
            ? await controller.clearEvidence()
            : await controller.deleteCapture(target.key);
        expect(outcome?.deleted, isTrue);
        api.tools.complete(
          const ToolResultPage(items: [], nextCursor: 'stale'),
        );
        await expected;
      },
    );
  }

  test(
    'initial dashboard and pending approvals complete while optional detail waits',
    () async {
      final fixture = PreviewControlApi();
      final api = _HeldInitialDetailApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      final initialized = controller.initialize();
      await api.entered.future;
      var completed = false;
      initialized.then((_) {
        completed = true;
      });
      await Future<void>.delayed(const Duration(milliseconds: 50));
      expect(controller.data, isNotNull);
      expect(controller.loading, isFalse);
      expect(api.approvalReads, greaterThan(0));
      expect(completed, isTrue);
      final pending = controller.pendingApprovals!.first;
      expect(
        await controller.decideApproval(pending, pending.choices.first),
        isTrue,
      );
      expect(api.decisions, 1);
      api.release.complete();
      await initialized;
    },
  );

  test(
    'Activity inspection invalidation rejects a late detail response',
    () async {
      final delegate = PreviewControlApi();
      final api = _ActivityInspectionApi(delegate, 409);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: delegate.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await controller.selectCapture('managed_run:run-2');
      final id = controller.selectedCapturePage!.items.first.id;
      api.exchangeGate = Completer<ExchangeDetail>();
      final load = controller.loadExchangeDetail(id);
      await expectLater(
        controller.loadToolResults(id, 'call', cursor: 'old'),
        throwsA(isA<ControlProblem>()),
      );
      api.exchangeGate!.complete(await delegate.exchange(id));
      expect(await load, isNull);
      expect(controller.exchangeDetail(id), isNull);
    },
  );

  for (final status in [409, 422, 503]) {
    for (final domain in ['activities', 'content', 'tool']) {
      test(
        'Activity inspection clears stale pages and nested caches $domain $status',
        () async {
          final delegate = PreviewControlApi();
          final api = _ActivityInspectionApi(delegate, status);
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: true,
            closeRuntime: delegate.close,
          );
          addTearDown(controller.dispose);
          await controller.initialize();
          await controller.selectCapture('managed_run:run-2');
          final current = controller.selectedCapturePage!;
          expect(current.items, isNotEmpty);
          final id = current.items.first.id;
          await controller.loadExchangeDetail(id);
          expect(controller.exchangeDetail(id), isNotNull);
          final capture = controller.selectedCapture;
          final assignment = controller.selectedAssignment;
          final approvals = controller.pendingApprovals;
          if (domain == 'activities') {
            await controller.loadMoreSelectedCapture();
          } else if (domain == 'content') {
            await expectLater(
              controller.loadExchangeContentPage(id, 'old-source-content'),
              throwsA(isA<ControlProblem>()),
            );
          } else {
            await expectLater(
              controller.loadToolResults(id, 'call', cursor: 'old-source-tool'),
              throwsA(isA<ControlProblem>()),
            );
          }
          expect(controller.selectedCapturePage, isNull);
          expect(controller.selectedCaptureConversations, isNull);
          expect(controller.exchangeDetail(id), isNull);
          expect(controller.selectedCapture, same(capture));
          expect(controller.selectedAssignment, same(assignment));
          expect(controller.pendingApprovals, same(approvals));
          if (status == 409 || status == 503) {
            expect(
              controller.errorMessage,
              isNull,
              reason:
                  'Optional history failures stay scoped to Capture records',
            );
            expect(
              controller.captureInspectionFailure?.messageKey,
              status == 503
                  ? 'error.inspection_unavailable'
                  : 'error.inspection_changed',
            );
          }
        },
      );
    }
  }

  for (final domain in ['connections', 'egress']) {
    for (final status in [409, 422]) {
      test(
        'network continuation resets retained audit pages $domain $status',
        () async {
          final delegate = PreviewControlApi();
          final api = _NetworkInspectionApi(delegate, failureStatus: status);
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: true,
            closeRuntime: delegate.close,
          );
          addTearDown(controller.dispose);
          await controller.initialize();
          await controller.refreshNetwork();
          final original = controller.networkData!;
          expect(original.connections.items, isNotEmpty);
          expect(original.egressAttempts.items, isNotEmpty);
          if (domain == 'connections') {
            await controller.loadMoreConnections();
          } else {
            await controller.loadMoreEgressAttempts();
          }
          final retained = controller.networkData!;
          expect(retained.connections.items, isEmpty);
          expect(retained.connections.nextCursor, isNull);
          expect(retained.egressAttempts.items, isEmpty);
          expect(retained.egressAttempts.nextCursor, isNull);
          expect(retained.approvals, same(original.approvals));
          expect(retained.rules, same(original.rules));
          expect(controller.networkError, isNotNull);
          await controller.refreshNetwork();
          expect(controller.networkData!.connections.items, isNotEmpty);
          expect(
            controller.networkData!.connections.nextCursor,
            'connections-2',
          );
        },
      );
    }
  }
  test('network continuation preserves empty advancing pages', () async {
    final delegate = PreviewControlApi();
    final api = _NetworkInspectionApi(delegate);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: delegate.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await controller.refreshNetwork();
    final original = controller.networkData!;
    await controller.loadMoreConnections();
    await controller.loadMoreEgressAttempts();
    expect(
      controller.networkData!.connections.items.length,
      original.connections.items.length,
    );
    expect(
      controller.networkData!.egressAttempts.items.length,
      original.egressAttempts.items.length,
    );
    expect(controller.networkData!.connections.nextCursor, 'connections-tail');
    expect(controller.networkData!.egressAttempts.nextCursor, 'egress-tail');
  });
  for (final width in [640.0, 1440.0]) {
    testWidgets('Runtime first cause survives failed inventory at $width', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(Size(width, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewControlApi();
      final api = _DashboardRevisionApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
        terminalManagement: false,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.environments,
        ),
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
      expect(find.byKey(const Key('runtime-recording-failure')), findsNothing);
      api.recordingFailure = RuntimePersistenceFailure(
        operation: 'usage',
        reason: 'write_failed',
        at: DateTime.utc(2026, 9, 28, 3),
      );
      await tester.pump(const Duration(seconds: 5));
      await tester.pumpAndSettle();
      expect(controller.runtimeStatus!.healthy, isTrue);
      expect(
        find.byKey(const Key('runtime-recording-failure')),
        findsOneWidget,
      );
      final accounts = controller.data!.accounts;
      api.failInventory = true;
      api.stopProxy = true;
      await tester.pump(const Duration(seconds: 5));
      await tester.pumpAndSettle();
      expect(controller.data!.accounts, same(accounts));
      expect(controller.runtimeStatus!.healthy, isFalse);
      expect(find.byKey(const Key('runtime-storage-failure')), findsOneWidget);
      for (final language in AppLanguage.values) {
        controller.setLanguage(language);
        await controller.refresh();
        await tester.pumpAndSettle();
        final copy = AppCopy.forLanguage(language);
        expect(find.text(copy('status.proxy_stopped')), findsOneWidget);
        expect(
          find.textContaining(copy('status.failure.operation.egress_complete')),
          findsOneWidget,
        );
        expect(controller.errorMessage, 'runtime_unavailable (503)');
        expect(controller.data!.accounts, same(accounts));
        expect(tester.takeException(), isNull);
      }
      // The first launch can fail before any inventory has been loaded.
      controller.data = null;
      await controller.refresh();
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('runtime-storage-failure')), findsOneWidget);
      expect(controller.data, isNull);
      expect(tester.takeException(), isNull);
      api.failInventory = false;
      api.stopProxy = false;
      api.recordingFailure = null;
      await controller.refresh();
      await tester.pumpAndSettle();
      expect(controller.runtimeStatus!.healthy, isTrue);
      expect(find.byKey(const Key('runtime-storage-failure')), findsNothing);
      expect(find.byKey(const Key('runtime-recording-failure')), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    });
  }

  testWidgets(
    'history invalidation preserves a late confirmed Capture assignment',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1420, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewControlApi();
      final api = _SummaryInspectionFailureApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await controller.selectCapture('manual_capture:manual-figma');
      final approvals = controller.pendingApprovals;
      final assignment = await fixture.captureAssignment('managed_run:run-2');
      api.assignmentGate = Completer<CaptureAssignment>();
      api.failSummary = true;
      final selected = controller.selectCapture('managed_run:run-2');
      await tester.pump();
      api.assignmentGate!.complete(assignment);
      await selected;

      expect(controller.selectedCaptureKey, 'managed_run:run-2');
      expect(
        controller.selectedAssignment?.environmentId,
        assignment.environmentId,
      );
      expect(controller.selectedCaptureLaunchIncomplete, isFalse);
      expect(controller.pendingApprovals, same(approvals));
      expect(controller.selectedCaptureConversations, isNull);
      expect(controller.selectedCapturePage, isNull);
      expect(controller.captureDetailError, isNotNull);
      expect(controller.errorMessage, isNull);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();
      expect(
        find.text('Saved history changed. Refresh this view.'),
        findsOneWidget,
      );
      expect(find.text('inspection_changed (409)'), findsNothing);
      expect(find.textContaining('0 turns'), findsNothing);
      expect(
        find.text('Traffic policy information is temporarily unavailable'),
        findsNothing,
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    },
  );

  testWidgets('unavailable history stays scoped to Capture records', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1420, 1000));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewControlApi();
    final api = _SummaryInspectionFailureApi(fixture)
      ..failure = const ControlProblem(
        status: 503,
        reasonCode: 'inspection_unavailable',
        messageKey: 'error.inspection_unavailable',
      );
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await controller.selectCapture('managed_run:run-2');
    expect(controller.selectedCapturePage!.items, isNotEmpty);
    expect(controller.selectedCaptureConversations!.items, isNotEmpty);
    final id = controller.selectedCapturePage!.items.first.id;
    await controller.loadExchangeDetail(id);
    expect(controller.exchangeDetail(id), isNotNull);
    final assignment = controller.selectedAssignment!;
    final approvals = controller.pendingApprovals;
    api.failSummary = true;
    await controller.refresh();
    expect(controller.selectedAssignment, isNotNull);
    expect(
      controller.selectedAssignment?.environmentId,
      assignment.environmentId,
    );
    expect(controller.pendingApprovals, equals(approvals));
    expect(controller.errorMessage, isNull);
    expect(controller.captureDetailError, isNotNull);
    expect(controller.selectedCaptureConversations, isNull);
    expect(controller.selectedCapturePage, isNull);
    expect(controller.exchangeDetail(id), isNull);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    final copy = AppCopy.forLanguage(AppLanguage.english);
    expect(find.text(copy('error.inspection_unavailable')), findsOneWidget);
    expect(find.text('inspection_unavailable (503)'), findsNothing);
    expect(find.textContaining('0 turns'), findsNothing);
    api.failSummary = false;
    await tester.tap(find.byKey(const Key('capture-detail-retry')));
    await tester.pumpAndSettle();
    expect(controller.captureDetailError, isNull);
    expect(controller.selectedCaptureConversations, isNotNull);
    expect(find.text(copy('error.inspection_unavailable')), findsNothing);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  test('Capture control rejection remains a control failure', () async {
    final fixture = PreviewControlApi();
    final api = _SummaryInspectionFailureApi(fixture)
      ..failure = const ControlProblem(
        status: 403,
        reasonCode: 'control_unauthorized',
        messageKey: 'error.control_unauthorized',
      );
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await controller.selectCapture('manual_capture:manual-figma');
    api.failSummary = true;
    await controller.selectCapture('managed_run:run-2');
    expect(controller.errorMessage, 'control_unauthorized (403)');
    expect(controller.captureDetailError, isNotNull);
    expect(controller.selectedCapturePage, isNull);
  });

  for (final assignmentFirst in [true, false]) {
    test(
      'Capture assignment rejection survives ${assignmentFirst ? 'later' : 'earlier'} history invalidation',
      () async {
        final fixture = PreviewControlApi();
        final api = _SummaryInspectionFailureApi(fixture);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        await controller.selectCapture('manual_capture:manual-figma');
        final approvals = controller.pendingApprovals;
        api.assignmentGate = Completer<CaptureAssignment>();
        api.summaryGate = Completer<ExchangeSummary>();
        final selected = controller.selectCapture('managed_run:run-2');
        const rejection = ControlProblem(
          status: 403,
          reasonCode: 'control_unauthorized',
          messageKey: 'error.control_unauthorized',
        );
        const unavailable = ControlProblem(
          status: 503,
          reasonCode: 'inspection_unavailable',
          messageKey: 'error.inspection_unavailable',
        );
        if (assignmentFirst) {
          api.assignmentGate!.completeError(rejection);
          await Future<void>.delayed(Duration.zero);
          api.summaryGate!.completeError(unavailable);
        } else {
          api.summaryGate!.completeError(unavailable);
          await Future<void>.delayed(Duration.zero);
          api.assignmentGate!.completeError(rejection);
        }
        await selected;
        expect(
          controller.errorMessage,
          'control_unauthorized (403)',
          reason:
              'An optional records failure must never hide a rejected control read',
        );
        expect(controller.selectedAssignment, isNull);
        expect(controller.selectedCapturePage, isNull);
        expect(controller.selectedCaptureLaunchIncomplete, isFalse);
        expect(controller.pendingApprovals, same(approvals));
      },
    );
  }

  for (final next in ['capture', 'request', 'dispose']) {
    test(
      'late Capture assignment cannot replace newer $next ownership',
      () async {
        final fixture = PreviewControlApi();
        final api = _SummaryInspectionFailureApi(fixture);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        await controller.selectCapture('manual_capture:manual-figma');
        final original = await fixture.captureAssignment('managed_run:run-2');
        final stale = CaptureAssignment(
          captureKey: original.captureKey,
          captureId: original.captureId,
          captureKind: original.captureKind,
          environmentId: original.environmentId,
          environmentRevision: original.environmentRevision,
          environmentDigest: original.environmentDigest,
          launchEnvironmentRevision: original.launchEnvironmentRevision,
          launchEnvironmentDigest: original.launchEnvironmentDigest,
          revision: 99,
          source: original.source,
          updatedAt: original.updatedAt,
        );
        final gate = Completer<CaptureAssignment>();
        api.assignmentGate = gate;
        api.failSummary = true;
        final selected = controller.selectCapture('managed_run:run-2');
        await Future<void>.delayed(Duration.zero);
        api.assignmentGate = null;
        api.failSummary = false;
        if (next == 'dispose') {
          controller.dispose();
        } else {
          await controller.selectCapture(
            next == 'capture' ? 'managed_run:run-1' : 'managed_run:run-2',
          );
        }
        final retained = controller.selectedAssignment;
        gate.complete(stale);
        await selected;
        expect(controller.selectedAssignment, same(retained));
        if (next != 'dispose') {
          expect(controller.captureDetailError, isNull);
          expect(controller.captureInspectionFailure, isNull);
          expect(controller.errorMessage, isNull);
          expect(controller.selectedCapturePage!.items, isNotEmpty);
        }
      },
    );
  }

  test(
    'old Capture history unavailability cannot clear a newer selection',
    () async {
      final fixture = PreviewControlApi();
      final api = _SummaryInspectionFailureApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      await controller.selectCapture('manual_capture:manual-figma');
      api.summaryGate = Completer<ExchangeSummary>();
      final oldRead = controller.selectCapture('managed_run:run-2');
      await Future<void>.delayed(Duration.zero);
      await controller.selectCapture('managed_run:run-1');
      final current = controller.selectedCapturePage;
      final assignment = controller.selectedAssignment;
      final approvals = controller.pendingApprovals;
      expect(current!.items, isNotEmpty);
      api.summaryGate!.completeError(
        const ControlProblem(
          status: 503,
          reasonCode: 'inspection_unavailable',
          messageKey: 'error.inspection_unavailable',
        ),
      );
      await oldRead;
      expect(controller.selectedCaptureKey, 'managed_run:run-1');
      expect(controller.selectedCapturePage, same(current));
      expect(controller.selectedAssignment, same(assignment));
      expect(controller.pendingApprovals, same(approvals));
      expect(controller.captureInspectionFailure, isNull);
      expect(controller.captureDetailError, isNull);
      expect(controller.errorMessage, isNull);
    },
  );

  testWidgets('Capture detail failure is not an empty or unassigned run', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1420, 1000));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewControlApi();
    final api = _ConversationFailureApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await controller.selectCapture('managed_run:run-2');
    expect(controller.errorMessage, 'runtime_unavailable (503)');
    expect(controller.selectedAssignment, isNotNull);
    expect(controller.selectedCaptureConversations, isNull);
    expect(controller.selectedCaptureLaunchIncomplete, isFalse);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.text('Run details are temporarily unavailable'),
      findsOneWidget,
    );
    expect(find.text('No traffic policy attached'), findsNothing);
    expect(find.textContaining('0 turns'), findsNothing);
    expect(
      find.byKey(const Key('capture-client-compatibility-managed_run:run-2')),
      findsNothing,
    );

    api.fails = false;
    await tester.tap(find.byKey(const Key('capture-detail-retry')));
    await tester.pumpAndSettle();
    expect(controller.errorMessage, isNull);
    expect(controller.selectedActivities, isNotEmpty);
    expect(find.text('Run details are temporarily unavailable'), findsNothing);

    final previous = controller.selectedCaptureConversations;
    final previousActivities = controller.selectedCapturePage;
    api.fails = true;
    await controller.refresh();
    await tester.pumpAndSettle();
    expect(controller.errorMessage, 'runtime_unavailable (503)');
    expect(controller.selectedCaptureConversations, same(previous));
    expect(controller.selectedCapturePage, same(previousActivities));
    expect(find.text('Run details are temporarily unavailable'), findsNothing);
    expect(tester.takeException(), isNull);
    api.fails = false;
    await tester.pump(const Duration(seconds: 6));
    await tester.pumpAndSettle();
    expect(controller.errorMessage, isNull);
    expect(controller.captureDetailError, isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  testWidgets(
    'Capture selection and refresh cannot change directory activity',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1420, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
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
      const key = 'managed_run:run-2';
      final label = find.byKey(const Key('capture-activity-$key'));
      final originalText = tester.widget<Text>(label).data;
      final order = controller.runningCaptures.map((item) => item.key).toList();
      final originalActivity = controller.data!.captures
          .firstWhere((item) => item.key == key)
          .activityAt;
      await controller.selectCapture(key);
      await tester.pumpAndSettle();
      expect(controller.selectedActivities, isNotEmpty);
      expect(tester.widget<Text>(label).data, originalText);
      await controller.refresh();
      await tester.pumpAndSettle();
      expect(controller.selectedCaptureKey, key);
      expect(tester.widget<Text>(label).data, originalText);
      expect(controller.runningCaptures.map((item) => item.key), order);
      expect(controller.selectedCapture!.activityAt, originalActivity);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    },
  );

  for (final scenario in [
    (
      status: 404,
      reason: 'capture_assignment_not_found',
      state: 'finished',
      observed: false,
      incomplete: true,
    ),
    (
      status: 503,
      reason: 'capture_assignment_unavailable',
      state: 'finished',
      observed: false,
      incomplete: false,
    ),
    (
      status: 404,
      reason: 'capture_not_found',
      state: 'finished',
      observed: false,
      incomplete: false,
    ),
    (
      status: 404,
      reason: 'capture_assignment_not_found',
      state: 'created',
      observed: false,
      incomplete: false,
    ),
    (
      status: 404,
      reason: 'capture_assignment_not_found',
      state: 'finished',
      observed: true,
      incomplete: false,
    ),
  ]) {
    testWidgets('unassigned Capture: $scenario', (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewControlApi();
      final api = _AssignmentFailureApi(
        fixture,
        ControlProblem(
          status: scenario.status,
          reasonCode: scenario.reason,
          messageKey: 'error.${scenario.reason}',
        ),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      final dashboard = await fixture.loadDashboard();
      final now = DateTime.utc(2026, 9, 6);
      final capture = CaptureRecord(
        key: 'managed_run:failed-run',
        id: 'failed-run',
        kind: 'managed_run',
        displayName: 'codex',
        state: scenario.state,
        observation: scenario.observed ? 'observed' : 'waiting_for_traffic',
        createdAt: now,
        updatedAt: now,
        managedRun: ManagedRunSummary(
          executableLabel: 'codex',
          cwd: '/workspace/project',
          canonicalExecutablePath: '/usr/local/bin/codex',
          recognition: 'recognized',
          expiresAt: now.add(const Duration(hours: 1)),
          firstObservedAt: scenario.observed ? now : null,
        ),
      );
      controller.data = DashboardData(
        status: dashboard.status,
        captures: [capture, ...dashboard.captures],
        captureNextCursor: null,
        environments: dashboard.environments,
        endpoints: dashboard.endpoints,
        accounts: dashboard.accounts,
      );
      controller.loading = false;
      await controller.selectCapture(capture.key);
      expect(controller.selectedCaptureLaunchIncomplete, scenario.incomplete);
      expect(controller.detailLoading, isFalse);
      if (scenario.incomplete) {
        expect(controller.errorMessage, isNull);
        expect(controller.selectedCapturePage?.items, isEmpty);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: WorkbenchShell(controller: controller),
          ),
        );
        await tester.pumpAndSettle();
        expect(find.text('Launch did not complete'), findsOneWidget);
        expect(find.text('No traffic policy attached'), findsOneWidget);
        expect(
          find.textContaining('capture_assignment_not_found'),
          findsNothing,
        );
        expect(find.text('Client passthrough'), findsNothing);
        expect(tester.takeException(), isNull);
      } else {
        expect(
          controller.errorMessage,
          '${scenario.reason} (${scenario.status})',
        );
      }
      await controller.selectCapture('managed_run:run-1');
      expect(controller.selectedCaptureLaunchIncomplete, isFalse);
      expect(controller.selectedAssignment, isNotNull);
      expect(controller.errorMessage, isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  test(
    'Root trust install uses exact material and reconciles success',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      final installer = _RecordingRootTrustInstaller(
        () => api.setRootTrustForPreview(installed: true),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
        rootTrustManagement: true,
        rootTrustInstaller: installer,
      );
      addTearDown(controller.dispose);

      await controller.refreshRootCA();
      expect(controller.rootCAStatus?.certificatePresent, 'absent');
      expect(await controller.installAndTrustRootCA(), isTrue);
      expect(installer.installed, hasLength(1));
      expect(
        installer.installed.single.fingerprint,
        controller.rootCAStatus?.fingerprint,
      );
      expect(controller.rootCAStatus?.installed, isTrue);
      expect(controller.rootCAGuideIntent, isNull);
    },
  );

  test(
    'an imported but untrusted Root safely retries the same installer',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      final installer = _RecordingRootTrustInstaller(
        () => api.setRootTrustForPreview(installed: true),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
        rootTrustManagement: true,
        rootTrustInstaller: installer,
      );
      addTearDown(controller.dispose);

      api.setRootTrustForPreview(installed: true);
      await controller.refreshRootCA();
      controller.rootCAStatus = controller.rootCAStatus!.copyWith(
        trustDecision: 'untrusted',
      );

      expect(await controller.installAndTrustRootCA(), isTrue);
      expect(installer.installed, hasLength(1));
      expect(controller.rootCAStatus?.installed, isTrue);
    },
  );

  testWidgets('usage refreshes only while the page and app are visible', (
    tester,
  ) async {
    var now = DateTime.utc(2026, 8, 25, 12);
    final fixture = PreviewControlApi();
    final api = _UsageTrackingApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      serverManagement: true,
      terminalManagement: false,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
      ),
      clock: () => now,
    );
    addTearDown(fixture.close);

    await controller.initialize();
    expect(api.usageCalls, 0);

    controller.selectSection(WorkbenchSection.usage);
    await tester.pump();
    await tester.pump();
    expect(api.usageCalls, 1);
    expect(api.usageQueries.single.toQueryParameters(), {
      'from': '2026-08-19',
      'until': '2026-08-26',
      'timeZone': 'UTC',
    });

    expect(controller.usageRangeDays, 7);
    expect(controller.runtimeUsage?.period.from, '2026-08-19');

    await tester.pump(const Duration(seconds: 11));
    await tester.pump();
    expect(api.usageCalls, 1);
    now = now.add(const Duration(seconds: 15));
    await tester.pump(const Duration(seconds: 5));
    await tester.pump();
    expect(api.usageCalls, 2);
    controller.didChangeAppLifecycleState(AppLifecycleState.hidden);
    await tester.pump(const Duration(seconds: 11));
    expect(api.usageCalls, 2);
    controller.selectSection(WorkbenchSection.settings);
    controller.didChangeAppLifecycleState(AppLifecycleState.resumed);
    await tester.pump();
    await tester.pump(const Duration(seconds: 6));
    expect(api.usageCalls, 2);
    controller.selectSection(WorkbenchSection.usage);
    await tester.pump();
    await tester.pump();
    expect(api.usageCalls, 3);
    controller.dispose();
  });

  testWidgets(
    'usage refresh coalesces, survives management failure and retains stale data on error',
    (tester) async {
      final fixture = PreviewControlApi();
      final api = _UsageTrackingApi(fixture)..managementFails = true;
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
        serverManagement: true,
        terminalManagement: false,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.usage,
        ),
      );
      await controller.initialize();
      expect(controller.runtimeUsage, isNotNull);
      expect(controller.usageError, isNull);
      final previous = controller.runtimeUsage;
      api.usageGate = Completer<void>();
      final first = controller.refreshUsage();
      final second = controller.refreshUsage();
      await tester.pump(const Duration(seconds: 11));
      expect(api.usageCalls, 2);
      expect(controller.usageLoading, isTrue);
      expect(controller.runtimeUsage, same(previous));
      final oldGate = api.usageGate!;
      api.usageGate = null;
      await controller.setUsageRange(30);
      expect(controller.usageRangeDays, 30);
      final newReport = controller.runtimeUsage;
      oldGate.complete();
      await first;
      await second;
      expect(controller.runtimeUsage, same(newReport));
      expect(controller.usageLoading, isFalse);
      api.usageFails = true;
      final lastGood = controller.runtimeUsage;
      await controller.refreshUsage();
      expect(controller.runtimeUsage, same(lastGood));
      expect(controller.usageError, isNotNull);
      api.usageFails = false;
      await controller.refreshUsage();
      expect(controller.usageError, isNull);
      expect(
        DateTime.parse(controller.runtimeUsage!.period.until)
            .difference(DateTime.parse(controller.runtimeUsage!.period.from))
            .inDays,
        30,
      );
      controller.dispose();
      await tester.pump();
    },
  );

  testWidgets('periodic dashboard polls never overlap', (tester) async {
    final fixture = PreviewControlApi();
    final api = _BlockingPollApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(fixture.close);

    await controller.initialize();
    await tester.pump(const Duration(seconds: 5));
    expect(api.pendingPolls, 1);

    await tester.pump(const Duration(seconds: 10));
    expect(api.pendingPolls, 1);
    expect(api.maximumConcurrentPolls, 1);

    api.completePolls();
    await tester.pump();
    controller.dispose();
  });

  testWidgets('unchanged inventory polls do not rebuild the workbench', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _DashboardRevisionApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
      ),
    );
    await controller.initialize();
    expect(controller.data!.accounts, isNotEmpty);
    // Initial detail and quota reads complete independently of initialize.
    // Settle their public completion boundaries before counting poll changes.
    await controller.selectCapture(controller.selectedCaptureKey!);
    await controller.ensureProviderAccountQuotas(controller.data!.accounts);
    var notifications = 0;
    controller.addListener(() => notifications++);

    await tester.pump(const Duration(seconds: 5));
    await tester.pump();
    expect(api.dashboardCalls, 2);
    expect(notifications, 0);

    api.changeAccount = true;
    await tester.pump(const Duration(seconds: 5));
    await tester.pump();
    expect(api.dashboardCalls, 3);
    expect(notifications, 1);
    expect(controller.data!.accounts.first.note, 'changed by poll');
    controller.dispose();
  });

  testWidgets(
    'non-inventory sections poll status without reloading inventory',
    (tester) async {
      final fixture = PreviewControlApi();
      final api = _StatusPollTrackingApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
        terminalManagement: false,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.environments,
        ),
      );
      await controller.initialize();
      final dashboardCallsAfterInit = api.dashboardCalls;
      final statusCallsAfterInit = api.statusCalls;

      await tester.pump(const Duration(seconds: 5));
      await tester.pump();
      expect(api.dashboardCalls, dashboardCallsAfterInit);
      expect(api.statusCalls, greaterThan(statusCallsAfterInit));

      controller.selectSection(WorkbenchSection.settings);
      await tester.pump(const Duration(seconds: 5));
      await tester.pump();
      expect(api.dashboardCalls, greaterThan(dashboardCallsAfterInit));
      controller.dispose();
      await tester.pump();
    },
  );

  for (final captureKey in [
    'managed_run:run-1',
    'manual_capture:manual-figma',
  ]) {
    testWidgets(
      'evidence poll propagates content availability alone for $captureKey',
      (tester) async {
        final fixture = PreviewControlApi();
        final api = _AvailabilityEvidenceApi(fixture);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
          terminalManagement: false,
        );
        await controller.initialize();
        await controller.selectCapture(captureKey);
        final before = controller.selectedActivities.first;
        expect(before.contentAvailable, isTrue);
        api.available = false;
        await tester.pump(const Duration(seconds: 1));
        await tester.pump();
        expect(controller.selectedActivities.first.id, before.id);
        expect(controller.selectedActivities.first.status, before.status);
        expect(controller.selectedActivities.first.contentAvailable, isFalse);
        controller.dispose();
      },
    );
  }

  testWidgets('visible evidence refreshes before the inventory poll', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _LiveEvidenceApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
    );
    await controller.initialize();
    await controller.selectCapture('managed_run:run-1');
    final visibleCount = controller.selectedActivities.length;
    expect(visibleCount, greaterThan(0));
    api.hideNewest = false;
    final inventoryCalls = api.dashboardCalls;
    final directoryCalls = api.conversationCalls;

    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(controller.selectedActivities.length, visibleCount + 1);
    expect(api.dashboardCalls, inventoryCalls);
    expect(api.conversationCalls, directoryCalls);
    expect(controller.detailLoading, isFalse);
    expect(controller.captureActivitiesLoading, isFalse);

    final requests = api.activityCalls;
    controller.selectSection(WorkbenchSection.environments);
    await tester.pump(const Duration(seconds: 6));
    expect(
      api.activityCalls,
      requests,
      reason: 'hidden timelines do not fast-poll or load with inventory polls',
    );
    expect(api.conversationCalls, directoryCalls);
    controller.dispose();
  });

  testWidgets(
    'empty live Capture probes once per tick and loads new evidence',
    (tester) async {
      final fixture = PreviewControlApi();
      final api = _IdleCaptureApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
        terminalManagement: false,
      );
      await controller.initialize();
      await controller.selectCapture('manual_capture:manual-figma');
      expect(controller.selectedCaptureConversationKey, isNull);
      final assignments = api.assignmentCalls;
      final conversations = api.conversationCalls;
      final activities = api.activityCalls;

      for (var tick = 0; tick < 4; tick++) {
        await tester.pump(const Duration(seconds: 1));
        await tester.pump();
      }
      expect(api.activityCalls, activities + 4);
      expect(api.assignmentCalls, assignments);
      expect(api.conversationCalls, conversations);

      api.revealEvidence = true;
      await tester.pump(const Duration(seconds: 1));
      await tester.pump();
      expect(api.conversationCalls, greaterThan(conversations));
      expect(controller.selectedCaptureConversations?.items, isNotEmpty);
      controller.dispose();
    },
  );

  testWidgets('manual Capture notices a second independent Exchange', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _GrowingManualApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
    );
    await controller.initialize();
    await controller.selectCapture('manual_capture:manual-figma');
    final oldLatest = controller.captureConversations.first;
    final oldCount = controller.captureConversations.length;
    expect(controller.selectedCaptureConversationKey, oldLatest.key);
    final directoryCalls = api.directoryCalls;

    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(api.directoryCalls, directoryCalls);
    expect(api.unscopedCalls, greaterThan(0));

    api.newestVisible = true;
    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(controller.captureConversations.length, oldCount + 1);
    expect(controller.captureConversations.first.key, isNot(oldLatest.key));
    expect(
      controller.selectedCaptureConversationKey,
      controller.captureConversations.first.key,
    );
    expect(
      controller.selectedActivities.first.id,
      controller.captureConversations.first.latest.id,
    );
    controller.dispose();
  });

  testWidgets('manual Capture keeps an older Exchange selected', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _GrowingManualApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
    );
    await controller.initialize();
    await controller.selectCapture('manual_capture:manual-figma');
    final older = controller.captureConversations[1];
    await controller.selectCaptureConversation(older.key);
    api.newestVisible = true;

    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(controller.captureConversations.first.key, isNot(older.key));
    expect(controller.selectedCaptureConversationKey, older.key);
    controller.dispose();
  });

  testWidgets('hidden workbench pauses polling and catches up on resume', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _LiveEvidenceApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
    );
    await controller.initialize();
    await controller.selectCapture('managed_run:run-1');
    final dashboardCalls = api.dashboardCalls;
    final activityCalls = api.activityCalls;

    tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
    for (var tick = 0; tick < 6; tick++) {
      await tester.pump(const Duration(seconds: 1));
      await tester.pump();
    }
    expect(api.dashboardCalls, dashboardCalls);
    expect(api.activityCalls, activityCalls);

    tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(api.dashboardCalls, greaterThan(dashboardCalls));
    expect(api.activityCalls, greaterThan(activityCalls));
    controller.dispose();
  });

  testWidgets('live evidence reads coalesce and ignore stale selections', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    final api = _LiveEvidenceApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
      terminalManagement: false,
    );
    await controller.initialize();
    await controller.selectCapture('managed_run:run-1');
    final gate = Completer<void>();
    api.gate = gate.future;
    final requests = api.activityCalls;
    await tester.pump(const Duration(seconds: 1));
    await tester.pump(const Duration(seconds: 2));
    expect(api.activityCalls, requests + 1);

    api.gate = null;
    await controller.selectCapture('managed_run:run-2');
    final selected = controller.selectedCapturePage;
    gate.complete();
    await tester.pump();
    expect(controller.selectedCapturePage, same(selected));
    expect(controller.selectedCaptureKey, 'managed_run:run-2');
    controller.dispose();
  });

  test(
    'Capture detail selects one proven conversation instead of mixing agents',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.selectCapture('managed_run:run-1');

      expect(controller.captureConversations.length, greaterThan(2));
      expect(
        controller.captureConversations
            .map((item) => item.conversation.kind)
            .toSet(),
        containsAll(<String>['main', 'agent', 'isolated_subagent']),
      );
      expect(controller.selectedCaptureConversation?.conversation.kind, 'main');
      expect(controller.selectedActivities, isNotEmpty);
      expect(
        controller.selectedActivities.every(
          (activity) =>
              activity.conversation.id ==
              controller.selectedCaptureConversationKey,
        ),
        isTrue,
      );

      final subagent = controller.captureConversations.firstWhere(
        (item) => item.conversation.kind == 'isolated_subagent',
      );
      await controller.selectCaptureConversation(subagent.key);

      expect(controller.selectedCaptureConversationKey, subagent.key);
      expect(controller.selectedActivities, hasLength(1));
      expect(
        controller.selectedActivities.single.conversation.id,
        subagent.key,
      );
    },
  );

  test(
    'Conversation switching keeps its bounded view visible while refreshing',
    () async {
      final fixture = PreviewControlApi();
      final api = _ControlledActivityApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      await controller.selectCapture('managed_run:run-1');
      final main = controller.selectedCaptureConversation!;
      final mainActivityIds = controller.selectedActivities
          .map((activity) => activity.id)
          .toList(growable: false);
      final subagent = controller.captureConversations.firstWhere(
        (item) => item.conversation.kind == 'isolated_subagent',
      );
      await controller.selectCaptureConversation(subagent.key);

      api.blockConversation(main.key);
      final switchBack = controller.selectCaptureConversation(main.key);

      expect(controller.selectedCaptureConversationKey, main.key);
      expect(controller.captureActivitiesLoading, isTrue);
      expect(
        controller.selectedActivities.map((activity) => activity.id),
        mainActivityIds,
      );

      api.releaseConversation();
      await switchBack;
      expect(controller.captureActivitiesLoading, isFalse);
      expect(
        controller.selectedActivities.map((activity) => activity.id),
        mainActivityIds,
      );
    },
  );

  test('native Client Session timeline crosses Capture boundaries', () async {
    final fixture = PreviewControlApi();
    final api = _ControlledActivityApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);

    controller.data = await fixture.loadDashboard();
    await controller.selectCapture('managed_run:run-2');

    final selected = controller.selectedCaptureConversation;
    expect(selected?.conversation.evidence, 'explicit_session');
    final request = api.activityRequests.last;
    expect(request.conversationId, selected?.key);
    expect(request.captureRunId, isNull);
    expect(request.manualCaptureId, isNull);
  });

  test(
    'Raw evidence copies one exact Transform attempt into Code Library',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      const exchangeId = 'run-1-exchange-222';
      expect(controller.rawEvidence(exchangeId), isNull);

      final page = await controller.loadRawEvidence(exchangeId);
      expect(page?.items, hasLength(6));
      expect(page?.items.first.layer, 'client_ingress');
      expect(controller.rawEvidence(exchangeId), same(page));
      expect(controller.rawEvidenceError(exchangeId), isNull);

      final revealed = await controller.revealRawEvidence(
        exchangeId: exchangeId,
        envelopeId: page!.items.first.envelopeId,
      );
      expect(revealed?.body, isNotEmpty);
      expect(
        controller.rawEvidence(exchangeId)?.items.first.bodyBytes,
        greaterThan(0),
      );

      expect(await controller.copyMessageTransformSample(exchangeId), isTrue);
      expect(controller.section, WorkbenchSection.codeLibrary);
      expect(controller.capturedMessageTransformSample?.exchangeId, exchangeId);
      expect(
        controller.capturedMessageTransformSample?.wireProtocol,
        'anthropic_messages',
      );
      expect(
        controller.capturedMessageTransformSample?.sample.request.body,
        contains('claude-sonnet-4-5'),
      );
    },
  );

  test(
    'real Transform inputs load locally with frozen Capture runtime metadata',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.selectCapture('managed_run:run-1');
      final activity = controller.selectedActivities.first;
      final originalSection = controller.section;

      final captured = await controller.loadMessageTransformSample(
        activity.id,
        activity: activity,
      );

      expect(captured?.exchangeId, activity.id);
      expect(captured?.sample.request.body, contains('claude-sonnet-4-5'));
      expect(captured?.sample.response.body, contains('sample'));
      expect(captured?.sample.runtime?.userName, 'mira');
      expect(captured?.sample.runtime?.homeDirectory, '/Users/mira');
      expect(
        captured?.sample.runtime?.workspaceRoot,
        '/Users/mira/Code/vibermate',
      );
      expect(captured?.sample.runtime?.workspaceLabel, 'vibermate');
      expect(captured?.sample.runtime?.operatingSystem, 'darwin');
      expect(captured?.sample.runtime?.timeZone, 'Asia/Singapore');
      expect(
        captured?.sample.runtime?.turnStartedAt,
        DateTime.utc(2026, 8, 10, 9, 41, 56, 1),
      );
      expect(controller.section, originalSection);
      expect(controller.capturedMessageTransformSample, isNull);
    },
  );

  test('Exchange refresh supersedes an older in-flight response', () async {
    final fixtureApi = PreviewControlApi();
    addTearDown(fixtureApi.close);
    const exchangeId = 'run-1-exchange-222';
    final stale = await fixtureApi.exchange(exchangeId);
    final fresh = await fixtureApi.exchange(exchangeId, contentView: 'full');
    final staleLoad = Completer<ExchangeDetail>();
    final freshLoad = Completer<ExchangeDetail>();
    final api = _SequencedExchangeApi([staleLoad.future, freshLoad.future]);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: () async {},
    );
    addTearDown(controller.dispose);

    final first = controller.loadExchangeDetail(exchangeId);
    final refresh = controller.loadExchangeDetail(exchangeId, refresh: true);
    expect(api.exchangeCalls, 2);

    freshLoad.complete(fresh);
    expect(await refresh, same(fresh));
    staleLoad.complete(stale);
    expect(await first, same(stale));

    expect(controller.exchangeDetail(exchangeId), same(fresh));
    expect(controller.exchangeError(exchangeId), isNull);
    expect(controller.exchangeIsLoading(exchangeId), isFalse);
  });

  test(
    'Capture directory loads every stable page without duplicates',
    () async {
      final api = PreviewControlApi(dashboardCaptureLimit: 2);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      expect(controller.data!.captures, hasLength(2));
      expect(controller.data!.captureNextCursor, isNotNull);
      expect(controller.runningCaptures, hasLength(2));

      await controller.loadMoreCaptures();

      final captures = controller.data!.captures;
      expect(captures, hasLength(9));
      expect(captures.map((capture) => capture.key).toSet(), hasLength(9));
      expect(controller.data!.captureNextCursor, isNull);
      expect(controller.runningCaptures, hasLength(8));
      expect(controller.historicalCaptures, hasLength(1));
      expect(controller.captureDirectoryError, isNull);
    },
  );

  test(
    'Capture deletion reconciles the dashboard and repairs a stale selection',
    () async {
      final fixture = PreviewControlApi();
      final api = _DeletionTrackingApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      final target = controller.data!.captures.firstWhere(
        (capture) => !capture.running,
      );
      controller.selectedCaptureKey = target.key;

      final outcome = await controller.deleteCapture(target.key);

      expect(outcome?.deleted, isTrue);
      expect(api.dashboardLoads, 1);
      expect(
        controller.data!.captures.any((capture) => capture.key == target.key),
        isFalse,
      );
      expect(controller.selectedCaptureKey, isNot(target.key));
      expect(
        controller.data!.captures.any(
          (capture) => capture.key == controller.selectedCaptureKey,
        ),
        isTrue,
      );
      expect(controller.inventoryNotice, 'capture_deleted');
    },
  );

  test(
    'Environment deletion reloads authority and cannot leave a retired selection',
    () async {
      final fixture = PreviewControlApi();
      final api = _DeletionTrackingApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      controller.selectedEnvironmentId = 'research';

      final outcome = await controller.deleteEnvironment('research');

      expect(outcome?.deleted, isTrue);
      expect(api.dashboardLoads, 1);
      expect(
        controller.data!.environments.any(
          (environment) => environment.id == 'research',
        ),
        isFalse,
      );
      expect(controller.selectedEnvironmentId, isNot('research'));
      expect(controller.selectedEnvironment, isNotNull);
      expect(controller.inventoryNotice, 'environment_deleted');
    },
  );

  test(
    'Preview Capture cursor walks every bounded page in stable order',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final seen = <String>[];
      String? cursor;

      do {
        final page = await api.captures(cursor: cursor, limit: 2);
        seen.addAll(page.items.map((capture) => capture.key));
        cursor = page.nextCursor;
      } while (cursor != null);

      expect(seen, hasLength(9));
      expect(seen.toSet(), hasLength(9));
      expect(seen.take(8).every((key) => key != 'managed_run:run-8'), isTrue);
      expect(seen.last, 'managed_run:run-8');
    },
  );

  test(
    'Terminal command changes only through an inspected closed operation',
    () async {
      final api = PreviewControlApi();
      final terminal = PreviewTerminalCommandService();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: terminal,
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.refreshTerminalCommand();
      expect(
        controller.terminalCommand!.state,
        TerminalCommandState.notInstalled,
      );
      expect(
        await controller.changeTerminalCommand(
          TerminalCommandOperation.install,
        ),
        isTrue,
      );
      expect(controller.terminalCommand!.state, TerminalCommandState.current);
      expect(controller.terminalCommandNotice, 'terminal.notice.installed');
      expect(
        await controller.changeTerminalCommand(
          TerminalCommandOperation.install,
        ),
        isFalse,
      );
      expect(
        await controller.changeTerminalCommand(TerminalCommandOperation.remove),
        isTrue,
      );
      expect(
        controller.terminalCommand!.state,
        TerminalCommandState.notInstalled,
      );
      expect(controller.terminalCommandNotice, 'terminal.notice.removed');

      final repairService = PreviewTerminalCommandService(
        initial: const TerminalCommandStatus(
          state: TerminalCommandState.targetMissing,
          sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
          targetPath: '/Users/preview/.local/bin/vibermate',
        ),
      );
      final repairController = WorkbenchController(
        api: api,
        terminalCommands: repairService,
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(repairController.dispose);
      await repairController.refreshTerminalCommand();
      expect(
        await repairController.changeTerminalCommand(
          TerminalCommandOperation.repair,
        ),
        isTrue,
      );
      expect(
        repairController.terminalCommand!.state,
        TerminalCommandState.current,
      );
      expect(
        repairController.terminalCommandNotice,
        'terminal.notice.repaired',
      );
    },
  );

  test(
    'rule save remains fenced by the revision the draft started from',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.refreshNetwork();
      final draftBase = controller.networkData!.rules;

      final external = await api.replaceConnectionRules(
        current: draftBase,
        rules: const [
          ConnectionRule(
            id: 'external-change',
            priority: 100,
            decision: 'deny',
            match: 'exact_host',
            host: 'external.example',
            port: null,
          ),
        ],
        mode: 'deny_unknown',
      );
      expect(external.revision, draftBase.revision + 1);
      await controller.refreshNetwork();
      expect(controller.networkData!.rules.revision, external.revision);

      final saved = await controller.replaceConnectionRules(
        base: draftBase,
        rules: const [
          ConnectionRule(
            id: 'stale-draft',
            priority: 1,
            decision: 'allow',
            match: 'exact_host',
            host: 'stale.example',
            port: null,
          ),
        ],
        mode: 'monitor',
      );

      expect(saved, isFalse);
      expect(controller.networkError, 'error.configuration_conflict');
      expect(controller.networkErrorDiagnostic, 'revision_conflict · HTTP 409');
      expect(controller.networkData!.rules.revision, external.revision);
      expect(controller.networkData!.rules.rules.single.id, 'external-change');
    },
  );

  test(
    'Environment review refreshes upstream revisions without changing history',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      controller.selectEnvironment('work');
      final current = controller.selectedEnvironment!;
      final before = current.clientEndpoints.first;
      final plan = before.protocolPlans.first;
      final route = plan.routes.first;
      final account = await api.createProviderAccount(
        id: 'account.replacement',
        displayName: 'Replacement',
        upstreamEndpointId: route.endpointId,
        kind: 'bearer_token',
        secret: 'synthetic-test-token',
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      final dashboard = await api.loadDashboard();
      controller.data = DashboardData(
        status: dashboard.status,
        captures: dashboard.captures,
        captureNextCursor: dashboard.captureNextCursor,
        environments: dashboard.environments,
        accounts: dashboard.accounts,
        endpoints: [
          for (final endpoint in dashboard.endpoints)
            UpstreamEndpoint(
              id: endpoint.id,
              displayName: endpoint.displayName,
              origin: endpoint.origin,
              realmId: endpoint.realmId,
              backendProtocols: endpoint.backendProtocols,
              capabilities: endpoint.capabilities,
              accountKinds: endpoint.accountKinds,
              state: endpoint.state,
              revision: endpoint.revision + 1,
            ),
        ],
      );
      final edited = assignEnvironmentRouteAccount(
        endpoints: current.clientEndpoints,
        clientEndpointId: before.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: account,
      );
      final impact = await controller.reviewSelectedEnvironment(
        EnvironmentDraftInput.fromEnvironment(
          current,
          expectedDraftRevision: 0,
          clientEndpoints: edited,
        ),
      );
      expect(impact, isNotNull, reason: controller.environmentError);
      final candidate = controller.reviewedEnvironmentDraft!.candidate;
      final next = candidate.clientEndpoints.first;
      final nextRoute = next.protocolPlans.first.routes.first;
      expect(
        nextRoute.providerTarget.revision,
        route.providerTarget.revision + 1,
      );
      expect(nextRoute.accountPolicy.fixedAccountId, account.id);
      expect(nextRoute.revision, route.revision + 1);
      expect(next.protocolPlans.first.revision, plan.revision + 1);
      expect(next.revision, before.revision + 1);
      expect(route.providerTarget.revision, 1);
      expect(controller.selectedEnvironment!.digest, current.digest);
      expect(controller.selectedEnvironment!.revision, current.revision);
    },
  );

  test('account link conflict reloads the revoked association', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    final account = controller.data!.accounts.firstWhere(
      (value) => value.id == 'anthropic-lab',
    );
    final endpoint = controller.data!.endpoints.firstWhere(
      (value) => account.isLinkedTo(value.id),
    );
    final external = await api.setProviderAccountAssociation(
      account: account,
      endpoint: endpoint,
      linked: false,
    );

    final linked = await controller.setProviderAccountAssociation(
      account: account,
      endpoint: endpoint,
      linked: false,
    );

    expect(linked, isFalse);
    expect(controller.inventoryError, 'error.account_conflict');
    final current = controller.data!.accounts.singleWhere(
      (value) => value.id == account.id,
    );
    expect(current.associationRevision, external.associationRevision);
    expect(current.isLinkedTo(endpoint.id), isFalse);
    expect(current.credentialEpoch, account.credentialEpoch);
  });

  test('Environment review freezes impact before CAS publish', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);

    await controller.initialize();
    controller.selectEnvironment('work');
    final current = controller.selectedEnvironment!;
    final candidate = EnvironmentDraftInput.fromEnvironment(
      current,
      expectedDraftRevision: 0,
      name: 'Work reviewed',
      contentRecording: const EnvironmentContentRecordingPolicy(
        mode: 'metadata_only',
        retentionDays: 14,
      ),
      policySet: const EnvironmentPolicySet(toolMode: 'review'),
    );

    final impact = await controller.reviewSelectedEnvironment(candidate);
    expect(impact, isNotNull);
    expect(impact!.continuingCaptures, hasLength(6));
    expect(controller.reviewedEnvironmentDraft!.baseRevision, 7);
    expect(controller.reviewedEnvironmentDraft!.draftRevision, 8);
    expect(controller.selectedEnvironment!.name, 'Work');

    final result = await controller.publishReviewedEnvironment();
    expect(result, isNotNull);
    expect(result!.environment.revision, 8);
    expect(result.environment.name, 'Work reviewed');
    expect(result.environment.contentRecording.mode, 'metadata_only');
    expect(result.environment.policySet.toolMode, 'review');
    expect(controller.selectedEnvironment!.revision, 8);
    expect(controller.environmentNotice, 'environment.published');
    expect(controller.reviewedEnvironmentDraft, isNull);
    await expectLater(
      api.environmentDraft('work'),
      throwsA(
        isA<ControlProblem>().having(
          (problem) => problem.reasonCode,
          'reasonCode',
          'environment_draft_not_found',
        ),
      ),
    );
  });

  test('publish conflict preserves the reviewed draft and impact', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    controller.selectEnvironment('work');
    final current = controller.selectedEnvironment!;
    final impact = await controller.reviewSelectedEnvironment(
      EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: 0,
        name: 'Reviewed locally',
      ),
    );
    expect(impact, isNotNull);
    final reviewed = controller.reviewedEnvironmentDraft!;
    await api.saveEnvironmentDraft(
      environmentId: current.id,
      expectedBaseRevision: current.revision,
      input: EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: reviewed.draftRevision,
        name: 'Competing edit',
      ),
    );

    final published = await controller.publishReviewedEnvironment();

    expect(published, isNull);
    expect(controller.environmentError, isNotNull);
    expect(controller.reviewedEnvironmentDraft, same(reviewed));
    expect(controller.reviewedEnvironmentImpact, same(impact));
    expect(controller.selectedEnvironment!.revision, current.revision);
  });

  test('Environment publish rejects a stale base revision', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);

    await controller.initialize();
    controller.selectEnvironment('work');
    final current = controller.selectedEnvironment!;
    final externalDraft = await api.saveEnvironmentDraft(
      environmentId: 'work',
      expectedBaseRevision: current.revision,
      input: EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: 0,
        name: 'External revision',
      ),
    );
    await api.publishEnvironmentDraft('work', externalDraft.draftRevision);

    final impact = await controller.reviewSelectedEnvironment(
      EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: 0,
        name: 'Stale local revision',
      ),
    );
    expect(impact, isNull);
    expect(controller.environmentError, 'error.configuration_conflict');
    expect(
      controller.environmentErrorDiagnostic,
      'revision_conflict · HTTP 409',
    );
    expect(controller.selectedEnvironment!.revision, 7);
  });

  test(
    'frozen Environment evidence loads by exact revision and remains read-only',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      controller.selectEnvironment('work');
      final frozen = controller.selectedEnvironment!;
      final impact = await controller.reviewSelectedEnvironment(
        EnvironmentDraftInput.fromEnvironment(
          frozen,
          expectedDraftRevision: 0,
          name: 'Work current',
        ),
      );
      expect(impact, isNotNull);
      final published = await controller.publishReviewedEnvironment();
      expect(published!.environment.revision, frozen.revision + 1);

      final historical = await controller.inspectEnvironmentRevision(
        frozen.id,
        frozen.revision,
        expectedDigest: frozen.digest,
      );
      expect(historical!.revision, 7);
      expect(controller.section, WorkbenchSection.environments);
      expect(controller.inspectingHistoricalEnvironment, isTrue);
      expect(controller.displayedEnvironment!.revision, 7);
      expect(controller.selectedEnvironment!.revision, 8);
      expect(
        await controller.reviewSelectedEnvironment(
          EnvironmentDraftInput.fromEnvironment(
            controller.selectedEnvironment!,
            expectedDraftRevision: 0,
          ),
        ),
        isNull,
      );

      controller.showCurrentEnvironment();
      expect(controller.inspectingHistoricalEnvironment, isFalse);
      expect(controller.displayedEnvironment!.revision, 8);
    },
  );

  test(
    'frozen Environment digest mismatch is surfaced and never displayed',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      final result = await controller.inspectEnvironmentRevision(
        'work',
        7,
        expectedDigest: List.filled(64, 'f').join(),
      );

      expect(result, isNull);
      expect(controller.historicalEnvironment, isNull);
      expect(controller.environmentError, 'error.control_contract');
      expect(controller.selectedEnvironmentRevision, 7);
    },
  );

  test(
    'preview keeps every published Environment revision inspectable',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final initial = await api.environmentRevision('work', 7);
      final draft = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: initial.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          initial,
          expectedDraftRevision: 0,
          name: 'Work next',
        ),
      );
      final published = await api.publishEnvironmentDraft(
        initial.id,
        draft.draftRevision,
      );

      expect((await api.environmentRevision('work', 7)).name, 'Work');
      expect(
        (await api.environmentRevision(
          'work',
          published.environment.revision,
        )).name,
        'Work next',
      );
      await expectLater(
        api.environmentRevision('work', 999),
        throwsA(
          isA<ControlProblem>().having(
            (problem) => problem.reasonCode,
            'reasonCode',
            'environment_revision_not_found',
          ),
        ),
      );
    },
  );

  test(
    'new Environment uses base revision zero and joins the directory only after publish',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      const input = EnvironmentDraftInput(
        expectedDraftRevision: 0,
        name: 'Local observe',
        state: 'active',
        clientEndpoints: [],
        pluginBindings: [],
        budgetPolicy: EnvironmentBudgetPolicy(id: '', revision: 0),
        contentRecording: EnvironmentContentRecordingPolicy(
          mode: 'metadata_only',
          retentionDays: 30,
        ),
        launchEnvironment: EnvironmentLaunchPolicy.empty(),
        policySet: EnvironmentPolicySet(toolMode: 'observe'),
      );

      final impact = await controller.reviewNewEnvironment(
        'local-observe',
        input,
      );
      expect(impact, isNotNull);
      expect(impact!.baseRevision, 0);
      expect(impact.continuingCaptures, isEmpty);
      expect(
        controller.data!.environments.any(
          (value) => value.id == 'local-observe',
        ),
        isFalse,
      );

      final result = await controller.publishReviewedEnvironment();
      expect(result!.environment.revision, 1);
      expect(controller.selectedEnvironmentId, 'local-observe');
      expect(controller.selectedEnvironment!.name, 'Local observe');
    },
  );

  test(
    'Environment draft allocation remains monotonic after publish consumes the draft',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final initial = (await api.loadDashboard()).environments.firstWhere(
        (environment) => environment.id == 'work',
      );

      final first = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: initial.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          initial,
          expectedDraftRevision: 0,
          name: 'Work allocation one',
        ),
      );
      await api.previewEnvironmentDraft(initial.id, first.draftRevision);
      final published = await api.publishEnvironmentDraft(
        initial.id,
        first.draftRevision,
      );

      final second = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: published.environment.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          published.environment,
          expectedDraftRevision: 0,
          name: 'Work allocation two',
        ),
      );

      expect(first.draftRevision, 8);
      expect(second.draftRevision, first.draftRevision + 1);
    },
  );

  test('code library reads are shared until a successful mutation', () async {
    final fixture = PreviewControlApi();
    final api = _CodeLibraryTrackingApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    addTearDown(fixture.close);

    await Future.wait([controller.codeLibrary(), controller.codeLibrary()]);
    expect(api.codeLibraryCalls, 1);

    await controller.createCodeLibraryCollection(displayName: 'Shared');
    await controller.codeLibrary();
    expect(api.codeLibraryCalls, 2);

    await controller.codeLibrary(refresh: true);
    expect(api.codeLibraryCalls, 3);
  });
}

final class _RecordingRootTrustInstaller implements RootTrustInstaller {
  _RecordingRootTrustInstaller(this.onInstall);

  final void Function() onInstall;
  final List<RootCAMaterial> installed = [];

  @override
  Future<void> install(RootCAMaterial material) async {
    installed.add(material);
    onInstall();
  }
}

final class _CodeLibraryTrackingApi implements ControlApi {
  _CodeLibraryTrackingApi(this.delegate);

  final PreviewControlApi delegate;
  int codeLibraryCalls = 0;

  @override
  Future<CodeLibraryCatalog> codeLibrary() {
    codeLibraryCalls += 1;
    return delegate.codeLibrary();
  }

  @override
  Future<CodeLibraryCollection> createCodeLibraryCollection({
    required String id,
    required String displayName,
  }) => delegate.createCodeLibraryCollection(id: id, displayName: displayName);

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _UsageTrackingApi implements ControlApi {
  _UsageTrackingApi(this.delegate);

  final PreviewControlApi delegate;
  int usageCalls = 0;
  bool managementFails = false;
  bool usageFails = false;
  Completer<void>? usageGate;
  final List<RuntimeUsageQuery> usageQueries = [];

  @override
  Future<DashboardData> loadDashboard() => delegate.loadDashboard();

  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) =>
      delegate.activitySummary(scope);

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) =>
      delegate.accountFacts(id, history: history);

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) => delegate.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    environmentId: environmentId,
    conversationId: conversationId,
  );

  @override
  Future<List<ApprovalRecord>> pendingApprovals() =>
      delegate.pendingApprovals();

  @override
  Future<RuntimeServerAccess> serverAccess() async {
    if (managementFails) throw StateError('management unavailable');
    return delegate.serverAccess();
  }

  @override
  Future<List<RuntimeUser>> runtimeUsers() => delegate.runtimeUsers();

  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) async {
    usageCalls += 1;
    usageQueries.add(query);
    await usageGate?.future;
    if (usageFails) throw StateError('usage unavailable');
    return delegate.runtimeUsage(query);
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _LiveEvidenceApi extends _UsageTrackingApi {
  _LiveEvidenceApi(super.delegate);

  bool hideNewest = true;
  int dashboardCalls = 0;
  int conversationCalls = 0;
  int activityCalls = 0;
  Future<void>? gate;

  @override
  Future<DashboardData> loadDashboard() {
    dashboardCalls++;
    return super.loadDashboard();
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) {
    conversationCalls++;
    return super.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
  }

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    activityCalls++;
    final page = await super.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
    await gate;
    return hideNewest
        ? ActivityPage(
            items: page.items.skip(1).toList(),
            nextCursor: page.nextCursor,
          )
        : page;
  }
}

final class _StatusPollTrackingApi extends _UsageTrackingApi
    implements RuntimeStatusReader {
  _StatusPollTrackingApi(super.delegate);

  int dashboardCalls = 0;
  int statusCalls = 0;

  @override
  Future<DashboardData> loadDashboard() {
    dashboardCalls++;
    return super.loadDashboard();
  }

  @override
  Future<RuntimeStatus> loadRuntimeStatus() {
    statusCalls++;
    return delegate.loadRuntimeStatus();
  }
}

final class _DashboardRevisionApi extends _UsageTrackingApi {
  _DashboardRevisionApi(super.delegate);

  int dashboardCalls = 0;
  bool changeAccount = false;
  bool failInventory = false;
  bool stopProxy = false;
  RuntimePersistenceFailure? recordingFailure;

  @override
  Future<DashboardData> loadDashboard() async {
    dashboardCalls++;
    var dashboard = await super.loadDashboard();
    if (recordingFailure != null || stopProxy) {
      final old = dashboard.status;
      dashboard = DashboardData(
        status: RuntimeStatus(
          ready: !stopProxy,
          productBuild: old.productBuild,
          state: stopProxy ? 'degraded' : old.state,
          host: old.host,
          schemaRevision: old.schemaRevision,
          storage: stopProxy ? 'unavailable' : old.storage,
          storageFailure: stopProxy
              ? RuntimePersistenceFailure(
                  operation: 'egress_complete',
                  reason: 'timeout',
                  at: DateTime.utc(2026, 9, 28, 4),
                )
              : null,
          recordingFailure: recordingFailure,
          environmentProjection: old.environmentProjection,
          unavailableEnvironments: old.unavailableEnvironments,
          offlineHold: old.offlineHold,
          instanceId: old.instanceId,
          startedAt: old.startedAt,
          stoppedAt: old.stoppedAt,
          stopReasonCode: old.stopReasonCode,
        ),
        captures: dashboard.captures,
        captureNextCursor: dashboard.captureNextCursor,
        environments: dashboard.environments,
        endpoints: dashboard.endpoints,
        accounts: dashboard.accounts,
      );
    }
    if (failInventory) {
      throw DashboardUnavailable(
        dashboard.status,
        const ControlProblem(
          status: 503,
          reasonCode: 'runtime_unavailable',
          messageKey: 'error.runtime_unavailable',
        ),
      );
    }
    if (!changeAccount) return dashboard;
    final account = dashboard.accounts.first;
    final updated = ProviderAccount(
      id: account.id,
      displayName: account.displayName,
      note: 'changed by poll',
      noteRevision: account.noteRevision + 1,
      credentialOrigin: account.credentialOrigin,
      linkedEndpointIds: account.linkedEndpointIds,
      associationRevision: account.associationRevision,
      kind: account.kind,
      realmId: account.realmId,
      state: account.state,
      revision: account.revision,
      credentialState: account.credentialState,
      credentialEpoch: account.credentialEpoch,
      setHeaderNames: account.setHeaderNames,
      deleteHeaderNames: account.deleteHeaderNames,
      codexOAuth: account.codexOAuth,
      tokenInfo: account.tokenInfo,
    );
    return DashboardData(
      status: dashboard.status,
      captures: dashboard.captures,
      captureNextCursor: dashboard.captureNextCursor,
      environments: dashboard.environments,
      endpoints: dashboard.endpoints,
      accounts: [updated, ...dashboard.accounts.skip(1)],
    );
  }
}

final class _IdleCaptureApi extends _UsageTrackingApi {
  _IdleCaptureApi(super.delegate);

  bool revealEvidence = false;
  int assignmentCalls = 0;
  int conversationCalls = 0;
  int activityCalls = 0;

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) {
    if (captureKey == 'manual_capture:manual-figma') assignmentCalls++;
    return super.captureAssignment(captureKey);
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) {
    if (manualCaptureId == 'manual-figma') {
      conversationCalls++;
      if (!revealEvidence) {
        return Future.value(
          const ConversationPage(items: [], nextCursor: null),
        );
      }
    }
    return super.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
  }

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) {
    if (manualCaptureId == 'manual-figma' && conversationId == null) {
      activityCalls++;
      if (!revealEvidence) {
        return Future.value(const ActivityPage(items: [], nextCursor: null));
      }
    }
    return super.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
  }
}

final class _GrowingManualApi extends _UsageTrackingApi {
  _GrowingManualApi(super.delegate);

  bool newestVisible = false;
  int directoryCalls = 0;
  int unscopedCalls = 0;

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) async {
    final page = await super.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
    if (manualCaptureId != 'manual-figma') return page;
    directoryCalls++;
    return newestVisible
        ? page
        : ConversationPage(
            items: page.items.skip(1).toList(),
            nextCursor: page.nextCursor,
          );
  }

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    if (manualCaptureId == 'manual-figma' && conversationId == null) {
      unscopedCalls++;
      final page = await super.activities(
        cursor: cursor,
        limit: newestVisible ? limit : limit + 1,
        manualCaptureId: manualCaptureId,
      );
      return newestVisible
          ? page
          : ActivityPage(
              items: page.items.skip(1).take(limit).toList(),
              nextCursor: page.nextCursor,
            );
    }
    return super.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
  }
}

final class _AssignmentFailureApi extends _UsageTrackingApi {
  _AssignmentFailureApi(super.delegate, this.failure);
  final ControlProblem failure;

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) async {
    if (captureKey == 'managed_run:failed-run') throw failure;
    return super.captureAssignment(captureKey);
  }
}

final class _SummaryInspectionFailureApi extends _UsageTrackingApi {
  _SummaryInspectionFailureApi(super.delegate);

  bool failSummary = false;
  ControlProblem failure = const ControlProblem(
    status: 409,
    reasonCode: 'inspection_changed',
    messageKey: 'error.inspection_changed',
  );
  Completer<CaptureAssignment>? assignmentGate;
  Completer<ExchangeSummary>? summaryGate;

  @override
  Future<ExchangeDetail> exchange(
    String id, {
    String contentView = 'incremental',
  }) => delegate.exchange(id, contentView: contentView);

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) {
    if (captureKey == 'managed_run:run-2' && assignmentGate != null) {
      return assignmentGate!.future;
    }
    return super.captureAssignment(captureKey);
  }

  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) {
    if (scope.captureRunId == 'run-2' && summaryGate != null) {
      return summaryGate!.future;
    }
    if (failSummary && scope.captureRunId == 'run-2') {
      return Future.error(failure);
    }
    return super.activitySummary(scope);
  }
}

final class _ConversationFailureApi extends _UsageTrackingApi {
  _ConversationFailureApi(super.delegate);
  bool fails = true;

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) {
    if (fails && captureRunId == 'run-2') {
      throw const ControlProblem(
        status: 503,
        reasonCode: 'runtime_unavailable',
        messageKey: 'error.runtime_unavailable',
      );
    }
    return super.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
  }
}

final class _BlockingPollApi implements ControlApi {
  _BlockingPollApi(this.delegate);

  final PreviewControlApi delegate;
  int _dashboardCalls = 0;
  int _concurrentPolls = 0;
  int maximumConcurrentPolls = 0;
  final List<Completer<DashboardData>> _polls = [];

  int get pendingPolls => _polls.length;

  @override
  Future<DashboardData> loadDashboard() {
    _dashboardCalls += 1;
    if (_dashboardCalls == 1) return delegate.loadDashboard();
    _concurrentPolls += 1;
    if (_concurrentPolls > maximumConcurrentPolls) {
      maximumConcurrentPolls = _concurrentPolls;
    }
    final result = Completer<DashboardData>();
    _polls.add(result);
    return result.future.whenComplete(() => _concurrentPolls -= 1);
  }

  void completePolls() {
    final dashboard = delegate.loadDashboard();
    for (final poll in List<Completer<DashboardData>>.from(_polls)) {
      unawaited(dashboard.then(poll.complete));
    }
    _polls.clear();
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) => delegate.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    environmentId: environmentId,
    conversationId: conversationId,
  );

  @override
  Future<List<ApprovalRecord>> pendingApprovals() =>
      delegate.pendingApprovals();

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _SequencedExchangeApi implements ControlApi {
  _SequencedExchangeApi(this._responses);

  final List<Future<ExchangeDetail>> _responses;
  int exchangeCalls = 0;

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) {
    final index = exchangeCalls++;
    if (index >= _responses.length) {
      throw StateError('Unexpected exchange request $index');
    }
    return _responses[index];
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _ControlledActivityApi implements ControlApi {
  _ControlledActivityApi(this.delegate);

  final PreviewControlApi delegate;
  String? _blockedConversation;
  Completer<void>? _release;
  final activityRequests =
      <
        ({
          String? captureRunId,
          String? manualCaptureId,
          String? conversationId,
        })
      >[];

  void blockConversation(String key) {
    _blockedConversation = key;
    _release = Completer<void>();
  }

  void releaseConversation() {
    _release?.complete();
    _release = null;
    _blockedConversation = null;
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) =>
      delegate.activitySummary(scope);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    activityRequests.add((
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      conversationId: conversationId,
    ));
    final release = conversationId == _blockedConversation ? _release : null;
    if (release != null) await release.future;
    return delegate.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _DeletionTrackingApi implements ControlApi {
  _DeletionTrackingApi(this.delegate);

  final PreviewControlApi delegate;
  int dashboardLoads = 0;

  @override
  Future<DashboardData> loadDashboard() {
    dashboardLoads += 1;
    return delegate.loadDashboard();
  }

  @override
  Future<DeletionOutcome> deleteCapture(String captureKey) =>
      delegate.deleteCapture(captureKey);

  @override
  Future<DeletionOutcome> deleteEnvironment(String environmentId) =>
      delegate.deleteEnvironment(environmentId);

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) => delegate.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    environmentId: environmentId,
    conversationId: conversationId,
  );

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _NetworkInspectionApi extends _UsageTrackingApi {
  _NetworkInspectionApi(super.delegate, {this.failureStatus});

  final int? failureStatus;
  int networkLoads = 0;

  @override
  Future<NetworkData> loadNetwork() async {
    networkLoads++;
    final original = await delegate.loadNetwork();
    return NetworkData(
      approvals: original.approvals,
      rules: original.rules,
      connections: ConnectionPage(
        items: original.connections.items,
        nextCursor: 'connections-$networkLoads',
      ),
      egressAttempts: EgressAttemptPage(
        items: original.egressAttempts.items,
        nextCursor: 'egress-$networkLoads',
      ),
    );
  }

  void check() {
    if (failureStatus case final status?) {
      throw ControlProblem(
        status: status,
        reasonCode: status == 409
            ? 'inspection_changed'
            : 'invalid_control_request',
        messageKey: 'error.inspection_changed',
      );
    }
  }

  @override
  Future<ConnectionPage> connections({String? cursor, int limit = 50}) async {
    check();
    return const ConnectionPage(items: [], nextCursor: 'connections-tail');
  }

  @override
  Future<EgressAttemptPage> egressAttempts({
    String? cursor,
    int limit = 50,
  }) async {
    check();
    return const EgressAttemptPage(items: [], nextCursor: 'egress-tail');
  }
}

final class _ActivityInspectionApi extends _UsageTrackingApi {
  Completer<ExchangeDetail>? exchangeGate;
  _ActivityInspectionApi(super.delegate, this.status);
  final int status;
  Never fail() => throw ControlProblem(
    status: status,
    reasonCode: status == 409
        ? 'inspection_changed'
        : status == 503
        ? 'inspection_unavailable'
        : 'invalid_control_request',
    messageKey: status == 503
        ? 'error.inspection_unavailable'
        : 'error.inspection_changed',
  );
  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    if (cursor != null) fail();
    final page = await super.activities(
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
    return ActivityPage(items: page.items, nextCursor: 'source-bound-next');
  }

  @override
  Future<ExchangeDetail> exchange(
    String id, {
    String contentView = 'incremental',
  }) => exchangeGate?.future ?? delegate.exchange(id, contentView: contentView);
  @override
  Future<ExchangeContentPage> exchangeContentPage(
    String id,
    String cursor,
  ) async => fail();
  @override
  Future<ToolResultPage> toolResults(
    String id,
    String callId, {
    String? cursor,
  }) async => fail();
}

final class _HeldInitialDetailApi extends _UsageTrackingApi {
  _HeldInitialDetailApi(super.delegate);
  final entered = Completer<void>();
  final release = Completer<void>();
  int approvalReads = 0;
  int decisions = 0;
  @override
  Future<ApprovalRecord> decideApproval({
    required ApprovalRecord approval,
    required ApprovalChoice choice,
  }) {
    decisions += 1;
    return delegate.decideApproval(approval: approval, choice: choice);
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) async {
    if (!entered.isCompleted) entered.complete();
    await release.future;
    return super.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
  }

  @override
  Future<List<ApprovalRecord>> pendingApprovals() {
    approvalReads += 1;
    return super.pendingApprovals();
  }
}

final class _DeletionInspectionApi extends _UsageTrackingApi {
  _DeletionInspectionApi(super.delegate);
  final tools = Completer<ToolResultPage>();
  @override
  Future<DeletionOutcome> deleteCapture(String key) =>
      delegate.deleteCapture(key);
  @override
  Future<DeletionOutcome> clearEvidence() async => const DeletionOutcome(
    deleted: true,
    holderCount: 0,
    holders: [],
    released: null,
  );
  @override
  Future<ToolResultPage> toolResults(
    String id,
    String callId, {
    String? cursor,
  }) => tools.future;
}

final class _PendingCleanupApi extends _UsageTrackingApi {
  _PendingCleanupApi(super.delegate);
  @override
  Future<RuntimeStorageLocation> storageLocation() =>
      delegate.storageLocation();
  @override
  Future<DeletionOutcome> cleanupExpiredEvidence() async =>
      const DeletionOutcome(
        deleted: true,
        holderCount: 0,
        holders: [],
        released: DeletionReleased(
          exchanges: 0,
          envelopes: 0,
          activities: 0,
          connections: 0,
          attempts: 0,
          approvals: 0,
          assignments: 0,
          captures: 0,
          cleanupPending: true,
          countsIncomplete: true,
          privacyMetadataRetained: true,
        ),
      );
}

final class _CompletedCleanupApi extends _PendingCleanupApi {
  _CompletedCleanupApi(super.delegate);
  bool completed = false;
  @override
  Future<RuntimeStorageLocation> storageLocation() async {
    final value = await delegate.storageLocation();
    final counts = <String, Object>{
      'exchanges': 0,
      'envelopes': 0,
      'activities': 0,
      'connections': 0,
      'attempts': 0,
      'approvals': 0,
      'assignments': 0,
      'captures': 0,
      'cleanupPending': !completed,
      'countsIncomplete': true,
      'privacyMetadataRetained': true,
    };
    return RuntimeStorageLocation.fromJson({
      'backend': 'sqlite',
      'dataDirectory': value.dataDirectory,
      'databasePath': value.databasePath,
      'collectedAt': value.collectedAt.toIso8601String(),
      'databaseBytes': value.databaseBytes,
      'walBytes': value.walBytes,
      'sharedMemoryBytes': value.sharedMemoryBytes,
      'evidenceBytes': value.evidenceBytes,
      'reusableBytes': value.reusableBytes,
      'filesystemAvailableBytes': value.filesystemAvailableBytes,
      'lowSpaceThresholdBytes': value.lowSpaceThresholdBytes,
      'capacityState': value.capacityState,
      'cleanupPreview': counts,
      if (completed) 'lastCleanup': {...counts, 'cleanupPending': false},
    });
  }
}

final class _HeldCleanupStatisticsApi extends _PendingCleanupApi {
  _HeldCleanupStatisticsApi(super.delegate);
  final entered = Completer<void>();
  final returned = Completer<RuntimeStorageLocation>();
  @override
  Future<RuntimeStorageLocation> storageLocation() {
    entered.complete();
    return returned.future;
  }
}

final class _QueuedCleanupStatisticsApi extends _PendingCleanupApi {
  _QueuedCleanupStatisticsApi(super.delegate);
  final reads = <Completer<RuntimeStorageLocation>>[];

  @override
  Future<RuntimeStorageLocation> storageLocation() {
    final read = Completer<RuntimeStorageLocation>();
    reads.add(read);
    return read.future;
  }
}

final class _AvailabilityEvidenceApi extends _UsageTrackingApi {
  _AvailabilityEvidenceApi(super.delegate);
  bool available = true;
  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    final page = await super.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
    return ActivityPage(
      items: [
        for (final item in page.items)
          ActivityRecord(
            id: item.id,
            occurredAt: item.occurredAt,
            title: item.title,
            status: item.status,
            reasonCode: item.reasonCode,
            source: item.source,
            conversation: item.conversation,
            environment: item.environment,
            parentRefs: item.parentRefs,
            contentAvailable: available,
          ),
      ],
      nextCursor: page.nextCursor,
    );
  }
}

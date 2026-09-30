import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/capture_overview.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test(
    'unknown directory body availability does not override absent bodies',
    () async {
      final api = _OverviewApi(
        retainBodies: false,
        unknownDirectoryBodies: true,
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.delegate.close,
      );
      addTearDown(controller.dispose);
      controller.data = await api.delegate.loadDashboard();
      await controller.selectCapture('managed_run:run-1');
      expect(controller.selectedActivities, isNotEmpty);
      expect(
        controller.selectedActivities.every(
          (record) => record.contentAvailable == false,
        ),
        isTrue,
      );
      expect(
        controller.captureConversations.every(
          (record) => record.latest.contentAvailable == null,
        ),
        isTrue,
      );
      expect(controller.showCaptureRequestRecords, isFalse);
    },
  );
  test(
    'recorded independent exchanges keep request records as the default',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      controller.data = await api.loadDashboard();
      await controller.selectCapture('managed_run:run-1');
      await controller.selectCapture('manual_capture:manual-figma');
      expect(controller.selectedActivities, isNotEmpty);
      expect(controller.selectedCaptureConversation!.exchangeScoped, isTrue);
      expect(controller.showCaptureRequestRecords, isTrue);
    },
  );
  for (final retain in [false, true]) {
    testWidgets(
      'Capture with content=$retain selects the honest default view',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1280, 800));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(retainConversationBodies: retain);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        await controller.initialize();
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: WorkbenchShell(controller: controller),
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const Key('capture-row-managed_run:run-1')),
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('capture-request-overview')),
          retain ? findsNothing : findsOneWidget,
        );
        if (!retain) {
          expect(find.textContaining('No conversation bodies'), findsOneWidget);
          final completed = controller.selectedCapturePage!;
          final activity = completed.items.first;
          controller.selectedCapturePage = ActivityPage(
            items: [
              ActivityRecord(
                id: 'pending-next-request',
                occurredAt: activity.occurredAt,
                title: activity.title,
                status: 'pending',
                reasonCode: null,
                source: activity.source,
                conversation: activity.conversation,
                environment: activity.environment,
                parentRefs: activity.parentRefs,
              ),
              ...completed.items,
            ],
            nextCursor: completed.nextCursor,
          );
          // A real view rebuild with a new pending request must keep Overview.
          await tester.binding.setSurfaceSize(const Size(1440, 800));
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsOneWidget,
          );
          controller.selectedCapturePage = completed;
          final mode = find.byKey(const Key('capture-evidence-mode'));
          await tester.tap(
            find.descendant(of: mode, matching: find.text('Conversation')),
          );
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsNothing,
          );
          expect(
            find.byKey(const Key('capture-evidence-mode')),
            findsOneWidget,
          );
          // Refresh and navigating away both rebuild the workspace. The
          // selected view belongs to the Capture, not to that widget lifetime.
          await controller.refresh();
          await tester.pumpAndSettle();
          controller.selectSection(WorkbenchSection.environments);
          await tester.pumpAndSettle();
          controller.selectSection(WorkbenchSection.captures);
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsNothing,
          );
          expect(controller.showCaptureRequestRecords, isTrue);
        } else {
          // Recorded bodies must not hide the statistics entry point.
          await tester.tap(
            find.descendant(
              of: find.byKey(const Key('capture-evidence-mode')),
              matching: find.text('Overview'),
            ),
          );
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsOneWidget,
          );
        }
        expect(tester.takeException(), isNull);
        controller.dispose();
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  for (final width in [390.0, 1440.0]) {
    testWidgets('Local overview at $width groups without requiring members', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(Size(width, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(seedRuntimeUsers: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: false,
        serverManagement: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.usage,
        ),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('usage-create-runtime-user')), findsNothing);
      expect(find.byKey(const Key('usage-member-details')), findsNothing);
      expect(find.byKey(const Key('usage-range')), findsOneWidget);
      expect(controller.usageRangeDays, 7);
      expect(find.byKey(const Key('usage-estimated-cost')), findsOneWidget);
      expect(find.text('≥ \$0.0460'), findsWidgets);
      final metric = find.byKey(const Key('usage-trend-metric'));
      await tester.ensureVisible(metric);
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(of: metric, matching: find.text('Estimated cost')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Daily estimated cost · USD'), findsOneWidget);
      final basis = find.byKey(const Key('usage-price-basis'));
      await tester.ensureVisible(basis);
      await tester.pumpAndSettle();
      await tester.tap(basis);
      await tester.pumpAndSettle();
      expect(find.textContaining('not subscription charges'), findsOneWidget);
      await tester.tap(
        find.descendant(
          of: find.byType(AlertDialog),
          matching: find.byType(TextButton),
        ),
      );
      await tester.pumpAndSettle();
      for (final group in ['accounts', 'models', 'sources']) {
        final chip = find.byKey(Key('usage-group-$group'));
        await tester.ensureVisible(chip);
        await tester.pumpAndSettle();
        await tester.tap(chip);
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('usage-breakdown-table')), findsOneWidget);
        expect(tester.takeException(), isNull);
      }
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  for (final (width, language) in [
    (390.0, AppLanguage.simplifiedChinese),
    (1440.0, AppLanguage.english),
  ]) {
    testWidgets('Complete capture overview at $width / $language', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(Size(width, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _OverviewApi();
      final controller = await _overviewController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(overview(controller, language));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      expect(controller.selectedCapturePage!.items.length, lessThan(1357));
      final copy = AppCopy.forLanguage(language);
      expect(
        find.text(copy.format('capture.summary.calls', {'count': '1,357'})),
        findsOneWidget,
      );
      expect(api.queries.first.filters, {'capture': 'run-2'});
      expect(api.queries.first.from, '2025-09-28');
      expect(api.queries.first.until, '2026-09-29');
      expect(api.queries.last.groupBy, 'model');
      expect(api.queries.last.filters, {'capture': 'run-2'});

      await controller.loadMoreSelectedCapture();
      await tester.pumpAndSettle();
      expect(api.scopes.length, 1);
      expect(
        find.text(copy.format('capture.summary.calls', {'count': '1,357'})),
        findsOneWidget,
      );

      await tester.tap(
        find.descendant(
          of: find.byKey(const Key('capture-summary-scope')),
          matching: find.text(copy('capture.summary.session')),
        ),
      );
      await tester.pumpAndSettle();
      final identity =
          controller.selectedCaptureConversation!.conversation.clientIdentity!;
      expect(
        api.scopes.last,
        ActivitySummaryScope(
          client: identity.client,
          sessionId: identity.sessionId,
        ),
      );
      expect(api.queries.last.filters, {
        'client': identity.client,
        'session': identity.sessionId,
      });
      expect(
        find.text(copy.format('capture.summary.calls', {'count': '2,714'})),
        findsOneWidget,
      );
      await tester.ensureVisible(
        find.byKey(const Key('usage-breakdown-table')),
      );
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  testWidgets(
    'Overview rejects stale scopes, polls only while visible, and isolates usage failure',
    (tester) async {
      final api = _OverviewApi()..gate = Completer<ExchangeSummary>();
      final controller = await _overviewController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(overview(controller, AppLanguage.english));
      await tester.pump();
      final initial = api.scopes.single;
      controller.selectCaptureSummarySession(true);
      await tester.pumpAndSettle();
      expect(find.text('2,714 requests'), findsOneWidget);
      api.gate!.complete(api.summary(initial));
      await tester.pumpAndSettle();
      expect(find.text('2,714 requests'), findsOneWidget);
      expect(
        api.queries.every((query) => query.filters.containsKey('session')),
        isTrue,
      );

      final requests = api.scopes.length;
      await tester.pump(const Duration(seconds: 14));
      expect(api.scopes.length, requests);
      api.usageFails = true;
      await tester.pump(const Duration(seconds: 2));
      await tester.pumpAndSettle();
      expect(api.scopes.length, requests + 1);
      expect(find.text('2,714 requests'), findsOneWidget);
      expect(find.textContaining('Refresh failed'), findsOneWidget);
      expect(find.byKey(const Key('capture-usage-updated')), findsOneWidget);

      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
      await tester.pump(const Duration(seconds: 30));
      expect(api.scopes.length, requests + 1);
      api.summaryFails = true;
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pumpAndSettle();
      expect(
        find.textContaining('Could not refresh request totals'),
        findsOneWidget,
      );
      expect(find.text('2,714 requests'), findsOneWidget);
      await tester.pumpWidget(const SizedBox.shrink());
      final finished = api.scopes.length;
      await tester.pump(const Duration(seconds: 30));
      expect(api.scopes.length, finished);
      expect(tester.takeException(), isNull);
    },
  );
}

Future<WorkbenchController> _overviewController(_OverviewApi api) async {
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: api.delegate.close,
  );
  controller.data = await api.delegate.loadDashboard();
  await controller.selectCapture('managed_run:run-2');
  return controller;
}

Widget overview(WorkbenchController controller, AppLanguage language) =>
    MaterialApp(
      theme: language == AppLanguage.english
          ? ViberTheme.light()
          : ViberTheme.dark(),
      home: Scaffold(
        body: AnimatedBuilder(
          animation: controller,
          builder: (_, _) => CaptureRequestOverview(
            controller: controller,
            copy: AppCopy.forLanguage(language),
            withoutBodies: true,
            onShowRecords: () {},
          ),
        ),
      ),
    );

final class _OverviewApi implements ControlApi {
  _OverviewApi({bool retainBodies = true, this.unknownDirectoryBodies = false})
    : delegate = PreviewControlApi(retainConversationBodies: retainBodies);

  final PreviewControlApi delegate;
  final bool unknownDirectoryBodies;
  final scopes = <ActivitySummaryScope>[];
  final queries = <RuntimeUsageQuery>[];
  Completer<ExchangeSummary>? gate;
  bool usageFails = false, summaryFails = false;

  ExchangeSummary summary(ActivitySummaryScope scope) {
    final factor = scope.sessionId.isEmpty ? 1 : 2;
    return ExchangeSummary(
      scope: scope,
      generatedAt: DateTime.utc(2026, 9, 28, 12),
      requests: 1357 * factor,
      succeeded: 1300 * factor,
      failed: 30 * factor,
      canceled: 22 * factor,
      pending: 5 * factor,
      firstObservedAt: DateTime.utc(2026, 9, 26),
      lastObservedAt: DateTime.utc(2026, 9, 28, 12),
      failures: {'provider_transport_failed': 30 * factor},
      otherFailures: 0,
    );
  }

  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) async {
    scopes.add(scope);
    if (summaryFails) throw StateError('unavailable');
    if (scopes.length == 1 && gate != null) return gate!.future;
    return summary(scope);
  }

  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) {
    queries.add(query);
    if (usageFails) throw StateError('unavailable');
    return delegate.runtimeUsage(query);
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
  }) async {
    final page = await delegate.conversations(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
    );
    if (!unknownDirectoryBodies) return page;
    return ConversationPage(
      nextCursor: page.nextCursor,
      items: page.items
          .map((record) {
            final latest = record.latest;
            return ConversationRecord(
              conversation: record.conversation,
              firstObservedAt: record.firstObservedAt,
              turnCount: record.turnCount,
              latest: ActivityRecord(
                id: latest.id,
                occurredAt: latest.occurredAt,
                title: latest.title,
                status: latest.status,
                reasonCode: latest.reasonCode,
                source: latest.source,
                conversation: latest.conversation,
                environment: latest.environment,
                parentRefs: latest.parentRefs,
              ),
            );
          })
          .toList(growable: false),
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

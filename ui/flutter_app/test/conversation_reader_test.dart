import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/conversation_timeline.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('long conversation entry hydrates a bounded visible window', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final all = (await api.fixture.activities(
      captureRunId: 'run-1',
      limit: 224,
    )).items;
    final seed = all.firstWhere((a) => a.status == 'succeeded');
    final rows = all
        .where(
          (a) =>
              a.status == 'succeeded' &&
              a.conversation.id == seed.conversation.id,
        )
        .take(100)
        .toList();
    expect(rows.length, 100);
    final base = await api.exchange(seed.id);
    for (final row in rows) {
      api.details[row.id] = detail(
        base,
        [message('user', 'INPUT ${row.id}')],
        [
          block(
            'text',
            List.filled(18, 'A retained response paragraph.').join('\n\n'),
          ),
        ],
        activity: row,
      );
    }
    api.exchangeCalls.clear();
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, rows));
    await tester.pumpAndSettle();
    expect(
      api.exchangeCalls.toSet().length,
      lessThan(30),
      reason: 'opening a long thread must not fetch every request',
    );
    expect(find.byKey(Key('reader-request-${rows.first.id}')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('returning to a conversation restores its reading position', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final rows = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.where((a) => a.status == 'succeeded').take(8).toList();
    final base = await api.exchange(rows.first.id);
    for (final row in rows) {
      api.details[row.id] = detail(
        base,
        [message('user', 'INPUT ${row.id}')],
        [
          block(
            'text',
            List.filled(12, 'Reading position fixture.').join('\n\n'),
          ),
        ],
        activity: row,
      );
    }
    final controller = makeController(api);
    addTearDown(controller.dispose);
    final storage = PageStorageBucket();
    final child = ValueNotifier(false);
    addTearDown(child.dispose);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: Scaffold(
          body: ValueListenableBuilder<bool>(
            valueListenable: child,
            builder: (_, selected, _) => ConversationReadingView(
              key: ValueKey(selected ? 'child' : 'parent'),
              controller: controller,
              activities: selected ? [rows.last] : rows,
              storage: storage,
              copy: AppCopy.forLanguage(AppLanguage.english),
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    readerPosition(tester).jumpTo(300);
    await tester.pumpAndSettle();
    final offset = readerPosition(tester).pixels;
    child.value = true;
    await tester.pumpAndSettle();
    child.value = false;
    await tester.pumpAndSettle();
    expect(readerPosition(tester).pixels, closeTo(offset, 1));
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  test(
    'request usage filter and cost addition preserve exact scope and overflow',
    () async {
      final query = RuntimeUsageQuery(
        from: '2026-09-01',
        until: '2026-10-01',
        timeZone: 'UTC',
        filters: const {'exchange': 'request-one'},
      );
      expect(query.toQueryParameters()['filter.exchange'], 'request-one');
      expect(
        () => const RuntimeUsageQuery(
          from: '2026-09-01',
          until: '2026-10-01',
          timeZone: 'UTC',
          filters: {'exchange': ''},
        ).toQueryParameters(),
        throwsA(isA<ControlContractException>()),
      );
      final sum = const RuntimeCostEstimate(
        nanoUsd: 9007199254740991,
        pricedCalls: 1,
      ).add(const RuntimeCostEstimate(nanoUsd: 1, pricedCalls: 1));
      expect(sum.nanoUsd, 9007199254740991);
      expect(sum.pricedCalls, 1);
      expect(sum.unpricedCalls, 1);
      expect(sum.partial, isTrue);
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final controller = makeController(api);
      addTearDown(controller.dispose);
      final report = await controller.loadExchangeUsage(activity.id);
      expect(report.total!.agentApiCalls, 1);
      expect(report.filters, {'exchange': activity.id});
      api.broadUsage = true;
      await expectLater(
        controller.loadExchangeUsage(activity.id),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  for (final width in [390.0, 1220.0]) {
    testWidgets(
      'reader isolates checkpoint, tools and instructions at $width',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(width, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _ReaderApi();
        final activity = (await api.fixture.activities(
          captureRunId: 'run-1',
        )).items.firstWhere((a) => a.status == 'succeeded');
        final base = await api.exchange(activity.id);
        final messages = [
          message('developer', 'PRIVATE_INSTRUCTIONS'),
          message('user', 'OLD_INPUT'),
          message('assistant', 'OLD_REPLY'),
        ];
        final response = [
          block('text', 'CURRENT_REPLY'),
          block('reasoning', 'RECORDED_REASONING'),
          tool('tool_call', 'call-current'),
        ];
        api.details[activity.id] = detail(
          base,
          messages,
          response,
          checkpoint: true,
        );
        final controller = makeController(api);
        addTearDown(controller.dispose);
        await tester.pumpWidget(host(controller, [activity]));
        await tester.pumpAndSettle();
        expect(find.text('CURRENT_REPLY'), findsOneWidget);
        expect(find.text('OLD_INPUT'), findsNothing);
        expect(find.text('PRIVATE_INSTRUCTIONS'), findsNothing);
        expect(find.textContaining('RECORDED_REASONING'), findsNothing);
        expect(
          find.byKey(Key('reader-checkpoint-${activity.id}')),
          findsOneWidget,
        );
        final scroll = readerPosition(tester);
        final before = scroll.pixels;
        await tester.tap(find.byKey(Key('reader-context-${activity.id}')));
        await tester.pumpAndSettle();
        expect(find.text('Developer context'), findsOneWidget);
        expect(find.text('PRIVATE_INSTRUCTIONS'), findsNothing);
        await tester.tap(find.text('Developer context'));
        await tester.pumpAndSettle();
        expect(find.text('PRIVATE_INSTRUCTIONS'), findsOneWidget);
        await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
        await tester.pumpAndSettle();
        expect(find.text('OLD_INPUT'), findsOneWidget);
        await tester.tap(find.byKey(const Key('reader-detail-tab-0')));
        await tester.pumpAndSettle();
        expect(
          find.text('PRIVATE_INSTRUCTIONS'),
          findsOneWidget,
          reason: 'instruction expansion survives tab changes',
        );
        await tester.tap(find.byKey(const Key('reader-close-details')));
        await tester.pumpAndSettle();
        expect(scroll.pixels, before);
        await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
        await tester.pumpAndSettle();
        expect(find.textContaining('echo safe'), findsWidgets);
        await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
        await tester.pumpAndSettle();
        expect(find.textContaining('No matching block'), findsOneWidget);
        await tester.tap(find.byKey(const Key('reader-close-details')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(Key('reader-reasoning-${activity.id}')));
        await tester.pumpAndSettle();
        expect(find.textContaining('RECORDED_REASONING'), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  testWidgets(
    'new requests do not replace an open inspector or its reading anchor',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1220, 950));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      api.details[activity.id] = detail(
        base,
        [message('user', 'VISIBLE_INPUT')],
        [block('text', 'ANCHOR_REPLY'), tool('tool_call', 'same-call')],
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      final rows = ValueNotifier([activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-0')));
      await tester.pumpAndSettle();
      final y = tester.getTopLeft(find.text('ANCHOR_REPLY')).dy;
      final next = ActivityRecord(
        id: '${activity.id}-next',
        occurredAt: activity.occurredAt.add(const Duration(seconds: 1)),
        title: activity.title,
        status: 'succeeded',
        reasonCode: null,
        source: activity.source,
        conversation: activity.conversation,
        environment: activity.environment,
        parentRefs: ActivityParentRefs(
          exchangeId: '${activity.id}-next',
          captureRunId: activity.captureRunId,
          manualCaptureId: activity.manualCaptureId,
          connectionId: activity.parentRefs.connectionId,
        ),
      );
      api.details[next.id] = detail(
        base,
        [
          ExchangeContentMessage(
            role: 'tool',
            blocks: [tool('tool_result', 'same-call')],
            agent: null,
          ),
        ],
        [block('text', 'NEW_REPLY')],
        activity: next,
      );
      rows.value = [activity, next];
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('reader-close-details')), findsOneWidget);
      expect(find.textContaining('echo safe'), findsWidgets);
      expect(tester.getTopLeft(find.text('ANCHOR_REPLY')).dy, closeTo(y, 1));
      expect(find.byKey(const Key('reader-new-replies')), findsOneWidget);
      await tester.tap(find.byKey(const Key('reader-close-details')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-new-replies')));
      await tester.pumpAndSettle();
      expect(readerPosition(tester).pixels, 0);
      expect(find.text('NEW_REPLY'), findsOneWidget);
      await tester.ensureVisible(
        find.byKey(Key('reader-tools-${activity.id}')),
      );
      await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
      await tester.pumpAndSettle();
      expect(find.text('MATCHED_TOOL_RESULT'), findsOneWidget);
      expect(
        find.text('Carried in request input · ${next.id}'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'unkeyed results remain unpaired and long inspection survives tab changes',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1220, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      final source =
          '${List.filled(40, 'Retained tool output line.').join('\n')}\nTOOL_TAIL';
      final output = ExchangeContentBlock.fromJson({
        'kind': 'tool_result',
        'availability': 'recorded',
        'originalSize': source.length,
        'text': source,
      }, 'fixture');
      api.details[activity.id] = detail(
        base,
        [
          ExchangeContentMessage(role: 'tool', blocks: [output], agent: null),
        ],
        [block('text', 'REPLY')],
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity]));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
      await tester.pumpAndSettle();
      expect(
        find.text('No call ID was recorded; these blocks cannot be paired.'),
        findsOneWidget,
      );
      final toggle = find.byKey(
        Key('toggle-long-reader-unkeyed-${activity.id}-1-0'),
      );
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(find.text('Show first 15 lines'), findsOneWidget);
      await tester.tap(find.byKey(const Key('reader-detail-tab-0')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
      await tester.pumpAndSettle();
      expect(find.text('Show first 15 lines'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('late context reads cannot replace the selected request', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1300, 1050));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activities = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.where((a) => a.status == 'succeeded').take(2).toList();
    for (var i = 0; i < activities.length; i++) {
      final base = await api.exchange(activities[i].id);
      api.details[base.id] = detail(
        base,
        [message('developer', 'INSTRUCTION_$i')],
        [block('text', 'REPLY_$i')],
        checkpoint: true,
      );
    }
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, activities));
    await tester.pumpAndSettle();
    final pending = Completer<ExchangeDetail>();
    api.delayedFull[activities[0].id] = pending;
    await tester.ensureVisible(
      find.byKey(Key('reader-context-${activities[0].id}')),
    );
    await tester.tap(find.byKey(Key('reader-context-${activities[0].id}')));
    await tester.pump();
    await tester.ensureVisible(
      find.byKey(Key('reader-context-${activities[1].id}')),
    );
    await tester.tap(find.byKey(Key('reader-context-${activities[1].id}')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Developer context'));
    await tester.pumpAndSettle();
    expect(find.text('INSTRUCTION_1'), findsOneWidget);
    pending.complete(api.details[activities[0].id]!);
    await tester.pumpAndSettle();
    expect(find.text('INSTRUCTION_1'), findsOneWidget);
    expect(find.text('INSTRUCTION_0'), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final language in AppLanguage.values) {
    for (final dark in [false, true]) {
      testWidgets(
        'reader supports 390px and 200% text in $language dark=$dark',
        (tester) async {
          await tester.binding.setSurfaceSize(const Size(390, 1050));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = _ReaderApi();
          final activity = (await api.fixture.activities(
            captureRunId: 'run-1',
          )).items.firstWhere((a) => a.status == 'succeeded');
          final base = await api.exchange(activity.id);
          api.details[activity.id] = detail(
            base,
            [message('user', 'INPUT')],
            [block('text', 'REPLY'), tool('tool_call', 'call-one')],
          );
          final controller = makeController(api);
          addTearDown(controller.dispose);
          await tester.pumpWidget(
            host(
              controller,
              [activity],
              language: language,
              dark: dark,
              scale: 2,
            ),
          );
          await tester.pumpAndSettle();
          expect(tester.takeException(), isNull);
          await tester.ensureVisible(
            find.byKey(Key('reader-tools-${activity.id}')),
          );
          await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
          await tester.pumpAndSettle();
          expect(find.textContaining('echo safe'), findsWidgets);
          expect(tester.takeException(), isNull);
          await tester.tap(find.byKey(const Key('reader-close-details')));
          await tester.pumpAndSettle();
          await tester.tap(find.byKey(const Key('reader-usage-summary')));
          await tester.pumpAndSettle();
          expect(tester.takeException(), isNull);
          await tester.pumpWidget(const SizedBox.shrink());
        },
      );
    }
  }

  testWidgets(
    'incremental conversation and request evidence keep their own state',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1220, 950));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      api.details[activity.id] = detail(
        base,
        [message('user', 'NEW_INPUT')],
        [block('text', 'NEW_REPLY')],
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity]));
      await tester.pumpAndSettle();
      expect(find.text('NEW_INPUT'), findsOneWidget);
      expect(find.text('NEW_REPLY'), findsOneWidget);
      expect(
        api.usageQueries.every((q) => q.filters['exchange'] == activity.id),
        isTrue,
      );
      await tester.tap(find.byKey(const Key('reader-usage-summary')));
      await tester.pumpAndSettle();
      expect(find.textContaining('Loaded requests · 1'), findsWidgets);
      expect(
        find.textContaining('not actual subscription charges'),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('reader-close-details')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-request-view')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('conversation-timeline-scroll')),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('reader-return-from-requests')));
      await tester.pumpAndSettle();
      expect(find.text('NEW_INPUT'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );
}

WorkbenchController makeController(ControlApi api) => WorkbenchController(
  api: api,
  terminalCommands: PreviewTerminalCommandService(),
  previewMode: true,
  closeRuntime: api.close,
);

Widget host(
  WorkbenchController controller,
  List<ActivityRecord> activities, {
  AppLanguage language = AppLanguage.english,
  bool dark = true,
  double scale = 1,
}) => MaterialApp(
  theme: dark ? ViberTheme.dark() : ViberTheme.light(),
  builder: (context, child) => MediaQuery(
    data: MediaQuery.of(context).copyWith(textScaler: TextScaler.linear(scale)),
    child: child!,
  ),
  home: Scaffold(
    body: AnimatedBuilder(
      animation: controller,
      builder: (context, _) => ConversationReadingView(
        controller: controller,
        activities: activities,
        copy: AppCopy.forLanguage(language),
      ),
    ),
  ),
);

ScrollPosition readerPosition(WidgetTester tester) => tester
    .state<ScrollableState>(
      find
          .descendant(
            of: find.byKey(const PageStorageKey('conversation-reader-scroll')),
            matching: find.byType(Scrollable),
          )
          .first,
    )
    .position;

ExchangeContentBlock block(String kind, String text) =>
    ExchangeContentBlock.fromJson({
      'kind': kind,
      'availability': 'recorded',
      'text': text,
      'originalSize': text.length,
    }, 'fixture');

ExchangeContentMessage message(String role, String text) =>
    ExchangeContentMessage(
      role: role,
      blocks: [block('text', text)],
      agent: null,
    );

ExchangeContentBlock tool(String kind, String id) =>
    ExchangeContentBlock.fromJson({
      'kind': kind,
      'availability': 'recorded',
      'originalSize': 32,
      'callId': id,
      if (kind == 'tool_call') ...{
        'toolName': 'exec_command',
        'arguments': {'cmd': 'echo safe'},
      },
      if (kind == 'tool_result') 'text': 'MATCHED_TOOL_RESULT',
    }, 'fixture');

ExchangeDetail detail(
  ExchangeDetail base,
  List<ExchangeContentMessage> messages,
  List<ExchangeContentBlock> output, {
  bool checkpoint = false,
  ActivityRecord? activity,
}) {
  final request = base.content.request!, response = base.content.response!;
  return ExchangeDetail(
    id: activity?.id ?? base.id,
    status: activity?.status ?? base.status,
    environment: base.environment,
    parentRefs: activity?.parentRefs ?? base.parentRefs,
    diagnosis: base.diagnosis,
    processingTrace: base.processingTrace,
    clientIdentity: base.clientIdentity,
    content: ExchangeContentDetail(
      state: 'recorded',
      mode: 'full',
      recordedAt: base.content.recordedAt,
      expiresAt: base.content.expiresAt,
      agentConversation: base.content.agentConversation,
      requestProjection: ExchangeRequestProjection(
        view: 'incremental',
        relationship: checkpoint ? 'checkpoint' : 'incremental',
        inheritedMessageCount: checkpoint ? 0 : 2,
        totalMessageCount: messages.length + (checkpoint ? 0 : 2),
        fullSnapshotAvailable: !checkpoint,
      ),
      request: ExchangeRequest(
        requestedModel: request.requestedModel,
        effectiveModel: request.effectiveModel,
        maxOutputTokens: request.maxOutputTokens,
        stream: true,
        system: const [],
        messages: messages,
        tools: request.tools,
        protocolEvidence: request.protocolEvidence,
      ),
      response: ExchangeResponse(
        id: response.id,
        requestedModel: response.requestedModel,
        effectiveModel: response.effectiveModel,
        reportedModel: response.reportedModel,
        stopReason: 'end_turn',
        blocks: output,
        usage: response.usage,
        protocolEvidence: response.protocolEvidence,
      ),
    ),
  );
}

final class _ReaderApi implements ControlApi {
  final fixture = PreviewControlApi();
  final details = <String, ExchangeDetail>{};
  final delayedFull = <String, Completer<ExchangeDetail>>{};
  final usageQueries = <RuntimeUsageQuery>[];
  final exchangeCalls = <String>[];
  bool broadUsage = false;
  @override
  Future<ExchangeDetail> exchange(
    String id, {
    String contentView = 'incremental',
  }) async {
    exchangeCalls.add('$contentView:$id');
    if (contentView == 'full' && delayedFull.containsKey(id)) {
      return delayedFull[id]!.future;
    }
    return details[id] ?? await fixture.exchange(id, contentView: contentView);
  }

  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) {
    usageQueries.add(query);
    return fixture.runtimeUsage(
      broadUsage
          ? RuntimeUsageQuery(
              from: query.from,
              until: query.until,
              timeZone: query.timeZone,
            )
          : query,
    );
  }

  @override
  Future<void> close() => fixture.close();

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

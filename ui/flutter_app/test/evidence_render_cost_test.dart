import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
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
  const measure = bool.fromEnvironment('VIBERMATE_PERFORMANCE');
  test(
    'exchange detail cache evicts old large evidence by byte budget',
    () async {
      final fixture = PreviewControlApi();
      addTearDown(fixture.close);
      final activity = (await fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((value) => value.status == 'succeeded');
      final base = await fixture.exchange(activity.id);
      final controller = _controller(
        _withResponse(
          base,
          'text',
          List.filled(5120, 'x'.padRight(1024, 'x')).join(),
        ),
      );
      addTearDown(controller.dispose);

      await controller.loadExchangeDetail('synthetic-first');
      expect(controller.exchangeDetail('synthetic-first'), isNotNull);
      await controller.loadExchangeDetail('synthetic-second');
      expect(controller.exchangeDetail('synthetic-first'), isNull);
      expect(controller.exchangeDetail('synthetic-second'), isNotNull);
    },
  );

  for (final kind in ['text', 'reasoning']) {
    testWidgets('long $kind content renders a bounded preview until expanded', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewControlApi();
      addTearDown(fixture.close);
      final activity = (await fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((value) => value.status == 'succeeded');
      final base = await fixture.exchange(activity.id);
      final source =
          '${List.filled(512, 'Synthetic **paragraph**.\n\n').join()}TAIL_SENTINEL';
      final detail = _withResponse(base, kind, source);
      expect(detail.content.response!.blocks.single.text, source);
      final controller = _controller(detail);
      addTearDown(controller.dispose);
      await controller.loadExchangeDetail(activity.id);
      await tester.pumpWidget(_timeline(controller, activity));
      await tester.pumpAndSettle();

      expect(_renderedText(tester).contains('TAIL_SENTINEL'), isFalse);
      final toggle = find.byKey(
        Key(
          kind == 'reasoning'
              ? 'toggle-thinking-response-${activity.id}-0'
              : 'toggle-long-response-${activity.id}-0',
        ),
      );
      expect(toggle, findsOneWidget);
      await tester.ensureVisible(toggle);
      await tester.pumpAndSettle();
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(_renderedText(tester).contains('TAIL_SENTINEL'), isTrue);
      if (kind == 'text') {
        expect(
          tester
              .widgetList<MarkdownBody>(find.byType(MarkdownBody))
              .any((widget) => widget.data.contains('TAIL_SENTINEL')),
          isTrue,
        );
      }
      await tester.ensureVisible(toggle);
      await tester.pumpAndSettle();
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(_renderedText(tester).contains('TAIL_SENTINEL'), isFalse);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('omitted large content keeps its ordinary placeholder', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    addTearDown(fixture.close);
    final activity = (await fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((value) => value.status == 'succeeded');
    final base = await fixture.exchange(activity.id);
    final controller = _controller(
      _withResponse(base, 'text', 'x'.padRight(9 * 1024, 'x'), omitted: true),
    );
    addTearDown(controller.dispose);
    await controller.loadExchangeDetail(activity.id);
    await tester.pumpWidget(_timeline(controller, activity));
    await tester.pumpAndSettle();

    expect(
      find.byKey(Key('toggle-long-response-${activity.id}-0')),
      findsNothing,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('large plain token keeps its exact selectable text', (
    tester,
  ) async {
    final fixture = PreviewControlApi();
    addTearDown(fixture.close);
    final activity = (await fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((value) => value.status == 'succeeded');
    final base = await fixture.exchange(activity.id);
    final source = '${List.filled(40 * 1024, 'x').join()}_TAIL_SENTINEL';
    final controller = _controller(_withResponse(base, 'text', source));
    addTearDown(controller.dispose);
    await controller.loadExchangeDetail(activity.id);
    await tester.pumpWidget(_timeline(controller, activity));
    await tester.pumpAndSettle();
    final toggle = find.byKey(Key('toggle-long-response-${activity.id}-0'));
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(
      tester
          .widgetList<SelectableText>(find.byType(SelectableText))
          .any((widget) => widget.data == source),
      isTrue,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('large Markdown prose remains selectable through its tail', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewControlApi();
    addTearDown(fixture.close);
    final activity = (await fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((value) => value.status == 'succeeded');
    final base = await fixture.exchange(activity.id);
    final source =
        '${List.filled(1024, 'Synthetic **paragraph** and `code`.\n\n').join()}TAIL_SENTINEL';
    final controller = _controller(_withResponse(base, 'text', source));
    addTearDown(controller.dispose);
    await controller.loadExchangeDetail(activity.id);
    await tester.pumpWidget(_timeline(controller, activity));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(Key('toggle-long-response-${activity.id}-0')));
    await tester.pumpAndSettle();
    final viewer = find.byKey(Key('long-markdown-response-${activity.id}-0'));
    expect(viewer, findsOneWidget);
    final scrollable = find
        .descendant(of: viewer, matching: find.byType(Scrollable))
        .first;
    for (
      var index = 0;
      index < 10 && !_renderedText(tester).contains('TAIL_SENTINEL');
      index++
    ) {
      final position = tester.state<ScrollableState>(scrollable).position;
      position.jumpTo(position.maxScrollExtent);
      await tester.pumpAndSettle();
    }
    expect(_renderedText(tester).contains('TAIL_SENTINEL'), isTrue);
    expect(
      tester
          .widgetList<MarkdownBody>(find.byType(MarkdownBody))
          .every((widget) => widget.selectable),
      isTrue,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('synthetic evidence rendering baseline', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewControlApi();
    addTearDown(fixture.close);
    final activity = (await fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((value) => value.status == 'succeeded');
    final base = await fixture.exchange(activity.id);
    for (final paragraphs in [1, 256, 4096]) {
      final text = List.filled(
        paragraphs,
        'Synthetic **paragraph** and `code`.\n\n',
      ).join();
      final elapsed = <int>[];
      final renderedSizes = <int>[];
      for (var sample = 0; sample < 7; sample++) {
        final controller = _controller(_withResponse(base, 'text', text));
        await controller.loadExchangeDetail(activity.id);
        final watch = Stopwatch()..start();
        await tester.pumpWidget(_timeline(controller, activity));
        await tester.pumpAndSettle();
        elapsed.add(watch.elapsedMicroseconds);
        renderedSizes.add(_renderedText(tester).length);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pumpAndSettle();
        controller.dispose();
      }
      final warm = elapsed.skip(1).toList()..sort();
      // Wall time in the test renderer; not release App frame/GPU timing.
      // ignore: avoid_print
      print(
        'EVIDENCE_RENDER_BASELINE ${jsonEncode({'renderer': kIsWeb ? 'chrome-test' : 'flutter-tester', 'mode': 'debug', 'viewport': '1180x760', 'synthetic': true, 'sourceBytes': utf8.encode(text).length, 'firstMountUs': elapsed.first, 'warmP50Us': warm[(warm.length * .5).ceil() - 1], 'warmP95Us': warm[(warm.length * .95).ceil() - 1], 'samples': elapsed.length, 'renderedCodeUnits': renderedSizes.last})}',
      );
    }
  }, skip: !measure);

  final expansionCases = {
    'unbroken': '${List.filled(151552, 'x').join()}TAIL_SENTINEL',
    'fenced': '```text\n${List.filled(151552, 'x').join()}TAIL_SENTINEL\n```',
    'paragraphs':
        '${List.filled(4096, 'Synthetic **paragraph** and `code`.\n\n').join()}TAIL_SENTINEL',
  };
  for (final entry in expansionCases.entries) {
    testWidgets('${entry.key} 151 KiB expansion baseline', (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewControlApi();
      addTearDown(fixture.close);
      final activity = (await fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((value) => value.status == 'succeeded');
      final base = await fixture.exchange(activity.id);
      final source = entry.value;
      final controller = _controller(_withResponse(base, 'text', source));
      addTearDown(controller.dispose);
      await controller.loadExchangeDetail(activity.id);
      await tester.pumpWidget(_timeline(controller, activity));
      await tester.pumpAndSettle();
      final toggle = find.byKey(Key('toggle-long-response-${activity.id}-0'));
      await tester.ensureVisible(toggle);
      final watch = Stopwatch()..start();
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      watch.stop();
      if (kIsWeb && entry.key != 'fenced') {
        // A generous guard catches the measured 17–34 s regression without
        // turning a synthetic debug run into a product latency promise.
        expect(watch.elapsed, lessThan(const Duration(seconds: 5)));
      }
      if (entry.key == 'fenced') {
        expect(
          find.byKey(Key('long-markdown-response-${activity.id}-0')),
          findsNothing,
        );
      }
      if (entry.key == 'paragraphs') {
        final viewer = find.byKey(
          Key('long-markdown-response-${activity.id}-0'),
        );
        expect(viewer, findsOneWidget);
        final scrollable = find.descendant(
          of: viewer,
          matching: find.byType(Scrollable),
        );
        for (
          var index = 0;
          index < 10 && !_renderedText(tester).contains('TAIL_SENTINEL');
          index++
        ) {
          final position = tester
              .state<ScrollableState>(scrollable.first)
              .position;
          position.jumpTo(position.maxScrollExtent);
          await tester.pumpAndSettle();
        }
      }
      expect(_renderedText(tester).contains('TAIL_SENTINEL'), isTrue);
      // ignore: avoid_print
      print(
        'EVIDENCE_EXPAND_BASELINE ${jsonEncode({'renderer': kIsWeb ? 'chrome-test' : 'flutter-tester', 'shape': entry.key, 'sourceBytes': utf8.encode(source).length, 'expandUs': watch.elapsedMicroseconds})}',
      );
    }, skip: !measure);
  }

  testWidgets('unbroken 151 KiB plain text baseline', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final source = '${List.filled(151552, 'x').join()}TAIL_SENTINEL';
    final watch = Stopwatch()..start();
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: SizedBox(width: 1000, child: SelectableText(source)),
        ),
      ),
    );
    await tester.pumpAndSettle();
    watch.stop();
    expect(find.byType(SelectableText), findsOneWidget);
    // ignore: avoid_print
    print(
      'PLAIN_TEXT_BASELINE ${jsonEncode({'renderer': kIsWeb ? 'chrome-test' : 'flutter-tester', 'sourceBytes': utf8.encode(source).length, 'mountUs': watch.elapsedMicroseconds})}',
    );
  }, skip: !measure);
}

String _renderedText(WidgetTester tester) => [
  ...tester
      .widgetList<MarkdownBody>(find.byType(MarkdownBody))
      .map((widget) => widget.data),
  ...tester
      .widgetList<SelectableText>(find.byType(SelectableText))
      .map((widget) => widget.data ?? ''),
].join('\n');

Widget _timeline(WorkbenchController controller, ActivityRecord activity) =>
    MaterialApp(
      theme: ViberTheme.dark(),
      home: Scaffold(
        body: AnimatedBuilder(
          animation: controller,
          builder: (context, _) => EvidenceConversationTimeline(
            controller: controller,
            activities: [activity],
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );

WorkbenchController _controller(ExchangeDetail detail) => WorkbenchController(
  api: _DetailApi(detail),
  terminalCommands: PreviewTerminalCommandService(),
  previewMode: true,
  closeRuntime: () async {},
);

ExchangeDetail _withResponse(
  ExchangeDetail base,
  String kind,
  String text, {
  bool omitted = false,
}) {
  final content = base.content;
  final response = content.response!;
  return ExchangeDetail(
    id: base.id,
    status: base.status,
    environment: base.environment,
    parentRefs: base.parentRefs,
    diagnosis: base.diagnosis,
    processingTrace: base.processingTrace,
    clientIdentity: base.clientIdentity,
    content: ExchangeContentDetail(
      state: content.state,
      mode: content.mode,
      recordedAt: content.recordedAt,
      expiresAt: content.expiresAt,
      requestProjection: content.requestProjection,
      agentConversation: content.agentConversation,
      request: content.request,
      response: ExchangeResponse(
        id: response.id,
        requestedModel: response.requestedModel,
        effectiveModel: response.effectiveModel,
        reportedModel: response.reportedModel,
        stopReason: response.stopReason,
        usage: response.usage,
        protocolEvidence: response.protocolEvidence,
        blocks: [
          ExchangeContentBlock.fromJson({
            'kind': kind,
            'availability': omitted ? 'omitted' : 'recorded',
            if (!omitted) 'text': text,
            'originalSize': utf8.encode(text).length,
            if (kind == 'reasoning') 'providerKind': 'thinking',
          }, 'synthetic'),
        ],
      ),
    ),
  );
}

final class _DetailApi implements ControlApi {
  _DetailApi(this.detail);
  final ExchangeDetail detail;

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) async => detail;

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

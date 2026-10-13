import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
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
  test(
    'tool lookup accepts a scoped deferred result, not unrelated content',
    () {
      final json = {
        'schema': 'vibermate-tool-results/v1',
        'originExchangeId': 'origin',
        'callId': 'call',
        'items': [
          {
            'exchangeId': 'later',
            'block': {
              'kind': 'deferred',
              'availability': 'recorded',
              'originalSize': 0,
              'deferred': {
                'exchangeId': 'later',
                'cursor': 'body-cursor',
                'estimatedBytes': 300000,
              },
            },
          },
        ],
      };
      expect(
        ToolResultPage.fromJson(json, 'origin', 'call').items,
        hasLength(1),
      );
      expect(
        () => ToolResultPage.fromJson(json, 'other-origin', 'call'),
        throwsA(isA<ControlContractException>()),
      );
      (json['items'] as List).first['exchangeId'] = 'other-exchange';
      expect(
        () => ToolResultPage.fromJson(json, 'origin', 'call'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  testWidgets('checkpoint input keeps text and two previewable attachments', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    ExchangeContentBlock image(String hex) => ExchangeContentBlock.fromJson({
      'kind': 'provider_extension',
      'availability': 'omitted',
      'originalSize': 100,
      'providerSource': 'openai_responses',
      'providerKind': 'input_image',
      'fingerprint': 'sha256:${List.filled(64, hex).join()}',
    }, 'image');
    api.details[activity.id] = detail(
      base,
      [
        ExchangeContentMessage(
          role: 'user',
          blocks: [
            block('text', '<image name=[Image #1] path="/PRIVATE_TEMP_PATH">'),
            image('a'),
          ],
          agent: null,
        ),
        ExchangeContentMessage(role: 'user', blocks: [image('b')], agent: null),
        message('user', 'MY QUESTION WITH TWO PICTURES'),
      ],
      [block('text', 'REPLY')],
      checkpoint: true,
      inputAnchor: List.filled(64, 'c').join(),
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity]));
    await tester.pumpAndSettle();
    expect(find.text('MY QUESTION WITH TWO PICTURES'), findsOneWidget);
    expect(find.textContaining('PRIVATE_TEMP_PATH'), findsNothing);
    expect(api.imageCalls.length, 2);
    expect(find.byType(Image), findsNWidgets(2));
    final thumbnail = find.byKey(
      ValueKey('image-thumbnail-sha256:${List.filled(64, 'a').join()}'),
    );
    await tester.ensureVisible(thumbnail);
    await tester.tap(thumbnail);
    await tester.pumpAndSettle();
    expect(find.byType(InteractiveViewer), findsOneWidget);
    await tester.tap(find.byKey(const Key('image-preview-close')));
    await tester.pumpAndSettle();
    expect(find.text('MY QUESTION WITH TWO PICTURES'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final imagePayload in [false, true]) {
    testWidgets('wire tool return does not invent User image=$imagePayload', (
      tester,
    ) async {
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      final result = imagePayload
          ? ExchangeContentBlock.fromJson({
              'kind': 'tool_result',
              'availability': 'recorded',
              'originalSize': 32,
              'callId': 'claude-return',
              'providerKind': 'input_image',
              'text': 'TOOL_IMAGE_PAYLOAD',
            }, 'fixture')
          : tool('tool_result', 'claude-return');
      api.details[activity.id] = detail(
        base,
        [
          ExchangeContentMessage(role: 'user', blocks: [result], agent: null),
        ],
        [block('text', 'DONE')],
        checkpoint: true,
        inputAnchor: List.filled(64, 'c').join(),
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity]));
      await tester.pumpAndSettle();
      expect(find.text('User'), findsNothing);
      expect(find.byType(Image), findsNothing);
      await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
      await tester.pumpAndSettle();
      expect(
        find.text(imagePayload ? 'TOOL_IMAGE_PAYLOAD' : 'MATCHED_TOOL_RESULT'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  for (final humanKind in ['text', 'refusal', 'image']) {
    testWidgets('wire tool return preserves real $humanKind once', (
      tester,
    ) async {
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      api.details[activity.id] = detail(
        base,
        [
          ExchangeContentMessage(
            role: 'user',
            blocks: [
              tool('tool_result', 'mixed-return'),
              humanKind == 'image'
                  ? readerImage('a')
                  : block(humanKind, 'REAL HUMAN CONTENT'),
            ],
            agent: null,
          ),
        ],
        [block('text', 'DONE')],
        checkpoint: true,
        inputAnchor: List.filled(64, 'c').join(),
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity]));
      await tester.pumpAndSettle();
      expect(find.text('User'), findsOneWidget);
      expect(find.text('MATCHED_TOOL_RESULT'), findsNothing);
      if (humanKind == 'image') {
        expect(find.byType(Image), findsOneWidget);
        expect(
          find.byKey(Key('reader-copy-${activity.id}-input')),
          findsNothing,
        );
      } else {
        expect(find.text('REAL HUMAN CONTENT'), findsOneWidget);
      }
      await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
      await tester.pumpAndSettle();
      expect(find.text('MATCHED_TOOL_RESULT'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  for (final language in AppLanguage.values) {
    for (final verifiedKind in ['text', 'image', 'tool_result']) {
      testWidgets(
        'unproved deferred input stays neutral then loads $verifiedKind in $language',
        (tester) async {
          await tester.binding.setSurfaceSize(const Size(390, 1000));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = _ReaderApi();
          final activity = (await api.fixture.activities(
            captureRunId: 'run-1',
          )).items.firstWhere((a) => a.status == 'succeeded');
          final base = await api.exchange(activity.id);
          api.details[activity.id] = detail(
            base,
            [
              ExchangeContentMessage(
                role: 'unknown',
                blocks: [readerDeferred(activity.id, 'unproved-input')],
                agent: null,
              ),
            ],
            [block('text', 'DONE')],
            checkpoint: true,
            reading: true,
          );
          // R3's unproved message cursor returns canonical blocks, not a
          // manufactured user role or inputAnchor (reading_content.go).
          api.pages['unproved-input'] = ExchangeContentPage(
            exchangeId: activity.id,
            kind: 'message',
            messages: const [],
            blocks: [
              if (verifiedKind == 'image')
                readerImage('a')
              else if (verifiedKind == 'tool_result')
                tool('tool_result', 'verified-return')
              else
                block('text', 'VERIFIED HUMAN TEXT'),
              readerDeferred(activity.id, 'nested-input'),
            ],
            text: '',
            offset: 0,
            total: 2,
          );
          api.pages['nested-input'] = ExchangeContentPage(
            exchangeId: activity.id,
            kind: 'text',
            messages: const [],
            blocks: const [],
            text: 'NESTED SEGMENT',
            offset: 0,
            total: 14,
          );
          final controller = makeController(api);
          addTearDown(controller.dispose);
          final copy = AppCopy.forLanguage(language);
          await tester.pumpWidget(
            host(controller, [activity], language: language, scale: 2),
          );
          await tester.pumpAndSettle();
          expect(find.text(copy('exchange.role.user')), findsNothing);
          expect(
            find.byKey(Key('reader-copy-${activity.id}-input-0')),
            findsNothing,
          );
          final before = readerPosition(tester).pixels;
          final open = find.byKey(
            const ValueKey('content-page-open-unproved-input'),
          );
          expect(open, findsOneWidget);
          await tester.ensureVisible(open);
          await tester.tap(open);
          await tester.pumpAndSettle();
          expect(find.text(copy('exchange.role.user')), findsNothing);
          if (verifiedKind == 'image') {
            final thumbnail = find.byKey(
              ValueKey('image-thumbnail-sha256:${List.filled(64, 'a').join()}'),
            );
            await tester.ensureVisible(thumbnail);
            await tester.tap(thumbnail);
            await tester.pumpAndSettle();
            expect(find.byType(InteractiveViewer), findsOneWidget);
            await tester.tap(find.byKey(const Key('image-preview-close')));
            await tester.pumpAndSettle();
          } else {
            expect(
              find.text(
                verifiedKind == 'text'
                    ? 'VERIFIED HUMAN TEXT'
                    : 'MATCHED_TOOL_RESULT',
              ),
              findsOneWidget,
            );
          }
          final nested = find.byKey(
            const ValueKey('content-page-open-nested-input'),
          );
          await tester.ensureVisible(nested);
          await tester.pumpAndSettle();
          await tester.tap(nested);
          await tester.pumpAndSettle();
          expect(find.text('NESTED SEGMENT'), findsOneWidget);
          await tester.tap(find.byKey(const Key('content-page-back')));
          await tester.pumpAndSettle();
          expect(find.text('NESTED SEGMENT'), findsNothing);
          expect(
            find.byKey(const ValueKey('content-page-open-nested-input')),
            findsOneWidget,
          );
          await tester.tap(find.byKey(const Key('content-page-close')));
          await tester.pumpAndSettle();
          expect(readerPosition(tester).pixels, before);
          expect(find.text(copy('exchange.role.user')), findsNothing);
          expect(tester.takeException(), isNull);
          await tester.pumpWidget(const SizedBox.shrink());
        },
      );
    }
  }

  testWidgets('deferred wire-user input uses neutral content entry', (
    tester,
  ) async {
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    api.details[activity.id] = detail(
      base,
      [
        ExchangeContentMessage(
          role: 'user',
          blocks: [readerDeferred(activity.id, 'untyped')],
          agent: null,
        ),
      ],
      [block('text', 'DONE')],
      checkpoint: true,
      inputAnchor: List.filled(64, 'c').join(),
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity]));
    await tester.pumpAndSettle();
    expect(find.text('User'), findsNothing);
    expect(
      find.byKey(const ValueKey('content-page-open-untyped')),
      findsOneWidget,
    );
    expect(find.byKey(Key('reader-copy-${activity.id}-input')), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('human attachments keep observed text and image order at 390px', (
    tester,
  ) async {
    String? copiedText;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copiedText = (call.arguments as Map)['text'] as String;
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
    await tester.binding.setSurfaceSize(const Size(390, 1200));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    api.details[activity.id] = detail(
      base,
      [
        ExchangeContentMessage(
          role: 'user',
          blocks: [
            readerImage('a'),
            block('text', 'QUESTION BETWEEN IMAGES'),
            readerImage('b'),
          ],
          agent: null,
        ),
      ],
      [block('text', 'DONE')],
      checkpoint: true,
      inputAnchor: List.filled(64, 'c').join(),
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity], scale: 2));
    await tester.pumpAndSettle();
    final first = find.byKey(
      ValueKey('image-thumbnail-sha256:${List.filled(64, 'a').join()}'),
    );
    final second = find.byKey(
      ValueKey('image-thumbnail-sha256:${List.filled(64, 'b').join()}'),
    );
    final question = find.text('QUESTION BETWEEN IMAGES');
    expect(
      tester.getTopLeft(first).dy,
      lessThan(tester.getTopLeft(question).dy),
    );
    expect(
      tester.getTopLeft(question).dy,
      lessThan(tester.getTopLeft(second).dy),
    );
    expect(find.text('User'), findsOneWidget);
    expect(find.byType(Image), findsNWidgets(2));
    final before = readerPosition(tester).pixels;
    await tester.ensureVisible(second);
    await tester.tap(second);
    await tester.pumpAndSettle();
    expect(find.byType(InteractiveViewer), findsOneWidget);
    await tester.tap(find.byKey(const Key('image-preview-close')));
    await tester.pumpAndSettle();
    expect(readerPosition(tester).pixels, before);
    final copy = find.byKey(Key('reader-copy-${activity.id}-input'));
    await tester.ensureVisible(copy);
    await tester.tap(copy);
    await tester.pumpAndSettle();
    expect(copiedText, 'QUESTION BETWEEN IMAGES');
    expect(question, findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final reading in [false, true]) {
    testWidgets('mixed deferred input keeps observed order reading=$reading', (
      tester,
    ) async {
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      api.details[activity.id] = detail(
        base,
        [
          ExchangeContentMessage(
            role: 'user',
            blocks: [
              readerDeferred(activity.id, 'before-human'),
              block('text', 'FIRST HUMAN TEXT'),
              readerDeferred(activity.id, 'between-human'),
              block('text', 'SECOND HUMAN TEXT'),
            ],
            agent: null,
          ),
        ],
        [block('text', 'DONE')],
        inputAnchor: reading ? List.filled(64, 'c').join() : null,
      );
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity]));
      await tester.pumpAndSettle();
      final before = find.byKey(
        const ValueKey('content-page-open-before-human'),
      );
      final between = find.byKey(
        const ValueKey('content-page-open-between-human'),
      );
      expect(
        tester.getTopLeft(before).dy,
        lessThan(tester.getTopLeft(find.text('FIRST HUMAN TEXT')).dy),
      );
      expect(
        tester.getTopLeft(find.text('FIRST HUMAN TEXT')).dy,
        lessThan(tester.getTopLeft(between).dy),
      );
      expect(
        tester.getTopLeft(between).dy,
        lessThan(tester.getTopLeft(find.text('SECOND HUMAN TEXT')).dy),
      );
      expect(find.text('FIRST HUMAN TEXT'), findsOneWidget);
      expect(find.text('SECOND HUMAN TEXT'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  testWidgets(
    'identical questions retain distinct anchors and replay only once',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1220, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ReaderApi();
      final activity = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      final repeated = readerActivity(
        activity,
        'repeated-question',
        seconds: 1,
      );
      final replay = readerActivity(activity, 'replayed-request', seconds: 2);
      for (final (row, anchor) in [
        (activity, 'a'),
        (repeated, 'b'),
        (replay, 'b'),
      ]) {
        api.details[row.id] = detail(
          base,
          [message('user', 'THE SAME QUESTION')],
          [block('text', 'REPLY ${row.id}')],
          activity: row,
          checkpoint: true,
          inputAnchor: List.filled(64, anchor).join(),
        );
      }
      final controller = makeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(host(controller, [activity, repeated, replay]));
      await tester.pumpAndSettle();
      expect(find.text('THE SAME QUESTION'), findsNWidgets(2));
      expect(find.text('User'), findsNWidgets(2));
      expect(find.byKey(Key('reader-copy-${replay.id}-input')), findsNothing);
      expect(find.text('REPLY ${replay.id}'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('a tool result is resolved beyond the visible history cache', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    api.details[activity.id] = detail(
      base,
      [message('user', 'QUESTION')],
      [block('text', 'REPLY'), tool('tool_call', 'outside-page')],
    );
    api.results['outside-page'] = ToolResultPage(
      items: [
        ToolResultMatch(
          exchangeId: 'later-request',
          block: tool('tool_result', 'outside-page'),
        ),
      ],
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity]));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
    await tester.pumpAndSettle();
    expect(find.text('MATCHED_TOOL_RESULT'), findsOneWidget);
    expect(api.toolCalls, ['outside-page']);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });
  testWidgets('late tool result is visible after reopening the inspector', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    api.details[activity.id] = detail(
      base,
      [message('user', 'QUESTION')],
      [block('text', 'REPLY'), tool('tool_call', 'late-return')],
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity]));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
    await tester.pumpAndSettle();
    expect(find.text('Result not found yet.'), findsOneWidget);
    api.results['late-return'] = ToolResultPage(
      items: [
        ToolResultMatch(
          exchangeId: 'later-request',
          block: tool('tool_result', 'late-return'),
        ),
      ],
    );
    await tester.tap(find.byKey(const Key('reader-close-details')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.pumpAndSettle();
    expect(find.text('MATCHED_TOOL_RESULT'), findsOneWidget);
    expect(find.text('Result not found yet.'), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'open tool results refresh on relevant activity without moving the reader',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final rows = ValueNotifier([rig.activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(rig.controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      final y = tester.getTopLeft(find.text('READER_ANCHOR')).dy;
      expect(find.text('Result not found yet.'), findsOneWidget);
      rig.api.results['call'] = resultPage('call', 'NEW_ACTIVITY_RETURN');
      final next = readerActivity(rig.activity, 'next', seconds: 1);
      rig.api.details[next.id] = detail(
        rig.base,
        [message('user', 'NEXT_INPUT')],
        [block('text', 'NEXT_REPLY')],
        activity: next,
      );
      rows.value = [rig.activity, next];
      await tester.pumpAndSettle();
      expect(find.text('NEW_ACTIVITY_RETURN'), findsOneWidget);
      expect(tester.getTopLeft(find.text('READER_ANCHOR')).dy, closeTo(y, 1));
      expect(find.textContaining('echo safe'), findsNothing);
      // A polling rebuild of the same evidence must keep the completed Future.
      final calls = rig.api.toolCalls.length;
      rows.value = [readerActivity(rig.activity, rig.activity.id), next];
      await tester.pumpAndSettle();
      expect(find.text('NEW_ACTIVITY_RETURN'), findsOneWidget);
      expect(rig.api.toolCalls.length, calls);
      rig.api.results['call'] = resultPage('call', 'UPDATED_ACTIVITY_RETURN');
      rows.value = [
        rig.activity,
        readerActivity(
          next,
          next.id,
          contentAvailable: next.contentAvailable != true,
        ),
      ];
      await tester.pumpAndSettle();
      expect(find.text('UPDATED_ACTIVITY_RETURN'), findsOneWidget);
      expect(find.text('NEW_ACTIVITY_RETURN'), findsNothing);
      expect(tester.getTopLeft(find.text('READER_ANCHOR')).dy, closeTo(y, 1));
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'interrupted paginated lookup cannot poison a reopened selection',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final pending = Completer<ToolResultPage>();
      var started = false;
      rig.api.lookup = (origin, call, cursor) {
        if (cursor == 'page-two') return pending.future;
        if (!started) {
          started = true;
          return Future.value(
            const ToolResultPage(items: [], nextCursor: 'page-two'),
          );
        }
        return Future.value(resultPage(call, 'REOPENED_RETURN'));
      };
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      await rig.openResults(tester, settle: false);
      expect(rig.api.toolRequests.map((r) => r.$3), [null, 'page-two']);
      await tester.tap(find.byKey(const Key('reader-close-details')));
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      expect(find.text('REOPENED_RETURN'), findsOneWidget);
      pending.complete(
        const ToolResultPage(items: [], nextCursor: 'page-three'),
      );
      await tester.pumpAndSettle();
      expect(find.text('REOPENED_RETURN'), findsOneWidget);
      expect(rig.api.toolRequests.any((r) => r.$3 == 'page-three'), isFalse);
      expect(find.text('Result not found yet.'), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('older tool replies cannot replace or cache a new selection', (
    tester,
  ) async {
    final rig = await _ToolReaderFixture.create(tester);
    final next = readerActivity(rig.activity, 'other-request', seconds: 1);
    rig.api.details[next.id] = detail(
      rig.base,
      [message('user', 'OTHER_INPUT')],
      [block('text', 'OTHER_REPLY'), tool('tool_call', 'call')],
      activity: next,
    );
    final pending = Completer<ToolResultPage>();
    var first = true;
    rig.api.lookup = (origin, call, cursor) {
      if (origin == rig.activity.id && first) {
        first = false;
        return pending.future;
      }
      return Future.value(
        resultPage(
          call,
          origin == rig.activity.id ? 'FRESH_ORIGIN_RETURN' : 'SELECTED_RETURN',
        ),
      );
    };
    await tester.pumpWidget(host(rig.controller, [rig.activity, next]));
    await tester.pumpAndSettle();
    await rig.openResults(tester, settle: false);
    await tester.tap(find.byKey(Key('reader-tools-${next.id}')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
    await tester.pumpAndSettle();
    expect(find.text('SELECTED_RETURN'), findsOneWidget);
    pending.complete(resultPage('call', 'STALE_RETURN'));
    await tester.pumpAndSettle();
    expect(find.text('SELECTED_RETURN'), findsOneWidget);
    expect(find.text('STALE_RETURN'), findsNothing);
    await rig.openResults(tester);
    expect(find.text('FRESH_ORIGIN_RETURN'), findsOneWidget);
    expect(find.text('STALE_RETURN'), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'lookup failure and repeated pagination cursor remain retryable',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      rig.api.lookup = (_, _, _) async =>
          throw const ControlContractException('unavailable');
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      expect(
        find.text('Could not load the result. Please retry.'),
        findsOneWidget,
      );
      rig.api.lookup = (_, _, _) async =>
          const ToolResultPage(items: [], nextCursor: 'repeat');
      await tester.tap(
        find.byKey(Key('reader-tool-refresh-${rig.activity.id}-call')),
      );
      await tester.pumpAndSettle();
      expect(
        find.text('Could not load the result. Please retry.'),
        findsOneWidget,
      );
      expect(rig.api.toolRequests.map((r) => r.$3), [null, null, 'repeat']);
      rig.api.lookup = (_, call, cursor) async => cursor == null
          ? const ToolResultPage(items: [], nextCursor: 'last-page')
          : resultPage(call, 'RETRIED_PAGINATED_RETURN');
      await tester.tap(
        find.byKey(Key('reader-tool-refresh-${rig.activity.id}-call')),
      );
      await tester.pumpAndSettle();
      expect(find.text('RETRIED_PAGINATED_RETURN'), findsOneWidget);
      expect(
        find.text('Could not load the result. Please retry.'),
        findsNothing,
      );
      final calls = rig.api.toolCalls.length;
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      expect(find.text('RETRIED_PAGINATED_RETURN'), findsOneWidget);
      expect(rig.api.toolCalls.length, calls);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('end of empty pagination is stable until explicit refresh', (
    tester,
  ) async {
    final rig = await _ToolReaderFixture.create(tester);
    rig.api.lookup = (_, _, cursor) async => ToolResultPage(
      items: [],
      nextCursor: cursor == null ? 'end-page' : null,
    );
    await tester.pumpWidget(host(rig.controller, [rig.activity]));
    await tester.pumpAndSettle();
    await rig.openResults(tester);
    expect(find.text('Result not found yet.'), findsOneWidget);
    expect(rig.api.toolRequests.map((r) => r.$3), [null, 'end-page']);
    await tester.pumpWidget(host(rig.controller, [rig.activity]));
    await tester.pumpAndSettle();
    expect(rig.api.toolRequests, hasLength(2));
    rig.api.lookup = (_, call, _) async => resultPage(call, 'REFRESHED_RETURN');
    await tester.tap(
      find.byKey(Key('reader-tool-refresh-${rig.activity.id}-call')),
    );
    await tester.pumpAndSettle();
    expect(find.text('REFRESHED_RETURN'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'deleting a selected record discards pending lookup and restored result',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final pending = Completer<ToolResultPage>();
      rig.api.lookup = (_, _, _) => pending.future;
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      await rig.openResults(tester, settle: false);
      await tester.pumpWidget(host(rig.controller, []));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('reader-close-details')), findsNothing);
      pending.complete(resultPage('call', 'DELETED_STALE_RETURN'));
      await tester.pumpAndSettle();
      rig.api.lookup = (_, call, _) async =>
          resultPage(call, 'RESTORED_RETURN');
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      expect(find.text('RESTORED_RETURN'), findsOneWidget);
      expect(find.text('DELETED_STALE_RETURN'), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  for (final controllerSwitch in [false, true]) {
    testWidgets(
      'switching ${controllerSwitch ? 'controller' : 'conversation'} discards old lookup work',
      (tester) async {
        final rig = await _ToolReaderFixture.create(tester);
        final pending = Completer<ToolResultPage>();
        rig.api.lookup = (_, _, _) => pending.future;
        await tester.pumpWidget(host(rig.controller, [rig.activity]));
        await tester.pumpAndSettle();
        await rig.openResults(tester, settle: false);
        final api = controllerSwitch ? _ReaderApi() : rig.api;
        final controller = controllerSwitch
            ? makeController(api)
            : rig.controller;
        if (controllerSwitch) addTearDown(controller.dispose);
        final next = controllerSwitch
            ? rig.activity
            : readerActivity(
                rig.activity,
                'different-conversation',
                conversation: ActivityConversationRef(
                  id: 'conversation-other',
                  displayName: null,
                  kind: 'main',
                  evidence: 'capture_run',
                  actor: null,
                ),
              );
        api.details[next.id] = detail(
          rig.base,
          [message('user', 'SWITCHED_INPUT')],
          [block('text', 'SWITCHED_REPLY'), tool('tool_call', 'call')],
          activity: next,
        );
        api.lookup = (_, call, _) async => resultPage(call, 'SWITCHED_RETURN');
        await tester.pumpWidget(host(controller, [next]));
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('reader-close-details')), findsNothing);
        await tester.tap(find.byKey(Key('reader-tools-${next.id}')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
        await tester.pumpAndSettle();
        expect(find.text('SWITCHED_RETURN'), findsOneWidget);
        pending.complete(resultPage('call', 'OLD_CONTEXT_RETURN'));
        await tester.pumpAndSettle();
        expect(find.text('SWITCHED_RETURN'), findsOneWidget);
        expect(find.text('OLD_CONTEXT_RETURN'), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  testWidgets(
    'successful lookups evict the least recently viewed result after 32 entries',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final rows = List.generate(
        33,
        (i) => readerActivity(rig.activity, 'cache-row-$i', seconds: i),
      );
      for (final (i, row) in rows.indexed) {
        rig.api.details[row.id] = detail(
          rig.base,
          [message('user', 'INPUT_$i')],
          [block('text', 'REPLY_$i'), tool('tool_call', 'cache-call-$i')],
          activity: row,
        );
        rig.api.results['cache-call-$i'] = resultPage(
          'cache-call-$i',
          'CACHED_RETURN_$i',
        );
      }
      await tester.pumpWidget(host(rig.controller, rows));
      await tester.pumpAndSettle();
      final scrollable = find
          .descendant(
            of: find.byKey(const PageStorageKey('conversation-reader-scroll')),
            matching: find.byType(Scrollable),
          )
          .first;
      Future<void> select(int i) async {
        readerPosition(tester).jumpTo(0);
        await tester.pumpAndSettle();
        await tester.scrollUntilVisible(
          find.byKey(Key('reader-tools-${rows[i].id}')),
          400,
          scrollable: scrollable,
          maxScrolls: 100,
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(Key('reader-tools-${rows[i].id}')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
        await tester.pumpAndSettle();
      }

      for (var i = 32; i >= 0; i--) {
        await select(i);
        expect(find.text('CACHED_RETURN_$i'), findsOneWidget);
      }
      // Touch an old retained entry. Its changed remote body must stay cached.
      rig.api.results['cache-call-31'] = resultPage(
        'cache-call-31',
        'REMOTE_CHANGED_31',
      );
      await select(31);
      expect(find.text('CACHED_RETURN_31'), findsOneWidget);
      // Entry 32 was evicted by entry 0 and must query its new retained body.
      rig.api.results['cache-call-32'] = resultPage(
        'cache-call-32',
        'EVICTED_RETURN_32',
      );
      await select(32);
      expect(find.text('EVICTED_RETURN_32'), findsOneWidget);
      // Entry 31 was touched, so entry 30 is now the least recently used.
      rig.api.results['cache-call-30'] = resultPage(
        'cache-call-30',
        'EVICTED_RETURN_30',
      );
      await select(30);
      expect(find.text('EVICTED_RETURN_30'), findsOneWidget);
      await select(31);
      expect(find.text('CACHED_RETURN_31'), findsOneWidget);
      expect(find.text('REMOTE_CHANGED_31'), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'relevant refresh preserves expanded result and inspector scroll',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final source = List.generate(
        90,
        (i) => 'RETAINED_RESULT_LINE_$i',
      ).join('\n');
      rig.api.results['call'] = resultPage('call', source);
      final rows = ValueNotifier([rig.activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(rig.controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      final toggle = find.byKey(
        const Key('toggle-long-looked-up-later-request-call-0'),
      );
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(find.text('Show first 15 lines'), findsOneWidget);
      ScrollPosition panel() => tester
          .state<ScrollableState>(
            find
                .descendant(
                  of: find.byKey(
                    PageStorageKey(
                      'reader-panel-${rig.activity.id}:tools:false:1',
                    ),
                  ),
                  matching: find.byType(Scrollable),
                )
                .first,
          )
          .position;
      panel().jumpTo(400);
      await tester.pumpAndSettle();
      final offset = panel().pixels;
      final anchor = tester.getTopLeft(find.text('READER_ANCHOR')).dy;
      final pending = Completer<ToolResultPage>();
      rig.api.lookup = (_, _, _) => pending.future;
      rows.value = [
        readerActivity(
          rig.activity,
          rig.activity.id,
          contentAvailable: rig.activity.contentAvailable != true,
        ),
      ];
      await tester.pump();
      await tester.pump();
      pending.complete(resultPage('call', '$source\nLATEST_RESULT_LINE'));
      await tester.pumpAndSettle();
      expect(find.text('Show first 15 lines'), findsOneWidget);
      expect(find.textContaining('LATEST_RESULT_LINE'), findsOneWidget);
      expect(panel().pixels, closeTo(offset, 1));
      expect(
        tester.getTopLeft(find.text('READER_ANCHOR')).dy,
        closeTo(anchor, 1),
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  for (final staleFirst in [false, true]) {
    testWidgets(
      'overlapping relevant refresh preserves the original panel offset staleFirst=$staleFirst',
      (tester) async {
        final rig = await _ToolReaderFixture.create(tester);
        final source = List.generate(
          90,
          (i) => 'RETAINED_RESULT_LINE_$i',
        ).join('\n');
        rig.api.results['call'] = resultPage('call', source);
        final rows = ValueNotifier([rig.activity]);
        addTearDown(rows.dispose);
        await tester.pumpWidget(
          ValueListenableBuilder<List<ActivityRecord>>(
            valueListenable: rows,
            builder: (_, value, _) => host(rig.controller, value),
          ),
        );
        await tester.pumpAndSettle();
        await rig.openResults(tester);
        final toggle = find.byKey(
          const Key('toggle-long-looked-up-later-request-call-0'),
        );
        await tester.ensureVisible(toggle);
        await tester.tap(toggle);
        await tester.pumpAndSettle();
        ScrollPosition panel() =>
            readerPanelPosition(tester, rig.activity.id, 1);
        panel().jumpTo(400);
        await tester.pumpAndSettle();
        final anchor = tester.getTopLeft(find.text('READER_ANCHOR')).dy;
        final first = Completer<ToolResultPage>(),
            latest = Completer<ToolResultPage>();
        var query = 0;
        rig.api.lookup = (_, _, _) =>
            query++ == 0 ? first.future : latest.future;
        rows.value = [
          readerActivity(
            rig.activity,
            rig.activity.id,
            contentAvailable: rig.activity.contentAvailable != true,
          ),
        ];
        await tester.pump();
        await tester.pump();
        expect(
          panel().pixels,
          0,
          reason: 'the temporary loading layout is short',
        );
        rows.value = [rig.activity];
        await tester.pump();
        await tester.pump();
        if (staleFirst) {
          first.complete(resultPage('call', '$source\nSTALE_RESULT_LINE'));
          await tester.pump();
          await tester.pump();
          expect(find.textContaining('STALE_RESULT_LINE'), findsNothing);
        }
        latest.complete(resultPage('call', '$source\nLATEST_RESULT_LINE'));
        await tester.pumpAndSettle();
        expect(find.text('Show first 15 lines'), findsOneWidget);
        expect(find.textContaining('LATEST_RESULT_LINE'), findsOneWidget);
        expect(panel().pixels, closeTo(400, 1));
        expect(
          tester.getTopLeft(find.text('READER_ANCHOR')).dy,
          closeTo(anchor, 1),
        );
        if (!staleFirst) {
          first.complete(resultPage('call', '$source\nSTALE_RESULT_LINE'));
          await tester.pumpAndSettle();
          expect(find.textContaining('LATEST_RESULT_LINE'), findsOneWidget);
          expect(find.textContaining('STALE_RESULT_LINE'), findsNothing);
          expect(panel().pixels, closeTo(400, 1));
          expect(
            tester.getTopLeft(find.text('READER_ANCHOR')).dy,
            closeTo(anchor, 1),
          );
        }
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  testWidgets(
    'overlapping refresh preserves position while another tool result is still pending',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final source = List.generate(90, (i) => 'MULTI_TOOL_LINE_$i').join('\n');
      rig.api.details[rig.activity.id] = detail(
        rig.base,
        [message('user', 'QUESTION')],
        [
          block('text', 'READER_ANCHOR'),
          tool('tool_call', 'short-call'),
          tool('tool_call', 'call'),
        ],
      );
      rig.api.results['short-call'] = resultPage('short-call', 'SHORT_RETURN');
      rig.api.results['call'] = resultPage('call', source);
      final rows = ValueNotifier([rig.activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(rig.controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      final toggle = find.byKey(
        const Key('toggle-long-looked-up-later-request-call-0'),
      );
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      readerPanelPosition(tester, rig.activity.id, 1).jumpTo(400);
      await tester.pumpAndSettle();
      final anchor = tester.getTopLeft(find.text('READER_ANCHOR')).dy;
      final first = Completer<ToolResultPage>(),
          latest = Completer<ToolResultPage>();
      var query = 0;
      rig.api.lookup = (_, call, _) => call == 'short-call'
          ? Future.value(resultPage(call, 'REFRESHED_SHORT_RETURN'))
          : query++ == 0
          ? first.future
          : latest.future;
      rows.value = [
        readerActivity(
          rig.activity,
          rig.activity.id,
          contentAvailable: rig.activity.contentAvailable != true,
        ),
      ];
      await tester.pump();
      await tester.pump();
      await tester.pump();
      expect(find.text('REFRESHED_SHORT_RETURN'), findsOneWidget);
      expect(readerPanelPosition(tester, rig.activity.id, 1).pixels, 0);
      rows.value = [rig.activity];
      await tester.pump();
      await tester.pump();
      await tester.pump();
      latest.complete(resultPage('call', '$source\nLATEST_MULTI_TOOL_LINE'));
      await tester.pumpAndSettle();
      expect(find.textContaining('LATEST_MULTI_TOOL_LINE'), findsOneWidget);
      expect(find.text('Show first 15 lines'), findsOneWidget);
      expect(
        readerPanelPosition(tester, rig.activity.id, 1).pixels,
        closeTo(400, 1),
      );
      expect(
        tester.getTopLeft(find.text('READER_ANCHOR')).dy,
        closeTo(anchor, 1),
      );
      first.complete(resultPage('call', '$source\nSTALE_MULTI_TOOL_LINE'));
      await tester.pumpAndSettle();
      expect(find.textContaining('STALE_MULTI_TOOL_LINE'), findsNothing);
      expect(
        readerPanelPosition(tester, rig.activity.id, 1).pixels,
        closeTo(400, 1),
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  for (final navigation in ['tab', 'close', 'selection']) {
    testWidgets(
      'user $navigation navigation cancels the pending panel restore',
      (tester) async {
        final rig = await _ToolReaderFixture.create(tester);
        final source = List.generate(
          90,
          (i) => 'NAVIGATION_RESULT_LINE_$i',
        ).join('\n');
        rig.api.results['call'] = resultPage('call', source);
        final next = readerActivity(rig.activity, 'next-selection', seconds: 1);
        rig.api.details[next.id] = detail(
          rig.base,
          [message('user', 'NEXT_INPUT')],
          [block('text', 'NEXT_REPLY'), tool('tool_call', 'next-call')],
          activity: next,
        );
        rig.api.results['next-call'] = resultPage(
          'next-call',
          'NEW_SELECTION_RETURN',
        );
        final rows = ValueNotifier([rig.activity, next]);
        addTearDown(rows.dispose);
        await tester.pumpWidget(
          ValueListenableBuilder<List<ActivityRecord>>(
            valueListenable: rows,
            builder: (_, value, _) => host(rig.controller, value),
          ),
        );
        await tester.pumpAndSettle();
        await rig.openResults(tester);
        await expandReaderResult(tester, rig.activity.id);
        final pending = Completer<ToolResultPage>();
        rig.api.lookup = (_, call, _) => call == 'call'
            ? pending.future
            : Future.value(rig.api.results[call]!);
        rows.value = [
          readerActivity(
            rig.activity,
            rig.activity.id,
            contentAvailable: rig.activity.contentAvailable != true,
          ),
          next,
        ];
        await tester.pump();
        await tester.pump();
        if (navigation == 'tab') {
          await tester.tap(find.byKey(const Key('reader-detail-tab-0')));
        } else if (navigation == 'close') {
          await tester.tap(find.byKey(const Key('reader-close-details')));
        } else {
          await tester.tap(find.byKey(Key('reader-tools-${next.id}')));
        }
        await tester.pumpAndSettle();
        if (navigation == 'selection') {
          await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
          await tester.pumpAndSettle();
          expect(find.text('NEW_SELECTION_RETURN'), findsOneWidget);
        }
        pending.complete(resultPage('call', '$source\nLATE_NAVIGATED_RETURN'));
        await tester.pumpAndSettle();
        expect(find.textContaining('LATE_NAVIGATED_RETURN'), findsNothing);
        if (navigation == 'tab') {
          expect(find.textContaining('echo safe'), findsWidgets);
          expect(readerPanelPosition(tester, rig.activity.id, 0).pixels, 0);
        } else if (navigation == 'close') {
          expect(find.byKey(const Key('reader-close-details')), findsNothing);
        } else {
          expect(find.text('NEW_SELECTION_RETURN'), findsOneWidget);
          expect(readerPanelPosition(tester, next.id, 1).pixels, 0);
        }
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  testWidgets(
    'manual refresh retains the saved offset after an empty intermediate result',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final source = List.generate(
        90,
        (i) => 'MANUAL_RESULT_LINE_$i',
      ).join('\n');
      rig.api.results['call'] = resultPage('call', source);
      final rows = ValueNotifier([rig.activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(rig.controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      await expandReaderResult(tester, rig.activity.id);
      rig.api.results['call'] = const ToolResultPage(items: []);
      rows.value = [
        readerActivity(
          rig.activity,
          rig.activity.id,
          contentAvailable: rig.activity.contentAvailable != true,
        ),
      ];
      await tester.pumpAndSettle();
      expect(find.text('Result not found yet.'), findsOneWidget);
      expect(readerPanelPosition(tester, rig.activity.id, 1).pixels, 0);
      rig.api.results['call'] = resultPage(
        'call',
        '$source\nMANUALLY_REFRESHED_LINE',
      );
      await tester.tap(
        find.byKey(Key('reader-tool-refresh-${rig.activity.id}-call')),
      );
      await tester.pumpAndSettle();
      expect(find.textContaining('MANUALLY_REFRESHED_LINE'), findsOneWidget);
      expect(find.text('Show first 15 lines'), findsOneWidget);
      expect(
        readerPanelPosition(tester, rig.activity.id, 1).pixels,
        closeTo(400, 1),
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'a user scroll supersedes restoration from another pending tool result',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final source = List.generate(
        90,
        (i) => 'SCROLLED_RESULT_LINE_$i',
      ).join('\n');
      rig.api.details[rig.activity.id] = detail(
        rig.base,
        [message('user', 'QUESTION')],
        [
          block('text', 'READER_ANCHOR'),
          tool('tool_call', 'call'),
          tool('tool_call', 'pending-call'),
        ],
      );
      rig.api.results['call'] = resultPage('call', source);
      rig.api.results['pending-call'] = resultPage(
        'pending-call',
        'INITIAL_SECOND_RETURN',
      );
      final rows = ValueNotifier([rig.activity]);
      addTearDown(rows.dispose);
      await tester.pumpWidget(
        ValueListenableBuilder<List<ActivityRecord>>(
          valueListenable: rows,
          builder: (_, value, _) => host(rig.controller, value),
        ),
      );
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      await expandReaderResult(tester, rig.activity.id);
      final pending = Completer<ToolResultPage>();
      rig.api.lookup = (_, call, _) => call == 'pending-call'
          ? pending.future
          : Future.value(resultPage(call, source));
      rows.value = [
        readerActivity(
          rig.activity,
          rig.activity.id,
          contentAvailable: rig.activity.contentAvailable != true,
        ),
      ];
      await tester.pump();
      await tester.pump();
      await tester.pump();
      await tester.pump();
      expect(
        readerPanelPosition(tester, rig.activity.id, 1).pixels,
        closeTo(400, 1),
      );
      final panel = find.byKey(
        PageStorageKey('reader-panel-${rig.activity.id}:tools:false:1'),
      );
      await tester.drag(panel, const Offset(0, 150));
      await tester.pump(const Duration(seconds: 1));
      final chosenOffset = readerPanelPosition(
        tester,
        rig.activity.id,
        1,
      ).pixels;
      expect(chosenOffset, greaterThan(0));
      expect(chosenOffset, lessThan(390));
      pending.complete(resultPage('pending-call', 'LATEST_SECOND_RETURN'));
      await tester.pumpAndSettle();
      // Scroll to the second result only after proving its completion kept the
      // position the user chose rather than jumping back to the saved 400.
      expect(
        readerPanelPosition(tester, rig.activity.id, 1).pixels,
        closeTo(chosenOffset, 1),
      );
      await tester.scrollUntilVisible(
        find.text('LATEST_SECOND_RETURN'),
        300,
        scrollable: find
            .descendant(of: panel, matching: find.byType(Scrollable))
            .first,
      );
      expect(find.text('LATEST_SECOND_RETURN'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'closing drops a completed result and deleting its source refreshes the open origin',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final later = readerActivity(rig.activity, 'return-source', seconds: 1);
      rig.api.details[later.id] = detail(
        rig.base,
        [message('user', 'LATER_INPUT')],
        [block('text', 'LATER_REPLY')],
        activity: later,
      );
      rig.api.results['call'] = resultPage(
        'call',
        'FIRST_RETURN',
        exchangeId: later.id,
      );
      await tester.pumpWidget(host(rig.controller, [rig.activity, later]));
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      expect(find.text('FIRST_RETURN'), findsOneWidget);
      await tester.tap(find.byKey(const Key('reader-close-details')));
      await tester.pumpAndSettle();
      rig.api.results['call'] = resultPage(
        'call',
        'REOPENED_POSITIVE_RETURN',
        exchangeId: later.id,
      );
      await rig.openResults(tester);
      expect(find.text('REOPENED_POSITIVE_RETURN'), findsOneWidget);
      rig.api.results['call'] = resultPage('call', 'AFTER_SOURCE_DELETION');
      await tester.pumpWidget(host(rig.controller, [rig.activity]));
      await tester.pumpAndSettle();
      expect(find.text('AFTER_SOURCE_DELETION'), findsOneWidget);
      expect(find.text('REOPENED_POSITIVE_RETURN'), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'lookup cache budget counts retained arguments and text rather than original size',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final rows = List.generate(
        4,
        (i) => readerActivity(rig.activity, 'byte-row-$i', seconds: i),
      );
      for (final (i, row) in rows.indexed) {
        rig.api.details[row.id] = detail(
          rig.base,
          [message('user', 'INPUT_$i')],
          [block('text', 'REPLY_$i'), tool('tool_call', 'byte-call-$i')],
          activity: row,
        );
      }
      rig.api.results['byte-call-0'] = resultPage(
        'byte-call-0',
        'ARGUMENT_BUDGET_RETURN',
        originalSize: 1,
        arguments: {'blob': List.filled(2300000, 'x').join()},
      );
      rig.api.results['byte-call-1'] = resultPage(
        'byte-call-1',
        'TEXT_BUDGET_RETURN\n${List.filled(150000, 'retained line.').join('\n')}',
        originalSize: 1,
      );
      // A deferred/omitted source can be large while the retained body is tiny.
      rig.api.results['byte-call-2'] = resultPage(
        'byte-call-2',
        'SMALL_RETAINED_RETURN',
        originalSize: 32 * 1024 * 1024,
      );
      rig.api.results['byte-call-3'] = resultPage(
        'byte-call-3',
        'OVERSIZED_RETURN',
        originalSize: 1,
        arguments: {'blob': List.filled(4200000, '界').join()},
      );
      await tester.pumpWidget(host(rig.controller, rows));
      await tester.pumpAndSettle();
      Future<void> select(int i) async {
        await tester.ensureVisible(
          find.byKey(Key('reader-tools-${rows[i].id}')),
        );
        await tester.tap(find.byKey(Key('reader-tools-${rows[i].id}')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
        await tester.pumpAndSettle();
      }

      await select(0);
      expect(find.text('ARGUMENT_BUDGET_RETURN'), findsOneWidget);
      await select(1);
      expect(find.textContaining('TEXT_BUDGET_RETURN'), findsOneWidget);
      await select(2);
      expect(find.text('SMALL_RETAINED_RETURN'), findsOneWidget);
      rig.api.results['byte-call-0'] = resultPage(
        'byte-call-0',
        'ARGUMENT_BYTES_EVICTED',
      );
      await select(0);
      expect(find.text('ARGUMENT_BYTES_EVICTED'), findsOneWidget);
      // The huge originalSize must not have evicted the retained text result.
      rig.api.results['byte-call-1'] = resultPage(
        'byte-call-1',
        'WRONG_ORIGINAL_SIZE_EVICTION',
      );
      await select(1);
      expect(find.textContaining('TEXT_BUDGET_RETURN'), findsOneWidget);
      expect(find.text('WRONG_ORIGINAL_SIZE_EVICTION'), findsNothing);
      await select(3);
      expect(find.text('OVERSIZED_RETURN'), findsOneWidget);
      await select(2);
      expect(find.text('SMALL_RETAINED_RETURN'), findsOneWidget);
      rig.api.results['byte-call-3'] = resultPage(
        'byte-call-3',
        'OVERSIZED_REQUERIED_RETURN',
      );
      await select(3);
      expect(find.text('OVERSIZED_REQUERIED_RETURN'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'relevant activity invalidates a cached result from a previous selection',
    (tester) async {
      final rig = await _ToolReaderFixture.create(tester);
      final next = readerActivity(rig.activity, 'second-origin', seconds: 1);
      rig.api.details[next.id] = detail(
        rig.base,
        [message('user', 'SECOND_INPUT')],
        [block('text', 'SECOND_REPLY'), tool('tool_call', 'second-call')],
        activity: next,
      );
      rig.api.results['call'] = resultPage('call', 'CACHED_FIRST_RETURN');
      rig.api.results['second-call'] = resultPage(
        'second-call',
        'SECOND_RETURN',
      );
      await tester.pumpWidget(host(rig.controller, [rig.activity, next]));
      await tester.pumpAndSettle();
      await rig.openResults(tester);
      expect(find.text('CACHED_FIRST_RETURN'), findsOneWidget);
      await tester.tap(find.byKey(Key('reader-tools-${next.id}')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
      await tester.pumpAndSettle();
      expect(find.text('SECOND_RETURN'), findsOneWidget);
      rig.api.results['call'] = resultPage(
        'call',
        'UPDATED_PREVIOUS_SELECTION_RETURN',
      );
      await tester.pumpWidget(
        host(rig.controller, [
          rig.activity,
          readerActivity(
            next,
            next.id,
            contentAvailable: next.contentAvailable != true,
          ),
        ]),
      );
      await tester.pumpAndSettle();
      expect(find.text('SECOND_RETURN'), findsOneWidget);
      await rig.openResults(tester);
      expect(find.text('UPDATED_PREVIOUS_SELECTION_RETURN'), findsOneWidget);
      expect(find.text('CACHED_FIRST_RETURN'), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('code-tool input is visible, not mistaken for empty arguments', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    const code = 'await tools.exec_command({cmd: "pwd"});';
    final call = ExchangeContentBlock.fromJson({
      'kind': 'tool_call',
      'availability': 'recorded',
      'originalSize': code.length,
      'text': code,
      'toolName': 'exec',
      'callId': 'code-call',
    }, 'fixture');
    api.details[activity.id] = detail(
      base,
      [message('user', 'QUESTION')],
      [block('text', 'REPLY'), call],
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(host(controller, [activity]));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.pumpAndSettle();
    expect(find.text(code), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final language in AppLanguage.values) {
    testWidgets(
      'large content is opt-in and pages do not accumulate at 390px in $language',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(390, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _ReaderApi();
        final activity = (await api.fixture.activities(
          captureRunId: 'run-1',
        )).items.firstWhere((a) => a.status == 'succeeded');
        final base = await api.exchange(activity.id);
        final deferred = ExchangeContentBlock.fromJson({
          'kind': 'deferred',
          'availability': 'recorded',
          'originalSize': 0,
          'deferred': {
            'exchangeId': activity.id,
            'cursor': 'first',
            'estimatedBytes': 3 * 1024 * 1024,
          },
        }, 'fixture');
        api.details[activity.id] = detail(
          base,
          [message('user', 'QUESTION')],
          [deferred],
        );
        api.pages['first'] = ExchangeContentPage(
          exchangeId: activity.id,
          kind: 'text',
          messages: const [],
          blocks: const [],
          text: 'PAGE_ONE',
          offset: 0,
          total: 16,
          nextCursor: 'second',
        );
        api.pages['second'] = ExchangeContentPage(
          exchangeId: activity.id,
          kind: 'text',
          messages: const [],
          blocks: const [],
          text: 'PAGE_TWO',
          offset: 8,
          total: 16,
        );
        final controller = makeController(api);
        addTearDown(controller.dispose);
        await tester.pumpWidget(
          host(controller, [activity], language: language, scale: 2),
        );
        await tester.pumpAndSettle();
        expect(api.pageCalls, isEmpty);
        expect(
          find.byKey(Key('reader-copy-${activity.id}-response')),
          findsNothing,
        );
        final before = readerPosition(tester).pixels;
        final open = find.byKey(const ValueKey('content-page-open-first'));
        await tester.ensureVisible(open);
        await tester.tap(open);
        await tester.pumpAndSettle();
        expect(api.pageCalls, ['first']);
        expect(find.text('PAGE_ONE'), findsOneWidget);
        expect(find.text('PAGE_TWO'), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.tap(find.byKey(const Key('content-page-next')));
        await tester.pumpAndSettle();
        expect(api.pageCalls, ['first', 'second']);
        expect(find.text('PAGE_ONE'), findsNothing);
        expect(find.text('PAGE_TWO'), findsOneWidget);
        await tester.tap(find.byKey(const Key('content-page-back')));
        await tester.pumpAndSettle();
        expect(api.pageCalls, ['first', 'second', 'first']);
        expect(find.text('PAGE_ONE'), findsOneWidget);
        await tester.tap(find.byKey(const Key('content-page-close')));
        await tester.pumpAndSettle();
        expect(find.text('PAGE_ONE'), findsNothing);
        expect(readerPosition(tester).pixels, before);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }
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
        expect(find.text('Result not found yet.'), findsOneWidget);
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
      await tester.tap(
        find.byKey(Key('reader-tool-record-${activity.id}-same-call')),
      );
      await tester.pumpAndSettle();
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
          await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
          await tester.pumpAndSettle();
          expect(
            find.text(
              language == AppLanguage.english ? 'Refresh result' : '刷新结果',
            ),
            findsOneWidget,
          );
          api.results['call-one'] = resultPage(
            'call-one',
            'NARROW_REFRESH_RETURN',
          );
          await tester.tap(
            find.byKey(Key('reader-tool-refresh-${activity.id}-call-one')),
          );
          await tester.pumpAndSettle();
          expect(find.text('NARROW_REFRESH_RETURN'), findsOneWidget);
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

ScrollPosition readerPanelPosition(WidgetTester tester, String id, int tab) =>
    tester
        .state<ScrollableState>(
          find
              .descendant(
                of: find.byKey(
                  PageStorageKey('reader-panel-$id:tools:false:$tab'),
                ),
                matching: find.byType(Scrollable),
              )
              .first,
        )
        .position;

Future<void> expandReaderResult(WidgetTester tester, String originId) async {
  final toggle = find.byKey(
    const Key('toggle-long-looked-up-later-request-call-0'),
  );
  await tester.ensureVisible(toggle);
  await tester.tap(toggle);
  await tester.pumpAndSettle();
  expect(find.text('Show first 15 lines'), findsOneWidget);
  readerPanelPosition(tester, originId, 1).jumpTo(400);
  await tester.pumpAndSettle();
}

ExchangeContentBlock block(String kind, String text) =>
    ExchangeContentBlock.fromJson({
      'kind': kind,
      'availability': 'recorded',
      'text': text,
      'originalSize': text.length,
    }, 'fixture');

ExchangeContentBlock readerImage(String hex) => ExchangeContentBlock.fromJson({
  'kind': 'provider_extension',
  'availability': 'omitted',
  'originalSize': 100,
  'providerSource': 'openai_responses',
  'providerKind': 'input_image',
  'fingerprint': 'sha256:${List.filled(64, hex).join()}',
}, 'fixture');

ExchangeContentBlock readerDeferred(String id, String cursor) =>
    ExchangeContentBlock.fromJson({
      'kind': 'deferred',
      'availability': 'recorded',
      'originalSize': 0,
      'deferred': {
        'exchangeId': id,
        'cursor': cursor,
        'estimatedBytes': 3 * 1024 * 1024,
      },
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
  bool reading = false,
  ActivityRecord? activity,
  String? inputAnchor,
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
      page: inputAnchor == null && !reading
          ? null
          : ExchangeContentPaging(requestOffset: 0, inputAnchor: inputAnchor),
      state: 'recorded',
      mode: 'full',
      recordedAt: base.content.recordedAt,
      expiresAt: base.content.expiresAt,
      agentConversation: base.content.agentConversation,
      requestProjection: ExchangeRequestProjection(
        view: inputAnchor == null && !reading ? 'incremental' : 'reading',
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

ToolResultPage resultPage(
  String call,
  String text, {
  String exchangeId = 'later-request',
  int originalSize = 32,
  JsonObject? arguments,
}) => ToolResultPage(
  items: [
    ToolResultMatch(
      exchangeId: exchangeId,
      block: ExchangeContentBlock.fromJson({
        'kind': 'tool_result',
        'availability': 'recorded',
        'originalSize': originalSize,
        'callId': call,
        'text': text,
        'arguments': ?arguments,
      }, 'fixture'),
    ),
  ],
);

ActivityRecord readerActivity(
  ActivityRecord seed,
  String id, {
  int seconds = 0,
  bool? contentAvailable,
  ActivityConversationRef? conversation,
}) => ActivityRecord(
  id: id,
  occurredAt: seed.occurredAt.add(Duration(seconds: seconds)),
  title: seed.title,
  status: seed.status,
  reasonCode: seed.reasonCode,
  source: seed.source,
  conversation: conversation ?? seed.conversation,
  environment: seed.environment,
  contentAvailable: contentAvailable ?? seed.contentAvailable,
  parentRefs: ActivityParentRefs(
    exchangeId: id,
    captureRunId: seed.captureRunId,
    manualCaptureId: seed.manualCaptureId,
    connectionId: seed.parentRefs.connectionId,
  ),
);

final class _ToolReaderFixture {
  _ToolReaderFixture(this.api, this.activity, this.base, this.controller);
  final _ReaderApi api;
  final ActivityRecord activity;
  final ExchangeDetail base;
  final WorkbenchController controller;
  static Future<_ToolReaderFixture> create(WidgetTester tester) async {
    await tester.binding.setSurfaceSize(const Size(1220, 950));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = _ReaderApi();
    final activity = (await api.fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final base = await api.exchange(activity.id);
    api.details[activity.id] = detail(
      base,
      [message('user', 'QUESTION')],
      [block('text', 'READER_ANCHOR'), tool('tool_call', 'call')],
    );
    final controller = makeController(api);
    addTearDown(controller.dispose);
    return _ToolReaderFixture(api, activity, base, controller);
  }

  Future<void> openResults(WidgetTester tester, {bool settle = true}) async {
    await tester.ensureVisible(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
    if (settle) {
      await tester.pumpAndSettle();
    } else {
      await tester.pump();
      await tester.pump();
    }
  }
}

final class _ReaderApi implements ControlApi {
  final fixture = PreviewControlApi();
  final details = <String, ExchangeDetail>{};
  final delayedFull = <String, Completer<ExchangeDetail>>{};
  final usageQueries = <RuntimeUsageQuery>[];
  final exchangeCalls = <String>[];
  final pages = <String, ExchangeContentPage>{};
  final pageCalls = <String>[];
  final imageCalls = <String>[];
  final toolCalls = <String>[];
  final results = <String, ToolResultPage>{};
  final toolRequests = <(String, String, String?)>[];
  Future<ToolResultPage> Function(String, String, String?)? lookup;
  @override
  Future<ToolResultPage> toolResults(
    String id,
    String callId, {
    String? cursor,
  }) async {
    toolCalls.add(callId);
    toolRequests.add((id, callId, cursor));
    if (lookup != null) return lookup!(id, callId, cursor);
    return results[callId] ?? const ToolResultPage(items: []);
  }

  @override
  Future<RawEvidencePage> rawEvidence(String id) => fixture.rawEvidence(id);
  @override
  Future<RetainedImagePreview> previewRawImage({
    required String envelopeId,
    required String fingerprint,
  }) async {
    imageCalls.add(fingerprint);
    return RetainedImagePreview(
      fingerprint: fingerprint,
      mimeType: 'image/png',
      width: 1,
      height: 1,
      bytes: base64Decode(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jPioAAAAASUVORK5CYII=',
      ),
    );
  }

  @override
  Future<ExchangeContentPage> exchangeContentPage(
    String id,
    String cursor,
  ) async {
    pageCalls.add(cursor);
    final page = pages[cursor];
    if (page == null || page.exchangeId != id) {
      throw const ControlContractException('fixture page unavailable');
    }
    return page;
  }

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

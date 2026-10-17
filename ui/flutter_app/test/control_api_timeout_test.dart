@TestOn('vm')
library;

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/conversation_timeline.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

DesktopSession _session([int port = 9666]) => DesktopSession(
  baseUrl: Uri.parse('http://127.0.0.1:$port'),
  readToken: 'R' * 43,
  writeToken: 'W' * 43,
  instanceId: 'timeout-test',
  expiresAt: DateTime.now().toUtc().add(const Duration(days: 10)),
);

List<int> get _users =>
    utf8.encode('{"schema":"vibermate-runtime-user-list-v1","items":[]}');

final class _Call {
  _Call(this.request) {
    if (request case http.Abortable(:final abortTrigger?)) {
      unawaited(abortTrigger.then((_) => aborted = true));
    }
  }
  final http.BaseRequest request;
  final reply = Completer<http.StreamedResponse>();
  bool aborted = false;
  void succeed() =>
      reply.complete(http.StreamedResponse(Stream.value(_users), 200));
}

final class _Client extends http.BaseClient {
  final calls = <_Call>[];
  bool closed = false;
  @override
  Future<http.StreamedResponse> send(http.BaseRequest request) {
    final call = _Call(request);
    calls.add(call);
    return call.reply.future;
  }

  @override
  void close() => closed = true;
}

final class _Outcome<T> {
  _Outcome(Future<T> future) {
    unawaited(
      future.then(
        (result) {
          value = result;
          done = true;
        },
        onError: (Object failure) {
          error = failure;
          done = true;
        },
      ),
    );
  }
  bool done = false;
  T? value;
  Object? error;
}

void _expectTimeout(_Outcome<Object?> outcome) {
  expect(outcome.error, isA<ControlRequestTimeout>());
  final error = outcome.error as TimeoutException;
  expect(error.duration, const Duration(seconds: 30));
  expect(error.toString(), isNot(contains('127.0.0.1')));
  expect(error.toString(), isNot(contains('R' * 43)));
}

void main() {
  testWidgets('a response between 10 and 30 seconds succeeds', (tester) async {
    final client = _Client();
    final api = await HttpControlApi.connect(
      _session(),
      client: client,
      inspectSession: false,
    );
    addTearDown(api.close);
    final outcome = _Outcome(api.runtimeUsers());
    await tester.pump();
    await tester.pump(const Duration(seconds: 12));
    expect(outcome.done, isFalse);
    client.calls.single.succeed();
    await tester.pump();
    expect(outcome.error, isNull);
    expect(outcome.value, isEmpty);
    expect(client.calls.single.aborted, isFalse);
  });

  testWidgets(
    'stalled headers abort at 30 seconds and late headers are discarded',
    (tester) async {
      final client = _Client();
      final api = await HttpControlApi.connect(
        _session(),
        client: client,
        inspectSession: false,
      );
      addTearDown(api.close);
      final outcome = _Outcome(api.runtimeUsers());
      await tester.pump();
      await tester.pump(const Duration(seconds: 29));
      expect(outcome.done, isFalse);
      await tester.pump(const Duration(seconds: 1));
      _expectTimeout(outcome);
      expect(client.calls.single.aborted, isTrue);
      expect(client.closed, isFalse);
      var canceled = false;
      final late = StreamController<List<int>>(onCancel: () => canceled = true);
      client.calls.single.reply.complete(
        http.StreamedResponse(late.stream, 200),
      );
      await tester.pump();
      expect(canceled, isTrue);
      expect(outcome.value, isNull);
      unawaited(late.close());
      await tester.pump();
    },
  );

  testWidgets('body trickle shares the header deadline and is canceled', (
    tester,
  ) async {
    final client = _Client();
    final api = await HttpControlApi.connect(
      _session(),
      client: client,
      inspectSession: false,
    );
    addTearDown(api.close);
    final outcome = _Outcome(api.runtimeUsers());
    await tester.pump();
    await tester.pump(const Duration(seconds: 8));
    var canceled = false;
    final body = StreamController<List<int>>(onCancel: () => canceled = true);
    client.calls.single.reply.complete(http.StreamedResponse(body.stream, 200));
    await tester.pump();
    for (var i = 0; i < 2; i++) {
      body.add(utf8.encode(' '));
      await tester.pump(const Duration(seconds: 8));
    }
    body.add(utf8.encode(' '));
    await tester.pump(const Duration(seconds: 5));
    expect(outcome.done, isFalse);
    await tester.pump(const Duration(seconds: 1));
    _expectTimeout(outcome);
    expect(client.calls.single.aborted, isTrue);
    expect(canceled, isTrue);
    unawaited(body.close());
    await tester.pump();
  });

  testWidgets('one timeout leaves a concurrent request and the client usable', (
    tester,
  ) async {
    final client = _Client();
    final api = await HttpControlApi.connect(
      _session(),
      client: client,
      inspectSession: false,
    );
    addTearDown(api.close);
    final first = _Outcome(api.runtimeUsers());
    await tester.pump();
    await tester.pump(const Duration(seconds: 20));
    final second = _Outcome(api.runtimeUsers());
    await tester.pump();
    await tester.pump(const Duration(seconds: 10));
    _expectTimeout(first);
    expect(client.calls[0].aborted, isTrue);
    expect(client.calls[1].aborted, isFalse);
    expect(second.done, isFalse);
    client.calls[1].succeed();
    await tester.pump();
    expect(second.value, isEmpty);
    final third = _Outcome(api.runtimeUsers());
    await tester.pump();
    client.calls[2].succeed();
    await tester.pump();
    expect(third.value, isEmpty);
    expect(client.closed, isFalse);
  });

  testWidgets('closing cancels owned header and body requests immediately', (
    tester,
  ) async {
    final client = _Client();
    final api = await HttpControlApi.connect(
      _session(),
      client: client,
      inspectSession: false,
    );
    final first = _Outcome(api.runtimeUsers());
    final second = _Outcome(api.runtimeUsers());
    await tester.pump();
    var canceled = false;
    final body = StreamController<List<int>>(onCancel: () => canceled = true);
    client.calls[1].reply.complete(http.StreamedResponse(body.stream, 200));
    await tester.pump();
    await api.close();
    await tester.pump();
    expect(first.error, isA<StateError>());
    expect(second.error, isA<StateError>());
    expect(client.calls.every((call) => call.aborted), isTrue);
    expect(canceled, isTrue);
    expect(client.closed, isTrue);
    unawaited(body.close());
    await tester.pump();
  });

  testWidgets(
    'a stalled body aborts at the total deadline without replaying a mutation',
    (tester) async {
      final client = _Client();
      final api = await HttpControlApi.connect(
        _session(),
        client: client,
        inspectSession: false,
      );
      addTearDown(api.close);
      final outcome = _Outcome(api.cleanupExpiredEvidence());
      await tester.pump();
      var canceled = false;
      final body = StreamController<List<int>>(onCancel: () => canceled = true);
      client.calls.single.reply.complete(
        http.StreamedResponse(body.stream, 200),
      );
      await tester.pump();
      await tester.pump(const Duration(seconds: 29));
      expect(outcome.done, isFalse);
      await tester.pump(const Duration(seconds: 1));
      _expectTimeout(outcome);
      expect(client.calls.single.request.method, 'POST');
      expect(client.calls.single.aborted, isTrue);
      expect(canceled, isTrue);
      await tester.pump(const Duration(seconds: 30));
      expect(client.calls, hasLength(1));
      unawaited(body.close());
      await tester.pump();
    },
  );

  for (final declared in [true, false]) {
    testWidgets(
      'oversized response is rejected and canceled (declared=$declared)',
      (tester) async {
        final client = _Client();
        final api = await HttpControlApi.connect(
          _session(),
          client: client,
          inspectSession: false,
        );
        addTearDown(api.close);
        final outcome = _Outcome(api.runtimeUsers());
        await tester.pump();
        var canceled = false;
        final body = StreamController<List<int>>(
          onCancel: () => canceled = true,
        );
        client.calls.single.reply.complete(
          http.StreamedResponse(
            body.stream,
            200,
            contentLength: declared ? 2 * 1024 * 1024 + 1 : null,
          ),
        );
        await tester.pump();
        if (!declared) body.add(List.filled(2 * 1024 * 1024 + 1, 32));
        await tester.pump();
        expect(outcome.error, isA<ControlContractException>());
        expect(canceled, isTrue);
        expect(client.calls.single.aborted, isTrue);
        unawaited(body.close());
        await tester.pump();
      },
    );
  }

  for (final language in AppLanguage.values) {
    testWidgets(
      'Raw HTTP and generic timeout recovery are readable in ${language.name}',
      (tester) async {
        final api = _TimeoutPresentationApi();
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.preview.close,
          initialPreferences: WorkbenchPreferences(language: language),
        );
        addTearDown(controller.dispose);
        final activity = (await api.preview.activities(
          captureRunId: 'run-1',
        )).items.last;
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: AnimatedBuilder(
                animation: controller,
                builder: (context, _) => EvidenceConversationTimeline(
                  controller: controller,
                  activities: [activity],
                  copy: AppCopy.forLanguage(language),
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final raw = find.byKey(Key('exchange-raw-${activity.id}'));
        await tester.ensureVisible(raw);
        await tester.tap(raw);
        await tester.pumpAndSettle();
        final expected = language == AppLanguage.english
            ? 'The control request timed out. Refresh to check the latest state. Agent work continues; recorded data may be incomplete.'
            : '控制请求超时。请刷新以查看最新状态。Agent 工作会继续；记录的数据可能不完整。';
        expect(find.text(expected), findsOneWidget);
        expect(controller.rawEvidenceError(activity.id), expected);
        await controller.loadExchangeDetail('timeout');
        expect(controller.exchangeError('timeout'), expected);
        expect(find.textContaining('Future not completed'), findsNothing);
      },
    );
  }
}

final class _TimeoutPresentationApi implements ControlApi {
  final preview = PreviewControlApi();
  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) {
    if (exchangeId == 'timeout') {
      throw TimeoutException(
        'Future not completed',
        const Duration(seconds: 30),
      );
    }
    return preview.exchange(exchangeId, contentView: contentView);
  }

  @override
  Future<RawEvidencePage> rawEvidence(String exchangeId) =>
      throw TimeoutException(
        'Future not completed',
        const Duration(seconds: 30),
      );
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

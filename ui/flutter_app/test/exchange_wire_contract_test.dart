import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'conversation_reader_test.dart' as support;
import 'fixtures/server_inspection_samples.dart';

const problemWire = <String, Object?>{
  'type': 'urn:vibermate:error:inspection-changed',
  'title': 'Conflict',
  'status': 409,
  'code': 'inspection_changed',
};

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final samples = jsonDecode(serverHostSummarySamples) as List;
  test('exact reported summary code maps to inspection_changed', () {
    final error = ControlProblem.fromJson(problemWire, status: 409);
    expect(error.reasonCode, 'inspection_changed');
    expect(error.messageKey, 'error.inspection_changed');
  });
  test('public continuation accepts the actual server issued cursor', () async {
    final wire = jsonDecode(serverCursorSamples) as List;
    final detail =
        (wire.firstWhere((sample) => sample['label'] == 'with_content_detail')
                as Map)['body']
            as Map;
    final pageSample =
        wire.firstWhere((sample) => sample['label'] == 'issued_cursor_page')
            as Map;
    final pageWire = pageSample['body'];
    final id = detail['id'] as String;
    final cursor =
        ((detail['content'] as Map)['page'] as Map)['requestNextCursor']
            as String;
    expect(pageSample['status'], 200);
    expect(
      ExchangeContentPage.fromJson(pageWire, 'contentPage').exchangeId,
      id,
    );
    var calls = 0;
    String? sentCursor;
    final client = MockClient((request) async {
      calls++;
      expect(request.url.path, '/api/v1/exchanges/$id');
      sentCursor = request.url.queryParameters['contentCursor'];
      return http.Response(jsonEncode(pageWire), 200);
    });
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:1'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'synthetic-issued-cursor',
        expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
      ),
      client: client,
      inspectSession: false,
    );
    addTearDown(api.close);
    ExchangeContentPage? result;
    Object? failure;
    try {
      result = await api.exchangeContentPage(id, cursor);
    } catch (error) {
      failure = error;
    }
    expect(
      calls,
      1,
      reason: 'An actual server-issued cursor must reach the API unchanged',
    );
    expect(sentCursor, cursor);
    expect(failure, isNull);
    expect(result?.exchangeId, id);
  });
  test(
    'public exchange decodes a recorded production detail before continuation',
    () async {
      final wire = jsonDecode(serverCursorSamples) as List;
      final body =
          (wire.firstWhere((sample) => sample['label'] == 'with_content_detail')
                  as Map)['body']
              as Map;
      final id = body['id'] as String;
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:1'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'synthetic-detail',
          expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
        ),
        client: MockClient((request) async {
          expect(request.url.queryParameters['contentView'], 'reading');
          expect(request.url.queryParameters['contentMode'], 'paged');
          return http.Response(jsonEncode(body), 200);
        }),
        inspectSession: false,
      );
      addTearDown(api.close);
      expect(
        (await api.exchange(
          id,
          contentView: 'reading',
        )).content.page?.requestNextCursor,
        ((body['content'] as Map)['page'] as Map)['requestNextCursor'],
      );
    },
  );

  for (final (cursor, valid) in [
    ('legacy_Cursor-1', true),
    ('proof_1.inner-2', true),
    ('a' * 2048, true),
    ('a.${'b' * 2046}', true),
    ('', false),
    ('a.', false),
    ('.a', false),
    ('a.b.c', false),
    ('a b', false),
    ('a\n', false),
    ('a=', false),
    ('a/b', false),
    ('a' * 2049, false),
  ]) {
    test(
      'cursor request and DTO share bounded syntax ${cursor.length} ${cursor.substring(0, cursor.length.clamp(0, 12))}',
      () async {
        final wire = jsonDecode(serverCursorSamples) as List;
        final body =
            (wire.firstWhere(
                      (sample) => sample['label'] == 'with_content_detail',
                    )
                    as Map)['body']
                as Map;
        final page =
            (wire.firstWhere(
                  (sample) => sample['label'] == 'issued_cursor_page',
                )
                as Map)['body'];
        ((body['content'] as Map)['page'] as Map)['requestNextCursor'] = cursor;
        if (valid) {
          expect(
            () => ExchangeDetail.fromJson(body, 'exchange'),
            returnsNormally,
          );
        } else {
          expect(
            () => ExchangeDetail.fromJson(body, 'exchange'),
            throwsA(isA<ControlContractException>()),
          );
        }
        var calls = 0;
        final api = await HttpControlApi.connect(
          DesktopSession(
            baseUrl: Uri.parse('http://127.0.0.1:1'),
            readToken: 'R' * 43,
            writeToken: 'W' * 43,
            instanceId: 'syntax',
            expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
          ),
          client: MockClient((request) async {
            calls++;
            expect(request.url.queryParameters['contentCursor'], cursor);
            return http.Response(jsonEncode(page), 200);
          }),
          inspectSession: false,
        );
        addTearDown(api.close);
        if (valid) {
          await api.exchangeContentPage(body['id'] as String, cursor);
        } else {
          await expectLater(
            api.exchangeContentPage(body['id'] as String, cursor),
            throwsA(isA<ControlContractException>()),
          );
        }
        expect(calls, valid ? 1 : 0);
      },
    );
  }
  for (final (index, raw) in samples.indexed) {
    final sample = raw as Map<String, dynamic>;
    test(
      'production decoder accepts actual HostServer sample $index ${sample['label']}',
      () {
        expect(sample['status'], 200);
        if ((sample['label'] as String).endsWith('_detail')) {
          final body = sample['body'] as Map<String, dynamic>;
          expect(
            () => ExchangeDetail.fromJson(body, 'exchange'),
            returnsNormally,
          );
        } else {
          expect(
            () => ExchangeSummary.fromJson(sample['body'], 'summary'),
            returnsNormally,
          );
        }
      },
    );
  }
  test(
    'repeated summary conflicts discard concurrently successful detail HTTP responses',
    () async {
      final detailWire =
          (samples.firstWhere(
                    (sample) => sample['label'] == 'without_content_detail',
                  )
                  as Map)['body']
              as Map<String, dynamic>;
      final id = detailWire['id'] as String;
      final runID = (detailWire['parentRefs'] as Map)['captureRunId'] as String;
      Completer<http.Response>? delayed;
      Completer<void>? entered;
      var detailCalls = 0, summaryCalls = 0;
      final client = MockClient((request) async {
        if (request.url.path == '/api/v1/activities/summary') {
          summaryCalls++;
          return http.Response(jsonEncode(problemWire), 409);
        }
        if (request.url.path == '/api/v1/exchanges/$id') {
          detailCalls++;
          if (entered != null && !entered.isCompleted) entered.complete();
          return delayed?.future ?? http.Response(jsonEncode(detailWire), 200);
        }
        throw StateError('unexpected synthetic control path');
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:1'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'synthetic-summary-race',
          expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
        ),
        client: client,
        inspectSession: false,
      );
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      for (var pass = 1; pass <= 3; pass++) {
        delayed = Completer<http.Response>();
        entered = Completer<void>();
        final read = controller.loadExchangeDetail(
          id,
          contentView: 'reading',
          refresh: true,
        );
        await entered.future.timeout(const Duration(seconds: 2));
        await expectLater(
          controller.loadActivitySummary(
            ActivitySummaryScope(captureRunId: runID),
          ),
          throwsA(
            isA<ControlProblem>().having(
              (e) => e.reasonCode,
              'reason',
              'inspection_changed',
            ),
          ),
        );
        delayed.complete(http.Response(jsonEncode(detailWire), 200));
        expect(await read, isNull);
        expect(controller.exchangeDetail(id, contentView: 'reading'), isNull);
      }
      delayed = null;
      entered = null;
      final isolated = await controller.loadExchangeDetail(
        id,
        contentView: 'reading',
        refresh: true,
      );
      expect(isolated?.id, id);
      expect(isolated?.status, 'succeeded');
      await expectLater(
        controller.loadActivitySummary(
          ActivitySummaryScope(captureRunId: runID),
        ),
        throwsA(isA<ControlProblem>()),
      );
      expect(controller.exchangeDetail(id, contentView: 'reading'), isNull);
      expect(detailCalls, 4);
      expect(summaryCalls, 4);
    },
  );
}

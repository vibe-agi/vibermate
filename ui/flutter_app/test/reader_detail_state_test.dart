import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'conversation_reader_test.dart' as support;
import 'fixtures/server_inspection_samples.dart';

final class _ReaderApi extends Fake implements ControlApi {
  final fixture = PreviewControlApi();
  ExchangeDetail? current;
  Completer<ExchangeDetail>? delayed;
  Object? failure;
  final calls = <String>[];

  @override
  Future<ExchangeDetail> exchange(
    String id, {
    String contentView = 'incremental',
  }) {
    calls.add('$id:$contentView');
    if (failure != null) return Future.error(failure!);
    return delayed?.future ?? Future.value(current);
  }

  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) =>
      fixture.runtimeUsage(query);
  @override
  Future<CaptureAssignment> captureAssignment(String key) =>
      fixture.captureAssignment(key);
  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) =>
      fixture.activitySummary(scope);
  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => fixture.conversations(
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
  }) => fixture.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    environmentId: environmentId,
    conversationId: conversationId,
  );
  @override
  Future<void> close() => fixture.close();
}

ExchangeDetail noContent(ExchangeDetail base, {String? status}) =>
    ExchangeDetail(
      id: base.id,
      status: status ?? base.status,
      environment: base.environment,
      parentRefs: base.parentRefs,
      diagnosis: base.diagnosis,
      processingTrace: base.processingTrace,
      clientIdentity: base.clientIdentity,
      content: ExchangeContentDetail.fromJson({
        'state': 'not_recorded',
      }, 'content'),
    );

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  for (final (error, code) in <(Object, String)>[
    (
      const ControlContractException('SECRET malformed response'),
      'control_contract_mismatch',
    ),
    (
      http.ClientException('SECRET transport address'),
      'control_connection_failed',
    ),
    (
      const ControlProblem(
        status: 500,
        reasonCode: 'runtime_unavailable',
        messageKey: 'error.runtime_unavailable',
        detail: 'SECRET server detail',
      ),
      'runtime_unavailable · HTTP 500',
    ),
  ]) {
    testWidgets(
      'failed detail shows safe $code diagnostic and retry clears it',
      (tester) async {
        final api = _ReaderApi()..failure = error;
        final seed = (await api.fixture.activities(
          captureRunId: 'run-1',
        )).items.first;
        final controller = support.makeController(api);
        addTearDown(controller.dispose);
        addTearDown(api.close);
        await tester.pumpWidget(support.host(controller, [seed]));
        await tester.pumpAndSettle();
        final copy = AppCopy.forLanguage(AppLanguage.english);
        expect(find.text(copy('reader.unavailable')), findsOneWidget);
        expect(find.textContaining('SECRET'), findsNothing);
        expect(find.text(code), findsOneWidget);
        expect(find.textContaining('SECRET'), findsNothing);
        api.failure = null;
        api.current = await api.fixture.exchange(
          seed.id,
          contentView: 'reading',
        );
        await tester.tap(find.text(copy('common.retry')));
        await tester.pumpAndSettle();
        expect(find.text(copy('reader.unavailable')), findsNothing);
        expect(find.text(code), findsNothing);
      },
    );
  }

  test(
    'reopening terminal detail rechecks absent content and coalesces active loads',
    () async {
      final api = _ReaderApi();
      final seed = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.fixture.exchange(seed.id, contentView: 'reading');
      api.current = noContent(base);
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      expect(
        (await controller.loadExchangeDetail(
          seed.id,
          contentView: 'reading',
        ))?.content.state,
        'not_recorded',
      );
      api.delayed = Completer<ExchangeDetail>();
      final reopened = controller.loadExchangeDetail(
        seed.id,
        contentView: 'reading',
      );
      final duplicate = controller.loadExchangeDetail(
        seed.id,
        contentView: 'reading',
      );
      api.delayed!.complete(base);
      expect(await reopened, same(base));
      expect(await duplicate, same(base));
      expect(api.calls, hasLength(2));
      expect(
        await controller.loadExchangeDetail(seed.id, contentView: 'reading'),
        same(base),
      );
      expect(
        api.calls,
        hasLength(2),
        reason: 'complete retained detail remains reusable',
      );
      await controller.loadExchangeDetail(
        seed.id,
        contentView: 'reading',
        refresh: true,
      );
      expect(api.calls, hasLength(3));
    },
  );

  testWidgets(
    'reopening terminal request-only content rechecks the response without new Activity evidence',
    (tester) async {
      final wire = jsonDecode(serverCursorSamples) as List;
      final body =
          (wire.firstWhere((sample) => sample['label'] == 'with_content_detail')
                  as Map)['body']
              as Map<String, dynamic>;
      final complete = ExchangeDetail.fromJson(body, 'exchange');
      (body['content'] as Map).remove('response');
      // Source publication admits request-only manifests and later completes them;
      // AvailableBodies already reports true while the response is absent.
      final requestOnly = ExchangeDetail.fromJson(body, 'exchange');
      expect(requestOnly.status, 'succeeded');
      expect(requestOnly.content.state, 'recorded');
      expect(requestOnly.content.response, isNull);
      final api = _ReaderApi()..current = requestOnly;
      final seed = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final activity = support.readerActivity(
        seed,
        complete.id,
        contentAvailable: true,
      );
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      await tester.pumpWidget(support.host(controller, [activity]));
      await tester.pumpAndSettle();
      expect(find.text('synthetic question'), findsOneWidget);
      expect(find.text('synthetic response'), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
      api.current = complete;
      await tester.pumpWidget(support.host(controller, [activity]));
      await tester.pumpAndSettle();
      expect(find.text('synthetic response'), findsOneWidget);
      expect(api.calls, hasLength(2));
    },
  );

  testWidgets('recorded production HTTP detail renders in the reader', (
    tester,
  ) async {
    final wire = jsonDecode(serverCursorSamples) as List;
    final body =
        (wire.firstWhere((sample) => sample['label'] == 'with_content_detail')
                as Map)['body']
            as Map;
    final fixture = PreviewControlApi();
    addTearDown(fixture.close);
    final seed = (await fixture.activities(
      captureRunId: 'run-1',
    )).items.firstWhere((a) => a.status == 'succeeded');
    final activity = support.readerActivity(
      seed,
      body['id'] as String,
      contentAvailable: true,
    );
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:1'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'production-reader-fixture',
        expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
      ),
      client: MockClient((request) async {
        if (request.url.path == '/api/v1/exchanges/${activity.id}') {
          expect(request.url.queryParameters['contentView'], 'reading');
          return http.Response(jsonEncode(body), 200);
        }
        // Usage is independent of retained content and may be unavailable.
        return http.Response(
          jsonEncode({
            'type': 'urn:vibermate:error:runtime-unavailable',
            'title': 'Service Unavailable',
            'status': 503,
            'code': 'runtime_unavailable',
          }),
          503,
        );
      }),
      inspectSession: false,
    );
    final controller = support.makeController(api);
    addTearDown(controller.dispose);
    addTearDown(api.close);
    await tester.pumpWidget(support.host(controller, [activity]));
    await tester.pumpAndSettle();
    expect(find.text('synthetic question'), findsOneWidget);
    expect(find.text('synthetic response'), findsOneWidget);
    expect(find.textContaining('control_contract_mismatch'), findsNothing);
  });

  testWidgets(
    'content arriving after terminal status refreshes visible reader row',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1220, 950));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ReaderApi();
      final seed = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.fixture.exchange(seed.id, contentView: 'reading');
      api.current = noContent(base);
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      final absent = support.readerActivity(
        seed,
        seed.id,
        contentAvailable: false,
      );
      await tester.pumpWidget(
        support.host(controller, [
          absent,
        ], language: AppLanguage.simplifiedChinese),
      );
      await tester.pumpAndSettle();
      expect(api.calls, hasLength(1));
      expect(find.textContaining('没有可用的已保存对话内容'), findsOneWidget);
      api.current = support.detail(
        base,
        [support.message('user', 'AUDIT_QUESTION')],
        [support.block('text', 'AUDIT_LATE_RECORDED_REPLY')],
        reading: true,
      );
      final present = support.readerActivity(
        seed,
        seed.id,
        contentAvailable: true,
      );
      await tester.pumpWidget(
        support.host(controller, [
          present,
        ], language: AppLanguage.simplifiedChinese),
      );
      await tester.pumpAndSettle();
      expect(find.text('AUDIT_LATE_RECORDED_REPLY'), findsOneWidget);
    },
  );

  test(
    'old capture detail 503 cannot invalidate the new capture selection',
    () async {
      final api = _ReaderApi();
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      controller.data = await api.fixture.loadDashboard();
      await controller.selectCapture('managed_run:run-2');
      final oldID = controller.selectedCapturePage!.items.first.id;
      api.delayed = Completer<ExchangeDetail>();
      final oldLoad = controller.loadExchangeDetail(
        oldID,
        contentView: 'reading',
      );
      await controller.selectCapture('managed_run:run-1');
      final page = controller.selectedCapturePage;
      final assignment = controller.selectedAssignment;
      expect(page, isNotNull);
      expect(page!.items, isNotEmpty);
      api.delayed!.completeError(
        const ControlProblem(
          status: 503,
          reasonCode: 'inspection_unavailable',
          messageKey: 'error.inspection_unavailable',
        ),
      );
      expect(await oldLoad, isNull);
      expect(controller.selectedCapturePage, same(page));
      expect(controller.selectedAssignment, same(assignment));
      expect(controller.captureInspectionFailure, isNull);
    },
  );

  test(
    'same selection detail 503 still invalidates unavailable inspection',
    () async {
      final api = _ReaderApi();
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      controller.data = await api.fixture.loadDashboard();
      await controller.selectCapture('managed_run:run-1');
      api.failure = const ControlProblem(
        status: 503,
        reasonCode: 'inspection_unavailable',
        messageKey: 'error.inspection_unavailable',
      );
      expect(
        await controller.loadExchangeDetail(
          controller.selectedActivities.first.id,
        ),
        isNull,
      );
      expect(controller.selectedCapturePage, isNull);
      expect(
        controller.captureInspectionFailure?.diagnostic,
        'inspection_unavailable · HTTP 503',
      );
      expect(controller.selectedAssignment, isNotNull);
    },
  );

  test(
    'old capture detail conflict still invalidates retained evidence globally',
    () async {
      final api = _ReaderApi();
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      controller.data = await api.fixture.loadDashboard();
      await controller.selectCapture('managed_run:run-2');
      api.delayed = Completer<ExchangeDetail>();
      final load = controller.loadExchangeDetail(
        controller.selectedActivities.first.id,
      );
      await controller.selectCapture('managed_run:run-1');
      api.delayed!.completeError(
        const ControlProblem(
          status: 409,
          reasonCode: 'inspection_changed',
          messageKey: 'error.inspection_changed',
        ),
      );
      expect(await load, isNull);
      expect(controller.selectedCapturePage, isNull);
      expect(
        controller.captureInspectionFailure?.diagnostic,
        'inspection_changed · HTTP 409',
      );
      expect(controller.selectedAssignment, isNotNull);
    },
  );

  testWidgets(
    'disposing reader during detail load leaves no late widget update',
    (tester) async {
      final api = _ReaderApi();
      final seed = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.first;
      final base = await api.fixture.exchange(seed.id, contentView: 'reading');
      api.delayed = Completer<ExchangeDetail>();
      final controller = support.makeController(api);
      addTearDown(api.close);
      await tester.pumpWidget(support.host(controller, [seed]));
      await tester.pump();
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
      api.delayed!.complete(base);
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      expect(
        controller.exchangeDetail(seed.id, contentView: 'reading'),
        isNull,
      );
    },
  );

  testWidgets(
    'reopening a completed request must replace a cached pending detail',
    (tester) async {
      final api = _ReaderApi();
      final done = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.fixture.exchange(done.id, contentView: 'reading');
      final pending = ActivityRecord(
        id: done.id,
        occurredAt: done.occurredAt,
        title: done.title,
        status: 'pending',
        reasonCode: null,
        source: done.source,
        conversation: done.conversation,
        environment: done.environment,
        parentRefs: done.parentRefs,
        contentAvailable: false,
      );
      api.current = noContent(base, status: 'pending');
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      await tester.pumpWidget(
        support.host(controller, [
          pending,
        ], language: AppLanguage.simplifiedChinese),
      );
      await tester.pumpAndSettle();
      expect(find.text('正在等待最终响应…'), findsOneWidget);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
      api.current = support.detail(
        base,
        [support.message('user', 'AUDIT_QUESTION')],
        [support.block('text', 'AUDIT_COMPLETED_REPLY')],
        reading: true,
      );
      await tester.pumpWidget(
        support.host(controller, [
          done,
        ], language: AppLanguage.simplifiedChinese),
      );
      await tester.pumpAndSettle();
      expect(find.text('正在等待最终响应…'), findsNothing);
      expect(find.text('AUDIT_COMPLETED_REPLY'), findsOneWidget);
    },
  );

  testWidgets(
    'request pending and not recorded messages are independent of capture lifecycle',
    (tester) async {
      final api = _ReaderApi();
      final seed = (await api.fixture.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.fixture.exchange(seed.id, contentView: 'reading');
      api.current = noContent(base, status: 'pending');
      final controller = support.makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.close);
      await tester.pumpWidget(
        support.host(controller, [
          seed,
        ], language: AppLanguage.simplifiedChinese),
      );
      await tester.pumpAndSettle();
      expect(find.textContaining('没有可用的已保存对话内容'), findsOneWidget);
      expect(find.text('正在等待最终响应…'), findsOneWidget);
    },
  );
}

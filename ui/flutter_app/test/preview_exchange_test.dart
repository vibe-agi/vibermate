import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  test(
    'Conversation index preserves proven boundaries and cursor identity',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);

      final first = await api.activities(limit: 50);
      expect(first.items, hasLength(50));
      expect(first.nextCursor, isNotNull);
      final second = await api.activities(cursor: first.nextCursor, limit: 50);
      expect(second.items, hasLength(50));
      expect(
        first.items
            .map((item) => item.id)
            .toSet()
            .intersection(second.items.map((item) => item.id).toSet()),
        isEmpty,
      );

      final summaries = ConversationSummary.fromActivities(first.items);
      final managed = summaries.where((item) => item.captureRunId != null);
      final manual = summaries.where(
        (item) => item.latest.manualCaptureId != null,
      );
      expect(managed, isNotEmpty);
      expect(managed.first.turnCount, greaterThan(1));
      expect(manual, isNotEmpty);
      expect(manual.every((item) => item.turnCount == 1), isTrue);
      expect(manual.map((item) => item.key).toSet(), hasLength(manual.length));

      final manualCaptureId = manual.first.latest.manualCaptureId!;
      final manualFirst = await api.activities(
        limit: 10,
        manualCaptureId: manualCaptureId,
      );
      final manualSecond = await api.activities(
        cursor: manualFirst.nextCursor,
        limit: 20,
        manualCaptureId: manualCaptureId,
      );
      expect(manualFirst.items, hasLength(10));
      expect(manualSecond.items, hasLength(14));
      expect(manualSecond.nextCursor, isNull);
      expect(
        [...manualFirst.items, ...manualSecond.items].every(
          (item) =>
              item.manualCaptureId == manualCaptureId &&
              item.captureRunId == null,
        ),
        isTrue,
      );
      expect(
        () => api.activities(
          captureRunId: 'run-1',
          manualCaptureId: manualCaptureId,
        ),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test(
    'Exchange projection expands from incremental suffix to frozen snapshot',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final page = await api.activities(limit: 50, captureRunId: 'run-1');
      final activity = page.items.firstWhere(
        (item) => item.id.endsWith('-exchange-222'),
      );

      final incremental = await api.exchange(activity.id);
      expect(incremental.content.requestProjection?.view, 'incremental');
      expect(
        incremental.content.requestProjection?.relationship,
        'incremental',
      );
      expect(incremental.content.request?.messages, hasLength(1));
      expect(incremental.content.response, isNotNull);
      expect(incremental.processingTrace.attempts, hasLength(1));
      expect(
        incremental.processingTrace.attempts.single.exchangeId,
        activity.id,
      );

      final full = await api.exchange(activity.id, contentView: 'full');
      expect(full.content.requestProjection?.view, 'full');
      expect(full.content.request?.messages, hasLength(3));
      expect(full.environment.digest, activity.environment.digest);
      expect(full.environment.accountId, activity.accountId);
    },
  );
}

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> summary(String kind, Map<String, Object?> observation) => {
  'scope': {
    kind == 'managed_run' ? 'captureRunId' : 'manualCaptureId': 'capture',
  },
  'generatedAt': '2026-10-18T00:00:00Z',
  'requests': 0,
  'succeeded': 0,
  'failed': 0,
  'canceled': 0,
  'pending': 0,
  'firstObservedAt': null,
  'lastObservedAt': null,
  'failures': <Object?>[],
  'otherFailures': 0,
  'captureObservation': observation,
};
Map<String, Object?> observation(String kind) => {
  'captureKind': kind,
  'captureId': 'capture',
  'archiveEpoch': 1,
  'captureGeneration': 2,
  'credentialRevision': kind == 'managed_run' ? 0 : 3,
  'firstObservedAt': kind == 'managed_run' ? '2026-10-17T23:00:00Z' : null,
  'lastObservedAt': kind == 'manual_capture' ? '2026-10-17T23:30:00Z' : null,
};
void main() {
  for (final kind in ['managed_run', 'manual_capture']) {
    test(
      'Capture observation preserves sparse $kind facts with zero requests',
      () {
        final value = ExchangeSummary.fromJson(
          summary(kind, observation(kind)),
          'summary',
        );
        expect(value.requests, 0);
        expect(value.captureObservation, isNotNull);
        expect(value.captureObservation!.captureKind, kind);
        expect(value.captureObservation!.archiveEpoch, 1);
        expect(value.captureObservation!.captureGeneration, 2);
        expect(
          value.captureObservation!.firstObservedAt == null,
          kind == 'manual_capture',
        );
        expect(
          value.captureObservation!.lastObservedAt == null,
          kind == 'managed_run',
        );
        expect(value.firstObservedAt, isNull);
        expect(value.lastObservedAt, isNull);
      },
    );
  }
  test(
    'Capture observation uses the same Capture ID grammar as its launch scope',
    () {
      final fact = observation('managed_run')..['captureId'] = '_capture';
      final payload = summary('managed_run', fact)
        ..['scope'] = {'captureRunId': '_capture'};
      expect(
        ExchangeSummary.fromJson(
          payload,
          'summary',
        ).captureObservation!.captureId,
        '_capture',
      );
    },
  );
  test('Capture observation preserves both unknown counterparts', () {
    final unknown = observation('managed_run')..['firstObservedAt'] = null;
    final value = ExchangeSummary.fromJson(
      summary('managed_run', unknown),
      'summary',
    );
    expect(value.captureObservation!.firstObservedAt, isNull);
    expect(value.captureObservation!.lastObservedAt, isNull);
    expect(value.requests, 0);
  });
  test(
    'Capture observation refuses invalid scope revision and fabricated request times',
    () {
      final base = observation('manual_capture');
      for (final bad in <Map<String, Object?>>[
        {...base, 'captureId': 'another'},
        {...base, 'captureKind': 'managed_run'},
        {...base, 'credentialRevision': 0},
        {...base, 'archiveEpoch': 0},
        {...base, 'captureGeneration': 0},
        {...base, 'firstObservedAt': '2026-10-18T00:00:00Z'},
        {...base, 'extra': true},
      ]) {
        expect(
          () => ExchangeSummary.fromJson(
            summary('manual_capture', bad),
            'summary',
          ),
          throwsA(isA<ControlContractException>()),
        );
      }
      final session = summary('manual_capture', base)
        ..['scope'] = {'client': 'codex', 'sessionId': 'session'};
      expect(
        () => ExchangeSummary.fromJson(session, 'summary'),
        throwsA(isA<ControlContractException>()),
      );
      final fakeRequest = summary('manual_capture', base)
        ..['firstObservedAt'] = '2026-10-17T23:30:00Z';
      expect(
        () => ExchangeSummary.fromJson(fakeRequest, 'summary'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );
}

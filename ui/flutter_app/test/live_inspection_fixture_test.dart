import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';

import 'live_inspection_fixture.dart';

void main() {
  test(
    'live inspection waits for the requested optional read to succeed',
    () async {
      var calls = 0;
      final expected = Object();
      final actual = await readLiveInspection(
        'new Capture activities',
        () async {
          calls++;
          if (calls == 1) {
            throw const ControlProblem(
              status: 503,
              reasonCode: 'inspection_unavailable',
              messageKey: 'unavailable',
            );
          }
          if (calls == 2) {
            throw const ControlProblem(
              status: 409,
              reasonCode: 'inspection_changed',
              messageKey: 'changed',
            );
          }
          return expected;
        },
      );
      expect(actual, same(expected));
      expect(calls, 3);
    },
  );

  for (final problem in [
    const ControlProblem(
      status: 401,
      reasonCode: 'unauthorized',
      messageKey: 'unauthorized',
    ),
    const ControlProblem(
      status: 503,
      reasonCode: 'control_unavailable',
      messageKey: 'unavailable',
    ),
    const ControlProblem(
      status: 404,
      reasonCode: 'capture_not_found',
      messageKey: 'missing',
    ),
  ]) {
    test('live inspection never retries ${problem.reasonCode}', () async {
      var calls = 0;
      await expectLater(
        readLiveInspection('fixture', () async {
          calls++;
          throw problem;
        }),
        throwsA(same(problem)),
      );
      expect(calls, 1);
    });
  }

  test(
    'unavailable live inspection fails at a finite fixture deadline',
    () async {
      var calls = 0;
      await expectLater(
        readLiveInspection('never ready', () async {
          calls++;
          throw const ControlProblem(
            status: 503,
            reasonCode: 'inspection_unavailable',
            messageKey: 'unavailable',
          );
        }, timeout: Duration.zero),
        throwsA(
          isA<TimeoutException>().having(
            (e) => e.message,
            'diagnostic',
            contains('never ready'),
          ),
        ),
      );
      expect(calls, 1);
    },
  );
}

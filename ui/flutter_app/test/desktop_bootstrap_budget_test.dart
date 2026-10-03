import 'dart:async';
import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_bootstrap_budget.dart';

void main() {
  const starting = {
    'schema': 'vibermate-daemon-progress-v1',
    'phase': 'runtime_starting',
  };
  const terminal = {'schema': 'vibermate-daemon-bootstrap-v1', 'pid': 123};
  test(
    'initial starting frame is bounded by five seconds from launch',
    () async {
      var elapsed = Duration.zero;
      final budget = DesktopBootstrapBudget(elapsed: () => elapsed);
      Stream<String> frames() async* {
        elapsed = const Duration(seconds: 5);
        yield jsonEncode(starting);
        yield jsonEncode(terminal);
      }

      await expectLater(
        readDesktopBootstrapFrames(frames(), budget),
        throwsA(isA<TimeoutException>()),
      );
    },
  );
  test('both owned phases use bounded stream parser before terminal', () async {
    for (final phase in ['storage_preparing', 'storage_upgrade']) {
      var elapsed = Duration.zero;
      final budget = DesktopBootstrapBudget(elapsed: () => elapsed);
      Stream<String> frames() async* {
        yield jsonEncode(starting);
        elapsed = const Duration(seconds: 119);
        yield jsonEncode({
          'schema': 'vibermate-daemon-progress-v1',
          'phase': phase,
        });
        elapsed = const Duration(minutes: 19);
        yield jsonEncode(terminal);
      }

      expect(await readDesktopBootstrapFrames(frames(), budget), terminal);
      expect(budget.remaining, const Duration(minutes: 1));
    }
  });
  test('duplicate, unknown and capability-bearing progress reject', () async {
    for (final invalid in [
      starting,
      {'schema': 'vibermate-daemon-progress-v1', 'phase': 'unknown'},
      {...starting, 'bootstrapNonce': 'not-authority'},
    ]) {
      final budget = DesktopBootstrapBudget(elapsed: () => Duration.zero);
      await expectLater(
        readDesktopBootstrapFrames(
          Stream.fromIterable([jsonEncode(starting), jsonEncode(invalid)]),
          budget,
        ),
        throwsFormatException,
      );
    }
    final budget = DesktopBootstrapBudget(elapsed: () => Duration.zero);
    await expectLater(
      readDesktopBootstrapFrames(
        Stream.fromIterable([
          jsonEncode(starting),
          jsonEncode({
            'schema': 'vibermate-daemon-progress-v1',
            'phase': 'storage_preparing',
          }),
          jsonEncode({
            'schema': 'vibermate-daemon-progress-v1',
            'phase': 'storage_upgrade',
          }),
        ]),
        budget,
      ),
      throwsFormatException,
    );
  });
  test('EOF and malformed terminal retain preparation fact', () async {
    for (final tail in <String?>[
      null,
      '{bad json',
      jsonEncode({
        'schema': 'vibermate-daemon-failure-v1',
        'reason': 'invented',
      }),
    ]) {
      final budget = DesktopBootstrapBudget(elapsed: () => Duration.zero);
      await expectLater(
        readDesktopBootstrapFrames(
          Stream.fromIterable([
            jsonEncode(starting),
            jsonEncode({
              'schema': 'vibermate-daemon-progress-v1',
              'phase': 'storage_upgrade',
            }),
            ?tail,
          ]),
          budget,
        ),
        throwsFormatException,
      );
      expect(budget.upgradeSeen, isTrue);
    }
  });
  test(
    'recognized preparation with malformed extra fields prevents fallback',
    () async {
      final budget = DesktopBootstrapBudget(elapsed: () => Duration.zero);
      await expectLater(
        readDesktopBootstrapFrames(
          Stream.fromIterable([
            jsonEncode(starting),
            jsonEncode({
              'schema': 'vibermate-daemon-progress-v1',
              'phase': 'storage_preparing',
              'bootstrapNonce': 'invalid',
            }),
          ]),
          budget,
        ),
        throwsFormatException,
      );
      expect(budget.upgradeSeen, isTrue);
    },
  );
  test(
    'pending stream elapsed advance cannot activate at terminal boundary',
    () async {
      for (final prepare in [false, true]) {
        var elapsed = Duration.zero;
        final budget = DesktopBootstrapBudget(elapsed: () => elapsed);
        Stream<String> frames() async* {
          yield jsonEncode(starting);
          if (prepare) {
            yield jsonEncode({
              'schema': 'vibermate-daemon-progress-v1',
              'phase': 'storage_upgrade',
            });
          }
          elapsed = prepare
              ? const Duration(minutes: 20)
              : const Duration(seconds: 120);
          yield jsonEncode(terminal);
        }

        await expectLater(
          readDesktopBootstrapFrames(frames(), budget),
          throwsA(isA<TimeoutException>()),
        );
      }
    },
  );
  test('ordinary readiness expires 120 seconds after starting', () {
    var now = Duration.zero;
    final budget = DesktopBootstrapBudget(elapsed: () => now);
    budget.acceptRuntimeStarting();
    now = const Duration(seconds: 119);
    expect(budget.remaining, const Duration(seconds: 1));
    now = const Duration(seconds: 120);
    expect(() => budget.remaining, throwsA(isA<TimeoutException>()));
  });
  test('one preparation grants only launch-relative twenty minutes', () {
    var now = const Duration(seconds: 4);
    final budget = DesktopBootstrapBudget(elapsed: () => now);
    budget.acceptRuntimeStarting();
    now = const Duration(seconds: 119);
    budget.acceptStoragePreparation();
    expect(budget.remaining, const Duration(seconds: 1081));
    expect(budget.upgradeSeen, isTrue);
    expect(budget.acceptStoragePreparation, throwsFormatException);
    expect(budget.acceptRuntimeStarting, throwsFormatException);
    now = const Duration(minutes: 20);
    expect(() => budget.remaining, throwsA(isA<TimeoutException>()));
  });
  test('preparation cannot revive an expired ordinary deadline', () {
    var now = Duration.zero;
    final budget = DesktopBootstrapBudget(elapsed: () => now);
    budget.acceptRuntimeStarting();
    now = const Duration(seconds: 120);
    expect(budget.acceptStoragePreparation, throwsA(isA<TimeoutException>()));
  });
  test('missing starting and backwards time are rejected', () {
    var now = const Duration(seconds: 4);
    final budget = DesktopBootstrapBudget(elapsed: () => now);
    expect(budget.acceptStoragePreparation, throwsFormatException);
    budget.acceptRuntimeStarting();
    now = const Duration(seconds: 3);
    expect(() => budget.remaining, throwsStateError);
  });
}

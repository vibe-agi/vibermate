import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_daemon_lifecycle.dart';

void main() {
  test('production lifecycle preserves the daemon drain budget', () {
    const lifecycle = DesktopDaemonLifecycle.production();

    expect(
      lifecycle.gracefulTimeout,
      greaterThanOrEqualTo(const Duration(seconds: 25)),
    );
    expect(lifecycle.terminateTimeout, greaterThan(const Duration(seconds: 1)));
  });

  test(
    'terminate and kill happen only after their preceding budgets',
    () async {
      final process = _FakeDesktopDaemonProcess();
      const lifecycle = DesktopDaemonLifecycle(
        gracefulTimeout: Duration(milliseconds: 30),
        terminateTimeout: Duration(milliseconds: 20),
        killTimeout: Duration(milliseconds: 20),
      );
      final stopwatch = Stopwatch()..start();

      await lifecycle.close(process);

      expect(process.inputClosed, isTrue);
      expect(process.terminateAt, isNotNull);
      expect(process.killAt, isNotNull);
      expect(
        process.terminateAt!,
        greaterThanOrEqualTo(const Duration(milliseconds: 25)),
      );
      expect(
        process.killAt! - process.terminateAt!,
        greaterThanOrEqualTo(const Duration(milliseconds: 15)),
      );
      expect(stopwatch.elapsed, lessThan(const Duration(seconds: 1)));
    },
  );
}

final class _FakeDesktopDaemonProcess implements DesktopDaemonProcess {
  final Completer<int> _exit = Completer<int>();
  final Stopwatch _clock = Stopwatch()..start();

  bool inputClosed = false;
  Duration? terminateAt;
  Duration? killAt;

  @override
  Future<int> get exitCode => _exit.future;

  @override
  Future<void> closeInput() async {
    inputClosed = true;
  }

  @override
  bool terminate() {
    terminateAt = _clock.elapsed;
    return true;
  }

  @override
  bool kill() {
    killAt = _clock.elapsed;
    if (!_exit.isCompleted) _exit.complete(-9);
    return true;
  }
}

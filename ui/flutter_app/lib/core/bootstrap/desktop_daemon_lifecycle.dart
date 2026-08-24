import 'dart:async';

abstract interface class DesktopDaemonProcess {
  Future<int> get exitCode;

  Future<void> closeInput();

  bool terminate();

  bool kill();
}

/// Owns the escalation contract for the packaged daemon.
///
/// Closing stdin asks the daemon to drain HTTP, evidence, and SQLite work. The
/// production grace period is deliberately longer than the daemon's declared
/// 25-second shutdown budget; signals are escalation, not the normal path.
final class DesktopDaemonLifecycle {
  const DesktopDaemonLifecycle({
    required this.gracefulTimeout,
    required this.terminateTimeout,
    required this.killTimeout,
  });

  const DesktopDaemonLifecycle.production()
    : gracefulTimeout = const Duration(seconds: 27),
      terminateTimeout = const Duration(seconds: 5),
      killTimeout = const Duration(seconds: 5);

  final Duration gracefulTimeout;
  final Duration terminateTimeout;
  final Duration killTimeout;

  Future<void> close(DesktopDaemonProcess process) async {
    try {
      await process.closeInput();
    } on Object {
      // An unexpectedly exited child may have already closed the pipe.
    }
    if (await _exitedWithin(process, gracefulTimeout)) return;
    process.terminate();
    if (await _exitedWithin(process, terminateTimeout)) return;
    process.kill();
    await process.exitCode.timeout(killTimeout);
  }

  Future<bool> _exitedWithin(
    DesktopDaemonProcess process,
    Duration timeout,
  ) async {
    try {
      await process.exitCode.timeout(timeout);
      return true;
    } on TimeoutException {
      return false;
    }
  }
}

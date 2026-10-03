import 'dart:async';
import 'dart:convert';
import '../api/control_models.dart';

/// Reads only the existing closed native bootstrap protocol. PID and
/// capability-bearing descriptor validation remain with the launch owner.
Future<JsonObject> readDesktopBootstrapFrames(
  Stream<String> lines,
  DesktopBootstrapBudget budget,
) async {
  final iterator = StreamIterator<String>(lines);
  Future<JsonObject> next(Duration timeout) async {
    if (!await iterator.moveNext().timeout(timeout)) {
      throw const FormatException('Sidecar exited before bootstrap');
    }
    final line = iterator.current;
    if (utf8.encode(line).length > 16 * 1024) {
      throw const FormatException('Bootstrap frame exceeded size limit');
    }
    return requireObject(jsonDecode(line), 'bootstrap.frame');
  }

  try {
    final firstRemaining = const Duration(seconds: 5) - budget._now();
    if (firstRemaining <= Duration.zero) {
      throw TimeoutException('Bootstrap starting deadline exceeded');
    }
    final first = await next(firstRemaining);
    if (budget._now() >= const Duration(seconds: 5)) {
      throw TimeoutException('Bootstrap starting deadline exceeded');
    }
    if (first.length != 2 ||
        first['schema'] != 'vibermate-daemon-progress-v1' ||
        first['phase'] != 'runtime_starting') {
      throw const FormatException('Unexpected starting phase');
    }
    budget.acceptRuntimeStarting();
    var terminal = await next(budget.remaining);
    if (terminal['schema'] == 'vibermate-daemon-progress-v1') {
      if (!const {
        'storage_preparing',
        'storage_upgrade',
      }.contains(terminal['phase'])) {
        throw const FormatException('Unexpected storage preparation');
      }
      budget.acceptStoragePreparation();
      if (terminal.length != 2) {
        throw const FormatException('Unexpected preparation fields');
      }
      terminal = await next(budget.remaining);
    }
    budget.remaining;
    if (terminal['schema'] == 'vibermate-daemon-failure-v1') {
      if (terminal.length != 2 ||
          !const {
            'runtime_unavailable',
            'runtime_already_active',
            'secret_store_unavailable',
            'storage_unavailable',
            'storage_incompatible',
            'storage_upgrade_incomplete',
            'root_reset_failed',
          }.contains(terminal['reason'])) {
        throw const FormatException('Unexpected bootstrap failure');
      }
    } else if (terminal['schema'] != 'vibermate-daemon-bootstrap-v1') {
      throw const FormatException('Unexpected terminal frame');
    }
    return terminal;
  } finally {
    await iterator.cancel();
  }
}

/// Bounded startup work measured from launch; progress conveys no authority.
final class DesktopBootstrapBudget {
  DesktopBootstrapBudget({required Duration Function() elapsed})
    : _elapsed = elapsed;
  final Duration Function() _elapsed;
  Duration? _deadline;
  Duration _last = Duration.zero;
  bool _upgrade = false;
  bool get upgradeSeen => _upgrade;

  Duration _now() {
    final next = _elapsed();
    if (next < _last) throw StateError('Bootstrap clock moved backwards');
    _last = next;
    return next;
  }

  void acceptRuntimeStarting() {
    if (_deadline != null) {
      throw const FormatException('Duplicate starting phase');
    }
    _deadline = _now() + const Duration(seconds: 120);
  }

  void acceptStoragePreparation() {
    if (_deadline == null || _upgrade) {
      throw const FormatException('Unexpected upgrade phase');
    }
    // A frame read at the terminal boundary cannot revive an expired budget.
    remaining;
    _upgrade = true;
    _deadline = const Duration(minutes: 20);
  }

  Duration get remaining {
    final deadline = _deadline;
    if (deadline == null) {
      throw const FormatException('Missing starting phase');
    }
    final left = deadline - _now();
    if (left <= Duration.zero) {
      throw TimeoutException('Bootstrap deadline exceeded');
    }
    return left;
  }
}

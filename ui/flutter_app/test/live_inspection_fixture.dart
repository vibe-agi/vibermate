import 'dart:async';

import 'package:vibermate_app/core/api/control_api.dart';

// Completion boundary for read-only optional inspection in isolated live tests.
// Required commands and outbound requests must never be passed here. Each read
// retains the Control API's own timeout; no overlapping retry is started.
Future<T> readLiveInspection<T>(
  String label,
  Future<T> Function() read, {
  Duration timeout = const Duration(seconds: 10),
}) async {
  final elapsed = Stopwatch()..start();
  while (true) {
    try {
      return await read();
    } on ControlProblem catch (error) {
      final retry =
          (error.status == 503 &&
              error.reasonCode == 'inspection_unavailable') ||
          (error.status == 409 && error.reasonCode == 'inspection_changed');
      if (!retry) rethrow;
      if (elapsed.elapsed >= timeout) {
        throw TimeoutException('$label remained unavailable: $error', timeout);
      }
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }
  }
}

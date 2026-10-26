@TestOn('vm')
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/transparent_capture.dart';

void main() {
  test(
    'optional native capture tolerates a process-only caller without Flutter binding',
    () async {
      final capture = TransparentCaptureController.shared;
      await capture.configure(
        instanceId: 'headless-generation',
        flowPort: 32123,
        flowSecret: 'F' * 43,
      );
      expect(capture.state, 'unavailable');
      expect(capture.enabled, isFalse);
      await capture.clearConfiguration('headless-generation');
    },
  );
}

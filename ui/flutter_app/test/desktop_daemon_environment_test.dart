import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_daemon_environment.dart';

void main() {
  test(
    'sidecar keeps the login environment without the fixed Foundation home',
    () {
      final parent = <String, String>{
        'CFFIXED_USER_HOME': '/private/tmp/isolated-app-home',
        'HOME': '/Users/example',
        'PATH': '/usr/bin:/bin',
        'TMPDIR': '/private/tmp/isolated-app-home/tmp',
      };

      final child = desktopDaemonEnvironment(parent);

      expect(child, <String, String>{
        'HOME': '/Users/example',
        'PATH': '/usr/bin:/bin',
        'TMPDIR': '/private/tmp/isolated-app-home/tmp',
      });
      expect(parent['CFFIXED_USER_HOME'], '/private/tmp/isolated-app-home');
    },
  );
}

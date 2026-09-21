import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/runtime_connection_guide.dart';

void main() {
  const advertised = RuntimeServerAccess(
    transport: 'http',
    authentication: 'runtime_user_password',
    sessionPolicy: 'reusable_until_logout_disable_or_expiry',
    targets: ['172.18.0.2:9666'],
  );

  test('connected DNS origin wins over container interface discovery', () {
    final guide = RuntimeConnectionGuide(
      connectedTarget:
          'https://runtime.example.test:8443/?not-copied=1#settings',
      advertised: advertised,
    );
    expect(guide.serverURL, 'https://runtime.example.test:8443');
    expect(guide.webURL, 'https://runtime.example.test:8443/');
    expect(guide.security, ServerConnectionSecurity.https);
    expect(guide.usesConnectedOrigin, isTrue);
  });

  test('unavailable discovery never invents an http://This Mac command', () {
    final guide = RuntimeConnectionGuide(connectedTarget: 'This Mac');
    expect(guide.available, isFalse);
    expect(guide.serverURL, isNull);
    expect(guide.webURL, isNull);
    expect(guide.security, ServerConnectionSecurity.unavailable);
  });

  test('native App uses the advertised reachable address', () {
    final guide = RuntimeConnectionGuide(
      connectedTarget: 'This Mac',
      advertised: advertised,
    );
    expect(guide.serverURL, 'http://172.18.0.2:9666');
    expect(guide.security, ServerConnectionSecurity.remoteHttp);
    expect(guide.usesConnectedOrigin, isFalse);
  });

  for (final target in [
    'http://localhost:9666',
    'http://127.0.0.1:9666',
    'http://127.12.2.3:9666',
    'http://[::1]:9666',
  ]) {
    test('loopback HTTP scope: $target', () {
      expect(
        RuntimeConnectionGuide(connectedTarget: target).security,
        ServerConnectionSecurity.loopbackHttp,
      );
    });
  }

  for (final target in [
    'http://192.168.1.2:9666',
    'http://localhost.example.test:9666',
    'http://127.0.0.1.example.test:9666',
    'http://[2001:db8::1]:9666',
  ]) {
    test('remote HTTP is not presented as local: $target', () {
      expect(
        RuntimeConnectionGuide(connectedTarget: target).security,
        ServerConnectionSecurity.remoteHttp,
      );
    });
  }

  for (final target in [
    'https://user:password@runtime.example.test',
    'https://',
    'file:///tmp/runtime',
    'https://runtime.example.test:70000',
    'https://runtime.example.test:invalid',
  ]) {
    test(
      'invalid or credential-bearing connected target is not copied: $target',
      () {
        expect(
          RuntimeConnectionGuide(connectedTarget: target).available,
          isFalse,
        );
      },
    );
  }
}

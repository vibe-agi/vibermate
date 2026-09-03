@TestOn('vm')
library;

import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/root_trust_installer.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const channel = MethodChannel('io.vibermate.desktop/root-trust-installer');

  test(
    'native Root installer receives one digest-bound closed payload',
    () async {
      MethodCall? observed;
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, (call) async {
            observed = call;
            return null;
          });
      addTearDown(
        () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(channel, null),
      );
      final der = Uint8List.fromList([0x30, 0x03, 0x01, 0x02, 0x03]);
      final fingerprint = crypto.sha256.convert(der).toString();
      final material = RootCAMaterial.fromJson({
        'rootRevision': 9,
        'fingerprint': fingerprint,
        'certificateDerBase64': base64Encode(der),
      }, 'material');

      await const PlatformRootTrustInstaller().install(material);

      expect(observed?.method, 'installRootCertificate');
      expect(observed?.arguments, {
        'schema': 'vibermate-root-trust-install/v1',
        'rootRevision': 9,
        'fingerprint': fingerprint,
        'certificateDerBase64': base64Encode(der),
      });
    },
  );

  test('native authorization failures remain stable and actionable', () async {
    final material = _material();
    for (final fixture in <({String code, RootTrustInstallerFailure failure})>[
      (
        code: 'user_cancelled',
        failure: RootTrustInstallerFailure.userCancelled,
      ),
      (
        code: 'permission_denied',
        failure: RootTrustInstallerFailure.permissionDenied,
      ),
    ]) {
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(
            channel,
            (_) async => throw PlatformException(code: fixture.code),
          );
      await expectLater(
        const PlatformRootTrustInstaller().install(material),
        throwsA(
          isA<RootTrustInstallerException>().having(
            (error) => error.failure,
            'failure',
            fixture.failure,
          ),
        ),
      );
    }
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, null);
  });

  test('missing native Root installer fails explicitly', () async {
    await expectLater(
      const PlatformRootTrustInstaller().install(_material()),
      throwsA(
        isA<RootTrustInstallerException>().having(
          (error) => error.failure,
          'failure',
          RootTrustInstallerFailure.unavailable,
        ),
      ),
    );
  });
}

RootCAMaterial _material() {
  final der = Uint8List.fromList(utf8.encode('root'));
  return RootCAMaterial.fromJson({
    'rootRevision': 1,
    'fingerprint': crypto.sha256.convert(der).toString(),
    'certificateDerBase64': base64Encode(der),
  }, 'material');
}

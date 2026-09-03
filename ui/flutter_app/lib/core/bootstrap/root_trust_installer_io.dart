import 'package:flutter/services.dart';

import '../api/control_models.dart';
import 'root_trust_installer_contract.dart';

final class PlatformRootTrustInstaller implements RootTrustInstaller {
  const PlatformRootTrustInstaller();

  static const _channel = MethodChannel(
    'io.vibermate.desktop/root-trust-installer',
  );

  @override
  Future<void> install(RootCAMaterial material) async {
    try {
      await _channel.invokeMethod<void>('installRootCertificate', {
        'schema': 'vibermate-root-trust-install/v1',
        'rootRevision': material.rootRevision,
        'fingerprint': material.fingerprint,
        'certificateDerBase64': material.certificateDerBase64,
      });
    } on MissingPluginException {
      throw const RootTrustInstallerException(
        RootTrustInstallerFailure.unavailable,
      );
    } on PlatformException catch (error) {
      throw RootTrustInstallerException(switch (error.code) {
        'invalid_arguments' => RootTrustInstallerFailure.contract,
        'user_cancelled' => RootTrustInstallerFailure.userCancelled,
        'permission_denied' => RootTrustInstallerFailure.permissionDenied,
        _ => RootTrustInstallerFailure.failed,
      });
    }
  }
}

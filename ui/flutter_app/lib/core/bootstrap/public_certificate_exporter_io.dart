import 'package:flutter/services.dart';

import '../api/control_models.dart';
import 'public_certificate_exporter_contract.dart';

final class PlatformPublicCertificateExporter
    implements PublicCertificateExporter {
  const PlatformPublicCertificateExporter();
  static const _channel = MethodChannel(
    'io.vibermate.desktop/public-certificate',
  );

  @override
  Future<bool> save(PublicCertificate certificate) async {
    if (!certificate.available) {
      throw StateError('server certificate unavailable');
    }
    return await _channel.invokeMethod<bool>('saveServerCertificate', {
          'certificatePem': certificate.certificatePem,
          'fileName': certificate.fileName,
        }) ??
        false;
  }
}

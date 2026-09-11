import 'dart:async';
import 'dart:js_interop';

import 'package:web/web.dart' as web;

import '../api/control_models.dart';
import 'public_certificate_exporter_contract.dart';

final class PlatformPublicCertificateExporter
    implements PublicCertificateExporter {
  const PlatformPublicCertificateExporter();

  @override
  Future<bool> save(PublicCertificate certificate) async {
    if (!certificate.available) {
      throw StateError('server certificate unavailable');
    }
    final blob = web.Blob(
      [certificate.certificatePem.toJS].toJS,
      web.BlobPropertyBag(type: 'application/x-pem-file'),
    );
    final url = web.URL.createObjectURL(blob);
    final anchor = web.HTMLAnchorElement()
      ..href = url
      ..download = certificate.fileName
      ..style.display = 'none';
    try {
      web.document.body!.appendChild(anchor);
      anchor.click();
    } finally {
      anchor.remove();
      // Let the browser consume the URL before releasing its backing blob.
      Timer(const Duration(seconds: 30), () => web.URL.revokeObjectURL(url));
    }
    return true;
  }
}

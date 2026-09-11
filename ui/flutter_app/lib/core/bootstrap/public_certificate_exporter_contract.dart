import '../api/control_models.dart';

abstract interface class PublicCertificateExporter {
  /// Returns false when the user cancels the save dialog.
  Future<bool> save(PublicCertificate certificate);
}

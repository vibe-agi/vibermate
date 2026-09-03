import '../api/control_models.dart';

enum RootTrustInstallerFailure {
  unavailable,
  contract,
  userCancelled,
  permissionDenied,
  failed,
}

final class RootTrustInstallerException implements Exception {
  const RootTrustInstallerException(this.failure);

  final RootTrustInstallerFailure failure;
}

abstract interface class RootTrustInstaller {
  Future<void> install(RootCAMaterial material);
}

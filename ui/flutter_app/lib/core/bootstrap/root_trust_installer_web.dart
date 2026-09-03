import '../api/control_models.dart';
import 'root_trust_installer_contract.dart';

final class PlatformRootTrustInstaller implements RootTrustInstaller {
  const PlatformRootTrustInstaller();

  @override
  Future<void> install(RootCAMaterial material) => Future<void>.error(
    const RootTrustInstallerException(RootTrustInstallerFailure.unavailable),
  );
}

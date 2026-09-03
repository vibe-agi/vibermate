import '../core/api/control_models.dart';
import '../core/bootstrap/root_trust_installer_contract.dart';
import 'preview_control_api.dart';

final class PreviewRootTrustInstaller implements RootTrustInstaller {
  const PreviewRootTrustInstaller(this.api);

  final PreviewControlApi api;

  @override
  Future<void> install(RootCAMaterial material) async {
    api.setRootTrustForPreview(installed: true);
  }
}

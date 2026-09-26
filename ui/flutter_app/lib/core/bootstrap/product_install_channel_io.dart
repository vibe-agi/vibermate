import 'dart:io';

import 'product_install_channel_contract.dart';

Future<ProductInstallChannel> detectProductInstallChannel() async {
  try {
    final executable = await File(
      Platform.resolvedExecutable,
    ).resolveSymbolicLinks();
    if (executable.contains('/Caskroom/vibermate/') ||
        executable.contains('/Cellar/vibermate/')) {
      return ProductInstallChannel.homebrew;
    }
  } on FileSystemException {
    // A missing canonical path does not block update guidance.
  }
  return ProductInstallChannel.manual;
}

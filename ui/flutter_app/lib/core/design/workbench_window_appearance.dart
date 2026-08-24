import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import '../preferences/workbench_preferences.dart';

/// Keeps the native macOS window chrome on the same explicit theme choice as
/// the Flutter workbench. A missing channel is expected in widget tests and on
/// platforms that do not own native desktop chrome.
final class PlatformWorkbenchWindowAppearance {
  const PlatformWorkbenchWindowAppearance();

  static const _channel = MethodChannel('io.vibermate.desktop/preferences');

  Future<void> apply(WorkbenchTheme theme) async {
    if (kIsWeb) return;
    try {
      await _channel.invokeMethod<void>('setWorkbenchTheme', theme.wireName);
    } on MissingPluginException {
      return;
    }
  }
}

import 'package:flutter/material.dart';

import 'app/vibermate_app.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  final locale = WidgetsBinding.instance.platformDispatcher.locale;
  runApp(
    ViberMateApp(
      previewMode: const bool.fromEnvironment('VIBERMATE_PREVIEW'),
      preferChinese: locale.languageCode.toLowerCase() == 'zh',
    ),
  );
}

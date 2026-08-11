import 'dart:io';

import 'package:flutter/material.dart';

import 'app/vibermate_app.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(
    ViberMateApp(
      previewMode: const bool.fromEnvironment('VIBERMATE_PREVIEW'),
      preferChinese: Platform.localeName.toLowerCase().startsWith('zh'),
    ),
  );
}

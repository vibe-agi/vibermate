import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test(
    'adaptive macOS controls have their packaged Cupertino icon font',
    () async {
      final source = await rootBundle.loadString('FontManifest.json');
      final manifest = jsonDecode(source) as List<dynamic>;

      expect(
        manifest.any(
          (entry) =>
              entry is Map<String, dynamic> &&
              entry['family'] == 'packages/cupertino_icons/CupertinoIcons' &&
              entry['fonts'] is List<dynamic> &&
              (entry['fonts'] as List<dynamic>).isNotEmpty,
        ),
        isTrue,
      );
    },
  );
}

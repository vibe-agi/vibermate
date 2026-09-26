@TestOn('vm')
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/update/product_version.dart';

void main() {
  test('compiled product version matches pubspec release identity', () {
    final match = RegExp(
      r'^version:\s*([0-9]+\.[0-9]+\.[0-9]+)\+([0-9]+)\s*$',
      multiLine: true,
    ).firstMatch(File('pubspec.yaml').readAsStringSync());
    expect(match, isNotNull);
    expect(productVersion, match!.group(1));
    expect(productBuildNumber, int.parse(match.group(2)!));
    expect(productVersionLabel, 'v$productVersion');
  });
}

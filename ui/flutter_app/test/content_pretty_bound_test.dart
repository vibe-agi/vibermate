import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';

import 'fixtures/content_pretty_bound.dart';

void main() {
  test('Go authenticated bounds dominate actual VM and web pretty output', () {
    final fixtures = jsonDecode(contentPrettyBound) as List;
    expect(fixtures.length, 12);
    for (final fixture in fixtures.cast<Map<String, dynamic>>()) {
      final name = fixture['Name'] as String;
      final value = jsonDecode(fixture['Raw'] as String);
      if (fixture['Finite'] == true) {
        final pretty = const JsonEncoder.withIndent('  ').convert(value);
        // Saturation means only eligible bounds promise a complete upper bound.
        if (fixture['Upper'] <= 131072) {
          expect(
            utf8.encode(pretty).length,
            lessThanOrEqualTo(fixture['Upper'] as int),
            reason: name,
          );
          expect(
            pretty.length,
            lessThanOrEqualTo(fixture['Upper'] as int),
            reason: name,
          );
        }
      } else {
        expect(fixture['Inline'], isFalse, reason: name);
      }
      if (name == 'nonfinite' || name == 'overflowExponent') {
        expect(
          () => const JsonEncoder.withIndent('  ').convert(value),
          throwsA(isA<JsonUnsupportedObjectError>()),
        );
      }
      if (name == 'fit254') expect(fixture['Inline'], isTrue);
      if (name == 'exceed255') expect(fixture['Inline'], isFalse);
    }
  });
}

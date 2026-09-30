@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  test('Go content projections satisfy the Flutter wire contract', () {
    final fixture =
        jsonDecode(
              File(
                '../../api/samples/content-contract.json',
              ).readAsStringSync(),
            )
            as Map<String, dynamic>;
    for (final raw in fixture['previews'] as List) {
      final preview = ActivityRequestPreview.fromJson(raw, 'preview');
      expect(preview.text.trim(), preview.text);
      expect(preview.text.runes.length, lessThanOrEqualTo(180));
    }
    final responses = (fixture['responses'] as List)
        .map((raw) => ExchangeResponse.fromJson(raw, 'response'))
        .toList();
    expect(responses.map((r) => r.stopReason).toSet().length, 8);
    expect(
      responses.any((r) => r.stopReason == 'incomplete' && r.blocks.isEmpty),
      isTrue,
    );
    expect(responses.every((r) => r.usage.output.known), isTrue);
  });
}

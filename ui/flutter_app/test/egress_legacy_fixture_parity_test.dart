@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'egress_legacy_fixture.dart';

void main() {
  test(
    'portable legacy Direct fixture matches every Go input byte including LF',
    () {
      final original = File(
        '../../internal/environment/testdata/egress_legacy_938_direct.json',
      ).readAsBytesSync();
      expect(original.length, 3013);
      expect(original.last, 10);
      expect(utf8.encode(egressLegacyDirectJSON), original);
    },
  );
}

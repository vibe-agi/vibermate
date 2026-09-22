import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/runtime_storage.dart';

void main() {
  const location = {
    'backend': 'sqlite',
    'dataDirectory': '/srv/vibermate data',
    'databasePath': '/srv/vibermate data/runtime.db',
  };

  test(
    'storage paths belong to the connected Runtime, not a guessed local home',
    () async {
      var calls = 0;
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('https://runtime.example'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'storage-runtime',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        inspectSession: false,
        client: MockClient((request) async {
          calls++;
          expect(request.method, 'GET');
          expect(request.url.path, '/api/v1/storage');
          expect(request.headers['authorization'], 'Bearer ${'R' * 43}');
          return http.Response(
            jsonEncode(location),
            200,
            headers: {'content-type': 'application/json'},
          );
        }),
      );
      addTearDown(api.close);
      final actual = await api.storageLocation();
      expect(actual.dataDirectory, location['dataDirectory']);
      expect(actual.databasePath, location['databasePath']);
      expect(calls, 1);
    },
  );

  test('storage contract rejects unknown or inconsistent locations', () {
    for (final change in [
      {'backend': 'unknown'},
      {'dataDirectory': 'relative'},
      {'dataDirectory': '/'},
      {'dataDirectory': '/srv/../tmp'},
      {'databasePath': '/unrelated/runtime.db'},
      {'accessToken': 'must-not-be-returned'},
    ]) {
      expect(
        () => RuntimeStorageLocation.fromJson({...location, ...change}),
        throwsA(isA<ControlContractException>()),
      );
    }
  });
}

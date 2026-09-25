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
    'collectedAt': '2026-09-26T04:00:00.000Z',
    'databaseBytes': 4096,
    'walBytes': 1024,
    'sharedMemoryBytes': 512,
    'evidenceBytes': 2048,
    'reusableBytes': 256,
    'filesystemAvailableBytes': 8589934592,
    'lowSpaceThresholdBytes': 1073741824,
    'capacityState': 'healthy',
    'cleanupPreview': {
      'exchanges': 1,
      'envelopes': 2,
      'activities': 0,
      'connections': 0,
      'attempts': 0,
      'approvals': 0,
      'assignments': 0,
      'captures': 0,
    },
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
      expect(actual.fileBytes, 5632);
      expect(actual.cleanupPreview.envelopes, 2);
      expect(calls, 1);
    },
  );

  test('storage contract rejects unknown or inconsistent locations', () {
    final windows = RuntimeStorageLocation.fromJson({
      ...location,
      'dataDirectory': r'C:\ViberMate\data',
      'databasePath': r'C:\ViberMate\data\runtime.db',
    });
    expect(windows.dataDirectory, r'C:\ViberMate\data');
    for (final change in [
      {'backend': 'unknown'},
      {'dataDirectory': 'relative'},
      {'dataDirectory': '/'},
      {'dataDirectory': '/srv/../tmp'},
      {'databasePath': '/unrelated/runtime.db'},
      {
        'dataDirectory': r'\\server\share\ViberMate',
        'databasePath': r'\\server\share\ViberMate\runtime.db',
      },
      {'capacityState': 'low'},
      {'lowSpaceThresholdBytes': 0},
      {'filesystemAvailableBytes': null, 'capacityState': 'healthy'},
      {'accessToken': 'must-not-be-returned'},
    ]) {
      expect(
        () => RuntimeStorageLocation.fromJson({...location, ...change}),
        throwsA(isA<ControlContractException>()),
      );
    }
  });

  test('expired evidence cleanup uses one idempotent owner mutation', () async {
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
        expect(request.method, 'POST');
        expect(request.url.path, '/api/v1/storage/actions/cleanup-expired');
        expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
        expect(request.headers['if-match'], '0');
        expect(request.headers['idempotency-key'], isNotEmpty);
        return http.Response(
          jsonEncode({
            'deleted': true,
            'holderCount': 0,
            'holders': <Object?>[],
            'released': {
              'exchanges': 3,
              'envelopes': 12,
              'activities': 0,
              'connections': 0,
              'attempts': 0,
              'approvals': 0,
              'assignments': 0,
              'captures': 0,
            },
          }),
          200,
          headers: {'content-type': 'application/json'},
        );
      }),
    );
    addTearDown(api.close);
    final result = await api.cleanupExpiredEvidence();
    expect(result.released?.exchanges, 3);
    expect(result.released?.envelopes, 12);
  });

  test('archive impact is read only when the user asks to clear', () async {
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
        expect(request.method, 'GET');
        expect(request.url.path, '/api/v1/evidence/actions/clear');
        expect(request.headers['authorization'], 'Bearer ${'R' * 43}');
        return http.Response(
          jsonEncode({
            'exchanges': 8,
            'envelopes': 32,
            'activities': 8,
            'connections': 2,
            'attempts': 8,
            'approvals': 1,
            'assignments': 1,
            'captures': 1,
          }),
          200,
          headers: {'content-type': 'application/json'},
        );
      }),
    );
    addTearDown(api.close);
    final preview = await api.evidenceClearPreview();
    expect(preview.captures, 1);
    expect(preview.envelopes, 32);
  });
}

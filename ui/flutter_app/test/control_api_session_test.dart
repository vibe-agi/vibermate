@TestOn('vm')
library;

import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

final _read = List.filled(43, 'R').join();
final _write = List.filled(43, 'W').join();
final _nextRead = List.filled(43, 'A').join();
final _nextWrite = List.filled(43, 'B').join();

DesktopSession _session() => DesktopSession(
  baseUrl: Uri.parse('http://127.0.0.1:9666'),
  readToken: _read,
  writeToken: _write,
  instanceId: 'instance-test',
  expiresAt: DateTime.now().toUtc().add(const Duration(days: 10)),
);

http.Response _state(Duration remaining) => http.Response(
  jsonEncode({
    'schema': 'vibermate-app-session-state-v1',
    'revision': 1,
    'expiresAt': DateTime.now().toUtc().add(remaining).toIso8601String(),
  }),
  200,
);

http.Response _rotation() => http.Response(
  jsonEncode({
    'schema': 'vibermate-app-session-rotation-v1',
    'revision': 2,
    'readToken': _nextRead,
    'writeToken': _nextWrite,
    'expiresAt': DateTime.now()
        .toUtc()
        .add(const Duration(days: 10))
        .toIso8601String(),
  }),
  200,
);

http.Response _users() => http.Response(
  jsonEncode({'schema': 'vibermate-runtime-user-list-v1', 'items': []}),
  200,
);

void main() {
  test(
    'Desktop session renews while no dashboard or user request is made',
    () async {
      final renewed = Completer<void>();
      final client = MockClient((request) async {
        if (request.url.path.endsWith('/current')) {
          return _state(const Duration(milliseconds: 1300));
        }
        expect(request.url.path, '/api/v1/auth/sessions/refresh');
        expect(request.headers['authorization'], 'Bearer $_write');
        expect(request.headers['if-match'], '1');
        renewed.complete();
        return _rotation();
      });
      final api = await HttpControlApi.connect(_session(), client: client);
      addTearDown(api.close);
      // Hidden windows deliberately make no dashboard requests. Renewal must be
      // owned by the API connection, not workbench visibility.
      await expectLater(
        renewed.future.timeout(const Duration(seconds: 2)),
        completes,
      );
    },
  );

  test(
    'a lost renewal response replays the same command instead of losing the session',
    () async {
      String? committedKey;
      var renewals = 0;
      final client = MockClient((request) async {
        if (request.url.path.endsWith('/current')) {
          return _state(const Duration(milliseconds: 500));
        }
        if (request.url.path.endsWith('/refresh')) {
          renewals += 1;
          final key = request.headers['idempotency-key'];
          expect(request.headers['authorization'], 'Bearer $_write');
          expect(request.headers['if-match'], '1');
          if (committedKey == null) {
            committedKey = key;
            // The server rotated successfully, but its reply did not reach UI.
            throw http.ClientException('synthetic lost renewal response');
          }
          if (key != committedKey) return http.Response('{}', 401);
          return _rotation();
        }
        expect(request.headers['authorization'], 'Bearer $_nextRead');
        return _users();
      });
      var invalidations = 0;
      final api = await HttpControlApi.connect(
        _session(),
        client: client,
        onSessionInvalidated: () => invalidations++,
      );
      addTearDown(api.close);
      await expectLater(
        api.runtimeUsers(),
        throwsA(isA<http.ClientException>()),
      );
      expect(await api.runtimeUsers(), isEmpty);
      expect(renewals, 2);
      expect(invalidations, 0);
    },
  );

  test(
    'an in-flight retired read retries once and does not invalidate the new session',
    () async {
      final oldReadStarted = Completer<void>();
      final oldReadReply = Completer<http.Response>();
      var reads = 0;
      final client = MockClient((request) async {
        if (request.url.path.endsWith('/current')) {
          return _state(const Duration(hours: 1));
        }
        reads += 1;
        if (request.headers['authorization'] == 'Bearer $_read') {
          oldReadStarted.complete();
          return oldReadReply.future;
        }
        expect(request.headers['authorization'], 'Bearer $_nextRead');
        return _users();
      });
      var invalidations = 0;
      final api = await HttpControlApi.connect(
        _session(),
        client: client,
        onSessionInvalidated: () => invalidations++,
      );
      addTearDown(api.close);
      final reading = api.runtimeUsers();
      await oldReadStarted.future;
      api.replaceSession(
        DesktopSession(
          baseUrl: _session().baseUrl,
          readToken: _nextRead,
          writeToken: _nextWrite,
          instanceId: 'instance-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 12)),
        ),
      );
      oldReadReply.complete(http.Response('{}', 401));
      expect(await reading, isEmpty);
      expect(reads, 2);
      expect(invalidations, 0);
    },
  );

  test('a retired write is not replayed when the session changes', () async {
    final writeStarted = Completer<void>();
    final writeReply = Completer<http.Response>();
    var writes = 0;
    final client = MockClient((request) async {
      if (request.url.path.endsWith('/current')) {
        return _state(const Duration(hours: 1));
      }
      expect(request.method, 'POST');
      expect(request.headers['authorization'], 'Bearer $_write');
      writes += 1;
      writeStarted.complete();
      return writeReply.future;
    });
    var invalidations = 0;
    final api = await HttpControlApi.connect(
      _session(),
      client: client,
      onSessionInvalidated: () => invalidations++,
    );
    addTearDown(api.close);
    final writing = api.cleanupExpiredEvidence();
    await writeStarted.future;
    api.replaceSession(
      DesktopSession(
        baseUrl: _session().baseUrl,
        readToken: _nextRead,
        writeToken: _nextWrite,
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(days: 10)),
      ),
    );
    writeReply.complete(http.Response('{}', 401));
    await expectLater(writing, throwsA(isA<ControlProblem>()));
    expect(writes, 1);
    expect(invalidations, 0);
  });

  test('closing the API cancels independent background renewal', () async {
    var renewals = 0;
    final client = MockClient((request) async {
      if (request.url.path.endsWith('/current')) {
        return _state(const Duration(milliseconds: 1300));
      }
      renewals += 1;
      return _rotation();
    });
    final api = await HttpControlApi.connect(_session(), client: client);
    await api.close();
    await Future<void>.delayed(const Duration(milliseconds: 500));
    expect(renewals, 0);
  });
}

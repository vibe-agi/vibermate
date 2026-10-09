@TestOn('browser')
library;

import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:web/web.dart' as web;
import 'package:vibermate_app/core/bootstrap/platform_runtime_web.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/bootstrap/web_session_store.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

import 'runtime_usage_fixture.dart';

const _read = 'RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR';
const _write = 'WWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWW';
const _owner = {'id': 'user.owner', 'username': 'owner', 'role': 'owner'};
const _member = {'id': 'user.member', 'username': 'alice', 'role': 'member'};
const _nextRead = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';
const _nextWrite = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB';
const _key = 'vibermate.web-session.v1';
const _login = RuntimeLoginAttempt.signIn(
  username: 'owner',
  password: 'never-persist-this-password',
);

Map<String, Object?> _session() => {
  'schema': 'vibermate-web-session-v1',
  'instanceId': 'instance-browser-test',
  'apiVersion': 'v1',
  'principal': _owner,
  'readToken': _read,
  'writeToken': _write,
  'expiresAt': DateTime.now()
      .toUtc()
      .add(const Duration(hours: 1))
      .toIso8601String(),
};

String _record(Map<String, Object?> session, {String? origin}) => jsonEncode({
  'schema': 'vibermate-web-session-storage-v1',
  'origin': origin ?? Uri.base.origin,
  'session': session,
});

void _seed(String record) => web.window.sessionStorage.setItem(_key, record);
String? _saved() => web.window.sessionStorage.getItem(_key);

final class _Server {
  Map<String, Object?> loginSession = _session();
  Map<String, Object?> currentPrincipal = Map.of(_owner);
  Map<String, Object?> nextSession = _session()
    ..['readToken'] = _nextRead
    ..['writeToken'] = _nextWrite;
  int currentStatus = 200;
  int apiStatus = 200;
  int passwordStatus = 201;
  String passwordCode = 'web_session_invalid';
  bool networkFailure = false;
  bool logoutFailure = false;
  final requests = <http.Request>[];
  Future<http.Response> Function(http.Request)? intercept;

  Future<T> run<T>(Future<T> Function() body) =>
      http.runWithClient(body, () => MockClient(handle));

  Future<http.Response> handle(http.Request request) async {
    expect(request.url.origin, Uri.base.origin);
    expect(request.followRedirects, isFalse);
    expect(request.headers.containsKey('origin'), isFalse);
    requests.add(request);
    if (intercept != null) return intercept!(request);
    return respond(request);
  }

  http.Response respond(http.Request request) {
    switch (request.url.path) {
      case '/api/v1/server/web-auth':
        return _json({
          'schema': 'vibermate-web-auth-v1',
          'setupRequired': false,
        });
      case '/api/v1/server/web-sessions':
      case '/api/v1/server/web-setup':
      case '/api/v1/server/web-recovery':
        expect(request.method, 'POST');
        return _json(loginSession, 201);
      case '/api/v1/server/web-sessions/current':
        if (request.method == 'DELETE') {
          if (logoutFailure) {
            throw http.ClientException('synthetic logout outage');
          }
          return http.Response('', 204);
        }
        expect(request.method, 'GET');
        if (networkFailure) {
          throw http.ClientException('synthetic restore outage');
        }
        return _json(currentPrincipal, currentStatus);
      case '/api/v1/server/web-account/password':
        expect(request.method, 'PATCH');
        return passwordStatus == 201
            ? _json(nextSession, 201)
            : _json({'code': passwordCode}, passwordStatus);
      case '/api/v1/server/runtime-users':
        return apiStatus == 200
            ? _json({'schema': 'vibermate-runtime-user-list-v1', 'items': []})
            : _json({'code': 'web_session_invalid'}, apiStatus);
      case '/api/v1/server/me/usage':
      case '/api/v1/server/runtime-users/usage':
        return _json(runtimeUsagePayload());
      default:
        fail('unexpected request ${request.method} ${request.url.path}');
    }
  }
}

final class _UnavailableStore implements WebSessionStore {
  @override
  String? read() => throw StateError('storage access denied');
  @override
  void write(String value) => throw StateError('storage access denied');
  @override
  void remove() => throw StateError('storage access denied');
}

final class _WriteFailingStore implements WebSessionStore {
  const _WriteFailingStore();
  @override
  String? read() => const BrowserWebSessionStore().read();
  @override
  void write(String value) => throw StateError('storage quota exceeded');
  @override
  void remove() => const BrowserWebSessionStore().remove();
}

http.Response _json(Object value, [int status = 200]) =>
    http.Response(jsonEncode(value), status);

void main() {
  setUp(() => web.window.sessionStorage.clear());
  tearDown(() => web.window.sessionStorage.clear());

  test('real Web connector restores after close without credentials', () async {
    var logins = 0;
    var verifications = 0;
    final client = MockClient((request) async {
      expect(request.url.origin, Uri.base.origin);
      expect(request.followRedirects, isFalse);
      switch (request.url.path) {
        case '/api/v1/server/web-sessions':
          expect(request.method, 'POST');
          logins++;
          return _json(_session(), 201);
        case '/api/v1/server/web-sessions/current':
          expect(request.method, 'GET');
          expect(request.headers['authorization'], 'Bearer $_read');
          verifications++;
          return _json(_owner);
        case '/api/v1/server/runtime-users':
          expect(request.headers['authorization'], 'Bearer $_read');
          return _json({
            'schema': 'vibermate-runtime-user-list-v1',
            'items': [],
          });
        case '/api/v1/server/web-auth':
          return _json({
            'schema': 'vibermate-web-auth-v1',
            'setupRequired': false,
          });
        default:
          fail('unexpected HTTP request ${request.method} ${request.url.path}');
      }
    });
    await http.runWithClient(() async {
      final first = await connectPlatformRuntime(
        login: const RuntimeLoginAttempt.signIn(
          username: 'owner',
          password: 'never-persist-this-password',
        ),
      );
      await first.close(); // Disposal is not logout.
      final restored = await connectPlatformRuntime();
      addTearDown(restored.close);
      expect(restored.webPrincipal?.id, 'user.owner');
      expect(restored.webPrincipal?.username, 'owner');
      expect(restored.serverManagement, isTrue);
      expect(await restored.api.runtimeUsers(), isEmpty);
      expect(logins, 1);
      expect(verifications, 1);
      final storage = web.window.sessionStorage;
      expect(storage.length, 1);
      expect(
        storage.getItem(storage.key(0)!)!,
        isNot(contains('never-persist')),
      );
    }, () => client);
  });

  for (final principal in [_owner, _member]) {
    test(
      'restore uses server ${principal['role']} identity and API scope',
      () async {
        final server = _Server()..currentPrincipal = Map.of(principal);
        // Deliberately cache the opposite authority to catch trusting stored role.
        _seed(
          _record(
            _session()..['principal'] = principal == _member ? _owner : _member,
          ),
        );
        await server.run(() async {
          final connection = await connectPlatformRuntime();
          addTearDown(connection.close);
          expect(connection.webPrincipal?.id, principal['id']);
          expect(connection.webPrincipal?.username, principal['username']);
          expect(connection.serverManagement, principal == _owner);
          final usage = await connection.api.runtimeUsage(
            const RuntimeUsageQuery(
              from: '2026-07-27',
              until: '2026-08-26',
              timeZone: 'Asia/Singapore',
            ),
          );
          expect(usage.total?.agentApiCalls, 2);
          expect(server.requests.map((r) => r.url.path).toList(), [
            '/api/v1/server/web-sessions/current',
            principal == _owner
                ? '/api/v1/server/runtime-users/usage'
                : '/api/v1/server/me/usage',
          ]);
          expect(
            server.requests.every(
              (r) => r.headers['authorization'] == 'Bearer $_read',
            ),
            isTrue,
          );
        });
      },
    );
  }

  final invalid = <String, String Function()>{
    'corrupt JSON': () => '{broken',
    'foreign origin': () =>
        _record(_session(), origin: 'https://foreign.example.test'),
    'unsupported storage version': () =>
        _record(_session()).replaceFirst('storage-v1', 'storage-v2'),
    'oversized record': () => 'x' * (16 * 1024 + 1),
    'invalid capability': () => _record(_session()..['readToken'] = 'short'),
    'duplicate capabilities': () => _record(_session()..['writeToken'] = _read),
    'invalid timestamp': () => _record(_session()..['expiresAt'] = 'tomorrow'),
    'unsupported session schema': () =>
        _record(_session()..['schema'] = 'other'),
    'unsupported API version': () => _record(_session()..['apiVersion'] = 'v2'),
    'unexpected secret field': () =>
        _record(_session()..['password'] = 'do-not-accept'),
    'invalid cached principal': () => _record(
      _session()..['principal'] = {'id': 'u', 'username': 'a', 'role': 'admin'},
    ),
  };
  for (final entry in invalid.entries) {
    test('${entry.key} is cleared without sending capabilities', () async {
      final server = _Server();
      _seed(entry.value());
      await server.run(() async {
        await expectLater(
          connectPlatformRuntime(),
          throwsA(
            isA<RuntimeLoginRequired>().having(
              (e) => e.reason,
              'reason',
              'credentials_required',
            ),
          ),
        );
        expect(_saved(), isNull);
        expect(server.requests.single.url.path, '/api/v1/server/web-auth');
        expect(
          server.requests.single.headers.containsKey('authorization'),
          isFalse,
        );
      });
    });
  }

  test('expired saved session is cleared before any server request', () async {
    final server = _Server();
    _seed(_record(_session()..['expiresAt'] = '2000-01-01T00:00:00Z'));
    await server.run(() async {
      await expectLater(
        connectPlatformRuntime(),
        throwsA(
          isA<RuntimeLoginRequired>().having(
            (e) => e.reason,
            'reason',
            'session_expired',
          ),
        ),
      );
      expect(_saved(), isNull);
      expect(server.requests, isEmpty);
    });
  });

  test('server 401 clears saved session and returns session_expired', () async {
    final server = _Server()..currentStatus = 401;
    _seed(_record(_session()));
    await server.run(() async {
      await expectLater(
        connectPlatformRuntime(),
        throwsA(
          isA<RuntimeLoginRequired>().having(
            (e) => e.reason,
            'reason',
            'session_expired',
          ),
        ),
      );
      expect(_saved(), isNull);
      expect(
        server.requests.single.url.path,
        '/api/v1/server/web-sessions/current',
      );
    });
  });

  for (final status in [302, 500, 503]) {
    test('restore HTTP $status retains session and permits retry', () async {
      final server = _Server()..currentStatus = status;
      final saved = _record(_session());
      _seed(saved);
      await server.run(() async {
        await expectLater(
          connectPlatformRuntime(),
          throwsA(
            isA<RuntimeConnectionException>().having(
              (e) => e.message,
              'message',
              'web_auth_unavailable',
            ),
          ),
        );
        expect(_saved(), saved);
        server.currentStatus = 200;
        final retried = await connectPlatformRuntime();
        addTearDown(retried.close);
        expect(retried.serverManagement, isTrue);
        expect(server.requests.every((r) => r.method == 'GET'), isTrue);
      });
    });
  }

  test('restore network failure retains session and permits retry', () async {
    final server = _Server()..networkFailure = true;
    final saved = _record(_session());
    _seed(saved);
    await server.run(() async {
      await expectLater(
        connectPlatformRuntime(),
        throwsA(
          isA<RuntimeConnectionException>().having(
            (e) => e.message,
            'message',
            'web_auth_unavailable',
          ),
        ),
      );
      expect(_saved(), saved);
      server.networkFailure = false;
      final retried = await connectPlatformRuntime();
      addTearDown(retried.close);
      expect(retried.webPrincipal?.id, 'user.owner');
    });
  });

  test(
    'malformed server principal fails closed and retains the session',
    () async {
      final server = _Server()
        ..currentPrincipal = {'id': 'u', 'username': 'a', 'role': 'admin'};
      final saved = _record(_session());
      _seed(saved);
      await server.run(() async {
        await expectLater(
          connectPlatformRuntime(),
          throwsA(isA<RuntimeConnectionException>()),
        );
        expect(_saved(), saved);
      });
    },
  );

  for (final outage in [false, true]) {
    test(
      'explicit logout clears session even with network outage=$outage',
      () async {
        final server = _Server()..logoutFailure = outage;
        await server.run(() async {
          final connection = await connectPlatformRuntime(login: _login);
          addTearDown(connection.close);
          expect(_saved(), isNotNull);
          if (outage) {
            await expectLater(
              connection.signOut!(),
              throwsA(isA<http.ClientException>()),
            );
          } else {
            await connection.signOut!();
          }
          expect(_saved(), isNull);
          expect(connection.isClosed(), isTrue);
          expect(
            server.requests.last.headers['authorization'],
            'Bearer $_write',
          );
        });
      },
    );
  }

  test('password rotation replaces persisted and API capabilities', () async {
    final server = _Server();
    await server.run(() async {
      final first = await connectPlatformRuntime(login: _login);
      addTearDown(first.close);
      final before = _saved();
      await first.changePassword!('old-secret', 'new-secret');
      expect(_saved(), isNot(before));
      final record = jsonDecode(_saved()!) as Map;
      expect((record['session'] as Map)['readToken'], _nextRead);
      expect((record['session'] as Map)['writeToken'], _nextWrite);
      expect(_saved(), isNot(contains('secret')));
      await first.api.runtimeUsers();
      expect(
        server.requests.last.headers['authorization'],
        'Bearer $_nextRead',
      );
      await first.close();
      final restored = await connectPlatformRuntime();
      addTearDown(restored.close);
      expect(restored.webPrincipal?.id, 'user.owner');
      expect(
        server.requests.last.headers['authorization'],
        'Bearer $_nextRead',
      );
    });
  });

  test('late password 401 cannot invalidate a rotated session', () async {
    final server = _Server();
    final started = Completer<void>();
    final retiredReply = Completer<http.Response>();
    var passwordRequests = 0;
    server.intercept = (request) async {
      if (request.url.path == '/api/v1/server/web-account/password' &&
          ++passwordRequests == 1) {
        started.complete();
        return retiredReply.future;
      }
      return server.respond(request);
    };
    await server.run(() async {
      final connection = await connectPlatformRuntime(login: _login);
      addTearDown(connection.close);
      var ended = false;
      unawaited(connection.webSessionEnded!.then((_) => ended = true));
      final old = connection.changePassword!('old', 'new');
      final oldFailure = expectLater(
        old,
        throwsA(isA<RuntimeConnectionException>()),
      );
      await started.future;
      await connection.changePassword!('old', 'new');
      final saved = _saved();
      retiredReply.complete(_json({'code': 'web_session_invalid'}, 401));
      await oldFailure;
      expect(_saved(), saved);
      expect(ended, isFalse);
      expect(await connection.api.runtimeUsers(), isEmpty);
      expect(
        server.requests.last.headers['authorization'],
        'Bearer $_nextRead',
      );
    });
  });

  test('expiry during server verification cannot open a workbench', () async {
    final server = _Server();
    final expiry = DateTime.now().toUtc().add(
      const Duration(milliseconds: 250),
    );
    _seed(_record(_session()..['expiresAt'] = expiry.toIso8601String()));
    server.intercept = (request) async {
      if (request.url.path.endsWith('/current')) {
        // Wait for this specific capability's deadline, not a polling loop.
        await Future<void>.delayed(
          expiry.difference(DateTime.now().toUtc()) +
              const Duration(milliseconds: 30),
        );
      }
      return server.respond(request);
    };
    await server.run(() async {
      await expectLater(
        connectPlatformRuntime(),
        throwsA(
          isA<RuntimeLoginRequired>().having(
            (e) => e.reason,
            'reason',
            'session_expired',
          ),
        ),
      );
      expect(_saved(), isNull);
    });
  });

  for (final field in ['instanceId', 'principal']) {
    test(
      'password response with changed $field retains current session',
      () async {
        final server = _Server();
        server.nextSession[field] = field == 'instanceId'
            ? 'other-instance'
            : _member;
        await server.run(() async {
          final connection = await connectPlatformRuntime(login: _login);
          addTearDown(connection.close);
          final saved = _saved();
          await expectLater(
            connection.changePassword!('old', 'new'),
            throwsA(
              anyOf(
                isA<RuntimeConnectionException>(),
                isA<ControlContractException>(),
              ),
            ),
          );
          expect(_saved(), saved);
          expect(await connection.api.runtimeUsers(), isEmpty);
          expect(
            server.requests.last.headers['authorization'],
            'Bearer $_read',
          );
        });
      },
    );
  }

  test(
    'restore response bound fails closed and retains the capability',
    () async {
      final server = _Server();
      final saved = _record(_session());
      _seed(saved);
      server.intercept = (request) async =>
          http.Response('x' * (16 * 1024 + 1), 200);
      await server.run(() async {
        await expectLater(
          connectPlatformRuntime(),
          throwsA(isA<RuntimeConnectionException>()),
        );
        expect(_saved(), saved);
      });
    },
  );

  test('confirmed password session invalidation clears the record', () async {
    final server = _Server()..passwordStatus = 401;
    await server.run(() async {
      final connection = await connectPlatformRuntime(login: _login);
      addTearDown(connection.close);
      await expectLater(
        connection.changePassword!('old', 'new'),
        throwsA(isA<RuntimeLoginRequired>()),
      );
      await connection.webSessionEnded!.timeout(const Duration(seconds: 2));
      expect(_saved(), isNull);
    });
  });

  test('wrong current password retains the live session', () async {
    final server = _Server()
      ..passwordStatus = 401
      ..passwordCode = 'web_access_denied';
    await server.run(() async {
      final connection = await connectPlatformRuntime(login: _login);
      addTearDown(connection.close);
      final saved = _saved();
      await expectLater(
        connection.changePassword!('wrong', 'new'),
        throwsA(
          isA<RuntimeLoginRequired>().having(
            (e) => e.reason,
            'reason',
            'current_password_rejected',
          ),
        ),
      );
      expect(_saved(), saved);
      expect(await connection.api.runtimeUsers(), isEmpty);
    });
  });

  test('authenticated API 401 clears storage and notifies UI', () async {
    final server = _Server()..apiStatus = 401;
    await server.run(() async {
      final connection = await connectPlatformRuntime(login: _login);
      addTearDown(connection.close);
      await expectLater(
        connection.api.runtimeUsers(),
        throwsA(isA<ControlProblem>()),
      );
      await connection.webSessionEnded!.timeout(const Duration(seconds: 2));
      expect(_saved(), isNull);
    });
  });

  test('live expiry clears storage and notifies UI', () async {
    final server = _Server();
    server.loginSession['expiresAt'] = DateTime.now()
        .toUtc()
        .add(const Duration(milliseconds: 250))
        .toIso8601String();
    await server.run(() async {
      final connection = await connectPlatformRuntime(login: _login);
      addTearDown(connection.close);
      await connection.webSessionEnded!.timeout(const Duration(seconds: 2));
      expect(_saved(), isNull);
    });
  });

  test('stale API invalidation cannot clear a newer account login', () async {
    final server = _Server();
    await server.run(() async {
      final old = await connectPlatformRuntime(login: _login);
      addTearDown(old.close);
      server.loginSession = Map.of(server.nextSession)..['principal'] = _member;
      final newer = await connectPlatformRuntime(login: _login);
      addTearDown(newer.close);
      final saved = _saved();
      server.apiStatus = 401;
      await expectLater(old.api.runtimeUsers(), throwsA(isA<ControlProblem>()));
      expect(_saved(), saved);
      expect((jsonDecode(_saved()!)['session'] as Map)['principal'], _member);
    });
  });

  test(
    'storage unavailable still permits in-memory login and API access',
    () async {
      final server = _Server();
      await server.run(() async {
        final connection = await connectPlatformRuntime(
          login: _login,
          sessionStore: _UnavailableStore(),
        );
        addTearDown(connection.close);
        expect(await connection.api.runtimeUsers(), isEmpty);
        expect(_saved(), isNull);
        await expectLater(
          connectPlatformRuntime(sessionStore: _UnavailableStore()),
          throwsA(isA<RuntimeLoginRequired>()),
        );
      });
    },
  );

  for (final mode in [
    RuntimeLoginMode.signIn,
    RuntimeLoginMode.setup,
    RuntimeLoginMode.recover,
  ]) {
    test(
      'successful $mode replaces the old session without persisting input secrets',
      () async {
        final server = _Server();
        server.loginSession['principal'] = _member;
        _seed(_record(_session()));
        final login = switch (mode) {
          RuntimeLoginMode.signIn => _login,
          RuntimeLoginMode.setup => const RuntimeLoginAttempt.setup(
            recoveryKey: 'recovery-secret',
            username: 'owner',
            password: 'password-secret',
          ),
          RuntimeLoginMode.recover => const RuntimeLoginAttempt.recover(
            recoveryKey: 'recovery-secret',
            password: 'password-secret',
          ),
        };
        await server.run(() async {
          final connection = await connectPlatformRuntime(login: login);
          addTearDown(connection.close);
          expect(
            (jsonDecode(_saved()!)['session'] as Map)['principal'],
            _member,
          );
          expect(_saved(), isNot(contains('secret')));
          expect(_saved(), isNot(contains('password')));
          expect(_saved(), isNot(contains('recoveryKey')));
        });
      },
    );
  }

  test('replacement write failure retires old account storage', () async {
    _seed(_record(_session()));
    final server = _Server()..loginSession['principal'] = _member;
    await server.run(() async {
      final connection = await connectPlatformRuntime(
        login: _login,
        sessionStore: const _WriteFailingStore(),
      );
      addTearDown(connection.close);
      expect(connection.webPrincipal?.id, 'user.member');
      expect(await connection.api.runtimeUsers(), isEmpty);
      expect(_saved(), isNull);
    });
  });

  test(
    'password replacement write failure retires the old capability record',
    () async {
      final server = _Server();
      await server.run(() async {
        final normal = await connectPlatformRuntime(login: _login);
        await normal.close();
        final connection = await connectPlatformRuntime(
          sessionStore: const _WriteFailingStore(),
        );
        addTearDown(connection.close);
        await connection.changePassword!('old', 'new');
        expect(_saved(), isNull);
        expect(await connection.api.runtimeUsers(), isEmpty);
        expect(
          server.requests.last.headers['authorization'],
          'Bearer $_nextRead',
        );
      });
    },
  );
}

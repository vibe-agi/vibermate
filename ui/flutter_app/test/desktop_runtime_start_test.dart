@TestOn('vm')
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';
import 'package:vibermate_app/core/bootstrap/transparent_capture.dart';

final class _StartedRuntime {
  const _StartedRuntime(this.preparationSeen);
  final bool preparationSeen;
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  // These are native process/HTTP tests; only the platform channel is mocked.
  HttpOverrides.global = null;
  const captureChannel = MethodChannel(
    'io.vibermate.desktop/transparent-capture',
  );

  test(
    'forwards flow bootstrap only after API connection and clears its generation',
    () async {
      final fixture = await _BootstrapDaemon.create({
        'flowPort': 32123,
        'flowSecret': 'F' * 43,
      });
      addTearDown(fixture.dispose);
      final calls = <MethodCall>[];
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(captureChannel, (call) async {
            expect(fixture.sessionInspected, isTrue);
            calls.add(call);
            return {'state': 'disabled', 'enabled': false};
          });
      addTearDown(
        () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(captureChannel, null),
      );

      final runtime = await fixture.start();
      addTearDown(runtime.close);
      expect(calls.map((call) => call.method), ['configure']);
      expect(calls.single.arguments, {
        'instanceId': 'flow-test',
        'flowPort': 32123,
        'flowSecret': 'F' * 43,
      });
      await runtime.close();
      expect(calls.map((call) => call.method), [
        'configure',
        'clearConfiguration',
      ]);
      expect(calls.last.arguments, {'instanceId': 'flow-test'});
    },
    skip: Platform.isWindows,
  );

  test(
    'missing flow bootstrap and missing plugin preserve ordinary startup',
    () async {
      for (final fields in [
        <String, Object?>{},
        {'flowPort': 32123, 'flowSecret': 'F' * 43},
      ]) {
        final fixture = await _BootstrapDaemon.create(fields);
        try {
          final runtime = await fixture.start();
          expect(fixture.sessionInspected, isTrue);
          await runtime.close();
        } finally {
          await fixture.dispose();
        }
      }
    },
    skip: Platform.isWindows,
  );

  for (final nextHasFlows in [false, true]) {
    test(
      'new generation invalidates old native cleanup: flows=$nextHasFlows',
      () async {
        final first = await _BootstrapDaemon.create({
          'flowPort': 32123,
          'flowSecret': 'F' * 43,
        }, instanceId: 'first-generation');
        final second = await _BootstrapDaemon.create(
          nextHasFlows ? {'flowPort': 32124, 'flowSecret': 'G' * 43} : {},
          instanceId: 'second-generation',
        );
        addTearDown(first.dispose);
        addTearDown(second.dispose);
        final calls = <MethodCall>[];
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(captureChannel, (call) async {
              calls.add(call);
              return {'state': 'disabled', 'enabled': false};
            });
        addTearDown(
          () => TestDefaultBinaryMessengerBinding
              .instance
              .defaultBinaryMessenger
              .setMockMethodCallHandler(captureChannel, null),
        );
        final oldRuntime = await first.start();
        addTearDown(oldRuntime.close);
        final newRuntime = await second.start();
        addTearDown(newRuntime.close);
        await oldRuntime.close();
        expect(calls.map((call) => call.method), [
          'configure',
          nextHasFlows ? 'configure' : 'clearConfiguration',
        ]);
        expect(
          (calls.last.arguments as Map)['instanceId'],
          nextHasFlows ? 'second-generation' : 'first-generation',
        );
        await newRuntime.close();
        if (nextHasFlows) {
          expect(calls.last.method, 'clearConfiguration');
          expect(calls.last.arguments, {'instanceId': 'second-generation'});
        } else {
          expect(calls, hasLength(2));
        }
      },
      skip: Platform.isWindows,
    );
  }

  for (final fields in <Map<String, Object?>>[
    {'flowPort': 32123},
    {'flowSecret': 'F' * 43},
    {'flowPort': 0, 'flowSecret': 'F' * 43},
    {'flowPort': 65536, 'flowSecret': 'F' * 43},
    {'flowPort': 32123.5, 'flowSecret': 'F' * 43},
    {'flowPort': 32123, 'flowSecret': 'invalid'},
    {'flowPort': 32123, 'flowSecret': '${'F' * 42}='},
  ]) {
    test(
      'rejects malformed flow bootstrap: ${fields.keys.join(', ')} ${fields['flowPort']}',
      () async {
        final fixture = await _BootstrapDaemon.create(fields);
        addTearDown(fixture.dispose);
        DesktopRuntime? runtime;
        addTearDown(() async => runtime?.close());
        await expectLater(
          fixture.start().then((value) => runtime = value),
          throwsA(isA<DesktopRuntimeException>()),
        );
        expect(fixture.sessionInspected, isFalse);
      },
      skip: Platform.isWindows,
    );
  }

  test(
    'Host flow startup error reaches Settings without blocking the desktop',
    () async {
      const reason = 'flow registration path is not a socket';
      final fixture = await _BootstrapDaemon.create({
        'transparentCaptureError': reason,
      });
      addTearDown(fixture.dispose);
      final runtime = await fixture.start();
      addTearDown(runtime.close);
      expect(fixture.sessionInspected, isTrue);
      expect(TransparentCaptureController.shared.error, reason);
      expect(TransparentCaptureController.shared.state, 'unavailable');
      await runtime.close();
      expect(TransparentCaptureController.shared.error, isNull);
    },
    skip: Platform.isWindows,
  );

  test(
    'failed API connection never forwards flow bootstrap',
    () async {
      final fixture = await _BootstrapDaemon.create({
        'flowPort': 32123,
        'flowSecret': 'F' * 43,
      }, rejectSession: true);
      addTearDown(fixture.dispose);
      final calls = <MethodCall>[];
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(captureChannel, (call) async {
            calls.add(call);
            return {'state': 'disabled', 'enabled': false};
          });
      addTearDown(
        () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(captureChannel, null),
      );
      await expectLater(fixture.start(), throwsA(anything));
      expect(calls, isEmpty);
    },
    skip: Platform.isWindows,
  );

  for (final remoteAddress in <String?>[null, '127.0.0.1:0']) {
    test(
      'native launch supplies owned loopback SOCKS argv: remote=$remoteAddress',
      () async {
        final root = await Directory.systemTemp.createTemp(
          'vibermate-bootstrap-argv.',
        );
        try {
          final daemon = File('${root.path}/synthetic-daemon');
          await daemon.writeAsString(r'''#!/bin/sh
printf '%s\n' "$@" > "$0.arguments"
printf '%s\n' '{"schema":"vibermate-daemon-progress-v1","phase":"runtime_starting"}'
printf '%s\n' '{"schema":"vibermate-daemon-failure-v1","reason":"runtime_unavailable"}'
while IFS= read -r line; do :; done
: > "$0.closed"
''');
          final mode = await Process.run('/bin/chmod', ['700', daemon.path]);
          expect(mode.exitCode, 0);
          await expectLater(
            DesktopRuntime.start(
              daemonPath: daemon.path,
              cacheDirectory: '${root.path}/cache',
              dataDirectory: '${root.path}/data',
              homeDirectory: root.path,
              remoteServerListenAddress: remoteAddress,
            ),
            throwsA(
              isA<DesktopRuntimeException>().having(
                (error) => error.reason,
                'bootstrap failure reason',
                'runtime_unavailable',
              ),
            ),
          );
          final canonicalRoot = await root.resolveSymbolicLinks();
          final arguments = await File(
            '${daemon.path}.arguments',
          ).readAsLines();
          expect(
            arguments.where(
              (argument) => argument.startsWith('--socks-listen='),
            ),
            ['--socks-listen=127.0.0.1:0'],
          );
          expect(arguments, [
            '--app-cache-dir=$canonicalRoot/cache',
            '--data-dir=$canonicalRoot/data',
            '--webview-origin=vibermate://desktop',
            '--parent-lifetime-fd=0',
            '--bootstrap-fd=1',
            '--socks-listen=127.0.0.1:0',
            if (remoteAddress != null) '--remote-server-listen=$remoteAddress',
          ]);
          expect(await File('${daemon.path}.closed').exists(), isTrue);
        } finally {
          await root.delete(recursive: true);
        }
      },
      skip: Platform.isWindows
          ? 'Native Unix synthetic executable fixture'
          : false,
    );
  }
  test(
    'prelaunch preservation failure never saves or launches previous',
    () async {
      final launches = <String>[];
      final result = startDesktopRuntimeSelection<_StartedRuntime>(
        directory: '/chosen',
        previous: '/previous',
        launch: (directory, notice) async {
          launches.add(directory);
          throw const DesktopRuntimeException(
            'preparation incomplete',
            reason: 'storage_upgrade_incomplete',
            preserveDataDirectory: true,
          );
        },
        saveSelection: (selection) async {
          fail('preservation failure saved another directory');
        },
        close: (runtime) async {
          fail('no returned runtime to close');
        },
        preparationSeen: (runtime) => runtime.preparationSeen,
        hasDatabase: (directory) async {
          fail('preservation failure entered fallback');
        },
      );
      await expectLater(
        result,
        throwsA(
          isA<DesktopRuntimeException>().having(
            (e) => e.preserveDataDirectory,
            'preservation fact',
            isTrue,
          ),
        ),
      );
      expect(launches, ['/chosen']);
    },
  );
  for (final preparation in [false, true]) {
    for (final exitConfirmed in [false, true]) {
      test(
        'started runtime save failure preserves chosen directory: prep=$preparation close=$exitConfirmed',
        () async {
          final launches = <String>[];
          final saves = <String>[];
          var closes = 0;
          var fallbackChecks = 0;
          final result = startDesktopRuntimeSelection<_StartedRuntime>(
            directory: '/chosen',
            previous: '/previous',
            launch: (directory, notice) async {
              launches.add(directory);
              return _StartedRuntime(preparation);
            },
            saveSelection: (selection) async {
              saves.add(selection.directory);
              if (selection.directory == '/chosen') {
                throw StateError('injected save failure');
              }
            },
            close: (runtime) async {
              closes++;
              if (!exitConfirmed) {
                throw TimeoutException('injected unconfirmed exit');
              }
            },
            preparationSeen: (runtime) => runtime.preparationSeen,
            hasDatabase: (directory) async {
              fallbackChecks++;
              return true;
            },
          );
          await expectLater(
            result,
            throwsA(
              isA<DesktopRuntimeException>()
                  .having(
                    (e) => e.preserveDataDirectory,
                    'preserves chosen data directory',
                    isTrue,
                  )
                  .having(
                    (e) => e.reason,
                    'closed reason',
                    preparation
                        ? 'storage_upgrade_incomplete'
                        : exitConfirmed
                        ? 'storage_unavailable'
                        : 'runtime_unavailable',
                  ),
            ),
          );
          expect(launches, ['/chosen']);
          expect(saves, ['/chosen']);
          expect(closes, 1);
          expect(fallbackChecks, 0);
        },
      );
    }
  }
  test(
    'prelaunch uncommitted move retains previous-directory fallback',
    () async {
      final launches = <String>[];
      final saves = <String>[];
      final result = await startDesktopRuntimeSelection<_StartedRuntime>(
        directory: '/chosen',
        previous: '/previous',
        launch: (directory, notice) async {
          launches.add(directory);
          if (directory == '/chosen') {
            throw const DesktopRuntimeException(
              'uncommitted move startup failure',
            );
          }
          expect(notice, 'settings.storage.rolled_back');
          return const _StartedRuntime(false);
        },
        saveSelection: (selection) async {
          saves.add(selection.directory);
        },
        close: (runtime) async {
          fail('no started chosen runtime to close');
        },
        preparationSeen: (runtime) => runtime.preparationSeen,
        hasDatabase: (directory) async => directory == '/previous',
      );
      expect(result.preparationSeen, isFalse);
      expect(launches, ['/chosen', '/previous']);
      expect(saves, ['/previous']);
    },
  );
}

final class _BootstrapDaemon {
  _BootstrapDaemon(this.root, this.server, this.daemon);

  final Directory root;
  final HttpServer server;
  final File daemon;
  bool sessionInspected = false;

  static Future<_BootstrapDaemon> create(
    Map<String, Object?> optionalFields, {
    String instanceId = 'flow-test',
    bool rejectSession = false,
  }) async {
    final root = await Directory.systemTemp.createTemp(
      'vibermate-flow-bootstrap.',
    );
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final daemon = File('${root.path}/synthetic-daemon');
    final fixture = _BootstrapDaemon(root, server, daemon);
    final baseUrl = 'http://127.0.0.1:${server.port}';
    final expiry = DateTime.now()
        .toUtc()
        .add(const Duration(days: 1))
        .toIso8601String();
    server.listen((request) async {
      await request.drain<void>();
      request.response.headers.contentType = ContentType.json;
      request.response.headers.set('Cache-Control', 'no-store');
      if (request.uri.path == '/api/v1/auth/sessions') {
        request.response.statusCode = HttpStatus.created;
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-app-session-v1',
            'baseUrl': baseUrl,
            'instanceId': instanceId,
            'readToken': 'R' * 43,
            'writeToken': 'W' * 43,
            'expiresAt': expiry,
          }),
        );
      } else if (request.uri.path == '/api/v1/auth/sessions/current') {
        fixture.sessionInspected = true;
        if (rejectSession) {
          request.response.statusCode = HttpStatus.serviceUnavailable;
          request.response.write('{}');
        } else {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': expiry,
            }),
          );
        }
      } else {
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });
    final descriptor = jsonEncode({
      'schema': 'vibermate-daemon-bootstrap-v1',
      'instanceId': instanceId,
      'pid': 0,
      'baseUrl': baseUrl,
      'apiVersions': ['v1'],
      'eventVersions': <String>[],
      'bootstrapNonce': 'N' * 43,
      ...optionalFields,
    }).replaceFirst('"pid":0', '"pid":%s');
    await daemon.writeAsString('''#!/bin/sh
printf '%s\\n' '{"schema":"vibermate-daemon-progress-v1","phase":"runtime_starting"}'
printf '${descriptor.replaceAll("'", "'\\''")}\\n' "\$\$"
while IFS= read -r line; do :; done
''');
    final mode = await Process.run('/bin/chmod', ['700', daemon.path]);
    expect(mode.exitCode, 0);
    return fixture;
  }

  Future<DesktopRuntime> start() => DesktopRuntime.start(
    daemonPath: daemon.path,
    cacheDirectory: '${root.path}/cache',
    dataDirectory: '${root.path}/data',
    homeDirectory: root.path,
  );

  Future<void> dispose() async {
    await server.close(force: true);
    await root.delete(recursive: true);
  }
}

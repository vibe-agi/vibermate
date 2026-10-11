import 'dart:async';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';

final class _StartedRuntime {
  const _StartedRuntime(this.preparationSeen);
  final bool preparationSeen;
}

void main() {
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

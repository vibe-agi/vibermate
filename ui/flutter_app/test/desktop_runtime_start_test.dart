import 'dart:async';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';

final class _StartedRuntime {
  const _StartedRuntime(this.preparationSeen);
  final bool preparationSeen;
}

void main() {
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

@TestOn('vm')
library;

import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';
import 'package:vibermate_app/core/bootstrap/desktop_storage.dart';

void main() {
  final daemon = Platform.environment['VIBERMATE_LIVE_TEST_DAEMON'];
  test(
    'real daemon creates and restores a verified credential-free backup',
    () async {
      final temporary = await Directory.systemTemp.createTemp(
        'vibermate-live-backup-',
      );
      final root = await temporary.resolveSymbolicLinks();
      final backup = '$root/backup';
      final restored = '$root/restored';
      DesktopRuntime? runtime;
      Future<DesktopRuntime> start() => DesktopRuntime.start(
        daemonPath: daemon,
        homeDirectory: root,
        remoteServerListenAddress: '127.0.0.1:0',
      );
      try {
        runtime = await start();
        final original = (await runtime.api.storageLocation()).dataDirectory;
        final ca = await runtime.api.rootCA();
        await runtime.prepareStorageBackup(backup);
        await runtime.backupStorage(backup);
        expect(await File('$original/runtime.db').exists(), isTrue);
        expect(await File('$backup/backup-manifest.json').exists(), isTrue);

        runtime = await start();
        final selection = (backup: backup, target: restored);
        await runtime.prepareStorageRestore(selection);
        await runtime.restoreStorage(selection);
        runtime = await start();
        expect((await runtime.api.storageLocation()).dataDirectory, restored);
        expect((await runtime.api.rootCA()).fingerprint, ca.fingerprint);
        expect((await DesktopStorage(original).read()).previous, isNull);
      } finally {
        await runtime?.close();
        await temporary.delete(recursive: true);
      }
    },
    skip: daemon == null,
    timeout: const Timeout(Duration(minutes: 2)),
  );

  test(
    'real daemon relocates data, retains CA, restarts, and fails closed on a missing volume',
    () async {
      final temporary = await Directory.systemTemp.createTemp(
        'vibermate-live-move-',
      );
      final root = await temporary.resolveSymbolicLinks();
      final target = '$root/destination';
      DesktopRuntime? runtime;
      Future<DesktopRuntime> start() => DesktopRuntime.start(
        daemonPath: daemon,
        homeDirectory: root,
        remoteServerListenAddress: '127.0.0.1:0',
      );
      try {
        runtime = await start();
        final original = (await runtime.api.storageLocation()).dataDirectory;
        final before = await runtime.api.loadDashboard();
        final ca = await runtime.api.rootCA();
        final manual = await runtime.api.createManualCapture(
          context: await runtime.api.manualCaptureContext('system_transparent'),
          displayName: 'Synthetic migration guard',
          clientClass: 'desktop_app',
          lifetime: 'until_revoked',
        );
        await expectLater(
          runtime.prepareStorageMove(target),
          throwsA(
            isA<DesktopStorageFailure>().having(
              (e) => e.code,
              'code',
              'storage_in_use',
            ),
          ),
        );
        expect(runtime.isClosed, isFalse);
        expect(
          (await runtime.api.loadDashboard()).status.offlineHold.state,
          'online',
        );
        await runtime.api.revokeManualCapture(
          manualCaptureId: manual.grant.capture.id,
          stateTag: manual.stateTag,
        );
        await runtime.prepareStorageMove(target);
        await runtime.moveStorage(target);
        expect(await File('$original/runtime.db').exists(), isTrue);
        final originalBytes = await File('$original/runtime.db').readAsBytes();
        runtime = await start();
        expect((await runtime.api.storageLocation()).dataDirectory, target);
        expect(
          (await runtime.api.loadDashboard()).environments.map((e) => e.id),
          before.environments.map((e) => e.id),
        );
        expect((await runtime.api.rootCA()).fingerprint, ca.fingerprint);
        expect((await DesktopStorage(original).read()).previous, isNull);
        // Concurrent close callers must BOTH await process exit before copying.
        await Future.wait([runtime.close(), runtime.close()]);
        expect(await File('$original/runtime.db').readAsBytes(), originalBytes);
        final missing = '$root/disconnected';
        await Directory(target).rename(missing);
        await expectLater(
          start(),
          throwsA(
            isA<DesktopStorageFailure>().having(
              (e) => e.code,
              'code',
              'storage_location_unavailable',
            ),
          ),
        );
        expect(await Directory(target).exists(), isFalse);
        // A pending switch is different: source is retained for automatic rollback.
        await DesktopStorage(
          original,
        ).save(DesktopStorageSelection(target, previous: original));
        runtime = await start();
        expect((await runtime.api.storageLocation()).dataDirectory, original);
        expect(runtime.storageNotice, 'settings.storage.rolled_back');
        await runtime.close();
        final damaged = Directory('$root/damaged');
        await damaged.create();
        await File(
          '${damaged.path}/runtime.db',
        ).writeAsString('not a database');
        await DesktopStorage(
          original,
        ).save(DesktopStorageSelection(damaged.path, previous: original));
        runtime = await start();
        expect((await runtime.api.storageLocation()).dataDirectory, original);
        expect((await runtime.api.rootCA()).fingerprint, ca.fingerprint);
        expect(runtime.storageNotice, 'settings.storage.rolled_back');
      } finally {
        await runtime?.close();
        await temporary.delete(recursive: true);
      }
    },
    skip: daemon == null,
    timeout: const Timeout(Duration(minutes: 2)),
  );
}

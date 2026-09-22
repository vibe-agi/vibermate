@TestOn('vm')
library;

import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/desktop_storage.dart';

void main() {
  late Directory root;
  late DesktopStorage storage;
  setUp(() async {
    root = Directory(
      await (await Directory.systemTemp.createTemp(
        'vibermate-storage-test-',
      )).resolveSymbolicLinks(),
    );
    storage = DesktopStorage('${root.path}/default');
  });
  tearDown(() => root.delete(recursive: true));

  test(
    'pending switch survives restart and keeps source until committed',
    () async {
      expect((await storage.read()).directory, storage.defaultDirectory);
      final target = '${root.path}/custom';
      await storage.save(
        DesktopStorageSelection(target, previous: storage.defaultDirectory),
      );
      var selection = await DesktopStorage(storage.defaultDirectory).read();
      expect(selection.directory, target);
      expect(selection.previous, storage.defaultDirectory);
      await storage.save(DesktopStorageSelection(target));
      selection = await storage.read();
      expect(selection.directory, target);
      expect(selection.previous, isNull);
      expect(storage.settings.path.startsWith('$target/'), isFalse);
    },
  );

  test(
    'malformed settings fail closed instead of opening an empty database',
    () async {
      await storage.settings.writeAsString('{broken');
      await expectLater(storage.read(), throwsA(isA<DesktopStorageFailure>()));
    },
  );

  test('rejects existing, nested and symlink destinations', () async {
    final source = Directory(storage.defaultDirectory);
    await source.create();
    await DesktopStorage.validateTarget(source.path, '${root.path}/new');
    for (final invalid in [
      source.path,
      '${source.path}/inside',
      root.path,
      '${root.path}/../escape',
    ]) {
      await expectLater(
        DesktopStorage.validateTarget(source.path, invalid),
        throwsA(isA<DesktopStorageFailure>()),
      );
    }
    await Link('${root.path}/link').create(source.path);
    await expectLater(
      DesktopStorage.validateTarget(source.path, '${root.path}/link/new'),
      throwsA(isA<DesktopStorageFailure>()),
    );
  });

  test('a failed selection write preserves the previous selection', () async {
    await storage.save(DesktopStorageSelection(storage.defaultDirectory));
    await expectLater(
      storage.save(const DesktopStorageSelection('/')),
      throwsA(isA<DesktopStorageFailure>()),
    );
    expect((await storage.read()).directory, storage.defaultDirectory);
  });
}

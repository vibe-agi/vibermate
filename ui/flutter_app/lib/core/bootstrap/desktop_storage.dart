import 'dart:convert';
import 'dart:io';

/// The selection lives OUTSIDE the movable directory. A pending selection
/// retains its source until the new runtime has successfully bootstrapped.
final class DesktopStorageSelection {
  const DesktopStorageSelection(this.directory, {this.previous});
  final String directory;
  final String? previous;
}

final class DesktopStorageFailure implements Exception {
  const DesktopStorageFailure(this.code);
  final String code;
  @override
  String toString() => code;
}

final class DesktopStorage {
  DesktopStorage(this.defaultDirectory)
    : settings = File('$defaultDirectory.storage.json');

  final String defaultDirectory;
  final File settings;

  Future<DesktopStorageSelection> read() async {
    if (!await settings.exists()) {
      return DesktopStorageSelection(defaultDirectory);
    }
    try {
      final value = jsonDecode(await settings.readAsString());
      if (value is! Map<String, dynamic> ||
          value['schema'] != 'vibermate-storage-v1' ||
          value.keys.any(
            (key) => !{'schema', 'directory', 'previous'}.contains(key),
          )) {
        throw const FormatException();
      }
      final directory = value['directory'];
      final previous = value['previous'];
      if (!_validPath(directory) ||
          (previous != null &&
              (!_validPath(previous) || previous == directory))) {
        throw const FormatException();
      }
      return DesktopStorageSelection(
        directory as String,
        previous: previous as String?,
      );
    } on Object {
      throw const DesktopStorageFailure('storage_settings_invalid');
    }
  }

  Future<void> save(DesktopStorageSelection selection) async {
    if (!_validPath(selection.directory) ||
        (selection.previous != null && !_validPath(selection.previous))) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
    await settings.parent.create(recursive: true);
    // Rename in the same directory is atomic; an interrupted write cannot
    // replace a good selection with half a JSON document.
    final staging = await settings.parent.createTemp('.vibermate-storage-');
    final file = File('${staging.path}/selection.json');
    try {
      await file.writeAsString(
        jsonEncode({
          'schema': 'vibermate-storage-v1',
          'directory': selection.directory,
          if (selection.previous != null) 'previous': selection.previous,
        }),
        flush: true,
      );
      await file.rename(settings.path);
    } finally {
      await staging.delete(recursive: true);
    }
  }

  static bool _validPath(Object? value) =>
      value is String &&
      value.startsWith('/') &&
      value.length > 1 &&
      !value.endsWith('/') &&
      !value.contains('//') &&
      !value.split('/').any((part) => part == '.' || part == '..');

  static Future<void> validateTarget(String source, String target) async {
    if (!_validPath(target) ||
        target == source ||
        target.startsWith('$source/') ||
        source.startsWith('$target/') ||
        await FileSystemEntity.type(target, followLinks: false) !=
            FileSystemEntityType.notFound) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
    final parent = Directory(target).parent;
    if (!await parent.exists() ||
        await parent.resolveSymbolicLinks() != parent.path) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
  }
}

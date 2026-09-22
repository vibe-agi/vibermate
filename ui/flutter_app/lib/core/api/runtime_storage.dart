import 'control_models.dart';

/// Resolved paths on the connected Runtime's machine, never on the browser.
final class RuntimeStorageLocation {
  const RuntimeStorageLocation({
    required this.dataDirectory,
    required this.databasePath,
  });

  factory RuntimeStorageLocation.fromJson(Object? json) {
    const path = 'storage';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'backend', 'dataDirectory', 'databasePath'},
    );
    final directory = requireString(value, 'dataDirectory', path);
    final database = requireString(value, 'databasePath', path);
    if (requireString(value, 'backend', path) != 'sqlite' ||
        !directory.startsWith('/') ||
        directory == '/' ||
        directory
            .substring(1)
            .split('/')
            .any((part) => part.isEmpty || part == '.' || part == '..') ||
        database != '$directory/runtime.db') {
      throw const ControlContractException('storage location is invalid');
    }
    return RuntimeStorageLocation(
      dataDirectory: directory,
      databasePath: database,
    );
  }

  final String dataDirectory;
  final String databasePath;
}

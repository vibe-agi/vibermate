import 'control_models.dart';

/// Resolved paths on the connected Runtime's machine, never on the browser.
final class RuntimeStorageLocation {
  const RuntimeStorageLocation({
    required this.dataDirectory,
    required this.databasePath,
    required this.collectedAt,
    required this.databaseBytes,
    required this.walBytes,
    required this.sharedMemoryBytes,
    required this.evidenceBytes,
    required this.reusableBytes,
    required this.filesystemAvailableBytes,
    required this.lowSpaceThresholdBytes,
    required this.capacityState,
    required this.cleanupPreview,
  });

  factory RuntimeStorageLocation.fromJson(Object? json) {
    const path = 'storage';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'backend',
        'dataDirectory',
        'databasePath',
        'collectedAt',
        'databaseBytes',
        'walBytes',
        'sharedMemoryBytes',
        'evidenceBytes',
        'reusableBytes',
        'filesystemAvailableBytes',
        'lowSpaceThresholdBytes',
        'capacityState',
        'cleanupPreview',
      },
    );
    final directory = requireString(value, 'dataDirectory', path);
    final database = requireString(value, 'databasePath', path);
    int bytes(String key) {
      final result = requireInteger(value, key, path);
      if (result > 9007199254740991) {
        throw ControlContractException('$path.$key exceeds the safe range');
      }
      return result;
    }

    final availableValue = value['filesystemAvailableBytes'];
    final available = availableValue == null
        ? null
        : bytes('filesystemAvailableBytes');
    final threshold = bytes('lowSpaceThresholdBytes');
    final capacityState = requireString(value, 'capacityState', path);
    if (requireString(value, 'backend', path) != 'sqlite' ||
        !_validStoragePaths(directory, database) ||
        !const {'healthy', 'low', 'unavailable'}.contains(capacityState) ||
        threshold == 0 ||
        (capacityState == 'unavailable') != (available == null) ||
        (capacityState == 'low' &&
            (available == null || available >= threshold)) ||
        (capacityState == 'healthy' &&
            (available == null || available < threshold))) {
      throw const ControlContractException('storage location is invalid');
    }
    return RuntimeStorageLocation(
      dataDirectory: directory,
      databasePath: database,
      collectedAt: requireTimestamp(value, 'collectedAt', path),
      databaseBytes: bytes('databaseBytes'),
      walBytes: bytes('walBytes'),
      sharedMemoryBytes: bytes('sharedMemoryBytes'),
      evidenceBytes: bytes('evidenceBytes'),
      reusableBytes: bytes('reusableBytes'),
      filesystemAvailableBytes: available,
      lowSpaceThresholdBytes: threshold,
      capacityState: capacityState,
      cleanupPreview: DeletionReleased.fromJson(
        value['cleanupPreview'],
        '$path.cleanupPreview',
      ),
    );
  }

  final String dataDirectory;
  final String databasePath;
  final DateTime collectedAt;
  final int databaseBytes;
  final int walBytes;
  final int sharedMemoryBytes;
  final int evidenceBytes;
  final int reusableBytes;
  final int? filesystemAvailableBytes;
  final int lowSpaceThresholdBytes;
  final String capacityState;
  final DeletionReleased cleanupPreview;

  int get fileBytes => databaseBytes + walBytes + sharedMemoryBytes;
}

bool _validStoragePaths(String directory, String database) {
  final windows = RegExp(r'^[A-Za-z]:\\').hasMatch(directory);
  final separator = windows ? '\\' : '/';
  final prefixLength = windows ? 3 : 1;
  if (!windows && !directory.startsWith('/') ||
      directory.length <= prefixLength ||
      directory.contains(windows ? '/' : '\\')) {
    return false;
  }
  final parts = directory.substring(prefixLength).split(separator);
  return parts.every(
        (part) => part.isNotEmpty && part != '.' && part != '..',
      ) &&
      database == '$directory${separator}runtime.db';
}

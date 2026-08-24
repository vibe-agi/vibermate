enum TerminalCommandState {
  notInstalled('not_installed'),
  current('current'),
  sourceUpdated('source_updated'),
  sourceMissing('source_missing'),
  targetMissing('target_missing'),
  unownedTarget('unowned_target'),
  conflict('conflict');

  const TerminalCommandState(this.wireName);

  final String wireName;

  static TerminalCommandState? fromWire(Object? value) {
    for (final state in values) {
      if (state.wireName == value) return state;
    }
    return null;
  }
}

enum TerminalCommandOperation {
  install('install'),
  refresh('refresh'),
  repair('repair'),
  remove('remove');

  const TerminalCommandOperation(this.wireName);

  final String wireName;
}

enum TerminalCommandFailure {
  unavailable('terminal.error.unavailable'),
  timeout('terminal.error.timeout'),
  failed('terminal.error.failed'),
  contract('terminal.error.contract');

  const TerminalCommandFailure(this.copyKey);

  final String copyKey;
}

final class TerminalCommandException implements Exception {
  const TerminalCommandException(this.failure);

  final TerminalCommandFailure failure;

  @override
  String toString() => failure.copyKey;
}

final class TerminalCommandStatus {
  const TerminalCommandStatus({
    required this.state,
    required this.sourcePath,
    required this.targetPath,
    this.detail,
  });

  factory TerminalCommandStatus.fromJson(
    Object? value, {
    String? expectedSourcePath,
  }) {
    if (value is! Map) {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    final payload = <String, Object?>{};
    for (final entry in value.entries) {
      if (entry.key is! String) {
        throw const TerminalCommandException(TerminalCommandFailure.contract);
      }
      payload[entry.key as String] = entry.value;
    }
    const required = {'schema', 'state', 'sourcePath', 'targetPath'};
    const allowed = {...required, 'detail'};
    if (!payload.keys.toSet().containsAll(required) ||
        payload.keys.any((key) => !allowed.contains(key)) ||
        payload['schema'] != 'vibermate-terminal-command/v1') {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    final state = TerminalCommandState.fromWire(payload['state']);
    final sourcePath = payload['sourcePath'];
    final targetPath = payload['targetPath'];
    final detail = payload['detail'];
    if (state == null ||
        sourcePath is! String ||
        targetPath is! String ||
        !_validAbsolutePath(sourcePath) ||
        !_validAbsolutePath(targetPath) ||
        sourcePath == targetPath ||
        _basename(sourcePath) != 'vibermate' ||
        _basename(targetPath) != 'vibermate' ||
        (expectedSourcePath != null && sourcePath != expectedSourcePath) ||
        (detail != null && !_validDetail(detail))) {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    return TerminalCommandStatus(
      state: state,
      sourcePath: sourcePath,
      targetPath: targetPath,
      detail: detail as String?,
    );
  }

  final TerminalCommandState state;
  final String sourcePath;
  final String targetPath;
  final String? detail;

  bool get canInstall => state == TerminalCommandState.notInstalled;
  bool get canRefresh => state == TerminalCommandState.sourceUpdated;
  bool get canRepair => state == TerminalCommandState.targetMissing;

  bool get canRemove => switch (state) {
    TerminalCommandState.current ||
    TerminalCommandState.sourceUpdated ||
    TerminalCommandState.sourceMissing ||
    TerminalCommandState.targetMissing => true,
    _ => false,
  };
}

abstract interface class TerminalCommandService {
  Future<TerminalCommandStatus> inspect();

  Future<TerminalCommandStatus> execute(TerminalCommandOperation operation);
}

bool _validAbsolutePath(String value) {
  if (!value.startsWith('/') ||
      value.length > 4096 ||
      value.trim() != value ||
      value.contains(RegExp(r'[\x00\r\n]'))) {
    return false;
  }
  final parts = value.split('/').skip(1);
  return parts.isNotEmpty &&
      parts.every((part) => part.isNotEmpty && part != '.' && part != '..');
}

bool _validDetail(Object value) =>
    value is String &&
    value.isNotEmpty &&
    value.length <= 4096 &&
    value.trim() == value &&
    !value.contains(RegExp(r'[\x00\r\n]'));

String _basename(String path) => path.substring(path.lastIndexOf('/') + 1);

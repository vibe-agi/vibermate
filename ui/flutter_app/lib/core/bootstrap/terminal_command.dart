import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

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

final class PackagedTerminalCommandService implements TerminalCommandService {
  PackagedTerminalCommandService({String? commandPath})
    : _configuredPath = commandPath;

  static const _maximumOutputBytes = 16 * 1024;
  static const _operationTimeout = Duration(seconds: 15);

  final String? _configuredPath;
  String? _resolvedPath;

  @override
  Future<TerminalCommandStatus> inspect() => _run('status');

  @override
  Future<TerminalCommandStatus> execute(TerminalCommandOperation operation) =>
      _run(operation.wireName);

  Future<TerminalCommandStatus> _run(String operation) async {
    final executable = await _resolveCommandPath();
    Process process;
    try {
      process = await Process.start(
        executable,
        ['terminal-command', operation, '--json'],
        mode: ProcessStartMode.normal,
        runInShell: false,
      );
    } on Object {
      throw const TerminalCommandException(TerminalCommandFailure.unavailable);
    }
    await process.stdin.close();
    final stdout = _collectBounded(process.stdout);
    final stderr = _collectBounded(process.stderr, allowEmpty: true);
    final exitCode = process.exitCode;
    int result;
    try {
      result = await exitCode.timeout(_operationTimeout);
    } on TimeoutException {
      process.kill(ProcessSignal.sigterm);
      try {
        await exitCode.timeout(const Duration(milliseconds: 250));
      } on TimeoutException {
        process.kill(ProcessSignal.sigkill);
        await exitCode;
      }
      await _ignoreFailure(stdout);
      await _ignoreFailure(stderr);
      throw const TerminalCommandException(TerminalCommandFailure.timeout);
    }
    Uint8List output;
    try {
      output = await stdout;
      await stderr;
    } on TerminalCommandException {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    if (result != 0) {
      throw const TerminalCommandException(TerminalCommandFailure.failed);
    }
    try {
      final decoded = const Utf8Decoder(allowMalformed: false).convert(output);
      return TerminalCommandStatus.fromJson(
        jsonDecode(decoded),
        expectedSourcePath: executable,
      );
    } on TerminalCommandException {
      rethrow;
    } on Object {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
  }

  Future<String> _resolveCommandPath() async {
    final resolved = _resolvedPath;
    if (resolved != null) return resolved;
    if (!Platform.isMacOS) {
      throw const TerminalCommandException(TerminalCommandFailure.unavailable);
    }
    final configured = _configuredPath;
    final candidate = configured == null || configured.isEmpty
        ? File(
            Platform.resolvedExecutable,
          ).parent.uri.resolve('vibermate').toFilePath()
        : configured;
    final file = File(candidate);
    if (!file.isAbsolute) {
      throw const TerminalCommandException(TerminalCommandFailure.unavailable);
    }
    try {
      final canonical = await file.resolveSymbolicLinks();
      final metadata = await File(canonical).stat();
      if (metadata.type != FileSystemEntityType.file ||
          (metadata.mode & 0x49) == 0 ||
          _basename(canonical) != 'vibermate') {
        throw const TerminalCommandException(
          TerminalCommandFailure.unavailable,
        );
      }
      _resolvedPath = canonical;
      return canonical;
    } on TerminalCommandException {
      rethrow;
    } on Object {
      throw const TerminalCommandException(TerminalCommandFailure.unavailable);
    }
  }

  static Future<Uint8List> _collectBounded(
    Stream<List<int>> stream, {
    bool allowEmpty = false,
  }) async {
    final bytes = BytesBuilder(copy: false);
    var length = 0;
    var exceeded = false;
    await for (final chunk in stream) {
      length += chunk.length;
      if (length <= _maximumOutputBytes) {
        bytes.add(chunk);
      } else {
        exceeded = true;
      }
    }
    if (exceeded || (!allowEmpty && length == 0)) {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    return bytes.takeBytes();
  }

  static Future<void> _ignoreFailure(Future<Object?> future) async {
    try {
      await future;
    } on Object {
      // Process output is never surfaced from a failed or timed-out operation.
    }
  }
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

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'terminal_command_contract.dart';

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

String _basename(String path) => path.substring(path.lastIndexOf('/') + 1);

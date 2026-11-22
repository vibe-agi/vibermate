import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'terminal_command_contract.dart';

final class PackagedTerminalCommandService implements TerminalCommandService {
  PackagedTerminalCommandService({String? commandPath, this.shellProbe})
    : _configuredPath = commandPath;

  /// Asks the login shell where `vibermate` is. Without one, which is how
  /// tests build this service, the answer is always unknown.
  final LoginShellProbe? shellProbe;

  static const _maximumOutputBytes = 16 * 1024;
  static const _operationTimeout = Duration(seconds: 15);

  final String? _configuredPath;
  String? _resolvedPath;

  @override
  Future<TerminalCommandStatus> inspect() => _run('status');

  @override
  Future<TerminalCommandStatus> execute(TerminalCommandOperation operation) =>
      _run(operation.wireName);

  @override
  Future<ShellCommandCheck> checkShell(TerminalCommandStatus status) async {
    final probe = shellProbe;
    if (probe == null || !status.ready) return ShellCommandCheck.unknown;
    return probe.check(status.targetPath);
  }

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

/// Asks the person's login shell, started the way a new Terminal window
/// starts it, where `vibermate` is: `$SHELL -lic 'command -v vibermate'`.
///
/// Starting that shell runs their profile, so the check has stdin closed,
/// TERM=dumb and a time limit, reads only the last line of what the shell
/// printed, and never shows any of it. The App still never edits a profile.
final class LoginShellProbe {
  const LoginShellProbe({
    this.timeout = const Duration(seconds: 3),
    this.environment,
  });

  final Duration timeout;

  /// The App's environment; [Platform.environment] when null.
  final Map<String, String>? environment;

  Future<ShellCommandCheck> check(String targetPath) async {
    final shell = (environment ?? Platform.environment)['SHELL'];
    if (shell == null || !shell.startsWith('/')) {
      return ShellCommandCheck.unknown;
    }
    final name = _basename(shell);
    Process process;
    try {
      process = await Process.start(
        shell,
        ['-lic', 'command -v vibermate'],
        environment: const {'TERM': 'dumb'},
        mode: ProcessStartMode.normal,
        runInShell: false,
      );
    } on Object {
      return ShellCommandCheck(ShellCommandLookup.unknown, shell: name);
    }
    try {
      await process.stdin.close();
    } on Object {
      // A shell that already exited has nothing to read from stdin anyway.
    }
    final output = PackagedTerminalCommandService._collectBounded(
      process.stdout,
      allowEmpty: true,
    );
    final ignored = process.stderr.drain<void>();
    int exitCode;
    try {
      exitCode = await process.exitCode.timeout(timeout);
    } on TimeoutException {
      process.kill(ProcessSignal.sigkill);
      await PackagedTerminalCommandService._ignoreFailure(output);
      await PackagedTerminalCommandService._ignoreFailure(ignored);
      return ShellCommandCheck(ShellCommandLookup.unknown, shell: name);
    }
    String last;
    try {
      final text = const Utf8Decoder(
        allowMalformed: true,
      ).convert(await output);
      await PackagedTerminalCommandService._ignoreFailure(ignored);
      last = text
          .split('\n')
          .map((line) => line.trim())
          .lastWhere((line) => line.isNotEmpty, orElse: () => '');
    } on Object {
      return ShellCommandCheck(ShellCommandLookup.unknown, shell: name);
    }
    // `command -v` fails when nothing on PATH is called vibermate.
    if (exitCode != 0) {
      return ShellCommandCheck(ShellCommandLookup.notFound, shell: name);
    }
    if (!last.startsWith('/')) {
      return ShellCommandCheck(ShellCommandLookup.unknown, shell: name);
    }
    if (last == targetPath || await _sameFile(last, targetPath)) {
      return ShellCommandCheck(ShellCommandLookup.found, shell: name);
    }
    // Another vibermate comes first. It may be an older copy, so the App
    // does not claim the shell runs this one.
    return ShellCommandCheck(ShellCommandLookup.unknown, shell: name);
  }

  static Future<bool> _sameFile(String found, String target) async {
    try {
      final resolved = await Future.wait([
        File(found).resolveSymbolicLinks(),
        File(target).resolveSymbolicLinks(),
      ]);
      return resolved[0] == resolved[1];
    } on Object {
      return false;
    }
  }
}

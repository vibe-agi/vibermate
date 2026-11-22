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

/// Whether a new Terminal window finds the `vibermate` command: what the
/// person's login shell answers to `command -v vibermate`.
enum ShellCommandLookup {
  /// The shell finds this App's command.
  found,

  /// The shell finds no `vibermate` at all.
  notFound,

  /// The check could not run, timed out, or found another `vibermate`.
  unknown,
}

/// What the login shell answered, and which shell it was.
final class ShellCommandCheck {
  const ShellCommandCheck(this.lookup, {this.shell});

  static const unknown = ShellCommandCheck(ShellCommandLookup.unknown);

  final ShellCommandLookup lookup;

  /// The shell's name, such as 'zsh', 'bash' or 'fish', when known.
  final String? shell;
}

final class TerminalCommandStatus {
  const TerminalCommandStatus({
    required this.state,
    required this.sourcePath,
    required this.targetPath,
    this.sourceBuild = 'development',
    this.installedBuild,
    this.detail,
    this.shellFindsCommand,
    this.loginShell,
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
    const required = {
      'schema',
      'state',
      'sourcePath',
      'targetPath',
      'sourceBuild',
    };
    const allowed = {...required, 'installedBuild', 'detail'};
    if (!payload.keys.toSet().containsAll(required) ||
        payload.keys.any((key) => !allowed.contains(key)) ||
        payload['schema'] != 'vibermate-terminal-command/v1') {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    final state = TerminalCommandState.fromWire(payload['state']);
    final sourcePath = payload['sourcePath'];
    final targetPath = payload['targetPath'];
    final detail = payload['detail'];
    final sourceBuild = payload['sourceBuild'];
    final installedBuild = payload['installedBuild'];
    if (state == null ||
        sourcePath is! String ||
        targetPath is! String ||
        !_validAbsolutePath(sourcePath) ||
        !_validAbsolutePath(targetPath) ||
        sourcePath == targetPath ||
        _basename(sourcePath) != 'vibermate' ||
        _basename(targetPath) != 'vibermate' ||
        (expectedSourcePath != null && sourcePath != expectedSourcePath) ||
        sourceBuild is! String ||
        !_validBuild(sourceBuild) ||
        (installedBuild != null &&
            (installedBuild is! String || !_validBuild(installedBuild))) ||
        (detail != null && !_validDetail(detail))) {
      throw const TerminalCommandException(TerminalCommandFailure.contract);
    }
    return TerminalCommandStatus(
      state: state,
      sourcePath: sourcePath,
      targetPath: targetPath,
      sourceBuild: sourceBuild,
      installedBuild: installedBuild as String?,
      detail: detail as String?,
    );
  }

  final TerminalCommandState state;
  final String sourcePath;
  final String targetPath;
  final String sourceBuild;
  final String? installedBuild;
  final String? detail;

  /// Whether a new Terminal window finds the command; null until the App has
  /// asked. The App asks the login shell itself: the packaged command's
  /// status never carries this, so its wire contract is unchanged.
  final ShellCommandLookup? shellFindsCommand;

  /// The login shell that answered [shellFindsCommand], when known.
  final String? loginShell;

  /// Installed and usable, whether or not the shell finds it yet.
  bool get ready =>
      state == TerminalCommandState.current ||
      state == TerminalCommandState.sourceUpdated;

  /// This status with what the login shell answered.
  TerminalCommandStatus withShellCheck(ShellCommandCheck check) =>
      TerminalCommandStatus(
        state: state,
        sourcePath: sourcePath,
        targetPath: targetPath,
        sourceBuild: sourceBuild,
        installedBuild: installedBuild,
        detail: detail,
        shellFindsCommand: check.lookup,
        loginShell: check.shell,
      );

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

  /// Asks the login shell whether a new Terminal window finds the command
  /// that [status] installed. Never throws; a check that cannot run is
  /// [ShellCommandCheck.unknown].
  Future<ShellCommandCheck> checkShell(TerminalCommandStatus status);
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

bool _validBuild(String value) =>
    value.isNotEmpty &&
    value.length <= 128 &&
    value.trim() == value &&
    !value.contains(RegExp(r'[\x00-\x1f\x7f]'));

String _basename(String path) => path.substring(path.lastIndexOf('/') + 1);

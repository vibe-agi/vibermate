import '../core/bootstrap/terminal_command.dart';

final class PreviewTerminalCommandService implements TerminalCommandService {
  PreviewTerminalCommandService({
    TerminalCommandStatus? initial,
    this.shellLookup = ShellCommandLookup.found,
  }) : _status =
           initial ??
           const TerminalCommandStatus(
             state: TerminalCommandState.notInstalled,
             sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
             targetPath: '/Users/preview/.local/bin/vibermate',
           );

  TerminalCommandStatus _status;

  /// What a new Terminal window answers once the command is set up: found,
  /// as on most Macs, unless the preview asks for another answer.
  ShellCommandLookup shellLookup;

  @override
  Future<ShellCommandCheck> checkShell(TerminalCommandStatus status) async =>
      status.ready
      ? ShellCommandCheck(shellLookup, shell: 'zsh')
      : ShellCommandCheck.unknown;

  @override
  Future<TerminalCommandStatus> inspect() async => _status;

  @override
  Future<TerminalCommandStatus> execute(
    TerminalCommandOperation operation,
  ) async {
    final current = _status;
    switch (operation) {
      case TerminalCommandOperation.install:
        if (!current.canInstall) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
          sourceBuild: current.sourceBuild,
          installedBuild: current.sourceBuild,
        );
      case TerminalCommandOperation.refresh:
        if (!current.canRefresh) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
          sourceBuild: current.sourceBuild,
          installedBuild: current.sourceBuild,
        );
      case TerminalCommandOperation.repair:
        if (!current.canRepair) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
          sourceBuild: current.sourceBuild,
          installedBuild: current.sourceBuild,
        );
      case TerminalCommandOperation.remove:
        if (!current.canRemove) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.notInstalled,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
          sourceBuild: current.sourceBuild,
        );
    }
    return _status;
  }

  static TerminalCommandException _failed() =>
      const TerminalCommandException(TerminalCommandFailure.failed);
}

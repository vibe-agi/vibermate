import '../core/bootstrap/terminal_command.dart';

final class PreviewTerminalCommandService implements TerminalCommandService {
  PreviewTerminalCommandService({TerminalCommandStatus? initial})
    : _status =
          initial ??
          const TerminalCommandStatus(
            state: TerminalCommandState.notInstalled,
            sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
            targetPath: '/Users/preview/.local/bin/vibermate',
          );

  TerminalCommandStatus _status;

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
        );
      case TerminalCommandOperation.refresh:
        if (!current.canRefresh) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
        );
      case TerminalCommandOperation.repair:
        if (!current.canRepair) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
        );
      case TerminalCommandOperation.remove:
        if (!current.canRemove) throw _failed();
        _status = TerminalCommandStatus(
          state: TerminalCommandState.notInstalled,
          sourcePath: current.sourcePath,
          targetPath: current.targetPath,
        );
    }
    return _status;
  }

  static TerminalCommandException _failed() =>
      const TerminalCommandException(TerminalCommandFailure.failed);
}

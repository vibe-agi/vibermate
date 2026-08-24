import 'terminal_command_contract.dart';

/// A browser cannot install or inspect a command on the machine serving the
/// Runtime. The workbench keeps the same interface and reports that boundary
/// explicitly instead of pretending a remote file operation is available.
final class PackagedTerminalCommandService implements TerminalCommandService {
  const PackagedTerminalCommandService({String? commandPath});

  @override
  Future<TerminalCommandStatus> inspect() => Future.error(
    const TerminalCommandException(TerminalCommandFailure.unavailable),
  );

  @override
  Future<TerminalCommandStatus> execute(TerminalCommandOperation operation) =>
      Future.error(
        const TerminalCommandException(TerminalCommandFailure.unavailable),
      );
}

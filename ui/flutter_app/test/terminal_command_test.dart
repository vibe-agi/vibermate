import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  const source = '/Applications/ViberMate.app/Contents/MacOS/vibermate';
  const target = '/Users/mira/.local/bin/vibermate';

  test('Terminal command status is closed and source-bound', () {
    final status = TerminalCommandStatus.fromJson({
      'schema': 'vibermate-terminal-command/v1',
      'state': 'source_updated',
      'sourcePath': source,
      'targetPath': target,
      'detail': 'the packaged command changed',
    }, expectedSourcePath: source);

    expect(status.state, TerminalCommandState.sourceUpdated);
    expect(status.canInstall, isFalse);
    expect(status.canRefresh, isTrue);
    expect(status.canRepair, isFalse);
    expect(status.canRemove, isTrue);

    expect(
      () => TerminalCommandStatus.fromJson({
        'schema': 'vibermate-terminal-command/v1',
        'state': 'current',
        'sourcePath': source,
        'targetPath': target,
        'credential': 'must-not-cross-the-host-boundary',
      }, expectedSourcePath: source),
      throwsA(isA<TerminalCommandException>()),
    );
    expect(
      () => TerminalCommandStatus.fromJson({
        'schema': 'vibermate-terminal-command/v1',
        'state': 'current',
        'sourcePath': '/Applications/Other.app/Contents/MacOS/vibermate',
        'targetPath': target,
      }, expectedSourcePath: source),
      throwsA(isA<TerminalCommandException>()),
    );
    expect(
      () => TerminalCommandStatus.fromJson({
        'schema': 'vibermate-terminal-command/v1',
        'state': 'current',
        'sourcePath': source,
        'targetPath': '/Users/mira/.local/../bin/vibermate',
      }, expectedSourcePath: source),
      throwsA(isA<TerminalCommandException>()),
    );
  });

  test(
    'Preview service refuses operations that do not match authority',
    () async {
      final conflict = PreviewTerminalCommandService(
        initial: const TerminalCommandStatus(
          state: TerminalCommandState.unownedTarget,
          sourcePath: source,
          targetPath: target,
        ),
      );
      await expectLater(
        conflict.execute(TerminalCommandOperation.install),
        throwsA(
          isA<TerminalCommandException>().having(
            (error) => error.failure,
            'failure',
            TerminalCommandFailure.failed,
          ),
        ),
      );

      final updated = PreviewTerminalCommandService(
        initial: const TerminalCommandStatus(
          state: TerminalCommandState.sourceUpdated,
          sourcePath: source,
          targetPath: target,
        ),
      );
      expect(
        (await updated.execute(TerminalCommandOperation.refresh)).state,
        TerminalCommandState.current,
      );
      expect(
        (await updated.execute(TerminalCommandOperation.remove)).state,
        TerminalCommandState.notInstalled,
      );

      final missing = PreviewTerminalCommandService(
        initial: const TerminalCommandStatus(
          state: TerminalCommandState.targetMissing,
          sourcePath: source,
          targetPath: target,
        ),
      );
      expect(
        (await missing.execute(TerminalCommandOperation.repair)).state,
        TerminalCommandState.current,
      );
    },
  );
}

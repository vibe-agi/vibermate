import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final state in TerminalCommandState.values) {
    test('startup maintains only App-owned terminal links: $state', () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(
          initial: TerminalCommandStatus(
            state: state,
            sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
            targetPath: '/Users/test/.local/bin/vibermate',
          ),
        ),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.initialize();
      final maintained =
          state == TerminalCommandState.sourceUpdated ||
          state == TerminalCommandState.targetMissing;
      expect(
        controller.terminalCommand!.state,
        maintained ? TerminalCommandState.current : state,
      );
      expect(controller.terminalCommandNotice, switch (state) {
        TerminalCommandState.sourceUpdated => 'terminal.notice.auto_refreshed',
        TerminalCommandState.targetMissing => 'terminal.notice.auto_repaired',
        TerminalCommandState.sourceMissing ||
        TerminalCommandState.unownedTarget ||
        TerminalCommandState.conflict => 'terminal.attention',
        _ => null,
      });
    });
  }
}

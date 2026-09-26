import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/core/update/product_update.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets(
    '390px update guidance is explicit, channel-aware, and never automatic',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(productBuild: 'v0.1.12');
      final updates = _UpdateService(
        ProductUpdateResult(
          state: ProductUpdateState.available,
          channel: ProductInstallChannel.homebrew,
          checkedAt: DateTime.utc(2026, 9, 26),
          availableVersion: 'v0.1.14',
          releaseName: 'ViberMate 0.1.14',
          releaseUrl: Uri.parse(
            'https://github.com/vibe-agi/vibermate/releases/tag/v0.1.14',
          ),
          publishedAt: DateTime.utc(2026, 9, 26),
        ),
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(
          initial: const TerminalCommandStatus(
            state: TerminalCommandState.sourceUpdated,
            sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
            targetPath: '/Users/preview/.local/bin/vibermate',
            sourceBuild: 'v0.1.13',
            installedBuild: 'v0.1.12',
          ),
        ),
        previewMode: false,
        closeRuntime: api.close,
        productUpdateService: updates,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.settings,
        ),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('product-build-mismatch')), findsOneWidget);
      expect(find.byKey(const Key('product-update-panel')), findsOneWidget);
      expect(updates.calls, 0);
      final check = find.byKey(const Key('product-update-check'));
      await tester.scrollUntilVisible(
        check,
        240,
        scrollable: find.descendant(
          of: find.byKey(const Key('settings-scroll')),
          matching: find.byType(Scrollable),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(check);
      await tester.pumpAndSettle();
      expect(updates.calls, 1);
      expect(find.text('Update available'), findsOneWidget);
      expect(
        find.textContaining('Homebrew installation detected'),
        findsOneWidget,
      );
      expect(find.byKey(const Key('product-update-copy-brew')), findsOneWidget);
      expect(
        find.byKey(const Key('product-update-open-release')),
        findsOneWidget,
      );
      expect(
        find.textContaining('never downloads or installs'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );
}

final class _UpdateService implements ProductUpdateService {
  _UpdateService(this.result);

  final ProductUpdateResult result;
  int calls = 0;

  @override
  Future<ProductUpdateResult> check() async {
    calls++;
    return result;
  }
}

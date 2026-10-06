import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/core/api/product_update_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final available in [true, false]) {
      testWidgets(
        'Actual update action ${language.name} available=$available at390px/200percent',
        (tester) async {
          await tester.binding.setSurfaceSize(const Size(390, 1100));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = _ViewUpdateApi(available);
          final controller = WorkbenchController(
            api: PreviewControlApi(),
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: false,
            closeRuntime: api.close,
            productUpdateService: ControlProductUpdateService(
              api: api,
              channelResolver: () async => ProductInstallChannel.manual,
            ),
            initialPreferences: WorkbenchPreferences(
              language: language,
              section: WorkbenchSection.settings,
            ),
          );
          addTearDown(controller.dispose);
          await controller.initialize();
          await tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.light(),
              builder: (context, child) => MediaQuery(
                data: MediaQuery.of(
                  context,
                ).copyWith(textScaler: const TextScaler.linear(2)),
                child: child!,
              ),
              home: WorkbenchShell(controller: controller),
            ),
          );
          await tester.pumpAndSettle();
          expect(api.checks, 0);
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
          expect(api.checks, 1);
          final copy = language == AppLanguage.english
              ? (available
                    ? 'Update available'
                    : 'The release check is unavailable.')
              : (available ? '有可用更新' : '暂时无法检查 Release。');
          if (available) {
            expect(find.text(copy), findsOneWidget);
          } else {
            expect(find.textContaining(copy), findsOneWidget);
            expect(controller.productUpdate?.releaseUrl, isNull);
            expect(
              find.byKey(const Key('product-update-copy-brew')),
              findsNothing,
            );
          }
          expect(tester.takeException(), isNull);
          controller.dispose();
          await tester.pumpWidget(const SizedBox.shrink());
          await tester.pump();
        },
      );
    }
  }
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

final class _ViewUpdateApi implements ControlApi {
  _ViewUpdateApi(this.available);
  final bool available;
  int checks = 0;
  @override
  Future<ProductUpdateMetadata> checkProductUpdate() async {
    checks++;
    if (!available) {
      throw const ControlContractException('Old server unavailable');
    }
    return ProductUpdateMetadata(
      checkedAt: DateTime.utc(2026, 10, 5),
      tag: 'v9.8.7',
      name: 'Private release',
      url: Uri.parse(
        'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
      ),
      publishedAt: DateTime.utc(2026, 10, 4),
    );
  }

  @override
  Future<void> close() async {}
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
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

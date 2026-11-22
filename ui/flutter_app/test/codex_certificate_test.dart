import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/launch_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';

void main() {
  Future<WorkbenchController> mount(
    WidgetTester tester, {
    AppLanguage language = AppLanguage.english,
    void Function(PreviewControlApi api)? prepare,
  }) async {
    final fixture = PreviewFixture.freshInstall(
      stage: PreviewFreshStage.command,
    );
    prepare?.call(fixture.api);
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      rootTrustManagement: true,
      rootTrustInstaller: PreviewRootTrustInstaller(fixture.api),
      closeRuntime: fixture.api.close,
      initialPreferences: WorkbenchPreferences(language: language),
    );
    await controller.initialize();
    await controller.refreshRootCA();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: AnimatedBuilder(
            animation: controller,
            builder: (context, _) => Padding(
              padding: const EdgeInsets.all(16),
              child: CodexCertificateRow(
                controller: controller,
                copy: AppCopy.forLanguage(language),
                keyPrefix: 'row',
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pump();
    return controller;
  }

  testWidgets('a certificate not yet trusted is a next step, not a fault', (
    tester,
  ) async {
    final controller = await mount(tester);
    final pill = tester.widget<StatusPill>(
      find.descendant(
        of: find.byKey(const Key('row-codex-requirement')),
        matching: find.byType(StatusPill),
      ),
    );
    expect(pill.label, 'Not trusted yet · needed for Codex');
    expect(pill.color, ViberColors.light.textMuted);
    expect(find.text('ViberMate certificate'), findsOneWidget);
    expect(
      tester.widget(find.byKey(const Key('row-install-trust'))),
      isA<OutlinedButton>(),
    );
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  testWidgets('a half-finished authorization is a warning', (tester) async {
    final controller = await mount(
      tester,
      language: AppLanguage.simplifiedChinese,
      prepare: (api) {
        api.setRootTrustForPreview(installed: true);
      },
    );
    // Present in the keychain, but its trust setting is gone.
    controller.rootCAStatus = controller.rootCAStatus!.copyWith(
      trustDecision: 'untrusted',
    );
    controller.notifyListeners();
    await tester.pump();
    final pill = tester.widget<StatusPill>(
      find.descendant(
        of: find.byKey(const Key('row-codex-requirement')),
        matching: find.byType(StatusPill),
      ),
    );
    expect(pill.label, '已安装但未信任 · Codex 需要');
    expect(pill.color, ViberColors.light.warning);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  testWidgets('the row asks before macOS does, then trusts the certificate', (
    tester,
  ) async {
    final controller = await mount(tester);
    await tester.tap(find.byKey(const Key('row-install-trust')));
    await tester.pumpAndSettle();
    expect(
      find.text("Trust ViberMate's certificate on this Mac?"),
      findsOneWidget,
    );
    expect(find.textContaining('keep them private'), findsOneWidget);
    expect(find.textContaining('macOS will ask for your password'), findsOne);
    await tester.tap(find.byKey(const Key('root-ca-install-continue')));
    await tester.pumpAndSettle();
    expect(controller.rootCAStatus!.installed, isTrue);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  test('backups say first that they hold the private key', () {
    expect(
      AppCopy.forLanguage(AppLanguage.english)(
        'settings.storage.backup_confirmation',
      ),
      startsWith(
        "This backup includes the private key of ViberMate's certificate.",
      ),
    );
    expect(
      AppCopy.forLanguage(AppLanguage.simplifiedChinese)(
        'settings.storage.backup_confirmation',
      ),
      startsWith('此备份包含 ViberMate 证书的私钥。'),
    );
    for (final language in AppLanguage.values) {
      expect(
        AppCopy.forLanguage(language)('settings.root_ca.private_key'),
        isNot(anyOf(contains('never exported'), contains('永不导出'))),
      );
    }
  });
}

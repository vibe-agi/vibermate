import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/root_trust_installer.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('Root CA guidance fits both themes and locales at 390 px', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    for (final fixture
        in <
          ({
            ThemeData theme,
            AppLanguage language,
            String title,
            String delivery,
          })
        >[
          (
            theme: ViberTheme.light(),
            language: AppLanguage.english,
            title: 'ViberMate certificate',
            delivery: _purposeEnglish,
          ),
          (
            theme: ViberTheme.dark(),
            language: AppLanguage.english,
            title: 'ViberMate certificate',
            delivery: _purposeEnglish,
          ),
          (
            theme: ViberTheme.light(),
            language: AppLanguage.simplifiedChinese,
            title: 'ViberMate 证书',
            delivery: _purposeChinese,
          ),
          (
            theme: ViberTheme.dark(),
            language: AppLanguage.simplifiedChinese,
            title: 'ViberMate 证书',
            delivery: _purposeChinese,
          ),
        ]) {
      final api = PreviewControlApi(seedCaptures: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        terminalManagement: false,
        rootTrustManagement: true,
        closeRuntime: api.close,
        initialPreferences: WorkbenchPreferences(
          section: WorkbenchSection.settings,
          language: fixture.language,
        ),
      );
      await controller.refresh();
      await tester.pumpWidget(
        MaterialApp(
          theme: fixture.theme,
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();
      await _revealInSettings(
        tester,
        find.byKey(const Key('root-ca-settings-panel')),
      );
      expect(find.text(fixture.title), findsOneWidget);
      // The purpose, including that Codex needs it, needs no help button.
      expect(find.text(fixture.delivery), findsOneWidget);
      expect(find.byTooltip(fixture.title), findsNothing);
      // Details most people never need start folded away.
      expect(find.byKey(const Key('root-ca-details')), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
      await tester.pump();
    }
  });

  testWidgets('Root CA settings install explicitly and replace in one flow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(seedCaptures: false);
    var restarted = false;
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      terminalManagement: false,
      rootTrustManagement: true,
      rootTrustInstaller: PreviewRootTrustInstaller(api),
      closeRuntime: api.close,
      restartRuntime: () async {
        restarted = true;
      },
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
        language: AppLanguage.simplifiedChinese,
      ),
    );
    addTearDown(controller.dispose);
    await controller.refresh();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();

    final panel = find.byKey(const Key('root-ca-settings-panel'));
    await _revealInSettings(tester, panel);
    expect(panel, findsOneWidget);
    expect(find.text('ViberMate 证书'), findsOneWidget);
    expect(find.text('尚未信任 · Codex 需要'), findsOneWidget);
    expect(
      tester.widget<StatusPill>(find.byKey(const Key('root-ca-status'))).color,
      ViberColors.light.textMuted,
      reason: 'a next step, not a fault',
    );
    final install = find.byKey(const Key('root-ca-install'));
    await _revealInSettings(tester, install);
    expect(tester.widget(install), isA<OutlinedButton>());
    await tester.tap(install);
    await tester.pumpAndSettle();
    // Before macOS asks for a password, the app says what trusting allows.
    final confirmation = find.byKey(const Key('root-ca-install-confirmation'));
    expect(confirmation, findsOneWidget);
    expect(find.text('要在这台 Mac 上信任 ViberMate 的证书吗？'), findsOneWidget);
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();
    expect(find.text('尚未信任 · Codex 需要'), findsOneWidget);
    await tester.tap(install);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('root-ca-install-continue')));
    await tester.pumpAndSettle();
    expect(find.text('已安装并信任'), findsOneWidget);
    expect(find.byKey(const Key('root-ca-updated')), findsOneWidget);
    expect(find.textContaining('永不导出'), findsNothing);

    final guidedReplace = find.byKey(const Key('root-ca-replace'));
    await _openDetails(tester);
    await _revealInSettings(tester, guidedReplace);
    await tester.tap(guidedReplace);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('root-ca-confirmation')), findsNothing);
    expect(find.textContaining('更换前，请在“钥匙串访问”'), findsOneWidget);

    final remove = find.byKey(const Key('root-ca-remove'));
    await _revealInSettings(tester, remove);
    await tester.tap(remove);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('root-ca-confirmation')), findsNothing);
    expect(find.text('已安装并信任'), findsOneWidget);
    expect(find.textContaining('在“钥匙串访问”中打开'), findsOneWidget);

    api.setRootTrustForPreview(installed: false);
    await controller.refreshRootCA();
    await tester.pumpAndSettle();
    expect(find.text('尚未信任 · Codex 需要'), findsOneWidget);

    final replace = find.byKey(const Key('root-ca-replace'));
    await _openDetails(tester);
    await _revealInSettings(tester, replace);
    await tester.tap(replace);
    await tester.pumpAndSettle();
    expect(find.text('更换证书并重新启动？'), findsOneWidget);
    await tester.tap(find.byKey(const Key('root-ca-confirm-action')));
    await tester.pumpAndSettle();
    expect(restarted, isTrue);
    expect(tester.takeException(), isNull);
  });

  testWidgets('Unknown trust keeps the public identity visible for recovery', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(seedCaptures: false);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      terminalManagement: false,
      rootTrustManagement: true,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
        language: AppLanguage.english,
      ),
    );
    addTearDown(controller.dispose);
    await controller.refresh();
    final current = controller.rootCAStatus!;
    controller.rootCAStatus = current.copyWith(
      certificatePresent: 'unknown',
      trustDecision: 'unknown',
      available: false,
      reason: 'observation_unknown',
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    await _revealInSettings(
      tester,
      find.byKey(const Key('root-ca-settings-panel')),
    );

    expect(find.text(current.fingerprint), findsOneWidget);
    expect(find.text('Trust status unavailable'), findsOneWidget);
    expect(
      find.textContaining('Keychain Access → login → Certificates'),
      findsOneWidget,
    );
    expect(find.byKey(const Key('root-ca-install')), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'Root trust authorization failures stay actionable in both locales',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      for (final fixture in <({AppLanguage language, String expected})>[
        (
          language: AppLanguage.english,
          expected:
              "macOS could not update the current user's certificate store. Unlock this Mac, then retry.",
        ),
        (
          language: AppLanguage.simplifiedChinese,
          expected: 'macOS 无法更新当前用户的证书存储。请先解锁这台 Mac，再重试。',
        ),
      ]) {
        final api = PreviewControlApi(seedCaptures: false);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          terminalManagement: false,
          rootTrustManagement: true,
          rootTrustInstaller: const _FailingRootTrustInstaller(),
          closeRuntime: api.close,
          initialPreferences: WorkbenchPreferences(
            section: WorkbenchSection.settings,
            language: fixture.language,
          ),
        );
        await controller.refresh();
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: WorkbenchShell(controller: controller),
          ),
        );
        await tester.pumpAndSettle();
        final install = find.byKey(const Key('root-ca-install'));
        await _revealInSettings(tester, install);
        await tester.tap(install);
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('root-ca-install-continue')));
        await tester.pumpAndSettle();
        expect(find.text(fixture.expected), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        controller.dispose();
        await tester.pump();
      }
    },
  );

  testWidgets('Active captures make Root replacement unavailable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      terminalManagement: false,
      rootTrustManagement: true,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
        language: AppLanguage.simplifiedChinese,
      ),
    );
    addTearDown(controller.dispose);
    await controller.refresh();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    final replace = find.byKey(const Key('root-ca-replace'));
    await _openDetails(tester);
    await _revealInSettings(tester, replace);

    expect(tester.widget<OutlinedButton>(replace).onPressed, isNull);
    expect(find.text('更换根证书前，请先停止所有运行中与手动 Capture。'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('Root replacement restarts the local runtime generation', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(900, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    var connections = 0;
    final firstCloseEntered = Completer<void>();
    final allowFirstClose = Completer<void>();
    await tester.pumpWidget(
      ViberMateApp(
        previewMode: false,
        preferChinese: true,
        preferencesStore: MemoryWorkbenchPreferencesStore(
          encoded: const WorkbenchPreferences(
            section: WorkbenchSection.settings,
            language: AppLanguage.simplifiedChinese,
          ).encode(),
        ),
        runtimeConnector: ({RuntimeLoginAttempt? login}) async {
          connections += 1;
          final generation = connections;
          final api = PreviewControlApi(seedCaptures: false);
          var closed = false;
          return RuntimeConnection(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            close: () async {
              if (closed) return;
              closed = true;
              if (generation == 1) {
                firstCloseEntered.complete();
                await allowFirstClose.future;
              }
              await api.close();
            },
            isClosed: () => closed,
            serverManagement: false,
            terminalManagement: false,
            rootTrustManagement: true,
            targetLabel: 'This Mac',
          );
        },
      ),
    );
    await tester.pumpAndSettle();

    final replace = find.byKey(const Key('root-ca-replace'));
    await _openDetails(tester);
    await _revealInSettings(tester, replace);
    await tester.tap(replace);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('root-ca-confirm-action')));
    await tester.pump();
    await firstCloseEntered.future;
    await tester.pump();
    expect(
      connections,
      1,
      reason: 'a new daemon started before the old one exited',
    );
    allowFirstClose.complete();
    await tester.pumpAndSettle();

    expect(connections, 2);
    final panel = find.byKey(const Key('root-ca-settings-panel'));
    await _revealInSettings(tester, panel);
    expect(panel, findsOneWidget);
    expect(find.text('尚未信任 · Codex 需要'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

const _purposeEnglish =
    "ViberMate uses this certificate to read your agents' encrypted requests. "
    'Claude Code is set up automatically on every run. Codex only accepts '
    'certificates this Mac trusts, so choose Install and trust once before '
    'you run vibermate run -- codex. macOS then trusts this certificate for '
    'HTTPS in the apps you use under your macOS account; ViberMate uses it '
    'only for connections that go through ViberMate. You can undo this with '
    'Removal steps.';

const _purposeChinese =
    'ViberMate 用这张证书读取 Agent 的加密请求。Claude Code 每次运行都会自动配置；'
    'Codex 只接受这台 Mac 信任的证书，所以首次运行 vibermate run -- codex 前，'
    '请先点一次「安装并信任」。之后，你的 macOS 账户中的应用都会在 HTTPS 连接中信任'
    '这张证书，但 ViberMate 只把它用于经过 ViberMate 的连接。可以通过「查看移除步骤」撤销。';

/// Opens "Certificate details", where replacing the certificate lives.
Future<void> _openDetails(WidgetTester tester) async {
  if (find.byKey(const Key('root-ca-details')).evaluate().isNotEmpty) return;
  final toggle = find.byKey(const Key('root-ca-details-toggle'));
  await _revealInSettings(tester, toggle);
  await tester.tap(toggle);
  await tester.pumpAndSettle();
}

final class _FailingRootTrustInstaller implements RootTrustInstaller {
  const _FailingRootTrustInstaller();

  @override
  Future<void> install(RootCAMaterial material) => Future<void>.error(
    const RootTrustInstallerException(
      RootTrustInstallerFailure.permissionDenied,
    ),
  );
}

Future<void> _revealInSettings(WidgetTester tester, Finder target) async {
  if (target.evaluate().isEmpty) {
    final picker = find.byKey(const Key('settings-section-picker'));
    if (picker.evaluate().isNotEmpty) {
      await tester.tap(picker);
      await tester.pumpAndSettle();
      final label = tester
          .widget<SettingsView>(find.byType(SettingsView))
          .copy('settings.tab.safety');
      await tester.tap(find.text(label).hitTestable().last);
    } else {
      final safetyTab = find.byKey(const Key('settings-tab-safety'));
      await tester.ensureVisible(safetyTab);
      await tester.pumpAndSettle();
      await tester.tap(safetyTab);
    }
    await tester.pumpAndSettle();
  }
  final scrollable = find
      .descendant(
        of: find.byKey(const Key('settings-safety-scroll')),
        matching: find.byType(Scrollable),
      )
      .first;
  await tester.scrollUntilVisible(target, 180, scrollable: scrollable);
  await tester.pumpAndSettle();
}

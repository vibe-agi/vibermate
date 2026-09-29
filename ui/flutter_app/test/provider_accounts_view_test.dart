import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final reviewDirectory = kIsWeb
      ? null
      : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
  if (reviewDirectory != null && Platform.isMacOS) {
    setUpAll(() async {
      for (final font in {
        viberSystemFontFamily: '/System/Library/Fonts/Helvetica.ttc',
        'Ahem': '/System/Library/Fonts/Helvetica.ttc',
        'Menlo': '/System/Library/Fonts/Menlo.ttc',
        'Hiragino Sans GB': '/System/Library/Fonts/Hiragino Sans GB.ttc',
      }.entries) {
        final loader = FontLoader(font.key);
        loader.addFont(
          File(
            font.value,
          ).readAsBytes().then((bytes) => ByteData.sublistView(bytes)),
        );
        await loader.load();
      }
      final icons = FontLoader('MaterialIcons')
        ..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
      await icons.load();
    });
  }

  testWidgets('remote account settings appear before the next local edit', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(seedCaptures: false);
    final login = await api.startCodexLogin(
      accountId: 'account.remote-settings',
      upstreamEndpointId: 'target.codex.official',
      displayName: 'Remote settings fixture',
      callbackMode: 'manual',
    );
    await api.completeCodexLogin(
      login.id,
      'http://localhost:1455/auth/callback?state=${login.id}&code=synthetic',
    );
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        language: AppLanguage.simplifiedChinese,
        section: WorkbenchSection.providerAccounts,
      ),
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('provider-accounts-search')),
      'Remote settings fixture',
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(
        const Key('provider-account-details-toggle-account.remote-settings'),
      ),
    );
    await tester.pumpAndSettle();
    final toggle = find.byKey(
      const Key('account-automatic-refresh-account.remote-settings'),
    );
    expect(tester.widget<Switch>(toggle).value, isTrue);
    final original = (await api.loadDashboard()).accounts.singleWhere(
      (a) => a.id == 'account.remote-settings',
    );
    final remote = await api.setProviderAccountSettings(
      original,
      automaticRefresh: false,
      egressProfile: EgressProfileRevision.direct,
    );
    expect(remote.revision, original.revision);
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();
    expect(tester.widget<Switch>(toggle).value, isFalse);
    final exit = find.byKey(
      const Key('account-egress-account.remote-settings'),
    );
    expect(
      find.descendant(of: exit, matching: find.text('直连 · 系统 DNS')),
      findsOneWidget,
    );
    await tester.ensureVisible(toggle);
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(controller.inventoryError, isNull);
    final saved = (await api.loadDashboard()).accounts.singleWhere(
      (a) => a.id == original.id,
    );
    expect(saved.automaticRefresh, isTrue);
    expect(saved.egressProfile, EgressProfileRevision.direct);
    expect(saved.settingsRevision, remote.settingsRevision + 1);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  for (final width in [390.0, 1180.0]) {
    testWidgets(
      'account settings select a shared exit and keep refresh explicit at $width',
      (tester) async {
        final semantics = tester.ensureSemantics();
        await tester.binding.setSurfaceSize(Size(width, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(seedCaptures: false);
        final login = await api.startCodexLogin(
          accountId: 'account.settings',
          upstreamEndpointId: 'target.codex.official',
          displayName: 'Settings fixture',
          callbackMode: 'manual',
        );
        await api.completeCodexLogin(
          login.id,
          'http://localhost:1455/auth/callback?state=${login.id}&code=synthetic',
        );
        final secondLogin = await api.startCodexLogin(
          accountId: 'account.settings-second',
          upstreamEndpointId: 'target.codex.official',
          displayName: 'Settings fixture second',
          callbackMode: 'manual',
        );
        await api.completeCodexLogin(
          secondLogin.id,
          'http://localhost:1455/auth/callback?state=${secondLogin.id}&code=synthetic',
        );
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
          initialPreferences: const WorkbenchPreferences(
            language: AppLanguage.simplifiedChinese,
            section: WorkbenchSection.providerAccounts,
          ),
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        final boundary = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: boundary,
            child: MaterialApp(
              theme: ViberTheme.dark(),
              home: WorkbenchShell(controller: controller),
            ),
          ),
        );
        await tester.pumpAndSettle();
        await tester.enterText(
          find.byKey(const Key('provider-accounts-search')),
          'Settings fixture',
        );
        await tester.pumpAndSettle();
        ProviderAccount current() => controller.data!.accounts.singleWhere(
          (a) => a.id == 'account.settings',
        );
        final toggle = find.byKey(
          const Key('account-automatic-refresh-account.settings'),
        );
        expect(find.text('自动刷新'), findsNWidgets(width >= 1180 ? 1 : 2));
        expect(tester.getRect(toggle).height, lessThanOrEqualTo(32));
        final hit = tester.getRect(
          find.byKey(
            const Key('account-automatic-refresh-hit-account.settings'),
          ),
        );
        expect(hit.width, greaterThanOrEqualTo(48));
        expect(hit.height, greaterThanOrEqualTo(width < 1180 ? 44 : 32));
        if (width >= 1180) {
          final heading = tester.getRect(
            find.byKey(const Key('provider-accounts-automatic-refresh-column')),
          );
          expect(hit.center.dx, closeTo(heading.center.dx, .1));
        }
        expect(find.bySemanticsLabel('自动刷新: Settings fixture'), findsOneWidget);
        expect(
          tester.getSemantics(toggle),
          matchesSemantics(
            label: '自动刷新: Settings fixture',
            textDirection: TextDirection.ltr,
            hasEnabledState: true,
            isEnabled: true,
            hasToggledState: true,
            isToggled: true,
            isFocusable: true,
            hasFocusAction: true,
            hasTapAction: true,
          ),
        );
        semantics.dispose();
        expect(tester.widget<Switch>(toggle).value, isTrue);
        // The quiet, smaller drawing must not shrink the clickable target.
        await tester.tapAt(Offset(hit.right - 2, hit.center.dy));
        await tester.pumpAndSettle();
        expect(current().automaticRefresh, isFalse);
        expect(tester.widget<Switch>(toggle).value, isFalse);
        expect(find.text('已托管刷新'), findsNothing);
        await tester.tap(
          find.byKey(
            const Key('provider-account-details-toggle-account.settings'),
          ),
        );
        await tester.pumpAndSettle();
        final exit = find.byKey(const Key('account-egress-account.settings'));
        await tester.ensureVisible(exit);
        await tester.tap(exit);
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('account-egress-inherit')), findsOneWidget);
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'account-exit-picker-${width.toInt()}',
        );
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-profile.direct-1')),
        );
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-save')),
        );
        await tester.pumpAndSettle();
        expect(current().egressProfile, EgressProfileRevision.direct);
        expect(current().automaticRefresh, isFalse);
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'account-settings-${width.toInt()}',
        );
        await tester.ensureVisible(exit);
        await tester.tap(exit);
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('account-egress-inherit')));
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-save')),
        );
        await tester.pumpAndSettle();
        expect(current().egressProfile, isNull);
        expect(current().revision, 1);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        controller.dispose();
      },
    );

    testWidgets(
      'accounts are independent; compatible links can be removed at $width px',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(width, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(seedCaptures: false);
        final endpoint = await api.createUpstreamEndpoint(
          id: 'profile.test',
          displayName: 'ChatGPT · Review',
          origin: 'https://chatgpt.com',
          backendProtocols: ['openai_responses'],
        );
        await api.createProviderAccount(
          id: 'account.independent',
          displayName: 'Codex · Personal',
          upstreamEndpointId: 'target.codex.official',
          kind: 'bearer_token',
          secret: 'synthetic-account-secret',
          headerPolicy: const ProviderAccountHeaderPolicy(),
          unlinked: true,
        );
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
          initialPreferences: const WorkbenchPreferences(
            language: AppLanguage.simplifiedChinese,
            section: WorkbenchSection.providerAccounts,
          ),
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        final boundary = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: boundary,
            child: MaterialApp(
              theme: ViberTheme.dark(),
              home: WorkbenchShell(controller: controller),
            ),
          ),
        );
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('provider-accounts-add')), findsOneWidget);
        expect(find.text('synthetic-account-secret'), findsNothing);
        final searchRect = tester.getRect(
          find.byKey(const Key('provider-accounts-search')),
        );
        final sortRect = tester.getRect(
          find.byKey(const Key('provider-accounts-sort')),
        );
        expect(sortRect.height, searchRect.height);
        if (width >= 1000) expect(sortRect.top, searchRect.top);
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'accounts-${width.toInt()}',
        );
        controller.selectEndpoint(endpoint.id);
        controller.selectSection(WorkbenchSection.routes);
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('accounts-add')), findsNothing);
        expect(
          find.byKey(const Key('account-delete-account.independent')),
          findsNothing,
        );
        await tester.ensureVisible(find.byKey(const Key('accounts-link')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('accounts-link')));
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('account-link-account.independent')),
          findsOneWidget,
        );
        // Same protocol is insufficient: other origins have no link action.
        expect(
          find.byKey(const Key('account-link-openai-personal')),
          findsNothing,
        );
        expect(
          find.byKey(const Key('account-link-anthropic-work')),
          findsNothing,
        );
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'linker-${width.toInt()}',
        );
        await tester.tap(
          find.byKey(const Key('account-link-account.independent')),
        );
        await tester.pumpAndSettle();
        ProviderAccount current() => controller.data!.accounts.singleWhere(
          (account) => account.id == 'account.independent',
        );
        expect(current().linkedEndpointIds, [endpoint.id]);
        expect(current().credentialEpoch, 1);
        expect(current().revision, 1);
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'service-linked-${width.toInt()}',
        );
        await tester.ensureVisible(find.byKey(const Key('accounts-link')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('accounts-link')));
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('account-link-row-account.independent')),
          findsOneWidget,
        );
        expect(
          find.byKey(const Key('account-link-account.independent')),
          findsNothing,
        );
        expect(find.text('已关联'), findsOneWidget);
        await tester.enterText(
          find.byKey(const Key('account-link-search')),
          'api.openai.com',
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('account-link-row-openai-work')),
          findsOneWidget,
        );
        expect(find.text('账号的适用地址与此服务不同。'), findsOneWidget);
        expect(find.byKey(const Key('account-link-openai-work')), findsNothing);
        await tester.enterText(
          find.byKey(const Key('account-link-search')),
          'no matching account',
        );
        await tester.pumpAndSettle();
        expect(find.text('没有匹配的账号，请调整搜索内容'), findsOneWidget);
        await tester.tap(find.byKey(const Key('account-link-clear-search')));
        await tester.pumpAndSettle();
        expect(find.text('已关联'), findsOneWidget);
        await tester.tap(find.text('取消'));
        await tester.pumpAndSettle();
        await tester.ensureVisible(
          find.byKey(const Key('account-unlink-account.independent')),
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const Key('account-unlink-account.independent')),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('account-unlink-confirm')));
        await tester.pumpAndSettle();
        expect(current().linkedEndpointIds, isEmpty);
        expect(current().credentialEpoch, 1);
        expect(current().associationRevision, 3);
        controller.inventoryNotice = 'account_created';
        controller.selectSection(WorkbenchSection.providerAccounts);
        await tester.pumpAndSettle();
        expect(
          find.text('关联到上游服务'),
          width < 600 ? findsNothing : findsOneWidget,
        );
        await tester.enterText(
          find.byKey(const Key('provider-accounts-search')),
          'Codex · Personal',
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('provider-account-account.independent')),
          findsOneWidget,
        );
        expect(find.text('未关联'), findsOneWidget);
        expect(
          find.byKey(const Key('provider-account-details-account.independent')),
          findsNothing,
        );
        if (width >= 1000) {
          expect(find.text('已用额度'), findsOneWidget);
          expect(
            find.byKey(
              const Key('provider-account-resets-account.independent'),
            ),
            findsOneWidget,
          );
          expect(
            tester
                .getSize(
                  find.byKey(const Key('provider-account-account.independent')),
                )
                .height,
            lessThan(100),
          );
        }
        final detailsToggle = find.byKey(
          const Key('provider-account-details-toggle-account.independent'),
        );
        await tester.ensureVisible(detailsToggle);
        await tester.tap(detailsToggle);
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('provider-account-details-account.independent')),
          findsOneWidget,
        );
        expect(
          find.byKey(const Key('account-quota-account.independent')),
          findsNothing,
        );
        await _review(
          tester,
          boundary,
          reviewDirectory,
          'account-details-${width.toInt()}',
        );
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
        controller.dispose();
      },
    );
  }

  testWidgets('account sorting uses the visible account identity', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(seedCaptures: false);
    for (final name in ['sort-fixture-zulu', 'sort-fixture-alpha']) {
      await api.createProviderAccount(
        id: 'account.$name',
        displayName: name,
        upstreamEndpointId: 'target.codex.official',
        kind: 'bearer_token',
        secret: 'synthetic-account-secret',
        headerPolicy: const ProviderAccountHeaderPolicy(),
        unlinked: true,
      );
    }
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
      initialPreferences: const WorkbenchPreferences(
        language: AppLanguage.simplifiedChinese,
        section: WorkbenchSection.providerAccounts,
      ),
    );
    addTearDown(controller.dispose);
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('provider-accounts-search')),
      'sort-fixture',
    );
    await tester.pumpAndSettle();
    final alpha = find.byKey(
      const Key('provider-account-account.sort-fixture-alpha'),
    );
    final zulu = find.byKey(
      const Key('provider-account-account.sort-fixture-zulu'),
    );
    expect(tester.getTopLeft(zulu).dy, lessThan(tester.getTopLeft(alpha).dy));
    expect(find.textContaining('25%'), findsNWidgets(2));

    await tester.tap(find.byKey(const Key('provider-accounts-sort')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('按账号排序').last);
    await tester.pumpAndSettle();
    expect(tester.getTopLeft(alpha).dy, lessThan(tester.getTopLeft(zulu).dy));
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
    controller.dispose();
  });
}

// Optional local review artifacts, never a required golden baseline or secret.
Future<void> _review(
  WidgetTester tester,
  GlobalKey key,
  String? directory,
  String name,
) async {
  if (directory == null) return;
  await tester.runAsync(() async {
    final boundary =
        key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage();
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    await Directory(directory).create(recursive: true);
    await File(
      '$directory/$name.png',
    ).writeAsBytes(bytes!.buffer.asUint8List());
    image.dispose();
  });
}

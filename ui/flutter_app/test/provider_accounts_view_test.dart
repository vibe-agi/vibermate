import 'dart:io';
import 'dart:ui' as ui;

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
  final reviewDirectory = Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
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

  for (final width in [390.0, 1180.0]) {
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
        expect(find.text('尚未关联 · 前往上游服务选择使用此账号'), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
        controller.dispose();
      },
    );
  }
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

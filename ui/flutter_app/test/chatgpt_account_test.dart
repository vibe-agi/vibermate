import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/endpoints_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final isChatGPT in [true, false]) {
      testWidgets('account guidance $language ChatGPT=$isChatGPT', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(
          Size(language == AppLanguage.english ? 1050 : 390, 800),
        );
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi();
        addTearDown(api.close);
        final endpoint = await api.createUpstreamEndpoint(
          id: 'target.test',
          displayName: 'Test service',
          origin: isChatGPT ? 'https://chatgpt.com' : 'https://api.openai.com',
          backendProtocols: ['openai_responses'],
        );
        await api.createProviderAccount(
          id: 'account.test',
          displayName: 'Test account',
          upstreamEndpointId: endpoint.id,
          kind: 'bearer_token',
          secret: 'synthetic-not-a-real-token',
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        await controller.initialize();
        controller.selectEndpoint(endpoint.id);
        final copy = AppCopy.forLanguage(language);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: EndpointsView(controller: controller, copy: copy),
            ),
          ),
        );
        await tester.pumpAndSettle();
        for (final action in ['accounts-add', 'account-update-account.test']) {
          final button = find.byKey(Key(action));
          await tester.ensureVisible(button);
          await tester.tap(button);
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('account-editor-chatgpt-hint')),
            isChatGPT ? findsOneWidget : findsNothing,
          );
          if (isChatGPT) {
            expect(
              find.text(copy('routes.account.chatgpt_hint')),
              findsOneWidget,
            );
          }
          final secret = tester.widget<TextFormField>(
            find.byKey(const Key('account-editor-secret')),
          );
          expect(secret.controller!.text, isEmpty);
          expect(find.text('synthetic-not-a-real-token'), findsNothing);
          expect(tester.takeException(), isNull);
          await tester.sendKeyEvent(LogicalKeyboardKey.escape);
          await tester.pumpAndSettle();
        }
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
        controller.dispose();
      });
    }
  }
}

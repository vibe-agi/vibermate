import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/code_library_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('empty Code Library exposes editable built-in examples', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    addTearDown(api.close);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: CodeLibraryView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('code-library-starter-localPaths')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('code-library-starter-turnTime')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('code-library-starter-systemPrompt')),
      findsOneWidget,
    );
    expect(find.textContaining('runtime.user.homeDirectory'), findsOneWidget);
    expect((await api.codeLibrary()).transforms, isEmpty);

    await tester.tap(find.byKey(const Key('code-library-starter-localPaths')));
    await tester.pumpAndSettle();
    expect(find.text('New collection'), findsOneWidget);
    await tester.enterText(
      find.byKey(const Key('code-library-name')),
      'Privacy',
    );
    await tester.tap(find.byKey(const Key('code-library-name-save')));
    await tester.pumpAndSettle();

    final name = tester.widget<TextField>(
      find.byKey(const Key('code-library-transform-name')),
    );
    expect(name.controller?.text, 'Hide local paths');
    await tester.tap(find.byKey(const Key('code-library-transform-next')));
    await tester.pumpAndSettle();
    final request = tester.widget<TextField>(
      find.byKey(const Key('environment-transform-request-new-transform')),
    );
    expect(request.controller?.text, contains('runtime.user.homeDirectory'));
    expect(tester.takeException(), isNull);
  });

  testWidgets('390px Code Library publishes a new immutable revision', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    addTearDown(api.close);
    controller.capturedMessageTransformSample =
        const CapturedMessageTransformSample(
          exchangeId: 'exchange-private-sample',
          wireProtocol: 'anthropic_messages',
          sample: MessageTransformTestSample(
            request: MessageTransformTestRequest(
              method: 'POST',
              path: '/v1/messages',
              headers: {
                'content-type': ['application/json'],
              },
              body: '{"prompt":"private"}',
            ),
            response: MessageTransformTestResponse(
              statusCode: 200,
              streaming: false,
              headers: {
                'content-type': ['application/json'],
              },
              body: '{"type":"message"}',
            ),
          ),
        );

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: CodeLibraryView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('code-library-captured-sample')),
      findsOneWidget,
    );
    expect(find.textContaining('exchange-private-sample'), findsOneWidget);

    await tester.tap(find.byKey(const Key('code-library-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('New collection'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('code-library-name')),
      'Privacy',
    );
    await tester.tap(find.byKey(const Key('code-library-name-save')));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('code-library-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('New transform'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('code-library-transform-name')),
      'Home redaction',
    );
    await tester.tap(find.byKey(const Key('code-library-transform-next')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-transform-sample-new-transform')),
      findsOneWidget,
    );
    await tester.enterText(
      find.byKey(const Key('environment-transform-request-new-transform')),
      'request.body = request.body.replaceAll("/Users/mira", "/Users/guest");',
    );
    await tester.tap(
      find.byKey(const Key('environment-transform-save-new-transform')),
    );
    await tester.pumpAndSettle();

    final first = (await api.codeLibrary()).transforms.single;
    expect(first.revision, 1);
    expect(find.text('Home redaction'), findsWidgets);
    expect(first.policy.requestJavaScript, contains('/Users/guest'));
    expect(find.byKey(Key('code-library-detail-${first.id}')), findsOneWidget);
    final editAction = find.byKey(const Key('code-library-edit'));
    expect(find.byKey(const Key('code-library-test-format')), findsNothing);
    await tester.binding.setSurfaceSize(const Size(1000, 760));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await tester.tap(editAction);
    await tester.pumpAndSettle();
    final testFormat = find.byKey(
      Key('environment-transform-test-format-${first.id}'),
    );
    final runSample = find.byKey(Key('environment-transform-test-${first.id}'));
    expect(testFormat, findsOneWidget);
    expect(find.text('Test format · Anthropic Messages'), findsOneWidget);
    expect(
      find.byTooltip(
        'Only selects the built-in request and response used by Run sample Turn. It does not bind this code to a protocol.',
      ),
      findsOneWidget,
    );
    expect(tester.getSize(testFormat).height, tester.getSize(runSample).height);
    expect(tester.getTopLeft(testFormat).dy, tester.getTopLeft(runSample).dy);
    await tester.tap(
      find.byKey(Key('environment-transform-tab-response-${first.id}')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(Key('environment-transform-response-${first.id}')),
      'response.body = response.body.replaceAll("/Users/guest", "/Users/mira");',
    );
    await tester.tap(find.byKey(Key('environment-transform-save-${first.id}')));
    await tester.pumpAndSettle();

    final latest = (await api.codeLibrary()).transforms.single;
    final historical = await api.codeLibraryTransformRevision(first.id, 1);
    expect(latest.revision, 2);
    expect(latest.policy.responseJavaScript, contains('/Users/mira'));
    expect(historical.policy.responseJavaScript, isEmpty);
    expect(tester.takeException(), isNull);
  });

  testWidgets('starter examples open as ordinary editable transforms', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);
    addTearDown(api.close);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: CodeLibraryView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('code-library-add')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('New collection'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('code-library-name')),
      'Examples',
    );
    await tester.tap(find.byKey(const Key('code-library-name-save')));
    await tester.pumpAndSettle();

    Future<void> openStarter(String label) async {
      await tester.tap(find.byKey(const Key('code-library-add')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('New transform'));
      await tester.pumpAndSettle();
      expect(find.text('Starter protocol'), findsOneWidget);
      await tester.enterText(
        find.byKey(const Key('code-library-transform-name')),
        label,
      );
      await tester.tap(find.byKey(const Key('code-library-transform-starter')));
      await tester.pumpAndSettle();
      expect(find.text('Blank'), findsWidgets);
      expect(find.text('Hide local paths'), findsWidgets);
      expect(find.text('Show Turn time'), findsWidgets);
      expect(find.text('Adjust system prompt'), findsWidgets);
      await tester.tap(find.text(label).last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('code-library-transform-next')));
      await tester.pumpAndSettle();
    }

    await openStarter('Hide local paths');
    final privacyRequest = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-request-new-transform')),
        )
        .controller!
        .text;
    expect(privacyRequest, contains('runtime.user.homeDirectory'));
    expect(privacyRequest, contains('context.privateHome'));
    await tester.tap(
      find.byKey(const Key('environment-transform-tab-response-new-transform')),
    );
    await tester.pumpAndSettle();
    final privacyResponse = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-response-new-transform')),
        )
        .controller!
        .text;
    expect(privacyResponse, contains('context.privateHome'));
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();

    await openStarter('Show Turn time');
    await tester.tap(
      find.byKey(const Key('environment-transform-tab-response-new-transform')),
    );
    await tester.pumpAndSettle();
    final timeResponse = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-response-new-transform')),
        )
        .controller!
        .text;
    expect(timeResponse, contains('runtime.annotations.create'));
    expect(timeResponse, contains('runtime.turn.startedAt'));
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();

    await openStarter('Adjust system prompt');
    final systemRequest = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-request-new-transform')),
        )
        .controller!
        .text;
    expect(systemRequest, contains('payload.system'));
    expect(tester.takeException(), isNull);
  });
}

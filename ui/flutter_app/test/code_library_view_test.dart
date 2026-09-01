import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/account_selector_editor.dart';
import 'package:vibermate_app/features/workbench/code_library_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

import 'code_library_contract.dart';

const _protocolLabels = <String, String>{
  'anthropic_messages': 'Anthropic Messages',
  'openai_responses': 'OpenAI Responses',
  'openai_chat': 'OpenAI Chat',
};

const _transformLabels = <String, String>{
  'localIdentity': 'Hide local identity',
  'blockSecrets': 'Block secret leakage',
  'privateContacts': 'Hide email and private IP',
  'turnTime': 'Show Turn time',
  'replyLanguage': 'Set default reply language',
  'workspaceRules': 'Apply Workspace rules',
  'responseModel': 'Show actual response model',
};

void main() {
  for (final starter in transformSourceContracts.entries) {
    for (final protocol in starter.value.entries) {
      testWidgets(
        '${starter.key} exposes exact ${protocol.key} source in the public editor',
        (tester) async {
          await tester.binding.setSurfaceSize(const Size(820, 760));
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
          await tester.tap(
            find.byKey(const Key('code-library-create-transform-menu')),
          );
          await tester.pumpAndSettle();
          await tester.enterText(
            find.byKey(const Key('code-library-transform-name')),
            '${starter.key}-${protocol.key}',
          );
          await tester.tap(
            find.byKey(const Key('code-library-transform-protocol')),
          );
          await tester.pumpAndSettle();
          await tester.tap(find.text(_protocolLabels[protocol.key]!).last);
          await tester.pumpAndSettle();
          await tester.tap(
            find.byKey(const Key('code-library-transform-starter')),
          );
          await tester.pumpAndSettle();
          await tester.tap(find.text(_transformLabels[starter.key]!).last);
          await tester.pumpAndSettle();
          await tester.tap(
            find.byKey(const Key('code-library-transform-next')),
          );
          await tester.pumpAndSettle();

          final request = tester.widget<TextField>(
            find.byKey(
              const Key('environment-transform-request-new-transform'),
            ),
          );
          expect(request.controller?.text, protocol.value.request);
          await tester.tap(
            find.byKey(
              const Key('environment-transform-tab-response-new-transform'),
            ),
          );
          await tester.pumpAndSettle();
          final response = tester.widget<TextField>(
            find.byKey(
              const Key('environment-transform-response-new-transform'),
            ),
          );
          expect(response.controller?.text, protocol.value.response);
          expect(tester.takeException(), isNull);
        },
      );
    }
  }

  for (final languageCase in [
    (
      language: AppLanguage.english,
      title: 'Script library',
      transforms: 'Message transforms',
      selectors: 'Account selection rules',
      createTransform: 'New message transform',
      createSelector: 'New account selection rule',
      createFromExample: 'Create from example',
    ),
    (
      language: AppLanguage.simplifiedChinese,
      title: '脚本库',
      transforms: '消息变换',
      selectors: '账号选择规则',
      createTransform: '新建消息变换',
      createSelector: '新建账号选择规则',
      createFromExample: '以此新建',
    ),
  ]) {
    testWidgets(
      '${languageCase.language.name} Script library names objects and effects',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1000, 760));
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
                copy: AppCopy.forLanguage(languageCase.language),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();

        expect(find.text(languageCase.title), findsOneWidget);
        expect(find.text(languageCase.transforms), findsOneWidget);
        expect(find.text(languageCase.selectors), findsOneWidget);
        expect(find.text(languageCase.createFromExample), findsNWidgets(7));
        await tester.tap(find.byKey(const Key('code-library-add')));
        await tester.pumpAndSettle();
        expect(find.text(languageCase.createTransform), findsOneWidget);
        expect(find.text(languageCase.createSelector), findsOneWidget);
        await tester.sendKeyEvent(LogicalKeyboardKey.escape);
        await tester.pumpAndSettle();
        await tester.tap(find.text(languageCase.selectors));
        await tester.pumpAndSettle();
        expect(find.text(languageCase.createFromExample), findsOneWidget);
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('Script library keeps raw failures behind technical details', (
    tester,
  ) async {
    final controller = WorkbenchController(
      api: _FailingCodeLibraryApi(),
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      closeRuntime: () async {},
    );
    addTearDown(controller.dispose);

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
      find.text(
        'Script library could not be loaded. Published revisions were not changed. Try again.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('library-secret'), findsNothing);
    await tester.tap(find.text('Technical details'));
    await tester.pumpAndSettle();
    expect(find.textContaining('library-secret'), findsOneWidget);
  });

  testWidgets(
    'wide built-in examples align exact selectable highlighted source',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
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

      final actions = [
        find.byKey(const Key('code-library-starter-localIdentity')),
        find.byKey(const Key('code-library-starter-blockSecrets')),
        find.byKey(const Key('code-library-starter-privateContacts')),
        find.byKey(const Key('code-library-starter-turnTime')),
        find.byKey(const Key('code-library-starter-replyLanguage')),
        find.byKey(const Key('code-library-starter-workspaceRules')),
        find.byKey(const Key('code-library-starter-responseModel')),
      ];
      expect(actions, everyElement(findsOneWidget));
      expect(
        actions.take(3).map((finder) => tester.getTopLeft(finder).dy).toSet(),
        hasLength(1),
      );

      final source = find.byKey(
        const Key('code-library-starter-source-localIdentity'),
      );
      expect(source, findsOneWidget);
      expect(
        find.descendant(of: source, matching: find.byType(SelectionArea)),
        findsOneWidget,
      );
      final richText = tester.widget<RichText>(
        find.descendant(of: source, matching: find.byType(RichText)),
      );
      expect(richText.text.toPlainText(), localIdentityContract.request);
      expect(richText.overflow, TextOverflow.clip);
      final colors = <Color>{};
      richText.text.visitChildren((span) {
        if (span.style?.color case final color?) colors.add(color);
        return true;
      });
      expect(colors.length, greaterThanOrEqualTo(3));
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'account-selection cards expose exact selectable highlighted source',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
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
      final copy = AppCopy.forLanguage(AppLanguage.english);

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: CodeLibraryView(controller: controller, copy: copy),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text(copy('code_library.kind.account_selectors')));
      await tester.pumpAndSettle();

      expect(
        find.byKey(const Key('code-library-selector-starter-loginUser')),
        findsOneWidget,
      );
      for (final contract in accountSelectorSourceContracts.entries) {
        final source = find.byKey(
          Key('code-library-selector-starter-source-${contract.key}'),
        );
        expect(source, findsOneWidget);
        expect(
          find.descendant(of: source, matching: find.byType(SelectionArea)),
          findsOneWidget,
        );
        final richText = tester.widget<RichText>(
          find.descendant(of: source, matching: find.byType(RichText)),
        );
        expect(richText.text.toPlainText(), contract.value);
      }
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'local-path example preserves exact source through publish and reopen',
    (tester) async {
      String? copiedSource;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedSource =
                (call.arguments as Map<Object?, Object?>)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1000, 760));
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

      await tester.tap(
        find.byKey(const Key('code-library-starter-localIdentity')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('code-library-transform-next')));
      await tester.pumpAndSettle();

      final request = tester.widget<TextField>(
        find.byKey(const Key('environment-transform-request-new-transform')),
      );
      expect(request.controller?.text, localIdentityContract.request);
      await tester.tap(
        find.byKey(
          const Key('environment-transform-tab-response-new-transform'),
        ),
      );
      await tester.pumpAndSettle();
      final response = tester.widget<TextField>(
        find.byKey(const Key('environment-transform-response-new-transform')),
      );
      expect(response.controller?.text, localIdentityContract.response);

      await tester.tap(
        find.byKey(const Key('environment-transform-save-new-transform')),
      );
      await tester.pumpAndSettle();

      final published = (await api.codeLibrary()).transforms.single;
      final reopened = await api.codeLibraryTransformRevision(published.id, 1);
      expect(published.policy.requestJavaScript, localIdentityContract.request);
      expect(
        published.policy.responseJavaScript,
        localIdentityContract.response,
      );
      expect(reopened.policy, published.policy);

      final readOnlyRequest = tester
          .widgetList<SelectableText>(find.byType(SelectableText))
          .where(
            (widget) =>
                widget.textSpan?.toPlainText() == localIdentityContract.request,
          );
      expect(readOnlyRequest, hasLength(1));
      final colors = <Color>{};
      readOnlyRequest.single.textSpan!.visitChildren((span) {
        if (span.style?.color case final color?) colors.add(color);
        return true;
      });
      expect(colors.length, greaterThanOrEqualTo(3));
      await tester.tap(find.byTooltip('Copy Request'));
      await tester.pump();
      expect(copiedSource, localIdentityContract.request);
      expect(tester.takeException(), isNull);
    },
  );

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
      find.byKey(const Key('code-library-starter-localIdentity')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('code-library-starter-turnTime')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('code-library-starter-replyLanguage')),
      findsOneWidget,
    );
    expect(find.textContaining('runtime.user.homeDirectory'), findsOneWidget);
    expect((await api.codeLibrary()).transforms, isEmpty);

    await tester.tap(
      find.byKey(const Key('code-library-starter-localIdentity')),
    );
    await tester.pumpAndSettle();
    expect(find.text('New collection'), findsNothing);

    final name = tester.widget<TextField>(
      find.byKey(const Key('code-library-transform-name')),
    );
    expect(name.controller?.text, 'Hide local identity');
    await tester.tap(find.byKey(const Key('code-library-transform-next')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('message-transform-editor-page')),
      findsOneWidget,
    );
    expect(find.text('Create and publish'), findsOneWidget);
    expect(find.byType(Dialog), findsNothing);
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
    await tester.tap(find.text('New message transform'));
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
    expect(find.text('Publish new revision'), findsOneWidget);
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
    Future<void> openStarter(String label) async {
      await tester.tap(find.byKey(const Key('code-library-add')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('New message transform'));
      await tester.pumpAndSettle();
      expect(find.text('Starter protocol'), findsOneWidget);
      await tester.enterText(
        find.byKey(const Key('code-library-transform-name')),
        label,
      );
      await tester.tap(find.byKey(const Key('code-library-transform-starter')));
      await tester.pumpAndSettle();
      expect(find.text('Blank'), findsWidgets);
      expect(find.text('Hide local identity'), findsWidgets);
      expect(find.text('Block secret leakage'), findsWidgets);
      expect(find.text('Hide email and private IP'), findsWidgets);
      expect(find.text('Show Turn time'), findsWidgets);
      expect(find.text('Set default reply language'), findsWidgets);
      expect(find.text('Apply Workspace rules'), findsWidgets);
      expect(find.text('Show actual response model'), findsWidgets);
      await tester.tap(find.text(label).last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('code-library-transform-next')));
      await tester.pumpAndSettle();
    }

    await openStarter('Hide local identity');
    final privacyRequest = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-request-new-transform')),
        )
        .controller!
        .text;
    expect(privacyRequest, contains('runtime.user.homeDirectory'));
    expect(privacyRequest, contains('context.redactions'));
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
    expect(privacyResponse, contains('context.redactions'));
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

    await openStarter('Set default reply language');
    final systemRequest = tester
        .widget<TextField>(
          find.byKey(const Key('environment-transform-request-new-transform')),
        )
        .controller!
        .text;
    expect(systemRequest, contains('payload.system'));
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'account selection rule sample uses the exact read-only Turn contract',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(820, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      AccountSelectorTestSample? observed;

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: AccountSelectorEditorDialog(
              selectorId: 'selector.test',
              initial: const AccountSelectorPolicy(
                javaScript: 'selection.accountId = accounts[0].id;',
              ),
              copy: AppCopy.forLanguage(AppLanguage.english),
              testSelector: ({required policy, required sample}) async {
                observed = sample;
                return AccountSelectorTestResult(
                  accountId: sample.accounts.last.id,
                );
              },
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      await tester.scrollUntilVisible(
        find.byKey(const Key('account-selector-suggestion-6')),
        120,
        scrollable: find.descendant(
          of: find.byKey(const Key('account-selector-suggestions')),
          matching: find.byType(Scrollable),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.text('request.protocol'), findsOneWidget);
      await tester.tap(find.byKey(const Key('account-selector-sample')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('account-selector-sample-accounts')),
        'account.red, account.blue',
      );
      await tester.enterText(
        find.byKey(const Key('account-selector-sample-workspace')),
        'blue-workspace',
      );
      await tester.tap(
        find.byKey(const Key('account-selector-test-selector.test')),
      );
      await tester.pumpAndSettle();

      expect(observed?.accounts.map((account) => account.id), [
        'account.red',
        'account.blue',
      ]);
      expect(observed?.runtime.workspaceLabel, 'blue-workspace');
      expect(observed?.request.clientProtocol, 'anthropic_messages');
      expect(find.text('Selected account.blue'), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('built-in ViberMate login selector passes its default sample', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(820, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: AccountSelectorEditorDialog(
            selectorId: 'selector.user',
            initial: const AccountSelectorPolicy(
              javaScript: '''const accountByLogin = {
  "alice": "account.team-a",
  "bob": "account.team-b",
};
selection.accountId = accountByLogin[runtime.login.username];''',
            ),
            copy: AppCopy.forLanguage(AppLanguage.english),
            testSelector: ({required policy, required sample}) async {
              if (!sample.accounts.any(
                (account) => account.id == 'account.team-a',
              )) {
                throw StateError('account.team-a is absent from the sample');
              }
              expect(sample.runtime.loginUsername, 'alice');
              expect(sample.runtime.userName, 'local-user');
              return const AccountSelectorTestResult(
                accountId: 'account.team-a',
              );
            },
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(
      find.byKey(const Key('account-selector-test-selector.user')),
    );
    await tester.pumpAndSettle();

    expect(find.text('Selected account.team-a'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('expanded account selection sample scrolls above fixed actions', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(820, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: AccountSelectorEditorDialog(
            selectorId: 'selector.layout',
            initial: const AccountSelectorPolicy(
              javaScript: 'selection.accountId = accounts[0].id;',
            ),
            copy: AppCopy.forLanguage(AppLanguage.english),
            testSelector: ({required policy, required sample}) async =>
                AccountSelectorTestResult(accountId: sample.accounts.first.id),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('account-selector-body-scroll')),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('account-selector-sample')));
    await tester.pumpAndSettle();

    final protocol = find.byKey(const Key('account-selector-sample-protocol'));
    await tester.ensureVisible(protocol);
    await tester.pumpAndSettle();

    expect(protocol.hitTestable(), findsOneWidget);
    expect(
      find
          .byKey(const Key('account-selector-save-selector.layout'))
          .hitTestable(),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('390px Script library publishes account selection revisions', (
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

    await tester.tap(find.text('Account selection rules'));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('code-library-selector-starter-loginUser')),
      findsOneWidget,
    );
    expect(find.textContaining('runtime.login.username'), findsOneWidget);

    final loginStarter = find.byKey(
      const Key('code-library-selector-starter-loginUser'),
    );
    await tester.ensureVisible(loginStarter);
    await tester.tap(loginStarter);
    await tester.pumpAndSettle();
    expect(find.text('New collection'), findsNothing);
    final starterName = tester.widget<TextField>(
      find.byKey(const Key('code-library-selector-name')),
    );
    expect(starterName.controller?.text, 'Choose by ViberMate login');
    await tester.enterText(
      find.byKey(const Key('code-library-selector-name')),
      'Login account',
    );
    await tester.tap(find.byKey(const Key('code-library-selector-next')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('account-selector-editor-page')),
      findsOneWidget,
    );
    expect(find.text('Create and publish'), findsOneWidget);
    expect(find.byType(Dialog), findsNothing);
    expect(
      find.byKey(const Key('account-selector-source-new-selector')),
      findsOneWidget,
    );
    final source = tester.widget<TextField>(
      find.byKey(const Key('account-selector-source-new-selector')),
    );
    expect(source.controller?.text, contains('runtime.login.username'));
    await tester.tap(
      find.byKey(const Key('account-selector-save-new-selector')),
    );
    await tester.pumpAndSettle();

    final first = (await api.codeLibrary()).accountSelectors.single;
    expect(first.revision, 1);
    expect(first.displayName, 'Login account');
    expect(
      find.byKey(Key('code-library-selector-detail-${first.id}')),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('code-library-selector-edit')));
    await tester.pumpAndSettle();
    expect(find.text('Publish new revision'), findsOneWidget);
    await tester.enterText(
      find.byKey(Key('account-selector-source-${first.id}')),
      'selection.accountId = accounts[accounts.length - 1].id;',
    );
    await tester.tap(find.byKey(Key('account-selector-save-${first.id}')));
    await tester.pumpAndSettle();

    final latest = (await api.codeLibrary()).accountSelectors.single;
    final historical = await api.codeLibraryAccountSelectorRevision(
      first.id,
      1,
    );
    expect(latest.revision, 2);
    expect(latest.policy.javaScript, contains('accounts.length - 1'));
    expect(historical.policy.javaScript, isNot(contains('accounts.length')));
    expect(tester.takeException(), isNull);
  });
}

final class _FailingCodeLibraryApi implements ControlApi {
  @override
  Future<CodeLibraryCatalog> codeLibrary() =>
      Future.error(StateError('library-secret'));

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

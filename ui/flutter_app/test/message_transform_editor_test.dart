import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/message_transform_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  testWidgets(
    '390px editor keeps request and response scripts in one tested Turn',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      TrafficTransformPolicy? saved;
      TrafficTransformPolicy? tested;
      MessageTransformTestSample? testedSample;

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: Builder(
              builder: (context) => TextButton(
                key: const Key('open-transform-editor'),
                onPressed: () async {
                  saved = await Navigator.of(context)
                      .push<TrafficTransformPolicy>(
                        MaterialPageRoute(
                          builder: (context) => MessageTransformEditorDialog(
                            planId: 'plan-a',
                            displayName: '隐藏本机身份',
                            baseRevision: 3,
                            wireProtocol: 'anthropic_messages',
                            initial: const TrafficTransformPolicy.disabled(),
                            copy: copy,
                            testTransform:
                                ({
                                  required wireProtocol,
                                  required policy,
                                  sample,
                                }) async {
                                  tested = policy;
                                  testedSample = sample;
                                  return const MessageTransformTestResult(
                                    wireProtocol: 'anthropic_messages',
                                    requestBefore: MessageTransformTestRequest(
                                      method: 'POST',
                                      path: '/v1/messages',
                                      headers: {
                                        'content-type': ['application/json'],
                                      },
                                      body: '{"model":"sample-client"}',
                                    ),
                                    requestAfter: MessageTransformTestRequest(
                                      method: 'POST',
                                      path: '/v1/messages',
                                      headers: {
                                        'content-type': ['application/json'],
                                        'x-sample': ['request-ok'],
                                      },
                                      body: '{"model":"sample-upstream"}',
                                    ),
                                    responseBefore:
                                        MessageTransformTestResponse(
                                          statusCode: 200,
                                          streaming: false,
                                          headers: {
                                            'content-type': [
                                              'application/json',
                                            ],
                                          },
                                          body: '{"type":"message"}',
                                        ),
                                    responseAfter: MessageTransformTestResponse(
                                      statusCode: 200,
                                      streaming: false,
                                      headers: {
                                        'content-type': ['application/json'],
                                        'x-sample': ['response-ok'],
                                      },
                                      body: '{"type":"message"}',
                                    ),
                                  );
                                },
                          ),
                        ),
                      );
                },
                child: const Text('open'),
              ),
            ),
          ),
        ),
      );

      await tester.tap(find.byKey(const Key('open-transform-editor')));
      await tester.pumpAndSettle();

      final dialog = find.byKey(
        const Key('environment-transform-dialog-plan-a'),
      );
      expect(dialog, findsOneWidget);
      expect(
        find.byKey(const Key('message-transform-editor-page')),
        findsOneWidget,
      );
      expect(tester.getSize(dialog).width, 390);
      expect(find.text('隐藏本机身份'), findsOneWidget);
      expect(find.text('基于 r3 的草稿 · Anthropic Messages'), findsOneWidget);
      expect(find.text('消息变换'), findsNothing);
      expect(find.text('同一轮次上下文'), findsOneWidget);
      expect(
        find.byKey(const Key('environment-transform-sample-plan-a')),
        findsOneWidget,
      );

      await tester.enterText(
        find.byKey(const Key('environment-transform-request-plan-a')),
        'context.requestModel = JSON.parse(request.body).model;',
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-tab-response-plan-a')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-transform-response-plan-a')),
        'response.headers["x-sample"] = [context.requestModel];',
      );

      await tester.tap(
        find.byKey(const Key('environment-transform-test-plan-a')),
      );
      await tester.pumpAndSettle();
      expect(tested?.requestJavaScript, contains('request.body'));
      expect(tested?.responseJavaScript, contains('response.headers'));
      expect(testedSample?.request.path, '/v1/messages');
      expect(testedSample?.runtime?.userName, 'example-user');
      expect(testedSample?.runtime?.workspaceLabel, 'example');
      expect(
        find.byKey(const Key('environment-transform-request-diff')),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('environment-transform-response-diff')),
        findsOneWidget,
      );
      expect(find.text('请求 · 修改前 → 修改后'), findsOneWidget);
      expect(find.text('响应 · 修改前 → 修改后'), findsOneWidget);
      expect(find.textContaining('sample-client'), findsOneWidget);
      expect(find.textContaining('sample-upstream'), findsOneWidget);
      expect(find.textContaining('request-ok'), findsOneWidget);
      expect(find.textContaining('response-ok'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.tap(
        find.byKey(const Key('environment-transform-save-plan-a')),
      );
      await tester.pumpAndSettle();
      expect(saved, tested);
      expect(dialog, findsNothing);
    },
  );

  testWidgets('Escape closes only the message transform editor page', (
    tester,
  ) async {
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              key: const Key('open-transform-editor'),
              onPressed: () => Navigator.of(context).push<void>(
                MaterialPageRoute(
                  builder: (context) => MessageTransformEditorDialog(
                    planId: 'plan-a',
                    displayName: 'Response cleanup',
                    wireProtocol: 'openai_responses',
                    initial: const TrafficTransformPolicy.disabled(),
                    copy: copy,
                    testTransform:
                        ({required wireProtocol, required policy, sample}) =>
                            throw UnimplementedError(),
                  ),
                ),
              ),
              child: const Text('parent editor'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.byKey(const Key('open-transform-editor')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-transform-dialog-plan-a')),
      findsOneWidget,
    );

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('environment-transform-dialog-plan-a')),
      findsNothing,
    );
    expect(find.text('parent editor'), findsOneWidget);
  });

  testWidgets('captured sample is edited locally before the sample Turn runs', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final copy = AppCopy.forLanguage(AppLanguage.english);
    MessageTransformTestSample? testedSample;
    final initialSample = MessageTransformTestSample(
      request: const MessageTransformTestRequest(
        method: 'POST',
        path: '/v1/messages',
        headers: {
          'content-type': ['application/json'],
        },
        body: '{"prompt":"/Users/jack/private"}',
      ),
      response: const MessageTransformTestResponse(
        statusCode: 200,
        streaming: false,
        headers: {
          'content-type': ['application/json'],
        },
        body: '{"text":"/Users/guest/private"}',
      ),
      runtime: MessageTransformTestRuntime(
        userName: 'jack',
        homeDirectory: '/Users/jack',
        operatingSystem: 'darwin',
        operatingSystemVersion: '15.0',
        architecture: 'arm64',
        timeZone: 'Etc/UTC',
        workspaceRoot: '/Users/jack/private',
        workspaceLabel: 'private',
        turnStartedAt: DateTime.utc(2026, 1, 2, 3, 4, 5),
      ),
    );

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              key: const Key('open-transform-editor'),
              onPressed: () => Navigator.of(context).push<void>(
                MaterialPageRoute(
                  builder: (context) => MessageTransformEditorDialog(
                    planId: 'captured',
                    displayName: 'Hide local identity',
                    wireProtocol: 'anthropic_messages',
                    initial: const TrafficTransformPolicy.disabled(),
                    initialSample: initialSample,
                    copy: copy,
                    testTransform:
                        ({
                          required wireProtocol,
                          required policy,
                          sample,
                        }) async {
                          testedSample = sample;
                          return MessageTransformTestResult(
                            wireProtocol: wireProtocol,
                            requestBefore: sample!.request,
                            requestAfter: sample.request,
                            responseBefore: sample.response,
                            responseAfter: sample.response,
                          );
                        },
                  ),
                ),
              ),
              child: const Text('open'),
            ),
          ),
        ),
      ),
    );

    await tester.tap(find.byKey(const Key('open-transform-editor')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('environment-transform-sample-captured')),
    );
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-transform-sample-dialog-captured')),
      findsOneWidget,
    );

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('environment-transform-sample-dialog-captured')),
      findsNothing,
    );
    expect(
      find.byKey(const Key('environment-transform-dialog-captured')),
      findsOneWidget,
    );

    await tester.tap(
      find.byKey(const Key('environment-transform-sample-captured')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-transform-sample-request-body')),
      '{"prompt":"/Users/guest/private"}',
    );
    await tester.enterText(
      find.byKey(const Key('environment-transform-sample-request-headers')),
      '{"content-type":["application/json"],"x-user":["guest"]}',
    );
    await tester.tap(
      find.byKey(const Key('environment-transform-sample-tab-response')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-transform-sample-response-body')),
      '{"text":"/Users/guest/private"}',
    );
    await tester.tap(
      find.byKey(const Key('environment-transform-sample-tab-runtime')),
    );
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-transform-sample-runtime-user-name')),
      'guest',
    );
    await tester.enterText(
      find.byKey(
        const Key('environment-transform-sample-runtime-home-directory'),
      ),
      '/Users/guest',
    );
    await tester.enterText(
      find.byKey(
        const Key('environment-transform-sample-runtime-workspace-root'),
      ),
      '/workspace/project',
    );
    await tester.enterText(
      find.byKey(const Key('environment-transform-sample-runtime-time-zone')),
      'Asia/Singapore',
    );
    await tester.tap(
      find.byKey(const Key('environment-transform-sample-save')),
    );
    await tester.pumpAndSettle();

    await tester.tap(
      find.byKey(const Key('environment-transform-test-captured')),
    );
    await tester.pumpAndSettle();
    expect(testedSample?.request.body, contains('/Users/guest'));
    expect(testedSample?.request.body, isNot(contains('/Users/jack')));
    expect(testedSample?.request.headers['x-user'], ['guest']);
    expect(testedSample?.response.body, contains('/Users/guest'));
    expect(testedSample?.runtime?.userName, 'guest');
    expect(testedSample?.runtime?.homeDirectory, '/Users/guest');
    expect(testedSample?.runtime?.workspaceRoot, '/workspace/project');
    expect(testedSample?.runtime?.timeZone, 'Asia/Singapore');
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'user can replace built-in inputs with one real captured Exchange',
    (tester) async {
      final copy = AppCopy.forLanguage(AppLanguage.english);
      MessageTransformTestSample? testedSample;
      String? testedProtocol;
      final captured = CapturedMessageTransformSample(
        exchangeId: 'exchange-real-42',
        wireProtocol: 'openai_responses',
        sample: MessageTransformTestSample(
          request: const MessageTransformTestRequest(
            method: 'POST',
            path: '/v1/responses',
            headers: {
              'content-type': ['application/json'],
            },
            body: '{"input":"the real conversation"}',
          ),
          response: const MessageTransformTestResponse(
            statusCode: 200,
            streaming: false,
            headers: {
              'content-type': ['application/json'],
            },
            body: '{"output_text":"the real answer"}',
          ),
          runtime: MessageTransformTestRuntime(
            userName: 'mira',
            homeDirectory: '/Users/mira',
            operatingSystem: 'darwin',
            operatingSystemVersion: '26.0',
            architecture: 'arm64',
            timeZone: 'Asia/Singapore',
            workspaceRoot: '/Users/mira/Code/vibermate',
            workspaceLabel: 'vibermate',
            turnStartedAt: DateTime.utc(2026, 9, 1, 2, 3, 4),
          ),
        ),
      );

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: MessageTransformEditorDialog(
            planId: 'real-capture',
            displayName: 'Real capture test',
            wireProtocol: 'anthropic_messages',
            initial: const TrafficTransformPolicy.disabled(),
            copy: copy,
            pickCapturedSample: () async => captured,
            testTransform:
                ({required wireProtocol, required policy, sample}) async {
                  testedProtocol = wireProtocol;
                  testedSample = sample;
                  return MessageTransformTestResult(
                    wireProtocol: wireProtocol,
                    requestBefore: sample!.request,
                    requestAfter: sample.request,
                    responseBefore: sample.response,
                    responseAfter: sample.response,
                  );
                },
          ),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(
        find.byKey(const Key('environment-transform-sample-real-capture')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Built-in example data'), findsOneWidget);

      await tester.tap(
        find.byKey(const Key('environment-transform-sample-pick-captured')),
      );
      await tester.pumpAndSettle();
      expect(find.textContaining('exchange-real-42'), findsOneWidget);
      expect(
        tester
            .widget<TextField>(
              find.byKey(
                const Key('environment-transform-sample-request-body'),
              ),
            )
            .controller
            ?.text,
        contains('the real conversation'),
      );

      await tester.tap(
        find.byKey(const Key('environment-transform-sample-save')),
      );
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('environment-transform-test-real-capture')),
      );
      await tester.pumpAndSettle();

      expect(testedProtocol, 'openai_responses');
      expect(testedSample?.request.body, contains('the real conversation'));
      expect(testedSample?.response.body, contains('the real answer'));
      expect(testedSample?.runtime?.userName, 'mira');
      expect(find.text('Test passed'), findsOneWidget);
      expect(find.text('Sample Turn passed'), findsNothing);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('failed sample names the safe server stage and error code', (
    tester,
  ) async {
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              key: const Key('open-transform-editor'),
              onPressed: () => Navigator.of(context).push<void>(
                MaterialPageRoute(
                  builder: (context) => MessageTransformEditorDialog(
                    planId: 'failure',
                    displayName: 'Hide local identity',
                    baseRevision: 2,
                    wireProtocol: 'anthropic_messages',
                    initial: const TrafficTransformPolicy.disabled(),
                    copy: copy,
                    testTransform:
                        ({required wireProtocol, required policy, sample}) =>
                            throw const ControlProblem(
                              status: 422,
                              reasonCode: 'message_transform_test_failed',
                              messageKey: 'error.message_transform_test_failed',
                              detail: 'request · invalid JavaScript',
                            ),
                  ),
                ),
              ),
              child: const Text('open'),
            ),
          ),
        ),
      ),
    );

    await tester.tap(find.byKey(const Key('open-transform-editor')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('environment-transform-test-failure')),
    );
    await tester.pumpAndSettle();

    final error = find.byKey(const Key('environment-transform-error-failure'));
    expect(error, findsOneWidget);
    expect(
      find.descendant(
        of: error,
        matching: find.textContaining('message_transform_test_failed'),
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: error,
        matching: find.textContaining('request · invalid JavaScript'),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    '390px pipeline preserves and reorders exact published revisions',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      final alpha = _revision('alpha', 2);
      final beta = _revision('beta', 1);
      final gamma = _revision('gamma', 3);
      List<CodeLibraryTransformRevision>? saved;

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: Builder(
              builder: (context) => TextButton(
                key: const Key('open-transform-pipeline'),
                onPressed: () async {
                  saved = await showDialog<List<CodeLibraryTransformRevision>>(
                    context: context,
                    builder: (context) => MessageTransformPipelineDialog(
                      planId: 'plan-a',
                      initial: [alpha, beta],
                      copy: copy,
                      loadLibrary: () async => CodeLibraryCatalog(
                        collections: const [
                          CodeLibraryCollection(
                            id: 'tools',
                            displayName: 'Tools',
                          ),
                        ],
                        transforms: [alpha, beta, gamma],
                        accountSelectors: const [],
                      ),
                    ),
                  );
                },
                child: const Text('open'),
              ),
            ),
          ),
        ),
      );

      await tester.tap(find.byKey(const Key('open-transform-pipeline')));
      await tester.pumpAndSettle();
      final dialog = find.byKey(
        const Key('environment-transform-pipeline-dialog-plan-a'),
      );
      expect(tester.getSize(dialog).width, lessThanOrEqualTo(350));

      await tester.tap(
        find.byKey(const Key('environment-transform-pipeline-up-beta-1')),
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-pipeline-add-gamma-3')),
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-pipeline-save-plan-a')),
      );
      await tester.pumpAndSettle();

      expect(saved?.map((item) => item.id), ['beta', 'alpha', 'gamma']);
      expect(tester.takeException(), isNull);
    },
  );
}

CodeLibraryTransformRevision _revision(String id, int revision) =>
    CodeLibraryTransformRevision(
      id: id,
      revision: revision,
      collectionId: 'tools',
      displayName: id.toUpperCase(),
      policy: const TrafficTransformPolicy.disabled(),
      publishedAt: DateTime.utc(2026, 8, 27, revision),
    );

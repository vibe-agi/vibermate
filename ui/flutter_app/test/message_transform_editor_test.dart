import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
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

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: Builder(
              builder: (context) => TextButton(
                key: const Key('open-transform-editor'),
                onPressed: () async {
                  saved = await showDialog<TrafficTransformPolicy>(
                    context: context,
                    builder: (context) => MessageTransformEditorDialog(
                      planId: 'plan-a',
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
                              responseBefore: MessageTransformTestResponse(
                                statusCode: 200,
                                streaming: false,
                                headers: {
                                  'content-type': ['application/json'],
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
      expect(tester.getSize(dialog).width, lessThanOrEqualTo(350));
      expect(find.text('Anthropic Messages'), findsOneWidget);
      expect(find.text('同一 Turn 上下文'), findsOneWidget);

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

  testWidgets('Escape closes only the message transform child dialog', (
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
              onPressed: () => showDialog<void>(
                context: context,
                builder: (context) => MessageTransformEditorDialog(
                  planId: 'plan-a',
                  wireProtocol: 'openai_responses',
                  initial: const TrafficTransformPolicy.disabled(),
                  copy: copy,
                  testTransform:
                      ({required wireProtocol, required policy, sample}) =>
                          throw UnimplementedError(),
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
    final copy = AppCopy.forLanguage(AppLanguage.english);
    MessageTransformTestSample? testedSample;
    const initialSample = MessageTransformTestSample(
      request: MessageTransformTestRequest(
        method: 'POST',
        path: '/v1/messages',
        headers: {
          'content-type': ['application/json'],
        },
        body: '{"prompt":"/Users/jack/private"}',
      ),
      response: MessageTransformTestResponse(
        statusCode: 200,
        streaming: false,
        headers: {
          'content-type': ['application/json'],
        },
        body: '{"text":"/Users/guest/private"}',
      ),
    );

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              key: const Key('open-transform-editor'),
              onPressed: () => showDialog<void>(
                context: context,
                builder: (context) => MessageTransformEditorDialog(
                  planId: 'captured',
                  wireProtocol: 'anthropic_messages',
                  initial: const TrafficTransformPolicy.disabled(),
                  initialSample: initialSample,
                  copy: copy,
                  testTransform:
                      ({required wireProtocol, required policy, sample}) async {
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

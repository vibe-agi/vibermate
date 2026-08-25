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
                      clientProtocol: 'anthropic_messages',
                      initial: const TrafficTransformPolicy.disabled(),
                      copy: copy,
                      testTransform:
                          ({required clientProtocol, required policy}) async {
                            tested = policy;
                            return const MessageTransformTestResult(
                              clientProtocol: 'anthropic_messages',
                              request: MessageTransformTestRequest(
                                method: 'POST',
                                path: '/v1/messages',
                                headers: {
                                  'content-type': ['application/json'],
                                  'x-sample': ['request-ok'],
                                },
                                body: '{"model":"sample-upstream"}',
                              ),
                              response: MessageTransformTestResponse(
                                statusCode: 200,
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
      expect(find.textContaining('POST /v1/messages'), findsOneWidget);
      expect(find.textContaining('200\n'), findsOneWidget);
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
                  clientProtocol: 'openai_responses',
                  initial: const TrafficTransformPolicy.disabled(),
                  copy: copy,
                  testTransform: ({required clientProtocol, required policy}) =>
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
}

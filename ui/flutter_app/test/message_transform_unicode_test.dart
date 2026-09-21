import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/message_transform_editor.dart';

void main() {
  for (final protocol in [
    'openai_responses',
    'anthropic_messages',
    'openai_chat',
  ]) {
    test(
      '$protocol preserves literal replacement characters in test results',
      () {
        final sample = MessageTransformTestSample.example(
          protocol,
          userMessage: 'valid � 中文 🙂',
          assistantMessage: 'reply � 中文 🙂',
        );
        final result = MessageTransformTestResult.fromJson({
          'wireProtocol': protocol,
          'requestBefore': sample.request.toJson(),
          'requestAfter': sample.request.toJson(),
          'responseBefore': sample.response.toJson(),
          'responseAfter': sample.response.toJson(),
        }, r'$.result');
        expect(result.requestAfter.body, sample.request.body);
        expect(result.responseAfter.body, sample.response.body);
      },
    );
  }

  test(
    'transform bodies reject unpaired UTF-16 without rejecting valid pairs',
    () {
      for (final units in [
        [0xd800],
        [0xdfff],
        [0xdc00, 0xd800],
        [0xd800, 0x61],
        [0xfffd, 0xd800],
        [0xd83d, 0xde42, 0xd800],
      ]) {
        final body = String.fromCharCodes(units);
        expect(
          () => MessageTransformTestRequest.fromJson({
            'method': 'POST',
            'path': '/v1/responses',
            'headers': <String, Object?>{},
            'body': body,
          }, r'$.request'),
          throwsA(isA<ControlContractException>()),
        );
        expect(
          () => MessageTransformTestResponse.fromJson({
            'statusCode': 200,
            'streaming': true,
            'headers': <String, Object?>{},
            'body': body,
          }, r'$.response'),
          throwsA(isA<ControlContractException>()),
        );
      }
      final body = String.fromCharCodes([0xfffd, 0xd83d, 0xde42, 0xfffd]);
      expect(
        MessageTransformTestResponse.fromJson({
          'statusCode': 200,
          'streaming': true,
          'headers': <String, Object?>{},
          'body': body,
        }, r'$.response').body.codeUnits,
        body.codeUnits,
      );
    },
  );

  testWidgets(
    'sample dialog saves literal replacement characters and runs test',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1200, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      MessageTransformTestSample? tested;
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: MessageTransformEditorDialog(
            planId: 'unicode',
            displayName: 'Unicode regression',
            wireProtocol: 'openai_responses',
            initial: const TrafficTransformPolicy(
              requestJavaScript: 'request.body = request.body;',
              responseJavaScript: '',
            ),
            copy: AppCopy.forLanguage(AppLanguage.english),
            testTransform:
                ({required wireProtocol, required policy, sample}) async {
                  tested = sample;
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
        find.byKey(const Key('environment-transform-sample-unicode')),
      );
      await tester.pumpAndSettle();
      const request = '{"input":"valid � 中文 🙂"}';
      const response = '{"output":"reply � 中文 🙂"}';
      await tester.enterText(
        find.byKey(const Key('environment-transform-sample-request-body')),
        request,
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-sample-tab-response')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-transform-sample-response-body')),
        response,
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-sample-save')),
      );
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-transform-sample-dialog-unicode')),
        findsNothing,
      );
      await tester.tap(
        find.byKey(const Key('environment-transform-test-unicode')),
      );
      await tester.pumpAndSettle();
      expect(tested?.request.body, request);
      expect(tested?.response.body, response);
      expect(find.text('Test passed'), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );
}

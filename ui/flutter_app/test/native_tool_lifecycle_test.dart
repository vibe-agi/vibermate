import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/native_tool_reader.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'native_tool_approval_test.dart'
    show nativeApi, nativeController, nativeResponse;
import 'native_tool_reader_test.dart'
    show
        nativeOperationFixture,
        nativeOperationID,
        nativeTextResponse,
        nativePNG;

const _otherID = '00000000000000000000000000000003';

void main() {
  for (final invalidation in [
    'expired',
    'deleted',
    'epoch',
    'capture',
    'dispose',
    'controller_replace',
  ]) {
    testWidgets('enlarged native image drops bytes on $invalidation', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      var expired = false;
      final api = await nativeApi((request) async {
        final op = nativeOperationFixture();
        if (expired &&
            invalidation == 'deleted' &&
            request.url.path.endsWith('/$nativeOperationID')) {
          return _problem(404);
        }
        if (expired) {
          (op['content'] as Map)['result'] = {
            'state': 'expired',
            'digest': 'c' * 64,
            'expiresAt': '2025-01-01T00:00:00Z',
          };
        }
        if (request.url.path.endsWith('/operations')) {
          return nativeResponse({
            'items': expired && invalidation == 'deleted' ? [] : [op],
            'nextSequence': 7,
          });
        }
        if (request.url.path.contains('/content')) {
          return nativeResponse({
            'operationId': nativeOperationID,
            'captureKey': op['captureKey'],
            'part': 'tool_result',
            'bodyDigest': 'c' * 64,
            'blocks': [
              {
                'kind': 'image',
                'mimeType': 'image/png',
                'imageData': nativePNG,
                'offset': 0,
                'total': nativePNG.length,
                'complete': true,
              },
            ],
          });
        }
        return nativeResponse(op);
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      var activeController = controller;
      final replacement = nativeController(PreviewControlApi());
      addTearDown(replacement.dispose);
      late VoidCallback replaceController;
      await tester.runAsync(() async {
        controller.setNativeReaderVisible(true);
        while (controller.nativeOperationsLoading) {
          await Future<void>.delayed(Duration.zero);
        }
        await controller.loadNativeToolContent(
          controller.nativeOperations.single,
          'tool_result',
        );
      });
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light().copyWith(
            splashFactory: NoSplash.splashFactory,
          ),
          home: MediaQuery(
            data: const MediaQueryData(textScaler: TextScaler.linear(2)),
            child: Scaffold(
              body: SingleChildScrollView(
                child: StatefulBuilder(
                  builder: (context, setState) {
                    replaceController = () =>
                        setState(() => activeController = replacement);
                    return NativeToolReader(
                      controller: activeController,
                      copy: AppCopy.forLanguage(AppLanguage.english),
                    );
                  },
                ),
              ),
            ),
          ),
        ),
      );
      await tester.tap(
        find.byKey(const Key('native-operation-$nativeOperationID')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('View image'));
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: find.byType(InteractiveViewer),
          matching: find.byType(Image),
        ),
        findsOneWidget,
      );
      if (invalidation == 'expired' || invalidation == 'deleted') {
        expired = true;
        await tester.runAsync(controller.refreshNativeTools);
      } else if (invalidation == 'capture') {
        controller.selectedCaptureKey = 'manual_capture:other';
        controller.notifyListeners();
      } else if (invalidation == 'dispose') {
        await tester.runAsync(() async {
          controller.dispose();
          await Future<void>.delayed(Duration.zero);
        });
      } else if (invalidation == 'controller_replace') {
        replaceController();
      } else {
        controller.setNativeReaderVisible(false);
      }
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: find.byType(InteractiveViewer),
          matching: find.byType(Image),
        ),
        findsNothing,
      );
      if (invalidation != 'dispose' && invalidation != 'controller_replace') {
        expired = false;
        controller.selectedCaptureKey =
            nativeOperationFixture()['captureKey'] as String;
        await tester.runAsync(() async {
          controller.setNativeReaderVisible(true);
          while (controller.nativeOperationsLoading) {
            await Future<void>.delayed(Duration.zero);
          }
          await controller.refreshNativeTools();
        });
        await tester.pumpAndSettle();
        expect(
          find.descendant(
            of: find.byType(InteractiveViewer),
            matching: find.byType(Image),
          ),
          findsNothing,
        );
      }
      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('native-image-viewer')), findsNothing);
      expect(tester.takeException(), isNull);
    });
  }
  for (final outcome in ['expired', 'deleted', 'page_revision']) {
    for (final failure in ['transport', '503']) {
      testWidgets(
        'confirmed $outcome A is published when unrelated B $failure fails',
        (tester) async {
          var failed = false;
          var recovered = false;
          var contentReads = 0;
          final cursors = <String?>[];
          final api = await nativeApi((request) async {
            final a = nativeOperationFixture();
            final b = nativeOperationFixture()
              ..['operationId'] = _otherID
              ..['toolName'] = 'other_tool';
            if (failed && outcome == 'page_revision') {
              a['revision'] = 5;
              (a['content'] as Map)['result'] = {'state': 'unavailable'};
            }
            if (request.url.path.endsWith('/operations')) {
              cursors.add(request.url.queryParameters['afterSequence']);
              return nativeResponse({
                'items': failed
                    ? outcome == 'page_revision' && !recovered
                          ? [a]
                          : []
                    : [a, b],
                'nextSequence': failed && outcome == 'page_revision' ? 8 : 7,
              });
            }
            if (request.url.path.contains('/content')) {
              contentReads++;
              return nativeTextResponse('old A result');
            }
            if (request.url.path.endsWith('/$_otherID') &&
                failed &&
                !recovered) {
              if (failure == '503') {
                return _problem(503);
              }
              throw http.ClientException('unrelated B unavailable');
            }
            if (request.url.path.endsWith('/$_otherID')) {
              return nativeResponse(b);
            }
            if (failed && outcome == 'deleted') {
              return _problem(404);
            }
            if (failed && outcome == 'expired') {
              (a['content'] as Map)['result'] = {
                'state': 'expired',
                'digest': 'c' * 64,
                'expiresAt': '2025-01-01T00:00:00Z',
              };
            }
            return nativeResponse(a);
          });
          addTearDown(api.close);
          final controller = nativeController(api);
          addTearDown(controller.dispose);
          await tester.runAsync(() async {
            controller.setNativeReaderVisible(true);
            while (controller.nativeOperationsLoading) {
              await Future<void>.delayed(Duration.zero);
            }
            await controller.loadNativeToolContent(
              controller.nativeOperations.first,
              'tool_result',
            );
          });
          await tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.light().copyWith(
                splashFactory: NoSplash.splashFactory,
              ),
              home: Scaffold(
                body: SingleChildScrollView(
                  child: NativeToolReader(
                    controller: controller,
                    copy: AppCopy.forLanguage(AppLanguage.english),
                  ),
                ),
              ),
            ),
          );
          await tester.tap(
            find.byKey(const Key('native-operation-$nativeOperationID')),
          );
          await tester.pumpAndSettle();
          expect(find.text('old A result'), findsOneWidget);
          failed = true;
          await tester.runAsync(controller.refreshNativeTools);
          await tester.pumpAndSettle();
          expect(controller.nativeOperationsError, isNotNull);
          expect(find.text('old A result'), findsNothing);
          if (outcome == 'deleted') {
            expect(
              controller.nativeOperations.any(
                (op) => op.operationId == nativeOperationID,
              ),
              isFalse,
            );
          } else {
            expect(
              controller.nativeOperations.first.result.state,
              outcome == 'expired' ? 'expired' : 'unavailable',
            );
          }
          expect(controller.nativeOperationsComplete, isFalse);
          recovered = true;
          await tester.runAsync(controller.refreshNativeTools);
          await tester.pumpAndSettle();
          expect(controller.nativeOperationsError, isNull);
          expect(cursors, ['0', '7', outcome == 'page_revision' ? '8' : '7']);
          expect(find.text('old A result'), findsNothing);
          expect(contentReads, 1);
        },
      );
    }
  }
}

http.Response _problem(int status) => http.Response(
  jsonEncode({
    'type': 'urn:vibermate:error:runtime-unavailable',
    'title': 'Unavailable',
    'status': status,
    'code': status == 404 ? 'approval_not_found' : 'runtime_unavailable',
  }),
  status,
  headers: {'content-type': 'application/problem+json'},
);

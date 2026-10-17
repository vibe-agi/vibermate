import 'package:flutter_test/flutter_test.dart';
import 'dart:convert';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/native_tool_models.dart';
import 'native_tool_reader_test.dart'
    show nativeOperationFixture, nativeTextResponse;
import 'native_tool_approval_test.dart'
    show nativeCapture, nativeApi, nativeController, nativeResponse;

void main() {
  test(
    'source-aware native page permits explicitly unknown body-only lifecycle',
    () {
      final operation = nativeOperationFixture()
        ..['inspectionVersion'] = 'e' * 64
        ..['revision'] = 0
        ..['state'] = 'unknown'
        ..['decisionSource'] = ''
        ..['reason'] = ''
        ..['completedAt'] = null;
      final page = NativeToolOperationPage.fromJson(
        {
          'items': [operation],
          'nextCursor': 'opaque-source-position',
          'inspectionVersion': 'e' * 64,
        },
        nativeCapture,
        '',
        50,
      );
      expect(page.items.single.state, 'unknown');
      expect(page.items.single.revision, 0);
    },
  );
  test(
    'stale inspection generation clears pages and cached bodies before restarting',
    () async {
      var changed = false;
      var contentReads = 0;
      final cursors = <String?>[];
      final api = await nativeApi((request) async {
        final version = (changed ? 'f' : 'e') * 64;
        final op = nativeOperationFixture()..['inspectionVersion'] = version;
        if (request.url.path.endsWith('/operations')) {
          final cursor = request.url.queryParameters['cursor'];
          cursors.add(cursor);
          if (changed && cursor != null) {
            return http.Response(
              jsonEncode({
                'type': 'urn:vibermate:error:revision-conflict',
                'title': 'Changed',
                'status': 409,
                'code': 'revision_conflict',
              }),
              409,
              headers: {'content-type': 'application/problem+json'},
            );
          }
          return nativeResponse({
            'items': [op],
            'nextCursor': changed ? 'new-source-tail' : 'old-source-tail',
            'inspectionVersion': version,
          });
        }
        if (request.url.path.contains('/content')) {
          contentReads++;
          final body =
              jsonDecode(nativeTextResponse('same retained bytes').body)
                  as Map<String, dynamic>;
          body['inspectionVersion'] = version;
          return nativeResponse(body);
        }
        return nativeResponse(op);
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeReaderVisible(true);
      while (controller.nativeOperationsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
      expect(controller.nativeOperationsError, isNull);
      final before = controller.nativeReaderGeneration;
      await controller.loadNativeToolContent(
        controller.nativeOperations.single,
        'tool_result',
      );
      expect(contentReads, 1);
      changed = true;
      await controller.refreshNativeTools();
      expect(controller.nativeOperations, isEmpty);
      expect(controller.nativeReaderGeneration, greaterThan(before));
      await controller.refreshNativeTools();
      expect(controller.nativeOperations.single.inspectionVersion, 'f' * 64);
      await controller.loadNativeToolContent(
        controller.nativeOperations.single,
        'tool_result',
      );
      expect(contentReads, 2);
      expect(cursors, [null, 'old-source-tail', null]);
    },
  );
}

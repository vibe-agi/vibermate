import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'dart:convert';
import 'dart:async';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/native_tool_reader.dart';
import 'package:vibermate_app/features/workbench/native_tool_approval_row.dart';
import 'package:vibermate_app/core/api/native_tool_models.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/capture_overview.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'native_tool_approval_test.dart'
    show
        nativeController,
        nativeApi,
        nativeResponse,
        nativeCapture,
        nativeApprovalFixture,
        nativeApprovalID;

void main() {
  for (final action in ['form_reply', 'url_reply', 'verification_reply']) {
    testWidgets(
      'native callback HTTP approval row accepts empty catalog name $action',
      (tester) async {
        final fixture = nativeApprovalFixture();
        fixture['preview'] = {
          'intents': [
            {
              'toolName': '',
              'action': action,
              'target': 'https://native.example',
              'truncated': false,
            },
          ],
          'truncated': false,
        };
        final api = await nativeApi((_) async => nativeResponse(fixture));
        addTearDown(api.close);
        final approval = (await tester.runAsync(
          () => api.nativeApproval(nativeCapture, nativeApprovalID),
        ))!;
        final copy = AppCopy.forLanguage(AppLanguage.english);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light().copyWith(
              splashFactory: NoSplash.splashFactory,
            ),
            home: Scaffold(
              body: NativeToolApprovalRow(
                approval: approval,
                profileName: 'Synthetic profile',
                copy: copy,
                onDecision: (_) {},
              ),
            ),
          ),
        );
        expect(find.text(copy('native.action.$action')), findsOneWidget);
        expect(find.text(copy('native.login')), findsOneWidget);
        expect(approval.canAllow, isTrue);
        expect(approval.intents.single.arguments, isNull);
      },
    );
  }
  test('repeated native visible signal keeps the same query work', () async {
    var reads = 0;
    final api = await nativeApi((request) async {
      reads++;
      return nativeResponse({'items': [], 'nextSequence': 0});
    });
    addTearDown(api.close);
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    controller.setNativeReaderVisible(true);
    while (controller.nativeOperationsLoading) {
      await Future<void>.delayed(Duration.zero);
    }
    controller.setNativeReaderVisible(true);
    while (controller.nativeOperationsLoading) {
      await Future<void>.delayed(Duration.zero);
    }
    expect(reads, 1);
  });
  test(
    'started native operation preserves empty reason and unconfirmed name/login',
    () {
      final raw = nativeOperationFixture()
        ..['state'] = 'started'
        ..['completedAt'] = null
        ..['reason'] = ''
        ..['toolName'] = ''
        ..['credentialOwner'] = '';
      final op = NativeToolOperation.fromJson(raw, nativeCapture);
      expect(op.state, 'started');
      expect(op.reason, isEmpty);
      expect(op.toolName, isEmpty);
      expect(op.credentialOwner, isEmpty);
    },
  );
  test(
    'native completed cache evicts by32items and4MiB actual bytes',
    () async {
      for (final bytesLimit in [false, true]) {
        final count = bytesLimit ? 5 : 33, size = bytesLimit ? 1 << 20 : 16;
        final reads = <String, int>{};
        final operations = [
          for (var i = 1; i <= count; i++)
            nativeOperationFixture()
              ..['operationId'] = i.toRadixString(16).padLeft(32, '0'),
        ];
        final api = await nativeApi((request) async {
          if (request.url.path.endsWith('/operations')) {
            return nativeResponse({'items': operations, 'nextSequence': 7});
          }
          final segments = request.url.pathSegments;
          final index = segments.indexOf('operations');
          final id = segments[index + 1];
          if (!request.url.path.contains('/content')) {
            return nativeResponse(
              operations.firstWhere((op) => op['operationId'] == id),
            );
          }
          reads[id] = (reads[id] ?? 0) + 1;
          final offset = int.parse(
            request.url.queryParameters['cursor'] ?? '0',
          );
          final end = (offset + (64 << 10)).clamp(0, size);
          return nativeResponse({
            'operationId': id,
            'captureKey': nativeCapture,
            'part': 'tool_result',
            'bodyDigest': 'c' * 64,
            'blocks': [
              {
                'kind': 'text',
                'text': 'x' * (end - offset),
                'offset': offset,
                'total': size,
                'complete': end == size,
              },
            ],
            if (end < size) 'nextCursor': '$end',
          });
        });
        final controller = nativeController(api);
        controller.setNativeReaderVisible(true);
        while (controller.nativeOperationsLoading) {
          await Future<void>.delayed(Duration.zero);
        }
        for (final op in controller.nativeOperations) {
          await controller.loadNativeToolContent(op, 'tool_result');
        }
        final last = controller.nativeOperations.last;
        final before = reads[last.operationId];
        await controller.loadNativeToolContent(last, 'tool_result');
        expect(reads[last.operationId], before);
        final first = controller.nativeOperations.first;
        final firstReads = reads[first.operationId]!;
        await controller.loadNativeToolContent(first, 'tool_result');
        expect(reads[first.operationId], firstReads * 2);
        controller.dispose();
        api.close();
      }
    },
  );
  testWidgets(
    'new native revision hides old content while its page is pending',
    (tester) async {
      var changed = false;
      final delayed = Completer<http.Response>();
      final api = await nativeApi((request) async {
        final op = nativeOperationFixture();
        if (changed) {
          op['revision'] = 5;
          (op['content'] as Map)['result'] = {
            'state': 'available',
            'digest': 'd' * 64,
            'expiresAt': '2099-01-01T00:00:00Z',
          };
        }
        if (request.url.path.endsWith('/operations')) {
          return nativeResponse({
            'items': [op],
            'nextSequence': changed ? 8 : 7,
          });
        }
        if (request.url.path.contains('/content')) {
          return changed
              ? delayed.future
              : nativeTextResponse('old native result');
        }
        return nativeResponse(op);
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
          controller.nativeOperations.single,
          'tool_result',
        );
      });
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light().copyWith(
            splashFactory: NoSplash.splashFactory,
          ),
          home: Scaffold(
            body: NativeToolReader(
              controller: controller,
              copy: AppCopy.forLanguage(AppLanguage.english),
            ),
          ),
        ),
      );
      await tester.pump();
      await tester.tap(
        find.byKey(const Key('native-operation-$nativeOperationID')),
      );
      await tester.pumpAndSettle();
      expect(find.text('old native result'), findsOneWidget);
      await tester.runAsync(() async {
        while (controller.nativeOperationsLoading) {
          await Future<void>.delayed(Duration.zero);
        }
      });
      changed = true;
      await tester.runAsync(controller.refreshNativeTools);
      expect(controller.nativeOperationsError, isNull);
      expect(controller.nativeOperations.single.revision, 5);
      await tester.pump();
      expect(find.text('old native result'), findsNothing);
      delayed.complete(
        nativeResponse({
          'operationId': nativeOperationID,
          'captureKey': nativeCapture,
          'part': 'tool_result',
          'bodyDigest': 'd' * 64,
          'blocks': [
            {
              'kind': 'text',
              'text': 'new',
              'offset': 0,
              'total': 3,
              'complete': true,
            },
          ],
        }),
      );
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 20)),
      );
      await tester.pump();
      expect(find.text('new'), findsOneWidget);
    },
  );
  test('cleanup discards native content already in flight', () async {
    final delayed = Completer<http.Response>();
    final api = await nativeApi((request) async {
      if (request.url.path.contains('cleanup-expired')) {
        return nativeResponse({
          'deleted': true,
          'holderCount': 0,
          'holders': [],
        });
      }
      if (request.url.path.endsWith('/storage')) {
        return http.Response('{}', 503);
      }
      if (request.url.path.endsWith('/operations')) {
        return nativeResponse({
          'items': [nativeOperationFixture()],
          'nextSequence': 7,
        });
      }
      if (request.url.path.contains('/content')) {
        return delayed.future;
      }
      return nativeResponse(nativeOperationFixture());
    });
    addTearDown(api.close);
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    controller.setNativeReaderVisible(true);
    while (controller.nativeOperationsLoading) {
      await Future<void>.delayed(Duration.zero);
    }
    final pending = controller.loadNativeToolContent(
      controller.nativeOperations.single,
      'tool_result',
    );
    var rejected = false;
    final outcome = pending.then(
      (_) => null,
      onError: (Object _) {
        rejected = true;
        return null;
      },
    );
    await controller.cleanupExpiredEvidence();
    delayed.complete(nativeTextResponse('expired old result'));
    await outcome;
    expect(rejected, isTrue);
  });
  testWidgets('open and reopened native details observe late completion', (
    tester,
  ) async {
    var completed = false, reads = 0;
    final api = await nativeApi((request) async {
      final operation = nativeOperationFixture();
      if (!completed) {
        operation['state'] = 'started';
        operation['reason'] = '';
        operation['completedAt'] = null;
        operation['revision'] = 3;
        (operation['content'] as Map)['result'] = {'state': 'unavailable'};
      }
      if (request.url.path.endsWith('/operations')) {
        return nativeResponse({
          'items': [operation],
          'nextSequence': completed ? 8 : 7,
        });
      }
      if (request.url.path.contains('/content')) {
        reads++;
        return nativeTextResponse('late completed result');
      }
      return nativeResponse(operation);
    });
    addTearDown(api.close);
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    await tester.runAsync(() async {
      controller.setNativeReaderVisible(true);
      while (controller.nativeOperationsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
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
    await tester.pump();
    await tester.tap(
      find.byKey(const Key('native-operation-$nativeOperationID')),
    );
    await tester.pump();
    expect(find.text('Result unavailable'), findsWidgets);
    expect(reads, 0);
    completed = true;
    await tester.runAsync(controller.refreshNativeTools);
    expect(controller.nativeOperationsError, isNull);
    expect(controller.nativeOperations.single.revision, 4);
    await tester.pump();
    final completedBlocks = await tester.runAsync(
      () => controller.loadNativeToolContent(
        controller.nativeOperations.single,
        'tool_result',
      ),
    );
    expect(completedBlocks!.single.value, 'late completed result');
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    await tester.pumpAndSettle();
    expect(
      find.text('late completed result'),
      findsOneWidget,
      reason: tester
          .widgetList<Text>(
            find.descendant(
              of: find.byType(AlertDialog),
              matching: find.byType(Text),
            ),
          )
          .map((text) => text.data)
          .join('|'),
    );
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('native-operation-$nativeOperationID')),
    );
    await tester.pump();
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    await tester.pumpAndSettle();
    expect(find.text('late completed result'), findsOneWidget);
    expect(reads, 2);
  });
  test('native operation wire rejects unknown kind and reason', () {
    for (final field in ['kind', 'reason']) {
      final raw = nativeOperationFixture()..[field] = 'invented';
      expect(
        () => NativeToolOperation.fromJson(raw, nativeCapture),
        throwsA(isA<ControlContractException>()),
      );
    }
  });
  test('native image cache retains bytes without base64 copy', () {
    final block = NativeRecordedBlock('image', nativePNG, 'image/png');
    expect(block.value, isEmpty);
    expect(block.byteSize, 68);
  });
  test(
    'native fragments reconstruct once and interrupted pages are retried',
    () async {
      var pageReads = 0;
      var fail = true;
      final api = await nativeApi((request) async {
        if (request.url.path.endsWith('/operations')) {
          return nativeResponse({
            'items': [nativeOperationFixture()],
            'nextSequence': 7,
          });
        }
        if (!request.url.path.contains('/content')) {
          return nativeResponse(nativeOperationFixture());
        }
        pageReads++;
        final later = request.url.queryParameters.containsKey('cursor');
        if (later && fail) {
          throw http.ClientException('interrupted page');
        }
        return nativeResponse({
          'operationId': nativeOperationID,
          'captureKey': nativeCapture,
          'part': 'tool_result',
          'bodyDigest': 'c' * 64,
          'blocks': [
            {
              'kind': 'text',
              'text': later ? ' world' : 'hello',
              'offset': later ? 5 : 0,
              'total': 11,
              'complete': later,
            },
          ],
          if (!later) 'nextCursor': 'signed-synthetic',
        });
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeReaderVisible(true);
      while (controller.nativeOperationsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
      final op = controller.nativeOperations.single;
      final failed = controller.loadNativeToolContent(op, 'tool_result');
      expect(
        identical(failed, controller.loadNativeToolContent(op, 'tool_result')),
        isTrue,
      );
      await expectLater(failed, throwsA(isA<http.ClientException>()));
      fail = false;
      final blocks = await controller.loadNativeToolContent(op, 'tool_result');
      expect(blocks.single.value, 'hello world');
      expect(pageReads, 4);
      await controller.loadNativeToolContent(op, 'tool_result');
      expect(pageReads, 4);
    },
  );
  test(
    'late native content is discarded on close and Capture switch',
    () async {
      for (final switchCapture in [false, true]) {
        final delayed = Completer<http.Response>();
        final api = await nativeApi((request) async {
          if (request.url.path.endsWith('/operations')) {
            return nativeResponse({
              'items': [nativeOperationFixture()],
              'nextSequence': 7,
            });
          }
          if (request.url.path.contains('/content')) {
            return delayed.future;
          }
          return nativeResponse(nativeOperationFixture());
        });
        final controller = nativeController(api);
        controller.setNativeReaderVisible(true);
        while (controller.nativeOperationsLoading) {
          await Future<void>.delayed(Duration.zero);
        }
        final pending = controller.loadNativeToolContent(
          controller.nativeOperations.single,
          'tool_result',
        );
        final rejected = expectLater(
          pending,
          throwsA(isA<ControlContractException>()),
        );
        if (switchCapture) {
          controller.selectedCaptureKey = 'manual_capture:other';
        } else {
          controller.setNativeReaderVisible(false);
        }
        delayed.complete(nativeTextResponse('old result'));
        await rejected;
        controller.dispose();
        api.close();
      }
    },
  );
  for (final language in AppLanguage.values) {
    testWidgets('native reader image keyboard390200 ${language.name}', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = await nativeApi((request) async {
        if (request.url.path.endsWith('/operations')) {
          return nativeResponse({
            'items': [nativeOperationFixture()],
            'nextSequence': 7,
          });
        }
        if (request.url.path.contains('/content')) {
          return nativeResponse({
            'operationId': nativeOperationID,
            'captureKey': nativeCapture,
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
        return nativeResponse(nativeOperationFixture());
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
                child: NativeToolReader(
                  controller: controller,
                  copy: AppCopy.forLanguage(language),
                ),
              ),
            ),
          ),
        ),
      );
      await tester.pump();
      await tester.tap(
        find.byKey(const Key('native-operation-$nativeOperationID')),
      );
      await tester.pumpAndSettle();
      expect(find.byType(Image), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pumpAndSettle();
      expect(find.byType(AlertDialog), findsNothing);
    });
  }
  test(
    'typed native HTTP terminal page keeps cursor and separate Capture path',
    () async {
      final api = await nativeApi((request) async {
        expect(
          Uri.decodeComponent(request.url.path),
          '/api/v1/captures/$nativeCapture/native-tools/operations',
        );
        expect(request.url.queryParameters, {
          'afterSequence': '999999',
          'limit': '50',
        });
        return nativeResponse({'items': [], 'nextSequence': 999999});
      });
      addTearDown(api.close);
      final page = await api.nativeToolOperations(
        nativeCapture,
        afterSequence: 999999,
      );
      expect(page.items, isEmpty);
      expect(page.nextSequence, 999999);
    },
  );
  testWidgets('native HTTP Capture reader opens actual result text and PNG', (
    tester,
  ) async {
    final api = await nativeApi((request) async {
      if (request.url.path.endsWith('/operations')) {
        return nativeResponse({
          'items': [nativeOperationFixture()],
          'nextSequence': 7,
        });
      }
      if (request.url.path.contains('/content')) {
        return nativeResponse({
          'operationId': nativeOperationID,
          'captureKey': nativeCapture,
          'part': 'tool_result',
          'bodyDigest': 'c' * 64,
          'blocks': [
            {
              'kind': 'text',
              'text': 'actual native text',
              'offset': 0,
              'total': 18,
              'complete': true,
            },
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
      return nativeResponse(nativeOperationFixture());
    });
    addTearDown(api.close);
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    await tester.runAsync(() async {
      controller.setNativeReaderVisible(true);
      while (controller.nativeOperationsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
    });
    expect(controller.nativeOperationsError, isNull);
    expect(controller.nativeOperations.length, 1);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light().copyWith(
          splashFactory: NoSplash.splashFactory,
        ),
        home: Scaffold(
          body: CaptureRequestOverview(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
            withoutBodies: false,
            onShowRecords: () {},
          ),
        ),
      ),
    );
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    await tester.pump();
    expect(
      find.byKey(const Key('native-operation-$nativeOperationID')),
      findsOneWidget,
    );
    await tester.tap(
      find.byKey(const Key('native-operation-$nativeOperationID')),
    );
    await tester.pump();
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    await tester.pump();
    expect(find.text('actual native text'), findsOneWidget);
    expect(find.byType(Image), findsOneWidget);
    expect(find.text("Client's existing login"), findsWidgets);
    expect(find.text('User'), findsNothing);
    expect(find.text('Agent'), findsNothing);
    expect(tester.takeException(), isNull);
  });
  testWidgets('Capture has a standalone neutral tool service reader', (
    tester,
  ) async {
    final controller = nativeController(PreviewControlApi());
    addTearDown(controller.dispose);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light().copyWith(
          splashFactory: NoSplash.splashFactory,
        ),
        home: Scaffold(
          body: CaptureRequestOverview(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
            withoutBodies: true,
            onShowRecords: () {},
          ),
        ),
      ),
    );
    await tester.pump();
    expect(find.text('Tool service'), findsOneWidget);
    expect(find.byKey(const Key('native-tool-reader')), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
  for (final chinese in [false, true]) {
    testWidgets('whole shell environment390px200percent chinese=$chinese', (
      tester,
    ) async {
      final renderingDetails = <String>[];
      final handler = FlutterError.onError;
      FlutterError.onError = (details) {
        renderingDetails.add(details.toString());
        handler?.call(details);
      };
      addTearDown(() => FlutterError.onError = handler);
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        MediaQuery(
          data: const MediaQueryData(textScaler: TextScaler.linear(2)),
          child: ViberMateApp(previewMode: true, preferChinese: chinese),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-row-work')), findsOneWidget);
      await tester.tap(find.byIcon(Icons.adjust).first);
      await tester.pumpAndSettle();
      final capture = find.byKey(
        const Key('capture-row-manual_capture:manual-figma'),
      );
      await tester.ensureVisible(capture);
      await tester.tap(capture);
      await tester.pumpAndSettle();
      final copy = AppCopy.forLanguage(
        chinese ? AppLanguage.simplifiedChinese : AppLanguage.english,
      );
      await tester.tap(
        find.descendant(
          of: find.byKey(const Key('capture-evidence-mode')),
          matching: find.text(copy('capture.summary.overview')),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('native-tool-reader')), findsOneWidget);
      expect(
        tester.takeException(),
        isNull,
        reason: renderingDetails.join('\n'),
      );
    });
  }
}

const nativeOperationID = '00000000000000000000000000000002';
const nativePNG =
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=';
Map<String, Object?> nativeOperationFixture() => {
  'operationId': nativeOperationID,
  'captureKey': nativeCapture,
  'environment': {'id': 'native.synthetic', 'revision': 3, 'digest': 'a' * 64},
  'revision': 4,
  'kind': 'tools_call',
  'toolName': 'read_file',
  'state': 'completed',
  'reason': 'completed',
  'decisionSource': 'policy',
  'targetOrigin': 'https://native.example',
  'credentialOwner': 'original_client',
  'createdAt': '2026-10-04T00:00:00Z',
  'completedAt': '2026-10-04T00:00:01Z',
  'bytesIn': null,
  'bytesOut': null,
  'content': {
    'mode': 'full',
    'arguments': {'state': 'unavailable'},
    'result': {
      'state': 'available',
      'digest': 'c' * 64,
      'expiresAt': '2099-01-01T00:00:00Z',
    },
  },
};
http.Response nativeTextResponse(String text) => nativeResponse({
  'operationId': nativeOperationID,
  'captureKey': nativeCapture,
  'part': 'tool_result',
  'bodyDigest': 'c' * 64,
  'blocks': [
    {
      'kind': 'text',
      'text': text,
      'offset': 0,
      'total': utf8.encode(text).length,
      'complete': true,
    },
  ],
});

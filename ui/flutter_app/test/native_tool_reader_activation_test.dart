import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/native_tool_models.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/native_tool_reader.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'native_tool_approval_test.dart' show nativeController, nativeCapture;
import 'native_tool_reader_test.dart' show nativeOperationFixture;

void main() {
  testWidgets('synchronous list failure at mount is safely unavailable once', (
    tester,
  ) async {
    final api = _ActivationApi();
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    await tester.pumpWidget(_reader(controller));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(api.readCaptures, [nativeCapture]);
    expect(controller.nativeOperationsError, isNotNull);
    expect(
      find.text(
        AppCopy.forLanguage(AppLanguage.english)('native.readerUnavailable'),
      ),
      findsOneWidget,
    );
    await tester.pumpWidget(_reader(controller));
    await tester.pumpAndSettle();
    expect(api.readCaptures, [nativeCapture]);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'capture selected before queued activation reads only current scope',
    (tester) async {
      final api = _ActivationApi();
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      const nextCapture = 'manual_capture:other';
      // Registered before mounting: this real selection runs in the same frame
      // before the reader's post-frame activation, without a build-phase mutation.
      tester.binding.addPostFrameCallback((_) {
        unawaited(controller.selectCapture(nextCapture));
      });
      await tester.pumpWidget(_reader(controller));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      expect(controller.selectedCaptureKey, nextCapture);
      expect(api.readCaptures, [nextCapture]);
      expect(controller.nativeOperationsError, isNotNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'controller replacement hides old owner and activates new owner once',
    (tester) async {
      final held = Completer<NativeToolOperationPage>();
      addTearDown(() {
        if (!held.isCompleted) {
          held.complete(const NativeToolOperationPage([], 0));
        }
      });
      final oldApi = _ActivationApi(read: (_) => held.future);
      final oldController = nativeController(oldApi);
      final newApi = _ActivationApi();
      final newController = nativeController(newApi)
        ..selectedCaptureKey = 'manual_capture:new-owner';
      addTearDown(oldController.dispose);
      addTearDown(newController.dispose);
      await tester.pumpWidget(_reader(oldController));
      expect(oldApi.readCaptures, [nativeCapture]);
      final epoch = oldController.nativeReaderGeneration;
      await tester.pumpWidget(_reader(newController));
      expect(oldController.nativeReaderGeneration, greaterThan(epoch));
      expect(oldController.nativeOperationsLoading, isFalse);
      expect(newApi.readCaptures, ['manual_capture:new-owner']);
      held.complete(_oldPage());
      await tester.pumpAndSettle();
      expect(oldController.nativeOperations, isEmpty);
      expect(newController.nativeOperations, isEmpty);
      expect(newController.nativeOperationsError, isNotNull);
      expect(newApi.readCaptures, ['manual_capture:new-owner']);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'capture refresh rejects late old response and coalesces rebuilds',
    (tester) async {
      final held = Completer<NativeToolOperationPage>();
      addTearDown(() {
        if (!held.isCompleted) {
          held.complete(const NativeToolOperationPage([], 0));
        }
      });
      final api = _ActivationApi(
        read: (capture) => capture == nativeCapture
            ? held.future
            : Future.value(const NativeToolOperationPage([], 0)),
      );
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(_reader(controller));
      final epoch = controller.nativeReaderGeneration;
      await controller.selectCapture('manual_capture:next');
      expect(controller.nativeReaderGeneration, greaterThan(epoch));
      expect(controller.nativeOperations, isEmpty);
      await tester.pumpAndSettle();
      expect(api.readCaptures, [nativeCapture, 'manual_capture:next']);
      held.complete(_oldPage());
      await tester.pumpAndSettle();
      await tester.pumpWidget(_reader(controller));
      await tester.pumpAndSettle();
      expect(api.readCaptures, [nativeCapture, 'manual_capture:next']);
      expect(controller.nativeOperations, isEmpty);
      expect(controller.nativeOperationsError, isNull);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'leaving reader immediately invalidates and never revives late content',
    (tester) async {
      final held = Completer<NativeToolOperationPage>();
      addTearDown(() {
        if (!held.isCompleted) {
          held.complete(const NativeToolOperationPage([], 0));
        }
      });
      final api = _ActivationApi(read: (_) => held.future);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      await tester.pumpWidget(_reader(controller));
      final epoch = controller.nativeReaderGeneration;
      expect(api.readCaptures, [nativeCapture]);
      await tester.pumpWidget(const SizedBox.shrink());
      expect(controller.nativeReaderGeneration, greaterThan(epoch));
      expect(controller.nativeOperationsLoading, isFalse);
      held.complete(_oldPage());
      await tester.pumpAndSettle();
      await controller.refreshNativeTools();
      expect(api.readCaptures, [nativeCapture]);
      expect(controller.nativeOperations, isEmpty);
      expect(controller.nativeOperationsError, isNull);
      expect(tester.takeException(), isNull);
    },
  );
}

NativeToolOperationPage _oldPage() => NativeToolOperationPage([
  NativeToolOperation.fromJson(nativeOperationFixture(), nativeCapture),
], 4);

Widget _reader(WorkbenchController controller) => MaterialApp(
  home: Scaffold(
    body: AnimatedBuilder(
      animation: controller,
      builder: (_, _) => SingleChildScrollView(
        child: NativeToolReader(
          controller: controller,
          copy: AppCopy.forLanguage(AppLanguage.english),
        ),
      ),
    ),
  ),
);

final class _ActivationApi implements ControlApi {
  _ActivationApi({this.read});
  final Future<NativeToolOperationPage> Function(String)? read;
  final readCaptures = <String>[];

  @override
  Future<NativeToolOperationPage> nativeToolOperations(
    String capture, {
    int afterSequence = 0,
    int limit = 50,
  }) {
    readCaptures.add(capture);
    if (read != null) return read!(capture);
    throw StateError('synthetic synchronous native list unavailable');
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('unexpected activation dependency');
}

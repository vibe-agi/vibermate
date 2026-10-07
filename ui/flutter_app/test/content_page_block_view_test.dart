import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'conversation_reader_test.dart' as fixture;

final class _BlockApi implements ControlApi {
  final fixtureApi = PreviewControlApi();
  final details = <String, ExchangeDetail>{};
  final pages = <String, ExchangeContentPage>{};
  @override
  Future<ExchangeDetail> exchange(
    String id, {
    String contentView = 'incremental',
  }) async =>
      details[id] ?? await fixtureApi.exchange(id, contentView: contentView);
  @override
  Future<ExchangeContentPage> exchangeContentPage(
    String id,
    String cursor,
  ) async => pages[cursor]!;
  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) =>
      fixtureApi.runtimeUsage(query);
  @override
  Future<void> close() => fixtureApi.close();
  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

void main() {
  testWidgets(
    'Complete record keeps exact bytes, copy and bounded navigation at 200 percent',
    (tester) async {
      const fontDirectory = String.fromEnvironment(
        'CONTENT_PAGE_FONT_DIRECTORY',
      );
      if (fontDirectory.isNotEmpty) {
        await tester.runAsync(() async {
          final textBytes = ByteData.sublistView(
            await File('$fontDirectory/Roboto-Regular.ttf').readAsBytes(),
          );
          for (final family in {'Ahem', viberSystemFontFamily, 'Menlo'}) {
            final loader = FontLoader(family)..addFont(Future.value(textBytes));
            await loader.load();
          }
          final icons = FontLoader('MaterialIcons')
            ..addFont(
              File(
                '$fontDirectory/MaterialIcons-Regular.otf',
              ).readAsBytes().then(ByteData.sublistView),
            );
          await icons.load();
        });
      }
      await tester.binding.setSurfaceSize(const Size(390, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      String? copied;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copied = (call.arguments as Map)['text'] as String;
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
      final api = _BlockApi();
      final activity = (await api.fixtureApi.activities(
        captureRunId: 'run-1',
      )).items.firstWhere((a) => a.status == 'succeeded');
      final base = await api.exchange(activity.id);
      final deferred = ExchangeContentBlock.fromJson({
        'kind': 'deferred',
        'availability': 'recorded',
        'originalSize': 0,
        'deferred': {
          'exchangeId': activity.id,
          'cursor': 'first',
          'estimatedBytes': 200000,
        },
      }, 'fixture');
      api.details[activity.id] = fixture.detail(
        base,
        [fixture.message('user', 'question')],
        [deferred],
      );
      api.pages['first'] = ExchangeContentPage(
        exchangeId: activity.id,
        kind: 'text',
        messages: const [],
        blocks: const [],
        text: 'SHORT_BODY',
        offset: 0,
        total: 10,
        canonicalCursor: 'exact',
      );
      final raw = [...utf8.encode('VISIBLE '), 0xff, 0xe2, 0x80];
      api.pages['exact'] = ExchangeContentPage(
        exchangeId: activity.id,
        kind: 'block_bytes',
        messages: const [],
        blocks: const [],
        text: '',
        offset: 0,
        total: raw.length + 1,
        data: raw,
        nextCursor: 'last',
      );
      api.pages['last'] = ExchangeContentPage(
        exchangeId: activity.id,
        kind: 'block_bytes',
        messages: const [],
        blocks: const [],
        text: '',
        offset: raw.length,
        total: raw.length + 1,
        data: const [125],
      );
      final controller = fixture.makeController(api);
      addTearDown(controller.dispose);
      final originalHost =
          fixture.host(controller, [activity], scale: 2) as MaterialApp;
      // VM tests substitute Ahem when a TextStyle inherits its font family.
      // Capture only: bind that inherited family to the cached readable font.
      final host = fontDirectory.isEmpty
          ? originalHost
          : MaterialApp(
              theme: originalHost.theme!.copyWith(
                textTheme: originalHost.theme!.textTheme.apply(
                  fontFamily: viberSystemFontFamily,
                ),
              ),
              builder: originalHost.builder,
              home: originalHost.home,
            );
      await tester.pumpWidget(host);
      await tester.pumpAndSettle();
      final open = find.byKey(const ValueKey('content-page-open-first'));
      await tester.ensureVisible(open);
      await tester.tap(open);
      await tester.pumpAndSettle();
      await tester.scrollUntilVisible(
        find.byKey(const Key('content-page-text')),
        100,
        scrollable: find
            .descendant(
              of: find.byKey(const Key('content-page-scroll')),
              matching: find.byType(Scrollable),
            )
            .first,
      );
      expect(find.text('SHORT_BODY'), findsOneWidget);
      await tester.tap(find.byTooltip('Copy loaded page'));
      await tester.pump();
      expect(copied, 'SHORT_BODY');
      await tester.ensureVisible(
        find.byKey(const Key('content-page-complete-record')),
      );
      await tester.tap(find.byKey(const Key('content-page-complete-record')));
      await tester.pumpAndSettle();
      await tester.scrollUntilVisible(
        find.byKey(const Key('content-page-bytes')),
        100,
        scrollable: find
            .descendant(
              of: find.byKey(const Key('content-page-scroll')),
              matching: find.byType(Scrollable),
            )
            .first,
      );
      expect(find.text('VISIBLE [FF][E2][80]'), findsOneWidget);
      expect(find.text('SHORT_BODY'), findsNothing);
      await tester.tap(find.byTooltip('Copy fragment (Base64)'));
      await tester.pump();
      expect(copied, base64.encode(raw));
      const capture = String.fromEnvironment('CONTENT_PAGE_GOLDEN');
      if (capture.isNotEmpty) {
        await expectLater(find.byType(Dialog), matchesGoldenFile(capture));
      }
      await tester.tap(find.byKey(const Key('content-page-next')));
      await tester.pumpAndSettle();
      expect(find.text('VISIBLE [FF][E2][80]'), findsNothing);
      await tester.tap(find.byKey(const Key('content-page-back')));
      await tester.pumpAndSettle();
      expect(find.text('VISIBLE [FF][E2][80]'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );
}

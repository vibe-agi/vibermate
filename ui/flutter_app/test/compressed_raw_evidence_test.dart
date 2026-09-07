import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/conversation_timeline.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final encoding in ['zstd', 'gzip']) {
      testWidgets('raw $encoding defaults to decoded reading in $language', (
        tester,
      ) async {
        final api = _CompressedEvidenceApi(encoding);
        final copy = AppCopy.forLanguage(language);
        String? clipboard;
        tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          (call) async {
            if (call.method == 'Clipboard.setData') {
              clipboard = (call.arguments as Map)['text'] as String;
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
        await _showRaw(tester, api, copy);
        expect(find.text(api.text), findsOneWidget);
        expect(find.text(base64.encode(api.body)), findsNothing);
        expect(
          find.text(
            copy.format('exchange.raw.body.auto_decoded', {
              'encoding': encoding,
            }),
          ),
          findsOneWidget,
        );
        expect(
          find.text(
            'Content-Encoding: $encoding\nContent-Length: ${api.body.length}',
          ),
          findsNothing,
        );
        expect(
          find.text(copy('exchange.raw.frames.original_offsets')),
          findsNothing,
        );
        final copyBody = find.byKey(
          const Key('copy-raw-body-compressed-fixture'),
        );
        await tester.ensureVisible(copyBody);
        await tester.tap(copyBody);
        await tester.pumpAndSettle();
        expect(clipboard, api.text);
        final technical = find.byKey(
          const Key('raw-technical-compressed-fixture'),
        );
        final toggle = find.byKey(
          const Key('raw-body-toggle-compressed-fixture'),
        );
        expect(toggle, findsNothing);
        await tester.ensureVisible(technical);
        await tester.tap(technical);
        await tester.pumpAndSettle();
        expect(
          find.text(
            'Content-Encoding: $encoding\nContent-Length: ${api.body.length}',
          ),
          findsOneWidget,
        );
        expect(
          find.text(copy('exchange.raw.frames.original_offsets')),
          findsOneWidget,
        );
        final copyButton = find.byKey(const Key('copy-raw-compressed-fixture'));
        await tester.ensureVisible(copyButton);
        await tester.tap(copyButton);
        await tester.pumpAndSettle();
        expect(clipboard, contains(base64.encode(api.body)));
        expect(clipboard, isNot(contains(api.text)));
        expect(clipboard, contains(copy('exchange.raw.body.original_help')));
        await tester.ensureVisible(toggle);
        await tester.tap(toggle);
        await tester.pumpAndSettle();
        expect(find.text(api.text), findsOneWidget);
        expect(find.text(base64.encode(api.body)), findsOneWidget);
        await tester.ensureVisible(copyButton);
        await tester.tap(copyButton);
        await tester.pumpAndSettle();
        expect(clipboard, contains(base64.encode(api.body)));
        expect(clipboard, isNot(contains(api.text)));
        await tester.ensureVisible(technical);
        await tester.tap(technical);
        await tester.pumpAndSettle();
        expect(find.text(api.text), findsOneWidget);
        expect(find.text(base64.encode(api.body)), findsNothing);
        expect(toggle, findsNothing);

        // Hiding/collapsing the evidence discards both representations.
        final raw = find.byKey(const Key('exchange-raw-run-1-exchange-222'));
        await tester.ensureVisible(raw);
        await tester.tap(raw);
        await tester.pumpAndSettle();
        expect(api.lastReveal!.body.every((byte) => byte == 0), isTrue);
        expect(
          api.lastReveal!.decodedBody!.body.every((byte) => byte == 0),
          isTrue,
        );
        expect(find.text(api.text), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      });
    }
    for (final state in [
      null,
      'incomplete',
      'invalid_compression',
      'size_limit',
      'unsupported_encoding',
    ]) {
      testWidgets('raw decode $state explains fallback in $language', (
        tester,
      ) async {
        final api = _CompressedEvidenceApi('zstd', state: state);
        final copy = AppCopy.forLanguage(language);
        await _showRaw(tester, api, copy);
        expect(
          find.text(
            copy('exchange.raw.body.decode.${state ?? 'decoder_unavailable'}'),
          ),
          findsOneWidget,
        );
        expect(find.text(base64.encode(api.body)), findsNothing);
        expect(find.text(api.text), findsNothing);
        expect(
          find.byKey(const Key('copy-raw-body-compressed-fixture')),
          findsNothing,
        );
        expect(
          find.byKey(const Key('raw-body-toggle-compressed-fixture')),
          findsNothing,
        );
        final fallback = find.byKey(
          const Key('raw-body-fallback-compressed-fixture'),
        );
        await tester.ensureVisible(fallback);
        await tester.pumpAndSettle();
        await tester.tap(fallback);
        await tester.pumpAndSettle();
        expect(find.text(base64.encode(api.body)), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      });
    }
  }
  for (final theme in [ViberTheme.light(), ViberTheme.dark()]) {
    testWidgets('390px reading and HTTP details fit ${theme.brightness}', (
      tester,
    ) async {
      final api = _CompressedEvidenceApi('zstd');
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      await _showRaw(
        tester,
        api,
        copy,
        size: const Size(390, 820),
        theme: theme,
      );
      expect(find.text(api.text), findsOneWidget);
      final bodyCopy = find.byKey(
        const Key('copy-raw-body-compressed-fixture'),
      );
      expect(tester.getTopLeft(bodyCopy).dx, greaterThanOrEqualTo(0));
      expect(tester.getBottomRight(bodyCopy).dx, lessThanOrEqualTo(390));
      final technical = find.byKey(
        const Key('raw-technical-compressed-fixture'),
      );
      await tester.ensureVisible(technical);
      await tester.pumpAndSettle();
      await tester.tap(technical);
      await tester.pumpAndSettle();
      final original = find.byKey(
        const Key('raw-body-toggle-compressed-fixture'),
      );
      await tester.ensureVisible(original);
      await tester.pumpAndSettle();
      await tester.tap(original);
      await tester.pumpAndSettle();
      expect(find.text(base64.encode(api.body)), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    });
  }
  for (final text in ['', '{"input":"直接阅读正文"}']) {
    testWidgets('uncompressed text stays readable and copies exactly: $text', (
      tester,
    ) async {
      final api = _CompressedEvidenceApi(
        '',
        state: null,
        body: utf8.encode(text),
        text: text,
      );
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      String? clipboard;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            clipboard = (call.arguments as Map)['text'] as String;
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
      await _showRaw(tester, api, copy);
      expect(
        find.text(text.isEmpty ? copy('exchange.raw.body.empty') : text),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('raw-body-fallback-compressed-fixture')),
        findsNothing,
      );
      final copyBody = find.byKey(
        const Key('copy-raw-body-compressed-fixture'),
      );
      await tester.ensureVisible(copyBody);
      await tester.pumpAndSettle();
      await tester.tap(copyBody);
      await tester.pumpAndSettle();
      expect(clipboard, text);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    });
  }
  for (final encoding in ['', 'zstd']) {
    for (final bytes in [
      [0xff, 0xfe, 0x80],
      [0x00, 0x01, 0x7f],
    ]) {
      testWidgets(
        'binary $encoding body is not mislabeled as readable: $bytes',
        (tester) async {
          final api = _CompressedEvidenceApi(
            encoding,
            state: encoding.isEmpty ? null : 'decoded',
            body: encoding.isEmpty ? bytes : null,
            decodedBytes: bytes,
          );
          final copy = AppCopy.forLanguage(AppLanguage.english);
          await _showRaw(tester, api, copy);
          expect(
            find.text(copy('exchange.raw.body.binary_help')),
            findsOneWidget,
          );
          expect(find.text(base64.encode(api.body)), findsNothing);
          expect(
            find.byKey(const Key('copy-raw-body-compressed-fixture')),
            findsNothing,
          );
          final fallback = find.byKey(
            const Key('raw-body-fallback-compressed-fixture'),
          );
          await tester.ensureVisible(fallback);
          await tester.pumpAndSettle();
          await tester.tap(fallback);
          await tester.pumpAndSettle();
          expect(find.text(base64.encode(api.body)), findsOneWidget);
          expect(tester.takeException(), isNull);
          await tester.pumpWidget(const SizedBox.shrink());
          await tester.pump();
          expect(api.lastReveal!.body.every((byte) => byte == 0), isTrue);
          if (encoding.isNotEmpty) {
            expect(
              api.lastReveal!.decodedBody!.body.every((byte) => byte == 0),
              isTrue,
            );
          }
        },
      );
    }
  }
}

Future<void> _showRaw(
  WidgetTester tester,
  _CompressedEvidenceApi api,
  AppCopy copy, {
  Size size = const Size(1100, 1100),
  ThemeData? theme,
}) async {
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: api.preview.close,
  );
  addTearDown(controller.dispose);
  addTearDown(api.preview.close);
  final page = await api.preview.activities(captureRunId: 'run-1', limit: 224);
  final activity = page.items.singleWhere(
    (item) => item.id == 'run-1-exchange-222',
  );
  await controller.loadExchangeDetail(activity.id);
  await tester.pumpWidget(
    MaterialApp(
      theme: theme ?? ViberTheme.dark(),
      home: Scaffold(
        body: EvidenceConversationTimeline(
          controller: controller,
          activities: [activity],
          copy: copy,
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  final raw = find.byKey(Key('exchange-raw-${activity.id}'));
  await tester.ensureVisible(raw);
  await tester.tap(raw);
  await tester.pumpAndSettle();
  expect(find.text(api.text), findsNothing);
  expect(find.text(base64.encode(api.body)), findsNothing);
  final reveal = find.byKey(const Key('raw-reveal-compressed-fixture'));
  await tester.ensureVisible(reveal);
  await tester.pumpAndSettle();
  await tester.tap(reveal);
  await tester.pumpAndSettle();
}

final class _CompressedEvidenceApi implements ControlApi {
  _CompressedEvidenceApi(
    this.encoding, {
    this.state = 'decoded',
    List<int>? body,
    this.decodedBytes,
    this.text = '{"input":"你好，Codex","stream":true}',
  }) : body = Uint8List.fromList(body ?? [0x28, 0xb5, 0x2f, 0xfd, 0, 0xff]);
  final String encoding;
  final String? state;
  final List<int>? decodedBytes;
  final preview = PreviewControlApi();
  // The server decoder is exercised with real compression in Go tests. This
  // fixture isolates display, copy, reveal lifetime and fallback semantics.
  final Uint8List body;
  final String text;
  late RawEvidenceEnvelope envelope;
  RevealedRawEvidence? lastReveal;

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) => preview.exchange(exchangeId, contentView: contentView);

  @override
  Future<RawEvidencePage> rawEvidence(String exchangeId) async {
    final page = await preview.rawEvidence(exchangeId);
    final sample = page.items.first;
    envelope = RawEvidenceEnvelope(
      envelopeId: 'compressed-fixture',
      layer: 'client_ingress',
      scopeKind: sample.scopeKind,
      scopeId: sample.scopeId,
      exchangeId: exchangeId,
      observedAt: sample.observedAt,
      expiresAt: sample.expiresAt,
      method: 'POST',
      headerCount: encoding.isEmpty ? 1 : 2,
      trailerCount: 0,
      contentEncoding: encoding,
      contentType: 'application/json',
      bodyBytes: body.length,
      bodySha256: crypto.sha256.convert(body).toString(),
      digestScope: 'full_body',
      payloadState: state == 'incomplete' ? 'truncated' : 'captured',
      redactedCredentialFields: const [],
      revealAvailable: true,
    );
    return RawEvidencePage(
      items: [envelope],
      recovery: page.recovery,
      writer: page.writer,
    );
  }

  @override
  Future<RevealedRawEvidence> revealRawEvidence({
    required String envelopeId,
  }) async => lastReveal = RevealedRawEvidence(
    envelope: envelope,
    headers: [
      if (encoding.isNotEmpty)
        RawHeaderField(
          name: 'Content-Encoding',
          values: [encoding],
          redacted: const [],
        ),
      RawHeaderField(
        name: 'Content-Length',
        values: ['${body.length}'],
        redacted: const [],
      ),
    ],
    trailers: const [],
    body: Uint8List.fromList(body),
    frames: [RawFrame(kind: 'data', offset: 0, length: body.length)],
    decodedBody: state == null
        ? null
        : RawDecodedBody(
            state: state!,
            body: Uint8List.fromList(
              state == 'decoded' ? (decodedBytes ?? utf8.encode(text)) : [],
            ),
          ),
  );

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

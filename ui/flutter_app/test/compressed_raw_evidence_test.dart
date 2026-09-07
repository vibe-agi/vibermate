import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter/material.dart';
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
      testWidgets('raw $encoding explains wire bytes in $language', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(1100, 950));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _CompressedEvidenceApi(encoding);
        final copy = AppCopy.forLanguage(language);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.preview.close,
        );
        addTearDown(controller.dispose);
        addTearDown(api.preview.close);
        final page = await api.preview.activities(
          captureRunId: 'run-1',
          limit: 224,
        );
        final activity = page.items.singleWhere(
          (item) => item.id == 'run-1-exchange-222',
        );
        await controller.loadExchangeDetail(activity.id);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
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
        expect(find.text(base64.encode(api.body)), findsNothing);
        final reveal = find.byKey(const Key('raw-reveal-compressed-fixture'));
        await tester.ensureVisible(reveal);
        await tester.pumpAndSettle();
        await tester.tap(reveal);
        await tester.pumpAndSettle();
        expect(
          find.text(
            copy.format('exchange.raw.body.compressed', {'encoding': encoding}),
          ),
          findsOneWidget,
        );
        expect(
          find.text(copy('exchange.raw.body.compressed_help')),
          findsOneWidget,
        );
        expect(find.text(base64.encode(api.body)), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      });
    }
  }
}

final class _CompressedEvidenceApi implements ControlApi {
  _CompressedEvidenceApi(this.encoding);
  final String encoding;
  final preview = PreviewControlApi();
  // Display-only fixture: the Raw HTTP view must preserve bytes, not attempt
  // to decode them or fabricate semantic content from a partial stream.
  final body = Uint8List.fromList([0x28, 0xb5, 0x2f, 0xfd, 0, 0xff]);
  late RawEvidenceEnvelope envelope;

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
      headerCount: 0,
      trailerCount: 0,
      contentEncoding: encoding,
      contentType: 'application/json',
      bodyBytes: body.length,
      bodySha256: crypto.sha256.convert(body).toString(),
      digestScope: 'full_body',
      payloadState: 'captured',
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
  }) async => RevealedRawEvidence(
    envelope: envelope,
    headers: const [],
    trailers: const [],
    body: body,
    frames: const [],
  );

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

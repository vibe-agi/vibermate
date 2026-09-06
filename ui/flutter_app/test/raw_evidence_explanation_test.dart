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
    for (final entry in {
      'response_payload_limit': 'recording_limit',
      'response_closed_before_eof': 'closed_early',
      'response_read_failed': 'read_failed',
      'other_incomplete_reason': 'incomplete',
    }.entries) {
      testWidgets('Raw prefix explains ${entry.key} in $language', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(1000, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _PrefixEvidenceApi(entry.key);
        final copy = AppCopy.forLanguage(language);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        addTearDown(api.close);
        final page = await api.preview.activities(
          captureRunId: 'run-1',
          limit: 224,
        );
        final activity = page.items.singleWhere(
          (item) => item.id == 'run-1-exchange-222',
        );
        expect(await controller.loadExchangeDetail(activity.id), isNotNull);
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
        final notice = find.byKey(
          const Key('raw-prefix-notice-prefix-fixture'),
        );
        await tester.ensureVisible(notice);
        await tester.pumpAndSettle();
        expect(
          find.text(copy('exchange.raw.prefix.${entry.value}')),
          findsOneWidget,
        );
        expect(find.text(copy('exchange.raw.state.truncated')), findsOneWidget);
        expect(
          find.text(
            copy.format('exchange.raw.bytes_observed', {'bytes': '64.0 KiB'}),
          ),
          findsOneWidget,
        );
        // The explanation must be visible without revealing any private bytes.
        expect(
          find.byKey(const Key('raw-revealed-prefix-fixture')),
          findsNothing,
        );
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      });
    }
  }
}

final class _PrefixEvidenceApi implements ControlApi {
  _PrefixEvidenceApi(this.reason);

  final String reason;
  final preview = PreviewControlApi();

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) => preview.exchange(exchangeId, contentView: contentView);

  @override
  Future<RawEvidencePage> rawEvidence(String exchangeId) async {
    final page = await preview.rawEvidence(exchangeId);
    final sample = page.items.first;
    return RawEvidencePage(
      items: [
        RawEvidenceEnvelope(
          envelopeId: 'prefix-fixture',
          layer: 'provider_response',
          scopeKind: sample.scopeKind,
          scopeId: sample.scopeId,
          exchangeId: exchangeId,
          observedAt: sample.observedAt,
          expiresAt: sample.expiresAt,
          statusCode: 200,
          headerCount: 0,
          trailerCount: 0,
          bodyBytes: 65537,
          digestScope: reason == 'response_payload_limit'
              ? 'full'
              : 'observed_prefix',
          payloadState: 'truncated',
          payloadReason: reason,
          redactedCredentialFields: const [],
          revealAvailable: true,
        ),
      ],
      recovery: page.recovery,
      writer: page.writer,
    );
  }

  @override
  Future<void> close() => preview.close();

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

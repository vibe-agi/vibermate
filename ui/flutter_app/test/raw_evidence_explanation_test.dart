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
    for (final scope in ['full_body', 'observed_prefix', 'unavailable']) {
      testWidgets('Client Body digest explains $scope in $language', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(390, 760));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _PrefixEvidenceApi(
          'recording_metadata_only',
          clientDigestScope: scope,
        );
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
        final digest = find.byKey(const Key('raw-body-digest-digest-fixture'));
        await tester.ensureVisible(digest);
        expect(digest, findsOneWidget);
        expect(find.text(copy('exchange.raw.digest.$scope')), findsOneWidget);
        expect(
          find.text('a' * 64),
          scope == 'unavailable' ? findsNothing : findsOneWidget,
        );
        expect(
          find.byTooltip(copy('exchange.raw.digest.copy')),
          scope == 'unavailable' ? findsNothing : findsOneWidget,
        );
        final help = find.byKey(
          const Key('raw-body-digest-help-digest-fixture'),
        );
        await tester.ensureVisible(help);
        await tester.pumpAndSettle();
        await tester.tap(help);
        await tester.pumpAndSettle();
        expect(find.text(copy('exchange.raw.digest.help')), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      });
    }
  }
}

final class _PrefixEvidenceApi implements ControlApi {
  _PrefixEvidenceApi(this.reason, {this.clientDigestScope});

  final String reason;
  final String? clientDigestScope;
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
    final client = clientDigestScope != null;
    return RawEvidencePage(
      items: [
        RawEvidenceEnvelope(
          envelopeId: client ? 'digest-fixture' : 'prefix-fixture',
          layer: client ? 'client_ingress' : 'provider_response',
          scopeKind: sample.scopeKind,
          scopeId: sample.scopeId,
          exchangeId: exchangeId,
          observedAt: sample.observedAt,
          expiresAt: sample.expiresAt,
          method: client ? 'POST' : null,
          statusCode: client ? null : 200,
          headerCount: 0,
          trailerCount: 0,
          bodyBytes: client
              ? clientDigestScope == 'unavailable'
                    ? 0
                    : 123
              : 65537,
          bodySha256: client && clientDigestScope != 'unavailable'
              ? 'a' * 64
              : null,
          digestScope:
              clientDigestScope ??
              (reason == 'response_payload_limit'
                  ? 'full_body'
                  : 'observed_prefix'),
          payloadState: client
              ? clientDigestScope == 'unavailable'
                    ? 'unavailable'
                    : 'metadata_only'
              : 'truncated',
          payloadReason: client && clientDigestScope == 'unavailable'
              ? 'unavailable'
              : reason,
          redactedCredentialFields: const [],
          revealAvailable: !client,
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

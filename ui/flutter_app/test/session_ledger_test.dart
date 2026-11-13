import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/session_ledger.dart';

ScriptOutcome _script(String outcome, {String name = 'Mask secrets'}) =>
    ScriptOutcome(
      scriptId: 'transform.x',
      revision: 2,
      name: name,
      stage: 'request',
      outcome: outcome,
      blockReason: outcome == 'blocked' ? 'secret-in-prompt' : null,
      applications: 1,
      changed: outcome == 'changed' ? 1 : 0,
      headersChanged: false,
      bodyChanged: outcome == 'changed',
      bytesBefore: 100,
      bytesAfter: outcome == 'changed' ? 90 : 100,
    );

ActivityRecord _activity(
  String status, {
  List<ScriptOutcome> scripts = const [],
  String? reasonCode,
}) => ActivityRecord.fromJson({
  'id': 'exchange-1',
  'occurredAt': '2026-11-12T09:00:00.000Z',
  'kind': 'exchange',
  'title': 'Agent exchange',
  'status': status,
  'reasonCode': ?reasonCode,
  'source': {
    'kind': 'capture_run',
    'displayName': 'codex',
    'recognition': 'verified',
  },
  'conversation': {
    'id': 'capture_run:run-1:main',
    'displayName': 'codex',
    'kind': 'main',
    'evidence': 'capture_run',
  },
  'environment': {
    'id': 'system_transparent',
    'revision': 1,
    'digest': List.filled(64, 'a').join(),
    'clientEndpointId': 'endpoint.system.chatgpt',
    'clientEndpointRevision': 1,
    'protocolPlanId': 'plan.system.chatgpt.responses',
    'protocolPlanRevision': 1,
  },
  'parentRefs': {'captureRunId': 'run-1', 'exchangeId': 'exchange-1'},
  if (scripts.isNotEmpty)
    'scripts': [
      for (final script in scripts)
        {
          'scriptId': script.scriptId,
          'revision': script.revision,
          'name': script.name,
          'stage': script.stage,
          'outcome': script.outcome,
          'blockReason': ?script.blockReason,
          'applications': script.applications,
          'changed': script.changed,
          'headersChanged': script.headersChanged,
          'bodyChanged': script.bodyChanged,
          'bytesBefore': script.bytesBefore,
          'bytesAfter': script.bytesAfter,
        },
    ],
}, 'activity');

void main() {
  final copy = AppCopy.forLanguage(AppLanguage.english);

  test('a script decision outranks the upstream result', () {
    expect(ledgerResultOf(_activity('succeeded')), LedgerResult.forwarded);
    expect(ledgerResultOf(_activity('pending')), LedgerResult.inProgress);
    expect(ledgerResultOf(_activity('canceled')), LedgerResult.canceled);
    expect(
      ledgerResultOf(_activity('succeeded', scripts: [_script('changed')])),
      LedgerResult.changed,
    );
    // A blocked request is recorded as failed; the ledger says why.
    expect(
      ledgerResultOf(
        _activity(
          'failed',
          reasonCode: 'message_transform_failed',
          scripts: [_script('unchanged'), _script('blocked')],
        ),
      ),
      LedgerResult.blocked,
    );
    expect(
      ledgerResultOf(_activity('failed', scripts: [_script('failed')])),
      LedgerResult.scriptFailed,
    );
    // Changed by a script, then refused upstream: an upstream failure.
    expect(
      ledgerResultOf(
        _activity(
          'failed',
          reasonCode: 'provider_status_rejected',
          scripts: [_script('changed')],
        ),
      ),
      LedgerResult.failed,
    );
  });

  test('filters keep a changed request that later failed under both', () {
    final changedThenFailed = _activity(
      'failed',
      reasonCode: 'provider_status_rejected',
      scripts: [_script('changed')],
    );
    expect(
      ledgerFilterMatches(SessionLedgerFilter.changed, changedThenFailed),
      isTrue,
    );
    expect(
      ledgerFilterMatches(SessionLedgerFilter.failed, changedThenFailed),
      isTrue,
    );
    final blocked = _activity('failed', scripts: [_script('blocked')]);
    expect(ledgerFilterMatches(SessionLedgerFilter.blocked, blocked), isTrue);
    expect(ledgerFilterMatches(SessionLedgerFilter.failed, blocked), isFalse);
    expect(
      ledgerFilterMatches(SessionLedgerFilter.changed, _activity('succeeded')),
      isFalse,
    );
  });

  test('the result line names the script and the reason', () {
    expect(
      ledgerResultText(
        copy,
        _activity('succeeded', scripts: [_script('changed')]),
      ),
      'Changed · Mask secrets',
    );
    expect(
      ledgerResultText(
        copy,
        _activity(
          'succeeded',
          scripts: [
            _script('changed'),
            _script('changed', name: 'Hide identity'),
          ],
        ),
      ),
      'Changed · 2 scripts',
    );
    expect(
      ledgerResultText(
        copy,
        _activity('failed', scripts: [_script('blocked')]),
      ),
      'Blocked by script · secret-in-prompt',
    );
    expect(
      ledgerResultText(
        copy,
        _activity('failed', reasonCode: 'provider_transport_failed'),
      ),
      'Failed · provider_transport_failed',
    );
  });

  for (final size in const [Size(1440, 900), Size(390, 844)]) {
    testWidgets('a session opens on its request ledger at ${size.width}px', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(size);
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Claude Code').first);
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('session-ledger-list')), findsOneWidget);
      expect(tester.takeException(), isNull);

      // Blocked: exactly the one request a script refused.
      // On a narrow pane the filter tabs scroll sideways.
      await tester.ensureVisible(
        find.byKey(const Key('ledger-filter-blocked')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('ledger-filter-blocked')));
      await tester.pumpAndSettle();
      final blocked = find.textContaining('Blocked by script');
      expect(blocked, findsOneWidget);
      expect(find.textContaining('destructive-command'), findsOneWidget);

      // Its detail names both scripts and what each did.
      await tester.tap(blocked);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('ledger-inspector')), findsOneWidget);
      expect(
        find.textContaining('Block dangerous commands · r2'),
        findsOneWidget,
      );
      expect(find.textContaining('Mask secrets · r4'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.tap(find.byKey(const Key('ledger-inspector-close')));
      await tester.pumpAndSettle();
      // On a narrow pane the filter tabs scroll sideways.
      await tester.ensureVisible(
        find.byKey(const Key('ledger-filter-changed')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('ledger-filter-changed')));
      await tester.pumpAndSettle();
      expect(find.textContaining('Changed · Mask secrets'), findsWidgets);
      expect(find.textContaining('Blocked by script'), findsNothing);

      // The conversation is one step away and the ledger is still there after.
      await tester.tap(find.byKey(const Key('ledger-read-conversation')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('session-ledger-list')), findsNothing);
      expect(tester.takeException(), isNull);
    });
  }
}

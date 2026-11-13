import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/session_ledger.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

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
  String id = 'exchange-1',
  List<ScriptOutcome> scripts = const [],
  String? reasonCode,
}) => ActivityRecord.fromJson({
  'id': id,
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
  'parentRefs': {'captureRunId': 'run-1', 'exchangeId': id},
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

  test('refreshing the newest page keeps how far back the ledger reaches', () {
    final held = ActivityPage(
      items: [
        _activity('pending', id: 'b'),
        _activity('succeeded', id: 'a'),
      ],
      nextCursor: 'older-than-a',
    );
    final refreshed = mergeLedgerPage(
      held,
      ActivityPage(
        items: [
          _activity('succeeded', id: 'c'),
          _activity('failed', id: 'b', scripts: [_script('blocked')]),
        ],
        nextCursor: 'older-than-b',
      ),
      older: false,
    );
    expect(refreshed.items.map((item) => item.id).toSet(), {'a', 'b', 'c'});
    // The request that was in progress now carries its final result.
    final b = refreshed.items.singleWhere((item) => item.id == 'b');
    expect(ledgerResultOf(b), LedgerResult.blocked);
    expect(refreshed.nextCursor, 'older-than-a');

    final older = mergeLedgerPage(
      refreshed,
      ActivityPage(items: [_activity('succeeded', id: 'z')], nextCursor: null),
      older: true,
    );
    expect(older.items, hasLength(4));
    expect(older.nextCursor, isNull);
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

      // From the detail, the full request opens in place and comes back.
      await tester.ensureVisible(
        find.byKey(const Key('ledger-inspector-open')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('ledger-inspector-open')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('conversation-turn-run-1-exchange-206')),
        findsOneWidget,
      );
      expect(find.byKey(const Key('session-ledger-list')), findsNothing);
      expect(tester.takeException(), isNull);
      await tester.tap(find.byKey(const Key('ledger-back')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('ledger-inspector')), findsOneWidget);

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

      // The ledger covers the whole session by default and can be narrowed
      // to the conversation selected above it.
      final scope = find.byKey(const Key('ledger-scope'));
      await tester.ensureVisible(scope);
      await tester.pumpAndSettle();
      expect(
        find.descendant(of: scope, matching: find.text('Whole session')),
        findsOneWidget,
      );
      await tester.tap(scope);
      await tester.pumpAndSettle();
      await tester.tap(find.text('This conversation').last);
      await tester.pumpAndSettle();
      expect(
        find.descendant(of: scope, matching: find.text('This conversation')),
        findsOneWidget,
      );
      expect(find.byKey(const Key('session-ledger-list')), findsOneWidget);
      expect(tester.takeException(), isNull);

      // The conversation is one step away and the ledger is still there after.
      await tester.tap(find.byKey(const Key('ledger-read-conversation')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('session-ledger-list')), findsNothing);
      expect(tester.takeException(), isNull);
    });
  }
}

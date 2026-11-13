import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> _activity({Object? scripts}) => {
  'id': 'exchange-scripts',
  'occurredAt': '2026-11-12T09:00:00.000Z',
  'kind': 'exchange',
  'title': 'codex',
  'status': 'failed',
  'source': {
    'kind': 'capture_run',
    'displayName': 'codex',
    'recognition': 'verified',
  },
  'conversation': {
    'id': 'capture_run:run-scripts:main',
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
  'parentRefs': {
    'captureRunId': 'run-scripts',
    'exchangeId': 'exchange-scripts',
  },
  'scripts': ?scripts,
};

Map<String, Object?> _outcome({String outcome = 'changed'}) => {
  'scriptId': 'mask',
  'revision': 4,
  'name': 'Mask secrets',
  'stage': 'request',
  'outcome': outcome,
  if (outcome == 'blocked') 'blockReason': 'secret-in-prompt',
  'applications': 1,
  'changed': outcome == 'changed' ? 1 : 0,
  'headersChanged': false,
  'bodyChanged': outcome == 'changed',
  'bytesBefore': 900,
  'bytesAfter': 880,
};

void main() {
  test('a request reports what each script did to it', () {
    final activity = ActivityRecord.fromJson(
      _activity(
        scripts: [
          _outcome(),
          _outcome(outcome: 'blocked'),
        ],
      ),
      'activity',
    );
    expect(activity.scripts, hasLength(2));
    expect(activity.scripts.first.name, 'Mask secrets');
    expect(activity.scripts.first.changedMessage, isTrue);
    expect(activity.scripts.first.bytesAfter, 880);
    expect(activity.scripts.last.blocked, isTrue);
    expect(activity.scripts.last.blockReason, 'secret-in-prompt');
  });

  test('a request no script touched has no outcomes', () {
    expect(ActivityRecord.fromJson(_activity(), 'activity').scripts, isEmpty);
  });

  test('outcomes this build cannot read never hide the request', () {
    final activity = ActivityRecord.fromJson(
      _activity(
        scripts: [
          {..._outcome(), 'outcome': 'quarantined', 'futureField': true},
        ],
      ),
      'activity',
    );
    expect(activity.id, 'exchange-scripts');
    expect(activity.scripts, isEmpty);
  });
}

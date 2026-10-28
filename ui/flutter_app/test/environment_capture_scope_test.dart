import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  Future<JsonObject> environmentWithScope(Object? scope) async {
    final preview = PreviewControlApi();
    addTearDown(preview.close);
    final work = (await preview.loadDashboard()).environments.firstWhere(
      (environment) => environment.id == 'work',
    );
    return {...work.toJson(), 'captureScope': scope};
  }

  const scope = {
    'rules': [
      {'id': 'intranet', 'kind': 'suffix', 'value': 'corp.example'},
      {
        'id': 'dashboard',
        'kind': 'host',
        'value': 'dash.corp.example',
        'port': 443,
      },
    ],
    'allowUserAdditions': true,
  };

  // ADR 0025: the capture scope is administered on the Runtime; editing any
  // other setting in the App must carry it through unchanged.
  test('a capture scope survives loading, editing and saving', () async {
    final record = EnvironmentRecord.fromJson(
      await environmentWithScope(scope),
      'environment',
    );
    expect(record.captureScope!.rules, hasLength(2));
    expect(record.captureScope!.rules[1].port, 443);
    expect(record.captureScope!.allowUserAdditions, isTrue);
    expect(record.toJson()['captureScope'], scope);

    final draft = EnvironmentDraftInput.fromEnvironment(
      record,
      expectedDraftRevision: 0,
      name: 'Renamed',
    );
    expect(draft.toJson()['name'], 'Renamed');
    expect(draft.toJson()['captureScope'], scope);
    expect(
      draft.copyWith(expectedDraftRevision: 2).toJson()['captureScope'],
      scope,
    );
    draft.validateFor(record.id, record.revision);
  });

  test('an Environment without a capture scope sends none', () async {
    final json = await environmentWithScope(null)
      ..remove('captureScope');
    final record = EnvironmentRecord.fromJson(json, 'environment');
    expect(record.captureScope, isNull);
    final draft = EnvironmentDraftInput.fromEnvironment(
      record,
      expectedDraftRevision: 0,
    );
    expect(draft.toJson().containsKey('captureScope'), isFalse);
  });

  test('a malformed capture scope is refused', () async {
    for (final malformed in [
      {
        'rules': [
          {'id': 'r', 'kind': 'regex', 'value': '.*'},
        ],
      },
      {
        'rules': [
          {'id': 'r', 'kind': 'host', 'value': 'a.example', 'port': 0},
        ],
      },
      {
        'rules': [
          {'id': 'r', 'kind': 'host', 'value': 'a.example', 'port': 70000},
        ],
      },
      {
        'rules': [
          {'id': 'r', 'kind': 'host', 'value': 'a.example'},
          {'id': 'r', 'kind': 'host', 'value': 'b.example'},
        ],
      },
      {'rules': [], 'unknown': true},
    ]) {
      expect(
        () async => EnvironmentRecord.fromJson(
          await environmentWithScope(malformed),
          'environment',
        ),
        throwsA(isA<ControlContractException>()),
        reason: '$malformed',
      );
    }
  });
}

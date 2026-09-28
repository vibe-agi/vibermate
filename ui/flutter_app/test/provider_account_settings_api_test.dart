import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> _account({
  int settingsRevision = 4,
  bool automaticRefresh = false,
  EgressProfileRevision? egressProfile,
}) => {
  'id': 'account.settings',
  'displayName': 'Settings fixture',
  'note': '',
  'noteRevision': 0,
  'credentialOrigin': 'https://chatgpt.com',
  'linkedEndpointIds': <String>[],
  'associationRevision': 3,
  'kind': 'codex_oauth',
  'realmId': 'openai.chatgpt',
  'state': 'active',
  'revision': 2,
  'credentialState': 'ready',
  'credentialEpoch': 9,
  'setHeaderNames': <String>[],
  'deleteHeaderNames': <String>[],
  'settingsRevision': settingsRevision,
  'automaticRefresh': automaticRefresh,
  'supportsAutomaticRefresh': true,
  'egressProfile': egressProfile?.toJson(),
  'codexOAuth': {
    'chatgptAccountId': 'workspace',
    'fedRamp': false,
    'state': 'ready',
    'lastRefresh': '2026-09-28T01:00:00Z',
  },
};

void main() {
  test(
    'settings pin their revision and send only published profile identity',
    () async {
      var current = _account();
      var inconsistent = false;
      final client = MockClient((request) async {
        expect(request.method, 'PUT');
        expect(
          request.url.path,
          '/api/v1/provider-accounts/account.settings/settings',
        );
        expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
        expect(request.headers['if-match'], '${current['settingsRevision']}');
        expect(request.headers['idempotency-key'], isNotEmpty);
        final body = jsonDecode(request.body) as Map;
        expect(
          body.keys,
          unorderedEquals(['automaticRefresh', 'egressProfile']),
        );
        if (body['egressProfile'] != null) {
          expect(body['egressProfile'], {
            'id': 'profile.direct',
            'revision': 1,
          });
        }
        final profile = body['egressProfile'] == null
            ? null
            : EgressProfileRevision.direct;
        final changed =
            current['automaticRefresh'] != body['automaticRefresh'] ||
            current['egressProfile'] != profile?.toJson();
        current = _account(
          settingsRevision:
              (current['settingsRevision'] as int) + (changed ? 1 : 0),
          automaticRefresh: body['automaticRefresh'] as bool,
          egressProfile: profile,
        );
        return http.Response(
          jsonEncode({
            ...current,
            if (inconsistent) 'credentialOrigin': 'https://elsewhere.example',
          }),
          200,
          headers: {'content-type': 'application/json'},
        );
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:9666'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'settings-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        client: client,
        inspectSession: false,
      );
      addTearDown(api.close);
      final account = ProviderAccount.fromJson(current, 'account');
      final direct = await api.setProviderAccountSettings(
        account,
        egressProfile: EgressProfileRevision.direct,
        automaticRefresh: true,
      );
      expect(direct.settingsRevision, 5);
      expect(direct.revision, 2);
      expect(direct.credentialEpoch, 9);
      final inherited = await api.setProviderAccountSettings(
        direct,
        egressProfile: null,
        automaticRefresh: false,
      );
      expect(inherited.settingsRevision, 6);
      expect(inherited.egressProfile, isNull);
      expect(inherited.withNote('note', 1).automaticRefresh, isFalse);
      final noChange = await api.setProviderAccountSettings(
        inherited,
        egressProfile: null,
        automaticRefresh: false,
      );
      expect(noChange.settingsRevision, 6);
      inconsistent = true;
      await expectLater(
        api.setProviderAccountSettings(
          noChange,
          egressProfile: null,
          automaticRefresh: true,
        ),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('projection requires complete current account settings', () {
    for (final field in [
      'automaticRefresh',
      'supportsAutomaticRefresh',
      'settingsRevision',
      'egressProfile',
      'note',
      'noteRevision',
    ]) {
      final incomplete = _account()..remove(field);
      expect(
        () => ProviderAccount.fromJson(incomplete, 'account'),
        throwsA(isA<ControlContractException>()),
        reason: 'Missing $field must not guess old account behavior',
      );
    }
    for (final invalid in [
      {..._account(), 'settingsRevision': 0},
      {..._account(), 'settingsRevision': -1},
      {
        ..._account(),
        'supportsAutomaticRefresh': false,
        'automaticRefresh': true,
      },
    ]) {
      expect(
        () => ProviderAccount.fromJson(invalid, 'account'),
        throwsA(isA<ControlContractException>()),
      );
    }
  });
}

import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> _account({
  int settingsRevision = 4,
  bool automaticRefresh = false,
  String cyberAccessProgram = 'daybreak_blue',
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
  'cyberAccessProgram': cyberAccessProgram,
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
      final programWrites = <String>[];
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
          unorderedEquals([
            'automaticRefresh',
            'cyberAccessProgram',
            'egressProfile',
          ]),
        );
        programWrites.add(body['cyberAccessProgram'] as String);
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
            current['cyberAccessProgram'] != body['cyberAccessProgram'] ||
            current['egressProfile'] != profile?.toJson();
        current = _account(
          settingsRevision:
              (current['settingsRevision'] as int) + (changed ? 1 : 0),
          automaticRefresh: body['automaticRefresh'] as bool,
          cyberAccessProgram: body['cyberAccessProgram'] as String,
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
      expect(direct.cyberAccessProgram, 'daybreak_blue');
      expect(direct.withNote('note', 1).cyberAccessProgram, 'daybreak_blue');
      expect(
        direct.withAssociations([], 4).cyberAccessProgram,
        'daybreak_blue',
      );
      final inherited = await api.setProviderAccountSettings(
        direct,
        egressProfile: null,
        automaticRefresh: false,
      );
      expect(inherited.settingsRevision, 6);
      expect(inherited.egressProfile, isNull);
      expect(inherited.withNote('note', 1).automaticRefresh, isFalse);
      final standard = await api.setProviderAccountSettings(
        inherited,
        egressProfile: null,
        automaticRefresh: false,
        cyberAccessProgram: 'standard',
      );
      expect(standard.cyberAccessProgram, 'standard');
      expect(standard.settingsRevision, 7);
      final noChange = await api.setProviderAccountSettings(
        standard,
        egressProfile: null,
        automaticRefresh: false,
      );
      expect(noChange.settingsRevision, 7);
      final red = await api.setProviderAccountSettings(
        noChange,
        egressProfile: null,
        automaticRefresh: false,
        cyberAccessProgram: 'daybreak_red',
      );
      expect(red.cyberAccessProgram, 'daybreak_red');
      expect(red.settingsRevision, 8);
      inconsistent = true;
      await expectLater(
        api.setProviderAccountSettings(
          red,
          egressProfile: null,
          automaticRefresh: true,
        ),
        throwsA(isA<ControlContractException>()),
      );
      expect(programWrites, [
        'daybreak_blue',
        'daybreak_blue',
        'standard',
        'standard',
        'daybreak_red',
        'daybreak_red',
      ]);
    },
  );

  test('projection requires complete current account settings', () {
    final legacy = _account()..remove('cyberAccessProgram');
    expect(
      ProviderAccount.fromJson(legacy, 'account').cyberAccessProgram,
      'standard',
    );
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
      {..._account(), 'cyberAccessProgram': null},
      {..._account(), 'cyberAccessProgram': 'true'},
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

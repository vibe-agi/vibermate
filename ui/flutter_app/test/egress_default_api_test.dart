import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> defaultWire({
  int revision = 7,
  EgressProfileRevision? profile,
}) => {
  'revision': revision,
  'profile': profile?.toJson(),
  'effective': {
    'profile': (profile ?? EgressProfileRevision.direct).toJson(),
    'source': profile == null ? 'builtin' : 'global',
    'defaultRevision': revision,
  },
};

Future<HttpControlApi> defaultAPI(MockClient client) => HttpControlApi.connect(
  DesktopSession(
    instanceId: 'default-contract',
    baseUrl: Uri.parse('http://127.0.0.1:18662'),
    readToken: 'R' * 43,
    writeToken: 'W' * 43,
    expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
  ),
  inspectSession: false,
  client: client,
);

void main() {
  test(
    'Default response rejects inconsistent selection rather than inventing Direct',
    () {
      for (final edge in [
        'missing',
        'extra',
        'alias',
        'revision-zero',
        'effective-revision',
        'builtin-profile',
        'builtin-substitution',
        'global-missing',
        'global-different',
        'foreign-source',
      ]) {
        final wire = defaultWire();
        final effective = wire['effective']! as Map<String, Object?>;
        switch (edge) {
          case 'missing':
            wire.remove('profile');
          case 'extra':
            wire['unexpected'] = true;
          case 'alias':
            wire['Revision'] = wire.remove('revision');
          case 'revision-zero':
            wire['revision'] = 0;
          case 'effective-revision':
            effective['defaultRevision'] = 6;
          case 'builtin-profile':
            wire['profile'] = EgressProfileRevision.direct.toJson();
          case 'builtin-substitution':
            (effective['profile']! as Map<String, Object?>)['displayName'] =
                'Forged direct';
          case 'global-missing':
            effective['source'] = 'global';
          case 'global-different':
            wire['profile'] = {
              ...EgressProfileRevision.direct.toJson(),
              'id': 'profile.other',
            };
            effective['source'] = 'global';
          case 'foreign-source':
            effective['source'] = 'account';
        }
        expect(
          () => EgressDefaultSetting.fromJson(wire),
          throwsA(isA<ControlContractException>()),
          reason: edge,
        );
      }
    },
  );

  test(
    'Default mutation refuses forged published profile before owner HTTP',
    () async {
      var requests = 0;
      final api = await defaultAPI(
        MockClient((request) async {
          requests++;
          return http.Response(jsonEncode(defaultWire(revision: 8)), 200);
        }),
      );
      addTearDown(api.close);
      final invalid = EgressProfileRevision(
        id: 'profile.direct',
        revision: 1,
        displayName: 'Not builtin',
        policy: const TrafficEgressPolicy.direct(),
        publishedAt: DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
      );
      await expectLater(
        api.setEgressDefault(
          EgressDefaultSetting.fromJson(defaultWire()),
          invalid,
        ),
        throwsA(isA<ControlContractException>()),
      );
      expect(requests, 0);
    },
  );

  test(
    'Clearing sends explicit null and unchanged explicit Direct keeps revision',
    () async {
      var calls = 0;
      final api = await defaultAPI(
        MockClient((request) async {
          calls++;
          expect(request.method, 'PUT');
          expect(request.url.path, '/api/v1/egress-default');
          expect(request.headers['if-match'], '7');
          expect(request.headers['idempotency-key'], isNotEmpty);
          expect(jsonDecode(request.body), {
            'profile': calls == 1
                ? {'id': 'profile.direct', 'revision': 1}
                : null,
          });
          return http.Response(
            jsonEncode(
              defaultWire(
                revision: calls == 1 ? 7 : 8,
                profile: calls == 1 ? EgressProfileRevision.direct : null,
              ),
            ),
            200,
            headers: {'content-type': 'application/json; charset=utf-8'},
          );
        }),
      );
      addTearDown(api.close);
      final current = EgressDefaultSetting.fromJson(
        defaultWire(profile: EgressProfileRevision.direct),
      );
      final unchanged = await api.setEgressDefault(
        current,
        EgressProfileRevision.direct,
      );
      expect(unchanged.revision, 7);
      expect(unchanged.source, 'global');
      final cleared = await api.setEgressDefault(unchanged, null);
      expect(cleared.revision, 8);
      expect(cleared.source, 'builtin');
      expect(cleared.profile, isNull);
      expect(calls, 2);
    },
  );
}

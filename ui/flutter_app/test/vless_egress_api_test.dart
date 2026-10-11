import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'egress_default_api_test.dart' show defaultAPI;

const syntheticNode =
    'vless://00000000-0000-4000-8000-000000000001@node.invalid:443?security=reality&flow=xtls-rprx-vision';
Map<String, Object?> savedWire({
  String id = 'profile.fixture',
  int revision = 1,
}) => {
  'id': id,
  'revision': revision,
  'displayName': 'Chosen exit',
  'policy': {
    'proxy': {
      'kind': 'vless',
      'credentialRef': 'secret://egress-vless/fixture',
      'credentialRevision': revision,
    },
    'resolver': {'kind': 'system', 'transport': 'direct'},
    'directDomains': '*.github.com\ngithub.com',
  },
  'publishedAt': '2026-10-10T00:00:00Z',
};
Future<EgressProfileRevision> importNode(
  ControlApi api, {
  String id = 'profile.fixture',
  int revision = 0,
}) async => await api.importVlessEgressProfile(
  id: id,
  expectedRevision: revision,
  displayName: 'Chosen exit',
  uri: syntheticNode,
  resolver: const TrafficResolverPolicy.system(),
  directDomains: 'github.com\n*.github.com\ngithub.com',
);

void main() {
  for (final response in [
    http.Response('{"security":"tls"}', 200),
    http.Response('{"security":"tls","trustMode":"skip"}', 200),
    http.Response('{"security":"reality","trustMode":"system"}', 200),
    http.Response('{}', 503),
  ]) {
    test(
      'unavailable or malformed settings fail closed ${response.statusCode}/${response.body}',
      () async {
        var calls = 0;
        final api = await defaultAPI(
          MockClient((request) async {
            calls++;
            return response;
          }),
        );
        addTearDown(api.close);
        await expectLater(
          api.vlessProfileDescription('profile.fixture', 1),
          throwsA(
            response.statusCode == 200
                ? isA<ControlContractException>()
                : isA<ControlProblem>(),
          ),
        );
        expect(calls, 1);
      },
    );
  }
  test(
    'exact revision settings GET uses read transport and strict response',
    () async {
      var calls = 0;
      final api = await defaultAPI(
        MockClient((request) async {
          calls++;
          expect(request.method, 'GET');
          expect(
            request.url.path,
            '/api/v1/egress-profiles/profile.fixture/revisions/3/vless-settings',
          );
          expect(request.headers['authorization'], 'Bearer ${'R' * 43}');
          return http.Response(
            '{"security":"tls","trustMode":"custom_ca"}',
            200,
          );
        }),
      );
      addTearDown(api.close);
      final settings = await api.vlessProfileDescription('profile.fixture', 3);
      expect(settings.trustMode, 'custom_ca');
      for (final args in [
        (id: '../bad', rev: 1),
        (id: 'profile.fixture', rev: 0),
      ]) {
        await expectLater(
          api.vlessProfileDescription(args.id, args.rev),
          throwsA(isA<ControlContractException>()),
        );
      }
      expect(calls, 1);
    },
  );
  for (final tls in [
    null,
    const VlessTLSImportOptions.system(),
    const VlessTLSImportOptions.customCA('fixture PEM'),
  ]) {
    test(
      'private trust wire ${tls?.mode ?? 'omitted'} preserves empty URI and exact revision',
      () async {
        var calls = 0;
        final api = await defaultAPI(
          MockClient((request) async {
            calls++;
            final body = jsonDecode(request.body) as Map<String, dynamic>;
            expect(body['uri'], '');
            expect(request.headers['if-match'], '3');
            expect(request.headers['idempotency-key'], isNotEmpty);
            if (tls == null) {
              expect(body.containsKey('tls'), isFalse);
            } else {
              expect(
                body['tls'],
                tls.mode == 'system'
                    ? {'mode': 'system'}
                    : {'mode': 'custom_ca', 'caPem': 'fixture PEM'},
              );
            }
            return http.Response(jsonEncode(savedWire(revision: 4)), 200);
          }),
        );
        addTearDown(api.close);
        await api.importVlessEgressProfile(
          id: 'profile.fixture',
          expectedRevision: 3,
          displayName: 'Chosen exit',
          uri: '',
          resolver: const TrafficResolverPolicy.system(),
          directDomains: '',
          tls: tls,
        );
        expect(calls, 1);
      },
    );
  }
  test('empty or oversized custom CA never sends a private mutation', () async {
    var calls = 0;
    final api = await defaultAPI(
      MockClient((request) async {
        calls++;
        return http.Response('{}', 200);
      }),
    );
    addTearDown(api.close);
    for (final pem in ['', ' ', 'a' * (64 * 1024 + 1), '中' * 22000]) {
      await expectLater(
        api.importVlessEgressProfile(
          id: 'profile.fixture',
          expectedRevision: 1,
          displayName: 'Chosen',
          uri: '',
          resolver: const TrafficResolverPolicy.system(),
          directDomains: '',
          tls: VlessTLSImportOptions.customCA(pem),
        ),
        throwsA(isA<ControlContractException>()),
      );
    }
    expect(calls, 0);
  });
  for (final revision in [0, 3]) {
    test(
      'private import revision $revision sends authenticated exact wire and returns normalized readback',
      () async {
        var calls = 0;
        final api = await defaultAPI(
          MockClient((request) async {
            calls++;
            expect(request.method, 'POST');
            expect(request.url.path, '/api/v1/egress-profiles/import-vless');
            expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
            expect(request.headers['if-match'], '$revision');
            expect(request.headers['idempotency-key'], isNotEmpty);
            expect(jsonDecode(request.body), {
              'id': 'profile.fixture',
              'displayName': 'Chosen exit',
              'uri': syntheticNode,
              'resolver': {'kind': 'system', 'transport': 'direct'},
              'directDomains': 'github.com\n*.github.com\ngithub.com',
            });
            return http.Response(
              jsonEncode(savedWire(revision: revision + 1)),
              200,
            );
          }),
        );
        addTearDown(api.close);
        final saved = await importNode(api, revision: revision);
        expect(
          saved.toJson(),
          savedWire(revision: revision + 1)
            ..['publishedAt'] = '2026-10-10T00:00:00.000Z',
        );
        expect(jsonEncode(saved.toJson()), isNot(contains(syntheticNode)));
        expect(calls, 1);
      },
    );
  }
  test('invalid authority sends nothing', () async {
    var calls = 0;
    final api = await defaultAPI(
      MockClient((request) async {
        calls++;
        return http.Response('{}', 200);
      }),
    );
    addTearDown(api.close);
    for (final args in [
      (id: '../escape', revision: 0),
      (id: 'profile.direct', revision: 0),
      (id: 'profile.fixture', revision: -1),
    ]) {
      await expectLater(
        importNode(api, id: args.id, revision: args.revision),
        throwsA(isA<ControlContractException>()),
      );
    }
    expect(calls, 0);
  });
  for (final response in [
    http.Response('', 200),
    http.Response('{}', 200),
    http.Response(jsonEncode(savedWire(id: 'profile.other')), 200),
    http.Response(jsonEncode(savedWire(revision: 2)), 200),
    for (final status in [400, 409, 503]) http.Response('{}', status),
  ]) {
    test(
      'unsafe acknowledgement ${response.statusCode}/${response.body.length} never fabricates success',
      () async {
        var calls = 0;
        final api = await defaultAPI(
          MockClient((request) async {
            calls++;
            return response;
          }),
        );
        addTearDown(api.close);
        await expectLater(
          importNode(api),
          throwsA(
            response.statusCode == 200
                ? isA<ControlContractException>()
                : isA<ControlProblem>(),
          ),
        );
        expect(calls, 1);
      },
    );
  }
  test('network failure never automatically replays private import', () async {
    var calls = 0;
    final api = await defaultAPI(
      MockClient((request) async {
        calls++;
        throw http.ClientException('synthetic unavailable');
      }),
    );
    addTearDown(api.close);
    await expectLater(importNode(api), throwsA(isA<http.ClientException>()));
    expect(calls, 1);
  });
}

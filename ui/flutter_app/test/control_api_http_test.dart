import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart' as crypto;

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

import 'runtime_usage_fixture.dart';

void main() {
  test('HTTP API reads Server access and manages Runtime Users', () async {
    final requests = <({String method, String path, String token})>[];
    final bodies = <Map<String, Object?>>[];
    var state = 'active';
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add((
        method: request.method,
        path: request.uri.path,
        token: request.headers.value(HttpHeaders.authorizationHeader) ?? '',
      ));
      request.response.headers.contentType = ContentType.json;
      if (request.method == 'GET' &&
          request.uri.path == '/api/v1/server/access') {
        await request.drain<void>();
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-server-access-v1',
            'transport': 'http',
            'authentication': 'runtime_user_password',
            'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          }),
        );
      } else if (request.method == 'GET' &&
          request.uri.path == '/api/v1/server/runtime-users') {
        await request.drain<void>();
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-runtime-user-list-v1',
            'items': [
              {
                'id': 'user.test',
                'username': 'alice',
                'state': state,
                'createdAt': '2026-08-24T12:00:00.000Z',
                'updatedAt': '2026-08-24T12:00:00.000Z',
              },
            ],
          }),
        );
      } else if (request.method == 'GET' &&
          request.uri.path == '/api/v1/server/runtime-users/usage') {
        await request.drain<void>();
        request.response.write(jsonEncode(runtimeUsagePayload()));
      } else if (request.method == 'POST' &&
          request.uri.path == '/api/v1/server/runtime-users') {
        final body = Map<String, Object?>.from(
          jsonDecode(await utf8.decoder.bind(request).join()) as Map,
        );
        bodies.add(body);
        request.response.statusCode = HttpStatus.created;
        request.response.write(
          jsonEncode({
            'id': 'user.bob',
            'username': body['username'],
            'state': 'active',
            'createdAt': '2026-08-24T13:00:00.000Z',
            'updatedAt': '2026-08-24T13:00:00.000Z',
          }),
        );
      } else if (request.method == 'PATCH' &&
          request.uri.path == '/api/v1/server/runtime-users/user.test') {
        final body = Map<String, Object?>.from(
          jsonDecode(await utf8.decoder.bind(request).join()) as Map,
        );
        bodies.add(body);
        state = body['state']! as String;
        request.response.write(
          jsonEncode({
            'id': 'user.test',
            'username': 'alice',
            'state': state,
            'createdAt': '2026-08-24T12:00:00.000Z',
            'updatedAt': '2026-08-24T13:30:00.000Z',
          }),
        );
      } else {
        await request.drain<void>();
        request.response.statusCode = HttpStatus.notFound;
        await request.response.close();
        return;
      }
      await request.response.close();
    });

    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: List.filled(43, 'R').join(),
        writeToken: List.filled(43, 'W').join(),
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
      inspectSession: false,
    );
    addTearDown(api.close);

    final access = await api.serverAccess();
    expect(access.transport, 'http');
    final users = await api.runtimeUsers();
    expect(users.single.username, 'alice');
    final usage = await api.runtimeUsage();
    expect(usage.users.single.username, 'alice');
    expect(usage.users.single.turns, 2);
    expect(
      usage.users.single.models.single.upstreamModel,
      'relay:model/custom',
    );
    final created = await api.createRuntimeUser(
      username: 'bob',
      password: 'test-password',
    );
    expect(created.username, 'bob');
    final disabled = await api.disableRuntimeUser(users.single.id);
    expect(disabled.active, isFalse);
    expect(bodies, [
      {
        'schema': 'vibermate-runtime-user-create-v1',
        'username': 'bob',
        'password': 'test-password',
      },
      {'schema': 'vibermate-runtime-user-update-v1', 'state': 'disabled'},
    ]);
    expect(requests.map((request) => request.path), [
      '/api/v1/server/access',
      '/api/v1/server/runtime-users',
      '/api/v1/server/runtime-users/usage',
      '/api/v1/server/runtime-users',
      '/api/v1/server/runtime-users/user.test',
    ]);
    expect(requests.first.token, 'Bearer ${List.filled(43, 'R').join()}');
    expect(requests[1].token, 'Bearer ${List.filled(43, 'R').join()}');
    expect(requests[2].token, 'Bearer ${List.filled(43, 'R').join()}');
    expect(requests[3].token, 'Bearer ${List.filled(43, 'W').join()}');
    expect(requests.last.token, 'Bearer ${List.filled(43, 'W').join()}');
  });

  test('HTTP API reads one exact Environment revision authority', () async {
    final preview = PreviewControlApi();
    addTearDown(preview.close);
    final work = (await preview.loadDashboard()).environments.firstWhere(
      (environment) => environment.id == 'work',
    );
    final requests = <Uri>[];
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add(request.uri);
      await request.drain<void>();
      request.response.headers.contentType = ContentType.json;
      if (request.uri.path == '/api/v1/auth/sessions/current') {
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-app-session-state-v1',
            'revision': 1,
            'expiresAt': DateTime.now()
                .toUtc()
                .add(const Duration(hours: 1))
                .toIso8601String(),
          }),
        );
      } else if (request.uri.path.startsWith(
        '/api/v1/environments/work/revisions/',
      )) {
        request.response.write(jsonEncode(work.toJson()));
      } else {
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });

    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: List.filled(43, 'R').join(),
        writeToken: List.filled(43, 'W').join(),
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);

    final historical = await api.environmentRevision('work', work.revision);
    expect(historical.id, 'work');
    expect(historical.revision, work.revision);
    expect(
      requests.last.toString(),
      '/api/v1/environments/work/revisions/${work.revision}',
    );

    await expectLater(
      api.environmentRevision('work', work.revision + 1),
      throwsA(isA<ControlContractException>()),
    );
    final requestCount = requests.length;
    await expectLater(
      api.environmentRevision('../work', work.revision),
      throwsA(isA<ControlContractException>()),
    );
    await expectLater(
      api.environmentRevision('work', 0),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests, hasLength(requestCount));
  });

  test('HTTP API creates one explicit multi-protocol Endpoint', () async {
    final requests = <({String method, Uri uri})>[];
    final createBodies = <Map<String, Object?>>[];
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add((method: request.method, uri: request.uri));
      request.response.headers.contentType = ContentType.json;
      if (request.uri.path == '/api/v1/auth/sessions/current') {
        await request.drain<void>();
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-app-session-state-v1',
            'revision': 1,
            'expiresAt': DateTime.now()
                .toUtc()
                .add(const Duration(hours: 1))
                .toIso8601String(),
          }),
        );
      } else if (request.method == 'POST' &&
          request.uri.path == '/api/v1/upstream-endpoints') {
        final body = jsonDecode(await utf8.decoder.bind(request).join());
        final input = Map<String, Object?>.from(body as Map);
        createBodies.add(input);
        request.response.statusCode = HttpStatus.created;
        request.response.write(
          jsonEncode({
            'id': input['id'],
            'displayName': input['displayName'],
            'origin': input['origin'],
            'realmId': input['id'],
            'backendProtocols': input['backendProtocols'],
            'capabilities': ['messages', 'streaming', 'tool_calls'],
            'accountKinds': ['anthropic_api_key', 'bearer_token'],
            'state': 'active',
            'revision': 1,
          }),
        );
      } else {
        await request.drain<void>();
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });

    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: List.filled(43, 'R').join(),
        writeToken: List.filled(43, 'W').join(),
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);

    final endpoint = await api.createUpstreamEndpoint(
      id: 'target.custom.anthropic.http-test',
      displayName: 'Spark',
      origin: 'http://spark-2a59:8888',
      backendProtocols: const [
        'anthropic_messages',
        'openai_responses',
        'openai_chat',
      ],
    );
    expect(endpoint.origin.toString(), 'http://spark-2a59:8888');
    expect(createBodies.single['origin'], 'http://spark-2a59:8888');
    expect(createBodies.single['backendProtocols'], [
      'anthropic_messages',
      'openai_responses',
      'openai_chat',
    ]);
    expect(createBodies.single.containsKey('kind'), isFalse);
    expect(requests.where((request) => request.method == 'POST'), hasLength(1));

    final requestCount = requests.length;
    await expectLater(
      api.createUpstreamEndpoint(
        id: 'target.custom.anthropic.smart-dash',
        displayName: 'Invalid dash',
        origin: 'http://spark–2a59:8888',
        backendProtocols: const ['anthropic_messages'],
      ),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests, hasLength(requestCount));

    for (final protocols in const <List<String>>[
      [],
      ['anthropic_messages', 'anthropic_messages'],
      ['provider_inferred'],
    ]) {
      await expectLater(
        api.createUpstreamEndpoint(
          id: 'target.custom.invalid-protocols',
          displayName: 'Invalid protocols',
          origin: 'https://relay.example',
          backendProtocols: protocols,
        ),
        throwsA(isA<ControlContractException>()),
      );
    }
    expect(requests, hasLength(requestCount));
  });

  test(
    'HTTP API reads and explicitly refreshes an Endpoint model catalog',
    () async {
      final requests = <Uri>[];
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      server.listen((request) async {
        requests.add(request.uri);
        await request.drain<void>();
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': DateTime.now()
                  .toUtc()
                  .add(const Duration(hours: 1))
                  .toIso8601String(),
            }),
          );
        } else if (request.uri.path == '/api/v1/client-models') {
          request.response.write(
            jsonEncode({
              'protocol': 'anthropic_messages',
              'providerId': 'anthropic',
              'metadataSource': 'models.dev',
              'models': [
                {
                  'id': 'claude-opus-4-1',
                  'canonicalId': 'anthropic/claude-opus-4-1',
                  'displayName': 'Claude Opus 4.1',
                  'description': '',
                  'family': 'claude-opus',
                  'reasoning': true,
                  'toolCalls': true,
                  'structuredOutput': true,
                  'attachments': true,
                  'openWeights': false,
                  'contextLimit': 200000,
                  'outputLimit': 32000,
                  'inputModalities': ['text', 'image'],
                  'outputModalities': ['text'],
                  'knowledgeCutoff': '',
                  'releaseDate': '2025-08-05',
                },
              ],
            }),
          );
        } else if (request.uri.path ==
            '/api/v1/upstream-endpoints/target.spark.local/models') {
          request.response.write(
            jsonEncode({
              'endpointId': 'target.spark.local',
              'endpointRevision': 2,
              'accountId': 'account.spark.models',
              'accountRevision': 4,
              'credentialEpoch': 7,
              'observedAt': '2026-08-20T03:04:05.000Z',
              'availabilitySource': 'endpoint',
              'models': [
                {
                  'id': 'dashscope:deepseek-v4-flash-0731',
                  'displayName': '',
                  'ownedBy': '',
                  'verifiedAvailable': true,
                  'contextLimit': 0,
                  'outputLimit': 0,
                },
              ],
            }),
          );
        } else {
          request.response.statusCode = HttpStatus.notFound;
        }
        await request.response.close();
      });

      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: List.filled(43, 'R').join(),
          writeToken: List.filled(43, 'W').join(),
          instanceId: 'instance-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);

      final cached = await api.upstreamModels(
        'target.spark.local',
        accountId: 'account.spark.models',
      );
      final refreshed = await api.upstreamModels(
        'target.spark.local',
        accountId: 'account.spark.models',
        refresh: true,
      );
      final client = await api.clientModels('anthropic_messages');
      expect(cached.models.single.id, 'dashscope:deepseek-v4-flash-0731');
      expect(refreshed.endpointRevision, 2);
      expect(client.models.single.id, 'claude-opus-4-1');
      expect(
        requests.singleWhere((uri) => uri.path == '/api/v1/client-models'),
        Uri.parse('/api/v1/client-models?protocol=anthropic_messages'),
      );
      expect(requests.where((uri) => uri.path.endsWith('/models')).toList(), [
        Uri.parse(
          '/api/v1/upstream-endpoints/target.spark.local/models?accountId=account.spark.models',
        ),
        Uri.parse(
          '/api/v1/upstream-endpoints/target.spark.local/models?accountId=account.spark.models&refresh=1',
        ),
      ]);
    },
  );

  test(
    'HTTP API sends one canonical mapped draft before preview and publish',
    () async {
      final preview = PreviewControlApi();
      addTearDown(preview.close);
      final dashboard = await preview.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (environment) => environment.id == 'work',
      );
      final endpoint = work.clientEndpoints.first;
      final plan = endpoint.protocolPlans.first;
      final route = plan.routes.firstWhere(
        (candidate) => candidate.id == 'anthropic-direct',
      );
      final account = dashboard.accounts.firstWhere(
        (candidate) => candidate.id == 'anthropic-lab',
      );
      final accountEdited = assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: account,
      );
      final locallyEdited = assignEnvironmentRouteModelMappings(
        endpoints: accountEdited,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        mappings: const [
          EnvironmentModelMapping(
            requestedModel: 'claude-sonnet-4-5',
            upstreamModel: 'dashscope:deepseek-v4-flash-0731',
          ),
          EnvironmentModelMapping(
            requestedModel: 'claude-opus-4-1',
            upstreamModel: 'relay/custom:model_2',
          ),
        ],
      );
      final cumulativeRoute = locallyEdited
          .firstWhere((candidate) => candidate.id == endpoint.id)
          .protocolPlans
          .firstWhere((candidate) => candidate.id == plan.id)
          .routes
          .firstWhere((candidate) => candidate.id == route.id);
      expect(cumulativeRoute.revision, route.revision + 2);

      final normalized = normalizeEnvironmentDraftRevisions(
        base: work.clientEndpoints,
        edited: locallyEdited,
      );
      final normalizedRoute = normalized
          .firstWhere((candidate) => candidate.id == endpoint.id)
          .protocolPlans
          .firstWhere((candidate) => candidate.id == plan.id)
          .routes
          .firstWhere((candidate) => candidate.id == route.id);
      final input = EnvironmentDraftInput.fromEnvironment(
        work,
        expectedDraftRevision: 0,
        name: 'Work mapped',
        clientEndpoints: normalized,
      );
      final digest = List.filled(64, 'b').join();
      final candidate = work.copyWith(
        name: 'Work mapped',
        revision: work.revision + 1,
        digest: digest,
        clientEndpoints: normalized,
      );
      final impactJson = <String, Object?>{
        'environmentId': work.id,
        'baseRevision': work.revision,
        'draftRevision': 1,
        'candidateDigest': digest,
        'continuingCaptures': <Object?>[],
      };
      final requests =
          <({String method, Uri uri, String? ifMatch, List<int> body})>[];
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      server.listen((request) async {
        final body = await request.fold<List<int>>(
          <int>[],
          (bytes, chunk) => bytes..addAll(chunk),
        );
        requests.add((
          method: request.method,
          uri: request.uri,
          ifMatch: request.headers.value(HttpHeaders.ifMatchHeader),
          body: List.unmodifiable(body),
        ));
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': DateTime.now()
                  .toUtc()
                  .add(const Duration(hours: 1))
                  .toIso8601String(),
            }),
          );
        } else if (request.method == 'PUT' &&
            request.uri.path == '/api/v1/environments/work/draft') {
          request.response.write(
            jsonEncode({
              'environmentId': work.id,
              'baseRevision': work.revision,
              'draftRevision': 1,
              'candidateDigest': digest,
              'candidate': candidate.toJson(),
            }),
          );
        } else if (request.method == 'POST' &&
            request.uri.path ==
                '/api/v1/environments/work/draft/actions/preview') {
          request.response.write(jsonEncode(impactJson));
        } else if (request.method == 'POST' &&
            request.uri.path ==
                '/api/v1/environments/work/draft/actions/publish') {
          request.response.write(
            jsonEncode({
              'outcome': 'committed',
              'environment': candidate.toJson(),
              'impact': impactJson,
            }),
          );
        } else {
          request.response.statusCode = HttpStatus.notFound;
        }
        await request.response.close();
      });

      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: List.filled(43, 'R').join(),
          writeToken: List.filled(43, 'W').join(),
          instanceId: 'instance-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);

      final saved = await api.saveEnvironmentDraft(
        environmentId: work.id,
        expectedBaseRevision: work.revision,
        input: input,
      );
      final impact = await api.previewEnvironmentDraft(
        work.id,
        saved.draftRevision,
      );
      final published = await api.publishEnvironmentDraft(
        work.id,
        saved.draftRevision,
      );
      expect(impact.candidateDigest, digest);
      expect(published.environment.routes, hasLength(work.routes.length));

      final put = requests.singleWhere((request) => request.method == 'PUT');
      expect(put.uri.path, '/api/v1/environments/work/draft');
      expect(put.ifMatch, '${work.revision}');
      final wireBody = jsonDecode(utf8.decode(put.body));
      expect(wireBody, input.toJson());
      expect(normalizedRoute.revision, route.revision + 1);
      expect(
        normalizedRoute.modelPolicy.revision,
        route.modelPolicy.revision + 1,
      );
      expect(normalizedRoute.modelPolicy.mode, 'map');
      expect(
        normalizedRoute.modelPolicy.mappings.map(
          (mapping) => '${mapping.requestedModel}->${mapping.upstreamModel}',
        ),
        [
          'claude-opus-4-1->relay/custom:model_2',
          'claude-sonnet-4-5->dashscope:deepseek-v4-flash-0731',
        ],
      );
      final wireRoute =
          ((wireBody as Map<String, Object?>)['clientEndpoints']!
                      as List<Object?>)
                  .cast<Map>()
                  .firstWhere(
                    (candidate) => candidate['id'] == endpoint.id,
                  )['protocolPlans']
              as List<Object?>;
      final wirePlan = wireRoute.cast<Map>().firstWhere(
        (candidate) => candidate['id'] == plan.id,
      );
      final wireDestination = wirePlan['destination'] as Map;
      final wireUpstream = wireDestination['upstream'] as Map;
      final wireModelPolicy =
          (wireUpstream['routes'] as List<Object?>).cast<Map>().firstWhere(
                (candidate) => candidate['id'] == route.id,
              )['modelPolicy']
              as Map;
      expect(wireModelPolicy['mappings'], hasLength(2));

      final actions = requests.where((request) => request.method == 'POST');
      expect(actions.map((request) => request.uri.path), [
        '/api/v1/environments/work/draft/actions/preview',
        '/api/v1/environments/work/draft/actions/publish',
      ]);
      expect(actions.every((request) => request.body.isEmpty), isTrue);
      expect(actions.every((request) => request.ifMatch == '1'), isTrue);
    },
  );

  test('HTTP API follows the opaque Capture continuation cursor', () async {
    final requests = <Uri>[];
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add(request.uri);
      await request.drain<void>();
      request.response.headers.contentType = ContentType.json;
      if (request.uri.path == '/api/v1/auth/sessions/current') {
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-app-session-state-v1',
            'revision': 1,
            'expiresAt': DateTime.now()
                .toUtc()
                .add(const Duration(hours: 1))
                .toIso8601String(),
          }),
        );
      } else if (request.uri.path == '/api/v1/captures') {
        request.response.write(
          jsonEncode({
            'items': [
              {
                'key': 'manual_capture:older-proxy',
                'id': 'older-proxy',
                'kind': 'manual_capture',
                'displayName': 'Older proxy',
                'state': 'revoked',
                'observation': 'waiting_for_traffic',
                'createdAt': '2026-08-08T09:00:00.000Z',
                'updatedAt': '2026-08-08T09:01:00.000Z',
                'manualCapture': {
                  'clientClass': 'desktop_app',
                  'lifetime': 'until_revoked',
                  'credentialRevision': 2,
                },
              },
            ],
            'nextCursor': 'next_capture_cursor',
          }),
        );
      } else {
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });

    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: List.filled(43, 'R').join(),
        writeToken: List.filled(43, 'W').join(),
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);

    final page = await api.captures(
      cursor: 'current_capture_cursor',
      limit: 17,
    );
    expect(page.items.single.key, 'manual_capture:older-proxy');
    expect(page.nextCursor, 'next_capture_cursor');
    expect(requests.last.queryParameters, {
      'limit': '17',
      'cursor': 'current_capture_cursor',
    });
    await expectLater(
      api.captures(limit: 200),
      throwsA(isA<ControlContractException>()),
    );
  });

  test(
    'HTTP API preserves protocol-proven agent conversation evidence',
    () async {
      final requests = <Uri>[];
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      server.listen((request) async {
        requests.add(request.uri);
        await request.drain<void>();
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': DateTime.now()
                  .toUtc()
                  .add(const Duration(hours: 1))
                  .toIso8601String(),
            }),
          );
        } else if (request.uri.path == '/api/v1/exchanges/_exchange-agent') {
          request.response.write(
            jsonEncode({
              'id': '_exchange-agent',
              'status': 'succeeded',
              'environment': {
                'id': 'work',
                'revision': 7,
                'digest': List.filled(64, 'a').join(),
                'clientEndpointId': 'endpoint.codex',
                'clientEndpointRevision': 2,
                'protocolPlanId': 'plan.codex',
                'protocolPlanRevision': 3,
                'routeId': 'route.codex',
                'routeRevision': 5,
              },
              'parentRefs': {
                'exchangeId': '_exchange-agent',
                'captureRunId': 'run-agent',
              },
              'processingTrace': {
                'pluginRunIds': <String>[],
                'attempts': <Object>[],
                'result': 'succeeded',
              },
              'content': {
                'state': 'recorded',
                'mode': 'full',
                'recordedAt': '2026-08-12T04:00:00.000Z',
                'expiresAt': '2026-09-11T04:00:00.000Z',
                'requestProjection': {
                  'view': 'incremental',
                  'relationship': 'checkpoint',
                  'inheritedMessageCount': 0,
                  'totalMessageCount': 1,
                  'fullSnapshotAvailable': false,
                },
                'agentConversation': {
                  'scope': 'capture_run',
                  'agents': [
                    {'name': 'root'},
                    {'name': 'reviewer'},
                  ],
                  'relationships': [
                    {'source': 'root', 'target': 'reviewer', 'kind': 'message'},
                  ],
                  'actions': [
                    {
                      'callId': 'agent-call-1',
                      'name': 'spawn_agent',
                      'status': 'completed',
                      'sourceAgent': 'root',
                      'resultAgent': 'reviewer',
                      'attributed': true,
                    },
                  ],
                },
                'request': {
                  'system': <Object?>[],
                  'requestedModel': 'gpt-5.6-sol',
                  'effectiveModel': 'gpt-5.6-sol',
                  'maxOutputTokens': 0,
                  'stream': true,
                  'messages': [
                    {
                      'role': 'user',
                      'blocks': [
                        {
                          'kind': 'text',
                          'availability': 'recorded',
                          'text': 'delegate',
                          'originalSize': 8,
                          'agent': {
                            'agentName': 'root',
                            'author': 'root',
                            'recipient': 'reviewer',
                          },
                        },
                      ],
                    },
                  ],
                  'tools': <Object>[],
                  'protocolEvidence': [
                    {'name': 'codex.session_id', 'value': 'session-root-1'},
                    {'name': 'codex.thread_id', 'value': 'thread-subagent-1'},
                  ],
                },
              },
            }),
          );
        } else {
          request.response.statusCode = HttpStatus.notFound;
        }
        await request.response.close();
      });

      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: List.filled(43, 'R').join(),
          writeToken: List.filled(43, 'W').join(),
          instanceId: 'instance-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);

      final detail = await api.exchange('_exchange-agent');
      final projection = detail.content.agentConversation;
      expect(projection?.scope, 'capture_run');
      expect(projection?.agents.map((agent) => agent.name), [
        'root',
        'reviewer',
      ]);
      expect(projection?.relationships.single.source, 'root');
      expect(projection?.relationships.single.target, 'reviewer');
      expect(projection?.actions.single.name, 'spawn_agent');
      expect(projection?.actions.single.attributed, isTrue);
      expect(
        detail.content.request?.messages.single.blocks.single.agent?.recipient,
        'reviewer',
      );
      expect(
        detail.content.request?.protocolEvidence.last.value,
        'thread-subagent-1',
      );
      expect(requests.last.path, '/api/v1/exchanges/_exchange-agent');
      expect(requests.last.queryParameters, {'contentView': 'incremental'});
    },
  );

  test('HTTP API separates Raw read and audited reveal capabilities', () async {
    const exchangeId = 'exchange raw';
    const envelopeId = '_raw-writer.1';
    final body = <int>[0x00, 0xff, 0x0a, 0x41];
    final digest = crypto.sha256.convert(body).toString();
    final requests = <({String method, Uri uri, String? authorization})>[];
    var revealBodyBytes = -1;
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add((
        method: request.method,
        uri: request.uri,
        authorization: request.headers.value(HttpHeaders.authorizationHeader),
      ));
      if (request.method == 'POST') {
        revealBodyBytes = (await request.fold<List<int>>(
          <int>[],
          (bytes, chunk) => bytes..addAll(chunk),
        )).length;
      } else {
        await request.drain<void>();
      }
      request.response.headers.contentType = ContentType.json;
      if (request.uri.path == '/api/v1/auth/sessions/current') {
        request.response.write(
          jsonEncode({
            'schema': 'vibermate-app-session-state-v1',
            'revision': 1,
            'expiresAt': DateTime.now()
                .toUtc()
                .add(const Duration(hours: 1))
                .toIso8601String(),
          }),
        );
      } else if (request.uri.pathSegments.length == 5 &&
          request.uri.pathSegments[0] == 'api' &&
          request.uri.pathSegments[1] == 'v1' &&
          request.uri.pathSegments[2] == 'exchanges' &&
          request.uri.pathSegments[3] == exchangeId &&
          request.uri.pathSegments[4] == 'raw-evidence') {
        request.response.headers.set(
          HttpHeaders.cacheControlHeader,
          'no-store',
        );
        request.response.write(
          jsonEncode({
            'items': [
              _rawEnvelopeJson(
                envelopeId: envelopeId,
                exchangeId: exchangeId,
                bodyBytes: body.length,
                bodySha256: digest,
              ),
            ],
            'recovery': {
              'recoveredUncleanWriters': 0,
              'purgedExpiredEnvelopes': 0,
              'maximumPossibleLossMs': 0,
            },
            'writer': {
              'state': 'active',
              'admittedRecords': 1,
              'durableWatermark': 1,
              'queueRecords': 0,
              'queueBytes': 0,
              'maximumUnflushedTimeMs': 250,
            },
          }),
        );
      } else if (request.uri.path ==
          '/api/v1/raw-evidence/$envelopeId/actions/reveal') {
        request.response.headers.set(
          HttpHeaders.cacheControlHeader,
          'no-store',
        );
        request.response.write(
          jsonEncode({
            'envelope': _rawEnvelopeJson(
              envelopeId: envelopeId,
              exchangeId: exchangeId,
              bodyBytes: body.length,
              bodySha256: digest,
            ),
            'headers': [
              {
                'name': 'Authorization',
                'values': ['Bearer local-client-secret'],
              },
              {
                'name': 'Content-Type',
                'values': ['application/octet-stream'],
              },
            ],
            'trailers': <Object?>[],
            'bodyBase64': base64.encode(body),
            'frames': [
              {'kind': 'data', 'offset': 0, 'length': body.length},
            ],
          }),
        );
      } else {
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });

    final readToken = List.filled(43, 'R').join();
    final writeToken = List.filled(43, 'W').join();
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: readToken,
        writeToken: writeToken,
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);

    final page = await api.rawEvidence(exchangeId);
    expect(page.items.single.envelopeId, envelopeId);
    expect(page.items.single.redactedCredentialFields, ['Authorization']);
    expect(page.writer.state, 'active');
    expect(requests.last.method, 'GET');
    expect(requests.last.authorization, 'Bearer $readToken');

    final revealed = await api.revealRawEvidence(envelopeId: envelopeId);
    expect(revealed.body, orderedEquals(body));
    expect(revealed.headers.first.name, 'Authorization');
    expect(revealed.frames.single.length, body.length);
    expect(requests.last.method, 'POST');
    expect(requests.last.authorization, 'Bearer $writeToken');
    expect(revealBodyBytes, 0);

    final requestCount = requests.length;
    await expectLater(
      api.revealRawEvidence(envelopeId: 'invalid\nidentity'),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests, hasLength(requestCount));
  });
}

Map<String, Object?> _rawEnvelopeJson({
  required String envelopeId,
  required String exchangeId,
  required int bodyBytes,
  required String bodySha256,
}) => {
  'envelopeId': envelopeId,
  'layer': 'provider_egress',
  'scopeKind': 'managed_run',
  'scopeId': 'run-raw-test',
  'exchangeId': exchangeId,
  'attemptId': 'attempt-raw-test',
  'observedAt': '2026-08-12T04:00:00.000Z',
  'expiresAt': '2026-09-11T04:00:00.000Z',
  'method': 'POST',
  'scheme': 'https',
  'authority': 'api.anthropic.com',
  'path': '/v1/messages',
  'contentType': 'application/octet-stream',
  'representation': 'http_message',
  'canonicalization': 'go_net_http_v1',
  'headerCount': 2,
  'trailerCount': 0,
  'bodyBytes': bodyBytes,
  'bodySha256': bodySha256,
  'digestScope': 'full_body',
  'payloadState': 'captured',
  'redactedCredentialFields': ['Authorization'],
  'revealAvailable': true,
};

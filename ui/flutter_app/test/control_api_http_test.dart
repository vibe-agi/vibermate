import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart' as crypto;

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
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

  test('HTTP API creates a private HTTP upstream Endpoint', () async {
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
            'realmId': 'anthropic.official',
            'backendProtocols': ['anthropic_messages'],
            'capabilities': ['messages', 'streaming', 'tool_calls'],
            'accountKinds': ['anthropic_api_key', 'claude_oauth_token'],
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
      kind: 'anthropic',
    );
    expect(endpoint.origin.toString(), 'http://spark-2a59:8888');
    expect(createBodies.single['origin'], 'http://spark-2a59:8888');
    expect(requests.where((request) => request.method == 'POST'), hasLength(1));

    final requestCount = requests.length;
    await expectLater(
      api.createUpstreamEndpoint(
        id: 'target.custom.anthropic.smart-dash',
        displayName: 'Invalid dash',
        origin: 'http://spark–2a59:8888',
        kind: 'anthropic',
      ),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests, hasLength(requestCount));
  });

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

import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

import 'runtime_usage_fixture.dart';

void main() {
  test(
    'Capture assignment separates launch and applied Environment revisions',
    () {
      final assignment = CaptureAssignment.fromJson({
        'captureKey': 'managed_run:run-one',
        'captureId': 'run-one',
        'captureKind': 'managed_run',
        'environmentId': 'work',
        'environmentRevision': 4,
        'environmentDigest': List.filled(64, 'b').join(),
        'launchEnvironmentRevision': 2,
        'launchEnvironmentDigest': List.filled(64, 'a').join(),
        'revision': 3,
        'source': 'launch',
        'updatedAt': '2026-08-28T01:02:03.000Z',
      }, 'assignment');

      expect(assignment.environmentRevision, 4);
      expect(assignment.launchEnvironmentRevision, 2);
      expect(assignment.revision, 3);
      expect(assignment.environmentDigest, List.filled(64, 'b').join());
    },
  );

  test(
    'Runtime Server access states one reusable Runtime User login model',
    () {
      final access = RuntimeServerAccess.fromJson({
        'schema': 'vibermate-server-access-v2',
        'transport': 'http',
        'authentication': 'runtime_user_password',
        'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
        'targets': ['192.168.1.44:9666', '[fd00::8]:9666'],
      }, 'serverAccess');

      expect(access.transport, 'http');
      expect(access.encrypted, isFalse);
      expect(access.requiresRuntimeUserLogin, isTrue);
      expect(access.preferredTarget, '192.168.1.44:9666');

      for (final invalid in <Map<String, Object?>>[
        {
          'schema': 'vibermate-server-access-v2',
          'transport': 'ftp',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['192.168.1.44:9666'],
        },
        {
          'schema': 'vibermate-server-access-v2',
          'transport': 'https',
          'authentication': 'anonymous',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['192.168.1.44:9666'],
        },
        {
          'schema': 'vibermate-server-access-v2',
          'transport': 'https',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'per_run_approval',
          'targets': ['192.168.1.44:9666'],
        },
        {
          'schema': 'vibermate-server-access-v2',
          'transport': 'https',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['server.local:9666'],
        },
      ]) {
        expect(
          () => RuntimeServerAccess.fromJson(invalid, 'serverAccess'),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );

  test(
    'Runtime User projection excludes password material and validates state',
    () {
      final user = RuntimeUser.fromJson({
        'id': 'user.test',
        'username': 'alice',
        'state': 'active',
        'createdAt': '2026-08-24T12:00:00.000Z',
        'updatedAt': '2026-08-24T12:00:00.000Z',
      }, 'runtimeUser');
      expect(user.username, 'alice');
      expect(user.active, isTrue);
      expect(
        () => RuntimeUser.fromJson({
          'id': 'user.test',
          'username': 'alice',
          'state': 'active',
          'createdAt': '2026-08-24T12:00:00.000Z',
          'updatedAt': '2026-08-24T12:00:00.000Z',
          'password': 'must-not-exist',
        }, 'runtimeUser'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test(
    'Runtime usage keeps exact A to B models and partial token knowledge',
    () {
      final report = RuntimeUsageReport.fromJson(
        runtimeUsagePayload(),
        'runtimeUsage',
      );

      final alice = report.users.single;
      expect(report.truncated, isFalse);
      expect(report.period.from, '2026-07-27');
      expect(report.period.until, '2026-08-26');
      expect(report.period.timeZone, 'Asia/Singapore');
      expect(report.days.single.date, '2026-08-24');
      expect(alice.days.single.failed, 1);
      expect(alice.latestContext?.workspaceLabel, 'vibermate');
      expect(alice.models.single.requestedModel, 'gpt-5.6-sol');
      expect(alice.models.single.upstreamModel, 'relay:model/custom');
      expect(alice.tokens.inputUncached.tokens, 42);
      expect(alice.tokens.inputUncached.knownCalls, 1);
      expect(alice.tokens.inputUncached.unknownCalls, 1);
      expect(alice.agentSessions.single.sessionId, 'native-session-one');
    },
  );

  const machineId = 'BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc';
  const workspaceId = 'CAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAg';

  test('Control problem retains the canonical Go reason code', () {
    final problem = ControlProblem.fromJson({
      'type': 'urn:vibermate:error:revision-conflict',
      'title': 'Conflict',
      'status': 409,
      'code': 'revision_conflict',
    }, status: 409);

    expect(problem.reasonCode, 'revision_conflict');
    expect(problem.messageKey, 'error.revision_conflict');
    expect(
      () => ControlProblem.fromJson({
        'reasonCode': 'revision_conflict',
        'messageKey': 'error.revision_conflict',
      }, status: 409),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Control problem retains an actionable safe detail', () {
    final problem = ControlProblem.fromJson({
      'type': 'urn:vibermate:error:account-selector-test-failed',
      'title': 'Unprocessable Entity',
      'status': 422,
      'code': 'account_selector_test_failed',
      'detail': 'invalid Account Selector policy: compile JavaScript',
    }, status: 422);

    expect(problem.detail, contains('compile JavaScript'));
    expect(problem.toString(), contains('compile JavaScript'));
  });

  test('upstream model catalog trusts only the selected Endpoint', () {
    final catalog = UpstreamModelCatalog.fromJson({
      'endpointId': 'target.spark.local',
      'endpointRevision': 3,
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
    }, 'upstreamModels');

    expect(catalog.endpointId, 'target.spark.local');
    expect(catalog.accountId, 'account.spark.models');
    expect(catalog.credentialEpoch, 7);
    expect(catalog.verifiedFromEndpoint, isTrue);
    expect(catalog.models.single.id, 'dashscope:deepseek-v4-flash-0731');

    expect(
      () => UpstreamModelCatalog.fromJson({
        'endpointId': 'target.spark.local',
        'endpointRevision': 3,
        'accountId': 'account.spark.models',
        'accountRevision': 4,
        'credentialEpoch': 7,
        'observedAt': '2026-08-20T03:04:05.000Z',
        'availabilitySource': 'directory',
        'models': [
          {
            'id': 'deepseek-v4-flash',
            'displayName': '',
            'ownedBy': '',
            'verifiedAvailable': true,
            'contextLimit': 0,
            'outputLimit': 0,
          },
        ],
      }, 'upstreamModels'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('models.dev describes request-side model IDs only', () {
    final catalog = ClientModelCatalog.fromJson({
      'protocol': 'anthropic_messages',
      'providerId': 'anthropic',
      'metadataSource': 'models.dev',
      'models': [
        {
          'id': 'claude-opus-4-1',
          'canonicalId': 'anthropic/claude-opus-4-1',
          'displayName': 'Claude Opus 4.1',
          'description': 'Request-side metadata',
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
          'knowledgeCutoff': '2025-03',
          'releaseDate': '2025-08-05',
        },
      ],
    }, 'clientModels');

    expect(catalog.protocol, 'anthropic_messages');
    expect(catalog.models.single.id, 'claude-opus-4-1');
    expect(catalog.models.single.canonicalId, 'anthropic/claude-opus-4-1');
  });

  test('opaque upstream IDs fit the Environment mapping contract', () {
    final model = {
      'id': 'm' * 257,
      'displayName': '',
      'ownedBy': '',
      'verifiedAvailable': true,
      'contextLimit': 0,
      'outputLimit': 0,
    };

    expect(
      () => UpstreamModel.fromJson(model, 'upstreamModel'),
      throwsA(isA<ControlContractException>()),
    );

    final policy = EnvironmentModelPolicy.fromJson({
      'revision': 2,
      'mode': 'map',
      'mappings': [
        {
          'requestedModel': 'claude-opus-4-1',
          'upstreamModel': 'dashscope:deepseek-v4-flash-0731',
        },
      ],
    }, 'modelPolicy');
    expect(
      policy.mappings.single.upstreamModel,
      'dashscope:deepseek-v4-flash-0731',
    );
    expect(policy.toJson()['mappings'], isA<List<Object?>>());
  });

  test('opaque model IDs preserve printable edge whitespace', () {
    final upstream = UpstreamModel.fromJson({
      'id': ' relay custom:model ',
      'displayName': '',
      'ownedBy': '',
      'verifiedAvailable': true,
      'contextLimit': 0,
      'outputLimit': 0,
    }, 'upstreamModel');
    final mapping = EnvironmentModelMapping.fromJson({
      'requestedModel': ' client model ',
      'upstreamModel': ' relay custom:model ',
    }, 'mapping');

    expect(upstream.id, ' relay custom:model ');
    expect(mapping.toJson(), {
      'requestedModel': ' client model ',
      'upstreamModel': ' relay custom:model ',
    });
  });

  test(
    'managed run retains complete workspace identity and real active states',
    () {
      final record = CaptureRecord.fromJson({
        'key': 'managed_run:run-1',
        'id': 'run-1',
        'kind': 'managed_run',
        'displayName': 'Claude Code',
        'state': 'attached',
        'observation': 'observed',
        'createdAt': '2026-08-10T09:00:00.000Z',
        'updatedAt': '2026-08-10T09:01:00.000Z',
        'managedRun': {
          'executableLabel': 'claude',
          'cwd': '/Users/mira/Code/vibermate',
          'canonicalExecutablePath': '/usr/local/bin/claude',
          'localUserLabel': 'mira',
          'runtimeUserId': 'user.remote',
          'runtimeUsername': 'alice',
          'loginSessionId': 'login.remote',
          'deviceName': 'MacBook Pro',
          'machineId': machineId,
          'machineRegistrationRevision': 1,
          'workspaceId': workspaceId,
          'workspaceLabel': 'vibermate',
          'workspaceEvidence': 'registered_companion',
          'workspaceDerivationRevision': 1,
          'processId': 7300,
          'recognition': 'verified',
          'clientAdapter': {
            'id': 'claude-code',
            'revision': 1,
            'version': '2.1.220',
            'catalogRevision': 1,
            'source': 'prelaunch_digest_catalog',
            'installShape': 'native_single_binary',
            'launchRecipe': 'node_env_proxy',
          },
          'expiresAt': '2026-08-10T10:00:00.000Z',
          'firstObservedAt': '2026-08-10T09:00:30.000Z',
        },
      }, 'capture');

      expect(record.running, isTrue);
      expect(record.managedRun!.machineId, machineId);
      expect(record.managedRun!.workspaceId, workspaceId);
      expect(record.managedRun!.workspaceEvidence, 'registered_companion');
      expect(record.managedRun!.runtimeUserId, 'user.remote');
      expect(record.managedRun!.runtimeUsername, 'alice');
      expect(record.managedRun!.deviceName, 'MacBook Pro');
      expect(record.managedRun!.clientAdapter?.version, '2.1.220');
      expect(record.managedRun!.clientAdapter?.id, 'claude-code');
      expect(
        record.managedRun!.canonicalExecutablePath,
        '/usr/local/bin/claude',
      );

      expect(
        () => ManagedRunSummary.fromJson({
          'executableLabel': 'claude',
          'cwd': '/Users/mira/Code/vibermate',
          'canonicalExecutablePath': '/usr/local/bin/claude',
          'machineId': machineId,
          'recognition': 'verified',
          'expiresAt': '2026-08-10T10:00:00.000Z',
        }, 'managedRun'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Provider Account response rejects any returned secret field', () {
    expect(
      () => ProviderAccount.fromJson({
        'id': 'account.test',
        'displayName': 'Test',
        'upstreamEndpointId': 'target.test',
        'kind': 'anthropic_api_key',
        'realmId': 'anthropic.test',
        'state': 'active',
        'revision': 1,
        'credentialState': 'ready',
        'credentialEpoch': 1,
        'setHeaderNames': ['X-Team'],
        'deleteHeaderNames': ['X-Legacy'],
        'secret': 'must-not-cross-response-boundary',
      }, 'providerAccount'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('credential-missing Account has an explicit zero epoch', () {
    final account = ProviderAccount.fromJson({
      'id': 'account.missing',
      'displayName': 'Missing',
      'upstreamEndpointId': 'target.test',
      'kind': 'bearer_token',
      'realmId': 'openai.test',
      'state': 'active',
      'revision': 1,
      'credentialState': 'credential_missing',
      'credentialEpoch': 0,
      'setHeaderNames': ['X-Team'],
      'deleteHeaderNames': ['X-Legacy'],
    }, 'providerAccount');

    expect(account.credentialEpoch, 0);
    expect(account.usable, isFalse);
    expect(account.setHeaderNames, ['X-Team']);
    expect(account.deleteHeaderNames, ['X-Legacy']);
  });

  test(
    'Provider Account Header summary is canonical and never returns values',
    () {
      final account = ProviderAccount.fromJson({
        'id': 'account.headers',
        'displayName': 'Headers',
        'upstreamEndpointId': 'target.test',
        'kind': 'bearer_token',
        'realmId': 'relay.test',
        'state': 'active',
        'revision': 2,
        'credentialState': 'ready',
        'credentialEpoch': 3,
        'setHeaderNames': ['X-Organization', 'X-Team'],
        'deleteHeaderNames': ['X-Legacy'],
      }, 'providerAccount');

      expect(account.setHeaderNames, ['X-Organization', 'X-Team']);
      expect(account.deleteHeaderNames, ['X-Legacy']);
      expect(
        () => ProviderAccount.fromJson({
          'id': 'account.headers',
          'displayName': 'Headers',
          'upstreamEndpointId': 'target.test',
          'kind': 'bearer_token',
          'realmId': 'relay.test',
          'state': 'active',
          'revision': 2,
          'credentialState': 'ready',
          'credentialEpoch': 3,
          'setHeaderNames': ['X-Team'],
          'deleteHeaderNames': <String>[],
          'setHeaders': {'X-Team': 'must-not-cross-response-boundary'},
        }, 'providerAccount'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test(
    'Provider Account Header input enforces the HTTP field-value grammar',
    () {
      expect(
        () => const ProviderAccountHeaderPolicy(
          setHeaders: {'X-Team': 'bad\u0001value'},
        ).validate(accountKind: 'bearer_token'),
        throwsA(isA<ControlContractException>()),
      );
      expect(
        () => const ProviderAccountHeaderPolicy(
          setHeaders: {'X-Team': 'tab\tvalue'},
        ).validate(accountKind: 'bearer_token'),
        returnsNormally,
      );
    },
  );

  test(
    'Agent client identity keeps common and native protocol identifiers',
    () {
      final identity = AgentClientIdentity.fromJson(
        _codexClientIdentityJson(),
        'clientIdentity',
      );

      expect(identity.client, 'codex');
      expect(identity.sessionId, 'session-root-1');
      expect(identity.actorId, 'thread-subagent-1');
      expect(identity.actorLabel, 'reviewer');
      expect(identity.actorIsSubagent, isTrue);
      expect(
        identity.protocolIds.map((value) => value.name),
        containsAll(<String>[
          'codex.response_item_id',
          'codex.session_id',
          'codex.thread_id',
          'codex.turn_id',
        ]),
      );
      expect(identity.searchableValues, contains('turn-7'));
      expect(identity.searchableValues, contains('reviewer'));
    },
  );

  test('Agent client identity rejects non-canonical or ambiguous evidence', () {
    final unordered = _codexClientIdentityJson();
    unordered['protocolIds'] = <Object?>[
      {'name': 'codex.turn_id', 'value': 'turn-7'},
      {'name': 'codex.session_id', 'value': 'session-root-1'},
    ];
    expect(
      () => AgentClientIdentity.fromJson(unordered, 'clientIdentity'),
      throwsA(isA<ControlContractException>()),
    );

    final actorless = _codexClientIdentityJson();
    actorless.remove('actorId');
    expect(
      () => AgentClientIdentity.fromJson(actorless, 'clientIdentity'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('network Agent identity may precede the provider response', () {
    final identity = AgentClientIdentity.fromJson({
      'client': 'claude',
      'sessionId': '64fe284e-4565-4065-961d-3db7351ff152',
      'sessionResumable': true,
      'actorId': 'a5ef98e49c0e228c9',
      'actorIsSubagent': true,
      'source': 'client_protocol_evidence',
      'confidence': 'exact',
      'observedAt': '2026-08-14T11:31:05.000Z',
      'protocolIds': <Object?>[
        {'name': 'claude.agent_id', 'value': 'a5ef98e49c0e228c9'},
        {'name': 'claude.parent_agent_id', 'value': 'aaac343a3a31d4ccf'},
        {
          'name': 'claude.session_id',
          'value': '64fe284e-4565-4065-961d-3db7351ff152',
        },
      ],
    }, 'clientIdentity');

    expect(identity.providerResponseId, isNull);
    expect(identity.actorIsSubagent, isTrue);
    expect(identity.searchableValues, contains('aaac343a3a31d4ccf'));
  });

  // The dialect's top-level instruction parameter is a per-request field, not a
  // turn in the conversation. It has to arrive as its own field so the timeline
  // can present it as configuration and the transcript can stay append-only.
  test('Exchange request carries the system parameter as its own field', () {
    final request = ExchangeRequest.fromJson({
      'requestedModel': 'claude-opus-5',
      'effectiveModel': 'claude-opus-5',
      'maxOutputTokens': 64000,
      'stream': true,
      'system': [
        {
          'kind': 'text',
          'availability': 'recorded',
          'text': 'You are an interactive agent.',
          'originalSize': 29,
        },
      ],
      'messages': [
        {
          'role': 'user',
          'blocks': [
            {
              'kind': 'text',
              'availability': 'recorded',
              'text': 'inspect this',
              'originalSize': 12,
            },
          ],
        },
      ],
      'tools': <Object?>[],
      'protocolEvidence': <Object?>[],
    }, 'exchange.content.request');

    expect(request.system.single.text, 'You are an interactive agent.');
    expect(request.messages.single.role, 'user');
  });

  test('Exchange request accepts a dialect with no system parameter', () {
    final request = ExchangeRequest.fromJson({
      'requestedModel': 'gpt-5.6-sol',
      'effectiveModel': 'gpt-5.6-sol',
      'maxOutputTokens': 64000,
      'stream': false,
      'system': <Object?>[],
      'messages': [
        {
          'role': 'system',
          'blocks': [
            {
              'kind': 'text',
              'availability': 'recorded',
              'text': 'inline instruction',
              'originalSize': 18,
            },
          ],
        },
      ],
      'tools': <Object?>[],
      'protocolEvidence': <Object?>[],
    }, 'exchange.content.request');

    // OpenAI Chat Completions has no top-level parameter; its instruction is
    // genuinely a message and must stay in place.
    expect(request.system, isEmpty);
    expect(request.messages.single.role, 'system');
  });

  test('Exchange content retains native request and response identifiers', () {
    final request = ExchangeRequest.fromJson({
      'requestedModel': 'claude-opus-5',
      'effectiveModel': 'claude-opus-5',
      'maxOutputTokens': 64000,
      'stream': true,
      'system': <Object?>[],
      'messages': [
        {
          'role': 'user',
          'blocks': [
            {
              'kind': 'text',
              'availability': 'recorded',
              'text': 'inspect this',
              'originalSize': 12,
            },
          ],
        },
      ],
      'tools': <Object?>[],
      'protocolEvidence': [
        {'name': 'claude.agent_id', 'value': 'agent-reviewer'},
        {'name': 'claude.session_id', 'value': 'session-review'},
      ],
    }, 'exchange.content.request');
    final response = ExchangeResponse.fromJson({
      'id': 'msg-response',
      'requestedModel': 'claude-opus-5',
      'effectiveModel': 'claude-opus-5',
      'reportedModel': 'claude-opus-5',
      'stopReason': 'end_turn',
      'blocks': [
        {
          'kind': 'text',
          'availability': 'recorded',
          'text': 'done',
          'originalSize': 4,
        },
      ],
      'usage': {
        'inputUncached': {'known': false},
        'cacheWrite': {'known': false},
        'cacheRead': {'known': false},
        'output': {'known': true, 'tokens': 1, 'source': 'anthropic'},
        'reasoning': {'known': false},
      },
      'protocolEvidence': [
        {'name': 'anthropic.response_id', 'value': 'msg-response'},
      ],
    }, 'exchange.content.response');

    expect(request.protocolEvidence.map((value) => value.name), [
      'claude.agent_id',
      'claude.session_id',
    ]);
    expect(request.protocolEvidence.last.value, 'session-review');
    expect(response.protocolEvidence.single.value, 'msg-response');
    expect(
      () => ExchangeRequest.fromJson({
        'requestedModel': 'claude-opus-5',
        'effectiveModel': 'claude-opus-5',
        'maxOutputTokens': 64000,
        'stream': true,
        'messages': <Object?>[],
        'tools': <Object?>[],
      }, 'exchange.content.request'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => ExchangeRequest.fromJson({
        'requestedModel': 'claude-opus-5',
        'effectiveModel': 'claude-opus-5',
        'maxOutputTokens': 64000,
        'stream': true,
        'messages': <Object?>[],
        'tools': <Object?>[],
        'protocolEvidence': null,
      }, 'exchange.content.request'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => ExchangeRequest.fromJson({
        'requestedModel': 'claude-opus-5',
        'effectiveModel': 'claude-opus-5',
        'maxOutputTokens': 64000,
        'stream': true,
        'messages': <Object?>[],
        'tools': <Object?>[],
        'protocolEvidence': [
          {'name': 'claude.session_id', 'value': 'session\uFEFFreview'},
        ],
      }, 'exchange.content.request'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Offline hold accepts only internally consistent safety evidence', () {
    final json = <String, Object?>{
      'state': 'held',
      'revision': 9,
      'since': '2026-08-11T00:00:00.000Z',
      'activeActions': 2,
      'enteringActions': 0,
      'activeEgress': 0,
      'queuedRequests': 2,
      'heldBytes': 4096,
      'safeToDisconnect': true,
      'activeByKind': <String, Object?>{},
      'queuedByKind': <String, Object?>{'provider': 1, 'plugin': 1},
    };
    final snapshot = OfflineHoldSnapshot.fromJson(json);

    expect(snapshot.state, 'held');
    expect(snapshot.safeToDisconnect, isTrue);
    expect(snapshot.activeActions, 2);
    expect(snapshot.queuedByKind, {'provider': 1, 'plugin': 1});

    final unsafe = jsonDecode(jsonEncode(json)) as Map<String, dynamic>;
    unsafe['safeToDisconnect'] = false;
    expect(
      () => OfflineHoldSnapshot.fromJson(unsafe),
      throwsA(isA<ControlContractException>()),
    );

    final wrongTotal = jsonDecode(jsonEncode(json)) as Map<String, dynamic>;
    wrongTotal['activeByKind'] = {'provider': 1};
    expect(
      () => OfflineHoldSnapshot.fromJson(wrongTotal),
      throwsA(isA<ControlContractException>()),
    );

    final unknownKind = jsonDecode(jsonEncode(json)) as Map<String, dynamic>;
    unknownKind['queuedByKind'] = {'future_kind': 2};
    expect(
      () => OfflineHoldSnapshot.fromJson(unknownKind),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Runtime status retains the Offline hold CAS authority', () {
    final status = RuntimeStatus.fromJson({
      'generation': 'instance-test',
      'ready': true,
      'apiVersion': 'v1',
      'statusKey': 'runtime.state.initialized',
      'runtime': {
        'state': 'initialized',
        'instanceId': 'instance-test',
        'host': 'desktop',
        'schemaRevision': 1,
        'storage': 'healthy',
        'environmentProjection': {
          'state': 'healthy',
          'unavailableEnvironments': null,
        },
        'offlineHold': {
          'state': 'online',
          'revision': 4,
          'since': '2026-08-11T00:00:00.000Z',
          'activeActions': 1,
          'enteringActions': 1,
          'activeEgress': 1,
          'queuedRequests': 0,
          'heldBytes': 0,
          'safeToDisconnect': false,
          'activeByKind': {'provider': 1},
          'queuedByKind': <String, Object?>{},
        },
        'startedAt': '2026-08-10T00:00:00.000Z',
      },
    }, expectedInstanceId: 'instance-test');

    expect(status.offlineHold.revision, 4);
    expect(status.offlineHold.activeByKind, {'provider': 1});
    expect(status.schemaRevision, 1);
  });

  test('Runtime status accepts the public Runtime Server host kind', () {
    final status = RuntimeStatus.fromJson({
      'generation': 'instance-server',
      'ready': true,
      'apiVersion': 'v1',
      'statusKey': 'runtime.state.initialized',
      'runtime': {
        'state': 'initialized',
        'instanceId': 'instance-server',
        'host': 'server',
        'schemaRevision': 6,
        'storage': 'healthy',
        'environmentProjection': {
          'state': 'healthy',
          'unavailableEnvironments': null,
        },
        'offlineHold': {
          'state': 'online',
          'revision': 1,
          'since': '2026-08-24T00:00:00.000Z',
          'activeActions': 0,
          'enteringActions': 0,
          'activeEgress': 0,
          'queuedRequests': 0,
          'heldBytes': 0,
          'safeToDisconnect': false,
          'activeByKind': <String, Object?>{},
          'queuedByKind': <String, Object?>{},
        },
        'startedAt': '2026-08-24T00:00:00.000Z',
      },
    }, expectedInstanceId: 'instance-server');

    expect(status.host, 'server');
  });

  test(
    'Manual Capture context accepts only literal loopback proxy authority',
    () {
      final context = ManualCaptureContext.fromJson(
        {
          'confirmationToken': 'ctx_${List.filled(43, 'A').join()}',
          'proxyAddress': 'http://127.0.0.1:43123',
          'environmentId': 'work',
          'environmentRevision': 7,
          'environmentDigest': List.filled(64, 'a').join(),
          'launchAuthorityDigest': List.filled(64, 'b').join(),
          'protectedAuthorities': ['api.anthropic.com'],
          'managedCredentialAuthorities': ['api.anthropic.com'],
          'defaultTemporarySeconds': 3600,
          'maxTemporarySeconds': 86400,
          'root': {
            'kind': 'local_path',
            'derSha256': List.filled(64, 'c').join(),
            'fingerprint': 'CC:CC',
            'pemPath': '/tmp/vibermate-root.pem',
          },
        },
        'manualCaptureContext',
        expectedEnvironmentId: 'work',
      );

      expect(context.environmentRevision, 7);
      expect(context.root?.pemPath, '/tmp/vibermate-root.pem');

      expect(
        () => ManualCaptureContext.fromJson(
          {
            'confirmationToken': 'ctx_${List.filled(43, 'A').join()}',
            'proxyAddress': 'http://localhost:43123',
            'environmentId': 'work',
            'environmentRevision': 7,
            'environmentDigest': List.filled(64, 'a').join(),
            'launchAuthorityDigest': List.filled(64, 'b').join(),
            'protectedAuthorities': <String>[],
            'managedCredentialAuthorities': <String>[],
            'defaultTemporarySeconds': 3600,
            'maxTemporarySeconds': 86400,
          },
          'manualCaptureContext',
          expectedEnvironmentId: 'work',
        ),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Manual Capture read projection cannot smuggle a credential', () {
    expect(
      () => ManualCaptureRecord.fromJson({
        'id': 'manual-test',
        'displayName': 'Test app',
        'clientClass': 'desktop_app',
        'lifetime': 'until_revoked',
        'state': 'active',
        'observation': 'waiting_for_traffic',
        'createdAt': '2026-08-11T00:00:00.000Z',
        'updatedAt': '2026-08-11T00:00:00.000Z',
        'proxyPassword': 'must-only-appear-in-the-one-time-grant',
      }, 'manualCapture'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Activity requires complete frozen Account and parent evidence', () {
    final json = <String, Object?>{
      'id': 'exchange-test',
      'occurredAt': '2026-08-11T00:00:00.000Z',
      'kind': 'exchange',
      'title': 'Claude request',
      'status': 'succeeded',
      'requestPreview': {
        'kind': 'tool_call',
        'text': 'workspace.read',
        'truncated': false,
      },
      'source': {
        'kind': 'capture_run',
        'displayName': 'Claude Code',
        'recognition': 'verified',
      },
      'conversation': {
        'id': 'capture_run:run-test:main',
        'displayName': 'Claude Code',
        'kind': 'main',
        'evidence': 'capture_run',
      },
      'environment': {
        'id': 'work',
        'revision': 7,
        'digest': List.filled(64, 'a').join(),
        'clientEndpointId': 'claude-client',
        'clientEndpointRevision': 2,
        'protocolPlanId': 'anthropic-messages',
        'protocolPlanRevision': 3,
        'routeId': 'anthropic-direct',
        'routeRevision': 4,
        'accountId': 'anthropic-work',
        'accountRevision': 5,
        'credentialEpoch': 6,
      },
      'parentRefs': {
        'captureRunId': 'run-test',
        'connectionId': 'connection-test',
        'exchangeId': 'exchange-test',
      },
    };
    final activity = ActivityRecord.fromJson(json, 'activity');

    expect(activity.environment.accountRevision, 5);
    expect(activity.captureRunId, 'run-test');
    expect(activity.requestPreview?.kind, 'tool_call');
    expect(activity.requestPreview?.text, 'workspace.read');

    final invalidPreview = jsonDecode(jsonEncode(json)) as Map<String, dynamic>;
    invalidPreview['requestPreview'] = {
      'kind': 'reasoning',
      'text': 'private chain of thought',
      'truncated': false,
    };
    expect(
      () => ActivityRecord.fromJson(invalidPreview, 'activity'),
      throwsA(isA<ControlContractException>()),
    );

    expect(
      () => ActivityRecord.fromJson({
        'id': 'exchange-test',
        'occurredAt': '2026-08-11T00:00:00.000Z',
        'kind': 'exchange',
        'title': 'Claude request',
        'status': 'succeeded',
        'source': {
          'kind': 'capture_run',
          'displayName': 'Claude Code',
          'recognition': 'verified',
        },
        'environment': {
          'id': 'work',
          'revision': 7,
          'digest': List.filled(64, 'a').join(),
          'clientEndpointId': 'claude-client',
          'clientEndpointRevision': 2,
          'protocolPlanId': 'anthropic-messages',
          'protocolPlanRevision': 3,
          'routeId': 'anthropic-direct',
          'routeRevision': 4,
          'accountId': 'anthropic-work',
          'credentialEpoch': 6,
        },
        'parentRefs': {
          'captureRunId': 'run-test',
          'exchangeId': 'exchange-test',
        },
      }, 'activity'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Original Destination Activity has no synthetic Route or Account', () {
    final activity = ActivityRecord.fromJson({
      'id': 'exchange-original',
      'occurredAt': '2026-08-24T10:17:19.000Z',
      'kind': 'exchange',
      'title': 'codex',
      'status': 'succeeded',
      'source': {
        'kind': 'capture_run',
        'displayName': 'codex',
        'recognition': 'verified',
      },
      'conversation': {
        'id': 'capture_run:run-original:main',
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
        'captureRunId': 'run-original',
        'connectionId': 'connection-original',
        'exchangeId': 'exchange-original',
      },
    }, 'activity');

    expect(activity.environment.routeId, isNull);
    expect(activity.environment.routeRevision, isNull);
    expect(activity.environment.accountId, isNull);
  });

  test('native Client Session conversation evidence crosses Captures', () {
    final conversation = ActivityConversationRef.fromJson({
      'id': 'client_session:codex:opaque_digest:thread:opaque_thread:main',
      'displayName': 'Codex',
      'kind': 'main',
      'evidence': 'explicit_session',
    }, 'conversation');

    expect(conversation.evidence, 'explicit_session');
    expect(conversation.id, startsWith('client_session:codex:'));
  });

  test('Approval retains the complete closed decision authority', () {
    final approval = ApprovalRecord.fromJson(_toolApprovalJson(), 'approval');

    expect(approval.kind, 'tool_intent');
    expect(approval.exchangeId, 'exchange-test');
    expect(approval.environmentId, 'work');
    expect(approval.environmentRevision, 7);
    expect(approval.environmentDigest, List.filled(64, 'a').join());
    expect(approval.routeId, 'anthropic-direct');
    expect(approval.routeRevision, 4);
    expect(approval.subjectRefs, ['tool-call-1']);
    expect(approval.resolvedAt, isNull);
  });

  test('Approval rejects partial, invented, or inconsistent evidence', () {
    final unknown = _networkApprovalJson()..['futureAuthority'] = true;
    expect(
      () => ApprovalRecord.fromJson(unknown, 'approval'),
      throwsA(isA<ControlContractException>()),
    );

    final partial = _networkApprovalJson()..['environmentId'] = 'work';
    expect(
      () => ApprovalRecord.fromJson(partial, 'approval'),
      throwsA(isA<ControlContractException>()),
    );

    final pendingWithDecision = _networkApprovalJson()
      ..['decision'] = 'allow-once'
      ..['decisionScope'] = 'request';
    expect(
      () => ApprovalRecord.fromJson(pendingWithDecision, 'approval'),
      throwsA(isA<ControlContractException>()),
    );

    final wrongChoices = _networkApprovalJson();
    (wrongChoices['choices']! as List<Object?>).removeLast();
    expect(
      () => ApprovalRecord.fromJson(wrongChoices, 'approval'),
      throwsA(isA<ControlContractException>()),
    );

    final resolvedWithoutReason = _networkApprovalJson()
      ..['state'] = 'denied'
      ..['resolvedAt'] = '2026-08-11T00:01:00.000Z'
      ..['decision'] = 'deny'
      ..['decisionScope'] = 'request';
    expect(
      () => ApprovalRecord.fromJson(resolvedWithoutReason, 'approval'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('unknown usage cannot claim token or source evidence', () {
    expect(
      () => ExchangeUsageValue.fromJson({'known': false, 'tokens': 0}, 'usage'),
      throwsA(isA<ControlContractException>()),
    );
    final knownZero = ExchangeUsageValue.fromJson({
      'known': true,
      'tokens': 0,
      'source': 'provider',
    }, 'usage');
    expect(knownZero.tokens, 0);
  });

  test(
    'Environment authority round-trips without dropping nested evidence',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );

      final encoded = work.toJson();
      expect(encoded, isNot(contains('egressPolicy')));
      final encodedEndpoint =
          (encoded['clientEndpoints']! as List<Object?>).first! as JsonObject;
      final encodedPlan =
          (encodedEndpoint['protocolPlans']! as List<Object?>).first!
              as JsonObject;
      expect(encodedPlan['egressProfile'], {
        'id': 'profile.direct',
        'revision': 1,
        'displayName': 'Direct · System DNS',
        'policy': {
          'proxy': {'kind': 'direct'},
          'resolver': {'kind': 'system', 'transport': 'direct'},
        },
        'publishedAt': '1970-01-01T00:00:00.000Z',
      });
      expect(encodedPlan['transforms'], isEmpty);
      final decoded = EnvironmentRecord.fromJson(
        jsonDecode(jsonEncode(encoded)),
        'environment',
      );
      expect(encodedPlan, isNot(contains('mode')));
      expect(encodedPlan, isNot(contains('upstreamPlan')));
      expect(encodedPlan['destination'], {
        'kind': 'upstream',
        'upstream': isA<JsonObject>(),
      });

      expect(decoded.id, work.id);
      expect(decoded.revision, work.revision);
      expect(decoded.launchEnvironment, const EnvironmentLaunchPolicy.empty());
      expect(decoded.clientEndpoints.length, work.clientEndpoints.length);
      expect(
        decoded.routes.map((route) => route.id),
        work.routes.map((route) => route.id),
      );
      expect(
        decoded.routes.first.accountPolicy.accounts,
        work.routes.first.accountPolicy.accounts,
      );
      expect(
        decoded.clientEndpoints.first.protocolPlans.first.egressProfile,
        EgressProfileRevision.direct,
      );
      expect(
        decoded.clientEndpoints.first.protocolPlans.first.transforms,
        isEmpty,
      );
    },
  );

  test('Environment launch overlay is exact, bounded, and round-trips', () {
    final policy = EnvironmentLaunchPolicy.fromJson({
      'setEnv': {'TEAM_CONTEXT': 'research', 'FEATURE_FLAG': '1'},
      'deleteEnv': ['OLD_CONTEXT'],
    }, 'launchEnvironment');

    expect(policy.setEnv['TEAM_CONTEXT'], 'research');
    expect(policy.deleteEnv, ['OLD_CONTEXT']);
    expect(
      EnvironmentLaunchPolicy.fromJson(
        jsonDecode(jsonEncode(policy.toJson())),
        'launchEnvironment',
      ),
      policy,
    );
    expect(
      () => EnvironmentLaunchPolicy.fromJson({
        'setEnv': {'OPENAI_API_KEY': 'forbidden'},
      }, 'launchEnvironment'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => EnvironmentLaunchPolicy.fromJson({
        'deleteEnv': ['VIBERMATE_INTERNAL'],
      }, 'launchEnvironment'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test(
    'traffic egress contract keeps resolver and proxy semantics explicit',
    () {
      final socks5DoH = TrafficEgressPolicy.fromJson({
        'proxy': {'kind': 'socks5', 'endpoint': '127.0.0.1:1080'},
        'resolver': {
          'kind': 'doh',
          'dohUrl': 'https://resolver.example/',
          'transport': 'proxy',
        },
      }, 'egress');
      expect(socks5DoH.toJson(), {
        'proxy': {'kind': 'socks5', 'endpoint': '127.0.0.1:1080'},
        'resolver': {
          'kind': 'doh',
          'dohUrl': 'https://resolver.example/',
          'transport': 'proxy',
        },
      });

      final ipDoH = TrafficResolverPolicy.fromJson({
        'kind': 'doh',
        'dohUrl': 'https://8.8.8.8/dns-query',
        'transport': 'direct',
      }, 'egress.resolver');
      expect(ipDoH.dohUrl, 'https://8.8.8.8/dns-query');

      for (final invalid in <JsonObject>[
        {
          'proxy': {'kind': 'socks5h', 'endpoint': 'proxy.example:1080'},
          'resolver': {'kind': 'proxy', 'transport': 'proxy'},
        },
        {
          'proxy': {'kind': 'direct'},
          'resolver': {'kind': 'proxy', 'transport': 'proxy'},
        },
        {
          'proxy': {'kind': 'socks5h', 'endpoint': 'proxy.example:1080'},
          'resolver': {'kind': 'system', 'transport': 'direct'},
        },
        {
          'proxy': {'kind': 'socks5', 'endpoint': 'proxy.example:01080'},
          'resolver': {'kind': 'system', 'transport': 'direct'},
        },
        {
          'proxy': {'kind': 'direct'},
          'resolver': {
            'kind': 'doh',
            'dohUrl': 'https://resolver.example/dns-query?token=secret',
            'transport': 'direct',
          },
        },
        {
          'proxy': {'kind': 'direct'},
          'resolver': {
            'kind': 'doh',
            'dohUrl': 'https://8.8.8.8',
            'transport': 'direct',
          },
        },
      ]) {
        expect(
          () => TrafficEgressPolicy.fromJson(invalid, 'egress'),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );

  test('egress profile contract freezes exact published network policy', () {
    final profile = EgressProfileRevision.fromJson({
      'id': 'profile.office',
      'revision': 3,
      'displayName': 'Office',
      'policy': {
        'proxy': {'kind': 'socks5', 'endpoint': '127.0.0.1:7890'},
        'resolver': {'kind': 'system', 'transport': 'direct'},
      },
      'publishedAt': '2026-08-27T01:02:03.000Z',
    }, 'profile');
    expect(profile.id, 'profile.office');
    expect(profile.policy.proxy.endpoint, '127.0.0.1:7890');
    expect(
      EgressProfileCatalog.fromJson({
        'items': [profile.toJson()],
      }, 'profiles').items.single,
      profile,
    );
  });

  test(
    'traffic transform contract is strict, bounded, and round-trips source',
    () {
      final policy = TrafficTransformPolicy.fromJson({
        'requestJavaScript': 'request.body = request.body.trim();',
        'responseJavaScript': 'response.headers["x-audit"] = "yes";',
      }, 'transform');
      expect(policy.enabled, isTrue);
      expect(policy.toJson(), {
        'requestJavaScript': 'request.body = request.body.trim();',
        'responseJavaScript': 'response.headers["x-audit"] = "yes";',
      });
      expect(
        TrafficTransformPolicy.fromJson({
          'requestJavaScript': '',
          'responseJavaScript': '',
        }, 'transform'),
        const TrafficTransformPolicy.disabled(),
      );
      expect(
        () => TrafficTransformPolicy.fromJson({
          'requestJavaScript': '',
        }, 'transform'),
        throwsA(isA<ControlContractException>()),
      );
      expect(
        () => TrafficTransformPolicy.fromJson({
          'requestJavaScript': 'request.body = "\u0000";',
          'responseJavaScript': '',
        }, 'transform'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Code Library Transform revision round-trips immutable source', () {
    final revision = CodeLibraryTransformRevision.fromJson({
      'id': 'home-redaction',
      'revision': 7,
      'collectionId': 'privacy',
      'displayName': 'Home redaction',
      'policy': {
        'requestJavaScript': 'request.body = request.body.trim();',
        'responseJavaScript': '',
      },
      'publishedAt': '2026-08-27T10:11:12.123Z',
    }, 'transform');

    expect(revision.id, 'home-redaction');
    expect(revision.revision, 7);
    expect(revision.policy.requestJavaScript, contains('trim'));
    expect(
      CodeLibraryTransformRevision.fromJson(
        jsonDecode(jsonEncode(revision.toJson())),
        'transform',
      ),
      revision,
    );
    expect(
      () => CodeLibraryTransformRevision.fromJson({
        ...revision.toJson(),
        'policy': {
          'requestJavaScript': 'request.body = "\u0000";',
          'responseJavaScript': '',
        },
      }, 'transform'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Account Selector revision and Route authority round-trip', () {
    final selector = CodeLibraryAccountSelectorRevision.fromJson({
      'id': 'workspace-account',
      'revision': 3,
      'collectionId': 'routing',
      'displayName': 'Workspace account',
      'policy': {'javaScript': 'selection.accountId = accounts[0].id;'},
      'publishedAt': '2026-08-28T10:11:12.123Z',
    }, 'selector');
    final policy = RouteAccountPolicy.fromJson({
      'revision': 4,
      'mode': 'javascript',
      'selector': selector.toJson(),
      'accounts': [
        {'id': 'account.work', 'revision': 2, 'displayName': 'Work'},
      ],
    }, 'accountPolicy');

    expect(selector.policy.javaScript, contains('accounts[0]'));
    expect(policy.mode, 'javascript');
    expect(policy.selector, selector);
    expect(policy.accounts.single.id, 'account.work');
    expect(
      RouteAccountPolicy.fromJson(
        jsonDecode(jsonEncode(policy.toJson())),
        'accountPolicy',
      ),
      policy,
    );
    expect(
      () => RouteAccountPolicy.fromJson({
        ...policy.toJson(),
        'fixedAccountId': 'account.work',
      }, 'accountPolicy'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test(
    'Environment rejects mutable or cross-shaped Account authority',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );
      final json =
          jsonDecode(jsonEncode(work.toJson())) as Map<String, dynamic>;
      final clientEndpoint = (json['clientEndpoints'] as List).first as Map;
      final protocolPlan =
          (clientEndpoint['protocolPlans'] as List).first as Map;
      final destination = protocolPlan['destination'] as Map;
      final upstreamPlan = destination['upstream'] as Map;
      final route = (upstreamPlan['routes'] as List).first as Map;
      final accountPolicy = route['accountPolicy'] as Map;
      final accounts = accountPolicy['accounts'] as List;
      accounts.clear();

      expect(
        () => EnvironmentRecord.fromJson(json, 'environment'),
        throwsA(isA<ControlContractException>()),
      );

      final wrongOwner =
          jsonDecode(jsonEncode(work.toJson())) as Map<String, dynamic>;
      wrongOwner['systemOwned'] = true;
      expect(
        () => EnvironmentRecord.fromJson(wrongOwner, 'environment'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Raw reveal presents a redacted credential field without its value', () {
    final json = _rawRevealJson();
    (json['headers']! as List<Object?>).add({
      'name': 'Authorization',
      'redacted': [
        {'digest': List.filled(64, 'a').join(), 'bytes': 41},
      ],
    });
    // The envelope counts header values as observed, before redaction, so a
    // redacted value still counts. Anything else would make the reveal of every
    // credential-bearing envelope fail its own reconciliation.
    (json['envelope']! as Map<String, Object?>)['headerCount'] = 3;

    final reveal = RevealedRawEvidence.fromJson(
      json,
      'rawReveal',
      expectedEnvelopeId: 'raw-test',
    );

    final authorization = reveal.headers.firstWhere(
      (field) => field.name == 'Authorization',
    );
    expect(authorization.values, isEmpty);
    expect(authorization.redacted.single.bytes, 41);
    expect(authorization.redacted.single.digest, List.filled(64, 'a').join());
  });

  test(
    'Raw reveal rejects a header field carrying both a value and a digest',
    () {
      final json = _rawRevealJson();
      (json['headers']! as List<Object?>).add({
        'name': 'Authorization',
        'values': ['Bearer leaked'],
        'redacted': [
          {'digest': List.filled(64, 'a').join(), 'bytes': 13},
        ],
      });

      expect(
        () => RevealedRawEvidence.fromJson(
          json,
          'rawReveal',
          expectedEnvelopeId: 'raw-test',
        ),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Captured Transform inputs become one editable SSE test sample', () {
    final request = RevealedRawEvidence.fromJson(
      _transformInputRevealJson(
        envelopeId: 'raw-transform-request',
        layer: 'transform_request_input',
        method: 'POST',
        path: '/v1/responses',
        contentType: 'application/json',
        representation: 'message_transform_input',
        body: '{"model":"captured","input":"private"}',
      ),
      'requestReveal',
      expectedEnvelopeId: 'raw-transform-request',
    );
    final response = RevealedRawEvidence.fromJson(
      _transformInputRevealJson(
        envelopeId: 'raw-transform-response',
        layer: 'transform_response_input',
        statusCode: 200,
        contentType: 'text/event-stream',
        representation: 'message_transform_stream_input',
        body:
            'event: response.completed\n'
            'data: {"status":"completed"}\n\n',
      ),
      'responseReveal',
      expectedEnvelopeId: 'raw-transform-response',
    );

    final captured = CapturedMessageTransformSample.fromRawEvidence(
      request: request,
      response: response,
    );

    expect(captured.exchangeId, 'exchange-transform-sample');
    expect(captured.wireProtocol, 'openai_responses');
    expect(captured.sample.request.body, contains('private'));
    expect(captured.sample.response.streaming, isTrue);
    expect(captured.sample.response.headers['content-type'], [
      'text/event-stream',
    ]);
  });

  test('Raw reveal rejects tampered body digests and frame ranges', () {
    final valid = _rawRevealJson();
    final reveal = RevealedRawEvidence.fromJson(
      valid,
      'rawReveal',
      expectedEnvelopeId: 'raw-test',
    );
    expect(utf8.decode(reveal.body), 'hello');
    expect(reveal.headers.single.values, ['one', 'two']);
    expect(reveal.frames.single.length, 5);

    final wrongDigest = jsonDecode(jsonEncode(valid)) as Map<String, dynamic>;
    (wrongDigest['envelope'] as Map<String, dynamic>)['bodySha256'] =
        List.filled(64, '0').join();
    expect(
      () => RevealedRawEvidence.fromJson(
        wrongDigest,
        'rawReveal',
        expectedEnvelopeId: 'raw-test',
      ),
      throwsA(isA<ControlContractException>()),
    );

    final invalidFrame = jsonDecode(jsonEncode(valid)) as Map<String, dynamic>;
    final frame =
        (invalidFrame['frames'] as List<dynamic>).single
            as Map<String, dynamic>;
    frame['offset'] = 4;
    frame['length'] = 2;
    expect(
      () => RevealedRawEvidence.fromJson(
        invalidFrame,
        'rawReveal',
        expectedEnvelopeId: 'raw-test',
      ),
      throwsA(isA<ControlContractException>()),
    );

    final nullableEmptyCollections = _rawRevealJson();
    final envelope =
        nullableEmptyCollections['envelope']! as Map<String, Object?>;
    envelope['headerCount'] = 0;
    envelope['trailerCount'] = 0;
    nullableEmptyCollections['headers'] = null;
    nullableEmptyCollections['trailers'] = null;
    nullableEmptyCollections['frames'] = null;
    final normalized = RevealedRawEvidence.fromJson(
      nullableEmptyCollections,
      'rawReveal',
      expectedEnvelopeId: 'raw-test',
    );
    expect(normalized.headers, isEmpty);
    expect(normalized.trailers, isEmpty);
    expect(normalized.frames, isEmpty);

    nullableEmptyCollections['trailers'] = 'not-an-array';
    expect(
      () => RevealedRawEvidence.fromJson(
        nullableEmptyCollections,
        'rawReveal',
        expectedEnvelopeId: 'raw-test',
      ),
      throwsA(isA<ControlContractException>()),
    );
  });
}

Map<String, Object?> _rawRevealJson() => {
  'envelope': {
    'envelopeId': 'raw-test',
    'layer': 'provider_response',
    'scopeKind': 'managed_run',
    'scopeId': 'run-test',
    'exchangeId': 'exchange-test',
    'observedAt': '2026-08-11T00:00:00.000Z',
    'expiresAt': '2026-09-10T00:00:00.000Z',
    'statusCode': 200,
    'headerCount': 2,
    'trailerCount': 1,
    'bodyBytes': 5,
    'bodySha256':
        '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824',
    'digestScope': 'full_body',
    'payloadState': 'captured',
    'redactedCredentialFields': <String>[],
    'revealAvailable': true,
  },
  'headers': [
    {
      'name': 'X-Repeat',
      'values': ['one', 'two'],
    },
  ],
  'trailers': [
    {
      'name': 'X-Trailer',
      'values': ['done'],
    },
  ],
  'bodyBase64': 'aGVsbG8=',
  'frames': [
    {'kind': 'data', 'offset': 0, 'length': 5},
  ],
};

Map<String, Object?> _transformInputRevealJson({
  required String envelopeId,
  required String layer,
  required String contentType,
  required String representation,
  required String body,
  String? method,
  String? path,
  int? statusCode,
}) {
  final bodyBytes = utf8.encode(body);
  return {
    'envelope': {
      'envelopeId': envelopeId,
      'layer': layer,
      'scopeKind': 'managed_run',
      'scopeId': 'run-transform-sample',
      'exchangeId': 'exchange-transform-sample',
      'attemptId': 'attempt-transform-sample',
      'observedAt': '2026-08-11T00:00:00.000Z',
      'expiresAt': '2026-09-10T00:00:00.000Z',
      'method': ?method,
      'path': ?path,
      'statusCode': ?statusCode,
      'contentType': contentType,
      'representation': representation,
      'headerCount': 1,
      'trailerCount': 0,
      'bodyBytes': bodyBytes.length,
      'bodySha256': crypto.sha256.convert(bodyBytes).toString(),
      'digestScope': 'full_body',
      'payloadState': 'captured',
      'redactedCredentialFields': <String>[],
      'revealAvailable': true,
    },
    'headers': [
      {
        'name': 'Content-Type',
        'values': [contentType],
      },
    ],
    'trailers': <Object?>[],
    'bodyBase64': base64.encode(bodyBytes),
    'frames': <Object?>[],
  };
}

Map<String, Object?> _codexClientIdentityJson() => {
  'client': 'codex',
  'sessionId': 'session-root-1',
  'sessionResumable': true,
  'actorId': 'thread-subagent-1',
  'actorLabel': 'reviewer',
  'actorType': 'worker',
  'actorIsSubagent': true,
  'providerResponseId': 'response-1',
  'source': 'client_local_state',
  'confidence': 'exact',
  'observedAt': '2026-08-11T00:00:00.000Z',
  'protocolIds': <Object?>[
    {'name': 'codex.response_item_id', 'value': 'item-1'},
    {'name': 'codex.session_id', 'value': 'session-root-1'},
    {'name': 'codex.thread_id', 'value': 'thread-subagent-1'},
    {'name': 'codex.turn_id', 'value': 'turn-7'},
  ],
  'attributes': <Object?>[
    {'name': 'codex.agent_nickname', 'value': 'reviewer'},
    {'name': 'codex.spawn_depth', 'value': '1'},
  ],
};

Map<String, Object?> _networkApprovalJson() => {
  'id': 'approval-network-test',
  'revision': 2,
  'kind': 'network_ask',
  'state': 'pending',
  'risk': 'medium',
  'titleKey': 'approval.networkAsk.title',
  'summaryKey': 'approval.networkAsk.summary',
  'aggregateKey': 'network:example.com:443',
  'target': {'host': 'example.com', 'port': 443},
  'subjectRefs': ['connection-test'],
  'subjectLabels': ['example.com:443'],
  'requestCount': 1,
  'waiterCount': 1,
  'choices': <Object?>[
    {
      'decision': 'allow-once',
      'scope': 'request',
      'labelKey': 'approval.networkAsk.choice.allowOnce',
    },
    {
      'decision': 'allow-once',
      'scope': 'host_port',
      'labelKey': 'approval.networkAsk.choice.allowHostPort',
    },
    {
      'decision': 'deny',
      'scope': 'request',
      'labelKey': 'approval.networkAsk.choice.denyOnce',
    },
    {
      'decision': 'deny',
      'scope': 'host_port',
      'labelKey': 'approval.networkAsk.choice.denyHostPort',
    },
  ],
  'createdAt': '2026-08-11T00:00:00.000Z',
  'expiresAt': '2026-08-11T00:10:00.000Z',
};

Map<String, Object?> _toolApprovalJson() => {
  'id': 'approval-tool-test',
  'revision': 3,
  'kind': 'tool_intent',
  'state': 'pending',
  'risk': 'high',
  'titleKey': 'approval.toolIntent.title',
  'summaryKey': 'approval.toolIntent.summary',
  'aggregateKey': 'tool:exchange-test',
  'exchangeId': 'exchange-test',
  'environmentId': 'work',
  'environmentRevision': 7,
  'environmentDigest': List.filled(64, 'a').join(),
  'routeId': 'anthropic-direct',
  'routeRevision': 4,
  'subjectRefs': ['tool-call-1'],
  'subjectLabels': ['Read'],
  'requestCount': 1,
  'waiterCount': 1,
  'choices': <Object?>[
    {
      'decision': 'allow-once',
      'scope': 'request',
      'labelKey': 'approval.toolIntent.choice.allowOnce',
    },
    {
      'decision': 'deny',
      'scope': 'request',
      'labelKey': 'approval.toolIntent.choice.deny',
    },
  ],
  'createdAt': '2026-08-11T00:00:00.000Z',
  'expiresAt': '2026-08-11T00:10:00.000Z',
};

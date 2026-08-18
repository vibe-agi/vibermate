import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
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
          'machineId': machineId,
          'machineRegistrationRevision': 1,
          'workspaceId': workspaceId,
          'workspaceLabel': 'vibermate',
          'workspaceEvidence': 'local_launcher',
          'workspaceDerivationRevision': 1,
          'processId': 7300,
          'recognition': 'verified',
          'expiresAt': '2026-08-10T10:00:00.000Z',
          'firstObservedAt': '2026-08-10T09:00:30.000Z',
        },
      }, 'capture');

      expect(record.running, isTrue);
      expect(record.managedRun!.machineId, machineId);
      expect(record.managedRun!.workspaceId, workspaceId);
      expect(record.managedRun!.workspaceEvidence, 'local_launcher');
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

  test(
    'Workspace default accepts only exact workspace and Environment authority',
    () {
      final record = WorkspaceEnvironmentDefault.fromJson(
        {
          'machineId': machineId,
          'workspaceId': workspaceId,
          'environmentId': 'work',
          'environmentName': 'Work',
          'revision': 3,
          'updatedAt': '2026-08-10T09:01:00.000Z',
        },
        'workspaceDefault',
        expectedMachineId: machineId,
        expectedWorkspaceId: workspaceId,
      );
      expect(record.environmentId, 'work');
      expect(record.revision, 3);

      expect(
        () => WorkspaceEnvironmentDefault.fromJson(
          {
            'machineId': machineId,
            'workspaceId': workspaceId,
            'environmentId': 'system_transparent',
            'environmentName': 'Transparent',
            'revision': 1,
            'updatedAt': '2026-08-10T09:01:00.000Z',
          },
          'workspaceDefault',
          expectedMachineId: machineId,
          expectedWorkspaceId: workspaceId,
        ),
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
      'kind': 'openai_api_key',
      'realmId': 'openai.test',
      'state': 'active',
      'revision': 1,
      'credentialState': 'credential_missing',
      'credentialEpoch': 0,
    }, 'providerAccount');

    expect(account.credentialEpoch, 0);
    expect(account.usable, isFalse);
  });

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
    final activity = ActivityRecord.fromJson({
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
    }, 'activity');

    expect(activity.environment.accountRevision, 5);
    expect(activity.captureRunId, 'run-test');

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

      final decoded = EnvironmentRecord.fromJson(
        jsonDecode(jsonEncode(work.toJson())),
        'environment',
      );

      expect(decoded.id, work.id);
      expect(decoded.revision, work.revision);
      expect(decoded.clientEndpoints.length, work.clientEndpoints.length);
      expect(
        decoded.routes.map((route) => route.id),
        work.routes.map((route) => route.id),
      );
      expect(
        decoded.routes.first.accountPolicy.accountRevisions,
        work.routes.first.accountPolicy.accountRevisions,
      );
    },
  );

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
      final upstreamPlan = protocolPlan['upstreamPlan'] as Map;
      final route = (upstreamPlan['routes'] as List).first as Map;
      final accountPolicy = route['accountPolicy'] as Map;
      final revisions = accountPolicy['accountRevisions'] as Map;
      revisions.remove('anthropic-lab');

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

  test('Raw reveal rejects a header field carrying both a value and a digest', () {
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

import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;

import '../core/api/control_api.dart';
import '../core/api/control_models.dart';

final class PreviewControlApi implements ControlApi {
  PreviewControlApi({int dashboardCaptureLimit = 50})
    : _dashboardCaptureLimit = dashboardCaptureLimit {
    if (dashboardCaptureLimit < 1 || dashboardCaptureLimit > 199) {
      throw ArgumentError.value(dashboardCaptureLimit, 'dashboardCaptureLimit');
    }
    _environments = _initialEnvironments();
    for (final environment in _environments) {
      _environmentHistory[_environmentRevisionKey(
            environment.id,
            environment.revision,
          )] =
          environment;
      if (!environment.systemOwned) {
        _environmentDraftCounters[environment.id] = environment.revision;
      }
    }
    for (var index = 0; index < 8; index += 1) {
      final id = 'run-${index + 1}';
      final capture = CaptureRecord(
        key: 'managed_run:$id',
        id: id,
        kind: 'managed_run',
        displayName: index.isEven ? 'Claude Code' : 'Codex',
        state: index < 7 ? 'attached' : 'finished',
        observation: 'observed',
        createdAt: _now.subtract(Duration(hours: index + 1)),
        updatedAt: _now.subtract(Duration(minutes: index * 3 + 1)),
        managedRun: ManagedRunSummary(
          executableLabel: index.isEven ? 'claude' : 'codex',
          cwd: index < 4
              ? '/Users/mira/Code/vibermate'
              : '/Users/mira/Code/agent-lab-${index - 3}',
          canonicalExecutablePath: index.isEven
              ? '/usr/local/bin/claude'
              : '/usr/local/bin/codex',
          recognition: 'verified',
          expiresAt: _now.add(const Duration(hours: 1)),
          localUserLabel: 'mira',
          machineId: _previewMachineId,
          machineRegistrationRevision: 1,
          workspaceId: index < 4 ? _previewWorkspaceId : _identity(9 + index),
          workspaceLabel: index < 4 ? 'vibermate' : 'agent-lab-${index - 3}',
          workspaceEvidence: 'local_launcher',
          workspaceDerivationRevision: 1,
          processId: 7300 + index,
          firstObservedAt: _now.subtract(Duration(minutes: index * 3 + 2)),
        ),
      );
      _captures[capture.key] = capture;
      _assignments[capture.key] = CaptureAssignment(
        captureKey: capture.key,
        captureId: capture.id,
        captureKind: capture.kind,
        environmentId: index < 5 ? 'work' : 'research',
        revision: 1,
        source: 'launch',
        updatedAt: capture.createdAt,
      );
    }
    const manualId = 'manual-figma';
    final manual = CaptureRecord(
      key: 'manual_capture:$manualId',
      id: manualId,
      kind: 'manual_capture',
      displayName: 'Figma Desktop',
      state: 'active',
      observation: 'observed',
      createdAt: _now.subtract(const Duration(hours: 6)),
      updatedAt: _now.subtract(const Duration(minutes: 2)),
      manualCapture: ManualCaptureSummary(
        clientClass: 'desktop_app',
        lifetime: 'until_revoked',
        credentialRevision: 2,
        lastObservedAt: _now.subtract(const Duration(minutes: 2)),
      ),
    );
    _captures[manual.key] = manual;
    _assignments[manual.key] = CaptureAssignment(
      captureKey: manual.key,
      captureId: manual.id,
      captureKind: manual.kind,
      environmentId: 'work',
      revision: 2,
      source: 'operator_switch',
      updatedAt: _now.subtract(const Duration(hours: 1)),
    );
    _manualRecords[manualId] = ManualCaptureRecord(
      id: manualId,
      displayName: manual.displayName,
      clientClass: manual.manualCapture!.clientClass,
      lifetime: manual.manualCapture!.lifetime,
      state: manual.state,
      observation: manual.observation,
      createdAt: manual.createdAt,
      updatedAt: manual.updatedAt,
      expiresAt: null,
      lastObservedAt: manual.manualCapture!.lastObservedAt,
    );
    _manualVersions[manualId] = manual.manualCapture!.credentialRevision;
  }

  final int _dashboardCaptureLimit;

  static final _now = DateTime.utc(2026, 8, 10, 9, 42);
  static String _identity(int byte) =>
      base64Url.encode(List<int>.filled(32, byte)).replaceAll('=', '');
  static final _previewMachineId = _identity(7);
  static final _previewWorkspaceId = _identity(8);
  static final _contextToken = 'ctx_${List.filled(43, 'C').join()}';
  static const _previewDigest =
      'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';

  final Map<String, CaptureRecord> _captures = {};
  final Map<String, CaptureAssignment> _assignments = {};
  final Map<String, WorkspaceEnvironmentDefault> _workspaceDefaults = {
    '$_previewMachineId:$_previewWorkspaceId': WorkspaceEnvironmentDefault(
      machineId: _previewMachineId,
      workspaceId: _previewWorkspaceId,
      environmentId: 'work',
      environmentName: 'Work',
      revision: 1,
      updatedAt: _now.subtract(const Duration(hours: 3)),
    ),
  };
  final Map<String, ManualCaptureRecord> _manualRecords = {};
  final Map<String, int> _manualVersions = {};
  ApprovalRecord _approval = _pendingApproval;
  ConnectionRuleSet _rules = const ConnectionRuleSet(
    revision: 3,
    rules: [
      ConnectionRule(
        id: 'allow-anthropic',
        priority: 10,
        decision: 'allow',
        match: 'exact_host_port',
        host: 'api.anthropic.com',
        port: 443,
      ),
      ConnectionRule(
        id: 'review-source-control',
        priority: 20,
        decision: 'ask',
        match: 'exact_host',
        host: 'github.com',
        port: null,
      ),
    ],
    mode: 'ask_unknown',
  );
  bool _closed = false;
  OfflineHoldSnapshot _offline = OfflineHoldSnapshot(
    state: 'online',
    revision: 1,
    since: _now.subtract(const Duration(hours: 19)),
    activeActions: 0,
    enteringActions: 0,
    activeEgress: 0,
    queuedRequests: 0,
    heldBytes: 0,
    safeToDisconnect: false,
    activeByKind: const {},
    queuedByKind: const {},
    lastProbeReason: null,
  );

  static final ApprovalRecord _pendingApproval = ApprovalRecord(
    id: 'approval-network-github',
    revision: 2,
    kind: 'network_ask',
    state: 'pending',
    risk: 'medium',
    titleKey: 'approval.networkAsk.title',
    summaryKey: 'approval.networkAsk.summary',
    aggregateKey: 'network:github.com:443',
    exchangeId: null,
    environmentId: null,
    environmentRevision: null,
    environmentDigest: null,
    routeId: null,
    routeRevision: null,
    target: const ApprovalTarget(host: 'github.com', port: 443),
    subjectRefs: const ['connection-preview-1', 'connection-preview-2'],
    subjectLabels: const ['Claude Code · vibermate', 'Codex · gateway'],
    requestCount: 3,
    waiterCount: 2,
    choices: const [
      ApprovalChoice(
        decision: 'allow-once',
        scope: 'request',
        labelKey: 'approval.networkAsk.choice.allowOnce',
      ),
      ApprovalChoice(
        decision: 'allow-once',
        scope: 'host_port',
        labelKey: 'approval.networkAsk.choice.allowHostPort',
      ),
      ApprovalChoice(
        decision: 'deny',
        scope: 'request',
        labelKey: 'approval.networkAsk.choice.denyOnce',
      ),
      ApprovalChoice(
        decision: 'deny',
        scope: 'host_port',
        labelKey: 'approval.networkAsk.choice.denyHostPort',
      ),
    ],
    createdAt: _now.subtract(const Duration(minutes: 2)),
    expiresAt: _now.add(const Duration(minutes: 8)),
    resolvedAt: null,
    decision: null,
    decisionScope: null,
    terminalReason: null,
  );

  static final List<ConnectionRecord> _connectionEvidence = List.generate(
    18,
    (index) => ConnectionRecord(
      sequence: 180 - index,
      connectionId: 'connection-${index + 1}',
      sourceLabel: index.isEven ? 'Claude Code' : 'Codex',
      sourceConfidence: 'verified',
      environmentId: index < 12 ? 'work' : 'research',
      environmentName: index < 12 ? 'Work' : 'Research',
      requestedHost: switch (index % 4) {
        0 => 'api.anthropic.com',
        1 => 'api.openai.com',
        2 => 'github.com',
        _ => 'tokyo.orbitrelay.example',
      },
      observedSni: index % 4 == 2 ? 'github.com' : null,
      routeHost: null,
      ip: '203.0.113.${index + 10}',
      port: 443,
      decision: index == 7 ? 'ask' : 'allow',
      ruleId: index == 7 ? 'review-source-control' : 'allow-anthropic',
      egressScope: 'network',
      egressSource: 'network_rule',
      decryption: index % 5 == 0 ? 'blind' : 'mitm',
      phase: index == 7
          ? 'asked'
          : index < 3
          ? 'connected'
          : 'closed',
      bytesUp: 640 + index * 71,
      bytesDown: 2048 + index * 139,
      startedAt: _now.subtract(Duration(minutes: index * 3 + 1)),
      endedAt: index < 3 ? null : _now.subtract(Duration(minutes: index * 3)),
      outcome: index < 3 ? null : 'completed',
      errorClass: null,
    ),
  );

  static final List<EgressAttemptRecord> _egressEvidence = List.generate(
    22,
    (index) => EgressAttemptRecord(
      sequence: index + 1,
      id: 'egress-${index + 1}',
      connectionId: 'connection-${index % 18 + 1}',
      purpose: index % 6 == 0 ? 'route_operation' : 'provider_attempt',
      payloadClass: index % 6 == 0 ? 'control' : 'client_semantic',
      parentKind: 'upstream_attempt',
      parentId: 'attempt-${index + 1}',
      exchangeId: 'exchange-${index + 1}',
      caller: 'core',
      callerId: null,
      targetOrigin: index.isEven
          ? 'https://api.anthropic.com'
          : 'https://api.openai.com',
      authority: 'environment',
      policyId: index < 12 ? 'egress.work' : 'egress.research',
      ruleId: null,
      proxyId: null,
      reusedTransport: index % 3 != 0,
      startedAt: _now.subtract(Duration(minutes: index * 2 + 1)),
      terminal: true,
      outcome: index == 9 ? 'failed' : 'completed',
      errorClass: index == 9 ? 'provider_timeout' : null,
      bytesOut: 320 + index * 53,
      bytesIn: index == 9 ? 0 : 1040 + index * 97,
      completedAt: _now.subtract(Duration(minutes: index * 2)),
    ),
  );

  final List<UpstreamEndpoint> _endpoints = [
    UpstreamEndpoint(
      id: 'target.anthropic.official',
      displayName: 'Anthropic API',
      origin: Uri.parse('https://api.anthropic.com'),
      realmId: 'anthropic.official',
      backendProtocols: const ['anthropic_messages'],
      capabilities: const ['messages', 'streaming', 'tool_calls'],
      accountKinds: const ['anthropic_api_key', 'claude_oauth_token'],
      state: 'active',
      revision: 1,
    ),
    UpstreamEndpoint(
      id: 'target.openai.official',
      displayName: 'OpenAI API',
      origin: Uri.parse('https://api.openai.com'),
      realmId: 'openai.platform',
      backendProtocols: const ['openai_responses', 'openai_chat'],
      capabilities: const ['messages', 'streaming', 'tool_calls'],
      accountKinds: const ['openai_api_key'],
      state: 'active',
      revision: 1,
    ),
    UpstreamEndpoint(
      id: 'target.orbit.relay',
      displayName: 'Orbit Relay · Tokyo',
      origin: Uri.parse('https://tokyo.orbitrelay.example'),
      realmId: 'relay.orbit.tokyo',
      backendProtocols: const ['anthropic_messages', 'openai_responses'],
      capabilities: const ['messages', 'streaming', 'tool_calls'],
      accountKinds: const ['anthropic_api_key', 'openai_api_key'],
      state: 'active',
      revision: 4,
    ),
  ];

  final List<ProviderAccount> _accounts = [
    ProviderAccount(
      id: 'anthropic-work',
      displayName: 'Anthropic · Work',
      upstreamEndpointId: 'target.anthropic.official',
      kind: 'anthropic_api_key',
      realmId: 'anthropic.official',
      state: 'active',
      revision: 2,
      credentialState: 'ready',
      credentialEpoch: 3,
    ),
    ProviderAccount(
      id: 'anthropic-lab',
      displayName: 'Anthropic · Lab',
      upstreamEndpointId: 'target.anthropic.official',
      kind: 'anthropic_api_key',
      realmId: 'anthropic.official',
      state: 'active',
      revision: 1,
      credentialState: 'ready',
      credentialEpoch: 1,
    ),
    ProviderAccount(
      id: 'openai-work',
      displayName: 'OpenAI · Work',
      upstreamEndpointId: 'target.openai.official',
      kind: 'openai_api_key',
      realmId: 'openai.platform',
      state: 'active',
      revision: 1,
      credentialState: 'ready',
      credentialEpoch: 2,
    ),
    ProviderAccount(
      id: 'orbit-team',
      displayName: 'Orbit · Team Pool',
      upstreamEndpointId: 'target.orbit.relay',
      kind: 'anthropic_api_key',
      realmId: 'relay.orbit.tokyo',
      state: 'active',
      revision: 3,
      credentialState: 'ready',
      credentialEpoch: 5,
    ),
  ];

  late final List<EnvironmentRecord> _environments;
  final Map<String, EnvironmentRecord> _environmentHistory = {};
  final Map<String, EnvironmentDraft> _environmentDrafts = {};
  final Map<String, int> _environmentDraftCounters = {};

  List<EnvironmentRecord> _initialEnvironments() => [
    _environment(
      id: 'system_transparent',
      name: 'Transparent',
      revision: 1,
      digestCharacter: '1',
      systemOwned: true,
      endpoints: const [],
    ),
    _environment(
      id: 'work',
      name: 'Work',
      revision: 7,
      digestCharacter: '2',
      endpoints: [
        _clientEndpoint(
          id: 'claude-client',
          origin: 'https://api.anthropic.com',
          protocol: 'anthropic_messages',
          routes: [
            _route(
              id: 'anthropic-direct',
              endpointId: 'target.anthropic.official',
              protocol: 'anthropic_messages',
              accountIds: const ['anthropic-work', 'anthropic-lab'],
            ),
            _route(
              id: 'orbit-fallback',
              endpointId: 'target.orbit.relay',
              protocol: 'anthropic_messages',
              accountIds: const ['orbit-team'],
            ),
          ],
          defaultRouteId: 'anthropic-direct',
        ),
        _clientEndpoint(
          id: 'codex-client',
          origin: 'https://api.openai.com',
          protocol: 'openai_responses',
          routes: [
            _route(
              id: 'openai-direct',
              endpointId: 'target.openai.official',
              protocol: 'openai_responses',
              accountIds: const ['openai-work'],
            ),
          ],
          defaultRouteId: 'openai-direct',
        ),
      ],
    ),
    _environment(
      id: 'research',
      name: 'Research',
      revision: 3,
      digestCharacter: '3',
      endpoints: [
        _clientEndpoint(
          id: 'research-claude',
          origin: 'https://api.anthropic.com',
          protocol: 'anthropic_messages',
          routes: [
            _route(
              id: 'research-orbit',
              endpointId: 'target.orbit.relay',
              protocol: 'anthropic_messages',
              accountIds: const ['orbit-team'],
            ),
          ],
          defaultRouteId: 'research-orbit',
        ),
      ],
    ),
  ];

  static String _environmentRevisionKey(String environmentId, int revision) =>
      '$environmentId@$revision';

  static EnvironmentRecord _environment({
    required String id,
    required String name,
    required int revision,
    required String digestCharacter,
    required List<EnvironmentClientEndpoint> endpoints,
    bool systemOwned = false,
  }) => EnvironmentRecord(
    id: id,
    name: name,
    state: 'active',
    revision: revision,
    digest: List.filled(64, digestCharacter).join(),
    systemOwned: systemOwned,
    clientEndpoints: endpoints,
    pluginBindings: const [],
    budgetPolicy: const EnvironmentBudgetPolicy(id: '', revision: 0),
    egressPolicy: const EnvironmentEgressPolicy(id: '', revision: 0, mode: ''),
    contentRecording: const EnvironmentContentRecordingPolicy(
      mode: 'full',
      retentionDays: 30,
    ),
    policySet: const EnvironmentPolicySet(toolMode: 'observe'),
  );

  static EnvironmentClientEndpoint _clientEndpoint({
    required String id,
    required String origin,
    required String protocol,
    required List<EnvironmentRoute> routes,
    required String defaultRouteId,
  }) => EnvironmentClientEndpoint(
    id: id,
    revision: 1,
    clientOrigin: Uri.parse(origin),
    protocolPlans: [
      EnvironmentProtocolPlan(
        id: '$id-plan',
        revision: 1,
        clientProtocol: protocol,
        clientAdapterPolicy: EnvironmentClientAdapterPolicy(
          id: '$id-adapter',
          revision: 1,
        ),
        mode: 'managed',
        defaultRouteId: defaultRouteId,
        routeSet: EnvironmentRouteSet(
          id: '$id-routes',
          revision: 1,
          candidateRouteIds: routes
              .map((route) => route.id)
              .toList(growable: false),
        ),
        routes: routes,
        pluginBindings: const [],
      ),
    ],
  );

  EnvironmentRoute _route({
    required String id,
    required String endpointId,
    required String protocol,
    required List<String> accountIds,
  }) {
    final endpoint = _endpoints.firstWhere(
      (candidate) => candidate.id == endpointId,
    );
    final accountRevisions = <String, int>{
      for (final accountId in accountIds)
        accountId: _accounts
            .firstWhere((candidate) => candidate.id == accountId)
            .revision,
    };
    return EnvironmentRoute(
      id: id,
      revision: 1,
      providerTarget: EnvironmentProviderTarget(
        id: endpoint.id,
        revision: endpoint.revision,
        origin: endpoint.origin,
        realmId: endpoint.realmId,
        capabilities: endpoint.capabilities,
      ),
      backendProtocol: protocol,
      accountPolicy: RouteAccountPolicy(
        revision: 1,
        mode: accountIds.isEmpty ? 'client_passthrough' : 'managed',
        preferredAccountId: accountIds.firstOrNull ?? '',
        candidateAccountIds: accountIds,
        accountRevisions: accountRevisions,
        failoverPolicy: accountIds.length > 1 ? 'account_scoped_safe' : 'off',
      ),
      modelPolicy: const EnvironmentModelPolicy(
        revision: 1,
        mode: 'passthrough',
        fixedModel: '',
      ),
      wireProfileRef: 'follow-client',
      pluginBindings: const [],
    );
  }

  @override
  Future<DashboardData> loadDashboard() async {
    _requireOpen();
    final capturePage = await captures(limit: _dashboardCaptureLimit);
    return DashboardData(
      status: RuntimeStatus(
        ready: true,
        state: 'initialized',
        host: 'desktop',
        schemaRevision: 1,
        storage: 'healthy',
        environmentProjection: 'healthy',
        unavailableEnvironments: null,
        offlineHold: _offline,
        instanceId: 'preview-instance',
        startedAt: _now.subtract(const Duration(hours: 19)),
        stoppedAt: null,
        stopReasonCode: null,
      ),
      captures: capturePage.items,
      captureNextCursor: capturePage.nextCursor,
      environments: List.unmodifiable(_environments),
      endpoints: List.unmodifiable(_endpoints),
      accounts: List.unmodifiable(_accounts),
    );
  }

  @override
  Future<CapturePage> captures({String? cursor, int limit = 50}) async {
    _requireOpen();
    if (limit < 1 || limit > 199) {
      throw const ControlContractException('Capture page limit is invalid');
    }
    var offset = 0;
    if (cursor != null) {
      final match = RegExp(
        r'^preview-captures-([1-9][0-9]*)$',
      ).firstMatch(cursor);
      if (match == null) {
        throw const ControlContractException('Capture cursor is invalid');
      }
      offset = int.parse(match.group(1)!);
    }
    final values = _captures.values.toList(growable: false)
      ..sort((left, right) {
        if (left.running != right.running) return left.running ? -1 : 1;
        final updated = right.updatedAt.compareTo(left.updatedAt);
        if (updated != 0) return updated;
        final leftKind = left.kind == 'managed_run' ? 0 : 1;
        final rightKind = right.kind == 'managed_run' ? 0 : 1;
        if (leftKind != rightKind) return leftKind.compareTo(rightKind);
        return left.id.compareTo(right.id);
      });
    if (offset > values.length) {
      throw const ControlContractException('Capture cursor is stale');
    }
    final requestedEnd = offset + limit;
    final end = requestedEnd < values.length ? requestedEnd : values.length;
    return CapturePage(
      items: List.unmodifiable(values.sublist(offset, end)),
      nextCursor: end < values.length ? 'preview-captures-$end' : null,
    );
  }

  @override
  Future<OfflineHoldSnapshot> enterOfflineHold(
    OfflineHoldSnapshot current,
  ) async {
    _requireOpen();
    if (current.revision != _offline.revision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    if (!_offline.canEnter) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_transition',
        messageKey: 'error.invalid_transition',
      );
    }
    _offline = OfflineHoldSnapshot(
      state: 'held',
      revision: current.revision + 2,
      since: _now,
      activeActions: 0,
      enteringActions: 0,
      activeEgress: 0,
      queuedRequests: 0,
      heldBytes: 0,
      safeToDisconnect: true,
      activeByKind: const {},
      queuedByKind: const {},
      lastProbeReason: null,
    );
    return _offline;
  }

  @override
  Future<OfflineHoldSnapshot> resumeOfflineHold(
    OfflineHoldSnapshot current,
  ) async {
    _requireOpen();
    if (current.revision != _offline.revision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    if (!_offline.canResume) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_transition',
        messageKey: 'error.invalid_transition',
      );
    }
    _offline = OfflineHoldSnapshot(
      state: 'online',
      revision: current.revision + 3,
      since: _now,
      activeActions: 0,
      enteringActions: 0,
      activeEgress: 0,
      queuedRequests: 0,
      heldBytes: 0,
      safeToDisconnect: false,
      activeByKind: const {},
      queuedByKind: const {},
      lastProbeReason: null,
    );
    return _offline;
  }

  @override
  Future<EnvironmentRecord> environmentRevision(
    String environmentId,
    int revision,
  ) async {
    _requireOpen();
    final environment =
        _environmentHistory[_environmentRevisionKey(environmentId, revision)];
    if (environment == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'environment_revision_not_found',
        messageKey: 'error.environment_revision_not_found',
      );
    }
    return environment;
  }

  @override
  Future<EnvironmentDraft> environmentDraft(String environmentId) async {
    _requireOpen();
    final draft = _environmentDrafts[environmentId];
    if (draft == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'environment_draft_not_found',
        messageKey: 'error.environment_draft_not_found',
      );
    }
    return draft;
  }

  @override
  Future<EnvironmentDraft> saveEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required EnvironmentDraftInput input,
  }) async {
    _requireOpen();
    input.validateFor(environmentId, expectedBaseRevision);
    final currentIndex = _environments.indexWhere(
      (environment) => environment.id == environmentId,
    );
    final current = currentIndex < 0 ? null : _environments[currentIndex];
    if (current?.systemOwned == true) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'environment_system_owned',
        messageKey: 'error.environment_system_owned',
      );
    }
    final existingDraft = _environmentDrafts[environmentId];
    if ((current?.revision ?? 0) != expectedBaseRevision ||
        (existingDraft?.draftRevision ?? 0) != input.expectedDraftRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final allocatedDraftRevision =
        (_environmentDraftCounters[environmentId] ?? 0) + 1;
    _environmentDraftCounters[environmentId] = allocatedDraftRevision;
    final draftRevision = allocatedDraftRevision;
    final digest = _environmentDigest(
      '$environmentId:$expectedBaseRevision:$draftRevision:${input.toJson()}',
    );
    final candidate = EnvironmentRecord(
      id: environmentId,
      name: input.name,
      state: input.state,
      revision: expectedBaseRevision + 1,
      digest: digest,
      systemOwned: false,
      clientEndpoints: input.clientEndpoints,
      pluginBindings: input.pluginBindings,
      budgetPolicy: input.budgetPolicy,
      egressPolicy: input.egressPolicy,
      contentRecording: input.contentRecording,
      policySet: input.policySet,
    );
    final draft = EnvironmentDraft(
      environmentId: environmentId,
      baseRevision: expectedBaseRevision,
      draftRevision: draftRevision,
      candidateDigest: digest,
      candidate: candidate,
    );
    _environmentDrafts[environmentId] = draft;
    return draft;
  }

  @override
  Future<EnvironmentImpact> previewEnvironmentDraft(
    String environmentId,
    int draftRevision,
  ) async {
    _requireOpen();
    return _environmentImpact(environmentId, draftRevision);
  }

  @override
  Future<EnvironmentPublishResult> publishEnvironmentDraft(
    String environmentId,
    int draftRevision,
  ) async {
    _requireOpen();
    final draft = _environmentDrafts[environmentId];
    if (draft == null || draft.draftRevision != draftRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final currentIndex = _environments.indexWhere(
      (environment) => environment.id == environmentId,
    );
    if ((currentIndex < 0 ? 0 : _environments[currentIndex].revision) !=
        draft.baseRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final impact = _environmentImpact(environmentId, draftRevision);
    if (currentIndex < 0) {
      _environments.add(draft.candidate);
    } else {
      _environments[currentIndex] = draft.candidate;
    }
    _environmentHistory[_environmentRevisionKey(
          draft.candidate.id,
          draft.candidate.revision,
        )] =
        draft.candidate;
    _environmentDrafts.remove(environmentId);
    return EnvironmentPublishResult(
      environment: draft.candidate,
      impact: impact,
    );
  }

  EnvironmentImpact _environmentImpact(
    String environmentId,
    int draftRevision,
  ) {
    final draft = _environmentDrafts[environmentId];
    if (draft == null || draft.draftRevision != draftRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final current = _environments
        .where((environment) => environment.id == environmentId)
        .firstOrNull;
    final classification = current == null
        ? 'hot_switch'
        : current.clientEndpoints.length !=
                  draft.candidate.clientEndpoints.length ||
              !_sameClientAuthorities(
                current.clientEndpoints,
                draft.candidate.clientEndpoints,
              )
        ? 'restart_required'
        : current.state != draft.candidate.state
        ? 'reconnect_required'
        : 'hot_switch';
    final affected = _captures.values
        .where(
          (capture) =>
              capture.running &&
              _assignments[capture.key]?.environmentId == environmentId,
        )
        .map(
          (capture) => EnvironmentImpactCapture(
            captureKind: capture.kind,
            captureId: capture.id,
            classification: classification,
          ),
        )
        .toList(growable: false);
    return EnvironmentImpact(
      environmentId: environmentId,
      baseRevision: draft.baseRevision,
      draftRevision: draftRevision,
      candidateDigest: draft.candidateDigest,
      classification: classification,
      hotSwitchCount: classification == 'hot_switch' ? affected.length : 0,
      reconnectRequiredCount: classification == 'reconnect_required'
          ? affected.length
          : 0,
      restartRequiredCount: classification == 'restart_required'
          ? affected.length
          : 0,
      affected: affected,
    );
  }

  static bool _sameClientAuthorities(
    List<EnvironmentClientEndpoint> left,
    List<EnvironmentClientEndpoint> right,
  ) {
    String signature(EnvironmentClientEndpoint endpoint) =>
        '${endpoint.id}:${endpoint.clientOrigin}:${endpoint.protocolPlans.map((plan) => '${plan.id}:${plan.clientProtocol}').join(',')}';
    final leftValues = left.map(signature).toList(growable: false)..sort();
    final rightValues = right.map(signature).toList(growable: false)..sort();
    if (leftValues.length != rightValues.length) return false;
    for (var index = 0; index < leftValues.length; index += 1) {
      if (leftValues[index] != rightValues[index]) return false;
    }
    return true;
  }

  static String _environmentDigest(String value) {
    var hash = 0x811c9dc5;
    for (final unit in value.codeUnits) {
      hash = ((hash ^ unit) * 0x01000193) & 0xffffffff;
    }
    final pieces = <String>[];
    for (var index = 0; index < 8; index += 1) {
      hash = ((hash ^ index) * 0x01000193) & 0xffffffff;
      pieces.add(hash.toRadixString(16).padLeft(8, '0'));
    }
    return pieces.join();
  }

  @override
  Future<UpstreamEndpoint> createUpstreamEndpoint({
    required String id,
    required String displayName,
    required String origin,
    required String kind,
  }) async {
    _requireOpen();
    if (_endpoints.any((endpoint) => endpoint.id == id)) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final anthropic = kind == 'anthropic';
    final endpoint = UpstreamEndpoint(
      id: id,
      displayName: displayName,
      origin: Uri.parse(origin),
      realmId: anthropic ? 'anthropic.official' : 'openai.platform',
      backendProtocols: anthropic
          ? const ['anthropic_messages']
          : const ['openai_responses', 'openai_chat'],
      capabilities: const ['messages', 'streaming', 'tool_calls'],
      accountKinds: anthropic
          ? const ['anthropic_api_key', 'claude_oauth_token']
          : const ['openai_api_key'],
      state: 'active',
      revision: 1,
    );
    _endpoints.add(endpoint);
    return endpoint;
  }

  @override
  Future<ProviderAccount> createProviderAccount({
    required String id,
    required String displayName,
    required String upstreamEndpointId,
    required String kind,
    required String secret,
  }) async {
    _requireOpen();
    final endpoint = _endpoints
        .where((candidate) => candidate.id == upstreamEndpointId)
        .firstOrNull;
    if (endpoint == null || !endpoint.accountKinds.contains(kind)) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'upstream_endpoint_not_found',
        messageKey: 'error.upstream_endpoint_not_found',
      );
    }
    if (_accounts.any((account) => account.id == id)) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final account = ProviderAccount(
      id: id,
      displayName: displayName,
      upstreamEndpointId: upstreamEndpointId,
      kind: kind,
      realmId: endpoint.realmId,
      state: 'active',
      revision: 1,
      credentialState: 'ready',
      credentialEpoch: 1,
    );
    _accounts.add(account);
    return account;
  }

  @override
  Future<ProviderAccount> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
  }) async {
    _requireOpen();
    final index = _accounts.indexWhere(
      (candidate) => candidate.id == account.id,
    );
    if (index < 0) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'provider_account_not_found',
        messageKey: 'error.provider_account_not_found',
      );
    }
    final current = _accounts[index];
    if (current.credentialEpoch != account.credentialEpoch) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final updated = ProviderAccount(
      id: current.id,
      displayName: current.displayName,
      upstreamEndpointId: current.upstreamEndpointId,
      kind: current.kind,
      realmId: current.realmId,
      state: current.state,
      revision: current.revision + 1,
      credentialState: 'ready',
      credentialEpoch: current.credentialEpoch + 1,
    );
    _accounts[index] = updated;
    return updated;
  }

  @override
  Future<ProviderAccountDeleteResult> deleteProviderAccount(
    ProviderAccount account,
  ) async {
    _requireOpen();
    final index = _accounts.indexWhere(
      (candidate) => candidate.id == account.id,
    );
    if (index < 0) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'provider_account_not_found',
        messageKey: 'error.provider_account_not_found',
      );
    }
    final current = _accounts[index];
    if (current.credentialEpoch != account.credentialEpoch) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final references = <ProviderAccountReference>[];
    for (final environment in _environments) {
      for (final route in environment.routes) {
        if (route.accountPolicy.candidateAccountIds.contains(account.id)) {
          references.add(
            ProviderAccountReference(
              environmentId: environment.id,
              environmentName: environment.name,
              environmentRevision: environment.revision,
              routeId: route.id,
              routeRevision: 1,
            ),
          );
        }
      }
    }
    if (references.isNotEmpty) {
      return ProviderAccountDeleteResult(
        deleted: false,
        referenceCount: references.length,
        references: references,
      );
    }
    _accounts.removeAt(index);
    return const ProviderAccountDeleteResult(
      deleted: true,
      referenceCount: 0,
      references: [],
    );
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) async {
    _requireOpen();
    final value = _assignments[captureKey];
    if (value == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'capture_assignment_not_found',
        messageKey: 'error.capture_assignment_not_found',
      );
    }
    return value;
  }

  @override
  Future<WorkspaceEnvironmentDefault?> workspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
  }) async {
    _requireOpen();
    return _workspaceDefaults['$machineId:$workspaceId'];
  }

  @override
  Future<WorkspaceEnvironmentDefault> setWorkspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
    required int expectedRevision,
    required String environmentId,
  }) async {
    _requireOpen();
    final environment = _environments
        .where(
          (candidate) =>
              candidate.id == environmentId &&
              candidate.state == 'active' &&
              !candidate.systemOwned,
        )
        .firstOrNull;
    if (environment == null) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'workspace_environment_default_invalid',
        messageKey: 'error.workspace_environment_default_invalid',
      );
    }
    final key = '$machineId:$workspaceId';
    final current = _workspaceDefaults[key];
    if ((current?.revision ?? 0) != expectedRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final updated = WorkspaceEnvironmentDefault(
      machineId: machineId,
      workspaceId: workspaceId,
      environmentId: environment.id,
      environmentName: environment.name,
      revision: expectedRevision + 1,
      updatedAt: _now,
    );
    _workspaceDefaults[key] = updated;
    return updated;
  }

  @override
  Future<void> clearWorkspaceEnvironmentDefault({
    required WorkspaceEnvironmentDefault current,
  }) async {
    _requireOpen();
    final key = '${current.machineId}:${current.workspaceId}';
    final authoritative = _workspaceDefaults[key];
    if (authoritative == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'workspace_environment_default_not_found',
        messageKey: 'error.workspace_environment_default_not_found',
      );
    }
    if (authoritative.revision != current.revision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    _workspaceDefaults.remove(key);
  }

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    _requireOpen();
    if (captureRunId != null && manualCaptureId != null) {
      throw const ControlContractException(
        'Activity query has more than one Capture authority',
      );
    }
    final offset = _previewOffset(cursor, 'activities');
    final values = _allPreviewActivities()
        .where(
          (activity) =>
              (captureRunId == null || activity.captureRunId == captureRunId) &&
              (manualCaptureId == null ||
                  activity.manualCaptureId == manualCaptureId) &&
              (environmentId == null ||
                  activity.environmentId == environmentId) &&
              (conversationId == null ||
                  activity.conversation.id == conversationId),
        )
        .toList(growable: false);
    final end = (offset + limit).clamp(0, values.length).toInt();
    return ActivityPage(
      items: values.sublist(offset, end),
      nextCursor: end < values.length ? 'activities-$end' : null,
    );
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) async {
    _requireOpen();
    if (captureRunId != null && manualCaptureId != null) {
      throw const ControlContractException(
        'Conversation query has more than one Capture authority',
      );
    }
    final offset = _previewOffset(cursor, 'conversations');
    final grouped = <String, List<ActivityRecord>>{};
    for (final activity in _allPreviewActivities()) {
      if (captureRunId != null && activity.captureRunId != captureRunId) {
        continue;
      }
      if (manualCaptureId != null &&
          activity.manualCaptureId != manualCaptureId) {
        continue;
      }
      (grouped[activity.conversation.id] ??= []).add(activity);
    }
    final values =
        grouped.values
            .map((activities) {
              activities.sort(
                (left, right) => left.occurredAt.compareTo(right.occurredAt),
              );
              return ConversationRecord(
                conversation: activities.first.conversation,
                firstObservedAt: activities.first.occurredAt,
                turnCount: activities.length,
                latest: activities.last,
              );
            })
            .toList(growable: false)
          ..sort((left, right) {
            final time = right.firstObservedAt.compareTo(left.firstObservedAt);
            if (time != 0) return time;
            final name = (left.conversation.displayName ?? '')
                .toLowerCase()
                .compareTo(
                  (right.conversation.displayName ?? '').toLowerCase(),
                );
            if (name != 0) return name;
            return left.conversation.id.compareTo(right.conversation.id);
          });
    final end = (offset + limit).clamp(0, values.length).toInt();
    return ConversationPage(
      items: values.sublist(offset, end),
      nextCursor: end < values.length ? 'conversations-$end' : null,
    );
  }

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) async {
    _requireOpen();
    final activity = _allPreviewActivities()
        .where((candidate) => candidate.id == exchangeId)
        .firstOrNull;
    if (activity == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'exchange_not_found',
        messageKey: 'error.exchange_not_found',
      );
    }
    return _previewExchange(activity, contentView);
  }

  @override
  Future<RawEvidencePage> rawEvidence(String exchangeId) async {
    _requireOpen();
    if (!_allPreviewActivities().any((value) => value.id == exchangeId)) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'exchange_not_found',
        messageKey: 'error.exchange_not_found',
      );
    }
    final observed = _now.subtract(const Duration(seconds: 4));
    final body = utf8.encode('{"model":"claude-sonnet-4-5","stream":true}');
    return RawEvidencePage(
      items: [
        RawEvidenceEnvelope(
          envelopeId: 'raw-preview-$exchangeId',
          layer: 'provider_egress',
          scopeKind: 'managed_run',
          scopeId: 'run-preview-$exchangeId',
          exchangeId: exchangeId,
          attemptId: 'attempt-preview-$exchangeId',
          observedAt: observed,
          expiresAt: observed.add(const Duration(days: 30)),
          method: 'POST',
          statusCode: null,
          scheme: 'https',
          authority: 'api.anthropic.com',
          path: '/v1/messages',
          rawQuery: null,
          contentType: 'application/json',
          contentEncoding: null,
          headerCount: 2,
          trailerCount: 0,
          bodyBytes: body.length,
          bodySha256: crypto.sha256.convert(body).toString(),
          digestScope: 'full_body',
          payloadState: 'captured',
          payloadReason: null,
          containsSecret: true,
          revealAvailable: true,
        ),
      ],
      recovery: const RawEvidenceRecovery(
        recoveredUncleanWriters: 0,
        purgedExpiredEnvelopes: 0,
        maximumPossibleLossMs: 0,
      ),
      writer: const RawEvidenceWriter(
        state: 'active',
        admittedRecords: 1,
        durableWatermark: 1,
        queueRecords: 0,
        queueBytes: 0,
        lastFailure: null,
        maximumUnflushedTimeMs: 250,
      ),
    );
  }

  @override
  Future<RevealedRawEvidence> revealRawEvidence({
    required String envelopeId,
  }) async {
    _requireOpen();
    if (!envelopeId.startsWith('raw-preview-')) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_control_request',
        messageKey: 'error.invalid_control_request',
      );
    }
    final exchangeId = envelopeId.substring('raw-preview-'.length);
    final page = await rawEvidence(exchangeId);
    return RevealedRawEvidence(
      envelope: page.items.single,
      headers: const [
        RawHeaderField(name: 'Authorization', values: ['Bearer ••••••••']),
        RawHeaderField(name: 'Content-Type', values: ['application/json']),
      ],
      trailers: const [],
      body: Uint8List.fromList(
        utf8.encode('{"model":"claude-sonnet-4-5","stream":true}'),
      ),
      frames: const [],
    );
  }

  List<ActivityRecord> _allPreviewActivities() {
    final values = _captures.values
        .expand(_previewActivitiesFor)
        .toList(growable: false);
    values.sort((left, right) => right.occurredAt.compareTo(left.occurredAt));
    return values;
  }

  List<ActivityRecord> _previewActivitiesFor(CaptureRecord capture) {
    final assignment = _assignments[capture.key]!;
    final environment = _environments
        .where((candidate) => candidate.id == assignment.environmentId)
        .first;
    final route = environment.routes.firstOrNull;
    final account = route == null
        ? null
        : _accounts
              .where(
                (candidate) =>
                    candidate.id == route.accountPolicy.preferredAccountId,
              )
              .firstOrNull;
    final count = capture.id == 'run-1' ? 224 : 24;
    final values = List.generate(count, (index) {
      final succeeded = index != 5 && index != count - 6;
      final conversation = _previewConversation(capture, index);
      return ActivityRecord(
        id: '${capture.id}-exchange-${index + 1}',
        occurredAt: _now.subtract(Duration(minutes: (count - 1 - index) * 4)),
        title: index % 4 == 0 ? 'Tool-assisted request' : 'Agent exchange',
        status: index == count - 1
            ? 'pending'
            : succeeded
            ? 'succeeded'
            : 'failed',
        reasonCode: succeeded ? null : 'provider_timeout',
        source: ActivitySourceRef(
          kind: capture.isManual ? 'manual_proxy' : 'capture_run',
          displayName: capture.displayName,
          recognition: capture.managedRun?.recognition ?? 'configured',
        ),
        conversation: conversation,
        environment: FrozenEnvironmentRef(
          id: environment.id,
          revision: environment.revision,
          digest: environment.digest,
          clientEndpointId:
              environment.clientEndpoints.firstOrNull?.id ?? 'transparent',
          clientEndpointRevision: 1,
          protocolPlanId:
              environment
                  .clientEndpoints
                  .firstOrNull
                  ?.protocolPlans
                  .firstOrNull
                  ?.id ??
              'transparent',
          protocolPlanRevision: 1,
          routeId: route?.id ?? 'client-passthrough',
          routeRevision: 1,
          accountId: account?.id,
          accountRevision: account?.revision,
          credentialEpoch: account?.credentialEpoch,
        ),
        parentRefs: ActivityParentRefs(
          exchangeId: '${capture.id}-exchange-${index + 1}',
          captureRunId: capture.captureRunId,
          manualCaptureId: capture.isManual ? capture.id : null,
          connectionId: 'connection-${index % 18 + 1}',
        ),
      );
    });
    values.sort((left, right) => right.occurredAt.compareTo(left.occurredAt));
    return values;
  }

  ActivityConversationRef _previewConversation(
    CaptureRecord capture,
    int index,
  ) {
    final exchangeId = '${capture.id}-exchange-${index + 1}';
    if (capture.isManual) {
      return ActivityConversationRef(
        id: 'exchange:$exchangeId',
        displayName: null,
        kind: 'isolated_exchange',
        evidence: 'exchange_boundary',
        actor: null,
      );
    }
    if (capture.id == 'run-1' && index % 17 == 5) {
      return ActivityConversationRef(
        id: 'exchange:$exchangeId',
        displayName: null,
        kind: 'isolated_subagent',
        evidence: 'client_asserted_subagent',
        actor: null,
      );
    }
    if (capture.id == 'run-1' && index % 13 == 3) {
      return ActivityConversationRef(
        id: 'capture_run:${capture.captureRunId}:agent:reviewer',
        displayName: 'reviewer',
        kind: 'agent',
        evidence: 'explicit_actor',
        actor: '/root/reviewer',
        clientIdentity: _previewClientIdentity(
          capture,
          index,
          actorId: '/root/reviewer',
          actorLabel: 'reviewer',
        ),
      );
    }
    return ActivityConversationRef(
      id: 'capture_run:${capture.captureRunId}:main',
      displayName: capture.displayName,
      kind: 'main',
      evidence: 'capture_run',
      actor: null,
      clientIdentity: _previewClientIdentity(capture, index),
    );
  }

  AgentClientIdentity _previewClientIdentity(
    CaptureRecord capture,
    int index, {
    String? actorId,
    String? actorLabel,
  }) {
    final client = capture.managedRun?.executableLabel == 'codex'
        ? 'codex'
        : 'claude';
    final sessionId = '$client-session-${capture.id}';
    final responseId = 'response-${capture.id}-exchange-${index + 1}';
    final exchangeCount = capture.id == 'run-1' ? 224 : 24;
    final observedAt = _now.subtract(
      Duration(minutes: (exchangeCount - 1 - index) * 4),
    );
    if (client == 'codex') {
      final threadId = actorId ?? 'codex-thread-${capture.id}';
      return AgentClientIdentity(
        client: client,
        sessionId: sessionId,
        sessionResumable: true,
        actorId: actorId == null ? null : threadId,
        actorLabel: actorLabel,
        actorType: actorId == null ? null : 'reviewer',
        actorIsSubagent: actorId != null,
        providerResponseId: responseId,
        providerMessageId: null,
        source: 'client_local_state',
        confidence: 'exact',
        observedAt: observedAt,
        protocolIds: [
          AgentClientEvidenceValue(
            name: 'codex.call_id',
            value: 'call-${capture.id}-${index + 1}',
          ),
          AgentClientEvidenceValue(
            name: 'codex.response_item_id',
            value: 'item-${capture.id}-${index + 1}',
          ),
          AgentClientEvidenceValue(name: 'codex.session_id', value: sessionId),
          AgentClientEvidenceValue(name: 'codex.thread_id', value: threadId),
          AgentClientEvidenceValue(
            name: 'codex.turn_id',
            value: 'turn-${capture.id}-${index + 1}',
          ),
        ],
        attributes: const [
          AgentClientEvidenceValue(
            name: 'codex.cli_version',
            value: '0.101.0-preview',
          ),
          AgentClientEvidenceValue(
            name: 'codex.originator',
            value: 'codex_cli_rs',
          ),
        ],
      );
    }
    return AgentClientIdentity(
      client: client,
      sessionId: sessionId,
      sessionResumable: true,
      actorId: actorId,
      actorLabel: actorLabel,
      actorType: actorId == null ? null : 'reviewer',
      actorIsSubagent: actorId != null,
      providerResponseId: responseId,
      providerMessageId: responseId,
      source: 'client_local_state',
      confidence: 'exact',
      observedAt: observedAt,
      protocolIds: [
        AgentClientEvidenceValue(
          name: 'claude.event_uuid',
          value: 'event-${capture.id}-${index + 1}',
        ),
        AgentClientEvidenceValue(
          name: 'claude.request_id',
          value: 'request-${capture.id}-${index + 1}',
        ),
      ],
      attributes: const [
        AgentClientEvidenceValue(name: 'claude.skill', value: 'code-review'),
      ],
    );
  }

  @override
  Future<List<ApprovalRecord>> pendingApprovals() async {
    _requireOpen();
    return _approval.state == 'pending' ? [_approval] : const [];
  }

  @override
  Future<NetworkData> loadNetwork() async {
    _requireOpen();
    return NetworkData(
      approvals: await pendingApprovals(),
      connections: await connections(limit: 10),
      egressAttempts: await egressAttempts(limit: 10),
      rules: _rules,
    );
  }

  @override
  Future<ConnectionPage> connections({String? cursor, int limit = 50}) async {
    _requireOpen();
    final offset = _previewOffset(cursor, 'connections');
    final end = (offset + limit).clamp(0, _connectionEvidence.length).toInt();
    return ConnectionPage(
      items: _connectionEvidence.sublist(offset, end),
      nextCursor: end < _connectionEvidence.length ? 'connections-$end' : null,
    );
  }

  @override
  Future<EgressAttemptPage> egressAttempts({
    String? cursor,
    int limit = 50,
  }) async {
    _requireOpen();
    final offset = _previewOffset(cursor, 'egress');
    final end = (offset + limit).clamp(0, _egressEvidence.length).toInt();
    return EgressAttemptPage(
      items: _egressEvidence.sublist(offset, end),
      nextCursor: end < _egressEvidence.length ? 'egress-$end' : null,
    );
  }

  @override
  Future<ApprovalRecord> decideApproval({
    required ApprovalRecord approval,
    required ApprovalChoice choice,
  }) async {
    _requireOpen();
    if (_approval.state != 'pending' ||
        _approval.id != approval.id ||
        _approval.revision != approval.revision ||
        !approval.choices.any(
          (candidate) =>
              candidate.decision == choice.decision &&
              candidate.scope == choice.scope,
        )) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    _approval = ApprovalRecord(
      id: approval.id,
      revision: approval.revision + 1,
      kind: approval.kind,
      state: choice.decision == 'deny' ? 'denied' : 'allowed',
      risk: approval.risk,
      titleKey: approval.titleKey,
      summaryKey: approval.summaryKey,
      aggregateKey: approval.aggregateKey,
      exchangeId: approval.exchangeId,
      environmentId: approval.environmentId,
      environmentRevision: approval.environmentRevision,
      environmentDigest: approval.environmentDigest,
      routeId: approval.routeId,
      routeRevision: approval.routeRevision,
      target: approval.target,
      subjectRefs: approval.subjectRefs,
      subjectLabels: approval.subjectLabels,
      requestCount: approval.requestCount,
      waiterCount: approval.waiterCount,
      choices: approval.choices,
      createdAt: approval.createdAt,
      expiresAt: approval.expiresAt,
      resolvedAt: _now,
      decision: choice.decision,
      decisionScope: choice.scope,
      terminalReason: choice.decision == 'deny' ? 'user_denied' : null,
    );
    return _approval;
  }

  @override
  Future<ConnectionRuleSet> replaceConnectionRules({
    required ConnectionRuleSet current,
    required List<ConnectionRule> rules,
    required String mode,
  }) async {
    _requireOpen();
    if (current.revision != _rules.revision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    _rules = ConnectionRuleSet(
      revision: current.revision + 1,
      rules: List.unmodifiable(rules),
      mode: mode,
    );
    return _rules;
  }

  @override
  Future<CaptureAssignmentChange> switchCaptureEnvironment({
    required CaptureAssignment assignment,
    required String environmentId,
  }) async {
    _requireOpen();
    if (!_environments.any((environment) => environment.id == environmentId)) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'environment_not_found',
        messageKey: 'error.environment_not_found',
      );
    }
    final current = await captureAssignment(assignment.captureKey);
    if (current.revision != assignment.revision) {
      throw const ControlProblem(
        status: 412,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    if (current.environmentId == environmentId) {
      return CaptureAssignmentChange(
        assignment: current,
        boundary: 'no_change',
        closedConnections: const [],
        applied: true,
      );
    }
    final updated = CaptureAssignment(
      captureKey: current.captureKey,
      captureId: current.captureId,
      captureKind: current.captureKind,
      environmentId: environmentId,
      revision: current.revision + 1,
      source: 'operator_switch',
      updatedAt: _now,
    );
    _assignments[current.captureKey] = updated;
    return CaptureAssignmentChange(
      assignment: updated,
      boundary: captureIsManual(current.captureKey)
          ? 'reconnect_required'
          : 'hot_switch',
      closedConnections: const [],
      applied: true,
    );
  }

  bool captureIsManual(String captureKey) =>
      captureKey.startsWith('manual_capture:');

  @override
  Future<ManualCaptureStateTag> manualCaptureState(
    String manualCaptureId,
  ) async {
    _requireOpen();
    final capture = _manualRecords[manualCaptureId];
    if (capture == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'manual_capture_not_found',
        messageKey: 'error.manual_capture_not_found',
      );
    }
    return ManualCaptureStateTag(
      capture: capture,
      stateTag: _tagFor(manualCaptureId),
    );
  }

  @override
  Future<ManualCaptureContext> manualCaptureContext(
    String environmentId,
  ) async {
    _requireOpen();
    final environment = _environments
        .where((candidate) => candidate.id == environmentId)
        .firstOrNull;
    if (environment == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'environment_not_found',
        messageKey: 'error.environment_not_found',
      );
    }
    return _manualContextFor(environment);
  }

  @override
  Future<ManualCaptureGrantStateTag> createManualCapture({
    required ManualCaptureContext context,
    required String displayName,
    required String clientClass,
    required String lifetime,
    int? expiresInSeconds,
  }) async {
    _requireOpen();
    final environment = _environments
        .where((candidate) => candidate.id == context.environmentId)
        .firstOrNull;
    final reviewed = environment == null
        ? null
        : _manualContextFor(environment);
    if (reviewed == null ||
        context.confirmationToken != reviewed.confirmationToken ||
        context.proxyAddress != reviewed.proxyAddress ||
        context.environmentRevision != reviewed.environmentRevision ||
        context.environmentDigest != reviewed.environmentDigest ||
        context.launchAuthorityDigest != reviewed.launchAuthorityDigest) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'manual_capture_context_stale',
        messageKey: 'error.manual_capture_context_stale',
      );
    }
    final id = 'manual-preview-${_manualRecords.length + 1}';
    final record = ManualCaptureRecord(
      id: id,
      displayName: displayName,
      clientClass: clientClass,
      lifetime: lifetime,
      state: 'active',
      observation: 'waiting_for_traffic',
      createdAt: _now,
      updatedAt: _now,
      expiresAt: lifetime == 'temporary'
          ? _now.add(Duration(seconds: expiresInSeconds!))
          : null,
      lastObservedAt: null,
    );
    _manualRecords[id] = record;
    _manualVersions[id] = 1;
    _captures['manual_capture:$id'] = CaptureRecord(
      key: 'manual_capture:$id',
      id: id,
      kind: 'manual_capture',
      displayName: displayName,
      state: 'active',
      observation: 'waiting_for_traffic',
      createdAt: _now,
      updatedAt: _now,
      manualCapture: ManualCaptureSummary(
        clientClass: clientClass,
        lifetime: lifetime,
        credentialRevision: 1,
        expiresAt: record.expiresAt,
      ),
    );
    _assignments['manual_capture:$id'] = CaptureAssignment(
      captureKey: 'manual_capture:$id',
      captureId: id,
      captureKind: 'manual_capture',
      environmentId: context.environmentId,
      revision: 1,
      source: 'manual_create',
      updatedAt: _now,
    );
    return ManualCaptureGrantStateTag(
      grant: _manualGrant(record, context.environmentId),
      stateTag: _tagFor(id),
    );
  }

  @override
  Future<ManualCaptureGrantStateTag> rotateManualCapture(
    ManualCaptureStateTag current,
  ) async {
    _requireOpen();
    final id = current.capture.id;
    final record = _manualRecords[id];
    if (record == null ||
        record.state != 'active' ||
        current.stateTag != _tagFor(id)) {
      throw const ControlProblem(
        status: 412,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    _manualVersions[id] = _manualVersions[id]! + 1;
    final updatedRecord = ManualCaptureRecord(
      id: record.id,
      displayName: record.displayName,
      clientClass: record.clientClass,
      lifetime: record.lifetime,
      state: record.state,
      observation: record.observation,
      createdAt: record.createdAt,
      updatedAt: _now,
      expiresAt: record.expiresAt,
      lastObservedAt: record.lastObservedAt,
    );
    _manualRecords[id] = updatedRecord;
    final aggregate = _captures['manual_capture:$id']!;
    _captures['manual_capture:$id'] = CaptureRecord(
      key: aggregate.key,
      id: aggregate.id,
      kind: aggregate.kind,
      displayName: aggregate.displayName,
      state: aggregate.state,
      observation: aggregate.observation,
      createdAt: aggregate.createdAt,
      updatedAt: _now,
      manualCapture: ManualCaptureSummary(
        clientClass: aggregate.manualCapture!.clientClass,
        lifetime: aggregate.manualCapture!.lifetime,
        credentialRevision: _manualVersions[id]!,
        expiresAt: aggregate.manualCapture!.expiresAt,
        lastObservedAt: aggregate.manualCapture!.lastObservedAt,
      ),
    );
    final environmentId = _assignments['manual_capture:$id']!.environmentId;
    return ManualCaptureGrantStateTag(
      grant: _manualGrant(updatedRecord, environmentId),
      stateTag: _tagFor(id),
    );
  }

  @override
  Future<void> revokeManualCapture({
    required String manualCaptureId,
    required String stateTag,
  }) async {
    _requireOpen();
    if (!_manualVersions.containsKey(manualCaptureId) ||
        stateTag != _tagFor(manualCaptureId)) {
      throw const ControlProblem(
        status: 412,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final key = 'manual_capture:$manualCaptureId';
    final capture = _captures[key];
    if (capture == null) return;
    final record = _manualRecords[manualCaptureId]!;
    _manualRecords[manualCaptureId] = ManualCaptureRecord(
      id: record.id,
      displayName: record.displayName,
      clientClass: record.clientClass,
      lifetime: record.lifetime,
      state: 'revoked',
      observation: record.observation,
      createdAt: record.createdAt,
      updatedAt: _now,
      expiresAt: record.expiresAt,
      lastObservedAt: record.lastObservedAt,
    );
    _captures[key] = CaptureRecord(
      key: capture.key,
      id: capture.id,
      kind: capture.kind,
      displayName: capture.displayName,
      state: 'revoked',
      observation: capture.observation,
      createdAt: capture.createdAt,
      updatedAt: _now,
      manualCapture: capture.manualCapture,
    );
  }

  ExchangeDetail _previewExchange(ActivityRecord activity, String view) {
    final match = RegExp(r'-exchange-(\d+)$').firstMatch(activity.id);
    final index = (int.tryParse(match?.group(1) ?? '1') ?? 1) - 1;
    final checkpoint = index == 0;
    final inherited = checkpoint ? 0 : 2;
    final allMessages = <ExchangeContentMessage>[
      ExchangeContentMessage(
        role: 'system',
        agent: null,
        blocks: [
          _previewTextBlock(
            'You are operating inside ${activity.environment.id}.',
          ),
        ],
      ),
      if (!checkpoint)
        ExchangeContentMessage(
          role: 'user',
          agent: null,
          blocks: [
            _previewTextBlock(
              'Inspect the current workspace.\n\n'
              '```text\n'
              'WRAPPING-CHECK: this intentionally long diagnostic line must wrap inside the message column instead of creating a horizontal scrollbar that hides the remaining evidence.\n'
              '```\n\n'
              'hello',
            ),
          ],
        ),
      ExchangeContentMessage(
        role: 'user',
        agent: null,
        blocks: [
          _previewTextBlock(
            index % 4 == 0
                ? 'Read the project notes and summarize the next action.'
                : 'Continue with the next verified implementation step.',
          ),
        ],
      ),
    ];
    final visibleMessages = view == 'full' || checkpoint
        ? allMessages
        : allMessages.skip(inherited).toList(growable: false);
    final toolTurn = index % 4 == 0;
    final terminal = activity.status != 'pending';
    final failed = activity.status == 'failed';
    final agentTurn = activity.source.displayName == 'Codex' && index >= 20;
    final attempt = EgressAttemptRecord(
      sequence: 1,
      id: 'egress-${activity.id}',
      connectionId: activity.parentRefs.connectionId,
      purpose: 'provider_attempt',
      payloadClass: 'client_semantic',
      parentKind: 'upstream_attempt',
      parentId: 'attempt-${activity.id}',
      exchangeId: activity.id,
      caller: 'core',
      callerId: null,
      targetOrigin: activity.routeId.contains('openai')
          ? 'https://api.openai.com'
          : activity.routeId.contains('orbit')
          ? 'https://tokyo.orbitrelay.example'
          : 'https://api.anthropic.com',
      authority: 'environment',
      policyId: 'egress.${activity.environmentId}',
      ruleId: null,
      proxyId: null,
      reusedTransport: index.isEven,
      startedAt: activity.occurredAt,
      terminal: terminal,
      outcome: terminal ? (failed ? 'failed' : 'completed') : null,
      errorClass: failed ? 'provider_timeout' : null,
      bytesOut: 420 + index * 7,
      bytesIn: failed ? 0 : 1280 + index * 17,
      completedAt: terminal
          ? activity.occurredAt.add(const Duration(milliseconds: 840))
          : null,
    );
    final response = terminal && !failed
        ? ExchangeResponse(
            id: 'response-${activity.id}',
            requestedModel: 'claude-sonnet-4-5',
            effectiveModel: 'claude-sonnet-4-5',
            reportedModel: 'claude-sonnet-4-5-20250929',
            stopReason: toolTurn ? 'tool_use' : 'end_turn',
            blocks: [
              if (toolTurn)
                ExchangeContentBlock(
                  kind: 'tool_call',
                  availability: 'recorded',
                  text: null,
                  originalSize: 25,
                  callId: 'call-read-notes',
                  toolName: 'Read',
                  toolNamespace: null,
                  arguments: {'path': 'README.md'},
                  toolError: false,
                  providerSource: null,
                  providerKind: null,
                  fingerprint: null,
                  agent: agentTurn
                      ? const ExchangeAgentContext(
                          agentName: 'root',
                          author: null,
                          recipient: null,
                        )
                      : null,
                )
              else
                _previewTextBlock(
                  index == 219
                      ? '### Long captured response\n\n'
                            '- Preserve **frozen evidence**.\n'
                            '- Verify `stateTag` before mutation.\n'
                            '- Keep the exact client session ID.\n'
                            '- Keep the exact Agent ID.\n'
                            '- Keep the exact parent Agent ID.\n'
                            '- Preserve request headers.\n'
                            '- Preserve request bytes.\n'
                            '- Preserve response headers.\n'
                            '- Preserve response bytes.\n'
                            '- Record provider attempts.\n'
                            '- Record tool calls.\n'
                            '- Record usage.\n'
                            '- Record thinking summaries.\n'
                            '- Record signature metadata.\n'
                            '- Keep failures non-destructive.\n'
                            '- Flush admitted evidence before shutdown.'
                      : index == 221
                      ? '### Verified result\n\nThe runtime evidence is consistent; continue with the next bounded change.\n\n- Preserve **frozen evidence**.\n- Verify `stateTag` before mutation.'
                      : 'The runtime evidence is consistent; continue with the next bounded change.',
                ),
            ],
            usage: const ExchangeUsage(
              inputUncached: ExchangeUsageValue(
                known: true,
                tokens: 1240,
                source: 'provider',
              ),
              cacheWrite: ExchangeUsageValue(
                known: true,
                tokens: 0,
                source: 'provider',
              ),
              cacheRead: ExchangeUsageValue(
                known: true,
                tokens: 384,
                source: 'provider',
              ),
              output: ExchangeUsageValue(
                known: true,
                tokens: 96,
                source: 'provider',
              ),
              reasoning: ExchangeUsageValue(
                known: false,
                tokens: null,
                source: null,
              ),
            ),
          )
        : null;
    return ExchangeDetail(
      id: activity.id,
      status: activity.status,
      environment: activity.environment,
      parentRefs: activity.parentRefs,
      diagnosis: failed
          ? const ExchangeDiagnosis(
              providerStatus: 504,
              providerField: 'upstream',
              clientField: null,
              clientPath: null,
            )
          : null,
      clientIdentity: activity.conversation.clientIdentity,
      processingTrace: ExchangeProcessingTrace(
        egressProxyId: null,
        pluginRunIds: const [],
        attempts: [attempt],
        result: activity.reasonCode ?? activity.status,
      ),
      content: ExchangeContentDetail(
        state: 'recorded',
        mode: 'full',
        recordedAt: activity.occurredAt,
        expiresAt: activity.occurredAt.add(const Duration(days: 30)),
        requestProjection: ExchangeRequestProjection(
          view: view,
          relationship: checkpoint ? 'checkpoint' : 'incremental',
          inheritedMessageCount: inherited,
          totalMessageCount: allMessages.length,
          fullSnapshotAvailable: inherited > 0,
        ),
        agentConversation: agentTurn
            ? const AgentConversationProjection(
                scope: 'capture_run',
                agents: [
                  AgentConversationAgent(name: 'root'),
                  AgentConversationAgent(name: 'reviewer'),
                ],
                relationships: [
                  AgentConversationRelationship(
                    source: 'root',
                    target: 'reviewer',
                    kind: 'message',
                  ),
                ],
                actions: [
                  AgentConversationAction(
                    callId: 'preview-agent-call',
                    name: 'spawn_agent',
                    status: 'completed',
                    sourceAgent: 'root',
                    resultAgent: 'reviewer',
                    attributed: true,
                  ),
                ],
              )
            : null,
        request: ExchangeRequest(
          requestedModel: 'claude-sonnet-4-5',
          effectiveModel: 'claude-sonnet-4-5',
          maxOutputTokens: 4096,
          stream: true,
          messages: visibleMessages,
          tools: toolTurn
              ? const [ExchangeToolDefinition(name: 'Read', namespace: null)]
              : const [],
        ),
        response: response,
      ),
    );
  }

  static ExchangeContentBlock _previewTextBlock(String text) =>
      ExchangeContentBlock(
        kind: 'text',
        availability: 'recorded',
        text: text,
        originalSize: text.length,
        callId: null,
        toolName: null,
        toolNamespace: null,
        arguments: null,
        toolError: false,
        providerSource: null,
        providerKind: null,
        fingerprint: null,
        agent: null,
      );

  ManualCaptureGrant _manualGrant(
    ManualCaptureRecord record,
    String environmentId,
  ) {
    final environment = _environments
        .where((candidate) => candidate.id == environmentId)
        .first;
    final context = _manualContextFor(environment);
    return ManualCaptureGrant(
      capture: record,
      proxyAddress: context.proxyAddress,
      proxyUsername: 'capture:${record.id}',
      proxyPassword: 'preview-password-${_manualVersions[record.id]}',
      environmentId: environmentId,
      assignmentRevision: _assignments['manual_capture:${record.id}']!.revision,
      launchAuthorityDigest: context.launchAuthorityDigest,
      protectedAuthorities: context.protectedAuthorities,
      managedCredentialAuthorities: context.managedCredentialAuthorities,
      root: context.root,
    );
  }

  ManualCaptureContext _manualContextFor(EnvironmentRecord environment) {
    final protected =
        environment.clientEndpoints
            .map((endpoint) => endpoint.clientOrigin.host)
            .toSet()
            .toList(growable: false)
          ..sort();
    final managed =
        environment.routes
            .map((route) => route.endpointOrigin.host)
            .toSet()
            .toList(growable: false)
          ..sort();
    return ManualCaptureContext(
      confirmationToken: _contextToken,
      proxyAddress: 'http://127.0.0.1:43123',
      environmentId: environment.id,
      environmentRevision: environment.revision,
      environmentDigest: environment.digest,
      launchAuthorityDigest: _previewDigest,
      protectedAuthorities: protected,
      managedCredentialAuthorities: managed,
      defaultTemporarySeconds: 3600,
      maxTemporarySeconds: 86400,
      root: protected.isEmpty
          ? null
          : const ManualCaptureRoot(
              derSha256:
                  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
              fingerprint: 'BB:BB:BB:BB:BB:BB',
              pemPath:
                  '/Users/mira/Library/Application Support/ViberMate/root.pem',
            ),
    );
  }

  String _tagFor(String manualCaptureId) {
    final version = _manualVersions[manualCaptureId]!;
    final payload = version.toRadixString(36).padLeft(43, 'A');
    return '"mc_$payload"';
  }

  void _requireOpen() {
    if (_closed) throw StateError('Preview control is closed');
  }

  int _previewOffset(String? cursor, String prefix) {
    if (cursor == null) return 0;
    final parts = cursor.split('-');
    final offset = parts.length == 2 && parts.first == prefix
        ? int.tryParse(parts.last)
        : null;
    if (offset == null || offset < 0) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_request',
        messageKey: 'error.invalid_request',
      );
    }
    return offset;
  }

  @override
  Future<void> close() async {
    _closed = true;
  }
}

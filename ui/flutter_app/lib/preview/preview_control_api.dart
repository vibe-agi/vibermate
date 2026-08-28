import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;

import '../core/api/control_api.dart';
import '../core/api/control_models.dart';

final class PreviewControlApi implements ControlApi {
  PreviewControlApi({
    int dashboardCaptureLimit = 50,
    ControlProblem? upstreamModelFailure,
  }) : _dashboardCaptureLimit = dashboardCaptureLimit,
       _upstreamModelFailure = upstreamModelFailure {
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
      final assignedEnvironment = _environments.firstWhere(
        (value) => value.id == (index < 5 ? 'work' : 'research'),
      );
      _assignments[capture.key] = CaptureAssignment(
        captureKey: capture.key,
        captureId: capture.id,
        captureKind: capture.kind,
        environmentId: assignedEnvironment.id,
        environmentRevision: assignedEnvironment.revision,
        environmentDigest: assignedEnvironment.digest,
        launchEnvironmentRevision: assignedEnvironment.revision,
        launchEnvironmentDigest: assignedEnvironment.digest,
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
    final manualEnvironment = _environments.firstWhere(
      (value) => value.id == 'work',
    );
    _assignments[manual.key] = CaptureAssignment(
      captureKey: manual.key,
      captureId: manual.id,
      captureKind: manual.kind,
      environmentId: 'work',
      environmentRevision: manualEnvironment.revision,
      environmentDigest: manualEnvironment.digest,
      launchEnvironmentRevision: manualEnvironment.revision,
      launchEnvironmentDigest: manualEnvironment.digest,
      revision: 1,
      source: 'manual_create',
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
  final ControlProblem? _upstreamModelFailure;

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
  final Map<String, ManualCaptureRecord> _manualRecords = {};
  final Map<String, int> _manualVersions = {};
  final List<CodeLibraryCollection> _codeLibraryCollections = [];
  final Map<String, List<CodeLibraryTransformRevision>> _codeLibraryRevisions =
      {};
  final Map<String, List<EgressProfileRevision>> _egressProfileRevisions = {
    EgressProfileRevision.direct.id: [EgressProfileRevision.direct],
  };
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
  final RuntimeServerAccess _serverAccess = const RuntimeServerAccess(
    transport: 'http',
    authentication: 'runtime_user_password',
    sessionPolicy: 'reusable_until_logout_disable_or_expiry',
    targets: ['192.168.1.44:9666'],
  );
  final List<RuntimeUser> _runtimeUsers = [
    RuntimeUser(
      id: 'user.preview.alice',
      username: 'alice',
      state: 'active',
      createdAt: DateTime.utc(2026, 8, 24, 9),
      updatedAt: DateTime.utc(2026, 8, 24, 9),
    ),
  ];
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
      accountKinds: const ['anthropic_api_key', 'bearer_token'],
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
      accountKinds: const ['bearer_token'],
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
      accountKinds: const ['anthropic_api_key', 'bearer_token'],
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
      setHeaderNames: const [],
      deleteHeaderNames: const [],
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
      setHeaderNames: const [],
      deleteHeaderNames: const [],
    ),
    ProviderAccount(
      id: 'openai-work',
      displayName: 'OpenAI · Work',
      upstreamEndpointId: 'target.openai.official',
      kind: 'bearer_token',
      realmId: 'openai.platform',
      state: 'active',
      revision: 1,
      credentialState: 'ready',
      credentialEpoch: 2,
      setHeaderNames: const [],
      deleteHeaderNames: const [],
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
      setHeaderNames: const ['X-Team'],
      deleteHeaderNames: const ['X-Legacy'],
    ),
  ];

  late final List<EnvironmentRecord> _environments;
  final Map<String, EnvironmentRecord> _environmentHistory = {};
  final Map<String, EnvironmentDraft> _environmentDrafts = {};
  final Map<String, int> _environmentDraftCounters = {};

  List<EnvironmentRecord> _initialEnvironments() => [
    _environment(
      id: 'system_transparent',
      name: 'System capture',
      revision: 1,
      digestCharacter: '1',
      systemOwned: true,
      endpoints: [
        _originalClientEndpoint(
          id: 'system-anthropic',
          origin: 'https://api.anthropic.com',
          protocol: 'anthropic_messages',
        ),
        _originalClientEndpoint(
          id: 'system-openai',
          origin: 'https://api.openai.com',
          protocol: 'openai_responses',
        ),
        _originalClientEndpoint(
          id: 'system-chatgpt',
          origin: 'https://chatgpt.com',
          protocol: 'openai_responses',
        ),
      ],
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
    contentRecording: const EnvironmentContentRecordingPolicy(
      mode: 'full',
      retentionDays: 30,
    ),
    launchEnvironment: const EnvironmentLaunchPolicy.empty(),
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
        destination: EnvironmentDestination.upstream(
          EnvironmentUpstreamPlan(
            defaultRouteId: defaultRouteId,
            routeSet: EnvironmentRouteSet(
              id: '$id-routes',
              revision: 1,
              candidateRouteIds: routes
                  .map((route) => route.id)
                  .toList(growable: false),
            ),
            routes: routes,
          ),
        ),
        egressProfile: EgressProfileRevision.direct,
        transforms: const [],
        pluginBindings: const [],
      ),
    ],
  );

  static EnvironmentClientEndpoint _originalClientEndpoint({
    required String id,
    required String origin,
    required String protocol,
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
        destination: const EnvironmentDestination.original(),
        egressProfile: EgressProfileRevision.direct,
        transforms: const [],
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
        preferredAccountId: accountIds.first,
        candidateAccountIds: accountIds,
        accountRevisions: accountRevisions,
        failoverPolicy: accountIds.length > 1 ? 'account_scoped_safe' : 'off',
      ),
      modelPolicy: const EnvironmentModelPolicy(
        revision: 1,
        mode: 'passthrough',
        mappings: [],
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
      contentRecording: input.contentRecording,
      launchEnvironment: input.launchEnvironment,
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
  Future<MessageTransformTestResult> testMessageTransform({
    required String wireProtocol,
    required TrafficTransformPolicy policy,
    MessageTransformTestSample? sample,
  }) async {
    _requireOpen();
    throw const ControlProblem(
      status: 501,
      reasonCode: 'message_transform_test_unavailable',
      messageKey: 'error.message_transform_test_unavailable',
    );
  }

  @override
  Future<CodeLibraryCatalog> codeLibrary() async {
    _requireOpen();
    return CodeLibraryCatalog(
      collections: List.unmodifiable(_codeLibraryCollections),
      transforms: List.unmodifiable(
        _codeLibraryRevisions.values
            .where((revisions) => revisions.isNotEmpty)
            .map((revisions) => revisions.last),
      ),
    );
  }

  @override
  Future<CodeLibraryCollection> createCodeLibraryCollection({
    required String id,
    required String displayName,
  }) async {
    _requireOpen();
    final collection = CodeLibraryCollection.fromJson({
      'id': id,
      'displayName': displayName,
    }, 'codeLibraryCollection');
    if (_codeLibraryCollections.any((item) => item.id == collection.id)) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'code_library_conflict',
        messageKey: 'error.code_library_conflict',
      );
    }
    _codeLibraryCollections.add(collection);
    return collection;
  }

  @override
  Future<CodeLibraryTransformRevision> publishCodeLibraryTransform({
    required String id,
    required int expectedRevision,
    required String collectionId,
    required String displayName,
    required TrafficTransformPolicy policy,
  }) async {
    _requireOpen();
    final revisions =
        _codeLibraryRevisions[id] ?? const <CodeLibraryTransformRevision>[];
    final currentRevision = revisions.lastOrNull?.revision ?? 0;
    if (currentRevision != expectedRevision ||
        !_codeLibraryCollections.any((item) => item.id == collectionId)) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'code_library_conflict',
        messageKey: 'error.code_library_conflict',
      );
    }
    final revision = CodeLibraryTransformRevision.fromJson({
      'id': id,
      'revision': currentRevision + 1,
      'collectionId': collectionId,
      'displayName': displayName,
      'policy': policy.toJson(),
      'publishedAt': _now
          .add(Duration(seconds: currentRevision + 1))
          .toIso8601String(),
    }, 'codeLibraryTransform');
    _codeLibraryRevisions[id] = [...revisions, revision];
    return revision;
  }

  @override
  Future<CodeLibraryTransformRevision> codeLibraryTransformRevision(
    String id,
    int revision,
  ) async {
    _requireOpen();
    final match = _codeLibraryRevisions[id]
        ?.where((item) => item.revision == revision)
        .firstOrNull;
    if (match == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'code_library_not_found',
        messageKey: 'error.code_library_not_found',
      );
    }
    return match;
  }

  @override
  Future<EgressProfileCatalog> egressProfiles() async {
    _requireOpen();
    return EgressProfileCatalog(
      items: List.unmodifiable(
        _egressProfileRevisions.values
            .where((revisions) => revisions.isNotEmpty)
            .map((revisions) => revisions.last),
      ),
    );
  }

  @override
  Future<EgressProfileRevision> publishEgressProfile({
    required String id,
    required int expectedRevision,
    required String displayName,
    required TrafficEgressPolicy policy,
  }) async {
    _requireOpen();
    final revisions =
        _egressProfileRevisions[id] ?? const <EgressProfileRevision>[];
    final currentRevision = revisions.lastOrNull?.revision ?? 0;
    if (id == EgressProfileRevision.direct.id ||
        currentRevision != expectedRevision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'egress_profile_conflict',
        messageKey: 'error.egress_profile_conflict',
      );
    }
    final revision = EgressProfileRevision.fromJson({
      'id': id,
      'revision': currentRevision + 1,
      'displayName': displayName,
      'policy': policy.toJson(),
      'publishedAt': _now
          .add(Duration(seconds: currentRevision + 1))
          .toIso8601String(),
    }, 'egressProfile');
    _egressProfileRevisions[id] = [...revisions, revision];
    return revision;
  }

  @override
  Future<EgressProfileRevision> egressProfileRevision(
    String id,
    int revision,
  ) async {
    _requireOpen();
    final match = _egressProfileRevisions[id]
        ?.where((item) => item.revision == revision)
        .firstOrNull;
    if (match == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'egress_profile_not_found',
        messageKey: 'error.egress_profile_not_found',
      );
    }
    return match;
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
    final continuing = _captures.values
        .where(
          (capture) =>
              capture.running &&
              _assignments[capture.key]?.environmentId == environmentId,
        )
        .map(
          (capture) => EnvironmentImpactCapture(
            captureKind: capture.kind,
            captureId: capture.id,
          ),
        )
        .toList(growable: false);
    return EnvironmentImpact(
      environmentId: environmentId,
      baseRevision: draft.baseRevision,
      draftRevision: draftRevision,
      candidateDigest: draft.candidateDigest,
      continuingCaptures: continuing,
    );
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
  Future<ClientModelCatalog> clientModels(String protocol) async {
    _requireOpen();
    final providerId = switch (protocol) {
      'anthropic_messages' => 'anthropic',
      'openai_responses' || 'openai_chat' => 'openai',
      _ => throw const ControlProblem(
        status: 422,
        reasonCode: 'client_protocol_unsupported',
        messageKey: 'error.client_protocol_unsupported',
      ),
    };
    final models = providerId == 'anthropic'
        ? const [
            ClientModel(
              id: 'claude-sonnet-4-5',
              canonicalId: 'anthropic/claude-sonnet-4-5',
              displayName: 'Claude Sonnet 4.5',
              description: '',
              family: 'claude-sonnet',
              reasoning: true,
              toolCalls: true,
              structuredOutput: true,
              attachments: true,
              openWeights: false,
              contextLimit: 200000,
              outputLimit: 64000,
              inputModalities: ['text', 'image'],
              outputModalities: ['text'],
              knowledgeCutoff: '',
              releaseDate: '2025-09-29',
            ),
            ClientModel(
              id: 'claude-haiku-4-5',
              canonicalId: 'anthropic/claude-haiku-4-5',
              displayName: 'Claude Haiku 4.5',
              description: '',
              family: 'claude-haiku',
              reasoning: true,
              toolCalls: true,
              structuredOutput: true,
              attachments: true,
              openWeights: false,
              contextLimit: 200000,
              outputLimit: 64000,
              inputModalities: ['text', 'image'],
              outputModalities: ['text'],
              knowledgeCutoff: '',
              releaseDate: '2025-10-01',
            ),
          ]
        : const [
            ClientModel(
              id: 'gpt-5.4',
              canonicalId: 'openai/gpt-5.4',
              displayName: 'GPT-5.4',
              description: '',
              family: 'gpt-5',
              reasoning: true,
              toolCalls: true,
              structuredOutput: true,
              attachments: true,
              openWeights: false,
              contextLimit: 400000,
              outputLimit: 128000,
              inputModalities: ['text', 'image'],
              outputModalities: ['text'],
              knowledgeCutoff: '',
              releaseDate: '',
            ),
          ];
    return ClientModelCatalog(
      protocol: protocol,
      providerId: providerId,
      metadataSource: 'models.dev',
      models: models,
    );
  }

  @override
  Future<UpstreamModelCatalog> upstreamModels(
    String endpointId, {
    required String accountId,
    bool refresh = false,
  }) async {
    _requireOpen();
    final failure = _upstreamModelFailure;
    if (failure != null) throw failure;
    final endpoint = _endpoints
        .where((candidate) => candidate.id == endpointId)
        .firstOrNull;
    if (endpoint == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'upstream_endpoint_not_found',
        messageKey: 'error.upstream_endpoint_not_found',
      );
    }
    final account = _accounts
        .where((candidate) => candidate.id == accountId)
        .firstOrNull;
    if (account == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'provider_account_not_found',
        messageKey: 'error.provider_account_not_found',
      );
    }
    if (!account.usable || account.upstreamEndpointId != endpoint.id) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'provider_account_conflict',
        messageKey: 'error.provider_account_conflict',
      );
    }
    final models = switch (endpoint.id) {
      'target.anthropic.official' => const [
        UpstreamModel(
          id: 'claude-sonnet-4-5-20250929',
          displayName: 'Claude Sonnet 4.5',
          ownedBy: 'anthropic',
          verifiedAvailable: true,
          contextLimit: 200000,
          outputLimit: 64000,
        ),
        UpstreamModel(
          id: 'claude-haiku-4-5-20251001',
          displayName: 'Claude Haiku 4.5',
          ownedBy: 'anthropic',
          verifiedAvailable: true,
          contextLimit: 200000,
          outputLimit: 64000,
        ),
      ],
      'target.openai.official' => const [
        UpstreamModel(
          id: 'gpt-5.4',
          displayName: 'GPT-5.4',
          ownedBy: 'openai',
          verifiedAvailable: true,
          contextLimit: 400000,
          outputLimit: 128000,
        ),
      ],
      _ => const [
        UpstreamModel(
          id: 'relay-default',
          displayName: '',
          ownedBy: '',
          verifiedAvailable: true,
          contextLimit: 0,
          outputLimit: 0,
        ),
      ],
    };
    return UpstreamModelCatalog(
      endpointId: endpoint.id,
      endpointRevision: endpoint.revision,
      accountId: account.id,
      accountRevision: account.revision,
      credentialEpoch: account.credentialEpoch,
      observedAt: _now,
      availabilitySource: 'endpoint',
      models: models,
    );
  }

  @override
  Future<UpstreamEndpoint> createUpstreamEndpoint({
    required String id,
    required String displayName,
    required String origin,
    required List<String> backendProtocols,
  }) async {
    _requireOpen();
    if (_endpoints.any((endpoint) => endpoint.id == id)) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    if (!validUpstreamBackendProtocols(backendProtocols)) {
      throw const ControlContractException(
        'upstream Endpoint input is invalid',
      );
    }
    final protocols = upstreamBackendProtocols
        .where(backendProtocols.contains)
        .toList(growable: false);
    final anthropic = protocols.contains('anthropic_messages');
    final endpoint = UpstreamEndpoint(
      id: id,
      displayName: displayName,
      origin: Uri.parse(origin),
      realmId: id,
      backendProtocols: protocols,
      capabilities: const ['messages', 'streaming', 'tool_calls'],
      accountKinds: anthropic
          ? const ['anthropic_api_key', 'bearer_token']
          : const ['bearer_token'],
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
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    _requireOpen();
    headerPolicy.validate(accountKind: kind);
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
      setHeaderNames: headerPolicy.setHeaders.keys.toList(growable: false)
        ..sort(),
      deleteHeaderNames: [...headerPolicy.deleteHeaders]..sort(),
    );
    _accounts.add(account);
    return account;
  }

  @override
  Future<ProviderAccount> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    _requireOpen();
    headerPolicy.validate(accountKind: account.kind);
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
      setHeaderNames: headerPolicy.setHeaders.keys.toList(growable: false)
        ..sort(),
      deleteHeaderNames: [...headerPolicy.deleteHeaders]..sort(),
    );
    _accounts[index] = updated;
    return updated;
  }

  // The fixture answers deletions the way the runtime does: a Capture that is
  // running holds the archive, and everything else completes. It exists so the
  // confirmation flow can be exercised without a daemon, including the refusal
  // path, which is the half that is easy to leave untested.
  @override
  Future<DeletionOutcome> deleteEnvironment(String environmentId) async {
    if (environmentId == 'work') {
      return const DeletionOutcome(
        deleted: false,
        holderCount: 1,
        holders: [
          DeletionHolder(
            kind: 'running_capture',
            id: 'managed_run:run-1',
            label: 'claude',
            detail: 'attached',
          ),
        ],
        released: null,
      );
    }
    _environments.removeWhere((value) => value.id == environmentId);
    return const DeletionOutcome(
      deleted: true,
      holderCount: 0,
      holders: [],
      released: null,
    );
  }

  @override
  Future<DeletionOutcome> deleteUpstreamEndpoint(String endpointId) async {
    return const DeletionOutcome(
      deleted: false,
      holderCount: 1,
      holders: [
        DeletionHolder(
          kind: 'environment_route',
          id: 'work/route-anthropic',
          label: 'Work',
          detail: 'route-anthropic',
        ),
      ],
      released: null,
    );
  }

  @override
  Future<DeletionOutcome> deleteCapture(String captureKey) async {
    final capture = _captures[captureKey];
    if (capture != null && capture.running) {
      return DeletionOutcome(
        deleted: false,
        holderCount: 1,
        holders: [
          DeletionHolder(
            kind: 'running_capture',
            id: captureKey,
            label: capture.displayName,
            detail: capture.state,
          ),
        ],
        released: null,
      );
    }
    _captures.remove(captureKey);
    return const DeletionOutcome(
      deleted: true,
      holderCount: 0,
      holders: [],
      released: DeletionReleased(
        exchanges: 24,
        envelopes: 96,
        activities: 24,
        connections: 8,
        attempts: 24,
        approvals: 3,
        assignments: 1,
        captures: 1,
      ),
    );
  }

  @override
  Future<DeletionOutcome> clearEvidence() async {
    final running = _captures.values.where((capture) => capture.running);
    if (running.isNotEmpty) {
      return DeletionOutcome(
        deleted: false,
        holderCount: running.length,
        holders: running
            .map(
              (capture) => DeletionHolder(
                kind: 'running_capture',
                id: capture.key,
                label: capture.displayName,
                detail: capture.state,
              ),
            )
            .toList(growable: false),
        released: null,
      );
    }
    return const DeletionOutcome(
      deleted: true,
      holderCount: 0,
      holders: [],
      released: DeletionReleased(
        exchanges: 744,
        envelopes: 3055,
        activities: 795,
        connections: 148,
        attempts: 761,
        approvals: 12,
        assignments: 20,
        captures: 20,
      ),
    );
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
  Future<CaptureAssignment> applyLatestCaptureEnvironment(
    CaptureAssignment current,
  ) async {
    _requireOpen();
    final stored = _assignments[current.captureKey];
    if (stored == null) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'capture_assignment_not_found',
        messageKey: 'error.capture_assignment_not_found',
      );
    }
    if (stored.revision != current.revision) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.revision_conflict',
      );
    }
    final latest = _environments.firstWhere(
      (value) => value.id == stored.environmentId,
    );
    if (latest.revision == stored.environmentRevision &&
        latest.digest == stored.environmentDigest) {
      return stored;
    }
    final updated = CaptureAssignment(
      captureKey: stored.captureKey,
      captureId: stored.captureId,
      captureKind: stored.captureKind,
      environmentId: stored.environmentId,
      environmentRevision: latest.revision,
      environmentDigest: latest.digest,
      launchEnvironmentRevision: stored.launchEnvironmentRevision,
      launchEnvironmentDigest: stored.launchEnvironmentDigest,
      revision: stored.revision + 1,
      source: stored.source,
      updatedAt: _now,
    );
    _assignments[current.captureKey] = updated;
    return updated;
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
    final requestBody = utf8.encode(
      '{"model":"claude-sonnet-4-5","stream":true}',
    );
    final responseBody = utf8.encode(
      '{"type":"message","content":[{"type":"text","text":"sample"}]}',
    );
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
          bodyBytes: requestBody.length,
          bodySha256: crypto.sha256.convert(requestBody).toString(),
          digestScope: 'full_body',
          payloadState: 'captured',
          payloadReason: null,
          redactedCredentialFields: const ['Authorization'],
          revealAvailable: true,
        ),
        RawEvidenceEnvelope(
          envelopeId: 'raw-preview-transform-request-$exchangeId',
          layer: 'transform_request_input',
          scopeKind: 'managed_run',
          scopeId: 'run-preview-$exchangeId',
          exchangeId: exchangeId,
          attemptId: 'attempt-preview-$exchangeId',
          observedAt: observed.add(const Duration(milliseconds: 1)),
          expiresAt: observed.add(const Duration(days: 30)),
          method: 'POST',
          path: '/v1/messages',
          contentType: 'application/json',
          representation: 'message_transform_input',
          headerCount: 1,
          trailerCount: 0,
          bodyBytes: requestBody.length,
          bodySha256: crypto.sha256.convert(requestBody).toString(),
          digestScope: 'full_body',
          payloadState: 'captured',
          redactedCredentialFields: const [],
          revealAvailable: true,
        ),
        RawEvidenceEnvelope(
          envelopeId: 'raw-preview-transform-response-$exchangeId',
          layer: 'transform_response_input',
          scopeKind: 'managed_run',
          scopeId: 'run-preview-$exchangeId',
          exchangeId: exchangeId,
          attemptId: 'attempt-preview-$exchangeId',
          observedAt: observed.add(const Duration(milliseconds: 2)),
          expiresAt: observed.add(const Duration(days: 30)),
          statusCode: 200,
          contentType: 'application/json',
          representation: 'message_transform_input',
          headerCount: 1,
          trailerCount: 0,
          bodyBytes: responseBody.length,
          bodySha256: crypto.sha256.convert(responseBody).toString(),
          digestScope: 'full_body',
          payloadState: 'captured',
          redactedCredentialFields: const [],
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
        admittedRecords: 3,
        durableWatermark: 3,
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
    const requestPrefix = 'raw-preview-transform-request-';
    const responsePrefix = 'raw-preview-transform-response-';
    const rawPrefix = 'raw-preview-';
    if (!envelopeId.startsWith(rawPrefix)) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_control_request',
        messageKey: 'error.invalid_control_request',
      );
    }
    final exchangeId = envelopeId.startsWith(requestPrefix)
        ? envelopeId.substring(requestPrefix.length)
        : envelopeId.startsWith(responsePrefix)
        ? envelopeId.substring(responsePrefix.length)
        : envelopeId.substring(rawPrefix.length);
    final page = await rawEvidence(exchangeId);
    final envelope = page.items.firstWhere(
      (candidate) => candidate.envelopeId == envelopeId,
    );
    final transformInput = envelope.layer.startsWith('transform_');
    final responseInput = envelope.layer == 'transform_response_input';
    return RevealedRawEvidence(
      envelope: envelope,
      headers: transformInput
          ? const [
              RawHeaderField(
                name: 'Content-Type',
                values: ['application/json'],
                redacted: [],
              ),
            ]
          : const [
              // The real product stores no recognized credential header
              // value, so the fixture shows the retained digest evidence.
              RawHeaderField(
                name: 'Authorization',
                values: [],
                redacted: [
                  RawRedactedValue(
                    digest:
                        'b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3'
                        'b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3',
                    bytes: 108,
                  ),
                ],
              ),
              RawHeaderField(
                name: 'Content-Type',
                values: ['application/json'],
                redacted: [],
              ),
            ],
      trailers: const [],
      body: Uint8List.fromList(
        utf8.encode(
          responseInput
              ? '{"type":"message","content":[{"type":"text","text":"sample"}]}'
              : '{"model":"claude-sonnet-4-5","stream":true}',
        ),
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
        requestPreview: ActivityRequestPreview(
          kind: index % 4 == 0 ? 'tool_call' : 'text',
          text: index % 4 == 0
              ? 'workspace.read'
              : 'Continue with the next verified implementation step.',
          truncated: false,
        ),
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
      final identity = _previewClientIdentity(
        capture,
        index,
        actorId: '/root/reviewer',
        actorLabel: 'reviewer',
      );
      return ActivityConversationRef(
        id: '${_previewClientSessionProjectionPrefix(identity)}:agent:${_previewConversationProjectionDigest('/root/reviewer')}',
        displayName: 'reviewer',
        kind: 'agent',
        evidence: 'explicit_actor',
        actor: '/root/reviewer',
        clientIdentity: identity,
      );
    }
    if (capture.id == 'run-2') {
      final identity = _previewClientIdentity(capture, index);
      final threadId = identity.protocolIds
          .where((value) => value.name == 'codex.thread_id')
          .first
          .value;
      return ActivityConversationRef(
        id: '${_previewClientSessionProjectionPrefix(identity)}:thread:${_previewConversationProjectionDigest(threadId)}:main',
        displayName: capture.displayName,
        kind: 'main',
        evidence: 'explicit_session',
        actor: null,
        clientIdentity: identity,
      );
    }
    final identity = _previewClientIdentity(capture, index);
    return ActivityConversationRef(
      id: '${_previewClientSessionProjectionPrefix(identity)}:main',
      displayName: capture.displayName,
      kind: 'main',
      evidence: 'explicit_session',
      actor: null,
      clientIdentity: identity,
    );
  }

  String _previewClientSessionProjectionPrefix(AgentClientIdentity identity) =>
      'client_session:${identity.client}:${_previewConversationProjectionDigest(identity.sessionId)}';

  String _previewConversationProjectionDigest(String value) => base64Url
      .encode(crypto.sha256.convert(utf8.encode(value)).bytes)
      .replaceAll('=', '');

  AgentClientIdentity _previewClientIdentity(
    CaptureRecord capture,
    int index, {
    String? actorId,
    String? actorLabel,
  }) {
    final client = capture.managedRun?.executableLabel == 'codex'
        ? 'codex'
        : 'claude';
    final sessionVariant = capture.id == 'run-2'
        ? (index < 12 ? 'primary' : 'resumed')
        : null;
    final sessionId = sessionVariant == null
        ? '$client-session-${capture.id}'
        : '$client-session-${capture.id}-$sessionVariant';
    final responseId = 'response-${capture.id}-exchange-${index + 1}';
    final exchangeCount = capture.id == 'run-1' ? 224 : 24;
    final observedAt = _now.subtract(
      Duration(minutes: (exchangeCount - 1 - index) * 4),
    );
    if (client == 'codex') {
      final threadId =
          actorId ??
          'codex-thread-${capture.id}${sessionVariant == null ? '' : '-$sessionVariant'}';
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
  Future<RuntimeServerAccess> serverAccess() async {
    _requireOpen();
    return _serverAccess;
  }

  @override
  Future<List<RuntimeUser>> runtimeUsers() async {
    _requireOpen();
    return List.unmodifiable(_runtimeUsers);
  }

  @override
  Future<RuntimeUsageReport> runtimeUsage(RuntimeUsageQuery query) async {
    _requireOpen();
    query.toQueryParameters();
    final until = DateTime.parse('${query.until}T00:00:00.000Z');
    final activityDate = until.subtract(const Duration(days: 1));
    final activityDay = [
      activityDate.year.toString().padLeft(4, '0'),
      activityDate.month.toString().padLeft(2, '0'),
      activityDate.day.toString().padLeft(2, '0'),
    ].join('-');
    const emptyTokens = RuntimeTokenUsage(
      inputUncached: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 0,
      ),
      cacheWrite: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 0,
      ),
      cacheRead: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 0,
      ),
      output: RuntimeTokenAggregate(tokens: 0, knownTurns: 0, unknownTurns: 0),
      reasoning: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 0,
      ),
    );
    const aliceTokens = RuntimeTokenUsage(
      inputUncached: RuntimeTokenAggregate(
        tokens: 25864,
        knownTurns: 18,
        unknownTurns: 0,
      ),
      cacheWrite: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 18,
      ),
      cacheRead: RuntimeTokenAggregate(
        tokens: 4200,
        knownTurns: 16,
        unknownTurns: 2,
      ),
      output: RuntimeTokenAggregate(
        tokens: 1318,
        knownTurns: 18,
        unknownTurns: 0,
      ),
      reasoning: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: 18,
      ),
    );
    RuntimeTokenUsage observedTokens({
      required int input,
      required int output,
      required int turns,
    }) => RuntimeTokenUsage(
      inputUncached: RuntimeTokenAggregate(
        tokens: input,
        knownTurns: turns,
        unknownTurns: 0,
      ),
      cacheWrite: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: turns,
      ),
      cacheRead: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: turns,
      ),
      output: RuntimeTokenAggregate(
        tokens: output,
        knownTurns: turns,
        unknownTurns: 0,
      ),
      reasoning: RuntimeTokenAggregate(
        tokens: 0,
        knownTurns: 0,
        unknownTurns: turns,
      ),
    );
    return RuntimeUsageReport(
      generatedAt: _now,
      period: RuntimeUsagePeriod(
        from: query.from,
        until: query.until,
        timeZone: query.timeZone,
      ),
      truncated: false,
      days: [
        RuntimeDayUsage(
          date: activityDay,
          turns: 18,
          succeeded: 16,
          failed: 2,
          canceled: 0,
          contentUnavailableTurns: 0,
          modelUnavailableTurns: 0,
          tokens: aliceTokens,
        ),
      ],
      users: [
        for (final user in _runtimeUsers)
          if (user.username == 'alice')
            RuntimeUserUsage(
              userId: user.id,
              username: user.username,
              state: user.state,
              captureRuns: 3,
              activeRuns: 1,
              turns: 18,
              succeeded: 16,
              failed: 2,
              canceled: 0,
              contentUnavailableTurns: 0,
              modelUnavailableTurns: 0,
              tokens: aliceTokens,
              latestContext: RuntimeUsageContextRef(
                loginSessionId: 'login.preview.alice',
                deviceName: 'MacBook Pro',
                machineId: _previewMachineId,
                workspaceId: 'workspace.preview.vibermate',
                workspaceLabel: 'vibermate',
                observedAt: _now,
              ),
              lastActivityAt: _now,
              days: [
                RuntimeDayUsage(
                  date: activityDay,
                  turns: 18,
                  succeeded: 16,
                  failed: 2,
                  canceled: 0,
                  contentUnavailableTurns: 0,
                  modelUnavailableTurns: 0,
                  tokens: aliceTokens,
                ),
              ],
              models: const [
                RuntimeModelUsage(
                  requestedModel: 'gpt-5.6-sol',
                  upstreamModel: 'dashscope:deepseek-v4-flash-0731',
                  turns: 18,
                  succeeded: 16,
                  failed: 2,
                  canceled: 0,
                  tokens: RuntimeTokenUsage(
                    inputUncached: RuntimeTokenAggregate(
                      tokens: 25864,
                      knownTurns: 18,
                      unknownTurns: 0,
                    ),
                    cacheWrite: RuntimeTokenAggregate(
                      tokens: 0,
                      knownTurns: 0,
                      unknownTurns: 18,
                    ),
                    cacheRead: RuntimeTokenAggregate(
                      tokens: 4200,
                      knownTurns: 16,
                      unknownTurns: 2,
                    ),
                    output: RuntimeTokenAggregate(
                      tokens: 1318,
                      knownTurns: 18,
                      unknownTurns: 0,
                    ),
                    reasoning: RuntimeTokenAggregate(
                      tokens: 0,
                      knownTurns: 0,
                      unknownTurns: 18,
                    ),
                  ),
                ),
              ],
              contexts: [
                RuntimeContextUsage(
                  loginSessionId: 'login.preview.alice.mac',
                  deviceName: 'MacBook Pro',
                  machineId: _previewMachineId,
                  workspaceId: 'workspace.preview.vibermate',
                  workspaceLabel: 'vibermate',
                  captureRuns: 1,
                  activeRuns: 1,
                  turns: 10,
                  succeeded: 9,
                  failed: 1,
                  canceled: 0,
                  tokens: observedTokens(input: 15000, output: 700, turns: 10),
                  lastActivityAt: _now,
                ),
                RuntimeContextUsage(
                  loginSessionId: 'login.preview.alice.linux',
                  deviceName: 'Linux workstation',
                  machineId: 'machine.preview.linux',
                  workspaceId: 'workspace.preview.vibermate',
                  workspaceLabel: 'vibermate',
                  captureRuns: 1,
                  activeRuns: 0,
                  turns: 2,
                  succeeded: 2,
                  failed: 0,
                  canceled: 0,
                  tokens: observedTokens(input: 3000, output: 200, turns: 2),
                  lastActivityAt: _now.subtract(const Duration(minutes: 12)),
                ),
                RuntimeContextUsage(
                  loginSessionId: 'login.preview.alice.design',
                  deviceName: 'MacBook Pro',
                  machineId: _previewMachineId,
                  workspaceId: 'workspace.preview.vibermate-design',
                  workspaceLabel: 'vibermate-design',
                  captureRuns: 1,
                  activeRuns: 0,
                  turns: 6,
                  succeeded: 5,
                  failed: 1,
                  canceled: 0,
                  tokens: observedTokens(input: 7864, output: 418, turns: 6),
                  lastActivityAt: _now.subtract(const Duration(hours: 2)),
                ),
              ],
              agentSessions: [
                RuntimeAgentSessionUsage(
                  client: 'codex',
                  sessionId: '01a02deb-d420-79e2-b0bc-1a9cbdaa643f',
                  captureRuns: 2,
                  turns: 18,
                  succeeded: 16,
                  failed: 2,
                  canceled: 0,
                  tokens: emptyTokens,
                  lastActivityAt: _now,
                ),
              ],
            )
          else
            RuntimeUserUsage(
              userId: user.id,
              username: user.username,
              state: user.state,
              captureRuns: 0,
              activeRuns: 0,
              turns: 0,
              succeeded: 0,
              failed: 0,
              canceled: 0,
              contentUnavailableTurns: 0,
              modelUnavailableTurns: 0,
              tokens: emptyTokens,
              latestContext: null,
              lastActivityAt: null,
              days: const [],
              models: const [],
              contexts: const [],
              agentSessions: const [],
            ),
      ],
    );
  }

  @override
  Future<RuntimeUser> createRuntimeUser({
    required String username,
    required String password,
  }) async {
    _requireOpen();
    if (username.isEmpty ||
        password.length < 8 ||
        _runtimeUsers.any((user) => user.username == username)) {
      throw const ControlProblem(
        status: 422,
        reasonCode: 'invalid_runtime_user',
        messageKey: 'error.invalid_runtime_user',
      );
    }
    final now = DateTime.now().toUtc();
    final created = RuntimeUser(
      id: 'user.preview.${_runtimeUsers.length + 1}',
      username: username,
      state: 'active',
      createdAt: now,
      updatedAt: now,
    );
    _runtimeUsers.add(created);
    return created;
  }

  @override
  Future<RuntimeUser> disableRuntimeUser(String userId) async {
    _requireOpen();
    final index = _runtimeUsers.indexWhere((user) => user.id == userId);
    if (index < 0) {
      throw const ControlProblem(
        status: 404,
        reasonCode: 'runtime_user_not_found',
        messageKey: 'error.runtime_user_not_found',
      );
    }
    final current = _runtimeUsers[index];
    final disabled = RuntimeUser(
      id: current.id,
      username: current.username,
      state: 'disabled',
      createdAt: current.createdAt,
      updatedAt: DateTime.now().toUtc(),
    );
    _runtimeUsers[index] = disabled;
    return disabled;
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
      environmentRevision: context.environmentRevision,
      environmentDigest: context.environmentDigest,
      launchEnvironmentRevision: context.environmentRevision,
      launchEnvironmentDigest: context.environmentDigest,
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
    const thinkingEvidence = '''Inspect the evidence boundary.
Confirm the client Session identity.
Keep the requested model distinct from the upstream model.
Preserve the provider response model as evidence.
Do not infer a vendor from the model ID.
Read the selected Account transport.
Keep credential values outside the evidence body.
Verify the exact upstream request path.
Observe the first protocol boundary.
Retain the raw provider stream.
Project plaintext Thinking separately.
Keep opaque signatures out of plaintext.
Associate the Turn with its Conversation.
Associate the Conversation with its Session.
Keep Subagent identity explicit.
Do not group by timestamps.
Do not group by display titles.
Keep resume on the same Session.
Check the frozen Environment revision.
Record the terminal outcome.''';
    const firstMultiBlockTail = '''
Evidence line 01
Evidence line 02
Evidence line 03
Evidence line 04
Evidence line 05
Evidence line 06
Evidence line 07
Evidence line 08
Evidence line 09
Evidence line 10
Evidence line 11
Evidence line 12
Evidence line 13
Evidence line 14
Evidence line 15
Evidence line 16''';
    const secondMultiBlockEvidence = '''<environment_context>
  <cwd>/Users/mira/Code/vibermate</cwd>
  <shell>zsh</shell>
  <current_date>2026-08-23</current_date>
  <timezone>Asia/Singapore</timezone>
  <line>06</line>
  <line>07</line>
  <line>08</line>
  <line>09</line>
  <line>10</line>
  <line>11</line>
  <line>12</line>
  <line>13</line>
  <line>14</line>
  <line>15</line>
  <line>16</line>
</environment_context>''';
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
              'hello${index == 221 ? firstMultiBlockTail : ''}',
            ),
            if (index == 221) _previewTextBlock(secondMultiBlockEvidence),
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
    final routeId = activity.routeId ?? '';
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
      targetOrigin: routeId.contains('openai')
          ? 'https://api.openai.com'
          : routeId.contains('orbit')
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
              if (index == 221)
                ExchangeContentBlock(
                  kind: 'reasoning',
                  availability: 'recorded',
                  text: thinkingEvidence,
                  originalSize: thinkingEvidence.length,
                  callId: null,
                  toolName: null,
                  toolNamespace: null,
                  arguments: null,
                  toolError: false,
                  providerSource: 'anthropic-messages',
                  providerKind: 'thinking',
                  fingerprint: null,
                  agent: null,
                ),
              if (index == 221)
                ExchangeContentBlock(
                  kind: 'reasoning',
                  availability: 'recorded',
                  text: thinkingEvidence,
                  originalSize: thinkingEvidence.length,
                  callId: null,
                  toolName: null,
                  toolNamespace: null,
                  arguments: null,
                  toolError: false,
                  providerSource: 'openai-responses',
                  providerKind: 'reasoning_summary',
                  fingerprint: null,
                  agent: null,
                ),
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
            protocolEvidence: const [],
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
          // Only one fixture Exchange carries a top-level instruction
          // parameter. A fixture exists to exercise a surface, not to make every
          // other fixture taller.
          system: activity.id.endsWith('-exchange-222')
              ? [
                  _previewTextBlock(
                    'You are an interactive agent. Stay precise.',
                  ),
                ]
              : const [],
          messages: visibleMessages,
          tools: toolTurn
              ? const [ExchangeToolDefinition(name: 'Read', namespace: null)]
              : const [],
          protocolEvidence: const [],
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
            .map(
              (endpoint) =>
                  '${endpoint.clientOrigin.host}:${endpoint.clientOrigin.hasPort ? endpoint.clientOrigin.port : 443}',
            )
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

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';
import 'dart:typed_data';

import 'control_models.dart';
import 'provider_origin.dart';

abstract interface class ControlApi {
  Future<DashboardData> loadDashboard();

  Future<CapturePage> captures({String? cursor, int limit = 50});

  Future<OfflineHoldSnapshot> enterOfflineHold(OfflineHoldSnapshot current);

  Future<OfflineHoldSnapshot> resumeOfflineHold(OfflineHoldSnapshot current);

  Future<EnvironmentRecord> environmentRevision(
    String environmentId,
    int revision,
  );

  Future<EnvironmentDraft> environmentDraft(String environmentId);

  Future<EnvironmentDraft> saveEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required EnvironmentDraftInput input,
  });

  Future<EnvironmentImpact> previewEnvironmentDraft(
    String environmentId,
    int draftRevision,
  );

  Future<EnvironmentPublishResult> publishEnvironmentDraft(
    String environmentId,
    int draftRevision,
  );

  Future<CaptureAssignment> captureAssignment(String captureKey);

  Future<WorkspaceEnvironmentDefault?> workspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
  });

  Future<WorkspaceEnvironmentDefault> setWorkspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
    required int expectedRevision,
    required String environmentId,
  });

  Future<void> clearWorkspaceEnvironmentDefault({
    required WorkspaceEnvironmentDefault current,
  });

  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  });

  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  });

  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  });

  Future<RawEvidencePage> rawEvidence(String exchangeId);

  Future<RevealedRawEvidence> revealRawEvidence({required String envelopeId});

  Future<NetworkData> loadNetwork();

  Future<List<ApprovalRecord>> pendingApprovals();

  Future<UpstreamEndpoint> createUpstreamEndpoint({
    required String id,
    required String displayName,
    required String origin,
    required String kind,
  });

  Future<ProviderAccount> createProviderAccount({
    required String id,
    required String displayName,
    required String upstreamEndpointId,
    required String kind,
    required String secret,
  });

  Future<ProviderAccount> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
  });

  /// Retires an Environment. Refused, with holders, while a Capture is running
  /// or a workspace default names it.
  Future<DeletionOutcome> deleteEnvironment(String environmentId);

  /// Retires an upstream Endpoint. Refused while a published route names it or
  /// it still owns an Account.
  Future<DeletionOutcome> deleteUpstreamEndpoint(String endpointId);

  /// Deletes a Capture and every piece of evidence scoped to it. Refused while
  /// that Capture is still running.
  Future<DeletionOutcome> deleteCapture(String captureKey);

  /// Empties the evidence archive, keeping configuration. Refused while any
  /// Capture is running.
  Future<DeletionOutcome> clearEvidence();

  Future<ProviderAccountDeleteResult> deleteProviderAccount(
    ProviderAccount account,
  );

  Future<ConnectionPage> connections({String? cursor, int limit = 50});

  Future<EgressAttemptPage> egressAttempts({String? cursor, int limit = 50});

  Future<ApprovalRecord> decideApproval({
    required ApprovalRecord approval,
    required ApprovalChoice choice,
  });

  Future<ConnectionRuleSet> replaceConnectionRules({
    required ConnectionRuleSet current,
    required List<ConnectionRule> rules,
    required String mode,
  });

  Future<CaptureAssignmentChange> switchCaptureEnvironment({
    required CaptureAssignment assignment,
    required String environmentId,
  });

  Future<ManualCaptureContext> manualCaptureContext(String environmentId);

  Future<ManualCaptureGrantStateTag> createManualCapture({
    required ManualCaptureContext context,
    required String displayName,
    required String clientClass,
    required String lifetime,
    int? expiresInSeconds,
  });

  Future<ManualCaptureGrantStateTag> rotateManualCapture(
    ManualCaptureStateTag current,
  );

  Future<ManualCaptureStateTag> manualCaptureState(String manualCaptureId);

  Future<void> revokeManualCapture({
    required String manualCaptureId,
    required String stateTag,
  });

  Future<void> close();
}

final class ControlProblem implements Exception {
  const ControlProblem({
    required this.status,
    required this.reasonCode,
    required this.messageKey,
  });

  factory ControlProblem.fromJson(Object? json, {required int status}) {
    final value = requireObject(json, 'problem');
    requireFields(
      value,
      'problem',
      required: const {'type', 'title', 'status', 'code'},
    );
    final wireStatus = requireInteger(value, 'status', 'problem', minimum: 100);
    final code = requireString(value, 'code', 'problem');
    if (wireStatus != status ||
        !RegExp(r'^[a-z][a-z0-9_]{0,127}$').hasMatch(code) ||
        requireString(value, 'type', 'problem') !=
            'urn:vibermate:error:${code.replaceAll('_', '-')}' ||
        requireString(value, 'title', 'problem').length > 128) {
      throw const ControlContractException('problem response is invalid');
    }
    return ControlProblem(
      status: status,
      reasonCode: code,
      messageKey: 'error.$code',
    );
  }

  final int status;
  final String reasonCode;
  final String messageKey;

  @override
  String toString() => 'Control problem $status: $reasonCode';
}

final class HttpControlApi implements ControlApi {
  HttpControlApi._(this._session, this._client);

  static const _origin = 'vibermate://desktop';
  static const _maximumResponseBytes = 2 * 1024 * 1024;
  // A deliberately revealed body may contain the configured 16 MiB retained
  // prefix plus Base64 and JSON overhead. Ordinary control reads stay at the
  // tighter 2 MiB boundary.
  static const _maximumRawRevealResponseBytes = 32 * 1024 * 1024;
  static const _requestTimeout = Duration(seconds: 10);

  static Future<HttpControlApi> connect(
    DesktopSession session, {
    HttpClient? client,
  }) async {
    final api = HttpControlApi._(session, client ?? HttpClient());
    await api._inspectSession();
    return api;
  }

  DesktopSession _session;
  final HttpClient _client;
  int _sessionRevision = 1;
  DateTime? _renewAt;
  Future<void>? _renewal;
  bool _closed = false;

  @override
  Future<DashboardData> loadDashboard() async {
    final results = await Future.wait<Object?>([
      _read('/api/v1/status'),
      captures(),
      _read('/api/v1/environments'),
      _read('/api/v1/upstream-endpoints'),
      _read('/api/v1/provider-accounts'),
    ]);
    return DashboardData(
      status: RuntimeStatus.fromJson(
        results[0],
        expectedInstanceId: _session.instanceId,
      ),
      captures: (results[1]! as CapturePage).items,
      captureNextCursor: (results[1]! as CapturePage).nextCursor,
      environments: _page(
        results[2],
        'environments',
        (item, path) => EnvironmentRecord.fromJson(item, path),
      ),
      endpoints: _page(
        results[3],
        'upstreamEndpoints',
        (item, path) => UpstreamEndpoint.fromJson(item, path),
      ),
      accounts: _page(
        results[4],
        'providerAccounts',
        (item, path) => ProviderAccount.fromJson(item, path),
      ),
    );
  }

  @override
  Future<CapturePage> captures({String? cursor, int limit = 50}) async {
    _validatePageRequest(cursor, limit);
    if (limit > 199) {
      throw const ControlContractException(
        'Capture page limit exceeds the API maximum',
      );
    }
    final query = <String, String>{'limit': '$limit'};
    if (cursor != null) query['cursor'] = cursor;
    final uri = Uri(path: '/api/v1/captures', queryParameters: query);
    return CapturePage.fromJson(await _read(uri.toString()), 'captures');
  }

  @override
  Future<OfflineHoldSnapshot> enterOfflineHold(
    OfflineHoldSnapshot current,
  ) async {
    if (!current.canEnter) {
      throw const ControlContractException(
        'offline hold can only be entered from online',
      );
    }
    final snapshot = OfflineHoldSnapshot.fromJson(
      await _mutation(
        'POST',
        '/api/v1/offline-hold/actions/enter',
        expectedRevision: current.revision,
        responseTimeout: const Duration(minutes: 2),
      ),
      path: 'offlineHold.enter',
      afterRevision: current.revision,
    );
    if (snapshot.state != 'held' || !snapshot.safeToDisconnect) {
      throw const ControlContractException(
        'offline hold enter did not reach the safe boundary',
      );
    }
    return snapshot;
  }

  @override
  Future<OfflineHoldSnapshot> resumeOfflineHold(
    OfflineHoldSnapshot current,
  ) async {
    if (!current.canResume) {
      throw const ControlContractException(
        'offline hold can only resume from held',
      );
    }
    final snapshot = OfflineHoldSnapshot.fromJson(
      await _mutation(
        'POST',
        '/api/v1/offline-hold/actions/resume',
        expectedRevision: current.revision,
        responseTimeout: const Duration(seconds: 30),
      ),
      path: 'offlineHold.resume',
      afterRevision: current.revision,
    );
    if (!const {'online', 'releasing', 'held'}.contains(snapshot.state) ||
        (snapshot.state == 'held') != (snapshot.lastProbeReason != null)) {
      throw const ControlContractException(
        'offline hold resume response is inconsistent',
      );
    }
    return snapshot;
  }

  @override
  Future<EnvironmentDraft> environmentDraft(String environmentId) async {
    if (!_validResourceId(environmentId)) {
      throw const ControlContractException('Environment ID is invalid');
    }
    return EnvironmentDraft.fromJson(
      await _read(
        '/api/v1/environments/${Uri.encodeComponent(environmentId)}/draft',
      ),
      'environmentDraft',
      expectedEnvironmentId: environmentId,
    );
  }

  @override
  Future<EnvironmentRecord> environmentRevision(
    String environmentId,
    int revision,
  ) async {
    if (!_validResourceId(environmentId) || revision < 1) {
      throw const ControlContractException(
        'Environment revision authority is invalid',
      );
    }
    final environment = EnvironmentRecord.fromJson(
      await _read(
        '/api/v1/environments/${Uri.encodeComponent(environmentId)}/revisions/$revision',
      ),
      'environmentRevision',
    );
    if (environment.id != environmentId || environment.revision != revision) {
      throw const ControlContractException(
        'Environment revision response does not match the requested authority',
      );
    }
    return environment;
  }

  @override
  Future<EnvironmentDraft> saveEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required EnvironmentDraftInput input,
  }) async {
    input.validateFor(environmentId, expectedBaseRevision);
    final draft = EnvironmentDraft.fromJson(
      await _mutation(
        'PUT',
        '/api/v1/environments/${Uri.encodeComponent(environmentId)}/draft',
        expectedRevision: expectedBaseRevision,
        body: input.toJson(),
      ),
      'environmentDraft',
      expectedEnvironmentId: environmentId,
    );
    final draftRevisionAdvanced = input.expectedDraftRevision == 0
        ? draft.draftRevision > 0
        : draft.draftRevision == input.expectedDraftRevision + 1;
    if (draft.baseRevision != expectedBaseRevision || !draftRevisionAdvanced) {
      throw const ControlContractException(
        'Environment draft revision did not advance',
      );
    }
    return draft;
  }

  @override
  Future<EnvironmentImpact> previewEnvironmentDraft(
    String environmentId,
    int draftRevision,
  ) async {
    if (!_validResourceId(environmentId) || draftRevision < 1) {
      throw const ControlContractException(
        'Environment draft preview authority is invalid',
      );
    }
    return EnvironmentImpact.fromJson(
      await _mutation(
        'POST',
        '/api/v1/environments/${Uri.encodeComponent(environmentId)}/draft/actions/preview',
        expectedRevision: draftRevision,
      ),
      'environmentImpact',
      expectedEnvironmentId: environmentId,
      expectedDraftRevision: draftRevision,
    );
  }

  @override
  Future<EnvironmentPublishResult> publishEnvironmentDraft(
    String environmentId,
    int draftRevision,
  ) async {
    if (!_validResourceId(environmentId) || draftRevision < 1) {
      throw const ControlContractException(
        'Environment draft publish authority is invalid',
      );
    }
    return EnvironmentPublishResult.fromJson(
      await _mutation(
        'POST',
        '/api/v1/environments/${Uri.encodeComponent(environmentId)}/draft/actions/publish',
        expectedRevision: draftRevision,
      ),
      'environmentPublish',
      expectedEnvironmentId: environmentId,
      expectedDraftRevision: draftRevision,
    );
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) async {
    final payload = await _read(
      '/api/v1/captures/${Uri.encodeComponent(captureKey)}/environment-assignment',
    );
    final assignment = CaptureAssignment.fromJson(payload, 'captureAssignment');
    if (assignment.captureKey != captureKey) {
      throw const ControlContractException(
        'Capture assignment identity is inconsistent',
      );
    }
    return assignment;
  }

  @override
  Future<WorkspaceEnvironmentDefault?> workspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
  }) async {
    _validateWorkspaceIdentity(machineId, 'machine');
    _validateWorkspaceIdentity(workspaceId, 'workspace');
    try {
      return WorkspaceEnvironmentDefault.fromJson(
        await _read(_workspaceDefaultPath(machineId, workspaceId)),
        'workspaceEnvironmentDefault',
        expectedMachineId: machineId,
        expectedWorkspaceId: workspaceId,
      );
    } on ControlProblem catch (problem) {
      if (problem.status == 404 &&
          problem.reasonCode == 'workspace_environment_default_not_found') {
        return null;
      }
      rethrow;
    }
  }

  @override
  Future<WorkspaceEnvironmentDefault> setWorkspaceEnvironmentDefault({
    required String machineId,
    required String workspaceId,
    required int expectedRevision,
    required String environmentId,
  }) async {
    _validateWorkspaceIdentity(machineId, 'machine');
    _validateWorkspaceIdentity(workspaceId, 'workspace');
    if (expectedRevision < 0 ||
        !_validResourceId(environmentId) ||
        environmentId == 'system_transparent') {
      throw const ControlContractException(
        'Workspace Environment default authority is invalid',
      );
    }
    final updated = WorkspaceEnvironmentDefault.fromJson(
      await _mutation(
        'PUT',
        _workspaceDefaultPath(machineId, workspaceId),
        expectedRevision: expectedRevision,
        body: {'environmentId': environmentId},
      ),
      'workspaceEnvironmentDefault',
      expectedMachineId: machineId,
      expectedWorkspaceId: workspaceId,
      expectedEnvironmentId: environmentId,
    );
    if (updated.revision != expectedRevision + 1) {
      throw const ControlContractException(
        'Workspace Environment default revision did not advance',
      );
    }
    return updated;
  }

  @override
  Future<void> clearWorkspaceEnvironmentDefault({
    required WorkspaceEnvironmentDefault current,
  }) async {
    _validateWorkspaceIdentity(current.machineId, 'machine');
    _validateWorkspaceIdentity(current.workspaceId, 'workspace');
    if (current.revision < 1) {
      throw const ControlContractException(
        'Workspace Environment default revision is invalid',
      );
    }
    final payload = await _mutation(
      'DELETE',
      _workspaceDefaultPath(current.machineId, current.workspaceId),
      expectedRevision: current.revision,
      expectedStatus: 204,
    );
    if (payload != null) {
      throw const ControlContractException(
        'Workspace Environment default clear response is invalid',
      );
    }
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
    _validatePageRequest(cursor, limit);
    if ((captureRunId != null && !_validResourceId(captureRunId)) ||
        (manualCaptureId != null && !_validResourceId(manualCaptureId)) ||
        (captureRunId != null && manualCaptureId != null) ||
        (environmentId != null && !_validResourceId(environmentId)) ||
        (conversationId != null && !_validConversationId(conversationId))) {
      throw const ControlContractException('activity query is invalid');
    }
    final uri = Uri(
      path: '/api/v1/activities',
      queryParameters: {
        'kind': 'exchange',
        'limit': '$limit',
        'cursor': ?cursor,
        'captureRunId': ?captureRunId,
        'manualCaptureId': ?manualCaptureId,
        'environmentId': ?environmentId,
        'conversationId': ?conversationId,
      },
    );
    return ActivityPage.fromJson(await _read(uri.toString()), 'activities');
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) async {
    _validatePageRequest(cursor, limit);
    if ((captureRunId != null && !_validResourceId(captureRunId)) ||
        (manualCaptureId != null && !_validResourceId(manualCaptureId)) ||
        (captureRunId != null && manualCaptureId != null)) {
      throw const ControlContractException('Conversation query is invalid');
    }
    final uri = Uri(
      path: '/api/v1/conversations',
      queryParameters: {
        'limit': '$limit',
        'cursor': ?cursor,
        'captureRunId': ?captureRunId,
        'manualCaptureId': ?manualCaptureId,
      },
    );
    return ConversationPage.fromJson(
      await _read(uri.toString()),
      'conversations',
    );
  }

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) async {
    if (!_validResourceId(exchangeId) ||
        !const {'incremental', 'full'}.contains(contentView)) {
      throw const ControlContractException('Exchange query is invalid');
    }
    final uri = Uri(
      path: '/api/v1/exchanges/${Uri.encodeComponent(exchangeId)}',
      queryParameters: {'contentView': contentView},
    );
    final detail = ExchangeDetail.fromJson(
      await _read(uri.toString()),
      'exchange',
    );
    final projection = detail.content.requestProjection;
    if (detail.id != exchangeId ||
        (projection != null && projection.view != contentView)) {
      throw const ControlContractException(
        'Exchange response does not match the requested projection',
      );
    }
    return detail;
  }

  @override
  Future<RawEvidencePage> rawEvidence(String exchangeId) async {
    if (!_validRawEvidenceIdentity(exchangeId)) {
      throw const ControlContractException('Raw evidence query is invalid');
    }
    await _ensureFreshSession();
    final response = await _send(
      'GET',
      '/api/v1/exchanges/${Uri.encodeComponent(exchangeId)}/raw-evidence',
      token: _session.readToken,
      expectedStatus: 200,
    );
    if (response.headers.value(HttpHeaders.cacheControlHeader) != 'no-store') {
      throw const ControlContractException(
        'Raw evidence response can be cached',
      );
    }
    return RawEvidencePage.fromJson(
      response.payload,
      'rawEvidence',
      expectedExchangeId: exchangeId,
    );
  }

  @override
  Future<RevealedRawEvidence> revealRawEvidence({
    required String envelopeId,
  }) async {
    if (!_validRawEvidenceIdentity(envelopeId)) {
      throw const ControlContractException('Raw evidence reveal is invalid');
    }
    await _ensureFreshSession();
    final response = await _send(
      'POST',
      '/api/v1/raw-evidence/${Uri.encodeComponent(envelopeId)}/actions/reveal',
      token: _session.writeToken,
      expectedStatus: 200,
      maximumResponseBytes: _maximumRawRevealResponseBytes,
    );
    if (response.headers.value(HttpHeaders.cacheControlHeader) != 'no-store') {
      throw const ControlContractException(
        'Revealed raw evidence response can be cached',
      );
    }
    return RevealedRawEvidence.fromJson(
      response.payload,
      'rawReveal',
      expectedEnvelopeId: envelopeId,
    );
  }

  @override
  Future<List<ApprovalRecord>> pendingApprovals() async {
    final page = requireObject(
      await _read('/api/v1/approvals?state=pending&limit=50'),
      'approvals',
    );
    requireFields(page, 'approvals', required: const {'items'});
    final items = requireList(page['items'], 'approvals.items');
    if (items.length > 50) {
      throw const ControlContractException(
        'pending approval response exceeds the requested page',
      );
    }
    final records = items.indexed
        .map(
          (entry) =>
              ApprovalRecord.fromJson(entry.$2, 'approvals.items[${entry.$1}]'),
        )
        .toList(growable: false);
    if (records.any((record) => record.state != 'pending')) {
      throw const ControlContractException(
        'pending approval response contains a resolved record',
      );
    }
    return records;
  }

  @override
  Future<NetworkData> loadNetwork() async {
    final values = await Future.wait<Object?>([
      pendingApprovals(),
      connections(),
      egressAttempts(),
      _read('/api/v1/policies/connections'),
    ]);
    return NetworkData(
      approvals: values[0]! as List<ApprovalRecord>,
      connections: values[1]! as ConnectionPage,
      egressAttempts: values[2]! as EgressAttemptPage,
      rules: ConnectionRuleSet.fromJson(values[3], 'connectionRules'),
    );
  }

  @override
  Future<UpstreamEndpoint> createUpstreamEndpoint({
    required String id,
    required String displayName,
    required String origin,
    required String kind,
  }) async {
    if (!_validResourceId(id) ||
        !_validDisplayLabel(displayName) ||
        !isCanonicalProviderOrigin(origin) ||
        !const {'anthropic', 'openai_compatible'}.contains(kind)) {
      throw const ControlContractException(
        'upstream Endpoint input is invalid',
      );
    }
    final payload = await _mutation(
      'POST',
      '/api/v1/upstream-endpoints',
      expectedRevision: 0,
      expectedStatus: 201,
      body: {
        'id': id,
        'displayName': displayName,
        'origin': origin,
        'kind': kind,
      },
    );
    final created = UpstreamEndpoint.fromJson(payload, 'upstreamEndpoint');
    if (created.id != id || created.revision != 1) {
      throw const ControlContractException(
        'created upstream Endpoint response is inconsistent',
      );
    }
    return created;
  }

  @override
  Future<ProviderAccount> createProviderAccount({
    required String id,
    required String displayName,
    required String upstreamEndpointId,
    required String kind,
    required String secret,
  }) async {
    if (!_validResourceId(id) ||
        !_validDisplayLabel(displayName) ||
        !_validResourceId(upstreamEndpointId) ||
        !const {
          'anthropic_api_key',
          'claude_oauth_token',
          'openai_api_key',
        }.contains(kind) ||
        !_validSecret(secret)) {
      throw const ControlContractException('Provider Account input is invalid');
    }
    final payload = await _mutation(
      'POST',
      '/api/v1/provider-accounts',
      expectedRevision: 0,
      expectedStatus: 201,
      body: {
        'id': id,
        'displayName': displayName,
        'upstreamEndpointId': upstreamEndpointId,
        'kind': kind,
        'secret': secret,
      },
    );
    final created = ProviderAccount.fromJson(payload, 'providerAccount');
    if (created.id != id ||
        created.upstreamEndpointId != upstreamEndpointId ||
        created.kind != kind ||
        created.credentialState != 'ready' ||
        created.credentialEpoch != 1) {
      throw const ControlContractException(
        'created Provider Account response is inconsistent',
      );
    }
    return created;
  }

  @override
  Future<ProviderAccount> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
  }) async {
    if (!_validResourceId(account.id) || !_validSecret(secret)) {
      throw const ControlContractException(
        'Provider Account credential input is invalid',
      );
    }
    final payload = await _mutation(
      'PUT',
      '/api/v1/provider-accounts/${Uri.encodeComponent(account.id)}/credential',
      expectedRevision: account.credentialEpoch,
      body: {'secret': secret},
    );
    final updated = ProviderAccount.fromJson(payload, 'providerAccount');
    if (updated.id != account.id ||
        updated.upstreamEndpointId != account.upstreamEndpointId ||
        updated.kind != account.kind ||
        updated.credentialState != 'ready' ||
        updated.credentialEpoch != account.credentialEpoch + 1) {
      throw const ControlContractException(
        'Provider Account credential response is inconsistent',
      );
    }
    return updated;
  }

  @override
  Future<ProviderAccountDeleteResult> deleteProviderAccount(
    ProviderAccount account,
  ) async {
    if (!_validResourceId(account.id)) {
      throw const ControlContractException('Provider Account ID is invalid');
    }
    final payload = await _mutation(
      'DELETE',
      '/api/v1/provider-accounts/${Uri.encodeComponent(account.id)}',
      expectedRevision: account.credentialEpoch,
    );
    return ProviderAccountDeleteResult.fromJson(
      payload,
      'providerAccountDelete',
    );
  }

  @override
  Future<DeletionOutcome> deleteEnvironment(String environmentId) => _delete(
    '/api/v1/environments/${Uri.encodeComponent(environmentId)}',
    'environmentDelete',
    valid: _validResourceId(environmentId),
    subject: 'Environment ID',
  );

  @override
  Future<DeletionOutcome> deleteUpstreamEndpoint(String endpointId) => _delete(
    '/api/v1/upstream-endpoints/${Uri.encodeComponent(endpointId)}',
    'upstreamEndpointDelete',
    valid: _validResourceId(endpointId),
    subject: 'Upstream Endpoint ID',
  );

  @override
  Future<DeletionOutcome> deleteCapture(String captureKey) => _delete(
    '/api/v1/captures/${Uri.encodeComponent(captureKey)}',
    'captureDelete',
    valid:
        captureKey.startsWith('managed_run:') ||
        captureKey.startsWith('manual_capture:'),
    subject: 'Capture key',
  );

  @override
  Future<DeletionOutcome> clearEvidence() async => DeletionOutcome.fromJson(
    await _mutation(
      'POST',
      '/api/v1/evidence/actions/clear',
      expectedRevision: 0,
    ),
    'evidenceClear',
  );

  /// Every destructive operation answers the same shape, so they share one
  /// call. The expected revision is deliberately absent: a delete is guarded by
  /// its holders, not by a revision the user would have to hold on to.
  Future<DeletionOutcome> _delete(
    String path,
    String contractPath, {
    required bool valid,
    required String subject,
  }) async {
    if (!valid) {
      throw ControlContractException('$subject is invalid');
    }
    return DeletionOutcome.fromJson(
      await _mutation('DELETE', path, expectedRevision: 0),
      contractPath,
    );
  }

  @override
  Future<ConnectionPage> connections({String? cursor, int limit = 50}) async {
    _validatePageRequest(cursor, limit);
    final query = <String, String>{'limit': '$limit', 'view': 'latest'};
    if (cursor != null) query['cursor'] = cursor;
    final uri = Uri(path: '/api/v1/connections', queryParameters: query);
    final value = requireObject(await _read(uri.toString()), 'connections');
    final items = requireList(value['items'], 'connections.items');
    return ConnectionPage(
      items: items.indexed
          .map(
            (entry) => ConnectionRecord.fromJson(
              entry.$2,
              'connections.items[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      nextCursor: optionalString(value, 'nextCursor', 'connections'),
    );
  }

  @override
  Future<EgressAttemptPage> egressAttempts({
    String? cursor,
    int limit = 50,
  }) async {
    _validatePageRequest(cursor, limit);
    final query = <String, String>{'limit': '$limit'};
    if (cursor != null) query['cursor'] = cursor;
    final uri = Uri(path: '/api/v1/egress-attempts', queryParameters: query);
    final value = requireObject(await _read(uri.toString()), 'egressAttempts');
    final items = requireList(value['items'], 'egressAttempts.items');
    return EgressAttemptPage(
      items: items.indexed
          .map(
            (entry) => EgressAttemptRecord.fromJson(
              entry.$2,
              'egressAttempts.items[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      nextCursor: optionalString(value, 'nextCursor', 'egressAttempts'),
    );
  }

  @override
  Future<ApprovalRecord> decideApproval({
    required ApprovalRecord approval,
    required ApprovalChoice choice,
  }) async {
    final offered = approval.choices.any(
      (candidate) =>
          candidate.decision == choice.decision &&
          candidate.scope == choice.scope,
    );
    if (approval.state != 'pending' || !offered) {
      throw const ControlContractException(
        'approval decision is no longer available',
      );
    }
    final payload = await _mutation(
      'POST',
      '/api/v1/approvals/${Uri.encodeComponent(approval.id)}/actions/decide',
      expectedRevision: approval.revision,
      body: {
        'decision': choice.decision,
        'scope': choice.scope,
        if (choice.decision == 'deny') 'reasonCode': 'user_denied',
      },
    );
    final resolved = ApprovalRecord.fromJson(payload, 'approvalDecision');
    if (resolved.id != approval.id ||
        resolved.revision <= approval.revision ||
        resolved.decision != choice.decision ||
        resolved.decisionScope != choice.scope) {
      throw const ControlContractException(
        'approval decision response is inconsistent',
      );
    }
    return resolved;
  }

  @override
  Future<ConnectionRuleSet> replaceConnectionRules({
    required ConnectionRuleSet current,
    required List<ConnectionRule> rules,
    required String mode,
  }) async {
    if (!const {'monitor', 'ask_unknown', 'deny_unknown'}.contains(mode)) {
      throw const ControlContractException(
        'connection policy mode is unsupported',
      );
    }
    final payload = await _mutation(
      'PATCH',
      '/api/v1/policies/connections',
      expectedRevision: current.revision,
      body: {
        'rules': rules.map((rule) => rule.toJson()).toList(growable: false),
        'mode': mode,
      },
    );
    final updated = ConnectionRuleSet.fromJson(payload, 'connectionRules');
    if (updated.revision != current.revision + 1) {
      throw const ControlContractException(
        'connection policy revision did not advance',
      );
    }
    return updated;
  }

  @override
  Future<CaptureAssignmentChange> switchCaptureEnvironment({
    required CaptureAssignment assignment,
    required String environmentId,
  }) async {
    if (!_validResourceId(environmentId)) {
      throw const ControlContractException('Environment ID is invalid');
    }
    final payload = await _mutation(
      'PATCH',
      '/api/v1/captures/${Uri.encodeComponent(assignment.captureKey)}/environment-assignment',
      expectedRevision: assignment.revision,
      body: {'environmentId': environmentId},
    );
    final change = CaptureAssignmentChange.fromJson(
      payload,
      'captureAssignmentSwitch',
    );
    final revisionAdvanced =
        change.boundary == 'hot_switch' ||
        change.boundary == 'reconnect_required';
    if (change.assignment.captureKey != assignment.captureKey ||
        change.assignment.captureId != assignment.captureId ||
        change.assignment.captureKind != assignment.captureKind ||
        change.assignment.environmentId !=
            (revisionAdvanced ? environmentId : assignment.environmentId) ||
        change.assignment.revision !=
            assignment.revision + (revisionAdvanced ? 1 : 0)) {
      throw const ControlContractException(
        'Capture assignment switch authority is inconsistent',
      );
    }
    return change;
  }

  @override
  Future<ManualCaptureContext> manualCaptureContext(
    String environmentId,
  ) async {
    if (!_validResourceId(environmentId)) {
      throw const ControlContractException('Environment ID is invalid');
    }
    await _ensureFreshSession();
    final uri = Uri(
      path: '/api/v1/manual-captures/context',
      queryParameters: {'environmentId': environmentId},
    );
    final response = await _send(
      'GET',
      uri.toString(),
      token: _session.readToken,
      expectedStatus: 200,
    );
    _manualStateTag(response, required: false);
    return ManualCaptureContext.fromJson(
      response.payload,
      'manualCaptureContext',
      expectedEnvironmentId: environmentId,
    );
  }

  @override
  Future<ManualCaptureGrantStateTag> createManualCapture({
    required ManualCaptureContext context,
    required String displayName,
    required String clientClass,
    required String lifetime,
    int? expiresInSeconds,
  }) async {
    if (!_validResourceId(context.environmentId) ||
        !_validDisplayLabel(displayName) ||
        !const {'cli', 'desktop_app', 'other'}.contains(clientClass) ||
        !const {'temporary', 'until_revoked'}.contains(lifetime) ||
        !RegExp(
          r'^ctx_[A-Za-z0-9_-]{43}$',
        ).hasMatch(context.confirmationToken) ||
        ((lifetime == 'temporary') != (expiresInSeconds != null)) ||
        (expiresInSeconds != null &&
            (expiresInSeconds < 1 ||
                expiresInSeconds > context.maxTemporarySeconds))) {
      throw const ControlContractException('Manual Capture input is invalid');
    }
    await _ensureFreshSession();
    final response = await _send(
      'POST',
      '/api/v1/manual-captures',
      token: _session.writeToken,
      expectedStatus: 201,
      body: {
        'environmentId': context.environmentId,
        'displayName': displayName,
        'clientClass': clientClass,
        'lifetime': lifetime,
        'expiresInSeconds': ?expiresInSeconds,
        'confirmationToken': context.confirmationToken,
      },
    );
    final stateTag = _manualStateTag(response, required: true)!;
    final grant = ManualCaptureGrant.fromJson(
      response.payload,
      'manualCaptureGrant',
    );
    if (grant.environmentId != context.environmentId ||
        grant.capture.displayName != displayName ||
        grant.capture.clientClass != clientClass ||
        grant.capture.lifetime != lifetime ||
        grant.capture.state != 'active') {
      throw const ControlContractException(
        'created Manual Capture grant is inconsistent',
      );
    }
    return ManualCaptureGrantStateTag(grant: grant, stateTag: stateTag);
  }

  @override
  Future<ManualCaptureGrantStateTag> rotateManualCapture(
    ManualCaptureStateTag current,
  ) async {
    if (!_validResourceId(current.capture.id) ||
        current.capture.state != 'active' ||
        !_validManualStateTag(current.stateTag)) {
      throw const ControlContractException(
        'Manual Capture rotation authority is invalid',
      );
    }
    await _ensureFreshSession();
    final response = await _send(
      'POST',
      '/api/v1/manual-captures/${Uri.encodeComponent(current.capture.id)}/actions/rotate-credential',
      token: _session.writeToken,
      expectedStatus: 200,
      headers: {HttpHeaders.ifMatchHeader: current.stateTag},
    );
    final stateTag = _manualStateTag(response, required: true)!;
    final grant = ManualCaptureGrant.fromJson(
      response.payload,
      'manualCaptureGrant',
      expectedCaptureId: current.capture.id,
    );
    if (stateTag == current.stateTag || grant.capture.state != 'active') {
      throw const ControlContractException(
        'rotated Manual Capture grant is inconsistent',
      );
    }
    return ManualCaptureGrantStateTag(grant: grant, stateTag: stateTag);
  }

  @override
  Future<ManualCaptureStateTag> manualCaptureState(
    String manualCaptureId,
  ) async {
    await _ensureFreshSession();
    final response = await _send(
      'GET',
      '/api/v1/manual-captures/${Uri.encodeComponent(manualCaptureId)}',
      token: _session.readToken,
      expectedStatus: 200,
    );
    final stateTag = _manualStateTag(response, required: true)!;
    return ManualCaptureStateTag.fromJson(response.payload, stateTag);
  }

  @override
  Future<void> revokeManualCapture({
    required String manualCaptureId,
    required String stateTag,
  }) async {
    await _ensureFreshSession();
    if (!_validManualStateTag(stateTag)) {
      throw const ControlContractException(
        'manual capture state tag is invalid',
      );
    }
    final response = await _send(
      'POST',
      '/api/v1/manual-captures/${Uri.encodeComponent(manualCaptureId)}/actions/revoke',
      token: _session.writeToken,
      expectedStatus: 204,
      headers: {HttpHeaders.ifMatchHeader: stateTag},
    );
    if (response.payload != null ||
        response.headers.value(HttpHeaders.cacheControlHeader) != 'no-store' ||
        response.headers.value(HttpHeaders.etagHeader) != null) {
      throw const ControlContractException(
        'manual capture revoke response is invalid',
      );
    }
  }

  Future<Object?> _read(String path) async {
    await _ensureFreshSession();
    return (await _send(
      'GET',
      path,
      token: _session.readToken,
      expectedStatus: 200,
    )).payload;
  }

  Future<Object?> _mutation(
    String method,
    String path, {
    required int expectedRevision,
    Object? body,
    int expectedStatus = 200,
    Duration responseTimeout = _requestTimeout,
  }) async {
    await _ensureFreshSession();
    return (await _send(
      method,
      path,
      token: _session.writeToken,
      expectedStatus: expectedStatus,
      body: body,
      responseTimeout: responseTimeout,
      headers: {
        HttpHeaders.ifMatchHeader: '$expectedRevision',
        'Idempotency-Key': _newCapability(),
      },
    )).payload;
  }

  Future<void> _inspectSession() async {
    final response = await _send(
      'GET',
      '/api/v1/auth/sessions/current',
      token: _session.readToken,
      expectedStatus: 200,
      skipRenewal: true,
    );
    final value = requireObject(response.payload, 'sessionState');
    if (requireString(value, 'schema', 'sessionState') !=
        'vibermate-app-session-state-v1') {
      throw const ControlContractException(
        'session state schema is unsupported',
      );
    }
    _sessionRevision = requireInteger(
      value,
      'revision',
      'sessionState',
      minimum: 1,
    );
    final expiresAt = requireTimestamp(value, 'expiresAt', 'sessionState');
    _session = DesktopSession(
      baseUrl: _session.baseUrl,
      readToken: _session.readToken,
      writeToken: _session.writeToken,
      instanceId: _session.instanceId,
      expiresAt: expiresAt,
    );
    _renewAt = _renewalTime(DateTime.now().toUtc(), expiresAt);
  }

  Future<void> _ensureFreshSession() async {
    _requireOpen();
    final renewAt = _renewAt;
    if (renewAt == null || DateTime.now().toUtc().isBefore(renewAt)) return;
    final existing = _renewal;
    if (existing != null) return existing;
    final renewal = _renewSession();
    _renewal = renewal;
    try {
      await renewal;
    } finally {
      if (identical(_renewal, renewal)) _renewal = null;
    }
  }

  Future<void> _renewSession() async {
    final previous = _session;
    final response = await _send(
      'POST',
      '/api/v1/auth/sessions/refresh',
      token: previous.writeToken,
      expectedStatus: 200,
      headers: {
        HttpHeaders.ifMatchHeader: '$_sessionRevision',
        'Idempotency-Key': _newCapability(),
      },
      skipRenewal: true,
    );
    final value = requireObject(response.payload, 'sessionRotation');
    if (requireString(value, 'schema', 'sessionRotation') !=
            'vibermate-app-session-rotation-v1' ||
        requireInteger(value, 'revision', 'sessionRotation', minimum: 2) !=
            _sessionRevision + 1) {
      throw const ControlContractException(
        'session rotation schema is invalid',
      );
    }
    final rotated = DesktopSession(
      baseUrl: previous.baseUrl,
      readToken: requireString(value, 'readToken', 'sessionRotation'),
      writeToken: requireString(value, 'writeToken', 'sessionRotation'),
      instanceId: previous.instanceId,
      expiresAt: requireTimestamp(value, 'expiresAt', 'sessionRotation'),
    );
    if ({
          previous.readToken,
          previous.writeToken,
          rotated.readToken,
          rotated.writeToken,
        }.length !=
        4) {
      throw const ControlContractException(
        'session rotation reused a capability',
      );
    }
    _sessionRevision += 1;
    _session = rotated;
    _renewAt = _renewalTime(DateTime.now().toUtc(), rotated.expiresAt);
  }

  Future<_WireResponse> _send(
    String method,
    String path, {
    required String token,
    required int expectedStatus,
    Object? body,
    Map<String, String> headers = const {},
    bool skipRenewal = false,
    Duration responseTimeout = _requestTimeout,
    int maximumResponseBytes = _maximumResponseBytes,
  }) async {
    _requireOpen();
    if (!skipRenewal && path != '/api/v1/auth/sessions/current') {
      // Callers normally refresh before entering _send. This guard prevents a
      // new path from silently bypassing the session lifetime policy.
      final renewAt = _renewAt;
      if (renewAt != null && !DateTime.now().toUtc().isBefore(renewAt)) {
        throw const ControlContractException(
          'request used a session due for renewal',
        );
      }
    }
    final destination = _resolve(path);
    final request = await _client
        .openUrl(method, destination)
        .timeout(responseTimeout);
    request.followRedirects = false;
    request.headers
      ..set(
        HttpHeaders.acceptHeader,
        'application/json, application/problem+json',
      )
      ..set(HttpHeaders.authorizationHeader, 'Bearer $token')
      ..set('Origin', _origin)
      ..set('Sec-Fetch-Site', 'cross-site')
      ..set('Sec-Fetch-Mode', 'cors')
      ..set('Sec-Fetch-Dest', 'empty');
    for (final entry in headers.entries) {
      request.headers.set(entry.key, entry.value);
    }
    if (body != null) {
      final encoded = utf8.encode(jsonEncode(body));
      if (encoded.length > 1024 * 1024) {
        throw const ControlContractException('control request exceeded 1 MiB');
      }
      request.headers.contentType = ContentType.json;
      request.add(encoded);
    }
    final response = await request.close().timeout(responseTimeout);
    final bytes = await _readBounded(
      response,
      responseTimeout,
      maximumResponseBytes,
    );
    final payload = bytes.isEmpty ? null : _decodeJson(bytes);
    if (response.statusCode != expectedStatus) {
      throw _problem(response.statusCode, payload);
    }
    if (expectedStatus == 204 && bytes.isNotEmpty) {
      throw const ControlContractException('204 response contained a body');
    }
    return _WireResponse(response.headers, payload);
  }

  Uri _resolve(String path) {
    final parsed = Uri.tryParse(path);
    if (parsed == null ||
        parsed.hasScheme ||
        !parsed.path.startsWith('/api/v1/')) {
      throw const ControlContractException('control path escaped /api/v1');
    }
    final destination = _session.baseUrl.resolveUri(parsed);
    if (destination.scheme != _session.baseUrl.scheme ||
        destination.host != _session.baseUrl.host ||
        destination.port != _session.baseUrl.port) {
      throw const ControlContractException(
        'control request escaped bootstrap origin',
      );
    }
    return destination;
  }

  Future<Uint8List> _readBounded(
    HttpClientResponse response,
    Duration responseTimeout,
    int maximumResponseBytes,
  ) async {
    final declared = response.contentLength;
    if (declared > maximumResponseBytes) {
      throw ControlContractException(
        'control response exceeded ${maximumResponseBytes ~/ (1024 * 1024)} MiB',
      );
    }
    final builder = BytesBuilder(copy: false);
    var length = 0;
    await for (final chunk in response.timeout(responseTimeout)) {
      length += chunk.length;
      if (length > maximumResponseBytes) {
        throw ControlContractException(
          'control response exceeded ${maximumResponseBytes ~/ (1024 * 1024)} MiB',
        );
      }
      builder.add(chunk);
    }
    return builder.takeBytes();
  }

  Object? _decodeJson(Uint8List bytes) {
    try {
      return jsonDecode(utf8.decode(bytes, allowMalformed: false));
    } on FormatException {
      throw const ControlContractException(
        'control response was not valid JSON',
      );
    }
  }

  ControlProblem _problem(int status, Object? payload) {
    try {
      return ControlProblem.fromJson(payload, status: status);
    } on ControlContractException {
      return ControlProblem(
        status: status,
        reasonCode: 'invalid_control_response',
        messageKey: 'error.invalid_control_response',
      );
    }
  }

  List<T> _page<T>(
    Object? payload,
    String path,
    T Function(Object? item, String path) decode,
  ) {
    final value = requireObject(payload, path);
    final items = requireList(value['items'], '$path.items');
    return items.indexed
        .map((entry) => decode(entry.$2, '$path.items[${entry.$1}]'))
        .toList(growable: false);
  }

  static void _validatePageRequest(String? cursor, int limit) {
    if (limit < 1 ||
        limit > 200 ||
        (cursor != null &&
            (cursor.isEmpty ||
                cursor.length > 512 ||
                RegExp(r'\s').hasMatch(cursor)))) {
      throw const ControlContractException('page request is invalid');
    }
  }

  static bool _validResourceId(String value) =>
      RegExp(r'^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,127}$').hasMatch(value);

  static bool _validConversationId(String value) =>
      RegExp(r'^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,511}$').hasMatch(value);

  static bool _validRawEvidenceIdentity(String value) =>
      value.isNotEmpty &&
      utf8.encode(value).length <= 512 &&
      !RegExp(r'[\u0000\r\n\t]').hasMatch(value);

  static void _validateWorkspaceIdentity(String value, String label) {
    if (!RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(value)) {
      throw ControlContractException('$label identity is invalid');
    }
    try {
      final decoded = base64Url.decode('$value=');
      if (decoded.length != 32 ||
          base64Url.encode(decoded).replaceAll('=', '') != value) {
        throw ControlContractException('$label identity is invalid');
      }
    } on FormatException {
      throw ControlContractException('$label identity is invalid');
    }
  }

  static String _workspaceDefaultPath(String machineId, String workspaceId) =>
      '/api/v1/machines/${Uri.encodeComponent(machineId)}/workspaces/'
      '${Uri.encodeComponent(workspaceId)}/environment-default';

  static bool _validDisplayLabel(String value) =>
      value.isNotEmpty &&
      value.trim() == value &&
      utf8.encode(value).length <= 256 &&
      !RegExp(r'[\u0000-\u001f\u007f]').hasMatch(value);

  static bool _validSecret(String value) =>
      value.isNotEmpty &&
      utf8.encode(value).length <= 64 * 1024 &&
      !value.contains(RegExp(r'[\u0000\r\n]'));

  static bool _validManualStateTag(String value) =>
      RegExp(r'^"mc_[A-Za-z0-9_-]{43}"$').hasMatch(value);

  static String? _manualStateTag(
    _WireResponse response, {
    required bool required,
  }) {
    final stateTag = response.headers.value(HttpHeaders.etagHeader);
    if (response.headers.value(HttpHeaders.cacheControlHeader) != 'no-store' ||
        (required && (stateTag == null || !_validManualStateTag(stateTag))) ||
        (!required && stateTag != null)) {
      throw const ControlContractException(
        'Manual Capture response headers are invalid',
      );
    }
    return stateTag;
  }

  static DateTime _renewalTime(DateTime now, DateTime expiresAt) {
    final lifetime = expiresAt.difference(now);
    final oneFifth = Duration(milliseconds: lifetime.inMilliseconds ~/ 5);
    final lead = oneFifth < const Duration(seconds: 1)
        ? const Duration(seconds: 1)
        : oneFifth > const Duration(minutes: 5)
        ? const Duration(minutes: 5)
        : oneFifth;
    final candidate = expiresAt.subtract(lead);
    return candidate.isBefore(now) ? now : candidate;
  }

  static String _newCapability() {
    final random = Random.secure();
    final bytes = List<int>.generate(24, (_) => random.nextInt(256));
    return base64Url.encode(bytes).replaceAll('=', '');
  }

  void _requireOpen() {
    if (_closed) throw StateError('Control API is closed');
  }

  @override
  Future<void> close() async {
    if (_closed) return;
    _closed = true;
    _session = DesktopSession(
      baseUrl: _session.baseUrl,
      readToken: '___________________________________________',
      writeToken: '-------------------------------------------',
      instanceId: _session.instanceId,
      expiresAt: DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
    );
    _client.close(force: true);
  }
}

final class _WireResponse {
  const _WireResponse(this.headers, this.payload);

  final HttpHeaders headers;
  final Object? payload;
}

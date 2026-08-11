import 'dart:convert';

typedef JsonObject = Map<String, Object?>;

final class ControlContractException implements Exception {
  const ControlContractException(this.message);

  final String message;

  @override
  String toString() => 'Control contract error: $message';
}

JsonObject requireObject(Object? value, String path) {
  if (value is Map<String, Object?>) return value;
  if (value is Map) {
    return value.map((key, item) => MapEntry(key.toString(), item));
  }
  throw ControlContractException('$path must be an object');
}

void requireFields(
  JsonObject value,
  String path, {
  required Set<String> required,
  Set<String> optional = const {},
}) {
  final allowed = {...required, ...optional};
  if (!required.every(value.containsKey) ||
      value.keys.any((key) => !allowed.contains(key))) {
    throw ControlContractException('$path fields do not match the contract');
  }
}

List<Object?> requireList(Object? value, String path) {
  if (value is List<Object?>) return value;
  if (value is List) return value.cast<Object?>();
  throw ControlContractException('$path must be an array');
}

String requireString(JsonObject value, String key, String path) {
  final field = value[key];
  if (field is String && field.isNotEmpty) return field;
  throw ControlContractException('$path.$key must be a non-empty string');
}

String requireStringValue(JsonObject value, String key, String path) {
  final field = value[key];
  if (field is String) return field;
  throw ControlContractException('$path.$key must be a string');
}

String? optionalString(JsonObject value, String key, String path) {
  final field = value[key];
  if (field == null) return null;
  if (field is String && field.isNotEmpty) return field;
  throw ControlContractException('$path.$key must be a non-empty string');
}

int requireInteger(
  JsonObject value,
  String key,
  String path, {
  int minimum = 0,
}) {
  final field = value[key];
  if (field is int && field >= minimum) return field;
  throw ControlContractException('$path.$key must be an integer >= $minimum');
}

int? optionalInteger(
  JsonObject value,
  String key,
  String path, {
  int minimum = 0,
}) {
  final field = value[key];
  if (field == null) return null;
  if (field is int && field >= minimum) return field;
  throw ControlContractException('$path.$key must be an integer >= $minimum');
}

bool requireBoolean(JsonObject value, String key, String path) {
  final field = value[key];
  if (field is bool) return field;
  throw ControlContractException('$path.$key must be a boolean');
}

List<String> requireStringList(JsonObject value, String key, String path) {
  return requireList(value[key], '$path.$key').indexed
      .map((entry) {
        final (index, item) = entry;
        if (item is String && item.isNotEmpty) return item;
        throw ControlContractException(
          '$path.$key[$index] must be a non-empty string',
        );
      })
      .toList(growable: false);
}

DateTime requireTimestamp(JsonObject value, String key, String path) {
  final raw = requireString(value, key, path);
  final parsed = DateTime.tryParse(raw);
  if (parsed == null || !parsed.isUtc) {
    throw ControlContractException('$path.$key must be a UTC timestamp');
  }
  return parsed;
}

DateTime? optionalTimestamp(JsonObject value, String key, String path) {
  final raw = optionalString(value, key, path);
  if (raw == null) return null;
  final parsed = DateTime.tryParse(raw);
  if (parsed == null || !parsed.isUtc) {
    throw ControlContractException('$path.$key must be a UTC timestamp');
  }
  return parsed;
}

bool _isCanonicalHttpsOrigin(Uri value) =>
    value.scheme == 'https' &&
    value.host.isNotEmpty &&
    value.userInfo.isEmpty &&
    !value.hasQuery &&
    !value.hasFragment &&
    (value.path.isEmpty || value.path == '/') &&
    value.toString() == value.origin;

final _resourceIdPattern = RegExp(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$');
final _digestPattern = RegExp(r'^[0-9a-f]{64}$');

bool _validWorkspaceIdentity(String value) {
  if (!RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(value)) return false;
  try {
    final decoded = base64Url.decode('$value=');
    return decoded.length == 32 &&
        base64Url.encode(decoded).replaceAll('=', '') == value;
  } on FormatException {
    return false;
  }
}

bool _validDisplayLabel(String value, {int maximumBytes = 256}) =>
    value.isNotEmpty &&
    value.trim() == value &&
    utf8.encode(value).length <= maximumBytes &&
    !_containsControlCharacter(value);

bool _validCleanAbsolutePath(String value) {
  if (!value.startsWith('/') ||
      utf8.encode(value).length > 4096 ||
      !_validDisplayLabel(value, maximumBytes: 4096)) {
    return false;
  }
  if (value == '/') return true;
  if (value.endsWith('/')) return false;
  return value
      .substring(1)
      .split('/')
      .every(
        (segment) => segment.isNotEmpty && segment != '.' && segment != '..',
      );
}

bool _containsControlCharacter(String value) => value.runes.any(
  (character) => character < 0x20 || character >= 0x7f && character <= 0x9f,
);

String _requireResourceId(
  JsonObject value,
  String key,
  String path, {
  bool allowEmpty = false,
}) {
  final field = requireStringValue(value, key, path);
  if ((allowEmpty && field.isEmpty) || _resourceIdPattern.hasMatch(field)) {
    return field;
  }
  throw ControlContractException('$path.$key must be a resource ID');
}

String _requireDigest(JsonObject value, String key, String path) {
  final field = requireString(value, key, path);
  if (_digestPattern.hasMatch(field)) return field;
  throw ControlContractException('$path.$key must be a lowercase SHA-256');
}

Map<String, int> _requireRevisionMap(
  Object? json,
  String path, {
  bool allowEmpty = true,
}) {
  final value = requireObject(json, path);
  if (!allowEmpty && value.isEmpty) {
    throw ControlContractException('$path must not be empty');
  }
  final result = <String, int>{};
  for (final entry in value.entries) {
    if (!_resourceIdPattern.hasMatch(entry.key) ||
        entry.value is! int ||
        (entry.value! as int) < 1) {
      throw ControlContractException('$path contains an invalid revision');
    }
    result[entry.key] = entry.value! as int;
  }
  return Map.unmodifiable(result);
}

final class DesktopSession {
  const DesktopSession({
    required this.baseUrl,
    required this.readToken,
    required this.writeToken,
    required this.instanceId,
    required this.expiresAt,
  });

  factory DesktopSession.fromJson(Object? json) {
    final value = requireObject(json, 'session');
    if (requireString(value, 'schema', 'session') !=
        'vibermate-app-session-v1') {
      throw const ControlContractException('session.schema is unsupported');
    }
    final baseUrl = Uri.tryParse(requireString(value, 'baseUrl', 'session'));
    if (baseUrl == null ||
        baseUrl.scheme != 'http' ||
        baseUrl.host != '127.0.0.1' ||
        !baseUrl.hasPort ||
        baseUrl.path.isNotEmpty ||
        baseUrl.hasQuery ||
        baseUrl.hasFragment) {
      throw const ControlContractException(
        'session.baseUrl must be literal loopback HTTP',
      );
    }
    final readToken = requireString(value, 'readToken', 'session');
    final writeToken = requireString(value, 'writeToken', 'session');
    if (!_validCapability(readToken) ||
        !_validCapability(writeToken) ||
        readToken == writeToken) {
      throw const ControlContractException('session capabilities are invalid');
    }
    final expiresAt = requireTimestamp(value, 'expiresAt', 'session');
    if (!expiresAt.isAfter(DateTime.now().toUtc())) {
      throw const ControlContractException('session has already expired');
    }
    return DesktopSession(
      baseUrl: baseUrl,
      readToken: readToken,
      writeToken: writeToken,
      instanceId: requireString(value, 'instanceId', 'session'),
      expiresAt: expiresAt,
    );
  }

  final Uri baseUrl;
  final String readToken;
  final String writeToken;
  final String instanceId;
  final DateTime expiresAt;

  static bool _validCapability(String value) =>
      RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(value);
}

final class OfflineHoldSnapshot {
  const OfflineHoldSnapshot({
    required this.state,
    required this.revision,
    required this.since,
    required this.activeActions,
    required this.enteringActions,
    required this.activeEgress,
    required this.queuedRequests,
    required this.heldBytes,
    required this.safeToDisconnect,
    required this.activeByKind,
    required this.queuedByKind,
    required this.lastProbeReason,
  });

  factory OfflineHoldSnapshot.fromJson(
    Object? json, {
    String path = 'offlineHold',
    int? afterRevision,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'state',
        'revision',
        'since',
        'activeActions',
        'enteringActions',
        'activeEgress',
        'queuedRequests',
        'heldBytes',
        'safeToDisconnect',
        'activeByKind',
        'queuedByKind',
      },
      optional: const {'lastProbeReason'},
    );
    final state = requireString(value, 'state', path);
    if (!_states.contains(state)) {
      throw ControlContractException('$path.state is unsupported');
    }
    final revision = requireInteger(value, 'revision', path);
    if (afterRevision != null && revision <= afterRevision) {
      throw ControlContractException('$path.revision did not advance');
    }
    final activeActions = requireInteger(value, 'activeActions', path);
    final enteringActions = requireInteger(value, 'enteringActions', path);
    final activeEgress = requireInteger(value, 'activeEgress', path);
    final queuedRequests = requireInteger(value, 'queuedRequests', path);
    final heldBytes = requireInteger(value, 'heldBytes', path);
    if (heldBytes > 0x7fffffffffffffff || enteringActions > activeActions) {
      throw ControlContractException('$path counters are inconsistent');
    }
    final activeByKind = _countMap(value['activeByKind'], '$path.activeByKind');
    final queuedByKind = _countMap(value['queuedByKind'], '$path.queuedByKind');
    if (_sum(activeByKind) != activeEgress ||
        _sum(queuedByKind) != queuedRequests) {
      throw ControlContractException('$path kind totals are inconsistent');
    }
    final safeToDisconnect = requireBoolean(value, 'safeToDisconnect', path);
    final expectedSafe =
        state == 'held' && activeEgress == 0 && enteringActions == 0;
    if (safeToDisconnect != expectedSafe ||
        (state == 'online' && (queuedRequests != 0 || heldBytes != 0))) {
      throw ControlContractException('$path safety boundary is inconsistent');
    }
    final lastProbeReason = optionalString(value, 'lastProbeReason', path);
    if (lastProbeReason != null && !_probeReasons.contains(lastProbeReason)) {
      throw ControlContractException('$path.lastProbeReason is unsupported');
    }
    return OfflineHoldSnapshot(
      state: state,
      revision: revision,
      since: requireTimestamp(value, 'since', path),
      activeActions: activeActions,
      enteringActions: enteringActions,
      activeEgress: activeEgress,
      queuedRequests: queuedRequests,
      heldBytes: heldBytes,
      safeToDisconnect: safeToDisconnect,
      activeByKind: activeByKind,
      queuedByKind: queuedByKind,
      lastProbeReason: lastProbeReason,
    );
  }

  final String state;
  final int revision;
  final DateTime since;
  final int activeActions;
  final int enteringActions;
  final int activeEgress;
  final int queuedRequests;
  final int heldBytes;
  final bool safeToDisconnect;
  final Map<String, int> activeByKind;
  final Map<String, int> queuedByKind;
  final String? lastProbeReason;

  bool get canEnter => state == 'online';
  bool get canResume => state == 'held';
  bool get transitioning =>
      const {'entering', 'probing', 'releasing'}.contains(state);

  static const _states = {
    'unbound',
    'online',
    'entering',
    'held',
    'probing',
    'releasing',
    'stopping',
  };
  static const _egressKinds = {
    'provider',
    'opaque',
    'auxiliary',
    'plugin',
    'update',
    'blind_tunnel',
  };
  static const _probeReasons = {
    'transport_unavailable',
    'tls_rejected',
    'canceled',
    'probe_failed',
  };

  static Map<String, int> _countMap(Object? json, String path) {
    final value = requireObject(json, path);
    final result = <String, int>{};
    for (final entry in value.entries) {
      if (!_egressKinds.contains(entry.key) ||
          entry.value is! int ||
          (entry.value! as int) < 1) {
        throw ControlContractException('$path contains an invalid count');
      }
      result[entry.key] = entry.value! as int;
    }
    return Map.unmodifiable(result);
  }

  static int _sum(Map<String, int> value) =>
      value.values.fold(0, (sum, count) => sum + count);
}

final class RuntimeStatus {
  const RuntimeStatus({
    required this.ready,
    required this.state,
    required this.host,
    required this.schemaRevision,
    required this.storage,
    required this.environmentProjection,
    required this.unavailableEnvironments,
    required this.offlineHold,
    required this.instanceId,
    required this.startedAt,
    required this.stoppedAt,
    required this.stopReasonCode,
  });

  factory RuntimeStatus.fromJson(
    Object? json, {
    required String expectedInstanceId,
  }) {
    final value = requireObject(json, 'status');
    requireFields(
      value,
      'status',
      required: const {
        'generation',
        'ready',
        'apiVersion',
        'statusKey',
        'runtime',
      },
    );
    if (requireString(value, 'apiVersion', 'status') != 'v1') {
      throw const ControlContractException('status.apiVersion is unsupported');
    }
    final runtime = requireObject(value['runtime'], 'status.runtime');
    requireFields(
      runtime,
      'status.runtime',
      required: const {
        'state',
        'instanceId',
        'host',
        'schemaRevision',
        'storage',
        'environmentProjection',
        'offlineHold',
        'startedAt',
      },
      optional: const {'stoppedAt', 'stopReasonCode'},
    );
    final instanceId = requireString(runtime, 'instanceId', 'status.runtime');
    if (instanceId != expectedInstanceId ||
        requireString(value, 'generation', 'status') != expectedInstanceId) {
      throw const ControlContractException(
        'status instance does not match session',
      );
    }
    final state = requireString(runtime, 'state', 'status.runtime');
    if (!const {
      'starting',
      'initialized',
      'degraded',
      'stopping',
      'stopped',
      'stop_failed',
    }.contains(state)) {
      throw const ControlContractException('status.runtime.state is invalid');
    }
    if (requireString(value, 'statusKey', 'status') != 'runtime.state.$state') {
      throw const ControlContractException('status.statusKey is inconsistent');
    }
    final host = requireString(runtime, 'host', 'status.runtime');
    final storage = requireString(runtime, 'storage', 'status.runtime');
    if (host != 'desktop' ||
        !const {'healthy', 'unavailable'}.contains(storage)) {
      throw const ControlContractException('status runtime host is invalid');
    }
    final projection = requireObject(
      runtime['environmentProjection'],
      'status.runtime.environmentProjection',
    );
    requireFields(
      projection,
      'status.runtime.environmentProjection',
      required: const {'state', 'unavailableEnvironments'},
    );
    final projectionState = requireString(
      projection,
      'state',
      'status.runtime.environmentProjection',
    );
    if (!const {
      'unrestored',
      'healthy',
      'unavailable',
    }.contains(projectionState)) {
      throw const ControlContractException(
        'status environment projection is invalid',
      );
    }
    final unavailableRaw = projection['unavailableEnvironments'];
    final unavailableEnvironments = unavailableRaw == null
        ? null
        : requireStringList(
            projection,
            'unavailableEnvironments',
            'status.runtime.environmentProjection',
          );
    if ((projectionState == 'unavailable') !=
            (unavailableEnvironments != null &&
                unavailableEnvironments.isNotEmpty) ||
        (unavailableEnvironments != null &&
            (unavailableEnvironments.toSet().length !=
                    unavailableEnvironments.length ||
                unavailableEnvironments.any(
                  (id) => !_resourceIdPattern.hasMatch(id),
                )))) {
      throw const ControlContractException(
        'status unavailable Environments are inconsistent',
      );
    }
    final stoppedAt = optionalTimestamp(runtime, 'stoppedAt', 'status.runtime');
    final stopReasonCode = optionalString(
      runtime,
      'stopReasonCode',
      'status.runtime',
    );
    if ((state == 'stopped' && (stoppedAt == null || stopReasonCode != null)) ||
        (state == 'stop_failed' &&
            (stoppedAt != null || stopReasonCode != 'shutdown_failed')) ||
        (!const {'stopped', 'stop_failed'}.contains(state) &&
            (stoppedAt != null || stopReasonCode != null))) {
      throw const ControlContractException(
        'status runtime terminal evidence is inconsistent',
      );
    }
    return RuntimeStatus(
      ready: requireBoolean(value, 'ready', 'status'),
      state: state,
      host: host,
      schemaRevision: requireInteger(
        runtime,
        'schemaRevision',
        'status.runtime',
      ),
      storage: storage,
      environmentProjection: projectionState,
      unavailableEnvironments: unavailableEnvironments == null
          ? null
          : List.unmodifiable(unavailableEnvironments),
      offlineHold: OfflineHoldSnapshot.fromJson(
        runtime['offlineHold'],
        path: 'status.runtime.offlineHold',
      ),
      instanceId: instanceId,
      startedAt: requireTimestamp(runtime, 'startedAt', 'status.runtime'),
      stoppedAt: stoppedAt,
      stopReasonCode: stopReasonCode,
    );
  }

  final bool ready;
  final String state;
  final String host;
  final int schemaRevision;
  final String storage;
  final String environmentProjection;
  final List<String>? unavailableEnvironments;
  final OfflineHoldSnapshot offlineHold;
  final String instanceId;
  final DateTime startedAt;
  final DateTime? stoppedAt;
  final String? stopReasonCode;

  RuntimeStatus withOfflineHold(OfflineHoldSnapshot value) => RuntimeStatus(
    ready: ready,
    state: state,
    host: host,
    schemaRevision: schemaRevision,
    storage: storage,
    environmentProjection: environmentProjection,
    unavailableEnvironments: unavailableEnvironments,
    offlineHold: value,
    instanceId: instanceId,
    startedAt: startedAt,
    stoppedAt: stoppedAt,
    stopReasonCode: stopReasonCode,
  );

  bool get healthy => ready && state == 'initialized' && storage == 'healthy';
}

final class UpstreamEndpoint {
  const UpstreamEndpoint({
    required this.id,
    required this.displayName,
    required this.origin,
    required this.realmId,
    required this.backendProtocols,
    required this.capabilities,
    required this.accountKinds,
    required this.state,
    required this.revision,
  });

  factory UpstreamEndpoint.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'displayName',
        'origin',
        'realmId',
        'backendProtocols',
        'capabilities',
        'accountKinds',
        'state',
        'revision',
      },
    );
    final origin = Uri.tryParse(requireString(value, 'origin', path));
    if (origin == null || !_isCanonicalHttpsOrigin(origin)) {
      throw ControlContractException(
        '$path.origin must be a canonical HTTPS provider origin',
      );
    }
    return UpstreamEndpoint(
      id: requireString(value, 'id', path),
      displayName: requireString(value, 'displayName', path),
      origin: origin,
      realmId: requireString(value, 'realmId', path),
      backendProtocols: requireStringList(value, 'backendProtocols', path),
      capabilities: requireStringList(value, 'capabilities', path),
      accountKinds: requireStringList(value, 'accountKinds', path),
      state: requireString(value, 'state', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
    );
  }

  final String id;
  final String displayName;
  final Uri origin;
  final String realmId;
  final List<String> backendProtocols;
  final List<String> capabilities;
  final List<String> accountKinds;
  final String state;
  final int revision;
}

final class ProviderAccount {
  const ProviderAccount({
    required this.id,
    required this.displayName,
    required this.upstreamEndpointId,
    required this.kind,
    required this.realmId,
    required this.state,
    required this.revision,
    required this.credentialState,
    required this.credentialEpoch,
  });

  factory ProviderAccount.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'displayName',
        'upstreamEndpointId',
        'kind',
        'realmId',
        'state',
        'revision',
        'credentialState',
        'credentialEpoch',
      },
    );
    final credentialState = requireString(value, 'credentialState', path);
    final credentialEpoch = requireInteger(value, 'credentialEpoch', path);
    if (!const {
          'ready',
          'disabled',
          'credential_missing',
          'credential_unavailable',
        }.contains(credentialState) ||
        (credentialState == 'ready' && credentialEpoch == 0) ||
        ((credentialState == 'disabled' ||
                credentialState == 'credential_missing') &&
            credentialEpoch != 0)) {
      throw ControlContractException(
        '$path credential state and epoch are inconsistent',
      );
    }
    return ProviderAccount(
      id: requireString(value, 'id', path),
      displayName: requireString(value, 'displayName', path),
      upstreamEndpointId: requireString(value, 'upstreamEndpointId', path),
      kind: requireString(value, 'kind', path),
      realmId: requireString(value, 'realmId', path),
      state: requireString(value, 'state', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      credentialState: credentialState,
      credentialEpoch: credentialEpoch,
    );
  }

  final String id;
  final String displayName;
  final String upstreamEndpointId;
  final String kind;
  final String realmId;
  final String state;
  final int revision;
  final String credentialState;
  final int credentialEpoch;

  bool get usable => state == 'active' && credentialState == 'ready';
}

final class ProviderAccountReference {
  const ProviderAccountReference({
    required this.environmentId,
    required this.environmentName,
    required this.environmentRevision,
    required this.routeId,
    required this.routeRevision,
  });

  factory ProviderAccountReference.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'environmentId',
        'environmentName',
        'environmentRevision',
        'routeId',
        'routeRevision',
      },
    );
    return ProviderAccountReference(
      environmentId: requireString(value, 'environmentId', path),
      environmentName: requireString(value, 'environmentName', path),
      environmentRevision: requireInteger(
        value,
        'environmentRevision',
        path,
        minimum: 1,
      ),
      routeId: requireString(value, 'routeId', path),
      routeRevision: requireInteger(value, 'routeRevision', path, minimum: 1),
    );
  }

  final String environmentId;
  final String environmentName;
  final int environmentRevision;
  final String routeId;
  final int routeRevision;
}

final class ProviderAccountDeleteResult {
  const ProviderAccountDeleteResult({
    required this.deleted,
    required this.referenceCount,
    required this.references,
  });

  factory ProviderAccountDeleteResult.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'deleted', 'referenceCount', 'references'},
    );
    final deleted = requireBoolean(value, 'deleted', path);
    final referenceCount = requireInteger(value, 'referenceCount', path);
    final rawReferences = requireList(value['references'], '$path.references');
    final references = rawReferences.indexed
        .map(
          (entry) => ProviderAccountReference.fromJson(
            entry.$2,
            '$path.references[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    if (referenceCount < references.length ||
        deleted != (referenceCount == 0)) {
      throw ControlContractException('$path deletion evidence is inconsistent');
    }
    return ProviderAccountDeleteResult(
      deleted: deleted,
      referenceCount: referenceCount,
      references: references,
    );
  }

  final bool deleted;
  final int referenceCount;
  final List<ProviderAccountReference> references;
}

final class EnvironmentPluginBinding {
  const EnvironmentPluginBinding({
    required this.id,
    required this.revision,
    required this.pluginId,
  });

  factory EnvironmentPluginBinding.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'id', 'revision', 'pluginId'});
    return EnvironmentPluginBinding(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      pluginId: _requireResourceId(value, 'pluginId', path),
    );
  }

  final String id;
  final int revision;
  final String pluginId;

  JsonObject toJson() => {'id': id, 'revision': revision, 'pluginId': pluginId};
}

final class EnvironmentBudgetPolicy {
  const EnvironmentBudgetPolicy({required this.id, required this.revision});

  factory EnvironmentBudgetPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'id', 'revision'});
    final id = _requireResourceId(value, 'id', path, allowEmpty: true);
    final revision = requireInteger(value, 'revision', path);
    if ((id.isEmpty) != (revision == 0)) {
      throw ControlContractException('$path budget authority is incomplete');
    }
    return EnvironmentBudgetPolicy(id: id, revision: revision);
  }

  final String id;
  final int revision;

  JsonObject toJson() => {'id': id, 'revision': revision};
}

final class EnvironmentEgressPolicy {
  const EnvironmentEgressPolicy({
    required this.id,
    required this.revision,
    required this.mode,
  });

  factory EnvironmentEgressPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'id', 'revision', 'mode'});
    final id = _requireResourceId(value, 'id', path, allowEmpty: true);
    final revision = requireInteger(value, 'revision', path);
    final mode = requireStringValue(value, 'mode', path);
    if ((id.isEmpty) != (revision == 0) ||
        (id.isEmpty && mode.isNotEmpty) ||
        mode.length > 128) {
      throw ControlContractException('$path egress authority is inconsistent');
    }
    return EnvironmentEgressPolicy(id: id, revision: revision, mode: mode);
  }

  final String id;
  final int revision;
  final String mode;

  JsonObject toJson() => {'id': id, 'revision': revision, 'mode': mode};
}

final class EnvironmentContentRecordingPolicy {
  const EnvironmentContentRecordingPolicy({
    required this.mode,
    required this.retentionDays,
  });

  factory EnvironmentContentRecordingPolicy.fromJson(
    Object? json,
    String path,
  ) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'mode', 'retentionDays'});
    final mode = requireString(value, 'mode', path);
    final retentionDays = requireInteger(value, 'retentionDays', path);
    if (!const {'full', 'metadata_only', 'off'}.contains(mode) ||
        (mode == 'off' && retentionDays != 0) ||
        (mode != 'off' && (retentionDays < 1 || retentionDays > 3650))) {
      throw ControlContractException('$path recording policy is invalid');
    }
    return EnvironmentContentRecordingPolicy(
      mode: mode,
      retentionDays: retentionDays,
    );
  }

  final String mode;
  final int retentionDays;

  JsonObject toJson() => {'mode': mode, 'retentionDays': retentionDays};
}

final class EnvironmentPolicySet {
  const EnvironmentPolicySet({required this.toolMode});

  factory EnvironmentPolicySet.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'toolMode'});
    final toolMode = requireString(value, 'toolMode', path);
    if (!const {'observe', 'review', 'strict'}.contains(toolMode)) {
      throw ControlContractException('$path tool policy is invalid');
    }
    return EnvironmentPolicySet(toolMode: toolMode);
  }

  final String toolMode;

  JsonObject toJson() => {'toolMode': toolMode};
}

final class EnvironmentClientAdapterPolicy {
  const EnvironmentClientAdapterPolicy({
    required this.id,
    required this.revision,
  });

  factory EnvironmentClientAdapterPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'id', 'revision'});
    return EnvironmentClientAdapterPolicy(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
    );
  }

  final String id;
  final int revision;

  JsonObject toJson() => {'id': id, 'revision': revision};
}

final class EnvironmentRouteSet {
  const EnvironmentRouteSet({
    required this.id,
    required this.revision,
    required this.candidateRouteIds,
  });

  factory EnvironmentRouteSet.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'revision', 'candidateRouteIds'},
    );
    final candidates = requireStringList(value, 'candidateRouteIds', path);
    if (candidates.isEmpty ||
        candidates.toSet().length != candidates.length ||
        candidates.any((id) => !_resourceIdPattern.hasMatch(id))) {
      throw ControlContractException('$path route set is invalid');
    }
    return EnvironmentRouteSet(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      candidateRouteIds: List.unmodifiable(candidates),
    );
  }

  final String id;
  final int revision;
  final List<String> candidateRouteIds;

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'candidateRouteIds': candidateRouteIds,
  };
}

final class EnvironmentProviderTarget {
  const EnvironmentProviderTarget({
    required this.id,
    required this.revision,
    required this.origin,
    required this.realmId,
    required this.capabilities,
  });

  factory EnvironmentProviderTarget.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'revision', 'origin', 'realmId', 'capabilities'},
    );
    final origin = Uri.tryParse(requireString(value, 'origin', path));
    final capabilities = requireStringList(value, 'capabilities', path);
    if (origin == null ||
        !_isCanonicalHttpsOrigin(origin) ||
        capabilities.isEmpty ||
        capabilities.toSet().length != capabilities.length) {
      throw ControlContractException('$path provider target is invalid');
    }
    return EnvironmentProviderTarget(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      origin: origin,
      realmId: _requireResourceId(value, 'realmId', path),
      capabilities: List.unmodifiable(capabilities),
    );
  }

  final String id;
  final int revision;
  final Uri origin;
  final String realmId;
  final List<String> capabilities;

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'origin': origin.toString(),
    'realmId': realmId,
    'capabilities': capabilities,
  };
}

final class EnvironmentModelPolicy {
  const EnvironmentModelPolicy({
    required this.revision,
    required this.mode,
    required this.fixedModel,
  });

  factory EnvironmentModelPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'revision', 'mode', 'fixedModel'},
    );
    final mode = requireString(value, 'mode', path);
    final fixedModel = requireStringValue(value, 'fixedModel', path);
    if (!const {'preserve', 'passthrough', 'fixed'}.contains(mode) ||
        (mode == 'fixed') != fixedModel.isNotEmpty ||
        utf8.encode(fixedModel).length > 256 ||
        fixedModel.trim() != fixedModel ||
        _containsControlCharacter(fixedModel)) {
      throw ControlContractException('$path model policy is invalid');
    }
    return EnvironmentModelPolicy(
      revision: requireInteger(value, 'revision', path, minimum: 1),
      mode: mode,
      fixedModel: fixedModel,
    );
  }

  final int revision;
  final String mode;
  final String fixedModel;

  JsonObject toJson() => {
    'revision': revision,
    'mode': mode,
    'fixedModel': fixedModel,
  };
}

final class RouteAccountPolicy {
  const RouteAccountPolicy({
    required this.revision,
    required this.mode,
    required this.preferredAccountId,
    required this.candidateAccountIds,
    required this.accountRevisions,
    required this.failoverPolicy,
  });

  factory RouteAccountPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'revision',
        'mode',
        'preferredAccountId',
        'candidateAccountIds',
        'accountRevisions',
        'failoverPolicy',
      },
    );
    final mode = requireString(value, 'mode', path);
    final preferred = requireStringValue(value, 'preferredAccountId', path);
    final candidates = requireStringList(value, 'candidateAccountIds', path);
    final revisions = _requireRevisionMap(
      value['accountRevisions'],
      '$path.accountRevisions',
    );
    final failover = requireString(value, 'failoverPolicy', path);
    final candidateSet = candidates.toSet();
    final passthrough = mode == 'client_passthrough';
    if (!const {'client_passthrough', 'managed'}.contains(mode) ||
        !const {'off', 'account_scoped_safe'}.contains(failover) ||
        candidateSet.length != candidates.length ||
        candidates.any((id) => !_resourceIdPattern.hasMatch(id)) ||
        (passthrough &&
            (preferred.isNotEmpty ||
                candidates.isNotEmpty ||
                revisions.isNotEmpty ||
                failover != 'off')) ||
        (!passthrough &&
            (!_resourceIdPattern.hasMatch(preferred) ||
                candidates.isEmpty ||
                !candidateSet.contains(preferred) ||
                revisions.keys.toSet().difference(candidateSet).isNotEmpty ||
                candidateSet.difference(revisions.keys.toSet()).isNotEmpty))) {
      throw ControlContractException('$path Account authority is invalid');
    }
    return RouteAccountPolicy(
      revision: requireInteger(value, 'revision', path, minimum: 1),
      mode: mode,
      preferredAccountId: preferred,
      candidateAccountIds: List.unmodifiable(candidates),
      accountRevisions: revisions,
      failoverPolicy: failover,
    );
  }

  final int revision;
  final String mode;
  final String preferredAccountId;
  final List<String> candidateAccountIds;
  final Map<String, int> accountRevisions;
  final String failoverPolicy;

  RouteAccountPolicy copyWith({
    int? revision,
    String? mode,
    String? preferredAccountId,
    List<String>? candidateAccountIds,
    Map<String, int>? accountRevisions,
    String? failoverPolicy,
  }) => RouteAccountPolicy(
    revision: revision ?? this.revision,
    mode: mode ?? this.mode,
    preferredAccountId: preferredAccountId ?? this.preferredAccountId,
    candidateAccountIds: candidateAccountIds ?? this.candidateAccountIds,
    accountRevisions: accountRevisions ?? this.accountRevisions,
    failoverPolicy: failoverPolicy ?? this.failoverPolicy,
  );

  JsonObject toJson() => {
    'revision': revision,
    'mode': mode,
    'preferredAccountId': preferredAccountId,
    'candidateAccountIds': candidateAccountIds,
    'accountRevisions': accountRevisions,
    'failoverPolicy': failoverPolicy,
  };
}

final class EnvironmentRoute {
  const EnvironmentRoute({
    required this.id,
    required this.revision,
    required this.providerTarget,
    required this.backendProtocol,
    required this.accountPolicy,
    required this.modelPolicy,
    required this.wireProfileRef,
    required this.pluginBindings,
  });

  factory EnvironmentRoute.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'providerTarget',
        'backendProtocol',
        'accountPolicy',
        'modelPolicy',
        'wireProfileRef',
        'pluginBindings',
      },
    );
    final bindings =
        requireList(value['pluginBindings'], '$path.pluginBindings').indexed
            .map(
              (entry) => EnvironmentPluginBinding.fromJson(
                entry.$2,
                '$path.pluginBindings[${entry.$1}]',
              ),
            )
            .toList(growable: false);
    if (bindings.map((item) => item.id).toSet().length != bindings.length) {
      throw ControlContractException('$path has duplicate plugin bindings');
    }
    return EnvironmentRoute(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      providerTarget: EnvironmentProviderTarget.fromJson(
        value['providerTarget'],
        '$path.providerTarget',
      ),
      backendProtocol: _requireResourceId(value, 'backendProtocol', path),
      accountPolicy: RouteAccountPolicy.fromJson(
        value['accountPolicy'],
        '$path.accountPolicy',
      ),
      modelPolicy: EnvironmentModelPolicy.fromJson(
        value['modelPolicy'],
        '$path.modelPolicy',
      ),
      wireProfileRef: _requireResourceId(value, 'wireProfileRef', path),
      pluginBindings: List.unmodifiable(bindings),
    );
  }

  final String id;
  final int revision;
  final EnvironmentProviderTarget providerTarget;
  final String backendProtocol;
  final RouteAccountPolicy accountPolicy;
  final EnvironmentModelPolicy modelPolicy;
  final String wireProfileRef;
  final List<EnvironmentPluginBinding> pluginBindings;

  String get endpointId => providerTarget.id;
  Uri get endpointOrigin => providerTarget.origin;

  EnvironmentRoute copyWith({RouteAccountPolicy? accountPolicy}) =>
      EnvironmentRoute(
        id: id,
        revision: revision,
        providerTarget: providerTarget,
        backendProtocol: backendProtocol,
        accountPolicy: accountPolicy ?? this.accountPolicy,
        modelPolicy: modelPolicy,
        wireProfileRef: wireProfileRef,
        pluginBindings: pluginBindings,
      );

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'providerTarget': providerTarget.toJson(),
    'backendProtocol': backendProtocol,
    'accountPolicy': accountPolicy.toJson(),
    'modelPolicy': modelPolicy.toJson(),
    'wireProfileRef': wireProfileRef,
    'pluginBindings': pluginBindings
        .map((binding) => binding.toJson())
        .toList(growable: false),
  };
}

final class EnvironmentProtocolPlan {
  const EnvironmentProtocolPlan({
    required this.id,
    required this.revision,
    required this.clientProtocol,
    required this.clientAdapterPolicy,
    required this.mode,
    required this.defaultRouteId,
    required this.routeSet,
    required this.routes,
    required this.pluginBindings,
  });

  factory EnvironmentProtocolPlan.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'clientProtocol',
        'clientAdapterPolicy',
        'mode',
        'upstreamPlan',
        'pluginBindings',
      },
    );
    final upstream = requireObject(value['upstreamPlan'], '$path.upstreamPlan');
    requireFields(
      upstream,
      '$path.upstreamPlan',
      required: const {'routes', 'defaultRouteId', 'routeSet'},
    );
    final routes = requireList(upstream['routes'], '$path.upstreamPlan.routes')
        .indexed
        .map(
          (entry) => EnvironmentRoute.fromJson(
            entry.$2,
            '$path.upstreamPlan.routes[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final routeIds = routes.map((route) => route.id).toSet();
    final defaultRouteId = _requireResourceId(
      upstream,
      'defaultRouteId',
      '$path.upstreamPlan',
    );
    final routeSet = EnvironmentRouteSet.fromJson(
      upstream['routeSet'],
      '$path.upstreamPlan.routeSet',
    );
    final bindings =
        requireList(value['pluginBindings'], '$path.pluginBindings').indexed
            .map(
              (entry) => EnvironmentPluginBinding.fromJson(
                entry.$2,
                '$path.pluginBindings[${entry.$1}]',
              ),
            )
            .toList(growable: false);
    final mode = requireString(value, 'mode', path);
    final clientProtocol = _requireResourceId(value, 'clientProtocol', path);
    if (routes.isEmpty ||
        routeIds.length != routes.length ||
        !routeIds.contains(defaultRouteId) ||
        routeIds.difference(routeSet.candidateRouteIds.toSet()).isNotEmpty ||
        routeSet.candidateRouteIds.toSet().difference(routeIds).isNotEmpty ||
        bindings.map((item) => item.id).toSet().length != bindings.length ||
        !const {
          'anthropic_messages',
          'openai_responses',
          'openai_chat',
        }.contains(clientProtocol) ||
        !const {'original_passthrough', 'managed'}.contains(mode)) {
      throw ControlContractException('$path protocol plan is invalid');
    }
    return EnvironmentProtocolPlan(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      clientProtocol: clientProtocol,
      clientAdapterPolicy: EnvironmentClientAdapterPolicy.fromJson(
        value['clientAdapterPolicy'],
        '$path.clientAdapterPolicy',
      ),
      mode: mode,
      defaultRouteId: defaultRouteId,
      routeSet: routeSet,
      routes: List.unmodifiable(routes),
      pluginBindings: List.unmodifiable(bindings),
    );
  }

  final String id;
  final int revision;
  final String clientProtocol;
  final EnvironmentClientAdapterPolicy clientAdapterPolicy;
  final String mode;
  final String defaultRouteId;
  final EnvironmentRouteSet routeSet;
  final List<EnvironmentRoute> routes;
  final List<EnvironmentPluginBinding> pluginBindings;

  EnvironmentProtocolPlan copyWith({List<EnvironmentRoute>? routes}) =>
      EnvironmentProtocolPlan(
        id: id,
        revision: revision,
        clientProtocol: clientProtocol,
        clientAdapterPolicy: clientAdapterPolicy,
        mode: mode,
        defaultRouteId: defaultRouteId,
        routeSet: routeSet,
        routes: routes ?? this.routes,
        pluginBindings: pluginBindings,
      );

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'clientProtocol': clientProtocol,
    'clientAdapterPolicy': clientAdapterPolicy.toJson(),
    'mode': mode,
    'upstreamPlan': {
      'routes': routes.map((route) => route.toJson()).toList(growable: false),
      'defaultRouteId': defaultRouteId,
      'routeSet': routeSet.toJson(),
    },
    'pluginBindings': pluginBindings
        .map((binding) => binding.toJson())
        .toList(growable: false),
  };
}

final class EnvironmentClientEndpoint {
  const EnvironmentClientEndpoint({
    required this.id,
    required this.revision,
    required this.clientOrigin,
    required this.protocolPlans,
  });

  factory EnvironmentClientEndpoint.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'revision', 'clientOrigin', 'protocolPlans'},
    );
    final origin = Uri.tryParse(requireString(value, 'clientOrigin', path));
    final plans = requireList(value['protocolPlans'], '$path.protocolPlans')
        .indexed
        .map(
          (entry) => EnvironmentProtocolPlan.fromJson(
            entry.$2,
            '$path.protocolPlans[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    if (origin == null ||
        !_isCanonicalHttpsOrigin(origin) ||
        plans.isEmpty ||
        plans.map((plan) => plan.id).toSet().length != plans.length) {
      throw ControlContractException('$path client Endpoint is invalid');
    }
    return EnvironmentClientEndpoint(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      clientOrigin: origin,
      protocolPlans: List.unmodifiable(plans),
    );
  }

  final String id;
  final int revision;
  final Uri clientOrigin;
  final List<EnvironmentProtocolPlan> protocolPlans;

  EnvironmentClientEndpoint copyWith({
    List<EnvironmentProtocolPlan>? protocolPlans,
  }) => EnvironmentClientEndpoint(
    id: id,
    revision: revision,
    clientOrigin: clientOrigin,
    protocolPlans: protocolPlans ?? this.protocolPlans,
  );

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'clientOrigin': clientOrigin.toString(),
    'protocolPlans': protocolPlans
        .map((plan) => plan.toJson())
        .toList(growable: false),
  };
}

final class EnvironmentRecord {
  const EnvironmentRecord({
    required this.id,
    required this.name,
    required this.state,
    required this.revision,
    required this.digest,
    required this.systemOwned,
    required this.clientEndpoints,
    required this.pluginBindings,
    required this.budgetPolicy,
    required this.egressPolicy,
    required this.contentRecording,
    required this.policySet,
  });

  factory EnvironmentRecord.fromJson(
    Object? json,
    String path, {
    bool draftCandidate = false,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'name',
        'state',
        'revision',
        'digest',
        'systemOwned',
        'clientEndpoints',
        'pluginBindings',
        'budgetPolicy',
        'egressPolicy',
        'contentRecording',
        'policySet',
      },
    );
    final endpoints =
        requireList(value['clientEndpoints'], '$path.clientEndpoints').indexed
            .map(
              (entry) => EnvironmentClientEndpoint.fromJson(
                entry.$2,
                '$path.clientEndpoints[${entry.$1}]',
              ),
            )
            .toList(growable: false);
    final bindings =
        requireList(value['pluginBindings'], '$path.pluginBindings').indexed
            .map(
              (entry) => EnvironmentPluginBinding.fromJson(
                entry.$2,
                '$path.pluginBindings[${entry.$1}]',
              ),
            )
            .toList(growable: false);
    final name = requireString(value, 'name', path);
    final state = requireString(value, 'state', path);
    final revision = requireInteger(
      value,
      'revision',
      path,
      minimum: draftCandidate ? 0 : 1,
    );
    final id = _requireResourceId(value, 'id', path);
    final systemOwned = requireBoolean(value, 'systemOwned', path);
    final origins = <String>{};
    final planIds = <String>{};
    final routeIds = <String>{};
    var nestedIdentityInvalid = false;
    for (final endpoint in endpoints) {
      if (!origins.add(endpoint.clientOrigin.toString())) {
        nestedIdentityInvalid = true;
      }
      final protocols = <String>{};
      for (final plan in endpoint.protocolPlans) {
        if (!planIds.add(plan.id) || !protocols.add(plan.clientProtocol)) {
          nestedIdentityInvalid = true;
        }
        for (final route in plan.routes) {
          if (!routeIds.add(route.id)) nestedIdentityInvalid = true;
        }
      }
    }
    if (name.trim() != name ||
        utf8.encode(name).length > 256 ||
        _containsControlCharacter(name) ||
        !const {'active', 'disabled'}.contains(state) ||
        systemOwned != (id == 'system_transparent') ||
        nestedIdentityInvalid ||
        endpoints.map((endpoint) => endpoint.id).toSet().length !=
            endpoints.length ||
        bindings.map((binding) => binding.id).toSet().length !=
            bindings.length) {
      throw ControlContractException('$path Environment is invalid');
    }
    return EnvironmentRecord(
      id: id,
      name: name,
      state: state,
      revision: revision,
      digest: _requireDigest(value, 'digest', path),
      systemOwned: systemOwned,
      clientEndpoints: List.unmodifiable(endpoints),
      pluginBindings: List.unmodifiable(bindings),
      budgetPolicy: EnvironmentBudgetPolicy.fromJson(
        value['budgetPolicy'],
        '$path.budgetPolicy',
      ),
      egressPolicy: EnvironmentEgressPolicy.fromJson(
        value['egressPolicy'],
        '$path.egressPolicy',
      ),
      contentRecording: EnvironmentContentRecordingPolicy.fromJson(
        value['contentRecording'],
        '$path.contentRecording',
      ),
      policySet: EnvironmentPolicySet.fromJson(
        value['policySet'],
        '$path.policySet',
      ),
    );
  }

  final String id;
  final String name;
  final String state;
  final int revision;
  final String digest;
  final bool systemOwned;
  final List<EnvironmentClientEndpoint> clientEndpoints;
  final List<EnvironmentPluginBinding> pluginBindings;
  final EnvironmentBudgetPolicy budgetPolicy;
  final EnvironmentEgressPolicy egressPolicy;
  final EnvironmentContentRecordingPolicy contentRecording;
  final EnvironmentPolicySet policySet;

  Iterable<EnvironmentRoute> get routes sync* {
    for (final endpoint in clientEndpoints) {
      for (final plan in endpoint.protocolPlans) {
        yield* plan.routes;
      }
    }
  }

  EnvironmentRecord copyWith({
    String? name,
    String? state,
    int? revision,
    String? digest,
    List<EnvironmentClientEndpoint>? clientEndpoints,
    EnvironmentContentRecordingPolicy? contentRecording,
    EnvironmentPolicySet? policySet,
  }) => EnvironmentRecord(
    id: id,
    name: name ?? this.name,
    state: state ?? this.state,
    revision: revision ?? this.revision,
    digest: digest ?? this.digest,
    systemOwned: systemOwned,
    clientEndpoints: clientEndpoints ?? this.clientEndpoints,
    pluginBindings: pluginBindings,
    budgetPolicy: budgetPolicy,
    egressPolicy: egressPolicy,
    contentRecording: contentRecording ?? this.contentRecording,
    policySet: policySet ?? this.policySet,
  );

  JsonObject toJson() => {
    'id': id,
    'name': name,
    'state': state,
    'revision': revision,
    'digest': digest,
    'systemOwned': systemOwned,
    'clientEndpoints': clientEndpoints
        .map((endpoint) => endpoint.toJson())
        .toList(growable: false),
    'pluginBindings': pluginBindings
        .map((binding) => binding.toJson())
        .toList(growable: false),
    'budgetPolicy': budgetPolicy.toJson(),
    'egressPolicy': egressPolicy.toJson(),
    'contentRecording': contentRecording.toJson(),
    'policySet': policySet.toJson(),
  };
}

final class EnvironmentDraftInput {
  const EnvironmentDraftInput({
    required this.expectedDraftRevision,
    required this.name,
    required this.state,
    required this.clientEndpoints,
    required this.pluginBindings,
    required this.budgetPolicy,
    required this.egressPolicy,
    required this.contentRecording,
    required this.policySet,
  });

  factory EnvironmentDraftInput.fromEnvironment(
    EnvironmentRecord environment, {
    required int expectedDraftRevision,
    String? name,
    String? state,
    List<EnvironmentClientEndpoint>? clientEndpoints,
    EnvironmentContentRecordingPolicy? contentRecording,
    EnvironmentPolicySet? policySet,
  }) => EnvironmentDraftInput(
    expectedDraftRevision: expectedDraftRevision,
    name: name ?? environment.name,
    state: state ?? environment.state,
    clientEndpoints: clientEndpoints ?? environment.clientEndpoints,
    pluginBindings: environment.pluginBindings,
    budgetPolicy: environment.budgetPolicy,
    egressPolicy: environment.egressPolicy,
    contentRecording: contentRecording ?? environment.contentRecording,
    policySet: policySet ?? environment.policySet,
  );

  final int expectedDraftRevision;
  final String name;
  final String state;
  final List<EnvironmentClientEndpoint> clientEndpoints;
  final List<EnvironmentPluginBinding> pluginBindings;
  final EnvironmentBudgetPolicy budgetPolicy;
  final EnvironmentEgressPolicy egressPolicy;
  final EnvironmentContentRecordingPolicy contentRecording;
  final EnvironmentPolicySet policySet;

  EnvironmentDraftInput withExpectedDraftRevision(int revision) =>
      EnvironmentDraftInput(
        expectedDraftRevision: revision,
        name: name,
        state: state,
        clientEndpoints: clientEndpoints,
        pluginBindings: pluginBindings,
        budgetPolicy: budgetPolicy,
        egressPolicy: egressPolicy,
        contentRecording: contentRecording,
        policySet: policySet,
      );

  void validateFor(String environmentId, int expectedBaseRevision) {
    if (!_resourceIdPattern.hasMatch(environmentId) ||
        expectedBaseRevision < 0 ||
        expectedDraftRevision < 0) {
      throw const ControlContractException(
        'Environment draft authority is invalid',
      );
    }
    EnvironmentRecord.fromJson(
      {
        'id': environmentId,
        'name': name,
        'state': state,
        'revision': expectedBaseRevision + 1,
        'digest': List.filled(64, '0').join(),
        'systemOwned': false,
        'clientEndpoints': clientEndpoints
            .map((endpoint) => endpoint.toJson())
            .toList(growable: false),
        'pluginBindings': pluginBindings
            .map((binding) => binding.toJson())
            .toList(growable: false),
        'budgetPolicy': budgetPolicy.toJson(),
        'egressPolicy': egressPolicy.toJson(),
        'contentRecording': contentRecording.toJson(),
        'policySet': policySet.toJson(),
      },
      'environmentDraftInput',
      draftCandidate: true,
    );
  }

  JsonObject toJson() => {
    'expectedDraftRevision': expectedDraftRevision,
    'name': name,
    'state': state,
    'clientEndpoints': clientEndpoints
        .map((endpoint) => endpoint.toJson())
        .toList(growable: false),
    'pluginBindings': pluginBindings
        .map((binding) => binding.toJson())
        .toList(growable: false),
    'budgetPolicy': budgetPolicy.toJson(),
    'egressPolicy': egressPolicy.toJson(),
    'contentRecording': contentRecording.toJson(),
    'policySet': policySet.toJson(),
  };
}

final class EnvironmentDraft {
  const EnvironmentDraft({
    required this.environmentId,
    required this.baseRevision,
    required this.draftRevision,
    required this.candidateDigest,
    required this.candidate,
  });

  factory EnvironmentDraft.fromJson(
    Object? json,
    String path, {
    required String expectedEnvironmentId,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'environmentId',
        'baseRevision',
        'draftRevision',
        'candidateDigest',
        'candidate',
      },
    );
    final environmentId = _requireResourceId(value, 'environmentId', path);
    final baseRevision = requireInteger(value, 'baseRevision', path);
    final draftRevision = requireInteger(
      value,
      'draftRevision',
      path,
      minimum: 1,
    );
    final digest = _requireDigest(value, 'candidateDigest', path);
    final candidate = EnvironmentRecord.fromJson(
      value['candidate'],
      '$path.candidate',
      draftCandidate: true,
    );
    if (environmentId != expectedEnvironmentId ||
        candidate.id != environmentId ||
        candidate.systemOwned ||
        candidate.revision != baseRevision + 1 ||
        candidate.digest != digest) {
      throw ControlContractException('$path draft identity is inconsistent');
    }
    return EnvironmentDraft(
      environmentId: environmentId,
      baseRevision: baseRevision,
      draftRevision: draftRevision,
      candidateDigest: digest,
      candidate: candidate,
    );
  }

  final String environmentId;
  final int baseRevision;
  final int draftRevision;
  final String candidateDigest;
  final EnvironmentRecord candidate;
}

final class EnvironmentImpactCapture {
  const EnvironmentImpactCapture({
    required this.captureKind,
    required this.captureId,
    required this.classification,
  });

  factory EnvironmentImpactCapture.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'captureKind', 'captureId', 'classification'},
    );
    final kind = requireString(value, 'captureKind', path);
    final classification = requireString(value, 'classification', path);
    if (!const {'managed_run', 'manual_capture'}.contains(kind) ||
        !_environmentCompatibility.contains(classification)) {
      throw ControlContractException('$path impact Capture is invalid');
    }
    return EnvironmentImpactCapture(
      captureKind: kind,
      captureId: _requireResourceId(value, 'captureId', path),
      classification: classification,
    );
  }

  final String captureKind;
  final String captureId;
  final String classification;
}

const _environmentCompatibility = {
  'hot_switch',
  'reconnect_required',
  'restart_required',
};

final class EnvironmentImpact {
  const EnvironmentImpact({
    required this.environmentId,
    required this.baseRevision,
    required this.draftRevision,
    required this.candidateDigest,
    required this.classification,
    required this.hotSwitchCount,
    required this.reconnectRequiredCount,
    required this.restartRequiredCount,
    required this.affected,
  });

  factory EnvironmentImpact.fromJson(
    Object? json,
    String path, {
    required String expectedEnvironmentId,
    required int expectedDraftRevision,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'environmentId',
        'baseRevision',
        'draftRevision',
        'candidateDigest',
        'classification',
        'hotSwitchCount',
        'reconnectRequiredCount',
        'restartRequiredCount',
        'affected',
      },
    );
    final affected = requireList(value['affected'], '$path.affected').indexed
        .map(
          (entry) => EnvironmentImpactCapture.fromJson(
            entry.$2,
            '$path.affected[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final environmentId = _requireResourceId(value, 'environmentId', path);
    final draftRevision = requireInteger(
      value,
      'draftRevision',
      path,
      minimum: 1,
    );
    final classification = requireString(value, 'classification', path);
    final hot = requireInteger(value, 'hotSwitchCount', path);
    final reconnect = requireInteger(value, 'reconnectRequiredCount', path);
    final restart = requireInteger(value, 'restartRequiredCount', path);
    if (environmentId != expectedEnvironmentId ||
        draftRevision != expectedDraftRevision ||
        !_environmentCompatibility.contains(classification) ||
        hot + reconnect + restart != affected.length ||
        affected.where((item) => item.classification == 'hot_switch').length !=
            hot ||
        affected
                .where((item) => item.classification == 'reconnect_required')
                .length !=
            reconnect ||
        affected
                .where((item) => item.classification == 'restart_required')
                .length !=
            restart) {
      throw ControlContractException('$path impact evidence is inconsistent');
    }
    return EnvironmentImpact(
      environmentId: environmentId,
      baseRevision: requireInteger(value, 'baseRevision', path),
      draftRevision: draftRevision,
      candidateDigest: _requireDigest(value, 'candidateDigest', path),
      classification: classification,
      hotSwitchCount: hot,
      reconnectRequiredCount: reconnect,
      restartRequiredCount: restart,
      affected: List.unmodifiable(affected),
    );
  }

  final String environmentId;
  final int baseRevision;
  final int draftRevision;
  final String candidateDigest;
  final String classification;
  final int hotSwitchCount;
  final int reconnectRequiredCount;
  final int restartRequiredCount;
  final List<EnvironmentImpactCapture> affected;
}

final class EnvironmentPublishResult {
  const EnvironmentPublishResult({
    required this.environment,
    required this.impact,
  });

  factory EnvironmentPublishResult.fromJson(
    Object? json,
    String path, {
    required String expectedEnvironmentId,
    required int expectedDraftRevision,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'outcome', 'environment', 'impact'},
    );
    final environment = EnvironmentRecord.fromJson(
      value['environment'],
      '$path.environment',
    );
    final impact = EnvironmentImpact.fromJson(
      value['impact'],
      '$path.impact',
      expectedEnvironmentId: expectedEnvironmentId,
      expectedDraftRevision: expectedDraftRevision,
    );
    if (requireString(value, 'outcome', path) != 'committed' ||
        environment.id != expectedEnvironmentId ||
        environment.digest != impact.candidateDigest ||
        environment.revision != impact.baseRevision + 1) {
      throw ControlContractException('$path publish result is inconsistent');
    }
    return EnvironmentPublishResult(environment: environment, impact: impact);
  }

  final EnvironmentRecord environment;
  final EnvironmentImpact impact;
}

final class ManagedRunSummary {
  const ManagedRunSummary({
    required this.executableLabel,
    required this.cwd,
    required this.canonicalExecutablePath,
    required this.recognition,
    required this.expiresAt,
    this.localUserLabel,
    this.machineId,
    this.machineRegistrationRevision,
    this.workspaceId,
    this.workspaceLabel,
    this.workspaceEvidence,
    this.workspaceDerivationRevision,
    this.processId,
    this.firstObservedAt,
  });

  factory ManagedRunSummary.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'executableLabel',
        'cwd',
        'canonicalExecutablePath',
        'recognition',
        'expiresAt',
      },
      optional: const {
        'localUserLabel',
        'machineId',
        'machineRegistrationRevision',
        'workspaceId',
        'workspaceLabel',
        'workspaceEvidence',
        'workspaceDerivationRevision',
        'processId',
        'firstObservedAt',
      },
    );
    final executableLabel = requireString(value, 'executableLabel', path);
    final cwd = requireString(value, 'cwd', path);
    final canonicalExecutablePath = requireString(
      value,
      'canonicalExecutablePath',
      path,
    );
    final recognition = requireString(value, 'recognition', path);
    final localUserLabel = optionalString(value, 'localUserLabel', path);
    final machineId = optionalString(value, 'machineId', path);
    final machineRegistrationRevision = optionalInteger(
      value,
      'machineRegistrationRevision',
      path,
      minimum: 1,
    );
    final workspaceId = optionalString(value, 'workspaceId', path);
    final workspaceLabel = optionalString(value, 'workspaceLabel', path);
    final workspaceEvidence = optionalString(value, 'workspaceEvidence', path);
    final workspaceDerivationRevision = optionalInteger(
      value,
      'workspaceDerivationRevision',
      path,
      minimum: 1,
    );
    final workspaceEvidenceValues = <Object?>[
      machineId,
      machineRegistrationRevision,
      workspaceId,
      workspaceLabel,
      workspaceEvidence,
      workspaceDerivationRevision,
    ];
    final hasWorkspace = workspaceEvidenceValues.every((item) => item != null);
    if (!_validDisplayLabel(executableLabel) ||
        !_validCleanAbsolutePath(cwd) ||
        !_validCleanAbsolutePath(canonicalExecutablePath) ||
        !const {
          'unknown',
          'unverified',
          'recognized',
          'verified',
        }.contains(recognition) ||
        (localUserLabel != null &&
            !_validDisplayLabel(localUserLabel, maximumBytes: 128)) ||
        (workspaceEvidenceValues.any((item) => item != null) != hasWorkspace) ||
        (hasWorkspace &&
            (!_validWorkspaceIdentity(machineId!) ||
                !_validWorkspaceIdentity(workspaceId!) ||
                !_validDisplayLabel(workspaceLabel!, maximumBytes: 120) ||
                !const {
                  'local_launcher',
                  'registered_companion',
                }.contains(workspaceEvidence)))) {
      throw ControlContractException('$path managed run evidence is invalid');
    }
    return ManagedRunSummary(
      executableLabel: executableLabel,
      cwd: cwd,
      canonicalExecutablePath: canonicalExecutablePath,
      recognition: recognition,
      expiresAt: requireTimestamp(value, 'expiresAt', path),
      localUserLabel: localUserLabel,
      machineId: machineId,
      machineRegistrationRevision: machineRegistrationRevision,
      workspaceId: workspaceId,
      workspaceLabel: workspaceLabel,
      workspaceEvidence: workspaceEvidence,
      workspaceDerivationRevision: workspaceDerivationRevision,
      processId: optionalInteger(value, 'processId', path, minimum: 1),
      firstObservedAt: optionalTimestamp(value, 'firstObservedAt', path),
    );
  }

  final String executableLabel;
  final String cwd;
  final String canonicalExecutablePath;
  final String recognition;
  final DateTime expiresAt;
  final String? localUserLabel;
  final String? machineId;
  final int? machineRegistrationRevision;
  final String? workspaceId;
  final String? workspaceLabel;
  final String? workspaceEvidence;
  final int? workspaceDerivationRevision;
  final int? processId;
  final DateTime? firstObservedAt;

  bool get hasWorkspaceIdentity => machineId != null && workspaceId != null;
}

final class ManualCaptureSummary {
  const ManualCaptureSummary({
    required this.clientClass,
    required this.lifetime,
    required this.credentialRevision,
    this.expiresAt,
    this.lastObservedAt,
  });

  factory ManualCaptureSummary.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'clientClass', 'lifetime', 'credentialRevision'},
      optional: const {'expiresAt', 'lastObservedAt'},
    );
    final clientClass = requireString(value, 'clientClass', path);
    final lifetime = requireString(value, 'lifetime', path);
    final expiresAt = optionalTimestamp(value, 'expiresAt', path);
    if (!const {'cli', 'desktop_app', 'other'}.contains(clientClass) ||
        !const {'temporary', 'until_revoked'}.contains(lifetime) ||
        (lifetime == 'temporary') != (expiresAt != null)) {
      throw ControlContractException(
        '$path manual Capture evidence is invalid',
      );
    }
    return ManualCaptureSummary(
      clientClass: clientClass,
      lifetime: lifetime,
      credentialRevision: requireInteger(
        value,
        'credentialRevision',
        path,
        minimum: 1,
      ),
      expiresAt: expiresAt,
      lastObservedAt: optionalTimestamp(value, 'lastObservedAt', path),
    );
  }

  final String clientClass;
  final String lifetime;
  final int credentialRevision;
  final DateTime? expiresAt;
  final DateTime? lastObservedAt;
}

final class CaptureRecord {
  const CaptureRecord({
    required this.key,
    required this.id,
    required this.kind,
    required this.displayName,
    required this.state,
    required this.observation,
    required this.createdAt,
    required this.updatedAt,
    this.managedRun,
    this.manualCapture,
  });

  factory CaptureRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'key',
        'id',
        'kind',
        'displayName',
        'state',
        'observation',
        'createdAt',
        'updatedAt',
      },
      optional: const {'managedRun', 'manualCapture'},
    );
    final kind = requireString(value, 'kind', path);
    final id = _requireResourceId(value, 'id', path);
    final key = requireString(value, 'key', path);
    final state = requireString(value, 'state', path);
    final observation = requireString(value, 'observation', path);
    final managed = value['managedRun'];
    final manual = value['manualCapture'];
    if ((kind == 'managed_run') != (managed != null) ||
        (kind == 'manual_capture') != (manual != null) ||
        key != '$kind:$id' ||
        !_validDisplayLabel(requireString(value, 'displayName', path)) ||
        !const {'waiting_for_traffic', 'observed'}.contains(observation) ||
        (kind == 'managed_run' &&
            !const {
              'created',
              'attached',
              'finished',
              'revoked',
              'expired',
            }.contains(state)) ||
        (kind == 'manual_capture' &&
            !const {'active', 'revoked', 'expired'}.contains(state))) {
      throw ControlContractException(
        '$path capture kind evidence is inconsistent',
      );
    }
    final managedSummary = managed == null
        ? null
        : ManagedRunSummary.fromJson(managed, '$path.managedRun');
    final manualSummary = manual == null
        ? null
        : ManualCaptureSummary.fromJson(manual, '$path.manualCapture');
    final createdAt = requireTimestamp(value, 'createdAt', path);
    final updatedAt = requireTimestamp(value, 'updatedAt', path);
    if (updatedAt.isBefore(createdAt) ||
        (kind == 'managed_run' &&
            ((state == 'created' && managedSummary?.processId != null) ||
                (state == 'attached' && managedSummary?.processId == null))) ||
        (observation == 'observed') !=
            (kind == 'managed_run'
                ? managedSummary?.firstObservedAt != null
                : manualSummary?.lastObservedAt != null)) {
      throw ControlContractException('$path capture evidence is inconsistent');
    }
    return CaptureRecord(
      key: key,
      id: id,
      kind: kind,
      displayName: requireString(value, 'displayName', path),
      state: state,
      observation: observation,
      createdAt: createdAt,
      updatedAt: updatedAt,
      managedRun: managedSummary,
      manualCapture: manualSummary,
    );
  }

  final String key;
  final String id;
  final String kind;
  final String displayName;
  final String state;
  final String observation;
  final DateTime createdAt;
  final DateTime updatedAt;
  final ManagedRunSummary? managedRun;
  final ManualCaptureSummary? manualCapture;

  bool get running => kind == 'managed_run'
      ? state == 'created' || state == 'attached'
      : state == 'active';
  bool get isManual => kind == 'manual_capture';
  String? get captureRunId => kind == 'managed_run' ? id : null;
}

final class CaptureAssignment {
  const CaptureAssignment({
    required this.captureKey,
    required this.captureId,
    required this.captureKind,
    required this.environmentId,
    required this.revision,
    required this.source,
    required this.updatedAt,
  });

  factory CaptureAssignment.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'captureKey',
        'captureId',
        'captureKind',
        'environmentId',
        'revision',
        'source',
        'updatedAt',
      },
    );
    final captureId = _requireResourceId(value, 'captureId', path);
    final captureKind = requireString(value, 'captureKind', path);
    final captureKey = requireString(value, 'captureKey', path);
    final source = requireString(value, 'source', path);
    if (!const {'managed_run', 'manual_capture'}.contains(captureKind) ||
        captureKey != '$captureKind:$captureId' ||
        !const {
          'launch',
          'manual_create',
          'workspace_default',
          'operator_switch',
          'system_transparent',
        }.contains(source)) {
      throw ControlContractException('$path assignment authority is invalid');
    }
    return CaptureAssignment(
      captureKey: captureKey,
      captureId: captureId,
      captureKind: captureKind,
      environmentId: _requireResourceId(value, 'environmentId', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      source: source,
      updatedAt: requireTimestamp(value, 'updatedAt', path),
    );
  }

  final String captureKey;
  final String captureId;
  final String captureKind;
  final String environmentId;
  final int revision;
  final String source;
  final DateTime updatedAt;
}

final class WorkspaceEnvironmentDefault {
  const WorkspaceEnvironmentDefault({
    required this.machineId,
    required this.workspaceId,
    required this.environmentId,
    required this.environmentName,
    required this.revision,
    required this.updatedAt,
  });

  factory WorkspaceEnvironmentDefault.fromJson(
    Object? json,
    String path, {
    required String expectedMachineId,
    required String expectedWorkspaceId,
    String? expectedEnvironmentId,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'machineId',
        'workspaceId',
        'environmentId',
        'environmentName',
        'revision',
        'updatedAt',
      },
    );
    final machineId = requireString(value, 'machineId', path);
    final workspaceId = requireString(value, 'workspaceId', path);
    final environmentId = _requireResourceId(value, 'environmentId', path);
    final environmentName = requireString(value, 'environmentName', path);
    if (!_validWorkspaceIdentity(machineId) ||
        !_validWorkspaceIdentity(workspaceId) ||
        machineId != expectedMachineId ||
        workspaceId != expectedWorkspaceId ||
        environmentId == 'system_transparent' ||
        (expectedEnvironmentId != null &&
            environmentId != expectedEnvironmentId) ||
        !_validDisplayLabel(environmentName)) {
      throw ControlContractException('$path workspace default is invalid');
    }
    return WorkspaceEnvironmentDefault(
      machineId: machineId,
      workspaceId: workspaceId,
      environmentId: environmentId,
      environmentName: environmentName,
      revision: requireInteger(value, 'revision', path, minimum: 1),
      updatedAt: requireTimestamp(value, 'updatedAt', path),
    );
  }

  final String machineId;
  final String workspaceId;
  final String environmentId;
  final String environmentName;
  final int revision;
  final DateTime updatedAt;
}

final class CaptureAssignmentChange {
  const CaptureAssignmentChange({
    required this.assignment,
    required this.boundary,
    required this.closedConnections,
    required this.applied,
    this.reasonCode,
  });

  factory CaptureAssignmentChange.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'assignment',
        'boundary',
        'closedConnections',
        'applied',
      },
      optional: const {'reasonCode'},
    );
    final boundary = requireString(value, 'boundary', path);
    final closedConnections = requireStringList(
      value,
      'closedConnections',
      path,
    );
    final applied = requireBoolean(value, 'applied', path);
    final reasonCode = optionalString(value, 'reasonCode', path);
    if (!const {
          'no_change',
          'hot_switch',
          'reconnect_required',
          'restart_required',
        }.contains(boundary) ||
        closedConnections.any((id) => !_resourceIdPattern.hasMatch(id)) ||
        closedConnections.toSet().length != closedConnections.length ||
        (boundary != 'reconnect_required' && closedConnections.isNotEmpty) ||
        (boundary == 'restart_required') != !applied ||
        (boundary == 'restart_required') !=
            (reasonCode == 'capture_restart_required') ||
        (reasonCode != null && reasonCode != 'capture_restart_required')) {
      throw ControlContractException('$path switch result is inconsistent');
    }
    return CaptureAssignmentChange(
      assignment: CaptureAssignment.fromJson(
        value['assignment'],
        '$path.assignment',
      ),
      boundary: boundary,
      closedConnections: closedConnections,
      applied: applied,
      reasonCode: reasonCode,
    );
  }

  final CaptureAssignment assignment;
  final String boundary;
  final List<String> closedConnections;
  final bool applied;
  final String? reasonCode;
}

final class FrozenEnvironmentRef {
  const FrozenEnvironmentRef({
    required this.id,
    required this.revision,
    required this.digest,
    required this.clientEndpointId,
    required this.clientEndpointRevision,
    required this.protocolPlanId,
    required this.protocolPlanRevision,
    required this.routeId,
    required this.routeRevision,
    required this.accountId,
    required this.accountRevision,
    required this.credentialEpoch,
  });

  factory FrozenEnvironmentRef.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'digest',
        'clientEndpointId',
        'clientEndpointRevision',
        'protocolPlanId',
        'protocolPlanRevision',
        'routeId',
        'routeRevision',
      },
      optional: const {'accountId', 'accountRevision', 'credentialEpoch'},
    );
    final accountId = optionalString(value, 'accountId', path);
    final accountRevision = optionalInteger(
      value,
      'accountRevision',
      path,
      minimum: 1,
    );
    final credentialEpoch = optionalInteger(
      value,
      'credentialEpoch',
      path,
      minimum: 1,
    );
    if ((accountId == null) != (accountRevision == null) ||
        (accountId == null) != (credentialEpoch == null)) {
      throw ControlContractException('$path account evidence is incomplete');
    }
    return FrozenEnvironmentRef(
      id: requireString(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      digest: requireString(value, 'digest', path),
      clientEndpointId: requireString(value, 'clientEndpointId', path),
      clientEndpointRevision: requireInteger(
        value,
        'clientEndpointRevision',
        path,
        minimum: 1,
      ),
      protocolPlanId: requireString(value, 'protocolPlanId', path),
      protocolPlanRevision: requireInteger(
        value,
        'protocolPlanRevision',
        path,
        minimum: 1,
      ),
      routeId: requireString(value, 'routeId', path),
      routeRevision: requireInteger(value, 'routeRevision', path, minimum: 1),
      accountId: accountId,
      accountRevision: accountRevision,
      credentialEpoch: credentialEpoch,
    );
  }

  final String id;
  final int revision;
  final String digest;
  final String clientEndpointId;
  final int clientEndpointRevision;
  final String protocolPlanId;
  final int protocolPlanRevision;
  final String routeId;
  final int routeRevision;
  final String? accountId;
  final int? accountRevision;
  final int? credentialEpoch;
}

final class ActivitySourceRef {
  const ActivitySourceRef({
    required this.kind,
    required this.displayName,
    required this.recognition,
  });

  factory ActivitySourceRef.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'displayName', 'recognition'},
    );
    final kind = requireString(value, 'kind', path);
    final recognition = requireString(value, 'recognition', path);
    if (!const {'capture_run', 'manual_proxy', 'system_proxy'}.contains(kind) ||
        !const {'verified', 'configured', 'unknown'}.contains(recognition)) {
      throw ControlContractException('$path source evidence is unsupported');
    }
    return ActivitySourceRef(
      kind: kind,
      displayName: requireString(value, 'displayName', path),
      recognition: recognition,
    );
  }

  final String kind;
  final String displayName;
  final String recognition;
}

final class ActivityParentRefs {
  const ActivityParentRefs({
    required this.exchangeId,
    required this.captureRunId,
    required this.manualCaptureId,
    required this.connectionId,
  });

  factory ActivityParentRefs.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'exchangeId'},
      optional: const {'captureRunId', 'manualCaptureId', 'connectionId'},
    );
    final captureRunId = optionalString(value, 'captureRunId', path);
    final manualCaptureId = optionalString(value, 'manualCaptureId', path);
    if (captureRunId != null && manualCaptureId != null) {
      throw ControlContractException('$path capture parent is ambiguous');
    }
    return ActivityParentRefs(
      exchangeId: requireString(value, 'exchangeId', path),
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      connectionId: optionalString(value, 'connectionId', path),
    );
  }

  final String exchangeId;
  final String? captureRunId;
  final String? manualCaptureId;
  final String? connectionId;
}

final class ActivityRecord {
  const ActivityRecord({
    required this.id,
    required this.occurredAt,
    required this.title,
    required this.status,
    required this.reasonCode,
    required this.source,
    required this.environment,
    required this.parentRefs,
  });

  factory ActivityRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'occurredAt',
        'kind',
        'title',
        'status',
        'source',
        'environment',
        'parentRefs',
      },
      optional: const {'reasonCode'},
    );
    final id = requireString(value, 'id', path);
    final status = requireString(value, 'status', path);
    final parents = ActivityParentRefs.fromJson(
      value['parentRefs'],
      '$path.parentRefs',
    );
    if (requireString(value, 'kind', path) != 'exchange' ||
        !const {
          'pending',
          'succeeded',
          'failed',
          'canceled',
        }.contains(status) ||
        parents.exchangeId != id) {
      throw ControlContractException('$path Activity evidence is inconsistent');
    }
    return ActivityRecord(
      id: id,
      occurredAt: requireTimestamp(value, 'occurredAt', path),
      title: requireString(value, 'title', path),
      status: status,
      reasonCode: optionalString(value, 'reasonCode', path),
      source: ActivitySourceRef.fromJson(value['source'], '$path.source'),
      environment: FrozenEnvironmentRef.fromJson(
        value['environment'],
        '$path.environment',
      ),
      parentRefs: parents,
    );
  }

  final String id;
  final DateTime occurredAt;
  final String title;
  final String status;
  final String? reasonCode;
  final ActivitySourceRef source;
  final FrozenEnvironmentRef environment;
  final ActivityParentRefs parentRefs;

  String get sourceName => source.displayName;
  String get environmentId => environment.id;
  String get routeId => environment.routeId;
  String? get accountId => environment.accountId;
  String? get captureRunId => parentRefs.captureRunId;
  String? get manualCaptureId => parentRefs.manualCaptureId;
}

final class ActivityPage {
  const ActivityPage({required this.items, required this.nextCursor});

  factory ActivityPage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'items'},
      optional: const {'nextCursor'},
    );
    final rawItems = requireList(value['items'], '$path.items');
    final nextCursor = optionalString(value, 'nextCursor', path);
    if (rawItems.length > 200 ||
        (nextCursor != null &&
            (nextCursor.length > 512 || RegExp(r'\s').hasMatch(nextCursor)))) {
      throw ControlContractException('$path page boundary is invalid');
    }
    return ActivityPage(
      items: rawItems.indexed
          .map(
            (entry) =>
                ActivityRecord.fromJson(entry.$2, '$path.items[${entry.$1}]'),
          )
          .toList(growable: false),
      nextCursor: nextCursor,
    );
  }

  final List<ActivityRecord> items;
  final String? nextCursor;
}

final class ExchangeDiagnosis {
  const ExchangeDiagnosis({
    required this.providerStatus,
    required this.providerField,
    required this.clientField,
    required this.clientPath,
  });

  factory ExchangeDiagnosis.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {},
      optional: const {
        'providerStatus',
        'providerField',
        'clientField',
        'clientPath',
      },
    );
    final result = ExchangeDiagnosis(
      providerStatus: optionalInteger(
        value,
        'providerStatus',
        path,
        minimum: 100,
      ),
      providerField: optionalString(value, 'providerField', path),
      clientField: optionalString(value, 'clientField', path),
      clientPath: optionalString(value, 'clientPath', path),
    );
    if (result.providerStatus == null &&
        result.providerField == null &&
        result.clientField == null &&
        result.clientPath == null) {
      throw ControlContractException('$path diagnosis is empty');
    }
    return result;
  }

  final int? providerStatus;
  final String? providerField;
  final String? clientField;
  final String? clientPath;
}

final class ExchangeContentBlock {
  const ExchangeContentBlock({
    required this.kind,
    required this.availability,
    required this.text,
    required this.originalSize,
    required this.callId,
    required this.toolName,
    required this.arguments,
    required this.toolError,
  });

  factory ExchangeContentBlock.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'availability', 'originalSize'},
      optional: const {'text', 'callId', 'toolName', 'arguments', 'toolError'},
    );
    final kind = requireString(value, 'kind', path);
    final availability = requireString(value, 'availability', path);
    final toolErrorValue = value['toolError'];
    if (!const {
          'text',
          'refusal',
          'tool_call',
          'tool_result',
          'reasoning',
          'provider_extension',
        }.contains(kind) ||
        !const {'recorded', 'omitted'}.contains(availability) ||
        (toolErrorValue != null && toolErrorValue is! bool)) {
      throw ControlContractException('$path content block is unsupported');
    }
    final text = optionalString(value, 'text', path);
    final arguments = value['arguments'] == null
        ? null
        : requireObject(value['arguments'], '$path.arguments');
    if (availability == 'omitted' && (text != null || arguments != null)) {
      throw ControlContractException('$path omitted block retained content');
    }
    return ExchangeContentBlock(
      kind: kind,
      availability: availability,
      text: text,
      originalSize: requireInteger(value, 'originalSize', path),
      callId: optionalString(value, 'callId', path),
      toolName: optionalString(value, 'toolName', path),
      arguments: arguments,
      toolError: toolErrorValue == true,
    );
  }

  final String kind;
  final String availability;
  final String? text;
  final int originalSize;
  final String? callId;
  final String? toolName;
  final JsonObject? arguments;
  final bool toolError;
}

final class ExchangeContentMessage {
  const ExchangeContentMessage({required this.role, required this.blocks});

  factory ExchangeContentMessage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'role', 'blocks'});
    final role = requireString(value, 'role', path);
    final rawBlocks = requireList(value['blocks'], '$path.blocks');
    if (!const {
          'system',
          'developer',
          'user',
          'assistant',
          'tool',
        }.contains(role) ||
        rawBlocks.isEmpty) {
      throw ControlContractException('$path message is invalid');
    }
    return ExchangeContentMessage(
      role: role,
      blocks: rawBlocks.indexed
          .map(
            (entry) => ExchangeContentBlock.fromJson(
              entry.$2,
              '$path.blocks[${entry.$1}]',
            ),
          )
          .toList(growable: false),
    );
  }

  final String role;
  final List<ExchangeContentBlock> blocks;
}

final class ExchangeToolDefinition {
  const ExchangeToolDefinition({required this.name, required this.namespace});

  factory ExchangeToolDefinition.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'name'},
      optional: const {'namespace'},
    );
    return ExchangeToolDefinition(
      name: requireString(value, 'name', path),
      namespace: optionalString(value, 'namespace', path),
    );
  }

  final String name;
  final String? namespace;
}

final class ExchangeRequest {
  const ExchangeRequest({
    required this.requestedModel,
    required this.effectiveModel,
    required this.maxOutputTokens,
    required this.stream,
    required this.messages,
    required this.tools,
  });

  factory ExchangeRequest.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'requestedModel',
        'effectiveModel',
        'maxOutputTokens',
        'stream',
        'messages',
        'tools',
      },
    );
    final rawMessages = requireList(value['messages'], '$path.messages');
    final rawTools = requireList(value['tools'], '$path.tools');
    return ExchangeRequest(
      requestedModel: requireString(value, 'requestedModel', path),
      effectiveModel: requireString(value, 'effectiveModel', path),
      maxOutputTokens: requireInteger(value, 'maxOutputTokens', path),
      stream: requireBoolean(value, 'stream', path),
      messages: rawMessages.indexed
          .map(
            (entry) => ExchangeContentMessage.fromJson(
              entry.$2,
              '$path.messages[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      tools: rawTools.indexed
          .map(
            (entry) => ExchangeToolDefinition.fromJson(
              entry.$2,
              '$path.tools[${entry.$1}]',
            ),
          )
          .toList(growable: false),
    );
  }

  final String requestedModel;
  final String effectiveModel;
  final int maxOutputTokens;
  final bool stream;
  final List<ExchangeContentMessage> messages;
  final List<ExchangeToolDefinition> tools;
}

final class ExchangeUsageValue {
  const ExchangeUsageValue({
    required this.known,
    required this.tokens,
    required this.source,
  });

  factory ExchangeUsageValue.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final known = requireBoolean(value, 'known', path);
    requireFields(
      value,
      path,
      required: const {'known'},
      optional: known ? const {'tokens', 'source'} : const {},
    );
    final tokens = optionalInteger(value, 'tokens', path);
    final source = optionalString(value, 'source', path);
    if (known != (tokens != null && source != null)) {
      throw ControlContractException('$path usage evidence is inconsistent');
    }
    return ExchangeUsageValue(known: known, tokens: tokens, source: source);
  }

  final bool known;
  final int? tokens;
  final String? source;
}

final class ExchangeUsage {
  const ExchangeUsage({
    required this.inputUncached,
    required this.cacheWrite,
    required this.cacheRead,
    required this.output,
    required this.reasoning,
  });

  factory ExchangeUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'inputUncached',
        'cacheWrite',
        'cacheRead',
        'output',
        'reasoning',
      },
    );
    return ExchangeUsage(
      inputUncached: ExchangeUsageValue.fromJson(
        value['inputUncached'],
        '$path.inputUncached',
      ),
      cacheWrite: ExchangeUsageValue.fromJson(
        value['cacheWrite'],
        '$path.cacheWrite',
      ),
      cacheRead: ExchangeUsageValue.fromJson(
        value['cacheRead'],
        '$path.cacheRead',
      ),
      output: ExchangeUsageValue.fromJson(value['output'], '$path.output'),
      reasoning: ExchangeUsageValue.fromJson(
        value['reasoning'],
        '$path.reasoning',
      ),
    );
  }

  final ExchangeUsageValue inputUncached;
  final ExchangeUsageValue cacheWrite;
  final ExchangeUsageValue cacheRead;
  final ExchangeUsageValue output;
  final ExchangeUsageValue reasoning;
}

final class ExchangeResponse {
  const ExchangeResponse({
    required this.id,
    required this.requestedModel,
    required this.effectiveModel,
    required this.reportedModel,
    required this.stopReason,
    required this.blocks,
    required this.usage,
  });

  factory ExchangeResponse.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'requestedModel',
        'effectiveModel',
        'reportedModel',
        'stopReason',
        'blocks',
        'usage',
      },
    );
    final stopReason = requireString(value, 'stopReason', path);
    final rawBlocks = requireList(value['blocks'], '$path.blocks');
    if (!const {
          'end_turn',
          'max_tokens',
          'tool_use',
          'stop_sequence',
        }.contains(stopReason) ||
        rawBlocks.isEmpty) {
      throw ControlContractException('$path response is invalid');
    }
    return ExchangeResponse(
      id: requireString(value, 'id', path),
      requestedModel: requireString(value, 'requestedModel', path),
      effectiveModel: requireString(value, 'effectiveModel', path),
      reportedModel: requireString(value, 'reportedModel', path),
      stopReason: stopReason,
      blocks: rawBlocks.indexed
          .map(
            (entry) => ExchangeContentBlock.fromJson(
              entry.$2,
              '$path.blocks[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      usage: ExchangeUsage.fromJson(value['usage'], '$path.usage'),
    );
  }

  final String id;
  final String requestedModel;
  final String effectiveModel;
  final String reportedModel;
  final String stopReason;
  final List<ExchangeContentBlock> blocks;
  final ExchangeUsage usage;
}

final class ExchangeRequestProjection {
  const ExchangeRequestProjection({
    required this.view,
    required this.relationship,
    required this.inheritedMessageCount,
    required this.totalMessageCount,
    required this.fullSnapshotAvailable,
  });

  factory ExchangeRequestProjection.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'view',
        'relationship',
        'inheritedMessageCount',
        'totalMessageCount',
        'fullSnapshotAvailable',
      },
    );
    final view = requireString(value, 'view', path);
    final relationship = requireString(value, 'relationship', path);
    final inherited = requireInteger(value, 'inheritedMessageCount', path);
    final total = requireInteger(value, 'totalMessageCount', path, minimum: 1);
    final full = requireBoolean(value, 'fullSnapshotAvailable', path);
    final relationshipValid = switch (relationship) {
      'checkpoint' => inherited == 0,
      'incremental' => inherited > 0 && inherited < total,
      'same_transcript' => inherited == total,
      _ => false,
    };
    if (!const {'incremental', 'full'}.contains(view) ||
        !relationshipValid ||
        full != (inherited > 0)) {
      throw ControlContractException('$path request projection is invalid');
    }
    return ExchangeRequestProjection(
      view: view,
      relationship: relationship,
      inheritedMessageCount: inherited,
      totalMessageCount: total,
      fullSnapshotAvailable: full,
    );
  }

  final String view;
  final String relationship;
  final int inheritedMessageCount;
  final int totalMessageCount;
  final bool fullSnapshotAvailable;
}

final class ExchangeContentDetail {
  const ExchangeContentDetail({
    required this.state,
    required this.mode,
    required this.recordedAt,
    required this.expiresAt,
    required this.requestProjection,
    required this.request,
    required this.response,
  });

  factory ExchangeContentDetail.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final state = requireString(value, 'state', path);
    if (state == 'not_recorded') {
      requireFields(value, path, required: const {'state'});
      return const ExchangeContentDetail(
        state: 'not_recorded',
        mode: null,
        recordedAt: null,
        expiresAt: null,
        requestProjection: null,
        request: null,
        response: null,
      );
    }
    if (state != 'recorded') {
      throw ControlContractException('$path content state is unsupported');
    }
    requireFields(
      value,
      path,
      required: const {
        'state',
        'mode',
        'recordedAt',
        'expiresAt',
        'requestProjection',
        'request',
      },
      optional: const {'response'},
    );
    final mode = requireString(value, 'mode', path);
    final recordedAt = requireTimestamp(value, 'recordedAt', path);
    final expiresAt = requireTimestamp(value, 'expiresAt', path);
    final projection = ExchangeRequestProjection.fromJson(
      value['requestProjection'],
      '$path.requestProjection',
    );
    final request = ExchangeRequest.fromJson(value['request'], '$path.request');
    final expectedMessages = projection.view == 'full'
        ? projection.totalMessageCount
        : projection.totalMessageCount - projection.inheritedMessageCount;
    if (!const {'full', 'metadata_only'}.contains(mode) ||
        !expiresAt.isAfter(recordedAt) ||
        request.messages.length != expectedMessages) {
      throw ControlContractException('$path recorded content is inconsistent');
    }
    return ExchangeContentDetail(
      state: state,
      mode: mode,
      recordedAt: recordedAt,
      expiresAt: expiresAt,
      requestProjection: projection,
      request: request,
      response: value['response'] == null
          ? null
          : ExchangeResponse.fromJson(value['response'], '$path.response'),
    );
  }

  final String state;
  final String? mode;
  final DateTime? recordedAt;
  final DateTime? expiresAt;
  final ExchangeRequestProjection? requestProjection;
  final ExchangeRequest? request;
  final ExchangeResponse? response;
}

final class ExchangeProcessingTrace {
  const ExchangeProcessingTrace({
    required this.egressProxyId,
    required this.pluginRunIds,
    required this.attempts,
    required this.result,
  });

  factory ExchangeProcessingTrace.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'pluginRunIds', 'attempts', 'result'},
      optional: const {'egressProxyId'},
    );
    final rawAttempts = requireList(value['attempts'], '$path.attempts');
    return ExchangeProcessingTrace(
      egressProxyId: optionalString(value, 'egressProxyId', path),
      pluginRunIds: requireStringList(value, 'pluginRunIds', path),
      attempts: rawAttempts.indexed
          .map(
            (entry) => EgressAttemptRecord.fromJson(
              entry.$2,
              '$path.attempts[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      result: requireString(value, 'result', path),
    );
  }

  final String? egressProxyId;
  final List<String> pluginRunIds;
  final List<EgressAttemptRecord> attempts;
  final String result;
}

final class ExchangeDetail {
  const ExchangeDetail({
    required this.id,
    required this.status,
    required this.environment,
    required this.parentRefs,
    required this.diagnosis,
    required this.processingTrace,
    required this.content,
  });

  factory ExchangeDetail.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'status',
        'environment',
        'parentRefs',
        'processingTrace',
        'content',
      },
      optional: const {'diagnosis'},
    );
    final id = requireString(value, 'id', path);
    final status = requireString(value, 'status', path);
    final parents = ActivityParentRefs.fromJson(
      value['parentRefs'],
      '$path.parentRefs',
    );
    final trace = ExchangeProcessingTrace.fromJson(
      value['processingTrace'],
      '$path.processingTrace',
    );
    if (!const {
          'pending',
          'succeeded',
          'failed',
          'canceled',
        }.contains(status) ||
        parents.exchangeId != id ||
        trace.attempts.any((attempt) => attempt.exchangeId != id)) {
      throw ControlContractException('$path Exchange evidence is inconsistent');
    }
    return ExchangeDetail(
      id: id,
      status: status,
      environment: FrozenEnvironmentRef.fromJson(
        value['environment'],
        '$path.environment',
      ),
      parentRefs: parents,
      diagnosis: value['diagnosis'] == null
          ? null
          : ExchangeDiagnosis.fromJson(value['diagnosis'], '$path.diagnosis'),
      processingTrace: trace,
      content: ExchangeContentDetail.fromJson(
        value['content'],
        '$path.content',
      ),
    );
  }

  final String id;
  final String status;
  final FrozenEnvironmentRef environment;
  final ActivityParentRefs parentRefs;
  final ExchangeDiagnosis? diagnosis;
  final ExchangeProcessingTrace processingTrace;
  final ExchangeContentDetail content;
}

final class ApprovalChoice {
  const ApprovalChoice({
    required this.decision,
    required this.scope,
    required this.labelKey,
  });

  factory ApprovalChoice.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'decision', 'scope', 'labelKey'},
    );
    final decision = requireString(value, 'decision', path);
    final scope = requireString(value, 'scope', path);
    if (!const {'allow-once', 'deny'}.contains(decision) ||
        !const {'request', 'host_port'}.contains(scope)) {
      throw ControlContractException('$path choice is unsupported');
    }
    return ApprovalChoice(
      decision: decision,
      scope: scope,
      labelKey: _requireApprovalIdentity(value, 'labelKey', path),
    );
  }

  final String decision;
  final String scope;
  final String labelKey;
}

final class ApprovalTarget {
  const ApprovalTarget({required this.host, required this.port});

  factory ApprovalTarget.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'host', 'port'});
    final port = requireInteger(value, 'port', path, minimum: 1);
    final host = requireString(value, 'host', path);
    if (port > 65535 ||
        host.trim() != host ||
        utf8.encode(host).length > 1024 ||
        RegExp(r'[\s/?#@]').hasMatch(host)) {
      throw ControlContractException('$path.port exceeds 65535');
    }
    return ApprovalTarget(host: host, port: port);
  }

  final String host;
  final int port;
}

final class ApprovalRecord {
  const ApprovalRecord({
    required this.id,
    required this.revision,
    required this.kind,
    required this.state,
    required this.risk,
    required this.titleKey,
    required this.summaryKey,
    required this.aggregateKey,
    required this.exchangeId,
    required this.environmentId,
    required this.environmentRevision,
    required this.environmentDigest,
    required this.routeId,
    required this.routeRevision,
    required this.target,
    required this.subjectRefs,
    required this.subjectLabels,
    required this.requestCount,
    required this.waiterCount,
    required this.choices,
    required this.createdAt,
    required this.expiresAt,
    required this.resolvedAt,
    required this.decision,
    required this.decisionScope,
    required this.terminalReason,
  });

  factory ApprovalRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'kind',
        'state',
        'risk',
        'titleKey',
        'summaryKey',
        'aggregateKey',
        'subjectRefs',
        'subjectLabels',
        'requestCount',
        'waiterCount',
        'choices',
        'createdAt',
        'expiresAt',
      },
      optional: const {
        'exchangeId',
        'environmentId',
        'environmentRevision',
        'environmentDigest',
        'routeId',
        'routeRevision',
        'target',
        'resolvedAt',
        'decision',
        'decisionScope',
        'terminalReason',
      },
    );
    final kind = requireString(value, 'kind', path);
    final presentation = _approvalPresentation(kind, path);
    final state = requireString(value, 'state', path);
    if (!const {
      'pending',
      'allowed',
      'denied',
      'canceled',
      'expired',
    }.contains(state)) {
      throw ControlContractException('$path.state is unsupported');
    }
    final risk = _requireApprovalIdentity(value, 'risk', path);
    final titleKey = _requireApprovalIdentity(value, 'titleKey', path);
    final summaryKey = _requireApprovalIdentity(value, 'summaryKey', path);
    if (risk != presentation.risk ||
        titleKey != presentation.titleKey ||
        summaryKey != presentation.summaryKey) {
      throw ControlContractException('$path presentation is inconsistent');
    }

    final exchangeId = _optionalApprovalIdentity(value, 'exchangeId', path);
    final environmentId = _optionalApprovalResourceId(
      value,
      'environmentId',
      path,
    );
    final environmentRevision = optionalInteger(
      value,
      'environmentRevision',
      path,
      minimum: 1,
    );
    final environmentDigest = optionalString(value, 'environmentDigest', path);
    final routeId = _optionalApprovalResourceId(value, 'routeId', path);
    final routeRevision = optionalInteger(
      value,
      'routeRevision',
      path,
      minimum: 1,
    );
    final binding = <Object?>[
      exchangeId,
      environmentId,
      environmentRevision,
      environmentDigest,
      routeId,
      routeRevision,
    ];
    final hasBinding = binding.any((field) => field != null);
    if ((hasBinding && binding.any((field) => field == null)) ||
        (environmentDigest != null &&
            !_digestPattern.hasMatch(environmentDigest)) ||
        (kind == 'tool_intent' && !hasBinding)) {
      throw ControlContractException(
        '$path frozen Environment route binding is incomplete',
      );
    }

    final target = value['target'] == null
        ? null
        : ApprovalTarget.fromJson(value['target'], '$path.target');
    if ((kind == 'network_ask') != (target != null)) {
      throw ControlContractException('$path target does not match its kind');
    }

    final subjectRefs = _requireApprovalIdentityList(
      value,
      'subjectRefs',
      path,
      maximumItems: 128,
    );
    final subjectLabels = _requireApprovalIdentityList(
      value,
      'subjectLabels',
      path,
      maximumItems: 128,
    );
    if (subjectRefs.isEmpty || subjectRefs.length != subjectLabels.length) {
      throw ControlContractException('$path subject evidence is inconsistent');
    }

    final choices = requireList(value['choices'], '$path.choices');
    if (choices.isEmpty || choices.length > 8) {
      throw ControlContractException('$path choices are invalid');
    }
    final parsedChoices = choices.indexed
        .map(
          (entry) =>
              ApprovalChoice.fromJson(entry.$2, '$path.choices[${entry.$1}]'),
        )
        .toList(growable: false);
    final offered = <String, String>{
      for (final choice in parsedChoices)
        '${choice.decision}\u0000${choice.scope}': choice.labelKey,
    };
    if (offered.length != parsedChoices.length ||
        offered.length != presentation.choices.length ||
        presentation.choices.entries.any(
          (entry) => offered[entry.key] != entry.value,
        )) {
      throw ControlContractException('$path choices are inconsistent');
    }

    final requestCount = requireInteger(
      value,
      'requestCount',
      path,
      minimum: 1,
    );
    final waiterCount = requireInteger(value, 'waiterCount', path, minimum: 1);
    final createdAt = requireTimestamp(value, 'createdAt', path);
    final expiresAt = requireTimestamp(value, 'expiresAt', path);
    final resolvedAt = optionalTimestamp(value, 'resolvedAt', path);
    final decision = optionalString(value, 'decision', path);
    final decisionScope = optionalString(value, 'decisionScope', path);
    final terminalReason = _optionalApprovalIdentity(
      value,
      'terminalReason',
      path,
    );
    final scopeSupported =
        decisionScope == null ||
        decisionScope == 'request' ||
        kind == 'network_ask' && decisionScope == 'host_port';
    final stateConsistent = switch (state) {
      'pending' =>
        resolvedAt == null &&
            decision == null &&
            decisionScope == null &&
            terminalReason == null,
      'allowed' =>
        resolvedAt != null &&
            decision == 'allow-once' &&
            decisionScope != null &&
            terminalReason == null,
      'denied' =>
        resolvedAt != null &&
            decision == 'deny' &&
            decisionScope != null &&
            terminalReason != null,
      'canceled' || 'expired' =>
        resolvedAt != null &&
            decision == null &&
            decisionScope == null &&
            terminalReason != null,
      _ => false,
    };
    if (waiterCount > requestCount ||
        !expiresAt.isAfter(createdAt) ||
        resolvedAt != null && resolvedAt.isBefore(createdAt) ||
        !scopeSupported ||
        !stateConsistent) {
      throw ControlContractException(
        '$path lifecycle evidence is inconsistent',
      );
    }

    return ApprovalRecord(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      kind: kind,
      state: state,
      risk: risk,
      titleKey: titleKey,
      summaryKey: summaryKey,
      aggregateKey: _requireApprovalIdentity(value, 'aggregateKey', path),
      exchangeId: exchangeId,
      environmentId: environmentId,
      environmentRevision: environmentRevision,
      environmentDigest: environmentDigest,
      routeId: routeId,
      routeRevision: routeRevision,
      target: target,
      subjectRefs: List.unmodifiable(subjectRefs),
      subjectLabels: List.unmodifiable(subjectLabels),
      requestCount: requestCount,
      waiterCount: waiterCount,
      choices: List.unmodifiable(parsedChoices),
      createdAt: createdAt,
      expiresAt: expiresAt,
      resolvedAt: resolvedAt,
      decision: decision,
      decisionScope: decisionScope,
      terminalReason: terminalReason,
    );
  }

  final String id;
  final int revision;
  final String kind;
  final String state;
  final String risk;
  final String titleKey;
  final String summaryKey;
  final String aggregateKey;
  final String? exchangeId;
  final String? environmentId;
  final int? environmentRevision;
  final String? environmentDigest;
  final String? routeId;
  final int? routeRevision;
  final ApprovalTarget? target;
  final List<String> subjectRefs;
  final List<String> subjectLabels;
  final int requestCount;
  final int waiterCount;
  final List<ApprovalChoice> choices;
  final DateTime createdAt;
  final DateTime expiresAt;
  final DateTime? resolvedAt;
  final String? decision;
  final String? decisionScope;
  final String? terminalReason;
}

typedef _ApprovalPresentation = ({
  String risk,
  String titleKey,
  String summaryKey,
  Map<String, String> choices,
});

_ApprovalPresentation _approvalPresentation(String kind, String path) =>
    switch (kind) {
      'tool_intent' => (
        risk: 'high',
        titleKey: 'approval.toolIntent.title',
        summaryKey: 'approval.toolIntent.summary',
        choices: const {
          'allow-once\u0000request': 'approval.toolIntent.choice.allowOnce',
          'deny\u0000request': 'approval.toolIntent.choice.deny',
        },
      ),
      'network_ask' => (
        risk: 'medium',
        titleKey: 'approval.networkAsk.title',
        summaryKey: 'approval.networkAsk.summary',
        choices: const {
          'allow-once\u0000request': 'approval.networkAsk.choice.allowOnce',
          'allow-once\u0000host_port':
              'approval.networkAsk.choice.allowHostPort',
          'deny\u0000request': 'approval.networkAsk.choice.denyOnce',
          'deny\u0000host_port': 'approval.networkAsk.choice.denyHostPort',
        },
      ),
      'client_root_ask' => (
        risk: 'high',
        titleKey: 'approval.clientRootAsk.title',
        summaryKey: 'approval.clientRootAsk.summary',
        choices: const {
          'allow-once\u0000request': 'approval.clientRootAsk.choice.allowOnce',
          'deny\u0000request': 'approval.clientRootAsk.choice.denyOnce',
        },
      ),
      _ => throw ControlContractException('$path.kind is unsupported'),
    };

String _requireApprovalIdentity(JsonObject value, String key, String path) {
  final field = requireString(value, key, path);
  if (field.trim() != field ||
      utf8.encode(field).length > 512 ||
      _containsControlCharacter(field)) {
    throw ControlContractException('$path.$key must be a bounded identity');
  }
  return field;
}

String? _optionalApprovalIdentity(JsonObject value, String key, String path) {
  if (value[key] == null) return null;
  return _requireApprovalIdentity(value, key, path);
}

String? _optionalApprovalResourceId(JsonObject value, String key, String path) {
  if (value[key] == null) return null;
  return _requireResourceId(value, key, path);
}

List<String> _requireApprovalIdentityList(
  JsonObject value,
  String key,
  String path, {
  required int maximumItems,
}) {
  final items = requireList(value[key], '$path.$key');
  if (items.length > maximumItems) {
    throw ControlContractException('$path.$key has too many items');
  }
  return items.indexed
      .map((entry) {
        final item = entry.$2;
        if (item is! String) {
          throw ControlContractException('$path.$key[${entry.$1}] is invalid');
        }
        return _requireApprovalIdentity(
          <String, Object?>{'value': item},
          'value',
          '$path.$key[${entry.$1}]',
        );
      })
      .toList(growable: false);
}

final class ConnectionRecord {
  const ConnectionRecord({
    required this.sequence,
    required this.connectionId,
    required this.sourceLabel,
    required this.sourceConfidence,
    required this.environmentId,
    required this.environmentName,
    required this.requestedHost,
    required this.observedSni,
    required this.routeHost,
    required this.ip,
    required this.port,
    required this.decision,
    required this.ruleId,
    required this.egressScope,
    required this.egressSource,
    required this.decryption,
    required this.phase,
    required this.bytesUp,
    required this.bytesDown,
    required this.startedAt,
    required this.endedAt,
    required this.outcome,
    required this.errorClass,
  });

  factory ConnectionRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final port = requireInteger(value, 'port', path, minimum: 1);
    if (port > 65535) {
      throw ControlContractException('$path.port exceeds 65535');
    }
    return ConnectionRecord(
      sequence: requireInteger(value, 'sequence', path, minimum: 1),
      connectionId: requireString(value, 'connectionId', path),
      sourceLabel: optionalString(value, 'sourceLabel', path),
      sourceConfidence: requireString(value, 'sourceConfidence', path),
      environmentId: optionalString(value, 'environmentId', path),
      environmentName: optionalString(value, 'environmentName', path),
      requestedHost: requireString(value, 'requestedHost', path),
      observedSni: optionalString(value, 'observedSni', path),
      routeHost: optionalString(value, 'routeHost', path),
      ip: optionalString(value, 'ip', path),
      port: port,
      decision: optionalString(value, 'decision', path),
      ruleId: optionalString(value, 'ruleId', path),
      egressScope: optionalString(value, 'egressScope', path),
      egressSource: optionalString(value, 'egressSource', path),
      decryption: requireString(value, 'decryption', path),
      phase: requireString(value, 'phase', path),
      bytesUp: requireInteger(value, 'bytesUp', path),
      bytesDown: requireInteger(value, 'bytesDown', path),
      startedAt: requireTimestamp(value, 'startedAt', path),
      endedAt: optionalTimestamp(value, 'endedAt', path),
      outcome: optionalString(value, 'outcome', path),
      errorClass: optionalString(value, 'errorClass', path),
    );
  }

  final int sequence;
  final String connectionId;
  final String? sourceLabel;
  final String sourceConfidence;
  final String? environmentId;
  final String? environmentName;
  final String requestedHost;
  final String? observedSni;
  final String? routeHost;
  final String? ip;
  final int port;
  final String? decision;
  final String? ruleId;
  final String? egressScope;
  final String? egressSource;
  final String decryption;
  final String phase;
  final int bytesUp;
  final int bytesDown;
  final DateTime startedAt;
  final DateTime? endedAt;
  final String? outcome;
  final String? errorClass;
}

final class ConnectionPage {
  const ConnectionPage({required this.items, required this.nextCursor});

  final List<ConnectionRecord> items;
  final String? nextCursor;
}

final class EgressAttemptRecord {
  const EgressAttemptRecord({
    required this.sequence,
    required this.id,
    required this.connectionId,
    required this.purpose,
    required this.payloadClass,
    required this.parentKind,
    required this.parentId,
    required this.exchangeId,
    required this.caller,
    required this.callerId,
    required this.targetOrigin,
    required this.authority,
    required this.policyId,
    required this.ruleId,
    required this.proxyId,
    required this.reusedTransport,
    required this.startedAt,
    required this.terminal,
    required this.outcome,
    required this.errorClass,
    required this.bytesOut,
    required this.bytesIn,
    required this.completedAt,
  });

  factory EgressAttemptRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final parent = requireObject(value['parent'], '$path.parent');
    final decision = requireObject(value['decision'], '$path.decision');
    final reusedTransport = requireBoolean(value, 'reusedTransport', path);
    final terminal = requireBoolean(value, 'terminal', path);
    final completedAt = optionalTimestamp(value, 'completedAt', path);
    if (terminal != (completedAt != null)) {
      throw ControlContractException('$path terminal evidence is inconsistent');
    }
    return EgressAttemptRecord(
      sequence: requireInteger(value, 'sequence', path, minimum: 1),
      id: requireString(value, 'id', path),
      connectionId: optionalString(value, 'connectionId', path),
      purpose: requireString(value, 'purpose', path),
      payloadClass: requireString(value, 'payloadClass', path),
      parentKind: requireString(parent, 'kind', '$path.parent'),
      parentId: optionalString(parent, 'id', '$path.parent'),
      exchangeId: optionalString(parent, 'exchangeId', '$path.parent'),
      caller: requireString(value, 'caller', path),
      callerId: optionalString(value, 'callerId', path),
      targetOrigin: requireString(value, 'targetOrigin', path),
      authority: requireString(decision, 'authority', '$path.decision'),
      policyId: optionalString(decision, 'policyId', '$path.decision'),
      ruleId: optionalString(decision, 'ruleId', '$path.decision'),
      proxyId: optionalString(decision, 'proxyId', '$path.decision'),
      reusedTransport: reusedTransport,
      startedAt: requireTimestamp(value, 'startedAt', path),
      terminal: terminal,
      outcome: optionalString(value, 'outcome', path),
      errorClass: optionalString(value, 'errorClass', path),
      bytesOut: requireInteger(value, 'bytesOut', path),
      bytesIn: requireInteger(value, 'bytesIn', path),
      completedAt: completedAt,
    );
  }

  final int sequence;
  final String id;
  final String? connectionId;
  final String purpose;
  final String payloadClass;
  final String parentKind;
  final String? parentId;
  final String? exchangeId;
  final String caller;
  final String? callerId;
  final String targetOrigin;
  final String authority;
  final String? policyId;
  final String? ruleId;
  final String? proxyId;
  final bool reusedTransport;
  final DateTime startedAt;
  final bool terminal;
  final String? outcome;
  final String? errorClass;
  final int bytesOut;
  final int bytesIn;
  final DateTime? completedAt;
}

final class EgressAttemptPage {
  const EgressAttemptPage({required this.items, required this.nextCursor});

  final List<EgressAttemptRecord> items;
  final String? nextCursor;
}

final class ConnectionRule {
  const ConnectionRule({
    required this.id,
    required this.priority,
    required this.decision,
    required this.match,
    required this.host,
    required this.port,
  });

  factory ConnectionRule.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final match = requireString(value, 'match', path);
    final host = optionalString(value, 'host', path);
    final port = optionalInteger(value, 'port', path, minimum: 1);
    if (!const {'exact_host', 'exact_host_port'}.contains(match) ||
        host == null ||
        (match == 'exact_host' && port != null) ||
        (match == 'exact_host_port' && (port == null || port > 65535))) {
      throw ControlContractException('$path match authority is invalid');
    }
    return ConnectionRule(
      id: requireString(value, 'id', path),
      priority: requireInteger(value, 'priority', path),
      decision: requireString(value, 'decision', path),
      match: match,
      host: host,
      port: port,
    );
  }

  final String id;
  final int priority;
  final String decision;
  final String match;
  final String host;
  final int? port;

  JsonObject toJson() => {
    'id': id,
    'priority': priority,
    'decision': decision,
    'match': match,
    'host': host,
    if (port != null) 'port': port,
  };
}

final class ConnectionRuleSet {
  const ConnectionRuleSet({
    required this.revision,
    required this.rules,
    required this.mode,
  });

  factory ConnectionRuleSet.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final mode = requireString(value, 'mode', path);
    if (!const {'monitor', 'ask_unknown', 'deny_unknown'}.contains(mode)) {
      throw ControlContractException('$path.mode is unsupported');
    }
    final rules = requireList(value['rules'], '$path.rules');
    return ConnectionRuleSet(
      revision: requireInteger(value, 'revision', path, minimum: 1),
      rules: rules.indexed
          .map(
            (entry) =>
                ConnectionRule.fromJson(entry.$2, '$path.rules[${entry.$1}]'),
          )
          .toList(growable: false),
      mode: mode,
    );
  }

  final int revision;
  final List<ConnectionRule> rules;
  final String mode;
}

final class NetworkData {
  const NetworkData({
    required this.approvals,
    required this.connections,
    required this.egressAttempts,
    required this.rules,
  });

  final List<ApprovalRecord> approvals;
  final ConnectionPage connections;
  final EgressAttemptPage egressAttempts;
  final ConnectionRuleSet rules;
}

final class ManualCaptureRoot {
  const ManualCaptureRoot({
    required this.derSha256,
    required this.fingerprint,
    required this.pemPath,
  });

  factory ManualCaptureRoot.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'derSha256', 'fingerprint', 'pemPath'},
    );
    final digest = requireString(value, 'derSha256', path);
    final pemPath = requireString(value, 'pemPath', path);
    if (requireString(value, 'kind', path) != 'local_path' ||
        !RegExp(r'^[0-9a-f]{64}$').hasMatch(digest) ||
        !pemPath.startsWith('/') ||
        pemPath.contains('\u0000')) {
      throw ControlContractException('$path Root evidence is invalid');
    }
    return ManualCaptureRoot(
      derSha256: digest,
      fingerprint: requireString(value, 'fingerprint', path),
      pemPath: pemPath,
    );
  }

  final String derSha256;
  final String fingerprint;
  final String pemPath;
}

final class ManualCaptureContext {
  const ManualCaptureContext({
    required this.confirmationToken,
    required this.proxyAddress,
    required this.environmentId,
    required this.environmentRevision,
    required this.environmentDigest,
    required this.launchAuthorityDigest,
    required this.protectedAuthorities,
    required this.managedCredentialAuthorities,
    required this.defaultTemporarySeconds,
    required this.maxTemporarySeconds,
    required this.root,
  });

  factory ManualCaptureContext.fromJson(
    Object? json,
    String path, {
    required String expectedEnvironmentId,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'confirmationToken',
        'proxyAddress',
        'environmentId',
        'environmentRevision',
        'environmentDigest',
        'launchAuthorityDigest',
        'protectedAuthorities',
        'managedCredentialAuthorities',
        'defaultTemporarySeconds',
        'maxTemporarySeconds',
      },
      optional: const {'root'},
    );
    final confirmationToken = requireString(value, 'confirmationToken', path);
    final proxyAddress = requireString(value, 'proxyAddress', path);
    final environmentDigest = requireString(value, 'environmentDigest', path);
    final launchAuthorityDigest = requireString(
      value,
      'launchAuthorityDigest',
      path,
    );
    final defaultTemporarySeconds = requireInteger(
      value,
      'defaultTemporarySeconds',
      path,
      minimum: 1,
    );
    final maxTemporarySeconds = requireInteger(
      value,
      'maxTemporarySeconds',
      path,
      minimum: 1,
    );
    if (!RegExp(r'^ctx_[A-Za-z0-9_-]{43}$').hasMatch(confirmationToken) ||
        !_validManualProxyAddress(proxyAddress) ||
        requireString(value, 'environmentId', path) != expectedEnvironmentId ||
        !RegExp(r'^[0-9a-f]{64}$').hasMatch(environmentDigest) ||
        !RegExp(r'^[0-9a-f]{64}$').hasMatch(launchAuthorityDigest) ||
        defaultTemporarySeconds > maxTemporarySeconds) {
      throw ControlContractException('$path capture context is inconsistent');
    }
    return ManualCaptureContext(
      confirmationToken: confirmationToken,
      proxyAddress: proxyAddress,
      environmentId: expectedEnvironmentId,
      environmentRevision: requireInteger(
        value,
        'environmentRevision',
        path,
        minimum: 1,
      ),
      environmentDigest: environmentDigest,
      launchAuthorityDigest: launchAuthorityDigest,
      protectedAuthorities: requireStringList(
        value,
        'protectedAuthorities',
        path,
      ),
      managedCredentialAuthorities: requireStringList(
        value,
        'managedCredentialAuthorities',
        path,
      ),
      defaultTemporarySeconds: defaultTemporarySeconds,
      maxTemporarySeconds: maxTemporarySeconds,
      root: value['root'] == null
          ? null
          : ManualCaptureRoot.fromJson(value['root'], '$path.root'),
    );
  }

  final String confirmationToken;
  final String proxyAddress;
  final String environmentId;
  final int environmentRevision;
  final String environmentDigest;
  final String launchAuthorityDigest;
  final List<String> protectedAuthorities;
  final List<String> managedCredentialAuthorities;
  final int defaultTemporarySeconds;
  final int maxTemporarySeconds;
  final ManualCaptureRoot? root;
}

final class ManualCaptureRecord {
  const ManualCaptureRecord({
    required this.id,
    required this.displayName,
    required this.clientClass,
    required this.lifetime,
    required this.state,
    required this.observation,
    required this.createdAt,
    required this.updatedAt,
    required this.expiresAt,
    required this.lastObservedAt,
  });

  factory ManualCaptureRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'displayName',
        'clientClass',
        'lifetime',
        'state',
        'observation',
        'createdAt',
        'updatedAt',
      },
      optional: const {'expiresAt', 'lastObservedAt'},
    );
    final clientClass = requireString(value, 'clientClass', path);
    final lifetime = requireString(value, 'lifetime', path);
    final state = requireString(value, 'state', path);
    final observation = requireString(value, 'observation', path);
    final expiresAt = optionalTimestamp(value, 'expiresAt', path);
    if (!const {'cli', 'desktop_app', 'other'}.contains(clientClass) ||
        !const {'temporary', 'until_revoked'}.contains(lifetime) ||
        !const {'active', 'revoked', 'expired'}.contains(state) ||
        !const {'waiting_for_traffic', 'observed'}.contains(observation) ||
        (lifetime == 'temporary') != (expiresAt != null)) {
      throw ControlContractException('$path capture state is invalid');
    }
    return ManualCaptureRecord(
      id: requireString(value, 'id', path),
      displayName: requireString(value, 'displayName', path),
      clientClass: clientClass,
      lifetime: lifetime,
      state: state,
      observation: observation,
      createdAt: requireTimestamp(value, 'createdAt', path),
      updatedAt: requireTimestamp(value, 'updatedAt', path),
      expiresAt: expiresAt,
      lastObservedAt: optionalTimestamp(value, 'lastObservedAt', path),
    );
  }

  final String id;
  final String displayName;
  final String clientClass;
  final String lifetime;
  final String state;
  final String observation;
  final DateTime createdAt;
  final DateTime updatedAt;
  final DateTime? expiresAt;
  final DateTime? lastObservedAt;
}

final class ManualCaptureGrant {
  const ManualCaptureGrant({
    required this.capture,
    required this.proxyAddress,
    required this.proxyUsername,
    required this.proxyPassword,
    required this.environmentId,
    required this.assignmentRevision,
    required this.launchAuthorityDigest,
    required this.protectedAuthorities,
    required this.managedCredentialAuthorities,
    required this.root,
  });

  factory ManualCaptureGrant.fromJson(
    Object? json,
    String path, {
    String? expectedCaptureId,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'capture',
        'proxyAddress',
        'proxyUsername',
        'proxyPassword',
        'environmentId',
        'assignmentRevision',
        'launchAuthorityDigest',
        'protectedAuthorities',
        'managedCredentialAuthorities',
      },
      optional: const {'root'},
    );
    final capture = ManualCaptureRecord.fromJson(
      value['capture'],
      '$path.capture',
    );
    final password = requireString(value, 'proxyPassword', path);
    final launchDigest = requireString(value, 'launchAuthorityDigest', path);
    if ((expectedCaptureId != null && capture.id != expectedCaptureId) ||
        !_validManualProxyAddress(requireString(value, 'proxyAddress', path)) ||
        utf8.encode(password).length > 2048 ||
        !RegExp(r'^[0-9a-f]{64}$').hasMatch(launchDigest)) {
      throw ControlContractException('$path capture grant is inconsistent');
    }
    return ManualCaptureGrant(
      capture: capture,
      proxyAddress: requireString(value, 'proxyAddress', path),
      proxyUsername: requireString(value, 'proxyUsername', path),
      proxyPassword: password,
      environmentId: requireString(value, 'environmentId', path),
      assignmentRevision: requireInteger(
        value,
        'assignmentRevision',
        path,
        minimum: 1,
      ),
      launchAuthorityDigest: launchDigest,
      protectedAuthorities: requireStringList(
        value,
        'protectedAuthorities',
        path,
      ),
      managedCredentialAuthorities: requireStringList(
        value,
        'managedCredentialAuthorities',
        path,
      ),
      root: value['root'] == null
          ? null
          : ManualCaptureRoot.fromJson(value['root'], '$path.root'),
    );
  }

  final ManualCaptureRecord capture;
  final String proxyAddress;
  final String proxyUsername;
  final String proxyPassword;
  final String environmentId;
  final int assignmentRevision;
  final String launchAuthorityDigest;
  final List<String> protectedAuthorities;
  final List<String> managedCredentialAuthorities;
  final ManualCaptureRoot? root;
}

final class ManualCaptureStateTag {
  const ManualCaptureStateTag({required this.capture, required this.stateTag});

  factory ManualCaptureStateTag.fromJson(Object? json, String stateTag) =>
      ManualCaptureStateTag(
        capture: ManualCaptureRecord.fromJson(json, 'manualCapture'),
        stateTag: stateTag,
      );

  final ManualCaptureRecord capture;
  final String stateTag;

  String get state => capture.state;
}

final class ManualCaptureGrantStateTag {
  const ManualCaptureGrantStateTag({
    required this.grant,
    required this.stateTag,
  });

  final ManualCaptureGrant grant;
  final String stateTag;
}

bool _validManualProxyAddress(String value) {
  final parsed = Uri.tryParse(value);
  return parsed != null &&
      parsed.scheme == 'http' &&
      parsed.host == '127.0.0.1' &&
      parsed.hasPort &&
      parsed.userInfo.isEmpty &&
      (parsed.path.isEmpty || parsed.path == '/') &&
      !parsed.hasQuery &&
      !parsed.hasFragment;
}

final class DashboardData {
  const DashboardData({
    required this.status,
    required this.captures,
    required this.environments,
    required this.endpoints,
    required this.accounts,
  });

  final RuntimeStatus status;
  final List<CaptureRecord> captures;
  final List<EnvironmentRecord> environments;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
}

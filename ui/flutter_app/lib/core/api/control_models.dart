import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;

import 'provider_origin.dart';

typedef JsonObject = Map<String, Object?>;

const upstreamBackendProtocols = <String>[
  'anthropic_messages',
  'openai_responses',
  'openai_chat',
];

bool validUpstreamBackendProtocols(List<String> values) =>
    values.isNotEmpty &&
    values.length <= upstreamBackendProtocols.length &&
    values.toSet().length == values.length &&
    values.every(upstreamBackendProtocols.contains);

final class ControlContractException implements Exception {
  const ControlContractException(this.message);

  final String message;

  @override
  String toString() => 'Control contract error: $message';
}

/// The fixed remote-access contract of one Runtime Server.
///
/// There is no per-run approval mode: a Runtime User logs in once and reuses
/// the revocable Login Session until logout, disablement, or expiry.
final class RuntimeServerAccess {
  const RuntimeServerAccess({
    required this.transport,
    required this.authentication,
    required this.sessionPolicy,
    required this.targets,
  });

  factory RuntimeServerAccess.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'schema',
        'transport',
        'authentication',
        'sessionPolicy',
        'targets',
      },
    );
    if (requireString(value, 'schema', path) != 'vibermate-server-access-v2') {
      throw ControlContractException('$path schema is unsupported');
    }
    final transport = requireString(value, 'transport', path);
    final authentication = requireString(value, 'authentication', path);
    final sessionPolicy = requireString(value, 'sessionPolicy', path);
    final rawTargets = requireList(value['targets'], '$path.targets');
    final targets = <String>[];
    for (final (index, item) in rawTargets.indexed) {
      if (item is! String || item.isEmpty) {
        throw ControlContractException('$path.targets[$index] is invalid');
      }
      targets.add(item);
    }
    if (!const {'http', 'https'}.contains(transport) ||
        authentication != 'runtime_user_password' ||
        sessionPolicy != 'reusable_until_logout_disable_or_expiry' ||
        targets.isEmpty ||
        targets.length > 32 ||
        targets.toSet().length != targets.length ||
        !targets.every(_validRuntimeServerTarget)) {
      throw ControlContractException('$path access contract is unsupported');
    }
    return RuntimeServerAccess(
      transport: transport,
      authentication: authentication,
      sessionPolicy: sessionPolicy,
      targets: List.unmodifiable(targets),
    );
  }

  final String transport;
  final String authentication;
  final String sessionPolicy;
  final List<String> targets;

  String get preferredTarget => targets.first;

  bool get encrypted => transport == 'https';

  bool get requiresRuntimeUserLogin =>
      authentication == 'runtime_user_password';
}

bool _validRuntimeServerTarget(String value) {
  final uri = Uri.tryParse('http://$value');
  if (uri == null ||
      !uri.hasPort ||
      uri.port < 1 ||
      uri.port > 65535 ||
      uri.userInfo.isNotEmpty ||
      uri.path.isNotEmpty ||
      uri.query.isNotEmpty ||
      uri.fragment.isNotEmpty) {
    return false;
  }
  final host = uri.host;
  if (host == '0.0.0.0' || host == '::') return false;
  final parts = host.split('.');
  if (parts.length == 4 &&
      parts.every((part) {
        final parsed = int.tryParse(part);
        return parsed != null &&
            parsed >= 0 &&
            parsed <= 255 &&
            part == '$parsed';
      })) {
    return true;
  }
  return host.contains(':') && RegExp(r'^[0-9A-Fa-f:.]+$').hasMatch(host);
}

final class RuntimeUser {
  const RuntimeUser({
    required this.id,
    required this.username,
    required this.state,
    required this.createdAt,
    required this.updatedAt,
  });

  factory RuntimeUser.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'username', 'state', 'createdAt', 'updatedAt'},
    );
    final state = requireString(value, 'state', path);
    if (!const {'active', 'disabled'}.contains(state)) {
      throw ControlContractException('$path.state is unsupported');
    }
    final createdAt = requireTimestamp(value, 'createdAt', path);
    final updatedAt = requireTimestamp(value, 'updatedAt', path);
    if (updatedAt.isBefore(createdAt)) {
      throw ControlContractException('$path timestamps are inconsistent');
    }
    return RuntimeUser(
      id: requireString(value, 'id', path),
      username: requireString(value, 'username', path),
      state: state,
      createdAt: createdAt,
      updatedAt: updatedAt,
    );
  }

  final String id;
  final String username;
  final String state;
  final DateTime createdAt;
  final DateTime updatedAt;

  bool get active => state == 'active';
}

final class RuntimeUsageQuery {
  const RuntimeUsageQuery({
    required this.from,
    required this.until,
    required this.timeZone,
  });

  final String from;
  final String until;
  final String timeZone;

  Map<String, String> toQueryParameters() {
    _validateUsageWindow(from, until, timeZone, 'runtimeUsageQuery');
    return {'from': from, 'until': until, 'timeZone': timeZone};
  }
}

final class RuntimeUsagePeriod {
  const RuntimeUsagePeriod({
    required this.from,
    required this.until,
    required this.timeZone,
  });

  factory RuntimeUsagePeriod.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'from', 'until', 'timeZone'});
    final from = requireString(value, 'from', path);
    final until = requireString(value, 'until', path);
    final timeZone = requireString(value, 'timeZone', path);
    _validateUsageWindow(from, until, timeZone, path);
    return RuntimeUsagePeriod(from: from, until: until, timeZone: timeZone);
  }

  final String from;
  final String until;
  final String timeZone;
}

final class RuntimeUsageReport {
  const RuntimeUsageReport({
    required this.generatedAt,
    required this.period,
    required this.truncated,
    required this.days,
    required this.users,
  });

  factory RuntimeUsageReport.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'schema',
        'generatedAt',
        'period',
        'truncated',
        'days',
        'users',
      },
    );
    if (requireString(value, 'schema', path) !=
        'vibermate-runtime-usage-report-v2') {
      throw ControlContractException('$path schema is unsupported');
    }
    final period = RuntimeUsagePeriod.fromJson(value['period'], '$path.period');
    final days = _runtimeUsageDays(value['days'], '$path.days', period);
    final users = requireList(value['users'], '$path.users').indexed
        .map(
          (entry) =>
              RuntimeUserUsage.fromJson(entry.$2, '$path.users[${entry.$1}]'),
        )
        .toList(growable: false);
    if (users.length > 10000) {
      throw ControlContractException('$path contains too many users');
    }
    for (final (index, user) in users.indexed) {
      _validateUsageDaysWithinPeriod(
        user.days,
        period,
        '$path.users[$index].days',
      );
    }
    return RuntimeUsageReport(
      generatedAt: requireTimestamp(value, 'generatedAt', path),
      period: period,
      truncated: requireBoolean(value, 'truncated', path),
      days: days,
      users: users,
    );
  }

  final DateTime generatedAt;
  final RuntimeUsagePeriod period;
  final bool truncated;
  final List<RuntimeDayUsage> days;
  final List<RuntimeUserUsage> users;
}

final class RuntimeUserUsage {
  const RuntimeUserUsage({
    required this.userId,
    required this.username,
    required this.state,
    required this.captureRuns,
    required this.activeRuns,
    required this.turns,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.contentUnavailableTurns,
    required this.modelUnavailableTurns,
    required this.tokens,
    required this.latestContext,
    required this.lastActivityAt,
    required this.days,
    required this.models,
    required this.contexts,
    required this.agentSessions,
  });

  factory RuntimeUserUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'userId',
        'username',
        'state',
        'captureRuns',
        'activeRuns',
        'turns',
        'succeeded',
        'failed',
        'canceled',
        'contentUnavailableTurns',
        'modelUnavailableTurns',
        'tokens',
        'days',
        'models',
        'contexts',
        'agentSessions',
      },
      optional: const {'latestContext', 'lastActivityAt'},
    );
    final state = requireString(value, 'state', path);
    if (!const {'active', 'disabled'}.contains(state)) {
      throw ControlContractException('$path.state is unsupported');
    }
    final turns = requireInteger(value, 'turns', path);
    final succeeded = requireInteger(value, 'succeeded', path);
    final failed = requireInteger(value, 'failed', path);
    final canceled = requireInteger(value, 'canceled', path);
    if (succeeded + failed + canceled > turns) {
      throw ControlContractException('$path terminal counters exceed turns');
    }
    final latest = value['latestContext'];
    final days = _runtimeUsageDays(value['days'], '$path.days', null);
    return RuntimeUserUsage(
      userId: requireString(value, 'userId', path),
      username: requireString(value, 'username', path),
      state: state,
      captureRuns: requireInteger(value, 'captureRuns', path),
      activeRuns: requireInteger(value, 'activeRuns', path),
      turns: turns,
      succeeded: succeeded,
      failed: failed,
      canceled: canceled,
      contentUnavailableTurns: requireInteger(
        value,
        'contentUnavailableTurns',
        path,
      ),
      modelUnavailableTurns: requireInteger(
        value,
        'modelUnavailableTurns',
        path,
      ),
      tokens: RuntimeTokenUsage.fromJson(value['tokens'], '$path.tokens'),
      latestContext: latest == null
          ? null
          : RuntimeUsageContextRef.fromJson(latest, '$path.latestContext'),
      lastActivityAt: optionalTimestamp(value, 'lastActivityAt', path),
      days: days,
      models: _runtimeUsageList(
        value['models'],
        '$path.models',
        RuntimeModelUsage.fromJson,
      ),
      contexts: _runtimeUsageList(
        value['contexts'],
        '$path.contexts',
        RuntimeContextUsage.fromJson,
      ),
      agentSessions: _runtimeUsageList(
        value['agentSessions'],
        '$path.agentSessions',
        RuntimeAgentSessionUsage.fromJson,
      ),
    );
  }

  final String userId;
  final String username;
  final String state;
  final int captureRuns;
  final int activeRuns;
  final int turns;
  final int succeeded;
  final int failed;
  final int canceled;
  final int contentUnavailableTurns;
  final int modelUnavailableTurns;
  final RuntimeTokenUsage tokens;
  final RuntimeUsageContextRef? latestContext;
  final DateTime? lastActivityAt;
  final List<RuntimeDayUsage> days;
  final List<RuntimeModelUsage> models;
  final List<RuntimeContextUsage> contexts;
  final List<RuntimeAgentSessionUsage> agentSessions;

  bool get active => state == 'active';
  bool get partial => contentUnavailableTurns > 0 || modelUnavailableTurns > 0;
}

final class RuntimeDayUsage {
  const RuntimeDayUsage({
    required this.date,
    required this.turns,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.contentUnavailableTurns,
    required this.modelUnavailableTurns,
    required this.tokens,
  });

  factory RuntimeDayUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'date',
        'turns',
        'succeeded',
        'failed',
        'canceled',
        'contentUnavailableTurns',
        'modelUnavailableTurns',
        'tokens',
      },
    );
    final date = requireString(value, 'date', path);
    _parseUsageDate(date, '$path.date');
    final turns = requireInteger(value, 'turns', path);
    final succeeded = requireInteger(value, 'succeeded', path);
    final failed = requireInteger(value, 'failed', path);
    final canceled = requireInteger(value, 'canceled', path);
    final contentUnavailableTurns = requireInteger(
      value,
      'contentUnavailableTurns',
      path,
    );
    final modelUnavailableTurns = requireInteger(
      value,
      'modelUnavailableTurns',
      path,
    );
    if (turns <= 0 ||
        succeeded + failed + canceled > turns ||
        contentUnavailableTurns > turns ||
        modelUnavailableTurns > turns) {
      throw ControlContractException('$path counters are inconsistent');
    }
    return RuntimeDayUsage(
      date: date,
      turns: turns,
      succeeded: succeeded,
      failed: failed,
      canceled: canceled,
      contentUnavailableTurns: contentUnavailableTurns,
      modelUnavailableTurns: modelUnavailableTurns,
      tokens: RuntimeTokenUsage.fromJson(value['tokens'], '$path.tokens'),
    );
  }

  final String date;
  final int turns;
  final int succeeded;
  final int failed;
  final int canceled;
  final int contentUnavailableTurns;
  final int modelUnavailableTurns;
  final RuntimeTokenUsage tokens;

  bool get partial => contentUnavailableTurns > 0 || modelUnavailableTurns > 0;
}

final class RuntimeUsageContextRef {
  const RuntimeUsageContextRef({
    required this.loginSessionId,
    required this.deviceName,
    required this.machineId,
    required this.workspaceId,
    required this.workspaceLabel,
    required this.observedAt,
  });

  factory RuntimeUsageContextRef.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'loginSessionId',
        'deviceName',
        'machineId',
        'observedAt',
      },
      optional: const {'workspaceId', 'workspaceLabel'},
    );
    return RuntimeUsageContextRef(
      loginSessionId: requireString(value, 'loginSessionId', path),
      deviceName: requireString(value, 'deviceName', path),
      machineId: requireString(value, 'machineId', path),
      workspaceId: optionalString(value, 'workspaceId', path),
      workspaceLabel: optionalString(value, 'workspaceLabel', path),
      observedAt: requireTimestamp(value, 'observedAt', path),
    );
  }

  final String loginSessionId;
  final String deviceName;
  final String machineId;
  final String? workspaceId;
  final String? workspaceLabel;
  final DateTime observedAt;
}

final class RuntimeContextUsage {
  const RuntimeContextUsage({
    required this.loginSessionId,
    required this.deviceName,
    required this.machineId,
    required this.workspaceId,
    required this.workspaceLabel,
    required this.captureRuns,
    required this.activeRuns,
    required this.turns,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.tokens,
    required this.lastActivityAt,
  });

  factory RuntimeContextUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'loginSessionId',
        'deviceName',
        'machineId',
        'captureRuns',
        'activeRuns',
        'turns',
        'succeeded',
        'failed',
        'canceled',
        'tokens',
      },
      optional: const {'workspaceId', 'workspaceLabel', 'lastActivityAt'},
    );
    return RuntimeContextUsage(
      loginSessionId: requireString(value, 'loginSessionId', path),
      deviceName: requireString(value, 'deviceName', path),
      machineId: requireString(value, 'machineId', path),
      workspaceId: optionalString(value, 'workspaceId', path),
      workspaceLabel: optionalString(value, 'workspaceLabel', path),
      captureRuns: requireInteger(value, 'captureRuns', path),
      activeRuns: requireInteger(value, 'activeRuns', path),
      turns: requireInteger(value, 'turns', path),
      succeeded: requireInteger(value, 'succeeded', path),
      failed: requireInteger(value, 'failed', path),
      canceled: requireInteger(value, 'canceled', path),
      tokens: RuntimeTokenUsage.fromJson(value['tokens'], '$path.tokens'),
      lastActivityAt: optionalTimestamp(value, 'lastActivityAt', path),
    );
  }

  final String loginSessionId;
  final String deviceName;
  final String machineId;
  final String? workspaceId;
  final String? workspaceLabel;
  final int captureRuns;
  final int activeRuns;
  final int turns;
  final int succeeded;
  final int failed;
  final int canceled;
  final RuntimeTokenUsage tokens;
  final DateTime? lastActivityAt;
}

final class RuntimeModelUsage {
  const RuntimeModelUsage({
    required this.requestedModel,
    required this.upstreamModel,
    required this.turns,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.tokens,
  });

  factory RuntimeModelUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'requestedModel',
        'upstreamModel',
        'turns',
        'succeeded',
        'failed',
        'canceled',
        'tokens',
      },
    );
    return RuntimeModelUsage(
      requestedModel: requireString(value, 'requestedModel', path),
      upstreamModel: requireString(value, 'upstreamModel', path),
      turns: requireInteger(value, 'turns', path),
      succeeded: requireInteger(value, 'succeeded', path),
      failed: requireInteger(value, 'failed', path),
      canceled: requireInteger(value, 'canceled', path),
      tokens: RuntimeTokenUsage.fromJson(value['tokens'], '$path.tokens'),
    );
  }

  final String requestedModel;
  final String upstreamModel;
  final int turns;
  final int succeeded;
  final int failed;
  final int canceled;
  final RuntimeTokenUsage tokens;
}

final class RuntimeAgentSessionUsage {
  const RuntimeAgentSessionUsage({
    required this.client,
    required this.sessionId,
    required this.captureRuns,
    required this.turns,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.tokens,
    required this.lastActivityAt,
  });

  factory RuntimeAgentSessionUsage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'client',
        'sessionId',
        'captureRuns',
        'turns',
        'succeeded',
        'failed',
        'canceled',
        'tokens',
        'lastActivityAt',
      },
    );
    return RuntimeAgentSessionUsage(
      client: requireString(value, 'client', path),
      sessionId: requireString(value, 'sessionId', path),
      captureRuns: requireInteger(value, 'captureRuns', path),
      turns: requireInteger(value, 'turns', path),
      succeeded: requireInteger(value, 'succeeded', path),
      failed: requireInteger(value, 'failed', path),
      canceled: requireInteger(value, 'canceled', path),
      tokens: RuntimeTokenUsage.fromJson(value['tokens'], '$path.tokens'),
      lastActivityAt: requireTimestamp(value, 'lastActivityAt', path),
    );
  }

  final String client;
  final String sessionId;
  final int captureRuns;
  final int turns;
  final int succeeded;
  final int failed;
  final int canceled;
  final RuntimeTokenUsage tokens;
  final DateTime lastActivityAt;
}

final class RuntimeTokenUsage {
  const RuntimeTokenUsage({
    required this.inputUncached,
    required this.cacheWrite,
    required this.cacheRead,
    required this.output,
    required this.reasoning,
  });

  factory RuntimeTokenUsage.fromJson(Object? json, String path) {
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
    RuntimeTokenAggregate field(String key) =>
        RuntimeTokenAggregate.fromJson(value[key], '$path.$key');
    return RuntimeTokenUsage(
      inputUncached: field('inputUncached'),
      cacheWrite: field('cacheWrite'),
      cacheRead: field('cacheRead'),
      output: field('output'),
      reasoning: field('reasoning'),
    );
  }

  final RuntimeTokenAggregate inputUncached;
  final RuntimeTokenAggregate cacheWrite;
  final RuntimeTokenAggregate cacheRead;
  final RuntimeTokenAggregate output;
  final RuntimeTokenAggregate reasoning;
}

final class RuntimeTokenAggregate {
  const RuntimeTokenAggregate({
    required this.tokens,
    required this.knownTurns,
    required this.unknownTurns,
  });

  factory RuntimeTokenAggregate.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'tokens', 'knownTurns', 'unknownTurns'},
    );
    return RuntimeTokenAggregate(
      tokens: requireInteger(value, 'tokens', path),
      knownTurns: requireInteger(value, 'knownTurns', path),
      unknownTurns: requireInteger(value, 'unknownTurns', path),
    );
  }

  final int tokens;
  final int knownTurns;
  final int unknownTurns;

  bool get complete => unknownTurns == 0;
  bool get observed => knownTurns > 0;
}

List<T> _runtimeUsageList<T>(
  Object? json,
  String path,
  T Function(Object?, String) parse,
) {
  final items = requireList(json, path);
  if (items.length > 100000) {
    throw ControlContractException('$path is too large');
  }
  return items.indexed
      .map((entry) => parse(entry.$2, '$path[${entry.$1}]'))
      .toList(growable: false);
}

List<RuntimeDayUsage> _runtimeUsageDays(
  Object? json,
  String path,
  RuntimeUsagePeriod? period,
) {
  final items = requireList(json, path);
  if (items.length > 366) {
    throw ControlContractException('$path contains too many days');
  }
  final days = items.indexed
      .map((entry) => RuntimeDayUsage.fromJson(entry.$2, '$path[${entry.$1}]'))
      .toList(growable: false);
  var previous = '';
  for (final (index, day) in days.indexed) {
    if (day.date.compareTo(previous) <= 0) {
      throw ControlContractException('$path[$index] is not strictly ordered');
    }
    previous = day.date;
  }
  if (period != null) {
    _validateUsageDaysWithinPeriod(days, period, path);
  }
  return days;
}

void _validateUsageDaysWithinPeriod(
  List<RuntimeDayUsage> days,
  RuntimeUsagePeriod period,
  String path,
) {
  for (final (index, day) in days.indexed) {
    if (day.date.compareTo(period.from) < 0 ||
        day.date.compareTo(period.until) >= 0) {
      throw ControlContractException('$path[$index] is outside the period');
    }
  }
}

void _validateUsageWindow(
  String from,
  String until,
  String timeZone,
  String path,
) {
  final fromDate = _parseUsageDate(from, '$path.from');
  final untilDate = _parseUsageDate(until, '$path.until');
  final dayCount = untilDate.difference(fromDate).inDays;
  if (dayCount < 1 || dayCount > 366) {
    throw ControlContractException('$path period must contain 1 to 366 days');
  }
  final timeZoneBytes = utf8.encode(timeZone);
  if (timeZone.isEmpty ||
      timeZoneBytes.length > 128 ||
      timeZone.runes.any(
        (character) => character < 0x20 || character == 0x7f,
      )) {
    throw ControlContractException('$path.timeZone is invalid');
  }
}

DateTime _parseUsageDate(String value, String path) {
  final match = RegExp(r'^(\d{4})-(\d{2})-(\d{2})$').firstMatch(value);
  if (match == null) {
    throw ControlContractException('$path must be YYYY-MM-DD');
  }
  final year = int.parse(match.group(1)!);
  final month = int.parse(match.group(2)!);
  final day = int.parse(match.group(3)!);
  final parsed = DateTime.utc(year, month, day);
  if (parsed.year != year || parsed.month != month || parsed.day != day) {
    throw ControlContractException('$path is not a civil date');
  }
  return parsed;
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
final _conversationProjectionPattern = RegExp(
  r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,511}$',
);
final _clientEvidenceNamePattern = RegExp(r'^[a-z0-9._-]{1,128}$');
final _digestPattern = RegExp(r'^[0-9a-f]{64}$');

bool _validRawIdentity(String value) =>
    value.isNotEmpty &&
    utf8.encode(value).length <= 512 &&
    !RegExp(r'[\u0000\r\n\t]').hasMatch(value);

bool _validRawMetadata(String value, {int maximumBytes = 4096}) =>
    utf8.encode(value).length <= maximumBytes &&
    !RegExp(r'[\u0000\r\n]').hasMatch(value);

bool _validClientIdentityText(String value, {bool allowEmpty = false}) =>
    (allowEmpty || value.isNotEmpty) &&
    value.trim() == value &&
    utf8.encode(value).length <= 512 &&
    !value.contains('\uFEFF') &&
    !_containsControlCharacter(value);

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
    // Keep the JSON number exact on both native Dart and JavaScript. Values
    // above JS's safe-integer boundary cannot be displayed as trustworthy
    // byte evidence even though the Go authority uses int64 internally.
    if (heldBytes > 9007199254740991 || enteringActions > activeActions) {
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
    if (!const {'desktop', 'server'}.contains(host) ||
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
    final originText = requireString(value, 'origin', path);
    final origin = Uri.tryParse(originText);
    if (origin == null || !isCanonicalProviderOrigin(originText)) {
      throw ControlContractException(
        '$path.origin must be a canonical provider origin',
      );
    }
    final backendProtocols = requireStringList(value, 'backendProtocols', path);
    if (!validUpstreamBackendProtocols(backendProtocols)) {
      throw ControlContractException(
        '$path.backendProtocols must be a non-empty explicit protocol set',
      );
    }
    return UpstreamEndpoint(
      id: requireString(value, 'id', path),
      displayName: requireString(value, 'displayName', path),
      origin: origin,
      realmId: requireString(value, 'realmId', path),
      backendProtocols: List.unmodifiable(backendProtocols),
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

/// One opaque model ID advertised by a particular upstream Endpoint.
///
/// This is availability evidence, not a provider taxonomy. The ID is retained
/// exactly as returned by `Endpoint + /v1/models`.
final class UpstreamModel {
  const UpstreamModel({
    required this.id,
    required this.displayName,
    required this.ownedBy,
    required this.verifiedAvailable,
    required this.contextLimit,
    required this.outputLimit,
  });

  factory UpstreamModel.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'displayName',
        'ownedBy',
        'verifiedAvailable',
        'contextLimit',
        'outputLimit',
      },
    );
    final id = requireString(value, 'id', path);
    final displayName = requireStringValue(value, 'displayName', path);
    final ownedBy = requireStringValue(value, 'ownedBy', path);
    if (!_validOpaqueModelId(id) ||
        !_validOptionalCatalogText(displayName, maximumBytes: 512) ||
        !_validOptionalCatalogText(ownedBy, maximumBytes: 512)) {
      throw ControlContractException('$path Endpoint model is invalid');
    }
    return UpstreamModel(
      id: id,
      displayName: displayName,
      ownedBy: ownedBy,
      verifiedAvailable: requireBoolean(value, 'verifiedAvailable', path),
      contextLimit: requireInteger(value, 'contextLimit', path, minimum: 0),
      outputLimit: requireInteger(value, 'outputLimit', path, minimum: 0),
    );
  }

  final String id;
  final String displayName;
  final String ownedBy;
  final bool verifiedAvailable;
  final int contextLimit;
  final int outputLimit;
}

/// A point-in-time model catalog for exactly one upstream Endpoint.
///
/// Availability is proved live through one explicit Account belonging to the
/// Endpoint. No metadata directory participates in this authority.
final class UpstreamModelCatalog {
  const UpstreamModelCatalog({
    required this.endpointId,
    required this.endpointRevision,
    required this.accountId,
    required this.accountRevision,
    required this.credentialEpoch,
    required this.observedAt,
    required this.availabilitySource,
    required this.models,
  });

  factory UpstreamModelCatalog.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'endpointId',
        'endpointRevision',
        'accountId',
        'accountRevision',
        'credentialEpoch',
        'observedAt',
        'availabilitySource',
        'models',
      },
    );
    final endpointId = _requireResourceId(value, 'endpointId', path);
    final accountId = _requireResourceId(value, 'accountId', path);
    final availabilitySource = requireString(value, 'availabilitySource', path);
    final rawModels = requireList(value['models'], '$path.models');
    if (availabilitySource != 'endpoint' || rawModels.length > 16384) {
      throw ControlContractException('$path catalog authority is invalid');
    }
    final models = rawModels.indexed
        .map(
          (entry) =>
              UpstreamModel.fromJson(entry.$2, '$path.models[${entry.$1}]'),
        )
        .toList(growable: false);
    final verified = availabilitySource == 'endpoint';
    if (models.map((model) => model.id).toSet().length != models.length ||
        models.any((model) => model.verifiedAvailable != verified)) {
      throw ControlContractException('$path model availability is invalid');
    }
    return UpstreamModelCatalog(
      endpointId: endpointId,
      endpointRevision: requireInteger(
        value,
        'endpointRevision',
        path,
        minimum: 1,
      ),
      accountId: accountId,
      accountRevision: requireInteger(
        value,
        'accountRevision',
        path,
        minimum: 1,
      ),
      credentialEpoch: requireInteger(
        value,
        'credentialEpoch',
        path,
        minimum: 1,
      ),
      observedAt: requireTimestamp(value, 'observedAt', path),
      availabilitySource: availabilitySource,
      models: List.unmodifiable(models),
    );
  }

  final String endpointId;
  final int endpointRevision;
  final String accountId;
  final int accountRevision;
  final int credentialEpoch;
  final DateTime observedAt;
  final String availabilitySource;
  final List<UpstreamModel> models;

  bool get verifiedFromEndpoint => availabilitySource == 'endpoint';
}

/// Descriptive metadata for a request-side model ID that an Agent may send.
/// This never proves that an upstream Endpoint accepts the same identifier.
final class ClientModel {
  const ClientModel({
    required this.id,
    required this.canonicalId,
    required this.displayName,
    required this.description,
    required this.family,
    required this.reasoning,
    required this.toolCalls,
    required this.structuredOutput,
    required this.attachments,
    required this.openWeights,
    required this.contextLimit,
    required this.outputLimit,
    required this.inputModalities,
    required this.outputModalities,
    required this.knowledgeCutoff,
    required this.releaseDate,
  });

  factory ClientModel.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'canonicalId',
        'displayName',
        'description',
        'family',
        'reasoning',
        'toolCalls',
        'structuredOutput',
        'attachments',
        'openWeights',
        'contextLimit',
        'outputLimit',
        'inputModalities',
        'outputModalities',
        'knowledgeCutoff',
        'releaseDate',
      },
    );
    final id = requireString(value, 'id', path);
    final canonicalId = requireString(value, 'canonicalId', path);
    final displayName = requireStringValue(value, 'displayName', path);
    final description = requireStringValue(value, 'description', path);
    final family = requireStringValue(value, 'family', path);
    final knowledgeCutoff = requireStringValue(value, 'knowledgeCutoff', path);
    final releaseDate = requireStringValue(value, 'releaseDate', path);
    final inputs = requireStringList(value, 'inputModalities', path);
    final outputs = requireStringList(value, 'outputModalities', path);
    if (!_validCatalogText(id, maximumBytes: 256) ||
        !_validCatalogText(canonicalId, maximumBytes: 512) ||
        !_validOptionalCatalogText(displayName, maximumBytes: 512) ||
        !_validOptionalCatalogText(description, maximumBytes: 8192) ||
        !_validOptionalCatalogText(family, maximumBytes: 512) ||
        !_validOptionalCatalogText(knowledgeCutoff, maximumBytes: 512) ||
        !_validOptionalCatalogText(releaseDate, maximumBytes: 512) ||
        inputs.toSet().length != inputs.length ||
        outputs.toSet().length != outputs.length) {
      throw ControlContractException('$path client model metadata is invalid');
    }
    return ClientModel(
      id: id,
      canonicalId: canonicalId,
      displayName: displayName,
      description: description,
      family: family,
      reasoning: requireBoolean(value, 'reasoning', path),
      toolCalls: requireBoolean(value, 'toolCalls', path),
      structuredOutput: requireBoolean(value, 'structuredOutput', path),
      attachments: requireBoolean(value, 'attachments', path),
      openWeights: requireBoolean(value, 'openWeights', path),
      contextLimit: requireInteger(value, 'contextLimit', path, minimum: 0),
      outputLimit: requireInteger(value, 'outputLimit', path, minimum: 0),
      inputModalities: List.unmodifiable(inputs),
      outputModalities: List.unmodifiable(outputs),
      knowledgeCutoff: knowledgeCutoff,
      releaseDate: releaseDate,
    );
  }

  final String id;
  final String canonicalId;
  final String displayName;
  final String description;
  final String family;
  final bool reasoning;
  final bool toolCalls;
  final bool structuredOutput;
  final bool attachments;
  final bool openWeights;
  final int contextLimit;
  final int outputLimit;
  final List<String> inputModalities;
  final List<String> outputModalities;
  final String knowledgeCutoff;
  final String releaseDate;
}

/// Request-side catalog supplied by models.dev for one client protocol.
final class ClientModelCatalog {
  const ClientModelCatalog({
    required this.protocol,
    required this.providerId,
    required this.metadataSource,
    required this.models,
  });

  factory ClientModelCatalog.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'protocol', 'providerId', 'metadataSource', 'models'},
    );
    final protocol = requireString(value, 'protocol', path);
    final providerId = requireString(value, 'providerId', path);
    final metadataSource = requireString(value, 'metadataSource', path);
    final expectedProvider = protocol == 'anthropic_messages'
        ? 'anthropic'
        : const {'openai_responses', 'openai_chat'}.contains(protocol)
        ? 'openai'
        : '';
    final rawModels = requireList(value['models'], '$path.models');
    if (expectedProvider.isEmpty ||
        providerId != expectedProvider ||
        metadataSource != 'models.dev' ||
        rawModels.length > 16384) {
      throw ControlContractException('$path client model catalog is invalid');
    }
    final models = rawModels.indexed
        .map(
          (entry) =>
              ClientModel.fromJson(entry.$2, '$path.models[${entry.$1}]'),
        )
        .toList(growable: false);
    if (models.map((model) => model.id).toSet().length != models.length ||
        models.any((model) => model.canonicalId != '$providerId/${model.id}')) {
      throw ControlContractException(
        '$path client model identities are invalid',
      );
    }
    return ClientModelCatalog(
      protocol: protocol,
      providerId: providerId,
      metadataSource: metadataSource,
      models: List.unmodifiable(models),
    );
  }

  final String protocol;
  final String providerId;
  final String metadataSource;
  final List<ClientModel> models;
}

bool _validCatalogText(String value, {required int maximumBytes}) =>
    value.isNotEmpty &&
    value.trim() == value &&
    utf8.encode(value).length <= maximumBytes &&
    !value.contains('\uFEFF') &&
    !_containsControlCharacter(value);

bool _validOpaqueModelId(String value) =>
    value.isNotEmpty &&
    utf8.encode(value).length <= 256 &&
    !value.contains('\uFEFF') &&
    !_containsControlCharacter(value);

bool _validOptionalCatalogText(String value, {required int maximumBytes}) =>
    value.isEmpty || _validCatalogText(value, maximumBytes: maximumBytes);

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
    required this.setHeaderNames,
    required this.deleteHeaderNames,
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
        'setHeaderNames',
        'deleteHeaderNames',
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
    final setHeaderNames = _requireProviderHeaderNames(
      value['setHeaderNames'],
      '$path.setHeaderNames',
    );
    final deleteHeaderNames = _requireProviderHeaderNames(
      value['deleteHeaderNames'],
      '$path.deleteHeaderNames',
    );
    final allHeaderNames = [
      ...setHeaderNames,
      ...deleteHeaderNames,
    ].map((name) => name.toLowerCase()).toList(growable: false);
    if (allHeaderNames.length > 64 ||
        allHeaderNames.toSet().length != allHeaderNames.length) {
      throw ControlContractException('$path Header policy is inconsistent');
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
      setHeaderNames: List.unmodifiable(setHeaderNames),
      deleteHeaderNames: List.unmodifiable(deleteHeaderNames),
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
  final List<String> setHeaderNames;
  final List<String> deleteHeaderNames;

  bool get usable => state == 'active' && credentialState == 'ready';
}

final _providerHeaderNamePattern = RegExp(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$");

const _transportOwnedProviderHeaders = <String>{
  'connection',
  'content-length',
  'host',
  'keep-alive',
  'proxy-connection',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
};

List<String> _requireProviderHeaderNames(Object? json, String path) {
  final items = requireList(json, path);
  if (items.length > 64) {
    throw ControlContractException('$path has too many Header names');
  }
  final result = <String>[];
  var previous = '';
  for (final (index, item) in items.indexed) {
    if (item is! String ||
        item.isEmpty ||
        item.trim() != item ||
        utf8.encode(item).length > 256 ||
        !_providerHeaderNamePattern.hasMatch(item) ||
        _transportOwnedProviderHeaders.contains(item.toLowerCase()) ||
        item.compareTo(previous) <= 0) {
      throw ControlContractException('$path[$index] Header name is invalid');
    }
    result.add(item);
    previous = item;
  }
  return result;
}

/// Ephemeral secret input. Values cross the control boundary once and are
/// intentionally absent from [ProviderAccount] read projections.
final class ProviderAccountHeaderPolicy {
  const ProviderAccountHeaderPolicy({
    this.setHeaders = const {},
    this.deleteHeaders = const [],
  });

  final Map<String, String> setHeaders;
  final List<String> deleteHeaders;

  void validate({required String accountKind}) {
    if (setHeaders.length + deleteHeaders.length > 64) {
      throw const ControlContractException(
        'Provider Account Header policy has too many rules',
      );
    }
    final seen = <String>{};
    for (final entry in setHeaders.entries) {
      _validateInputName(entry.key, seen);
      if (!_validBoundedHttpHeaderValue(entry.value)) {
        throw const ControlContractException(
          'Provider Account Header value is invalid',
        );
      }
    }
    for (final name in deleteHeaders) {
      _validateInputName(name, seen);
    }
    final primary = switch (accountKind) {
      'anthropic_api_key' => 'x-api-key',
      'bearer_token' => 'authorization',
      _ => throw const ControlContractException(
        'Provider Account kind is invalid',
      ),
    };
    if (seen.contains(primary)) {
      throw const ControlContractException(
        'Provider Account authentication Header is driver-owned',
      );
    }
  }

  static void _validateInputName(String name, Set<String> seen) {
    final normalized = name.toLowerCase();
    if (name.isEmpty ||
        name.trim() != name ||
        utf8.encode(name).length > 256 ||
        !_providerHeaderNamePattern.hasMatch(name) ||
        _transportOwnedProviderHeaders.contains(normalized) ||
        !seen.add(normalized)) {
      throw const ControlContractException(
        'Provider Account Header name is invalid',
      );
    }
  }

  JsonObject toJson() => {
    'setHeaders': Map<String, String>.fromEntries(
      setHeaders.entries.toList(growable: false)
        ..sort((left, right) => left.key.compareTo(right.key)),
    ),
    'deleteHeaders': [...deleteHeaders]..sort(),
  };
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

final class TrafficProxyPolicy {
  const TrafficProxyPolicy({required this.kind, this.endpoint});

  const TrafficProxyPolicy.direct() : kind = 'direct', endpoint = null;

  factory TrafficProxyPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind'},
      optional: const {'endpoint'},
    );
    final kind = requireString(value, 'kind', path);
    final endpoint = optionalString(value, 'endpoint', path);
    if (!const {'direct', 'socks5'}.contains(kind) ||
        (kind == 'direct' && endpoint != null) ||
        (kind != 'direct' && !_validEgressEndpoint(endpoint))) {
      throw ControlContractException('$path proxy policy is invalid');
    }
    return TrafficProxyPolicy(kind: kind, endpoint: endpoint);
  }

  final String kind;
  final String? endpoint;

  bool get isDirect => kind == 'direct';
  bool get isSocks5 => kind == 'socks5';

  JsonObject toJson() => {'kind': kind, 'endpoint': ?endpoint};

  @override
  bool operator ==(Object other) =>
      other is TrafficProxyPolicy &&
      other.kind == kind &&
      other.endpoint == endpoint;

  @override
  int get hashCode => Object.hash(kind, endpoint);
}

final class TrafficResolverPolicy {
  const TrafficResolverPolicy({
    required this.kind,
    required this.transport,
    this.dohUrl,
  });

  const TrafficResolverPolicy.system()
    : kind = 'system',
      transport = 'direct',
      dohUrl = null;

  factory TrafficResolverPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'transport'},
      optional: const {'dohUrl'},
    );
    final kind = requireString(value, 'kind', path);
    final transport = requireString(value, 'transport', path);
    final dohUrl = optionalString(value, 'dohUrl', path);
    final valid = switch (kind) {
      'system' => transport == 'direct' && dohUrl == null,
      'doh' =>
        const {'direct', 'proxy'}.contains(transport) &&
            _validDoHEndpoint(dohUrl),
      _ => false,
    };
    if (!valid) {
      throw ControlContractException('$path resolver policy is invalid');
    }
    return TrafficResolverPolicy(
      kind: kind,
      transport: transport,
      dohUrl: dohUrl,
    );
  }

  final String kind;
  final String transport;
  final String? dohUrl;

  bool get isSystem => kind == 'system';
  bool get isDoH => kind == 'doh';

  JsonObject toJson() => {
    'kind': kind,
    'dohUrl': ?dohUrl,
    'transport': transport,
  };

  @override
  bool operator ==(Object other) =>
      other is TrafficResolverPolicy &&
      other.kind == kind &&
      other.transport == transport &&
      other.dohUrl == dohUrl;

  @override
  int get hashCode => Object.hash(kind, transport, dohUrl);
}

final class TrafficEgressPolicy {
  const TrafficEgressPolicy({required this.proxy, required this.resolver});

  const TrafficEgressPolicy.direct()
    : proxy = const TrafficProxyPolicy.direct(),
      resolver = const TrafficResolverPolicy.system();

  factory TrafficEgressPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'proxy', 'resolver'});
    final proxy = TrafficProxyPolicy.fromJson(value['proxy'], '$path.proxy');
    final resolver = TrafficResolverPolicy.fromJson(
      value['resolver'],
      '$path.resolver',
    );
    final valid = switch (proxy.kind) {
      'direct' => resolver.transport != 'proxy',
      'socks5' => true,
      _ => false,
    };
    if (!valid) {
      throw ControlContractException('$path egress policy is inconsistent');
    }
    return TrafficEgressPolicy(proxy: proxy, resolver: resolver);
  }

  final TrafficProxyPolicy proxy;
  final TrafficResolverPolicy resolver;

  JsonObject toJson() => {
    'proxy': proxy.toJson(),
    'resolver': resolver.toJson(),
  };

  @override
  bool operator ==(Object other) =>
      other is TrafficEgressPolicy &&
      other.proxy == proxy &&
      other.resolver == resolver;

  @override
  int get hashCode => Object.hash(proxy, resolver);
}

final class EgressProfileRevision {
  const EgressProfileRevision({
    required this.id,
    required this.revision,
    required this.displayName,
    required this.policy,
    required this.publishedAt,
  });

  static final direct = EgressProfileRevision(
    id: 'profile.direct',
    revision: 1,
    displayName: 'Direct · System DNS',
    policy: const TrafficEgressPolicy.direct(),
    publishedAt: DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
  );

  factory EgressProfileRevision.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'displayName',
        'policy',
        'publishedAt',
      },
    );
    final displayName = requireString(value, 'displayName', path);
    if (!_validDisplayLabel(displayName)) {
      throw ControlContractException('$path display name is invalid');
    }
    return EgressProfileRevision(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      displayName: displayName,
      policy: TrafficEgressPolicy.fromJson(value['policy'], '$path.policy'),
      publishedAt: requireTimestamp(value, 'publishedAt', path),
    );
  }

  final String id;
  final int revision;
  final String displayName;
  final TrafficEgressPolicy policy;
  final DateTime publishedAt;

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'displayName': displayName,
    'policy': policy.toJson(),
    'publishedAt': publishedAt.toUtc().toIso8601String(),
  };

  @override
  bool operator ==(Object other) =>
      other is EgressProfileRevision &&
      other.id == id &&
      other.revision == revision &&
      other.displayName == displayName &&
      other.policy == policy &&
      other.publishedAt == publishedAt;

  @override
  int get hashCode =>
      Object.hash(id, revision, displayName, policy, publishedAt);
}

final class EgressProfileCatalog {
  const EgressProfileCatalog({required this.items});

  factory EgressProfileCatalog.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'items'});
    final items = requireList(value['items'], '$path.items').indexed
        .map(
          (entry) => EgressProfileRevision.fromJson(
            entry.$2,
            '$path.items[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    if (items.map((item) => item.id).toSet().length != items.length) {
      throw ControlContractException('$path contains duplicate profiles');
    }
    return EgressProfileCatalog(items: List.unmodifiable(items));
  }

  final List<EgressProfileRevision> items;
}

final class TrafficTransformPolicy {
  const TrafficTransformPolicy({
    required this.requestJavaScript,
    required this.responseJavaScript,
  });

  const TrafficTransformPolicy.disabled()
    : requestJavaScript = '',
      responseJavaScript = '';

  factory TrafficTransformPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'requestJavaScript', 'responseJavaScript'},
    );
    final requestJavaScript = requireStringValue(
      value,
      'requestJavaScript',
      path,
    );
    final responseJavaScript = requireStringValue(
      value,
      'responseJavaScript',
      path,
    );
    if (!_validTransformSource(requestJavaScript) ||
        !_validTransformSource(responseJavaScript)) {
      throw ControlContractException('$path JavaScript source is invalid');
    }
    return TrafficTransformPolicy(
      requestJavaScript: requestJavaScript,
      responseJavaScript: responseJavaScript,
    );
  }

  final String requestJavaScript;
  final String responseJavaScript;

  bool get enabled =>
      requestJavaScript.isNotEmpty || responseJavaScript.isNotEmpty;

  JsonObject toJson() => {
    'requestJavaScript': requestJavaScript,
    'responseJavaScript': responseJavaScript,
  };

  @override
  bool operator ==(Object other) =>
      other is TrafficTransformPolicy &&
      other.requestJavaScript == requestJavaScript &&
      other.responseJavaScript == responseJavaScript;

  @override
  int get hashCode => Object.hash(requestJavaScript, responseJavaScript);
}

final class CodeLibraryCollection {
  const CodeLibraryCollection({required this.id, required this.displayName});

  factory CodeLibraryCollection.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'id', 'displayName'});
    final displayName = requireString(value, 'displayName', path);
    if (!_validDisplayLabel(displayName)) {
      throw ControlContractException('$path display name is invalid');
    }
    return CodeLibraryCollection(
      id: _requireResourceId(value, 'id', path),
      displayName: displayName,
    );
  }

  final String id;
  final String displayName;

  JsonObject toJson() => {'id': id, 'displayName': displayName};
}

final class CodeLibraryTransformRevision {
  const CodeLibraryTransformRevision({
    required this.id,
    required this.revision,
    required this.collectionId,
    required this.displayName,
    required this.policy,
    required this.publishedAt,
  });

  factory CodeLibraryTransformRevision.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'id',
        'revision',
        'collectionId',
        'displayName',
        'policy',
        'publishedAt',
      },
    );
    final displayName = requireString(value, 'displayName', path);
    if (!_validDisplayLabel(displayName)) {
      throw ControlContractException('$path display name is invalid');
    }
    return CodeLibraryTransformRevision(
      id: _requireResourceId(value, 'id', path),
      revision: requireInteger(value, 'revision', path, minimum: 1),
      collectionId: _requireResourceId(value, 'collectionId', path),
      displayName: displayName,
      policy: TrafficTransformPolicy.fromJson(value['policy'], '$path.policy'),
      publishedAt: requireTimestamp(value, 'publishedAt', path),
    );
  }

  final String id;
  final int revision;
  final String collectionId;
  final String displayName;
  final TrafficTransformPolicy policy;
  final DateTime publishedAt;

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'collectionId': collectionId,
    'displayName': displayName,
    'policy': policy.toJson(),
    'publishedAt': publishedAt.toUtc().toIso8601String(),
  };

  @override
  bool operator ==(Object other) =>
      other is CodeLibraryTransformRevision &&
      other.id == id &&
      other.revision == revision &&
      other.collectionId == collectionId &&
      other.displayName == displayName &&
      other.policy == policy &&
      other.publishedAt == publishedAt;

  @override
  int get hashCode =>
      Object.hash(id, revision, collectionId, displayName, policy, publishedAt);
}

final class CodeLibraryCatalog {
  const CodeLibraryCatalog({
    required this.collections,
    required this.transforms,
  });

  factory CodeLibraryCatalog.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'collections', 'transforms'});
    final collections = requireList(value['collections'], '$path.collections')
        .indexed
        .map(
          (entry) => CodeLibraryCollection.fromJson(
            entry.$2,
            '$path.collections[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final transforms = requireList(value['transforms'], '$path.transforms')
        .indexed
        .map(
          (entry) => CodeLibraryTransformRevision.fromJson(
            entry.$2,
            '$path.transforms[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final collectionIds = collections.map((item) => item.id).toSet();
    if (collectionIds.length != collections.length ||
        transforms.map((item) => item.id).toSet().length != transforms.length ||
        transforms.any((item) => !collectionIds.contains(item.collectionId))) {
      throw ControlContractException('$path catalog is inconsistent');
    }
    return CodeLibraryCatalog(
      collections: List.unmodifiable(collections),
      transforms: List.unmodifiable(transforms),
    );
  }

  final List<CodeLibraryCollection> collections;
  final List<CodeLibraryTransformRevision> transforms;
}

final class MessageTransformTestRequest {
  const MessageTransformTestRequest({
    required this.method,
    required this.path,
    required this.headers,
    required this.body,
  });

  factory MessageTransformTestRequest.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'method', 'path', 'headers', 'body'},
    );
    final method = requireString(value, 'method', path);
    final requestPath = requireString(value, 'path', path);
    if (method != 'POST' || !requestPath.startsWith('/v1/')) {
      throw ControlContractException('$path request authority is invalid');
    }
    return MessageTransformTestRequest(
      method: method,
      path: requestPath,
      headers: _requireTransformHeaders(value['headers'], '$path.headers'),
      body: _requireTransformBody(value, 'body', path),
    );
  }

  final String method;
  final String path;
  final Map<String, List<String>> headers;
  final String body;

  JsonObject toJson() => {
    'method': method,
    'path': path,
    'headers': headers,
    'body': body,
  };
}

final class MessageTransformTestResponse {
  const MessageTransformTestResponse({
    required this.statusCode,
    required this.streaming,
    required this.headers,
    required this.body,
  });

  factory MessageTransformTestResponse.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'statusCode', 'streaming', 'headers', 'body'},
    );
    return MessageTransformTestResponse(
      statusCode: requireInteger(value, 'statusCode', path, minimum: 100),
      streaming: requireBoolean(value, 'streaming', path),
      headers: _requireTransformHeaders(value['headers'], '$path.headers'),
      body: _requireTransformBody(value, 'body', path),
    );
  }

  final int statusCode;
  final bool streaming;
  final Map<String, List<String>> headers;
  final String body;

  JsonObject toJson() => {
    'statusCode': statusCode,
    'streaming': streaming,
    'headers': headers,
    'body': body,
  };
}

final class MessageTransformTestSample {
  const MessageTransformTestSample({
    required this.request,
    required this.response,
  });

  final MessageTransformTestRequest request;
  final MessageTransformTestResponse response;

  JsonObject toJson() => {
    'request': request.toJson(),
    'response': response.toJson(),
  };
}

final class CapturedMessageTransformSample {
  const CapturedMessageTransformSample({
    required this.exchangeId,
    required this.wireProtocol,
    required this.sample,
  });

  factory CapturedMessageTransformSample.fromRawEvidence({
    required RevealedRawEvidence request,
    required RevealedRawEvidence response,
  }) {
    final requestEnvelope = request.envelope;
    final responseEnvelope = response.envelope;
    final wireProtocol = switch (requestEnvelope.path) {
      '/v1/messages' => 'anthropic_messages',
      '/v1/responses' => 'openai_responses',
      '/v1/chat/completions' => 'openai_chat',
      _ => null,
    };
    final responseStreaming =
        responseEnvelope.representation == 'message_transform_stream_input';
    if (requestEnvelope.layer != 'transform_request_input' ||
        responseEnvelope.layer != 'transform_response_input' ||
        requestEnvelope.payloadState != 'captured' ||
        responseEnvelope.payloadState != 'captured' ||
        requestEnvelope.exchangeId != responseEnvelope.exchangeId ||
        requestEnvelope.attemptId == null ||
        requestEnvelope.attemptId != responseEnvelope.attemptId ||
        requestEnvelope.scopeKind != responseEnvelope.scopeKind ||
        requestEnvelope.scopeId != responseEnvelope.scopeId ||
        requestEnvelope.method != 'POST' ||
        wireProtocol == null ||
        responseEnvelope.statusCode == null ||
        requestEnvelope.representation != 'message_transform_input' ||
        !const {
          'message_transform_input',
          'message_transform_stream_input',
        }.contains(responseEnvelope.representation) ||
        request.trailers.isNotEmpty ||
        response.trailers.isNotEmpty ||
        request.headers.any((field) => field.redacted.isNotEmpty) ||
        response.headers.any((field) => field.redacted.isNotEmpty)) {
      throw const ControlContractException(
        'Captured Transform sample evidence is incomplete',
      );
    }
    late final String requestBody;
    late final String responseBody;
    try {
      requestBody = utf8.decode(request.body, allowMalformed: false);
      responseBody = utf8.decode(response.body, allowMalformed: false);
    } on FormatException {
      throw const ControlContractException(
        'Captured Transform sample Body is not UTF-8',
      );
    }
    final sample = MessageTransformTestSample(
      request: MessageTransformTestRequest.fromJson({
        'method': requestEnvelope.method,
        'path': requestEnvelope.path,
        'headers': _transformSampleHeaders(request.headers),
        'body': requestBody,
      }, r'$.sample.request'),
      response: MessageTransformTestResponse.fromJson({
        'statusCode': responseEnvelope.statusCode,
        'streaming': responseStreaming,
        'headers': _transformSampleHeaders(response.headers),
        'body': responseBody,
      }, r'$.sample.response'),
    );
    return CapturedMessageTransformSample(
      exchangeId: requestEnvelope.exchangeId,
      wireProtocol: wireProtocol,
      sample: sample,
    );
  }

  final String exchangeId;
  final String wireProtocol;
  final MessageTransformTestSample sample;
}

JsonObject _transformSampleHeaders(List<RawHeaderField> fields) => {
  for (final field in fields) field.name: field.values,
};

final class MessageTransformTestResult {
  const MessageTransformTestResult({
    required this.wireProtocol,
    required this.requestBefore,
    required this.requestAfter,
    required this.responseBefore,
    required this.responseAfter,
  });

  factory MessageTransformTestResult.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'wireProtocol',
        'requestBefore',
        'requestAfter',
        'responseBefore',
        'responseAfter',
      },
    );
    final wireProtocol = requireString(value, 'wireProtocol', path);
    final requestBefore = MessageTransformTestRequest.fromJson(
      value['requestBefore'],
      '$path.requestBefore',
    );
    final requestAfter = MessageTransformTestRequest.fromJson(
      value['requestAfter'],
      '$path.requestAfter',
    );
    final responseBefore = MessageTransformTestResponse.fromJson(
      value['responseBefore'],
      '$path.responseBefore',
    );
    final responseAfter = MessageTransformTestResponse.fromJson(
      value['responseAfter'],
      '$path.responseAfter',
    );
    final expectedPath = switch (wireProtocol) {
      'anthropic_messages' => '/v1/messages',
      'openai_responses' => '/v1/responses',
      'openai_chat' => '/v1/chat/completions',
      _ => null,
    };
    if (expectedPath == null ||
        requestBefore.path != expectedPath ||
        requestAfter.path != expectedPath ||
        requestBefore.method != requestAfter.method ||
        responseBefore.statusCode != responseAfter.statusCode ||
        responseBefore.streaming != responseAfter.streaming) {
      throw ControlContractException('$path wire protocol is inconsistent');
    }
    return MessageTransformTestResult(
      wireProtocol: wireProtocol,
      requestBefore: requestBefore,
      requestAfter: requestAfter,
      responseBefore: responseBefore,
      responseAfter: responseAfter,
    );
  }

  final String wireProtocol;
  final MessageTransformTestRequest requestBefore;
  final MessageTransformTestRequest requestAfter;
  final MessageTransformTestResponse responseBefore;
  final MessageTransformTestResponse responseAfter;
}

final _httpHeaderNamePattern = RegExp(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$");

Map<String, List<String>> _requireTransformHeaders(Object? json, String path) {
  final value = requireObject(json, path);
  if (value.length > 128) {
    throw ControlContractException('$path has too many Header fields');
  }
  final result = <String, List<String>>{};
  var encodedBytes = 0;
  for (final entry in value.entries) {
    final lower = entry.key.toLowerCase();
    final values = requireList(entry.value, '$path.${entry.key}');
    if (!_httpHeaderNamePattern.hasMatch(entry.key) ||
        result.containsKey(lower) ||
        values.isEmpty ||
        values.length > 128) {
      throw ControlContractException('$path Header field is invalid');
    }
    final strings = <String>[];
    encodedBytes += utf8.encode(entry.key).length;
    for (final item in values) {
      if (item is! String || !_validBoundedHttpHeaderValue(item)) {
        throw ControlContractException('$path Header value is invalid');
      }
      encodedBytes += utf8.encode(item).length;
      strings.add(item);
    }
    result[lower] = List.unmodifiable(strings);
  }
  if (encodedBytes > 64 << 10) {
    throw ControlContractException('$path Headers exceed their byte limit');
  }
  return Map.unmodifiable(result);
}

String _requireTransformBody(JsonObject value, String key, String path) {
  final body = requireStringValue(value, key, path);
  if (utf8.encode(body).length > 16 << 20 || body.runes.contains(0xfffd)) {
    throw ControlContractException('$path.$key exceeds its limit');
  }
  return body;
}

bool _validBoundedHttpHeaderValue(String value) {
  try {
    if (utf8.encode(value).length > 16 << 10) return false;
  } on FormatException {
    return false;
  }
  for (final character in value.runes) {
    if (character == 0 ||
        character == 0x7f ||
        character == 0x0a ||
        character == 0x0d ||
        (character < 0x20 && character != 0x09)) {
      return false;
    }
  }
  return true;
}

bool _validTransformSource(String value) {
  late final int encodedBytes;
  try {
    encodedBytes = utf8.encode(value).length;
  } on FormatException {
    return false;
  }
  if (encodedBytes > 64 << 10) return false;
  for (final character in value.runes) {
    if (character == 0xfffd ||
        (character < 0x20 &&
            character != 0x09 &&
            character != 0x0a &&
            character != 0x0d) ||
        (character >= 0x7f && character <= 0x9f)) {
      return false;
    }
  }
  return true;
}

bool _validEgressEndpoint(String? value) {
  if (value == null ||
      value.isEmpty ||
      utf8.encode(value).length > 2048 ||
      _containsControlCharacter(value)) {
    return false;
  }
  final separator = value.startsWith('[')
      ? value.indexOf(']:') + 1
      : value.lastIndexOf(':');
  if (separator <= 0 || separator >= value.length - 1) return false;
  final host = value.substring(0, separator);
  final rawPort = value.substring(separator + 1);
  final port = int.tryParse(rawPort);
  return host.isNotEmpty &&
      port != null &&
      port > 0 &&
      port <= 65535 &&
      port.toString() == rawPort;
}

bool _validDoHEndpoint(String? value) {
  if (value == null ||
      value.isEmpty ||
      utf8.encode(value).length > 2048 ||
      _containsControlCharacter(value)) {
    return false;
  }
  final uri = Uri.tryParse(value);
  return uri != null &&
      uri.scheme == 'https' &&
      uri.host.isNotEmpty &&
      uri.userInfo.isEmpty &&
      uri.path.startsWith('/') &&
      uri.path.isNotEmpty &&
      !uri.hasQuery &&
      !uri.hasFragment;
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
    final originText = requireString(value, 'origin', path);
    final origin = Uri.tryParse(originText);
    final capabilities = requireStringList(value, 'capabilities', path);
    if (origin == null ||
        !isCanonicalProviderOrigin(originText) ||
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

final class EnvironmentModelMapping {
  const EnvironmentModelMapping({
    required this.requestedModel,
    required this.upstreamModel,
  });

  factory EnvironmentModelMapping.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'requestedModel', 'upstreamModel'},
    );
    final requestedModel = requireString(value, 'requestedModel', path);
    final upstreamModel = requireString(value, 'upstreamModel', path);
    if (!_validOpaqueModelId(requestedModel) ||
        !_validOpaqueModelId(upstreamModel)) {
      throw ControlContractException('$path model mapping is invalid');
    }
    return EnvironmentModelMapping(
      requestedModel: requestedModel,
      upstreamModel: upstreamModel,
    );
  }

  final String requestedModel;
  final String upstreamModel;

  JsonObject toJson() => {
    'requestedModel': requestedModel,
    'upstreamModel': upstreamModel,
  };
}

final class EnvironmentModelPolicy {
  const EnvironmentModelPolicy({
    required this.revision,
    required this.mode,
    required this.mappings,
  });

  factory EnvironmentModelPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'revision', 'mode', 'mappings'},
    );
    final mode = requireString(value, 'mode', path);
    final rawMappings = requireList(value['mappings'], '$path.mappings');
    final mappings = rawMappings.indexed
        .map(
          (entry) => EnvironmentModelMapping.fromJson(
            entry.$2,
            '$path.mappings[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final requestedModels = mappings
        .map((item) => item.requestedModel)
        .toList();
    if (!const {'passthrough', 'map'}.contains(mode) ||
        (mode == 'map') != mappings.isNotEmpty ||
        requestedModels.toSet().length != requestedModels.length) {
      throw ControlContractException('$path model policy is invalid');
    }
    return EnvironmentModelPolicy(
      revision: requireInteger(value, 'revision', path, minimum: 1),
      mode: mode,
      mappings: List.unmodifiable(mappings),
    );
  }

  final int revision;
  final String mode;
  final List<EnvironmentModelMapping> mappings;

  JsonObject toJson() => {
    'revision': revision,
    'mode': mode,
    'mappings': mappings
        .map((mapping) => mapping.toJson())
        .toList(growable: false),
  };
}

final class RouteAccountPolicy {
  const RouteAccountPolicy({
    required this.revision,
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
        'preferredAccountId',
        'candidateAccountIds',
        'accountRevisions',
        'failoverPolicy',
      },
    );
    final preferred = requireStringValue(value, 'preferredAccountId', path);
    final candidates = requireStringList(value, 'candidateAccountIds', path);
    final revisions = _requireRevisionMap(
      value['accountRevisions'],
      '$path.accountRevisions',
    );
    final failover = requireString(value, 'failoverPolicy', path);
    final candidateSet = candidates.toSet();
    if (!const {'off', 'account_scoped_safe'}.contains(failover) ||
        candidateSet.length != candidates.length ||
        candidates.any((id) => !_resourceIdPattern.hasMatch(id)) ||
        !_resourceIdPattern.hasMatch(preferred) ||
        candidates.isEmpty ||
        !candidateSet.contains(preferred) ||
        revisions.keys.toSet().difference(candidateSet).isNotEmpty ||
        candidateSet.difference(revisions.keys.toSet()).isNotEmpty) {
      throw ControlContractException('$path Account authority is invalid');
    }
    return RouteAccountPolicy(
      revision: requireInteger(value, 'revision', path, minimum: 1),
      preferredAccountId: preferred,
      candidateAccountIds: List.unmodifiable(candidates),
      accountRevisions: revisions,
      failoverPolicy: failover,
    );
  }

  final int revision;
  final String preferredAccountId;
  final List<String> candidateAccountIds;
  final Map<String, int> accountRevisions;
  final String failoverPolicy;

  RouteAccountPolicy copyWith({
    int? revision,
    String? preferredAccountId,
    List<String>? candidateAccountIds,
    Map<String, int>? accountRevisions,
    String? failoverPolicy,
  }) => RouteAccountPolicy(
    revision: revision ?? this.revision,
    preferredAccountId: preferredAccountId ?? this.preferredAccountId,
    candidateAccountIds: candidateAccountIds ?? this.candidateAccountIds,
    accountRevisions: accountRevisions ?? this.accountRevisions,
    failoverPolicy: failoverPolicy ?? this.failoverPolicy,
  );

  JsonObject toJson() => {
    'revision': revision,
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

final class EnvironmentUpstreamPlan {
  const EnvironmentUpstreamPlan({
    required this.defaultRouteId,
    required this.routeSet,
    required this.routes,
  });

  factory EnvironmentUpstreamPlan.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'routes', 'defaultRouteId', 'routeSet'},
    );
    final routes = requireList(value['routes'], '$path.routes').indexed
        .map(
          (entry) =>
              EnvironmentRoute.fromJson(entry.$2, '$path.routes[${entry.$1}]'),
        )
        .toList(growable: false);
    final routeIds = routes.map((route) => route.id).toSet();
    final defaultRouteId = _requireResourceId(value, 'defaultRouteId', path);
    final routeSet = EnvironmentRouteSet.fromJson(
      value['routeSet'],
      '$path.routeSet',
    );
    if (routes.isEmpty ||
        routeIds.length != routes.length ||
        !routeIds.contains(defaultRouteId) ||
        routeIds.difference(routeSet.candidateRouteIds.toSet()).isNotEmpty ||
        routeSet.candidateRouteIds.toSet().difference(routeIds).isNotEmpty) {
      throw ControlContractException('$path upstream plan is invalid');
    }
    return EnvironmentUpstreamPlan(
      defaultRouteId: defaultRouteId,
      routeSet: routeSet,
      routes: List.unmodifiable(routes),
    );
  }

  final String defaultRouteId;
  final EnvironmentRouteSet routeSet;
  final List<EnvironmentRoute> routes;

  JsonObject toJson() => {
    'routes': routes.map((route) => route.toJson()).toList(growable: false),
    'defaultRouteId': defaultRouteId,
    'routeSet': routeSet.toJson(),
  };
}

final class EnvironmentDestination {
  const EnvironmentDestination.original() : kind = 'original', upstream = null;

  const EnvironmentDestination.upstream(EnvironmentUpstreamPlan value)
    : kind = 'upstream',
      upstream = value;

  factory EnvironmentDestination.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    final kind = requireString(value, 'kind', path);
    switch (kind) {
      case 'original':
        requireFields(value, path, required: const {'kind'});
        return const EnvironmentDestination.original();
      case 'upstream':
        requireFields(value, path, required: const {'kind', 'upstream'});
        return EnvironmentDestination.upstream(
          EnvironmentUpstreamPlan.fromJson(value['upstream'], '$path.upstream'),
        );
      default:
        throw ControlContractException('$path destination kind is invalid');
    }
  }

  final String kind;
  final EnvironmentUpstreamPlan? upstream;

  bool get isOriginal => kind == 'original';
  bool get isUpstream => kind == 'upstream';

  JsonObject toJson() => {
    'kind': kind,
    if (upstream case final value?) 'upstream': value.toJson(),
  };
}

final class EnvironmentProtocolPlan {
  const EnvironmentProtocolPlan({
    required this.id,
    required this.revision,
    required this.clientProtocol,
    required this.clientAdapterPolicy,
    required this.destination,
    required this.egressProfile,
    required this.transforms,
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
        'destination',
        'egressProfile',
        'transforms',
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
    final transforms = requireList(value['transforms'], '$path.transforms')
        .indexed
        .map(
          (entry) => CodeLibraryTransformRevision.fromJson(
            entry.$2,
            '$path.transforms[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final clientProtocol = _requireResourceId(value, 'clientProtocol', path);
    if (bindings.map((item) => item.id).toSet().length != bindings.length ||
        !const {
          'anthropic_messages',
          'openai_responses',
          'openai_chat',
        }.contains(clientProtocol)) {
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
      destination: EnvironmentDestination.fromJson(
        value['destination'],
        '$path.destination',
      ),
      egressProfile: EgressProfileRevision.fromJson(
        value['egressProfile'],
        '$path.egressProfile',
      ),
      transforms: List.unmodifiable(transforms),
      pluginBindings: List.unmodifiable(bindings),
    );
  }

  final String id;
  final int revision;
  final String clientProtocol;
  final EnvironmentClientAdapterPolicy clientAdapterPolicy;
  final EnvironmentDestination destination;
  final EgressProfileRevision egressProfile;
  final List<CodeLibraryTransformRevision> transforms;
  final List<EnvironmentPluginBinding> pluginBindings;

  List<EnvironmentRoute> get routes =>
      destination.upstream?.routes ?? const <EnvironmentRoute>[];

  EnvironmentProtocolPlan copyWith({
    EnvironmentDestination? destination,
    EgressProfileRevision? egressProfile,
    List<CodeLibraryTransformRevision>? transforms,
  }) => EnvironmentProtocolPlan(
    id: id,
    revision: revision,
    clientProtocol: clientProtocol,
    clientAdapterPolicy: clientAdapterPolicy,
    destination: destination ?? this.destination,
    egressProfile: egressProfile ?? this.egressProfile,
    transforms: transforms ?? this.transforms,
    pluginBindings: pluginBindings,
  );

  JsonObject toJson() => {
    'id': id,
    'revision': revision,
    'clientProtocol': clientProtocol,
    'clientAdapterPolicy': clientAdapterPolicy.toJson(),
    'destination': destination.toJson(),
    'egressProfile': egressProfile.toJson(),
    'transforms': transforms
        .map((transform) => transform.toJson())
        .toList(growable: false),
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

/// Exact child-process environment overlay frozen with one Environment
/// revision. Runtime routing, trust, proxy, and credential variables remain
/// launcher-owned and cannot be overridden here.
final class EnvironmentLaunchPolicy {
  const EnvironmentLaunchPolicy({
    required this.setEnv,
    required this.deleteEnv,
  });

  const EnvironmentLaunchPolicy.empty()
    : setEnv = const {},
      deleteEnv = const [];

  factory EnvironmentLaunchPolicy.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {},
      optional: const {'setEnv', 'deleteEnv'},
    );
    final rawSet = value['setEnv'] == null
        ? const <String, Object?>{}
        : requireObject(value['setEnv'], '$path.setEnv');
    final setEntries = rawSet.entries.toList(growable: false)
      ..sort((left, right) => left.key.compareTo(right.key));
    final setEnv = <String, String>{};
    var totalBytes = 0;
    for (final entry in setEntries) {
      if (entry.value is! String ||
          !_validLaunchEnvironmentName(entry.key) ||
          _reservedLaunchEnvironmentName(entry.key) ||
          utf8.encode(entry.value! as String).length > 16 << 10 ||
          (entry.value! as String).contains('\u0000')) {
        throw ControlContractException('$path.setEnv is invalid');
      }
      final stringValue = entry.value! as String;
      setEnv[entry.key] = stringValue;
      totalBytes +=
          utf8.encode(entry.key).length + utf8.encode(stringValue).length;
    }
    final rawDelete = value['deleteEnv'] == null
        ? const <Object?>[]
        : requireList(value['deleteEnv'], '$path.deleteEnv');
    final deleteEnv = <String>[];
    for (final (index, item) in rawDelete.indexed) {
      if (item is! String ||
          !_validLaunchEnvironmentName(item) ||
          _reservedLaunchEnvironmentName(item) ||
          setEnv.containsKey(item)) {
        throw ControlContractException('$path.deleteEnv[$index] is invalid');
      }
      deleteEnv.add(item);
      totalBytes += utf8.encode(item).length;
    }
    if (setEnv.length + deleteEnv.length > 128 ||
        deleteEnv.toSet().length != deleteEnv.length ||
        !_strictlySorted(deleteEnv) ||
        totalBytes > 64 << 10) {
      throw ControlContractException('$path launch environment is invalid');
    }
    return EnvironmentLaunchPolicy(
      setEnv: Map.unmodifiable(setEnv),
      deleteEnv: List.unmodifiable(deleteEnv),
    );
  }

  final Map<String, String> setEnv;
  final List<String> deleteEnv;

  JsonObject toJson() => {
    if (setEnv.isNotEmpty) 'setEnv': Map<String, String>.from(setEnv),
    if (deleteEnv.isNotEmpty) 'deleteEnv': [...deleteEnv],
  };

  @override
  bool operator ==(Object other) =>
      other is EnvironmentLaunchPolicy &&
      _stringMapsEqual(other.setEnv, setEnv) &&
      _stringListsEqual(other.deleteEnv, deleteEnv);

  @override
  int get hashCode => Object.hash(
    Object.hashAll(
      setEnv.entries.map((entry) => Object.hash(entry.key, entry.value)),
    ),
    Object.hashAll(deleteEnv),
  );
}

bool _validLaunchEnvironmentName(String name) =>
    name.length <= 128 && RegExp(r'^[A-Za-z_][A-Za-z0-9_]*$').hasMatch(name);

const _reservedLaunchEnvironmentNames = <String>{
  'ALL_PROXY',
  'ANTHROPIC_API_KEY',
  'ANTHROPIC_AUTH_TOKEN',
  'ANTHROPIC_BASE_URL',
  'ANTHROPIC_BEDROCK_BASE_URL',
  'ANTHROPIC_CUSTOM_HEADERS',
  'ANTHROPIC_FOUNDRY_BASE_URL',
  'ANTHROPIC_VERTEX_BASE_URL',
  'CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK',
  'CLAUDE_CODE_OAUTH_TOKEN',
  'CLAUDE_CODE_USE_BEDROCK',
  'CLAUDE_CODE_USE_FOUNDRY',
  'CLAUDE_CODE_USE_VERTEX',
  'CODEX_API_KEY',
  'CODEX_BASE_URL',
  'CURL_CA_BUNDLE',
  'HTTP_PROXY',
  'HTTPS_PROXY',
  'NODE_EXTRA_CA_CERTS',
  'NODE_USE_ENV_PROXY',
  'NO_PROXY',
  'OPENAI_API_KEY',
  'OPENAI_BASE_URL',
  'OPENAI_ORGANIZATION',
  'OPENAI_ORG_ID',
  'OPENAI_PROJECT',
  'OPENAI_PROJECT_ID',
  'REQUESTS_CA_BUNDLE',
  'SSL_CERT_FILE',
};

bool _reservedLaunchEnvironmentName(String name) =>
    name.toUpperCase().startsWith('VIBERMATE_') ||
    _reservedLaunchEnvironmentNames.contains(name.toUpperCase());

bool _strictlySorted(List<String> values) {
  for (var index = 1; index < values.length; index++) {
    if (values[index - 1].compareTo(values[index]) >= 0) return false;
  }
  return true;
}

bool _stringMapsEqual(Map<String, String> left, Map<String, String> right) {
  if (left.length != right.length) return false;
  for (final entry in left.entries) {
    if (right[entry.key] != entry.value) return false;
  }
  return true;
}

bool _stringListsEqual(List<String> left, List<String> right) {
  if (left.length != right.length) return false;
  for (var index = 0; index < left.length; index++) {
    if (left[index] != right[index]) return false;
  }
  return true;
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
    required this.contentRecording,
    required this.launchEnvironment,
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
        'contentRecording',
        'launchEnvironment',
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
      contentRecording: EnvironmentContentRecordingPolicy.fromJson(
        value['contentRecording'],
        '$path.contentRecording',
      ),
      launchEnvironment: EnvironmentLaunchPolicy.fromJson(
        value['launchEnvironment'],
        '$path.launchEnvironment',
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
  final EnvironmentContentRecordingPolicy contentRecording;
  final EnvironmentLaunchPolicy launchEnvironment;
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
    EnvironmentLaunchPolicy? launchEnvironment,
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
    contentRecording: contentRecording ?? this.contentRecording,
    launchEnvironment: launchEnvironment ?? this.launchEnvironment,
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
    'contentRecording': contentRecording.toJson(),
    'launchEnvironment': launchEnvironment.toJson(),
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
    required this.contentRecording,
    required this.launchEnvironment,
    required this.policySet,
  });

  factory EnvironmentDraftInput.fromEnvironment(
    EnvironmentRecord environment, {
    required int expectedDraftRevision,
    String? name,
    String? state,
    List<EnvironmentClientEndpoint>? clientEndpoints,
    EnvironmentContentRecordingPolicy? contentRecording,
    EnvironmentLaunchPolicy? launchEnvironment,
    EnvironmentPolicySet? policySet,
  }) => EnvironmentDraftInput(
    expectedDraftRevision: expectedDraftRevision,
    name: name ?? environment.name,
    state: state ?? environment.state,
    clientEndpoints: clientEndpoints ?? environment.clientEndpoints,
    pluginBindings: environment.pluginBindings,
    budgetPolicy: environment.budgetPolicy,
    contentRecording: contentRecording ?? environment.contentRecording,
    launchEnvironment: launchEnvironment ?? environment.launchEnvironment,
    policySet: policySet ?? environment.policySet,
  );

  final int expectedDraftRevision;
  final String name;
  final String state;
  final List<EnvironmentClientEndpoint> clientEndpoints;
  final List<EnvironmentPluginBinding> pluginBindings;
  final EnvironmentBudgetPolicy budgetPolicy;
  final EnvironmentContentRecordingPolicy contentRecording;
  final EnvironmentLaunchPolicy launchEnvironment;
  final EnvironmentPolicySet policySet;

  EnvironmentDraftInput withExpectedDraftRevision(int revision) =>
      EnvironmentDraftInput(
        expectedDraftRevision: revision,
        name: name,
        state: state,
        clientEndpoints: clientEndpoints,
        pluginBindings: pluginBindings,
        budgetPolicy: budgetPolicy,
        contentRecording: contentRecording,
        launchEnvironment: launchEnvironment,
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
        'contentRecording': contentRecording.toJson(),
        'launchEnvironment': launchEnvironment.toJson(),
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
    'contentRecording': contentRecording.toJson(),
    'launchEnvironment': launchEnvironment.toJson(),
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
  });

  factory EnvironmentImpactCapture.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'captureKind', 'captureId'});
    final kind = requireString(value, 'captureKind', path);
    if (!const {'managed_run', 'manual_capture'}.contains(kind)) {
      throw ControlContractException('$path impact Capture is invalid');
    }
    return EnvironmentImpactCapture(
      captureKind: kind,
      captureId: _requireResourceId(value, 'captureId', path),
    );
  }

  final String captureKind;
  final String captureId;
}

final class EnvironmentImpact {
  const EnvironmentImpact({
    required this.environmentId,
    required this.baseRevision,
    required this.draftRevision,
    required this.candidateDigest,
    required this.continuingCaptures,
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
        'continuingCaptures',
      },
    );
    final continuing =
        requireList(value['continuingCaptures'], '$path.continuingCaptures')
            .indexed
            .map(
              (entry) => EnvironmentImpactCapture.fromJson(
                entry.$2,
                '$path.continuingCaptures[${entry.$1}]',
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
    if (environmentId != expectedEnvironmentId ||
        draftRevision != expectedDraftRevision) {
      throw ControlContractException('$path impact evidence is inconsistent');
    }
    return EnvironmentImpact(
      environmentId: environmentId,
      baseRevision: requireInteger(value, 'baseRevision', path),
      draftRevision: draftRevision,
      candidateDigest: _requireDigest(value, 'candidateDigest', path),
      continuingCaptures: List.unmodifiable(continuing),
    );
  }

  final String environmentId;
  final int baseRevision;
  final int draftRevision;
  final String candidateDigest;
  final List<EnvironmentImpactCapture> continuingCaptures;
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
    this.runtimeUserId,
    this.loginSessionId,
    this.deviceName,
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
        'runtimeUserId',
        'loginSessionId',
        'deviceName',
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
    final runtimeUserId = optionalString(value, 'runtimeUserId', path);
    final loginSessionId = optionalString(value, 'loginSessionId', path);
    final deviceName = optionalString(value, 'deviceName', path);
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
    final runtimeAttribution = <Object?>[
      runtimeUserId,
      loginSessionId,
      deviceName,
    ];
    final hasRuntimeAttribution = runtimeAttribution.every(
      (item) => item != null,
    );
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
        (runtimeAttribution.any((item) => item != null) !=
            hasRuntimeAttribution) ||
        (deviceName != null &&
            !_validDisplayLabel(deviceName, maximumBytes: 128)) ||
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
      runtimeUserId: runtimeUserId,
      loginSessionId: loginSessionId,
      deviceName: deviceName,
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
  final String? runtimeUserId;
  final String? loginSessionId;
  final String? deviceName;
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

final class CapturePage {
  const CapturePage({required this.items, required this.nextCursor});

  factory CapturePage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'items'},
      optional: const {'nextCursor'},
    );
    final items = requireList(value['items'], '$path.items').indexed
        .map(
          (entry) =>
              CaptureRecord.fromJson(entry.$2, '$path.items[${entry.$1}]'),
        )
        .toList(growable: false);
    if (items.map((item) => item.key).toSet().length != items.length) {
      throw ControlContractException('$path contains duplicate Capture keys');
    }
    final nextCursor = optionalString(value, 'nextCursor', path);
    if (nextCursor != null &&
        (nextCursor.length > 512 || RegExp(r'\s').hasMatch(nextCursor))) {
      throw ControlContractException('$path.nextCursor is invalid');
    }
    return CapturePage(items: List.unmodifiable(items), nextCursor: nextCursor);
  }

  final List<CaptureRecord> items;
  final String? nextCursor;
}

final class CaptureAssignment {
  const CaptureAssignment({
    required this.captureKey,
    required this.captureId,
    required this.captureKind,
    required this.environmentId,
    required this.environmentRevision,
    required this.environmentDigest,
    required this.launchEnvironmentRevision,
    required this.launchEnvironmentDigest,
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
        'environmentRevision',
        'environmentDigest',
        'launchEnvironmentRevision',
        'launchEnvironmentDigest',
        'revision',
        'source',
        'updatedAt',
      },
    );
    final captureId = _requireResourceId(value, 'captureId', path);
    final captureKind = requireString(value, 'captureKind', path);
    final captureKey = requireString(value, 'captureKey', path);
    final source = requireString(value, 'source', path);
    final environmentRevision = requireInteger(
      value,
      'environmentRevision',
      path,
      minimum: 1,
    );
    final environmentDigest = _requireDigest(value, 'environmentDigest', path);
    final launchEnvironmentRevision = requireInteger(
      value,
      'launchEnvironmentRevision',
      path,
      minimum: 1,
    );
    final launchEnvironmentDigest = _requireDigest(
      value,
      'launchEnvironmentDigest',
      path,
    );
    final revision = requireInteger(value, 'revision', path, minimum: 1);
    if (!const {'managed_run', 'manual_capture'}.contains(captureKind) ||
        captureKey != '$captureKind:$captureId' ||
        !const {
          'launch',
          'manual_create',
          'system_transparent',
        }.contains(source) ||
        environmentRevision < launchEnvironmentRevision ||
        (environmentRevision == launchEnvironmentRevision &&
            environmentDigest != launchEnvironmentDigest)) {
      throw ControlContractException('$path assignment authority is invalid');
    }
    return CaptureAssignment(
      captureKey: captureKey,
      captureId: captureId,
      captureKind: captureKind,
      environmentId: _requireResourceId(value, 'environmentId', path),
      environmentRevision: environmentRevision,
      environmentDigest: environmentDigest,
      launchEnvironmentRevision: launchEnvironmentRevision,
      launchEnvironmentDigest: launchEnvironmentDigest,
      revision: revision,
      source: source,
      updatedAt: requireTimestamp(value, 'updatedAt', path),
    );
  }

  final String captureKey;
  final String captureId;
  final String captureKind;
  final String environmentId;
  final int environmentRevision;
  final String environmentDigest;
  final int launchEnvironmentRevision;
  final String launchEnvironmentDigest;
  final int revision;
  final String source;
  final DateTime updatedAt;
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
      },
      optional: const {
        'routeId',
        'routeRevision',
        'accountId',
        'accountRevision',
        'credentialEpoch',
      },
    );
    final routeId = optionalString(value, 'routeId', path);
    final routeRevision = optionalInteger(
      value,
      'routeRevision',
      path,
      minimum: 1,
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
    if ((routeId == null) != (routeRevision == null)) {
      throw ControlContractException('$path Route evidence is incomplete');
    }
    if ((accountId == null) != (accountRevision == null) ||
        (accountId == null) != (credentialEpoch == null) ||
        accountId != null && routeId == null) {
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
      routeId: routeId,
      routeRevision: routeRevision,
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
  final String? routeId;
  final int? routeRevision;
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

final class ActivityRequestPreview {
  const ActivityRequestPreview({
    required this.kind,
    required this.text,
    required this.truncated,
  });

  factory ActivityRequestPreview.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'kind', 'text', 'truncated'});
    final kind = requireString(value, 'kind', path);
    final text = requireString(value, 'text', path);
    if (!const {'text', 'refusal', 'tool_call', 'tool_result'}.contains(kind) ||
        text.trim() != text ||
        text.runes.length > 180 ||
        _containsControlCharacter(text)) {
      throw ControlContractException('$path request preview is invalid');
    }
    return ActivityRequestPreview(
      kind: kind,
      text: text,
      truncated: requireBoolean(value, 'truncated', path),
    );
  }

  final String kind;
  final String text;
  final bool truncated;
}

final class ActivityRecord {
  const ActivityRecord({
    required this.id,
    required this.occurredAt,
    required this.title,
    required this.status,
    required this.reasonCode,
    required this.source,
    required this.conversation,
    required this.environment,
    required this.parentRefs,
    this.requestPreview,
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
        'conversation',
        'environment',
        'parentRefs',
      },
      optional: const {'reasonCode', 'requestPreview'},
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
      conversation: ActivityConversationRef.fromJson(
        value['conversation'],
        '$path.conversation',
      ),
      environment: FrozenEnvironmentRef.fromJson(
        value['environment'],
        '$path.environment',
      ),
      parentRefs: parents,
      requestPreview: value['requestPreview'] == null
          ? null
          : ActivityRequestPreview.fromJson(
              value['requestPreview'],
              '$path.requestPreview',
            ),
    );
  }

  final String id;
  final DateTime occurredAt;
  final String title;
  final String status;
  final String? reasonCode;
  final ActivitySourceRef source;
  final ActivityConversationRef conversation;
  final FrozenEnvironmentRef environment;
  final ActivityParentRefs parentRefs;
  final ActivityRequestPreview? requestPreview;

  String get sourceName => source.displayName;
  String get environmentId => environment.id;
  String? get routeId => environment.routeId;
  String? get accountId => environment.accountId;
  String? get captureRunId => parentRefs.captureRunId;
  String? get manualCaptureId => parentRefs.manualCaptureId;
}

final class ActivityConversationRef {
  const ActivityConversationRef({
    required this.id,
    required this.displayName,
    required this.kind,
    required this.evidence,
    required this.actor,
    this.clientIdentity,
  });

  factory ActivityConversationRef.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'kind', 'evidence'},
      optional: const {'displayName', 'actor', 'clientIdentity'},
    );
    final id = requireString(value, 'id', path);
    final kind = requireString(value, 'kind', path);
    final evidence = requireString(value, 'evidence', path);
    final displayName = optionalString(value, 'displayName', path);
    final actor = optionalString(value, 'actor', path);
    final clientIdentity = value['clientIdentity'] == null
        ? null
        : AgentClientIdentity.fromJson(
            value['clientIdentity'],
            '$path.clientIdentity',
          );
    if (!_conversationProjectionPattern.hasMatch(id) ||
        !const {
          'pending_exchange',
          'main',
          'agent',
          'isolated_subagent',
          'isolated_exchange',
        }.contains(kind) ||
        !const {
          'pending',
          'capture_run',
          'explicit_session',
          'explicit_actor',
          'client_asserted_subagent',
          'ambiguous_actor',
          'undecoded_exchange',
          'exchange_boundary',
        }.contains(evidence) ||
        (kind == 'agent' && (actor == null || actor.isEmpty)) ||
        (kind != 'agent' && actor != null) ||
        (clientIdentity != null &&
            (actor ?? '') != (clientIdentity.actorId ?? ''))) {
      throw ControlContractException('$path Conversation evidence is invalid');
    }
    return ActivityConversationRef(
      id: id,
      displayName: displayName,
      kind: kind,
      evidence: evidence,
      actor: actor,
      clientIdentity: clientIdentity,
    );
  }

  final String id;
  final String? displayName;
  final String kind;
  final String evidence;
  final String? actor;
  final AgentClientIdentity? clientIdentity;
}

/// One opaque, client-owned identity value. The name remains namespaced so
/// Claude and Codex can retain their native identifiers without pretending
/// their protocols share a wire schema.
final class AgentClientEvidenceValue {
  const AgentClientEvidenceValue({required this.name, required this.value});

  factory AgentClientEvidenceValue.fromJson(Object? json, String path) {
    final object = requireObject(json, path);
    requireFields(object, path, required: const {'name', 'value'});
    final name = requireString(object, 'name', path);
    final value = requireString(object, 'value', path);
    if (!_clientEvidenceNamePattern.hasMatch(name) ||
        !_validClientIdentityText(value)) {
      throw ControlContractException('$path client evidence is invalid');
    }
    return AgentClientEvidenceValue(name: name, value: value);
  }

  final String name;
  final String value;
}

final class AgentClientIdentity {
  const AgentClientIdentity({
    required this.client,
    required this.sessionId,
    required this.sessionResumable,
    required this.actorId,
    required this.actorLabel,
    required this.actorType,
    required this.actorIsSubagent,
    required this.providerResponseId,
    required this.providerMessageId,
    required this.source,
    required this.confidence,
    required this.observedAt,
    required this.protocolIds,
    required this.attributes,
  });

  factory AgentClientIdentity.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'client',
        'sessionId',
        'sessionResumable',
        'actorIsSubagent',
        'source',
        'confidence',
        'observedAt',
      },
      optional: const {
        'actorId',
        'actorLabel',
        'actorType',
        'providerResponseId',
        'providerMessageId',
        'protocolIds',
        'attributes',
      },
    );
    final client = requireString(value, 'client', path);
    final sessionId = requireString(value, 'sessionId', path);
    final actorId = optionalString(value, 'actorId', path);
    final actorLabel = optionalString(value, 'actorLabel', path);
    final actorType = optionalString(value, 'actorType', path);
    final actorIsSubagent = requireBoolean(value, 'actorIsSubagent', path);
    final providerResponseId = optionalString(
      value,
      'providerResponseId',
      path,
    );
    final providerMessageId = optionalString(value, 'providerMessageId', path);
    final source = requireString(value, 'source', path);
    final confidence = requireString(value, 'confidence', path);
    final protocolIds = _agentClientEvidenceValues(
      value['protocolIds'],
      '$path.protocolIds',
      singleValueNames: false,
    );
    final attributes = _agentClientEvidenceValues(
      value['attributes'],
      '$path.attributes',
      singleValueNames: true,
    );
    final identityValues = <String?>[
      sessionId,
      actorId,
      actorLabel,
      actorType,
      providerResponseId,
      providerMessageId,
    ];
    if (!const {'claude', 'codex'}.contains(client) ||
        !identityValues.whereType<String>().every(_validClientIdentityText) ||
        !const {
          'client_local_state',
          'client_protocol_evidence',
        }.contains(source) ||
        (source == 'client_local_state' && providerResponseId == null) ||
        confidence != 'exact' ||
        (actorId == null &&
            (actorLabel != null || actorType != null || actorIsSubagent))) {
      throw ControlContractException('$path Agent client identity is invalid');
    }
    return AgentClientIdentity(
      client: client,
      sessionId: sessionId,
      sessionResumable: requireBoolean(value, 'sessionResumable', path),
      actorId: actorId,
      actorLabel: actorLabel,
      actorType: actorType,
      actorIsSubagent: actorIsSubagent,
      providerResponseId: providerResponseId,
      providerMessageId: providerMessageId,
      source: source,
      confidence: confidence,
      observedAt: requireTimestamp(value, 'observedAt', path),
      protocolIds: protocolIds,
      attributes: attributes,
    );
  }

  final String client;
  final String sessionId;
  final bool sessionResumable;
  final String? actorId;
  final String? actorLabel;
  final String? actorType;
  final bool actorIsSubagent;
  final String? providerResponseId;
  final String? providerMessageId;
  final String source;
  final String confidence;
  final DateTime observedAt;
  final List<AgentClientEvidenceValue> protocolIds;
  final List<AgentClientEvidenceValue> attributes;

  Iterable<String> get searchableValues sync* {
    yield client;
    yield sessionId;
    if (actorId case final value?) yield value;
    if (actorLabel case final value?) yield value;
    if (actorType case final value?) yield value;
    if (providerResponseId case final value?) yield value;
    if (providerMessageId case final value?) yield value;
    for (final evidence in protocolIds) {
      yield evidence.name;
      yield evidence.value;
    }
    for (final evidence in attributes) {
      yield evidence.name;
      yield evidence.value;
    }
  }
}

List<AgentClientEvidenceValue> _agentClientEvidenceValues(
  Object? json,
  String path, {
  required bool singleValueNames,
  int maximumValues = 8192,
  bool requiredValue = false,
}) {
  if (json == null) {
    if (requiredValue) {
      throw ControlContractException('$path must be an array');
    }
    return const [];
  }
  final raw = requireList(json, path);
  if (raw.length > maximumValues) {
    throw ControlContractException('$path contains too many values');
  }
  final values = raw.indexed
      .map(
        (entry) =>
            AgentClientEvidenceValue.fromJson(entry.$2, '$path[${entry.$1}]'),
      )
      .toList(growable: false);
  final pairs = <String>{};
  final names = <String>{};
  AgentClientEvidenceValue? previous;
  for (final value in values) {
    final pair = '${value.name}\u0000${value.value}';
    final ordered =
        previous == null ||
        value.name.compareTo(previous.name) > 0 ||
        value.name == previous.name &&
            value.value.compareTo(previous.value) >= 0;
    if (!ordered ||
        !pairs.add(pair) ||
        (singleValueNames && !names.add(value.name))) {
      throw ControlContractException('$path is not canonical');
    }
    previous = value;
  }
  return List.unmodifiable(values);
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

final class ConversationRecord {
  const ConversationRecord({
    required this.conversation,
    required this.firstObservedAt,
    required this.turnCount,
    required this.latest,
  });

  factory ConversationRecord.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'conversation',
        'firstObservedAt',
        'turnCount',
        'latest',
      },
      optional: const {},
    );
    final conversation = ActivityConversationRef.fromJson(
      value['conversation'],
      '$path.conversation',
    );
    final firstObservedAt = requireTimestamp(value, 'firstObservedAt', path);
    final latest = ActivityRecord.fromJson(value['latest'], '$path.latest');
    final turnCount = requireInteger(value, 'turnCount', path, minimum: 1);
    if (latest.conversation.id != conversation.id ||
        latest.occurredAt.isBefore(firstObservedAt)) {
      throw ControlContractException(
        '$path Conversation projection is inconsistent',
      );
    }
    return ConversationRecord(
      conversation: conversation,
      firstObservedAt: firstObservedAt,
      turnCount: turnCount,
      latest: latest,
    );
  }

  final ActivityConversationRef conversation;
  final DateTime firstObservedAt;
  final int turnCount;
  final ActivityRecord latest;
}

final class ConversationPage {
  const ConversationPage({required this.items, required this.nextCursor});

  factory ConversationPage.fromJson(Object? json, String path) {
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
    return ConversationPage(
      items: rawItems.indexed
          .map(
            (entry) => ConversationRecord.fromJson(
              entry.$2,
              '$path.items[${entry.$1}]',
            ),
          )
          .toList(growable: false),
      nextCursor: nextCursor,
    );
  }

  final List<ConversationRecord> items;
  final String? nextCursor;
}

final class RawEvidenceRecovery {
  const RawEvidenceRecovery({
    required this.recoveredUncleanWriters,
    required this.purgedExpiredEnvelopes,
    required this.maximumPossibleLossMs,
  });

  factory RawEvidenceRecovery.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'recoveredUncleanWriters',
        'purgedExpiredEnvelopes',
        'maximumPossibleLossMs',
      },
    );
    final recovered = requireInteger(value, 'recoveredUncleanWriters', path);
    final maximumLoss = requireInteger(value, 'maximumPossibleLossMs', path);
    if (recovered == 0 && maximumLoss != 0) {
      throw ControlContractException('$path recovery boundary is invalid');
    }
    return RawEvidenceRecovery(
      recoveredUncleanWriters: recovered,
      purgedExpiredEnvelopes: requireInteger(
        value,
        'purgedExpiredEnvelopes',
        path,
      ),
      maximumPossibleLossMs: maximumLoss,
    );
  }

  final int recoveredUncleanWriters;
  final int purgedExpiredEnvelopes;
  final int maximumPossibleLossMs;
}

final class RawEvidenceWriter {
  const RawEvidenceWriter({
    required this.state,
    required this.admittedRecords,
    required this.durableWatermark,
    required this.queueRecords,
    required this.queueBytes,
    required this.lastFailure,
    required this.maximumUnflushedTimeMs,
  });

  factory RawEvidenceWriter.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'state',
        'admittedRecords',
        'durableWatermark',
        'queueRecords',
        'queueBytes',
        'maximumUnflushedTimeMs',
      },
      optional: const {'lastFailure'},
    );
    final state = requireString(value, 'state', path);
    final admitted = requireInteger(value, 'admittedRecords', path);
    final durable = requireInteger(value, 'durableWatermark', path);
    final queued = requireInteger(value, 'queueRecords', path);
    final queueBytes = requireInteger(value, 'queueBytes', path);
    final maximumUnflushed = requireInteger(
      value,
      'maximumUnflushedTimeMs',
      path,
    );
    final failure = optionalString(value, 'lastFailure', path);
    if (!const {'active', 'degraded', 'unavailable'}.contains(state) ||
        durable > admitted ||
        queued < 0 ||
        queueBytes < 0 ||
        maximumUnflushed < 0 ||
        (state == 'degraded') != (failure != null)) {
      throw ControlContractException('$path writer boundary is invalid');
    }
    return RawEvidenceWriter(
      state: state,
      admittedRecords: admitted,
      durableWatermark: durable,
      queueRecords: queued,
      queueBytes: queueBytes,
      lastFailure: failure,
      maximumUnflushedTimeMs: maximumUnflushed,
    );
  }

  final String state;
  final int admittedRecords;
  final int durableWatermark;
  final int queueRecords;
  final int queueBytes;
  final String? lastFailure;
  final int maximumUnflushedTimeMs;

  bool get degraded => state == 'degraded';
}

final class RawEvidenceEnvelope {
  const RawEvidenceEnvelope({
    required this.envelopeId,
    required this.layer,
    required this.scopeKind,
    required this.exchangeId,
    required this.observedAt,
    required this.expiresAt,
    required this.headerCount,
    required this.trailerCount,
    required this.bodyBytes,
    required this.digestScope,
    required this.payloadState,
    required this.redactedCredentialFields,
    required this.revealAvailable,
    this.scopeId,
    this.connectionId,
    this.attemptId,
    this.environmentId,
    this.environmentRevision,
    this.environmentDigest,
    this.clientEndpointId,
    this.clientEndpointRevision,
    this.upstreamEndpointId,
    this.upstreamEndpointRevision,
    this.protocolPlanId,
    this.protocolPlanRevision,
    this.routeId,
    this.routeRevision,
    this.accountId,
    this.accountRevision,
    this.credentialEpoch,
    this.method,
    this.statusCode,
    this.scheme,
    this.authority,
    this.path,
    this.rawQuery,
    this.contentType,
    this.contentEncoding,
    this.representation,
    this.canonicalization,
    this.bodySha256,
    this.payloadReason,
  });

  factory RawEvidenceEnvelope.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'envelopeId',
        'layer',
        'scopeKind',
        'exchangeId',
        'observedAt',
        'expiresAt',
        'headerCount',
        'trailerCount',
        'bodyBytes',
        'digestScope',
        'payloadState',
        'redactedCredentialFields',
        'revealAvailable',
      },
      optional: const {
        'scopeId',
        'connectionId',
        'attemptId',
        'environmentId',
        'environmentRevision',
        'environmentDigest',
        'clientEndpointId',
        'clientEndpointRevision',
        'upstreamEndpointId',
        'upstreamEndpointRevision',
        'protocolPlanId',
        'protocolPlanRevision',
        'routeId',
        'routeRevision',
        'accountId',
        'accountRevision',
        'credentialEpoch',
        'method',
        'statusCode',
        'scheme',
        'authority',
        'path',
        'rawQuery',
        'contentType',
        'contentEncoding',
        'representation',
        'canonicalization',
        'bodySha256',
        'payloadReason',
      },
    );
    final envelopeId = requireString(value, 'envelopeId', path);
    final layer = requireString(value, 'layer', path);
    final scopeKind = requireString(value, 'scopeKind', path);
    final exchangeId = requireString(value, 'exchangeId', path);
    final scopeId = optionalString(value, 'scopeId', path);
    final observedAt = requireTimestamp(value, 'observedAt', path);
    final expiresAt = requireTimestamp(value, 'expiresAt', path);
    final statusCode = optionalInteger(value, 'statusCode', path, minimum: 100);
    final digestScope = requireString(value, 'digestScope', path);
    final payloadState = requireString(value, 'payloadState', path);
    final bodySha256 = optionalString(value, 'bodySha256', path);
    final payloadReason = optionalString(value, 'payloadReason', path);
    final revealAvailable = requireBoolean(value, 'revealAvailable', path);
    final method = optionalString(value, 'method', path);
    final canonicalization = optionalString(value, 'canonicalization', path);
    final identities = <String?>[
      envelopeId,
      exchangeId,
      scopeId,
      optionalString(value, 'connectionId', path),
      optionalString(value, 'attemptId', path),
      optionalString(value, 'environmentId', path),
      optionalString(value, 'clientEndpointId', path),
      optionalString(value, 'upstreamEndpointId', path),
      optionalString(value, 'protocolPlanId', path),
      optionalString(value, 'routeId', path),
      optionalString(value, 'accountId', path),
    ];
    final metadata = <String?>[
      optionalString(value, 'environmentDigest', path),
      optionalString(value, 'scheme', path),
      optionalString(value, 'authority', path),
      optionalString(value, 'path', path),
      optionalString(value, 'rawQuery', path),
      optionalString(value, 'contentType', path),
      optionalString(value, 'contentEncoding', path),
      optionalString(value, 'representation', path),
      canonicalization,
    ];
    if (!identities.whereType<String>().every(_validRawIdentity) ||
        !metadata.whereType<String>().every(_validRawMetadata) ||
        !const {
          'client_ingress',
          'transform_request_input',
          'provider_egress',
          'provider_response',
          'transform_response_input',
          'client_downstream',
        }.contains(layer) ||
        !const {
          'runtime',
          'managed_run',
          'manual_capture',
        }.contains(scopeKind) ||
        (scopeKind == 'runtime' ? scopeId != null : scopeId == null) ||
        !expiresAt.isAfter(observedAt) ||
        (statusCode != null && statusCode > 599) ||
        (method != null && !RegExp(r'^[A-Z][A-Z-]*$').hasMatch(method)) ||
        !const {
          'full_body',
          'observed_prefix',
          'unavailable',
        }.contains(digestScope) ||
        !const {
          'captured',
          'metadata_only',
          'truncated',
          'unavailable',
        }.contains(payloadState) ||
        (digestScope == 'unavailable'
            ? bodySha256 != null
            : bodySha256 == null || !_digestPattern.hasMatch(bodySha256)) ||
        (payloadState == 'captured'
            ? payloadReason != null
            : payloadReason == null ||
                  !RegExp(
                    r'^[a-z][a-z0-9_]{0,127}$',
                  ).hasMatch(payloadReason)) ||
        revealAvailable !=
            const {'captured', 'truncated'}.contains(payloadState) ||
        (canonicalization != null && canonicalization != 'go_net_http_v1')) {
      throw ControlContractException('$path raw envelope is invalid');
    }
    return RawEvidenceEnvelope(
      envelopeId: envelopeId,
      layer: layer,
      scopeKind: scopeKind,
      scopeId: scopeId,
      exchangeId: exchangeId,
      connectionId: identities[3],
      attemptId: identities[4],
      environmentId: identities[5],
      environmentRevision: optionalInteger(
        value,
        'environmentRevision',
        path,
        minimum: 1,
      ),
      environmentDigest: metadata[0],
      clientEndpointId: identities[6],
      clientEndpointRevision: optionalInteger(
        value,
        'clientEndpointRevision',
        path,
        minimum: 1,
      ),
      upstreamEndpointId: identities[7],
      upstreamEndpointRevision: optionalInteger(
        value,
        'upstreamEndpointRevision',
        path,
        minimum: 1,
      ),
      protocolPlanId: identities[8],
      protocolPlanRevision: optionalInteger(
        value,
        'protocolPlanRevision',
        path,
        minimum: 1,
      ),
      routeId: identities[9],
      routeRevision: optionalInteger(value, 'routeRevision', path, minimum: 1),
      accountId: identities[10],
      accountRevision: optionalInteger(
        value,
        'accountRevision',
        path,
        minimum: 1,
      ),
      credentialEpoch: optionalInteger(
        value,
        'credentialEpoch',
        path,
        minimum: 1,
      ),
      observedAt: observedAt,
      expiresAt: expiresAt,
      method: method,
      statusCode: statusCode,
      scheme: metadata[1],
      authority: metadata[2],
      path: metadata[3],
      rawQuery: metadata[4],
      contentType: metadata[5],
      contentEncoding: metadata[6],
      representation: metadata[7],
      canonicalization: canonicalization,
      headerCount: requireInteger(value, 'headerCount', path),
      trailerCount: requireInteger(value, 'trailerCount', path),
      bodyBytes: requireInteger(value, 'bodyBytes', path),
      bodySha256: bodySha256,
      digestScope: digestScope,
      payloadState: payloadState,
      payloadReason: payloadReason,
      redactedCredentialFields: requireList(
        value['redactedCredentialFields'],
        '$path.redactedCredentialFields',
      ).map((entry) => entry! as String).toList(growable: false),
      revealAvailable: revealAvailable,
    );
  }

  final String envelopeId;
  final String layer;
  final String scopeKind;
  final String? scopeId;
  final String exchangeId;
  final String? connectionId;
  final String? attemptId;
  final String? environmentId;
  final int? environmentRevision;
  final String? environmentDigest;
  final String? clientEndpointId;
  final int? clientEndpointRevision;
  final String? upstreamEndpointId;
  final int? upstreamEndpointRevision;
  final String? protocolPlanId;
  final int? protocolPlanRevision;
  final String? routeId;
  final int? routeRevision;
  final String? accountId;
  final int? accountRevision;
  final int? credentialEpoch;
  final DateTime observedAt;
  final DateTime expiresAt;
  final String? method;
  final int? statusCode;
  final String? scheme;
  final String? authority;
  final String? path;
  final String? rawQuery;
  final String? contentType;
  final String? contentEncoding;
  final String? representation;
  final String? canonicalization;
  final int headerCount;
  final int trailerCount;
  final int bodyBytes;
  final String? bodySha256;
  final String digestScope;
  final String payloadState;
  final String? payloadReason;
  final List<String> redactedCredentialFields;
  final bool revealAvailable;
}

final class RawEvidencePage {
  const RawEvidencePage({
    required this.items,
    required this.recovery,
    required this.writer,
  });

  factory RawEvidencePage.fromJson(
    Object? json,
    String path, {
    required String expectedExchangeId,
  }) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'items', 'recovery', 'writer'});
    final rawItems = requireList(value['items'], '$path.items');
    if (rawItems.length > 4096) {
      throw ControlContractException('$path contains too many envelopes');
    }
    final items = rawItems.indexed
        .map(
          (entry) => RawEvidenceEnvelope.fromJson(
            entry.$2,
            '$path.items[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final identities = <String>{};
    DateTime? previous;
    for (final item in items) {
      if (item.exchangeId != expectedExchangeId ||
          !identities.add(item.envelopeId) ||
          (previous != null && item.observedAt.isBefore(previous))) {
        throw ControlContractException('$path envelope order is invalid');
      }
      previous = item.observedAt;
    }
    return RawEvidencePage(
      items: items,
      recovery: RawEvidenceRecovery.fromJson(
        value['recovery'],
        '$path.recovery',
      ),
      writer: RawEvidenceWriter.fromJson(value['writer'], '$path.writer'),
    );
  }

  final List<RawEvidenceEnvelope> items;
  final RawEvidenceRecovery recovery;
  final RawEvidenceWriter writer;
}

/// RawRedactedValue is what remains of a credential header value: proof the
/// field carried one, how long it was, and a database-local digest that answers
/// whether it changed. The value itself was removed before storage.
final class RawRedactedValue {
  const RawRedactedValue({required this.digest, required this.bytes});

  factory RawRedactedValue.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'digest', 'bytes'});
    final digest = requireString(value, 'digest', path);
    final bytes = requireInteger(value, 'bytes', path);
    if (!RegExp(r'^[0-9a-f]{64}$').hasMatch(digest) || bytes < 0) {
      throw ControlContractException('$path redacted value is invalid');
    }
    return RawRedactedValue(digest: digest, bytes: bytes);
  }

  final String digest;
  final int bytes;
}

/// RawHeaderField keeps a header's name, order and multiplicity as wire
/// evidence. A credential field carries [redacted] instead of [values]; a field
/// carrying both would mean a credential reached storage, so the contract
/// rejects it.
final class RawHeaderField {
  const RawHeaderField({
    required this.name,
    required this.values,
    required this.redacted,
  });

  factory RawHeaderField.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'name'},
      optional: const {'values', 'redacted'},
    );
    final name = requireString(value, 'name', path);
    final rawValues = value['values'] == null
        ? const <Object?>[]
        : requireList(value['values'], '$path.values');
    final rawRedacted = value['redacted'] == null
        ? const <Object?>[]
        : requireList(value['redacted'], '$path.redacted');
    if (!RegExp(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$").hasMatch(name) ||
        rawValues.length > 1024 ||
        rawRedacted.length > 1024 ||
        (rawValues.isNotEmpty && rawRedacted.isNotEmpty)) {
      throw ControlContractException('$path header field is invalid');
    }
    final values = rawValues.indexed
        .map((entry) {
          final item = entry.$2;
          if (item is! String ||
              utf8.encode(item).length > 64 * 1024 ||
              RegExp(r'[\u0000\r\n]').hasMatch(item)) {
            throw ControlContractException(
              '$path.values[${entry.$1}] is invalid',
            );
          }
          return item;
        })
        .toList(growable: false);
    final redacted = rawRedacted.indexed
        .map(
          (entry) => RawRedactedValue.fromJson(
            entry.$2,
            '$path.redacted[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    return RawHeaderField(name: name, values: values, redacted: redacted);
  }

  final String name;
  final List<String> values;
  final List<RawRedactedValue> redacted;
}

final class RawFrame {
  const RawFrame({
    required this.kind,
    required this.offset,
    required this.length,
  });

  factory RawFrame.fromJson(
    Object? json,
    String path, {
    required int bodyBytes,
  }) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'kind', 'offset', 'length'});
    final kind = requireString(value, 'kind', path);
    final offset = requireInteger(value, 'offset', path);
    final length = requireInteger(value, 'length', path, minimum: 1);
    if (!const {'data', 'keepalive', 'abort'}.contains(kind) ||
        offset + length > bodyBytes) {
      throw ControlContractException('$path frame range is invalid');
    }
    return RawFrame(kind: kind, offset: offset, length: length);
  }

  final String kind;
  final int offset;
  final int length;
}

final class RevealedRawEvidence {
  const RevealedRawEvidence({
    required this.envelope,
    required this.headers,
    required this.trailers,
    required this.body,
    required this.frames,
  });

  factory RevealedRawEvidence.fromJson(
    Object? json,
    String path, {
    required String expectedEnvelopeId,
  }) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'envelope',
        'headers',
        'trailers',
        'bodyBase64',
        'frames',
      },
    );
    final envelope = RawEvidenceEnvelope.fromJson(
      value['envelope'],
      '$path.envelope',
    );
    final encodedBody = requireStringValue(value, 'bodyBase64', path);
    late final Uint8List body;
    try {
      body = Uint8List.fromList(base64.decode(encodedBody));
    } on FormatException {
      throw ControlContractException('$path.bodyBase64 is invalid');
    }
    if (base64.encode(body) != encodedBody ||
        envelope.envelopeId != expectedEnvelopeId ||
        !envelope.revealAvailable ||
        (envelope.payloadState == 'captured' &&
            envelope.bodyBytes != body.length) ||
        body.length > envelope.bodyBytes) {
      throw ControlContractException('$path revealed payload is inconsistent');
    }
    final digestCanBeRecomputed =
        envelope.payloadState == 'captured' ||
        envelope.digestScope == 'observed_prefix';
    if (digestCanBeRecomputed &&
        crypto.sha256.convert(body).toString() != envelope.bodySha256) {
      throw ControlContractException('$path body digest does not match');
    }
    final headers = _rawHeaderFields(value['headers'], '$path.headers');
    final trailers = _rawHeaderFields(value['trailers'], '$path.trailers');
    // headerCount is the number of values observed on the wire, counted before
    // redaction. A redacted credential field therefore still contributes its
    // values; counting only the surviving ones would make the reveal of every
    // credential-bearing envelope fail its own reconciliation.
    int observedValues(List<RawHeaderField> fields) => fields.fold<int>(
      0,
      (sum, field) => sum + field.values.length + field.redacted.length,
    );
    if (observedValues(headers) != envelope.headerCount ||
        observedValues(trailers) != envelope.trailerCount) {
      throw ControlContractException('$path header counts are inconsistent');
    }
    final rawFrames = _rawEvidenceList(value['frames'], '$path.frames');
    if (rawFrames.length > 65536) {
      throw ControlContractException('$path contains too many frames');
    }
    final frames = rawFrames.indexed
        .map(
          (entry) => RawFrame.fromJson(
            entry.$2,
            '$path.frames[${entry.$1}]',
            bodyBytes: body.length,
          ),
        )
        .toList(growable: false);
    return RevealedRawEvidence(
      envelope: envelope,
      headers: headers,
      trailers: trailers,
      body: body,
      frames: frames,
    );
  }

  final RawEvidenceEnvelope envelope;
  final List<RawHeaderField> headers;
  final List<RawHeaderField> trailers;
  final Uint8List body;
  final List<RawFrame> frames;
}

List<RawHeaderField> _rawHeaderFields(Object? json, String path) {
  final values = _rawEvidenceList(json, path);
  if (values.length > 4096) {
    throw ControlContractException('$path contains too many fields');
  }
  return values.indexed
      .map((entry) => RawHeaderField.fromJson(entry.$2, '$path[${entry.$1}]'))
      .toList(growable: false);
}

List<Object?> _rawEvidenceList(Object? json, String path) {
  // The service contract emits arrays. Treat a present null as the empty
  // collection it represents so one malformed optional HTTP collection cannot
  // hide the rest of an otherwise valid captured envelope.
  if (json == null) return const <Object?>[];
  return requireList(json, path);
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
    required this.toolNamespace,
    required this.arguments,
    required this.toolError,
    required this.providerSource,
    required this.providerKind,
    required this.fingerprint,
    required this.agent,
  });

  factory ExchangeContentBlock.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'availability', 'originalSize'},
      optional: const {
        'text',
        'callId',
        'toolName',
        'toolNamespace',
        'arguments',
        'toolError',
        'providerSource',
        'providerKind',
        'fingerprint',
        'agent',
      },
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
      toolNamespace: optionalString(value, 'toolNamespace', path),
      arguments: arguments,
      toolError: toolErrorValue == true,
      providerSource: optionalString(value, 'providerSource', path),
      providerKind: optionalString(value, 'providerKind', path),
      fingerprint: optionalString(value, 'fingerprint', path),
      agent: value['agent'] == null
          ? null
          : ExchangeAgentContext.fromJson(value['agent'], '$path.agent'),
    );
  }

  final String kind;
  final String availability;
  final String? text;
  final int originalSize;
  final String? callId;
  final String? toolName;
  final String? toolNamespace;
  final JsonObject? arguments;
  final bool toolError;
  final String? providerSource;
  final String? providerKind;
  final String? fingerprint;
  final ExchangeAgentContext? agent;
}

final class ExchangeContentMessage {
  const ExchangeContentMessage({
    required this.role,
    required this.blocks,
    required this.agent,
  });

  factory ExchangeContentMessage.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'role', 'blocks'},
      optional: const {'agent'},
    );
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
      agent: value['agent'] == null
          ? null
          : ExchangeAgentContext.fromJson(value['agent'], '$path.agent'),
    );
  }

  final String role;
  final List<ExchangeContentBlock> blocks;
  final ExchangeAgentContext? agent;
}

final class ExchangeAgentContext {
  const ExchangeAgentContext({
    required this.agentName,
    required this.author,
    required this.recipient,
  });

  factory ExchangeAgentContext.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {},
      optional: const {'agentName', 'author', 'recipient'},
    );
    final context = ExchangeAgentContext(
      agentName: optionalString(value, 'agentName', path),
      author: optionalString(value, 'author', path),
      recipient: optionalString(value, 'recipient', path),
    );
    if (context.agentName == null &&
        context.author == null &&
        context.recipient == null) {
      throw ControlContractException('$path agent context is empty');
    }
    if ((context.author == null) != (context.recipient == null)) {
      throw ControlContractException('$path agent direction is incomplete');
    }
    return context;
  }

  final String? agentName;
  final String? author;
  final String? recipient;
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
    required this.system,
    required this.messages,
    required this.tools,
    required this.protocolEvidence,
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
        'system',
        'messages',
        'tools',
        'protocolEvidence',
      },
    );
    final rawSystem = requireList(value['system'], '$path.system');
    final rawMessages = requireList(value['messages'], '$path.messages');
    final rawTools = requireList(value['tools'], '$path.tools');
    return ExchangeRequest(
      requestedModel: requireString(value, 'requestedModel', path),
      effectiveModel: requireString(value, 'effectiveModel', path),
      maxOutputTokens: requireInteger(value, 'maxOutputTokens', path),
      stream: requireBoolean(value, 'stream', path),
      system: rawSystem.indexed
          .map(
            (entry) => ExchangeContentBlock.fromJson(
              entry.$2,
              '$path.system[${entry.$1}]',
            ),
          )
          .toList(growable: false),
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
      protocolEvidence: _agentClientEvidenceValues(
        value['protocolEvidence'],
        '$path.protocolEvidence',
        singleValueNames: true,
        requiredValue: true,
      ),
    );
  }

  final String requestedModel;
  final String effectiveModel;
  final int maxOutputTokens;
  final bool stream;

  /// The dialect's top-level instruction parameter — Anthropic `system`, OpenAI
  /// Responses `instructions` — presented as per-request configuration rather
  /// than a turn. A dialect without one, such as OpenAI Chat Completions,
  /// leaves this empty and keeps its instruction inside [messages].
  final List<ExchangeContentBlock> system;
  final List<ExchangeContentMessage> messages;
  final List<ExchangeToolDefinition> tools;
  final List<AgentClientEvidenceValue> protocolEvidence;
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
    required this.protocolEvidence,
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
        'protocolEvidence',
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
      protocolEvidence: _agentClientEvidenceValues(
        value['protocolEvidence'],
        '$path.protocolEvidence',
        singleValueNames: true,
        requiredValue: true,
      ),
    );
  }

  final String id;
  final String requestedModel;
  final String effectiveModel;
  final String reportedModel;
  final String stopReason;
  final List<ExchangeContentBlock> blocks;
  final ExchangeUsage usage;
  final List<AgentClientEvidenceValue> protocolEvidence;
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

final class AgentConversationProjection {
  const AgentConversationProjection({
    required this.scope,
    required this.agents,
    required this.relationships,
    required this.actions,
  });

  factory AgentConversationProjection.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'scope', 'agents', 'relationships', 'actions'},
    );
    final scope = requireString(value, 'scope', path);
    if (!const {'capture_run', 'exchange'}.contains(scope)) {
      throw ControlContractException('$path scope is unsupported');
    }
    final agents = requireList(value['agents'], '$path.agents').indexed
        .map(
          (entry) => AgentConversationAgent.fromJson(
            entry.$2,
            '$path.agents[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    final names = agents.map((agent) => agent.name).toSet();
    if (names.length != agents.length) {
      throw ControlContractException('$path agents are duplicated');
    }
    final relationships =
        requireList(value['relationships'], '$path.relationships').indexed
            .map((entry) {
              final relationship = AgentConversationRelationship.fromJson(
                entry.$2,
                '$path.relationships[${entry.$1}]',
              );
              if (!names.contains(relationship.source) ||
                  !names.contains(relationship.target)) {
                throw ControlContractException(
                  '$path relationship references an unknown agent',
                );
              }
              return relationship;
            })
            .toList(growable: false);
    final actions = requireList(value['actions'], '$path.actions').indexed
        .map(
          (entry) => AgentConversationAction.fromJson(
            entry.$2,
            '$path.actions[${entry.$1}]',
          ),
        )
        .toList(growable: false);
    if (agents.isEmpty && actions.isEmpty) {
      throw ControlContractException('$path projection is empty');
    }
    return AgentConversationProjection(
      scope: scope,
      agents: agents,
      relationships: relationships,
      actions: actions,
    );
  }

  final String scope;
  final List<AgentConversationAgent> agents;
  final List<AgentConversationRelationship> relationships;
  final List<AgentConversationAction> actions;
}

final class AgentConversationAgent {
  const AgentConversationAgent({required this.name});

  factory AgentConversationAgent.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'name'});
    return AgentConversationAgent(name: requireString(value, 'name', path));
  }

  final String name;
}

final class AgentConversationRelationship {
  const AgentConversationRelationship({
    required this.source,
    required this.target,
    required this.kind,
  });

  factory AgentConversationRelationship.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(value, path, required: const {'source', 'target', 'kind'});
    final kind = requireString(value, 'kind', path);
    if (kind != 'message') {
      throw ControlContractException('$path relationship kind is unsupported');
    }
    return AgentConversationRelationship(
      source: requireString(value, 'source', path),
      target: requireString(value, 'target', path),
      kind: kind,
    );
  }

  final String source;
  final String target;
  final String kind;
}

final class AgentConversationAction {
  const AgentConversationAction({
    required this.callId,
    required this.name,
    required this.status,
    required this.sourceAgent,
    required this.resultAgent,
    required this.attributed,
  });

  factory AgentConversationAction.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'callId', 'name', 'status', 'attributed'},
      optional: const {'sourceAgent', 'resultAgent'},
    );
    final status = requireString(value, 'status', path);
    if (!const {'requested', 'completed', 'failed'}.contains(status)) {
      throw ControlContractException('$path action status is unsupported');
    }
    return AgentConversationAction(
      callId: requireString(value, 'callId', path),
      name: requireString(value, 'name', path),
      status: status,
      sourceAgent: optionalString(value, 'sourceAgent', path),
      resultAgent: optionalString(value, 'resultAgent', path),
      attributed: requireBoolean(value, 'attributed', path),
    );
  }

  final String callId;
  final String name;
  final String status;
  final String? sourceAgent;
  final String? resultAgent;
  final bool attributed;
}

final class ExchangeContentDetail {
  const ExchangeContentDetail({
    required this.state,
    required this.mode,
    required this.recordedAt,
    required this.expiresAt,
    required this.requestProjection,
    required this.agentConversation,
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
        agentConversation: null,
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
      optional: const {'agentConversation', 'response'},
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
      agentConversation: value['agentConversation'] == null
          ? null
          : AgentConversationProjection.fromJson(
              value['agentConversation'],
              '$path.agentConversation',
            ),
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
  final AgentConversationProjection? agentConversation;
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
    this.clientIdentity,
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
      optional: const {'diagnosis', 'clientIdentity'},
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
      clientIdentity: value['clientIdentity'] == null
          ? null
          : AgentClientIdentity.fromJson(
              value['clientIdentity'],
              '$path.clientIdentity',
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
  final AgentClientIdentity? clientIdentity;
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
    required this.captureNextCursor,
    required this.environments,
    required this.endpoints,
    required this.accounts,
  });

  final RuntimeStatus status;
  final List<CaptureRecord> captures;
  final String? captureNextCursor;
  final List<EnvironmentRecord> environments;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
}

/// One reason a delete did not happen.
///
/// Every destructive operation answers with the same shape, so the workbench
/// explains a refusal the same way wherever it happens. Both fields are
/// required by the runtime: an id with no label is unreadable, and a label with
/// no id is unactionable.
final class DeletionHolder {
  const DeletionHolder({
    required this.kind,
    required this.id,
    required this.label,
    required this.detail,
  });

  factory DeletionHolder.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'kind', 'id', 'label'},
      optional: const {'detail'},
    );
    return DeletionHolder(
      kind: requireString(value, 'kind', path),
      id: requireString(value, 'id', path),
      label: requireString(value, 'label', path),
      detail: optionalString(value, 'detail', path) ?? '',
    );
  }

  final String kind;
  final String id;
  final String label;
  final String detail;
}

/// What a completed delete released.
final class DeletionReleased {
  const DeletionReleased({
    required this.exchanges,
    required this.envelopes,
    required this.activities,
    required this.connections,
    required this.attempts,
    required this.approvals,
    required this.assignments,
    required this.captures,
  });

  factory DeletionReleased.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'exchanges',
        'envelopes',
        'activities',
        'connections',
        'attempts',
        'approvals',
        'assignments',
        'captures',
      },
    );
    return DeletionReleased(
      exchanges: requireInteger(value, 'exchanges', path),
      envelopes: requireInteger(value, 'envelopes', path),
      activities: requireInteger(value, 'activities', path),
      connections: requireInteger(value, 'connections', path),
      attempts: requireInteger(value, 'attempts', path),
      approvals: requireInteger(value, 'approvals', path),
      assignments: requireInteger(value, 'assignments', path),
      captures: requireInteger(value, 'captures', path),
    );
  }

  final int exchanges;
  final int envelopes;
  final int activities;
  final int connections;
  final int attempts;
  final int approvals;
  final int assignments;
  final int captures;
}

/// The single answer every destructive operation gives.
final class DeletionOutcome {
  const DeletionOutcome({
    required this.deleted,
    required this.holderCount,
    required this.holders,
    required this.released,
  });

  factory DeletionOutcome.fromJson(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'deleted', 'holderCount', 'holders'},
      optional: const {'released'},
    );
    final deleted = requireBoolean(value, 'deleted', path);
    final holderCount = requireInteger(value, 'holderCount', path);
    final holders = requireList(value['holders'], '$path.holders')
        .map((item) => DeletionHolder.fromJson(item, '$path.holders'))
        .toList(growable: false);
    // The two answers are mutually exclusive at the source, and a client that
    // does not enforce that would render "deleted" over a list of reasons it
    // was not.
    if (deleted && holderCount != 0) {
      throw ControlContractException(
        '$path reported a completed delete with $holderCount holders',
      );
    }
    if (!deleted && holderCount == 0) {
      throw ControlContractException(
        '$path refused a delete without naming a holder',
      );
    }
    if (holders.length > holderCount) {
      throw ControlContractException(
        '$path listed more holders than it counted',
      );
    }
    return DeletionOutcome(
      deleted: deleted,
      holderCount: holderCount,
      holders: holders,
      released: value['released'] == null
          ? null
          : DeletionReleased.fromJson(value['released'], '$path.released'),
    );
  }

  final bool deleted;

  /// The total, which can exceed [holders] when the runtime truncated the list.
  final int holderCount;
  final List<DeletionHolder> holders;
  final DeletionReleased? released;

  bool get truncated => holderCount > holders.length;
}

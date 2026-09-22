import 'control_models.dart';

/// Upstream account observations, never ViberMate's per-Exchange token totals.
final class AccountFacts {
  AccountFacts.fromJson(Object? json) {
    const path = 'accountFacts';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'accountId',
        'credentialEpoch',
        'origin',
        'adapterId',
        'adapterRevision',
        'observedAt',
        'state',
        'limits',
      },
      optional: const {
        'planType',
        'upstreamAccountId',
        'upstreamUserId',
        'credits',
        'history',
      },
    );
    accountId = requireString(value, 'accountId', path);
    credentialEpoch = requireInteger(
      value,
      'credentialEpoch',
      path,
      minimum: 1,
    );
    origin = requireString(value, 'origin', path);
    adapterId = requireString(value, 'adapterId', path);
    adapterRevision = requireInteger(
      value,
      'adapterRevision',
      path,
      minimum: 1,
    );
    observedAt = requireTimestamp(value, 'observedAt', path);
    state = requireString(value, 'state', path);
    if (!const {
      'known',
      'stale',
      'unsupported',
      'unavailable',
    }.contains(state)) {
      throw const ControlContractException('account facts state is invalid');
    }
    planType = _text(value, 'planType', path);
    upstreamAccountId = _text(value, 'upstreamAccountId', path);
    upstreamUserId = _text(value, 'upstreamUserId', path);
    final items = requireList(value['limits'], '$path.limits');
    if (items.length > 65) {
      throw const ControlContractException('too many account quota buckets');
    }
    limits = List.unmodifiable(items.map(AccountQuotaLimit.fromJson));
    credits = value['credits'] == null
        ? null
        : AccountCredits.fromJson(value['credits']);
    history = value['history'] == null
        ? null
        : AccountHistory.fromJson(value['history']);
  }
  late final String accountId, origin, adapterId, state;
  late final int credentialEpoch, adapterRevision;
  late final DateTime observedAt;
  late final String? planType, upstreamAccountId, upstreamUserId;
  late final List<AccountQuotaLimit> limits;
  late final AccountCredits? credits;
  late final AccountHistory? history;
}

final class AccountQuotaLimit {
  AccountQuotaLimit.fromJson(Object? json) {
    const path = 'accountQuota';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id'},
      optional: const {
        'name',
        'model',
        'allowed',
        'limitReached',
        'primary',
        'secondary',
      },
    );
    id = requireString(value, 'id', path);
    name = _text(value, 'name', path);
    model = _text(value, 'model', path);
    allowed = value['allowed'] == null
        ? null
        : requireBoolean(value, 'allowed', path);
    limitReached = value['limitReached'] == null
        ? null
        : requireBoolean(value, 'limitReached', path);
    primary = value['primary'] == null
        ? null
        : AccountQuotaWindow.fromJson(value['primary']);
    secondary = value['secondary'] == null
        ? null
        : AccountQuotaWindow.fromJson(value['secondary']);
  }
  late final String id;
  late final String? name, model;
  late final bool? allowed, limitReached;
  late final AccountQuotaWindow? primary, secondary;
}

final class AccountQuotaWindow {
  AccountQuotaWindow.fromJson(Object? json) {
    const path = 'quotaWindow';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'usedPercent',
        'windowSeconds',
        'resetAfterSeconds',
        'resetAt',
      },
    );
    usedPercent = requireInteger(value, 'usedPercent', path, minimum: 0);
    windowSeconds = requireInteger(value, 'windowSeconds', path, minimum: 0);
    resetAfterSeconds = requireInteger(
      value,
      'resetAfterSeconds',
      path,
      minimum: 0,
    );
    resetAt = DateTime.fromMillisecondsSinceEpoch(
      requireInteger(value, 'resetAt', path, minimum: 0) * 1000,
      isUtc: true,
    );
  }
  late final int usedPercent, windowSeconds, resetAfterSeconds;
  late final DateTime resetAt;
}

final class AccountCredits {
  AccountCredits.fromJson(Object? json) {
    const path = 'accountCredits';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'hasCredits', 'unlimited'},
      optional: const {'balance'},
    );
    hasCredits = requireBoolean(value, 'hasCredits', path);
    unlimited = requireBoolean(value, 'unlimited', path);
    balance = _text(value, 'balance', path);
  }
  late final bool hasCredits, unlimited;
  late final String? balance;
}

final class AccountHistory {
  AccountHistory.fromJson(Object? json) {
    const path = 'accountHistory';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'daily', 'partial'},
      optional: const {
        'lifetimeTokens',
        'peakDailyTokens',
        'currentStreakDays',
        'asOf',
      },
    );
    lifetimeTokens = value['lifetimeTokens'] == null
        ? null
        : requireInteger(value, 'lifetimeTokens', path, minimum: 0);
    peakDailyTokens = value['peakDailyTokens'] == null
        ? null
        : requireInteger(value, 'peakDailyTokens', path, minimum: 0);
    currentStreakDays = value['currentStreakDays'] == null
        ? null
        : requireInteger(value, 'currentStreakDays', path, minimum: 0);
    partial = requireBoolean(value, 'partial', path);
    asOf = _text(value, 'asOf', path);
    final items = requireList(value['daily'], '$path.daily');
    if (items.length > 3660) {
      throw const ControlContractException('too many history buckets');
    }
    daily = List.unmodifiable(
      items.map((item) {
        final bucket = requireObject(item, '$path.daily');
        requireFields(
          bucket,
          '$path.daily',
          required: const {'date', 'tokens'},
        );
        return (
          date: requireString(bucket, 'date', path),
          tokens: requireInteger(bucket, 'tokens', path, minimum: 0),
        );
      }),
    );
  }
  late final int? lifetimeTokens, peakDailyTokens, currentStreakDays;
  late final String? asOf;
  late final bool partial;
  late final List<({String date, int tokens})> daily;
}

String? _text(JsonObject value, String key, String path) {
  final text = optionalString(value, key, path);
  if (text != null &&
      (text.length > 512 || text.runes.any((c) => c < 32 || c == 127))) {
    throw ControlContractException('$path.$key must be bounded display text');
  }
  return text;
}

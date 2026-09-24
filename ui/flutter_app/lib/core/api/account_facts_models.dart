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
        'rateLimitResets',
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
    rateLimitResets = value['rateLimitResets'] == null
        ? null
        : AccountRateLimitResets.fromJson(value['rateLimitResets']);
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
  late final AccountRateLimitResets? rateLimitResets;
  late final AccountHistory? history;
}

final class AccountRateLimitResets {
  AccountRateLimitResets.fromJson(Object? json) {
    const path = 'accountRateLimitResets';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'availableCount'},
      optional: const {'applicableAvailableCount', 'details'},
    );
    availableCount = requireInteger(value, 'availableCount', path, minimum: 0);
    applicableAvailableCount = value['applicableAvailableCount'] == null
        ? null
        : requireInteger(value, 'applicableAvailableCount', path, minimum: 0);
    if (applicableAvailableCount != null &&
        applicableAvailableCount! > availableCount) {
      throw const ControlContractException(
        'applicable resets exceed available resets',
      );
    }
    final rawDetails = value['details'];
    if (rawDetails == null) {
      details = null;
    } else {
      final items = requireList(rawDetails, '$path.details');
      if (items.length > 64) {
        throw const ControlContractException('too many reset-credit details');
      }
      details = List.unmodifiable(items.map(AccountResetCredit.fromJson));
    }
  }

  late final int availableCount;
  late final int? applicableAvailableCount;
  late final List<AccountResetCredit>? details;
}

final class AccountResetCredit {
  AccountResetCredit.fromJson(Object? json) {
    const path = 'accountResetCredit';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'resetType', 'status', 'grantedAt'},
      optional: const {'expiresAt', 'title', 'description'},
    );
    id = requireString(value, 'id', path);
    resetType = requireString(value, 'resetType', path);
    status = requireString(value, 'status', path);
    grantedAt = requireTimestamp(value, 'grantedAt', path);
    expiresAt = value['expiresAt'] == null || value['expiresAt'] == ''
        ? null
        : requireTimestamp(value, 'expiresAt', path);
    title = _text(value, 'title', path);
    description = _text(value, 'description', path);
    if (id.isEmpty ||
        id.length > 256 ||
        resetType.length > 64 ||
        status.length > 64) {
      throw const ControlContractException('reset credit identity is invalid');
    }
  }

  late final String id, resetType, status;
  late final DateTime grantedAt;
  late final DateTime? expiresAt;
  late final String? title, description;

  bool get available =>
      resetType == 'codex_rate_limits' &&
      status == 'available' &&
      (expiresAt == null || expiresAt!.isAfter(DateTime.now().toUtc()));
}

final class AccountResetRedemption {
  AccountResetRedemption.fromJson(Object? json) {
    const path = 'accountResetRedemption';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {
        'accountId',
        'credentialEpoch',
        'creditId',
        'outcome',
        'windowsReset',
      },
    );
    accountId = requireString(value, 'accountId', path);
    credentialEpoch = requireInteger(
      value,
      'credentialEpoch',
      path,
      minimum: 1,
    );
    creditId = requireString(value, 'creditId', path);
    outcome = requireString(value, 'outcome', path);
    windowsReset = requireInteger(value, 'windowsReset', path, minimum: 0);
    if (accountId.isEmpty ||
        creditId.isEmpty ||
        windowsReset > 64 ||
        !const {
          'reset',
          'nothing_to_reset',
          'no_credit',
          'already_redeemed',
        }.contains(outcome)) {
      throw const ControlContractException(
        'reset redemption response is invalid',
      );
    }
  }

  late final String accountId, creditId, outcome;
  late final int credentialEpoch, windowsReset;
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

Map<String, Object?> runtimeUsagePayload() => {
  'schema': 'vibermate-runtime-usage-report-v1',
  'pricing': {
    'source': 'models.dev',
    'currency': 'USD',
    'basis': 'current_standard_api',
    'state': 'ready',
    'updatedAt': '2026-08-24T13:00:00Z',
  },
  'collection': {
    'enabled': true,
    'retentionDays': 90,
    'revision': 1,
    'collectingSince': '2026-07-27T00:00:00Z',
  },
  'total': usageGroupPayload('all'),
  'snapshot': List.filled(64, 'a').join(),
  'dimension': '',
  'filters': <Object?>[],
  'groups': <Object?>[],
  'nextCursor': '',
  'generatedAt': '2026-08-24T14:00:00.000Z',
  'period': {
    'from': '2026-07-27',
    'until': '2026-08-26',
    'timeZone': 'Asia/Singapore',
  },
  'days': [dayUsagePayload('2026-08-24')],
};

Map<String, Object?> dayUsagePayload(String date) => {
  'date': date,
  'agentApiCalls': 2,
  'succeeded': 1,
  'failed': 1,
  'canceled': 0,
  'modelUnavailableCalls': 0,
  'tokens': tokenUsagePayload(),
  'cost': costUsagePayload(),
};

Map<String, Object?> usageGroupPayload(String id) => {
  'id': id,
  'label': id,
  'dimension': '',
  'evidence': '',
  'agentApiCalls': 2,
  'succeeded': 1,
  'failed': 1,
  'canceled': 0,
  'tokens': tokenUsagePayload(),
  'cost': costUsagePayload(),
};

Map<String, Object?> costUsagePayload() => {
  'nanoUsd': 12500000,
  'pricedCalls': 1,
  'partialCalls': 1,
  'unpricedCalls': 1,
};

Map<String, Object?> tokenUsagePayload() => {
  'inputUncached': {'tokens': 42, 'knownCalls': 1, 'unknownCalls': 1},
  'cacheWrite': {'tokens': 0, 'knownCalls': 0, 'unknownCalls': 2},
  'cacheRead': {'tokens': 5, 'knownCalls': 1, 'unknownCalls': 1},
  'output': {'tokens': 9, 'knownCalls': 1, 'unknownCalls': 1},
  'reasoning': {'tokens': 0, 'knownCalls': 0, 'unknownCalls': 2},
};

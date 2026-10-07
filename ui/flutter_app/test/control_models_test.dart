import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

import 'runtime_usage_fixture.dart';
import 'fixtures/retained_identity_pages.dart';

void main() {
  test('retained metadata identity whitespace and UTF8 limits stay exact', () {
    final base = <String, Object?>{
      'kind': 'tool_call',
      'availability': 'recorded',
      'originalSize': 0,
      'textBytes': 0,
      'argumentBytes': 2,
      'callId': 'call',
      'toolName': 'f',
      'toolNamespace': 'ns',
    };
    final spaces = [
      for (var c = 0x09; c <= 0x0d; c++) c,
      0x20,
      0x85,
      0xa0,
      0x1680,
      for (var c = 0x2000; c <= 0x200a; c++) c,
      0x2028,
      0x2029,
      0x202f,
      0x205f,
      0x3000,
    ];
    for (final key in ['callId', 'toolName', 'toolNamespace']) {
      for (final c in spaces) {
        final space = String.fromCharCode(c);
        for (final value in ['${space}id', 'id$space']) {
          expect(
            () => ExchangeBlockPageMetadata.fromJson({
              ...base,
              key: value,
            }, 'metadata'),
            throwsA(isA<ControlContractException>()),
            reason: '$key boundary U+${c.toRadixString(16)}',
          );
        }
      }
      for (final value in ['a\tb', 'a\u00a0b', 'a\u2028b', 'a\ufeffb']) {
        expect(
          ExchangeBlockPageMetadata.fromJson({
            ...base,
            key: value,
          }, 'metadata').values[key],
          value,
        );
      }
      for (final value in ['a\rb', 'a\nb', 'a\u0000b', '']) {
        if (key == 'toolNamespace' && value.isEmpty) continue;
        expect(
          () => ExchangeBlockPageMetadata.fromJson({
            ...base,
            key: value,
          }, 'metadata'),
          throwsA(isA<ControlContractException>()),
        );
      }
      final maximum = key == 'callId' ? 512 : 256;
      final exact = '\ufeff${'é' * ((maximum - 4) ~/ 2)}x';
      expect(utf8.encode(exact).length, maximum);
      expect(
        ExchangeBlockPageMetadata.fromJson({
          ...base,
          key: exact,
        }, 'metadata').values[key],
        exact,
      );
      expect(
        () => ExchangeBlockPageMetadata.fromJson({
          ...base,
          key: '${exact}x',
        }, 'metadata'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final withoutNamespace = {...base}..remove('toolNamespace');
    expect(
      ExchangeBlockPageMetadata.fromJson(
        withoutNamespace,
        'metadata',
      ).values.containsKey('toolNamespace'),
      isFalse,
    );
    expect(
      () => ExchangeBlockPageMetadata.fromJson({
        ...base,
        'toolNamespace': '',
      }, 'metadata'),
      throwsA(isA<ControlContractException>()),
    );
  });
  test(
    'real Store tool detail pages preserve retained boundary BOM identities',
    () {
      final cases = jsonDecode(retainedIdentityPages) as List;
      expect(cases.length, 18);
      for (final example in cases) {
        for (final wire in example['pages'] as List) {
          final page = ExchangeContentPage.fromJson(
            wire,
            example['name'] as String,
          );
          final metadata = page.blockMetadata!.values;
          for (final key in ['callId', 'toolName', 'toolNamespace']) {
            expect(
              metadata[key] ?? '',
              example[key],
              reason: '${example['name']} $key',
            );
          }
          expect(page.text, page.kind == 'text' ? 'x' : '{}');
        }
      }
    },
  );
  test('preview complete record is scoped and strictly decoded', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final activity = (await api.activities(captureRunId: 'run-1')).items.first;
    final page = await api.exchangeContentPage(
      activity.id,
      'preview-complete-record',
    );
    expect(page.kind, 'block_bytes');
    expect(jsonDecode(utf8.decode(page.data))['providerSource'], 'preview');
    await expectLater(
      api.exchangeContentPage('other', 'preview-complete-record'),
      throwsA(isA<ControlContractException>()),
    );
    await expectLater(
      api.exchangeContentPage(activity.id, 'wrong'),
      throwsA(isA<ControlContractException>()),
    );
  });
  test('Complete block byte pages retain exact transport bytes', () {
    final bytes = [123, 34, 0xff, 0xe2, 0x80];
    final page = ExchangeContentPage.fromJson({
      'exchangeId': 'e',
      'kind': 'block_bytes',
      'messages': [],
      'blocks': [],
      'offset': 33554432,
      'total': 33554437,
      'data': base64.encode(bytes),
    }, 'page');
    expect(page.kind, 'block_bytes');
    expect(page.data, bytes);
  });

  test('Block detail page union and encoded bounds are strict', () {
    final valid = <String, Object?>{
      'exchangeId': 'e',
      'kind': 'block_bytes',
      'messages': [],
      'blocks': [],
      'offset': 0,
      'total': 1,
      'data': 'eA==',
    };
    for (final bad in <Map<String, Object?>>[
      {...valid, 'data': 'eA'},
      {...valid, 'data': 'eA__'},
      {...valid, 'data': 'A' * 43696},
      {...valid, 'data': 'eA==', 'text': 'hidden'},
      {...valid, 'canonicalCursor': 'c'},
      {...valid, 'total': 549754896385},
      {...valid, 'offset': 2},
      {...valid, 'nextCursor': 'c'},
      {...valid, 'kind': 'text'},
      {...valid, 'unknown': true},
    ]) {
      expect(
        () => ExchangeContentPage.fromJson(bad, 'page'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final metadata = {
      'kind': 'reasoning',
      'availability': 'recorded',
      'originalSize': 5,
      'providerSource': 'source',
      'providerKind': 'thinking',
      'textBytes': 5,
      'argumentBytes': 0,
    };
    final ordinary = ExchangeContentPage.fromJson({
      'exchangeId': 'e',
      'kind': 'text',
      'messages': [],
      'blocks': [],
      'offset': 0,
      'total': 5,
      'text': 'hello',
      'blockMetadata': metadata,
      'canonicalCursor': 'complete',
    }, 'page');
    expect(ordinary.blockMetadata!.values['providerSource'], 'source');
    expect(ordinary.canonicalCursor, 'complete');
    expect(
      () => ExchangeBlockPageMetadata.fromJson({
        ...metadata,
        'text': 'forged',
      }, 'metadata'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => ExchangeBlockPageMetadata.fromJson({
        ...metadata,
        'availability': 'omitted',
      }, 'metadata'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Block metadata rejects contradictory retained shapes', () {
    final base = <String, Object?>{
      'kind': 'text',
      'availability': 'recorded',
      'originalSize': 0,
      'textBytes': 0,
      'argumentBytes': 0,
    };
    for (final fields in <Map<String, Object?>>[
      {'callId': 'hidden'},
      {'argumentBytes': 1},
      {'toolError': true},
      {'kind': 'reasoning', 'providerSource': '', 'providerKind': 'thinking'},
      {
        'kind': 'reasoning',
        'providerSource': 'p',
        'providerKind': 'k',
        'fingerprint': 'sha256:${'a' * 64}',
      },
      {
        'kind': 'provider_extension',
        'availability': 'omitted',
        'providerSource': 'p',
        'providerKind': 'k',
      },
      {'kind': 'tool_call', 'callId': '', 'toolName': 'f'},
      {'kind': 'tool_result', 'callId': 'c', 'toolName': 'f'},
    ]) {
      expect(
        () => ExchangeBlockPageMetadata.fromJson({
          ...base,
          ...fields,
        }, 'metadata'),
        throwsA(isA<ControlContractException>()),
        reason: '$fields',
      );
    }
  });

  test('Egress evidence keeps frozen account settings and proxy revisions', () {
    final decision = <String, Object?>{
      'authority': 'environment',
      'policyId': 'policy.work',
      'policyRevision': 2,
      'ruleId': 'route.work',
      'proxyId': 'profile.us',
      'proxyRevision': 3,
      'accountId': 'account.work',
      'accountSettingsRevision': 4,
    };
    final json = <String, Object?>{
      'sequence': 1,
      'id': 'egress.work',
      'purpose': 'provider_attempt',
      'payloadClass': 'client_semantic',
      'parent': {
        'kind': 'upstream_attempt',
        'id': 'attempt.work',
        'exchangeId': 'exchange.work',
      },
      'caller': 'core',
      'targetOrigin': 'https://provider.example',
      'decision': decision,
      'reusedTransport': false,
      'startedAt': '2026-09-28T00:00:00Z',
      'terminal': false,
      'bytesOut': 0,
      'bytesIn': 0,
    };
    final record = EgressAttemptRecord.fromJson(json, 'attempt');
    expect(record.policyRevision, 2);
    expect(record.proxyRevision, 3);
    expect(record.accountId, 'account.work');
    expect(record.accountSettingsRevision, 4);
    for (final missing in [
      'accountId',
      'accountSettingsRevision',
      'proxyRevision',
    ]) {
      final partial = {...decision}..remove(missing);
      expect(
        () => EgressAttemptRecord.fromJson({
          ...json,
          'decision': partial,
        }, 'attempt'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final passthrough = {...decision}
      ..remove('accountId')
      ..remove('accountSettingsRevision');
    expect(
      EgressAttemptRecord.fromJson({
        ...json,
        'decision': passthrough,
      }, 'attempt').accountId,
      isNull,
    );
  });

  test('Body-free diagnosis retains the structural upstream error code', () {
    final diagnosis = ExchangeDiagnosis.fromJson({
      'providerStatus': 200,
      'providerErrorCode': 'invalid_encrypted_content',
    }, 'diagnosis');
    expect(diagnosis.providerErrorCode, 'invalid_encrypted_content');
    expect(diagnosis.providerStatus, 200);
  });
  test('Root CA material is bound to its advertised SHA-256 identity', () {
    final der = Uint8List.fromList([0x30, 0x03, 0x01, 0x02, 0x03]);
    final fingerprint = crypto.sha256.convert(der).toString();
    final material = RootCAMaterial.fromJson({
      'rootRevision': 4,
      'fingerprint': fingerprint,
      'certificateDerBase64': base64.encode(der),
    }, 'rootMaterial');
    expect(material.rootRevision, 4);
    expect(material.fingerprint, fingerprint);
    expect(material.certificateDer, der);
    final mutableCopy = material.certificateDer;
    mutableCopy[0] = 0;
    expect(material.certificateDer, der);

    expect(
      () => RootCAMaterial.fromJson({
        'rootRevision': 4,
        'fingerprint': List.filled(64, 'a').join(),
        'certificateDerBase64': base64.encode(der),
      }, 'rootMaterial'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Root CA status keeps certificate presence separate from trust', () {
    final status = RootCAStatus.fromJson({
      'rootRevision': 3,
      'fingerprint': List.filled(64, 'a').join(),
      'algorithm': 'ecdsa-p256',
      'notBefore': '2026-01-01T00:00:00.000Z',
      'notAfter': '2036-01-01T00:00:00.000Z',
      'rootValid': true,
      'certificatePresence': 'present',
      'trustDecision': 'untrusted',
      'evidenceRevision': 'macos-fixture-v1',
      'observedAt': '2026-09-02T00:00:00.000Z',
      'available': true,
    }, 'rootCA');

    expect(status.rootRevision, 3);
    expect(status.certificatePresent, 'present');
    expect(status.trustDecision, 'untrusted');
    expect(status.installed, isFalse);
    expect(status.needsTrust, isTrue);
    expect(
      () => RootCAStatus.fromJson({
        'rootRevision': 1,
        'fingerprint': 'bad',
        'algorithm': 'ecdsa-p256',
        'notBefore': '2026-01-01T00:00:00.000Z',
        'notAfter': '2036-01-01T00:00:00.000Z',
        'rootValid': true,
        'certificatePresence': 'present',
        'trustDecision': 'trusted',
        'observedAt': '2026-09-02T00:00:00.000Z',
        'available': true,
      }, 'rootCA'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Root replacement result requires an absent old certificate', () {
    final status = {
      'rootRevision': 1,
      'fingerprint': List.filled(64, 'b').join(),
      'algorithm': 'ecdsa-p256',
      'notBefore': '2026-01-01T00:00:00.000Z',
      'notAfter': '2036-01-01T00:00:00.000Z',
      'rootValid': true,
      'certificatePresence': 'present',
      'trustDecision': 'untrusted',
      'observedAt': '2026-09-02T00:00:00.000Z',
      'available': true,
    };
    expect(
      () => RootCAActionResult.fromJson({
        'status': status,
        'resultStatus': 'applied',
        'reason': 'applied',
        'completed': false,
        'restartRequired': true,
      }, 'replace'),
      throwsA(isA<ControlContractException>()),
    );
    final absent = Map<String, Object?>.from(status)
      ..['certificatePresence'] = 'absent';
    final result = RootCAActionResult.fromJson({
      'status': absent,
      'resultStatus': 'applied',
      'reason': 'applied',
      'completed': false,
      'restartRequired': true,
    }, 'replace');
    expect(result.restartRequired, isTrue);
    expect(
      () => RootCAActionResult.fromJson({
        'status': absent,
        'resultStatus': 'needs_manual',
        'reason': 'postcondition_mismatch',
        'completed': false,
        'restartRequired': false,
      }, 'replace'),
      throwsA(isA<ControlContractException>()),
    );
  });

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
        'schema': 'vibermate-server-access-v1',
        'transport': 'http',
        'authentication': 'runtime_user_password',
        'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
        'targets': ['server.local:9666', '192.168.1.44:9666', '[fd00::8]:9666'],
        'tls': {'mode': 'http', 'state': 'disabled'},
      }, 'serverAccess');

      expect(access.transport, 'http');
      expect(access.encrypted, isFalse);
      expect(access.requiresRuntimeUserLogin, isTrue);
      expect(access.preferredTarget, 'server.local:9666');
      expect(access.tls.state, 'disabled');

      for (final invalid in <Map<String, Object?>>[
        {
          'schema': 'vibermate-server-access-v1',
          'transport': 'ftp',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['192.168.1.44:9666'],
          'tls': {'mode': 'http', 'state': 'disabled'},
        },
        {
          'schema': 'vibermate-server-access-v1',
          'transport': 'https',
          'authentication': 'anonymous',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['192.168.1.44:9666'],
          'tls': {'mode': 'automatic_tls', 'state': 'ready'},
        },
        {
          'schema': 'vibermate-server-access-v1',
          'transport': 'https',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'per_run_approval',
          'targets': ['192.168.1.44:9666'],
          'tls': {'mode': 'automatic_tls', 'state': 'ready'},
        },
        {
          'schema': 'vibermate-server-access-v1',
          'transport': 'https',
          'authentication': 'runtime_user_password',
          'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
          'targets': ['Server.Local:9666'],
          'tls': {'mode': 'automatic_tls', 'state': 'ready'},
        },
      ]) {
        expect(
          () => RuntimeServerAccess.fromJson(invalid, 'serverAccess'),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );

  test('Runtime Server access reports automatic TLS lifecycle', () {
    final access = RuntimeServerAccess.fromJson({
      'schema': 'vibermate-server-access-v1',
      'transport': 'https',
      'authentication': 'runtime_user_password',
      'sessionPolicy': 'reusable_until_logout_disable_or_expiry',
      'targets': ['runtime.example.com:443'],
      'tls': {
        'mode': 'automatic_tls',
        'state': 'renewing',
        'serverName': 'runtime.example.com',
        'challenge': 'http_01',
        'fingerprint': List.filled(64, 'a').join(),
        'issuer': 'Example CA',
        'notBefore': '2026-09-01T00:00:00Z',
        'notAfter': '2026-12-01T00:00:00Z',
      },
    }, 'serverAccess');

    expect(access.tls.mode, 'automatic_tls');
    expect(access.tls.state, 'renewing');
    expect(access.tls.serverName, 'runtime.example.com');
  });

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
    'Runtime summary preserves partial token knowledge and bounded shape',
    () {
      final report = RuntimeUsageReport.fromJson(
        runtimeUsagePayload(),
        'runtimeUsage',
      );

      final total = report.total!;
      expect(report.groups, isEmpty);
      expect(report.period.from, '2026-07-27');
      expect(report.period.until, '2026-08-26');
      expect(report.period.timeZone, 'Asia/Singapore');
      expect(report.days.single.date, '2026-08-24');
      expect(report.cost!.nanoUsd, 12500000);
      expect(report.cost!.partial, isTrue);
      expect(report.cost!.unpricedCalls, 1);
      expect(report.pricing.state, 'ready');
      expect(total.failed, 1);
      expect(total.tokens.inputUncached.tokens, 42);
      expect(total.tokens.inputUncached.knownCalls, 1);
      expect(total.tokens.inputUncached.unknownCalls, 1);
    },
  );

  test(
    'Usage cost rejects inconsistent coverage, negative money and unsupported basis',
    () {
      for (final edit in <void Function(Map<String, Object?>)>[
        (value) => value['nanoUsd'] = -1,
        (value) => value['nanoUsd'] = 9007199254740992,
        (value) => value['partialCalls'] = 2,
        (value) => value['unpricedCalls'] = 3,
      ]) {
        final cost = costUsagePayload();
        edit(cost);
        expect(
          () => RuntimeCostEstimate.fromJson(cost, 'cost', 2),
          throwsA(isA<ControlContractException>()),
        );
      }
      final payload = runtimeUsagePayload();
      (payload['pricing']! as Map<String, Object?>)['currency'] = 'EUR';
      expect(
        () => RuntimeUsageReport.fromJson(payload, 'usage'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );

  test('Usage page rejects mixed scopes, excess rows and old nested shape', () {
    Map<String, Object?> page() => runtimeUsagePayload()
      ..['dimension'] = 'model'
      ..['total'] = null
      ..['days'] = <Object?>[]
      ..['groups'] = [
        usageGroupPayload('relay:model/custom')..['dimension'] = 'model',
      ];
    final report = RuntimeUsageReport.fromJson(page(), 'usage');
    expect(report.groups.single.id, 'relay:model/custom');
    expect(report.total, isNull);
    for (final edit in <void Function(Map<String, Object?>)>[
      (value) => value['total'] = usageGroupPayload('all'),
      (value) => value['dimension'] = 'account',
      (value) => value['snapshot'] = 'bad',
      (value) => value['groups'] = List.filled(
        51,
        usageGroupPayload('model')..['dimension'] = 'model',
      ),
      (value) => value['filters'] = [
        {'dimension': 'caller', 'id': 'alice'},
        {'dimension': 'caller', 'id': 'bob'},
      ],
      (value) => value['users'] = <Object?>[],
    ]) {
      final value = page();
      edit(value);
      expect(
        () => RuntimeUsageReport.fromJson(value, 'usage'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final nested = usageGroupPayload('profile')
      ..['children'] = [usageGroupPayload('model')];
    expect(
      () => RuntimeUsageGroup.fromJson(nested, 'group'),
      throwsA(isA<ControlContractException>()),
    );
    final inconsistent = usageGroupPayload('profile')
      ..['agentApiCalls'] = 3
      ..['succeeded'] = 2;
    expect(
      () => RuntimeUsageGroup.fromJson(inconsistent, 'group'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test(
    'Usage query preserves empty unknown filters and rejects user overrides',
    () {
      final query = RuntimeUsageQuery(
        from: '2026-08-01',
        until: '2026-08-26',
        timeZone: 'UTC',
        groupBy: 'model',
        filters: const {'project': '', 'caller': 'alice'},
        snapshot: List.filled(64, 'a').join(),
        cursor: 'next',
      );
      expect(query.toQueryParameters(), containsPair('filter.project', ''));
      expect(query.toQueryParameters(), containsPair('filter.caller', 'alice'));
      expect(query.toQueryParameters(), containsPair('limit', '50'));
      for (final filters in [
        {'userId': 'alice'},
        {'session': 'session-only'},
      ]) {
        expect(
          () => RuntimeUsageQuery(
            from: query.from,
            until: query.until,
            timeZone: query.timeZone,
            filters: filters,
          ).toQueryParameters(),
          throwsA(isA<ControlContractException>()),
        );
      }
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
        'activityAt': '2026-08-10T09:00:45.000Z',
        'managedRun': {
          'executableLabel': 'claude',
          'cwd': '/Users/mira/Code/vibermate',
          'canonicalExecutablePath': '/usr/local/bin/claude',
          'localUserLabel': 'mira',
          'homeDirectory': '/Users/mira',
          'operatingSystem': 'darwin',
          'operatingSystemVersion': '26.0',
          'architecture': 'arm64',
          'timeZone': 'Asia/Singapore',
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
      expect(record.activityAt, DateTime.utc(2026, 8, 10, 9, 0, 45));
      expect(record.managedRun!.machineId, machineId);
      expect(record.managedRun!.workspaceId, workspaceId);
      expect(record.managedRun!.workspaceEvidence, 'registered_companion');
      expect(record.managedRun!.runtimeUserId, 'user.remote');
      expect(record.managedRun!.runtimeUsername, 'alice');
      expect(record.managedRun!.homeDirectory, '/Users/mira');
      expect(record.managedRun!.operatingSystem, 'darwin');
      expect(record.managedRun!.operatingSystemVersion, '26.0');
      expect(record.managedRun!.architecture, 'arm64');
      expect(record.managedRun!.timeZone, 'Asia/Singapore');
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
      expect(
        () => ManagedRunSummary.fromJson({
          'executableLabel': 'claude',
          'cwd': '/Users/mira/Code/vibermate',
          'canonicalExecutablePath': '/usr/local/bin/claude',
          'homeDirectory': 'Users/mira',
          'recognition': 'unknown',
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
        'note': '',
        'noteRevision': 0,
        'settingsRevision': 1,
        'automaticRefresh': false,
        'supportsAutomaticRefresh': false,
        'egressProfile': null,
        'credentialOrigin': 'https://api.anthropic.com',
        'linkedEndpointIds': ['target.test'],
        'associationRevision': 1,
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

  test('Codex OAuth Account exposes only safe identity and refresh state', () {
    final account = ProviderAccount.fromJson({
      'id': 'account.codex.work',
      'displayName': 'Codex Work',
      'note': '',
      'noteRevision': 0,
      'settingsRevision': 1,
      'automaticRefresh': false,
      'supportsAutomaticRefresh': false,
      'egressProfile': null,
      'credentialOrigin': 'https://chatgpt.com',
      'linkedEndpointIds': ['target.codex.official'],
      'associationRevision': 1,
      'kind': 'codex_oauth',
      'realmId': 'openai.chatgpt',
      'state': 'active',
      'revision': 1,
      'credentialState': 'ready',
      'credentialEpoch': 4,
      'setHeaderNames': <String>[],
      'deleteHeaderNames': <String>[],
      'codexOAuth': {
        'chatgptAccountId': 'workspace-42',
        'email': 'engineer@example.com',
        'userId': 'user-42',
        'planType': 'team',
        'fedRamp': false,
        'expiresAt': '2026-09-21T12:00:00.000Z',
        'lastRefresh': '2026-09-21T11:00:00.000Z',
        'state': 'ready',
      },
    }, 'providerAccount');

    expect(account.codexOAuth?.chatgptAccountId, 'workspace-42');
    expect(account.codexOAuth?.email, 'engineer@example.com');
    expect(account.codexOAuth?.planType, 'team');
    expect(account.codexOAuth?.state, 'ready');
    expect(account.codexOAuth?.expiresAt, DateTime.utc(2026, 9, 21, 12));
    expect(
      () => ProviderAccount.fromJson({
        'id': 'account.codex.bad',
        'displayName': 'Codex Bad',
        'note': '',
        'noteRevision': 0,
        'settingsRevision': 1,
        'automaticRefresh': false,
        'supportsAutomaticRefresh': false,
        'egressProfile': null,
        'credentialOrigin': 'https://chatgpt.com',
        'linkedEndpointIds': ['target.codex.official'],
        'associationRevision': 1,
        'kind': 'codex_oauth',
        'realmId': 'openai.chatgpt',
        'state': 'active',
        'revision': 1,
        'credentialState': 'ready',
        'credentialEpoch': 1,
        'setHeaderNames': <String>[],
        'deleteHeaderNames': <String>[],
        'codexOAuth': {
          'chatgptAccountId': 'workspace-42',
          'fedRamp': false,
          'lastRefresh': '2026-09-21T11:00:00.000Z',
          'state': 'ready',
          'refreshToken': 'must-not-cross-response-boundary',
        },
      }, 'providerAccount'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('credential-missing Account has an explicit zero epoch', () {
    final account = ProviderAccount.fromJson({
      'id': 'account.missing',
      'displayName': 'Missing',
      'note': '',
      'noteRevision': 0,
      'settingsRevision': 1,
      'automaticRefresh': false,
      'supportsAutomaticRefresh': false,
      'egressProfile': null,
      'credentialOrigin': 'https://api.anthropic.com',
      'linkedEndpointIds': ['target.test'],
      'associationRevision': 1,
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
        'note': '',
        'noteRevision': 0,
        'settingsRevision': 1,
        'automaticRefresh': false,
        'supportsAutomaticRefresh': false,
        'egressProfile': null,
        'credentialOrigin': 'https://api.anthropic.com',
        'linkedEndpointIds': ['target.test'],
        'associationRevision': 1,
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
          'note': '',
          'noteRevision': 0,
          'settingsRevision': 1,
          'automaticRefresh': false,
          'supportsAutomaticRefresh': false,
          'egressProfile': null,
          'credentialOrigin': 'https://api.anthropic.com',
          'linkedEndpointIds': ['target.test'],
          'associationRevision': 1,
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
    final json = <String, Object?>{
      'generation': 'instance-test',
      'ready': true,
      'apiVersion': 'v1',
      'productBuild': 'test-build',
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
    };
    final status = RuntimeStatus.fromJson(
      json,
      expectedInstanceId: 'instance-test',
    );

    expect(status.offlineHold.revision, 4);
    expect(status.offlineHold.activeByKind, {'provider': 1});
    expect(status.productBuild, 'test-build');
    expect(status.schemaRevision, 1);

    final runtime = Map<String, Object?>.from(json['runtime']! as Map);
    json['runtime'] = runtime;
    final warning = <String, Object?>{
      'operation': 'usage',
      'reason': 'write_failed',
      'at': '2026-08-11T00:00:01.000Z',
    };
    runtime['recordingFailure'] = warning;
    final recorded = RuntimeStatus.fromJson(
      json,
      expectedInstanceId: 'instance-test',
    );
    expect(recorded.healthy, isTrue);
    expect(recorded.recordingFailure?.operation, 'usage');
    expect(
      recorded.recordingFailure,
      RuntimePersistenceFailure.fromJson(warning),
    );
    for (final malformed in [
      {...warning, 'operation': 'egress_complete'},
      {...warning, 'operation': 'future_operation'},
      {...warning, 'reason': 'raw secret error'},
      {...warning, 'at': 'bad date'},
      {...warning, 'message': 'secret account'},
      null,
    ]) {
      runtime['recordingFailure'] = malformed;
      expect(
        () => RuntimeStatus.fromJson(json, expectedInstanceId: 'instance-test'),
        throwsA(isA<ControlContractException>()),
      );
    }
    runtime['recordingFailure'] = warning;
    runtime['storageFailure'] = {
      ...warning,
      'operation': 'egress_complete',
      'reason': 'timeout',
    };
    expect(
      () => RuntimeStatus.fromJson(json, expectedInstanceId: 'instance-test'),
      throwsA(isA<ControlContractException>()),
    );
    runtime['storage'] = 'unavailable';
    runtime['state'] = 'degraded';
    json['ready'] = false;
    json['statusKey'] = 'runtime.state.degraded';
    final failed = RuntimeStatus.fromJson(
      json,
      expectedInstanceId: 'instance-test',
    );
    expect(failed.healthy, isFalse);
    expect(failed.storageFailure?.reason, 'timeout');
    expect(failed.recordingFailure, recorded.recordingFailure);
    runtime['storageFailure'] = warning;
    expect(
      () => RuntimeStatus.fromJson(json, expectedInstanceId: 'instance-test'),
      throwsA(isA<ControlContractException>()),
    );
    runtime.remove('storageFailure');

    final unsafe = jsonDecode(jsonEncode(json)) as Map<String, dynamic>;
    unsafe['productBuild'] = 'unsafe\nbuild';
    expect(
      () => RuntimeStatus.fromJson(unsafe, expectedInstanceId: 'instance-test'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test('Runtime status accepts the public Runtime Server host kind', () {
    final status = RuntimeStatus.fromJson({
      'generation': 'instance-server',
      'ready': true,
      'apiVersion': 'v1',
      'productBuild': 'test-build',
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
    'Manual Capture context accepts an exact proxy origin without a path',
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
            'proxyAddress': 'http://localhost:43123/path',
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

  test(
    'Web Manual Capture accepts a canonical server proxy and downloadable Root',
    () {
      final context = ManualCaptureContext.fromJson(
        {
          'confirmationToken': 'ctx_${List.filled(43, 'A').join()}',
          'proxyAddress': 'https://vibermate.home.arpa:9666',
          'environmentId': 'work',
          'environmentRevision': 7,
          'environmentDigest': List.filled(64, 'a').join(),
          'launchAuthorityDigest': List.filled(64, 'b').join(),
          'protectedAuthorities': ['api.anthropic.com'],
          'managedCredentialAuthorities': <String>[],
          'defaultTemporarySeconds': 3600,
          'maxTemporarySeconds': 86400,
          'root': {
            'kind': 'server_download',
            'derSha256': List.filled(64, 'c').join(),
            'fingerprint': 'CC:CC',
          },
        },
        'manualCaptureContext',
        expectedEnvironmentId: 'work',
      );
      expect(context.proxyAddress, 'https://vibermate.home.arpa:9666');
      expect(context.root?.kind, 'server_download');
      expect(context.root?.pemPath, isNull);
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

    final search = EvidenceSearchPage.fromJson({
      'items': [
        {
          'activity': json,
          'context': {
            'workspaceId': 'workspace_test',
            'workspaceLabel': 'Test workspace',
            'captureLabel': 'Claude Code',
            'requestedModel': 'claude-sonnet-4-5',
            'effectiveModel': 'claude-sonnet-4-5',
            'reportedModel': 'claude-sonnet-4-5-20260925',
            'toolNames': ['workspace.read'],
            'contentAvailable': true,
          },
          'matches': ['workspace', 'tool'],
        },
      ],
      'nextCursor': 'AQAAAAAAAAAqYWJjZGVmZ2hpamtsbW5vcA',
    }, 'search');
    expect(search.items.single.activity.id, 'exchange-test');
    expect(search.items.single.context.toolNames, ['workspace.read']);
    expect(search.items.single.matches, ['workspace', 'tool']);
    expect(search.nextCursor, isNotEmpty);

    for (final preview in [
      {
        'kind': 'reasoning',
        'text': 'private chain of thought',
        'truncated': false,
      },
      {
        'kind': 'text',
        'text': '${List.filled(179, 'x').join()} ',
        'truncated': true,
      },
      {'kind': 'text', 'text': 'a\nline', 'truncated': false},
      {'kind': 'text', 'text': List.filled(181, 'x').join(), 'truncated': true},
      {'kind': 'text', 'text': '', 'truncated': false},
      {'kind': 'text', 'text': 'missing flag'},
      'invalid preview object',
    ]) {
      final withPreview = {...json, 'requestPreview': preview};
      final page = ActivityPage.fromJson({
        'items': [json, withPreview],
      }, 'activities');
      expect(page.items.length, 2);
      expect(page.items.first.requestPreview?.text, 'workspace.read');
      expect(page.items.last.requestPreview, isNull);
      expect(page.items.last.accountId, 'anthropic-work');
      expect(page.items.last.status, 'succeeded');
      // Malformed authority still fails, even beside a discarded preview.
      expect(
        () => ActivityRecord.fromJson({
          ...withPreview,
          'status': 'bogus',
        }, 'activity'),
        throwsA(isA<ControlContractException>()),
      );
    }

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
    final original = RouteAccountPolicy.fromJson({
      'revision': 1,
      'mode': 'original',
      'accounts': <Object>[],
    }, 'policy');
    expect(original.mode, 'original');
    expect(original.fixedAccountId, isEmpty);
    expect(RouteAccountPolicy.fromJson(original.toJson(), 'policy'), original);
    expect(
      () => RouteAccountPolicy.fromJson({
        ...original.toJson(),
        'fixedAccountId': 'account.other',
      }, 'policy'),
      throwsA(isA<ControlContractException>()),
    );
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

  test('Decoded reading view keeps wire bytes and is cleared with them', () {
    final json = _rawRevealJson();
    (json['envelope']! as Map<String, Object?>)['contentEncoding'] = 'zstd';
    json['decodedBody'] = {
      'state': 'decoded',
      'bodyBase64': base64.encode(utf8.encode('解压后的正文')),
    };
    final reveal = RevealedRawEvidence.fromJson(
      json,
      'rawReveal',
      expectedEnvelopeId: 'raw-test',
    );
    expect(utf8.decode(reveal.body), 'hello');
    expect(utf8.decode(reveal.decodedBody!.body), '解压后的正文');
    expect(reveal.envelope.bodyBytes, 5);
    expect(reveal.frames.single.length, 5);
    reveal.clearBody();
    expect(reveal.body.every((byte) => byte == 0), isTrue);
    expect(reveal.decodedBody!.body.every((byte) => byte == 0), isTrue);
  });

  test(
    'Decoded view rejects contradictory states and retains legacy support',
    () {
      expect(
        RevealedRawEvidence.fromJson(
          _rawRevealJson(),
          'rawReveal',
          expectedEnvelopeId: 'raw-test',
        ).decodedBody,
        isNull,
      );
      for (final state in [
        'incomplete',
        'invalid_compression',
        'size_limit',
        'unsupported_encoding',
      ]) {
        expect(
          RawDecodedBody.fromJson({
            'state': state,
            'bodyBase64': '',
          }, 'view').body,
          isEmpty,
        );
        expect(
          () => RawDecodedBody.fromJson({
            'state': state,
            'bodyBase64': 'aGVsbG8=',
          }, 'view'),
          throwsA(isA<ControlContractException>()),
        );
      }
      for (final invalid in [
        {'state': 'unknown', 'bodyBase64': ''},
        {'state': 'decoded', 'bodyBase64': '!'},
        {'state': 'decoded', 'bodyBase64': List.filled(5592412, 'A').join()},
      ]) {
        expect(
          () => RawDecodedBody.fromJson(invalid, 'view'),
          throwsA(isA<ControlContractException>()),
        );
      }
      final contradictory = _rawRevealJson();
      contradictory['decodedBody'] = {'state': 'decoded', 'bodyBase64': ''};
      expect(
        () => RevealedRawEvidence.fromJson(
          contradictory,
          'rawReveal',
          expectedEnvelopeId: 'raw-test',
        ),
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

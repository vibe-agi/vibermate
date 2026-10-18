import 'dart:async';

import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/native_tool_models.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final recovery in ['poll', 'explicit refresh']) {
    testWidgets(
      'startup inventory recovery via $recovery keeps five-minute quota scheduling',
      (tester) async {
        final fixture = await _Fixture.create();
        fixture.api.inventoryFailures = 1;
        await fixture.controller.initialize();
        expect(fixture.controller.data, isNull);
        expect(fixture.controller.errorMessage, isNotNull);
        expect(fixture.api.calls, isEmpty);

        if (recovery == 'poll') {
          await fixture.advance(tester, const Duration(seconds: 5));
        } else {
          await fixture.controller.refresh();
          await tester.pump();
        }
        expect(fixture.controller.data, isNotNull);
        expect(fixture.api.calls, ['a']);
        expect(
          fixture.controller.providerAccountQuota(fixture.account),
          isNotNull,
        );
        await fixture.advance(tester, const Duration(minutes: 4, seconds: 59));
        expect(fixture.api.calls, ['a']);
        await fixture.advance(tester, const Duration(seconds: 1));
        expect(fixture.api.calls, ['a', 'a']);
        expect(fixture.api.historyCalls, 0);
        expect(fixture.api.mutations, 0);
        fixture.controller.dispose();
      },
    );
  }

  testWidgets(
    'successful inventory schedules quota despite capture detail failure',
    (tester) async {
      final fixture = await _Fixture.create(seedCaptures: true);
      fixture.api.captureDetailFailure = true;
      await fixture.controller.initialize();
      await tester.pump();
      expect(fixture.controller.data, isNotNull);
      expect(fixture.api.captureAssignmentCalls, 1);
      expect(fixture.controller.captureDetailError, isNotNull);
      expect(fixture.controller.errorMessage, isNotNull);
      expect(fixture.controller.nativeApprovalError, isNull);
      expect(fixture.api.calls, ['a']);
      expect(
        fixture.controller.providerAccountQuota(fixture.account),
        isNotNull,
      );
      fixture.controller.didChangeAppLifecycleState(AppLifecycleState.hidden);
      await fixture.advance(tester, const Duration(minutes: 5));
      expect(fixture.api.calls, ['a', 'a']);
      expect(fixture.api.historyCalls, 0);
      expect(fixture.api.mutations, 0);
      fixture.controller.dispose();
    },
  );

  testWidgets('initial missing quotas load without blocking initialize', (
    tester,
  ) async {
    final fixture = await _Fixture.create();
    fixture.api.hold = true;
    await fixture.controller.initialize();
    expect(fixture.controller.errorMessage, isNull);
    expect(fixture.api.calls, ['a']);
    expect(
      fixture.controller.providerAccountQuotaLoading(fixture.account),
      isTrue,
    );
    fixture.api.completeAll();
    await tester.pump();
    expect(fixture.controller.providerAccountQuota(fixture.account), isNotNull);
    fixture.controller.dispose();
  });

  testWidgets('one schedule reads once at five minutes, never before', (
    tester,
  ) async {
    final fixture = await _Fixture.create(count: 2);
    await fixture.controller.initialize();
    await tester.pump();
    await fixture.controller.initialize();
    expect(fixture.api.calls, ['a', 'b']);
    await fixture.advance(tester, const Duration(minutes: 4, seconds: 59));
    expect(fixture.api.calls, ['a', 'b']);
    await fixture.advance(tester, const Duration(seconds: 1));
    expect(fixture.api.calls, ['a', 'b', 'a', 'b']);
    expect(fixture.api.historyCalls, 0);
    expect(fixture.api.mutations, 0);
    expect(fixture.controller.errorMessage, isNull);
    fixture.controller.dispose();
  });

  testWidgets('fresh manual read suppresses the old periodic deadline', (
    tester,
  ) async {
    final fixture = await _Fixture.create();
    await fixture.controller.initialize();
    await tester.pump();
    await fixture.advance(tester, const Duration(minutes: 4));
    await fixture.controller.refreshProviderAccountQuota(fixture.account);
    expect(fixture.api.calls, ['a', 'a']);
    await fixture.advance(tester, const Duration(minutes: 1));
    expect(fixture.api.calls, ['a', 'a']);
    await fixture.advance(tester, const Duration(minutes: 5));
    expect(fixture.api.calls, ['a', 'a', 'a']);
    fixture.controller.dispose();
  });

  testWidgets('manual periodic and lazy readers share the pending result', (
    tester,
  ) async {
    final fixture = await _Fixture.create();
    await fixture.controller.initialize();
    await tester.pump();
    await fixture.advance(tester, const Duration(minutes: 4));
    fixture.api.hold = true;
    final manual = fixture.controller.refreshProviderAccountQuota(
      fixture.account,
    );
    final joiner = fixture.controller.refreshProviderAccountQuota(
      fixture.account,
    );
    var lazySettled = false;
    final lazy = fixture.controller
        .ensureProviderAccountQuotas([fixture.account], refresh: true)
        .then((_) => lazySettled = true);
    await fixture.advance(tester, const Duration(minutes: 1));
    expect(fixture.api.calls, ['a', 'a']);
    expect(lazySettled, isFalse);
    final fresh = fixture.api.facts('a', percent: 42);
    fixture.api.pending.single.complete(fresh);
    expect(await manual, same(fresh));
    expect(await joiner, same(fresh));
    await lazy;
    expect(
      fixture.controller.providerAccountQuota(fixture.account),
      same(fresh),
    );
    fixture.controller.dispose();
  });

  testWidgets(
    'failed automatic read preserves facts and retries next interval',
    (tester) async {
      final fixture = await _Fixture.create();
      await fixture.controller.initialize();
      await tester.pump();
      final old = fixture.controller.providerAccountQuota(fixture.account);
      fixture.api.fail = true;
      await fixture.advance(tester, const Duration(minutes: 5));
      expect(fixture.api.calls, ['a', 'a']);
      expect(
        fixture.controller.providerAccountQuota(fixture.account),
        same(old),
      );
      expect(
        fixture.controller.providerAccountQuotaFailed(fixture.account),
        isTrue,
      );
      fixture.api.fail = false;
      await fixture.advance(tester, const Duration(minutes: 4, seconds: 59));
      expect(fixture.api.calls, ['a', 'a']);
      await fixture.advance(tester, const Duration(seconds: 1));
      expect(fixture.api.calls, ['a', 'a', 'a']);
      expect(
        fixture.controller.providerAccountQuotaFailed(fixture.account),
        isFalse,
      );
      fixture.controller.dispose();
    },
  );

  for (final change in ['epoch', 'settings', 'origin', 'invalidate']) {
    testWidgets('$change rejects held completion and its freshness time', (
      tester,
    ) async {
      final fixture = await _Fixture.create();
      await fixture.controller.initialize();
      await tester.pump();
      fixture.api.hold = true;
      final oldAccount = fixture.account;
      final held = fixture.controller.refreshProviderAccountQuota(oldAccount);
      final oldFacts = fixture.api.facts('a');
      if (change == 'invalidate') {
        fixture.controller.invalidateProviderAccountQuota(oldAccount);
      } else {
        fixture.api.accounts = [
          _account(
            'a',
            epoch: change == 'epoch' ? 2 : 1,
            settings: change == 'settings' ? 2 : 1,
            origin: change == 'origin'
                ? 'https://chatgpt.com/backend-api/codex'
                : 'https://chatgpt.com',
          ),
        ];
        fixture.controller.data = await fixture.api.loadDashboard();
      }
      fixture.api.pending.single.complete(oldFacts);
      expect(await held, isNull);
      expect(fixture.controller.providerAccountQuota(fixture.account), isNull);
      fixture.api.hold = false;
      await fixture.advance(tester, const Duration(minutes: 5));
      expect(fixture.api.calls, ['a', 'a', 'a']);
      expect(
        fixture.controller.providerAccountQuota(fixture.account),
        isNotNull,
      );
      fixture.controller.dispose();
    });
  }

  testWidgets('dispose settles held joiners and never starts a later batch', (
    tester,
  ) async {
    final fixture = await _Fixture.create(count: 6);
    fixture.api.hold = true;
    await fixture.controller.initialize();
    expect(fixture.api.calls, ['a', 'b', 'c', 'd']);
    final joined = fixture.controller.refreshProviderAccountQuota(
      fixture.account,
    );
    fixture.controller.dispose();
    expect(await joined, isNull);
    fixture.api.completeAll();
    await tester.pump();
    await fixture.advance(tester, const Duration(minutes: 10));
    expect(fixture.api.calls, ['a', 'b', 'c', 'd']);
    expect(
      await fixture.controller.refreshProviderAccountQuota(fixture.account),
      isNull,
    );
  });

  testWidgets('hidden ticks continue and resume performs one due catch-up', (
    tester,
  ) async {
    final fixture = await _Fixture.create();
    await fixture.controller.initialize();
    await tester.pump();
    fixture.controller.didChangeAppLifecycleState(AppLifecycleState.hidden);
    await fixture.advance(tester, const Duration(minutes: 5));
    expect(fixture.api.calls, ['a', 'a']);
    // A suspended process can advance wall time without executing timers.
    fixture.now = fixture.now.add(const Duration(hours: 1));
    fixture.controller.didChangeAppLifecycleState(AppLifecycleState.resumed);
    await tester.pump();
    expect(fixture.api.calls, ['a', 'a', 'a']);
    fixture.controller.didChangeAppLifecycleState(AppLifecycleState.resumed);
    await tester.pump();
    expect(fixture.api.calls, ['a', 'a', 'a']);
    fixture.controller.dispose();
  });

  testWidgets('unsupported disabled and Web member never auto-read quota', (
    tester,
  ) async {
    final fixture = await _Fixture.create();
    fixture.api.accounts = [
      _account('other', origin: 'https://api.openai.com'),
      _account('disabled', ready: false),
    ];
    await fixture.controller.initialize();
    await fixture.advance(tester, const Duration(minutes: 5));
    expect(fixture.api.calls, isEmpty);
    fixture.controller.dispose();
    final member = await _Fixture.create(member: true);
    await member.controller.initialize();
    await member.advance(tester, const Duration(minutes: 5));
    expect(member.api.calls, isEmpty);
    member.controller.dispose();
  });
}

ProviderAccount _account(
  String id, {
  int epoch = 1,
  int settings = 1,
  String origin = 'https://chatgpt.com',
  bool ready = true,
}) => ProviderAccount(
  id: id,
  displayName: id,
  credentialOrigin: origin,
  linkedEndpointIds: const [],
  kind: 'bearer_token',
  realmId: 'realm.chatgpt',
  state: ready ? 'active' : 'disabled',
  revision: 1,
  credentialState: ready ? 'ready' : 'disabled',
  credentialEpoch: epoch,
  settingsRevision: settings,
  setHeaderNames: const [],
  deleteHeaderNames: const [],
);

final class _Fixture {
  _Fixture(this.api, {required bool member}) {
    controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      terminalManagement: false,
      closeRuntime: () async {},
      clock: () => now,
      webPrincipal: member
          ? const RuntimeWebPrincipal(
              id: 'member',
              username: 'fixture',
              role: RuntimeWebRole.member,
            )
          : null,
    );
    addTearDown(controller.dispose);
  }
  final _QuotaApi api;
  late final WorkbenchController controller;
  var now = DateTime.utc(2026, 10, 11);
  ProviderAccount get account => api.accounts.first;
  static Future<_Fixture> create({
    int count = 1,
    bool member = false,
    bool seedCaptures = false,
  }) async {
    final preview = PreviewControlApi(seedCaptures: seedCaptures);
    final base = await preview.loadDashboard();
    await preview.close();
    return _Fixture(
      _QuotaApi(
        base,
        List.generate(count, (i) => _account(String.fromCharCode(97 + i))),
      ),
      member: member,
    );
  }

  Future<void> advance(WidgetTester tester, Duration duration) async {
    now = now.add(duration);
    await tester.pump(duration);
    await tester.pump();
  }
}

final class _QuotaApi extends Fake implements ControlApi {
  _QuotaApi(this.base, this.accounts);
  final DashboardData base;
  List<ProviderAccount> accounts;
  final calls = <String>[];
  final pending = <Completer<AccountFacts>>[];
  bool hold = false, fail = false;
  bool captureDetailFailure = false;
  int inventoryFailures = 0, captureAssignmentCalls = 0;
  int historyCalls = 0, mutations = 0;

  @override
  Future<DashboardData> loadDashboard() async {
    if (inventoryFailures > 0) {
      inventoryFailures--;
      throw StateError('synthetic startup inventory failure');
    }
    return DashboardData(
      status: base.status,
      captures: captureDetailFailure
          ? base.captures.take(1).toList()
          : const [],
      captureNextCursor: '',
      environments: const [],
      endpoints: base.endpoints,
      accounts: accounts,
    );
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) async {
    captureAssignmentCalls++;
    throw StateError('synthetic capture detail failure');
  }

  @override
  Future<ExchangeSummary> activitySummary(ActivitySummaryScope scope) async {
    return ExchangeSummary(
      scope: scope,
      generatedAt: DateTime.utc(2026, 10, 11),
      requests: 0,
      succeeded: 0,
      failed: 0,
      canceled: 0,
      pending: 0,
      firstObservedAt: null,
      lastObservedAt: null,
      failures: const {},
      otherFailures: 0,
      contentBodiesAvailable: true,
    );
  }

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) async => const ConversationPage(items: [], nextCursor: null);

  @override
  Future<NativeApprovalPage> nativeApprovals(
    String captureKey, {
    String cursor = '',
    int limit = 50,
  }) async => NativeApprovalPage(const [], '');
  @override
  Future<List<ApprovalRecord>> pendingApprovals() async => const [];

  AccountFacts facts(String id, {int percent = 20}) {
    final account = accounts.singleWhere((a) => a.id == id);
    return AccountFacts.fromJson({
      'accountId': id,
      'credentialEpoch': account.credentialEpoch,
      'origin': account.credentialOrigin,
      'adapterId': 'chatgpt-codex',
      'adapterRevision': 1,
      // Intentionally older than the local clock: remote time is not freshness.
      'observedAt': '2020-01-01T00:00:00Z',
      'state': 'known',
      'limits': [
        {
          'id': 'codex',
          'primary': {
            'windowSeconds': 18000,
            'usedPercent': percent,
            'resetAfterSeconds': 60,
            'resetAt': 1791676800,
          },
        },
      ],
      'credits': {'hasCredits': true, 'unlimited': false, 'balance': '123.456'},
    });
  }

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) {
    if (history) historyCalls++;
    calls.add(id);
    if (fail) return Future.error(StateError('synthetic upstream failure'));
    if (!hold) return Future.value(facts(id));
    final completer = Completer<AccountFacts>();
    pending.add(completer);
    return completer.future;
  }

  void completeAll() {
    for (var i = 0; i < pending.length; i++) {
      if (!pending[i].isCompleted) pending[i].complete(facts(calls[i]));
    }
  }

  @override
  Future<ProviderAccount> setProviderAccountSettings(
    ProviderAccount account, {
    required EgressProfileRevision? egressProfile,
    required bool automaticRefresh,
    String? cyberAccessProgram,
  }) {
    mutations++;
    throw StateError('unexpected settings mutation');
  }

  @override
  Future<ProviderAccount> refreshProviderAccountCredential(
    ProviderAccount account,
  ) {
    mutations++;
    throw StateError('unexpected credential mutation');
  }

  @override
  Future<AccountResetRedemption> redeemAccountResetCredit(
    ProviderAccount account,
    String creditId,
  ) {
    mutations++;
    throw StateError('unexpected redemption');
  }
}

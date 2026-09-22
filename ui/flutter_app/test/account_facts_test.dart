import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:flutter/material.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_account_facts.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final width in [390.0, 1180.0]) {
    testWidgets(
      'account quota is explicit and remains separate from history at $width',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(width, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(seedCaptures: false);
        final account = await api.createProviderAccount(
          id: 'account.facts',
          displayName: 'Managed B',
          upstreamEndpointId: 'target.codex.official',
          kind: 'bearer_token',
          secret: 'synthetic-secret',
          unlinked: true,
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: SingleChildScrollView(
                child: ProviderAccountFactsPanel(
                  account: account,
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
                ),
              ),
            ),
          ),
        );
        expect(find.textContaining('25%'), findsNothing);
        await tester.tap(find.byKey(const Key('account-quota-account.facts')));
        await tester.pumpAndSettle();
        expect(find.textContaining('25%'), findsOneWidget);
        expect(find.textContaining('1,200'), findsNothing);
        await tester.tap(
          find.byKey(const Key('account-history-account.facts')),
        );
        await tester.pumpAndSettle();
        expect(find.text('1.20K'), findsOneWidget);
        expect(find.text('1,200 tokens'), findsOneWidget);
        final quota = tester.getRect(
          find.byKey(const Key('account-facts-quota')),
        );
        final history = tester.getRect(
          find.byKey(const Key('account-facts-history')),
        );
        if (width < 740) {
          expect(history.top, greaterThan(quota.bottom));
        } else {
          expect(history.left, greaterThan(quota.right));
          expect(history.top, quota.top);
          expect(quota.width + history.width, lessThanOrEqualTo(960));
        }
        expect(find.text('synthetic-secret'), findsNothing);
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets('large totals stay readable and exact in both themes', (
    tester,
  ) async {
    final api = _FactsApi();
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    for (final dark in [true, false]) {
      await _pumpPanel(tester, fixture, dark: dark);
      await _query(tester);
      await _query(tester, history: true);
      expect(find.text('Pro'), findsOneWidget);
      expect(find.text('6.98B'), findsOneWidget);
      expect(find.text('6,980,410,315 tokens'), findsOneWidget);
      expect(find.text('codex'), findsNothing);
      expect(find.text('积分余额：0'), findsOneWidget);
      expect(find.text('上游统计截至 2026-09-21'), findsOneWidget);
      expect(
        tester
            .widget<LinearProgressIndicator>(
              find.byType(LinearProgressIndicator),
            )
            .value,
        .69,
      );
      expect(tester.takeException(), isNull);
    }
  });

  testWidgets('failed reads only mark their own retained observation', (
    tester,
  ) async {
    final api = _FactsApi();
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    expect(api.calls, isEmpty);
    await _query(tester);
    expect(api.calls, [false]);
    await _query(tester, history: true);
    api.failQuota = true;
    await _query(tester);
    final stale = find.text('刷新失败 · 已保留上次成功采集的数据。');
    final quota = find.byKey(const Key('account-facts-quota'));
    final history = find.byKey(const Key('account-facts-history'));
    expect(find.descendant(of: quota, matching: stale), findsOneWidget);
    expect(find.descendant(of: history, matching: stale), findsNothing);
    expect(find.text('6,980,410,315 tokens'), findsOneWidget);
    expect(find.textContaining('69%'), findsOneWidget);
    // A successful history read must not clear a failed quota refresh.
    await _query(tester, history: true);
    expect(find.descendant(of: quota, matching: stale), findsOneWidget);
    api.failQuota = false;
    api.failHistory = true;
    await _query(tester);
    await _query(tester, history: true);
    expect(find.descendant(of: quota, matching: stale), findsNothing);
    expect(find.descendant(of: history, matching: stale), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('missing data is not zero, and partial history is explicit', (
    tester,
  ) async {
    final api = _FactsApi()
      ..quota = {'limits': <Object>[]}
      ..history = {
        'history': {'daily': <Object>[], 'partial': true},
      };
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.byType(LinearProgressIndicator), findsNothing);
    expect(find.text('上游未提供额度窗口，不代表用量为 0%。'), findsOneWidget);
    expect(find.text('—'), findsOneWidget);
    expect(find.text('未提供'), findsOneWidget);
    expect(find.text('上游报告历史统计暂不完整。'), findsOneWidget);
    expect(find.text('0 tokens'), findsNothing);
    expect(find.text('单日最高 Token'), findsNothing);
    expect(find.text('连续活跃天数'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('zero, exhausted quota and exact credits are not lost', (
    tester,
  ) async {
    final api = _FactsApi()
      ..quota = {
        'limits': [
          {
            'id': 'codex',
            'allowed': false,
            'limitReached': true,
            'primary': _window(0),
            'secondary': _window(105),
          },
        ],
        'credits': {'hasCredits': true, 'unlimited': false, 'balance': '12.50'},
      }
      ..history = {
        'history': {'lifetimeTokens': 0, 'daily': <Object>[], 'partial': false},
      };
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.text('0 tokens'), findsOneWidget);
    expect(find.text('已达限额'), findsOneWidget);
    expect(find.textContaining('105%'), findsOneWidget);
    expect(find.text('积分余额：12.50'), findsOneWidget);
    expect(
      tester
          .widgetList<LinearProgressIndicator>(
            find.byType(LinearProgressIndicator),
          )
          .map((w) => w.value),
      [0, 1],
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('narrow panel supports larger text without overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = await _fixture(_FactsApi());
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture, scale: 1.6);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.text('6,980,410,315 tokens'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'pending quota does not block history and resets on credential change',
    (tester) async {
      final api = _FactsApi();
      final fixture = await _fixture(api);
      addTearDown(fixture.controller.dispose);
      final pending = Completer<AccountFacts>();
      api.pendingQuota = pending.future;
      await _pumpPanel(tester, fixture);
      await tester.tap(find.byKey(const Key('account-quota-account.facts')));
      await tester.pump();
      await tester.tap(find.byKey(const Key('account-history-account.facts')));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 200));
      expect(api.calls, [false, true]);
      expect(find.text('6.98B'), findsOneWidget);
      final next = await _fixture(_FactsApi(), id: 'account.next');
      addTearDown(next.controller.dispose);
      await _pumpPanel(tester, next);
      pending.complete(api.facts(false));
      await tester.pumpAndSettle();
      expect(find.textContaining('69%'), findsNothing);
      expect(find.text('6.98B'), findsNothing);
      expect(tester.takeException(), isNull);
    },
  );

  test('account facts preserve source, missing windows and exact balances', () {
    final input = <String, Object?>{
      'accountId': 'account.b',
      'credentialEpoch': 2,
      'origin': 'https://chatgpt.com',
      'adapterId': 'chatgpt-codex',
      'adapterRevision': 1,
      'observedAt': '2026-09-22T01:00:00Z',
      'state': 'known',
      'planType': 'pro',
      'limits': <Object?>[],
    };
    final missing = AccountFacts.fromJson(input);
    expect(missing.accountId, 'account.b');
    expect(missing.credentialEpoch, 2);
    expect(missing.limits, isEmpty);
    expect(missing.credits, isNull);
    final zero = AccountFacts.fromJson({
      ...input,
      'limits': [
        {
          'id': 'codex',
          'allowed': true,
          'limitReached': false,
          'primary': {
            'usedPercent': 0,
            'windowSeconds': 18000,
            'resetAfterSeconds': 3600,
            'resetAt': 1800000000,
          },
        },
      ],
      'credits': {'hasCredits': true, 'unlimited': false, 'balance': '12.50'},
    });
    expect(zero.limits.single.primary?.usedPercent, 0);
    expect(zero.limits.single.secondary, isNull);
    expect(zero.credits?.balance, '12.50');
    expect(
      () => AccountFacts.fromJson({...input, 'accessToken': 'nope'}),
      throwsA(isA<ControlContractException>()),
    );
  });
}

typedef _Fixture = ({ProviderAccount account, WorkbenchController controller});

Future<_Fixture> _fixture(_FactsApi api, {String id = 'account.facts'}) async {
  final preview = PreviewControlApi(seedCaptures: false);
  final account = await preview.createProviderAccount(
    id: id,
    displayName: 'Managed B',
    upstreamEndpointId: 'target.codex.official',
    kind: 'bearer_token',
    secret: 'synthetic-secret',
    unlinked: true,
    headerPolicy: const ProviderAccountHeaderPolicy(),
  );
  api.account = account;
  return (
    account: account,
    controller: WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: preview.close,
    ),
  );
}

Future<void> _pumpPanel(
  WidgetTester tester,
  _Fixture fixture, {
  bool dark = true,
  double scale = 1,
}) => tester.pumpWidget(
  MaterialApp(
    theme: dark ? ViberTheme.dark() : ViberTheme.light(),
    home: MediaQuery(
      data: MediaQueryData(textScaler: TextScaler.linear(scale)),
      child: Scaffold(
        body: SingleChildScrollView(
          child: ProviderAccountFactsPanel(
            account: fixture.account,
            controller: fixture.controller,
            copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
          ),
        ),
      ),
    ),
  ),
);

Future<void> _query(WidgetTester tester, {bool history = false}) async {
  final button = find.byKey(
    Key('account-${history ? 'history' : 'quota'}-account.facts'),
  );
  await tester.ensureVisible(button);
  await tester.tap(button);
  await tester.pumpAndSettle();
}

Map<String, Object> _window(int used) => {
  'usedPercent': used,
  'windowSeconds': 604800,
  'resetAfterSeconds': 180000,
  'resetAt': DateTime(2026, 9, 24, 19, 42).millisecondsSinceEpoch ~/ 1000,
};

final class _FactsApi extends Fake implements ControlApi {
  late ProviderAccount account;
  final calls = <bool>[];
  bool failQuota = false, failHistory = false;
  Future<AccountFacts>? pendingQuota;
  Map<String, Object> quota = {
    'planType': 'pro',
    'limits': [
      {'id': 'codex', 'allowed': true, 'primary': _window(69)},
    ],
    'credits': {'hasCredits': false, 'unlimited': false, 'balance': '0'},
  };
  Map<String, Object> history = {
    'history': {
      'lifetimeTokens': 6980410315,
      'asOf': '2026-09-21',
      'daily': <Object>[],
      'partial': false,
    },
  };

  AccountFacts facts(bool isHistory) => AccountFacts.fromJson({
    'accountId': account.id,
    'credentialEpoch': account.credentialEpoch,
    'origin': account.credentialOrigin,
    'adapterId': 'chatgpt-codex',
    'adapterRevision': 1,
    'observedAt': '2026-09-22T04:56:07Z',
    'state': 'known',
    'limits': <Object>[],
    ...(isHistory ? history : quota),
  });

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) async {
    calls.add(history);
    if (history ? failHistory : failQuota) {
      throw StateError('synthetic failure');
    }
    if (!history && pendingQuota != null) return pendingQuota!;
    return facts(history);
  }
}

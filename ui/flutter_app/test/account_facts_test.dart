import 'dart:async';
import 'dart:convert';

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
        expect(find.text('Codex 可用额度重置券：1'), findsOneWidget);
        expect(find.text('当前可使用：1'), findsOneWidget);
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
      expect(find.text('积分余额：无可用积分'), findsOneWidget);
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
      ..quota = {
        'limits': <Object>[],
        'credits': {'hasCredits': true, 'unlimited': false},
      }
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
    expect(find.text('积分余额：未提供'), findsOneWidget);
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
    'banked reset requires choosing a credit and explicit confirmation',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _FactsApi()
        ..quota = {
          'planType': 'pro',
          'limits': [
            {'id': 'codex', 'allowed': false, 'primary': _window(100)},
          ],
          'rateLimitResets': {
            'availableCount': 1,
            'applicableAvailableCount': 1,
            'details': [
              {
                'id': 'credit-fixture',
                'resetType': 'codex_rate_limits',
                'status': 'available',
                'grantedAt': '2026-09-01T00:00:00Z',
                'expiresAt': '2030-10-01T00:00:00Z',
                'title': 'Full reset',
              },
            ],
          },
        };
      final fixture = await _fixture(api, codexOAuth: true);
      addTearDown(fixture.controller.dispose);
      await _pumpPanel(tester, fixture);
      await _query(tester);
      final reset = find.byKey(const Key('account-reset-account.facts'));
      expect(reset, findsOneWidget);
      await tester.ensureVisible(reset);
      await tester.tap(reset);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Full reset').last);
      await tester.pumpAndSettle();
      expect(find.text('使用这张重置券？'), findsOneWidget);
      await tester.tap(find.text('取消').last);
      await tester.pumpAndSettle();
      expect(api.redeemCalls, isEmpty);
      await tester.tap(reset);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Full reset').last);
      await tester.pumpAndSettle();
      final pending = Completer<AccountResetRedemption>();
      api.pendingRedeem = pending.future;
      await tester.tap(find.text('确认使用'));
      await tester.pump();
      expect(api.redeemCalls, hasLength(1));
      expect(tester.widget<TextButton>(reset).onPressed, isNull);
      pending.complete(
        AccountResetRedemption.fromJson({
          'accountId': fixture.account.id,
          'credentialEpoch': fixture.account.credentialEpoch,
          'creditId': 'credit-fixture',
          'outcome': 'reset',
          'windowsReset': 2,
        }),
      );
      await tester.pumpAndSettle();
      expect(api.redeemCalls, hasLength(1));
      expect(api.redeemCalls.single.creditId, 'credit-fixture');
      expect(find.text('已使用重置券。'), findsOneWidget);
      expect(api.calls.where((history) => !history).length, 2);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'ambiguous reset requires a fresh quota read before an explicit retry',
    (tester) async {
      final api = _FactsApi()
        ..quota = {
          'limits': <Object>[],
          'rateLimitResets': {
            'availableCount': 1,
            'details': [
              {
                'id': 'credit-fixture',
                'resetType': 'codex_rate_limits',
                'status': 'available',
                'grantedAt': '2026-09-01T00:00:00Z',
                'expiresAt': '2030-10-01T00:00:00Z',
              },
            ],
          },
        }
        ..resetUnconfirmed = true;
      final fixture = await _fixture(api, codexOAuth: true);
      addTearDown(fixture.controller.dispose);
      await _pumpPanel(tester, fixture);
      await _query(tester);
      Future<void> confirm() async {
        final reset = find.byKey(const Key('account-reset-account.facts'));
        await tester.ensureVisible(reset);
        await tester.tap(reset);
        await tester.pumpAndSettle();
        await tester.tap(find.text('Codex').last);
        await tester.pumpAndSettle();
        await tester.tap(find.text('确认使用'));
        await tester.pumpAndSettle();
      }

      await confirm();
      expect(find.text('未能确认重置结果。请先查看最新额度，再决定是否重试。'), findsOneWidget);
      expect(
        find.byKey(const Key('account-reset-account.facts')),
        findsNothing,
      );
      await _query(tester);
      api.resetUnconfirmed = false;
      await confirm();
      expect(api.redeemCalls, hasLength(2));
      expect(api.redeemCalls[1].creditId, api.redeemCalls[0].creditId);
      expect(tester.takeException(), isNull);
    },
  );

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
    expect(missing.rateLimitResets, isNull);
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
      'rateLimitResets': {'availableCount': 2, 'applicableAvailableCount': 1},
    });
    expect(zero.limits.single.primary?.usedPercent, 0);
    expect(zero.limits.single.secondary, isNull);
    expect(zero.credits?.balance, '12.50');
    expect(zero.rateLimitResets?.availableCount, 2);
    expect(zero.rateLimitResets?.applicableAvailableCount, 1);
    expect(
      AccountFacts.fromJson({
        ...input,
        'rateLimitResets': {'availableCount': 0},
      }).rateLimitResets?.applicableAvailableCount,
      isNull,
    );
    expect(
      () => AccountFacts.fromJson({
        ...input,
        'rateLimitResets': {'availableCount': 0, 'applicableAvailableCount': 1},
      }),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => AccountFacts.fromJson({...input, 'accessToken': 'nope'}),
      throwsA(isA<ControlContractException>()),
    );
  });
}

typedef _Fixture = ({ProviderAccount account, WorkbenchController controller});

Future<_Fixture> _fixture(
  _FactsApi api, {
  String id = 'account.facts',
  bool codexOAuth = false,
}) async {
  final preview = PreviewControlApi(seedCaptures: false);
  final claims = base64Url
      .encode(
        utf8.encode(
          jsonEncode({
            'exp':
                DateTime.now()
                    .toUtc()
                    .add(const Duration(days: 1))
                    .millisecondsSinceEpoch ~/
                1000,
            'https://api.openai.com/auth': {
              'chatgpt_account_id': 'workspace-fixture',
            },
          }),
        ),
      )
      .replaceAll('=', '');
  final token = 'eyJhbGciOiJub25lIn0.$claims.fixture';
  final account = await preview.createProviderAccount(
    id: id,
    displayName: 'Managed B',
    upstreamEndpointId: 'target.codex.official',
    kind: codexOAuth ? 'codex_oauth' : 'bearer_token',
    secret: codexOAuth ? '' : 'synthetic-secret',
    codexAuthJson: codexOAuth
        ? jsonEncode({
            'auth_mode': 'chatgpt',
            'OPENAI_API_KEY': null,
            'tokens': {
              'access_token': token,
              'id_token': token,
              'refresh_token': 'synthetic-refresh',
              'account_id': 'workspace-fixture',
            },
            'last_refresh': DateTime.now().toUtc().toIso8601String(),
          })
        : '',
    unlinked: true,
    headerPolicy: const ProviderAccountHeaderPolicy(),
  );
  api.account = account;
  return (
    account: account,
    controller: WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: !codexOAuth,
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
  Future<AccountResetRedemption>? pendingRedeem;
  bool resetUnconfirmed = false;
  final redeemCalls = <({String creditId})>[];
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

  @override
  Future<AccountResetRedemption> redeemAccountResetCredit(
    ProviderAccount account,
    String creditId,
  ) async {
    redeemCalls.add((creditId: creditId));
    if (pendingRedeem != null) return pendingRedeem!;
    if (resetUnconfirmed) {
      throw const ControlProblem(
        status: 502,
        reasonCode: 'reset_result_unconfirmed',
        messageKey: 'error.reset_result_unconfirmed',
      );
    }
    return AccountResetRedemption.fromJson({
      'accountId': account.id,
      'credentialEpoch': account.credentialEpoch,
      'creditId': creditId,
      'outcome': 'reset',
      'windowsReset': 2,
    });
  }
}

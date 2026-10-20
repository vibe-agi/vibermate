import 'dart:async';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/provider_account_facts.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  final reviewDirectory = kIsWeb
      ? null
      : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
  if (reviewDirectory != null && Platform.isMacOS) {
    setUpAll(() async {
      for (final font in {
        viberSystemFontFamily: '/System/Library/Fonts/Supplemental/Arial.ttf',
        'Ahem': '/System/Library/Fonts/Supplemental/Arial.ttf',
        'Roboto': '/System/Library/Fonts/Supplemental/Arial.ttf',
        'Menlo': '/System/Library/Fonts/Supplemental/Courier New.ttf',
        'PingFang SC': Platform.environment['VIBERMATE_QA_CJK_FONT'],
        'Hiragino Sans GB': '/System/Library/Fonts/Hiragino Sans GB.ttc',
      }.entries) {
        if (font.value == null) continue;
        final loader = FontLoader(font.key)
          ..addFont(File(font.value!).readAsBytes().then(ByteData.sublistView));
        await loader.load();
      }
      final icons = FontLoader('MaterialIcons')
        ..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
      await icons.load();
    });
  }

  testWidgets('compact quota includes the formatted credit balance', (
    tester,
  ) async {
    final facts = AccountFacts.fromJson({
      'accountId': 'account.credits',
      'credentialEpoch': 1,
      'origin': 'https://chatgpt.com',
      'adapterId': 'chatgpt-codex',
      'adapterRevision': 1,
      'observedAt': '2026-10-11T00:00:00Z',
      'state': 'known',
      'limits': [],
      'credits': {'hasCredits': true, 'unlimited': false, 'balance': '123.456'},
    });
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: Scaffold(
          body: ProviderAccountQuotaMini(
            facts: facts,
            loading: false,
            failed: false,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    expect(find.text('Credits: 123.46'), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  for (final (credits, english, chinese)
      in <(Map<String, Object>?, String, String)>[
        (
          {'hasCredits': true, 'unlimited': true},
          'Credits: Unlimited',
          '积分余额：无限制',
        ),
        (
          {'hasCredits': false, 'unlimited': false},
          'Credits: No credits available',
          '积分余额：无可用积分',
        ),
        (
          {'hasCredits': true, 'unlimited': false},
          'Credits: Not provided',
          '积分余额：未提供',
        ),
        (null, 'The upstream did not provide this data.', '上游暂未提供此项数据。'),
      ]) {
    for (final language in [
      AppLanguage.english,
      AppLanguage.simplifiedChinese,
    ]) {
      testWidgets('credit-only $english stays truthful in $language', (
        tester,
      ) async {
        final facts = AccountFacts.fromJson({
          'accountId': 'account.credit-states',
          'credentialEpoch': 1,
          'origin': 'https://chatgpt.com',
          'adapterId': 'chatgpt-codex',
          'adapterRevision': 1,
          'observedAt': '2026-10-11T00:00:00Z',
          'state': 'known',
          'limits': [],
          'credits': ?credits,
        });
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: ProviderAccountQuotaMini(
                facts: facts,
                loading: false,
                failed: false,
                copy: AppCopy.forLanguage(language),
              ),
            ),
          ),
        );
        expect(
          find.text(language == AppLanguage.english ? english : chinese),
          findsOneWidget,
        );
        expect(find.textContaining('0.00'), findsNothing);
        if (credits != null) {
          await tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.dark(),
              home: Scaffold(
                body: ProviderAccountQuotaMini(
                  facts: facts,
                  loading: false,
                  failed: true,
                  copy: AppCopy.forLanguage(language),
                ),
              ),
            ),
          );
          expect(
            find.text(language == AppLanguage.english ? english : chinese),
            findsOneWidget,
          );
          expect(
            find.text(AppCopy.forLanguage(language)('account_facts.stale')),
            findsOneWidget,
          );
        }
        await tester.pumpWidget(const SizedBox.shrink());
      });
    }
  }

  testWidgets(
    'quota countdown advances without a read and catches up on resume',
    (tester) async {
      var now = DateTime.utc(2026, 9, 29, 10);
      final facts = AccountFacts.fromJson({
        'accountId': 'account.clock',
        'credentialEpoch': 1,
        'origin': 'https://chatgpt.com',
        'adapterId': 'chatgpt-codex',
        'adapterRevision': 1,
        'observedAt': now.toIso8601String(),
        'state': 'known',
        'limits': [
          {
            'id': 'codex',
            'primary': {
              'windowSeconds': 18000,
              'usedPercent': 70,
              'resetAfterSeconds': 90,
              'resetAt':
                  DateTime.utc(2026, 9, 29, 10, 1, 30).millisecondsSinceEpoch ~/
                  1000,
            },
          },
        ],
      });
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      addTearDown(
        () => tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.resumed,
        ),
      );
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: ProviderAccountQuotaMini(
              facts: facts,
              loading: false,
              failed: false,
              copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
              clock: () => now,
            ),
          ),
        ),
      );
      expect(find.text('1m'), findsOneWidget);
      // A visible but unfocused desktop window is inactive, not hidden.
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
      now = now.add(const Duration(minutes: 1));
      await tester.pump(const Duration(minutes: 1));
      expect(find.text('<1m'), findsOneWidget);
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.paused);
      now = now.add(const Duration(hours: 12));
      await tester.pump(const Duration(minutes: 1));
      expect(find.text('<1m'), findsOneWidget);
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump();
      expect(find.text('待刷新'), findsOneWidget);
      expect(find.text('5h · 70%'), findsOneWidget);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump(const Duration(minutes: 2));
      expect(tester.takeException(), isNull);
    },
  );

  test(
    'quota thresholds and countdown preserve boundary and expired meaning',
    () {
      final now = DateTime.utc(2026, 9, 28);
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      for (final colors in [ViberColors.light, ViberColors.dark]) {
        for (final (percent, color) in [
          (69, colors.route),
          (70, colors.warning),
          (89, colors.warning),
          (90, colors.danger),
          (100, colors.danger),
        ]) {
          expect(accountQuotaUsageColor(colors, percent), color);
        }
      }
      expect(
        accountQuotaCountdown(
          now.add(const Duration(days: 5, hours: 6)),
          now,
          copy,
        ),
        '5d6h',
      );
      expect(
        accountQuotaCountdown(
          now.add(const Duration(hours: 6, minutes: 20)),
          now,
          copy,
        ),
        '6h20m',
      );
      expect(
        accountQuotaCountdown(now.add(const Duration(seconds: 1)), now, copy),
        '<1m',
      );
      expect(accountQuotaCountdown(now, now, copy), '待刷新');
      expect(
        accountQuotaCountdown(now.subtract(const Duration(days: 1)), now, copy),
        '待刷新',
      );
    },
  );

  for (final width in [390.0, 1440.0]) {
    for (final dark in [true, false]) {
      for (final scale in [1.0, 2.0]) {
        testWidgets(
          'soonest reset first; shared refresh keeps selection and order at $width dark=$dark scale=$scale',
          (tester) async {
            await tester.binding.setSurfaceSize(Size(width, 1250));
            addTearDown(() => tester.binding.setSurfaceSize(null));
            final preview = PreviewControlApi(seedCaptures: false);
            final accounts = <ProviderAccount>[];
            for (final id in ['a-far', 'b-soon', 'c-unknown', 'd-expired']) {
              accounts.add(
                await preview.createProviderAccount(
                  id: id,
                  displayName: '$id · a deliberately long account name',
                  upstreamEndpointId: 'target.codex.official',
                  kind: 'bearer_token',
                  secret: 'synthetic',
                  headerPolicy: const ProviderAccountHeaderPolicy(),
                ),
              );
            }
            final base = await preview.loadDashboard();
            final endpoint = base.endpoints.singleWhere(
              (e) => e.id == 'target.codex.official',
            );
            final original = base.environments.singleWhere(
              (e) => e.id == 'work',
            );
            final route = EnvironmentRoute(
              id: 'quota-route',
              revision: 1,
              providerTarget: EnvironmentProviderTarget(
                id: endpoint.id,
                revision: endpoint.revision,
                origin: endpoint.origin,
                realmId: endpoint.realmId,
                capabilities: endpoint.capabilities,
              ),
              backendProtocol: 'openai_responses',
              accountPolicy: RouteAccountPolicy(
                revision: 1,
                mode: 'fixed',
                selector: null,
                fixedAccountId: 'a-far',
                accounts: [
                  for (final account in accounts)
                    RouteAccountReference(
                      id: account.id,
                      revision: 1,
                      displayName: account.displayName,
                    ),
                ],
              ),
              modelPolicy: const EnvironmentModelPolicy(
                revision: 1,
                mode: 'passthrough',
                mappings: [],
              ),
              wireProfileRef: 'follow-client',
              pluginBindings: const [],
            );
            final client = original.clientEndpoints.last;
            final plan = client.protocolPlans.single;
            final environment = original.copyWith(
              clientEndpoints: [
                client.copyWith(
                  protocolPlans: [
                    plan.copyWith(
                      destination: EnvironmentDestination.upstream(
                        EnvironmentUpstreamPlan(
                          defaultRouteId: route.id,
                          routes: [route],
                          routeSet: EnvironmentRouteSet(
                            id: 'quota-routes',
                            revision: 1,
                            candidateRouteIds: [route.id],
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
              ],
            );
            final api = _QuotaApi(accounts);
            var localNow = api.now;
            final exercisePeriodic = width == 390 && !dark && scale == 2;
            final controller = WorkbenchController(
              api: api,
              terminalCommands: PreviewTerminalCommandService(),
              previewMode: true,
              terminalManagement: false,
              clock: () => localNow,
              closeRuntime: preview.close,
            );
            addTearDown(controller.dispose);
            controller.data = DashboardData(
              status: base.status,
              captures: const [],
              captureNextCursor: '',
              environments: [environment],
              endpoints: base.endpoints,
              accounts: accounts,
            );
            api.dashboard = controller.data;
            controller.selectEnvironment(environment.id);
            if (exercisePeriodic) {
              await controller.initialize();
              expect(controller.errorMessage, isNull);
            }
            final copy = AppCopy.forLanguage(
              dark ? AppLanguage.simplifiedChinese : AppLanguage.english,
            );
            final accountsVisible = ValueNotifier(false);
            addTearDown(accountsVisible.dispose);
            final boundary = GlobalKey();
            final baseTheme = dark ? ViberTheme.dark() : ViberTheme.light();
            final theme = reviewDirectory == null
                ? baseTheme
                : baseTheme.copyWith(
                    textTheme: baseTheme.textTheme
                        .apply(fontFamily: viberSystemFontFamily)
                        .copyWith(
                          labelSmall: baseTheme.textTheme.labelSmall?.copyWith(
                            fontFamily: viberSystemFontFamily,
                            fontFamilyFallback: viberFontFallback,
                          ),
                        ),
                    dialogTheme: baseTheme.dialogTheme.copyWith(
                      contentTextStyle: baseTheme.dialogTheme.contentTextStyle!
                          .copyWith(fontFamily: viberSystemFontFamily),
                    ),
                  );
            await tester.pumpWidget(
              MaterialApp(
                theme: theme,
                builder: (context, child) => MediaQuery(
                  data: MediaQuery.of(
                    context,
                  ).copyWith(textScaler: TextScaler.linear(scale)),
                  child: child!,
                ),
                home: Scaffold(
                  body: RepaintBoundary(
                    key: boundary,
                    child: AnimatedBuilder(
                      animation: controller,
                      builder: (_, _) => ValueListenableBuilder(
                        valueListenable: accountsVisible,
                        builder: (_, showAccounts, _) => Stack(
                          fit: StackFit.expand,
                          children: [
                            Offstage(
                              offstage: showAccounts,
                              child: EnvironmentsView(
                                controller: controller,
                                copy: copy,
                              ),
                            ),
                            Offstage(
                              offstage: !showAccounts,
                              child: ProviderAccountsView(
                                controller: controller,
                                copy: copy,
                              ),
                            ),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
              ),
            );
            await tester.pumpAndSettle();
            expect(api.calls, ['a-far', 'b-soon', 'c-unknown', 'd-expired']);
            expect(find.text('123.46', skipOffstage: false), findsNWidgets(4));
            expect(
              find.text(
                copy.format('account_facts.credits', {'balance': '123.46'}),
              ),
              findsNWidgets(4),
            );
            Finder card(String id) =>
                find.byKey(Key('environment-account-option-$id'));
            List<String> order() => tester
                .widgetList<Container>(
                  find.byWidgetPredicate(
                    (widget) =>
                        widget is Container &&
                        widget.key is ValueKey<String> &&
                        (widget.key! as ValueKey<String>).value.startsWith(
                          'environment-account-option-',
                        ),
                  ),
                )
                .map(
                  (w) => (w.key! as ValueKey<String>).value.replaceFirst(
                    'environment-account-option-',
                    '',
                  ),
                )
                .toList();
            expect(order(), ['b-soon', 'a-far', 'c-unknown', 'd-expired']);
            final active = find.byKey(
              const Key('environment-account-activate-a-far'),
            );
            final inactive = find.byKey(
              const Key('environment-account-activate-b-soon'),
            );
            expect(tester.getSize(active), tester.getSize(inactive));
            expect(tester.getSize(inactive).width, lessThanOrEqualTo(40));
            expect(tester.getSize(inactive).height, greaterThanOrEqualTo(32));
            expect(tester.widget<IconButton>(active).onPressed, isNull);
            expect(tester.widget<IconButton>(inactive).onPressed, isNotNull);
            expect(
              tester.widget<IconButton>(inactive).tooltip,
              contains(accounts[1].displayName),
            );
            expect(
              tester.widget<IconButton>(active).tooltip,
              contains(accounts[0].displayName),
            );
            final semantics = tester.ensureSemantics();
            expect(
              tester.getSemantics(active).flagsCollection.isSelected,
              ui.Tristate.isTrue,
            );
            expect(
              tester.getSemantics(inactive).flagsCollection.isSelected,
              ui.Tristate.isFalse,
            );
            semantics.dispose();
            expect(
              find.descendant(
                of: card('b-soon'),
                matching: find.text('7d · 90%'),
              ),
              findsOneWidget,
            );
            expect(
              find.descendant(
                of: card('b-soon'),
                matching: find.text('5h · 70%'),
              ),
              findsOneWidget,
            );
            expect(
              find.descendant(
                of: card('d-expired'),
                matching: find.text(copy('account_facts.reset_due')),
              ),
              findsOneWidget,
            );
            expect(
              find.descendant(
                of: card('c-unknown'),
                matching: find.byType(LinearProgressIndicator),
              ),
              findsNothing,
            );
            final progress = tester
                .widgetList<LinearProgressIndicator>(
                  find.descendant(
                    of: card('b-soon'),
                    matching: find.byType(LinearProgressIndicator),
                  ),
                )
                .toList();
            expect(progress.map((p) => p.color), [
              dark ? ViberColors.dark.danger : ViberColors.light.danger,
              dark ? ViberColors.dark.warning : ViberColors.light.warning,
            ]);

            api.hold = true;
            final refresh = find.byKey(
              const Key('environment-account-refresh-quota-route'),
            );
            final background = controller.ensureProviderAccountQuotas(
              accounts,
              refresh: true,
            );
            await tester.pump();
            expect(tester.widget<IconButton>(refresh).onPressed, isNotNull);
            expect(
              find.descendant(
                of: refresh,
                matching: find.byType(CircularProgressIndicator),
              ),
              findsNothing,
            );
            await tester.ensureVisible(refresh);
            await tester.tap(refresh);
            await tester.pump();
            expect(api.pending.length, 4);
            expect(
              find.descendant(
                of: refresh,
                matching: find.byType(CircularProgressIndicator),
              ),
              findsOneWidget,
            );
            if (exercisePeriodic) {
              // Actual mounted views, manual refresh and controller timer all
              // converge on these same four held account-facts requests.
              localNow = localNow.add(const Duration(minutes: 5));
              await tester.pump(const Duration(minutes: 5));
              expect(api.pending.length, 4);
              expect(api.calls.length, 8);
            }
            api.days['a-far'] = 1;
            api.days['b-soon'] = 5;
            api.primaryPercent = 42;
            api.balance = '234.567';
            api.pending['a-far']!.complete(api.facts('a-far'));
            await tester.pump();
            expect(order().take(2), ['b-soon', 'a-far']);
            expect(tester.widget<IconButton>(refresh).onPressed, isNull);
            for (final id in ['b-soon', 'c-unknown', 'd-expired']) {
              api.pending[id]!.complete(api.facts(id));
            }
            await background;
            await tester.pumpAndSettle();
            expect(api.calls, [
              'a-far',
              'b-soon',
              'c-unknown',
              'd-expired',
              'a-far',
              'b-soon',
              'c-unknown',
              'd-expired',
            ]);
            expect(
              find.text(
                copy.format('account_facts.credits', {'balance': '234.57'}),
              ),
              findsNWidgets(4),
            );
            expect(find.text('234.57', skipOffstage: false), findsNWidgets(4));
            expect(find.text('5h · 42%'), findsNWidgets(3));
            expect(find.text('42%', skipOffstage: false), findsNWidgets(3));
            expect(order(), ['a-far', 'b-soon', 'c-unknown', 'd-expired']);
            expect(
              controller
                  .selectedEnvironment!
                  .routes
                  .single
                  .accountPolicy
                  .fixedAccountId,
              'a-far',
            );
            expect(tester.takeException(), isNull);
            await tester.ensureVisible(refresh);
            await tester.pumpAndSettle();
            await _review(
              tester,
              boundary,
              reviewDirectory,
              'quota-flow-${width.toInt()}-${dark ? 'zh-dark' : 'en-light'}-scale$scale',
            );
            accountsVisible.value = true;
            await tester.pumpAndSettle();
            final accountRefresh = find.byKey(
              const Key('provider-accounts-refresh-all'),
            );
            expect(accountRefresh, findsOneWidget);
            expect(
              tester
                  .widget<IconButton>(
                    find.byKey(
                      const Key('environment-account-refresh-quota-route'),
                      skipOffstage: false,
                    ),
                  )
                  .onPressed,
              isNotNull,
            );
            await _review(
              tester,
              boundary,
              reviewDirectory,
              'quota-accounts-${width.toInt()}-${dark ? 'zh-dark' : 'en-light'}-scale$scale',
            );
            expect(tester.takeException(), isNull);
            await tester.pumpWidget(const SizedBox.shrink());
            controller.dispose();
          },
        );
      }
    }
  }
}

final class _QuotaApi extends Fake implements ControlApi {
  _QuotaApi(this.accounts);
  final List<ProviderAccount> accounts;
  DashboardData? dashboard;
  final now = DateTime.now().toUtc();
  final days = {'a-far': 5, 'b-soon': 1, 'd-expired': -1};
  final pending = <String, Completer<AccountFacts>>{};
  final calls = <String>[];
  int primaryPercent = 70;
  String balance = '123.456';
  bool hold = false;

  @override
  Future<DashboardData> loadDashboard() async => dashboard!;

  @override
  Future<List<ApprovalRecord>> pendingApprovals() async => const [];
  AccountFacts facts(String id) {
    final account = accounts.singleWhere((a) => a.id == id);
    Map<String, Object> window(int seconds, int percent, DateTime reset) => {
      'windowSeconds': seconds,
      'usedPercent': percent,
      'resetAfterSeconds': 1,
      'resetAt': reset.millisecondsSinceEpoch ~/ 1000,
    };
    return AccountFacts.fromJson({
      'accountId': id,
      'credentialEpoch': account.credentialEpoch,
      'origin': account.credentialOrigin,
      'adapterId': 'chatgpt-codex',
      'adapterRevision': 1,
      'observedAt': now.toIso8601String(),
      'state': 'known',
      'limits': days[id] == null
          ? []
          : [
              {
                'id': 'codex',
                'primary': window(
                  18000,
                  primaryPercent,
                  now.add(const Duration(hours: 2)),
                ),
                'secondary': window(
                  604800,
                  90,
                  now.add(Duration(days: days[id]!)),
                ),
              },
            ],
      'credits': {'hasCredits': true, 'unlimited': false, 'balance': balance},
    });
  }

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) async {
    calls.add(id);
    if (!hold) return facts(id);
    return (pending[id] = Completer<AccountFacts>()).future;
  }
}

Future<void> _review(
  WidgetTester tester,
  GlobalKey key,
  String? directory,
  String name,
) async {
  if (directory == null) return;
  await tester.runAsync(() async {
    final boundary =
        key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage();
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    await Directory(directory).create(recursive: true);
    await File(
      '$directory/$name.png',
    ).writeAsBytes(bytes!.buffer.asUint8List());
    image.dispose();
  });
}

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/provider_account_facts.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
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
      testWidgets(
        'soonest reset first; refresh keeps selection and order at $width dark=$dark',
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
          final original = base.environments.singleWhere((e) => e.id == 'work');
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
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: true,
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
          controller.selectEnvironment(environment.id);
          final copy = AppCopy.forLanguage(
            dark ? AppLanguage.simplifiedChinese : AppLanguage.english,
          );
          await tester.pumpWidget(
            MaterialApp(
              theme: dark ? ViberTheme.dark() : ViberTheme.light(),
              home: Scaffold(
                body: AnimatedBuilder(
                  animation: controller,
                  builder: (_, _) =>
                      EnvironmentsView(controller: controller, copy: copy),
                ),
              ),
            ),
          );
          await tester.pumpAndSettle();
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
          expect(tester.widget<OutlinedButton>(active).onPressed, isNull);
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
          await tester.ensureVisible(refresh);
          await tester.tap(refresh);
          await tester.pump();
          expect(api.pending.length, 4);
          api.days['a-far'] = 1;
          api.days['b-soon'] = 5;
          api.pending['a-far']!.complete(api.facts('a-far'));
          await tester.pump();
          expect(order().take(2), ['b-soon', 'a-far']);
          expect(tester.widget<IconButton>(refresh).onPressed, isNull);
          for (final id in ['b-soon', 'c-unknown', 'd-expired']) {
            api.pending[id]!.complete(api.facts(id));
          }
          await tester.pumpAndSettle();
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
          await tester.pumpWidget(const SizedBox.shrink());
          controller.dispose();
        },
      );
    }
  }
}

final class _QuotaApi extends Fake implements ControlApi {
  _QuotaApi(this.accounts);
  final List<ProviderAccount> accounts;
  final now = DateTime.now().toUtc();
  final days = {'a-far': 5, 'b-soon': 1, 'd-expired': -1};
  final pending = <String, Completer<AccountFacts>>{};
  bool hold = false;
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
                'primary': window(18000, 70, now.add(const Duration(hours: 2))),
                'secondary': window(
                  604800,
                  90,
                  now.add(Duration(days: days[id]!)),
                ),
              },
            ],
    });
  }

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) async {
    if (!hold) return facts(id);
    return (pending[id] = Completer<AccountFacts>()).future;
  }
}

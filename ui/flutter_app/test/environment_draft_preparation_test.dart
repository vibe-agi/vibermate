import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  late PreviewControlApi api;
  late UpstreamEndpoint service;
  late ProviderAccount oauth;
  late List<EnvironmentClientEndpoint> base;
  late List<ProviderAccount> accounts;

  setUp(() async {
    api = PreviewControlApi(seedCaptures: false);
    addTearDown(api.close);
    service = (await api.loadDashboard()).endpoints.singleWhere(
      (endpoint) => endpoint.id == 'target.codex.official',
    );
    final bearer = await api.createProviderAccount(
      id: 'account.old',
      displayName: 'Old bearer',
      upstreamEndpointId: service.id,
      kind: 'bearer_token',
      secret: 'synthetic-bearer',
      headerPolicy: const ProviderAccountHeaderPolicy(),
    );
    oauth = await api.createProviderAccount(
      id: 'account.oauth',
      displayName: 'Managed OAuth',
      upstreamEndpointId: service.id,
      kind: 'codex_oauth',
      secret: '',
      codexAuthJson: jsonEncode({
        'auth_mode': 'chatgpt',
        'tokens': {
          'account_id': 'synthetic-workspace',
          'access_token': 'synthetic-access',
          'refresh_token': 'synthetic-refresh',
          'id_token': 'synthetic-id',
        },
        'last_refresh': '2026-09-21T00:00:00Z',
      }),
      headerPolicy: const ProviderAccountHeaderPolicy(),
    );
    base = appendEnvironmentUpstreamEndpoint(
      endpoints: const [],
      upstreamEndpoint: service,
      accountPolicy: fixedRouteAccountPolicy(bearer),
      availableAccounts: [bearer],
      identityNonce: 'codex-stale-service',
    );
    final endpoint = base.single;
    final plan = endpoint.protocolPlans.single;
    base = assignEnvironmentRouteAccountHistory(
      endpoints: base,
      clientEndpointId: endpoint.id,
      protocolPlanId: plan.id,
      routeId: plan.routes.single.id,
      allowed: true,
    );
    accounts = (await api.loadDashboard()).accounts;
  });

  test(
    'Codex bearer to OAuth refreshes the service and revokes old history consent',
    () {
      final before = jsonEncode(
        base.map((endpoint) => endpoint.toJson()).toList(),
      );
      final endpoint = base.single;
      final plan = endpoint.protocolPlans.single;
      final route = plan.routes.single;
      final edited = assignEnvironmentRouteAccount(
        endpoints: base,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: oauth,
      );
      final prepared = prepareEnvironmentDraftEndpoints(
        availableAccounts: accounts,
        base: base,
        edited: edited,
        upstreamEndpoints: [_updated(service)],
      );
      final next = prepared.single.protocolPlans.single.routes.single;
      expect(next.providerTarget.revision, 2);
      expect(next.accountPolicy.fixedAccountId, oauth.id);
      expect(next.allowAccountHistory, isFalse);
      expect(next.revision, route.revision + 1);
      expect(next.accountPolicy.revision, route.accountPolicy.revision + 1);
      expect(prepared.single.revision, endpoint.revision + 1);
      expect(prepared.single.protocolPlans.single.revision, plan.revision + 1);
      expect(
        jsonEncode(base.map((endpoint) => endpoint.toJson()).toList()),
        before,
      );
      expect(
        prepareEnvironmentDraftEndpoints(
          availableAccounts: accounts,
          base: base,
          edited: prepared,
          upstreamEndpoints: [_updated(service)],
        ).single.toJson(),
        prepared.single.toJson(),
      );
    },
  );

  test(
    'refreshing a service alone preserves account, history permission and route choices',
    () {
      final endpoint = base.single;
      final plan = endpoint.protocolPlans.single;
      final route = plan.routes.single;
      final prepared = prepareEnvironmentDraftEndpoints(
        availableAccounts: accounts,
        base: base,
        edited: base,
        upstreamEndpoints: [_updated(service)],
      );
      final nextPlan = prepared.single.protocolPlans.single;
      final next = nextPlan.routes.single;
      expect(next.providerTarget.revision, 2);
      expect(next.accountPolicy, route.accountPolicy);
      expect(next.allowAccountHistory, isTrue);
      expect(next.modelPolicy.toJson(), route.modelPolicy.toJson());
      expect(
        nextPlan.destination.upstream!.routeSet.toJson(),
        plan.destination.upstream!.routeSet.toJson(),
      );
      expect(nextPlan.egressProfile.toJson(), plan.egressProfile.toJson());
      expect(next.revision, route.revision + 1);
      expect(route.providerTarget.revision, 1);
    },
  );

  test(
    'current references are a no-op even if catalog capabilities are reordered',
    () {
      final prepared = prepareEnvironmentDraftEndpoints(
        availableAccounts: accounts,
        base: base,
        edited: base,
        upstreamEndpoints: [
          _updated(
            service,
            revision: 1,
            capabilities: service.capabilities.reversed.toList(),
          ),
        ],
      );
      expect(prepared.single.toJson(), base.single.toJson());
    },
  );

  for (final scenario in [
    'missing',
    'disabled',
    'origin',
    'realm',
    'protocol',
    'capability',
    'older',
  ]) {
    test('does not silently refresh an incompatible service: $scenario', () {
      final changed = _updated(
        service,
        state: scenario == 'disabled' ? 'disabled' : null,
        origin: scenario == 'origin'
            ? Uri.parse('https://different.example')
            : null,
        realmId: scenario == 'realm' ? 'another.realm' : null,
        protocols: scenario == 'protocol' ? ['anthropic_messages'] : null,
        capabilities: scenario == 'capability' ? ['messages'] : null,
        revision: scenario == 'older' ? 0 : 2,
      );
      expect(
        () => prepareEnvironmentDraftEndpoints(
          availableAccounts: accounts,
          base: base,
          edited: base,
          upstreamEndpoints: scenario == 'missing' ? [] : [changed],
        ),
        throwsA(isA<EnvironmentUpstreamReferenceException>()),
      );
      expect(
        base.single.protocolPlans.single.routes.single.providerTarget.revision,
        1,
      );
    });
  }

  test(
    'new policies begin at revision one after refreshing their services',
    () {
      final prepared = prepareEnvironmentDraftEndpoints(
        availableAccounts: accounts,
        base: const [],
        edited: base,
        upstreamEndpoints: [_updated(service)],
      );
      expect(prepared.single.revision, 1);
      final plan = prepared.single.protocolPlans.single;
      expect(plan.revision, 1);
      expect(plan.routes.single.revision, 1);
      expect(plan.routes.single.providerTarget.revision, 2);
    },
  );

  for (final language in AppLanguage.values) {
    testWidgets('upstream revision errors are actionable in $language', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      final copy = AppCopy.forLanguage(language);
      for (final key in [
        'error.environment_upstream_stale',
        'error.environment_upstream_changed',
      ]) {
        controller.environmentError = key;
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: EnvironmentsView(controller: controller, copy: copy),
            ),
          ),
        );
        await tester.pump();
        expect(find.text(copy(key)), findsOneWidget);
        expect(find.text(key), findsNothing);
        expect(tester.takeException(), isNull);
      }
    });
  }

  test(
    'selector candidates follow current links and advance the draft exactly once',
    () async {
      final endpoint = base.single;
      final plan = endpoint.protocolPlans.single;
      final route = plan.routes.single;
      final selector = CodeLibraryAccountSelectorRevision(
        id: 'selector.fixture',
        revision: 1,
        collectionId: 'collection.fixture',
        displayName: 'Fixture selector',
        policy: const AccountSelectorPolicy(
          javaScript: 'selection.accountId = accounts[0].id;',
        ),
        publishedAt: DateTime.utc(2026, 9, 21),
      );
      final frozen = assignEnvironmentRouteAccountPolicy(
        endpoints: base,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        availableAccounts: accounts,
        policy: RouteAccountPolicy(
          revision: route.accountPolicy.revision,
          mode: 'javascript',
          fixedAccountId: '',
          selector: selector,
          accounts: [
            RouteAccountReference(
              id: oauth.id,
              revision: oauth.revision,
              displayName: oauth.displayName,
            ),
          ],
        ),
      );
      final unlinked = await api.createProviderAccount(
        id: 'account.unlinked',
        displayName: 'Unlinked',
        upstreamEndpointId: service.id,
        unlinked: true,
        kind: 'bearer_token',
        secret: 'synthetic-unlinked',
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      final before = frozen.single.protocolPlans.single.routes.single;
      final prepared = prepareEnvironmentDraftEndpoints(
        base: frozen,
        edited: frozen,
        upstreamEndpoints: [service],
        availableAccounts: [...accounts, unlinked],
      );
      final after = prepared.single.protocolPlans.single.routes.single;
      final eligible =
          accounts
              .where(
                (account) =>
                    account.usable &&
                    account.isLinkedTo(service.id) &&
                    account.credentialOrigin == service.origin.toString(),
              )
              .map((account) => account.id)
              .toList()
            ..sort();
      expect(
        after.accountPolicy.accounts.map((account) => account.id),
        eligible,
      );
      expect(
        after.accountPolicy.accounts.any(
          (account) => account.id == unlinked.id,
        ),
        isFalse,
      );
      expect(after.accountPolicy.selector, selector);
      expect(after.accountPolicy.revision, before.accountPolicy.revision + 1);
      expect(after.revision, before.revision + 1);
      expect(prepared.single.revision, frozen.single.revision + 1);
      expect(
        prepared.single.protocolPlans.single.revision,
        frozen.single.protocolPlans.single.revision + 1,
      );
      expect(before.accountPolicy.accounts.single.id, oauth.id);
      expect(
        prepareEnvironmentDraftEndpoints(
          base: frozen,
          edited: prepared,
          upstreamEndpoints: [service],
          availableAccounts: [...accounts.reversed, unlinked],
        ).single.toJson(),
        prepared.single.toJson(),
      );
      expect(
        () => prepareEnvironmentDraftEndpoints(
          base: frozen,
          edited: frozen,
          upstreamEndpoints: [service],
          availableAccounts: [unlinked],
        ),
        throwsA(isA<EnvironmentAccountSelectionException>()),
      );
    },
  );
}

UpstreamEndpoint _updated(
  UpstreamEndpoint service, {
  int revision = 2,
  String? state,
  Uri? origin,
  String? realmId,
  List<String>? protocols,
  List<String>? capabilities,
}) => UpstreamEndpoint(
  id: service.id,
  displayName: service.displayName,
  origin: origin ?? service.origin,
  realmId: realmId ?? service.realmId,
  backendProtocols: protocols ?? service.backendProtocols,
  capabilities: capabilities ?? service.capabilities,
  accountKinds: service.accountKinds,
  state: state ?? service.state,
  revision: revision,
);

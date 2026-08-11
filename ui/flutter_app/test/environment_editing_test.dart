import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  test(
    'Route Account rebinding advances every changed child revision',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );
      final endpoint = work.clientEndpoints.first;
      final plan = endpoint.protocolPlans.first;
      final route = plan.routes.firstWhere(
        (value) => value.id == 'anthropic-direct',
      );
      final untouched = plan.routes.firstWhere(
        (value) => value.id == 'orbit-fallback',
      );
      final account = dashboard.accounts.firstWhere(
        (value) => value.id == 'anthropic-lab',
      );

      final updated = assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: account,
      );
      final nextEndpoint = updated.first;
      final nextPlan = nextEndpoint.protocolPlans.first;
      final nextRoute = nextPlan.routes.firstWhere(
        (value) => value.id == route.id,
      );

      expect(nextEndpoint.revision, endpoint.revision + 1);
      expect(nextPlan.revision, plan.revision + 1);
      expect(nextRoute.revision, route.revision + 1);
      expect(
        nextRoute.accountPolicy.revision,
        route.accountPolicy.revision + 1,
      );
      expect(nextRoute.accountPolicy.preferredAccountId, account.id);
      expect(nextRoute.accountPolicy.candidateAccountIds, [account.id]);
      expect(nextRoute.accountPolicy.accountRevisions, {
        account.id: account.revision,
      });
      expect(
        nextPlan.routes.firstWhere((value) => value.id == untouched.id),
        same(untouched),
      );
    },
  );

  test('Route rejects an Account owned by another upstream Endpoint', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final work = dashboard.environments.firstWhere(
      (value) => value.id == 'work',
    );
    final endpoint = work.clientEndpoints.first;
    final plan = endpoint.protocolPlans.first;
    final route = plan.routes.firstWhere(
      (value) => value.id == 'anthropic-direct',
    );
    final foreign = dashboard.accounts.firstWhere(
      (value) => value.id == 'openai-work',
    );

    expect(
      () => assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: foreign,
      ),
      throwsArgumentError,
    );
  });

  test(
    'same-origin route may keep client credential inside a managed plan',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );
      final endpoint = work.clientEndpoints.first;
      final plan = endpoint.protocolPlans.first;

      final updated = assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: 'anthropic-direct',
        account: null,
      );
      final nextPlan = updated.first.protocolPlans.first;
      final route = nextPlan.routes.firstWhere(
        (value) => value.id == 'anthropic-direct',
      );
      expect(nextPlan.mode, 'managed');
      expect(route.accountPolicy.mode, 'client_passthrough');
      expect(route.accountPolicy.candidateAccountIds, isEmpty);
    },
  );

  test('retargeted route cannot reuse the client credential', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final work = dashboard.environments.firstWhere(
      (value) => value.id == 'work',
    );
    final endpoint = work.clientEndpoints.first;
    final plan = endpoint.protocolPlans.first;

    expect(
      () => assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: 'orbit-fallback',
        account: null,
      ),
      throwsArgumentError,
    );
  });

  test('adding a retargeted Endpoint requires an Account it owns', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final orbit = dashboard.endpoints.firstWhere(
      (value) => value.id == 'target.orbit.relay',
    );
    final orbitAccount = dashboard.accounts.firstWhere(
      (value) => value.id == 'orbit-team',
    );

    expect(
      () => appendEnvironmentUpstreamEndpoint(
        endpoints: const [],
        upstreamEndpoint: orbit,
        account: null,
      ),
      throwsArgumentError,
    );

    final endpoints = appendEnvironmentUpstreamEndpoint(
      endpoints: const [],
      upstreamEndpoint: orbit,
      account: orbitAccount,
    );
    final plan = endpoints.single.protocolPlans.single;
    final route = plan.routes.single;
    expect(plan.mode, 'managed');
    expect(route.endpointId, orbit.id);
    expect(route.accountPolicy.preferredAccountId, orbitAccount.id);
    expect(route.accountPolicy.accountRevisions, {
      orbitAccount.id: orbitAccount.revision,
    });
  });

  test('official Endpoint may start as exact original passthrough', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final official = dashboard.endpoints.firstWhere(
      (value) => value.id == 'target.anthropic.official',
    );

    final endpoints = appendEnvironmentUpstreamEndpoint(
      endpoints: const [],
      upstreamEndpoint: official,
      account: null,
    );
    final plan = endpoints.single.protocolPlans.single;
    final route = plan.routes.single;
    expect(plan.mode, 'original_passthrough');
    expect(route.modelPolicy.mode, 'preserve');
    expect(route.accountPolicy.mode, 'client_passthrough');
  });

  test('adding a candidate Route never silently changes the default', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final research = dashboard.environments.firstWhere(
      (value) => value.id == 'research',
    );
    final official = dashboard.endpoints.firstWhere(
      (value) => value.id == 'target.anthropic.official',
    );
    final account = dashboard.accounts.firstWhere(
      (value) => value.id == 'anthropic-work',
    );
    final originalPlan = research.clientEndpoints.single.protocolPlans.single;

    final endpoints = appendEnvironmentUpstreamEndpoint(
      endpoints: research.clientEndpoints,
      upstreamEndpoint: official,
      account: account,
    );
    final plan = endpoints.single.protocolPlans.single;

    expect(plan.defaultRouteId, originalPlan.defaultRouteId);
    expect(plan.routes, hasLength(2));
    expect(plan.routeSet.candidateRouteIds, hasLength(2));
    expect(
      plan.routes
          .firstWhere((route) => route.endpointId == official.id)
          .accountPolicy
          .preferredAccountId,
      account.id,
    );
  });
}

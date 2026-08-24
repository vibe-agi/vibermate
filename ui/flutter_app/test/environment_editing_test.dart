import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
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
    'choosing the original destination removes synthetic upstream authority',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );
      final endpoint = work.clientEndpoints.first;
      final plan = endpoint.protocolPlans.first;

      final updated = setEnvironmentProtocolOriginalDestination(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
      );
      final nextPlan = updated.first.protocolPlans.first;
      expect(nextPlan.destination.kind, 'original');
      expect(nextPlan.destination.upstream, isNull);
      expect(nextPlan.routes, isEmpty);
    },
  );

  test('Route rejects an Account owned by another Endpoint', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final work = dashboard.environments.firstWhere(
      (value) => value.id == 'work',
    );
    final endpoint = work.clientEndpoints.first;
    final plan = endpoint.protocolPlans.first;
    final foreignAccount = dashboard.accounts.firstWhere(
      (account) => account.id == 'openai-work',
    );

    expect(
      () => assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: 'orbit-fallback',
        account: foreignAccount,
      ),
      throwsArgumentError,
    );
  });

  test(
    'Route model mapping advances only its owning authority revisions',
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

      final updated = assignEnvironmentRouteModelMappings(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        mappings: const [
          EnvironmentModelMapping(
            requestedModel: 'claude-sonnet-4-5',
            upstreamModel: 'dashscope_deepseek-v4-flash-0731',
          ),
          EnvironmentModelMapping(
            requestedModel: 'claude-opus-4-1',
            upstreamModel: 'relay/model:opaque',
          ),
        ],
      );
      final nextEndpoint = updated.first;
      final nextPlan = nextEndpoint.protocolPlans.first;
      final nextRoute = nextPlan.routes.firstWhere(
        (value) => value.id == route.id,
      );

      expect(nextEndpoint.revision, endpoint.revision + 1);
      expect(nextPlan.revision, plan.revision + 1);
      expect(nextRoute.revision, route.revision + 1);
      expect(nextRoute.modelPolicy.revision, route.modelPolicy.revision + 1);
      expect(nextRoute.modelPolicy.mode, 'map');
      expect(
        nextRoute.modelPolicy.mappings.map(
          (mapping) => '${mapping.requestedModel}->${mapping.upstreamModel}',
        ),
        [
          'claude-opus-4-1->relay/model:opaque',
          'claude-sonnet-4-5->dashscope_deepseek-v4-flash-0731',
        ],
      );
      expect(nextRoute.accountPolicy, same(route.accountPolicy));
      expect(
        nextPlan.routes.firstWhere((value) => value.id == untouched.id),
        same(untouched),
      );

      final cleared = assignEnvironmentRouteModelMappings(
        endpoints: updated,
        clientEndpointId: nextEndpoint.id,
        protocolPlanId: nextPlan.id,
        routeId: nextRoute.id,
        mappings: const [],
      );
      final clearedRoute = cleared.first.protocolPlans.first.routes.firstWhere(
        (value) => value.id == route.id,
      );
      expect(clearedRoute.modelPolicy.mode, 'passthrough');
      expect(clearedRoute.modelPolicy.mappings, isEmpty);
      expect(clearedRoute.modelPolicy.revision, route.modelPolicy.revision + 2);
    },
  );

  test(
    'draft revision normalization collapses sequential route edits to one transition',
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
      final account = dashboard.accounts.firstWhere(
        (value) => value.id == 'anthropic-lab',
      );

      final accountEdited = assignEnvironmentRouteAccount(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        account: account,
      );
      final modelEdited = assignEnvironmentRouteModelMappings(
        endpoints: accountEdited,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        mappings: const [
          EnvironmentModelMapping(
            requestedModel: 'claude-opus-4-1',
            upstreamModel: 'dashscope:deepseek-v4-flash-0731',
          ),
        ],
      );

      expect(
        modelEdited.firstWhere((value) => value.id == endpoint.id).revision,
        endpoint.revision + 2,
      );

      final normalized = normalizeEnvironmentDraftRevisions(
        base: work.clientEndpoints,
        edited: modelEdited,
      );
      final normalizedEndpoint = normalized.firstWhere(
        (value) => value.id == endpoint.id,
      );
      final normalizedPlan = normalizedEndpoint.protocolPlans.first;
      final normalizedRoute = normalizedPlan.routes.firstWhere(
        (value) => value.id == route.id,
      );

      expect(normalizedEndpoint.revision, endpoint.revision + 1);
      expect(normalizedPlan.revision, plan.revision + 1);
      expect(normalizedRoute.revision, route.revision + 1);
      expect(
        normalizedRoute.modelPolicy.mappings.single.upstreamModel,
        'dashscope:deepseek-v4-flash-0731',
      );
    },
  );

  test(
    'draft revision normalization starts every new child at revision one',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final work = dashboard.environments.firstWhere(
        (value) => value.id == 'work',
      );
      final endpoint = work.clientEndpoints.first;
      final plan = endpoint.protocolPlans.first;
      final route = plan.routes.first;
      final locallyEdited = assignEnvironmentRouteModelMappings(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        mappings: const [
          EnvironmentModelMapping(
            requestedModel: 'claude-client-alias',
            upstreamModel: 'relay/custom:model_1',
          ),
        ],
      );

      final normalized = normalizeEnvironmentDraftRevisions(
        base: const [],
        edited: [locallyEdited.first],
      );
      final normalizedEndpoint = normalized.single;
      final normalizedPlan = normalizedEndpoint.protocolPlans.first;
      final normalizedRoute = normalizedPlan.routes.first;

      expect(normalizedEndpoint.revision, 1);
      expect(normalizedPlan.revision, 1);
      expect(normalizedPlan.clientAdapterPolicy.revision, 1);
      expect(normalizedPlan.destination.upstream!.routeSet.revision, 1);
      expect(normalizedRoute.revision, 1);
      expect(normalizedRoute.accountPolicy.revision, 1);
      expect(normalizedRoute.modelPolicy.revision, 1);
      expect(
        normalizedRoute.modelPolicy.mappings.single.upstreamModel,
        'relay/custom:model_1',
      );
    },
  );

  test('Route model mapping rejects unsafe model IDs', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    final work = dashboard.environments.firstWhere(
      (value) => value.id == 'work',
    );
    final endpoint = work.clientEndpoints.first;
    final plan = endpoint.protocolPlans.first;
    final route = plan.routes.first;

    for (final invalid in ['bad\nmodel', 'bad\u007fmodel', '', 'm' * 257]) {
      expect(
        () => assignEnvironmentRouteModelMappings(
          endpoints: work.clientEndpoints,
          clientEndpointId: endpoint.id,
          protocolPlanId: plan.id,
          routeId: route.id,
          mappings: [
            EnvironmentModelMapping(
              requestedModel: 'claude-sonnet-4-5',
              upstreamModel: invalid,
            ),
          ],
        ),
        throwsArgumentError,
      );
    }

    expect(
      () => assignEnvironmentRouteModelMappings(
        endpoints: work.clientEndpoints,
        clientEndpointId: endpoint.id,
        protocolPlanId: plan.id,
        routeId: route.id,
        mappings: const [
          EnvironmentModelMapping(
            requestedModel: 'claude-sonnet-4-5',
            upstreamModel: 'model-a',
          ),
          EnvironmentModelMapping(
            requestedModel: 'claude-sonnet-4-5',
            upstreamModel: 'model-b',
          ),
        ],
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
    final foreignAccount = dashboard.accounts.firstWhere(
      (value) => value.id == 'anthropic-work',
    );

    expect(
      () => appendEnvironmentUpstreamEndpoint(
        endpoints: const [],
        upstreamEndpoint: orbit,
        account: foreignAccount,
        identityNonce: 'missing-account',
      ),
      throwsArgumentError,
    );

    final endpoints = appendEnvironmentUpstreamEndpoint(
      endpoints: const [],
      upstreamEndpoint: orbit,
      account: orbitAccount,
      identityNonce: 'owned-account',
    );
    final plan = endpoints.single.protocolPlans.single;
    final route = plan.routes.single;
    expect(plan.destination.kind, 'upstream');
    expect(route.endpointId, orbit.id);
    expect(route.accountPolicy.preferredAccountId, orbitAccount.id);
    expect(route.accountPolicy.accountRevisions, {
      orbitAccount.id: orbitAccount.revision,
    });
  });

  test('original destination has no synthetic Route or Account', () {
    final endpoints = appendEnvironmentOriginalDestination(
      endpoints: const [],
      clientOrigin: Uri.parse('https://api.anthropic.com'),
      clientProtocol: 'anthropic_messages',
      identityNonce: 'official-original',
    );
    final plan = endpoints.single.protocolPlans.single;
    expect(plan.destination.kind, 'original');
    expect(plan.destination.upstream, isNull);
    expect(plan.routes, isEmpty);
  });

  test(
    're-adding a removed client graph uses fresh child identities',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final relay = dashboard.endpoints.firstWhere(
        (value) => value.id == 'target.orbit.relay',
      );
      final account = dashboard.accounts.firstWhere(
        (value) => value.id == 'orbit-team',
      );

      final published = appendEnvironmentUpstreamEndpoint(
        endpoints: const [],
        upstreamEndpoint: relay,
        account: account,
        identityNonce: 'published-r1',
      ).single;
      final readded = appendEnvironmentUpstreamEndpoint(
        endpoints: const [],
        upstreamEndpoint: relay,
        account: account,
        identityNonce: 'draft-r3',
      ).single;

      expect(readded.clientOrigin, published.clientOrigin);
      expect(readded.id, isNot(published.id));
      expect(
        readded.protocolPlans.single.id,
        isNot(published.protocolPlans.single.id),
      );
      expect(
        readded.protocolPlans.single.routes.single.id,
        isNot(published.protocolPlans.single.routes.single.id),
      );
    },
  );

  test(
    'one multi-protocol Endpoint serves independent client protocol plans',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final relay = dashboard.endpoints.firstWhere(
        (value) => value.id == 'target.orbit.relay',
      );
      final account = dashboard.accounts.firstWhere(
        (value) => value.id == 'orbit-team',
      );

      final anthropic = appendEnvironmentUpstreamEndpoint(
        endpoints: const [],
        upstreamEndpoint: relay,
        account: account,
        clientProtocol: 'anthropic_messages',
        identityNonce: 'anthropic-flow',
      );
      final combined = appendEnvironmentUpstreamEndpoint(
        endpoints: anthropic,
        upstreamEndpoint: relay,
        account: account,
        clientProtocol: 'openai_responses',
        identityNonce: 'responses-flow',
      );

      expect(combined, hasLength(2));
      expect(combined.map((endpoint) => endpoint.clientOrigin.toString()), [
        'https://api.anthropic.com',
        'https://api.openai.com',
      ]);
      expect(
        combined.map(
          (endpoint) => endpoint.protocolPlans.single.clientProtocol,
        ),
        ['anthropic_messages', 'openai_responses'],
      );
      expect(
        combined.map(
          (endpoint) => endpoint.protocolPlans.single.routes.single.endpointId,
        ),
        everyElement(relay.id),
      );
      expect(
        combined.map(
          (endpoint) =>
              endpoint.protocolPlans.single.routes.single.backendProtocol,
        ),
        ['anthropic_messages', 'openai_responses'],
      );
    },
  );

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
      identityNonce: 'candidate-route',
    );
    final plan = endpoints.single.protocolPlans.single;
    final upstream = plan.destination.upstream!;
    final originalUpstream = originalPlan.destination.upstream!;

    expect(upstream.defaultRouteId, originalUpstream.defaultRouteId);
    expect(plan.routes, hasLength(2));
    expect(upstream.routeSet.candidateRouteIds, hasLength(2));
    expect(
      plan.routes
          .firstWhere((route) => route.endpointId == official.id)
          .accountPolicy
          .preferredAccountId,
      account.id,
    );
  });

  test(
    'Claude client traffic can target an OpenAI-compatible upstream Route',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final dashboard = await api.loadDashboard();
      final research = dashboard.environments.firstWhere(
        (value) => value.id == 'research',
      );
      final clientEndpoint = research.clientEndpoints.single;
      final originalPlan = clientEndpoint.protocolPlans.single;
      final openAI = dashboard.endpoints.firstWhere(
        (value) => value.id == 'target.openai.official',
      );
      final account = dashboard.accounts.firstWhere(
        (value) => value.id == 'openai-work',
      );

      final endpoints = appendEnvironmentUpstreamEndpoint(
        endpoints: research.clientEndpoints,
        clientEndpointId: clientEndpoint.id,
        protocolPlanId: originalPlan.id,
        upstreamEndpoint: openAI,
        account: account,
        identityNonce: 'openai-route',
      );

      expect(endpoints, hasLength(1));
      final plan = endpoints.single.protocolPlans.single;
      final upstream = plan.destination.upstream!;
      final originalUpstream = originalPlan.destination.upstream!;
      expect(plan.clientProtocol, 'anthropic_messages');
      expect(upstream.defaultRouteId, originalUpstream.defaultRouteId);
      expect(plan.routes, hasLength(2));
      final route = plan.routes.firstWhere(
        (value) => value.endpointId == openAI.id,
      );
      expect(route.backendProtocol, 'openai_chat');
      expect(route.accountPolicy.preferredAccountId, account.id);
      expect(route.modelPolicy.mode, 'passthrough');
      expect(route.wireProfileRef, 'follow-client');
    },
  );
}

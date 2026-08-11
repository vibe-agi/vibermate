import '../../core/api/control_models.dart';

bool environmentUsesUpstreamEndpoint(
  List<EnvironmentClientEndpoint> endpoints,
  String upstreamEndpointId,
) => endpoints.any(
  (endpoint) => endpoint.protocolPlans.any(
    (plan) =>
        plan.routes.any((route) => route.endpointId == upstreamEndpointId),
  ),
);

String preferredClientProtocol(UpstreamEndpoint endpoint) {
  for (final protocol in const [
    'anthropic_messages',
    'openai_responses',
    'openai_chat',
  ]) {
    if (endpoint.backendProtocols.contains(protocol)) return protocol;
  }
  throw ArgumentError.value(
    endpoint.id,
    'endpoint',
    'Upstream Endpoint has no supported semantic protocol',
  );
}

Uri clientOriginForUpstreamEndpoint(
  UpstreamEndpoint endpoint,
  String protocol,
) {
  if (endpoint.realmId == 'openai.chatgpt') {
    return Uri.parse('https://chatgpt.com');
  }
  return Uri.parse(
    protocol == 'anthropic_messages'
        ? 'https://api.anthropic.com'
        : 'https://api.openai.com',
  );
}

bool upstreamEndpointCanUseClientCredential(UpstreamEndpoint endpoint) {
  final protocol = preferredClientProtocol(endpoint);
  return endpoint.origin == clientOriginForUpstreamEndpoint(endpoint, protocol);
}

/// Adds one upstream Endpoint as a frozen Route without changing an existing
/// plan's default Route. Retargeted Endpoints require one Account they own;
/// client credentials are allowed only when the upstream authority is exactly
/// the original client authority.
List<EnvironmentClientEndpoint> appendEnvironmentUpstreamEndpoint({
  required List<EnvironmentClientEndpoint> endpoints,
  required UpstreamEndpoint upstreamEndpoint,
  required ProviderAccount? account,
}) {
  if (environmentUsesUpstreamEndpoint(endpoints, upstreamEndpoint.id)) {
    return endpoints;
  }
  if (upstreamEndpoint.state != 'active') {
    throw ArgumentError.value(
      upstreamEndpoint.id,
      'upstreamEndpoint',
      'Only active upstream Endpoints can be added',
    );
  }
  if (account != null &&
      (account.upstreamEndpointId != upstreamEndpoint.id || !account.usable)) {
    throw ArgumentError.value(
      account.id,
      'account',
      'Account is not a ready child of the selected upstream Endpoint',
    );
  }
  final protocol = preferredClientProtocol(upstreamEndpoint);
  final clientOrigin = clientOriginForUpstreamEndpoint(
    upstreamEndpoint,
    protocol,
  );
  final exactOriginal = upstreamEndpoint.origin == clientOrigin;
  if (!exactOriginal && account == null) {
    throw ArgumentError.value(
      upstreamEndpoint.id,
      'upstreamEndpoint',
      'Retargeted upstream Endpoints require an Account they own',
    );
  }
  final route = _upstreamRoute(upstreamEndpoint, protocol, account);
  final endpointIndex = endpoints.indexWhere(
    (endpoint) => endpoint.clientOrigin == clientOrigin,
  );
  if (endpointIndex < 0) {
    return List.unmodifiable([
      ...endpoints,
      _clientEndpointFor(
        upstreamEndpoint,
        protocol,
        clientOrigin,
        route,
        account,
      ),
    ]);
  }

  final updated = endpoints.indexed
      .map((entry) {
        final (index, endpoint) = entry;
        if (index != endpointIndex) return endpoint;
        final planIndex = endpoint.protocolPlans.indexWhere(
          (plan) => plan.clientProtocol == protocol,
        );
        if (planIndex < 0) {
          return EnvironmentClientEndpoint(
            id: endpoint.id,
            revision: endpoint.revision + 1,
            clientOrigin: endpoint.clientOrigin,
            protocolPlans: List.unmodifiable([
              ...endpoint.protocolPlans,
              _protocolPlanFor(
                upstreamEndpoint,
                protocol,
                clientOrigin,
                route,
                account,
              ),
            ]),
          );
        }
        final plans = endpoint.protocolPlans.indexed
            .map((planEntry) {
              final (index, plan) = planEntry;
              if (index != planIndex) return plan;
              final existingRoutes = plan.routes
                  .map((existing) {
                    if (existing.modelPolicy.mode != 'preserve') {
                      return existing;
                    }
                    return EnvironmentRoute(
                      id: existing.id,
                      revision: existing.revision + 1,
                      providerTarget: existing.providerTarget,
                      backendProtocol: existing.backendProtocol,
                      accountPolicy: existing.accountPolicy,
                      modelPolicy: EnvironmentModelPolicy(
                        revision: existing.modelPolicy.revision + 1,
                        mode: 'passthrough',
                        fixedModel: '',
                      ),
                      wireProfileRef: existing.wireProfileRef,
                      pluginBindings: existing.pluginBindings,
                    );
                  })
                  .toList(growable: false);
              final candidateIds = [
                ...plan.routeSet.candidateRouteIds,
                route.id,
              ]..sort();
              return EnvironmentProtocolPlan(
                id: plan.id,
                revision: plan.revision + 1,
                clientProtocol: plan.clientProtocol,
                clientAdapterPolicy: plan.clientAdapterPolicy,
                mode: 'managed',
                defaultRouteId: plan.defaultRouteId,
                routeSet: EnvironmentRouteSet(
                  id: plan.routeSet.id,
                  revision: plan.routeSet.revision + 1,
                  candidateRouteIds: List.unmodifiable(candidateIds),
                ),
                routes: List.unmodifiable([...existingRoutes, route]),
                pluginBindings: plan.pluginBindings,
              );
            })
            .toList(growable: false);
        return EnvironmentClientEndpoint(
          id: endpoint.id,
          revision: endpoint.revision + 1,
          clientOrigin: endpoint.clientOrigin,
          protocolPlans: List.unmodifiable(plans),
        );
      })
      .toList(growable: false);
  return List.unmodifiable(updated);
}

EnvironmentClientEndpoint _clientEndpointFor(
  UpstreamEndpoint upstream,
  String protocol,
  Uri clientOrigin,
  EnvironmentRoute route,
  ProviderAccount? account,
) {
  final token = _stableResourceToken(clientOrigin.toString());
  return EnvironmentClientEndpoint(
    id: 'endpoint.client.$token',
    revision: 1,
    clientOrigin: clientOrigin,
    protocolPlans: [
      _protocolPlanFor(upstream, protocol, clientOrigin, route, account),
    ],
  );
}

EnvironmentProtocolPlan _protocolPlanFor(
  UpstreamEndpoint upstream,
  String protocol,
  Uri clientOrigin,
  EnvironmentRoute route,
  ProviderAccount? account,
) {
  final exactOriginal = upstream.origin == clientOrigin;
  final originalPassthrough = exactOriginal && account == null;
  final token = _stableResourceToken('$clientOrigin:$protocol');
  final effectiveRoute = originalPassthrough
      ? EnvironmentRoute(
          id: route.id,
          revision: route.revision,
          providerTarget: route.providerTarget,
          backendProtocol: route.backendProtocol,
          accountPolicy: route.accountPolicy,
          modelPolicy: EnvironmentModelPolicy(
            revision: route.modelPolicy.revision,
            mode: 'preserve',
            fixedModel: '',
          ),
          wireProfileRef: route.wireProfileRef,
          pluginBindings: route.pluginBindings,
        )
      : route;
  return EnvironmentProtocolPlan(
    id: 'plan.client.$token',
    revision: 1,
    clientProtocol: protocol,
    clientAdapterPolicy: EnvironmentClientAdapterPolicy(
      id: 'adapter.client.$token',
      revision: 1,
    ),
    mode: originalPassthrough ? 'original_passthrough' : 'managed',
    defaultRouteId: route.id,
    routeSet: EnvironmentRouteSet(
      id: 'routes.client.$token',
      revision: 1,
      candidateRouteIds: [route.id],
    ),
    routes: [effectiveRoute],
    pluginBindings: const [],
  );
}

EnvironmentRoute _upstreamRoute(
  UpstreamEndpoint upstream,
  String protocol,
  ProviderAccount? account,
) {
  final token = _stableResourceToken(upstream.id);
  return EnvironmentRoute(
    id: 'route.upstream.$token',
    revision: 1,
    providerTarget: EnvironmentProviderTarget(
      id: upstream.id,
      revision: upstream.revision,
      origin: upstream.origin,
      realmId: upstream.realmId,
      capabilities: upstream.capabilities,
    ),
    backendProtocol: protocol,
    accountPolicy: _accountPolicy(account),
    modelPolicy: const EnvironmentModelPolicy(
      revision: 1,
      mode: 'passthrough',
      fixedModel: '',
    ),
    wireProfileRef: 'follow-client',
    pluginBindings: const [],
  );
}

RouteAccountPolicy _accountPolicy(ProviderAccount? account) => account == null
    ? const RouteAccountPolicy(
        revision: 1,
        mode: 'client_passthrough',
        preferredAccountId: '',
        candidateAccountIds: [],
        accountRevisions: {},
        failoverPolicy: 'off',
      )
    : RouteAccountPolicy(
        revision: 1,
        mode: 'managed',
        preferredAccountId: account.id,
        candidateAccountIds: [account.id],
        accountRevisions: {account.id: account.revision},
        failoverPolicy: 'off',
      );

String _stableResourceToken(String value) {
  var hash = 2166136261;
  for (final character in value.runes) {
    hash = ((hash ^ character) * 16777619) & 0xffffffff;
  }
  var label = value
      .toLowerCase()
      .replaceAll(RegExp('[^a-z0-9]+'), '.')
      .replaceAll(RegExp(r'^\.+|\.+$'), '');
  if (label.length > 36) label = label.substring(label.length - 36);
  if (label.isEmpty) label = 'endpoint';
  return '$label.${hash.toRadixString(16).padLeft(8, '0')}';
}

/// Returns whether [route] can legally hand the original client credential
/// through. Client credentials may never be retargeted to another authority.
bool canUseClientCredential(
  EnvironmentClientEndpoint endpoint,
  EnvironmentProtocolPlan plan,
  EnvironmentRoute route,
) =>
    route.endpointOrigin == endpoint.clientOrigin &&
    route.backendProtocol == plan.clientProtocol;

bool _canUseOriginalPassthrough(
  EnvironmentClientEndpoint endpoint,
  EnvironmentProtocolPlan plan,
  EnvironmentRoute route,
) =>
    canUseClientCredential(endpoint, plan, route) &&
    plan.routes.length == 1 &&
    plan.routeSet.candidateRouteIds.length == 1 &&
    plan.defaultRouteId == route.id &&
    route.wireProfileRef == 'follow-client';

/// Rebinds one Environment Route to an Account owned by that Route's exact
/// upstream Endpoint. Every changed child authority advances its revision.
///
/// Passing `null` means client credential passthrough and is accepted only for
/// an identity-preserving single-route plan. Retargeted and multi-route plans
/// must use an Account owned by their upstream Endpoint.
List<EnvironmentClientEndpoint> assignEnvironmentRouteAccount({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required String routeId,
  required ProviderAccount? account,
}) {
  var found = false;
  var changed = false;
  final nextEndpoints = endpoints
      .map((endpoint) {
        if (endpoint.id != clientEndpointId) return endpoint;
        var endpointChanged = false;
        final nextPlans = endpoint.protocolPlans
            .map((plan) {
              if (plan.id != protocolPlanId) return plan;
              var planFound = false;
              var planChanged = false;
              final nextRoutes = plan.routes
                  .map((route) {
                    if (route.id != routeId) return route;
                    planFound = true;
                    found = true;
                    if (account != null &&
                        account.upstreamEndpointId != route.endpointId) {
                      throw ArgumentError.value(
                        account.id,
                        'account',
                        'Account does not belong to Route Endpoint ${route.endpointId}',
                      );
                    }
                    if (account == null &&
                        !canUseClientCredential(endpoint, plan, route)) {
                      throw ArgumentError.value(
                        route.id,
                        'routeId',
                        'Client credentials cannot be retargeted or used by a multi-route plan',
                      );
                    }

                    final desiredPlanMode =
                        account == null &&
                            _canUseOriginalPassthrough(endpoint, plan, route)
                        ? 'original_passthrough'
                        : 'managed';
                    final desiredPolicy = account == null
                        ? RouteAccountPolicy(
                            revision: route.accountPolicy.revision,
                            mode: 'client_passthrough',
                            preferredAccountId: '',
                            candidateAccountIds: const [],
                            accountRevisions: const {},
                            failoverPolicy: 'off',
                          )
                        : RouteAccountPolicy(
                            revision: route.accountPolicy.revision,
                            mode: 'managed',
                            preferredAccountId: account.id,
                            candidateAccountIds: [account.id],
                            accountRevisions: {account.id: account.revision},
                            failoverPolicy: 'off',
                          );
                    final accountChanged = !_sameAccountPolicy(
                      route.accountPolicy,
                      desiredPolicy,
                    );
                    final ownershipModeChanged = plan.mode != desiredPlanMode;
                    final desiredModelMode =
                        desiredPlanMode == 'original_passthrough'
                        ? 'preserve'
                        : ownershipModeChanged ||
                              route.modelPolicy.mode == 'preserve'
                        ? 'passthrough'
                        : route.modelPolicy.mode;
                    final modelChanged =
                        route.modelPolicy.mode != desiredModelMode;
                    if (!accountChanged && !modelChanged) return route;

                    planChanged = true;
                    return EnvironmentRoute(
                      id: route.id,
                      revision: route.revision + 1,
                      providerTarget: route.providerTarget,
                      backendProtocol: route.backendProtocol,
                      accountPolicy: accountChanged
                          ? desiredPolicy.copyWith(
                              revision: route.accountPolicy.revision + 1,
                            )
                          : route.accountPolicy,
                      modelPolicy: modelChanged
                          ? EnvironmentModelPolicy(
                              revision: route.modelPolicy.revision + 1,
                              mode: desiredModelMode,
                              fixedModel: '',
                            )
                          : route.modelPolicy,
                      wireProfileRef: route.wireProfileRef,
                      pluginBindings: route.pluginBindings,
                    );
                  })
                  .toList(growable: false);
              if (!planFound) return plan;
              if (!planChanged &&
                  plan.mode ==
                      (account == null &&
                              _canUseOriginalPassthrough(
                                endpoint,
                                plan,
                                nextRoutes.firstWhere(
                                  (candidate) => candidate.id == routeId,
                                ),
                              )
                          ? 'original_passthrough'
                          : 'managed')) {
                return plan;
              }
              endpointChanged = true;
              return EnvironmentProtocolPlan(
                id: plan.id,
                revision: plan.revision + 1,
                clientProtocol: plan.clientProtocol,
                clientAdapterPolicy: plan.clientAdapterPolicy,
                mode:
                    account == null &&
                        _canUseOriginalPassthrough(
                          endpoint,
                          plan,
                          nextRoutes.firstWhere(
                            (candidate) => candidate.id == routeId,
                          ),
                        )
                    ? 'original_passthrough'
                    : 'managed',
                defaultRouteId: plan.defaultRouteId,
                routeSet: plan.routeSet,
                routes: List.unmodifiable(nextRoutes),
                pluginBindings: plan.pluginBindings,
              );
            })
            .toList(growable: false);
        if (!endpointChanged) return endpoint;
        changed = true;
        return EnvironmentClientEndpoint(
          id: endpoint.id,
          revision: endpoint.revision + 1,
          clientOrigin: endpoint.clientOrigin,
          protocolPlans: List.unmodifiable(nextPlans),
        );
      })
      .toList(growable: false);

  if (!found) {
    throw StateError(
      'Environment Route $clientEndpointId/$protocolPlanId/$routeId was not found',
    );
  }
  return changed ? List.unmodifiable(nextEndpoints) : endpoints;
}

bool _sameAccountPolicy(RouteAccountPolicy left, RouteAccountPolicy right) =>
    left.mode == right.mode &&
    left.preferredAccountId == right.preferredAccountId &&
    left.failoverPolicy == right.failoverPolicy &&
    _sameStrings(left.candidateAccountIds, right.candidateAccountIds) &&
    _sameRevisions(left.accountRevisions, right.accountRevisions);

bool _sameStrings(List<String> left, List<String> right) =>
    left.length == right.length &&
    left.indexed.every((entry) => entry.$2 == right[entry.$1]);

bool _sameRevisions(Map<String, int> left, Map<String, int> right) =>
    left.length == right.length &&
    left.entries.every((entry) => right[entry.key] == entry.value);

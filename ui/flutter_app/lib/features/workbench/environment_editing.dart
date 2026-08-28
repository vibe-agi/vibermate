import 'dart:convert';

import '../../core/api/control_models.dart';

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

/// Chooses the upstream protocol for an existing client protocol plan.
///
/// Model identifiers are deliberately not involved here. They are opaque
/// provider-owned values; only the semantic request/response protocols decide
/// whether ViberMate can bridge this Route.
String preferredBackendProtocolForClient(
  String clientProtocol,
  UpstreamEndpoint endpoint,
) {
  final candidates = switch (clientProtocol) {
    'anthropic_messages' => const ['anthropic_messages', 'openai_chat'],
    'openai_responses' => const ['openai_responses', 'openai_chat'],
    'openai_chat' => const ['openai_chat'],
    _ => const <String>[],
  };
  for (final protocol in candidates) {
    if (endpoint.backendProtocols.contains(protocol)) return protocol;
  }
  throw ArgumentError.value(
    endpoint.id,
    'upstreamEndpoint',
    'Upstream Endpoint cannot serve client protocol $clientProtocol',
  );
}

bool upstreamEndpointSupportsClientProtocol(
  UpstreamEndpoint endpoint,
  String clientProtocol,
) {
  try {
    preferredBackendProtocolForClient(clientProtocol, endpoint);
    return true;
  } on ArgumentError {
    return false;
  }
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
/// plan's default Route. Every Upstream Route requires one ready Account owned
/// by that exact Endpoint; Original Destination is a separate plan choice.
List<EnvironmentClientEndpoint> appendEnvironmentUpstreamEndpoint({
  required List<EnvironmentClientEndpoint> endpoints,
  String? clientEndpointId,
  String? protocolPlanId,
  required UpstreamEndpoint upstreamEndpoint,
  required ProviderAccount account,
  String? clientProtocol,
  Uri? clientOrigin,
  required String identityNonce,
}) {
  if ((clientEndpointId == null) != (protocolPlanId == null)) {
    throw ArgumentError(
      'clientEndpointId and protocolPlanId must be provided together',
    );
  }
  _validateIdentityNonce(identityNonce);
  if (clientProtocol != null &&
      !upstreamBackendProtocols.contains(clientProtocol)) {
    throw ArgumentError.value(
      clientProtocol,
      'clientProtocol',
      'Client protocol is unsupported',
    );
  }
  if (upstreamEndpoint.state != 'active') {
    throw ArgumentError.value(
      upstreamEndpoint.id,
      'upstreamEndpoint',
      'Only active upstream Endpoints can be added',
    );
  }
  if (account.upstreamEndpointId != upstreamEndpoint.id || !account.usable) {
    throw ArgumentError.value(
      account.id,
      'account',
      'Account is not a ready child of the selected upstream Endpoint',
    );
  }

  if (clientEndpointId != null && protocolPlanId != null) {
    final clientEndpoint = endpoints
        .where((endpoint) => endpoint.id == clientEndpointId)
        .firstOrNull;
    if (clientEndpoint == null) {
      throw ArgumentError.value(
        clientEndpointId,
        'clientEndpointId',
        'Client Endpoint does not exist in this Environment',
      );
    }
    final plan = clientEndpoint.protocolPlans
        .where((candidate) => candidate.id == protocolPlanId)
        .firstOrNull;
    if (plan == null) {
      throw ArgumentError.value(
        protocolPlanId,
        'protocolPlanId',
        'Protocol plan does not belong to the selected Client Endpoint',
      );
    }
    if (clientProtocol != null && clientProtocol != plan.clientProtocol) {
      throw ArgumentError.value(
        clientProtocol,
        'clientProtocol',
        'Client protocol does not match the selected protocol plan',
      );
    }
    if (clientOrigin != null && clientOrigin != clientEndpoint.clientOrigin) {
      throw ArgumentError.value(
        clientOrigin,
        'clientOrigin',
        'Client origin does not match the selected Client Endpoint',
      );
    }
    if (plan.routes.any((route) => route.endpointId == upstreamEndpoint.id)) {
      return endpoints;
    }
    final backendProtocol = preferredBackendProtocolForClient(
      plan.clientProtocol,
      upstreamEndpoint,
    );
    return _appendRouteToExistingPlan(
      endpoints: endpoints,
      clientEndpointId: clientEndpointId,
      protocolPlanId: protocolPlanId,
      route: _upstreamRoute(
        upstreamEndpoint,
        backendProtocol,
        account,
        identityNonce,
      ),
    );
  }

  final protocol = clientProtocol ?? preferredClientProtocol(upstreamEndpoint);
  if (!upstreamEndpointSupportsClientProtocol(upstreamEndpoint, protocol)) {
    throw ArgumentError.value(
      upstreamEndpoint.id,
      'upstreamEndpoint',
      'Upstream Endpoint cannot serve client protocol $protocol',
    );
  }
  final desiredClientOrigin =
      clientOrigin ??
      clientOriginForUpstreamEndpoint(upstreamEndpoint, protocol);
  final route = _upstreamRoute(
    upstreamEndpoint,
    protocol,
    account,
    identityNonce,
  );
  final endpointIndex = endpoints.indexWhere(
    (endpoint) => endpoint.clientOrigin == desiredClientOrigin,
  );
  if (endpointIndex < 0) {
    return List.unmodifiable([
      ...endpoints,
      _clientEndpointFor(protocol, desiredClientOrigin, route, identityNonce),
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
              _protocolPlanFor(protocol, route, identityNonce),
            ]),
          );
        }
        if (endpoint.protocolPlans[planIndex].routes.any(
          (route) => route.endpointId == upstreamEndpoint.id,
        )) {
          return endpoint;
        }
        final plans = endpoint.protocolPlans.indexed
            .map((planEntry) {
              final (index, plan) = planEntry;
              if (index != planIndex) return plan;
              return _appendUpstreamRoute(plan, route);
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

/// Adds or selects the client's Original Destination for one protocol flow.
///
/// This is the only draft operation that preserves the client origin and
/// credential. It never manufactures an Upstream Route, Endpoint, Account, or
/// model policy to represent direct traffic.
List<EnvironmentClientEndpoint> appendEnvironmentOriginalDestination({
  required List<EnvironmentClientEndpoint> endpoints,
  String? clientEndpointId,
  String? protocolPlanId,
  required Uri clientOrigin,
  required String clientProtocol,
  required String identityNonce,
}) {
  if ((clientEndpointId == null) != (protocolPlanId == null)) {
    throw ArgumentError(
      'clientEndpointId and protocolPlanId must be provided together',
    );
  }
  if (!upstreamBackendProtocols.contains(clientProtocol)) {
    throw ArgumentError.value(
      clientProtocol,
      'clientProtocol',
      'Client protocol is unsupported',
    );
  }
  _validateIdentityNonce(identityNonce);

  if (clientEndpointId != null && protocolPlanId != null) {
    final endpoint = endpoints
        .where((candidate) => candidate.id == clientEndpointId)
        .firstOrNull;
    if (endpoint == null || endpoint.clientOrigin != clientOrigin) {
      throw ArgumentError.value(
        clientEndpointId,
        'clientEndpointId',
        'Client Endpoint does not match the selected origin',
      );
    }
    final plan = endpoint.protocolPlans
        .where((candidate) => candidate.id == protocolPlanId)
        .firstOrNull;
    if (plan == null || plan.clientProtocol != clientProtocol) {
      throw ArgumentError.value(
        protocolPlanId,
        'protocolPlanId',
        'Protocol plan does not match the selected client protocol',
      );
    }
    return setEnvironmentProtocolOriginalDestination(
      endpoints: endpoints,
      clientEndpointId: clientEndpointId,
      protocolPlanId: protocolPlanId,
    );
  }

  final endpointIndex = endpoints.indexWhere(
    (endpoint) => endpoint.clientOrigin == clientOrigin,
  );
  if (endpointIndex < 0) {
    final endpointToken = _stableResourceToken('$clientOrigin:$identityNonce');
    return List.unmodifiable([
      ...endpoints,
      EnvironmentClientEndpoint(
        id: 'endpoint.client.$endpointToken',
        revision: 1,
        clientOrigin: clientOrigin,
        protocolPlans: [
          _originalProtocolPlan(clientProtocol, clientOrigin, identityNonce),
        ],
      ),
    ]);
  }

  final endpoint = endpoints[endpointIndex];
  final existing = endpoint.protocolPlans
      .where((plan) => plan.clientProtocol == clientProtocol)
      .firstOrNull;
  if (existing != null) {
    return setEnvironmentProtocolOriginalDestination(
      endpoints: endpoints,
      clientEndpointId: endpoint.id,
      protocolPlanId: existing.id,
    );
  }
  final updated = List<EnvironmentClientEndpoint>.of(endpoints);
  updated[endpointIndex] = EnvironmentClientEndpoint(
    id: endpoint.id,
    revision: endpoint.revision + 1,
    clientOrigin: endpoint.clientOrigin,
    protocolPlans: List.unmodifiable([
      ...endpoint.protocolPlans,
      _originalProtocolPlan(clientProtocol, clientOrigin, identityNonce),
    ]),
  );
  return List.unmodifiable(updated);
}

EnvironmentProtocolPlan _originalProtocolPlan(
  String protocol,
  Uri clientOrigin,
  String identityNonce,
) {
  final token = _stableResourceToken('$clientOrigin:$protocol:$identityNonce');
  return EnvironmentProtocolPlan(
    id: 'plan.client.$token',
    revision: 1,
    clientProtocol: protocol,
    clientAdapterPolicy: EnvironmentClientAdapterPolicy(
      id: 'adapter.client.$token',
      revision: 1,
    ),
    destination: const EnvironmentDestination.original(),
    egressProfile: EgressProfileRevision.direct,
    transforms: const [],
    pluginBindings: const [],
  );
}

void _validateIdentityNonce(String identityNonce) {
  if (identityNonce.isEmpty ||
      utf8.encode(identityNonce).length > 128 ||
      RegExp(r'[\u0000-\u001f\u007f-\u009f]').hasMatch(identityNonce)) {
    throw ArgumentError.value(
      identityNonce,
      'identityNonce',
      'Environment child identity nonce is invalid',
    );
  }
}

List<EnvironmentClientEndpoint> _appendRouteToExistingPlan({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required EnvironmentRoute route,
}) {
  return List.unmodifiable([
    for (final endpoint in endpoints)
      if (endpoint.id != clientEndpointId)
        endpoint
      else
        EnvironmentClientEndpoint(
          id: endpoint.id,
          revision: endpoint.revision + 1,
          clientOrigin: endpoint.clientOrigin,
          protocolPlans: List.unmodifiable([
            for (final plan in endpoint.protocolPlans)
              if (plan.id != protocolPlanId)
                plan
              else
                _appendUpstreamRoute(plan, route),
          ]),
        ),
  ]);
}

EnvironmentProtocolPlan _appendUpstreamRoute(
  EnvironmentProtocolPlan plan,
  EnvironmentRoute route,
) {
  final upstream = plan.destination.upstream;
  final routes = List<EnvironmentRoute>.unmodifiable([
    ...?upstream?.routes,
    route,
  ]);
  final candidateIds = routes.map((value) => value.id).toList()..sort();
  final routeSet = upstream?.routeSet;
  return EnvironmentProtocolPlan(
    id: plan.id,
    revision: plan.revision + 1,
    clientProtocol: plan.clientProtocol,
    clientAdapterPolicy: plan.clientAdapterPolicy,
    destination: EnvironmentDestination.upstream(
      EnvironmentUpstreamPlan(
        defaultRouteId: upstream?.defaultRouteId ?? route.id,
        routeSet: EnvironmentRouteSet(
          id:
              routeSet?.id ??
              'routes.client.${_stableResourceToken('${plan.id}:routes')}',
          revision: routeSet == null ? 1 : routeSet.revision + 1,
          candidateRouteIds: List.unmodifiable(candidateIds),
        ),
        routes: routes,
      ),
    ),
    egressProfile: plan.egressProfile,
    transforms: plan.transforms,
    pluginBindings: plan.pluginBindings,
  );
}

EnvironmentClientEndpoint _clientEndpointFor(
  String protocol,
  Uri clientOrigin,
  EnvironmentRoute route,
  String identityNonce,
) {
  final token = _stableResourceToken('$clientOrigin:$identityNonce');
  return EnvironmentClientEndpoint(
    id: 'endpoint.client.$token',
    revision: 1,
    clientOrigin: clientOrigin,
    protocolPlans: [_protocolPlanFor(protocol, route, identityNonce)],
  );
}

EnvironmentProtocolPlan _protocolPlanFor(
  String protocol,
  EnvironmentRoute route,
  String identityNonce,
) {
  final token = _stableResourceToken('$protocol:$identityNonce');
  return EnvironmentProtocolPlan(
    id: 'plan.client.$token',
    revision: 1,
    clientProtocol: protocol,
    clientAdapterPolicy: EnvironmentClientAdapterPolicy(
      id: 'adapter.client.$token',
      revision: 1,
    ),
    destination: EnvironmentDestination.upstream(
      EnvironmentUpstreamPlan(
        defaultRouteId: route.id,
        routeSet: EnvironmentRouteSet(
          id: 'routes.client.$token',
          revision: 1,
          candidateRouteIds: [route.id],
        ),
        routes: [route],
      ),
    ),
    egressProfile: EgressProfileRevision.direct,
    transforms: const [],
    pluginBindings: const [],
  );
}

EnvironmentRoute _upstreamRoute(
  UpstreamEndpoint upstream,
  String protocol,
  ProviderAccount account,
  String identityNonce,
) {
  final token = _stableResourceToken('${upstream.id}:$identityNonce');
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
      mappings: [],
    ),
    wireProfileRef: 'follow-client',
    pluginBindings: const [],
  );
}

RouteAccountPolicy _accountPolicy(ProviderAccount account) =>
    RouteAccountPolicy(
      revision: 1,
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

/// Replaces the network path of exactly one client protocol flow.
///
/// Egress belongs to the protocol plan because one Environment may route its
/// Anthropic and OpenAI traffic through different networks. Destination and
/// Route authority are intentionally untouched.
List<EnvironmentClientEndpoint> assignEnvironmentProtocolEgressProfile({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required EgressProfileRevision profile,
}) {
  final validated = EgressProfileRevision.fromJson(
    profile.toJson(),
    'egressProfile',
  );
  var found = false;
  var changed = false;
  final result = <EnvironmentClientEndpoint>[];
  for (final endpoint in endpoints) {
    if (endpoint.id != clientEndpointId) {
      result.add(endpoint);
      continue;
    }
    var endpointChanged = false;
    final plans = <EnvironmentProtocolPlan>[];
    for (final plan in endpoint.protocolPlans) {
      if (plan.id != protocolPlanId) {
        plans.add(plan);
        continue;
      }
      found = true;
      if (plan.egressProfile == validated) {
        plans.add(plan);
        continue;
      }
      endpointChanged = true;
      plans.add(
        EnvironmentProtocolPlan(
          id: plan.id,
          revision: plan.revision + 1,
          clientProtocol: plan.clientProtocol,
          clientAdapterPolicy: plan.clientAdapterPolicy,
          destination: plan.destination,
          egressProfile: validated,
          transforms: plan.transforms,
          pluginBindings: plan.pluginBindings,
        ),
      );
    }
    if (!endpointChanged) {
      result.add(endpoint);
      continue;
    }
    changed = true;
    result.add(
      EnvironmentClientEndpoint(
        id: endpoint.id,
        revision: endpoint.revision + 1,
        clientOrigin: endpoint.clientOrigin,
        protocolPlans: List.unmodifiable(plans),
      ),
    );
  }
  if (!found) {
    throw StateError(
      'Environment protocol plan $clientEndpointId/$protocolPlanId was not found',
    );
  }
  return changed ? List.unmodifiable(result) : endpoints;
}

/// Replaces the ordered, published transforms of one client protocol flow.
///
/// The strict JSON round-trip keeps editor state on the same contract as a
/// server draft. Destination, Route, Account, model, and egress authority are
/// intentionally preserved.
List<EnvironmentClientEndpoint> assignEnvironmentProtocolTransforms({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required List<CodeLibraryTransformRevision> transforms,
}) {
  final validated = List<CodeLibraryTransformRevision>.unmodifiable(
    transforms.indexed.map(
      (entry) => CodeLibraryTransformRevision.fromJson(
        entry.$2.toJson(),
        'transforms[${entry.$1}]',
      ),
    ),
  );
  var found = false;
  var changed = false;
  final result = <EnvironmentClientEndpoint>[];
  for (final endpoint in endpoints) {
    if (endpoint.id != clientEndpointId) {
      result.add(endpoint);
      continue;
    }
    var endpointChanged = false;
    final plans = <EnvironmentProtocolPlan>[];
    for (final plan in endpoint.protocolPlans) {
      if (plan.id != protocolPlanId) {
        plans.add(plan);
        continue;
      }
      found = true;
      if (_sameTransformRevisions(plan.transforms, validated)) {
        plans.add(plan);
        continue;
      }
      endpointChanged = true;
      plans.add(
        EnvironmentProtocolPlan(
          id: plan.id,
          revision: plan.revision + 1,
          clientProtocol: plan.clientProtocol,
          clientAdapterPolicy: plan.clientAdapterPolicy,
          destination: plan.destination,
          egressProfile: plan.egressProfile,
          transforms: validated,
          pluginBindings: plan.pluginBindings,
        ),
      );
    }
    if (!endpointChanged) {
      result.add(endpoint);
      continue;
    }
    changed = true;
    result.add(
      EnvironmentClientEndpoint(
        id: endpoint.id,
        revision: endpoint.revision + 1,
        clientOrigin: endpoint.clientOrigin,
        protocolPlans: List.unmodifiable(plans),
      ),
    );
  }
  if (!found) {
    throw StateError(
      'Environment protocol plan $clientEndpointId/$protocolPlanId was not found',
    );
  }
  return changed ? List.unmodifiable(result) : endpoints;
}

/// Selects the client's original destination for one protocol flow.
///
/// Original is a complete destination choice, not an Account option and not a
/// synthetic upstream Route. Switching to it deliberately removes all upstream
/// Route, Account, and model-mapping authority from this protocol plan.
List<EnvironmentClientEndpoint> setEnvironmentProtocolOriginalDestination({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
}) {
  var found = false;
  var changed = false;
  final result = <EnvironmentClientEndpoint>[];
  for (final endpoint in endpoints) {
    if (endpoint.id != clientEndpointId) {
      result.add(endpoint);
      continue;
    }
    var endpointChanged = false;
    final plans = <EnvironmentProtocolPlan>[];
    for (final plan in endpoint.protocolPlans) {
      if (plan.id != protocolPlanId) {
        plans.add(plan);
        continue;
      }
      found = true;
      if (plan.destination.isOriginal) {
        plans.add(plan);
        continue;
      }
      endpointChanged = true;
      plans.add(
        EnvironmentProtocolPlan(
          id: plan.id,
          revision: plan.revision + 1,
          clientProtocol: plan.clientProtocol,
          clientAdapterPolicy: plan.clientAdapterPolicy,
          destination: const EnvironmentDestination.original(),
          egressProfile: plan.egressProfile,
          transforms: plan.transforms,
          pluginBindings: plan.pluginBindings,
        ),
      );
    }
    if (!endpointChanged) {
      result.add(endpoint);
      continue;
    }
    changed = true;
    result.add(
      EnvironmentClientEndpoint(
        id: endpoint.id,
        revision: endpoint.revision + 1,
        clientOrigin: endpoint.clientOrigin,
        protocolPlans: List.unmodifiable(plans),
      ),
    );
  }
  if (!found) {
    throw StateError(
      'Environment protocol plan $clientEndpointId/$protocolPlanId was not found',
    );
  }
  return changed ? List.unmodifiable(result) : endpoints;
}

/// Rebinds one Environment Route to an Account owned by that Route's exact
/// upstream Endpoint. Every changed child authority advances its revision.
List<EnvironmentClientEndpoint> assignEnvironmentRouteAccount({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required String routeId,
  required ProviderAccount account,
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
              final upstream = plan.destination.upstream;
              if (upstream == null) return plan;
              final routeIndex = upstream.routes.indexWhere(
                (route) => route.id == routeId,
              );
              if (routeIndex < 0) return plan;
              found = true;
              final route = upstream.routes[routeIndex];
              if (account.upstreamEndpointId != route.endpointId ||
                  !account.usable) {
                throw ArgumentError.value(
                  account.id,
                  'account',
                  'Account is not a ready child of Route Endpoint ${route.endpointId}',
                );
              }
              final desiredPolicy = RouteAccountPolicy(
                revision: route.accountPolicy.revision,
                preferredAccountId: account.id,
                candidateAccountIds: [account.id],
                accountRevisions: {account.id: account.revision},
                failoverPolicy: 'off',
              );
              if (_sameAccountPolicy(route.accountPolicy, desiredPolicy)) {
                return plan;
              }
              final nextRoutes = List<EnvironmentRoute>.of(upstream.routes);
              nextRoutes[routeIndex] = EnvironmentRoute(
                id: route.id,
                revision: route.revision + 1,
                providerTarget: route.providerTarget,
                backendProtocol: route.backendProtocol,
                accountPolicy: desiredPolicy.copyWith(
                  revision: route.accountPolicy.revision + 1,
                ),
                modelPolicy: route.modelPolicy,
                wireProfileRef: route.wireProfileRef,
                pluginBindings: route.pluginBindings,
              );
              endpointChanged = true;
              return EnvironmentProtocolPlan(
                id: plan.id,
                revision: plan.revision + 1,
                clientProtocol: plan.clientProtocol,
                clientAdapterPolicy: plan.clientAdapterPolicy,
                destination: EnvironmentDestination.upstream(
                  EnvironmentUpstreamPlan(
                    defaultRouteId: upstream.defaultRouteId,
                    routeSet: upstream.routeSet,
                    routes: List.unmodifiable(nextRoutes),
                  ),
                ),
                egressProfile: plan.egressProfile,
                transforms: plan.transforms,
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

/// Replaces the exact request-model to upstream-model mappings for one Route.
///
/// Both sides are opaque model identifiers. An empty list restores the plan's
/// transparent behavior. Only the Route and its owning plan/endpoint revisions
/// advance.
List<EnvironmentClientEndpoint> assignEnvironmentRouteModelMappings({
  required List<EnvironmentClientEndpoint> endpoints,
  required String clientEndpointId,
  required String protocolPlanId,
  required String routeId,
  required List<EnvironmentModelMapping> mappings,
}) {
  final normalizedMappings = [...mappings]
    ..sort((left, right) {
      final requested = left.requestedModel.compareTo(right.requestedModel);
      return requested != 0
          ? requested
          : left.upstreamModel.compareTo(right.upstreamModel);
    });
  final requestedModels = <String>{};
  for (final mapping in normalizedMappings) {
    _validateModelId(mapping.requestedModel, 'requestedModel');
    _validateModelId(mapping.upstreamModel, 'upstreamModel');
    if (!requestedModels.add(mapping.requestedModel)) {
      throw ArgumentError.value(
        mapping.requestedModel,
        'mappings',
        'Each requested model may be mapped only once',
      );
    }
  }
  final desiredMappings = List<EnvironmentModelMapping>.unmodifiable(
    normalizedMappings,
  );

  var found = false;
  var changed = false;
  final nextEndpoints = endpoints
      .map((endpoint) {
        if (endpoint.id != clientEndpointId) return endpoint;
        var endpointChanged = false;
        final nextPlans = endpoint.protocolPlans
            .map((plan) {
              if (plan.id != protocolPlanId) return plan;
              final upstream = plan.destination.upstream;
              if (upstream == null) return plan;
              var planChanged = false;
              final desiredMode = desiredMappings.isNotEmpty
                  ? 'map'
                  : 'passthrough';
              final nextRoutes = upstream.routes
                  .map((route) {
                    if (route.id != routeId) return route;
                    found = true;
                    if (route.modelPolicy.mode == desiredMode &&
                        _sameModelMappings(
                          route.modelPolicy.mappings,
                          desiredMappings,
                        )) {
                      return route;
                    }
                    planChanged = true;
                    return EnvironmentRoute(
                      id: route.id,
                      revision: route.revision + 1,
                      providerTarget: route.providerTarget,
                      backendProtocol: route.backendProtocol,
                      accountPolicy: route.accountPolicy,
                      modelPolicy: EnvironmentModelPolicy(
                        revision: route.modelPolicy.revision + 1,
                        mode: desiredMode,
                        mappings: desiredMappings,
                      ),
                      wireProfileRef: route.wireProfileRef,
                      pluginBindings: route.pluginBindings,
                    );
                  })
                  .toList(growable: false);
              if (!planChanged) return plan;
              endpointChanged = true;
              return EnvironmentProtocolPlan(
                id: plan.id,
                revision: plan.revision + 1,
                clientProtocol: plan.clientProtocol,
                clientAdapterPolicy: plan.clientAdapterPolicy,
                destination: EnvironmentDestination.upstream(
                  EnvironmentUpstreamPlan(
                    defaultRouteId: upstream.defaultRouteId,
                    routeSet: upstream.routeSet,
                    routes: List.unmodifiable(nextRoutes),
                  ),
                ),
                egressProfile: plan.egressProfile,
                transforms: plan.transforms,
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

void _validateModelId(String value, String name) {
  if (value.isEmpty ||
      utf8.encode(value).length > 256 ||
      RegExp(r'[\u0000-\u001f\u007f-\u009f\ufeff]').hasMatch(value)) {
    throw ArgumentError.value(
      value,
      name,
      'Model ID must be exact UTF-8 text of at most 256 bytes without control characters',
    );
  }
}

/// Rebuilds an edited Environment graph against its published base so every
/// changed authority advances exactly once, regardless of how many local UI
/// gestures produced the candidate.
///
/// Editing helpers intentionally advance revisions immediately. A user may,
/// however, change an Account and then choose a model before reviewing the
/// draft. Sending those cumulative `+2` revisions would violate the control
/// plane transition contract even though this is still one draft. This is the
/// single boundary where local edits become one atomic candidate.
List<EnvironmentClientEndpoint> normalizeEnvironmentDraftRevisions({
  required List<EnvironmentClientEndpoint> base,
  required List<EnvironmentClientEndpoint> edited,
}) {
  final baseById = {for (final endpoint in base) endpoint.id: endpoint};
  return List.unmodifiable(
    edited.map(
      (endpoint) => _normalizeClientEndpoint(baseById[endpoint.id], endpoint),
    ),
  );
}

EnvironmentClientEndpoint _normalizeClientEndpoint(
  EnvironmentClientEndpoint? base,
  EnvironmentClientEndpoint edited,
) {
  if (base == null) {
    return EnvironmentClientEndpoint(
      id: edited.id,
      revision: 1,
      clientOrigin: edited.clientOrigin,
      protocolPlans: List.unmodifiable(
        edited.protocolPlans.map((plan) => _normalizeProtocolPlan(null, plan)),
      ),
    );
  }

  final basePlans = {for (final plan in base.protocolPlans) plan.id: plan};
  final plans = List<EnvironmentProtocolPlan>.unmodifiable(
    edited.protocolPlans.map(
      (plan) => _normalizeProtocolPlan(basePlans[plan.id], plan),
    ),
  );
  final candidate = EnvironmentClientEndpoint(
    id: edited.id,
    revision: base.revision,
    clientOrigin: edited.clientOrigin,
    protocolPlans: plans,
  );
  return EnvironmentClientEndpoint(
    id: candidate.id,
    revision:
        base.revision + (_sameJson(base.toJson(), candidate.toJson()) ? 0 : 1),
    clientOrigin: candidate.clientOrigin,
    protocolPlans: candidate.protocolPlans,
  );
}

EnvironmentProtocolPlan _normalizeProtocolPlan(
  EnvironmentProtocolPlan? base,
  EnvironmentProtocolPlan edited,
) {
  final baseUpstream = base?.destination.upstream;
  final editedUpstream = edited.destination.upstream;
  final destination = editedUpstream == null
      ? const EnvironmentDestination.original()
      : EnvironmentDestination.upstream(
          EnvironmentUpstreamPlan(
            defaultRouteId: editedUpstream.defaultRouteId,
            routeSet: baseUpstream == null
                ? EnvironmentRouteSet(
                    id: editedUpstream.routeSet.id,
                    revision: 1,
                    candidateRouteIds:
                        editedUpstream.routeSet.candidateRouteIds,
                  )
                : _normalizeRouteSet(
                    baseUpstream.routeSet,
                    editedUpstream.routeSet,
                  ),
            routes: List<EnvironmentRoute>.unmodifiable(
              editedUpstream.routes.map((route) {
                final previous = baseUpstream?.routes
                    .where((value) => value.id == route.id)
                    .firstOrNull;
                return _normalizeRoute(previous, route);
              }),
            ),
          ),
        );
  final adapter = base == null
      ? EnvironmentClientAdapterPolicy(
          id: edited.clientAdapterPolicy.id,
          revision: 1,
        )
      : _normalizeClientAdapter(
          base.clientAdapterPolicy,
          edited.clientAdapterPolicy,
        );
  final bindings = _normalizePluginBindings(
    base?.pluginBindings ?? const [],
    edited.pluginBindings,
  );
  if (base == null) {
    return EnvironmentProtocolPlan(
      id: edited.id,
      revision: 1,
      clientProtocol: edited.clientProtocol,
      clientAdapterPolicy: adapter,
      destination: destination,
      egressProfile: edited.egressProfile,
      transforms: edited.transforms,
      pluginBindings: bindings,
    );
  }
  final candidate = EnvironmentProtocolPlan(
    id: edited.id,
    revision: base.revision,
    clientProtocol: edited.clientProtocol,
    clientAdapterPolicy: adapter,
    destination: destination,
    egressProfile: edited.egressProfile,
    transforms: edited.transforms,
    pluginBindings: bindings,
  );
  return EnvironmentProtocolPlan(
    id: candidate.id,
    revision:
        base.revision + (_sameJson(base.toJson(), candidate.toJson()) ? 0 : 1),
    clientProtocol: candidate.clientProtocol,
    clientAdapterPolicy: candidate.clientAdapterPolicy,
    destination: candidate.destination,
    egressProfile: candidate.egressProfile,
    transforms: candidate.transforms,
    pluginBindings: candidate.pluginBindings,
  );
}

EnvironmentClientAdapterPolicy _normalizeClientAdapter(
  EnvironmentClientAdapterPolicy base,
  EnvironmentClientAdapterPolicy edited,
) => EnvironmentClientAdapterPolicy(
  id: edited.id,
  revision: edited.id == base.id ? base.revision : 1,
);

EnvironmentRouteSet _normalizeRouteSet(
  EnvironmentRouteSet base,
  EnvironmentRouteSet edited,
) {
  if (base.id != edited.id) {
    return EnvironmentRouteSet(
      id: edited.id,
      revision: 1,
      candidateRouteIds: edited.candidateRouteIds,
    );
  }
  return EnvironmentRouteSet(
    id: edited.id,
    revision:
        base.revision +
        (_sameStrings(base.candidateRouteIds, edited.candidateRouteIds)
            ? 0
            : 1),
    candidateRouteIds: edited.candidateRouteIds,
  );
}

EnvironmentRoute _normalizeRoute(
  EnvironmentRoute? base,
  EnvironmentRoute edited,
) {
  if (base == null) {
    return EnvironmentRoute(
      id: edited.id,
      revision: 1,
      providerTarget: edited.providerTarget,
      backendProtocol: edited.backendProtocol,
      accountPolicy: edited.accountPolicy.copyWith(revision: 1),
      modelPolicy: EnvironmentModelPolicy(
        revision: 1,
        mode: edited.modelPolicy.mode,
        mappings: edited.modelPolicy.mappings,
      ),
      wireProfileRef: edited.wireProfileRef,
      pluginBindings: _normalizePluginBindings(const [], edited.pluginBindings),
    );
  }

  final accountPolicy = edited.accountPolicy.copyWith(
    revision:
        base.accountPolicy.revision +
        (_sameAccountPolicy(base.accountPolicy, edited.accountPolicy) ? 0 : 1),
  );
  final modelPolicy = EnvironmentModelPolicy(
    revision:
        base.modelPolicy.revision +
        (_sameModelPolicy(base.modelPolicy, edited.modelPolicy) ? 0 : 1),
    mode: edited.modelPolicy.mode,
    mappings: edited.modelPolicy.mappings,
  );
  final bindings = _normalizePluginBindings(
    base.pluginBindings,
    edited.pluginBindings,
  );
  final candidate = EnvironmentRoute(
    id: edited.id,
    revision: base.revision,
    providerTarget: edited.providerTarget,
    backendProtocol: edited.backendProtocol,
    accountPolicy: accountPolicy,
    modelPolicy: modelPolicy,
    wireProfileRef: edited.wireProfileRef,
    pluginBindings: bindings,
  );
  return EnvironmentRoute(
    id: candidate.id,
    revision:
        base.revision + (_sameJson(base.toJson(), candidate.toJson()) ? 0 : 1),
    providerTarget: candidate.providerTarget,
    backendProtocol: candidate.backendProtocol,
    accountPolicy: candidate.accountPolicy,
    modelPolicy: candidate.modelPolicy,
    wireProfileRef: candidate.wireProfileRef,
    pluginBindings: candidate.pluginBindings,
  );
}

List<EnvironmentPluginBinding> _normalizePluginBindings(
  List<EnvironmentPluginBinding> base,
  List<EnvironmentPluginBinding> edited,
) {
  final baseById = {for (final binding in base) binding.id: binding};
  return List.unmodifiable(
    edited.map((binding) {
      final previous = baseById[binding.id];
      return EnvironmentPluginBinding(
        id: binding.id,
        revision: previous == null
            ? 1
            : previous.revision +
                  (previous.pluginId == binding.pluginId ? 0 : 1),
        pluginId: binding.pluginId,
      );
    }),
  );
}

bool _sameModelPolicy(
  EnvironmentModelPolicy left,
  EnvironmentModelPolicy right,
) =>
    left.mode == right.mode &&
    _sameModelMappings(left.mappings, right.mappings);

bool _sameModelMappings(
  List<EnvironmentModelMapping> left,
  List<EnvironmentModelMapping> right,
) =>
    left.length == right.length &&
    left.indexed.every(
      (entry) =>
          entry.$2.requestedModel == right[entry.$1].requestedModel &&
          entry.$2.upstreamModel == right[entry.$1].upstreamModel,
    );

bool _sameJson(Map<String, Object?> left, Map<String, Object?> right) =>
    jsonEncode(left) == jsonEncode(right);

bool _sameAccountPolicy(RouteAccountPolicy left, RouteAccountPolicy right) =>
    left.preferredAccountId == right.preferredAccountId &&
    left.failoverPolicy == right.failoverPolicy &&
    _sameStrings(left.candidateAccountIds, right.candidateAccountIds) &&
    _sameRevisions(left.accountRevisions, right.accountRevisions);

bool _sameStrings(List<String> left, List<String> right) =>
    left.length == right.length &&
    left.indexed.every((entry) => entry.$2 == right[entry.$1]);

bool _sameTransformRevisions(
  List<CodeLibraryTransformRevision> left,
  List<CodeLibraryTransformRevision> right,
) =>
    left.length == right.length &&
    left.indexed.every((entry) => entry.$2 == right[entry.$1]);

bool _sameRevisions(Map<String, int> left, Map<String, int> right) =>
    left.length == right.length &&
    left.entries.every((entry) => right[entry.key] == entry.value);

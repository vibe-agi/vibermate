import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';
import 'egress_legacy_fixture.dart';

void main() {
  test(
    'New authored original flow inherits instead of silently opting out',
    () {
      final endpoints = appendEnvironmentOriginalDestination(
        endpoints: const [],
        clientOrigin: Uri.parse('https://chatgpt.com'),
        clientProtocol: 'openai_responses',
        identityNonce: 'synthetic-new-flow',
      );
      expect(endpoints.single.protocolPlans.single.egressProfile.toJson(), {
        'mode': 'inherit',
      });
    },
  );
  test(
    'Dry-run unresolved exit accepts only marked empty ID and zero revision',
    () {
      final wire = <String, Object?>{
        'evaluatedAt': '2026-10-06T00:00:00Z',
        'environmentId': 'preview',
        'environmentRevision': 1,
        'environmentDigest': List.filled(64, 'a').join(),
        'destinationKind': 'original',
        'providerOrigin': 'https://chatgpt.com',
        'routeId': '',
        'routeRevision': 0,
        'accountId': '',
        'accountRevision': 0,
        'requestedModel': 'synthetic',
        'effectiveModel': 'synthetic',
        'modelMapped': false,
        'networkExitId': '',
        'networkExitRevision': 0,
        'providerMethod': 'POST',
        'providerPath': '/v1/responses',
        'bodyChanged': false,
        'protocolChangedTopLevelFields': <String>[],
        'changedHeaderNames': <String>[],
        'changedTopLevelFields': <String>[],
        'unverified': <String>['network_exit'],
      };
      final result = EnvironmentDryRunDecision.fromJson(wire);
      expect(result.networkExitId, isEmpty);
      for (final invalid in <Map<String, Object?>>[
        {...wire, 'unverified': <String>[]},
        {...wire, 'networkExitRevision': 1},
        {...wire, 'networkExitId': 'profile.direct'},
      ]) {
        expect(
          () => EnvironmentDryRunDecision.fromJson(invalid),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );
  test('Authored inherit roundtrips without becoming explicit Direct', () {
    final wire = _modelWire(_freshLegacy());
    wire['egressProfile'] = <String, Object?>{'mode': 'inherit'};
    expect(EgressChoice.fromJson(wire['egressProfile'], 'choice').toJson(), {
      'mode': 'inherit',
    });
    final parsed = EnvironmentProtocolPlan.fromJson(wire, 'plan');
    expect(parsed.toJson()['egressProfile'], <String, Object?>{
      'mode': 'inherit',
    });
    expect(
      EnvironmentProtocolPlan.fromJson(
        parsed.toJson(),
        'plan',
      ).egressProfile.toJson(),
      {'mode': 'inherit'},
    );
    final nativeWire = _nativeWire(_freshLegacy());
    nativeWire['egressProfile'] = <String, Object?>{'mode': 'inherit'};
    final native = EnvironmentClientToolRoute.fromJson(nativeWire, 'route');
    expect(native.toJson()['egressProfile'], <String, Object?>{
      'mode': 'inherit',
    });
    expect(
      EnvironmentClientToolRoute.fromJson(
        native.toJson(),
        'route',
      ).egressProfile.toJson(),
      {'mode': 'inherit'},
    );
    // Mutation in either projection never changes the shared input or another
    // fresh projection's explicit authored exit.
    _expectDirect(
      EgressChoice.fromJson(
        _modelWire(_freshLegacy())['egressProfile'],
        'fresh',
      ),
    );
    _expectDirect(
      EgressChoice.fromJson(
        _nativeWire(_freshLegacy())['egressProfile'],
        'fresh',
      ),
    );
  });
  test(
    'Legacy explicit Direct stays explicit in Choice and both model plans and native route',
    () {
      for (var index = 0; index < 2; index++) {
        final wire = _modelWire(_freshLegacy(), index);
        final choice = EgressChoice.fromJson(wire['egressProfile'], 'choice');
        _expectDirect(choice);
        _expectDirect(EgressChoice.fromJson(choice.toJson(), 'choice'));
        final plan = EnvironmentProtocolPlan.fromJson(wire, 'plan');
        _expectDirect(plan.egressProfile);
        _expectDirect(
          EnvironmentProtocolPlan.fromJson(plan.toJson(), 'plan').egressProfile,
        );
      }
      final route = EnvironmentClientToolRoute.fromJson(
        _nativeWire(_freshLegacy()),
        'route',
      );
      _expectDirect(route.egressProfile);
      _expectDirect(
        EnvironmentClientToolRoute.fromJson(
          route.toJson(),
          'route',
        ).egressProfile,
      );
    },
  );
  for (final shape in ['empty', 'null', 'mixed']) {
    test('Choice and actual model/native DTO reject $shape authored shape', () {
      final Object? invalid = switch (shape) {
        'empty' => <String, Object?>{},
        'null' => null,
        _ => <String, Object?>{
          ..._modelWire(_freshLegacy())['egressProfile']
              as Map<String, dynamic>,
          'mode': 'inherit',
        },
      };
      expect(
        () => EgressChoice.fromJson(invalid, 'choice'),
        throwsA(isA<ControlContractException>()),
      );
      final model = _modelWire(_freshLegacy())..['egressProfile'] = invalid;
      final native = _nativeWire(_freshLegacy())..['egressProfile'] = invalid;
      expect(
        () => EnvironmentProtocolPlan.fromJson(model, 'plan'),
        throwsA(isA<ControlContractException>()),
      );
      expect(
        () => EnvironmentClientToolRoute.fromJson(native, 'route'),
        throwsA(isA<ControlContractException>()),
      );
    });
  }
}

Map<String, dynamic> _freshLegacy() =>
    jsonDecode(egressLegacyDirectJSON) as Map<String, dynamic>;

Map<String, dynamic> _modelWire(Map<String, dynamic> legacy, [int index = 0]) {
  final wire =
      legacy['clientEndpoints'][0]['protocolPlans'][index]
          as Map<String, dynamic>;
  // Existing owner-control DTO projection only; keep the original exit intact.
  wire['pluginBindings'] = <Object?>[];
  wire['transforms'] = <Object?>[];
  wire['destination'] = <String, Object?>{'kind': 'original'};
  return wire;
}

Map<String, dynamic> _nativeWire(Map<String, dynamic> legacy) =>
    legacy['clientToolAccess']['route'] as Map<String, dynamic>;

void _expectDirect(EgressChoice choice) {
  expect(choice, isA<EgressProfileRevision>());
  final profile = choice as EgressProfileRevision;
  expect(profile.id, 'profile.direct');
  expect(profile.revision, 1);
  expect(profile.displayName, 'Direct · System DNS');
  expect(profile.policy.proxy.kind, 'direct');
  expect(profile.policy.resolver.kind, 'system');
  expect(profile.policy.resolver.transport, 'direct');
  expect(profile.publishedAt, DateTime.utc(1970));
  expect(choice.toJson().containsKey('mode'), isFalse);
}

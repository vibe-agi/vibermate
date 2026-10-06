import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';

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
    final legacy =
        jsonDecode(
              File(
                '../../internal/environment/testdata/egress_legacy_938_direct.json',
              ).readAsStringSync(),
            )
            as Map<String, dynamic>;
    final wire =
        legacy['clientEndpoints'][0]['protocolPlans'][0]
            as Map<String, dynamic>;
    // The owner-control DTO projects optional internal nil slices as arrays.
    wire['pluginBindings'] = <Object?>[];
    wire['transforms'] = <Object?>[];
    wire['destination'] = <String, Object?>{'kind': 'original'};
    wire['egressProfile'] = <String, Object?>{'mode': 'inherit'};
    final parsed = EnvironmentProtocolPlan.fromJson(wire, 'plan');
    expect(parsed.toJson()['egressProfile'], <String, Object?>{
      'mode': 'inherit',
    });
    final nativeWire =
        legacy['clientToolAccess']['route'] as Map<String, dynamic>;
    nativeWire['egressProfile'] = <String, Object?>{'mode': 'inherit'};
    final native = EnvironmentClientToolRoute.fromJson(nativeWire, 'route');
    expect(native.toJson()['egressProfile'], <String, Object?>{
      'mode': 'inherit',
    });
  });
}

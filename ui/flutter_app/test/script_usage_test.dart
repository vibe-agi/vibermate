import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/code_library_view.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

CodeLibraryTransformRevision _script(String id, int revision) =>
    CodeLibraryTransformRevision(
      id: id,
      revision: revision,
      collectionId: 'default',
      displayName: id,
      policy: const TrafficTransformPolicy(
        requestJavaScript: '',
        responseJavaScript: '',
      ),
      publishedAt: DateTime.utc(2026, 11, 13),
    );

/// The same traffic policy with [scripts] on every client flow.
EnvironmentRecord _running(
  EnvironmentRecord environment,
  List<CodeLibraryTransformRevision> scripts, {
  String? state,
}) => environment.copyWith(
  state: state,
  clientEndpoints: [
    for (final endpoint in environment.clientEndpoints)
      endpoint.copyWith(
        protocolPlans: [
          for (final plan in endpoint.protocolPlans)
            plan.copyWith(transforms: scripts),
        ],
      ),
  ],
);

int _flows(EnvironmentRecord environment) => environment.clientEndpoints
    .expand((endpoint) => endpoint.protocolPlans)
    .length;

void main() {
  test('a script lists the traffic policies that run it', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final environments = (await api.loadDashboard()).environments
        .where((environment) => _flows(environment) > 0)
        .toList();
    expect(environments.length, greaterThanOrEqualTo(2));
    final active = _running(environments[0], [
      _script('mask', 3),
    ], state: 'active');
    final disabled = _running(environments[1], [
      _script('mask', 2),
      _script('other', 1),
    ], state: 'disabled');

    final usage = scriptUsage([disabled, active], 'mask');
    expect(usage.map((value) => value.environmentId), [active.id, disabled.id]);
    expect(usage.first.flows, _flows(active));
    expect(usage.first.revisions, {3});
    expect(usage.last.active, isFalse);
    expect(usage.last.revisions, {2});

    expect(
      scriptUsage([disabled, active], 'other').single.environmentId,
      disabled.id,
    );
    expect(scriptUsage([disabled, active], 'unused'), isEmpty);
  });
}

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/network_exit_usage.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

EgressProfileRevision _tokyo(int revision) => EgressProfileRevision(
  id: 'profile.tokyo',
  revision: revision,
  displayName: 'Tokyo proxy',
  policy: const TrafficEgressPolicy.direct(),
  publishedAt: DateTime.utc(2026, 11, 13),
);

/// The same traffic policy with every client flow leaving through [exit].
EnvironmentRecord _through(EnvironmentRecord environment, EgressChoice exit) =>
    environment.copyWith(
      clientEndpoints: [
        for (final endpoint in environment.clientEndpoints)
          endpoint.copyWith(
            protocolPlans: [
              for (final plan in endpoint.protocolPlans)
                plan.copyWith(egressProfile: exit),
            ],
          ),
      ],
    );

int _flows(EnvironmentRecord environment) => environment.clientEndpoints
    .expand((endpoint) => endpoint.protocolPlans)
    .length;

void main() {
  test(
    'an exit lists the traffic policies that send traffic through it',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final environments = (await api.loadDashboard()).environments
          .where((environment) => _flows(environment) > 0)
          .toList();
      expect(environments.length, greaterThanOrEqualTo(2));
      final current = _through(environments[0], _tokyo(3));
      final lagging = _through(environments[1], _tokyo(2));

      final usage = networkExitUsage('profile.tokyo', [lagging, current], []);
      expect(usage.policies.map((policy) => policy.name).toSet(), {
        current.name,
        lagging.name,
      });
      final pinned = usage.policies.singleWhere(
        (policy) => policy.name == lagging.name,
      );
      expect(pinned.flows, _flows(lagging));
      expect(pinned.revisions, {2});

      final copy = AppCopy.forLanguage(AppLanguage.english);
      final text = networkExitUsageText(copy, usage, 3);
      expect(text, startsWith('Used by '));
      expect(text, contains('pinned to r2'));
      expect(text.split('pinned').length, 2, reason: 'only the lagging policy');

      expect(
        networkExitUsage('profile.other', [lagging, current], []).isEmpty,
        isTrue,
      );
      expect(
        networkExitUsageText(
          copy,
          networkExitUsage('profile.other', [], []),
          1,
        ),
        'No traffic policy or account uses this exit.',
      );
    },
  );
}

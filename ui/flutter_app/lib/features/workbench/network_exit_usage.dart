import '../../core/api/control_models.dart';
import '../../core/i18n/app_copy.dart';

/// Who uses one network exit: traffic policies whose client flows or tool
/// route name it, and accounts that pin it. Each user freezes an exact
/// revision, so a user can lag behind the latest saved one.
final class NetworkExitUsage {
  const NetworkExitUsage({required this.policies, required this.accounts});

  final List<({String name, int flows, Set<int> revisions})> policies;
  final List<({String name, int revision})> accounts;

  bool get isEmpty => policies.isEmpty && accounts.isEmpty;
}

NetworkExitUsage networkExitUsage(
  String profileId,
  List<EnvironmentRecord> environments,
  List<ProviderAccount> accounts,
) {
  final policies = <({String name, int flows, Set<int> revisions})>[];
  for (final environment in environments) {
    var flows = 0;
    final revisions = <int>{};
    void count(EgressChoice choice) {
      if (choice is EgressProfileRevision && choice.id == profileId) {
        flows += 1;
        revisions.add(choice.revision);
      }
    }

    for (final endpoint in environment.clientEndpoints) {
      for (final plan in endpoint.protocolPlans) {
        count(plan.egressProfile);
      }
    }
    if (environment.clientToolAccess?.route case final route?) {
      count(route.egressProfile);
    }
    if (flows > 0) {
      policies.add((
        name: environment.name,
        flows: flows,
        revisions: revisions,
      ));
    }
  }
  policies.sort((left, right) => left.name.compareTo(right.name));
  final pinned = <({String name, int revision})>[
    for (final account in accounts)
      if (account.egressProfile case final profile?
          when profile.id == profileId)
        (name: account.displayName, revision: profile.revision),
  ]..sort((left, right) => left.name.compareTo(right.name));
  return NetworkExitUsage(policies: policies, accounts: pinned);
}

/// One line, e.g. "Used by Work, 2 flows · Research, 1 flow · 1 account".
String networkExitUsageText(
  AppCopy copy,
  NetworkExitUsage usage,
  int latestRevision,
) {
  if (usage.isEmpty) return copy('network_exits.used_by.none');
  String revisions(Set<int> values) {
    final older = values.where((revision) => revision != latestRevision);
    if (older.isEmpty) return '';
    final sorted = older.toList()..sort();
    return ' · ${copy.format('network_exits.used_by.pinned', {'revisions': sorted.map((r) => 'r$r').join(', ')})}';
  }

  final parts = [
    for (final policy in usage.policies)
      '${policy.name}, ${copy.format('network_exits.used_by.flows', {'count': policy.flows})}${revisions(policy.revisions)}',
    if (usage.accounts.isNotEmpty)
      copy.format('network_exits.used_by.accounts', {
        'count': usage.accounts.length,
      }),
  ];
  return '${copy('network_exits.used_by')} ${parts.join(' · ')}';
}

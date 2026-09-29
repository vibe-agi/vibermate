import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Edits a Route's explicit scope, never the shared Endpoint associations.
final class RouteAccountScopeButton extends StatelessWidget {
  const RouteAccountScopeButton({
    required this.policy,
    required this.accounts,
    required this.copy,
    required this.onChanged,
    super.key,
  });

  final RouteAccountPolicy policy;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final ValueChanged<RouteAccountPolicy>? onChanged;

  @override
  Widget build(BuildContext context) => CompactLabeledControl(
    label: copy('environment.account.scope'),
    child: OutlinedButton.icon(
      onPressed: onChanged == null
          ? null
          : () async {
              final selected = await showDialog<RouteAccountPolicy>(
                context: context,
                builder: (_) => _AccountScopeDialog(
                  policy: policy,
                  accounts: accounts,
                  copy: copy,
                ),
              );
              if (selected != null && context.mounted) {
                onChanged!(selected);
              }
            },
      icon: const Icon(Icons.filter_list, size: 15),
      label: Text(
        copy.format('environment.account.scope_count', {
          'count': policy.accounts.length,
        }),
      ),
    ),
  );
}

final class _AccountScopeDialog extends StatefulWidget {
  const _AccountScopeDialog({
    required this.policy,
    required this.accounts,
    required this.copy,
  });
  final RouteAccountPolicy policy;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  @override
  State<_AccountScopeDialog> createState() => _AccountScopeDialogState();
}

final class _AccountScopeDialogState extends State<_AccountScopeDialog> {
  late final _selected = widget.policy.accounts
      .map((account) => account.id)
      .toSet();
  late String _fixedAccountId = widget.policy.fixedAccountId;
  String _query = '';

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final available = {
      for (final account in widget.accounts) account.id: account,
    };
    // Only a structural loss (the Account was unlinked or deleted) forces a
    // replacement. Disabled state and credential health are runtime facts:
    // they are labelled, but never block editing the explicit scope.
    final needsReplacement =
        widget.policy.mode == 'fixed' &&
        !available.containsKey(widget.policy.fixedAccountId);
    final validSelection =
        _selected.isNotEmpty &&
        _selected.every(available.containsKey) &&
        (widget.policy.mode != 'fixed' || _selected.contains(_fixedAccountId));
    final references = {
      for (final account in widget.policy.accounts) account.id: account,
      for (final account in widget.accounts)
        account.id: RouteAccountReference(
          id: account.id,
          revision: account.revision,
          displayName: account.displayName,
        ),
    };
    final rows = references.values.where((reference) {
      final account = available[reference.id];
      return [
        reference.displayName,
        reference.id,
        account?.note ?? '',
        account?.codexOAuth?.email ?? '',
      ].any((value) => value.toLowerCase().contains(_query));
    }).toList()..sort((a, b) => a.displayName.compareTo(b.displayName));
    return AlertDialog(
      title: Text(copy('environment.account.scope')),
      content: SizedBox(
        width: ViberMetrics.dialogStandardWidth,
        height: (MediaQuery.sizeOf(context).height * 0.55).clamp(200, 480),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('environment.account.scope_hint'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 12),
            TextField(
              key: const Key('route-account-scope-search'),
              onChanged: (value) =>
                  setState(() => _query = value.trim().toLowerCase()),
              decoration: InputDecoration(
                hintText: copy('provider_accounts.search'),
                prefixIcon: const Icon(Icons.search, size: 18),
              ),
            ),
            const SizedBox(height: 8),
            Text(
              copy.format('environment.account.scope_count', {
                'count': _selected.length,
              }),
            ),
            if (needsReplacement) ...[
              const SizedBox(height: 8),
              CompactLabeledControl(
                label: copy('environment.account.replacement'),
                child: CompactSelectField<String>(
                  key: const Key('route-account-scope-replacement'),
                  initialValue: available.containsKey(_fixedAccountId)
                      ? _fixedAccountId
                      : null,
                  placeholder: copy('environment.account.replacement_hint'),
                  isExpanded: true,
                  items: [
                    for (final account in widget.accounts)
                      if (_selected.contains(account.id))
                        DropdownMenuItem(
                          value: account.id,
                          child: Text(account.displayName),
                        ),
                  ],
                  onChanged: (value) {
                    if (value != null) setState(() => _fixedAccountId = value);
                  },
                ),
              ),
            ],
            const SizedBox(height: 8),
            Flexible(
              child: rows.isEmpty
                  ? Text(copy('provider_accounts.no_results'))
                  : ListView.builder(
                      shrinkWrap: true,
                      itemCount: rows.length,
                      itemBuilder: (context, index) {
                        final reference = rows[index];
                        final account = available[reference.id];
                        final active = reference.id == _fixedAccountId;
                        final selected = _selected.contains(reference.id);
                        final unavailable = account == null
                            ? copy('environment.account.selection_lost')
                            : routeAccountUnavailableReason(account, copy);
                        final detail = unavailable.isNotEmpty
                            ? unavailable
                            : active
                            ? copy('environment.account.scope_active')
                            : account?.codexOAuth?.email ?? '';
                        return CheckboxListTile(
                          key: Key('route-account-scope-${reference.id}'),
                          contentPadding: EdgeInsets.zero,
                          controlAffinity: ListTileControlAffinity.leading,
                          dense: true,
                          title: Text(reference.displayName),
                          subtitle: detail.isEmpty ? null : Text(detail),
                          value: selected,
                          onChanged: active || (!selected && account == null)
                              ? null
                              : (value) => setState(
                                  () => value == true
                                      ? _selected.add(reference.id)
                                      : _selected.remove(reference.id),
                                ),
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('route-account-scope-apply'),
          onPressed: !validSelection
              ? null
              : () {
                  final selected =
                      _selected.map((id) => references[id]!).toList()
                        ..sort((a, b) => a.id.compareTo(b.id));
                  Navigator.pop(
                    context,
                    RouteAccountPolicy(
                      revision: widget.policy.revision,
                      mode: widget.policy.mode,
                      selector: widget.policy.selector,
                      accounts: selected,
                      fixedAccountId: _fixedAccountId,
                    ),
                  );
                },
          child: Text(copy('common.save')),
        ),
      ],
    );
  }
}

/// The runtime reason an Account cannot serve a request right now, or "" when
/// it can. Shared by every view that labels Route Account availability.
String routeAccountUnavailableReason(ProviderAccount account, AppCopy copy) {
  if (account.usable) return '';
  if (account.codexOAuth?.state == 'reconnect_required') {
    return copy('routes.account.oauth_state.reconnect_required');
  }
  if (account.state != 'active') return copy('environment.account.disabled');
  return copy('routes.credentials.unavailable');
}

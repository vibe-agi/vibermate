import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Edits selection and scope together, never the shared Endpoint associations.
final class RouteAccountScopeButton extends StatelessWidget {
  const RouteAccountScopeButton({
    required this.policy,
    required this.accounts,
    required this.copy,
    required this.onChanged,
    this.loadLibrary,
    this.allowOriginal = false,
    super.key,
  });

  final RouteAccountPolicy policy;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final ValueChanged<RouteAccountPolicy>? onChanged;
  final Future<CodeLibraryCatalog> Function()? loadLibrary;
  final bool allowOriginal;

  @override
  Widget build(BuildContext context) {
    final account = accounts
        .where((value) => value.id == policy.fixedAccountId)
        .firstOrNull;
    final name =
        policy.accounts
            .where((value) => value.id == policy.fixedAccountId)
            .firstOrNull
            ?.displayName ??
        policy.fixedAccountId;
    final selection = policy.mode == 'original'
        ? copy('environment.account.original')
        : policy.mode == 'javascript'
        ? '${copy('environment.account.javascript')} · ${policy.selector!.displayName} · r${policy.selector!.revision}'
        : _fixedLabel(name, account, copy);
    return CompactLabeledControl(
      label: copy('environment.account'),
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
                    loadLibrary: loadLibrary,
                    allowOriginal: allowOriginal,
                  ),
                );
                if (selected != null && context.mounted) {
                  onChanged!(selected);
                }
              },
        icon: const Icon(Icons.filter_list, size: 15),
        label: Text(
          '$selection · ${copy.format('environment.account.scope_count', {'count': policy.accounts.length})}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
        ),
      ),
    );
  }
}

String _fixedLabel(String name, ProviderAccount? account, AppCopy copy) {
  final reason = account == null
      ? copy('environment.account.selection_lost')
      : routeAccountUnavailableReason(account, copy);
  return '${copy('environment.account.fixed')} · $name${reason.isEmpty ? '' : ' · $reason'}';
}

final class _AccountScopeDialog extends StatefulWidget {
  const _AccountScopeDialog({
    required this.policy,
    required this.accounts,
    required this.copy,
    required this.loadLibrary,
    required this.allowOriginal,
  });
  final RouteAccountPolicy policy;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final Future<CodeLibraryCatalog> Function()? loadLibrary;
  final bool allowOriginal;
  @override
  State<_AccountScopeDialog> createState() => _AccountScopeDialogState();
}

final class _AccountScopeDialogState extends State<_AccountScopeDialog> {
  late final _selected = widget.policy.accounts
      .map((account) => account.id)
      .toSet();
  late String _fixedAccountId = widget.policy.fixedAccountId;
  late String _mode = widget.policy.mode;
  late CodeLibraryAccountSelectorRevision? _selector = widget.policy.selector;
  late final _library = widget.loadLibrary?.call();
  String _query = '';

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final available = {
      for (final account in widget.accounts) account.id: account,
    };
    final validSelection =
        (_selected.isNotEmpty || _mode == 'original') &&
        _selected.every(available.containsKey) &&
        (_mode == 'fixed'
            ? _selected.contains(_fixedAccountId)
            : _mode == 'original'
            ? widget.allowOriginal
            : _selector != null);
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
      title: Text(copy('environment.account.configure')),
      content: SizedBox(
        width: ViberMetrics.dialogStandardWidth,
        height: (MediaQuery.sizeOf(context).height * 0.55).clamp(200, 480),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            FutureBuilder<CodeLibraryCatalog>(
              future: _library,
              builder: (context, snapshot) {
                String token(CodeLibraryAccountSelectorRevision item) =>
                    'javascript:${item.id}:${item.revision}';
                final selectors = [...?snapshot.data?.accountSelectors]
                  ..sort((a, b) => a.displayName.compareTo(b.displayName));
                final current = _mode == 'original'
                    ? 'original'
                    : _mode == 'fixed'
                    ? 'fixed:$_fixedAccountId'
                    : token(_selector!);
                final frozen = _selector;
                if (frozen != null &&
                    !selectors.any((item) => token(item) == token(frozen))) {
                  selectors.insert(0, frozen);
                }
                return CompactLabeledControl(
                  label: copy('environment.account'),
                  detail: snapshot.hasError
                      ? copy('environment.account.selector_load_failed')
                      : copy('environment.account.scope_selection'),
                  child: CompactSelectField<String>(
                    key: const Key('route-account-selection'),
                    initialValue: current,
                    isExpanded: true,
                    items: [
                      if (widget.allowOriginal || _mode == 'original')
                        DropdownMenuItem(
                          value: 'original',
                          enabled: widget.allowOriginal,
                          child: Text(
                            copy('environment.account.original'),
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      for (final reference in references.values)
                        if (_selected.contains(reference.id))
                          DropdownMenuItem(
                            value: 'fixed:${reference.id}',
                            enabled: available[reference.id]?.usable ?? false,
                            child: Text(
                              _fixedLabel(
                                reference.displayName,
                                available[reference.id],
                                copy,
                              ),
                              overflow: TextOverflow.ellipsis,
                            ),
                          ),
                      for (final selector in selectors)
                        DropdownMenuItem(
                          value: token(selector),
                          enabled: _selected.any(
                            (id) => available[id]?.usable ?? false,
                          ),
                          child: Text(
                            '${copy('environment.account.javascript')} · ${selector.displayName} · r${selector.revision}',
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                    ],
                    onChanged: (value) {
                      if (value == null || value == current) return;
                      setState(() {
                        if (value == 'original') {
                          _mode = 'original';
                          _fixedAccountId = '';
                          _selector = null;
                        } else if (value.startsWith('fixed:')) {
                          _mode = 'fixed';
                          _fixedAccountId = value.substring('fixed:'.length);
                          _selector = null;
                        } else {
                          _mode = 'javascript';
                          _fixedAccountId = '';
                          _selector = selectors.firstWhere(
                            (item) => token(item) == value,
                          );
                        }
                      });
                    },
                  ),
                );
              },
            ),
            const SizedBox(height: 16),
            Text(
              copy(
                _mode == 'original'
                    ? 'environment.account.original_hint'
                    : 'environment.account.scope_hint',
              ),
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
                        final active =
                            _mode == 'fixed' && reference.id == _fixedAccountId;
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
                      mode: _mode,
                      selector: _selector,
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

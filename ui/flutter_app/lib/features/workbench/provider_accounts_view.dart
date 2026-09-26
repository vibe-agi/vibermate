import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/account_facts_models.dart';
import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'provider_account_editor.dart';
import 'provider_account_note_editor.dart';
import 'provider_account_token_details.dart';
import 'provider_account_facts.dart';
import 'workbench_controller.dart';
import 'control_failure_notice.dart';

enum _ProviderAccountSort {
  defaultOrder,
  attention,
  weeklyUsage,
  weeklyReset,
  name,
}

/// The one place for managing upstream credentials. Service configuration
/// screens navigate here instead of hosting another credential editor.
final class ProviderAccountsView extends StatefulWidget {
  const ProviderAccountsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<ProviderAccountsView> createState() => _ProviderAccountsViewState();
}

final class _ProviderAccountsViewState extends State<ProviderAccountsView> {
  final _search = TextEditingController();
  var _sort = _ProviderAccountSort.defaultOrder;
  String _quotaSignature = '';

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final accounts = controller.data?.accounts ?? const <ProviderAccount>[];
    final endpoints = controller.data?.endpoints ?? const <UpstreamEndpoint>[];
    _scheduleQuotaLoads(accounts);
    final query = _search.text.trim().toLowerCase();
    final filtered = _sortedAccounts(
      accounts
          .where((account) {
            return [
              account.displayName,
              account.note,
              copy('routes.account.kind.${account.kind}'),
              account.codexOAuth?.email ?? '',
              account.codexOAuth?.chatgptAccountId ?? '',
              account.tokenInfo?.email ?? '',
              account.tokenInfo?.chatgptAccountId ?? '',
              account.credentialOrigin,
              ...endpoints
                  .where((value) => account.isLinkedTo(value.id))
                  .map((value) => value.displayName),
            ].any((value) => value.toLowerCase().contains(query));
          })
          .toList(growable: false),
      accounts,
    );

    return Column(
      children: [
        PageHeading(
          title: copy('provider_accounts.title'),
          help: copy('provider_accounts.subtitle'),
          dismissHelpLabel: copy('common.dismiss'),
          trailing: FilledButton.icon(
            key: const Key('provider-accounts-add'),
            onPressed: controller.inventoryMutating || endpoints.isEmpty
                ? null
                : () => unawaited(
                    showProviderAccountEditor(
                      context,
                      controller: controller,
                      copy: copy,
                    ),
                  ),
            icon: const Icon(Icons.add, size: 16),
            label: Text(copy('routes.add_account')),
          ),
        ),
        const Divider(height: 1),
        if (controller.inventoryError case final error?)
          ControlFailureNotice(
            message: error,
            copy: copy,
            diagnostic: controller.inventoryErrorDiagnostic,
          ),
        if (controller.inventoryNotice case final notice?)
          LayoutBuilder(
            builder: (context, constraints) => InlineNotice(
              message: copy('notice.inventory.$notice'),
              // Each account already has a service-link action. In narrow
              // layouts it is more useful there than squeezing this notice.
              actionLabel:
                  notice == 'account_created' && constraints.maxWidth >= 600
                  ? copy('notice.inventory.account_created.action')
                  : null,
              onAction:
                  notice == 'account_created' && constraints.maxWidth >= 600
                  ? () => controller.selectSection(WorkbenchSection.routes)
                  : null,
              onDismiss: controller.clearInventoryNotice,
              dismissLabel: copy('common.dismiss'),
            ),
          ),
        Padding(
          padding: const EdgeInsets.all(16),
          child: LayoutBuilder(
            builder: (context, constraints) {
              final search = TextField(
                key: const Key('provider-accounts-search'),
                controller: _search,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  hintText: copy('provider_accounts.search'),
                  prefixIcon: const Icon(Icons.search, size: 18),
                  suffixIcon: query.isEmpty
                      ? null
                      : IconButton(
                          tooltip: copy('provider_accounts.clear_search'),
                          onPressed: () => setState(_search.clear),
                          icon: const Icon(Icons.close, size: 16),
                        ),
                ),
              );
              final sort = SizedBox(
                width: constraints.maxWidth < 560 ? constraints.maxWidth : 220,
                child: DropdownButtonFormField<_ProviderAccountSort>(
                  key: const Key('provider-accounts-sort'),
                  initialValue: _sort,
                  isExpanded: true,
                  decoration: InputDecoration(
                    labelText: copy('provider_accounts.sort.label'),
                    prefixIcon: const Icon(Icons.sort, size: 18),
                  ),
                  items: [
                    for (final value in _ProviderAccountSort.values)
                      DropdownMenuItem(
                        value: value,
                        child: Text(
                          copy('provider_accounts.sort.${value.name}'),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                  ],
                  onChanged: (value) {
                    if (value != null) setState(() => _sort = value);
                  },
                ),
              );
              if (constraints.maxWidth < 560) {
                return Column(
                  children: [search, const SizedBox(height: 8), sort],
                );
              }
              return Row(
                children: [
                  Expanded(child: search),
                  const SizedBox(width: 10),
                  sort,
                ],
              );
            },
          ),
        ),
        Expanded(
          child: filtered.isEmpty
              ? CenteredMessage(
                  icon: Icons.key_outlined,
                  title: copy(
                    query.isEmpty
                        ? 'provider_accounts.empty'
                        : 'provider_accounts.no_results',
                  ),
                  action: query.isEmpty && endpoints.isNotEmpty
                      ? OutlinedButton.icon(
                          onPressed: controller.inventoryMutating
                              ? null
                              : () => unawaited(
                                  showProviderAccountEditor(
                                    context,
                                    controller: controller,
                                    copy: copy,
                                  ),
                                ),
                          icon: const Icon(Icons.add, size: 16),
                          label: Text(copy('routes.add_account')),
                        )
                      : null,
                )
              : LayoutBuilder(
                  builder: (context, constraints) => ListView.builder(
                    key: const Key('provider-accounts-list'),
                    padding: const EdgeInsets.only(bottom: 24),
                    itemCount: filtered.length,
                    itemBuilder: (context, index) {
                      final account = filtered[index];
                      final linkedEndpoints = endpoints
                          .where((value) => account.isLinkedTo(value.id))
                          .toList(growable: false);
                      return Container(
                        key: Key('provider-account-${account.id}'),
                        margin: const EdgeInsets.fromLTRB(16, 0, 16, 10),
                        decoration: BoxDecoration(
                          color: context.viberColors.panel,
                          border: Border.all(
                            color: context.viberColors.dividerSoft,
                          ),
                          borderRadius: BorderRadius.circular(8),
                        ),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            ProviderAccountRow(
                              account: account,
                              compact: constraints.maxWidth < 850,
                              copy: copy,
                              busy: controller.inventoryMutating,
                              onEditNote: () => unawaited(
                                showProviderAccountNoteEditor(
                                  context,
                                  controller: controller,
                                  account: account,
                                  copy: copy,
                                ),
                              ),
                              refreshing:
                                  controller.refreshingProviderAccountId ==
                                  account.id,
                              onRefresh: account.kind == 'codex_oauth'
                                  ? () => unawaited(
                                      controller
                                          .refreshProviderAccountCredential(
                                            account,
                                          ),
                                    )
                                  : null,
                              onReplace: () => unawaited(
                                showProviderAccountEditor(
                                  context,
                                  controller: controller,
                                  copy: copy,
                                  account: account,
                                ),
                              ),
                              onDelete: () => unawaited(
                                showProviderAccountDeletion(
                                  context,
                                  controller: controller,
                                  copy: copy,
                                  account: account,
                                ),
                              ),
                            ),
                            ProviderAccountFactsPanel(
                              account: account,
                              controller: controller,
                              copy: copy,
                            ),
                            ProviderAccountTokenDetails(
                              account: account,
                              copy: copy,
                            ),
                            Padding(
                              padding: const EdgeInsets.fromLTRB(14, 0, 14, 6),
                              child: Wrap(
                                spacing: 6,
                                runSpacing: 2,
                                children: [
                                  if (linkedEndpoints.isEmpty)
                                    TextButton.icon(
                                      onPressed: () => controller.selectSection(
                                        WorkbenchSection.routes,
                                      ),
                                      icon: const Icon(
                                        Icons.add_link,
                                        size: 14,
                                      ),
                                      label: Text(
                                        copy('provider_accounts.unlinked'),
                                      ),
                                    ),
                                  for (final endpoint in linkedEndpoints)
                                    TextButton.icon(
                                      key: Key(
                                        'provider-account-service-${account.id}-${endpoint.id}',
                                      ),
                                      onPressed: () {
                                        controller.selectEndpoint(endpoint.id);
                                        controller.selectSection(
                                          WorkbenchSection.routes,
                                        );
                                      },
                                      icon: const Icon(
                                        Icons.hub_outlined,
                                        size: 14,
                                      ),
                                      label: Text(endpoint.displayName),
                                      style: TextButton.styleFrom(
                                        foregroundColor:
                                            context.viberColors.textMuted,
                                        textStyle: Theme.of(
                                          context,
                                        ).textTheme.bodySmall,
                                      ),
                                    ),
                                ],
                              ),
                            ),
                          ],
                        ),
                      );
                    },
                  ),
                ),
        ),
      ],
    );
  }

  void _scheduleQuotaLoads(List<ProviderAccount> accounts) {
    final eligible = accounts.where(_supportsQuota).toList(growable: false);
    final signature = eligible
        .map(
          (account) =>
              '${account.id}:${account.credentialEpoch}:${account.credentialOrigin}',
        )
        .join('|');
    if (signature == _quotaSignature) return;
    _quotaSignature = signature;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) {
        unawaited(widget.controller.ensureProviderAccountQuotas(eligible));
      }
    });
  }

  List<ProviderAccount> _sortedAccounts(
    List<ProviderAccount> filtered,
    List<ProviderAccount> all,
  ) {
    if (_sort == _ProviderAccountSort.defaultOrder) return filtered;
    final originalIndex = {
      for (final (index, account) in all.indexed) account.id: index,
    };
    if ((_sort == _ProviderAccountSort.weeklyUsage ||
            _sort == _ProviderAccountSort.weeklyReset) &&
        all
            .where(_supportsQuota)
            .any(
              (account) =>
                  !widget.controller.providerAccountQuotaSettled(account),
            )) {
      return filtered;
    }
    final sorted = [...filtered];
    sorted.sort((left, right) {
      final compared = switch (_sort) {
        _ProviderAccountSort.attention => _attentionRank(
          left,
        ).compareTo(_attentionRank(right)),
        _ProviderAccountSort.weeklyUsage => _compareNullableIntDescending(
          _weeklyWindow(left)?.usedPercent,
          _weeklyWindow(right)?.usedPercent,
        ),
        _ProviderAccountSort.weeklyReset => _compareNullableDate(
          _weeklyWindow(left)?.resetAt,
          _weeklyWindow(right)?.resetAt,
        ),
        _ProviderAccountSort.name => _accountIdentity(
          left,
        ).toLowerCase().compareTo(_accountIdentity(right).toLowerCase()),
        _ProviderAccountSort.defaultOrder => 0,
      };
      return compared != 0
          ? compared
          : originalIndex[left.id]!.compareTo(originalIndex[right.id]!);
    });
    return sorted;
  }

  int _attentionRank(ProviderAccount account) {
    final oauth = account.codexOAuth;
    final used = _weeklyWindow(account)?.usedPercent ?? 0;
    if (!account.usable ||
        oauth?.state == 'reconnect_required' ||
        used >= 100) {
      return 0;
    }
    if (oauth?.state == 'refresh_due' || used >= 90) return 1;
    return 2;
  }

  AccountQuotaWindow? _weeklyWindow(ProviderAccount account) {
    final facts = widget.controller.providerAccountQuota(account);
    AccountQuotaWindow? longest;
    for (final limit in facts?.limits ?? const <AccountQuotaLimit>[]) {
      for (final window in [limit.primary, limit.secondary]) {
        if (window != null &&
            (longest == null || window.windowSeconds > longest.windowSeconds)) {
          longest = window;
        }
      }
    }
    return longest;
  }

  bool _supportsQuota(ProviderAccount account) {
    final origin = Uri.tryParse(account.credentialOrigin);
    return account.usable && origin != null && isChatGPTCodexOrigin(origin);
  }

  String _accountIdentity(ProviderAccount account) => account.displayName;
}

int _compareNullableIntDescending(int? left, int? right) {
  if (left == null) return right == null ? 0 : 1;
  if (right == null) return -1;
  return right.compareTo(left);
}

int _compareNullableDate(DateTime? left, DateTime? right) {
  if (left == null) return right == null ? 0 : 1;
  if (right == null) return -1;
  return left.compareTo(right);
}

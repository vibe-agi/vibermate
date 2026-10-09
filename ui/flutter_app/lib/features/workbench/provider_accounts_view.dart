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
import 'egress_profile_editor.dart';

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
  final _expandedAccounts = <String>{};
  var _sort = _ProviderAccountSort.defaultOrder;
  String _quotaSignature = '';
  bool _refreshingAccounts = false;

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
          trailing: LayoutBuilder(
            builder: (context, constraints) {
              final onRefresh =
                  _refreshingAccounts || controller.inventoryMutating
                  ? null
                  : () => unawaited(_refreshAccounts());
              final icon = _refreshingAccounts
                  ? const SizedBox.square(
                      dimension: 14,
                      child: CircularProgressIndicator(strokeWidth: 1.5),
                    )
                  : const Icon(Icons.refresh, size: 16);
              final refresh = constraints.maxWidth < 360
                  ? IconButton.outlined(
                      key: const Key('provider-accounts-refresh-all'),
                      onPressed: onRefresh,
                      icon: icon,
                    )
                  : OutlinedButton.icon(
                      key: const Key('provider-accounts-refresh-all'),
                      onPressed: onRefresh,
                      icon: icon,
                      label: Text(copy('provider_accounts.refresh_all')),
                    );
              final onAdd = controller.inventoryMutating || endpoints.isEmpty
                  ? null
                  : () => unawaited(
                      showProviderAccountEditor(
                        context,
                        controller: controller,
                        copy: copy,
                      ),
                    );
              final add = constraints.maxWidth < 360
                  ? IconButton.filled(
                      key: const Key('provider-accounts-add'),
                      onPressed: onAdd,
                      icon: const Icon(Icons.add, size: 16),
                    )
                  : FilledButton.icon(
                      key: const Key('provider-accounts-add'),
                      onPressed: onAdd,
                      icon: const Icon(Icons.add, size: 16),
                      label: Text(copy('routes.add_account')),
                    );
              return Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  Tooltip(
                    message: copy('provider_accounts.refresh_all.hint'),
                    child: refresh,
                  ),
                  Tooltip(message: copy('routes.add_account'), child: add),
                ],
              );
            },
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
                child: Tooltip(
                  message: copy('provider_accounts.sort.label'),
                  child: CompactSelectField<_ProviderAccountSort>(
                    key: const Key('provider-accounts-sort'),
                    initialValue: _sort,
                    isExpanded: true,
                    decoration: const InputDecoration(
                      prefixIcon: Icon(Icons.sort, size: 18),
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
                  builder: (context, constraints) {
                    final table = constraints.maxWidth >= 1100;
                    final count = filtered.length + (table ? 1 : 0);
                    final list = ListView.separated(
                      key: const Key('provider-accounts-list'),
                      padding: table
                          ? EdgeInsets.zero
                          : const EdgeInsets.only(bottom: 24),
                      itemCount: count,
                      separatorBuilder: (_, _) => table
                          ? Divider(
                              height: 1,
                              color: context.viberColors.dividerSoft,
                            )
                          : const SizedBox.shrink(),
                      itemBuilder: (context, index) {
                        if (table && index == 0) {
                          return _accountTableHeader(context);
                        }
                        final account = filtered[index - (table ? 1 : 0)];
                        final linkedEndpoints = endpoints
                            .where((value) => account.isLinkedTo(value.id))
                            .toList(growable: false);
                        return _accountEntry(
                          context,
                          account: account,
                          linkedEndpoints: linkedEndpoints,
                          table: table,
                        );
                      },
                    );
                    if (!table) return list;
                    return Container(
                      margin: const EdgeInsets.fromLTRB(16, 0, 16, 24),
                      clipBehavior: Clip.antiAlias,
                      decoration: BoxDecoration(
                        color: context.viberColors.panel,
                        border: Border.all(
                          color: context.viberColors.dividerSoft,
                        ),
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: list,
                    );
                  },
                ),
        ),
      ],
    );
  }

  Widget _accountTableHeader(BuildContext context) {
    final copy = widget.copy;
    final style = Theme.of(context).textTheme.labelSmall?.copyWith(
      color: context.viberColors.textMuted,
      fontWeight: FontWeight.w600,
    );
    Widget label(String key, {TextAlign align = TextAlign.left}) =>
        Text(copy(key), textAlign: align, style: style);
    return Container(
      color: context.viberColors.panelRaised,
      padding: const EdgeInsets.fromLTRB(14, 4, 6, 4),
      child: Row(
        children: [
          Expanded(flex: 30, child: label('provider_accounts.table.account')),
          const SizedBox(width: 16),
          SizedBox(
            key: const Key('provider-accounts-automatic-refresh-column'),
            width: 128,
            child: Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                Flexible(child: label('provider_accounts.automatic_refresh')),
                ContextHelpButton(
                  title: copy('provider_accounts.automatic_refresh'),
                  message: copy('provider_accounts.automatic_refresh.detail'),
                  dismissLabel: copy('common.dismiss'),
                ),
              ],
            ),
          ),
          const SizedBox(width: 16),
          Expanded(flex: 42, child: label('provider_accounts.table.quota')),
          const SizedBox(width: 16),
          SizedBox(width: 150, child: label('provider_accounts.table.service')),
          const SizedBox(width: 8),
          SizedBox(
            width: 128,
            child: label(
              'provider_accounts.table.actions',
              align: TextAlign.right,
            ),
          ),
        ],
      ),
    );
  }

  Widget _accountEntry(
    BuildContext context, {
    required ProviderAccount account,
    required List<UpstreamEndpoint> linkedEndpoints,
    required bool table,
  }) {
    final controller = widget.controller;
    final copy = widget.copy;
    final expanded = _expandedAccounts.contains(account.id);
    final quota = _ProviderAccountQuotaSummary(
      account: account,
      controller: controller,
      copy: copy,
    );
    final service = _accountService(
      context,
      account: account,
      linkedEndpoints: linkedEndpoints,
    );
    final content = Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        ProviderAccountRow(
          account: account,
          compact: !table,
          copy: copy,
          busy: controller.inventoryMutating,
          quota: table ? quota : null,
          service: table ? service : null,
          detailsExpanded: expanded,
          onToggleDetails: () => setState(() {
            if (!_expandedAccounts.remove(account.id)) {
              _expandedAccounts.add(account.id);
            }
          }),
          onAutomaticRefreshChanged: (enabled) => unawaited(
            controller.setProviderAccountSettings(
              account,
              egressProfile: account.egressProfile,
              automaticRefresh: enabled,
            ),
          ),
          onEditNote: () => unawaited(
            showProviderAccountNoteEditor(
              context,
              controller: controller,
              account: account,
              copy: copy,
            ),
          ),
          refreshingQuota: controller.providerAccountQuotaLoading(account),
          onRefreshQuota: _supportsQuota(account)
              ? () => unawaited(controller.refreshProviderAccountQuota(account))
              : null,
          onReplace: () => unawaited(
            showProviderAccountEditor(
              context,
              controller: controller,
              copy: copy,
              account: account,
            ),
          ),
          onSignInAgain: account.kind == 'codex_oauth'
              ? () => unawaited(
                  showProviderAccountReauthorization(
                    context,
                    controller: controller,
                    copy: copy,
                    account: account,
                  ),
                )
              : null,
          onDelete: () => unawaited(
            showProviderAccountDeletion(
              context,
              controller: controller,
              copy: copy,
              account: account,
            ),
          ),
        ),
        if (!table)
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 2, 14, 7),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                quota,
                Align(alignment: Alignment.centerLeft, child: service),
              ],
            ),
          ),
        if (expanded)
          Container(
            key: Key('provider-account-details-${account.id}'),
            decoration: BoxDecoration(
              color: context.viberColors.panelRaised.withValues(alpha: .45),
              border: Border(
                top: BorderSide(color: context.viberColors.dividerSoft),
              ),
            ),
            child: Padding(
              padding: const EdgeInsets.all(14),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  ProviderAccountTokenDetails(account: account, copy: copy),
                  if (_supportsQuota(account) || account.kind == 'codex_oauth')
                    ProviderAccountFactsPanel(
                      account: account,
                      controller: controller,
                      copy: copy,
                      showQuotaWindows: false,
                    ),
                  const Divider(height: 24),
                  Align(
                    alignment: Alignment.centerLeft,
                    child: ConstrainedBox(
                      constraints: const BoxConstraints(maxWidth: 420),
                      child: CompactLabeledControl(
                        label: copy('provider_accounts.egress.label'),
                        help: copy('provider_accounts.egress.detail'),
                        dismissHelpLabel: copy('common.dismiss'),
                        child: OutlinedButton(
                          key: Key('account-egress-${account.id}'),
                          onPressed: controller.inventoryMutating
                              ? null
                              : () => unawaited(_selectAccountEgress(account)),
                          child: Row(
                            children: [
                              const Icon(Icons.alt_route_rounded, size: 16),
                              const SizedBox(width: 8),
                              Expanded(
                                child: Text(
                                  account.egressProfile != null
                                      ? egressProfileSummary(
                                          copy,
                                          account.egressProfile!,
                                        )
                                      : copy(
                                          'provider_accounts.egress.inherit',
                                        ),
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                ),
                              ),
                              const Icon(Icons.expand_more, size: 16),
                            ],
                          ),
                        ),
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
      ],
    );
    if (table) {
      return KeyedSubtree(
        key: Key('provider-account-${account.id}'),
        child: content,
      );
    }
    return Container(
      key: Key('provider-account-${account.id}'),
      margin: const EdgeInsets.fromLTRB(16, 0, 16, 10),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: BorderRadius.circular(8),
      ),
      clipBehavior: Clip.antiAlias,
      child: content,
    );
  }

  Widget _accountService(
    BuildContext context, {
    required ProviderAccount account,
    required List<UpstreamEndpoint> linkedEndpoints,
  }) {
    final linked = linkedEndpoints.isNotEmpty;
    final label = linked
        ? linkedEndpoints.map((endpoint) => endpoint.displayName).join(' · ')
        : widget.copy('provider_accounts.table.unlinked');
    return Tooltip(
      message: linked ? label : widget.copy('provider_accounts.unlinked'),
      child: TextButton.icon(
        key: linkedEndpoints.length == 1
            ? Key(
                'provider-account-service-${account.id}-${linkedEndpoints.single.id}',
              )
            : Key('provider-account-service-${account.id}'),
        onPressed: () {
          if (linkedEndpoints.length == 1) {
            widget.controller.selectEndpoint(linkedEndpoints.single.id);
          }
          widget.controller.selectSection(WorkbenchSection.routes);
        },
        style: TextButton.styleFrom(
          alignment: Alignment.centerLeft,
          padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 2),
          minimumSize: const Size(0, 30),
          foregroundColor: context.viberColors.textMuted,
          textStyle: Theme.of(context).textTheme.bodySmall,
        ),
        icon: Icon(linked ? Icons.hub_outlined : Icons.add_link, size: 14),
        label: Text(label, maxLines: 2, overflow: TextOverflow.ellipsis),
      ),
    );
  }

  Future<void> _selectAccountEgress(ProviderAccount account) async {
    final selection = await showEgressProfileSelection(
      context: context,
      selectionId: account.id,
      initial: account.egressProfile,
      copy: widget.copy,
      loadProfiles: widget.controller.egressProfiles,
      allowInheritance: true,
    );
    if (!mounted || selection == null) return;
    await widget.controller.setProviderAccountSettings(
      account,
      egressProfile: selection.profile,
      automaticRefresh: account.automaticRefresh,
    );
  }

  void _scheduleQuotaLoads(List<ProviderAccount> accounts) {
    final eligible = accounts.where(_supportsQuota).toList(growable: false);
    final signature = eligible
        .map(
          (account) =>
              '${account.id}:${account.credentialEpoch}:${account.settingsRevision}:${account.credentialOrigin}',
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

  Future<void> _refreshAccounts() async {
    if (_refreshingAccounts) return;
    setState(() => _refreshingAccounts = true);
    try {
      await widget.controller.refresh();
      if (!mounted) return;
      final accounts =
          widget.controller.data?.accounts ?? const <ProviderAccount>[];
      await widget.controller.ensureProviderAccountQuotas(
        accounts.where(_supportsQuota),
        refresh: true,
      );
    } finally {
      if (mounted) setState(() => _refreshingAccounts = false);
    }
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

final class _ProviderAccountQuotaSummary extends StatelessWidget {
  const _ProviderAccountQuotaSummary({
    required this.account,
    required this.controller,
    required this.copy,
  });

  final ProviderAccount account;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final origin = Uri.tryParse(account.credentialOrigin);
    if (origin == null || !isChatGPTCodexOrigin(origin)) {
      return const SizedBox.shrink();
    }
    final facts = controller.providerAccountQuota(account);
    final loading = controller.providerAccountQuotaLoading(account);
    final failed = controller.providerAccountQuotaFailed(account);
    if (facts == null) {
      return SizedBox(
        height: 44,
        child: Row(
          children: [
            if (loading) ...[
              const SizedBox.square(
                dimension: 13,
                child: CircularProgressIndicator(strokeWidth: 1.5),
              ),
              const SizedBox(width: 7),
            ],
            Flexible(
              child: Text(
                copy(
                  failed
                      ? 'account_facts.failed'
                      : loading
                      ? 'account_facts.loading'
                      : 'account_facts.unavailable',
                ),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: failed
                      ? context.viberColors.warning
                      : context.viberColors.textMuted,
                ),
              ),
            ),
          ],
        ),
      );
    }
    if (facts.state == 'unsupported' || facts.state == 'unavailable') {
      return Text(
        copy('account_facts.unavailable'),
        style: Theme.of(
          context,
        ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
      );
    }
    final windows = <({AccountQuotaLimit limit, AccountQuotaWindow window})>[];
    for (final limit in facts.limits) {
      if (limit.primary case final window?) {
        windows.add((limit: limit, window: window));
      }
      if (limit.secondary case final window?) {
        windows.add((limit: limit, window: window));
      }
    }
    windows.sort(
      (left, right) =>
          left.window.windowSeconds.compareTo(right.window.windowSeconds),
    );
    final stale = failed || facts.state == 'stale';
    final credits = facts.credits;
    final resets = facts.rateLimitResets;
    final compactFacts = <Widget>[
      if (credits != null)
        _compactFact(
          context,
          key: Key('provider-account-credits-${account.id}'),
          icon: Icons.toll_outlined,
          value: credits.unlimited
              ? '∞'
              : !credits.hasCredits
              ? '0.00'
              : accountCreditsLabel(credits.balance, '?'),
          hint: copy.format('account_facts.credits', {
            'balance': credits.unlimited
                ? copy('account_facts.unlimited')
                : !credits.hasCredits
                ? copy('account_facts.no_credits')
                : accountCreditsLabel(
                    credits.balance,
                    copy('account_facts.unknown'),
                  ),
          }),
        ),
      if (resets != null)
        ProviderAccountResetCreditsButton(
          account: account,
          controller: controller,
          copy: copy,
        ),
    ];
    return Row(
      key: Key('provider-account-quota-summary-${account.id}'),
      children: [
        if (stale) ...[
          Tooltip(
            message: copy(
              failed ? 'account_facts.failed_stale' : 'account_facts.stale',
            ),
            child: Icon(
              Icons.warning_amber_rounded,
              size: 15,
              color: context.viberColors.warning,
            ),
          ),
          const SizedBox(width: 7),
        ],
        Expanded(
          child: Align(
            alignment: Alignment.centerLeft,
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 560),
              child: LayoutBuilder(
                builder: (context, constraints) {
                  final width = constraints.maxWidth >= 300
                      ? (constraints.maxWidth - 14) / 2
                      : constraints.maxWidth;
                  return Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      if (windows.isEmpty)
                        Text(
                          copy('account_facts.no_windows'),
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        )
                      else
                        Wrap(
                          spacing: 14,
                          runSpacing: 7,
                          children: [
                            for (final item in windows)
                              SizedBox(
                                width: width,
                                child: _QuotaGauge(
                                  item: item,
                                  multipleLimits: facts.limits.length > 1,
                                  copy: copy,
                                ),
                              ),
                          ],
                        ),
                      if (compactFacts.isNotEmpty) ...[
                        const SizedBox(height: 5),
                        Wrap(
                          spacing: 12,
                          runSpacing: 3,
                          children: compactFacts,
                        ),
                      ],
                    ],
                  );
                },
              ),
            ),
          ),
        ),
      ],
    );
  }

  Widget _compactFact(
    BuildContext context, {
    required Key key,
    required IconData icon,
    required String value,
    required String hint,
  }) => Tooltip(
    key: key,
    message: hint,
    child: Semantics(
      label: hint,
      excludeSemantics: true,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 13, color: context.viberColors.textFaint),
          const SizedBox(width: 4),
          Text(
            value,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
              fontSize: 11,
            ),
          ),
        ],
      ),
    ),
  );
}

final class _QuotaGauge extends StatelessWidget {
  const _QuotaGauge({
    required this.item,
    required this.multipleLimits,
    required this.copy,
  });

  final ({AccountQuotaLimit limit, AccountQuotaWindow window}) item;
  final bool multipleLimits;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final window = item.window;
    final color = accountQuotaUsageColor(colors, window.usedPercent);
    final duration = _quotaDuration(copy, window.windowSeconds);
    final windowLabel = window.windowSeconds == 0
        ? copy('account_facts.window_unknown')
        : copy.format('account_facts.window_title', {'duration': duration});
    final name = item.limit.name ?? item.limit.model ?? item.limit.id;
    final label = multipleLimits || item.limit.id != 'codex'
        ? '$windowLabel · $name'
        : windowLabel;
    return Semantics(
      label: '$label ${window.usedPercent}%',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(
                    context,
                  ).textTheme.labelSmall?.copyWith(color: colors.textMuted),
                ),
              ),
              const SizedBox(width: 8),
              Text(
                '${window.usedPercent}%',
                style: monoStyle.copyWith(
                  fontSize: 14,
                  fontWeight: FontWeight.w700,
                  color: color,
                ),
              ),
            ],
          ),
          const SizedBox(height: 4),
          ExcludeSemantics(
            child: LinearProgressIndicator(
              value: (window.usedPercent / 100).clamp(0, 1),
              minHeight: 2,
              borderRadius: BorderRadius.circular(1),
              color: color,
              backgroundColor: colors.dividerSoft,
            ),
          ),
          const SizedBox(height: 3),
          Tooltip(
            message: copy.format('account_facts.resets', {
              'time': window.resetAt.toLocal().toString().split('.').first,
            }),
            child: Text(
              copy.format('account_facts.reset_short', {
                'time': _quotaTime(window.resetAt),
              }),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: colors.textFaint,
                fontSize: 11,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

String _quotaDuration(AppCopy copy, int seconds) {
  for (final unit in [(86400, 'days'), (3600, 'hours'), (60, 'minutes')]) {
    if (seconds > 0 && seconds % unit.$1 == 0) {
      return copy.format('account_facts.${unit.$2}', {
        'count': seconds ~/ unit.$1,
      });
    }
  }
  return copy.format('account_facts.seconds', {'count': seconds});
}

String _quotaTime(DateTime time) {
  final local = time.toLocal();
  final year = local.year == DateTime.now().year ? '' : '${local.year}/';
  String two(int value) => value.toString().padLeft(2, '0');
  return '$year${two(local.month)}/${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
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

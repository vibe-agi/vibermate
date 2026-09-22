import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'provider_account_editor.dart';
import 'provider_account_note_editor.dart';
import 'provider_account_token_details.dart';
import 'provider_account_facts.dart';
import 'workbench_controller.dart';
import 'control_failure_notice.dart';

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
    final query = _search.text.trim().toLowerCase();
    final filtered = accounts
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
        .toList(growable: false);

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
          child: TextField(
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
                  builder: (context, constraints) => ListView.separated(
                    key: const Key('provider-accounts-list'),
                    padding: const EdgeInsets.only(bottom: 24),
                    itemCount: filtered.length,
                    separatorBuilder: (_, _) => const Divider(height: 1),
                    itemBuilder: (context, index) {
                      final account = filtered[index];
                      final linkedEndpoints = endpoints.where(
                        (value) => account.isLinkedTo(value.id),
                      );
                      return Padding(
                        key: Key('provider-account-${account.id}'),
                        padding: const EdgeInsets.symmetric(vertical: 10),
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
                            ProviderAccountTokenDetails(
                              account: account,
                              copy: copy,
                            ),
                            ProviderAccountFactsPanel(
                              account: account,
                              controller: controller,
                              copy: copy,
                            ),
                            if (linkedEndpoints.isEmpty)
                              Padding(
                                padding: const EdgeInsets.only(
                                  left: 36,
                                  top: 6,
                                ),
                                child: TextButton.icon(
                                  onPressed: () => controller.selectSection(
                                    WorkbenchSection.routes,
                                  ),
                                  icon: const Icon(Icons.add_link, size: 14),
                                  label: Text(
                                    copy('provider_accounts.unlinked'),
                                  ),
                                ),
                              ),
                            for (final endpoint in linkedEndpoints)
                              Padding(
                                padding: const EdgeInsets.only(
                                  left: 36,
                                  top: 6,
                                ),
                                child: TextButton.icon(
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
}

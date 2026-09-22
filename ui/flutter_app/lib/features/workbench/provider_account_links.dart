import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

Future<void> showEndpointAccountLinker(
  BuildContext context, {
  required WorkbenchController controller,
  required UpstreamEndpoint endpoint,
  required AppCopy copy,
}) => showDialog<void>(
  context: context,
  builder: (_) => _AccountLinkDialog(
    controller: controller,
    endpoint: endpoint,
    copy: copy,
  ),
);

final class _AccountLinkDialog extends StatefulWidget {
  const _AccountLinkDialog({
    required this.controller,
    required this.endpoint,
    required this.copy,
  });
  final WorkbenchController controller;
  final UpstreamEndpoint endpoint;
  final AppCopy copy;
  @override
  State<_AccountLinkDialog> createState() => _AccountLinkDialogState();
}

final class _AccountLinkDialogState extends State<_AccountLinkDialog> {
  final _search = TextEditingController();
  String? _linking;
  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: widget.controller,
    builder: (context, _) => _buildDialog(context),
  );

  Widget _buildDialog(BuildContext context) {
    final copy = widget.copy;
    final controller = widget.controller;
    final accounts = controller.data?.accounts ?? const <ProviderAccount>[];
    final endpoints = controller.data?.endpoints ?? const <UpstreamEndpoint>[];
    final query = _search.text.trim().toLowerCase();
    // Compatibility controls the action, not whether an account is visible.
    // In particular, an already-linked account must not look like a missing one.
    final candidates =
        accounts
            .where(
              (account) => [
                account.displayName,
                account.note,
                account.codexOAuth?.email ?? '',
                account.credentialOrigin,
                copy('routes.account.kind.${account.kind}'),
                ...endpoints
                    .where(
                      (endpoint) =>
                          account.isLinkedTo(endpoint.id) ||
                          account.credentialOrigin ==
                              endpoint.origin.toString(),
                    )
                    .map((endpoint) => endpoint.displayName),
              ].any((value) => value.toLowerCase().contains(query)),
            )
            .toList(growable: false)
          ..sort((left, right) {
            final priority = _priority(left).compareTo(_priority(right));
            if (priority != 0) return priority;
            final name = left.displayName.toLowerCase().compareTo(
              right.displayName.toLowerCase(),
            );
            return name != 0 ? name : left.id.compareTo(right.id);
          });
    return AlertDialog(
      title: Text(copy('provider_accounts.link')),
      content: SizedBox(
        width: ViberMetrics.dialogStandardWidth,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              widget.endpoint.displayName,
              style: Theme.of(context).textTheme.titleMedium,
            ),
            Text(widget.endpoint.origin.toString(), style: monoStyle),
            const SizedBox(height: 12),
            Text(
              copy('provider_accounts.link_hint'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 12),
            TextField(
              key: const Key('account-link-search'),
              controller: _search,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                hintText: copy('provider_accounts.search'),
                prefixIcon: const Icon(Icons.search, size: 18),
                suffixIcon: _search.text.isEmpty
                    ? null
                    : IconButton(
                        key: const Key('account-link-clear-search'),
                        tooltip: copy('provider_accounts.clear_search'),
                        onPressed: () => setState(_search.clear),
                        icon: const Icon(Icons.close, size: 16),
                      ),
              ),
            ),
            if (controller.inventoryError case final error?)
              InlineNotice(message: copy.maybe(error) ?? error, error: true),
            const SizedBox(height: 12),
            Flexible(
              child: candidates.isEmpty
                  ? Padding(
                      padding: const EdgeInsets.symmetric(vertical: 20),
                      child: Text(
                        copy(
                          accounts.isEmpty
                              ? 'provider_accounts.link_empty'
                              : 'provider_accounts.no_results',
                        ),
                      ),
                    )
                  : ListView.separated(
                      key: const Key('account-link-list'),
                      shrinkWrap: true,
                      itemCount: candidates.length,
                      separatorBuilder: (_, _) => const Divider(height: 1),
                      itemBuilder: (context, index) {
                        final account = candidates[index];
                        return _AccountLinkRow(
                          key: Key('account-link-row-${account.id}'),
                          account: account,
                          endpoint: widget.endpoint,
                          copy: copy,
                          busy:
                              _linking != null || controller.inventoryMutating,
                          linking: _linking == account.id,
                          onLink: () => _link(account),
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          key: const Key('account-link-manage'),
          onPressed: _linking != null
              ? null
              : () {
                  Navigator.pop(context);
                  controller.selectSection(WorkbenchSection.providerAccounts);
                },
          child: Text(copy('provider_accounts.manage')),
        ),
        TextButton(
          onPressed: _linking != null ? null : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
      ],
    );
  }

  int _priority(ProviderAccount account) {
    if (account.isLinkedTo(widget.endpoint.id)) return 1;
    return account.canLinkTo(widget.endpoint) ? 0 : 2;
  }

  Future<void> _link(ProviderAccount account) async {
    setState(() => _linking = account.id);
    final linked = await widget.controller.setProviderAccountAssociation(
      account: account,
      endpoint: widget.endpoint,
      linked: true,
    );
    if (!mounted) return;
    if (linked) {
      Navigator.pop(context);
    } else {
      setState(() => _linking = null);
    }
  }
}

final class _AccountLinkRow extends StatelessWidget {
  const _AccountLinkRow({
    super.key,
    required this.account,
    required this.endpoint,
    required this.copy,
    required this.busy,
    required this.linking,
    required this.onLink,
  });

  final ProviderAccount account;
  final UpstreamEndpoint endpoint;
  final AppCopy copy;
  final bool busy;
  final bool linking;
  final VoidCallback onLink;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final linked = account.isLinkedTo(endpoint.id);
    final available = !linked && account.canLinkTo(endpoint);
    final action = available
        ? OutlinedButton.icon(
            key: Key('account-link-${account.id}'),
            onPressed: busy ? null : onLink,
            icon: linking
                ? const SizedBox.square(
                    dimension: 16,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Icon(Icons.link, size: 16),
            label: Text(copy('provider_accounts.link')),
          )
        : Text(
            copy(
              linked
                  ? 'provider_accounts.already_linked'
                  : 'provider_accounts.link_unavailable',
            ),
            key: Key('account-link-status-${account.id}'),
            style: Theme.of(context).textTheme.labelMedium?.copyWith(
              color: linked ? colors.verified : colors.textMuted,
            ),
          );
    final details = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          account.displayName,
          style: Theme.of(context).textTheme.titleSmall,
        ),
        if (account.note.isNotEmpty)
          Text(
            account.note,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        if (account.codexOAuth?.email case final email?
            when email != account.displayName)
          Text(email, style: Theme.of(context).textTheme.bodySmall),
        const SizedBox(height: 4),
        Text(
          copy('routes.account.kind.${account.kind}'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        Text(
          account.credentialOrigin,
          style: monoStyle.copyWith(color: colors.textMuted),
        ),
        if (!linked && !available) ...[
          const SizedBox(height: 4),
          Text(
            copy(_unavailableReason),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ] else if (!account.usable) ...[
          const SizedBox(height: 4),
          Text(
            copy('routes.credentials.unavailable'),
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(color: colors.warning),
          ),
        ],
      ],
    );
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 12),
      child: LayoutBuilder(
        builder: (context, constraints) {
          // Keep names, service addresses and reasons readable in narrow views.
          final stacked = constraints.maxWidth < 440;
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.only(top: 2),
                child: Icon(
                  linked ? Icons.check_circle_outline : Icons.key_outlined,
                  size: 18,
                  color: linked
                      ? colors.verified
                      : available
                      ? colors.route
                      : colors.textMuted,
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: stacked
                    ? Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [details, const SizedBox(height: 8), action],
                      )
                    : details,
              ),
              if (!stacked) ...[const SizedBox(width: 16), action],
            ],
          );
        },
      ),
    );
  }

  String get _unavailableReason {
    if (account.credentialOrigin != endpoint.origin.toString()) {
      return 'provider_accounts.link_origin_mismatch';
    }
    if (!endpoint.accountKinds.contains(account.kind)) {
      return 'provider_accounts.link_kind_mismatch';
    }
    return 'provider_accounts.link_service_inactive';
  }
}

Future<void> showAccountUnlinkConfirmation(
  BuildContext context, {
  required WorkbenchController controller,
  required UpstreamEndpoint endpoint,
  required ProviderAccount account,
  required AppCopy copy,
}) => showDialog<void>(
  context: context,
  builder: (context) {
    var busy = false;
    return StatefulBuilder(
      builder: (context, setState) => AlertDialog(
        title: Text(copy('provider_accounts.unlink')),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('${endpoint.displayName} → ${account.displayName}'),
            const SizedBox(height: 12),
            Text(copy('provider_accounts.unlink_hint')),
            if (controller.inventoryError case final error?)
              InlineNotice(message: copy.maybe(error) ?? error, error: true),
          ],
        ),
        actions: [
          TextButton(
            onPressed: busy ? null : () => Navigator.pop(context),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            key: const Key('account-unlink-confirm'),
            onPressed: busy
                ? null
                : () async {
                    setState(() => busy = true);
                    final done = await controller.setProviderAccountAssociation(
                      account: account,
                      endpoint: endpoint,
                      linked: false,
                    );
                    if (!context.mounted) return;
                    if (done) {
                      Navigator.pop(context);
                    } else {
                      setState(() => busy = false);
                    }
                  },
            child: Text(copy('provider_accounts.unlink')),
          ),
        ],
      ),
    );
  },
);

final class LinkedProviderAccountRow extends StatelessWidget {
  const LinkedProviderAccountRow({
    super.key,
    required this.account,
    required this.copy,
    required this.busy,
    required this.onManage,
    required this.onUnlink,
  });
  final ProviderAccount account;
  final AppCopy copy;
  final bool busy;
  final VoidCallback onManage;
  final VoidCallback onUnlink;
  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
    child: Row(
      children: [
        Icon(Icons.link, size: 18, color: context.viberColors.route),
        const SizedBox(width: 12),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                account.displayName,
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 4),
              Text(
                copy('routes.account.kind.${account.kind}'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: 4),
              InlineStatus(
                label: copy(
                  account.usable
                      ? 'routes.credentials.ready'
                      : 'routes.credentials.unavailable',
                ),
                color: account.usable
                    ? context.viberColors.verified
                    : context.viberColors.warning,
              ),
            ],
          ),
        ),
        IconButton(
          key: Key('linked-account-manage-${account.id}'),
          tooltip: copy('provider_accounts.manage'),
          onPressed: busy ? null : onManage,
          icon: const Icon(Icons.open_in_new, size: 18),
        ),
        IconButton(
          key: Key('account-unlink-${account.id}'),
          tooltip: copy('provider_accounts.unlink'),
          onPressed: busy ? null : onUnlink,
          icon: const Icon(Icons.link_off, size: 18),
        ),
      ],
    ),
  );
}

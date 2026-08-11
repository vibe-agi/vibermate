import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class EndpointsView extends StatefulWidget {
  const EndpointsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<EndpointsView> createState() => _EndpointsViewState();
}

final class _EndpointsViewState extends State<EndpointsView> {
  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final endpoints = controller.data?.endpoints ?? const <UpstreamEndpoint>[];
    final accounts = controller.data?.accounts ?? const <ProviderAccount>[];
    return Column(
      children: [
        PageHeading(
          title: copy('routes.title'),
          subtitle: copy('routes.subtitle'),
          trailing: FilledButton.icon(
            key: const Key('endpoints-add'),
            onPressed: controller.inventoryMutating
                ? null
                : () => _openEndpointEditor(context),
            icon: const Icon(Icons.add, size: 14),
            label: Text(copy('routes.add_endpoint')),
          ),
        ),
        const Divider(height: 1),
        if (controller.inventoryError case final error?)
          InlineNotice(message: error, error: true),
        if (controller.inventoryNotice case final notice?)
          InlineNotice(
            message: copy('notice.inventory.$notice'),
            onDismiss: controller.clearInventoryNotice,
          ),
        Expanded(
          child: LayoutBuilder(
            builder: (context, constraints) {
              final compact = constraints.maxWidth < 700;
              final directory = _EndpointDirectory(
                endpoints: endpoints,
                accounts: accounts,
                selectedId: controller.selectedEndpointId,
                onSelected: controller.selectEndpoint,
                horizontal: compact,
                copy: copy,
              );
              final detail = _EndpointDetail(
                endpoint: controller.selectedEndpoint,
                accounts: accounts,
                compact: compact,
                copy: copy,
                busy: controller.inventoryMutating,
                onAddAccount: (endpoint) =>
                    _openAccountEditor(context, endpoint),
                onReplaceCredential: (endpoint, account) =>
                    _openAccountEditor(context, endpoint, account: account),
                onDeleteAccount: (account) =>
                    _openDeleteAccount(context, account),
              );
              if (compact) {
                return Column(
                  children: [
                    SizedBox(height: 118, child: directory),
                    const Divider(height: 1),
                    Expanded(child: detail),
                  ],
                );
              }
              return Row(
                children: [
                  SizedBox(width: 278, child: directory),
                  const VerticalDivider(width: 1),
                  Expanded(child: detail),
                ],
              );
            },
          ),
        ),
      ],
    );
  }

  void _openEndpointEditor(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _EndpointEditorDialog(
          controller: widget.controller,
          copy: widget.copy,
        ),
      ),
    );
  }

  void _openAccountEditor(
    BuildContext context,
    UpstreamEndpoint endpoint, {
    ProviderAccount? account,
  }) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _AccountEditorDialog(
          controller: widget.controller,
          endpoint: endpoint,
          account: account,
          copy: widget.copy,
        ),
      ),
    );
  }

  void _openDeleteAccount(BuildContext context, ProviderAccount account) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _DeleteAccountDialog(
          controller: widget.controller,
          account: account,
          copy: widget.copy,
        ),
      ),
    );
  }
}

final class _EndpointDirectory extends StatelessWidget {
  const _EndpointDirectory({
    required this.endpoints,
    required this.accounts,
    required this.selectedId,
    required this.onSelected,
    required this.horizontal,
    required this.copy,
  });

  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final String? selectedId;
  final ValueChanged<String> onSelected;
  final bool horizontal;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return ColoredBox(
      color: ViberColors.panel,
      child: ListView.builder(
        scrollDirection: horizontal ? Axis.horizontal : Axis.vertical,
        padding: EdgeInsets.symmetric(
          horizontal: horizontal ? 8 : 0,
          vertical: horizontal ? 9 : 5,
        ),
        itemCount: endpoints.length,
        itemBuilder: (context, index) {
          final endpoint = endpoints[index];
          final count = accounts
              .where((account) => account.upstreamEndpointId == endpoint.id)
              .length;
          final selected = endpoint.id == selectedId;
          return Semantics(
            selected: selected,
            button: true,
            label: '${endpoint.displayName}, $count accounts',
            child: Material(
              color: selected ? ViberColors.selection : Colors.transparent,
              child: InkWell(
                onTap: () => onSelected(endpoint.id),
                child: Container(
                  width: horizontal ? 215 : null,
                  height: horizontal ? null : 68,
                  margin: horizontal
                      ? const EdgeInsets.only(right: 7)
                      : EdgeInsets.zero,
                  padding: const EdgeInsets.symmetric(
                    horizontal: 11,
                    vertical: 8,
                  ),
                  decoration: BoxDecoration(
                    border: Border(
                      left: BorderSide(
                        color: selected
                            ? ViberColors.route
                            : Colors.transparent,
                        width: 2,
                      ),
                      bottom: const BorderSide(color: ViberColors.dividerSoft),
                    ),
                  ),
                  child: Column(
                    mainAxisAlignment: MainAxisAlignment.center,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              endpoint.displayName,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          Container(
                            width: 6,
                            height: 6,
                            decoration: BoxDecoration(
                              color: endpoint.state == 'active'
                                  ? ViberColors.verified
                                  : ViberColors.textFaint,
                              shape: BoxShape.circle,
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 3),
                      Text(
                        copy.format('routes.accounts', {'count': count}),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                      Text(
                        endpoint.origin.host,
                        overflow: TextOverflow.ellipsis,
                        style: monoStyle,
                      ),
                    ],
                  ),
                ),
              ),
            ),
          );
        },
      ),
    );
  }
}

final class _EndpointDetail extends StatelessWidget {
  const _EndpointDetail({
    required this.endpoint,
    required this.accounts,
    required this.compact,
    required this.copy,
    required this.busy,
    required this.onAddAccount,
    required this.onReplaceCredential,
    required this.onDeleteAccount,
  });

  final UpstreamEndpoint? endpoint;
  final List<ProviderAccount> accounts;
  final bool compact;
  final AppCopy copy;
  final bool busy;
  final ValueChanged<UpstreamEndpoint> onAddAccount;
  final void Function(UpstreamEndpoint, ProviderAccount) onReplaceCredential;
  final ValueChanged<ProviderAccount> onDeleteAccount;

  @override
  Widget build(BuildContext context) {
    final value = endpoint;
    if (value == null) {
      return CenteredMessage(
        icon: Icons.hub_outlined,
        title: copy('routes.select_endpoint'),
      );
    }
    final ownedAccounts = accounts
        .where((account) => account.upstreamEndpointId == value.id)
        .toList(growable: false);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: double.infinity,
          color: ViberColors.panel,
          padding: const EdgeInsets.fromLTRB(16, 11, 12, 10),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      value.displayName,
                      style: Theme.of(context).textTheme.headlineSmall,
                    ),
                  ),
                  StatusPill(
                    label: value.state,
                    color: value.state == 'active'
                        ? ViberColors.verified
                        : ViberColors.textFaint,
                  ),
                  const SizedBox(width: 7),
                  OutlinedButton.icon(
                    key: const Key('accounts-add'),
                    onPressed: value.state == 'active' && !busy
                        ? () => onAddAccount(value)
                        : null,
                    icon: const Icon(Icons.add, size: 14),
                    label: Text(copy('routes.add_account')),
                  ),
                ],
              ),
              const SizedBox(height: 3),
              Text(
                value.origin.toString(),
                style: monoStyle.copyWith(color: ViberColors.text),
              ),
              const SizedBox(height: 7),
              Wrap(
                spacing: 6,
                runSpacing: 5,
                children: [
                  StatusPill(
                    label: value.realmId,
                    color: ViberColors.route,
                    icon: Icons.public,
                  ),
                  for (final protocol in value.backendProtocols)
                    StatusPill(label: protocol, color: ViberColors.textMuted),
                ],
              ),
            ],
          ),
        ),
        const Divider(height: 1),
        SectionLabel(label: copy('flow.account'), count: ownedAccounts.length),
        const Divider(height: 1),
        Expanded(
          child: ownedAccounts.isEmpty
              ? CenteredMessage(
                  icon: Icons.key_off_outlined,
                  title: copy('routes.no_accounts'),
                  detail: copy('routes.no_accounts.detail'),
                )
              : ListView.separated(
                  padding: const EdgeInsets.only(bottom: 16),
                  itemCount: ownedAccounts.length,
                  separatorBuilder: (context, index) =>
                      const Divider(height: 1),
                  itemBuilder: (context, index) => _AccountRow(
                    account: ownedAccounts[index],
                    compact: compact,
                    copy: copy,
                    busy: busy,
                    onReplace: () =>
                        onReplaceCredential(value, ownedAccounts[index]),
                    onDelete: () => onDeleteAccount(ownedAccounts[index]),
                  ),
                ),
        ),
      ],
    );
  }
}

final class _AccountRow extends StatelessWidget {
  const _AccountRow({
    required this.account,
    required this.compact,
    required this.copy,
    required this.busy,
    required this.onReplace,
    required this.onDelete,
  });

  final ProviderAccount account;
  final bool compact;
  final AppCopy copy;
  final bool busy;
  final VoidCallback onReplace;
  final VoidCallback onDelete;

  @override
  Widget build(BuildContext context) {
    final credentialLabel = account.usable
        ? copy('routes.credentials.ready')
        : copy('routes.credentials.unavailable');
    final actions = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        IconButton(
          key: Key('account-update-${account.id}'),
          onPressed: busy ? null : onReplace,
          tooltip: copy('routes.update_credential'),
          icon: const Icon(Icons.key_outlined, size: 15),
        ),
        IconButton(
          key: Key('account-delete-${account.id}'),
          onPressed: busy ? null : onDelete,
          tooltip: copy('routes.delete_account'),
          icon: const Icon(
            Icons.delete_outline,
            size: 15,
            color: ViberColors.danger,
          ),
        ),
      ],
    );
    if (compact) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(13, 9, 5, 9),
        child: Row(
          children: [
            const Icon(Icons.key, size: 14, color: ViberColors.verified),
            const SizedBox(width: 7),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          account.displayName,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                      ),
                      StatusPill(
                        label: credentialLabel,
                        color: account.usable
                            ? ViberColors.verified
                            : ViberColors.danger,
                      ),
                    ],
                  ),
                  const SizedBox(height: 4),
                  Text(
                    '${account.kind}  ·  epoch ${account.credentialEpoch}',
                    style: monoStyle,
                  ),
                  Text(
                    account.id,
                    overflow: TextOverflow.ellipsis,
                    style: monoStyle,
                  ),
                ],
              ),
            ),
            actions,
          ],
        ),
      );
    }
    return SizedBox(
      height: 51,
      child: Padding(
        padding: const EdgeInsets.only(left: 14, right: 5),
        child: Row(
          children: [
            const Icon(Icons.key, size: 14, color: ViberColors.verified),
            const SizedBox(width: 8),
            SizedBox(
              width: 190,
              child: Column(
                mainAxisAlignment: MainAxisAlignment.center,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    account.displayName,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                  Text(
                    account.id,
                    overflow: TextOverflow.ellipsis,
                    style: monoStyle,
                  ),
                ],
              ),
            ),
            Expanded(
              child: Text(
                account.kind,
                overflow: TextOverflow.ellipsis,
                style: monoStyle,
              ),
            ),
            SizedBox(
              width: 76,
              child: Text('epoch ${account.credentialEpoch}', style: monoStyle),
            ),
            StatusPill(
              label: credentialLabel,
              color: account.usable ? ViberColors.verified : ViberColors.danger,
            ),
            const SizedBox(width: 4),
            actions,
          ],
        ),
      ),
    );
  }
}

final class _EndpointEditorDialog extends StatefulWidget {
  const _EndpointEditorDialog({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_EndpointEditorDialog> createState() => _EndpointEditorDialogState();
}

final class _EndpointEditorDialogState extends State<_EndpointEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  final _name = TextEditingController();
  final _origin = TextEditingController();
  String _kind = 'anthropic';
  bool _submitted = false;

  @override
  void dispose() {
    _name.dispose();
    _origin.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return AlertDialog(
      title: Text(copy('routes.endpoint.create.title')),
      content: SizedBox(
        width: 430,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextFormField(
                  key: const Key('endpoint-editor-name'),
                  controller: _name,
                  autofocus: true,
                  maxLength: 256,
                  decoration: InputDecoration(
                    labelText: copy('routes.endpoint.name'),
                  ),
                  validator: (value) => value == null || value.trim().isEmpty
                      ? copy('routes.validation.required')
                      : null,
                ),
                const SizedBox(height: 7),
                TextFormField(
                  key: const Key('endpoint-editor-origin'),
                  controller: _origin,
                  autocorrect: false,
                  enableSuggestions: false,
                  decoration: InputDecoration(
                    labelText: copy('routes.endpoint.origin'),
                    hintText: 'https://relay.example.com',
                  ),
                  validator: (value) => _canonicalProviderOrigin(value ?? '')
                      ? null
                      : copy('routes.validation.origin'),
                ),
                const SizedBox(height: 9),
                DropdownButtonFormField<String>(
                  initialValue: _kind,
                  decoration: InputDecoration(
                    labelText: copy('routes.endpoint.protocol'),
                  ),
                  items: [
                    for (final kind in const ['anthropic', 'openai_compatible'])
                      DropdownMenuItem(
                        value: kind,
                        child: Text(copy('routes.endpoint.kind.$kind')),
                      ),
                  ],
                  onChanged: (value) => setState(() => _kind = value!),
                ),
                const SizedBox(height: 10),
                Text(
                  copy('routes.endpoint.boundary'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (_submitted && widget.controller.inventoryError != null) ...[
                  const SizedBox(height: 9),
                  InlineNotice(
                    message: widget.controller.inventoryError!,
                    error: true,
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.inventoryMutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('endpoint-editor-save'),
          onPressed: widget.controller.inventoryMutating ? null : _submit,
          child: widget.controller.inventoryMutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(copy('routes.endpoint.create.action')),
        ),
      ],
    );
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final created = await widget.controller.createUpstreamEndpoint(
      displayName: _name.text,
      origin: _origin.text,
      kind: _kind,
    );
    if (!mounted) return;
    if (created != null) {
      Navigator.pop(context);
    } else {
      setState(() {});
    }
  }
}

final class _AccountEditorDialog extends StatefulWidget {
  const _AccountEditorDialog({
    required this.controller,
    required this.endpoint,
    required this.account,
    required this.copy,
  });

  final WorkbenchController controller;
  final UpstreamEndpoint endpoint;
  final ProviderAccount? account;
  final AppCopy copy;

  @override
  State<_AccountEditorDialog> createState() => _AccountEditorDialogState();
}

final class _AccountEditorDialogState extends State<_AccountEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  final _secret = TextEditingController();
  late String _kind;
  bool _submitted = false;

  bool get _replacing => widget.account != null;

  @override
  void initState() {
    super.initState();
    _name = TextEditingController(text: widget.account?.displayName ?? '');
    _kind = widget.account?.kind ?? widget.endpoint.accountKinds.first;
  }

  @override
  void dispose() {
    _secret.clear();
    _secret.dispose();
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return AlertDialog(
      title: Text(
        _replacing
            ? copy.format('routes.account.replace.title', {
                'name': widget.account!.displayName,
              })
            : copy('routes.account.create.title'),
      ),
      content: SizedBox(
        width: 430,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                _AuthorityLine(
                  icon: Icons.hub_outlined,
                  label: widget.endpoint.displayName,
                  detail: widget.endpoint.origin.toString(),
                ),
                const SizedBox(height: 10),
                if (!_replacing) ...[
                  DropdownButtonFormField<String>(
                    key: const Key('account-editor-kind'),
                    initialValue: _kind,
                    decoration: InputDecoration(
                      labelText: copy('routes.account.kind'),
                    ),
                    items: [
                      for (final kind in widget.endpoint.accountKinds)
                        DropdownMenuItem(
                          value: kind,
                          child: Text(copy('routes.account.kind.$kind')),
                        ),
                    ],
                    onChanged: (value) => setState(() => _kind = value!),
                  ),
                  const SizedBox(height: 9),
                  TextFormField(
                    key: const Key('account-editor-name'),
                    controller: _name,
                    maxLength: 256,
                    decoration: InputDecoration(
                      labelText: copy('routes.account.name'),
                    ),
                    validator: (value) => value == null || value.trim().isEmpty
                        ? copy('routes.validation.required')
                        : null,
                  ),
                  const SizedBox(height: 7),
                ],
                TextFormField(
                  key: const Key('account-editor-secret'),
                  controller: _secret,
                  autofocus: _replacing,
                  obscureText: true,
                  autocorrect: false,
                  enableSuggestions: false,
                  decoration: InputDecoration(
                    labelText: copy(
                      _kind == 'claude_oauth_token'
                          ? 'routes.account.oauth_token'
                          : 'routes.account.api_key',
                    ),
                  ),
                  validator: (value) =>
                      value == null ||
                          value.isEmpty ||
                          value.contains(RegExp(r'[\u0000\r\n]'))
                      ? copy('routes.validation.secret')
                      : null,
                ),
                const SizedBox(height: 10),
                Text(
                  copy('routes.account.secret_boundary'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (_submitted && widget.controller.inventoryError != null) ...[
                  const SizedBox(height: 9),
                  InlineNotice(
                    message: widget.controller.inventoryError!,
                    error: true,
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.inventoryMutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('account-editor-save'),
          onPressed: widget.controller.inventoryMutating ? null : _submit,
          child: widget.controller.inventoryMutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(
                  copy(
                    _replacing
                        ? 'routes.account.replace.action'
                        : 'routes.account.create.action',
                  ),
                ),
        ),
      ],
    );
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final secret = _secret.text;
    final result = _replacing
        ? await widget.controller.replaceProviderAccountCredential(
            account: widget.account!,
            secret: secret,
          )
        : await widget.controller.createProviderAccount(
            endpoint: widget.endpoint,
            displayName: _name.text,
            kind: _kind,
            secret: secret,
          );
    _secret.clear();
    if (!mounted) return;
    if (result != null) {
      Navigator.pop(context);
    } else {
      setState(() {});
    }
  }
}

final class _DeleteAccountDialog extends StatefulWidget {
  const _DeleteAccountDialog({
    required this.controller,
    required this.account,
    required this.copy,
  });

  final WorkbenchController controller;
  final ProviderAccount account;
  final AppCopy copy;

  @override
  State<_DeleteAccountDialog> createState() => _DeleteAccountDialogState();
}

final class _DeleteAccountDialogState extends State<_DeleteAccountDialog> {
  ProviderAccountDeleteResult? _blocked;
  bool _submitted = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final blocked = _blocked;
    return AlertDialog(
      title: Text(
        copy.format('routes.account.delete.title', {
          'name': widget.account.displayName,
        }),
      ),
      content: SizedBox(
        width: 440,
        child: SingleChildScrollView(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                copy('routes.account.delete.detail'),
                style: Theme.of(context).textTheme.bodyMedium,
              ),
              const SizedBox(height: 8),
              Text(
                '${widget.account.id}  ·  epoch ${widget.account.credentialEpoch}',
                style: monoStyle,
              ),
              if (blocked != null) ...[
                const SizedBox(height: 10),
                Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(9),
                  decoration: BoxDecoration(
                    color: ViberColors.danger.withValues(alpha: 0.08),
                    border: Border.all(
                      color: ViberColors.danger.withValues(alpha: 0.35),
                    ),
                    borderRadius: BorderRadius.circular(4),
                  ),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy('routes.account.delete.blocked'),
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                      const SizedBox(height: 5),
                      for (final reference in blocked.references)
                        Padding(
                          padding: const EdgeInsets.only(bottom: 4),
                          child: Text(
                            copy.format('routes.account.delete.reference', {
                              'environment': reference.environmentName,
                              'revision': reference.environmentRevision,
                              'route': reference.routeId,
                            }),
                            style: monoStyle,
                          ),
                        ),
                      if (blocked.referenceCount > blocked.references.length)
                        Text(
                          copy.format('routes.account.delete.more', {
                            'count':
                                blocked.referenceCount -
                                blocked.references.length,
                          }),
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                    ],
                  ),
                ),
              ],
              if (_submitted &&
                  blocked == null &&
                  widget.controller.inventoryError != null) ...[
                const SizedBox(height: 9),
                InlineNotice(
                  message: widget.controller.inventoryError!,
                  error: true,
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.inventoryMutating
              ? null
              : () => Navigator.pop(context),
          child: Text(
            blocked == null ? copy('common.cancel') : copy('common.confirm'),
          ),
        ),
        if (blocked == null)
          FilledButton(
            key: const Key('account-delete-confirm'),
            onPressed: widget.controller.inventoryMutating ? null : _delete,
            style: FilledButton.styleFrom(backgroundColor: ViberColors.danger),
            child: widget.controller.inventoryMutating
                ? const SizedBox.square(
                    dimension: 13,
                    child: CircularProgressIndicator(strokeWidth: 1.5),
                  )
                : Text(copy('routes.account.delete.action')),
          ),
      ],
    );
  }

  Future<void> _delete() async {
    setState(() => _submitted = true);
    final result = await widget.controller.deleteProviderAccount(
      widget.account,
    );
    if (!mounted || result == null) {
      if (mounted) setState(() {});
      return;
    }
    if (result.deleted) {
      Navigator.pop(context);
    } else {
      setState(() => _blocked = result);
    }
  }
}

final class _AuthorityLine extends StatelessWidget {
  const _AuthorityLine({
    required this.icon,
    required this.label,
    required this.detail,
  });

  final IconData icon;
  final String label;
  final String detail;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
      decoration: BoxDecoration(
        color: ViberColors.route.withValues(alpha: 0.07),
        border: Border.all(color: ViberColors.route.withValues(alpha: 0.25)),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Row(
        children: [
          Icon(icon, size: 15, color: ViberColors.route),
          const SizedBox(width: 7),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(label, style: Theme.of(context).textTheme.titleSmall),
                Text(detail, style: monoStyle),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

bool _canonicalProviderOrigin(String value) {
  final parsed = Uri.tryParse(value);
  return parsed != null &&
      parsed.scheme == 'https' &&
      parsed.host.isNotEmpty &&
      parsed.userInfo.isEmpty &&
      !parsed.hasQuery &&
      !parsed.hasFragment &&
      (parsed.path.isEmpty || parsed.path == '/') &&
      parsed.toString() == parsed.origin;
}

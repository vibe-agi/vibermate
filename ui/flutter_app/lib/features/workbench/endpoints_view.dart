import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import 'deletion_dialog.dart';
import 'control_failure_notice.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'provider_account_links.dart';
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
          help: copy('routes.subtitle'),
          dismissHelpLabel: copy('common.dismiss'),
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
          ControlFailureNotice(
            message: error,
            copy: copy,
            diagnostic: controller.inventoryErrorDiagnostic,
          ),
        if (controller.inventoryNotice case final notice?)
          InlineNotice(
            message: copy('notice.inventory.$notice'),
            onDismiss: controller.clearInventoryNotice,
            dismissLabel: copy('common.dismiss'),
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
                onLinkAccount: (endpoint) =>
                    _openAccountLinker(context, endpoint),
                onManageAccount: () =>
                    controller.selectSection(WorkbenchSection.providerAccounts),
                onDeleteEndpoint: () => _confirmDeleteEndpoint(context),
                onUnlinkAccount: (endpoint, account) => unawaited(
                  showAccountUnlinkConfirmation(
                    context,
                    controller: controller,
                    endpoint: endpoint,
                    account: account,
                    copy: copy,
                  ),
                ),
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

  void _openAccountLinker(BuildContext context, UpstreamEndpoint endpoint) {
    widget.controller.clearInventoryNotice();
    unawaited(
      showEndpointAccountLinker(
        context,
        controller: widget.controller,
        endpoint: endpoint,
        copy: widget.copy,
      ),
    );
  }

  void _confirmDeleteEndpoint(BuildContext context) {
    final endpoint = widget.controller.data?.endpoints
        .where((value) => value.id == widget.controller.selectedEndpointId)
        .firstOrNull;
    if (endpoint == null) return;
    unawaited(
      showDialog<DeletionOutcome>(
        context: context,
        builder: (_) => DeletionConfirmation(
          copy: widget.copy,
          title: widget.copy('deletion.endpoint.title'),
          subject: endpoint.displayName,
          consequence: widget.copy('deletion.endpoint.consequence'),
          onConfirm: () async {
            final result = await widget.controller.deleteUpstreamEndpoint(
              endpoint.id,
            );
            if (result == null) {
              throw widget.controller.inventoryFailure;
            }
            return result;
          },
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
      color: context.viberColors.panel,
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
              .where((account) => account.isLinkedTo(endpoint.id))
              .length;
          final selected = endpoint.id == selectedId;
          return Semantics(
            selected: selected,
            button: true,
            label:
                '${endpoint.displayName}, '
                '${copy.format('routes.accounts', {'count': '$count'})}',
            child: Material(
              color: selected
                  ? context.viberColors.selection
                  : Colors.transparent,
              child: InkWell(
                onTap: () => onSelected(endpoint.id),
                child: Container(
                  width: horizontal ? 215 : null,
                  constraints: horizontal
                      ? null
                      : const BoxConstraints(minHeight: 68),
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
                            ? context.viberColors.route
                            : Colors.transparent,
                        width: 2,
                      ),
                      bottom: BorderSide(
                        color: context.viberColors.dividerSoft,
                      ),
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
                          if (endpoint.state != 'active') ...[
                            const SizedBox(width: 6),
                            InlineStatus(
                              label: _localizedCopy(
                                copy,
                                'environment.state',
                                endpoint.state,
                              ),
                              color: context.viberColors.textFaint,
                            ),
                          ],
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
    required this.onLinkAccount,
    required this.onManageAccount,
    required this.onUnlinkAccount,
    required this.onDeleteEndpoint,
  });

  final UpstreamEndpoint? endpoint;
  final List<ProviderAccount> accounts;
  final bool compact;
  final AppCopy copy;
  final bool busy;
  final ValueChanged<UpstreamEndpoint> onLinkAccount;
  final VoidCallback onManageAccount;
  final void Function(UpstreamEndpoint, ProviderAccount) onUnlinkAccount;
  final VoidCallback onDeleteEndpoint;

  @override
  Widget build(BuildContext context) {
    final value = endpoint;
    if (value == null) {
      return CenteredMessage(
        icon: Icons.hub_outlined,
        title: copy('routes.select_endpoint'),
      );
    }
    final linkedAccounts = accounts
        .where((account) => account.isLinkedTo(value.id))
        .toList(growable: false);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: double.infinity,
          color: context.viberColors.panel,
          padding: const EdgeInsets.fromLTRB(16, 11, 12, 10),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Flexible(
                              child: Text(
                                value.displayName,
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                                style: Theme.of(
                                  context,
                                ).textTheme.headlineSmall,
                              ),
                            ),
                            const SizedBox(width: 8),
                            IconButton(
                              key: const Key('endpoint-delete'),
                              onPressed: busy ? null : onDeleteEndpoint,
                              tooltip: copy('deletion.endpoint.title'),
                              icon: const Icon(Icons.delete_outline, size: 15),
                              color: context.viberColors.danger,
                              constraints: const BoxConstraints.tightFor(
                                width: 26,
                                height: 26,
                              ),
                              padding: EdgeInsets.zero,
                            ),
                            if (value.state != 'active') ...[
                              const SizedBox(width: 8),
                              InlineStatus(
                                label: _localizedCopy(
                                  copy,
                                  'environment.state',
                                  value.state,
                                ),
                                color: context.viberColors.textFaint,
                              ),
                            ],
                          ],
                        ),
                        const SizedBox(height: 3),
                        Text(
                          value.origin.toString(),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: monoStyle.copyWith(
                            color: context.viberColors.text,
                          ),
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(width: 12),
                  Align(
                    alignment: Alignment.topRight,
                    child: OutlinedButton.icon(
                      key: const Key('accounts-link'),
                      onPressed: value.state == 'active' && !busy
                          ? () => onLinkAccount(value)
                          : null,
                      icon: const Icon(Icons.add, size: 13),
                      label: Text(copy('provider_accounts.link')),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 7),
              Wrap(
                spacing: 6,
                runSpacing: 5,
                children: [
                  for (final protocol in value.backendProtocols)
                    StatusPill(
                      label: _localizedCopy(copy, 'routes.protocol', protocol),
                      color: context.viberColors.textMuted,
                    ),
                ],
              ),
            ],
          ),
        ),
        const Divider(height: 1),
        SectionLabel(
          label: copy('provider_accounts.linked'),
          count: linkedAccounts.length,
        ),
        const Divider(height: 1),
        Expanded(
          child: linkedAccounts.isEmpty
              ? CenteredMessage(
                  icon: Icons.key_off_outlined,
                  title: copy('provider_accounts.none_linked'),
                  action: OutlinedButton.icon(
                    key: const Key('accounts-empty-link'),
                    onPressed: value.state == 'active' && !busy
                        ? () => onLinkAccount(value)
                        : null,
                    icon: const Icon(Icons.add, size: 14),
                    label: Text(copy('provider_accounts.link')),
                  ),
                )
              : ListView.separated(
                  padding: const EdgeInsets.only(bottom: 16),
                  itemCount: linkedAccounts.length,
                  separatorBuilder: (context, index) =>
                      const Divider(height: 1),
                  itemBuilder: (context, index) => LinkedProviderAccountRow(
                    account: linkedAccounts[index],
                    copy: copy,
                    busy: busy,
                    onManage: onManageAccount,
                    onUnlink: () =>
                        onUnlinkAccount(value, linkedAccounts[index]),
                  ),
                ),
        ),
      ],
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
  final Set<String> _backendProtocols = {};
  bool _submitted = false;

  @override
  void initState() {
    super.initState();
    _origin.addListener(_originChanged);
  }

  void _originChanged() {
    if (mounted) setState(() {});
  }

  @override
  void dispose() {
    _origin.removeListener(_originChanged);
    _name.dispose();
    _origin.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return AlertDialog(
      insetPadding: ViberDialogInsets.inset,
      titlePadding: ViberDialogInsets.title,
      contentPadding: ViberDialogInsets.content,
      actionsPadding: ViberDialogInsets.actions,
      title: Text(copy('routes.endpoint.create.title')),
      content: SizedBox(
        key: const Key('endpoint-editor-frame'),
        width: ViberMetrics.dialogCompactWidth,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                CompactLabeledControl(
                  label: copy('routes.endpoint.name'),
                  child: TextFormField(
                    key: const Key('endpoint-editor-name'),
                    controller: _name,
                    autofocus: true,
                    maxLength: 256,
                    textAlignVertical: TextAlignVertical.center,
                    decoration: const InputDecoration(counterText: ''),
                    validator: (value) => value == null || value.trim().isEmpty
                        ? copy('routes.validation.required')
                        : null,
                  ),
                ),
                const SizedBox(height: 8),
                CompactLabeledControl(
                  label: copy('routes.endpoint.origin'),
                  child: TextFormField(
                    key: const Key('endpoint-editor-origin'),
                    controller: _origin,
                    autocorrect: false,
                    enableSuggestions: false,
                    keyboardType: TextInputType.url,
                    smartDashesType: SmartDashesType.disabled,
                    smartQuotesType: SmartQuotesType.disabled,
                    textAlignVertical: TextAlignVertical.center,
                    decoration: const InputDecoration(
                      hintText: 'https://relay.example.com',
                    ),
                    validator: (value) => isCanonicalProviderOrigin(value ?? '')
                        ? null
                        : copy('routes.validation.origin'),
                  ),
                ),
                if (isCleartextProviderOrigin(_origin.text)) ...[
                  const SizedBox(height: 6),
                  Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Icon(
                        Icons.warning_amber_rounded,
                        size: 14,
                        color: context.viberColors.warning,
                      ),
                      const SizedBox(width: 6),
                      Expanded(
                        child: Text(
                          copy('routes.endpoint.cleartext_warning'),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.warning),
                        ),
                      ),
                    ],
                  ),
                ],
                const SizedBox(height: 8),
                CompactLabeledControl(
                  label: copy('routes.endpoint.protocol'),
                  child: FormField<Set<String>>(
                    initialValue: const {},
                    validator: (value) => value == null || value.isEmpty
                        ? copy('routes.validation.protocol')
                        : null,
                    builder: (field) => Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        DecoratedBox(
                          key: const Key('endpoint-editor-protocols'),
                          decoration: BoxDecoration(
                            color: context.viberColors.input,
                            border: Border.all(
                              color: field.hasError
                                  ? context.viberColors.danger
                                  : context.viberColors.divider,
                            ),
                            borderRadius: ViberMetrics.controlRadius,
                          ),
                          child: Column(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              for (final (index, protocol)
                                  in upstreamBackendProtocols.indexed) ...[
                                if (index > 0)
                                  Divider(
                                    height: 1,
                                    color: context.viberColors.dividerSoft,
                                  ),
                                CompactCheckboxOption(
                                  key: Key(
                                    'endpoint-editor-protocol-$protocol',
                                  ),
                                  value: _backendProtocols.contains(protocol),
                                  label: copy('routes.protocol.$protocol'),
                                  detail: copy(
                                    'routes.endpoint.protocol.detail.$protocol',
                                  ),
                                  onChanged: widget.controller.inventoryMutating
                                      ? null
                                      : (selected) {
                                          setState(() {
                                            if (selected) {
                                              _backendProtocols.add(protocol);
                                            } else {
                                              _backendProtocols.remove(
                                                protocol,
                                              );
                                            }
                                          });
                                          field.didChange(
                                            Set.unmodifiable(_backendProtocols),
                                          );
                                        },
                                ),
                              ],
                            ],
                          ),
                        ),
                        if (field.errorText case final error?) ...[
                          const SizedBox(height: 5),
                          Text(
                            error,
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(color: context.viberColors.danger),
                          ),
                        ],
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 8),
                Text(
                  copy('routes.endpoint.boundary'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (_submitted && widget.controller.inventoryError != null) ...[
                  const SizedBox(height: 9),
                  ControlFailureNotice(
                    message: widget.controller.inventoryError!,
                    copy: copy,
                    diagnostic: widget.controller.inventoryErrorDiagnostic,
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
      backendProtocols: upstreamBackendProtocols
          .where(_backendProtocols.contains)
          .toList(growable: false),
    );
    if (!mounted) return;
    if (created != null) {
      Navigator.pop(context);
    } else {
      setState(() {});
    }
  }
}

String _localizedCopy(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  return copy.maybe(key) ?? value;
}

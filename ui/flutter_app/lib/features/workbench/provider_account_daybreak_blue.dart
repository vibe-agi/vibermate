import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

/// Capability evidence belongs to this account and its selected linked service.
/// Opening details or changing that scope queries the server's bounded cache;
/// ordinary rebuilds never trigger discovery.
final class ProviderAccountDaybreakBlue extends StatefulWidget {
  const ProviderAccountDaybreakBlue({
    required this.account,
    required this.linkedEndpoints,
    required this.controller,
    required this.copy,
    super.key,
  });

  final ProviderAccount account;
  final List<UpstreamEndpoint> linkedEndpoints;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<ProviderAccountDaybreakBlue> createState() =>
      _ProviderAccountDaybreakBlueState();
}

final class _ProviderAccountDaybreakBlueState
    extends State<ProviderAccountDaybreakBlue> {
  String? _endpointId;
  String? _scope;
  int _generation = 0;
  bool _loading = false;
  bool _failed = false;
  UpstreamModelCatalog? _catalog;

  List<UpstreamEndpoint> get _endpoints => widget.linkedEndpoints
      .where(
        (endpoint) =>
            isChatGPTCodexOrigin(endpoint.origin) &&
            endpoint.backendProtocols.contains('openai_responses') &&
            widget.account.isLinkedTo(endpoint.id) &&
            widget.account.canLinkTo(endpoint),
      )
      .toList(growable: false);

  UpstreamEndpoint? get _endpoint =>
      _endpoints.where((endpoint) => endpoint.id == _endpointId).firstOrNull;

  @override
  void initState() {
    super.initState();
    _synchronize();
  }

  @override
  void didUpdateWidget(covariant ProviderAccountDaybreakBlue oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (!identical(oldWidget.controller, widget.controller)) _scope = null;
    _synchronize();
  }

  void _synchronize() {
    final endpoints = _endpoints;
    if (!endpoints.any((endpoint) => endpoint.id == _endpointId)) {
      _endpointId =
          endpoints
              .where(
                (endpoint) =>
                    endpoint.id == widget.controller.selectedEndpointId,
              )
              .firstOrNull
              ?.id ??
          endpoints.firstOrNull?.id;
    }
    final account = widget.account;
    final endpoint = _endpoint;
    final scope =
        '${account.id}:${account.revision}:${account.credentialEpoch}:'
        '${account.settingsRevision}:${account.associationRevision}:'
        '${account.credentialOrigin}:${account.usable}:'
        '${endpoint?.id}:${endpoint?.revision}';
    if (scope == _scope) return;
    _scope = scope;
    _generation++;
    _catalog = null;
    _failed = false;
    _loading = false;
    if (endpoint != null && account.usable) unawaited(_load());
  }

  Future<void> _load({bool refresh = false}) async {
    final endpoint = _endpoint;
    final account = widget.account;
    if (_loading || endpoint == null || !account.usable) return;
    final generation = ++_generation;
    setState(() {
      _loading = true;
      _failed = false;
      _catalog = null;
    });
    try {
      final catalog = await widget.controller.upstreamModels(
        endpoint.id,
        accountId: account.id,
        refresh: refresh,
      );
      if (!mounted || generation != _generation) return;
      if (!catalog.verifiedFromEndpoint ||
          catalog.endpointId != endpoint.id ||
          catalog.endpointRevision != endpoint.revision ||
          catalog.accountId != account.id ||
          catalog.accountRevision != account.revision ||
          catalog.credentialEpoch != account.credentialEpoch) {
        throw const ControlContractException('Model capability scope changed');
      }
      setState(() => _catalog = catalog);
    } catch (_) {
      if (mounted && generation == _generation) {
        setState(() => _failed = true);
      }
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _loading = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final account = widget.account;
    final copy = widget.copy;
    final endpoints = _endpoints;
    final models = _catalog?.models ?? const <UpstreamModel>[];
    final supported = models
        .where(
          (model) =>
              model.verifiedAvailable &&
              (model.availableCyberPrograms?.contains('daybreak_blue') ??
                  false),
        )
        .map((model) => model.id)
        .toList(growable: false);
    final status = _loading
        ? copy('provider_accounts.daybreak_blue.loading')
        : _endpoint == null
        ? copy('provider_accounts.daybreak_blue.unlinked')
        : !account.usable
        ? copy('provider_accounts.daybreak_blue.credential_unavailable')
        : _failed
        ? copy('provider_accounts.daybreak_blue.unavailable')
        : supported.isNotEmpty
        ? copy.format(
            supported.length == 1
                ? 'provider_accounts.daybreak_blue.supported_one'
                : 'provider_accounts.daybreak_blue.supported',
            {'count': supported.length},
          )
        : _catalog == null ||
              models.any((model) => model.availableCyberPrograms == null)
        ? copy('provider_accounts.daybreak_blue.unknown')
        : copy('provider_accounts.daybreak_blue.unsupported');
    final label = copy('provider_accounts.daybreak_blue.toggle');
    final canToggle =
        !widget.controller.inventoryMutating &&
        (account.daybreakBlueEnabled || supported.isNotEmpty);
    final style = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted);
    return Column(
      key: Key('provider-account-daybreak-blue-${account.id}'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const Divider(height: 24),
        Row(
          children: [
            Expanded(
              child: Row(
                children: [
                  Flexible(child: Text(label)),
                  ContextHelpButton(
                    title: 'Daybreak Blue',
                    message: copy('provider_accounts.daybreak_blue.detail'),
                    dismissLabel: copy('common.dismiss'),
                  ),
                ],
              ),
            ),
            Semantics(
              label: '$label: ${account.displayName}',
              child: Switch(
                key: Key('account-daybreak-blue-${account.id}'),
                value: account.daybreakBlueEnabled,
                onChanged: canToggle
                    ? (enabled) => unawaited(
                        widget.controller.setProviderAccountSettings(
                          account,
                          egressProfile: account.egressProfile,
                          automaticRefresh: account.automaticRefresh,
                          daybreakBlueEnabled: enabled,
                        ),
                      )
                    : null,
              ),
            ),
          ],
        ),
        if (endpoints.length > 1)
          DropdownButton<String>(
            key: Key('account-daybreak-endpoint-${account.id}'),
            isExpanded: true,
            value: _endpointId,
            items: [
              for (final endpoint in endpoints)
                DropdownMenuItem(
                  value: endpoint.id,
                  child: Text(
                    endpoint.displayName,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
            ],
            onChanged: (id) => setState(() {
              _endpointId = id;
              _synchronize();
            }),
          ),
        Text(status, style: style),
        if (supported.isNotEmpty)
          Padding(
            padding: const EdgeInsets.only(top: 4),
            child: SelectableText(supported.join(' · '), style: style),
          ),
        TextButton.icon(
          key: Key('account-daybreak-refresh-${account.id}'),
          onPressed: !_loading && _endpoint != null && account.usable
              ? () => unawaited(_load(refresh: true))
              : null,
          icon: _loading
              ? const SizedBox.square(
                  dimension: 14,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : const Icon(Icons.refresh, size: 16),
          label: Text(
            copy(
              _failed
                  ? 'provider_accounts.daybreak_blue.retry'
                  : 'provider_accounts.daybreak_blue.refresh',
            ),
          ),
        ),
      ],
    );
  }
}

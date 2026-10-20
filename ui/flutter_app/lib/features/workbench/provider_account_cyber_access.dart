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
final class ProviderAccountCyberAccess extends StatefulWidget {
  const ProviderAccountCyberAccess({
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
  State<ProviderAccountCyberAccess> createState() =>
      _ProviderAccountCyberAccessState();
}

final class _ProviderAccountCyberAccessState
    extends State<ProviderAccountCyberAccess> {
  String? _endpointId;
  String? _scope;
  int _generation = 0;
  int _selectionGeneration = 0;
  bool _loading = false;
  bool _manuallyRefreshing = false;
  Future<void>? _pendingLoad;
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
  void didUpdateWidget(covariant ProviderAccountCyberAccess oldWidget) {
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
    _manuallyRefreshing = false;
    _pendingLoad = null;
    if (endpoint != null && account.usable) unawaited(_load());
  }

  Future<void> _load({bool refresh = false}) {
    final pending = _pendingLoad;
    if (pending != null) return pending;
    final endpoint = _endpoint;
    final account = widget.account;
    if (endpoint == null || !account.usable) return Future.value();
    final generation = ++_generation;
    setState(() {
      _loading = true;
      _failed = false;
      _catalog = null;
    });
    return _pendingLoad = _readCatalog(endpoint, account, generation, refresh);
  }

  Future<void> _readCatalog(
    UpstreamEndpoint endpoint,
    ProviderAccount account,
    int generation,
    bool refresh,
  ) async {
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
        setState(() {
          _loading = false;
          _pendingLoad = null;
        });
      }
    }
  }

  Future<void> _refreshSupport() async {
    if (_manuallyRefreshing || _endpoint == null || !widget.account.usable) {
      return;
    }
    final request = _load(refresh: true);
    final generation = _generation;
    setState(() => _manuallyRefreshing = true);
    try {
      await request;
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _manuallyRefreshing = false);
      }
    }
  }

  Future<void> _selectProgram(String? program) async {
    if (program == null || program == widget.account.cyberAccessProgram) return;
    final account = widget.account;
    await widget.controller.setProviderAccountSettings(
      account,
      egressProfile: account.egressProfile,
      automaticRefresh: account.automaticRefresh,
      cyberAccessProgram: program,
    );
    if (mounted) setState(() => _selectionGeneration++);
  }

  @override
  Widget build(BuildContext context) {
    final account = widget.account;
    final copy = widget.copy;
    final endpoints = _endpoints;
    final models = _catalog?.models ?? const <UpstreamModel>[];
    final support = {
      for (final program in providerAccountCyberAccessPrograms)
        program: models
            .where(
              (model) =>
                  model.verifiedAvailable &&
                  (model.availableCyberPrograms?.contains(program) ?? false),
            )
            .map((model) => model.id)
            .toList(growable: false),
    };
    String option(String program) =>
        copy('provider_accounts.cyber_access.option.$program');
    final selected = account.cyberAccessProgram;
    final supported = support[selected] ?? const <String>[];
    final standardFallback = selected == 'standard'
        ? const <String>[]
        : models
              .where(
                (model) =>
                    model.verifiedAvailable &&
                    (model.availableCyberPrograms?.contains('standard') ??
                        false) &&
                    !(model.availableCyberPrograms?.contains(selected) ??
                        false),
              )
              .map((model) => model.id)
              .toList(growable: false);
    final unknown =
        _catalog == null ||
        models.any((model) => model.availableCyberPrograms == null);
    final status = _loading
        ? copy('provider_accounts.cyber_access.loading')
        : _endpoint == null
        ? copy('provider_accounts.cyber_access.unlinked')
        : !account.usable
        ? copy('provider_accounts.cyber_access.credential_unavailable')
        : _failed
        ? copy('provider_accounts.cyber_access.unavailable')
        : supported.isNotEmpty
        ? copy.format(
            supported.length == 1
                ? 'provider_accounts.cyber_access.supported_one'
                : 'provider_accounts.cyber_access.supported',
            {'mode': option(selected), 'count': supported.length},
          )
        : unknown
        ? copy('provider_accounts.cyber_access.unknown')
        : selected == 'standard'
        ? copy('provider_accounts.cyber_access.standard_available')
        : copy.format('provider_accounts.cyber_access.unsupported', {
            'mode': option(selected),
          });
    final label = copy('provider_accounts.cyber_access.label');
    final style = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted);
    final heading = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Flexible(
          child: Text(label, style: Theme.of(context).textTheme.labelMedium),
        ),
        ContextHelpButton(
          title: label,
          message: copy('provider_accounts.cyber_access.detail'),
          dismissLabel: copy('common.dismiss'),
        ),
      ],
    );
    final refresh = IconButton.outlined(
      key: Key('account-cyber-refresh-${account.id}'),
      onPressed: !_manuallyRefreshing && _endpoint != null && account.usable
          ? () => unawaited(_refreshSupport())
          : null,
      constraints: const BoxConstraints.tightFor(width: 32, height: 32),
      padding: const EdgeInsets.all(6),
      icon: _manuallyRefreshing
          ? const SizedBox.square(
              dimension: 14,
              child: CircularProgressIndicator(strokeWidth: 1.5),
            )
          : const Icon(Icons.refresh, size: 16),
      tooltip: copy(
        _failed
            ? 'provider_accounts.cyber_access.retry'
            : 'provider_accounts.cyber_access.refresh',
      ),
    );
    return Column(
      key: Key('provider-account-cyber-access-${account.id}'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final select = SizedBox(
              key: Key('account-cyber-access-${account.id}'),
              width: constraints.maxWidth < 260 ? constraints.maxWidth : 260,
              child: Semantics(
                label: '$label: ${account.displayName}',
                child: CompactSelectField<String>(
                  key: ValueKey((
                    account.settingsRevision,
                    selected,
                    _selectionGeneration,
                  )),
                  initialValue: selected,
                  isExpanded: true,
                  items: [
                    for (final program in providerAccountCyberAccessPrograms)
                      DropdownMenuItem(
                        key: Key('account-cyber-option-${account.id}-$program'),
                        value: program,
                        enabled:
                            program == 'standard' ||
                            program == selected ||
                            support[program]!.isNotEmpty,
                        child: Text(
                          option(program),
                          style:
                              program == 'standard' ||
                                  program == selected ||
                                  support[program]!.isNotEmpty
                              ? null
                              : TextStyle(color: context.viberColors.textFaint),
                        ),
                      ),
                  ],
                  onChanged: widget.controller.inventoryMutating
                      ? null
                      : (program) => unawaited(_selectProgram(program)),
                ),
              ),
            );
            if (constraints.maxWidth >= 560) {
              return ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 680),
                child: Row(
                  children: [
                    Expanded(child: heading),
                    const SizedBox(width: 12),
                    select,
                    const SizedBox(width: 8),
                    refresh,
                  ],
                ),
              );
            }
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                heading,
                const SizedBox(height: 6),
                Row(
                  children: [
                    Expanded(child: select),
                    const SizedBox(width: 8),
                    refresh,
                  ],
                ),
              ],
            );
          },
        ),
        if (endpoints.length > 1)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: SizedBox(
              width: 260,
              child: CompactLabeledControl(
                label: copy('provider_accounts.table.service'),
                child: CompactSelectField<String>(
                  key: Key('account-cyber-endpoint-${account.id}'),
                  initialValue: _endpointId,
                  isExpanded: true,
                  items: [
                    for (final endpoint in endpoints)
                      DropdownMenuItem(
                        value: endpoint.id,
                        child: Text(endpoint.displayName),
                      ),
                  ],
                  onChanged: (id) => setState(() {
                    _endpointId = id;
                    _synchronize();
                  }),
                ),
              ),
            ),
          ),
        if (supported.isEmpty)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text(status, style: style),
          )
        else
          _CyberModelGroup(
            key: Key('account-cyber-supported-${account.id}'),
            title: selected == 'standard'
                ? copy('provider_accounts.cyber_access.uses_standard')
                : copy.format('provider_accounts.cyber_access.supports_mode', {
                    'mode': option(selected),
                  }),
            countHint: status,
            models: supported,
            preferred: selected != 'standard',
          ),
        if (standardFallback.isNotEmpty)
          _CyberModelGroup(
            key: Key('account-cyber-standard-fallback-${account.id}'),
            title: copy('provider_accounts.cyber_access.uses_standard'),
            countHint: copy.format(
              standardFallback.length == 1
                  ? 'provider_accounts.cyber_access.supported_one'
                  : 'provider_accounts.cyber_access.supported',
              {'mode': option('standard'), 'count': standardFallback.length},
            ),
            models: standardFallback,
          ),
      ],
    );
  }
}

final class _CyberModelGroup extends StatelessWidget {
  const _CyberModelGroup({
    required this.title,
    required this.countHint,
    required this.models,
    this.preferred = false,
    super.key,
  });

  final String title;
  final String countHint;
  final List<String> models;
  final bool preferred;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    return Semantics(
      container: true,
      explicitChildNodes: true,
      label: title,
      child: Container(
        margin: const EdgeInsets.only(top: 12),
        padding: const EdgeInsets.fromLTRB(10, 9, 10, 10),
        decoration: BoxDecoration(
          color: colors.canvas.withValues(alpha: .45),
          border: Border.all(color: colors.dividerSoft),
          borderRadius: ViberMetrics.controlRadius,
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Icon(
                  preferred ? Icons.shield_outlined : Icons.tune_rounded,
                  size: 14,
                  color: preferred ? colors.route : colors.textFaint,
                ),
                const SizedBox(width: 6),
                Expanded(
                  child: ExcludeSemantics(
                    child: Text(
                      title,
                      style: Theme.of(context).textTheme.labelMedium?.copyWith(
                        color: preferred ? colors.route : colors.textMuted,
                      ),
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Tooltip(
                  message: countHint,
                  child: Text(
                    '${models.length}',
                    style: Theme.of(
                      context,
                    ).textTheme.bodySmall?.copyWith(color: colors.textFaint),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 8),
            LayoutBuilder(
              builder: (context, constraints) => Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [
                  for (final model in models)
                    ConstrainedBox(
                      constraints: BoxConstraints(
                        maxWidth: constraints.maxWidth,
                      ),
                      child: DecoratedBox(
                        decoration: BoxDecoration(
                          color: colors.panel,
                          borderRadius: ViberMetrics.controlRadius,
                          border: Border.all(color: colors.dividerSoft),
                        ),
                        child: Padding(
                          padding: const EdgeInsets.symmetric(
                            horizontal: 7,
                            vertical: 4,
                          ),
                          child: SelectableText(
                            model,
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(color: colors.textMuted),
                          ),
                        ),
                      ),
                    ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

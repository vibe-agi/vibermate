import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'environment_editing.dart';
import 'workbench_controller.dart';

final class EnvironmentsView extends StatelessWidget {
  const EnvironmentsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final environments =
        controller.data?.environments ?? const <EnvironmentRecord>[];
    return Column(
      children: [
        PageHeading(
          title: copy('environment.title'),
          subtitle: copy('environment.subtitle'),
          trailing: FilledButton.icon(
            key: const Key('environment-create'),
            onPressed: controller.environmentMutating
                ? null
                : () => _openNewEditor(context),
            icon: const Icon(Icons.add, size: 14),
            label: Text(copy('environment.create')),
          ),
        ),
        const Divider(height: 1),
        if (controller.environmentError case final error?)
          InlineNotice(message: error, error: true),
        if (controller.environmentNotice case final notice?)
          InlineNotice(
            message: copy('notice.$notice'),
            onDismiss: controller.clearEnvironmentNotice,
          ),
        Expanded(
          child: LayoutBuilder(
            builder: (context, constraints) {
              final compact = constraints.maxWidth < 680;
              final directory = _EnvironmentDirectory(
                environments: environments,
                selectedId: controller.selectedEnvironmentId,
                onSelected: controller.selectEnvironment,
                horizontal: compact,
              );
              final detail = _EnvironmentDetail(
                controller: controller,
                environment: controller.displayedEnvironment,
                historicalRevision: controller.selectedEnvironmentRevision,
                loadingHistorical: controller.environmentRevisionLoading,
                endpoints: controller.data?.endpoints ?? const [],
                accounts: controller.data?.accounts ?? const [],
                copy: copy,
                onShowCurrent: controller.showCurrentEnvironment,
                onEdit: (environment) => _openEditor(context, environment),
              );
              if (compact) {
                return Column(
                  children: [
                    SizedBox(height: 116, child: directory),
                    const Divider(height: 1),
                    Expanded(child: detail),
                  ],
                );
              }
              return Row(
                children: [
                  SizedBox(width: 246, child: directory),
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

  void _openEditor(BuildContext context, EnvironmentRecord environment) {
    unawaited(
      showDialog<void>(
        context: context,
        barrierDismissible: false,
        builder: (context) => _EnvironmentEditorDialog(
          controller: controller,
          environment: environment,
          copy: copy,
        ),
      ),
    );
  }

  void _openNewEditor(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        barrierDismissible: false,
        builder: (context) =>
            _NewEnvironmentDialog(controller: controller, copy: copy),
      ),
    );
  }
}

final class _EnvironmentDirectory extends StatelessWidget {
  const _EnvironmentDirectory({
    required this.environments,
    required this.selectedId,
    required this.onSelected,
    required this.horizontal,
  });

  final List<EnvironmentRecord> environments;
  final String? selectedId;
  final ValueChanged<String> onSelected;
  final bool horizontal;

  @override
  Widget build(BuildContext context) {
    return ColoredBox(
      color: ViberColors.panel,
      child: ListView.builder(
        scrollDirection: horizontal ? Axis.horizontal : Axis.vertical,
        padding: EdgeInsets.symmetric(
          vertical: horizontal ? 9 : 6,
          horizontal: horizontal ? 8 : 0,
        ),
        itemCount: environments.length,
        itemBuilder: (context, index) {
          final environment = environments[index];
          final selected = environment.id == selectedId;
          final routes = environment.routes.length;
          return Semantics(
            selected: selected,
            button: true,
            label: '${environment.name}, ${environment.state}, $routes routes',
            child: Material(
              color: selected ? ViberColors.selection : Colors.transparent,
              child: InkWell(
                key: Key('environment-row-${environment.id}'),
                onTap: () => onSelected(environment.id),
                child: Container(
                  width: horizontal ? 188 : null,
                  height: horizontal ? null : 55,
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
                              environment.name,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          if (environment.systemOwned)
                            const Icon(
                              Icons.shield_outlined,
                              size: 13,
                              color: ViberColors.textFaint,
                            ),
                        ],
                      ),
                      const SizedBox(height: 3),
                      Text(
                        'r${environment.revision}  ·  $routes routes',
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

final class _EnvironmentDetail extends StatelessWidget {
  const _EnvironmentDetail({
    required this.controller,
    required this.environment,
    required this.historicalRevision,
    required this.loadingHistorical,
    required this.endpoints,
    required this.accounts,
    required this.copy,
    required this.onShowCurrent,
    required this.onEdit,
  });

  final WorkbenchController controller;
  final EnvironmentRecord? environment;
  final int? historicalRevision;
  final bool loadingHistorical;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final VoidCallback onShowCurrent;
  final ValueChanged<EnvironmentRecord> onEdit;

  @override
  Widget build(BuildContext context) {
    final value = environment;
    if (value == null) {
      return CenteredMessage(
        icon: loadingHistorical ? Icons.history : Icons.tune,
        title: copy(
          loadingHistorical
              ? 'environment.history.loading'
              : 'environment.select',
        ),
      );
    }
    final historical = historicalRevision != null;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: double.infinity,
          padding: const EdgeInsets.fromLTRB(16, 11, 16, 10),
          color: ViberColors.panel,
          child: LayoutBuilder(
            builder: (context, constraints) {
              final identity = Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Wrap(
                    spacing: 6,
                    runSpacing: 5,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      Text(
                        value.name,
                        style: Theme.of(context).textTheme.headlineSmall,
                      ),
                      if (historical)
                        StatusPill(
                          label: copy('environment.history.frozen'),
                          color: ViberColors.route,
                        ),
                      if (value.systemOwned)
                        StatusPill(
                          label: copy('common.system'),
                          color: ViberColors.textMuted,
                        ),
                      StatusPill(
                        label: copy('environment.state.${value.state}'),
                        color: value.state == 'active'
                            ? ViberColors.verified
                            : ViberColors.textFaint,
                      ),
                    ],
                  ),
                  const SizedBox(height: 3),
                  Text(
                    '${value.id}  ·  revision ${value.revision}  ·  ${value.digest.substring(0, 12)}',
                    style: monoStyle,
                  ),
                ],
              );
              final actions = Wrap(
                spacing: 8,
                runSpacing: 6,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    copy.format('environment.routes', {
                      'count': value.routes.length,
                    }),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  if (historical)
                    OutlinedButton.icon(
                      key: const Key('environment-history-current'),
                      onPressed: onShowCurrent,
                      icon: const Icon(Icons.update, size: 13),
                      label: Text(copy('environment.history.current')),
                    )
                  else if (!value.systemOwned)
                    OutlinedButton.icon(
                      key: const Key('environment-edit'),
                      onPressed: controller.environmentMutating
                          ? null
                          : () => onEdit(value),
                      icon: const Icon(Icons.edit_outlined, size: 13),
                      label: Text(copy('common.edit')),
                    ),
                ],
              );
              if (constraints.maxWidth < 580) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [identity, const SizedBox(height: 8), actions],
                );
              }
              return Row(
                children: [
                  Expanded(child: identity),
                  const SizedBox(width: 12),
                  actions,
                ],
              );
            },
          ),
        ),
        if (historical)
          Semantics(
            container: true,
            label: copy.format('environment.history.semantics', {
              'revision': value.revision,
            }),
            child: Container(
              key: const Key('environment-history-banner'),
              width: double.infinity,
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 7),
              color: ViberColors.route.withValues(alpha: 0.07),
              child: Row(
                children: [
                  const Icon(Icons.history, size: 14, color: ViberColors.route),
                  const SizedBox(width: 7),
                  Expanded(
                    child: Text(
                      copy.format('environment.history.detail', {
                        'revision': value.revision,
                      }),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ),
                ],
              ),
            ),
          ),
        const Divider(height: 1),
        Expanded(
          child: value.clientEndpoints.isEmpty
              ? CenteredMessage(
                  icon: Icons.compare_arrows,
                  title: value.name,
                  detail: copy(
                    value.systemOwned
                        ? 'environment.transparent'
                        : 'environment.observe',
                  ),
                )
              : ListView.builder(
                  padding: const EdgeInsets.fromLTRB(14, 12, 14, 20),
                  itemCount: value.clientEndpoints.length,
                  itemBuilder: (context, index) => _ClientEndpointPlan(
                    clientEndpoint: value.clientEndpoints[index],
                    endpoints: endpoints,
                    accounts: accounts,
                    copy: copy,
                  ),
                ),
        ),
      ],
    );
  }
}

final class _ClientEndpointPlan extends StatelessWidget {
  const _ClientEndpointPlan({
    required this.clientEndpoint,
    required this.endpoints,
    required this.accounts,
    required this.copy,
  });

  final EnvironmentClientEndpoint clientEndpoint;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      decoration: BoxDecoration(
        color: ViberColors.panel,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
            child: Row(
              children: [
                const Icon(Icons.input, size: 15, color: ViberColors.route),
                const SizedBox(width: 7),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        clientEndpoint.id,
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                      Text(
                        clientEndpoint.clientOrigin.toString(),
                        style: monoStyle,
                      ),
                    ],
                  ),
                ),
                Text(
                  copy('environment.clients'),
                  style: Theme.of(context).textTheme.labelMedium,
                ),
              ],
            ),
          ),
          const Divider(height: 1),
          for (final plan in clientEndpoint.protocolPlans)
            _ProtocolPlanRows(
              plan: plan,
              endpoints: endpoints,
              accounts: accounts,
              copy: copy,
            ),
        ],
      ),
    );
  }
}

final class _ProtocolPlanRows extends StatelessWidget {
  const _ProtocolPlanRows({
    required this.plan,
    required this.endpoints,
    required this.accounts,
    required this.copy,
  });

  final EnvironmentProtocolPlan plan;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: double.infinity,
          color: ViberColors.panelRaised.withValues(alpha: 0.45),
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
          child: Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(
                plan.clientProtocol,
                style: monoStyle.copyWith(color: ViberColors.text),
              ),
              StatusPill(
                label: plan.mode,
                color: plan.mode == 'managed'
                    ? ViberColors.route
                    : ViberColors.textMuted,
              ),
            ],
          ),
        ),
        for (final route in plan.routes)
          _RouteAuthorityRow(
            route: route,
            isDefault: route.id == plan.defaultRouteId,
            endpoint: endpoints
                .where((endpoint) => endpoint.id == route.endpointId)
                .firstOrNull,
            accounts: accounts,
            copy: copy,
          ),
      ],
    );
  }
}

final class _RouteAuthorityRow extends StatelessWidget {
  const _RouteAuthorityRow({
    required this.route,
    required this.isDefault,
    required this.endpoint,
    required this.accounts,
    required this.copy,
  });

  final EnvironmentRoute route;
  final bool isDefault;
  final UpstreamEndpoint? endpoint;
  final List<ProviderAccount> accounts;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final candidates = route.accountPolicy.candidateAccountIds
        .map((id) => accounts.where((account) => account.id == id).firstOrNull)
        .whereType<ProviderAccount>()
        .toList(growable: false);
    final invalid = candidates
        .where((account) => account.upstreamEndpointId != route.endpointId)
        .toList();
    return LayoutBuilder(
      builder: (context, constraints) {
        final compact = constraints.maxWidth < 500;
        return Container(
          padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
          decoration: const BoxDecoration(
            border: Border(bottom: BorderSide(color: ViberColors.dividerSoft)),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  SizedBox(
                    width: 130,
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          route.id,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                        Text(
                          route.backendProtocol,
                          overflow: TextOverflow.ellipsis,
                          style: monoStyle,
                        ),
                      ],
                    ),
                  ),
                  const Icon(
                    Icons.arrow_forward,
                    size: 14,
                    color: ViberColors.route,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Flexible(
                              child: Text(
                                endpoint?.displayName ?? route.endpointId,
                                overflow: TextOverflow.ellipsis,
                                style: Theme.of(context).textTheme.titleSmall,
                              ),
                            ),
                            const SizedBox(width: 6),
                            StatusPill(
                              label: isDefault
                                  ? copy('environment.route.default')
                                  : copy('environment.route.fallback'),
                              color: isDefault
                                  ? ViberColors.route
                                  : ViberColors.textMuted,
                            ),
                          ],
                        ),
                        Text(
                          route.endpointOrigin.toString(),
                          overflow: TextOverflow.ellipsis,
                          style: monoStyle,
                        ),
                      ],
                    ),
                  ),
                ],
              ),
              if (route.accountPolicy.mode == 'managed') ...[
                const SizedBox(height: 7),
                Padding(
                  padding: EdgeInsets.only(left: compact ? 0 : 152),
                  child: Wrap(
                    spacing: 6,
                    runSpacing: 5,
                    children: [
                      for (final account in candidates)
                        StatusPill(
                          label: account.displayName,
                          color: account.upstreamEndpointId == route.endpointId
                              ? ViberColors.verified
                              : ViberColors.danger,
                          icon: Icons.key,
                        ),
                      if (candidates.isEmpty)
                        Text(
                          'No candidate account',
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                    ],
                  ),
                ),
              ],
              if (invalid.isNotEmpty)
                Padding(
                  padding: EdgeInsets.only(left: compact ? 0 : 152, top: 5),
                  child: Text(
                    copy('environment.account.invalid'),
                    style: Theme.of(
                      context,
                    ).textTheme.bodySmall?.copyWith(color: ViberColors.danger),
                  ),
                ),
            ],
          ),
        );
      },
    );
  }
}

final class _NewEnvironmentDialog extends StatefulWidget {
  const _NewEnvironmentDialog({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_NewEnvironmentDialog> createState() => _NewEnvironmentDialogState();
}

final class _NewEnvironmentDialogState extends State<_NewEnvironmentDialog> {
  static const _clientCredential = '__client_credential__';

  final _formKey = GlobalKey<FormState>();
  final _name = TextEditingController();
  final _id = TextEditingController();
  late final TextEditingController _retention;
  String _endpointId = '';
  String _accountId = '';
  String _recordingMode = 'full';
  String _toolMode = 'observe';
  String? _draftEnvironmentId;
  bool _idEdited = false;
  bool _submitted = false;

  @override
  void initState() {
    super.initState();
    _retention = TextEditingController(text: '30');
  }

  @override
  void dispose() {
    _name.dispose();
    _id.dispose();
    _retention.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final media = MediaQuery.sizeOf(context);
    return AnimatedBuilder(
      animation: widget.controller,
      builder: (context, _) {
        final impact = widget.controller.reviewedEnvironmentImpact;
        final reviewed =
            impact != null &&
            impact.environmentId == (_draftEnvironmentId ?? _id.text);
        final endpoints =
            widget.controller.data?.endpoints
                .where((endpoint) => endpoint.state == 'active')
                .toList(growable: false) ??
            const <UpstreamEndpoint>[];
        final selectedEndpoint = endpoints
            .where((endpoint) => endpoint.id == _endpointId)
            .firstOrNull;
        final accounts = selectedEndpoint == null
            ? const <ProviderAccount>[]
            : (widget.controller.data?.accounts ?? const <ProviderAccount>[])
                  .where(
                    (account) =>
                        account.upstreamEndpointId == selectedEndpoint.id &&
                        account.usable,
                  )
                  .toList(growable: false);
        final allowClient =
            selectedEndpoint != null &&
            upstreamEndpointCanUseClientCredential(selectedEndpoint);
        return AlertDialog(
          insetPadding: const EdgeInsets.symmetric(
            horizontal: 16,
            vertical: 20,
          ),
          title: Text(copy('environment.create.title')),
          content: SizedBox(
            width: 560,
            child: ConstrainedBox(
              constraints: BoxConstraints(maxHeight: media.height * 0.68),
              child: reviewed
                  ? SingleChildScrollView(
                      key: const Key('environment-create-impact'),
                      child: _EnvironmentImpactReview(
                        impact: impact,
                        copy: copy,
                      ),
                    )
                  : Form(
                      key: _formKey,
                      child: SingleChildScrollView(
                        key: const Key('environment-create-form'),
                        child: Column(
                          mainAxisSize: MainAxisSize.min,
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(
                              copy('environment.create.scope'),
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                            const SizedBox(height: 11),
                            TextFormField(
                              key: const Key('environment-create-name'),
                              controller: _name,
                              autofocus: true,
                              maxLength: 256,
                              decoration: InputDecoration(
                                labelText: copy('environment.field.name'),
                              ),
                              onChanged: (value) {
                                if (!_idEdited) {
                                  _id.text = _environmentSlug(value);
                                }
                              },
                              validator: (value) =>
                                  value == null ||
                                      value.isEmpty ||
                                      value.trim() != value
                                  ? copy('environment.validation.name')
                                  : null,
                            ),
                            const SizedBox(height: 7),
                            TextFormField(
                              key: const Key('environment-create-id'),
                              controller: _id,
                              enabled: _draftEnvironmentId == null,
                              maxLength: 128,
                              autocorrect: false,
                              enableSuggestions: false,
                              decoration: InputDecoration(
                                labelText: copy('environment.field.id'),
                              ),
                              onChanged: (_) => _idEdited = true,
                              validator: (value) =>
                                  value == null ||
                                      !RegExp(
                                        r'^[a-z0-9][a-z0-9._-]{0,127}$',
                                      ).hasMatch(value) ||
                                      value == 'system_transparent'
                                  ? copy('environment.validation.id')
                                  : null,
                            ),
                            const SizedBox(height: 12),
                            _EditorSectionLabel(
                              label: copy('environment.edit.routes'),
                              detail: copy(
                                'environment.create.authority.detail',
                              ),
                            ),
                            const SizedBox(height: 6),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-create-endpoint'),
                              initialValue: _endpointId,
                              isExpanded: true,
                              decoration: InputDecoration(
                                labelText: copy('environment.create.authority'),
                              ),
                              items: [
                                DropdownMenuItem(
                                  value: '',
                                  child: Text(
                                    copy('environment.create.observe_only'),
                                  ),
                                ),
                                for (final endpoint in endpoints)
                                  DropdownMenuItem(
                                    value: endpoint.id,
                                    child: Text(
                                      '${endpoint.displayName} · ${endpoint.origin}',
                                      overflow: TextOverflow.ellipsis,
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) {
                                      final endpoint = endpoints
                                          .where(
                                            (candidate) =>
                                                candidate.id == value,
                                          )
                                          .firstOrNull;
                                      final compatible = endpoint == null
                                          ? const <ProviderAccount>[]
                                          : (widget.controller.data?.accounts ??
                                                    const <ProviderAccount>[])
                                                .where(
                                                  (account) =>
                                                      account.upstreamEndpointId ==
                                                          endpoint.id &&
                                                      account.usable,
                                                );
                                      setState(() {
                                        _endpointId = value ?? '';
                                        _accountId = endpoint == null
                                            ? ''
                                            : upstreamEndpointCanUseClientCredential(
                                                endpoint,
                                              )
                                            ? _clientCredential
                                            : compatible.firstOrNull?.id ?? '';
                                      });
                                    },
                            ),
                            if (selectedEndpoint != null) ...[
                              const SizedBox(height: 7),
                              DropdownButtonFormField<String>(
                                key: Key(
                                  'environment-create-account-${selectedEndpoint.id}',
                                ),
                                initialValue: _accountId.isEmpty
                                    ? null
                                    : _accountId,
                                isExpanded: true,
                                decoration: InputDecoration(
                                  labelText: copy('environment.account'),
                                  helperText: !allowClient && accounts.isEmpty
                                      ? copy(
                                          'environment.endpoint.account_required',
                                        )
                                      : copy('environment.account.owner'),
                                ),
                                items: [
                                  if (allowClient)
                                    DropdownMenuItem(
                                      value: _clientCredential,
                                      child: Text(
                                        copy('environment.account.client'),
                                      ),
                                    ),
                                  for (final account in accounts)
                                    DropdownMenuItem(
                                      value: account.id,
                                      child: Text(account.displayName),
                                    ),
                                ],
                                validator: (value) =>
                                    !allowClient && value == null
                                    ? copy(
                                        'environment.endpoint.account_required',
                                      )
                                    : null,
                                onChanged: widget.controller.environmentMutating
                                    ? null
                                    : (value) => setState(
                                        () => _accountId = value ?? '',
                                      ),
                              ),
                            ],
                            const SizedBox(height: 12),
                            _EditorSectionLabel(
                              label: copy('environment.edit.policy'),
                            ),
                            const SizedBox(height: 6),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-create-tool-mode'),
                              initialValue: _toolMode,
                              decoration: InputDecoration(
                                labelText: copy('environment.policy.tool_mode'),
                              ),
                              items: [
                                for (final value in const [
                                  'observe',
                                  'review',
                                  'strict',
                                ])
                                  DropdownMenuItem(
                                    value: value,
                                    child: Text(
                                      copy('environment.policy.$value'),
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) =>
                                        setState(() => _toolMode = value!),
                            ),
                            const SizedBox(height: 7),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-create-recording'),
                              initialValue: _recordingMode,
                              decoration: InputDecoration(
                                labelText: copy('environment.recording.mode'),
                              ),
                              items: [
                                for (final value in const [
                                  'full',
                                  'metadata_only',
                                  'off',
                                ])
                                  DropdownMenuItem(
                                    value: value,
                                    child: Text(
                                      copy('environment.recording.$value'),
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) =>
                                        setState(() => _recordingMode = value!),
                            ),
                            if (_recordingMode != 'off') ...[
                              const SizedBox(height: 7),
                              TextFormField(
                                key: const Key('environment-create-retention'),
                                controller: _retention,
                                keyboardType: TextInputType.number,
                                decoration: InputDecoration(
                                  labelText: copy(
                                    'environment.recording.retention',
                                  ),
                                  suffixText: copy(
                                    'environment.recording.days',
                                  ),
                                ),
                                validator: (value) {
                                  final days = int.tryParse(value ?? '');
                                  return days == null || days < 1 || days > 3650
                                      ? copy('environment.validation.retention')
                                      : null;
                                },
                              ),
                            ],
                            if (_submitted &&
                                widget.controller.environmentError != null) ...[
                              const SizedBox(height: 9),
                              InlineNotice(
                                message: widget.controller.environmentError!,
                                error: true,
                              ),
                            ],
                          ],
                        ),
                      ),
                    ),
            ),
          ),
          actions: reviewed
              ? [
                  TextButton(
                    key: const Key('environment-create-impact-back'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : widget.controller.clearEnvironmentReview,
                    child: Text(copy('common.back')),
                  ),
                  FilledButton.icon(
                    key: const Key('environment-create-publish'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _publish,
                    icon: const Icon(Icons.publish_outlined, size: 14),
                    label: Text(copy('environment.publish')),
                  ),
                ]
              : [
                  TextButton(
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _close,
                    child: Text(copy('common.cancel')),
                  ),
                  FilledButton.icon(
                    key: const Key('environment-create-review'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _review,
                    icon: const Icon(Icons.rule_outlined, size: 14),
                    label: Text(copy('environment.review')),
                  ),
                ],
        );
      },
    );
  }

  Future<void> _review() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final environmentId = _draftEnvironmentId ?? _id.text;
    final dashboard = widget.controller.data!;
    final endpoint = dashboard.endpoints
        .where((candidate) => candidate.id == _endpointId)
        .firstOrNull;
    final account = _accountId == _clientCredential || _accountId.isEmpty
        ? null
        : dashboard.accounts.firstWhere(
            (candidate) => candidate.id == _accountId,
          );
    final clientEndpoints = endpoint == null
        ? const <EnvironmentClientEndpoint>[]
        : appendEnvironmentUpstreamEndpoint(
            endpoints: const [],
            upstreamEndpoint: endpoint,
            account: account,
          );
    final retentionDays = _recordingMode == 'off'
        ? 0
        : int.parse(_retention.text);
    final impact = await widget.controller.reviewNewEnvironment(
      environmentId,
      EnvironmentDraftInput(
        expectedDraftRevision: 0,
        name: _name.text,
        state: 'active',
        clientEndpoints: clientEndpoints,
        pluginBindings: const [],
        budgetPolicy: const EnvironmentBudgetPolicy(id: '', revision: 0),
        egressPolicy: const EnvironmentEgressPolicy(
          id: '',
          revision: 0,
          mode: '',
        ),
        contentRecording: EnvironmentContentRecordingPolicy(
          mode: _recordingMode,
          retentionDays: retentionDays,
        ),
        policySet: EnvironmentPolicySet(toolMode: _toolMode),
      ),
    );
    if (!mounted) return;
    if (impact != null) _draftEnvironmentId = environmentId;
    setState(() {});
  }

  Future<void> _publish() async {
    final result = await widget.controller.publishReviewedEnvironment();
    if (!mounted || result == null) return;
    Navigator.pop(context);
  }

  void _close() {
    widget.controller.clearEnvironmentReview();
    Navigator.pop(context);
  }
}

String _environmentSlug(String value) {
  var slug = value
      .toLowerCase()
      .replaceAll(RegExp('[^a-z0-9._-]+'), '-')
      .replaceAll(RegExp(r'^-+|-+$'), '');
  if (slug.length > 64) slug = slug.substring(0, 64);
  return slug;
}

final class _EnvironmentEditorDialog extends StatefulWidget {
  const _EnvironmentEditorDialog({
    required this.controller,
    required this.environment,
    required this.copy,
  });

  final WorkbenchController controller;
  final EnvironmentRecord environment;
  final AppCopy copy;

  @override
  State<_EnvironmentEditorDialog> createState() =>
      _EnvironmentEditorDialogState();
}

final class _EnvironmentEditorDialogState
    extends State<_EnvironmentEditorDialog> {
  static const _clientCredential = '__client_credential__';

  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _retention;
  late String _state;
  late String _recordingMode;
  late String _toolMode;
  late List<EnvironmentClientEndpoint> _clientEndpoints;
  bool _submitted = false;

  @override
  void initState() {
    super.initState();
    final environment = widget.environment;
    _name = TextEditingController(text: environment.name);
    _state = environment.state;
    _recordingMode = environment.contentRecording.mode;
    _retention = TextEditingController(
      text:
          '${environment.contentRecording.retentionDays == 0 ? 30 : environment.contentRecording.retentionDays}',
    );
    _toolMode = environment.policySet.toolMode;
    _clientEndpoints = environment.clientEndpoints;
  }

  @override
  void dispose() {
    _name.dispose();
    _retention.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final media = MediaQuery.sizeOf(context);
    return AnimatedBuilder(
      animation: widget.controller,
      builder: (context, _) {
        final impact = widget.controller.reviewedEnvironmentImpact;
        final reviewed =
            impact != null && impact.environmentId == widget.environment.id;
        return AlertDialog(
          insetPadding: const EdgeInsets.symmetric(
            horizontal: 16,
            vertical: 20,
          ),
          title: Wrap(
            spacing: 9,
            runSpacing: 5,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(
                copy.format('environment.edit.title', {
                  'name': widget.environment.name,
                }),
              ),
              StatusPill(
                label: 'r${widget.environment.revision}',
                color: ViberColors.textMuted,
              ),
            ],
          ),
          content: SizedBox(
            width: 620,
            child: ConstrainedBox(
              constraints: BoxConstraints(maxHeight: media.height * 0.68),
              child: reviewed
                  ? SingleChildScrollView(
                      key: const Key('environment-impact-review'),
                      child: _EnvironmentImpactReview(
                        impact: impact,
                        copy: copy,
                      ),
                    )
                  : Form(
                      key: _formKey,
                      child: SingleChildScrollView(
                        key: const Key('environment-editor-form'),
                        child: Column(
                          mainAxisSize: MainAxisSize.min,
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(
                              copy('environment.edit.scope'),
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                            const SizedBox(height: 12),
                            _EditorSectionLabel(
                              label: copy('environment.edit.identity'),
                            ),
                            const SizedBox(height: 6),
                            TextFormField(
                              key: const Key('environment-editor-name'),
                              controller: _name,
                              autofocus: true,
                              maxLength: 256,
                              decoration: InputDecoration(
                                labelText: copy('environment.field.name'),
                              ),
                              validator: (value) =>
                                  value == null ||
                                      value.isEmpty ||
                                      value.trim() != value
                                  ? copy('environment.validation.name')
                                  : null,
                            ),
                            const SizedBox(height: 7),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-editor-state'),
                              initialValue: _state,
                              decoration: InputDecoration(
                                labelText: copy('environment.field.state'),
                              ),
                              items: [
                                for (final value in const [
                                  'active',
                                  'disabled',
                                ])
                                  DropdownMenuItem(
                                    value: value,
                                    child: Text(
                                      copy('environment.state.$value'),
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) => setState(() => _state = value!),
                            ),
                            const SizedBox(height: 14),
                            _EditorSectionLabel(
                              label: copy('environment.edit.policy'),
                            ),
                            const SizedBox(height: 6),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-editor-tool-mode'),
                              initialValue: _toolMode,
                              decoration: InputDecoration(
                                labelText: copy('environment.policy.tool_mode'),
                              ),
                              items: [
                                for (final value in const [
                                  'observe',
                                  'review',
                                  'strict',
                                ])
                                  DropdownMenuItem(
                                    value: value,
                                    child: Text(
                                      copy('environment.policy.$value'),
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) =>
                                        setState(() => _toolMode = value!),
                            ),
                            const SizedBox(height: 7),
                            DropdownButtonFormField<String>(
                              key: const Key('environment-editor-recording'),
                              initialValue: _recordingMode,
                              decoration: InputDecoration(
                                labelText: copy('environment.recording.mode'),
                              ),
                              items: [
                                for (final value in const [
                                  'full',
                                  'metadata_only',
                                  'off',
                                ])
                                  DropdownMenuItem(
                                    value: value,
                                    child: Text(
                                      copy('environment.recording.$value'),
                                    ),
                                  ),
                              ],
                              onChanged: widget.controller.environmentMutating
                                  ? null
                                  : (value) =>
                                        setState(() => _recordingMode = value!),
                            ),
                            if (_recordingMode != 'off') ...[
                              const SizedBox(height: 7),
                              TextFormField(
                                key: const Key('environment-editor-retention'),
                                controller: _retention,
                                keyboardType: TextInputType.number,
                                decoration: InputDecoration(
                                  labelText: copy(
                                    'environment.recording.retention',
                                  ),
                                  suffixText: copy(
                                    'environment.recording.days',
                                  ),
                                ),
                                validator: (value) {
                                  final days = int.tryParse(value ?? '');
                                  return days == null || days < 1 || days > 3650
                                      ? copy('environment.validation.retention')
                                      : null;
                                },
                              ),
                            ],
                            const SizedBox(height: 14),
                            _EditorSectionLabel(
                              label: copy('environment.edit.routes'),
                              detail: copy('environment.edit.routes.detail'),
                            ),
                            const SizedBox(height: 6),
                            _EnvironmentEndpointAdder(
                              current: _clientEndpoints,
                              endpoints:
                                  widget.controller.data?.endpoints ?? const [],
                              accounts:
                                  widget.controller.data?.accounts ?? const [],
                              copy: copy,
                              clientCredentialValue: _clientCredential,
                              enabled: !widget.controller.environmentMutating,
                              onAdd: (endpoint, account) => setState(() {
                                _clientEndpoints =
                                    appendEnvironmentUpstreamEndpoint(
                                      endpoints: _clientEndpoints,
                                      upstreamEndpoint: endpoint,
                                      account: account,
                                    );
                              }),
                            ),
                            const SizedBox(height: 8),
                            if (_clientEndpoints.isEmpty)
                              _EditorEmptyState(
                                text: copy('environment.edit.routes.empty'),
                              )
                            else
                              for (final endpoint in _clientEndpoints)
                                _EnvironmentEndpointEditor(
                                  endpoint: endpoint,
                                  endpoints:
                                      widget.controller.data?.endpoints ??
                                      const [],
                                  accounts:
                                      widget.controller.data?.accounts ??
                                      const [],
                                  copy: copy,
                                  clientCredentialValue: _clientCredential,
                                  enabled:
                                      !widget.controller.environmentMutating,
                                  onAccountChanged: (plan, route, account) =>
                                      setState(() {
                                        _clientEndpoints =
                                            assignEnvironmentRouteAccount(
                                              endpoints: _clientEndpoints,
                                              clientEndpointId: endpoint.id,
                                              protocolPlanId: plan.id,
                                              routeId: route.id,
                                              account: account,
                                            );
                                      }),
                                ),
                            if (_submitted &&
                                widget.controller.environmentError != null) ...[
                              const SizedBox(height: 10),
                              InlineNotice(
                                message: widget.controller.environmentError!,
                                error: true,
                              ),
                            ],
                          ],
                        ),
                      ),
                    ),
            ),
          ),
          actions: reviewed
              ? [
                  TextButton(
                    key: const Key('environment-impact-back'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : widget.controller.clearEnvironmentReview,
                    child: Text(copy('common.back')),
                  ),
                  FilledButton.icon(
                    key: const Key('environment-publish'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _publish,
                    icon: widget.controller.environmentMutating
                        ? const SizedBox.square(
                            dimension: 13,
                            child: CircularProgressIndicator(strokeWidth: 1.5),
                          )
                        : const Icon(Icons.publish_outlined, size: 14),
                    label: Text(copy('environment.publish')),
                  ),
                ]
              : [
                  TextButton(
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _close,
                    child: Text(copy('common.cancel')),
                  ),
                  FilledButton.icon(
                    key: const Key('environment-review'),
                    onPressed: widget.controller.environmentMutating
                        ? null
                        : _review,
                    icon: widget.controller.environmentMutating
                        ? const SizedBox.square(
                            dimension: 13,
                            child: CircularProgressIndicator(strokeWidth: 1.5),
                          )
                        : const Icon(Icons.rule_outlined, size: 14),
                    label: Text(copy('environment.review')),
                  ),
                ],
        );
      },
    );
  }

  Future<void> _review() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final retentionDays = _recordingMode == 'off'
        ? 0
        : int.parse(_retention.text);
    await widget.controller.reviewSelectedEnvironment(
      EnvironmentDraftInput.fromEnvironment(
        widget.environment,
        expectedDraftRevision: 0,
        name: _name.text,
        state: _state,
        clientEndpoints: _clientEndpoints,
        contentRecording: EnvironmentContentRecordingPolicy(
          mode: _recordingMode,
          retentionDays: retentionDays,
        ),
        policySet: EnvironmentPolicySet(toolMode: _toolMode),
      ),
    );
    if (mounted) setState(() {});
  }

  Future<void> _publish() async {
    final result = await widget.controller.publishReviewedEnvironment();
    if (!mounted || result == null) return;
    Navigator.pop(context);
  }

  void _close() {
    widget.controller.clearEnvironmentReview();
    Navigator.pop(context);
  }
}

final class _EditorSectionLabel extends StatelessWidget {
  const _EditorSectionLabel({required this.label, this.detail});

  final String label;
  final String? detail;

  @override
  Widget build(BuildContext context) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.end,
      children: [
        Text(
          label.toUpperCase(),
          style: Theme.of(context).textTheme.labelMedium,
        ),
        if (detail case final value?) ...[
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              value,
              textAlign: TextAlign.right,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ),
        ],
      ],
    );
  }
}

final class _EditorEmptyState extends StatelessWidget {
  const _EditorEmptyState({required this.text});

  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 9),
      decoration: BoxDecoration(
        color: ViberColors.panel,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(5),
      ),
      child: Row(
        children: [
          const Icon(Icons.visibility_outlined, size: 14),
          const SizedBox(width: 7),
          Expanded(
            child: Text(text, style: Theme.of(context).textTheme.bodySmall),
          ),
        ],
      ),
    );
  }
}

typedef _RouteAccountChanged =
    void Function(
      EnvironmentProtocolPlan plan,
      EnvironmentRoute route,
      ProviderAccount? account,
    );

typedef _EndpointAdded =
    void Function(UpstreamEndpoint endpoint, ProviderAccount? account);

final class _EnvironmentEndpointAdder extends StatefulWidget {
  const _EnvironmentEndpointAdder({
    required this.current,
    required this.endpoints,
    required this.accounts,
    required this.copy,
    required this.clientCredentialValue,
    required this.enabled,
    required this.onAdd,
  });

  final List<EnvironmentClientEndpoint> current;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final String clientCredentialValue;
  final bool enabled;
  final _EndpointAdded onAdd;

  @override
  State<_EnvironmentEndpointAdder> createState() =>
      _EnvironmentEndpointAdderState();
}

final class _EnvironmentEndpointAdderState
    extends State<_EnvironmentEndpointAdder> {
  String _endpointId = '';
  String _accountId = '';

  @override
  Widget build(BuildContext context) {
    final available = widget.endpoints
        .where(
          (endpoint) =>
              endpoint.state == 'active' &&
              !environmentUsesUpstreamEndpoint(widget.current, endpoint.id),
        )
        .toList(growable: false);
    final selected = available
        .where((endpoint) => endpoint.id == _endpointId)
        .firstOrNull;
    final owned = selected == null
        ? const <ProviderAccount>[]
        : widget.accounts
              .where(
                (account) =>
                    account.upstreamEndpointId == selected.id && account.usable,
              )
              .toList(growable: false);
    final allowClient =
        selected != null && upstreamEndpointCanUseClientCredential(selected);
    final canAdd =
        selected != null &&
        (_accountId == widget.clientCredentialValue ||
            owned.any((account) => account.id == _accountId));
    return Container(
      padding: const EdgeInsets.all(8),
      decoration: BoxDecoration(
        color: ViberColors.panelRaised.withValues(alpha: 0.42),
        border: Border.all(color: ViberColors.dividerSoft),
        borderRadius: BorderRadius.circular(5),
      ),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 470;
          final endpointField = DropdownButtonFormField<String>(
            key: const Key('environment-endpoint-catalog'),
            initialValue: available.any((value) => value.id == _endpointId)
                ? _endpointId
                : null,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: widget.copy('environment.endpoint.add'),
            ),
            items: [
              for (final endpoint in available)
                DropdownMenuItem(
                  value: endpoint.id,
                  child: Text(
                    '${endpoint.displayName} · ${endpoint.origin}',
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
            ],
            onChanged: !widget.enabled || available.isEmpty
                ? null
                : (value) {
                    final endpoint = available.firstWhere(
                      (candidate) => candidate.id == value,
                    );
                    final accounts = widget.accounts.where(
                      (account) =>
                          account.upstreamEndpointId == endpoint.id &&
                          account.usable,
                    );
                    setState(() {
                      _endpointId = endpoint.id;
                      _accountId =
                          upstreamEndpointCanUseClientCredential(endpoint)
                          ? widget.clientCredentialValue
                          : accounts.firstOrNull?.id ?? '';
                    });
                  },
          );
          final accountField = DropdownButtonFormField<String>(
            key: Key('environment-endpoint-account-${selected?.id ?? 'none'}'),
            initialValue: _accountId.isEmpty ? null : _accountId,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: widget.copy('environment.account'),
              helperText: selected != null && !allowClient && owned.isEmpty
                  ? widget.copy('environment.endpoint.account_required')
                  : null,
            ),
            items: [
              if (allowClient)
                DropdownMenuItem(
                  value: widget.clientCredentialValue,
                  child: Text(widget.copy('environment.account.client')),
                ),
              for (final account in owned)
                DropdownMenuItem(
                  value: account.id,
                  child: Text(
                    account.displayName,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
            ],
            onChanged: !widget.enabled || selected == null
                ? null
                : (value) => setState(() => _accountId = value ?? ''),
          );
          final add = OutlinedButton.icon(
            key: const Key('environment-add-endpoint'),
            onPressed: !widget.enabled || !canAdd ? null : _add,
            icon: const Icon(Icons.add, size: 13),
            label: Text(widget.copy('environment.endpoint.add_action')),
          );
          if (compact) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                endpointField,
                if (selected != null) ...[
                  const SizedBox(height: 7),
                  accountField,
                ],
                const SizedBox(height: 7),
                Align(alignment: Alignment.centerRight, child: add),
              ],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(child: endpointField),
              if (selected != null) ...[
                const SizedBox(width: 8),
                Expanded(child: accountField),
              ],
              const SizedBox(width: 8),
              Padding(padding: const EdgeInsets.only(top: 2), child: add),
            ],
          );
        },
      ),
    );
  }

  void _add() {
    final endpoint = widget.endpoints.firstWhere(
      (candidate) => candidate.id == _endpointId,
    );
    final account = _accountId == widget.clientCredentialValue
        ? null
        : widget.accounts.firstWhere((candidate) => candidate.id == _accountId);
    widget.onAdd(endpoint, account);
    setState(() {
      _endpointId = '';
      _accountId = '';
    });
  }
}

final class _EnvironmentEndpointEditor extends StatelessWidget {
  const _EnvironmentEndpointEditor({
    required this.endpoint,
    required this.endpoints,
    required this.accounts,
    required this.copy,
    required this.clientCredentialValue,
    required this.enabled,
    required this.onAccountChanged,
  });

  final EnvironmentClientEndpoint endpoint;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final String clientCredentialValue;
  final bool enabled;
  final _RouteAccountChanged onAccountChanged;

  @override
  Widget build(BuildContext context) {
    return Container(
      key: Key('environment-client-endpoint-${endpoint.id}'),
      margin: const EdgeInsets.only(bottom: 8),
      decoration: BoxDecoration(
        color: ViberColors.panel,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(5),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
            child: Row(
              children: [
                const Icon(Icons.input, size: 13, color: ViberColors.route),
                const SizedBox(width: 6),
                Expanded(
                  child: Text(
                    endpoint.clientOrigin.toString(),
                    overflow: TextOverflow.ellipsis,
                    style: monoStyle.copyWith(color: ViberColors.text),
                  ),
                ),
                Text(endpoint.id, style: monoStyle),
              ],
            ),
          ),
          const Divider(height: 1),
          for (final plan in endpoint.protocolPlans)
            for (final route in plan.routes)
              _RouteAccountEditor(
                endpoint: endpoint,
                plan: plan,
                route: route,
                upstreamEndpoint: endpoints
                    .where((candidate) => candidate.id == route.endpointId)
                    .firstOrNull,
                accounts: accounts,
                copy: copy,
                clientCredentialValue: clientCredentialValue,
                enabled: enabled,
                onChanged: (account) => onAccountChanged(plan, route, account),
              ),
        ],
      ),
    );
  }
}

final class _RouteAccountEditor extends StatelessWidget {
  const _RouteAccountEditor({
    required this.endpoint,
    required this.plan,
    required this.route,
    required this.upstreamEndpoint,
    required this.accounts,
    required this.copy,
    required this.clientCredentialValue,
    required this.enabled,
    required this.onChanged,
  });

  final EnvironmentClientEndpoint endpoint;
  final EnvironmentProtocolPlan plan;
  final EnvironmentRoute route;
  final UpstreamEndpoint? upstreamEndpoint;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final String clientCredentialValue;
  final bool enabled;
  final ValueChanged<ProviderAccount?> onChanged;

  @override
  Widget build(BuildContext context) {
    final owned =
        accounts
            .where((account) => account.upstreamEndpointId == route.endpointId)
            .toList(growable: false)
          ..sort(
            (left, right) => left.displayName.compareTo(right.displayName),
          );
    final currentId = route.accountPolicy.mode == 'client_passthrough'
        ? clientCredentialValue
        : route.accountPolicy.preferredAccountId;
    final allowClient = canUseClientCredential(endpoint, plan, route);
    final currentItemExists =
        currentId == clientCredentialValue && allowClient ||
        owned.any((account) => account.id == currentId);
    final selectable = owned.where((account) => account.usable).length;
    return Padding(
      padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 470;
          final authority = _RouteEditorAuthority(
            route: route,
            endpoint: upstreamEndpoint,
          );
          final selector = DropdownButtonFormField<String>(
            key: Key('environment-route-account-${route.id}-$currentId'),
            initialValue: currentId.isEmpty ? null : currentId,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: copy('environment.account'),
              helperText: selectable == 0 && !allowClient
                  ? copy('environment.account.none')
                  : copy('environment.account.owner'),
            ),
            items: [
              if (allowClient)
                DropdownMenuItem(
                  value: clientCredentialValue,
                  child: Text(copy('environment.account.client')),
                ),
              if (!currentItemExists && currentId.isNotEmpty)
                DropdownMenuItem(
                  value: currentId,
                  enabled: false,
                  child: Text(
                    currentId == clientCredentialValue
                        ? '${copy('environment.account.client')} · ${copy('environment.account.unavailable')}'
                        : currentId,
                  ),
                ),
              for (final account in owned)
                DropdownMenuItem(
                  value: account.id,
                  enabled: account.usable,
                  child: Text(
                    account.usable
                        ? account.displayName
                        : '${account.displayName} · ${copy('environment.account.unavailable')}',
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
            ],
            onChanged: !enabled || selectable == 0 && !allowClient
                ? null
                : (value) {
                    if (value == null) return;
                    onChanged(
                      value == clientCredentialValue
                          ? null
                          : owned.firstWhere((account) => account.id == value),
                    );
                  },
          );
          if (compact) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [authority, const SizedBox(height: 7), selector],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(flex: 4, child: authority),
              const SizedBox(width: 10),
              Expanded(flex: 5, child: selector),
            ],
          );
        },
      ),
    );
  }
}

final class _RouteEditorAuthority extends StatelessWidget {
  const _RouteEditorAuthority({required this.route, required this.endpoint});

  final EnvironmentRoute route;
  final UpstreamEndpoint? endpoint;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            const Icon(Icons.arrow_forward, size: 13, color: ViberColors.route),
            const SizedBox(width: 5),
            Expanded(
              child: Text(
                endpoint?.displayName ?? route.endpointId,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.titleSmall,
              ),
            ),
          ],
        ),
        const SizedBox(height: 2),
        Text(
          route.endpointOrigin.toString(),
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: monoStyle,
        ),
        Text(
          '${route.id} · ${route.backendProtocol}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: monoStyle.copyWith(color: ViberColors.textFaint),
        ),
      ],
    );
  }
}

final class _EnvironmentImpactReview extends StatelessWidget {
  const _EnvironmentImpactReview({required this.impact, required this.copy});

  final EnvironmentImpact impact;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final color = switch (impact.classification) {
      'hot_switch' => ViberColors.verified,
      'reconnect_required' => ViberColors.warning,
      _ => ViberColors.danger,
    };
    return Semantics(
      liveRegion: true,
      container: true,
      label: copy('environment.impact.title'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  copy('environment.impact.title'),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ),
              StatusPill(
                label: copy('environment.impact.${impact.classification}'),
                color: color,
                icon: Icons.sync_alt,
              ),
            ],
          ),
          const SizedBox(height: 7),
          Text(
            copy('environment.impact.description.${impact.classification}'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 12),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              _ImpactCount(
                count: impact.hotSwitchCount,
                label: copy('environment.impact.hot'),
                color: ViberColors.verified,
              ),
              _ImpactCount(
                count: impact.reconnectRequiredCount,
                label: copy('environment.impact.reconnect'),
                color: ViberColors.warning,
              ),
              _ImpactCount(
                count: impact.restartRequiredCount,
                label: copy('environment.impact.restart'),
                color: ViberColors.danger,
              ),
            ],
          ),
          const SizedBox(height: 13),
          _EditorSectionLabel(
            label: copy.format('environment.impact.affected', {
              'count': impact.affected.length,
            }),
          ),
          const SizedBox(height: 5),
          if (impact.affected.isEmpty)
            Text(
              copy('environment.impact.none'),
              style: Theme.of(context).textTheme.bodySmall,
            )
          else
            Container(
              decoration: BoxDecoration(
                border: Border.all(color: ViberColors.divider),
                borderRadius: BorderRadius.circular(5),
              ),
              child: Column(
                children: [
                  for (final capture in impact.affected.take(12))
                    Container(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 9,
                        vertical: 6,
                      ),
                      decoration: const BoxDecoration(
                        border: Border(
                          bottom: BorderSide(color: ViberColors.dividerSoft),
                        ),
                      ),
                      child: Row(
                        children: [
                          Icon(
                            capture.captureKind == 'managed_run'
                                ? Icons.terminal
                                : Icons.link,
                            size: 13,
                            color: ViberColors.textMuted,
                          ),
                          const SizedBox(width: 7),
                          Expanded(
                            child: Text(
                              capture.captureId,
                              overflow: TextOverflow.ellipsis,
                              style: monoStyle.copyWith(
                                color: ViberColors.text,
                              ),
                            ),
                          ),
                          Text(
                            copy(
                              'environment.impact.${capture.classification}',
                            ),
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ],
                      ),
                    ),
                  if (impact.affected.length > 12)
                    Padding(
                      padding: const EdgeInsets.all(7),
                      child: Text(
                        copy.format('environment.impact.more', {
                          'count': impact.affected.length - 12,
                        }),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ),
                ],
              ),
            ),
        ],
      ),
    );
  }
}

final class _ImpactCount extends StatelessWidget {
  const _ImpactCount({
    required this.count,
    required this.label,
    required this.color,
  });

  final int count;
  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      constraints: const BoxConstraints(minWidth: 112),
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.07),
        border: Border.all(color: color.withValues(alpha: 0.28)),
        borderRadius: BorderRadius.circular(5),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            '$count',
            style: Theme.of(
              context,
            ).textTheme.titleMedium?.copyWith(color: color),
          ),
          const SizedBox(width: 6),
          Text(label, style: Theme.of(context).textTheme.bodySmall),
        ],
      ),
    );
  }
}

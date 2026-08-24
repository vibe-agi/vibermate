import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import 'deletion_dialog.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'environment_editing.dart';
import 'workbench_controller.dart';

final class EnvironmentsView extends StatefulWidget {
  const EnvironmentsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<EnvironmentsView> createState() => _EnvironmentsViewState();
}

final class _EnvironmentsViewState extends State<EnvironmentsView> {
  bool _masterVisible = true;
  double _masterWidth = ViberMetrics.masterPaneWidth;

  WorkbenchController get controller => widget.controller;
  AppCopy get copy => widget.copy;

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
            dismissLabel: copy('common.dismiss'),
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
                copy: copy,
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
                masterVisible: _masterVisible,
                onToggleMaster: compact
                    ? null
                    : () => setState(() => _masterVisible = !_masterVisible),
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
              final maxWidth = math.min(
                ViberMetrics.masterPaneMaxWidth,
                constraints.maxWidth * 0.4,
              );
              final masterWidth = _masterWidth
                  .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
                  .toDouble();
              return Row(
                children: [
                  if (_masterVisible) ...[
                    SizedBox(
                      key: const Key('environment-master-pane'),
                      width: masterWidth,
                      child: directory,
                    ),
                    WorkbenchPaneDivider(
                      key: const Key('environment-master-divider'),
                      label: copy('common.resize_directory'),
                      onDrag: (delta) => setState(() {
                        _masterWidth = (_masterWidth + delta)
                            .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
                            .toDouble();
                      }),
                    ),
                  ],
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
        barrierDismissible: true,
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
        barrierDismissible: true,
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
    required this.copy,
  });

  final List<EnvironmentRecord> environments;
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
            label:
                '${environment.name}, ${environment.state}, '
                '${copy.format('environment.routes', {'count': '$routes'})}',
            child: Material(
              color: selected
                  ? context.viberColors.selection
                  : Colors.transparent,
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
                              environment.name,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          if (environment.systemOwned)
                            Tooltip(
                              message: copy('environment.system_managed'),
                              child: Semantics(
                                label: copy('environment.system_managed'),
                                child: Icon(
                                  Icons.lock_outline_rounded,
                                  key: Key(
                                    'environment-system-marker-${environment.id}',
                                  ),
                                  size: 12,
                                  color: context.viberColors.textFaint,
                                ),
                              ),
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
    required this.masterVisible,
    required this.onToggleMaster,
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
  final bool masterVisible;
  final VoidCallback? onToggleMaster;

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
          color: context.viberColors.panel,
          child: LayoutBuilder(
            builder: (context, constraints) {
              final identityContent = Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Wrap(
                    spacing: 6,
                    runSpacing: 5,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      IconButton(
                        key: const Key('environment-delete'),
                        onPressed: controller.inventoryMutating
                            ? null
                            : () => _confirmDeleteEnvironment(
                                context,
                                controller,
                                copy,
                                value,
                              ),
                        tooltip: copy('deletion.environment.title'),
                        icon: const Icon(Icons.delete_outline, size: 15),
                        color: context.viberColors.danger,
                        constraints: const BoxConstraints.tightFor(
                          width: 26,
                          height: 26,
                        ),
                        padding: EdgeInsets.zero,
                      ),
                      Text(
                        value.name,
                        style: Theme.of(context).textTheme.headlineSmall,
                      ),
                      if (historical)
                        StatusPill(
                          label: copy('environment.history.frozen'),
                          color: context.viberColors.route,
                        ),
                      if (value.systemOwned)
                        StatusPill(
                          label: copy('common.system'),
                          color: context.viberColors.textMuted,
                        ),
                      StatusPill(
                        label: copy('environment.state.${value.state}'),
                        color: value.state == 'active'
                            ? context.viberColors.verified
                            : context.viberColors.textFaint,
                      ),
                    ],
                  ),
                  const SizedBox(height: 3),
                  Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Text(
                        copy.format('common.revision', {
                          'revision': value.revision,
                        }),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                      const SizedBox(width: 6),
                      Tooltip(
                        message: '${value.id} · ${value.digest}',
                        child: Icon(
                          Icons.info_outline,
                          size: 13,
                          color: context.viberColors.textFaint,
                          semanticLabel: copy('common.technical_details'),
                        ),
                      ),
                    ],
                  ),
                ],
              );
              final identity = Row(
                children: [
                  if (onToggleMaster case final toggle?) ...[
                    IconButton(
                      key: const Key('environment-directory-toggle'),
                      onPressed: toggle,
                      tooltip: copy(
                        masterVisible
                            ? 'common.hide_directory'
                            : 'common.show_directory',
                      ),
                      icon: Icon(Icons.view_sidebar_outlined, size: 15),
                    ),
                    const SizedBox(width: 5),
                  ],
                  Expanded(child: identityContent),
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
              color: context.viberColors.route.withValues(alpha: 0.07),
              child: Row(
                children: [
                  Icon(
                    Icons.history,
                    size: 14,
                    color: context.viberColors.route,
                  ),
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
                        ? 'environment.connection_only'
                        : 'environment.no_client_flows',
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

/// Environment deletion is offered from the detail header rather than the
/// directory: the directory is where a user scans, and a destructive action
/// next to a row they are only passing over is an accident waiting to happen.
void _confirmDeleteEnvironment(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
  EnvironmentRecord environment,
) {
  unawaited(
    showDialog<DeletionOutcome>(
      context: context,
      builder: (_) => DeletionConfirmation(
        copy: copy,
        title: copy('deletion.environment.title'),
        subject: environment.name,
        consequence: copy('deletion.environment.consequence'),
        onConfirm: () async {
          final result = await controller.deleteEnvironment(environment.id);
          if (result == null) {
            throw StateError(
              controller.inventoryError ?? 'environment delete failed',
            );
          }
          return result;
        },
      ),
    ),
  );
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
      key: Key('environment-client-plan-${clientEndpoint.id}'),
      margin: const EdgeInsets.only(bottom: 8),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
            child: Row(
              children: [
                Icon(Icons.input, size: 15, color: context.viberColors.route),
                const SizedBox(width: 7),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        clientEndpoint.clientOrigin.toString(),
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                      Text(
                        clientEndpoint.id,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: monoStyle,
                      ),
                    ],
                  ),
                ),
                Text(
                  copy('environment.client_target'),
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
    final upstream = plan.destination.upstream;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: double.infinity,
          color: context.viberColors.panelRaised.withValues(alpha: 0.45),
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
          child: LayoutBuilder(
            builder: (context, constraints) {
              if (constraints.maxWidth < 560) {
                return Text(
                  '${copy('environment.mapping.client')}  →  ${copy('environment.mapping.upstream')}  →  ${copy('environment.mapping.accounts')}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.labelMedium,
                );
              }
              return Row(
                children: [
                  Expanded(
                    flex: 3,
                    child: Text(
                      copy('environment.mapping.client'),
                      style: Theme.of(context).textTheme.labelMedium,
                    ),
                  ),
                  const SizedBox(width: 24),
                  Expanded(
                    flex: 4,
                    child: Text(
                      copy('environment.mapping.upstream'),
                      style: Theme.of(context).textTheme.labelMedium,
                    ),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    flex: 4,
                    child: Text(
                      copy('environment.mapping.accounts'),
                      style: Theme.of(context).textTheme.labelMedium,
                    ),
                  ),
                ],
              );
            },
          ),
        ),
        if (upstream == null)
          _OriginalDestinationRow(
            clientProtocol: plan.clientProtocol,
            copy: copy,
          )
        else
          for (final route in upstream.routes)
            _RouteAuthorityRow(
              route: route,
              clientProtocol: plan.clientProtocol,
              isDefault: route.id == upstream.defaultRouteId,
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

final class _OriginalDestinationRow extends StatelessWidget {
  const _OriginalDestinationRow({
    required this.clientProtocol,
    required this.copy,
  });

  final String clientProtocol;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.all(10),
    child: Row(
      children: [
        Expanded(
          flex: 3,
          child: Text(
            _localizedCopy(copy, 'routes.protocol', clientProtocol),
            style: Theme.of(context).textTheme.titleSmall,
          ),
        ),
        const SizedBox(width: 24),
        Expanded(
          flex: 4,
          child: Text(
            copy('environment.destination.original'),
            style: Theme.of(context).textTheme.titleSmall,
          ),
        ),
        const SizedBox(width: 10),
        Expanded(
          flex: 4,
          child: Text(
            copy('environment.destination.original.detail'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
      ],
    ),
  );
}

final class _RouteAuthorityRow extends StatelessWidget {
  const _RouteAuthorityRow({
    required this.route,
    required this.clientProtocol,
    required this.isDefault,
    required this.endpoint,
    required this.accounts,
    required this.copy,
  });

  final EnvironmentRoute route;
  final String clientProtocol;
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
        final compact = constraints.maxWidth < 560;
        final client = Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              _localizedCopy(copy, 'routes.protocol', clientProtocol),
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.titleSmall,
            ),
            Text(
              copy('environment.destination.upstream'),
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        );
        final upstream = Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Flexible(
                  child: Tooltip(
                    message: route.id,
                    child: Text(
                      endpoint?.displayName ?? route.endpointId,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                  ),
                ),
                const SizedBox(width: 6),
                InlineStatus(
                  label: isDefault
                      ? copy('environment.route.default')
                      : copy('environment.route.fallback'),
                  color: isDefault
                      ? context.viberColors.route
                      : context.viberColors.textMuted,
                ),
              ],
            ),
            Text(
              '${_localizedCopy(copy, 'routes.protocol', route.backendProtocol)} · ${route.endpointOrigin}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: monoStyle,
            ),
          ],
        );
        final accountList = Wrap(
          spacing: 6,
          runSpacing: 5,
          children: [
            for (final account in candidates)
              StatusPill(
                label: account.displayName,
                color: account.upstreamEndpointId == route.endpointId
                    ? context.viberColors.verified
                    : context.viberColors.danger,
                icon: Icons.key_outlined,
              ),
            if (candidates.isEmpty)
              Text(
                copy('environment.account.no_candidate'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
          ],
        );
        return Container(
          padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
          decoration: BoxDecoration(
            border: Border(
              bottom: BorderSide(color: context.viberColors.dividerSoft),
            ),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (compact) ...[
                client,
                const SizedBox(height: 5),
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(
                      Icons.arrow_forward,
                      size: 14,
                      color: context.viberColors.route,
                    ),
                    const SizedBox(width: 7),
                    Expanded(child: upstream),
                  ],
                ),
                const SizedBox(height: 6),
                accountList,
              ] else
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(flex: 3, child: client),
                    Padding(
                      padding: const EdgeInsets.only(top: 3),
                      child: Icon(
                        Icons.arrow_forward,
                        size: 14,
                        color: context.viberColors.route,
                      ),
                    ),
                    const SizedBox(width: 10),
                    Expanded(flex: 4, child: upstream),
                    const SizedBox(width: 10),
                    Expanded(flex: 4, child: accountList),
                  ],
                ),
              if (invalid.isNotEmpty)
                Padding(
                  padding: const EdgeInsets.only(top: 5),
                  child: Text(
                    copy('environment.account.invalid'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.danger,
                    ),
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
  final _formKey = GlobalKey<FormState>();
  final _name = TextEditingController();
  final _id = TextEditingController();
  late final TextEditingController _retention;
  List<EnvironmentClientEndpoint> _clientEndpoints = const [];
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
        return AlertDialog(
          insetPadding: const EdgeInsets.symmetric(
            horizontal: 16,
            vertical: 20,
          ),
          titlePadding: const EdgeInsets.fromLTRB(18, 16, 18, 8),
          contentPadding: const EdgeInsets.fromLTRB(18, 0, 18, 4),
          actionsPadding: const EdgeInsets.fromLTRB(12, 6, 12, 12),
          title: Text(copy('environment.create.title')),
          content: SizedBox(
            width: 640,
            child: ConstrainedBox(
              constraints: BoxConstraints(maxHeight: media.height * 0.74),
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
                            const SizedBox(height: 9),
                            _EditorSectionLabel(
                              label: copy('environment.edit.identity'),
                            ),
                            const SizedBox(height: 6),
                            _EditorFieldGrid(
                              children: [
                                CompactLabeledControl(
                                  label: copy('environment.field.name'),
                                  child: TextFormField(
                                    key: const Key('environment-create-name'),
                                    controller: _name,
                                    autofocus: true,
                                    maxLength: 256,
                                    textAlignVertical: TextAlignVertical.center,
                                    decoration: const InputDecoration(
                                      counterText: '',
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
                                ),
                                CompactLabeledControl(
                                  label: copy('environment.field.id'),
                                  child: TextFormField(
                                    key: const Key('environment-create-id'),
                                    controller: _id,
                                    enabled: _draftEnvironmentId == null,
                                    maxLength: 128,
                                    autocorrect: false,
                                    enableSuggestions: false,
                                    textAlignVertical: TextAlignVertical.center,
                                    decoration: const InputDecoration(
                                      counterText: '',
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
                                ),
                              ],
                            ),
                            const SizedBox(height: 10),
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
                              enabled: !widget.controller.environmentMutating,
                              onOriginal:
                                  (
                                    clientEndpointId,
                                    protocolPlanId,
                                    clientOrigin,
                                    clientProtocol,
                                  ) => setState(() {
                                    _clientEndpoints =
                                        appendEnvironmentOriginalDestination(
                                          endpoints: _clientEndpoints,
                                          clientEndpointId: clientEndpointId,
                                          protocolPlanId: protocolPlanId,
                                          clientOrigin: clientOrigin,
                                          clientProtocol: clientProtocol,
                                          identityNonce: widget.controller
                                              .newEnvironmentChildIdentityNonce(),
                                        );
                                  }),
                              onAdd:
                                  (
                                    clientEndpointId,
                                    protocolPlanId,
                                    clientOrigin,
                                    clientProtocol,
                                    endpoint,
                                    account,
                                  ) => setState(() {
                                    _clientEndpoints =
                                        appendEnvironmentUpstreamEndpoint(
                                          endpoints: _clientEndpoints,
                                          clientEndpointId: clientEndpointId,
                                          protocolPlanId: protocolPlanId,
                                          clientOrigin: clientOrigin,
                                          clientProtocol: clientProtocol,
                                          upstreamEndpoint: endpoint,
                                          account: account,
                                          identityNonce: widget.controller
                                              .newEnvironmentChildIdentityNonce(),
                                        );
                                  }),
                            ),
                            const SizedBox(height: 8),
                            if (_clientEndpoints.isEmpty)
                              _EditorEmptyState(
                                text: copy('environment.edit.routes.empty'),
                              )
                            else
                              for (final (index, endpoint)
                                  in _clientEndpoints.indexed)
                                _EnvironmentEndpointEditor(
                                  controller: widget.controller,
                                  endpoint: endpoint,
                                  initiallyExpanded: index == 0,
                                  endpoints:
                                      widget.controller.data?.endpoints ??
                                      const [],
                                  accounts:
                                      widget.controller.data?.accounts ??
                                      const [],
                                  copy: copy,
                                  enabled:
                                      !widget.controller.environmentMutating,
                                  onRemove: () => setState(() {
                                    _clientEndpoints = [
                                      for (final value in _clientEndpoints)
                                        if (value.id != endpoint.id) value,
                                    ];
                                  }),
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
                                  onModelChanged: (plan, route, mappings) =>
                                      setState(() {
                                        _clientEndpoints =
                                            assignEnvironmentRouteModelMappings(
                                              endpoints: _clientEndpoints,
                                              clientEndpointId: endpoint.id,
                                              protocolPlanId: plan.id,
                                              routeId: route.id,
                                              mappings: mappings,
                                            );
                                      }),
                                ),
                            const SizedBox(height: 10),
                            _EditorSectionLabel(
                              label: copy('environment.edit.policy'),
                              detail: copy('environment.edit.policy.detail'),
                            ),
                            const SizedBox(height: 6),
                            _EditorFieldGrid(
                              maxColumns: 3,
                              children: [
                                CompactLabeledControl(
                                  label: copy('environment.policy.tool_mode'),
                                  child: CompactSelectField<String>(
                                    key: const Key(
                                      'environment-create-tool-mode',
                                    ),
                                    initialValue: _toolMode,
                                    isExpanded: true,
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
                                    onChanged:
                                        widget.controller.environmentMutating
                                        ? null
                                        : (value) => setState(
                                            () => _toolMode = value!,
                                          ),
                                  ),
                                ),
                                CompactLabeledControl(
                                  label: copy('environment.recording.mode'),
                                  child: CompactSelectField<String>(
                                    key: const Key(
                                      'environment-create-recording',
                                    ),
                                    initialValue: _recordingMode,
                                    isExpanded: true,
                                    items: [
                                      for (final value in const [
                                        'full',
                                        'metadata_only',
                                        'off',
                                      ])
                                        DropdownMenuItem(
                                          value: value,
                                          child: Text(
                                            copy(
                                              'environment.recording.$value',
                                            ),
                                          ),
                                        ),
                                    ],
                                    onChanged:
                                        widget.controller.environmentMutating
                                        ? null
                                        : (value) => setState(
                                            () => _recordingMode = value!,
                                          ),
                                  ),
                                ),
                                if (_recordingMode != 'off')
                                  CompactLabeledControl(
                                    label: copy(
                                      'environment.recording.retention',
                                    ),
                                    child: TextFormField(
                                      key: const Key(
                                        'environment-create-retention',
                                      ),
                                      controller: _retention,
                                      keyboardType: TextInputType.number,
                                      textAlignVertical:
                                          TextAlignVertical.center,
                                      decoration: InputDecoration(
                                        suffixText: copy(
                                          'environment.recording.days',
                                        ),
                                      ),
                                      validator: (value) {
                                        final days = int.tryParse(value ?? '');
                                        return days == null ||
                                                days < 1 ||
                                                days > 3650
                                            ? copy(
                                                'environment.validation.retention',
                                              )
                                            : null;
                                      },
                                    ),
                                  ),
                              ],
                            ),
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
    final retentionDays = _recordingMode == 'off'
        ? 0
        : int.parse(_retention.text);
    final normalizedEndpoints = normalizeEnvironmentDraftRevisions(
      base: const [],
      edited: _clientEndpoints,
    );
    final impact = await widget.controller.reviewNewEnvironment(
      environmentId,
      EnvironmentDraftInput(
        expectedDraftRevision: 0,
        name: _name.text,
        state: 'active',
        clientEndpoints: normalizedEndpoints,
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
          titlePadding: const EdgeInsets.fromLTRB(18, 16, 18, 8),
          contentPadding: const EdgeInsets.fromLTRB(18, 0, 18, 4),
          actionsPadding: const EdgeInsets.fromLTRB(12, 6, 12, 12),
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
                color: context.viberColors.textMuted,
              ),
            ],
          ),
          content: SizedBox(
            width: 640,
            child: ConstrainedBox(
              constraints: BoxConstraints(maxHeight: media.height * 0.74),
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
                            const SizedBox(height: 9),
                            _EditorSectionLabel(
                              label: copy('environment.edit.identity'),
                            ),
                            const SizedBox(height: 6),
                            _EditorFieldGrid(
                              children: [
                                CompactLabeledControl(
                                  label: copy('environment.field.name'),
                                  child: TextFormField(
                                    key: const Key('environment-editor-name'),
                                    controller: _name,
                                    autofocus: true,
                                    maxLength: 256,
                                    textAlignVertical: TextAlignVertical.center,
                                    decoration: const InputDecoration(
                                      counterText: '',
                                    ),
                                    validator: (value) =>
                                        value == null ||
                                            value.isEmpty ||
                                            value.trim() != value
                                        ? copy('environment.validation.name')
                                        : null,
                                  ),
                                ),
                                CompactLabeledControl(
                                  label: copy('environment.field.state'),
                                  child: CompactSelectField<String>(
                                    key: const Key('environment-editor-state'),
                                    initialValue: _state,
                                    isExpanded: true,
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
                                    onChanged:
                                        widget.controller.environmentMutating
                                        ? null
                                        : (value) =>
                                              setState(() => _state = value!),
                                  ),
                                ),
                              ],
                            ),
                            const SizedBox(height: 10),
                            _EditorSectionLabel(
                              label: copy('environment.edit.policy'),
                              detail: copy('environment.edit.policy.detail'),
                            ),
                            const SizedBox(height: 6),
                            _EditorFieldGrid(
                              maxColumns: 3,
                              children: [
                                CompactLabeledControl(
                                  label: copy('environment.policy.tool_mode'),
                                  child: CompactSelectField<String>(
                                    key: const Key(
                                      'environment-editor-tool-mode',
                                    ),
                                    initialValue: _toolMode,
                                    isExpanded: true,
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
                                    onChanged:
                                        widget.controller.environmentMutating
                                        ? null
                                        : (value) => setState(
                                            () => _toolMode = value!,
                                          ),
                                  ),
                                ),
                                CompactLabeledControl(
                                  label: copy('environment.recording.mode'),
                                  child: CompactSelectField<String>(
                                    key: const Key(
                                      'environment-editor-recording',
                                    ),
                                    initialValue: _recordingMode,
                                    isExpanded: true,
                                    items: [
                                      for (final value in const [
                                        'full',
                                        'metadata_only',
                                        'off',
                                      ])
                                        DropdownMenuItem(
                                          value: value,
                                          child: Text(
                                            copy(
                                              'environment.recording.$value',
                                            ),
                                          ),
                                        ),
                                    ],
                                    onChanged:
                                        widget.controller.environmentMutating
                                        ? null
                                        : (value) => setState(
                                            () => _recordingMode = value!,
                                          ),
                                  ),
                                ),
                                if (_recordingMode != 'off')
                                  CompactLabeledControl(
                                    label: copy(
                                      'environment.recording.retention',
                                    ),
                                    child: TextFormField(
                                      key: const Key(
                                        'environment-editor-retention',
                                      ),
                                      controller: _retention,
                                      keyboardType: TextInputType.number,
                                      textAlignVertical:
                                          TextAlignVertical.center,
                                      decoration: InputDecoration(
                                        suffixText: copy(
                                          'environment.recording.days',
                                        ),
                                      ),
                                      validator: (value) {
                                        final days = int.tryParse(value ?? '');
                                        return days == null ||
                                                days < 1 ||
                                                days > 3650
                                            ? copy(
                                                'environment.validation.retention',
                                              )
                                            : null;
                                      },
                                    ),
                                  ),
                              ],
                            ),
                            const SizedBox(height: 10),
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
                              enabled: !widget.controller.environmentMutating,
                              onOriginal:
                                  (
                                    clientEndpointId,
                                    protocolPlanId,
                                    clientOrigin,
                                    clientProtocol,
                                  ) => setState(() {
                                    _clientEndpoints =
                                        appendEnvironmentOriginalDestination(
                                          endpoints: _clientEndpoints,
                                          clientEndpointId: clientEndpointId,
                                          protocolPlanId: protocolPlanId,
                                          clientOrigin: clientOrigin,
                                          clientProtocol: clientProtocol,
                                          identityNonce: widget.controller
                                              .newEnvironmentChildIdentityNonce(),
                                        );
                                  }),
                              onAdd:
                                  (
                                    clientEndpointId,
                                    protocolPlanId,
                                    clientOrigin,
                                    clientProtocol,
                                    endpoint,
                                    account,
                                  ) => setState(() {
                                    _clientEndpoints =
                                        appendEnvironmentUpstreamEndpoint(
                                          endpoints: _clientEndpoints,
                                          clientEndpointId: clientEndpointId,
                                          protocolPlanId: protocolPlanId,
                                          clientOrigin: clientOrigin,
                                          clientProtocol: clientProtocol,
                                          upstreamEndpoint: endpoint,
                                          account: account,
                                          identityNonce: widget.controller
                                              .newEnvironmentChildIdentityNonce(),
                                        );
                                  }),
                            ),
                            const SizedBox(height: 8),
                            if (_clientEndpoints.isEmpty)
                              _EditorEmptyState(
                                text: copy('environment.edit.routes.empty'),
                              )
                            else
                              for (final (index, endpoint)
                                  in _clientEndpoints.indexed)
                                _EnvironmentEndpointEditor(
                                  controller: widget.controller,
                                  endpoint: endpoint,
                                  initiallyExpanded: index == 0,
                                  endpoints:
                                      widget.controller.data?.endpoints ??
                                      const [],
                                  accounts:
                                      widget.controller.data?.accounts ??
                                      const [],
                                  copy: copy,
                                  enabled:
                                      !widget.controller.environmentMutating,
                                  onRemove: () => setState(() {
                                    _clientEndpoints = [
                                      for (final value in _clientEndpoints)
                                        if (value.id != endpoint.id) value,
                                    ];
                                  }),
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
                                  onModelChanged: (plan, route, mappings) =>
                                      setState(() {
                                        _clientEndpoints =
                                            assignEnvironmentRouteModelMappings(
                                              endpoints: _clientEndpoints,
                                              clientEndpointId: endpoint.id,
                                              protocolPlanId: plan.id,
                                              routeId: route.id,
                                              mappings: mappings,
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
    final normalizedEndpoints = normalizeEnvironmentDraftRevisions(
      base: widget.environment.clientEndpoints,
      edited: _clientEndpoints,
    );
    await widget.controller.reviewSelectedEnvironment(
      EnvironmentDraftInput.fromEnvironment(
        widget.environment,
        expectedDraftRevision: 0,
        name: _name.text,
        state: _state,
        clientEndpoints: normalizedEndpoints,
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

final class _EditorFieldGrid extends StatelessWidget {
  const _EditorFieldGrid({required this.children, this.maxColumns = 2});

  final List<Widget> children;
  final int maxColumns;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final columns = maxColumns >= 3 && constraints.maxWidth >= 600
            ? 3
            : constraints.maxWidth >= 420
            ? 2
            : 1;
        final width = (constraints.maxWidth - (columns - 1) * 8) / columns;
        return Wrap(
          spacing: 8,
          runSpacing: 7,
          children: [
            for (final child in children) SizedBox(width: width, child: child),
          ],
        );
      },
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
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
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
      ProviderAccount account,
    );

typedef _RouteModelChanged =
    void Function(
      EnvironmentProtocolPlan plan,
      EnvironmentRoute route,
      List<EnvironmentModelMapping> mappings,
    );

typedef _EndpointAdded =
    void Function(
      String? clientEndpointId,
      String? protocolPlanId,
      Uri clientOrigin,
      String clientProtocol,
      UpstreamEndpoint endpoint,
      ProviderAccount account,
    );

typedef _OriginalDestinationSelected =
    void Function(
      String? clientEndpointId,
      String? protocolPlanId,
      Uri clientOrigin,
      String clientProtocol,
    );

final class _ClientPlanTarget {
  const _ClientPlanTarget({
    required this.clientOrigin,
    required this.clientProtocol,
    this.clientEndpointId,
    this.protocolPlanId,
    this.destinationKind,
  });

  factory _ClientPlanTarget.existing(
    EnvironmentClientEndpoint endpoint,
    EnvironmentProtocolPlan plan,
  ) => _ClientPlanTarget(
    clientOrigin: endpoint.clientOrigin,
    clientProtocol: plan.clientProtocol,
    clientEndpointId: endpoint.id,
    protocolPlanId: plan.id,
    destinationKind: plan.destination.kind,
  );

  final Uri clientOrigin;
  final String clientProtocol;
  final String? clientEndpointId;
  final String? protocolPlanId;
  final String? destinationKind;

  bool get exists => clientEndpointId != null && protocolPlanId != null;

  String get key => '$clientOrigin\u0000$clientProtocol';
}

List<_ClientPlanTarget> _clientPlanTargets(
  List<EnvironmentClientEndpoint> current,
) {
  final targets = <_ClientPlanTarget>[
    _ClientPlanTarget(
      clientOrigin: Uri.parse('https://api.anthropic.com'),
      clientProtocol: 'anthropic_messages',
    ),
    _ClientPlanTarget(
      clientOrigin: Uri.parse('https://api.openai.com'),
      clientProtocol: 'openai_responses',
    ),
    _ClientPlanTarget(
      clientOrigin: Uri.parse('https://api.openai.com'),
      clientProtocol: 'openai_chat',
    ),
    _ClientPlanTarget(
      clientOrigin: Uri.parse('https://chatgpt.com'),
      clientProtocol: 'openai_responses',
    ),
  ];
  for (final endpoint in current) {
    for (final plan in endpoint.protocolPlans) {
      final existing = _ClientPlanTarget.existing(endpoint, plan);
      final index = targets.indexWhere((target) => target.key == existing.key);
      if (index < 0) {
        targets.add(existing);
      } else {
        targets[index] = existing;
      }
    }
  }
  return List.unmodifiable(targets);
}

final class _EnvironmentEndpointAdder extends StatefulWidget {
  const _EnvironmentEndpointAdder({
    required this.current,
    required this.endpoints,
    required this.accounts,
    required this.copy,
    required this.enabled,
    required this.onOriginal,
    required this.onAdd,
  });

  final List<EnvironmentClientEndpoint> current;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final bool enabled;
  final _OriginalDestinationSelected onOriginal;
  final _EndpointAdded onAdd;

  @override
  State<_EnvironmentEndpointAdder> createState() =>
      _EnvironmentEndpointAdderState();
}

final class _EnvironmentEndpointAdderState
    extends State<_EnvironmentEndpointAdder> {
  final _pendingFieldKey = GlobalKey<FormFieldState<String>>();
  String _targetKey = '';
  String _destinationKind = 'original';
  String _endpointId = '';
  String _accountId = '';
  bool _pending = false;

  @override
  Widget build(BuildContext context) {
    final targets = _clientPlanTargets(widget.current);
    final existingTargets = targets.where((target) => target.exists).toList();
    final target = targets
        .where((candidate) => candidate.key == _targetKey)
        .firstOrNull;
    final effectiveTarget =
        target ?? (existingTargets.length == 1 ? existingTargets.single : null);
    final destinationKind = _targetKey.isEmpty
        ? effectiveTarget?.destinationKind ?? _destinationKind
        : _destinationKind;
    final originalDestination = destinationKind == 'original';
    final available = widget.endpoints
        .where(
          (endpoint) =>
              endpoint.state == 'active' &&
              effectiveTarget != null &&
              upstreamEndpointSupportsClientProtocol(
                endpoint,
                effectiveTarget.clientProtocol,
              ) &&
              (effectiveTarget.protocolPlanId == null ||
                  !widget.current
                      .firstWhere(
                        (client) =>
                            client.id == effectiveTarget.clientEndpointId,
                      )
                      .protocolPlans
                      .firstWhere(
                        (plan) => plan.id == effectiveTarget.protocolPlanId,
                      )
                      .routes
                      .any((route) => route.endpointId == endpoint.id)),
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
    final canAdd =
        selected != null && owned.any((account) => account.id == _accountId);
    final canApply = effectiveTarget != null && (originalDestination || canAdd);
    return FormField<String>(
      key: _pendingFieldKey,
      initialValue: '',
      validator: (_) {
        if (!_pending) return null;
        if (!originalDestination && !canAdd) {
          return widget.copy('environment.endpoint.account_required');
        }
        return widget.copy('environment.endpoint.pending');
      },
      builder: (field) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Container(
            padding: const EdgeInsets.all(8),
            decoration: BoxDecoration(
              color: context.viberColors.panelRaised.withValues(alpha: 0.42),
              border: Border.all(
                color: field.hasError
                    ? Theme.of(context).colorScheme.error
                    : context.viberColors.dividerSoft,
              ),
              borderRadius: ViberMetrics.surfaceRadius,
            ),
            child: LayoutBuilder(
              builder: (context, constraints) {
                final compact = constraints.maxWidth < 470;
                final targetField = CompactLabeledControl(
                  label: widget.copy('environment.mapping.client'),
                  detail: effectiveTarget?.clientOrigin.toString(),
                  child: CompactSelectField<String>(
                    key: const Key('environment-client-plan-target'),
                    initialValue: effectiveTarget?.key,
                    isExpanded: true,
                    decoration: InputDecoration(
                      hintText: targets.length > 1
                          ? widget.copy('environment.endpoint.client_choose')
                          : null,
                    ),
                    menuItemHeight: 48,
                    menuMaxLines: 2,
                    selectedItemBuilder: (context, selectedItem) {
                      final candidate = targets.firstWhere(
                        (target) => target.key == selectedItem.value,
                      );
                      return Text(
                        '${widget.copy('routes.protocol.${candidate.clientProtocol}')} · ${candidate.clientOrigin.host}',
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      );
                    },
                    items: [
                      for (final candidate in targets)
                        DropdownMenuItem(
                          value: candidate.key,
                          child: SizedBox(
                            key: Key(
                              'environment-client-plan-option-${candidate.clientProtocol}-${candidate.clientOrigin}',
                            ),
                            width: double.infinity,
                            child: Column(
                              mainAxisSize: MainAxisSize.min,
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text(
                                  widget.copy(
                                    'routes.protocol.${candidate.clientProtocol}',
                                  ),
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                ),
                                Text(
                                  candidate.clientOrigin.toString(),
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: monoStyle.copyWith(
                                    color: context.viberColors.textMuted,
                                  ),
                                ),
                              ],
                            ),
                          ),
                        ),
                    ],
                    onChanged: !widget.enabled || targets.isEmpty
                        ? null
                        : (value) {
                            setState(() {
                              _targetKey = value ?? '';
                              _destinationKind =
                                  targets
                                      .where(
                                        (candidate) => candidate.key == value,
                                      )
                                      .firstOrNull
                                      ?.destinationKind ??
                                  'original';
                              _endpointId = '';
                              _accountId = '';
                              _pending = true;
                            });
                            field.didChange('');
                          },
                  ),
                );
                final destinationField = CompactLabeledControl(
                  label: widget.copy('environment.destination.label'),
                  detail: originalDestination
                      ? widget.copy('environment.destination.original.detail')
                      : widget.copy('environment.destination.upstream.detail'),
                  child: SizedBox(
                    width: double.infinity,
                    child: SegmentedButton<String>(
                      key: const Key('environment-destination-kind'),
                      segments: [
                        ButtonSegment(
                          value: 'original',
                          icon: const Icon(Icons.language, size: 13),
                          label: Text(
                            widget.copy(
                              'environment.destination.original.short',
                            ),
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                        ButtonSegment(
                          value: 'upstream',
                          icon: const Icon(Icons.alt_route, size: 13),
                          label: Text(
                            widget.copy(
                              'environment.destination.upstream.short',
                            ),
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ],
                      selected: {destinationKind},
                      showSelectedIcon: false,
                      onSelectionChanged:
                          !widget.enabled || effectiveTarget == null
                          ? null
                          : (selection) {
                              setState(() {
                                _destinationKind = selection.single;
                                _endpointId = '';
                                _accountId = '';
                                _pending = true;
                              });
                              field.didChange(effectiveTarget.key);
                            },
                    ),
                  ),
                );
                final endpointField = CompactLabeledControl(
                  label: widget.copy('environment.endpoint.add'),
                  child: CompactSelectField<String>(
                    key: const Key('environment-endpoint-catalog'),
                    initialValue:
                        available.any((value) => value.id == _endpointId)
                        ? _endpointId
                        : null,
                    isExpanded: true,
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
                              _accountId = accounts.firstOrNull?.id ?? '';
                              _pending = true;
                            });
                            field.didChange(endpoint.id);
                          },
                  ),
                );
                final accountField = CompactLabeledControl(
                  label: widget.copy('environment.account'),
                  detail: selected != null && owned.isEmpty
                      ? widget.copy('environment.endpoint.account_required')
                      : null,
                  child: CompactSelectField<String>(
                    key: Key(
                      'environment-endpoint-account-${selected?.id ?? 'none'}',
                    ),
                    initialValue: _accountId.isEmpty ? null : _accountId,
                    isExpanded: true,
                    items: [
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
                        : (value) {
                            setState(() {
                              _accountId = value ?? '';
                              _pending = true;
                            });
                            field.didChange(_endpointId);
                          },
                  ),
                );
                final add = OutlinedButton.icon(
                  key: Key(
                    originalDestination
                        ? 'environment-use-original'
                        : 'environment-add-endpoint',
                  ),
                  onPressed: !widget.enabled || !canApply ? null : _apply,
                  icon: Icon(
                    originalDestination ? Icons.language : Icons.add,
                    size: 13,
                  ),
                  label: Text(
                    widget.copy(
                      originalDestination
                          ? 'environment.destination.original.action'
                          : 'environment.endpoint.add_action',
                    ),
                  ),
                );
                if (compact) {
                  return Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      targetField,
                      const SizedBox(height: 7),
                      destinationField,
                      if (!originalDestination) ...[
                        const SizedBox(height: 7),
                        endpointField,
                      ],
                      if (!originalDestination && selected != null) ...[
                        const SizedBox(height: 7),
                        accountField,
                      ],
                      const SizedBox(height: 7),
                      Align(alignment: Alignment.centerRight, child: add),
                    ],
                  );
                }
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Expanded(flex: 3, child: targetField),
                        const SizedBox(width: 8),
                        Expanded(flex: 2, child: destinationField),
                      ],
                    ),
                    if (!originalDestination) ...[
                      const SizedBox(height: 7),
                      Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Expanded(flex: 4, child: endpointField),
                          if (selected != null) ...[
                            const SizedBox(width: 8),
                            Expanded(flex: 3, child: accountField),
                          ],
                          const SizedBox(width: 8),
                          Padding(
                            padding: const EdgeInsets.only(top: 18),
                            child: add,
                          ),
                        ],
                      ),
                    ] else ...[
                      const SizedBox(height: 7),
                      Align(alignment: Alignment.centerRight, child: add),
                    ],
                  ],
                );
              },
            ),
          ),
          if (field.errorText != null) ...[
            const SizedBox(height: 5),
            Row(
              key: const Key('environment-endpoint-pending-error'),
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(
                  Icons.error_outline,
                  size: 13,
                  color: Theme.of(context).colorScheme.error,
                ),
                const SizedBox(width: 5),
                Expanded(
                  child: Text(
                    field.errorText!,
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
              ],
            ),
          ],
        ],
      ),
    );
  }

  void _apply() {
    final targets = _clientPlanTargets(widget.current);
    final existingTargets = targets.where((target) => target.exists).toList();
    final target =
        targets.where((candidate) => candidate.key == _targetKey).firstOrNull ??
        (existingTargets.length == 1 ? existingTargets.single : null);
    if (target == null) return;
    final destinationKind = _targetKey.isEmpty
        ? target.destinationKind ?? _destinationKind
        : _destinationKind;
    if (destinationKind == 'original') {
      widget.onOriginal(
        target.clientEndpointId,
        target.protocolPlanId,
        target.clientOrigin,
        target.clientProtocol,
      );
    } else {
      final endpoint = widget.endpoints.firstWhere(
        (candidate) => candidate.id == _endpointId,
      );
      final account = widget.accounts.firstWhere(
        (candidate) => candidate.id == _accountId,
      );
      widget.onAdd(
        target.clientEndpointId,
        target.protocolPlanId,
        target.clientOrigin,
        target.clientProtocol,
        endpoint,
        account,
      );
    }
    setState(() {
      _endpointId = '';
      _accountId = '';
      _pending = false;
    });
    _pendingFieldKey.currentState?.reset();
  }
}

final class _EnvironmentEndpointEditor extends StatelessWidget {
  const _EnvironmentEndpointEditor({
    required this.controller,
    required this.endpoint,
    required this.endpoints,
    required this.accounts,
    required this.copy,
    required this.enabled,
    required this.initiallyExpanded,
    required this.onRemove,
    required this.onAccountChanged,
    required this.onModelChanged,
  });

  final WorkbenchController controller;
  final EnvironmentClientEndpoint endpoint;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final bool enabled;
  final bool initiallyExpanded;
  final VoidCallback onRemove;
  final _RouteAccountChanged onAccountChanged;
  final _RouteModelChanged onModelChanged;

  @override
  Widget build(BuildContext context) {
    final routeCount = endpoint.protocolPlans.fold<int>(
      0,
      (count, plan) => count + plan.routes.length,
    );
    return Container(
      key: Key('environment-client-endpoint-${endpoint.id}'),
      margin: const EdgeInsets.only(bottom: 8),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Theme(
        data: Theme.of(context).copyWith(dividerColor: Colors.transparent),
        child: ExpansionTile(
          key: PageStorageKey('environment-endpoint-expansion-${endpoint.id}'),
          initiallyExpanded: initiallyExpanded,
          maintainState: true,
          dense: true,
          tilePadding: const EdgeInsets.fromLTRB(9, 1, 5, 1),
          childrenPadding: EdgeInsets.zero,
          leading: Icon(
            Icons.input,
            size: 14,
            color: context.viberColors.route,
          ),
          title: Row(
            children: [
              Expanded(
                child: Text(
                  endpoint.clientOrigin.toString(),
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              Tooltip(
                message: endpoint.id,
                child: Icon(
                  Icons.info_outline,
                  size: 13,
                  color: context.viberColors.textFaint,
                  semanticLabel: copy('common.technical_details'),
                ),
              ),
              IconButton(
                key: Key('environment-remove-endpoint-${endpoint.id}'),
                onPressed: enabled ? onRemove : null,
                tooltip: copy('environment.endpoint.remove'),
                icon: const Icon(Icons.close, size: 14),
              ),
            ],
          ),
          subtitle: Text(
            routeCount == 0
                ? copy('environment.destination.original.detail')
                : copy.format('environment.endpoint.routes', {
                    'count': routeCount,
                  }),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          children: [
            const Divider(height: 1),
            for (final plan in endpoint.protocolPlans)
              if (plan.destination.isOriginal)
                _OriginalDestinationEditorRow(plan: plan, copy: copy)
              else
                for (final route in plan.routes)
                  _RouteAccountEditor(
                    controller: controller,
                    plan: plan,
                    route: route,
                    upstreamEndpoint: endpoints
                        .where((candidate) => candidate.id == route.endpointId)
                        .firstOrNull,
                    accounts: accounts,
                    copy: copy,
                    enabled: enabled,
                    onChanged: (account) =>
                        onAccountChanged(plan, route, account),
                    onModelChanged: (mappings) =>
                        onModelChanged(plan, route, mappings),
                  ),
          ],
        ),
      ),
    );
  }
}

final class _OriginalDestinationEditorRow extends StatelessWidget {
  const _OriginalDestinationEditorRow({required this.plan, required this.copy});

  final EnvironmentProtocolPlan plan;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Container(
    key: Key('environment-original-destination-${plan.id}'),
    width: double.infinity,
    padding: const EdgeInsets.fromLTRB(10, 9, 10, 10),
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(Icons.language, size: 15, color: context.viberColors.verified),
        const SizedBox(width: 8),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                '${_localizedCopy(copy, 'routes.protocol', plan.clientProtocol)} · ${copy('environment.destination.original')}',
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 2),
              Text(
                copy('environment.destination.original.detail'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
      ],
    ),
  );
}

final class _RouteAccountEditor extends StatelessWidget {
  const _RouteAccountEditor({
    required this.controller,
    required this.plan,
    required this.route,
    required this.upstreamEndpoint,
    required this.accounts,
    required this.copy,
    required this.enabled,
    required this.onChanged,
    required this.onModelChanged,
  });

  final WorkbenchController controller;
  final EnvironmentProtocolPlan plan;
  final EnvironmentRoute route;
  final UpstreamEndpoint? upstreamEndpoint;
  final List<ProviderAccount> accounts;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<ProviderAccount> onChanged;
  final ValueChanged<List<EnvironmentModelMapping>> onModelChanged;

  @override
  Widget build(BuildContext context) {
    final owned =
        accounts
            .where((account) => account.upstreamEndpointId == route.endpointId)
            .toList(growable: false)
          ..sort(
            (left, right) => left.displayName.compareTo(right.displayName),
          );
    final currentId = route.accountPolicy.preferredAccountId;
    final currentItemExists = owned.any((account) => account.id == currentId);
    final selectable = owned.where((account) => account.usable).length;
    final mappings = route.modelPolicy.mappings;
    final modelLabel = mappings.isEmpty
        ? copy('environment.model.passthrough')
        : copy.format('environment.model.mapping_count', {
            'count': mappings.length,
          });
    return Padding(
      padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 470;
          final authority = _RouteEditorAuthority(
            route: route,
            endpoint: upstreamEndpoint,
          );
          final selector = CompactLabeledControl(
            label: copy('environment.account'),
            detail: selectable == 0
                ? copy('environment.account.none')
                : copy('environment.account.owner'),
            child: CompactSelectField<String>(
              key: Key('environment-route-account-${route.id}-$currentId'),
              initialValue: currentId.isEmpty ? null : currentId,
              isExpanded: true,
              items: [
                if (!currentItemExists && currentId.isNotEmpty)
                  DropdownMenuItem(
                    value: currentId,
                    enabled: false,
                    child: Text(currentId),
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
              onChanged: !enabled || selectable == 0
                  ? null
                  : (value) {
                      if (value == null) return;
                      final selectedAccount = owned.firstWhere(
                        (account) => account.id == value,
                      );
                      onChanged(selectedAccount);
                      if (selectedAccount.usable) {
                        unawaited(
                          controller
                              .upstreamModels(
                                route.endpointId,
                                accountId: selectedAccount.id,
                              )
                              .then<void>((_) {})
                              .onError((_, _) {}),
                        );
                      }
                    },
            ),
          );
          final modelSelector = CompactLabeledControl(
            label: copy('environment.model.label'),
            detail: mappings.isEmpty
                ? copy('environment.model.passthrough.detail')
                : copy('environment.model.map.detail'),
            child: SizedBox(
              width: double.infinity,
              height: ViberMetrics.controlHeight,
              child: OutlinedButton.icon(
                key: Key('environment-route-model-${route.id}'),
                onPressed: enabled
                    ? () => unawaited(_selectModels(context, mappings))
                    : null,
                icon: Icon(
                  mappings.isEmpty
                      ? Icons.sync_alt_rounded
                      : Icons.model_training_outlined,
                  size: 14,
                ),
                label: Align(
                  alignment: Alignment.centerLeft,
                  child: Text(
                    modelLabel,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
                style: OutlinedButton.styleFrom(
                  alignment: Alignment.centerLeft,
                  padding: const EdgeInsets.symmetric(horizontal: 9),
                ),
              ),
            ),
          );
          if (compact) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                authority,
                const SizedBox(height: 7),
                selector,
                const SizedBox(height: 7),
                modelSelector,
              ],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(flex: 4, child: authority),
              const SizedBox(width: 10),
              Expanded(flex: 4, child: selector),
              const SizedBox(width: 10),
              Expanded(flex: 4, child: modelSelector),
            ],
          );
        },
      ),
    );
  }

  Future<void> _selectModels(
    BuildContext context,
    List<EnvironmentModelMapping> mappings,
  ) async {
    final catalogAccountId = route.accountPolicy.preferredAccountId;
    final catalogAccount = catalogAccountId.isEmpty
        ? null
        : accounts
              .where((account) => account.id == catalogAccountId)
              .firstOrNull;
    final selection = await showDialog<List<EnvironmentModelMapping>>(
      context: context,
      barrierDismissible: true,
      builder: (context) => _ModelMappingsDialog(
        controller: controller,
        clientProtocol: plan.clientProtocol,
        endpoint: upstreamEndpoint,
        endpointId: route.endpointId,
        accountId: catalogAccountId.isEmpty ? null : catalogAccountId,
        account: catalogAccount,
        initialMappings: mappings,
        copy: copy,
      ),
    );
    if (selection != null) onModelChanged(selection);
  }
}

final class _ModelMappingsDialog extends StatefulWidget {
  const _ModelMappingsDialog({
    required this.controller,
    required this.clientProtocol,
    required this.endpoint,
    required this.endpointId,
    required this.accountId,
    required this.account,
    required this.initialMappings,
    required this.copy,
  });

  final WorkbenchController controller;
  final String clientProtocol;
  final UpstreamEndpoint? endpoint;
  final String endpointId;
  final String? accountId;
  final ProviderAccount? account;
  final List<EnvironmentModelMapping> initialMappings;
  final AppCopy copy;

  @override
  State<_ModelMappingsDialog> createState() => _ModelMappingsDialogState();
}

final class _ModelMappingDraft {
  _ModelMappingDraft({required this.requested, required this.upstream});

  String requested;
  String upstream;
}

final class _ModelMappingsDialogState extends State<_ModelMappingsDialog> {
  late final List<_ModelMappingDraft> _drafts;
  ClientModelCatalog? _clientCatalog;
  UpstreamModelCatalog? _upstreamCatalog;
  Object? _clientLoadError;
  Object? _upstreamLoadError;
  bool _clientLoading = false;
  bool _upstreamLoading = false;
  String? _validationError;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _drafts = widget.initialMappings
        .map(
          (mapping) => _ModelMappingDraft(
            requested: mapping.requestedModel,
            upstream: mapping.upstreamModel,
          ),
        )
        .toList();
    if (_drafts.isEmpty) {
      _drafts.add(_ModelMappingDraft(requested: '', upstream: ''));
    }
    _clientCatalog = widget.controller.clientModelCatalog(
      widget.clientProtocol,
    );
    final accountId = widget.accountId;
    if (accountId != null) {
      _upstreamCatalog = widget.controller.upstreamModelCatalog(
        widget.endpointId,
        accountId,
      );
    }
    unawaited(_loadClient());
    if (accountId != null) unawaited(_loadUpstream());
  }

  Future<void> _loadClient({bool refresh = false}) async {
    setState(() {
      _clientLoading = true;
      _clientLoadError = null;
    });
    try {
      final catalog = await widget.controller.clientModels(
        widget.clientProtocol,
        refresh: refresh,
      );
      if (!mounted) return;
      setState(() => _clientCatalog = catalog);
    } catch (error) {
      if (!mounted) return;
      setState(() => _clientLoadError = error);
    } finally {
      if (mounted) setState(() => _clientLoading = false);
    }
  }

  Future<void> _loadUpstream({bool refresh = false}) async {
    final accountId = widget.accountId;
    if (accountId == null) return;
    setState(() {
      _upstreamLoading = true;
      _upstreamLoadError = null;
    });
    try {
      final catalog = await widget.controller.upstreamModels(
        widget.endpointId,
        accountId: accountId,
        refresh: refresh,
      );
      if (!mounted) return;
      setState(() => _upstreamCatalog = catalog);
    } catch (error) {
      if (!mounted) return;
      setState(() => _upstreamLoadError = error);
    } finally {
      if (mounted) setState(() => _upstreamLoading = false);
    }
  }

  bool _validModelId(String value) {
    if (value.isEmpty || utf8.encode(value).length > 256) {
      return false;
    }
    return !RegExp(r'[\u0000-\u001f\u007f-\u009f\ufeff]').hasMatch(value);
  }

  String _loadErrorMessage(Object? error) {
    if (error is ControlProblem) {
      if (error.reasonCode == 'model_catalog_timeout') {
        return copy('environment.model.load_timeout');
      }
      if (error.reasonCode == 'model_catalog_authentication_rejected') {
        return copy('environment.model.authentication_rejected');
      }
      return copy.format('environment.model.load_problem', {
        'status': error.status,
        'code': error.reasonCode,
      });
    }
    return copy('environment.model.load_error');
  }

  void _save() {
    final mappings = <EnvironmentModelMapping>[];
    final requested = <String>{};
    for (final draft in _drafts) {
      final left = draft.requested;
      final right = draft.upstream;
      if (left.isEmpty && right.isEmpty) continue;
      if (!_validModelId(left) || !_validModelId(right)) {
        setState(
          () => _validationError = copy('environment.model.mapping_invalid'),
        );
        return;
      }
      if (!requested.add(left)) {
        setState(
          () => _validationError = copy('environment.model.mapping_duplicate'),
        );
        return;
      }
      mappings.add(
        EnvironmentModelMapping(requestedModel: left, upstreamModel: right),
      );
    }
    mappings.sort(
      (left, right) => left.requestedModel.compareTo(right.requestedModel),
    );
    Navigator.of(
      context,
    ).pop(List<EnvironmentModelMapping>.unmodifiable(mappings));
  }

  @override
  Widget build(BuildContext context) {
    final endpointName = widget.endpoint?.displayName ?? widget.endpointId;
    final endpointModelsUrl = widget.endpoint == null
        ? widget.endpointId
        : _endpointModelsUrl(widget.endpoint!.origin);
    final accountAuthority = widget.account == null
        ? copy('environment.model.account_missing_authority')
        : copy.format('environment.model.account_authority', {
            'account': widget.account!.displayName,
            'kind': copy('routes.account.kind.${widget.account!.kind}'),
            'transport': copy(
              'routes.account.transport.${widget.account!.kind}',
            ),
          });
    final requestModels = (_clientCatalog?.models ?? const <ClientModel>[])
        .map(
          (model) => _CatalogModelChoice(
            id: model.id,
            label: model.displayName.isEmpty ? model.id : model.displayName,
            detail: model.canonicalId,
          ),
        )
        .toList(growable: false);
    final upstreamModels = (_upstreamCatalog?.models ?? const <UpstreamModel>[])
        .map(
          (model) => _CatalogModelChoice(
            id: model.id,
            label: model.displayName.isEmpty ? model.id : model.displayName,
            detail: model.ownedBy,
          ),
        )
        .toList(growable: false);
    final maximumHeight = math.min(
      660.0,
      MediaQuery.sizeOf(context).height - 48,
    );

    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      child: ConstrainedBox(
        key: const Key('environment-model-selector'),
        constraints: BoxConstraints(maxWidth: 760, maxHeight: maximumHeight),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(18, 16, 18, 14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          copy('environment.model.dialog.title'),
                          style: Theme.of(context).textTheme.titleMedium,
                        ),
                        const SizedBox(height: 2),
                        Text(
                          copy.format('environment.model.dialog.scope', {
                            'endpoint': endpointName,
                          }),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      ],
                    ),
                  ),
                  IconButton(
                    tooltip: copy('common.dismiss'),
                    onPressed: () => Navigator.of(context).pop(),
                    icon: const Icon(Icons.close, size: 16),
                  ),
                ],
              ),
              const SizedBox(height: 8),
              LayoutBuilder(
                builder: (context, constraints) {
                  final requestSource = _ModelCatalogSource(
                    key: const Key('environment-model-request-source'),
                    marker: 'A',
                    title: copy('environment.model.request_catalog'),
                    subtitle: 'models.dev · ${widget.clientProtocol}',
                    authority: copy('environment.model.request_authority'),
                    count: _clientCatalog?.models.length,
                    loading: _clientLoading,
                    failed: _clientLoadError != null,
                    onRefresh: () => unawaited(_loadClient(refresh: true)),
                    refreshLabel: copy('environment.model.refresh_client'),
                  );
                  final upstreamSource = _ModelCatalogSource(
                    key: const Key('environment-model-upstream-source'),
                    marker: 'B',
                    title: copy('environment.model.upstream_catalog'),
                    subtitle: endpointModelsUrl,
                    authority: accountAuthority,
                    count: _upstreamCatalog?.models.length,
                    loading: _upstreamLoading,
                    failed:
                        widget.accountId == null || _upstreamLoadError != null,
                    onRefresh: widget.accountId == null
                        ? null
                        : () => unawaited(_loadUpstream(refresh: true)),
                    refreshLabel: copy('environment.model.refresh_upstream'),
                  );
                  if (constraints.maxWidth < 520) {
                    return Column(
                      children: [
                        requestSource,
                        const SizedBox(height: 6),
                        upstreamSource,
                      ],
                    );
                  }
                  return Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Expanded(child: requestSource),
                      const SizedBox(width: 8),
                      Expanded(child: upstreamSource),
                    ],
                  );
                },
              ),
              if (_clientLoadError != null || _upstreamLoadError != null) ...[
                const SizedBox(height: 6),
                if (_clientLoadError != null)
                  _ModelLoadMessage(
                    label: copy('environment.model.client_load_error'),
                    detail: _loadErrorMessage(_clientLoadError),
                  ),
                if (_upstreamLoadError != null)
                  _ModelLoadMessage(
                    label: copy('environment.model.upstream_load_error'),
                    detail: _loadErrorMessage(_upstreamLoadError),
                  ),
                if (_upstreamLoadError != null && widget.account != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 3),
                    child: Text(
                      '${copy('environment.model.auth_hint.${widget.account!.kind}')} ${copy('environment.model.manual_available')}',
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ),
              ] else if (widget.accountId == null) ...[
                const SizedBox(height: 6),
                _ModelLoadMessage(
                  label: copy('environment.model.account_required'),
                  detail: copy('environment.model.manual_available'),
                ),
              ],
              const SizedBox(height: 8),
              Expanded(
                child: DecoratedBox(
                  decoration: BoxDecoration(
                    border: Border.all(color: context.viberColors.divider),
                    borderRadius: ViberMetrics.surfaceRadius,
                  ),
                  child: ListView(
                    padding: const EdgeInsets.all(8),
                    children: [
                      for (final entry in _drafts.indexed) ...[
                        _ModelMappingEditor(
                          key: ObjectKey(entry.$2),
                          index: entry.$1,
                          draft: entry.$2,
                          requestModels: requestModels,
                          upstreamModels: upstreamModels,
                          canRemove:
                              _drafts.length > 1 ||
                              entry.$2.requested.isNotEmpty ||
                              entry.$2.upstream.isNotEmpty,
                          onChanged: () {
                            if (_validationError != null) {
                              setState(() => _validationError = null);
                            }
                          },
                          onRemove: () {
                            setState(() {
                              _drafts.remove(entry.$2);
                              if (_drafts.isEmpty) {
                                _drafts.add(
                                  _ModelMappingDraft(
                                    requested: '',
                                    upstream: '',
                                  ),
                                );
                              }
                              _validationError = null;
                            });
                          },
                          copy: copy,
                        ),
                        if (entry.$1 != _drafts.length - 1)
                          const SizedBox(height: 7),
                      ],
                      const SizedBox(height: 7),
                      Align(
                        alignment: Alignment.centerLeft,
                        child: TextButton.icon(
                          key: const Key('environment-model-add'),
                          onPressed: () {
                            setState(() {
                              _drafts.add(
                                _ModelMappingDraft(requested: '', upstream: ''),
                              );
                              _validationError = null;
                            });
                          },
                          icon: const Icon(Icons.add, size: 14),
                          label: Text(copy('environment.model.add')),
                        ),
                      ),
                    ],
                  ),
                ),
              ),
              if (_validationError != null) ...[
                const SizedBox(height: 6),
                InlineNotice(message: _validationError!, error: true),
              ],
              const SizedBox(height: 9),
              Row(
                children: [
                  TextButton.icon(
                    key: const Key('environment-model-passthrough'),
                    onPressed: () => Navigator.of(
                      context,
                    ).pop(const <EnvironmentModelMapping>[]),
                    icon: const Icon(Icons.sync_alt_rounded, size: 14),
                    label: Text(copy('environment.model.clear')),
                  ),
                  const Spacer(),
                  TextButton(
                    onPressed: () => Navigator.of(context).pop(),
                    child: Text(copy('common.cancel')),
                  ),
                  const SizedBox(width: 6),
                  FilledButton(
                    key: const Key('environment-model-save'),
                    onPressed: _save,
                    child: Text(copy('environment.model.save')),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

final class _ModelCatalogSource extends StatelessWidget {
  const _ModelCatalogSource({
    required this.marker,
    required this.title,
    required this.subtitle,
    required this.authority,
    required this.count,
    required this.loading,
    required this.failed,
    required this.onRefresh,
    required this.refreshLabel,
    super.key,
  });

  final String marker;
  final String title;
  final String subtitle;
  final String authority;
  final int? count;
  final bool loading;
  final bool failed;
  final VoidCallback? onRefresh;
  final String refreshLabel;

  @override
  Widget build(BuildContext context) {
    final stateColor = failed
        ? context.viberColors.danger
        : count != null
        ? context.viberColors.verified
        : context.viberColors.textMuted;
    return Container(
      padding: const EdgeInsets.fromLTRB(9, 7, 5, 7),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised.withValues(alpha: 0.45),
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Row(
        children: [
          Container(
            width: 22,
            height: 22,
            alignment: Alignment.center,
            decoration: BoxDecoration(
              color: context.viberColors.route.withValues(alpha: 0.10),
              borderRadius: ViberMetrics.controlRadius,
            ),
            child: Text(
              marker,
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: context.viberColors.route,
                fontWeight: FontWeight.w700,
              ),
            ),
          ),
          const SizedBox(width: 7),
          Expanded(
            child: Tooltip(
              message: subtitle,
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Flexible(
                        child: Text(
                          title,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                      ),
                      const SizedBox(width: 6),
                      if (loading)
                        const SizedBox.square(
                          dimension: 12,
                          child: CircularProgressIndicator(strokeWidth: 1.4),
                        )
                      else
                        Text(
                          count == null ? '—' : '$count',
                          style: Theme.of(
                            context,
                          ).textTheme.labelSmall?.copyWith(color: stateColor),
                        ),
                    ],
                  ),
                  const SizedBox(height: 1),
                  Text(
                    subtitle,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: monoStyle.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                  const SizedBox(height: 1),
                  Text(
                    authority,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.labelSmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
            ),
          ),
          SizedBox(
            width: ViberMetrics.controlHeight,
            height: ViberMetrics.controlHeight,
            child: IconButton(
              tooltip: refreshLabel,
              onPressed: loading ? null : onRefresh,
              padding: EdgeInsets.zero,
              icon: const Icon(Icons.refresh_rounded, size: 15),
            ),
          ),
        ],
      ),
    );
  }
}

final class _ModelLoadMessage extends StatelessWidget {
  const _ModelLoadMessage({required this.label, required this.detail});

  final String label;
  final String detail;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 2),
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(
          Icons.info_outline_rounded,
          size: 13,
          color: context.viberColors.textMuted,
        ),
        const SizedBox(width: 5),
        Expanded(
          child: Text.rich(
            TextSpan(
              children: [
                TextSpan(
                  text: '$label ',
                  style: const TextStyle(fontWeight: FontWeight.w600),
                ),
                TextSpan(text: detail),
              ],
            ),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
      ],
    ),
  );
}

final class _CatalogModelChoice {
  const _CatalogModelChoice({
    required this.id,
    required this.label,
    required this.detail,
  });

  final String id;
  final String label;
  final String detail;
}

final class _ModelMappingEditor extends StatelessWidget {
  const _ModelMappingEditor({
    required this.index,
    required this.draft,
    required this.requestModels,
    required this.upstreamModels,
    required this.canRemove,
    required this.onChanged,
    required this.onRemove,
    required this.copy,
    super.key,
  });

  final int index;
  final _ModelMappingDraft draft;
  final List<_CatalogModelChoice> requestModels;
  final List<_CatalogModelChoice> upstreamModels;
  final bool canRemove;
  final VoidCallback onChanged;
  final VoidCallback onRemove;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final requested = _CatalogModelField(
      fieldKey: Key('environment-model-requested-$index'),
      optionKeyPrefix: 'requested-$index',
      initialValue: draft.requested,
      label: copy('environment.model.requested'),
      hint: copy('environment.model.requested_hint'),
      options: requestModels,
      onChanged: (value) {
        draft.requested = value;
        onChanged();
      },
    );
    final upstream = _CatalogModelField(
      fieldKey: Key('environment-model-upstream-$index'),
      optionKeyPrefix: 'upstream-$index',
      initialValue: draft.upstream,
      label: copy('environment.model.upstream'),
      hint: copy('environment.model.upstream_hint'),
      options: upstreamModels,
      onChanged: (value) {
        draft.upstream = value;
        onChanged();
      },
    );
    return Container(
      padding: const EdgeInsets.fromLTRB(9, 6, 5, 8),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised.withValues(alpha: 0.34),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        children: [
          Row(
            children: [
              Text(
                copy.format('environment.model.mapping', {'index': index + 1}),
                style: Theme.of(context).textTheme.labelSmall,
              ),
              const Spacer(),
              SizedBox(
                width: ViberMetrics.controlHeight,
                height: ViberMetrics.controlHeight,
                child: IconButton(
                  key: Key('environment-model-remove-$index'),
                  tooltip: copy('environment.model.remove'),
                  onPressed: canRemove ? onRemove : null,
                  padding: EdgeInsets.zero,
                  icon: const Icon(Icons.close_rounded, size: 14),
                ),
              ),
            ],
          ),
          LayoutBuilder(
            builder: (context, constraints) {
              if (constraints.maxWidth < 490) {
                return Column(
                  children: [
                    requested,
                    Padding(
                      padding: const EdgeInsets.symmetric(vertical: 4),
                      child: Icon(
                        Icons.arrow_downward_rounded,
                        size: 14,
                        color: context.viberColors.route,
                      ),
                    ),
                    upstream,
                  ],
                );
              }
              return Row(
                children: [
                  Expanded(child: requested),
                  Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 7),
                    child: Icon(
                      Icons.arrow_forward_rounded,
                      size: 15,
                      color: context.viberColors.route,
                    ),
                  ),
                  Expanded(child: upstream),
                ],
              );
            },
          ),
        ],
      ),
    );
  }
}

final class _CatalogModelField extends StatelessWidget {
  const _CatalogModelField({
    required this.fieldKey,
    required this.optionKeyPrefix,
    required this.initialValue,
    required this.label,
    required this.hint,
    required this.options,
    required this.onChanged,
  });

  final Key fieldKey;
  final String optionKeyPrefix;
  final String initialValue;
  final String label;
  final String hint;
  final List<_CatalogModelChoice> options;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => Autocomplete<_CatalogModelChoice>(
    initialValue: TextEditingValue(text: initialValue),
    displayStringForOption: (option) => option.id,
    optionsBuilder: (value) {
      final query = value.text.trim().toLowerCase();
      return options
          .where(
            (option) =>
                query.isEmpty ||
                option.id.toLowerCase().contains(query) ||
                option.label.toLowerCase().contains(query) ||
                option.detail.toLowerCase().contains(query),
          )
          .take(80);
    },
    onSelected: (option) => onChanged(option.id),
    fieldViewBuilder: (context, controller, focusNode, onSubmitted) =>
        TextField(
          key: fieldKey,
          controller: controller,
          focusNode: focusNode,
          onChanged: onChanged,
          onSubmitted: (_) => onSubmitted(),
          style: monoStyle,
          decoration: InputDecoration(labelText: label, hintText: hint),
        ),
    optionsViewBuilder: (context, onSelected, visibleOptions) {
      final values = visibleOptions.toList(growable: false);
      return Align(
        alignment: Alignment.topLeft,
        child: Material(
          color: context.viberColors.panel,
          elevation: 8,
          shape: RoundedRectangleBorder(
            side: BorderSide(color: context.viberColors.divider),
            borderRadius: ViberMetrics.surfaceRadius,
          ),
          child: ConstrainedBox(
            constraints: BoxConstraints(
              maxWidth: math.min(360.0, MediaQuery.sizeOf(context).width - 48),
              maxHeight: 230,
            ),
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(vertical: 3),
              shrinkWrap: true,
              itemCount: values.length,
              itemBuilder: (context, index) {
                final option = values[index];
                return InkWell(
                  key: Key(
                    'environment-model-$optionKeyPrefix-option-${option.id}',
                  ),
                  onTap: () => onSelected(option),
                  child: Padding(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 9,
                      vertical: 6,
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          option.label,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                        if (option.id != option.label)
                          Text(
                            option.id,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: monoStyle.copyWith(
                              color: context.viberColors.textMuted,
                            ),
                          ),
                      ],
                    ),
                  ),
                );
              },
            ),
          ),
        ),
      );
    },
  );
}

String _endpointModelsUrl(Uri origin) {
  var path = origin.path;
  while (path.length > 1 && path.endsWith('/')) {
    path = path.substring(0, path.length - 1);
  }
  final modelsPath = path.endsWith('/v1')
      ? '$path/models'
      : '${path == '/' ? '' : path}/v1/models';
  return origin
      .replace(path: modelsPath, query: null, fragment: null)
      .toString();
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
            Icon(
              Icons.arrow_forward,
              size: 13,
              color: context.viberColors.route,
            ),
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
          style: monoStyle.copyWith(color: context.viberColors.textFaint),
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
    final captures = impact.continuingCaptures;
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
                label: copy('environment.impact.future_only'),
                color: context.viberColors.route,
                icon: Icons.schedule,
              ),
            ],
          ),
          const SizedBox(height: 7),
          Text(
            copy('environment.impact.description'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 13),
          _EditorSectionLabel(
            label: copy.format('environment.impact.continuing', {
              'count': captures.length,
            }),
          ),
          const SizedBox(height: 5),
          if (captures.isEmpty)
            Text(
              copy('environment.impact.none'),
              style: Theme.of(context).textTheme.bodySmall,
            )
          else
            Container(
              decoration: BoxDecoration(
                border: Border.all(color: context.viberColors.divider),
                borderRadius: ViberMetrics.surfaceRadius,
              ),
              child: Column(
                children: [
                  for (final capture in captures.take(12))
                    Container(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 9,
                        vertical: 6,
                      ),
                      decoration: BoxDecoration(
                        border: Border(
                          bottom: BorderSide(
                            color: context.viberColors.dividerSoft,
                          ),
                        ),
                      ),
                      child: Row(
                        children: [
                          Icon(
                            capture.captureKind == 'managed_run'
                                ? Icons.terminal
                                : Icons.link,
                            size: 13,
                            color: context.viberColors.textMuted,
                          ),
                          const SizedBox(width: 7),
                          Expanded(
                            child: Text(
                              capture.captureId,
                              overflow: TextOverflow.ellipsis,
                              style: monoStyle.copyWith(
                                color: context.viberColors.text,
                              ),
                            ),
                          ),
                          Text(
                            copy('environment.impact.unchanged'),
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ],
                      ),
                    ),
                  if (captures.length > 12)
                    Padding(
                      padding: const EdgeInsets.all(7),
                      child: Text(
                        copy.format('environment.impact.more', {
                          'count': captures.length - 12,
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

String _localizedCopy(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  final localized = copy(key);
  return localized == key ? value : localized;
}

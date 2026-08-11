import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'conversation_timeline.dart';
import 'workbench_controller.dart';

final class CapturesView extends StatefulWidget {
  const CapturesView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<CapturesView> createState() => _CapturesViewState();
}

final class _CapturesViewState extends State<CapturesView> {
  String _filter = '';
  bool _narrowDetail = false;
  bool _confirmRevoke = false;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final narrow = constraints.maxWidth < 760;
        final master = _CaptureMaster(
          controller: widget.controller,
          copy: widget.copy,
          filter: _filter,
          onFilter: (value) => setState(() => _filter = value),
          onCreateManual: () => _openCreateManualCapture(context),
          onSelect: (key) {
            unawaited(widget.controller.selectCapture(key));
            if (narrow) setState(() => _narrowDetail = true);
          },
        );
        final detail = _CaptureDetail(
          controller: widget.controller,
          copy: widget.copy,
          showBack: narrow,
          confirmRevoke: _confirmRevoke,
          onBack: () => setState(() {
            _narrowDetail = false;
            _confirmRevoke = false;
          }),
          onConfirmRevoke: (value) => setState(() => _confirmRevoke = value),
          onRotateManual: () => _openRotateManualCapture(context),
        );
        if (narrow) {
          return AnimatedSwitcher(
            duration: const Duration(milliseconds: 140),
            child: _narrowDetail && widget.controller.selectedCapture != null
                ? KeyedSubtree(
                    key: const ValueKey('capture-detail'),
                    child: detail,
                  )
                : KeyedSubtree(
                    key: const ValueKey('capture-master'),
                    child: master,
                  ),
          );
        }
        return Row(
          children: [
            SizedBox(width: 316, child: master),
            const VerticalDivider(width: 1),
            Expanded(child: detail),
          ],
        );
      },
    );
  }

  void _openCreateManualCapture(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _ManualCaptureCreateDialog(
          controller: widget.controller,
          copy: widget.copy,
        ),
      ),
    );
  }

  void _openRotateManualCapture(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _ManualCaptureRotateDialog(
          controller: widget.controller,
          copy: widget.copy,
        ),
      ),
    );
  }
}

final class _CaptureMaster extends StatelessWidget {
  const _CaptureMaster({
    required this.controller,
    required this.copy,
    required this.filter,
    required this.onFilter,
    required this.onCreateManual,
    required this.onSelect,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final String filter;
  final ValueChanged<String> onFilter;
  final VoidCallback onCreateManual;
  final ValueChanged<String> onSelect;

  @override
  Widget build(BuildContext context) {
    final query = filter.trim().toLowerCase();
    bool matches(CaptureRecord capture) {
      if (query.isEmpty) return true;
      final managed = capture.managedRun;
      return [
        capture.displayName,
        capture.id,
        managed?.workspaceLabel ?? '',
        managed?.cwd ?? '',
      ].any((value) => value.toLowerCase().contains(query));
    }

    final running = controller.runningCaptures
        .where(matches)
        .toList(growable: false);
    final history = controller.historicalCaptures
        .where(matches)
        .toList(growable: false);
    return ColoredBox(
      color: ViberColors.panel,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 10, 7, 7),
            child: Row(
              children: [
                Expanded(
                  child: Semantics(
                    textField: true,
                    label: copy('capture.search'),
                    child: TextField(
                      onChanged: onFilter,
                      decoration: InputDecoration(
                        hintText: copy('capture.search'),
                        prefixIcon: const Icon(Icons.search, size: 15),
                        prefixIconConstraints: const BoxConstraints.tightFor(
                          width: 31,
                        ),
                        suffixIcon: filter.isEmpty
                            ? null
                            : IconButton(
                                onPressed: () => onFilter(''),
                                icon: const Icon(Icons.close, size: 14),
                                tooltip: 'Clear filter',
                              ),
                      ),
                    ),
                  ),
                ),
                const SizedBox(width: 5),
                IconButton(
                  key: const Key('manual-capture-create'),
                  onPressed:
                      controller.data?.environments.any(
                            (environment) => environment.state == 'active',
                          ) ??
                          false
                      ? onCreateManual
                      : null,
                  tooltip: copy('capture.manual.create'),
                  icon: const Icon(Icons.add_link, size: 17),
                ),
              ],
            ),
          ),
          Expanded(
            child: running.isEmpty && history.isEmpty
                ? CenteredMessage(
                    icon: Icons.filter_alt_off,
                    title: copy('capture.empty'),
                  )
                : ListView(
                    children: [
                      SectionLabel(
                        label: copy('capture.running'),
                        count: running.length,
                      ),
                      for (final capture in running)
                        _CaptureRow(
                          capture: capture,
                          selected:
                              capture.key == controller.selectedCaptureKey,
                          onPressed: () => onSelect(capture.key),
                        ),
                      SectionLabel(
                        label: copy('capture.history'),
                        count: history.length,
                      ),
                      for (final capture in history)
                        _CaptureRow(
                          capture: capture,
                          selected:
                              capture.key == controller.selectedCaptureKey,
                          onPressed: () => onSelect(capture.key),
                        ),
                      const SizedBox(height: 12),
                    ],
                  ),
          ),
        ],
      ),
    );
  }
}

final class _CaptureRow extends StatelessWidget {
  const _CaptureRow({
    required this.capture,
    required this.selected,
    required this.onPressed,
  });

  final CaptureRecord capture;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final managed = capture.managedRun;
    final subtitle =
        managed?.workspaceLabel ??
        managed?.cwd ??
        capture.manualCapture?.clientClass ??
        capture.id;
    final activity = _relativeTime(capture.updatedAt);
    return Semantics(
      key: Key('capture-row-${capture.key}'),
      selected: selected,
      button: true,
      label: '${capture.displayName}, $subtitle, ${capture.state}',
      child: Material(
        color: selected ? ViberColors.selection : Colors.transparent,
        child: InkWell(
          onTap: onPressed,
          canRequestFocus: true,
          focusColor: ViberColors.focus.withValues(alpha: 0.15),
          child: Container(
            height: 52,
            decoration: BoxDecoration(
              border: Border(
                left: BorderSide(
                  width: 2,
                  color: selected ? ViberColors.route : Colors.transparent,
                ),
                bottom: const BorderSide(color: ViberColors.dividerSoft),
              ),
            ),
            padding: const EdgeInsets.fromLTRB(9, 6, 8, 6),
            child: Row(
              children: [
                _CaptureGlyph(capture: capture),
                const SizedBox(width: 8),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              capture.displayName,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          Text(
                            activity,
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ],
                      ),
                      const SizedBox(height: 2),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              subtitle,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                          ),
                          const SizedBox(width: 6),
                          Container(
                            width: 6,
                            height: 6,
                            decoration: BoxDecoration(
                              shape: BoxShape.circle,
                              color: capture.running
                                  ? ViberColors.verified
                                  : ViberColors.textFaint,
                            ),
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _CaptureGlyph extends StatelessWidget {
  const _CaptureGlyph({required this.capture});

  final CaptureRecord capture;

  @override
  Widget build(BuildContext context) {
    final color = capture.isManual ? ViberColors.warning : ViberColors.route;
    return Container(
      width: 28,
      height: 28,
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.1),
        border: Border.all(color: color.withValues(alpha: 0.25)),
        borderRadius: BorderRadius.circular(5),
      ),
      child: Icon(
        capture.isManual ? Icons.link : Icons.terminal,
        size: 15,
        color: color,
      ),
    );
  }
}

final class _CaptureDetail extends StatelessWidget {
  const _CaptureDetail({
    required this.controller,
    required this.copy,
    required this.showBack,
    required this.confirmRevoke,
    required this.onBack,
    required this.onConfirmRevoke,
    required this.onRotateManual,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool showBack;
  final bool confirmRevoke;
  final VoidCallback onBack;
  final ValueChanged<bool> onConfirmRevoke;
  final VoidCallback onRotateManual;

  @override
  Widget build(BuildContext context) {
    final capture = controller.selectedCapture;
    if (capture == null) {
      return const CenteredMessage(
        icon: Icons.adjust,
        title: 'Select a Capture to inspect its evidence.',
      );
    }
    final assignment = controller.selectedAssignment;
    final environment = controller.data?.environments
        .where((candidate) => candidate.id == assignment?.environmentId)
        .firstOrNull;
    final route = _routeForCapture(capture, environment);
    final endpoint = controller.data?.endpoints
        .where((candidate) => candidate.id == route?.endpointId)
        .firstOrNull;
    final account = controller.data?.accounts
        .where(
          (candidate) =>
              candidate.id == route?.accountPolicy.preferredAccountId,
        )
        .firstOrNull;
    final accountMatches =
        account == null || account.upstreamEndpointId == endpoint?.id;
    final notice = controller.operationNotice;
    return ColoredBox(
      color: ViberColors.canvas,
      child: Column(
        children: [
          if (controller.errorMessage case final message?)
            InlineNotice(message: message, error: true),
          if (notice case final value?)
            InlineNotice(
              message: copy('notice.$value'),
              onDismiss: controller.clearNotice,
            ),
          if (controller.workspaceDefaultError case final message?)
            InlineNotice(
              message: message,
              error: true,
              onDismiss: controller.clearWorkspaceDefaultNotice,
            )
          else if (controller.workspaceDefaultNotice case final value?)
            InlineNotice(
              message: copy('notice.$value'),
              onDismiss: controller.clearWorkspaceDefaultNotice,
            ),
          _CaptureContext(
            capture: capture,
            assignment: assignment,
            environments: controller.data?.environments ?? const [],
            workspaceDefault: controller.selectedWorkspaceDefault,
            copy: copy,
            showBack: showBack,
            confirmRevoke: confirmRevoke,
            mutating: controller.mutating,
            workspaceDefaultLoading: controller.workspaceDefaultLoading,
            workspaceDefaultMutating: controller.workspaceDefaultMutating,
            onBack: onBack,
            onEnvironment: (value) =>
                unawaited(controller.switchEnvironment(value)),
            onWorkspaceDefault: (value) =>
                unawaited(controller.setSelectedWorkspaceDefault(value)),
            onConfirmRevoke: onConfirmRevoke,
            onRotate: onRotateManual,
            onRevoke: () async {
              final success = await controller.revokeSelectedManualCapture();
              if (success) onConfirmRevoke(false);
            },
          ),
          Container(
            decoration: const BoxDecoration(
              color: ViberColors.panel,
              border: Border(
                top: BorderSide(color: ViberColors.dividerSoft),
                bottom: BorderSide(color: ViberColors.divider),
              ),
            ),
            child: FlowSpine(
              nodes: [
                FlowNode(
                  kind: copy('flow.capture'),
                  label: capture.displayName,
                ),
                FlowNode(
                  kind: copy('flow.environment'),
                  label: environment?.name ?? assignment?.environmentId ?? '—',
                ),
                if (endpoint != null)
                  FlowNode(
                    kind: copy('flow.endpoint'),
                    label: endpoint.displayName,
                  ),
                if (account != null)
                  FlowNode(
                    kind: copy('flow.account'),
                    label: account.displayName,
                    tone: accountMatches
                        ? ViberColors.verified
                        : ViberColors.danger,
                  ),
              ],
            ),
          ),
          if (!accountMatches)
            InlineNotice(
              message: copy('environment.account.invalid'),
              error: true,
            ),
          Expanded(
            child: controller.detailLoading
                ? const Center(child: CircularProgressIndicator(strokeWidth: 2))
                : EvidenceConversationTimeline(
                    controller: controller,
                    activities: controller.selectedActivities,
                    copy: copy,
                    canLoadEarlier:
                        controller.selectedCapturePage?.nextCursor != null,
                    loadingEarlier: controller.captureActivitiesLoading,
                    exchangeScoped: capture.isManual,
                    onLoadEarlier: () =>
                        unawaited(controller.loadMoreSelectedCapture()),
                  ),
          ),
        ],
      ),
    );
  }

  static EnvironmentRoute? _routeForCapture(
    CaptureRecord capture,
    EnvironmentRecord? environment,
  ) {
    if (environment == null) return null;
    final executable = capture.managedRun?.executableLabel.toLowerCase() ?? '';
    final desired = executable.contains('codex')
        ? 'openai'
        : executable.contains('claude')
        ? 'anthropic'
        : '';
    for (final endpoint in environment.clientEndpoints) {
      for (final plan in endpoint.protocolPlans) {
        if (desired.isNotEmpty && !plan.clientProtocol.contains(desired)) {
          continue;
        }
        return plan.routes
                .where((route) => route.id == plan.defaultRouteId)
                .firstOrNull ??
            plan.routes.firstOrNull;
      }
    }
    return environment.routes.firstOrNull;
  }
}

final class _CaptureContext extends StatelessWidget {
  const _CaptureContext({
    required this.capture,
    required this.assignment,
    required this.environments,
    required this.workspaceDefault,
    required this.copy,
    required this.showBack,
    required this.confirmRevoke,
    required this.mutating,
    required this.workspaceDefaultLoading,
    required this.workspaceDefaultMutating,
    required this.onBack,
    required this.onEnvironment,
    required this.onWorkspaceDefault,
    required this.onConfirmRevoke,
    required this.onRevoke,
    required this.onRotate,
  });

  final CaptureRecord capture;
  final CaptureAssignment? assignment;
  final List<EnvironmentRecord> environments;
  final WorkspaceEnvironmentDefault? workspaceDefault;
  final AppCopy copy;
  final bool showBack;
  final bool confirmRevoke;
  final bool mutating;
  final bool workspaceDefaultLoading;
  final bool workspaceDefaultMutating;
  final VoidCallback onBack;
  final ValueChanged<String> onEnvironment;
  final ValueChanged<String?> onWorkspaceDefault;
  final ValueChanged<bool> onConfirmRevoke;
  final VoidCallback onRevoke;
  final VoidCallback onRotate;

  @override
  Widget build(BuildContext context) {
    final source = capture.isManual
        ? copy('capture.manual')
        : copy('capture.managed');
    final detail = capture.isManual
        ? copy('capture.source.manual')
        : copy('capture.source.managed');
    return Container(
      color: ViberColors.panel,
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 520;
          final canManage =
              capture.isManual && capture.running && !confirmRevoke;
          final revokeButton = OutlinedButton.icon(
            onPressed: mutating ? null : () => onConfirmRevoke(true),
            icon: const Icon(Icons.link_off, size: 14),
            label: Text(copy('capture.revoke')),
            style: OutlinedButton.styleFrom(
              foregroundColor: ViberColors.warning,
            ),
          );
          final manualActions = Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              OutlinedButton.icon(
                key: const Key('manual-capture-rotate'),
                onPressed: mutating ? null : onRotate,
                icon: const Icon(Icons.sync_lock, size: 14),
                label: Text(copy('capture.manual.rotate')),
              ),
              revokeButton,
            ],
          );
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  if (showBack) ...[
                    IconButton(
                      onPressed: onBack,
                      tooltip: copy('common.back'),
                      icon: const Icon(Icons.arrow_back, size: 17),
                      constraints: const BoxConstraints.tightFor(
                        width: 30,
                        height: 30,
                      ),
                      padding: EdgeInsets.zero,
                    ),
                    const SizedBox(width: 4),
                  ],
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Expanded(
                              child: Text(
                                capture.displayName,
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                                style: Theme.of(
                                  context,
                                ).textTheme.headlineSmall,
                              ),
                            ),
                            const SizedBox(width: 8),
                            StatusPill(
                              label: capture.state,
                              color: capture.running
                                  ? ViberColors.verified
                                  : ViberColors.textFaint,
                            ),
                          ],
                        ),
                        const SizedBox(height: 2),
                        Text(
                          '${capture.managedRun?.workspaceLabel ?? capture.id}  ·  $source',
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      ],
                    ),
                  ),
                  if (canManage && !compact) ...[
                    const SizedBox(width: 8),
                    manualActions,
                  ],
                ],
              ),
              if (canManage && compact) ...[
                const SizedBox(height: 7),
                Align(alignment: Alignment.centerLeft, child: manualActions),
              ],
              const SizedBox(height: 7),
              Text(detail, style: Theme.of(context).textTheme.bodySmall),
              const SizedBox(height: 9),
              _EnvironmentScopeControls(
                capture: capture,
                assignment: assignment,
                environments: environments,
                workspaceDefault: workspaceDefault,
                copy: copy,
                captureMutating: mutating,
                workspaceLoading: workspaceDefaultLoading,
                workspaceMutating: workspaceDefaultMutating,
                onCaptureEnvironment: onEnvironment,
                onWorkspaceDefault: onWorkspaceDefault,
              ),
              if (confirmRevoke) ...[
                const SizedBox(height: 9),
                Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(9),
                  decoration: BoxDecoration(
                    color: ViberColors.warning.withValues(alpha: 0.08),
                    border: Border.all(
                      color: ViberColors.warning.withValues(alpha: 0.32),
                    ),
                    borderRadius: BorderRadius.circular(5),
                  ),
                  child: compact
                      ? Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Row(
                              children: [
                                const Icon(
                                  Icons.info_outline,
                                  size: 16,
                                  color: ViberColors.warning,
                                ),
                                const SizedBox(width: 7),
                                Expanded(
                                  child: Text(
                                    copy('capture.revoke.confirm'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                ),
                              ],
                            ),
                            const SizedBox(height: 4),
                            Text(
                              copy('capture.revoke.detail'),
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                            const SizedBox(height: 7),
                            Row(
                              mainAxisAlignment: MainAxisAlignment.end,
                              children: [
                                TextButton(
                                  onPressed: mutating
                                      ? null
                                      : () => onConfirmRevoke(false),
                                  child: Text(copy('common.cancel')),
                                ),
                                const SizedBox(width: 4),
                                FilledButton(
                                  onPressed: mutating ? null : onRevoke,
                                  style: FilledButton.styleFrom(
                                    backgroundColor: ViberColors.danger,
                                  ),
                                  child: Text(copy('capture.revoke.action')),
                                ),
                              ],
                            ),
                          ],
                        )
                      : Row(
                          children: [
                            const Icon(
                              Icons.info_outline,
                              size: 16,
                              color: ViberColors.warning,
                            ),
                            const SizedBox(width: 8),
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    copy('capture.revoke.confirm'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                  Text(
                                    copy('capture.revoke.detail'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.bodySmall,
                                  ),
                                ],
                              ),
                            ),
                            TextButton(
                              onPressed: mutating
                                  ? null
                                  : () => onConfirmRevoke(false),
                              child: Text(copy('common.cancel')),
                            ),
                            const SizedBox(width: 4),
                            FilledButton(
                              onPressed: mutating ? null : onRevoke,
                              style: FilledButton.styleFrom(
                                backgroundColor: ViberColors.danger,
                              ),
                              child: Text(copy('capture.revoke.action')),
                            ),
                          ],
                        ),
                ),
              ],
            ],
          );
        },
      ),
    );
  }
}

final class _EnvironmentScopeControls extends StatelessWidget {
  const _EnvironmentScopeControls({
    required this.capture,
    required this.assignment,
    required this.environments,
    required this.workspaceDefault,
    required this.copy,
    required this.captureMutating,
    required this.workspaceLoading,
    required this.workspaceMutating,
    required this.onCaptureEnvironment,
    required this.onWorkspaceDefault,
  });

  final CaptureRecord capture;
  final CaptureAssignment? assignment;
  final List<EnvironmentRecord> environments;
  final WorkspaceEnvironmentDefault? workspaceDefault;
  final AppCopy copy;
  final bool captureMutating;
  final bool workspaceLoading;
  final bool workspaceMutating;
  final ValueChanged<String> onCaptureEnvironment;
  final ValueChanged<String?> onWorkspaceDefault;

  @override
  Widget build(BuildContext context) {
    final assigned = assignment?.environmentId;
    final captureChoices = environments
        .where(
          (environment) =>
              environment.state == 'active' || environment.id == assigned,
        )
        .toList(growable: false);
    final managed = capture.managedRun;
    final hasWorkspace = managed?.hasWorkspaceIdentity == true;
    final futureChoices = environments
        .where(
          (environment) =>
              !environment.systemOwned &&
              (environment.state == 'active' ||
                  environment.id == workspaceDefault?.environmentId),
        )
        .map((environment) => (id: environment.id, name: environment.name))
        .toList(growable: true);
    if (workspaceDefault != null &&
        !futureChoices.any(
          (environment) => environment.id == workspaceDefault!.environmentId,
        )) {
      futureChoices.add((
        id: workspaceDefault!.environmentId,
        name: workspaceDefault!.environmentName,
      ));
    }

    return Container(
      decoration: const BoxDecoration(
        border: Border.symmetric(
          horizontal: BorderSide(color: ViberColors.dividerSoft),
        ),
      ),
      child: Column(
        children: [
          _EnvironmentScopeRow(
            key: const Key('capture-environment-scope'),
            icon: Icons.adjust,
            tone: ViberColors.route,
            title: copy('capture.environment.current'),
            detail: copy('capture.environment.help'),
            control: DropdownButtonFormField<String>(
              key: ValueKey(
                'capture-environment:${capture.key}:${assignment?.revision ?? 0}',
              ),
              initialValue: assigned,
              isExpanded: true,
              decoration: _scopeDecoration(),
              items: [
                for (final environment in captureChoices)
                  DropdownMenuItem(
                    value: environment.id,
                    child: Text(
                      environment.name,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
              ],
              onChanged: captureMutating
                  ? null
                  : (value) =>
                        value == null ? null : onCaptureEnvironment(value),
            ),
          ),
          if (!capture.isManual) ...[
            const Divider(height: 1),
            if (hasWorkspace)
              _EnvironmentScopeRow(
                key: const Key('workspace-default-scope'),
                icon: Icons.schedule_outlined,
                tone: ViberColors.warning,
                title: copy('capture.workspace_default'),
                detail:
                    '${copy.format('capture.workspace_default.scope', {'workspace': managed?.workspaceLabel ?? managed?.cwd ?? '—'})}\n${copy('capture.workspace_default.help')}',
                control: DropdownButtonFormField<String>(
                  key: ValueKey(
                    'workspace-default:${capture.key}:${workspaceDefault?.revision ?? 0}',
                  ),
                  initialValue: workspaceDefault?.environmentId ?? '',
                  isExpanded: true,
                  decoration: _scopeDecoration(
                    loading: workspaceLoading || workspaceMutating,
                  ),
                  items: [
                    DropdownMenuItem(
                      value: '',
                      child: Text(copy('capture.workspace_default.none')),
                    ),
                    for (final environment in futureChoices)
                      DropdownMenuItem(
                        value: environment.id,
                        child: Text(
                          environment.name,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                  ],
                  onChanged: workspaceLoading || workspaceMutating
                      ? null
                      : (value) => onWorkspaceDefault(
                          value == null || value.isEmpty ? null : value,
                        ),
                ),
              )
            else
              _EnvironmentScopeRow(
                key: const Key('workspace-default-unavailable'),
                icon: Icons.schedule_outlined,
                tone: ViberColors.textFaint,
                title: copy('capture.workspace_default'),
                detail: copy('capture.workspace_default.unavailable'),
                control: const SizedBox.shrink(),
              ),
          ],
        ],
      ),
    );
  }

  static InputDecoration _scopeDecoration({bool loading = false}) =>
      InputDecoration(
        contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        suffixIcon: loading
            ? const Padding(
                padding: EdgeInsets.all(9),
                child: SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                ),
              )
            : null,
        suffixIconConstraints: const BoxConstraints.tightFor(
          width: 32,
          height: 32,
        ),
      );
}

final class _EnvironmentScopeRow extends StatelessWidget {
  const _EnvironmentScopeRow({
    required this.icon,
    required this.tone,
    required this.title,
    required this.detail,
    required this.control,
    super.key,
  });

  final IconData icon;
  final Color tone;
  final String title;
  final String detail;
  final Widget control;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 7),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 650;
          final label = Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Container(
                width: 2,
                height: 31,
                margin: const EdgeInsets.only(top: 1, right: 8),
                color: tone,
              ),
              Icon(icon, size: 15, color: tone),
              const SizedBox(width: 7),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(title, style: Theme.of(context).textTheme.titleSmall),
                    const SizedBox(height: 2),
                    Text(detail, style: Theme.of(context).textTheme.bodySmall),
                  ],
                ),
              ),
            ],
          );
          if (compact) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                label,
                if (control is! SizedBox) ...[
                  const SizedBox(height: 6),
                  control,
                ],
              ],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.center,
            children: [
              Expanded(child: label),
              const SizedBox(width: 14),
              SizedBox(width: 220, child: control),
            ],
          );
        },
      ),
    );
  }
}

final class _ManualCaptureCreateDialog extends StatefulWidget {
  const _ManualCaptureCreateDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ManualCaptureCreateDialog> createState() =>
      _ManualCaptureCreateDialogState();
}

final class _ManualCaptureCreateDialogState
    extends State<_ManualCaptureCreateDialog> {
  final _formKey = GlobalKey<FormState>();
  final _name = TextEditingController();
  late String _environmentId;
  String _clientClass = 'desktop_app';
  String _lifetime = 'until_revoked';
  int _temporarySeconds = 3600;
  int _loadGeneration = 0;
  bool _loadingContext = true;
  bool _submitted = false;
  ManualCaptureContext? _context;
  ManualCaptureGrantStateTag? _created;

  @override
  void initState() {
    super.initState();
    final environments = widget.controller.data!.environments
        .where((environment) => environment.state == 'active')
        .toList(growable: false);
    _environmentId =
        environments
            .where((environment) => environment.id == 'system_transparent')
            .firstOrNull
            ?.id ??
        environments.first.id;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(_loadContext());
    });
  }

  @override
  void dispose() {
    _name.dispose();
    _created = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final created = _created;
    if (created != null) {
      return AlertDialog(
        title: Text(copy('capture.manual.delivery.title')),
        content: SizedBox(
          width: 470,
          child: _ManualGrantDelivery(grant: created.grant, copy: copy),
        ),
        actions: [
          FilledButton(
            key: const Key('manual-capture-delivery-done'),
            onPressed: () => Navigator.pop(context),
            child: Text(copy('capture.manual.delivery.done')),
          ),
        ],
      );
    }
    final environments = widget.controller.data!.environments
        .where((environment) => environment.state == 'active')
        .toList(growable: false);
    return AlertDialog(
      title: Text(copy('capture.manual.create.title')),
      content: SizedBox(
        width: 470,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextFormField(
                  key: const Key('manual-capture-name'),
                  controller: _name,
                  autofocus: true,
                  maxLength: 256,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.name'),
                    hintText: copy('capture.manual.name.placeholder'),
                  ),
                  validator: (value) => value == null || value.trim().isEmpty
                      ? copy('routes.validation.required')
                      : null,
                ),
                const SizedBox(height: 7),
                DropdownButtonFormField<String>(
                  key: const Key('manual-capture-environment'),
                  initialValue: _environmentId,
                  decoration: InputDecoration(
                    labelText: copy('capture.environment'),
                  ),
                  items: [
                    for (final environment in environments)
                      DropdownMenuItem(
                        value: environment.id,
                        child: Text(environment.name),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) {
                          setState(() {
                            _environmentId = value!;
                            _context = null;
                            _loadingContext = true;
                            _submitted = false;
                          });
                          unawaited(_loadContext());
                        },
                ),
                const SizedBox(height: 9),
                DropdownButtonFormField<String>(
                  initialValue: _clientClass,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.client_class'),
                  ),
                  items: [
                    for (final value in const ['desktop_app', 'cli', 'other'])
                      DropdownMenuItem(
                        value: value,
                        child: Text(copy('capture.manual.client_class.$value')),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) => setState(() => _clientClass = value!),
                ),
                const SizedBox(height: 9),
                DropdownButtonFormField<String>(
                  initialValue: _lifetime,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.lifetime'),
                  ),
                  items: [
                    for (final value in const ['until_revoked', 'temporary'])
                      DropdownMenuItem(
                        value: value,
                        child: Text(copy('capture.manual.lifetime.$value')),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) => setState(() => _lifetime = value!),
                ),
                if (_lifetime == 'temporary' && _context != null) ...[
                  const SizedBox(height: 9),
                  DropdownButtonFormField<int>(
                    key: const Key('manual-capture-duration'),
                    initialValue: _temporarySeconds,
                    decoration: InputDecoration(
                      labelText: copy('capture.manual.duration'),
                    ),
                    items: [
                      for (final seconds in _durationOptions(_context!))
                        DropdownMenuItem(
                          value: seconds,
                          child: Text(_durationLabel(seconds, copy)),
                        ),
                    ],
                    onChanged: widget.controller.mutating
                        ? null
                        : (value) => setState(() => _temporarySeconds = value!),
                  ),
                ],
                const SizedBox(height: 11),
                if (_loadingContext)
                  const Center(
                    child: Padding(
                      padding: EdgeInsets.all(12),
                      child: CircularProgressIndicator(strokeWidth: 1.6),
                    ),
                  )
                else if (_context case final captureContext?)
                  _ManualContextReview(context: captureContext, copy: copy)
                else
                  InlineNotice(
                    message:
                        widget.controller.errorMessage ?? copy('common.retry'),
                    error: true,
                  ),
                if (_submitted && widget.controller.errorMessage != null) ...[
                  const SizedBox(height: 9),
                  InlineNotice(
                    message: widget.controller.errorMessage!,
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
          onPressed: widget.controller.mutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('manual-capture-create-confirm'),
          onPressed: widget.controller.mutating || _context == null
              ? null
              : _create,
          child: widget.controller.mutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(copy('capture.manual.create.action')),
        ),
      ],
    );
  }

  Future<void> _loadContext() async {
    final generation = ++_loadGeneration;
    final environmentId = _environmentId;
    final value = await widget.controller.loadManualCaptureContext(
      environmentId,
    );
    if (!mounted || generation != _loadGeneration) return;
    setState(() {
      _context = value;
      _loadingContext = false;
      if (value != null) {
        _temporarySeconds = value.defaultTemporarySeconds;
      }
    });
  }

  Future<void> _create() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final created = await widget.controller.createManualCapture(
      context: _context!,
      displayName: _name.text,
      clientClass: _clientClass,
      lifetime: _lifetime,
      expiresInSeconds: _lifetime == 'temporary' ? _temporarySeconds : null,
    );
    if (!mounted) return;
    setState(() => _created = created);
  }

  static List<int> _durationOptions(ManualCaptureContext context) {
    final values = {
      context.defaultTemporarySeconds,
      3600,
      8 * 3600,
      24 * 3600,
    }.where((value) => value <= context.maxTemporarySeconds).toList()..sort();
    return values;
  }
}

final class _ManualCaptureRotateDialog extends StatefulWidget {
  const _ManualCaptureRotateDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ManualCaptureRotateDialog> createState() =>
      _ManualCaptureRotateDialogState();
}

final class _ManualCaptureRotateDialogState
    extends State<_ManualCaptureRotateDialog> {
  ManualCaptureGrantStateTag? _rotated;
  bool _submitted = false;

  @override
  void dispose() {
    _rotated = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final rotated = _rotated;
    if (rotated != null) {
      return AlertDialog(
        title: Text(copy('capture.manual.delivery.rotated.title')),
        content: SizedBox(
          width: 470,
          child: _ManualGrantDelivery(grant: rotated.grant, copy: copy),
        ),
        actions: [
          FilledButton(
            key: const Key('manual-capture-delivery-done'),
            onPressed: () => Navigator.pop(context),
            child: Text(copy('capture.manual.delivery.done')),
          ),
        ],
      );
    }
    return AlertDialog(
      title: Text(copy('capture.manual.rotate.title')),
      content: SizedBox(
        width: 430,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('capture.manual.rotate.detail'),
              style: Theme.of(context).textTheme.bodyMedium,
            ),
            if (_submitted && widget.controller.errorMessage != null) ...[
              const SizedBox(height: 9),
              InlineNotice(
                message: widget.controller.errorMessage!,
                error: true,
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.mutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('manual-capture-rotate-confirm'),
          onPressed: widget.controller.mutating ? null : _rotate,
          child: widget.controller.mutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(copy('capture.manual.rotate.action')),
        ),
      ],
    );
  }

  Future<void> _rotate() async {
    setState(() => _submitted = true);
    final rotated = await widget.controller.rotateSelectedManualCapture();
    if (!mounted) return;
    setState(() => _rotated = rotated);
  }
}

final class _ManualContextReview extends StatelessWidget {
  const _ManualContextReview({required this.context, required this.copy});

  final ManualCaptureContext context;
  final AppCopy copy;

  @override
  Widget build(BuildContext buildContext) {
    final semantic = context.root != null;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(9),
      decoration: BoxDecoration(
        color: (semantic ? ViberColors.warning : ViberColors.route).withValues(
          alpha: 0.07,
        ),
        border: Border.all(
          color: (semantic ? ViberColors.warning : ViberColors.route)
              .withValues(alpha: 0.28),
        ),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            copy('capture.manual.review.title'),
            style: Theme.of(buildContext).textTheme.titleSmall,
          ),
          const SizedBox(height: 4),
          Text(
            semantic
                ? copy('capture.manual.review.semantic')
                : copy('capture.manual.review.transparent'),
            style: Theme.of(buildContext).textTheme.bodySmall,
          ),
          const SizedBox(height: 6),
          Text(context.proxyAddress, style: monoStyle),
          Text(
            '${context.environmentId}  ·  r${context.environmentRevision}',
            style: monoStyle,
          ),
          if (context.protectedAuthorities.isNotEmpty)
            Text(
              context.protectedAuthorities.join('  ·  '),
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: monoStyle,
            ),
        ],
      ),
    );
  }
}

final class _ManualGrantDelivery extends StatefulWidget {
  const _ManualGrantDelivery({required this.grant, required this.copy});

  final ManualCaptureGrant grant;
  final AppCopy copy;

  @override
  State<_ManualGrantDelivery> createState() => _ManualGrantDeliveryState();
}

final class _ManualGrantDeliveryState extends State<_ManualGrantDelivery> {
  String? _copied;

  @override
  Widget build(BuildContext context) {
    final grant = widget.grant;
    final copy = widget.copy;
    return Semantics(
      liveRegion: true,
      container: true,
      child: SingleChildScrollView(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            InlineNotice(message: copy('capture.manual.delivery.once')),
            const SizedBox(height: 10),
            _ManualValueRow(
              label: copy('capture.manual.delivery.proxy'),
              value: grant.proxyAddress,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.proxy'),
              }),
              onCopy: () => _copy('proxy', grant.proxyAddress),
            ),
            _ManualValueRow(
              label: copy('capture.manual.delivery.username'),
              value: grant.proxyUsername,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.username'),
              }),
              onCopy: () => _copy('username', grant.proxyUsername),
            ),
            _ManualValueRow(
              label: copy('capture.manual.delivery.password'),
              value: grant.proxyPassword,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.password'),
              }),
              onCopy: () => _copy('password', grant.proxyPassword),
            ),
            if (grant.root case final root?)
              _ManualValueRow(
                label: copy('capture.manual.delivery.root'),
                value: root.pemPath,
                copyTooltip: copy.format('common.copy', {
                  'field': copy('capture.manual.delivery.root'),
                }),
                onCopy: () => _copy('root', root.pemPath),
              ),
            const SizedBox(height: 7),
            Text(
              copy('capture.manual.delivery.evidence'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            if (_copied case final copied?) ...[
              const SizedBox(height: 6),
              Text(
                copy.format('capture.manual.delivery.copied', {
                  'field': copy('capture.manual.delivery.$copied'),
                }),
                style: Theme.of(
                  context,
                ).textTheme.bodySmall?.copyWith(color: ViberColors.verified),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Future<void> _copy(String field, String value) async {
    await Clipboard.setData(ClipboardData(text: value));
    if (mounted) setState(() => _copied = field);
  }
}

final class _ManualValueRow extends StatelessWidget {
  const _ManualValueRow({
    required this.label,
    required this.value,
    required this.copyTooltip,
    required this.onCopy,
  });

  final String label;
  final String value;
  final String copyTooltip;
  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(vertical: 6),
      decoration: const BoxDecoration(
        border: Border(bottom: BorderSide(color: ViberColors.dividerSoft)),
      ),
      child: Row(
        children: [
          SizedBox(
            width: 82,
            child: Text(label, style: Theme.of(context).textTheme.labelMedium),
          ),
          Expanded(
            child: SelectableText(
              value,
              maxLines: 2,
              style: monoStyle.copyWith(color: ViberColors.text),
            ),
          ),
          IconButton(
            onPressed: onCopy,
            tooltip: copyTooltip,
            icon: const Icon(Icons.copy, size: 14),
          ),
        ],
      ),
    );
  }
}

String _durationLabel(int seconds, AppCopy copy) {
  if (seconds % 3600 == 0) {
    return copy.format('capture.manual.duration.hours', {
      'count': seconds ~/ 3600,
    });
  }
  return copy.format('capture.manual.duration.minutes', {
    'count': math.max(1, seconds ~/ 60),
  });
}

String _relativeTime(DateTime timestamp) {
  final now = DateTime.now().toUtc();
  final delta = now.difference(timestamp);
  if (delta.isNegative) return 'now';
  if (delta.inMinutes < 1) return 'now';
  if (delta.inHours < 1) return '${delta.inMinutes}m';
  if (delta.inDays < 1) return '${delta.inHours}h';
  return '${delta.inDays}d';
}

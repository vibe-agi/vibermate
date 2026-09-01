import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

enum _NetworkPanel { approvals, connections, egress, rules }

final class NetworkView extends StatefulWidget {
  const NetworkView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<NetworkView> createState() => _NetworkViewState();
}

final class _NetworkViewState extends State<NetworkView> {
  _NetworkPanel _panel = _NetworkPanel.approvals;

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final data = controller.networkData;
    return Column(
      children: [
        PageHeading(
          title: copy('network.title'),
          subtitle: copy('network.subtitle'),
        ),
        const Divider(height: 1),
        _NetworkTabs(
          selected: _panel,
          data: data,
          copy: copy,
          onSelected: (panel) {
            controller.clearNetworkNotice();
            setState(() => _panel = panel);
          },
        ),
        const Divider(height: 1),
        if (controller.networkError case final message?)
          InlineNotice(message: message, error: true),
        if (controller.networkNotice case final notice?)
          InlineNotice(
            message: copy('notice.$notice'),
            onDismiss: controller.clearNetworkNotice,
            dismissLabel: copy('common.dismiss'),
          ),
        Expanded(child: _body(data)),
      ],
    );
  }

  Widget _body(NetworkData? data) {
    final controller = widget.controller;
    final copy = widget.copy;
    if (data == null) {
      if (controller.networkLoading) {
        return Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const CompactProgressIndicator(),
              const SizedBox(height: 10),
              Text(
                copy('common.loading'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        );
      }
      return CenteredMessage(
        icon: Icons.cloud_off_outlined,
        title: controller.networkError ?? copy('common.retry'),
        detail: copy('network.subtitle'),
      );
    }
    return switch (_panel) {
      _NetworkPanel.approvals => _ApprovalsPane(
        approvals: data.approvals,
        controller: controller,
        copy: copy,
      ),
      _NetworkPanel.connections => _ConnectionsPane(
        page: data.connections,
        controller: controller,
        copy: copy,
      ),
      _NetworkPanel.egress => _EgressPane(
        page: data.egressAttempts,
        controller: controller,
        copy: copy,
      ),
      _NetworkPanel.rules => _RulesPane(
        key: const ValueKey('network-rules-pane'),
        data: data,
        controller: controller,
        copy: copy,
      ),
    };
  }
}

final class _NetworkTabs extends StatelessWidget {
  const _NetworkTabs({
    required this.selected,
    required this.data,
    required this.copy,
    required this.onSelected,
  });

  final _NetworkPanel selected;
  final NetworkData? data;
  final AppCopy copy;
  final ValueChanged<_NetworkPanel> onSelected;

  @override
  Widget build(BuildContext context) {
    final counts = <_NetworkPanel, int?>{
      _NetworkPanel.approvals: data?.approvals.length,
      _NetworkPanel.connections: data?.connections.items.length,
      _NetworkPanel.egress: data?.egressAttempts.items.length,
      _NetworkPanel.rules: data?.rules.rules.length,
    };
    return ColoredBox(
      color: context.viberColors.panel,
      child: SizedBox(
        height: 32,
        child: ListView(
          scrollDirection: Axis.horizontal,
          children: [
            for (final panel in _NetworkPanel.values)
              _NetworkTabButton(
                panel: panel,
                selected: panel == selected,
                label: copy('network.tab.${panel.name}'),
                count: counts[panel],
                onPressed: () => onSelected(panel),
              ),
          ],
        ),
      ),
    );
  }
}

final class _NetworkTabButton extends StatelessWidget {
  const _NetworkTabButton({
    required this.panel,
    required this.selected,
    required this.label,
    required this.count,
    required this.onPressed,
  });

  final _NetworkPanel panel;
  final bool selected;
  final String label;
  final int? count;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      selected: selected,
      button: true,
      label: count == null ? label : '$label, $count',
      child: Material(
        color: selected ? context.viberColors.selection : Colors.transparent,
        child: InkWell(
          key: Key('network-tab-${panel.name}'),
          onTap: onPressed,
          child: Container(
            constraints: const BoxConstraints(minWidth: 82),
            padding: const EdgeInsets.symmetric(horizontal: 10),
            decoration: BoxDecoration(
              border: Border(
                bottom: BorderSide(
                  color: selected
                      ? context.viberColors.route
                      : Colors.transparent,
                  width: 2,
                ),
              ),
            ),
            child: Row(
              mainAxisAlignment: MainAxisAlignment.center,
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                Text(
                  label,
                  style: Theme.of(
                    context,
                  ).textTheme.labelMedium?.copyWith(height: 1),
                ),
                if (count case final value?) ...[
                  const SizedBox(width: 6),
                  Text(
                    '$value',
                    strutStyle: const StrutStyle(
                      fontSize: ViberType.utility,
                      height: 1.2,
                      forceStrutHeight: true,
                    ),
                    style: monoStyle.copyWith(
                      color: panel == _NetworkPanel.approvals && value > 0
                          ? context.viberColors.warning
                          : context.viberColors.textFaint,
                      height: 1,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _ApprovalsPane extends StatelessWidget {
  const _ApprovalsPane({
    required this.approvals,
    required this.controller,
    required this.copy,
  });

  final List<ApprovalRecord> approvals;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    if (approvals.isEmpty) {
      return CenteredMessage(
        icon: Icons.task_alt,
        title: copy('network.approval.empty.title'),
        detail: copy('network.approval.empty.detail'),
      );
    }
    return Column(
      children: [
        SectionLabel(
          label: copy('network.tab.approvals'),
          count: approvals.length,
        ),
        const Divider(height: 1),
        Expanded(
          child: ListView.separated(
            padding: const EdgeInsets.only(bottom: 16),
            itemCount: approvals.length,
            separatorBuilder: (context, index) => const Divider(height: 1),
            itemBuilder: (context, index) => _ApprovalRow(
              key: ValueKey(approvals[index].id),
              approval: approvals[index],
              controller: controller,
              copy: copy,
            ),
          ),
        ),
      ],
    );
  }
}

final class _ApprovalRow extends StatefulWidget {
  const _ApprovalRow({
    required this.approval,
    required this.controller,
    required this.copy,
    super.key,
  });

  final ApprovalRecord approval;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ApprovalRow> createState() => _ApprovalRowState();
}

final class _ApprovalRowState extends State<_ApprovalRow> {
  ApprovalChoice? _pendingChoice;
  bool _technicalExpanded = false;

  @override
  Widget build(BuildContext context) {
    final approval = widget.approval;
    final copy = widget.copy;
    final target = approval.target == null
        ? approval.aggregateKey
        : '${approval.target!.host}:${approval.target!.port}';
    final pending = _pendingChoice;
    return ColoredBox(
      color: context.viberColors.canvas,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 11, 14, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Container(
                  width: 3,
                  height: 37,
                  color: context.viberColors.warning,
                ),
                const SizedBox(width: 9),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy(approval.titleKey),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      const SizedBox(height: 2),
                      Text(
                        copy(approval.summaryKey),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                StatusPill(
                  label: _localizedWire(
                    copy,
                    'network.approval.kind',
                    approval.kind,
                  ),
                  color: context.viberColors.warning,
                ),
              ],
            ),
            const SizedBox(height: 8),
            Wrap(
              spacing: 12,
              runSpacing: 5,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                _InlineEvidence(icon: Icons.language, text: target, mono: true),
                _InlineEvidence(
                  icon: Icons.call_split,
                  text: copy.format('network.approval.requests', {
                    'count': approval.requestCount,
                  }),
                ),
                _InlineEvidence(
                  icon: Icons.hourglass_top,
                  text: copy.format('network.approval.waiters', {
                    'count': approval.waiterCount,
                  }),
                ),
                _InlineEvidence(
                  icon: Icons.schedule,
                  text: copy.format('network.approval.expires', {
                    'time': _clock(approval.expiresAt),
                  }),
                ),
                if (approval.environmentId != null &&
                    approval.environmentRevision != null &&
                    approval.environmentDigest != null)
                  TextButton.icon(
                    key: Key('approval-environment-${approval.id}'),
                    onPressed: widget.controller.environmentRevisionLoading
                        ? null
                        : () => unawaited(
                            widget.controller.inspectEnvironmentRevision(
                              approval.environmentId!,
                              approval.environmentRevision!,
                              expectedDigest: approval.environmentDigest,
                            ),
                          ),
                    icon: const Icon(Icons.history, size: 12),
                    label: Text(
                      copy.format('environment.history.inspect', {
                        'revision': approval.environmentRevision!,
                      }),
                    ),
                  ),
              ],
            ),
            if (approval.subjectLabels.isNotEmpty) ...[
              const SizedBox(height: 6),
              Text(
                approval.subjectLabels.join('  ·  '),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
            const SizedBox(height: 8),
            if (pending == null)
              Wrap(
                spacing: 7,
                runSpacing: 7,
                children: [
                  for (final choice in approval.choices)
                    OutlinedButton.icon(
                      key: Key(
                        'approval-${approval.id}-${choice.decision}-${choice.scope}',
                      ),
                      onPressed: widget.controller.networkMutating
                          ? null
                          : () => setState(() => _pendingChoice = choice),
                      icon: Icon(
                        choice.decision == 'deny'
                            ? Icons.block
                            : Icons.play_arrow,
                        size: 14,
                      ),
                      label: Text(copy(choice.labelKey)),
                    ),
                ],
              )
            else
              _ApprovalConfirmation(
                approval: approval,
                choice: pending,
                target: target,
                copy: copy,
                busy: widget.controller.networkMutating,
                onCancel: () => setState(() => _pendingChoice = null),
                onConfirm: () => unawaited(_confirm(pending)),
              ),
            const SizedBox(height: 3),
            TextButton.icon(
              key: Key('approval-technical-${approval.id}'),
              onPressed: () =>
                  setState(() => _technicalExpanded = !_technicalExpanded),
              icon: Icon(
                _technicalExpanded ? Icons.expand_less : Icons.expand_more,
                size: 14,
              ),
              label: Text(copy('network.approval.technical')),
            ),
            if (_technicalExpanded)
              _EvidenceDetails(
                facts: [
                  (copy('network.approval.id'), approval.id),
                  (copy('network.approval.revision'), '${approval.revision}'),
                ],
              ),
          ],
        ),
      ),
    );
  }

  Future<void> _confirm(ApprovalChoice choice) async {
    final resolved = await widget.controller.decideApproval(
      widget.approval,
      choice,
    );
    if (!mounted || resolved) return;
    setState(() => _pendingChoice = null);
  }
}

final class _ApprovalConfirmation extends StatelessWidget {
  const _ApprovalConfirmation({
    required this.approval,
    required this.choice,
    required this.target,
    required this.copy,
    required this.busy,
    required this.onCancel,
    required this.onConfirm,
  });

  final ApprovalRecord approval;
  final ApprovalChoice choice;
  final String target;
  final AppCopy copy;
  final bool busy;
  final VoidCallback onCancel;
  final VoidCallback onConfirm;

  @override
  Widget build(BuildContext context) {
    final allowing = choice.decision != 'deny';
    final tone = allowing
        ? context.viberColors.warning
        : context.viberColors.route;
    return Semantics(
      liveRegion: true,
      container: true,
      child: Container(
        key: const Key('approval-confirmation'),
        width: double.infinity,
        padding: const EdgeInsets.all(9),
        decoration: BoxDecoration(
          color: tone.withValues(alpha: 0.08),
          border: Border.all(color: tone.withValues(alpha: 0.38)),
          borderRadius: ViberMetrics.surfaceRadius,
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('network.approval.confirm.title'),
              style: Theme.of(context).textTheme.titleSmall,
            ),
            const SizedBox(height: 3),
            Text(
              copy.format('network.approval.confirm.detail', {
                'action': copy(choice.labelKey),
                'target': target,
                'revision': approval.revision,
              }),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 7),
            Wrap(
              spacing: 7,
              children: [
                TextButton(
                  onPressed: busy ? null : onCancel,
                  child: Text(copy('common.cancel')),
                ),
                FilledButton(
                  key: const Key('approval-confirm-action'),
                  onPressed: busy ? null : onConfirm,
                  style: FilledButton.styleFrom(backgroundColor: tone),
                  child: busy
                      ? const SizedBox.square(
                          dimension: 13,
                          child: CircularProgressIndicator(strokeWidth: 1.5),
                        )
                      : Text(copy('common.confirm')),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

final class _InlineEvidence extends StatelessWidget {
  const _InlineEvidence({
    required this.icon,
    required this.text,
    this.mono = false,
  });

  final IconData icon;
  final String text;
  final bool mono;

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 12, color: context.viberColors.textFaint),
        const SizedBox(width: 4),
        Text(
          text,
          style: mono
              ? monoStyle
              : Theme.of(
                  context,
                ).textTheme.bodySmall?.copyWith(fontSize: ViberType.utility),
        ),
      ],
    );
  }
}

final class _EvidenceFilterField extends StatelessWidget {
  const _EvidenceFilterField({
    required this.controller,
    required this.label,
    required this.clearLabel,
    required this.onChanged,
    required this.onClear,
    this.moreHint,
    super.key,
  });

  final TextEditingController controller;
  final String label;
  final String clearLabel;
  final String? moreHint;
  final ValueChanged<String> onChanged;
  final VoidCallback onClear;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      color: context.viberColors.panel,
      padding: const EdgeInsets.fromLTRB(9, 6, 9, 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 420),
            child: CompactSearchField(
              controller: controller,
              hintText: label,
              onChanged: onChanged,
              onClear: controller.text.isEmpty ? null : onClear,
              clearLabel: clearLabel,
            ),
          ),
          if (moreHint case final hint?) ...[
            const SizedBox(height: 4),
            Text(hint, style: Theme.of(context).textTheme.bodySmall),
          ],
        ],
      ),
    );
  }
}

final class _ConnectionsPane extends StatefulWidget {
  const _ConnectionsPane({
    required this.page,
    required this.controller,
    required this.copy,
  });

  final ConnectionPage page;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ConnectionsPane> createState() => _ConnectionsPaneState();
}

final class _ConnectionsPaneState extends State<_ConnectionsPane> {
  late final TextEditingController _filterController;
  String _query = '';

  @override
  void initState() {
    super.initState();
    _filterController = TextEditingController();
  }

  @override
  void dispose() {
    _filterController.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final page = widget.page;
    final controller = widget.controller;
    final copy = widget.copy;
    final query = _query.trim().toLowerCase();
    final visible = query.isEmpty
        ? page.items
        : page.items
              .where(
                (record) => _connectionSearchText(record, copy).contains(query),
              )
              .toList(growable: false);
    return LayoutBuilder(
      builder: (context, constraints) {
        final compact = constraints.maxWidth < 760;
        return Column(
          children: [
            SectionLabel(
              label: copy('network.connections.title'),
              count: query.isEmpty ? page.items.length : visible.length,
              trailing: Tooltip(
                message: copy('network.connections.latest.detail'),
                child: StatusPill(
                  label: copy('network.connections.latest'),
                  color: context.viberColors.route,
                  icon: Icons.filter_center_focus,
                ),
              ),
            ),
            _EvidenceFilterField(
              key: const Key('connections-filter'),
              controller: _filterController,
              label: copy('network.filter.connections'),
              clearLabel: copy('network.filter.clear'),
              moreHint: query.isNotEmpty && page.nextCursor != null
                  ? copy('network.filter.more_hint')
                  : null,
              onChanged: (value) => setState(() => _query = value),
              onClear: () {
                _filterController.clear();
                setState(() => _query = '');
              },
            ),
            if (!compact) _ConnectionHeader(copy: copy),
            const Divider(height: 1),
            Expanded(
              child: page.items.isEmpty
                  ? CenteredMessage(
                      icon: Icons.lan_outlined,
                      title: copy('network.connections.empty'),
                    )
                  : visible.isEmpty
                  ? CenteredMessage(
                      icon: Icons.filter_alt_off_outlined,
                      title: copy('network.filter.no_match'),
                      action: page.nextCursor == null
                          ? null
                          : OutlinedButton.icon(
                              key: const Key('connections-load-more'),
                              onPressed: controller.networkLoading
                                  ? null
                                  : () => unawaited(
                                      controller.loadMoreConnections(),
                                    ),
                              icon: const Icon(Icons.expand_more, size: 14),
                              label: Text(
                                copy('network.connections.load_more'),
                              ),
                            ),
                    )
                  : ListView.builder(
                      itemCount:
                          visible.length + (page.nextCursor == null ? 0 : 1),
                      itemBuilder: (context, index) {
                        if (index == visible.length) {
                          return _LoadMoreRow(
                            key: const Key('connections-load-more'),
                            label: copy('network.connections.load_more'),
                            loading: controller.networkLoading,
                            onPressed: () =>
                                unawaited(controller.loadMoreConnections()),
                          );
                        }
                        return _ConnectionEvidenceRow(
                          key: ValueKey(visible[index].connectionId),
                          record: visible[index],
                          compact: compact,
                          copy: copy,
                        );
                      },
                    ),
            ),
          ],
        );
      },
    );
  }
}

final class _ConnectionHeader extends StatelessWidget {
  const _ConnectionHeader({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final style = Theme.of(context).textTheme.labelMedium;
    return Container(
      height: ViberMetrics.compactRowHeight,
      color: context.viberColors.panel,
      padding: const EdgeInsets.symmetric(horizontal: 12),
      child: Row(
        children: [
          SizedBox(
            width: 150,
            child: Text(copy('network.connections.source'), style: style),
          ),
          Expanded(
            flex: 2,
            child: Text(copy('network.connections.destination'), style: style),
          ),
          SizedBox(
            width: 140,
            child: Text(copy('network.connections.environment'), style: style),
          ),
          SizedBox(
            width: 82,
            child: Text(copy('network.connections.decision'), style: style),
          ),
          SizedBox(
            width: 84,
            child: Text(copy('network.connections.phase'), style: style),
          ),
          SizedBox(
            width: 58,
            child: Text(copy('network.connections.started'), style: style),
          ),
          const SizedBox(width: 20),
        ],
      ),
    );
  }
}

final class _ConnectionEvidenceRow extends StatefulWidget {
  const _ConnectionEvidenceRow({
    required this.record,
    required this.compact,
    required this.copy,
    super.key,
  });

  final ConnectionRecord record;
  final bool compact;
  final AppCopy copy;

  @override
  State<_ConnectionEvidenceRow> createState() => _ConnectionEvidenceRowState();
}

final class _ConnectionEvidenceRowState extends State<_ConnectionEvidenceRow> {
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final record = widget.record;
    final copy = widget.copy;
    final source =
        record.sourceLabel ?? copy('network.connections.unknown_source');
    final environment =
        record.environmentName ??
        copy('network.connections.unknown_environment');
    final decision = record.decision == null
        ? copy('network.connections.undecided')
        : _localizedWire(copy, 'network.value.decision', record.decision!);
    final phase = _localizedWire(copy, 'network.value.phase', record.phase);
    return Column(
      children: [
        Semantics(
          button: true,
          expanded: _expanded,
          label: '$source, ${record.requestedHost}:${record.port}, $decision',
          child: Material(
            color: _expanded
                ? context.viberColors.panelRaised
                : Colors.transparent,
            child: InkWell(
              onTap: () => setState(() => _expanded = !_expanded),
              child: widget.compact
                  ? _compactRow(source, environment, decision, phase)
                  : _wideRow(source, environment, decision, phase),
            ),
          ),
        ),
        if (_expanded)
          _EvidenceDetails(
            key: Key('connection-evidence-table-${record.connectionId}'),
            facts: [
              (copy('network.fact.connection_id'), record.connectionId),
              (copy('network.fact.sequence'), '${record.sequence}'),
              (
                copy('network.fact.source_confidence'),
                _localizedWire(
                  copy,
                  'network.value.confidence',
                  record.sourceConfidence,
                ),
              ),
              (copy('network.fact.environment_id'), record.environmentId ?? ''),
              (copy('network.fact.observed_sni'), record.observedSni ?? ''),
              (copy('network.fact.route_host'), record.routeHost ?? ''),
              (copy('network.fact.resolved_ip'), record.ip ?? ''),
              (copy('network.fact.rule'), record.ruleId ?? ''),
              (
                copy('network.fact.decryption'),
                _localizedWire(
                  copy,
                  'network.value.decryption',
                  record.decryption,
                ),
              ),
              (
                copy('network.fact.egress_authority'),
                record.egressScope == null
                    ? ''
                    : _localizedWire(
                        copy,
                        'network.value.scope',
                        record.egressScope!,
                      ),
              ),
              (
                copy('network.fact.egress_source'),
                record.egressSource == null
                    ? ''
                    : _localizedWire(
                        copy,
                        'network.value.source',
                        record.egressSource!,
                      ),
              ),
              (
                copy('network.fact.bytes'),
                copy.format('network.connections.bytes', {
                  'up': _bytes(record.bytesUp),
                  'down': _bytes(record.bytesDown),
                }),
              ),
              (
                copy('network.fact.outcome'),
                record.outcome == null
                    ? ''
                    : _localizedWire(
                        copy,
                        'network.value.outcome',
                        record.outcome!,
                      ),
              ),
              (copy('network.fact.error'), record.errorClass ?? ''),
            ],
          ),
        const Divider(height: 1),
      ],
    );
  }

  Widget _wideRow(
    String source,
    String environment,
    String decision,
    String phase,
  ) {
    final record = widget.record;
    return SizedBox(
      height: 38,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12),
        child: Row(
          children: [
            SizedBox(
              width: 150,
              child: Text(source, maxLines: 1, overflow: TextOverflow.ellipsis),
            ),
            Expanded(
              flex: 2,
              child: Text(
                '${record.requestedHost}:${record.port}',
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: monoStyle.copyWith(color: context.viberColors.text),
              ),
            ),
            SizedBox(
              width: 140,
              child: Text(
                environment,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
            SizedBox(
              width: 82,
              child: Text(
                decision,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: _decisionColor(context, record.decision),
                  fontSize: ViberType.supporting,
                ),
              ),
            ),
            SizedBox(width: 84, child: Text(phase)),
            SizedBox(
              width: 58,
              child: Text(_clock(record.startedAt), style: monoStyle),
            ),
            SizedBox(
              width: 20,
              child: Icon(
                _expanded ? Icons.expand_less : Icons.expand_more,
                size: 16,
                color: context.viberColors.textFaint,
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _compactRow(
    String source,
    String environment,
    String decision,
    String phase,
  ) {
    final record = widget.record;
    return Padding(
      padding: const EdgeInsets.fromLTRB(11, 8, 9, 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        source,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                    ),
                    Text(
                      decision,
                      style: Theme.of(context).textTheme.labelMedium?.copyWith(
                        color: _decisionColor(context, record.decision),
                        fontSize: ViberType.utility,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 2),
                Text(
                  '${record.requestedHost}:${record.port}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: monoStyle.copyWith(color: context.viberColors.text),
                ),
                Text(
                  '$environment  ·  $phase  ·  ${_clock(record.startedAt)}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
          const SizedBox(width: 5),
          Icon(
            _expanded ? Icons.expand_less : Icons.expand_more,
            size: 16,
            color: context.viberColors.textFaint,
          ),
        ],
      ),
    );
  }
}

final class _EgressPane extends StatefulWidget {
  const _EgressPane({
    required this.page,
    required this.controller,
    required this.copy,
  });

  final EgressAttemptPage page;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_EgressPane> createState() => _EgressPaneState();
}

final class _EgressPaneState extends State<_EgressPane> {
  late final TextEditingController _filterController;
  String _query = '';

  @override
  void initState() {
    super.initState();
    _filterController = TextEditingController();
  }

  @override
  void dispose() {
    _filterController.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final page = widget.page;
    final controller = widget.controller;
    final copy = widget.copy;
    final query = _query.trim().toLowerCase();
    final visible = query.isEmpty
        ? page.items
        : page.items
              .where(
                (record) => _egressSearchText(record, copy).contains(query),
              )
              .toList(growable: false);
    return LayoutBuilder(
      builder: (context, constraints) {
        final compact = constraints.maxWidth < 760;
        return Column(
          children: [
            SectionLabel(
              label: copy('network.egress.title'),
              count: query.isEmpty ? page.items.length : visible.length,
              trailing: Tooltip(
                message: copy('network.egress.detail'),
                child: Icon(
                  Icons.info_outline,
                  size: 14,
                  color: context.viberColors.textFaint,
                ),
              ),
            ),
            _EvidenceFilterField(
              key: const Key('egress-filter'),
              controller: _filterController,
              label: copy('network.filter.egress'),
              clearLabel: copy('network.filter.clear'),
              moreHint: query.isNotEmpty && page.nextCursor != null
                  ? copy('network.filter.more_hint')
                  : null,
              onChanged: (value) => setState(() => _query = value),
              onClear: () {
                _filterController.clear();
                setState(() => _query = '');
              },
            ),
            if (!compact) _EgressHeader(copy: copy),
            const Divider(height: 1),
            Expanded(
              child: page.items.isEmpty
                  ? CenteredMessage(
                      icon: Icons.north_east,
                      title: copy('network.egress.empty'),
                    )
                  : visible.isEmpty
                  ? CenteredMessage(
                      icon: Icons.filter_alt_off_outlined,
                      title: copy('network.filter.no_match'),
                      action: page.nextCursor == null
                          ? null
                          : OutlinedButton.icon(
                              key: const Key('egress-load-more'),
                              onPressed: controller.networkLoading
                                  ? null
                                  : () => unawaited(
                                      controller.loadMoreEgressAttempts(),
                                    ),
                              icon: const Icon(Icons.expand_more, size: 14),
                              label: Text(copy('network.egress.load_more')),
                            ),
                    )
                  : ListView.builder(
                      itemCount:
                          visible.length + (page.nextCursor == null ? 0 : 1),
                      itemBuilder: (context, index) {
                        if (index == visible.length) {
                          return _LoadMoreRow(
                            key: const Key('egress-load-more'),
                            label: copy('network.egress.load_more'),
                            loading: controller.networkLoading,
                            onPressed: () =>
                                unawaited(controller.loadMoreEgressAttempts()),
                          );
                        }
                        return _EgressEvidenceRow(
                          key: ValueKey(visible[index].id),
                          record: visible[index],
                          compact: compact,
                          copy: copy,
                        );
                      },
                    ),
            ),
          ],
        );
      },
    );
  }
}

final class _EgressHeader extends StatelessWidget {
  const _EgressHeader({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final style = Theme.of(context).textTheme.labelMedium;
    return Container(
      height: ViberMetrics.compactRowHeight,
      color: context.viberColors.panel,
      padding: const EdgeInsets.symmetric(horizontal: 12),
      child: Row(
        children: [
          SizedBox(
            width: 110,
            child: Text(copy('network.egress.caller'), style: style),
          ),
          Expanded(
            flex: 2,
            child: Text(copy('network.egress.target'), style: style),
          ),
          SizedBox(
            width: 130,
            child: Text(copy('network.egress.purpose'), style: style),
          ),
          SizedBox(
            width: 95,
            child: Text(copy('network.egress.authority'), style: style),
          ),
          SizedBox(
            width: 90,
            child: Text(copy('network.egress.outcome'), style: style),
          ),
          SizedBox(
            width: 58,
            child: Text(copy('network.egress.started'), style: style),
          ),
          const SizedBox(width: 20),
        ],
      ),
    );
  }
}

final class _EgressEvidenceRow extends StatefulWidget {
  const _EgressEvidenceRow({
    required this.record,
    required this.compact,
    required this.copy,
    super.key,
  });

  final EgressAttemptRecord record;
  final bool compact;
  final AppCopy copy;

  @override
  State<_EgressEvidenceRow> createState() => _EgressEvidenceRowState();
}

final class _EgressEvidenceRowState extends State<_EgressEvidenceRow> {
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final record = widget.record;
    final copy = widget.copy;
    final outcome = record.outcome == null
        ? copy('network.egress.running')
        : _localizedWire(copy, 'network.value.outcome', record.outcome!);
    final caller = _localizedWire(copy, 'network.value.caller', record.caller);
    final purpose = _localizedWire(
      copy,
      'network.value.purpose',
      record.purpose,
    );
    final authority = _localizedWire(
      copy,
      'network.value.authority',
      record.authority,
    );
    return Column(
      children: [
        Semantics(
          button: true,
          expanded: _expanded,
          label: '$caller, ${record.targetOrigin}, $outcome',
          child: Material(
            color: _expanded
                ? context.viberColors.panelRaised
                : Colors.transparent,
            child: InkWell(
              onTap: () => setState(() => _expanded = !_expanded),
              child: widget.compact
                  ? _compactRow(outcome, caller, purpose, authority)
                  : _wideRow(outcome, caller, purpose, authority),
            ),
          ),
        ),
        if (_expanded)
          _EvidenceDetails(
            key: Key('egress-evidence-table-${record.id}'),
            facts: [
              (copy('network.fact.attempt_id'), record.id),
              (copy('network.fact.sequence'), '${record.sequence}'),
              (copy('network.fact.connection_id'), record.connectionId ?? ''),
              (
                copy('network.fact.parent'),
                [
                  _localizedWire(
                    copy,
                    'network.value.parent',
                    record.parentKind,
                  ),
                  ?record.parentId,
                ].join(' · '),
              ),
              (copy('network.fact.exchange'), record.exchangeId ?? ''),
              (copy('network.fact.caller_id'), record.callerId ?? ''),
              (
                copy('network.fact.payload'),
                _localizedWire(
                  copy,
                  'network.value.payload',
                  record.payloadClass,
                ),
              ),
              (copy('network.fact.policy'), record.policyId ?? ''),
              (copy('network.fact.rule'), record.ruleId ?? ''),
              (copy('network.fact.proxy'), record.proxyId ?? ''),
              (
                copy('network.fact.transport'),
                record.reusedTransport
                    ? copy('network.egress.reused')
                    : copy('network.egress.fresh'),
              ),
              (
                copy('network.fact.bytes'),
                copy.format('network.egress.bytes', {
                  'out': _bytes(record.bytesOut),
                  'in': _bytes(record.bytesIn),
                }),
              ),
              (copy('network.fact.error'), record.errorClass ?? ''),
            ],
          ),
        const Divider(height: 1),
      ],
    );
  }

  Widget _wideRow(
    String outcome,
    String caller,
    String purpose,
    String authority,
  ) {
    final record = widget.record;
    return SizedBox(
      height: 38,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12),
        child: Row(
          children: [
            SizedBox(width: 110, child: Text(caller)),
            Expanded(
              flex: 2,
              child: Text(
                record.targetOrigin,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: monoStyle.copyWith(color: context.viberColors.text),
              ),
            ),
            SizedBox(
              width: 130,
              child: Text(purpose, overflow: TextOverflow.ellipsis),
            ),
            SizedBox(width: 95, child: Text(authority)),
            SizedBox(
              width: 90,
              child: Text(
                outcome,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: _outcomeColor(context, record.outcome),
                  fontSize: ViberType.supporting,
                ),
              ),
            ),
            SizedBox(
              width: 58,
              child: Text(_clock(record.startedAt), style: monoStyle),
            ),
            SizedBox(
              width: 20,
              child: Icon(
                _expanded ? Icons.expand_less : Icons.expand_more,
                size: 16,
                color: context.viberColors.textFaint,
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _compactRow(
    String outcome,
    String caller,
    String purpose,
    String authority,
  ) {
    final record = widget.record;
    return Padding(
      padding: const EdgeInsets.fromLTRB(11, 8, 9, 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        purpose,
                        style: Theme.of(context).textTheme.titleSmall,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    Text(
                      outcome,
                      style: Theme.of(context).textTheme.labelMedium?.copyWith(
                        color: _outcomeColor(context, record.outcome),
                        fontSize: ViberType.utility,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 2),
                Text(
                  record.targetOrigin,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: monoStyle.copyWith(color: context.viberColors.text),
                ),
                Text(
                  '$caller  ·  $authority  ·  ${_clock(record.startedAt)}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
          const SizedBox(width: 5),
          Icon(
            _expanded ? Icons.expand_less : Icons.expand_more,
            size: 16,
            color: context.viberColors.textFaint,
          ),
        ],
      ),
    );
  }
}

final class _EvidenceDetails extends StatelessWidget {
  const _EvidenceDetails({required this.facts, super.key});

  final List<(String, String)> facts;

  @override
  Widget build(BuildContext context) {
    final visibleFacts = facts
        .where((fact) => fact.$2.trim().isNotEmpty && fact.$2 != '—')
        .toList(growable: false);
    return LayoutBuilder(
      builder: (context, constraints) {
        final groupsPerRow = constraints.maxWidth >= 1320
            ? 3
            : constraints.maxWidth >= 720
            ? 2
            : 1;
        final rows = <TableRow>[];
        for (
          var start = 0;
          start < visibleFacts.length;
          start += groupsPerRow
        ) {
          final rowIndex = rows.length;
          rows.add(
            TableRow(
              decoration: BoxDecoration(
                color: rowIndex.isEven
                    ? context.viberColors.panel
                    : context.viberColors.panelRaised,
              ),
              children: [
                for (var slot = 0; slot < groupsPerRow; slot++) ...[
                  if (start + slot < visibleFacts.length) ...[
                    _EvidenceLabelCell(label: visibleFacts[start + slot].$1),
                    _EvidenceValueCell(
                      label: visibleFacts[start + slot].$1,
                      value: visibleFacts[start + slot].$2,
                    ),
                  ] else ...[
                    const SizedBox.shrink(),
                    const SizedBox.shrink(),
                  ],
                ],
              ],
            ),
          );
        }
        return Container(
          width: double.infinity,
          decoration: BoxDecoration(
            color: context.viberColors.panel,
            border: Border(
              top: BorderSide(color: context.viberColors.dividerSoft),
            ),
          ),
          padding: const EdgeInsets.fromLTRB(12, 5, 12, 7),
          child: Table(
            key: Key('evidence-table-$groupsPerRow-groups'),
            columnWidths: {
              for (var slot = 0; slot < groupsPerRow; slot++) ...{
                slot * 2: FixedColumnWidth(groupsPerRow == 1 ? 110 : 98),
                slot * 2 + 1: const FlexColumnWidth(),
              },
            },
            defaultVerticalAlignment: TableCellVerticalAlignment.middle,
            border: TableBorder(
              horizontalInside: BorderSide(
                color: context.viberColors.dividerSoft,
              ),
            ),
            children: rows,
          ),
        );
      },
    );
  }
}

final class _EvidenceLabelCell extends StatelessWidget {
  const _EvidenceLabelCell({required this.label});

  final String label;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(8, 6, 6, 6),
      child: Text(
        label,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: Theme.of(context).textTheme.labelMedium?.copyWith(
          color: context.viberColors.textFaint,
          fontSize: ViberType.micro,
          fontWeight: FontWeight.w600,
        ),
      ),
    );
  }
}

final class _EvidenceValueCell extends StatelessWidget {
  const _EvidenceValueCell({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      container: true,
      label: '$label: $value',
      child: Tooltip(
        message: value,
        child: Padding(
          padding: const EdgeInsets.fromLTRB(4, 6, 10, 6),
          child: SelectableText(
            value,
            textWidthBasis: TextWidthBasis.parent,
            style: monoStyle.copyWith(color: context.viberColors.text),
          ),
        ),
      ),
    );
  }
}

final class _LoadMoreRow extends StatelessWidget {
  const _LoadMoreRow({
    required this.label,
    required this.loading,
    required this.onPressed,
    super.key,
  });

  final String label;
  final bool loading;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 12),
      child: Center(
        child: OutlinedButton.icon(
          onPressed: loading ? null : onPressed,
          icon: loading
              ? const SizedBox.square(
                  dimension: 12,
                  child: CircularProgressIndicator(strokeWidth: 1.4),
                )
              : const Icon(Icons.expand_more, size: 14),
          label: Text(label),
        ),
      ),
    );
  }
}

final class _RulesPane extends StatefulWidget {
  const _RulesPane({
    required this.data,
    required this.controller,
    required this.copy,
    super.key,
  });

  final NetworkData data;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_RulesPane> createState() => _RulesPaneState();
}

final class _RulesPaneState extends State<_RulesPane> {
  late ConnectionRuleSet _base;
  late List<ConnectionRule> _draft;
  late String _mode;
  bool _dirty = false;
  bool _confirmingSave = false;
  bool _conflict = false;
  bool _confirmingReload = false;

  @override
  void initState() {
    super.initState();
    _adopt(widget.data.rules);
  }

  @override
  void didUpdateWidget(covariant _RulesPane oldWidget) {
    super.didUpdateWidget(oldWidget);
    final incoming = widget.data.rules;
    if (incoming.revision == _base.revision) return;
    if (!_dirty || _matchesDraft(incoming)) {
      _adopt(incoming);
    } else {
      _conflict = true;
      _confirmingSave = false;
    }
  }

  void _adopt(ConnectionRuleSet value) {
    _base = value;
    _draft = List.of(value.rules);
    _mode = value.mode;
    _dirty = false;
    _confirmingSave = false;
    _conflict = false;
    _confirmingReload = false;
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return Column(
      children: [
        _RulesToolbar(
          revision: _base.revision,
          count: _draft.length,
          dirty: _dirty,
          conflict: _conflict,
          busy: widget.controller.networkMutating,
          copy: copy,
          onAdd: () => unawaited(_editRule()),
          onSave: () => setState(() {
            _confirmingSave = true;
            _confirmingReload = false;
          }),
        ),
        const Divider(height: 1),
        _ModeEditor(
          mode: _mode,
          copy: copy,
          enabled: !widget.controller.networkMutating,
          onChanged: (mode) => setState(() {
            _mode = mode;
            _recomputeDirty();
          }),
        ),
        const Divider(height: 1),
        if (_conflict)
          _RuleConflictBanner(
            copy: copy,
            confirming: _confirmingReload,
            onReload: () => setState(() => _confirmingReload = true),
            onCancel: () => setState(() => _confirmingReload = false),
            onConfirm: () => setState(() => _adopt(widget.data.rules)),
          ),
        if (_confirmingSave && !_conflict)
          _RuleSaveConfirmation(
            revision: _base.revision,
            count: _draft.length,
            busy: widget.controller.networkMutating,
            copy: copy,
            onCancel: () => setState(() => _confirmingSave = false),
            onConfirm: () => unawaited(_save()),
          ),
        SectionLabel(label: copy('network.tab.rules'), count: _draft.length),
        const Divider(height: 1),
        Expanded(
          child: _draft.isEmpty
              ? CenteredMessage(
                  icon: Icons.rule_outlined,
                  title: copy('network.rules.empty'),
                  action: OutlinedButton.icon(
                    key: const Key('rules-empty-add'),
                    onPressed: widget.controller.networkMutating
                        ? null
                        : () => unawaited(_editRule()),
                    icon: const Icon(Icons.add, size: 14),
                    label: Text(copy('network.rules.add')),
                  ),
                )
              : LayoutBuilder(
                  builder: (context, constraints) {
                    final compact = constraints.maxWidth < 650;
                    final sorted = List.of(_draft)
                      ..sort((left, right) {
                        final priority = right.priority.compareTo(
                          left.priority,
                        );
                        return priority == 0
                            ? left.id.compareTo(right.id)
                            : priority;
                      });
                    return ListView.separated(
                      itemCount: sorted.length,
                      separatorBuilder: (context, index) =>
                          const Divider(height: 1),
                      itemBuilder: (context, index) {
                        final rule = sorted[index];
                        return _RuleRow(
                          rule: rule,
                          compact: compact,
                          copy: copy,
                          enabled: !widget.controller.networkMutating,
                          onEdit: () => unawaited(_editRule(rule)),
                          onRemove: () => _remove(rule),
                        );
                      },
                    );
                  },
                ),
        ),
      ],
    );
  }

  Future<void> _editRule([ConnectionRule? existing]) async {
    final updated = await _showRuleEditor(
      context: context,
      existing: existing,
      rules: _draft,
      copy: widget.copy,
    );
    if (!mounted || updated == null) return;
    setState(() {
      final index = existing == null
          ? -1
          : _draft.indexWhere((rule) => rule.id == existing.id);
      if (index < 0) {
        _draft.add(updated);
      } else {
        _draft[index] = updated;
      }
      _confirmingSave = false;
      _recomputeDirty();
    });
  }

  void _remove(ConnectionRule rule) {
    setState(() {
      _draft.removeWhere((candidate) => candidate.id == rule.id);
      _confirmingSave = false;
      _recomputeDirty();
    });
  }

  void _recomputeDirty() {
    _dirty = _mode != _base.mode || !_sameRules(_draft, _base.rules);
    if (!_dirty) {
      _confirmingSave = false;
      _conflict = false;
    }
  }

  bool _matchesDraft(ConnectionRuleSet value) =>
      value.mode == _mode && _sameRules(value.rules, _draft);

  Future<void> _save() async {
    final saved = await widget.controller.replaceConnectionRules(
      base: _base,
      rules: List.unmodifiable(_draft),
      mode: _mode,
    );
    if (!mounted) return;
    if (saved) {
      setState(() => _adopt(widget.controller.networkData!.rules));
      return;
    }
    await widget.controller.refreshNetwork();
    if (!mounted) return;
    setState(() {
      _confirmingSave = false;
      _conflict = widget.data.rules.revision != _base.revision;
    });
  }
}

final class _RulesToolbar extends StatelessWidget {
  const _RulesToolbar({
    required this.revision,
    required this.count,
    required this.dirty,
    required this.conflict,
    required this.busy,
    required this.copy,
    required this.onAdd,
    required this.onSave,
  });

  final int revision;
  final int count;
  final bool dirty;
  final bool conflict;
  final bool busy;
  final AppCopy copy;
  final VoidCallback onAdd;
  final VoidCallback onSave;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      color: context.viberColors.panel,
      padding: const EdgeInsets.fromLTRB(12, 9, 10, 9),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final actions = Wrap(
            spacing: 7,
            runSpacing: 7,
            children: [
              OutlinedButton.icon(
                key: const Key('rules-add'),
                onPressed: busy ? null : onAdd,
                icon: const Icon(Icons.add, size: 14),
                label: Text(copy('network.rules.add')),
              ),
              FilledButton.icon(
                key: const Key('rules-save'),
                onPressed: !dirty || conflict || busy ? null : onSave,
                icon: const Icon(Icons.save_outlined, size: 14),
                label: Text(copy('network.rules.save')),
              ),
            ],
          );
          final heading = Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    copy('network.rules.title'),
                    style: Theme.of(context).textTheme.titleMedium,
                  ),
                  if (dirty) ...[
                    const SizedBox(width: 7),
                    StatusPill(
                      label: copy('network.rules.dirty'),
                      color: context.viberColors.warning,
                    ),
                  ],
                ],
              ),
              const SizedBox(height: 2),
              Text(
                '${copy.format('network.rules.revision', {'revision': revision})}  ·  $count',
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          );
          if (constraints.maxWidth < 560) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [heading, const SizedBox(height: 8), actions],
            );
          }
          return Row(
            children: [
              Expanded(child: heading),
              actions,
            ],
          );
        },
      ),
    );
  }
}

final class _ModeEditor extends StatelessWidget {
  const _ModeEditor({
    required this.mode,
    required this.copy,
    required this.enabled,
    required this.onChanged,
  });

  final String mode;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    const modes = ['monitor', 'ask_unknown', 'deny_unknown'];
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.fromLTRB(12, 9, 12, 10),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final description = Text(
            copy('network.rules.mode.$mode.detail'),
            style: Theme.of(context).textTheme.bodySmall,
          );
          if (constraints.maxWidth < 620) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('network.rules.mode'),
                  style: Theme.of(context).textTheme.labelMedium,
                ),
                const SizedBox(height: 6),
                CompactSelectField<String>(
                  key: const Key('rules-mode'),
                  initialValue: mode,
                  isExpanded: true,
                  items: [
                    for (final value in modes)
                      DropdownMenuItem(
                        value: value,
                        child: Text(copy('network.rules.mode.$value')),
                      ),
                  ],
                  onChanged: enabled ? (value) => onChanged(value!) : null,
                ),
                const SizedBox(height: 6),
                description,
              ],
            );
          }
          return Row(
            children: [
              SizedBox(
                width: 190,
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      copy('network.rules.mode'),
                      style: Theme.of(context).textTheme.labelMedium,
                    ),
                    const SizedBox(height: 4),
                    description,
                  ],
                ),
              ),
              const SizedBox(width: 14),
              CompactSegmentedControl<String>(
                key: const Key('rules-mode'),
                minSegmentWidth: 56,
                segments: [
                  for (final value in modes)
                    CompactSegment(
                      value: value,
                      label: copy('network.rules.mode.$value'),
                    ),
                ],
                selected: mode,
                onSelected: enabled ? onChanged : null,
              ),
            ],
          );
        },
      ),
    );
  }
}

final class _RuleConflictBanner extends StatelessWidget {
  const _RuleConflictBanner({
    required this.copy,
    required this.confirming,
    required this.onReload,
    required this.onCancel,
    required this.onConfirm,
  });

  final AppCopy copy;
  final bool confirming;
  final VoidCallback onReload;
  final VoidCallback onCancel;
  final VoidCallback onConfirm;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      color: context.viberColors.danger.withValues(alpha: 0.09),
      padding: const EdgeInsets.fromLTRB(12, 8, 10, 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.sync_problem, size: 15, color: context.viberColors.danger),
          const SizedBox(width: 7),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('network.rules.conflict'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
                Text(
                  copy('network.rules.conflict.detail'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
          const SizedBox(width: 7),
          if (!confirming)
            OutlinedButton(
              onPressed: onReload,
              child: Text(copy('common.reload')),
            )
          else
            Wrap(
              spacing: 5,
              children: [
                TextButton(
                  onPressed: onCancel,
                  child: Text(copy('common.cancel')),
                ),
                FilledButton(
                  key: const Key('rules-reload-confirm'),
                  onPressed: onConfirm,
                  child: Text(copy('common.confirm')),
                ),
              ],
            ),
        ],
      ),
    );
  }
}

final class _RuleSaveConfirmation extends StatelessWidget {
  const _RuleSaveConfirmation({
    required this.revision,
    required this.count,
    required this.busy,
    required this.copy,
    required this.onCancel,
    required this.onConfirm,
  });

  final int revision;
  final int count;
  final bool busy;
  final AppCopy copy;
  final VoidCallback onCancel;
  final VoidCallback onConfirm;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      liveRegion: true,
      container: true,
      child: Container(
        key: const Key('rules-save-confirmation'),
        width: double.infinity,
        color: context.viberColors.warning.withValues(alpha: 0.08),
        padding: const EdgeInsets.fromLTRB(12, 8, 10, 8),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(Icons.rule, size: 15, color: context.viberColors.warning),
            const SizedBox(width: 7),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    copy('network.rules.confirm.title'),
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                  Text(
                    copy.format('network.rules.confirm.detail', {
                      'revision': revision,
                      'count': count,
                    }),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ],
              ),
            ),
            const SizedBox(width: 7),
            Wrap(
              spacing: 5,
              children: [
                TextButton(
                  onPressed: busy ? null : onCancel,
                  child: Text(copy('common.cancel')),
                ),
                FilledButton(
                  key: const Key('rules-save-confirm'),
                  onPressed: busy ? null : onConfirm,
                  child: busy
                      ? const SizedBox.square(
                          dimension: 13,
                          child: CircularProgressIndicator(strokeWidth: 1.5),
                        )
                      : Text(copy('common.confirm')),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

final class _RuleRow extends StatelessWidget {
  const _RuleRow({
    required this.rule,
    required this.compact,
    required this.copy,
    required this.enabled,
    required this.onEdit,
    required this.onRemove,
  });

  final ConnectionRule rule;
  final bool compact;
  final AppCopy copy;
  final bool enabled;
  final VoidCallback onEdit;
  final VoidCallback onRemove;

  @override
  Widget build(BuildContext context) {
    final target = rule.port == null ? rule.host : '${rule.host}:${rule.port}';
    final actions = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        IconButton(
          key: Key('rule-edit-${rule.id}'),
          onPressed: enabled ? onEdit : null,
          tooltip: copy('network.rules.edit'),
          icon: const Icon(Icons.edit_outlined, size: 15),
        ),
        IconButton(
          key: Key('rule-remove-${rule.id}'),
          onPressed: enabled ? onRemove : null,
          tooltip: copy('network.rules.remove'),
          icon: const Icon(Icons.remove_circle_outline, size: 15),
        ),
      ],
    );
    if (compact) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(12, 8, 5, 8),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          rule.id,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                      ),
                      Text('P${rule.priority}', style: monoStyle),
                    ],
                  ),
                  Text(
                    target,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: monoStyle.copyWith(color: context.viberColors.text),
                  ),
                  Text(
                    '${copy('network.rules.decision.${rule.decision}')}  ·  ${copy('network.rules.match.${rule.match}')}',
                    style: Theme.of(context).textTheme.bodySmall,
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
      height: 42,
      child: Padding(
        padding: const EdgeInsets.only(left: 12, right: 5),
        child: Row(
          children: [
            SizedBox(
              width: 180,
              child: Text(rule.id, overflow: TextOverflow.ellipsis),
            ),
            Expanded(
              child: Text(
                target,
                overflow: TextOverflow.ellipsis,
                style: monoStyle.copyWith(color: context.viberColors.text),
              ),
            ),
            SizedBox(
              width: 90,
              child: Text(copy('network.rules.decision.${rule.decision}')),
            ),
            SizedBox(
              width: 150,
              child: Text(copy('network.rules.match.${rule.match}')),
            ),
            SizedBox(
              width: 62,
              child: Text('${rule.priority}', style: monoStyle),
            ),
            actions,
          ],
        ),
      ),
    );
  }
}

Future<ConnectionRule?> _showRuleEditor({
  required BuildContext context,
  required ConnectionRule? existing,
  required List<ConnectionRule> rules,
  required AppCopy copy,
}) => showDialog<ConnectionRule>(
  context: context,
  builder: (context) =>
      _RuleEditorDialog(existing: existing, rules: rules, copy: copy),
);

final class _RuleEditorDialog extends StatefulWidget {
  const _RuleEditorDialog({
    required this.existing,
    required this.rules,
    required this.copy,
  });

  final ConnectionRule? existing;
  final List<ConnectionRule> rules;
  final AppCopy copy;

  @override
  State<_RuleEditorDialog> createState() => _RuleEditorDialogState();
}

final class _RuleEditorDialogState extends State<_RuleEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _id;
  late final TextEditingController _priority;
  late final TextEditingController _host;
  late final TextEditingController _port;
  late String _decision;
  late String _match;

  @override
  void initState() {
    super.initState();
    final existing = widget.existing;
    final nextPriority = widget.rules.isEmpty
        ? 10
        : widget.rules
                  .map((rule) => rule.priority)
                  .reduce((a, b) => a > b ? a : b) +
              10;
    _id = TextEditingController(
      text: existing?.id ?? _nextRuleId(widget.rules),
    );
    _priority = TextEditingController(
      text: '${existing?.priority ?? nextPriority}',
    );
    _host = TextEditingController(text: existing?.host ?? '');
    _port = TextEditingController(text: existing?.port?.toString() ?? '443');
    _decision = existing?.decision ?? 'allow';
    _match = existing?.match ?? 'exact_host_port';
  }

  @override
  void dispose() {
    _id.dispose();
    _priority.dispose();
    _host.dispose();
    _port.dispose();
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
      title: Text(
        widget.existing == null
            ? copy('network.rules.add')
            : copy('network.rules.edit'),
      ),
      content: SizedBox(
        key: const Key('rule-editor-frame'),
        width: ViberMetrics.dialogStandardWidth,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: ResponsiveFormGrid(
              children: [
                CompactLabeledControl(
                  label: copy('network.rules.field.id'),
                  child: TextFormField(
                    key: const Key('rule-editor-id'),
                    controller: _id,
                    autofocus: true,
                    textAlignVertical: TextAlignVertical.center,
                    decoration: const InputDecoration(),
                    validator: (value) {
                      if (value == null ||
                          !RegExp(
                            r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$',
                          ).hasMatch(value) ||
                          const {
                            'mode.monitor',
                            'mode.ask_unknown',
                            'mode.deny_unknown',
                          }.contains(value)) {
                        return copy('network.rules.validation.id');
                      }
                      if (widget.rules.any(
                        (rule) =>
                            rule.id == value && rule.id != widget.existing?.id,
                      )) {
                        return copy('network.rules.validation.duplicate');
                      }
                      return null;
                    },
                  ),
                ),
                CompactLabeledControl(
                  label: copy('network.rules.field.priority'),
                  child: TextFormField(
                    key: const Key('rule-editor-priority'),
                    controller: _priority,
                    keyboardType: TextInputType.number,
                    textAlignVertical: TextAlignVertical.center,
                    decoration: const InputDecoration(),
                    validator: (value) {
                      final parsed = int.tryParse(value ?? '');
                      return parsed == null || parsed < 0 || parsed > 4294967295
                          ? copy('network.rules.validation.priority')
                          : null;
                    },
                  ),
                ),
                CompactLabeledControl(
                  label: copy('network.rules.field.decision'),
                  child: CompactSelectField<String>(
                    key: const Key('rule-editor-decision'),
                    initialValue: _decision,
                    isExpanded: true,
                    items: [
                      for (final value in const ['allow', 'deny', 'ask'])
                        DropdownMenuItem(
                          value: value,
                          child: Text(copy('network.rules.decision.$value')),
                        ),
                    ],
                    onChanged: (value) => setState(() => _decision = value!),
                  ),
                ),
                CompactLabeledControl(
                  label: copy('network.rules.field.match'),
                  child: CompactSelectField<String>(
                    key: const Key('rule-editor-match'),
                    initialValue: _match,
                    isExpanded: true,
                    items: [
                      for (final value in const [
                        'exact_host',
                        'exact_host_port',
                      ])
                        DropdownMenuItem(
                          value: value,
                          child: Text(copy('network.rules.match.$value')),
                        ),
                    ],
                    onChanged: (value) => setState(() => _match = value!),
                  ),
                ),
                CompactLabeledControl(
                  label: copy('network.rules.field.host'),
                  child: TextFormField(
                    key: const Key('rule-editor-host'),
                    controller: _host,
                    textAlignVertical: TextAlignVertical.center,
                    decoration: const InputDecoration(
                      hintText: 'api.example.com',
                    ),
                    validator: (value) => _validCanonicalHost(value ?? '')
                        ? null
                        : copy('network.rules.validation.host'),
                  ),
                ),
                if (_match == 'exact_host_port')
                  CompactLabeledControl(
                    label: copy('network.rules.field.port'),
                    child: TextFormField(
                      key: const Key('rule-editor-port'),
                      controller: _port,
                      keyboardType: TextInputType.number,
                      textAlignVertical: TextAlignVertical.center,
                      decoration: const InputDecoration(),
                      validator: (value) {
                        final parsed = int.tryParse(value ?? '');
                        return parsed == null || parsed < 1 || parsed > 65535
                            ? copy('network.rules.validation.port')
                            : null;
                      },
                    ),
                  ),
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('rule-editor-save'),
          onPressed: _submit,
          child: Text(copy('common.save')),
        ),
      ],
    );
  }

  void _submit() {
    if (!_formKey.currentState!.validate()) return;
    Navigator.pop(
      context,
      ConnectionRule(
        id: _id.text,
        priority: int.parse(_priority.text),
        decision: _decision,
        match: _match,
        host: _host.text,
        port: _match == 'exact_host_port' ? int.parse(_port.text) : null,
      ),
    );
  }
}

String _nextRuleId(List<ConnectionRule> rules) {
  var index = rules.length + 1;
  while (rules.any((rule) => rule.id == 'rule-$index')) {
    index += 1;
  }
  return 'rule-$index';
}

bool _sameRules(List<ConnectionRule> left, List<ConnectionRule> right) {
  if (left.length != right.length) return false;
  final leftSorted = List.of(left)..sort((a, b) => a.id.compareTo(b.id));
  final rightSorted = List.of(right)..sort((a, b) => a.id.compareTo(b.id));
  for (var index = 0; index < leftSorted.length; index += 1) {
    final a = leftSorted[index];
    final b = rightSorted[index];
    if (a.id != b.id ||
        a.priority != b.priority ||
        a.decision != b.decision ||
        a.match != b.match ||
        a.host != b.host ||
        a.port != b.port) {
      return false;
    }
  }
  return true;
}

bool _validCanonicalHost(String value) =>
    value.isNotEmpty &&
    value.length <= 253 &&
    value == value.toLowerCase() &&
    !value.contains(RegExp(r'[\s/*?]')) &&
    !value.startsWith('.') &&
    !value.endsWith('.');

String _connectionSearchText(ConnectionRecord record, AppCopy copy) =>
    <String?>[
      record.sourceLabel,
      record.sourceConfidence,
      record.environmentId,
      record.environmentName,
      record.requestedHost,
      record.observedSni,
      record.routeHost,
      record.ip,
      record.decision,
      record.ruleId,
      record.egressScope,
      record.egressSource,
      record.decryption,
      record.phase,
      record.outcome,
      record.errorClass,
      if (record.decision != null)
        _localizedWire(copy, 'network.value.decision', record.decision!),
      _localizedWire(copy, 'network.value.phase', record.phase),
    ].whereType<String>().join(' ').toLowerCase();

String _egressSearchText(EgressAttemptRecord record, AppCopy copy) => <String?>[
  record.id,
  record.connectionId,
  record.purpose,
  record.payloadClass,
  record.parentKind,
  record.parentId,
  record.exchangeId,
  record.caller,
  record.callerId,
  record.targetOrigin,
  record.authority,
  record.policyId,
  record.ruleId,
  record.proxyId,
  record.outcome,
  record.errorClass,
  _localizedWire(copy, 'network.value.purpose', record.purpose),
  _localizedWire(copy, 'network.value.caller', record.caller),
  _localizedWire(copy, 'network.value.authority', record.authority),
  if (record.outcome != null)
    _localizedWire(copy, 'network.value.outcome', record.outcome!),
].whereType<String>().join(' ').toLowerCase();

String _clock(DateTime value) {
  final local = value.toLocal();
  return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}';
}

String _bytes(int value) {
  if (value < 1024) return '$value B';
  if (value < 1024 * 1024) return '${(value / 1024).toStringAsFixed(1)} KiB';
  return '${(value / (1024 * 1024)).toStringAsFixed(1)} MiB';
}

String _humanize(String value) {
  if (value.isEmpty) return value;
  final words = value.replaceAll('-', ' ').replaceAll('_', ' ');
  return '${words[0].toUpperCase()}${words.substring(1)}';
}

String _localizedWire(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  return copy.maybe(key) ?? _humanize(value);
}

Color _decisionColor(BuildContext context, String? decision) =>
    switch (decision) {
      'allow' => context.viberColors.verified,
      'deny' => context.viberColors.danger,
      'ask' => context.viberColors.warning,
      _ => context.viberColors.textFaint,
    };

Color _outcomeColor(BuildContext context, String? outcome) => switch (outcome) {
  'completed' => context.viberColors.verified,
  'failed' => context.viberColors.danger,
  'canceled' => context.viberColors.warning,
  _ => context.viberColors.route,
};

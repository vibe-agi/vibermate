import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

enum _ActivityMetric { agentApiCalls, tokens }

/// Operator-facing projection of the Runtime's body-free usage ledger.
///
/// This view deliberately reports only protocol-declared usage. Unknown token
/// values stay unknown and are rendered with an em dash or a lower-bound mark.
final class UsageDashboardView extends StatefulWidget {
  const UsageDashboardView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<UsageDashboardView> createState() => _UsageDashboardViewState();
}

final class _UsageDashboardViewState extends State<UsageDashboardView> {
  String? _selectedUserId;
  String _userQuery = '';
  _ActivityMetric _activityMetric = _ActivityMetric.agentApiCalls;
  String _groupBy = 'profiles';
  final GlobalKey _detailKey = GlobalKey();

  RuntimeUserUsage? _selectedUser(RuntimeUsageReport report) {
    if (report.users.isEmpty) return null;
    for (final user in report.users) {
      if (user.userId == _selectedUserId) return user;
    }
    final ordered = _rankedUsers(report.users);
    return ordered.first;
  }

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final report = controller.runtimeUsage;
    return Column(
      key: const Key('usage-dashboard'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        PageHeading(
          title: copy('usage.title'),
          help: copy('usage.subtitle'),
          dismissHelpLabel: copy('common.dismiss'),
        ),
        const Divider(height: 1),
        Expanded(
          child: switch (report) {
            null when controller.usageLoading => Center(
              child: CompactLoadingMessage(label: copy('usage.loading')),
            ),
            null => _UsageUnavailable(
              copy: copy,
              detail: controller.usageError,
              onRetry: () => unawaited(controller.refreshUsage()),
            ),
            final value => _UsageReportBody(
              controller: controller,
              groupBy: _groupBy,
              onGroupChanged: (value) => setState(() => _groupBy = value),
              report: value,
              selected: _selectedUser(value),
              copy: copy,
              refreshing: controller.usageLoading,
              onRefresh: () => unawaited(controller.refreshUsage()),
              activityMetric: _activityMetric,
              onActivityMetricChanged: (value) => setState(() {
                _activityMetric = value;
              }),
              onSelectUser: _selectUser,
              detailKey: _detailKey,
              userQuery: _userQuery,
              onUserQueryChanged: (value) => setState(() {
                _userQuery = value;
              }),
            ),
          },
        ),
      ],
    );
  }

  void _selectUser(String userId) {
    setState(() {
      _selectedUserId = userId;
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final target = _detailKey.currentContext;
      if (target == null) return;
      unawaited(
        Scrollable.ensureVisible(
          target,
          alignment: 0.04,
          duration: const Duration(milliseconds: 180),
          curve: Curves.easeOutCubic,
        ),
      );
    });
  }
}

/// Member-facing projection. The Server has already scoped [report] to the
/// signed-in Runtime User; this view deliberately contains no team ranking or
/// management affordance.
final class PersonalUsageDashboard extends StatefulWidget {
  const PersonalUsageDashboard({
    required this.report,
    required this.loading,
    required this.error,
    required this.onRefresh,
    required this.copy,
    this.rangeDays = 7,
    this.onRangeChanged,
    super.key,
  });

  final RuntimeUsageReport? report;
  final bool loading;
  final String? error;
  final VoidCallback onRefresh;
  final AppCopy copy;
  final int rangeDays;
  final ValueChanged<int>? onRangeChanged;

  @override
  State<PersonalUsageDashboard> createState() => _PersonalUsageDashboardState();
}

final class _PersonalUsageDashboardState extends State<PersonalUsageDashboard> {
  _ActivityMetric _metric = _ActivityMetric.agentApiCalls;
  String _groupBy = 'profiles';

  @override
  Widget build(BuildContext context) {
    final report = widget.report;
    return Column(
      key: const Key('personal-usage-dashboard'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        PageHeading(
          title: widget.copy('usage.personal.title'),
          help: widget.copy('usage.personal.subtitle'),
          dismissHelpLabel: widget.copy('common.dismiss'),
        ),
        const Divider(height: 1),
        Expanded(
          child: report == null
              ? widget.loading
                    ? Center(
                        child: CompactLoadingMessage(
                          label: widget.copy('usage.loading'),
                        ),
                      )
                    : _UsageUnavailable(
                        copy: widget.copy,
                        detail: widget.error,
                        onRetry: widget.onRefresh,
                      )
              : _body(report),
        ),
      ],
    );
  }

  Widget _body(RuntimeUsageReport report) {
    final user = report.users.firstOrNull;
    return SingleChildScrollView(
      key: const Key('personal-usage-scroll'),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 24),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (widget.onRangeChanged != null) ...[
            Align(
              alignment: Alignment.centerLeft,
              child: SegmentedButton<int>(
                showSelectedIcon: false,
                segments: [
                  for (final days in [7, 30, 90, 365])
                    ButtonSegment(
                      value: days,
                      label: Text(
                        widget.copy.format('usage.range', {'days': '$days'}),
                      ),
                    ),
                ],
                selected: {widget.rangeDays},
                onSelectionChanged: widget.loading
                    ? null
                    : (value) => widget.onRangeChanged!(value.single),
              ),
            ),
            const SizedBox(height: 12),
          ],
          _ReportScope(
            report: report,
            copy: widget.copy,
            refreshing: widget.loading,
            onRefresh: widget.onRefresh,
          ),
          if (widget.error != null)
            InlineNotice(message: widget.error!, error: true),
          if (!report.collection.enabled) ...[
            const SizedBox(height: 8),
            InlineNotice(message: widget.copy('usage.personal.collection_off')),
          ],
          if (report.truncated) ...[
            const SizedBox(height: ViberSpacing.md),
            InlineNotice(message: widget.copy('server.usage.truncated')),
          ],
          const SizedBox(height: ViberSpacing.lg),
          if (user == null)
            _PersonalUsageEmpty(copy: widget.copy)
          else ...[
            _UsageOverview(report: report, copy: widget.copy),
            _PricingNote(report: report, copy: widget.copy),
            const SizedBox(height: ViberSpacing.lg),
            _UsageTrend(report: report, copy: widget.copy),
            const SizedBox(height: ViberSpacing.lg),
            _UsageGroupTable(
              report: report,
              groupBy: _groupBy,
              onChanged: (value) => setState(() => _groupBy = value),
              copy: widget.copy,
            ),
            const SizedBox(height: ViberSpacing.lg),
            ExpansionTile(
              key: const Key('personal-usage-details'),
              title: Text(widget.copy('usage.personal.details')),
              children: [
                SegmentedButton<_ActivityMetric>(
                  segments: [
                    ButtonSegment(
                      value: _ActivityMetric.agentApiCalls,
                      label: Text(
                        widget.copy('usage.activity.metric.api_calls'),
                      ),
                    ),
                    ButtonSegment(
                      value: _ActivityMetric.tokens,
                      label: Text(widget.copy('usage.activity.metric.tokens')),
                    ),
                  ],
                  selected: {_metric},
                  showSelectedIcon: false,
                  onSelectionChanged: (value) => setState(() {
                    _metric = value.single;
                  }),
                ),
                const SizedBox(height: ViberSpacing.md),
                _UserEvidence(
                  user: user,
                  period: report.period,
                  metric: _metric,
                  copy: widget.copy,
                ),
              ],
            ),
          ],
        ],
      ),
    );
  }
}

final class _PersonalUsageEmpty extends StatelessWidget {
  const _PersonalUsageEmpty({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) =>
      InlineNotice(message: copy('usage.personal.empty'));
}

final class _UsageUnavailable extends StatelessWidget {
  const _UsageUnavailable({
    required this.copy,
    required this.detail,
    required this.onRetry,
  });

  final AppCopy copy;
  final String? detail;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) => Center(
    child: ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 420),
      child: Padding(
        padding: const EdgeInsets.all(ViberSpacing.xl),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              Icons.query_stats_outlined,
              size: 30,
              color: context.viberColors.textMuted,
            ),
            const SizedBox(height: ViberSpacing.md),
            Text(
              copy('usage.unavailable'),
              textAlign: TextAlign.center,
              style: Theme.of(context).textTheme.titleMedium,
            ),
            if (detail != null) ...[
              const SizedBox(height: ViberSpacing.sm),
              Text(
                detail!,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
            const SizedBox(height: ViberSpacing.md),
            OutlinedButton.icon(
              onPressed: onRetry,
              icon: const Icon(Icons.refresh, size: 15),
              label: Text(copy('common.retry')),
            ),
          ],
        ),
      ),
    ),
  );
}

final class _UsageReportBody extends StatelessWidget {
  const _UsageReportBody({
    required this.report,
    required this.selected,
    required this.copy,
    required this.refreshing,
    required this.onRefresh,
    required this.activityMetric,
    required this.onActivityMetricChanged,
    required this.onSelectUser,
    required this.detailKey,
    required this.userQuery,
    required this.onUserQueryChanged,
    required this.controller,
    required this.groupBy,
    required this.onGroupChanged,
  });

  final RuntimeUsageReport report;
  final RuntimeUserUsage? selected;
  final AppCopy copy;
  final bool refreshing;
  final VoidCallback onRefresh;
  final _ActivityMetric activityMetric;
  final ValueChanged<_ActivityMetric> onActivityMetricChanged;
  final ValueChanged<String> onSelectUser;
  final Key detailKey;
  final String userQuery;
  final ValueChanged<String> onUserQueryChanged;
  final WorkbenchController controller;
  final String groupBy;
  final ValueChanged<String> onGroupChanged;

  @override
  Widget build(BuildContext context) => SingleChildScrollView(
    key: const Key('usage-dashboard-scroll'),
    padding: const EdgeInsets.fromLTRB(14, 12, 14, 24),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Wrap(
          spacing: 12,
          runSpacing: 8,
          alignment: WrapAlignment.spaceBetween,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            SegmentedButton<int>(
              key: const Key('usage-range'),
              showSelectedIcon: false,
              segments: [
                for (final days in [7, 30, 90, 365])
                  ButtonSegment(
                    value: days,
                    label: Text(copy.format('usage.range', {'days': '$days'})),
                  ),
              ],
              selected: {controller.usageRangeDays},
              onSelectionChanged: refreshing
                  ? null
                  : (value) =>
                        unawaited(controller.setUsageRange(value.single)),
            ),
            TextButton.icon(
              key: const Key('usage-collection-settings'),
              onPressed: controller.runtimeUserMutating
                  ? null
                  : () => unawaited(
                      _editUsageCollection(context, controller, copy),
                    ),
              icon: const Icon(Icons.tune, size: 16),
              label: Text(copy('usage.collection.settings')),
            ),
          ],
        ),
        const SizedBox(height: 12),
        _ReportScope(
          report: report,
          copy: copy,
          refreshing: refreshing,
          onRefresh: onRefresh,
        ),
        const SizedBox(height: 8),
        Text(
          report.collection.enabled
              ? copy.format('usage.collection.since', {
                  'time': _timestamp(report.collection.collectingSince!),
                  'days': '${report.collection.retentionDays}',
                })
              : copy('usage.collection.disabled'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        if (controller.usageError != null)
          InlineNotice(
            message:
                '${copy('usage.refresh.failed')} ${controller.usageError!}',
            error: true,
          ),
        if (report.truncated) ...[
          const SizedBox(height: ViberSpacing.md),
          InlineNotice(message: copy('server.usage.truncated'), error: true),
        ],
        const SizedBox(height: ViberSpacing.lg),
        _UsageOverview(report: report, copy: copy),
        _PricingNote(report: report, copy: copy),
        const SizedBox(height: ViberSpacing.lg),
        _UsageTrend(
          report: report,
          copy: copy,
          annual: controller.usageRangeDays == 365
              ? _TeamActivityPanel(
                  report: report,
                  metric: activityMetric,
                  copy: copy,
                  onMetricChanged: onActivityMetricChanged,
                )
              : null,
        ),
        const SizedBox(height: ViberSpacing.xl),
        _UsageGroupTable(
          report: report,
          groupBy: groupBy,
          onChanged: onGroupChanged,
          copy: copy,
        ),
        if (report.users.any((user) => user.agentApiCalls > 0)) ...[
          const SizedBox(height: 16),
          ExpansionTile(
            key: const Key('usage-member-details'),
            tilePadding: EdgeInsets.zero,
            title: Text(copy('usage.members')),
            children: [
              _UserLedger(
                users: _rankedUsers(report.users),
                selectedUserId: selected?.userId,
                query: userQuery,
                copy: copy,
                metric: activityMetric,
                period: report.period,
                onQueryChanged: onUserQueryChanged,
                onSelectUser: onSelectUser,
              ),
              if (selected != null) ...[
                const SizedBox(height: ViberSpacing.xl),
                _UserEvidence(
                  key: detailKey,
                  user: selected!,
                  period: report.period,
                  metric: activityMetric,
                  copy: copy,
                ),
              ],
            ],
          ),
        ],
      ],
    ),
  );
}

Future<void> _editUsageCollection(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
) async {
  final policy = controller.runtimeUsage!.collection;
  var enabled = policy.enabled;
  var days = policy.retentionDays;
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => StatefulBuilder(
      builder: (context, setState) => AlertDialog(
        title: Text(copy('usage.collection.settings')),
        content: SizedBox(
          width: 480,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(copy('usage.collection.explanation')),
              const SizedBox(height: 16),
              SwitchListTile.adaptive(
                contentPadding: EdgeInsets.zero,
                title: Text(copy('usage.collection.enable')),
                value: enabled,
                onChanged: (value) => setState(() => enabled = value),
              ),
              const SizedBox(height: 12),
              DropdownButtonFormField<int>(
                key: const Key('usage-retention'),
                initialValue: days,
                decoration: InputDecoration(
                  labelText: copy('usage.collection.retention'),
                  border: const OutlineInputBorder(),
                ),
                items: [
                  for (final value in ({7, 30, 90, 365, days}.toList()..sort()))
                    DropdownMenuItem(
                      value: value,
                      child: Text(
                        copy.format('usage.range', {'days': '$value'}),
                      ),
                    ),
                ],
                onChanged: (value) {
                  if (value != null) setState(() => days = value);
                },
              ),
              const SizedBox(height: 12),
              Text(
                copy('usage.collection.retention_hint'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              if (days < policy.retentionDays) ...[
                const SizedBox(height: 12),
                InlineNotice(
                  message: copy('usage.collection.shorter'),
                  error: true,
                ),
              ],
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: Text(copy('common.save')),
          ),
        ],
      ),
    ),
  );
  if (confirmed == true) {
    await controller.setUsageCollection(enabled: enabled, retentionDays: days);
  }
}

final class _UsageTrend extends StatefulWidget {
  const _UsageTrend({required this.report, required this.copy, this.annual});
  final RuntimeUsageReport report;
  final AppCopy copy;
  final Widget? annual;
  @override
  State<_UsageTrend> createState() => _UsageTrendState();
}

final class _UsageTrendState extends State<_UsageTrend> {
  bool _cost = false;
  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Align(
        alignment: Alignment.centerRight,
        child: SegmentedButton<bool>(
          key: const Key('usage-trend-metric'),
          showSelectedIcon: false,
          segments: [
            ButtonSegment(
              value: false,
              label: Text(widget.copy('usage.trend.requests')),
            ),
            ButtonSegment(
              value: true,
              label: Text(widget.copy('usage.cost.short')),
            ),
          ],
          selected: {_cost},
          onSelectionChanged: (value) => setState(() => _cost = value.single),
        ),
      ),
      const SizedBox(height: 8),
      if (!_cost && widget.annual != null)
        widget.annual!
      else
        _UsageDailyBars(report: widget.report, copy: widget.copy, cost: _cost),
    ],
  );
}

final class _UsageChartPoint {
  _UsageChartPoint(this.date);
  final String date;
  int calls = 0;
  int failed = 0;
  RuntimeCostEstimate cost = const RuntimeCostEstimate();
}

final class _UsageDailyBars extends StatelessWidget {
  const _UsageDailyBars({
    required this.report,
    required this.copy,
    this.cost = false,
  });
  final RuntimeUsageReport report;
  final AppCopy copy;
  final bool cost;

  @override
  Widget build(BuildContext context) {
    final dates = _periodDates(report.period);
    final monthly = dates.length > 90;
    String bucket(String date) => monthly ? date.substring(0, 7) : date;
    final byDate = <String, _UsageChartPoint>{};
    for (final date in dates) {
      final key = bucket(date.toIso8601String().substring(0, 10));
      byDate.putIfAbsent(key, () => _UsageChartPoint(key));
    }
    for (final day in report.days) {
      final point = byDate[bucket(day.date)]!;
      point.calls += day.agentApiCalls;
      point.failed += day.failed;
      point.cost = point.cost.add(
        day.cost ?? RuntimeCostEstimate(unpricedCalls: day.agentApiCalls),
      );
    }
    final points = byDate.values.toList(growable: false);
    final peak = points.fold<int>(
      1,
      (value, point) =>
          math.max(value, cost ? point.cost.nanoUsd : point.calls),
    );
    final colors = context.viberColors;
    return Container(
      key: const Key('usage-daily-trend'),
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        border: Border.all(color: colors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            cost
                ? copy(monthly ? 'usage.cost.monthly' : 'usage.cost.daily')
                : copy(monthly ? 'usage.trend.monthly' : 'usage.trend'),
            style: Theme.of(context).textTheme.titleSmall,
          ),
          const SizedBox(height: 8),
          if (report.days.isEmpty ||
              (cost && _reportCost(report).pricedCalls == 0))
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 24),
              child: Text(
                copy(
                  report.days.isEmpty
                      ? 'usage.empty.window'
                      : 'usage.cost.no_priced',
                ),
                style: TextStyle(color: colors.textMuted),
              ),
            )
          else
            SizedBox(
              height: 132,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  for (final point in points)
                    Expanded(
                      child: Builder(
                        builder: (context) {
                          final key = point.date;
                          final count = cost ? point.cost.nanoUsd : point.calls;
                          final failed = cost ? 0 : point.failed;
                          final label = point.calls == 0
                              ? '$key · ${copy('usage.empty.window')}'
                              : cost
                              ? '$key · ${_costLabel(point.cost)} USD · ${_costCoverage(point.cost, copy)}'
                              : '$key · $count ${copy('usage.metric.api_calls')} · $failed ${copy('usage.failed')}';
                          return Tooltip(
                            message: label,
                            child: Semantics(
                              label: label,
                              child: Padding(
                                padding: EdgeInsets.symmetric(
                                  horizontal: points.length > 30 ? 1 : 4,
                                ),
                                child: Column(
                                  mainAxisAlignment: MainAxisAlignment.end,
                                  children: [
                                    if (points.length <= 7)
                                      SizedBox(
                                        height: 16,
                                        child: LayoutBuilder(
                                          builder: (context, constraints) =>
                                              cost && constraints.maxWidth < 60
                                              ? const SizedBox.shrink()
                                              : Text(
                                                  point.calls == 0
                                                      ? '—'
                                                      : cost
                                                      ? _costLabel(point.cost)
                                                      : '$count',
                                                  style: Theme.of(
                                                    context,
                                                  ).textTheme.labelSmall,
                                                  maxLines: 1,
                                                  overflow:
                                                      TextOverflow.ellipsis,
                                                ),
                                        ),
                                      ),
                                    const SizedBox(height: 4),
                                    Container(
                                      height: math.max(2, 90.0 * count / peak),
                                      constraints: const BoxConstraints(
                                        maxWidth: 48,
                                      ),
                                      decoration: BoxDecoration(
                                        color: colors.route.withValues(
                                          alpha: .7,
                                        ),
                                        borderRadius: BorderRadius.circular(3),
                                      ),
                                      child: Align(
                                        alignment: Alignment.topCenter,
                                        child: FractionallySizedBox(
                                          heightFactor: count == 0
                                              ? 0
                                              : failed / count,
                                          child: Container(
                                            color: Theme.of(
                                              context,
                                            ).colorScheme.error,
                                          ),
                                        ),
                                      ),
                                    ),
                                    const SizedBox(height: 6),
                                    SizedBox(
                                      height: 16,
                                      child: points.length <= 13
                                          ? FittedBox(
                                              fit: BoxFit.scaleDown,
                                              child: Text(
                                                key.substring(5),
                                                style: Theme.of(
                                                  context,
                                                ).textTheme.labelSmall,
                                                maxLines: 1,
                                              ),
                                            )
                                          : null,
                                    ),
                                  ],
                                ),
                              ),
                            ),
                          );
                        },
                      ),
                    ),
                ],
              ),
            ),
          if (points.length > 13 && report.days.isNotEmpty)
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Text(report.period.from),
                Text(dates.last.toIso8601String().substring(0, 10)),
              ],
            ),
        ],
      ),
    );
  }
}

final class _UsageGroupTable extends StatefulWidget {
  const _UsageGroupTable({
    required this.report,
    required this.groupBy,
    required this.onChanged,
    required this.copy,
  });
  final RuntimeUsageReport report;
  final String groupBy;
  final ValueChanged<String> onChanged;
  final AppCopy copy;

  @override
  State<_UsageGroupTable> createState() => _UsageGroupTableState();
}

final class _UsageGroupTableState extends State<_UsageGroupTable> {
  final Set<String> _expanded = {};
  final _horizontalScroll = ScrollController();

  @override
  void dispose() {
    _horizontalScroll.dispose();
    super.dispose();
  }

  @override
  void didUpdateWidget(covariant _UsageGroupTable oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.groupBy != widget.groupBy && _horizontalScroll.hasClients) {
      _horizontalScroll.jumpTo(0);
    }
  }

  List<({RuntimeUsageGroup group, int depth, String path})> _rows(
    List<RuntimeUsageGroup> groups,
    String parent, [
    int depth = 0,
  ]) => [
    for (final group in groups) ...[
      (
        group: group,
        depth: depth,
        path: '$parent/${Uri.encodeComponent(group.id)}',
      ),
      if (_expanded.contains('$parent/${Uri.encodeComponent(group.id)}'))
        ..._rows(
          group.children,
          '$parent/${Uri.encodeComponent(group.id)}',
          depth + 1,
        ),
    ],
  ];

  String _label(RuntimeUsageGroup group) {
    if (group.dimension == 'source') {
      return widget.copy('usage.source.${group.id}');
    }
    if (group.evidence == 'detached') return 'Detached HEAD';
    if (group.label.isNotEmpty) return group.label;
    if (group.id.isEmpty || group.dimension == 'caller') {
      return widget.copy('usage.unknown');
    }
    return group.id;
  }

  DataRow _row(
    BuildContext context,
    RuntimeUsageGroup group, {
    required String path,
    int depth = 0,
    bool total = false,
  }) {
    final copy = widget.copy;
    final cost =
        group.cost ?? RuntimeCostEstimate(unpricedCalls: group.agentApiCalls);
    final name = total ? copy('usage.total') : _label(group);
    final expanded = _expanded.contains(path);
    final kind = group.evidence == 'local'
        ? copy('usage.caller.local')
        : group.evidence == 'member'
        ? copy('usage.caller.member')
        : group.dimension == 'branch'
        ? copy('usage.branch.launch')
        : '';
    return DataRow(
      key: ValueKey('usage-row-$path'),
      color: total || depth > 0
          ? WidgetStatePropertyAll(
              context.viberColors.panel.withValues(alpha: total ? 1 : .6),
            )
          : null,
      cells: [
        DataCell(
          SizedBox(
            width: 290,
            child: Padding(
              padding: EdgeInsets.only(left: depth * 18),
              child: Row(
                children: [
                  if (group.children.isNotEmpty)
                    IconButton(
                      key: Key('usage-expand-$path'),
                      tooltip:
                          '${copy(expanded ? 'usage.collapse' : 'usage.expand')} $name',
                      constraints: const BoxConstraints.tightFor(
                        width: 28,
                        height: 32,
                      ),
                      padding: EdgeInsets.zero,
                      onPressed: () => setState(() {
                        expanded ? _expanded.remove(path) : _expanded.add(path);
                      }),
                      icon: Icon(
                        expanded ? Icons.expand_more : Icons.chevron_right,
                        size: 18,
                      ),
                    )
                  else
                    const SizedBox(width: 28),
                  if (depth > 0) ...[
                    Icon(
                      switch (group.dimension) {
                        'caller' => Icons.person_outline,
                        'branch' => Icons.call_split,
                        _ => Icons.memory_outlined,
                      },
                      size: 14,
                      color: context.viberColors.textFaint,
                    ),
                    const SizedBox(width: 6),
                  ],
                  Expanded(
                    child: Tooltip(
                      message:
                          '$name${group.id.isEmpty || total ? '' : '\n${group.id}'}',
                      child: Column(
                        mainAxisAlignment: MainAxisAlignment.center,
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            name,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                              fontWeight: total || depth == 0
                                  ? FontWeight.w600
                                  : FontWeight.w400,
                            ),
                          ),
                          if (kind.isNotEmpty || group.childrenTruncated)
                            Text(
                              group.childrenTruncated
                                  ? copy('usage.children.truncated')
                                  : kind,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.bodySmall
                                  ?.copyWith(
                                    color: context.viberColors.textFaint,
                                  ),
                            ),
                        ],
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
        DataCell(Text(_reportInteger(group.agentApiCalls))),
        DataCell(Text(_reportInteger(group.failed))),
        for (final value in [
          group.tokens.inputUncached,
          group.tokens.cacheRead,
          group.tokens.output,
        ])
          DataCell(
            Tooltip(
              message: copy('usage.tokens.hint'),
              child: Text(
                value.observed
                    ? '${value.complete ? '' : '≥ '}${_reportInteger(value.tokens)}'
                    : '—',
              ),
            ),
          ),
        DataCell(
          Tooltip(
            message: '${_costCoverage(cost, copy)}\n${_costLabel(cost)}',
            child: Text(
              _tableCostLabel(cost),
              style: TextStyle(
                fontWeight: FontWeight.w600,
                color: context.viberColors.route,
              ),
            ),
          ),
        ),
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final report = widget.report;
    final groupBy = widget.groupBy;
    final copy = widget.copy;
    final groups = switch (groupBy) {
      'sources' => report.sources,
      'accounts' => report.accounts,
      'models' => report.models,
      'callers' => report.callers,
      'projects' => report.projects,
      _ => report.profiles,
    };
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            for (final key in [
              'profiles',
              'accounts',
              'models',
              'sources',
              'callers',
              'projects',
            ])
              ChoiceChip(
                key: Key('usage-group-$key'),
                label: Text(copy('usage.group.$key')),
                selected: key == groupBy,
                onSelected: (_) => widget.onChanged(key),
              ),
          ],
        ),
        const SizedBox(height: 12),
        Text(
          copy('usage.path.$groupBy'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        const SizedBox(height: 8),
        if (groups.isEmpty)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 16),
            child: Text(
              copy('usage.empty.window'),
              style: TextStyle(color: context.viberColors.textMuted),
            ),
          )
        else
          LayoutBuilder(
            builder: (context, constraints) => Scrollbar(
              controller: _horizontalScroll,
              thumbVisibility: true,
              child: SingleChildScrollView(
                key: const Key('usage-breakdown-scroll'),
                controller: _horizontalScroll,
                scrollDirection: Axis.horizontal,
                child: ConstrainedBox(
                  constraints: BoxConstraints(
                    minWidth: math.max(1000, constraints.maxWidth),
                  ),
                  child: DataTable(
                    key: const Key('usage-breakdown-table'),
                    headingRowHeight: 38,
                    dataRowMinHeight: 44,
                    dataRowMaxHeight: 52,
                    horizontalMargin: 12,
                    columnSpacing: 24,
                    dataTextStyle: Theme.of(context).textTheme.bodyMedium
                        ?.copyWith(
                          fontFeatures: const [FontFeature.tabularFigures()],
                        ),
                    columns: [
                      DataColumn(label: Text(copy('usage.group.$groupBy'))),
                      for (final label in [
                        'usage.metric.api_calls',
                        'usage.failed',
                        'usage.table.input',
                        'usage.cache_read',
                        'usage.table.output',
                        'usage.table.cost',
                      ])
                        DataColumn(numeric: true, label: Text(copy(label))),
                    ],
                    rows: [
                      for (final row in _rows(groups, groupBy))
                        _row(
                          context,
                          row.group,
                          path: row.path,
                          depth: row.depth,
                        ),
                      if (report.total case final total?)
                        _row(
                          context,
                          total,
                          path: '$groupBy#total',
                          total: true,
                        ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        const SizedBox(height: 8),
        Text(
          copy('usage.tokens.hint'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textFaint),
        ),
        if (groupBy == 'projects' ||
            groupBy == 'callers' ||
            groupBy == 'models') ...[
          const SizedBox(height: 4),
          Text(
            copy(
              groupBy == 'projects'
                  ? 'usage.projects.hint'
                  : 'usage.callers.hint',
            ),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ],
      ],
    );
  }
}

String _reportInteger(int value) =>
    '$value'.replaceAllMapped(RegExp(r'\B(?=(\d{3})+(?!\d))'), (_) => ',');

String _tableCostLabel(RuntimeCostEstimate cost) {
  if (cost.pricedCalls == 0) return '—';
  if (!cost.partial && cost.nanoUsd > 0 && cost.nanoUsd < 10000000) {
    return '< \$0.01';
  }
  final cents = cost.partial
      ? cost.nanoUsd ~/ 10000000
      : (cost.nanoUsd + 5000000) ~/ 10000000;
  return '${cost.partial ? '≥ ' : ''}\$${_reportInteger(cents ~/ 100)}.${(cents % 100).toString().padLeft(2, '0')}';
}

final class _ReportScope extends StatelessWidget {
  const _ReportScope({
    required this.report,
    required this.copy,
    required this.refreshing,
    required this.onRefresh,
  });

  final RuntimeUsageReport report;
  final AppCopy copy;
  final bool refreshing;
  final VoidCallback onRefresh;

  @override
  Widget build(BuildContext context) => Row(
    children: [
      Icon(
        Icons.inventory_2_outlined,
        size: 16,
        color: context.viberColors.route,
      ),
      const SizedBox(width: ViberSpacing.sm),
      Expanded(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('usage.scope.retained'),
              style: Theme.of(context).textTheme.labelLarge,
            ),
            Text(
              '${_periodLabel(report.period)} · '
              '${copy.format('usage.generated', {'time': _timestamp(report.generatedAt, seconds: true)})}',
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
        ),
      ),
      IconButton(
        key: const Key('usage-refresh'),
        onPressed: refreshing ? null : onRefresh,
        tooltip: copy('usage.refresh.hint'),
        icon: refreshing
            ? const CompactProgressIndicator()
            : const Icon(Icons.refresh, size: 16),
      ),
    ],
  );
}

final class _UsageOverview extends StatelessWidget {
  const _UsageOverview({required this.report, required this.copy});

  final RuntimeUsageReport report;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final succeeded = report.days.fold<int>(
      0,
      (sum, day) => sum + day.succeeded,
    );
    final failed = report.days.fold<int>(0, (sum, day) => sum + day.failed);
    final canceled = report.days.fold<int>(0, (sum, day) => sum + day.canceled);
    final agentApiCalls = report.days.fold<int>(
      0,
      (sum, day) => sum + day.agentApiCalls,
    );
    final input = _sumDayTokens(report.days, (tokens) => tokens.inputUncached);
    final output = _sumDayTokens(report.days, (tokens) => tokens.output);
    return LayoutBuilder(
      builder: (context, constraints) {
        final columns = constraints.maxWidth >= 1100
            ? 6
            : constraints.maxWidth >= 560
            ? 3
            : 2;
        final spacing = ViberSpacing.md * (columns - 1);
        final width = (constraints.maxWidth - spacing) / columns;
        return Wrap(
          spacing: ViberSpacing.md,
          runSpacing: ViberSpacing.md,
          children: [
            _OverviewCard(
              width: width,
              key: const Key('usage-total-api-calls'),
              icon: Icons.swap_horiz,
              label: copy('usage.metric.api_calls'),
              value: _integer(agentApiCalls),
              detail: copy('usage.metric.api_calls.detail'),
            ),
            _OverviewCard(
              width: width,
              icon: Icons.check_circle_outline,
              label: copy('usage.succeeded'),
              value: _integer(succeeded),
              detail: agentApiCalls == 0
                  ? '—'
                  : '${(100 * succeeded / agentApiCalls).toStringAsFixed(1)}%',
              accent: context.viberColors.verified,
            ),
            _OverviewCard(
              width: width,
              icon: Icons.error_outline,
              label: copy('usage.failed'),
              value: _integer(failed),
              detail: copy.format('usage.canceled', {'count': '$canceled'}),
            ),
            _OverviewCard(
              key: const Key('usage-input-tokens'),
              width: width,
              icon: Icons.input,
              label: copy('usage.metric.input'),
              value: input.label,
              detail: copy('usage.metric.protocol_declared'),
            ),
            _OverviewCard(
              key: const Key('usage-output-tokens'),
              width: width,
              icon: Icons.output,
              label: copy('usage.metric.output'),
              value: output.label,
              detail: copy('usage.metric.protocol_declared'),
            ),
            Tooltip(
              message: _costCoverage(_reportCost(report), copy),
              child: _OverviewCard(
                key: const Key('usage-estimated-cost'),
                width: width,
                icon: Icons.payments_outlined,
                label: copy('usage.cost.title'),
                value: _costLabel(_reportCost(report)),
                detail: copy.format('usage.cost.coverage', {
                  'known': '${_reportCost(report).completeCalls}',
                  'total': '$agentApiCalls',
                }),
              ),
            ),
          ],
        );
      },
    );
  }
}

final class _PricingNote extends StatelessWidget {
  const _PricingNote({required this.report, required this.copy});
  final RuntimeUsageReport report;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final pricing = report.pricing;
    final state = pricing?.state ?? 'unavailable';
    final label = state == 'unavailable'
        ? copy('usage.cost.unavailable')
        : '${copy.format('usage.cost.updated', {'time': _timestamp(pricing!.updatedAt!)})}'
              '${state == 'stale' ? ' · ${copy('usage.cost.stale')}' : ''}';
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Wrap(
        alignment: WrapAlignment.spaceBetween,
        crossAxisAlignment: WrapCrossAlignment.center,
        spacing: 12,
        runSpacing: 2,
        children: [
          Text(
            label,
            key: const Key('usage-price-status'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: state == 'ready'
                  ? context.viberColors.textFaint
                  : context.viberColors.warning,
            ),
          ),
          TextButton.icon(
            key: const Key('usage-price-basis'),
            onPressed: () => showDialog<void>(
              context: context,
              builder: (context) => AlertDialog(
                title: Text(copy('usage.cost.title')),
                content: SizedBox(
                  width: 460,
                  child: SingleChildScrollView(
                    child: SelectableText(
                      '${copy('usage.cost.explanation')}\n\n'
                      '${copy('usage.cost.formula')}\n\n'
                      '${copy('usage.cost.limits')}\n\n'
                      '${_costCoverage(_reportCost(report), copy)}\n$label',
                    ),
                  ),
                ),
                actions: [
                  TextButton(
                    onPressed: () => Navigator.pop(context),
                    child: Text(copy('common.dismiss')),
                  ),
                ],
              ),
            ),
            icon: const Icon(Icons.info_outline, size: 14),
            label: Text(copy('usage.cost.basis')),
          ),
        ],
      ),
    );
  }
}

RuntimeCostEstimate _reportCost(RuntimeUsageReport report) =>
    report.cost ??
    report.days.fold(
      const RuntimeCostEstimate(),
      (sum, day) => sum.add(
        day.cost ?? RuntimeCostEstimate(unpricedCalls: day.agentApiCalls),
      ),
    );

String _costCoverage(RuntimeCostEstimate cost, AppCopy copy) =>
    copy.format('usage.cost.coverage_detail', {
      'complete': '${cost.completeCalls}',
      'partial': '${cost.partialCalls}',
      'unpriced': '${cost.unpricedCalls}',
    });

String _costLabel(RuntimeCostEstimate cost) {
  if (cost.pricedCalls == 0) return '—';
  final amount = cost.nanoUsd;
  final digits = amount == 0 || amount >= 1000000000
      ? 2
      : amount >= 100000
      ? 4
      : amount >= 1000
      ? 6
      : 9;
  final scale = math.pow(10, 9 - digits).toInt();
  final value = cost.partial ? (amount ~/ scale) * scale : amount;
  return '${cost.partial ? '≥ ' : ''}\$${(value / 1000000000).toStringAsFixed(digits)}';
}

final class _OverviewCard extends StatelessWidget {
  const _OverviewCard({
    required this.width,
    required this.icon,
    required this.label,
    required this.value,
    required this.detail,
    this.accent,
    super.key,
  });

  final double width;
  final IconData icon;
  final String label;
  final String value;
  final String detail;
  final Color? accent;

  @override
  Widget build(BuildContext context) {
    final color = accent ?? context.viberColors.route;
    return Container(
      width: width,
      constraints: const BoxConstraints(minHeight: 66),
      padding: const EdgeInsets.fromLTRB(9, 7, 9, 7),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(icon, size: 15, color: color),
              const SizedBox(width: ViberSpacing.sm),
              Expanded(
                child: Tooltip(
                  message: label,
                  child: Text(
                    label,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.labelMedium?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: ViberSpacing.xs),
          Text(
            value,
            style: Theme.of(context).textTheme.titleLarge?.copyWith(
              color: context.viberColors.text,
              fontWeight: FontWeight.w600,
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
          const SizedBox(height: ViberSpacing.xxs),
          Text(
            detail,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ],
      ),
    );
  }
}

final class _TeamActivityPanel extends StatelessWidget {
  const _TeamActivityPanel({
    required this.report,
    required this.metric,
    required this.copy,
    required this.onMetricChanged,
  });

  final RuntimeUsageReport report;
  final _ActivityMetric metric;
  final AppCopy copy;
  final ValueChanged<_ActivityMetric> onMetricChanged;

  @override
  Widget build(BuildContext context) => Container(
    key: const Key('usage-team-heatmap'),
    decoration: BoxDecoration(
      color: context.viberColors.panel,
      border: Border.all(color: context.viberColors.divider),
      borderRadius: ViberMetrics.surfaceRadius,
    ),
    clipBehavior: Clip.antiAlias,
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 9),
          child: LayoutBuilder(
            builder: (context, constraints) {
              final heading = _SectionHeading(
                icon: Icons.calendar_view_month_outlined,
                title: copy('usage.activity.team.title'),
                detail: copy('usage.activity.team.detail'),
              );
              final selector = _ActivityMetricSelector(
                selected: metric,
                copy: copy,
                onChanged: onMetricChanged,
              );
              if (constraints.maxWidth < 520) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    heading,
                    const SizedBox(height: ViberSpacing.sm),
                    Align(alignment: Alignment.centerRight, child: selector),
                  ],
                );
              }
              return Row(
                children: [
                  Expanded(child: heading),
                  const SizedBox(width: ViberSpacing.lg),
                  selector,
                ],
              );
            },
          ),
        ),
        Divider(height: 1, color: context.viberColors.dividerSoft),
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 11),
          child: _CalendarHeatmap(
            period: report.period,
            days: report.days,
            metric: metric,
            copy: copy,
            keyPrefix: 'usage-team-day',
          ),
        ),
      ],
    ),
  );
}

final class _ActivityMetricSelector extends StatelessWidget {
  const _ActivityMetricSelector({
    required this.selected,
    required this.copy,
    required this.onChanged,
  });

  final _ActivityMetric selected;
  final AppCopy copy;
  final ValueChanged<_ActivityMetric> onChanged;

  @override
  Widget build(BuildContext context) => Container(
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.controlRadius,
    ),
    clipBehavior: Clip.antiAlias,
    child: Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        _MetricButton(
          key: const Key('usage-metric-api-calls'),
          label: copy('usage.activity.metric.api_calls'),
          selected: selected == _ActivityMetric.agentApiCalls,
          onPressed: () => onChanged(_ActivityMetric.agentApiCalls),
        ),
        _MetricButton(
          key: const Key('usage-metric-tokens'),
          label: copy('usage.activity.metric.tokens'),
          selected: selected == _ActivityMetric.tokens,
          onPressed: () => onChanged(_ActivityMetric.tokens),
        ),
      ],
    ),
  );
}

final class _MetricButton extends StatelessWidget {
  const _MetricButton({
    required this.label,
    required this.selected,
    required this.onPressed,
    super.key,
  });

  final String label;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) => Semantics(
    selected: selected,
    button: true,
    child: Material(
      color: selected ? context.viberColors.selection : Colors.transparent,
      child: InkWell(
        onTap: onPressed,
        child: SizedBox(
          width: ViberMetrics.compactSegmentWidth,
          height: ViberMetrics.controlHeight,
          child: Center(
            child: Text(
              label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: selected
                    ? context.viberColors.route
                    : context.viberColors.textMuted,
                fontWeight: selected ? FontWeight.w600 : FontWeight.w500,
              ),
            ),
          ),
        ),
      ),
    ),
  );
}

final class _CalendarHeatmap extends StatelessWidget {
  const _CalendarHeatmap({
    required this.period,
    required this.days,
    required this.metric,
    required this.copy,
    required this.keyPrefix,
  });

  final RuntimeUsagePeriod period;
  final List<RuntimeDayUsage> days;
  final _ActivityMetric metric;
  final AppCopy copy;
  final String keyPrefix;

  @override
  Widget build(BuildContext context) {
    final dates = _periodDates(period);
    final cellSize = dates.length >= 300 ? 13.0 : 11.0;
    const gap = 3.0;
    final byDate = {for (final day in days) day.date: day};
    final leading = dates.isEmpty ? 0 : dates.first.weekday - DateTime.monday;
    final cells = leading + dates.length;
    final weekCount = math.max(1, (cells + 6) ~/ 7);
    final gridWidth = weekCount * cellSize + (weekCount - 1) * gap;
    const monthAxisHeight = 14.0;
    final monthMarkers = dates.indexed
        .where((entry) => entry.$2.day == 1)
        .map((entry) {
          final date = entry.$2;
          final week = (leading + entry.$1) ~/ 7;
          return (
            date: date,
            left: week * (cellSize + gap),
            label: date.month.toString().padLeft(2, '0'),
          );
        })
        .toList(growable: false);
    final maxValue = math.max(
      1,
      days.fold<int>(
        0,
        (value, day) => math.max(value, _dayValue(day, metric)),
      ),
    );
    final dayGrid = Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (var week = 0; week < weekCount; week += 1) ...[
          Column(
            children: [
              for (var weekday = 0; weekday < 7; weekday += 1) ...[
                Builder(
                  builder: (context) {
                    final dateIndex = week * 7 + weekday - leading;
                    if (dateIndex < 0 || dateIndex >= dates.length) {
                      return SizedBox(width: cellSize, height: cellSize);
                    }
                    final date = dates[dateIndex];
                    final label = _civilDate(date);
                    return _HeatCell(
                      key: Key('$keyPrefix-$label'),
                      date: label,
                      day: byDate[label],
                      metric: metric,
                      maximum: maxValue,
                      copy: copy,
                      size: cellSize,
                    );
                  },
                ),
                if (weekday != 6) const SizedBox(height: gap),
              ],
            ],
          ),
          if (week != weekCount - 1) const SizedBox(width: gap),
        ],
      ],
    );
    final grid = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          width: gridWidth,
          height: monthAxisHeight,
          child: Stack(
            clipBehavior: Clip.none,
            children: [
              for (final marker in monthMarkers)
                Positioned(
                  key: Key(
                    '$keyPrefix-month-${marker.date.year}-'
                    '${marker.date.month.toString().padLeft(2, '0')}',
                  ),
                  left: marker.left,
                  child: Semantics(
                    label:
                        '${marker.date.year}-'
                        '${marker.date.month.toString().padLeft(2, '0')}',
                    child: Text(
                      marker.label,
                      style: Theme.of(context).textTheme.labelSmall?.copyWith(
                        fontSize: ViberType.micro,
                        color: context.viberColors.textFaint,
                      ),
                    ),
                  ),
                ),
            ],
          ),
        ),
        const SizedBox(height: ViberSpacing.xxs),
        dayGrid,
      ],
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(
              width: 24,
              child: Column(
                children: [
                  const SizedBox(height: monthAxisHeight + ViberSpacing.xxs),
                  for (var weekday = 0; weekday < 7; weekday += 1) ...[
                    SizedBox(
                      height: cellSize,
                      child: weekday == 0 || weekday == 2 || weekday == 4
                          ? Text(
                              copy('usage.activity.weekday.${weekday + 1}'),
                              style: Theme.of(context).textTheme.labelSmall
                                  ?.copyWith(
                                    fontSize: ViberType.micro,
                                    color: context.viberColors.textFaint,
                                  ),
                            )
                          : null,
                    ),
                    if (weekday != 6) const SizedBox(height: gap),
                  ],
                ],
              ),
            ),
            Expanded(
              child: LayoutBuilder(
                builder: (context, constraints) => SingleChildScrollView(
                  key: Key('$keyPrefix-scroll'),
                  scrollDirection: Axis.horizontal,
                  // When the entire selected period fits, keep its beginning
                  // next to the weekday axis. If it overflows, reveal the
                  // most recent evidence first while preserving older days.
                  reverse: gridWidth > constraints.maxWidth,
                  child: grid,
                ),
              ),
            ),
          ],
        ),
        const SizedBox(height: ViberSpacing.sm),
        _HeatmapLegend(metric: metric, copy: copy),
      ],
    );
  }
}

final class _HeatCell extends StatelessWidget {
  const _HeatCell({
    required this.date,
    required this.day,
    required this.metric,
    required this.maximum,
    required this.copy,
    required this.size,
    super.key,
  });

  final String date;
  final RuntimeDayUsage? day;
  final _ActivityMetric metric;
  final int maximum;
  final AppCopy copy;
  final double size;

  @override
  Widget build(BuildContext context) {
    final value = day == null ? 0 : _dayValue(day!, metric);
    final incomplete = day != null && !_dayMetricComplete(day!, metric);
    final level = value == 0
        ? 0
        : math.max(1, math.min(4, (value * 4 / maximum).ceil()));
    final label = _dayEvidenceLabel(
      day: day,
      date: date,
      metric: metric,
      copy: copy,
    );
    final color = level == 0
        ? context.viberColors.panelRaised
        : Color.alphaBlend(
            context.viberColors.route.withValues(alpha: 0.16 + level * 0.16),
            context.viberColors.panel,
          );
    final borderColor = day != null && day!.failed > 0
        ? context.viberColors.danger
        : incomplete
        ? context.viberColors.warning
        : context.viberColors.dividerSoft;
    return Tooltip(
      message: label,
      child: Semantics(
        label: label,
        child: Container(
          width: size,
          height: size,
          decoration: BoxDecoration(
            color: color,
            border: Border.all(color: borderColor, width: incomplete ? 1.2 : 1),
            borderRadius: BorderRadius.circular(2),
          ),
        ),
      ),
    );
  }
}

final class _HeatmapLegend extends StatelessWidget {
  const _HeatmapLegend({required this.metric, required this.copy});

  final _ActivityMetric metric;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Row(
    mainAxisAlignment: MainAxisAlignment.end,
    children: [
      Text(
        copy('usage.activity.legend.less'),
        style: Theme.of(
          context,
        ).textTheme.labelSmall?.copyWith(color: context.viberColors.textFaint),
      ),
      const SizedBox(width: ViberSpacing.xs),
      for (var level = 0; level <= 4; level += 1) ...[
        Container(
          width: 10,
          height: 10,
          decoration: BoxDecoration(
            color: level == 0
                ? context.viberColors.panelRaised
                : Color.alphaBlend(
                    context.viberColors.route.withValues(
                      alpha: 0.16 + level * 0.16,
                    ),
                    context.viberColors.panel,
                  ),
            border: Border.all(color: context.viberColors.dividerSoft),
            borderRadius: BorderRadius.circular(2),
          ),
        ),
        if (level != 4) const SizedBox(width: 3),
      ],
      const SizedBox(width: ViberSpacing.xs),
      Text(
        copy('usage.activity.legend.more'),
        style: Theme.of(
          context,
        ).textTheme.labelSmall?.copyWith(color: context.viberColors.textFaint),
      ),
      if (metric == _ActivityMetric.tokens) ...[
        const SizedBox(width: ViberSpacing.md),
        Container(
          width: 10,
          height: 10,
          decoration: BoxDecoration(
            border: Border.all(color: context.viberColors.warning, width: 1.2),
            borderRadius: BorderRadius.circular(2),
          ),
        ),
        const SizedBox(width: ViberSpacing.xs),
        Flexible(
          child: Text(
            copy('usage.activity.legend.partial'),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.labelSmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ),
      ],
    ],
  );
}

final class _UserLedger extends StatelessWidget {
  const _UserLedger({
    required this.users,
    required this.selectedUserId,
    required this.query,
    required this.copy,
    required this.metric,
    required this.period,
    required this.onQueryChanged,
    required this.onSelectUser,
  });

  final List<RuntimeUserUsage> users;
  final String? selectedUserId;
  final String query;
  final AppCopy copy;
  final _ActivityMetric metric;
  final RuntimeUsagePeriod period;
  final ValueChanged<String> onQueryChanged;
  final ValueChanged<String> onSelectUser;

  @override
  Widget build(BuildContext context) {
    final normalizedQuery = query.trim().toLowerCase();
    final visible = users.indexed
        .where(
          (entry) =>
              normalizedQuery.isEmpty ||
              entry.$2.username.toLowerCase().contains(normalizedQuery) ||
              (entry.$2.latestContext?.workspaceLabel ?? '')
                  .toLowerCase()
                  .contains(normalizedQuery) ||
              (entry.$2.latestContext?.deviceName ?? '').toLowerCase().contains(
                normalizedQuery,
              ),
        )
        .toList(growable: false);
    return Container(
      key: const Key('usage-ranking'),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 10, 12, 9),
            child: _SectionHeading(
              icon: Icons.leaderboard_outlined,
              title: copy('usage.ranking.title'),
              detail: copy('usage.ranking.detail'),
            ),
          ),
          Divider(height: 1, color: context.viberColors.dividerSoft),
          Padding(
            padding: const EdgeInsets.all(ViberSpacing.md),
            child: Row(
              children: [
                Expanded(
                  child: SizedBox(
                    height: ViberMetrics.searchHeight,
                    child: TextField(
                      key: const Key('usage-user-search'),
                      onChanged: onQueryChanged,
                      decoration: InputDecoration(
                        hintText: copy('usage.ranking.search'),
                        prefixIcon: const Icon(Icons.search, size: 15),
                        contentPadding: const EdgeInsets.symmetric(
                          horizontal: ViberSpacing.md,
                        ),
                      ),
                    ),
                  ),
                ),
                const SizedBox(width: ViberSpacing.md),
                Text(
                  key: const Key('usage-ranking-count'),
                  copy.format('usage.ranking.count', {
                    'visible': '${visible.length}',
                    'total': '${users.length}',
                  }),
                  style: Theme.of(context).textTheme.labelSmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            ),
          ),
          if (visible.isEmpty)
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 16, 12, 18),
              child: _EvidenceEmpty(label: copy('usage.ranking.empty')),
            )
          else
            _RankingRows(
              entries: visible,
              selectedUserId: selectedUserId,
              query: query,
              copy: copy,
              metric: metric,
              period: period,
              onSelectUser: onSelectUser,
            ),
        ],
      ),
    );
  }
}

final class _UsageWarningNotice extends StatelessWidget {
  const _UsageWarningNotice({required this.user, required this.copy});

  final RuntimeUserUsage user;
  final AppCopy copy;

  static bool exceeded(RuntimeUserUsage user) => user.days.any(
    (day) =>
        user.dailyAgentApiCallWarning > 0 &&
            day.agentApiCalls >= user.dailyAgentApiCallWarning ||
        user.dailyTokenWarning > 0 &&
            _dayValue(day, _ActivityMetric.tokens) >= user.dailyTokenWarning,
  );

  @override
  Widget build(BuildContext context) {
    final days = user.days.where(
      (day) =>
          user.dailyAgentApiCallWarning > 0 &&
              day.agentApiCalls >= user.dailyAgentApiCallWarning ||
          user.dailyTokenWarning > 0 &&
              _dayValue(day, _ActivityMetric.tokens) >= user.dailyTokenWarning,
    );
    final day = days.last;
    final facts = <String>[];
    if (user.dailyAgentApiCallWarning > 0 &&
        day.agentApiCalls >= user.dailyAgentApiCallWarning) {
      facts.add(
        copy.format('usage.warning.calls', {
          'value': _integer(day.agentApiCalls),
          'threshold': _integer(user.dailyAgentApiCallWarning),
        }),
      );
    }
    final tokens = _dayValue(day, _ActivityMetric.tokens);
    if (user.dailyTokenWarning > 0 && tokens >= user.dailyTokenWarning) {
      facts.add(
        copy.format('usage.warning.tokens', {
          'value':
              '${_dayMetricComplete(day, _ActivityMetric.tokens) ? '' : '≥'}${_integer(tokens)}',
          'threshold': _integer(user.dailyTokenWarning),
        }),
      );
    }
    return InlineNotice(
      key: Key('usage-warning-${user.userId}'),
      message: copy.format('usage.warning', {
        'date': day.date,
        'facts': facts.join(' · '),
      }),
    );
  }
}

final class _RankingRows extends StatefulWidget {
  const _RankingRows({
    required this.entries,
    required this.selectedUserId,
    required this.query,
    required this.copy,
    required this.metric,
    required this.period,
    required this.onSelectUser,
  });

  final List<(int, RuntimeUserUsage)> entries;
  final String? selectedUserId;
  final String query;
  final AppCopy copy;
  final _ActivityMetric metric;
  final RuntimeUsagePeriod period;
  final ValueChanged<String> onSelectUser;

  @override
  State<_RankingRows> createState() => _RankingRowsState();
}

final class _RankingRowsState extends State<_RankingRows> {
  static const _rowExtent = 76.0;
  static const _maximumHeight = 300.0;
  final ScrollController _controller = ScrollController();

  @override
  void didUpdateWidget(covariant _RankingRows oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.query != widget.query && _controller.hasClients) {
      _controller.jumpTo(0);
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final height = math.min(widget.entries.length * _rowExtent, _maximumHeight);
    return SizedBox(
      height: height,
      child: Scrollbar(
        controller: _controller,
        thumbVisibility: widget.entries.length * _rowExtent > height,
        thickness: 4,
        radius: const Radius.circular(2),
        child: ListView.builder(
          key: const Key('usage-ranking-scroll'),
          controller: _controller,
          padding: EdgeInsets.zero,
          itemExtent: _rowExtent,
          itemCount: widget.entries.length,
          itemBuilder: (context, index) {
            final entry = widget.entries[index];
            return _UserUsageRow(
              rank: entry.$1 + 1,
              user: entry.$2,
              selected: entry.$2.userId == widget.selectedUserId,
              copy: widget.copy,
              metric: widget.metric,
              period: widget.period,
              onPressed: () => widget.onSelectUser(entry.$2.userId),
            );
          },
        ),
      ),
    );
  }
}

final class _UserUsageRow extends StatelessWidget {
  const _UserUsageRow({
    required this.rank,
    required this.user,
    required this.selected,
    required this.copy,
    required this.metric,
    required this.period,
    required this.onPressed,
  });

  final int rank;
  final RuntimeUserUsage user;
  final bool selected;
  final AppCopy copy;
  final _ActivityMetric metric;
  final RuntimeUsagePeriod period;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final latest = user.latestContext;
    final consumption = _userConsumptionLabel(user);
    return LayoutBuilder(
      builder: (context, constraints) {
        final compact = constraints.maxWidth < 560;
        final activity = _ActivityStrip(
          key: Key('usage-user-activity-${user.userId}'),
          period: period,
          days: user.days,
          metric: metric,
          maximumCells: compact ? 14 : 28,
          copy: copy,
        );
        return Material(
          key: Key('usage-user-${user.userId}'),
          color: selected
              ? context.viberColors.selection
              : context.viberColors.panelRaised,
          shape: RoundedRectangleBorder(
            side: BorderSide(
              color: selected
                  ? context.viberColors.selectionStrong
                  : context.viberColors.dividerSoft,
            ),
          ),
          clipBehavior: Clip.antiAlias,
          child: InkWell(
            onTap: onPressed,
            child: Padding(
              padding: const EdgeInsets.fromLTRB(10, 7, 8, 6),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      SizedBox(
                        width: 28,
                        child: Text(
                          key: Key('usage-rank-${user.userId}'),
                          '#$rank',
                          style: monoStyle.copyWith(
                            fontSize: ViberType.utility,
                            color: rank <= 3
                                ? context.viberColors.route
                                : context.viberColors.textFaint,
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                      ),
                      Icon(
                        user.activeRuns > 0
                            ? Icons.radio_button_checked
                            : Icons.radio_button_unchecked,
                        size: 13,
                        color: user.activeRuns > 0
                            ? context.viberColors.verified
                            : context.viberColors.textFaint,
                      ),
                      const SizedBox(width: ViberSpacing.sm),
                      Expanded(
                        child: Text(
                          user.username,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                      ),
                      if (!compact) ...[
                        SizedBox(width: 126, child: activity),
                        const SizedBox(width: ViberSpacing.md),
                      ],
                      Text(
                        consumption,
                        style: monoStyle.copyWith(
                          fontSize: ViberType.supporting,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                      if (!compact) ...[
                        const SizedBox(width: ViberSpacing.sm),
                        Text(
                          user.activeRuns > 0
                              ? copy.format('usage.ranking.running', {
                                  'count': '${user.activeRuns}',
                                })
                              : copy(
                                  user.active
                                      ? 'usage.ranking.idle'
                                      : 'server.users.state.disabled',
                                ),
                          style: Theme.of(context).textTheme.labelSmall
                              ?.copyWith(
                                color: user.activeRuns > 0
                                    ? context.viberColors.verified
                                    : context.viberColors.textFaint,
                              ),
                        ),
                      ],
                      const SizedBox(width: ViberSpacing.xs),
                      Icon(
                        Icons.chevron_right,
                        size: 15,
                        color: context.viberColors.textFaint,
                      ),
                    ],
                  ),
                  const SizedBox(height: ViberSpacing.xs),
                  Row(
                    children: [
                      const SizedBox(width: 41),
                      Expanded(
                        child: Text(
                          latest == null
                              ? copy('server.usage.no_traffic')
                              : [
                                  latest.workspaceLabel ??
                                      copy('server.usage.workspace.unknown'),
                                  copy.format('usage.ranking.result', {
                                    'calls': '${user.agentApiCalls}',
                                    'succeeded': '${user.succeeded}',
                                  }),
                                ].join(' · '),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                      ),
                      const SizedBox(width: ViberSpacing.sm),
                      if (compact)
                        SizedBox(width: 82, child: activity)
                      else
                        Text(
                          copy.format('usage.user.tokens.short', {
                            'input': _tokenLabel(user.tokens.inputUncached),
                            'output': _tokenLabel(user.tokens.output),
                          }),
                          style: monoStyle.copyWith(
                            fontSize: ViberType.micro,
                            color: context.viberColors.textMuted,
                          ),
                        ),
                    ],
                  ),
                  const SizedBox(height: ViberSpacing.xs),
                  _OutcomeStripe(
                    succeeded: user.succeeded,
                    failed: user.failed,
                    canceled: user.canceled,
                  ),
                ],
              ),
            ),
          ),
        );
      },
    );
  }
}

final class _ActivityStrip extends StatelessWidget {
  const _ActivityStrip({
    required this.period,
    required this.days,
    required this.metric,
    required this.maximumCells,
    required this.copy,
    super.key,
  });

  final RuntimeUsagePeriod period;
  final List<RuntimeDayUsage> days;
  final _ActivityMetric metric;
  final int maximumCells;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final allDates = _periodDates(period);
    final dates = allDates.length <= maximumCells
        ? allDates
        : allDates.sublist(allDates.length - maximumCells);
    final byDate = {for (final day in days) day.date: day};
    final maximum = math.max(
      1,
      days.fold<int>(
        0,
        (value, day) => math.max(value, _dayValue(day, metric)),
      ),
    );
    return Semantics(
      label: copy('usage.activity.user_strip'),
      child: SizedBox(
        height: 12,
        child: Row(
          children: [
            for (final (index, date) in dates.indexed) ...[
              Expanded(
                child: _HeatCell(
                  date: _civilDate(date),
                  day: byDate[_civilDate(date)],
                  metric: metric,
                  maximum: maximum,
                  copy: copy,
                  size: 12,
                ),
              ),
              if (index != dates.length - 1) const SizedBox(width: 2),
            ],
          ],
        ),
      ),
    );
  }
}

final class _OutcomeStripe extends StatelessWidget {
  const _OutcomeStripe({
    required this.succeeded,
    required this.failed,
    required this.canceled,
  });

  final int succeeded;
  final int failed;
  final int canceled;

  @override
  Widget build(BuildContext context) {
    final total = succeeded + failed + canceled;
    if (total == 0) {
      return Container(
        height: 4,
        decoration: BoxDecoration(
          color: context.viberColors.dividerSoft,
          borderRadius: ViberMetrics.pillRadius,
        ),
      );
    }
    return Semantics(
      label: '$succeeded succeeded, $failed failed, $canceled canceled',
      child: ClipRRect(
        borderRadius: ViberMetrics.pillRadius,
        child: SizedBox(
          height: 4,
          child: Row(
            children: [
              if (succeeded > 0)
                Expanded(
                  flex: succeeded,
                  child: ColoredBox(color: context.viberColors.verified),
                ),
              if (failed > 0)
                Expanded(
                  flex: failed,
                  child: ColoredBox(color: context.viberColors.danger),
                ),
              if (canceled > 0)
                Expanded(
                  flex: canceled,
                  child: ColoredBox(color: context.viberColors.warning),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

enum _UsageDimension { workspaces, models, sessions }

final class _UserEvidence extends StatefulWidget {
  const _UserEvidence({
    required this.user,
    required this.period,
    required this.metric,
    required this.copy,
    super.key,
  });

  final RuntimeUserUsage user;
  final RuntimeUsagePeriod period;
  final _ActivityMetric metric;
  final AppCopy copy;

  @override
  State<_UserEvidence> createState() => _UserEvidenceState();
}

final class _UserEvidenceState extends State<_UserEvidence> {
  _UsageDimension _dimension = _UsageDimension.workspaces;

  @override
  void didUpdateWidget(covariant _UserEvidence oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.user.userId != widget.user.userId) {
      _dimension = _UsageDimension.workspaces;
    }
  }

  @override
  Widget build(BuildContext context) {
    final user = widget.user;
    final copy = widget.copy;
    return Container(
      key: Key('usage-user-evidence-${user.userId}'),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 9, 12, 8),
            child: Row(
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        user.username,
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      const SizedBox(height: ViberSpacing.xxs),
                      Text(
                        copy.format('usage.user.activity', {
                          'time': user.lastActivityAt == null
                              ? '—'
                              : _timestamp(user.lastActivityAt!),
                        }),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      ),
                    ],
                  ),
                ),
                Text(
                  copy.format('usage.user.runs', {
                    'runs': '${user.captureRuns}',
                    'active': '${user.activeRuns}',
                  }),
                  style: Theme.of(context).textTheme.labelSmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            ),
          ),
          Divider(height: 1, color: context.viberColors.dividerSoft),
          if (_UsageWarningNotice.exceeded(user))
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 9, 12, 0),
              child: _UsageWarningNotice(user: user, copy: copy),
            ),
          Padding(
            key: Key('usage-user-heatmap-${user.userId}'),
            padding: const EdgeInsets.fromLTRB(12, 10, 12, 9),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                _SectionHeading(
                  icon: Icons.calendar_view_week_outlined,
                  title: copy('usage.activity.user.title'),
                  detail: copy('usage.activity.user.detail'),
                ),
                const SizedBox(height: ViberSpacing.md),
                _CalendarHeatmap(
                  period: widget.period,
                  days: user.days,
                  metric: widget.metric,
                  copy: copy,
                  keyPrefix: 'usage-user-${user.userId}-day',
                ),
              ],
            ),
          ),
          Divider(height: 1, color: context.viberColors.dividerSoft),
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 8, 12, 8),
            child: _TokenEvidence(tokens: user.tokens, copy: copy),
          ),
          if (user.partial)
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
              child: InlineNotice(message: copy('server.usage.partial')),
            ),
          _DimensionTabs(
            selected: _dimension,
            copy: copy,
            onSelected: (value) => setState(() {
              _dimension = value;
            }),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
            child: switch (_dimension) {
              _UsageDimension.workspaces => _WorkspaceEvidence(
                user: user,
                copy: copy,
              ),
              _UsageDimension.models => _ModelEvidence(user: user, copy: copy),
              _UsageDimension.sessions => _SessionEvidence(
                user: user,
                copy: copy,
              ),
            },
          ),
        ],
      ),
    );
  }
}

final class _TokenEvidence extends StatelessWidget {
  const _TokenEvidence({required this.tokens, required this.copy});

  final RuntimeTokenUsage tokens;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Wrap(
    spacing: ViberSpacing.sm,
    runSpacing: ViberSpacing.sm,
    children: [
      _TokenCell(
        label: copy('usage.tokens.input'),
        value: tokens.inputUncached,
      ),
      _TokenCell(
        label: copy('usage.tokens.cache_write'),
        value: tokens.cacheWrite,
      ),
      _TokenCell(
        label: copy('usage.tokens.cache_read'),
        value: tokens.cacheRead,
      ),
      _TokenCell(label: copy('usage.tokens.output'), value: tokens.output),
      _TokenCell(
        label: copy('usage.tokens.reasoning'),
        value: tokens.reasoning,
      ),
    ],
  );
}

final class _TokenCell extends StatelessWidget {
  const _TokenCell({required this.label, required this.value});

  final String label;
  final RuntimeTokenAggregate value;

  @override
  Widget build(BuildContext context) => Container(
    constraints: const BoxConstraints(minWidth: 98),
    padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 5),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Text(
          label,
          style: Theme.of(context).textTheme.labelSmall?.copyWith(
            color: context.viberColors.textMuted,
          ),
        ),
        const SizedBox(width: ViberSpacing.sm),
        Text(
          _tokenLabel(value),
          style: monoStyle.copyWith(
            fontSize: ViberType.body,
            fontWeight: FontWeight.w600,
          ),
        ),
      ],
    ),
  );
}

final class _DimensionTabs extends StatelessWidget {
  const _DimensionTabs({
    required this.selected,
    required this.copy,
    required this.onSelected,
  });

  final _UsageDimension selected;
  final AppCopy copy;
  final ValueChanged<_UsageDimension> onSelected;

  @override
  Widget build(BuildContext context) => Container(
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.symmetric(
        horizontal: BorderSide(color: context.viberColors.dividerSoft),
      ),
    ),
    child: Row(
      children: [
        Expanded(
          child: _DimensionTab(
            key: const Key('usage-dimension-workspaces'),
            icon: Icons.workspaces_outline,
            label: copy('usage.dimension.workspaces'),
            selected: selected == _UsageDimension.workspaces,
            onPressed: () => onSelected(_UsageDimension.workspaces),
          ),
        ),
        Expanded(
          child: _DimensionTab(
            key: const Key('usage-dimension-models'),
            icon: Icons.compare_arrows,
            label: copy('usage.dimension.models'),
            selected: selected == _UsageDimension.models,
            onPressed: () => onSelected(_UsageDimension.models),
          ),
        ),
        Expanded(
          child: _DimensionTab(
            key: const Key('usage-dimension-sessions'),
            icon: Icons.account_tree_outlined,
            label: copy('usage.dimension.sessions'),
            selected: selected == _UsageDimension.sessions,
            onPressed: () => onSelected(_UsageDimension.sessions),
          ),
        ),
      ],
    ),
  );
}

final class _DimensionTab extends StatelessWidget {
  const _DimensionTab({
    required this.icon,
    required this.label,
    required this.selected,
    required this.onPressed,
    super.key,
  });

  final IconData icon;
  final String label;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) => Semantics(
    selected: selected,
    button: true,
    child: Material(
      color: selected ? context.viberColors.selection : Colors.transparent,
      child: InkWell(
        onTap: onPressed,
        child: Container(
          height: 34,
          decoration: BoxDecoration(
            border: Border(
              bottom: BorderSide(
                width: 2,
                color: selected
                    ? context.viberColors.selectionStrong
                    : Colors.transparent,
              ),
            ),
          ),
          padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.sm),
          child: Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              Icon(
                icon,
                size: 14,
                color: selected
                    ? context.viberColors.route
                    : context.viberColors.textMuted,
              ),
              const SizedBox(width: ViberSpacing.xs),
              Flexible(
                child: Text(
                  label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.labelMedium?.copyWith(
                    color: selected
                        ? context.viberColors.text
                        : context.viberColors.textMuted,
                    fontWeight: selected ? FontWeight.w600 : FontWeight.w500,
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    ),
  );
}

final class _ModelEvidence extends StatelessWidget {
  const _ModelEvidence({required this.user, required this.copy});

  final RuntimeUserUsage user;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    key: const Key('usage-dimension-content-models'),
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      _SectionHeading(
        icon: Icons.compare_arrows,
        title: copy('usage.models.title'),
        detail: copy('usage.models.detail'),
      ),
      const SizedBox(height: ViberSpacing.md),
      if (user.models.isEmpty)
        _EvidenceEmpty(label: copy('usage.models.empty'))
      else
        for (final (index, model) in user.models.indexed) ...[
          Container(
            key: Key('usage-model-${user.userId}-$index'),
            padding: const EdgeInsets.all(ViberSpacing.md),
            decoration: BoxDecoration(
              color: context.viberColors.panelRaised,
              border: Border.all(color: context.viberColors.dividerSoft),
              borderRadius: ViberMetrics.controlRadius,
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  model.requestedModel,
                  style: monoStyle.copyWith(
                    fontSize: ViberType.supporting,
                    color: context.viberColors.route,
                  ),
                ),
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 3),
                  child: Icon(
                    Icons.arrow_downward,
                    size: 13,
                    color: context.viberColors.textFaint,
                  ),
                ),
                Text(
                  model.upstreamModel,
                  style: monoStyle.copyWith(fontSize: ViberType.supporting),
                ),
                const SizedBox(height: ViberSpacing.sm),
                Text(
                  copy.format('usage.model.metrics', {
                    'calls': '${model.agentApiCalls}',
                    'succeeded': '${model.succeeded}',
                    'failed': '${model.failed}',
                    'input': _tokenLabel(model.tokens.inputUncached),
                    'output': _tokenLabel(model.tokens.output),
                  }),
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            ),
          ),
          if (index != user.models.length - 1)
            const SizedBox(height: ViberSpacing.sm),
        ],
    ],
  );
}

final class _WorkspaceEvidence extends StatelessWidget {
  const _WorkspaceEvidence({required this.user, required this.copy});

  final RuntimeUserUsage user;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final workspaces = _groupWorkspaces(
      user.contexts,
      unknownLabel: copy('server.usage.workspace.unknown'),
    );
    return Column(
      key: const Key('usage-dimension-content-workspaces'),
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _SectionHeading(
          icon: Icons.workspaces_outline,
          title: copy('usage.contexts.title'),
          detail: copy('usage.contexts.detail'),
        ),
        const SizedBox(height: ViberSpacing.md),
        if (workspaces.isEmpty && user.latestContext == null)
          _EvidenceEmpty(label: copy('usage.contexts.empty'))
        else if (workspaces.isEmpty)
          _EvidenceRow(
            key: Key(
              'usage-workspace-${user.userId}-${_contextIdentity(user.latestContext!)}',
            ),
            icon: Icons.workspaces_outline,
            title:
                user.latestContext!.workspaceLabel ??
                copy('server.usage.workspace.unknown'),
            detail:
                '${user.latestContext!.deviceName} · ${_timestamp(user.latestContext!.observedAt)}',
            trailing: copy('usage.context.latest'),
          )
        else
          for (final (index, workspace) in workspaces.indexed) ...[
            _EvidenceRow(
              key: Key('usage-workspace-${user.userId}-${workspace.identity}'),
              icon: Icons.workspaces_outline,
              title: workspace.label,
              detail: copy.format('usage.workspace.metrics', {
                'runs': '${workspace.captureRuns}',
                'calls': '${workspace.agentApiCalls}',
                'devices': '${workspace.devices.length}',
                'input': _tokenLabel(workspace.tokens.inputUncached),
                'output': _tokenLabel(workspace.tokens.output),
              }),
              trailing: workspace.activeRuns > 0
                  ? copy.format('usage.context.active', {
                      'count': '${workspace.activeRuns}',
                    })
                  : workspace.lastActivityAt == null
                  ? null
                  : _timestamp(workspace.lastActivityAt!),
            ),
            if (index != workspaces.length - 1)
              const SizedBox(height: ViberSpacing.sm),
          ],
      ],
    );
  }
}

final class _SessionEvidence extends StatelessWidget {
  const _SessionEvidence({required this.user, required this.copy});

  final RuntimeUserUsage user;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    key: const Key('usage-dimension-content-sessions'),
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      _SectionHeading(
        icon: Icons.account_tree_outlined,
        title: copy('usage.sessions.title'),
        detail: copy('usage.sessions.detail'),
      ),
      const SizedBox(height: ViberSpacing.md),
      if (user.agentSessions.isEmpty)
        _EvidenceEmpty(label: copy('usage.sessions.empty'))
      else
        for (final (index, session) in user.agentSessions.indexed) ...[
          _EvidenceRow(
            key: Key('usage-session-${user.userId}-$index'),
            icon: Icons.history_toggle_off,
            title: '${session.client} · ${session.sessionId}',
            detail: copy.format('usage.session.metrics', {
              'runs': '${session.captureRuns}',
              'calls': '${session.agentApiCalls}',
              'succeeded': '${session.succeeded}',
              'failed': '${session.failed}',
            }),
            trailing: _timestamp(session.lastActivityAt),
            monoTitle: true,
          ),
          if (index != user.agentSessions.length - 1)
            const SizedBox(height: ViberSpacing.sm),
        ],
    ],
  );
}

final class _WorkspaceUsage {
  const _WorkspaceUsage({
    required this.identity,
    required this.label,
    required this.devices,
    required this.captureRuns,
    required this.activeRuns,
    required this.agentApiCalls,
    required this.succeeded,
    required this.failed,
    required this.canceled,
    required this.tokens,
    required this.lastActivityAt,
  });

  final String identity;
  final String label;
  final Set<String> devices;
  final int captureRuns;
  final int activeRuns;
  final int agentApiCalls;
  final int succeeded;
  final int failed;
  final int canceled;
  final RuntimeTokenUsage tokens;
  final DateTime? lastActivityAt;
}

final class _WorkspaceAccumulator {
  _WorkspaceAccumulator({required this.identity, required this.label});

  final String identity;
  String label;
  final Set<String> devices = {};
  final List<RuntimeTokenUsage> tokenEvidence = [];
  int captureRuns = 0;
  int activeRuns = 0;
  int agentApiCalls = 0;
  int succeeded = 0;
  int failed = 0;
  int canceled = 0;
  DateTime? lastActivityAt;

  void add(RuntimeContextUsage context) {
    devices.add(context.machineId);
    captureRuns += context.captureRuns;
    activeRuns += context.activeRuns;
    agentApiCalls += context.agentApiCalls;
    succeeded += context.succeeded;
    failed += context.failed;
    canceled += context.canceled;
    tokenEvidence.add(context.tokens);
    final activity = context.lastActivityAt;
    if (activity != null &&
        (lastActivityAt == null || activity.isAfter(lastActivityAt!))) {
      lastActivityAt = activity;
      label = context.workspaceLabel ?? context.workspaceId ?? label;
    }
  }

  _WorkspaceUsage freeze() => _WorkspaceUsage(
    identity: identity,
    label: label,
    devices: Set.unmodifiable(devices),
    captureRuns: captureRuns,
    activeRuns: activeRuns,
    agentApiCalls: agentApiCalls,
    succeeded: succeeded,
    failed: failed,
    canceled: canceled,
    tokens: _sumRuntimeTokens(tokenEvidence),
    lastActivityAt: lastActivityAt,
  );
}

List<_WorkspaceUsage> _groupWorkspaces(
  Iterable<RuntimeContextUsage> contexts, {
  required String unknownLabel,
}) {
  final grouped = <String, _WorkspaceAccumulator>{};
  for (final context in contexts) {
    final identity = _runtimeContextIdentity(context);
    final label = context.workspaceLabel ?? context.workspaceId ?? unknownLabel;
    grouped
        .putIfAbsent(
          identity,
          () => _WorkspaceAccumulator(identity: identity, label: label),
        )
        .add(context);
  }
  final result = grouped.values
      .map((value) => value.freeze())
      .toList(growable: false);
  result.sort((left, right) {
    final agentApiCalls = right.agentApiCalls.compareTo(left.agentApiCalls);
    if (agentApiCalls != 0) return agentApiCalls;
    return left.label.toLowerCase().compareTo(right.label.toLowerCase());
  });
  return result;
}

String _runtimeContextIdentity(RuntimeContextUsage context) {
  final id = context.workspaceId;
  if (id != null) return id;
  final label = context.workspaceLabel;
  if (label != null) return 'workspace-label:${context.machineId}:$label';
  return 'workspace-unknown:${context.loginSessionId}:${context.machineId}';
}

String _contextIdentity(RuntimeUsageContextRef context) {
  final id = context.workspaceId;
  if (id != null) return id;
  final label = context.workspaceLabel;
  if (label != null) return 'workspace-label:${context.machineId}:$label';
  return 'workspace-unknown:${context.loginSessionId}:${context.machineId}';
}

RuntimeTokenUsage _sumRuntimeTokens(Iterable<RuntimeTokenUsage> values) {
  final items = values.toList(growable: false);
  RuntimeTokenAggregate sum(
    RuntimeTokenAggregate Function(RuntimeTokenUsage) select,
  ) {
    var tokens = 0;
    var knownCalls = 0;
    var unknownCalls = 0;
    for (final item in items) {
      final value = select(item);
      tokens += value.tokens;
      knownCalls += value.knownCalls;
      unknownCalls += value.unknownCalls;
    }
    return RuntimeTokenAggregate(
      tokens: tokens,
      knownCalls: knownCalls,
      unknownCalls: unknownCalls,
    );
  }

  return RuntimeTokenUsage(
    inputUncached: sum((value) => value.inputUncached),
    cacheWrite: sum((value) => value.cacheWrite),
    cacheRead: sum((value) => value.cacheRead),
    output: sum((value) => value.output),
    reasoning: sum((value) => value.reasoning),
  );
}

final class _SectionHeading extends StatelessWidget {
  const _SectionHeading({
    required this.icon,
    required this.title,
    required this.detail,
  });

  final IconData icon;
  final String title;
  final String detail;

  @override
  Widget build(BuildContext context) => Row(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Icon(icon, size: 16, color: context.viberColors.route),
      const SizedBox(width: ViberSpacing.sm),
      Expanded(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(height: ViberSpacing.xxs),
            Text(
              detail,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
        ),
      ),
    ],
  );
}

final class _EvidenceRow extends StatelessWidget {
  const _EvidenceRow({
    required this.icon,
    required this.title,
    required this.detail,
    this.trailing,
    this.monoTitle = false,
    super.key,
  });

  final IconData icon;
  final String title;
  final String detail;
  final String? trailing;
  final bool monoTitle;

  @override
  Widget build(BuildContext context) => Container(
    padding: const EdgeInsets.all(ViberSpacing.md),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(icon, size: 15, color: context.viberColors.textMuted),
        const SizedBox(width: ViberSpacing.sm),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                title,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: monoTitle
                    ? monoStyle.copyWith(fontSize: ViberType.micro)
                    : Theme.of(context).textTheme.labelLarge,
              ),
              const SizedBox(height: ViberSpacing.xxs),
              Text(
                detail,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
          ),
        ),
        if (trailing != null) ...[
          const SizedBox(width: ViberSpacing.sm),
          Text(
            trailing!,
            style: Theme.of(context).textTheme.labelSmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ],
      ],
    ),
  );
}

final class _EvidenceEmpty extends StatelessWidget {
  const _EvidenceEmpty({required this.label});

  final String label;

  @override
  Widget build(BuildContext context) => Container(
    padding: const EdgeInsets.all(ViberSpacing.md),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Text(
      label,
      style: Theme.of(
        context,
      ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
    ),
  );
}

String _periodLabel(RuntimeUsagePeriod period) {
  final until = _parseCivilDate(period.until).subtract(const Duration(days: 1));
  return '${period.from} – ${_civilDate(until)} · ${period.timeZone}';
}

List<DateTime> _periodDates(RuntimeUsagePeriod period) {
  final from = _parseCivilDate(period.from);
  final until = _parseCivilDate(period.until);
  final result = <DateTime>[];
  for (
    var value = from;
    value.isBefore(until);
    value = value.add(const Duration(days: 1))
  ) {
    result.add(value);
  }
  return result;
}

DateTime _parseCivilDate(String value) => DateTime.utc(
  int.parse(value.substring(0, 4)),
  int.parse(value.substring(5, 7)),
  int.parse(value.substring(8, 10)),
);

String _civilDate(DateTime value) => [
  value.year.toString().padLeft(4, '0'),
  value.month.toString().padLeft(2, '0'),
  value.day.toString().padLeft(2, '0'),
].join('-');

int _dayValue(RuntimeDayUsage day, _ActivityMetric metric) => switch (metric) {
  _ActivityMetric.agentApiCalls => day.agentApiCalls,
  _ActivityMetric.tokens =>
    day.tokens.inputUncached.tokens +
        day.tokens.cacheWrite.tokens +
        day.tokens.cacheRead.tokens +
        day.tokens.output.tokens,
};

bool _dayMetricComplete(RuntimeDayUsage day, _ActivityMetric metric) {
  if (metric == _ActivityMetric.agentApiCalls) return true;
  final values = [
    day.tokens.inputUncached,
    day.tokens.cacheWrite,
    day.tokens.cacheRead,
    day.tokens.output,
  ];
  return values.every((value) => value.complete);
}

String _dayEvidenceLabel({
  required RuntimeDayUsage? day,
  required String date,
  required _ActivityMetric metric,
  required AppCopy copy,
}) {
  if (day == null) {
    return copy.format('usage.activity.cell.zero', {'date': date});
  }
  return copy.format('usage.activity.cell.value', {
    'date': date,
    'value': _integer(_dayValue(day, metric)),
    'metric': copy(
      metric == _ActivityMetric.agentApiCalls
          ? 'usage.activity.metric.api_calls'
          : 'usage.activity.metric.tokens',
    ),
    'failed': '${day.failed}',
    'evidence': _dayMetricComplete(day, metric)
        ? copy('usage.activity.evidence.complete')
        : copy('usage.activity.evidence.partial'),
  });
}

final class _ObservedTotal {
  const _ObservedTotal({
    required this.tokens,
    required this.knownCalls,
    required this.unknownCalls,
  });

  final int tokens;
  final int knownCalls;
  final int unknownCalls;

  String get label {
    if (knownCalls == 0) return '—';
    return '${unknownCalls > 0 ? '≥' : ''}${_integer(tokens)}';
  }
}

_ObservedTotal _sumDayTokens(
  List<RuntimeDayUsage> days,
  RuntimeTokenAggregate Function(RuntimeTokenUsage) select,
) {
  var tokens = 0;
  var known = 0;
  var unknown = 0;
  for (final day in days) {
    final value = select(day.tokens);
    tokens += value.tokens;
    known += value.knownCalls;
    unknown += value.unknownCalls;
  }
  return _ObservedTotal(
    tokens: tokens,
    knownCalls: known,
    unknownCalls: unknown,
  );
}

String _tokenLabel(RuntimeTokenAggregate value) {
  if (!value.observed) return '—';
  return '${value.complete ? '' : '≥'}${_integer(value.tokens)}';
}

List<RuntimeUserUsage> _rankedUsers(Iterable<RuntimeUserUsage> users) {
  final ordered = users.toList(growable: false);
  ordered.sort((left, right) {
    final consumption = _userConsumption(
      right,
    ).compareTo(_userConsumption(left));
    if (consumption != 0) return consumption;
    final agentApiCalls = right.agentApiCalls.compareTo(left.agentApiCalls);
    if (agentApiCalls != 0) return agentApiCalls;
    return left.username.toLowerCase().compareTo(right.username.toLowerCase());
  });
  return ordered;
}

int _userConsumption(RuntimeUserUsage user) =>
    user.tokens.inputUncached.tokens + user.tokens.output.tokens;

String _userConsumptionLabel(RuntimeUserUsage user) {
  final input = user.tokens.inputUncached;
  final output = user.tokens.output;
  if (!input.observed && !output.observed) return '—';
  final complete =
      (!input.observed || input.complete) &&
      (!output.observed || output.complete);
  return '${complete ? '' : '≥'}${_integer(_userConsumption(user))}';
}

String _integer(int value) {
  if (value >= 1000000000) {
    return '${(value / 1000000000).toStringAsFixed(1)}B';
  }
  if (value >= 1000000) return '${(value / 1000000).toStringAsFixed(1)}M';
  if (value >= 1000) return '${(value / 1000).toStringAsFixed(1)}K';
  return '$value';
}

String _timestamp(DateTime value, {bool seconds = false}) {
  final local = value.toLocal();
  String two(int number) => number.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}${seconds ? ':${two(local.second)}' : ''}';
}

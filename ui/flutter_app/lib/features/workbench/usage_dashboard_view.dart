import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/foundation.dart' show listEquals, mapEquals;
import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/api/control_api.dart' show ControlProblem;
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

enum _ActivityMetric { agentApiCalls, tokens }

typedef UsagePageLoader =
    Future<RuntimeUsageReport> Function(RuntimeUsageQuery query);

final class UsageDashboardView extends StatelessWidget {
  const UsageDashboardView({
    required this.controller,
    required this.copy,
    super.key,
  });
  final WorkbenchController controller;
  final AppCopy copy;
  @override
  Widget build(BuildContext context) => _UsageDashboard(
    copy: copy,
    report: controller.runtimeUsage,
    loading: controller.usageLoading,
    error: controller.usageError,
    onRefresh: () => unawaited(controller.refreshUsage()),
    rangeDays: controller.usageRangeDays,
    onRangeChanged: (days) => unawaited(controller.setUsageRange(days)),
    loadPage: controller.loadUsagePage,
    controller: controller,
  );
}

/// The server owns personal visibility; the presentation is shared.
final class PersonalUsageDashboard extends StatelessWidget {
  const PersonalUsageDashboard({
    required this.report,
    required this.loading,
    required this.error,
    required this.onRefresh,
    required this.copy,
    required this.loadPage,
    this.rangeDays = 7,
    this.onRangeChanged,
    super.key,
  });
  final RuntimeUsageReport? report;
  final bool loading;
  final String? error;
  final VoidCallback onRefresh;
  final AppCopy copy;
  final UsagePageLoader loadPage;
  final int rangeDays;
  final ValueChanged<int>? onRangeChanged;
  @override
  Widget build(BuildContext context) => _UsageDashboard(
    copy: copy,
    report: report,
    loading: loading,
    error: error,
    onRefresh: onRefresh,
    rangeDays: rangeDays,
    onRangeChanged: onRangeChanged,
    loadPage: loadPage,
  );
}

final class _UsageDashboard extends StatefulWidget {
  const _UsageDashboard({
    required this.copy,
    required this.report,
    required this.loading,
    required this.error,
    required this.onRefresh,
    required this.rangeDays,
    required this.onRangeChanged,
    required this.loadPage,
    this.controller,
  });
  final AppCopy copy;
  final RuntimeUsageReport? report;
  final bool loading;
  final String? error;
  final VoidCallback onRefresh;
  final int rangeDays;
  final ValueChanged<int>? onRangeChanged;
  final UsagePageLoader loadPage;
  final WorkbenchController? controller;
  @override
  State<_UsageDashboard> createState() => _UsageDashboardState();
}

final class _UsageDashboardState extends State<_UsageDashboard> {
  _ActivityMetric _metric = _ActivityMetric.agentApiCalls;
  @override
  Widget build(BuildContext context) {
    final report = widget.report;
    final copy = widget.copy;
    final controller = widget.controller;
    return Column(
      key: Key(
        controller == null ? 'personal-usage-dashboard' : 'usage-dashboard',
      ),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        PageHeading(
          title: copy(
            controller == null ? 'usage.personal.title' : 'usage.title',
          ),
          help: copy(
            controller == null ? 'usage.personal.subtitle' : 'usage.subtitle',
          ),
          dismissHelpLabel: copy('common.dismiss'),
        ),
        const Divider(height: 1),
        Expanded(
          child: report == null
              ? widget.loading
                    ? Center(
                        child: CompactLoadingMessage(
                          label: copy('usage.loading'),
                        ),
                      )
                    : _UsageUnavailable(
                        copy: copy,
                        detail: widget.error,
                        onRetry: widget.onRefresh,
                      )
              : SingleChildScrollView(
                  key: Key(
                    controller == null
                        ? 'personal-usage-scroll'
                        : 'usage-dashboard-scroll',
                  ),
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
                          if (widget.onRangeChanged != null)
                            SegmentedButton<int>(
                              key: const Key('usage-range'),
                              showSelectedIcon: false,
                              segments: [
                                for (final days in [7, 30, 90, 365])
                                  ButtonSegment(
                                    value: days,
                                    label: Text(
                                      copy.format('usage.range', {
                                        'days': '$days',
                                      }),
                                    ),
                                  ),
                              ],
                              selected: {widget.rangeDays},
                              onSelectionChanged: (values) =>
                                  widget.onRangeChanged!(values.single),
                            ),
                          if (controller != null)
                            TextButton.icon(
                              key: const Key('usage-collection-settings'),
                              onPressed: controller.runtimeUserMutating
                                  ? null
                                  : () => unawaited(
                                      _editUsageCollection(
                                        context,
                                        controller,
                                        copy,
                                      ),
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
                        refreshing: widget.loading,
                        onRefresh: widget.onRefresh,
                      ),
                      const SizedBox(height: 8),
                      Text(
                        report.collection.enabled
                            ? copy.format('usage.collection.since', {
                                'time': _timestamp(
                                  report.collection.collectingSince!,
                                ),
                                'days': report.collection.retentionDays
                                    .toString(),
                              })
                            : copy(
                                controller == null
                                    ? 'usage.personal.collection_off'
                                    : 'usage.collection.disabled',
                              ),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      ),
                      if (widget.error != null)
                        InlineNotice(
                          message: copy('usage.refresh.failed'),
                          error: true,
                        ),
                      const SizedBox(height: 16),
                      _UsageOverview(report: report, copy: copy),
                      UsagePricingNote(report: report, copy: copy),
                      const SizedBox(height: 16),
                      _UsageTrend(
                        report: report,
                        copy: copy,
                        annual: widget.rangeDays == 365
                            ? _TeamActivityPanel(
                                report: report,
                                metric: _metric,
                                copy: copy,
                                onMetricChanged: (value) =>
                                    setState(() => _metric = value),
                              )
                            : null,
                      ),
                      const SizedBox(height: 20),
                      UsageGroupTable(
                        report: report,
                        copy: copy,
                        loadPage: widget.loadPage,
                        onRefresh: widget.onRefresh,
                      ),
                    ],
                  ),
                ),
        ),
      ],
    );
  }
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
              Row(
                children: [
                  Expanded(child: Text(copy('usage.collection.enable'))),
                  Semantics(
                    label: copy('usage.collection.enable'),
                    child: CompactSwitch(
                      switchKey: const Key('usage-collection-enabled'),
                      value: enabled,
                      onChanged: (value) => setState(() => enabled = value),
                    ),
                  ),
                ],
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
      point.cost = point.cost.add(day.cost);
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
                              ? '$key · ${usageCostLabel(point.cost)} USD · ${_costCoverage(point.cost, copy)}'
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
                                                      ? usageCostLabel(
                                                          point.cost,
                                                        )
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

final class UsageGroupTable extends StatefulWidget {
  const UsageGroupTable({
    required this.report,
    required this.copy,
    required this.loadPage,
    required this.onRefresh,
    this.modelsOnly = false,
    super.key,
  });
  final RuntimeUsageReport report;
  final AppCopy copy;
  final UsagePageLoader loadPage;
  final VoidCallback onRefresh;
  final bool modelsOnly;
  @override
  State<UsageGroupTable> createState() => _UsageGroupTableState();
}

final class _UsageGroupTableState extends State<UsageGroupTable> {
  static const _labels = {
    'profile': 'profiles',
    'account': 'accounts',
    'model': 'models',
    'source': 'sources',
    'caller': 'callers',
    'project': 'projects',
  };
  final _horizontalScroll = ScrollController();
  final _rowsScroll = ScrollController();
  // The committed view. A load names a target view and commits it together
  // with its page, snapshot and subtotal only when every request succeeded,
  // so a failed or superseded navigation never leaves a partial view.
  final List<RuntimeUsageGroup> _trail = [];
  final List<String> _cursors = [''];
  String _groupBy = 'project';
  bool _byBranch = false;
  bool _loading = true;
  // A newer snapshot exists (seen by the report poll) or the committed one was
  // refused (409). Either way later pages of the committed snapshot cannot be
  // read, so paging waits for an explicit refresh of the current page.
  bool _updatesPending = false;
  bool _snapshotExpired = false;
  String? _error;
  _UsageTarget? _failedTarget;
  bool _failedRefresh = false;
  double _viewportHeight = 80;
  RuntimeUsageReport? _page;
  RuntimeUsageGroup? _subtotal;
  late String _snapshot;
  int _generation = 0;

  bool get _pagingBlocked => _loading || _updatesPending || _snapshotExpired;

  _UsageTarget get _committed => _UsageTarget(
    groupBy: _groupBy,
    byBranch: _byBranch,
    trail: List.of(_trail),
    cursors: List.of(_cursors),
  );

  @override
  void initState() {
    super.initState();
    _snapshot = widget.report.snapshot;
    if (widget.modelsOnly) _groupBy = 'model';
    unawaited(_load(_committed));
  }

  @override
  void dispose() {
    _horizontalScroll.dispose();
    _rowsScroll.dispose();
    super.dispose();
  }

  @override
  void didUpdateWidget(covariant UsageGroupTable oldWidget) {
    super.didUpdateWidget(oldWidget);
    final period = widget.report.period;
    final previous = oldWidget.report.period;
    if (period.from != previous.from ||
        period.until != previous.until ||
        period.timeZone != previous.timeZone ||
        !mapEquals(widget.report.filters, oldWidget.report.filters)) {
      // A different scope has no relationship to the committed view.
      _trail.clear();
      _cursors
        ..clear()
        ..add('');
      _page = null;
      _refresh();
    } else if (widget.report.snapshot != oldWidget.report.snapshot) {
      if (_page?.snapshot == widget.report.snapshot) {
        _updatesPending = false;
      } else if (_trail.isEmpty && _cursors.length == 1 && !_loading) {
        // The root first page follows the report silently.
        unawaited(_load(_committed, snapshot: widget.report.snapshot));
      } else {
        _updatesPending = true;
      }
    }
  }

  void _refresh() {
    unawaited(_load(_committed, refreshPage: true));
  }

  Future<void> _load(
    _UsageTarget target, {
    RuntimeUsageGroup? knownSubtotal,
    String? snapshot,
    bool renewable = true,
    bool refreshPage = false,
  }) async {
    final generation = ++_generation;
    void begin() {
      _loading = true;
      _error = null;
      _failedTarget = null;
    }

    // The first load starts from initState, before the first build.
    if (_page == null && generation == 1) {
      begin();
    } else {
      setState(begin);
    }
    final period = widget.report.period;
    final rootSnapshot = widget.report.snapshot;
    final requestedSnapshot = snapshot ?? _snapshot;
    final filters = target.filters(widget.report.filters);
    final dimension = target.dimensions[target.trail.length];
    final cursor = target.cursors.last;
    RuntimeUsageQuery query({
      bool summary = false,
      required String snapshot,
      String? pageCursor,
    }) => RuntimeUsageQuery(
      from: period.from,
      until: period.until,
      timeZone: period.timeZone,
      groupBy: summary ? '' : dimension,
      filters: filters,
      snapshot: snapshot,
      cursor: summary ? '' : pageCursor ?? cursor,
    );
    // Navigation (open a level, regroup, update) may move to the newest
    // snapshot: it starts a fresh first page, so nothing being read is
    // replaced. Paging reads one snapshot and is never swapped for a page of
    // another. Renewal is bounded so continuous writes cannot loop forever.
    final renewals = renewable && (refreshPage || cursor.isEmpty) ? 2 : 0;
    for (var attempt = 0; attempt <= renewals; attempt++) {
      try {
        RuntimeUsageReport page;
        RuntimeUsageGroup? subtotal;
        var cursors = target.cursors;
        if (refreshPage) {
          // Cursors belong to one snapshot; clamp only if it has fewer pages.
          // ponytail: replay is O(current page); add server-side seek only if
          // deep-page refresh becomes slow.
          final summary = await widget.loadPage(
            query(summary: true, snapshot: ''),
          );
          if (!mounted || generation != _generation) return;
          subtotal = summary.total;
          cursors = [''];
          page = await widget.loadPage(
            query(snapshot: summary.snapshot, pageCursor: ''),
          );
          while (cursors.length < target.cursors.length &&
              page.nextCursor.isNotEmpty) {
            if (!mounted || generation != _generation) return;
            cursors.add(page.nextCursor);
            page = await widget.loadPage(
              query(snapshot: summary.snapshot, pageCursor: cursors.last),
            );
          }
        } else if (attempt == 0) {
          subtotal =
              knownSubtotal ??
              (target.trail.isEmpty && requestedSnapshot == rootSnapshot
                  ? widget.report.total
                  : null);
          final results = await Future.wait([
            widget.loadPage(query(snapshot: requestedSnapshot)),
            if (subtotal == null)
              widget.loadPage(
                query(summary: true, snapshot: requestedSnapshot),
              ),
          ]);
          page = results.first;
          subtotal ??= results.last.total;
        } else {
          final summary = await widget.loadPage(
            query(summary: true, snapshot: ''),
          );
          if (!mounted || generation != _generation) return;
          page = await widget.loadPage(query(snapshot: summary.snapshot));
          subtotal = summary.total;
        }
        if (!mounted || generation != _generation) return;
        final pageChanged =
            _cursors.last != target.cursors.last ||
            !listEquals(_trail, target.trail) ||
            _groupBy != target.groupBy ||
            _byBranch != target.byBranch;
        setState(() {
          _groupBy = target.groupBy;
          _byBranch = target.byBranch;
          _trail
            ..clear()
            ..addAll(target.trail);
          _cursors
            ..clear()
            ..addAll(cursors);
          _page = page;
          _snapshot = page.snapshot;
          _subtotal = subtotal;
          // Keep this viewport stable when regrouping to fewer rows, so the
          // surrounding scroll view does not clamp and jump towards the top.
          _viewportHeight = math.max(
            _viewportHeight,
            math.min(
              480,
              44 + (page.groups.length + (subtotal == null ? 0 : 1)) * 60 + 24,
            ),
          );
          _loading = false;
          _snapshotExpired = false;
          _updatesPending =
              widget.report.snapshot != rootSnapshot &&
              widget.report.snapshot != page.snapshot;
        });
        if (pageChanged && _rowsScroll.hasClients) _rowsScroll.jumpTo(0);
        return;
      } on Object catch (error) {
        if (!mounted || generation != _generation) return;
        final expired =
            error is ControlProblem &&
            error.reasonCode == 'usage_snapshot_changed';
        if (expired && attempt < renewals) continue;
        setState(() {
          _loading = false;
          if (expired) {
            _snapshotExpired = true;
          } else {
            _error = 'usage.page.failed';
            _failedTarget = target;
            _failedRefresh = refreshPage;
          }
        });
        return;
      }
    }
  }

  void _navigate(
    int depth, {
    RuntimeUsageGroup? child,
    String? groupBy,
    bool? byBranch,
  }) {
    final regrouped =
        (groupBy != null && groupBy != _groupBy) ||
        (byBranch != null && byBranch != _byBranch);
    final trail = regrouped ? <RuntimeUsageGroup>[] : _trail.sublist(0, depth);
    if (child != null) trail.add(child);
    unawaited(
      _load(
        _UsageTarget(
          groupBy: groupBy ?? _groupBy,
          byBranch: byBranch ?? _byBranch,
          trail: trail,
          cursors: const [''],
        ),
        knownSubtotal: child,
        snapshot: _page?.snapshot ?? _snapshot,
      ),
    );
  }

  void _turnPage(List<String> cursors) {
    unawaited(
      _load(
        _UsageTarget(
          groupBy: _groupBy,
          byBranch: _byBranch,
          trail: List.of(_trail),
          cursors: cursors,
        ),
        knownSubtotal: _subtotal,
        renewable: false,
      ),
    );
  }

  String _label(RuntimeUsageGroup group) {
    if (group.dimension == 'source' && group.id.isNotEmpty) {
      return widget.copy('usage.source.${group.id}');
    }
    if (group.evidence == 'detached') return 'Detached HEAD';
    return group.label.isNotEmpty
        ? group.label
        : group.id.isEmpty
        ? widget.copy('usage.unknown')
        : group.id;
  }

  DataRow _row(
    RuntimeUsageGroup group,
    List<String> headers, {
    bool total = false,
  }) {
    final expandable =
        !total && _trail.length + 1 < _committed.dimensions.length;
    final name = total
        ? widget.copy(_trail.isEmpty ? 'usage.total' : 'usage.subtotal')
        : _label(group);
    final detail = switch (group.evidence) {
      'member' => widget.copy('usage.caller.member'),
      'local' => widget.copy(
        group.dimension == 'project'
            ? 'usage.project.local'
            : 'usage.caller.local',
      ),
      'remote' => widget.copy('usage.project.remote'),
      _ => '',
    };
    return DataRow(
      key: ValueKey('usage-row-${total ? 'total' : group.id}'),
      color: total ? WidgetStatePropertyAll(context.viberColors.panel) : null,
      cells: [
        DataCell(
          SizedBox(
            width: 300,
            child: Row(
              children: [
                if (expandable)
                  IconButton(
                    key: Key('usage-expand-${group.id}'),
                    tooltip: '${widget.copy('usage.expand')} $name',
                    constraints: const BoxConstraints.tightFor(
                      width: 32,
                      height: 40,
                    ),
                    padding: EdgeInsets.zero,
                    onPressed: () => _navigate(_trail.length, child: group),
                    icon: const Icon(Icons.chevron_right, size: 18),
                  )
                else
                  const SizedBox(width: 32),
                Expanded(
                  child: Tooltip(
                    message: name,
                    child: Column(
                      mainAxisAlignment: MainAxisAlignment.center,
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          name,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: const TextStyle(fontWeight: FontWeight.w600),
                        ),
                        if (detail.isNotEmpty)
                          Text(
                            detail,
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
          onTap: expandable
              ? () => _navigate(_trail.length, child: group)
              : null,
        ),
        DataCell(
          _headed(headers[1], Text(usageIntegerLabel(group.agentApiCalls))),
        ),
        DataCell(_headed(headers[2], Text(usageIntegerLabel(group.failed)))),
        for (final (index, value) in [
          group.tokens.inputUncached,
          group.tokens.cacheRead,
          group.tokens.output,
        ].indexed)
          DataCell(
            _headed(
              headers[3 + index],
              Tooltip(
                message: widget.copy('usage.tokens.hint'),
                child: Text(
                  value.observed
                      ? '${value.complete ? '' : '≥ '}${usageIntegerLabel(value.tokens)}'
                      : '—',
                ),
              ),
            ),
          ),
        DataCell(
          _headed(
            headers[6],
            Tooltip(
              message:
                  '${_costCoverage(group.cost, widget.copy)}\n${usageCostLabel(group.cost)}',
              child: Text(
                _tableCostLabel(group.cost),
                style: TextStyle(
                  fontWeight: FontWeight.w600,
                  color: context.viberColors.route,
                ),
              ),
            ),
          ),
        ),
      ],
    );
  }

  // The body table repeats no visible heading, so each value carries its
  // column name for screen readers.
  static Widget _headed(String header, Widget child) => MergeSemantics(
    child: Semantics(label: header, child: child),
  );

  Widget _table(RuntimeUsageReport page) => LayoutBuilder(
    builder: (context, constraints) {
      final labels = [
        _committed.dimensions[_trail.length] == 'branch'
            ? 'usage.branch.launch'
            : 'usage.group.${_labels[_committed.dimensions[_trail.length]]}',
        'usage.metric.api_calls',
        'usage.failed',
        'usage.table.input',
        'usage.cache_read',
        'usage.table.output',
        'usage.table.cost',
      ];
      final headers = [for (final label in labels) widget.copy(label)];
      List<DataColumn> columns({required bool heading}) => [
        for (var i = 0; i < labels.length; i++)
          DataColumn(
            numeric: i > 0,
            columnWidth: i == 0
                ? const FixedColumnWidth(324)
                : const FlexColumnWidth(),
            label: heading
                ? Flexible(
                    child: Text(
                      headers[i],
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                    ),
                  )
                : const SizedBox.shrink(),
          ),
      ];
      return SizedBox(
        key: const Key('usage-breakdown-viewport'),
        height: _viewportHeight,
        child: Scrollbar(
          controller: _horizontalScroll,
          thumbVisibility: true,
          notificationPredicate: (notification) => notification.depth == 0,
          child: SingleChildScrollView(
            key: const Key('usage-breakdown-scroll'),
            controller: _horizontalScroll,
            scrollDirection: Axis.horizontal,
            child: SizedBox(
              width: math.max(1200, constraints.maxWidth),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  DataTable(
                    headingRowHeight: 44,
                    headingRowColor: WidgetStatePropertyAll(
                      context.viberColors.panelRaised,
                    ),
                    horizontalMargin: 12,
                    columnSpacing: 24,
                    columns: columns(heading: true),
                    rows: const [],
                  ),
                  Expanded(
                    child: Scrollbar(
                      controller: _rowsScroll,
                      thumbVisibility: true,
                      child: SingleChildScrollView(
                        controller: _rowsScroll,
                        child: DataTable(
                          key: const Key('usage-breakdown-table'),
                          headingRowHeight: 0,
                          dataRowMinHeight: 44,
                          dataRowMaxHeight: 60,
                          horizontalMargin: 12,
                          columnSpacing: 24,
                          dataTextStyle: Theme.of(context).textTheme.bodyMedium
                              ?.copyWith(
                                fontFeatures: const [
                                  FontFeature.tabularFigures(),
                                ],
                              ),
                          columns: columns(heading: false),
                          rows: [
                            for (final group in page.groups)
                              _row(group, headers),
                            if (_subtotal case final total?)
                              _row(total, headers, total: true),
                          ],
                        ),
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      );
    },
  );

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          copy('usage.breakdown.title'),
          style: Theme.of(context).textTheme.titleMedium,
        ),
        const SizedBox(height: 8),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            if (!widget.modelsOnly)
              for (final dimension in [
                'project',
                'profile',
                'account',
                'model',
                'source',
                'caller',
              ])
                ChoiceChip(
                  key: Key('usage-group-${_labels[dimension]}'),
                  label: Text(copy('usage.group.${_labels[dimension]}')),
                  selected: dimension == _groupBy,
                  onSelected: (_) => _navigate(0, groupBy: dimension),
                ),
          ],
        ),
        const SizedBox(height: 8),
        ConstrainedBox(
          constraints: const BoxConstraints(minHeight: 40),
          child: Wrap(
            spacing: 4,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              TextButton(
                onPressed: _trail.isEmpty ? null : () => _navigate(0),
                child: Text(copy('usage.group.${_labels[_groupBy]}')),
              ),
              for (var i = 0; i < _trail.length; i++) ...[
                const Icon(Icons.chevron_right, size: 14),
                ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 240),
                  child: TextButton(
                    onPressed: i == _trail.length - 1
                        ? null
                        : () => _navigate(i + 1),
                    child: Text(
                      _label(_trail[i]),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                ),
              ],
              if (_groupBy == 'project')
                FilterChip(
                  label: Text(copy('usage.branch.launch')),
                  selected: _byBranch,
                  onSelected: (value) => _navigate(0, byBranch: value),
                ),
            ],
          ),
        ),
        Text(
          copy('usage.path.${_labels[_groupBy]}'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        SizedBox(
          height: 36,
          child: Row(
            children: [
              Expanded(
                child: Text(
                  _snapshotExpired
                      ? copy('usage.page.changed')
                      : _updatesPending
                      ? copy('usage.page.updated')
                      : copy.format('usage.page.as_of', {
                          'time': _timestamp(
                            (_page ?? widget.report).generatedAt,
                            seconds: true,
                          ),
                        }),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ),
              TextButton.icon(
                key: const Key('usage-breakdown-update'),
                onPressed: _loading ? null : _refresh,
                icon: const Icon(Icons.refresh, size: 15),
                label: Text(copy('usage.page.update')),
              ),
            ],
          ),
        ),
        if (_error case final error?)
          InlineNotice(
            key: const Key('usage-breakdown-error'),
            message: copy(error),
            error: true,
            actionLabel: copy('common.retry'),
            onAction: () {
              final target = _failedTarget;
              if (target != null) {
                unawaited(_load(target, refreshPage: _failedRefresh));
              }
            },
          ),
        const SizedBox(height: 8),
        SizedBox(
          height: 2,
          child: _loading ? const LinearProgressIndicator() : null,
        ),
        if (_loading && _page == null)
          SizedBox(
            height: _viewportHeight,
            child: Center(
              child: CompactLoadingMessage(label: copy('usage.loading')),
            ),
          )
        else if (_page case final page?) ...[
          if (page.groups.isEmpty)
            SizedBox(
              height: _viewportHeight,
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Text(copy('usage.empty.window')),
              ),
            )
          else
            _table(page),
          SizedBox(
            height: 40,
            child: _cursors.length == 1 && page.nextCursor.isEmpty
                ? null
                : Row(
                    mainAxisAlignment: MainAxisAlignment.end,
                    children: [
                      IconButton(
                        tooltip: copy('usage.page.previous'),
                        icon: const Icon(Icons.chevron_left),
                        onPressed: _pagingBlocked || _cursors.length == 1
                            ? null
                            : () => _turnPage(
                                _cursors.sublist(0, _cursors.length - 1),
                              ),
                      ),
                      Text(
                        copy.format('usage.page.number', {
                          'page': _cursors.length.toString(),
                        }),
                      ),
                      IconButton(
                        tooltip: copy('usage.page.next'),
                        icon: const Icon(Icons.chevron_right),
                        onPressed: _pagingBlocked || page.nextCursor.isEmpty
                            ? null
                            : () => _turnPage([..._cursors, page.nextCursor]),
                      ),
                    ],
                  ),
          ),
        ],
        const SizedBox(height: 8),
        Text(
          copy('usage.page.hint'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        Text(
          copy('usage.tokens.hint'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textFaint),
        ),
        Visibility(
          visible: _groupBy == 'project',
          maintainSize: true,
          maintainAnimation: true,
          maintainState: true,
          child: Text(
            copy('usage.projects.hint'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ),
      ],
    );
  }
}

String usageIntegerLabel(int value) =>
    '$value'.replaceAllMapped(RegExp(r'\B(?=(\d{3})+(?!\d))'), (_) => ',');

String _tableCostLabel(RuntimeCostEstimate cost) {
  if (cost.pricedCalls == 0) return '—';
  if (!cost.partial && cost.nanoUsd > 0 && cost.nanoUsd < 10000000) {
    return '< \$0.01';
  }
  final cents = cost.partial
      ? cost.nanoUsd ~/ 10000000
      : (cost.nanoUsd + 5000000) ~/ 10000000;
  return '${cost.partial ? '≥ ' : ''}\$${usageIntegerLabel(cents ~/ 100)}.${(cents % 100).toString().padLeft(2, '0')}';
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
            UsageOverviewCard(
              width: width,
              key: const Key('usage-total-api-calls'),
              icon: Icons.swap_horiz,
              label: copy('usage.metric.api_calls'),
              value: _integer(agentApiCalls),
              detail: copy('usage.metric.api_calls.detail'),
            ),
            UsageOverviewCard(
              width: width,
              icon: Icons.check_circle_outline,
              label: copy('usage.succeeded'),
              value: _integer(succeeded),
              detail: agentApiCalls == 0
                  ? '—'
                  : '${(100 * succeeded / agentApiCalls).toStringAsFixed(1)}%',
              accent: context.viberColors.verified,
            ),
            UsageOverviewCard(
              width: width,
              icon: Icons.error_outline,
              label: copy('usage.failed'),
              value: _integer(failed),
              detail: copy.format('usage.canceled', {'count': '$canceled'}),
            ),
            UsageOverviewCard(
              key: const Key('usage-input-tokens'),
              width: width,
              icon: Icons.input,
              label: copy('usage.metric.input'),
              value: input.label,
              detail: copy('usage.metric.protocol_declared'),
            ),
            UsageOverviewCard(
              key: const Key('usage-output-tokens'),
              width: width,
              icon: Icons.output,
              label: copy('usage.metric.output'),
              value: output.label,
              detail: copy('usage.metric.protocol_declared'),
            ),
            Tooltip(
              message: _costCoverage(_reportCost(report), copy),
              child: UsageOverviewCard(
                key: const Key('usage-estimated-cost'),
                width: width,
                icon: Icons.payments_outlined,
                label: copy('usage.cost.title'),
                value: usageCostLabel(_reportCost(report)),
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

final class UsagePricingNote extends StatelessWidget {
  const UsagePricingNote({required this.report, required this.copy, super.key});
  final RuntimeUsageReport report;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final pricing = report.pricing;
    final state = pricing.state;
    final label = state == 'unavailable'
        ? copy('usage.cost.unavailable')
        : '${copy.format('usage.cost.updated', {'time': _timestamp(pricing.updatedAt!)})}'
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
    report.total!.cost;

String _costCoverage(RuntimeCostEstimate cost, AppCopy copy) =>
    copy.format('usage.cost.coverage_detail', {
      'complete': '${cost.completeCalls}',
      'partial': '${cost.partialCalls}',
      'unpriced': '${cost.unpricedCalls}',
    });

String usageCostLabel(RuntimeCostEstimate cost) {
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

final class UsageOverviewCard extends StatelessWidget {
  const UsageOverviewCard({
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

/// One requested breakdown view: grouping, drill-down path and page cursors.
final class _UsageTarget {
  const _UsageTarget({
    required this.groupBy,
    required this.byBranch,
    required this.trail,
    required this.cursors,
  });

  final String groupBy;
  final bool byBranch;
  final List<RuntimeUsageGroup> trail;
  final List<String> cursors;

  List<String> get dimensions => switch (groupBy) {
    'project' => ['project', if (byBranch) 'branch', 'caller', 'model'],
    'model' => ['model', 'caller'],
    _ => [groupBy, 'model'],
  };

  Map<String, String> filters(Map<String, String> base) => {
    ...base,
    for (var i = 0; i < trail.length; i++) dimensions[i]: trail[i].id,
  };
}

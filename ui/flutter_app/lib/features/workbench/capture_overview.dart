import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'usage_dashboard_view.dart';
import 'workbench_controller.dart';

/// Request evidence and optional usage have independent retention. Both use the
/// same explicit launch/session scope, but their totals are never conflated.
final class CaptureRequestOverview extends StatefulWidget {
  const CaptureRequestOverview({
    required this.controller,
    required this.copy,
    required this.withoutBodies,
    required this.onShowRecords,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool withoutBodies;
  final VoidCallback onShowRecords;

  @override
  State<CaptureRequestOverview> createState() => _CaptureRequestOverviewState();
}

final class _CaptureRequestOverviewState extends State<CaptureRequestOverview>
    with WidgetsBindingObserver {
  Timer? _poller;
  ActivitySummaryScope? _scope;
  ExchangeSummary? _requests;
  RuntimeUsageReport? _usage;
  bool _loading = false;
  bool _failed = false;
  bool _usageFailed = false;
  int _generation = 0;
  int _refreshRevision = 0;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _refreshRevision = widget.controller.captureOverviewRefresh;
    unawaited(_load());
    _poller = Timer.periodic(const Duration(seconds: 15), (_) {
      final state = WidgetsBinding.instance.lifecycleState;
      if (state == null || state == AppLifecycleState.resumed) {
        unawaited(_load());
      }
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) unawaited(_load());
  }

  @override
  void didUpdateWidget(covariant CaptureRequestOverview oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (_scope != widget.controller.captureSummaryScope ||
        _refreshRevision != widget.controller.captureOverviewRefresh) {
      _refreshRevision = widget.controller.captureOverviewRefresh;
      unawaited(_load());
    }
  }

  @override
  void dispose() {
    _poller?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  Future<void> _load() async {
    final scope = widget.controller.captureSummaryScope;
    if (scope == null || (_loading && scope == _scope)) return;
    final generation = ++_generation;
    setState(() {
      if (scope != _scope) {
        _requests = null;
        _usage = null;
      }
      _scope = scope;
      _loading = true;
      _failed = false;
      _usageFailed = false;
    });
    try {
      final requests = await widget.controller.loadActivitySummary(scope);
      if (!mounted || generation != _generation) return;
      setState(() => _requests = requests);
      try {
        final usage = await widget.controller.loadCaptureUsage(
          scope,
          requests.generatedAt,
        );
        if (!mounted || generation != _generation) return;
        setState(() => _usage = usage);
      } on Object {
        if (mounted && generation == _generation) {
          setState(() => _usageFailed = true);
        }
      }
    } on Object {
      if (mounted && generation == _generation) setState(() => _failed = true);
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _loading = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final summary = _requests;
    final usage = _usage;
    final session = _scope?.sessionId.isNotEmpty == true;
    final sessionAvailable =
        widget
            .controller
            .selectedCaptureConversation
            ?.conversation
            .clientIdentity !=
        null;
    return SingleChildScrollView(
      key: const Key('capture-request-overview'),
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 24),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Wrap(
            spacing: 12,
            runSpacing: 8,
            alignment: WrapAlignment.spaceBetween,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              SegmentedButton<bool>(
                key: const Key('capture-summary-scope'),
                showSelectedIcon: false,
                segments: [
                  ButtonSegment(
                    value: false,
                    label: Text(copy('capture.summary.run')),
                  ),
                  ButtonSegment(
                    value: true,
                    enabled: sessionAvailable,
                    label: Text(copy('capture.summary.session')),
                  ),
                ],
                selected: {session},
                onSelectionChanged: (values) => widget.controller
                    .selectCaptureSummarySession(values.single),
              ),
              IconButton(
                key: const Key('capture-summary-refresh'),
                tooltip: copy('status.refresh'),
                onPressed: _loading ? null : () => unawaited(_load()),
                icon: _loading
                    ? const CompactProgressIndicator()
                    : const Icon(Icons.refresh, size: 18),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            copy(
              session
                  ? 'capture.summary.session_scope'
                  : 'capture.summary.run_scope',
            ),
            style: TextStyle(color: context.viberColors.textMuted),
          ),
          if (widget.withoutBodies) ...[
            const SizedBox(height: 6),
            Text(
              copy('capture.summary.hint'),
              style: TextStyle(color: context.viberColors.textFaint),
            ),
          ],
          if (_failed)
            InlineNotice(
              message: copy('capture.summary.load_failed'),
              error: true,
            ),
          if (summary == null)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 24),
              child: _loading
                  ? CompactLoadingMessage(label: copy('common.loading'))
                  : TextButton(
                      onPressed: () => unawaited(_load()),
                      child: Text(copy('common.retry')),
                    ),
            )
          else ...[
            const SizedBox(height: 20),
            _RequestStatus(summary: summary, copy: copy),
            const SizedBox(height: 24),
            Text(
              copy('capture.summary.collected_usage'),
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 8),
            if (_usageFailed)
              InlineNotice(message: copy('usage.refresh.failed'), error: true),
            if (usage == null)
              Text(
                copy(
                  _loading
                      ? 'usage.loading'
                      : 'capture.summary.usage_unavailable',
                ),
              )
            else ...[
              Text(
                copy.format('usage.generated', {
                  'time': _time(usage.generatedAt),
                }),
                key: const Key('capture-usage-updated'),
                style: TextStyle(
                  color: context.viberColors.textFaint,
                  fontSize: 12,
                ),
              ),
              Text(
                copy.format('capture.summary.coverage', {
                  'count': usageIntegerLabel(usage.total!.agentApiCalls),
                  'days': '${usage.collection.retentionDays}',
                }),
                style: TextStyle(color: context.viberColors.textMuted),
              ),
              if (!usage.collection.enabled)
                InlineNotice(message: copy('usage.collection.disabled')),
              if (usage.collection.collectingSince case final since?)
                Text(
                  copy.format('usage.collection.since', {
                    'time': _time(since),
                    'days': '${usage.collection.retentionDays}',
                  }),
                  style: TextStyle(color: context.viberColors.textFaint),
                ),
              const SizedBox(height: 12),
              _CollectedUsage(report: usage, copy: copy),
              UsagePricingNote(report: usage, copy: copy),
              const SizedBox(height: 12),
              UsageGroupTable(
                key: ValueKey(_scope),
                report: usage,
                copy: copy,
                modelsOnly: true,
                loadPage: widget.controller.loadUsagePage,
                onRefresh: () => unawaited(_load()),
              ),
            ],
            const SizedBox(height: 24),
            Text(
              copy('capture.summary.failure_types'),
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 8),
            if (summary.failed == 0) Text(copy('capture.summary.no_failures')),
            for (final failure in summary.failures.entries)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 6),
                child: Row(
                  children: [
                    Icon(
                      Icons.error_outline,
                      size: 16,
                      color: context.viberColors.danger,
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        failure.key.isEmpty
                            ? copy('usage.unknown')
                            : failure.key,
                      ),
                    ),
                    const SizedBox(width: 12),
                    Text(
                      usageIntegerLabel(failure.value),
                      style: const TextStyle(
                        fontFeatures: [FontFeature.tabularFigures()],
                      ),
                    ),
                  ],
                ),
              ),
            if (summary.otherFailures > 0)
              Text(
                copy.format('capture.summary.other_failures', {
                  'count': usageIntegerLabel(summary.otherFailures),
                }),
              ),
            const SizedBox(height: 8),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                onPressed: widget.onShowRecords,
                icon: const Icon(Icons.list_alt, size: 16),
                label: Text(copy('capture.summary.selected_records')),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

String _time(DateTime value) =>
    '${value.toUtc().toIso8601String().substring(0, 19).replaceFirst('T', ' ')} UTC';

final class _RequestStatus extends StatelessWidget {
  const _RequestStatus({required this.summary, required this.copy});
  final ExchangeSummary summary;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final completed = summary.requests - summary.pending;
    final states = [
      ('succeeded', summary.succeeded, colors.verified),
      ('failed', summary.failed, colors.danger),
      ('canceled', summary.canceled, colors.textMuted),
      ('pending', summary.pending, colors.warning),
    ];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Wrap(
          spacing: 24,
          runSpacing: 8,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              copy.format('capture.summary.calls', {
                'count': usageIntegerLabel(summary.requests),
              }),
              key: const Key('capture-summary-total'),
              style: Theme.of(context).textTheme.headlineSmall?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            Text(
              copy.format('capture.summary.success_rate', {
                'rate': completed == 0
                    ? '—'
                    : '${(summary.succeeded * 100 / completed).toStringAsFixed(1)}%',
              }),
              style: TextStyle(color: colors.textMuted),
            ),
          ],
        ),
        const SizedBox(height: 8),
        if (summary.firstObservedAt case final first?)
          Text(
            copy.format('capture.summary.observed_range', {
              'from': _time(first),
              'until': _time(summary.lastObservedAt!),
            }),
            style: TextStyle(color: colors.textFaint, fontSize: 12),
          ),
        Text(
          copy('capture.summary.request_basis'),
          style: TextStyle(color: colors.textFaint),
        ),
        const SizedBox(height: 12),
        if (summary.requests > 0)
          ExcludeSemantics(
            child: SizedBox(
              height: 6,
              child: Row(
                children: [
                  for (final (_, count, color) in states)
                    if (count > 0)
                      Expanded(
                        flex: count,
                        child: ColoredBox(
                          color: color,
                          child: const SizedBox.expand(),
                        ),
                      ),
                ],
              ),
            ),
          ),
        const SizedBox(height: 12),
        LayoutBuilder(
          builder: (context, constraints) {
            final columns = constraints.maxWidth >= 680 ? 4 : 2;
            final width = (constraints.maxWidth - 12 * (columns - 1)) / columns;
            return Wrap(
              spacing: 12,
              runSpacing: 8,
              children: [
                for (final (status, count, color) in states)
                  SizedBox(
                    width: width,
                    child: Row(
                      children: [
                        Icon(Icons.circle, size: 7, color: color),
                        const SizedBox(width: 8),
                        Expanded(child: Text(copy('activity.status.$status'))),
                        Text(
                          usageIntegerLabel(count),
                          key: Key('capture-summary-$status'),
                          style: const TextStyle(
                            fontWeight: FontWeight.w600,
                            fontFeatures: [FontFeature.tabularFigures()],
                          ),
                        ),
                      ],
                    ),
                  ),
              ],
            );
          },
        ),
        const SizedBox(height: 8),
        Text(
          copy.format('capture.summary.updated', {
            'time': _time(summary.generatedAt),
          }),
          style: TextStyle(color: colors.textFaint, fontSize: 12),
        ),
      ],
    );
  }
}

final class _CollectedUsage extends StatelessWidget {
  const _CollectedUsage({required this.report, required this.copy});
  final RuntimeUsageReport report;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, constraints) {
      final columns = constraints.maxWidth >= 820 ? 4 : 2;
      final width = (constraints.maxWidth - 12 * (columns - 1)) / columns;
      final total = report.total!;
      return Wrap(
        spacing: 12,
        runSpacing: 12,
        children: [
          for (final (key, tokens, icon) in [
            ('usage.metric.input', total.tokens.inputUncached, Icons.input),
            ('usage.cache_read', total.tokens.cacheRead, Icons.cached),
            ('usage.metric.output', total.tokens.output, Icons.output),
          ])
            UsageOverviewCard(
              width: width,
              icon: icon,
              label: copy(key),
              value: tokens.observed
                  ? '${tokens.complete ? '' : '≥ '}${usageIntegerLabel(tokens.tokens)}'
                  : '—',
              detail: copy('usage.metric.protocol_declared'),
            ),
          UsageOverviewCard(
            width: width,
            icon: Icons.payments_outlined,
            label: copy('usage.cost.title'),
            value: usageCostLabel(total.cost),
            detail: copy.format('usage.cost.coverage', {
              'known': '${total.cost.completeCalls}',
              'total': '${total.agentApiCalls}',
            }),
          ),
        ],
      );
    },
  );
}

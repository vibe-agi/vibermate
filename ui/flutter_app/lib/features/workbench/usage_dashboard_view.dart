import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

/// Operator-facing projection of retained Runtime User evidence.
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
          subtitle: copy('usage.subtitle'),
        ),
        const Divider(height: 1),
        Expanded(
          child: switch (report) {
            null when controller.serverManagementLoading => Center(
              child: CompactLoadingMessage(label: copy('usage.loading')),
            ),
            null => _UsageUnavailable(
              copy: copy,
              detail: controller.serverManagementError,
              onRetry: () => unawaited(controller.refreshServerManagement()),
            ),
            final value => _UsageReportBody(
              report: value,
              selected: _selectedUser(value),
              copy: copy,
              refreshing: controller.serverManagementLoading,
              onRefresh: () => unawaited(controller.refreshServerManagement()),
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
    required this.onSelectUser,
    required this.detailKey,
    required this.userQuery,
    required this.onUserQueryChanged,
  });

  final RuntimeUsageReport report;
  final RuntimeUserUsage? selected;
  final AppCopy copy;
  final bool refreshing;
  final VoidCallback onRefresh;
  final ValueChanged<String> onSelectUser;
  final Key detailKey;
  final String userQuery;
  final ValueChanged<String> onUserQueryChanged;

  @override
  Widget build(BuildContext context) => SingleChildScrollView(
    key: const Key('usage-dashboard-scroll'),
    padding: const EdgeInsets.fromLTRB(14, 12, 14, 24),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _ReportScope(
          report: report,
          copy: copy,
          refreshing: refreshing,
          onRefresh: onRefresh,
        ),
        if (report.truncated) ...[
          const SizedBox(height: ViberSpacing.md),
          InlineNotice(message: copy('server.usage.truncated'), error: true),
        ],
        const SizedBox(height: ViberSpacing.lg),
        _UsageOverview(report: report, copy: copy),
        const SizedBox(height: ViberSpacing.xl),
        if (report.users.isEmpty)
          _EmptyUsage(copy: copy)
        else ...[
          _UserLedger(
            users: _rankedUsers(report.users),
            selectedUserId: selected?.userId,
            query: userQuery,
            copy: copy,
            onQueryChanged: onUserQueryChanged,
            onSelectUser: onSelectUser,
          ),
          if (selected != null) ...[
            const SizedBox(height: ViberSpacing.xl),
            _UserEvidence(key: detailKey, user: selected!, copy: copy),
          ],
        ],
      ],
    ),
  );
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
              copy.format('usage.generated', {
                'time': _timestamp(report.generatedAt),
              }),
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
        tooltip: copy('status.refresh'),
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
    final users = report.users;
    final activeUsers = users.where((user) => user.active).length;
    final activeRuns = users.fold<int>(0, (sum, user) => sum + user.activeRuns);
    final turns = users.fold<int>(0, (sum, user) => sum + user.turns);
    final input = _sumTokens(users, (tokens) => tokens.inputUncached);
    final output = _sumTokens(users, (tokens) => tokens.output);
    return LayoutBuilder(
      builder: (context, constraints) {
        final columns = constraints.maxWidth >= 900
            ? 5
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
              icon: Icons.people_outline,
              label: copy('usage.metric.users'),
              value: _integer(users.length),
              detail: copy.format('usage.metric.users.detail', {
                'count': '$activeUsers',
              }),
            ),
            _OverviewCard(
              key: const Key('usage-active-runs'),
              width: width,
              icon: Icons.podcasts_outlined,
              label: copy('usage.metric.active_runs'),
              value: _integer(activeRuns),
              detail: copy('usage.metric.active_runs.detail'),
              accent: activeRuns > 0
                  ? context.viberColors.verified
                  : context.viberColors.textFaint,
            ),
            _OverviewCard(
              key: const Key('usage-total-turns'),
              width: width,
              icon: Icons.forum_outlined,
              label: copy('usage.metric.turns'),
              value: _integer(turns),
              detail: copy('usage.metric.turns.detail'),
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
          ],
        );
      },
    );
  }
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
                child: Text(
                  label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.labelMedium?.copyWith(
                    color: context.viberColors.textMuted,
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

final class _UserLedger extends StatelessWidget {
  const _UserLedger({
    required this.users,
    required this.selectedUserId,
    required this.query,
    required this.copy,
    required this.onQueryChanged,
    required this.onSelectUser,
  });

  final List<RuntimeUserUsage> users;
  final String? selectedUserId;
  final String query;
  final AppCopy copy;
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
              onSelectUser: onSelectUser,
            ),
        ],
      ),
    );
  }
}

final class _RankingRows extends StatefulWidget {
  const _RankingRows({
    required this.entries,
    required this.selectedUserId,
    required this.query,
    required this.copy,
    required this.onSelectUser,
  });

  final List<(int, RuntimeUserUsage)> entries;
  final String? selectedUserId;
  final String query;
  final AppCopy copy;
  final ValueChanged<String> onSelectUser;

  @override
  State<_RankingRows> createState() => _RankingRowsState();
}

final class _RankingRowsState extends State<_RankingRows> {
  static const _rowExtent = 70.0;
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
    required this.onPressed,
  });

  final int rank;
  final RuntimeUserUsage user;
  final bool selected;
  final AppCopy copy;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final latest = user.latestContext;
    final consumption = _userConsumptionLabel(user);
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
          padding: const EdgeInsets.fromLTRB(10, 7, 8, 7),
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
                  Text(
                    consumption,
                    style: monoStyle.copyWith(
                      fontSize: ViberType.supporting,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
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
                    style: Theme.of(context).textTheme.labelSmall?.copyWith(
                      color: user.activeRuns > 0
                          ? context.viberColors.verified
                          : context.viberColors.textFaint,
                    ),
                  ),
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
                                'turns': '${user.turns}',
                                'succeeded': '${user.succeeded}',
                              }),
                            ].join(' · '),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                  ),
                  const SizedBox(width: ViberSpacing.md),
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
  const _UserEvidence({required this.user, required this.copy, super.key});

  final RuntimeUserUsage user;
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
                    'turns': '${model.turns}',
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
                'turns': '${workspace.turns}',
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
              'turns': '${session.turns}',
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
    required this.turns,
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
  final int turns;
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
  int turns = 0;
  int succeeded = 0;
  int failed = 0;
  int canceled = 0;
  DateTime? lastActivityAt;

  void add(RuntimeContextUsage context) {
    devices.add(context.machineId);
    captureRuns += context.captureRuns;
    activeRuns += context.activeRuns;
    turns += context.turns;
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
    turns: turns,
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
    final turns = right.turns.compareTo(left.turns);
    if (turns != 0) return turns;
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
    var knownTurns = 0;
    var unknownTurns = 0;
    for (final item in items) {
      final value = select(item);
      tokens += value.tokens;
      knownTurns += value.knownTurns;
      unknownTurns += value.unknownTurns;
    }
    return RuntimeTokenAggregate(
      tokens: tokens,
      knownTurns: knownTurns,
      unknownTurns: unknownTurns,
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

final class _EmptyUsage extends StatelessWidget {
  const _EmptyUsage({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Container(
    padding: const EdgeInsets.all(ViberSpacing.xl),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.surfaceRadius,
    ),
    child: Row(
      children: [
        Icon(
          Icons.person_add_alt_1,
          size: 20,
          color: context.viberColors.route,
        ),
        const SizedBox(width: ViberSpacing.md),
        Expanded(
          child: Text(
            copy('usage.empty'),
            style: Theme.of(context).textTheme.bodyMedium,
          ),
        ),
      ],
    ),
  );
}

final class _ObservedTotal {
  const _ObservedTotal({
    required this.tokens,
    required this.knownTurns,
    required this.unknownTurns,
  });

  final int tokens;
  final int knownTurns;
  final int unknownTurns;

  String get label {
    if (knownTurns == 0) return '—';
    return '${unknownTurns > 0 ? '≥' : ''}${_integer(tokens)}';
  }
}

_ObservedTotal _sumTokens(
  List<RuntimeUserUsage> users,
  RuntimeTokenAggregate Function(RuntimeTokenUsage) select,
) {
  var tokens = 0;
  var known = 0;
  var unknown = 0;
  for (final user in users) {
    final value = select(user.tokens);
    tokens += value.tokens;
    known += value.knownTurns;
    unknown += value.unknownTurns;
  }
  return _ObservedTotal(
    tokens: tokens,
    knownTurns: known,
    unknownTurns: unknown,
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
    final turns = right.turns.compareTo(left.turns);
    if (turns != 0) return turns;
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

String _timestamp(DateTime value) {
  final local = value.toLocal();
  String two(int number) => number.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
}

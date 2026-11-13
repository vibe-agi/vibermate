import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'conversation_timeline.dart';
import 'workbench_controller.dart';

/// What happened to one request, as the audit sees it. A script decision
/// outranks the upstream result: a blocked request is "blocked", not "failed".
enum LedgerResult {
  inProgress,
  forwarded,
  changed,
  blocked,
  scriptFailed,
  failed,
  canceled,
}

LedgerResult ledgerResultOf(ActivityRecord activity) {
  if (activity.scripts.any((script) => script.blocked)) {
    return LedgerResult.blocked;
  }
  if (activity.scripts.any((script) => script.failed)) {
    return LedgerResult.scriptFailed;
  }
  switch (activity.status) {
    case 'failed':
      return LedgerResult.failed;
    case 'canceled':
      return LedgerResult.canceled;
    case 'pending':
      return LedgerResult.inProgress;
  }
  if (activity.scripts.any((script) => script.changedMessage)) {
    return LedgerResult.changed;
  }
  return LedgerResult.forwarded;
}

enum SessionLedgerFilter { all, changed, blocked, failed }

bool ledgerFilterMatches(SessionLedgerFilter filter, ActivityRecord activity) {
  switch (filter) {
    case SessionLedgerFilter.all:
      return true;
    case SessionLedgerFilter.changed:
      // A request can be changed by one script and then fail upstream; it
      // still belongs under "changed".
      return activity.scripts.any((script) => script.changedMessage);
    case SessionLedgerFilter.blocked:
      return activity.scripts.any((script) => script.blocked);
    case SessionLedgerFilter.failed:
      final result = ledgerResultOf(activity);
      return result == LedgerResult.failed ||
          result == LedgerResult.scriptFailed;
  }
}

/// The requests of one session as a ledger: one row per request with what the
/// audit did to it. It reads only the request list already held by the
/// controller, so opening it costs no request per row.
final class SessionLedger extends StatefulWidget {
  const SessionLedger({
    required this.controller,
    required this.copy,
    required this.activities,
    required this.canLoadEarlier,
    required this.loadingEarlier,
    required this.onLoadEarlier,
    required this.onReadConversation,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final List<ActivityRecord> activities;
  final bool canLoadEarlier;
  final bool loadingEarlier;
  final VoidCallback onLoadEarlier;
  final VoidCallback onReadConversation;

  @override
  State<SessionLedger> createState() => _SessionLedgerState();
}

final class _SessionLedgerState extends State<SessionLedger> {
  static const _inspectorWidth = 340.0;
  static const _wideLayout = 900.0;

  SessionLedgerFilter _filter = SessionLedgerFilter.all;
  String? _selectedId;
  String? _openedId;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final ordered = [...widget.activities]
      ..sort((left, right) => right.occurredAt.compareTo(left.occurredAt));
    final counts = {
      for (final filter in SessionLedgerFilter.values)
        filter: ordered
            .where((activity) => ledgerFilterMatches(filter, activity))
            .length,
    };
    final visible = ordered
        .where((activity) => ledgerFilterMatches(_filter, activity))
        .toList(growable: false);
    final selected = ordered
        .where((activity) => activity.id == _selectedId)
        .firstOrNull;

    final opened = ordered
        .where((activity) => activity.id == _openedId)
        .firstOrNull;
    if (opened != null) {
      // The full request reuses the request-record view, so everything it
      // offers (content, raw record, frozen routing) is available from a row.
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton.icon(
              key: const Key('ledger-back'),
              onPressed: () => setState(() => _openedId = null),
              icon: const Icon(Icons.arrow_back, size: 16),
              label: Text(copy('ledger.back')),
            ),
          ),
          Expanded(
            child: EvidenceConversationTimeline(
              key: ValueKey('ledger-exchange-${opened.id}'),
              controller: widget.controller,
              activities: [opened],
              copy: copy,
              exchangeScoped: true,
              showCount: false,
            ),
          ),
        ],
      );
    }

    return LayoutBuilder(
      builder: (context, constraints) {
        final wide = constraints.maxWidth >= _wideLayout;
        final list = Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            _LedgerToolbar(
              copy: copy,
              filter: _filter,
              counts: counts,
              partial: widget.canLoadEarlier,
              onFilter: (filter) => setState(() => _filter = filter),
              onReadConversation: widget.onReadConversation,
            ),
            Expanded(
              child: visible.isEmpty
                  ? CenteredMessage(
                      icon: Icons.receipt_long_outlined,
                      title: copy(
                        ordered.isEmpty
                            ? 'ledger.empty'
                            : 'ledger.empty_filter',
                      ),
                    )
                  : ListView.builder(
                      key: const Key('session-ledger-list'),
                      itemCount: visible.length + 1,
                      itemBuilder: (context, index) {
                        if (index == visible.length) {
                          return _LedgerFooter(
                            copy: copy,
                            canLoadEarlier: widget.canLoadEarlier,
                            loading: widget.loadingEarlier,
                            onLoadEarlier: widget.onLoadEarlier,
                          );
                        }
                        final activity = visible[index];
                        return _LedgerRow(
                          key: ValueKey('ledger-row-${activity.id}'),
                          activity: activity,
                          copy: copy,
                          subtitle: _routeLabel(activity),
                          selected: activity.id == _selectedId,
                          compact: constraints.maxWidth < 560,
                          onTap: () => setState(
                            () => _selectedId = activity.id == _selectedId
                                ? null
                                : activity.id,
                          ),
                        );
                      },
                    ),
            ),
          ],
        );
        if (selected == null) return list;
        final inspector = _LedgerInspector(
          key: ValueKey('ledger-inspector-${selected.id}'),
          activity: selected,
          copy: copy,
          endpoint: widget.controller.activityEndpointLabel(selected),
          account: widget.controller.activityAccountLabel(selected),
          onClose: () => setState(() => _selectedId = null),
          onOpen: () => setState(() => _openedId = selected.id),
        );
        if (!wide) return inspector;
        return Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Expanded(child: list),
            VerticalDivider(width: 1, color: context.viberColors.divider),
            SizedBox(width: _inspectorWidth, child: inspector),
          ],
        );
      },
    );
  }

  String _routeLabel(ActivityRecord activity) {
    final endpoint = widget.controller.activityEndpointLabel(activity);
    final account = widget.controller.activityAccountLabel(activity);
    return [endpoint, account].where((part) => part.isNotEmpty).join(' › ');
  }
}

final class _LedgerToolbar extends StatelessWidget {
  const _LedgerToolbar({
    required this.copy,
    required this.filter,
    required this.counts,
    required this.partial,
    required this.onFilter,
    required this.onReadConversation,
  });

  final AppCopy copy;
  final SessionLedgerFilter filter;
  final Map<SessionLedgerFilter, int> counts;
  final bool partial;
  final ValueChanged<SessionLedgerFilter> onFilter;
  final VoidCallback onReadConversation;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final text = Theme.of(context).textTheme;
    Widget tab(SessionLedgerFilter value, String label, Color? accent) {
      final selected = value == filter;
      final count = counts[value] ?? 0;
      // More requests can still be loaded, so every count is a minimum.
      final shown = partial ? '$count+' : '$count';
      return Semantics(
        button: true,
        selected: selected,
        label: '$label $shown',
        child: InkWell(
          key: Key('ledger-filter-${value.name}'),
          onTap: () => onFilter(value),
          child: Container(
            padding: const EdgeInsets.symmetric(
              horizontal: ViberSpacing.md,
              vertical: ViberSpacing.md,
            ),
            decoration: BoxDecoration(
              border: Border(
                bottom: BorderSide(
                  width: 2,
                  color: selected ? colors.text : Colors.transparent,
                ),
              ),
            ),
            child: ExcludeSemantics(
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    label,
                    style: text.bodyMedium?.copyWith(
                      color: selected ? colors.text : colors.textMuted,
                      fontWeight: selected ? FontWeight.w600 : FontWeight.w400,
                    ),
                  ),
                  const SizedBox(width: ViberSpacing.sm),
                  Text(
                    shown,
                    style: text.bodyMedium?.copyWith(
                      color: count > 0 && accent != null
                          ? accent
                          : colors.textMuted,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      );
    }

    return Container(
      padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.lg),
      decoration: BoxDecoration(
        border: Border(bottom: BorderSide(color: colors.dividerSoft)),
      ),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final tabs = SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                tab(SessionLedgerFilter.all, copy('ledger.filter.all'), null),
                tab(
                  SessionLedgerFilter.changed,
                  copy('ledger.filter.changed'),
                  colors.route,
                ),
                tab(
                  SessionLedgerFilter.blocked,
                  copy('ledger.filter.blocked'),
                  colors.danger,
                ),
                tab(
                  SessionLedgerFilter.failed,
                  copy('ledger.filter.failed'),
                  colors.warning,
                ),
              ],
            ),
          );
          final read = TextButton(
            key: const Key('ledger-read-conversation'),
            onPressed: onReadConversation,
            child: Text(
              copy('ledger.read_conversation'),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          );
          // Narrow panes and large text: the tabs scroll on their own line.
          if (constraints.maxWidth < 560) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [tabs, read],
            );
          }
          return Row(
            children: [
              Expanded(child: tabs),
              const SizedBox(width: ViberSpacing.lg),
              read,
            ],
          );
        },
      ),
    );
  }
}

final class _LedgerRow extends StatelessWidget {
  const _LedgerRow({
    required this.activity,
    required this.copy,
    required this.subtitle,
    required this.selected,
    required this.compact,
    required this.onTap,
    super.key,
  });

  final ActivityRecord activity;
  final AppCopy copy;
  final String subtitle;
  final bool selected;
  final bool compact;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final text = Theme.of(context).textTheme;
    final preview = activity.requestPreview;
    final title = preview == null
        ? activity.title
        : '${preview.text}${preview.truncated ? '…' : ''}';
    final result = _LedgerResultLabel(activity: activity, copy: copy);
    return Semantics(
      button: true,
      selected: selected,
      child: InkWell(
        onTap: onTap,
        child: Container(
          padding: const EdgeInsets.symmetric(
            horizontal: ViberSpacing.lg,
            vertical: ViberSpacing.md,
          ),
          decoration: BoxDecoration(
            color: selected ? colors.selection : null,
            border: Border(bottom: BorderSide(color: colors.dividerSoft)),
          ),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SizedBox(
                width: 68,
                child: Text(
                  ledgerClockTime(activity.occurredAt),
                  style: text.bodySmall?.copyWith(
                    color: colors.textMuted,
                    fontFeatures: const [FontFeature.tabularFigures()],
                  ),
                ),
              ),
              Expanded(
                flex: 5,
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: text.bodyMedium,
                    ),
                    if (subtitle.isNotEmpty)
                      Text(
                        subtitle,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: text.bodySmall?.copyWith(
                          color: colors.textMuted,
                        ),
                      ),
                    if (compact)
                      Padding(
                        padding: const EdgeInsets.only(top: ViberSpacing.xs),
                        child: result,
                      ),
                  ],
                ),
              ),
              if (!compact) ...[
                const SizedBox(width: ViberSpacing.lg),
                Expanded(
                  flex: 3,
                  child: Align(alignment: Alignment.centerLeft, child: result),
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}

final class _LedgerResultLabel extends StatelessWidget {
  const _LedgerResultLabel({required this.activity, required this.copy});

  final ActivityRecord activity;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final result = ledgerResultOf(activity);
    final label = ledgerResultText(copy, activity);
    switch (result) {
      case LedgerResult.forwarded:
        return Text(
          label,
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: Theme.of(context).textTheme.bodyMedium,
        );
      case LedgerResult.inProgress:
      case LedgerResult.canceled:
        return Text(
          label,
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: Theme.of(
            context,
          ).textTheme.bodyMedium?.copyWith(color: colors.textMuted),
        );
      case LedgerResult.changed:
        return _LedgerStatus(label: label, color: colors.route);
      case LedgerResult.blocked:
        return _LedgerStatus(label: label, color: colors.danger);
      case LedgerResult.scriptFailed:
      case LedgerResult.failed:
        return _LedgerStatus(label: label, color: colors.warning);
    }
  }
}

/// The one-line audit result of a request, e.g. "Changed · Mask secrets".
String ledgerResultText(AppCopy copy, ActivityRecord activity) {
  String named(String key, Iterable<ScriptOutcome> scripts, [String? detail]) {
    final names = scripts
        .map((script) => script.name ?? script.scriptId)
        .whereType<String>()
        .toSet()
        .toList(growable: false);
    final suffix =
        detail ??
        (names.isEmpty
            ? null
            : names.length == 1
            ? names.single
            : copy.format('ledger.result.scripts', {'count': names.length}));
    return suffix == null ? copy(key) : '${copy(key)} · $suffix';
  }

  switch (ledgerResultOf(activity)) {
    case LedgerResult.blocked:
      final blocking = activity.scripts.firstWhere((script) => script.blocked);
      return named('ledger.result.blocked', [blocking], blocking.blockReason);
    case LedgerResult.scriptFailed:
      return named(
        'ledger.result.script_failed',
        activity.scripts.where((script) => script.failed),
      );
    case LedgerResult.changed:
      return named(
        'ledger.result.changed',
        activity.scripts.where((script) => script.changedMessage),
      );
    case LedgerResult.failed:
      final reason = activity.reasonCode;
      return reason == null
          ? copy('ledger.result.failed')
          : '${copy('ledger.result.failed')} · $reason';
    case LedgerResult.canceled:
      return copy('ledger.result.canceled');
    case LedgerResult.inProgress:
      return copy('ledger.result.in_progress');
    case LedgerResult.forwarded:
      return copy('ledger.result.forwarded');
  }
}

String ledgerClockTime(DateTime timestamp) {
  final local = timestamp.toLocal();
  String two(int value) => value.toString().padLeft(2, '0');
  return '${two(local.hour)}:${two(local.minute)}:${two(local.second)}';
}

final class _LedgerFooter extends StatelessWidget {
  const _LedgerFooter({
    required this.copy,
    required this.canLoadEarlier,
    required this.loading,
    required this.onLoadEarlier,
  });

  final AppCopy copy;
  final bool canLoadEarlier;
  final bool loading;
  final VoidCallback onLoadEarlier;

  @override
  Widget build(BuildContext context) {
    if (!canLoadEarlier) return const SizedBox(height: ViberSpacing.xl);
    return Padding(
      padding: const EdgeInsets.all(ViberSpacing.lg),
      child: Align(
        alignment: Alignment.centerLeft,
        child: loading
            ? CompactLoadingMessage(label: copy('common.loading'))
            : Wrap(
                crossAxisAlignment: WrapCrossAlignment.center,
                spacing: ViberSpacing.lg,
                children: [
                  TextButton(
                    key: const Key('ledger-load-earlier'),
                    onPressed: onLoadEarlier,
                    child: Text(copy('ledger.load_earlier')),
                  ),
                  Text(
                    copy('ledger.partial'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
      ),
    );
  }
}

final class _LedgerInspector extends StatelessWidget {
  const _LedgerInspector({
    required this.activity,
    required this.copy,
    required this.endpoint,
    required this.account,
    required this.onClose,
    required this.onOpen,
    super.key,
  });

  final ActivityRecord activity;
  final AppCopy copy;
  final String endpoint;
  final String account;
  final VoidCallback onClose;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final text = Theme.of(context).textTheme;
    final preview = activity.requestPreview;
    Widget section(String title) => Padding(
      padding: const EdgeInsets.only(
        top: ViberSpacing.xl,
        bottom: ViberSpacing.sm,
      ),
      child: Text(
        title,
        style: text.bodySmall?.copyWith(color: colors.textMuted),
      ),
    );
    Widget step(String label, String value) => Padding(
      padding: const EdgeInsets.only(bottom: ViberSpacing.md),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: text.bodySmall?.copyWith(color: colors.textMuted)),
          Text(value, style: text.bodyMedium),
        ],
      ),
    );

    return Container(
      color: colors.panel,
      child: ListView(
        key: const Key('ledger-inspector'),
        padding: const EdgeInsets.all(ViberSpacing.xl),
        children: [
          Row(
            children: [
              Expanded(
                child: SelectableText(
                  '${ledgerClockTime(activity.occurredAt)} · ${activity.id}',
                  maxLines: 1,
                  style: text.bodySmall?.copyWith(color: colors.textMuted),
                ),
              ),
              IconButton(
                key: const Key('ledger-inspector-close'),
                tooltip: copy('ledger.detail.close'),
                icon: const Icon(Icons.close, size: 16),
                onPressed: onClose,
              ),
            ],
          ),
          const SizedBox(height: ViberSpacing.sm),
          Text(
            ledgerResultText(copy, activity),
            style: text.titleMedium?.copyWith(fontWeight: FontWeight.w600),
          ),
          if (preview != null) ...[
            const SizedBox(height: ViberSpacing.md),
            Text(
              '${preview.text}${preview.truncated ? '…' : ''}',
              maxLines: 4,
              overflow: TextOverflow.ellipsis,
              style: text.bodyMedium?.copyWith(color: colors.textMuted),
            ),
          ],
          const SizedBox(height: ViberSpacing.lg),
          Align(
            alignment: Alignment.centerLeft,
            child: FilledButton(
              key: const Key('ledger-inspector-open'),
              onPressed: onOpen,
              child: Text(copy('ledger.open_exchange')),
            ),
          ),
          section(copy('ledger.detail.scripts')),
          if (activity.scripts.isEmpty)
            Text(copy('ledger.detail.no_scripts'), style: text.bodyMedium)
          else
            for (final script in activity.scripts)
              _LedgerScriptOutcome(script: script, copy: copy),
          section(copy('ledger.detail.path')),
          step(copy('ledger.detail.agent'), activity.sourceName),
          if (endpoint.isNotEmpty)
            step(copy('ledger.detail.service'), endpoint),
          if (account.isNotEmpty) step(copy('ledger.detail.account'), account),
          if (activity.reasonCode != null)
            step(copy('ledger.detail.reason'), activity.reasonCode!),
        ],
      ),
    );
  }
}

final class _LedgerScriptOutcome extends StatelessWidget {
  const _LedgerScriptOutcome({required this.script, required this.copy});

  final ScriptOutcome script;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final text = Theme.of(context).textTheme;
    final name = script.name ?? script.scriptId ?? copy('ledger.detail.script');
    final revision = script.revision == null ? '' : ' · r${script.revision}';
    final stage = copy(
      script.isRequestStage
          ? 'ledger.detail.stage.request'
          : 'ledger.detail.stage.response',
    );
    final color = script.blocked
        ? colors.danger
        : script.failed
        ? colors.warning
        : script.changedMessage
        ? colors.route
        : colors.textMuted;
    final facts = <String>[
      copy('ledger.outcome.${script.outcome}'),
      if (script.blockReason != null) script.blockReason!,
      if (script.changedMessage && script.headersChanged)
        copy('ledger.detail.headers'),
      if (script.changedMessage && script.bodyChanged)
        copy('ledger.detail.body'),
      if (script.bytesBefore != script.bytesAfter)
        copy.format('ledger.detail.bytes', {
          'before': script.bytesBefore,
          'after': script.bytesAfter,
        }),
      if (script.applications > 1)
        copy.format('ledger.detail.applications', {
          'changed': script.changed,
          'count': script.applications,
        }),
    ];
    return Padding(
      padding: const EdgeInsets.only(bottom: ViberSpacing.md),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: 7, right: ViberSpacing.md),
            child: Icon(Icons.circle, size: 6, color: color),
          ),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('$name$revision · $stage', style: text.bodyMedium),
                Text(
                  facts.join(' · '),
                  style: text.bodySmall?.copyWith(color: colors.textMuted),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// A status dot and label that truncates instead of overflowing its column.
final class _LedgerStatus extends StatelessWidget {
  const _LedgerStatus({required this.label, required this.color});

  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: label,
      container: true,
      child: ExcludeSemantics(
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.circle, size: 6, color: color),
            const SizedBox(width: ViberSpacing.xs),
            Flexible(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: color,
                  fontWeight: FontWeight.w500,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

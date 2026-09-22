import 'package:flutter/material.dart';

import '../../core/api/account_facts_models.dart';
import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

/// Observations belong to this account and are only fetched on demand. Quota
/// and account-wide history have independent freshness and failure states.
final class ProviderAccountFactsPanel extends StatefulWidget {
  const ProviderAccountFactsPanel({
    required this.account,
    required this.controller,
    required this.copy,
    super.key,
  });
  final ProviderAccount account;
  final WorkbenchController controller;
  final AppCopy copy;
  @override
  State<ProviderAccountFactsPanel> createState() =>
      _ProviderAccountFactsPanelState();
}

final class _Observation {
  AccountFacts? facts;
  bool loading = false;
  bool failed = false;
  bool get started => facts != null || loading || failed;
}

final class _ProviderAccountFactsPanelState
    extends State<ProviderAccountFactsPanel> {
  var _quota = _Observation(), _history = _Observation();
  int _generation = 0;
  AppCopy get copy => widget.copy;

  @override
  void didUpdateWidget(covariant ProviderAccountFactsPanel oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.account.id != widget.account.id ||
        oldWidget.account.credentialEpoch != widget.account.credentialEpoch ||
        oldWidget.account.credentialOrigin != widget.account.credentialOrigin) {
      _generation++;
      _quota = _Observation();
      _history = _Observation();
    }
  }

  Future<void> _read({bool history = false}) async {
    final observation = history ? _history : _quota;
    if (observation.loading || !widget.account.usable) return;
    final generation = _generation;
    setState(() {
      observation.loading = true;
      observation.failed = false;
    });
    try {
      final facts = await widget.controller.accountFacts(
        widget.account,
        history: history,
      );
      if (!mounted || generation != _generation) return;
      setState(() => observation.facts = facts);
    } catch (_) {
      if (mounted && generation == _generation) {
        setState(() => observation.failed = true);
      }
    } finally {
      if (mounted && generation == _generation) {
        setState(() => observation.loading = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final origin = Uri.tryParse(widget.account.credentialOrigin);
    if (origin == null || !isChatGPTCodexOrigin(origin)) {
      return const SizedBox.shrink();
    }
    final colors = context.viberColors;
    return Semantics(
      container: true,
      explicitChildNodes: true,
      child: LayoutBuilder(
        builder: (context, constraints) => Padding(
          padding: EdgeInsets.fromLTRB(
            constraints.maxWidth < 600 ? 16 : 36,
            8,
            16,
            8,
          ),
          child: !_quota.started && !_history.started
              ? Wrap(
                  spacing: 12,
                  runSpacing: 4,
                  children: [_action(), _action(history: true)],
                )
              : Container(
                  constraints: const BoxConstraints(maxWidth: 960),
                  decoration: BoxDecoration(
                    color: colors.panel,
                    border: Border.all(color: colors.dividerSoft),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: LayoutBuilder(
                    builder: (context, constraints) {
                      final scale =
                          MediaQuery.textScalerOf(context).scale(14) / 14;
                      final sideBySide = constraints.maxWidth >= 740 * scale;
                      final quota = _section(context);
                      final history = _section(context, history: true);
                      if (!sideBySide) {
                        return Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            quota,
                            Divider(height: 1, color: colors.dividerSoft),
                            history,
                          ],
                        );
                      }
                      return IntrinsicHeight(
                        child: Row(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            Expanded(flex: 3, child: quota),
                            VerticalDivider(
                              width: 1,
                              color: colors.dividerSoft,
                            ),
                            Expanded(flex: 2, child: history),
                          ],
                        ),
                      );
                    },
                  ),
                ),
        ),
      ),
    );
  }

  Widget _action({bool history = false, bool compact = false}) {
    final observation = history ? _history : _quota;
    final label = copy(
      history
          ? observation.facts == null
                ? 'account_facts.query_history'
                : 'account_facts.refresh_history'
          : observation.facts == null
          ? 'account_facts.query_quota'
          : 'account_facts.refresh_quota',
    );
    final key = Key(
      'account-${history ? 'history' : 'quota'}-${widget.account.id}',
    );
    final enabled = !observation.loading && widget.account.usable;
    final icon = observation.loading
        ? const SizedBox.square(
            dimension: 16,
            child: CircularProgressIndicator(strokeWidth: 2),
          )
        : Icon(
            compact
                ? Icons.refresh
                : history
                ? Icons.history
                : Icons.data_usage_outlined,
            size: 16,
          );
    if (compact) {
      return IconButton(
        key: key,
        tooltip: label,
        onPressed: enabled ? () => _read(history: history) : null,
        icon: icon,
      );
    }
    return TextButton.icon(
      key: key,
      onPressed: enabled ? () => _read(history: history) : null,
      icon: icon,
      label: Text(label),
    );
  }

  Widget _section(BuildContext context, {bool history = false}) {
    final observation = history ? _history : _quota;
    final facts = observation.facts;
    final colors = context.viberColors;
    final unavailable =
        facts != null &&
        (facts.state == 'unsupported' || facts.state == 'unavailable');
    return Padding(
      key: Key('account-facts-${history ? 'history' : 'quota'}'),
      padding: const EdgeInsets.all(18),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Wrap(
                  spacing: 10,
                  runSpacing: 6,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    Text(
                      copy(
                        history
                            ? 'account_facts.history_title'
                            : 'account_facts.quota_title',
                      ),
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                    if (!history && facts?.planType != null)
                      Container(
                        padding: const EdgeInsets.symmetric(
                          horizontal: 7,
                          vertical: 2,
                        ),
                        decoration: BoxDecoration(
                          color: colors.panelRaised,
                          borderRadius: BorderRadius.circular(4),
                        ),
                        child: Text(
                          _planName(facts!.planType!),
                          style: Theme.of(context).textTheme.labelSmall,
                        ),
                      ),
                  ],
                ),
              ),
              if (observation.started) _action(history: history, compact: true),
            ],
          ),
          const SizedBox(height: 14),
          if (observation.failed || facts?.state == 'stale') ...[
            _notice(
              context,
              observation.failed
                  ? facts == null
                        ? 'account_facts.failed'
                        : 'account_facts.failed_stale'
                  : 'account_facts.stale',
            ),
            const SizedBox(height: 10),
          ],
          if (unavailable)
            _caption(context, copy('account_facts.unavailable'))
          else if (facts != null)
            history
                ? _historyContent(context, facts)
                : _quotaContent(context, facts)
          else if (observation.loading)
            _caption(context, copy('account_facts.loading'))
          else if (!observation.failed) ...[
            _caption(
              context,
              copy(
                history
                    ? 'account_facts.history_hint'
                    : 'account_facts.quota_hint',
              ),
            ),
            const SizedBox(height: 8),
            _action(history: history),
          ],
          if (facts != null) ...[
            const SizedBox(height: 16),
            Tooltip(
              message: copy.format('account_facts.observed', {
                'time': _fullTime(facts.observedAt),
              }),
              child: _caption(
                context,
                copy.format('account_facts.updated', {
                  'time': _shortTime(facts.observedAt),
                }),
              ),
            ),
          ],
          if (history && facts != null) ...[
            const SizedBox(height: 4),
            _caption(context, copy('account_facts.history_source')),
          ],
        ],
      ),
    );
  }

  Widget _quotaContent(BuildContext context, AccountFacts facts) {
    final hasWindows = facts.limits.any(
      (limit) => limit.primary != null || limit.secondary != null,
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (!hasWindows) _caption(context, copy('account_facts.no_windows')),
        for (final limit in facts.limits)
          Padding(
            padding: const EdgeInsets.only(bottom: 14),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                if (facts.limits.length > 1 || limit.id != 'codex') ...[
                  Text(
                    limit.name ??
                        limit.model ??
                        (limit.id == 'codex' ? 'Codex' : limit.id),
                    style: Theme.of(context).textTheme.labelMedium,
                  ),
                  const SizedBox(height: 8),
                ],
                if (limit.limitReached == true || limit.allowed == false) ...[
                  _notice(
                    context,
                    limit.limitReached == true
                        ? 'account_facts.limit_reached'
                        : 'account_facts.not_allowed',
                  ),
                  const SizedBox(height: 8),
                ],
                if (limit.primary case final window?) _window(context, window),
                if (limit.primary != null && limit.secondary != null)
                  const SizedBox(height: 18),
                if (limit.secondary case final window?)
                  _window(context, window),
              ],
            ),
          ),
        if (facts.credits case final credits?)
          _caption(
            context,
            copy.format('account_facts.credits', {
              'balance': credits.unlimited
                  ? copy('account_facts.unlimited')
                  : credits.balance ?? copy('account_facts.unknown'),
            }),
          ),
      ],
    );
  }

  Widget _window(BuildContext context, AccountQuotaWindow window) {
    final colors = context.viberColors;
    final color = window.usedPercent >= 100
        ? colors.danger
        : window.usedPercent >= 90
        ? colors.warning
        : colors.route;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 16,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              window.windowSeconds == 0
                  ? copy('account_facts.window_unknown')
                  : copy.format('account_facts.window_title', {
                      'duration': _duration(window.windowSeconds),
                    }),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            Text.rich(
              TextSpan(
                children: [
                  TextSpan(
                    text: '${window.usedPercent}%',
                    style: monoStyle.copyWith(
                      fontSize: 22,
                      fontWeight: FontWeight.w600,
                      color: color,
                    ),
                  ),
                  TextSpan(text: '  ${copy('account_facts.used')}'),
                ],
              ),
              style: Theme.of(
                context,
              ).textTheme.bodySmall?.copyWith(color: colors.textMuted),
            ),
          ],
        ),
        const SizedBox(height: 8),
        ExcludeSemantics(
          child: LinearProgressIndicator(
            value: (window.usedPercent / 100).clamp(0, 1),
            minHeight: 6,
            borderRadius: BorderRadius.circular(3),
            color: color,
            backgroundColor: colors.dividerSoft,
          ),
        ),
        const SizedBox(height: 8),
        Tooltip(
          message: copy.format('account_facts.resets', {
            'time': _fullTime(window.resetAt),
          }),
          child: _caption(
            context,
            copy.format('account_facts.reset_short', {
              'time': _shortTime(window.resetAt),
            }),
          ),
        ),
      ],
    );
  }

  Widget _historyContent(BuildContext context, AccountFacts facts) {
    final history = facts.history;
    final tokens = history?.lifetimeTokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _caption(context, copy('account_facts.lifetime_label')),
        const SizedBox(height: 6),
        Text(
          tokens == null ? '—' : _compactNumber(tokens),
          style: monoStyle.copyWith(
            fontSize: 28,
            fontWeight: FontWeight.w600,
            color: context.viberColors.text,
          ),
        ),
        if (tokens == null)
          _caption(context, copy('account_facts.unknown'))
        else
          SelectableText(
            '${_groupedNumber(tokens)} tokens',
            semanticsLabel: copy.format('account_facts.lifetime_exact', {
              'tokens': _groupedNumber(tokens),
            }),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
        if (history?.asOf case final asOf?) ...[
          const SizedBox(height: 8),
          _caption(context, copy.format('account_facts.as_of', {'time': asOf})),
        ],
        if (history?.peakDailyTokens != null ||
            history?.currentStreakDays != null) ...[
          const SizedBox(height: 16),
          Wrap(
            spacing: 24,
            runSpacing: 12,
            children: [
              if (history?.peakDailyTokens case final peak?)
                _historyMetric(context, 'account_facts.peak_daily', peak),
              if (history?.currentStreakDays case final days?)
                _historyMetric(context, 'account_facts.streak_days', days),
            ],
          ),
        ],
        if (history?.partial == true) ...[
          const SizedBox(height: 10),
          _notice(context, 'account_facts.partial'),
        ],
      ],
    );
  }

  Widget _historyMetric(BuildContext context, String label, int value) =>
      Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _caption(context, copy(label)),
          const SizedBox(height: 3),
          Tooltip(
            message: _groupedNumber(value),
            child: Text(_compactNumber(value), style: monoStyle),
          ),
        ],
      );

  Widget _caption(BuildContext context, String text) => Text(
    text,
    style: Theme.of(context).textTheme.bodySmall?.copyWith(
      color: context.viberColors.textMuted,
      fontSize: 12,
    ),
  );

  Widget _notice(BuildContext context, String key) => Text(
    copy(key),
    style: Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.warning),
  );

  String _duration(int seconds) {
    for (final unit in [(86400, 'days'), (3600, 'hours'), (60, 'minutes')]) {
      if (seconds > 0 && seconds % unit.$1 == 0) {
        return copy.format('account_facts.${unit.$2}', {
          'count': seconds ~/ unit.$1,
        });
      }
    }
    return copy.format('account_facts.seconds', {'count': seconds});
  }
}

String _planName(String plan) =>
    const {
      'free': 'Free',
      'plus': 'Plus',
      'pro': 'Pro',
      'team': 'Team',
      'business': 'Business',
      'enterprise': 'Enterprise',
      'edu': 'Edu',
    }[plan.toLowerCase()] ??
    plan;

String _fullTime(DateTime time) => time.toLocal().toString().split('.').first;

String _shortTime(DateTime time) {
  final local = time.toLocal();
  final year = local.year == DateTime.now().year ? '' : '${local.year}/';
  String two(int value) => value.toString().padLeft(2, '0');
  return '$year${two(local.month)}/${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
}

String _groupedNumber(int value) => value.toString().replaceAllMapped(
  RegExp(r'(\d)(?=(\d{3})+(?!\d))'),
  (match) => '${match[1]},',
);

String _compactNumber(int value) {
  for (final unit in [(1000000000, 'B'), (1000000, 'M'), (1000, 'K')]) {
    if (value >= unit.$1) {
      return '${(value / unit.$1).toStringAsFixed(2)}${unit.$2}';
    }
  }
  return '$value';
}

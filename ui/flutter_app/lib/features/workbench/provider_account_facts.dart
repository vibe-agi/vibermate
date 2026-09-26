import 'package:flutter/material.dart';

import '../../core/api/account_facts_models.dart';
import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

/// Keeps the operational quota visible while leaving account-wide history as
/// an explicit, lower-frequency query.
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
  var _history = _Observation();
  int _generation = 0;
  bool _redeeming = false;
  String? _resetNotice;
  AppCopy get copy => widget.copy;

  @override
  void didUpdateWidget(covariant ProviderAccountFactsPanel oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.account.id != widget.account.id ||
        oldWidget.account.credentialEpoch != widget.account.credentialEpoch ||
        oldWidget.account.credentialOrigin != widget.account.credentialOrigin) {
      _generation++;
      _history = _Observation();
      _resetNotice = null;
      _redeeming = false;
    }
  }

  Future<void> _readHistory() async {
    if (_history.loading || !widget.account.usable) return;
    final generation = _generation;
    setState(() {
      _history.loading = true;
      _history.failed = false;
    });
    try {
      final facts = await widget.controller.accountFacts(
        widget.account,
        history: true,
      );
      if (!mounted || generation != _generation) return;
      setState(() => _history.facts = facts);
    } catch (_) {
      if (mounted && generation == _generation) {
        setState(() => _history.failed = true);
      }
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _history.loading = false);
      }
    }
  }

  Future<void> _refreshQuota() async {
    final request = widget.controller.refreshProviderAccountQuota(
      widget.account,
    );
    if (mounted) setState(() {});
    await request;
    if (mounted) setState(() {});
  }

  Future<void> _chooseReset(AccountRateLimitResets resets) async {
    if (_redeeming ||
        widget.controller.providerAccountQuotaFailed(widget.account) ||
        widget.controller.providerAccountQuotaLoading(widget.account) ||
        widget.account.kind != 'codex_oauth' ||
        !widget.account.usable ||
        resets.applicableAvailableCount == 0) {
      return;
    }
    final eligible =
        resets.details?.where((credit) => credit.available).toList() ?? [];
    if (eligible.isEmpty) return;
    final credit = await showDialog<AccountResetCredit>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(copy('account_facts.reset.choose_title')),
        content: SizedBox(
          width: 460,
          height: (eligible.length * 68.0).clamp(68, 300),
          child: ListView.builder(
            itemCount: eligible.length,
            itemBuilder: (context, index) {
              final item = eligible[index];
              return ListTile(
                title: Text(item.title ?? 'Codex'),
                subtitle: Text(
                  item.expiresAt == null
                      ? copy('account_facts.reset.no_expiry')
                      : copy.format('account_facts.reset.expires', {
                          'time': _fullTime(item.expiresAt!),
                        }),
                ),
                onTap: () => Navigator.of(context).pop(item),
              );
            },
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(),
            child: Text(copy('common.cancel')),
          ),
        ],
      ),
    );
    if (!mounted || credit == null || !credit.available) return;
    final selectedAccount = widget.account;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(copy('account_facts.reset.confirm_title')),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(selectedAccount.displayName),
            const SizedBox(height: 8),
            Text(credit.title ?? 'Codex'),
            const SizedBox(height: 12),
            Text(copy('account_facts.reset.confirm_detail')),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(copy('account_facts.reset.confirm')),
          ),
        ],
      ),
    );
    if (!mounted ||
        confirmed != true ||
        widget.account.id != selectedAccount.id ||
        widget.account.revision != selectedAccount.revision ||
        !credit.available) {
      return;
    }
    final generation = _generation;
    setState(() {
      _redeeming = true;
      _resetNotice = null;
    });
    try {
      final result = await widget.controller.redeemAccountResetCredit(
        selectedAccount,
        credit,
      );
      if (!mounted || generation != _generation) return;
      setState(() {
        _resetNotice = switch (result.outcome) {
          'reset' => 'account_facts.reset.applied',
          'already_redeemed' => 'account_facts.reset.already',
          'nothing_to_reset' => 'account_facts.reset.not_needed',
          _ => 'account_facts.reset.none',
        };
      });
      widget.controller.invalidateProviderAccountQuota(selectedAccount);
      await widget.controller.refreshProviderAccountQuota(selectedAccount);
    } on ControlProblem catch (error) {
      if (!mounted || generation != _generation) return;
      widget.controller.invalidateProviderAccountQuota(selectedAccount);
      setState(() {
        _resetNotice = error.reasonCode == 'reset_result_unconfirmed'
            ? 'account_facts.reset.unconfirmed'
            : 'account_facts.reset.failed';
      });
    } catch (_) {
      if (!mounted || generation != _generation) return;
      widget.controller.invalidateProviderAccountQuota(selectedAccount);
      setState(() {
        _resetNotice = 'account_facts.reset.unconfirmed';
      });
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _redeeming = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final origin = Uri.tryParse(widget.account.credentialOrigin);
    if (origin == null || !isChatGPTCodexOrigin(origin)) {
      return const SizedBox.shrink();
    }
    return Semantics(
      container: true,
      explicitChildNodes: true,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 4, 14, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            _quotaSection(context),
            Divider(height: 18, color: context.viberColors.dividerSoft),
            Align(alignment: Alignment.centerLeft, child: _historyAction()),
            if (_history.started) _historySection(context),
          ],
        ),
      ),
    );
  }

  Widget _quotaAction({bool compact = false}) {
    final facts = widget.controller.providerAccountQuota(widget.account);
    final loading = widget.controller.providerAccountQuotaLoading(
      widget.account,
    );
    final label = copy(
      facts == null
          ? 'account_facts.query_quota'
          : 'account_facts.refresh_quota',
    );
    final enabled = !loading && widget.account.usable;
    final icon = loading
        ? const SizedBox.square(
            dimension: 16,
            child: CircularProgressIndicator(strokeWidth: 2),
          )
        : Icon(compact ? Icons.refresh : Icons.data_usage_outlined, size: 16);
    if (compact) {
      return IconButton(
        key: Key('account-quota-${widget.account.id}'),
        tooltip: label,
        onPressed: enabled ? _refreshQuota : null,
        icon: icon,
      );
    }
    return TextButton.icon(
      key: Key('account-quota-${widget.account.id}'),
      onPressed: enabled ? _refreshQuota : null,
      icon: icon,
      label: Text(label),
    );
  }

  Widget _historyAction() {
    final label = copy(
      _history.facts == null
          ? 'account_facts.query_history'
          : 'account_facts.refresh_history',
    );
    return TextButton.icon(
      key: Key('account-history-${widget.account.id}'),
      onPressed: !_history.loading && widget.account.usable
          ? _readHistory
          : null,
      icon: _history.loading
          ? const SizedBox.square(
              dimension: 16,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : const Icon(Icons.history, size: 16),
      label: Text(label),
    );
  }

  Widget _quotaSection(BuildContext context) {
    final facts = widget.controller.providerAccountQuota(widget.account);
    final loading = widget.controller.providerAccountQuotaLoading(
      widget.account,
    );
    final failed = widget.controller.providerAccountQuotaFailed(widget.account);
    final colors = context.viberColors;
    final unavailable =
        facts != null &&
        (facts.state == 'unsupported' || facts.state == 'unavailable');
    return Container(
      key: const Key('account-facts-quota'),
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
                      copy('account_facts.quota_title'),
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                    if (facts?.planType != null)
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
              if (facts != null || loading || failed)
                _quotaAction(compact: true),
            ],
          ),
          const SizedBox(height: 10),
          if (_resetNotice != null) ...[
            _notice(context, _resetNotice!),
            const SizedBox(height: 10),
          ],
          if (failed || facts?.state == 'stale') ...[
            _notice(
              context,
              failed
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
            _quotaContent(context, facts)
          else if (loading)
            Row(
              children: [
                const SizedBox.square(
                  dimension: 14,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                ),
                const SizedBox(width: 8),
                _caption(context, copy('account_facts.loading')),
              ],
            )
          else if (!failed) ...[
            _caption(context, copy('account_facts.quota_hint')),
            const SizedBox(height: 4),
            _quotaAction(),
          ],
          if (facts != null) ...[
            const SizedBox(height: 8),
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
        ],
      ),
    );
  }

  Widget _historySection(BuildContext context) {
    final facts = _history.facts;
    return Container(
      key: const Key('account-facts-history'),
      margin: const EdgeInsets.only(top: 4),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (_history.failed || facts?.state == 'stale') ...[
            _notice(
              context,
              _history.failed
                  ? facts == null
                        ? 'account_facts.failed'
                        : 'account_facts.failed_stale'
                  : 'account_facts.stale',
            ),
            if (facts != null) const SizedBox(height: 8),
          ],
          if (facts != null)
            _historyContent(context, facts)
          else if (_history.loading)
            _caption(context, copy('account_facts.loading')),
          if (facts != null) ...[
            const SizedBox(height: 8),
            _caption(
              context,
              copy.format('account_facts.updated', {
                'time': _shortTime(facts.observedAt),
              }),
            ),
            const SizedBox(height: 2),
            _caption(context, copy('account_facts.history_source')),
          ],
        ],
      ),
    );
  }

  Widget _quotaContent(BuildContext context, AccountFacts facts) {
    final windows = <({AccountQuotaLimit limit, AccountQuotaWindow window})>[];
    for (final limit in facts.limits) {
      if (limit.primary case final window?) {
        windows.add((limit: limit, window: window));
      }
      if (limit.secondary case final window?) {
        windows.add((limit: limit, window: window));
      }
    }
    windows.sort(
      (left, right) =>
          left.window.windowSeconds.compareTo(right.window.windowSeconds),
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (facts.limits.any(
          (limit) => limit.limitReached == true || limit.allowed == false,
        )) ...[
          _notice(
            context,
            facts.limits.any((limit) => limit.limitReached == true)
                ? 'account_facts.limit_reached'
                : 'account_facts.not_allowed',
          ),
          const SizedBox(height: 8),
        ],
        if (windows.isEmpty)
          _caption(context, copy('account_facts.no_windows'))
        else
          LayoutBuilder(
            builder: (context, constraints) {
              final width = constraints.maxWidth >= 560
                  ? (constraints.maxWidth - 10) / 2
                  : constraints.maxWidth;
              return Wrap(
                spacing: 10,
                runSpacing: 8,
                children: [
                  for (final item in windows)
                    SizedBox(
                      width: width,
                      child: _window(
                        context,
                        item.window,
                        name:
                            facts.limits.length > 1 || item.limit.id != 'codex'
                            ? item.limit.name ??
                                  item.limit.model ??
                                  item.limit.id
                            : null,
                      ),
                    ),
                ],
              );
            },
          ),
        if (facts.credits != null || facts.rateLimitResets != null)
          const SizedBox(height: 8),
        if (facts.credits case final credits?)
          _caption(
            context,
            copy.format('account_facts.credits', {
              'balance': credits.unlimited
                  ? copy('account_facts.unlimited')
                  : !credits.hasCredits
                  ? copy('account_facts.no_credits')
                  : credits.balance ?? copy('account_facts.unknown'),
            }),
          ),
        if (facts.rateLimitResets case final resets?) ...[
          if (facts.credits != null) const SizedBox(height: 8),
          _caption(
            context,
            copy.format('account_facts.banked_resets', {
              'count': resets.availableCount,
            }),
          ),
          if (resets.applicableAvailableCount case final applicable?)
            _caption(
              context,
              copy.format('account_facts.applicable_resets', {
                'count': applicable,
              }),
            ),
          if (resets.availableCount > 0 && widget.account.kind != 'codex_oauth')
            _caption(context, copy('account_facts.reset.oauth_only'))
          else if (resets.availableCount > 0 && resets.details == null)
            _caption(context, copy('account_facts.reset.details_unavailable'))
          else if (resets.availableCount > 0 &&
              resets.applicableAvailableCount == 0)
            _caption(context, copy('account_facts.reset.not_needed'))
          else if (resets.availableCount > 0 &&
              resets.details?.any((credit) => credit.available) == false)
            _caption(context, copy('account_facts.reset.none')),
          if (resets.availableCount > 0 &&
              resets.applicableAvailableCount != 0 &&
              resets.details?.any((credit) => credit.available) == true &&
              widget.account.kind == 'codex_oauth' &&
              !widget.controller.previewMode) ...[
            const SizedBox(height: 6),
            TextButton.icon(
              key: Key('account-reset-${widget.account.id}'),
              onPressed:
                  _redeeming ||
                      widget.controller.providerAccountQuotaFailed(
                        widget.account,
                      ) ||
                      widget.controller.providerAccountQuotaLoading(
                        widget.account,
                      )
                  ? null
                  : () => _chooseReset(resets),
              icon: _redeeming
                  ? const SizedBox.square(
                      dimension: 15,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.restart_alt, size: 16),
              label: Text(copy('account_facts.reset.choose')),
            ),
          ],
        ],
      ],
    );
  }

  Widget _window(
    BuildContext context,
    AccountQuotaWindow window, {
    String? name,
  }) {
    final colors = context.viberColors;
    final color = window.usedPercent >= 100
        ? colors.danger
        : window.usedPercent >= 90
        ? colors.warning
        : colors.route;
    final title = window.windowSeconds == 0
        ? copy('account_facts.window_unknown')
        : copy.format('account_facts.window_title', {
            'duration': _duration(window.windowSeconds),
          });
    return Semantics(
      label: '$title ${window.usedPercent}% ${copy('account_facts.used')}',
      child: Container(
        padding: const EdgeInsets.fromLTRB(11, 9, 11, 9),
        decoration: BoxDecoration(
          color: colors.panelRaised,
          borderRadius: BorderRadius.circular(6),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    name == null ? title : '$title · $name',
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.labelMedium,
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  '${window.usedPercent}%',
                  style: monoStyle.copyWith(
                    fontSize: 18,
                    fontWeight: FontWeight.w700,
                    color: color,
                  ),
                ),
                const SizedBox(width: 4),
                _caption(context, copy('account_facts.used')),
              ],
            ),
            const SizedBox(height: 7),
            ExcludeSemantics(
              child: LinearProgressIndicator(
                value: (window.usedPercent / 100).clamp(0, 1),
                minHeight: 4,
                borderRadius: BorderRadius.circular(2),
                color: color,
                backgroundColor: colors.dividerSoft,
              ),
            ),
            const SizedBox(height: 6),
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
        ),
      ),
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
            fontSize: 22,
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

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
    this.showQuotaWindows = true,
    super.key,
  });
  final ProviderAccount account;
  final WorkbenchController controller;
  final AppCopy copy;
  final bool showQuotaWindows;
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
        padding: widget.showQuotaWindows
            ? const EdgeInsets.fromLTRB(14, 2, 14, 8)
            : EdgeInsets.zero,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            _quotaSection(context),
            if (_history.started) _historySection(context),
          ],
        ),
      ),
    );
  }

  Widget _quotaAction() {
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
        : Icon(
            facts == null ? Icons.data_usage_outlined : Icons.refresh,
            size: 16,
          );
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
    final unavailable =
        facts != null &&
        (facts.state == 'unsupported' || facts.state == 'unavailable');
    return Container(
      key: const Key('account-facts-quota'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (_resetNotice != null) ...[
            _notice(context, _resetNotice!),
            const SizedBox(height: 6),
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
            const SizedBox(height: 6),
          ],
          if (unavailable)
            Wrap(
              spacing: 10,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                _caption(context, copy('account_facts.unavailable')),
                _quotaAction(),
                _historyAction(),
              ],
            )
          else if (facts != null)
            _quotaContent(context, facts)
          else if (loading)
            Wrap(
              spacing: 10,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const SizedBox.square(
                      dimension: 14,
                      child: CircularProgressIndicator(strokeWidth: 1.5),
                    ),
                    const SizedBox(width: 8),
                    _caption(context, copy('account_facts.loading')),
                  ],
                ),
                _historyAction(),
              ],
            )
          else
            Wrap(
              spacing: 10,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                if (!failed)
                  _caption(context, copy('account_facts.quota_hint')),
                _quotaAction(),
                _historyAction(),
              ],
            ),
        ],
      ),
    );
  }

  Widget _historySection(BuildContext context) {
    final facts = _history.facts;
    return Container(
      key: const Key('account-facts-history'),
      margin: const EdgeInsets.only(top: 8),
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
    if (!widget.showQuotaWindows) return _quotaMetadata(context, facts);
    return LayoutBuilder(
      builder: (context, constraints) {
        final windowsPanel = _quotaWindows(context, facts, windows);
        final metadata = _quotaMetadata(context, facts);
        if (windows.isNotEmpty && constraints.maxWidth >= 720) {
          final width = windows.length == 1
              ? 320.0
              : (constraints.maxWidth - 300).clamp(420.0, 640.0).toDouble();
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SizedBox(width: width, child: windowsPanel),
              const SizedBox(width: 16),
              Expanded(child: metadata),
            ],
          );
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [windowsPanel, const SizedBox(height: 8), metadata],
        );
      },
    );
  }

  Widget _quotaWindows(
    BuildContext context,
    AccountFacts facts,
    List<({AccountQuotaLimit limit, AccountQuotaWindow window})> windows,
  ) {
    if (windows.isEmpty) {
      return _caption(context, copy('account_facts.no_windows'));
    }
    return LayoutBuilder(
      builder: (context, constraints) {
        final columns = windows.length > 1 && constraints.maxWidth >= 420
            ? 2
            : 1;
        final available = columns == 2
            ? (constraints.maxWidth - 8) / 2
            : constraints.maxWidth;
        final width = columns == 1 && available > 360 ? 360.0 : available;
        return Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            for (final item in windows)
              SizedBox(
                width: width,
                child: _window(
                  context,
                  item.window,
                  name: facts.limits.length > 1 || item.limit.id != 'codex'
                      ? item.limit.name ?? item.limit.model ?? item.limit.id
                      : null,
                ),
              ),
          ],
        );
      },
    );
  }

  Widget _quotaMetadata(BuildContext context, AccountFacts facts) {
    final credits = facts.credits;
    final resets = facts.rateLimitResets;
    final limitReached = facts.limits.any(
      (limit) => limit.limitReached == true || limit.allowed == false,
    );
    final resetAvailable =
        resets != null &&
        resets.availableCount > 0 &&
        resets.applicableAvailableCount != 0 &&
        resets.details?.any((credit) => credit.available) == true &&
        widget.account.kind == 'codex_oauth' &&
        !widget.controller.previewMode;
    final resetHint = resets != null && resets.availableCount > 0
        ? widget.account.kind != 'codex_oauth'
              ? 'account_facts.reset.oauth_only'
              : resets.details == null
              ? 'account_facts.reset.details_unavailable'
              : resets.applicableAvailableCount == 0
              ? 'account_facts.reset.not_needed'
              : resets.details?.any((credit) => credit.available) == false
              ? 'account_facts.reset.none'
              : null
        : null;
    final summary = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 10,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              copy('account_facts.quota_title'),
              style: Theme.of(context).textTheme.titleSmall,
            ),
            if (limitReached)
              _notice(
                context,
                facts.limits.any((limit) => limit.limitReached == true)
                    ? 'account_facts.limit_reached'
                    : 'account_facts.not_allowed',
              ),
          ],
        ),
        if (credits != null || resets != null) ...[
          const SizedBox(height: 5),
          Wrap(
            spacing: 14,
            runSpacing: 3,
            children: [
              if (credits != null)
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
              if (resets != null)
                _caption(
                  context,
                  copy.format('account_facts.banked_resets', {
                    'count': resets.availableCount,
                  }),
                ),
              if (resets?.applicableAvailableCount case final applicable?)
                _caption(
                  context,
                  copy.format('account_facts.applicable_resets', {
                    'count': applicable,
                  }),
                ),
            ],
          ),
        ],
        if (resetHint != null) ...[
          const SizedBox(height: 3),
          _caption(context, copy(resetHint)),
        ],
        const SizedBox(height: 6),
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
    );
    final actions = Wrap(
      spacing: 4,
      runSpacing: 2,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        _quotaAction(),
        _historyAction(),
        if (resetAvailable)
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
    );
    if (widget.showQuotaWindows) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [summary, const SizedBox(height: 3), actions],
      );
    }
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: BorderRadius.circular(6),
      ),
      child: LayoutBuilder(
        builder: (context, constraints) {
          if (constraints.maxWidth < 560) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [summary, const SizedBox(height: 8), actions],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(child: summary),
              const SizedBox(width: 16),
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 380),
                child: actions,
              ),
            ],
          );
        },
      ),
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
        padding: const EdgeInsets.fromLTRB(10, 7, 10, 7),
        decoration: BoxDecoration(
          color: colors.panelRaised,
          borderRadius: BorderRadius.circular(5),
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
                    fontSize: 16,
                    fontWeight: FontWeight.w700,
                    color: color,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 5),
            ExcludeSemantics(
              child: LinearProgressIndicator(
                value: (window.usedPercent / 100).clamp(0, 1),
                minHeight: 3,
                borderRadius: BorderRadius.circular(2),
                color: color,
                backgroundColor: colors.dividerSoft,
              ),
            ),
            const SizedBox(height: 4),
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

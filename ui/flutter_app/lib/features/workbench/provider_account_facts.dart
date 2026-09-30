import 'dart:async';

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
  AppCopy get copy => widget.copy;

  @override
  void didUpdateWidget(covariant ProviderAccountFactsPanel oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.account.id != widget.account.id ||
        oldWidget.account.credentialEpoch != widget.account.credentialEpoch ||
        oldWidget.account.settingsRevision != widget.account.settingsRevision ||
        oldWidget.account.credentialOrigin != widget.account.credentialOrigin) {
      _generation++;
      _history = _Observation();
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
            if (widget.showQuotaWindows) _quotaSection(context),
            if (_history.started) _historySection(context),
            if (!widget.showQuotaWindows) _compactActions(context),
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

  Widget _credentialAction() {
    final refreshing =
        widget.controller.refreshingProviderAccountId == widget.account.id;
    return Tooltip(
      message: copy('provider_accounts.credential.refresh_hint'),
      child: TextButton.icon(
        key: Key('account-credential-refresh-${widget.account.id}'),
        onPressed: widget.controller.inventoryMutating || !widget.account.usable
            ? null
            : () => widget.controller.refreshProviderAccountCredential(
                widget.account,
              ),
        icon: refreshing
            ? const SizedBox.square(
                dimension: 15,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            : const Icon(Icons.refresh, size: 16),
        label: Text(copy('provider_accounts.refresh.action')),
      ),
    );
  }

  Widget _compactActions(BuildContext context) {
    return Container(
      key: const Key('account-facts-quota'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (widget.account.tokenInfo != null || _history.started)
            const Divider(height: 20),
          Wrap(
            spacing: 4,
            runSpacing: 2,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              _historyAction(),
              if (widget.account.kind == 'codex_oauth') _credentialAction(),
            ],
          ),
        ],
      ),
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
                        : accountCreditsLabel(
                            credits.balance,
                            copy('account_facts.unknown'),
                          ),
                  }),
                ),
              if (resets != null)
                ProviderAccountResetCreditsButton(
                  account: widget.account,
                  controller: widget.controller,
                  copy: copy,
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
      children: [_quotaAction(), _historyAction()],
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [summary, const SizedBox(height: 3), actions],
    );
  }

  Widget _window(
    BuildContext context,
    AccountQuotaWindow window, {
    String? name,
  }) {
    final colors = context.viberColors;
    final color = accountQuotaUsageColor(colors, window.usedPercent);
    final title = window.windowSeconds == 0
        ? copy('account_facts.window_unknown')
        : copy.format('account_facts.window_title', {
            'duration': _quotaDuration(window.windowSeconds, copy),
          });
    return Semantics(
      label: '$title ${window.usedPercent}%',
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
}

String _quotaDuration(int seconds, AppCopy copy) {
  for (final unit in [(86400, 'days'), (3600, 'hours'), (60, 'minutes')]) {
    if (seconds > 0 && seconds % unit.$1 == 0) {
      return copy.format('account_facts.${unit.$2}', {
        'count': seconds ~/ unit.$1,
      });
    }
  }
  return copy.format('account_facts.seconds', {'count': seconds});
}

String accountCreditsLabel(String? raw, String unknown) {
  final value = double.tryParse(raw ?? '');
  return value != null && value.isFinite && value >= 0
      ? value.toStringAsFixed(2)
      : unknown;
}

bool resetCreditExpiresSoon(AccountResetCredit credit, DateTime now) =>
    credit.availableAt(now) &&
    credit.expiresAt != null &&
    !credit.expiresAt!.isAfter(now.add(const Duration(days: 3)));

/// The same voucher entry is used in the account table and quota panel.
/// Selecting a voucher never redeems it: one explicit confirmation owns all warnings.
final class ProviderAccountResetCreditsButton extends StatefulWidget {
  const ProviderAccountResetCreditsButton({
    required this.account,
    required this.controller,
    required this.copy,
    this.clock,
    super.key,
  });
  final ProviderAccount account;
  final WorkbenchController controller;
  final AppCopy copy;
  final DateTime Function()? clock;

  @override
  State<ProviderAccountResetCreditsButton> createState() =>
      _ProviderAccountResetCreditsButtonState();
}

final class _ProviderAccountResetCreditsButtonState
    extends State<ProviderAccountResetCreditsButton> {
  bool _busy = false;
  int _generation = 0;
  late final Timer _tick;
  DateTime get _now => (widget.clock ?? DateTime.now)().toUtc();

  @override
  void initState() {
    super.initState();
    _tick = Timer.periodic(const Duration(minutes: 1), (_) {
      if (mounted) setState(() {});
    });
  }

  @override
  void didUpdateWidget(covariant ProviderAccountResetCreditsButton oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.account.id != widget.account.id ||
        oldWidget.account.revision != widget.account.revision ||
        oldWidget.account.credentialEpoch != widget.account.credentialEpoch) {
      _generation++;
      _busy = false;
    }
  }

  @override
  void dispose() {
    _tick.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: widget.controller,
    builder: (context, _) {
      final resets = widget.controller
          .providerAccountQuota(widget.account)
          ?.rateLimitResets;
      if (resets == null) return const SizedBox.shrink();
      final expiring =
          resets.details
              ?.where((credit) => resetCreditExpiresSoon(credit, _now))
              .length ??
          0;
      final label = widget.copy.format('account_facts.banked_resets', {
        'count': resets.availableCount,
      });
      return Tooltip(
        message: expiring > 0
            ? '$label · ${widget.copy.format('account_facts.reset.expiring_soon', {'count': expiring})}'
            : '$label · ${widget.copy('account_facts.reset.view')}',
        child: TextButton.icon(
          key: Key('provider-account-resets-${widget.account.id}'),
          onPressed: _busy ? null : _open,
          style: TextButton.styleFrom(
            foregroundColor: expiring > 0
                ? context.viberColors.danger
                : context.viberColors.textMuted,
            minimumSize: const Size(32, 28),
            padding: const EdgeInsets.symmetric(horizontal: 4),
            textStyle: Theme.of(context).textTheme.bodySmall,
            tapTargetSize: MaterialTapTargetSize.shrinkWrap,
          ),
          icon: _busy
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : const Icon(Icons.confirmation_number_outlined, size: 14),
          label: Text('${resets.availableCount}', semanticsLabel: label),
        ),
      );
    },
  );

  Future<void> _open() async {
    if (_busy) return;
    final account = widget.account;
    final generation = _generation;
    final copy = widget.copy;
    var facts = widget.controller.providerAccountQuota(account);
    if (facts?.rateLimitResets?.details == null && account.usable) {
      setState(() => _busy = true);
      await widget.controller.refreshProviderAccountQuota(account);
      if (!mounted || generation != _generation) return;
      setState(() => _busy = false);
      facts = widget.controller.providerAccountQuota(account);
    }
    if (!mounted || facts == null) return;
    final observed = facts;
    final resets = observed.rateLimitResets;
    final details = [...?resets?.details]
      ..sort((a, b) {
        if (a.expiresAt == null) {
          return b.expiresAt == null ? a.id.compareTo(b.id) : 1;
        }
        if (b.expiresAt == null) return -1;
        final compared = a.expiresAt!.compareTo(b.expiresAt!);
        return compared == 0 ? a.id.compareTo(b.id) : compared;
      });
    AccountResetCredit? selected;
    final credit = await showDialog<AccountResetCredit>(
      context: context,
      builder: (dialogContext) => StatefulBuilder(
        builder: (dialogContext, change) {
          final now = _now;
          final usable = details
              .where((item) => item.availableAt(now))
              .toList();
          final firstExpiry = usable
              .map((item) => item.expiresAt)
              .whereType<DateTime>()
              .firstOrNull;
          final warnings = <String>[];
          if (selected != null) {
            final windows = accountQuotaWindows(observed);
            final weekly = windows
                .where((window) => window.windowSeconds == 7 * 86400)
                .firstOrNull;
            if (weekly != null && weekly.usedPercent > 0) {
              warnings.add(
                copy.format('account_facts.reset.weekly_used', {
                  'percent': weekly.usedPercent,
                }),
              );
            }
            if (firstExpiry != null &&
                (selected!.expiresAt == null ||
                    selected!.expiresAt!.isAfter(firstExpiry))) {
              warnings.add(
                copy.format('account_facts.reset.not_earliest', {
                  'time': _fullTime(firstExpiry),
                }),
              );
            }
            final soon =
                windows
                    .where(
                      (window) =>
                          window.resetAt.isAfter(now) &&
                          !window.resetAt.isAfter(
                            now.add(const Duration(hours: 3)),
                          ),
                    )
                    .toList()
                  ..sort((a, b) => a.resetAt.compareTo(b.resetAt));
            if (soon.isNotEmpty) {
              warnings.add(
                copy.format('account_facts.reset.natural_soon', {
                  'window': _quotaDuration(soon.first.windowSeconds, copy),
                  'time': _fullTime(soon.first.resetAt),
                }),
              );
            }
          }
          final canRedeem =
              selected?.availableAt(now) == true &&
              account.kind == 'codex_oauth' &&
              account.usable &&
              !widget.controller.previewMode &&
              observed.state == 'known' &&
              !widget.controller.providerAccountQuotaFailed(account) &&
              !widget.controller.providerAccountQuotaLoading(account) &&
              resets?.applicableAvailableCount != 0 &&
              generation == _generation;
          return AlertDialog(
            key: const Key('account-reset-dialog'),
            title: Text(copy('account_facts.reset.choose_title')),
            content: SizedBox(
              width: 520,
              child: ConstrainedBox(
                constraints: BoxConstraints(
                  maxHeight: MediaQuery.sizeOf(dialogContext).height * .62,
                ),
                child: SingleChildScrollView(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        account.displayName,
                        style: Theme.of(dialogContext).textTheme.titleSmall,
                      ),
                      if (resets?.details == null)
                        Text(copy('account_facts.reset.details_unavailable'))
                      else if (details.isEmpty)
                        Text(copy('account_facts.reset.none')),
                      for (final item in details)
                        ListTile(
                          key: Key('account-reset-credit-${item.id}'),
                          contentPadding: EdgeInsets.zero,
                          selected: identical(selected, item),
                          leading: Icon(
                            identical(selected, item)
                                ? Icons.radio_button_checked
                                : Icons.radio_button_off,
                            color: item.availableAt(now)
                                ? context.viberColors.route
                                : context.viberColors.textFaint,
                          ),
                          title: Text(item.title ?? 'Codex'),
                          subtitle: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              if (item.description case final description?)
                                Text(description),
                              Text(
                                item.expiresAt == null
                                    ? copy('account_facts.reset.no_expiry')
                                    : copy.format(
                                        'account_facts.reset.expires',
                                        {'time': _fullTime(item.expiresAt!)},
                                      ),
                                style: TextStyle(
                                  color: resetCreditExpiresSoon(item, now)
                                      ? context.viberColors.danger
                                      : null,
                                ),
                              ),
                              Text(
                                copy.format('account_facts.reset.granted', {
                                  'time': _fullTime(item.grantedAt),
                                }),
                              ),
                              Text(
                                item.status == 'available' &&
                                        item.expiresAt != null &&
                                        !item.expiresAt!.isAfter(now)
                                    ? copy('account_facts.reset.status_expired')
                                    : copy.maybe(
                                            'account_facts.reset.status_${item.status}',
                                          ) ??
                                          item.status,
                              ),
                              if (item.resetType != 'codex_rate_limits')
                                Text(item.resetType),
                              Text(
                                'ID: ${item.id}',
                                style: Theme.of(
                                  dialogContext,
                                ).textTheme.bodySmall,
                              ),
                            ],
                          ),
                          onTap: item.availableAt(now)
                              ? () => change(() => selected = item)
                              : null,
                        ),
                      if (selected != null) ...[
                        const Divider(),
                        Text(
                          copy('account_facts.reset.confirm_title'),
                          style: Theme.of(dialogContext).textTheme.titleSmall,
                        ),
                        const SizedBox(height: 8),
                        Text(copy('account_facts.reset.confirm_detail')),
                        if (warnings.isNotEmpty)
                          Container(
                            key: const Key('account-reset-warnings'),
                            margin: const EdgeInsets.only(top: 10),
                            padding: const EdgeInsets.all(10),
                            decoration: BoxDecoration(
                              color: context.viberColors.warning.withValues(
                                alpha: .10,
                              ),
                              borderRadius: BorderRadius.circular(6),
                            ),
                            child: Text(warnings.join('\n\n')),
                          ),
                      ],
                    ],
                  ),
                ),
              ),
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(dialogContext),
                child: Text(copy('common.cancel')),
              ),
              FilledButton(
                key: const Key('account-reset-confirm'),
                onPressed: canRedeem
                    ? () => Navigator.pop(dialogContext, selected)
                    : null,
                child: Text(copy('account_facts.reset.confirm')),
              ),
            ],
          );
        },
      ),
    );
    if (!mounted ||
        credit == null ||
        generation != _generation ||
        !credit.availableAt(_now)) {
      return;
    }
    setState(() => _busy = true);
    String notice;
    var confirmed = false;
    try {
      final result = await widget.controller.redeemAccountResetCredit(
        account,
        credit,
      );
      notice = switch (result.outcome) {
        'reset' => 'account_facts.reset.applied',
        'already_redeemed' => 'account_facts.reset.already',
        'nothing_to_reset' => 'account_facts.reset.not_needed',
        _ => 'account_facts.reset.none',
      };
      confirmed = true;
    } on ControlProblem catch (error) {
      notice = error.reasonCode == 'reset_result_unconfirmed'
          ? 'account_facts.reset.unconfirmed'
          : 'account_facts.reset.failed';
    } catch (_) {
      notice = 'account_facts.reset.unconfirmed';
    }
    if (!mounted || generation != _generation) return;
    ScaffoldMessenger.of(
      context,
    ).showSnackBar(SnackBar(content: Text(copy(notice))));
    // Publish the result before invalidation removes this quota-derived chip.
    widget.controller.invalidateProviderAccountQuota(account);
    if (confirmed) await widget.controller.refreshProviderAccountQuota(account);
    if (mounted && generation == _generation) setState(() => _busy = false);
  }
}

String _fullTime(DateTime time) => time.toLocal().toString().split('.').first;

Color accountQuotaUsageColor(ViberColors colors, int percent) => percent >= 90
    ? colors.danger
    : percent >= 70
    ? colors.warning
    : colors.route;

/// Keep the provider's main bucket separate from model-specific quotas. An
/// ambiguous multi-bucket response has no comparable account-wide percentage.
List<AccountQuotaWindow> accountQuotaWindows(AccountFacts? facts) {
  if (facts == null || !{'known', 'stale'}.contains(facts.state)) {
    return const [];
  }
  final main =
      facts.limits.where((limit) => limit.id == 'codex').firstOrNull ??
      (facts.limits.length == 1 ? facts.limits.single : null);
  return [?main?.primary, ?main?.secondary]
    ..sort((left, right) => right.windowSeconds.compareTo(left.windowSeconds));
}

String accountQuotaCountdown(DateTime reset, DateTime now, AppCopy copy) {
  final remaining = reset.difference(now);
  if (remaining <= Duration.zero) return copy('account_facts.reset_due');
  if (remaining.inDays > 0) {
    return '${remaining.inDays}d${remaining.inHours % 24}h';
  }
  if (remaining.inHours > 0) {
    return '${remaining.inHours}h${remaining.inMinutes % 60}m';
  }
  return remaining.inMinutes > 0 ? '${remaining.inMinutes}m' : '<1m';
}

/// Two independent quota windows, not a blended account percentage.
final class ProviderAccountQuotaMini extends StatefulWidget {
  const ProviderAccountQuotaMini({
    required this.facts,
    required this.loading,
    required this.failed,
    required this.copy,
    this.clock,
    super.key,
  });
  final AccountFacts? facts;
  final bool loading, failed;
  final AppCopy copy;
  final DateTime Function()? clock;

  @override
  State<ProviderAccountQuotaMini> createState() =>
      _ProviderAccountQuotaMiniState();
}

final class _ProviderAccountQuotaMiniState
    extends State<ProviderAccountQuotaMini>
    with WidgetsBindingObserver {
  late final Timer _clockTick;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _clockTick = Timer.periodic(const Duration(minutes: 1), (_) {
      if (_visible(WidgetsBinding.instance.lifecycleState)) setState(() {});
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (_visible(state)) setState(() {});
  }

  // A desktop window that lost focus is inactive but still on screen.
  static bool _visible(AppLifecycleState? state) =>
      state == null ||
      state == AppLifecycleState.resumed ||
      state == AppLifecycleState.inactive;

  @override
  void dispose() {
    _clockTick.cancel();
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final facts = widget.facts, copy = widget.copy;
    final loading = widget.loading, failed = widget.failed;
    final colors = context.viberColors;
    final windows = accountQuotaWindows(facts);
    final stale = failed || facts?.state == 'stale';
    final now = (widget.clock ?? DateTime.now)();
    final caption = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: colors.textMuted);
    if (windows.isEmpty) {
      return Text(
        copy(
          failed
              ? 'account_facts.failed'
              : loading
              ? 'account_facts.loading'
              : 'account_facts.unavailable',
        ),
        style: caption,
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final window in windows)
          Padding(
            padding: const EdgeInsets.only(top: 4),
            child: Tooltip(
              message:
                  '${copy.format('account_facts.resets', {'time': _fullTime(window.resetAt)})}\n'
                  '${copy.format('account_facts.observed', {'time': _fullTime(facts!.observedAt)})}',
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          '${_compactQuotaPeriod(window.windowSeconds)} · ${window.usedPercent}%',
                          style: monoStyle.copyWith(
                            fontSize: 11,
                            color: accountQuotaUsageColor(
                              colors,
                              window.usedPercent,
                            ),
                          ),
                        ),
                      ),
                      const SizedBox(width: 6),
                      Text(
                        accountQuotaCountdown(window.resetAt, now, copy),
                        style: monoStyle.copyWith(
                          fontSize: 11,
                          color: window.resetAt.isAfter(now)
                              ? colors.textMuted
                              : colors.warning,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 3),
                  ExcludeSemantics(
                    child: LinearProgressIndicator(
                      value: (window.usedPercent / 100).clamp(0, 1),
                      minHeight: 2,
                      color: accountQuotaUsageColor(colors, window.usedPercent),
                      backgroundColor: colors.dividerSoft,
                    ),
                  ),
                ],
              ),
            ),
          ),
        if (stale)
          Text(
            copy('account_facts.stale'),
            style: caption?.copyWith(color: colors.warning),
          ),
        if ((facts?.limits.length ?? 0) > 1)
          Text(copy('account_facts.additional_limits'), style: caption),
      ],
    );
  }
}

String _compactQuotaPeriod(int seconds) {
  for (final (size, unit) in [(86400, 'd'), (3600, 'h'), (60, 'm')]) {
    if (seconds > 0 && seconds % size == 0) return '${seconds ~/ size}$unit';
  }
  return seconds > 0 ? '${seconds}s' : '—';
}

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

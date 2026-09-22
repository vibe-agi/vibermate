import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';

/// Keeps expiry visible while folding lower-frequency identity and time fields.
/// Only safe backend projections enter this widget, never token bytes.
final class ProviderAccountTokenDetails extends StatefulWidget {
  const ProviderAccountTokenDetails({
    required this.account,
    required this.copy,
    super.key,
  });

  final ProviderAccount account;
  final AppCopy copy;

  @override
  State<ProviderAccountTokenDetails> createState() =>
      _ProviderAccountTokenDetailsState();
}

final class _ProviderAccountTokenDetailsState
    extends State<ProviderAccountTokenDetails> {
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final info = widget.account.tokenInfo;
    if (info == null) return const SizedBox.shrink();
    final account = widget.account;
    final copy = widget.copy;
    final colors = context.viberColors;
    final expiry = info.expiresAt;
    final expired = expiry != null && !expiry.isAfter(DateTime.now());
    final fields = <(String, String)>[
      if (info.email case final email?)
        ('provider_accounts.token.email', email),
      if (info.planType case final plan?)
        ('provider_accounts.token.plan', plan),
      (
        'provider_accounts.token.authenticated',
        _timeOrMissing(info.authenticatedAt),
      ),
      ('provider_accounts.token.issued', _timeOrMissing(info.issuedAt)),
      ('provider_accounts.token.expires', _timeOrMissing(expiry)),
      if (account.codexOAuth case final oauth?)
        ('provider_accounts.token.refreshed', _timestamp(oauth.lastRefresh)),
      if (info.chatgptAccountId case final id?)
        ('provider_accounts.token.account', id),
      if (info.userId case final id?) ('provider_accounts.token.user', id),
    ];
    return Padding(
      key: Key('provider-account-token-${account.id}'),
      padding: const EdgeInsets.fromLTRB(36, 4, 16, 0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 12,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              if (info.email != null &&
                  info.email != account.displayName &&
                  info.email != account.codexOAuth?.email)
                Text(info.email!, style: Theme.of(context).textTheme.bodySmall),
              if (info.planType != null &&
                  info.planType != account.codexOAuth?.planType)
                Text(
                  copy.format('provider_accounts.token.plan_summary', {
                    'plan': info.planType!,
                  }),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              if (expiry != null)
                Text(
                  copy.format(
                    expired
                        ? 'provider_accounts.token.expired_summary'
                        : 'provider_accounts.token.expiry_summary',
                    {'time': _timestamp(expiry)},
                  ),
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: expired ? colors.warning : colors.textMuted,
                  ),
                ),
              TextButton.icon(
                key: Key('provider-account-token-toggle-${account.id}'),
                onPressed: () => setState(() => _expanded = !_expanded),
                icon: Icon(
                  _expanded ? Icons.expand_less : Icons.expand_more,
                  size: 16,
                ),
                label: Text(
                  copy(
                    _expanded
                        ? 'provider_accounts.token.hide'
                        : 'provider_accounts.token.details',
                  ),
                ),
              ),
            ],
          ),
          if (_expanded)
            Container(
              width: double.infinity,
              margin: const EdgeInsets.only(top: 6, bottom: 6),
              padding: const EdgeInsets.only(left: 12, top: 8, bottom: 8),
              decoration: BoxDecoration(
                border: Border(left: BorderSide(color: colors.divider)),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  LayoutBuilder(
                    builder: (context, constraints) {
                      final columns = constraints.maxWidth >= 900
                          ? 3
                          : constraints.maxWidth >= 600
                          ? 2
                          : 1;
                      final width =
                          (constraints.maxWidth - (columns - 1) * 16) / columns;
                      return Wrap(
                        spacing: 16,
                        runSpacing: 12,
                        children: [
                          for (final (label, value) in fields)
                            SizedBox(
                              width: width,
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    copy(label),
                                    style: Theme.of(context).textTheme.bodySmall
                                        ?.copyWith(color: colors.textMuted),
                                  ),
                                  const SizedBox(height: 3),
                                  SelectableText(value, style: monoStyle),
                                ],
                              ),
                            ),
                        ],
                      );
                    },
                  ),
                  const SizedBox(height: 12),
                  Text(
                    copy('provider_accounts.token.unverified'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  if (account.kind == 'bearer_token') ...[
                    const SizedBox(height: 4),
                    Text(
                      copy('provider_accounts.token.manual'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ],
              ),
            ),
        ],
      ),
    );
  }

  String _timeOrMissing(DateTime? value) => value == null
      ? widget.copy('provider_accounts.token.not_provided')
      : _timestamp(value);
}

String _timestamp(DateTime value) {
  final local = value.toLocal();
  String two(int number) => number.toString().padLeft(2, '0');
  final offset = local.timeZoneOffset;
  final hours = two(offset.inMinutes.abs() ~/ 60);
  final minutes = two(offset.inMinutes.abs() % 60);
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}:${two(local.second)} '
      'UTC${offset.isNegative ? '-' : '+'}$hours:$minutes';
}

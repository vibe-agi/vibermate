import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';

/// Only safe backend projections enter this widget, never token bytes.
final class ProviderAccountTokenDetails extends StatelessWidget {
  const ProviderAccountTokenDetails({
    required this.account,
    required this.copy,
    super.key,
  });

  final ProviderAccount account;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final info = account.tokenInfo;
    if (info == null) return const SizedBox.shrink();
    final expiry = info.expiresAt;
    final expired = expiry != null && !expiry.isAfter(DateTime.now());
    final identity = <(String, String)>[
      if (info.email case final email?)
        ('provider_accounts.token.email', email),
      if (info.planType case final plan?)
        ('provider_accounts.token.plan', plan),
      if (info.chatgptAccountId case final id?)
        ('provider_accounts.token.account', id),
      if (info.userId case final id?) ('provider_accounts.token.user', id),
    ];
    final dates = <(String, String)>[
      (
        'provider_accounts.token.authenticated',
        _timeOrMissing(info.authenticatedAt),
      ),
      ('provider_accounts.token.issued', _timeOrMissing(info.issuedAt)),
      ('provider_accounts.token.expires', _timeOrMissing(expiry)),
      if (account.codexOAuth case final oauth?)
        ('provider_accounts.token.refreshed', _timestamp(oauth.lastRefresh)),
    ];
    return Column(
      key: Key('provider-account-token-${account.id}'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final paired = constraints.maxWidth >= 900 && identity.isNotEmpty;
            final width = paired
                ? (constraints.maxWidth - 12) / 2
                : constraints.maxWidth;
            return Wrap(
              spacing: 12,
              runSpacing: 12,
              children: [
                for (final (name, fields) in [
                  if (identity.isNotEmpty) ('identity', identity),
                  ('credential_dates', dates),
                ])
                  SizedBox(
                    width: width,
                    child: _TokenMetadataGroup(
                      key: Key('provider-account-$name-${account.id}'),
                      title: copy('provider_accounts.details.$name'),
                      fields: fields,
                      expired: expired,
                      copy: copy,
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
    );
  }

  String _timeOrMissing(DateTime? value) => value == null
      ? copy('provider_accounts.token.not_provided')
      : _timestamp(value);
}

final class _TokenMetadataGroup extends StatelessWidget {
  const _TokenMetadataGroup({
    required this.title,
    required this.fields,
    required this.expired,
    required this.copy,
    super.key,
  });

  final String title;
  final List<(String, String)> fields;
  final bool expired;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Semantics(
    container: true,
    explicitChildNodes: true,
    child: Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: context.viberColors.panel.withValues(alpha: .45),
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Semantics(
            header: true,
            child: Text(title, style: Theme.of(context).textTheme.labelMedium),
          ),
          const SizedBox(height: 10),
          LayoutBuilder(
            builder: (context, constraints) {
              final columns = constraints.maxWidth >= 440 ? 2 : 1;
              final width =
                  (constraints.maxWidth - (columns - 1) * 12) / columns;
              return Wrap(
                spacing: 12,
                runSpacing: 10,
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
                                ?.copyWith(
                                  color: context.viberColors.textMuted,
                                ),
                          ),
                          const SizedBox(height: 3),
                          SelectableText(
                            value,
                            style:
                                expired &&
                                    label == 'provider_accounts.token.expires'
                                ? monoStyle.copyWith(
                                    color: context.viberColors.warning,
                                  )
                                : monoStyle,
                          ),
                        ],
                      ),
                    ),
                ],
              );
            },
          ),
        ],
      ),
    ),
  );
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

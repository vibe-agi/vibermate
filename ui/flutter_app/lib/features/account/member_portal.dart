import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/bootstrap/runtime_connection.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/vibermate_mark.dart';
import '../../core/i18n/app_copy.dart';
import '../workbench/usage_dashboard_view.dart';
import 'web_account_controls.dart';

final class MemberPortal extends StatefulWidget {
  const MemberPortal({
    required this.runtime,
    required this.copy,
    required this.onSignOut,
    super.key,
  });

  final RuntimeConnection runtime;
  final AppCopy copy;
  final Future<void> Function() onSignOut;

  @override
  State<MemberPortal> createState() => _MemberPortalState();
}

final class _MemberPortalState extends State<MemberPortal> {
  RuntimeUsageReport? _report;
  bool _loading = true;
  String? _error;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    if (!_loading) setState(() => _loading = true);
    try {
      final now = DateTime.now().toUtc();
      final until = DateTime.utc(
        now.year,
        now.month,
        now.day,
      ).add(const Duration(days: 1));
      final from = until.subtract(const Duration(days: 365));
      String date(DateTime value) =>
          '${value.year.toString().padLeft(4, '0')}-'
          '${value.month.toString().padLeft(2, '0')}-'
          '${value.day.toString().padLeft(2, '0')}';
      final report = await widget.runtime.api.runtimeUsage(
        RuntimeUsageQuery(
          from: date(from),
          until: date(until),
          timeZone: 'UTC',
        ),
      );
      if (!mounted) return;
      setState(() {
        _report = report;
        _loading = false;
        _error = null;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = widget.copy('usage.unavailable');
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final principal = widget.runtime.webPrincipal!;
    return Scaffold(
      body: Column(
        children: [
          Container(
            height: ViberMetrics.toolbarHeight,
            color: context.viberColors.panel,
            padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.lg),
            child: LayoutBuilder(
              builder: (context, constraints) {
                final compact = constraints.maxWidth < 500;
                return Row(
                  children: [
                    const ViberMateMark(size: 23, framed: true),
                    const SizedBox(width: ViberSpacing.md),
                    Text(
                      widget.copy('app.name'),
                      style: Theme.of(context).textTheme.titleMedium,
                    ),
                    if (!compact) ...[
                      const SizedBox(width: ViberSpacing.md),
                      Text(
                        widget.copy('usage.personal.nav'),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      ),
                    ],
                    const Spacer(),
                    WebAccountButton(
                      principal: principal,
                      copy: widget.copy,
                      compact: compact,
                      onChangePassword: widget.runtime.changePassword,
                      onSignOut: widget.onSignOut,
                    ),
                  ],
                );
              },
            ),
          ),
          const Divider(height: 1),
          Expanded(
            child: PersonalUsageDashboard(
              report: _report,
              loading: _loading,
              error: _error,
              onRefresh: () => unawaited(_load()),
              copy: widget.copy,
            ),
          ),
          const Divider(height: 1),
          Container(
            height: 25,
            color: context.viberColors.panel,
            padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.md),
            child: Row(
              children: [
                Icon(
                  Icons.circle,
                  size: 7,
                  color: context.viberColors.verified,
                ),
                const SizedBox(width: 6),
                Expanded(
                  child: Text(
                    widget.runtime.targetLabel,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.labelSmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'captures_view.dart';
import 'conversations_view.dart';
import 'endpoints_view.dart';
import 'environments_view.dart';
import 'network_view.dart';
import 'offline_hold_view.dart';
import 'settings_view.dart';
import 'workbench_controller.dart';

final class WorkbenchShell extends StatelessWidget {
  const WorkbenchShell({required this.controller, super.key});

  final WorkbenchController controller;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: controller,
      builder: (context, _) {
        final copy = AppCopy.forLanguage(controller.language);
        return Shortcuts(
          shortcuts: const {
            SingleActivator(LogicalKeyboardKey.keyR, meta: true):
                _RefreshIntent(),
            SingleActivator(LogicalKeyboardKey.digit1, meta: true):
                _SectionIntent(WorkbenchSection.captures),
            SingleActivator(LogicalKeyboardKey.digit2, meta: true):
                _SectionIntent(WorkbenchSection.conversations),
            SingleActivator(LogicalKeyboardKey.digit3, meta: true):
                _SectionIntent(WorkbenchSection.environments),
            SingleActivator(LogicalKeyboardKey.digit4, meta: true):
                _SectionIntent(WorkbenchSection.routes),
            SingleActivator(LogicalKeyboardKey.digit5, meta: true):
                _SectionIntent(WorkbenchSection.network),
            SingleActivator(LogicalKeyboardKey.comma, meta: true):
                _SectionIntent(WorkbenchSection.settings),
          },
          child: Actions(
            actions: {
              _RefreshIntent: CallbackAction<_RefreshIntent>(
                onInvoke: (_) => unawaited(controller.refresh()),
              ),
              _SectionIntent: CallbackAction<_SectionIntent>(
                onInvoke: (intent) => controller.selectSection(intent.section),
              ),
            },
            child: Focus(
              autofocus: true,
              child: FocusTraversalGroup(
                policy: ReadingOrderTraversalPolicy(),
                child: Scaffold(
                  body: Column(
                    children: [
                      _TitleBar(controller: controller, copy: copy),
                      const Divider(height: 1),
                      Expanded(
                        child: Row(
                          children: [
                            _NavigationRail(controller: controller, copy: copy),
                            const VerticalDivider(width: 1),
                            Expanded(child: _body(copy)),
                          ],
                        ),
                      ),
                      const Divider(height: 1),
                      _StatusBar(controller: controller, copy: copy),
                    ],
                  ),
                ),
              ),
            ),
          ),
        );
      },
    );
  }

  Widget _body(AppCopy copy) {
    if (controller.loading && controller.data == null) {
      return _LoadingView(copy: copy);
    }
    if (controller.data == null) {
      return _StartupFailure(
        message: controller.errorMessage ?? 'Runtime unavailable',
        retryLabel: copy('common.retry'),
        onRetry: () => unawaited(controller.refresh(selectDefaults: true)),
      );
    }
    return switch (controller.section) {
      WorkbenchSection.captures => CapturesView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.conversations => ConversationsView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.environments => EnvironmentsView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.routes => EndpointsView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.network => NetworkView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.settings => SettingsView(
        controller: controller,
        copy: copy,
      ),
    };
  }
}

final class _RefreshIntent extends Intent {
  const _RefreshIntent();
}

final class _SectionIntent extends Intent {
  const _SectionIntent(this.section);

  final WorkbenchSection section;
}

final class _TitleBar extends StatelessWidget {
  const _TitleBar({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final status = controller.data?.status;
    return Container(
      height: 43,
      color: ViberColors.panel,
      padding: const EdgeInsets.only(left: 13, right: 8),
      child: LayoutBuilder(
        builder: (context, constraints) {
          // Keep the global controls usable at common half-screen desktop
          // widths. The full labels are reserved for a genuinely wide window;
          // the compact controls retain their tooltip and semantic labels.
          final compact = constraints.maxWidth < 1120;
          final narrow = constraints.maxWidth < 520;
          final statusLabel = status?.healthy == true
              ? copy('status.ready')
              : status?.state ?? 'Starting';
          final statusColor = status?.healthy == true
              ? ViberColors.verified
              : ViberColors.warning;
          return Row(
            children: [
              const _ViberMark(),
              const SizedBox(width: 8),
              Text(
                copy('app.name'),
                style: Theme.of(context).textTheme.titleMedium,
              ),
              if (!compact) ...[
                const SizedBox(width: 9),
                Text(
                  copy('app.subtitle'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
              const Spacer(),
              if (controller.previewMode) ...[
                if (compact)
                  Tooltip(
                    message: copy('status.preview'),
                    child: const Padding(
                      padding: EdgeInsets.symmetric(horizontal: 6),
                      child: Icon(
                        Icons.science_outlined,
                        size: 16,
                        color: ViberColors.warning,
                      ),
                    ),
                  )
                else
                  StatusPill(
                    label: copy('status.preview'),
                    color: ViberColors.warning,
                    icon: Icons.science_outlined,
                  ),
                const SizedBox(width: 7),
              ],
              OfflineHoldCommand(
                controller: controller,
                copy: copy,
                compact: compact,
              ),
              const SizedBox(width: 5),
              _ApprovalAttention(
                controller: controller,
                copy: copy,
                compact: compact,
              ),
              const SizedBox(width: 5),
              if (narrow)
                Tooltip(
                  message: statusLabel,
                  child: Semantics(
                    label: statusLabel,
                    container: true,
                    child: Container(
                      width: 29,
                      height: 29,
                      alignment: Alignment.center,
                      decoration: BoxDecoration(
                        color: statusColor.withValues(alpha: 0.09),
                        border: Border.all(
                          color: statusColor.withValues(alpha: 0.4),
                        ),
                        borderRadius: BorderRadius.circular(999),
                      ),
                      child: Icon(Icons.circle, size: 7, color: statusColor),
                    ),
                  ),
                )
              else
                StatusPill(label: statusLabel, color: statusColor),
              const SizedBox(width: 5),
              IconButton(
                onPressed: controller.loading
                    ? null
                    : () => unawaited(controller.refresh()),
                tooltip: '${copy('status.refresh')}  ⌘R',
                icon: controller.loading
                    ? const SizedBox.square(
                        dimension: 14,
                        child: CircularProgressIndicator(strokeWidth: 1.5),
                      )
                    : const Icon(Icons.refresh, size: 16),
                constraints: const BoxConstraints.tightFor(
                  width: 31,
                  height: 31,
                ),
                padding: EdgeInsets.zero,
              ),
            ],
          );
        },
      ),
    );
  }
}

final class _ApprovalAttention extends StatelessWidget {
  const _ApprovalAttention({
    required this.controller,
    required this.copy,
    required this.compact,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final count = controller.pendingApprovalCount;
    final unavailable =
        count == null && controller.approvalAttentionError != null;
    final pending = count != null && count > 0;
    final label = unavailable
        ? copy('approval.attention.unavailable')
        : copy.format('approval.attention.count', {'count': count ?? 0});
    final color = unavailable
        ? ViberColors.danger
        : pending
        ? ViberColors.warning
        : ViberColors.verified;
    final selected = controller.section == WorkbenchSection.network;
    final icon = Badge(
      isLabelVisible: pending,
      label: Text(count == 50 ? '50+' : '${count ?? 0}'),
      backgroundColor: ViberColors.warning,
      textColor: ViberColors.rail,
      textStyle: const TextStyle(fontSize: 8, fontWeight: FontWeight.w700),
      smallSize: 7,
      largeSize: 14,
      child: Icon(
        unavailable ? Icons.error_outline : Icons.approval_outlined,
        size: 15,
        color: color,
      ),
    );
    void action() => controller.selectSection(WorkbenchSection.network);
    return Semantics(
      liveRegion: pending,
      label: label,
      button: true,
      selected: selected,
      child: compact
          ? IconButton(
              key: const Key('approval-attention'),
              onPressed: action,
              tooltip: label,
              style: IconButton.styleFrom(
                backgroundColor: selected
                    ? ViberColors.selection
                    : Colors.transparent,
                side: BorderSide(
                  color: pending
                      ? ViberColors.warning.withValues(alpha: 0.45)
                      : ViberColors.divider,
                ),
              ),
              icon: icon,
              constraints: const BoxConstraints.tightFor(width: 34, height: 29),
              padding: EdgeInsets.zero,
            )
          : OutlinedButton.icon(
              key: const Key('approval-attention'),
              onPressed: action,
              style: OutlinedButton.styleFrom(
                backgroundColor: selected
                    ? ViberColors.selection
                    : Colors.transparent,
                minimumSize: const Size(0, 29),
                padding: const EdgeInsets.symmetric(horizontal: 8),
                side: BorderSide(
                  color: pending
                      ? ViberColors.warning.withValues(alpha: 0.45)
                      : ViberColors.divider,
                ),
              ),
              icon: icon,
              label: Text(
                pending
                    ? copy.format('approval.attention.pending', {
                        'count': count,
                      })
                    : copy('approval.attention.none'),
              ),
            ),
    );
  }
}

final class _ViberMark extends StatelessWidget {
  const _ViberMark();

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: 'ViberMate',
      image: true,
      child: SizedBox.square(
        dimension: 23,
        child: CustomPaint(painter: _ViberMarkPainter()),
      ),
    );
  }
}

final class _ViberMarkPainter extends CustomPainter {
  @override
  void paint(Canvas canvas, Size size) {
    final route = Paint()
      ..color = ViberColors.route
      ..strokeWidth = 2
      ..strokeCap = StrokeCap.round
      ..style = PaintingStyle.stroke;
    final verified = Paint()..color = ViberColors.verified;
    final center = Offset(size.width * 0.48, size.height * 0.5);
    canvas.drawLine(Offset(3, size.height * 0.25), center, route);
    canvas.drawLine(center, Offset(size.width - 3, size.height * 0.25), route);
    canvas.drawLine(center, Offset(size.width - 3, size.height * 0.75), route);
    canvas.drawCircle(Offset(3, size.height * 0.25), 2.2, verified);
    canvas.drawCircle(center, 2.4, verified);
    canvas.drawCircle(
      Offset(size.width - 3, size.height * 0.25),
      2.2,
      verified,
    );
    canvas.drawCircle(
      Offset(size.width - 3, size.height * 0.75),
      2.2,
      verified,
    );
  }

  @override
  bool shouldRepaint(covariant CustomPainter oldDelegate) => false;
}

final class _NavigationRail extends StatelessWidget {
  const _NavigationRail({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 52,
      color: ViberColors.rail,
      child: Column(
        children: [
          const SizedBox(height: 6),
          _RailButton(
            icon: Icons.adjust,
            label: '${copy('nav.captures')}  ⌘1',
            selected: controller.section == WorkbenchSection.captures,
            onPressed: () =>
                controller.selectSection(WorkbenchSection.captures),
          ),
          _RailButton(
            icon: Icons.forum_outlined,
            label: '${copy('nav.conversations')}  ⌘2',
            selected: controller.section == WorkbenchSection.conversations,
            onPressed: () =>
                controller.selectSection(WorkbenchSection.conversations),
          ),
          _RailButton(
            icon: Icons.tune,
            label: '${copy('nav.environments')}  ⌘3',
            selected: controller.section == WorkbenchSection.environments,
            onPressed: () =>
                controller.selectSection(WorkbenchSection.environments),
          ),
          _RailButton(
            icon: Icons.hub_outlined,
            label: '${copy('nav.routes')}  ⌘4',
            selected: controller.section == WorkbenchSection.routes,
            onPressed: () => controller.selectSection(WorkbenchSection.routes),
          ),
          _RailButton(
            icon: Icons.security_outlined,
            label: '${copy('nav.network')}  ⌘5',
            selected: controller.section == WorkbenchSection.network,
            onPressed: () => controller.selectSection(WorkbenchSection.network),
          ),
          const Spacer(),
          _RailButton(
            icon: Icons.settings_outlined,
            label: '${copy('nav.settings')}  ⌘,',
            selected: controller.section == WorkbenchSection.settings,
            onPressed: () =>
                controller.selectSection(WorkbenchSection.settings),
          ),
          const SizedBox(height: 6),
        ],
      ),
    );
  }
}

final class _RailButton extends StatelessWidget {
  const _RailButton({
    required this.icon,
    required this.label,
    required this.selected,
    required this.onPressed,
  });

  final IconData icon;
  final String label;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    return Tooltip(
      message: label,
      preferBelow: false,
      child: Semantics(
        label: label,
        selected: selected,
        button: true,
        child: Material(
          color: selected ? ViberColors.selection : Colors.transparent,
          child: InkWell(
            onTap: onPressed,
            canRequestFocus: true,
            child: Container(
              width: 52,
              height: 43,
              decoration: BoxDecoration(
                border: Border(
                  left: BorderSide(
                    color: selected ? ViberColors.route : Colors.transparent,
                    width: 2,
                  ),
                ),
              ),
              child: Icon(
                icon,
                size: 19,
                color: selected ? ViberColors.text : ViberColors.textMuted,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

final class _StatusBar extends StatelessWidget {
  const _StatusBar({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final data = controller.data;
    return Container(
      height: 23,
      color: ViberColors.rail,
      padding: const EdgeInsets.symmetric(horizontal: 9),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 520;
          final showInstance = constraints.maxWidth >= 720;
          return Row(
            children: [
              const Icon(Icons.circle, size: 6, color: ViberColors.verified),
              const SizedBox(width: 5),
              Expanded(
                child: Text(
                  compact
                      ? controller.previewMode
                            ? 'Preview'
                            : 'Live'
                      : controller.previewMode
                      ? copy('settings.preview')
                      : copy('settings.live'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(
                    context,
                  ).textTheme.bodySmall?.copyWith(fontSize: 10),
                ),
              ),
              if (data != null) ...[
                const SizedBox(width: 8),
                Text(
                  '${controller.runningCaptures.length} running',
                  style: monoStyle.copyWith(fontSize: 9.5),
                ),
                if (showInstance) ...[
                  const SizedBox(width: 12),
                  SizedBox(
                    width: 180,
                    child: Text(
                      data.status.instanceId,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: monoStyle.copyWith(fontSize: 9.5),
                    ),
                  ),
                ],
              ],
            ],
          );
        },
      ),
    );
  }
}

final class _LoadingView extends StatelessWidget {
  const _LoadingView({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const SizedBox.square(
            dimension: 24,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
          const SizedBox(height: 12),
          Text(
            copy('common.loading'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
      ),
    );
  }
}

final class _StartupFailure extends StatelessWidget {
  const _StartupFailure({
    required this.message,
    required this.retryLabel,
    required this.onRetry,
  });

  final String message;
  final String retryLabel;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 440),
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(
                Icons.error_outline,
                size: 28,
                color: ViberColors.danger,
              ),
              const SizedBox(height: 9),
              Text(
                message,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.bodyMedium,
              ),
              const SizedBox(height: 12),
              OutlinedButton(onPressed: onRetry, child: Text(retryLabel)),
            ],
          ),
        ),
      ),
    );
  }
}

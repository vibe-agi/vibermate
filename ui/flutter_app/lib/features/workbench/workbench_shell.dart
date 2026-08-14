import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/design/viber_theme.dart';
import '../../core/design/vibermate_mark.dart';
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
      height: ViberMetrics.toolbarHeight,
      color: context.viberColors.panel,
      padding: const EdgeInsets.only(
        left: ViberSpacing.lg,
        right: ViberSpacing.md,
      ),
      child: LayoutBuilder(
        builder: (context, constraints) {
          // Keep the global controls usable at common half-screen desktop
          // widths. The full labels are reserved for a genuinely wide window;
          // the compact controls retain their tooltip and semantic labels.
          final compact = constraints.maxWidth < 1120;
          final narrow = constraints.maxWidth < 520;
          final statusLabel = status?.healthy == true
              ? copy('status.ready')
              : _runtimeStateLabel(copy, status?.state ?? 'starting');
          final statusColor = status?.healthy == true
              ? context.viberColors.verified
              : context.viberColors.warning;
          return Row(
            children: [
              const _ViberMark(),
              const SizedBox(width: ViberSpacing.md),
              Text(
                copy('app.name'),
                style: Theme.of(context).textTheme.titleMedium,
              ),
              if (!compact) ...[
                const SizedBox(width: ViberSpacing.md),
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
                    child: Padding(
                      padding: const EdgeInsets.symmetric(
                        horizontal: ViberSpacing.sm,
                      ),
                      child: Icon(
                        Icons.science_outlined,
                        size: 16,
                        color: context.viberColors.warning,
                      ),
                    ),
                  )
                else
                  StatusPill(
                    label: copy('status.preview'),
                    color: context.viberColors.warning,
                    icon: Icons.science_outlined,
                  ),
                const SizedBox(width: ViberSpacing.sm),
              ],
              OfflineHoldCommand(
                controller: controller,
                copy: copy,
                compact: true,
              ),
              const SizedBox(width: ViberSpacing.xs),
              _ApprovalAttention(
                controller: controller,
                copy: copy,
                compact: compact,
              ),
              const SizedBox(width: ViberSpacing.xs),
              if (status?.healthy != true) ...[
                if (narrow)
                  Tooltip(
                    message: statusLabel,
                    child: Icon(
                      Icons.error_outline,
                      size: 16,
                      color: statusColor,
                    ),
                  )
                else
                  InlineStatus(label: statusLabel, color: statusColor),
                const SizedBox(width: ViberSpacing.xs),
              ],
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
                  width: ViberMetrics.controlHeight,
                  height: ViberMetrics.controlHeight,
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
    if (!unavailable && (count == null || count == 0)) {
      return const SizedBox.shrink();
    }
    final pending = count != null && count > 0;
    final label = unavailable
        ? copy('approval.attention.unavailable')
        : copy.format('approval.attention.count', {'count': count ?? 0});
    final color = unavailable
        ? context.viberColors.danger
        : pending
        ? context.viberColors.warning
        : context.viberColors.verified;
    final selected = controller.section == WorkbenchSection.network;
    final normalSide = BorderSide(
      color: pending
          ? context.viberColors.warning.withValues(alpha: 0.45)
          : context.viberColors.divider,
    );
    final focusSide = WidgetStateProperty.resolveWith<BorderSide?>((states) {
      return states.contains(WidgetState.focused)
          ? BorderSide(color: context.viberColors.focus, width: 1.5)
          : normalSide;
    });
    final icon = Badge(
      isLabelVisible: pending,
      label: Text(count == 50 ? '50+' : '${count ?? 0}'),
      backgroundColor: context.viberColors.warning,
      textColor: context.viberColors.rail,
      textStyle: Theme.of(context).textTheme.labelMedium?.copyWith(
        fontSize: ViberType.micro,
        fontWeight: FontWeight.w600,
      ),
      smallSize: 7,
      largeSize: 16,
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
                    ? context.viberColors.selection
                    : Colors.transparent,
              ).copyWith(side: focusSide),
              icon: icon,
              constraints: const BoxConstraints.tightFor(
                width: ViberMetrics.controlHeight,
                height: ViberMetrics.controlHeight,
              ),
              padding: EdgeInsets.zero,
            )
          : OutlinedButton.icon(
              key: const Key('approval-attention'),
              onPressed: action,
              style: OutlinedButton.styleFrom(
                backgroundColor: selected
                    ? context.viberColors.selection
                    : Colors.transparent,
                minimumSize: const Size(0, ViberMetrics.controlHeight),
                padding: const EdgeInsets.symmetric(horizontal: 8),
              ).copyWith(side: focusSide),
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
    return const ViberMateMark(size: 23, framed: true);
  }
}

final class _NavigationRail extends StatelessWidget {
  const _NavigationRail({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 52,
      color: context.viberColors.rail,
      child: Column(
        children: [
          const SizedBox(height: ViberSpacing.xs),
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
          const SizedBox(height: ViberSpacing.xs),
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
          color: selected ? context.viberColors.selection : Colors.transparent,
          child: InkWell(
            onTap: onPressed,
            canRequestFocus: true,
            focusColor: context.viberColors.focus.withValues(alpha: 0.18),
            child: Container(
              width: 52,
              height: ViberMetrics.toolbarHeight,
              decoration: BoxDecoration(
                border: Border(
                  left: BorderSide(
                    color: selected
                        ? context.viberColors.route
                        : Colors.transparent,
                    width: 2,
                  ),
                ),
              ),
              child: Icon(
                icon,
                size: 19,
                color: selected
                    ? context.viberColors.text
                    : context.viberColors.textMuted,
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
      height: 24,
      color: context.viberColors.rail,
      padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.md),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 520;
          final showInstance = constraints.maxWidth >= 720;
          return Row(
            children: [
              Icon(Icons.circle, size: 6, color: context.viberColors.verified),
              const SizedBox(width: ViberSpacing.xs),
              Expanded(
                child: Text(
                  compact
                      ? controller.previewMode
                            ? copy('status.preview.short')
                            : copy('status.live.short')
                      : controller.previewMode
                      ? copy('settings.preview')
                      : copy('settings.live'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(
                    context,
                  ).textTheme.bodySmall?.copyWith(fontSize: ViberType.utility),
                ),
              ),
              if (data != null) ...[
                const SizedBox(width: 8),
                Text(
                  copy.format('status.running', {
                    'count': controller.runningCaptures.length,
                  }),
                  style: monoStyle.copyWith(fontSize: ViberType.micro),
                ),
                if (showInstance) ...[
                  const SizedBox(width: 12),
                  Tooltip(
                    message: copy.format('status.runtime_instance', {
                      'id': data.status.instanceId,
                    }),
                    child: Semantics(
                      label: copy.format('status.runtime_instance', {
                        'id': data.status.instanceId,
                      }),
                      child: Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Icon(
                            Icons.memory_outlined,
                            size: 11,
                            color: context.viberColors.textFaint,
                          ),
                          const SizedBox(width: 4),
                          Text(
                            _shortRuntimeId(data.status.instanceId),
                            style: monoStyle.copyWith(
                              fontSize: ViberType.micro,
                            ),
                          ),
                        ],
                      ),
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

String _runtimeStateLabel(AppCopy copy, String value) {
  final key = 'status.state.$value';
  final localized = copy(key);
  if (localized != key) return localized;
  return value.replaceAll('_', ' ');
}

String _shortRuntimeId(String value) =>
    value.length <= 10 ? value : value.substring(0, 8);

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
              Icon(
                Icons.error_outline,
                size: 28,
                color: context.viberColors.danger,
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

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/vibermate_mark.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import '../account/web_account_controls.dart';
import 'captures_view.dart';
import 'code_library_view.dart';
import 'endpoints_view.dart';
import 'environments_view.dart';
import 'network_view.dart';
import 'provider_accounts_view.dart';
import 'settings_view.dart';
import 'usage_dashboard_view.dart';
import 'workbench_controller.dart';

// Product screenshots render the sample Preview dataset without the Preview
// markers; the published pages state that the screens show sample data.
const _productScreenshot = bool.fromEnvironment('VIBERMATE_SCREENSHOT');

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
                _SectionIntent(WorkbenchSection.usage),
            SingleActivator(LogicalKeyboardKey.digit3, meta: true):
                _SectionIntent(WorkbenchSection.environments),
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
                  backgroundColor: context.viberColors.canvas,
                  body: LayoutBuilder(
                    builder: (context, constraints) => Row(
                      children: [
                        _Sidebar(
                          controller: controller,
                          copy: copy,
                          labelled:
                              constraints.maxWidth >= _sidebarLabelBreakpoint,
                        ),
                        Expanded(
                          // The inset sheet belongs to the roomy layout;
                          // narrower windows keep every pixel for content.
                          child: _WorkArea(
                            framed:
                                constraints.maxWidth >= _sidebarLabelBreakpoint,
                            child: _body(copy),
                          ),
                        ),
                      ],
                    ),
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
    return Column(
      children: [
        if (_runtimeAttentionLabel(copy, controller.runtimeStatus)
            case final attention?)
          Container(
            key: const Key('runtime-attention'),
            height: ViberMetrics.compactRowHeight,
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.lg),
            child: Builder(
              builder: (context) => InlineStatus(
                label: attention,
                color: context.viberColors.warning,
              ),
            ),
          ),
        if (controller.runtimeStatus?.storageFailure case final failure?)
          InlineNotice(
            key: const Key('runtime-storage-failure'),
            error: true,
            message: copy.format('status.failure.storage', {
              'operation': copy(
                'status.failure.operation.${failure.operation}',
              ),
              'reason': copy('status.failure.reason.${failure.reason}'),
              'at': failure.at.toUtc().toIso8601String(),
            }),
          ),
        if (controller.runtimeStatus?.recording case final recording?)
          if (recording.lowSpacePause)
            InlineNotice(
              key: const Key('runtime-recording-low-space'),
              error: true,
              message: copy('status.recording.low_space'),
            )
          else if (recording.incomplete)
            InlineNotice(
              key: const Key('runtime-recording-state'),
              message: copy.format('status.recording.evidence', {
                'state': copy('status.recording.${recording.state}'),
                'dropped': '${recording.dropped}',
                'unconfirmed': '${recording.unconfirmed}',
              }),
            ),
        if (controller.runtimeStatus?.recordingMaintenanceFailure
            case final failure?)
          InlineNotice(
            key: const Key('runtime-recording-maintenance-failure'),
            message: copy.format('status.maintenance.failure', {
              'phase': copy('status.maintenance.phase.${failure.phase}'),
              'reason': copy('status.maintenance.reason.${failure.reason}'),
              'at': failure.at.toUtc().toIso8601String(),
            }),
          ),
        if (controller.runtimeStatus?.recordingFailure case final failure?)
          InlineNotice(
            key: const Key('runtime-recording-failure'),
            message: copy.format('status.failure.recording', {
              'operation': copy(
                'status.failure.operation.${failure.operation}',
              ),
              'reason': copy('status.failure.reason.${failure.reason}'),
              'at': failure.at.toUtc().toIso8601String(),
            }),
          ),
        Expanded(child: _content(copy)),
      ],
    );
  }

  Widget _content(AppCopy copy) {
    if (controller.loading && controller.data == null) {
      return _LoadingView(copy: copy);
    }
    if (controller.data == null) {
      return _StartupFailure(
        message: copy('startup.unavailable'),
        technicalDetail: controller.errorMessage,
        technicalDetailsLabel: copy('common.technical_details'),
        retryLabel: copy('common.retry'),
        onRetry: () => unawaited(controller.refresh(selectDefaults: true)),
      );
    }
    final body = switch (controller.section) {
      WorkbenchSection.usage => UsageDashboardView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.captures => CapturesView(
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
      WorkbenchSection.providerAccounts => ProviderAccountsView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.network => NetworkView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.codeLibrary => CodeLibraryView(
        controller: controller,
        copy: copy,
      ),
      WorkbenchSection.settings => SettingsView(
        controller: controller,
        copy: copy,
      ),
    };
    return Column(
      children: [
        if (controller.runtimeBuildMismatch || controller.terminalBuildMismatch)
          InlineNotice(
            key: const Key('product-build-mismatch'),
            message: copy.format('updates.mismatch', {
              'details': [
                copy.format('updates.app_build', {
                  'build': controller.appProductBuild,
                }),
                if (controller.runtimeBuildMismatch)
                  copy.format('updates.runtime_build', {
                    'build': controller.runtimeReleaseBuild!,
                  }),
                if (controller.terminalBuildMismatch)
                  copy.format('updates.terminal_build', {
                    'build': controller.terminalCommand!.installedBuild!,
                  }),
              ].join(' · '),
            }),
            actionLabel: copy('updates.review'),
            onAction: controller.openUpdateSettings,
          ),
        if (controller.terminalManagement &&
            controller.section != WorkbenchSection.settings) ...[
          if (controller.terminalCommandNotice case final notice?)
            InlineNotice(
              message: copy(notice),
              actionLabel: notice == 'terminal.attention'
                  ? copy('settings.title')
                  : null,
              onAction: notice == 'terminal.attention'
                  ? controller.openAccessSettings
                  : null,
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
          if (controller.terminalCommandError case final error?)
            InlineNotice(
              message: '${copy('terminal.attention')} ${copy(error)}',
              actionLabel: copy('settings.title'),
              onAction: controller.openAccessSettings,
              error: true,
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
        ],
        Expanded(child: body),
      ],
    );
  }
}

final class _RefreshIntent extends Intent {
  const _RefreshIntent();
}

final class _SectionIntent extends Intent {
  const _SectionIntent(this.section);

  final WorkbenchSection section;
}

final class _ViberMark extends StatelessWidget {
  const _ViberMark();

  @override
  Widget build(BuildContext context) {
    return const ViberMateMark(size: 22, framed: true);
  }
}

/// Below this window width the labelled sidebar folds to an icon rail.
const double _sidebarLabelBreakpoint = 1200;

const double _sidebarWidth = 216;
const double _railWidth = 52;

final class _WorkArea extends StatelessWidget {
  const _WorkArea({required this.framed, required this.child});

  final bool framed;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    if (!framed) {
      return DecoratedBox(
        decoration: BoxDecoration(
          color: colors.panel,
          border: Border(left: BorderSide(color: colors.dividerSoft)),
        ),
        child: child,
      );
    }
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        0,
        ViberSpacing.md,
        ViberSpacing.md,
        ViberSpacing.md,
      ),
      child: Container(
        clipBehavior: Clip.antiAlias,
        decoration: BoxDecoration(
          color: colors.panel,
          border: Border.all(color: colors.dividerSoft),
          borderRadius: const BorderRadius.all(Radius.circular(12)),
        ),
        child: child,
      ),
    );
  }
}

typedef _NavEntry = ({
  WorkbenchSection section,
  IconData icon,
  String labelKey,
  String key,
  String? shortcut,
});

const _watchEntries = <_NavEntry>[
  (
    section: WorkbenchSection.captures,
    icon: Icons.adjust,
    labelKey: 'nav.captures',
    key: 'captures',
    shortcut: '⌘1',
  ),
  (
    section: WorkbenchSection.network,
    icon: Icons.security_outlined,
    labelKey: 'nav.network',
    key: 'network',
    shortcut: null,
  ),
];

const _usageEntry = (
  section: WorkbenchSection.usage,
  icon: Icons.bar_chart_outlined,
  labelKey: 'nav.usage',
  key: 'usage',
  shortcut: '⌘2',
);

const _controlEntries = <_NavEntry>[
  (
    section: WorkbenchSection.environments,
    icon: Icons.tune,
    labelKey: 'nav.environments',
    key: 'environments',
    shortcut: '⌘3',
  ),
  (
    section: WorkbenchSection.routes,
    icon: Icons.hub_outlined,
    labelKey: 'nav.routes',
    key: 'routes',
    shortcut: null,
  ),
  (
    section: WorkbenchSection.providerAccounts,
    icon: Icons.key_outlined,
    labelKey: 'nav.provider_accounts',
    key: 'provider-accounts',
    shortcut: null,
  ),
  (
    section: WorkbenchSection.codeLibrary,
    icon: Icons.data_object_rounded,
    labelKey: 'nav.code_library',
    key: 'code-library',
    shortcut: null,
  ),
];

/// The one navigation surface: every destination is visible and labelled, in
/// the order a request meets them. It sits on the frame, not on a panel.
final class _Sidebar extends StatelessWidget {
  const _Sidebar({
    required this.controller,
    required this.copy,
    required this.labelled,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool labelled;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final preview = controller.previewMode && !_productScreenshot;
    Widget item(_NavEntry entry, {Widget? trailing}) => _NavItem(
      key: Key('workbench-tab-${entry.key}'),
      icon: entry.icon,
      label: copy(entry.labelKey),
      shortcut: entry.shortcut,
      labelled: labelled,
      selected: controller.section == entry.section,
      trailing: trailing,
      onPressed: () => controller.selectSection(entry.section),
    );
    return SizedBox(
      width: labelled ? _sidebarWidth : _railWidth,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(
            height: 48,
            child: Padding(
              padding: EdgeInsets.symmetric(
                horizontal: labelled ? ViberSpacing.xl : 0,
              ),
              child: Row(
                mainAxisAlignment: labelled
                    ? MainAxisAlignment.start
                    : MainAxisAlignment.center,
                children: [
                  const _ViberMark(),
                  if (labelled) ...[
                    const SizedBox(width: ViberSpacing.md),
                    Flexible(
                      child: Text(
                        copy('app.name'),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ),
          Expanded(
            child: SingleChildScrollView(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  _NavGroupLabel(
                    label: copy('nav.group.watch'),
                    labelled: labelled,
                    first: true,
                  ),
                  item(
                    _watchEntries[0],
                    // The count needs the labelled row; the rail has no room.
                    trailing: controller.data == null || !labelled
                        ? null
                        : Text(
                            copy.format('status.running', {
                              'count': controller.runningCaptures.length,
                            }),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(color: colors.textFaint),
                          ),
                  ),
                  item(
                    _watchEntries[1],
                    trailing: _ApprovalAttention(
                      controller: controller,
                      copy: copy,
                      compact: !labelled,
                    ),
                  ),
                  if (controller.serverManagement)
                    KeyedSubtree(
                      key: const Key('usage-dashboard-nav'),
                      child: item(_usageEntry),
                    ),
                  _NavGroupLabel(
                    label: copy('nav.group.control'),
                    labelled: labelled,
                  ),
                  for (final entry in _controlEntries) item(entry),
                ],
              ),
            ),
          ),
          if (preview)
            Padding(
              padding: const EdgeInsets.only(bottom: ViberSpacing.md),
              child: Center(
                child: labelled
                    ? StatusPill(
                        label: copy('status.preview'),
                        color: colors.warning,
                        icon: Icons.science_outlined,
                      )
                    : Tooltip(
                        message: copy('status.preview'),
                        child: Icon(
                          Icons.science_outlined,
                          size: 16,
                          color: colors.warning,
                        ),
                      ),
              ),
            ),
          if (controller.webPrincipal case final principal?)
            Padding(
              padding: const EdgeInsets.only(bottom: ViberSpacing.xs),
              child: Center(
                child: FittedBox(
                  fit: BoxFit.scaleDown,
                  child: WebAccountButton(
                    principal: principal,
                    copy: copy,
                    compact: !labelled,
                    onChangePassword: controller.changeWebPassword,
                    onSignOut: controller.onSignOut,
                  ),
                ),
              ),
            ),
          _NavItem(
            key: const Key('workbench-settings-nav'),
            icon: Icons.settings_outlined,
            label: copy('nav.settings'),
            shortcut: '⌘,',
            labelled: labelled,
            selected: controller.section == WorkbenchSection.settings,
            onPressed: () =>
                controller.selectSection(WorkbenchSection.settings),
          ),
          _RuntimeStatusRow(
            controller: controller,
            copy: copy,
            labelled: labelled,
          ),
          const SizedBox(height: ViberSpacing.sm),
        ],
      ),
    );
  }
}

final class _NavGroupLabel extends StatelessWidget {
  const _NavGroupLabel({
    required this.label,
    required this.labelled,
    this.first = false,
  });

  final String label;
  final bool labelled;
  final bool first;

  @override
  Widget build(BuildContext context) {
    if (!labelled) {
      return first
          ? const SizedBox(height: ViberSpacing.xs)
          : Padding(
              padding: const EdgeInsets.symmetric(
                horizontal: ViberSpacing.lg,
                vertical: ViberSpacing.md,
              ),
              child: Divider(height: 1, color: context.viberColors.dividerSoft),
            );
    }
    return Padding(
      padding: EdgeInsets.fromLTRB(
        ViberSpacing.xl + ViberSpacing.xxs,
        first ? ViberSpacing.md : ViberSpacing.xl + ViberSpacing.xs,
        ViberSpacing.xl,
        ViberSpacing.sm,
      ),
      child: Text(
        label,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: Theme.of(
          context,
        ).textTheme.bodySmall?.copyWith(color: context.viberColors.textFaint),
      ),
    );
  }
}

final class _NavItem extends StatelessWidget {
  const _NavItem({
    super.key,
    required this.icon,
    required this.label,
    required this.labelled,
    required this.selected,
    required this.onPressed,
    this.shortcut,
    this.trailing,
  });

  final IconData icon;
  final String label;
  final String? shortcut;
  final bool labelled;
  final bool selected;
  final VoidCallback onPressed;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final hint = shortcut == null ? label : '$label  $shortcut';
    final foreground = selected ? colors.text : colors.textMuted;
    final iconWidget = Icon(icon, size: 17, color: foreground);
    return Padding(
      padding: const EdgeInsets.symmetric(
        horizontal: ViberSpacing.md,
        vertical: 1,
      ),
      child: Tooltip(
        message: hint,
        preferBelow: false,
        // A labelled row already says its name; only the shortcut is news.
        triggerMode: labelled && shortcut == null
            ? TooltipTriggerMode.manual
            : null,
        child: Semantics(
          label: hint,
          selected: selected,
          button: true,
          excludeSemantics: true,
          child: Material(
            color: selected ? colors.selection : Colors.transparent,
            borderRadius: ViberMetrics.surfaceRadius,
            clipBehavior: Clip.antiAlias,
            child: InkWell(
              onTap: onPressed,
              canRequestFocus: true,
              focusColor: colors.focus.withValues(alpha: 0.18),
              child: SizedBox(
                height: ViberMetrics.optionRowHeight,
                child: labelled
                    ? Padding(
                        padding: const EdgeInsets.symmetric(
                          horizontal: ViberSpacing.md + ViberSpacing.xxs,
                        ),
                        child: Row(
                          children: [
                            iconWidget,
                            const SizedBox(width: ViberSpacing.md + 2),
                            Expanded(
                              child: Text(
                                label,
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                                style: Theme.of(context).textTheme.bodyMedium
                                    ?.copyWith(
                                      color: selected
                                          ? colors.text
                                          : colors.text.withValues(alpha: 0.86),
                                      fontWeight: selected
                                          ? FontWeight.w600
                                          : FontWeight.w400,
                                    ),
                              ),
                            ),
                            if (trailing case final trailing?) ...[
                              const SizedBox(width: ViberSpacing.sm),
                              trailing,
                            ],
                          ],
                        ),
                      )
                    : Stack(
                        alignment: Alignment.center,
                        children: [
                          iconWidget,
                          if (trailing case final trailing?)
                            Positioned(top: 3, right: 3, child: trailing),
                        ],
                      ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// Pending decisions ride on the Connections row, where they are answered.
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
    final colors = context.viberColors;
    final count = controller.pendingApprovalCount;
    final nativePending = controller.nativeApprovalAttention;
    final partialNative =
        nativePending && !controller.nativeApprovalScanComplete;
    final nativeLabel =
        '${controller.selectedCapture?.displayName ?? copy('native.selectedCapture')} · ${controller.nativeApprovalScanComplete ? copy.format('native.pendingCount', {'count': controller.nativePendingCount!}) : copy('native.pending')}';
    final unavailable =
        count == null && controller.approvalAttentionError != null;
    if (!unavailable && !nativePending && (count == null || count == 0)) {
      return const SizedBox.shrink();
    }
    final label = unavailable
        ? copy('approval.attention.unavailable')
        : nativePending
        ? '${copy.format('approval.attention.count', {'count': controller.pendingApprovals?.length ?? 0})} · $nativeLabel'
        : copy.format('approval.attention.count', {'count': count ?? 0});
    final color = unavailable ? colors.danger : colors.warning;
    final text = unavailable
        ? '!'
        : partialNative
        ? '•'
        : count == 50
        ? '50+'
        : '${count ?? 0}';
    return Tooltip(
      message: label,
      child: Semantics(
        key: const Key('approval-attention'),
        liveRegion: !unavailable,
        label: label,
        child: ExcludeSemantics(
          child: Container(
            constraints: BoxConstraints(
              minWidth: compact ? 14 : 20,
              minHeight: compact ? 14 : 18,
            ),
            padding: EdgeInsets.symmetric(horizontal: compact ? 3 : 6),
            decoration: BoxDecoration(
              color: color.withValues(alpha: 0.14),
              borderRadius: ViberMetrics.pillRadius,
            ),
            child: Center(
              widthFactor: 1,
              heightFactor: 1,
              child: Text(
                text,
                style: Theme.of(context).textTheme.labelMedium?.copyWith(
                  color: color,
                  fontSize: compact ? ViberType.micro : ViberType.utility,
                  fontWeight: FontWeight.w600,
                  height: 1.1,
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// Runtime health and the manual refresh, at the foot of the sidebar.
final class _RuntimeStatusRow extends StatelessWidget {
  const _RuntimeStatusRow({
    required this.controller,
    required this.copy,
    required this.labelled,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool labelled;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final status = controller.runtimeStatus;
    final data = controller.data;
    final recordingDegraded =
        status?.recordingFailure != null ||
        status?.recording?.incomplete == true;
    final healthy = status?.healthy == true && !recordingDegraded;
    final sourceLabel = controller.previewMode && !_productScreenshot
        ? copy('settings.preview')
        : controller.serverManagement
        ? copy.format('settings.remote', {
            'target': controller.runtimeConnectTarget,
          })
        : copy('settings.live');
    final label = sourceLabel;
    final color = healthy ? colors.verified : colors.warning;
    final attention = _runtimeAttentionLabel(copy, status);
    final detail = [
      label,
      ?attention,
      if (data != null)
        copy.format('status.runtime_instance', {'id': data.status.instanceId}),
    ].join('\n');
    final refresh = IconButton(
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
    );
    if (!labelled) {
      return Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          refresh,
          Tooltip(
            message: detail,
            child: Semantics(
              label: label,
              child: Padding(
                padding: const EdgeInsets.all(ViberSpacing.sm),
                child: Icon(Icons.circle, size: 7, color: color),
              ),
            ),
          ),
        ],
      );
    }
    return Padding(
      padding: const EdgeInsets.only(
        left: ViberSpacing.xl + ViberSpacing.xxs,
        right: ViberSpacing.md,
      ),
      child: Row(
        children: [
          Icon(Icons.circle, size: 7, color: color),
          const SizedBox(width: ViberSpacing.md),
          Expanded(
            child: Tooltip(
              message: detail,
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(
                  context,
                ).textTheme.bodySmall?.copyWith(color: colors.textFaint),
              ),
            ),
          ),
          refresh,
        ],
      ),
    );
  }
}

/// The one-line runtime problem, or null while the Runtime is healthy.
String? _runtimeAttentionLabel(AppCopy copy, RuntimeStatus? status) {
  final recordingDegraded =
      status?.recordingFailure != null || status?.recording?.incomplete == true;
  if (status?.healthy == true && !recordingDegraded) return null;
  if (status?.storageFailure != null) return copy('status.proxy_stopped');
  if (recordingDegraded && status?.healthy == true) {
    return copy('status.recording_degraded');
  }
  return _runtimeStateLabel(copy, status?.state ?? 'starting');
}

String _runtimeStateLabel(AppCopy copy, String value) {
  final key = 'status.state.$value';
  final localized = copy.maybe(key);
  if (localized != null) return localized;
  return value.replaceAll('_', ' ');
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
          const CompactProgressIndicator(),
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
    required this.technicalDetail,
    required this.technicalDetailsLabel,
    required this.retryLabel,
    required this.onRetry,
  });

  final String message;
  final String? technicalDetail;
  final String technicalDetailsLabel;
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
              if (technicalDetail case final detail?) ...[
                const SizedBox(height: 6),
                ExpansionTile(
                  key: const Key('startup-technical-details'),
                  tilePadding: EdgeInsets.zero,
                  childrenPadding: const EdgeInsets.only(bottom: 8),
                  title: Text(
                    technicalDetailsLabel,
                    textAlign: TextAlign.center,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  children: [
                    SelectableText(
                      detail,
                      style: monoStyle.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                  ],
                ),
              ],
              const SizedBox(height: 12),
              OutlinedButton(onPressed: onRetry, child: Text(retryLabel)),
            ],
          ),
        ),
      ),
    );
  }
}

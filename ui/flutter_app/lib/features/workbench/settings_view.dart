import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/bootstrap/terminal_command.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'offline_hold_view.dart';
import 'workbench_controller.dart';

final class SettingsView extends StatelessWidget {
  const SettingsView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        PageHeading(
          title: copy('settings.title'),
          subtitle: copy('settings.subtitle'),
        ),
        const Divider(height: 1),
        Expanded(
          child: ListView(
            padding: const EdgeInsets.fromLTRB(14, 12, 14, 20),
            children: [
              if (controller.preferenceWarning case final warning?) ...[
                InlineNotice(
                  key: const Key('preferences-warning'),
                  message: copy(warning),
                  error: true,
                ),
                const SizedBox(height: 10),
              ],
              Wrap(
                spacing: 28,
                runSpacing: 12,
                children: [
                  _PreferenceControl(
                    label: copy('settings.appearance'),
                    child: CompactSegmentedControl<WorkbenchTheme>(
                      key: const Key('settings-theme'),
                      segments: [
                        CompactSegment(
                          value: WorkbenchTheme.system,
                          label: copy('settings.auto'),
                        ),
                        CompactSegment(
                          value: WorkbenchTheme.light,
                          label: copy('settings.light'),
                        ),
                        CompactSegment(
                          value: WorkbenchTheme.dark,
                          label: copy('settings.dark'),
                        ),
                      ],
                      minSegmentWidth: 42,
                      selected: controller.theme,
                      onSelected: controller.setTheme,
                    ),
                  ),
                  _PreferenceControl(
                    label: copy('settings.language'),
                    child: CompactSegmentedControl<AppLanguage>(
                      key: const Key('settings-language'),
                      segments: [
                        CompactSegment(
                          value: AppLanguage.english,
                          label: copy('settings.english'),
                        ),
                        CompactSegment(
                          value: AppLanguage.simplifiedChinese,
                          label: copy('settings.chinese'),
                        ),
                      ],
                      selected: controller.language,
                      onSelected: controller.setLanguage,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 14),
              const Divider(height: 1),
              const SizedBox(height: 14),
              OfflineHoldSettingsPanel(controller: controller, copy: copy),
              const SizedBox(height: 14),
              _TerminalCommandPanel(controller: controller, copy: copy),
              const SizedBox(height: 9),
              _ManagedRunGuide(copy: copy, status: controller.terminalCommand),
              const SizedBox(height: 18),
              _SettingsLabel(copy('settings.runtime')),
              const SizedBox(height: 7),
              Row(
                children: [
                  Icon(
                    Icons.memory,
                    size: 15,
                    color: context.viberColors.route,
                  ),
                  const SizedBox(width: 7),
                  Expanded(
                    child: Text(
                      controller.previewMode
                          ? copy('settings.preview')
                          : copy('settings.live'),
                      style: Theme.of(context).textTheme.bodyMedium,
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ],
    );
  }
}

final class _ManagedRunGuide extends StatefulWidget {
  const _ManagedRunGuide({required this.copy, required this.status});

  final AppCopy copy;
  final TerminalCommandStatus? status;

  @override
  State<_ManagedRunGuide> createState() => _ManagedRunGuideState();
}

final class _ManagedRunGuideState extends State<_ManagedRunGuide> {
  String? _copied;
  bool _copyFailed = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final state = widget.status?.state;
    final enabled =
        state == TerminalCommandState.current ||
        state == TerminalCommandState.sourceUpdated;
    return Container(
      key: const Key('managed-run-guide'),
      padding: const EdgeInsets.fromLTRB(11, 9, 10, 10),
      decoration: BoxDecoration(
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.play_arrow_outlined,
                size: 15,
                color: context.viberColors.verified,
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  copy('terminal.run.title'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              if (_copied case final value?)
                Semantics(
                  liveRegion: true,
                  child: Text(
                    copy.format('terminal.run.copied', {'client': value}),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.verified,
                    ),
                  ),
                )
              else if (_copyFailed)
                Semantics(
                  liveRegion: true,
                  child: Text(
                    copy('terminal.run.copy_failed'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.danger,
                    ),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 4),
          Text(
            copy(
              enabled ? 'terminal.run.detail' : 'terminal.run.install_first',
            ),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 7),
          Wrap(
            spacing: 7,
            runSpacing: 7,
            children: [
              _RunCommand(
                client: 'Claude',
                command: 'vibermate run -- claude',
                copyLabel: copy.format('terminal.run.copy', {
                  'client': 'Claude',
                }),
                enabled: enabled,
                onCopy: () => _copy('Claude', 'vibermate run -- claude'),
              ),
              _RunCommand(
                client: 'Codex',
                command: 'vibermate run -- codex',
                copyLabel: copy.format('terminal.run.copy', {
                  'client': 'Codex',
                }),
                enabled: enabled,
                onCopy: () => _copy('Codex', 'vibermate run -- codex'),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            copy('terminal.run.environment'),
            style: monoStyle.copyWith(
              fontSize: ViberType.micro,
              color: context.viberColors.textFaint,
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _copy(String client, String command) async {
    try {
      await Clipboard.setData(ClipboardData(text: command));
      if (!mounted) return;
      setState(() {
        _copied = client;
        _copyFailed = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _copied = null;
        _copyFailed = true;
      });
    }
  }
}

final class _RunCommand extends StatelessWidget {
  const _RunCommand({
    required this.client,
    required this.command,
    required this.copyLabel,
    required this.enabled,
    required this.onCopy,
  });

  final String client;
  final String command;
  final String copyLabel;
  final bool enabled;
  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 220,
      height: ViberMetrics.controlHeight,
      decoration: BoxDecoration(
        color: context.viberColors.rail,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Expanded(
            child: Padding(
              padding: const EdgeInsets.only(left: 8, right: 5),
              child: Text(
                command,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: monoStyle.copyWith(
                  fontSize: ViberType.utility,
                  color: enabled
                      ? context.viberColors.text
                      : context.viberColors.textFaint,
                ),
              ),
            ),
          ),
          IconButton(
            key: Key('managed-run-copy-${client.toLowerCase()}'),
            onPressed: enabled ? onCopy : null,
            tooltip: copyLabel,
            icon: const Icon(Icons.copy, size: 12),
            constraints: const BoxConstraints.tightFor(
              width: ViberMetrics.controlHeight,
              height: ViberMetrics.controlHeight,
            ),
            padding: EdgeInsets.zero,
          ),
        ],
      ),
    );
  }
}

final class _SettingsLabel extends StatelessWidget {
  const _SettingsLabel(this.label);

  final String label;

  @override
  Widget build(BuildContext context) =>
      Text(label, style: Theme.of(context).textTheme.titleMedium);
}

final class _PreferenceControl extends StatelessWidget {
  const _PreferenceControl({required this.label, required this.child});

  final String label;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 210,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: Theme.of(context).textTheme.titleSmall),
          const SizedBox(height: 5),
          Align(alignment: Alignment.centerLeft, child: child),
        ],
      ),
    );
  }
}

final class _TerminalCommandPanel extends StatelessWidget {
  const _TerminalCommandPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final status = controller.terminalCommand;
    final error = controller.terminalCommandError;
    final notice = controller.terminalCommandNotice;
    return Container(
      key: const Key('terminal-command-panel'),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(11, 10, 10, 9),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Container(
                  width: ViberMetrics.controlHeight,
                  height: ViberMetrics.controlHeight,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: context.viberColors.route.withValues(alpha: 0.09),
                    border: Border.all(
                      color: context.viberColors.route.withValues(alpha: 0.3),
                    ),
                    borderRadius: ViberMetrics.controlRadius,
                  ),
                  child: Icon(
                    Icons.terminal,
                    size: 15,
                    color: context.viberColors.route,
                  ),
                ),
                const SizedBox(width: 9),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy('terminal.title'),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      const SizedBox(height: 2),
                      Text(
                        copy('terminal.description'),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                IconButton(
                  key: const Key('terminal-command-refresh'),
                  tooltip: copy('terminal.refresh_status'),
                  onPressed:
                      controller.terminalCommandLoading ||
                          controller.terminalCommandMutating
                      ? null
                      : () => unawaited(controller.refreshTerminalCommand()),
                  icon: controller.terminalCommandLoading
                      ? const SizedBox.square(
                          dimension: 13,
                          child: CircularProgressIndicator(strokeWidth: 1.4),
                        )
                      : const Icon(Icons.refresh, size: 15),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
              ],
            ),
          ),
          const Divider(height: 1),
          if (status == null && controller.terminalCommandLoading)
            const SizedBox(
              height: 54,
              child: Center(
                child: SizedBox.square(
                  dimension: 16,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                ),
              ),
            )
          else if (status != null)
            _TerminalCommandStatusView(
              controller: controller,
              copy: copy,
              status: status,
            )
          else
            Padding(
              padding: const EdgeInsets.all(10),
              child: Text(
                copy('terminal.unavailable'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          if (error != null)
            InlineNotice(
              message: copy(error),
              error: true,
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
          if (notice != null)
            InlineNotice(
              message: copy(notice),
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
        ],
      ),
    );
  }
}

final class _TerminalCommandStatusView extends StatelessWidget {
  const _TerminalCommandStatusView({
    required this.controller,
    required this.copy,
    required this.status,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(11, 9, 11, 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LayoutBuilder(
            builder: (context, constraints) {
              final narrow = constraints.maxWidth < 560;
              final summary = Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  StatusPill(
                    label: copy('terminal.state.${status.state.wireName}'),
                    color: _stateColor(context, status.state),
                    icon: _stateIcon(status.state),
                  ),
                  const SizedBox(width: 9),
                  Expanded(
                    child: Padding(
                      padding: const EdgeInsets.only(top: 1),
                      child: Text(
                        copy('terminal.state_detail.${status.state.wireName}'),
                        style: Theme.of(context).textTheme.bodyMedium,
                      ),
                    ),
                  ),
                ],
              );
              final actions = _TerminalCommandActions(
                controller: controller,
                copy: copy,
                status: status,
              );
              if (narrow) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [summary, const SizedBox(height: 9), actions],
                );
              }
              return Row(
                crossAxisAlignment: CrossAxisAlignment.center,
                children: [
                  Expanded(child: summary),
                  const SizedBox(width: 16),
                  actions,
                ],
              );
            },
          ),
          const SizedBox(height: 3),
          _TerminalCommandDetails(
            key: ValueKey('terminal-command-details-${status.state.wireName}'),
            copy: copy,
            status: status,
          ),
        ],
      ),
    );
  }
}

final class _TerminalCommandDetails extends StatefulWidget {
  const _TerminalCommandDetails({
    required this.copy,
    required this.status,
    super.key,
  });

  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  State<_TerminalCommandDetails> createState() =>
      _TerminalCommandDetailsState();
}

final class _TerminalCommandDetailsState
    extends State<_TerminalCommandDetails> {
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final status = widget.status;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Semantics(
          button: true,
          expanded: _expanded,
          child: InkWell(
            key: const Key('terminal-command-details-toggle'),
            borderRadius: ViberMetrics.controlRadius,
            onTap: () => setState(() => _expanded = !_expanded),
            child: SizedBox(
              height: ViberMetrics.controlHeight,
              child: Row(
                children: [
                  Icon(
                    _expanded ? Icons.expand_more : Icons.chevron_right,
                    size: 15,
                    color: context.viberColors.textFaint,
                  ),
                  const SizedBox(width: 3),
                  Text(
                    copy('terminal.details'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
        if (_expanded)
          Container(
            key: const Key('terminal-command-technical-details'),
            padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
            decoration: BoxDecoration(
              color: context.viberColors.rail,
              border: Border.all(color: context.viberColors.dividerSoft),
              borderRadius: ViberMetrics.surfaceRadius,
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                _CommandPath(
                  label: copy('terminal.target'),
                  value: status.targetPath,
                ),
                const SizedBox(height: 4),
                _CommandPath(
                  label: copy('terminal.source'),
                  value: status.sourcePath,
                  muted: true,
                ),
                if (status.detail case final detail?) ...[
                  const SizedBox(height: 4),
                  _CommandPath(
                    label: copy('terminal.diagnosis'),
                    value: detail,
                    muted: true,
                  ),
                ],
                const SizedBox(height: 7),
                Text(
                  copy('terminal.boundary'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (status.state == TerminalCommandState.unownedTarget ||
                    status.state == TerminalCommandState.conflict) ...[
                  const SizedBox(height: 5),
                  Text(
                    copy('terminal.manual_resolution'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.warning,
                    ),
                  ),
                ],
              ],
            ),
          ),
      ],
    );
  }
}

final class _CommandPath extends StatelessWidget {
  const _CommandPath({
    required this.label,
    required this.value,
    this.muted = false,
  });

  final String label;
  final String value;
  final bool muted;

  @override
  Widget build(BuildContext context) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          width: 53,
          child: Text(
            label,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
              fontSize: ViberType.utility,
            ),
          ),
        ),
        Expanded(
          child: SelectableText(
            value,
            style: monoStyle.copyWith(
              color: muted
                  ? context.viberColors.textMuted
                  : context.viberColors.text,
              fontSize: ViberType.utility,
            ),
          ),
        ),
      ],
    );
  }
}

final class _TerminalCommandActions extends StatelessWidget {
  const _TerminalCommandActions({
    required this.controller,
    required this.copy,
    required this.status,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  Widget build(BuildContext context) {
    final busy = controller.terminalCommandMutating;
    return Wrap(
      alignment: WrapAlignment.end,
      spacing: 7,
      runSpacing: 7,
      children: [
        if (status.canInstall)
          FilledButton.icon(
            key: const Key('terminal-command-install'),
            onPressed: busy
                ? null
                : () => _confirm(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.install,
                  ),
            icon: const Icon(Icons.add_link, size: 14),
            label: Text(copy('terminal.action.install')),
          ),
        if (status.canRefresh)
          FilledButton.icon(
            key: const Key('terminal-command-refresh-installation'),
            onPressed: busy
                ? null
                : () => _confirm(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.refresh,
                  ),
            icon: const Icon(Icons.sync, size: 14),
            label: Text(copy('terminal.action.refresh')),
          ),
        if (status.canRepair)
          FilledButton.icon(
            key: const Key('terminal-command-repair'),
            onPressed: busy
                ? null
                : () => _confirm(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.repair,
                  ),
            icon: const Icon(Icons.build_outlined, size: 14),
            label: Text(copy('terminal.action.repair')),
          ),
        if (status.canRemove &&
            status.state != TerminalCommandState.targetMissing)
          OutlinedButton.icon(
            key: const Key('terminal-command-remove'),
            onPressed: busy
                ? null
                : () => _confirm(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.remove,
                  ),
            icon: const Icon(Icons.link_off, size: 14),
            label: Text(copy('terminal.action.remove')),
          ),
      ],
    );
  }
}

Future<void> _confirm(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
  TerminalCommandStatus status,
  TerminalCommandOperation operation,
) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      key: const Key('terminal-command-confirmation'),
      title: Text(copy('terminal.confirm.${operation.wireName}.title')),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 430),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy.format('terminal.confirm.${operation.wireName}.detail', {
                'target': status.targetPath,
              }),
            ),
            const SizedBox(height: 10),
            Text(
              copy('terminal.confirm.boundary'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, false),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('terminal-command-confirm-action'),
          onPressed: () => Navigator.pop(context, true),
          child: Text(copy('terminal.action.${operation.wireName}')),
        ),
      ],
    ),
  );
  if (confirmed == true) {
    await controller.changeTerminalCommand(operation);
  }
}

Color _stateColor(BuildContext context, TerminalCommandState state) =>
    switch (state) {
      TerminalCommandState.current => context.viberColors.verified,
      TerminalCommandState.notInstalled => context.viberColors.textFaint,
      TerminalCommandState.sourceUpdated ||
      TerminalCommandState.targetMissing => context.viberColors.warning,
      _ => context.viberColors.danger,
    };

IconData _stateIcon(TerminalCommandState state) => switch (state) {
  TerminalCommandState.current => Icons.check_circle_outline,
  TerminalCommandState.notInstalled => Icons.remove_circle_outline,
  TerminalCommandState.sourceUpdated => Icons.update,
  TerminalCommandState.targetMissing => Icons.link_off,
  _ => Icons.error_outline,
};

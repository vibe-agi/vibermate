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
              OfflineHoldSettingsPanel(controller: controller, copy: copy),
              const SizedBox(height: 14),
              _TerminalCommandPanel(controller: controller, copy: copy),
              const SizedBox(height: 9),
              _ManagedRunGuide(copy: copy, status: controller.terminalCommand),
              const SizedBox(height: 18),
              _SettingsLabel(copy('settings.language')),
              const SizedBox(height: 7),
              Align(
                alignment: Alignment.centerLeft,
                child: SegmentedButton<AppLanguage>(
                  segments: [
                    ButtonSegment(
                      value: AppLanguage.english,
                      label: Text(copy('settings.english')),
                    ),
                    ButtonSegment(
                      value: AppLanguage.simplifiedChinese,
                      label: Text(copy('settings.chinese')),
                    ),
                  ],
                  selected: {controller.language},
                  onSelectionChanged: (selection) =>
                      controller.setLanguage(selection.single),
                ),
              ),
              const SizedBox(height: 18),
              _SettingsLabel(copy('settings.runtime')),
              const SizedBox(height: 7),
              Row(
                children: [
                  const Icon(Icons.memory, size: 15, color: ViberColors.route),
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
        border: Border.all(color: ViberColors.dividerSoft),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(
                Icons.play_arrow_outlined,
                size: 15,
                color: ViberColors.verified,
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
                      color: ViberColors.verified,
                    ),
                  ),
                )
              else if (_copyFailed)
                Semantics(
                  liveRegion: true,
                  child: Text(
                    copy('terminal.run.copy_failed'),
                    style: Theme.of(
                      context,
                    ).textTheme.bodySmall?.copyWith(color: ViberColors.danger),
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
              fontSize: 9.5,
              color: ViberColors.textFaint,
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
      height: 29,
      decoration: BoxDecoration(
        color: ViberColors.rail,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Padding(
            padding: const EdgeInsets.only(left: 8, right: 5),
            child: Text(
              command,
              style: monoStyle.copyWith(
                fontSize: 10,
                color: enabled ? ViberColors.text : ViberColors.textFaint,
              ),
            ),
          ),
          IconButton(
            key: Key('managed-run-copy-${client.toLowerCase()}'),
            onPressed: enabled ? onCopy : null,
            tooltip: copyLabel,
            icon: const Icon(Icons.copy, size: 12),
            constraints: const BoxConstraints.tightFor(width: 28, height: 28),
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
        color: ViberColors.panel,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(7),
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
                  width: 27,
                  height: 27,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: ViberColors.route.withValues(alpha: 0.09),
                    border: Border.all(
                      color: ViberColors.route.withValues(alpha: 0.3),
                    ),
                    borderRadius: BorderRadius.circular(5),
                  ),
                  child: const Icon(
                    Icons.terminal,
                    size: 15,
                    color: ViberColors.route,
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
                    width: 29,
                    height: 29,
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
            ),
          if (notice != null)
            InlineNotice(
              message: copy(notice),
              onDismiss: controller.clearTerminalCommandMessage,
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
      child: LayoutBuilder(
        builder: (context, constraints) {
          final narrow = constraints.maxWidth < 560;
          final summary = Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              StatusPill(
                label: copy('terminal.state.${status.state.wireName}'),
                color: _stateColor(status.state),
                icon: _stateIcon(status.state),
              ),
              const SizedBox(height: 7),
              Text(
                copy('terminal.state_detail.${status.state.wireName}'),
                style: Theme.of(context).textTheme.bodyMedium,
              ),
              const SizedBox(height: 7),
              _CommandPath(
                label: copy('terminal.target'),
                value: status.targetPath,
              ),
              const SizedBox(height: 3),
              _CommandPath(
                label: copy('terminal.source'),
                value: status.sourcePath,
                muted: true,
              ),
              const SizedBox(height: 7),
              Text(
                copy('terminal.boundary'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              if (status.state == TerminalCommandState.unownedTarget ||
                  status.state == TerminalCommandState.conflict) ...[
                const SizedBox(height: 7),
                Text(
                  copy('terminal.manual_resolution'),
                  style: Theme.of(
                    context,
                  ).textTheme.bodySmall?.copyWith(color: ViberColors.warning),
                ),
                if (status.detail case final detail?) ...[
                  const SizedBox(height: 3),
                  Text(detail, style: monoStyle.copyWith(fontSize: 10)),
                ],
              ],
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
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Expanded(child: summary),
              const SizedBox(width: 16),
              actions,
            ],
          );
        },
      ),
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
              color: ViberColors.textFaint,
              fontSize: 10,
            ),
          ),
        ),
        Expanded(
          child: SelectableText(
            value,
            style: monoStyle.copyWith(
              color: muted ? ViberColors.textMuted : ViberColors.text,
              fontSize: 10,
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
        if (status.canRemove)
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
            label: Text(
              copy(
                status.state == TerminalCommandState.targetMissing
                    ? 'terminal.action.clean_record'
                    : 'terminal.action.remove',
              ),
            ),
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

Color _stateColor(TerminalCommandState state) => switch (state) {
  TerminalCommandState.current => ViberColors.verified,
  TerminalCommandState.notInstalled => ViberColors.textFaint,
  TerminalCommandState.sourceUpdated ||
  TerminalCommandState.targetMissing => ViberColors.warning,
  _ => ViberColors.danger,
};

IconData _stateIcon(TerminalCommandState state) => switch (state) {
  TerminalCommandState.current => Icons.check_circle_outline,
  TerminalCommandState.notInstalled => Icons.remove_circle_outline,
  TerminalCommandState.sourceUpdated => Icons.update,
  TerminalCommandState.targetMissing => Icons.link_off,
  _ => Icons.error_outline,
};

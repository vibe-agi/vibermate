import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/bootstrap/terminal_command.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'settings_view.dart'
    show confirmTerminalCommandChange, rootCAErrorMessage;
import 'workbench_controller.dart';

/// Where one step of the setup guide stands.
enum SetupStepState {
  /// Finished; the step collapses to one line.
  done,

  /// The step to do now; only it has a filled button.
  current,

  /// Comes after the current step.
  upcoming,

  /// Something ViberMate cannot check, shown as advice and never ticked.
  hint,
}

/// What the setup guide knows about a first run, read from the controller:
/// the Terminal command, the root certificate, and every loaded session.
final class SetupGuideProgress {
  const SetupGuideProgress({
    required this.local,
    required this.command,
    required this.rootTrusted,
    required this.startedRun,
    required this.failedLaunch,
    required this.observed,
  });

  factory SetupGuideProgress.of(WorkbenchController controller) {
    final captures = controller.data?.captures ?? const <CaptureRecord>[];
    CaptureRecord? newest(Iterable<CaptureRecord> values) => values.fold(
      null,
      (latest, capture) =>
          latest == null || capture.createdAt.isAfter(latest.createdAt)
          ? capture
          : latest,
    );
    final managed = captures.where((capture) => capture.kind == 'managed_run');
    // An agent counts as started only while it runs with a process, or once
    // it sent a request. A launch that never started must not tick step 2.
    final started = newest(
      managed.where(
        (capture) =>
            capture.observation == 'observed' ||
            (capture.running && capture.managedRun?.processId != null),
      ),
    );
    final latest = newest(managed);
    final root = controller.rootCAStatus;
    return SetupGuideProgress(
      local: controller.terminalManagement,
      command: controller.terminalCommand,
      rootTrusted: !controller.rootTrustManagement
          ? null
          : root == null
          ? null
          : root.rootValid && root.installed,
      startedRun: started,
      failedLaunch: latest != null && latest.stoppedBeforeStart ? latest : null,
      observed: captures.any((capture) => capture.observation == 'observed'),
    );
  }

  /// The desktop App, which sets up the Terminal command itself. Otherwise
  /// the agent runs on another computer and signs in to this Server.
  final bool local;
  final TerminalCommandStatus? command;

  /// Null when this Mac's trust is not managed here or not read yet.
  final bool? rootTrusted;
  final CaptureRecord? startedRun;

  /// The newest launch, when it stopped before its agent started.
  final CaptureRecord? failedLaunch;
  final bool observed;

  bool get commandReady =>
      !local ||
      command?.state == TerminalCommandState.current ||
      command?.state == TerminalCommandState.sourceUpdated;

  SetupStepState get commandStep => !local
      ? SetupStepState.hint
      : commandReady
      ? SetupStepState.done
      : SetupStepState.current;

  SetupStepState get startStep => startedRun != null
      ? SetupStepState.done
      : commandReady
      ? SetupStepState.current
      : SetupStepState.upcoming;

  SetupStepState get promptStep => observed
      ? SetupStepState.done
      : startedRun != null
      ? SetupStepState.current
      : SetupStepState.upcoming;

  /// The Codex requirement line: shown only while this Mac does not trust
  /// ViberMate's certificate (the launcher's own check).
  bool get codexNeedsRoot => rootTrusted == false;
}

/// The launch command for [client] ('claude' or 'codex'), as this App or a
/// Server's own computer would run it.
String setupLaunchCommand(WorkbenchController controller, String client) =>
    controller.terminalManagement || controller.runtimeServerURL.isEmpty
    ? 'vibermate run -- $client'
    : 'vibermate run --server ${controller.runtimeServerURL} -- $client';

/// The agent a run launched: 'codex' or 'claude'.
String setupClientOf(CaptureRecord capture) =>
    (capture.managedRun?.executableLabel.toLowerCase() ?? '').contains('codex')
    ? 'codex'
    : 'claude';

/// The first-run guide on the empty Sessions screen: the steps to connect
/// Claude Code or Codex, and whether each is done.
final class SetupGuide extends StatefulWidget {
  const SetupGuide({
    required this.controller,
    required this.copy,
    required this.onConnectOtherApp,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  /// Opens 'Connect another app'; null where it cannot be created.
  final VoidCallback? onConnectOtherApp;

  @override
  State<SetupGuide> createState() => _SetupGuideState();
}

final class _SetupGuideState extends State<SetupGuide> {
  String? _copied;
  bool _copyFailed = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(widget.controller.refreshSetupFacts());
    });
  }

  Future<void> _copy(String key, String text) async {
    try {
      await Clipboard.setData(ClipboardData(text: text));
      if (!mounted) return;
      setState(() {
        _copied = key;
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

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final progress = SetupGuideProgress.of(controller);
    return LayoutBuilder(
      builder: (context, constraints) {
        // Each step's button goes under its text when the guide is narrow,
        // which also happens beside an open directory on a small window.
        final narrow = constraints.maxWidth < 520;
        final steps = <Widget>[
          if (progress.local)
            _commandStep(context, progress, narrow)
          else
            _serverInstallStep(context, narrow),
          _startStep(context, progress, narrow),
          _promptStep(context, progress, narrow),
        ];
        return Semantics(
          container: true,
          label: copy('setup.title'),
          child: Column(
            key: const Key('setup-guide'),
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                copy('setup.title'),
                style: Theme.of(context).textTheme.headlineSmall,
              ),
              const SizedBox(height: ViberSpacing.sm),
              Text(
                copy('setup.intro'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: ViberSpacing.lg),
              for (final (index, step) in steps.indexed) ...[
                if (index > 0)
                  Divider(height: 1, color: context.viberColors.dividerSoft),
                step,
              ],
              Divider(height: 1, color: context.viberColors.dividerSoft),
              const SizedBox(height: ViberSpacing.md),
              _footer(context),
            ],
          ),
        );
      },
    );
  }

  Widget _commandStep(
    BuildContext context,
    SetupGuideProgress progress,
    bool narrow,
  ) {
    final controller = widget.controller;
    final copy = widget.copy;
    final status = progress.command;
    final state = progress.commandStep;
    if (state == SetupStepState.done) {
      return _SetupStep(
        key: const Key('setup-step-command'),
        number: 1,
        state: state,
        title: copy('setup.command.title'),
        copy: copy,
        summary: copy('terminal.state_detail.current'),
        narrow: narrow,
      );
    }
    final busy =
        controller.terminalCommandMutating || controller.terminalCommandLoading;
    final operation = switch (status?.state) {
      TerminalCommandState.notInstalled => TerminalCommandOperation.install,
      TerminalCommandState.targetMissing => TerminalCommandOperation.repair,
      _ => null,
    };
    final Widget? action = status == null
        ? null
        : operation != null
        ? FilledButton.icon(
            key: const Key('setup-command-action'),
            onPressed: busy
                ? null
                : () => unawaited(
                    confirmTerminalCommandChange(
                      context,
                      controller,
                      copy,
                      status,
                      operation,
                    ),
                  ),
            icon: Icon(
              operation == TerminalCommandOperation.install
                  ? Icons.add_link
                  : Icons.build_outlined,
              size: 14,
            ),
            label: Text(copy('terminal.action.${operation.wireName}')),
          )
        : FilledButton.icon(
            key: const Key('setup-command-settings'),
            onPressed: controller.openAccessSettings,
            icon: const Icon(Icons.terminal, size: 14),
            label: Text(copy('setup.command.open_settings')),
          );
    final attention =
        status != null && status.state != TerminalCommandState.notInstalled;
    return _SetupStep(
      key: const Key('setup-step-command'),
      number: 1,
      state: state,
      title: copy('setup.command.title'),
      copy: copy,
      status: status == null
          ? null
          : StatusPill(
              label: attention
                  ? copy('terminal.state.${status.state.wireName}')
                  : copy('setup.status.todo'),
              color: attention
                  ? context.viberColors.warning
                  : context.viberColors.textMuted,
            ),
      detail: attention
          ? copy('terminal.state_detail.${status.state.wireName}')
          : copy('setup.command.detail'),
      action: action,
      narrow: narrow,
      children: [
        if (status == null && controller.terminalCommandLoading)
          const Align(
            alignment: Alignment.centerLeft,
            child: CompactProgressIndicator(),
          ),
        if (controller.terminalCommandError case final error?)
          InlineNotice(message: copy(error), error: true),
      ],
    );
  }

  Widget _serverInstallStep(BuildContext context, bool narrow) {
    final controller = widget.controller;
    final copy = widget.copy;
    final login = 'vibermate login --server ${controller.runtimeServerURL}';
    return _SetupStep(
      key: const Key('setup-step-command'),
      number: 1,
      state: SetupStepState.hint,
      title: copy('setup.server.install.title'),
      copy: copy,
      detail: copy('setup.server.install.detail'),
      action: OutlinedButton.icon(
        key: const Key('setup-open-access'),
        onPressed: controller.openAccessSettings,
        icon: const Icon(Icons.menu_book_outlined, size: 14),
        label: Text(copy('capture.empty.web.action')),
      ),
      narrow: narrow,
      children: [
        _SetupCommandRow(
          key: const Key('setup-command-login'),
          label: copy('server.login.command.client'),
          command: login,
          copy: copy,
          enabled: controller.runtimeServerURL.isNotEmpty,
          copied: _copied == 'login',
          copyTooltip: copy('server.login.command.copy'),
          onCopy: () => _copy('login', login),
          narrow: narrow,
        ),
      ],
    );
  }

  Widget _startStep(
    BuildContext context,
    SetupGuideProgress progress,
    bool narrow,
  ) {
    final controller = widget.controller;
    final copy = widget.copy;
    final state = progress.startStep;
    if (state == SetupStepState.done) {
      return _SetupStep(
        key: const Key('setup-step-start'),
        number: 2,
        state: state,
        title: copy('setup.start.title'),
        copy: copy,
        summary: copy.format('setup.start.done', {
          'agent': progress.startedRun!.displayName,
        }),
        narrow: narrow,
      );
    }
    final failed = state == SetupStepState.current
        ? progress.failedLaunch
        : null;
    final failedCodex = failed != null && setupClientOf(failed) == 'codex';
    final installFromFailure = failedCodex && progress.codexNeedsRoot;
    final enabled = progress.commandReady;
    Widget commandRow(String client, String label) {
      final command = setupLaunchCommand(controller, client);
      return _SetupCommandRow(
        key: Key('setup-command-$client'),
        label: label,
        command: command,
        copy: copy,
        enabled: enabled,
        copied: _copied == client,
        copyTooltip: copy.format('terminal.run.copy', {'client': label}),
        onCopy: () => _copy(client, command),
        narrow: narrow,
      );
    }

    return _SetupStep(
      key: const Key('setup-step-start'),
      number: 2,
      state: state,
      title: copy('setup.start.title'),
      copy: copy,
      status: failed != null
          ? StatusPill(
              label: copy('setup.status.failed'),
              color: context.viberColors.danger,
            )
          : null,
      detail: copy(
        progress.local ? 'setup.start.detail' : 'setup.server.start.detail',
      ),
      narrow: narrow,
      children: [
        commandRow('claude', 'Claude Code'),
        const SizedBox(height: ViberSpacing.sm),
        commandRow('codex', 'Codex'),
        if (progress.codexNeedsRoot && !installFromFailure) ...[
          const SizedBox(height: ViberSpacing.sm),
          _CodexRequirement(controller: controller, copy: copy, narrow: narrow),
        ],
        if (controller.rootCAError case final error?) ...[
          const SizedBox(height: ViberSpacing.sm),
          InlineNotice(message: rootCAErrorMessage(copy, error), error: true),
        ],
        if (failed != null) ...[
          const SizedBox(height: ViberSpacing.md),
          LaunchStoppedNotice(
            key: const Key('setup-launch-failed'),
            capture: failed,
            controller: controller,
            copy: copy,
            rootTrusted: progress.rootTrusted,
            narrow: narrow,
          ),
        ] else if (state == SetupStepState.current) ...[
          const SizedBox(height: ViberSpacing.md),
          Text(
            copy('setup.start.waiting'),
            key: const Key('setup-waiting'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
        if (_copyFailed)
          Text(
            copy('terminal.run.copy_failed'),
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(color: context.viberColors.danger),
          ),
      ],
    );
  }

  Widget _promptStep(
    BuildContext context,
    SetupGuideProgress progress,
    bool narrow,
  ) {
    final copy = widget.copy;
    final state = progress.promptStep;
    if (state == SetupStepState.done) {
      return _SetupStep(
        key: const Key('setup-step-prompt'),
        number: 3,
        state: state,
        title: copy('setup.prompt.title'),
        copy: copy,
        summary: copy('setup.prompt.done'),
        narrow: narrow,
      );
    }
    return _SetupStep(
      key: const Key('setup-step-prompt'),
      number: 3,
      state: state,
      title: copy('setup.prompt.title'),
      copy: copy,
      status: state == SetupStepState.current
          ? StatusPill(
              label: copy('setup.status.waiting'),
              color: context.viberColors.route,
            )
          : null,
      detail: copy('setup.prompt.detail'),
      narrow: narrow,
      children: [
        if (state == SetupStepState.current)
          Semantics(
            liveRegion: true,
            child: Row(
              key: const Key('setup-prompt-waiting'),
              children: [
                const CompactProgressIndicator(),
                const SizedBox(width: ViberSpacing.md),
                Expanded(
                  child: Text(
                    copy('setup.start.waiting'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ),
              ],
            ),
          ),
      ],
    );
  }

  Widget _footer(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final linkStyle = TextButton.styleFrom(
      padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.sm),
      alignment: Alignment.centerLeft,
    );
    return Wrap(
      spacing: ViberSpacing.lg,
      runSpacing: ViberSpacing.xs,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        if (widget.onConnectOtherApp case final open?)
          TextButton.icon(
            key: const Key('setup-connect-other-app'),
            style: linkStyle,
            onPressed: open,
            icon: const Icon(Icons.add_link, size: 14),
            label: Text(copy('setup.footer.other_app')),
          ),
        Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Flexible(
              child: Padding(
                padding: const EdgeInsets.only(left: ViberSpacing.sm),
                child: Text(
                  copy('setup.footer.doctor'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ),
            ),
            IconButton(
              key: const Key('setup-copy-doctor'),
              onPressed: () => _copy('doctor', 'vibermate doctor'),
              tooltip: copy('setup.footer.doctor.copy'),
              icon: Icon(
                _copied == 'doctor' ? Icons.check : Icons.copy,
                size: 13,
              ),
              constraints: const BoxConstraints.tightFor(
                width: ViberMetrics.controlHeight,
                height: ViberMetrics.controlHeight,
              ),
              padding: EdgeInsets.zero,
            ),
          ],
        ),
        TextButton(
          key: const Key('setup-hide'),
          style: linkStyle,
          onPressed: controller.hideSetupGuide,
          child: Text(copy('setup.hide')),
        ),
      ],
    );
  }
}

/// One numbered step: a marker, the title with its status, the action, one
/// muted line, and what the step needs below. A finished step is one line.
final class _SetupStep extends StatelessWidget {
  const _SetupStep({
    required this.number,
    required this.state,
    required this.title,
    required this.copy,
    this.status,
    this.detail,
    this.summary,
    this.action,
    this.narrow = false,
    this.children = const [],
    super.key,
  });

  final int number;
  final SetupStepState state;
  final String title;
  final AppCopy copy;
  final Widget? status;
  final String? detail;

  /// The one line a finished step collapses to.
  final String? summary;
  final Widget? action;
  final bool narrow;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final upcoming = state == SetupStepState.upcoming;
    final titleText = Text(
      title,
      style: Theme.of(context).textTheme.titleSmall?.copyWith(
        color: upcoming ? context.viberColors.textMuted : null,
      ),
    );
    final label = copy.format('setup.step', {'number': number});
    if (state == SetupStepState.done) {
      return Semantics(
        container: true,
        label: '$label, $title, ${copy('setup.status.done')}',
        child: Padding(
          padding: const EdgeInsets.symmetric(vertical: ViberSpacing.lg),
          child: Row(
            children: [
              _StepMarker(number: number, state: state),
              const SizedBox(width: ViberSpacing.lg),
              if (narrow) Expanded(child: titleText) else titleText,
              if (summary case final value? when !narrow) ...[
                const SizedBox(width: ViberSpacing.md),
                Expanded(
                  child: Text(
                    value,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ),
              ],
              const SizedBox(width: ViberSpacing.md),
              StatusPill(
                label: copy('setup.status.done'),
                color: context.viberColors.verified,
                icon: Icons.check,
              ),
            ],
          ),
        ),
      );
    }
    final heading = Row(
      children: [
        Flexible(child: titleText),
        if (status case final pill?) ...[
          const SizedBox(width: ViberSpacing.md),
          pill,
        ],
      ],
    );
    return Semantics(
      container: true,
      label: '$label, $title',
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: ViberSpacing.lg),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _StepMarker(number: number, state: state),
            const SizedBox(width: ViberSpacing.lg),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Expanded(child: heading),
                      if (action case final value? when !narrow) ...[
                        const SizedBox(width: ViberSpacing.md),
                        value,
                      ],
                    ],
                  ),
                  if (detail case final value?) ...[
                    const SizedBox(height: ViberSpacing.xs),
                    Text(value, style: Theme.of(context).textTheme.bodySmall),
                  ],
                  if (children.isNotEmpty) ...[
                    const SizedBox(height: ViberSpacing.md),
                    ...children,
                  ],
                  if (action case final value? when narrow) ...[
                    const SizedBox(height: ViberSpacing.md),
                    SizedBox(width: double.infinity, child: value),
                  ],
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

final class _StepMarker extends StatelessWidget {
  const _StepMarker({required this.number, required this.state});

  final int number;
  final SetupStepState state;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final (border, foreground, fill) = switch (state) {
      SetupStepState.done => (
        colors.verified,
        colors.verified,
        colors.verified.withValues(alpha: 0.12),
      ),
      SetupStepState.current => (colors.route, colors.route, colors.canvas),
      SetupStepState.upcoming ||
      SetupStepState.hint => (colors.divider, colors.textMuted, colors.canvas),
    };
    return ExcludeSemantics(
      child: Container(
        width: 20,
        height: 20,
        alignment: Alignment.center,
        decoration: BoxDecoration(
          color: fill,
          shape: BoxShape.circle,
          border: Border.all(
            color: border,
            width: state == SetupStepState.current ? 1.5 : 1,
          ),
        ),
        child: state == SetupStepState.done
            ? Icon(Icons.check, size: 12, color: foreground)
            : Text(
                '$number',
                style: TextStyle(
                  color: foreground,
                  fontSize: ViberType.utility,
                  fontWeight: FontWeight.w600,
                  height: 1,
                ),
              ),
      ),
    );
  }
}

/// A launch command in Terminal's font with a labelled Copy button.
final class _SetupCommandRow extends StatelessWidget {
  const _SetupCommandRow({
    required this.label,
    required this.command,
    required this.copy,
    required this.enabled,
    required this.copied,
    required this.copyTooltip,
    required this.onCopy,
    required this.narrow,
    super.key,
  });

  final String label;
  final String command;
  final AppCopy copy;
  final bool enabled;
  final bool copied;
  final String copyTooltip;
  final VoidCallback onCopy;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final box = Container(
      constraints: const BoxConstraints(minHeight: ViberMetrics.controlHeight),
      decoration: BoxDecoration(
        color: colors.rail,
        border: Border.all(color: colors.divider),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Row(
        children: [
          Expanded(
            child: Padding(
              padding: const EdgeInsets.symmetric(
                horizontal: ViberSpacing.md,
                vertical: ViberSpacing.sm,
              ),
              child: SelectableText(
                command,
                style: monoStyle.copyWith(
                  fontSize: ViberType.body,
                  color: enabled ? colors.text : colors.textFaint,
                ),
              ),
            ),
          ),
          Tooltip(
            message: copyTooltip,
            child: TextButton.icon(
              onPressed: enabled ? onCopy : null,
              icon: Icon(copied ? Icons.check : Icons.copy, size: 13),
              label: Text(
                copy(copied ? 'setup.start.copied' : 'setup.start.copy'),
              ),
            ),
          ),
        ],
      ),
    );
    final caption = Text(label, style: Theme.of(context).textTheme.labelLarge);
    if (narrow) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          caption,
          const SizedBox(height: ViberSpacing.xs),
          box,
        ],
      );
    }
    return Row(
      children: [
        SizedBox(width: 92, child: caption),
        Expanded(child: box),
      ],
    );
  }
}

/// Under the Codex command: this Mac must trust ViberMate's certificate.
final class _CodexRequirement extends StatelessWidget {
  const _CodexRequirement({
    required this.controller,
    required this.copy,
    required this.narrow,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final busy = controller.rootCAMutating || controller.rootCALoading;
    final button = OutlinedButton.icon(
      key: const Key('setup-install-trust'),
      onPressed: busy
          ? null
          : () => unawaited(controller.installAndTrustRootCA()),
      icon: busy
          ? const SizedBox.square(
              dimension: 12,
              child: CircularProgressIndicator(strokeWidth: 1.5),
            )
          : const Icon(Icons.verified_user_outlined, size: 14),
      label: Text(copy('setup.start.install_trust')),
    );
    final text = Text(
      copy('setup.start.codex_requirement'),
      style: Theme.of(context).textTheme.bodySmall,
    );
    return Container(
      key: const Key('setup-codex-requirement'),
      padding: EdgeInsets.only(left: narrow ? 0 : 92),
      child: narrow
          ? Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                text,
                const SizedBox(height: ViberSpacing.sm),
                button,
              ],
            )
          : Row(
              children: [
                Icon(
                  Icons.info_outline,
                  size: 14,
                  color: context.viberColors.textMuted,
                ),
                const SizedBox(width: ViberSpacing.sm),
                Expanded(child: text),
                const SizedBox(width: ViberSpacing.md),
                button,
              ],
            ),
    );
  }
}

/// A launch that stopped before its agent started: what to fix and the one
/// action that helps. The guide shows it in step 2, the session in its body.
final class LaunchStoppedNotice extends StatelessWidget {
  const LaunchStoppedNotice({
    required this.capture,
    required this.controller,
    required this.copy,
    required this.rootTrusted,
    this.narrow = false,
    super.key,
  });

  final CaptureRecord capture;
  final WorkbenchController controller;
  final AppCopy copy;
  final bool? rootTrusted;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final client = setupClientOf(capture);
    final needsRoot = client == 'codex' && rootTrusted == false;
    final busy = controller.rootCAMutating || controller.rootCALoading;
    final Widget action = needsRoot
        ? FilledButton.icon(
            key: const Key('launch-stopped-install-trust'),
            onPressed: busy
                ? null
                : () => unawaited(controller.installAndTrustRootCA()),
            icon: const Icon(Icons.verified_user_outlined, size: 14),
            label: Text(copy('setup.start.install_trust')),
          )
        : CopyCommandAgainButton(
            key: const Key('launch-stopped-copy-again'),
            command: setupLaunchCommand(controller, client),
            copy: copy,
          );
    final colors = context.viberColors;
    return Container(
      padding: const EdgeInsets.all(ViberSpacing.lg),
      decoration: BoxDecoration(
        color: colors.danger.withValues(alpha: 0.06),
        border: Border.all(color: colors.danger.withValues(alpha: 0.28)),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(Icons.error_outline, size: 16, color: colors.danger),
              const SizedBox(width: ViberSpacing.md),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      copy.format('capture.launch_incomplete', {
                        'agent': capture.displayName,
                      }),
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                    const SizedBox(height: ViberSpacing.xs),
                    Text(
                      copy(
                        needsRoot
                            ? 'capture.launch_incomplete.detail.codex_root'
                            : 'capture.launch_incomplete.detail',
                      ),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
            ],
          ),
          const SizedBox(height: ViberSpacing.md),
          Align(
            alignment: Alignment.centerLeft,
            child: narrow
                ? SizedBox(width: double.infinity, child: action)
                : action,
          ),
        ],
      ),
    );
  }
}

/// 'Copy command again', for a launch that stopped before its agent started.
final class CopyCommandAgainButton extends StatefulWidget {
  const CopyCommandAgainButton({
    required this.command,
    required this.copy,
    super.key,
  });

  final String command;
  final AppCopy copy;

  @override
  State<CopyCommandAgainButton> createState() => _CopyCommandAgainButtonState();
}

final class _CopyCommandAgainButtonState extends State<CopyCommandAgainButton> {
  bool _copied = false;

  @override
  Widget build(BuildContext context) => Tooltip(
    message: widget.command,
    child: OutlinedButton.icon(
      onPressed: () async {
        await Clipboard.setData(ClipboardData(text: widget.command));
        if (mounted) setState(() => _copied = true);
      },
      icon: Icon(_copied ? Icons.check : Icons.copy, size: 14),
      label: Text(
        widget.copy(_copied ? 'setup.start.copied' : 'setup.start.copy_again'),
      ),
    ),
  );
}

/// Above the request list of the session that completed the guide: it is
/// connected, what ViberMate keeps of it, and where to go next.
final class SetupConnectedNotice extends StatelessWidget {
  const SetupConnectedNotice({
    required this.capture,
    required this.controller,
    required this.copy,
    super.key,
  });

  final CaptureRecord capture;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final environments =
        controller.data?.environments ?? const <EnvironmentRecord>[];
    // What is kept, and for how long, comes from the routing set the session
    // runs on, falling back to the built-in one; never a fixed number.
    final assigned =
        controller.selectedAssignment?.environmentId ?? capture.environment?.id;
    final environment =
        environments.where((value) => value.id == assigned).firstOrNull ??
        environments.where((value) => value.systemOwned).firstOrNull;
    final recording = environment?.contentRecording;
    final saved = switch (recording?.mode) {
      'full' => copy.format(
        controller.terminalManagement
            ? 'setup.connected.saved'
            : 'setup.connected.saved.server',
        {'days': recording!.retentionDays},
      ),
      'metadata_only' => copy.format('setup.connected.saved.metadata', {
        'days': recording!.retentionDays,
      }),
      _ => null,
    };
    final link = TextButton.styleFrom(
      padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.sm),
      minimumSize: const Size(0, ViberMetrics.compactControlHeight),
    );
    return Semantics(
      key: const Key('setup-connected-notice'),
      container: true,
      liveRegion: true,
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.fromLTRB(
          ViberSpacing.lg,
          ViberSpacing.md,
          ViberSpacing.sm,
          ViberSpacing.sm,
        ),
        decoration: BoxDecoration(
          color: colors.route.withValues(alpha: 0.1),
          border: Border(
            left: BorderSide(color: colors.route, width: 2),
            bottom: BorderSide(color: colors.dividerSoft),
          ),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Padding(
                  padding: const EdgeInsets.only(top: 1),
                  child: Icon(
                    Icons.check_circle_outline,
                    size: 14,
                    color: colors.route,
                  ),
                ),
                const SizedBox(width: ViberSpacing.md),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy.format('setup.connected', {
                          'agent': capture.displayName,
                        }),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: colors.text,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                      if (saved != null) ...[
                        const SizedBox(height: ViberSpacing.xxs),
                        Text(
                          saved,
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      ],
                      const SizedBox(height: ViberSpacing.xxs),
                      Text(
                        copy('setup.connected.next'),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                IconButton(
                  key: const Key('setup-connected-dismiss'),
                  onPressed: controller.dismissSetupConnected,
                  tooltip: copy('common.dismiss'),
                  icon: const Icon(Icons.close, size: 14),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
              ],
            ),
            Padding(
              padding: const EdgeInsets.only(left: 18),
              child: Wrap(
                spacing: ViberSpacing.xs,
                children: [
                  if (saved != null)
                    TextButton(
                      key: const Key('setup-connected-manage'),
                      style: link,
                      onPressed: controller.openSafetySettings,
                      child: Text(copy('setup.connected.manage')),
                    ),
                  TextButton(
                    key: const Key('setup-connected-accounts'),
                    style: link,
                    onPressed: () => controller.selectSection(
                      WorkbenchSection.providerAccounts,
                    ),
                    child: Text(copy('nav.provider_accounts')),
                  ),
                  TextButton(
                    key: const Key('setup-connected-approvals'),
                    style: link,
                    onPressed: () =>
                        controller.selectSection(WorkbenchSection.network),
                    child: Text(copy('nav.network')),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

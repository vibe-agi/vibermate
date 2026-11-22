import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/bootstrap/terminal_command.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'agent_connection.dart';
import 'launch_command.dart';
import 'settings_view.dart'
    show
        confirmRootTrustInstall,
        confirmTerminalCommandChange,
        rootCAErrorMessage;
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
    this.launched = false,
    this.blocked = false,
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
    return SetupGuideProgress(
      local: controller.terminalManagement,
      command: controller.terminalCommand,
      rootTrusted: setupRootTrusted(controller),
      startedRun: started,
      failedLaunch: latest != null && latest.stoppedBeforeStart ? latest : null,
      observed: captures.any((capture) => capture.observation == 'observed'),
      launched: managed.isNotEmpty,
      blocked:
          started != null &&
          agentCannotConnect(
            started,
            captureEnvironment(
              started,
              controller.data?.environments ?? const <EnvironmentRecord>[],
            ),
          ),
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

  /// A `vibermate run` launch reached ViberMate, which proves Terminal found
  /// the command however it was typed.
  final bool launched;

  /// The started agent cannot connect through ViberMate, so its first
  /// request will never arrive.
  final bool blocked;

  /// The command is set up, so its launch commands can be copied.
  bool get commandInstalled => !local || command?.ready == true;

  /// Set up, and a new Terminal window finds it: the shell said so, could
  /// not be asked, or a launch already came through it.
  bool get commandFound =>
      commandInstalled &&
      (!local ||
          launched ||
          command!.shellFindsCommand != ShellCommandLookup.notFound);

  SetupStepState get commandStep => !local
      ? SetupStepState.hint
      : commandFound
      ? SetupStepState.done
      : SetupStepState.current;

  SetupStepState get startStep => startedRun != null
      ? SetupStepState.done
      : commandInstalled
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

/// The routing set a launch ran on: the selected session's assignment once
/// read, otherwise the one the session list names. Null when none is known.
String? launchRoutingSetId(
  WorkbenchController controller,
  CaptureRecord capture,
) =>
    (controller.selectedCaptureKey == capture.key
        ? controller.selectedAssignment?.environmentId
        : null) ??
    capture.environment?.id;

/// The command that starts [capture]'s program again on the routing set it
/// launched with. The launch's own arguments are not recorded, so only the
/// program is repeated. [sameRoutingSet] is false when that set is unknown
/// and the command uses the built-in one.
({String command, bool sameRoutingSet}) relaunchCommand(
  WorkbenchController controller,
  CaptureRecord capture,
) {
  final program = capture.managedRun?.executableLabel ?? setupClientOf(capture);
  final routingSet = launchRoutingSetId(controller, capture);
  return (
    command: launchCommand(controller, program, routingSetId: routingSet),
    sameRoutingSet: routingSet != null,
  );
}

/// The agent a run launched: 'codex' or 'claude'.
String setupClientOf(CaptureRecord capture) =>
    (capture.managedRun?.executableLabel.toLowerCase() ?? '').contains('codex')
    ? 'codex'
    : 'claude';

/// Whether this Mac trusts ViberMate's certificate; null when that trust is
/// not managed here or not read yet.
bool? setupRootTrusted(WorkbenchController controller) {
  final root = controller.rootCAStatus;
  if (!controller.rootTrustManagement || root == null) return null;
  return root.rootValid && root.installed;
}

/// A stopped Codex launch on a Mac known not to trust ViberMate's
/// certificate: the launcher's own check is what stopped it.
bool launchNeedsRoot(WorkbenchController controller, CaptureRecord capture) =>
    setupClientOf(capture) == 'codex' && setupRootTrusted(controller) == false;

/// What the notice of a launch that stopped before its agent started says
/// under its title. Only causes that leave such a record are named: a
/// program Terminal cannot find stops before any session is created.
String launchStoppedDetail(
  AppCopy copy,
  WorkbenchController controller,
  CaptureRecord capture,
) => copy(
  launchNeedsRoot(controller, capture)
      ? 'capture.launch_incomplete.detail.codex_root'
      : launchWithoutRoutingSet(controller, capture)
      ? 'capture.launch_incomplete.detail.no_routing_set'
      : 'capture.launch_incomplete.detail',
);

/// The open session's assignment was read and it has none. The session list
/// alone cannot say so: its routing set may also be missing because the
/// Runtime could not read it for that page.
bool launchWithoutRoutingSet(
  WorkbenchController controller,
  CaptureRecord capture,
) =>
    controller.selectedCaptureKey == capture.key &&
    controller.selectedCaptureLaunchIncomplete &&
    controller.selectedAssignment == null;

/// The one action for a launch that stopped before its agent started:
/// 'Install and trust' when the certificate stopped it, otherwise 'Copy
/// command again'. Null for a record that is not a `vibermate run` launch.
Widget? launchStoppedAction({
  required WorkbenchController controller,
  required AppCopy copy,
  required CaptureRecord capture,
  required String keyPrefix,
}) {
  if (launchNeedsRoot(controller, capture)) {
    final busy = controller.rootCAMutating || controller.rootCALoading;
    return Builder(
      builder: (context) => FilledButton.icon(
        key: Key('$keyPrefix-install-trust'),
        onPressed: busy
            ? null
            : () =>
                  unawaited(confirmRootTrustInstall(context, controller, copy)),
        icon: const Icon(Icons.verified_user_outlined, size: 14),
        label: Text(copy('setup.start.install_trust')),
      ),
    );
  }
  if (capture.kind != 'managed_run') return null;
  final relaunch = relaunchCommand(controller, capture);
  return CopyCommandAgainButton(
    key: Key('$keyPrefix-copy-again'),
    command: relaunch.command,
    again: relaunch.sameRoutingSet,
    copy: copy,
  );
}

/// Why an agent that cannot connect through ViberMate fails, and which
/// versions can.
String cannotConnectDetail(
  AppCopy copy,
  WorkbenchController controller,
  CaptureRecord capture,
) {
  final client = agentClientId(capture);
  final versions = client == null
      ? null
      : controller.supportedClientVersions(client);
  return copy.sentences([
    copy.format('capture.cannot_connect.detail', {
      'agent': capture.displayName,
    }),
    if (versions != null && versions.isNotEmpty)
      copy.format('capture.cannot_connect.versions', {
        'versions': versions.join(', '),
      }),
  ]);
}

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

  Future<void> _copy(String key, String text) async {
    try {
      await Clipboard.setData(ClipboardData(text: text));
      if (!mounted) return;
      setState(() => _copied = key);
    } on Object {
      if (!mounted) return;
      setState(() => _copied = null);
    }
  }

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final progress = SetupGuideProgress.of(controller);
    final guide = LayoutBuilder(
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
    return SetupFactsWatcher(controller: controller, child: guide);
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
    if (status != null && progress.commandInstalled) {
      // Set up, but a new Terminal window does not find it yet.
      return _SetupStep(
        key: const Key('setup-step-command'),
        number: 1,
        state: state,
        title: copy('setup.command.title'),
        copy: copy,
        status: StatusPill(
          label: copy('terminal.state.not_on_path'),
          color: context.viberColors.warning,
        ),
        detail: copy('terminal.state_detail.not_on_path'),
        action: OutlinedButton.icon(
          key: const Key('setup-command-check'),
          onPressed: controller.terminalCommandLoading
              ? null
              : () => unawaited(controller.checkTerminalShell()),
          icon: const Icon(Icons.refresh, size: 14),
          label: Text(copy('setup.command.check')),
        ),
        narrow: narrow,
        children: [TerminalPathHint(status: status, copy: copy)],
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
        onPressed: () =>
            controller.openAccessSettings(focus: AccessFocus.remote),
        icon: const Icon(Icons.menu_book_outlined, size: 14),
        label: Text(copy('capture.empty.web.action')),
      ),
      narrow: narrow,
      children: [
        CommandBlock(
          key: const Key('setup-command-login'),
          label: copy('server.login.command.client'),
          text: login,
          copyLabel: copy('setup.start.copy'),
          copiedLabel: copy('setup.start.copied'),
          copyFailedLabel: copy('common.copy_failed'),
          copyTooltip: copy('server.login.command.copy'),
          fontSize: ViberType.body,
          enabled: controller.runtimeServerURL.isNotEmpty,
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
    // The certificate was trusted here after this Codex launch stopped, so
    // running it again is all that is left; a later failure is explained
    // as usual.
    final retryAfterTrust =
        failed != null &&
        failedCodex &&
        progress.rootTrusted == true &&
        controller.stoppedBeforeRootTrust(failed);
    // The Codex requirement was met during this app session: say so where
    // the requirement stood instead of letting the line vanish.
    final codexReady =
        failed == null &&
        progress.rootTrusted == true &&
        controller.rootTrustedHere;
    return _SetupStep(
      key: const Key('setup-step-start'),
      number: 2,
      state: state,
      title: copy('setup.start.title'),
      copy: copy,
      status: failed != null && !retryAfterTrust
          ? StatusPill(
              label: copy('setup.status.failed'),
              color: context.viberColors.warning,
            )
          : null,
      detail: copy(
        progress.local ? 'setup.start.detail' : 'setup.server.start.detail',
      ),
      narrow: narrow,
      children: [
        // The same commands as Access and launch; the step's line above
        // already says where to run them.
        LaunchCommandCard(
          controller: controller,
          copy: copy,
          keyPrefix: 'setup',
          lead: false,
          codexFooter: progress.codexNeedsRoot && !installFromFailure
              ? CodexCertificateRow(
                  controller: controller,
                  copy: copy,
                  narrow: narrow,
                )
              : codexReady
              ? _StepNote(
                  key: const Key('setup-codex-ready'),
                  icon: Icons.check,
                  iconColor: context.viberColors.verified,
                  text: copy('setup.start.codex_ready'),
                  narrow: narrow,
                  indent: false,
                )
              : null,
        ),
        if (controller.rootCAError case final error?) ...[
          const SizedBox(height: ViberSpacing.sm),
          InlineNotice(message: rootCAErrorMessage(copy, error), error: true),
        ],
        if (failed != null && retryAfterTrust) ...[
          const SizedBox(height: ViberSpacing.md),
          _StepNote(
            key: const Key('setup-root-trusted-retry'),
            icon: Icons.check,
            iconColor: context.viberColors.verified,
            text: copy('setup.start.root_trusted_retry'),
            narrow: narrow,
            indent: false,
            action: launchStoppedAction(
              controller: controller,
              copy: copy,
              capture: failed,
              keyPrefix: 'setup-root-trusted',
            ),
          ),
        ] else if (failed != null) ...[
          const SizedBox(height: ViberSpacing.md),
          LaunchStoppedNotice(
            key: const Key('setup-launch-failed'),
            capture: failed,
            controller: controller,
            copy: copy,
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
      status: state != SetupStepState.current
          ? null
          : progress.blocked
          ? StatusPill(
              label: copy('capture.cannot_connect.badge'),
              color: context.viberColors.danger,
            )
          : StatusPill(
              label: copy('setup.status.waiting'),
              color: context.viberColors.route,
            ),
      // An agent that cannot connect gets only the explanation of why; the
      // promise that its requests appear would contradict it.
      detail: state == SetupStepState.current && progress.blocked
          ? null
          : copy('setup.prompt.detail'),
      narrow: narrow,
      children: [
        if (state == SetupStepState.current && progress.blocked)
          _CannotConnectNotice(
            capture: progress.startedRun!,
            controller: widget.controller,
            copy: copy,
          )
        else if (state == SetupStepState.current)
          Semantics(
            liveRegion: true,
            child: Row(
              key: const Key('setup-prompt-waiting'),
              children: [
                const CompactProgressIndicator(),
                const SizedBox(width: ViberSpacing.md),
                Expanded(
                  child: Text(
                    copy('setup.prompt.waiting'),
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

/// Rereads what setting up depends on when [child] first shows, and again
/// whenever the window regains focus while it is on screen: meanwhile the
/// person may have set up the command, fixed their PATH in Terminal or
/// trusted the certificate.
final class SetupFactsWatcher extends StatefulWidget {
  const SetupFactsWatcher({
    required this.controller,
    required this.child,
    super.key,
  });

  final WorkbenchController controller;
  final Widget child;

  @override
  State<SetupFactsWatcher> createState() => _SetupFactsWatcherState();
}

final class _SetupFactsWatcherState extends State<SetupFactsWatcher>
    with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(widget.controller.refreshSetupFacts());
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && mounted) {
      unawaited(widget.controller.refreshSetupFacts());
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => widget.child;
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
    // A long status goes under the title rather than squeezing it.
    final heading = Wrap(
      spacing: ViberSpacing.md,
      runSpacing: ViberSpacing.xs,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [titleText, ?status],
    );
    // A button beside the title makes the heading row taller than the
    // title; the marker is centred on that row so it stays level with it.
    final inlineAction = action != null && !narrow;
    return Semantics(
      container: true,
      label: '$label, $title',
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: ViberSpacing.lg),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              crossAxisAlignment: inlineAction
                  ? CrossAxisAlignment.center
                  : CrossAxisAlignment.start,
              children: [
                _StepMarker(number: number, state: state),
                const SizedBox(width: ViberSpacing.lg),
                Expanded(child: heading),
                if (action case final value? when inlineAction) ...[
                  const SizedBox(width: ViberSpacing.md),
                  value,
                ],
              ],
            ),
            Padding(
              padding: const EdgeInsets.only(
                left: _StepMarker.size + ViberSpacing.lg,
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
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

  static const double size = 20;

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
        width: size,
        height: size,
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
                // The circle keeps its size, so its digit does too; the
                // marker is decorative and the step's label carries it.
                textScaler: TextScaler.noScaling,
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

/// One neutral line in a step: an icon, a sentence and, optionally, its
/// button. By default it lines up with the launch commands above it; at
/// phone width the button goes full width under the sentence.
final class _StepNote extends StatelessWidget {
  const _StepNote({
    required this.icon,
    required this.iconColor,
    required this.text,
    required this.narrow,
    this.indent = true,
    this.action,
    super.key,
  });

  final IconData icon;
  final Color iconColor;
  final String text;
  final bool narrow;

  /// Start under the commands rather than under the step's title.
  final bool indent;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final line = Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(top: 1),
          child: Icon(icon, size: 14, color: iconColor),
        ),
        const SizedBox(width: ViberSpacing.sm),
        Expanded(
          child: Text(text, style: Theme.of(context).textTheme.bodySmall),
        ),
      ],
    );
    if (narrow) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          line,
          if (action case final value?) ...[
            const SizedBox(height: ViberSpacing.sm),
            value,
          ],
        ],
      );
    }
    return Padding(
      padding: EdgeInsets.only(left: indent ? 92 : 0),
      child: Row(
        children: [
          Expanded(child: line),
          if (action case final value?) ...[
            const SizedBox(width: ViberSpacing.md),
            value,
          ],
        ],
      ),
    );
  }
}

/// The started agent cannot connect, so the guide stops waiting for it.
final class _CannotConnectNotice extends StatelessWidget {
  const _CannotConnectNotice({
    required this.capture,
    required this.controller,
    required this.copy,
  });

  final CaptureRecord capture;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    return Container(
      key: const Key('setup-cannot-connect'),
      padding: const EdgeInsets.all(ViberSpacing.lg),
      decoration: BoxDecoration(
        color: colors.danger.withValues(alpha: 0.06),
        border: Border.all(color: colors.danger.withValues(alpha: 0.28)),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.error_outline, size: 16, color: colors.danger),
          const SizedBox(width: ViberSpacing.md),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('capture.client.compatibility.blocked'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
                const SizedBox(height: ViberSpacing.xs),
                Text(
                  cannotConnectDetail(copy, controller, capture),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// A launch that stopped before its agent started, in step 2 of the guide:
/// what to fix and the one action that helps. The session itself says the
/// same in place of its request list.
final class LaunchStoppedNotice extends StatelessWidget {
  const LaunchStoppedNotice({
    required this.capture,
    required this.controller,
    required this.copy,
    this.narrow = false,
    super.key,
  });

  final CaptureRecord capture;
  final WorkbenchController controller;
  final AppCopy copy;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final action = launchStoppedAction(
      controller: controller,
      copy: copy,
      capture: capture,
      keyPrefix: 'launch-stopped',
    );
    // A launch that never started is a failed outcome: the ledger's Failed
    // colour, not the Blocked or Can't connect red.
    final colors = context.viberColors;
    return Container(
      padding: const EdgeInsets.all(ViberSpacing.lg),
      decoration: BoxDecoration(
        color: colors.warning.withValues(alpha: 0.06),
        border: Border.all(color: colors.warning.withValues(alpha: 0.28)),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(Icons.error_outline, size: 16, color: colors.warning),
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
                      launchStoppedDetail(copy, controller, capture),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
            ],
          ),
          if (action != null) ...[
            const SizedBox(height: ViberSpacing.md),
            Align(
              alignment: Alignment.centerLeft,
              child: narrow
                  ? SizedBox(width: double.infinity, child: action)
                  : action,
            ),
          ],
        ],
      ),
    );
  }
}

/// 'Copy command again', for a launch that stopped before its agent started.
/// Reads 'Copy launch command' when [again] is false: the command could not
/// be rebuilt as it was run.
final class CopyCommandAgainButton extends StatefulWidget {
  const CopyCommandAgainButton({
    required this.command,
    required this.copy,
    this.again = true,
    super.key,
  });

  final String command;
  final AppCopy copy;
  final bool again;

  @override
  State<CopyCommandAgainButton> createState() => _CopyCommandAgainButtonState();
}

final class _CopyCommandAgainButtonState extends State<CopyCommandAgainButton> {
  bool _copied = false;
  bool _failed = false;

  Future<void> _copy() async {
    try {
      await Clipboard.setData(ClipboardData(text: widget.command));
      if (!mounted) return;
      setState(() {
        _copied = true;
        _failed = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _copied = false;
        _failed = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final button = Tooltip(
      message: widget.command,
      child: OutlinedButton.icon(
        onPressed: () => unawaited(_copy()),
        icon: Icon(_copied ? Icons.check : Icons.copy, size: 14),
        label: Text(
          widget.copy(
            _copied
                ? 'setup.start.copied'
                : widget.again
                ? 'setup.start.copy_again'
                : 'setup.start.copy_launch',
          ),
        ),
      ),
    );
    if (!_failed) return button;
    // Without the clipboard, the command stays on screen to select.
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        button,
        const SizedBox(height: ViberSpacing.xs),
        Text(
          widget.copy('terminal.run.copy_failed'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.danger),
        ),
        SelectableText(widget.command, style: monoStyle),
      ],
    );
  }
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

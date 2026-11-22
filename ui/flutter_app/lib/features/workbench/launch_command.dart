import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/bootstrap/terminal_command.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'routing_set_name.dart';
import 'settings_view.dart' show confirmRootTrustInstall, rootCAStatusPill;
import 'workbench_controller.dart';

/// The one builder behind every command that starts an agent through
/// ViberMate: Access and launch, the setup guide, the Routing header and
/// 'Copy command again' all copy what it returns.
///
/// `vibermate` is the full path while this Mac's login shell does not find
/// the command, so the first command someone copies always works.
String launchCommand(
  WorkbenchController controller,
  String program, {
  String? routingSetId,
}) {
  final environment =
      routingSetId == null || routingSetId == builtInRoutingSetId
      ? ''
      : '--env ${shellWord(routingSetId)} ';
  return '${runCommandPrefix(controller)}$environment-- ${shellWord(program)}';
}

/// `vibermate run `, with the Server to sign in to when this is not the App.
String runCommandPrefix(WorkbenchController controller) =>
    controller.terminalManagement || controller.runtimeServerURL.isEmpty
    ? '${vibermateProgram(controller)} run '
    : 'vibermate run --server ${controller.runtimeServerURL} ';

/// How Terminal is told to run vibermate: by name, or by its full path while
/// this Mac's login shell does not find it. A check that could not run keeps
/// the name, because most shells do find it.
String vibermateProgram(WorkbenchController controller) {
  final status = controller.terminalCommand;
  if (!controller.terminalManagement ||
      status == null ||
      !status.ready ||
      status.shellFindsCommand != ShellCommandLookup.notFound) {
    return 'vibermate';
  }
  return vibermateFullPath(status);
}

/// The installed command's full path, written the short way when it is the
/// usual `~/.local/bin/vibermate`; the command is only ever set up in the
/// person's home folder.
String vibermateFullPath(TerminalCommandStatus status) {
  const home = '/.local/bin/vibermate';
  final target = status.targetPath;
  return target.endsWith(home) ? '~$home' : shellWord(target);
}

/// The line that puts `~/.local/bin` on PATH for [shell]: the person runs it
/// once, so ViberMate itself never edits a shell profile. Terminal starts
/// bash as a login shell, which reads ~/.bash_profile.
String shellPathLine(String? shell) => switch (shell) {
  'fish' => 'fish_add_path ~/.local/bin',
  'bash' =>
    "echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bash_profile",
  _ => "echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc",
};

/// [value] as one word for Terminal: unchanged when it has no characters a
/// shell treats specially, otherwise in single quotes.
String shellWord(String value) =>
    RegExp(r'^[A-Za-z0-9._+@%=:,/-]+$').hasMatch(value)
    ? value
    : "'${value.replaceAll("'", r"'\''")}'";

/// The installed command is there, but a new Terminal window does not find
/// it: the line that fixes that for this shell, and the full-path command
/// that works meanwhile. Both are copied, never run by ViberMate.
final class TerminalPathHint extends StatelessWidget {
  const TerminalPathHint({required this.status, required this.copy, super.key});

  final TerminalCommandStatus status;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    key: const Key('terminal-path-hint'),
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      CommandBlock(
        label: copy('terminal.path.run_once'),
        text: shellPathLine(status.loginShell),
        copyKey: const Key('terminal-path-copy-line'),
        textKey: const Key('terminal-path-line'),
        copyTooltip: copy('terminal.path.copy_line'),
        copiedLabel: copy('common.copied'),
        copyFailedLabel: copy('common.copy_failed'),
      ),
      const SizedBox(height: ViberSpacing.md),
      CommandBlock(
        label: copy('terminal.path.full_path'),
        text: '${vibermateFullPath(status)} run -- claude',
        copyKey: const Key('terminal-path-copy-full'),
        textKey: const Key('terminal-path-full'),
        copyTooltip: copy('terminal.path.copy_full'),
        copiedLabel: copy('common.copied'),
        copyFailedLabel: copy('common.copy_failed'),
      ),
    ],
  );
}

/// The login shell could not be asked: one muted line, and the full-path
/// command it points at.
final class TerminalPathFallback extends StatelessWidget {
  const TerminalPathFallback({
    required this.status,
    required this.copy,
    super.key,
  });

  final TerminalCommandStatus status;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    key: const Key('terminal-path-fallback'),
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Text(
        copy('terminal.path.unknown'),
        style: Theme.of(
          context,
        ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
      ),
      const SizedBox(height: ViberSpacing.sm),
      CommandBlock(
        text: '${vibermateFullPath(status)} run -- claude',
        copyKey: const Key('terminal-path-copy-full'),
        textKey: const Key('terminal-path-full'),
        copyTooltip: copy('terminal.path.copy_full'),
        copiedLabel: copy('common.copied'),
        copyFailedLabel: copy('common.copy_failed'),
      ),
    ],
  );
}

/// Under the Codex command: Codex connects only once this Mac trusts
/// ViberMate's certificate. The same row in Access and launch and in the
/// setup guide; its owner shows it only while the certificate is not
/// trusted.
final class CodexCertificateRow extends StatelessWidget {
  const CodexCertificateRow({
    required this.controller,
    required this.copy,
    this.narrow = false,
    this.keyPrefix = 'setup',
    this.explain = true,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool narrow;

  /// The line saying only Codex needs it; left out under a heading that
  /// already says so.
  final bool explain;

  /// Keys the row and its button: '[keyPrefix]-codex-requirement' and
  /// '[keyPrefix]-install-trust'.
  final String keyPrefix;

  @override
  Widget build(BuildContext context) {
    final status = controller.rootCAStatus;
    final busy = controller.rootCAMutating || controller.rootCALoading;
    final label = Wrap(
      spacing: ViberSpacing.md,
      runSpacing: ViberSpacing.xs,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        Text(
          copy('settings.root_ca.title'),
          style: Theme.of(context).textTheme.labelLarge,
        ),
        if (status != null) rootCAStatusPill(context, copy, status),
      ],
    );
    final button = OutlinedButton.icon(
      key: Key('$keyPrefix-install-trust'),
      onPressed: busy
          ? null
          : () => unawaited(confirmRootTrustInstall(context, controller, copy)),
      icon: busy
          ? const SizedBox.square(
              dimension: 12,
              child: CircularProgressIndicator(strokeWidth: 1.5),
            )
          : const Icon(Icons.verified_user_outlined, size: 14),
      label: Text(copy('setup.start.install_trust')),
    );
    return Semantics(
      container: true,
      child: Column(
        key: Key('$keyPrefix-codex-requirement'),
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (narrow) ...[
            label,
            const SizedBox(height: ViberSpacing.sm),
            button,
          ] else
            Row(
              children: [
                Expanded(child: label),
                const SizedBox(width: ViberSpacing.md),
                button,
              ],
            ),
          if (explain) ...[
            const SizedBox(height: ViberSpacing.xs),
            Text(
              copy('setup.start.codex_requirement'),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// Start Claude Code or Codex: where to run the commands, then each command
/// in Terminal's font with a labelled Copy that turns into Copied. Access
/// and launch, the setup guide and every routing set show this card; the
/// commands come from [launchCommand].
final class LaunchCommandCard extends StatelessWidget {
  const LaunchCommandCard({
    required this.controller,
    required this.copy,
    this.routingSetId,
    this.keyPrefix = 'managed-run',
    this.lead = true,
    this.requirement = true,
    this.codexFooter,
    this.onUseRoutingSet,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  /// The routing set the commands start on; the built-in one when null.
  final String? routingSetId;

  /// Keys each row '[keyPrefix]-command-claude' and its button
  /// '[keyPrefix]-copy-claude', and the same for Codex.
  final String keyPrefix;

  /// 'In Terminal, go to your project folder and run one of these:'.
  final bool lead;

  /// The line saying the agents must already be installed and start by
  /// name: a shell alias is not enough, because the launcher looks the
  /// program up on PATH.
  final bool requirement;

  /// Shown under the Codex command, such as the certificate Codex needs.
  final Widget? codexFooter;

  /// 'Use a routing set instead', which opens Routing.
  final VoidCallback? onUseRoutingSet;

  static const double _labelWidth = 92;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final muted = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: colors.textMuted);
    final enabled =
        !controller.terminalManagement ||
        controller.terminalCommand?.ready == true;
    return LayoutBuilder(
      builder: (context, constraints) {
        final narrow = constraints.maxWidth < 480;
        Widget indented(Widget child) => Padding(
          padding: EdgeInsets.only(left: narrow ? 0 : _labelWidth),
          child: child,
        );
        Widget row(String client, String label) {
          final block = CommandBlock(
            text: launchCommand(controller, client, routingSetId: routingSetId),
            copyLabel: copy('setup.start.copy'),
            copiedLabel: copy('setup.start.copied'),
            copyFailedLabel: copy('common.copy_failed'),
            copyTooltip: copy.format('terminal.run.copy', {'client': label}),
            fontSize: ViberType.body,
            enabled: enabled,
            copyKey: Key('$keyPrefix-copy-$client'),
          );
          final caption = Text(
            label,
            style: Theme.of(context).textTheme.labelLarge,
          );
          return KeyedSubtree(
            key: Key('$keyPrefix-command-$client'),
            child: narrow
                ? Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      caption,
                      const SizedBox(height: ViberSpacing.xs),
                      block,
                    ],
                  )
                : Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      SizedBox(
                        width: _labelWidth,
                        height: ViberMetrics.controlHeight,
                        child: Align(
                          alignment: Alignment.centerLeft,
                          child: caption,
                        ),
                      ),
                      Expanded(child: block),
                    ],
                  ),
          );
        }

        return Column(
          key: Key('$keyPrefix-launch-card'),
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (lead) ...[
              Text(copy('terminal.run.lead'), style: muted),
              const SizedBox(height: ViberSpacing.md),
            ],
            if (!enabled) ...[
              Text(
                copy('terminal.run.install_first'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: ViberSpacing.sm),
            ],
            row('claude', 'Claude Code'),
            const SizedBox(height: ViberSpacing.sm),
            row('codex', 'Codex'),
            if (codexFooter case final footer?) ...[
              const SizedBox(height: ViberSpacing.sm),
              indented(footer),
            ],
            if (requirement) ...[
              const SizedBox(height: ViberSpacing.sm),
              indented(
                Text(
                  copy('terminal.run.requirement'),
                  key: Key('$keyPrefix-requirement'),
                  style: muted,
                ),
              ),
            ],
            if (onUseRoutingSet case final open?) ...[
              const SizedBox(height: ViberSpacing.xs),
              indented(
                Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    key: Key('$keyPrefix-use-routing-set'),
                    onPressed: open,
                    style: TextButton.styleFrom(
                      padding: const EdgeInsets.symmetric(
                        horizontal: ViberSpacing.xs,
                      ),
                    ),
                    icon: const Icon(Icons.alt_route, size: 14),
                    label: Text(copy('terminal.run.use_routing_set')),
                  ),
                ),
              ),
            ],
          ],
        );
      },
    );
  }
}

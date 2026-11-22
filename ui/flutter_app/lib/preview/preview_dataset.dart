import '../core/bootstrap/terminal_command.dart';
import 'preview_control_api.dart';
import 'preview_terminal_command.dart';

/// The data a preview build shows, chosen by the page URL on the Web:
///
///  * no query: the seeded workbench (sessions, accounts, routing sets);
///  * `/?dataset=fresh#/`: a fresh install, with `stage=` (start, command,
///    trusted, waiting, failed, observed) fixing one point of the first run,
///    `arrive=` (1, failed) playing a short timeline from the start, and
///    `client=` (recognized, unverified) choosing the agent build launched;
///  * with either, `shell=` (missing, unknown) choosing what a new Terminal
///    window answers when asked for the vibermate command; found otherwise,
///    and `shell_delay=` (milliseconds, at most 10000) how long it takes to
///    answer, as a slow shell profile would.
///
/// The macOS preview has no query, so it always shows the seeded workbench.
final class PreviewDataset {
  const PreviewDataset({
    this.fresh = false,
    this.stage,
    this.arrive,
    this.client = PreviewFreshClient.known,
    this.shell = ShellCommandLookup.found,
    this.shellDelay = Duration.zero,
  });

  factory PreviewDataset.fromQuery(Map<String, String> query) {
    final shell = switch (query['shell']) {
      'missing' => ShellCommandLookup.notFound,
      'unknown' => ShellCommandLookup.unknown,
      _ => ShellCommandLookup.found,
    };
    final delay = Duration(
      milliseconds: (int.tryParse(query['shell_delay'] ?? '') ?? 0).clamp(
        0,
        10000,
      ),
    );
    if (query['dataset'] != 'fresh') {
      return PreviewDataset(shell: shell, shellDelay: delay);
    }
    return PreviewDataset(
      fresh: true,
      stage: PreviewFreshStage.parse(query['stage']),
      arrive: PreviewFreshArrival.parse(query['arrive']),
      client: PreviewFreshClient.parse(query['client']),
      shell: shell,
      shellDelay: delay,
    );
  }

  final bool fresh;
  final PreviewFreshStage? stage;
  final PreviewFreshArrival? arrive;
  final PreviewFreshClient client;
  final ShellCommandLookup shell;
  final Duration shellDelay;

  /// Without a stage, a timeline starts with the Terminal command ready,
  /// because nothing can launch before it is.
  PreviewFreshStage get effectiveStage =>
      stage ??
      (arrive == null ? PreviewFreshStage.start : PreviewFreshStage.command);

  PreviewFixture build({required bool seedRuntimeUsers}) {
    if (!fresh) {
      return PreviewFixture(
        api: PreviewControlApi(seedRuntimeUsers: seedRuntimeUsers),
        terminalCommands: PreviewTerminalCommandService(
          shellLookup: shell,
          shellDelay: shellDelay,
        ),
      );
    }
    return PreviewFixture.freshInstall(
      stage: effectiveStage,
      arrive: arrive,
      client: client,
      shell: shell,
      shellDelay: shellDelay,
    );
  }
}

/// A preview backend and the Terminal command service that matches it.
final class PreviewFixture {
  const PreviewFixture({required this.api, required this.terminalCommands});

  factory PreviewFixture.freshInstall({
    PreviewFreshStage stage = PreviewFreshStage.start,
    PreviewFreshArrival? arrive,
    PreviewFreshClient client = PreviewFreshClient.known,
    ShellCommandLookup shell = ShellCommandLookup.found,
    Duration shellDelay = Duration.zero,
  }) => PreviewFixture(
    api: PreviewControlApi.freshInstall(
      stage: stage,
      arrive: arrive,
      client: client,
    ),
    terminalCommands: PreviewTerminalCommandService(
      initial: previewFreshTerminalCommand(stage),
      shellLookup: shell,
      shellDelay: shellDelay,
    ),
  );

  final PreviewControlApi api;
  final PreviewTerminalCommandService terminalCommands;
}

/// The Terminal command at [stage]: not set up at the start, ready after.
TerminalCommandStatus previewFreshTerminalCommand(PreviewFreshStage stage) =>
    TerminalCommandStatus(
      state: stage == PreviewFreshStage.start
          ? TerminalCommandState.notInstalled
          : TerminalCommandState.current,
      sourcePath: _previewSourcePath,
      targetPath: _previewTargetPath,
      installedBuild: stage == PreviewFreshStage.start ? null : 'development',
    );

const _previewSourcePath =
    '/Applications/ViberMate.app/Contents/MacOS/vibermate';
const _previewTargetPath = '/Users/preview/.local/bin/vibermate';

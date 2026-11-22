import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/launch_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';
import 'package:vibermate_app/preview/preview_root_trust_installer.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

/// Counts the login-shell checks the controller asks for.
final class _CountingCommands implements TerminalCommandService {
  _CountingCommands(this._inner);

  final PreviewTerminalCommandService _inner;
  int checks = 0;
  int inspections = 0;

  @override
  Future<TerminalCommandStatus> inspect() {
    inspections += 1;
    return _inner.inspect();
  }

  @override
  Future<TerminalCommandStatus> execute(TerminalCommandOperation operation) =>
      _inner.execute(operation);

  @override
  Future<ShellCommandCheck> checkShell(TerminalCommandStatus status) {
    checks += 1;
    return _inner.checkShell(status);
  }
}

void main() {
  group('the login shell probe', () {
    late Directory scratch;
    setUp(() => scratch = Directory.systemTemp.createTempSync('vm-shell-'));
    tearDown(() => scratch.deleteSync(recursive: true));

    String fakeShell(String name, String body) {
      final file = File('${scratch.path}/$name')
        ..writeAsStringSync('#!/bin/sh\n$body\n');
      Process.runSync('chmod', ['+x', file.path]);
      return file.path;
    }

    const target = '/Users/mira/.local/bin/vibermate';

    test('reads only the last line the shell printed', () async {
      final shell = fakeShell(
        'zsh',
        'echo "Welcome back, profile noise"; echo "$target"',
      );
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
      ).check(target);
      expect(check.lookup, ShellCommandLookup.found);
      expect(check.shell, 'zsh');
    });

    test('a shell that finds no vibermate says not found', () async {
      final shell = fakeShell('bash', 'echo "profile noise"; exit 1');
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
      ).check(target);
      expect(check.lookup, ShellCommandLookup.notFound);
      expect(check.shell, 'bash');
    });

    test(
      'another vibermate first on PATH is not claimed as this one',
      () async {
        final shell = fakeShell('zsh', 'echo /opt/old/bin/vibermate');
        final check = await LoginShellProbe(
          environment: {'SHELL': shell},
        ).check(target);
        expect(check.lookup, ShellCommandLookup.unknown);
      },
    );

    test('a link to the same command counts as found', () async {
      final real = File('${scratch.path}/vibermate')..writeAsStringSync('');
      final linked = Link('${scratch.path}/linked')..createSync(real.path);
      final shell = fakeShell('zsh', 'echo ${linked.path}');
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
      ).check(real.path);
      expect(check.lookup, ShellCommandLookup.found);
    });

    test('a slow profile times out as unknown, on time', () async {
      final shell = fakeShell('zsh', 'sleep 5; echo "$target"');
      final clock = Stopwatch()..start();
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
        timeout: const Duration(milliseconds: 200),
      ).check(target);
      expect(check.lookup, ShellCommandLookup.unknown);
      // The orphaned sleep still holds the pipe; the check does not wait.
      expect(clock.elapsed, lessThan(const Duration(seconds: 3)));
    });

    test(
      'a process the profile leaves running does not hold the check',
      () async {
        final shell = fakeShell('zsh', 'sleep 30 & echo "$target"');
        final clock = Stopwatch()..start();
        final check = await LoginShellProbe(
          environment: {'SHELL': shell},
          timeout: const Duration(seconds: 1),
        ).check(target);
        expect(check.lookup, ShellCommandLookup.found);
        expect(clock.elapsed, lessThan(const Duration(seconds: 5)));
      },
    );

    test('starts the shell the way a new Terminal window does', () async {
      // Each guard fails the check: a wrong flag or command, a TERM other
      // than dumb, or stdin left open, which blocks `read` until the time
      // limit.
      final shell = fakeShell('zsh', '''
[ "\$1" = -lic ] || exit 1
[ "\$2" = 'command -v vibermate' ] || exit 1
[ "\$TERM" = dumb ] || exit 1
read -r _
echo "$target"''');
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
        timeout: const Duration(seconds: 2),
      ).check(target);
      expect(check.lookup, ShellCommandLookup.found);
    });

    test('an unfamiliar shell is not started', () async {
      final marker = File('${scratch.path}/started');
      final shell = fakeShell('tcsh', 'touch ${marker.path}; exit 1');
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
      ).check(target);
      // Its failure could mean anything, so it never reads as not found.
      expect(check.lookup, ShellCommandLookup.unknown);
      expect(check.shell, 'tcsh');
      expect(marker.existsSync(), isFalse);
    });

    test('no SHELL means the check cannot run', () async {
      final check = await const LoginShellProbe(environment: {}).check(target);
      expect(check.lookup, ShellCommandLookup.unknown);
    });

    test('the packaged service checks nothing without a probe', () async {
      final service = PackagedTerminalCommandService();
      final check = await service.checkShell(
        const TerminalCommandStatus(
          state: TerminalCommandState.current,
          sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
          targetPath: target,
        ),
      );
      expect(check.lookup, ShellCommandLookup.unknown);
    });
  });

  test('the PATH line matches the login shell', () {
    expect(
      shellPathLine('zsh'),
      'echo \'export PATH="\$HOME/.local/bin:\$PATH"\' >> ~/.zshrc',
    );
    expect(
      shellPathLine('bash'),
      'echo \'export PATH="\$HOME/.local/bin:\$PATH"\' >> ~/.bash_profile',
    );
    expect(shellPathLine('fish'), 'fish_add_path ~/.local/bin');
    // sh, dash and ksh read ~/.profile as login shells.
    for (final shell in ['sh', 'dash', 'ksh']) {
      expect(
        shellPathLine(shell),
        'echo \'export PATH="\$HOME/.local/bin:\$PATH"\' >> ~/.profile',
      );
    }
    // A shell whose settings file is not known gets no line to run.
    expect(shellPathLine('tcsh'), isNull);
    expect(shellPathLine(null), isNull);
  });

  group('the guide and Settings', () {
    Future<WorkbenchController> open(
      WidgetTester tester, {
      PreviewFreshStage stage = PreviewFreshStage.command,
      ShellCommandLookup shell = ShellCommandLookup.notFound,
      AppLanguage language = AppLanguage.english,
      TerminalCommandService? commands,
    }) async {
      await tester.binding.setSurfaceSize(const Size(1440, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = PreviewFixture.freshInstall(stage: stage, shell: shell);
      final controller = WorkbenchController(
        api: fixture.api,
        terminalCommands: commands ?? fixture.terminalCommands,
        previewMode: true,
        rootTrustManagement: true,
        rootTrustInstaller: PreviewRootTrustInstaller(fixture.api),
        closeRuntime: fixture.api.close,
        initialPreferences: WorkbenchPreferences(language: language),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      return controller;
    }

    Future<void> close(
      WidgetTester tester,
      WorkbenchController controller,
    ) async {
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
      await tester.pump();
    }

    Finder inStep(String step, Finder finder) => find.descendant(
      of: find.byKey(Key('setup-step-$step')),
      matching: finder,
    );

    testWidgets('a command Terminal cannot find keeps step 1 open', (
      tester,
    ) async {
      final controller = await open(tester);
      expect(
        controller.terminalCommand!.shellFindsCommand,
        ShellCommandLookup.notFound,
      );
      expect(inStep('command', find.text('Done')), findsNothing);
      expect(
        inStep('command', find.text("Installed · Terminal can't find it yet")),
        findsOneWidget,
      );
      expect(
        tester
            .widget<SelectableText>(find.byKey(const Key('terminal-path-line')))
            .data,
        'echo \'export PATH="\$HOME/.local/bin:\$PATH"\' >> ~/.zshrc',
      );
      expect(
        tester
            .widget<SelectableText>(find.byKey(const Key('terminal-path-full')))
            .data,
        '~/.local/bin/vibermate run -- claude',
      );
      // The launch commands stay copyable, in the form that works now.
      expect(find.text('~/.local/bin/vibermate run -- claude'), findsWidgets);
      expect(find.text('~/.local/bin/vibermate run -- codex'), findsOneWidget);
      expect(find.text('vibermate run -- claude'), findsNothing);
      await close(tester, controller);
    });

    testWidgets('Check again ticks step 1 once Terminal finds it', (
      tester,
    ) async {
      final service = PreviewTerminalCommandService(
        initial: previewFreshTerminalCommand(PreviewFreshStage.command),
        shellLookup: ShellCommandLookup.notFound,
      );
      final controller = await open(tester, commands: service);
      expect(inStep('command', find.text('Done')), findsNothing);
      service.shellLookup = ShellCommandLookup.found;
      await tester.tap(find.byKey(const Key('setup-command-check')));
      await tester.pump(const Duration(milliseconds: 300));
      expect(inStep('command', find.text('Done')), findsOneWidget);
      expect(find.text('vibermate run -- claude'), findsOneWidget);
      await close(tester, controller);
    });

    testWidgets('a launch through the command proves step 1', (tester) async {
      final controller = await open(tester, stage: PreviewFreshStage.waiting);
      expect(inStep('command', find.text('Done')), findsOneWidget);
      await close(tester, controller);
    });

    testWidgets('a shell that finds it gives the plain command', (
      tester,
    ) async {
      final controller = await open(tester, shell: ShellCommandLookup.found);
      expect(inStep('command', find.text('Done')), findsOneWidget);
      expect(find.text('vibermate run -- claude'), findsOneWidget);
      expect(find.byKey(const Key('terminal-path-hint')), findsNothing);
      // Step 2 says the agent must start by its own name in Terminal.
      expect(
        inStep('start', find.byKey(const Key('setup-requirement'))),
        findsOne,
      );
      await close(tester, controller);
    });

    testWidgets('Settings warns, explains and offers both commands', (
      tester,
    ) async {
      final controller = await open(tester, language: AppLanguage.english);
      controller.openAccessSettings();
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      final state = find.byKey(const Key('terminal-command-state'));
      expect(
        find.descendant(
          of: state,
          matching: find.text("Installed · Terminal can't find it yet"),
        ),
        findsOneWidget,
      );
      final pill = tester.widget<StatusPill>(state);
      expect(pill.color, ViberColors.light.warning);
      expect(
        find.descendant(
          of: find.byKey(const Key('terminal-command-panel')),
          matching: find.byKey(const Key('terminal-path-hint')),
        ),
        findsOneWidget,
      );
      await close(tester, controller);
    });

    testWidgets('an unanswered check keeps Ready and points at the path', (
      tester,
    ) async {
      final controller = await open(tester, shell: ShellCommandLookup.unknown);
      expect(inStep('command', find.text('Done')), findsOneWidget);
      controller.openAccessSettings();
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      expect(
        find.descendant(
          of: find.byKey(const Key('terminal-command-state')),
          matching: find.text('Ready'),
        ),
        findsOneWidget,
      );
      expect(find.byKey(const Key('terminal-path-fallback')), findsOneWidget);
      expect(
        find.text(
          'If Terminal says “command not found”, use the full path shown below.',
        ),
        findsOneWidget,
      );
      await close(tester, controller);
    });

    testWidgets('the not-set-up pill reads as a state', (tester) async {
      final controller = await open(
        tester,
        stage: PreviewFreshStage.start,
        shell: ShellCommandLookup.found,
      );
      controller.openAccessSettings();
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      final state = find.byKey(const Key('terminal-command-state'));
      final pill = tester.widget<StatusPill>(state);
      expect(pill.label, 'Not set up');
      expect(pill.icon, Icons.radio_button_unchecked);
      expect(find.text('Set up command'), findsOneWidget);

      await tester.tap(find.byKey(const Key('terminal-command-install')));
      await tester.pump(const Duration(milliseconds: 300));
      expect(find.text('Set up the vibermate command?'), findsOneWidget);
      expect(
        find.text(
          'ViberMate will add the vibermate command at '
          "/Users/preview/.local/bin/vibermate. It won't replace any existing "
          'file or change your shell settings.',
        ),
        findsOneWidget,
      );
      expect(
        find.text(
          'You can remove it here at any time. Your saved sessions are not '
          'affected.',
        ),
        findsOneWidget,
      );
      await close(tester, controller);
    });

    testWidgets('the poll never starts the shell, and keeps its answer', (
      tester,
    ) async {
      final service = _CountingCommands(
        PreviewTerminalCommandService(
          initial: previewFreshTerminalCommand(PreviewFreshStage.command),
          shellLookup: ShellCommandLookup.notFound,
        ),
      );
      final controller = await open(tester, commands: service);
      final checks = service.checks;
      expect(checks, greaterThan(0), reason: 'the guide asked when shown');
      await controller.refreshTerminalCommand(quiet: true);
      await controller.refreshTerminalCommand();
      expect(service.checks, checks);
      expect(
        controller.terminalCommand!.shellFindsCommand,
        ShellCommandLookup.notFound,
        reason: 'a status read keeps the last answer',
      );
      await controller.refreshTerminalCommand(checkShell: true);
      expect(service.checks, checks + 1);
      // Coming back to the window asks again: the person may have fixed
      // their PATH in Terminal meanwhile.
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump(const Duration(milliseconds: 300));
      expect(service.checks, checks + 2);
      await close(tester, controller);
    });

    testWidgets(
      'returning to the window off the guide never starts the shell',
      (tester) async {
        final service = _CountingCommands(
          PreviewTerminalCommandService(
            initial: previewFreshTerminalCommand(PreviewFreshStage.command),
            shellLookup: ShellCommandLookup.notFound,
          ),
        );
        final controller = await open(tester, commands: service);
        expect(controller.setupGuideEligible, isTrue);
        controller.selectSection(WorkbenchSection.environments);
        await tester.pump(const Duration(milliseconds: 300));
        final checks = service.checks;
        final inspections = service.inspections;
        tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.inactive,
        );
        tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.resumed,
        );
        await tester.pump(const Duration(milliseconds: 300));
        expect(service.checks, checks, reason: 'Routing is on screen');
        expect(service.inspections, inspections + 1);
        await close(tester, controller);
      },
    );

    testWidgets('one focus reads the command once with the guide on screen', (
      tester,
    ) async {
      final service = _CountingCommands(
        PreviewTerminalCommandService(
          initial: previewFreshTerminalCommand(PreviewFreshStage.command),
          shellLookup: ShellCommandLookup.notFound,
        ),
      );
      final controller = await open(tester, commands: service);
      final checks = service.checks;
      final inspections = service.inspections;
      // The controller and the guide both reread on focus; they share it.
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
      tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump(const Duration(milliseconds: 300));
      expect(service.inspections, inspections + 1);
      expect(service.checks, checks + 1);
      await close(tester, controller);
    });

    testWidgets('until the shell answers, both screens say Checking', (
      tester,
    ) async {
      final service = PreviewTerminalCommandService(
        initial: previewFreshTerminalCommand(PreviewFreshStage.command),
        shellLookup: ShellCommandLookup.notFound,
        shellDelay: const Duration(seconds: 2),
      );
      final controller = await open(tester, commands: service);
      // Never Ready first: step 1 stays open with a neutral pill, and no
      // command can be copied before it is known which form works.
      expect(inStep('command', find.text('Checking Terminal…')), findsOne);
      expect(inStep('command', find.text('Done')), findsNothing);
      TextButton copyButton(String key) =>
          tester.widget<TextButton>(find.byKey(Key(key)));
      expect(copyButton('setup-copy-claude').onPressed, isNull);
      expect(
        tester
            .widget<OutlinedButton>(
              find.byKey(const Key('setup-command-check')),
            )
            .onPressed,
        isNull,
      );
      await tester.pump(const Duration(seconds: 2));
      await tester.pump();
      expect(
        inStep('command', find.text("Installed · Terminal can't find it yet")),
        findsOne,
      );
      expect(copyButton('setup-copy-claude').onPressed, isNotNull);
      expect(find.text('~/.local/bin/vibermate run -- claude'), findsWidgets);

      // Settings opens on its own check, keeping the last answer meanwhile.
      service.shellLookup = ShellCommandLookup.found;
      controller.openAccessSettings();
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      final state = find.byKey(const Key('terminal-command-state'));
      expect(
        tester.widget<StatusPill>(state).label,
        "Installed · Terminal can't find it yet",
      );
      await tester.pump(const Duration(seconds: 2));
      await tester.pump();
      expect(tester.widget<StatusPill>(state).label, 'Ready');
      expect(copyButton('managed-run-copy-claude').onPressed, isNotNull);
      expect(find.text('vibermate run -- claude'), findsOne);
      await close(tester, controller);
    });

    testWidgets('a command just set up is checked before it is copied', (
      tester,
    ) async {
      final service = PreviewTerminalCommandService(
        initial: previewFreshTerminalCommand(PreviewFreshStage.command),
        shellLookup: ShellCommandLookup.notFound,
        shellDelay: const Duration(seconds: 2),
      );
      final controller = await open(
        tester,
        commands: service,
        stage: PreviewFreshStage.command,
      );
      controller.hideSetupGuide();
      await tester.pump(const Duration(seconds: 3));
      // Removing forgets the answer; setting it up again asks anew.
      await controller.changeTerminalCommand(TerminalCommandOperation.remove);
      await controller.changeTerminalCommand(TerminalCommandOperation.install);
      controller.openAccessSettings();
      await tester.pump();
      final state = find.byKey(const Key('terminal-command-state'));
      expect(tester.widget<StatusPill>(state).label, 'Checking Terminal…');
      expect(
        tester
            .widget<TextButton>(
              find.byKey(const Key('managed-run-copy-claude')),
            )
            .onPressed,
        isNull,
      );
      await tester.pump(const Duration(seconds: 2));
      await tester.pump();
      expect(
        tester.widget<StatusPill>(state).label,
        "Installed · Terminal can't find it yet",
      );
      await close(tester, controller);
    });

    testWidgets('a launch with the full path is Ready on both screens', (
      tester,
    ) async {
      final controller = await open(tester, stage: PreviewFreshStage.waiting);
      expect(controller.terminalReach, TerminalReach.fullPathWorks);
      const works =
          'Works with the full path ~/.local/bin/vibermate. To type just '
          'vibermate, run the PATH line once.';
      expect(inStep('command', find.text('Done')), findsOne);
      expect(inStep('command', find.text(works)), findsOne);
      controller.openAccessSettings();
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      final state = find.byKey(const Key('terminal-command-state'));
      expect(tester.widget<StatusPill>(state).label, 'Ready');
      expect(
        tester
            .widget<Text>(find.byKey(const Key('terminal-command-detail')))
            .data,
        works,
      );
      // The launch proved only the form that ran: commands keep the path,
      // and the PATH line stays offered.
      expect(find.byKey(const Key('terminal-path-hint')), findsOne);
      expect(find.text('~/.local/bin/vibermate run -- codex'), findsOne);
      await close(tester, controller);
    });
  });
}

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

  @override
  Future<TerminalCommandStatus> inspect() => _inner.inspect();

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

    test('a slow profile times out as unknown', () async {
      final shell = fakeShell('zsh', 'sleep 5; echo "$target"');
      final check = await LoginShellProbe(
        environment: {'SHELL': shell},
        timeout: const Duration(milliseconds: 200),
      ).check(target);
      expect(check.lookup, ShellCommandLookup.unknown);
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
    expect(shellPathLine(null), contains('~/.zshrc'));
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
      await close(tester, controller);
    });
  });
}

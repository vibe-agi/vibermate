import 'dart:async';

import 'package:flutter/material.dart';

import '../core/api/control_api.dart';
import '../core/bootstrap/desktop_runtime.dart';
import '../core/bootstrap/terminal_command.dart';
import '../core/design/viber_theme.dart';
import '../core/preferences/workbench_preferences.dart';
import '../features/workbench/workbench_controller.dart';
import '../features/workbench/workbench_shell.dart';
import '../preview/preview_control_api.dart';
import '../preview/preview_terminal_command.dart';

final class ViberMateApp extends StatelessWidget {
  const ViberMateApp({
    required this.previewMode,
    required this.preferChinese,
    this.preferencesStore,
    super.key,
  });

  final bool previewMode;
  final bool preferChinese;
  final WorkbenchPreferencesStore? preferencesStore;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'ViberMate',
      debugShowCheckedModeBanner: false,
      theme: ViberTheme.dark(),
      darkTheme: ViberTheme.dark(),
      themeMode: ThemeMode.dark,
      home: _RuntimeBootstrap(
        previewMode: previewMode,
        preferChinese: preferChinese,
        preferencesStore: preferencesStore,
      ),
    );
  }
}

final class _RuntimeBootstrap extends StatefulWidget {
  const _RuntimeBootstrap({
    required this.previewMode,
    required this.preferChinese,
    required this.preferencesStore,
  });

  final bool previewMode;
  final bool preferChinese;
  final WorkbenchPreferencesStore? preferencesStore;

  @override
  State<_RuntimeBootstrap> createState() => _RuntimeBootstrapState();
}

final class _RuntimeBootstrapState extends State<_RuntimeBootstrap> {
  WorkbenchController? _controller;
  Object? _failure;
  bool _starting = true;
  int _attempt = 0;

  @override
  void initState() {
    super.initState();
    unawaited(_start());
  }

  Future<void> _start() async {
    final attempt = ++_attempt;
    setState(() {
      _starting = true;
      _failure = null;
    });
    try {
      final ControlApi api;
      final TerminalCommandService terminalCommands;
      final Future<void> Function() closeRuntime;
      final WorkbenchPreferencesStore preferencesStore =
          widget.preferencesStore ??
          (widget.previewMode
              ? MemoryWorkbenchPreferencesStore()
              : const PlatformWorkbenchPreferencesStore());
      final loadedPreferences = await loadWorkbenchPreferences(
        preferencesStore,
        fallbackLanguage: widget.preferChinese
            ? AppLanguage.simplifiedChinese
            : AppLanguage.english,
      );
      DesktopRuntime? liveRuntime;
      if (widget.previewMode) {
        final preview = PreviewControlApi();
        api = preview;
        terminalCommands = PreviewTerminalCommandService();
        closeRuntime = preview.close;
      } else {
        final runtime = await DesktopRuntime.start();
        liveRuntime = runtime;
        api = runtime.api;
        terminalCommands = PackagedTerminalCommandService();
        closeRuntime = runtime.close;
      }
      if (!mounted || attempt != _attempt) {
        await closeRuntime();
        return;
      }
      final controller = WorkbenchController(
        api: api,
        terminalCommands: terminalCommands,
        previewMode: widget.previewMode,
        closeRuntime: closeRuntime,
        initialPreferences: loadedPreferences.value,
        preferencesStore: preferencesStore,
        preferencesWritable: loadedPreferences.writable,
        initialPreferencesIssue: loadedPreferences.issue,
      );
      setState(() {
        _controller = controller;
        _starting = false;
      });
      if (liveRuntime != null) {
        unawaited(_watchRuntime(liveRuntime, attempt));
      }
      await controller.initialize();
    } catch (error) {
      if (!mounted || attempt != _attempt) return;
      setState(() {
        _failure = error;
        _starting = false;
      });
    }
  }

  Future<void> _watchRuntime(DesktopRuntime runtime, int attempt) async {
    await runtime.exitCode;
    if (!mounted || attempt != _attempt || runtime.isClosed) return;
    final controller = _controller;
    setState(() {
      _controller = null;
      _failure = const DesktopRuntimeException(
        'Desktop runtime exited unexpectedly',
        reason: 'daemon_exited',
      );
      _starting = false;
    });
    controller?.dispose();
  }

  @override
  void dispose() {
    _attempt += 1;
    _controller?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final controller = _controller;
    if (controller != null) return WorkbenchShell(controller: controller);
    return Scaffold(
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 460),
          child: Padding(
            padding: const EdgeInsets.all(28),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                const _BootstrapMark(),
                const SizedBox(height: 14),
                Text(
                  'ViberMate',
                  style: Theme.of(context).textTheme.headlineLarge,
                ),
                const SizedBox(height: 5),
                if (_starting) ...[
                  const SizedBox(
                    width: 210,
                    child: LinearProgressIndicator(minHeight: 2),
                  ),
                  const SizedBox(height: 10),
                  Text(
                    widget.previewMode
                        ? 'Preparing Preview evidence…'
                        : 'Starting local traffic runtime…',
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ] else ...[
                  const Icon(
                    Icons.error_outline,
                    size: 23,
                    color: ViberColors.danger,
                  ),
                  const SizedBox(height: 8),
                  Text(
                    _failure.toString(),
                    textAlign: TextAlign.center,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  const SizedBox(height: 12),
                  OutlinedButton.icon(
                    onPressed: _start,
                    icon: const Icon(Icons.refresh, size: 15),
                    label: const Text('Retry'),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _BootstrapMark extends StatelessWidget {
  const _BootstrapMark();

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 42,
      height: 42,
      decoration: BoxDecoration(
        color: ViberColors.route.withValues(alpha: 0.08),
        border: Border.all(color: ViberColors.route.withValues(alpha: 0.35)),
        borderRadius: BorderRadius.circular(9),
      ),
      child: const Icon(Icons.route, color: ViberColors.route, size: 23),
    );
  }
}

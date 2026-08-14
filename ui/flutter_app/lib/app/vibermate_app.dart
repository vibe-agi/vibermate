import 'dart:async';

import 'package:flutter/material.dart';

import '../core/api/control_api.dart';
import '../core/bootstrap/desktop_runtime.dart';
import '../core/bootstrap/terminal_command.dart';
import '../core/design/viber_theme.dart';
import '../core/design/vibermate_mark.dart';
import '../core/design/workbench_window_appearance.dart';
import '../core/i18n/app_copy.dart';
import '../core/preferences/workbench_preferences.dart';
import '../features/workbench/workbench_controller.dart';
import '../features/workbench/workbench_shell.dart';
import '../preview/preview_control_api.dart';
import '../preview/preview_terminal_command.dart';

final class ViberMateApp extends StatefulWidget {
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
  State<ViberMateApp> createState() => _ViberMateAppState();
}

final class _ViberMateAppState extends State<ViberMateApp> {
  static const _windowAppearance = PlatformWorkbenchWindowAppearance();

  WorkbenchTheme _theme = WorkbenchTheme.system;
  late final WorkbenchPreferencesStore _preferencesStore;
  LoadedWorkbenchPreferences? _loadedPreferences;

  @override
  void initState() {
    super.initState();
    _preferencesStore =
        widget.preferencesStore ??
        (widget.previewMode
            ? MemoryWorkbenchPreferencesStore()
            : const PlatformWorkbenchPreferencesStore());
    unawaited(_loadPreferences());
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'ViberMate',
      debugShowCheckedModeBanner: false,
      themeAnimationDuration: Duration.zero,
      theme: ViberTheme.light(),
      darkTheme: ViberTheme.dark(),
      themeMode: switch (_theme) {
        WorkbenchTheme.system => ThemeMode.system,
        WorkbenchTheme.light => ThemeMode.light,
        WorkbenchTheme.dark => ThemeMode.dark,
      },
      home: _loadedPreferences == null
          ? const _PreferenceBootstrapView()
          : _RuntimeBootstrap(
              previewMode: widget.previewMode,
              preferChinese: widget.preferChinese,
              preferencesStore: _preferencesStore,
              loadedPreferences: _loadedPreferences!,
              onThemeChanged: _setTheme,
            ),
    );
  }

  void _setTheme(WorkbenchTheme value) {
    if (_theme == value || !mounted) return;
    setState(() => _theme = value);
    unawaited(_windowAppearance.apply(value));
  }

  Future<void> _loadPreferences() async {
    final loaded = await loadWorkbenchPreferences(
      _preferencesStore,
      fallbackLanguage: widget.preferChinese
          ? AppLanguage.simplifiedChinese
          : AppLanguage.english,
    );
    if (!mounted) return;
    unawaited(_windowAppearance.apply(loaded.value.theme));
    setState(() {
      _theme = loaded.value.theme;
      _loadedPreferences = loaded;
    });
  }
}

final class _PreferenceBootstrapView extends StatelessWidget {
  const _PreferenceBootstrapView();

  @override
  Widget build(BuildContext context) {
    return const Scaffold(
      body: Center(
        child: SizedBox(
          width: 210,
          child: LinearProgressIndicator(minHeight: 2),
        ),
      ),
    );
  }
}

final class _RuntimeBootstrap extends StatefulWidget {
  const _RuntimeBootstrap({
    required this.previewMode,
    required this.preferChinese,
    required this.preferencesStore,
    required this.loadedPreferences,
    required this.onThemeChanged,
  });

  final bool previewMode;
  final bool preferChinese;
  final WorkbenchPreferencesStore preferencesStore;
  final LoadedWorkbenchPreferences loadedPreferences;
  final ValueChanged<WorkbenchTheme> onThemeChanged;

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
      final preferencesStore = widget.preferencesStore;
      final loadedPreferences = widget.loadedPreferences;
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
        onThemeChanged: widget.onThemeChanged,
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
    final copy = AppCopy.forLanguage(
      widget.preferChinese
          ? AppLanguage.simplifiedChinese
          : AppLanguage.english,
    );
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
                        ? copy('bootstrap.preview')
                        : copy('bootstrap.live'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ] else ...[
                  Icon(
                    Icons.error_outline,
                    size: 23,
                    color: context.viberColors.danger,
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
                    label: Text(copy('common.retry')),
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
    return const ViberMateMark(size: 42, framed: true);
  }
}

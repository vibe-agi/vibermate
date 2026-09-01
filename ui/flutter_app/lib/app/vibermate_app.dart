import 'dart:async';

import 'package:flutter/material.dart';

import '../core/api/control_api.dart';
import '../core/bootstrap/platform_runtime.dart';
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

typedef RuntimeConnector =
    Future<RuntimeConnection> Function({String? accessKey});

final class ViberMateApp extends StatefulWidget {
  const ViberMateApp({
    required this.previewMode,
    required this.preferChinese,
    this.preferencesStore,
    this.runtimeConnector = connectPlatformRuntime,
    super.key,
  });

  final bool previewMode;
  final bool preferChinese;
  final WorkbenchPreferencesStore? preferencesStore;
  final RuntimeConnector runtimeConnector;

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
              runtimeConnector: widget.runtimeConnector,
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
    required this.runtimeConnector,
  });

  final bool previewMode;
  final bool preferChinese;
  final WorkbenchPreferencesStore preferencesStore;
  final LoadedWorkbenchPreferences loadedPreferences;
  final ValueChanged<WorkbenchTheme> onThemeChanged;
  final RuntimeConnector runtimeConnector;

  @override
  State<_RuntimeBootstrap> createState() => _RuntimeBootstrapState();
}

final class _RuntimeBootstrapState extends State<_RuntimeBootstrap> {
  WorkbenchController? _controller;
  Object? _failure;
  bool _starting = true;
  bool _loginRequired = false;
  bool _accessKeyVisible = false;
  int _attempt = 0;
  final TextEditingController _accessKey = TextEditingController();

  @override
  void initState() {
    super.initState();
    unawaited(_start());
  }

  Future<void> _start({String? accessKey}) async {
    final attempt = ++_attempt;
    setState(() {
      _starting = true;
      _failure = null;
      _loginRequired = false;
    });
    try {
      final ControlApi api;
      final TerminalCommandService terminalCommands;
      final Future<void> Function() closeRuntime;
      final preferencesStore = widget.preferencesStore;
      final loadedPreferences = widget.loadedPreferences;
      RuntimeConnection? liveRuntime;
      if (widget.previewMode) {
        final preview = PreviewControlApi();
        api = preview;
        terminalCommands = PreviewTerminalCommandService();
        closeRuntime = preview.close;
      } else {
        final runtime = await widget.runtimeConnector(accessKey: accessKey);
        liveRuntime = runtime;
        api = runtime.api;
        terminalCommands = runtime.terminalCommands;
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
        serverManagement: liveRuntime?.serverManagement ?? false,
        terminalManagement: liveRuntime?.terminalManagement ?? true,
        runtimeTarget: liveRuntime?.targetLabel ?? platformRuntimeTargetLabel(),
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
      _accessKey.clear();
      if (liveRuntime != null) {
        unawaited(_watchRuntime(liveRuntime, attempt));
      }
      await controller.initialize();
    } on RuntimeLoginRequired catch (error) {
      if (!mounted || attempt != _attempt) return;
      setState(() {
        _loginRequired = true;
        _failure = error.reason == 'access_key_required' ? null : error;
        _starting = false;
      });
    } catch (error) {
      if (!mounted || attempt != _attempt) return;
      setState(() {
        _failure = error;
        _starting = false;
      });
    }
  }

  Future<void> _watchRuntime(RuntimeConnection runtime, int attempt) async {
    final exitCode = runtime.exitCode;
    if (exitCode == null) return;
    await exitCode;
    if (!mounted || attempt != _attempt || runtime.isClosed()) return;
    final controller = _controller;
    setState(() {
      _controller = null;
      _failure = const RuntimeConnectionException('daemon_exited');
      _starting = false;
    });
    controller?.dispose();
  }

  @override
  void dispose() {
    _attempt += 1;
    _accessKey.dispose();
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
    if (_loginRequired) {
      return RuntimeServerLoginView(
        copy: copy,
        target: platformRuntimeTargetLabel(),
        plaintextTransport: platformRuntimeUsesPlaintext(),
        accessKey: _accessKey,
        accessKeyVisible: _accessKeyVisible,
        failure: _failure,
        onVisibilityChanged: () {
          setState(() => _accessKeyVisible = !_accessKeyVisible);
        },
        onConnect: () => _start(accessKey: _accessKey.text.trim()),
      );
    }
    final sidecarUnavailable =
        _failure is RuntimeConnectionException &&
        (_failure as RuntimeConnectionException).message ==
            'desktop_sidecar_unavailable';
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
                    sidecarUnavailable
                        ? copy('bootstrap.sidecar_unavailable')
                        : _failure.toString(),
                    textAlign: TextAlign.center,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  const SizedBox(height: 12),
                  if (!sidecarUnavailable)
                    OutlinedButton.icon(
                      onPressed: () => _start(),
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

final class RuntimeServerLoginView extends StatelessWidget {
  const RuntimeServerLoginView({
    super.key,
    required this.copy,
    required this.target,
    required this.plaintextTransport,
    required this.accessKey,
    required this.accessKeyVisible,
    required this.failure,
    required this.onVisibilityChanged,
    required this.onConnect,
  });

  final AppCopy copy;
  final String target;
  final bool plaintextTransport;
  final TextEditingController accessKey;
  final bool accessKeyVisible;
  final Object? failure;
  final VoidCallback onVisibilityChanged;
  final VoidCallback onConnect;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final failureCopy = _failureCopy();
    return Scaffold(
      backgroundColor: colors.canvas,
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 520),
              child: DecoratedBox(
                decoration: BoxDecoration(
                  color: colors.panel,
                  border: Border.all(color: colors.divider),
                  borderRadius: ViberMetrics.dialogRadius,
                ),
                child: IntrinsicHeight(
                  child: Row(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Container(
                        width: 6,
                        decoration: BoxDecoration(
                          color: colors.route,
                          borderRadius: const BorderRadius.horizontal(
                            left: Radius.circular(9),
                          ),
                        ),
                      ),
                      Expanded(
                        child: Padding(
                          padding: const EdgeInsets.fromLTRB(26, 24, 26, 26),
                          child: Column(
                            mainAxisSize: MainAxisSize.min,
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              const ViberMateMark(size: 36, framed: true),
                              const SizedBox(height: 18),
                              Text(
                                copy('server.login.title'),
                                style: Theme.of(
                                  context,
                                ).textTheme.headlineLarge,
                              ),
                              const SizedBox(height: 6),
                              Text(
                                copy.format('server.login.target', {
                                  'server': target,
                                }),
                                style: Theme.of(context).textTheme.bodyMedium,
                              ),
                              if (plaintextTransport) ...[
                                const SizedBox(height: 12),
                                Container(
                                  key: const Key('server-login-http-warning'),
                                  padding: const EdgeInsets.all(10),
                                  decoration: BoxDecoration(
                                    color: colors.warning.withValues(
                                      alpha: 0.1,
                                    ),
                                    border: Border(
                                      left: BorderSide(
                                        color: colors.warning,
                                        width: 3,
                                      ),
                                    ),
                                  ),
                                  child: Row(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.start,
                                    children: [
                                      Icon(
                                        Icons.lock_open_outlined,
                                        size: 17,
                                        color: colors.warning,
                                      ),
                                      const SizedBox(width: 8),
                                      Expanded(
                                        child: Text(
                                          copy('server.login.http_warning'),
                                          style: Theme.of(
                                            context,
                                          ).textTheme.bodySmall,
                                        ),
                                      ),
                                    ],
                                  ),
                                ),
                              ],
                              const SizedBox(height: 18),
                              TextField(
                                controller: accessKey,
                                obscureText: !accessKeyVisible,
                                autocorrect: false,
                                enableSuggestions: false,
                                autofocus: true,
                                onSubmitted: (_) => onConnect(),
                                decoration: InputDecoration(
                                  labelText: copy('server.login.access_key'),
                                  hintText: copy(
                                    'server.login.access_key.hint',
                                  ),
                                  suffixIcon: IconButton(
                                    tooltip: copy(
                                      accessKeyVisible
                                          ? 'server.login.hide_key'
                                          : 'server.login.show_key',
                                    ),
                                    onPressed: onVisibilityChanged,
                                    icon: Icon(
                                      accessKeyVisible
                                          ? Icons.visibility_off_outlined
                                          : Icons.visibility_outlined,
                                    ),
                                  ),
                                ),
                              ),
                              const SizedBox(height: 8),
                              Text(
                                copy('server.login.key_help'),
                                style: Theme.of(context).textTheme.bodySmall
                                    ?.copyWith(color: colors.textMuted),
                              ),
                              if (failureCopy != null) ...[
                                const SizedBox(height: 14),
                                DecoratedBox(
                                  decoration: BoxDecoration(
                                    color: colors.danger.withValues(
                                      alpha: 0.08,
                                    ),
                                    border: Border(
                                      left: BorderSide(
                                        color: colors.danger,
                                        width: 3,
                                      ),
                                    ),
                                  ),
                                  child: Padding(
                                    padding: const EdgeInsets.all(10),
                                    child: Row(
                                      children: [
                                        Icon(
                                          Icons.error_outline,
                                          size: 17,
                                          color: colors.danger,
                                        ),
                                        const SizedBox(width: 8),
                                        Expanded(child: Text(failureCopy)),
                                      ],
                                    ),
                                  ),
                                ),
                              ],
                              const SizedBox(height: 20),
                              Align(
                                alignment: Alignment.centerRight,
                                child: ValueListenableBuilder<TextEditingValue>(
                                  valueListenable: accessKey,
                                  builder: (context, value, _) =>
                                      FilledButton.icon(
                                        onPressed: value.text.trim().isEmpty
                                            ? null
                                            : onConnect,
                                        icon: const Icon(Icons.login, size: 17),
                                        label: Text(
                                          copy('server.login.connect'),
                                        ),
                                      ),
                                ),
                              ),
                            ],
                          ),
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  String? _failureCopy() {
    final value = failure;
    if (value is RuntimeLoginRequired) {
      return copy('server.login.error.${value.reason}');
    }
    if (value is RuntimeConnectionException) {
      return copy('server.login.error.${value.message}');
    }
    return value == null ? null : copy('server.login.error.unavailable');
  }
}

final class _BootstrapMark extends StatelessWidget {
  const _BootstrapMark();

  @override
  Widget build(BuildContext context) {
    return const ViberMateMark(size: 42, framed: true);
  }
}

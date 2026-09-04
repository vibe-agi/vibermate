import 'dart:async';

import 'package:flutter/material.dart';

import '../core/api/control_api.dart';
import '../core/api/control_models.dart';
import '../core/bootstrap/platform_runtime.dart';
import '../core/bootstrap/terminal_command.dart';
import '../core/design/viber_theme.dart';
import '../core/design/vibermate_mark.dart';
import '../core/design/workbench_window_appearance.dart';
import '../core/i18n/app_copy.dart';
import '../core/preferences/workbench_preferences.dart';
import '../features/workbench/workbench_controller.dart';
import '../features/workbench/workbench_shell.dart';
import '../features/account/member_portal.dart';
import '../preview/preview_control_api.dart';
import '../preview/preview_terminal_command.dart';
import '../core/bootstrap/root_trust_installer_contract.dart';
import '../preview/preview_root_trust_installer.dart';

typedef RuntimeConnector =
    Future<RuntimeConnection> Function({RuntimeLoginAttempt? login});

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
  RuntimeConnection? _runtime;
  Object? _failure;
  bool _starting = true;
  bool _loginRequired = false;
  RuntimeLoginMode _loginMode = RuntimeLoginMode.signIn;
  bool _passwordVisible = false;
  bool _recoveryKeyVisible = false;
  int _attempt = 0;
  final TextEditingController _username = TextEditingController();
  final TextEditingController _password = TextEditingController();
  final TextEditingController _confirmPassword = TextEditingController();
  final TextEditingController _recoveryKey = TextEditingController();

  @override
  void initState() {
    super.initState();
    unawaited(_start());
  }

  Future<void> _start({RuntimeLoginAttempt? login}) async {
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
      final RootTrustInstaller? rootTrustInstaller;
      final preferencesStore = widget.preferencesStore;
      final loadedPreferences = widget.loadedPreferences;
      RuntimeConnection? liveRuntime;
      if (widget.previewMode) {
        final preview = PreviewControlApi();
        api = preview;
        terminalCommands = PreviewTerminalCommandService();
        closeRuntime = preview.close;
        rootTrustInstaller = PreviewRootTrustInstaller(preview);
      } else {
        final runtime = await widget.runtimeConnector(login: login);
        liveRuntime = runtime;
        api = runtime.api;
        terminalCommands = runtime.terminalCommands;
        closeRuntime = runtime.close;
        rootTrustInstaller = runtime.rootTrustInstaller;
      }
      if (!mounted || attempt != _attempt) {
        await closeRuntime();
        return;
      }
      _runtime = liveRuntime;
      if (liveRuntime?.webPrincipal?.role == RuntimeWebRole.member) {
        setState(() {
          _starting = false;
        });
        _password.clear();
        _confirmPassword.clear();
        _recoveryKey.clear();
        unawaited(_watchRuntime(liveRuntime!, attempt));
        return;
      }
      final controller = WorkbenchController(
        api: api,
        terminalCommands: terminalCommands,
        previewMode: widget.previewMode,
        serverManagement: liveRuntime?.serverManagement ?? false,
        terminalManagement: liveRuntime?.terminalManagement ?? true,
        rootTrustManagement:
            liveRuntime?.rootTrustManagement ?? widget.previewMode,
        rootTrustInstaller: rootTrustInstaller,
        runtimeTarget: liveRuntime?.targetLabel ?? platformRuntimeTargetLabel(),
        closeRuntime: closeRuntime,
        restartRuntime: () async {
          // Invalidate the old watcher before asking the daemon to drain; the
          // next generation is intentionally started with a fresh attempt.
          _attempt += 1;
          final previous = _controller;
          if (mounted) {
            setState(() {
              _controller = null;
              _starting = true;
              _failure = null;
            });
          }
          previous?.dispose();
          await closeRuntime();
          if (!mounted) return;
          await _start();
        },
        initialPreferences: loadedPreferences.value,
        preferencesStore: preferencesStore,
        preferencesWritable: loadedPreferences.writable,
        initialPreferencesIssue: loadedPreferences.issue,
        onThemeChanged: widget.onThemeChanged,
        webPrincipal: liveRuntime?.webPrincipal,
        onSignOut: liveRuntime?.signOut == null ? null : _signOut,
        changeWebPassword: liveRuntime?.changePassword,
      );
      setState(() {
        _controller = controller;
        _starting = false;
      });
      _password.clear();
      _confirmPassword.clear();
      _recoveryKey.clear();
      if (liveRuntime != null) {
        unawaited(_watchRuntime(liveRuntime, attempt));
      }
      await controller.initialize();
    } on RuntimeLoginRequired catch (error) {
      if (!mounted || attempt != _attempt) return;
      setState(() {
        _loginRequired = true;
        if (error.reason == 'setup_required') {
          _loginMode = RuntimeLoginMode.setup;
        } else if (error.reason == 'setup_changed') {
          _loginMode = RuntimeLoginMode.signIn;
        } else if (error.reason.startsWith('recovery_')) {
          _loginMode = RuntimeLoginMode.recover;
        } else {
          _loginMode = RuntimeLoginMode.signIn;
        }
        _failure =
            const {
              'setup_required',
              'credentials_required',
            }.contains(error.reason)
            ? null
            : error;
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
    final webSessionEnded = runtime.webSessionEnded;
    final endings = <Future<bool>>[
      if (exitCode != null) exitCode.then((_) => false),
      if (webSessionEnded != null) webSessionEnded.then((_) => true),
    ];
    if (endings.isEmpty) return;
    final signedOutByServer = await Future.any(endings);
    if (!mounted || attempt != _attempt || runtime.isClosed()) return;
    final controller = _controller;
    if (signedOutByServer) {
      _attempt += 1;
      setState(() {
        _controller = null;
        _runtime = null;
        _loginRequired = true;
        _loginMode = RuntimeLoginMode.signIn;
        _failure = const RuntimeLoginRequired('session_expired');
        _starting = false;
      });
      controller?.dispose();
      await runtime.close();
      return;
    }
    setState(() {
      _controller = null;
      _runtime = null;
      _failure = const RuntimeConnectionException('daemon_exited');
      _starting = false;
    });
    controller?.dispose();
  }

  void _setLoginMode(RuntimeLoginMode value) {
    if (_loginMode == value) return;
    setState(() {
      _loginMode = value;
      _failure = null;
      _password.clear();
      _confirmPassword.clear();
      if (value == RuntimeLoginMode.signIn) _recoveryKey.clear();
    });
  }

  void _submitLogin() {
    final password = _password.text;
    final attempt = switch (_loginMode) {
      RuntimeLoginMode.signIn => RuntimeLoginAttempt.signIn(
        username: _username.text.trim(),
        password: password,
      ),
      RuntimeLoginMode.setup => RuntimeLoginAttempt.setup(
        recoveryKey: _recoveryKey.text.trim(),
        username: _username.text.trim(),
        password: password,
      ),
      RuntimeLoginMode.recover => RuntimeLoginAttempt.recover(
        recoveryKey: _recoveryKey.text.trim(),
        password: password,
      ),
    };
    unawaited(_start(login: attempt));
  }

  Future<void> _signOut() async {
    final runtime = _runtime;
    if (runtime == null) return;
    _attempt += 1;
    final controller = _controller;
    setState(() {
      _controller = null;
      _runtime = null;
      _starting = true;
      _failure = null;
    });
    try {
      if (runtime.signOut case final signOut?) {
        await signOut();
      } else {
        await runtime.close();
      }
    } on Object {
      // A local sign-out still forgets the in-memory browser capability. The
      // short-lived Server copy expires independently if the network vanished.
    }
    controller?.dispose();
    if (!mounted) return;
    await _start();
  }

  @override
  void dispose() {
    _attempt += 1;
    final runtime = _runtime;
    _runtime = null;
    if (runtime != null) unawaited(runtime.close());
    _username.dispose();
    _password.dispose();
    _confirmPassword.dispose();
    _recoveryKey.dispose();
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
    final runtime = _runtime;
    if (runtime?.webPrincipal?.role == RuntimeWebRole.member) {
      return MemberPortal(runtime: runtime!, copy: copy, onSignOut: _signOut);
    }
    if (_loginRequired) {
      return RuntimeServerLoginView(
        copy: copy,
        target: platformRuntimeTargetLabel(),
        plaintextTransport: platformRuntimeUsesPlaintext(),
        mode: _loginMode,
        username: _username,
        password: _password,
        confirmPassword: _confirmPassword,
        recoveryKey: _recoveryKey,
        passwordVisible: _passwordVisible,
        recoveryKeyVisible: _recoveryKeyVisible,
        failure: _failure,
        onPasswordVisibilityChanged: () {
          setState(() => _passwordVisible = !_passwordVisible);
        },
        onRecoveryKeyVisibilityChanged: () {
          setState(() => _recoveryKeyVisible = !_recoveryKeyVisible);
        },
        onModeChanged: _setLoginMode,
        onConnect: _submitLogin,
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
                        : _bootstrapFailureMessage(copy, _failure),
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

String _bootstrapFailureMessage(AppCopy copy, Object? failure) {
  if (failure is RuntimeConnectionException) {
    return switch (failure.message) {
      'runtime_unavailable' => copy('bootstrap.failure.runtime_unavailable'),
      'runtime_already_active' => copy(
        'bootstrap.failure.runtime_already_active',
      ),
      'secret_store_unavailable' => copy(
        'bootstrap.failure.secret_store_unavailable',
      ),
      'storage_unavailable' => copy('bootstrap.failure.storage_unavailable'),
      'root_reset_failed' => copy('bootstrap.failure.root_reset_failed'),
      _ => copy('bootstrap.failure.runtime_unavailable'),
    };
  }
  return copy('bootstrap.failure.runtime_unavailable');
}

final class RuntimeServerLoginView extends StatelessWidget {
  const RuntimeServerLoginView({
    super.key,
    required this.copy,
    required this.target,
    required this.plaintextTransport,
    required this.mode,
    required this.username,
    required this.password,
    required this.confirmPassword,
    required this.recoveryKey,
    required this.passwordVisible,
    required this.recoveryKeyVisible,
    required this.failure,
    required this.onPasswordVisibilityChanged,
    required this.onRecoveryKeyVisibilityChanged,
    required this.onModeChanged,
    required this.onConnect,
  });

  final AppCopy copy;
  final String target;
  final bool plaintextTransport;
  final RuntimeLoginMode mode;
  final TextEditingController username;
  final TextEditingController password;
  final TextEditingController confirmPassword;
  final TextEditingController recoveryKey;
  final bool passwordVisible;
  final bool recoveryKeyVisible;
  final Object? failure;
  final VoidCallback onPasswordVisibilityChanged;
  final VoidCallback onRecoveryKeyVisibilityChanged;
  final ValueChanged<RuntimeLoginMode> onModeChanged;
  final VoidCallback onConnect;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final failureCopy = _failureCopy();
    final setup = mode == RuntimeLoginMode.setup;
    final recovery = mode == RuntimeLoginMode.recover;
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
                child: ClipRRect(
                  borderRadius: ViberMetrics.dialogRadius,
                  child: Stack(
                    children: [
                      Positioned(
                        left: 0,
                        top: 0,
                        bottom: 0,
                        width: 6,
                        child: ColoredBox(color: colors.route),
                      ),
                      Padding(
                        padding: const EdgeInsets.fromLTRB(26, 24, 26, 26),
                        child: Column(
                          mainAxisSize: MainAxisSize.min,
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            const ViberMateMark(size: 36, framed: true),
                            const SizedBox(height: 18),
                            Text(
                              copy(
                                setup
                                    ? 'server.login.setup.title'
                                    : recovery
                                    ? 'server.login.recovery.title'
                                    : 'server.login.title',
                              ),
                              style: Theme.of(context).textTheme.headlineLarge,
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
                                  color: colors.warning.withValues(alpha: 0.1),
                                  border: Border(
                                    left: BorderSide(
                                      color: colors.warning,
                                      width: 3,
                                    ),
                                  ),
                                ),
                                child: Row(
                                  crossAxisAlignment: CrossAxisAlignment.start,
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
                            Text(
                              copy(
                                setup
                                    ? 'server.login.setup.detail'
                                    : recovery
                                    ? 'server.login.recovery.detail'
                                    : 'server.login.detail',
                              ),
                              style: Theme.of(context).textTheme.bodySmall
                                  ?.copyWith(color: colors.textMuted),
                            ),
                            const SizedBox(height: 14),
                            AutofillGroup(
                              child: Column(
                                children: [
                                  if (setup || recovery) ...[
                                    TextField(
                                      key: const Key('server-recovery-key'),
                                      controller: recoveryKey,
                                      obscureText: !recoveryKeyVisible,
                                      autocorrect: false,
                                      enableSuggestions: false,
                                      autofocus: true,
                                      decoration: InputDecoration(
                                        labelText: copy(
                                          'server.login.recovery_key',
                                        ),
                                        suffixIcon: IconButton(
                                          tooltip: copy(
                                            recoveryKeyVisible
                                                ? 'server.login.hide_key'
                                                : 'server.login.show_key',
                                          ),
                                          onPressed:
                                              onRecoveryKeyVisibilityChanged,
                                          icon: Icon(
                                            recoveryKeyVisible
                                                ? Icons.visibility_off_outlined
                                                : Icons.visibility_outlined,
                                          ),
                                        ),
                                      ),
                                    ),
                                    const SizedBox(height: 7),
                                    Align(
                                      alignment: Alignment.centerLeft,
                                      child: Text(
                                        copy('server.login.recovery_help'),
                                        style: Theme.of(context)
                                            .textTheme
                                            .bodySmall
                                            ?.copyWith(color: colors.textMuted),
                                      ),
                                    ),
                                    const SizedBox(height: 12),
                                  ],
                                  if (!recovery) ...[
                                    TextField(
                                      key: const Key('server-username'),
                                      controller: username,
                                      autofocus: !setup,
                                      autocorrect: false,
                                      enableSuggestions: false,
                                      autofillHints: const [
                                        AutofillHints.username,
                                      ],
                                      textInputAction: TextInputAction.next,
                                      decoration: InputDecoration(
                                        labelText: copy(
                                          'server.login.username',
                                        ),
                                        helperText: copy(
                                          'server.login.username_help',
                                        ),
                                      ),
                                    ),
                                    const SizedBox(height: 12),
                                  ],
                                  TextField(
                                    key: const Key('server-password'),
                                    controller: password,
                                    obscureText: !passwordVisible,
                                    autocorrect: false,
                                    enableSuggestions: false,
                                    autofillHints: [
                                      setup || recovery
                                          ? AutofillHints.newPassword
                                          : AutofillHints.password,
                                    ],
                                    textInputAction: setup || recovery
                                        ? TextInputAction.next
                                        : TextInputAction.done,
                                    onSubmitted: setup || recovery
                                        ? null
                                        : (_) {
                                            if (_complete) onConnect();
                                          },
                                    decoration: InputDecoration(
                                      labelText: copy(
                                        setup || recovery
                                            ? 'server.login.new_password'
                                            : 'server.login.password',
                                      ),
                                      suffixIcon: IconButton(
                                        tooltip: copy(
                                          passwordVisible
                                              ? 'account.password.hide'
                                              : 'account.password.show',
                                        ),
                                        onPressed: onPasswordVisibilityChanged,
                                        icon: Icon(
                                          passwordVisible
                                              ? Icons.visibility_off_outlined
                                              : Icons.visibility_outlined,
                                        ),
                                      ),
                                    ),
                                  ),
                                  if (setup || recovery) ...[
                                    const SizedBox(height: 12),
                                    TextField(
                                      key: const Key('server-confirm-password'),
                                      controller: confirmPassword,
                                      obscureText: !passwordVisible,
                                      autocorrect: false,
                                      enableSuggestions: false,
                                      autofillHints: const [
                                        AutofillHints.newPassword,
                                      ],
                                      textInputAction: TextInputAction.done,
                                      onSubmitted: (_) {
                                        if (_complete) onConnect();
                                      },
                                      decoration: InputDecoration(
                                        labelText: copy(
                                          'server.login.confirm_password',
                                        ),
                                      ),
                                    ),
                                  ],
                                ],
                              ),
                            ),
                            if (failureCopy != null) ...[
                              const SizedBox(height: 14),
                              DecoratedBox(
                                decoration: BoxDecoration(
                                  color: colors.danger.withValues(alpha: 0.08),
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
                            const SizedBox(height: 16),
                            LayoutBuilder(
                              builder: (context, constraints) {
                                final secondary = !setup
                                    ? TextButton(
                                        key: const Key('server-login-mode'),
                                        onPressed: () => onModeChanged(
                                          recovery
                                              ? RuntimeLoginMode.signIn
                                              : RuntimeLoginMode.recover,
                                        ),
                                        child: Text(
                                          copy(
                                            recovery
                                                ? 'server.login.back'
                                                : 'server.login.forgot',
                                          ),
                                        ),
                                      )
                                    : null;
                                final primary = ListenableBuilder(
                                  listenable: Listenable.merge([
                                    username,
                                    password,
                                    confirmPassword,
                                    recoveryKey,
                                  ]),
                                  builder: (context, _) => FilledButton.icon(
                                    key: const Key('server-login-submit'),
                                    onPressed: _complete ? onConnect : null,
                                    icon: Icon(
                                      setup
                                          ? Icons.person_add_alt_1
                                          : recovery
                                          ? Icons.restart_alt
                                          : Icons.login,
                                      size: 17,
                                    ),
                                    label: Text(
                                      copy(
                                        setup
                                            ? 'server.login.setup.action'
                                            : recovery
                                            ? 'server.login.recovery.action'
                                            : 'server.login.connect',
                                      ),
                                    ),
                                  ),
                                );
                                if (constraints.maxWidth < 330) {
                                  return Column(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.stretch,
                                    children: [
                                      primary,
                                      if (secondary != null) ...[
                                        const SizedBox(height: 6),
                                        Align(
                                          alignment: Alignment.centerLeft,
                                          child: secondary,
                                        ),
                                      ],
                                    ],
                                  );
                                }
                                return Row(
                                  children: [
                                    ?secondary,
                                    const Spacer(),
                                    primary,
                                  ],
                                );
                              },
                            ),
                          ],
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

  bool get _complete {
    final validPassword = password.text.length >= 8;
    return switch (mode) {
      RuntimeLoginMode.signIn =>
        validRuntimeUsernameInput(username.text) && validPassword,
      RuntimeLoginMode.setup =>
        recoveryKey.text.trim().isNotEmpty &&
            validRuntimeUsernameInput(username.text) &&
            validPassword &&
            password.text == confirmPassword.text,
      RuntimeLoginMode.recover =>
        recoveryKey.text.trim().isNotEmpty &&
            validPassword &&
            password.text == confirmPassword.text,
    };
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

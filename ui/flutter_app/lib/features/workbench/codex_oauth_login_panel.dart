import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class CodexOAuthLoginPanel extends StatefulWidget {
  const CodexOAuthLoginPanel({
    super.key,
    required this.controller,
    required this.endpoint,
    required this.displayName,
    required this.copy,
    required this.onActiveChanged,
    required this.onCompleted,
  });
  final WorkbenchController controller;
  final UpstreamEndpoint endpoint;
  final String Function() displayName;
  final AppCopy copy;
  final ValueChanged<bool> onActiveChanged;
  final VoidCallback onCompleted;
  @override
  State<CodexOAuthLoginPanel> createState() => _CodexOAuthLoginPanelState();
}

final class _CodexOAuthLoginPanelState extends State<CodexOAuthLoginPanel> {
  final _callback = TextEditingController();
  CodexLogin? _login;
  Timer? _poller;
  bool _busy = false;
  bool _polling = false;
  String? _error;

  @override
  void dispose() {
    _poller?.cancel();
    _callback.clear();
    _callback.dispose();
    final login = _login;
    if (login?.active == true) {
      unawaited(
        widget.controller.cancelCodexLogin(login!.id).catchError((Object _) {}),
      );
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final login = _login;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text('Codex OAuth', style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 8),
        Text(
          copy('provider_accounts.oauth.hint'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 12),
        if (login?.active != true)
          FilledButton.icon(
            key: const Key('codex-oauth-start'),
            onPressed: _busy ? null : _start,
            icon: const Icon(Icons.login, size: 16),
            label: Text(copy('provider_accounts.oauth.start')),
          ),
        if (login?.active == true) ...[
          InlineNotice(
            message: copy('provider_accounts.oauth.${login!.state}'),
          ),
          if (login.authorizationUrl.isNotEmpty) ...[
            const SizedBox(height: 10),
            SelectableText(
              login.authorizationUrl,
              key: const Key('codex-oauth-url'),
              style: monoStyle,
              maxLines: 4,
            ),
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                OutlinedButton.icon(
                  key: const Key('codex-oauth-open'),
                  onPressed: _open,
                  icon: const Icon(Icons.open_in_new, size: 16),
                  label: Text(copy('provider_accounts.oauth.open')),
                ),
                TextButton.icon(
                  key: const Key('codex-oauth-copy'),
                  onPressed: () => Clipboard.setData(
                    ClipboardData(text: login.authorizationUrl),
                  ),
                  icon: const Icon(Icons.copy, size: 16),
                  label: Text(copy('provider_accounts.oauth.copy')),
                ),
              ],
            ),
          ],
          const SizedBox(height: 12),
          Text(
            copy(
              login.callbackMode == 'manual'
                  ? 'provider_accounts.oauth.manual_hint'
                  : 'provider_accounts.oauth.loopback_hint',
            ),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 8),
          CompactLabeledControl(
            label: copy('provider_accounts.oauth.callback'),
            child: TextField(
              key: const Key('codex-oauth-callback'),
              controller: _callback,
              enabled: !_busy && login.state == 'pending',
              obscureText: true,
              autocorrect: false,
              enableSuggestions: false,
              decoration: InputDecoration(
                hintText: 'http://localhost:1455/auth/callback?code=…&state=…',
              ),
            ),
          ),
          const SizedBox(height: 8),
          Wrap(
            spacing: 8,
            children: [
              OutlinedButton(
                key: const Key('codex-oauth-submit'),
                onPressed: _busy || login.state != 'pending'
                    ? null
                    : _submitCallback,
                child: Text(copy('provider_accounts.oauth.submit')),
              ),
              TextButton(
                key: const Key('codex-oauth-cancel'),
                onPressed: _busy || login.state != 'pending' ? null : _cancel,
                child: Text(copy('provider_accounts.oauth.cancel')),
              ),
            ],
          ),
        ],
        if (login?.reason case final reason?) ...[
          const SizedBox(height: 8),
          InlineNotice(
            message: copy('provider_accounts.oauth.$reason'),
            error: true,
          ),
        ],
        if (_error case final message?) ...[
          const SizedBox(height: 8),
          InlineNotice(message: copy(message), error: true),
        ],
      ],
    );
  }

  Future<void> _start() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    widget.onActiveChanged(true);
    try {
      final login = await widget.controller.startCodexLogin(
        endpoint: widget.endpoint,
        displayName: widget.displayName(),
        callbackMode: kIsWeb ? 'manual' : 'loopback',
      );
      if (!mounted) {
        await widget.controller.cancelCodexLogin(login.id);
        return;
      }
      setState(() => _login = login);
      _poller?.cancel();
      _poller = Timer.periodic(
        const Duration(seconds: 2),
        (_) => unawaited(_poll()),
      );
    } catch (_) {
      if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.start_failed');
        widget.onActiveChanged(false);
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _open() async {
    final url = _login?.authorizationUrl;
    if (url == null || url.isEmpty) return;
    try {
      if (!await launchUrl(
        Uri.parse(url),
        mode: LaunchMode.externalApplication,
      )) {
        throw const FormatException();
      }
    } catch (_) {
      if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.open_failed');
      }
    }
  }

  Future<void> _poll() async {
    final login = _login;
    if (!mounted || _busy || _polling || login?.active != true) return;
    _polling = true;
    try {
      await _accept(await widget.controller.codexLoginStatus(login!.id));
    } on ControlProblem catch (problem) {
      if (mounted && (problem.status == 404 || problem.status == 401)) {
        _poller?.cancel();
        setState(() {
          _login = null;
          _error = 'provider_accounts.oauth.login_expired';
        });
        widget.onActiveChanged(false);
      } else if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.status_failed');
      }
    } catch (_) {
      if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.status_failed');
      }
    } finally {
      _polling = false;
    }
  }

  Future<void> _submitCallback() async {
    if (_callback.text.trim().isEmpty) {
      setState(() => _error = 'provider_accounts.oauth.callback_invalid');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _accept(
        await widget.controller.completeCodexLogin(
          _login!.id,
          _callback.text.trim(),
        ),
      );
    } catch (_) {
      if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.callback_invalid');
      }
    } finally {
      if (mounted) {
        _callback.clear();
        setState(() => _busy = false);
      }
    }
  }

  Future<void> _accept(CodexLogin login) async {
    if (!mounted || _login?.id != login.id) return;
    // Ignore stale polls and duplicate completion so navigation happens once.
    if (_login?.active != true) return;
    setState(() {
      _login = login;
      _error = null;
    });
    widget.onActiveChanged(login.active);
    if (!login.active) _poller?.cancel();
    if (login.state == 'completed') {
      await widget.controller.refresh();
      if (mounted) widget.onCompleted();
    }
  }

  Future<void> _cancel() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await widget.controller.cancelCodexLogin(_login!.id);
      if (!mounted) return;
      _poller?.cancel();
      setState(() {
        _login = null;
        _callback.clear();
      });
      widget.onActiveChanged(false);
    } catch (_) {
      if (mounted) {
        setState(() => _error = 'provider_accounts.oauth.status_failed');
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }
}

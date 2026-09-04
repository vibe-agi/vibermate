import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/bootstrap/runtime_connection.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

enum _AccountAction { password, signOut }

final class WebAccountButton extends StatelessWidget {
  const WebAccountButton({
    required this.principal,
    required this.copy,
    required this.onSignOut,
    this.onChangePassword,
    this.compact = false,
    super.key,
  });

  final RuntimeWebPrincipal principal;
  final AppCopy copy;
  final Future<void> Function()? onSignOut;
  final Future<void> Function(String currentPassword, String newPassword)?
  onChangePassword;
  final bool compact;

  @override
  Widget build(BuildContext context) => PopupMenuButton<_AccountAction>(
    key: const Key('web-account-menu'),
    tooltip: copy('account.menu'),
    onSelected: (action) {
      switch (action) {
        case _AccountAction.password:
          final change = onChangePassword;
          if (change != null) {
            unawaited(
              showDialog<void>(
                context: context,
                builder: (_) => _PasswordDialog(copy: copy, onChange: change),
              ),
            );
          }
        case _AccountAction.signOut:
          final signOut = onSignOut;
          if (signOut != null) unawaited(signOut());
      }
    },
    itemBuilder: (context) => [
      PopupMenuItem(
        enabled: false,
        child: ListTile(
          dense: true,
          contentPadding: EdgeInsets.zero,
          leading: const Icon(Icons.account_circle_outlined, size: 18),
          title: Text(principal.username, overflow: TextOverflow.ellipsis),
          subtitle: Text(
            copy(
              principal.owner ? 'account.role.owner' : 'account.role.member',
            ),
          ),
        ),
      ),
      if (onChangePassword != null)
        PopupMenuItem(
          value: _AccountAction.password,
          child: ListTile(
            dense: true,
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.password_outlined, size: 18),
            title: Text(copy('account.change_password')),
          ),
        ),
      if (onSignOut != null)
        PopupMenuItem(
          value: _AccountAction.signOut,
          child: ListTile(
            dense: true,
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.logout, size: 18),
            title: Text(copy('account.sign_out')),
          ),
        ),
    ],
    child: Container(
      height: ViberMetrics.controlHeight,
      padding: const EdgeInsets.symmetric(horizontal: 9),
      constraints: BoxConstraints(maxWidth: compact ? 54 : 240),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            Icons.account_circle_outlined,
            size: 16,
            color: context.viberColors.route,
          ),
          if (!compact) ...[
            const SizedBox(width: 6),
            Flexible(
              child: Text(
                principal.username,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.labelMedium,
              ),
            ),
            const SizedBox(width: 5),
            Text(
              copy(
                principal.owner ? 'account.role.owner' : 'account.role.member',
              ),
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
          const SizedBox(width: 2),
          const Icon(Icons.arrow_drop_down, size: 16),
        ],
      ),
    ),
  );
}

final class _PasswordDialog extends StatefulWidget {
  const _PasswordDialog({required this.copy, required this.onChange});

  final AppCopy copy;
  final Future<void> Function(String currentPassword, String newPassword)
  onChange;

  @override
  State<_PasswordDialog> createState() => _PasswordDialogState();
}

final class _PasswordDialogState extends State<_PasswordDialog> {
  final _current = TextEditingController();
  final _next = TextEditingController();
  final _confirm = TextEditingController();
  bool _visible = false;
  bool _busy = false;
  String? _error;

  bool get _complete =>
      _current.text.isNotEmpty &&
      _next.text.length >= 8 &&
      _next.text == _confirm.text;

  @override
  void dispose() {
    _current.dispose();
    _next.dispose();
    _confirm.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    key: const Key('web-password-dialog'),
    title: Text(widget.copy('account.password.title')),
    content: AutofillGroup(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            _field(
              key: const Key('web-current-password'),
              controller: _current,
              label: widget.copy('account.password.current'),
              hints: const [AutofillHints.password],
              autofocus: true,
            ),
            const SizedBox(height: 10),
            _field(
              key: const Key('web-new-password'),
              controller: _next,
              label: widget.copy('account.password.new'),
              hints: const [AutofillHints.newPassword],
            ),
            const SizedBox(height: 10),
            _field(
              key: const Key('web-confirm-password'),
              controller: _confirm,
              label: widget.copy('account.password.confirm'),
              hints: const [AutofillHints.newPassword],
            ),
            if (_error case final error?) ...[
              const SizedBox(height: 10),
              InlineNotice(message: error, error: true),
            ],
          ],
        ),
      ),
    ),
    actions: [
      TextButton(
        onPressed: _busy ? null : () => Navigator.of(context).pop(),
        child: Text(widget.copy('common.cancel')),
      ),
      ListenableBuilder(
        listenable: Listenable.merge([_current, _next, _confirm]),
        builder: (context, _) => FilledButton(
          key: const Key('web-password-save'),
          onPressed: _busy || !_complete ? null : () => unawaited(_submit()),
          child: _busy
              ? const CompactProgressIndicator()
              : Text(widget.copy('common.save')),
        ),
      ),
    ],
  );

  Widget _field({
    required Key key,
    required TextEditingController controller,
    required String label,
    required Iterable<String> hints,
    bool autofocus = false,
  }) => TextField(
    key: key,
    controller: controller,
    enabled: !_busy,
    autofocus: autofocus,
    obscureText: !_visible,
    autocorrect: false,
    enableSuggestions: false,
    autofillHints: hints,
    onChanged: (_) => setState(() => _error = null),
    decoration: InputDecoration(
      labelText: label,
      suffixIcon: IconButton(
        tooltip: widget.copy(
          _visible ? 'account.password.hide' : 'account.password.show',
        ),
        onPressed: () => setState(() => _visible = !_visible),
        icon: Icon(
          _visible ? Icons.visibility_off_outlined : Icons.visibility_outlined,
        ),
      ),
    ),
  );

  Future<void> _submit() async {
    if (!_complete || _busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await widget.onChange(_current.text, _next.text);
      if (mounted) Navigator.of(context).pop();
    } on RuntimeLoginRequired catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = widget.copy('account.password.error.${error.reason}');
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = widget.copy('account.password.error.unavailable');
      });
    }
  }
}

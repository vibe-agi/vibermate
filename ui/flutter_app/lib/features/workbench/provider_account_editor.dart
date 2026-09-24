import 'dart:convert';
import 'dart:typed_data';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/api/provider_origin.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'account_header_policy_editor.dart';
import 'codex_oauth_login_panel.dart';
import 'workbench_controller.dart';
import 'control_failure_notice.dart';

Future<void> showProviderAccountEditor(
  BuildContext context, {
  required WorkbenchController controller,
  required AppCopy copy,
  UpstreamEndpoint? endpoint,
  ProviderAccount? account,
}) => showDialog<void>(
  context: context,
  builder: (_) => _AccountEditorDialog(
    controller: controller,
    endpoint: endpoint,
    account: account,
    copy: copy,
  ),
);

Future<void> showProviderAccountDeletion(
  BuildContext context, {
  required WorkbenchController controller,
  required AppCopy copy,
  required ProviderAccount account,
}) => showDialog<void>(
  context: context,
  builder: (_) => _DeleteAccountDialog(
    controller: controller,
    account: account,
    copy: copy,
  ),
);

final class ProviderAccountRow extends StatelessWidget {
  const ProviderAccountRow({
    super.key,
    required this.account,
    required this.compact,
    required this.copy,
    required this.busy,
    required this.onReplace,
    required this.onDelete,
    this.onRefresh,
    this.onEditNote,
    this.refreshing = false,
  });

  final ProviderAccount account;
  final bool compact;
  final AppCopy copy;
  final bool busy;
  final VoidCallback onReplace;
  final VoidCallback onDelete;
  final VoidCallback? onRefresh;
  final VoidCallback? onEditNote;
  final bool refreshing;

  @override
  Widget build(BuildContext context) {
    final oauth = account.codexOAuth;
    final credentialLabel = oauth != null
        ? copy('routes.account.oauth_state.${oauth.state}')
        : account.usable
        ? copy('routes.credentials.ready')
        : copy('routes.credentials.unavailable');
    final kindLabel = _localizedCopy(copy, 'routes.account.kind', account.kind);
    final transportLabel = _localizedCopy(
      copy,
      'routes.account.transport',
      account.kind,
    );
    final headerSummary = copy.format('routes.account.headers.summary', {
      'set': account.setHeaderNames.length,
      'delete': account.deleteHeaderNames.length,
    });
    final identitySummary = oauth == null
        ? null
        : [oauth.email ?? oauth.chatgptAccountId, ?oauth.planType].join(' · ');
    final compactActions = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (onRefresh != null)
          compact
              ? IconButton(
                  key: Key('account-refresh-${account.id}'),
                  tooltip: copy('provider_accounts.refresh.action'),
                  onPressed: busy || !account.usable ? null : onRefresh,
                  icon: refreshing
                      ? const SizedBox.square(
                          dimension: 15,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.refresh, size: 15),
                )
              : TextButton.icon(
                  key: Key('account-refresh-${account.id}'),
                  onPressed: busy || !account.usable ? null : onRefresh,
                  icon: refreshing
                      ? const SizedBox.square(
                          dimension: 15,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.refresh, size: 15),
                  label: Text(copy('provider_accounts.refresh.action')),
                ),
        IconButton(
          key: Key('account-update-${account.id}'),
          onPressed: busy ? null : onReplace,
          tooltip: copy('routes.update_credential'),
          icon: const Icon(Icons.key_outlined, size: 15),
        ),
        IconButton(
          key: Key('account-delete-${account.id}'),
          onPressed: busy ? null : onDelete,
          tooltip: copy('routes.delete_account'),
          icon: Icon(
            Icons.delete_outline,
            size: 15,
            color: context.viberColors.danger,
          ),
        ),
      ],
    );
    if (compact) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(13, 9, 5, 9),
        child: Row(
          children: [
            Icon(Icons.key, size: 14, color: context.viberColors.verified),
            const SizedBox(width: 7),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      _identity(context),
                      const SizedBox(height: 4),
                      InlineStatus(
                        label: credentialLabel,
                        color: account.usable
                            ? context.viberColors.verified
                            : context.viberColors.danger,
                      ),
                    ],
                  ),
                  const SizedBox(height: 4),
                  Text(
                    '${identitySummary == null ? kindLabel : '$kindLabel  ·  $identitySummary'}  ·  $transportLabel  ·  $headerSummary  ·  ${copy.format('routes.credentials.epoch', {'epoch': account.credentialEpoch})}',
                    style: monoStyle,
                  ),
                  const SizedBox(height: 4),
                  Text(account.credentialOrigin, style: monoStyle),
                ],
              ),
            ),
            compactActions,
          ],
        ),
      );
    }
    return ConstrainedBox(
      constraints: const BoxConstraints(minHeight: 44),
      child: Padding(
        padding: const EdgeInsets.only(left: 14, right: 5),
        child: Row(
          children: [
            Icon(Icons.key, size: 14, color: context.viberColors.verified),
            const SizedBox(width: 8),
            Expanded(
              child: Tooltip(
                message: account.id,
                child: Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    _identity(context),
                    Text(
                      '${identitySummary == null ? kindLabel : '$kindLabel · $identitySummary'} · $transportLabel · $headerSummary',
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    Text(account.credentialOrigin, style: monoStyle),
                  ],
                ),
              ),
            ),
            const SizedBox(width: 10),
            SizedBox(
              width: 76,
              child: Text(
                copy.format('routes.credentials.epoch', {
                  'epoch': '${account.credentialEpoch}',
                }),
                style: monoStyle,
              ),
            ),
            InlineStatus(
              label: credentialLabel,
              color: account.usable
                  ? context.viberColors.verified
                  : context.viberColors.danger,
            ),
            const SizedBox(width: 4),
            compactActions,
          ],
        ),
      ),
    );
  }

  Widget _identity(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Row(
        children: [
          Flexible(
            child: Text(
              account.displayName,
              maxLines: compact ? 2 : 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.titleSmall,
            ),
          ),
          if (onEditNote != null) ...[
            const SizedBox(width: 4),
            if (!compact && account.note.isEmpty)
              TextButton.icon(
                key: Key('account-note-${account.id}'),
                onPressed: busy ? null : onEditNote,
                icon: const Icon(Icons.edit_note_outlined, size: 16),
                label: Text(copy('provider_accounts.note.add')),
              )
            else
              IconButton(
                key: Key('account-note-${account.id}'),
                onPressed: busy ? null : onEditNote,
                tooltip: copy(
                  account.note.isEmpty
                      ? 'provider_accounts.note.add'
                      : 'provider_accounts.note.edit',
                ),
                constraints: const BoxConstraints.tightFor(
                  width: 28,
                  height: 28,
                ),
                padding: EdgeInsets.zero,
                icon: const Icon(Icons.edit_note_outlined, size: 16),
              ),
          ],
        ],
      ),
      if (account.note.isNotEmpty)
        Tooltip(
          message: account.note,
          child: Text(
            account.note,
            key: Key('account-note-text-${account.id}'),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
        ),
    ],
  );
}

final class _AccountEditorDialog extends StatefulWidget {
  const _AccountEditorDialog({
    required this.controller,
    required this.endpoint,
    required this.account,
    required this.copy,
  });

  final WorkbenchController controller;
  final UpstreamEndpoint? endpoint;
  final ProviderAccount? account;
  final AppCopy copy;

  @override
  State<_AccountEditorDialog> createState() => _AccountEditorDialogState();
}

enum _AccountEntryMethod { oauth, importFile, manual }

final class _AccountEditorDialogState extends State<_AccountEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  final _secret = TextEditingController();
  late String _kind;
  late UpstreamEndpoint _endpoint;
  late final AccountHeaderPolicyDraft _headers;
  bool _submitted = false;
  bool _headerInvalid = false;
  bool _revealCodexAuthJSON = false;
  late _AccountEntryMethod _method;
  bool _loginActive = false;
  bool _fileLoading = false;
  String? _fileError;

  bool get _replacing => widget.account != null;
  bool get _codexOAuth => _kind == 'codex_oauth';
  bool _supportsMethod(UpstreamEndpoint endpoint, _AccountEntryMethod method) =>
      endpoint.state == 'active' &&
      (method == _AccountEntryMethod.manual
          ? endpoint.accountKinds.any((kind) => kind != 'codex_oauth')
          : isChatGPTCodexOrigin(endpoint.origin) &&
                endpoint.accountKinds.contains('codex_oauth'));
  Iterable<UpstreamEndpoint> get _availableEndpoints => widget
      .controller
      .data!
      .endpoints
      .where((endpoint) => _supportsMethod(endpoint, _method));
  bool get _supportsOAuth =>
      _supportsMethod(_endpoint, _AccountEntryMethod.oauth);
  bool get _browserLogin => !_replacing && _method == _AccountEntryMethod.oauth;
  bool get _busy => widget.controller.inventoryMutating || _fileLoading;

  @override
  void initState() {
    super.initState();
    _name = TextEditingController(text: widget.account?.displayName ?? '');
    _endpoint =
        widget.endpoint ??
        widget.controller.data!.endpoints.firstWhere(
          (endpoint) => widget.account == null
              ? endpoint.id ==
                    (widget.controller.selectedEndpointId ??
                        'target.codex.official')
              : endpoint.origin.toString() == widget.account!.credentialOrigin,
          orElse: () => widget.controller.data!.endpoints.first,
        );
    _kind = widget.account?.kind ?? _endpoint.accountKinds.first;
    _method = _replacing
        ? (_codexOAuth
              ? _AccountEntryMethod.importFile
              : _AccountEntryMethod.manual)
        : (_supportsOAuth
              ? _AccountEntryMethod.oauth
              : _AccountEntryMethod.manual);
    if (!_replacing) {
      if (!_supportsMethod(_endpoint, _method)) {
        _endpoint = _availableEndpoints.firstOrNull ?? _endpoint;
      }
      _kind = _method == _AccountEntryMethod.manual
          ? _endpoint.accountKinds.firstWhere((kind) => kind != 'codex_oauth')
          : 'codex_oauth';
    }
    _headers = AccountHeaderPolicyDraft(
      existingSetHeaderNames: widget.account?.setHeaderNames ?? const [],
      initialDeleteHeaderNames: widget.account?.deleteHeaderNames ?? const [],
    );
  }

  @override
  void dispose() {
    _secret.clear();
    _secret.dispose();
    _name.dispose();
    _headers.clearSensitiveValues();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final availableEndpoints = _availableEndpoints.toList();
    final serviceAvailable = availableEndpoints.any(
      (endpoint) => endpoint.id == _endpoint.id,
    );
    return AlertDialog(
      constraints: const BoxConstraints(
        maxWidth: ViberMetrics.dialogStandardWidth + ViberSpacing.xl * 2,
      ),
      insetPadding: ViberDialogInsets.inset,
      titlePadding: ViberDialogInsets.title,
      contentPadding: ViberDialogInsets.content,
      actionsPadding: ViberDialogInsets.actions,
      title: Text(
        _replacing
            ? copy.format('routes.account.replace.title', {
                'name': widget.account!.displayName,
              })
            : copy('routes.account.create.title'),
      ),
      content: SizedBox(
        key: const Key('account-editor-frame'),
        width: ViberMetrics.dialogStandardWidth,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                if (!_replacing) ...[
                  SizedBox(
                    width: double.infinity,
                    child: SegmentedButton<_AccountEntryMethod>(
                      showSelectedIcon: false,
                      segments: [
                        ButtonSegment(
                          value: _AccountEntryMethod.oauth,
                          label: Text(
                            copy('provider_accounts.entry.oauth'),
                            key: const Key('account-entry-oauth'),
                          ),
                        ),
                        ButtonSegment(
                          value: _AccountEntryMethod.importFile,
                          label: Text(
                            copy('provider_accounts.entry.import'),
                            key: const Key('account-entry-import'),
                          ),
                        ),
                        ButtonSegment(
                          value: _AccountEntryMethod.manual,
                          label: Text(
                            copy('provider_accounts.entry.manual'),
                            key: const Key('account-entry-manual'),
                          ),
                        ),
                      ],
                      selected: {_method},
                      onSelectionChanged: _busy || _loginActive
                          ? null
                          : (value) => _changeMethod(value.single),
                    ),
                  ),
                  const SizedBox(height: 16),
                ],
                if (!_replacing)
                  CompactLabeledControl(
                    label: copy('provider_accounts.service'),
                    child: CompactSelectField<String>(
                      key: const Key('account-editor-service'),
                      initialValue: serviceAvailable ? _endpoint.id : null,
                      isExpanded: true,
                      items: [
                        for (final endpoint in availableEndpoints)
                          DropdownMenuItem(
                            value: endpoint.id,
                            child: Text(endpoint.displayName),
                          ),
                      ],
                      onChanged:
                          _busy || _loginActive || availableEndpoints.isEmpty
                          ? null
                          : (value) => setState(() {
                              _endpoint = widget.controller.data!.endpoints
                                  .firstWhere(
                                    (endpoint) => endpoint.id == value,
                                  );
                              _kind = _method == _AccountEntryMethod.manual
                                  ? _endpoint.accountKinds.firstWhere(
                                      (kind) => kind != 'codex_oauth',
                                    )
                                  : 'codex_oauth';
                              _secret.clear();
                              _headerInvalid = false;
                            }),
                    ),
                  )
                else
                  _AuthorityLine(
                    icon: Icons.hub_outlined,
                    label: copy('routes.account.kind.${widget.account!.kind}'),
                    detail: widget.account!.credentialOrigin,
                  ),
                const SizedBox(height: 8),
                if (!_replacing) ...[
                  if (_method == _AccountEntryMethod.manual) ...[
                    CompactLabeledControl(
                      label: copy('routes.account.kind'),
                      child: CompactSelectField<String>(
                        key: const Key('account-editor-kind'),
                        initialValue: _kind,
                        isExpanded: true,
                        items: [
                          for (final kind in _endpoint.accountKinds)
                            if (kind != 'codex_oauth')
                              DropdownMenuItem(
                                value: kind,
                                child: Text(copy('routes.account.kind.$kind')),
                              ),
                        ],
                        onChanged: (value) => setState(() {
                          _kind = value!;
                          _secret.clear();
                          _headerInvalid = false;
                        }),
                      ),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      copy('routes.account.transport.$_kind'),
                      key: const Key('account-editor-auth-transport'),
                      style: monoStyle.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                    const SizedBox(height: 8),
                  ],
                  CompactLabeledControl(
                    label: copy(
                      _browserLogin
                          ? 'provider_accounts.oauth.name'
                          : 'routes.account.name',
                    ),
                    child: TextFormField(
                      key: const Key('account-editor-name'),
                      controller: _name,
                      enabled: !_loginActive,
                      maxLength: 256,
                      textAlignVertical: TextAlignVertical.center,
                      decoration: const InputDecoration(counterText: ''),
                      validator: (value) =>
                          !_browserLogin &&
                              (value == null || value.trim().isEmpty)
                          ? copy('routes.validation.required')
                          : null,
                    ),
                  ),
                  const SizedBox(height: 8),
                ],
                if (!_replacing &&
                    _method != _AccountEntryMethod.manual &&
                    !_supportsOAuth)
                  InlineNotice(
                    key: const Key('account-editor-oauth-unavailable'),
                    message: copy('provider_accounts.oauth.unsupported'),
                  ),
                if (_browserLogin && _supportsOAuth)
                  CodexOAuthLoginPanel(
                    controller: widget.controller,
                    endpoint: _endpoint,
                    displayName: () => _name.text,
                    copy: copy,
                    onActiveChanged: (active) =>
                        setState(() => _loginActive = active),
                    onCompleted: () => Navigator.pop(context),
                  ),
                if (!_browserLogin) ...[
                  if (_replacing && _codexOAuth) ...[
                    _CodexOAuthIdentityCard(
                      account: widget.account!.codexOAuth,
                      copy: copy,
                    ),
                    const SizedBox(height: 10),
                  ],
                  if (_codexOAuth) ...[
                    InlineNotice(
                      key: const Key('account-editor-codex-ownership'),
                      message: copy('routes.account.codex_ownership'),
                    ),
                    const SizedBox(height: 10),
                    Wrap(
                      spacing: 8,
                      runSpacing: 8,
                      children: [
                        OutlinedButton.icon(
                          key: const Key('account-editor-load-auth-json'),
                          onPressed: _busy ? null : _loadCodexAuthJSON,
                          icon: const Icon(Icons.upload_file, size: 16),
                          label: Text(
                            copy('provider_accounts.import.choose_file'),
                          ),
                        ),
                        TextButton.icon(
                          key: const Key('account-editor-paste-auth-json'),
                          onPressed: _busy ? null : _pasteCodexAuthJSON,
                          icon: const Icon(Icons.content_paste, size: 16),
                          label: Text(copy('routes.account.paste')),
                        ),
                      ],
                    ),
                    const SizedBox(height: 10),
                    if (_fileError case final error?)
                      InlineNotice(message: copy(error), error: true),
                    CompactLabeledControl(
                      label: copy('routes.account.codex_auth_json'),
                      detail: copy('routes.account.codex_auth_json_hint'),
                      child: TextFormField(
                        key: const Key('account-editor-codex-auth-json'),
                        controller: _secret,
                        autofocus: _replacing,
                        obscureText: !_revealCodexAuthJSON,
                        obscuringCharacter: '•',
                        minLines: _revealCodexAuthJSON ? 5 : 1,
                        maxLines: _revealCodexAuthJSON ? 9 : 1,
                        autocorrect: false,
                        enableSuggestions: false,
                        style: monoStyle,
                        decoration: InputDecoration(
                          alignLabelWithHint: true,
                          suffixIcon: IconButton(
                            tooltip: copy(
                              _revealCodexAuthJSON
                                  ? 'common.hide_secret'
                                  : 'common.show_secret',
                            ),
                            onPressed: () => setState(
                              () =>
                                  _revealCodexAuthJSON = !_revealCodexAuthJSON,
                            ),
                            icon: Icon(
                              _revealCodexAuthJSON
                                  ? Icons.visibility_off_outlined
                                  : Icons.visibility_outlined,
                            ),
                          ),
                        ),
                        validator: _validateCodexAuthJSON,
                      ),
                    ),
                  ] else
                    CompactLabeledControl(
                      label: copy(
                        _kind == 'bearer_token'
                            ? 'routes.account.bearer_token'
                            : 'routes.account.api_key',
                      ),
                      child: TextFormField(
                        key: const Key('account-editor-secret'),
                        controller: _secret,
                        autofocus: _replacing,
                        obscureText: true,
                        autocorrect: false,
                        enableSuggestions: false,
                        textAlignVertical: TextAlignVertical.center,
                        decoration: const InputDecoration(),
                        validator: (value) {
                          if (value == null || value.isEmpty) {
                            return copy(
                              _kind == 'bearer_token'
                                  ? 'routes.validation.bearer_required'
                                  : 'routes.validation.api_key_required',
                            );
                          }
                          return value.contains(RegExp(r'[\u0000\r\n]'))
                              ? copy('routes.validation.secret')
                              : null;
                        },
                      ),
                    ),
                  const SizedBox(height: 10),
                  if (!_codexOAuth &&
                      _kind == 'bearer_token' &&
                      isChatGPTCodexOrigin(_endpoint.origin)) ...[
                    Text(
                      copy('routes.account.chatgpt_hint'),
                      key: const Key('account-editor-chatgpt-hint'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    const SizedBox(height: 10),
                  ],
                  Text(
                    copy('routes.account.secret_boundary'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  const SizedBox(height: 10),
                  const Divider(height: 1),
                  const SizedBox(height: 9),
                  AccountHeaderPolicyEditor(
                    accountKind: _kind,
                    draft: _headers,
                    copy: copy,
                    enabled: !widget.controller.inventoryMutating,
                  ),
                ],
                if (_headerInvalid) ...[
                  const SizedBox(height: 9),
                  InlineNotice(
                    message: copy('routes.account.headers.validation'),
                    error: true,
                  ),
                ],
                if (_submitted && widget.controller.inventoryError != null) ...[
                  const SizedBox(height: 9),
                  ControlFailureNotice(
                    message: widget.controller.inventoryError!,
                    copy: copy,
                    diagnostic: widget.controller.inventoryErrorDiagnostic,
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _busy ? null : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        if (!_browserLogin)
          FilledButton(
            key: const Key('account-editor-save'),
            onPressed: _busy || (_codexOAuth && !_supportsOAuth)
                ? null
                : _submit,
            child: widget.controller.inventoryMutating
                ? const SizedBox.square(
                    dimension: 13,
                    child: CircularProgressIndicator(strokeWidth: 1.5),
                  )
                : Text(
                    copy(
                      _replacing
                          ? 'routes.account.replace.action'
                          : 'routes.account.create.action',
                    ),
                  ),
          ),
      ],
    );
  }

  void _changeMethod(_AccountEntryMethod method) {
    setState(() {
      _method = method;
      if (!_supportsMethod(_endpoint, method)) {
        _endpoint = _availableEndpoints.firstOrNull ?? _endpoint;
      }
      _kind = method == _AccountEntryMethod.manual
          ? _endpoint.accountKinds.firstWhere((kind) => kind != 'codex_oauth')
          : 'codex_oauth';
      _secret.clear();
      _headers.clearSensitiveValues();
      _fileError = null;
      _headerInvalid = false;
      _submitted = false;
    });
  }

  Future<void> _loadCodexAuthJSON() async {
    setState(() {
      _fileLoading = true;
      _fileError = null;
    });
    try {
      final file = await openFile(
        acceptedTypeGroups: const [
          XTypeGroup(
            label: 'auth.json',
            extensions: ['json'],
            uniformTypeIdentifiers: ['public.json'],
          ),
        ],
      );
      if (file == null || !mounted) return;
      final bytes = BytesBuilder(copy: false);
      try {
        final length = await file.length();
        if (length <= 0 || length > 32 * 1024) {
          throw const FormatException();
        }
        await for (final chunk in file.openRead(0, length)) {
          if (bytes.length + chunk.length > 32 * 1024) {
            throw const FormatException();
          }
          bytes.add(chunk);
        }
        final data = bytes.takeBytes();
        try {
          final text = utf8.decode(data);
          if (_validateCodexAuthJSON(text) != null) {
            throw const FormatException();
          }
          if (mounted) {
            setState(() {
              _secret.text = text;
              _revealCodexAuthJSON = false;
            });
          }
        } finally {
          data.fillRange(0, data.length, 0);
        }
      } finally {
        final remaining = bytes.takeBytes();
        remaining.fillRange(0, remaining.length, 0);
      }
    } catch (_) {
      if (mounted) {
        setState(() => _fileError = 'provider_accounts.import.failed');
      }
    } finally {
      if (mounted) setState(() => _fileLoading = false);
    }
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    late final ProviderAccountHeaderPolicy headerPolicy;
    try {
      headerPolicy = _headers.build(accountKind: _kind);
    } on ControlContractException {
      setState(() => _headerInvalid = true);
      return;
    }
    setState(() => _submitted = true);
    final secret = _secret.text;
    final result = _replacing
        ? await widget.controller.replaceProviderAccountCredential(
            account: widget.account!,
            secret: _codexOAuth ? '' : secret,
            codexAuthJson: _codexOAuth ? secret : '',
            headerPolicy: headerPolicy,
          )
        : await widget.controller.createProviderAccount(
            endpoint: _endpoint,
            displayName: _name.text,
            kind: _kind,
            secret: _codexOAuth ? '' : secret,
            codexAuthJson: _codexOAuth ? secret : '',
            headerPolicy: headerPolicy,
          );
    _secret.clear();
    _headers.clearSensitiveValues();
    if (!mounted) return;
    if (result != null) {
      Navigator.pop(context);
    } else {
      setState(() {});
    }
  }

  String? _validateCodexAuthJSON(String? raw) {
    if (raw == null || raw.isEmpty || utf8.encode(raw).length > 32 * 1024) {
      return widget.copy('routes.validation.codex_auth_json');
    }
    try {
      final value = jsonDecode(raw);
      if (value is! Map || value['auth_mode'] != 'chatgpt') {
        return widget.copy('routes.validation.codex_auth_json');
      }
      final tokens = value['tokens'];
      final apiKey = value['OPENAI_API_KEY'];
      final lastRefreshValue = value['last_refresh'];
      final lastRefresh = lastRefreshValue is String
          ? DateTime.tryParse(lastRefreshValue)
          : null;
      if (tokens is! Map ||
          tokens['id_token'] is! String ||
          (tokens['id_token'] as String).isEmpty ||
          tokens['access_token'] is! String ||
          (tokens['access_token'] as String).isEmpty ||
          tokens['refresh_token'] is! String ||
          (tokens['refresh_token'] as String).isEmpty ||
          (tokens['account_id'] != null && tokens['account_id'] is! String) ||
          (apiKey != null && apiKey != '') ||
          lastRefresh == null ||
          !lastRefresh.isUtc) {
        return widget.copy('routes.validation.codex_auth_json');
      }
      return null;
    } on FormatException {
      return widget.copy('routes.validation.codex_auth_json');
    }
  }

  Future<void> _pasteCodexAuthJSON() async {
    final value = await Clipboard.getData('text/plain');
    if (!mounted || value?.text == null) return;
    setState(() => _secret.text = value!.text!);
  }
}

final class _CodexOAuthIdentityCard extends StatelessWidget {
  const _CodexOAuthIdentityCard({required this.account, required this.copy});

  final CodexOAuthAccount? account;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final value = account;
    if (value == null) return const SizedBox.shrink();
    final title = value.email ?? value.chatgptAccountId;
    final state = copy('routes.account.oauth_state.${value.state}');
    return Container(
      key: const Key('account-editor-codex-profile'),
      width: double.infinity,
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.account_circle_outlined,
                size: 18,
                color: context.viberColors.verified,
              ),
              const SizedBox(width: 7),
              Expanded(
                child: Text(
                  title,
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              InlineStatus(
                label: state,
                color: value.state == 'reconnect_required'
                    ? context.viberColors.danger
                    : context.viberColors.verified,
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            copy.format('routes.account.codex_profile', {
              'account': value.chatgptAccountId,
              'plan': value.planType ?? '—',
            }),
            style: monoStyle,
          ),
        ],
      ),
    );
  }
}

final class _DeleteAccountDialog extends StatefulWidget {
  const _DeleteAccountDialog({
    required this.controller,
    required this.account,
    required this.copy,
  });

  final WorkbenchController controller;
  final ProviderAccount account;
  final AppCopy copy;

  @override
  State<_DeleteAccountDialog> createState() => _DeleteAccountDialogState();
}

final class _DeleteAccountDialogState extends State<_DeleteAccountDialog> {
  ProviderAccountDeleteResult? _blocked;
  bool _submitted = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final blocked = _blocked;
    return AlertDialog(
      title: Text(
        copy.format('routes.account.delete.title', {
          'name': widget.account.displayName,
        }),
      ),
      content: SizedBox(
        width: 440,
        child: SingleChildScrollView(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                copy('routes.account.delete.detail'),
                style: Theme.of(context).textTheme.bodyMedium,
              ),
              const SizedBox(height: 8),
              Text(
                '${widget.account.id}  ·  ${copy.format('routes.credentials.epoch', {'epoch': widget.account.credentialEpoch})}',
                style: monoStyle,
              ),
              if (blocked != null) ...[
                const SizedBox(height: 10),
                Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(9),
                  decoration: BoxDecoration(
                    color: context.viberColors.danger.withValues(alpha: 0.08),
                    border: Border.all(
                      color: context.viberColors.danger.withValues(alpha: 0.35),
                    ),
                    borderRadius: ViberMetrics.surfaceRadius,
                  ),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy('routes.account.delete.blocked'),
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                      const SizedBox(height: 5),
                      for (final reference in blocked.references)
                        Padding(
                          padding: const EdgeInsets.only(bottom: 4),
                          child: Text(
                            copy.format('routes.account.delete.reference', {
                              'environment': reference.environmentName,
                              'revision': reference.environmentRevision,
                              'route': reference.routeId,
                            }),
                            style: monoStyle,
                          ),
                        ),
                      if (blocked.referenceCount > blocked.references.length)
                        Text(
                          copy.format('routes.account.delete.more', {
                            'count':
                                blocked.referenceCount -
                                blocked.references.length,
                          }),
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                    ],
                  ),
                ),
              ],
              if (_submitted &&
                  blocked == null &&
                  widget.controller.inventoryError != null) ...[
                const SizedBox(height: 9),
                ControlFailureNotice(
                  message: widget.controller.inventoryError!,
                  copy: copy,
                  diagnostic: widget.controller.inventoryErrorDiagnostic,
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.inventoryMutating
              ? null
              : () => Navigator.pop(context),
          child: Text(
            blocked == null ? copy('common.cancel') : copy('common.confirm'),
          ),
        ),
        if (blocked == null)
          FilledButton(
            key: const Key('account-delete-confirm'),
            onPressed: widget.controller.inventoryMutating ? null : _delete,
            style: FilledButton.styleFrom(
              backgroundColor: context.viberColors.danger,
            ),
            child: widget.controller.inventoryMutating
                ? const SizedBox.square(
                    dimension: 13,
                    child: CircularProgressIndicator(strokeWidth: 1.5),
                  )
                : Text(copy('routes.account.delete.action')),
          ),
      ],
    );
  }

  Future<void> _delete() async {
    setState(() => _submitted = true);
    final result = await widget.controller.deleteProviderAccount(
      widget.account,
    );
    if (!mounted || result == null) {
      if (mounted) setState(() {});
      return;
    }
    if (result.deleted) {
      Navigator.pop(context);
    } else {
      setState(() => _blocked = result);
    }
  }
}

final class _AuthorityLine extends StatelessWidget {
  const _AuthorityLine({
    required this.icon,
    required this.label,
    required this.detail,
  });

  final IconData icon;
  final String label;
  final String detail;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        children: [
          Icon(icon, size: 15, color: context.viberColors.route),
          const SizedBox(width: 7),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  label,
                  style: Theme.of(
                    context,
                  ).textTheme.bodyMedium?.copyWith(fontWeight: FontWeight.w500),
                ),
                Text(
                  detail,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: monoStyle,
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

String _localizedCopy(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  return copy.maybe(key) ?? value;
}

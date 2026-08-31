import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'message_transform_editor.dart';

typedef AccountSelectorTestCallback =
    Future<AccountSelectorTestResult> Function({
      required AccountSelectorPolicy policy,
      required AccountSelectorTestSample sample,
    });

final class AccountSelectorEditorDialog extends StatefulWidget {
  const AccountSelectorEditorDialog({
    required this.selectorId,
    required this.initial,
    required this.copy,
    required this.testSelector,
    super.key,
  });

  final String selectorId;
  final AccountSelectorPolicy initial;
  final AppCopy copy;
  final AccountSelectorTestCallback testSelector;

  @override
  State<AccountSelectorEditorDialog> createState() =>
      _AccountSelectorEditorDialogState();
}

final class _AccountSelectorEditorDialogState
    extends State<AccountSelectorEditorDialog> {
  late final JavaScriptEditingController _source;
  late final TextEditingController _accounts;
  late final TextEditingController _user;
  late final TextEditingController _workspace;
  late final TextEditingController _model;
  String _protocol = 'anthropic_messages';
  AccountSelectorTestResult? _result;
  String? _error;
  bool _testing = false;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _source = JavaScriptEditingController(text: widget.initial.javaScript);
    _accounts = TextEditingController(
      text:
          'account.work, account.personal, account.team-a, account.team-b, '
          'account.high-capacity, account.standard',
    );
    _user = TextEditingController(text: 'alice');
    _workspace = TextEditingController(text: 'work');
    _model = TextEditingController(text: 'claude-sonnet-4-5');
  }

  @override
  void dispose() {
    _source.dispose();
    _accounts.dispose();
    _user.dispose();
    _workspace.dispose();
    _model.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.escape): () =>
            unawaited(Navigator.of(context).maybePop()),
      },
      child: Focus(
        autofocus: true,
        child: Scaffold(
          key: const Key('account-selector-editor-page'),
          backgroundColor: context.viberColors.canvas,
          body: SafeArea(
            child: SizedBox.expand(
              key: Key('account-selector-dialog-${widget.selectorId}'),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  _header(context),
                  const Divider(height: 1),
                  _scope(context),
                  Expanded(
                    child: LayoutBuilder(
                      builder: (context, constraints) {
                        final editorHeight = (constraints.maxHeight - 68)
                            .clamp(240.0, 440.0)
                            .toDouble();
                        return SingleChildScrollView(
                          key: const Key('account-selector-body-scroll'),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              SizedBox(
                                height: editorHeight,
                                child: _editor(context),
                              ),
                              _sample(context),
                            ],
                          ),
                        );
                      },
                    ),
                  ),
                  if (_result case final result?)
                    Container(
                      key: const Key('account-selector-test-result'),
                      color: context.viberColors.verified.withValues(
                        alpha: 0.08,
                      ),
                      padding: const EdgeInsets.symmetric(
                        horizontal: 14,
                        vertical: 9,
                      ),
                      child: Row(
                        children: [
                          Icon(
                            Icons.check_circle_outline,
                            size: 15,
                            color: context.viberColors.verified,
                          ),
                          const SizedBox(width: 7),
                          Expanded(
                            child: Text(
                              copy.format('account_selector.test.selected', {
                                'account': result.accountId,
                              }),
                            ),
                          ),
                        ],
                      ),
                    ),
                  if (_error case final error?)
                    InlineNotice(message: error, error: true),
                  const Divider(height: 1),
                  _actions(context),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _header(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(16, 12, 8, 10),
    child: Row(
      children: [
        Icon(Icons.data_object, size: 19, color: context.viberColors.route),
        const SizedBox(width: 8),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                copy('account_selector.editor.title'),
                style: Theme.of(context).textTheme.titleLarge,
              ),
              Text(
                copy('account_selector.editor.subtitle'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
        IconButton(
          tooltip: copy('common.close'),
          onPressed: () => Navigator.of(context).pop(),
          icon: const Icon(Icons.close, size: 18),
        ),
      ],
    ),
  );

  Widget _scope(BuildContext context) => Container(
    color: context.viberColors.selection.withValues(alpha: 0.3),
    padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
    child: Row(
      children: [
        const Icon(Icons.lock_outline, size: 14),
        const SizedBox(width: 6),
        Expanded(
          child: Text(
            copy('account_selector.editor.scope'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
      ],
    ),
  );

  Widget _editor(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(12, 10, 12, 8),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          copy('account_selector.editor.detail'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        const SizedBox(height: 6),
        SizedBox(
          height: 28,
          child: ListView.separated(
            key: const Key('account-selector-suggestions'),
            scrollDirection: Axis.horizontal,
            itemCount: _suggestions.length,
            separatorBuilder: (_, _) => const SizedBox(width: 5),
            itemBuilder: (context, index) => ActionChip(
              key: Key('account-selector-suggestion-$index'),
              visualDensity: VisualDensity.compact,
              label: Text(
                _suggestions[index],
                style: const TextStyle(fontFamily: 'Menlo', fontSize: 10.5),
              ),
              onPressed: () => _source.insertSnippet(_suggestions[index]),
            ),
          ),
        ),
        const SizedBox(height: 7),
        Expanded(
          child: DecoratedBox(
            decoration: BoxDecoration(
              color: context.viberColors.input,
              border: Border.all(color: context.viberColors.divider),
              borderRadius: ViberMetrics.controlRadius,
            ),
            child: TextField(
              key: Key('account-selector-source-${widget.selectorId}'),
              controller: _source,
              expands: true,
              minLines: null,
              maxLines: null,
              keyboardType: TextInputType.multiline,
              textAlignVertical: TextAlignVertical.top,
              style: const TextStyle(
                fontFamily: 'Menlo',
                fontSize: 12,
                height: 1.45,
              ),
              decoration: InputDecoration(
                border: InputBorder.none,
                contentPadding: const EdgeInsets.all(10),
                hintText: 'selection.accountId = accounts[0].id;',
              ),
            ),
          ),
        ),
        const SizedBox(height: 4),
        Row(
          children: [
            Icon(
              Icons.shield_outlined,
              size: 12,
              color: context.viberColors.textFaint,
            ),
            const SizedBox(width: 4),
            Expanded(
              child: Text(
                copy('account_selector.editor.sandbox'),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textFaint,
                ),
              ),
            ),
            Text(
              '${utf8.encode(_source.text).length} B / 65536 B',
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textFaint,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ],
        ),
      ],
    ),
  );

  Widget _sample(BuildContext context) => ExpansionTile(
    key: const Key('account-selector-sample'),
    dense: true,
    initiallyExpanded: false,
    title: Text(copy('account_selector.sample.title')),
    subtitle: Text(copy('account_selector.sample.detail')),
    childrenPadding: const EdgeInsets.fromLTRB(12, 0, 12, 10),
    children: [
      LayoutBuilder(
        builder: (context, constraints) {
          final fields = <Widget>[
            TextField(
              key: const Key('account-selector-sample-accounts'),
              controller: _accounts,
              decoration: InputDecoration(
                labelText: copy('account_selector.sample.accounts'),
              ),
            ),
            TextField(
              key: const Key('account-selector-sample-user'),
              controller: _user,
              decoration: InputDecoration(
                labelText: copy('account_selector.sample.user'),
              ),
            ),
            TextField(
              key: const Key('account-selector-sample-workspace'),
              controller: _workspace,
              decoration: InputDecoration(
                labelText: copy('account_selector.sample.workspace'),
              ),
            ),
            TextField(
              key: const Key('account-selector-sample-model'),
              controller: _model,
              decoration: InputDecoration(
                labelText: copy('account_selector.sample.model'),
              ),
            ),
          ];
          if (constraints.maxWidth < 620) {
            return Column(
              children: [
                for (final field in fields) ...[
                  field,
                  const SizedBox(height: 7),
                ],
                _protocolField(),
              ],
            );
          }
          return Column(
            children: [
              Row(
                children: [
                  Expanded(child: fields[0]),
                  const SizedBox(width: 8),
                  Expanded(child: fields[1]),
                  const SizedBox(width: 8),
                  Expanded(child: fields[2]),
                ],
              ),
              const SizedBox(height: 8),
              Row(
                children: [
                  Expanded(child: fields[3]),
                  const SizedBox(width: 8),
                  Expanded(child: _protocolField()),
                ],
              ),
            ],
          );
        },
      ),
    ],
  );

  Widget _protocolField() => DropdownButtonFormField<String>(
    key: const Key('account-selector-sample-protocol'),
    initialValue: _protocol,
    isExpanded: true,
    decoration: InputDecoration(
      labelText: copy('account_selector.sample.protocol'),
    ),
    items: const [
      DropdownMenuItem(
        value: 'anthropic_messages',
        child: Text('Anthropic Messages'),
      ),
      DropdownMenuItem(
        value: 'openai_responses',
        child: Text('OpenAI Responses'),
      ),
      DropdownMenuItem(value: 'openai_chat', child: Text('OpenAI Chat')),
    ],
    onChanged: (value) {
      if (value != null) setState(() => _protocol = value);
    },
  );

  Widget _actions(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
    child: LayoutBuilder(
      builder: (context, constraints) {
        final test = OutlinedButton.icon(
          key: Key('account-selector-test-${widget.selectorId}'),
          onPressed: _testing ? null : () => unawaited(_run()),
          icon: _testing
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : const Icon(Icons.play_arrow_rounded, size: 16),
          label: Text(copy('account_selector.test.run')),
        );
        final commit = Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            TextButton(
              onPressed: () => Navigator.of(context).pop(),
              child: Text(copy('common.cancel')),
            ),
            const SizedBox(width: 8),
            FilledButton(
              key: Key('account-selector-save-${widget.selectorId}'),
              onPressed: _save,
              child: Text(copy('common.save')),
            ),
          ],
        );
        if (constraints.maxWidth < 440) {
          return Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              test,
              const SizedBox(height: 6),
              Align(alignment: Alignment.centerRight, child: commit),
            ],
          );
        }
        return Row(
          mainAxisAlignment: MainAxisAlignment.end,
          children: [test, const SizedBox(width: 8), commit],
        );
      },
    ),
  );

  AccountSelectorPolicy _policy() => AccountSelectorPolicy.fromJson({
    'javaScript': _source.text,
  }, 'accountSelector.policy');

  AccountSelectorTestSample _sampleValue() {
    final accountIds = _accounts.text
        .split(',')
        .map((value) => value.trim())
        .where((value) => value.isNotEmpty)
        .toSet()
        .toList(growable: false);
    if (accountIds.isEmpty) throw const FormatException('accounts');
    final user = _user.text.trim();
    final workspace = _workspace.text.trim();
    final model = _model.text.trim();
    if (user.isEmpty || workspace.isEmpty || model.isEmpty) {
      throw const FormatException('sample');
    }
    final path = switch (_protocol) {
      'anthropic_messages' => '/v1/messages',
      'openai_responses' => '/v1/responses',
      _ => '/v1/chat/completions',
    };
    return AccountSelectorTestSample(
      accounts: [
        for (final id in accountIds)
          AccountSelectorTestAccount(id: id, displayName: id),
      ],
      request: AccountSelectorTestRequest(
        method: 'POST',
        path: path,
        headers: _protocol == 'anthropic_messages'
            ? const {
                'anthropic-beta': ['sample-beta'],
              }
            : const {},
        body: jsonEncode({'model': model}),
        clientProtocol: _protocol,
        requestedModel: model,
      ),
      runtime: AccountSelectorTestRuntime(
        userName: user,
        homeDirectory: '/Users/$user',
        operatingSystem: 'macos',
        operatingSystemVersion: 'sample',
        architecture: 'arm64',
        timeZone: 'Asia/Singapore',
        workspaceRoot: '/workspace/$workspace',
        workspaceLabel: workspace,
        turnStartedAt: DateTime.utc(2026, 8, 28, 12),
      ),
    );
  }

  Future<void> _run() async {
    setState(() {
      _testing = true;
      _result = null;
      _error = null;
    });
    try {
      final result = await widget.testSelector(
        policy: _policy(),
        sample: _sampleValue(),
      );
      if (mounted) setState(() => _result = result);
    } on Object catch (error) {
      if (mounted) setState(() => _error = error.toString());
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }

  void _save() {
    try {
      Navigator.of(context).pop(_policy());
    } on Object catch (error) {
      setState(() => _error = error.toString());
    }
  }
}

const _suggestions = <String>[
  'selection.accountId',
  'accounts[0].id',
  'accounts[0].displayName',
  'request.body',
  'request.headers["anthropic-beta"]',
  'request.requestedModel',
  'request.protocol',
  'runtime.user.name',
  'runtime.workspace.label',
  'runtime.workspace.root',
  'runtime.device.operatingSystem',
  'runtime.device.timeZone',
  'runtime.turn.startedAt',
];

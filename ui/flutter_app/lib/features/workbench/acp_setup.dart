import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

final class ACPSetupGuide extends StatefulWidget {
  const ACPSetupGuide({
    required this.copy,
    required this.program,
    required this.serverURL,
    super.key,
  });
  final AppCopy copy;
  final String program, serverURL;
  @override
  State<ACPSetupGuide> createState() => _ACPSetupGuideState();
}

final class _ACPSetupGuideState extends State<ACPSetupGuide> {
  late final _program = TextEditingController(text: widget.program);
  late final _server = TextEditingController(text: widget.serverURL);
  final _agent = TextEditingController(text: 'codex-acp');
  String _editor = 'Zed', _kind = 'Codex';
  bool _content = false;

  @override
  void dispose() {
    _program.dispose();
    _server.dispose();
    _agent.dispose();
    super.dispose();
  }

  String get _configuration {
    final agents = {
      'vibermate-${_kind.toLowerCase().replaceAll(' ', '-')}': {
        if (_editor == 'Zed') 'type': 'custom',
        'command': _program.text.trim(),
        'args': [
          'acp',
          if (_server.text.trim().isNotEmpty) ...[
            '--server',
            _server.text.trim(),
          ],
          if (_content) '--record-content',
          '--',
          _agent.text.trim(),
          if (_kind == 'Cursor CLI') 'acp',
        ],
        'env': <String, String>{},
      },
    };
    return const JsonEncoder.withIndent('  ').convert(
      _editor == 'VS Code (ACP Client)'
          ? {'acp.agents': agents}
          : {'agent_servers': agents},
    );
  }

  /// The npm package of the adapter for the chosen agent, at the version
  /// docs/acp-quickstart.md pins; Cursor CLI speaks ACP itself.
  String? get _installCommand => switch (_kind) {
    'Codex' => 'npm install -g @agentclientprotocol/codex-acp@1.13.1',
    'Claude' => 'npm install -g @agentclientprotocol/claude-agent-acp@0.81.2',
    _ => null,
  };

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final muted = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted);
    Widget stepTitle(int number, String key) => Padding(
      padding: const EdgeInsets.only(top: 14, bottom: 6),
      child: Text(
        copy.format('terminal.run.step', {
          'number': number,
          'title': copy(key),
        }),
        style: Theme.of(context).textTheme.labelLarge,
      ),
    );
    final install = _installCommand;
    return Column(
      key: const Key('acp-setup-guide'),
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        // What matters most comes first: editor sessions are only watched.
        InlineNotice(
          key: const Key('acp-setup-limitation'),
          message: copy('acp.setup.limitation'),
        ),
        const SizedBox(height: 8),
        Text(copy('acp.setup.detail'), style: muted),
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton.icon(
            key: const Key('acp-setup-guide-link'),
            onPressed: () => unawaited(
              launchUrl(
                Uri.parse(copy('acp.setup.guide_url')),
                mode: LaunchMode.externalApplication,
              ),
            ),
            icon: const Icon(Icons.open_in_new, size: 14),
            label: Text(copy('acp.setup.guide')),
          ),
        ),
        stepTitle(1, 'acp.setup.step.install'),
        Align(
          alignment: Alignment.centerLeft,
          child: SizedBox(
            width: 220,
            child: CompactLabeledControl(
              label: copy('acp.setup.agent'),
              child: CompactSelectField<String>(
                key: const Key('acp-agent-kind'),
                initialValue: _kind,
                isExpanded: true,
                items: [
                  for (final name in ['Codex', 'Claude', 'Cursor CLI'])
                    DropdownMenuItem(value: name, child: Text(name)),
                ],
                onChanged: (value) => setState(() {
                  _kind = value!;
                  _agent.text = switch (_kind) {
                    'Claude' => 'claude-agent-acp',
                    'Cursor CLI' => 'agent',
                    _ => 'codex-acp',
                  };
                }),
              ),
            ),
          ),
        ),
        const SizedBox(height: 8),
        if (install != null)
          CommandBlock(
            key: const Key('acp-install-command'),
            text: install,
            copyKey: const Key('acp-copy-install'),
            copyTooltip: copy('acp.setup.install.copy'),
            copiedLabel: copy('common.copied'),
            copyFailedLabel: copy('common.copy_failed'),
          ),
        if (_kind != 'Codex') ...[
          if (install != null) const SizedBox(height: 4),
          Text(
            copy(
              _kind == 'Claude'
                  ? 'acp.setup.install.claude_node'
                  : 'acp.setup.install.cursor',
            ),
            style: muted,
          ),
        ],
        stepTitle(2, 'acp.setup.step.choose'),
        Align(
          alignment: Alignment.centerLeft,
          child: SizedBox(
            width: 220,
            child: CompactLabeledControl(
              label: copy('acp.setup.editor'),
              child: CompactSelectField<String>(
                key: const Key('acp-editor'),
                initialValue: _editor,
                isExpanded: true,
                items: [
                  for (final name in [
                    'Zed',
                    'JetBrains',
                    'VS Code (ACP Client)',
                  ])
                    DropdownMenuItem(value: name, child: Text(name)),
                ],
                onChanged: (value) => setState(() => _editor = value!),
              ),
            ),
          ),
        ),
        stepTitle(3, 'acp.setup.step.copy'),
        _field(copy('acp.setup.program'), _program, 'acp-program'),
        const SizedBox(height: 10),
        _field(copy('acp.setup.agent_path'), _agent, 'acp-agent'),
        const SizedBox(height: 10),
        _field(copy('acp.setup.server'), _server, 'acp-server'),
        SwitchListTile.adaptive(
          contentPadding: EdgeInsets.zero,
          title: Text(copy('acp.setup.content')),
          value: _content,
          onChanged: (value) => setState(() => _content = value),
        ),
        Text(copy('acp.setup.paths'), style: muted),
        const SizedBox(height: 10),
        CommandBlock(
          text: _configuration,
          textKey: const Key('acp-editor-json'),
          copyKey: const Key('acp-copy-config'),
          copyTooltip: copy('acp.setup.copy'),
          copiedLabel: copy('common.copied'),
          copyFailedLabel: copy('common.copy_failed'),
          enabled:
              _program.text.trim().isNotEmpty && _agent.text.trim().isNotEmpty,
        ),
        const SizedBox(height: 8),
        Text(
          copy(
            _server.text.trim().isEmpty
                ? 'acp.setup.help.local'
                : 'acp.setup.help.remote',
          ),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    );
  }

  Widget _field(String label, TextEditingController controller, String key) =>
      CompactLabeledControl(
        label: label,
        child: TextField(
          key: Key(key),
          controller: controller,
          decoration: const InputDecoration(isDense: true),
          onChanged: (_) => setState(() {}),
        ),
      );
}

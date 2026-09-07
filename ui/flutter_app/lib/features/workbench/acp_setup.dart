import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/design/viber_theme.dart';
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
  String? _notice;

  @override
  void dispose() {
    _program.dispose();
    _server.dispose();
    _agent.dispose();
    super.dispose();
  }

  String get _configuration => const JsonEncoder.withIndent('  ').convert({
    'agent_servers': {
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
    },
  });

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return Container(
      key: const Key('acp-setup-guide'),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.swap_horiz,
                color: context.viberColors.route,
                size: 18,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  copy('acp.setup.title'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(copy('acp.setup.detail')),
          const SizedBox(height: 12),
          Wrap(
            spacing: 10,
            runSpacing: 10,
            children: [
              SizedBox(
                width: 190,
                child: DropdownButtonFormField<String>(
                  initialValue: _editor,
                  isExpanded: true,
                  decoration: InputDecoration(
                    labelText: copy('acp.setup.editor'),
                  ),
                  items: [
                    for (final name in ['Zed', 'JetBrains'])
                      DropdownMenuItem(value: name, child: Text(name)),
                  ],
                  onChanged: (value) => setState(() => _editor = value!),
                ),
              ),
              SizedBox(
                width: 190,
                child: DropdownButtonFormField<String>(
                  initialValue: _kind,
                  isExpanded: true,
                  decoration: const InputDecoration(labelText: 'Agent'),
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
            ],
          ),
          const SizedBox(height: 12),
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
          Text(
            copy('acp.setup.paths'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 10),
          Container(
            width: double.infinity,
            padding: const EdgeInsets.all(12),
            color: context.viberColors.input,
            child: SelectableText(
              _configuration,
              key: const Key('acp-editor-json'),
              style: Theme.of(
                context,
              ).textTheme.bodySmall?.copyWith(fontFamily: 'monospace'),
            ),
          ),
          const SizedBox(height: 10),
          OutlinedButton.icon(
            key: const Key('acp-copy-config'),
            onPressed:
                _program.text.trim().isEmpty || _agent.text.trim().isEmpty
                ? null
                : () async {
                    try {
                      await Clipboard.setData(
                        ClipboardData(text: _configuration),
                      );
                      if (mounted) setState(() => _notice = 'acp.setup.copied');
                    } on Object {
                      if (mounted) setState(() => _notice = 'acp.setup.failed');
                    }
                  },
            icon: const Icon(Icons.copy, size: 16),
            label: Text(copy('acp.setup.copy')),
          ),
          if (_notice != null) Text(copy(_notice!)),
          const SizedBox(height: 8),
          Text(
            copy('acp.setup.help'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 8),
          Text(
            copy('acp.boundary'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
        ],
      ),
    );
  }

  Widget _field(String label, TextEditingController controller, String key) =>
      Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label),
          const SizedBox(height: 4),
          TextField(
            key: Key(key),
            controller: controller,
            onChanged: (_) => setState(() => _notice = null),
          ),
        ],
      );
}

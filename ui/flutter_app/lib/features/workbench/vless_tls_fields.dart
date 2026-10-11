import 'dart:convert';
import 'dart:typed_data';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Short-lived trust input. The key belongs to one node/dialog context.
final class VlessTLSFields extends StatefulWidget {
  const VlessTLSFields({
    required this.copy,
    required this.savedMode,
    required this.onChanged,
    super.key,
  });
  final AppCopy copy;
  final String? savedMode;
  final void Function(VlessTLSImportOptions? options, bool valid) onChanged;

  @override
  State<VlessTLSFields> createState() => _VlessTLSFieldsState();
}

final class _VlessTLSFieldsState extends State<VlessTLSFields> {
  late String _mode = widget.savedMode ?? 'system';
  final _paste = TextEditingController();
  String? _pem;
  String? _error;
  bool _expanded = false;
  bool _loading = false;
  int _generation = 0;
  AppCopy get copy => widget.copy;

  bool get _valid =>
      !_loading &&
      (_mode == 'system' ||
          (_pem == null && widget.savedMode == 'custom_ca') ||
          (_pem != null &&
              _pem!.trim().isNotEmpty &&
              utf8.encode(_pem!).length <= 64 * 1024));

  void _notify() {
    VlessTLSImportOptions? intent;
    if (_mode == 'system' && widget.savedMode == 'custom_ca') {
      intent = const VlessTLSImportOptions.system();
    } else if (_mode == 'custom_ca' && _pem != null && _valid) {
      intent = VlessTLSImportOptions.customCA(_pem!);
    }
    widget.onChanged(intent, _valid);
  }

  @override
  void dispose() {
    _generation++;
    _pem = null;
    _paste.clear();
    _paste.dispose();
    super.dispose();
  }

  void _select(String mode) {
    setState(() {
      _generation++;
      _loading = false;
      _mode = mode;
      _pem = null;
      _paste.clear();
      _expanded = false;
      _error = null;
    });
    _notify();
  }

  Future<void> _chooseFile() async {
    final generation = ++_generation;
    bool current() => mounted && generation == _generation;
    setState(() {
      _loading = true;
      _error = null;
    });
    _notify();
    final bytes = BytesBuilder(copy: false);
    try {
      final file = await openFile(
        acceptedTypeGroups: const [
          XTypeGroup(
            label: 'PEM CA',
            extensions: ['pem', 'crt', 'cer'],
            uniformTypeIdentifiers: ['public.data'],
          ),
        ],
      );
      if (file == null || !current()) return;
      final length = await file.length();
      if (!current()) return;
      if (length <= 0 || length > 64 * 1024) throw const FormatException();
      await for (final chunk in file.openRead(0, length)) {
        if (!current()) return;
        if (bytes.length + chunk.length > 64 * 1024) {
          throw const FormatException();
        }
        bytes.add(chunk);
      }
      final data = bytes.takeBytes();
      try {
        final pem = utf8.decode(data);
        if (pem.trim().isEmpty) throw const FormatException();
        if (!current()) return;
        setState(() {
          _pem = pem;
          _paste.clear();
          _expanded = false;
        });
      } finally {
        data.fillRange(0, data.length, 0);
      }
    } on Object {
      if (current()) {
        setState(() => _error = copy('environment.egress.tls.invalid'));
      }
    } finally {
      final remaining = bytes.takeBytes();
      remaining.fillRange(0, remaining.length, 0);
      if (current()) {
        setState(() => _loading = false);
        _notify();
      }
    }
  }

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      CompactLabeledControl(
        label: copy('environment.egress.tls.label'),
        child: CompactSelectField<String>(
          key: const Key('vless-tls-mode'),
          initialValue: _mode,
          isExpanded: true,
          items: [
            for (final mode in ['system', 'custom_ca'])
              DropdownMenuItem(
                key: Key('vless-tls-$mode'),
                value: mode,
                child: Text(
                  copy('environment.egress.tls.$mode'),
                  overflow: TextOverflow.ellipsis,
                ),
              ),
          ],
          onChanged: (mode) => _select(mode ?? 'system'),
        ),
      ),
      if (_mode == 'custom_ca') ...[
        const SizedBox(height: 6),
        if (_pem != null)
          Text(copy('environment.egress.tls.selected'))
        else if (widget.savedMode == 'custom_ca')
          Text(copy('environment.egress.tls.saved')),
        const SizedBox(height: 6),
        Wrap(
          spacing: 8,
          runSpacing: 6,
          children: [
            OutlinedButton.icon(
              key: const Key('vless-tls-file'),
              onPressed: _loading ? null : _chooseFile,
              icon: _loading
                  ? const CompactProgressIndicator()
                  : const Icon(Icons.upload_file, size: 15),
              label: Text(
                copy(
                  widget.savedMode == 'custom_ca' || _pem != null
                      ? 'environment.egress.tls.replace'
                      : 'environment.egress.tls.import',
                ),
              ),
            ),
            TextButton(
              key: const Key('vless-tls-paste-toggle'),
              onPressed: () => setState(() => _expanded = !_expanded),
              child: Text(copy('environment.egress.tls.paste')),
            ),
          ],
        ),
        if (_expanded)
          TextFormField(
            key: const Key('vless-tls-pem'),
            controller: _paste,
            minLines: 3,
            maxLines: 5,
            autocorrect: false,
            enableSuggestions: false,
            onChanged: (value) {
              setState(() {
                _generation++;
                _loading = false;
                _pem = value.isEmpty ? null : value;
                _error = _valid ? null : copy('environment.egress.tls.invalid');
              });
              _notify();
            },
          ),
        const SizedBox(height: 6),
        Text(
          copy('environment.egress.tls.local'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
      if (_error != null) InlineNotice(message: _error!, error: true),
    ],
  );
}

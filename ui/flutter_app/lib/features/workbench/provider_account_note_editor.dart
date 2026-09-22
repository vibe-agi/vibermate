import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

Future<void> showProviderAccountNoteEditor(
  BuildContext context, {
  required WorkbenchController controller,
  required ProviderAccount account,
  required AppCopy copy,
}) => showDialog<void>(
  context: context,
  barrierDismissible: false,
  builder: (_) =>
      _NoteEditor(controller: controller, account: account, copy: copy),
);

final class _NoteEditor extends StatefulWidget {
  const _NoteEditor({
    required this.controller,
    required this.account,
    required this.copy,
  });
  final WorkbenchController controller;
  final ProviderAccount account;
  final AppCopy copy;

  @override
  State<_NoteEditor> createState() => _NoteEditorState();
}

final class _NoteEditorState extends State<_NoteEditor> {
  late final _note = TextEditingController(text: widget.account.note);
  bool _saving = false;
  String? _error;

  @override
  void dispose() {
    _note.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return PopScope(
      canPop: !_saving,
      child: AlertDialog(
        key: const Key('account-note-dialog'),
        constraints: const BoxConstraints(maxWidth: 500),
        insetPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 24),
        scrollable: true,
        title: Text(copy('provider_accounts.note.title')),
        content: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              widget.account.displayName,
              style: Theme.of(context).textTheme.titleSmall,
            ),
            const SizedBox(height: 12),
            TextField(
              key: const Key('account-note-input'),
              controller: _note,
              autofocus: true,
              // Keep the editing connection/focus stable across failed saves.
              readOnly: _saving,
              maxLength: maxProviderAccountNoteCharacters,
              textInputAction: TextInputAction.done,
              onChanged: (_) => setState(() => _error = null),
              onSubmitted: (_) => _save(),
              decoration: InputDecoration(
                hintText: copy('provider_accounts.note.hint'),
              ),
            ),
            Text(
              copy('provider_accounts.note.scope'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            if (_error case final message?) ...[
              const SizedBox(height: 10),
              InlineNotice(message: copy(message), error: true),
            ],
          ],
        ),
        actions: [
          TextButton(
            onPressed: _saving ? null : () => Navigator.of(context).pop(),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            key: const Key('account-note-save'),
            onPressed: _saving || _note.text.trim() == widget.account.note
                ? null
                : _save,
            child: _saving
                ? const CompactProgressIndicator()
                : Text(copy('common.save')),
          ),
        ],
      ),
    );
  }

  Future<void> _save() async {
    final note = _note.text.trim();
    if (_saving || note == widget.account.note) return;
    if (!validProviderAccountNote(note)) {
      setState(() => _error = 'provider_accounts.note.invalid');
      return;
    }
    setState(() {
      _saving = true;
      _error = null;
    });
    final updated = await widget.controller.setProviderAccountNote(
      widget.account,
      note,
    );
    if (!mounted) return;
    setState(() => _saving = false);
    if (updated != null) {
      Navigator.of(context).pop();
    } else {
      setState(
        () => _error =
            widget.controller.inventoryError ?? 'provider_accounts.note.failed',
      );
    }
  }
}

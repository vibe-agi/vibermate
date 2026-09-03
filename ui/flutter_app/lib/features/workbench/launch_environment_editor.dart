import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

final class LaunchEnvironmentEditorButton extends StatefulWidget {
  const LaunchEnvironmentEditorButton({
    required this.policy,
    required this.copy,
    required this.enabled,
    required this.onChanged,
    super.key,
  });

  final EnvironmentLaunchPolicy policy;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<EnvironmentLaunchPolicy> onChanged;

  @override
  State<LaunchEnvironmentEditorButton> createState() =>
      _LaunchEnvironmentEditorButtonState();
}

final class _LaunchEnvironmentEditorButtonState
    extends State<LaunchEnvironmentEditorButton> {
  late EnvironmentLaunchPolicy _policy = widget.policy;

  @override
  void didUpdateWidget(covariant LaunchEnvironmentEditorButton oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.policy != widget.policy) _policy = widget.policy;
  }

  @override
  Widget build(BuildContext context) {
    return CompactLabeledControl(
      label: widget.copy('environment.launch.label'),
      detail: widget.copy('environment.launch.detail'),
      child: SizedBox(
        width: double.infinity,
        height: ViberMetrics.controlHeight,
        child: OutlinedButton.icon(
          key: const Key('environment-launch-edit'),
          onPressed: widget.enabled ? () => unawaited(_edit(context)) : null,
          icon: const Icon(Icons.terminal_rounded, size: 14),
          label: Align(
            alignment: Alignment.centerLeft,
            child: Text(
              widget.copy.format('environment.launch.summary', {
                'set': _policy.setEnv.length,
                'delete': _policy.deleteEnv.length,
              }),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ),
          style: OutlinedButton.styleFrom(
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: 9),
          ),
        ),
      ),
    );
  }

  Future<void> _edit(BuildContext context) async {
    final policy = await showDialog<EnvironmentLaunchPolicy>(
      context: context,
      barrierDismissible: true,
      builder: (context) =>
          _LaunchEnvironmentDialog(initial: _policy, copy: widget.copy),
    );
    if (policy == null || !mounted) return;
    setState(() => _policy = policy);
    widget.onChanged(policy);
  }
}

final class _LaunchSetEntry {
  _LaunchSetEntry(this.name, this.value);

  String name;
  String value;
}

final class _LaunchEnvironmentDialog extends StatefulWidget {
  const _LaunchEnvironmentDialog({required this.initial, required this.copy});

  final EnvironmentLaunchPolicy initial;
  final AppCopy copy;

  @override
  State<_LaunchEnvironmentDialog> createState() =>
      _LaunchEnvironmentDialogState();
}

final class _LaunchEnvironmentDialogState
    extends State<_LaunchEnvironmentDialog> {
  final _formKey = GlobalKey<FormState>();
  late final List<_LaunchSetEntry> _set = widget.initial.setEnv.entries
      .map((entry) => _LaunchSetEntry(entry.key, entry.value))
      .toList();
  late final List<String> _delete = [...widget.initial.deleteEnv];
  bool _invalid = false;

  AppCopy get copy => widget.copy;

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: const Key('environment-launch-dialog'),
        width: math.max(280, math.min(680, viewport.width - 48)),
        height: math.max(410, math.min(650, viewport.height - 48)),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 8, 10),
              child: Row(
                children: [
                  Icon(
                    Icons.terminal_rounded,
                    size: 18,
                    color: context.viberColors.route,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      copy('environment.launch.dialog.title'),
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                  ),
                  IconButton(
                    tooltip: copy('common.dismiss'),
                    onPressed: () => Navigator.of(context).pop(),
                    icon: const Icon(Icons.close, size: 18),
                  ),
                ],
              ),
            ),
            Container(
              color: context.viberColors.selection.withValues(alpha: 0.45),
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
              child: Text(
                copy('environment.launch.authority'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
            Expanded(
              child: Form(
                key: _formKey,
                child: SingleChildScrollView(
                  padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      _section(
                        context,
                        title: copy('environment.launch.set'),
                        detail: copy('environment.launch.set_detail'),
                        addKey: const Key('environment-launch-add-set'),
                        onAdd: () =>
                            setState(() => _set.add(_LaunchSetEntry('', ''))),
                      ),
                      if (_set.isEmpty)
                        _empty(context)
                      else
                        for (final (index, entry) in _set.indexed)
                          _setRow(context, index, entry),
                      const SizedBox(height: 14),
                      _section(
                        context,
                        title: copy('environment.launch.delete'),
                        detail: copy('environment.launch.delete_detail'),
                        addKey: const Key('environment-launch-add-delete'),
                        onAdd: () => setState(() => _delete.add('')),
                      ),
                      if (_delete.isEmpty)
                        _empty(context)
                      else
                        for (final (index, name) in _delete.indexed)
                          _deleteRow(context, index, name),
                      if (_invalid) ...[
                        const SizedBox(height: 10),
                        Container(
                          key: const Key('environment-launch-error'),
                          color: context.viberColors.danger.withValues(
                            alpha: 0.10,
                          ),
                          padding: const EdgeInsets.all(8),
                          child: Text(
                            copy('environment.launch.validation'),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(color: context.viberColors.danger),
                          ),
                        ),
                      ],
                    ],
                  ),
                ),
              ),
            ),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
              child: Wrap(
                alignment: WrapAlignment.end,
                spacing: 8,
                runSpacing: 8,
                children: [
                  TextButton(
                    onPressed: () => Navigator.of(context).pop(),
                    child: Text(copy('common.cancel')),
                  ),
                  FilledButton(
                    key: const Key('environment-launch-save'),
                    onPressed: _save,
                    child: Text(copy('common.save')),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _section(
    BuildContext context, {
    required String title,
    required String detail,
    required Key addKey,
    required VoidCallback onAdd,
  }) => Row(
    crossAxisAlignment: CrossAxisAlignment.center,
    children: [
      Expanded(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: Theme.of(context).textTheme.titleSmall),
            Text(
              detail,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
        ),
      ),
      IconButton(
        key: addKey,
        onPressed: onAdd,
        tooltip: copy('common.add'),
        icon: const Icon(Icons.add, size: 16),
      ),
    ],
  );

  Widget _empty(BuildContext context) => Container(
    margin: const EdgeInsets.only(top: 5),
    padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
    color: context.viberColors.panelRaised,
    child: Text(
      copy('environment.launch.none'),
      style: Theme.of(
        context,
      ).textTheme.bodySmall?.copyWith(color: context.viberColors.textFaint),
    ),
  );

  Widget _setRow(BuildContext context, int index, _LaunchSetEntry entry) {
    final name = TextFormField(
      key: Key('environment-launch-set-name-$index'),
      initialValue: entry.name,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(hintText: copy('environment.launch.name')),
      onChanged: (value) => entry.name = value,
      validator: _required,
    );
    final value = TextFormField(
      key: Key('environment-launch-set-value-$index'),
      initialValue: entry.value,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(hintText: copy('environment.launch.value')),
      onChanged: (next) => entry.value = next,
    );
    return Container(
      margin: const EdgeInsets.only(top: 6),
      padding: const EdgeInsets.fromLTRB(7, 6, 2, 6),
      decoration: BoxDecoration(
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: LayoutBuilder(
        builder: (context, constraints) => Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: constraints.maxWidth < 430
                  ? Column(children: [name, const SizedBox(height: 6), value])
                  : Row(
                      children: [
                        Expanded(flex: 2, child: name),
                        const SizedBox(width: 6),
                        Expanded(flex: 3, child: value),
                      ],
                    ),
            ),
            IconButton(
              key: Key('environment-launch-remove-set-$index'),
              tooltip: copy('common.remove'),
              onPressed: () => setState(() => _set.removeAt(index)),
              icon: const Icon(Icons.close, size: 15),
            ),
          ],
        ),
      ),
    );
  }

  Widget _deleteRow(BuildContext context, int index, String name) => Padding(
    padding: const EdgeInsets.only(top: 6),
    child: Row(
      children: [
        Expanded(
          child: TextFormField(
            key: Key('environment-launch-delete-name-$index'),
            initialValue: name,
            autocorrect: false,
            enableSuggestions: false,
            decoration: InputDecoration(
              hintText: copy('environment.launch.name'),
            ),
            onChanged: (value) => _delete[index] = value,
            validator: _required,
          ),
        ),
        IconButton(
          key: Key('environment-launch-remove-delete-$index'),
          tooltip: copy('common.remove'),
          onPressed: () => setState(() => _delete.removeAt(index)),
          icon: const Icon(Icons.close, size: 15),
        ),
      ],
    ),
  );

  String? _required(String? value) => value == null || value.isEmpty
      ? copy('environment.launch.validation')
      : null;

  void _save() {
    if (!_formKey.currentState!.validate()) return;
    try {
      final names = <String>{};
      final setEnv = <String, String>{};
      for (final entry in _set) {
        if (!names.add(entry.name)) {
          throw const ControlContractException('duplicate Environment name');
        }
        setEnv[entry.name] = entry.value;
      }
      final deleteEnv = <String>[];
      for (final name in _delete) {
        if (!names.add(name)) {
          throw const ControlContractException('duplicate Environment name');
        }
        deleteEnv.add(name);
      }
      deleteEnv.sort();
      final policy = EnvironmentLaunchPolicy.fromJson({
        if (setEnv.isNotEmpty) 'setEnv': setEnv,
        if (deleteEnv.isNotEmpty) 'deleteEnv': deleteEnv,
      }, r'$.launchEnvironment');
      Navigator.of(context).pop(policy);
    } on ControlContractException {
      setState(() => _invalid = true);
    }
  }
}

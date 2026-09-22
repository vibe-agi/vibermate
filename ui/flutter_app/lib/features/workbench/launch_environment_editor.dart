import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/api/launch_environment_snapshot.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'launch_snapshot_picker.dart';

final class LaunchEnvironmentEditorButton extends StatefulWidget {
  const LaunchEnvironmentEditorButton({
    required this.policy,
    required this.copy,
    required this.enabled,
    required this.onChanged,
    this.loadSnapshots,
    super.key,
  });

  final EnvironmentLaunchPolicy policy;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<EnvironmentLaunchPolicy> onChanged;
  final Future<List<LaunchEnvironmentSnapshot>> Function()? loadSnapshots;

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
  Widget build(BuildContext context) => CompactLabeledControl(
    label: widget.copy('environment.launch.label'),
    help: widget.copy('environment.launch.detail'),
    dismissHelpLabel: widget.copy('common.dismiss'),
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

  Future<void> _edit(BuildContext context) async {
    final policy = await showDialog<EnvironmentLaunchPolicy>(
      context: context,
      builder: (_) => _LaunchEnvironmentDialog(
        initial: _policy,
        copy: widget.copy,
        loadSnapshots: widget.loadSnapshots,
      ),
    );
    if (policy == null || !mounted) return;
    setState(() => _policy = policy);
    widget.onChanged(policy);
  }
}

final class _LaunchSetEntry {
  _LaunchSetEntry(this.name, this.value);
  String name, value;
  bool reveal = false;
}

final class _LaunchEnvironmentDialog extends StatefulWidget {
  const _LaunchEnvironmentDialog({
    required this.initial,
    required this.copy,
    this.loadSnapshots,
  });
  final EnvironmentLaunchPolicy initial;
  final AppCopy copy;
  final Future<List<LaunchEnvironmentSnapshot>> Function()? loadSnapshots;

  @override
  State<_LaunchEnvironmentDialog> createState() =>
      _LaunchEnvironmentDialogState();
}

final class _LaunchEnvironmentDialogState
    extends State<_LaunchEnvironmentDialog> {
  final _formKey = GlobalKey<FormState>();
  final _manual = TextEditingController();
  late final _set = widget.initial.setEnv.entries
      .map((entry) => _LaunchSetEntry(entry.key, entry.value))
      .toList();
  late final _blocked = widget.initial.deleteEnv.toSet();
  int _tab = 0;
  String? _error;

  String copy(String key) => widget.copy('environment.launch.$key');

  @override
  void dispose() {
    _manual.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: const Key('environment-launch-dialog'),
        width: math.min(900, viewport.width - 48),
        height: math.min(720, viewport.height - 48),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 8, 8),
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
                      copy('dialog.title'),
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                  ),
                  ContextHelpButton(
                    title: copy('dialog.title'),
                    message: copy('security'),
                    dismissLabel: widget.copy('common.dismiss'),
                  ),
                  IconButton(
                    tooltip: widget.copy('common.dismiss'),
                    onPressed: () => Navigator.pop(context),
                    icon: const Icon(Icons.close, size: 18),
                  ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 14),
              child: Row(
                children: [
                  for (final (index, key) in ['block_tab', 'set_tab'].indexed)
                    Expanded(
                      child: TextButton(
                        key: Key('environment-launch-tab-$index'),
                        style: TextButton.styleFrom(
                          backgroundColor: _tab == index
                              ? context.viberColors.selection
                              : null,
                        ),
                        onPressed: () => setState(() {
                          _tab = index;
                          _error = null;
                        }),
                        child: Text(
                          '${copy(key)} · ${index == 0 ? _blocked.length : _set.length}',
                          textAlign: TextAlign.center,
                        ),
                      ),
                    ),
                ],
              ),
            ),
            Expanded(
              child: Form(
                key: _formKey,
                child: SingleChildScrollView(
                  padding: const EdgeInsets.all(14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Offstage(
                        offstage: _tab != 0,
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            LaunchSnapshotPicker(
                              copy: widget.copy,
                              selected: _blocked,
                              loadSnapshots: widget.loadSnapshots,
                              onToggle: _toggle,
                              onSelectSuggested: _selectSuggested,
                            ),
                          ],
                        ),
                      ),
                      Offstage(
                        offstage: _tab != 1,
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            Text(
                              copy('set_help'),
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                            const SizedBox(height: 8),
                            for (final (index, entry) in _set.indexed)
                              _setRow(context, index, entry),
                            Align(
                              alignment: Alignment.centerLeft,
                              child: TextButton.icon(
                                key: const Key('environment-launch-add-set'),
                                onPressed: () {
                                  if (_blocked.length + _set.length >= 128) {
                                    setState(
                                      () => _error = copy('limit_error'),
                                    );
                                    return;
                                  }
                                  setState(
                                    () => _set.add(_LaunchSetEntry('', '')),
                                  );
                                },
                                icon: const Icon(Icons.add, size: 16),
                                label: Text(widget.copy('common.add')),
                              ),
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
            if (_tab == 0)
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 6, 14, 8),
                child: Row(
                  children: [
                    Expanded(
                      child: TextField(
                        key: const Key('environment-launch-delete-name-0'),
                        controller: _manual,
                        autocorrect: false,
                        enableSuggestions: false,
                        decoration: InputDecoration(hintText: copy('manual')),
                        onSubmitted: (_) => _addManual(),
                      ),
                    ),
                    const SizedBox(width: 6),
                    IconButton(
                      key: const Key('environment-launch-add-delete'),
                      tooltip: copy('manual'),
                      onPressed: _addManual,
                      icon: const Icon(Icons.add, size: 18),
                    ),
                  ],
                ),
              ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 4, 14, 8),
                child: Text(
                  _error!,
                  key: const Key('environment-launch-error'),
                  style: TextStyle(color: context.viberColors.danger),
                ),
              ),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    copy('effective'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  const SizedBox(height: 8),
                  Align(
                    alignment: Alignment.centerRight,
                    child: Wrap(
                      alignment: WrapAlignment.end,
                      spacing: 8,
                      runSpacing: 6,
                      crossAxisAlignment: WrapCrossAlignment.center,
                      children: [
                        Text(
                          widget.copy.format(
                            'environment.launch.blocked_count',
                            {'count': _blocked.length},
                          ),
                        ),
                        TextButton(
                          onPressed: () => Navigator.pop(context),
                          child: Text(widget.copy('common.cancel')),
                        ),
                        FilledButton(
                          key: const Key('environment-launch-save'),
                          onPressed: _save,
                          child: Text(copy('save')),
                        ),
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _setRow(BuildContext context, int index, _LaunchSetEntry entry) {
    final name = TextFormField(
      key: Key('environment-launch-set-name-$index'),
      initialValue: entry.name,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(hintText: copy('name')),
      onChanged: (value) => entry.name = value,
      validator: (value) => _nameError(value?.trim() ?? ''),
    );
    final value = TextFormField(
      key: Key('environment-launch-set-value-$index'),
      initialValue: entry.value,
      autocorrect: false,
      enableSuggestions: false,
      obscureText: !entry.reveal,
      decoration: InputDecoration(
        hintText: copy('value'),
        suffixIcon: IconButton(
          tooltip: copy('value'),
          onPressed: () => setState(() => entry.reveal = !entry.reveal),
          icon: Icon(
            entry.reveal
                ? Icons.visibility_off_outlined
                : Icons.visibility_outlined,
            size: 16,
          ),
        ),
      ),
      onChanged: (value) => entry.value = value,
    );
    return Padding(
      key: ObjectKey(entry),
      padding: const EdgeInsets.only(bottom: 10),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: LayoutBuilder(
              builder: (context, constraints) => constraints.maxWidth < 480
                  ? Column(children: [name, const SizedBox(height: 6), value])
                  : Row(
                      children: [
                        Expanded(flex: 2, child: name),
                        const SizedBox(width: 8),
                        Expanded(flex: 3, child: value),
                      ],
                    ),
            ),
          ),
          IconButton(
            key: Key('environment-launch-remove-set-$index'),
            tooltip: widget.copy('common.remove'),
            onPressed: () => setState(() => _set.remove(entry)),
            icon: const Icon(Icons.close, size: 16),
          ),
        ],
      ),
    );
  }

  String? _nameError(String name) {
    if (!EnvironmentLaunchPolicy.validName(name)) return copy('name_error');
    if (EnvironmentLaunchPolicy.managedName(name)) return copy('managed_error');
    return null;
  }

  void _toggle(String name, bool selected) {
    if (!selected) {
      setState(() {
        _blocked.remove(name);
        _error = null;
      });
      return;
    }
    _selectSuggested([name]);
  }

  void _selectSuggested(List<String> names) {
    final additions = names.toSet().difference(_blocked);
    final invalid = additions.map(_nameError).whereType<String>().firstOrNull;
    final conflict = additions.any(
      (name) => _set.any((entry) => entry.name.trim() == name),
    );
    setState(() {
      _error =
          invalid ??
          (conflict
              ? copy('conflict_error')
              : _blocked.length + _set.length + additions.length > 128
              ? copy('limit_error')
              : null);
      if (_error == null) _blocked.addAll(additions);
    });
  }

  void _addManual() {
    final name = _manual.text.trim();
    _toggle(name, true);
    if (_error == null) {
      _manual.clear();
      FocusScope.of(context).unfocus();
    }
  }

  void _save() {
    if (_manual.text.trim().isNotEmpty) {
      _addManual();
      if (_error != null) return;
    }
    if (!_formKey.currentState!.validate()) {
      setState(() {
        _tab = 1;
        _error = copy('validation');
      });
      return;
    }
    final names = {..._blocked};
    final setEnv = <String, String>{};
    for (final entry in _set) {
      final name = entry.name.trim();
      if (!names.add(name)) {
        setState(() => _error = copy('conflict_error'));
        return;
      }
      setEnv[name] = entry.value;
    }
    try {
      final policy = EnvironmentLaunchPolicy.fromJson({
        'setEnv': setEnv,
        'deleteEnv': _blocked.toList()..sort(),
      }, r'$.launchEnvironment');
      Navigator.pop(context, policy);
    } on ControlContractException {
      setState(() => _error = copy('validation'));
    }
  }
}

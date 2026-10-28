import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Edits an Environment's capture scope: destinations its runs keep off
/// ViberMate and open directly from the user's machine (ADR 0025). The
/// Runtime enforces the rule bounds; this editor checks only the shape.
final class CaptureScopeEditorButton extends StatefulWidget {
  const CaptureScopeEditorButton({
    required this.scope,
    required this.copy,
    required this.enabled,
    required this.onChanged,
    super.key,
  });

  final EnvironmentCaptureScope? scope;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<EnvironmentCaptureScope?> onChanged;

  @override
  State<CaptureScopeEditorButton> createState() =>
      _CaptureScopeEditorButtonState();
}

final class _CaptureScopeEditorButtonState
    extends State<CaptureScopeEditorButton> {
  late EnvironmentCaptureScope? _scope = widget.scope;

  @override
  void didUpdateWidget(covariant CaptureScopeEditorButton oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.scope != widget.scope) _scope = widget.scope;
  }

  @override
  Widget build(BuildContext context) {
    final scope = _scope;
    return CompactLabeledControl(
      label: widget.copy('environment.capture_scope.label'),
      help: widget.copy('environment.capture_scope.detail'),
      dismissHelpLabel: widget.copy('common.dismiss'),
      child: SizedBox(
        width: double.infinity,
        height: ViberMetrics.controlHeight,
        child: OutlinedButton.icon(
          key: const Key('environment-capture-scope-edit'),
          onPressed: widget.enabled ? () => unawaited(_edit(context)) : null,
          icon: const Icon(Icons.alt_route_rounded, size: 14),
          label: Align(
            alignment: Alignment.centerLeft,
            child: Text(
              widget.copy.format('environment.capture_scope.summary', {
                'count': scope?.rules.length ?? 0,
                'user': widget.copy(
                  scope?.allowUserAdditions == true
                      ? 'environment.capture_scope.user_on'
                      : 'environment.capture_scope.user_off',
                ),
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
    final result = await showDialog<_CaptureScopeResult>(
      context: context,
      builder: (_) => _CaptureScopeDialog(initial: _scope, copy: widget.copy),
    );
    if (result == null || !mounted) return;
    setState(() => _scope = result.scope);
    widget.onChanged(result.scope);
  }
}

/// Wraps the dialog's answer, so "cleared" (a null scope) differs from
/// "cancelled" (no result).
final class _CaptureScopeResult {
  const _CaptureScopeResult(this.scope);
  final EnvironmentCaptureScope? scope;
}

final class _RuleEntry {
  _RuleEntry({this.id, this.kind = 'suffix', String value = '', int port = 0})
    : value = TextEditingController(text: value),
      port = TextEditingController(text: port == 0 ? '' : '$port');

  /// The stored identifier, kept for existing rules; new ones get one on save.
  final String? id;
  String kind;
  final TextEditingController value;
  final TextEditingController port;

  void dispose() {
    value.dispose();
    port.dispose();
  }
}

final class _CaptureScopeDialog extends StatefulWidget {
  const _CaptureScopeDialog({required this.initial, required this.copy});
  final EnvironmentCaptureScope? initial;
  final AppCopy copy;

  @override
  State<_CaptureScopeDialog> createState() => _CaptureScopeDialogState();
}

final class _CaptureScopeDialogState extends State<_CaptureScopeDialog> {
  static const _maxRules = 256;
  final _formKey = GlobalKey<FormState>();
  late final List<_RuleEntry> _rules = [
    for (final rule in widget.initial?.rules ?? const [])
      _RuleEntry(
        id: rule.id,
        kind: rule.kind,
        value: rule.value,
        port: rule.port,
      ),
  ];
  late bool _allowUserAdditions = widget.initial?.allowUserAdditions ?? false;
  String? _error;

  String copy(String key) => widget.copy('environment.capture_scope.$key');

  @override
  void dispose() {
    for (final rule in _rules) {
      rule.dispose();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: const Key('environment-capture-scope-dialog'),
        width: math.min(760, viewport.width - 48),
        height: math.min(640, viewport.height - 48),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 8, 8),
              child: Row(
                children: [
                  Icon(
                    Icons.alt_route_rounded,
                    size: 18,
                    color: context.viberColors.route,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      copy('label'),
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                  ),
                  ContextHelpButton(
                    title: copy('label'),
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
            Expanded(
              child: Form(
                key: _formKey,
                child: ListView(
                  padding: const EdgeInsets.all(14),
                  children: [
                    Text(
                      copy('detail'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    const SizedBox(height: 10),
                    if (_rules.isEmpty)
                      Padding(
                        padding: const EdgeInsets.symmetric(vertical: 8),
                        child: Text(
                          copy('empty'),
                          key: const Key('environment-capture-scope-empty'),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                      ),
                    for (final (index, rule) in _rules.indexed)
                      _ruleRow(context, index, rule),
                    Align(
                      alignment: Alignment.centerLeft,
                      child: TextButton.icon(
                        key: const Key('environment-capture-scope-add'),
                        onPressed: () {
                          if (_rules.length >= _maxRules) {
                            setState(() => _error = copy('limit_error'));
                            return;
                          }
                          setState(() => _rules.add(_RuleEntry()));
                        },
                        icon: const Icon(Icons.add, size: 16),
                        label: Text(widget.copy('common.add')),
                      ),
                    ),
                    const SizedBox(height: 8),
                    SwitchListTile(
                      key: const Key('environment-capture-scope-user'),
                      contentPadding: EdgeInsets.zero,
                      title: Text(copy('user_additions')),
                      subtitle: Text(copy('user_additions_help')),
                      value: _allowUserAdditions,
                      onChanged: (value) =>
                          setState(() => _allowUserAdditions = value),
                    ),
                  ],
                ),
              ),
            ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 4, 14, 8),
                child: Text(
                  _error!,
                  key: const Key('environment-capture-scope-error'),
                  style: TextStyle(color: context.viberColors.danger),
                ),
              ),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  TextButton(
                    onPressed: () => Navigator.pop(context),
                    child: Text(widget.copy('common.cancel')),
                  ),
                  const SizedBox(width: 8),
                  FilledButton(
                    key: const Key('environment-capture-scope-apply'),
                    onPressed: _apply,
                    child: Text(copy('save')),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _ruleRow(BuildContext context, int index, _RuleEntry rule) {
    final kind = DropdownButtonFormField<String>(
      key: Key('environment-capture-scope-kind-$index'),
      initialValue: rule.kind,
      isExpanded: true,
      items: [
        for (final kind in const ['host', 'suffix', 'prefix'])
          DropdownMenuItem(value: kind, child: Text(copy('kind.$kind'))),
      ],
      onChanged: (value) => setState(() => rule.kind = value!),
    );
    final value = TextFormField(
      key: Key('environment-capture-scope-value-$index'),
      controller: rule.value,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(hintText: copy('hint.${rule.kind}')),
      validator: (value) =>
          (value ?? '').trim().isEmpty ? copy('value_required') : null,
    );
    final port = TextFormField(
      key: Key('environment-capture-scope-port-$index'),
      controller: rule.port,
      keyboardType: TextInputType.number,
      decoration: InputDecoration(hintText: copy('port')),
      validator: (value) {
        final text = (value ?? '').trim();
        if (text.isEmpty) return null;
        final port = int.tryParse(text);
        return port == null || port < 1 || port > 65535
            ? copy('port_invalid')
            : null;
      },
    );
    final remove = IconButton(
      key: Key('environment-capture-scope-remove-$index'),
      tooltip: widget.copy('common.remove'),
      onPressed: () => setState(() => _rules.removeAt(index).dispose()),
      icon: const Icon(Icons.delete_outline, size: 18),
    );
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: LayoutBuilder(
        builder: (context, constraints) => constraints.maxWidth >= 560
            ? Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  SizedBox(width: 190, child: kind),
                  const SizedBox(width: 8),
                  Expanded(child: value),
                  const SizedBox(width: 8),
                  SizedBox(width: 110, child: port),
                  remove,
                ],
              )
            : Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Expanded(child: kind),
                      remove,
                    ],
                  ),
                  const SizedBox(height: 6),
                  value,
                  const SizedBox(height: 6),
                  port,
                ],
              ),
      ),
    );
  }

  void _apply() {
    if (!_formKey.currentState!.validate()) return;
    final used = _rules.map((rule) => rule.id).whereType<String>().toSet();
    var next = 1;
    String freshId() {
      while (used.contains('rule-$next')) {
        next++;
      }
      return used.add('rule-$next') ? 'rule-$next' : freshId();
    }

    final rules = [
      for (final rule in _rules)
        EnvironmentCaptureScopeRule(
          id: rule.id ?? freshId(),
          kind: rule.kind,
          value: rule.value.text.trim().toLowerCase(),
          port: int.tryParse(rule.port.text.trim()) ?? 0,
        ),
    ];
    Navigator.pop(
      context,
      _CaptureScopeResult(
        rules.isEmpty && !_allowUserAdditions
            ? null
            : EnvironmentCaptureScope(
                rules: List.unmodifiable(rules),
                allowUserAdditions: _allowUserAdditions,
              ),
      ),
    );
  }
}

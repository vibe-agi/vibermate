import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

final class AccountSetHeaderDraft {
  AccountSetHeaderDraft({this.name = '', this.value = ''});

  String name;
  String value;
}

/// Ephemeral form state for an Account's complete outbound Header policy.
/// Existing set values are deliberately unavailable and are never inferred.
final class AccountHeaderPolicyDraft {
  AccountHeaderPolicyDraft({
    List<String> existingSetHeaderNames = const [],
    List<String> initialDeleteHeaderNames = const [],
  }) : existingSetHeaderNames = List.unmodifiable(existingSetHeaderNames),
       deleteHeaders = [...initialDeleteHeaderNames];

  final List<String> existingSetHeaderNames;
  final List<AccountSetHeaderDraft> setHeaders = [];
  final List<String> deleteHeaders;

  void addSet({String name = '', String value = ''}) {
    setHeaders.add(AccountSetHeaderDraft(name: name, value: value));
  }

  void clearSensitiveValues() {
    for (final entry in setHeaders) {
      entry.value = '';
    }
  }

  ProviderAccountHeaderPolicy build({required String accountKind}) {
    final assignments = <String, String>{};
    final seen = <String>{};
    for (final entry in setHeaders) {
      if (!seen.add(entry.name.toLowerCase())) {
        throw const ControlContractException(
          'Provider Account Header name is duplicated',
        );
      }
      assignments[entry.name] = entry.value;
    }
    for (final name in deleteHeaders) {
      if (!seen.add(name.toLowerCase())) {
        throw const ControlContractException(
          'Provider Account Header name is duplicated',
        );
      }
    }
    final policy = ProviderAccountHeaderPolicy(
      setHeaders: assignments,
      deleteHeaders: [...deleteHeaders],
    );
    policy.validate(accountKind: accountKind);
    return policy;
  }
}

final class AccountHeaderPolicyEditor extends StatefulWidget {
  const AccountHeaderPolicyEditor({
    required this.accountKind,
    required this.draft,
    required this.copy,
    required this.enabled,
    super.key,
  });

  final String accountKind;
  final AccountHeaderPolicyDraft draft;
  final AppCopy copy;
  final bool enabled;

  @override
  State<AccountHeaderPolicyEditor> createState() =>
      _AccountHeaderPolicyEditorState();
}

final class _AccountHeaderPolicyEditorState
    extends State<AccountHeaderPolicyEditor> {
  bool _revealValues = false;

  AppCopy get copy => widget.copy;

  @override
  Widget build(BuildContext context) {
    final primary = widget.accountKind == 'bearer_token'
        ? 'Authorization: Bearer'
        : 'X-Api-Key';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Icon(
              Icons.shield_outlined,
              size: 14,
              color: context.viberColors.route,
            ),
            const SizedBox(width: 6),
            Expanded(
              child: Text(
                copy.format('routes.account.headers.auth_owned', {
                  'transport': primary,
                }),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          ],
        ),
        if (widget.draft.existingSetHeaderNames.isNotEmpty) ...[
          const SizedBox(height: 8),
          Container(
            padding: const EdgeInsets.all(8),
            decoration: BoxDecoration(
              color: context.viberColors.warning.withValues(alpha: 0.09),
              border: Border(
                left: BorderSide(color: context.viberColors.warning, width: 2),
              ),
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('routes.account.headers.replace_warning'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                const SizedBox(height: 5),
                Wrap(
                  spacing: 5,
                  runSpacing: 4,
                  children: [
                    for (final name in widget.draft.existingSetHeaderNames)
                      Chip(
                        visualDensity: VisualDensity.compact,
                        label: Text(
                          name,
                          style: const TextStyle(fontSize: 10.5),
                        ),
                      ),
                  ],
                ),
              ],
            ),
          ),
        ],
        const SizedBox(height: 10),
        CompactFormSectionHeader(
          title: copy('routes.account.headers.set'),
          detail: copy('routes.account.headers.set_detail'),
          trailing: IconButton(
            key: const Key('account-header-add-set'),
            tooltip: copy('routes.account.headers.add_set'),
            onPressed: widget.enabled
                ? () => setState(widget.draft.addSet)
                : null,
            icon: const Icon(Icons.add, size: 16),
          ),
        ),
        if (widget.draft.setHeaders.isEmpty)
          _empty(context, copy('routes.account.headers.none_set'))
        else
          for (final (index, entry) in widget.draft.setHeaders.indexed)
            _setRow(context, index, entry),
        const SizedBox(height: 9),
        CompactFormSectionHeader(
          title: copy('routes.account.headers.delete'),
          detail: copy('routes.account.headers.delete_detail'),
          trailing: IconButton(
            key: const Key('account-header-add-delete'),
            tooltip: copy('routes.account.headers.add_delete'),
            onPressed: widget.enabled
                ? () => setState(() => widget.draft.deleteHeaders.add(''))
                : null,
            icon: const Icon(Icons.add, size: 16),
          ),
        ),
        if (widget.draft.deleteHeaders.isEmpty)
          _empty(context, copy('routes.account.headers.none_delete'))
        else
          for (final (index, name) in widget.draft.deleteHeaders.indexed)
            _deleteRow(context, index, name),
      ],
    );
  }

  Widget _empty(BuildContext context, String text) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
    color: context.viberColors.panelRaised,
    child: Text(
      text,
      style: Theme.of(
        context,
      ).textTheme.bodySmall?.copyWith(color: context.viberColors.textFaint),
    ),
  );

  Widget _setRow(BuildContext context, int index, AccountSetHeaderDraft entry) {
    final name = TextFormField(
      key: Key('account-header-set-name-$index'),
      initialValue: entry.name,
      enabled: widget.enabled,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(
        hintText: copy('routes.account.headers.name'),
      ),
      onChanged: (value) => entry.name = value,
      validator: (value) => value == null || value.isEmpty
          ? copy('routes.account.headers.validation')
          : null,
    );
    final value = TextFormField(
      key: Key('account-header-set-value-$index'),
      initialValue: entry.value,
      enabled: widget.enabled,
      obscureText: !_revealValues,
      autocorrect: false,
      enableSuggestions: false,
      decoration: InputDecoration(
        hintText: copy('routes.account.headers.value'),
        suffixIcon: IconButton(
          tooltip: copy(
            _revealValues ? 'common.hide_secret' : 'common.show_secret',
          ),
          onPressed: widget.enabled
              ? () => setState(() => _revealValues = !_revealValues)
              : null,
          icon: Icon(
            _revealValues
                ? Icons.visibility_off_outlined
                : Icons.visibility_outlined,
            size: 15,
          ),
        ),
      ),
      onChanged: (next) => entry.value = next,
      validator: (next) =>
          next != null && next.contains(RegExp(r'[\u0000\r\n]'))
          ? copy('routes.account.headers.validation')
          : null,
    );
    return Container(
      margin: const EdgeInsets.only(top: 5),
      padding: const EdgeInsets.fromLTRB(7, 6, 2, 6),
      decoration: BoxDecoration(
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final fields = constraints.maxWidth < 430
              ? Column(children: [name, const SizedBox(height: 6), value])
              : Row(
                  children: [
                    Expanded(flex: 2, child: name),
                    const SizedBox(width: 6),
                    Expanded(flex: 3, child: value),
                  ],
                );
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(child: fields),
              IconButton(
                key: Key('account-header-remove-set-$index'),
                tooltip: copy('common.remove'),
                onPressed: widget.enabled
                    ? () => setState(
                        () => widget.draft.setHeaders.removeAt(index),
                      )
                    : null,
                icon: const Icon(Icons.close, size: 15),
              ),
            ],
          );
        },
      ),
    );
  }

  Widget _deleteRow(BuildContext context, int index, String name) => Padding(
    padding: const EdgeInsets.only(top: 5),
    child: Row(
      children: [
        Expanded(
          child: TextFormField(
            key: Key('account-header-delete-name-$index'),
            initialValue: name,
            enabled: widget.enabled,
            autocorrect: false,
            enableSuggestions: false,
            decoration: InputDecoration(
              hintText: copy('routes.account.headers.name'),
            ),
            onChanged: (value) => widget.draft.deleteHeaders[index] = value,
            validator: (value) => value == null || value.isEmpty
                ? copy('routes.account.headers.validation')
                : null,
          ),
        ),
        IconButton(
          key: Key('account-header-remove-delete-$index'),
          tooltip: copy('common.remove'),
          onPressed: widget.enabled
              ? () => setState(() => widget.draft.deleteHeaders.removeAt(index))
              : null,
          icon: const Icon(Icons.close, size: 15),
        ),
      ],
    ),
  );
}

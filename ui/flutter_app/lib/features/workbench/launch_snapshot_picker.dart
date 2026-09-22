import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/api/launch_environment_snapshot.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

bool suspectedEnvironmentCredential(String name) => RegExp(
  r'(TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE_KEY|CREDENTIAL|DATABASE_URL|DSN|API_KEY|ACCESS_KEY)',
  caseSensitive: false,
).hasMatch(name);

bool essentialEnvironmentVariable(String name) => const {
  'PATH',
  'HOME',
  'USERPROFILE',
  'SYSTEMROOT',
  'WINDIR',
  'TMPDIR',
  'XDG_CONFIG_HOME',
  'APPDATA',
  'PATHEXT',
  'COMSPEC',
}.contains(name.toUpperCase());

/// Display-only, on-demand launcher evidence. This widget has no access to
/// process values and cannot turn changing observations into policy changes.
final class LaunchSnapshotPicker extends StatefulWidget {
  const LaunchSnapshotPicker({
    required this.copy,
    required this.selected,
    required this.onToggle,
    required this.onSelectSuggested,
    this.loadSnapshots,
    super.key,
  });

  final AppCopy copy;
  final Set<String> selected;
  final void Function(String name, bool selected) onToggle;
  final ValueChanged<List<String>> onSelectSuggested;
  final Future<List<LaunchEnvironmentSnapshot>> Function()? loadSnapshots;

  @override
  State<LaunchSnapshotPicker> createState() => _LaunchSnapshotPickerState();
}

final class _LaunchSnapshotPickerState extends State<LaunchSnapshotPicker> {
  List<LaunchEnvironmentSnapshot> _snapshots = [];
  String? _sourceId;
  String _search = '';
  int _filter = 0;
  bool _loading = false;
  bool _failed = false;

  String copy(String key) => widget.copy('environment.launch.$key');

  @override
  void initState() {
    super.initState();
    unawaited(_refresh());
  }

  Future<void> _refresh() async {
    if (_loading || widget.loadSnapshots == null) return;
    setState(() {
      _loading = true;
      _failed = false;
    });
    try {
      final snapshots = await widget.loadSnapshots!();
      if (!mounted) return;
      setState(() {
        _snapshots = snapshots;
        if (!snapshots.any((item) => item.id == _sourceId)) {
          _sourceId = snapshots.firstOrNull?.id;
        }
      });
    } catch (_) {
      if (mounted) setState(() => _failed = true);
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  String _sourceLabel(LaunchEnvironmentSnapshot snapshot) => [
    copy(snapshot.remote ? 'remote' : 'local'),
    if (snapshot.deviceName.isNotEmpty) snapshot.deviceName,
    if (snapshot.userLabel.isNotEmpty) snapshot.userLabel,
    snapshot.executable,
  ].join(' · ');

  @override
  Widget build(BuildContext context) {
    final snapshot = _snapshots
        .where((item) => item.id == _sourceId)
        .firstOrNull;
    final observed = snapshot?.names.toSet() ?? <String>{};
    final all = {...observed, ...widget.selected};
    final editable = all.where(
      (name) => !EnvironmentLaunchPolicy.managedName(name),
    );
    final names =
        editable
            .where(
              (name) =>
                  name.toLowerCase().contains(_search.toLowerCase()) &&
                  (_filter != 1 || suspectedEnvironmentCredential(name)) &&
                  (_filter != 2 || widget.selected.contains(name)),
            )
            .toList()
          ..sort();
    final managed = observed.where(EnvironmentLaunchPolicy.managedName).toList()
      ..sort();
    final suggested = names
        .where(suspectedEnvironmentCredential)
        .where((name) => !widget.selected.contains(name))
        .toList();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Expanded(
              child: CompactLabeledControl(
                label: copy('source'),
                help: copy('source_help'),
                dismissHelpLabel: widget.copy('common.dismiss'),
                child: _snapshots.isEmpty
                    ? Text(copy(_failed ? 'load_failed' : 'empty_snapshot'))
                    : DropdownButtonFormField<String>(
                        key: ValueKey('launch-source-$_sourceId'),
                        initialValue: _sourceId,
                        isExpanded: true,
                        items: [
                          for (final source in _snapshots)
                            DropdownMenuItem(
                              value: source.id,
                              child: Text(
                                _sourceLabel(source),
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                              ),
                            ),
                        ],
                        onChanged: _loading
                            ? null
                            : (id) => setState(() => _sourceId = id),
                      ),
              ),
            ),
            const SizedBox(width: 6),
            IconButton(
              key: const Key('launch-snapshots-refresh'),
              tooltip: copy('refresh'),
              onPressed: _loading || widget.loadSnapshots == null
                  ? null
                  : _refresh,
              icon: _loading
                  ? const SizedBox.square(
                      dimension: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.refresh, size: 18),
            ),
          ],
        ),
        if (snapshot != null)
          Padding(
            padding: const EdgeInsets.only(top: 6),
            child: Text(
              '${snapshot.collectedAt.toLocal().toString().split('.').first} · ${copy('names_only')}',
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ),
        if (_failed && _snapshots.isNotEmpty) Text(copy('load_failed')),
        if (snapshot?.truncated ?? false)
          Text(
            copy('truncated'),
            style: TextStyle(color: context.viberColors.warning),
          ),
        const SizedBox(height: 12),
        TextField(
          key: const Key('launch-snapshots-search'),
          decoration: InputDecoration(
            prefixIcon: const Icon(Icons.search, size: 18),
            hintText: copy('search'),
          ),
          onChanged: (value) => setState(() => _search = value),
        ),
        const SizedBox(height: 8),
        Wrap(
          spacing: 6,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            for (final (index, key) in ['all', 'sensitive', 'selected'].indexed)
              ChoiceChip(
                key: Key('launch-filter-$index'),
                label: Text(copy(key)),
                selected: _filter == index,
                onSelected: (_) => setState(() => _filter = index),
                visualDensity: VisualDensity.compact,
              ),
            TextButton.icon(
              key: const Key('launch-select-sensitive'),
              onPressed: suggested.isEmpty
                  ? null
                  : () => widget.onSelectSuggested(suggested),
              icon: const Icon(Icons.shield_outlined, size: 15),
              label: Text(copy('select_sensitive')),
            ),
          ],
        ),
        const SizedBox(height: 8),
        Container(
          decoration: BoxDecoration(
            border: Border.all(color: context.viberColors.dividerSoft),
            borderRadius: ViberMetrics.controlRadius,
          ),
          child: Column(
            children: [
              if (names.isEmpty)
                Padding(
                  padding: const EdgeInsets.all(14),
                  child: Text(copy('no_matches')),
                ),
              for (final name in names)
                _row(context, name, observed.contains(name)),
            ],
          ),
        ),
        const SizedBox(height: 8),
        Text(copy('inherit'), style: Theme.of(context).textTheme.bodySmall),
        if (widget.selected.any(essentialEnvironmentVariable))
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text(
              copy('essential_warning'),
              style: TextStyle(color: context.viberColors.warning),
            ),
          ),
        if (managed.isNotEmpty)
          Theme(
            data: Theme.of(context).copyWith(dividerColor: Colors.transparent),
            child: ExpansionTile(
              key: const Key('launch-managed-variables'),
              tilePadding: EdgeInsets.zero,
              leading: const Icon(Icons.lock_outline, size: 15),
              title: Text(
                '${copy('managed')} · ${managed.length}',
                style: Theme.of(context).textTheme.bodySmall,
              ),
              children: [
                Align(
                  alignment: Alignment.centerLeft,
                  child: SelectableText(managed.join('  ·  ')),
                ),
              ],
            ),
          ),
      ],
    );
  }

  Widget _row(BuildContext context, String name, bool observed) {
    final selected = widget.selected.contains(name);
    final hint = !observed
        ? 'missing'
        : essentialEnvironmentVariable(name)
        ? 'essential'
        : suspectedEnvironmentCredential(name)
        ? 'sensitive'
        : 'ordinary';
    return Material(
      color: selected
          ? context.viberColors.selection.withValues(alpha: 0.25)
          : Colors.transparent,
      child: InkWell(
        onTap: () => widget.onToggle(name, !selected),
        canRequestFocus: false,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
          child: Row(
            children: [
              SizedBox.square(
                dimension: 32,
                child: Transform.scale(
                  scale: 16 / 18,
                  child: Checkbox(
                    key: Key('launch-block-$name'),
                    semanticLabel: '${copy('block_tab')}: $name',
                    value: selected,
                    onChanged: (value) => widget.onToggle(name, value ?? false),
                    side: BorderSide(
                      color: context.viberColors.textFaint,
                      width: 1.2,
                    ),
                    shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(2),
                    ),
                    materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                  ),
                ),
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      name,
                      style: const TextStyle(fontFamily: 'monospace'),
                      softWrap: true,
                    ),
                    Text(
                      copy(hint),
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: hint == 'sensitive' || hint == 'essential'
                            ? context.viberColors.warning
                            : context.viberColors.textMuted,
                      ),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

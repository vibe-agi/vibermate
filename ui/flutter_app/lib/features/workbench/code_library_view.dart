import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'message_transform_editor.dart';
import 'workbench_controller.dart';

final class CodeLibraryView extends StatefulWidget {
  const CodeLibraryView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<CodeLibraryView> createState() => _CodeLibraryViewState();
}

final class _CodeLibraryViewState extends State<CodeLibraryView> {
  CodeLibraryCatalog? _catalog;
  String? _selectedTransformId;
  String? _error;
  bool _loading = true;
  bool _mutating = false;
  String _testWireProtocol = 'anthropic_messages';

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _testWireProtocol =
        widget.controller.capturedMessageTransformSample?.wireProtocol ??
        _testWireProtocol;
    unawaited(_load());
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final catalog = await widget.controller.codeLibrary();
      if (!mounted) return;
      setState(() {
        _catalog = catalog;
        _loading = false;
        final selectedExists = catalog.transforms.any(
          (item) => item.id == _selectedTransformId,
        );
        if (!selectedExists) {
          _selectedTransformId = catalog.transforms.firstOrNull?.id;
        }
      });
    } on Object catch (error) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = error.toString();
      });
    }
  }

  CodeLibraryTransformRevision? get _selected => _catalog?.transforms
      .where((item) => item.id == _selectedTransformId)
      .firstOrNull;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        PageHeading(
          title: copy('code_library.title'),
          subtitle: copy('code_library.subtitle'),
          trailing: PopupMenuButton<_LibraryAction>(
            key: const Key('code-library-add'),
            tooltip: copy('common.add'),
            enabled: !_mutating,
            onSelected: (action) => switch (action) {
              _LibraryAction.collection => unawaited(_createCollection()),
              _LibraryAction.transform => unawaited(_createTransform()),
            },
            itemBuilder: (context) => [
              PopupMenuItem(
                value: _LibraryAction.collection,
                child: Text(copy('code_library.collection.create')),
              ),
              PopupMenuItem(
                value: _LibraryAction.transform,
                enabled: _catalog?.collections.isNotEmpty == true,
                child: Text(copy('code_library.transform.create')),
              ),
            ],
            icon: const Icon(Icons.add_rounded, size: 18),
          ),
        ),
        const Divider(height: 1),
        if (_error case final error?) InlineNotice(message: error, error: true),
        if (widget.controller.capturedMessageTransformSample
            case final captured?)
          _CapturedSampleBanner(
            captured: captured,
            copy: copy,
            onClear: () =>
                setState(widget.controller.clearMessageTransformSample),
          ),
        Expanded(child: _body(context)),
      ],
    );
  }

  Widget _body(BuildContext context) {
    if (_loading) return const Center(child: CompactProgressIndicator());
    final catalog = _catalog!;
    if (catalog.transforms.isEmpty) {
      return _StarterGallery(
        copy: copy,
        wireProtocol: _testWireProtocol,
        enabled: !_mutating,
        onUse: (starter) =>
            unawaited(_createTransform(initialStarter: starter)),
      );
    }
    return LayoutBuilder(
      builder: (context, constraints) {
        final tree = _LibraryTree(
          catalog: catalog,
          selectedTransformId: _selectedTransformId,
          copy: copy,
          onSelected: (id) => setState(() => _selectedTransformId = id),
        );
        final detail = _LibraryDetail(
          transform: _selected,
          copy: copy,
          enabled: !_mutating,
          onEdit: _selected == null ? null : () => unawaited(_edit(_selected!)),
        );
        if (constraints.maxWidth < 680) {
          return Column(
            children: [
              SizedBox(height: 190, child: tree),
              const Divider(height: 1),
              Expanded(child: detail),
            ],
          );
        }
        return Row(
          children: [
            SizedBox(width: 260, child: tree),
            const VerticalDivider(width: 1),
            Expanded(child: detail),
          ],
        );
      },
    );
  }

  Future<bool> _createCollection() async {
    final name = await showDialog<String>(
      context: context,
      builder: (context) => _NameDialog(
        title: copy('code_library.collection.create'),
        label: copy('code_library.name'),
        copy: copy,
      ),
    );
    if (name == null || !mounted) return false;
    CodeLibraryCollection? created;
    await _mutate(() async {
      created = await widget.controller.createCodeLibraryCollection(
        displayName: name,
      );
      return created;
    });
    return created != null;
  }

  Future<void> _createTransform({
    _TransformStarter initialStarter = _TransformStarter.blank,
  }) async {
    var catalog = _catalog;
    if (catalog == null) return;
    if (catalog.collections.isEmpty) {
      if (!await _createCollection() || !mounted) return;
      catalog = _catalog;
    }
    if (catalog == null || catalog.collections.isEmpty) return;
    final availableCatalog = catalog;
    final draft = await showDialog<_TransformDraft>(
      context: context,
      builder: (context) => _TransformDraftDialog(
        collections: availableCatalog.collections,
        initialWireProtocol:
            widget.controller.capturedMessageTransformSample?.wireProtocol,
        initialStarter: initialStarter,
        copy: copy,
      ),
    );
    if (draft == null || !mounted) return;
    final policy = await showDialog<TrafficTransformPolicy>(
      context: context,
      builder: (context) => MessageTransformEditorDialog(
        planId: 'new-transform',
        wireProtocol: draft.wireProtocol,
        initial: draft.policy,
        initialSample: _capturedSampleFor(draft.wireProtocol),
        copy: copy,
        testTransform: widget.controller.testMessageTransform,
        onTestWireProtocolChanged: _rememberTestWireProtocol,
      ),
    );
    if (policy == null || !mounted) return;
    await _mutate(() async {
      final created = await widget.controller.createCodeLibraryTransform(
        collectionId: draft.collectionId,
        displayName: draft.displayName,
        policy: policy,
      );
      _selectedTransformId = created.id;
      _testWireProtocol = draft.wireProtocol;
      return created;
    });
  }

  Future<void> _edit(CodeLibraryTransformRevision current) async {
    final policy = await showDialog<TrafficTransformPolicy>(
      context: context,
      builder: (context) => MessageTransformEditorDialog(
        planId: current.id,
        wireProtocol: _testWireProtocol,
        initial: current.policy,
        initialSample: _capturedSampleFor(_testWireProtocol),
        copy: copy,
        testTransform: widget.controller.testMessageTransform,
        onTestWireProtocolChanged: _rememberTestWireProtocol,
      ),
    );
    if (policy == null || !mounted) return;
    await _mutate(
      () => widget.controller.publishCodeLibraryTransform(
        id: current.id,
        expectedRevision: current.revision,
        collectionId: current.collectionId,
        displayName: current.displayName,
        policy: policy,
      ),
    );
  }

  MessageTransformTestSample? _capturedSampleFor(String wireProtocol) {
    final captured = widget.controller.capturedMessageTransformSample;
    return captured?.wireProtocol == wireProtocol ? captured?.sample : null;
  }

  void _rememberTestWireProtocol(String value) {
    if (mounted && value != _testWireProtocol) {
      setState(() => _testWireProtocol = value);
    }
  }

  Future<void> _mutate(Future<Object?> Function() action) async {
    setState(() {
      _mutating = true;
      _error = null;
    });
    try {
      await action();
      if (mounted) await _load();
    } on Object catch (error) {
      if (!mounted) return;
      setState(() => _error = error.toString());
    } finally {
      if (mounted) setState(() => _mutating = false);
    }
  }
}

final class _CapturedSampleBanner extends StatelessWidget {
  const _CapturedSampleBanner({
    required this.captured,
    required this.copy,
    required this.onClear,
  });

  final CapturedMessageTransformSample captured;
  final AppCopy copy;
  final VoidCallback onClear;

  @override
  Widget build(BuildContext context) => Container(
    key: const Key('code-library-captured-sample'),
    color: context.viberColors.selection.withValues(alpha: 0.38),
    padding: const EdgeInsets.fromLTRB(14, 8, 6, 8),
    child: Row(
      children: [
        Icon(
          Icons.science_outlined,
          size: 16,
          color: context.viberColors.route,
        ),
        const SizedBox(width: 8),
        Expanded(
          child: Text(
            copy.format('code_library.sample.captured', {
              'exchange': captured.exchangeId,
              'protocol': switch (captured.wireProtocol) {
                'anthropic_messages' => 'Anthropic Messages',
                'openai_responses' => 'OpenAI Responses',
                'openai_chat' => 'OpenAI Chat',
                _ => captured.wireProtocol,
              },
            }),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
        IconButton(
          key: const Key('code-library-captured-sample-clear'),
          tooltip: copy('code_library.sample.clear'),
          onPressed: onClear,
          icon: const Icon(Icons.close, size: 16),
        ),
      ],
    ),
  );
}

enum _LibraryAction { collection, transform }

final class _StarterGallery extends StatelessWidget {
  const _StarterGallery({
    required this.copy,
    required this.wireProtocol,
    required this.enabled,
    required this.onUse,
  });

  final AppCopy copy;
  final String wireProtocol;
  final bool enabled;
  final ValueChanged<_TransformStarter> onUse;

  @override
  Widget build(BuildContext context) => SingleChildScrollView(
    padding: const EdgeInsets.all(16),
    child: Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 1040),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Wrap(
              alignment: WrapAlignment.spaceBetween,
              crossAxisAlignment: WrapCrossAlignment.center,
              spacing: 12,
              runSpacing: 8,
              children: [
                ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 720),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy('code_library.starters.title'),
                        style: Theme.of(context).textTheme.titleLarge,
                      ),
                      const SizedBox(height: 4),
                      Text(
                        copy('code_library.starters.detail'),
                        style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      ),
                    ],
                  ),
                ),
                TextButton.icon(
                  key: const Key('code-library-starter-blank'),
                  onPressed: enabled
                      ? () => onUse(_TransformStarter.blank)
                      : null,
                  icon: const Icon(Icons.add_rounded, size: 16),
                  label: Text(copy('code_library.starters.blank_action')),
                ),
              ],
            ),
            const SizedBox(height: 16),
            LayoutBuilder(
              builder: (context, constraints) {
                final columns = constraints.maxWidth >= 900
                    ? 3
                    : constraints.maxWidth >= 620
                    ? 2
                    : 1;
                final width =
                    (constraints.maxWidth - (columns - 1) * 12) / columns;
                return Wrap(
                  spacing: 12,
                  runSpacing: 12,
                  children: [
                    for (final starter in const [
                      _TransformStarter.localPaths,
                      _TransformStarter.turnTime,
                      _TransformStarter.systemPrompt,
                    ])
                      SizedBox(
                        width: width,
                        child: _StarterCard(
                          starter: starter,
                          wireProtocol: wireProtocol,
                          copy: copy,
                          enabled: enabled,
                          onUse: () => onUse(starter),
                        ),
                      ),
                  ],
                );
              },
            ),
          ],
        ),
      ),
    ),
  );
}

final class _StarterCard extends StatelessWidget {
  const _StarterCard({
    required this.starter,
    required this.wireProtocol,
    required this.copy,
    required this.enabled,
    required this.onUse,
  });

  final _TransformStarter starter;
  final String wireProtocol;
  final AppCopy copy;
  final bool enabled;
  final VoidCallback onUse;

  @override
  Widget build(BuildContext context) {
    final policy = _starterPolicy(starter, wireProtocol);
    final requestStage = policy.requestJavaScript.isNotEmpty;
    final responseStage = policy.responseJavaScript.isNotEmpty;
    final source = requestStage
        ? policy.requestJavaScript
        : policy.responseJavaScript;
    final stage = requestStage && responseStage
        ? '${copy('environment.transform.request')} + ${copy('environment.transform.response')}'
        : copy(
            requestStage
                ? 'environment.transform.request'
                : 'environment.transform.response',
          );
    return Container(
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Icon(
                starter == _TransformStarter.turnTime
                    ? Icons.schedule_outlined
                    : starter == _TransformStarter.localPaths
                    ? Icons.visibility_off_outlined
                    : Icons.tune_rounded,
                size: 18,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  _starterLabel(copy, starter),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ),
              Text(
                stage,
                style: Theme.of(context).textTheme.labelSmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            copy(_starterDetailKey(starter)),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
          const SizedBox(height: 10),
          Container(
            height: 104,
            padding: const EdgeInsets.all(10),
            decoration: BoxDecoration(
              color: context.viberColors.panelRaised,
              borderRadius: BorderRadius.circular(6),
            ),
            child: Text(
              source,
              maxLines: 5,
              overflow: TextOverflow.fade,
              style: monoStyle.copyWith(
                color: context.viberColors.textMuted,
                fontSize: 10.5,
              ),
            ),
          ),
          const SizedBox(height: 10),
          OutlinedButton.icon(
            key: Key('code-library-starter-${starter.name}'),
            onPressed: enabled ? onUse : null,
            icon: const Icon(Icons.content_copy_rounded, size: 15),
            label: Text(copy('code_library.starters.use')),
          ),
        ],
      ),
    );
  }
}

final class _LibraryTree extends StatelessWidget {
  const _LibraryTree({
    required this.catalog,
    required this.selectedTransformId,
    required this.copy,
    required this.onSelected,
  });

  final CodeLibraryCatalog catalog;
  final String? selectedTransformId;
  final AppCopy copy;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    if (catalog.collections.isEmpty) {
      return CenteredMessage(
        icon: Icons.data_object_rounded,
        title: copy('code_library.empty'),
        detail: copy('code_library.empty.detail'),
      );
    }
    return ListView(
      key: const Key('code-library-tree'),
      padding: const EdgeInsets.symmetric(vertical: 6),
      children: [
        for (final collection in catalog.collections) ...[
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 8, 8, 4),
            child: Text(
              collection.displayName,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.labelMedium?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ),
          for (final transform in catalog.transforms.where(
            (item) => item.collectionId == collection.id,
          ))
            ListTile(
              key: Key('code-library-transform-${transform.id}'),
              dense: true,
              selected: transform.id == selectedTransformId,
              leading: const Icon(Icons.javascript_rounded, size: 17),
              title: Text(
                transform.displayName,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
              subtitle: Text('r${transform.revision}'),
              onTap: () => onSelected(transform.id),
            ),
        ],
      ],
    );
  }
}

final class _LibraryDetail extends StatelessWidget {
  const _LibraryDetail({
    required this.transform,
    required this.copy,
    required this.enabled,
    required this.onEdit,
  });

  final CodeLibraryTransformRevision? transform;
  final AppCopy copy;
  final bool enabled;
  final VoidCallback? onEdit;

  @override
  Widget build(BuildContext context) {
    final value = transform;
    if (value == null) {
      return CenteredMessage(
        icon: Icons.code_rounded,
        title: copy('code_library.select'),
        detail: copy('code_library.select.detail'),
      );
    }
    return ListView(
      key: Key('code-library-detail-${value.id}'),
      padding: const EdgeInsets.all(14),
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final identity = Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  value.displayName,
                  style: Theme.of(context).textTheme.titleLarge,
                ),
                Text(
                  '${value.collectionId} · r${value.revision} · ${value.publishedAt.toLocal()}',
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            );
            final edit = constraints.maxWidth < 360
                ? SizedBox.square(
                    dimension: ViberMetrics.controlHeight,
                    child: IconButton(
                      key: const Key('code-library-edit'),
                      tooltip: copy('code_library.edit_publish'),
                      padding: EdgeInsets.zero,
                      onPressed: enabled ? onEdit : null,
                      icon: const Icon(Icons.edit_outlined, size: 17),
                    ),
                  )
                : FilledButton.icon(
                    key: const Key('code-library-edit'),
                    onPressed: enabled ? onEdit : null,
                    icon: const Icon(Icons.edit_outlined, size: 15),
                    label: Text(copy('code_library.edit_publish')),
                    style: FilledButton.styleFrom(
                      minimumSize: const Size(0, ViberMetrics.controlHeight),
                    ),
                  );
            if (constraints.maxWidth < 560) {
              return Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  identity,
                  const SizedBox(height: 10),
                  Wrap(
                    alignment: WrapAlignment.end,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [edit],
                  ),
                ],
              );
            }
            return Row(
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                Expanded(child: identity),
                const SizedBox(width: 8),
                edit,
              ],
            );
          },
        ),
        const SizedBox(height: 14),
        _SourcePanel(
          title: copy('environment.transform.request'),
          source: value.policy.requestJavaScript,
          emptyLabel: copy('code_library.no_changes'),
        ),
        const SizedBox(height: 10),
        _SourcePanel(
          title: copy('environment.transform.response'),
          source: value.policy.responseJavaScript,
          emptyLabel: copy('code_library.no_changes'),
        ),
      ],
    );
  }
}

final class _SourcePanel extends StatelessWidget {
  const _SourcePanel({
    required this.title,
    required this.source,
    required this.emptyLabel,
  });

  final String title;
  final String source;
  final String emptyLabel;

  @override
  Widget build(BuildContext context) => Container(
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.divider),
      borderRadius: ViberMetrics.surfaceRadius,
    ),
    padding: const EdgeInsets.all(12),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title, style: Theme.of(context).textTheme.titleSmall),
        const SizedBox(height: 8),
        SelectableText(
          source.isEmpty ? emptyLabel : source,
          style: monoStyle.copyWith(
            color: source.isEmpty
                ? context.viberColors.textMuted
                : context.viberColors.text,
          ),
        ),
      ],
    ),
  );
}

final class _NameDialog extends StatefulWidget {
  const _NameDialog({
    required this.title,
    required this.label,
    required this.copy,
  });

  final String title;
  final String label;
  final AppCopy copy;

  @override
  State<_NameDialog> createState() => _NameDialogState();
}

final class _NameDialogState extends State<_NameDialog> {
  final _controller = TextEditingController();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.title),
    content: SizedBox(
      width: ViberMetrics.dialogCompactWidth,
      child: TextField(
        key: const Key('code-library-name'),
        controller: _controller,
        autofocus: true,
        decoration: InputDecoration(labelText: widget.label),
        onSubmitted: (_) => _save(),
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.of(context).pop(),
        child: Text(widget.copy('common.cancel')),
      ),
      FilledButton(
        key: const Key('code-library-name-save'),
        onPressed: _save,
        child: Text(widget.copy('common.save')),
      ),
    ],
  );

  void _save() {
    final value = _controller.text.trim();
    if (value.isNotEmpty) Navigator.of(context).pop(value);
  }
}

final class _TransformDraft {
  const _TransformDraft({
    required this.collectionId,
    required this.displayName,
    required this.wireProtocol,
    required this.policy,
  });

  final String collectionId;
  final String displayName;
  final String wireProtocol;
  final TrafficTransformPolicy policy;
}

enum _TransformStarter { blank, localPaths, turnTime, systemPrompt }

final class _TransformDraftDialog extends StatefulWidget {
  const _TransformDraftDialog({
    required this.collections,
    required this.initialWireProtocol,
    this.initialStarter = _TransformStarter.blank,
    required this.copy,
  });

  final List<CodeLibraryCollection> collections;
  final String? initialWireProtocol;
  final _TransformStarter initialStarter;
  final AppCopy copy;

  @override
  State<_TransformDraftDialog> createState() => _TransformDraftDialogState();
}

final class _TransformDraftDialogState extends State<_TransformDraftDialog> {
  late final TextEditingController _name;
  late String _collectionId = widget.collections.first.id;
  late String _wireProtocol =
      widget.initialWireProtocol ?? 'anthropic_messages';
  late _TransformStarter _starter;

  @override
  void initState() {
    super.initState();
    _starter = widget.initialStarter;
    _name = TextEditingController(
      text: _starter == _TransformStarter.blank
          ? ''
          : _starterLabel(widget.copy, _starter),
    );
  }

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.copy('code_library.transform.create')),
    content: SizedBox(
      width: ViberMetrics.dialogCompactWidth,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          TextField(
            key: const Key('code-library-transform-name'),
            controller: _name,
            autofocus: true,
            decoration: InputDecoration(
              labelText: widget.copy('code_library.name'),
            ),
          ),
          const SizedBox(height: 10),
          DropdownButtonFormField<String>(
            initialValue: _collectionId,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: widget.copy('code_library.collection'),
            ),
            items: [
              for (final collection in widget.collections)
                DropdownMenuItem(
                  value: collection.id,
                  child: Text(collection.displayName),
                ),
            ],
            onChanged: (value) {
              if (value != null) setState(() => _collectionId = value);
            },
          ),
          const SizedBox(height: 10),
          DropdownButtonFormField<String>(
            initialValue: _wireProtocol,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: widget.copy('code_library.starter.protocol'),
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
              DropdownMenuItem(
                value: 'openai_chat',
                child: Text('OpenAI Chat'),
              ),
            ],
            onChanged: (value) {
              if (value != null) setState(() => _wireProtocol = value);
            },
          ),
          const SizedBox(height: 10),
          DropdownButtonFormField<_TransformStarter>(
            key: const Key('code-library-transform-starter'),
            initialValue: _starter,
            isExpanded: true,
            decoration: InputDecoration(
              labelText: widget.copy('code_library.starter'),
            ),
            items: [
              for (final starter in _TransformStarter.values)
                DropdownMenuItem(
                  value: starter,
                  child: Text(_starterLabel(widget.copy, starter)),
                ),
            ],
            onChanged: (value) {
              if (value != null) setState(() => _starter = value);
            },
          ),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.of(context).pop(),
        child: Text(widget.copy('common.cancel')),
      ),
      FilledButton(
        key: const Key('code-library-transform-next'),
        onPressed: () {
          final displayName = _name.text.trim();
          if (displayName.isEmpty) return;
          Navigator.of(context).pop(
            _TransformDraft(
              collectionId: _collectionId,
              displayName: displayName,
              wireProtocol: _wireProtocol,
              policy: _starterPolicy(_starter, _wireProtocol),
            ),
          );
        },
        child: Text(widget.copy('common.continue')),
      ),
    ],
  );
}

String _starterLabel(AppCopy copy, _TransformStarter starter) =>
    switch (starter) {
      _TransformStarter.blank => copy('code_library.starter.blank'),
      _TransformStarter.localPaths => copy('code_library.starter.local_paths'),
      _TransformStarter.turnTime => copy('code_library.starter.turn_time'),
      _TransformStarter.systemPrompt => copy(
        'code_library.starter.system_prompt',
      ),
    };

String _starterDetailKey(_TransformStarter starter) => switch (starter) {
  _TransformStarter.blank => 'code_library.empty.detail',
  _TransformStarter.localPaths => 'code_library.starter.local_paths.detail',
  _TransformStarter.turnTime => 'code_library.starter.turn_time.detail',
  _TransformStarter.systemPrompt => 'code_library.starter.system_prompt.detail',
};

TrafficTransformPolicy _starterPolicy(
  _TransformStarter starter,
  String wireProtocol,
) => switch (starter) {
  _TransformStarter.blank => const TrafficTransformPolicy.disabled(),
  _TransformStarter.localPaths => const TrafficTransformPolicy(
    requestJavaScript: r'''const privateHome = runtime.user.homeDirectory;
if (privateHome) {
  context.privateHome = JSON.stringify(privateHome).slice(1, -1);
  context.publicHome = "/Users/guest";
  request.body = request.body.split(context.privateHome).join(context.publicHome);
}''',
    responseJavaScript: r'''if (context.privateHome && context.publicHome) {
  response.body = response.body.split(context.publicHome).join(context.privateHome);
}''',
  ),
  _TransformStarter.turnTime => TrafficTransformPolicy(
    requestJavaScript: '',
    responseJavaScript: _turnTimeStarter(wireProtocol),
  ),
  _TransformStarter.systemPrompt => TrafficTransformPolicy(
    requestJavaScript: _systemPromptStarter(wireProtocol),
    responseJavaScript: '',
  ),
};

String _turnTimeStarter(String wireProtocol) => switch (wireProtocol) {
  'openai_responses' =>
    r'''const payload = JSON.parse(response.body);
if (response.streaming && !context.turnTimeShown && payload.type === "response.output_text.delta" && typeof payload.delta === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  payload.delta = runtime.annotations.create("turn-time", label) + "\n" + payload.delta;
  context.turnTimeShown = true;
} else if (!response.streaming && Array.isArray(payload.output)) {
  for (let outputIndex = 0; outputIndex < payload.output.length; outputIndex += 1) {
    const item = payload.output[outputIndex];
    if (!Array.isArray(item.content)) continue;
    for (let contentIndex = 0; contentIndex < item.content.length; contentIndex += 1) {
      const part = item.content[contentIndex];
      if (part.type === "output_text" && typeof part.text === "string") {
        const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
        part.text = runtime.annotations.create("turn-time", label) + "\n" + part.text;
        outputIndex = payload.output.length;
        break;
      }
    }
  }
}
response.body = JSON.stringify(payload);''',
  'openai_chat' =>
    r'''const payload = JSON.parse(response.body);
const choice = Array.isArray(payload.choices) ? payload.choices[0] : undefined;
if (response.streaming && !context.turnTimeShown && choice && choice.delta && typeof choice.delta.content === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  choice.delta.content = runtime.annotations.create("turn-time", label) + "\n" + choice.delta.content;
  context.turnTimeShown = true;
} else if (!response.streaming && choice && choice.message && typeof choice.message.content === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  choice.message.content = runtime.annotations.create("turn-time", label) + "\n" + choice.message.content;
}
response.body = JSON.stringify(payload);''',
  _ =>
    r'''const payload = JSON.parse(response.body);
if (response.streaming && !context.turnTimeShown && payload.type === "content_block_delta" && payload.delta && payload.delta.type === "text_delta") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  payload.delta.text = runtime.annotations.create("turn-time", label) + "\n" + payload.delta.text;
  context.turnTimeShown = true;
} else if (!response.streaming && Array.isArray(payload.content)) {
  payload.content.unshift({
    type: "text",
    text: runtime.annotations.create("turn-time", runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "")),
  });
}
response.body = JSON.stringify(payload);''',
};

String _systemPromptStarter(String wireProtocol) => switch (wireProtocol) {
  'openai_responses' =>
    r'''const payload = JSON.parse(request.body);
const guidance = "Be concise. State assumptions and surface uncertainty.";
payload.instructions = typeof payload.instructions === "string"
  ? payload.instructions + "\n\n" + guidance
  : guidance;
request.body = JSON.stringify(payload);''',
  'openai_chat' =>
    r'''const payload = JSON.parse(request.body);
const guidance = "Be concise. State assumptions and surface uncertainty.";
let message;
if (Array.isArray(payload.messages)) {
  for (let index = 0; index < payload.messages.length; index += 1) {
    const candidate = payload.messages[index];
    if (candidate.role === "developer" || candidate.role === "system") {
      message = candidate;
      break;
    }
  }
}
if (message && typeof message.content === "string") {
  message.content += "\n\n" + guidance;
} else {
  if (!Array.isArray(payload.messages)) payload.messages = [];
  payload.messages.unshift({role: "developer", content: guidance});
}
request.body = JSON.stringify(payload);''',
  _ =>
    r'''const payload = JSON.parse(request.body);
const guidance = "Be concise. State assumptions and surface uncertainty.";
if (typeof payload.system === "string") {
  payload.system += "\n\n" + guidance;
} else if (Array.isArray(payload.system)) {
  payload.system.push({type: "text", text: guidance});
} else {
  payload.system = guidance;
}
request.body = JSON.stringify(payload);''',
};

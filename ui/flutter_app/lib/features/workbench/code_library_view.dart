import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'account_selector_editor.dart';
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
  String? _selectedSelectorId;
  String? _error;
  bool _loading = true;
  bool _mutating = false;
  String _testWireProtocol = 'anthropic_messages';
  _LibraryKind _kind = _LibraryKind.transforms;

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
      final catalog = await widget.controller.codeLibrary(refresh: true);
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
        final selectedSelectorExists = catalog.accountSelectors.any(
          (item) => item.id == _selectedSelectorId,
        );
        if (!selectedSelectorExists) {
          _selectedSelectorId = catalog.accountSelectors.firstOrNull?.id;
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

  CodeLibraryAccountSelectorRevision? get _selectedSelector => _catalog
      ?.accountSelectors
      .where((item) => item.id == _selectedSelectorId)
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
              _LibraryAction.transform => unawaited(_createTransform()),
              _LibraryAction.accountSelector => unawaited(
                _createAccountSelector(),
              ),
            },
            itemBuilder: (context) => [
              PopupMenuItem(
                value: _LibraryAction.transform,
                child: Text(copy('code_library.transform.create')),
              ),
              PopupMenuItem(
                value: _LibraryAction.accountSelector,
                child: Text(copy('code_library.selector.create')),
              ),
            ],
            icon: const Icon(Icons.add_rounded, size: 18),
          ),
        ),
        const Divider(height: 1),
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 8),
          child: Align(
            alignment: Alignment.centerLeft,
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 420),
              child: CompactSegmentedControl<_LibraryKind>(
                expanded: true,
                segments: [
                  CompactSegment(
                    value: _LibraryKind.transforms,
                    label: copy('code_library.kind.transforms'),
                    icon: Icons.transform_rounded,
                  ),
                  CompactSegment(
                    value: _LibraryKind.accountSelectors,
                    label: copy('code_library.kind.account_selectors'),
                    icon: Icons.account_tree_outlined,
                  ),
                ],
                selected: _kind,
                onSelected: (value) => setState(() => _kind = value),
              ),
            ),
          ),
        ),
        const Divider(height: 1),
        if (_error case final error?) InlineNotice(message: error, error: true),
        if (_kind == _LibraryKind.transforms)
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
    if (_kind == _LibraryKind.accountSelectors) {
      return _accountSelectorBody(catalog);
    }
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
          kind: _LibraryKind.transforms,
          selectedId: _selectedTransformId,
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

  Widget _accountSelectorBody(CodeLibraryCatalog catalog) {
    if (catalog.accountSelectors.isEmpty) {
      return _AccountSelectorStarterGallery(
        copy: copy,
        enabled: !_mutating,
        onUse: (starter) =>
            unawaited(_createAccountSelector(initialStarter: starter)),
      );
    }
    return LayoutBuilder(
      builder: (context, constraints) {
        final tree = _LibraryTree(
          catalog: catalog,
          kind: _LibraryKind.accountSelectors,
          selectedId: _selectedSelectorId,
          copy: copy,
          onSelected: (id) => setState(() => _selectedSelectorId = id),
        );
        final detail = _AccountSelectorDetail(
          selector: _selectedSelector,
          copy: copy,
          enabled: !_mutating,
          onEdit: _selectedSelector == null
              ? null
              : () => unawaited(_editAccountSelector(_selectedSelector!)),
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

  Future<CodeLibraryCatalog?> _ensureCollection() async {
    final catalog = _catalog;
    if (catalog == null || catalog.collections.isNotEmpty) return catalog;
    await _mutate(
      () => widget.controller.createCodeLibraryCollection(
        displayName: copy('code_library.default_collection'),
      ),
    );
    return _catalog;
  }

  Future<void> _createTransform({
    _TransformStarter initialStarter = _TransformStarter.blank,
  }) async {
    final catalog = await _ensureCollection();
    if (!mounted) return;
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
    final policy = await Navigator.of(context).push<TrafficTransformPolicy>(
      MaterialPageRoute(
        builder: (context) => MessageTransformEditorDialog(
          planId: 'new-transform',
          wireProtocol: draft.wireProtocol,
          initial: draft.policy,
          initialSample: _capturedSampleFor(draft.wireProtocol),
          copy: copy,
          testTransform: widget.controller.testMessageTransform,
          onTestWireProtocolChanged: _rememberTestWireProtocol,
        ),
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
    final policy = await Navigator.of(context).push<TrafficTransformPolicy>(
      MaterialPageRoute(
        builder: (context) => MessageTransformEditorDialog(
          planId: current.id,
          wireProtocol: _testWireProtocol,
          initial: current.policy,
          initialSample: _capturedSampleFor(_testWireProtocol),
          copy: copy,
          testTransform: widget.controller.testMessageTransform,
          onTestWireProtocolChanged: _rememberTestWireProtocol,
        ),
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

  Future<void> _createAccountSelector({
    _AccountSelectorStarter initialStarter =
        _AccountSelectorStarter.firstAvailable,
  }) async {
    final catalog = await _ensureCollection();
    if (!mounted) return;
    if (catalog == null || catalog.collections.isEmpty) return;
    final draft = await showDialog<_AccountSelectorDraft>(
      context: context,
      builder: (context) => _AccountSelectorDraftDialog(
        collections: catalog.collections,
        initialStarter: initialStarter,
        copy: copy,
      ),
    );
    if (draft == null || !mounted) return;
    final policy = await Navigator.of(context).push<AccountSelectorPolicy>(
      MaterialPageRoute(
        builder: (context) => AccountSelectorEditorDialog(
          selectorId: 'new-selector',
          initial: _accountSelectorStarterPolicy(initialStarter),
          copy: copy,
          testSelector: widget.controller.testAccountSelector,
        ),
      ),
    );
    if (policy == null || !mounted) return;
    await _mutate(() async {
      final created = await widget.controller.createCodeLibraryAccountSelector(
        collectionId: draft.collectionId,
        displayName: draft.displayName,
        policy: policy,
      );
      _kind = _LibraryKind.accountSelectors;
      _selectedSelectorId = created.id;
      return created;
    });
  }

  Future<void> _editAccountSelector(
    CodeLibraryAccountSelectorRevision current,
  ) async {
    final policy = await Navigator.of(context).push<AccountSelectorPolicy>(
      MaterialPageRoute(
        builder: (context) => AccountSelectorEditorDialog(
          selectorId: current.id,
          initial: current.policy,
          copy: copy,
          testSelector: widget.controller.testAccountSelector,
        ),
      ),
    );
    if (policy == null || !mounted) return;
    await _mutate(
      () => widget.controller.publishCodeLibraryAccountSelector(
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

enum _LibraryKind { transforms, accountSelectors }

enum _LibraryAction { transform, accountSelector }

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
    return _ExampleCard(
      icon: starter == _TransformStarter.turnTime
          ? Icons.schedule_outlined
          : starter == _TransformStarter.localPaths
          ? Icons.visibility_off_outlined
          : Icons.tune_rounded,
      label: _starterLabel(copy, starter),
      badge: stage,
      detail: copy(_starterDetailKey(starter)),
      source: source,
      actionKey: Key('code-library-starter-${starter.name}'),
      actionLabel: copy('code_library.starters.use'),
      enabled: enabled,
      onUse: onUse,
    );
  }
}

final class _AccountSelectorStarterGallery extends StatelessWidget {
  const _AccountSelectorStarterGallery({
    required this.copy,
    required this.enabled,
    required this.onUse,
  });

  final AppCopy copy;
  final bool enabled;
  final ValueChanged<_AccountSelectorStarter> onUse;

  @override
  Widget build(BuildContext context) => SingleChildScrollView(
    padding: const EdgeInsets.all(16),
    child: Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 1040),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              copy('code_library.selector.starters.title'),
              style: Theme.of(context).textTheme.titleLarge,
            ),
            const SizedBox(height: 4),
            Text(
              copy('code_library.selector.starters.detail'),
              style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                color: context.viberColors.textMuted,
              ),
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
                    for (final starter in _AccountSelectorStarter.values)
                      SizedBox(
                        width: width,
                        child: _ExampleCard(
                          icon: switch (starter) {
                            _AccountSelectorStarter.firstAvailable =>
                              Icons.looks_one_outlined,
                            _AccountSelectorStarter.workspace =>
                              Icons.workspaces_outline,
                            _AccountSelectorStarter.user =>
                              Icons.person_outline,
                            _AccountSelectorStarter.model =>
                              Icons.model_training_outlined,
                          },
                          label: _accountSelectorStarterLabel(copy, starter),
                          badge: 'JavaScript',
                          detail: copy(
                            'code_library.selector.starter.${starter.name}.detail',
                          ),
                          source: _accountSelectorStarterPolicy(
                            starter,
                          ).javaScript,
                          actionKey: Key(
                            'code-library-selector-starter-${starter.name}',
                          ),
                          actionLabel: copy('code_library.starters.use'),
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

final class _ExampleCard extends StatelessWidget {
  const _ExampleCard({
    required this.icon,
    required this.label,
    required this.badge,
    required this.detail,
    required this.source,
    required this.actionKey,
    required this.actionLabel,
    required this.enabled,
    required this.onUse,
  });

  final IconData icon;
  final String label;
  final String badge;
  final String detail;
  final String source;
  final Key actionKey;
  final String actionLabel;
  final bool enabled;
  final VoidCallback onUse;

  @override
  Widget build(BuildContext context) => Container(
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
            Icon(icon, size: 18, color: context.viberColors.route),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                label,
                style: Theme.of(context).textTheme.titleMedium,
              ),
            ),
            Text(
              badge,
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ],
        ),
        const SizedBox(height: 8),
        Text(
          detail,
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
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
          key: actionKey,
          onPressed: enabled ? onUse : null,
          icon: const Icon(Icons.content_copy_rounded, size: 15),
          label: Text(actionLabel),
        ),
      ],
    ),
  );
}

final class _LibraryTree extends StatelessWidget {
  const _LibraryTree({
    required this.catalog,
    required this.kind,
    required this.selectedId,
    required this.copy,
    required this.onSelected,
  });

  final CodeLibraryCatalog catalog;
  final _LibraryKind kind;
  final String? selectedId;
  final AppCopy copy;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    final visibleCollections = catalog.collections
        .where((collection) {
          return kind == _LibraryKind.transforms
              ? catalog.transforms.any(
                  (item) => item.collectionId == collection.id,
                )
              : catalog.accountSelectors.any(
                  (item) => item.collectionId == collection.id,
                );
        })
        .toList(growable: false);
    if (visibleCollections.isEmpty) {
      return CenteredMessage(
        icon: Icons.data_object_rounded,
        title: copy('code_library.empty'),
        detail: copy('code_library.empty.detail'),
      );
    }
    final showCollectionNames = visibleCollections.length > 1;
    return ListView(
      key: Key('code-library-tree-${kind.name}'),
      padding: const EdgeInsets.symmetric(vertical: 6),
      children: [
        for (final collection in visibleCollections) ...[
          if (showCollectionNames)
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
          if (kind == _LibraryKind.transforms)
            for (final transform in catalog.transforms.where(
              (item) => item.collectionId == collection.id,
            ))
              ListTile(
                key: Key('code-library-transform-${transform.id}'),
                dense: true,
                selected: transform.id == selectedId,
                leading: const Icon(Icons.javascript_rounded, size: 17),
                title: Text(
                  transform.displayName,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
                subtitle: Text('r${transform.revision}'),
                onTap: () => onSelected(transform.id),
              )
          else
            for (final selector in catalog.accountSelectors.where(
              (item) => item.collectionId == collection.id,
            ))
              ListTile(
                key: Key('code-library-selector-${selector.id}'),
                dense: true,
                selected: selector.id == selectedId,
                leading: const Icon(Icons.account_tree_outlined, size: 17),
                title: Text(
                  selector.displayName,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
                subtitle: Text('r${selector.revision}'),
                onTap: () => onSelected(selector.id),
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
                  'r${value.revision} · ${value.publishedAt.toLocal()}',
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

final class _AccountSelectorDetail extends StatelessWidget {
  const _AccountSelectorDetail({
    required this.selector,
    required this.copy,
    required this.enabled,
    required this.onEdit,
  });

  final CodeLibraryAccountSelectorRevision? selector;
  final AppCopy copy;
  final bool enabled;
  final VoidCallback? onEdit;

  @override
  Widget build(BuildContext context) {
    final value = selector;
    if (value == null) {
      return CenteredMessage(
        icon: Icons.account_tree_outlined,
        title: copy('code_library.selector.select'),
        detail: copy('code_library.selector.select.detail'),
      );
    }
    return ListView(
      key: Key('code-library-selector-detail-${value.id}'),
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
                  'r${value.revision} · ${value.publishedAt.toLocal()}',
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            );
            final edit = FilledButton.icon(
              key: const Key('code-library-selector-edit'),
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
                  Align(alignment: Alignment.centerRight, child: edit),
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
          title: copy('code_library.selector.source'),
          source: value.policy.javaScript,
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

final class _AccountSelectorDraft {
  const _AccountSelectorDraft({
    required this.collectionId,
    required this.displayName,
  });

  final String collectionId;
  final String displayName;
}

final class _AccountSelectorDraftDialog extends StatefulWidget {
  const _AccountSelectorDraftDialog({
    required this.collections,
    required this.initialStarter,
    required this.copy,
  });

  final List<CodeLibraryCollection> collections;
  final _AccountSelectorStarter initialStarter;
  final AppCopy copy;

  @override
  State<_AccountSelectorDraftDialog> createState() =>
      _AccountSelectorDraftDialogState();
}

final class _AccountSelectorDraftDialogState
    extends State<_AccountSelectorDraftDialog> {
  late final TextEditingController _name;
  late String _collectionId = widget.collections.first.id;

  @override
  void initState() {
    super.initState();
    _name = TextEditingController(
      text: _accountSelectorStarterLabel(widget.copy, widget.initialStarter),
    );
  }

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.copy('code_library.selector.create')),
    content: SizedBox(
      width: ViberMetrics.dialogCompactWidth,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          TextField(
            key: const Key('code-library-selector-name'),
            controller: _name,
            autofocus: true,
            decoration: InputDecoration(
              labelText: widget.copy('code_library.name'),
            ),
            onSubmitted: (_) => _next(),
          ),
          if (widget.collections.length > 1) ...[
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
          ],
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.of(context).pop(),
        child: Text(widget.copy('common.cancel')),
      ),
      FilledButton(
        key: const Key('code-library-selector-next'),
        onPressed: _next,
        child: Text(widget.copy('common.continue')),
      ),
    ],
  );

  void _next() {
    final name = _name.text.trim();
    if (name.isEmpty) return;
    Navigator.of(context).pop(
      _AccountSelectorDraft(collectionId: _collectionId, displayName: name),
    );
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

enum _AccountSelectorStarter { firstAvailable, workspace, user, model }

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
          if (widget.collections.length > 1) ...[
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
          ],
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

String _accountSelectorStarterLabel(
  AppCopy copy,
  _AccountSelectorStarter starter,
) => copy('code_library.selector.starter.${starter.name}');

AccountSelectorPolicy _accountSelectorStarterPolicy(
  _AccountSelectorStarter starter,
) => AccountSelectorPolicy(
  javaScript: switch (starter) {
    _AccountSelectorStarter.firstAvailable =>
      r'''if (accounts.length === 0) {
  throw new Error("No Account is available");
}
selection.accountId = accounts[0].id;''',
    _AccountSelectorStarter.workspace =>
      r'''const accountByWorkspace = {
  "work": "account.work",
  "personal": "account.personal",
};
const accountId = accountByWorkspace[runtime.workspace.label];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this Workspace");
}
selection.accountId = accountId;''',
    _AccountSelectorStarter.user =>
      r'''const accountByUser = {
  "alice": "account.team-a",
  "bob": "account.team-b",
};
const accountId = accountByUser[runtime.user.name];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this Runtime User");
}
selection.accountId = accountId;''',
    _AccountSelectorStarter.model =>
      r'''const accountByModel = {
  "claude-opus-4-1": "account.high-capacity",
  "claude-sonnet-4-5": "account.standard",
};
const accountId = accountByModel[request.requestedModel];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this requested model");
}
selection.accountId = accountId;''',
  },
);

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

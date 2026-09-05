import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

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
  String? _errorKey;
  String? _errorDetail;
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
      _errorKey = null;
      _errorDetail = null;
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
        _errorKey = 'code_library.error.load';
        _errorDetail = error.toString();
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
                key: const Key('code-library-create-transform-menu'),
                value: _LibraryAction.transform,
                child: Text(copy('code_library.transform.create')),
              ),
              PopupMenuItem(
                key: const Key('code-library-create-selector-menu'),
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
        if (_errorKey case final errorKey?)
          _LibraryFailureNotice(
            message: copy(errorKey),
            detail: _errorDetail,
            copy: copy,
            onRetry: () => unawaited(_load()),
          ),
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
    final catalog = _catalog;
    if (catalog == null) return const SizedBox.shrink();
    if (_kind == _LibraryKind.accountSelectors) {
      return _accountSelectorBody(catalog);
    }
    if (catalog.transforms.isEmpty) {
      return _StarterGallery(
        copy: copy,
        wireProtocol: _testWireProtocol,
        enabled: !_mutating,
        onStartBlank: () => unawaited(_createTransform()),
        onUse: (starter) => unawaited(_previewTransformStarter(starter)),
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
        onUse: (starter) => unawaited(_previewAccountSelectorStarter(starter)),
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

  Future<void> _previewTransformStarter(_TransformStarter starter) async {
    final policy = await Navigator.of(context).push<TrafficTransformPolicy>(
      MaterialPageRoute(
        builder: (context) => MessageTransformEditorDialog(
          planId: 'starter-preview-${starter.name}',
          displayName: _starterLabel(copy, starter),
          wireProtocol: _testWireProtocol,
          initial: _starterPolicy(starter, _testWireProtocol),
          initialSample:
              _capturedSampleFor(_testWireProtocol) ??
              _starterTestSample(starter, _testWireProtocol),
          initialSampleExchangeId: _capturedFor(_testWireProtocol)?.exchangeId,
          copy: copy,
          testTransform: widget.controller.testMessageTransform,
          pickCapturedSample: _pickCapturedSample,
          onTestWireProtocolChanged: _rememberTestWireProtocol,
          sampleForWireProtocol: (wireProtocol) =>
              _starterTestSample(starter, wireProtocol),
          primaryActionLabel: copy('code_library.starters.use'),
          headerDetail: copy('code_library.starters.preview.detail'),
        ),
      ),
    );
    if (policy != null && mounted) {
      await _createTransform(
        initialStarter: starter,
        starterLocked: true,
        initialPolicy: policy,
      );
    }
  }

  Future<void> _previewAccountSelectorStarter(
    _AccountSelectorStarter starter,
  ) async {
    final policy = await Navigator.of(context).push<AccountSelectorPolicy>(
      MaterialPageRoute(
        builder: (context) => AccountSelectorEditorDialog(
          selectorId: 'starter-preview-${starter.name}',
          initial: _accountSelectorStarterPolicy(starter),
          copy: copy,
          testSelector: widget.controller.testAccountSelector,
          primaryActionLabel: copy('code_library.starters.use'),
          headerDetail: copy('code_library.starters.preview.detail'),
        ),
      ),
    );
    if (policy != null && mounted) {
      await _createAccountSelector(
        initialStarter: starter,
        initialPolicy: policy,
      );
    }
  }

  Future<void> _createTransform({
    _TransformStarter initialStarter = _TransformStarter.blank,
    bool starterLocked = false,
    TrafficTransformPolicy? initialPolicy,
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
            widget.controller.capturedMessageTransformSample?.wireProtocol ??
            _testWireProtocol,
        initialStarter: initialStarter,
        starterLocked: starterLocked,
        initialPolicy: initialPolicy,
        copy: copy,
      ),
    );
    if (draft == null || !mounted) return;
    final policy = await Navigator.of(context).push<TrafficTransformPolicy>(
      MaterialPageRoute(
        builder: (context) => MessageTransformEditorDialog(
          planId: 'new-transform',
          displayName: draft.displayName,
          wireProtocol: draft.wireProtocol,
          initial: draft.policy,
          initialSample: _capturedSampleFor(draft.wireProtocol),
          initialSampleExchangeId: _capturedFor(draft.wireProtocol)?.exchangeId,
          copy: copy,
          testTransform: widget.controller.testMessageTransform,
          pickCapturedSample: _pickCapturedSample,
          onTestWireProtocolChanged: _rememberTestWireProtocol,
          primaryActionLabel: copy('code_library.create_publish'),
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
          displayName: current.displayName,
          baseRevision: current.revision,
          wireProtocol: _testWireProtocol,
          initial: current.policy,
          initialSample: _capturedSampleFor(_testWireProtocol),
          initialSampleExchangeId: _capturedFor(_testWireProtocol)?.exchangeId,
          copy: copy,
          testTransform: widget.controller.testMessageTransform,
          pickCapturedSample: _pickCapturedSample,
          onTestWireProtocolChanged: _rememberTestWireProtocol,
          primaryActionLabel: copy('code_library.publish_revision'),
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
    _AccountSelectorStarter initialStarter = _AccountSelectorStarter.loginUser,
    AccountSelectorPolicy? initialPolicy,
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
          initial:
              initialPolicy ?? _accountSelectorStarterPolicy(initialStarter),
          copy: copy,
          testSelector: widget.controller.testAccountSelector,
          primaryActionLabel: copy('code_library.create_publish'),
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
          primaryActionLabel: copy('code_library.publish_revision'),
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
    return _capturedFor(wireProtocol)?.sample;
  }

  CapturedMessageTransformSample? _capturedFor(String wireProtocol) {
    final captured = widget.controller.capturedMessageTransformSample;
    return captured?.wireProtocol == wireProtocol ? captured : null;
  }

  Future<CapturedMessageTransformSample?> _pickCapturedSample() =>
      showDialog<CapturedMessageTransformSample>(
        context: context,
        builder: (context) => _CapturedExchangePickerDialog(
          controller: widget.controller,
          copy: copy,
        ),
      );

  void _rememberTestWireProtocol(String value) {
    if (mounted && value != _testWireProtocol) {
      setState(() => _testWireProtocol = value);
    }
  }

  Future<void> _mutate(Future<Object?> Function() action) async {
    setState(() {
      _mutating = true;
      _errorKey = null;
      _errorDetail = null;
    });
    try {
      await action();
      if (mounted) await _load();
    } on Object catch (error) {
      if (!mounted) return;
      setState(() {
        _errorKey = 'code_library.error.change';
        _errorDetail = error.toString();
      });
    } finally {
      if (mounted) setState(() => _mutating = false);
    }
  }
}

final class _CapturedExchangePickerDialog extends StatefulWidget {
  const _CapturedExchangePickerDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_CapturedExchangePickerDialog> createState() =>
      _CapturedExchangePickerDialogState();
}

final class _CapturedExchangePickerDialogState
    extends State<_CapturedExchangePickerDialog> {
  String? _loadingExchangeId;
  String? _error;

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: const Key('code-library-captured-exchange-picker'),
        width: math.max(280, math.min(720, viewport.width - 48)),
        height: math.max(420, math.min(620, viewport.height - 48)),
        child: AnimatedBuilder(
          animation: widget.controller,
          builder: (context, _) => _content(context),
        ),
      ),
    );
  }

  Widget _content(BuildContext context) {
    final copy = widget.copy;
    final controller = widget.controller;
    final captures = [
      ...controller.runningCaptures,
      ...controller.historicalCaptures,
    ];
    final conversations = controller.captureConversations;
    final activities = [...controller.selectedActivities]
      ..sort((left, right) => right.occurredAt.compareTo(left.occurredAt));
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 14, 8, 10),
          child: Row(
            children: [
              Icon(
                Icons.history_rounded,
                size: 19,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      copy('code_library.sample.picker.title'),
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                    Text(
                      copy('code_library.sample.picker.detail'),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                  ],
                ),
              ),
              IconButton(
                tooltip: copy('common.dismiss'),
                onPressed: _loadingExchangeId == null
                    ? () => Navigator.of(context).pop()
                    : null,
                icon: const Icon(Icons.close, size: 18),
              ),
            ],
          ),
        ),
        const Divider(height: 1),
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 10),
          child: LayoutBuilder(
            builder: (context, constraints) {
              final capture = _captureField(captures);
              final conversation = _conversationField(conversations);
              if (constraints.maxWidth < 560) {
                return Column(
                  children: [capture, const SizedBox(height: 10), conversation],
                );
              }
              return Row(
                children: [
                  Expanded(child: capture),
                  const SizedBox(width: 10),
                  Expanded(child: conversation),
                ],
              );
            },
          ),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
          child: Text(
            copy('code_library.sample.picker.calls'),
            style: Theme.of(context).textTheme.titleSmall,
          ),
        ),
        if (_error case final error?)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: Text(
              error,
              key: const Key('code-library-captured-exchange-error'),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.danger,
              ),
            ),
          ),
        Expanded(
          child: controller.captureActivitiesLoading
              ? const Center(child: CompactProgressIndicator())
              : activities.isEmpty
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(24),
                    child: Text(
                      captures.isEmpty
                          ? copy('code_library.sample.picker.empty_captures')
                          : copy('code_library.sample.picker.empty_calls'),
                      textAlign: TextAlign.center,
                      style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                  ),
                )
              : ListView.separated(
                  padding: const EdgeInsets.fromLTRB(12, 0, 12, 12),
                  itemCount: activities.length,
                  separatorBuilder: (_, _) => const SizedBox(height: 6),
                  itemBuilder: (context, index) =>
                      _activityTile(context, activities[index]),
                ),
        ),
      ],
    );
  }

  Widget _captureField(List<CaptureRecord> captures) =>
      DropdownButtonFormField<String>(
        key: const Key('code-library-sample-capture'),
        initialValue: widget.controller.selectedCaptureKey,
        isExpanded: true,
        decoration: InputDecoration(
          labelText: widget.copy('code_library.sample.picker.capture'),
        ),
        items: [
          for (final capture in captures)
            DropdownMenuItem(
              value: capture.key,
              child: Text(
                capture.displayName,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
        ],
        onChanged: _loadingExchangeId == null
            ? (value) {
                if (value != null) {
                  setState(() => _error = null);
                  unawaited(widget.controller.selectCapture(value));
                }
              }
            : null,
      );

  Widget _conversationField(List<ConversationSummary> conversations) =>
      DropdownButtonFormField<String>(
        key: const Key('code-library-sample-conversation'),
        initialValue: widget.controller.selectedCaptureConversationKey,
        isExpanded: true,
        decoration: InputDecoration(
          labelText: widget.copy('code_library.sample.picker.conversation'),
        ),
        items: [
          for (final conversation in conversations)
            DropdownMenuItem(
              value: conversation.key,
              child: Text(
                conversation.conversation.displayName ??
                    widget.copy(
                      'code_library.sample.picker.unnamed_conversation',
                    ),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
        ],
        onChanged: _loadingExchangeId == null
            ? (value) {
                if (value != null) {
                  setState(() => _error = null);
                  unawaited(widget.controller.selectCaptureConversation(value));
                }
              }
            : null,
      );

  Widget _activityTile(BuildContext context, ActivityRecord activity) {
    final loading = _loadingExchangeId == activity.id;
    final preview = activity.requestPreview?.text;
    return Material(
      color: context.viberColors.panelRaised.withValues(alpha: 0.42),
      borderRadius: ViberMetrics.controlRadius,
      child: ListTile(
        key: Key('code-library-real-exchange-${activity.id}'),
        enabled: _loadingExchangeId == null,
        dense: true,
        leading: const Icon(Icons.chat_bubble_outline_rounded, size: 17),
        title: Text(
          preview == null || preview.isEmpty ? activity.title : preview,
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
        ),
        subtitle: Text(
          '${activity.sourceName} · ${activity.occurredAt.toLocal().toIso8601String()}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
        ),
        trailing: loading
            ? const CompactProgressIndicator()
            : const Icon(Icons.chevron_right_rounded, size: 18),
        onTap: _loadingExchangeId == null
            ? () => unawaited(_select(activity))
            : null,
      ),
    );
  }

  Future<void> _select(ActivityRecord activity) async {
    setState(() {
      _loadingExchangeId = activity.id;
      _error = null;
    });
    final captured = await widget.controller.loadMessageTransformSample(
      activity.id,
      activity: activity,
    );
    if (!mounted) return;
    if (captured != null) {
      Navigator.of(context).pop(captured);
      return;
    }
    setState(() {
      _loadingExchangeId = null;
      _error = widget.copy('code_library.sample.picker.unavailable');
    });
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

final class _LibraryFailureNotice extends StatelessWidget {
  const _LibraryFailureNotice({
    required this.message,
    required this.detail,
    required this.copy,
    required this.onRetry,
  });

  final String message;
  final String? detail;
  final AppCopy copy;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      InlineNotice(message: message, error: true),
      Padding(
        padding: const EdgeInsets.symmetric(horizontal: 8),
        child: Row(
          children: [
            if (detail case final value?)
              Expanded(
                child: ExpansionTile(
                  key: const Key('code-library-technical-details'),
                  tilePadding: const EdgeInsets.symmetric(horizontal: 4),
                  childrenPadding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
                  title: Text(
                    copy('common.technical_details'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  children: [
                    Align(
                      alignment: Alignment.centerLeft,
                      child: SelectableText(value, style: monoStyle),
                    ),
                  ],
                ),
              )
            else
              const Spacer(),
            TextButton.icon(
              onPressed: onRetry,
              icon: const Icon(Icons.refresh_rounded, size: 15),
              label: Text(copy('common.retry')),
            ),
          ],
        ),
      ),
    ],
  );
}

final class _StarterGallery extends StatelessWidget {
  const _StarterGallery({
    required this.copy,
    required this.wireProtocol,
    required this.enabled,
    required this.onStartBlank,
    required this.onUse,
  });

  final AppCopy copy;
  final String wireProtocol;
  final bool enabled;
  final VoidCallback onStartBlank;
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
                  onPressed: enabled ? onStartBlank : null,
                  icon: const Icon(Icons.add_rounded, size: 16),
                  label: Text(copy('code_library.starters.blank_action')),
                ),
              ],
            ),
            const SizedBox(height: 16),
            _ExampleGrid(
              children: [
                for (final starter in const [
                  _TransformStarter.localIdentity,
                  _TransformStarter.blockSecrets,
                  _TransformStarter.privateContacts,
                  _TransformStarter.turnTime,
                  _TransformStarter.replyLanguage,
                  _TransformStarter.workspaceRules,
                  _TransformStarter.responseModel,
                ])
                  _StarterCard(
                    starter: starter,
                    wireProtocol: wireProtocol,
                    copy: copy,
                    enabled: enabled,
                    onUse: () => onUse(starter),
                  ),
              ],
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
      icon: switch (starter) {
        _TransformStarter.localIdentity => Icons.visibility_off_outlined,
        _TransformStarter.blockSecrets => Icons.key_off_outlined,
        _TransformStarter.privateContacts => Icons.contact_page_outlined,
        _TransformStarter.turnTime => Icons.schedule_outlined,
        _TransformStarter.replyLanguage => Icons.translate_outlined,
        _TransformStarter.workspaceRules => Icons.workspaces_outline,
        _TransformStarter.responseModel => Icons.model_training_outlined,
        _TransformStarter.blank => Icons.add_rounded,
      },
      label: _starterLabel(copy, starter),
      badge: stage,
      detail: copy(_starterDetailKey(starter)),
      source: source,
      sourceKey: Key('code-library-starter-source-${starter.name}'),
      actionKey: Key('code-library-starter-${starter.name}'),
      actionLabel: copy('code_library.starters.view'),
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
            _ExampleGrid(
              children: [
                for (final starter in _AccountSelectorStarter.values)
                  _ExampleCard(
                    icon: Icons.person_outline,
                    label: _accountSelectorStarterLabel(copy, starter),
                    badge: 'JavaScript',
                    detail: copy(
                      'code_library.selector.starter.${starter.name}.detail',
                    ),
                    source: _accountSelectorStarterPolicy(starter).javaScript,
                    sourceKey: Key(
                      'code-library-selector-starter-source-${starter.name}',
                    ),
                    actionKey: Key(
                      'code-library-selector-starter-${starter.name}',
                    ),
                    actionLabel: copy('code_library.starters.view'),
                    enabled: enabled,
                    onUse: () => onUse(starter),
                  ),
              ],
            ),
          ],
        ),
      ),
    ),
  );
}

final class _ExampleGrid extends StatelessWidget {
  const _ExampleGrid({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, constraints) {
      final columns = constraints.maxWidth >= 900
          ? 3
          : constraints.maxWidth >= 620
          ? 2
          : 1;
      return Column(
        children: [
          for (var start = 0; start < children.length; start += columns) ...[
            IntrinsicHeight(
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (var column = 0; column < columns; column++) ...[
                    if (column > 0) const SizedBox(width: 12),
                    Expanded(
                      child: start + column < children.length
                          ? children[start + column]
                          : const SizedBox.shrink(),
                    ),
                  ],
                ],
              ),
            ),
            if (start + columns < children.length) const SizedBox(height: 12),
          ],
        ],
      );
    },
  );
}

final class _ExampleCard extends StatelessWidget {
  const _ExampleCard({
    required this.icon,
    required this.label,
    required this.badge,
    required this.detail,
    required this.source,
    required this.sourceKey,
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
  final Key sourceKey;
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
        const Spacer(),
        Container(
          key: sourceKey,
          height: 104,
          padding: const EdgeInsets.all(10),
          decoration: BoxDecoration(
            color: context.viberColors.panelRaised,
            borderRadius: BorderRadius.circular(6),
          ),
          child: SelectionArea(
            child: Text.rich(
              javaScriptTextSpan(
                context,
                source,
                style: monoStyle.copyWith(
                  color: context.viberColors.textMuted,
                  fontSize: 10.5,
                ),
              ),
              maxLines: 5,
              overflow: TextOverflow.clip,
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
          copy: copy,
        ),
        const SizedBox(height: 10),
        _SourcePanel(
          title: copy('environment.transform.response'),
          source: value.policy.responseJavaScript,
          emptyLabel: copy('code_library.no_changes'),
          copy: copy,
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
          copy: copy,
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
    required this.copy,
  });

  final String title;
  final String source;
  final String emptyLabel;
  final AppCopy copy;

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
        Row(
          children: [
            Expanded(
              child: Text(title, style: Theme.of(context).textTheme.titleSmall),
            ),
            if (source.isNotEmpty)
              IconButton(
                tooltip: copy.format('common.copy', {'field': title}),
                onPressed: () =>
                    unawaited(Clipboard.setData(ClipboardData(text: source))),
                icon: const Icon(Icons.content_copy_rounded, size: 15),
                visualDensity: VisualDensity.compact,
              ),
          ],
        ),
        const SizedBox(height: 8),
        if (source.isEmpty)
          SelectableText(
            emptyLabel,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          )
        else
          SelectableText.rich(
            javaScriptTextSpan(
              context,
              source,
              style: monoStyle.copyWith(color: context.viberColors.text),
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

enum _TransformStarter {
  blank,
  localIdentity,
  blockSecrets,
  privateContacts,
  turnTime,
  replyLanguage,
  workspaceRules,
  responseModel,
}

enum _AccountSelectorStarter { loginUser }

final class _TransformDraftDialog extends StatefulWidget {
  const _TransformDraftDialog({
    required this.collections,
    required this.initialWireProtocol,
    this.initialStarter = _TransformStarter.blank,
    this.starterLocked = false,
    this.initialPolicy,
    required this.copy,
  });

  final List<CodeLibraryCollection> collections;
  final String? initialWireProtocol;
  final _TransformStarter initialStarter;
  final bool starterLocked;
  final TrafficTransformPolicy? initialPolicy;
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
          if (!widget.starterLocked) ...[
            const SizedBox(height: 10),
            DropdownButtonFormField<String>(
              key: const Key('code-library-transform-protocol'),
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
              policy:
                  widget.initialPolicy ??
                  _starterPolicy(_starter, _wireProtocol),
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
      _TransformStarter.localIdentity => copy(
        'code_library.starter.local_identity',
      ),
      _TransformStarter.blockSecrets => copy(
        'code_library.starter.block_secrets',
      ),
      _TransformStarter.privateContacts => copy(
        'code_library.starter.private_contacts',
      ),
      _TransformStarter.turnTime => copy('code_library.starter.turn_time'),
      _TransformStarter.replyLanguage => copy(
        'code_library.starter.reply_language',
      ),
      _TransformStarter.workspaceRules => copy(
        'code_library.starter.workspace_rules',
      ),
      _TransformStarter.responseModel => copy(
        'code_library.starter.response_model',
      ),
    };

String _starterDetailKey(_TransformStarter starter) => switch (starter) {
  _TransformStarter.blank => 'code_library.empty.detail',
  _TransformStarter.localIdentity =>
    'code_library.starter.local_identity.detail',
  _TransformStarter.blockSecrets => 'code_library.starter.block_secrets.detail',
  _TransformStarter.privateContacts =>
    'code_library.starter.private_contacts.detail',
  _TransformStarter.turnTime => 'code_library.starter.turn_time.detail',
  _TransformStarter.replyLanguage =>
    'code_library.starter.reply_language.detail',
  _TransformStarter.workspaceRules =>
    'code_library.starter.workspace_rules.detail',
  _TransformStarter.responseModel =>
    'code_library.starter.response_model.detail',
};

String _accountSelectorStarterLabel(
  AppCopy copy,
  _AccountSelectorStarter starter,
) => copy('code_library.selector.starter.${starter.name}');

AccountSelectorPolicy _accountSelectorStarterPolicy(
  _AccountSelectorStarter starter,
) => AccountSelectorPolicy(
  javaScript: switch (starter) {
    _AccountSelectorStarter.loginUser =>
      r'''const accountByLogin = {
  "alice": "account.team-a",
  "bob": "account.team-b",
};
if (!runtime.login.username) {
  throw new Error("ViberMate login is required");
}
const accountId = accountByLogin[runtime.login.username];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this ViberMate login");
}
selection.accountId = accountId;''',
  },
);

TrafficTransformPolicy _starterPolicy(
  _TransformStarter starter,
  String wireProtocol,
) => switch (starter) {
  _TransformStarter.blank => const TrafficTransformPolicy.disabled(),
  _TransformStarter.localIdentity => const TrafficTransformPolicy(
    requestJavaScript: _localIdentityRequest,
    responseJavaScript: _restoreRedactionsResponse,
  ),
  _TransformStarter.blockSecrets => const TrafficTransformPolicy(
    requestJavaScript: _blockSecretsRequest,
    responseJavaScript: '',
  ),
  _TransformStarter.privateContacts => const TrafficTransformPolicy(
    requestJavaScript: _privateContactsRequest,
    responseJavaScript: _restoreRedactionsResponse,
  ),
  _TransformStarter.turnTime => TrafficTransformPolicy(
    requestJavaScript: '',
    responseJavaScript: _turnTimeStarter(wireProtocol),
  ),
  _TransformStarter.replyLanguage => TrafficTransformPolicy(
    requestJavaScript: _replyLanguageStarter(wireProtocol),
    responseJavaScript: '',
  ),
  _TransformStarter.workspaceRules => TrafficTransformPolicy(
    requestJavaScript: _workspaceRulesStarter(wireProtocol),
    responseJavaScript: '',
  ),
  _TransformStarter.responseModel => TrafficTransformPolicy(
    requestJavaScript: '',
    responseJavaScript: _responseModelStarter(wireProtocol),
  ),
};

MessageTransformTestSample _starterTestSample(
  _TransformStarter starter,
  String wireProtocol,
) => switch (starter) {
  _TransformStarter.localIdentity => MessageTransformTestSample.example(
    wireProtocol,
    userMessage:
        'My username is example-user. My home is /Users/example-user '
        'and my project is /Users/example-user/Code/example. Where should I put the config?',
    assistantMessage:
        'For vibermate-user, put the project config in /workspace/project '
        'or the user config in /Users/guest.',
  ),
  _TransformStarter.privateContacts => MessageTransformTestSample.example(
    wireProtocol,
    userMessage:
        'Email alice@example.com from 10.0.0.8; public DNS is 8.8.8.8.',
    assistantMessage:
        'Contact redacted-email-1@example.invalid from 192.0.2.2; public DNS is 8.8.8.8.',
  ),
  _ => MessageTransformTestSample.example(wireProtocol),
};

const _localIdentityRequest = r'''const candidates = [
  [runtime.workspace.root, "/workspace/project"],
  [runtime.user.homeDirectory, "/Users/guest"],
  [runtime.user.name, "vibermate-user"],
];
context.redactions = [];
for (let index = 0; index < candidates.length; index += 1) {
  const privateValue = candidates[index][0];
  const publicValue = candidates[index][1];
  if (!privateValue || privateValue === publicValue) continue;
  const encodedPrivate = JSON.stringify(privateValue).slice(1, -1);
  const encodedPublic = JSON.stringify(publicValue).slice(1, -1);
  if (!request.body.includes(encodedPrivate)) continue;
  request.body = request.body.split(encodedPrivate).join(encodedPublic);
  context.redactions.push([encodedPrivate, encodedPublic]);
}''';

const _blockSecretsRequest =
    r'''const privateKey = /-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----/;
const commonKey = /\b(?:sk-ant-[A-Za-z0-9_-]{16,}|sk-proj-[A-Za-z0-9_-]{16,}|sk-[A-Za-z0-9_-]{20,}|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|xox[baprs]-[A-Za-z0-9-]{16,}|AKIA[A-Z0-9]{16})\b/;
if (privateKey.test(request.body) || commonKey.test(request.body)) {
  throw new Error("Request blocked because it contains a private key or access token");
}''';

const _privateContactsRequest = r'''context.redactions = [];
function hide(value, kind) {
  const existing = context.redactions.find(function (item) { return item[0] === value; });
  if (existing) return existing[1];
  const suffix = context.redactions.length + 1;
  const visible = kind === "email"
    ? "redacted-email-" + suffix + "@example.invalid"
    : "192.0.2." + suffix;
  context.redactions.push([value, visible]);
  return visible;
}
request.body = request.body.replace(
  /[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi,
  function (value) { return hide(value, "email"); }
);
request.body = request.body.replace(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, function (value) {
  const parts = value.split(".").map(Number);
  const privateAddress = parts.every(function (part) { return part >= 0 && part <= 255; }) &&
    (parts[0] === 10 || parts[0] === 127 ||
      (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
      (parts[0] === 192 && parts[1] === 168));
  return privateAddress ? hide(value, "ip") : value;
});''';

const _restoreRedactionsResponse = r'''if (Array.isArray(context.redactions)) {
  for (let index = 0; index < context.redactions.length; index += 1) {
    response.body = response.body
      .split(context.redactions[index][1])
      .join(context.redactions[index][0]);
  }
}''';

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

String _replyLanguageStarter(String wireProtocol) => switch (wireProtocol) {
  'openai_responses' =>
    r'''const payload = JSON.parse(request.body);
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
payload.instructions = typeof payload.instructions === "string"
  ? payload.instructions + "\n\n" + guidance
  : guidance;
request.body = JSON.stringify(payload);''',
  'openai_chat' =>
    r'''const payload = JSON.parse(request.body);
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
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
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
if (typeof payload.system === "string") {
  payload.system += "\n\n" + guidance;
} else if (Array.isArray(payload.system)) {
  payload.system.push({type: "text", text: guidance});
} else {
  payload.system = guidance;
}
request.body = JSON.stringify(payload);''',
};

String _workspaceRulesStarter(String wireProtocol) => switch (wireProtocol) {
  'openai_responses' =>
    r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
  payload.instructions = typeof payload.instructions === "string"
    ? payload.instructions + "\n\n" + guidance
    : guidance;
  request.body = JSON.stringify(payload);
}''',
  'openai_chat' =>
    r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
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
  request.body = JSON.stringify(payload);
}''',
  _ =>
    r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
  if (typeof payload.system === "string") {
    payload.system += "\n\n" + guidance;
  } else if (Array.isArray(payload.system)) {
    payload.system.push({type: "text", text: guidance});
  } else {
    payload.system = guidance;
  }
  request.body = JSON.stringify(payload);
}''',
};

String _responseModelStarter(String wireProtocol) => switch (wireProtocol) {
  'openai_responses' =>
    r'''const payload = JSON.parse(response.body);
if (typeof payload.model === "string") context.responseModel = payload.model;
if (payload.response && typeof payload.response.model === "string") {
  context.responseModel = payload.response.model;
}
if (response.streaming && !context.responseModelShown && context.responseModel && payload.type === "response.output_text.delta" && typeof payload.delta === "string") {
  payload.delta = runtime.annotations.create("response-model", context.responseModel) + "\n" + payload.delta;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && Array.isArray(payload.output)) {
  for (let outputIndex = 0; outputIndex < payload.output.length; outputIndex += 1) {
    const item = payload.output[outputIndex];
    if (!Array.isArray(item.content)) continue;
    for (let contentIndex = 0; contentIndex < item.content.length; contentIndex += 1) {
      const part = item.content[contentIndex];
      if (part.type === "output_text" && typeof part.text === "string") {
        part.text = runtime.annotations.create("response-model", context.responseModel) + "\n" + part.text;
        outputIndex = payload.output.length;
        break;
      }
    }
  }
}
response.body = JSON.stringify(payload);''',
  'openai_chat' =>
    r'''const payload = JSON.parse(response.body);
if (typeof payload.model === "string") context.responseModel = payload.model;
const choice = Array.isArray(payload.choices) ? payload.choices[0] : undefined;
if (response.streaming && !context.responseModelShown && context.responseModel && choice && choice.delta && typeof choice.delta.content === "string") {
  choice.delta.content = runtime.annotations.create("response-model", context.responseModel) + "\n" + choice.delta.content;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && choice && choice.message && typeof choice.message.content === "string") {
  choice.message.content = runtime.annotations.create("response-model", context.responseModel) + "\n" + choice.message.content;
}
response.body = JSON.stringify(payload);''',
  _ =>
    r'''const payload = JSON.parse(response.body);
if (payload.type === "message_start" && payload.message && typeof payload.message.model === "string") {
  context.responseModel = payload.message.model;
}
if (!response.streaming && typeof payload.model === "string") context.responseModel = payload.model;
if (response.streaming && !context.responseModelShown && context.responseModel && payload.type === "content_block_delta" && payload.delta && payload.delta.type === "text_delta") {
  payload.delta.text = runtime.annotations.create("response-model", context.responseModel) + "\n" + payload.delta.text;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && Array.isArray(payload.content)) {
  payload.content.unshift({
    type: "text",
    text: runtime.annotations.create("response-model", context.responseModel),
  });
}
response.body = JSON.stringify(payload);''',
};

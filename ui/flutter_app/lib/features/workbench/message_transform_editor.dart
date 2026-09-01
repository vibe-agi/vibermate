import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

typedef MessageTransformTestCallback =
    Future<MessageTransformTestResult> Function({
      required String wireProtocol,
      required TrafficTransformPolicy policy,
      MessageTransformTestSample? sample,
    });

typedef CodeLibraryLoader = Future<CodeLibraryCatalog> Function();

/// Selects the ordered, immutable Transform revisions frozen by one protocol.
final class MessageTransformPipelineButton extends StatelessWidget {
  const MessageTransformPipelineButton({
    required this.plan,
    required this.copy,
    required this.enabled,
    required this.loadLibrary,
    required this.onChanged,
    super.key,
  });

  final EnvironmentProtocolPlan plan;
  final AppCopy copy;
  final bool enabled;
  final CodeLibraryLoader loadLibrary;
  final ValueChanged<List<CodeLibraryTransformRevision>> onChanged;

  @override
  Widget build(BuildContext context) {
    return Container(
      color: context.viberColors.panelRaised.withValues(alpha: 0.34),
      padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
      child: CompactLabeledControl(
        label: copy('environment.transform.label'),
        detail: copy('environment.transform.detail'),
        child: SizedBox(
          width: double.infinity,
          height: ViberMetrics.controlHeight,
          child: OutlinedButton.icon(
            key: Key('environment-transform-${plan.id}'),
            onPressed: enabled ? () => unawaited(_edit(context)) : null,
            icon: const Icon(Icons.data_object_rounded, size: 14),
            label: Align(
              alignment: Alignment.centerLeft,
              child: Text(
                _summary(copy, plan.transforms),
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
      ),
    );
  }

  Future<void> _edit(BuildContext context) async {
    final selection = await showDialog<List<CodeLibraryTransformRevision>>(
      context: context,
      barrierDismissible: true,
      builder: (context) => MessageTransformPipelineDialog(
        planId: plan.id,
        initial: plan.transforms,
        copy: copy,
        loadLibrary: loadLibrary,
      ),
    );
    if (selection != null) onChanged(selection);
  }

  static String _summary(
    AppCopy copy,
    List<CodeLibraryTransformRevision> transforms,
  ) {
    if (transforms.isEmpty) return copy('environment.transform.summary.off');
    if (transforms.length == 1) {
      final transform = transforms.single;
      return copy.format('environment.transform.pipeline.item', {
        'name': transform.displayName,
        'revision': transform.revision,
      });
    }
    return copy.format('environment.transform.pipeline.count', {
      'count': transforms.length,
    });
  }
}

final class MessageTransformPipelineDialog extends StatefulWidget {
  const MessageTransformPipelineDialog({
    required this.planId,
    required this.initial,
    required this.copy,
    required this.loadLibrary,
    super.key,
  });

  final String planId;
  final List<CodeLibraryTransformRevision> initial;
  final AppCopy copy;
  final CodeLibraryLoader loadLibrary;

  @override
  State<MessageTransformPipelineDialog> createState() =>
      _MessageTransformPipelineDialogState();
}

final class _MessageTransformPipelineDialogState
    extends State<MessageTransformPipelineDialog> {
  late final List<CodeLibraryTransformRevision> _selected;
  CodeLibraryCatalog? _catalog;
  bool _loading = true;
  bool _failed = false;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _selected = List.of(widget.initial);
    unawaited(_load());
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _failed = false;
    });
    try {
      final catalog = await widget.loadLibrary();
      if (!mounted) return;
      setState(() {
        _catalog = catalog;
        _loading = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _failed = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    final width = math.max(280.0, math.min(640.0, viewport.width - 48));
    final height = math.max(420.0, math.min(580.0, viewport.height - 48));
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: Key('environment-transform-pipeline-dialog-${widget.planId}'),
        width: width,
        height: height,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 8, 10),
              child: Row(
                children: [
                  Icon(
                    Icons.account_tree_outlined,
                    size: 19,
                    color: context.viberColors.route,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      copy('environment.transform.pipeline.title'),
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
            const Divider(height: 1),
            Expanded(child: _content(context)),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  TextButton(
                    onPressed: () => Navigator.of(context).pop(),
                    child: Text(copy('common.cancel')),
                  ),
                  const SizedBox(width: 8),
                  FilledButton(
                    key: Key(
                      'environment-transform-pipeline-save-${widget.planId}',
                    ),
                    onPressed: () => Navigator.of(context).pop(
                      List<CodeLibraryTransformRevision>.unmodifiable(
                        _selected,
                      ),
                    ),
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

  Widget _content(BuildContext context) {
    if (_loading) return const Center(child: CompactProgressIndicator());
    if (_failed) {
      return Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(copy('environment.transform.pipeline.load_failed')),
            const SizedBox(height: 8),
            OutlinedButton(
              onPressed: () => unawaited(_load()),
              child: Text(copy('common.retry')),
            ),
          ],
        ),
      );
    }
    final available = _catalog!.transforms
        .where((candidate) => !_selected.contains(candidate))
        .toList(growable: false);
    return ListView(
      padding: const EdgeInsets.all(12),
      children: [
        Text(
          copy('environment.transform.pipeline.selected'),
          style: Theme.of(context).textTheme.titleSmall,
        ),
        const SizedBox(height: 6),
        if (_selected.isEmpty)
          _empty(context, copy('environment.transform.pipeline.empty'))
        else
          for (final (index, transform) in _selected.indexed)
            _selectedRow(context, index, transform),
        const SizedBox(height: 16),
        Text(
          copy('environment.transform.pipeline.available'),
          style: Theme.of(context).textTheme.titleSmall,
        ),
        const SizedBox(height: 6),
        if (available.isEmpty)
          _empty(context, copy('environment.transform.pipeline.none_available'))
        else
          for (final transform in available) _availableRow(context, transform),
      ],
    );
  }

  Widget _selectedRow(
    BuildContext context,
    int index,
    CodeLibraryTransformRevision transform,
  ) => Container(
    key: Key(
      'environment-transform-pipeline-selected-${transform.id}-${transform.revision}',
    ),
    margin: const EdgeInsets.only(bottom: 5),
    decoration: BoxDecoration(
      border: Border.all(color: context.viberColors.divider),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Row(
      children: [
        SizedBox(
          width: 30,
          child: Center(
            child: Text(
              '${index + 1}',
              style: Theme.of(context).textTheme.labelMedium?.copyWith(
                color: context.viberColors.route,
              ),
            ),
          ),
        ),
        Expanded(child: _transformLabel(context, transform)),
        _iconButton(
          key: Key(
            'environment-transform-pipeline-up-${transform.id}-${transform.revision}',
          ),
          tooltip: copy('environment.transform.pipeline.move_up'),
          icon: Icons.arrow_upward_rounded,
          onPressed: index == 0
              ? null
              : () => setState(() {
                  final item = _selected.removeAt(index);
                  _selected.insert(index - 1, item);
                }),
        ),
        _iconButton(
          key: Key(
            'environment-transform-pipeline-down-${transform.id}-${transform.revision}',
          ),
          tooltip: copy('environment.transform.pipeline.move_down'),
          icon: Icons.arrow_downward_rounded,
          onPressed: index == _selected.length - 1
              ? null
              : () => setState(() {
                  final item = _selected.removeAt(index);
                  _selected.insert(index + 1, item);
                }),
        ),
        _iconButton(
          key: Key(
            'environment-transform-pipeline-remove-${transform.id}-${transform.revision}',
          ),
          tooltip: copy('common.remove'),
          icon: Icons.close_rounded,
          onPressed: () => setState(() => _selected.removeAt(index)),
        ),
      ],
    ),
  );

  Widget _availableRow(
    BuildContext context,
    CodeLibraryTransformRevision transform,
  ) => Container(
    margin: const EdgeInsets.only(bottom: 5),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised.withValues(alpha: 0.34),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Row(
      children: [
        const SizedBox(width: 9),
        Expanded(child: _transformLabel(context, transform)),
        _iconButton(
          key: Key(
            'environment-transform-pipeline-add-${transform.id}-${transform.revision}',
          ),
          tooltip: copy('common.add'),
          icon: Icons.add_rounded,
          onPressed: () => setState(() => _selected.add(transform)),
        ),
      ],
    ),
  );

  Widget _transformLabel(
    BuildContext context,
    CodeLibraryTransformRevision transform,
  ) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 7),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(transform.displayName, overflow: TextOverflow.ellipsis),
        Text(
          '${transform.collectionId} · r${transform.revision}',
          overflow: TextOverflow.ellipsis,
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
      ],
    ),
  );

  Widget _empty(BuildContext context, String text) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 9),
    color: context.viberColors.panelRaised.withValues(alpha: 0.34),
    child: Text(
      text,
      style: Theme.of(
        context,
      ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
    ),
  );

  Widget _iconButton({
    required Key key,
    required String tooltip,
    required IconData icon,
    required VoidCallback? onPressed,
  }) => SizedBox(
    width: 32,
    height: 32,
    child: IconButton(
      key: key,
      tooltip: tooltip,
      onPressed: onPressed,
      padding: EdgeInsets.zero,
      iconSize: 16,
      icon: Icon(icon),
    ),
  );
}

/// One bounded editor for the request and response halves of a single Turn.
final class MessageTransformEditorDialog extends StatefulWidget {
  const MessageTransformEditorDialog({
    required this.planId,
    required this.displayName,
    required this.wireProtocol,
    required this.initial,
    required this.copy,
    required this.testTransform,
    this.initialSample,
    this.baseRevision,
    this.onTestWireProtocolChanged,
    this.primaryActionLabel,
    super.key,
  });

  final String planId;
  final String displayName;
  final String wireProtocol;
  final TrafficTransformPolicy initial;
  final AppCopy copy;
  final MessageTransformTestCallback testTransform;
  final MessageTransformTestSample? initialSample;
  final int? baseRevision;
  final ValueChanged<String>? onTestWireProtocolChanged;
  final String? primaryActionLabel;

  @override
  State<MessageTransformEditorDialog> createState() =>
      _MessageTransformEditorDialogState();
}

final class _MessageTransformEditorDialogState
    extends State<MessageTransformEditorDialog>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs;
  late final JavaScriptEditingController _request;
  late final JavaScriptEditingController _response;
  late String _wireProtocol;
  late MessageTransformTestSample? _sample;
  final Map<String, MessageTransformTestSample?> _samplesByProtocol = {};
  MessageTransformTestResult? _result;
  bool _testing = false;
  String? _error;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _tabs = TabController(length: 2, vsync: this);
    _request = JavaScriptEditingController(
      text: widget.initial.requestJavaScript,
    );
    _response = JavaScriptEditingController(
      text: widget.initial.responseJavaScript,
    );
    _wireProtocol = widget.wireProtocol;
    _sample = widget.initialSample;
    _samplesByProtocol[_wireProtocol] = _sample;
  }

  @override
  void dispose() {
    _tabs.dispose();
    _request.dispose();
    _response.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.escape): () =>
            unawaited(Navigator.of(context).maybePop()),
      },
      child: Focus(
        autofocus: true,
        child: Scaffold(
          key: const Key('message-transform-editor-page'),
          backgroundColor: context.viberColors.canvas,
          body: SafeArea(
            child: SizedBox.expand(
              key: Key('environment-transform-dialog-${widget.planId}'),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  _header(context),
                  const Divider(height: 1),
                  _scope(context),
                  TabBar(
                    controller: _tabs,
                    isScrollable: false,
                    tabs: [
                      Tab(
                        key: Key(
                          'environment-transform-tab-request-${widget.planId}',
                        ),
                        text: copy('environment.transform.request'),
                      ),
                      Tab(
                        key: Key(
                          'environment-transform-tab-response-${widget.planId}',
                        ),
                        text: copy('environment.transform.response'),
                      ),
                    ],
                  ),
                  Expanded(
                    child: TabBarView(
                      controller: _tabs,
                      children: [
                        _ScriptPane(
                          planId: widget.planId,
                          stage: 'request',
                          controller: _request,
                          copy: copy,
                          suggestions: const [
                            'request.body',
                            'request.headers["x-name"]',
                            'context.value',
                            'runtime.user.homeDirectory',
                            'runtime.user.name',
                            'runtime.workspace.root',
                            'JSON.parse(request.body)',
                          ],
                        ),
                        _ScriptPane(
                          planId: widget.planId,
                          stage: 'response',
                          controller: _response,
                          copy: copy,
                          suggestions: const [
                            'response.body',
                            'response.headers["x-name"]',
                            'context.value',
                            'runtime.turn.startedAt',
                            'runtime.device.timeZone',
                            'runtime.annotations.create("kind", "text")',
                            'JSON.parse(response.body)',
                          ],
                        ),
                      ],
                    ),
                  ),
                  if (_result case final result?)
                    _ResultPanel(result: result, copy: copy),
                  if (_error case final error?)
                    Container(
                      key: Key('environment-transform-error-${widget.planId}'),
                      color: context.viberColors.danger.withValues(alpha: 0.10),
                      padding: const EdgeInsets.symmetric(
                        horizontal: 12,
                        vertical: 7,
                      ),
                      child: Text(
                        error,
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.danger,
                        ),
                      ),
                    ),
                  const Divider(height: 1),
                  _actions(context),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _header(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 8, 10),
      child: Row(
        children: [
          Icon(
            Icons.data_object_rounded,
            size: 19,
            color: context.viberColors.route,
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  widget.displayName,
                  key: Key('environment-transform-title-${widget.planId}'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.headlineSmall,
                ),
                Text(
                  '${widget.baseRevision == null ? copy('environment.transform.draft.new') : copy.format('environment.transform.draft.revision', {'revision': widget.baseRevision!})} · ${_protocolLabel(copy, _wireProtocol)}',
                  key: Key('environment-transform-subtitle-${widget.planId}'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            ),
          ),
          IconButton(
            key: Key('environment-transform-close-${widget.planId}'),
            tooltip: copy('common.dismiss'),
            onPressed: () => Navigator.of(context).pop(),
            icon: const Icon(Icons.close, size: 18),
          ),
        ],
      ),
    );
  }

  Widget _scope(BuildContext context) {
    return Container(
      color: context.viberColors.selection.withValues(alpha: 0.48),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
      child: Wrap(
        spacing: 12,
        runSpacing: 3,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text(
            copy('environment.transform.scope'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(
                Icons.link_rounded,
                size: 13,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 4),
              Text(
                copy('environment.transform.context'),
                style: Theme.of(context).textTheme.labelMedium?.copyWith(
                  color: context.viberColors.route,
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
          if (_sample != null)
            OutlinedButton.icon(
              key: Key('environment-transform-sample-${widget.planId}'),
              onPressed: _testing ? null : () => unawaited(_editSample()),
              icon: const Icon(Icons.science_outlined, size: 14),
              label: Text(copy('environment.transform.sample.edit')),
              style: OutlinedButton.styleFrom(
                visualDensity: VisualDensity.compact,
              ),
            ),
        ],
      ),
    );
  }

  Future<void> _editSample() async {
    final current = _sample;
    if (current == null) return;
    final edited = await showDialog<MessageTransformTestSample>(
      context: context,
      builder: (context) => _MessageTransformSampleDialog(
        planId: widget.planId,
        initial: current,
        copy: copy,
      ),
    );
    if (edited != null && mounted) {
      setState(() {
        _sample = edited;
        _result = null;
        _error = null;
      });
    }
  }

  Widget _actions(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
      child: Wrap(
        alignment: WrapAlignment.end,
        crossAxisAlignment: WrapCrossAlignment.center,
        spacing: 8,
        runSpacing: 8,
        children: [
          TextButton(
            onPressed: _testing ? null : () => Navigator.of(context).pop(),
            child: Text(copy('common.cancel')),
          ),
          if (widget.onTestWireProtocolChanged != null)
            _TestProtocolMenu(
              planId: widget.planId,
              value: _wireProtocol,
              copy: copy,
              enabled: !_testing,
              onChanged: _selectTestWireProtocol,
            ),
          OutlinedButton.icon(
            key: Key('environment-transform-test-${widget.planId}'),
            onPressed: _testing ? null : () => unawaited(_runTest()),
            icon: _testing
                ? const CompactProgressIndicator()
                : const Icon(Icons.play_arrow_rounded, size: 16),
            label: Text(copy('environment.transform.test')),
          ),
          FilledButton(
            key: Key('environment-transform-save-${widget.planId}'),
            onPressed: _testing ? null : _save,
            child: Text(widget.primaryActionLabel ?? copy('common.save')),
          ),
        ],
      ),
    );
  }

  void _selectTestWireProtocol(String value) {
    if (value == _wireProtocol) return;
    setState(() {
      _samplesByProtocol[_wireProtocol] = _sample;
      _wireProtocol = value;
      _sample = _samplesByProtocol[value];
      _result = null;
      _error = null;
    });
    widget.onTestWireProtocolChanged?.call(value);
  }

  TrafficTransformPolicy? _policy() {
    try {
      return TrafficTransformPolicy.fromJson({
        'requestJavaScript': _request.text,
        'responseJavaScript': _response.text,
      }, r'$.policy');
    } on ControlContractException {
      setState(() {
        _result = null;
        _error = copy('environment.transform.invalid');
      });
      return null;
    }
  }

  Future<void> _runTest() async {
    final policy = _policy();
    if (policy == null) return;
    setState(() {
      _testing = true;
      _result = null;
      _error = null;
    });
    try {
      final result = await widget.testTransform(
        wireProtocol: _wireProtocol,
        policy: policy,
        sample: _sample,
      );
      if (!mounted) return;
      setState(() => _result = result);
    } on Object catch (error) {
      if (!mounted) return;
      setState(() => _error = _testError(error));
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }

  String _testError(Object error) {
    if (error is ControlProblem &&
        error.reasonCode == 'message_transform_test_failed') {
      final detail = error.detail?.trim();
      return '${copy('environment.transform.test.failed')}\n${error.reasonCode}${detail == null || detail.isEmpty ? '' : ' · $detail'}';
    }
    return copy('environment.transform.test.unavailable');
  }

  void _save() {
    final policy = _policy();
    if (policy != null) Navigator.of(context).pop(policy);
  }
}

final class _TestProtocolMenu extends StatelessWidget {
  const _TestProtocolMenu({
    required this.planId,
    required this.value,
    required this.copy,
    required this.enabled,
    required this.onChanged,
  });

  final String planId;
  final String value;
  final AppCopy copy;
  final bool enabled;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => SizedBox(
    key: Key('environment-transform-test-format-$planId'),
    height: ViberMetrics.controlHeight,
    child: PopupMenuButton<String>(
      initialValue: value,
      enabled: enabled,
      tooltip: copy('code_library.test_protocol.detail'),
      onSelected: onChanged,
      itemBuilder: (context) => const [
        PopupMenuItem(
          value: 'anthropic_messages',
          child: Text('Anthropic Messages'),
        ),
        PopupMenuItem(
          value: 'openai_responses',
          child: Text('OpenAI Responses'),
        ),
        PopupMenuItem(value: 'openai_chat', child: Text('OpenAI Chat')),
      ],
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 9),
        decoration: BoxDecoration(
          color: context.viberColors.input,
          border: Border.all(color: context.viberColors.divider),
          borderRadius: ViberMetrics.controlRadius,
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              Icons.science_outlined,
              size: 14,
              color: context.viberColors.textMuted,
            ),
            const SizedBox(width: 6),
            Flexible(
              child: Text(
                '${copy('code_library.test_protocol')} · ${_protocolLabel(copy, value)}',
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.labelLarge,
              ),
            ),
            const SizedBox(width: 6),
            const Icon(Icons.arrow_drop_down_rounded, size: 17),
          ],
        ),
      ),
    ),
  );
}

final class _MessageTransformSampleDialog extends StatefulWidget {
  const _MessageTransformSampleDialog({
    required this.planId,
    required this.initial,
    required this.copy,
  });

  final String planId;
  final MessageTransformTestSample initial;
  final AppCopy copy;

  @override
  State<_MessageTransformSampleDialog> createState() =>
      _MessageTransformSampleDialogState();
}

final class _MessageTransformSampleDialogState
    extends State<_MessageTransformSampleDialog> {
  late final TextEditingController _requestHeaders;
  late final TextEditingController _requestBody;
  late final TextEditingController _responseHeaders;
  late final TextEditingController _responseBody;
  late final TextEditingController _status;
  late bool _streaming;
  bool _invalid = false;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    const encoder = JsonEncoder.withIndent('  ');
    _requestHeaders = TextEditingController(
      text: encoder.convert(widget.initial.request.headers),
    );
    _requestBody = TextEditingController(text: widget.initial.request.body);
    _responseHeaders = TextEditingController(
      text: encoder.convert(widget.initial.response.headers),
    );
    _responseBody = TextEditingController(text: widget.initial.response.body);
    _status = TextEditingController(
      text: widget.initial.response.statusCode.toString(),
    );
    _streaming = widget.initial.response.streaming;
  }

  @override
  void dispose() {
    _requestHeaders.dispose();
    _requestBody.dispose();
    _responseHeaders.dispose();
    _responseBody.dispose();
    _status.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final viewport = MediaQuery.sizeOf(context);
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: Key('environment-transform-sample-dialog-${widget.planId}'),
        width: math.max(280, math.min(720, viewport.width - 48)),
        height: math.max(420, math.min(620, viewport.height - 48)),
        child: DefaultTabController(
          length: 2,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 14, 8, 10),
                child: Row(
                  children: [
                    const Icon(Icons.science_outlined, size: 18),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            copy('environment.transform.sample.title'),
                            style: Theme.of(context).textTheme.titleLarge,
                          ),
                          Text(
                            copy('environment.transform.sample.local_detail'),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(
                                  color: context.viberColors.textMuted,
                                ),
                          ),
                        ],
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
              const Divider(height: 1),
              TabBar(
                tabs: [
                  Tab(
                    key: const Key('environment-transform-sample-tab-request'),
                    text: copy('environment.transform.request'),
                  ),
                  Tab(
                    key: const Key('environment-transform-sample-tab-response'),
                    text: copy('environment.transform.response'),
                  ),
                ],
              ),
              Expanded(
                child: TabBarView(
                  children: [_requestPane(context), _responsePane(context)],
                ),
              ),
              if (_invalid)
                Padding(
                  padding: const EdgeInsets.symmetric(horizontal: 14),
                  child: Text(
                    copy('environment.transform.sample.invalid'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.danger,
                    ),
                  ),
                ),
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.end,
                  children: [
                    TextButton(
                      onPressed: () => Navigator.of(context).pop(),
                      child: Text(copy('common.cancel')),
                    ),
                    const SizedBox(width: 8),
                    FilledButton(
                      key: const Key('environment-transform-sample-save'),
                      onPressed: _save,
                      child: Text(copy('environment.transform.sample.save')),
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

  Widget _requestPane(BuildContext context) => ListView(
    padding: const EdgeInsets.all(14),
    children: [
      SelectableText(
        '${widget.initial.request.method} ${widget.initial.request.path}',
        style: monoStyle.copyWith(color: context.viberColors.textMuted),
      ),
      const SizedBox(height: 10),
      _sampleTextField(
        key: const Key('environment-transform-sample-request-headers'),
        controller: _requestHeaders,
        label: copy('environment.transform.sample.headers'),
        lines: 6,
      ),
      const SizedBox(height: 10),
      _sampleTextField(
        key: const Key('environment-transform-sample-request-body'),
        controller: _requestBody,
        label: copy('environment.transform.sample.body'),
        lines: 10,
      ),
    ],
  );

  Widget _responsePane(BuildContext context) => ListView(
    padding: const EdgeInsets.all(14),
    children: [
      Row(
        children: [
          Expanded(
            child: TextField(
              key: const Key('environment-transform-sample-response-status'),
              controller: _status,
              keyboardType: TextInputType.number,
              decoration: InputDecoration(
                labelText: copy('environment.transform.sample.status'),
              ),
            ),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: CheckboxListTile(
              key: const Key('environment-transform-sample-response-streaming'),
              value: _streaming,
              contentPadding: EdgeInsets.zero,
              dense: true,
              title: Text(copy('environment.transform.sample.streaming')),
              onChanged: (value) => setState(() => _streaming = value == true),
            ),
          ),
        ],
      ),
      const SizedBox(height: 10),
      _sampleTextField(
        key: const Key('environment-transform-sample-response-headers'),
        controller: _responseHeaders,
        label: copy('environment.transform.sample.headers'),
        lines: 6,
      ),
      const SizedBox(height: 10),
      _sampleTextField(
        key: const Key('environment-transform-sample-response-body'),
        controller: _responseBody,
        label: copy('environment.transform.sample.body'),
        lines: 10,
      ),
    ],
  );

  Widget _sampleTextField({
    required Key key,
    required TextEditingController controller,
    required String label,
    required int lines,
  }) => TextField(
    key: key,
    controller: controller,
    minLines: lines,
    maxLines: lines,
    style: monoStyle,
    decoration: InputDecoration(labelText: label, alignLabelWithHint: true),
  );

  void _save() {
    try {
      final sample = MessageTransformTestSample(
        request: MessageTransformTestRequest.fromJson({
          'method': widget.initial.request.method,
          'path': widget.initial.request.path,
          'headers': jsonDecode(_requestHeaders.text),
          'body': _requestBody.text,
        }, r'$.sample.request'),
        response: MessageTransformTestResponse.fromJson({
          'statusCode': int.tryParse(_status.text),
          'streaming': _streaming,
          'headers': jsonDecode(_responseHeaders.text),
          'body': _responseBody.text,
        }, r'$.sample.response'),
      );
      Navigator.of(context).pop(sample);
    } on Object {
      setState(() => _invalid = true);
    }
  }
}

final class _ScriptPane extends StatefulWidget {
  const _ScriptPane({
    required this.planId,
    required this.stage,
    required this.controller,
    required this.copy,
    required this.suggestions,
  });

  final String planId;
  final String stage;
  final JavaScriptEditingController controller;
  final AppCopy copy;
  final List<String> suggestions;

  @override
  State<_ScriptPane> createState() => _ScriptPaneState();
}

final class _ScriptPaneState extends State<_ScriptPane> {
  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_changed);
  }

  @override
  void dispose() {
    widget.controller.removeListener(_changed);
    super.dispose();
  }

  void _changed() => setState(() {});

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 9, 12, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            copy('environment.transform.${widget.stage}.detail'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
          const SizedBox(height: 6),
          SizedBox(
            height: 28,
            child: ListView.separated(
              scrollDirection: Axis.horizontal,
              itemCount: widget.suggestions.length,
              separatorBuilder: (_, _) => const SizedBox(width: 5),
              itemBuilder: (context, index) {
                final snippet = widget.suggestions[index];
                return ActionChip(
                  key: Key(
                    'environment-transform-suggestion-${widget.stage}-$index',
                  ),
                  visualDensity: VisualDensity.compact,
                  label: Text(
                    snippet,
                    style: const TextStyle(fontFamily: 'Menlo', fontSize: 10.5),
                  ),
                  onPressed: () => widget.controller.insertSnippet(snippet),
                );
              },
            ),
          ),
          const SizedBox(height: 7),
          Expanded(
            child: DecoratedBox(
              decoration: BoxDecoration(
                color: context.viberColors.input,
                border: Border.all(color: context.viberColors.divider),
                borderRadius: ViberMetrics.controlRadius,
              ),
              child: TextField(
                key: Key(
                  'environment-transform-${widget.stage}-${widget.planId}',
                ),
                controller: widget.controller,
                expands: true,
                minLines: null,
                maxLines: null,
                keyboardType: TextInputType.multiline,
                textAlignVertical: TextAlignVertical.top,
                style: const TextStyle(
                  fontFamily: 'Menlo',
                  fontSize: 12,
                  height: 1.45,
                ),
                decoration: InputDecoration(
                  border: InputBorder.none,
                  contentPadding: const EdgeInsets.all(10),
                  hintText: copy('environment.transform.editor.hint'),
                ),
              ),
            ),
          ),
          const SizedBox(height: 4),
          Row(
            children: [
              Icon(
                Icons.lock_outline,
                size: 12,
                color: context.viberColors.textFaint,
              ),
              const SizedBox(width: 4),
              Expanded(
                child: Text(
                  copy('environment.transform.sandbox'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textFaint,
                  ),
                ),
              ),
              Text(
                copy.format('environment.transform.bytes', {
                  'count': utf8.encode(widget.controller.text).length,
                }),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textFaint,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

final class _ResultPanel extends StatelessWidget {
  const _ResultPanel({required this.result, required this.copy});

  final MessageTransformTestResult result;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, panelConstraints) => Container(
        key: const Key('environment-transform-test-result'),
        constraints: BoxConstraints(
          maxHeight: panelConstraints.maxWidth < 560 ? 160 : 240,
        ),
        decoration: BoxDecoration(
          color: context.viberColors.verified.withValues(alpha: 0.07),
          border: Border(top: BorderSide(color: context.viberColors.divider)),
        ),
        child: SingleChildScrollView(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
          child: LayoutBuilder(
            builder: (context, constraints) {
              final request = _ResultDiff(
                key: const Key('environment-transform-request-diff'),
                title: copy('environment.transform.diff.request'),
                before: _requestSnapshot(result.requestBefore),
                after: _requestSnapshot(result.requestAfter),
                unchangedLabel: copy('environment.transform.diff.unchanged'),
              );
              final response = _ResultDiff(
                key: const Key('environment-transform-response-diff'),
                title: copy('environment.transform.diff.response'),
                before: _responseSnapshot(result.responseBefore),
                after: _responseSnapshot(result.responseAfter),
                unchangedLabel: copy('environment.transform.diff.unchanged'),
              );
              return Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Icon(
                        Icons.check_circle_outline,
                        size: 14,
                        color: context.viberColors.verified,
                      ),
                      const SizedBox(width: 5),
                      Text(
                        copy('environment.transform.test.passed'),
                        style: Theme.of(context).textTheme.labelMedium
                            ?.copyWith(
                              color: context.viberColors.verified,
                              fontWeight: FontWeight.w600,
                            ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  if (constraints.maxWidth >= 560)
                    Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Expanded(child: request),
                        const SizedBox(width: 8),
                        Expanded(child: response),
                      ],
                    )
                  else ...[
                    request,
                    const SizedBox(height: 6),
                    response,
                  ],
                ],
              );
            },
          ),
        ),
      ),
    );
  }
}

final class _ResultDiff extends StatelessWidget {
  const _ResultDiff({
    required this.title,
    required this.before,
    required this.after,
    required this.unchangedLabel,
    super.key,
  });

  final String title;
  final List<String> before;
  final List<String> after;
  final String unchangedLabel;

  @override
  Widget build(BuildContext context) {
    final lines = _diffLines(before, after);
    return DecoratedBox(
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(7, 6, 7, 7),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              title,
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: context.viberColors.textMuted,
                fontWeight: FontWeight.w600,
              ),
            ),
            const SizedBox(height: 4),
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: SelectionArea(
                child: Text.rich(
                  TextSpan(
                    children: lines.isEmpty
                        ? [TextSpan(text: '  $unchangedLabel')]
                        : [
                            for (final line in lines)
                              TextSpan(
                                text: '${line.marker} ${line.text}\n',
                                style: TextStyle(
                                  color: switch (line.marker) {
                                    '-' => context.viberColors.danger,
                                    '+' => context.viberColors.verified,
                                    _ => context.viberColors.textMuted,
                                  },
                                  backgroundColor: switch (line.marker) {
                                    '-' =>
                                      context.viberColors.danger.withValues(
                                        alpha: 0.08,
                                      ),
                                    '+' =>
                                      context.viberColors.verified.withValues(
                                        alpha: 0.08,
                                      ),
                                    _ => null,
                                  },
                                ),
                              ),
                          ],
                  ),
                  style: const TextStyle(
                    fontFamily: 'Menlo',
                    fontSize: 10.5,
                    height: 1.35,
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

List<String> _requestSnapshot(MessageTransformTestRequest request) => [
  '${request.method} ${request.path}',
  ..._headerLines(request.headers),
  '',
  ..._bodyLines(request.body),
];

List<String> _responseSnapshot(MessageTransformTestResponse response) => [
  'HTTP ${response.statusCode}',
  ..._headerLines(response.headers),
  '',
  ..._bodyLines(response.body),
];

List<String> _headerLines(Map<String, List<String>> headers) {
  final names = headers.keys.toList()..sort();
  return [for (final name in names) '$name: ${headers[name]!.join(', ')}'];
}

List<String> _bodyLines(String body) {
  try {
    return const JsonEncoder.withIndent(
      '  ',
    ).convert(jsonDecode(body)).split('\n');
  } on FormatException {
    return body.split('\n');
  }
}

List<({String marker, String text})> _diffLines(
  List<String> before,
  List<String> after,
) {
  if (before.length == after.length &&
      Iterable<int>.generate(
        before.length,
      ).every((index) => before[index] == after[index])) {
    return const [];
  }
  var prefix = 0;
  while (prefix < before.length &&
      prefix < after.length &&
      before[prefix] == after[prefix]) {
    prefix++;
  }
  var suffix = 0;
  while (suffix < before.length - prefix &&
      suffix < after.length - prefix &&
      before[before.length - suffix - 1] == after[after.length - suffix - 1]) {
    suffix++;
  }
  return [
    for (final line in before.take(prefix)) (marker: ' ', text: line),
    for (final line
        in before.skip(prefix).take(before.length - prefix - suffix))
      (marker: '-', text: line),
    for (final line in after.skip(prefix).take(after.length - prefix - suffix))
      (marker: '+', text: line),
    for (final line in before.skip(before.length - suffix))
      (marker: ' ', text: line),
  ];
}

String _protocolLabel(AppCopy copy, String protocol) => switch (protocol) {
  'anthropic_messages' => 'Anthropic Messages',
  'openai_responses' => 'OpenAI Responses',
  'openai_chat' => 'OpenAI Chat',
  _ => protocol,
};

final _javaScriptTokens = RegExp(
  r'''//[^\n]*|/\*[\s\S]*?\*/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`|\b(?:const|let|var|if|else|for|while|return|throw|new|delete|true|false|null|undefined)\b|\b(?:request|response|context|runtime|accounts|selection|JSON)\b''',
  multiLine: true,
);

TextSpan javaScriptTextSpan(
  BuildContext context,
  String source, {
  TextStyle? style,
}) {
  final palette = context.viberColors;
  final spans = <InlineSpan>[];
  var offset = 0;
  for (final match in _javaScriptTokens.allMatches(source)) {
    if (match.start > offset) {
      spans.add(TextSpan(text: source.substring(offset, match.start)));
    }
    final token = match.group(0)!;
    final tokenStyle = token.startsWith('//') || token.startsWith('/*')
        ? TextStyle(color: palette.textFaint, fontStyle: FontStyle.italic)
        : token.startsWith('"') ||
              token.startsWith("'") ||
              token.startsWith('`')
        ? TextStyle(color: palette.verified)
        : const {
            'request',
            'response',
            'context',
            'runtime',
            'accounts',
            'selection',
            'JSON',
          }.contains(token)
        ? TextStyle(color: palette.route, fontWeight: FontWeight.w600)
        : TextStyle(color: palette.warning, fontWeight: FontWeight.w600);
    spans.add(TextSpan(text: token, style: tokenStyle));
    offset = match.end;
  }
  if (offset < source.length) {
    spans.add(TextSpan(text: source.substring(offset)));
  }
  return TextSpan(style: style, children: spans);
}

final class JavaScriptEditingController extends TextEditingController {
  JavaScriptEditingController({super.text});

  void insertSnippet(String snippet) {
    final current = value;
    final selection = current.selection.isValid
        ? current.selection
        : TextSelection.collapsed(offset: current.text.length);
    final replacement =
        selection.start > 0 &&
            !RegExp(r'\s').hasMatch(current.text[selection.start - 1])
        ? ' $snippet'
        : snippet;
    final next = current.text.replaceRange(
      selection.start,
      selection.end,
      replacement,
    );
    value = current.copyWith(
      text: next,
      selection: TextSelection.collapsed(
        offset: selection.start + replacement.length,
      ),
      composing: TextRange.empty,
    );
  }

  @override
  TextSpan buildTextSpan({
    required BuildContext context,
    TextStyle? style,
    required bool withComposing,
  }) => javaScriptTextSpan(context, text, style: style);
}

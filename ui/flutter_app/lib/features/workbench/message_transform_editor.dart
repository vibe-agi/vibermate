import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

typedef MessageTransformTestCallback =
    Future<MessageTransformTestResult> Function({
      required String clientProtocol,
      required TrafficTransformPolicy policy,
    });

/// Edits the message transform owned by exactly one client protocol path.
///
/// The caller owns Environment revision changes. This widget owns only the
/// bounded authoring interaction and one static request/response test Turn.
final class MessageTransformEditorButton extends StatelessWidget {
  const MessageTransformEditorButton({
    required this.plan,
    required this.copy,
    required this.enabled,
    required this.testTransform,
    required this.onChanged,
    super.key,
  });

  final EnvironmentProtocolPlan plan;
  final AppCopy copy;
  final bool enabled;
  final MessageTransformTestCallback testTransform;
  final ValueChanged<TrafficTransformPolicy> onChanged;

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
                _summary(copy, plan.transformPolicy),
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
    final selection = await showDialog<TrafficTransformPolicy>(
      context: context,
      barrierDismissible: true,
      builder: (context) => MessageTransformEditorDialog(
        planId: plan.id,
        clientProtocol: plan.clientProtocol,
        initial: plan.transformPolicy,
        copy: copy,
        testTransform: testTransform,
      ),
    );
    if (selection != null) onChanged(selection);
  }

  static String _summary(AppCopy copy, TrafficTransformPolicy policy) {
    final request = policy.requestJavaScript.isNotEmpty;
    final response = policy.responseJavaScript.isNotEmpty;
    if (request && response) return copy('environment.transform.summary.both');
    if (request) return copy('environment.transform.summary.request');
    if (response) return copy('environment.transform.summary.response');
    return copy('environment.transform.summary.off');
  }
}

/// One bounded editor for the request and response halves of a single Turn.
final class MessageTransformEditorDialog extends StatefulWidget {
  const MessageTransformEditorDialog({
    required this.planId,
    required this.clientProtocol,
    required this.initial,
    required this.copy,
    required this.testTransform,
    super.key,
  });

  final String planId;
  final String clientProtocol;
  final TrafficTransformPolicy initial;
  final AppCopy copy;
  final MessageTransformTestCallback testTransform;

  @override
  State<MessageTransformEditorDialog> createState() =>
      _MessageTransformEditorDialogState();
}

final class _MessageTransformEditorDialogState
    extends State<MessageTransformEditorDialog>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs;
  late final _JavaScriptEditingController _request;
  late final _JavaScriptEditingController _response;
  MessageTransformTestResult? _result;
  bool _testing = false;
  String? _errorKey;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _tabs = TabController(length: 2, vsync: this);
    _request = _JavaScriptEditingController(
      text: widget.initial.requestJavaScript,
    );
    _response = _JavaScriptEditingController(
      text: widget.initial.responseJavaScript,
    );
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
    final viewport = MediaQuery.sizeOf(context);
    final width = math.max(280.0, math.min(860.0, viewport.width - 48));
    final height = math.max(420.0, math.min(700.0, viewport.height - 48));
    return Dialog(
      insetPadding: const EdgeInsets.all(24),
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        key: Key('environment-transform-dialog-${widget.planId}'),
        width: width,
        height: height,
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
                      'JSON.parse(response.body)',
                    ],
                  ),
                ],
              ),
            ),
            if (_result case final result?)
              _ResultPanel(result: result, copy: copy),
            if (_errorKey case final errorKey?)
              Container(
                key: Key('environment-transform-error-${widget.planId}'),
                color: context.viberColors.danger.withValues(alpha: 0.10),
                padding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 7,
                ),
                child: Text(
                  copy(errorKey),
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
                  copy('environment.transform.dialog.title'),
                  style: Theme.of(context).textTheme.titleLarge,
                ),
                Text(
                  _protocolLabel(copy, widget.clientProtocol),
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
            ),
          ),
          IconButton(
            key: Key('environment-transform-close-${widget.planId}'),
            tooltip: copy('common.close'),
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
        ],
      ),
    );
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
            child: Text(copy('common.save')),
          ),
        ],
      ),
    );
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
        _errorKey = 'environment.transform.invalid';
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
      _errorKey = null;
    });
    try {
      final result = await widget.testTransform(
        clientProtocol: widget.clientProtocol,
        policy: policy,
      );
      if (!mounted) return;
      setState(() => _result = result);
    } on Object {
      if (!mounted) return;
      setState(() => _errorKey = 'environment.transform.test.failed');
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }

  void _save() {
    final policy = _policy();
    if (policy != null) Navigator.of(context).pop(policy);
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
  final _JavaScriptEditingController controller;
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
    return Container(
      key: const Key('environment-transform-test-result'),
      constraints: const BoxConstraints(maxHeight: 154),
      decoration: BoxDecoration(
        color: context.viberColors.verified.withValues(alpha: 0.07),
        border: Border(top: BorderSide(color: context.viberColors.divider)),
      ),
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
        child: LayoutBuilder(
          builder: (context, constraints) {
            final request = _ResultMessage(
              title: '${result.request.method} ${result.request.path}',
              headers: result.request.headers,
              body: result.request.body,
            );
            final response = _ResultMessage(
              title: '${result.response.statusCode}',
              headers: result.response.headers,
              body: result.response.body,
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
                      style: Theme.of(context).textTheme.labelMedium?.copyWith(
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
    );
  }
}

final class _ResultMessage extends StatelessWidget {
  const _ResultMessage({
    required this.title,
    required this.headers,
    required this.body,
  });

  final String title;
  final Map<String, List<String>> headers;
  final String body;

  @override
  Widget build(BuildContext context) {
    final headerText = headers.entries
        .map((entry) => '${entry.key}: ${entry.value.join(', ')}')
        .join('\n');
    return DecoratedBox(
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Padding(
        padding: const EdgeInsets.all(7),
        child: SelectionArea(
          child: Text(
            '$title\n$headerText\n$body',
            style: const TextStyle(
              fontFamily: 'Menlo',
              fontSize: 10.5,
              height: 1.35,
            ),
          ),
        ),
      ),
    );
  }
}

String _protocolLabel(AppCopy copy, String protocol) => switch (protocol) {
  'anthropic_messages' => 'Anthropic Messages',
  'openai_responses' => 'OpenAI Responses',
  'openai_chat' => 'OpenAI Chat',
  _ => protocol,
};

final class _JavaScriptEditingController extends TextEditingController {
  _JavaScriptEditingController({super.text});

  static final _tokens = RegExp(
    r'''//[^\n]*|/\*[\s\S]*?\*/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`|\b(?:const|let|var|if|else|for|while|return|throw|new|delete|true|false|null|undefined)\b|\b(?:request|response|context|JSON)\b''',
    multiLine: true,
  );

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
  }) {
    final palette = context.viberColors;
    final spans = <InlineSpan>[];
    var offset = 0;
    for (final match in _tokens.allMatches(text)) {
      if (match.start > offset) {
        spans.add(TextSpan(text: text.substring(offset, match.start)));
      }
      final token = match.group(0)!;
      final tokenStyle = token.startsWith('//') || token.startsWith('/*')
          ? TextStyle(color: palette.textFaint, fontStyle: FontStyle.italic)
          : token.startsWith('"') ||
                token.startsWith("'") ||
                token.startsWith('`')
          ? TextStyle(color: palette.verified)
          : const {'request', 'response', 'context', 'JSON'}.contains(token)
          ? TextStyle(color: palette.route, fontWeight: FontWeight.w600)
          : TextStyle(color: palette.warning, fontWeight: FontWeight.w600);
      spans.add(TextSpan(text: token, style: tokenStyle));
      offset = match.end;
    }
    if (offset < text.length) spans.add(TextSpan(text: text.substring(offset)));
    return TextSpan(style: style, children: spans);
  }
}

import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api/control_failure.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'control_failure_notice.dart';
import 'conversation_timeline.dart';
import 'workbench_controller.dart';

Future<void> showEvidenceSearchDialog(
  BuildContext context, {
  required WorkbenchController controller,
  required AppCopy copy,
}) => showDialog<void>(
  context: context,
  builder: (_) => _EvidenceSearchDialog(controller: controller, copy: copy),
);

final class _EvidenceSearchDialog extends StatefulWidget {
  const _EvidenceSearchDialog({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_EvidenceSearchDialog> createState() => _EvidenceSearchDialogState();
}

final class _EvidenceSearchDialogState extends State<_EvidenceSearchDialog> {
  final _query = TextEditingController();
  final _environment = TextEditingController();
  final _account = TextEditingController();
  final _model = TextEditingController();
  final _tool = TextEditingController();
  final _reason = TextEditingController();
  String _status = '';
  int _days = 0;
  bool _advanced = false;
  bool _loading = false;
  bool _searched = false;
  int _generation = 0;
  EvidenceSearchRequest? _request;
  EvidenceSearchPage? _page;
  ControlFailure? _failure;

  @override
  void dispose() {
    _query.dispose();
    _environment.dispose();
    _account.dispose();
    _model.dispose();
    _tool.dispose();
    _reason.dispose();
    super.dispose();
  }

  bool get _hasFilter =>
      _query.text.trim().isNotEmpty ||
      _environment.text.trim().isNotEmpty ||
      _account.text.trim().isNotEmpty ||
      _model.text.trim().isNotEmpty ||
      _tool.text.trim().isNotEmpty ||
      _reason.text.trim().isNotEmpty ||
      _status.isNotEmpty ||
      _days != 0;

  EvidenceSearchRequest _buildRequest() {
    final until = _days == 0
        ? null
        : DateTime.now().toUtc().add(const Duration(seconds: 1));
    return EvidenceSearchRequest(
      query: _query.text.trim(),
      environmentId: _environment.text.trim(),
      accountId: _account.text.trim(),
      model: _model.text.trim(),
      tool: _tool.text.trim(),
      status: _status,
      reason: _reason.text.trim(),
      from: until?.subtract(Duration(days: _days)),
      until: until,
    );
  }

  Future<void> _search({bool append = false}) async {
    final base = append ? _request : _buildRequest();
    final cursor = append ? _page?.nextCursor : null;
    if (base == null || (append && cursor == null)) return;
    final request = cursor == null ? base : base.next(cursor);
    if (!request.valid || _loading) return;
    final generation = ++_generation;
    setState(() {
      _loading = true;
      _failure = null;
      if (!append) {
        _searched = true;
        _page = null;
      }
    });
    try {
      final result = await widget.controller.searchEvidence(request);
      if (!mounted || generation != _generation) return;
      setState(() {
        _request = base;
        _page = append
            ? EvidenceSearchPage(
                items: [...?_page?.items, ...result.items],
                nextCursor: result.nextCursor,
              )
            : result;
        _loading = false;
      });
    } on Object catch (error) {
      if (!mounted || generation != _generation) return;
      setState(() {
        _failure = ControlFailure.from(error);
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final viewport = MediaQuery.sizeOf(context);
    return AlertDialog(
      title: Text(copy('evidence_search.title')),
      content: SizedBox(
        width: ViberMetrics.dialogWideWidth,
        height: viewport.height * 0.72,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              copy('evidence_search.boundary'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: ViberSpacing.md),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    key: const Key('evidence-search-query'),
                    controller: _query,
                    autofocus: true,
                    textInputAction: TextInputAction.search,
                    onChanged: (_) => setState(() {}),
                    onSubmitted: (_) => unawaited(_search()),
                    decoration: InputDecoration(
                      hintText: copy('evidence_search.query'),
                      prefixIcon: const Icon(Icons.search, size: 18),
                    ),
                  ),
                ),
                const SizedBox(width: ViberSpacing.sm),
                FilledButton.icon(
                  key: const Key('evidence-search-submit'),
                  onPressed: !_hasFilter || _loading
                      ? null
                      : () => unawaited(_search()),
                  icon: const Icon(Icons.search, size: 15),
                  label: Text(copy('evidence_search.search')),
                ),
              ],
            ),
            const SizedBox(height: ViberSpacing.sm),
            ResponsiveFormGrid(
              children: [
                CompactLabeledControl(
                  label: copy('evidence_search.status'),
                  child: CompactSelectField<String>(
                    key: const Key('evidence-search-status'),
                    initialValue: _status,
                    items: [
                      DropdownMenuItem(
                        value: '',
                        child: Text(copy('evidence_search.status.any')),
                      ),
                      for (final value in const [
                        'succeeded',
                        'pending',
                        'failed',
                        'canceled',
                      ])
                        DropdownMenuItem(
                          value: value,
                          child: Text(copy('activity.status.$value')),
                        ),
                    ],
                    onChanged: (value) => setState(() => _status = value ?? ''),
                  ),
                ),
                CompactLabeledControl(
                  label: copy('evidence_search.period'),
                  child: CompactSelectField<int>(
                    key: const Key('evidence-search-period'),
                    initialValue: _days,
                    items: [
                      for (final value in const [0, 1, 7, 30])
                        DropdownMenuItem(
                          value: value,
                          child: Text(
                            copy(switch (value) {
                              1 => 'evidence_search.period.day',
                              7 => 'evidence_search.period.week',
                              30 => 'evidence_search.period.month',
                              _ => 'evidence_search.period.all',
                            }),
                          ),
                        ),
                    ],
                    onChanged: (value) => setState(() => _days = value ?? 0),
                  ),
                ),
              ],
            ),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: const Key('evidence-search-more-filters'),
                onPressed: () => setState(() => _advanced = !_advanced),
                icon: Icon(
                  _advanced ? Icons.expand_less : Icons.tune,
                  size: 15,
                ),
                label: Text(
                  copy(
                    _advanced
                        ? 'evidence_search.filters.hide'
                        : 'evidence_search.filters.show',
                  ),
                ),
              ),
            ),
            if (_advanced) ...[
              SizedBox(
                height: viewport.height * 0.16,
                child: SingleChildScrollView(
                  child: ResponsiveFormGrid(
                    children: [
                      _filterField('environment', _environment),
                      _filterField('account', _account),
                      _filterField('model', _model),
                      _filterField('tool', _tool),
                      _filterField('reason', _reason),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: ViberSpacing.sm),
            ],
            const Divider(height: 1),
            if (_failure case final failure?)
              ControlFailureNotice(
                message: failure.messageKey,
                diagnostic: failure.diagnostic,
                copy: copy,
              ),
            Expanded(child: _results()),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: Text(copy('common.dismiss')),
        ),
      ],
    );
  }

  Widget _filterField(String key, TextEditingController controller) =>
      CompactLabeledControl(
        label: widget.copy('evidence_search.$key'),
        child: TextField(
          key: Key('evidence-search-$key'),
          controller: controller,
          onChanged: (_) => setState(() {}),
          onSubmitted: (_) => unawaited(_search()),
        ),
      );

  Widget _results() {
    final copy = widget.copy;
    final page = _page;
    if (_loading && page == null) {
      return CompactLoadingMessage(label: copy('common.loading'));
    }
    if (!_searched) {
      return CenteredMessage(
        icon: Icons.manage_search,
        title: copy('evidence_search.prompt'),
      );
    }
    if (page == null || page.items.isEmpty) {
      return CenteredMessage(
        icon: Icons.search_off,
        title: copy('evidence_search.empty'),
      );
    }
    return ListView.separated(
      key: const Key('evidence-search-results'),
      padding: const EdgeInsets.symmetric(vertical: ViberSpacing.sm),
      itemCount: page.items.length + (page.nextCursor == null ? 0 : 1),
      separatorBuilder: (_, _) => const Divider(height: 1),
      itemBuilder: (context, index) {
        if (index == page.items.length) {
          return Center(
            child: TextButton.icon(
              key: const Key('evidence-search-load-more'),
              onPressed: _loading
                  ? null
                  : () => unawaited(_search(append: true)),
              icon: _loading
                  ? const CompactProgressIndicator()
                  : const Icon(Icons.expand_more, size: 15),
              label: Text(copy('evidence_search.load_more')),
            ),
          );
        }
        final hit = page.items[index];
        return _EvidenceSearchRow(
          key: Key('evidence-search-result-${hit.activity.id}'),
          hit: hit,
          copy: copy,
          onTap: () => _openResult(hit),
        );
      },
    );
  }

  void _openResult(EvidenceSearchHit hit) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (dialogContext) => AlertDialog(
          title: Text(widget.copy('evidence_search.result')),
          content: SizedBox(
            width: ViberMetrics.dialogWideWidth,
            height: MediaQuery.sizeOf(dialogContext).height * 0.65,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                SelectableText(
                  '${hit.context.captureLabel}  ·  ${hit.activity.conversation.displayName ?? hit.activity.conversation.id}\n${hit.activity.id}',
                  style: monoStyle,
                ),
                const SizedBox(height: ViberSpacing.sm),
                Expanded(
                  child: EvidenceConversationTimeline(
                    controller: widget.controller,
                    activities: [hit.activity],
                    copy: widget.copy,
                    exchangeScoped: true,
                    showCount: false,
                    title: widget.copy('evidence_search.result'),
                  ),
                ),
              ],
            ),
          ),
          actions: [
            TextButton(
              key: const Key('evidence-search-open-capture'),
              onPressed: () {
                final captureKey = hit.activity.captureRunId == null
                    ? 'manual_capture:${hit.activity.manualCaptureId}'
                    : 'managed_run:${hit.activity.captureRunId}';
                Navigator.of(dialogContext).pop();
                Navigator.of(context).pop();
                unawaited(widget.controller.selectCapture(captureKey));
              },
              child: Text(widget.copy('evidence_search.open_capture')),
            ),
            TextButton(
              onPressed: () => Navigator.of(dialogContext).pop(),
              child: Text(widget.copy('common.dismiss')),
            ),
          ],
        ),
      ),
    );
  }
}

final class _EvidenceSearchRow extends StatelessWidget {
  const _EvidenceSearchRow({
    required this.hit,
    required this.copy,
    required this.onTap,
    super.key,
  });

  final EvidenceSearchHit hit;
  final AppCopy copy;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final activity = hit.activity;
    final metadata = hit.context;
    final title = activity.requestPreview?.text ?? activity.title;
    final model = metadata.reportedModel.isNotEmpty
        ? metadata.reportedModel
        : metadata.effectiveModel;
    final scope = [
      metadata.workspaceLabel,
      metadata.captureLabel,
      activity.environmentId,
      activity.accountId ?? '',
    ].where((value) => value.isNotEmpty).join('  ·  ');
    final fields = hit.matches
        .map((value) => copy('evidence_search.match.$value'))
        .join(' · ');
    return Semantics(
      button: true,
      label: '$title, $scope, $fields',
      child: InkWell(
        onTap: onTap,
        canRequestFocus: true,
        child: Padding(
          padding: const EdgeInsets.symmetric(
            horizontal: ViberSpacing.sm,
            vertical: ViberSpacing.md,
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      title,
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                  ),
                  const SizedBox(width: ViberSpacing.sm),
                  Text(
                    _searchTimestamp(activity.occurredAt),
                    style: monoStyle.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: ViberSpacing.xs),
              Text(
                scope,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall,
              ),
              if (model.isNotEmpty || metadata.toolNames.isNotEmpty) ...[
                const SizedBox(height: ViberSpacing.xs),
                Text(
                  [
                    model,
                    ...metadata.toolNames,
                  ].where((value) => value.isNotEmpty).join('  ·  '),
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: monoStyle,
                ),
              ] else if (!metadata.contentAvailable) ...[
                const SizedBox(height: ViberSpacing.xs),
                Text(
                  copy('evidence_search.content_unavailable'),
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ],
              const SizedBox(height: ViberSpacing.xs),
              Text(
                copy.format('evidence_search.matches', {'fields': fields}),
                style: Theme.of(context).textTheme.labelSmall?.copyWith(
                  color: context.viberColors.route,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

String _searchTimestamp(DateTime value) {
  final local = value.toLocal();
  String two(int number) => number.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} ${two(local.hour)}:${two(local.minute)}';
}

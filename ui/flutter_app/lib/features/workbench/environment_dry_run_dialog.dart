import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_failure.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'control_failure_notice.dart';
import 'workbench_controller.dart';

final class EnvironmentDryRunDialog extends StatefulWidget {
  const EnvironmentDryRunDialog({
    required this.controller,
    required this.environment,
    required this.copy,
    this.reviewedDraft,
    this.publishedAvailable = true,
    super.key,
  });

  final WorkbenchController controller;
  final EnvironmentRecord environment;
  final EnvironmentDraft? reviewedDraft;
  final bool publishedAvailable;
  final AppCopy copy;

  @override
  State<EnvironmentDryRunDialog> createState() =>
      _EnvironmentDryRunDialogState();
}

final class _EnvironmentDryRunDialogState
    extends State<EnvironmentDryRunDialog> {
  final _path = TextEditingController();
  final _body = TextEditingController();
  EnvironmentDraft? _draft;
  EnvironmentDryRun? _result;
  ControlFailure? _failure;
  String? _inputError;
  late String _source;
  int _flowIndex = 0;
  bool _running = false;

  EnvironmentRecord get _selectedEnvironment =>
      _source == 'draft' && _draft != null
      ? _draft!.candidate
      : widget.environment;

  List<(EnvironmentClientEndpoint, EnvironmentProtocolPlan)> get _flows => [
    for (final endpoint in _selectedEnvironment.clientEndpoints)
      for (final plan in endpoint.protocolPlans) (endpoint, plan),
  ];

  @override
  void initState() {
    super.initState();
    _draft = widget.reviewedDraft;
    _source = _draft == null ? 'published' : 'draft';
    _seedSample();
    if (_draft == null &&
        widget.publishedAvailable &&
        !widget.environment.systemOwned) {
      unawaited(_loadDraft());
    }
  }

  @override
  void dispose() {
    _path.dispose();
    _body.dispose();
    super.dispose();
  }

  Future<void> _loadDraft() async {
    try {
      final draft = await widget.controller.environmentDraft(
        widget.environment.id,
      );
      if (!mounted || draft.baseRevision != widget.environment.revision) return;
      setState(() => _draft = draft);
    } on ControlProblem catch (problem) {
      if (problem.status != 404 ||
          problem.reasonCode != 'environment_draft_not_found') {
        if (mounted) setState(() => _failure = ControlFailure.from(problem));
      }
    } on Object catch (error) {
      if (mounted) setState(() => _failure = ControlFailure.from(error));
    }
  }

  void _seedSample() {
    final flows = _flows;
    if (flows.isEmpty) {
      _path.clear();
      _body.clear();
      return;
    }
    final plan = flows[_flowIndex].$2;
    final requestedModel =
        plan
            .routes
            .firstOrNull
            ?.modelPolicy
            .mappings
            .firstOrNull
            ?.requestedModel ??
        'example-model';
    switch (plan.clientProtocol) {
      case 'anthropic_messages':
        _path.text = '/v1/messages';
        _body.text = jsonEncode({
          'model': requestedModel,
          'max_tokens': 16,
          'messages': [
            {'role': 'user', 'content': 'Synthetic test'},
          ],
        });
      case 'openai_responses':
        _path.text = '/v1/responses';
        _body.text = jsonEncode({
          'model': requestedModel,
          'input': 'Synthetic test',
        });
      case 'openai_chat':
        _path.text = '/v1/chat/completions';
        _body.text = jsonEncode({
          'model': requestedModel,
          'messages': [
            {'role': 'user', 'content': 'Synthetic test'},
          ],
        });
    }
  }

  void _changeSource(String source) {
    setState(() {
      _source = source;
      _flowIndex = 0;
      _result = null;
      _failure = null;
      _inputError = null;
      _seedSample();
    });
  }

  Future<void> _run() async {
    final flows = _flows;
    if (_running || flows.isEmpty) return;
    try {
      if (jsonDecode(_body.text) is! Map<String, dynamic>) {
        throw const FormatException('request is not an object');
      }
    } on FormatException {
      setState(
        () => _inputError = widget.copy('environment.dry_run.invalid_json'),
      );
      return;
    }
    final version = _source == 'draft'
        ? _draft!.draftRevision
        : widget.environment.revision;
    final typedPath = _path.text.trim();
    final queryAt = typedPath.indexOf('?');
    final input = EnvironmentDryRunInput(
      environmentId: widget.environment.id,
      source: _source,
      revision: version,
      clientOrigin: flows[_flowIndex].$1.clientOrigin.toString(),
      method: 'POST',
      path: queryAt < 0 ? typedPath : typedPath.substring(0, queryAt),
      rawQuery: queryAt < 0 ? '' : typedPath.substring(queryAt + 1),
      clientProtocol: 'http/1.1',
      body: _body.text,
    );
    setState(() {
      _running = true;
      _result = null;
      _failure = null;
      _inputError = null;
    });
    try {
      final result = await widget.controller.dryRunEnvironment(input);
      if (mounted) setState(() => _result = result);
    } on Object catch (error) {
      if (mounted) setState(() => _failure = ControlFailure.from(error));
    } finally {
      if (mounted) setState(() => _running = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final flows = _flows;
    final width = MediaQuery.sizeOf(context).width;
    return Dialog(
      key: const Key('environment-dry-run-dialog'),
      insetPadding: const EdgeInsets.all(12),
      child: ConstrainedBox(
        constraints: BoxConstraints(
          maxWidth: 760,
          maxHeight: MediaQuery.sizeOf(context).height - 24,
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(18, 14, 10, 12),
              child: Row(
                children: [
                  Icon(
                    Icons.route_outlined,
                    size: 20,
                    color: context.viberColors.route,
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Text(
                      copy('environment.dry_run.title'),
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                  ),
                  IconButton(
                    tooltip: copy('common.dismiss'),
                    onPressed: _running ? null : () => Navigator.pop(context),
                    icon: const Icon(Icons.close, size: 19),
                  ),
                ],
              ),
            ),
            const Divider(height: 1),
            Flexible(
              child: SingleChildScrollView(
                padding: const EdgeInsets.all(18),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Text(
                      copy('environment.dry_run.intro'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    const SizedBox(height: 14),
                    LayoutBuilder(
                      builder: (context, constraints) {
                        final fields = <Widget>[
                          CompactLabeledControl(
                            label: copy('environment.dry_run.source'),
                            child: DropdownButtonFormField<String>(
                              key: ValueKey(
                                'environment-dry-run-source-$_source',
                              ),
                              initialValue: _source,
                              isExpanded: true,
                              items: [
                                if (widget.publishedAvailable)
                                  DropdownMenuItem(
                                    value: 'published',
                                    child: Text(
                                      copy.format(
                                        'environment.dry_run.published',
                                        {
                                          'revision':
                                              widget.environment.revision,
                                        },
                                      ),
                                    ),
                                  ),
                                if (_draft case final draft?)
                                  DropdownMenuItem(
                                    value: 'draft',
                                    child: Text(
                                      copy.format('environment.dry_run.draft', {
                                        'revision': draft.draftRevision,
                                      }),
                                    ),
                                  ),
                              ],
                              onChanged: _running
                                  ? null
                                  : (value) {
                                      if (value != null) _changeSource(value);
                                    },
                            ),
                          ),
                          CompactLabeledControl(
                            label: copy('environment.dry_run.flow'),
                            child: DropdownButtonFormField<int>(
                              key: ValueKey(
                                'environment-dry-run-flow-$_source-$_flowIndex',
                              ),
                              initialValue: flows.isEmpty ? null : _flowIndex,
                              isExpanded: true,
                              items: [
                                for (final (index, flow) in flows.indexed)
                                  DropdownMenuItem(
                                    value: index,
                                    child: Text(
                                      '${flow.$1.clientOrigin.host} · ${copy.maybe('routes.protocol.${flow.$2.clientProtocol}') ?? flow.$2.clientProtocol}',
                                      maxLines: 1,
                                      overflow: TextOverflow.ellipsis,
                                    ),
                                  ),
                              ],
                              onChanged: _running
                                  ? null
                                  : (value) {
                                      if (value == null) return;
                                      setState(() {
                                        _flowIndex = value;
                                        _result = null;
                                        _failure = null;
                                        _seedSample();
                                      });
                                    },
                            ),
                          ),
                        ];
                        if (constraints.maxWidth < 560) {
                          return Column(
                            children: [
                              fields[0],
                              const SizedBox(height: 10),
                              fields[1],
                            ],
                          );
                        }
                        return Row(
                          children: [
                            Expanded(child: fields[0]),
                            const SizedBox(width: 12),
                            Expanded(child: fields[1]),
                          ],
                        );
                      },
                    ),
                    if (flows.isEmpty) ...[
                      const SizedBox(height: 14),
                      Text(copy('environment.dry_run.no_flow')),
                    ] else ...[
                      const SizedBox(height: 12),
                      CompactLabeledControl(
                        label: copy('environment.dry_run.path'),
                        child: TextField(
                          key: const Key('environment-dry-run-path'),
                          controller: _path,
                          enabled: !_running,
                          autocorrect: false,
                          onChanged: (_) => setState(() => _result = null),
                        ),
                      ),
                      const SizedBox(height: 12),
                      CompactLabeledControl(
                        label: copy('environment.dry_run.body'),
                        detail: copy('environment.dry_run.body_help'),
                        child: TextField(
                          key: const Key('environment-dry-run-body'),
                          controller: _body,
                          enabled: !_running,
                          minLines: width < 500 ? 3 : 6,
                          maxLines: 10,
                          style: monoStyle,
                          autocorrect: false,
                          enableSuggestions: false,
                          onChanged: (_) => setState(() => _result = null),
                        ),
                      ),
                    ],
                    if (_inputError case final error?) ...[
                      const SizedBox(height: 10),
                      InlineNotice(message: error, error: true),
                    ],
                    if (_failure case final failure?) ...[
                      const SizedBox(height: 10),
                      ControlFailureNotice(
                        message: failure.messageKey,
                        diagnostic: failure.diagnostic,
                        copy: copy,
                      ),
                    ],
                    if (_result case final result?) ...[
                      const SizedBox(height: 16),
                      _DryRunDecisionView(
                        result: result,
                        copy: copy,
                        environment: _selectedEnvironment,
                        endpoints:
                            widget.controller.data?.endpoints ?? const [],
                        accounts: widget.controller.data?.accounts ?? const [],
                        egressName:
                            flows[_flowIndex].$2.egressProfile.displayName,
                      ),
                    ],
                  ],
                ),
              ),
            ),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 9, 14, 10),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  TextButton(
                    onPressed: _running ? null : () => Navigator.pop(context),
                    child: Text(copy('common.cancel')),
                  ),
                  const SizedBox(width: 9),
                  FilledButton.icon(
                    key: const Key('environment-dry-run-run'),
                    onPressed: _running || flows.isEmpty ? null : _run,
                    icon: _running
                        ? const SizedBox.square(
                            dimension: 14,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          )
                        : const Icon(Icons.play_arrow_rounded, size: 17),
                    label: Text(copy('environment.dry_run.run')),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

final class _DryRunDecisionView extends StatelessWidget {
  const _DryRunDecisionView({
    required this.result,
    required this.copy,
    required this.environment,
    required this.endpoints,
    required this.accounts,
    required this.egressName,
  });

  final EnvironmentDryRun result;
  final AppCopy copy;
  final EnvironmentRecord environment;
  final List<UpstreamEndpoint> endpoints;
  final List<ProviderAccount> accounts;
  final String egressName;

  @override
  Widget build(BuildContext context) {
    final decision = result.decision;
    final route = environment.routes
        .where((item) => item.id == decision.routeId)
        .firstOrNull;
    final endpointName = endpoints
        .where((item) => item.id == route?.endpointId)
        .firstOrNull
        ?.displayName;
    final accountName = accounts
        .where((item) => item.id == decision.accountId)
        .firstOrNull
        ?.displayName;
    final changed = [
      ...decision.changedHeaderNames.map((name) => 'Header · $name'),
      ...?decision.changedTopLevelFields?.map((name) => 'JSON · $name'),
    ];
    return Container(
      key: const Key('environment-dry-run-result'),
      padding: const EdgeInsets.all(13),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Icon(
                Icons.account_tree_outlined,
                size: 17,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 7),
              Expanded(
                child: Text(
                  copy('environment.dry_run.result'),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ),
              StatusPill(
                label: 'r${decision.environmentRevision}',
                color: context.viberColors.route,
              ),
            ],
          ),
          if (result.source == 'draft') ...[
            const SizedBox(height: 5),
            Text(
              copy('environment.dry_run.draft_scope'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
          const SizedBox(height: 10),
          _DryRunFact(
            label: copy('environment.dry_run.destination'),
            value: decision.destinationKind == 'original'
                ? '${copy('environment.dry_run.original')} · ${decision.providerOrigin}'
                : decision.providerOrigin,
          ),
          _DryRunFact(
            label: copy('environment.dry_run.route'),
            value: decision.routeId.isEmpty
                ? copy('environment.dry_run.original')
                : endpointName == null
                ? decision.routeId
                : '$endpointName · ${decision.routeId}',
          ),
          _DryRunFact(
            label: copy('environment.dry_run.account'),
            value: decision.accountId.isEmpty
                ? copy('environment.dry_run.client_auth')
                : accountName == null
                ? decision.accountId
                : '$accountName · ${decision.accountId}',
          ),
          _DryRunFact(
            label: copy('environment.dry_run.model'),
            value: decision.effectiveModel.isEmpty
                ? copy('environment.dry_run.model_unavailable')
                : decision.modelMapped
                ? '${decision.requestedModel} → ${decision.effectiveModel}'
                : decision.effectiveModel,
          ),
          _DryRunFact(
            label: copy('environment.dry_run.egress'),
            value: egressName.isEmpty ? decision.networkExitId : egressName,
          ),
          const Divider(height: 16),
          Text(
            copy('environment.dry_run.changes'),
            style: Theme.of(context).textTheme.labelMedium,
          ),
          const SizedBox(height: 4),
          Text(
            changed.isNotEmpty
                ? changed.join(' · ')
                : decision.changedTopLevelFields == null
                ? copy('environment.dry_run.fields_unavailable')
                : decision.bodyChanged
                ? copy('environment.dry_run.body_changed')
                : copy('environment.dry_run.no_changes'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 9),
          Text(
            copy(
              decision.destinationKind == 'original'
                  ? 'environment.dry_run.not_verified_original'
                  : 'environment.dry_run.not_verified',
            ),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
        ],
      ),
    );
  }
}

final class _DryRunFact extends StatelessWidget {
  const _DryRunFact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 3),
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          width: 105,
          child: Text(label, style: Theme.of(context).textTheme.bodySmall),
        ),
        Expanded(
          child: SelectableText(
            value.isEmpty ? '—' : value,
            style: monoStyle.copyWith(color: context.viberColors.text),
          ),
        ),
      ],
    ),
  );
}

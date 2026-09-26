import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'raw_evidence_diff.dart';
import 'workbench_controller.dart';

Future<void> showRawEvidenceDiffDialog(
  BuildContext context, {
  required WorkbenchController controller,
  required String exchangeId,
  required RawEvidencePage page,
  required AppCopy copy,
}) => showDialog<void>(
  context: context,
  builder: (_) => _RawEvidenceDiffDialog(
    controller: controller,
    exchangeId: exchangeId,
    page: page,
    copy: copy,
  ),
);

final class _RawEvidenceDiffDialog extends StatefulWidget {
  const _RawEvidenceDiffDialog({
    required this.controller,
    required this.exchangeId,
    required this.page,
    required this.copy,
  });

  final WorkbenchController controller;
  final String exchangeId;
  final RawEvidencePage page;
  final AppCopy copy;

  @override
  State<_RawEvidenceDiffDialog> createState() => _RawEvidenceDiffDialogState();
}

final class _RawEvidenceDiffDialogState extends State<_RawEvidenceDiffDialog> {
  late final List<_RawEvidencePair> _pairs;
  late String _selected;
  int _generation = 0;
  bool _loading = false;
  String? _error;
  RawEvidenceComparison? _comparison;
  RevealedRawEvidence? _left;
  RevealedRawEvidence? _right;

  @override
  void initState() {
    super.initState();
    _pairs = _rawEvidencePairs(widget.page);
    _selected =
        _pairs.where((pair) => pair.complete).firstOrNull?.id ??
        _pairs.first.id;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(_load());
    });
  }

  @override
  void dispose() {
    _generation++;
    _clearBodies();
    super.dispose();
  }

  _RawEvidencePair get _pair =>
      _pairs.firstWhere((pair) => pair.id == _selected);

  void _clearBodies() {
    _left?.clearBody();
    _right?.clearBody();
    _left = null;
    _right = null;
  }

  Future<void> _load() async {
    final generation = ++_generation;
    final pair = _pair;
    _clearBodies();
    setState(() {
      _loading = true;
      _error = null;
      _comparison = null;
    });
    final leftEnvelope = pair.left;
    final rightEnvelope = pair.right;
    if (leftEnvelope == null || rightEnvelope == null) {
      setState(() {
        _loading = false;
        _comparison = _unavailableComparison(pair, 'missing_stage');
      });
      return;
    }
    if (!pair.complete) {
      setState(() {
        _loading = false;
        _comparison = _unavailableComparison(pair, 'incomplete_evidence');
      });
      return;
    }
    final revealed = await Future.wait([
      widget.controller.revealRawEvidence(
        exchangeId: widget.exchangeId,
        envelopeId: leftEnvelope.envelopeId,
      ),
      widget.controller.revealRawEvidence(
        exchangeId: widget.exchangeId,
        envelopeId: rightEnvelope.envelopeId,
      ),
    ]);
    if (!mounted || generation != _generation) {
      revealed[0]?.clearBody();
      revealed[1]?.clearBody();
      return;
    }
    final left = revealed[0];
    final right = revealed[1];
    if (left == null || right == null) {
      left?.clearBody();
      right?.clearBody();
      setState(() {
        _loading = false;
        _error = 'error.control_result_unknown';
      });
      return;
    }
    _left = left;
    _right = right;
    setState(() {
      _loading = false;
      _comparison = compareRawEvidence(left, right);
    });
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final pair = _pair;
    final comparison = _comparison;
    return AlertDialog(
      title: Text(copy('exchange.raw.diff.title')),
      content: SizedBox(
        key: const Key('raw-evidence-diff-dialog'),
        width: ViberMetrics.dialogWideWidth,
        height: MediaQuery.sizeOf(context).height * 0.68,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              copy('exchange.raw.diff.boundary'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: ViberSpacing.md),
            CompactSegmentedControl<String>(
              key: const Key('raw-evidence-diff-direction'),
              expanded: true,
              segments: [
                for (final candidate in _pairs)
                  CompactSegment(
                    value: candidate.id,
                    label: copy('exchange.raw.diff.${candidate.id}'),
                    icon: candidate.id == 'request'
                        ? Icons.upload_outlined
                        : Icons.download_outlined,
                  ),
              ],
              selected: _selected,
              onSelected: _loading
                  ? null
                  : (value) {
                      setState(() => _selected = value);
                      unawaited(_load());
                    },
            ),
            const SizedBox(height: ViberSpacing.md),
            ResponsiveFormGrid(
              children: [
                _stage(
                  pair.leftLabel,
                  pair.left,
                  comparison?.leftDecoded ?? false,
                ),
                _stage(
                  pair.rightLabel,
                  pair.right,
                  comparison?.rightDecoded ?? false,
                ),
              ],
            ),
            const SizedBox(height: ViberSpacing.md),
            const Divider(height: 1),
            if (_error case final error?)
              InlineNotice(message: copy(error), error: true),
            Expanded(
              child: _loading
                  ? CompactLoadingMessage(
                      label: copy('exchange.raw.diff.loading'),
                    )
                  : _comparisonBody(comparison),
            ),
          ],
        ),
      ),
      actions: [
        if (comparison != null)
          FilledButton.icon(
            key: const Key('raw-evidence-diff-copy'),
            onPressed: () => Clipboard.setData(
              ClipboardData(text: comparison.clipboardText()),
            ),
            icon: const Icon(Icons.copy, size: 15),
            label: Text(copy('exchange.raw.diff.copy')),
          ),
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: Text(copy('common.dismiss')),
        ),
      ],
    );
  }

  Widget _stage(String labelKey, RawEvidenceEnvelope? envelope, bool decoded) {
    final copy = widget.copy;
    final encoding = envelope?.contentEncoding?.trim().toLowerCase() ?? '';
    return CompactLabeledControl(
      label: copy(labelKey),
      child: Container(
        padding: const EdgeInsets.all(ViberSpacing.sm),
        decoration: BoxDecoration(
          color: context.viberColors.panelRaised,
          border: Border.all(color: context.viberColors.dividerSoft),
          borderRadius: ViberMetrics.controlRadius,
        ),
        child: Text(
          envelope == null
              ? copy('exchange.raw.diff.reason.missing_stage')
              : decoded
              ? copy.format('exchange.raw.diff.decoded', {'encoding': encoding})
              : copy('exchange.raw.diff.original'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ),
    );
  }

  Widget _comparisonBody(RawEvidenceComparison? comparison) {
    final copy = widget.copy;
    if (comparison == null) return const SizedBox.shrink();
    final state = comparison.state;
    if (state != RawEvidenceComparisonState.changed) {
      final key = switch (state) {
        RawEvidenceComparisonState.identical => 'identical',
        RawEvidenceComparisonState.incomplete => 'incomplete',
        RawEvidenceComparisonState.unsupported => 'unsupported',
        RawEvidenceComparisonState.tooLarge => 'too_large',
        RawEvidenceComparisonState.changed => 'identical',
      };
      final reason = comparison.reason;
      return CenteredMessage(
        icon: state == RawEvidenceComparisonState.identical
            ? Icons.check_circle_outline
            : Icons.info_outline,
        title: copy('exchange.raw.diff.$key'),
        detail: reason == null
            ? null
            : copy.maybe('exchange.raw.diff.reason.$reason'),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.symmetric(vertical: ViberSpacing.sm),
          child: Text(
            copy('exchange.raw.diff.mode.${comparison.mode!.name}'),
            style: Theme.of(context).textTheme.labelMedium,
          ),
        ),
        Expanded(
          child: ListView.separated(
            key: const Key('raw-evidence-diff-changes'),
            itemCount: comparison.changes.length,
            separatorBuilder: (_, _) => const SizedBox(height: ViberSpacing.sm),
            itemBuilder: (context, index) =>
                _change(comparison.changes[index], index),
          ),
        ),
      ],
    );
  }

  Widget _change(RawEvidenceChange change, int index) {
    final copy = widget.copy;
    final color = switch (change.kind) {
      RawEvidenceChangeKind.added => context.viberColors.verified,
      RawEvidenceChangeKind.removed => context.viberColors.danger,
      RawEvidenceChangeKind.changed => context.viberColors.warning,
    };
    return Container(
      key: Key('raw-evidence-diff-change-$index'),
      padding: const EdgeInsets.all(ViberSpacing.md),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border(left: BorderSide(color: color, width: 2)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Wrap(
            spacing: ViberSpacing.sm,
            runSpacing: ViberSpacing.xs,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              InlineStatus(
                label: copy('exchange.raw.diff.kind.${change.kind.name}'),
                color: color,
              ),
              SelectableText(change.path, style: monoStyle),
            ],
          ),
          const SizedBox(height: ViberSpacing.sm),
          ResponsiveFormGrid(
            children: [
              _value('exchange.raw.diff.before', change.before),
              _value('exchange.raw.diff.after', change.after),
            ],
          ),
        ],
      ),
    );
  }

  Widget _value(String labelKey, String? value) => CompactLabeledControl(
    label: widget.copy(labelKey),
    child: SelectableText(value ?? '—', style: monoStyle),
  );
}

final class _RawEvidencePair {
  const _RawEvidencePair({
    required this.id,
    required this.leftLabel,
    required this.rightLabel,
    required this.leftLayer,
    required this.rightLayer,
    required this.left,
    required this.right,
  });

  final String id;
  final String leftLabel;
  final String rightLabel;
  final String leftLayer;
  final String rightLayer;
  final RawEvidenceEnvelope? left;
  final RawEvidenceEnvelope? right;

  bool get complete => [left, right].every(
    (value) =>
        value != null &&
        value.payloadState == 'captured' &&
        value.digestScope == 'full_body' &&
        value.revealAvailable,
  );
}

List<_RawEvidencePair> _rawEvidencePairs(RawEvidencePage page) {
  RawEvidenceEnvelope? latest(String layer) => page.items.reversed
      .where((envelope) => envelope.layer == layer)
      .firstOrNull;
  return [
    _RawEvidencePair(
      id: 'request',
      leftLabel: 'exchange.raw.diff.client_request',
      rightLabel: 'exchange.raw.diff.provider_request',
      leftLayer: 'client_ingress',
      rightLayer: 'provider_egress',
      left: latest('client_ingress'),
      right: latest('provider_egress'),
    ),
    _RawEvidencePair(
      id: 'response',
      leftLabel: 'exchange.raw.diff.provider_response',
      rightLabel: 'exchange.raw.diff.client_response',
      leftLayer: 'provider_response',
      rightLayer: 'client_downstream',
      left: latest('provider_response'),
      right: latest('client_downstream'),
    ),
  ];
}

RawEvidenceComparison _unavailableComparison(
  _RawEvidencePair pair,
  String reason,
) => RawEvidenceComparison(
  state: RawEvidenceComparisonState.incomplete,
  mode: null,
  leftLayer: pair.left?.layer ?? pair.leftLayer,
  rightLayer: pair.right?.layer ?? pair.rightLayer,
  leftDecoded: false,
  rightDecoded: false,
  changes: const [],
  reason: reason,
);

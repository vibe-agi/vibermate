import 'package:flutter/material.dart';

import '../../core/api/acp_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// ACP evidence has its own units and provenance. It is deliberately not an
/// HTTP ConversationTimeline: prompt completion does not prove a model call.
final class ACPObservationView extends StatefulWidget {
  const ACPObservationView({
    required this.record,
    required this.copy,
    required this.running,
    super.key,
  });
  final ACPRecord record;
  final AppCopy copy;
  final bool running;
  @override
  State<ACPObservationView> createState() => _ACPObservationViewState();
}

final class _ACPObservationViewState extends State<ACPObservationView> {
  String? _session;
  @override
  void didUpdateWidget(ACPObservationView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.record.runId != widget.record.runId) _session = null;
  }

  @override
  Widget build(BuildContext context) {
    final record = widget.record, copy = widget.copy;
    final session =
        record.sessions.where((value) => value.id == _session).firstOrNull ??
        record.sessions.firstOrNull;
    final prompts = record.prompts
        .where((value) => value.sessionId == session?.id)
        .toList();
    return ListView(
      key: const Key('acp-observation-view'),
      padding: const EdgeInsets.all(16),
      children: [
        Wrap(
          spacing: 10,
          runSpacing: 8,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Icon(Icons.swap_horiz, color: context.viberColors.route),
            Text(
              copy('acp.transport'),
              style: Theme.of(context).textTheme.titleMedium,
            ),
            if (record.agentName.isNotEmpty)
              Text(
                '${record.agentName} ${record.agentVersion} · ACP v${record.protocolVersion}',
              ),
          ],
        ),
        const SizedBox(height: 8),
        Text(
          copy('acp.boundary'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        if (record.agentName.isNotEmpty) ...[
          const SizedBox(height: 4),
          Text(
            copy('acp.reported_identity'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
        ],
        const SizedBox(height: 12),
        if (record.needsAuthentication) ...[
          InlineNotice(message: copy('acp.login_required')),
          const SizedBox(height: 8),
        ],
        InlineNotice(
          message: copy(
            record.expired
                ? 'acp.expired'
                : record.mode == 'full'
                ? 'acp.content_recorded'
                : record.mode == 'off'
                ? 'acp.recording_off'
                : 'acp.metadata_only',
          ),
        ),
        if (record.incomplete || !record.finished && !widget.running) ...[
          const SizedBox(height: 8),
          InlineNotice(message: copy('acp.incomplete')),
        ],
        if (record.sessions.isEmpty) ...[
          const SizedBox(height: 24),
          Text(
            copy(
              record.expired
                  ? 'acp.expired'
                  : widget.running
                  ? 'acp.awaiting_session'
                  : 'acp.no_session',
            ),
          ),
        ] else ...[
          const SizedBox(height: 18),
          Text(
            copy('acp.session'),
            style: Theme.of(context).textTheme.labelLarge,
          ),
          const SizedBox(height: 6),
          KeyedSubtree(
            key: ValueKey('acp-session-${record.runId}'),
            child: DropdownButtonFormField<String>(
              key: const Key('acp-session-selector'),
              initialValue: session!.id,
              isExpanded: true,
              decoration: const InputDecoration(),
              items: [
                for (final value in record.sessions)
                  DropdownMenuItem(
                    value: value.id,
                    child: Text(
                      value.id,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
              ],
              onChanged: (value) => setState(() => _session = value),
            ),
          ),
          const SizedBox(height: 10),
          SelectableText(
            session.cwd,
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(fontFamily: 'monospace'),
          ),
          const SizedBox(height: 4),
          Text(
            copy('acp.workspace_claim'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
            ),
          ),
          const SizedBox(height: 16),
          if (prompts.isEmpty) Text(copy('acp.no_prompts')),
          for (final prompt in prompts) ...[
            _ACPPromptCard(
              prompt: prompt,
              copy: copy,
              showText: record.mode == 'full',
            ),
            const SizedBox(height: 10),
          ],
        ],
      ],
    );
  }
}

final class _ACPPromptCard extends StatelessWidget {
  const _ACPPromptCard({
    required this.prompt,
    required this.copy,
    required this.showText,
  });
  final ACPPrompt prompt;
  final AppCopy copy;
  final bool showText;
  @override
  Widget build(BuildContext context) {
    final state =
        {
          'pending',
          'completed',
          'cancelled',
          'failed',
          'interrupted',
        }.contains(prompt.state)
        ? prompt.state
        : 'interrupted';
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 12,
            runSpacing: 5,
            children: [
              Text(
                'Prompt ${prompt.sequence}',
                style: Theme.of(context).textTheme.titleSmall,
              ),
              Text(
                '${copy('acp.prompt.$state')}${prompt.stopReason.isEmpty ? '' : ' · ${prompt.stopReason}'}',
                style: TextStyle(
                  color: state == 'failed'
                      ? context.viberColors.danger
                      : context.viberColors.textMuted,
                ),
              ),
              if (prompt.toolCalls > 0)
                Text(
                  copy.format('acp.tool_calls', {'count': prompt.toolCalls}),
                ),
            ],
          ),
          if (showText && prompt.userText.isNotEmpty)
            _text(
              context,
              copy('acp.user'),
              prompt.userText,
              context.viberColors.route,
            ),
          if (showText && prompt.agentText.isNotEmpty)
            _text(
              context,
              copy('acp.agent'),
              prompt.agentText,
              context.viberColors.verified,
            ),
        ],
      ),
    );
  }

  Widget _text(BuildContext context, String label, String text, Color color) =>
      Padding(
        padding: const EdgeInsets.only(top: 12),
        child: Container(
          padding: const EdgeInsets.only(left: 12),
          decoration: BoxDecoration(
            border: Border(left: BorderSide(color: color, width: 2)),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                label,
                style: Theme.of(
                  context,
                ).textTheme.labelSmall?.copyWith(color: color),
              ),
              const SizedBox(height: 4),
              SelectableText(text),
            ],
          ),
        ),
      );
}

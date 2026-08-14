import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/agent_identity.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'conversation_timeline.dart';
import 'workbench_controller.dart';

final class ConversationsView extends StatefulWidget {
  const ConversationsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<ConversationsView> createState() => _ConversationsViewState();
}

final class _ConversationsViewState extends State<ConversationsView> {
  String _filter = '';
  bool _narrowDetail = false;
  bool _masterVisible = true;
  double _masterWidth = ViberMetrics.masterPaneWidth;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final narrow = constraints.maxWidth < 760;
        final master = _ConversationDirectory(
          controller: widget.controller,
          copy: widget.copy,
          filter: _filter,
          onFilter: (value) => setState(() => _filter = value),
          onSelect: (key) {
            unawaited(widget.controller.selectConversation(key));
            if (narrow) setState(() => _narrowDetail = true);
          },
        );
        final detail = _ConversationDetail(
          controller: widget.controller,
          copy: widget.copy,
          showBack: narrow,
          onBack: () => setState(() => _narrowDetail = false),
          masterVisible: _masterVisible,
          onToggleMaster: narrow
              ? null
              : () => setState(() => _masterVisible = !_masterVisible),
        );
        if (narrow) {
          return AnimatedSwitcher(
            duration: const Duration(milliseconds: 140),
            child:
                _narrowDetail && widget.controller.selectedConversation != null
                ? KeyedSubtree(
                    key: const ValueKey('conversation-detail'),
                    child: detail,
                  )
                : KeyedSubtree(
                    key: const ValueKey('conversation-directory'),
                    child: master,
                  ),
          );
        }
        final maxWidth = math.min(
          ViberMetrics.masterPaneMaxWidth,
          constraints.maxWidth * 0.45,
        );
        final masterWidth = _masterWidth
            .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
            .toDouble();
        return Row(
          children: [
            if (_masterVisible) ...[
              SizedBox(
                key: const Key('conversation-master-pane'),
                width: masterWidth,
                child: master,
              ),
              WorkbenchPaneDivider(
                key: const Key('conversation-master-divider'),
                label: widget.copy('common.resize_directory'),
                onDrag: (delta) => setState(() {
                  _masterWidth = (_masterWidth + delta)
                      .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
                      .toDouble();
                }),
              ),
            ],
            Expanded(child: detail),
          ],
        );
      },
    );
  }
}

final class _ConversationDirectory extends StatelessWidget {
  const _ConversationDirectory({
    required this.controller,
    required this.copy,
    required this.filter,
    required this.onFilter,
    required this.onSelect,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final String filter;
  final ValueChanged<String> onFilter;
  final ValueChanged<String> onSelect;

  @override
  Widget build(BuildContext context) {
    final normalized = filter.trim().toLowerCase();
    final conversations = controller.conversations
        .where((conversation) {
          if (normalized.isEmpty) return true;
          final latest = conversation.latest;
          return [
            conversation.key,
            conversation.captureRunId ?? '',
            latest.id,
            latest.sourceName,
            conversation.conversation.displayName ?? '',
            conversation.conversation.actor ?? '',
            ...?conversation.conversation.clientIdentity?.searchableValues,
            conversation.conversation.kind,
            latest.environmentId,
            latest.status,
            latest.reasonCode ?? '',
          ].any((value) => value.toLowerCase().contains(normalized));
        })
        .toList(growable: false);
    return ColoredBox(
      color: context.viberColors.panel,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 8, 8, 6),
            child: Semantics(
              textField: true,
              label: copy('conversations.filter'),
              child: CompactSearchField(
                key: const Key('conversation-filter'),
                hintText: copy('conversations.filter'),
                onChanged: onFilter,
              ),
            ),
          ),
          SectionLabel(
            label: copy('conversations.title'),
            count: conversations.length,
          ),
          Expanded(
            child:
                controller.conversationIndex == null &&
                    controller.conversationsLoading
                ? const Center(
                    child: CircularProgressIndicator(strokeWidth: 1.7),
                  )
                : conversations.isEmpty
                ? CenteredMessage(
                    icon: Icons.forum_outlined,
                    title: copy('conversations.empty'),
                  )
                : ListView.builder(
                    itemCount:
                        conversations.length +
                        (controller.conversationIndex?.nextCursor == null
                            ? 0
                            : 1),
                    itemBuilder: (context, index) {
                      if (index == conversations.length) {
                        return Padding(
                          padding: const EdgeInsets.all(8),
                          child: OutlinedButton.icon(
                            key: const Key('conversations-load-more'),
                            onPressed: controller.conversationsLoading
                                ? null
                                : () => unawaited(
                                    controller.loadMoreConversations(),
                                  ),
                            icon: const Icon(Icons.more_horiz, size: 14),
                            label: Text(copy('conversations.load_more')),
                          ),
                        );
                      }
                      final conversation = conversations[index];
                      return _ConversationRow(
                        conversation: conversation,
                        copy: copy,
                        selected:
                            controller.selectedConversationKey ==
                            conversation.key,
                        onPressed: () => onSelect(conversation.key),
                      );
                    },
                  ),
          ),
          if (controller.conversationsError case final error?)
            InlineNotice(message: error, error: true),
        ],
      ),
    );
  }
}

final class _ConversationRow extends StatelessWidget {
  const _ConversationRow({
    required this.conversation,
    required this.copy,
    required this.selected,
    required this.onPressed,
  });

  final ConversationSummary conversation;
  final AppCopy copy;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final latest = conversation.latest;
    final failed = latest.status == 'failed';
    final pending = latest.status == 'pending';
    final tone = failed
        ? context.viberColors.danger
        : pending
        ? context.viberColors.warning
        : context.viberColors.verified;
    final title = _conversationTitle(copy, conversation);
    return Semantics(
      selected: selected,
      button: true,
      label: '$title, ${conversation.turnCount}, ${latest.status}',
      child: Material(
        color: selected ? context.viberColors.selection : Colors.transparent,
        child: InkWell(
          key: Key('conversation-row-${conversation.key}'),
          onTap: onPressed,
          canRequestFocus: true,
          child: Container(
            height: 52,
            padding: const EdgeInsets.fromLTRB(8, 4, 7, 4),
            decoration: BoxDecoration(
              border: Border(
                left: BorderSide(
                  width: 2,
                  color: selected
                      ? context.viberColors.route
                      : Colors.transparent,
                ),
                bottom: BorderSide(color: context.viberColors.dividerSoft),
              ),
            ),
            child: Row(
              children: [
                AgentIdentityMark(
                  candidates: [latest.sourceName],
                  fallbackLabel: title,
                  fallbackIcon: conversation.exchangeScoped
                      ? Icons.swap_horiz
                      : Icons.forum_outlined,
                  fallbackColor: tone,
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Column(
                    mainAxisAlignment: MainAxisAlignment.center,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              title,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          Text(
                            _relativeTime(latest.occurredAt, copy),
                            style: monoStyle,
                          ),
                        ],
                      ),
                      const SizedBox(height: 3),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              _conversationScope(copy, conversation),
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                          ),
                          Text(latest.environmentId, style: monoStyle),
                        ],
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _ConversationDetail extends StatelessWidget {
  const _ConversationDetail({
    required this.controller,
    required this.copy,
    required this.showBack,
    required this.onBack,
    required this.masterVisible,
    required this.onToggleMaster,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool showBack;
  final VoidCallback onBack;
  final bool masterVisible;
  final VoidCallback? onToggleMaster;

  @override
  Widget build(BuildContext context) {
    final conversation = controller.selectedConversation;
    if (conversation == null) {
      return CenteredMessage(
        icon: Icons.forum_outlined,
        title: copy('conversations.select'),
      );
    }
    final activities = controller.selectedConversationPage?.items ?? const [];
    final capture = controller.data?.captures.where((candidate) {
      return conversation.captureRunId != null
          ? candidate.captureRunId == conversation.captureRunId
          : candidate.isManual &&
                candidate.id == conversation.latest.manualCaptureId;
    }).firstOrNull;
    final title = _conversationTitle(copy, conversation);
    final scope = _conversationScope(copy, conversation);
    return ColoredBox(
      color: context.viberColors.canvas,
      child: Column(
        children: [
          Container(
            width: double.infinity,
            padding: const EdgeInsets.fromLTRB(14, 9, 14, 9),
            color: context.viberColors.panel,
            child: Row(
              children: [
                if (onToggleMaster case final toggle?) ...[
                  IconButton(
                    key: const Key('conversation-directory-toggle'),
                    onPressed: toggle,
                    tooltip: copy(
                      masterVisible
                          ? 'common.hide_directory'
                          : 'common.show_directory',
                    ),
                    icon: Icon(Icons.view_sidebar_outlined, size: 15),
                    constraints: const BoxConstraints.tightFor(
                      width: 24,
                      height: 24,
                    ),
                    padding: EdgeInsets.zero,
                  ),
                  const SizedBox(width: 4),
                ],
                if (showBack) ...[
                  IconButton(
                    onPressed: onBack,
                    tooltip: copy('common.back'),
                    icon: const Icon(Icons.arrow_back, size: 17),
                    constraints: const BoxConstraints.tightFor(
                      width: 30,
                      height: 30,
                    ),
                    padding: EdgeInsets.zero,
                  ),
                  const SizedBox(width: 4),
                ],
                AgentIdentityMark(
                  candidates: [conversation.latest.sourceName],
                  fallbackLabel: title,
                  fallbackIcon: conversation.exchangeScoped
                      ? Icons.swap_horiz
                      : Icons.forum_outlined,
                  size: 30,
                  glyphSize: 18,
                  muted: capture != null && !capture.running,
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.headlineSmall,
                      ),
                      const SizedBox(height: 2),
                      Tooltip(
                        message: copy('conversations.derived'),
                        child: Text(
                          _conversationMetadata(copy, capture, scope),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                if (showBack)
                  IconButton(
                    key: const Key('conversation-capture-context'),
                    onPressed: () =>
                        unawaited(controller.openSelectedConversationCapture()),
                    tooltip: copy('conversations.capture_context'),
                    icon: const Icon(Icons.arrow_forward_rounded, size: 15),
                  )
                else
                  TextButton.icon(
                    key: const Key('conversation-capture-context'),
                    onPressed: () =>
                        unawaited(controller.openSelectedConversationCapture()),
                    icon: const Icon(Icons.arrow_forward_rounded, size: 13),
                    label: Text(copy('conversations.capture_context')),
                  ),
              ],
            ),
          ),
          const Divider(height: 1),
          if (controller.conversationsError case final error?)
            InlineNotice(message: error, error: true),
          Expanded(
            child: controller.conversationsLoading && activities.isEmpty
                ? const Center(
                    child: CircularProgressIndicator(strokeWidth: 1.7),
                  )
                : EvidenceConversationTimeline(
                    controller: controller,
                    activities: activities,
                    copy: copy,
                    title: conversation.exchangeScoped
                        ? null
                        : copy('conversations.audit'),
                    canLoadEarlier:
                        controller.selectedConversationPage?.nextCursor != null,
                    loadingEarlier: controller.conversationsLoading,
                    exchangeScoped: conversation.exchangeScoped,
                    onLoadEarlier: () =>
                        unawaited(controller.loadMoreSelectedConversation()),
                  ),
          ),
        ],
      ),
    );
  }
}

String _conversationTitle(AppCopy copy, ConversationSummary conversation) {
  final source = conversation.latest.sourceName;
  final displayName = conversation.conversation.displayName?.trim();
  return switch (conversation.conversation.kind) {
    'agent' when displayName != null && displayName.isNotEmpty => displayName,
    'isolated_subagent' => copy.format(
      'conversations.name.anonymous_subagent',
      {'agent': source},
    ),
    'pending_exchange' => copy.format('conversations.name.pending', {
      'agent': source,
    }),
    _ => displayName == null || displayName.isEmpty ? source : displayName,
  };
}

String _conversationScope(AppCopy copy, ConversationSummary conversation) {
  final turns = copy.format('conversations.turn_count', {
    'count': conversation.turnCount,
  });
  return switch (conversation.conversation.kind) {
    'agent' => '$turns · ${copy('conversations.scope.named_agent')}',
    'isolated_subagent' =>
      '$turns · ${copy('conversations.scope.anonymous_agent')}',
    'pending_exchange' => copy('conversations.scope.pending'),
    'isolated_exchange' => '$turns · ${copy('conversations.scope.exchange')}',
    _ => turns,
  };
}

String _conversationMetadata(
  AppCopy copy,
  CaptureRecord? capture,
  String fallback,
) {
  if (capture == null) return fallback;
  final managed = capture.managedRun;
  final parts = <String>[
    ?managed?.workspaceLabel,
    ?managed?.localUserLabel,
    capture.isManual
        ? copy('capture.source.manual.short')
        : copy('capture.source.managed.short'),
  ];
  return parts.join('  ·  ');
}

String _relativeTime(DateTime timestamp, AppCopy copy) {
  final delta = DateTime.now().toUtc().difference(timestamp);
  if (delta.isNegative || delta.inMinutes < 1) {
    return copy('common.time.now');
  }
  if (delta.inHours < 1) {
    return copy.format('common.time.minutes', {'count': delta.inMinutes});
  }
  if (delta.inDays < 1) {
    return copy.format('common.time.hours', {'count': delta.inHours});
  }
  return copy.format('common.time.days', {'count': delta.inDays});
}

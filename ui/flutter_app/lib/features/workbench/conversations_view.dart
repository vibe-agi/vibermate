import 'dart:async';

import 'package:flutter/material.dart';

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
        return Row(
          children: [
            SizedBox(width: 316, child: master),
            const VerticalDivider(width: 1),
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
            latest.environmentId,
            latest.status,
            latest.reasonCode ?? '',
          ].any((value) => value.toLowerCase().contains(normalized));
        })
        .toList(growable: false);
    return ColoredBox(
      color: ViberColors.panel,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 10, 10, 7),
            child: Semantics(
              textField: true,
              label: copy('conversations.filter'),
              child: TextField(
                onChanged: onFilter,
                decoration: InputDecoration(
                  hintText: copy('conversations.filter'),
                  prefixIcon: const Icon(Icons.search, size: 15),
                  prefixIconConstraints: const BoxConstraints.tightFor(
                    width: 31,
                  ),
                ),
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
        ? ViberColors.danger
        : pending
        ? ViberColors.warning
        : ViberColors.verified;
    return Semantics(
      selected: selected,
      button: true,
      label:
          '${latest.sourceName}, ${conversation.turnCount}, ${latest.status}',
      child: Material(
        color: selected ? ViberColors.selection : Colors.transparent,
        child: InkWell(
          key: Key('conversation-row-${conversation.key}'),
          onTap: onPressed,
          canRequestFocus: true,
          child: Container(
            height: 58,
            padding: const EdgeInsets.fromLTRB(9, 6, 8, 6),
            decoration: BoxDecoration(
              border: Border(
                left: BorderSide(
                  width: 2,
                  color: selected ? ViberColors.route : Colors.transparent,
                ),
                bottom: const BorderSide(color: ViberColors.dividerSoft),
              ),
            ),
            child: Row(
              children: [
                Container(
                  width: 28,
                  height: 28,
                  decoration: BoxDecoration(
                    color: tone.withValues(alpha: 0.08),
                    border: Border.all(color: tone.withValues(alpha: 0.25)),
                    borderRadius: BorderRadius.circular(5),
                  ),
                  child: Icon(
                    conversation.exchangeScoped
                        ? Icons.swap_horiz
                        : Icons.forum_outlined,
                    size: 15,
                    color: tone,
                  ),
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
                              latest.sourceName,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          Text(
                            _relativeTime(latest.occurredAt),
                            style: monoStyle,
                          ),
                        ],
                      ),
                      const SizedBox(height: 3),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              conversation.exchangeScoped
                                  ? copy('conversations.boundary.exchange')
                                  : copy.format(
                                      'conversations.boundary.capture_run',
                                      {'count': conversation.turnCount},
                                    ),
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
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool showBack;
  final VoidCallback onBack;

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
    return ColoredBox(
      color: ViberColors.canvas,
      child: Column(
        children: [
          Container(
            width: double.infinity,
            padding: const EdgeInsets.fromLTRB(14, 9, 14, 9),
            color: ViberColors.panel,
            child: Row(
              children: [
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
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        conversation.latest.sourceName,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.headlineSmall,
                      ),
                      const SizedBox(height: 2),
                      Text(
                        conversation.exchangeScoped
                            ? copy('conversations.exchange_boundary.detail')
                            : copy('conversations.capture_run_boundary.detail'),
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                StatusPill(
                  label: conversation.exchangeScoped
                      ? copy('conversations.exchange_boundary')
                      : copy('conversations.capture_run_boundary'),
                  color: conversation.exchangeScoped
                      ? ViberColors.warning
                      : ViberColors.route,
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

String _relativeTime(DateTime timestamp) {
  final delta = DateTime.now().toUtc().difference(timestamp);
  if (delta.isNegative || delta.inMinutes < 1) return 'now';
  if (delta.inHours < 1) return '${delta.inMinutes}m';
  if (delta.inDays < 1) return '${delta.inHours}h';
  return '${delta.inDays}d';
}

import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class EvidenceConversationTimeline extends StatefulWidget {
  const EvidenceConversationTimeline({
    required this.controller,
    required this.activities,
    required this.copy,
    this.canLoadEarlier = false,
    this.loadingEarlier = false,
    this.exchangeScoped = false,
    this.onLoadEarlier,
    super.key,
  });

  final WorkbenchController controller;
  final List<ActivityRecord> activities;
  final AppCopy copy;
  final bool canLoadEarlier;
  final bool loadingEarlier;
  final bool exchangeScoped;
  final VoidCallback? onLoadEarlier;

  @override
  State<EvidenceConversationTimeline> createState() =>
      _EvidenceConversationTimelineState();
}

final class _EvidenceConversationTimelineState
    extends State<EvidenceConversationTimeline> {
  static const _collapsedExtent = 100.0;
  static const _expandedExtent = 640.0;
  final _scroll = ScrollController();
  final _mapScroll = ScrollController();
  final _fullSnapshots = <String>{};
  bool _nearBottom = true;
  int _current = 0;
  String? _expandedId;

  List<ActivityRecord> get _ordered {
    final values = [...widget.activities];
    values.sort((left, right) {
      final time = left.occurredAt.compareTo(right.occurredAt);
      return time != 0 ? time : left.id.compareTo(right.id);
    });
    return values;
  }

  @override
  void initState() {
    super.initState();
    _scroll.addListener(_handleScroll);
    WidgetsBinding.instance.addPostFrameCallback((_) => _jumpToLatest());
  }

  @override
  void didUpdateWidget(covariant EvidenceConversationTimeline oldWidget) {
    super.didUpdateWidget(oldWidget);
    final oldOrdered = [...oldWidget.activities]..sort(_compareActivity);
    final newOrdered = [...widget.activities]..sort(_compareActivity);
    final oldIds = oldOrdered.map((value) => value.id).toSet();
    final newNewest = newOrdered.lastOrNull;
    if (newNewest != null && !oldIds.contains(newNewest.id) && _nearBottom) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _animateToLatest());
    }
    final oldFirst = oldOrdered.firstOrNull;
    final addedBefore = oldFirst == null
        ? 0
        : newOrdered.indexWhere((value) => value.id == oldFirst.id);
    if (addedBefore > 0 && _scroll.hasClients) {
      final previousOffset = _scroll.offset;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!_scroll.hasClients) return;
        _scroll.jumpTo(
          (previousOffset + addedBefore * _collapsedExtent).clamp(
            0,
            _scroll.position.maxScrollExtent,
          ),
        );
      });
    }
    if (_expandedId != null &&
        !widget.activities.any((activity) => activity.id == _expandedId)) {
      _expandedId = null;
    }
  }

  static int _compareActivity(ActivityRecord left, ActivityRecord right) {
    final time = left.occurredAt.compareTo(right.occurredAt);
    return time != 0 ? time : left.id.compareTo(right.id);
  }

  void _handleScroll() {
    if (!_scroll.hasClients) return;
    final position = _scroll.position;
    final nearBottom = position.maxScrollExtent - position.pixels < 150;
    final current = _indexAtOffset(
      position.pixels,
    ).clamp(0, math.max(0, widget.activities.length - 1)).toInt();
    if (nearBottom == _nearBottom && current == _current) return;
    setState(() {
      _nearBottom = nearBottom;
      _current = current;
    });
    _keepMapVisible(current);
  }

  int _indexAtOffset(double offset) {
    final activities = _ordered;
    final expanded = activities.indexWhere(
      (activity) => activity.id == _expandedId,
    );
    if (expanded < 0) return (offset / _collapsedExtent).floor();
    final expandedStart = expanded * _collapsedExtent;
    if (offset < expandedStart) return (offset / _collapsedExtent).floor();
    if (offset < expandedStart + _expandedExtent) return expanded;
    final adjusted = offset - (_expandedExtent - _collapsedExtent);
    return (adjusted / _collapsedExtent).floor();
  }

  double _offsetFor(int index) {
    final activities = _ordered;
    final expanded = activities.indexWhere(
      (activity) => activity.id == _expandedId,
    );
    return index * _collapsedExtent +
        (expanded >= 0 && expanded < index
            ? _expandedExtent - _collapsedExtent
            : 0);
  }

  void _keepMapVisible(int index) {
    if (!_mapScroll.hasClients) return;
    final target = (index * 22.0 - 80).clamp(
      0.0,
      _mapScroll.position.maxScrollExtent,
    );
    _mapScroll.animateTo(
      target,
      duration: const Duration(milliseconds: 100),
      curve: Curves.easeOut,
    );
  }

  void _jumpToLatest() {
    if (_scroll.hasClients) _scroll.jumpTo(_scroll.position.maxScrollExtent);
  }

  void _animateToLatest() {
    if (!_scroll.hasClients) return;
    _scroll.animateTo(
      _scroll.position.maxScrollExtent,
      duration: const Duration(milliseconds: 180),
      curve: Curves.easeOut,
    );
  }

  void _goTo(int index) {
    if (!_scroll.hasClients) return;
    _scroll.animateTo(
      _offsetFor(index).clamp(0, _scroll.position.maxScrollExtent),
      duration: const Duration(milliseconds: 180),
      curve: Curves.easeOut,
    );
  }

  void _toggle(ActivityRecord activity) {
    final expanding = _expandedId != activity.id;
    setState(() {
      _expandedId = expanding ? activity.id : null;
      _current = _ordered.indexWhere((value) => value.id == activity.id);
    });
    if (expanding) {
      unawaited(widget.controller.loadExchangeDetail(activity.id));
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!mounted) return;
        _goTo(_current);
      });
    }
  }

  Future<void> _toggleFull(ActivityRecord activity) async {
    if (_fullSnapshots.remove(activity.id)) {
      setState(() {});
      return;
    }
    final loaded = await widget.controller.loadExchangeDetail(
      activity.id,
      contentView: 'full',
    );
    if (!mounted || loaded == null) return;
    setState(() => _fullSnapshots.add(activity.id));
  }

  @override
  void dispose() {
    _scroll
      ..removeListener(_handleScroll)
      ..dispose();
    _mapScroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final activities = _ordered;
    if (activities.isEmpty) {
      return CenteredMessage(
        icon: Icons.chat_bubble_outline,
        title: widget.copy('conversation.empty'),
      );
    }
    return Column(
      children: [
        SizedBox(
          height: 34,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 14),
            child: LayoutBuilder(
              builder: (context, constraints) => Row(
                children: [
                  Flexible(
                    child: Text(
                      widget.copy(
                        widget.exchangeScoped
                            ? 'conversation.exchanges_title'
                            : 'conversation.title',
                      ),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.titleMedium,
                    ),
                  ),
                  const SizedBox(width: 7),
                  Text(
                    widget.copy.format(
                      widget.exchangeScoped
                          ? 'conversation.exchanges'
                          : 'conversation.turns',
                      {'count': activities.length},
                    ),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  if (widget.canLoadEarlier) ...[
                    const SizedBox(width: 4),
                    if (constraints.maxWidth < 520)
                      IconButton(
                        key: const Key('conversation-load-earlier'),
                        tooltip: widget.copy(
                          widget.exchangeScoped
                              ? 'conversation.load_earlier_exchanges'
                              : 'conversation.load_earlier',
                        ),
                        onPressed: widget.loadingEarlier
                            ? null
                            : widget.onLoadEarlier,
                        icon: const Icon(Icons.history, size: 15),
                      )
                    else
                      TextButton.icon(
                        key: const Key('conversation-load-earlier'),
                        onPressed: widget.loadingEarlier
                            ? null
                            : widget.onLoadEarlier,
                        icon: const Icon(Icons.history, size: 13),
                        label: Text(
                          widget.copy(
                            widget.exchangeScoped
                                ? 'conversation.load_earlier_exchanges'
                                : 'conversation.load_earlier',
                          ),
                        ),
                      ),
                  ],
                  const Spacer(),
                  if (!_nearBottom)
                    if (constraints.maxWidth < 520)
                      IconButton(
                        tooltip: widget.copy('conversation.latest'),
                        onPressed: _animateToLatest,
                        icon: const Icon(Icons.arrow_downward, size: 15),
                      )
                    else
                      TextButton.icon(
                        onPressed: _animateToLatest,
                        icon: const Icon(Icons.arrow_downward, size: 13),
                        label: Text(widget.copy('conversation.latest')),
                      ),
                ],
              ),
            ),
          ),
        ),
        const Divider(height: 1),
        Expanded(
          child: Row(
            children: [
              Expanded(
                child: Scrollbar(
                  controller: _scroll,
                  thumbVisibility: true,
                  child: ListView.builder(
                    controller: _scroll,
                    padding: const EdgeInsets.fromLTRB(14, 8, 10, 14),
                    itemExtentBuilder: (index, _) =>
                        activities[index].id == _expandedId
                        ? _expandedExtent
                        : _collapsedExtent,
                    itemCount: activities.length,
                    itemBuilder: (context, index) {
                      final activity = activities[index];
                      final expanded = activity.id == _expandedId;
                      return _TurnEvidenceItem(
                        activity: activity,
                        number: index + 1,
                        copy: widget.copy,
                        expanded: expanded,
                        showFull: _fullSnapshots.contains(activity.id),
                        controller: widget.controller,
                        exchangeScoped: widget.exchangeScoped,
                        onToggle: () => _toggle(activity),
                        onToggleFull: () => _toggleFull(activity),
                      );
                    },
                  ),
                ),
              ),
              if (!widget.exchangeScoped)
                Container(
                  width: 52,
                  decoration: const BoxDecoration(
                    color: ViberColors.panel,
                    border: Border(
                      left: BorderSide(color: ViberColors.divider),
                    ),
                  ),
                  child: Semantics(
                    label: widget.copy('conversation.map'),
                    child: ListView.builder(
                      controller: _mapScroll,
                      padding: const EdgeInsets.symmetric(vertical: 8),
                      itemExtent: 22,
                      itemCount: activities.length,
                      itemBuilder: (context, index) {
                        final selected = index == _current;
                        return Tooltip(
                          message: widget.copy.format('conversation.turn', {
                            'number': index + 1,
                          }),
                          child: InkWell(
                            onTap: () => _goTo(index),
                            canRequestFocus: true,
                            child: Center(
                              child: AnimatedContainer(
                                duration: const Duration(milliseconds: 100),
                                width: selected ? 26 : 17,
                                height: selected ? 13 : 4,
                                alignment: Alignment.center,
                                decoration: BoxDecoration(
                                  color: selected
                                      ? ViberColors.route.withValues(
                                          alpha: 0.22,
                                        )
                                      : activities[index].status == 'failed'
                                      ? ViberColors.danger
                                      : activities[index].status == 'pending'
                                      ? ViberColors.warning
                                      : ViberColors.textFaint,
                                  borderRadius: BorderRadius.circular(3),
                                  border: selected
                                      ? Border.all(color: ViberColors.route)
                                      : null,
                                ),
                                child: selected
                                    ? Text(
                                        '${index + 1}',
                                        style: const TextStyle(
                                          fontSize: 8,
                                          color: ViberColors.route,
                                        ),
                                      )
                                    : null,
                              ),
                            ),
                          ),
                        );
                      },
                    ),
                  ),
                ),
            ],
          ),
        ),
      ],
    );
  }
}

final class _TurnEvidenceItem extends StatelessWidget {
  const _TurnEvidenceItem({
    required this.activity,
    required this.number,
    required this.copy,
    required this.expanded,
    required this.showFull,
    required this.controller,
    required this.exchangeScoped,
    required this.onToggle,
    required this.onToggleFull,
  });

  final ActivityRecord activity;
  final int number;
  final AppCopy copy;
  final bool expanded;
  final bool showFull;
  final WorkbenchController controller;
  final bool exchangeScoped;
  final VoidCallback onToggle;
  final VoidCallback onToggleFull;

  @override
  Widget build(BuildContext context) {
    final failed = activity.status == 'failed';
    final pending = activity.status == 'pending';
    final tone = failed
        ? ViberColors.danger
        : pending
        ? ViberColors.warning
        : ViberColors.verified;
    return Semantics(
      container: true,
      button: true,
      label:
          '${exchangeScoped ? copy('conversation.exchange') : copy.format('conversation.turn', {'number': number})}, ${activity.title}, ${activity.status}',
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(
            width: 36,
            child: Column(
              children: [
                Container(
                  width: 9,
                  height: 9,
                  margin: const EdgeInsets.only(top: 11),
                  decoration: BoxDecoration(
                    color: tone,
                    shape: BoxShape.circle,
                    border: Border.all(color: ViberColors.canvas, width: 2),
                  ),
                ),
                if (!exchangeScoped)
                  const Expanded(
                    child: VerticalDivider(
                      width: 1,
                      color: ViberColors.divider,
                    ),
                  ),
              ],
            ),
          ),
          Expanded(
            child: Container(
              margin: const EdgeInsets.only(bottom: 7),
              decoration: BoxDecoration(
                color: ViberColors.panel,
                border: Border(
                  left: BorderSide(
                    color: expanded ? ViberColors.route : Colors.transparent,
                    width: 2,
                  ),
                  bottom: const BorderSide(color: ViberColors.dividerSoft),
                ),
              ),
              child: Column(
                children: [
                  SizedBox(
                    height: 92,
                    child: InkWell(
                      key: Key('conversation-turn-${activity.id}'),
                      onTap: onToggle,
                      canRequestFocus: true,
                      child: Padding(
                        padding: const EdgeInsets.fromLTRB(9, 8, 7, 7),
                        child: LayoutBuilder(
                          builder: (context, constraints) {
                            final compact = constraints.maxWidth < 360;
                            final title = Row(
                              children: [
                                Text(
                                  exchangeScoped
                                      ? copy('conversation.exchange')
                                      : copy.format('conversation.turn', {
                                          'number': number,
                                        }),
                                  style: Theme.of(context).textTheme.labelMedium
                                      ?.copyWith(color: ViberColors.route),
                                ),
                                const SizedBox(width: 8),
                                Expanded(
                                  child: Text(
                                    activity.title,
                                    maxLines: 1,
                                    overflow: TextOverflow.ellipsis,
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                ),
                                if (!compact) ...[
                                  StatusPill(
                                    label: activity.status,
                                    color: tone,
                                  ),
                                  const SizedBox(width: 6),
                                  Text(
                                    _clockTime(activity.occurredAt),
                                    style: monoStyle,
                                  ),
                                ],
                                const SizedBox(width: 3),
                                Icon(
                                  expanded
                                      ? Icons.expand_less
                                      : Icons.expand_more,
                                  size: 16,
                                  color: ViberColors.textMuted,
                                ),
                              ],
                            );
                            if (compact) {
                              return Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  title,
                                  const SizedBox(height: 7),
                                  Row(
                                    children: [
                                      Semantics(
                                        label: activity.status,
                                        child: Container(
                                          width: 7,
                                          height: 7,
                                          decoration: BoxDecoration(
                                            shape: BoxShape.circle,
                                            color: tone,
                                          ),
                                        ),
                                      ),
                                      const SizedBox(width: 6),
                                      Text(
                                        _clockTime(activity.occurredAt),
                                        style: monoStyle,
                                      ),
                                      const SizedBox(width: 8),
                                      Expanded(
                                        child: Text(
                                          activity.routeId,
                                          maxLines: 1,
                                          overflow: TextOverflow.ellipsis,
                                          style: monoStyle,
                                        ),
                                      ),
                                    ],
                                  ),
                                ],
                              );
                            }
                            return Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                title,
                                const SizedBox(height: 7),
                                Wrap(
                                  spacing: 12,
                                  runSpacing: 3,
                                  children: [
                                    _Evidence(
                                      label: copy('conversation.route'),
                                      value: activity.routeId,
                                    ),
                                    _Evidence(
                                      label: copy('conversation.account'),
                                      value:
                                          activity.accountId ??
                                          'client passthrough',
                                    ),
                                    _Evidence(
                                      label: 'Exchange',
                                      value: activity.id,
                                    ),
                                  ],
                                ),
                              ],
                            );
                          },
                        ),
                      ),
                    ),
                  ),
                  if (expanded) ...[
                    const Divider(height: 1),
                    Expanded(
                      child: _ExchangeEvidencePanel(
                        activity: activity,
                        copy: copy,
                        showFull: showFull,
                        controller: controller,
                        onToggleFull: onToggleFull,
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

final class _ExchangeEvidencePanel extends StatelessWidget {
  const _ExchangeEvidencePanel({
    required this.activity,
    required this.copy,
    required this.showFull,
    required this.controller,
    required this.onToggleFull,
  });

  final ActivityRecord activity;
  final AppCopy copy;
  final bool showFull;
  final WorkbenchController controller;
  final VoidCallback onToggleFull;

  @override
  Widget build(BuildContext context) {
    final detail = controller.exchangeDetail(
      activity.id,
      contentView: showFull ? 'full' : 'incremental',
    );
    if (detail == null) {
      if (controller.conversationsError case final error?) {
        return Padding(
          padding: const EdgeInsets.all(10),
          child: InlineNotice(message: error, error: true),
        );
      }
      return const Center(child: CircularProgressIndicator(strokeWidth: 1.6));
    }
    final content = detail.content;
    final projection = content.requestProjection;
    return SingleChildScrollView(
      padding: const EdgeInsets.fromLTRB(10, 9, 10, 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(Icons.history, size: 13, color: ViberColors.route),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  copy.format('exchange.environment.frozen', {
                    'environment': activity.environment.id,
                    'revision': activity.environment.revision,
                  }),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: monoStyle,
                ),
              ),
              TextButton.icon(
                key: Key('exchange-environment-${activity.id}'),
                onPressed: controller.environmentRevisionLoading
                    ? null
                    : () => unawaited(
                        controller.inspectEnvironmentRevision(
                          activity.environment.id,
                          activity.environment.revision,
                          expectedDigest: activity.environment.digest,
                        ),
                      ),
                icon: const Icon(Icons.open_in_new, size: 12),
                label: Text(
                  copy.format('environment.history.inspect', {
                    'revision': activity.environment.revision,
                  }),
                ),
              ),
            ],
          ),
          const Divider(height: 10),
          if (detail.diagnosis case final diagnosis?)
            _DiagnosisNotice(
              diagnosis: diagnosis,
              result: detail.processingTrace.result,
              copy: copy,
            ),
          if (content.state == 'not_recorded')
            InlineNotice(message: copy('exchange.content.not_recorded'))
          else ...[
            Row(
              children: [
                Expanded(
                  child: Wrap(
                    spacing: 10,
                    runSpacing: 4,
                    children: [
                      _Evidence(
                        label: copy('exchange.model.requested'),
                        value: content.request!.requestedModel,
                      ),
                      _Evidence(
                        label: copy('exchange.model.effective'),
                        value: content.request!.effectiveModel,
                      ),
                      _Evidence(
                        label: copy('exchange.max_output'),
                        value: '${content.request!.maxOutputTokens}',
                      ),
                    ],
                  ),
                ),
                if (projection?.fullSnapshotAvailable == true)
                  TextButton.icon(
                    key: Key('exchange-full-${activity.id}'),
                    onPressed:
                        controller.exchangeIsLoading(
                          activity.id,
                          contentView: 'full',
                        )
                        ? null
                        : onToggleFull,
                    icon: Icon(
                      showFull ? Icons.compress : Icons.unfold_more,
                      size: 13,
                    ),
                    label: Text(
                      copy(
                        showFull
                            ? 'exchange.content.incremental'
                            : 'exchange.content.full',
                      ),
                    ),
                  ),
              ],
            ),
            if (projection != null) ...[
              const SizedBox(height: 6),
              Container(
                width: double.infinity,
                padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 5),
                color: ViberColors.route.withValues(alpha: 0.07),
                child: Text(
                  copy.format('exchange.projection', {
                    'relationship': projection.relationship,
                    'visible': content.request!.messages.length,
                    'total': projection.totalMessageCount,
                  }),
                  style: monoStyle,
                ),
              ),
            ],
            const SizedBox(height: 8),
            for (final message in content.request!.messages)
              _MessageCard(message: message, copy: copy),
            if (content.response case final response?)
              _ResponseCard(response: response, copy: copy)
            else
              _PendingResponse(copy: copy),
          ],
          const SizedBox(height: 8),
          Theme(
            data: Theme.of(context).copyWith(dividerColor: Colors.transparent),
            child: ExpansionTile(
              tilePadding: EdgeInsets.zero,
              childrenPadding: EdgeInsets.zero,
              dense: true,
              title: Text(
                copy('exchange.evidence'),
                style: Theme.of(context).textTheme.titleSmall,
              ),
              subtitle: Text(
                '${detail.processingTrace.attempts.length} ${copy('exchange.attempts')}  ·  ${detail.processingTrace.result}',
                style: Theme.of(context).textTheme.bodySmall,
              ),
              children: [
                _FrozenEvidence(detail: detail, copy: copy),
                for (final attempt in detail.processingTrace.attempts)
                  _AttemptRow(attempt: attempt, copy: copy),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

final class _MessageCard extends StatelessWidget {
  const _MessageCard({required this.message, required this.copy});

  final ExchangeContentMessage message;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final user = message.role == 'user' || message.role == 'tool';
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 7),
      padding: const EdgeInsets.fromLTRB(9, 7, 9, 8),
      decoration: BoxDecoration(
        color: user
            ? ViberColors.route.withValues(alpha: 0.055)
            : ViberColors.panelRaised,
        border: Border.all(color: ViberColors.dividerSoft),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            copy('exchange.role.${message.role}'),
            style: Theme.of(context).textTheme.labelMedium?.copyWith(
              color: user ? ViberColors.route : ViberColors.textMuted,
            ),
          ),
          const SizedBox(height: 5),
          for (final block in message.blocks)
            _ContentBlockView(block: block, copy: copy),
        ],
      ),
    );
  }
}

final class _ResponseCard extends StatelessWidget {
  const _ResponseCard({required this.response, required this.copy});

  final ExchangeResponse response;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 7),
      padding: const EdgeInsets.fromLTRB(9, 7, 9, 8),
      decoration: BoxDecoration(
        color: ViberColors.verified.withValues(alpha: 0.045),
        border: Border.all(color: ViberColors.dividerSoft),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                copy('exchange.role.assistant'),
                style: Theme.of(
                  context,
                ).textTheme.labelMedium?.copyWith(color: ViberColors.verified),
              ),
              const Spacer(),
              Text(
                '${response.reportedModel}  ·  ${response.stopReason}',
                style: monoStyle,
              ),
            ],
          ),
          const SizedBox(height: 5),
          for (final block in response.blocks)
            _ContentBlockView(block: block, copy: copy),
          const SizedBox(height: 6),
          Wrap(
            spacing: 9,
            runSpacing: 3,
            children: [
              _UsageValue(
                label: copy('exchange.usage.input'),
                value: response.usage.inputUncached,
              ),
              _UsageValue(
                label: copy('exchange.usage.cache_read'),
                value: response.usage.cacheRead,
              ),
              _UsageValue(
                label: copy('exchange.usage.output'),
                value: response.usage.output,
              ),
              _UsageValue(
                label: copy('exchange.usage.reasoning'),
                value: response.usage.reasoning,
              ),
            ],
          ),
        ],
      ),
    );
  }
}

final class _ContentBlockView extends StatelessWidget {
  const _ContentBlockView({required this.block, required this.copy});

  final ExchangeContentBlock block;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    if (block.availability == 'omitted') {
      return Text(
        copy.format('exchange.content.omitted', {'bytes': block.originalSize}),
        style: Theme.of(context).textTheme.bodySmall,
      );
    }
    if (block.kind == 'tool_call') {
      return Container(
        width: double.infinity,
        margin: const EdgeInsets.only(top: 3),
        padding: const EdgeInsets.all(7),
        color: ViberColors.warning.withValues(alpha: 0.07),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(
                  Icons.build_outlined,
                  size: 13,
                  color: ViberColors.warning,
                ),
                const SizedBox(width: 5),
                Text(block.toolName ?? copy('exchange.tool.unknown')),
                const Spacer(),
                Text(block.callId ?? '—', style: monoStyle),
              ],
            ),
            if (block.arguments != null) ...[
              const SizedBox(height: 5),
              SelectableText(
                const JsonEncoder.withIndent('  ').convert(block.arguments),
                style: monoStyle,
              ),
            ],
          ],
        ),
      );
    }
    if (block.kind == 'reasoning') {
      return ExpansionTile(
        tilePadding: EdgeInsets.zero,
        childrenPadding: const EdgeInsets.only(bottom: 6),
        dense: true,
        title: Text(copy('exchange.content.reasoning')),
        children: [SelectableText(block.text ?? '', style: monoStyle)],
      );
    }
    if (block.kind == 'tool_result') {
      return Container(
        width: double.infinity,
        padding: const EdgeInsets.all(7),
        color: (block.toolError ? ViberColors.danger : ViberColors.verified)
            .withValues(alpha: 0.06),
        child: SelectableText(block.text ?? '', style: monoStyle),
      );
    }
    return SelectableText(
      block.text ?? '',
      style: Theme.of(context).textTheme.bodyMedium?.copyWith(
        color: block.kind == 'refusal' ? ViberColors.danger : ViberColors.text,
        height: 1.35,
      ),
    );
  }
}

final class _UsageValue extends StatelessWidget {
  const _UsageValue({required this.label, required this.value});

  final String label;
  final ExchangeUsageValue value;

  @override
  Widget build(BuildContext context) {
    return Text(
      '$label ${value.known ? value.tokens : '—'}',
      style: monoStyle.copyWith(color: ViberColors.textMuted),
    );
  }
}

final class _PendingResponse extends StatelessWidget {
  const _PendingResponse({required this.copy});

  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 7),
      child: Row(
        children: [
          const SizedBox.square(
            dimension: 12,
            child: CircularProgressIndicator(strokeWidth: 1.4),
          ),
          const SizedBox(width: 7),
          Text(copy('exchange.response.pending')),
        ],
      ),
    );
  }
}

final class _DiagnosisNotice extends StatelessWidget {
  const _DiagnosisNotice({
    required this.diagnosis,
    required this.result,
    required this.copy,
  });

  final ExchangeDiagnosis diagnosis;
  final String result;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final location = [
      diagnosis.clientField,
      diagnosis.clientPath,
      diagnosis.providerField,
    ].whereType<String>().join(' · ');
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 8),
      padding: const EdgeInsets.all(8),
      color: ViberColors.danger.withValues(alpha: 0.07),
      child: Text(
        '$result${diagnosis.providerStatus == null ? '' : ' · ${diagnosis.providerStatus}'}${location.isEmpty ? '' : ' · $location'}',
        style: monoStyle.copyWith(color: ViberColors.danger),
      ),
    );
  }
}

final class _FrozenEvidence extends StatelessWidget {
  const _FrozenEvidence({required this.detail, required this.copy});

  final ExchangeDetail detail;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final value = detail.environment;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(vertical: 6),
      decoration: const BoxDecoration(
        border: Border(top: BorderSide(color: ViberColors.dividerSoft)),
      ),
      child: Wrap(
        spacing: 12,
        runSpacing: 4,
        children: [
          _Evidence(
            label: 'Environment',
            value: '${value.id}@${value.revision}',
          ),
          _Evidence(label: 'Endpoint', value: value.clientEndpointId),
          _Evidence(label: 'Protocol', value: value.protocolPlanId),
          _Evidence(
            label: 'Route',
            value: '${value.routeId}@${value.routeRevision}',
          ),
          _Evidence(label: 'Account', value: value.accountId ?? 'client'),
          _Evidence(label: 'Digest', value: value.digest),
        ],
      ),
    );
  }
}

final class _AttemptRow extends StatelessWidget {
  const _AttemptRow({required this.attempt, required this.copy});

  final EgressAttemptRecord attempt;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(vertical: 6),
      decoration: const BoxDecoration(
        border: Border(top: BorderSide(color: ViberColors.dividerSoft)),
      ),
      child: Row(
        children: [
          SizedBox(
            width: 24,
            child: Text('${attempt.sequence}', style: monoStyle),
          ),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(attempt.targetOrigin, style: monoStyle),
                Text(
                  '${attempt.purpose}  ·  ${attempt.outcome ?? copy('network.egress.running')}  ·  ${attempt.bytesOut}/${attempt.bytesIn} B',
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

final class _Evidence extends StatelessWidget {
  const _Evidence({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    return RichText(
      maxLines: 1,
      overflow: TextOverflow.ellipsis,
      text: TextSpan(
        style: monoStyle,
        children: [
          TextSpan(
            text: '$label ',
            style: const TextStyle(color: ViberColors.textFaint),
          ),
          TextSpan(text: value),
        ],
      ),
    );
  }
}

String _clockTime(DateTime timestamp) {
  final local = timestamp.toLocal();
  return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}:${local.second.toString().padLeft(2, '0')}';
}

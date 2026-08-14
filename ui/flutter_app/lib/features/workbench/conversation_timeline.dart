import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:markdown/markdown.dart' as md;

import '../../core/api/control_models.dart';
import '../../core/design/agent_client_profile.dart';
import '../../core/design/agent_identity.dart';
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
    this.showCount = true,
    this.title,
    this.onLoadEarlier,
    super.key,
  });

  final WorkbenchController controller;
  final List<ActivityRecord> activities;
  final AppCopy copy;
  final bool canLoadEarlier;
  final bool loadingEarlier;
  final bool exchangeScoped;
  final bool showCount;
  final String? title;
  final VoidCallback? onLoadEarlier;

  @override
  State<EvidenceConversationTimeline> createState() =>
      _EvidenceConversationTimelineState();
}

final class _EvidenceConversationTimelineState
    extends State<EvidenceConversationTimeline> {
  static const _collapsedExtent = 54.0;
  static const _expandedExtentEstimate = 640.0;
  static const _mapItemExtent = 16.0;
  final _scroll = ScrollController();
  final _mapScroll = ScrollController();
  final _itemKeys = <String, GlobalKey>{};
  final _fullSnapshots = <String>{};
  bool _nearBottom = true;
  int _current = 0;
  String? _expandedId;
  int? _navigationTarget;

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
    final latest = _ordered.lastOrNull;
    if (latest != null) {
      _expandedId = latest.id;
      _current = _ordered.length - 1;
    }
    _scroll.addListener(_handleScroll);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (latest != null) {
        unawaited(widget.controller.loadExchangeDetail(latest.id));
      }
      _revealInitialLatest();
    });
  }

  @override
  void didUpdateWidget(covariant EvidenceConversationTimeline oldWidget) {
    super.didUpdateWidget(oldWidget);
    final oldOrdered = [...oldWidget.activities]..sort(_compareActivity);
    final newOrdered = [...widget.activities]..sort(_compareActivity);
    final oldIds = oldOrdered.map((value) => value.id).toSet();
    final newNewest = newOrdered.lastOrNull;
    if (newNewest != null && !oldIds.contains(newNewest.id) && _nearBottom) {
      _expandedId = newNewest.id;
      _current = newOrdered.length - 1;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!mounted) return;
        unawaited(widget.controller.loadExchangeDetail(newNewest.id));
        _animateToLatest();
      });
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
    _itemKeys.removeWhere((id, _) => !newOrdered.any((item) => item.id == id));
  }

  static int _compareActivity(ActivityRecord left, ActivityRecord right) {
    final time = left.occurredAt.compareTo(right.occurredAt);
    return time != 0 ? time : left.id.compareTo(right.id);
  }

  void _handleScroll() {
    if (!_scroll.hasClients) return;
    final position = _scroll.position;
    final distanceFromBottom = position.maxScrollExtent - position.pixels;
    final nearBottom = distanceFromBottom < 150;
    final atBottom = distanceFromBottom <= 24;
    final lastIndex = math.max(0, _ordered.length - 1);
    // A tall expanded Turn can keep its top above the viewport even when the
    // user has reached the end of the conversation. At the bottom, the latest
    // Turn is the useful and truthful map selection.
    final current =
        _navigationTarget ??
        (atBottom
            ? lastIndex
            : _currentVisibleIndex(
                position.pixels,
              ).clamp(0, lastIndex).toInt());
    if (nearBottom == _nearBottom && current == _current) return;
    setState(() {
      _nearBottom = nearBottom;
      _current = current;
    });
    _keepMapVisible(current);
  }

  int _currentVisibleIndex(double offset) {
    final activities = _ordered;
    var visible = -1;
    for (var index = 0; index < activities.length; index += 1) {
      final target = _revealedOffset(activities[index].id);
      if (target == null) continue;
      if (target <= offset + 10) {
        visible = index;
      } else {
        break;
      }
    }
    if (visible >= 0) return visible;
    return (offset / _collapsedExtent).floor();
  }

  double _offsetFor(int index) {
    final activities = _ordered;
    final exact = _revealedOffset(activities[index].id);
    if (exact != null) return exact;
    final expanded = activities.indexWhere(
      (activity) => activity.id == _expandedId,
    );
    return index * _collapsedExtent +
        (expanded >= 0 && expanded < index
            ? _expandedHeight() - _collapsedExtent
            : 0);
  }

  double? _revealedOffset(String id) {
    final object = _itemKeys[id]?.currentContext?.findRenderObject();
    if (object == null || !object.attached) return null;
    final viewport = RenderAbstractViewport.maybeOf(object);
    return viewport?.getOffsetToReveal(object, 0).offset;
  }

  double _expandedHeight() {
    final id = _expandedId;
    if (id == null) return _collapsedExtent;
    final object = _itemKeys[id]?.currentContext?.findRenderObject();
    return object is RenderBox && object.hasSize
        ? object.size.height
        : _expandedExtentEstimate;
  }

  void _keepMapVisible(int index) {
    if (!_mapScroll.hasClients) return;
    final target = (index * _mapItemExtent - 80).clamp(
      0.0,
      _mapScroll.position.maxScrollExtent,
    );
    _mapScroll.animateTo(
      target,
      duration: const Duration(milliseconds: 100),
      curve: Curves.easeOut,
    );
  }

  void _revealInitialLatest() {
    if (!_scroll.hasClients || _ordered.isEmpty) return;
    _scroll.jumpTo(_scroll.position.maxScrollExtent);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || _ordered.isEmpty) return;
      _goTo(_ordered.length - 1);
    });
  }

  void _animateToLatest() {
    if (!_scroll.hasClients) return;
    _scroll.animateTo(
      _scroll.position.maxScrollExtent,
      duration: const Duration(milliseconds: 180),
      curve: Curves.easeOut,
    );
  }

  void _openLatest() {
    final latest = _ordered.lastOrNull;
    if (latest == null) return;
    if (_expandedId != latest.id) {
      setState(() {
        _expandedId = latest.id;
        _current = _ordered.length - 1;
      });
      unawaited(widget.controller.loadExchangeDetail(latest.id));
    }
    WidgetsBinding.instance.addPostFrameCallback((_) => _animateToLatest());
  }

  void _goTo(int index) {
    if (!_scroll.hasClients) return;
    _navigationTarget = index;
    if (_current != index) {
      setState(() => _current = index);
      _keepMapVisible(index);
    }
    unawaited(
      _navigateTo(index).whenComplete(() {
        if (_navigationTarget == index) _navigationTarget = null;
      }),
    );
  }

  Future<void> _navigateTo(int index) async {
    if (!_scroll.hasClients) return;
    final activities = _ordered;
    final itemContext = _itemKeys[activities[index].id]?.currentContext;
    if (itemContext != null) {
      await Scrollable.ensureVisible(
        itemContext,
        alignment: 0,
        duration: const Duration(milliseconds: 180),
        curve: Curves.easeOut,
      );
      return;
    }
    await _scroll.animateTo(
      _offsetFor(index).clamp(0, _scroll.position.maxScrollExtent),
      duration: const Duration(milliseconds: 180),
      curve: Curves.easeOut,
    );
    await WidgetsBinding.instance.endOfFrame;
    final context = _itemKeys[activities[index].id]?.currentContext;
    if (context == null || !context.mounted) return;
    await Scrollable.ensureVisible(
      context,
      alignment: 0,
      duration: const Duration(milliseconds: 100),
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
          height: 30,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 14),
            child: LayoutBuilder(
              builder: (context, constraints) => Row(
                children: [
                  Expanded(
                    child: Row(
                      children: [
                        Flexible(
                          child: Text(
                            widget.title ??
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
                        if (widget.showCount) ...[
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
                        ],
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
                      ],
                    ),
                  ),
                  if (!_nearBottom)
                    if (constraints.maxWidth < 520)
                      IconButton(
                        key: const Key('conversation-scroll-latest'),
                        tooltip: widget.copy('conversation.scroll_latest'),
                        onPressed: _openLatest,
                        icon: const Icon(Icons.arrow_downward, size: 15),
                      )
                    else
                      TextButton.icon(
                        key: const Key('conversation-scroll-latest'),
                        onPressed: _openLatest,
                        icon: const Icon(Icons.arrow_downward, size: 13),
                        label: Text(widget.copy('conversation.scroll_latest')),
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
                    key: const Key('conversation-timeline-scroll'),
                    controller: _scroll,
                    padding: const EdgeInsets.fromLTRB(14, 8, 10, 14),
                    itemCount: activities.length,
                    itemBuilder: (context, index) {
                      final activity = activities[index];
                      final expanded = activity.id == _expandedId;
                      return _TurnEvidenceItem(
                        key: _itemKeys.putIfAbsent(activity.id, GlobalKey.new),
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
                  width: 38,
                  decoration: BoxDecoration(
                    color: context.viberColors.panel,
                    border: Border(
                      left: BorderSide(color: context.viberColors.divider),
                    ),
                  ),
                  child: Semantics(
                    label: widget.copy('conversation.map'),
                    child: ListView.builder(
                      controller: _mapScroll,
                      padding: const EdgeInsets.symmetric(vertical: 8),
                      itemExtent: _mapItemExtent,
                      itemCount: activities.length,
                      itemBuilder: (context, index) {
                        final selected = index == _current;
                        final label = widget.copy.format('conversation.turn', {
                          'number': index + 1,
                        });
                        return Tooltip(
                          message: label,
                          child: Semantics(
                            button: true,
                            selected: selected,
                            label: label,
                            child: InkWell(
                              key: Key('conversation-map-turn-${index + 1}'),
                              onTap: () => _goTo(index),
                              canRequestFocus: true,
                              child: Center(
                                child: AnimatedContainer(
                                  duration: const Duration(milliseconds: 100),
                                  width: selected ? 22 : 8,
                                  height: selected ? 14 : 2,
                                  alignment: Alignment.center,
                                  decoration: BoxDecoration(
                                    color: selected
                                        ? context.viberColors.route.withValues(
                                            alpha: 0.22,
                                          )
                                        : activities[index].status == 'failed'
                                        ? context.viberColors.danger
                                        : activities[index].status == 'pending'
                                        ? context.viberColors.warning
                                        : context.viberColors.textFaint,
                                    borderRadius: ViberMetrics.controlRadius,
                                    border: selected
                                        ? Border.all(
                                            color: context.viberColors.route,
                                          )
                                        : null,
                                  ),
                                  child: selected
                                      ? Text(
                                          '${index + 1}',
                                          style: Theme.of(context)
                                              .textTheme
                                              .labelMedium
                                              ?.copyWith(
                                                fontSize: ViberType.micro,
                                                color:
                                                    context.viberColors.route,
                                              ),
                                        )
                                      : null,
                                ),
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
    super.key,
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
        ? context.viberColors.danger
        : pending
        ? context.viberColors.warning
        : context.viberColors.verified;
    final statusLabel = _localizedCopy(
      copy,
      'activity.status',
      activity.status,
    );
    final endpointLabel = controller.activityEndpointLabel(activity);
    final accountLabel = controller.activityAccountLabel(activity);
    final detail = controller.exchangeDetail(activity.id);
    final evidenceSummary = _turnEvidenceSummary(detail, copy);
    final displayPath = [
      if (endpointLabel.isNotEmpty) endpointLabel,
      accountLabel.isEmpty ? copy('common.client_passthrough') : accountLabel,
    ].join('  ›  ');
    final exactPath = [
      '${copy('flow.route')}: ${activity.routeId}',
      '${copy('flow.account')}: ${activity.accountId ?? copy('common.client_passthrough')}',
    ].join('\n');
    return Semantics(
      container: true,
      button: true,
      expanded: expanded,
      label:
          '${exchangeScoped ? copy('conversation.exchange') : copy.format('conversation.turn', {'number': number})}, ${activity.title}, $statusLabel',
      child: Stack(
        children: [
          if (!exchangeScoped)
            Positioned(
              left: 17,
              top: 16,
              bottom: 0,
              child: ColoredBox(
                color: context.viberColors.divider,
                child: const SizedBox(width: 1),
              ),
            ),
          Padding(
            padding: const EdgeInsets.only(left: 32, bottom: ViberSpacing.xs),
            child: Container(
              key: Key('conversation-turn-card-${activity.id}'),
              clipBehavior: Clip.antiAlias,
              decoration: BoxDecoration(
                color: context.viberColors.panel,
                border: Border.all(
                  color: expanded
                      ? context.viberColors.route.withValues(alpha: 0.5)
                      : context.viberColors.dividerSoft,
                ),
                borderRadius: ViberMetrics.surfaceRadius,
              ),
              child: Column(
                children: [
                  Tooltip(
                    message: copy(
                      expanded
                          ? 'conversation.collapse'
                          : 'conversation.expand',
                    ),
                    child: SizedBox(
                      height:
                          _EvidenceConversationTimelineState._collapsedExtent,
                      child: InkWell(
                        key: Key('conversation-turn-${activity.id}'),
                        onTap: onToggle,
                        canRequestFocus: true,
                        child: Padding(
                          padding: const EdgeInsets.fromLTRB(9, 4, 6, 4),
                          child: LayoutBuilder(
                            builder: (context, constraints) {
                              final compact = constraints.maxWidth < 360;
                              return Column(
                                mainAxisAlignment: MainAxisAlignment.center,
                                children: [
                                  Row(
                                    children: [
                                      Text(
                                        exchangeScoped
                                            ? copy('conversation.exchange')
                                            : copy.format('conversation.turn', {
                                                'number': number,
                                              }),
                                        style: Theme.of(context)
                                            .textTheme
                                            .labelMedium
                                            ?.copyWith(
                                              color: context.viberColors.route,
                                            ),
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
                                        InlineStatus(
                                          label: statusLabel,
                                          color: tone,
                                        ),
                                        const SizedBox(width: 8),
                                        Text(
                                          _clockTime(activity.occurredAt),
                                          style: monoStyle,
                                        ),
                                      ],
                                      const SizedBox(width: 4),
                                      Icon(
                                        expanded
                                            ? Icons.keyboard_arrow_up
                                            : Icons.keyboard_arrow_down,
                                        size: 16,
                                        color: context.viberColors.textMuted,
                                      ),
                                    ],
                                  ),
                                  const SizedBox(height: 3),
                                  Row(
                                    children: [
                                      if (compact) ...[
                                        Icon(
                                          Icons.circle,
                                          size: 6,
                                          color: tone,
                                        ),
                                        const SizedBox(width: 5),
                                        Text(
                                          _clockTime(activity.occurredAt),
                                          style: monoStyle,
                                        ),
                                        const SizedBox(width: 8),
                                      ],
                                      Expanded(
                                        child: Tooltip(
                                          message: exactPath,
                                          child: Text(
                                            displayPath,
                                            maxLines: 1,
                                            overflow: TextOverflow.ellipsis,
                                            style: Theme.of(context)
                                                .textTheme
                                                .bodySmall
                                                ?.copyWith(
                                                  fontWeight: FontWeight.w500,
                                                ),
                                          ),
                                        ),
                                      ),
                                      if (!compact &&
                                          evidenceSummary != null) ...[
                                        const SizedBox(width: 10),
                                        Flexible(
                                          child: Tooltip(
                                            message: evidenceSummary,
                                            child: Text(
                                              evidenceSummary,
                                              key: Key(
                                                'conversation-turn-summary-${activity.id}',
                                              ),
                                              maxLines: 1,
                                              overflow: TextOverflow.ellipsis,
                                              textAlign: TextAlign.end,
                                              style: monoStyle.copyWith(
                                                color: context
                                                    .viberColors
                                                    .textMuted,
                                              ),
                                            ),
                                          ),
                                        ),
                                      ],
                                    ],
                                  ),
                                ],
                              );
                            },
                          ),
                        ),
                      ),
                    ),
                  ),
                  if (expanded) ...[
                    const Divider(height: 1),
                    _ExchangeEvidencePanel(
                      activity: activity,
                      copy: copy,
                      showFull: showFull,
                      controller: controller,
                      onToggleFull: onToggleFull,
                    ),
                  ],
                ],
              ),
            ),
          ),
          Positioned(
            left: 13,
            // A timeline node represents the Turn, so it aligns to the first
            // header row's optical center rather than to the card border.
            top: 12,
            child: Container(
              key: Key('conversation-turn-node-${activity.id}'),
              width: 8,
              height: 8,
              decoration: BoxDecoration(
                color: tone,
                shape: BoxShape.circle,
                border: Border.all(
                  color: context.viberColors.canvas,
                  width: 1.5,
                ),
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
    return Padding(
      padding: const EdgeInsets.fromLTRB(10, 9, 10, 5),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(Icons.history, size: 13, color: context.viberColors.route),
              const SizedBox(width: 6),
              Flexible(
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
              const SizedBox(width: 2),
              IconButton(
                key: Key('exchange-environment-${activity.id}'),
                tooltip: copy.format('environment.history.inspect', {
                  'revision': activity.environment.revision,
                }),
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
              ),
            ],
          ),
          if (detail.clientIdentity case final identity?) ...[
            const SizedBox(height: 5),
            _ClientIdentityDisclosure(
              exchangeId: detail.id,
              identity: identity,
              copy: copy,
            ),
          ],
          const SizedBox(height: 6),
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
                color: context.viberColors.route.withValues(alpha: 0.07),
                child: Text(
                  copy.format('exchange.projection', {
                    'relationship': _localizedCopy(
                      copy,
                      'exchange.projection.relationship',
                      projection.relationship,
                    ),
                    'visible': content.request!.messages.length,
                    'total': projection.totalMessageCount,
                  }),
                  style: monoStyle,
                ),
              ),
            ],
            const SizedBox(height: 8),
            for (final (index, message) in content.request!.messages.indexed)
              _MessageCard(
                id: '${activity.id}-$index',
                message: message,
                copy: copy,
              ),
            if (content.response case final response?)
              _ResponseCard(response: response, copy: copy)
            else
              _PendingResponse(copy: copy),
          ],
          const SizedBox(height: 3),
          _EvidenceDisclosure(detail: detail, copy: copy),
          _RawEvidenceDisclosure(
            exchangeId: activity.id,
            controller: controller,
            copy: copy,
          ),
        ],
      ),
    );
  }
}

final class _ClientIdentityDisclosure extends StatefulWidget {
  const _ClientIdentityDisclosure({
    required this.exchangeId,
    required this.identity,
    required this.copy,
  });

  final String exchangeId;
  final AgentClientIdentity identity;
  final AppCopy copy;

  @override
  State<_ClientIdentityDisclosure> createState() =>
      _ClientIdentityDisclosureState();
}

final class _ClientIdentityDisclosureState
    extends State<_ClientIdentityDisclosure> {
  bool _expanded = false;
  String? _copied;

  @override
  Widget build(BuildContext context) {
    final identity = widget.identity;
    final copy = widget.copy;
    final clientLabel = copy('exchange.client.${identity.client}');
    final actorLabel = identity.actorLabel?.trim();
    final roleLabel = identity.actorIsSubagent
        ? copy('exchange.client.subagent')
        : copy('exchange.client.main');
    final summary = <String>[
      if (actorLabel != null && actorLabel.isNotEmpty) actorLabel,
      roleLabel,
      copy.format('exchange.client.session_short', {
        'id': _compactClientIdentity(identity.sessionId),
      }),
    ].join('  ·  ');
    final profile = AgentClientProfile.resolve(identity.client);
    final groupedRows = _clientIdentityRows(identity, profile, copy);

    return DecoratedBox(
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Semantics(
            button: true,
            expanded: _expanded,
            label: copy.format('exchange.client.evidence', {
              'client': clientLabel,
            }),
            child: InkWell(
              key: Key('exchange-client-evidence-${widget.exchangeId}'),
              borderRadius: ViberMetrics.controlRadius,
              onTap: () => setState(() => _expanded = !_expanded),
              child: Padding(
                padding: const EdgeInsets.fromLTRB(6, 4, 5, 4),
                child: Row(
                  children: [
                    AgentIdentityMark(
                      candidates: [identity.client],
                      fallbackLabel: clientLabel,
                      fallbackIcon: Icons.terminal,
                      size: 20,
                      glyphSize: 13,
                    ),
                    const SizedBox(width: 6),
                    Text(
                      clientLabel,
                      style: Theme.of(context).textTheme.labelLarge,
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        summary,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ),
                    Icon(
                      _expanded
                          ? Icons.keyboard_arrow_up
                          : Icons.keyboard_arrow_down,
                      size: 15,
                      color: context.viberColors.textMuted,
                    ),
                  ],
                ),
              ),
            ),
          ),
          if (_expanded) ...[
            Divider(height: 1, color: context.viberColors.dividerSoft),
            for (final family in AgentClientEvidenceFamily.values)
              if (groupedRows[family] case final rows? when rows.isNotEmpty)
                _ClientIdentitySection(
                  copy: copy,
                  title: copy(_clientIdentityFamilyTitle(family)),
                  rows: rows,
                  copied: _copied,
                  onCopy: _copy,
                ),
          ],
        ],
      ),
    );
  }

  Future<void> _copy(_ClientIdentityRowValue row) async {
    await Clipboard.setData(ClipboardData(text: row.value));
    if (mounted) setState(() => _copied = row.key);
  }
}

final class _ClientIdentitySection extends StatelessWidget {
  const _ClientIdentitySection({
    required this.copy,
    required this.rows,
    required this.copied,
    required this.onCopy,
    this.title,
  });

  final String? title;
  final AppCopy copy;
  final List<_ClientIdentityRowValue> rows;
  final String? copied;
  final ValueChanged<_ClientIdentityRowValue> onCopy;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(7, 6, 5, 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (title case final title?) ...[
            Text(
              title,
              style: Theme.of(context).textTheme.labelMedium?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
            const SizedBox(height: 3),
          ],
          for (final row in rows)
            _ClientIdentityValueRow(
              row: row,
              copied: copied == row.key,
              copyTooltip: copy.format('common.copy', {'field': row.label}),
              onCopy: () => onCopy(row),
            ),
        ],
      ),
    );
  }
}

final class _ClientIdentityRowValue {
  const _ClientIdentityRowValue({
    required this.label,
    required this.nativeName,
    required this.value,
    required this.family,
    this.order = 0,
  });

  final String label;
  final String nativeName;
  final String value;
  final AgentClientEvidenceFamily family;
  final int order;

  String get key => '$nativeName\u0000$value';
}

final class _ClientIdentityValueRow extends StatelessWidget {
  const _ClientIdentityValueRow({
    required this.row,
    required this.copied,
    required this.copyTooltip,
    required this.onCopy,
  });

  final _ClientIdentityRowValue row;
  final bool copied;
  final String copyTooltip;
  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    final label = Tooltip(
      message: row.nativeName,
      child: Text(
        row.label,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: Theme.of(
          context,
        ).textTheme.labelSmall?.copyWith(color: context.viberColors.textMuted),
      ),
    );
    final value = SelectableText(
      row.value,
      style: monoStyle.copyWith(color: context.viberColors.text),
    );
    final copyButton = IconButton(
      tooltip: copyTooltip,
      onPressed: onCopy,
      icon: Icon(
        copied ? Icons.check : Icons.content_copy,
        size: 12,
        color: copied
            ? context.viberColors.verified
            : context.viberColors.textMuted,
      ),
    );
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: LayoutBuilder(
        builder: (context, constraints) {
          if (constraints.maxWidth < 520) {
            return Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [label, const SizedBox(height: 1), value],
                  ),
                ),
                copyButton,
              ],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SizedBox(width: 138, child: label),
              Expanded(child: value),
              copyButton,
            ],
          );
        },
      ),
    );
  }
}

String _compactClientIdentity(String value) {
  if (value.length <= 16) return value;
  return '${value.substring(0, 8)}…${value.substring(value.length - 4)}';
}

Map<AgentClientEvidenceFamily, List<_ClientIdentityRowValue>>
_clientIdentityRows(
  AgentClientIdentity identity,
  AgentClientProfile profile,
  AppCopy copy,
) {
  final rows = <AgentClientEvidenceFamily, List<_ClientIdentityRowValue>>{
    for (final family in AgentClientEvidenceFamily.values) family: [],
  };
  final consumed = <String>{};
  void add({
    required String label,
    required String nativeName,
    required String value,
    required AgentClientEvidenceFamily family,
    required int order,
  }) {
    final key = '$nativeName\u0000$value';
    if (value.isEmpty || !consumed.add(key)) return;
    rows[family]!.add(
      _ClientIdentityRowValue(
        label: label,
        nativeName: nativeName,
        value: value,
        family: family,
        order: order,
      ),
    );
  }

  final sessionName = '${identity.client}.session_id';
  add(
    label: copy('exchange.client.field.session_id'),
    nativeName: sessionName,
    value: identity.sessionId,
    family: AgentClientEvidenceFamily.session,
    order: 10,
  );
  if (identity.sessionResumable) {
    add(
      label: copy('exchange.client.resume_command'),
      nativeName: '${identity.client}.resume_command',
      value: profile.resumeCommand(identity.sessionId),
      family: AgentClientEvidenceFamily.session,
      order: 0,
    );
  }
  if (identity.actorId case final actorId?) {
    final actorName = identity.client == 'codex'
        ? 'codex.thread_id'
        : 'claude.agent_id';
    add(
      label: copy(
        identity.client == 'codex'
            ? 'exchange.client.field.thread_id'
            : 'exchange.client.field.agent_id',
      ),
      nativeName: actorName,
      value: actorId,
      family: AgentClientEvidenceFamily.agent,
      order: 10,
    );
  }
  if (identity.providerResponseId case final responseId?) {
    add(
      label: copy('exchange.client.response'),
      nativeName: '${identity.client}.provider_response_id',
      value: responseId,
      family: AgentClientEvidenceFamily.request,
      order: 0,
    );
  }
  if (identity.providerMessageId case final messageId?
      when messageId != identity.providerResponseId) {
    add(
      label: copy('exchange.client.message'),
      nativeName: '${identity.client}.provider_message_id',
      value: messageId,
      family: AgentClientEvidenceFamily.request,
      order: 1,
    );
  }
  for (final evidence in [...identity.protocolIds, ...identity.attributes]) {
    final spec = profile.field(evidence.name);
    add(
      label: _clientEvidenceLabel(copy, spec, evidence.name),
      nativeName: evidence.name,
      value: evidence.value,
      family: spec.family,
      order: spec.order,
    );
  }
  for (final familyRows in rows.values) {
    familyRows.sort((left, right) {
      final byOrder = left.order.compareTo(right.order);
      if (byOrder != 0) return byOrder;
      final byName = left.nativeName.compareTo(right.nativeName);
      return byName != 0 ? byName : left.value.compareTo(right.value);
    });
  }
  return rows;
}

String _clientIdentityFamilyTitle(AgentClientEvidenceFamily family) =>
    switch (family) {
      AgentClientEvidenceFamily.session => 'exchange.client.group.session',
      AgentClientEvidenceFamily.agent => 'exchange.client.group.agent',
      AgentClientEvidenceFamily.request => 'exchange.client.group.request',
      AgentClientEvidenceFamily.client => 'exchange.client.group.client',
    };

String _clientEvidenceLabel(
  AppCopy copy,
  AgentClientFieldSpec spec,
  String nativeName,
) {
  if (spec.labelKey.isNotEmpty) return copy(spec.labelKey);
  final leaf = nativeName.split('.').last.replaceAll('_', ' ');
  return leaf.isEmpty
      ? nativeName
      : '${leaf[0].toUpperCase()}${leaf.substring(1)}';
}

final class _EvidenceDisclosure extends StatefulWidget {
  const _EvidenceDisclosure({required this.detail, required this.copy});

  final ExchangeDetail detail;
  final AppCopy copy;

  @override
  State<_EvidenceDisclosure> createState() => _EvidenceDisclosureState();
}

final class _EvidenceDisclosureState extends State<_EvidenceDisclosure> {
  bool _expanded = false;
  bool _technical = false;

  @override
  Widget build(BuildContext context) {
    final detail = widget.detail;
    final copy = widget.copy;
    final attemptCount = detail.processingTrace.attempts.length;
    final summary =
        '${copy.format(attemptCount == 1 ? 'exchange.attempt.one' : 'exchange.attempt.many', {'count': attemptCount})}  ·  ${_localizedCopy(copy, 'activity.status', detail.processingTrace.result)}';
    return DecoratedBox(
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Semantics(
            button: true,
            expanded: _expanded,
            child: InkWell(
              onTap: () => setState(() => _expanded = !_expanded),
              child: Padding(
                padding: const EdgeInsets.symmetric(vertical: 5),
                child: Row(
                  children: [
                    Flexible(
                      flex: 3,
                      child: Text(
                        copy('exchange.evidence'),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      flex: 2,
                      child: Text(
                        summary,
                        key: Key('exchange-evidence-summary-${detail.id}'),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ),
                    Icon(
                      _expanded
                          ? Icons.keyboard_arrow_up
                          : Icons.keyboard_arrow_down,
                      size: 16,
                    ),
                  ],
                ),
              ),
            ),
          ),
          if (_expanded) ...[
            for (final attempt in detail.processingTrace.attempts)
              _AttemptRow(attempt: attempt, copy: copy),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: const Key('exchange-technical-details'),
                onPressed: () => setState(() => _technical = !_technical),
                icon: Icon(
                  _technical ? Icons.expand_less : Icons.info_outline,
                  size: 13,
                ),
                label: Text(copy('common.technical_details')),
              ),
            ),
            if (_technical) _FrozenEvidence(detail: detail, copy: copy),
          ],
        ],
      ),
    );
  }
}

final class _RawEvidenceDisclosure extends StatefulWidget {
  const _RawEvidenceDisclosure({
    required this.exchangeId,
    required this.controller,
    required this.copy,
  });

  final String exchangeId;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_RawEvidenceDisclosure> createState() => _RawEvidenceDisclosureState();
}

final class _RawEvidenceDisclosureState extends State<_RawEvidenceDisclosure> {
  bool _expanded = false;
  bool _revealing = false;
  RevealedRawEvidence? _revealed;

  @override
  void didUpdateWidget(covariant _RawEvidenceDisclosure oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.exchangeId != widget.exchangeId) {
      _clearRevealed();
      _expanded = false;
      _revealing = false;
    }
  }

  @override
  void dispose() {
    _clearRevealed();
    super.dispose();
  }

  void _clearRevealed() {
    final body = _revealed?.body;
    if (body != null) body.fillRange(0, body.length, 0);
    _revealed = null;
  }

  void _toggle() {
    setState(() {
      _expanded = !_expanded;
      if (!_expanded) _clearRevealed();
    });
    if (_expanded && widget.controller.rawEvidence(widget.exchangeId) == null) {
      unawaited(widget.controller.loadRawEvidence(widget.exchangeId));
    }
  }

  Future<void> _reveal(RawEvidenceEnvelope envelope) async {
    setState(() => _revealing = true);
    final revealed = await widget.controller.revealRawEvidence(
      exchangeId: widget.exchangeId,
      envelopeId: envelope.envelopeId,
    );
    if (!mounted) {
      revealed?.body.fillRange(0, revealed.body.length, 0);
      return;
    }
    setState(() {
      _clearRevealed();
      _revealed = revealed;
      _revealing = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final page = widget.controller.rawEvidence(widget.exchangeId);
    final count = page?.items.length;
    return DecoratedBox(
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Semantics(
            button: true,
            expanded: _expanded,
            child: InkWell(
              key: Key('exchange-raw-${widget.exchangeId}'),
              onTap: _toggle,
              child: Padding(
                padding: const EdgeInsets.symmetric(vertical: 5),
                child: Row(
                  children: [
                    Icon(
                      Icons.data_object,
                      size: 14,
                      color: context.viberColors.textMuted,
                    ),
                    const SizedBox(width: 6),
                    Text(
                      copy('exchange.raw.title'),
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                    if (count != null) ...[
                      const SizedBox(width: 10),
                      Text(
                        copy.format('exchange.raw.summary', {'count': count}),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                    const Spacer(),
                    Icon(
                      _expanded
                          ? Icons.keyboard_arrow_up
                          : Icons.keyboard_arrow_down,
                      size: 16,
                    ),
                  ],
                ),
              ),
            ),
          ),
          if (_expanded) _body(context, page),
        ],
      ),
    );
  }

  Widget _body(BuildContext context, RawEvidencePage? page) {
    final copy = widget.copy;
    if (widget.controller.rawEvidenceError(widget.exchangeId)
        case final error?) {
      return Padding(
        padding: const EdgeInsets.only(bottom: 6),
        child: InlineNotice(message: error, error: true),
      );
    }
    if (page == null ||
        widget.controller.rawEvidenceIsLoading(widget.exchangeId)) {
      return Padding(
        padding: const EdgeInsets.only(bottom: 7),
        child: Row(
          children: [
            const SizedBox(
              width: 13,
              height: 13,
              child: CircularProgressIndicator(strokeWidth: 1.4),
            ),
            const SizedBox(width: 7),
            Text(copy('exchange.raw.loading')),
          ],
        ),
      );
    }
    if (page.items.isEmpty) {
      return Padding(
        padding: const EdgeInsets.only(bottom: 7),
        child: page.writer.degraded
            ? InlineNotice(
                message: copy.format('exchange.raw.writer_degraded', {
                  'count': page.writer.queueRecords,
                }),
                error: true,
              )
            : Text(
                copy('exchange.raw.empty'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
      );
    }
    final recovery = page.recovery;
    return Padding(
      padding: const EdgeInsets.only(bottom: 7),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (page.writer.degraded) ...[
            Tooltip(
              message: page.writer.lastFailure ?? '',
              child: InlineNotice(
                message: copy.format('exchange.raw.writer_degraded', {
                  'count': page.writer.queueRecords,
                }),
                error: true,
              ),
            ),
            const SizedBox(height: 6),
          ],
          if (recovery.recoveredUncleanWriters > 0) ...[
            InlineNotice(
              message: copy.format('exchange.raw.recovery', {
                'ms': recovery.maximumPossibleLossMs,
              }),
            ),
            const SizedBox(height: 6),
          ],
          for (final envelope in page.items) ...[
            _RawEnvelopeRow(
              envelope: envelope,
              revealed: _revealed?.envelope.envelopeId == envelope.envelopeId
                  ? _revealed
                  : null,
              revealing: _revealing,
              copy: copy,
              onReveal: () => _reveal(envelope),
              onHide: () => setState(_clearRevealed),
            ),
            if (envelope != page.items.last) const SizedBox(height: 5),
          ],
        ],
      ),
    );
  }
}

final class _RawEnvelopeRow extends StatelessWidget {
  const _RawEnvelopeRow({
    required this.envelope,
    required this.revealed,
    required this.revealing,
    required this.copy,
    required this.onReveal,
    required this.onHide,
  });

  final RawEvidenceEnvelope envelope;
  final RevealedRawEvidence? revealed;
  final bool revealing;
  final AppCopy copy;
  final VoidCallback onReveal;
  final VoidCallback onHide;

  @override
  Widget build(BuildContext context) {
    final target = _rawTarget(envelope);
    final action =
        envelope.method ??
        (envelope.statusCode == null ? 'HTTP' : '${envelope.statusCode}');
    return Container(
      key: Key('raw-envelope-${envelope.envelopeId}'),
      width: double.infinity,
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 6, 6, 6),
            child: Row(
              children: [
                Icon(
                  _rawLayerIcon(envelope.layer),
                  size: 13,
                  color: context.viberColors.route,
                ),
                const SizedBox(width: 6),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Wrap(
                        spacing: 7,
                        runSpacing: 2,
                        crossAxisAlignment: WrapCrossAlignment.center,
                        children: [
                          Text(
                            copy('exchange.raw.layer.${envelope.layer}'),
                            style: Theme.of(context).textTheme.labelMedium,
                          ),
                          Text(action, style: monoStyle),
                          Text(
                            copy('exchange.raw.state.${envelope.payloadState}'),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(
                                  color: context.viberColors.textMuted,
                                ),
                          ),
                          Text(_bytes(envelope.bodyBytes), style: monoStyle),
                        ],
                      ),
                      if (target.isNotEmpty)
                        Tooltip(
                          message: target,
                          child: Text(
                            target,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: monoStyle.copyWith(
                              color: context.viberColors.textMuted,
                            ),
                          ),
                        ),
                    ],
                  ),
                ),
                if (envelope.containsSecret)
                  Tooltip(
                    message: copy('exchange.raw.contains_secret'),
                    child: Icon(
                      Icons.lock_outline,
                      size: 13,
                      color: context.viberColors.warning,
                    ),
                  ),
                const SizedBox(width: 4),
                if (revealed != null)
                  TextButton(
                    onPressed: onHide,
                    child: Text(copy('exchange.raw.hide')),
                  )
                else if (envelope.revealAvailable)
                  TextButton.icon(
                    key: Key('raw-reveal-${envelope.envelopeId}'),
                    onPressed: revealing ? null : onReveal,
                    icon: const Icon(Icons.visibility_outlined, size: 13),
                    label: Text(copy('exchange.raw.reveal')),
                  ),
              ],
            ),
          ),
          if (revealed != null) ...[
            Divider(height: 1, color: context.viberColors.dividerSoft),
            _RevealedRawPayload(value: revealed!, copy: copy),
          ],
        ],
      ),
    );
  }
}

final class _RevealedRawPayload extends StatelessWidget {
  const _RevealedRawPayload({required this.value, required this.copy});

  final RevealedRawEvidence value;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final textBody = _rawTextBody(value);
    return Padding(
      key: Key('raw-revealed-${value.envelope.envelopeId}'),
      padding: const EdgeInsets.all(8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _RawFields(
            title: copy('exchange.raw.headers'),
            fields: value.headers,
          ),
          if (value.trailers.isNotEmpty) ...[
            const SizedBox(height: 7),
            _RawFields(
              title: copy('exchange.raw.trailers'),
              fields: value.trailers,
            ),
          ],
          const SizedBox(height: 7),
          Text(
            textBody.binary
                ? copy('exchange.raw.body.base64')
                : copy('exchange.raw.body'),
            style: Theme.of(context).textTheme.labelMedium,
          ),
          const SizedBox(height: 3),
          Container(
            width: double.infinity,
            constraints: const BoxConstraints(maxHeight: 260),
            padding: const EdgeInsets.all(7),
            decoration: BoxDecoration(
              color: context.viberColors.input,
              border: Border.all(color: context.viberColors.dividerSoft),
              borderRadius: ViberMetrics.controlRadius,
            ),
            child: SingleChildScrollView(
              child: SelectableText(
                textBody.value.isEmpty
                    ? copy('exchange.raw.body.empty')
                    : textBody.value,
                style: monoStyle,
              ),
            ),
          ),
          if (value.frames.isNotEmpty) ...[
            const SizedBox(height: 7),
            Text(
              copy('exchange.raw.frames'),
              style: Theme.of(context).textTheme.labelMedium,
            ),
            const SizedBox(height: 2),
            SelectableText(
              value.frames
                  .map(
                    (frame) =>
                        '${frame.kind}  ${frame.offset}..${frame.offset + frame.length}',
                  )
                  .join('\n'),
              style: monoStyle,
            ),
          ],
        ],
      ),
    );
  }
}

final class _RawFields extends StatelessWidget {
  const _RawFields({required this.title, required this.fields});

  final String title;
  final List<RawHeaderField> fields;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(title, style: Theme.of(context).textTheme.labelMedium),
      const SizedBox(height: 2),
      SelectableText(
        fields
            .expand(
              (field) => field.values.isEmpty
                  ? ['${field.name}:']
                  : field.values.map((value) => '${field.name}: $value'),
            )
            .join('\n'),
        style: monoStyle,
      ),
    ],
  );
}

({String value, bool binary}) _rawTextBody(RevealedRawEvidence value) {
  if (value.body.isEmpty) return (value: '', binary: false);
  try {
    final decoded = utf8.decode(value.body, allowMalformed: false);
    final hasBinaryControls = decoded.runes.any(
      (character) =>
          character < 0x20 &&
          character != 0x09 &&
          character != 0x0a &&
          character != 0x0d,
    );
    if (!hasBinaryControls) return (value: decoded, binary: false);
  } on FormatException {
    // Preserve arbitrary bodies as exact Base64 rather than replacing bytes.
  }
  return (value: base64.encode(value.body), binary: true);
}

String _rawTarget(RawEvidenceEnvelope envelope) {
  final authority = envelope.authority ?? '';
  final path = envelope.path ?? '';
  final query = envelope.rawQuery;
  if (authority.isEmpty) return path;
  return '${envelope.scheme == null ? '' : '${envelope.scheme}://'}$authority$path${query == null ? '' : '?$query'}';
}

IconData _rawLayerIcon(String layer) => switch (layer) {
  'client_ingress' => Icons.login,
  'provider_egress' => Icons.north_east,
  'provider_response' => Icons.south_west,
  'client_downstream' => Icons.logout,
  _ => Icons.data_object,
};

final class _MessageCard extends StatefulWidget {
  const _MessageCard({
    required this.id,
    required this.message,
    required this.copy,
  });

  final String id;
  final ExchangeContentMessage message;
  final AppCopy copy;

  @override
  State<_MessageCard> createState() => _MessageCardState();
}

final class _MessageCardState extends State<_MessageCard> {
  late bool _expanded;

  @override
  void initState() {
    super.initState();
    _expanded = widget.message.role != 'system';
  }

  @override
  void didUpdateWidget(covariant _MessageCard oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.id != widget.id ||
        oldWidget.message.role != widget.message.role) {
      _expanded = widget.message.role != 'system';
    }
  }

  @override
  Widget build(BuildContext context) {
    final message = widget.message;
    final copy = widget.copy;
    final user = message.role == 'user' || message.role == 'tool';
    final system = message.role == 'system';
    final agent = message.agent;
    final size = message.blocks.fold<int>(
      0,
      (total, block) => total + block.originalSize,
    );
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 7),
      decoration: BoxDecoration(
        color: user
            ? context.viberColors.route.withValues(alpha: 0.055)
            : context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (system)
            Semantics(
              button: true,
              expanded: _expanded,
              label: copy(
                _expanded ? 'exchange.system.hide' : 'exchange.system.show',
              ),
              child: InkWell(
                key: Key('system-context-${widget.id}'),
                onTap: () => setState(() => _expanded = !_expanded),
                child: Padding(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 9,
                    vertical: 7,
                  ),
                  child: Row(
                    children: [
                      Expanded(
                        child: Text(
                          copy('exchange.role.system'),
                          style: Theme.of(context).textTheme.labelMedium
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                      ),
                      if (!_expanded) ...[
                        Text(
                          copy.format('exchange.system.collapsed', {
                            'size': _bytes(size),
                          }),
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                        const SizedBox(width: 6),
                      ],
                      Icon(
                        _expanded ? Icons.expand_less : Icons.expand_more,
                        size: 15,
                        color: context.viberColors.textMuted,
                      ),
                    ],
                  ),
                ),
              ),
            )
          else
            Padding(
              padding: const EdgeInsets.fromLTRB(9, 7, 9, 0),
              child: Row(
                children: [
                  if (agent != null) ...[
                    Icon(
                      Icons.account_tree_outlined,
                      size: 13,
                      color: context.viberColors.route,
                    ),
                    const SizedBox(width: 5),
                  ],
                  Text(
                    agent == null
                        ? copy('exchange.role.${message.role}')
                        : copy('exchange.agent.message'),
                    style: Theme.of(context).textTheme.labelMedium?.copyWith(
                      color: user
                          ? context.viberColors.route
                          : context.viberColors.textMuted,
                    ),
                  ),
                  if (agent != null) ...[
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        _agentDirection(agent),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: monoStyle.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      ),
                    ),
                  ],
                ],
              ),
            ),
          if (_expanded) ...[
            if (system)
              Divider(height: 1, color: context.viberColors.dividerSoft),
            Padding(
              padding: const EdgeInsets.fromLTRB(9, 5, 9, 8),
              child: _ExpandableContentRegion(
                key: ValueKey('message-content-${widget.id}'),
                copy: copy,
                estimatedLines: _estimatedContentLines(message.blocks),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    for (final block in message.blocks)
                      _ContentBlockView(block: block, copy: copy),
                  ],
                ),
              ),
            ),
          ],
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
    final agents = <String>[];
    for (final block in response.blocks) {
      final agent = block.agent;
      if (agent == null) continue;
      final label = _agentDirection(agent);
      if (!agents.contains(label)) agents.add(label);
    }
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 7),
      padding: const EdgeInsets.fromLTRB(9, 7, 9, 8),
      decoration: BoxDecoration(
        color: context.viberColors.verified.withValues(alpha: 0.045),
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          LayoutBuilder(
            builder: (context, constraints) {
              final identity = Wrap(
                spacing: 7,
                runSpacing: 2,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    agents.isEmpty
                        ? copy('exchange.role.assistant')
                        : copy('exchange.agent.message'),
                    style: Theme.of(context).textTheme.labelMedium?.copyWith(
                      color: context.viberColors.verified,
                    ),
                  ),
                  if (agents.isNotEmpty)
                    Text(
                      agents.join('  ·  '),
                      style: monoStyle.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                ],
              );
              final model = Text(
                '${response.reportedModel}  ·  ${_localizedCopy(copy, 'exchange.stop', response.stopReason)}',
                maxLines: constraints.maxWidth < 420 ? 2 : 1,
                overflow: TextOverflow.ellipsis,
                textAlign: constraints.maxWidth < 420
                    ? TextAlign.start
                    : TextAlign.end,
                style: monoStyle,
              );
              if (constraints.maxWidth < 420) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [identity, const SizedBox(height: 3), model],
                );
              }
              return Row(
                children: [
                  Expanded(child: identity),
                  const SizedBox(width: 10),
                  Flexible(child: model),
                ],
              );
            },
          ),
          const SizedBox(height: 5),
          _ExpandableContentRegion(
            copy: copy,
            estimatedLines: _estimatedContentLines(response.blocks),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final block in response.blocks)
                  _ContentBlockView(block: block, copy: copy),
              ],
            ),
          ),
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

const _defaultVisibleContentLines = 15;

int _estimatedContentLines(Iterable<ExchangeContentBlock> blocks) {
  var lines = 0;
  for (final block in blocks) {
    final text = switch (block.kind) {
      'tool_call' when block.arguments != null => const JsonEncoder.withIndent(
        '  ',
      ).convert(block.arguments),
      _ => block.text ?? '',
    };
    if (text.isEmpty) {
      lines += 1;
      continue;
    }
    for (final line in text.split('\n')) {
      // This is only an admission heuristic. The collapsed viewport itself is
      // measured in rendered line heights, so Markdown and code stay intact.
      lines += math.max(1, (line.runes.length / 72).ceil());
    }
  }
  return lines;
}

final class _ExpandableContentRegion extends StatefulWidget {
  const _ExpandableContentRegion({
    required this.copy,
    required this.estimatedLines,
    required this.child,
    super.key,
  });

  final AppCopy copy;
  final int estimatedLines;
  final Widget child;

  @override
  State<_ExpandableContentRegion> createState() =>
      _ExpandableContentRegionState();
}

final class _ExpandableContentRegionState
    extends State<_ExpandableContentRegion> {
  static const _collapsedHeight = 315.0;
  bool _expanded = false;

  bool get _collapsible => widget.estimatedLines > _defaultVisibleContentLines;

  @override
  void didUpdateWidget(covariant _ExpandableContentRegion oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.key != widget.key ||
        oldWidget.estimatedLines != widget.estimatedLines) {
      _expanded = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    final content = !_collapsible || _expanded
        ? widget.child
        : SizedBox(
            height: _collapsedHeight,
            child: ClipRect(
              child: OverflowBox(
                alignment: Alignment.topLeft,
                minHeight: 0,
                maxHeight: double.infinity,
                child: widget.child,
              ),
            ),
          );
    if (!_collapsible) return content;

    final label = widget.copy(
      _expanded
          ? 'exchange.content.show_15_lines'
          : 'exchange.content.show_all',
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        content,
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton.icon(
            key: const Key('toggle-long-exchange-content'),
            onPressed: () => setState(() => _expanded = !_expanded),
            icon: Icon(
              _expanded ? Icons.unfold_less : Icons.unfold_more,
              size: 15,
            ),
            label: Text(label),
          ),
        ),
      ],
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
      if (block.kind == 'provider_extension') {
        final digest = block.fingerprint?.replaceFirst('sha256:', '');
        final shortDigest = digest?.substring(0, math.min(10, digest.length));
        return Container(
          width: double.infinity,
          margin: const EdgeInsets.only(top: 3),
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
          decoration: BoxDecoration(
            color: context.viberColors.canvas,
            border: Border.all(color: context.viberColors.dividerSoft),
            borderRadius: ViberMetrics.controlRadius,
          ),
          child: Row(
            children: [
              Icon(
                Icons.enhanced_encryption_outlined,
                size: 14,
                color: context.viberColors.textMuted,
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  copy(_providerEvidenceCopyKey(block.providerKind)),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ),
              Text(
                '${_bytes(block.originalSize)}${shortDigest == null ? '' : '  ·  $shortDigest'}',
                style: monoStyle.copyWith(color: context.viberColors.textMuted),
              ),
            ],
          ),
        );
      }
      return Text(
        copy.format('exchange.content.omitted', {'bytes': block.originalSize}),
        style: Theme.of(context).textTheme.bodySmall,
      );
    }
    if (block.kind == 'tool_call') {
      final multiAgent = block.toolNamespace == 'multi_agent';
      return Container(
        width: double.infinity,
        margin: const EdgeInsets.only(top: 3),
        padding: const EdgeInsets.all(7),
        color: context.viberColors.warning.withValues(alpha: 0.07),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  multiAgent
                      ? Icons.account_tree_outlined
                      : Icons.build_outlined,
                  size: 13,
                  color: context.viberColors.warning,
                ),
                const SizedBox(width: 5),
                Text(
                  multiAgent
                      ? copy('exchange.agent.action.${block.toolName}')
                      : (block.toolName ?? copy('exchange.tool.unknown')),
                ),
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
        title: Text(
          copy(
            block.providerKind == 'reasoning_summary'
                ? 'exchange.content.reasoning_summary'
                : 'exchange.content.reasoning',
          ),
        ),
        children: [SelectableText(block.text ?? '', style: monoStyle)],
      );
    }
    if (block.kind == 'tool_result') {
      return Container(
        width: double.infinity,
        padding: const EdgeInsets.all(7),
        color:
            (block.toolError
                    ? context.viberColors.danger
                    : context.viberColors.verified)
                .withValues(alpha: 0.06),
        child: SelectableText(block.text ?? '', style: monoStyle),
      );
    }
    final bodyStyle = Theme.of(context).textTheme.bodyMedium?.copyWith(
      color: block.kind == 'refusal'
          ? context.viberColors.danger
          : context.viberColors.text,
      height: 1.35,
    );
    final segments = _splitAfterFencedBlocks(block.text ?? '');
    Widget markdown(String data, int index) => MarkdownBody(
      key: ValueKey('markdown-${block.kind}-${block.originalSize}-$index'),
      data: data,
      selectable: true,
      softLineBreak: true,
      builders: {'pre': _WrappingCodeBlockBuilder()},
      imageBuilder: (uri, title, alt) => Text(
        alt == null || alt.isEmpty ? '[image]' : '[image: $alt]',
        style: bodyStyle?.copyWith(color: context.viberColors.textFaint),
      ),
      styleSheet: MarkdownStyleSheet.fromTheme(Theme.of(context)).copyWith(
        p: bodyStyle,
        pPadding: EdgeInsets.zero,
        h1: Theme.of(context).textTheme.titleMedium,
        h1Padding: const EdgeInsets.only(bottom: 6),
        h2: Theme.of(context).textTheme.titleSmall,
        h2Padding: const EdgeInsets.only(bottom: 4),
        h3: Theme.of(context).textTheme.titleSmall,
        h3Padding: const EdgeInsets.only(bottom: 4),
        strong: bodyStyle?.copyWith(fontWeight: FontWeight.w600),
        a: bodyStyle?.copyWith(
          color: context.viberColors.route,
          decoration: TextDecoration.underline,
        ),
        code: monoStyle.copyWith(
          color: context.viberColors.text,
          backgroundColor: context.viberColors.canvas,
        ),
        codeblockPadding: const EdgeInsets.all(8),
        codeblockDecoration: BoxDecoration(
          color: context.viberColors.canvas,
          borderRadius: ViberMetrics.controlRadius,
          border: Border.all(color: context.viberColors.dividerSoft),
        ),
        blockquotePadding: const EdgeInsets.fromLTRB(8, 4, 0, 4),
        blockquoteDecoration: BoxDecoration(
          border: Border(
            left: BorderSide(color: context.viberColors.divider, width: 2),
          ),
        ),
        listIndent: 18,
        blockSpacing: 7,
      ),
    );
    if (segments.length == 1) return markdown(segments.single, 0);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final (index, segment) in segments.indexed) ...[
          if (index > 0) const SizedBox(height: 11),
          markdown(segment, index),
        ],
      ],
    );
  }
}

final class _WrappingCodeBlockBuilder extends MarkdownElementBuilder {
  @override
  Widget? visitText(md.Text text, TextStyle? preferredStyle) {
    final value = text.text.replaceFirst(RegExp(r'\n$'), '');
    return SizedBox(
      key: const Key('markdown-code-block'),
      width: double.infinity,
      child: Padding(
        padding: const EdgeInsets.all(8),
        child: SelectableText(
          value,
          maxLines: null,
          textWidthBasis: TextWidthBasis.parent,
          style: preferredStyle ?? monoStyle,
        ),
      ),
    );
  }
}

List<String> _splitAfterFencedBlocks(String source) {
  final lines = source.split('\n');
  final segments = <String>[];
  var segmentStart = 0;
  String? fenceCharacter;
  var fenceLength = 0;

  for (var index = 0; index < lines.length; index += 1) {
    final line = lines[index];
    final trimmedLeft = line.trimLeft();
    final indent = line.length - trimmedLeft.length;
    if (fenceCharacter == null) {
      if (indent > 3 || trimmedLeft.isEmpty) continue;
      final character = trimmedLeft[0];
      if (character != '`' && character != '~') continue;
      final run = RegExp(
        '^${RegExp.escape(character)}+',
      ).firstMatch(trimmedLeft);
      final length = run?.group(0)?.length ?? 0;
      if (length < 3) continue;
      fenceCharacter = character;
      fenceLength = length;
      continue;
    }

    final trimmed = line.trim();
    final closing = RegExp(
      '^${RegExp.escape(fenceCharacter)}{$fenceLength,}\$',
    ).hasMatch(trimmed);
    if (!closing) continue;
    fenceCharacter = null;
    fenceLength = 0;
    final hasFollowingContent = lines
        .skip(index + 1)
        .any((candidate) => candidate.trim().isNotEmpty);
    if (!hasFollowingContent) continue;
    segments.add(lines.sublist(segmentStart, index + 1).join('\n'));
    segmentStart = index + 1;
  }

  segments.add(lines.sublist(segmentStart).join('\n'));
  return segments.where((segment) => segment.trim().isNotEmpty).toList();
}

final class _UsageValue extends StatelessWidget {
  const _UsageValue({required this.label, required this.value});

  final String label;
  final ExchangeUsageValue value;

  @override
  Widget build(BuildContext context) {
    return Text(
      '$label ${value.known ? value.tokens : '—'}',
      style: monoStyle.copyWith(color: context.viberColors.textMuted),
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
          Icon(
            Icons.schedule_outlined,
            size: 13,
            color: context.viberColors.warning,
          ),
          const SizedBox(width: 7),
          Expanded(child: Text(copy('exchange.response.pending'))),
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
    final resultLabel = _localizedCopy(copy, 'activity.status', result);
    final location = [
      diagnosis.clientField,
      diagnosis.clientPath,
      diagnosis.providerField,
    ].whereType<String>().join(' · ');
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 8),
      padding: const EdgeInsets.all(8),
      color: context.viberColors.danger.withValues(alpha: 0.07),
      child: Text(
        '$resultLabel${diagnosis.providerStatus == null ? '' : ' · ${diagnosis.providerStatus}'}${location.isEmpty ? '' : ' · $location'}',
        style: monoStyle.copyWith(color: context.viberColors.danger),
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
    final entries = <(String, String)>[
      (copy('flow.environment'), '${value.id}@${value.revision}'),
      (copy('flow.endpoint'), value.clientEndpointId),
      (copy('flow.protocol'), value.protocolPlanId),
      (copy('flow.route'), '${value.routeId}@${value.routeRevision}'),
      (
        copy('flow.account'),
        value.accountId ?? copy('common.client_passthrough'),
      ),
      (copy('flow.digest'), value.digest),
    ];
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.fromLTRB(0, 2, 0, 6),
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
      ),
      child: Column(
        children: [for (final entry in entries) _TechnicalRow(entry: entry)],
      ),
    );
  }
}

final class _TechnicalRow extends StatelessWidget {
  const _TechnicalRow({required this.entry});

  final (String, String) entry;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final label = Text(
            entry.$1,
            style: monoStyle.copyWith(color: context.viberColors.textFaint),
          );
          final value = SelectableText(
            entry.$2,
            style: monoStyle.copyWith(color: context.viberColors.text),
          );
          if (constraints.maxWidth < 420) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [label, const SizedBox(height: 1), value],
            );
          }
          return Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SizedBox(width: 82, child: label),
              Expanded(child: value),
            ],
          );
        },
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
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
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
                  '${_localizedCopy(copy, 'network.value.purpose', attempt.purpose)}  ·  ${attempt.outcome == null ? copy('network.egress.running') : _localizedCopy(copy, 'network.value.outcome', attempt.outcome!)}  ·  ${copy.format('network.egress.bytes', {'out': _bytes(attempt.bytesOut), 'in': _bytes(attempt.bytesIn)})}',
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
    return Text.rich(
      TextSpan(
        children: [
          TextSpan(
            text: '$label ',
            style: TextStyle(color: context.viberColors.textFaint),
          ),
          TextSpan(text: value),
        ],
      ),
      style: monoStyle,
      maxLines: 1,
      overflow: TextOverflow.ellipsis,
    );
  }
}

String _clockTime(DateTime timestamp) {
  final local = timestamp.toLocal();
  return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}:${local.second.toString().padLeft(2, '0')}';
}

String? _turnEvidenceSummary(ExchangeDetail? detail, AppCopy copy) {
  if (detail == null) return null;
  final content = detail.content;
  final request = content.request;
  final response = content.response;
  final values = <String>[
    if (response?.reportedModel case final String model when model.isNotEmpty)
      model
    else if (request?.effectiveModel case final String model
        when model.isNotEmpty)
      model,
  ];
  void addUsage(String key, ExchangeUsageValue value) {
    if (!value.known || value.tokens == null) return;
    values.add('${copy(key)} ${value.tokens}');
  }

  if (response case final value?) {
    addUsage('exchange.usage.input', value.usage.inputUncached);
    addUsage('exchange.usage.cache_read', value.usage.cacheRead);
    addUsage('exchange.usage.output', value.usage.output);
  }
  final attemptCount = detail.processingTrace.attempts.length;
  values.add(
    copy.format(
      attemptCount == 1 ? 'exchange.attempt.one' : 'exchange.attempt.many',
      {'count': attemptCount},
    ),
  );
  return values.join('  ·  ');
}

String _localizedCopy(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  final localized = copy(key);
  if (localized != key) return localized;
  final words = value.replaceAll('-', ' ').replaceAll('_', ' ');
  if (words.isEmpty) return words;
  return '${words[0].toUpperCase()}${words.substring(1)}';
}

String _bytes(int value) {
  if (value < 1024) return '$value B';
  if (value < 1024 * 1024) return '${(value / 1024).toStringAsFixed(1)} KiB';
  return '${(value / (1024 * 1024)).toStringAsFixed(1)} MiB';
}

String _agentDirection(ExchangeAgentContext context) {
  final author = context.author;
  final recipient = context.recipient;
  if (author != null && recipient != null) return '$author → $recipient';
  return context.agentName ?? 'Agent';
}

String _providerEvidenceCopyKey(String? kind) {
  return switch (kind) {
    'thinking' => 'exchange.content.reasoning_signature',
    'reasoning_encrypted_content' => 'exchange.content.reasoning_encrypted',
    'redacted_thinking' => 'exchange.content.reasoning_redacted',
    'agent_message_encrypted_content' => 'exchange.content.agent_encrypted',
    'agent_message_image' => 'exchange.content.agent_image',
    'agent_message_file' => 'exchange.content.agent_file',
    'agent_message_screenshot' => 'exchange.content.agent_screenshot',
    _ => 'exchange.content.provider_state',
  };
}

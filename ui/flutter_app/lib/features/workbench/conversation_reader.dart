part of 'conversation_timeline.dart';

/// Reading and protocol inspection share the same retained blocks and renderers.
/// Only verified incremental input belongs on the conversation's main line.
final class ConversationReadingView extends StatefulWidget {
  const ConversationReadingView({
    required this.controller,
    required this.activities,
    required this.copy,
    this.canLoadEarlier = false,
    this.loadingEarlier = false,
    this.exchangeScoped = false,
    this.showCount = true,
    this.title,
    this.onLoadEarlier,
    this.storage,
    super.key,
  });

  final WorkbenchController controller;
  final List<ActivityRecord> activities;
  final AppCopy copy;
  final bool canLoadEarlier, loadingEarlier, exchangeScoped, showCount;
  final String? title;
  final VoidCallback? onLoadEarlier;
  final PageStorageBucket? storage;

  @override
  State<ConversationReadingView> createState() =>
      _ConversationReadingViewState();
}

enum _ReaderDetail { tools, reasoning, context, usage, raw, other }

final class _ToolLookupOutcome {
  const _ToolLookupOutcome(this.items, {required this.complete});
  final List<ToolResultMatch> items;
  final bool complete;
}

final class _CachedToolResults {
  const _CachedToolResults(this.items, this.bytes);
  final List<ToolResultMatch> items;
  final int bytes;
}

final class _ReaderSelection {
  const _ReaderSelection(
    this.activity,
    this.kind, {
    this.toolIds = const {},
    this.aggregateUsage = false,
    this.unkeyedTools = const [],
  });
  final ActivityRecord activity;
  final _ReaderDetail kind;
  final Set<String> toolIds;
  final bool aggregateUsage;
  final List<ExchangeContentBlock> unkeyedTools;
  String get key => '${activity.id}:${kind.name}:$aggregateUsage';
}

final class _ConversationReadingViewState
    extends State<ConversationReadingView> {
  late final _scroll = ScrollController(
    onAttach: (position) {
      _follow = position.pixels <= 48;
    },
  );
  final _panelScroll = ScrollController();
  final _itemKeys = <String, GlobalKey>{};
  final _usageLoads = <String, Future<RuntimeUsageReport?>>{};
  final _usage = <String, RuntimeUsageReport>{};
  final _usageVersions = <String, int>{};
  final _detailTabs = <String, int>{};
  final _toolLookups = <(String, String), Future<_ToolLookupOutcome>>{};
  final _toolResults = <(String, String), _CachedToolResults>{};
  int _toolLookupGeneration = 0, _toolResultBytes = 0;
  bool _restoreToolPanel = false;
  String? _refreshingToolPanel;
  int _panelNavigation = 0;
  final _panelOffsets = <String, double>{};
  final _pageStorage = PageStorageBucket();
  final _inspectorFocus = FocusNode(debugLabel: 'conversation-inspector');
  _ReaderSelection? _selection;
  Future<ExchangeDetail?>? _inspection;
  List<RuntimeUsageReport> _inspectedUsage = const [];
  FocusNode? _returnFocus;
  bool _reading = true, _legacyOpened = false, _follow = true;
  int _unread = 0;

  AppCopy get copy => widget.copy;
  List<ActivityRecord> get _ordered => [...widget.activities]
    ..sort((a, b) {
      final time = a.occurredAt.compareTo(b.occurredAt);
      return time == 0 ? a.id.compareTo(b.id) : time;
    });

  bool _showInput(ActivityRecord activity, String anchor) =>
      !widget.activities.any(
        (other) =>
            other.conversation.id == activity.conversation.id &&
            (other.occurredAt.isBefore(activity.occurredAt) ||
                (other.occurredAt == activity.occurredAt &&
                    other.id.compareTo(activity.id) < 0)) &&
            widget.controller
                    .exchangeDetail(other.id, contentView: 'reading')
                    ?.content
                    .page
                    ?.inputAnchor ==
                anchor,
      );

  void _inputLoaded() {
    if (mounted) setState(() {});
  }

  @override
  void initState() {
    super.initState();
    _scroll.addListener(_scrolled);
  }

  @override
  void didUpdateWidget(covariant ConversationReadingView oldWidget) {
    super.didUpdateWidget(oldWidget);
    final conversationChanged = _conversationIds(
      oldWidget.activities,
    ).intersection(_conversationIds(widget.activities)).isEmpty;
    if (oldWidget.controller != widget.controller || conversationChanged) {
      _usageLoads.clear();
      _usage.clear();
      _usageVersions.clear();
      _itemKeys.clear();
      _selection = null;
      _inspection = null;
      _detailTabs.clear();
      _invalidateToolLookups(clearResults: true);
      _panelOffsets.clear();
      _follow = true;
      _unread = 0;
    }
    final ids = widget.activities.map((a) => a.id).toSet();
    final previous = oldWidget.activities.map((a) => a.id).toSet();
    final deleted = previous.difference(ids);
    final changedConversations =
        {
              ..._conversationIds(oldWidget.activities),
              ..._conversationIds(widget.activities),
            }
            .where(
              (id) =>
                  _toolActivityVersion(oldWidget.activities, id) !=
                  _toolActivityVersion(widget.activities, id),
            )
            .toSet();
    final changedOrigins = {
      for (final activity in [...oldWidget.activities, ...widget.activities])
        if (changedConversations.contains(activity.conversation.id))
          activity.id,
    };
    _removeToolResults(
      (key, value) =>
          changedOrigins.contains(key.$1) ||
          deleted.contains(key.$1) ||
          value.items.any((item) => deleted.contains(item.exchangeId)),
    );
    final selection = _selection;
    if (selection != null &&
        changedConversations.contains(selection.activity.conversation.id)) {
      _beginToolPanelRefresh();
      _invalidateToolLookups();
    }
    final newest = _ordered.lastOrNull;
    if (newest != null && !previous.contains(newest.id)) {
      final restore = _beforeLayoutChange();
      if (!_follow ||
          _selection != null ||
          !_reading ||
          !(ModalRoute.of(context)?.isCurrent ?? true)) {
        _unread += ids.difference(previous).length;
      }
      restore();
    }
    for (final activity in widget.activities) {
      final old = oldWidget.activities
          .where((a) => a.id == activity.id)
          .firstOrNull;
      if (old != null && old.status != activity.status) {
        _usageLoads.remove(activity.id);
        _usageVersions[activity.id] = (_usageVersions[activity.id] ?? 0) + 1;
        _usage.remove(activity.id);
        // Do not replace the object being inspected during polling. Reopening
        // explicitly loads its current snapshot; the reader row refreshes below.
      }
    }
    _usage.removeWhere((id, _) => !ids.contains(id));
    _usageLoads.removeWhere((id, _) => !ids.contains(id));
    _usageVersions.removeWhere((id, _) => !ids.contains(id));
    _itemKeys.removeWhere((id, _) => !ids.contains(id));
    if (_selection != null && !ids.contains(_selection!.activity.id)) {
      _cancelToolPanelRestore();
      _invalidateToolLookups();
      _selection = null;
      _inspection = null;
    }
  }

  Set<String> _conversationIds(List<ActivityRecord> activities) =>
      activities.map((a) => a.conversation.id).toSet();

  // Polling recreates records: compare the observed fields, not object identity.
  // Any request in this conversation can carry a previously missing return.
  String _toolActivityVersion(
    List<ActivityRecord> activities,
    String conversationId,
  ) => jsonEncode(
    (activities
        .where((a) => a.conversation.id == conversationId)
        .map(
          (a) => [
            a.id,
            a.status,
            a.contentAvailable,
            a.reasonCode,
            a.occurredAt.toIso8601String(),
          ],
        )
        .toList()
      ..sort((a, b) => (a.first as String).compareTo(b.first as String))),
  );

  void _invalidateToolLookups({bool clearResults = false}) {
    _toolLookupGeneration++;
    _toolLookups.clear();
    _restoreToolPanel = false;
    if (clearResults) {
      _cancelToolPanelRestore();
      _toolResults.clear();
      _toolResultBytes = 0;
    }
  }

  void _removeToolResults(
    bool Function((String, String), _CachedToolResults) test,
  ) {
    for (final key in _toolResults.keys.toList()) {
      final result = _toolResults[key]!;
      if (test(key, result)) {
        _toolResults.remove(key);
        _toolResultBytes -= result.bytes;
      }
    }
  }

  void _scrolled() {
    if (!_scroll.hasClients) return;
    final following = _scroll.offset <= 48;
    if (_follow == following && (!following || _unread == 0)) return;
    setState(() {
      _follow = following;
      if (following && _selection == null) _unread = 0;
    });
  }

  /// Preserve an actual visible message, not an estimate of all earlier heights.
  VoidCallback _beforeLayoutChange() {
    if (!_scroll.hasClients) return () {};
    final position = _scroll.offset;
    final follow =
        _follow &&
        _reading &&
        _selection == null &&
        (ModalRoute.of(context)?.isCurrent ?? true);
    GlobalKey? anchor;
    double? anchorY;
    final viewport = _scroll.position.context.storageContext.findRenderObject();
    if (viewport is RenderBox) {
      final top = viewport.localToGlobal(Offset.zero).dy;
      for (final key in _itemKeys.values) {
        final box = key.currentContext?.findRenderObject();
        if (box is! RenderBox || !box.hasSize) continue;
        final y = box.localToGlobal(Offset.zero).dy;
        if (y + box.size.height > top && (anchorY == null || y < anchorY)) {
          anchor = key;
          anchorY = y;
        }
      }
    }
    return () => WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients) return;
      if (follow && _follow && _selection == null && _reading) {
        _scroll.jumpTo(0);
        return;
      }
      // A user scroll while an async read was pending supersedes the anchor.
      if ((_scroll.offset - position).abs() > 1) return;
      final box = anchor?.currentContext?.findRenderObject();
      if (box is RenderBox && box.hasSize && anchorY != null) {
        final delta = box.localToGlobal(Offset.zero).dy - anchorY;
        _scroll.jumpTo(
          (position - delta).clamp(0, _scroll.position.maxScrollExtent),
        );
      }
    });
  }

  Future<RuntimeUsageReport?> _loadUsage(String id, {bool refresh = false}) {
    if (refresh) {
      _usageLoads.remove(id);
      _usageVersions[id] = (_usageVersions[id] ?? 0) + 1;
    }
    final version = _usageVersions[id] ?? 0;
    final controller = widget.controller;
    return _usageLoads.putIfAbsent(id, () async {
      try {
        final report = await controller.loadExchangeUsage(id);
        if (!mounted ||
            widget.controller != controller ||
            version != (_usageVersions[id] ?? 0)) {
          return null;
        }
        if (widget.activities.any((a) => a.id == id)) {
          setState(() => _usage[id] = report);
        }
        return report;
      } on Object {
        return null; // Optional usage cannot make retained conversation unreadable.
      }
    });
  }

  String get _panelKey =>
      '${_selection?.key}:${_detailTabs[_selection?.key] ?? 0}';

  void _savePanel() {
    // Pending results can temporarily clamp the list to zero. A second refresh
    // must preserve the position from before this loading episode.
    if (_refreshingToolPanel == _panelKey) return;
    if (_panelScroll.hasClients) _panelOffsets[_panelKey] = _panelScroll.offset;
  }

  void _beginToolPanelRefresh() {
    _savePanel();
    if (_selection?.kind == _ReaderDetail.tools &&
        _detailTabs[_selection?.key] == 1) {
      _refreshingToolPanel = _panelKey;
    }
  }

  void _cancelToolPanelRestore() {
    _refreshingToolPanel = null;
    _restoreToolPanel = false;
    _panelNavigation++;
  }

  void _restorePanel({bool completesToolRefresh = false}) {
    final key = _panelKey;
    final generation = _toolLookupGeneration, navigation = _panelNavigation;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted ||
          !_panelScroll.hasClients ||
          key != _panelKey ||
          generation != _toolLookupGeneration ||
          navigation != _panelNavigation) {
        return;
      }
      _panelScroll.jumpTo(
        (_panelOffsets[key] ?? 0).clamp(
          0,
          _panelScroll.position.maxScrollExtent,
        ),
      );
      if (completesToolRefresh &&
          _refreshingToolPanel == key &&
          (_panelScroll.offset - (_panelOffsets[key] ?? 0)).abs() <= 1) {
        _refreshingToolPanel = null;
      }
    });
  }

  void _inspect(_ReaderSelection selection) {
    _savePanel();
    _cancelToolPanelRestore();
    final focus = FocusManager.instance.primaryFocus;
    if (focus != _inspectorFocus &&
        !(focus?.ancestors.contains(_inspectorFocus) ?? false)) {
      _returnFocus = focus;
    }
    setState(() {
      _invalidateToolLookups();
      _selection = selection;
      _inspectedUsage = selection.aggregateUsage
          ? _usage.values.toList(growable: false)
          : const [];
      _inspection = widget.controller.loadExchangeDetail(
        selection.activity.id,
        contentView:
            selection.kind == _ReaderDetail.context ||
                selection.kind == _ReaderDetail.tools
            ? 'full'
            : 'incremental',
      );
    });
    unawaited(_loadUsage(selection.activity.id));
    _restorePanel();
  }

  void _closeDetails() {
    _savePanel();
    _cancelToolPanelRestore();
    setState(() {
      final id = _selection?.activity.id;
      _invalidateToolLookups();
      _removeToolResults((key, _) => key.$1 == id);
      _selection = null;
      _inspection = null;
    });
    if (_returnFocus?.context != null) _returnFocus!.requestFocus();
  }

  void _latest() {
    _closeDetails();
    setState(() {
      _follow = true;
      _unread = 0;
    });
    if (_scroll.hasClients) _scroll.jumpTo(0);
  }

  @override
  void dispose() {
    _invalidateToolLookups(clearResults: true);
    _scroll.dispose();
    _panelScroll.dispose();
    _inspectorFocus.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => _ContentPageScope(
    controller: widget.controller,
    child: Builder(builder: _buildReader),
  );

  Widget _buildReader(BuildContext context) {
    if (widget.activities.isEmpty) {
      return CenteredMessage(
        icon: Icons.chat_bubble_outline,
        title: copy('conversation.empty'),
      );
    }
    final ordered = _ordered;
    final reader = LayoutBuilder(
      builder: (context, constraints) {
        final wide =
            constraints.maxWidth >=
            940 * MediaQuery.textScalerOf(context).scale(1).clamp(1, 1.5);
        final narrowDetails = !wide && _selection != null;
        final body = Column(
          children: [
            SizedBox(
              height: 34,
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 12),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        widget.title ?? copy('reader.title'),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                    ),
                    if (_usage.isNotEmpty)
                      Flexible(
                        child: _ReaderUsageBadge(
                          copy: copy,
                          reports: _usage.values.toList(),
                          onPressed: () => _inspect(
                            _ReaderSelection(
                              ordered.last,
                              _ReaderDetail.usage,
                              aggregateUsage: true,
                            ),
                          ),
                          aggregate: true,
                        ),
                      ),
                    IconButton(
                      key: const Key('reader-request-view'),
                      tooltip: copy('reader.requests'),
                      icon: const Icon(Icons.view_list_outlined, size: 16),
                      onPressed: () => setState(() {
                        _reading = false;
                        _legacyOpened = true;
                      }),
                    ),
                  ],
                ),
              ),
            ),
            const Divider(height: 1),
            Expanded(
              child: Stack(
                children: [
                  Scrollbar(
                    controller: _scroll,
                    child: ListView.builder(
                      key: const PageStorageKey('conversation-reader-scroll'),
                      controller: _scroll,
                      // Only the newest visible records are loaded. Short
                      // threads grow from the top without moving existing text.
                      reverse: true,
                      shrinkWrap: true,
                      findChildIndexCallback: (key) {
                        final index = ordered.indexWhere(
                          (a) => _itemKeys[a.id] == key,
                        );
                        return index < 0 ? null : ordered.length - 1 - index;
                      },
                      padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
                      itemCount:
                          ordered.length + (widget.canLoadEarlier ? 1 : 0),
                      itemBuilder: (context, index) {
                        if (index == ordered.length) {
                          return TextButton.icon(
                            key: const Key('reader-load-earlier'),
                            onPressed: widget.loadingEarlier
                                ? null
                                : widget.onLoadEarlier,
                            icon: const Icon(Icons.history, size: 15),
                            label: Text(copy('conversation.load_earlier')),
                          );
                        }
                        final position = ordered.length - 1 - index;
                        final activity = ordered[position];
                        return _ReadingRequest(
                          key: _itemKeys.putIfAbsent(
                            activity.id,
                            GlobalKey.new,
                          ),
                          activity: activity,
                          controller: widget.controller,
                          copy: copy,
                          number: position + 1,
                          loadUsage: () => _loadUsage(activity.id),
                          onInspect: _inspect,
                          beforeLayoutChange: _beforeLayoutChange,
                          showInput: (anchor) => _showInput(activity, anchor),
                          onInputLoaded: _inputLoaded,
                        );
                      },
                    ),
                  ),
                  if (_unread > 0 || !_follow)
                    Positioned(
                      bottom: 10,
                      right: 16,
                      child: FilledButton.tonalIcon(
                        key: const Key('reader-new-replies'),
                        onPressed: _latest,
                        icon: const Icon(Icons.arrow_downward, size: 14),
                        label: Text(
                          _unread > 0
                              ? copy.format('reader.new_replies', {
                                  'count': _unread,
                                })
                              : copy('conversation.scroll_latest'),
                        ),
                      ),
                    ),
                ],
              ),
            ),
          ],
        );
        final panel = _selection == null
            ? Padding(
                padding: const EdgeInsets.all(16),
                child: Text(
                  copy('reader.inspect_hint'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              )
            : _inspector();
        return Row(
          children: [
            Expanded(
              child: Stack(
                children: [
                  ExcludeFocus(
                    excluding: narrowDetails,
                    child: ExcludeSemantics(
                      excluding: narrowDetails,
                      child: IgnorePointer(
                        ignoring: narrowDetails,
                        child: Visibility(
                          visible: !narrowDetails,
                          maintainState: true,
                          maintainAnimation: true,
                          maintainSize: true,
                          child: body,
                        ),
                      ),
                    ),
                  ),
                  if (narrowDetails) Positioned.fill(child: panel),
                ],
              ),
            ),
            if (wide) ...[
              const VerticalDivider(width: 1),
              SizedBox(width: 360, child: panel),
            ],
          ],
        );
      },
    );
    return PageStorage(
      bucket: widget.storage ?? _pageStorage,
      child: KeyedSubtree(
        key: PageStorageKey(
          'reader:${identityHashCode(widget.controller)}:${widget.key ?? widget.activities.first.conversation.id}',
        ),
        child: IndexedStack(
          index: _reading ? 0 : 1,
          children: [
            reader,
            if (_legacyOpened)
              Column(
                children: [
                  Align(
                    alignment: Alignment.centerLeft,
                    child: TextButton.icon(
                      key: const Key('reader-return-from-requests'),
                      onPressed: () => setState(() => _reading = true),
                      icon: const Icon(Icons.arrow_back, size: 14),
                      label: Text(copy('reader.back')),
                    ),
                  ),
                  Expanded(
                    child: EvidenceConversationTimeline(
                      controller: widget.controller,
                      activities: widget.activities,
                      copy: copy,
                      canLoadEarlier: widget.canLoadEarlier,
                      loadingEarlier: widget.loadingEarlier,
                      exchangeScoped: widget.exchangeScoped,
                      showCount: widget.showCount,
                      title: copy('reader.requests'),
                      onLoadEarlier: widget.onLoadEarlier,
                    ),
                  ),
                ],
              )
            else
              const SizedBox.shrink(),
          ],
        ),
      ),
    );
  }

  Widget _inspector() {
    final selection = _selection!;
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.escape): _closeDetails,
      },
      child: Focus(
        focusNode: _inspectorFocus,
        autofocus: true,
        child: ColoredBox(
          color: context.viberColors.panel,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 10, 8, 4),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        copy('reader.detail.${selection.kind.name}'),
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                    ),
                    IconButton(
                      key: const Key('reader-close-details'),
                      tooltip: copy('reader.back'),
                      onPressed: _closeDetails,
                      icon: const Icon(Icons.close, size: 16),
                    ),
                  ],
                ),
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
                child: SelectableText(
                  selection.aggregateUsage
                      ? copy.format('reader.usage_snapshot', {
                          'count': _inspectedUsage.length,
                        })
                      : selection.kind == _ReaderDetail.raw
                      ? selection.activity.id
                      : _clockTime(selection.activity.occurredAt),
                  style: monoStyle.copyWith(
                    color: context.viberColors.textMuted,
                  ),
                ),
              ),
              const Divider(height: 1),
              Expanded(
                child: FutureBuilder<ExchangeDetail?>(
                  key: ValueKey(selection.key),
                  future: _inspection,
                  builder: (context, snapshot) {
                    if (selection.kind == _ReaderDetail.usage) {
                      return _usageInspector(selection);
                    }
                    final detail = snapshot.data;
                    if (detail == null || detail.id != selection.activity.id) {
                      if (snapshot.connectionState != ConnectionState.done) {
                        return const Center(child: CompactProgressIndicator());
                      }
                      return CenteredMessage(
                        icon: Icons.error_outline,
                        title: copy('reader.unavailable'),
                      );
                    }
                    return _ContentPageScope(
                      controller: widget.controller,
                      exchangeId: detail.id,
                      openPage: _ContentPageScope.of(context)?.openPage,
                      child: _detailBody(selection, detail),
                    );
                  },
                ),
              ),
              if (_unread > 0)
                TextButton.icon(
                  onPressed: _latest,
                  icon: const Icon(Icons.arrow_downward, size: 14),
                  label: Text(
                    copy.format('reader.new_replies', {'count': _unread}),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _detailBody(_ReaderSelection selection, ExchangeDetail detail) {
    final content = detail.content;
    final tab = _detailTabs[selection.key] ?? 0;
    if (selection.kind == _ReaderDetail.raw) {
      return _panelList([
        _EvidenceDisclosure(detail: detail, copy: copy),
        _RawEvidenceDisclosure(
          detail: detail,
          controller: widget.controller,
          copy: copy,
        ),
      ]);
    }
    if (selection.kind == _ReaderDetail.reasoning) {
      final blocks =
          content.response?.blocks
              .where((b) => b.kind == 'reasoning')
              .toList() ??
          const <ExchangeContentBlock>[];
      return _panelList([
        Text(
          copy('reader.reasoning_note'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 12),
        _ContentBlocksView(
          id: 'reader-reasoning-${detail.id}',
          blocks: blocks,
          copy: copy,
        ),
      ]);
    }
    if (selection.kind == _ReaderDetail.other) {
      return _panelList([
        _ContentBlocksView(
          id: 'reader-other-${detail.id}',
          blocks:
              content.response?.blocks
                  .where((b) => b.kind == 'provider_extension')
                  .toList() ??
              const [],
          copy: copy,
        ),
      ]);
    }
    if (selection.kind == _ReaderDetail.tools) {
      return _toolInspector(selection, detail);
    }
    final messages =
        content.request?.messages ?? const <ExchangeContentMessage>[];
    final instructions = messages
        .where((m) => m.role == 'system' || m.role == 'developer')
        .toList();
    final history = messages
        .where((m) => m.role != 'system' && m.role != 'developer')
        .toList();
    final system = content.request?.system ?? const <ExchangeContentBlock>[];
    return Column(
      children: [
        _tabs(selection, [
          'reader.instructions',
          'reader.history',
          'reader.client',
        ], tab),
        Expanded(
          child: tab == 2
              ? _panelList([
                  if (detail.clientIdentity case final identity?)
                    _ClientIdentityDisclosure(
                      exchangeId: detail.id,
                      identity: identity,
                      copy: copy,
                    ),
                  _EvidenceDisclosure(detail: detail, copy: copy),
                ])
              : ListView.builder(
                  key: PageStorageKey('reader-panel-$_panelKey'),
                  controller: _panelScroll,
                  padding: const EdgeInsets.all(12),
                  itemCount:
                      (tab == 0
                          ? instructions.length + (system.isEmpty ? 0 : 1)
                          : history.length + 1) +
                      (content.page?.requestNextCursor == null ? 0 : 1),
                  itemBuilder: (context, index) {
                    final count = tab == 0
                        ? instructions.length + (system.isEmpty ? 0 : 1)
                        : history.length + 1;
                    if (index == count &&
                        content.page?.requestNextCursor != null) {
                      return _ContentPageButton(
                        exchangeId: detail.id,
                        cursor: content.page!.requestNextCursor!,
                        copy: copy,
                        label: copy('content_page.earlier'),
                      );
                    }
                    if (tab == 1 && index == 0) {
                      return Padding(
                        padding: const EdgeInsets.only(bottom: 12),
                        child: Text(
                          copy.format('reader.history_note', {
                            'count':
                                content.requestProjection?.totalMessageCount ??
                                messages.length,
                          }),
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      );
                    }
                    final topLevel =
                        tab == 0 && system.isNotEmpty && index == 0;
                    final message = topLevel
                        ? ExchangeContentMessage(
                            role: 'system',
                            blocks: system,
                            agent: null,
                          )
                        : tab == 0
                        ? instructions[index - (system.isEmpty ? 0 : 1)]
                        : history[index - 1];
                    return _MessageCard(
                      key: PageStorageKey(
                        'reader-context-${detail.id}-$tab-$index',
                      ),
                      id: 'reader-context-${detail.id}-$tab-$index',
                      message: message,
                      copy: copy,
                      label: topLevel
                          ? copy('exchange.system_parameter')
                          : null,
                    );
                  },
                ),
        ),
      ],
    );
  }

  Widget _tabs(_ReaderSelection selection, List<String> labels, int selected) =>
      Padding(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        child: Wrap(
          spacing: 4,
          runSpacing: 4,
          children: [
            for (var i = 0; i < labels.length; i++)
              TextButton(
                key: Key('reader-detail-tab-$i'),
                style: TextButton.styleFrom(
                  foregroundColor: i == selected
                      ? context.viberColors.route
                      : context.viberColors.textMuted,
                  backgroundColor: i == selected
                      ? context.viberColors.route.withValues(alpha: .1)
                      : null,
                ),
                onPressed: () {
                  _savePanel();
                  _cancelToolPanelRestore();
                  setState(() => _detailTabs[selection.key] = i);
                  _restorePanel();
                },
                child: Text(copy(labels[i])),
              ),
          ],
        ),
      );

  Widget _panelList(List<Widget> children) =>
      NotificationListener<UserScrollNotification>(
        onNotification: (notification) {
          if (notification.depth == 0 &&
              notification.direction != ScrollDirection.idle) {
            // A drag/wheel scroll supersedes an automatic position restoration.
            _cancelToolPanelRestore();
          }
          return false;
        },
        child: ListView(
          key: PageStorageKey('reader-panel-$_panelKey'),
          controller: _panelScroll,
          padding: const EdgeInsets.all(12),
          children: children,
        ),
      );

  Widget _toolInspector(_ReaderSelection selection, ExchangeDetail detail) {
    final ids = selection.toolIds;
    final wanted =
        <({ExchangeContentBlock block, String requestId, bool input})>[
          for (final message
              in detail.content.request?.messages ??
                  const <ExchangeContentMessage>[])
            for (final block in message.blocks)
              if (ids.contains(block.callId))
                (block: block, requestId: detail.id, input: true),
          for (final block
              in detail.content.response?.blocks ??
                  const <ExchangeContentBlock>[])
            if (ids.contains(block.callId))
              (block: block, requestId: detail.id, input: false),
        ];
    // Later requests can carry this call's return. Never match by
    // neighbouring text, tool name, or a different conversation/actor.
    for (final activity in widget.activities) {
      if (activity.id == detail.id ||
          activity.conversation.id != selection.activity.conversation.id ||
          !activity.occurredAt.isAfter(selection.activity.occurredAt)) {
        continue;
      }
      final next =
          (widget.controller.exchangeDetail(
                    activity.id,
                    contentView: 'reading',
                  ) ??
                  widget.controller.exchangeDetail(activity.id))
              ?.content;
      if (next?.request == null) continue;
      for (final block in next!.request!.messages.expand((m) => m.blocks)) {
        if (block.kind == 'tool_result' &&
            block.callId != null &&
            ids.contains(block.callId)) {
          wanted.add((block: block, requestId: activity.id, input: true));
        }
      }
    }
    final tab =
        _detailTabs[selection.key] ??
        (wanted.any((entry) => entry.block.kind == 'tool_result') ||
                selection.unkeyedTools.any(
                  (block) => block.kind == 'tool_result',
                )
            ? 1
            : 0);
    _detailTabs.putIfAbsent(selection.key, () => tab);
    return Column(
      children: [
        _tabs(selection, ['reader.arguments', 'reader.results'], tab),
        Expanded(
          child: _panelList([
            if (selection.unkeyedTools.isNotEmpty) ...[
              Text(
                copy('reader.unmatched_tool'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              _ContentBlocksView(
                id: 'reader-unkeyed-${detail.id}-$tab',
                blocks: selection.unkeyedTools
                    .where(
                      (b) => b.kind == (tab == 0 ? 'tool_call' : 'tool_result'),
                    )
                    .toList(),
                copy: copy,
              ),
              const SizedBox(height: 12),
            ],
            for (final id in ids) ...[
              if (tab == 1)
                Text(
                  wanted
                          .where(
                            (entry) =>
                                entry.block.callId == id &&
                                entry.block.toolName != null,
                          )
                          .firstOrNull
                          ?.block
                          .toolName ??
                      copy('exchange.tool.unknown'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              KeyedSubtree(
                // ExpansionTile's bool state must not share the panel's
                // PageStorage path, which stores a double scroll offset.
                key: PageStorageKey('reader-tool-expansion-${detail.id}-$id'),
                child: ExpansionTile(
                  key: Key('reader-tool-record-${detail.id}-$id'),
                  title: Text(
                    copy('reader.technical_details'),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  children: [
                    Text(
                      copy('reader.tool_pairing'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    SelectableText(
                      id,
                      key: PageStorageKey('tool-record-call-${detail.id}-$id'),
                      style: monoStyle,
                    ),
                    for (final entry in wanted.where(
                      (e) => e.block.callId == id,
                    ))
                      SelectableText(
                        key: PageStorageKey(
                          'tool-record-source-${entry.requestId}-$id-${entry.input}',
                        ),
                        copy.format(
                          entry.input
                              ? 'reader.tool_source_input'
                              : 'reader.tool_source_output',
                          {'request': entry.requestId},
                        ),
                        style: monoStyle,
                      ),
                  ],
                ),
              ),
              const SizedBox(height: 6),
              Builder(
                builder: (context) {
                  final matches = wanted
                      .where(
                        (entry) =>
                            entry.block.callId == id &&
                            entry.block.kind ==
                                (tab == 0 ? 'tool_call' : 'tool_result'),
                      )
                      .toList();
                  // Multiple distinct candidates are evidence, not permission to pick
                  // whichever result happens to appear last.
                  if (matches.isEmpty) {
                    if (tab == 0) return Text(copy('reader.tool_not_observed'));
                    return FutureBuilder<_ToolLookupOutcome>(
                      future: _toolLookup(detail.id, id),
                      builder: (context, snapshot) {
                        if (snapshot.connectionState != ConnectionState.done) {
                          return const CompactProgressIndicator();
                        }
                        if (_restoreToolPanel) {
                          _restoreToolPanel = false;
                          // Restore after the result has actually been laid out,
                          // not while the pending indicator still bounds the list.
                          _restorePanel(
                            completesToolRefresh:
                                snapshot.data?.complete == true &&
                                snapshot.data!.items.isNotEmpty,
                          );
                        }
                        if (snapshot.hasError) {
                          return Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(copy('reader.tool_lookup_failed')),
                              TextButton(
                                key: Key(
                                  'reader-tool-refresh-${detail.id}-$id',
                                ),
                                onPressed: _refreshToolResults,
                                child: Text(copy('common.retry')),
                              ),
                            ],
                          );
                        }
                        final found =
                            snapshot.data?.items ?? const <ToolResultMatch>[];
                        if (found.isEmpty) {
                          return Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(copy('reader.tool_not_observed')),
                              TextButton.icon(
                                key: Key(
                                  'reader-tool-refresh-${detail.id}-$id',
                                ),
                                onPressed: _refreshToolResults,
                                icon: const Icon(Icons.refresh, size: 14),
                                label: Text(copy('reader.tool_refresh')),
                              ),
                            ],
                          );
                        }
                        return Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            for (final entry in found)
                              _ContentBlocksView(
                                id: 'looked-up-${entry.exchangeId}-$id',
                                blocks: [entry.block],
                                copy: copy,
                              ),
                          ],
                        );
                      },
                    );
                  }
                  return Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      if (matches.length > 1)
                        Text(
                          copy('reader.tool_ambiguous'),
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      for (final (index, entry) in matches.indexed) ...[
                        _ContentBlocksView(
                          id: 'reader-tool-${detail.id}-$id-$tab-$index',
                          blocks: [entry.block],
                          copy: copy,
                        ),
                        const SizedBox(height: 10),
                      ],
                    ],
                  );
                },
              ),
              const SizedBox(height: 16),
            ],
            if (ids.isEmpty && selection.unkeyedTools.isEmpty)
              Text(copy('reader.unmatched_tool')),
          ]),
        ),
      ],
    );
  }

  void _refreshToolResults() {
    _beginToolPanelRefresh();
    setState(() {
      _invalidateToolLookups();
      _removeToolResults((key, _) => key.$1 == _selection?.activity.id);
    });
  }

  Future<_ToolLookupOutcome> _toolLookup(String exchangeId, String callId) {
    final key = (exchangeId, callId);
    return _toolLookups.putIfAbsent(key, () {
      final cached = _toolResults.remove(key);
      if (cached != null) {
        _toolResults[key] = cached; // Access refreshes the LRU order.
        return Future.value(_ToolLookupOutcome(cached.items, complete: true));
      }
      final generation = _toolLookupGeneration;
      final navigation = _panelNavigation;
      return _findToolResults(exchangeId, callId, generation).then((outcome) {
        if (mounted && generation == _toolLookupGeneration) {
          if (outcome.complete && outcome.items.isNotEmpty) {
            _rememberToolResult(key, outcome.items);
          }
          if (navigation == _panelNavigation) _restoreToolPanel = true;
        }
        return outcome;
      });
    });
  }

  void _rememberToolResult((String, String) key, List<ToolResultMatch> items) {
    int bytes(String? text) => text == null ? 0 : utf8.encode(text).length;
    var size = bytes(key.$1) + bytes(key.$2);
    for (final item in items) {
      final block = item.block;
      size +=
          bytes(item.exchangeId) +
          bytes(block.text) +
          bytes(block.arguments == null ? null : jsonEncode(block.arguments)) +
          bytes(block.callId) +
          bytes(block.toolName) +
          bytes(block.toolNamespace) +
          bytes(block.providerSource) +
          bytes(block.providerKind) +
          bytes(block.fingerprint) +
          bytes(block.agent?.agentName) +
          bytes(block.agent?.author) +
          bytes(block.agent?.recipient) +
          bytes(block.deferred?.exchangeId) +
          bytes(block.deferred?.cursor);
    }
    _removeToolResults((candidate, _) => candidate == key);
    if (size > 4 * 1024 * 1024) return;
    _toolResults[key] = _CachedToolResults(List.unmodifiable(items), size);
    _toolResultBytes += size;
    while (_toolResults.length > 32 || _toolResultBytes > 4 * 1024 * 1024) {
      final oldest = _toolResults.keys.first;
      _toolResultBytes -= _toolResults.remove(oldest)!.bytes;
    }
  }

  Future<_ToolLookupOutcome> _findToolResults(
    String exchangeId,
    String callId,
    int generation,
  ) async {
    final controller = widget.controller;
    final found = <ToolResultMatch>[];
    final seen = <String>{};
    String? cursor;
    do {
      final page = await controller.loadToolResults(
        exchangeId,
        callId,
        cursor: cursor,
      );
      if (!mounted ||
          generation != _toolLookupGeneration ||
          widget.controller != controller ||
          _selection?.activity.id != exchangeId) {
        return _ToolLookupOutcome(found, complete: false);
      }
      found.addAll(page.items);
      cursor = page.nextCursor;
      if (found.isNotEmpty || cursor == null) {
        return _ToolLookupOutcome(found, complete: true);
      }
      if (!seen.add(cursor)) {
        throw const ControlContractException('Tool lookup cursor repeated');
      }
    } while (true);
  }

  Widget _usageInspector(_ReaderSelection selection) => selection.aggregateUsage
      ? _panelList([
          Text(
            copy.format('reader.usage_snapshot', {
              'count': _inspectedUsage.length,
            }),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 8),
          _ReaderUsageDetails(reports: _inspectedUsage, copy: copy),
        ])
      : FutureBuilder<RuntimeUsageReport?>(
          future: _loadUsage(selection.activity.id),
          builder: (context, snapshot) => _panelList([
            Text(
              copy('usage.cost.basis'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 8),
            if (snapshot.connectionState != ConnectionState.done)
              const CompactProgressIndicator()
            else if (snapshot.data == null)
              InlineNotice(
                message: copy('reader.usage_unavailable'),
                error: true,
              )
            else
              _ReaderUsageDetails(reports: [snapshot.data!], copy: copy),
            TextButton.icon(
              onPressed: () => setState(() {
                _loadUsage(selection.activity.id, refresh: true);
              }),
              icon: const Icon(Icons.refresh, size: 14),
              label: Text(copy('common.retry')),
            ),
          ]),
        );
}

List<ExchangeContentBlock> _readerText(Iterable<ExchangeContentBlock> blocks) =>
    blocks
        .where(
          (b) =>
              b.kind == 'text' ||
              b.kind == 'refusal' ||
              b.deferred != null ||
              (b.kind == 'provider_extension' &&
                  b.providerKind == 'input_image'),
        )
        .toList(growable: false);

List<ExchangeContentBlock> _readerInput(List<ExchangeContentMessage> messages) {
  final blocks = _readerText(messages.expand((m) => m.blocks));
  final images = blocks.where((b) => b.providerKind == 'input_image').toList();
  if (images.isEmpty) return blocks;
  final attachmentLabel = RegExp(r'^\s*(?:\[Image #\d+\]\s*)+$');
  final fileLabel = RegExp(r'^\s*<image name=\[Image #\d+\] path="[^"]+">\s*$');
  return [
    ...blocks.where(
      (b) =>
          b.providerKind != 'input_image' &&
          !(b.kind == 'text' &&
              (attachmentLabel.hasMatch(b.text ?? '') ||
                  fileLabel.hasMatch(b.text ?? ''))),
    ),
    ...images,
  ];
}

final class _ReadingRequest extends StatefulWidget {
  const _ReadingRequest({
    required this.activity,
    required this.controller,
    required this.copy,
    required this.number,
    required this.loadUsage,
    required this.onInspect,
    required this.beforeLayoutChange,
    required this.showInput,
    required this.onInputLoaded,
    super.key,
  });
  final ActivityRecord activity;
  final WorkbenchController controller;
  final AppCopy copy;
  final int number;
  final Future<RuntimeUsageReport?> Function() loadUsage;
  final ValueChanged<_ReaderSelection> onInspect;
  final VoidCallback Function() beforeLayoutChange;
  final bool Function(String) showInput;
  final VoidCallback onInputLoaded;
  @override
  State<_ReadingRequest> createState() => _ReadingRequestState();
}

final class _ReadingRequestState extends State<_ReadingRequest> {
  ExchangeDetail? _detail;
  RuntimeUsageReport? _usage;
  bool _loading = true;
  int _generation = 0;
  @override
  void initState() {
    super.initState();
    _detail = widget.controller.exchangeDetail(
      widget.activity.id,
      contentView: 'reading',
    );
    _loading = _detail == null;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(_load());
    });
  }

  @override
  void didUpdateWidget(covariant _ReadingRequest oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.activity.status != widget.activity.status) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) unawaited(_load(refresh: true));
      });
    }
  }

  Future<void> _load({bool refresh = false}) async {
    final generation = ++_generation;
    final usage = widget.loadUsage();
    final detail = await widget.controller.loadExchangeDetail(
      widget.activity.id,
      contentView: 'reading',
      refresh: refresh,
    );
    if (!mounted || generation != _generation) return;
    final restore = widget.beforeLayoutChange();
    setState(() {
      _detail = detail;
      _loading = false;
    });
    restore();
    widget.onInputLoaded();
    final report = await usage;
    if (mounted && generation == _generation) setState(() => _usage = report);
  }

  void _inspect(
    _ReaderDetail kind, {
    Set<String> tools = const {},
    List<ExchangeContentBlock> unkeyed = const [],
  }) => widget.onInspect(
    _ReaderSelection(
      widget.activity,
      kind,
      toolIds: tools,
      unkeyedTools: unkeyed,
    ),
  );

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final detail = _detail;
    if (_loading) {
      return Padding(
        padding: const EdgeInsets.all(16),
        child: Row(
          children: [
            const CompactProgressIndicator(),
            const SizedBox(width: 8),
            Text(copy('common.loading')),
          ],
        ),
      );
    }
    if (detail == null) {
      return Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          children: [
            InlineNotice(message: copy('reader.unavailable'), error: true),
            TextButton(
              onPressed: () => _load(refresh: true),
              child: Text(copy('common.retry')),
            ),
          ],
        ),
      );
    }
    final content = detail.content;
    final incremental =
        content.requestProjection?.relationship == 'incremental';
    final inputAnchor = content.page?.inputAnchor;
    final readingInput = inputAnchor != null;
    final inputs = (readingInput ? widget.showInput(inputAnchor) : incremental)
        ? content.request!.messages
        : const <ExchangeContentMessage>[];
    final toolBlocks = [
      for (final m in inputs)
        ...m.blocks.where(
          (b) => b.kind == 'tool_call' || b.kind == 'tool_result',
        ),
      ...?content.response?.blocks.where(
        (b) => b.kind == 'tool_call' || b.kind == 'tool_result',
      ),
    ];
    final toolIds = toolBlocks.map((b) => b.callId).whereType<String>().toSet();
    final unkeyed = toolBlocks.where((b) => b.callId == null).toList();
    final reasoning =
        content.response?.blocks.where((b) => b.kind == 'reasoning').length ??
        0;
    final response = content.response;
    final text = _readerText(response?.blocks ?? const []);
    final other =
        response?.blocks.where((b) => b.kind == 'provider_extension').length ??
        0;
    final actorLabel =
        widget.activity.conversation.clientIdentity?.actorLabel ??
        (const {
              'agent',
              'isolated_subagent',
            }.contains(widget.activity.conversation.kind)
            ? copy('reader.subagent')
            : null);
    return Padding(
      key: Key('reader-request-${detail.id}'),
      padding: const EdgeInsets.only(bottom: 22),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(child: Divider(color: context.viberColors.dividerSoft)),
              const SizedBox(width: 8),
              Flexible(
                flex: 8,
                child: Text(
                  _clockTime(widget.activity.occurredAt),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ),
              const SizedBox(width: 8),
              Expanded(child: Divider(color: context.viberColors.dividerSoft)),
              if (text.isEmpty && detail.status != 'pending')
                Flexible(
                  flex: 6,
                  child: _ReaderUsageBadge(
                    copy: copy,
                    reports: _usage == null ? const [] : [_usage!],
                    onPressed: () => _inspect(_ReaderDetail.usage),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 12),
          if (detail.status == 'failed')
            _FailureNotice(
              key: PageStorageKey('reader-failure:${detail.id}'),
              diagnosis: detail.diagnosis,
              result: detail.processingTrace.result,
              providerErrorClass: null,
              copy: copy,
            ),
          if (content.state == 'not_recorded')
            InlineNotice(message: copy('exchange.content.not_recorded')),
          if (content.requestProjection?.relationship == 'checkpoint' &&
              !readingInput)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: Key('reader-checkpoint-${detail.id}'),
                onPressed: () => _inspect(_ReaderDetail.context),
                icon: const Icon(Icons.history, size: 14),
                label: Text(
                  copy.format('reader.checkpoint', {
                    'count': content.requestProjection!.totalMessageCount,
                  }),
                ),
              ),
            ),
          if (incremental && content.page?.requestNextCursor != null)
            _ContentPageButton(
              exchangeId: detail.id,
              cursor: content.page!.requestNextCursor!,
              copy: copy,
              label: copy('content_page.earlier'),
            ),
          if (readingInput && inputs.isNotEmpty)
            _ReadingMessage(
              id: '${detail.id}-input',
              exchangeId: detail.id,
              role: 'user',
              roleLabel: copy('exchange.role.user'),
              blocks: _readerInput(inputs),
              copy: copy,
              usage: null,
              onUsage: null,
            ),
          for (final (index, message)
              in (readingInput ? const <ExchangeContentMessage>[] : inputs)
                  .indexed)
            if (message.role != 'system' &&
                message.role != 'developer' &&
                message.role != 'tool' &&
                _readerText(message.blocks).isNotEmpty)
              _ReadingMessage(
                id: '${detail.id}-input-$index',
                exchangeId: detail.id,
                role: message.role,
                blocks: _readerText(message.blocks),
                copy: copy,
                agent: message.agent,
                roleLabel: copy('exchange.role.${message.role}'),
                usage: null,
                onUsage: null,
              ),
          if (text.isNotEmpty)
            _ReadingMessage(
              id: '${detail.id}-response',
              exchangeId: detail.id,
              role: 'assistant',
              roleLabel: actorLabel ?? copy('reader.agent_reply'),
              blocks: text,
              copy: copy,
              usage: _usage,
              onUsage: () => _inspect(_ReaderDetail.usage),
            ),
          if (detail.status == 'pending')
            _PendingResponse(copy: copy)
          else if (response != null && text.isEmpty)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 8),
              child: Text(
                copy('reader.no_text'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          Wrap(
            spacing: 10,
            runSpacing: 4,
            children: [
              if (toolBlocks.isNotEmpty)
                TextButton.icon(
                  key: Key('reader-tools-${detail.id}'),
                  onPressed: () => _inspect(
                    _ReaderDetail.tools,
                    tools: toolIds,
                    unkeyed: unkeyed,
                  ),
                  icon: const Icon(Icons.build_outlined, size: 14),
                  label: Text(
                    copy.format('reader.tools', {
                      'count': toolIds.length + unkeyed.length,
                    }),
                  ),
                ),
              if (reasoning > 0)
                TextButton.icon(
                  onPressed: () => _inspect(_ReaderDetail.reasoning),
                  key: Key('reader-reasoning-${detail.id}'),
                  icon: const Icon(Icons.psychology_outlined, size: 14),
                  label: Text(
                    copy.format('reader.reasoning', {'count': reasoning}),
                  ),
                ),
              if (other > 0)
                TextButton.icon(
                  key: Key('reader-other-${detail.id}'),
                  onPressed: () => _inspect(_ReaderDetail.other),
                  icon: const Icon(Icons.layers_outlined, size: 14),
                  label: Text(copy.format('reader.other', {'count': other})),
                ),
              TextButton.icon(
                key: Key('reader-context-${detail.id}'),
                onPressed: () => _inspect(_ReaderDetail.context),
                icon: const Icon(Icons.subject, size: 14),
                label: Text(copy('reader.context')),
              ),
              TextButton.icon(
                key: Key('reader-raw-${detail.id}'),
                onPressed: () => _inspect(_ReaderDetail.raw),
                icon: const Icon(Icons.data_object, size: 14),
                label: Text(copy('reader.raw')),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

final class _ReadingMessage extends StatelessWidget {
  const _ReadingMessage({
    required this.id,
    required this.role,
    required this.blocks,
    required this.copy,
    required this.usage,
    required this.onUsage,
    this.agent,
    this.roleLabel,
    this.exchangeId,
  });
  final String id, role;
  final List<ExchangeContentBlock> blocks;
  final AppCopy copy;
  final ExchangeAgentContext? agent;
  final String? roleLabel;
  final String? exchangeId;
  final RuntimeUsageReport? usage;
  final VoidCallback? onUsage;
  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 14),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Icon(
              agent != null
                  ? Icons.account_tree_outlined
                  : role == 'user'
                  ? Icons.person_outline
                  : Icons.auto_awesome_outlined,
              size: 15,
              color: role == 'user'
                  ? context.viberColors.route
                  : context.viberColors.verified,
            ),
            const SizedBox(width: 7),
            Expanded(
              child: Text(
                agent == null
                    ? roleLabel ?? copy('exchange.role.$role')
                    : _agentDirection(agent!),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.labelMedium,
              ),
            ),
            if (onUsage != null)
              Flexible(
                child: _ReaderUsageBadge(
                  copy: copy,
                  reports: usage == null ? const [] : [usage!],
                  onPressed: onUsage!,
                ),
              ),
            if (_hasCopyableContent(blocks))
              _CopyValueButton(
                key: Key('reader-copy-$id'),
                tooltip: blocks.any((b) => b.deferred != null)
                    ? copy('content_page.copy')
                    : copy.format('common.copy', {
                        'field': copy('exchange.content.value'),
                      }),
                value: () => _contentBlocksClipboardText(blocks),
              ),
          ],
        ),
        Container(
          padding: role == 'user'
              ? const EdgeInsets.all(12)
              : const EdgeInsets.fromLTRB(22, 4, 0, 0),
          decoration: role == 'user'
              ? BoxDecoration(
                  color: context.viberColors.route.withValues(alpha: .07),
                  borderRadius: ViberMetrics.surfaceRadius,
                )
              : null,
          child: _ContentPageScope(
            controller: _ContentPageScope.of(context)!.controller,
            exchangeId: exchangeId,
            openPage: _ContentPageScope.of(context)?.openPage,
            child: _ContentBlocksView(
              id: 'reader-message-$id',
              blocks: blocks,
              copy: copy,
            ),
          ),
        ),
      ],
    ),
  );
}

final class _ReaderUsageBadge extends StatelessWidget {
  const _ReaderUsageBadge({
    required this.copy,
    required this.reports,
    required this.onPressed,
    this.aggregate = false,
  });
  final AppCopy copy;
  final List<RuntimeUsageReport> reports;
  final VoidCallback onPressed;
  final bool aggregate;
  @override
  Widget build(BuildContext context) {
    final total = _ReaderUsageTotals(reports);
    final tokens = total.count;
    final known = total.known;
    final partial = total.partial;
    final cost = total.cost;
    final count = known == 0
        ? '—'
        : '${partial ? '≥' : ''}${tokens >= 1000 ? '${(partial ? (tokens ~/ 100) / 10 : tokens / 1000).toStringAsFixed(1)}K' : tokens}';
    return TextButton(
      key: aggregate ? const Key('reader-usage-summary') : null,
      style: TextButton.styleFrom(
        foregroundColor: context.viberColors.textMuted,
        padding: const EdgeInsets.symmetric(horizontal: 4),
        textStyle: Theme.of(context).textTheme.bodySmall,
      ),
      onPressed: onPressed,
      child: Tooltip(
        message: copy(
          aggregate ? 'reader.loaded_usage' : 'reader.request_usage',
        ),
        child: Text(
          '${aggregate ? '${reports.length} · ' : ''}$count · ${cost.pricedCalls > 0 && !cost.partial ? '≈' : ''}${usageCostLabel(cost)}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
        ),
      ),
    );
  }
}

final class _ReaderUsageDetails extends StatelessWidget {
  const _ReaderUsageDetails({required this.reports, required this.copy});
  final List<RuntimeUsageReport> reports;
  final AppCopy copy;
  @override
  Widget build(BuildContext context) {
    final total = _ReaderUsageTotals(reports);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (total.collected == 0)
          InlineNotice(message: copy('reader.usage_not_recorded')),
        Text(
          '${copy('usage.cost.basis')} · ${usageCostLabel(total.cost)}',
          style: Theme.of(context).textTheme.titleSmall,
        ),
        const SizedBox(height: 8),
        for (final entry in [
          (copy('exchange.usage.input'), total.fields[0]),
          (copy('exchange.usage.cache_read'), total.fields[1]),
          (copy('reader.cache_write'), total.fields[2]),
          (copy('exchange.usage.output'), total.fields[3]),
          (copy('exchange.usage.reasoning'), total.fields[4]),
        ])
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 7),
            child: Row(
              children: [
                Expanded(child: Text(entry.$1)),
                Text(
                  entry.$2.knownCalls == 0
                      ? '—'
                      : '${entry.$2.unknownCalls > 0 ? '≥' : ''}${entry.$2.tokens}',
                  style: monoStyle,
                ),
              ],
            ),
          ),
        const SizedBox(height: 8),
        Text(
          copy('reader.usage_basis'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 8),
        Text(
          copy.format('reader.usage_coverage', {
            'known': total.collected,
            'count': reports.length,
          }),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        if (total.cost.partial)
          Text(
            copy('reader.partial_cost'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
      ],
    );
  }
}

/// Summarize request-scoped numeric evidence; never sum response text lengths
/// or the same response's display blocks. Reasoning is an output subset.
final class _ReaderUsageTotals {
  _ReaderUsageTotals(List<RuntimeUsageReport> reports) {
    for (final report in reports) {
      final group = report.total;
      if (group == null || group.agentApiCalls != 1) {
        cost = cost.add(const RuntimeCostEstimate(unpricedCalls: 1));
        for (var i = 0; i < fields.length; i++) {
          final current = fields[i];
          fields[i] = RuntimeTokenAggregate(
            tokens: current.tokens,
            knownCalls: current.knownCalls,
            unknownCalls: current.unknownCalls + 1,
          );
        }
        continue;
      }
      collected++;
      cost = cost.add(group.cost);
      final values = [
        group.tokens.inputUncached,
        group.tokens.cacheRead,
        group.tokens.cacheWrite,
        group.tokens.output,
        group.tokens.reasoning,
      ];
      for (var i = 0; i < values.length; i++) {
        final a = fields[i], b = values[i];
        final fits = b.tokens <= _maxExactInteger - a.tokens;
        fields[i] = RuntimeTokenAggregate(
          tokens: a.tokens + (fits ? b.tokens : 0),
          knownCalls: a.knownCalls + (fits ? b.knownCalls : 0),
          unknownCalls:
              a.unknownCalls + b.unknownCalls + (fits ? 0 : b.knownCalls),
        );
      }
    }
    for (final field in fields.take(4)) {
      known += field.knownCalls;
      if (field.tokens <= _maxExactInteger - count) {
        count += field.tokens;
      } else {
        partial = true;
      }
      partial = partial || field.unknownCalls > 0;
    }
  }
  static const _maxExactInteger = 9007199254740991;
  final fields = List.generate(
    5,
    (_) =>
        const RuntimeTokenAggregate(tokens: 0, knownCalls: 0, unknownCalls: 0),
  );
  RuntimeCostEstimate cost = const RuntimeCostEstimate();
  int collected = 0, count = 0, known = 0;
  bool partial = false;
}

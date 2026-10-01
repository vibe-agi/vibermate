part of 'conversation_timeline.dart';

// All retained-content renderers use the same opt-in loader. A nested block
// navigates inside the existing reader instead of opening a stack of dialogs.
final class _ContentPageScope extends InheritedWidget {
  const _ContentPageScope({
    required this.controller,
    required super.child,
    this.openPage,
  });
  final WorkbenchController controller;
  final void Function(String exchangeId, String cursor)? openPage;
  static _ContentPageScope? of(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<_ContentPageScope>();
  @override
  bool updateShouldNotify(_ContentPageScope oldWidget) =>
      controller != oldWidget.controller || openPage != oldWidget.openPage;
}

final class _ContentPageButton extends StatelessWidget {
  const _ContentPageButton({
    required this.exchangeId,
    required this.cursor,
    required this.copy,
    this.label,
    this.estimatedBytes,
  });
  final String exchangeId, cursor;
  final AppCopy copy;
  final String? label;
  final int? estimatedBytes;
  @override
  Widget build(BuildContext context) {
    final scope = _ContentPageScope.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (estimatedBytes case final size? when size > 0)
          Text(
            copy.format('content_page.unloaded', {
              'size': (size / 1024).ceil(),
            }),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        TextButton.icon(
          key: ValueKey('content-page-open-$cursor'),
          icon: const Icon(Icons.read_more, size: 16),
          label: Text(label ?? copy('content_page.load')),
          onPressed: scope == null
              ? null
              : () {
                  if (scope.openPage != null) {
                    scope.openPage!(exchangeId, cursor);
                  } else {
                    showDialog<void>(
                      context: context,
                      builder: (_) => _ContentPageDialog(
                        controller: scope.controller,
                        exchangeId: exchangeId,
                        cursor: cursor,
                        copy: copy,
                      ),
                    );
                  }
                },
        ),
      ],
    );
  }
}

final class _ContentPageDialog extends StatefulWidget {
  const _ContentPageDialog({
    required this.controller,
    required this.exchangeId,
    required this.cursor,
    required this.copy,
  });
  final WorkbenchController controller;
  final String exchangeId, cursor;
  final AppCopy copy;
  @override
  State<_ContentPageDialog> createState() => _ContentPageDialogState();
}

final class _ContentPageDialogState extends State<_ContentPageDialog> {
  late String _cursor = widget.cursor;
  late Future<ExchangeContentPage> _page = widget.controller
      .loadExchangeContentPage(widget.exchangeId, _cursor);
  final _back = <String>[];
  final _positions = <String, double>{};
  late final _scroll = ScrollController(
    onAttach: (position) {
      final cursor = _cursor;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && cursor == _cursor && position.hasContentDimensions) {
          position.jumpTo(
            (_positions[cursor] ?? 0).clamp(0, position.maxScrollExtent),
          );
        }
      });
    },
  );

  void _load(String cursor, {bool remember = true}) {
    if (_scroll.hasClients) _positions[_cursor] = _scroll.offset;
    setState(() {
      if (remember) _back.add(_cursor);
      _cursor = cursor;
      _page = widget.controller.loadExchangeContentPage(
        widget.exchangeId,
        cursor,
      );
    });
  }

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  String _copyText(ExchangeContentPage page) {
    if (page.kind == 'text' || page.kind == 'arguments') return page.text;
    if (page.kind == 'protocol') {
      return page.protocolEvidence
          .map((e) => '${e.name}: ${e.value}')
          .join('\n');
    }
    return _contentBlocksClipboardText([
      ...page.blocks,
      for (final m in page.messages) ...m.blocks,
    ]);
  }

  Widget _navigation(ExchangeContentPage page) => LayoutBuilder(
    builder: (context, constraints) {
      final copy = widget.copy;
      final compact = constraints.maxWidth < 480;
      final back = _back.isEmpty
          ? null
          : () => _load(_back.removeLast(), remember: false);
      final next = page.nextCursor == null
          ? null
          : () => _load(page.nextCursor!);
      final nextLabel = copy(
        page.kind == 'request' ? 'content_page.earlier' : 'content_page.next',
      );
      final copyable =
          page.text.isNotEmpty ||
          page.protocolEvidence.isNotEmpty ||
          _hasCopyableContent([
            ...page.blocks,
            for (final m in page.messages) ...m.blocks,
          ]);
      return Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          if (compact)
            IconButton(
              key: const Key('content-page-back'),
              tooltip: copy('common.back'),
              onPressed: back,
              icon: const Icon(Icons.arrow_back),
            )
          else
            TextButton.icon(
              key: const Key('content-page-back'),
              onPressed: back,
              icon: const Icon(Icons.arrow_back, size: 16),
              label: Text(copy('common.back')),
            ),
          if (copyable)
            _CopyValueButton(
              tooltip: copy('content_page.copy'),
              label: compact ? null : copy('content_page.copy'),
              value: () => _copyText(page),
            ),
          if (compact)
            IconButton(
              key: const Key('content-page-next'),
              tooltip: nextLabel,
              onPressed: next,
              icon: const Icon(Icons.arrow_forward),
            )
          else
            TextButton.icon(
              key: const Key('content-page-next'),
              onPressed: next,
              icon: const Icon(Icons.arrow_forward, size: 16),
              label: Text(nextLabel),
            ),
        ],
      );
    },
  );

  Widget _body(ExchangeContentPage page) {
    final copy = widget.copy;
    final body = page.kind == 'text' || page.kind == 'arguments';
    final count = switch (page.kind) {
      'request' => page.messages.length,
      'protocol' => page.protocolEvidence.length,
      'message' => page.blocks.length,
      _ => utf8.encode(page.text).length,
    };
    return Column(
      children: [
        Expanded(
          child: SelectionArea(
            child: ListView(
              key: const Key('content-page-scroll'),
              controller: _scroll,
              children: [
                Text(
                  copy.format(
                    body
                        ? 'content_page.byte_range'
                        : 'content_page.item_range',
                    {
                      'start': page.offset + (count == 0 ? 0 : 1),
                      'end': page.offset + count,
                      'total': page.total,
                    },
                  ),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (page.blockKind != null)
                  Text(
                    [
                      page.blockKind,
                      page.toolName,
                      page.callId,
                    ].whereType<String>().join(' · '),
                    style: monoStyle,
                  ),
                if (body) ...[
                  Text(
                    copy(
                      page.kind == 'arguments'
                          ? 'content_page.arguments_note'
                          : 'content_page.body_note',
                    ),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                  const SizedBox(height: 8),
                  SelectableText(
                    page.text,
                    key: const Key('content-page-text'),
                    style: monoStyle,
                  ),
                ],
                for (final evidence in page.protocolEvidence)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 8),
                    child: SelectableText(
                      '${evidence.name}: ${evidence.value}',
                      style: monoStyle,
                    ),
                  ),
                if (page.kind == 'message')
                  _ContentBlocksView(
                    id: 'page-${widget.exchangeId}-$_cursor',
                    blocks: page.blocks,
                    copy: copy,
                  ),
                for (final (index, message) in page.messages.indexed)
                  _MessageCard(
                    key: ValueKey('page-message-$_cursor-$index'),
                    id: 'page-${widget.exchangeId}-$_cursor-$index',
                    message: message,
                    copy: copy,
                  ),
              ],
            ),
          ),
        ),
        const Divider(),
        _navigation(page),
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    return Dialog(
      child: SizedBox(
        width: 780,
        height: math.min(720.0, MediaQuery.sizeOf(context).height * .82),
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: _ContentPageScope(
            controller: widget.controller,
            openPage: (id, cursor) {
              if (id == widget.exchangeId) _load(cursor);
            },
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        copy('content_page.title'),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                    ),
                    IconButton(
                      key: const Key('content-page-close'),
                      tooltip: copy('content_page.close'),
                      onPressed: () => Navigator.of(context).pop(),
                      icon: const Icon(Icons.close),
                    ),
                  ],
                ),
                Text(
                  widget.exchangeId,
                  style: monoStyle,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                ),
                const SizedBox(height: 8),
                Expanded(
                  child: FutureBuilder<ExchangeContentPage>(
                    future: _page,
                    builder: (context, snapshot) {
                      if (snapshot.connectionState != ConnectionState.done) {
                        return const Center(child: CompactProgressIndicator());
                      }
                      final page = snapshot.data;
                      if (snapshot.hasError || page == null) {
                        return ListView(
                          children: [
                            InlineNotice(
                              message: copy('content_page.unavailable'),
                              error: true,
                            ),
                            TextButton(
                              onPressed: () => _load(_cursor, remember: false),
                              child: Text(copy('common.retry')),
                            ),
                            if (_back.isNotEmpty)
                              TextButton(
                                onPressed: () =>
                                    _load(_back.removeLast(), remember: false),
                                child: Text(copy('common.back')),
                              ),
                          ],
                        );
                      }
                      return _body(page);
                    },
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

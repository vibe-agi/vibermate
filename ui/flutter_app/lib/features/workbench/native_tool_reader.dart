import 'dart:async';
import 'dart:math' as math;
import 'dart:typed_data';
import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';
import '../../core/api/native_tool_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

/// Native operations have their own Capture owner and loader. They never enter
/// the conversation content scope or acquire a human/assistant role.
final class NativeToolReader extends StatefulWidget {
  const NativeToolReader({
    required this.controller,
    required this.copy,
    super.key,
  });
  final WorkbenchController controller;
  final AppCopy copy;
  @override
  State<NativeToolReader> createState() => _NativeToolReaderState();
}

final class _NativeToolReaderState extends State<NativeToolReader> {
  String? _capture;
  @override
  void initState() {
    super.initState();
    _capture = widget.controller.selectedCaptureKey;
    widget.controller.setNativeReaderVisible(true);
  }

  @override
  void didUpdateWidget(NativeToolReader old) {
    super.didUpdateWidget(old);
    if (old.controller != widget.controller) {
      old.controller.setNativeReaderVisible(false);
      widget.controller.setNativeReaderVisible(true);
    }
    if (_capture != widget.controller.selectedCaptureKey) {
      _capture = widget.controller.selectedCaptureKey;
      unawaited(widget.controller.refreshNativeTools());
    }
  }

  @override
  void dispose() {
    widget.controller.setNativeReaderVisible(false);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: widget.controller,
    builder: (context, _) {
      final controller = widget.controller, copy = widget.copy;
      return Column(
        key: const Key('native-tool-reader'),
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: 16),
          Row(
            children: [
              const Icon(Icons.build_outlined, size: 16),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  copy('native.service'),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ),
              IconButton(
                tooltip: copy('status.refresh'),
                onPressed: () => unawaited(controller.refreshNativeTools()),
                icon: const Icon(Icons.refresh, size: 16),
              ),
            ],
          ),
          if (controller.nativeOperationsError != null)
            Text(copy('native.readerUnavailable')),
          if (controller.nativeOperations.isEmpty)
            Text(
              copy('native.empty'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
          for (final op in controller.nativeOperations)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: OutlinedButton(
                key: Key('native-operation-${op.operationId}'),
                onPressed: () => showDialog<void>(
                  context: context,
                  builder: (_) => _NativeInspector(
                    controller: controller,
                    operation: op,
                    copy: copy,
                  ),
                ),
                style: OutlinedButton.styleFrom(
                  alignment: Alignment.centerLeft,
                  padding: const EdgeInsets.all(12),
                  shape: const RoundedRectangleBorder(
                    borderRadius: ViberMetrics.surfaceRadius,
                  ),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Wrap(
                      spacing: 12,
                      runSpacing: 4,
                      children: [
                        Text(
                          op.toolName.isEmpty
                              ? copy('native.service')
                              : op.toolName,
                        ),
                        Text(copy('native.state.${op.state}')),
                      ],
                    ),
                    Text(
                      op.targetOrigin,
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    Text(
                      copy(
                        op.credentialOwner == 'original_client'
                            ? 'native.login'
                            : 'native.loginUnknown',
                      ),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    if (op.state == 'uncertain') Text(copy('native.uncertain')),
                    Text(
                      copy('native.part.${op.result.state}'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
            ),
          if (!controller.nativeOperationsComplete &&
              controller.nativeOperations.isNotEmpty)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton(
                onPressed: () => unawaited(controller.refreshNativeTools()),
                child: Text(copy('native.loadMore')),
              ),
            ),
        ],
      );
    },
  );
}

final class _NativeInspector extends StatefulWidget {
  const _NativeInspector({
    required this.controller,
    required this.operation,
    required this.copy,
  });
  final WorkbenchController controller;
  final NativeToolOperation operation;
  final AppCopy copy;
  @override
  State<_NativeInspector> createState() => _NativeInspectorState();
}

final class _NativeInspectorState extends State<_NativeInspector> {
  String _part = 'tool_result';
  NativeToolOperation? _operation;
  Future<List<NativeRecordedBlock>>? _content;
  late final ScrollController _scroll;
  int _epoch = 0;
  int _operationGeneration = 0;
  String? _signature;
  @override
  void initState() {
    super.initState();
    _scroll = ScrollController();
    _epoch = widget.controller.nativeReaderGeneration;
    _operation = widget.operation;
    widget.controller.addListener(_updated);
    widget.controller.nativeContentInvalidations.addListener(_invalidated);
    _load();
  }

  void _load() {
    final op = _operation;
    final descriptor = op?.part(_part);
    _operationGeneration = op == null
        ? 0
        : widget.controller.nativeOperationGeneration(op.operationId);
    _signature = op == null
        ? null
        : '${op.revision}/$_part/${descriptor!.state}/${descriptor.digest}';
    _content = descriptor?.state == 'available'
        ? widget.controller.loadNativeToolContent(op!, _part)
        : null;
  }

  void _invalidated() {
    final op = _operation;
    if (!mounted ||
        op == null ||
        widget.controller.nativeContentScopeCurrent(
          op,
          _part,
          _epoch,
          _operationGeneration,
        )) {
      return;
    }
    _content = null;
    _signature = null;
    if (_epoch != widget.controller.nativeReaderGeneration ||
        op.captureKey != widget.controller.selectedCaptureKey ||
        widget.controller.nativeOperations.every(
          (current) => current.operationId != op.operationId,
        )) {
      _operation = null;
    }
    if (SchedulerBinding.instance.schedulerPhase ==
        SchedulerPhase.persistentCallbacks) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) {
          setState(() {});
        }
      });
    } else {
      setState(() {});
    }
  }

  void _updated() {
    if (!mounted) {
      return;
    }
    final controller = widget.controller;
    final op = controller.nativeOperations
        .where(
          (op) =>
              op.operationId == widget.operation.operationId &&
              op.captureKey == widget.operation.captureKey,
        )
        .firstOrNull;
    final signature = op == null
        ? null
        : '${op.revision}/$_part/${op.part(_part).state}/${op.part(_part).digest}';
    if (_epoch != controller.nativeReaderGeneration || op == null) {
      setState(() {
        _operation = null;
        _content = null;
        _signature = null;
      });
      return;
    }
    if (signature != _signature) {
      setState(() {
        _operation = op;
        _load();
      });
    }
  }

  @override
  void dispose() {
    widget.controller.removeListener(_updated);
    widget.controller.nativeContentInvalidations.removeListener(_invalidated);
    widget.controller.invalidateNativeInspector(widget.operation.operationId);
    _scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy, op = _operation;
    final state = op?.part(_part).state ?? 'unavailable';
    return AlertDialog(
      title: Text(
        op?.toolName.isNotEmpty == true ? op!.toolName : copy('native.service'),
      ),
      content: SizedBox(
        width: 520,
        height: math.min(520, MediaQuery.sizeOf(context).height * 0.6),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              copy(
                op?.credentialOwner == 'original_client'
                    ? 'native.login'
                    : 'native.loginUnknown',
              ),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              showSelectedIcon: false,
              segments: [
                ButtonSegment(
                  value: 'tool_arguments',
                  label: Text(copy('native.parameters')),
                ),
                ButtonSegment(
                  value: 'tool_result',
                  label: Text(copy('native.result')),
                ),
              ],
              selected: {_part},
              onSelectionChanged: (selection) => setState(() {
                _part = selection.single;
                _load();
              }),
            ),
            const SizedBox(height: 12),
            Expanded(
              child: _content == null
                  ? SingleChildScrollView(
                      child: Text(copy('native.part.$state')),
                    )
                  : FutureBuilder<List<NativeRecordedBlock>>(
                      future: _content,
                      builder: (context, snapshot) {
                        if (snapshot.connectionState != ConnectionState.done) {
                          return Center(child: Text(copy('common.loading')));
                        }
                        if (snapshot.hasError) {
                          return SingleChildScrollView(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text(copy('native.part.unavailable')),
                                TextButton(
                                  onPressed: () => setState(_load),
                                  child: Text(copy('common.retry')),
                                ),
                              ],
                            ),
                          );
                        }
                        if (!snapshot.hasData) {
                          return Center(child: Text(copy('common.loading')));
                        }
                        final blocks = snapshot.data!;
                        if (blocks.isEmpty) {
                          return Text(copy('native.part.unavailable'));
                        }
                        return SingleChildScrollView(
                          controller: _scroll,
                          key: PageStorageKey(
                            'native-inspector-${op?.operationId}-$_part',
                          ),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              for (final block in blocks)
                                Padding(
                                  padding: const EdgeInsets.only(bottom: 12),
                                  child: block.kind == 'image'
                                      ? Column(
                                          crossAxisAlignment:
                                              CrossAxisAlignment.start,
                                          children: [
                                            ConstrainedBox(
                                              constraints: const BoxConstraints(
                                                maxHeight: 240,
                                              ),
                                              child: Image.memory(
                                                block.imageBytes!,
                                                fit: BoxFit.contain,
                                                errorBuilder: (_, _, _) => Text(
                                                  copy(
                                                    'native.part.unavailable',
                                                  ),
                                                ),
                                              ),
                                            ),
                                            TextButton(
                                              onPressed: () => showDialog<void>(
                                                context: context,
                                                builder: (_) =>
                                                    _NativeImageViewer(
                                                      controller:
                                                          widget.controller,
                                                      operation: op!,
                                                      part: _part,
                                                      blockIndex: blocks
                                                          .indexOf(block),
                                                      epoch: _epoch,
                                                      generation:
                                                          _operationGeneration,
                                                      copy: copy,
                                                    ),
                                              ),
                                              child: Text(copy('native.image')),
                                            ),
                                          ],
                                        )
                                      : SelectableText(block.value),
                                ),
                            ],
                          ),
                        );
                      },
                    ),
            ),
            if (op?.state == 'uncertain') Text(copy('native.uncertain')),
            if (op?.state == 'uncertain')
              TextButton(
                onPressed: () {
                  widget.controller.invalidateNativeInspector(op!.operationId);
                  Navigator.pop(context);
                  widget.controller.selectSection(WorkbenchSection.network);
                },
                child: Text(copy('native.reviewNew')),
              ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: Text(copy('content_page.close')),
        ),
      ],
    );
  }
}

/// The route owns only its source scope. Validated bytes are loaded through the
/// native owner and released when that exact scope changes; the widget never
/// closes over a thumbnail's independent image reference.
final class _NativeImageViewer extends StatefulWidget {
  const _NativeImageViewer({
    required this.controller,
    required this.operation,
    required this.part,
    required this.blockIndex,
    required this.epoch,
    required this.generation,
    required this.copy,
  });
  final WorkbenchController controller;
  final NativeToolOperation operation;
  final String part;
  final int blockIndex, epoch, generation;
  final AppCopy copy;
  @override
  State<_NativeImageViewer> createState() => _NativeImageViewerState();
}

final class _NativeImageViewerState extends State<_NativeImageViewer> {
  Uint8List? _bytes;
  bool _valid = true;
  Timer? _expiry;
  bool get _current =>
      _valid &&
      widget.controller.nativeContentScopeCurrent(
        widget.operation,
        widget.part,
        widget.epoch,
        widget.generation,
      );
  @override
  void initState() {
    super.initState();
    widget.controller.nativeContentInvalidations.addListener(_check);
    widget.controller.addListener(_check);
    unawaited(_load());
    _scheduleExpiry();
  }

  Future<void> _load() async {
    try {
      if (!_current) {
        _invalidate();
        return;
      }
      final blocks = await widget.controller.loadNativeToolContent(
        widget.operation,
        widget.part,
      );
      if (!mounted) {
        return;
      }
      if (!_current ||
          widget.blockIndex >= blocks.length ||
          blocks[widget.blockIndex].kind != 'image') {
        _invalidate();
        return;
      }
      setState(() => _bytes = blocks[widget.blockIndex].imageBytes);
    } on Object {
      if (mounted) {
        _invalidate();
      }
    }
  }

  void _scheduleExpiry() {
    final remaining = widget.operation
        .part(widget.part)
        .expiresAt
        ?.difference(DateTime.now().toUtc());
    if (remaining == null || remaining <= Duration.zero) {
      _invalidate();
      return;
    }
    _expiry = Timer(
      remaining > const Duration(days: 1) ? const Duration(days: 1) : remaining,
      () {
        _check();
        if (_valid) {
          _scheduleExpiry();
        }
      },
    );
  }

  void _check() {
    if (!_current) {
      _invalidate();
    }
  }

  void _invalidate() {
    if (!_valid) {
      return;
    }
    _valid = false;
    _expiry?.cancel();
    final old = _bytes;
    _bytes = null;
    if (old != null) {
      unawaited(MemoryImage(old).evict());
    }
    if (!mounted) {
      return;
    }
    if (SchedulerBinding.instance.schedulerPhase ==
        SchedulerPhase.persistentCallbacks) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) {
          setState(() {});
        }
      });
    } else {
      setState(() {});
    }
  }

  @override
  void dispose() {
    widget.controller.nativeContentInvalidations.removeListener(_check);
    widget.controller.removeListener(_check);
    _expiry?.cancel();
    final old = _bytes;
    _bytes = null;
    if (old != null) {
      unawaited(MemoryImage(old).evict());
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Dialog(
    key: const Key('native-image-viewer'),
    child: SizedBox(
      width: 1100,
      height: MediaQuery.sizeOf(context).height * .85,
      child: Column(
        children: [
          Row(
            children: [
              const SizedBox(width: 12),
              Expanded(child: Text(widget.copy('native.image'))),
              IconButton(
                key: const Key('native-image-close'),
                tooltip: widget.copy('content_page.close'),
                onPressed: () => Navigator.pop(context),
                icon: const Icon(Icons.close),
              ),
            ],
          ),
          Expanded(
            child: !_valid
                ? Center(child: Text(widget.copy('native.part.unavailable')))
                : _bytes == null
                ? Center(child: Text(widget.copy('common.loading')))
                : InteractiveViewer(
                    minScale: .5,
                    maxScale: 8,
                    child: Image.memory(
                      _bytes!,
                      fit: BoxFit.contain,
                      errorBuilder: (_, _, _) =>
                          Text(widget.copy('native.part.unavailable')),
                    ),
                  ),
          ),
        ],
      ),
    ),
  );
}

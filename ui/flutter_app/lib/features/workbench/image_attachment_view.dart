part of 'conversation_timeline.dart';

final class _ImageAttachmentView extends StatefulWidget {
  const _ImageAttachmentView({required this.fingerprint, required this.copy});
  final String? fingerprint;
  final AppCopy copy;
  @override
  State<_ImageAttachmentView> createState() => _ImageAttachmentViewState();
}

final class _ImageAttachmentViewState extends State<_ImageAttachmentView> {
  RetainedImagePreview? _preview;
  String? _error, _identity;
  bool _loading = false;
  int _generation = 0;
  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _refreshIdentity();
  }

  @override
  void didUpdateWidget(covariant _ImageAttachmentView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.fingerprint != widget.fingerprint) _refreshIdentity();
  }

  void _refreshIdentity() {
    final scope = _ContentPageScope.of(context);
    final identity =
        '${scope?.exchangeId}:${widget.fingerprint}:${identityHashCode(scope?.controller)}';
    if (identity != _identity) {
      _identity = identity;
      final generation = ++_generation;
      _preview = null;
      _error = null;
      _loading = true;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && generation == _generation) unawaited(_load());
      });
    }
  }

  Future<void> _load() async {
    final scope = _ContentPageScope.of(context),
        fingerprint = widget.fingerprint;
    final generation = ++_generation;
    if (scope?.exchangeId == null || fingerprint == null) {
      setState(() {
        _error = 'reader.image_unavailable';
        _loading = false;
      });
      return;
    }
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final image = await scope!.controller.loadImagePreview(
        scope.exchangeId!,
        fingerprint,
      );
      if (mounted && generation == _generation) {
        setState(() => _preview = image);
      }
    } catch (error) {
      final key = switch (error) {
        ControlProblem(reasonCode: 'image_too_large') =>
          'reader.image_too_large',
        ControlProblem(reasonCode: 'image_format_unsupported') =>
          'reader.image_unsupported',
        ControlProblem(status: 401 || 403) => 'reader.image_denied',
        ControlProblem(status: 404 || 410) => 'reader.image_unavailable',
        _ => 'reader.image_load_failed',
      };
      if (mounted && generation == _generation) setState(() => _error = key);
    } finally {
      if (mounted && generation == _generation) {
        setState(() => _loading = false);
      }
    }
  }

  void _open(RetainedImagePreview preview) {
    showDialog<void>(
      context: context,
      builder: (context) => Dialog(
        child: SizedBox(
          width: 1100,
          height: MediaQuery.sizeOf(context).height * .85,
          child: Column(
            children: [
              Row(
                children: [
                  const SizedBox(width: 12),
                  Expanded(child: Text(widget.copy('reader.image'))),
                  IconButton(
                    key: const Key('image-preview-close'),
                    tooltip: widget.copy('content_page.close'),
                    onPressed: () => Navigator.of(context).pop(),
                    icon: const Icon(Icons.close),
                  ),
                ],
              ),
              Expanded(
                child: InteractiveViewer(
                  minScale: .5,
                  maxScale: 8,
                  child: Image.memory(
                    preview.bytes,
                    fit: BoxFit.contain,
                    semanticLabel: widget.copy('reader.image'),
                    errorBuilder: (_, _, _) =>
                        Text(widget.copy('reader.image_unsupported')),
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final preview = _preview, copy = widget.copy;
    return Align(
      alignment: Alignment.centerLeft,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 6),
        child: preview != null
            ? Semantics(
                button: true,
                label: copy('reader.image_open'),
                child: InkWell(
                  key: ValueKey('image-thumbnail-${widget.fingerprint}'),
                  onTap: () => _open(preview),
                  child: ClipRRect(
                    borderRadius: ViberMetrics.controlRadius,
                    child: Image.memory(
                      preview.bytes,
                      width: 180,
                      height: 120,
                      cacheWidth: 360,
                      fit: BoxFit.contain,
                      semanticLabel: copy('reader.image'),
                      errorBuilder: (_, _, _) =>
                          Text(copy('reader.image_unsupported')),
                    ),
                  ),
                ),
              )
            : Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  const Icon(Icons.image_outlined, size: 18),
                  const SizedBox(width: 8),
                  Flexible(
                    child: Text(
                      copy(
                        _loading ? 'common.loading' : _error ?? 'reader.image',
                      ),
                    ),
                  ),
                  if (!_loading)
                    IconButton(
                      tooltip: copy('common.retry'),
                      onPressed: _load,
                      icon: const Icon(Icons.refresh, size: 16),
                    ),
                ],
              ),
      ),
    );
  }
}

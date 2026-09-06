import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Plaintext belongs only to this explicitly opened view, never to an evidence
/// model, cache, or diagnostic export. Late replies cannot reopen a hidden view.
final class RawHeaderReveal extends StatefulWidget {
  const RawHeaderReveal({
    required this.identity,
    required this.name,
    required this.redactedText,
    required this.reveal,
    required this.copy,
    super.key,
  });

  final String identity;
  final String name;
  final String redactedText;
  final Future<String> Function() reveal;
  final AppCopy copy;

  @override
  State<RawHeaderReveal> createState() => _RawHeaderRevealState();
}

final class _RawHeaderRevealState extends State<RawHeaderReveal>
    with WidgetsBindingObserver {
  String? _value;
  bool _loading = false;
  bool _unavailable = false;
  int _generation = 0;
  Timer? _timer;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  void _clear() {
    _generation++;
    _timer?.cancel();
    _timer = null;
    _value = null;
    _loading = false;
    _unavailable = false;
  }

  @override
  void didUpdateWidget(RawHeaderReveal oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.identity != widget.identity ||
        oldWidget.name != widget.name ||
        oldWidget.redactedText != widget.redactedText) {
      _clear();
    }
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state != AppLifecycleState.resumed) setState(_clear);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _clear();
    super.dispose();
  }

  Future<void> _toggle() async {
    if (_value != null || _loading) {
      setState(_clear);
      return;
    }
    final generation = ++_generation;
    setState(() {
      _loading = true;
      _unavailable = false;
    });
    // This also bounds an in-flight reveal, not just an already-visible value.
    _timer?.cancel();
    _timer = Timer(const Duration(seconds: 60), () {
      if (mounted) setState(_clear);
    });
    try {
      final value = await widget.reveal();
      if (!mounted || generation != _generation) return;
      setState(() {
        _value = value;
        _loading = false;
      });
    } catch (_) {
      if (!mounted || generation != _generation) return;
      setState(() {
        _loading = false;
        _unavailable = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: SelectableText(
              _value == null ? widget.redactedText : '${widget.name}: $_value',
              style: monoStyle,
            ),
          ),
          IconButton(
            key: Key('reveal-header-${widget.identity}'),
            tooltip: widget.copy(
              _value != null || _loading
                  ? 'common.hide_secret'
                  : 'exchange.raw.header_reveal',
            ),
            visualDensity: VisualDensity.compact,
            onPressed: () => unawaited(_toggle()),
            icon: _loading
                ? const CompactProgressIndicator()
                : Icon(
                    _value == null
                        ? Icons.visibility_outlined
                        : Icons.visibility_off_outlined,
                    size: 16,
                  ),
          ),
        ],
      ),
      if (_unavailable)
        Text(
          widget.copy('exchange.raw.header_unavailable'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.warning),
        ),
    ],
  );
}

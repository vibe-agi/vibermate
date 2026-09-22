import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class RuntimeRootCAPanel extends StatefulWidget {
  const RuntimeRootCAPanel({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<RuntimeRootCAPanel> createState() => _RuntimeRootCAPanelState();
}

final class _RuntimeRootCAPanelState extends State<RuntimeRootCAPanel> {
  RuntimeRootCertificate? _certificate;
  bool _loading = true;
  bool _saving = false;
  String? _error;
  bool _saved = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
      _saved = false;
    });
    try {
      final certificate = await widget.controller.loadRuntimeRootCA();
      if (!mounted) return;
      setState(() {
        _certificate = certificate;
        _loading = false;
      });
    } catch (_) {
      if (!mounted) return;
      setState(() {
        _certificate = null;
        _loading = false;
        _error = 'load_error';
      });
    }
  }

  Future<void> _save() async {
    if (_loading || _saving || _certificate == null) return;
    setState(() {
      _saving = true;
      _error = null;
      _saved = false;
    });
    try {
      // Re-read the current public Root; never export a stale trust anchor
      // after an explicit Runtime Root replacement in another session.
      final certificate = await widget.controller.loadRuntimeRootCA();
      if (!mounted) return;
      setState(() => _certificate = certificate);
      final saved = await widget.controller.saveRuntimeRootCA(certificate);
      if (mounted) setState(() => _saved = saved);
    } catch (_) {
      if (mounted) setState(() => _error = 'save_error');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final certificate = _certificate;
    final textStyle = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted);
    return Container(
      key: const Key('runtime-root-ca-settings-panel'),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.verified_user_outlined,
                size: 18,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: ContextHelpHeading(
                  title: copy('settings.runtime_ca.title'),
                  message: copy('settings.runtime_ca.detail'),
                  dismissLabel: copy('common.dismiss'),
                ),
              ),
              IconButton(
                key: const Key('runtime-root-ca-refresh'),
                tooltip: copy('settings.runtime_ca.refresh'),
                onPressed: _loading || _saving ? null : _load,
                icon: const Icon(Icons.refresh, size: 18),
              ),
            ],
          ),
          Text(copy('settings.root_ca.scope'), style: textStyle),
          const SizedBox(height: 10),
          if (_loading) const CompactProgressIndicator(),
          if (!_loading && certificate != null) ...[
            ExpansionTile(
              key: const Key('runtime-root-ca-manual-details'),
              tilePadding: EdgeInsets.zero,
              childrenPadding: EdgeInsets.zero,
              title: Text(copy('settings.runtime_ca.manual')),
              expandedCrossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(copy('settings.runtime_ca.fingerprint'), style: textStyle),
                SelectableText(
                  certificate.fingerprint,
                  key: const Key('runtime-root-ca-fingerprint'),
                  style: textStyle,
                ),
                const SizedBox(height: 6),
                Text(
                  '${copy('settings.runtime_ca.expires')}: ${certificate.notAfter.toUtc().toIso8601String()}',
                  style: textStyle,
                ),
                const SizedBox(height: 8),
                Text(copy('settings.runtime_ca.guide'), style: textStyle),
                const SizedBox(height: 10),
                OutlinedButton.icon(
                  key: const Key('runtime-root-ca-download'),
                  onPressed: _saving ? null : _save,
                  icon: _saving
                      ? const CompactProgressIndicator()
                      : const Icon(Icons.download_outlined, size: 16),
                  label: Text(copy('settings.runtime_ca.download')),
                ),
              ],
            ),
          ],
          if (_error != null) ...[
            const SizedBox(height: 8),
            InlineNotice(
              message: copy('settings.runtime_ca.$_error'),
              error: true,
            ),
          ],
          if (_saved) ...[
            const SizedBox(height: 8),
            Text(copy('settings.runtime_ca.saved'), style: textStyle),
          ],
        ],
      ),
    );
  }
}

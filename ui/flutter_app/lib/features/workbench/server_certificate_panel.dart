import 'package:flutter/material.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class ServerCertificatePanel extends StatefulWidget {
  const ServerCertificatePanel({
    required this.controller,
    required this.copy,
    super.key,
  });
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<ServerCertificatePanel> createState() => _ServerCertificatePanelState();
}

final class _ServerCertificatePanelState extends State<ServerCertificatePanel> {
  RuntimeServerCertificate? _certificate;
  bool _loading = true;
  bool _saving = false;
  String? _error;
  bool _saved = false;
  bool _applied = false;
  bool _dirty = false;
  final _hosts = TextEditingController();

  @override
  void dispose() {
    _hosts.dispose();
    super.dispose();
  }

  void _adopt(RuntimeServerCertificate certificate, {bool fillHosts = false}) {
    _certificate = certificate;
    if (fillHosts) {
      _hosts.text = (certificate.pending ?? certificate).hosts
          .where(
            (host) => !const {'localhost', '127.0.0.1', '::1'}.contains(host),
          )
          .join(', ');
      _dirty = false;
    }
  }

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
      _applied = false;
    });
    try {
      final certificate = await widget.controller.loadServerCertificate();
      if (!mounted) return;
      setState(() {
        _adopt(certificate, fillHosts: true);
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

  Future<void> _save({bool pending = false, bool ca = false}) async {
    if (_loading || _saving) return;
    setState(() {
      _saving = true;
      _error = null;
      _saved = false;
    });
    try {
      // Prepared certificates and their persistent CA remain downloadable
      // even if applying a candidate made fresh TLS connections untrusted.
      final snapshot = pending || ca
          ? _certificate!
          : await widget.controller.loadServerCertificate();
      if (!mounted) return;
      if (!pending && !ca) setState(() => _adopt(snapshot));
      final PublicCertificate? certificate = ca
          ? snapshot.ca
          : pending
          ? snapshot.pending
          : snapshot;
      if (certificate == null || !certificate.available) return;
      final saved = await widget.controller.saveServerCertificate(certificate);
      if (mounted) setState(() => _saved = saved);
    } catch (_) {
      if (mounted) setState(() => _error = 'save_error');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  String _mutationError(Object error, String fallback) {
    if (error is ControlProblem) {
      return switch (error.reasonCode) {
        'server_certificate_conflict' => 'conflict',
        'invalid_server_certificate_hosts' => 'invalid_hosts',
        'server_certificate_access_host_missing' => 'access_host_missing',
        'server_certificate_ca_changed' => 'ca_changed',
        _ => fallback,
      };
    }
    return fallback;
  }

  Future<void> _stage() async {
    if (_saving || _loading || _certificate == null) return;
    final hosts = _hosts.text.trim().isEmpty
        ? <String>[]
        : _hosts.text
              .split(RegExp(r'[,，\s]+'))
              .where((host) => host.isNotEmpty)
              .toList();
    if (hosts.length > 32 ||
        hosts.any(
          (host) =>
              host.length > 253 ||
              host.contains('://') ||
              host.contains('/') ||
              host.contains('*'),
        )) {
      setState(() => _error = 'invalid_hosts');
      return;
    }
    setState(() {
      _saving = true;
      _error = null;
      _saved = false;
      _applied = false;
    });
    try {
      final certificate = await widget.controller.stageServerCertificate(
        _certificate!,
        hosts,
      );
      if (mounted) setState(() => _adopt(certificate, fillHosts: true));
    } catch (error) {
      if (mounted) {
        setState(() => _error = _mutationError(error, 'stage_error'));
      }
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _apply() async {
    if (_saving || _loading || _dirty || _certificate?.pending == null) return;
    final snapshot = _certificate!;
    final pending = snapshot.pending!;
    final copy = widget.copy;
    var acknowledged = false;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setDialogState) => AlertDialog(
          scrollable: true,
          title: Text(copy('settings.server_certificate.confirm_title')),
          content: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(copy('settings.server_certificate.confirm_detail')),
              const SizedBox(height: 12),
              SelectableText(pending.hosts.join(', ')),
              const SizedBox(height: 8),
              SelectableText(pending.fingerprint),
              CheckboxListTile(
                key: const Key('server-certificate-confirm-trust'),
                contentPadding: EdgeInsets.zero,
                title: Text(copy('settings.server_certificate.confirm_trust')),
                value: acknowledged,
                onChanged: (value) =>
                    setDialogState(() => acknowledged = value == true),
              ),
            ],
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: Text(copy('settings.server_certificate.cancel')),
            ),
            FilledButton(
              key: const Key('server-certificate-confirm-apply'),
              onPressed: acknowledged
                  ? () => Navigator.pop(context, true)
                  : null,
              child: Text(copy('settings.server_certificate.apply')),
            ),
          ],
        ),
      ),
    );
    if (confirmed != true || !mounted) return;
    setState(() {
      _saving = true;
      _error = null;
      _saved = false;
    });
    try {
      final certificate = await widget.controller.applyServerCertificate(
        snapshot,
      );
      if (mounted) {
        setState(() {
          _adopt(certificate, fillHosts: true);
          _applied = true;
        });
      }
    } catch (error) {
      if (mounted) {
        setState(() => _error = _mutationError(error, 'apply_unknown'));
      }
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
      key: const Key('server-certificate-settings-panel'),
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
                child: Text(
                  copy('settings.server_certificate.title'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              IconButton(
                key: const Key('server-certificate-refresh'),
                tooltip: copy('settings.server_certificate.refresh'),
                onPressed: _loading || _saving ? null : _load,
                icon: const Icon(Icons.refresh, size: 18),
              ),
            ],
          ),
          Text(copy('settings.server_certificate.detail'), style: textStyle),
          const SizedBox(height: 10),
          if (_loading) const CompactProgressIndicator(),
          if (!_loading && certificate != null && !certificate.available)
            Text(copy('settings.server_certificate.http'), style: textStyle),
          if (!_loading && certificate != null && certificate.available) ...[
            Text(
              copy('settings.server_certificate.active'),
              style: Theme.of(context).textTheme.titleSmall,
            ),
            const SizedBox(height: 6),
            SelectableText(
              '${copy('settings.server_certificate.hosts')}: ${certificate.hosts.join(', ')}',
              style: textStyle,
            ),
            const SizedBox(height: 6),
            Text(
              copy('settings.server_certificate.fingerprint'),
              style: textStyle,
            ),
            SelectableText(
              certificate.fingerprint,
              key: const Key('server-certificate-fingerprint'),
              style: textStyle,
            ),
            const SizedBox(height: 6),
            Text(
              '${copy('settings.server_certificate.expires')}: ${certificate.notAfter!.toUtc().toIso8601String()}',
              style: textStyle,
            ),
            const SizedBox(height: 8),
            Text(copy('settings.server_certificate.guide'), style: textStyle),
            const SizedBox(height: 10),
            OutlinedButton.icon(
              key: const Key('server-certificate-download'),
              onPressed: _saving ? null : () => _save(),
              icon: _saving
                  ? const CompactProgressIndicator()
                  : const Icon(Icons.download_outlined, size: 16),
              label: Text(copy('settings.server_certificate.download')),
            ),
            if (certificate.ca case final ca?) ...[
              const Divider(height: 28),
              Text(
                copy(
                  'settings.server_certificate.${ca.unified ? 'ca_title' : 'legacy_ca_title'}',
                ),
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 6),
              Text(
                copy(
                  'settings.server_certificate.${ca.unified ? 'ca_detail' : 'legacy_ca_detail'}',
                ),
                style: textStyle,
              ),
              const SizedBox(height: 8),
              Text(
                copy('settings.server_certificate.fingerprint'),
                style: textStyle,
              ),
              SelectableText(
                ca.fingerprint,
                key: const Key('server-ca-fingerprint'),
                style: textStyle,
              ),
              const SizedBox(height: 6),
              Text(
                '${copy('settings.server_certificate.expires')}: ${ca.notAfter.toUtc().toIso8601String()}',
                style: textStyle,
              ),
              const SizedBox(height: 8),
              Text(
                copy(
                  'settings.server_certificate.${certificate.issuedByCA ? 'ca_active' : 'ca_migration'}',
                ),
                key: const Key('server-ca-status'),
                style: textStyle,
              ),
              const SizedBox(height: 10),
              OutlinedButton.icon(
                key: const Key('server-ca-download'),
                onPressed: _saving ? null : () => _save(ca: true),
                icon: const Icon(Icons.download_outlined, size: 16),
                label: Text(
                  copy(
                    'settings.server_certificate.${ca.unified ? 'ca_download' : 'legacy_ca_download'}',
                  ),
                ),
              ),
            ],
            if (certificate.managed) ...[
              const Divider(height: 28),
              TextField(
                key: const Key('server-certificate-hosts-input'),
                controller: _hosts,
                enabled: !_saving,
                minLines: 1,
                maxLines: 4,
                maxLength: 8192,
                onChanged: (_) => setState(() => _dirty = true),
                decoration: InputDecoration(
                  labelText: copy('settings.server_certificate.input_label'),
                  floatingLabelBehavior: FloatingLabelBehavior.always,
                  counterText: '',
                ),
              ),
              const SizedBox(height: 8),
              Text(
                copy('settings.server_certificate.input_example'),
                key: const Key('server-certificate-hosts-example'),
                style: textStyle,
              ),
              const SizedBox(height: 4),
              Text(
                copy('settings.server_certificate.input_help'),
                style: textStyle,
              ),
              const SizedBox(height: 10),
              OutlinedButton.icon(
                key: const Key('server-certificate-stage'),
                onPressed: _saving ? null : _stage,
                icon: const Icon(Icons.add_moderator_outlined, size: 16),
                label: Text(copy('settings.server_certificate.stage')),
              ),
              if (certificate.pending case final pending?) ...[
                const SizedBox(height: 14),
                if (certificate.ca?.unified == true && !pending.issuedByCA)
                  Text(
                    copy('settings.server_certificate.ca_changed'),
                    style: textStyle,
                  ),
                Text(
                  copy('settings.server_certificate.pending'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
                const SizedBox(height: 6),
                Text(
                  copy(
                    'settings.server_certificate.${pending.issuedByCA ? 'pending_ca_help' : 'pending_help'}',
                  ),
                  style: textStyle,
                ),
                const SizedBox(height: 8),
                SelectableText(pending.hosts.join(', '), style: textStyle),
                const SizedBox(height: 6),
                SelectableText(
                  pending.fingerprint,
                  key: const Key('server-certificate-pending-fingerprint'),
                  style: textStyle,
                ),
                const SizedBox(height: 10),
                Wrap(
                  spacing: 8,
                  runSpacing: 8,
                  children: [
                    OutlinedButton.icon(
                      key: const Key('server-certificate-pending-download'),
                      onPressed: _saving ? null : () => _save(pending: true),
                      icon: const Icon(Icons.download_outlined, size: 16),
                      label: Text(
                        copy('settings.server_certificate.pending_download'),
                      ),
                    ),
                    FilledButton.icon(
                      key: const Key('server-certificate-apply'),
                      onPressed:
                          _saving ||
                              _dirty ||
                              (certificate.ca?.unified == true &&
                                  !pending.issuedByCA)
                          ? null
                          : _apply,
                      icon: const Icon(Icons.check, size: 16),
                      label: Text(copy('settings.server_certificate.apply')),
                    ),
                  ],
                ),
                if (_dirty)
                  Text(
                    copy('settings.server_certificate.dirty'),
                    style: textStyle,
                  ),
              ],
            ] else ...[
              const SizedBox(height: 8),
              Text(
                copy('settings.server_certificate.external'),
                style: textStyle,
              ),
            ],
          ],
          if (_error != null) ...[
            const SizedBox(height: 8),
            InlineNotice(
              message: copy('settings.server_certificate.$_error'),
              error: true,
            ),
          ],
          if (_saved) ...[
            const SizedBox(height: 8),
            Text(copy('settings.server_certificate.saved'), style: textStyle),
          ],
          if (_applied) ...[
            const SizedBox(height: 8),
            Text(copy('settings.server_certificate.applied'), style: textStyle),
          ],
        ],
      ),
    );
  }
}

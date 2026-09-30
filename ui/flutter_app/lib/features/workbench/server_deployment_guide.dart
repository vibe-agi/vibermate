import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

/// Deployment instructions only: never changes a listener or contacts a CA.
final class ServerDeploymentGuide extends StatelessWidget {
  const ServerDeploymentGuide({required this.copy, super.key});
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Container(
    key: const Key('server-deployment-guide'),
    padding: const EdgeInsets.all(12),
    decoration: BoxDecoration(
      border: Border.all(color: context.viberColors.dividerSoft),
      borderRadius: ViberMetrics.surfaceRadius,
    ),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          copy('deployment.title'),
          style: Theme.of(context).textTheme.titleSmall,
        ),
        const SizedBox(height: 6),
        Text(
          copy('deployment.detail'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 10),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            OutlinedButton.icon(
              key: const Key('server-deployment-open'),
              onPressed: () => unawaited(
                showDialog<void>(
                  context: context,
                  builder: (_) => _PublicDeploymentDialog(copy: copy),
                ),
              ),
              icon: const Icon(Icons.public, size: 15),
              label: Text(copy('deployment.configure')),
            ),
            TextButton.icon(
              onPressed: () => unawaited(
                launchUrl(
                  Uri.parse(copy('deployment.docs_url')),
                  mode: LaunchMode.externalApplication,
                ),
              ),
              icon: const Icon(Icons.open_in_new, size: 14),
              label: Text(copy('deployment.other')),
            ),
          ],
        ),
      ],
    ),
  );
}

bool _validPublicName(String value) {
  final labels = value.split('.');
  return value.length <= 253 &&
      labels.length > 1 &&
      labels.every(
        (label) =>
            RegExp(r'^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$').hasMatch(label),
      ) &&
      !RegExp(r'^[0-9.]+$').hasMatch(value) &&
      ![
        'localhost',
        'local',
        'lan',
        'test',
        'example',
        'invalid',
        'internal',
        'home.arpa',
      ].any((suffix) => value == suffix || value.endsWith('.$suffix'));
}

// Keep generated shell and dotenv values literal. Unusual mailbox forms can
// still be configured by the deployment guide, not interpolated by this form.
bool _validContact(String value) =>
    value.length <= 320 &&
    RegExp(
      r'^[A-Za-z0-9._+%\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,63}$',
    ).hasMatch(value);

({String? environment, String start, String recovery, String? logs})?
publicDeploymentRecipe({
  required String domain,
  required String email,
  required bool docker,
  required bool termsAccepted,
}) {
  final host = domain.trim().toLowerCase();
  final contact = email.trim();
  if (!_validPublicName(host) || !_validContact(contact) || !termsAccepted) {
    return null;
  }
  const compose =
      'docker compose --env-file .env.public -f compose.public.yaml';
  return (
    environment: docker
        ? 'VIBERMATE_PUBLIC_HOST=$host\nVIBERMATE_ACME_EMAIL=$contact\nVIBERMATE_PUBLIC_BIND_ADDRESS=0.0.0.0\nVIBERMATE_PUBLIC_DATA_VOLUME=vibermate-public-data\nVIBERMATE_IMAGE=vibermate-runtime:local\nVIBERMATE_TRUSTED_PROXIES=none'
        : null,
    start: docker
        ? '$compose up -d --wait --wait-timeout 120'
        : './vibermated server \\\n  --listen 0.0.0.0:8443 \\\n  --access-address $host:443 \\\n  --transport automatic_tls \\\n  --acme-agree-terms \\\n  --acme-email $contact \\\n  --acme-challenge tls_alpn_01',
    recovery: docker
        ? '$compose exec vibermate /opt/vibermate/vibermated server recovery-key --data-dir /data'
        : './vibermated server recovery-key',
    logs: docker ? '$compose logs --tail=100 -f vibermate' : null,
  );
}

final class _PublicDeploymentDialog extends StatefulWidget {
  const _PublicDeploymentDialog({required this.copy});
  final AppCopy copy;
  @override
  State<_PublicDeploymentDialog> createState() =>
      _PublicDeploymentDialogState();
}

final class _PublicDeploymentDialogState
    extends State<_PublicDeploymentDialog> {
  final _domain = TextEditingController();
  final _email = TextEditingController();
  bool _docker = true, _terms = false;
  String? _notice;

  @override
  void dispose() {
    _domain.dispose();
    _email.dispose();
    super.dispose();
  }

  Future<void> _copy(String value) async {
    try {
      await Clipboard.setData(ClipboardData(text: value));
      if (mounted) setState(() => _notice = 'deployment.copied');
    } catch (_) {
      if (mounted) setState(() => _notice = 'deployment.copy_failed');
    }
  }

  Widget _code(String label, String value, String key) => Padding(
    padding: const EdgeInsets.only(top: 12),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(label, style: Theme.of(context).textTheme.labelLarge),
            ),
            IconButton(
              key: Key('deployment-copy-$key'),
              tooltip: widget.copy('deployment.copy'),
              icon: const Icon(Icons.copy, size: 15),
              onPressed: () => unawaited(_copy(value)),
            ),
          ],
        ),
        SelectableText(
          value,
          key: Key('deployment-code-$key'),
          style: monoStyle,
        ),
      ],
    ),
  );

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final recipe = publicDeploymentRecipe(
      domain: _domain.text,
      email: _email.text,
      docker: _docker,
      termsAccepted: _terms,
    );
    return AlertDialog(
      key: const Key('server-deployment-dialog'),
      title: Text(copy('deployment.configure')),
      content: SizedBox(
        width: ViberMetrics.dialogStandardWidth,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                copy('deployment.instructions_only'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: 14),
              CompactLabeledControl(
                label: copy('deployment.method'),
                child: CompactSelectField<bool>(
                  key: const Key('deployment-method'),
                  initialValue: _docker,
                  isExpanded: true,
                  items: [
                    DropdownMenuItem(
                      value: true,
                      child: Text(copy('deployment.docker')),
                    ),
                    DropdownMenuItem(
                      value: false,
                      child: Text(copy('deployment.native')),
                    ),
                  ],
                  onChanged: (value) {
                    if (value != null) setState(() => _docker = value);
                  },
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                key: const Key('deployment-domain'),
                controller: _domain,
                decoration: InputDecoration(
                  labelText: copy('deployment.domain'),
                  hintText: 'runtime.example.com',
                  errorText:
                      _domain.text.isNotEmpty &&
                          !_validPublicName(_domain.text.trim().toLowerCase())
                      ? copy('deployment.domain_invalid')
                      : null,
                ),
                onChanged: (_) => setState(() => _notice = null),
              ),
              const SizedBox(height: 12),
              TextField(
                key: const Key('deployment-email'),
                controller: _email,
                decoration: InputDecoration(
                  labelText: copy('deployment.email'),
                  hintText: 'admin@example.com',
                  errorText:
                      _email.text.isNotEmpty &&
                          !_validContact(_email.text.trim())
                      ? copy('deployment.email_invalid')
                      : null,
                ),
                onChanged: (_) => setState(() => _notice = null),
              ),
              const SizedBox(height: 14),
              Text(
                copy(
                  _docker
                      ? 'deployment.prerequisites.docker'
                      : 'deployment.prerequisites.native',
                ),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: 8),
              Text(
                copy('deployment.trust'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: 8),
              CompactCheckboxField(
                key: const Key('deployment-terms'),
                value: _terms,
                label: copy('deployment.terms'),
                description: copy('deployment.terms_detail'),
                onChanged: (value) => setState(() => _terms = value),
              ),
              Align(
                alignment: Alignment.centerLeft,
                child: TextButton(
                  onPressed: () => unawaited(
                    launchUrl(
                      Uri.parse('https://letsencrypt.org/repository/'),
                      mode: LaunchMode.externalApplication,
                    ),
                  ),
                  child: Text(copy('deployment.read_terms')),
                ),
              ),
              if (recipe == null)
                Text(
                  copy('deployment.complete_form'),
                  style: Theme.of(context).textTheme.bodySmall,
                )
              else ...[
                if (recipe.environment case final value?)
                  _code(copy('deployment.env_file'), value, 'env'),
                _code(copy('deployment.start'), recipe.start, 'start'),
                const SizedBox(height: 12),
                SelectableText(
                  'https://${_domain.text.trim().toLowerCase()}',
                  style: monoStyle,
                ),
                Text(
                  copy('deployment.first_certificate'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                ExpansionTile(
                  tilePadding: EdgeInsets.zero,
                  title: Text(copy('deployment.after_start')),
                  children: [
                    Text(
                      copy('deployment.recovery_hint'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    _code(
                      copy('deployment.recovery'),
                      recipe.recovery,
                      'recovery',
                    ),
                    if (recipe.logs case final value?)
                      _code(copy('deployment.logs'), value, 'logs'),
                    Text(
                      copy('deployment.retention'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              ],
              if (_notice case final notice?)
                Text(
                  copy(notice),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: Text(copy('common.dismiss')),
        ),
      ],
    );
  }
}

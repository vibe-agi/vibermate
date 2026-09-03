import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

typedef EgressProfileLoader = Future<EgressProfileCatalog> Function();

/// Selects one exact, published network-exit revision for a protocol path.
final class EgressProfileButton extends StatelessWidget {
  const EgressProfileButton({
    required this.plan,
    required this.copy,
    required this.enabled,
    required this.loadProfiles,
    required this.onChanged,
    super.key,
  });

  final EnvironmentProtocolPlan plan;
  final AppCopy copy;
  final bool enabled;
  final EgressProfileLoader loadProfiles;
  final ValueChanged<EgressProfileRevision> onChanged;

  @override
  Widget build(BuildContext context) => Container(
    color: context.viberColors.panelRaised.withValues(alpha: 0.34),
    padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
    child: CompactLabeledControl(
      label: copy('environment.egress.label'),
      detail: copy('environment.egress.profile.detail'),
      child: SizedBox(
        width: double.infinity,
        height: ViberMetrics.controlHeight,
        child: OutlinedButton.icon(
          key: Key('environment-egress-${plan.id}'),
          onPressed: enabled ? () => unawaited(_edit(context)) : null,
          icon: const Icon(Icons.alt_route_rounded, size: 14),
          label: Align(
            alignment: Alignment.centerLeft,
            child: Text(
              egressProfileSummary(copy, plan.egressProfile),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ),
          style: OutlinedButton.styleFrom(
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: 9),
          ),
        ),
      ),
    ),
  );

  Future<void> _edit(BuildContext context) async {
    final profile = await showDialog<EgressProfileRevision>(
      context: context,
      barrierDismissible: true,
      builder: (context) => _EgressProfileSelectionDialog(
        planId: plan.id,
        initial: plan.egressProfile,
        copy: copy,
        loadProfiles: loadProfiles,
      ),
    );
    if (profile != null) onChanged(profile);
  }
}

String egressProfileSummary(AppCopy copy, EgressProfileRevision profile) {
  if (profile.id == EgressProfileRevision.direct.id) {
    return egressPolicySummary(copy, profile.policy);
  }
  return '${profile.displayName} · r${profile.revision}';
}

String egressPolicySummary(AppCopy copy, TrafficEgressPolicy policy) {
  final proxy = switch (policy.proxy.kind) {
    'direct' => copy('environment.egress.proxy.direct'),
    'socks5' => 'SOCKS5 · ${policy.proxy.endpoint}',
    _ => policy.proxy.kind,
  };
  final resolver = switch (policy.resolver.kind) {
    'system' => copy('environment.egress.resolver.system'),
    'doh' =>
      policy.resolver.transport == 'proxy'
          ? 'DoH · ${copy('environment.egress.doh.transport.proxy')}'
          : 'DoH · ${copy('environment.egress.doh.transport.direct')}',
    _ => policy.resolver.kind,
  };
  return '$proxy · $resolver';
}

final class _EgressProfileSelectionDialog extends StatefulWidget {
  const _EgressProfileSelectionDialog({
    required this.planId,
    required this.initial,
    required this.copy,
    required this.loadProfiles,
  });

  final String planId;
  final EgressProfileRevision initial;
  final AppCopy copy;
  final EgressProfileLoader loadProfiles;

  @override
  State<_EgressProfileSelectionDialog> createState() =>
      _EgressProfileSelectionDialogState();
}

final class _EgressProfileSelectionDialogState
    extends State<_EgressProfileSelectionDialog> {
  List<EgressProfileRevision> _profiles = const [];
  late EgressProfileRevision _selected = widget.initial;
  bool _loading = true;
  bool _failed = false;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _failed = false;
    });
    try {
      final catalog = await widget.loadProfiles();
      if (!mounted) return;
      final profiles =
          <String, EgressProfileRevision>{
            '${widget.initial.id}@${widget.initial.revision}': widget.initial,
            for (final profile in catalog.items)
              '${profile.id}@${profile.revision}': profile,
          }.values.toList(growable: false)..sort(
            (left, right) => left.displayName.compareTo(right.displayName),
          );
      setState(() {
        _profiles = profiles;
        _loading = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _failed = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    insetPadding: const EdgeInsets.all(24),
    titlePadding: const EdgeInsets.fromLTRB(18, 16, 18, 8),
    contentPadding: const EdgeInsets.fromLTRB(18, 0, 18, 4),
    actionsPadding: const EdgeInsets.fromLTRB(12, 6, 12, 12),
    title: Text(copy('environment.egress.profile.title')),
    content: SizedBox(
      key: Key('environment-egress-profile-dialog-${widget.planId}'),
      width: 520,
      child: _loading
          ? const Padding(
              padding: EdgeInsets.symmetric(vertical: 28),
              child: Center(child: CircularProgressIndicator()),
            )
          : _failed
          ? Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                InlineNotice(
                  message: copy('environment.egress.profile.load_failed'),
                  error: true,
                ),
                const SizedBox(height: 8),
                OutlinedButton.icon(
                  onPressed: _load,
                  icon: const Icon(Icons.refresh, size: 15),
                  label: Text(copy('common.retry')),
                ),
              ],
            )
          : ConstrainedBox(
              constraints: const BoxConstraints(maxHeight: 420),
              child: ListView.separated(
                shrinkWrap: true,
                itemCount: _profiles.length,
                separatorBuilder: (_, _) => const Divider(height: 1),
                itemBuilder: (context, index) {
                  final profile = _profiles[index];
                  final selected =
                      profile.id == _selected.id &&
                      profile.revision == _selected.revision;
                  return ListTile(
                    key: Key(
                      'environment-egress-profile-${profile.id}-${profile.revision}',
                    ),
                    onTap: () => setState(() => _selected = profile),
                    leading: Icon(
                      selected
                          ? Icons.radio_button_checked
                          : Icons.radio_button_unchecked,
                      size: 18,
                      color: selected
                          ? context.viberColors.route
                          : context.viberColors.textFaint,
                    ),
                    title: Text(
                      egressProfileSummary(copy, profile),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                    ),
                    subtitle: profile.id == EgressProfileRevision.direct.id
                        ? null
                        : Text(
                            egressPolicySummary(copy, profile.policy),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                    contentPadding: EdgeInsets.zero,
                  );
                },
              ),
            ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.of(context).pop(),
        child: Text(copy('common.cancel')),
      ),
      FilledButton(
        key: const Key('environment-egress-profile-save'),
        onPressed: _loading || _failed
            ? null
            : () => Navigator.of(context).pop(_selected),
        child: Text(copy('common.save')),
      ),
    ],
  );
}

final class EgressProfileDraft {
  const EgressProfileDraft({required this.displayName, required this.policy});

  final String displayName;
  final TrafficEgressPolicy policy;
}

Future<EgressProfileDraft?> showEgressProfileEditor({
  required BuildContext context,
  required AppCopy copy,
  EgressProfileRevision? initial,
}) => showDialog<EgressProfileDraft>(
  context: context,
  barrierDismissible: true,
  builder: (context) =>
      _EgressProfileEditorDialog(copy: copy, initial: initial),
);

final class _DoHPreset {
  const _DoHPreset(this.id, this.copyKey, this.url);

  final String id;
  final String copyKey;
  final String url;
}

const _customDoH = 'custom';
const _doHPresets = [
  _DoHPreset(
    'cloudflare',
    'environment.egress.doh.preset.cloudflare',
    'https://1.1.1.1/dns-query',
  ),
  _DoHPreset(
    'google',
    'environment.egress.doh.preset.google',
    'https://8.8.8.8/dns-query',
  ),
  _DoHPreset(
    'quad9',
    'environment.egress.doh.preset.quad9',
    'https://9.9.9.9/dns-query',
  ),
];

final class _EgressProfileEditorDialog extends StatefulWidget {
  const _EgressProfileEditorDialog({required this.copy, this.initial});

  final AppCopy copy;
  final EgressProfileRevision? initial;

  @override
  State<_EgressProfileEditorDialog> createState() =>
      _EgressProfileEditorDialogState();
}

final class _EgressProfileEditorDialogState
    extends State<_EgressProfileEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _displayName;
  late final TextEditingController _proxyEndpoint;
  late final TextEditingController _dohUrl;
  late String _proxyKind;
  late String _resolverKind;
  late String _resolverTransport;
  late String _dohPreset;
  String? _error;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    final policy = widget.initial?.policy ?? const TrafficEgressPolicy.direct();
    _displayName = TextEditingController(text: widget.initial?.displayName);
    _proxyKind = policy.proxy.kind;
    _proxyEndpoint = TextEditingController(text: policy.proxy.endpoint);
    _resolverKind = policy.resolver.kind;
    _resolverTransport = policy.resolver.transport;
    _dohUrl = TextEditingController(text: policy.resolver.dohUrl);
    _dohPreset = _presetFor(_dohUrl.text);
  }

  @override
  void dispose() {
    _displayName.dispose();
    _proxyEndpoint.dispose();
    _dohUrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final maxHeight = math.min(650.0, MediaQuery.sizeOf(context).height - 48);
    return AlertDialog(
      insetPadding: const EdgeInsets.all(24),
      titlePadding: const EdgeInsets.fromLTRB(18, 16, 18, 8),
      contentPadding: const EdgeInsets.fromLTRB(18, 0, 18, 4),
      actionsPadding: const EdgeInsets.fromLTRB(12, 6, 12, 12),
      title: Text(
        copy(
          widget.initial == null
              ? 'settings.egress.create'
              : 'settings.egress.edit',
        ),
      ),
      content: SizedBox(
        key: const Key('settings-egress-profile-dialog'),
        width: 520,
        child: ConstrainedBox(
          constraints: BoxConstraints(maxHeight: maxHeight),
          child: Form(
            key: _formKey,
            child: SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  CompactLabeledControl(
                    label: copy('settings.egress.name'),
                    child: TextFormField(
                      key: const Key('egress-profile-display-name'),
                      controller: _displayName,
                      autofocus: true,
                      validator: (value) =>
                          value == null ||
                              value.trim().isEmpty ||
                              value.trim().length > 96
                          ? copy('settings.egress.validation.name')
                          : null,
                    ),
                  ),
                  const SizedBox(height: 9),
                  CompactLabeledControl(
                    label: copy('environment.egress.proxy.label'),
                    child: CompactSelectField<String>(
                      key: const Key('egress-profile-proxy-kind'),
                      initialValue: _proxyKind,
                      isExpanded: true,
                      items: [
                        for (final kind in const ['direct', 'socks5'])
                          DropdownMenuItem(
                            key: Key('egress-profile-proxy-$kind'),
                            value: kind,
                            child: Text(
                              copy('environment.egress.proxy.$kind'),
                              overflow: TextOverflow.ellipsis,
                            ),
                          ),
                      ],
                      onChanged: (value) => _setProxy(value ?? 'direct'),
                    ),
                  ),
                  if (_proxyKind == 'socks5') ...[
                    const SizedBox(height: 9),
                    CompactLabeledControl(
                      label: copy('environment.egress.proxy.endpoint'),
                      child: TextFormField(
                        key: const Key('egress-profile-proxy-endpoint'),
                        controller: _proxyEndpoint,
                        autocorrect: false,
                        enableSuggestions: false,
                        decoration: InputDecoration(
                          hintText: copy(
                            'environment.egress.proxy.endpoint_hint',
                          ),
                        ),
                        validator: _validateProxy,
                      ),
                    ),
                  ],
                  const SizedBox(height: 9),
                  CompactLabeledControl(
                    label: copy('environment.egress.resolver.label'),
                    child: CompactSelectField<String>(
                      key: const Key('egress-profile-resolver-kind'),
                      initialValue: _resolverKind,
                      isExpanded: true,
                      items: [
                        for (final kind in const ['system', 'doh'])
                          DropdownMenuItem(
                            key: Key('egress-profile-resolver-$kind'),
                            value: kind,
                            child: Text(
                              copy('environment.egress.resolver.$kind'),
                              overflow: TextOverflow.ellipsis,
                            ),
                          ),
                      ],
                      onChanged: (value) => _setResolver(value ?? 'system'),
                    ),
                  ),
                  if (_resolverKind == 'doh') ...[
                    const SizedBox(height: 9),
                    CompactLabeledControl(
                      label: copy('environment.egress.doh.service'),
                      child: CompactSelectField<String>(
                        key: const Key('egress-profile-doh-preset'),
                        initialValue: _dohPreset,
                        isExpanded: true,
                        items: [
                          for (final preset in _doHPresets)
                            DropdownMenuItem(
                              key: Key('egress-profile-doh-${preset.id}'),
                              value: preset.id,
                              child: Text(copy(preset.copyKey)),
                            ),
                          DropdownMenuItem(
                            key: const Key('egress-profile-doh-custom'),
                            value: _customDoH,
                            child: Text(
                              copy('environment.egress.doh.preset.custom'),
                            ),
                          ),
                        ],
                        onChanged: (value) => _setPreset(value ?? _customDoH),
                      ),
                    ),
                    const SizedBox(height: 9),
                    if (_dohPreset == _customDoH)
                      CompactLabeledControl(
                        label: copy('environment.egress.doh.url'),
                        detail: copy('environment.egress.doh.url_detail'),
                        child: TextFormField(
                          key: const Key('egress-profile-doh-url'),
                          controller: _dohUrl,
                          autocorrect: false,
                          enableSuggestions: false,
                          validator: _validateDoH,
                        ),
                      )
                    else
                      CompactLabeledControl(
                        label: copy('environment.egress.doh.endpoint'),
                        child: _ReadOnlyValue(text: _dohUrl.text),
                      ),
                    if (_proxyKind == 'socks5') ...[
                      const SizedBox(height: 9),
                      CompactLabeledControl(
                        label: copy('environment.egress.doh.transport'),
                        child: CompactSelectField<String>(
                          key: const Key('egress-profile-doh-transport'),
                          initialValue: _resolverTransport,
                          isExpanded: true,
                          items: [
                            for (final transport in const ['direct', 'proxy'])
                              DropdownMenuItem(
                                key: Key(
                                  transport == 'proxy'
                                      ? 'egress-profile-doh-via-proxy'
                                      : 'egress-profile-doh-direct',
                                ),
                                value: transport,
                                child: Text(
                                  copy(
                                    'environment.egress.doh.transport.$transport',
                                  ),
                                ),
                              ),
                          ],
                          onChanged: (value) => setState(
                            () => _resolverTransport = value ?? 'direct',
                          ),
                        ),
                      ),
                    ],
                  ],
                  if (_error case final error?) ...[
                    const SizedBox(height: 10),
                    InlineNotice(message: error, error: true),
                  ],
                ],
              ),
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('egress-profile-publish'),
          onPressed: _save,
          child: Text(copy('settings.egress.publish')),
        ),
      ],
    );
  }

  void _setProxy(String value) => setState(() {
    _proxyKind = value;
    _error = null;
    if (value == 'direct') _resolverTransport = 'direct';
  });

  void _setResolver(String value) => setState(() {
    _resolverKind = value;
    _error = null;
    if (value == 'system' || _proxyKind == 'direct') {
      _resolverTransport = 'direct';
    }
  });

  void _setPreset(String value) => setState(() {
    _dohPreset = value;
    _error = null;
    for (final preset in _doHPresets) {
      if (preset.id == value) _dohUrl.text = preset.url;
    }
  });

  String? _validateProxy(String? value) {
    if (_proxyKind == 'direct') return null;
    try {
      TrafficProxyPolicy.fromJson({
        'kind': _proxyKind,
        'endpoint': value?.trim() ?? '',
      }, 'proxy');
      return null;
    } on ControlContractException {
      return copy('environment.egress.validation.proxy');
    }
  }

  String? _validateDoH(String? value) {
    if (_resolverKind != 'doh') return null;
    try {
      TrafficResolverPolicy.fromJson({
        'kind': 'doh',
        'dohUrl': value?.trim() ?? '',
        'transport': _proxyKind == 'socks5' ? _resolverTransport : 'direct',
      }, 'resolver');
      return null;
    } on ControlContractException {
      return copy('environment.egress.validation.doh');
    }
  }

  void _save() {
    setState(() => _error = null);
    if (!_formKey.currentState!.validate()) return;
    try {
      final policy = TrafficEgressPolicy.fromJson({
        'proxy': {
          'kind': _proxyKind,
          if (_proxyKind == 'socks5') 'endpoint': _proxyEndpoint.text.trim(),
        },
        'resolver': {
          'kind': _resolverKind,
          'transport': _resolverKind == 'doh' && _proxyKind == 'socks5'
              ? _resolverTransport
              : 'direct',
          if (_resolverKind == 'doh') 'dohUrl': _dohUrl.text.trim(),
        },
      }, 'egressProfile.policy');
      Navigator.of(context).pop(
        EgressProfileDraft(
          displayName: _displayName.text.trim(),
          policy: policy,
        ),
      );
    } on ControlContractException {
      setState(() => _error = copy('environment.egress.validation.policy'));
    }
  }

  String _presetFor(String url) {
    for (final preset in _doHPresets) {
      if (preset.url == url) return preset.id;
    }
    return _customDoH;
  }
}

final class _ReadOnlyValue extends StatelessWidget {
  const _ReadOnlyValue({required this.text});

  final String text;

  @override
  Widget build(BuildContext context) => Container(
    height: ViberMetrics.controlHeight,
    alignment: Alignment.centerLeft,
    padding: const EdgeInsets.symmetric(horizontal: 10),
    decoration: BoxDecoration(
      color: context.viberColors.panelRaised,
      border: Border.all(color: context.viberColors.divider),
      borderRadius: ViberMetrics.controlRadius,
    ),
    child: Text(text, maxLines: 1, overflow: TextOverflow.ellipsis),
  );
}

import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'vless_tls_fields.dart';

typedef EgressProfileLoader = Future<EgressProfileCatalog> Function();
typedef EgressDefaultLoader = Future<EgressDefaultSetting> Function();

/// Selects one exact, published network-exit revision for a protocol path.
final class EgressProfileButton extends StatelessWidget {
  const EgressProfileButton({
    required this.plan,
    required this.copy,
    required this.enabled,
    required this.loadProfiles,
    required this.onChanged,
    this.loadDefault,
    required this.selectionScope,
    super.key,
  });

  final EnvironmentProtocolPlan plan;
  final AppCopy copy;
  final bool enabled;
  final EgressProfileLoader loadProfiles;
  final EgressDefaultLoader? loadDefault;
  final Object selectionScope;
  final ValueChanged<EgressChoice> onChanged;

  @override
  Widget build(BuildContext context) => Container(
    color: context.viberColors.panelRaised.withValues(alpha: 0.34),
    padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
    child: CompactLabeledControl(
      label: copy('environment.egress.label'),
      help: copy('environment.egress.profile.detail'),
      dismissHelpLabel: copy('common.dismiss'),
      child: SizedBox(
        width: double.infinity,
        child: OutlinedButton.icon(
          key: Key('environment-egress-${plan.id}'),
          onPressed: enabled ? () => unawaited(_edit(context)) : null,
          icon: const Icon(Icons.alt_route_rounded, size: 14),
          label: Align(
            alignment: Alignment.centerLeft,
            child: Text(egressProfileSummary(copy, plan.egressProfile)),
          ),
          style: OutlinedButton.styleFrom(
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 8),
          ),
        ),
      ),
    ),
  );

  Future<void> _edit(BuildContext context) async {
    final selection = await showEgressProfileSelection(
      context: context,
      selectionId: plan.id,
      initial: plan.egressProfile is EgressProfileRevision
          ? plan.egressProfile as EgressProfileRevision
          : null,
      copy: copy,
      loadProfiles: loadProfiles,
      allowInheritance: true,
      loadDefault: loadDefault,
      flowInheritance: true,
    );
    if (!context.mounted) return;
    final current = context.widget;
    if (current is! EgressProfileButton ||
        !current.enabled ||
        !identical(current.selectionScope, selectionScope) ||
        current.plan.id != plan.id ||
        current.plan.revision != plan.revision ||
        current.plan.egressProfile != plan.egressProfile) {
      return;
    }
    if (selection != null) {
      current.onChanged(selection.profile ?? EgressChoice.inherit);
    }
  }
}

/// A null result is cancellation; a selected null profile means inheritance.
Future<({EgressProfileRevision? profile})?> showEgressProfileSelection({
  required BuildContext context,
  required String selectionId,
  required EgressProfileRevision? initial,
  required AppCopy copy,
  required EgressProfileLoader loadProfiles,
  bool allowInheritance = false,
  EgressDefaultLoader? loadDefault,
  bool flowInheritance = false,
}) => showDialog<({EgressProfileRevision? profile})>(
  context: context,
  builder: (_) => _EgressProfileSelectionDialog(
    planId: selectionId,
    initial: initial,
    copy: copy,
    loadProfiles: loadProfiles,
    allowInheritance: allowInheritance,
    loadDefault: loadDefault,
    flowInheritance: flowInheritance,
  ),
);

String egressProfileSummary(AppCopy copy, EgressChoice choice) {
  if (choice is EgressInheritance) {
    return copy('environment.egress.use_global');
  }
  final profile = choice as EgressProfileRevision;
  if (profile.id == EgressProfileRevision.direct.id) {
    return egressPolicySummary(copy, profile.policy);
  }
  return profile.policy.proxy.isVless
      ? '${profile.displayName} · VLESS'
      : '${profile.displayName} · r${profile.revision}';
}

String egressPolicySummary(AppCopy copy, TrafficEgressPolicy policy) {
  final proxy = switch (policy.proxy.kind) {
    'direct' => copy('environment.egress.proxy.direct'),
    'socks5' => 'SOCKS5 · ${policy.proxy.endpoint}',
    'vless' => 'VLESS',
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
    required this.allowInheritance,
    required this.loadDefault,
    required this.flowInheritance,
  }) : assert(initial != null || allowInheritance);

  final String planId;
  final EgressProfileRevision? initial;
  final AppCopy copy;
  final EgressProfileLoader loadProfiles;
  final bool allowInheritance;
  final EgressDefaultLoader? loadDefault;
  final bool flowInheritance;

  @override
  State<_EgressProfileSelectionDialog> createState() =>
      _EgressProfileSelectionDialogState();
}

final class _EgressProfileSelectionDialogState
    extends State<_EgressProfileSelectionDialog> {
  List<EgressProfileRevision> _profiles = const [];
  late EgressProfileRevision? _selected = widget.initial;
  bool _loading = true;
  bool _failed = false;
  EgressDefaultSetting? _default;
  bool _defaultLoading = false;

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
    unawaited(_loadDefault());
  }

  Future<void> _loadDefault() async {
    final loader = widget.loadDefault;
    if (loader == null) return;
    setState(() => _defaultLoading = true);
    EgressDefaultSetting? observed;
    try {
      observed = await loader();
    } on Object {
      observed = null;
    }
    if (!mounted) return;
    setState(() {
      _default = observed;
      _defaultLoading = false;
    });
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
            for (final profile in [?widget.initial, ...catalog.items])
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
          ? Padding(
              padding: const EdgeInsets.symmetric(vertical: 28),
              child: CompactLoadingMessage(label: copy('common.loading')),
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
          : Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (widget.loadDefault != null)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 8),
                    child: Text(
                      key: Key(
                        _default == null
                            ? 'egress-selection-default-unavailable'
                            : 'egress-selection-default-preview',
                      ),
                      _defaultLoading
                          ? copy('common.loading')
                          : _default == null
                          ? copy('settings.egress.default_unavailable')
                          : '${copy('settings.egress.current')}: ${egressProfileSummary(copy, _default!.effectiveProfile)}',
                    ),
                  ),
                Flexible(
                  child: ConstrainedBox(
                    constraints: const BoxConstraints(maxHeight: 420),
                    child: ListView.separated(
                      shrinkWrap: true,
                      itemCount:
                          _profiles.length + (widget.allowInheritance ? 1 : 0),
                      separatorBuilder: (_, _) => const Divider(height: 1),
                      itemBuilder: (context, index) {
                        final profile = widget.allowInheritance && index == 0
                            ? null
                            : _profiles[index -
                                  (widget.allowInheritance ? 1 : 0)];
                        final selected =
                            profile?.id == _selected?.id &&
                            profile?.revision == _selected?.revision;
                        return ListTile(
                          key: Key(
                            profile == null
                                ? 'account-egress-inherit'
                                : 'environment-egress-profile-${profile.id}-${profile.revision}',
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
                            profile == null
                                ? copy(
                                    widget.flowInheritance
                                        ? 'environment.egress.use_global'
                                        : 'provider_accounts.egress.inherit',
                                  )
                                : egressProfileSummary(copy, profile),
                          ),
                          subtitle: profile == null
                              ? Text(
                                  copy(
                                    widget.flowInheritance
                                        ? 'settings.egress.default_detail'
                                        : 'provider_accounts.egress.inherit.detail',
                                  ),
                                )
                              : profile.id == EgressProfileRevision.direct.id
                              ? null
                              : Text(egressPolicySummary(copy, profile.policy)),
                          contentPadding: EdgeInsets.zero,
                        );
                      },
                    ),
                  ),
                ),
              ],
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
            : () => Navigator.of(context).pop((profile: _selected)),
        child: Text(copy('common.save')),
      ),
    ],
  );
}

sealed class EgressProfileEditIntent {
  const EgressProfileEditIntent();
  String get displayName;
}

final class EgressProfileDraft extends EgressProfileEditIntent {
  const EgressProfileDraft({required this.displayName, required this.policy});

  @override
  final String displayName;
  final TrafficEgressPolicy policy;
}

final class VlessEgressProfileDraft extends EgressProfileEditIntent {
  const VlessEgressProfileDraft({
    required this.displayName,
    required this.import,
  });
  @override
  final String displayName;
  final VlessImportIntent import;
}

/// A dialog-only private mutation intent. It has no public serialization.
final class VlessImportIntent {
  const VlessImportIntent({
    required this.uri,
    required this.resolver,
    required this.directDomains,
    this.tls,
  });
  final String uri;
  final TrafficResolverPolicy resolver;
  final String directDomains;
  final VlessTLSImportOptions? tls;

  @override
  String toString() => 'VlessImportIntent(redacted)';
}

Future<EgressProfileEditIntent?> showEgressProfileEditor({
  required BuildContext context,
  required AppCopy copy,
  EgressProfileRevision? initial,
  Future<VlessProfileDescription> Function()? loadVlessDescription,
}) => showDialog<EgressProfileEditIntent>(
  context: context,
  barrierDismissible: true,
  builder: (context) => _EgressProfileEditorDialog(
    copy: copy,
    initial: initial,
    loadVlessDescription: loadVlessDescription,
  ),
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
  const _EgressProfileEditorDialog({
    required this.copy,
    this.initial,
    this.loadVlessDescription,
  });

  final AppCopy copy;
  final EgressProfileRevision? initial;
  final Future<VlessProfileDescription> Function()? loadVlessDescription;

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
  late final TextEditingController _nodeLink;
  late final TextEditingController _directDomains;
  late String _proxyKind;
  late String _resolverKind;
  late String _resolverTransport;
  late String _dohPreset;
  String? _error;
  VlessProfileDescription? _description;
  bool _descriptionLoading = false;
  bool _descriptionFailed = false;
  int _descriptionGeneration = 0;
  int _nodeContext = 0;
  VlessTLSImportOptions? _tls;
  bool _tlsValid = true;

  bool get _usesSavedNode =>
      _nodeLink.text.trim().isEmpty &&
      widget.initial?.policy.proxy.isVless == true;
  bool get _showsTLS {
    if (_proxyKind != 'vless') return false;
    if (_usesSavedNode) return _description?.security == 'tls';
    try {
      final uri = Uri.tryParse(_nodeLink.text.trim());
      return uri?.scheme == 'vless' &&
          uri?.queryParametersAll['security']?.singleOrNull == 'tls';
    } on FormatException {
      return false;
    }
  }

  bool get _savedStateUnavailable =>
      widget.initial?.policy.proxy.isVless == true &&
      (_descriptionLoading || _descriptionFailed);

  Future<void> _loadDescription() async {
    final generation = ++_descriptionGeneration;
    setState(() {
      _descriptionLoading = true;
      _descriptionFailed = false;
    });
    try {
      final loader = widget.loadVlessDescription;
      if (loader == null) throw UnsupportedError('Saved settings unavailable');
      final description = await loader();
      if (!mounted || generation != _descriptionGeneration) return;
      setState(() {
        _description = description;
        _descriptionLoading = false;
        if (_usesSavedNode) _clearTLS();
      });
    } on Object {
      if (!mounted || generation != _descriptionGeneration) return;
      setState(() {
        _descriptionLoading = false;
        _descriptionFailed = true;
      });
    }
  }

  void _clearTLS() {
    _nodeContext++;
    _tls = null;
    _tlsValid = true;
  }

  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    final policy = widget.initial?.policy ?? const TrafficEgressPolicy.direct();
    _displayName = TextEditingController(text: widget.initial?.displayName);
    _proxyKind = policy.proxy.kind;
    _proxyEndpoint = TextEditingController(text: policy.proxy.endpoint);
    _nodeLink = TextEditingController();
    _directDomains = TextEditingController(text: policy.directDomains);
    _resolverKind = policy.resolver.kind;
    _resolverTransport = policy.resolver.transport;
    _dohUrl = TextEditingController(text: policy.resolver.dohUrl);
    _dohPreset = _presetFor(_dohUrl.text);
    if (policy.proxy.isVless) unawaited(_loadDescription());
  }

  @override
  void dispose() {
    _descriptionGeneration++;
    _tls = null;
    _displayName.dispose();
    _proxyEndpoint.dispose();
    _dohUrl.dispose();
    _nodeLink.clear();
    _nodeLink.dispose();
    _directDomains.dispose();
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
                        for (final kind in const ['direct', 'socks5', 'vless'])
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
                  if (_proxyKind == 'vless') ...[
                    const SizedBox(height: 9),
                    CompactLabeledControl(
                      label: copy('environment.egress.vless.link'),
                      detail: copy(
                        widget.initial?.policy.proxy.isVless == true
                            ? 'environment.egress.vless.saved'
                            : 'environment.egress.vless.supported',
                      ),
                      child: TextFormField(
                        key: const Key('egress-profile-vless-uri'),
                        controller: _nodeLink,
                        obscureText: true,
                        autocorrect: false,
                        enableSuggestions: false,
                        onChanged: (_) => setState(_clearTLS),
                        validator: (value) =>
                            (value == null || value.trim().isEmpty) &&
                                widget.initial?.policy.proxy.isVless != true
                            ? copy('environment.egress.validation.vless')
                            : null,
                      ),
                    ),
                    if (_descriptionLoading)
                      CompactLoadingMessage(label: copy('common.loading')),
                    if (_descriptionFailed) ...[
                      InlineNotice(
                        message: copy('environment.egress.tls.load_failed'),
                        error: true,
                      ),
                      OutlinedButton.icon(
                        key: const Key('vless-settings-retry'),
                        onPressed: _loadDescription,
                        icon: const Icon(Icons.refresh, size: 15),
                        label: Text(copy('common.retry')),
                      ),
                    ],
                    if (_showsTLS) ...[
                      const SizedBox(height: 9),
                      VlessTLSFields(
                        key: ValueKey(_nodeContext),
                        copy: copy,
                        savedMode: _usesSavedNode
                            ? _description?.trustMode
                            : null,
                        onChanged: (tls, valid) => setState(() {
                          _tls = tls;
                          _tlsValid = valid;
                        }),
                      ),
                    ],
                  ],
                  const SizedBox(height: 9),
                  CompactLabeledControl(
                    label: copy('environment.egress.direct_domains'),
                    help: copy('environment.egress.direct_domains.detail'),
                    dismissHelpLabel: copy('common.dismiss'),
                    child: TextFormField(
                      key: const Key('egress-profile-direct-domains'),
                      controller: _directDomains,
                      minLines: 2,
                      maxLines: 4,
                      autocorrect: false,
                      enableSuggestions: false,
                    ),
                  ),
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
                    if (_proxyKind != 'direct') ...[
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
                                    transport == 'proxy' &&
                                            _proxyKind == 'vless'
                                        ? 'environment.egress.doh.transport.vless'
                                        : 'environment.egress.doh.transport.$transport',
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
          onPressed: _savedStateUnavailable || !_tlsValid ? null : _save,
          child: Text(copy('common.save')),
        ),
      ],
    );
  }

  void _setProxy(String value) => setState(() {
    _clearTLS();
    _proxyKind = value;
    if (value != 'vless') _nodeLink.clear();
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
        'transport': _proxyKind != 'direct' ? _resolverTransport : 'direct',
      }, 'resolver');
      return null;
    } on ControlContractException {
      return copy('environment.egress.validation.doh');
    }
  }

  void _save() {
    if (_savedStateUnavailable || !_tlsValid) return;
    setState(() => _error = null);
    if (!_formKey.currentState!.validate()) return;
    try {
      final resolver = TrafficResolverPolicy.fromJson({
        'kind': _resolverKind,
        'transport': _resolverKind == 'doh' && _proxyKind != 'direct'
            ? _resolverTransport
            : 'direct',
        if (_resolverKind == 'doh') 'dohUrl': _dohUrl.text.trim(),
      }, 'resolver');
      if (_proxyKind == 'vless' &&
          (_nodeLink.text.trim().isNotEmpty || _tls != null)) {
        final intent = VlessImportIntent(
          uri: _nodeLink.text.trim(),
          resolver: resolver,
          directDomains: _directDomains.text,
          tls: _showsTLS ? _tls : null,
        );
        _nodeLink.clear();
        Navigator.of(context).pop(
          VlessEgressProfileDraft(
            displayName: _displayName.text.trim(),
            import: intent,
          ),
        );
        return;
      }
      final policy = TrafficEgressPolicy.fromJson({
        'proxy': _proxyKind == 'vless'
            ? widget.initial!.policy.proxy.toJson()
            : {
                'kind': _proxyKind,
                if (_proxyKind == 'socks5')
                  'endpoint': _proxyEndpoint.text.trim(),
              },
        'resolver': resolver.toJson(),
        'directDomains': _directDomains.text,
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

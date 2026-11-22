import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/api/runtime_storage.dart';
import '../../core/bootstrap/terminal_command.dart';
import '../../core/bootstrap/transparent_capture.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import '../../core/api/product_update_api.dart';
import 'acp_setup.dart';
import 'runtime_root_ca_panel.dart';
import '../../core/api/control_api.dart' show ControlProblem;
import '../../core/api/control_models.dart';
import 'deletion_dialog.dart';
import 'egress_profile_editor.dart';
import 'launch_command.dart';
import 'setup_guide.dart' show SetupFactsWatcher, setupRootTrusted;
import 'runtime_connection_guide.dart';
import 'server_deployment_guide.dart';
import 'network_exit_usage.dart';
import 'workbench_controller.dart';
import 'routing_set_name.dart';

final class SettingsView extends StatelessWidget {
  const SettingsView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final destinations = controller.settingsDestinations;
    return LayoutBuilder(
      builder: (context, viewport) {
        final wide = viewport.maxWidth >= 1000;
        final panes = TabBarView(
          physics: wide ? const NeverScrollableScrollPhysics() : null,
          children: [
            for (final destination in destinations)
              switch (destination) {
                SettingsDestination.preferences => _GeneralSettingsPane(
                  controller: controller,
                  copy: copy,
                ),
                SettingsDestination.access => _AccessSettingsPane(
                  controller: controller,
                  copy: copy,
                ),
                SettingsDestination.users => _UsersSettingsPane(
                  controller: controller,
                  copy: copy,
                ),
                SettingsDestination.safety => _SafetyDataSettingsPane(
                  controller: controller,
                  copy: copy,
                ),
              },
          ],
        );
        return DefaultTabController(
          key: ValueKey('settings-tab-${controller.settingsTab}'),
          length: destinations.length,
          initialIndex: controller.settingsTab,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              PageHeading(
                title: copy('settings.title'),
                help: copy(
                  controller.serverManagement
                      ? 'settings.subtitle.server'
                      : 'settings.subtitle',
                ),
                dismissHelpLabel: copy('common.dismiss'),
              ),
              if (!wide)
                Material(
                  color: context.viberColors.panel,
                  child: LayoutBuilder(
                    builder: (context, constraints) =>
                        constraints.maxWidth < 600
                        ? Padding(
                            padding: const EdgeInsets.symmetric(horizontal: 12),
                            child: Semantics(
                              label: copy('settings.section'),
                              child: DropdownButtonHideUnderline(
                                child: DropdownButton<SettingsDestination>(
                                  key: const Key('settings-section-picker'),
                                  isExpanded: true,
                                  value: destinations[controller.settingsTab],
                                  items: [
                                    for (final destination in destinations)
                                      DropdownMenuItem(
                                        key: Key(
                                          'settings-option-${destination.name}',
                                        ),
                                        value: destination,
                                        child: Text(
                                          _settingsDestinationLabel(
                                            copy,
                                            destination,
                                          ),
                                        ),
                                      ),
                                  ],
                                  onChanged: (destination) {
                                    if (destination != null) {
                                      controller.selectSettingsTab(
                                        destinations.indexOf(destination),
                                      );
                                    }
                                  },
                                ),
                              ),
                            ),
                          )
                        : Align(
                            alignment: Alignment.centerLeft,
                            child: TabBar(
                              isScrollable: true,
                              tabAlignment: TabAlignment.start,
                              dividerHeight: 0,
                              onTap: controller.selectSettingsTab,
                              tabs: <Widget>[
                                for (final destination in destinations)
                                  switch (destination) {
                                    SettingsDestination.preferences =>
                                      _SettingsTab(
                                        key: const Key('settings-tab-general'),
                                        icon: Icons.tune,
                                        label: copy('settings.tab.preferences'),
                                      ),
                                    SettingsDestination.access => _SettingsTab(
                                      key: const Key('settings-tab-access'),
                                      icon: Icons.link,
                                      label: copy('settings.tab.access'),
                                    ),
                                    SettingsDestination.users => _SettingsTab(
                                      key: const Key('settings-tab-users'),
                                      icon: Icons.group_outlined,
                                      label: copy('settings.tab.users'),
                                    ),
                                    SettingsDestination.safety => _SettingsTab(
                                      key: const Key('settings-tab-safety'),
                                      icon: Icons.shield_outlined,
                                      label: copy('settings.tab.safety'),
                                    ),
                                  },
                              ],
                            ),
                          ),
                  ),
                ),
              const Divider(height: 1),
              Expanded(
                child: wide
                    ? Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          SizedBox(
                            width: 264,
                            child: _SettingsSectionList(
                              destinations: destinations,
                              selected: controller.settingsTab,
                              copy: copy,
                              onSelect: controller.selectSettingsTab,
                            ),
                          ),
                          Expanded(child: panes),
                        ],
                      )
                    : panes,
              ),
            ],
          ),
        );
      },
    );
  }
}

/// Settings sections as a list beside the content on wide windows, each with
/// one line saying what it holds.
final class _SettingsSectionList extends StatelessWidget {
  const _SettingsSectionList({
    required this.destinations,
    required this.selected,
    required this.copy,
    required this.onSelect,
  });

  final List<SettingsDestination> destinations;
  final int selected;
  final AppCopy copy;
  final ValueChanged<int> onSelect;

  @override
  Widget build(BuildContext context) {
    final colors = context.viberColors;
    final theme = Theme.of(context).textTheme;
    return ListView(
      padding: const EdgeInsets.fromLTRB(12, 14, 8, 14),
      children: [
        for (final (index, destination) in destinations.indexed)
          Padding(
            padding: const EdgeInsets.only(bottom: 2),
            child: Semantics(
              button: true,
              selected: index == selected,
              child: Material(
                color: index == selected
                    ? colors.panelRaised
                    : Colors.transparent,
                borderRadius: ViberMetrics.controlRadius,
                child: InkWell(
                  key: Key('settings-tab-${_settingsTabKey(destination)}'),
                  borderRadius: ViberMetrics.controlRadius,
                  onTap: () => onSelect(index),
                  child: Padding(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 10,
                      vertical: 8,
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          _settingsDestinationLabel(copy, destination),
                          style: theme.bodyMedium?.copyWith(
                            fontWeight: index == selected
                                ? FontWeight.w600
                                : FontWeight.w500,
                          ),
                        ),
                        Text(
                          copy(
                            'settings.section_detail.${_settingsTabKey(destination)}',
                          ),
                          style: theme.bodySmall?.copyWith(
                            color: colors.textMuted,
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
          ),
      ],
    );
  }
}

String _settingsTabKey(SettingsDestination destination) =>
    switch (destination) {
      SettingsDestination.preferences => 'general',
      SettingsDestination.access => 'access',
      SettingsDestination.users => 'users',
      SettingsDestination.safety => 'safety',
    };

String _settingsDestinationLabel(
  AppCopy copy,
  SettingsDestination destination,
) => copy(switch (destination) {
  SettingsDestination.preferences => 'settings.tab.preferences',
  SettingsDestination.access => 'settings.tab.access',
  SettingsDestination.users => 'settings.tab.users',
  SettingsDestination.safety => 'settings.tab.safety',
});

final class _SettingsTab extends StatelessWidget {
  const _SettingsTab({required this.icon, required this.label, super.key});

  final IconData icon;
  final String label;

  @override
  Widget build(BuildContext context) => Tab(
    height: ViberMetrics.toolbarHeight,
    child: Row(
      mainAxisSize: MainAxisSize.min,
      children: [Icon(icon, size: 15), const SizedBox(width: 6), Text(label)],
    ),
  );
}

final class _SettingsPaneLayout extends StatelessWidget {
  const _SettingsPaneLayout({required this.scrollKey, required this.children});

  final Key scrollKey;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) => ListView(
    key: scrollKey,
    padding: const EdgeInsets.fromLTRB(14, 14, 14, 24),
    children: [
      Align(
        alignment: Alignment.topLeft,
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 1080),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: children,
          ),
        ),
      ),
    ],
  );
}

final class _SettingsPaneHeader extends StatelessWidget {
  const _SettingsPaneHeader({
    required this.icon,
    required this.title,
    required this.detail,
    required this.dismissHelpLabel,
  });

  final IconData icon;
  final String title;
  final String detail;
  final String dismissHelpLabel;

  @override
  Widget build(BuildContext context) => ContextHelpHeading(
    title: title,
    message: detail,
    dismissLabel: dismissHelpLabel,
    style: Theme.of(
      context,
    ).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w600),
  );
}

final class _SettingsSurface extends StatelessWidget {
  const _SettingsSurface({required this.child, this.accent = false, super.key});

  final Widget child;
  // Kept for callers; sections no longer draw an accent stripe.
  final bool accent;

  @override
  Widget build(BuildContext context) => DecoratedBox(
    decoration: BoxDecoration(
      border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
    ),
    child: Padding(
      padding: const EdgeInsets.symmetric(vertical: 14),
      child: child,
    ),
  );
}

/// Network exits as their own destination: how traffic leaves after an
/// account is chosen. Routing and accounts point at a saved revision.
final class NetworkExitsView extends StatelessWidget {
  const NetworkExitsView({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) =>
      NetworkExitsPane(controller: controller, copy: copy);
}

final class NetworkExitsPane extends StatefulWidget {
  const NetworkExitsPane({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<NetworkExitsPane> createState() => _NetworkExitsPaneState();
}

final class _NetworkExitsPaneState extends State<NetworkExitsPane> {
  List<EgressProfileRevision> _profiles = const [];
  EgressDefaultSetting? _default;
  bool _loading = true;
  bool _saving = false;
  bool _failed = false;
  bool _unconfirmed = false;
  String? _profileMutationError;
  int _generation = 0;
  int _runtimeGeneration = 0;

  AppCopy get copy => widget.copy;
  bool get _owner => widget.controller.webPrincipal?.owner ?? true;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  @override
  void didUpdateWidget(covariant NetworkExitsPane oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (!identical(oldWidget.controller, widget.controller)) {
      _runtimeGeneration++;
      _saving = false;
      _unconfirmed = false;
      _profileMutationError = null;
      unawaited(_load());
    }
  }

  Future<void> _load() async {
    final generation = ++_generation;
    final controller = widget.controller;
    setState(() {
      _loading = true;
      _failed = false;
      _default = null;
      _profileMutationError = null;
    });
    try {
      final catalog = await controller.egressProfiles();
      EgressDefaultSetting? current;
      try {
        current = await controller.egressDefault();
      } on Object {
        current = null;
      }
      if (!mounted ||
          generation != _generation ||
          !identical(controller, widget.controller)) {
        return;
      }
      final profiles = [...catalog.items]
        ..sort((left, right) => left.displayName.compareTo(right.displayName));
      setState(() {
        _profiles = profiles;
        _default = current;
        _loading = false;
      });
    } on Object {
      if (!mounted ||
          generation != _generation ||
          !identical(controller, widget.controller)) {
        return;
      }
      setState(() {
        _loading = false;
        _failed = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      PageHeading(
        title: copy('nav.network_exits'),
        help: copy('network_exits.subtitle'),
        dismissHelpLabel: copy('common.dismiss'),
        trailing: _owner
            ? FilledButton.icon(
                key: const Key('egress-profile-add'),
                onPressed: _saving ? null : () => unawaited(_edit()),
                icon: const Icon(Icons.add, size: 15),
                label: Text(copy('settings.egress.add')),
              )
            : null,
      ),
      const Divider(height: 1),
      Expanded(
        child: _SettingsPaneLayout(
          scrollKey: const Key('egress-profiles-settings-scroll'),
          children: [
            if (_profileMutationError case final message?) ...[
              InlineNotice(
                key: const Key('egress-profile-mutation-error'),
                message: copy(message),
                error: true,
              ),
              Align(
                alignment: Alignment.centerLeft,
                child: OutlinedButton.icon(
                  onPressed: _load,
                  icon: const Icon(Icons.refresh, size: 15),
                  label: Text(copy('status.refresh')),
                ),
              ),
              const SizedBox(height: 12),
            ],
            if (_loading)
              Padding(
                padding: const EdgeInsets.all(28),
                child: CompactLoadingMessage(label: copy('common.loading')),
              )
            else if (_failed)
              Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  InlineNotice(
                    message: copy('settings.egress.load_failed'),
                    error: true,
                  ),
                  const SizedBox(height: 8),
                  Align(
                    alignment: Alignment.centerLeft,
                    child: OutlinedButton.icon(
                      onPressed: _load,
                      icon: const Icon(Icons.refresh, size: 15),
                      label: Text(copy('common.retry')),
                    ),
                  ),
                ],
              )
            else ...[
              _defaultBand(context),
              const SizedBox(height: 20),
              LayoutBuilder(
                builder: (context, constraints) => constraints.maxWidth >= 760
                    ? _profileTable(context)
                    : _profileList(context),
              ),
            ],
          ],
        ),
      ),
    ],
  );

  /// The exit used when neither the account nor the routing set names one.
  Widget _defaultBand(BuildContext context) {
    final colors = context.viberColors;
    final theme = Theme.of(context).textTheme;
    final muted = theme.bodySmall?.copyWith(color: colors.textMuted);
    final current = _default;
    final effective = current?.effectiveProfile;
    return Container(
      padding: const EdgeInsets.fromLTRB(20, 16, 20, 12),
      decoration: BoxDecoration(
        color: colors.panelRaised,
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(copy('network_exits.default'), style: muted),
          const SizedBox(height: 2),
          Text(
            key: const Key('egress-default-summary'),
            effective == null
                ? copy('settings.egress.default_unavailable')
                : effective.id == EgressProfileRevision.direct.id
                ? egressPolicySummary(copy, effective.policy)
                : effective.displayName,
            style: theme.titleLarge?.copyWith(fontWeight: FontWeight.w600),
          ),
          if (effective != null) ...[
            const SizedBox(height: 2),
            Text(
              key: const Key('egress-default-policy'),
              '${copy.format('settings.egress.revision', {'revision': effective.revision})} · ${egressPolicySummary(copy, effective.policy)}',
              style: muted,
            ),
            if (effective.policy.resolver.dohUrl case final endpoint?)
              Text(
                key: const Key('egress-default-doh-endpoint'),
                endpoint,
                style: muted,
              ),
          ],
          const SizedBox(height: 8),
          Text(copy('settings.egress.default_detail'), style: muted),
          Text(copy('settings.egress.remote_detail'), style: muted),
          if (current != null && current.profile == null)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                key: const Key('egress-default-unset'),
                copy('settings.egress.unset'),
                style: muted,
              ),
            ),
          if (_unconfirmed)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: InlineNotice(
                key: const Key('egress-default-unconfirmed'),
                message: copy('settings.egress.unconfirmed'),
                error: true,
              ),
            ),
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              if (current != null)
                ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 260),
                  child: Theme(
                    data: Theme.of(
                      context,
                    ).copyWith(dividerColor: Colors.transparent),
                    child: ExpansionTile(
                      key: const Key('egress-default-details'),
                      tilePadding: EdgeInsets.zero,
                      dense: true,
                      expandedAlignment: Alignment.centerLeft,
                      expandedCrossAxisAlignment: CrossAxisAlignment.start,
                      title: Text(
                        copy('settings.egress.details'),
                        style: muted,
                      ),
                      children: [
                        Text(current.effectiveProfile.id, style: muted),
                        Text(
                          copy.format('settings.egress.revision', {
                            'revision': current.effectiveProfile.revision,
                          }),
                          style: muted,
                        ),
                        Text(
                          copy.format('settings.egress.default_revision', {
                            'revision': current.revision,
                          }),
                          style: muted,
                        ),
                      ],
                    ),
                  ),
                ),
              if (_owner && current?.profile != null)
                TextButton(
                  key: const Key('egress-default-clear'),
                  onPressed: _saving
                      ? null
                      : () => unawaited(_setDefault(null)),
                  child: Text(copy('settings.egress.clear_default')),
                ),
            ],
          ),
        ],
      ),
    );
  }

  /// One exit's facts, shared by the table and the narrow list.
  _ExitFacts _facts(EgressProfileRevision profile) {
    final builtIn = profile.id == EgressProfileRevision.direct.id;
    final current = _default?.effectiveProfile;
    final policy = profile.policy;
    final (connection, connectionDetail) = switch (policy.proxy.kind) {
      'direct' => (copy('environment.egress.proxy.direct'), null),
      'socks5' => ('SOCKS5', policy.proxy.endpoint),
      'vless' => ('VLESS', null),
      final other => (other, null),
    };
    final (dns, dnsDetail) = switch (policy.resolver.kind) {
      'system' => (copy('environment.egress.resolver.system'), null),
      'doh' => (
        copy('environment.egress.resolver.doh'),
        copy(
          policy.resolver.transport == 'proxy'
              ? policy.proxy.isVless
                    ? 'environment.egress.doh.transport.vless'
                    : 'environment.egress.doh.transport.proxy'
              : 'environment.egress.doh.transport.direct',
        ),
      ),
      final other => (other, null),
    };
    return _ExitFacts(
      builtIn: builtIn,
      name: builtIn ? connection : profile.displayName,
      revision: builtIn
          ? copy('settings.egress.builtin')
          : copy.format('settings.egress.latest_revision', {
              'revision': profile.revision,
            }),
      connection: connection,
      connectionDetail: connectionDetail,
      dns: dns,
      dnsDetail: dnsDetail,
      isDefault: current == profile,
      newerThanDefault:
          current != null &&
          current.id == profile.id &&
          current.revision < profile.revision,
      defaultRevision: current?.revision,
    );
  }

  Widget _usedBy(EgressProfileRevision profile) => Text(
    networkExitUsageText(
      copy,
      networkExitUsage(
        profile.id,
        widget.controller.data?.environments ?? const [],
        widget.controller.data?.accounts ?? const [],
      ),
      profile.revision,
    ),
    key: Key('egress-profile-used-by-${profile.id}'),
    style: Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
  );

  List<Widget> _actions(EgressProfileRevision profile, _ExitFacts facts) => [
    if (facts.isDefault)
      StatusPill(
        label: copy('settings.egress.current'),
        color: context.viberColors.route,
      ),
    if (_owner)
      TextButton(
        key: Key('egress-default-set-${profile.id}'),
        onPressed: _saving || _default == null
            ? null
            : () => unawaited(_setDefault(profile)),
        child: Text(
          facts.newerThanDefault
              ? copy.format('settings.egress.apply_revision', {
                  'revision': profile.revision,
                })
              : copy('settings.egress.set_default'),
        ),
      ),
    if (_owner && !facts.builtIn)
      TextButton(
        key: Key('egress-profile-edit-${profile.id}'),
        onPressed: _saving ? null : () => unawaited(_edit(profile)),
        child: Text(copy('common.edit')),
      ),
  ];

  Widget? _newerNotice(_ExitFacts facts, EgressProfileRevision profile) =>
      facts.newerThanDefault
      ? Padding(
          padding: const EdgeInsets.only(top: 8),
          child: InlineNotice(
            message: copy.format('settings.egress.latest_unapplied', {
              'latest': profile.revision,
              'current': facts.defaultRevision!,
            }),
          ),
        )
      : null;

  Widget _profileTable(BuildContext context) {
    final colors = context.viberColors;
    final theme = Theme.of(context).textTheme;
    final header = theme.labelMedium?.copyWith(color: colors.textMuted);
    final muted = theme.bodySmall?.copyWith(color: colors.textMuted);
    Widget cell(String title, String? detail, {bool strong = false}) => Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          title,
          style: strong
              ? theme.bodyMedium?.copyWith(fontWeight: FontWeight.w500)
              : theme.bodyMedium,
        ),
        if (detail != null) Text(detail, style: muted),
      ],
    );
    const flex = [3, 2, 2, 3, 3];
    return Column(
      key: const Key('egress-profile-table'),
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.only(bottom: 8),
          child: Row(
            children: [
              for (final (index, label) in [
                copy('network_exits.column.exit'),
                copy('network_exits.column.connection'),
                copy('environment.egress.resolver.label'),
                copy('network_exits.used_by'),
                '',
              ].indexed)
                Expanded(
                  flex: flex[index],
                  child: Text(label, style: header),
                ),
            ],
          ),
        ),
        for (final profile in _profiles)
          Builder(
            builder: (context) {
              final facts = _facts(profile);
              final notice = _newerNotice(facts, profile);
              return DecoratedBox(
                key: Key('egress-profile-row-${profile.id}'),
                decoration: BoxDecoration(
                  border: Border(top: BorderSide(color: colors.dividerSoft)),
                ),
                child: Padding(
                  padding: const EdgeInsets.symmetric(vertical: 10),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Expanded(
                            flex: flex[0],
                            child: cell(
                              facts.name,
                              facts.revision,
                              strong: true,
                            ),
                          ),
                          Expanded(
                            flex: flex[1],
                            child: cell(
                              facts.connection,
                              facts.connectionDetail,
                            ),
                          ),
                          Expanded(
                            flex: flex[2],
                            child: cell(facts.dns, facts.dnsDetail),
                          ),
                          Expanded(
                            flex: flex[3],
                            child: facts.builtIn
                                ? const SizedBox.shrink()
                                : _usedBy(profile),
                          ),
                          Expanded(
                            flex: flex[4],
                            child: Wrap(
                              alignment: WrapAlignment.end,
                              crossAxisAlignment: WrapCrossAlignment.center,
                              spacing: 4,
                              children: _actions(profile, facts),
                            ),
                          ),
                        ],
                      ),
                      ?notice,
                    ],
                  ),
                ),
              );
            },
          ),
      ],
    );
  }

  Widget _profileList(BuildContext context) {
    final colors = context.viberColors;
    final muted = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: colors.textMuted);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final profile in _profiles)
          Builder(
            builder: (context) {
              final facts = _facts(profile);
              final notice = _newerNotice(facts, profile);
              return DecoratedBox(
                key: Key('egress-profile-row-${profile.id}'),
                decoration: BoxDecoration(
                  border: Border(top: BorderSide(color: colors.dividerSoft)),
                ),
                child: Padding(
                  padding: const EdgeInsets.symmetric(vertical: 10),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Text(
                        facts.name,
                        style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                          fontWeight: FontWeight.w500,
                        ),
                      ),
                      Text(facts.revision, style: muted),
                      Text(
                        egressPolicySummary(copy, profile.policy),
                        style: muted,
                      ),
                      if (!facts.builtIn) _usedBy(profile),
                      ?notice,
                      Wrap(
                        spacing: 4,
                        crossAxisAlignment: WrapCrossAlignment.center,
                        children: _actions(profile, facts),
                      ),
                    ],
                  ),
                ),
              );
            },
          ),
      ],
    );
  }

  Future<void> _setDefault(EgressProfileRevision? profile) async {
    final current = _default;
    if (current == null || _saving || !_owner) return;
    final generation = _generation;
    final controller = widget.controller;
    setState(() {
      _saving = true;
      _unconfirmed = false;
    });
    try {
      final acknowledged = await controller.setEgressDefault(current, profile);
      if (!mounted ||
          generation != _generation ||
          !identical(controller, widget.controller)) {
        return;
      }
      setState(() {
        _default = acknowledged;
        _saving = false;
      });
    } on Object {
      if (!mounted ||
          generation != _generation ||
          !identical(controller, widget.controller)) {
        return;
      }
      setState(() {
        _saving = false;
        _unconfirmed = true;
      });
      await _load();
    }
  }

  Future<void> _edit([EgressProfileRevision? profile]) async {
    if (_saving || !_owner) return;
    final controller = widget.controller;
    final generation = _runtimeGeneration;
    bool current() =>
        mounted &&
        generation == _runtimeGeneration &&
        identical(controller, widget.controller) &&
        _owner;
    final draft = await showEgressProfileEditor(
      context: context,
      copy: copy,
      initial: profile,
      vlessEngineInstalled:
          controller.runtimeStatus?.networkAccess?.vlessAvailable,
      loadVlessDescription: profile?.policy.proxy.isVless == true
          ? () async {
              if (!current()) {
                throw StateError('Profile editor is no longer current');
              }
              final settings = await controller.vlessProfileDescription(
                profile!.id,
                profile.revision,
              );
              if (!current()) {
                throw StateError('Profile editor is no longer current');
              }
              return settings;
            }
          : null,
    );
    if (draft == null || !current()) return;
    setState(() {
      _saving = true;
      _profileMutationError = null;
    });
    try {
      if (draft is VlessEgressProfileDraft) {
        final intent = draft.import;
        if (profile == null) {
          await controller.createVlessEgressProfile(
            displayName: draft.displayName,
            uri: intent.uri,
            resolver: intent.resolver,
            directDomains: intent.directDomains,
            tls: intent.tls,
          );
        } else {
          await controller.importVlessEgressProfile(
            id: profile.id,
            expectedRevision: profile.revision,
            displayName: draft.displayName,
            uri: intent.uri,
            resolver: intent.resolver,
            directDomains: intent.directDomains,
            tls: intent.tls,
          );
        }
      } else if (profile == null) {
        await controller.createEgressProfile(
          displayName: draft.displayName,
          policy: (draft as EgressProfileDraft).policy,
        );
      } else {
        await controller.publishEgressProfile(
          id: profile.id,
          expectedRevision: profile.revision,
          displayName: draft.displayName,
          policy: (draft as EgressProfileDraft).policy,
        );
      }
      if (!current()) return;
      setState(() => _saving = false);
      await _load();
    } on Object catch (error) {
      if (!current()) return;
      setState(() {
        _saving = false;
        _profileMutationError = switch (error) {
          ControlProblem(status: 400 || 422)
              when draft is VlessEgressProfileDraft =>
            'environment.egress.validation.vless',
          UnsupportedError() when draft is VlessEgressProfileDraft =>
            'environment.egress.vless.preview_unsupported',
          _ => 'settings.egress.profile_unconfirmed',
        };
      });
    }
  }
}

final class _GeneralSettingsPane extends StatelessWidget {
  const _GeneralSettingsPane({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => _SettingsPaneLayout(
    scrollKey: const Key('settings-scroll'),
    children: [
      _SettingsPaneHeader(
        icon: Icons.tune,
        title: copy('settings.preferences.title'),
        detail: copy('settings.preferences.detail'),
        dismissHelpLabel: copy('common.dismiss'),
      ),
      const SizedBox(height: 14),
      if (controller.preferenceWarning case final warning?) ...[
        InlineNotice(
          key: const Key('preferences-warning'),
          message: copy(warning),
          error: true,
        ),
        const SizedBox(height: 10),
      ],
      _SettingsSurface(
        accent: true,
        child: Wrap(
          spacing: 28,
          runSpacing: 12,
          children: [
            _PreferenceControl(
              label: copy('settings.appearance'),
              child: CompactSegmentedControl<WorkbenchTheme>(
                key: const Key('settings-theme'),
                segments: [
                  CompactSegment(
                    value: WorkbenchTheme.system,
                    label: copy('settings.auto'),
                  ),
                  CompactSegment(
                    value: WorkbenchTheme.light,
                    label: copy('settings.light'),
                  ),
                  CompactSegment(
                    value: WorkbenchTheme.dark,
                    label: copy('settings.dark'),
                  ),
                ],
                minSegmentWidth: 42,
                expanded: true,
                selected: controller.theme,
                onSelected: controller.setTheme,
              ),
            ),
            _PreferenceControl(
              label: copy('settings.language'),
              child: CompactSegmentedControl<AppLanguage>(
                key: const Key('settings-language'),
                segments: [
                  CompactSegment(
                    value: AppLanguage.english,
                    label: copy('settings.english'),
                  ),
                  CompactSegment(
                    value: AppLanguage.simplifiedChinese,
                    label: copy('settings.chinese'),
                  ),
                ],
                expanded: true,
                selected: controller.language,
                onSelected: controller.setLanguage,
              ),
            ),
          ],
        ),
      ),
      const SizedBox(height: 12),
      _SettingsSurface(
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(Icons.memory, size: 17, color: context.viberColors.route),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    copy('settings.runtime'),
                    style: Theme.of(context).textTheme.labelLarge,
                  ),
                  const SizedBox(height: 3),
                  Text(
                    controller.previewMode
                        ? copy('settings.preview')
                        : controller.serverManagement
                        ? copy.format('settings.remote', {
                            'target': controller.runtimeConnectTarget,
                          })
                        : copy('settings.live'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
      const SizedBox(height: 12),
      // Transparent capture is a way to launch agents, so it lives on
      // Access and launch wherever that page exists.
      if (controller.transparentCapture case final capture?
          when !controller.accessSettingsAvailable) ...[
        _TransparentCapturePanel(controller: capture, copy: copy),
        const SizedBox(height: 12),
      ],
      _ProductUpdatePanel(controller: controller, copy: copy),
    ],
  );
}

final class _TransparentCapturePanel extends StatelessWidget {
  const _TransparentCapturePanel({
    required this.controller,
    required this.copy,
  });

  final TransparentCaptureController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => _SettingsSurface(
    key: const Key('transparent-capture-panel'),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Icon(
              Icons.hub_outlined,
              size: 17,
              color: context.viberColors.route,
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                copy('settings.transparent_capture.title'),
                style: Theme.of(context).textTheme.labelLarge,
              ),
            ),
            const SizedBox(width: 8),
            Semantics(
              label: copy('settings.transparent_capture.title'),
              child: Switch(
                key: const Key('transparent-capture-toggle'),
                value: controller.enabled,
                onChanged:
                    controller.busy ||
                        (!controller.enabled && !controller.canEnable)
                    ? null
                    : controller.setEnabled,
              ),
            ),
          ],
        ),
        Text(
          copy('settings.transparent_capture.detail'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
        const SizedBox(height: 8),
        Text(
          copy('settings.transparent_capture.state.${controller.state}'),
          key: const Key('transparent-capture-state'),
          style: Theme.of(context).textTheme.labelMedium,
        ),
        if (controller.error case final error?) ...[
          const SizedBox(height: 8),
          InlineNotice(message: error, error: true),
        ],
        const SizedBox(height: 8),
        OutlinedButton.icon(
          key: const Key('transparent-capture-system-settings'),
          onPressed: controller.busy ? null : controller.openSystemSettings,
          icon: const Icon(Icons.open_in_new, size: 14),
          label: Text(copy('settings.transparent_capture.system_settings')),
        ),
      ],
    ),
  );
}

final class _ProductUpdatePanel extends StatelessWidget {
  const _ProductUpdatePanel({required this.controller, required this.copy});

  static final _releases = Uri.https(
    'github.com',
    '/vibe-agi/vibermate/releases',
  );
  static const _brewCommand = 'brew upgrade --cask vibe-agi/tap/vibermate';

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final update = controller.productUpdate;
    final runtimeBuild = controller.data?.status.productBuild ?? '—';
    final terminalBuild = controller.terminalCommand?.installedBuild;
    final heading = Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(
          Icons.system_update_alt,
          size: 17,
          color: context.viberColors.route,
        ),
        const SizedBox(width: 8),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                copy('updates.title'),
                style: Theme.of(context).textTheme.labelLarge,
              ),
              const SizedBox(height: 3),
              Text(
                copy('updates.detail'),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
          ),
        ),
      ],
    );
    final check = OutlinedButton.icon(
      key: const Key('product-update-check'),
      onPressed: controller.productUpdateLoading
          ? null
          : () => unawaited(controller.checkProductUpdate()),
      icon: controller.productUpdateLoading
          ? const CompactProgressIndicator()
          : const Icon(Icons.refresh, size: 15),
      label: Text(copy('updates.check')),
    );
    return _SettingsSurface(
      key: const Key('product-update-panel'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LayoutBuilder(
            builder: (context, constraints) => constraints.maxWidth < 520
                ? Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      heading,
                      const SizedBox(height: 8),
                      Align(alignment: Alignment.centerLeft, child: check),
                    ],
                  )
                : Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Expanded(child: heading),
                      const SizedBox(width: 12),
                      check,
                    ],
                  ),
          ),
          const SizedBox(height: 10),
          Wrap(
            spacing: 18,
            runSpacing: 6,
            children: [
              _UpdateBuildFact(
                label: copy('updates.app'),
                value: controller.appProductBuild,
              ),
              _UpdateBuildFact(
                label: copy('updates.runtime'),
                value: runtimeBuild,
              ),
              if (terminalBuild != null)
                _UpdateBuildFact(
                  label: copy('updates.terminal'),
                  value: terminalBuild,
                ),
            ],
          ),
          if (controller.runtimeBuildMismatch ||
              controller.terminalBuildMismatch) ...[
            const SizedBox(height: 9),
            InlineNotice(message: copy('updates.mismatch_detail'), error: true),
          ],
          if (update != null) ...[
            const SizedBox(height: 9),
            if (update.state == ProductUpdateState.unavailable)
              InlineNotice(message: copy('updates.unavailable'))
            else
              Wrap(
                spacing: 8,
                runSpacing: 6,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    copy(
                      update.state == ProductUpdateState.available
                          ? 'updates.available'
                          : 'updates.current',
                    ),
                    style: Theme.of(context).textTheme.labelMedium?.copyWith(
                      fontWeight: FontWeight.w600,
                      color: update.state == ProductUpdateState.available
                          ? context.viberColors.warning
                          : context.viberColors.verified,
                    ),
                  ),
                  Text(
                    [
                      if (update.availableVersion != null)
                        update.availableVersion!,
                      if (update.releaseName != null) update.releaseName!,
                    ].join(' · '),
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ],
              ),
            const SizedBox(height: 8),
            Text(
              copy(switch (update.channel) {
                ProductInstallChannel.homebrew => 'updates.guide.homebrew',
                ProductInstallChannel.webServer => 'updates.guide.server',
                ProductInstallChannel.manual => 'updates.guide.manual',
              }),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 7),
            Wrap(
              spacing: 8,
              runSpacing: 7,
              children: [
                if (update.channel == ProductInstallChannel.homebrew)
                  FilledButton.icon(
                    key: const Key('product-update-copy-brew'),
                    onPressed: () => unawaited(
                      Clipboard.setData(
                        const ClipboardData(text: _brewCommand),
                      ),
                    ),
                    icon: const Icon(Icons.copy, size: 14),
                    label: Text(copy('updates.copy_brew')),
                  ),
                OutlinedButton.icon(
                  key: const Key('product-update-open-release'),
                  onPressed: () => unawaited(
                    launchUrl(
                      update.releaseUrl ?? _releases,
                      mode: LaunchMode.externalApplication,
                    ),
                  ),
                  icon: const Icon(Icons.open_in_new, size: 14),
                  label: Text(copy('updates.release_notes')),
                ),
              ],
            ),
          ],
          const SizedBox(height: 8),
          Text(
            copy('updates.no_auto_install'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
        ],
      ),
    );
  }
}

final class _UpdateBuildFact extends StatelessWidget {
  const _UpdateBuildFact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Text.rich(
    TextSpan(
      children: [
        TextSpan(text: '$label  '),
        TextSpan(text: value, style: monoStyle),
      ],
    ),
    style: Theme.of(context).textTheme.bodySmall,
  );
}

final class _AccessSettingsPane extends StatefulWidget {
  const _AccessSettingsPane({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_AccessSettingsPane> createState() => _AccessSettingsPaneState();
}

final class _AccessSettingsPaneState extends State<_AccessSettingsPane> {
  final _terminalKey = GlobalKey();
  final _remoteKey = GlobalKey();

  WorkbenchController get controller => widget.controller;
  AppCopy get copy => widget.copy;

  @override
  void initState() {
    super.initState();
    _scrollToFocus();
  }

  @override
  void didUpdateWidget(covariant _AccessSettingsPane oldWidget) {
    super.didUpdateWidget(oldWidget);
    _scrollToFocus();
  }

  /// A link into this page names what it is for; bring that into view once
  /// the page has been laid out.
  void _scrollToFocus() {
    final focus = controller.takeAccessFocus();
    if (focus == null) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final target = switch (focus) {
        AccessFocus.terminal => _terminalKey,
        AccessFocus.remote => _remoteKey,
      };
      final context = target.currentContext;
      if (!mounted || context == null) return;
      unawaited(
        Scrollable.ensureVisible(
          context,
          duration: const Duration(milliseconds: 200),
        ),
      );
    });
  }

  // On this Mac, showing the page or returning to the window rechecks the
  // Terminal command, whether Terminal finds it, and the certificate.
  @override
  Widget build(BuildContext context) => controller.terminalManagement
      ? SetupFactsWatcher(controller: controller, child: _localPane(context))
      : _serverPane(context);

  Widget _header() => _SettingsPaneHeader(
    icon: Icons.link,
    title: copy('settings.access.title'),
    detail: copy(
      controller.terminalManagement
          ? 'settings.access.detail.local'
          : 'settings.access.detail.server',
    ),
    dismissHelpLabel: copy('common.dismiss'),
  );

  Widget _showGuide() => Align(
    alignment: Alignment.centerLeft,
    child: TextButton.icon(
      key: const Key('settings-show-setup-guide'),
      onPressed: controller.showSetupGuide,
      icon: const Icon(Icons.checklist, size: 15),
      label: Text(copy('setup.show')),
    ),
  );

  Widget _editorGuide() => ACPSetupGuide(
    copy: copy,
    program:
        controller.terminalCommand?.targetPath ?? '/absolute/path/to/vibermate',
    serverURL: controller.terminalManagement ? '' : controller.runtimeServerURL,
  );

  /// The Mac app: starting Claude Code or Codex first, then the optional
  /// ways, each folded away, and the listener details last.
  Widget _localPane(BuildContext context) {
    final access = controller.runtimeStatus?.networkAccess;
    final remote =
        controller.serverManagement || controller.webPrincipal != null;
    final codexNeedsRoot = setupRootTrusted(controller) == false;
    return _SettingsPaneLayout(
      scrollKey: const Key('settings-access-scroll'),
      children: [
        _header(),
        const SizedBox(height: 6),
        _showGuide(),
        const SizedBox(height: 8),
        _AccessSectionTitle(
          key: const Key('access-start'),
          title: copy('terminal.run.title'),
          detail: copy('terminal.run.detail'),
        ),
        const SizedBox(height: 10),
        KeyedSubtree(
          key: _terminalKey,
          child: _TerminalCommandPanel(controller: controller, copy: copy),
        ),
        const SizedBox(height: 9),
        Container(
          key: const Key('managed-run-guide'),
          padding: const EdgeInsets.fromLTRB(11, 10, 10, 10),
          decoration: BoxDecoration(
            border: Border.all(color: context.viberColors.dividerSoft),
            borderRadius: ViberMetrics.surfaceRadius,
          ),
          child: LaunchCommandCard(
            controller: controller,
            copy: copy,
            codexFooter: codexNeedsRoot
                ? CodexCertificateRow(
                    controller: controller,
                    copy: copy,
                    keyPrefix: 'access',
                  )
                : null,
            onUseRoutingSet: () =>
                controller.selectSection(WorkbenchSection.environments),
          ),
        ),
        const SizedBox(height: 8),
        Text(
          copy('settings.access.local.no_account'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 16),
        _AccessSection(
          key: const Key('access-editor-section'),
          title: copy('settings.access.editor.title'),
          detail: copy('settings.access.editor.detail'),
          children: [_editorGuide()],
        ),
        if (controller.transparentCapture case final capture?)
          _transparentSection(capture),
        _AccessSection(
          key: ValueKey(
            'access-other-section-${controller.remoteConnectionGuideRequested}',
          ),
          title: copy('settings.access.other.title'),
          detail: copy('settings.access.other.detail'),
          initiallyExpanded: controller.remoteConnectionGuideRequested,
          children: [
            if (remote)
              KeyedSubtree(
                key: _remoteKey,
                child: ExpansionTile(
                  key: ValueKey(
                    'settings-remote-guide-${controller.remoteConnectionGuideRequested}',
                  ),
                  initiallyExpanded: controller.remoteConnectionGuideRequested,
                  tilePadding: EdgeInsets.zero,
                  childrenPadding: EdgeInsets.zero,
                  shape: const Border(),
                  collapsedShape: const Border(),
                  title: Text(copy('settings.access.team.title')),
                  subtitle: Text(copy('settings.access.team.optional')),
                  children: [
                    _RemoteAccessGuide(controller: controller, copy: copy),
                  ],
                ),
              ),
            const SizedBox(height: 8),
            ServerDeploymentGuide(copy: copy),
            const SizedBox(height: 8),
          ],
        ),
        if (access != null) ...[
          const SizedBox(height: 8),
          CompactDisclosure(
            key: const Key('access-technical-details'),
            toggleKey: const Key('access-technical-details-toggle'),
            label: copy('common.technical_details'),
            child: Padding(
              padding: const EdgeInsets.only(left: 18, top: 2),
              child: _NetworkAccessFacts(
                access: access,
                copy: copy,
                vless: false,
              ),
            ),
          ),
        ],
      ],
    );
  }

  /// A Server or the Web workbench: today's order, connecting from another
  /// computer being the point of the page.
  Widget _serverPane(BuildContext context) => _SettingsPaneLayout(
    scrollKey: const Key('settings-access-scroll'),
    children: [
      _header(),
      const SizedBox(height: 6),
      _showGuide(),
      const SizedBox(height: 8),
      if (controller.runtimeStatus?.networkAccess case final access?)
        _NetworkAccessFacts(access: access, copy: copy),
      if (controller.serverManagement) ...[
        const SizedBox(height: 14),
        ServerDeploymentGuide(copy: copy),
      ],
      if (controller.runtimeServerURL.isNotEmpty) ...[
        const SizedBox(height: 16),
        _editorGuide(),
      ],
      if (controller.serverManagement || controller.webPrincipal != null) ...[
        const SizedBox(height: 16),
        KeyedSubtree(
          key: _remoteKey,
          child: _RemoteAccessGuide(controller: controller, copy: copy),
        ),
      ],
      if (controller.transparentCapture case final capture?) ...[
        const SizedBox(height: 16),
        _transparentSection(capture),
      ],
    ],
  );

  Widget _transparentSection(TransparentCaptureController capture) =>
      _AccessSection(
        key: const Key('access-transparent-section'),
        title: copy('settings.access.transparent.title'),
        badge: copy('settings.access.transparent.badge'),
        detail: copy('settings.access.transparent.detail'),
        children: [_TransparentCapturePanel(controller: capture, copy: copy)],
      );
}

/// The title of a section of Access and launch, with one line under it.
final class _AccessSectionTitle extends StatelessWidget {
  const _AccessSectionTitle({
    required this.title,
    required this.detail,
    super.key,
  });

  final String title;
  final String detail;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(
        title,
        style: Theme.of(
          context,
        ).textTheme.titleSmall?.copyWith(fontWeight: FontWeight.w600),
      ),
      const SizedBox(height: 2),
      Text(
        detail,
        style: Theme.of(
          context,
        ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
      ),
    ],
  );
}

/// An optional way to use ViberMate on Access and launch, folded away until
/// someone opens it.
final class _AccessSection extends StatelessWidget {
  const _AccessSection({
    required this.title,
    required this.detail,
    required this.children,
    this.badge,
    this.initiallyExpanded = false,
    super.key,
  });

  final String title;
  final String detail;
  final String? badge;
  final bool initiallyExpanded;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) => DecoratedBox(
    decoration: BoxDecoration(
      border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
    ),
    child: ExpansionTile(
      initiallyExpanded: initiallyExpanded,
      tilePadding: EdgeInsets.zero,
      childrenPadding: const EdgeInsets.only(bottom: 12),
      expandedCrossAxisAlignment: CrossAxisAlignment.stretch,
      shape: const Border(),
      collapsedShape: const Border(),
      title: Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text(title, style: Theme.of(context).textTheme.titleSmall),
          if (badge case final label?)
            StatusPill(label: label, color: context.viberColors.warning),
        ],
      ),
      subtitle: Text(
        detail,
        style: Theme.of(
          context,
        ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
      ),
      children: children,
    ),
  );
}

final class _NetworkAccessFacts extends StatelessWidget {
  const _NetworkAccessFacts({
    required this.access,
    required this.copy,
    this.vless = true,
  });
  final HostNetworkAccess access;
  final AppCopy copy;

  /// The VLESS engine line; on this Mac it sits with the network exits.
  final bool vless;

  @override
  Widget build(BuildContext context) => Column(
    key: const Key('settings-network-access'),
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(
        copy('settings.access.socks'),
        style: Theme.of(context).textTheme.labelLarge,
      ),
      const SizedBox(height: 2),
      if (access.socksAddress case final address?)
        SelectableText(address, style: monoStyle)
      else
        Text(
          copy('settings.access.network.disabled'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      if (vless) ...[
        const SizedBox(height: 4),
        Text(
          copy(
            access.vlessAvailable
                ? 'settings.access.vless.installed'
                : 'settings.access.vless.absent',
          ),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    ],
  );
}

final class _RemoteAccessGuide extends StatelessWidget {
  const _RemoteAccessGuide({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      if (controller.serverManagement) ...[
        Align(
          alignment: Alignment.centerLeft,
          child: OutlinedButton.icon(
            key: const Key('settings-access-manage-users'),
            onPressed: controller.openRuntimeUsersSettings,
            icon: const Icon(Icons.group_outlined, size: 15),
            label: Text(copy('settings.tab.users')),
          ),
        ),
        const SizedBox(height: 8),
      ],
      _ServerAccessPanel(controller: controller, copy: copy),
      if (controller.serverManagement) ...[
        const SizedBox(height: 12),
        _ServerIpAllowlistPanel(controller: controller, copy: copy),
      ],
    ],
  );
}

/// Lets the Owner choose which networks may connect to the Server.
final class _ServerIpAllowlistPanel extends StatefulWidget {
  const _ServerIpAllowlistPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ServerIpAllowlistPanel> createState() =>
      _ServerIpAllowlistPanelState();
}

final class _ServerIpAllowlistPanelState
    extends State<_ServerIpAllowlistPanel> {
  final _text = TextEditingController();
  int? _shownRevision;
  String? _problem;
  bool _saved = false;

  @override
  void initState() {
    super.initState();
    _text.addListener(_edited);
  }

  @override
  void dispose() {
    _text
      ..removeListener(_edited)
      ..dispose();
    super.dispose();
  }

  void _edited() {
    if (_saved || _problem != null) {
      setState(() {
        _saved = false;
        _problem = null;
      });
    } else {
      setState(() {});
    }
  }

  /// Non-empty lines with the 1-based line each came from.
  List<(int, String)> get _entries => [
    for (final (index, line) in _text.text.split('\n').indexed)
      if (line.trim().isNotEmpty) (index + 1, line.trim()),
  ];

  bool _dirty(ServerIpAllowlist allowlist) {
    final entries = _entries.map((entry) => entry.$2).toList();
    return entries.length != allowlist.ranges.length ||
        entries.indexed.any((entry) => entry.$2 != allowlist.ranges[entry.$1]);
  }

  void _show(ServerIpAllowlist allowlist) {
    _shownRevision = allowlist.revision;
    _text.text = allowlist.ranges.join('\n');
  }

  void _addMine(String address) {
    final text = _text.text.trimRight();
    _text.text = text.isEmpty ? address : '$text\n$address';
  }

  Future<void> _save() async {
    final entries = _entries;
    final error = await widget.controller.saveServerIpAllowlist([
      for (final entry in entries) entry.$2,
    ]);
    if (!mounted) return;
    final latest = widget.controller.serverIpAllowlist;
    setState(() {
      if (error == null) {
        if (latest != null) _show(latest);
        _saved = true;
        _problem = null;
        return;
      }
      if (error is ControlProblem && error.status == 409 && latest != null) {
        _show(latest);
      }
      _saved = false;
      _problem = _describe(error, entries);
    });
  }

  String _describe(Object error, List<(int, String)> entries) {
    final copy = widget.copy;
    if (error is! ControlProblem) {
      return copy.format('server.ip_allowlist.error.failed', {
        'error': error.toString(),
      });
    }
    switch (error.reasonCode) {
      case 'ip_allowlist_entry_invalid':
        final index = error.entry;
        final line = index != null && index < entries.length
            ? entries[index].$1
            : 0;
        final reason = copy.format(
          'server.ip_allowlist.reason.${error.reason ?? 'syntax'}',
          {'suggestion': error.suggestion ?? ''},
        );
        return copy.format('server.ip_allowlist.error.line', {
          'line': line,
          'reason': reason,
        });
      case 'ip_allowlist_excludes_requester':
        return copy.format('server.ip_allowlist.error.excludes', {
          'address': error.clientAddress ?? '?',
        });
      case 'ip_allowlist_too_long':
        return copy.format('server.ip_allowlist.error.too_long', {
          'max': error.maxEntries ?? 256,
        });
      case 'ip_allowlist_conflict':
        return copy('server.ip_allowlist.error.conflict');
      default:
        return copy.format('server.ip_allowlist.error.failed', {
          'error': '${error.reasonCode} (${error.status})',
        });
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final allowlist = widget.controller.serverIpAllowlist;
    if (allowlist != null &&
        allowlist.revision != _shownRevision &&
        (_shownRevision == null || !_dirty(allowlist))) {
      _show(allowlist);
    }
    final saving = widget.controller.serverIpAllowlistSaving;
    final dirty = allowlist != null && _dirty(allowlist);
    final client = allowlist?.clientAddress;
    final small = Theme.of(context).textTheme.bodySmall;
    return _SettingsSurface(
      key: const Key('server-ip-allowlist'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          ContextHelpHeading(
            title: copy('server.ip_allowlist.title'),
            message: copy('server.ip_allowlist.help'),
            dismissLabel: copy('common.dismiss'),
          ),
          const SizedBox(height: 6),
          if (allowlist == null)
            InlineNotice(message: copy('server.ip_allowlist.unavailable'))
          else ...[
            Text(
              allowlist.restricted
                  ? copy.format('server.ip_allowlist.restricted', {
                      'count': allowlist.ranges.length,
                    })
                  : copy('server.ip_allowlist.open'),
              key: const Key('server-ip-allowlist-state'),
            ),
            const SizedBox(height: 4),
            Text(
              client == null
                  ? copy('server.ip_allowlist.app')
                  : copy.format('server.ip_allowlist.client', {
                      'address': client,
                    }),
              style: small,
            ),
            if (client != null && _loopbackAddress(client)) ...[
              const SizedBox(height: 8),
              InlineNotice(
                key: const Key('server-ip-allowlist-loopback-hint'),
                message: copy.format('server.ip_allowlist.loopback_hint', {
                  'address': client,
                }),
              ),
            ] else if (allowlist.trustedProxies.isNotEmpty)
              Text(
                copy.format('server.ip_allowlist.trusted', {
                  'proxies': allowlist.trustedProxies.join(', '),
                }),
                key: const Key('server-ip-allowlist-trusted'),
                style: small,
              )
            else if (client != null && _sharedAddress(client))
              Text(
                copy.format('server.ip_allowlist.private_hint', {
                  'address': client,
                }),
                key: const Key('server-ip-allowlist-private-hint'),
                style: small,
              ),
            if (allowlist.proxyHeaderProblems > 0) ...[
              const SizedBox(height: 8),
              InlineNotice(
                key: const Key('server-ip-allowlist-proxy-problems'),
                message: copy.format('server.ip_allowlist.proxy_problems', {
                  'count': allowlist.proxyHeaderProblems,
                }),
                error: true,
              ),
            ],
            const SizedBox(height: 10),
            TextField(
              key: const Key('server-ip-allowlist-field'),
              controller: _text,
              minLines: 3,
              maxLines: 10,
              enabled: !saving,
              style: monoStyle,
              keyboardType: TextInputType.multiline,
              decoration: InputDecoration(
                labelText: copy('server.ip_allowlist.field'),
                hintText: '203.0.113.0/24\n2001:db8::/32\n198.51.100.7',
                border: const OutlineInputBorder(),
              ),
            ),
            if (_problem case final problem?) ...[
              const SizedBox(height: 8),
              InlineNotice(
                key: const Key('server-ip-allowlist-problem'),
                message: problem,
                error: true,
              ),
            ] else if (_saved) ...[
              const SizedBox(height: 8),
              Text(
                copy('server.ip_allowlist.saved'),
                key: const Key('server-ip-allowlist-saved'),
                style: small?.copyWith(color: context.viberColors.verified),
              ),
            ],
            const SizedBox(height: 10),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                FilledButton.icon(
                  key: const Key('server-ip-allowlist-save'),
                  onPressed: dirty && !saving ? () => unawaited(_save()) : null,
                  icon: const Icon(Icons.save_outlined, size: 15),
                  label: Text(copy('server.ip_allowlist.save')),
                ),
                if (client != null &&
                    !_entries.any((entry) => entry.$2 == client))
                  OutlinedButton.icon(
                    key: const Key('server-ip-allowlist-add-mine'),
                    onPressed: saving ? null : () => _addMine(client),
                    icon: const Icon(Icons.add, size: 15),
                    label: Text(copy('server.ip_allowlist.add_mine')),
                  ),
                if (dirty)
                  TextButton(
                    key: const Key('server-ip-allowlist-revert'),
                    onPressed: saving
                        ? null
                        : () => setState(() => _show(allowlist)),
                    child: Text(copy('server.ip_allowlist.revert')),
                  ),
              ],
            ),
            if (allowlist.refusedCount > 0) ...[
              const SizedBox(height: 10),
              Text(
                allowlist.lastRefusedAddress != null &&
                        allowlist.lastRefusedAt != null
                    ? copy.format('server.ip_allowlist.refused', {
                        'count': allowlist.refusedCount,
                        'address': allowlist.lastRefusedAddress!,
                        'time': _storageTimestamp(allowlist.lastRefusedAt!),
                      })
                    : copy.format('server.ip_allowlist.refused_count', {
                        'count': allowlist.refusedCount,
                      }),
                key: const Key('server-ip-allowlist-refused'),
                style: small,
              ),
            ],
          ],
        ],
      ),
    );
  }
}

final class _UsersSettingsPane extends StatelessWidget {
  const _UsersSettingsPane({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => _SettingsPaneLayout(
    scrollKey: const Key('settings-users-scroll'),
    children: [
      _RuntimeUsersPanel(controller: controller, copy: copy),
      const SizedBox(height: 10),
      Align(
        alignment: Alignment.centerLeft,
        child: OutlinedButton.icon(
          key: const Key('settings-users-open-access'),
          onPressed: () =>
              controller.openAccessSettings(focus: AccessFocus.remote),
          icon: const Icon(Icons.link, size: 15),
          label: Text(copy('server.users.connection_guide')),
        ),
      ),
    ],
  );
}

final class _SafetyDataSettingsPane extends StatelessWidget {
  const _SafetyDataSettingsPane({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => _SettingsPaneLayout(
    scrollKey: const Key('settings-safety-scroll'),
    children: [
      _SettingsPaneHeader(
        icon: Icons.shield_outlined,
        title: copy('settings.safety.title'),
        detail: copy('settings.safety.detail'),
        dismissHelpLabel: copy('common.dismiss'),
      ),
      const SizedBox(height: 14),
      if (controller.accessSettingsAvailable) ...[
        _ServerConnectionSettingsPanel(controller: controller, copy: copy),
        const SizedBox(height: 12),
      ],
      if (controller.rootTrustManagement) ...[
        _RootCASettingsPanel(controller: controller, copy: copy),
        const SizedBox(height: 12),
      ] else if (controller.serverManagement) ...[
        RuntimeRootCAPanel(controller: controller, copy: copy),
        const SizedBox(height: 12),
      ],
      _StorageDisclosure(copy: copy, controller: controller),
    ],
  );
}

final class _ServerConnectionSettingsPanel extends StatelessWidget {
  const _ServerConnectionSettingsPanel({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final guide = controller.connectionGuide;
    final access = controller.serverAccess;
    final tls =
        guide.security == ServerConnectionSecurity.https &&
            access?.transport == 'https'
        ? access!.tls
        : null;
    final status = switch (guide.security) {
      ServerConnectionSecurity.unavailable => 'unknown',
      ServerConnectionSecurity.loopbackHttp => 'local_http',
      ServerConnectionSecurity.remoteHttp => 'remote_http',
      ServerConnectionSecurity.https => _tlsNotice(tls),
    };
    final connectionError =
        guide.security == ServerConnectionSecurity.remoteHttp ||
        tls?.state == 'renewal_failed';
    return _SettingsSurface(
      key: const Key('server-connection-settings-panel'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          ContextHelpHeading(
            title: copy('settings.server_connection.title'),
            message: copy('settings.server_connection.detail'),
            dismissLabel: copy('common.dismiss'),
          ),
          const SizedBox(height: 8),
          if (guide.serverURL case final url?)
            SelectableText(url, style: monoStyle),
          const SizedBox(height: 5),
          InlineNotice(
            message: copy('settings.server_connection.$status'),
            error: connectionError,
          ),
          if (tls case final identity?) ...[
            const SizedBox(height: 8),
            Wrap(
              key: const Key('server-tls-summary'),
              spacing: 16,
              runSpacing: 8,
              children: [
                Text(
                  copy('settings.server_connection.tls.mode.${identity.mode}'),
                ),
                if (identity.serverName case final name?)
                  SelectableText(name, style: monoStyle),
                if (identity.notAfter case final expiry?)
                  Text(
                    '${copy('settings.server_connection.tls.expires')}: $expiry',
                  ),
              ],
            ),
          ],
          if (guide.available)
            ExpansionTile(
              key: const Key('server-connection-details'),
              tilePadding: EdgeInsets.zero,
              childrenPadding: EdgeInsets.zero,
              title: Text(copy('settings.server_connection.advanced')),
              children: [
                Text(
                  copy(
                    guide.usesConnectedOrigin
                        ? 'settings.server_connection.source.browser'
                        : 'settings.server_connection.source.server',
                  ),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                const SizedBox(height: 6),
                Text(
                  copy(
                    tls == null
                        ? 'settings.server_connection.deployment'
                        : 'settings.server_connection.deployment.${tls.mode}',
                  ),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
                if (tls case final status?) ...[
                  const SizedBox(height: 8),
                  _ServerTLSDetails(status: status, copy: copy),
                ],
              ],
            ),
        ],
      ),
    );
  }

  static String _tlsNotice(RuntimeServerTLS? tls) {
    if (tls == null) return 'https';
    if (tls.state == 'renewal_failed') return 'automatic_failed';
    if (tls.mode == 'automatic_tls') {
      return switch (tls.state) {
        'pending' => 'automatic_pending',
        'renewing' => 'automatic_renewing',
        'ready' => 'automatic_ready',
        _ => 'https',
      };
    }
    return switch (tls.mode) {
      'private_ca_tls' || 'self_signed_tls' => 'private_ca',
      'tls_files' => 'tls_files',
      _ => 'https',
    };
  }
}

final class _ServerTLSDetails extends StatelessWidget {
  const _ServerTLSDetails({required this.status, required this.copy});

  final RuntimeServerTLS status;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final facts = <(String, String)>[
      (
        copy('settings.server_connection.tls.state'),
        copy('settings.server_connection.tls.state.${status.state}'),
      ),
      if (status.challenge case final value?)
        (
          copy('settings.server_connection.tls.challenge'),
          copy('settings.server_connection.tls.challenge.$value'),
        ),
      if (status.issuer case final value?)
        (copy('settings.server_connection.tls.issuer'), value),
      if (status.fingerprint case final value?)
        (copy('settings.server_connection.tls.fingerprint'), value),
      if (status.lastError case final value?)
        (copy('settings.server_connection.tls.error'), value),
    ];
    return Container(
      key: const Key('server-tls-details'),
      width: double.infinity,
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          for (var index = 0; index < facts.length; index++) ...[
            Text(
              facts[index].$1,
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
            const SizedBox(height: 1),
            SelectableText(facts[index].$2, style: monoStyle),
            if (index != facts.length - 1) const SizedBox(height: 7),
          ],
        ],
      ),
    );
  }
}

final class _RootCASettingsPanel extends StatefulWidget {
  const _RootCASettingsPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_RootCASettingsPanel> createState() => _RootCASettingsPanelState();
}

final class _RootCASettingsPanelState extends State<_RootCASettingsPanel> {
  WorkbenchController get controller => widget.controller;
  AppCopy get copy => widget.copy;

  @override
  Widget build(BuildContext context) {
    final status = controller.rootCAStatus;
    final localDesktop = controller.rootTrustManagement;
    return Container(
      key: const Key('root-ca-settings-panel'),
      padding: const EdgeInsets.symmetric(vertical: 14),
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: context.viberColors.dividerSoft)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
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
                  copy('settings.root_ca.title'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              if (localDesktop && status != null)
                IconButton(
                  key: const Key('root-ca-retry'),
                  onPressed:
                      controller.rootCAMutating || controller.rootCALoading
                      ? null
                      : () => unawaited(controller.refreshRootCA()),
                  tooltip: copy('settings.root_ca.retry'),
                  icon: const Icon(Icons.refresh, size: 16),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
            ],
          ),
          // What the certificate is for, and that Codex needs it, is always
          // on the page rather than behind a help button.
          Padding(
            padding: const EdgeInsets.only(left: 26, top: 2),
            child: Text(
              copy('settings.root_ca.detail'),
              key: const Key('root-ca-purpose'),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
          ),
          const SizedBox(height: 10),
          if (!localDesktop)
            InlineNotice(
              message: copy('settings.root_ca.unavailable'),
              error: false,
            )
          else if (controller.rootCALoading && status == null)
            CompactLoadingMessage(label: copy('common.loading'))
          else if (status == null)
            _RootCAUnavailable(
              message: copy('settings.root_ca.load_failed'),
              recovery: copy('settings.root_ca.load_recovery'),
              retryLabel: copy('settings.root_ca.retry'),
              onRetry: () => unawaited(controller.refreshRootCA()),
            )
          else
            _RootCAStatusBody(
              status: status,
              copy: copy,
              mutating: controller.rootCAMutating || controller.rootCALoading,
              error: controller.rootCAError,
              onInstall: () =>
                  unawaited(confirmRootTrustInstall(context, controller, copy)),
              onRemove: () => unawaited(controller.openRootCARemovalGuide()),
              onReplace: _confirmReplace,
              onRestart: () => unawaited(controller.restartForRootReset()),
              restartRequired: controller.rootCARestartRequired,
              guideIntent: controller.rootCAGuideIntent,
              canReplace: controller.runningCaptures.isEmpty,
              onRetry: () => unawaited(controller.refreshRootCA()),
            ),
          if (controller.operationNotice == 'root_ca_updated') ...[
            const SizedBox(height: 8),
            InlineNotice(
              key: const Key('root-ca-updated'),
              message: copy('settings.root_ca.updated'),
              onDismiss: controller.clearNotice,
              dismissLabel: copy('common.dismiss'),
            ),
          ],
        ],
      ),
    );
  }

  Future<void> _confirmReplace() async {
    if (controller.rootCAStatus?.certificatePresent != 'absent') {
      await controller.replaceRootCA();
      return;
    }
    await _confirmAction(
      title: copy('settings.root_ca.replace.title'),
      detail: copy('settings.root_ca.replace.detail'),
      actionLabel: copy('settings.root_ca.confirm'),
      action: controller.replaceRootCA,
    );
  }

  Future<void> _confirmAction({
    required String title,
    required String detail,
    required String actionLabel,
    required Future<bool> Function() action,
  }) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        key: const Key('root-ca-confirmation'),
        title: Text(title),
        content: Text(detail),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(dialogContext).pop(false),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            key: const Key('root-ca-confirm-action'),
            onPressed: () => Navigator.of(dialogContext).pop(true),
            child: Text(actionLabel),
          ),
        ],
      ),
    );
    if (confirmed == true && mounted) await action();
  }
}

/// Says what trusting ViberMate's certificate allows, before macOS asks for
/// a password, then trusts it. Every 'Install and trust' button goes
/// through here: Settings, the setup guide and a stopped Codex launch.
Future<bool> confirmRootTrustInstall(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (dialogContext) => AlertDialog(
      key: const Key('root-ca-install-confirmation'),
      title: Text(copy('settings.root_ca.confirm_install.title')),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 430),
        child: Text(copy('settings.root_ca.confirm_install.detail')),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(dialogContext).pop(false),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('root-ca-install-continue'),
          onPressed: () => Navigator.of(dialogContext).pop(true),
          child: Text(copy('common.continue')),
        ),
      ],
    ),
  );
  if (confirmed != true) return false;
  return controller.installAndTrustRootCA();
}

/// The certificate's trust as a pill, in the same idiom as the Terminal
/// command: neutral while it is a next step, the warning tone only for a
/// half-finished authorization, green once trusted.
StatusPill rootCAStatusPill(
  BuildContext context,
  AppCopy copy,
  RootCAStatus status, {
  Key? key,
}) {
  final colors = context.viberColors;
  final (label, color, icon) = status.installed
      ? ('settings.root_ca.status.trusted', colors.verified, Icons.check)
      : !status.available
      ? ('settings.root_ca.status.unknown', colors.danger, Icons.error_outline)
      : status.certificatePresent == 'absent'
      ? (
          'settings.root_ca.status.not_installed',
          colors.textMuted,
          Icons.radio_button_unchecked,
        )
      : (
          'settings.root_ca.status.needs_trust',
          colors.warning,
          Icons.gpp_maybe_outlined,
        );
  return StatusPill(key: key, label: copy(label), color: color, icon: icon);
}

final class _RootCAUnavailable extends StatelessWidget {
  const _RootCAUnavailable({
    required this.message,
    required this.recovery,
    required this.retryLabel,
    required this.onRetry,
  });

  final String message;
  final String recovery;
  final String retryLabel;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      InlineNotice(message: message, error: true),
      const SizedBox(height: 6),
      Text(recovery, style: Theme.of(context).textTheme.bodySmall),
      const SizedBox(height: 8),
      OutlinedButton.icon(
        key: const Key('root-ca-retry'),
        onPressed: onRetry,
        icon: const Icon(Icons.refresh, size: 15),
        label: Text(retryLabel),
      ),
    ],
  );
}

final class _RootCAStatusBody extends StatelessWidget {
  const _RootCAStatusBody({
    required this.status,
    required this.copy,
    required this.mutating,
    required this.error,
    required this.onInstall,
    required this.onRemove,
    required this.onReplace,
    required this.onRestart,
    required this.restartRequired,
    required this.guideIntent,
    required this.canReplace,
    required this.onRetry,
  });

  final RootCAStatus status;
  final AppCopy copy;
  final bool mutating;
  final String? error;
  final VoidCallback onInstall;
  final VoidCallback onRemove;
  final VoidCallback onReplace;
  final VoidCallback onRestart;
  final bool restartRequired;
  final RootCAGuideIntent? guideIntent;
  final bool canReplace;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final muted = Theme.of(
      context,
    ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted);
    final replaceLabel = copy(
      status.certificatePresent == 'absent'
          ? 'settings.root_ca.replace_and_restart'
          : 'settings.root_ca.replace',
    );
    // The details open by themselves when a guide refers to them or the
    // trust status cannot be read.
    final openDetails = guideIntent != null || !status.available;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Align(
          alignment: Alignment.centerLeft,
          child: rootCAStatusPill(
            context,
            copy,
            status,
            key: const Key('root-ca-status'),
          ),
        ),
        if (status.available && !restartRequired) ...[
          const SizedBox(height: 10),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              if (status.installed)
                OutlinedButton.icon(
                  key: const Key('root-ca-remove'),
                  onPressed: mutating ? null : onRemove,
                  icon: const Icon(Icons.remove_circle_outline, size: 15),
                  label: Text(copy('settings.root_ca.remove')),
                )
              else
                OutlinedButton.icon(
                  key: const Key('root-ca-install'),
                  onPressed: mutating ? null : onInstall,
                  icon: mutating
                      ? const SizedBox.square(
                          dimension: 15,
                          child: CircularProgressIndicator(strokeWidth: 1.5),
                        )
                      : const Icon(Icons.verified_user_outlined, size: 15),
                  label: Text(
                    copy(
                      status.certificatePresent == 'present'
                          ? 'settings.root_ca.trust'
                          : 'settings.root_ca.install',
                    ),
                  ),
                ),
            ],
          ),
        ],
        if (status.available && restartRequired) ...[
          const SizedBox(height: 8),
          InlineNotice(
            message: copy('settings.root_ca.restart_required'),
            error: false,
          ),
          const SizedBox(height: 8),
          Align(
            alignment: Alignment.centerLeft,
            child: FilledButton.icon(
              key: const Key('root-ca-restart'),
              onPressed: mutating ? null : onRestart,
              icon: const Icon(Icons.restart_alt, size: 15),
              label: Text(copy('settings.root_ca.restart')),
            ),
          ),
        ],
        const SizedBox(height: 6),
        CompactDisclosure(
          key: ValueKey('root-ca-details-$openDetails'),
          toggleKey: const Key('root-ca-details-toggle'),
          label: copy('settings.root_ca.details'),
          initiallyExpanded: openDetails,
          child: Padding(
            key: const Key('root-ca-details'),
            padding: const EdgeInsets.only(left: 18, top: 4, bottom: 4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                CommandBlock(
                  label: copy('settings.root_ca.fingerprint'),
                  text: status.fingerprint,
                  fontSize: ViberType.utility,
                  enabled: !mutating,
                  copyKey: const Key('root-ca-copy-fingerprint'),
                  copyTooltip: copy('settings.root_ca.copy_fingerprint'),
                  copiedLabel: copy('common.copied'),
                  copyFailedLabel: copy('common.copy_failed'),
                ),
                const SizedBox(height: 6),
                Text(
                  copy.format('settings.root_ca.validity', {
                    'from': _rootCADate(status.notBefore),
                    'to': _rootCADate(status.notAfter),
                  }),
                  style: muted,
                ),
                const SizedBox(height: 3),
                Text(copy('settings.root_ca.private_key'), style: muted),
                const SizedBox(height: 3),
                Text(copy('settings.root_ca.proxy_ca'), style: muted),
                if (status.available && !restartRequired) ...[
                  const SizedBox(height: 9),
                  Align(
                    alignment: Alignment.centerLeft,
                    child: Tooltip(
                      message: canReplace
                          ? replaceLabel
                          : copy('settings.root_ca.active_captures'),
                      child: OutlinedButton.icon(
                        key: const Key('root-ca-replace'),
                        onPressed: mutating || !canReplace ? null : onReplace,
                        icon: const Icon(Icons.autorenew, size: 15),
                        label: Text(replaceLabel),
                      ),
                    ),
                  ),
                  if (!canReplace) ...[
                    const SizedBox(height: 6),
                    Text(
                      copy('settings.root_ca.active_captures'),
                      style: muted,
                    ),
                  ],
                ],
              ],
            ),
          ),
        ),
        if (!restartRequired && guideIntent != null) ...[
          const SizedBox(height: 8),
          InlineNotice(
            message: _rootCAGuideMessage(copy, guideIntent!),
            error: false,
          ),
          const SizedBox(height: 8),
          Align(
            alignment: Alignment.centerLeft,
            child: OutlinedButton.icon(
              key: const Key('root-ca-guide-check'),
              onPressed: mutating ? null : onRetry,
              icon: const Icon(Icons.refresh, size: 15),
              label: Text(copy('settings.root_ca.guide.check')),
            ),
          ),
        ],
        if (!status.available) ...[
          const SizedBox(height: 6),
          Text(copy('settings.root_ca.recovery'), style: muted),
        ],
        if (error != null) ...[
          const SizedBox(height: 8),
          InlineNotice(message: rootCAErrorMessage(copy, error!), error: true),
        ],
      ],
    );
  }

  String _rootCADate(DateTime value) {
    final local = value.toLocal();
    return '${local.year.toString().padLeft(4, '0')}-'
        '${local.month.toString().padLeft(2, '0')}-'
        '${local.day.toString().padLeft(2, '0')}';
  }
}

/// The message for a root certificate failure the controller reports.
String rootCAErrorMessage(AppCopy copy, String error) {
  if (error.contains('root_trust_install_not_confirmed')) {
    return copy('settings.root_ca.install.not_confirmed');
  }
  if (error.contains('root_trust_install_userCancelled')) {
    return copy('settings.root_ca.install.cancelled');
  }
  if (error.contains('root_trust_install_permissionDenied')) {
    return copy('settings.root_ca.install.denied');
  }
  if (error.contains('root_trust_install_')) {
    return copy('settings.root_ca.install.failed');
  }
  if (error.contains('root_reset_active_captures')) {
    return copy('settings.root_ca.active_captures');
  }
  if (error.contains('root_reset_requires_removal')) {
    return copy('settings.root_ca.guide.replace');
  }
  if (error.contains('root_trust_conflict')) {
    return copy('settings.root_ca.conflict');
  }
  return copy('settings.root_ca.recovery');
}

String _rootCAGuideMessage(AppCopy copy, RootCAGuideIntent intent) =>
    switch (intent) {
      RootCAGuideIntent.remove => copy('settings.root_ca.guide.remove'),
      RootCAGuideIntent.replace => copy('settings.root_ca.guide.replace'),
    };

final class _ServerAccessPanel extends StatefulWidget {
  const _ServerAccessPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ServerAccessPanel> createState() => _ServerAccessPanelState();
}

final class _ServerAccessPanelState extends State<_ServerAccessPanel> {
  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final access = controller.serverAccess;
    final guide = controller.connectionGuide;
    if (!guide.available) {
      return _SettingsSurface(
        key: const Key('server-runtime-access'),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('server.access.title'),
              style: Theme.of(context).textTheme.titleSmall,
            ),
            const SizedBox(height: 8),
            if (controller.serverManagementLoading)
              CompactLoadingMessage(label: copy('server.access.loading'))
            else ...[
              InlineNotice(message: copy('settings.server_connection.unknown')),
              if (controller.serverManagement)
                OutlinedButton.icon(
                  onPressed: () =>
                      unawaited(controller.refreshServerManagement()),
                  icon: const Icon(Icons.refresh, size: 15),
                  label: Text(copy('common.retry')),
                ),
            ],
          ],
        ),
      );
    }
    final encrypted = guide.security == ServerConnectionSecurity.https;
    return Column(
      key: const Key('server-runtime-access'),
      children: [
        Container(
          clipBehavior: Clip.antiAlias,
          decoration: BoxDecoration(
            color: context.viberColors.panelRaised,
            border: Border.all(color: context.viberColors.dividerSoft),
            borderRadius: ViberMetrics.surfaceRadius,
          ),
          child: IntrinsicHeight(
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Container(width: 4, color: context.viberColors.route),
                Expanded(
                  child: Padding(
                    padding: const EdgeInsets.fromLTRB(12, 11, 12, 12),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Icon(
                              Icons.dns_outlined,
                              size: 17,
                              color: context.viberColors.route,
                            ),
                            const SizedBox(width: 7),
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    copy('server.access.title'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                  const SizedBox(height: 2),
                                  SelectableText(
                                    controller.runtimeConnectTarget,
                                    style: monoStyle.copyWith(
                                      fontSize: ViberType.micro,
                                      color: context.viberColors.textMuted,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                            ContextHelpButton(
                              title: copy('server.access.title'),
                              message: copy('server.access.description'),
                              dismissLabel: copy('common.dismiss'),
                            ),
                            if (guide.available) ...[
                              const SizedBox(width: 8),
                              _AccessTransportBadge(
                                label: copy(
                                  encrypted
                                      ? 'server.access.transport.https'
                                      : 'server.access.transport.http',
                                ),
                                encrypted: encrypted,
                              ),
                            ],
                          ],
                        ),
                        const SizedBox(height: 9),
                        if (controller.serverManagementLoading &&
                            access == null)
                          CompactLoadingMessage(
                            label: copy('server.access.loading'),
                          )
                        else if (guide.available) ...[
                          if (!encrypted) ...[
                            InlineNotice(
                              message: copy(
                                guide.security ==
                                        ServerConnectionSecurity.loopbackHttp
                                    ? 'settings.server_connection.local_http'
                                    : 'server.access.http_warning',
                              ),
                              error:
                                  guide.security ==
                                  ServerConnectionSecurity.remoteHttp,
                            ),
                            const SizedBox(height: 8),
                          ],
                          _ServerAccessFact(
                            icon: Icons.login,
                            title: copy('server.access.session.title'),
                            detail: copy('server.access.session.detail'),
                          ),
                        ] else
                          OutlinedButton.icon(
                            onPressed: () =>
                                unawaited(controller.refreshServerManagement()),
                            icon: const Icon(Icons.refresh, size: 15),
                            label: Text(copy('common.retry')),
                          ),
                        if (controller.serverManagementError
                            case final error?) ...[
                          const SizedBox(height: 8),
                          InlineNotice(
                            message: copy.format('server.access.error', {
                              'detail': error,
                            }),
                            error: true,
                          ),
                        ],
                        if (guide.available) ...[
                          const SizedBox(height: 10),
                          Divider(
                            height: 1,
                            color: context.viberColors.dividerSoft,
                          ),
                          const SizedBox(height: 9),
                          Text(
                            copy('server.web.title'),
                            style: Theme.of(context).textTheme.labelLarge,
                          ),
                          const SizedBox(height: 3),
                          Text(
                            copy('server.web.detail'),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(
                                  color: context.viberColors.textMuted,
                                ),
                          ),
                          const SizedBox(height: 7),
                          _RunCommand(
                            client: 'Web',
                            command: controller.runtimeWebURL,
                            copyLabel: copy('server.web.copy'),
                            enabled: true,
                            copy: copy,
                          ),
                        ],
                        const SizedBox(height: 10),
                        Divider(
                          height: 1,
                          color: context.viberColors.dividerSoft,
                        ),
                        const SizedBox(height: 9),
                        Text(
                          copy('server.login.command.title'),
                          style: Theme.of(context).textTheme.labelLarge,
                        ),
                        const SizedBox(height: 3),
                        Text(
                          copy('server.login.command.detail'),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                        const SizedBox(height: 7),
                        _RunCommand(
                          client: copy('server.login.command.client'),
                          command:
                              'vibermate login --server ${controller.runtimeServerURL}',
                          copyLabel: copy('server.login.command.copy'),
                          enabled: guide.available,
                          copy: copy,
                        ),
                        const SizedBox(height: 10),
                        Text(
                          copy('server.run.title'),
                          style: Theme.of(context).textTheme.labelLarge,
                        ),
                        const SizedBox(height: 3),
                        Text(
                          copy('server.run.detail'),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                        const SizedBox(height: 7),
                        Column(
                          children: [
                            for (final (index, client) in const [
                              'claude',
                              'codex',
                            ].indexed) ...[
                              _RunCommand(
                                client: client == 'claude' ? 'Claude' : 'Codex',
                                command:
                                    'vibermate run --server ${controller.runtimeServerURL} -- $client',
                                copyLabel: copy.format('terminal.run.copy', {
                                  'client': client == 'claude'
                                      ? 'Claude'
                                      : 'Codex',
                                }),
                                enabled: guide.available,
                                copy: copy,
                              ),
                              if (index == 0) const SizedBox(height: 7),
                            ],
                          ],
                        ),
                      ],
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

final class _RuntimeUsersPanel extends StatefulWidget {
  const _RuntimeUsersPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_RuntimeUsersPanel> createState() => _RuntimeUsersPanelState();
}

final class _RuntimeUsersPanelState extends State<_RuntimeUsersPanel> {
  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final users = controller.runtimeUsers;
    final firstOwner = users != null && !users.any((user) => user.owner);
    final heading = Row(
      children: [
        Icon(Icons.group_outlined, size: 17, color: context.viberColors.route),
        const SizedBox(width: 7),
        Expanded(
          child: ContextHelpHeading(
            title: copy('settings.tab.users'),
            message: copy('server.users.description'),
            dismissLabel: copy('common.dismiss'),
          ),
        ),
      ],
    );
    final add = (firstOwner ? FilledButton.icon : OutlinedButton.icon)(
      key: const Key('runtime-user-add'),
      onPressed: controller.runtimeUserMutating || users == null
          ? null
          : () => _showCreateRuntimeUserDialog(firstOwner: firstOwner),
      icon: const Icon(Icons.person_add_alt_1, size: 15),
      label: Text(
        copy(firstOwner ? 'server.users.add_owner' : 'server.users.add'),
      ),
    );
    return _SettingsSurface(
      key: const Key('runtime-users-panel'),
      accent: firstOwner,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LayoutBuilder(
            builder: (context, constraints) {
              if (constraints.maxWidth < 560) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    heading,
                    const SizedBox(height: 8),
                    Align(alignment: Alignment.centerLeft, child: add),
                  ],
                );
              }
              return Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(child: heading),
                  const SizedBox(width: 12),
                  add,
                ],
              );
            },
          ),
          const SizedBox(height: 9),
          if (controller.serverManagementLoading && users == null)
            CompactLoadingMessage(label: copy('server.users.loading'))
          else if (users == null)
            Align(
              alignment: Alignment.centerLeft,
              child: OutlinedButton.icon(
                onPressed: () =>
                    unawaited(controller.refreshServerManagement()),
                icon: const Icon(Icons.refresh, size: 15),
                label: Text(copy('common.retry')),
              ),
            )
          else if (users.isEmpty)
            InlineNotice(message: copy('server.users.empty'))
          else
            Column(
              children: [
                for (final user in users) ...[
                  _RuntimeUserRow(
                    user: user,
                    copy: copy,
                    disabling: controller.runtimeUserMutating,
                    owner: user.owner,
                    onReset:
                        user.active &&
                            (!user.owner || controller.webPrincipal == null)
                        ? () => _showResetRuntimeUserDialog(user)
                        : null,
                    onDisable: !user.owner
                        ? () => _confirmDisableRuntimeUser(user)
                        : null,
                    onPolicy: () => _showRuntimeUserPolicyDialog(user),
                  ),
                  if (user != users.last) const SizedBox(height: 6),
                ],
              ],
            ),
          if (controller.serverManagementError case final error?) ...[
            const SizedBox(height: 8),
            InlineNotice(
              message: copy.format('server.users.error', {'detail': error}),
              error: true,
            ),
          ],
        ],
      ),
    );
  }

  Future<void> _showCreateRuntimeUserDialog({required bool firstOwner}) async {
    await showDialog<void>(
      context: context,
      builder: (_) => _CreateRuntimeUserDialog(
        controller: widget.controller,
        copy: widget.copy,
        firstOwner: firstOwner,
      ),
    );
  }

  Future<void> _confirmDisableRuntimeUser(RuntimeUser user) async {
    if (!user.active) {
      await widget.controller.setRuntimeUserEnabled(user.id, enabled: true);
      return;
    }
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: Text(widget.copy('server.users.disable.title')),
        content: Text(
          widget.copy.format('server.users.disable.detail', {
            'username': user.username,
          }),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(dialogContext).pop(false),
            child: Text(widget.copy('common.cancel')),
          ),
          FilledButton(
            onPressed: () => Navigator.of(dialogContext).pop(true),
            child: Text(widget.copy('server.users.disable.action')),
          ),
        ],
      ),
    );
    if (confirmed == true) {
      await widget.controller.setRuntimeUserEnabled(user.id, enabled: false);
    }
  }

  Future<void> _showResetRuntimeUserDialog(RuntimeUser user) async {
    await showDialog<void>(
      context: context,
      builder: (_) => _ResetRuntimeUserPasswordDialog(
        controller: widget.controller,
        copy: widget.copy,
        user: user,
      ),
    );
  }

  Future<void> _showRuntimeUserPolicyDialog(RuntimeUser user) async {
    await showDialog<void>(
      context: context,
      builder: (_) => _RuntimeUserPolicyDialog(
        controller: widget.controller,
        copy: widget.copy,
        user: user,
      ),
    );
  }
}

final class _RuntimeUserPolicyDialog extends StatefulWidget {
  const _RuntimeUserPolicyDialog({
    required this.controller,
    required this.copy,
    required this.user,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final RuntimeUser user;

  @override
  State<_RuntimeUserPolicyDialog> createState() =>
      _RuntimeUserPolicyDialogState();
}

final class _RuntimeUserPolicyDialogState
    extends State<_RuntimeUserPolicyDialog> {
  late bool _all;
  late final Set<String> _selected;
  late final TextEditingController _calls;
  late final TextEditingController _tokens;
  bool _saving = false;
  bool _failed = false;

  @override
  void initState() {
    super.initState();
    _all = widget.user.allEnvironments;
    _selected = widget.user.allowedEnvironmentIds.toSet();
    _calls = TextEditingController(
      text: widget.user.dailyAgentApiCallWarning == 0
          ? ''
          : '${widget.user.dailyAgentApiCallWarning}',
    );
    _tokens = TextEditingController(
      text: widget.user.dailyTokenWarning == 0
          ? ''
          : '${widget.user.dailyTokenWarning}',
    );
  }

  @override
  void dispose() {
    _calls.dispose();
    _tokens.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final records =
        widget.controller.data?.environments ?? const <EnvironmentRecord>[];
    final labels = {
      for (final value in records)
        value.id: environmentDisplayName(value, widget.copy),
    };
    final ids = {...labels.keys, ..._selected}.toList()..sort();
    final calls = int.tryParse(_calls.text.trim()) ?? 0;
    final tokens = int.tryParse(_tokens.text.trim()) ?? 0;
    final valid =
        !_saving &&
        calls >= 0 &&
        calls <= 1000000000000000 &&
        tokens >= 0 &&
        tokens <= 1000000000000000;
    return AlertDialog(
      title: Text(
        widget.copy.format('server.users.policy.title', {
          'username': widget.user.username,
        }),
      ),
      content: SizedBox(
        width: 520,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(widget.copy('server.users.policy.detail')),
              const SizedBox(height: 12),
              CheckboxListTile(
                key: const Key('runtime-user-policy-all'),
                contentPadding: EdgeInsets.zero,
                dense: true,
                value: _all,
                onChanged: _saving
                    ? null
                    : (value) => setState(() => _all = value ?? false),
                title: Text(widget.copy('server.users.policy.all')),
              ),
              if (!_all)
                Container(
                  constraints: const BoxConstraints(maxHeight: 230),
                  decoration: BoxDecoration(
                    border: Border.all(color: context.viberColors.dividerSoft),
                    borderRadius: ViberMetrics.controlRadius,
                  ),
                  child: ListView(
                    shrinkWrap: true,
                    children: [
                      for (final id in ids)
                        CheckboxListTile(
                          key: Key('runtime-user-policy-environment-$id'),
                          dense: true,
                          value: _selected.contains(id),
                          onChanged: _saving
                              ? null
                              : (value) => setState(() {
                                  if (value == true) {
                                    _selected.add(id);
                                  } else {
                                    _selected.remove(id);
                                  }
                                }),
                          title: Text(labels[id] ?? id),
                          subtitle: labels[id] == null ? null : Text(id),
                        ),
                    ],
                  ),
                ),
              if (!_all && _selected.isEmpty)
                Padding(
                  padding: const EdgeInsets.only(top: 6),
                  child: Text(
                    widget.copy('server.users.policy.none_detail'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.warning,
                    ),
                  ),
                ),
              const SizedBox(height: 14),
              Text(
                widget.copy('server.users.policy.alerts'),
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 4),
              Text(widget.copy('server.users.policy.alerts_detail')),
              const SizedBox(height: 8),
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      key: const Key('runtime-user-policy-calls'),
                      controller: _calls,
                      enabled: !_saving,
                      keyboardType: TextInputType.number,
                      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                      decoration: InputDecoration(
                        labelText: widget.copy('server.users.policy.calls'),
                        hintText: '0',
                      ),
                      onChanged: (_) => setState(() {}),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: TextField(
                      key: const Key('runtime-user-policy-tokens'),
                      controller: _tokens,
                      enabled: !_saving,
                      keyboardType: TextInputType.number,
                      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                      decoration: InputDecoration(
                        labelText: widget.copy('server.users.policy.tokens'),
                        hintText: '0',
                      ),
                      onChanged: (_) => setState(() {}),
                    ),
                  ),
                ],
              ),
              if (_failed) ...[
                const SizedBox(height: 8),
                InlineNotice(
                  message: widget.copy('server.users.policy.failed'),
                  error: true,
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _saving ? null : () => Navigator.of(context).pop(),
          child: Text(widget.copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('runtime-user-policy-save'),
          onPressed: valid ? () => _save(calls, tokens) : null,
          child: _saving
              ? const CompactProgressIndicator()
              : Text(widget.copy('common.save')),
        ),
      ],
    );
  }

  Future<void> _save(int calls, int tokens) async {
    setState(() {
      _saving = true;
      _failed = false;
    });
    final ids = _all ? <String>[] : (_selected.toList()..sort());
    final saved = await widget.controller.setRuntimeUserPolicy(
      user: widget.user,
      allEnvironments: _all,
      allowedEnvironmentIds: ids,
      dailyAgentApiCallWarning: calls,
      dailyTokenWarning: tokens,
    );
    if (!mounted) return;
    if (saved) {
      Navigator.of(context).pop();
    } else {
      setState(() {
        _saving = false;
        _failed = true;
      });
    }
  }
}

final class _CreateRuntimeUserDialog extends StatefulWidget {
  const _CreateRuntimeUserDialog({
    required this.controller,
    required this.copy,
    required this.firstOwner,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool firstOwner;

  @override
  State<_CreateRuntimeUserDialog> createState() =>
      _CreateRuntimeUserDialogState();
}

final class _CreateRuntimeUserDialogState
    extends State<_CreateRuntimeUserDialog> {
  final _username = TextEditingController();
  final _password = TextEditingController();
  final _confirmPassword = TextEditingController();
  bool _busy = false;
  String? _error;

  bool get _complete =>
      validRuntimeUsernameInput(_username.text) &&
      _password.text.length >= 8 &&
      (!widget.firstOwner || _password.text == _confirmPassword.text);

  @override
  void dispose() {
    _username.dispose();
    _password.dispose();
    _confirmPassword.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      key: const Key('runtime-user-dialog'),
      title: Text(
        widget.copy(
          widget.firstOwner
              ? 'server.users.dialog.owner_title'
              : 'server.users.dialog.title',
        ),
      ),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              widget.copy(
                widget.firstOwner
                    ? 'server.users.dialog.owner_detail'
                    : 'server.users.dialog.detail',
              ),
            ),
            const SizedBox(height: 12),
            CompactLabeledControl(
              label: widget.copy('server.users.dialog.username'),
              child: TextField(
                key: const Key('runtime-user-username'),
                controller: _username,
                enabled: !_busy,
                autofocus: true,
                autocorrect: false,
                enableSuggestions: false,
                textInputAction: TextInputAction.next,
                decoration: InputDecoration(
                  helperText: widget.copy('server.login.username_help'),
                ),
                onChanged: (_) => setState(() => _error = null),
              ),
            ),
            const SizedBox(height: 10),
            CompactLabeledControl(
              label: widget.copy('server.users.dialog.password'),
              child: TextField(
                key: const Key('runtime-user-password'),
                controller: _password,
                enabled: !_busy,
                obscureText: true,
                autocorrect: false,
                enableSuggestions: false,
                textInputAction: widget.firstOwner
                    ? TextInputAction.next
                    : TextInputAction.done,
                decoration: InputDecoration(
                  helperText: widget.copy('server.users.dialog.password_help'),
                ),
                onChanged: (_) => setState(() => _error = null),
                onSubmitted: !widget.firstOwner && _complete && !_busy
                    ? (_) => unawaited(_submit())
                    : null,
              ),
            ),
            if (widget.firstOwner) ...[
              const SizedBox(height: 10),
              CompactLabeledControl(
                label: widget.copy('server.login.confirm_password'),
                child: TextField(
                  key: const Key('runtime-user-confirm-password'),
                  controller: _confirmPassword,
                  enabled: !_busy,
                  obscureText: true,
                  autocorrect: false,
                  enableSuggestions: false,
                  textInputAction: TextInputAction.done,
                  decoration: InputDecoration(),
                  onChanged: (_) => setState(() => _error = null),
                  onSubmitted: _complete && !_busy
                      ? (_) => unawaited(_submit())
                      : null,
                ),
              ),
            ],
            if (_error case final error?) ...[
              const SizedBox(height: 10),
              InlineNotice(message: error, error: true),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: _busy ? null : () => Navigator.of(context).pop(),
          child: Text(widget.copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('runtime-user-create'),
          onPressed: !_complete || _busy ? null : () => unawaited(_submit()),
          child: _busy
              ? const CompactProgressIndicator()
              : Text(
                  widget.copy(
                    widget.firstOwner
                        ? 'server.users.dialog.create_owner'
                        : 'server.users.dialog.create',
                  ),
                ),
        ),
      ],
    );
  }

  Future<void> _submit() async {
    if (!_complete || _busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    final created = await widget.controller.createRuntimeUser(
      username: _username.text.trim(),
      password: _password.text,
    );
    if (!mounted) return;
    if (created) {
      Navigator.of(context).pop();
      return;
    }
    setState(() {
      _busy = false;
      _error = widget.copy.format('server.users.dialog.error', {
        'detail':
            widget.controller.serverManagementError ??
            widget.copy('server.users.dialog.unknown'),
      });
    });
  }
}

final class _ResetRuntimeUserPasswordDialog extends StatefulWidget {
  const _ResetRuntimeUserPasswordDialog({
    required this.controller,
    required this.copy,
    required this.user,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final RuntimeUser user;

  @override
  State<_ResetRuntimeUserPasswordDialog> createState() =>
      _ResetRuntimeUserPasswordDialogState();
}

final class _ResetRuntimeUserPasswordDialogState
    extends State<_ResetRuntimeUserPasswordDialog> {
  final _password = TextEditingController();
  final _confirm = TextEditingController();
  bool _visible = false;
  bool _busy = false;
  String? _error;

  bool get _complete =>
      _password.text.length >= 8 && _password.text == _confirm.text;

  @override
  void dispose() {
    _password.dispose();
    _confirm.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    key: const Key('runtime-user-password-dialog'),
    title: Text(
      widget.copy(
        widget.user.owner
            ? 'server.users.password.owner_title'
            : 'server.users.password.title',
      ),
    ),
    content: ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 420),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            widget.copy.format(
              widget.user.owner
                  ? 'server.users.password.owner_detail'
                  : 'server.users.password.detail',
              {'username': widget.user.username},
            ),
          ),
          const SizedBox(height: 12),
          CompactLabeledControl(
            label: widget.copy('account.password.new'),
            child: TextField(
              key: const Key('runtime-user-new-password'),
              controller: _password,
              autofocus: true,
              enabled: !_busy,
              obscureText: !_visible,
              autocorrect: false,
              enableSuggestions: false,
              autofillHints: const [AutofillHints.newPassword],
              decoration: InputDecoration(
                suffixIcon: IconButton(
                  onPressed: () => setState(() => _visible = !_visible),
                  icon: Icon(
                    _visible
                        ? Icons.visibility_off_outlined
                        : Icons.visibility_outlined,
                  ),
                ),
              ),
              onChanged: (_) => setState(() => _error = null),
            ),
          ),
          const SizedBox(height: 10),
          CompactLabeledControl(
            label: widget.copy('account.password.confirm'),
            child: TextField(
              key: const Key('runtime-user-confirm-password'),
              controller: _confirm,
              enabled: !_busy,
              obscureText: !_visible,
              autocorrect: false,
              enableSuggestions: false,
              autofillHints: const [AutofillHints.newPassword],
              decoration: InputDecoration(),
              onChanged: (_) => setState(() => _error = null),
              onSubmitted: _complete ? (_) => unawaited(_submit()) : null,
            ),
          ),
          if (_error case final error?) ...[
            const SizedBox(height: 10),
            InlineNotice(message: error, error: true),
          ],
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: _busy ? null : () => Navigator.of(context).pop(),
        child: Text(widget.copy('common.cancel')),
      ),
      ListenableBuilder(
        listenable: Listenable.merge([_password, _confirm]),
        builder: (context, _) => FilledButton(
          key: const Key('runtime-user-password-save'),
          onPressed: _busy || !_complete ? null : () => unawaited(_submit()),
          child: _busy
              ? const CompactProgressIndicator()
              : Text(widget.copy('server.users.password.action')),
        ),
      ),
    ],
  );

  Future<void> _submit() async {
    if (!_complete || _busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    final changed = await widget.controller.replaceRuntimeUserPassword(
      userId: widget.user.id,
      password: _password.text,
    );
    if (!mounted) return;
    if (changed) {
      Navigator.of(context).pop();
      return;
    }
    setState(() {
      _busy = false;
      _error = widget.copy('server.users.password.error');
    });
  }
}

final class _AccessTransportBadge extends StatelessWidget {
  const _AccessTransportBadge({required this.label, required this.encrypted});

  final String label;
  final bool encrypted;

  @override
  Widget build(BuildContext context) {
    final color = encrypted
        ? context.viberColors.verified
        : context.viberColors.danger;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.08),
        border: Border.all(color: color.withValues(alpha: 0.55)),
        borderRadius: BorderRadius.circular(999),
      ),
      child: Text(
        label,
        style: Theme.of(context).textTheme.labelSmall?.copyWith(color: color),
      ),
    );
  }
}

final class _ServerAccessFact extends StatelessWidget {
  const _ServerAccessFact({
    required this.icon,
    required this.title,
    required this.detail,
  });

  final IconData icon;
  final String title;
  final String detail;

  @override
  Widget build(BuildContext context) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(icon, size: 16, color: context.viberColors.verified),
        const SizedBox(width: 7),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(title, style: Theme.of(context).textTheme.labelLarge),
              const SizedBox(height: 2),
              Text(
                detail,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

final class _RuntimeUserRow extends StatelessWidget {
  const _RuntimeUserRow({
    required this.user,
    required this.copy,
    required this.disabling,
    required this.owner,
    required this.onReset,
    required this.onDisable,
    required this.onPolicy,
  });

  final RuntimeUser user;
  final AppCopy copy;
  final bool disabling;
  final bool owner;
  final VoidCallback? onReset;
  final VoidCallback? onDisable;
  final VoidCallback onPolicy;

  @override
  Widget build(BuildContext context) {
    return Container(
      key: Key('runtime-user-row-${user.id}'),
      padding: const EdgeInsets.fromLTRB(9, 7, 7, 7),
      decoration: BoxDecoration(
        color: context.viberColors.rail,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                user.active ? Icons.person_outline : Icons.person_off_outlined,
                size: 16,
                color: user.active
                    ? context.viberColors.verified
                    : context.viberColors.textFaint,
              ),
              const SizedBox(width: 7),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      user.username,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodyMedium,
                    ),
                    if (owner) ...[
                      const SizedBox(height: 2),
                      Text(
                        copy('server.users.owner'),
                        style: Theme.of(context).textTheme.labelSmall?.copyWith(
                          color: context.viberColors.route,
                        ),
                      ),
                    ],
                    const SizedBox(height: 2),
                    Text(
                      copy('server.users.authentication'),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                    const SizedBox(height: 2),
                    Text(
                      copy.format('server.users.policy.summary', {
                        // The Owner configures every policy and holds all of them.
                        'access': user.owner || user.allEnvironments
                            ? copy('server.users.policy.all_short')
                            : user.allowedEnvironmentIds.isEmpty
                            ? copy('server.users.policy.none_short')
                            : copy.format('server.users.policy.count', {
                                'count': '${user.allowedEnvironmentIds.length}',
                              }),
                        'alerts':
                            user.dailyAgentApiCallWarning == 0 &&
                                user.dailyTokenWarning == 0
                            ? copy('server.users.policy.alerts_off')
                            : copy('server.users.policy.alerts_on'),
                      }),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 6),
              IconButton(
                key: Key('runtime-user-policy-${user.id}'),
                onPressed: disabling ? null : onPolicy,
                tooltip: copy('server.users.policy.action'),
                icon: const Icon(Icons.policy_outlined, size: 15),
                constraints: const BoxConstraints.tightFor(
                  width: ViberMetrics.controlHeight,
                  height: ViberMetrics.controlHeight,
                ),
                padding: EdgeInsets.zero,
              ),
              Text(
                copy(
                  user.active
                      ? 'server.users.state.active'
                      : 'server.users.state.disabled',
                ),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: user.active
                      ? context.viberColors.verified
                      : context.viberColors.textFaint,
                ),
              ),
              if (onReset != null) ...[
                const SizedBox(width: 3),
                IconButton(
                  key: Key('runtime-user-reset-${user.id}'),
                  onPressed: disabling ? null : onReset,
                  tooltip: copy('server.users.password.action'),
                  icon: const Icon(Icons.password_outlined, size: 15),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
              ],
              if (onDisable != null) ...[
                const SizedBox(width: 6),
                IconButton(
                  onPressed: disabling ? null : onDisable,
                  tooltip: copy(
                    user.active
                        ? 'server.users.disable.action'
                        : 'server.users.enable.action',
                  ),
                  icon: Icon(
                    user.active ? Icons.block : Icons.person_add_alt_1_outlined,
                    size: 15,
                  ),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
              ],
            ],
          ),
        ],
      ),
    );
  }
}

/// One copyable command, keyed by the client it starts.
final class _RunCommand extends StatelessWidget {
  const _RunCommand({
    required this.client,
    required this.command,
    required this.copyLabel,
    required this.enabled,
    required this.copy,
  });

  final String client;
  final String command;
  final String copyLabel;
  final bool enabled;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => CommandBlock(
    text: command,
    copyTooltip: copyLabel,
    copiedLabel: copy('common.copied'),
    copyFailedLabel: copy('common.copy_failed'),
    enabled: enabled,
    copyKey: Key('managed-run-copy-${client.toLowerCase()}'),
  );
}

/// _StorageDisclosure satisfies INV-STORE-DISCLOSED. It states where evidence
/// is written, how long it is kept, that credential header values are removed
/// before the write, and that the database is not encrypted at rest. The last
/// one is the point: the product would rather disclose an absence than imply
/// a protection it does not provide.
final class _StorageDisclosure extends StatelessWidget {
  const _StorageDisclosure({required this.copy, required this.controller});

  final AppCopy copy;
  final WorkbenchController controller;

  @override
  Widget build(BuildContext context) {
    final body = Theme.of(
      context,
    ).textTheme.bodyMedium?.copyWith(color: context.viberColors.textMuted);
    return _SettingsSurface(
      key: const Key('storage-disclosure-panel'),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.storage_outlined,
                size: 17,
                color: context.viberColors.route,
              ),
              const SizedBox(width: 8),
              Expanded(child: _SettingsLabel(copy('settings.storage'))),
              IconButton(
                key: const Key('storage-refresh'),
                tooltip: copy('settings.storage.refresh'),
                onPressed: controller.storageLocationLoading
                    ? null
                    : controller.refreshStorageLocation,
                icon: controller.storageLocationLoading
                    ? const CompactProgressIndicator()
                    : const Icon(Icons.refresh, size: 16),
              ),
              ContextHelpButton(
                message: copy('settings.storage.move_hint'),
                title: copy('settings.storage'),
                dismissLabel: copy('common.dismiss'),
              ),
            ],
          ),
          const SizedBox(height: 8),
          if (controller.storageLocation case final location?) ...[
            for (final entry in [
              (copy('settings.storage.directory'), location.dataDirectory),
              (copy('settings.storage.database'), location.databasePath),
            ]) ...[
              CommandBlock(
                label: entry.$1,
                text: entry.$2,
                copyTooltip: copy.format('common.copy', {'field': entry.$1}),
                copiedLabel: copy('common.copied'),
                copyFailedLabel: copy('common.copy_failed'),
              ),
              const SizedBox(height: 8),
            ],
            _StorageCapacity(
              current: location,
              previous: controller.previousStorageLocation,
              copy: copy,
            ),
            const SizedBox(height: 10),
          ] else if (controller.storageLocationFailed)
            Wrap(
              crossAxisAlignment: WrapCrossAlignment.center,
              spacing: 8,
              children: [
                Text(copy('settings.storage.location_failed'), style: body),
                TextButton(
                  onPressed: controller.refreshStorageLocation,
                  child: Text(copy('common.retry')),
                ),
              ],
            )
          else
            const CompactProgressIndicator(),
          if (!controller.terminalManagement && !controller.previewMode) ...[
            Text(copy('settings.storage.server_path'), style: body),
            const SizedBox(height: 8),
            if (controller.storageLocation case final location?)
              _ServerBackupGuide(location: location, copy: copy),
            const SizedBox(height: 8),
          ],
          if (controller.moveStorage != null ||
              controller.backupStorage != null ||
              controller.restoreStorage != null) ...[
            Wrap(
              spacing: 8,
              runSpacing: 6,
              children: [
                if (controller.moveStorage != null &&
                    controller.chooseStorageDirectory != null)
                  TextButton.icon(
                    key: const Key('storage-change-directory'),
                    onPressed:
                        controller.storageMoving ||
                            controller.storageLocation == null
                        ? null
                        : () => _chooseStorage(context),
                    icon: const Icon(Icons.drive_file_move_outline, size: 16),
                    label: Text(copy('settings.storage.change')),
                  ),
                if (controller.backupStorage != null &&
                    controller.chooseStorageBackupDirectory != null)
                  TextButton.icon(
                    key: const Key('storage-create-backup'),
                    onPressed: controller.storageMoving
                        ? null
                        : () => _chooseBackup(context),
                    icon: const Icon(Icons.archive_outlined, size: 16),
                    label: Text(copy('settings.storage.backup')),
                  ),
                if (controller.restoreStorage != null &&
                    controller.chooseStorageRestore != null)
                  TextButton.icon(
                    key: const Key('storage-restore-backup'),
                    onPressed: controller.storageMoving
                        ? null
                        : () => _chooseRestore(context),
                    icon: const Icon(Icons.restore_page_outlined, size: 16),
                    label: Text(copy('settings.storage.restore')),
                  ),
              ],
            ),
            if (controller.storageMoveFailure ?? controller.storageMoveNotice
                case final message?)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 8),
                child: Text(copy(message), style: body),
              ),
            const SizedBox(height: 8),
          ],
          for (final line in const [
            'settings.storage.not_encrypted',
            'settings.storage.credentials',
            'settings.storage.location',
            'settings.storage.retention',
          ]) ...[Text(copy(line), style: body), const SizedBox(height: 5)],
          if (controller.storageLocation case final location?) ...[
            const SizedBox(height: 7),
            _StorageCleanup(
              location: location,
              controller: controller,
              copy: copy,
            ),
          ],
          const SizedBox(height: 7),
          // Design 06 section 8.2 makes clearing a distinct, deliberate and
          // confirmable action rather than a side effect of stopping or
          // uninstalling, which is why it lives here behind its own confirmation
          // instead of anywhere a user could reach it by accident.
          Align(
            alignment: Alignment.centerLeft,
            child: OutlinedButton.icon(
              key: const Key('storage-clear-archive'),
              onPressed: controller.storageArchivePreviewLoading
                  ? null
                  : () async {
                      final preview = await controller
                          .loadEvidenceClearPreview();
                      if (preview == null || !context.mounted) return;
                      final outcome = await showDialog<DeletionOutcome>(
                        context: context,
                        builder: (_) => DeletionConfirmation(
                          copy: copy,
                          title: copy('deletion.archive.title'),
                          subject: copy('settings.storage'),
                          consequence: _archiveConsequence(preview),
                          onConfirm: () async {
                            final result = await controller.clearEvidence();
                            if (result == null) {
                              throw controller.inventoryFailure;
                            }
                            return result;
                          },
                        ),
                      );
                      if (outcome == null) return;
                    },
              icon: controller.storageArchivePreviewLoading
                  ? const CompactProgressIndicator()
                  : const Icon(Icons.delete_sweep_outlined, size: 15),
              label: Text(copy('deletion.archive.title')),
            ),
          ),
          if (controller.storageArchivePreviewError case final error?)
            Text(
              copy.maybe(error) ?? copy('error.control_result_unknown'),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.danger,
              ),
            ),
        ],
      ),
    );
  }

  String _archiveConsequence(DeletionReleased preview) {
    return '${copy('deletion.archive.consequence')}\n\n'
        '${copy.format(preview.countsIncomplete ? 'settings.storage.archive_preview_partial' : 'settings.storage.archive_preview', {'captures': preview.captures, 'exchanges': preview.exchanges, 'envelopes': preview.envelopes})}';
  }

  Future<void> _chooseStorage(BuildContext context) async {
    final target = await controller.pickStorageDirectory();
    if (target == null || !context.mounted) return;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(copy('settings.storage.change')),
        content: SizedBox(
          width: 520,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('settings.storage.new_directory'),
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                const SizedBox(height: 6),
                SelectableText(target, style: monoStyle),
                const SizedBox(height: 18),
                Text(copy('settings.storage.move_confirmation')),
              ],
            ),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: Text(copy('common.cancel')),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: Text(copy('settings.storage.move_confirm')),
          ),
        ],
      ),
    );
    if (confirmed == true) await controller.relocateStorage(target);
  }

  Future<void> _chooseBackup(BuildContext context) async {
    final target = await controller.pickStorageBackupDirectory();
    if (target == null || !context.mounted) return;
    final confirmed = await _confirmStorageOperation(
      context,
      title: copy('settings.storage.backup_title'),
      paths: [(copy('settings.storage.backup_target'), target)],
      consequence: copy('settings.storage.backup_confirmation'),
      action: copy('settings.storage.backup_confirm'),
    );
    if (confirmed) await controller.createStorageBackup(target);
  }

  Future<void> _chooseRestore(BuildContext context) async {
    final selection = await controller.pickStorageRestore();
    if (selection == null || !context.mounted) return;
    final confirmed = await _confirmStorageOperation(
      context,
      title: copy('settings.storage.restore_title'),
      paths: [
        (copy('settings.storage.restore_source'), selection.backup),
        (copy('settings.storage.restore_target'), selection.target),
      ],
      consequence: copy('settings.storage.restore_confirmation'),
      action: copy('settings.storage.restore_confirm'),
    );
    if (confirmed) await controller.restoreStorageBackup(selection);
  }

  Future<bool> _confirmStorageOperation(
    BuildContext context, {
    required String title,
    required List<(String, String)> paths,
    required String consequence,
    required String action,
  }) async {
    return await showDialog<bool>(
          context: context,
          builder: (context) => AlertDialog(
            title: Text(title),
            content: SizedBox(
              width: 520,
              child: SingleChildScrollView(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    for (final (label, value) in paths) ...[
                      Text(
                        label,
                        style: Theme.of(context).textTheme.labelLarge,
                      ),
                      const SizedBox(height: 5),
                      SelectableText(value, style: monoStyle),
                      const SizedBox(height: 12),
                    ],
                    Text(consequence),
                  ],
                ),
              ),
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: Text(copy('common.cancel')),
              ),
              FilledButton(
                onPressed: () => Navigator.pop(context, true),
                child: Text(action),
              ),
            ],
          ),
        ) ??
        false;
  }
}

final class _ServerBackupGuide extends StatelessWidget {
  const _ServerBackupGuide({required this.location, required this.copy});

  final RuntimeStorageLocation location;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final backup =
        'vibermated backup-data --source=${_shellQuote(location.dataDirectory)} '
        "--target='/absolute/new-backup-directory'";
    final restore =
        "vibermated restore-data --source='/absolute/backup-directory' "
        "--target='/absolute/new-data-directory'";
    return Column(
      key: const Key('storage-server-backup-guide'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          copy('settings.storage.server_backup'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 6),
        for (final (index, command) in [backup, restore].indexed) ...[
          if (index > 0) const SizedBox(height: 6),
          CommandBlock(
            text: command,
            copyTooltip: copy('settings.storage.copy_command'),
            copiedLabel: copy('common.copied'),
            copyFailedLabel: copy('common.copy_failed'),
          ),
        ],
      ],
    );
  }
}

String _shellQuote(String value) {
  final escaped = value.replaceAll("'", "'\"'\"'");
  return "'$escaped'";
}

final class _StorageCapacity extends StatelessWidget {
  const _StorageCapacity({
    required this.current,
    required this.previous,
    required this.copy,
  });

  final RuntimeStorageLocation current;
  final RuntimeStorageLocation? previous;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final prior = previous;
    final delta = prior == null ? null : current.fileBytes - prior.fileBytes;
    return Column(
      key: const Key('storage-capacity'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        ResponsiveFormGrid(
          children: [
            _StorageMetric(
              label: copy('settings.storage.database_size'),
              value: _storageBytes(current.databaseBytes),
            ),
            _StorageMetric(
              label: copy('settings.storage.wal_size'),
              value: _storageBytes(current.walBytes),
            ),
            _StorageMetric(
              label: copy('settings.storage.evidence_size'),
              value: _storageBytes(current.evidenceBytes),
            ),
            _StorageMetric(
              label: copy('settings.storage.reusable_size'),
              value: _storageBytes(current.reusableBytes),
            ),
          ],
        ),
        const SizedBox(height: 7),
        Text(
          copy.format('settings.storage.capacity_sample', {
            'time': _storageTimestamp(current.collectedAt),
            'available': current.filesystemAvailableBytes == null
                ? copy('settings.storage.unknown')
                : _storageBytes(current.filesystemAvailableBytes!),
            'threshold': _storageBytes(current.lowSpaceThresholdBytes),
          }),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        if (delta != null && delta != 0)
          Text(
            copy.format('settings.storage.growth', {
              'change': '${delta > 0 ? '+' : '−'}${_storageBytes(delta.abs())}',
            }),
            key: const Key('storage-growth'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        if (current.capacityState == 'low') ...[
          const SizedBox(height: 7),
          InlineNotice(
            message: copy('settings.storage.low_space'),
            error: true,
          ),
        ] else if (current.capacityState == 'unavailable') ...[
          const SizedBox(height: 7),
          InlineNotice(message: copy('settings.storage.capacity_unavailable')),
        ],
        const SizedBox(height: 5),
        Text(
          copy('settings.storage.measurement_scope'),
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: context.viberColors.textMuted),
        ),
      ],
    );
  }
}

final class _StorageMetric extends StatelessWidget {
  const _StorageMetric({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Semantics(
    container: true,
    label: '$label: $value',
    child: ExcludeSemantics(
      child: CompactLabeledControl(
        label: label,
        child: SelectableText(value, style: monoStyle),
      ),
    ),
  );
}

final class _StorageCleanup extends StatelessWidget {
  const _StorageCleanup({
    required this.location,
    required this.controller,
    required this.copy,
  });

  final RuntimeStorageLocation location;
  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final preview = location.cleanupPreview;
    final pending = preview.exchanges + preview.envelopes;
    final summary = preview.cleanupPending
        ? copy('settings.storage.cleanup_pending')
        : preview.countsIncomplete
        ? copy.format('settings.storage.cleanup_preview_partial', {
            'exchanges': preview.exchanges,
            'envelopes': preview.envelopes,
          })
        : pending == 0
        ? copy('settings.storage.cleanup_empty')
        : copy.format('settings.storage.cleanup_preview', {
            'exchanges': preview.exchanges,
            'envelopes': preview.envelopes,
          });
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          summary,
          key: const Key('storage-cleanup-preview'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
        if (pending > 0 || preview.countsIncomplete || preview.cleanupPending)
          TextButton.icon(
            key: const Key('storage-cleanup-expired'),
            onPressed:
                controller.storageCleanupRunning || preview.cleanupPending
                ? null
                : () => showDialog<DeletionOutcome>(
                    context: context,
                    builder: (_) => DeletionConfirmation(
                      copy: copy,
                      title: copy('settings.storage.cleanup_title'),
                      subject: summary,
                      consequence: copy('settings.storage.cleanup_consequence'),
                      confirmLabel: copy('settings.storage.cleanup_action'),
                      onConfirm: controller.cleanupExpiredEvidence,
                    ),
                  ),
            icon: const Icon(Icons.cleaning_services_outlined, size: 15),
            label: Text(copy('settings.storage.cleanup_action')),
          ),
        if (controller.storageCleanupNotice case final notice?)
          Text(copy(notice), style: Theme.of(context).textTheme.bodySmall),
        if (controller.storageCleanupError case final error?)
          Text(
            copy.maybe(error) ?? copy('error.control_result_unknown'),
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(color: context.viberColors.danger),
          ),
      ],
    );
  }
}

String _storageBytes(int value) {
  if (value < 1024) return '$value B';
  if (value < 1024 * 1024) return '${(value / 1024).toStringAsFixed(1)} KiB';
  if (value < 1024 * 1024 * 1024) {
    return '${(value / (1024 * 1024)).toStringAsFixed(1)} MiB';
  }
  return '${(value / (1024 * 1024 * 1024)).toStringAsFixed(1)} GiB';
}

String _storageTimestamp(DateTime value) {
  final local = value.toLocal();
  String two(int number) => number.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}:${two(local.second)}';
}

final class _SettingsLabel extends StatelessWidget {
  const _SettingsLabel(this.label);

  final String label;

  @override
  Widget build(BuildContext context) =>
      Text(label, style: Theme.of(context).textTheme.titleMedium);
}

final class _PreferenceControl extends StatelessWidget {
  const _PreferenceControl({required this.label, required this.child});

  final String label;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 210,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: Theme.of(context).textTheme.titleSmall),
          const SizedBox(height: 5),
          Align(alignment: Alignment.centerLeft, child: child),
        ],
      ),
    );
  }
}

final class _TerminalCommandPanel extends StatelessWidget {
  const _TerminalCommandPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final status = controller.terminalCommand;
    final error = controller.terminalCommandError;
    final notice = controller.terminalCommandNotice;
    return Container(
      key: const Key('terminal-command-panel'),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(11, 10, 10, 9),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Container(
                  width: ViberMetrics.controlHeight,
                  height: ViberMetrics.controlHeight,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: context.viberColors.route.withValues(alpha: 0.09),
                    border: Border.all(
                      color: context.viberColors.route.withValues(alpha: 0.3),
                    ),
                    borderRadius: ViberMetrics.controlRadius,
                  ),
                  child: Icon(
                    Icons.terminal,
                    size: 15,
                    color: context.viberColors.route,
                  ),
                ),
                const SizedBox(width: 9),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      ContextHelpHeading(
                        title: copy('terminal.title'),
                        message: copy('terminal.description'),
                        dismissLabel: copy('common.dismiss'),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                IconButton(
                  key: const Key('terminal-command-refresh'),
                  tooltip: copy('terminal.refresh_status'),
                  onPressed:
                      controller.terminalCommandLoading ||
                          controller.terminalCommandMutating
                      ? null
                      : () => unawaited(
                          controller.refreshTerminalCommand(checkShell: true),
                        ),
                  icon: controller.terminalCommandLoading
                      ? const SizedBox.square(
                          dimension: 13,
                          child: CircularProgressIndicator(strokeWidth: 1.4),
                        )
                      : const Icon(Icons.refresh, size: 15),
                  constraints: const BoxConstraints.tightFor(
                    width: ViberMetrics.controlHeight,
                    height: ViberMetrics.controlHeight,
                  ),
                  padding: EdgeInsets.zero,
                ),
              ],
            ),
          ),
          const Divider(height: 1),
          if (status == null && controller.terminalCommandLoading)
            const SizedBox(
              height: 54,
              child: Center(
                child: SizedBox.square(
                  dimension: 16,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                ),
              ),
            )
          else if (status != null)
            _TerminalCommandStatusView(
              controller: controller,
              copy: copy,
              status: status,
            )
          else
            Padding(
              padding: const EdgeInsets.all(10),
              child: Text(
                copy('terminal.unavailable'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          if (error != null)
            InlineNotice(
              message: copy(error),
              error: true,
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
          if (notice != null)
            InlineNotice(
              message: copy(notice),
              onDismiss: controller.clearTerminalCommandMessage,
              dismissLabel: copy('common.dismiss'),
            ),
        ],
      ),
    );
  }
}

final class _TerminalCommandStatusView extends StatelessWidget {
  const _TerminalCommandStatusView({
    required this.controller,
    required this.copy,
    required this.status,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  Widget build(BuildContext context) {
    final lookup = status.ready ? status.shellFindsCommand : null;
    // Set up, but a new Terminal window does not find it: a warning, not
    // 'Ready', until the person's shell looks in ~/.local/bin.
    final notOnPath =
        status.state == TerminalCommandState.current &&
        lookup == ShellCommandLookup.notFound;
    return Padding(
      padding: const EdgeInsets.fromLTRB(11, 9, 11, 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LayoutBuilder(
            builder: (context, constraints) {
              final narrow = constraints.maxWidth < 560;
              final pill = StatusPill(
                key: const Key('terminal-command-state'),
                label: copy(
                  notOnPath
                      ? 'terminal.state.not_on_path'
                      : 'terminal.state.${status.state.wireName}',
                ),
                color: notOnPath
                    ? context.viberColors.warning
                    : _stateColor(context, status.state),
                icon: notOnPath ? Icons.search_off : _stateIcon(status.state),
              );
              final detail = Text(
                copy(
                  notOnPath
                      ? 'terminal.state_detail.not_on_path'
                      : 'terminal.state_detail.${status.state.wireName}',
                ),
                style: Theme.of(context).textTheme.bodyMedium,
              );
              // The longer warning sits above its explanation.
              final summary = notOnPath
                  ? Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [pill, const SizedBox(height: 6), detail],
                    )
                  : Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        pill,
                        const SizedBox(width: 9),
                        Expanded(
                          child: Padding(
                            padding: const EdgeInsets.only(top: 1),
                            child: detail,
                          ),
                        ),
                      ],
                    );
              final actions = _TerminalCommandActions(
                controller: controller,
                copy: copy,
                status: status,
              );
              if (narrow) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [summary, const SizedBox(height: 9), actions],
                );
              }
              return Row(
                crossAxisAlignment: CrossAxisAlignment.center,
                children: [
                  Expanded(child: summary),
                  const SizedBox(width: 16),
                  actions,
                ],
              );
            },
          ),
          if (lookup == ShellCommandLookup.notFound) ...[
            const SizedBox(height: 9),
            TerminalPathHint(status: status, copy: copy),
          ] else if (lookup == ShellCommandLookup.unknown) ...[
            const SizedBox(height: 9),
            TerminalPathFallback(status: status, copy: copy),
          ],
          const SizedBox(height: 3),
          _TerminalCommandDetails(
            key: ValueKey('terminal-command-details-${status.state.wireName}'),
            copy: copy,
            status: status,
          ),
        ],
      ),
    );
  }
}

final class _TerminalCommandDetails extends StatefulWidget {
  const _TerminalCommandDetails({
    required this.copy,
    required this.status,
    super.key,
  });

  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  State<_TerminalCommandDetails> createState() =>
      _TerminalCommandDetailsState();
}

final class _TerminalCommandDetailsState
    extends State<_TerminalCommandDetails> {
  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final status = widget.status;
    return CompactDisclosure(
      toggleKey: const Key('terminal-command-details-toggle'),
      label: copy('terminal.details'),
      child: Container(
        key: const Key('terminal-command-technical-details'),
        padding: const EdgeInsets.fromLTRB(9, 8, 9, 9),
        decoration: BoxDecoration(
          color: context.viberColors.rail,
          border: Border.all(color: context.viberColors.dividerSoft),
          borderRadius: ViberMetrics.surfaceRadius,
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _CommandPath(
              label: copy('terminal.source_build'),
              value: status.sourceBuild,
            ),
            if (status.installedBuild case final installed?) ...[
              const SizedBox(height: 4),
              _CommandPath(
                label: copy('terminal.installed_build'),
                value: installed,
                muted: installed != status.sourceBuild,
              ),
            ],
            const SizedBox(height: 4),
            _CommandPath(
              label: copy('terminal.target'),
              value: status.targetPath,
            ),
            const SizedBox(height: 4),
            _CommandPath(
              label: copy('terminal.source'),
              value: status.sourcePath,
              muted: true,
            ),
            if (status.detail case final detail?) ...[
              const SizedBox(height: 4),
              _CommandPath(
                label: copy('terminal.diagnosis'),
                value: detail,
                muted: true,
              ),
            ],
            const SizedBox(height: 7),
            Text(
              copy('terminal.boundary'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            if (status.state == TerminalCommandState.unownedTarget ||
                status.state == TerminalCommandState.conflict) ...[
              const SizedBox(height: 5),
              Text(
                copy('terminal.manual_resolution'),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.warning,
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

final class _CommandPath extends StatelessWidget {
  const _CommandPath({
    required this.label,
    required this.value,
    this.muted = false,
  });

  final String label;
  final String value;
  final bool muted;

  @override
  Widget build(BuildContext context) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          width: 53,
          child: Text(
            label,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
              fontSize: ViberType.utility,
            ),
          ),
        ),
        Expanded(
          child: SelectableText(
            value,
            style: monoStyle.copyWith(
              color: muted
                  ? context.viberColors.textMuted
                  : context.viberColors.text,
              fontSize: ViberType.utility,
            ),
          ),
        ),
      ],
    );
  }
}

final class _TerminalCommandActions extends StatelessWidget {
  const _TerminalCommandActions({
    required this.controller,
    required this.copy,
    required this.status,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final TerminalCommandStatus status;

  @override
  Widget build(BuildContext context) {
    final busy = controller.terminalCommandMutating;
    return Wrap(
      alignment: WrapAlignment.end,
      spacing: 7,
      runSpacing: 7,
      children: [
        if (status.canInstall)
          FilledButton.icon(
            key: const Key('terminal-command-install'),
            onPressed: busy
                ? null
                : () => confirmTerminalCommandChange(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.install,
                  ),
            icon: const Icon(Icons.add_link, size: 14),
            label: Text(copy('terminal.action.install')),
          ),
        if (status.canRefresh)
          FilledButton.icon(
            key: const Key('terminal-command-refresh-installation'),
            onPressed: busy
                ? null
                : () => confirmTerminalCommandChange(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.refresh,
                  ),
            icon: const Icon(Icons.sync, size: 14),
            label: Text(copy('terminal.action.refresh')),
          ),
        if (status.canRepair)
          FilledButton.icon(
            key: const Key('terminal-command-repair'),
            onPressed: busy
                ? null
                : () => confirmTerminalCommandChange(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.repair,
                  ),
            icon: const Icon(Icons.build_outlined, size: 14),
            label: Text(copy('terminal.action.repair')),
          ),
        if (status.canRemove &&
            status.state != TerminalCommandState.targetMissing)
          OutlinedButton.icon(
            key: const Key('terminal-command-remove'),
            onPressed: busy
                ? null
                : () => confirmTerminalCommandChange(
                    context,
                    controller,
                    copy,
                    status,
                    TerminalCommandOperation.remove,
                  ),
            icon: const Icon(Icons.link_off, size: 14),
            label: Text(copy('terminal.action.remove')),
          ),
      ],
    );
  }
}

/// Asks before setting up, updating, repairing or removing the Terminal
/// command, then runs the operation. Settings and the setup guide share it.
Future<void> confirmTerminalCommandChange(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
  TerminalCommandStatus status,
  TerminalCommandOperation operation,
) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      key: const Key('terminal-command-confirmation'),
      title: Text(copy('terminal.confirm.${operation.wireName}.title')),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 430),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy.format('terminal.confirm.${operation.wireName}.detail', {
                'target': status.targetPath,
              }),
            ),
            const SizedBox(height: 10),
            Text(
              copy(
                operation == TerminalCommandOperation.remove
                    ? 'terminal.confirm.remove.boundary'
                    : 'terminal.confirm.boundary',
              ),
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, false),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('terminal-command-confirm-action'),
          onPressed: () => Navigator.pop(context, true),
          child: Text(copy('terminal.action.${operation.wireName}')),
        ),
      ],
    ),
  );
  if (confirmed == true) {
    await controller.changeTerminalCommand(operation);
  }
}

Color _stateColor(BuildContext context, TerminalCommandState state) =>
    switch (state) {
      TerminalCommandState.current => context.viberColors.verified,
      TerminalCommandState.notInstalled => context.viberColors.textFaint,
      TerminalCommandState.sourceUpdated ||
      TerminalCommandState.targetMissing => context.viberColors.warning,
      _ => context.viberColors.danger,
    };

IconData _stateIcon(TerminalCommandState state) => switch (state) {
  TerminalCommandState.current => Icons.check_circle_outline,
  // A state, not a second 'Set up' button: a hollow circle.
  TerminalCommandState.notInstalled => Icons.radio_button_unchecked,
  TerminalCommandState.sourceUpdated => Icons.update,
  TerminalCommandState.targetMissing => Icons.link_off,
  _ => Icons.error_outline,
};

/// Whether an address belongs to the Server machine itself, which is also how
/// every client arrives through a local tunnel such as frp or cloudflared.
bool _loopbackAddress(String address) {
  try {
    if (address.contains(':')) {
      final bytes = Uri.parseIPv6Address(address);
      return bytes.take(15).every((byte) => byte == 0) && bytes[15] == 1;
    }
    return Uri.parseIPv4Address(address)[0] == 127;
  } on FormatException {
    return false;
  }
}

/// Whether an address is private, shared or local, which is what a browser
/// appears to come from behind a load balancer, NAT gateway or Docker Desktop.
bool _sharedAddress(String address) {
  try {
    if (address.contains(':')) {
      final bytes = Uri.parseIPv6Address(address);
      return (bytes[0] & 0xfe) == 0xfc || // fc00::/7 unique local
          (bytes[0] == 0xfe && (bytes[1] & 0xc0) == 0x80); // fe80::/10
    }
    final bytes = Uri.parseIPv4Address(address);
    return bytes[0] == 10 ||
        (bytes[0] == 172 && bytes[1] >= 16 && bytes[1] < 32) ||
        (bytes[0] == 192 && bytes[1] == 168) ||
        (bytes[0] == 100 && bytes[1] >= 64 && bytes[1] < 128) || // CGNAT
        (bytes[0] == 169 && bytes[1] == 254);
  } on FormatException {
    return false;
  }
}

final class _ExitFacts {
  const _ExitFacts({
    required this.builtIn,
    required this.name,
    required this.revision,
    required this.connection,
    required this.connectionDetail,
    required this.dns,
    required this.dnsDetail,
    required this.isDefault,
    required this.newerThanDefault,
    required this.defaultRevision,
  });

  final bool builtIn;
  final String name;
  final String revision;
  final String connection;
  final String? connectionDetail;
  final String dns;
  final String? dnsDetail;
  final bool isDefault;
  final bool newerThanDefault;
  final int? defaultRevision;
}

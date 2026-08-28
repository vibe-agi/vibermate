import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/bootstrap/terminal_command.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'offline_hold_view.dart';
import '../../core/api/control_models.dart';
import 'deletion_dialog.dart';
import 'egress_profile_editor.dart';
import 'workbench_controller.dart';

final class SettingsView extends StatelessWidget {
  const SettingsView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final server = controller.serverManagement;
    return DefaultTabController(
      length: server ? 3 : 2,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          PageHeading(
            title: copy('settings.title'),
            subtitle: copy(
              server ? 'settings.subtitle.server' : 'settings.subtitle',
            ),
          ),
          Material(
            color: context.viberColors.panel,
            child: Align(
              alignment: Alignment.centerLeft,
              child: TabBar(
                isScrollable: true,
                tabAlignment: TabAlignment.start,
                dividerHeight: 0,
                tabs: [
                  Tab(
                    key: const Key('settings-tab-general'),
                    text: copy('settings.tab.general'),
                  ),
                  if (server)
                    Tab(
                      key: const Key('settings-tab-users'),
                      text: copy('settings.tab.users'),
                    ),
                  Tab(
                    key: const Key('settings-tab-proxy'),
                    text: copy('settings.tab.proxy'),
                  ),
                ],
              ),
            ),
          ),
          const Divider(height: 1),
          Expanded(
            child: TabBarView(
              children: [
                _GeneralSettingsPane(controller: controller, copy: copy),
                if (server)
                  _RuntimeUsersSettingsPane(controller: controller, copy: copy),
                _EgressProfilesSettingsPane(controller: controller, copy: copy),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

final class _EgressProfilesSettingsPane extends StatefulWidget {
  const _EgressProfilesSettingsPane({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_EgressProfilesSettingsPane> createState() =>
      _EgressProfilesSettingsPaneState();
}

final class _EgressProfilesSettingsPaneState
    extends State<_EgressProfilesSettingsPane> {
  List<EgressProfileRevision> _profiles = const [];
  bool _loading = true;
  bool _saving = false;
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
      final catalog = await widget.controller.egressProfiles();
      if (!mounted) return;
      final profiles = [...catalog.items]
        ..sort((left, right) => left.displayName.compareTo(right.displayName));
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
  Widget build(BuildContext context) => ListView(
    key: const Key('egress-profiles-settings-scroll'),
    padding: const EdgeInsets.fromLTRB(14, 12, 14, 20),
    children: [
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  copy('settings.egress.title'),
                  style: Theme.of(context).textTheme.titleMedium,
                ),
                const SizedBox(height: 2),
                Text(
                  copy('settings.egress.detail'),
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
          ),
          const SizedBox(width: 10),
          FilledButton.icon(
            key: const Key('egress-profile-add'),
            onPressed: _saving ? null : () => unawaited(_edit()),
            icon: const Icon(Icons.add, size: 15),
            label: Text(copy('settings.egress.add')),
          ),
        ],
      ),
      const SizedBox(height: 12),
      if (_loading)
        const Center(
          child: Padding(
            padding: EdgeInsets.all(28),
            child: CircularProgressIndicator(),
          ),
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
            OutlinedButton.icon(
              onPressed: _load,
              icon: const Icon(Icons.refresh, size: 15),
              label: Text(copy('common.retry')),
            ),
          ],
        )
      else
        Container(
          clipBehavior: Clip.antiAlias,
          decoration: BoxDecoration(
            border: Border.all(color: context.viberColors.divider),
            borderRadius: ViberMetrics.surfaceRadius,
          ),
          child: Column(
            children: [
              for (var index = 0; index < _profiles.length; index++) ...[
                if (index > 0) const Divider(height: 1),
                _profileRow(_profiles[index]),
              ],
            ],
          ),
        ),
    ],
  );

  Widget _profileRow(EgressProfileRevision profile) {
    final builtIn = profile.id == EgressProfileRevision.direct.id;
    return ListTile(
      key: Key('egress-profile-row-${profile.id}'),
      dense: true,
      leading: Icon(
        builtIn ? Icons.language_rounded : Icons.alt_route_rounded,
        size: 17,
        color: context.viberColors.route,
      ),
      title: Text(
        egressProfileSummary(copy, profile),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: builtIn
          ? Text(copy('settings.egress.builtin'))
          : Text(
              egressPolicySummary(copy, profile.policy),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
      trailing: builtIn
          ? null
          : IconButton(
              key: Key('egress-profile-edit-${profile.id}'),
              onPressed: _saving ? null : () => unawaited(_edit(profile)),
              tooltip: copy('common.edit'),
              icon: const Icon(Icons.edit_outlined, size: 17),
            ),
    );
  }

  Future<void> _edit([EgressProfileRevision? profile]) async {
    final draft = await showEgressProfileEditor(
      context: context,
      copy: copy,
      initial: profile,
    );
    if (draft == null || !mounted) return;
    setState(() => _saving = true);
    try {
      if (profile == null) {
        await widget.controller.createEgressProfile(
          displayName: draft.displayName,
          policy: draft.policy,
        );
      } else {
        await widget.controller.publishEgressProfile(
          id: profile.id,
          expectedRevision: profile.revision,
          displayName: draft.displayName,
          policy: draft.policy,
        );
      }
      if (!mounted) return;
      setState(() => _saving = false);
      await _load();
    } on Object {
      if (!mounted) return;
      setState(() {
        _saving = false;
        _failed = true;
      });
    }
  }
}

final class _GeneralSettingsPane extends StatelessWidget {
  const _GeneralSettingsPane({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => ListView(
    key: const Key('settings-scroll'),
    padding: const EdgeInsets.fromLTRB(14, 12, 14, 20),
    children: [
      if (controller.preferenceWarning case final warning?) ...[
        InlineNotice(
          key: const Key('preferences-warning'),
          message: copy(warning),
          error: true,
        ),
        const SizedBox(height: 10),
      ],
      Wrap(
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
              selected: controller.language,
              onSelected: controller.setLanguage,
            ),
          ),
        ],
      ),
      const SizedBox(height: 14),
      const Divider(height: 1),
      const SizedBox(height: 14),
      OfflineHoldSettingsPanel(controller: controller, copy: copy),
      if (controller.terminalManagement) ...[
        const SizedBox(height: 14),
        _TerminalCommandPanel(controller: controller, copy: copy),
        const SizedBox(height: 9),
        _ManagedRunGuide(copy: copy, status: controller.terminalCommand),
      ],
      const SizedBox(height: 18),
      _StorageDisclosure(copy: copy, controller: controller),
      const SizedBox(height: 18),
      _SettingsLabel(copy('settings.runtime')),
      const SizedBox(height: 7),
      Row(
        children: [
          Icon(Icons.memory, size: 15, color: context.viberColors.route),
          const SizedBox(width: 7),
          Expanded(
            child: Text(
              controller.previewMode
                  ? copy('settings.preview')
                  : controller.serverManagement
                  ? copy.format('settings.remote', {
                      'target': controller.runtimeConnectTarget,
                    })
                  : copy('settings.live'),
              style: Theme.of(context).textTheme.bodyMedium,
            ),
          ),
        ],
      ),
    ],
  );
}

final class _RuntimeUsersSettingsPane extends StatelessWidget {
  const _RuntimeUsersSettingsPane({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => ListView(
    key: const Key('runtime-users-settings-scroll'),
    padding: const EdgeInsets.fromLTRB(14, 12, 14, 20),
    children: [_ServerAccessPanel(controller: controller, copy: copy)],
  );
}

final class _ServerAccessPanel extends StatefulWidget {
  const _ServerAccessPanel({required this.controller, required this.copy});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ServerAccessPanel> createState() => _ServerAccessPanelState();
}

final class _ServerAccessPanelState extends State<_ServerAccessPanel> {
  String? _copiedClient;
  bool _copyFailed = false;

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final access = controller.serverAccess;
    final users = controller.runtimeUsers;
    return Container(
      key: const Key('server-runtime-access'),
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
                                style: Theme.of(context).textTheme.titleSmall,
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
                        if (access != null) ...[
                          const SizedBox(width: 8),
                          _AccessTransportBadge(
                            label: copy(
                              access.encrypted
                                  ? 'server.access.transport.https'
                                  : 'server.access.transport.http',
                            ),
                            encrypted: access.encrypted,
                          ),
                        ],
                      ],
                    ),
                    const SizedBox(height: 5),
                    Text(
                      copy('server.access.description'),
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                    const SizedBox(height: 9),
                    if (controller.serverManagementLoading && access == null)
                      CompactLoadingMessage(
                        label: copy('server.access.loading'),
                      )
                    else if (access != null) ...[
                      if (!access.encrypted) ...[
                        InlineNotice(
                          message: copy('server.access.http_warning'),
                          error: true,
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
                    if (controller.serverManagementError case final error?) ...[
                      const SizedBox(height: 8),
                      InlineNotice(
                        message: copy.format('server.access.error', {
                          'detail': error,
                        }),
                        error: true,
                      ),
                    ],
                    const SizedBox(height: 10),
                    Divider(height: 1, color: context.viberColors.dividerSoft),
                    const SizedBox(height: 9),
                    Row(
                      children: [
                        Expanded(
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(
                                copy('server.users.title'),
                                style: Theme.of(context).textTheme.labelLarge,
                              ),
                              const SizedBox(height: 2),
                              Text(
                                copy('server.users.description'),
                                style: Theme.of(context).textTheme.bodySmall
                                    ?.copyWith(
                                      color: context.viberColors.textMuted,
                                    ),
                              ),
                            ],
                          ),
                        ),
                        const SizedBox(width: 8),
                        OutlinedButton.icon(
                          key: const Key('runtime-user-add'),
                          onPressed: controller.runtimeUserMutating
                              ? null
                              : _showCreateRuntimeUserDialog,
                          icon: const Icon(Icons.person_add_alt_1, size: 15),
                          label: Text(copy('server.users.add')),
                        ),
                      ],
                    ),
                    const SizedBox(height: 8),
                    if (controller.serverManagementLoading && users == null)
                      CompactLoadingMessage(label: copy('server.users.loading'))
                    else if (users == null)
                      OutlinedButton.icon(
                        onPressed: () =>
                            unawaited(controller.refreshServerManagement()),
                        icon: const Icon(Icons.refresh, size: 15),
                        label: Text(copy('common.retry')),
                      )
                    else if (users.isEmpty)
                      Text(
                        copy('server.users.empty'),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.textMuted,
                        ),
                      )
                    else
                      Column(
                        children: [
                          for (final user in users) ...[
                            _RuntimeUserRow(
                              user: user,
                              copy: copy,
                              disabling: controller.runtimeUserMutating,
                              onDisable: user.active
                                  ? () => _confirmDisableRuntimeUser(user)
                                  : null,
                            ),
                            if (user != users.last) const SizedBox(height: 6),
                          ],
                        ],
                      ),
                    const SizedBox(height: 10),
                    Divider(height: 1, color: context.viberColors.dividerSoft),
                    const SizedBox(height: 9),
                    Text(
                      copy('server.login.command.title'),
                      style: Theme.of(context).textTheme.labelLarge,
                    ),
                    const SizedBox(height: 3),
                    Text(
                      copy('server.login.command.detail'),
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
                    ),
                    const SizedBox(height: 7),
                    _RunCommand(
                      client: copy('server.login.command.client'),
                      command:
                          'vibermate login --server ${controller.runtimeConnectTarget}',
                      copyLabel: copy('server.login.command.copy'),
                      enabled: access != null,
                      onCopy: () => _copyCommand(
                        copy('server.login.command.client'),
                        'vibermate login --server ${controller.runtimeConnectTarget}',
                      ),
                    ),
                    const SizedBox(height: 10),
                    Text(
                      copy('server.run.title'),
                      style: Theme.of(context).textTheme.labelLarge,
                    ),
                    const SizedBox(height: 3),
                    Text(
                      copy('server.run.detail'),
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: context.viberColors.textMuted,
                      ),
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
                                'vibermate run --server ${controller.runtimeConnectTarget} -- $client',
                            copyLabel: copy.format('terminal.run.copy', {
                              'client': client == 'claude' ? 'Claude' : 'Codex',
                            }),
                            enabled: access != null,
                            onCopy: () => _copyCommand(
                              client == 'claude' ? 'Claude' : 'Codex',
                              'vibermate run --server ${controller.runtimeConnectTarget} -- $client',
                            ),
                          ),
                          if (index == 0) const SizedBox(height: 7),
                        ],
                      ],
                    ),
                    if (_copiedClient case final client?) ...[
                      const SizedBox(height: 6),
                      Text(
                        copy.format('terminal.run.copied', {'client': client}),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.verified,
                        ),
                      ),
                    ] else if (_copyFailed) ...[
                      const SizedBox(height: 6),
                      Text(
                        copy('terminal.run.copy_failed'),
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: context.viberColors.danger,
                        ),
                      ),
                    ],
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _copyCommand(String client, String command) async {
    try {
      await Clipboard.setData(ClipboardData(text: command));
      if (!mounted) return;
      setState(() {
        _copiedClient = client;
        _copyFailed = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _copiedClient = null;
        _copyFailed = true;
      });
    }
  }

  Future<void> _showCreateRuntimeUserDialog() async {
    await showDialog<void>(
      context: context,
      builder: (_) => _CreateRuntimeUserDialog(
        controller: widget.controller,
        copy: widget.copy,
      ),
    );
  }

  Future<void> _confirmDisableRuntimeUser(RuntimeUser user) async {
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
      await widget.controller.disableRuntimeUser(user.id);
    }
  }
}

final class _CreateRuntimeUserDialog extends StatefulWidget {
  const _CreateRuntimeUserDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_CreateRuntimeUserDialog> createState() =>
      _CreateRuntimeUserDialogState();
}

final class _CreateRuntimeUserDialogState
    extends State<_CreateRuntimeUserDialog> {
  final _username = TextEditingController();
  final _password = TextEditingController();
  bool _busy = false;
  String? _error;

  bool get _complete =>
      _username.text.trim().isNotEmpty && _password.text.length >= 8;

  @override
  void dispose() {
    _username.dispose();
    _password.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      key: const Key('runtime-user-dialog'),
      title: Text(widget.copy('server.users.dialog.title')),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(widget.copy('server.users.dialog.detail')),
            const SizedBox(height: 12),
            TextField(
              key: const Key('runtime-user-username'),
              controller: _username,
              enabled: !_busy,
              autofocus: true,
              autocorrect: false,
              enableSuggestions: false,
              textInputAction: TextInputAction.next,
              decoration: InputDecoration(
                labelText: widget.copy('server.users.dialog.username'),
              ),
              onChanged: (_) => setState(() => _error = null),
            ),
            const SizedBox(height: 10),
            TextField(
              key: const Key('runtime-user-password'),
              controller: _password,
              enabled: !_busy,
              obscureText: true,
              autocorrect: false,
              enableSuggestions: false,
              textInputAction: TextInputAction.done,
              decoration: InputDecoration(
                labelText: widget.copy('server.users.dialog.password'),
                helperText: widget.copy('server.users.dialog.password_help'),
              ),
              onChanged: (_) => setState(() => _error = null),
              onSubmitted: _complete && !_busy
                  ? (_) => unawaited(_submit())
                  : null,
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
        FilledButton(
          key: const Key('runtime-user-create'),
          onPressed: !_complete || _busy ? null : () => unawaited(_submit()),
          child: _busy
              ? const CompactProgressIndicator()
              : Text(widget.copy('server.users.dialog.create')),
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
    required this.onDisable,
  });

  final RuntimeUser user;
  final AppCopy copy;
  final bool disabling;
  final VoidCallback? onDisable;

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
                    const SizedBox(height: 2),
                    Text(
                      copy('server.users.authentication'),
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
              if (onDisable != null) ...[
                const SizedBox(width: 6),
                IconButton(
                  onPressed: disabling ? null : onDisable,
                  tooltip: copy('server.users.disable.action'),
                  icon: const Icon(Icons.block, size: 15),
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

final class _ManagedRunGuide extends StatefulWidget {
  const _ManagedRunGuide({required this.copy, required this.status});

  final AppCopy copy;
  final TerminalCommandStatus? status;

  @override
  State<_ManagedRunGuide> createState() => _ManagedRunGuideState();
}

final class _ManagedRunGuideState extends State<_ManagedRunGuide> {
  String? _copied;
  bool _copyFailed = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final state = widget.status?.state;
    final enabled =
        state == TerminalCommandState.current ||
        state == TerminalCommandState.sourceUpdated;
    return Container(
      key: const Key('managed-run-guide'),
      padding: const EdgeInsets.fromLTRB(11, 9, 10, 10),
      decoration: BoxDecoration(
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.play_arrow_outlined,
                size: 15,
                color: context.viberColors.verified,
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  copy('terminal.run.title'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              if (_copied case final value?)
                Semantics(
                  liveRegion: true,
                  child: Text(
                    copy.format('terminal.run.copied', {'client': value}),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.verified,
                    ),
                  ),
                )
              else if (_copyFailed)
                Semantics(
                  liveRegion: true,
                  child: Text(
                    copy('terminal.run.copy_failed'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.danger,
                    ),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 4),
          Text(
            copy(
              enabled ? 'terminal.run.detail' : 'terminal.run.install_first',
            ),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 7),
          Wrap(
            spacing: 7,
            runSpacing: 7,
            children: [
              _RunCommand(
                client: 'Claude',
                command: 'vibermate run -- claude',
                copyLabel: copy.format('terminal.run.copy', {
                  'client': 'Claude',
                }),
                enabled: enabled,
                onCopy: () => _copy('Claude', 'vibermate run -- claude'),
              ),
              _RunCommand(
                client: 'Codex',
                command: 'vibermate run -- codex',
                copyLabel: copy.format('terminal.run.copy', {
                  'client': 'Codex',
                }),
                enabled: enabled,
                onCopy: () => _copy('Codex', 'vibermate run -- codex'),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            copy('terminal.run.environment'),
            style: monoStyle.copyWith(
              fontSize: ViberType.micro,
              color: context.viberColors.textFaint,
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _copy(String client, String command) async {
    try {
      await Clipboard.setData(ClipboardData(text: command));
      if (!mounted) return;
      setState(() {
        _copied = client;
        _copyFailed = false;
      });
    } on Object {
      if (!mounted) return;
      setState(() {
        _copied = null;
        _copyFailed = true;
      });
    }
  }
}

final class _RunCommand extends StatelessWidget {
  const _RunCommand({
    required this.client,
    required this.command,
    required this.copyLabel,
    required this.enabled,
    required this.onCopy,
  });

  final String client;
  final String command;
  final String copyLabel;
  final bool enabled;
  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      constraints: const BoxConstraints(minHeight: ViberMetrics.controlHeight),
      decoration: BoxDecoration(
        color: context.viberColors.rail,
        border: Border.all(color: context.viberColors.divider),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Expanded(
            child: Padding(
              padding: const EdgeInsets.only(left: 8, right: 5),
              child: Text(
                command,
                softWrap: true,
                style: monoStyle.copyWith(
                  fontSize: ViberType.utility,
                  color: enabled
                      ? context.viberColors.text
                      : context.viberColors.textFaint,
                ),
              ),
            ),
          ),
          IconButton(
            key: Key('managed-run-copy-${client.toLowerCase()}'),
            onPressed: enabled ? onCopy : null,
            tooltip: copyLabel,
            icon: const Icon(Icons.copy, size: 12),
            constraints: const BoxConstraints.tightFor(
              width: ViberMetrics.controlHeight,
              height: ViberMetrics.controlHeight,
            ),
            padding: EdgeInsets.zero,
          ),
        ],
      ),
    );
  }
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
    return Column(
      key: const Key('storage-disclosure-panel'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _SettingsLabel(copy('settings.storage')),
        const SizedBox(height: 7),
        for (final line in const [
          'settings.storage.not_encrypted',
          'settings.storage.credentials',
          'settings.storage.location',
          'settings.storage.retention',
        ]) ...[Text(copy(line), style: body), const SizedBox(height: 5)],
        const SizedBox(height: 7),
        // Design 06 section 8.2 makes clearing a distinct, deliberate and
        // confirmable action rather than a side effect of stopping or
        // uninstalling, which is why it lives here behind its own confirmation
        // instead of anywhere a user could reach it by accident.
        Align(
          alignment: Alignment.centerLeft,
          child: OutlinedButton.icon(
            key: const Key('storage-clear-archive'),
            onPressed: () async {
              final outcome = await showDialog<DeletionOutcome>(
                context: context,
                builder: (_) => DeletionConfirmation(
                  copy: copy,
                  title: copy('deletion.archive.title'),
                  subject: copy('settings.storage'),
                  consequence: copy('deletion.archive.consequence'),
                  onConfirm: () async {
                    final result = await controller.clearEvidence();
                    if (result == null) {
                      throw StateError(
                        controller.inventoryError ?? 'archive clear failed',
                      );
                    }
                    return result;
                  },
                ),
              );
              if (outcome == null) return;
            },
            icon: const Icon(Icons.delete_sweep_outlined, size: 15),
            label: Text(copy('deletion.archive.title')),
          ),
        ),
      ],
    );
  }
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
                      Text(
                        copy('terminal.title'),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      const SizedBox(height: 2),
                      Text(
                        copy('terminal.description'),
                        style: Theme.of(context).textTheme.bodySmall,
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
                      : () => unawaited(controller.refreshTerminalCommand()),
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
    return Padding(
      padding: const EdgeInsets.fromLTRB(11, 9, 11, 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LayoutBuilder(
            builder: (context, constraints) {
              final narrow = constraints.maxWidth < 560;
              final summary = Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  StatusPill(
                    label: copy('terminal.state.${status.state.wireName}'),
                    color: _stateColor(context, status.state),
                    icon: _stateIcon(status.state),
                  ),
                  const SizedBox(width: 9),
                  Expanded(
                    child: Padding(
                      padding: const EdgeInsets.only(top: 1),
                      child: Text(
                        copy('terminal.state_detail.${status.state.wireName}'),
                        style: Theme.of(context).textTheme.bodyMedium,
                      ),
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
  bool _expanded = false;

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final status = widget.status;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Semantics(
          button: true,
          expanded: _expanded,
          child: InkWell(
            key: const Key('terminal-command-details-toggle'),
            borderRadius: ViberMetrics.controlRadius,
            onTap: () => setState(() => _expanded = !_expanded),
            child: SizedBox(
              height: ViberMetrics.controlHeight,
              child: Row(
                children: [
                  Icon(
                    _expanded ? Icons.expand_more : Icons.chevron_right,
                    size: 15,
                    color: context.viberColors.textFaint,
                  ),
                  const SizedBox(width: 3),
                  Text(
                    copy('terminal.details'),
                    style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      color: context.viberColors.textMuted,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
        if (_expanded)
          Container(
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
      ],
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
                : () => _confirm(
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
                : () => _confirm(
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
                : () => _confirm(
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
                : () => _confirm(
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

Future<void> _confirm(
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
              copy('terminal.confirm.boundary'),
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
  TerminalCommandState.notInstalled => Icons.remove_circle_outline,
  TerminalCommandState.sourceUpdated => Icons.update,
  TerminalCommandState.targetMissing => Icons.link_off,
  _ => Icons.error_outline,
};

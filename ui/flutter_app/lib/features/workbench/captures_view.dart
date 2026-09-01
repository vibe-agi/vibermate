import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api/control_models.dart';
import '../../core/design/agent_identity.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'capture_conversation_tree.dart';
import 'conversation_timeline.dart';
import 'deletion_dialog.dart';
import 'workbench_controller.dart';

final class CapturesView extends StatefulWidget {
  const CapturesView({required this.controller, required this.copy, super.key});

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<CapturesView> createState() => _CapturesViewState();
}

final class _CapturesViewState extends State<CapturesView> {
  final TextEditingController _filterController = TextEditingController();
  String _filter = '';
  bool _narrowDetail = false;
  bool _confirmRevoke = false;
  bool _masterVisible = true;
  double _masterWidth = ViberMetrics.masterPaneWidth;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final narrow = constraints.maxWidth < 760;
        final master = _CaptureMaster(
          controller: widget.controller,
          copy: widget.copy,
          filter: _filter,
          filterController: _filterController,
          onFilter: (value) => setState(() => _filter = value),
          onClearFilter: () => setState(() {
            _filterController.clear();
            _filter = '';
          }),
          onCreateManual: () => _openCreateManualCapture(context),
          onSelect: (key) {
            unawaited(widget.controller.selectCapture(key));
            if (narrow) setState(() => _narrowDetail = true);
          },
        );
        final detail = _CaptureDetail(
          controller: widget.controller,
          copy: widget.copy,
          showBack: narrow,
          confirmRevoke: _confirmRevoke,
          onBack: () => setState(() {
            _narrowDetail = false;
            _confirmRevoke = false;
          }),
          onConfirmRevoke: (value) => setState(() => _confirmRevoke = value),
          onRotateManual: () => _openRotateManualCapture(context),
          onDeleteCapture: () => _confirmDeleteCapture(context),
          masterVisible: _masterVisible,
          onToggleMaster: narrow
              ? null
              : () => setState(() => _masterVisible = !_masterVisible),
        );
        if (narrow) {
          return AnimatedSwitcher(
            duration: const Duration(milliseconds: 140),
            child: _narrowDetail && widget.controller.selectedCapture != null
                ? KeyedSubtree(
                    key: const ValueKey('capture-detail'),
                    child: detail,
                  )
                : KeyedSubtree(
                    key: const ValueKey('capture-master'),
                    child: master,
                  ),
          );
        }
        final maxWidth = math.min(
          ViberMetrics.masterPaneMaxWidth,
          constraints.maxWidth * 0.45,
        );
        final masterWidth = _masterWidth
            .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
            .toDouble();
        return Row(
          children: [
            if (_masterVisible) ...[
              SizedBox(
                key: const Key('capture-master-pane'),
                width: masterWidth,
                child: master,
              ),
              WorkbenchPaneDivider(
                key: const Key('capture-master-divider'),
                label: widget.copy('common.resize_directory'),
                onDrag: (delta) => setState(() {
                  _masterWidth = (_masterWidth + delta)
                      .clamp(ViberMetrics.masterPaneMinWidth, maxWidth)
                      .toDouble();
                }),
              ),
            ],
            Expanded(child: detail),
          ],
        );
      },
    );
  }

  @override
  void dispose() {
    _filterController.dispose();
    super.dispose();
  }

  void _openCreateManualCapture(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _ManualCaptureCreateDialog(
          controller: widget.controller,
          copy: widget.copy,
        ),
      ),
    );
  }

  void _confirmDeleteCapture(BuildContext context) {
    final capture = widget.controller.selectedCapture;
    if (capture == null) return;
    unawaited(
      showDialog<DeletionOutcome>(
        context: context,
        builder: (_) => DeletionConfirmation(
          copy: widget.copy,
          title: widget.copy('deletion.capture.title'),
          subject: capture.displayName,
          consequence: widget.copy('deletion.capture.consequence'),
          onConfirm: () async {
            final result = await widget.controller.deleteCapture(capture.key);
            if (result == null) {
              throw StateError(
                widget.controller.inventoryError ?? 'capture delete failed',
              );
            }
            return result;
          },
        ),
      ),
    );
  }

  void _openRotateManualCapture(BuildContext context) {
    unawaited(
      showDialog<void>(
        context: context,
        builder: (context) => _ManualCaptureRotateDialog(
          controller: widget.controller,
          copy: widget.copy,
        ),
      ),
    );
  }
}

final class _CaptureMaster extends StatelessWidget {
  const _CaptureMaster({
    required this.controller,
    required this.copy,
    required this.filter,
    required this.filterController,
    required this.onFilter,
    required this.onClearFilter,
    required this.onCreateManual,
    required this.onSelect,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final String filter;
  final TextEditingController filterController;
  final ValueChanged<String> onFilter;
  final VoidCallback onClearFilter;
  final VoidCallback onCreateManual;
  final ValueChanged<String> onSelect;

  @override
  Widget build(BuildContext context) {
    final query = filter.trim().toLowerCase();
    bool matches(CaptureRecord capture) {
      if (query.isEmpty) return true;
      final managed = capture.managedRun;
      return [
        capture.displayName,
        capture.id,
        managed?.workspaceLabel ?? '',
        managed?.deviceName ?? '',
        managed?.cwd ?? '',
      ].any((value) => value.toLowerCase().contains(query));
    }

    final running = controller.runningCaptures
        .where(matches)
        .toList(growable: false);
    final history = controller.historicalCaptures
        .where(matches)
        .toList(growable: false);
    return ColoredBox(
      color: context.viberColors.panel,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 8, 6, 6),
            child: Row(
              children: [
                Expanded(
                  child: Semantics(
                    textField: true,
                    label: copy('capture.search'),
                    child: CompactSearchField(
                      key: const Key('capture-filter'),
                      controller: filterController,
                      hintText: copy('capture.search'),
                      onChanged: onFilter,
                      onClear: filter.isEmpty ? null : onClearFilter,
                      clearLabel: copy('capture.search.clear'),
                    ),
                  ),
                ),
                const SizedBox(width: 4),
                IconButton(
                  key: const Key('manual-capture-create'),
                  onPressed:
                      controller.data?.environments.any(
                            (environment) => environment.state == 'active',
                          ) ??
                          false
                      ? onCreateManual
                      : null,
                  tooltip: copy('capture.manual.create'),
                  icon: const Icon(Icons.add_link, size: 17),
                ),
              ],
            ),
          ),
          Expanded(
            child:
                running.isEmpty &&
                    history.isEmpty &&
                    controller.data?.captureNextCursor == null
                ? CenteredMessage(
                    icon: Icons.filter_alt_off,
                    title: copy('capture.empty'),
                    detail: copy('capture.empty.detail'),
                    action: TextButton.icon(
                      key: const Key('capture-empty-open-terminal-settings'),
                      onPressed: () =>
                          controller.selectSection(WorkbenchSection.settings),
                      icon: const Icon(Icons.terminal, size: 15),
                      label: Text(copy('capture.empty.action')),
                    ),
                  )
                : ListView(
                    children: [
                      SectionLabel(
                        label: copy('capture.running'),
                        count: running.length,
                      ),
                      for (final capture in running)
                        _CaptureRow(
                          capture: capture,
                          copy: copy,
                          activityAt: _activityAt(capture),
                          selected:
                              capture.key == controller.selectedCaptureKey,
                          onPressed: () => onSelect(capture.key),
                        ),
                      SectionLabel(
                        label: copy('capture.history'),
                        count: history.length,
                      ),
                      for (final capture in history)
                        _CaptureRow(
                          capture: capture,
                          copy: copy,
                          activityAt: _activityAt(capture),
                          selected:
                              capture.key == controller.selectedCaptureKey,
                          onPressed: () => onSelect(capture.key),
                        ),
                      if (running.isEmpty && history.isEmpty)
                        SizedBox(
                          height: 176,
                          child: CenteredMessage(
                            icon: Icons.filter_alt_off,
                            title: copy('capture.search.no_match'),
                            detail: copy('capture.search.load_hint'),
                          ),
                        ),
                      if (controller.captureDirectoryError case final error?)
                        Padding(
                          padding: const EdgeInsets.fromLTRB(8, 8, 8, 0),
                          child: InlineNotice(message: error, error: true),
                        ),
                      if (controller.data?.captureNextCursor != null)
                        Padding(
                          padding: const EdgeInsets.fromLTRB(8, 9, 8, 0),
                          child: OutlinedButton.icon(
                            key: const Key('captures-load-more'),
                            onPressed: controller.captureDirectoryLoading
                                ? null
                                : () =>
                                      unawaited(controller.loadMoreCaptures()),
                            icon: controller.captureDirectoryLoading
                                ? const SizedBox.square(
                                    dimension: 12,
                                    child: CircularProgressIndicator(
                                      strokeWidth: 1.5,
                                    ),
                                  )
                                : const Icon(Icons.history, size: 14),
                            label: Text(copy('capture.load_more')),
                          ),
                        ),
                      const SizedBox(height: 12),
                    ],
                  ),
          ),
        ],
      ),
    );
  }

  // The last-activity stamp used to prefer a global Conversation index, which
  // only ever loaded while the retired Conversations section was on screen. A
  // Capture row therefore showed a different time depending on where the user
  // had browsed. The selected Capture's own Activities are exact, and the
  // Capture's own timestamps are the answer for every other row.
  DateTime _activityAt(CaptureRecord capture) {
    if (capture.key == controller.selectedCaptureKey &&
        controller.selectedActivities.isNotEmpty) {
      return controller.selectedActivities
          .map((value) => value.occurredAt)
          .reduce((left, right) => left.isAfter(right) ? left : right);
    }
    return capture.manualCapture?.lastObservedAt ?? capture.updatedAt;
  }
}

final class _CaptureRow extends StatelessWidget {
  const _CaptureRow({
    required this.capture,
    required this.copy,
    required this.activityAt,
    required this.selected,
    required this.onPressed,
  });

  final CaptureRecord capture;
  final AppCopy copy;
  final DateTime activityAt;
  final bool selected;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final managed = capture.managedRun;
    final subtitle =
        managed?.workspaceLabel ??
        managed?.cwd ??
        capture.manualCapture?.clientClass ??
        capture.id;
    final activity = _relativeTime(activityAt, copy);
    final state = _localizedCopy(copy, 'capture.state', capture.state);
    return Semantics(
      key: Key('capture-row-${capture.key}'),
      selected: selected,
      button: true,
      label: '${capture.displayName}, $subtitle, $state',
      child: Material(
        color: selected ? context.viberColors.selection : Colors.transparent,
        child: InkWell(
          onTap: onPressed,
          canRequestFocus: true,
          focusColor: context.viberColors.focus.withValues(alpha: 0.15),
          child: Container(
            height: 48,
            decoration: BoxDecoration(
              border: Border(
                left: BorderSide(
                  width: 2,
                  color: selected
                      ? context.viberColors.route
                      : Colors.transparent,
                ),
                bottom: BorderSide(color: context.viberColors.dividerSoft),
              ),
            ),
            padding: const EdgeInsets.fromLTRB(8, 4, 7, 4),
            child: Row(
              children: [
                _CaptureGlyph(capture: capture),
                const SizedBox(width: 8),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              capture.displayName,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.titleSmall,
                            ),
                          ),
                          Text(
                            activity,
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ],
                      ),
                      const SizedBox(height: 2),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              subtitle,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _CaptureGlyph extends StatelessWidget {
  const _CaptureGlyph({
    required this.capture,
    this.size = ViberMetrics.controlHeight,
    this.glyphSize = 16,
  });

  final CaptureRecord capture;
  final double size;
  final double glyphSize;

  @override
  Widget build(BuildContext context) {
    final color = capture.isManual
        ? context.viberColors.warning
        : context.viberColors.route;
    return AgentIdentityMark(
      candidates: [capture.displayName, capture.managedRun?.executableLabel],
      fallbackLabel: capture.displayName,
      fallbackIcon: capture.isManual ? Icons.link : Icons.terminal,
      fallbackColor: color,
      muted: !capture.running,
      size: size,
      glyphSize: glyphSize,
    );
  }
}

final class _CaptureDetail extends StatelessWidget {
  const _CaptureDetail({
    required this.controller,
    required this.copy,
    required this.showBack,
    required this.confirmRevoke,
    required this.onBack,
    required this.onConfirmRevoke,
    required this.onRotateManual,
    required this.onDeleteCapture,
    required this.masterVisible,
    required this.onToggleMaster,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool showBack;
  final bool confirmRevoke;
  final VoidCallback onBack;
  final ValueChanged<bool> onConfirmRevoke;
  final VoidCallback onRotateManual;
  final VoidCallback onDeleteCapture;
  final bool masterVisible;
  final VoidCallback? onToggleMaster;

  @override
  Widget build(BuildContext context) {
    final capture = controller.selectedCapture;
    if (capture == null) {
      return CenteredMessage(icon: Icons.adjust, title: copy('capture.select'));
    }
    final assignment = controller.selectedAssignment;
    final environment = controller.data?.environments
        .where((candidate) => candidate.id == assignment?.environmentId)
        .firstOrNull;
    final route = _routeForCapture(capture, environment);
    final endpoint = controller.data?.endpoints
        .where((candidate) => candidate.id == route?.endpointId)
        .firstOrNull;
    final account = controller.data?.accounts
        .where(
          (candidate) => candidate.id == route?.accountPolicy.fixedAccountId,
        )
        .firstOrNull;
    final accountPolicy = route?.accountPolicy;
    final frozenFixedAccount = accountPolicy?.accounts
        .where((candidate) => candidate.id == accountPolicy.fixedAccountId)
        .firstOrNull;
    final selector = accountPolicy?.selector;
    final accountAuthority = switch (accountPolicy?.mode) {
      'fixed' =>
        account?.displayName ??
            frozenFixedAccount?.displayName ??
            accountPolicy!.fixedAccountId,
      'javascript' when selector != null =>
        '${copy('environment.account.javascript')} · ${selector.displayName} · r${selector.revision}',
      _ => copy('common.client_passthrough'),
    };
    final accountMatches =
        route == null ||
        route.accountPolicy.mode == 'javascript' ||
        account != null && account.upstreamEndpointId == endpoint?.id;
    final notice = controller.operationNotice;
    return ColoredBox(
      color: context.viberColors.canvas,
      child: Column(
        children: [
          if (controller.errorMessage case final message?)
            InlineNotice(message: message, error: true),
          if (notice case final value?)
            InlineNotice(
              message: copy('notice.$value'),
              onDismiss: controller.clearNotice,
              dismissLabel: copy('common.dismiss'),
            ),
          _CaptureContext(
            capture: capture,
            assignment: assignment,
            environments: controller.data?.environments ?? const [],
            conversations: controller.captureConversations,
            hasEarlierConversations:
                controller.selectedCaptureConversations?.nextCursor != null,
            copy: copy,
            showBack: showBack,
            confirmRevoke: confirmRevoke,
            mutating: controller.mutating,
            onBack: onBack,
            onConfirmRevoke: onConfirmRevoke,
            onRotate: onRotateManual,
            onDelete: onDeleteCapture,
            onApplyLatestEnvironment: () =>
                unawaited(controller.applyLatestSelectedCaptureEnvironment()),
            routeDetail: [
              if (endpoint != null) endpoint.displayName,
              accountAuthority,
            ].join('  ·  '),
            masterVisible: masterVisible,
            onToggleMaster: onToggleMaster,
            onRevoke: () async {
              final success = await controller.revokeSelectedManualCapture();
              if (success) onConfirmRevoke(false);
            },
          ),
          if (!accountMatches)
            InlineNotice(
              message: copy('environment.account.invalid'),
              error: true,
            ),
          Expanded(
            child: controller.detailLoading
                ? const Center(child: CompactProgressIndicator())
                : _CaptureConversationWorkspace(
                    controller: controller,
                    copy: copy,
                  ),
          ),
        ],
      ),
    );
  }

  static EnvironmentRoute? _routeForCapture(
    CaptureRecord capture,
    EnvironmentRecord? environment,
  ) {
    if (environment == null) return null;
    final executable = capture.managedRun?.executableLabel.toLowerCase() ?? '';
    final desired = executable.contains('codex')
        ? 'openai'
        : executable.contains('claude')
        ? 'anthropic'
        : '';
    for (final endpoint in environment.clientEndpoints) {
      for (final plan in endpoint.protocolPlans) {
        if (desired.isNotEmpty && !plan.clientProtocol.contains(desired)) {
          continue;
        }
        final upstream = plan.destination.upstream;
        if (upstream == null) continue;
        return upstream.routes
                .where((route) => route.id == upstream.defaultRouteId)
                .firstOrNull ??
            upstream.routes.firstOrNull;
      }
    }
    return environment.routes.firstOrNull;
  }
}

final class _CaptureConversationWorkspace extends StatefulWidget {
  const _CaptureConversationWorkspace({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_CaptureConversationWorkspace> createState() =>
      _CaptureConversationWorkspaceState();
}

final class _CaptureConversationWorkspaceState
    extends State<_CaptureConversationWorkspace> {
  static const _minimumDirectoryWidth = 196.0;
  static const _maximumDirectoryWidth = 420.0;

  double _directoryWidth = 260;
  final Set<String> _collapsed = <String>{};
  String? _captureKey;
  String? _selectedSessionKey;

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final copy = widget.copy;
    final conversations = controller.captureConversations;
    final nextCaptureKey = controller.selectedCapture?.key;
    if (_captureKey != nextCaptureKey) {
      _captureKey = nextCaptureKey;
      _selectedSessionKey = null;
      _collapsed.clear();
    }
    final sessions = _captureSessionGroups(conversations);
    final selected = controller.selectedCaptureConversation;
    final selectedConversationSession = sessions
        .where(
          (session) => session.conversations.any(
            (conversation) => conversation.key == selected?.key,
          ),
        )
        .firstOrNull;
    if (!sessions.any((session) => session.key == _selectedSessionKey)) {
      _selectedSessionKey =
          selectedConversationSession?.key ?? sessions.firstOrNull?.key;
    }
    final selectedSession = sessions
        .where((session) => session.key == _selectedSessionKey)
        .firstOrNull;
    final visibleConversations =
        selectedSession?.conversations ?? conversations;
    final tree = _captureConversationTree(visibleConversations);
    final timelineTitle = selected?.exchangeScoped == true
        ? copy('conversation.exchanges_title')
        : copy('capture.conversation');
    final Widget timeline =
        controller.captureActivitiesLoading &&
            controller.selectedCapturePage == null
        ? CompactLoadingMessage(label: copy('common.loading'))
        : EvidenceConversationTimeline(
            key: ValueKey(
              'capture-timeline:${controller.selectedCaptureConversationKey ?? 'empty'}',
            ),
            controller: controller,
            activities: controller.selectedActivities,
            copy: copy,
            title: timelineTitle,
            canLoadEarlier: controller.selectedCapturePage?.nextCursor != null,
            loadingEarlier: controller.captureActivitiesLoading,
            exchangeScoped: selected?.exchangeScoped ?? false,
            onLoadEarlier: () =>
                unawaited(controller.loadMoreSelectedCapture()),
          );
    final hasExactSession = sessions.any(
      (session) => session.sessionId != null,
    );
    if (conversations.length <= 1 && !hasExactSession) return timeline;

    void selectSession(String key) {
      final session = sessions.where((value) => value.key == key).firstOrNull;
      if (session == null) return;
      setState(() {
        _selectedSessionKey = key;
        _collapsed.clear();
      });
      final preferred = _preferredSessionConversation(session.conversations);
      if (preferred != null) {
        unawaited(controller.selectCaptureConversation(preferred.key));
      }
    }

    return LayoutBuilder(
      builder: (context, constraints) {
        if (constraints.maxWidth < 1040) {
          return Column(
            children: [
              _CaptureSessionSelector(
                sessions: sessions,
                selectedKey: _selectedSessionKey,
                copy: copy,
                onSelected: selectSession,
              ),
              if (visibleConversations.length > 1)
                _CaptureConversationSelector(
                  conversations: tree,
                  selectedKey: controller.selectedCaptureConversationKey,
                  copy: copy,
                  onSelected: (key) =>
                      unawaited(controller.selectCaptureConversation(key)),
                ),
              Expanded(child: timeline),
            ],
          );
        }
        final maximumWidth = math.min(
          _maximumDirectoryWidth,
          constraints.maxWidth * 0.42,
        );
        final directoryWidth = _directoryWidth
            .clamp(_minimumDirectoryWidth, maximumWidth)
            .toDouble();
        return Row(
          children: [
            SizedBox(
              key: const Key('capture-conversation-pane'),
              width: directoryWidth,
              child: _CaptureConversationDirectory(
                conversations: tree,
                sessions: sessions,
                selectedSessionKey: _selectedSessionKey,
                collapsed: _collapsed,
                selectedKey: controller.selectedCaptureConversationKey,
                copy: copy,
                onSelected: (key) =>
                    unawaited(controller.selectCaptureConversation(key)),
                onSessionSelected: selectSession,
                onToggleBranch: (key) => setState(() {
                  if (!_collapsed.add(key)) _collapsed.remove(key);
                }),
              ),
            ),
            WorkbenchPaneDivider(
              key: const Key('capture-conversation-divider'),
              label: copy('common.resize_directory'),
              onDrag: (delta) => setState(() {
                _directoryWidth = (_directoryWidth + delta)
                    .clamp(_minimumDirectoryWidth, maximumWidth)
                    .toDouble();
              }),
            ),
            Expanded(child: timeline),
          ],
        );
      },
    );
  }
}

final class _CaptureSessionGroup {
  const _CaptureSessionGroup({
    required this.key,
    required this.client,
    required this.sessionId,
    required this.conversations,
  });

  final String key;
  final String? client;
  final String? sessionId;
  final List<ConversationSummary> conversations;
}

const _unresolvedCaptureSessionKey = 'unresolved';

List<_CaptureSessionGroup> _captureSessionGroups(
  List<ConversationSummary> conversations,
) {
  final grouped = <String, List<ConversationSummary>>{};
  final identities = <String, ({String? client, String? sessionId})>{};
  for (final conversation in conversations) {
    final identity = conversation.conversation.clientIdentity;
    final exact = identity != null && identity.sessionId.isNotEmpty;
    final key = exact
        ? '${identity.client}:${identity.sessionId}'
        : _unresolvedCaptureSessionKey;
    (grouped[key] ??= []).add(conversation);
    identities[key] = (
      client: exact ? identity.client : null,
      sessionId: exact ? identity.sessionId : null,
    );
  }
  final result = grouped.entries
      .map((entry) {
        final identity = identities[entry.key]!;
        return _CaptureSessionGroup(
          key: entry.key,
          client: identity.client,
          sessionId: identity.sessionId,
          conversations: List.unmodifiable(entry.value),
        );
      })
      .toList(growable: false);
  result.sort((left, right) {
    final leftTime = left.conversations
        .map((value) => value.latest.occurredAt)
        .reduce((a, b) => a.isAfter(b) ? a : b);
    final rightTime = right.conversations
        .map((value) => value.latest.occurredAt)
        .reduce((a, b) => a.isAfter(b) ? a : b);
    final byTime = rightTime.compareTo(leftTime);
    return byTime != 0 ? byTime : left.key.compareTo(right.key);
  });
  return List.unmodifiable(result);
}

ConversationSummary? _preferredSessionConversation(
  List<ConversationSummary> values,
) =>
    values.where((value) => value.conversation.kind == 'main').firstOrNull ??
    values.where((value) => value.conversation.kind == 'agent').firstOrNull ??
    values.firstOrNull;

String _captureSessionTitle(AppCopy copy, _CaptureSessionGroup session) {
  final sessionId = session.sessionId;
  if (sessionId == null) return copy('capture.session_unavailable');
  final client = switch (session.client) {
    'codex' => 'Codex',
    'claude' => 'Claude',
    final value? => value,
    null => 'Agent',
  };
  return '$client  ·  ${_compactOpaqueIdentity(sessionId)}';
}

String _compactOpaqueIdentity(String value) {
  if (value.length <= 16) return value;
  return '${value.substring(0, 6)}…${value.substring(value.length - 8)}';
}

final class _CaptureSessionSelector extends StatelessWidget {
  const _CaptureSessionSelector({
    required this.sessions,
    required this.selectedKey,
    required this.copy,
    required this.onSelected,
  });

  final List<_CaptureSessionGroup> sessions;
  final String? selectedKey;
  final AppCopy copy;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    return Container(
      key: const Key('capture-session-selector'),
      padding: const EdgeInsets.fromLTRB(8, 6, 8, 7),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border(bottom: BorderSide(color: context.viberColors.divider)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            copy('capture.session'),
            style: Theme.of(context).textTheme.labelSmall?.copyWith(
              color: context.viberColors.textMuted,
            ),
          ),
          const SizedBox(height: 2),
          Text(
            copy('capture.session_scope'),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: context.viberColors.textFaint,
              height: 1.2,
            ),
          ),
          const SizedBox(height: 4),
          CompactSelectField<String>(
            key: ValueKey('capture-session-select:$selectedKey'),
            initialValue: selectedKey,
            isExpanded: true,
            items: [
              for (final session in sessions)
                DropdownMenuItem(
                  key: Key('capture-session-option-${session.key}'),
                  value: session.key,
                  child: Text(
                    _captureSessionTitle(copy, session),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
            ],
            onChanged: sessions.length < 2
                ? null
                : (value) {
                    if (value != null) onSelected(value);
                  },
          ),
        ],
      ),
    );
  }
}

final class _CaptureConversationSelector extends StatelessWidget {
  const _CaptureConversationSelector({
    required this.conversations,
    required this.selectedKey,
    required this.copy,
    required this.onSelected,
  });

  final List<CaptureConversationTreeEntry<ConversationSummary>> conversations;
  final String? selectedKey;
  final AppCopy copy;
  final ValueChanged<String> onSelected;

  @override
  Widget build(BuildContext context) {
    return Container(
      key: const Key('capture-conversation-selector'),
      height: 38,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
      decoration: BoxDecoration(
        color: context.viberColors.panel,
        border: Border(bottom: BorderSide(color: context.viberColors.divider)),
      ),
      child: Row(
        children: [
          Text(
            copy('capture.conversations'),
            style: Theme.of(context).textTheme.labelMedium,
          ),
          const SizedBox(width: 8),
          Expanded(
            child: CompactSelectField<String>(
              key: ValueKey('capture-conversation-select:$selectedKey'),
              initialValue: selectedKey,
              isExpanded: true,
              items: [
                for (final entry in conversations.indexed)
                  DropdownMenuItem(
                    value: entry.$2.value.key,
                    child: Text(
                      '${entry.$2.depth == 0 ? '' : '${'  ' * entry.$2.depth}\u21b3 '}'
                      '${_captureConversationTitle(copy, entry.$2.value, entry.$1)}  ·  '
                      '${copy.format('conversations.turn_count', {'count': entry.$2.value.turnCount})}',
                    ),
                  ),
              ],
              onChanged: (value) {
                if (value != null) onSelected(value);
              },
              decoration: InputDecoration(
                labelText: copy('capture.conversation_select'),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

final class _CaptureConversationDirectory extends StatelessWidget {
  const _CaptureConversationDirectory({
    required this.conversations,
    required this.sessions,
    required this.selectedSessionKey,
    required this.collapsed,
    required this.selectedKey,
    required this.copy,
    required this.onSelected,
    required this.onSessionSelected,
    required this.onToggleBranch,
  });

  final List<CaptureConversationTreeEntry<ConversationSummary>> conversations;
  final List<_CaptureSessionGroup> sessions;
  final String? selectedSessionKey;
  final Set<String> collapsed;
  final String? selectedKey;
  final AppCopy copy;
  final ValueChanged<String> onSelected;
  final ValueChanged<String> onSessionSelected;
  final ValueChanged<String> onToggleBranch;

  @override
  Widget build(BuildContext context) {
    final byKey = <String, CaptureConversationTreeEntry<ConversationSummary>>{
      for (final entry in conversations) entry.key: entry,
    };
    bool visible(CaptureConversationTreeEntry<ConversationSummary> entry) {
      var parentKey = entry.parentKey;
      while (parentKey != null) {
        if (collapsed.contains(parentKey)) return false;
        parentKey = byKey[parentKey]?.parentKey;
      }
      return true;
    }

    final visibleConversations = conversations.where(visible).toList();
    return ColoredBox(
      color: context.viberColors.panel,
      child: Column(
        children: [
          _CaptureSessionSelector(
            sessions: sessions,
            selectedKey: selectedSessionKey,
            copy: copy,
            onSelected: onSessionSelected,
          ),
          SectionLabel(
            label: copy('capture.conversations'),
            count: conversations.length,
          ),
          Expanded(
            child: ListView.builder(
              itemCount: visibleConversations.length,
              itemBuilder: (context, index) {
                final treeEntry = visibleConversations[index];
                final conversation = treeEntry.value;
                final selected = conversation.key == selectedKey;
                final title = _captureConversationTitle(
                  copy,
                  conversation,
                  conversations.indexOf(treeEntry),
                );
                final subtitle = [
                  copy.format('conversations.turn_count', {
                    'count': conversation.turnCount,
                  }),
                  _relativeTime(conversation.latest.occurredAt, copy),
                ].join('  ·  ');
                final statusLabel = copy('activity.status.${treeEntry.status}');
                return Tooltip(
                  message: _captureConversationTooltip(
                    copy,
                    conversation,
                    title,
                    subtitle,
                    statusLabel,
                  ),
                  waitDuration: const Duration(milliseconds: 450),
                  child: Semantics(
                    selected: selected,
                    button: true,
                    label: '$title, $subtitle, $statusLabel',
                    child: Material(
                      color: selected
                          ? context.viberColors.selection
                          : Colors.transparent,
                      child: InkWell(
                        key: Key('capture-conversation-${conversation.key}'),
                        onTap: () => onSelected(conversation.key),
                        child: Container(
                          height: 46,
                          padding: const EdgeInsets.fromLTRB(7, 5, 8, 5),
                          decoration: BoxDecoration(
                            border: Border(
                              left: BorderSide(
                                width: 2,
                                color: selected
                                    ? context.viberColors.route
                                    : Colors.transparent,
                              ),
                              bottom: BorderSide(
                                color: context.viberColors.dividerSoft,
                              ),
                            ),
                          ),
                          child: Row(
                            children: [
                              _ConversationTreeLeading(
                                entry: treeEntry,
                                conversation: conversation,
                                collapsed: collapsed.contains(treeEntry.key),
                                selected: selected,
                                copy: copy,
                                onToggle: () => onToggleBranch(treeEntry.key),
                              ),
                              const SizedBox(width: 7),
                              Expanded(
                                child: Column(
                                  mainAxisAlignment: MainAxisAlignment.center,
                                  crossAxisAlignment: CrossAxisAlignment.start,
                                  children: [
                                    Text(
                                      title,
                                      maxLines: 1,
                                      overflow: TextOverflow.ellipsis,
                                      style: Theme.of(
                                        context,
                                      ).textTheme.titleSmall,
                                    ),
                                    const SizedBox(height: 2),
                                    Text(
                                      subtitle,
                                      maxLines: 1,
                                      overflow: TextOverflow.ellipsis,
                                      style: Theme.of(
                                        context,
                                      ).textTheme.bodySmall,
                                    ),
                                  ],
                                ),
                              ),
                              const SizedBox(width: 5),
                              _ConversationBranchStatus(
                                status: treeEntry.status,
                                label: statusLabel,
                              ),
                            ],
                          ),
                        ),
                      ),
                    ),
                  ),
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

final class _ConversationBranchStatus extends StatelessWidget {
  const _ConversationBranchStatus({required this.status, required this.label});

  final String status;
  final String label;

  @override
  Widget build(BuildContext context) {
    final color = switch (status) {
      'pending' => context.viberColors.warning,
      'failed' => context.viberColors.danger,
      'canceled' => context.viberColors.textFaint,
      _ => context.viberColors.verified,
    };
    final icon = switch (status) {
      'pending' => Icons.hourglass_top_rounded,
      'failed' => Icons.error_outline_rounded,
      'canceled' => Icons.cancel_outlined,
      _ => Icons.check_circle_outline_rounded,
    };
    return Tooltip(
      message: label,
      child: Semantics(
        label: label,
        child: Icon(icon, size: 14, color: color),
      ),
    );
  }
}

final class _ConversationTreeLeading extends StatelessWidget {
  const _ConversationTreeLeading({
    required this.entry,
    required this.conversation,
    required this.collapsed,
    required this.selected,
    required this.copy,
    required this.onToggle,
  });

  final CaptureConversationTreeEntry<ConversationSummary> entry;
  final ConversationSummary conversation;
  final bool collapsed;
  final bool selected;
  final AppCopy copy;
  final VoidCallback onToggle;

  @override
  Widget build(BuildContext context) {
    const indent = 14.0;
    const iconWidth = 18.0;
    final depth = math.min(entry.depth, 8);
    final color = selected
        ? context.viberColors.route
        : context.viberColors.textMuted;
    final icon = entry.hasChildren
        ? IconButton(
            padding: EdgeInsets.zero,
            constraints: const BoxConstraints.tightFor(
              width: iconWidth,
              height: 26,
            ),
            tooltip: collapsed
                ? copy('capture.conversation_expand_branch')
                : copy('capture.conversation_collapse_branch'),
            onPressed: onToggle,
            icon: Icon(
              collapsed ? Icons.chevron_right : Icons.expand_more,
              size: 16,
            ),
          )
        : Icon(
            entry.depth > 0
                ? Icons.subdirectory_arrow_right
                : _captureConversationIcon(conversation),
            size: 15,
            color: color,
          );
    return SizedBox(
      key: Key('capture-conversation-tree-leading-${entry.key}'),
      width: depth * indent + iconWidth,
      height: 30,
      child: Stack(
        children: [
          Positioned.fill(
            child: CustomPaint(
              painter: _ConversationTreeGuidePainter(
                depth: depth,
                ancestorHasNextSibling: entry.ancestorHasNextSibling,
                isLastSibling: entry.isLastSibling,
                color: context.viberColors.divider,
              ),
            ),
          ),
          Positioned(right: 0, top: 2, bottom: 2, child: icon),
        ],
      ),
    );
  }
}

final class _ConversationTreeGuidePainter extends CustomPainter {
  const _ConversationTreeGuidePainter({
    required this.depth,
    required this.ancestorHasNextSibling,
    required this.isLastSibling,
    required this.color,
  });

  final int depth;
  final List<bool> ancestorHasNextSibling;
  final bool isLastSibling;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    if (depth == 0) return;
    const indent = 14.0;
    const branchCenter = 7.0;
    final paint = Paint()
      ..color = color
      ..strokeWidth = 1
      ..style = PaintingStyle.stroke;
    final middle = size.height / 2;
    final ancestorCount = math.min(depth - 1, ancestorHasNextSibling.length);
    for (var index = 0; index < ancestorCount; index += 1) {
      if (!ancestorHasNextSibling[index]) continue;
      final x = index * indent + branchCenter;
      canvas.drawLine(Offset(x, 0), Offset(x, size.height), paint);
    }
    final x = (depth - 1) * indent + branchCenter;
    canvas.drawLine(Offset(x, 0), Offset(x, middle), paint);
    if (!isLastSibling) {
      canvas.drawLine(Offset(x, middle), Offset(x, size.height), paint);
    }
    canvas.drawLine(Offset(x, middle), Offset(depth * indent, middle), paint);
  }

  @override
  bool shouldRepaint(covariant _ConversationTreeGuidePainter oldDelegate) =>
      depth != oldDelegate.depth ||
      isLastSibling != oldDelegate.isLastSibling ||
      color != oldDelegate.color ||
      !_sameBoolList(
        ancestorHasNextSibling,
        oldDelegate.ancestorHasNextSibling,
      );
}

bool _sameBoolList(List<bool> left, List<bool> right) {
  if (identical(left, right)) return true;
  if (left.length != right.length) return false;
  for (var index = 0; index < left.length; index += 1) {
    if (left[index] != right[index]) return false;
  }
  return true;
}

List<CaptureConversationTreeEntry<ConversationSummary>>
_captureConversationTree(List<ConversationSummary> conversations) {
  return buildCaptureConversationTree(
    conversations.map((conversation) {
      final identity = conversation.conversation.clientIdentity;
      return CaptureConversationTreeSeed<ConversationSummary>(
        value: conversation,
        key: conversation.key,
        client: identity?.client,
        sessionId: identity?.sessionId,
        actorId: identity?.actorId,
        parentActorIds: _nativeParentActorIds(identity),
        firstObservedAt: conversation.firstObservedAt,
        status: conversation.latest.status,
      );
    }),
  );
}

List<String> _nativeParentActorIds(AgentClientIdentity? identity) {
  if (identity == null) return const [];
  final names = switch (identity.client) {
    'claude' => const {'claude.parent_agent_id'},
    'codex' => const {'codex.parent_thread_id', 'codex.forked_from_thread_id'},
    _ => const <String>{},
  };
  return [
    for (final evidence in identity.protocolIds)
      if (names.contains(evidence.name)) evidence.value,
  ];
}

String _captureConversationTitle(
  AppCopy copy,
  ConversationSummary conversation,
  int index,
) {
  final displayName = conversation.conversation.displayName?.trim();
  final identity = conversation.conversation.clientIdentity;
  final actorLabel = identity?.actorLabel?.trim();
  final actorId = identity?.actorId?.trim();
  return switch (conversation.conversation.kind) {
    'main' => copy('capture.conversation_main'),
    'agent' when actorLabel != null && actorLabel.isNotEmpty => actorLabel,
    'agent'
        when displayName != null &&
            displayName.isNotEmpty &&
            displayName != actorId =>
      displayName,
    'agent'
        when identity?.actorIsSubagent == true &&
            actorId != null &&
            actorId.isNotEmpty =>
      'subagent · ${_compactNativeIdentity(actorId)}',
    'agent' when actorId != null && actorId.isNotEmpty =>
      '${identity?.client ?? 'agent'} · ${_compactNativeIdentity(actorId)}',
    'isolated_subagent' => copy.format('capture.conversation_subagent', {
      'time': _clockTime(conversation.firstObservedAt),
    }),
    'pending_exchange' => copy.format('capture.conversation_pending', {
      'time': _clockTime(conversation.firstObservedAt),
    }),
    'isolated_exchange' => copy.format('capture.conversation_exchange', {
      'time': _clockTime(conversation.firstObservedAt),
    }),
    _ when displayName != null && displayName.isNotEmpty => displayName,
    _ => copy.format('capture.conversation_numbered', {'number': index + 1}),
  };
}

String _captureConversationTooltip(
  AppCopy copy,
  ConversationSummary conversation,
  String title,
  String subtitle,
  String statusLabel,
) {
  final lines = <String>[title, subtitle, statusLabel];
  final identity = conversation.conversation.clientIdentity;
  if (identity == null) return lines.join('\n');
  lines.add(
    '${copy('exchange.client.field.session_id')}: ${identity.sessionId}',
  );
  if (identity.actorId case final actorId?) {
    final label = copy(
      identity.client == 'codex'
          ? 'exchange.client.field.thread_id'
          : 'exchange.client.field.agent_id',
    );
    lines.add('$label: $actorId');
  }
  final parentNames = switch (identity.client) {
    'claude' => const {'claude.parent_agent_id'},
    'codex' => const {'codex.parent_thread_id', 'codex.forked_from_thread_id'},
    _ => const <String>{},
  };
  for (final evidence in identity.protocolIds) {
    if (!parentNames.contains(evidence.name)) continue;
    lines.add(
      '${copy('exchange.client.field.parent_agent_id')}: ${evidence.value}',
    );
  }
  return lines.join('\n');
}

String _compactNativeIdentity(String value) {
  if (value.length <= 14) return value;
  return '${value.substring(0, 8)}…${value.substring(value.length - 4)}';
}

IconData _captureConversationIcon(ConversationSummary conversation) =>
    switch (conversation.conversation.kind) {
      'main' => Icons.forum_outlined,
      'agent' => Icons.account_tree_outlined,
      'isolated_subagent' => Icons.subdirectory_arrow_right,
      'pending_exchange' => Icons.pending_outlined,
      _ => Icons.swap_horiz,
    };

String _clockTime(DateTime timestamp) {
  final local = timestamp.toLocal();
  String two(int value) => value.toString().padLeft(2, '0');
  return '${two(local.hour)}:${two(local.minute)}:${two(local.second)}';
}

final class _CaptureContext extends StatelessWidget {
  const _CaptureContext({
    required this.capture,
    required this.assignment,
    required this.environments,
    required this.conversations,
    required this.hasEarlierConversations,
    required this.copy,
    required this.showBack,
    required this.confirmRevoke,
    required this.mutating,
    required this.onBack,
    required this.onConfirmRevoke,
    required this.onRevoke,
    required this.onRotate,
    required this.onDelete,
    required this.onApplyLatestEnvironment,
    required this.routeDetail,
    required this.masterVisible,
    required this.onToggleMaster,
  });

  final CaptureRecord capture;
  final CaptureAssignment? assignment;
  final List<EnvironmentRecord> environments;
  final List<ConversationSummary> conversations;
  final bool hasEarlierConversations;
  final AppCopy copy;
  final bool showBack;
  final bool confirmRevoke;
  final bool mutating;
  final VoidCallback onBack;
  final ValueChanged<bool> onConfirmRevoke;
  final VoidCallback onRevoke;
  final VoidCallback onRotate;
  final VoidCallback onDelete;
  final VoidCallback onApplyLatestEnvironment;
  final String routeDetail;
  final bool masterVisible;
  final VoidCallback? onToggleMaster;

  @override
  Widget build(BuildContext context) {
    final source = capture.isManual
        ? copy('capture.source.manual.short')
        : copy('capture.source.managed.short');
    final detail = capture.isManual
        ? copy('capture.source.manual')
        : copy('capture.source.managed');
    final aggregate = _captureAggregate(
      copy,
      conversations,
      exchangeScoped: capture.isManual,
      hasEarlier: hasEarlierConversations,
    );
    return Container(
      color: context.viberColors.panel,
      padding: const EdgeInsets.fromLTRB(14, 8, 14, 7),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final compact = constraints.maxWidth < 520;
          final canManage =
              capture.isManual && capture.running && !confirmRevoke;
          final revokeButton = OutlinedButton.icon(
            key: const Key('manual-capture-revoke'),
            onPressed: mutating ? null : () => onConfirmRevoke(true),
            icon: const Icon(Icons.link_off, size: 14),
            label: Text(copy('capture.revoke')),
            style: OutlinedButton.styleFrom(
              foregroundColor: context.viberColors.warning,
            ),
          );
          // Deleting a Capture is the one action here that removes evidence,
          // so it is offered only once the Capture has stopped: while it runs
          // its own writer is still adding to what would be deleted.
          final deleteButton = OutlinedButton.icon(
            key: const Key('capture-delete'),
            onPressed: mutating || capture.running ? null : () => onDelete(),
            icon: const Icon(Icons.delete_outline, size: 14),
            label: Text(copy('deletion.confirm')),
            style: OutlinedButton.styleFrom(
              foregroundColor: context.viberColors.danger,
            ),
          );
          final hasHeaderActions = canManage || !capture.running;
          final headerActions = Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              if (canManage) ...[
                OutlinedButton.icon(
                  key: const Key('manual-capture-rotate'),
                  onPressed: mutating ? null : onRotate,
                  icon: const Icon(Icons.sync_lock, size: 14),
                  label: Text(copy('capture.manual.rotate')),
                ),
                revokeButton,
              ],
              if (!capture.running) deleteButton,
            ],
          );
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  if (onToggleMaster case final toggle?) ...[
                    IconButton(
                      key: const Key('capture-directory-toggle'),
                      onPressed: toggle,
                      tooltip: copy(
                        masterVisible
                            ? 'common.hide_directory'
                            : 'common.show_directory',
                      ),
                      icon: Icon(Icons.view_sidebar_outlined, size: 15),
                      constraints: const BoxConstraints.tightFor(
                        width: 24,
                        height: 24,
                      ),
                      padding: EdgeInsets.zero,
                    ),
                    const SizedBox(width: 4),
                  ],
                  if (showBack) ...[
                    IconButton(
                      onPressed: onBack,
                      tooltip: copy('common.back'),
                      icon: const Icon(Icons.arrow_back, size: 17),
                      constraints: const BoxConstraints.tightFor(
                        width: 30,
                        height: 30,
                      ),
                      padding: EdgeInsets.zero,
                    ),
                    const SizedBox(width: 4),
                  ],
                  _CaptureGlyph(capture: capture, size: 30, glyphSize: 18),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Expanded(
                              child: Text(
                                capture.displayName,
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                                style: Theme.of(
                                  context,
                                ).textTheme.headlineSmall,
                              ),
                            ),
                          ],
                        ),
                        const SizedBox(height: 2),
                        _CaptureSummaryLine(
                          metadata: _captureMetadata(copy, capture, source),
                          aggregate: aggregate,
                          detail: detail,
                        ),
                        if (capture.managedRun case final managed?) ...[
                          const SizedBox(height: 3),
                          _CaptureClientCompatibility(
                            key: Key(
                              'capture-client-compatibility-${capture.key}',
                            ),
                            managed: managed,
                            copy: copy,
                          ),
                        ],
                      ],
                    ),
                  ),
                  if (hasHeaderActions && !compact) ...[
                    const SizedBox(width: 8),
                    headerActions,
                  ],
                ],
              ),
              if (hasHeaderActions && compact) ...[
                const SizedBox(height: 7),
                Align(alignment: Alignment.centerLeft, child: headerActions),
              ],
              const SizedBox(height: 6),
              _EnvironmentScopeControls(
                capture: capture,
                assignment: assignment,
                environments: environments,
                copy: copy,
                routeDetail: routeDetail,
                mutating: mutating,
                onApplyLatest: onApplyLatestEnvironment,
              ),
              if (confirmRevoke) ...[
                const SizedBox(height: 9),
                Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(9),
                  decoration: BoxDecoration(
                    color: context.viberColors.warning.withValues(alpha: 0.08),
                    border: Border.all(
                      color: context.viberColors.warning.withValues(
                        alpha: 0.32,
                      ),
                    ),
                    borderRadius: ViberMetrics.surfaceRadius,
                  ),
                  child: compact
                      ? Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Row(
                              children: [
                                Icon(
                                  Icons.info_outline,
                                  size: 16,
                                  color: context.viberColors.warning,
                                ),
                                const SizedBox(width: 7),
                                Expanded(
                                  child: Text(
                                    copy('capture.revoke.confirm'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                ),
                              ],
                            ),
                            const SizedBox(height: 4),
                            Text(
                              copy('capture.revoke.detail'),
                              style: Theme.of(context).textTheme.bodySmall,
                            ),
                            const SizedBox(height: 7),
                            Row(
                              mainAxisAlignment: MainAxisAlignment.end,
                              children: [
                                TextButton(
                                  key: const Key(
                                    'manual-capture-revoke-cancel',
                                  ),
                                  onPressed: mutating
                                      ? null
                                      : () => onConfirmRevoke(false),
                                  child: Text(copy('common.cancel')),
                                ),
                                const SizedBox(width: 4),
                                FilledButton(
                                  key: const Key(
                                    'manual-capture-revoke-confirm',
                                  ),
                                  onPressed: mutating ? null : onRevoke,
                                  style: FilledButton.styleFrom(
                                    backgroundColor: context.viberColors.danger,
                                  ),
                                  child: Text(copy('capture.revoke.action')),
                                ),
                              ],
                            ),
                          ],
                        )
                      : Row(
                          children: [
                            Icon(
                              Icons.info_outline,
                              size: 16,
                              color: context.viberColors.warning,
                            ),
                            const SizedBox(width: 8),
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    copy('capture.revoke.confirm'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                  Text(
                                    copy('capture.revoke.detail'),
                                    style: Theme.of(
                                      context,
                                    ).textTheme.bodySmall,
                                  ),
                                ],
                              ),
                            ),
                            TextButton(
                              key: const Key('manual-capture-revoke-cancel'),
                              onPressed: mutating
                                  ? null
                                  : () => onConfirmRevoke(false),
                              child: Text(copy('common.cancel')),
                            ),
                            const SizedBox(width: 4),
                            FilledButton(
                              key: const Key('manual-capture-revoke-confirm'),
                              onPressed: mutating ? null : onRevoke,
                              style: FilledButton.styleFrom(
                                backgroundColor: context.viberColors.danger,
                              ),
                              child: Text(copy('capture.revoke.action')),
                            ),
                          ],
                        ),
                ),
              ],
            ],
          );
        },
      ),
    );
  }
}

final class _CaptureSummaryLine extends StatelessWidget {
  const _CaptureSummaryLine({
    required this.metadata,
    required this.aggregate,
    required this.detail,
  });

  final String metadata;
  final String aggregate;
  final String detail;

  @override
  Widget build(BuildContext context) {
    return Tooltip(
      message: '$aggregate. $metadata. $detail',
      child: LayoutBuilder(
        builder: (context, constraints) {
          final stats = Text(
            aggregate,
            key: const Key('capture-aggregate-summary'),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(
              context,
            ).textTheme.labelMedium?.copyWith(color: context.viberColors.text),
          );
          final contextText = Text(
            metadata,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.bodySmall,
          );
          if (constraints.maxWidth < 520) {
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [stats, const SizedBox(height: 1), contextText],
            );
          }
          return Row(
            children: [
              Flexible(flex: 3, child: stats),
              const SizedBox(width: 10),
              Expanded(flex: 2, child: contextText),
            ],
          );
        },
      ),
    );
  }
}

final class _CaptureClientCompatibility extends StatelessWidget {
  const _CaptureClientCompatibility({
    required this.managed,
    required this.copy,
    super.key,
  });

  final ManagedRunSummary managed;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final adapter = managed.clientAdapter;
    final status = copy('capture.client.compatibility.${managed.recognition}');
    final verified = managed.recognition == 'verified';
    final color = verified
        ? context.viberColors.verified
        : managed.recognition == 'unknown'
        ? context.viberColors.textMuted
        : context.viberColors.warning;
    return Tooltip(
      message: status,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            verified ? Icons.verified_outlined : Icons.info_outline,
            size: 13,
            color: color,
          ),
          const SizedBox(width: 4),
          Text(
            adapter?.version ?? copy('capture.client.version_unknown'),
            style: monoStyle.copyWith(color: context.viberColors.textMuted),
          ),
          const SizedBox(width: 6),
          Flexible(
            child: Text(
              status,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(
                context,
              ).textTheme.labelSmall?.copyWith(color: color),
            ),
          ),
        ],
      ),
    );
  }
}

final class _EnvironmentScopeControls extends StatelessWidget {
  const _EnvironmentScopeControls({
    required this.capture,
    required this.assignment,
    required this.environments,
    required this.copy,
    required this.routeDetail,
    required this.mutating,
    required this.onApplyLatest,
  });

  final CaptureRecord capture;
  final CaptureAssignment? assignment;
  final List<EnvironmentRecord> environments;
  final AppCopy copy;
  final String routeDetail;
  final bool mutating;
  final VoidCallback onApplyLatest;

  @override
  Widget build(BuildContext context) {
    final assigned = assignment?.environmentId;
    final assignedName = environments
        .where((environment) => environment.id == assigned)
        .map((environment) => environment.name)
        .firstOrNull;
    final latest = environments
        .where((environment) => environment.id == assigned)
        .firstOrNull;
    final updateAvailable =
        capture.running &&
        assignment != null &&
        latest != null &&
        latest.state == 'active' &&
        latest.revision > assignment!.environmentRevision;
    final revisions = assignment == null
        ? null
        : copy.format(
            updateAvailable
                ? 'capture.environment.revisions_update'
                : 'capture.environment.revisions',
            {
              'launch': assignment!.launchEnvironmentRevision,
              'current': assignment!.environmentRevision,
              if (latest != null) 'latest': latest.revision,
            },
          );
    final visibleDetails = [
      if (routeDetail.isNotEmpty) routeDetail,
      ?revisions,
    ];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _EnvironmentScopeRow(
          key: const Key('capture-environment-scope'),
          icon: Icons.adjust,
          tone: context.viberColors.route,
          title: copy('capture.environment.current'),
          visibleDetail: visibleDetails.isEmpty
              ? null
              : visibleDetails.join(' · '),
          detail: [
            if (routeDetail.isNotEmpty) routeDetail,
            copy(
              capture.running
                  ? 'capture.environment.help'
                  : 'capture.environment.history',
            ),
          ].join('. '),
          control: _ReadOnlyEnvironmentValue(
            key: const Key('capture-environment-readonly'),
            name: assignedName ?? assigned ?? '—',
          ),
        ),
        if (updateAvailable)
          Padding(
            padding: const EdgeInsets.only(left: 28, top: 2),
            child: Align(
              alignment: Alignment.centerLeft,
              child: OutlinedButton.icon(
                key: const Key('capture-environment-apply-latest'),
                onPressed: mutating ? null : onApplyLatest,
                icon: const Icon(Icons.update, size: 14),
                label: Text(copy('capture.environment.apply_latest')),
              ),
            ),
          ),
      ],
    );
  }
}

final class _ReadOnlyEnvironmentValue extends StatelessWidget {
  const _ReadOnlyEnvironmentValue({required this.name, super.key});

  final String name;

  @override
  Widget build(BuildContext context) {
    return Container(
      height: ViberMetrics.controlHeight,
      alignment: Alignment.centerLeft,
      padding: const EdgeInsets.symmetric(horizontal: 8),
      decoration: BoxDecoration(
        color: context.viberColors.canvas,
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: Text(
        name,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: Theme.of(
          context,
        ).textTheme.titleSmall?.copyWith(color: context.viberColors.textMuted),
      ),
    );
  }
}

final class _EnvironmentScopeRow extends StatelessWidget {
  const _EnvironmentScopeRow({
    required this.icon,
    required this.tone,
    required this.title,
    required this.detail,
    required this.control,
    this.visibleDetail,
    super.key,
  });

  final IconData icon;
  final Color tone;
  final String title;
  final String detail;
  final Widget control;
  final String? visibleDetail;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: '$title. $detail',
      container: true,
      child: Tooltip(
        message: detail,
        child: Padding(
          padding: const EdgeInsets.symmetric(vertical: 3),
          child: LayoutBuilder(
            builder: (context, constraints) {
              final compact = constraints.maxWidth < 280;
              final label = Row(
                children: [
                  Container(
                    width: 22,
                    height: 22,
                    alignment: Alignment.center,
                    decoration: BoxDecoration(
                      color: tone.withValues(alpha: 0.08),
                      borderRadius: ViberMetrics.controlRadius,
                    ),
                    child: Icon(icon, size: 13, color: tone),
                  ),
                  const SizedBox(width: 6),
                  Expanded(
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          title,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall,
                        ),
                        if (visibleDetail case final value?)
                          Text(
                            value,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                      ],
                    ),
                  ),
                  const SizedBox(width: 3),
                  Icon(
                    Icons.info_outline,
                    size: 12,
                    color: context.viberColors.textFaint,
                  ),
                ],
              );
              if (compact) {
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    label,
                    if (control is! SizedBox) ...[
                      const SizedBox(height: 4),
                      control,
                    ],
                  ],
                );
              }
              return Row(
                children: [
                  Expanded(child: label),
                  const SizedBox(width: 8),
                  SizedBox(width: 170, child: control),
                ],
              );
            },
          ),
        ),
      ),
    );
  }
}

final class _ManualCaptureCreateDialog extends StatefulWidget {
  const _ManualCaptureCreateDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ManualCaptureCreateDialog> createState() =>
      _ManualCaptureCreateDialogState();
}

final class _ManualCaptureCreateDialogState
    extends State<_ManualCaptureCreateDialog> {
  final _formKey = GlobalKey<FormState>();
  final _name = TextEditingController();
  late String _environmentId;
  String _clientClass = 'desktop_app';
  String _lifetime = 'until_revoked';
  int _temporarySeconds = 3600;
  int _loadGeneration = 0;
  bool _loadingContext = true;
  bool _submitted = false;
  ManualCaptureContext? _context;
  ManualCaptureGrantStateTag? _created;

  @override
  void initState() {
    super.initState();
    final environments = widget.controller.data!.environments
        .where((environment) => environment.state == 'active')
        .toList(growable: false);
    _environmentId =
        environments
            .where((environment) => environment.id == 'system_transparent')
            .firstOrNull
            ?.id ??
        environments.first.id;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(_loadContext());
    });
  }

  @override
  void dispose() {
    _name.dispose();
    _created = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final created = _created;
    if (created != null) {
      return AlertDialog(
        title: Text(copy('capture.manual.delivery.title')),
        content: SizedBox(
          width: 470,
          child: _ManualGrantDelivery(grant: created.grant, copy: copy),
        ),
        actions: [
          FilledButton(
            key: const Key('manual-capture-delivery-done'),
            onPressed: () => Navigator.pop(context),
            child: Text(copy('capture.manual.delivery.done')),
          ),
        ],
      );
    }
    final environments = widget.controller.data!.environments
        .where((environment) => environment.state == 'active')
        .toList(growable: false);
    return AlertDialog(
      title: Text(copy('capture.manual.create.title')),
      content: SizedBox(
        width: 470,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextFormField(
                  key: const Key('manual-capture-name'),
                  controller: _name,
                  autofocus: true,
                  maxLength: 256,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.name'),
                    hintText: copy('capture.manual.name.placeholder'),
                    counterText: '',
                  ),
                  validator: (value) => value == null || value.trim().isEmpty
                      ? copy('routes.validation.required')
                      : null,
                ),
                const SizedBox(height: 7),
                CompactSelectField<String>(
                  key: const Key('manual-capture-environment'),
                  initialValue: _environmentId,
                  decoration: InputDecoration(
                    labelText: copy('capture.environment'),
                  ),
                  items: [
                    for (final environment in environments)
                      DropdownMenuItem(
                        value: environment.id,
                        child: Text(environment.name),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) {
                          setState(() {
                            _environmentId = value!;
                            _context = null;
                            _loadingContext = true;
                            _submitted = false;
                          });
                          unawaited(_loadContext());
                        },
                ),
                const SizedBox(height: 9),
                CompactSelectField<String>(
                  initialValue: _clientClass,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.client_class'),
                  ),
                  items: [
                    for (final value in const ['desktop_app', 'cli', 'other'])
                      DropdownMenuItem(
                        value: value,
                        child: Text(copy('capture.manual.client_class.$value')),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) => setState(() => _clientClass = value!),
                ),
                const SizedBox(height: 9),
                CompactSelectField<String>(
                  initialValue: _lifetime,
                  decoration: InputDecoration(
                    labelText: copy('capture.manual.lifetime'),
                  ),
                  items: [
                    for (final value in const ['until_revoked', 'temporary'])
                      DropdownMenuItem(
                        value: value,
                        child: Text(copy('capture.manual.lifetime.$value')),
                      ),
                  ],
                  onChanged: widget.controller.mutating
                      ? null
                      : (value) => setState(() => _lifetime = value!),
                ),
                if (_lifetime == 'temporary' && _context != null) ...[
                  const SizedBox(height: 9),
                  CompactSelectField<int>(
                    key: const Key('manual-capture-duration'),
                    initialValue: _temporarySeconds,
                    decoration: InputDecoration(
                      labelText: copy('capture.manual.duration'),
                    ),
                    items: [
                      for (final seconds in _durationOptions(_context!))
                        DropdownMenuItem(
                          value: seconds,
                          child: Text(_durationLabel(seconds, copy)),
                        ),
                    ],
                    onChanged: widget.controller.mutating
                        ? null
                        : (value) => setState(() => _temporarySeconds = value!),
                  ),
                ],
                const SizedBox(height: 11),
                if (_loadingContext)
                  const Center(
                    child: Padding(
                      padding: EdgeInsets.all(12),
                      child: CompactProgressIndicator(),
                    ),
                  )
                else if (_context case final captureContext?)
                  _ManualContextReview(context: captureContext, copy: copy)
                else
                  InlineNotice(
                    message:
                        widget.controller.errorMessage ?? copy('common.retry'),
                    error: true,
                  ),
                if (_submitted && widget.controller.errorMessage != null) ...[
                  const SizedBox(height: 9),
                  InlineNotice(
                    message: widget.controller.errorMessage!,
                    error: true,
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.mutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('manual-capture-create-confirm'),
          onPressed: widget.controller.mutating || _context == null
              ? null
              : _create,
          child: widget.controller.mutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(copy('capture.manual.create.action')),
        ),
      ],
    );
  }

  Future<void> _loadContext() async {
    final generation = ++_loadGeneration;
    final environmentId = _environmentId;
    final value = await widget.controller.loadManualCaptureContext(
      environmentId,
    );
    if (!mounted || generation != _loadGeneration) return;
    setState(() {
      _context = value;
      _loadingContext = false;
      if (value != null) {
        _temporarySeconds = value.defaultTemporarySeconds;
      }
    });
  }

  Future<void> _create() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _submitted = true);
    final created = await widget.controller.createManualCapture(
      context: _context!,
      displayName: _name.text,
      clientClass: _clientClass,
      lifetime: _lifetime,
      expiresInSeconds: _lifetime == 'temporary' ? _temporarySeconds : null,
    );
    if (!mounted) return;
    setState(() => _created = created);
  }

  static List<int> _durationOptions(ManualCaptureContext context) {
    final values = {
      context.defaultTemporarySeconds,
      3600,
      8 * 3600,
      24 * 3600,
    }.where((value) => value <= context.maxTemporarySeconds).toList()..sort();
    return values;
  }
}

final class _ManualCaptureRotateDialog extends StatefulWidget {
  const _ManualCaptureRotateDialog({
    required this.controller,
    required this.copy,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  State<_ManualCaptureRotateDialog> createState() =>
      _ManualCaptureRotateDialogState();
}

final class _ManualCaptureRotateDialogState
    extends State<_ManualCaptureRotateDialog> {
  ManualCaptureGrantStateTag? _rotated;
  bool _submitted = false;

  @override
  void dispose() {
    _rotated = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final rotated = _rotated;
    if (rotated != null) {
      return AlertDialog(
        title: Text(copy('capture.manual.delivery.rotated.title')),
        content: SizedBox(
          width: 470,
          child: _ManualGrantDelivery(grant: rotated.grant, copy: copy),
        ),
        actions: [
          FilledButton(
            key: const Key('manual-capture-delivery-done'),
            onPressed: () => Navigator.pop(context),
            child: Text(copy('capture.manual.delivery.done')),
          ),
        ],
      );
    }
    return AlertDialog(
      title: Text(copy('capture.manual.rotate.title')),
      content: SizedBox(
        width: 430,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy('capture.manual.rotate.detail'),
              style: Theme.of(context).textTheme.bodyMedium,
            ),
            if (_submitted && widget.controller.errorMessage != null) ...[
              const SizedBox(height: 9),
              InlineNotice(
                message: widget.controller.errorMessage!,
                error: true,
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: widget.controller.mutating
              ? null
              : () => Navigator.pop(context),
          child: Text(copy('common.cancel')),
        ),
        FilledButton(
          key: const Key('manual-capture-rotate-confirm'),
          onPressed: widget.controller.mutating ? null : _rotate,
          child: widget.controller.mutating
              ? const SizedBox.square(
                  dimension: 13,
                  child: CircularProgressIndicator(strokeWidth: 1.5),
                )
              : Text(copy('capture.manual.rotate.action')),
        ),
      ],
    );
  }

  Future<void> _rotate() async {
    setState(() => _submitted = true);
    final rotated = await widget.controller.rotateSelectedManualCapture();
    if (!mounted) return;
    setState(() => _rotated = rotated);
  }
}

final class _ManualContextReview extends StatelessWidget {
  const _ManualContextReview({required this.context, required this.copy});

  final ManualCaptureContext context;
  final AppCopy copy;

  @override
  Widget build(BuildContext buildContext) {
    final semantic = context.root != null;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(9),
      decoration: BoxDecoration(
        color:
            (semantic
                    ? buildContext.viberColors.warning
                    : buildContext.viberColors.route)
                .withValues(alpha: 0.07),
        border: Border.all(
          color:
              (semantic
                      ? buildContext.viberColors.warning
                      : buildContext.viberColors.route)
                  .withValues(alpha: 0.28),
        ),
        borderRadius: ViberMetrics.surfaceRadius,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            copy('capture.manual.review.title'),
            style: Theme.of(buildContext).textTheme.titleSmall,
          ),
          const SizedBox(height: 4),
          Text(
            semantic
                ? copy('capture.manual.review.semantic')
                : copy('capture.manual.review.connection_only'),
            style: Theme.of(buildContext).textTheme.bodySmall,
          ),
          const SizedBox(height: 6),
          Text(context.proxyAddress, style: monoStyle),
          Text(
            '${context.environmentId}  ·  r${context.environmentRevision}',
            style: monoStyle,
          ),
          if (context.protectedAuthorities.isNotEmpty)
            Text(
              context.protectedAuthorities.join('  ·  '),
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: monoStyle,
            ),
        ],
      ),
    );
  }
}

final class _ManualGrantDelivery extends StatefulWidget {
  const _ManualGrantDelivery({required this.grant, required this.copy});

  final ManualCaptureGrant grant;
  final AppCopy copy;

  @override
  State<_ManualGrantDelivery> createState() => _ManualGrantDeliveryState();
}

final class _ManualGrantDeliveryState extends State<_ManualGrantDelivery> {
  String? _copied;

  @override
  Widget build(BuildContext context) {
    final grant = widget.grant;
    final copy = widget.copy;
    return Semantics(
      liveRegion: true,
      container: true,
      child: SingleChildScrollView(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            InlineNotice(message: copy('capture.manual.delivery.once')),
            const SizedBox(height: 10),
            _ManualValueRow(
              label: copy('capture.manual.delivery.proxy'),
              value: grant.proxyAddress,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.proxy'),
              }),
              onCopy: () => _copy('proxy', grant.proxyAddress),
            ),
            _ManualValueRow(
              label: copy('capture.manual.delivery.username'),
              value: grant.proxyUsername,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.username'),
              }),
              onCopy: () => _copy('username', grant.proxyUsername),
            ),
            _ManualValueRow(
              label: copy('capture.manual.delivery.password'),
              value: grant.proxyPassword,
              copyTooltip: copy.format('common.copy', {
                'field': copy('capture.manual.delivery.password'),
              }),
              onCopy: () => _copy('password', grant.proxyPassword),
            ),
            if (grant.root case final root?)
              _ManualValueRow(
                label: copy('capture.manual.delivery.root'),
                value: root.pemPath,
                copyTooltip: copy.format('common.copy', {
                  'field': copy('capture.manual.delivery.root'),
                }),
                onCopy: () => _copy('root', root.pemPath),
              ),
            const SizedBox(height: 7),
            Text(
              copy('capture.manual.delivery.evidence'),
              style: Theme.of(context).textTheme.bodySmall,
            ),
            if (_copied case final copied?) ...[
              const SizedBox(height: 6),
              Text(
                copy.format('capture.manual.delivery.copied', {
                  'field': copy('capture.manual.delivery.$copied'),
                }),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.verified,
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Future<void> _copy(String field, String value) async {
    await Clipboard.setData(ClipboardData(text: value));
    if (mounted) setState(() => _copied = field);
  }
}

final class _ManualValueRow extends StatelessWidget {
  const _ManualValueRow({
    required this.label,
    required this.value,
    required this.copyTooltip,
    required this.onCopy,
  });

  final String label;
  final String value;
  final String copyTooltip;
  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(vertical: 6),
      decoration: BoxDecoration(
        border: Border(
          bottom: BorderSide(color: context.viberColors.dividerSoft),
        ),
      ),
      child: Row(
        children: [
          SizedBox(
            width: 82,
            child: Text(label, style: Theme.of(context).textTheme.labelMedium),
          ),
          Expanded(
            child: SelectableText(
              value,
              maxLines: 2,
              style: monoStyle.copyWith(color: context.viberColors.text),
            ),
          ),
          IconButton(
            onPressed: onCopy,
            tooltip: copyTooltip,
            icon: const Icon(Icons.copy, size: 14),
          ),
        ],
      ),
    );
  }
}

String _durationLabel(int seconds, AppCopy copy) {
  if (seconds % 3600 == 0) {
    return copy.format('capture.manual.duration.hours', {
      'count': seconds ~/ 3600,
    });
  }
  return copy.format('capture.manual.duration.minutes', {
    'count': math.max(1, seconds ~/ 60),
  });
}

String _captureMetadata(AppCopy copy, CaptureRecord capture, String source) {
  final managed = capture.managedRun;
  final parts = <String>[
    if (_usefulLabel(managed?.workspaceLabel)) managed!.workspaceLabel!,
    if (_usefulLabel(managed?.deviceName)) managed!.deviceName!,
    if (_usefulLabel(managed?.localUserLabel)) managed!.localUserLabel!,
    if (capture.isManual && capture.manualCapture != null)
      copy('capture.manual.client_class.${capture.manualCapture!.clientClass}'),
    source,
    _localizedCopy(copy, 'capture.state', capture.state),
  ];
  return parts.join('  ·  ');
}

String _captureAggregate(
  AppCopy copy,
  List<ConversationSummary> conversations, {
  required bool exchangeScoped,
  required bool hasEarlier,
}) {
  final turns = conversations.fold<int>(
    0,
    (total, conversation) => total + conversation.turnCount,
  );
  final count = '$turns${hasEarlier ? '+' : ''}';
  final values = <String>[
    copy.format(
      exchangeScoped ? 'conversation.exchanges' : 'conversation.turns',
      {'count': count},
    ),
  ];
  if (!exchangeScoped && conversations.length > 1) {
    values.add(
      copy.format('capture.conversation_count', {
        'count': '${conversations.length}${hasEarlier ? '+' : ''}',
      }),
    );
  }
  return values.join('  ·  ');
}

bool _usefulLabel(String? value) {
  final normalized = value?.trim();
  return normalized != null &&
      normalized.isNotEmpty &&
      normalized.toLowerCase() != 'null';
}

String _relativeTime(DateTime timestamp, AppCopy copy) {
  final now = DateTime.now().toUtc();
  final delta = now.difference(timestamp);
  if (delta.isNegative || delta.inMinutes < 1) {
    return copy('common.time.now');
  }
  if (delta.inHours < 1) {
    return copy.format('common.time.minutes', {'count': delta.inMinutes});
  }
  if (delta.inDays < 1) {
    return copy.format('common.time.hours', {'count': delta.inHours});
  }
  return copy.format('common.time.days', {'count': delta.inDays});
}

String _localizedCopy(AppCopy copy, String family, String value) {
  final key = '$family.$value';
  return copy.maybe(key) ?? value;
}

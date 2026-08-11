import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';
import 'workbench_controller.dart';

final class OfflineHoldCommand extends StatelessWidget {
  const OfflineHoldCommand({
    required this.controller,
    required this.copy,
    required this.compact,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final snapshot = controller.offlineHold;
    if (snapshot == null) return const SizedBox.shrink();
    final actionable =
        !controller.offlineMutating &&
        (snapshot.canEnter || snapshot.canResume);
    final label = snapshot.canEnter
        ? copy('offline.enter')
        : snapshot.canResume
        ? copy('offline.resume')
        : _stateLabel(copy, snapshot);
    final icon = _stateIcon(snapshot);
    final message = controller.offlineError == null
        ? '${copy('offline.title')} · ${_stateLabel(copy, snapshot)}'
        : '${copy('offline.title')} · ${_offlineMessage(copy, controller.offlineError!)}';
    final action = actionable
        ? () => showOfflineHoldConfirmation(context, controller, copy)
        : null;

    if (compact) {
      return Tooltip(
        message: message,
        child: Semantics(
          liveRegion: snapshot.transitioning,
          label: '$label · ${_stateLabel(copy, snapshot)}',
          button: true,
          enabled: actionable,
          child: IconButton(
            key: const Key('offline-hold-command'),
            onPressed: action,
            icon: controller.offlineMutating
                ? const SizedBox.square(
                    dimension: 14,
                    child: CircularProgressIndicator(strokeWidth: 1.5),
                  )
                : Icon(icon, size: 16, color: _stateColor(snapshot)),
            constraints: const BoxConstraints.tightFor(width: 31, height: 31),
            padding: EdgeInsets.zero,
          ),
        ),
      );
    }

    return Tooltip(
      message: message,
      child: OutlinedButton.icon(
        key: const Key('offline-hold-command'),
        onPressed: action,
        icon: controller.offlineMutating
            ? const SizedBox.square(
                dimension: 12,
                child: CircularProgressIndicator(strokeWidth: 1.4),
              )
            : Icon(icon, size: 14, color: _stateColor(snapshot)),
        label: Text(label, maxLines: 1),
        style: OutlinedButton.styleFrom(
          foregroundColor: _stateColor(snapshot),
          side: BorderSide(
            color: _stateColor(snapshot).withValues(alpha: 0.46),
          ),
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 5),
          textStyle: const TextStyle(
            fontSize: 10.5,
            fontWeight: FontWeight.w600,
          ),
        ),
      ),
    );
  }
}

final class OfflineHoldSettingsPanel extends StatelessWidget {
  const OfflineHoldSettingsPanel({
    required this.controller,
    required this.copy,
    super.key,
  });

  final WorkbenchController controller;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) {
    final snapshot = controller.offlineHold;
    if (snapshot == null) return const SizedBox.shrink();
    final notice = controller.offlineError ?? controller.offlineNotice;
    final isError = controller.offlineError != null;
    final kinds = <String>{
      ...snapshot.activeByKind.keys,
      ...snapshot.queuedByKind.keys,
    }.toList()..sort();
    return Container(
      key: const Key('offline-settings-panel'),
      decoration: BoxDecoration(
        color: ViberColors.panelRaised,
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 10, 10, 9),
            child: Row(
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        copy('offline.title'),
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      const SizedBox(height: 3),
                      Text(
                        copy('offline.summary'),
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 10),
                StatusPill(
                  label: _stateLabel(copy, snapshot),
                  color: _stateColor(snapshot),
                  icon: _stateIcon(snapshot),
                ),
              ],
            ),
          ),
          if (notice != null)
            InlineNotice(
              message: isError
                  ? _offlineMessage(copy, notice)
                  : copy('notice.$notice'),
              error: isError,
              onDismiss: controller.clearOfflineMessage,
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Wrap(
                  spacing: 7,
                  runSpacing: 7,
                  children: [
                    _OfflineMetric(
                      label: copy('offline.safe'),
                      value: snapshot.safeToDisconnect
                          ? copy('offline.safe.yes')
                          : copy('offline.safe.no'),
                      color: snapshot.safeToDisconnect
                          ? ViberColors.verified
                          : ViberColors.textMuted,
                    ),
                    _OfflineMetric(
                      label: copy('offline.active_actions'),
                      value: '${snapshot.activeActions}',
                    ),
                    _OfflineMetric(
                      label: copy('offline.entering_actions'),
                      value: '${snapshot.enteringActions}',
                    ),
                    _OfflineMetric(
                      label: copy('offline.active_egress'),
                      value: '${snapshot.activeEgress}',
                    ),
                    _OfflineMetric(
                      label: copy('offline.queued_requests'),
                      value: '${snapshot.queuedRequests}',
                    ),
                    _OfflineMetric(
                      label: copy('offline.held_bytes'),
                      value: _bytes(snapshot.heldBytes),
                    ),
                  ],
                ),
                if (kinds.isNotEmpty) ...[
                  const SizedBox(height: 9),
                  Wrap(
                    spacing: 6,
                    runSpacing: 5,
                    children: [
                      for (final kind in kinds)
                        _KindChip(
                          copy: copy,
                          label: copy('offline.kind.$kind'),
                          active: snapshot.activeByKind[kind] ?? 0,
                          queued: snapshot.queuedByKind[kind] ?? 0,
                        ),
                    ],
                  ),
                ],
                if (snapshot.lastProbeReason case final reason?) ...[
                  const SizedBox(height: 9),
                  Text(
                    '${copy('offline.last_probe')}: ${_offlineMessage(copy, reason)}',
                    style: Theme.of(
                      context,
                    ).textTheme.bodySmall?.copyWith(color: ViberColors.danger),
                  ),
                ],
                const SizedBox(height: 10),
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        '${copy('offline.revision')} r${snapshot.revision} · ${_clock(snapshot.since.toLocal())}',
                        style: monoStyle,
                      ),
                    ),
                    OutlinedButton.icon(
                      key: const Key('offline-settings-action'),
                      onPressed:
                          !controller.offlineMutating &&
                              (snapshot.canEnter || snapshot.canResume)
                          ? () => showOfflineHoldConfirmation(
                              context,
                              controller,
                              copy,
                            )
                          : null,
                      icon: controller.offlineMutating
                          ? const SizedBox.square(
                              dimension: 13,
                              child: CircularProgressIndicator(
                                strokeWidth: 1.4,
                              ),
                            )
                          : Icon(_stateIcon(snapshot), size: 14),
                      label: Text(
                        snapshot.canResume
                            ? copy('offline.resume')
                            : snapshot.canEnter
                            ? copy('offline.enter')
                            : _stateLabel(copy, snapshot),
                      ),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

Future<void> showOfflineHoldConfirmation(
  BuildContext context,
  WorkbenchController controller,
  AppCopy copy,
) async {
  final snapshot = controller.offlineHold;
  if (snapshot == null || (!snapshot.canEnter && !snapshot.canResume)) return;
  final resume = snapshot.canResume;
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      key: const Key('offline-confirmation'),
      title: Text(
        copy(
          resume
              ? 'offline.confirm.resume.title'
              : 'offline.confirm.enter.title',
        ),
      ),
      content: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 430),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              copy(
                resume
                    ? 'offline.confirm.resume.detail'
                    : 'offline.confirm.enter.detail',
              ),
              style: Theme.of(context).textTheme.bodyMedium,
            ),
            const SizedBox(height: 12),
            Wrap(
              spacing: 7,
              runSpacing: 7,
              children: [
                _OfflineMetric(
                  label: copy('offline.active_egress'),
                  value: '${snapshot.activeEgress}',
                ),
                _OfflineMetric(
                  label: copy('offline.queued_requests'),
                  value: '${snapshot.queuedRequests}',
                ),
                _OfflineMetric(
                  label: copy('offline.held_bytes'),
                  value: _bytes(snapshot.heldBytes),
                ),
              ],
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, false),
          child: Text(copy('common.cancel')),
        ),
        FilledButton.icon(
          key: const Key('offline-confirm-action'),
          onPressed: () => Navigator.pop(context, true),
          icon: Icon(resume ? Icons.cloud_sync_outlined : Icons.cloud_off),
          label: Text(
            copy(
              resume
                  ? 'offline.confirm.resume.action'
                  : 'offline.confirm.enter.action',
            ),
          ),
        ),
      ],
    ),
  );
  if (confirmed != true || !context.mounted) return;
  if (resume) {
    await controller.resumeOfflineHold();
  } else {
    await controller.enterOfflineHold();
  }
}

final class _OfflineMetric extends StatelessWidget {
  const _OfflineMetric({
    required this.label,
    required this.value,
    this.color = ViberColors.text,
  });

  final String label;
  final String value;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      constraints: const BoxConstraints(minWidth: 92),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      decoration: BoxDecoration(
        color: ViberColors.canvas.withValues(alpha: 0.54),
        border: Border.all(color: ViberColors.dividerSoft),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(label, style: Theme.of(context).textTheme.labelMedium),
          const SizedBox(height: 2),
          Text(
            value,
            style: monoStyle.copyWith(
              color: color,
              fontSize: 11,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }
}

final class _KindChip extends StatelessWidget {
  const _KindChip({
    required this.copy,
    required this.label,
    required this.active,
    required this.queued,
  });

  final AppCopy copy;
  final String label;
  final int active;
  final int queued;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 4),
      decoration: BoxDecoration(
        border: Border.all(color: ViberColors.divider),
        borderRadius: BorderRadius.circular(999),
      ),
      child: Text(
        '$label · ${copy.format('offline.kind.counts', {'active': active, 'queued': queued})}',
        style: monoStyle.copyWith(fontSize: 9.5),
      ),
    );
  }
}

String _stateLabel(AppCopy copy, OfflineHoldSnapshot snapshot) =>
    copy('offline.state.${snapshot.state}');

String _offlineMessage(AppCopy copy, String value) {
  if (const {
    'transport_unavailable',
    'tls_rejected',
    'canceled',
    'probe_failed',
  }.contains(value)) {
    return copy('offline.probe.$value');
  }
  return value;
}

Color _stateColor(OfflineHoldSnapshot snapshot) => switch (snapshot.state) {
  'online' => ViberColors.verified,
  'held' || 'entering' || 'probing' || 'releasing' => ViberColors.warning,
  'stopping' => ViberColors.danger,
  _ => ViberColors.textFaint,
};

IconData _stateIcon(OfflineHoldSnapshot snapshot) => switch (snapshot.state) {
  'online' => Icons.cloud_done_outlined,
  'held' => Icons.cloud_off,
  'entering' => Icons.hourglass_bottom,
  'probing' => Icons.cloud_sync_outlined,
  'releasing' => Icons.play_circle_outline,
  'stopping' => Icons.stop_circle_outlined,
  _ => Icons.cloud_queue_outlined,
};

String _bytes(int value) {
  if (value < 1024) return '$value B';
  if (value < 1024 * 1024) return '${(value / 1024).toStringAsFixed(1)} KiB';
  return '${(value / (1024 * 1024)).toStringAsFixed(1)} MiB';
}

String _clock(DateTime value) =>
    '${value.hour.toString().padLeft(2, '0')}:'
    '${value.minute.toString().padLeft(2, '0')}:'
    '${value.second.toString().padLeft(2, '0')}';

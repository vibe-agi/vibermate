import 'package:flutter/material.dart';

import '../../core/api/control_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';

/// The one confirmation every destructive action in the workbench goes through.
///
/// Four resources can be deleted and they differ only in what holds them, so
/// they differ in nothing the user has to learn twice. The dialog does two
/// things a per-feature dialog kept getting wrong: it says what will be lost
/// before the fact, and when the runtime refuses it shows the exact holders
/// rather than "in use", which leaves a user with no move.
final class DeletionConfirmation extends StatefulWidget {
  const DeletionConfirmation({
    required this.title,
    required this.subject,
    required this.consequence,
    required this.onConfirm,
    required this.copy,
    super.key,
  });

  /// What is being deleted, in the user's words.
  final String title;

  /// The exact thing: a name, so the user can tell two of them apart.
  final String subject;

  /// What deleting it costs. Stated before the fact, not after.
  final String consequence;

  final Future<DeletionOutcome> Function() onConfirm;
  final AppCopy copy;

  @override
  State<DeletionConfirmation> createState() => _DeletionConfirmationState();
}

class _DeletionConfirmationState extends State<DeletionConfirmation> {
  bool _running = false;
  DeletionOutcome? _refused;
  String? _error;

  Future<void> _confirm() async {
    setState(() {
      _running = true;
      _error = null;
      _refused = null;
    });
    try {
      final outcome = await widget.onConfirm();
      if (!mounted) return;
      if (outcome.deleted) {
        Navigator.of(context).pop(outcome);
        return;
      }
      setState(() {
        _running = false;
        _refused = outcome;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _running = false;
        _error = error.toString();
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final copy = widget.copy;
    final refused = _refused;
    return AlertDialog(
      key: const Key('deletion-confirm-dialog'),
      title: Text(widget.title),
      content: SizedBox(
        width: 420,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(widget.subject, style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(height: ViberSpacing.xs),
            Text(
              widget.consequence,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.textMuted,
              ),
            ),
            if (refused != null) ...[
              const SizedBox(height: ViberSpacing.sm),
              Text(
                copy('deletion.blocked'),
                key: const Key('deletion-blocked'),
                style: Theme.of(context).textTheme.labelMedium?.copyWith(
                  color: context.viberColors.warning,
                ),
              ),
              const SizedBox(height: ViberSpacing.xs),
              ConstrainedBox(
                constraints: const BoxConstraints(maxHeight: 180),
                child: SingleChildScrollView(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      for (final holder in refused.holders)
                        Padding(
                          key: Key('deletion-holder-${holder.id}'),
                          padding: const EdgeInsets.only(
                            bottom: ViberSpacing.xs,
                          ),
                          child: Text(
                            '${copy('deletion.holder.${holder.kind}')} · '
                            '${holder.label}',
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ),
                      // A truncated list that does not say so reads as the
                      // whole story, and the user stops looking.
                      if (refused.truncated)
                        Text(
                          copy.format('deletion.more_holders', {
                            'count':
                                refused.holderCount - refused.holders.length,
                          }),
                          key: const Key('deletion-more-holders'),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(color: context.viberColors.textMuted),
                        ),
                    ],
                  ),
                ),
              ),
            ],
            if (_error != null) ...[
              const SizedBox(height: ViberSpacing.sm),
              Text(
                _error!,
                key: const Key('deletion-error'),
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.danger,
                ),
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          key: const Key('deletion-cancel'),
          onPressed: _running ? null : () => Navigator.of(context).pop(),
          child: Text(copy('common.cancel')),
        ),
        // Once the runtime has named a holder, confirming again would only ask
        // the same question and get the same answer.
        FilledButton(
          key: const Key('deletion-confirm'),
          onPressed: _running || refused != null ? null : _confirm,
          child: Text(copy('deletion.confirm')),
        ),
      ],
    );
  }
}

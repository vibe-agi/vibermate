import 'package:flutter/material.dart';
import '../../core/api/native_tool_models.dart';
import '../../core/design/viber_theme.dart';
import '../../core/i18n/app_copy.dart';

class NativeToolApprovalRow extends StatelessWidget {
  const NativeToolApprovalRow({
    required this.approval,
    required this.profileName,
    required this.copy,
    required this.onDecision,
    super.key,
  });
  final NativeApproval approval;
  final String profileName;
  final AppCopy copy;
  final ValueChanged<NativeDecision> onDecision;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 4,
            children: [
              const Icon(Icons.build_outlined, size: 16),
              Text(copy('native.service')),
              Text(
                profileName,
                style: TextStyle(color: context.viberColors.textMuted),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(copy('native.login')),
          for (final intent in approval.intents) ...[
            const SizedBox(height: 8),
            if (intent.toolName.isNotEmpty)
              Text(
                intent.toolName,
                style: Theme.of(context).textTheme.titleMedium,
              ),
            Text(copy('native.action.${intent.action}')),
            SelectableText(intent.target),
            if (intent.arguments case final arguments?)
              SelectableText(arguments),
            if (intent.truncated)
              Text(
                copy('native.truncated'),
                style: TextStyle(color: context.viberColors.warning),
              ),
          ],
          if (!approval.canAllow)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 8),
              child: Text(copy('native.unavailable')),
            ),
          const SizedBox(height: 8),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              FilledButton(
                key: Key('native-approval-${approval.id}-allow-once'),
                onPressed: approval.canAllow
                    ? () => onDecision(NativeDecision.allowOnce)
                    : null,
                child: Text(copy('native.allowOnce')),
              ),
              OutlinedButton(
                key: Key('native-approval-${approval.id}-deny'),
                onPressed: approval.pending
                    ? () => onDecision(NativeDecision.deny)
                    : null,
                child: Text(copy('native.deny')),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

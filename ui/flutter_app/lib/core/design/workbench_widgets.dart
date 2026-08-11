import 'package:flutter/material.dart';

import 'viber_theme.dart';

final class StatusPill extends StatelessWidget {
  const StatusPill({
    required this.label,
    this.color = ViberColors.verified,
    this.icon,
    super.key,
  });

  final String label;
  final Color color;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: label,
      container: true,
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.11),
          border: Border.all(color: color.withValues(alpha: 0.42)),
          borderRadius: BorderRadius.circular(999),
        ),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(
                icon ?? Icons.circle,
                size: icon == null ? 6 : 12,
                color: color,
              ),
              const SizedBox(width: 5),
              Text(
                label,
                style: TextStyle(
                  color: color,
                  fontSize: 10.5,
                  height: 1.15,
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

final class SectionLabel extends StatelessWidget {
  const SectionLabel({
    required this.label,
    this.count,
    this.trailing,
    super.key,
  });

  final String label;
  final int? count;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: 30,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 10),
        child: Row(
          children: [
            Text(
              label.toUpperCase(),
              style: Theme.of(context).textTheme.labelMedium,
            ),
            if (count case final value?) ...[
              const SizedBox(width: 6),
              Text(
                '$value',
                style: Theme.of(
                  context,
                ).textTheme.labelMedium?.copyWith(color: ViberColors.textFaint),
              ),
            ],
            const Spacer(),
            ?trailing,
          ],
        ),
      ),
    );
  }
}

final class PageHeading extends StatelessWidget {
  const PageHeading({
    required this.title,
    required this.subtitle,
    this.trailing,
    super.key,
  });

  final String title;
  final String subtitle;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(18, 16, 16, 12),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title, style: Theme.of(context).textTheme.headlineLarge),
                const SizedBox(height: 3),
                Text(subtitle, style: Theme.of(context).textTheme.bodySmall),
              ],
            ),
          ),
          ?trailing,
        ],
      ),
    );
  }
}

final class InlineNotice extends StatelessWidget {
  const InlineNotice({
    required this.message,
    this.error = false,
    this.onDismiss,
    super.key,
  });

  final String message;
  final bool error;
  final VoidCallback? onDismiss;

  @override
  Widget build(BuildContext context) {
    final color = error ? ViberColors.danger : ViberColors.route;
    return Semantics(
      liveRegion: true,
      label: message,
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.1),
          border: Border(
            left: BorderSide(color: color, width: 2),
            bottom: const BorderSide(color: ViberColors.dividerSoft),
          ),
        ),
        child: Row(
          children: [
            Icon(
              error ? Icons.error_outline : Icons.info_outline,
              size: 14,
              color: color,
            ),
            const SizedBox(width: 7),
            Expanded(
              child: Text(
                message,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
            if (onDismiss case final action?)
              IconButton(
                onPressed: action,
                tooltip: 'Dismiss',
                icon: const Icon(Icons.close, size: 14),
                constraints: const BoxConstraints.tightFor(
                  width: 26,
                  height: 26,
                ),
                padding: EdgeInsets.zero,
              ),
          ],
        ),
      ),
    );
  }
}

final class FlowNode {
  const FlowNode({
    required this.kind,
    required this.label,
    this.detail,
    this.tone,
  });

  final String kind;
  final String label;
  final String? detail;
  final Color? tone;
}

final class FlowSpine extends StatelessWidget {
  const FlowSpine({required this.nodes, super.key});

  final List<FlowNode> nodes;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: nodes.map((node) => '${node.kind}: ${node.label}').join(', '),
      child: SizedBox(
        height: 56,
        child: ListView.separated(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 7),
          scrollDirection: Axis.horizontal,
          itemCount: nodes.length,
          separatorBuilder: (context, index) => const _FlowConnector(),
          itemBuilder: (context, index) => _FlowNodeView(node: nodes[index]),
        ),
      ),
    );
  }
}

final class _FlowNodeView extends StatelessWidget {
  const _FlowNodeView({required this.node});

  final FlowNode node;

  @override
  Widget build(BuildContext context) {
    final tone = node.tone ?? ViberColors.route;
    return Container(
      constraints: const BoxConstraints(minWidth: 96, maxWidth: 180),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 5),
      decoration: BoxDecoration(
        color: tone.withValues(alpha: 0.08),
        borderRadius: BorderRadius.circular(4),
        border: Border.all(color: tone.withValues(alpha: 0.28)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            node.kind.toUpperCase(),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(
              context,
            ).textTheme.labelMedium?.copyWith(color: tone, fontSize: 8.5),
          ),
          Text(
            node.label,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(
              context,
            ).textTheme.titleSmall?.copyWith(fontSize: 11),
          ),
        ],
      ),
    );
  }
}

final class _FlowConnector extends StatelessWidget {
  const _FlowConnector();

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 28,
      child: Row(
        children: [
          const Expanded(
            child: Divider(color: ViberColors.selectionStrong, height: 1),
          ),
          Transform.translate(
            offset: const Offset(-2, 0),
            child: const Icon(
              Icons.arrow_right,
              size: 13,
              color: ViberColors.route,
            ),
          ),
        ],
      ),
    );
  }
}

final class CenteredMessage extends StatelessWidget {
  const CenteredMessage({
    required this.icon,
    required this.title,
    this.detail,
    super.key,
  });

  final IconData icon;
  final String title;
  final String? detail;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 360),
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: 28, color: ViberColors.textFaint),
              const SizedBox(height: 10),
              Text(
                title,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleMedium,
              ),
              if (detail case final value?) ...[
                const SizedBox(height: 5),
                Text(
                  value,
                  textAlign: TextAlign.center,
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}

import 'package:flutter/material.dart';

import 'viber_theme.dart';

/// A restrained desktop loading mark for bounded workbench regions.
///
/// Material's unconstrained circular indicator is sized as a primary mobile
/// affordance. Evidence panels only need a quiet progress cue, so they share
/// this smaller geometry instead of each choosing an arbitrary spinner size.
final class CompactProgressIndicator extends StatelessWidget {
  const CompactProgressIndicator({this.semanticsLabel, super.key});

  final String? semanticsLabel;

  @override
  Widget build(BuildContext context) {
    return SizedBox.square(
      dimension: ViberMetrics.compactProgressSize,
      child: CircularProgressIndicator(
        strokeWidth: 1.4,
        semanticsLabel: semanticsLabel,
      ),
    );
  }
}

/// Quiet loading feedback for a bounded desktop pane.
///
/// A row keeps the indicator and label on one baseline, so loading does not
/// read as a primary empty state or pull attention away from neighboring
/// panes that are still usable.
final class CompactLoadingMessage extends StatelessWidget {
  const CompactLoadingMessage({required this.label, super.key});

  final String label;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Semantics(
        container: true,
        liveRegion: true,
        label: label,
        child: ExcludeSemantics(
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              const CompactProgressIndicator(),
              const SizedBox(width: ViberSpacing.sm),
              Text(
                label,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: context.viberColors.textMuted,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// A narrow desktop split handle. The adjacent pane must also expose an
/// ordinary button for collapsing it, so resizing is never the only path.
final class WorkbenchPaneDivider extends StatelessWidget {
  const WorkbenchPaneDivider({
    required this.onDrag,
    required this.label,
    super.key,
  });

  final ValueChanged<double> onDrag;
  final String label;

  @override
  Widget build(BuildContext context) {
    return Tooltip(
      message: label,
      child: MouseRegion(
        cursor: SystemMouseCursors.resizeColumn,
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onHorizontalDragUpdate: (details) => onDrag(details.delta.dx),
          child: SizedBox(
            width: ViberMetrics.splitDividerWidth,
            child: Center(
              child: VerticalDivider(
                width: 1,
                color: context.viberColors.divider,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

@immutable
final class CompactSegment<T> {
  const CompactSegment({required this.value, required this.label, this.icon});

  final T value;
  final String label;
  final IconData? icon;
}

/// A desktop-sized segmented selector. Material's segmented button retains
/// touch-first internal minimums even when the surrounding theme is compact.
final class CompactSegmentedControl<T> extends StatelessWidget {
  const CompactSegmentedControl({
    required this.segments,
    required this.selected,
    required this.onSelected,
    this.minSegmentWidth = 58,
    super.key,
  });

  final List<CompactSegment<T>> segments;
  final T selected;
  final ValueChanged<T>? onSelected;
  final double minSegmentWidth;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      container: true,
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: context.viberColors.panel,
          border: Border.all(color: context.viberColors.divider),
          borderRadius: ViberMetrics.controlRadius,
        ),
        child: ClipRRect(
          borderRadius: ViberMetrics.controlRadius,
          child: SizedBox(
            height: ViberMetrics.controlHeight,
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                for (var index = 0; index < segments.length; index++)
                  _CompactSegmentButton<T>(
                    segment: segments[index],
                    selected: segments[index].value == selected,
                    enabled: onSelected != null,
                    minWidth: minSegmentWidth,
                    drawDivider: index > 0,
                    onPressed: onSelected == null
                        ? null
                        : () => onSelected!(segments[index].value),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

final class _CompactSegmentButton<T> extends StatelessWidget {
  const _CompactSegmentButton({
    required this.segment,
    required this.selected,
    required this.enabled,
    required this.minWidth,
    required this.drawDivider,
    required this.onPressed,
  });

  final CompactSegment<T> segment;
  final bool selected;
  final bool enabled;
  final double minWidth;
  final bool drawDivider;
  final VoidCallback? onPressed;

  @override
  Widget build(BuildContext context) {
    final foreground = selected
        ? context.viberColors.route
        : enabled
        ? context.viberColors.textMuted
        : context.viberColors.textFaint;
    return Semantics(
      button: true,
      selected: selected,
      enabled: enabled,
      label: segment.label,
      child: Material(
        color: selected ? context.viberColors.selection : Colors.transparent,
        child: InkWell(
          onTap: onPressed,
          focusColor: context.viberColors.focus.withValues(alpha: 0.13),
          child: Container(
            constraints: BoxConstraints(minWidth: minWidth),
            height: ViberMetrics.controlHeight,
            padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.md),
            alignment: Alignment.center,
            decoration: BoxDecoration(
              border: drawDivider
                  ? Border(left: BorderSide(color: context.viberColors.divider))
                  : null,
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              mainAxisAlignment: MainAxisAlignment.center,
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                if (segment.icon case final icon?) ...[
                  Icon(icon, size: 12, color: foreground),
                  const SizedBox(width: ViberSpacing.xs),
                ],
                Text(
                  segment.label,
                  maxLines: 1,
                  style: Theme.of(context).textTheme.labelLarge?.copyWith(
                    color: foreground,
                    height: 1,
                    fontWeight: selected ? FontWeight.w600 : FontWeight.w500,
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

/// Search field with an explicit desktop baseline. A fixed height prevents the
/// hint from drifting when prefix/suffix icons have different constraints.
final class CompactSearchField extends StatelessWidget {
  const CompactSearchField({
    required this.hintText,
    required this.onChanged,
    this.controller,
    this.onClear,
    this.clearLabel,
    super.key,
  });

  final String hintText;
  final ValueChanged<String> onChanged;
  final TextEditingController? controller;
  final VoidCallback? onClear;
  final String? clearLabel;

  @override
  Widget build(BuildContext context) {
    final canClear = onClear != null && (controller?.text.isNotEmpty ?? false);
    return SizedBox(
      height: ViberMetrics.searchHeight,
      child: TextField(
        controller: controller,
        onChanged: onChanged,
        textInputAction: TextInputAction.search,
        textAlignVertical: TextAlignVertical.center,
        cursorHeight: 13,
        style: Theme.of(context).textTheme.bodySmall?.copyWith(
          color: context.viberColors.text,
          height: 1,
        ),
        decoration: InputDecoration(
          hintText: hintText,
          contentPadding: EdgeInsets.zero,
          prefixIcon: const Icon(Icons.search, size: 15),
          prefixIconConstraints: const BoxConstraints.tightFor(
            width: ViberMetrics.searchHeight,
            height: ViberMetrics.searchHeight,
          ),
          suffixIcon: canClear
              ? IconButton(
                  onPressed: onClear,
                  tooltip: clearLabel,
                  icon: const Icon(Icons.close, size: 13),
                )
              : null,
          suffixIconConstraints: const BoxConstraints.tightFor(
            width: ViberMetrics.searchHeight,
            height: ViberMetrics.searchHeight,
          ),
        ),
      ),
    );
  }
}

/// External labels keep dense form fields legible without Material's floating
/// label geometry increasing every row to a mobile-sized control.
final class CompactLabeledControl extends StatelessWidget {
  const CompactLabeledControl({
    required this.label,
    required this.child,
    this.detail,
    super.key,
  });

  final String label;
  final Widget child;
  final String? detail;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          label,
          style: Theme.of(context).textTheme.labelMedium?.copyWith(
            color: context.viberColors.textMuted,
            letterSpacing: 0.1,
          ),
        ),
        const SizedBox(height: ViberSpacing.xs),
        child,
        if (detail case final value?) ...[
          const SizedBox(height: ViberSpacing.xs),
          Text(value, style: Theme.of(context).textTheme.bodySmall),
        ],
      ],
    );
  }
}

/// Desktop select control with menu rows that follow the workbench's compact
/// geometry. Flutter's [DropdownButton] hard-codes 48px popup rows, which is
/// appropriate for touch surfaces but visually coarse in this desktop app.
final class CompactSelectField<T> extends StatefulWidget {
  const CompactSelectField({
    required this.items,
    required this.onChanged,
    this.initialValue,
    this.decoration = const InputDecoration(),
    this.isExpanded = false,
    this.menuItemHeight = ViberMetrics.controlHeight,
    this.menuMaxLines = 1,
    this.selectedItemBuilder,
    this.validator,
    super.key,
  });

  final List<DropdownMenuItem<T>> items;
  final ValueChanged<T?>? onChanged;
  final T? initialValue;
  final InputDecoration decoration;
  final bool isExpanded;
  final double menuItemHeight;
  final int menuMaxLines;
  final Widget Function(BuildContext context, DropdownMenuItem<T> selectedItem)?
  selectedItemBuilder;
  final FormFieldValidator<T>? validator;

  @override
  State<CompactSelectField<T>> createState() => _CompactSelectFieldState<T>();
}

final class _CompactSelectFieldState<T> extends State<CompactSelectField<T>> {
  T? _value;
  bool _focused = false;

  @override
  void initState() {
    super.initState();
    _value = widget.initialValue;
  }

  @override
  void didUpdateWidget(covariant CompactSelectField<T> oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.initialValue != widget.initialValue) {
      _value = widget.initialValue;
    }
  }

  @override
  Widget build(BuildContext context) {
    return FormField<T>(
      key: ValueKey(_value),
      initialValue: _value,
      validator: widget.validator,
      builder: (field) {
        final enabled = widget.onChanged != null && widget.items.isNotEmpty;
        final selected = widget.items
            .where((item) => item.value == field.value)
            .firstOrNull;
        return LayoutBuilder(
          builder: (context, constraints) {
            final menuWidth = constraints.hasBoundedWidth
                ? constraints.maxWidth
                : null;
            final menuHeight = (widget.items.length * widget.menuItemHeight + 4)
                .clamp(32.0, 240.0)
                .toDouble();
            return MenuAnchor(
              crossAxisUnconstrained: false,
              alignmentOffset: const Offset(0, ViberSpacing.xs),
              style: menuWidth == null
                  ? null
                  : MenuStyle(
                      minimumSize: WidgetStatePropertyAll(Size(menuWidth, 0)),
                      maximumSize: WidgetStatePropertyAll(
                        Size(menuWidth, menuHeight),
                      ),
                    ),
              menuChildren: [
                for (final item in widget.items)
                  MenuItemButton(
                    style: ButtonStyle(
                      minimumSize: WidgetStatePropertyAll(
                        Size(0, widget.menuItemHeight),
                      ),
                      maximumSize: WidgetStatePropertyAll(
                        Size(double.infinity, widget.menuItemHeight),
                      ),
                    ),
                    onPressed: enabled && item.enabled
                        ? () {
                            item.onTap?.call();
                            setState(() => _value = item.value);
                            field.didChange(item.value);
                            widget.onChanged?.call(item.value);
                          }
                        : null,
                    leadingIcon: SizedBox(
                      width: 12,
                      child: item.value == field.value
                          ? Icon(
                              Icons.check,
                              size: 13,
                              color: context.viberColors.route,
                            )
                          : null,
                    ),
                    child: DefaultTextStyle(
                      style:
                          Theme.of(context).textTheme.bodySmall?.copyWith(
                            color: context.viberColors.text,
                            fontWeight: FontWeight.w400,
                          ) ??
                          const TextStyle(),
                      maxLines: widget.menuMaxLines,
                      overflow: TextOverflow.ellipsis,
                      child: item.child,
                    ),
                  ),
              ],
              builder: (context, controller, _) {
                final active = _focused || controller.isOpen;
                return Semantics(
                  button: true,
                  enabled: enabled,
                  expanded: controller.isOpen,
                  child: InkWell(
                    onTap: enabled
                        ? () => controller.isOpen
                              ? controller.close()
                              : controller.open()
                        : null,
                    onFocusChange: (value) {
                      if (_focused != value) setState(() => _focused = value);
                    },
                    canRequestFocus: enabled,
                    focusColor: Colors.transparent,
                    hoverColor: Colors.transparent,
                    borderRadius: ViberMetrics.controlRadius,
                    child: InputDecorator(
                      isEmpty: selected == null,
                      isFocused: active,
                      decoration: widget.decoration.copyWith(
                        enabled: enabled,
                        errorText:
                            widget.decoration.errorText ?? field.errorText,
                        suffixIcon: Icon(
                          controller.isOpen
                              ? Icons.arrow_drop_up
                              : Icons.arrow_drop_down,
                          size: 17,
                        ),
                        suffixIconConstraints: const BoxConstraints.tightFor(
                          width: ViberMetrics.controlHeight,
                          height: ViberMetrics.controlHeight,
                        ),
                      ),
                      child: DefaultTextStyle(
                        style:
                            Theme.of(context).textTheme.labelLarge?.copyWith(
                              color: context.viberColors.text,
                              fontWeight: FontWeight.w400,
                              height: 1,
                            ) ??
                            const TextStyle(),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        child: selected == null
                            ? const SizedBox.shrink()
                            : widget.selectedItemBuilder?.call(
                                    context,
                                    selected,
                                  ) ??
                                  selected.child,
                      ),
                    ),
                  ),
                );
              },
            );
          },
        );
      },
    );
  }
}

final class StatusPill extends StatelessWidget {
  const StatusPill({required this.label, this.color, this.icon, super.key});

  final String label;
  final Color? color;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    final tone = color ?? context.viberColors.verified;
    return Semantics(
      label: label,
      container: true,
      child: ConstrainedBox(
        constraints: const BoxConstraints(
          minHeight: ViberMetrics.statusPillHeight,
        ),
        child: DecoratedBox(
          decoration: BoxDecoration(
            color: tone.withValues(alpha: 0.11),
            border: Border.all(color: tone.withValues(alpha: 0.42)),
            borderRadius: ViberMetrics.pillRadius,
          ),
          child: Padding(
            padding: const EdgeInsets.symmetric(
              horizontal: ViberSpacing.sm,
              vertical: ViberSpacing.xxs,
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  icon ?? Icons.circle,
                  size: icon == null ? 6 : 12,
                  color: tone,
                ),
                const SizedBox(width: ViberSpacing.xs),
                Text(
                  label,
                  style: Theme.of(context).textTheme.labelMedium?.copyWith(
                    color: tone,
                    fontSize: ViberType.utility,
                    height: 1.15,
                    fontWeight: FontWeight.w600,
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

/// Compact, non-interactive state treatment. Use this beside object metadata;
/// actions belong in a separate action cluster.
final class InlineStatus extends StatelessWidget {
  const InlineStatus({required this.label, required this.color, super.key});

  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: label,
      container: true,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(Icons.circle, size: 6, color: color),
          const SizedBox(width: ViberSpacing.xs),
          Text(
            label,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: color,
              fontWeight: FontWeight.w500,
            ),
          ),
        ],
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
      height: ViberMetrics.compactRowHeight,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.lg),
        child: Row(
          children: [
            Text(
              label.toUpperCase(),
              style: Theme.of(context).textTheme.labelMedium,
            ),
            if (count case final value?) ...[
              const SizedBox(width: ViberSpacing.sm),
              Text(
                '$value',
                style: Theme.of(context).textTheme.labelMedium?.copyWith(
                  color: context.viberColors.textFaint,
                ),
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
    return LayoutBuilder(
      builder: (context, constraints) {
        final compact = constraints.maxWidth < 520;
        final titleStyle = compact
            ? Theme.of(context).textTheme.headlineSmall?.copyWith(fontSize: 16)
            : Theme.of(context).textTheme.headlineLarge;
        return Padding(
          padding: EdgeInsets.symmetric(
            horizontal: ViberSpacing.lg,
            vertical: compact ? ViberSpacing.sm : ViberSpacing.md,
          ),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: titleStyle,
                    ),
                    const SizedBox(height: ViberSpacing.xs),
                    Text(
                      subtitle,
                      maxLines: compact ? 2 : 1,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
              if (trailing case final action?) ...[
                const SizedBox(width: ViberSpacing.md),
                action,
              ],
            ],
          ),
        );
      },
    );
  }
}

final class InlineNotice extends StatelessWidget {
  const InlineNotice({
    required this.message,
    this.error = false,
    this.onDismiss,
    this.dismissLabel = 'Dismiss',
    super.key,
  });

  final String message;
  final bool error;
  final VoidCallback? onDismiss;
  final String dismissLabel;

  @override
  Widget build(BuildContext context) {
    final color = error
        ? context.viberColors.danger
        : context.viberColors.route;
    return Semantics(
      liveRegion: true,
      label: message,
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.symmetric(
          horizontal: ViberSpacing.lg,
          vertical: ViberSpacing.sm,
        ),
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.1),
          border: Border(
            left: BorderSide(color: color, width: 2),
            bottom: BorderSide(color: context.viberColors.dividerSoft),
          ),
        ),
        child: Row(
          children: [
            Icon(
              error ? Icons.error_outline : Icons.info_outline,
              size: 14,
              color: color,
            ),
            const SizedBox(width: ViberSpacing.md),
            Expanded(
              child: Text(
                message,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
            if (onDismiss case final action?)
              IconButton(
                onPressed: action,
                tooltip: dismissLabel,
                icon: const Icon(Icons.close, size: 14),
                constraints: const BoxConstraints.tightFor(
                  width: ViberMetrics.controlHeight,
                  height: ViberMetrics.controlHeight,
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
        height: 36,
        child: ListView.separated(
          padding: const EdgeInsets.symmetric(horizontal: ViberSpacing.lg),
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
    final tone = node.tone ?? context.viberColors.route;
    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 164),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Text(
            node.kind.toUpperCase(),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: Theme.of(context).textTheme.labelMedium?.copyWith(
              color: tone,
              fontSize: ViberType.micro,
            ),
          ),
          Tooltip(
            message: node.detail ?? node.label,
            child: Text(
              node.label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: context.viberColors.text,
                fontWeight: FontWeight.w500,
              ),
            ),
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
      width: 18,
      child: Center(
        child: Icon(
          Icons.chevron_right,
          size: 14,
          color: context.viberColors.textFaint,
        ),
      ),
    );
  }
}

final class CenteredMessage extends StatelessWidget {
  const CenteredMessage({
    required this.icon,
    required this.title,
    this.detail,
    this.action,
    super.key,
  });

  final IconData icon;
  final String title;
  final String? detail;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 320),
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: 20, color: context.viberColors.textFaint),
              const SizedBox(height: ViberSpacing.sm),
              Text(
                title,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleSmall,
              ),
              if (detail case final value?) ...[
                const SizedBox(height: ViberSpacing.xs),
                Text(
                  value,
                  textAlign: TextAlign.center,
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
              if (action case final value?) ...[
                const SizedBox(height: ViberSpacing.md),
                value,
              ],
            ],
          ),
        ),
      ),
    );
  }
}

import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';

import 'viber_theme.dart';

@immutable
final class AgentIdentity {
  const AgentIdentity._({
    required this.id,
    required this.label,
    required this.assetPath,
  });

  static const claudeCode = AgentIdentity._(
    id: 'claude-code',
    label: 'Claude Code',
    assetPath: 'assets/agent-icons/claude-code.svg',
  );
  static const codex = AgentIdentity._(
    id: 'codex',
    label: 'Codex',
    assetPath: 'assets/agent-icons/codex.svg',
  );
  static const figma = AgentIdentity._(
    id: 'figma',
    label: 'Figma',
    assetPath: 'assets/agent-icons/figma.svg',
  );

  final String id;
  final String label;
  final String assetPath;

  /// Resolves only closed, exact aliases. A custom client whose label merely
  /// mentions a known product must not inherit that product's brand identity.
  static AgentIdentity? resolve(Iterable<String?> candidates) {
    for (final candidate in candidates) {
      final normalized = _normalize(candidate);
      final identity = switch (normalized) {
        'claude' || 'claude code' || 'anthropic claude code' => claudeCode,
        'codex' || 'codex cli' || 'openai codex' => codex,
        'figma' || 'figma desktop' => figma,
        _ => null,
      };
      if (identity != null) return identity;
    }
    return null;
  }

  static String _normalize(String? value) {
    if (value == null) return '';
    final basename = value.trim().toLowerCase().split(RegExp(r'[/\\]')).last;
    return basename
        .replaceFirst(RegExp(r'\.js$'), '')
        .replaceAll(RegExp(r'[_-]+'), ' ')
        .replaceAll(RegExp(r'\s+'), ' ')
        .trim();
  }
}

/// A quiet, size-stable identity mark for one known Agent/client.
///
/// The adjacent text remains the accessible name. The mark is decorative so
/// screen readers do not announce the same product name twice.
final class AgentIdentityMark extends StatelessWidget {
  const AgentIdentityMark({
    required this.candidates,
    required this.fallbackLabel,
    required this.fallbackIcon,
    this.fallbackColor,
    this.muted = false,
    this.size = ViberMetrics.controlHeight,
    this.glyphSize = 16,
    super.key,
  });

  final List<String?> candidates;
  final String fallbackLabel;
  final IconData fallbackIcon;
  final Color? fallbackColor;
  final bool muted;
  final double size;
  final double glyphSize;

  @override
  Widget build(BuildContext context) {
    final identity = AgentIdentity.resolve(candidates);
    final color = fallbackColor ?? context.viberColors.textMuted;
    final mark = Container(
      key: Key('agent-logo-${identity?.id ?? 'fallback'}'),
      width: size,
      height: size,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: context.viberColors.panelRaised,
        border: Border.all(color: context.viberColors.dividerSoft),
        borderRadius: ViberMetrics.controlRadius,
      ),
      child: ColorFiltered(
        colorFilter: muted
            ? ColorFilter.mode(context.viberColors.textFaint, BlendMode.srcIn)
            : const ColorFilter.mode(Colors.transparent, BlendMode.dst),
        child: identity == null
            ? Icon(fallbackIcon, size: glyphSize, color: color)
            : SvgPicture.asset(
                identity.assetPath,
                width: glyphSize,
                height: glyphSize,
                fit: BoxFit.contain,
                excludeFromSemantics: true,
              ),
      ),
    );
    return Tooltip(
      message: identity?.label ?? fallbackLabel,
      excludeFromSemantics: true,
      child: ExcludeSemantics(child: mark),
    );
  }
}

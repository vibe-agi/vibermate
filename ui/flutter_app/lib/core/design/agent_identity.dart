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

/// The filter an inactive mark is drawn with.
///
/// Two earlier answers were both wrong. Replacing every opaque pixel with one
/// colour turned a brand mark into a solid block that no longer read as an
/// icon. Plain greyscale kept the shape but not the contrast: Codex's mark ends
/// in #3941FF, whose luma is 77, so on a dark panel it became the dark blob
/// this filter exists to prevent.
///
/// So the mark is desaturated to luma and then compressed into a narrow band
/// centred on the theme's own muted foreground. Detail survives, and contrast
/// is whatever the palette already guarantees for muted text.
ColorFilter _mutedMarkFilter(Color tone) {
  final band = MutedMarkBand.forTone(tone);
  return ColorFilter.matrix(<double>[
    for (var channel = 0; channel < 3; channel++) ...[
      MutedMarkBand.luma[0] * band.detail,
      MutedMarkBand.luma[1] * band.detail,
      MutedMarkBand.luma[2] * band.detail,
      0,
      band.offset, //
    ],
    0, 0, 0, 1, 0, //
  ]);
}

/// The luma band an inactive mark is compressed into, and the arithmetic that
/// places it. It is separate from the filter so the property that matters —
/// that every source colour lands at a readable tone — can be asserted rather
/// than eyeballed.
@immutable
final class MutedMarkBand {
  const MutedMarkBand({required this.detail, required this.offset});

  /// sRGB luma weights.
  static const luma = [0.2126, 0.7152, 0.0722];

  /// How much of the source range survives. Enough to keep a silhouette
  /// readable, little enough that the darkest brand colour cannot reach the
  /// panel behind it.
  static const detailRatio = 0.34;

  factory MutedMarkBand.forTone(Color tone) {
    final centre =
        255 * (luma[0] * tone.r + luma[1] * tone.g + luma[2] * tone.b);
    return MutedMarkBand(
      detail: detailRatio,
      offset: centre - 255 * detailRatio / 2,
    );
  }

  final double detail;
  final double offset;

  /// Where a source colour lands, in 0..255 luma.
  double resolve(Color source) =>
      255 *
          (luma[0] * source.r + luma[1] * source.g + luma[2] * source.b) *
          detail +
      offset;
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
        // An inactive mark is desaturated, not flattened. srcIn replaced every
        // opaque pixel with one colour, which turned a brand mark into a solid
        // block: it stopped reading as an icon and, at this size, carried more
        // visual weight than the running Captures above it. A saturation matrix
        // keeps the silhouette and the internal detail while making it grey.
        colorFilter: muted
            ? _mutedMarkFilter(context.viberColors.textFaint)
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

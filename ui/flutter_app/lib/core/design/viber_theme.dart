import 'package:flutter/material.dart';

const viberSystemFontFamily = '.AppleSystemUIFont';
const viberFontFallback = <String>['PingFang SC', 'Hiragino Sans GB'];

@immutable
final class ViberColors extends ThemeExtension<ViberColors> {
  const ViberColors({
    required this.canvas,
    required this.rail,
    required this.panel,
    required this.panelRaised,
    required this.selection,
    required this.selectionStrong,
    required this.divider,
    required this.dividerSoft,
    required this.text,
    required this.textMuted,
    required this.textFaint,
    required this.route,
    required this.verified,
    required this.warning,
    required this.danger,
    required this.focus,
    required this.input,
  });

  static const light = ViberColors(
    canvas: Color(0xFFF3F6F9),
    rail: Color(0xFFE9EEF3),
    panel: Color(0xFFFFFFFF),
    panelRaised: Color(0xFFF7F9FB),
    selection: Color(0xFFD9E9F7),
    selectionStrong: Color(0xFF1769A6),
    divider: Color(0xFFB8C4D0),
    dividerSoft: Color(0xFFD6DEE6),
    text: Color(0xFF17212B),
    textMuted: Color(0xFF526170),
    textFaint: Color(0xFF6E7D8B),
    route: Color(0xFF1769A6),
    verified: Color(0xFF1E7653),
    warning: Color(0xFF93600D),
    danger: Color(0xFFB13B48),
    focus: Color(0xFF006FC4),
    input: Color(0xFFFFFFFF),
  );

  static const dark = ViberColors(
    canvas: Color(0xFF10151A),
    rail: Color(0xFF0B1116),
    panel: Color(0xFF182028),
    panelRaised: Color(0xFF222C35),
    selection: Color(0xFF25425E),
    selectionStrong: Color(0xFF3979AC),
    divider: Color(0xFF465462),
    dividerSoft: Color(0xFF303C47),
    text: Color(0xFFF1F5F8),
    textMuted: Color(0xFFB7C1CB),
    textFaint: Color(0xFF8996A3),
    route: Color(0xFF75B8F0),
    verified: Color(0xFF72CFA5),
    warning: Color(0xFFE5B663),
    danger: Color(0xFFF0848E),
    focus: Color(0xFF9ACFFF),
    input: Color(0xFF11171D),
  );

  final Color canvas;
  final Color rail;
  final Color panel;
  final Color panelRaised;
  final Color selection;
  final Color selectionStrong;
  final Color divider;
  final Color dividerSoft;
  final Color text;
  final Color textMuted;
  final Color textFaint;
  final Color route;
  final Color verified;
  final Color warning;
  final Color danger;
  final Color focus;
  final Color input;

  @override
  ViberColors copyWith({
    Color? canvas,
    Color? rail,
    Color? panel,
    Color? panelRaised,
    Color? selection,
    Color? selectionStrong,
    Color? divider,
    Color? dividerSoft,
    Color? text,
    Color? textMuted,
    Color? textFaint,
    Color? route,
    Color? verified,
    Color? warning,
    Color? danger,
    Color? focus,
    Color? input,
  }) => ViberColors(
    canvas: canvas ?? this.canvas,
    rail: rail ?? this.rail,
    panel: panel ?? this.panel,
    panelRaised: panelRaised ?? this.panelRaised,
    selection: selection ?? this.selection,
    selectionStrong: selectionStrong ?? this.selectionStrong,
    divider: divider ?? this.divider,
    dividerSoft: dividerSoft ?? this.dividerSoft,
    text: text ?? this.text,
    textMuted: textMuted ?? this.textMuted,
    textFaint: textFaint ?? this.textFaint,
    route: route ?? this.route,
    verified: verified ?? this.verified,
    warning: warning ?? this.warning,
    danger: danger ?? this.danger,
    focus: focus ?? this.focus,
    input: input ?? this.input,
  );

  @override
  ViberColors lerp(covariant ViberColors? other, double t) {
    if (other == null) return this;
    return ViberColors(
      canvas: Color.lerp(canvas, other.canvas, t)!,
      rail: Color.lerp(rail, other.rail, t)!,
      panel: Color.lerp(panel, other.panel, t)!,
      panelRaised: Color.lerp(panelRaised, other.panelRaised, t)!,
      selection: Color.lerp(selection, other.selection, t)!,
      selectionStrong: Color.lerp(selectionStrong, other.selectionStrong, t)!,
      divider: Color.lerp(divider, other.divider, t)!,
      dividerSoft: Color.lerp(dividerSoft, other.dividerSoft, t)!,
      text: Color.lerp(text, other.text, t)!,
      textMuted: Color.lerp(textMuted, other.textMuted, t)!,
      textFaint: Color.lerp(textFaint, other.textFaint, t)!,
      route: Color.lerp(route, other.route, t)!,
      verified: Color.lerp(verified, other.verified, t)!,
      warning: Color.lerp(warning, other.warning, t)!,
      danger: Color.lerp(danger, other.danger, t)!,
      focus: Color.lerp(focus, other.focus, t)!,
      input: Color.lerp(input, other.input, t)!,
    );
  }
}

extension ViberThemeContext on BuildContext {
  ViberColors get viberColors {
    final palette = Theme.of(this).extension<ViberColors>();
    assert(palette != null, 'ViberColors is missing from ThemeData');
    return palette!;
  }
}

/// Geometry shared by the desktop workbench.
///
/// Structural regions (rails, tables, split panes) deliberately stay square.
/// Only interactive controls and bounded content surfaces receive a radius.
abstract final class ViberMetrics {
  static const double controlHeight = 26;
  static const double searchHeight = 28;
  static const double compactRowHeight = 26;
  static const double toolbarHeight = 42;
  static const double statusPillHeight = 20;
  static const double compactProgressSize = 16;
  static const double masterPaneWidth = 268;
  static const double masterPaneMinWidth = 220;
  static const double masterPaneMaxWidth = 360;
  static const double splitDividerWidth = 5;

  static const BorderRadius controlRadius = BorderRadius.all(
    Radius.circular(4),
  );
  static const BorderRadius surfaceRadius = BorderRadius.all(
    Radius.circular(8),
  );
  static const BorderRadius dialogRadius = BorderRadius.all(
    Radius.circular(10),
  );
  static const BorderRadius pillRadius = BorderRadius.all(Radius.circular(999));
}

/// A deliberately small type scale for a dense desktop evidence workbench.
abstract final class ViberType {
  static const double page = 20;
  static const double dialog = 15;
  static const double title = 12.5;
  static const double body = 12;
  static const double supporting = 11;
  static const double control = 11;
  // InputDecorator scales a floating label to 75%. Starting at 12.5 keeps the
  // rendered desktop label near the 9-10px utility-label target instead of
  // shrinking an already-small 10px style to an illegible 7.5px.
  static const double floatingFieldLabel = 12.5;
  static const double utility = 10;
  static const double micro = 9;
}

/// Layout follows a 4-point rhythm. The 2px token is reserved for strokes,
/// optical alignment, and micro gaps inside an already bounded control.
abstract final class ViberSpacing {
  static const double xxs = 2;
  static const double xs = 4;
  static const double sm = 6;
  static const double md = 8;
  static const double lg = 12;
  static const double xl = 16;
  static const double xxl = 24;
}

abstract final class ViberTheme {
  static ThemeData light() =>
      _build(brightness: Brightness.light, colors: ViberColors.light);

  static ThemeData dark() =>
      _build(brightness: Brightness.dark, colors: ViberColors.dark);

  static ThemeData _build({
    required Brightness brightness,
    required ViberColors colors,
  }) {
    final dark = brightness == Brightness.dark;
    final onAccent = dark ? const Color(0xFF07131D) : Colors.white;
    final scheme =
        (dark
                ? ColorScheme.dark(
                    primary: colors.route,
                    onPrimary: onAccent,
                    secondary: colors.verified,
                    onSecondary: onAccent,
                    error: colors.danger,
                    onError: const Color(0xFF250509),
                    surface: colors.panel,
                    onSurface: colors.text,
                    surfaceContainerHighest: colors.panelRaised,
                    outline: colors.divider,
                    outlineVariant: colors.dividerSoft,
                  )
                : ColorScheme.light(
                    primary: colors.route,
                    onPrimary: onAccent,
                    secondary: colors.verified,
                    onSecondary: Colors.white,
                    error: colors.danger,
                    onError: Colors.white,
                    surface: colors.panel,
                    onSurface: colors.text,
                    surfaceContainerHighest: colors.panelRaised,
                    outline: colors.divider,
                    outlineVariant: colors.dividerSoft,
                  ))
            .copyWith(surfaceTint: Colors.transparent);
    final base = ThemeData(
      brightness: brightness,
      colorScheme: scheme,
      canvasColor: colors.panel,
      scaffoldBackgroundColor: colors.canvas,
      fontFamily: viberSystemFontFamily,
      // Keep desktop controls horizontally economical without silently
      // shrinking the 30px control-height token along the vertical axis.
      visualDensity: const VisualDensity(horizontal: -1, vertical: 0),
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      splashFactory: InkSparkle.splashFactory,
      dividerColor: colors.divider,
      focusColor: colors.focus.withValues(alpha: dark ? 0.2 : 0.14),
      hoverColor: (dark ? Colors.white : colors.text).withValues(alpha: 0.055),
      extensions: [colors],
    );
    return base.copyWith(
      iconTheme: IconThemeData(color: colors.textMuted, size: 18),
      textTheme: base.textTheme.copyWith(
        headlineLarge: TextStyle(
          color: colors.text,
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.page,
          height: 1.08,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.1,
        ),
        headlineSmall: TextStyle(
          color: colors.text,
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.dialog,
          height: 1.1,
          fontWeight: FontWeight.w600,
        ),
        titleMedium: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.title,
          height: 1.25,
          fontWeight: FontWeight.w600,
        ),
        titleSmall: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.supporting,
          height: 1.25,
          fontWeight: FontWeight.w600,
        ),
        bodyMedium: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.body,
          height: 1.38,
        ),
        bodyLarge: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          // Material fields inherit bodyLarge. Keep typed values on the same
          // compact scale as select triggers and menu rows.
          fontSize: ViberType.control,
          height: 1,
        ),
        bodySmall: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.supporting,
          height: 1.35,
        ),
        labelLarge: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.control,
          height: 1.2,
          fontWeight: FontWeight.w500,
        ),
        labelMedium: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.utility,
          height: 1.2,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.35,
        ),
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: colors.panel,
        surfaceTintColor: Colors.transparent,
        barrierColor: Colors.black.withValues(alpha: dark ? 0.56 : 0.32),
        elevation: dark ? 18 : 12,
        shape: RoundedRectangleBorder(
          borderRadius: ViberMetrics.dialogRadius,
          side: BorderSide(color: colors.divider),
        ),
        titleTextStyle: TextStyle(
          color: colors.text,
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.dialog,
          fontWeight: FontWeight.w600,
        ),
        contentTextStyle: TextStyle(
          color: colors.text,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.body,
          height: 1.38,
        ),
      ),
      tooltipTheme: TooltipThemeData(
        decoration: BoxDecoration(
          color: dark ? const Color(0xFF303A44) : const Color(0xFF25313C),
          borderRadius: ViberMetrics.controlRadius,
          border: Border.all(
            color: dark ? colors.divider : const Color(0xFF25313C),
          ),
        ),
        textStyle: const TextStyle(
          color: Colors.white,
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.supporting,
        ),
        waitDuration: const Duration(milliseconds: 450),
      ),
      inputDecorationTheme: InputDecorationTheme(
        isDense: true,
        filled: true,
        fillColor: colors.input,
        hintStyle: TextStyle(
          color: colors.textFaint,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.control,
          height: 1,
        ),
        labelStyle: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.supporting,
          height: 1,
        ),
        floatingLabelStyle: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.floatingFieldLabel,
          height: 1,
          fontWeight: FontWeight.w500,
        ),
        helperStyle: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.utility,
          height: 1.2,
        ),
        counterStyle: TextStyle(
          color: colors.textFaint,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.utility,
          height: 1.2,
        ),
        errorStyle: TextStyle(
          color: colors.danger,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.utility,
          height: 1.2,
        ),
        contentPadding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
        suffixStyle: TextStyle(
          color: colors.textMuted,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.control,
          height: 1,
        ),
        constraints: const BoxConstraints(
          minHeight: ViberMetrics.controlHeight,
        ),
        border: OutlineInputBorder(
          borderRadius: ViberMetrics.controlRadius,
          borderSide: BorderSide(color: colors.divider),
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: ViberMetrics.controlRadius,
          borderSide: BorderSide(color: colors.divider),
        ),
        focusedBorder: OutlineInputBorder(
          borderRadius: ViberMetrics.controlRadius,
          borderSide: BorderSide(color: colors.focus, width: 1.5),
        ),
      ),
      dropdownMenuTheme: DropdownMenuThemeData(
        textStyle: TextStyle(
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.control,
          color: colors.text,
        ),
        menuStyle: MenuStyle(
          backgroundColor: WidgetStatePropertyAll(colors.panel),
          surfaceTintColor: const WidgetStatePropertyAll(Colors.transparent),
          side: WidgetStatePropertyAll(BorderSide(color: colors.divider)),
          shape: const WidgetStatePropertyAll(
            RoundedRectangleBorder(borderRadius: ViberMetrics.surfaceRadius),
          ),
        ),
      ),
      menuTheme: MenuThemeData(
        style: MenuStyle(
          backgroundColor: WidgetStatePropertyAll(colors.panel),
          surfaceTintColor: const WidgetStatePropertyAll(Colors.transparent),
          elevation: const WidgetStatePropertyAll(2),
          shadowColor: WidgetStatePropertyAll(
            Colors.black.withValues(alpha: dark ? 0.24 : 0.12),
          ),
          side: WidgetStatePropertyAll(BorderSide(color: colors.divider)),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(vertical: ViberSpacing.xxs),
          ),
          shape: const WidgetStatePropertyAll(
            RoundedRectangleBorder(borderRadius: ViberMetrics.surfaceRadius),
          ),
        ),
      ),
      menuButtonTheme: MenuButtonThemeData(
        style: ButtonStyle(
          minimumSize: const WidgetStatePropertyAll(
            Size(0, ViberMetrics.controlHeight),
          ),
          maximumSize: const WidgetStatePropertyAll(
            Size(double.infinity, ViberMetrics.controlHeight),
          ),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(horizontal: ViberSpacing.md),
          ),
          textStyle: const WidgetStatePropertyAll(
            TextStyle(
              fontFamily: viberSystemFontFamily,
              fontFamilyFallback: viberFontFallback,
              fontSize: ViberType.control,
              fontWeight: FontWeight.w400,
            ),
          ),
          foregroundColor: WidgetStatePropertyAll(colors.text),
          backgroundColor: WidgetStateProperty.resolveWith((states) {
            if (states.contains(WidgetState.focused)) {
              return colors.focus.withValues(alpha: dark ? 0.14 : 0.07);
            }
            if (states.contains(WidgetState.hovered)) {
              return colors.selection;
            }
            return Colors.transparent;
          }),
          shape: const WidgetStatePropertyAll(
            RoundedRectangleBorder(borderRadius: ViberMetrics.controlRadius),
          ),
        ),
      ),
      popupMenuTheme: PopupMenuThemeData(
        color: colors.panel,
        surfaceTintColor: Colors.transparent,
        textStyle: TextStyle(
          color: colors.text,
          fontFamily: viberSystemFontFamily,
          fontFamilyFallback: viberFontFallback,
          fontSize: ViberType.control,
        ),
        shape: RoundedRectangleBorder(
          borderRadius: ViberMetrics.surfaceRadius,
          side: BorderSide(color: colors.divider),
        ),
      ),
      iconButtonTheme: IconButtonThemeData(
        style:
            IconButton.styleFrom(
              minimumSize: const Size.square(ViberMetrics.controlHeight),
              padding: EdgeInsets.zero,
              shape: const RoundedRectangleBorder(
                borderRadius: ViberMetrics.controlRadius,
              ),
            ).copyWith(
              side: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? BorderSide(color: colors.focus, width: 1.5)
                    : BorderSide.none,
              ),
              overlayColor: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? colors.focus.withValues(alpha: dark ? 0.24 : 0.14)
                    : null,
              ),
            ),
      ),
      textButtonTheme: TextButtonThemeData(
        style:
            TextButton.styleFrom(
              foregroundColor: colors.text,
              minimumSize: const Size(0, ViberMetrics.controlHeight),
              padding: const EdgeInsets.symmetric(horizontal: 8),
              textStyle: const TextStyle(
                fontFamily: viberSystemFontFamily,
                fontFamilyFallback: viberFontFallback,
                fontSize: ViberType.control,
                fontWeight: FontWeight.w500,
              ),
              shape: const RoundedRectangleBorder(
                borderRadius: ViberMetrics.controlRadius,
              ),
            ).copyWith(
              side: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? BorderSide(color: colors.focus, width: 1.5)
                    : BorderSide.none,
              ),
              overlayColor: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? colors.focus.withValues(alpha: dark ? 0.24 : 0.14)
                    : null,
              ),
            ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style:
            OutlinedButton.styleFrom(
              foregroundColor: colors.text,
              side: BorderSide(color: colors.divider),
              minimumSize: const Size(0, ViberMetrics.controlHeight),
              padding: const EdgeInsets.symmetric(horizontal: 8),
              textStyle: const TextStyle(
                fontFamily: viberSystemFontFamily,
                fontFamilyFallback: viberFontFallback,
                fontSize: ViberType.control,
                fontWeight: FontWeight.w500,
              ),
              shape: const RoundedRectangleBorder(
                borderRadius: ViberMetrics.controlRadius,
              ),
            ).copyWith(
              side: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? BorderSide(color: colors.focus, width: 1.5)
                    : BorderSide(color: colors.divider),
              ),
              overlayColor: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? colors.focus.withValues(alpha: dark ? 0.24 : 0.14)
                    : null,
              ),
            ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style:
            FilledButton.styleFrom(
              backgroundColor: colors.selectionStrong,
              foregroundColor: Colors.white,
              disabledBackgroundColor: colors.dividerSoft,
              disabledForegroundColor: colors.textFaint,
              minimumSize: const Size(0, ViberMetrics.controlHeight),
              padding: const EdgeInsets.symmetric(horizontal: 9),
              textStyle: const TextStyle(
                fontFamily: viberSystemFontFamily,
                fontFamilyFallback: viberFontFallback,
                fontSize: ViberType.control,
                fontWeight: FontWeight.w600,
              ),
              shape: const RoundedRectangleBorder(
                borderRadius: ViberMetrics.controlRadius,
              ),
            ).copyWith(
              side: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? BorderSide(color: colors.focus, width: 1.5)
                    : BorderSide.none,
              ),
              overlayColor: WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.focused)
                    ? colors.focus.withValues(alpha: dark ? 0.28 : 0.18)
                    : null,
              ),
            ),
      ),
      segmentedButtonTheme: SegmentedButtonThemeData(
        style: ButtonStyle(
          minimumSize: const WidgetStatePropertyAll(
            Size(0, ViberMetrics.controlHeight),
          ),
          padding: const WidgetStatePropertyAll(
            EdgeInsets.symmetric(horizontal: 6),
          ),
          textStyle: const WidgetStatePropertyAll(
            TextStyle(
              fontFamily: viberSystemFontFamily,
              fontFamilyFallback: viberFontFallback,
              fontSize: ViberType.control,
              fontWeight: FontWeight.w500,
            ),
          ),
          shape: const WidgetStatePropertyAll(
            RoundedRectangleBorder(borderRadius: ViberMetrics.controlRadius),
          ),
          foregroundColor: WidgetStateProperty.resolveWith(
            (states) => states.contains(WidgetState.selected)
                ? colors.route
                : colors.textMuted,
          ),
          backgroundColor: WidgetStateProperty.resolveWith(
            (states) => states.contains(WidgetState.selected)
                ? colors.selection
                : colors.panel,
          ),
          side: WidgetStatePropertyAll(BorderSide(color: colors.divider)),
        ),
      ),
      progressIndicatorTheme: ProgressIndicatorThemeData(
        color: colors.route,
        linearTrackColor: colors.dividerSoft,
      ),
    );
  }
}

const monoStyle = TextStyle(
  fontFamily: 'Menlo',
  fontFamilyFallback: viberFontFallback,
  fontSize: ViberType.utility,
  height: 1.35,
);

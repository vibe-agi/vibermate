import 'package:flutter/material.dart';

abstract final class ViberColors {
  static const canvas = Color(0xFF111418);
  static const rail = Color(0xFF0D1013);
  static const panel = Color(0xFF171B20);
  static const panelRaised = Color(0xFF1D2228);
  static const selection = Color(0xFF24364B);
  static const selectionStrong = Color(0xFF315477);
  static const divider = Color(0xFF2A3037);
  static const dividerSoft = Color(0xFF22282E);
  static const text = Color(0xFFE7EAF0);
  static const textMuted = Color(0xFF9DA7B3);
  static const textFaint = Color(0xFF6E7884);
  static const route = Color(0xFF6EACEA);
  static const verified = Color(0xFF6FC6A0);
  static const warning = Color(0xFFE1B56A);
  static const danger = Color(0xFFE27D86);
  static const focus = Color(0xFF8CC5FF);
}

abstract final class ViberTheme {
  static ThemeData dark() {
    final scheme = const ColorScheme.dark(
      primary: ViberColors.route,
      onPrimary: Color(0xFF07121E),
      secondary: ViberColors.verified,
      onSecondary: Color(0xFF06150F),
      error: ViberColors.danger,
      onError: Color(0xFF1D0508),
      surface: ViberColors.panel,
      onSurface: ViberColors.text,
      surfaceContainerHighest: ViberColors.panelRaised,
      outline: ViberColors.divider,
      outlineVariant: ViberColors.dividerSoft,
    );
    final base = ThemeData(
      brightness: Brightness.dark,
      colorScheme: scheme,
      scaffoldBackgroundColor: ViberColors.canvas,
      fontFamily: '.AppleSystemUIFont',
      visualDensity: VisualDensity.compact,
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      splashFactory: InkSparkle.splashFactory,
      dividerColor: ViberColors.divider,
      focusColor: ViberColors.focus.withValues(alpha: 0.16),
      hoverColor: Colors.white.withValues(alpha: 0.045),
    );
    return base.copyWith(
      textTheme: base.textTheme.copyWith(
        headlineLarge: const TextStyle(
          color: ViberColors.text,
          fontFamily: 'Avenir Next Condensed',
          fontSize: 26,
          height: 1.05,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.1,
        ),
        headlineSmall: const TextStyle(
          color: ViberColors.text,
          fontFamily: 'Avenir Next Condensed',
          fontSize: 20,
          height: 1.1,
          fontWeight: FontWeight.w600,
        ),
        titleMedium: const TextStyle(
          color: ViberColors.text,
          fontSize: 13,
          height: 1.25,
          fontWeight: FontWeight.w600,
        ),
        titleSmall: const TextStyle(
          color: ViberColors.text,
          fontSize: 12,
          height: 1.25,
          fontWeight: FontWeight.w600,
        ),
        bodyMedium: const TextStyle(
          color: ViberColors.text,
          fontSize: 12.5,
          height: 1.38,
        ),
        bodySmall: const TextStyle(
          color: ViberColors.textMuted,
          fontSize: 11.5,
          height: 1.35,
        ),
        labelLarge: const TextStyle(
          color: ViberColors.text,
          fontSize: 12,
          height: 1.2,
          fontWeight: FontWeight.w600,
        ),
        labelMedium: const TextStyle(
          color: ViberColors.textMuted,
          fontSize: 10.5,
          height: 1.2,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.35,
        ),
      ),
      tooltipTheme: const TooltipThemeData(
        decoration: BoxDecoration(
          color: Color(0xFF30363D),
          borderRadius: BorderRadius.all(Radius.circular(5)),
          border: Border.fromBorderSide(BorderSide(color: ViberColors.divider)),
        ),
        textStyle: TextStyle(color: ViberColors.text, fontSize: 11),
        waitDuration: Duration(milliseconds: 450),
      ),
      inputDecorationTheme: const InputDecorationTheme(
        isDense: true,
        filled: true,
        fillColor: Color(0xFF12161A),
        hintStyle: TextStyle(color: ViberColors.textFaint, fontSize: 12),
        contentPadding: EdgeInsets.symmetric(horizontal: 9, vertical: 8),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.all(Radius.circular(5)),
          borderSide: BorderSide(color: ViberColors.divider),
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.all(Radius.circular(5)),
          borderSide: BorderSide(color: ViberColors.divider),
        ),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.all(Radius.circular(5)),
          borderSide: BorderSide(color: ViberColors.focus, width: 1.5),
        ),
      ),
      dropdownMenuTheme: const DropdownMenuThemeData(
        textStyle: TextStyle(fontSize: 12, color: ViberColors.text),
      ),
      textButtonTheme: TextButtonThemeData(
        style: TextButton.styleFrom(
          foregroundColor: ViberColors.text,
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(5)),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: OutlinedButton.styleFrom(
          foregroundColor: ViberColors.text,
          side: const BorderSide(color: ViberColors.divider),
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(5)),
        ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          backgroundColor: ViberColors.selectionStrong,
          foregroundColor: ViberColors.text,
          padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 7),
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(5)),
        ),
      ),
      progressIndicatorTheme: const ProgressIndicatorThemeData(
        color: ViberColors.route,
        linearTrackColor: ViberColors.dividerSoft,
      ),
    );
  }
}

const monoStyle = TextStyle(
  fontFamily: 'Menlo',
  fontSize: 10.5,
  height: 1.35,
  color: ViberColors.textMuted,
);

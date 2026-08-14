import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';

void main() {
  test('light theme is the high-contrast default work surface', () {
    final theme = ViberTheme.light();
    final colors = theme.extension<ViberColors>()!;

    expect(theme.brightness, Brightness.light);
    expect(colors, same(ViberColors.light));
    expect(_contrast(colors.text, colors.panel), greaterThan(12));
    expect(
      _contrast(colors.textMuted, colors.panel),
      greaterThanOrEqualTo(4.5),
    );
    expect(_contrast(colors.route, colors.panel), greaterThanOrEqualTo(4.5));
    expect(_contrast(colors.danger, colors.panel), greaterThanOrEqualTo(4.5));
    expect(_contrast(colors.divider, colors.panel), greaterThan(1.5));
  });

  test('dark theme keeps panels and dividers visibly distinct', () {
    final theme = ViberTheme.dark();
    final colors = theme.extension<ViberColors>()!;

    expect(theme.brightness, Brightness.dark);
    expect(colors, same(ViberColors.dark));
    expect(_contrast(colors.text, colors.panel), greaterThan(12));
    expect(
      _contrast(colors.textMuted, colors.panel),
      greaterThanOrEqualTo(4.5),
    );
    expect(_contrast(colors.route, colors.panel), greaterThanOrEqualTo(4.5));
    expect(_contrast(colors.divider, colors.panel), greaterThan(1.5));
    expect(colors.panel, isNot(colors.canvas));
    expect(colors.panelRaised, isNot(colors.panel));
  });

  testWidgets('desktop selectors and search fields keep a compact baseline', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              CompactSegmentedControl<String>(
                key: const Key('compact-segments'),
                segments: const [
                  CompactSegment(value: 'monitor', label: 'Monitor'),
                  CompactSegment(value: 'ask', label: 'Ask'),
                  CompactSegment(value: 'block', label: 'Block'),
                ],
                selected: 'ask',
                onSelected: (_) {},
              ),
              CompactSearchField(
                key: const Key('compact-search'),
                hintText: 'Filter captures',
                onChanged: (_) {},
              ),
            ],
          ),
        ),
      ),
    );

    expect(
      tester.getSize(find.byKey(const Key('compact-segments'))).height,
      ViberMetrics.controlHeight,
    );
    expect(
      tester.getSize(find.byKey(const Key('compact-search'))).height,
      ViberMetrics.searchHeight,
    );
    final search = tester.widget<TextField>(
      find.descendant(
        of: find.byKey(const Key('compact-search')),
        matching: find.byType(TextField),
      ),
    );
    expect(search.textAlignVertical, TextAlignVertical.center);
    expect(search.style?.fontSize, ViberType.supporting);
    expect(ViberTheme.light().textTheme.bodyLarge?.fontSize, ViberType.control);
    expect(
      ViberTheme.light().inputDecorationTheme.floatingLabelStyle?.fontSize,
      ViberType.floatingFieldLabel,
    );
  });
}

double _contrast(Color first, Color second) {
  final lighter = first.computeLuminance() > second.computeLuminance()
      ? first.computeLuminance()
      : second.computeLuminance();
  final darker = first.computeLuminance() > second.computeLuminance()
      ? second.computeLuminance()
      : first.computeLuminance();
  return (lighter + 0.05) / (darker + 0.05);
}

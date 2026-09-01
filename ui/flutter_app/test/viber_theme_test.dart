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
    expect(
      _contrast(colors.textFaint, colors.panel),
      greaterThanOrEqualTo(4.5),
    );
    expect(
      _contrast(colors.textFaint, colors.canvas),
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
              const SizedBox(
                width: 180,
                child: TextField(key: Key('standard-field')),
              ),
              IconButton(
                key: const Key('compact-icon-button'),
                onPressed: () {},
                icon: const Icon(Icons.refresh),
              ),
              const CompactProgressIndicator(key: Key('compact-progress')),
              const CompactLoadingMessage(
                key: Key('compact-loading-message'),
                label: 'Loading',
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
    expect(
      tester.getSize(find.byKey(const Key('standard-field'))).height,
      ViberMetrics.controlHeight,
    );
    expect(
      tester.getSize(find.byKey(const Key('compact-icon-button'))),
      const Size.square(ViberMetrics.compactControlHeight),
    );
    expect(
      tester.getSize(find.byKey(const Key('compact-progress'))),
      const Size.square(ViberMetrics.compactProgressSize),
    );
    expect(
      tester.getSize(
        find.descendant(
          of: find.byKey(const Key('compact-loading-message')),
          matching: find.byType(CircularProgressIndicator),
        ),
      ),
      const Size.square(ViberMetrics.compactProgressSize),
    );
    expect(
      tester
          .widget<CircularProgressIndicator>(
            find.descendant(
              of: find.byKey(const Key('compact-progress')),
              matching: find.byType(CircularProgressIndicator),
            ),
          )
          .strokeWidth,
      1.4,
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

  test(
    'visual tokens distinguish standard controls from compact utilities',
    () {
      expect(ViberMetrics.controlHeight, 30);
      expect(ViberMetrics.compactControlHeight, 26);
      expect(ViberType.page, 22);
      expect(ViberType.dialog, 18);
      expect(ViberType.primary, 14);
      expect(ViberType.title, 13);
      expect(ViberType.body, 13);
      expect(ViberType.control, 13);
      expect(ViberType.supporting, 12);
      expect(ViberType.utility, 11);
    },
  );
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

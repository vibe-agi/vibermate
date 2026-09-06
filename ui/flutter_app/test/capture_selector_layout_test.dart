import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';

/// A clipped paragraph can pass Flutter's overflow checks. Check that its
/// allocated height still fits a complete line, not just that it was mounted.
void expectReadableValue(WidgetTester tester, Finder field, String value) {
  final text = find.descendant(
    of: field,
    matching: find.byWidgetPredicate(
      (widget) => widget is RichText && widget.text.toPlainText() == value,
    ),
  );
  expect(text, findsOneWidget);
  final paragraph = tester.renderObject<RenderParagraph>(text);
  expect(
    paragraph.size.height,
    greaterThanOrEqualTo(
      paragraph.getMaxIntrinsicHeight(paragraph.size.width) - 0.01,
    ),
    reason: 'the selected value must fit a complete line vertically',
  );
  final bounds = tester.getRect(field);
  final textBounds = tester.getRect(text);
  expect(textBounds.top, greaterThanOrEqualTo(bounds.top));
  expect(textBounds.bottom, lessThanOrEqualTo(bounds.bottom));
}

void main() {
  for (final scenario in const [
    (width: 390.0, chinese: false, capture: 'managed_run:run-1'),
    (width: 390.0, chinese: true, capture: 'managed_run:run-1'),
    (width: 390.0, chinese: false, capture: 'manual_capture:manual-figma'),
    (width: 390.0, chinese: true, capture: 'manual_capture:manual-figma'),
    (width: 900.0, chinese: false, capture: 'managed_run:run-1'),
    (width: 900.0, chinese: false, capture: 'manual_capture:manual-figma'),
    (width: 900.0, chinese: true, capture: 'manual_capture:manual-figma'),
    (width: 900.0, chinese: true, capture: 'managed_run:run-1'),
  ]) {
    testWidgets(
      'conversation selector fits and switches at ${scenario.width}px '
      '(Chinese: ${scenario.chinese}, ${scenario.capture})',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(scenario.width, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ViberMateApp(previewMode: true, preferChinese: scenario.chinese),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(Key('capture-row-${scenario.capture}')));
        await tester.pumpAndSettle();

        final selector = find.byKey(const Key('capture-conversation-selector'));
        final field = find.descendant(
          of: selector,
          matching: find.byType(CompactSelectField<String>),
        );
        expect(field, findsOneWidget);
        expect(
          tester.getSize(field).height,
          greaterThanOrEqualTo(ViberMetrics.controlHeight),
          reason: 'the selector row must not squeeze the decorated field',
        );
        final select = tester.widget<CompactSelectField<String>>(field);
        expect(select.decoration.labelText, isNull);
        final selected = select.items.firstWhere(
          (item) => item.value == select.initialValue,
        );
        expectReadableValue(tester, field, (selected.child as Text).data!);

        final next = select.items.firstWhere(
          (item) => item.value != selected.value,
        );
        await tester.tap(field);
        await tester.pumpAndSettle();
        final option = find.widgetWithText(
          MenuItemButton,
          (next.child as Text).data!,
        );
        await tester.ensureVisible(option);
        await tester.tap(option);
        await tester.pumpAndSettle();
        expect(find.byType(MenuItemButton), findsNothing);
        expect(
          tester.widget<CompactSelectField<String>>(field).initialValue,
          next.value,
        );
        expectReadableValue(tester, field, (next.child as Text).data!);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      },
    );
  }

  for (final label in [null, 'Conversation']) {
    testWidgets('select accommodates 200% text (label: $label)', (
      tester,
    ) async {
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          builder: (context, child) => MediaQuery(
            data: MediaQuery.of(
              context,
            ).copyWith(textScaler: const TextScaler.linear(2)),
            child: child!,
          ),
          home: Scaffold(
            body: Center(
              child: SizedBox(
                width: 390,
                child: CompactSelectField<String>(
                  initialValue: 'one',
                  decoration: InputDecoration(labelText: label),
                  items: const [
                    DropdownMenuItem(
                      value: 'one',
                      child: Text('Exchange · 16:48:00 · 1 Turn'),
                    ),
                    DropdownMenuItem(value: 'two', child: Text('另一个会话')),
                  ],
                  onChanged: (_) {},
                ),
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final field = find.byType(CompactSelectField<String>);
      expectReadableValue(tester, field, 'Exchange · 16:48:00 · 1 Turn');

      await tester.tap(field);
      await tester.pumpAndSettle();
      for (final value in ['Exchange · 16:48:00 · 1 Turn', '另一个会话']) {
        expectReadableValue(
          tester,
          find.widgetWithText(MenuItemButton, value),
          value,
        );
      }
      await tester.tap(find.widgetWithText(MenuItemButton, '另一个会话'));
      await tester.pumpAndSettle();
      expectReadableValue(tester, field, '另一个会话');
      expect(tester.takeException(), isNull);
    });
  }
}

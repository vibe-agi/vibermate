import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';

void main() {
  for (final dark in [false, true]) {
    testWidgets(
      'compact permission retains wrapping, pointer and keyboard ($dark)',
      (tester) async {
        var checked = false;
        var changes = 0;
        await tester.pumpWidget(
          MaterialApp(
            theme: dark ? ViberTheme.dark() : ViberTheme.light(),
            home: Scaffold(
              body: SizedBox(
                width: 280,
                child: StatefulBuilder(
                  builder: (context, setState) => CompactCheckboxField(
                    value: checked,
                    label: '允许 /usage 读取此账号的历史用量',
                    description: '包含本次 Capture 以外的账号历史。默认关闭；更换账号后需重新授权。',
                    onChanged: (value) => setState(() {
                      checked = value;
                      changes++;
                    }),
                  ),
                ),
              ),
            ),
          ),
        );
        final checkbox = tester.widget<Checkbox>(find.byType(Checkbox));
        final border = (checkbox.side! as WidgetStateBorderSide).resolve({});
        expect(border!.width, 1.25);
        expect(
          tester.getSize(find.byType(CompactCheckboxField)).height,
          greaterThan(40),
        );
        await tester.tap(find.text('允许 /usage 读取此账号的历史用量'));
        await tester.pump();
        expect(checked, isTrue);
        expect(changes, 1);
        await tester.tap(find.byType(Checkbox));
        await tester.pump();
        expect(checked, isFalse);
        expect(changes, 2);
        await tester.sendKeyEvent(LogicalKeyboardKey.tab);
        await tester.sendKeyEvent(LogicalKeyboardKey.space);
        await tester.pump();
        expect(checked, isTrue);
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('disabled permission cannot be toggled through the label', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: const Scaffold(
          body: CompactCheckboxField(
            value: false,
            label: 'Permission',
            description: 'Account-wide history',
            onChanged: null,
          ),
        ),
      ),
    );
    await tester.tap(find.text('Permission'));
    expect(tester.widget<Checkbox>(find.byType(Checkbox)).value, isFalse);
    expect(tester.widget<Checkbox>(find.byType(Checkbox)).onChanged, isNull);
  });
}

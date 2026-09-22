import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/account_selector_editor.dart';

void main() {
  for (final size in [const Size(390, 800), const Size(1180, 850)]) {
    for (final scale in [1.0, 1.5]) {
      testWidgets('sample labels stay outside fields at $size / $scale', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(size);
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            builder: (context, child) => MediaQuery(
              data: MediaQuery.of(
                context,
              ).copyWith(textScaler: TextScaler.linear(scale)),
              child: child!,
            ),
            home: AccountSelectorEditorDialog(
              selectorId: 'labels',
              initial: const AccountSelectorPolicy(
                javaScript: 'selection.accountId = accounts[0].id;',
              ),
              copy: copy,
              testSelector: ({required policy, required sample}) async =>
                  const AccountSelectorTestResult(accountId: 'account.work'),
            ),
          ),
        );
        await tester.ensureVisible(
          find.byKey(const Key('account-selector-sample')),
        );
        await tester.tap(find.byKey(const Key('account-selector-sample')));
        await tester.pumpAndSettle();
        for (final field in ['accounts', 'user', 'workspace', 'model']) {
          final input = find.byKey(Key('account-selector-sample-$field'));
          final wrapper = find
              .ancestor(of: input, matching: find.byType(CompactLabeledControl))
              .first;
          await tester.ensureVisible(wrapper);
          await tester.pumpAndSettle();
          final label = find.descendant(
            of: wrapper,
            matching: find.text(copy('account_selector.sample.$field')),
          );
          final textField = tester.widget<TextField>(input);
          expect(textField.decoration?.labelText, isNull);
          expect(
            tester.getRect(label).bottom,
            lessThanOrEqualTo(tester.getRect(input).top),
          );
          expect(tester.getRect(label).top, greaterThanOrEqualTo(0));
        }
        expect(tester.takeException(), isNull);
      });
    }
  }

  testWidgets('select shows a placeholder and recovers after items arrive', (
    tester,
  ) async {
    List<DropdownMenuItem<String>> items = [];
    String? selected;
    late StateSetter rebuild;
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: Scaffold(
          body: StatefulBuilder(
            builder: (context, setState) {
              rebuild = setState;
              return CompactLabeledControl(
                label: '账号选择方式',
                child: CompactSelectField<String>(
                  key: const Key('account-select'),
                  initialValue: selected,
                  items: items,
                  placeholder: items.isEmpty ? '尚未关联账号' : '请选择账号',
                  onChanged: (value) => setState(() => selected = value),
                ),
              );
            },
          ),
        ),
      ),
    );
    expect(find.text('尚未关联账号'), findsOneWidget);
    expect(
      tester
          .widget<InkWell>(
            find
                .descendant(
                  of: find.byKey(const Key('account-select')),
                  matching: find.byType(InkWell),
                )
                .first,
          )
          .onTap,
      isNull,
    );
    rebuild(
      () => items = [const DropdownMenuItem(value: 'one', child: Text('账号一'))],
    );
    await tester.pumpAndSettle();
    expect(find.text('请选择账号'), findsOneWidget);
    expect(
      selected,
      isNull,
      reason: 'loading must not grant a different account implicitly',
    );
    await tester.tap(find.byKey(const Key('account-select')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('账号一').last);
    await tester.pumpAndSettle();
    expect(selected, 'one');
    rebuild(
      () => items = [
        const DropdownMenuItem(
          value: 'one',
          enabled: false,
          child: Text('账号一 · 需要重新连接'),
        ),
      ],
    );
    await tester.pumpAndSettle();
    expect(find.text('账号一 · 需要重新连接'), findsOneWidget);
    rebuild(
      () => items = [const DropdownMenuItem(value: 'one', child: Text('账号一'))],
    );
    await tester.pumpAndSettle();
    expect(find.text('账号一'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}

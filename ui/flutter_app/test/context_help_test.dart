import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;

  testWidgets(
    'Background help is on demand; errors and field guidance stay visible',
    (tester) async {
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: const Scaffold(
            body: Column(
              children: [
                PageHeading(
                  title: 'Accounts',
                  help: 'Credentials can be reused across traffic policies.',
                  dismissHelpLabel: 'Close',
                ),
                InlineNotice(message: 'Connection failed. Retry.', error: true),
                CompactLabeledControl(
                  label: 'Account',
                  help: 'This account supplies upstream credentials.',
                  dismissHelpLabel: 'Close',
                  detail: 'Select an account before saving.',
                  child: TextField(),
                ),
              ],
            ),
          ),
        ),
      );
      expect(
        find.text('Credentials can be reused across traffic policies.'),
        findsNothing,
      );
      expect(
        find.text('This account supplies upstream credentials.'),
        findsNothing,
      );
      expect(find.text('Connection failed. Retry.'), findsOneWidget);
      expect(find.text('Select an account before saving.'), findsOneWidget);

      await tester.tap(find.byTooltip('Accounts'));
      await tester.pumpAndSettle();
      expect(find.byType(AlertDialog), findsOneWidget);
      expect(
        find.text('Credentials can be reused across traffic policies.'),
        findsOneWidget,
      );
      await tester.tap(find.text('Close'));
      await tester.pumpAndSettle();
      expect(find.byType(AlertDialog), findsNothing);

      await tester.tap(find.byTooltip('Account'));
      await tester.pumpAndSettle();
      expect(
        find.text('This account supplies upstream credentials.'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('Help works with keyboard and returns focus after Escape', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: const Scaffold(
          body: ContextHelpButton(
            title: 'Network egress',
            message: 'Choose how this route reaches its upstream service.',
            dismissLabel: 'Close',
          ),
        ),
      ),
    );
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pumpAndSettle();
    final helpFocus = FocusManager.instance.primaryFocus;
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(FocusManager.instance.primaryFocus, same(helpFocus));
    expect(tester.takeException(), isNull);
  });

  for (final chinese in [false, true]) {
    for (final dark in [false, true]) {
      testWidgets('Help fits narrow windows and 200% text ($chinese, $dark)', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(390, 760));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final title = chinese ? '上游账号' : 'Upstream accounts';
        final dismiss = chinese ? '关闭' : 'Close';
        final message = List.filled(
          24,
          chinese
              ? '关联账号会复用同一份凭据，不会复制账号。'
              : 'Linked accounts reuse the same credentials without copying them.',
        ).join('\n');
        await tester.pumpWidget(
          MaterialApp(
            theme: dark ? ViberTheme.dark() : ViberTheme.light(),
            builder: (context, child) => MediaQuery(
              data: MediaQuery.of(
                context,
              ).copyWith(textScaler: TextScaler.linear(2)),
              child: child!,
            ),
            home: Scaffold(
              body: PageHeading(
                title: title,
                help: message,
                dismissHelpLabel: dismiss,
                trailing: TextButton(
                  onPressed: () {},
                  child: Text(chinese ? '添加账号' : 'Add account'),
                ),
              ),
            ),
          ),
        );
        expect(find.byTooltip(title), findsOneWidget);
        expect(find.text(message), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.tap(find.byTooltip(title));
        await tester.pumpAndSettle();
        expect(find.byType(SelectionArea), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.ensureVisible(find.text(dismiss));
        await tester.tap(find.text(dismiss));
        await tester.pumpAndSettle();
        expect(find.byType(AlertDialog), findsNothing);
        expect(tester.takeException(), isNull);
      });
    }
  }
}

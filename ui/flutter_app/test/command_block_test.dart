import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';

void main() {
  const command =
      'vibermate login --server http://127.0.0.1:36897 && vibermate run --server http://127.0.0.1:36897 -- claude';

  Future<List<String>> mountBlock(
    WidgetTester tester, {
    String text = command,
    String? copyLabel,
    bool clipboardFails = false,
    double width = 390,
    Widget? caption,
  }) async {
    await tester.binding.setSurfaceSize(Size(width, 400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final copied = <String>[];
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          if (clipboardFails) throw PlatformException(code: 'denied');
          copied.add((call.arguments as Map)['text'] as String);
        }
        return null;
      },
    );
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        null,
      ),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Padding(
            padding: const EdgeInsets.all(16),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                CommandBlock(
                  key: const Key('block'),
                  label: caption == null ? 'Login' : null,
                  caption: caption,
                  text: text,
                  copyLabel: copyLabel,
                  copyTooltip: 'Copy login command',
                  copiedLabel: 'Copied',
                  copyFailedLabel: 'Could not copy',
                  copyKey: const Key('block-copy'),
                  textKey: const Key('block-text'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
    return copied;
  }

  testWidgets('a long command scrolls sideways instead of wrapping', (
    tester,
  ) async {
    await mountBlock(tester);
    final text = tester.getRect(find.byKey(const Key('block-text')));
    final block = tester.getRect(find.byKey(const Key('block')));
    // One line of Terminal text, wider than the phone-width block it sits in.
    expect(text.height, lessThan(22));
    expect(text.width, greaterThan(block.width));
    expect(
      find.descendant(
        of: find.byKey(const Key('block')),
        matching: find.byWidgetPredicate(
          (widget) =>
              widget is SingleChildScrollView &&
              widget.scrollDirection == Axis.horizontal,
        ),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('a cut-off end fades out until it is scrolled into view', (
    tester,
  ) async {
    await mountBlock(tester);
    await tester.pump();
    final fade = find.byKey(const Key('command-block-fade'));
    expect(fade, findsOneWidget);
    // At the end of the command nothing is cut off any more.
    final scroll = tester.widget<SingleChildScrollView>(
      find.descendant(
        of: find.byKey(const Key('block')),
        matching: find.byType(SingleChildScrollView),
      ),
    );
    scroll.controller!.jumpTo(scroll.controller!.position.maxScrollExtent);
    await tester.pump();
    expect(fade, findsNothing);
  });

  testWidgets('a command that fits does not fade', (tester) async {
    await mountBlock(tester, text: 'vibermate doctor');
    await tester.pump();
    expect(find.byKey(const Key('command-block-fade')), findsNothing);
  });

  testWidgets('with a caption, Copy moves above and the command gets the '
      'whole width', (tester) async {
    await mountBlock(
      tester,
      text: 'vibermate doctor',
      copyLabel: 'Copy',
      caption: const Text('Claude Code'),
    );
    final caption = tester.getRect(find.text('Claude Code'));
    final button = tester.getRect(find.byKey(const Key('block-copy')));
    final text = tester.getRect(find.byKey(const Key('block-text')));
    final block = tester.getRect(find.byKey(const Key('block')));
    expect(button.bottom, lessThanOrEqualTo(text.top));
    expect(button.right, closeTo(block.right, 1));
    expect(caption.left, closeTo(block.left, 1));
    // Nothing sits beside the command inside the block.
    expect(
      find.descendant(
        of: find.byType(DecoratedBox),
        matching: find.byKey(const Key('block-copy')),
      ),
      findsNothing,
    );
  });

  testWidgets('multi-line text keeps its own line breaks', (tester) async {
    await mountBlock(tester, text: 'A=1\nB=2\nC=3');
    final text = tester.getRect(find.byKey(const Key('block-text')));
    expect(text.height, greaterThan(40));
    expect(text.width, lessThan(100));
  });

  testWidgets('the copy button sits at the top-right and says Copied', (
    tester,
  ) async {
    final copied = await mountBlock(tester, text: 'A=1\nB=2\nC=3');
    final label = tester.getRect(find.text('Login'));
    final button = tester.getRect(find.byKey(const Key('block-copy')));
    final text = tester.getRect(find.byKey(const Key('block-text')));
    // Inside the block, on its first line, at the right edge.
    expect(button.top, greaterThan(label.bottom));
    expect(button.top, lessThan(text.top + 8));
    expect(button.right, closeTo(390 - 16 - 1, 2));

    await tester.tap(find.byKey(const Key('block-copy')));
    await tester.pump();
    expect(copied, ['A=1\nB=2\nC=3']);
    expect(find.text('Copied'), findsOneWidget);
    final announcement = tester.getSemantics(find.text('Copied'));
    expect(announcement.flagsCollection.isLiveRegion, isTrue);

    // The confirmation goes away by itself.
    await tester.pump(const Duration(seconds: 3));
    expect(find.text('Copied'), findsNothing);
  });

  testWidgets('a labelled button turns into Copied in place', (tester) async {
    final copied = await mountBlock(tester, copyLabel: 'Copy');
    expect(find.text('Copy'), findsOneWidget);
    await tester.tap(find.byKey(const Key('block-copy')));
    await tester.pump();
    expect(copied, [command]);
    expect(find.text('Copy'), findsNothing);
    expect(
      find.descendant(
        of: find.byKey(const Key('block-copy')),
        matching: find.text('Copied'),
      ),
      findsOneWidget,
    );
    await tester.pump(const Duration(seconds: 3));
    expect(find.text('Copy'), findsOneWidget);
  });

  testWidgets('a refused copy says so and keeps the text selectable', (
    tester,
  ) async {
    await mountBlock(tester, clipboardFails: true);
    await tester.tap(find.byKey(const Key('block-copy')));
    await tester.pump();
    expect(find.text('Could not copy'), findsOneWidget);
    expect(find.text('Copied'), findsNothing);
    expect(
      tester.widget<SelectableText>(find.byKey(const Key('block-text'))).data,
      command,
    );
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';

void main() {
  // INV-STORE-DISCLOSED is a release gate: the recording mode, the location,
  // the retention period and the absence of at-rest database encryption must be
  // visible in Settings. A product that quietly stores plaintext is no more
  // honest than one that claims encryption it does not have.
  testWidgets('Settings discloses that the archive is not encrypted at rest', (
    tester,
  ) async {
    await tester.pumpWidget(const ViberMateApp(previewMode: true, preferChinese: false));
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();

    final panel = find.byKey(const Key('storage-disclosure-panel'));
    await tester.scrollUntilVisible(
      panel,
      240,
      scrollable: find.byType(Scrollable).last,
    );
    expect(panel, findsOneWidget);
    expect(
      find.textContaining('not encrypted at rest'),
      findsOneWidget,
      reason: 'the disclosure must state the absence plainly',
    );
  });

  // The disclosure used to say no credential value is stored, full stop. That
  // is true only of credential *headers*: bodies, tool arguments and query
  // strings are retained verbatim, which is the point of a forensic archive.
  // A user who pastes an API key into a prompt has it kept, and the panel that
  // claims otherwise is the one they would have read before doing it.
  testWidgets('Storage disclosure names what is retained, not just what is removed', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();

    final panel = find.byKey(const Key('storage-disclosure-panel'));
    await tester.scrollUntilVisible(
      panel,
      240,
      scrollable: find.byType(Scrollable).last,
    );
    expect(
      find.textContaining('stored as sent'),
      findsOneWidget,
      reason: 'the disclosure must state that bodies are retained verbatim',
    );
  });

  testWidgets('Storage disclosure is present in Simplified Chinese', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();

    final panel = find.byKey(const Key('storage-disclosure-panel'));
    await tester.scrollUntilVisible(
      panel,
      240,
      scrollable: find.byType(Scrollable).last,
    );
    expect(panel, findsOneWidget);
    expect(find.textContaining('未加密'), findsOneWidget);
  });

  // The disclosure grew when its claim was narrowed, and a copy change is
  // exactly the kind of edit that overflows a panel without anyone noticing:
  // every assertion above passes on a clipped layout. A widget test fails on a
  // RenderFlex overflow, so rendering the panel at the narrow end of the
  // window range is the assertion.
  for (final size in const [Size(820, 620), Size(700, 560)]) {
    for (final chinese in const [false, true]) {
      testWidgets(
        'Storage disclosure lays out at ${size.width.toInt()}x'
        '${size.height.toInt()} in ${chinese ? 'zh' : 'en'}',
        (tester) async {
          tester.view.physicalSize = size;
          tester.view.devicePixelRatio = 1.0;
          addTearDown(tester.view.reset);

          await tester.pumpWidget(
            ViberMateApp(previewMode: true, preferChinese: chinese),
          );
          await tester.pumpAndSettle();
          await tester.tap(find.byIcon(Icons.settings_outlined).first);
          await tester.pumpAndSettle();

          final panel = find.byKey(const Key('storage-disclosure-panel'));
          await tester.scrollUntilVisible(
            panel,
            240,
            scrollable: find.byType(Scrollable).last,
          );
          expect(panel, findsOneWidget);
        },
      );
    }
  }
}

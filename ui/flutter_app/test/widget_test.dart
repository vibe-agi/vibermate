import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';

void main() {
  testWidgets('Preview workbench starts with real capture hierarchy', (
    tester,
  ) async {
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.text('RUNNING NOW'), findsOneWidget);
    expect(find.text('HISTORY'), findsOneWidget);
    expect(find.text('Claude Code'), findsWidgets);

    await tester.tap(find.text('Claude Code').first);
    await tester.pumpAndSettle();
    expect(find.text('Capture conversation'), findsOneWidget);
    expect(
      find.byKey(const Key('capture-conversation-selector')),
      findsOneWidget,
    );

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    expect(find.text('Traffic policies'), findsWidgets);
    expect(find.text('Work'), findsWidgets);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

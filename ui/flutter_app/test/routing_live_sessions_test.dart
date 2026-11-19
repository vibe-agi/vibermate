import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';

void main() {
  testWidgets('a routing set lists the sessions running on it now', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 1000));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Traffic policies').first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();

    final live = find.byKey(const Key('environment-live-sessions'));
    expect(live, findsOneWidget);
    // The preview runs five managed sessions and one manual session on Work.
    expect(find.text('Running on it now · 6'), findsOneWidget);
    final first = find.byKey(const Key('environment-live-managed_run:run-1'));
    expect(first, findsOneWidget);
    expect(tester.takeException(), isNull);

    // A session opens in Sessions.
    await tester.tap(first);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-live-sessions')), findsNothing);
    expect(find.byKey(const Key('session-ledger-list')), findsOneWidget);
    expect(tester.takeException(), isNull);

    // Research has the other running session; a set nobody uses says so.
    await tester.tap(find.text('Traffic policies').first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-research')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Running on it now ·'), findsOneWidget);
    expect(find.textContaining('Running on it now · 0'), findsNothing);
  });
}

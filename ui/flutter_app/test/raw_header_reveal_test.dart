import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/raw_header_reveal.dart';

void main() {
  final copy = AppCopy.forLanguage(AppLanguage.english);
  Widget view(
    Future<String> Function() reveal, {
    String identity = 'envelope:User-Agent',
  }) => MaterialApp(
    theme: ViberTheme.dark(),
    home: Scaffold(
      body: RawHeaderReveal(
        identity: identity,
        name: 'User-Agent',
        redactedText: 'User-Agent: [redacted 11B fingerprint]',
        reveal: reveal,
        copy: copy,
      ),
    ),
  );

  testWidgets('header values are fetched only on eye click and auto-hide', (
    tester,
  ) async {
    var calls = 0;
    await tester.pumpWidget(
      view(() async {
        calls++;
        return 'private-agent';
      }),
    );
    expect(calls, 0);
    expect(find.textContaining('private-agent'), findsNothing);
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    expect(calls, 1);
    expect(find.text('User-Agent: private-agent'), findsOneWidget);
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    expect(find.textContaining('private-agent'), findsNothing);
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    expect(calls, 2);
    await tester.pump(const Duration(seconds: 61));
    expect(find.textContaining('private-agent'), findsNothing);
  });

  testWidgets('late header response cannot reopen a canceled reveal', (
    tester,
  ) async {
    final pending = Completer<String>();
    await tester.pumpWidget(view(() => pending.future));
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    pending.complete('private-agent');
    await tester.pumpAndSettle();
    expect(find.textContaining('private-agent'), findsNothing);
  });

  testWidgets('background and envelope changes clear header plaintext', (
    tester,
  ) async {
    await tester.pumpWidget(view(() async => 'private-agent'));
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
    await tester.pump();
    expect(find.textContaining('private-agent'), findsNothing);
    tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    await tester.pumpWidget(
      view(() async => 'other-agent', identity: 'other:User-Agent'),
    );
    expect(find.textContaining('private-agent'), findsNothing);
    expect(find.textContaining('other-agent'), findsNothing);
  });

  testWidgets('failure does not print credential error details', (
    tester,
  ) async {
    await tester.pumpWidget(
      view(() async => throw StateError('secret-diagnostic')),
    );
    await tester.tap(find.byType(IconButton));
    await tester.pumpAndSettle();
    expect(find.text(copy('exchange.raw.header_unavailable')), findsOneWidget);
    expect(find.textContaining('secret-diagnostic'), findsNothing);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('retry gets its own reveal lifetime after an unavailable value', (
    tester,
  ) async {
    var attempts = 0;
    await tester.pumpWidget(
      view(() async {
        if (attempts++ == 0) throw StateError('unavailable');
        return 'private-agent';
      }),
    );
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    await tester.pump(const Duration(seconds: 30));
    await tester.tap(find.byType(IconButton));
    await tester.pump();
    await tester.pump(const Duration(seconds: 31));
    expect(find.text('User-Agent: private-agent'), findsOneWidget);
    await tester.pump(const Duration(seconds: 30));
    expect(find.textContaining('private-agent'), findsNothing);
  });
}

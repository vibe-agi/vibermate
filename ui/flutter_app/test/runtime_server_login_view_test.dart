import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';

void main() {
  testWidgets('390px HTTP login is usable and names the plaintext risk', (
    tester,
  ) async {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = const Size(390, 844);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.view.resetPhysicalSize);
    final accessKey = TextEditingController();
    addTearDown(accessKey.dispose);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: RuntimeServerLoginView(
          copy: AppCopy.forLanguage(AppLanguage.english),
          target: 'http://192.168.1.20:9666',
          plaintextTransport: true,
          accessKey: accessKey,
          accessKeyVisible: false,
          failure: null,
          onVisibilityChanged: () {},
          onConnect: () {},
        ),
      ),
    );
    await tester.pump();

    expect(find.text('Server · http://192.168.1.20:9666'), findsOneWidget);
    final warning = find.byKey(const Key('server-login-http-warning'));
    expect(warning, findsOneWidget);
    expect(
      find.descendant(
        of: warning,
        matching: find.textContaining('not encrypted in transit'),
      ),
      findsOneWidget,
    );
    expect(find.text('Connect'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}

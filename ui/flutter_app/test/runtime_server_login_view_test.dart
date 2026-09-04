import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
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
    final username = TextEditingController();
    final password = TextEditingController();
    final confirmPassword = TextEditingController();
    final recoveryKey = TextEditingController();
    var connections = 0;
    addTearDown(username.dispose);
    addTearDown(password.dispose);
    addTearDown(confirmPassword.dispose);
    addTearDown(recoveryKey.dispose);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: RuntimeServerLoginView(
          copy: AppCopy.forLanguage(AppLanguage.english),
          target: 'http://192.168.1.20:9666',
          plaintextTransport: true,
          mode: RuntimeLoginMode.signIn,
          username: username,
          password: password,
          confirmPassword: confirmPassword,
          recoveryKey: recoveryKey,
          passwordVisible: false,
          recoveryKeyVisible: false,
          failure: null,
          onPasswordVisibilityChanged: () {},
          onRecoveryKeyVisibilityChanged: () {},
          onModeChanged: (_) {},
          onConnect: () => connections += 1,
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
    expect(find.textContaining('3–64'), findsOneWidget);
    await tester.enterText(find.byKey(const Key('server-username')), 'a');
    await tester.enterText(
      find.byKey(const Key('server-password')),
      'valid-password',
    );
    await tester.pump();
    expect(
      tester
          .widget<FilledButton>(find.byKey(const Key('server-login-submit')))
          .onPressed,
      isNull,
    );
    await tester.enterText(find.byKey(const Key('server-username')), 'alice');
    await tester.enterText(find.byKey(const Key('server-password')), 'short');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();
    expect(connections, 0);
    await tester.enterText(
      find.byKey(const Key('server-password')),
      'valid-password',
    );
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();
    expect(connections, 1);
    expect(tester.takeException(), isNull);
  });

  testWidgets('390px first-run setup explains the local recovery key', (
    tester,
  ) async {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = const Size(390, 844);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.view.resetPhysicalSize);
    final username = TextEditingController();
    final password = TextEditingController();
    final confirmPassword = TextEditingController();
    final recoveryKey = TextEditingController();
    addTearDown(username.dispose);
    addTearDown(password.dispose);
    addTearDown(confirmPassword.dispose);
    addTearDown(recoveryKey.dispose);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: RuntimeServerLoginView(
          copy: AppCopy.forLanguage(AppLanguage.english),
          target: 'https://runtime.example.test:9666',
          plaintextTransport: false,
          mode: RuntimeLoginMode.setup,
          username: username,
          password: password,
          confirmPassword: confirmPassword,
          recoveryKey: recoveryKey,
          passwordVisible: false,
          recoveryKeyVisible: false,
          failure: null,
          onPasswordVisibilityChanged: () {},
          onRecoveryKeyVisibilityChanged: () {},
          onModeChanged: (_) {},
          onConnect: () {},
        ),
      ),
    );
    await tester.pump();

    expect(find.text('Set up this Runtime Server'), findsOneWidget);
    expect(
      find.textContaining('vibermated server recovery-key'),
      findsOneWidget,
    );
    expect(
      find.textContaining('no default administrator password'),
      findsOneWidget,
    );
    await tester.enterText(
      find.byKey(const Key('server-recovery-key')),
      'recovery-key-from-server',
    );
    await tester.enterText(find.byKey(const Key('server-username')), 'alice');
    await tester.enterText(
      find.byKey(const Key('server-password')),
      'unique-password',
    );
    await tester.enterText(
      find.byKey(const Key('server-confirm-password')),
      'unique-password',
    );
    await tester.pump();
    expect(
      tester
          .widget<FilledButton>(find.byKey(const Key('server-login-submit')))
          .onPressed,
      isNotNull,
    );
    expect(tester.takeException(), isNull);
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/account/member_portal.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets(
    '390px Chinese member portal keeps account actions discoverable',
    (tester) async {
      tester.view.devicePixelRatio = 1;
      tester.view.physicalSize = const Size(390, 844);
      addTearDown(tester.view.resetDevicePixelRatio);
      addTearDown(tester.view.resetPhysicalSize);
      final api = PreviewControlApi();
      addTearDown(api.close);
      var signedOut = false;
      final runtime = RuntimeConnection(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        close: api.close,
        isClosed: () => false,
        serverManagement: false,
        terminalManagement: false,
        rootTrustManagement: false,
        targetLabel: 'https://runtime.example.test:9666',
        webPrincipal: const RuntimeWebPrincipal(
          id: 'user.member',
          username: 'member-with-a-very-long-username',
          role: RuntimeWebRole.member,
        ),
        changePassword: (_, _) async {},
        signOut: () async => signedOut = true,
      );

      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: MemberPortal(
            runtime: runtime,
            copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
            onSignOut: runtime.signOut!,
          ),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('ViberMate'), findsOneWidget);
      expect(find.byKey(const Key('web-account-menu')), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.tap(find.byKey(const Key('web-account-menu')));
      await tester.pumpAndSettle();
      expect(find.text('member-with-a-very-long-username'), findsOneWidget);
      expect(find.text('成员'), findsOneWidget);
      expect(find.text('修改密码'), findsOneWidget);
      expect(find.text('退出登录'), findsOneWidget);

      await tester.tap(find.text('退出登录'));
      await tester.pumpAndSettle();
      expect(signedOut, isTrue);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );
}

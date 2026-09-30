import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/server_deployment_guide.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  test(
    'public setup requires a literal DNS name, mailbox and explicit terms',
    () {
      for (final domain in [
        '',
        '127.0.0.1',
        '[::1]',
        '*.example.com',
        'app.lan',
        'app.local',
        'app.test',
        'app.home.arpa',
        'https://app.example.com',
        'app.example.com:443',
        'app.example.com/path',
        'app.example.com;id',
        'app.example.com\nBAD=1',
        '-app.example.com',
      ]) {
        expect(
          publicDeploymentRecipe(
            domain: domain,
            email: 'ops@example.com',
            docker: true,
            termsAccepted: true,
          ),
          isNull,
          reason: domain,
        );
      }
      for (final email in [
        '',
        'Ops <ops@example.com>',
        'ops@example.com\nBAD=1',
        r'$(id)@example.com',
        'ops\'@example.com',
      ]) {
        expect(
          publicDeploymentRecipe(
            domain: 'app.example.com',
            email: email,
            docker: false,
            termsAccepted: true,
          ),
          isNull,
        );
      }
      expect(
        publicDeploymentRecipe(
          domain: 'app.example.com',
          email: 'ops@example.com',
          docker: true,
          termsAccepted: false,
        ),
        isNull,
      );
      final docker = publicDeploymentRecipe(
        domain: ' APP.Example.COM ',
        email: 'ops+server@example.com',
        docker: true,
        termsAccepted: true,
      )!;
      expect(
        docker.environment,
        contains('VIBERMATE_PUBLIC_HOST=app.example.com\n'),
      );
      expect(docker.environment, contains('VIBERMATE_TRUSTED_PROXIES=none'));
      expect(
        docker.start,
        'docker compose --env-file .env.public -f compose.public.yaml up -d --wait --wait-timeout 120',
      );
      expect(docker.recovery, contains('recovery-key --data-dir /data'));
      expect(docker.logs, contains('logs --tail=100 -f vibermate'));
      final native = publicDeploymentRecipe(
        domain: 'app.example.com',
        email: 'ops@example.com',
        docker: false,
        termsAccepted: true,
      )!;
      expect(native.environment, isNull);
      expect(native.start, contains('--access-address app.example.com:443'));
      expect(native.start, contains('--listen 0.0.0.0:8443'));
      expect(native.start, contains('--transport automatic_tls'));
      expect(native.start, contains('--acme-agree-terms'));
      expect(native.start, contains('--acme-challenge tls_alpn_01'));
      expect(native.start.split('\\\n'), hasLength(7));
    },
  );

  for (final width in [390.0, 1180.0]) {
    for (final language in AppLanguage.values) {
      testWidgets(
        'setup preserves input and copies exact configuration at $width $language',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 900));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          String? copied;
          tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
            SystemChannels.platform,
            (call) async {
              if (call.method == 'Clipboard.setData') {
                copied = (call.arguments as Map)['text'] as String;
              }
              return null;
            },
          );
          addTearDown(
            () => tester.binding.defaultBinaryMessenger
                .setMockMethodCallHandler(SystemChannels.platform, null),
          );
          final copy = AppCopy.forLanguage(language);
          await tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.dark(),
              home: Scaffold(body: ServerDeploymentGuide(copy: copy)),
            ),
          );
          await tester.tap(find.byKey(const Key('server-deployment-open')));
          await tester.pumpAndSettle();
          expect(find.byKey(const Key('deployment-code-start')), findsNothing);
          await tester.enterText(
            find.byKey(const Key('deployment-domain')),
            'app.example.com',
          );
          await tester.enterText(
            find.byKey(const Key('deployment-email')),
            'ops@example.com',
          );
          await tester.pumpAndSettle();
          expect(find.byKey(const Key('deployment-copy-env')), findsNothing);
          final terms = find.byKey(const Key('deployment-terms'));
          await tester.ensureVisible(terms);
          await tester.pumpAndSettle();
          await tester.tap(terms);
          await tester.pumpAndSettle();
          final button = find.byKey(const Key('deployment-copy-env'));
          await tester.ensureVisible(button);
          await tester.pumpAndSettle();
          await tester.tap(button);
          await tester.pumpAndSettle();
          expect(
            copied,
            publicDeploymentRecipe(
              domain: 'app.example.com',
              email: 'ops@example.com',
              docker: true,
              termsAccepted: true,
            )!.environment,
          );
          final method = find.byKey(const Key('deployment-method'));
          await tester.ensureVisible(method);
          await tester.pumpAndSettle();
          await tester.tap(method);
          await tester.pumpAndSettle();
          await tester.tap(
            find.widgetWithText(MenuItemButton, copy('deployment.native')),
          );
          await tester.pumpAndSettle();
          expect(
            tester
                .widget<TextField>(find.byKey(const Key('deployment-domain')))
                .controller!
                .text,
            'app.example.com',
          );
          expect(find.byKey(const Key('deployment-code-env')), findsNothing);
          expect(
            tester
                .widget<SelectableText>(
                  find.byKey(const Key('deployment-code-start')),
                )
                .data,
            contains('--access-address app.example.com:443'),
          );
          expect(tester.takeException(), isNull);
          await tester.pumpWidget(const SizedBox.shrink());
        },
      );
    }
  }
}

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final width in [1000.0, 390.0]) {
      testWidgets(
        'Set as default waits for owner acknowledgement ${language.name} ${width.toInt()}px',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 1000));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final profile = EgressProfileRevision(
            id: 'profile.private',
            revision: 2,
            displayName: 'Private proxy',
            policy: const TrafficEgressPolicy(
              proxy: TrafficProxyPolicy(
                kind: 'socks5',
                endpoint: '127.0.0.1:7890',
              ),
              resolver: TrafficResolverPolicy.system(),
            ),
            publishedAt: DateTime.utc(2026, 10, 6),
          );
          Map<String, Object?> setting(bool selected) => {
            'revision': selected ? 8 : 7,
            'profile': selected ? profile.toJson() : null,
            'effective': {
              'profile': (selected ? profile : EgressProfileRevision.direct)
                  .toJson(),
              'source': selected ? 'global' : 'builtin',
              'defaultRevision': selected ? 8 : 7,
            },
          };
          final pending = Completer<http.Response>();
          http.Response jsonResponse(Object value) => http.Response(
            jsonEncode(value),
            200,
            headers: {'content-type': 'application/json; charset=utf-8'},
          );
          var writes = 0;
          final client = MockClient((request) async {
            expect(request.url.origin, 'http://127.0.0.1:18662');
            if (request.url.path == '/api/v1/egress-profiles' &&
                request.method == 'GET') {
              return jsonResponse({
                'items': [
                  EgressProfileRevision.direct.toJson(),
                  profile.toJson(),
                ],
              });
            }
            if (request.url.path == '/api/v1/egress-default' &&
                request.method == 'GET') {
              return jsonResponse(setting(false));
            }
            if (request.url.path == '/api/v1/egress-default' &&
                request.method == 'PUT') {
              writes++;
              expect(request.headers['if-match'], '7');
              expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
              expect(request.headers['idempotency-key'], isNotEmpty);
              expect(jsonDecode(request.body), {
                'profile': {'id': 'profile.private', 'revision': 2},
              });
              return pending.future;
            }
            fail(
              'Unexpected control operation ${request.method} ${request.url.path}',
            );
          });
          final api = await HttpControlApi.connect(
            DesktopSession(
              instanceId: 'default-widget',
              baseUrl: Uri.parse('http://127.0.0.1:18662'),
              readToken: 'R' * 43,
              writeToken: 'W' * 43,
              expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
            ),
            inspectSession: false,
            client: client,
          );
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: false,
            closeRuntime: () async {},
          );
          addTearDown(controller.dispose);
          addTearDown(api.close);
          final workspace = PreviewControlApi();
          controller.data = await workspace.loadDashboard();
          controller.loading = false;
          await workspace.close();
          controller.selectSettingsTab(
            controller.settingsDestinations.length - 1,
          );
          await tester.runAsync(() async {
            await tester.pumpWidget(
              MaterialApp(
                theme: ViberTheme.light(),
                builder: (context, child) => MediaQuery(
                  data: MediaQuery.of(context).copyWith(
                    textScaler: TextScaler.linear(width == 390 ? 2 : 1),
                  ),
                  child: child!,
                ),
                home: Scaffold(
                  body: SettingsView(
                    controller: controller,
                    copy: AppCopy.forLanguage(language),
                  ),
                ),
              ),
            );
            await Future<void>.delayed(Duration.zero);
          });
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('egress-profile-row-profile.private')),
            findsOneWidget,
          );
          final action = find.byKey(
            const Key('egress-default-set-profile.private'),
          );
          expect(action, findsOneWidget);
          await tester.scrollUntilVisible(
            action,
            180,
            scrollable: find.descendant(
              of: find.byKey(const Key('egress-profiles-settings-scroll')),
              matching: find.byType(Scrollable),
            ),
          );
          await tester.pumpAndSettle();
          expect(action.hitTestable(), findsOneWidget);
          if (width == 390) {
            final element = tester.element(action);
            bool focused() {
              final focus = FocusManager.instance.primaryFocus?.context;
              if (focus == null) return false;
              var found = identical(focus, element);
              (focus as Element).visitAncestorElements((ancestor) {
                if (identical(ancestor, element)) {
                  found = true;
                  return false;
                }
                return true;
              });
              return found;
            }

            for (var i = 0; i < 48 && !focused(); i++) {
              await tester.sendKeyEvent(LogicalKeyboardKey.tab);
              await tester.pump();
            }
            expect(
              focused(),
              isTrue,
              reason: 'Default action must be keyboard reachable',
            );
            await tester.sendKeyEvent(LogicalKeyboardKey.enter);
          } else {
            await tester.tap(action);
          }
          await tester.pump();
          expect(writes, 1);
          expect(
            find.descendant(
              of: find.byKey(const Key('egress-default-summary')),
              matching: find.textContaining('Private proxy'),
              matchRoot: true,
            ),
            findsNothing,
          );
          await tester.runAsync(() async {
            pending.complete(jsonResponse(setting(true)));
            await Future<void>.delayed(Duration.zero);
          });
          await tester.pumpAndSettle();
          expect(
            find.descendant(
              of: find.byKey(const Key('egress-default-summary')),
              matching: find.textContaining('Private proxy'),
              matchRoot: true,
            ),
            findsOneWidget,
          );
          expect(writes, 1);
          expect(tester.takeException(), isNull);
        },
      );
    }
  }
}

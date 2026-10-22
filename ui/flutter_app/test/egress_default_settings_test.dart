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
      testWidgets(
        'Editing the selected VLESS default keeps its actual DNS visible until explicit application ${language.name} ${width.toInt()}px',
        (tester) => _editSelectedDefault(tester, language, width),
      );
    }
  }
}

Future<void> _editSelectedDefault(
  WidgetTester tester,
  AppLanguage language,
  double width,
) async {
  await tester.binding.setSurfaceSize(Size(width, 1000));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final english = language == AppLanguage.english;
  Map<String, Object?> profile(int revision, String transport) => {
    'id': 'profile.office',
    'revision': revision,
    'displayName': 'Office exit',
    'policy': {
      'proxy': {
        'kind': 'vless',
        'credentialRef': 'secret://egress-vless/fixture',
        'credentialRevision': 1,
      },
      'resolver': {
        'kind': 'doh',
        'dohUrl': 'https://1.1.1.1/dns-query',
        'transport': transport,
      },
      'directDomains': '',
    },
    'publishedAt': '2026-10-21T03:30:00Z',
  };
  final original = profile(2, 'direct');
  final updated = profile(3, 'proxy');
  var catalogProfile = original;
  var selectedProfile = original;
  var defaultRevision = 6;
  var profileWrites = 0;
  var defaultWrites = 0;
  late http.Request profileRequest;
  late http.Request defaultRequest;
  final acknowledgement = Completer<http.Response>();
  Map<String, Object?> setting() => {
    'revision': defaultRevision,
    'profile': selectedProfile,
    'effective': {
      'profile': selectedProfile,
      'source': 'global',
      'defaultRevision': defaultRevision,
    },
  };
  http.Response response(Object value) => http.Response(
    jsonEncode(value),
    200,
    headers: {'content-type': 'application/json; charset=utf-8'},
  );
  final api = await HttpControlApi.connect(
    DesktopSession(
      instanceId: 'default-revision-widget',
      baseUrl: Uri.parse('http://127.0.0.1:18662'),
      readToken: 'R' * 43,
      writeToken: 'W' * 43,
      expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
    ),
    inspectSession: false,
    client: MockClient((request) async {
      switch ((request.method, request.url.path)) {
        case ('GET', '/api/v1/egress-profiles'):
          return response({
            'items': [catalogProfile],
          });
        case ('GET', '/api/v1/egress-default'):
          return response(setting());
        case (
          'GET',
          '/api/v1/egress-profiles/profile.office/revisions/2/vless-settings',
        ):
          return response({'security': 'reality'});
        case ('PUT', '/api/v1/egress-profiles/profile.office'):
          profileWrites++;
          profileRequest = request;
          catalogProfile = updated;
          return response(updated);
        case ('PUT', '/api/v1/egress-default'):
          defaultWrites++;
          defaultRequest = request;
          return acknowledgement.future;
        default:
          fail('Unexpected ${request.method} ${request.url.path}');
      }
    }),
  );
  addTearDown(api.close);
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: false,
    closeRuntime: () async {},
  );
  addTearDown(controller.dispose);
  final workspace = PreviewControlApi();
  controller.data = await workspace.loadDashboard();
  controller.loading = false;
  await workspace.close();
  controller.selectSettingsTab(controller.settingsDestinations.length - 1);
  await tester.runAsync(() async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        builder: (context, child) => MediaQuery(
          data: MediaQuery.of(
            context,
          ).copyWith(textScaler: TextScaler.linear(width == 390 ? 2 : 1)),
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
  Future<void> tap(Finder finder) async {
    await tester.ensureVisible(finder);
    await tester.pumpAndSettle();
    expect(finder.hitTestable(), findsOneWidget);
    await tester.tap(finder);
    await tester.pumpAndSettle();
  }

  await tap(find.byKey(const Key('egress-profile-edit-profile.office')));
  await tap(find.byKey(const Key('egress-profile-doh-transport')));
  await tap(find.byKey(const Key('egress-profile-doh-via-proxy')).last);
  await tap(find.byKey(const Key('egress-profile-publish')));
  expect(profileWrites, 1);
  expect(defaultWrites, 0);
  expect(profileRequest.headers['if-match'], '2');
  expect(jsonDecode(profileRequest.body), {
    'displayName': 'Office exit',
    'policy': {
      'proxy': {
        'kind': 'vless',
        'credentialRef': 'secret://egress-vless/fixture',
        'credentialRevision': 1,
      },
      'resolver': {
        'kind': 'doh',
        'dohUrl': 'https://1.1.1.1/dns-query',
        'transport': 'proxy',
      },
    },
  });
  expect(find.byKey(const Key('egress-profile-mutation-error')), findsNothing);

  final actualPolicy = find.byKey(const Key('egress-default-policy'));
  final oldPolicy = english
      ? 'Revision 2 · VLESS · DoH · Directly'
      : '版本 2 · VLESS · DoH · 直连';
  final newPolicy = english
      ? 'Revision 3 · VLESS · DoH · Through VLESS'
      : '版本 3 · VLESS · DoH · 经 VLESS';
  expect(actualPolicy, findsOneWidget);
  expect(tester.widget<Text>(actualPolicy).data, oldPolicy);
  expect(
    tester
        .widget<Text>(find.byKey(const Key('egress-default-doh-endpoint')))
        .data,
    'https://1.1.1.1/dns-query',
  );
  await tester.ensureVisible(actualPolicy);
  await tester.pumpAndSettle();
  expect(actualPolicy.hitTestable(), findsOneWidget);
  final row = find.byKey(const Key('egress-profile-row-profile.office'));
  expect(
    find.descendant(
      of: row,
      matching: find.text(english ? 'Current default' : '当前默认'),
    ),
    findsNothing,
  );
  expect(
    find.descendant(
      of: row,
      matching: find.text(
        english ? 'Latest saved · Revision 3' : '最新保存 · 版本 3',
      ),
    ),
    findsOneWidget,
  );
  expect(
    find.descendant(
      of: row,
      matching: find.text(
        english ? 'VLESS · DoH · Through VLESS' : 'VLESS · DoH · 经 VLESS',
      ),
    ),
    findsOneWidget,
  );
  final notice = find.text(
    english
        ? 'Revision 3 is saved but not applied. The current default still uses revision 2.'
        : '版本 3 已保存但尚未应用，当前默认仍使用版本 2。',
  );
  expect(notice, findsOneWidget);
  final apply = find.widgetWithText(
    TextButton,
    english ? 'Use revision 3 as default' : '将版本 3 设为默认',
  );
  await tester.ensureVisible(apply);
  await tester.pumpAndSettle();
  expect(apply.hitTestable(), findsOneWidget);
  await tester.tap(apply);
  await tester.pump();
  expect(defaultWrites, 1);
  expect(defaultRequest.headers['if-match'], '6');
  expect(defaultRequest.headers['authorization'], 'Bearer ${'W' * 43}');
  expect(defaultRequest.headers['idempotency-key'], isNotEmpty);
  expect(jsonDecode(defaultRequest.body), {
    'profile': {'id': 'profile.office', 'revision': 3},
  });
  expect(tester.widget<Text>(actualPolicy).data, oldPolicy);
  expect(notice, findsOneWidget);
  expect(tester.widget<TextButton>(apply).onPressed, isNull);
  await tester.runAsync(() async {
    selectedProfile = updated;
    defaultRevision = 7;
    acknowledgement.complete(response(setting()));
    await Future<void>.delayed(Duration.zero);
  });
  await tester.pumpAndSettle();
  expect(tester.widget<Text>(actualPolicy).data, newPolicy);
  expect(notice, findsNothing);
  expect(apply, findsNothing);
  expect(
    find.descendant(
      of: row,
      matching: find.text(english ? 'Current default' : '当前默认'),
    ),
    findsOneWidget,
  );
  expect(profileWrites, 1);
  expect(defaultWrites, 1);
  expect(tester.takeException(), isNull);
}

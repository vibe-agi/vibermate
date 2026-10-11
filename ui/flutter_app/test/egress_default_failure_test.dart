import 'dart:convert';
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

http.Response jsonResponse(Object value, [int status = 200]) => http.Response(
  jsonEncode(value),
  status,
  headers: {'content-type': 'application/json; charset=utf-8'},
);
Map<String, Object?> settingWire(
  int revision,
  EgressProfileRevision? profile,
) => {
  'revision': revision,
  'profile': profile?.toJson(),
  'effective': {
    'profile': (profile ?? EgressProfileRevision.direct).toJson(),
    'source': profile == null ? 'builtin' : 'global',
    'defaultRevision': revision,
  },
};

Future<WorkbenchController> mountDefaults(
  WidgetTester tester,
  Future<http.Response> Function(http.Request) handler, {
  bool readOnly = false,
  bool waitForLoad = true,
  List<EgressProfileRevision>? profiles,
}) async {
  await tester.binding.setSurfaceSize(const Size(1000, 900));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final api = await HttpControlApi.connect(
    DesktopSession(
      instanceId: 'default-failure',
      baseUrl: Uri.parse('https://private-runtime.invalid:8443'),
      readToken: 'R' * 43,
      writeToken: 'W' * 43,
      expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
    ),
    inspectSession: false,
    client: MockClient((request) async {
      expect(request.url.origin, 'https://private-runtime.invalid:8443');
      if (request.url.path == '/api/v1/egress-profiles' &&
          request.method == 'GET') {
        return jsonResponse({
          'items': (profiles ?? [EgressProfileRevision.direct])
              .map((p) => p.toJson())
              .toList(),
        });
      }
      expect(request.url.path, '/api/v1/egress-default');
      return handler(request);
    }),
  );
  addTearDown(api.close);
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: false,
    closeRuntime: () async {},
    runtimeTarget: 'https://private-runtime.invalid:8443',
    webPrincipal: readOnly
        ? const RuntimeWebPrincipal(
            id: 'member',
            username: 'reader',
            role: RuntimeWebRole.member,
          )
        : null,
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
        home: Scaffold(
          body: SettingsView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await Future<void>.delayed(Duration.zero);
  });
  if (waitForLoad) {
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('egress-profile-row-profile.direct')),
      findsOneWidget,
    );
  }
  return controller;
}

Future<void> tapDefaultsAction(WidgetTester tester, Finder action) async {
  await tester.ensureVisible(action);
  await tester.runAsync(() async {
    await tester.tap(action);
    await Future<void>.delayed(Duration.zero);
  });
  await tester.pumpAndSettle();
}

void main() {
  for (final lateMutation in [false, true]) {
    testWidgets(
      'Late old Runtime ${lateMutation ? 'ack' : 'read'} cannot overwrite the current default',
      (tester) async {
        final delayed = Completer<http.Response>();
        var writes = 0;
        await mountDefaults(tester, (request) async {
          if (request.method == 'PUT') {
            writes++;
            return delayed.future;
          }
          return lateMutation
              ? jsonResponse(settingWire(7, null))
              : delayed.future;
        }, waitForLoad: lateMutation);
        if (lateMutation) {
          await tester.runAsync(() async {
            await tester.tap(
              find.byKey(const Key('egress-default-set-profile.direct')),
            );
            await Future<void>.delayed(Duration.zero);
          });
          await tester.pump();
          expect(writes, 1);
        }
        final current = EgressProfileRevision(
          id: 'profile.current',
          revision: 1,
          displayName: 'Current Runtime',
          policy: const TrafficEgressPolicy.direct(),
          publishedAt: DateTime.utc(2026, 10, 6),
        );
        await mountDefaults(
          tester,
          (request) async => jsonResponse(settingWire(3, current)),
          profiles: [EgressProfileRevision.direct, current],
        );
        await tester.runAsync(() async {
          delayed.complete(
            jsonResponse(
              settingWire(
                lateMutation ? 8 : 7,
                lateMutation ? EgressProfileRevision.direct : null,
              ),
            ),
          );
          await Future<void>.delayed(Duration.zero);
        });
        await tester.pumpAndSettle();
        expect(
          tester
              .widget<Text>(find.byKey(const Key('egress-default-summary')))
              .data,
          'Current default: Current Runtime',
        );
        expect(writes, lateMutation ? 1 : 0);
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets(
    'Pinned older default is not replaced by the latest catalog revision',
    (tester) async {
      EgressProfileRevision profile(int revision) => EgressProfileRevision(
        id: 'profile.pinned',
        revision: revision,
        displayName: 'Pinned proxy',
        policy: const TrafficEgressPolicy(
          proxy: TrafficProxyPolicy(kind: 'socks5', endpoint: '127.0.0.1:7890'),
          resolver: TrafficResolverPolicy.system(),
        ),
        publishedAt: DateTime.utc(2026, 10, revision),
      );
      var puts = 0;
      await mountDefaults(tester, (request) async {
        if (request.method != 'GET') {
          puts++;
        }
        return jsonResponse(settingWire(7, profile(1)));
      }, profiles: [EgressProfileRevision.direct, profile(2)]);
      final summary = find.byKey(const Key('egress-default-summary'));
      expect(
        tester.widget<Text>(summary).data,
        'Current default: Pinned proxy',
      );
      expect(
        find.descendant(
          of: find.byKey(const Key('egress-profile-row-profile.pinned')),
          matching: find.text('Current default'),
        ),
        findsNothing,
      );
      final details = find.byKey(const Key('egress-default-details'));
      expect(details, findsOneWidget);
      await tapDefaultsAction(tester, details);
      expect(find.textContaining('Revision 1'), findsWidgets);
      expect(puts, 0);
    },
  );

  testWidgets(
    'Unavailable older Runtime default is not presented as builtin Direct',
    (tester) async {
      var puts = 0;
      await mountDefaults(tester, (request) async {
        if (request.method != 'GET') {
          puts++;
        }
        return jsonResponse({
          'reasonCode': 'unavailable',
          'messageKey': 'error.unavailable',
        }, 404);
      });
      expect(find.text('The current default is unavailable.'), findsOneWidget);
      expect(find.byKey(const Key('egress-default-unset')), findsNothing);
      expect(
        tester
            .widget<TextButton>(
              find.byKey(const Key('egress-default-set-profile.direct')),
            )
            .onPressed,
        isNull,
      );
      expect(puts, 0);
    },
  );

  testWidgets(
    'Replacing the connected controller reloads defaults for that Runtime',
    (tester) async {
      await mountDefaults(
        tester,
        (request) async => jsonResponse(settingWire(7, null)),
      );
      final selected = EgressProfileRevision(
        id: 'profile.second',
        revision: 1,
        displayName: 'Second Runtime',
        policy: const TrafficEgressPolicy.direct(),
        publishedAt: DateTime.utc(2026, 10, 6),
      );
      var reads = 0;
      await mountDefaults(tester, (request) async {
        reads++;
        return jsonResponse(settingWire(3, selected));
      }, profiles: [EgressProfileRevision.direct, selected]);
      expect(reads, 1);
      expect(
        tester
            .widget<Text>(find.byKey(const Key('egress-default-summary')))
            .data,
        contains('Second Runtime'),
      );
    },
  );

  testWidgets(
    'Clear default sends null without turning builtin into explicit Direct',
    (tester) async {
      var puts = 0;
      await mountDefaults(tester, (request) async {
        if (request.method == 'GET') {
          return jsonResponse(settingWire(7, EgressProfileRevision.direct));
        }
        expect(request.method, 'PUT');
        puts++;
        expect(request.headers['if-match'], '7');
        expect(jsonDecode(request.body), {'profile': null});
        return jsonResponse(settingWire(8, null));
      });
      final clear = find.byKey(const Key('egress-default-clear'));
      expect(clear, findsOneWidget);
      await tapDefaultsAction(tester, clear);
      expect(puts, 1);
      expect(find.byKey(const Key('egress-default-unset')), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );

  for (final status in [409, 503]) {
    testWidgets(
      'Failed default write $status observes state without claiming its own success or retrying',
      (tester) async {
        var reads = 0, puts = 0;
        await mountDefaults(tester, (request) async {
          if (request.method == 'GET') {
            reads++;
            return jsonResponse(
              settingWire(
                reads == 1 ? 7 : 9,
                reads == 1 ? null : EgressProfileRevision.direct,
              ),
            );
          }
          expect(request.method, 'PUT');
          puts++;
          return jsonResponse({
            'reasonCode': 'egress_profile_conflict',
            'messageKey': 'error.egress_profile_conflict',
          }, status);
        });
        await tapDefaultsAction(
          tester,
          find.byKey(const Key('egress-default-set-profile.direct')),
        );
        expect(puts, 1);
        expect(reads, 2);
        expect(
          find.byKey(const Key('egress-default-unconfirmed')),
          findsOneWidget,
        );
        expect(find.textContaining('Current default'), findsWidgets);
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('Read only settings do not offer owner default mutations', (
    tester,
  ) async {
    var puts = 0;
    await mountDefaults(tester, (request) async {
      if (request.method != 'GET') {
        puts++;
      }
      return jsonResponse(settingWire(7, EgressProfileRevision.direct));
    }, readOnly: true);
    expect(
      find.byKey(const Key('egress-default-set-profile.direct')).hitTestable(),
      findsNothing,
    );
    expect(
      find.byKey(const Key('egress-default-clear')).hitTestable(),
      findsNothing,
    );
    expect(puts, 0);
    expect(tester.takeException(), isNull);
  });
}

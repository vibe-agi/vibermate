import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test('allowlist contract is parsed strictly', () {
    final allowlist = ServerIpAllowlist.fromJson({
      'schema': 'vibermate-server-ip-allowlist-v1',
      'revision': 3,
      'ranges': ['203.0.113.0/24', '2001:db8::/32'],
      'updatedAt': '2026-09-29T12:00:00Z',
      'maxRanges': 256,
      'clientAddress': '203.0.113.9',
      'refused': {
        'count': 2,
        'lastAddress': '198.51.100.4',
        'lastAt': '2026-09-29T11:59:00Z',
      },
      'trustedProxies': ['10.0.0.0/24'],
      'proxyHeaderProblems': 1,
    }, 'allowlist');
    expect(allowlist.revision, 3);
    expect(allowlist.ranges, ['203.0.113.0/24', '2001:db8::/32']);
    expect(allowlist.restricted, isTrue);
    expect(allowlist.clientAddress, '203.0.113.9');
    expect(allowlist.refusedCount, 2);
    expect(allowlist.lastRefusedAddress, '198.51.100.4');
    expect(allowlist.trustedProxies, ['10.0.0.0/24']);
    expect(allowlist.proxyHeaderProblems, 1);

    for (final broken in <Map<String, Object?>>[
      {
        'schema': 'other',
        'revision': 0,
        'ranges': <String>[],
        'maxRanges': 256,
        'refused': {'count': 0},
        'trustedProxies': <String>[],
        'proxyHeaderProblems': 0,
      },
      {
        'schema': 'vibermate-server-ip-allowlist-v1',
        'revision': 0,
        'ranges': ['10.0.0.0/8', '10.0.0.0/8'],
        'maxRanges': 256,
        'refused': {'count': 0},
        'trustedProxies': <String>[],
        'proxyHeaderProblems': 0,
      },
      {
        'schema': 'vibermate-server-ip-allowlist-v1',
        'revision': 0,
        'ranges': <String>[],
        'maxRanges': 256,
        'refused': {'count': 0},
        'trustedProxies': <String>[],
        'proxyHeaderProblems': 0,
        'deny': <String>[],
      },
    ]) {
      expect(
        () => ServerIpAllowlist.fromJson(broken, 'allowlist'),
        throwsA(isA<ControlContractException>()),
      );
    }
  });

  test('problem details point at the entry to fix', () {
    final problem = ControlProblem.fromJson({
      'type': 'urn:vibermate:error:ip-allowlist-entry-invalid',
      'title': 'Unprocessable Entity',
      'status': 422,
      'code': 'ip_allowlist_entry_invalid',
      'entry': 1,
      'reason': 'host_bits',
      'suggestion': '198.51.100.0/24',
    }, status: 422);
    expect(problem.entry, 1);
    expect(problem.reason, 'host_bits');
    expect(problem.suggestion, '198.51.100.0/24');
    expect(
      () => ControlProblem.fromJson({
        'type': 'urn:vibermate:error:ip-allowlist-entry-invalid',
        'title': 'Unprocessable Entity',
        'status': 422,
        'code': 'ip_allowlist_entry_invalid',
        'reason': 'Not A Code',
      }, status: 422),
      throwsA(isA<ControlContractException>()),
    );
  });

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets('Owner edits the IP allowlist in ${language.name}', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copy = AppCopy.forLanguage(language);
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: false,
        serverManagement: true,
        terminalManagement: false,
        runtimeTarget: 'server.local:9666',
        closeRuntime: api.close,
        initialPreferences: WorkbenchPreferences(
          section: WorkbenchSection.settings,
          language: language,
        ),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      controller.openAccessSettings();
      await tester.pumpAndSettle();

      final panel = find.byKey(const Key('server-ip-allowlist'));
      await tester.ensureVisible(panel);
      await tester.pumpAndSettle();
      expect(find.text(copy('server.ip_allowlist.open')), findsOneWidget);
      expect(
        find.text(
          copy.format('server.ip_allowlist.client', {
            'address': '198.51.100.23',
          }),
        ),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('server-ip-allowlist-refused')),
        findsOneWidget,
      );
      final save = find.byKey(const Key('server-ip-allowlist-save'));
      expect(tester.widget<FilledButton>(save).onPressed, isNull);

      final field = find.byKey(const Key('server-ip-allowlist-field'));
      Future<void> saveList(String text) async {
        await tester.enterText(field, text);
        await tester.pump();
        await tester.ensureVisible(save);
        await tester.tap(save);
        await tester.pumpAndSettle();
      }

      // A host address inside the network points at its line and the fix.
      await saveList('198.51.100.0/24\n\n10.1.2.3/8');
      expect(
        find.text(
          copy.format('server.ip_allowlist.error.line', {
            'line': 3,
            'reason': copy.format('server.ip_allowlist.reason.host_bits', {
              'suggestion': '10.0.0.0/8',
            }),
          }),
        ),
        findsOneWidget,
      );

      // A list without this browser's address would disconnect the Owner.
      await saveList('203.0.113.0/24');
      expect(
        find.text(
          copy.format('server.ip_allowlist.error.excludes', {
            'address': '198.51.100.23',
          }),
        ),
        findsOneWidget,
      );

      final addMine = find.byKey(const Key('server-ip-allowlist-add-mine'));
      await tester.ensureVisible(addMine);
      await tester.tap(addMine);
      await tester.pump();
      expect(
        tester.widget<TextField>(field).controller!.text,
        '203.0.113.0/24\n198.51.100.23',
      );
      await tester.ensureVisible(save);
      await tester.tap(save);
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('server-ip-allowlist-saved')),
        findsOneWidget,
      );
      expect(
        find.text(copy.format('server.ip_allowlist.restricted', {'count': 2})),
        findsOneWidget,
      );
      expect(controller.serverIpAllowlist?.revision, 1);
      expect(controller.serverIpAllowlist?.ranges, [
        '203.0.113.0/24',
        '198.51.100.23',
      ]);

      // Another session saves first: the latest list is shown for review.
      await api.replaceServerIpAllowlist(
        revision: 1,
        ranges: const ['198.51.100.0/24'],
      );
      await saveList('198.51.100.23\n2001:db8::/32');
      expect(
        find.text(copy('server.ip_allowlist.error.conflict')),
        findsOneWidget,
      );
      expect(
        tester.widget<TextField>(field).controller!.text,
        '198.51.100.0/24',
      );
      expect(tester.takeException(), isNull);

      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    });
  }

  for (final (name, allowlist, visible, hidden) in [
    (
      'behind a trusted load balancer',
      const ServerIpAllowlist(
        revision: 0,
        ranges: [],
        maxRanges: 256,
        clientAddress: '203.0.113.9',
        trustedProxies: ['10.0.0.0/24'],
        proxyHeaderProblems: 3,
      ),
      ['server-ip-allowlist-trusted', 'server-ip-allowlist-proxy-problems'],
      ['server-ip-allowlist-private-hint'],
    ),
    (
      'reached through this machine',
      const ServerIpAllowlist(
        revision: 0,
        ranges: [],
        maxRanges: 256,
        clientAddress: '127.0.0.1',
      ),
      ['server-ip-allowlist-loopback-hint'],
      ['server-ip-allowlist-private-hint', 'server-ip-allowlist-trusted'],
    ),
    (
      'seen from a private address',
      const ServerIpAllowlist(
        revision: 0,
        ranges: [],
        maxRanges: 256,
        clientAddress: '172.17.0.1',
      ),
      ['server-ip-allowlist-private-hint'],
      [
        'server-ip-allowlist-trusted',
        'server-ip-allowlist-proxy-problems',
        'server-ip-allowlist-loopback-hint',
      ],
    ),
  ]) {
    testWidgets('allowlist panel explains the address $name', (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: false,
        serverManagement: true,
        terminalManagement: false,
        runtimeTarget: 'server.local:9666',
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.settings,
        ),
      );
      await controller.initialize();
      // The scenario stands in for what the Server reports.
      controller.serverIpAllowlist = allowlist;
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      controller.openAccessSettings();
      await tester.pumpAndSettle();
      await tester.ensureVisible(find.byKey(const Key('server-ip-allowlist')));
      await tester.pumpAndSettle();
      for (final key in visible) {
        expect(find.byKey(Key(key)), findsOneWidget, reason: key);
      }
      for (final key in hidden) {
        expect(find.byKey(Key(key)), findsNothing, reason: key);
      }
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    });
  }
}

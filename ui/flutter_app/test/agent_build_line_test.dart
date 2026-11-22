import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/agent_connection.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';

void main() {
  Future<WorkbenchController> open(
    WidgetTester tester, {
    required PreviewFreshClient client,
    AppLanguage language = AppLanguage.english,
  }) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = PreviewFixture.freshInstall(
      stage: PreviewFreshStage.waiting,
      client: client,
    );
    final controller = WorkbenchController(
      api: fixture.api,
      terminalCommands: fixture.terminalCommands,
      previewMode: true,
      closeRuntime: fixture.api.close,
      initialPreferences: WorkbenchPreferences(language: language),
    );
    await controller.initialize();
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: WorkbenchShell(controller: controller),
      ),
    );
    await tester.pump(const Duration(milliseconds: 300));
    return controller;
  }

  Future<void> openRun(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    final capture = controller.data!.captures.single;
    await tester.tap(find.byKey(Key('capture-row-${capture.key}')));
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump(const Duration(milliseconds: 300));
  }

  Future<void> close(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
    await tester.pump();
  }

  Text line(WidgetTester tester) =>
      tester.widget<Text>(find.byKey(const Key('capture-client-line')));

  testWidgets('a known release reads as one in the success colour', (
    tester,
  ) async {
    final controller = await open(tester, client: PreviewFreshClient.known);
    await openRun(tester, controller);
    expect(line(tester).data, 'Version 2.1.220 · Known release');
    final context = tester.element(
      find.byKey(const Key('capture-client-line')),
    );
    expect(line(tester).style?.color, context.viberColors.verified);
    await close(tester, controller);
  });

  testWidgets('an official build not yet listed is muted, not a warning', (
    tester,
  ) async {
    final controller = await open(
      tester,
      client: PreviewFreshClient.recognized,
    );
    // The Runtime records a version only for a verified release, and its
    // only client-reported version attribute is Codex's.
    expect(controller.data!.captures.single.managedRun!.clientAdapter, isNull);
    await openRun(tester, controller);
    expect(
      line(tester).data,
      'Version unknown · Official build, not yet listed',
    );
    final context = tester.element(
      find.byKey(const Key('capture-client-line')),
    );
    expect(line(tester).style?.color, context.viberColors.textMuted);
    expect(line(tester).style?.color, isNot(context.viberColors.warning));

    // The explanation opens on click, not only on hover.
    await tester.tap(find.byKey(const Key('capture-client-help')));
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(
      find.textContaining(
        "ViberMate checked that Anthropic signed this program. This exact "
        "version hasn't been tested with ViberMate yet.",
      ),
      findsOneWidget,
    );
    expect(
      find.textContaining('This is the Claude Code version.'),
      findsOneWidget,
    );
    await tester.tap(find.text('Dismiss'));
    await tester.pump(const Duration(milliseconds: 300));
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('an unchecked build under a decrypting set cannot connect', (
    tester,
  ) async {
    final controller = await open(
      tester,
      client: PreviewFreshClient.unverified,
    );
    final capture = controller.data!.captures.single;
    final badge = find.byKey(Key('capture-outcome-${capture.key}'));
    expect(tester.widget<Text>(badge).data, "Can't connect");
    final context = tester.element(badge);
    expect(tester.widget<Text>(badge).style?.color, context.viberColors.danger);
    // The guide stops waiting for a request that cannot arrive.
    expect(find.byKey(const Key('setup-cannot-connect')), findsOneWidget);
    expect(find.byKey(const Key('setup-prompt-waiting')), findsNothing);
    expect(
      find.text(
        'Type anything in your agent. Its requests appear here within a few '
        'seconds.',
      ),
      findsNothing,
      reason: 'only the reason it cannot connect, never a promise',
    );
    expect(
      find.descendant(
        of: find.byKey(const Key('setup-step-prompt')),
        matching: find.text("Can't connect"),
      ),
      findsOneWidget,
    );

    await openRun(tester, controller);
    expect(
      line(tester).data,
      "Version unknown · Can't connect through ViberMate",
    );
    expect(line(tester).style?.color, context.viberColors.danger);
    final message = find.byKey(const Key('capture-cannot-connect'));
    expect(message, findsOneWidget);
    expect(
      find.descendant(
        of: message,
        matching: find.textContaining(
          "ViberMate can't verify this copy of Claude Code",
        ),
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: message,
        matching: find.textContaining(
          'Versions ViberMate can connect on this Mac: 2.1.220.',
        ),
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await close(tester, controller);
  });

  testWidgets('the Chinese labels follow the same states', (tester) async {
    final controller = await open(
      tester,
      client: PreviewFreshClient.recognized,
      language: AppLanguage.simplifiedChinese,
    );
    await openRun(tester, controller);
    expect(line(tester).data, '版本未知 · 官方构建，尚未收录');
    await close(tester, controller);
  });

  test('only a set that decrypts the agent\'s own service blocks it', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final environments = (await api.loadDashboard()).environments;
    EnvironmentRecord named(String id) =>
        environments.singleWhere((value) => value.id == id);
    CaptureRecord run(String label, String recognition) => CaptureRecord(
      key: 'managed_run:$label',
      id: label,
      kind: 'managed_run',
      displayName: label,
      state: 'attached',
      observation: 'waiting_for_traffic',
      createdAt: DateTime.utc(2026, 8, 10),
      updatedAt: DateTime.utc(2026, 8, 10),
      managedRun: ManagedRunSummary(
        executableLabel: label,
        cwd: '/tmp',
        canonicalExecutablePath: '/usr/local/bin/$label',
        recognition: recognition,
        expiresAt: DateTime.utc(2026, 8, 11),
        processId: 42,
      ),
    );
    // Research decrypts only Anthropic's API.
    final research = named('research');
    expect(agentCannotConnect(run('claude', 'unverified'), research), isTrue);
    expect(agentCannotConnect(run('codex', 'unverified'), research), isFalse);
    expect(agentCannotConnect(run('claude', 'recognized'), research), isFalse);
    expect(agentCannotConnect(run('claude', 'verified'), research), isFalse);
    expect(
      agentCannotConnect(
        run('codex', 'unverified'),
        named('system_transparent'),
      ),
      isTrue,
    );
    expect(agentCannotConnect(run('claude', 'unverified'), null), isFalse);
    // A session that delivered a request evidently connected.
    final connected = run('claude', 'unverified');
    expect(
      agentCannotConnect(
        CaptureRecord(
          key: connected.key,
          id: connected.id,
          kind: connected.kind,
          displayName: connected.displayName,
          state: connected.state,
          observation: 'observed',
          createdAt: connected.createdAt,
          updatedAt: connected.updatedAt,
          managedRun: connected.managedRun,
        ),
        research,
      ),
      isFalse,
    );
  });

  test('ACP records never count as a launch that did not start', () {
    CaptureRecord record({String? transport}) => CaptureRecord(
      key: 'managed_run:acp',
      id: 'acp',
      kind: 'managed_run',
      displayName: 'Codex',
      state: 'finished',
      observation: 'waiting_for_traffic',
      createdAt: DateTime.utc(2026, 8, 10),
      updatedAt: DateTime.utc(2026, 8, 10),
      transport: transport,
      managedRun: ManagedRunSummary(
        executableLabel: 'codex',
        cwd: '/tmp',
        canonicalExecutablePath: '/usr/local/bin/codex',
        recognition: 'verified',
        expiresAt: DateTime.utc(2026, 8, 11),
      ),
    );
    expect(record().stoppedBeforeStart, isTrue);
    expect(record(transport: 'acp_stdio').stoppedBeforeStart, isFalse);
  });
}

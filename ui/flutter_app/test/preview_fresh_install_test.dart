import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_dataset.dart';

void main() {
  test('the URL query selects the fresh install, its stage and timeline', () {
    expect(PreviewDataset.fromQuery(const {}).fresh, isFalse);
    expect(
      PreviewDataset.fromQuery(const {'stage': 'waiting'}).fresh,
      isFalse,
      reason: 'a stage alone does not replace the seeded workbench',
    );
    final fresh = PreviewDataset.fromQuery(const {'dataset': 'fresh'});
    expect(fresh.fresh, isTrue);
    expect(fresh.effectiveStage, PreviewFreshStage.start);
    for (final stage in PreviewFreshStage.values) {
      expect(
        PreviewDataset.fromQuery({
          'dataset': 'fresh',
          'stage': stage.name,
        }).effectiveStage,
        stage,
      );
    }
    final arriving = PreviewDataset.fromQuery(const {
      'dataset': 'fresh',
      'arrive': 'failed',
    });
    expect(arriving.arrive, PreviewFreshArrival.failedLaunch);
    expect(arriving.effectiveStage, PreviewFreshStage.command);
    expect(
      PreviewDataset.fromQuery(const {
        'dataset': 'fresh',
        'arrive': '1',
      }).arrive,
      PreviewFreshArrival.firstRequest,
    );
  });

  test('the default preview keeps its seeded workbench', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    expect(dashboard.captures, hasLength(9));
    expect(
      dashboard.environments.map((value) => value.id),
      orderedEquals(['system_transparent', 'work', 'research']),
    );
    expect(dashboard.accounts, hasLength(5));
    expect(dashboard.endpoints, hasLength(4));
    expect(await api.pendingApprovals(), hasLength(1));
    expect((await api.loadNetwork()).rules.mode, 'ask_unknown');
  });

  test('a fresh install has only what ships built in', () async {
    final fixture = PreviewFixture.freshInstall();
    final api = fixture.api;
    addTearDown(api.close);
    final dashboard = await api.loadDashboard();
    expect(dashboard.captures, isEmpty);
    expect(dashboard.captureNextCursor, isNull);
    expect(dashboard.environments.single.id, 'system_transparent');
    expect(dashboard.environments.single.systemOwned, isTrue);
    expect(dashboard.accounts, isEmpty);
    expect(dashboard.endpoints.map((value) => value.id).toSet(), {
      'target.anthropic.official',
      'target.openai.official',
      'target.codex.official',
    });
    expect(await api.pendingApprovals(), isEmpty);
    expect(await api.decidedApprovals(), isEmpty);
    final network = await api.loadNetwork();
    expect(network.rules.rules, isEmpty);
    expect(network.rules.mode, 'monitor');
    expect(network.connections.items, isEmpty);
    expect(network.egressAttempts.items, isEmpty);
    expect(await api.runtimeUsers(), isEmpty);
    final status = await api.loadRuntimeStatus();
    expect(status.networkAccess?.socksAddress, isNull);
    expect(status.networkAccess?.vlessAvailable, isFalse);
    final root = await api.rootCA();
    expect(root.certificatePresent, 'absent');
    expect(root.trustDecision, 'untrusted');
    expect(
      (await fixture.terminalCommands.inspect()).state,
      TerminalCommandState.notInstalled,
    );
    final storage = await api.storageLocation();
    expect(storage.evidenceBytes, 0);
    expect(storage.databaseBytes, lessThan(1 << 20));
    final activity = await api.scriptActivity();
    expect(activity.requests, 0);
    expect(activity.scripts, isEmpty);
  });

  test('each fresh stage fixes one point of the first run', () async {
    Future<
      ({
        PreviewControlApi api,
        TerminalCommandState command,
        bool trusted,
        List<CaptureRecord> captures,
      })
    >
    stage(PreviewFreshStage value) async {
      final fixture = PreviewFixture.freshInstall(stage: value);
      addTearDown(fixture.api.close);
      final root = await fixture.api.rootCA();
      return (
        api: fixture.api,
        command: (await fixture.terminalCommands.inspect()).state,
        trusted: root.installed,
        captures: (await fixture.api.loadDashboard()).captures,
      );
    }

    final start = await stage(PreviewFreshStage.start);
    expect(start.command, TerminalCommandState.notInstalled);
    expect(start.trusted, isFalse);
    expect(start.captures, isEmpty);

    final command = await stage(PreviewFreshStage.command);
    expect(command.command, TerminalCommandState.current);
    expect(command.trusted, isFalse);
    expect(command.captures, isEmpty);

    final trusted = await stage(PreviewFreshStage.trusted);
    expect(trusted.command, TerminalCommandState.current);
    expect(trusted.trusted, isTrue);

    final waiting = await stage(PreviewFreshStage.waiting);
    final run = waiting.captures.single;
    expect(run.displayName, 'Claude Code');
    expect(run.running, isTrue);
    expect(run.observation, 'waiting_for_traffic');
    expect(run.managedRun!.processId, isNotNull);
    expect((await waiting.api.activities(captureRunId: run.id)).items, isEmpty);

    final failed = await stage(PreviewFreshStage.failed);
    final stopped = failed.captures.single;
    expect(stopped.displayName, 'Codex');
    expect(stopped.running, isFalse);
    expect(stopped.observation, 'waiting_for_traffic');
    expect(stopped.managedRun!.processId, isNull);
    expect(failed.trusted, isFalse);
    expect(
      (await failed.api.captureAssignment(stopped.key)).environmentId,
      'system_transparent',
    );

    final observed = await stage(PreviewFreshStage.observed);
    expect(
      observed.captures.single.observation,
      'waiting_for_traffic',
      reason: 'the guide is on screen before the first request arrives',
    );
    final next = (await observed.api.loadDashboard()).captures.single;
    expect(next.observation, 'observed');
    final requests = await observed.api.activities(captureRunId: next.id);
    expect(requests.items, hasLength(1));
    expect(requests.items.single.status, 'succeeded');
  });

  testWidgets('arrive timelines add their launch after eight seconds', (
    tester,
  ) async {
    final first = PreviewControlApi.freshInstall(
      stage: PreviewFreshStage.command,
      arrive: PreviewFreshArrival.firstRequest,
    );
    final failed = PreviewControlApi.freshInstall(
      stage: PreviewFreshStage.command,
      arrive: PreviewFreshArrival.failedLaunch,
    );
    Future<List<CaptureRecord>> captures(PreviewControlApi api) async =>
        (await api.loadDashboard()).captures;

    await tester.pump(const Duration(seconds: 7));
    expect(await captures(first), isEmpty);
    expect(await captures(failed), isEmpty);
    await tester.pump(const Duration(seconds: 2));
    final waiting = (await captures(first)).single;
    expect(waiting.observation, 'waiting_for_traffic');
    expect(waiting.running, isTrue);
    final stopped = (await captures(failed)).single;
    expect(stopped.running, isFalse);
    expect(stopped.managedRun!.processId, isNull);
    await tester.pump(const Duration(seconds: 8));
    expect((await captures(first)).single.observation, 'observed');

    await first.close();
    await failed.close();
  });
}

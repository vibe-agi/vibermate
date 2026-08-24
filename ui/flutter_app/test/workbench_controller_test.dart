import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test(
    'Capture detail selects one proven conversation instead of mixing agents',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.selectCapture('managed_run:run-1');

      expect(controller.captureConversations.length, greaterThan(2));
      expect(
        controller.captureConversations
            .map((item) => item.conversation.kind)
            .toSet(),
        containsAll(<String>['main', 'agent', 'isolated_subagent']),
      );
      expect(controller.selectedCaptureConversation?.conversation.kind, 'main');
      expect(controller.selectedActivities, isNotEmpty);
      expect(
        controller.selectedActivities.every(
          (activity) =>
              activity.conversation.id ==
              controller.selectedCaptureConversationKey,
        ),
        isTrue,
      );

      final subagent = controller.captureConversations.firstWhere(
        (item) => item.conversation.kind == 'isolated_subagent',
      );
      await controller.selectCaptureConversation(subagent.key);

      expect(controller.selectedCaptureConversationKey, subagent.key);
      expect(controller.selectedActivities, hasLength(1));
      expect(
        controller.selectedActivities.single.conversation.id,
        subagent.key,
      );
    },
  );

  test(
    'Conversation switching keeps its bounded view visible while refreshing',
    () async {
      final fixture = PreviewControlApi();
      final api = _ControlledActivityApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      await controller.selectCapture('managed_run:run-1');
      final main = controller.selectedCaptureConversation!;
      final mainActivityIds = controller.selectedActivities
          .map((activity) => activity.id)
          .toList(growable: false);
      final subagent = controller.captureConversations.firstWhere(
        (item) => item.conversation.kind == 'isolated_subagent',
      );
      await controller.selectCaptureConversation(subagent.key);

      api.blockConversation(main.key);
      final switchBack = controller.selectCaptureConversation(main.key);

      expect(controller.selectedCaptureConversationKey, main.key);
      expect(controller.captureActivitiesLoading, isTrue);
      expect(
        controller.selectedActivities.map((activity) => activity.id),
        mainActivityIds,
      );

      api.releaseConversation();
      await switchBack;
      expect(controller.captureActivitiesLoading, isFalse);
      expect(
        controller.selectedActivities.map((activity) => activity.id),
        mainActivityIds,
      );
    },
  );

  test('native Client Session timeline crosses Capture boundaries', () async {
    final fixture = PreviewControlApi();
    final api = _ControlledActivityApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);

    controller.data = await fixture.loadDashboard();
    await controller.selectCapture('managed_run:run-2');

    final selected = controller.selectedCaptureConversation;
    expect(selected?.conversation.evidence, 'explicit_session');
    final request = api.activityRequests.last;
    expect(request.conversationId, selected?.key);
    expect(request.captureRunId, isNull);
    expect(request.manualCaptureId, isNull);
  });

  test(
    'Raw evidence loads on demand without retaining revealed bytes',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      const exchangeId = 'run-1-exchange-222';
      expect(controller.rawEvidence(exchangeId), isNull);

      final page = await controller.loadRawEvidence(exchangeId);
      expect(page?.items, hasLength(1));
      expect(controller.rawEvidence(exchangeId), same(page));
      expect(controller.rawEvidenceError(exchangeId), isNull);

      final revealed = await controller.revealRawEvidence(
        exchangeId: exchangeId,
        envelopeId: page!.items.single.envelopeId,
      );
      expect(revealed?.body, isNotEmpty);
      expect(
        controller.rawEvidence(exchangeId)?.items.single.bodyBytes,
        greaterThan(0),
      );
    },
  );

  test('Exchange refresh supersedes an older in-flight response', () async {
    final fixtureApi = PreviewControlApi();
    addTearDown(fixtureApi.close);
    const exchangeId = 'run-1-exchange-222';
    final stale = await fixtureApi.exchange(exchangeId);
    final fresh = await fixtureApi.exchange(exchangeId, contentView: 'full');
    final staleLoad = Completer<ExchangeDetail>();
    final freshLoad = Completer<ExchangeDetail>();
    final api = _SequencedExchangeApi([staleLoad.future, freshLoad.future]);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: () async {},
    );
    addTearDown(controller.dispose);

    final first = controller.loadExchangeDetail(exchangeId);
    final refresh = controller.loadExchangeDetail(exchangeId, refresh: true);
    expect(api.exchangeCalls, 2);

    freshLoad.complete(fresh);
    expect(await refresh, same(fresh));
    staleLoad.complete(stale);
    expect(await first, same(stale));

    expect(controller.exchangeDetail(exchangeId), same(fresh));
    expect(controller.exchangeError(exchangeId), isNull);
    expect(controller.exchangeIsLoading(exchangeId), isFalse);
  });

  test(
    'Capture directory loads every stable page without duplicates',
    () async {
      final api = PreviewControlApi(dashboardCaptureLimit: 2);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      expect(controller.data!.captures, hasLength(2));
      expect(controller.data!.captureNextCursor, isNotNull);
      expect(controller.runningCaptures, hasLength(2));

      await controller.loadMoreCaptures();

      final captures = controller.data!.captures;
      expect(captures, hasLength(9));
      expect(captures.map((capture) => capture.key).toSet(), hasLength(9));
      expect(controller.data!.captureNextCursor, isNull);
      expect(controller.runningCaptures, hasLength(8));
      expect(controller.historicalCaptures, hasLength(1));
      expect(controller.captureDirectoryError, isNull);
    },
  );

  test(
    'Capture deletion reconciles the dashboard and repairs a stale selection',
    () async {
      final fixture = PreviewControlApi();
      final api = _DeletionTrackingApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      final target = controller.data!.captures.firstWhere(
        (capture) => !capture.running,
      );
      controller.selectedCaptureKey = target.key;

      final outcome = await controller.deleteCapture(target.key);

      expect(outcome?.deleted, isTrue);
      expect(api.dashboardLoads, 1);
      expect(
        controller.data!.captures.any((capture) => capture.key == target.key),
        isFalse,
      );
      expect(controller.selectedCaptureKey, isNot(target.key));
      expect(
        controller.data!.captures.any(
          (capture) => capture.key == controller.selectedCaptureKey,
        ),
        isTrue,
      );
      expect(controller.inventoryNotice, 'capture_deleted');
    },
  );

  test(
    'Environment deletion reloads authority and cannot leave a retired selection',
    () async {
      final fixture = PreviewControlApi();
      final api = _DeletionTrackingApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);

      controller.data = await fixture.loadDashboard();
      controller.selectedEnvironmentId = 'research';

      final outcome = await controller.deleteEnvironment('research');

      expect(outcome?.deleted, isTrue);
      expect(api.dashboardLoads, 1);
      expect(
        controller.data!.environments.any(
          (environment) => environment.id == 'research',
        ),
        isFalse,
      );
      expect(controller.selectedEnvironmentId, isNot('research'));
      expect(controller.selectedEnvironment, isNotNull);
      expect(controller.inventoryNotice, 'environment_deleted');
    },
  );

  test(
    'Preview Capture cursor walks every bounded page in stable order',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final seen = <String>[];
      String? cursor;

      do {
        final page = await api.captures(cursor: cursor, limit: 2);
        seen.addAll(page.items.map((capture) => capture.key));
        cursor = page.nextCursor;
      } while (cursor != null);

      expect(seen, hasLength(9));
      expect(seen.toSet(), hasLength(9));
      expect(seen.take(8).every((key) => key != 'managed_run:run-8'), isTrue);
      expect(seen.last, 'managed_run:run-8');
    },
  );

  test(
    'Terminal command changes only through an inspected closed operation',
    () async {
      final api = PreviewControlApi();
      final terminal = PreviewTerminalCommandService();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: terminal,
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.refreshTerminalCommand();
      expect(
        controller.terminalCommand!.state,
        TerminalCommandState.notInstalled,
      );
      expect(
        await controller.changeTerminalCommand(
          TerminalCommandOperation.install,
        ),
        isTrue,
      );
      expect(controller.terminalCommand!.state, TerminalCommandState.current);
      expect(controller.terminalCommandNotice, 'terminal.notice.installed');
      expect(
        await controller.changeTerminalCommand(
          TerminalCommandOperation.install,
        ),
        isFalse,
      );
      expect(
        await controller.changeTerminalCommand(TerminalCommandOperation.remove),
        isTrue,
      );
      expect(
        controller.terminalCommand!.state,
        TerminalCommandState.notInstalled,
      );
      expect(controller.terminalCommandNotice, 'terminal.notice.removed');

      final repairService = PreviewTerminalCommandService(
        initial: const TerminalCommandStatus(
          state: TerminalCommandState.targetMissing,
          sourcePath: '/Applications/ViberMate.app/Contents/MacOS/vibermate',
          targetPath: '/Users/preview/.local/bin/vibermate',
        ),
      );
      final repairController = WorkbenchController(
        api: api,
        terminalCommands: repairService,
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(repairController.dispose);
      await repairController.refreshTerminalCommand();
      expect(
        await repairController.changeTerminalCommand(
          TerminalCommandOperation.repair,
        ),
        isTrue,
      );
      expect(
        repairController.terminalCommand!.state,
        TerminalCommandState.current,
      );
      expect(
        repairController.terminalCommandNotice,
        'terminal.notice.repaired',
      );
    },
  );

  test(
    'Offline hold enters and resumes through exact runtime revisions',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      expect(controller.offlineHold!.state, 'online');
      expect(controller.offlineHold!.revision, 1);

      expect(await controller.enterOfflineHold(), isTrue);
      expect(controller.offlineHold!.state, 'held');
      expect(controller.offlineHold!.revision, 3);
      expect(controller.offlineHold!.safeToDisconnect, isTrue);
      expect(controller.offlineNotice, 'offline.held');

      expect(await controller.resumeOfflineHold(), isTrue);
      expect(controller.offlineHold!.state, 'online');
      expect(controller.offlineHold!.revision, 6);
      expect(controller.offlineHold!.safeToDisconnect, isFalse);
      expect(controller.offlineNotice, 'offline.resumed');
    },
  );

  test(
    'Offline hold stale CAS reconciles to the current runtime state',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      final stale = controller.offlineHold!;
      final external = await api.enterOfflineHold(stale);
      expect(external.revision, 3);

      expect(await controller.enterOfflineHold(), isFalse);
      expect(controller.offlineError, 'revision_conflict (409)');
      expect(controller.offlineHold!.state, 'held');
      expect(controller.offlineHold!.revision, external.revision);
      expect(controller.offlineHold!.safeToDisconnect, isTrue);
    },
  );

  test(
    'rule save remains fenced by the revision the draft started from',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      await controller.refreshNetwork();
      final draftBase = controller.networkData!.rules;

      final external = await api.replaceConnectionRules(
        current: draftBase,
        rules: const [
          ConnectionRule(
            id: 'external-change',
            priority: 100,
            decision: 'deny',
            match: 'exact_host',
            host: 'external.example',
            port: null,
          ),
        ],
        mode: 'deny_unknown',
      );
      expect(external.revision, draftBase.revision + 1);
      await controller.refreshNetwork();
      expect(controller.networkData!.rules.revision, external.revision);

      final saved = await controller.replaceConnectionRules(
        base: draftBase,
        rules: const [
          ConnectionRule(
            id: 'stale-draft',
            priority: 1,
            decision: 'allow',
            match: 'exact_host',
            host: 'stale.example',
            port: null,
          ),
        ],
        mode: 'monitor',
      );

      expect(saved, isFalse);
      expect(controller.networkError, 'revision_conflict (409)');
      expect(controller.networkData!.rules.revision, external.revision);
      expect(controller.networkData!.rules.rules.single.id, 'external-change');
    },
  );

  test('Environment review freezes impact before CAS publish', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);

    await controller.initialize();
    controller.selectEnvironment('work');
    final current = controller.selectedEnvironment!;
    final candidate = EnvironmentDraftInput.fromEnvironment(
      current,
      expectedDraftRevision: 0,
      name: 'Work reviewed',
      contentRecording: const EnvironmentContentRecordingPolicy(
        mode: 'metadata_only',
        retentionDays: 14,
      ),
      policySet: const EnvironmentPolicySet(toolMode: 'review'),
    );

    final impact = await controller.reviewSelectedEnvironment(candidate);
    expect(impact, isNotNull);
    expect(impact!.continuingCaptures, hasLength(6));
    expect(controller.reviewedEnvironmentDraft!.baseRevision, 7);
    expect(controller.reviewedEnvironmentDraft!.draftRevision, 8);
    expect(controller.selectedEnvironment!.name, 'Work');

    final result = await controller.publishReviewedEnvironment();
    expect(result, isNotNull);
    expect(result!.environment.revision, 8);
    expect(result.environment.name, 'Work reviewed');
    expect(result.environment.contentRecording.mode, 'metadata_only');
    expect(result.environment.policySet.toolMode, 'review');
    expect(controller.selectedEnvironment!.revision, 8);
    expect(controller.environmentNotice, 'environment.published');
    expect(controller.reviewedEnvironmentDraft, isNull);
    await expectLater(
      api.environmentDraft('work'),
      throwsA(
        isA<ControlProblem>().having(
          (problem) => problem.reasonCode,
          'reasonCode',
          'environment_draft_not_found',
        ),
      ),
    );
  });

  test('Environment publish rejects a stale base revision', () async {
    final api = PreviewControlApi();
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(controller.dispose);

    await controller.initialize();
    controller.selectEnvironment('work');
    final current = controller.selectedEnvironment!;
    final externalDraft = await api.saveEnvironmentDraft(
      environmentId: 'work',
      expectedBaseRevision: current.revision,
      input: EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: 0,
        name: 'External revision',
      ),
    );
    await api.publishEnvironmentDraft('work', externalDraft.draftRevision);

    final impact = await controller.reviewSelectedEnvironment(
      EnvironmentDraftInput.fromEnvironment(
        current,
        expectedDraftRevision: 0,
        name: 'Stale local revision',
      ),
    );
    expect(impact, isNull);
    expect(controller.environmentError, 'revision_conflict (409)');
    expect(controller.selectedEnvironment!.revision, 7);
  });

  test(
    'frozen Environment evidence loads by exact revision and remains read-only',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      controller.selectEnvironment('work');
      final frozen = controller.selectedEnvironment!;
      final impact = await controller.reviewSelectedEnvironment(
        EnvironmentDraftInput.fromEnvironment(
          frozen,
          expectedDraftRevision: 0,
          name: 'Work current',
        ),
      );
      expect(impact, isNotNull);
      final published = await controller.publishReviewedEnvironment();
      expect(published!.environment.revision, frozen.revision + 1);

      final historical = await controller.inspectEnvironmentRevision(
        frozen.id,
        frozen.revision,
        expectedDigest: frozen.digest,
      );
      expect(historical!.revision, 7);
      expect(controller.section, WorkbenchSection.environments);
      expect(controller.inspectingHistoricalEnvironment, isTrue);
      expect(controller.displayedEnvironment!.revision, 7);
      expect(controller.selectedEnvironment!.revision, 8);
      expect(
        await controller.reviewSelectedEnvironment(
          EnvironmentDraftInput.fromEnvironment(
            controller.selectedEnvironment!,
            expectedDraftRevision: 0,
          ),
        ),
        isNull,
      );

      controller.showCurrentEnvironment();
      expect(controller.inspectingHistoricalEnvironment, isFalse);
      expect(controller.displayedEnvironment!.revision, 8);
    },
  );

  test(
    'frozen Environment digest mismatch is surfaced and never displayed',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      final result = await controller.inspectEnvironmentRevision(
        'work',
        7,
        expectedDigest: List.filled(64, 'f').join(),
      );

      expect(result, isNull);
      expect(controller.historicalEnvironment, isNull);
      expect(
        controller.environmentError,
        'Frozen Environment digest does not match the stored revision',
      );
      expect(controller.selectedEnvironmentRevision, 7);
    },
  );

  test(
    'preview keeps every published Environment revision inspectable',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final initial = await api.environmentRevision('work', 7);
      final draft = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: initial.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          initial,
          expectedDraftRevision: 0,
          name: 'Work next',
        ),
      );
      final published = await api.publishEnvironmentDraft(
        initial.id,
        draft.draftRevision,
      );

      expect((await api.environmentRevision('work', 7)).name, 'Work');
      expect(
        (await api.environmentRevision(
          'work',
          published.environment.revision,
        )).name,
        'Work next',
      );
      await expectLater(
        api.environmentRevision('work', 999),
        throwsA(
          isA<ControlProblem>().having(
            (problem) => problem.reasonCode,
            'reasonCode',
            'environment_revision_not_found',
          ),
        ),
      );
    },
  );

  test(
    'new Environment uses base revision zero and joins the directory only after publish',
    () async {
      final api = PreviewControlApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);

      await controller.initialize();
      const input = EnvironmentDraftInput(
        expectedDraftRevision: 0,
        name: 'Local observe',
        state: 'active',
        clientEndpoints: [],
        pluginBindings: [],
        budgetPolicy: EnvironmentBudgetPolicy(id: '', revision: 0),
        egressPolicy: EnvironmentEgressPolicy(id: '', revision: 0, mode: ''),
        contentRecording: EnvironmentContentRecordingPolicy(
          mode: 'metadata_only',
          retentionDays: 30,
        ),
        policySet: EnvironmentPolicySet(toolMode: 'observe'),
      );

      final impact = await controller.reviewNewEnvironment(
        'local-observe',
        input,
      );
      expect(impact, isNotNull);
      expect(impact!.baseRevision, 0);
      expect(impact.continuingCaptures, isEmpty);
      expect(
        controller.data!.environments.any(
          (value) => value.id == 'local-observe',
        ),
        isFalse,
      );

      final result = await controller.publishReviewedEnvironment();
      expect(result!.environment.revision, 1);
      expect(controller.selectedEnvironmentId, 'local-observe');
      expect(controller.selectedEnvironment!.name, 'Local observe');
    },
  );

  test(
    'Environment draft allocation remains monotonic after publish consumes the draft',
    () async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final initial = (await api.loadDashboard()).environments.firstWhere(
        (environment) => environment.id == 'work',
      );

      final first = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: initial.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          initial,
          expectedDraftRevision: 0,
          name: 'Work allocation one',
        ),
      );
      await api.previewEnvironmentDraft(initial.id, first.draftRevision);
      final published = await api.publishEnvironmentDraft(
        initial.id,
        first.draftRevision,
      );

      final second = await api.saveEnvironmentDraft(
        environmentId: initial.id,
        expectedBaseRevision: published.environment.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          published.environment,
          expectedDraftRevision: 0,
          name: 'Work allocation two',
        ),
      );

      expect(first.draftRevision, 8);
      expect(second.draftRevision, first.draftRevision + 1);
    },
  );
}

final class _SequencedExchangeApi implements ControlApi {
  _SequencedExchangeApi(this._responses);

  final List<Future<ExchangeDetail>> _responses;
  int exchangeCalls = 0;

  @override
  Future<ExchangeDetail> exchange(
    String exchangeId, {
    String contentView = 'incremental',
  }) {
    final index = exchangeCalls++;
    if (index >= _responses.length) {
      throw StateError('Unexpected exchange request $index');
    }
    return _responses[index];
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _ControlledActivityApi implements ControlApi {
  _ControlledActivityApi(this.delegate);

  final PreviewControlApi delegate;
  String? _blockedConversation;
  Completer<void>? _release;
  final activityRequests =
      <
        ({
          String? captureRunId,
          String? manualCaptureId,
          String? conversationId,
        })
      >[];

  void blockConversation(String key) {
    _blockedConversation = key;
    _release = Completer<void>();
  }

  void releaseConversation() {
    _release?.complete();
    _release = null;
    _blockedConversation = null;
  }

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) async {
    activityRequests.add((
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      conversationId: conversationId,
    ));
    final release = conversationId == _blockedConversation ? _release : null;
    if (release != null) await release.future;
    return delegate.activities(
      cursor: cursor,
      limit: limit,
      captureRunId: captureRunId,
      manualCaptureId: manualCaptureId,
      environmentId: environmentId,
      conversationId: conversationId,
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

final class _DeletionTrackingApi implements ControlApi {
  _DeletionTrackingApi(this.delegate);

  final PreviewControlApi delegate;
  int dashboardLoads = 0;

  @override
  Future<DashboardData> loadDashboard() {
    dashboardLoads += 1;
    return delegate.loadDashboard();
  }

  @override
  Future<DeletionOutcome> deleteCapture(String captureKey) =>
      delegate.deleteCapture(captureKey);

  @override
  Future<DeletionOutcome> deleteEnvironment(String environmentId) =>
      delegate.deleteEnvironment(environmentId);

  @override
  Future<CaptureAssignment> captureAssignment(String captureKey) =>
      delegate.captureAssignment(captureKey);

  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );

  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? environmentId,
    String? conversationId,
  }) => delegate.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    environmentId: environmentId,
    conversationId: conversationId,
  );

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

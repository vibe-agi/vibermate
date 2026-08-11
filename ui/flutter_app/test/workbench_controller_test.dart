import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
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
    expect(impact!.classification, 'hot_switch');
    expect(impact.hotSwitchCount, 6);
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
      expect(impact.affected, isEmpty);
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

  test(
    'Workspace default changes future runs without mutating the current Capture',
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

      final capture = controller.selectedCapture!;
      final assignment = controller.selectedAssignment!;
      expect(capture.managedRun!.hasWorkspaceIdentity, isTrue);
      expect(controller.selectedWorkspaceDefault!.environmentId, 'work');

      expect(await controller.setSelectedWorkspaceDefault('research'), isTrue);
      expect(controller.selectedWorkspaceDefault!.environmentId, 'research');
      expect(controller.selectedWorkspaceDefault!.revision, 2);
      expect(controller.workspaceDefaultNotice, 'workspace_default.saved');
      expect(controller.selectedAssignment!.captureKey, assignment.captureKey);
      expect(
        controller.selectedAssignment!.environmentId,
        assignment.environmentId,
      );
      expect(controller.selectedAssignment!.revision, assignment.revision);
      final authoritativeAssignment = await api.captureAssignment(capture.key);
      expect(authoritativeAssignment.environmentId, assignment.environmentId);
      expect(authoritativeAssignment.revision, assignment.revision);

      expect(await controller.setSelectedWorkspaceDefault(null), isTrue);
      expect(controller.selectedWorkspaceDefault, isNull);
      expect(controller.workspaceDefaultNotice, 'workspace_default.cleared');
      expect(
        (await api.captureAssignment(capture.key)).revision,
        assignment.revision,
      );
    },
  );

  test(
    'Workspace default stale CAS reconciles without hiding the conflict',
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

      final managed = controller.selectedCapture!.managedRun!;
      final initial = controller.selectedWorkspaceDefault!;
      final external = await api.setWorkspaceEnvironmentDefault(
        machineId: managed.machineId!,
        workspaceId: managed.workspaceId!,
        expectedRevision: initial.revision,
        environmentId: 'research',
      );

      expect(await controller.setSelectedWorkspaceDefault('research'), isFalse);
      expect(controller.workspaceDefaultError, 'revision_conflict (409)');
      expect(controller.selectedWorkspaceDefault!.revision, external.revision);
      expect(controller.selectedWorkspaceDefault!.environmentId, 'research');
    },
  );
}

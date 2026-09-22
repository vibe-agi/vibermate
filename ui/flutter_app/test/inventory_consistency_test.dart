import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final operation in [
    'link',
    'unlink',
    'replace',
    'refresh',
    'create_account',
    'create_service',
    'delete_account',
    'publish_policy',
  ]) {
    test('a stale dashboard read cannot undo $operation', () async {
      final fixture = PreviewControlApi(seedCaptures: false);
      final account = await fixture.createProviderAccount(
        id: 'account.race',
        displayName: 'Race fixture',
        upstreamEndpointId: 'target.codex.official',
        unlinked: operation != 'unlink',
        kind: operation == 'refresh' ? 'codex_oauth' : 'bearer_token',
        secret: operation == 'refresh' ? '' : 'synthetic-secret',
        codexAuthJson: operation == 'refresh'
            ? jsonEncode({
                'auth_mode': 'chatgpt',
                'tokens': {
                  'account_id': 'synthetic-account',
                  'access_token': 'synthetic-access',
                  'refresh_token': 'synthetic-refresh',
                  'id_token': 'synthetic-id',
                },
                'last_refresh': '2026-09-21T00:00:00Z',
              })
            : '',
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      final api = _DelayedDashboardApi(fixture);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      final before = await fixture.loadDashboard();
      controller.data = before;
      final endpoint = before.endpoints.singleWhere(
        (value) => value.id == 'target.codex.official',
      );
      if (operation == 'publish_policy') {
        controller.selectEnvironment('work');
        final impact = await controller.reviewSelectedEnvironment(
          EnvironmentDraftInput.fromEnvironment(
            controller.selectedEnvironment!,
            expectedDraftRevision: 0,
            name: 'Published while refreshing',
          ),
        );
        expect(impact, isNotNull);
      }
      final pending = Completer<DashboardData>();
      api.pending = pending;
      final staleRead = controller.refresh();
      switch (operation) {
        case 'link':
        case 'unlink':
          expect(
            await controller.setProviderAccountAssociation(
              account: account,
              endpoint: endpoint,
              linked: operation == 'link',
            ),
            isTrue,
          );
          expect(
            controller.data!.accounts
                .singleWhere((value) => value.id == account.id)
                .isLinkedTo(endpoint.id),
            operation == 'link',
          );
        case 'replace':
          expect(
            (await controller.replaceProviderAccountCredential(
              account: account,
              secret: 'synthetic-replacement',
              headerPolicy: const ProviderAccountHeaderPolicy(),
            ))!.credentialEpoch,
            account.credentialEpoch + 1,
          );
        case 'refresh':
          expect(
            (await controller.refreshProviderAccountCredential(
              account,
            ))!.credentialEpoch,
            account.credentialEpoch + 1,
          );
        case 'create_account':
          final created = await controller.createProviderAccount(
            endpoint: endpoint,
            displayName: 'Another account',
            kind: 'bearer_token',
            secret: 'synthetic-new',
            headerPolicy: const ProviderAccountHeaderPolicy(),
          );
          expect(created, isNotNull);
          expect(controller.data!.accounts.length, before.accounts.length + 1);
        case 'create_service':
          final created = await controller.createUpstreamEndpoint(
            displayName: 'Another service',
            origin: 'https://example.invalid',
            backendProtocols: ['openai_responses'],
          );
          expect(created, isNotNull);
          expect(
            controller.data!.endpoints.length,
            before.endpoints.length + 1,
          );
        case 'delete_account':
          expect(
            (await controller.deleteProviderAccount(account))!.deleted,
            isTrue,
          );
          expect(
            controller.data!.accounts.any((a) => a.id == account.id),
            isFalse,
          );
        case 'publish_policy':
          expect(await controller.publishReviewedEnvironment(), isNotNull);
          expect(
            controller.selectedEnvironment!.name,
            'Published while refreshing',
          );
      }
      final saved = controller.data;
      expect(saved, isNot(same(before)));
      pending.complete(before);
      await staleRead;
      expect(controller.data, same(saved));
      expect(controller.loading, isFalse);
      expect(controller.inventoryMutating, isFalse);
    });
  }

  test(
    'link conflicts reconcile without rotating credentials or losing notes',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      var account = await api.createProviderAccount(
        id: 'account.conflict',
        displayName: 'Conflict fixture',
        upstreamEndpointId: 'target.codex.official',
        unlinked: true,
        kind: 'bearer_token',
        secret: 'synthetic-secret',
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      account = await api.setProviderAccountNote(account, 'Keep this note');
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.refresh();
      final endpoint = controller.data!.endpoints.singleWhere(
        (value) => value.id == 'target.codex.official',
      );
      await api.setProviderAccountAssociation(
        account: account,
        endpoint: endpoint,
        linked: true,
      );
      expect(
        await controller.setProviderAccountAssociation(
          account: account,
          endpoint: endpoint,
          linked: false,
        ),
        isFalse,
      );
      expect(controller.inventoryError, 'error.account_conflict');
      ProviderAccount current() => controller.data!.accounts.singleWhere(
        (value) => value.id == account.id,
      );
      expect(current().isLinkedTo(endpoint.id), isTrue);
      // The dialog may still hold its original object; retries use reconciled state.
      expect(
        await controller.setProviderAccountAssociation(
          account: account,
          endpoint: endpoint,
          linked: false,
        ),
        isTrue,
      );
      expect(current().isLinkedTo(endpoint.id), isFalse);
      expect(current().credentialEpoch, account.credentialEpoch);
      expect(current().revision, account.revision);
      expect(current().note, 'Keep this note');
      expect(current().noteRevision, account.noteRevision);
    },
  );

  test(
    'an open policy editor cannot adopt a newly polled base revision',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.refresh();
      controller.selectEnvironment('work');
      final editorBase = controller.selectedEnvironment!;
      final external = await api.saveEnvironmentDraft(
        environmentId: editorBase.id,
        expectedBaseRevision: editorBase.revision,
        input: EnvironmentDraftInput.fromEnvironment(
          editorBase,
          expectedDraftRevision: 0,
          name: 'Another published edit',
        ),
      );
      await api.publishEnvironmentDraft(editorBase.id, external.draftRevision);
      await controller.refresh();
      expect(controller.selectedEnvironment!.revision, editorBase.revision + 1);
      final impact = await controller.reviewSelectedEnvironment(
        EnvironmentDraftInput.fromEnvironment(
          editorBase,
          expectedDraftRevision: 0,
          name: 'My stale edit',
        ),
        baseEnvironment: editorBase,
      );
      expect(impact, isNull);
      expect(controller.environmentError, 'error.configuration_conflict');
      expect(controller.reviewedEnvironmentDraft, isNull);
      expect(controller.selectedEnvironment!.name, 'Another published edit');
    },
  );
}

final class _DelayedDashboardApi implements ControlApi {
  _DelayedDashboardApi(this.fixture);
  final PreviewControlApi fixture;
  Completer<DashboardData>? pending;

  @override
  Future<DashboardData> loadDashboard() {
    final read = pending;
    pending = null;
    return read?.future ?? fixture.loadDashboard();
  }

  @override
  Future<List<ApprovalRecord>> pendingApprovals() => fixture.pendingApprovals();

  @override
  Future<ProviderAccount> setProviderAccountAssociation({
    required ProviderAccount account,
    required UpstreamEndpoint endpoint,
    required bool linked,
  }) => fixture.setProviderAccountAssociation(
    account: account,
    endpoint: endpoint,
    linked: linked,
  );

  @override
  Future<ProviderAccount> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
    String codexAuthJson = '',
    required ProviderAccountHeaderPolicy headerPolicy,
  }) => fixture.replaceProviderAccountCredential(
    account: account,
    secret: secret,
    codexAuthJson: codexAuthJson,
    headerPolicy: headerPolicy,
  );

  @override
  Future<ProviderAccount> refreshProviderAccountCredential(
    ProviderAccount account,
  ) => fixture.refreshProviderAccountCredential(account);

  @override
  Future<ProviderAccount> createProviderAccount({
    required String id,
    required String displayName,
    required String upstreamEndpointId,
    bool unlinked = false,
    required String kind,
    required String secret,
    String codexAuthJson = '',
    required ProviderAccountHeaderPolicy headerPolicy,
  }) => fixture.createProviderAccount(
    id: id,
    displayName: displayName,
    upstreamEndpointId: upstreamEndpointId,
    unlinked: unlinked,
    kind: kind,
    secret: secret,
    codexAuthJson: codexAuthJson,
    headerPolicy: headerPolicy,
  );

  @override
  Future<UpstreamEndpoint> createUpstreamEndpoint({
    required String id,
    required String displayName,
    required String origin,
    required List<String> backendProtocols,
  }) => fixture.createUpstreamEndpoint(
    id: id,
    displayName: displayName,
    origin: origin,
    backendProtocols: backendProtocols,
  );

  @override
  Future<ProviderAccountDeleteResult> deleteProviderAccount(
    ProviderAccount account,
  ) => fixture.deleteProviderAccount(account);

  @override
  Future<EnvironmentDraft> environmentDraft(String id) =>
      fixture.environmentDraft(id);

  @override
  Future<EnvironmentDraft> saveEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required EnvironmentDraftInput input,
  }) => fixture.saveEnvironmentDraft(
    environmentId: environmentId,
    expectedBaseRevision: expectedBaseRevision,
    input: input,
  );

  @override
  Future<EnvironmentImpact> previewEnvironmentDraft(String id, int revision) =>
      fixture.previewEnvironmentDraft(id, revision);

  @override
  Future<EnvironmentPublishResult> publishEnvironmentDraft(
    String id,
    int revision,
  ) => fixture.publishEnvironmentDraft(id, revision);

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

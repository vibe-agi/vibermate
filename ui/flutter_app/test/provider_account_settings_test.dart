import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test(
    'imports start off, login starts on, manual refresh preserves settings',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      addTearDown(api.close);
      final imported = await _import(api);
      expect(imported.automaticRefresh, isFalse);
      expect(imported.supportsAutomaticRefresh, isTrue);
      final configured = await api.setProviderAccountSettings(
        imported,
        egressProfile: EgressProfileRevision.direct,
        automaticRefresh: false,
      );
      final refreshed = await api.refreshProviderAccountCredential(configured);
      expect(refreshed.automaticRefresh, isFalse);
      expect(refreshed.settingsRevision, configured.settingsRevision);
      expect(refreshed.egressProfile, EgressProfileRevision.direct);
      expect(refreshed.credentialEpoch, configured.credentialEpoch + 1);
      expect(
        refreshed.withNote('local note', 1).egressProfile,
        EgressProfileRevision.direct,
      );
      expect(
        refreshed.withAssociations([], 2).settingsRevision,
        refreshed.settingsRevision,
      );

      final login = await api.startCodexLogin(
        accountId: 'account.login',
        upstreamEndpointId: 'target.codex.official',
        displayName: 'Login',
        callbackMode: 'manual',
      );
      await api.completeCodexLogin(
        login.id,
        'http://localhost:1455/auth/callback?state=${login.id}&code=synthetic',
      );
      final loggedIn = (await api.loadDashboard()).accounts.singleWhere(
        (a) => a.id == 'account.login',
      );
      expect(loggedIn.automaticRefresh, isTrue);
      expect(loggedIn.settingsRevision, 1);
    },
  );

  test(
    'old quota cannot overwrite a new settings scope or invalidated read',
    () async {
      final preview = PreviewControlApi(seedCaptures: false);
      final account = await _import(preview);
      final api = _DelayedFactsApi(preview);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: preview.close,
      );
      addTearDown(controller.dispose);
      controller.data = await preview.loadDashboard();

      final first = controller.refreshProviderAccountQuota(account);
      final updated = (await controller.setProviderAccountSettings(
        account,
        egressProfile: EgressProfileRevision.direct,
        automaticRefresh: true,
      ))!;
      final second = controller.refreshProviderAccountQuota(updated);
      expect(api.pending.length, 2);
      final facts = await preview.accountFacts(account.id);
      api.pending[0].complete(facts);
      expect(await first, isNull);
      expect(controller.providerAccountQuota(updated), isNull);
      expect(controller.providerAccountQuotaLoading(updated), isTrue);
      api.pending[1].complete(facts);
      expect(await second, same(facts));
      expect(controller.providerAccountQuota(updated), same(facts));
      expect(controller.providerAccountQuota(account), isNull);

      final beforeReset = controller.refreshProviderAccountQuota(updated);
      controller.invalidateProviderAccountQuota(updated);
      final afterReset = controller.refreshProviderAccountQuota(updated);
      api.pending[2].completeError(StateError('obsolete read'));
      expect(await beforeReset, isNull);
      expect(controller.providerAccountQuotaFailed(updated), isFalse);
      expect(controller.providerAccountQuotaLoading(updated), isTrue);
      api.pending[3].complete(facts);
      expect(await afterReset, same(facts));

      // A competing editor wins; our stale revision neither resets the proxy nor
      // leaves an optimistic switch on screen. Reconciliation shows the winner.
      final competing = await preview.setProviderAccountSettings(
        updated,
        egressProfile: null,
        automaticRefresh: false,
      );
      expect(
        await controller.setProviderAccountSettings(
          updated,
          egressProfile: EgressProfileRevision.direct,
          automaticRefresh: false,
        ),
        isNull,
      );
      expect(controller.inventoryError, 'provider_accounts.settings.conflict');
      final current = controller.data!.accounts.singleWhere(
        (a) => a.id == account.id,
      );
      expect(current.settingsRevision, competing.settingsRevision);
      expect(current.egressProfile, isNull);
      expect(current.automaticRefresh, isFalse);
      expect(controller.providerAccountQuota(current), isNull);
    },
  );
}

Future<ProviderAccount> _import(PreviewControlApi api) =>
    api.createProviderAccount(
      id: 'account.import',
      displayName: 'Imported OAuth',
      upstreamEndpointId: 'target.codex.official',
      kind: 'codex_oauth',
      secret: '',
      codexAuthJson: jsonEncode({
        'auth_mode': 'chatgpt',
        'tokens': {
          'account_id': 'workspace-fixture',
          'access_token': 'synthetic',
          'id_token': 'synthetic',
          'refresh_token': 'synthetic',
        },
        'last_refresh': '2026-09-28T00:00:00Z',
      }),
      headerPolicy: const ProviderAccountHeaderPolicy(),
    );

final class _DelayedFactsApi extends Fake implements ControlApi {
  _DelayedFactsApi(this.preview);
  final PreviewControlApi preview;
  final pending = <Completer<AccountFacts>>[];
  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) {
    final result = Completer<AccountFacts>();
    pending.add(result);
    return result.future;
  }

  @override
  Future<DashboardData> loadDashboard() => preview.loadDashboard();
  @override
  Future<ProviderAccount> setProviderAccountSettings(
    ProviderAccount account, {
    required EgressProfileRevision? egressProfile,
    required bool automaticRefresh,
  }) => preview.setProviderAccountSettings(
    account,
    egressProfile: egressProfile,
    automaticRefresh: automaticRefresh,
  );
}

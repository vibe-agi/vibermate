import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/egress_profile_editor.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final canceled in [false, true]) {
    testWidgets(
      canceled
          ? 'Cancel account picker preserves the nullable override'
          : 'A late account picker cannot write its old selection to a new Runtime',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final oldApi = PreviewControlApi(seedCaptures: false),
            newApi = PreviewControlApi(seedCaptures: false);
        addTearDown(oldApi.close);
        addTearDown(newApi.close);
        Future<WorkbenchController> controller(PreviewControlApi api) async {
          final result = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: true,
            closeRuntime: api.close,
          );
          addTearDown(result.dispose);
          await result.refresh();
          return result;
        }

        final oldController = await controller(oldApi),
            newController = await controller(newApi);
        final account = oldController.data!.accounts.first;
        final before = newController.data!.accounts.firstWhere(
          (a) => a.id == account.id,
        );
        var current = oldController;
        late StateSetter replace;
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: StatefulBuilder(
                builder: (context, setState) {
                  replace = setState;
                  return ProviderAccountsView(
                    controller: current,
                    copy: AppCopy.forLanguage(AppLanguage.english),
                  );
                },
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        await tester.enterText(
          find.byKey(const Key('provider-accounts-search')),
          account.displayName,
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(Key('provider-account-details-toggle-${account.id}')),
        );
        await tester.pumpAndSettle();
        final exit = find.byKey(Key('account-egress-${account.id}'));
        await tester.ensureVisible(exit);
        await tester.tap(exit);
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-profile.direct-1')),
        );
        if (!canceled) {
          replace(() => current = newController);
        }
        await tester.pumpAndSettle();
        await tester.tap(
          canceled
              ? find.text('Cancel').last
              : find.byKey(const Key('environment-egress-profile-save')),
        );
        await tester.pumpAndSettle();
        final after = (await (canceled ? oldApi : newApi).loadDashboard())
            .accounts
            .firstWhere((a) => a.id == account.id);
        expect(after.settingsRevision, before.settingsRevision);
        expect(after.egressProfile, before.egressProfile);
        expect(tester.takeException(), isNull);
      },
    );
  }

  for (final scopeChanged in [true, false]) {
    testWidgets(
      'Flow dialog ${scopeChanged ? 'rejects changed scope' : 'survives same scope rebuild'}',
      (tester) async {
        final api = PreviewControlApi();
        addTearDown(api.close);
        final plan = (await api.loadDashboard()).environments
            .firstWhere((e) => !e.systemOwned)
            .clientEndpoints
            .first
            .protocolPlans
            .first;
        var oldWrites = 0, newWrites = 0, replaced = false;
        final originalScope = Object(), newScope = Object();
        late StateSetter rebuild;
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: StatefulBuilder(
                builder: (context, setState) {
                  rebuild = setState;
                  return EgressProfileButton(
                    plan: plan,
                    copy: AppCopy.forLanguage(AppLanguage.english),
                    enabled: true,
                    loadProfiles: api.egressProfiles,
                    selectionScope: replaced && scopeChanged
                        ? newScope
                        : originalScope,
                    onChanged: replaced
                        ? (_) => newWrites++
                        : (_) => oldWrites++,
                  );
                },
              ),
            ),
          ),
        );
        await tester.tap(find.byKey(Key('environment-egress-${plan.id}')));
        await tester.pumpAndSettle();
        rebuild(() => replaced = true);
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-save')),
        );
        await tester.pumpAndSettle();
        expect(oldWrites, 0);
        expect(newWrites, scopeChanged ? 0 : 1);
        expect(tester.takeException(), isNull);
      },
    );
  }
}

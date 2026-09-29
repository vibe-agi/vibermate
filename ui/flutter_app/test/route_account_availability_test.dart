import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/route_account_scope_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('390px account scope search is reversible until save', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi(seedCaptures: false);
    addTearDown(api.close);
    final data = await api.loadDashboard();
    final route = data.environments
        .firstWhere((value) => value.id == 'work')
        .routes
        .firstWhere((value) => value.id == 'anthropic-direct');
    RouteAccountPolicy? saved;
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: Scaffold(
          body: RouteAccountScopeButton(
            policy: route.accountPolicy,
            accounts: data.accounts
                .where((account) => account.isLinkedTo(route.endpointId))
                .toList(),
            copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
            onChanged: (value) => saved = value,
          ),
        ),
      ),
    );
    await tester.tap(find.byType(OutlinedButton));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('route-account-scope-search')),
      'lab',
    );
    await tester.pump();
    final alternative = find.byKey(
      const Key('route-account-scope-anthropic-lab'),
    );
    await tester.tap(alternative);
    await tester.pump();
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(saved, isNull);
    await tester.tap(find.byType(OutlinedButton));
    await tester.pumpAndSettle();
    expect(tester.widget<CheckboxListTile>(alternative).value, isFalse);
    await tester.tap(alternative);
    await tester.pump();
    await tester.tap(find.byKey(const Key('route-account-scope-apply')));
    await tester.pumpAndSettle();
    expect(saved!.fixedAccountId, 'anthropic-work');
    expect(saved!.accounts.map((account) => account.id), [
      'anthropic-lab',
      'anthropic-work',
    ]);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'an unavailable fixed account can be explicitly replaced in scope',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 850));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(seedCaptures: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
      );
      addTearDown(controller.dispose);
      await controller.refresh();
      final original = controller.data!;
      controller.data = DashboardData(
        status: original.status,
        captures: original.captures,
        captureNextCursor: original.captureNextCursor,
        environments: original.environments,
        endpoints: original.endpoints,
        accounts: [
          for (final account in original.accounts)
            if (account.id == 'anthropic-work')
              _unavailable(account, 'disabled')
            else
              account,
        ],
      );
      controller.selectEnvironment('work');
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: EnvironmentsView(
              controller: controller,
              copy: AppCopy.forLanguage(AppLanguage.english),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final scope = find.descendant(
        of: find.byKey(
          const Key('environment-route-accounts-anthropic-direct'),
        ),
        matching: find.byType(OutlinedButton),
      );
      await tester.ensureVisible(scope);
      await tester.tap(scope);
      await tester.pumpAndSettle();
      final apply = find.byKey(const Key('route-account-scope-apply'));
      expect(tester.widget<FilledButton>(apply).onPressed, isNull);
      await tester.tap(
        find.byKey(const Key('route-account-scope-anthropic-lab')),
      );
      await tester.pump();
      final replacement = find.byKey(
        const Key('route-account-scope-replacement'),
      );
      await tester.tap(replacement);
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(MenuItemButton, 'Anthropic · Lab'));
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('route-account-scope-anthropic-work')),
      );
      await tester.pump();
      await tester.tap(apply);
      await tester.pumpAndSettle();
      expect(
        find.byKey(
          const Key(
            'environment-route-account-anthropic-direct-fixed:anthropic-lab',
          ),
        ),
        findsOneWidget,
      );
      await tester.ensureVisible(find.byKey(const Key('environment-review')));
      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-publish')), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  for (final state in ['unlinked', 'credential_unavailable', 'disabled']) {
    testWidgets('a fixed route retains its account and recovers after $state', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1180, 850));
      addTearDown(() => tester.binding.setSurfaceSize(null));
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
      final original = controller.data!;
      final account = original.accounts.singleWhere(
        (value) => value.id == 'anthropic-work',
      );
      final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: AnimatedBuilder(
              animation: controller,
              builder: (context, _) =>
                  EnvironmentsView(controller: controller, copy: copy),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final activation = find.byKey(
        const Key('environment-account-group-anthropic-direct'),
      );
      expect(
        find.descendant(of: activation, matching: find.text('Anthropic · Lab')),
        findsNothing,
      );
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final field = find.byKey(
        const Key(
          'environment-route-account-anthropic-direct-fixed:anthropic-work',
        ),
      );
      await tester.ensureVisible(field);
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: field,
          matching: find.textContaining(account.displayName),
        ),
        findsOneWidget,
      );

      controller.data = DashboardData(
        status: original.status,
        captures: original.captures,
        captureNextCursor: original.captureNextCursor,
        environments: original.environments,
        endpoints: original.endpoints,
        accounts: state == 'unlinked'
            ? const []
            : [_unavailable(account, state)],
      );
      controller.selectEnvironment('work');
      await tester.pumpAndSettle();
      final unavailable = tester.widget<CompactSelectField<String>>(field);
      expect(unavailable.initialValue, 'fixed:anthropic-work');
      expect(unavailable.onChanged, isNull);
      expect(
        find.descendant(
          of: field,
          matching: find.textContaining(account.displayName),
        ),
        findsOneWidget,
      );
      final explanation = switch (state) {
        'unlinked' => copy('environment.account.selection_lost'),
        'disabled' => copy('environment.account.disabled'),
        _ => copy('routes.credentials.unavailable'),
      };
      expect(
        find.descendant(of: field, matching: find.textContaining(explanation)),
        findsOneWidget,
      );

      await controller.refresh();
      await tester.pumpAndSettle();
      final recovered = tester.widget<CompactSelectField<String>>(field);
      expect(recovered.initialValue, 'fixed:anthropic-work');
      expect(recovered.onChanged, isNotNull);
      expect(
        recovered.items
            .singleWhere((item) => item.value == 'fixed:anthropic-work')
            .enabled,
        isTrue,
      );
      expect(
        find.descendant(of: field, matching: find.textContaining(explanation)),
        findsNothing,
      );
      final alternative = original.accounts.singleWhere(
        (value) => value.id == 'anthropic-lab',
      );
      expect(
        recovered.items.any((item) => item.value == 'fixed:${alternative.id}'),
        isFalse,
      );
      final scope = find.byKey(
        const Key('environment-route-accounts-anthropic-direct'),
      );
      await tester.ensureVisible(scope);
      await tester.tap(
        find.descendant(of: scope, matching: find.byType(OutlinedButton)),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('route-account-scope-search')),
        alternative.displayName,
      );
      await tester.pump();
      await tester.tap(
        find.byKey(Key('route-account-scope-${alternative.id}')),
      );
      await tester.pump();
      await tester.tap(find.byKey(const Key('route-account-scope-apply')));
      await tester.pumpAndSettle();
      await tester.tap(field);
      await tester.pumpAndSettle();
      await tester.tap(
        find
            .text(
              '${copy('environment.account.fixed')} · ${alternative.displayName}',
            )
            .last,
      );
      await tester.pumpAndSettle();
      expect(
        find.byKey(
          const Key(
            'environment-route-account-anthropic-direct-fixed:anthropic-lab',
          ),
        ),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    });
  }
}

ProviderAccount _unavailable(ProviderAccount account, String state) =>
    ProviderAccount(
      id: account.id,
      displayName: account.displayName,
      credentialOrigin: account.credentialOrigin,
      linkedEndpointIds: account.linkedEndpointIds,
      kind: account.kind,
      realmId: account.realmId,
      state: state == 'disabled' ? 'disabled' : 'active',
      revision: account.revision,
      credentialState: state == 'disabled'
          ? 'disabled'
          : 'credential_unavailable',
      credentialEpoch: state == 'disabled' ? 0 : account.credentialEpoch,
      setHeaderNames: account.setHeaderNames,
      deleteHeaderNames: account.deleteHeaderNames,
    );

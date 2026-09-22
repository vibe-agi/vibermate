import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
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
      await tester.tap(field);
      await tester.pumpAndSettle();
      final alternative = original.accounts.singleWhere(
        (value) => value.id == 'anthropic-lab',
      );
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

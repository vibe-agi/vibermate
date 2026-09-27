@TestOn('vm')
library;

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final width in [390.0, 1180.0]) {
    testWidgets(
      'quota refresh stays safe while OAuth refresh is explained at $width',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(width, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(seedCaptures: false);
        for (final oauth in [true, false]) {
          await api.createProviderAccount(
            id: oauth ? 'account.oauth' : 'account.bearer',
            displayName: oauth ? 'Managed OAuth' : 'Manual bearer',
            upstreamEndpointId: 'target.codex.official',
            kind: oauth ? 'codex_oauth' : 'bearer_token',
            secret: oauth ? '' : 'synthetic-bearer',
            codexAuthJson: oauth
                ? jsonEncode({
                    'auth_mode': 'chatgpt',
                    'tokens': {
                      'account_id': 'workspace',
                      'access_token': 'synthetic-access',
                      'refresh_token': 'synthetic-refresh',
                      'id_token': 'synthetic-id',
                    },
                    'last_refresh': '2026-09-21T00:00:00Z',
                  })
                : '',
            unlinked: true,
            headerPolicy: const ProviderAccountHeaderPolicy(),
          );
        }
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        await controller.refresh();
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: ListenableBuilder(
                listenable: controller,
                builder: (context, _) => ProviderAccountsView(
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('account-credential-refresh-account.oauth')),
          findsNothing,
        );
        final quotaRefresh = find.byKey(
          const Key('account-quota-refresh-account.oauth'),
        );
        await tester.ensureVisible(quotaRefresh);
        await tester.pumpAndSettle();
        await tester.tap(quotaRefresh);
        await tester.pumpAndSettle();
        expect(
          controller.data!.accounts
              .singleWhere((a) => a.id == 'account.oauth')
              .credentialEpoch,
          1,
        );
        expect(controller.inventoryNotice, isNull);

        final details = find.byKey(
          const Key('provider-account-details-toggle-account.oauth'),
        );
        await tester.ensureVisible(details);
        await tester.tap(details);
        await tester.pumpAndSettle();
        expect(find.textContaining('通常无需操作'), findsOneWidget);
        expect(
          find.byKey(const Key('account-credential-refresh-account.bearer')),
          findsNothing,
        );
        final credentialRefresh = find.byKey(
          const Key('account-credential-refresh-account.oauth'),
        );
        final accountList = find.descendant(
          of: find.byKey(const Key('provider-accounts-list')),
          matching: find.byType(Scrollable),
        );
        await tester.scrollUntilVisible(
          credentialRefresh,
          200,
          scrollable: accountList,
        );
        if (width < 600) {
          await tester.drag(accountList, const Offset(0, -80));
          await tester.pumpAndSettle();
        }
        await tester.tap(credentialRefresh);
        await tester.pumpAndSettle();
        expect(
          controller.data!.accounts
              .singleWhere((a) => a.id == 'account.oauth')
              .credentialEpoch,
          2,
        );
        expect(controller.inventoryNotice, 'credential_refreshed');
        expect(find.text('OAuth 凭据已刷新，账号资料已更新。'), findsOneWidget);
        expect(find.textContaining('synthetic-refresh'), findsNothing);

        await tester.ensureVisible(
          find.byKey(const Key('provider-accounts-refresh-all')),
        );
        await tester.tap(
          find.byKey(const Key('provider-accounts-refresh-all')),
        );
        await tester.pumpAndSettle();
        expect(
          controller.data!.accounts
              .singleWhere((a) => a.id == 'account.oauth')
              .credentialEpoch,
          2,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }
}

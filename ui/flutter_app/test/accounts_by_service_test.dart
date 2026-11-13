import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final size in const [Size(1440, 900), Size(390, 844)]) {
    testWidgets('accounts are grouped by service at ${size.width}px', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(size);
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(seedCaptures: false);
      await api.createProviderAccount(
        id: 'account.loose',
        displayName: 'Loose key',
        upstreamEndpointId: 'target.codex.official',
        kind: 'bearer_token',
        secret: 'synthetic-loose-secret',
        headerPolicy: const ProviderAccountHeaderPolicy(),
        unlinked: true,
      );
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.providerAccounts,
          language: AppLanguage.english,
        ),
      );
      addTearDown(controller.dispose);
      await controller.refresh();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();

      final data = controller.data!;
      final list = find.descendant(
        of: find.byKey(const Key('provider-accounts-list')),
        matching: find.byType(Scrollable),
      );
      // Every service that has an account gets a heading, followed by its
      // accounts; an account no service uses is listed under its own heading.
      for (final endpoint in data.endpoints) {
        if (!data.accounts.any((a) => a.isLinkedTo(endpoint.id))) continue;
        final heading = find.byKey(
          Key('provider-accounts-group-${endpoint.id}'),
        );
        await tester.scrollUntilVisible(heading, 200, scrollable: list);
        expect(heading, findsOneWidget, reason: endpoint.id);
      }
      final unlinked = find.byKey(
        const Key('provider-accounts-group-unlinked'),
      );
      await tester.scrollUntilVisible(unlinked, 200, scrollable: list);
      expect(unlinked, findsOneWidget);
      expect(find.text('Not linked to a service'), findsOneWidget);
      final loose = find.byKey(const Key('provider-account-account.loose'));
      await tester.scrollUntilVisible(loose, 200, scrollable: list);
      expect(
        tester.getTopLeft(loose).dy,
        greaterThan(tester.getTopLeft(unlinked).dy),
      );
      // Each account is listed once even when it is linked to several
      // services: rows share keys, so a duplicate would fail the build.
      expect(tester.takeException(), isNull);

      // A heading opens that service's settings, and there is a way back.
      final endpoint = data.endpoints.firstWhere(
        (value) => data.accounts.any((a) => a.isLinkedTo(value.id)),
      );
      final settings = find.byKey(
        Key('provider-accounts-service-settings-${endpoint.id}'),
      );
      await tester.scrollUntilVisible(settings, -200, scrollable: list);
      await tester.ensureVisible(settings);
      await tester.pumpAndSettle();
      await tester.tap(settings);
      await tester.pumpAndSettle();
      expect(controller.section, WorkbenchSection.routes);
      expect(controller.selectedEndpointId, endpoint.id);
      await tester.tap(find.byKey(const Key('endpoints-back-to-accounts')));
      await tester.pumpAndSettle();
      expect(controller.section, WorkbenchSection.providerAccounts);
      expect(tester.takeException(), isNull);
    });
  }
}

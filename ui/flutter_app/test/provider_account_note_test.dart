import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  WidgetController.hitTestWarningShouldBeFatal = true;
  test('a stale dashboard read cannot replace a newly saved note', () async {
    final fixture = PreviewControlApi(seedCaptures: false);
    final account = await fixture.createProviderAccount(
      id: 'account.notes',
      displayName: 'Notes fixture',
      upstreamEndpointId: 'target.codex.official',
      kind: 'bearer_token',
      secret: 'synthetic-note-secret',
      headerPolicy: const ProviderAccountHeaderPolicy(),
      unlinked: true,
    );
    final api = _DelayedNoteDashboardApi(fixture);
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: fixture.close,
    );
    addTearDown(controller.dispose);
    final before = await fixture.loadDashboard();
    controller.data = before;
    api.pending = Completer<DashboardData>();
    final staleRead = controller.refresh();
    final saved = await controller.setProviderAccountNote(account, 'new note');
    expect(saved?.note, 'new note');
    api.pending!.complete(before);
    await staleRead;
    expect(
      controller.data!.accounts.singleWhere((a) => a.id == account.id).note,
      'new note',
    );
  });
  for (final width in [390.0, 1280.0]) {
    for (final chinese in [false, true]) {
      testWidgets(
        'Quick account notes save, search, cancel, and clear ($width, $chinese)',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 800));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = PreviewControlApi(seedCaptures: false);
          final account = await api.createProviderAccount(
            id: 'account.notes',
            displayName: 'Notes fixture',
            upstreamEndpointId: 'target.codex.official',
            kind: 'bearer_token',
            secret: 'synthetic-note-secret',
            headerPolicy: const ProviderAccountHeaderPolicy(),
            unlinked: true,
          );
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: true,
            closeRuntime: api.close,
            initialPreferences: WorkbenchPreferences(
              section: WorkbenchSection.providerAccounts,
              language: chinese
                  ? AppLanguage.simplifiedChinese
                  : AppLanguage.english,
            ),
          );
          addTearDown(controller.dispose);
          await controller.refresh();
          await tester.pumpWidget(
            MaterialApp(
              theme: chinese ? ViberTheme.dark() : ViberTheme.light(),
              home: WorkbenchShell(controller: controller),
            ),
          );
          await tester.pumpAndSettle();
          await tester.enterText(
            find.byKey(const Key('provider-accounts-search')),
            'Notes fixture',
          );
          await tester.pumpAndSettle();
          Future<void> edit() async {
            await tester.tap(
              find.byKey(const Key('account-note-account.notes')),
            );
            await tester.pumpAndSettle();
            expect(
              find.byKey(const Key('account-note-dialog')),
              findsOneWidget,
            );
          }

          ProviderAccount current() => controller.data!.accounts.singleWhere(
            (value) => value.id == account.id,
          );
          await edit();
          expect(
            tester
                .widget<FilledButton>(
                  find.byKey(const Key('account-note-save')),
                )
                .onPressed,
            isNull,
          );
          await tester.enterText(
            find.byKey(const Key('account-note-input')),
            '研发项目 · 测试专用',
          );
          await tester.testTextInput.receiveAction(TextInputAction.done);
          await tester.pumpAndSettle();
          expect(find.byKey(const Key('account-note-dialog')), findsNothing);
          expect(current().note, '研发项目 · 测试专用');
          expect(current().noteRevision, 1);
          expect(current().credentialEpoch, account.credentialEpoch);
          expect(current().revision, account.revision);
          expect(current().associationRevision, account.associationRevision);
          expect(find.text('synthetic-note-secret'), findsNothing);
          await tester.enterText(
            find.byKey(const Key('provider-accounts-search')),
            '研发项目',
          );
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('provider-account-account.notes')),
            findsOneWidget,
          );
          await edit();
          expect(
            tester
                .widget<TextField>(find.byKey(const Key('account-note-input')))
                .controller!
                .text,
            current().note,
          );
          await tester.enterText(
            find.byKey(const Key('account-note-input')),
            'discard',
          );
          await tester.tap(find.text(chinese ? '取消' : 'Cancel'));
          await tester.pumpAndSettle();
          expect(current().note, '研发项目 · 测试专用');
          await edit();
          await tester.enterText(
            find.byKey(const Key('account-note-input')),
            '',
          );
          await tester.pump();
          await tester.tap(find.byKey(const Key('account-note-save')));
          await tester.pumpAndSettle();
          expect(current().note, isEmpty);
          expect(current().noteRevision, 2);
          expect(
            find.byKey(const Key('provider-account-account.notes')),
            findsNothing,
          );
          expect(tester.takeException(), isNull);
        },
      );
    }
  }

  testWidgets(
    'A conflicting edit keeps the draft and reloads the account without overwriting it',
    (tester) async {
      final api = PreviewControlApi(seedCaptures: false);
      final account = await api.createProviderAccount(
        id: 'account.notes',
        displayName: 'Notes fixture',
        upstreamEndpointId: 'target.codex.official',
        kind: 'bearer_token',
        secret: 'synthetic-note-secret',
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
      await tester.enterText(
        find.byKey(const Key('provider-accounts-search')),
        'Notes fixture',
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('account-note-account.notes')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('account-note-input')),
        'my draft',
      );
      await api.setProviderAccountNote(account, 'another saved note');
      await tester.pump();
      await tester.tap(find.byKey(const Key('account-note-save')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('account-note-dialog')), findsOneWidget);
      expect(
        tester
            .widget<TextField>(find.byKey(const Key('account-note-input')))
            .controller!
            .text,
        'my draft',
      );
      expect(
        controller.data!.accounts
            .singleWhere((value) => value.id == account.id)
            .note,
        'another saved note',
      );
      expect(find.textContaining('This account changed'), findsWidgets);
      expect(tester.takeException(), isNull);
    },
  );

  test(
    'Notes stay independent across credential replacement and linking',
    () async {
      final api = PreviewControlApi(seedCaptures: false);
      addTearDown(api.close);
      final account = await api.createProviderAccount(
        id: 'account.notes',
        displayName: 'Notes fixture',
        upstreamEndpointId: 'target.codex.official',
        kind: 'bearer_token',
        secret: 'synthetic-note-secret',
        headerPolicy: const ProviderAccountHeaderPolicy(),
        unlinked: true,
      );
      final noted = await api.setProviderAccountNote(account, 'keep me');
      final rotated = await api.replaceProviderAccountCredential(
        account: noted,
        secret: 'synthetic-replacement',
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      expect(rotated.note, 'keep me');
      expect(rotated.noteRevision, 1);
      expect(
        rotated.withAssociations(['target.codex.official'], 2).note,
        'keep me',
      );
      expect(validProviderAccountNote('备' * 256), isTrue);
      expect(validProviderAccountNote('备' * 257), isFalse);
      expect(validProviderAccountNote('bad\nline'), isFalse);
    },
  );
}

final class _DelayedNoteDashboardApi implements ControlApi {
  _DelayedNoteDashboardApi(this.fixture);
  final PreviewControlApi fixture;
  Completer<DashboardData>? pending;

  @override
  Future<DashboardData> loadDashboard() =>
      pending?.future ?? fixture.loadDashboard();

  @override
  Future<ProviderAccount> setProviderAccountNote(
    ProviderAccount account,
    String note,
  ) => fixture.setProviderAccountNote(account, note);

  @override
  Future<List<ApprovalRecord>> pendingApprovals() => fixture.pendingApprovals();

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

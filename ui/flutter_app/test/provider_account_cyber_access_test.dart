import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/control_failure_notice.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('support refresh joins initial discovery and stops on failure', (
    tester,
  ) async {
    final api = _CyberApi()..pending = Completer<UpstreamModelCatalog>();
    await _mount(tester, api);
    await _expand(tester);
    final refresh = find.byKey(const Key('account-cyber-refresh-account.blue'));
    final spinner = find.descendant(
      of: refresh,
      matching: find.byType(CircularProgressIndicator),
    );
    expect(spinner, findsNothing);
    expect(tester.widget<TextButton>(refresh).onPressed, isNotNull);
    await tester.tap(refresh);
    await tester.pump();
    expect(spinner, findsOneWidget);
    await tester.tap(refresh);
    await tester.pump(const Duration(seconds: 1));
    expect(spinner, findsOneWidget);
    expect(api.requests, [('target.codex.official', 'account.blue', false)]);
    api.pending!.completeError(StateError('synthetic discovery failure'));
    api.pending = null;
    await tester.pumpAndSettle();
    expect(spinner, findsNothing);
    expect(tester.widget<TextButton>(refresh).onPressed, isNotNull);
    await tester.tap(refresh);
    await tester.pumpAndSettle();
    expect(api.requests.last, ('target.codex.official', 'account.blue', true));
    expect(find.text('Standard: supported by 1 model'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'expanded account discovers modes once and defaults to Standard',
    (tester) async {
      final api = _CyberApi()..pending = Completer<UpstreamModelCatalog>();
      final controller = await _mount(tester, api);
      expect(api.requests, isEmpty);
      await _expand(tester);
      expect(api.requests, [('target.codex.official', 'account.blue', false)]);
      final select = _select(tester);
      expect(select.initialValue, 'standard');
      expect(select.items.map((item) => item.value), [
        'standard',
        'daybreak_blue',
        'daybreak_red',
      ]);
      expect(
        select.items.where((item) => item.enabled).map((item) => item.value),
        ['standard'],
      );
      expect(find.text('Checking model capabilities…'), findsOneWidget);
      await tester.pump();
      expect(api.requests, hasLength(1));
      api.pending!.complete(await api.catalog(['standard', 'daybreak_blue']));
      await tester.pumpAndSettle();
      expect(find.text('Standard: supported by 1 model'), findsOneWidget);
      expect(find.text('gpt-blue'), findsOneWidget);
      expect(
        _select(
          tester,
        ).items.where((item) => item.enabled).map((item) => item.value),
        ['standard', 'daybreak_blue'],
      );
      expect(controller.inventoryError, isNull);
    },
  );

  for (final programs in <List<String>?>[
    null,
    [],
    ['standard'],
  ]) {
    testWidgets(
      'capability $programs keeps Standard available and gates Daybreak',
      (tester) async {
        final api = _CyberApi()..programs = programs;
        await _mount(tester, api);
        await _expand(tester);
        await tester.pumpAndSettle();
        expect(
          find.text(
            programs == null
                ? 'Daybreak support unknown: this service did not provide complete model capabilities.'
                : programs.isEmpty
                ? 'Standard is available. No verified Daybreak capabilities were reported.'
                : 'Standard: supported by 1 model',
          ),
          findsOneWidget,
        );
        expect(
          _select(
            tester,
          ).items.where((item) => item.enabled).map((item) => item.value),
          ['standard'],
        );
        await tester.tap(_control);
        await tester.pumpAndSettle();
        expect(
          tester
              .widget<MenuItemButton>(
                find.byKey(
                  const Key('account-cyber-option-account.blue-daybreak_blue'),
                ),
              )
              .onPressed,
          isNull,
        );
        expect(
          tester
              .widget<MenuItemButton>(
                find.byKey(
                  const Key('account-cyber-option-account.blue-daybreak_red'),
                ),
              )
              .onPressed,
          isNull,
        );
        expect(
          tester
              .widget<MenuItemButton>(
                find.byKey(
                  const Key('account-cyber-option-account.blue-standard'),
                ),
              )
              .onPressed,
          isNotNull,
        );
      },
    );
  }

  testWidgets(
    'all three modes persist and report only the selected model support',
    (tester) async {
      final api = _CyberApi()
        ..modelPrograms = {
          'standard-model': ['standard'],
          'blue-model': ['standard', 'daybreak_blue'],
          'red-model': ['daybreak_red'],
        };
      final controller = await _mount(tester, api);
      await _expand(tester);
      await tester.pumpAndSettle();
      for (final (program, label, model) in [
        ('daybreak_blue', 'Daybreak Blue', 'blue-model'),
        ('daybreak_red', 'Daybreak Red', 'red-model'),
        ('standard', 'Standard', 'standard-model · blue-model'),
      ]) {
        await _choose(tester, program);
        expect(_account(controller).cyberAccessProgram, program);
        expect(_select(tester).initialValue, program);
        expect(find.text(model), findsOneWidget);
        expect(
          find.text(
            '$label: supported by ${program == 'standard' ? '2 models' : '1 model'}',
          ),
          findsOneWidget,
        );
      }
    },
  );

  testWidgets(
    'unrelated settings preserve the mode and offline selection can revert to Standard',
    (tester) async {
      final api = _CyberApi();
      final controller = await _mount(tester, api);
      await _expand(tester);
      await tester.pumpAndSettle();
      await _choose(tester, 'daybreak_red');
      final revision = _account(controller).settingsRevision;
      await controller.setProviderAccountSettings(
        _account(controller),
        egressProfile: EgressProfileRevision.direct,
        automaticRefresh: true,
      );
      await tester.pumpAndSettle();
      expect(_account(controller).cyberAccessProgram, 'daybreak_red');
      expect(_account(controller).settingsRevision, revision + 1);
      api.failure = StateError('offline');
      await tester.tap(
        find.byKey(const Key('account-cyber-refresh-account.blue')),
      );
      await tester.pumpAndSettle();
      expect(api.requests.last.$3, isTrue);
      expect(
        find.text('Support check unavailable. Your saved mode is unchanged.'),
        findsOneWidget,
      );
      expect(_select(tester).initialValue, 'daybreak_red');
      expect(
        _select(
          tester,
        ).items.where((item) => item.enabled).map((item) => item.value),
        ['standard', 'daybreak_red'],
      );
      await _choose(tester, 'standard');
      expect(_account(controller).cyberAccessProgram, 'standard');
      expect(_account(controller).automaticRefresh, isTrue);
      expect(_account(controller).egressProfile, EgressProfileRevision.direct);
      expect(
        _select(
          tester,
        ).items.where((item) => item.enabled).map((item) => item.value),
        ['standard'],
      );
    },
  );

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets(
      'Standard fallback follows the saved preference and verified evidence in $language',
      (tester) async {
        final api = _CyberApi()
          ..modelPrograms = {
            'ordinary-model': ['standard'],
            'blue-model': ['standard', 'daybreak_blue'],
            'red-model': ['standard', 'daybreak_red'],
            'red-only-model': ['daybreak_red'],
            'unknown-model': null,
            'empty-model': [],
            'unverified-model': ['standard'],
          }
          ..unverifiedModels = {'unverified-model'};
        final controller = await _mount(
          tester,
          api,
          size: const Size(390, 900),
          language: language,
        );
        await _expand(tester);
        await tester.pumpAndSettle();
        final fallback = find.byKey(
          const Key('account-cyber-standard-fallback-account.blue'),
        );
        final prefix = language == AppLanguage.english
            ? 'Uses Standard: '
            : '以下模型使用 Standard：';
        expect(fallback, findsNothing);

        for (final (program, models) in [
          ('daybreak_blue', 'ordinary-model · red-model'),
          ('daybreak_red', 'ordinary-model · blue-model'),
        ]) {
          await _choose(tester, program);
          expect(_account(controller).cyberAccessProgram, program);
          expect(_select(tester).initialValue, program);
          expect(fallback, findsOneWidget);
          expect(tester.widget<Text>(fallback).data, '$prefix$models');
          await tester.ensureVisible(fallback);
          expect(tester.takeException(), isNull);
        }

        await _choose(tester, 'standard');
        expect(_account(controller).cyberAccessProgram, 'standard');
        expect(fallback, findsNothing);

        await _choose(tester, 'daybreak_blue');
        expect(fallback, findsOneWidget);
        final mixed = api.modelPrograms;
        api.modelPrograms = {
          'unknown-model': null,
          'empty-model': [],
          'unverified-model': ['standard'],
        };
        api.pending = Completer<UpstreamModelCatalog>();
        final refresh = find.byKey(
          const Key('account-cyber-refresh-account.blue'),
        );
        await tester.ensureVisible(refresh);
        await tester.tap(refresh);
        await tester.pump();
        expect(fallback, findsNothing);
        api.pending!.complete(await api.catalog(api.programs));
        await tester.pumpAndSettle();
        expect(fallback, findsNothing);
        expect(_account(controller).cyberAccessProgram, 'daybreak_blue');

        api.pending = null;
        api.modelPrograms = mixed;
        await tester.tap(refresh);
        await tester.pumpAndSettle();
        expect(
          tester.widget<Text>(fallback).data,
          '${prefix}ordinary-model · red-model',
        );
        api.failure = StateError('offline');
        await tester.tap(refresh);
        await tester.pumpAndSettle();
        expect(fallback, findsNothing);
        expect(_account(controller).cyberAccessProgram, 'daybreak_blue');
        expect(_select(tester).initialValue, 'daybreak_blue');
        expect(
          find.text(
            language == AppLanguage.english
                ? 'Support check unavailable. Your saved mode is unchanged.'
                : '暂时无法查询支持情况，已保存的模式保持不变。',
          ),
          findsOneWidget,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('removed support remains distinct from a failed lookup', (
    tester,
  ) async {
    final api = _CyberApi();
    await _mount(tester, api);
    await _expand(tester);
    await tester.pumpAndSettle();
    await _choose(tester, 'daybreak_blue');
    api.programs = ['standard'];
    await tester.tap(
      find.byKey(const Key('account-cyber-refresh-account.blue')),
    );
    await tester.pumpAndSettle();
    expect(
      find.text('No models currently advertise support for Daybreak Blue.'),
      findsOneWidget,
    );
    expect(_select(tester).initialValue, 'daybreak_blue');
    expect(find.text('gpt-blue'), findsNothing);
    expect(
      _select(
        tester,
      ).items.where((item) => item.enabled).map((item) => item.value),
      ['standard', 'daybreak_blue'],
    );
  });

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets(
      'timed out mode save shows uncertain outcome and refreshes saved state in $language',
      (tester) async {
        final api = _CyberApi()..timeoutAfterSettingsCommit = true;
        final controller = await _mount(tester, api, language: language);
        await _expand(tester);
        await tester.pumpAndSettle();
        final confirmed = _account(controller);
        await _choose(tester, 'daybreak_blue');

        // The server saved the PUT; a missing response cannot confirm failure.
        final saved = (await api.preview.loadDashboard()).accounts.singleWhere(
          (account) => account.id == confirmed.id,
        );
        expect(api.settingsWrites, 1);
        expect(saved.cyberAccessProgram, 'daybreak_blue');
        expect(saved.settingsRevision, confirmed.settingsRevision + 1);
        expect(_account(controller).cyberAccessProgram, 'standard');
        expect(
          _account(controller).settingsRevision,
          confirmed.settingsRevision,
        );
        expect(_select(tester).initialValue, 'standard');
        expect(controller.inventoryNotice, isNull);

        final copy = AppCopy.forLanguage(language);
        final expected = copy('error.control_timeout');
        expect(
          expected,
          contains(
            language == AppLanguage.english
                ? 'The operation may already have completed.'
                : '操作可能已经完成。',
          ),
        );
        expect(
          expected,
          contains(
            language == AppLanguage.english
                ? 'Refresh to check the latest state before deciding whether to retry.'
                : '请先刷新并检查最新状态，再决定是否重试。',
          ),
        );
        final notice = tester.widget<InlineNotice>(
          find.descendant(
            of: find.byType(ControlFailureNotice),
            matching: find.byType(InlineNotice),
          ),
        );
        expect(notice.message, expected);
        expect(find.text(expected), findsOneWidget);
        expect(
          find.text(copy('provider_accounts.settings.failed')),
          findsNothing,
        );

        await tester.tap(
          find.byKey(const Key('provider-accounts-refresh-all')),
        );
        await tester.pumpAndSettle();
        expect(
          _account(controller).cyberAccessProgram,
          saved.cyberAccessProgram,
        );
        expect(_account(controller).settingsRevision, saved.settingsRevision);
        expect(_select(tester).initialValue, 'daybreak_blue');
        expect(
          api.settingsWrites,
          1,
        ); // Refresh is a read, never mutation replay.
        expect(controller.errorMessage, isNull);
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets('failed save restores the persisted selection', (tester) async {
    final api = _CyberApi()..settingsFailure = StateError('offline');
    final controller = await _mount(tester, api);
    await _expand(tester);
    await tester.pumpAndSettle();
    await _choose(tester, 'daybreak_blue');
    expect(_account(controller).cyberAccessProgram, 'standard');
    expect(_select(tester).initialValue, 'standard');
    expect(
      find.descendant(of: _control, matching: find.text('Standard')),
      findsOneWidget,
    );
    expect(controller.inventoryError, 'provider_accounts.settings.failed');
  });

  testWidgets(
    'replacing credentials discards late capability and cache results',
    (tester) async {
      final api = _CyberApi()..pending = Completer<UpstreamModelCatalog>();
      final controller = await _mount(tester, api);
      await _expand(tester);
      final oldRequest = api.pending!;
      final oldCatalog = await api.catalog(['daybreak_blue']);
      final refresh = find.byKey(
        const Key('account-cyber-refresh-account.blue'),
      );
      final spinner = find.descendant(
        of: refresh,
        matching: find.byType(CircularProgressIndicator),
      );
      await tester.tap(refresh);
      await tester.pump();
      expect(spinner, findsOneWidget);
      final account = _account(controller);
      await api.preview.refreshProviderAccountCredential(account);
      api.pending = Completer<UpstreamModelCatalog>();
      controller.data = await api.preview.loadDashboard();
      await tester.enterText(
        find.byKey(const Key('provider-accounts-search')),
        'Blue',
      );
      await tester.pump();
      expect(api.requests, hasLength(2));
      expect(
        spinner,
        findsNothing,
        reason: 'The replacement account starts with a quiet background read',
      );
      await tester.tap(refresh);
      await tester.pump();
      expect(spinner, findsOneWidget);
      oldRequest.complete(oldCatalog);
      await tester.pump();
      expect(
        spinner,
        findsOneWidget,
        reason: 'Old completion cannot clear the new account wait',
      );
      expect(api.requests, hasLength(2));
      api.pending!.complete(await api.catalog(['standard']));
      await tester.pumpAndSettle();
      expect(spinner, findsNothing);
      expect(find.text('Standard: supported by 1 model'), findsOneWidget);
      expect(
        _select(
          tester,
        ).items.where((item) => item.enabled).map((item) => item.value),
        ['standard'],
      );
      expect(
        controller
            .upstreamModelCatalog('target.codex.official', 'account.blue')!
            .credentialEpoch,
        account.credentialEpoch + 1,
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('discovery uses the selected linked ChatGPT service', (
    tester,
  ) async {
    final api = _CyberApi();
    final controller = await _mount(tester, api);
    final endpoint = await api.preview.createUpstreamEndpoint(
      id: 'target.blue.second',
      displayName: 'ChatGPT second',
      origin: 'https://chatgpt.com',
      backendProtocols: ['openai_responses'],
    );
    await api.preview.setProviderAccountAssociation(
      account: _account(controller),
      endpoint: endpoint,
      linked: true,
    );
    controller.data = await api.preview.loadDashboard();
    controller.selectEndpoint(endpoint.id);
    await tester.pumpAndSettle();
    await _expand(tester);
    await tester.pumpAndSettle();
    expect(api.requests.single, ('target.blue.second', 'account.blue', false));
    await tester.tap(
      find.byKey(const Key('account-cyber-endpoint-account.blue')),
    );
    await tester.pumpAndSettle();
    final original = controller.data!.endpoints.singleWhere(
      (e) => e.id == 'target.codex.official',
    );
    await tester.tap(find.text(original.displayName).last);
    await tester.pumpAndSettle();
    expect(api.requests.last, ('target.codex.official', 'account.blue', false));
  });

  testWidgets('unlinked accounts do not discover models and retain Standard', (
    tester,
  ) async {
    final api = _CyberApi();
    final controller = await _mount(tester, api);
    final endpoint = controller.data!.endpoints.singleWhere(
      (e) => e.id == 'target.codex.official',
    );
    await api.preview.setProviderAccountAssociation(
      account: _account(controller),
      endpoint: endpoint,
      linked: false,
    );
    controller.data = await api.preview.loadDashboard();
    await _expand(tester);
    await tester.pumpAndSettle();
    expect(api.requests, isEmpty);
    expect(
      find.text(
        'Link this account to a ChatGPT service to check model support.',
      ),
      findsOneWidget,
    );
    expect(
      _select(
        tester,
      ).items.where((item) => item.enabled).map((item) => item.value),
      ['standard'],
    );
  });

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final width in [390.0, 1180.0]) {
      testWidgets(
        'selector stays compact without overflow at $width in $language',
        (tester) async {
          final api = _CyberApi();
          await _mount(tester, api, size: Size(width, 900), language: language);
          await _expand(tester);
          await tester.pumpAndSettle();
          expect(tester.getSize(_control).width, lessThanOrEqualTo(280));
          expect(tester.getSize(_control).height, lessThanOrEqualTo(36));
          await tester.ensureVisible(_control);
          await tester.tap(_control);
          await tester.pumpAndSettle();
          expect(find.byType(MenuItemButton), findsNWidgets(3));
          expect(tester.takeException(), isNull);
        },
      );
    }
  }
}

Finder get _control =>
    find.byKey(const Key('account-cyber-access-account.blue'));
CompactSelectField<String> _select(WidgetTester tester) =>
    tester.widget<CompactSelectField<String>>(
      find.descendant(
        of: _control,
        matching: find.byType(CompactSelectField<String>),
      ),
    );
ProviderAccount _account(WorkbenchController controller) =>
    controller.data!.accounts.singleWhere((a) => a.id == 'account.blue');
Future<void> _choose(WidgetTester tester, String program) async {
  await tester.ensureVisible(_control);
  await tester.tap(_control);
  await tester.pumpAndSettle();
  final option = find.byKey(Key('account-cyber-option-account.blue-$program'));
  expect(tester.widget<MenuItemButton>(option).onPressed, isNotNull);
  await tester.tap(option);
  await tester.pumpAndSettle();
}

Future<WorkbenchController> _mount(
  WidgetTester tester,
  _CyberApi api, {
  Size size = const Size(1180, 900),
  AppLanguage language = AppLanguage.english,
}) async {
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await api.preview.createProviderAccount(
    id: 'account.blue',
    displayName: 'Blue fixture',
    upstreamEndpointId: 'target.codex.official',
    kind: 'codex_oauth',
    secret: '',
    codexAuthJson: jsonEncode({
      'auth_mode': 'chatgpt',
      'tokens': {
        'account_id': 'blue-fixture',
        'access_token': 'synthetic',
        'id_token': 'synthetic',
        'refresh_token': 'synthetic',
      },
      'last_refresh': '2026-10-18T00:00:00Z',
    }),
    headerPolicy: const ProviderAccountHeaderPolicy(),
  );
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: api.preview.close,
    initialPreferences: WorkbenchPreferences(language: language),
  );
  addTearDown(controller.dispose);
  controller.data = await api.preview.loadDashboard();
  await tester.pumpWidget(
    MaterialApp(
      theme: ViberTheme.dark(),
      home: Scaffold(
        body: AnimatedBuilder(
          animation: controller,
          builder: (context, _) => ProviderAccountsView(
            controller: controller,
            copy: AppCopy.forLanguage(language),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  await tester.enterText(
    find.byKey(const Key('provider-accounts-search')),
    'Blue fixture',
  );
  await tester.pumpAndSettle();
  return controller;
}

Future<void> _expand(WidgetTester tester) async {
  await tester.tap(
    find.byKey(const Key('provider-account-details-toggle-account.blue')),
  );
  await tester.pump();
}

final class _CyberApi extends Fake implements ControlApi {
  final preview = PreviewControlApi(seedCaptures: false);
  final requests = <(String, String, bool)>[];
  List<String>? programs = const ['standard', 'daybreak_blue', 'daybreak_red'];
  Completer<UpstreamModelCatalog>? pending;
  Object? failure;
  Object? settingsFailure;
  bool timeoutAfterSettingsCommit = false;
  int settingsWrites = 0;
  Map<String, List<String>?>? modelPrograms;
  Set<String> unverifiedModels = {};

  Future<UpstreamModelCatalog> catalog(
    List<String>? programs, {
    String endpointId = 'target.codex.official',
  }) async {
    final base = await preview.upstreamModels(
      endpointId,
      accountId: 'account.blue',
    );
    return UpstreamModelCatalog(
      endpointId: base.endpointId,
      endpointRevision: base.endpointRevision,
      accountId: base.accountId,
      accountRevision: base.accountRevision,
      credentialEpoch: base.credentialEpoch,
      observedAt: base.observedAt,
      availabilitySource: base.availabilitySource,
      models: [
        for (final entry in (modelPrograms ?? {'gpt-blue': programs}).entries)
          UpstreamModel.fromJson({
            'id': entry.key,
            'displayName': 'GPT Blue',
            'ownedBy': 'openai',
            'verifiedAvailable': !unverifiedModels.contains(entry.key),
            'contextLimit': 400000,
            'outputLimit': 128000,
            'availableCyberPrograms': entry.value,
          }, 'model'),
      ],
    );
  }

  @override
  Future<UpstreamModelCatalog> upstreamModels(
    String endpointId, {
    required String accountId,
    bool refresh = false,
  }) {
    requests.add((endpointId, accountId, refresh));
    if (failure != null) return Future.error(failure!);
    return pending?.future ?? catalog(programs, endpointId: endpointId);
  }

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) =>
      preview.accountFacts(id, history: history);

  @override
  Future<DashboardData> loadDashboard() => preview.loadDashboard();

  @override
  Future<List<ApprovalRecord>> pendingApprovals() => preview.pendingApprovals();

  @override
  Future<ProviderAccount> setProviderAccountSettings(
    ProviderAccount account, {
    required EgressProfileRevision? egressProfile,
    required bool automaticRefresh,
    String? cyberAccessProgram,
  }) async {
    settingsWrites++;
    if (settingsFailure != null) throw settingsFailure!;
    final saved = await preview.setProviderAccountSettings(
      account,
      egressProfile: egressProfile,
      automaticRefresh: automaticRefresh,
      cyberAccessProgram: cyberAccessProgram,
    );
    if (timeoutAfterSettingsCommit) {
      throw ControlRequestTimeout(const Duration(seconds: 30));
    }
    return saved;
  }
}

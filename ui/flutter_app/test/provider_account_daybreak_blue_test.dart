import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  testWidgets('expanded account discovers Blue once and starts off', (
    tester,
  ) async {
    final api = _DaybreakApi()..pending = Completer<UpstreamModelCatalog>();
    final controller = await _mount(tester, api);
    expect(api.requests, isEmpty);
    await _expand(tester);
    expect(api.requests, [('target.codex.official', 'account.blue', false)]);
    final toggle = find.byKey(const Key('account-daybreak-blue-account.blue'));
    expect(tester.widget<Switch>(toggle).value, isFalse);
    expect(tester.widget<Switch>(toggle).onChanged, isNull);
    expect(find.text('Checking Daybreak Blue support…'), findsOneWidget);
    await tester.pump();
    expect(api.requests, hasLength(1));
    api.pending!.complete(await api.catalog(['standard', 'daybreak_blue']));
    await tester.pumpAndSettle();
    expect(find.text('Supported by 1 model'), findsOneWidget);
    expect(find.text('gpt-blue'), findsOneWidget);
    expect(tester.widget<Switch>(toggle).onChanged, isNotNull);
    expect(controller.inventoryError, isNull);
  });

  for (final programs in <List<String>?>[
    null,
    [],
    ['standard'],
  ]) {
    testWidgets('capability $programs distinguishes unknown from unsupported', (
      tester,
    ) async {
      final api = _DaybreakApi()..programs = programs;
      await _mount(tester, api);
      await _expand(tester);
      await tester.pumpAndSettle();
      expect(
        find.text(
          programs == null
              ? 'Support unknown: this service did not provide model capabilities.'
              : 'No models currently advertise Daybreak Blue support.',
        ),
        findsOneWidget,
      );
      expect(
        tester
            .widget<Switch>(
              find.byKey(const Key('account-daybreak-blue-account.blue')),
            )
            .onChanged,
        isNull,
      );
    });
  }

  testWidgets('toggle persists and can be disabled after discovery fails', (
    tester,
  ) async {
    final api = _DaybreakApi();
    final controller = await _mount(tester, api);
    await _expand(tester);
    await tester.pumpAndSettle();
    ProviderAccount current() => controller.data!.accounts.singleWhere(
      (account) => account.id == 'account.blue',
    );
    final toggle = find.byKey(const Key('account-daybreak-blue-account.blue'));
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(current().daybreakBlueEnabled, isTrue);
    final enabledRevision = current().settingsRevision;
    await controller.setProviderAccountSettings(
      current(),
      egressProfile: EgressProfileRevision.direct,
      automaticRefresh: true,
    );
    await tester.pumpAndSettle();
    expect(current().daybreakBlueEnabled, isTrue);
    expect(current().settingsRevision, enabledRevision + 1);
    api.failure = StateError('offline');
    await tester.tap(
      find.byKey(const Key('account-daybreak-refresh-account.blue')),
    );
    await tester.pumpAndSettle();
    expect(api.requests.last.$3, isTrue);
    expect(
      find.text(
        'Support check unavailable. Your saved preference is unchanged.',
      ),
      findsOneWidget,
    );
    expect(tester.widget<Switch>(toggle).value, isTrue);
    expect(tester.widget<Switch>(toggle).onChanged, isNotNull);
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(current().daybreakBlueEnabled, isFalse);
    expect(current().automaticRefresh, isTrue);
    expect(current().egressProfile, EgressProfileRevision.direct);
    expect(tester.widget<Switch>(toggle).onChanged, isNull);
    api.failure = null;
    await tester.tap(
      find.byKey(const Key('account-daybreak-refresh-account.blue')),
    );
    await tester.pumpAndSettle();
    expect(tester.widget<Switch>(toggle).onChanged, isNotNull);
    expect(current().daybreakBlueEnabled, isFalse);
  });

  testWidgets(
    'replacing credentials discards late capability and cache results',
    (tester) async {
      final api = _DaybreakApi()..pending = Completer<UpstreamModelCatalog>();
      final controller = await _mount(tester, api);
      await _expand(tester);
      final oldRequest = api.pending!;
      final oldCatalog = await api.catalog(['daybreak_blue']);
      final account = controller.data!.accounts.singleWhere(
        (a) => a.id == 'account.blue',
      );
      await api.preview.refreshProviderAccountCredential(account);
      api.pending = Completer<UpstreamModelCatalog>();
      controller.data = await api.preview.loadDashboard();
      await tester.enterText(
        find.byKey(const Key('provider-accounts-search')),
        'Blue',
      );
      await tester.pump();
      expect(api.requests, hasLength(2));
      api.pending!.complete(await api.catalog(['standard']));
      await tester.pumpAndSettle();
      expect(
        find.text('No models currently advertise Daybreak Blue support.'),
        findsOneWidget,
      );
      oldRequest.complete(oldCatalog);
      await tester.pumpAndSettle();
      expect(find.text('Supported by 1 model'), findsNothing);
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
    final api = _DaybreakApi();
    final controller = await _mount(tester, api);
    final endpoint = await api.preview.createUpstreamEndpoint(
      id: 'target.blue.second',
      displayName: 'ChatGPT second',
      origin: 'https://chatgpt.com',
      backendProtocols: ['openai_responses'],
    );
    final account = controller.data!.accounts.singleWhere(
      (a) => a.id == 'account.blue',
    );
    await api.preview.setProviderAccountAssociation(
      account: account,
      endpoint: endpoint,
      linked: true,
    );
    controller.data = await api.preview.loadDashboard();
    controller.selectEndpoint(endpoint.id);
    await tester.pumpAndSettle();
    await _expand(tester);
    await tester.pumpAndSettle();
    expect(api.requests.single, ('target.blue.second', 'account.blue', false));
    expect(find.text('Supported by 1 model'), findsOneWidget);
    await tester.tap(
      find.byKey(const Key('account-daybreak-endpoint-account.blue')),
    );
    await tester.pumpAndSettle();
    final original = controller.data!.endpoints.singleWhere(
      (e) => e.id == 'target.codex.official',
    );
    await tester.tap(find.text(original.displayName).last);
    await tester.pumpAndSettle();
    expect(api.requests.last, ('target.codex.official', 'account.blue', false));
    expect(find.text('Supported by 1 model'), findsOneWidget);
  });

  testWidgets(
    'unlinked accounts explain missing scope without discovering models',
    (tester) async {
      final api = _DaybreakApi();
      final controller = await _mount(tester, api);
      final account = controller.data!.accounts.singleWhere(
        (a) => a.id == 'account.blue',
      );
      final endpoint = controller.data!.endpoints.singleWhere(
        (e) => e.id == 'target.codex.official',
      );
      await api.preview.setProviderAccountAssociation(
        account: account,
        endpoint: endpoint,
        linked: false,
      );
      controller.data = await api.preview.loadDashboard();
      await _expand(tester);
      await tester.pumpAndSettle();
      expect(api.requests, isEmpty);
      expect(
        find.text('Link this account to a ChatGPT service to check support.'),
        findsOneWidget,
      );
    },
  );
}

Future<WorkbenchController> _mount(
  WidgetTester tester,
  _DaybreakApi api,
) async {
  await tester.binding.setSurfaceSize(const Size(1180, 900));
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
            copy: AppCopy.forLanguage(AppLanguage.english),
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

final class _DaybreakApi extends Fake implements ControlApi {
  final preview = PreviewControlApi(seedCaptures: false);
  final requests = <(String, String, bool)>[];
  List<String>? programs = const ['standard', 'daybreak_blue'];
  Completer<UpstreamModelCatalog>? pending;
  Object? failure;

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
        UpstreamModel.fromJson({
          'id': 'gpt-blue',
          'displayName': 'GPT Blue',
          'ownedBy': 'openai',
          'verifiedAvailable': true,
          'contextLimit': 400000,
          'outputLimit': 128000,
          'availableCyberPrograms': programs,
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
  Future<ProviderAccount> setProviderAccountSettings(
    ProviderAccount account, {
    required EgressProfileRevision? egressProfile,
    required bool automaticRefresh,
    bool? daybreakBlueEnabled,
  }) => preview.setProviderAccountSettings(
    account,
    egressProfile: egressProfile,
    automaticRefresh: automaticRefresh,
    daybreakBlueEnabled: daybreakBlueEnabled,
  );
}

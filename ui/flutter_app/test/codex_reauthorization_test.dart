import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

ProviderAccount accountFixture({
  int epoch = 7,
  bool disabled = false,
  bool reconnect = true,
}) => ProviderAccount(
  id: 'account.original',
  displayName: 'Original account',
  note: 'Keep this note',
  noteRevision: 3,
  credentialOrigin: 'https://chatgpt.com',
  linkedEndpointIds: const ['missing.service'],
  associationRevision: 4,
  kind: 'codex_oauth',
  realmId: 'openai.codex',
  state: disabled ? 'disabled' : 'active',
  revision: 2,
  credentialState: disabled ? 'unavailable' : 'ready',
  credentialEpoch: epoch,
  setHeaderNames: const ['X-Original'],
  deleteHeaderNames: const ['X-Removed'],
  settingsRevision: 5,
  automaticRefresh: false,
  supportsAutomaticRefresh: true,
  egressProfile: EgressProfileRevision.direct,
  codexOAuth: CodexOAuthAccount(
    chatgptAccountId: 'workspace-original',
    email: 'original@example.test',
    userId: 'user-original',
    planType: 'plus',
    fedRamp: false,
    expiresAt: DateTime.utc(2020),
    lastRefresh: DateTime.utc(2020),
    state: epoch == 7 && reconnect ? 'reconnect_required' : 'ready',
  ),
);

// The real HTTP login adapter sits below the real row/dialog/controller. Only
// runtime inventory and the external OAuth server are controlled fixtures.
class ReauthorizationApi implements ControlApi {
  final preview = PreviewControlApi(seedCaptures: false);
  late HttpControlApi loginApi;
  ProviderAccount account = accountFixture();
  String? failure;
  final requests = <http.Request>[];
  Completer<CodexLogin>? pendingPoll;
  final loginId = 'L' * 43;
  String mode = 'manual';
  String state = 'pending';
  int dashboardReads = 0;
  int quotaReads = 0;
  bool includeEndpoints = false;
  int expectedEpoch = 7;
  bool dashboardFails = false;

  Future<void> open() async {
    loginApi = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:1'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'fixture',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
      inspectSession: false,
      client: MockClient((request) async {
        requests.add(request);
        final path = request.url.path;
        if (path == '/api/v1/codex-oauth/logins') {
          expect(request.method, 'POST');
          expect(request.headers['if-match'], '$expectedEpoch');
          expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
          final body = jsonDecode(request.body);
          mode = kIsWeb ? 'manual' : 'loopback';
          expect(body, {
            'mode': 'reauthorize',
            'accountId': 'account.original',
            'callbackMode': mode,
          });
          state = 'pending';
          return http.Response(jsonEncode(view()), 201);
        }
        expect(path, startsWith('/api/v1/codex-oauth/logins/$loginId'));
        if (request.method == 'DELETE') {
          state = 'cancelled';
          return http.Response('', 204);
        }
        if (path.endsWith('/callback')) {
          if (failure == null) {
            account = accountFixture(epoch: 8);
            state = 'completed';
          } else {
            state = 'failed';
            if (failure == 'login_account_changed') {
              account = accountFixture(epoch: 8);
            }
          }
        }
        return http.Response(jsonEncode(view()), 200);
      }),
    );
  }

  Map<String, Object?> view() => {
    'id': loginId,
    'state': state,
    'callbackMode': mode,
    'authorizationUrl': state == 'pending'
        ? 'https://auth.openai.com/oauth/authorize?state=${'S' * 43}&code_challenge_method=S256'
        : '',
    'expiresAt': DateTime.now()
        .toUtc()
        .add(const Duration(minutes: 15))
        .toIso8601String(),
    if (state == 'completed') 'accountId': account.id,
    if (state == 'failed') 'reason': failure,
  };

  @override
  Future<CodexLogin> startCodexReauthorization({
    required ProviderAccount account,
    required String callbackMode,
  }) => loginApi.startCodexReauthorization(
    account: account,
    callbackMode: callbackMode,
  );
  @override
  Future<CodexLogin> codexLoginStatus(String id) =>
      pendingPoll?.future ?? loginApi.codexLoginStatus(id);
  @override
  Future<CodexLogin> completeCodexLogin(String id, String callback) =>
      loginApi.completeCodexLogin(id, callback);
  @override
  Future<void> cancelCodexLogin(String id) => loginApi.cancelCodexLogin(id);
  @override
  Future<DashboardData> loadDashboard() async {
    dashboardReads++;
    if (dashboardFails) throw StateError('Synthetic dashboard network failure');
    final base = await preview.loadDashboard();
    return DashboardData(
      status: base.status,
      captures: const [],
      captureNextCursor: null,
      environments: base.environments,
      endpoints: includeEndpoints ? base.endpoints : const [],
      accounts: [account],
    );
  }

  @override
  Future<AccountFacts> accountFacts(
    String accountId, {
    bool history = false,
  }) async {
    quotaReads++;
    return AccountFacts.fromJson({
      'accountId': accountId,
      'credentialEpoch': account.credentialEpoch,
      'origin': account.credentialOrigin,
      'adapterId': 'fixture',
      'adapterRevision': 1,
      'observedAt': DateTime.now().toUtc().toIso8601String(),
      'state': 'known',
      'limits': <Object>[],
    });
  }

  @override
  Future<List<ApprovalRecord>> pendingApprovals() async => [];
  @override
  Future<void> close() async {
    await loginApi.close();
    await preview.close();
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw StateError('Unexpected API call: ${invocation.memberName}');
}

Future<WorkbenchController> openAccounts(
  WidgetTester tester,
  ReauthorizationApi api,
  AppLanguage language,
) async {
  await tester.binding.setSurfaceSize(const Size(390, 900));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await api.open();
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: api.close,
  );
  addTearDown(controller.dispose);
  addTearDown(api.close);
  await controller.refresh();
  await tester.pumpWidget(
    MaterialApp(
      // Flutter's Chrome test server does not serve the InkSparkle shader.
      // Keep the production theme's layout/colors with an asset-free ripple.
      theme: ViberTheme.dark().copyWith(splashFactory: InkRipple.splashFactory),
      home: Scaffold(
        body: ListenableBuilder(
          listenable: controller,
          builder: (context, _) => ProviderAccountsView(
            controller: controller,
            copy: AppCopy.forLanguage(language),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return controller;
}

Future<void> startAgain(WidgetTester tester) async {
  final action = find.byKey(
    const Key('account-sign-in-again-account.original'),
  );
  expect(action, findsOneWidget);
  await tester.ensureVisible(action);
  await tester.tap(action);
  await tester.pumpAndSettle();
  expect(find.text('original@example.test'), findsWidgets);
  expect(find.byKey(const Key('account-editor-service')), findsNothing);
  expect(find.byKey(const Key('account-editor-name')), findsNothing);
  await tester.tap(find.byKey(const Key('codex-oauth-start')));
  await tester.runAsync(
    () => Future<void>.delayed(const Duration(milliseconds: 20)),
  );
  await tester.pumpAndSettle();
  expect(
    find.byKey(const Key('codex-oauth-callback')),
    findsOneWidget,
    reason: tester
        .widgetList<Text>(find.byType(Text))
        .map((text) => text.data)
        .join(' | '),
  );
}

Future<void> submit(WidgetTester tester) async {
  await tester.enterText(
    find.byKey(const Key('codex-oauth-callback')),
    'http://localhost:1455/auth/callback?code=synthetic&state=${'S' * 43}',
  );
  await tester.ensureVisible(find.byKey(const Key('codex-oauth-submit')));
  await tester.tap(find.byKey(const Key('codex-oauth-submit')));
  await tester.runAsync(
    () => Future<void>.delayed(const Duration(milliseconds: 20)),
  );
  await tester.pumpAndSettle();
}

void main() {
  for (final language in AppLanguage.values) {
    testWidgets(
      'conflict reload failure blocks stale login until recovery in $language',
      (tester) async {
        final api = ReauthorizationApi()..failure = 'login_account_changed';
        final controller = await openAccounts(tester, api, language);
        final original = controller.data!.accounts.single;
        await startAgain(tester);
        api.dashboardFails = true;
        await submit(tester);
        expect(controller.data!.accounts.single, same(original));
        expect(
          find.textContaining(
            language == AppLanguage.english
                ? 'status has been reloaded'
                : '已重新加载当前状态',
          ),
          findsNothing,
        );
        expect(
          find.text(
            language == AppLanguage.english
                ? 'Could not reload this account. Check your connection and try again.'
                : '无法重新加载此账号，请检查连接后重试。',
          ),
          findsOneWidget,
        );
        final start = find.byKey(const Key('codex-oauth-start'));
        await tester.ensureVisible(start);
        await tester.tap(start);
        await tester.runAsync(
          () => Future<void>.delayed(const Duration(milliseconds: 20)),
        );
        await tester.pumpAndSettle();
        expect(
          api.requests.where((r) => r.url.path == '/api/v1/codex-oauth/logins'),
          hasLength(1),
          reason: 'A failed reload must never submit the unverified old epoch',
        );
        expect(controller.data!.accounts.single, same(original));
        final failedReads = api.dashboardReads;
        api.dashboardFails = false;
        api.expectedEpoch = 8;
        await tester.tap(start);
        await tester.runAsync(
          () => Future<void>.delayed(const Duration(milliseconds: 20)),
        );
        await tester.pumpAndSettle();
        expect(api.dashboardReads, greaterThan(failedReads));
        expect(controller.data!.accounts.single.credentialEpoch, 8);
        expect(
          api.requests.where((r) => r.url.path == '/api/v1/codex-oauth/logins'),
          hasLength(2),
        );
        expect(find.byKey(const Key('codex-oauth-callback')), findsOneWidget);
        expect(controller.inventoryNotice, isNull);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.runAsync(
          () => Future<void>.delayed(const Duration(milliseconds: 20)),
        );
      },
    );
  }
  test(
    'reauthorization HTTP targets original epoch without create fields',
    () async {
      final api = ReauthorizationApi();
      await api.open();
      addTearDown(api.close);
      final login = await api.startCodexReauthorization(
        account: api.account,
        callbackMode: kIsWeb ? 'manual' : 'loopback',
      );
      expect(login.active, isTrue);
    },
  );
  test(
    'invalid reauthorization cannot send a create or login request',
    () async {
      final api = ReauthorizationApi();
      await api.open();
      addTearDown(api.close);
      for (final mode in ['manual', 'loopback']) {
        await expectLater(
          api.startCodexReauthorization(
            account: accountFixture(epoch: 0, disabled: true),
            callbackMode: mode,
          ),
          throwsA(isA<ControlContractException>()),
        );
      }
      await expectLater(
        api.startCodexReauthorization(
          account: api.account,
          callbackMode: 'foreign',
        ),
        throwsA(isA<ControlContractException>()),
      );
      expect(api.requests, isEmpty);
    },
  );
  for (final reason in [
    'login_identity_mismatch',
    'login_account_changed',
    'login_denied',
    'login_exchange_failed',
    'login_account_save_failed',
  ]) {
    testWidgets('$reason keeps the account and allows retry', (tester) async {
      final api = ReauthorizationApi()..failure = reason;
      final controller = await openAccounts(tester, api, AppLanguage.english);
      final original = controller.data!.accounts.single;
      await startAgain(tester);
      await submit(tester);
      expect(controller.data!.accounts, hasLength(1));
      if (reason == 'login_account_changed') {
        expect(controller.data!.accounts.single.credentialEpoch, 8);
        expect(api.dashboardReads, greaterThan(1));
      } else {
        expect(controller.data!.accounts.single, same(original));
      }
      expect(controller.inventoryNotice, isNull);
      expect(find.byKey(const Key('codex-oauth-start')), findsOneWidget);
      expect(
        find.textContaining(
          reason == 'login_identity_mismatch'
              ? 'different account'
              : reason == 'login_account_changed'
              ? 'current status'
              : 'Sign in again',
        ),
        findsWidgets,
      );
      api.expectedEpoch = reason == 'login_account_changed' ? 8 : 7;
      await tester.ensureVisible(find.byKey(const Key('codex-oauth-start')));
      await tester.tap(find.byKey(const Key('codex-oauth-start')));
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 20)),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('codex-oauth-callback')), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 20)),
      );
    });
  }
  testWidgets(
    'cancel and a stale poll cannot renew or navigate the original account',
    (tester) async {
      final api = ReauthorizationApi();
      final controller = await openAccounts(tester, api, AppLanguage.english);
      final original = controller.data!.accounts.single;
      await startAgain(tester);
      api.pendingPoll = Completer<CodexLogin>();
      await tester.pump(const Duration(seconds: 2));
      await tester.ensureVisible(find.byKey(const Key('codex-oauth-cancel')));
      await tester.tap(find.byKey(const Key('codex-oauth-cancel')));
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 20)),
      );
      await tester.pumpAndSettle();
      api.pendingPoll!.complete(
        CodexLogin(
          id: api.loginId,
          state: 'completed',
          callbackMode: api.mode,
          authorizationUrl: '',
          expiresAt: DateTime.now().toUtc(),
          accountId: original.id,
        ),
      );
      await tester.pumpAndSettle();
      expect(controller.data!.accounts.single, same(original));
      expect(controller.inventoryNotice, isNull);
      expect(find.byKey(const Key('codex-oauth-start')), findsOneWidget);
      expect(api.requests.where((r) => r.method == 'DELETE'), hasLength(1));
      await tester.pumpWidget(const SizedBox.shrink());
      expect(tester.takeException(), isNull);
    },
  );
  testWidgets('disposing a pending dialog cancels and ignores a late poll', (
    tester,
  ) async {
    final api = ReauthorizationApi();
    final controller = await openAccounts(tester, api, AppLanguage.english);
    final original = controller.data!.accounts.single;
    await startAgain(tester);
    api.pendingPoll = Completer<CodexLogin>();
    await tester.pump(const Duration(seconds: 2));
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    api.pendingPoll!.complete(
      CodexLogin(
        id: api.loginId,
        state: 'completed',
        callbackMode: api.mode,
        authorizationUrl: '',
        expiresAt: DateTime.now().toUtc(),
        accountId: original.id,
      ),
    );
    await tester.pump();
    expect(controller.data!.accounts.single, same(original));
    expect(api.requests.where((r) => r.method == 'DELETE'), hasLength(1));
    expect(controller.inventoryNotice, isNull);
    expect(tester.takeException(), isNull);
  });
  testWidgets(
    'disabled account explains enable-first without starting a login',
    (tester) async {
      final api = ReauthorizationApi()
        ..account = accountFixture(epoch: 0, disabled: true);
      await openAccounts(tester, api, AppLanguage.english);
      await tester.tap(
        find.byKey(const Key('account-sign-in-again-account.original')),
      );
      await tester.pumpAndSettle();
      expect(
        find.text('Enable this account first, then sign in again.'),
        findsOneWidget,
      );
      expect(find.byKey(const Key('codex-oauth-start')), findsNothing);
      expect(api.requests, isEmpty);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );
  testWidgets('existing import replacement stays separate and available', (
    tester,
  ) async {
    final api = ReauthorizationApi()..includeEndpoints = true;
    await openAccounts(tester, api, AppLanguage.english);
    await tester.tap(find.byKey(const Key('account-update-account.original')));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('account-editor-load-auth-json')),
      findsOneWidget,
    );
    expect(
      find.byKey(const Key('account-editor-codex-auth-json')),
      findsOneWidget,
    );
    expect(find.byKey(const Key('codex-oauth-start')), findsNothing);
    expect(api.requests, isEmpty);
    await tester.pumpWidget(const SizedBox.shrink());
  });
  testWidgets(
    'successful intentional sign-in replaces current quota observations',
    (tester) async {
      final api = ReauthorizationApi()
        ..account = accountFixture(reconnect: false);
      final controller = await openAccounts(tester, api, AppLanguage.english);
      final original = controller.data!.accounts.single;
      final oldQuota = controller.providerAccountQuota(original)!;
      expect(oldQuota.credentialEpoch, 7);
      await startAgain(tester);
      await submit(tester);
      final renewed = controller.data!.accounts.single;
      final quota = controller.providerAccountQuota(renewed)!;
      expect(quota.credentialEpoch, 8);
      expect(quota, isNot(same(oldQuota)));
      expect(api.quotaReads, 2);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );
  for (final language in AppLanguage.values) {
    testWidgets(
      'expired existing account renews the original ID at 390px in $language',
      (tester) async {
        final api = ReauthorizationApi();
        final controller = await openAccounts(tester, api, language);
        final original = controller.data!.accounts.single;
        expect(original.usable, isFalse);
        await startAgain(tester);
        await submit(tester);
        final renewed = controller.data!.accounts.single;
        expect(renewed.id, original.id);
        expect(renewed.credentialEpoch, 8);
        expect(renewed.displayName, original.displayName);
        expect(renewed.note, original.note);
        expect(renewed.linkedEndpointIds, original.linkedEndpointIds);
        expect(renewed.settingsRevision, original.settingsRevision);
        expect(renewed.automaticRefresh, original.automaticRefresh);
        expect(
          find.text(
            language == AppLanguage.english ? 'Signed in again' : '已重新登录',
          ),
          findsOneWidget,
        );
        expect(
          api.requests.where((r) => r.url.path == '/api/v1/codex-oauth/logins'),
          hasLength(1),
        );
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }
}

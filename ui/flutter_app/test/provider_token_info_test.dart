import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/provider_account_editor.dart';
import 'package:vibermate_app/features/workbench/provider_account_token_details.dart';

ProviderAccount _account({ProviderTokenInfo? info, CodexOAuthAccount? oauth}) =>
    ProviderAccount(
      id: 'account.jwt',
      displayName: 'Codex work',
      credentialOrigin: 'https://chatgpt.com',
      linkedEndpointIds: const [],
      kind: oauth == null ? 'bearer_token' : 'codex_oauth',
      realmId: 'openai.chatgpt',
      state: 'active',
      revision: 1,
      credentialState: 'ready',
      credentialEpoch: 1,
      setHeaderNames: const [],
      deleteHeaderNames: const [],
      tokenInfo: info,
      codexOAuth: oauth,
    );

void main() {
  final info = ProviderTokenInfo(
    email: 'engineer@example.com',
    chatgptAccountId: 'workspace-42',
    userId: 'user-42',
    planType: 'pro',
    issuedAt: DateTime.utc(2020, 1, 1, 10),
    expiresAt: DateTime.utc(2020, 1, 1, 11),
  );

  test('token metadata is allowlisted and never invents auth_time', () {
    final json = <String, Object?>{
      'email': 'engineer@example.com',
      'issuedAt': '2026-09-22T00:00:00Z',
      'expiresAt': '2026-09-22T01:00:00Z',
    };
    final parsed = ProviderTokenInfo.fromJson(json, 'tokenInfo');
    expect(parsed.email, 'engineer@example.com');
    expect(parsed.issuedAt, DateTime.utc(2026, 9, 22));
    expect(parsed.expiresAt, DateTime.utc(2026, 9, 22, 1));
    expect(parsed.authenticatedAt, isNull);
    for (final extra in [
      {'accessToken': 'must-not-cross-this-boundary'},
      {'refreshToken': 'must-not-cross-this-boundary'},
      {'rawClaims': <String, Object?>{}},
      {'email': 'unsafe\n@example.com'},
      {'email': 'x' * 513},
      {'issuedAt': 'not-a-date'},
    ]) {
      expect(
        () => ProviderTokenInfo.fromJson({...json, ...extra}, 'tokenInfo'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final account = ProviderAccount.fromJson({
      'id': 'account.jwt',
      'displayName': 'Codex work',
      'credentialOrigin': 'https://chatgpt.com',
      'linkedEndpointIds': <String>[],
      'associationRevision': 1,
      'kind': 'bearer_token',
      'realmId': 'openai.chatgpt',
      'state': 'active',
      'revision': 1,
      'credentialState': 'ready',
      'credentialEpoch': 1,
      'setHeaderNames': <String>[],
      'deleteHeaderNames': <String>[],
      'tokenInfo': json,
    }, 'account');
    expect(account.tokenInfo?.email, parsed.email);
    expect(
      account.withAssociations(['target.codex.official'], 2).tokenInfo,
      same(account.tokenInfo),
    );
    expect(
      account.usable,
      isTrue,
      reason: 'unverified metadata is not credential authority',
    );
  });

  for (final width in [390.0, 1180.0]) {
    for (final language in AppLanguage.values) {
      for (final dark in [true, false]) {
        testWidgets(
          'token details remain readable at $width px in $language, dark=$dark',
          (tester) async {
            await tester.binding.setSurfaceSize(Size(width, 900));
            addTearDown(() => tester.binding.setSurfaceSize(null));
            final account = _account(info: info);
            final copy = AppCopy.forLanguage(language);
            await tester.pumpWidget(
              MaterialApp(
                theme: dark ? ViberTheme.dark() : ViberTheme.light(),
                home: Scaffold(
                  body: SingleChildScrollView(
                    child: Column(
                      children: [
                        ProviderAccountRow(
                          account: account,
                          compact: width < 850,
                          copy: copy,
                          busy: false,
                          onReplace: () {},
                          onDelete: () {},
                        ),
                        ProviderAccountTokenDetails(
                          account: account,
                          copy: copy,
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            );
            expect(find.text('engineer@example.com'), findsOneWidget);
            expect(
              find.textContaining(
                language == AppLanguage.english ? 'Token expired:' : '令牌已过期：',
              ),
              findsOneWidget,
            );
            expect(
              find.text(copy('provider_accounts.token.authenticated')),
              findsNothing,
            );
            final toggle = find.byKey(
              const Key('provider-account-token-toggle-account.jwt'),
            );
            await tester.tap(toggle);
            await tester.pumpAndSettle();
            expect(
              find.text(copy('provider_accounts.token.authenticated')),
              findsOneWidget,
            );
            expect(
              find.text(copy('provider_accounts.token.issued')),
              findsOneWidget,
            );
            expect(
              find.text(copy('provider_accounts.token.not_provided')),
              findsOneWidget,
            );
            expect(
              find.text(copy('provider_accounts.token.unverified')),
              findsOneWidget,
            );
            expect(
              find.text(copy('provider_accounts.token.manual')),
              findsOneWidget,
            );
            expect(find.text('workspace-42'), findsOneWidget);
            expect(find.textContaining('UTC'), findsWidgets);
            expect(tester.takeException(), isNull);
            await tester.tap(toggle);
            await tester.pumpAndSettle();
            expect(find.text('workspace-42'), findsNothing);
            expect(tester.takeException(), isNull);
          },
        );
      }
    }
  }

  testWidgets(
    'OAuth details show actual sign-in and refresh times; opaque tokens have no JWT panel',
    (tester) async {
      final copy = AppCopy.forLanguage(AppLanguage.english);
      final oauth = CodexOAuthAccount(
        chatgptAccountId: 'workspace-42',
        email: 'engineer@example.com',
        userId: 'user-42',
        planType: 'pro',
        fedRamp: false,
        expiresAt: DateTime.utc(2026, 9, 22, 1),
        lastRefresh: DateTime.utc(2026, 9, 22),
        state: 'ready',
      );
      Future<void> mount(ProviderAccount account) => tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: SingleChildScrollView(
              child: ProviderAccountTokenDetails(account: account, copy: copy),
            ),
          ),
        ),
      );
      await mount(
        _account(
          info: ProviderTokenInfo(
            email: oauth.email,
            planType: oauth.planType,
            expiresAt: oauth.expiresAt,
            authenticatedAt: DateTime.utc(2026, 9, 20),
            issuedAt: DateTime.utc(2026, 9, 22),
          ),
          oauth: oauth,
        ),
      );
      await tester.tap(
        find.byKey(const Key('provider-account-token-toggle-account.jwt')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Last refreshed'), findsOneWidget);
      expect(
        find.text(copy('provider_accounts.token.not_provided')),
        findsNothing,
      );
      expect(find.text(copy('provider_accounts.token.manual')), findsNothing);
      await mount(_account());
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('provider-account-token-account.jwt')),
        findsNothing,
      );
      expect(tester.takeException(), isNull);
    },
  );
}

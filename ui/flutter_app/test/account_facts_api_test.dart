@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  test(
    'banked reset uses one exact owner action and validates its receipt',
    () async {
      final preview = PreviewControlApi(seedCaptures: false);
      addTearDown(preview.close);
      final claims = base64Url
          .encode(
            utf8.encode(
              jsonEncode({
                'exp':
                    DateTime.now()
                        .toUtc()
                        .add(const Duration(days: 1))
                        .millisecondsSinceEpoch ~/
                    1000,
                'https://api.openai.com/auth': {
                  'chatgpt_account_id': 'workspace-fixture',
                },
              }),
            ),
          )
          .replaceAll('=', '');
      final token = 'eyJhbGciOiJub25lIn0.$claims.fixture';
      final account = await preview.createProviderAccount(
        id: 'account.codex-test',
        displayName: 'Codex test',
        upstreamEndpointId: 'target.codex.official',
        kind: 'codex_oauth',
        secret: '',
        unlinked: true,
        codexAuthJson: jsonEncode({
          'auth_mode': 'chatgpt',
          'OPENAI_API_KEY': null,
          'tokens': {
            'id_token': token,
            'access_token': token,
            'refresh_token': 'synthetic-refresh',
            'account_id': 'workspace-fixture',
          },
          'last_refresh': DateTime.now().toUtc().toIso8601String(),
        }),
        headerPolicy: const ProviderAccountHeaderPolicy(),
      );
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final requests =
          <
            ({
              String method,
              String path,
              String? auth,
              String? match,
              String? key,
              Object? body,
            })
          >[];
      var responseAccount = account.id;
      server.listen((request) async {
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': DateTime.now()
                  .toUtc()
                  .add(const Duration(hours: 1))
                  .toIso8601String(),
            }),
          );
        } else {
          requests.add((
            method: request.method,
            path: request.uri.toString(),
            auth: request.headers.value('authorization'),
            match: request.headers.value('if-match'),
            key: request.headers.value('idempotency-key'),
            body: jsonDecode(await utf8.decoder.bind(request).join()),
          ));
          request.response.write(
            jsonEncode({
              'accountId': responseAccount,
              'credentialEpoch': account.credentialEpoch,
              'creditId': 'credit-fixture',
              'outcome': 'reset',
              'windowsReset': 2,
            }),
          );
        }
        await request.response.close();
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'reset-http-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);
      final result = await api.redeemAccountResetCredit(
        account,
        'credit-fixture',
      );
      expect(result.windowsReset, 2);
      expect(requests, hasLength(1));
      expect(requests.single.method, 'POST');
      expect(
        requests.single.path,
        '/api/v1/provider-accounts/account.codex-test/actions/redeem-reset-credit',
      );
      expect(requests.single.auth, 'Bearer ${'W' * 43}');
      expect(requests.single.match, '${account.revision}');
      expect(requests.single.key, matches(RegExp(r'^[A-Za-z0-9_-]{32}$')));
      expect(requests.single.body, {'creditId': 'credit-fixture'});
      responseAccount = 'another-account';
      await expectLater(
        api.redeemAccountResetCredit(account, 'credit-fixture'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );
  test(
    'explicit account reads use owner scope and keep history distinct',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final requests = <({String method, String uri, String? auth})>[];
      var returnedAccount = 'account.b';
      server.listen((request) async {
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': DateTime.now()
                  .toUtc()
                  .add(const Duration(hours: 1))
                  .toIso8601String(),
            }),
          );
        } else {
          requests.add((
            method: request.method,
            uri: request.uri.toString(),
            auth: request.headers.value('authorization'),
          ));
          request.response.write(
            jsonEncode({
              'accountId': returnedAccount,
              'credentialEpoch': 2,
              'origin': 'https://chatgpt.com',
              'adapterId': 'chatgpt-codex',
              'adapterRevision': 1,
              'observedAt': '2026-09-22T01:00:00Z',
              'state': 'known',
              'limits': <Object>[],
            }),
          );
        }
        await request.response.close();
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'account-facts-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);
      expect((await api.accountFacts('account.b')).credentialEpoch, 2);
      await api.accountFacts('account.b', history: true);
      expect(requests.map((r) => r.method), everyElement('GET'));
      expect(requests.map((r) => r.auth), everyElement('Bearer ${'W' * 43}'));
      expect(requests.map((r) => r.uri), [
        '/api/v1/provider-accounts/account.b/account-facts?kind=quota',
        '/api/v1/provider-accounts/account.b/account-facts?kind=history',
      ]);
      returnedAccount = 'account.a';
      await expectLater(
        api.accountFacts('account.b'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );
}

@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object?> loginFixture() => {
  'id': 'L' * 43,
  'state': 'pending',
  'callbackMode': 'manual',
  'authorizationUrl': Uri.https('auth.openai.com', '/oauth/authorize', {
    'client_id': 'app_EMoamEEZ73f0CkXaXp7hrann',
    'response_type': 'code',
    'state': 'S' * 43,
    'code_challenge': 'C' * 43,
    'code_challenge_method': 'S256',
    'redirect_uri': 'http://localhost:1455/auth/callback',
  }).toString(),
  'expiresAt': DateTime.now()
      .toUtc()
      .add(const Duration(minutes: 15))
      .toIso8601String(),
};

void main() {
  test(
    'Codex login status and mutations all require the write credential',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final requests =
          <
            ({
              String method,
              String path,
              String? auth,
              String? match,
              String body,
            })
          >[];
      final pending = loginFixture();
      final id = pending['id']! as String;
      server.listen((request) async {
        request.response.headers.contentType = ContentType.json;
        final body = await utf8.decoder.bind(request).join();
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
            path: request.uri.path,
            auth: request.headers.value('authorization'),
            match: request.headers.value('if-match'),
            body: body,
          ));
          if (request.method == 'DELETE') {
            request.response.statusCode = 204;
          } else if (request.uri.path.endsWith('/callback')) {
            request.response.write(
              jsonEncode({
                ...pending,
                'state': 'completed',
                'authorizationUrl': '',
                'accountId': 'account.codex.test',
              }),
            );
          } else {
            if (request.method == 'POST') request.response.statusCode = 201;
            request.response.write(jsonEncode(pending));
          }
        }
        await request.response.close();
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'instance-login',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);
      final login = await api.startCodexLogin(
        accountId: 'account.codex.test',
        upstreamEndpointId: 'target.codex.official',
        displayName: '',
        callbackMode: 'manual',
      );
      expect(login.active, isTrue);
      expect((await api.codexLoginStatus(id)).state, 'pending');
      final callback =
          'http://localhost:1455/auth/callback?state=${'S' * 43}&code=synthetic-code';
      expect(
        (await api.completeCodexLogin(id, callback)).accountId,
        'account.codex.test',
      );
      await api.cancelCodexLogin(id);
      expect(requests.map((r) => '${r.method} ${r.path}'), [
        'POST /api/v1/codex-oauth/logins',
        'GET /api/v1/codex-oauth/logins/$id',
        'POST /api/v1/codex-oauth/logins/$id/callback',
        'DELETE /api/v1/codex-oauth/logins/$id',
      ]);
      expect(requests.every((r) => r.auth == 'Bearer ${'W' * 43}'), isTrue);
      expect(requests.map((r) => r.match), ['0', null, '0', '0']);
      expect(jsonDecode(requests.first.body), {
        'accountId': 'account.codex.test',
        'upstreamEndpointId': 'target.codex.official',
        'displayName': '',
        'callbackMode': 'manual',
      });
      expect(jsonDecode(requests[2].body), {'callbackUrl': callback});
      final count = requests.length;
      await expectLater(
        api.codexLoginStatus('../invalid'),
        throwsA(isA<ControlContractException>()),
      );
      expect(requests.length, count);
    },
  );

  test(
    'Codex login DTO rejects foreign auth URLs and accidental token disclosure',
    () {
      final pending = loginFixture();
      expect(CodexLogin.fromJson(pending).active, isTrue);
      for (final url in [
        'javascript:alert(1)',
        'https://example.com/oauth/authorize?code_challenge_method=S256',
        'http://auth.openai.com/oauth/authorize?code_challenge_method=S256',
        'https://user@auth.openai.com/oauth/authorize?code_challenge_method=S256',
        'https://auth.openai.com:444/oauth/authorize?code_challenge_method=S256',
        'https://auth.openai.com/oauth/authorize?code_challenge_method=plain',
      ]) {
        expect(
          () => CodexLogin.fromJson({...pending, 'authorizationUrl': url}),
          throwsA(isA<ControlContractException>()),
        );
      }
      expect(
        () => CodexLogin.fromJson({
          ...pending,
          'access_token': 'synthetic-secret',
        }),
        throwsA(isA<ControlContractException>()),
      );
      expect(
        () => CodexLogin.fromJson({
          ...pending,
          'state': 'completed',
          'accountId': 'account.test',
        }),
        throwsA(isA<ControlContractException>()),
      );
      expect(
        () => CodexLogin.fromJson({
          ...pending,
          'state': 'completed',
          'authorizationUrl': '',
        }),
        throwsA(isA<ControlContractException>()),
      );
    },
  );
}

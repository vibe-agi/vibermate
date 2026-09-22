@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
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

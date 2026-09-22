@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object> _accountJson({
  int epoch = 1,
  String workspace = 'workspace',
}) => {
  'id': 'account.oauth',
  'displayName': 'Managed OAuth',
  'credentialOrigin': 'https://chatgpt.com',
  'linkedEndpointIds': <String>[],
  'associationRevision': 1,
  'kind': 'codex_oauth',
  'realmId': 'openai.chatgpt',
  'state': 'active',
  'revision': 1,
  'credentialState': 'ready',
  'credentialEpoch': epoch,
  'setHeaderNames': <String>[],
  'deleteHeaderNames': <String>[],
  'codexOAuth': {
    'chatgptAccountId': workspace,
    'fedRamp': false,
    'state': 'ready',
    'lastRefresh': '2026-09-22T01:00:00Z',
    'expiresAt': '2026-09-22T02:00:00Z',
  },
};

void main() {
  test(
    'refresh uses owner mutation, pins epoch, and rejects changed identity',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      var workspace = 'workspace';
      var calls = 0;
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
          calls++;
          expect(request.method, 'POST');
          expect(
            request.uri.path,
            '/api/v1/provider-accounts/account.oauth/credential/refresh',
          );
          expect(request.headers.value('authorization'), 'Bearer ${'W' * 43}');
          expect(request.headers.value('if-match'), '1');
          expect(request.headers.value('idempotency-key'), isNotEmpty);
          expect(await utf8.decoder.bind(request).join(), isEmpty);
          request.response.write(
            jsonEncode(_accountJson(epoch: 2, workspace: workspace)),
          );
        }
        await request.response.close();
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'refresh-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
      );
      addTearDown(api.close);
      final account = ProviderAccount.fromJson(_accountJson(), 'account');
      expect(
        (await api.refreshProviderAccountCredential(account)).credentialEpoch,
        2,
      );
      workspace = 'another-workspace';
      await expectLater(
        api.refreshProviderAccountCredential(account),
        throwsA(isA<ControlContractException>()),
      );
      expect(calls, 2);
    },
  );
}

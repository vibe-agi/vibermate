@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  test(
    'header reveal uses write authority, no-store, and exact envelope/name',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final expires = DateTime.now()
          .toUtc()
          .add(const Duration(hours: 1))
          .toIso8601String();
      final writeToken = List.filled(43, 'W').join();
      var calls = 0;
      var cache = 'no-store';
      var returnedEnvelope = 'envelope-1';
      server.listen((request) async {
        request.response.headers.contentType = ContentType.json;
        if (request.uri.path == '/api/v1/auth/sessions/current') {
          await request.drain<void>();
          request.response.write(
            jsonEncode({
              'schema': 'vibermate-app-session-state-v1',
              'revision': 1,
              'expiresAt': expires,
            }),
          );
        } else {
          calls++;
          expect(request.method, 'POST');
          expect(
            request.uri.path,
            '/api/v1/raw-evidence/envelope-1/actions/reveal-header',
          );
          expect(request.headers.value('authorization'), 'Bearer $writeToken');
          expect(jsonDecode(await utf8.decoder.bind(request).join()), {
            'headerName': 'User-Agent',
          });
          request.response.headers.set('cache-control', cache);
          request.response.write(
            jsonEncode({
              'envelopeId': returnedEnvelope,
              'name': 'User-Agent',
              'value': 'private-agent',
            }),
          );
        }
        await request.response.close();
      });
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
          readToken: List.filled(43, 'R').join(),
          writeToken: writeToken,
          instanceId: 'instance-test',
          expiresAt: DateTime.parse(expires),
        ),
      );
      addTearDown(api.close);
      expect(calls, 0);
      expect(
        await api.revealRawHeader(envelopeId: 'envelope-1', name: 'User-Agent'),
        'private-agent',
      );
      cache = 'public';
      await expectLater(
        api.revealRawHeader(envelopeId: 'envelope-1', name: 'User-Agent'),
        throwsA(isA<ControlContractException>()),
      );
      cache = 'no-store';
      returnedEnvelope = 'other-envelope';
      await expectLater(
        api.revealRawHeader(envelopeId: 'envelope-1', name: 'User-Agent'),
        throwsA(isA<ControlContractException>()),
      );
      final before = calls;
      await expectLater(
        api.revealRawHeader(
          envelopeId: 'envelope-1',
          name: 'User-Agent\nInjected',
        ),
        throwsA(isA<ControlContractException>()),
      );
      expect(calls, before);
    },
  );
}

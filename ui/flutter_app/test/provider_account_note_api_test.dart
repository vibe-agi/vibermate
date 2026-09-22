@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

Map<String, Object> _accountJson({String note = '', int noteRevision = 0}) => {
  'id': 'account.notes',
  'displayName': 'Notes fixture',
  'note': note,
  'noteRevision': noteRevision,
  'credentialOrigin': 'https://chatgpt.com',
  'linkedEndpointIds': <String>[],
  'associationRevision': 3,
  'kind': 'bearer_token',
  'realmId': 'openai.chatgpt',
  'state': 'active',
  'revision': 2,
  'credentialState': 'ready',
  'credentialEpoch': 9,
  'setHeaderNames': <String>[],
  'deleteHeaderNames': <String>[],
};

void main() {
  test('note uses its own revision and sends no credential payload', () async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    var calls = 0;
    var wrongIdentity = false;
    var conflict = false;
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
        expect(request.method, 'PUT');
        expect(
          request.uri.path,
          '/api/v1/provider-accounts/account.notes/note',
        );
        expect(request.headers.value('authorization'), 'Bearer ${'W' * 43}');
        expect(request.headers.value('idempotency-key'), isNotEmpty);
        final expected = int.parse(request.headers.value('if-match')!);
        // These are note revisions, not identity (2), links (3), or credentials (9).
        expect(expected, calls == 2 ? 1 : 0);
        final input =
            jsonDecode(await utf8.decoder.bind(request).join()) as Map;
        expect(input.keys.toList(), ['note']);
        expect(input['note'], calls == 2 ? '' : '研发测试');
        if (conflict) {
          request.response.statusCode = 409;
          request.response.write(
            jsonEncode({
              'type': 'about:blank',
              'title': 'Conflict',
              'status': 409,
              'code': 'provider_account_conflict',
              'messageKey': 'error.provider_account_conflict',
            }),
          );
        } else {
          final response = _accountJson(
            note: input['note'] as String,
            noteRevision: expected + 1,
          );
          if (wrongIdentity) {
            response['credentialOrigin'] = 'https://api.openai.com';
          }
          request.response.write(jsonEncode(response));
        }
      }
      await request.response.close();
    });
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'note-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);
    final account = ProviderAccount.fromJson(_accountJson(), 'account');
    final noted = await api.setProviderAccountNote(account, '  研发测试  ');
    expect(noted.note, '研发测试');
    expect(noted.noteRevision, 1);
    expect(noted.revision, 2);
    expect(noted.credentialEpoch, 9);
    expect((await api.setProviderAccountNote(noted, '')).noteRevision, 2);
    wrongIdentity = true;
    await expectLater(
      api.setProviderAccountNote(account, '研发测试'),
      throwsA(isA<ControlContractException>()),
    );
    conflict = true;
    await expectLater(
      api.setProviderAccountNote(account, '研发测试'),
      throwsA(isA<ControlProblem>().having((e) => e.status, 'status', 409)),
    );
    await expectLater(
      api.setProviderAccountNote(account, '备' * 257),
      throwsA(isA<ControlContractException>()),
    );
    expect(calls, 4);
  });

  test('account projection validates notes without exposing credentials', () {
    for (final invalid in [
      _accountJson(note: 'unversioned'),
      _accountJson(note: 'bad\nline', noteRevision: 1),
      _accountJson(note: '备' * 257, noteRevision: 1),
      _accountJson(noteRevision: -1),
    ]) {
      expect(
        () => ProviderAccount.fromJson(invalid, 'account'),
        throwsA(isA<ControlContractException>()),
      );
    }
    final account = ProviderAccount.fromJson(
      _accountJson(note: '📝' * 256, noteRevision: 1),
      'account',
    );
    expect(account.note.runes.length, 256);
    expect(
      account.withAssociations(['target.codex.official'], 4).note,
      account.note,
    );
  });
}

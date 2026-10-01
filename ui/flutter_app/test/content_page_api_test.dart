@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  Future<HttpControlApi> connect(HttpServer server) async {
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'page-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
      inspectSession: false,
    );
    addTearDown(api.close);
    return api;
  }

  test('content pages are explicit scoped reads with byte bounds', () async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    final requests = <Uri>[];
    var owner = 'exchange-page';
    var text = '片段🙂';
    server.listen((request) async {
      requests.add(request.uri);
      expect(request.method, 'GET');
      expect(request.headers.value('authorization'), 'Bearer ${'R' * 43}');
      request.response.headers.contentType = ContentType.json;
      request.response.write(
        jsonEncode({
          'exchangeId': owner,
          'kind': 'text',
          'messages': [],
          'blocks': [],
          'offset': 0,
          'total': utf8.encode(text).length,
          'text': text,
        }),
      );
      await request.response.close();
    });
    final api = await connect(server);
    expect(requests, isEmpty);
    final page = await api.exchangeContentPage('exchange-page', 'cursor');
    expect(page.text, text);
    expect(requests.single.path, '/api/v1/exchanges/exchange-page');
    expect(requests.single.queryParameters, {
      'contentMode': 'paged',
      'contentCursor': 'cursor',
    });
    owner = 'another-exchange';
    await expectLater(
      api.exchangeContentPage('exchange-page', 'cursor'),
      throwsA(isA<ControlContractException>()),
    );
    owner = 'exchange-page';
    text = 'x' * (32 * 1024 + 1);
    await expectLater(
      api.exchangeContentPage('exchange-page', 'cursor'),
      throwsA(isA<ControlContractException>()),
    );
    final before = requests.length;
    await expectLater(
      api.exchangeContentPage('exchange-page', 'bad&cursor'),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests.length, before);
    text = 'x' * (2 * 1024 * 1024);
    await expectLater(
      api.exchangeContentPage('exchange-page', 'cursor'),
      throwsA(isA<ControlContractException>()),
    );
  });

  test(
    'legacy fallback is negotiated once and never bypasses auth failures',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final requests = <Uri>[];
      var reject = 422;
      server.listen((request) async {
        requests.add(request.uri);
        request.response.headers.contentType = ContentType.json;
        if (request.uri.queryParameters.containsKey('contentMode')) {
          final code = reject == 422 ? 'invalid_request' : 'forbidden';
          request.response.statusCode = reject;
          request.response.write(
            jsonEncode({
              'type': 'urn:vibermate:error:${code.replaceAll('_', '-')}',
              'title': 'Rejected',
              'status': reject,
              'code': code,
            }),
          );
        } else {
          request.response.write(
            jsonEncode({
              'id': 'exchange-page',
              'status': 'succeeded',
              'environment': {
                'id': 'work',
                'revision': 1,
                'digest': 'a' * 64,
                'clientEndpointId': 'client',
                'clientEndpointRevision': 1,
                'protocolPlanId': 'plan',
                'protocolPlanRevision': 1,
              },
              'parentRefs': {'exchangeId': 'exchange-page'},
              'processingTrace': {
                'pluginRunIds': [],
                'attempts': [],
                'result': 'succeeded',
              },
              'content': {'state': 'not_recorded'},
            }),
          );
        }
        await request.response.close();
      });
      final api = await connect(server);
      expect((await api.exchange('exchange-page')).id, 'exchange-page');
      expect(requests.length, 2);
      await api.exchange('exchange-page');
      expect(requests.length, 3);
      expect(requests.last.queryParameters.containsKey('contentMode'), isFalse);
      reject = 403;
      final denied = await connect(server);
      await expectLater(
        denied.exchange('exchange-page'),
        throwsA(isA<ControlProblem>()),
      );
      expect(requests.length, 4);
    },
  );

  test('deferred content cannot assert loaded text or another owner', () {
    final deferred = {
      'kind': 'deferred',
      'availability': 'recorded',
      'originalSize': 0,
      'deferred': {
        'exchangeId': 'other',
        'cursor': 'cursor',
        'estimatedBytes': 2048,
      },
    };
    expect(
      () => ExchangeContentBlock.fromJson({
        ...deferred,
        'text': 'not loaded',
      }, 'block'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => ExchangeContentPage.fromJson({
        'exchangeId': 'expected',
        'kind': 'message',
        'messages': [],
        'blocks': [deferred],
        'offset': 0,
        'total': 1,
      }, 'page'),
      throwsA(isA<ControlContractException>()),
    );
  });
}

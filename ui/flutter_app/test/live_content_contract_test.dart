import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  const required = bool.fromEnvironment('VIBERMATE_LIVE_CONTENT_REQUIRED');
  const endpoint = String.fromEnvironment('VIBERMATE_LIVE_CONTENT_ENDPOINT');
  const read = String.fromEnvironment('VIBERMATE_LIVE_CONTENT_READ_TOKEN');
  const write = String.fromEnvironment('VIBERMATE_LIVE_CONTENT_WRITE_TOKEN');
  test(
    'current Runtime signed content round trip and proof rejection',
    () async {
      expect(endpoint, isNotEmpty);
      expect(read, isNotEmpty);
      expect(write, isNotEmpty);
      final client = _ObservedClient();
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse(endpoint),
          readToken: read,
          writeToken: write,
          instanceId: 'live-contract',
          expiresAt: DateTime.now().toUtc().add(const Duration(minutes: 5)),
        ),
        client: client,
        inspectSession: false,
      );
      addTearDown(api.close);
      const id = 'live-content-contract';
      final detail = await api.exchange(id, contentView: 'reading');
      expect(detail.id, id);
      expect(detail.status, 'succeeded');
      expect(detail.content.state, 'recorded');
      expect(
        detail.content.request!.messages.single.blocks.single.text,
        'synthetic question',
      );
      expect(detail.content.response!.blocks.single.text, 'synthetic response');
      final cursor = detail.content.page!.requestNextCursor!;
      expect(cursor.split('.'), hasLength(2));
      expect(cursor.split('.'), everyElement(isNotEmpty));
      final page = await api.exchangeContentPage(id, cursor);
      expect(client.requests.last.queryParameters['contentCursor'], cursor);
      expect(
        client.requests.last.queryParameters.containsKey('contentView'),
        false,
      );
      expect(page.exchangeId, id);
      expect(page.kind, 'request');
      expect(page.offset, 0);
      expect(page.total, 1);
      expect(page.messages.single.blocks.single.text, 'synthetic question');
      // Syntax is still valid: only the server can reject this altered proof.
      final tampered = '${cursor[0] == 'A' ? 'B' : 'A'}${cursor.substring(1)}';
      await expectLater(
        api.exchangeContentPage(id, tampered),
        throwsA(isA<ControlProblem>().having((e) => e.status, 'status', 422)),
      );
      expect(client.requests.last.queryParameters['contentCursor'], tampered);
    },
    skip: !required && endpoint.isEmpty
        ? 'Owned by the enabled Go Runtime/Flutter HTTP contract lane'
        : false,
  );
}

final class _ObservedClient extends http.BaseClient {
  final _inner = http.Client();
  final requests = <Uri>[];

  @override
  Future<http.StreamedResponse> send(http.BaseRequest request) {
    requests.add(request.url);
    return _inner.send(request);
  }

  @override
  void close() => _inner.close();
}

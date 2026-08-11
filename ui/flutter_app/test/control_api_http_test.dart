import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  test('HTTP API reads one exact Environment revision authority', () async {
    final preview = PreviewControlApi();
    addTearDown(preview.close);
    final work = (await preview.loadDashboard()).environments.firstWhere(
      (environment) => environment.id == 'work',
    );
    final requests = <Uri>[];
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    server.listen((request) async {
      requests.add(request.uri);
      await request.drain<void>();
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
      } else if (request.uri.path.startsWith(
        '/api/v1/environments/work/revisions/',
      )) {
        request.response.write(jsonEncode(work.toJson()));
      } else {
        request.response.statusCode = HttpStatus.notFound;
      }
      await request.response.close();
    });

    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
        readToken: List.filled(43, 'R').join(),
        writeToken: List.filled(43, 'W').join(),
        instanceId: 'instance-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
    );
    addTearDown(api.close);

    final historical = await api.environmentRevision('work', work.revision);
    expect(historical.id, 'work');
    expect(historical.revision, work.revision);
    expect(
      requests.last.toString(),
      '/api/v1/environments/work/revisions/${work.revision}',
    );

    await expectLater(
      api.environmentRevision('work', work.revision + 1),
      throwsA(isA<ControlContractException>()),
    );
    final requestCount = requests.length;
    await expectLater(
      api.environmentRevision('../work', work.revision),
      throwsA(isA<ControlContractException>()),
    );
    await expectLater(
      api.environmentRevision('work', 0),
      throwsA(isA<ControlContractException>()),
    );
    expect(requests, hasLength(requestCount));
  });
}

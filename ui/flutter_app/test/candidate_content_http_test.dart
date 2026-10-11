import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  const endpoint = String.fromEnvironment('TASK5B_HTTP_ENDPOINT');
  test(
    'candidate deep response and next history navigate as strings and bytes',
    () async {
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse(endpoint),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'task5b-candidate',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        inspectSession: false,
      );
      addTearDown(api.close);
      final expected = '{"a":${'[' * 9999}0${']' * 9999}}';
      for (final id in ['deep-response', 'deep-next-history']) {
        final detail = await api.exchange(id, contentView: 'full');
        final block = id == 'deep-response'
            ? detail.content.response!.blocks.single
            : detail.content.request!.messages[1].blocks.single;
        var cursor = block.deferred!.cursor;
        String? exact;
        final arguments = StringBuffer();
        while (cursor.isNotEmpty) {
          final page = await api.exchangeContentPage(id, cursor);
          if (page.blocks.isNotEmpty && page.blocks.first.deferred != null) {
            cursor = page.blocks.first.deferred!.cursor;
            continue;
          }
          arguments.write(page.text);
          exact ??= page.canonicalCursor;
          cursor = page.nextCursor ?? '';
        }
        expect(arguments.toString(), expected);
        expect(exact, isNotNull);
        final bytes = <int>[];
        cursor = exact!;
        while (cursor.isNotEmpty) {
          final page = await api.exchangeContentPage(id, cursor);
          expect(page.kind, 'block_bytes');
          expect(page.offset, bytes.length);
          bytes.addAll(page.data);
          cursor = page.nextCursor ?? '';
        }
        expect(utf8.decode(bytes), contains('"arguments":$expected'));
      }
    },
    skip: endpoint.isEmpty
        ? 'Requires the Go-produced HTTP fixture server'
        : false,
  );
}

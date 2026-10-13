@TestOn('browser')
library;

import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:ui' as ui;
import 'package:flutter/foundation.dart' show debugPrintSynchronously;
import 'package:flutter_test/flutter_test.dart';
import 'package:web/web.dart' as web;

void main() {
  for (final name in ['ink_sparkle.frag', 'stretch_effect.frag']) {
    testWidgets('owned Chrome origin serves usable framework shader $name', (
      tester,
    ) async {
      await tester.runAsync(() async {
        final page = Uri.base;
        final asset = Uri.parse(
          web.document.baseURI,
        ).resolve('assets/shaders/$name');
        expect(page.scheme, 'http');
        expect(['localhost', '127.0.0.1', '::1'], contains(page.host));
        expect(page.port, isNot(anyOf(9666, 18662)));
        expect(asset.origin, page.origin);
        expect(asset.userInfo, isEmpty);
        expect(asset.hasQuery || asset.hasFragment, isFalse);
        final abort = web.AbortController();
        final timer = Timer(const Duration(seconds: 5), () => abort.abort());
        web.ReadableStreamDefaultReader? reader;
        try {
          await (() async {
            final response = await web.window
                .fetch(
                  asset.toString().toJS,
                  web.RequestInit(
                    method: 'GET',
                    credentials: 'omit',
                    redirect: 'error',
                    signal: abort.signal,
                  ),
                )
                .toDart;
            debugPrintSynchronously(
              'WEB_SHADER asset=${asset.origin}${asset.path} status=${response.status}',
            );
            expect(response.status, 200);
            expect(response.body, isNotNull);
            reader =
                response.body!.getReader() as web.ReadableStreamDefaultReader;
            final bytes = <int>[];
            while (true) {
              final chunk = await reader!.read().toDart;
              if (chunk.done) break;
              final data = (chunk.value as JSUint8Array).toDart;
              expect(
                bytes.length + data.length,
                lessThanOrEqualTo(1024 * 1024),
              );
              bytes.addAll(data);
            }
            final json = jsonDecode(utf8.decode(bytes)) as Map<String, dynamic>;
            final sksl = json['sksl'] as Map<String, dynamic>;
            expect(
              sksl['shader'],
              isA<String>().having((s) => s.isNotEmpty, 'nonempty', isTrue),
            );
            expect(sksl['uniforms'], isA<List>());
            final program = await ui.FragmentProgram.fromAsset('shaders/$name');
            final shader = program.fragmentShader();
            shader.dispose();
            debugPrintSynchronously(
              'WEB_SHADER bytes=${bytes.length} web_json=true fragment_program=true',
            );
          })().timeout(const Duration(seconds: 5));
        } finally {
          timer.cancel();
          abort.abort();
          if (reader != null) {
            await reader!.cancel().toDart.timeout(const Duration(seconds: 1));
            reader!.releaseLock();
          }
        }
      });
      expect(tester.takeException(), isNull);
    });
  }
}

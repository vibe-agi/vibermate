import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/product_update_api.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

final class _UpdateApi implements ControlApi {
  int checks = 0;
  @override
  Future<ProductUpdateMetadata> checkProductUpdate() async {
    checks++;
    return ProductUpdateMetadata(
      checkedAt: DateTime.utc(2026, 10, 5),
      tag: 'v9.8.7',
      name: 'Private release',
      url: Uri.parse(
        'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
      ),
      publishedAt: DateTime.utc(2026, 10, 4),
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

void main() {
  test(
    'Strict update DTO rejects malformed metadata and never invents a URL or date',
    () {
      Map<String, Object?> valid() => {
        'checkedAt': '2026-10-05T00:00:00Z',
        'release': <String, Object?>{
          'tag': 'v9.8.7',
          'name': 'Private release',
          'url': 'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
          'publishedAt': '2026-10-04T00:00:00Z',
        },
      };
      for (final edge in [
        'missing',
        'extra',
        'null-release',
        'alias',
        'bad-version',
        'overflow',
        'url-userinfo',
        'url-port',
        'url-query',
        'url-fragment',
        'bad-date',
        'name-null',
        'name-long',
        'name-control',
        'name-c1-control',
      ]) {
        final value = valid();
        final release = value['release'] as Map<String, Object?>;
        switch (edge) {
          case 'missing':
            value.remove('checkedAt');
          case 'extra':
            value['unexpected'] = true;
          case 'null-release':
            value['release'] = null;
          case 'alias':
            release['Tag'] = release.remove('tag');
          case 'bad-version':
            release['tag'] = 'v9.8.7-beta';
          case 'overflow':
            release['tag'] = 'v2147483648.0.1';
          case 'url-userinfo':
            release['url'] =
                'https://user@github.com/vibe-agi/vibermate/releases/tag/v9.8.7';
          case 'url-port':
            release['url'] =
                'https://github.com:443/vibe-agi/vibermate/releases/tag/v9.8.7';
          case 'url-query':
            release['url'] = '${release['url']}?x=1';
          case 'url-fragment':
            release['url'] = '${release['url']}#x';
          case 'bad-date':
            release['publishedAt'] = '2026-02-30T00:00:00Z';
          case 'name-null':
            release['name'] = null;
          case 'name-long':
            release['name'] = List.filled(257, 'x').join();
          case 'name-control':
            release['name'] = 'bad\n';
          case 'name-c1-control':
            release['name'] = 'bad\u0085';
        }
        expect(
          () => ProductUpdateMetadata.fromJson(value),
          throwsA(isA<ControlContractException>()),
          reason: edge,
        );
      }
      final absentName = valid();
      (absentName['release'] as Map<String, Object?>)['name'] = '';
      expect(ProductUpdateMetadata.fromJson(absentName).name, '');
    },
  );
  test(
    'Connected local and remote Runtime errors never revive host GitHub HTTP',
    () async {
      for (final base in [
        'http://127.0.0.1:18662',
        'https://private-runtime.invalid:8443',
      ]) {
        for (final status in [200, 404, 401, 503]) {
          var commands = 0, external = 0;
          final api = await HttpControlApi.connect(
            DesktopSession(
              instanceId: 'update-target',
              baseUrl: Uri.parse(base),
              readToken: List.filled(43, 'R').join(),
              writeToken: List.filled(43, 'W').join(),
              expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
            ),
            inspectSession: false,
            client: MockClient((request) async {
              commands++;
              expect(
                request.url.toString(),
                '$base/api/v1/product-update/check',
              );
              expect(request.method, 'POST');
              expect(request.body, '{}');
              expect(
                request.headers['Authorization'],
                "Bearer ${List.filled(43, 'W').join()}",
              );
              return http.Response(
                status == 200
                    ? jsonEncode({
                        'checkedAt': '2026-10-05T00:00:00Z',
                        'release': {
                          'tag': 'v9.8.7',
                          'name': '',
                          'url':
                              'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
                          'publishedAt': '2026-10-04T00:00:00Z',
                        },
                      })
                    : '{}',
                status,
                headers: {'content-type': 'application/json'},
              );
            }),
          );
          try {
            await http.runWithClient(
              () async {
                final result = await ControlProductUpdateService(
                  api: api,
                  channelResolver: () async => ProductInstallChannel.homebrew,
                ).check();
                expect(
                  result.state,
                  status == 200
                      ? ProductUpdateState.available
                      : ProductUpdateState.unavailable,
                );
                expect(result.channel, ProductInstallChannel.homebrew);
                expect(commands, 1);
                expect(external, 0);
                if (status != 200) expect(result.releaseUrl, isNull);
              },
              () => MockClient((_) async {
                external++;
                throw StateError('host HTTP denied');
              }),
            );
          } finally {
            api.close();
          }
        }
      }
    },
  );
  test(
    'HttpControlApi performs only the fixed connected Runtime update command',
    () async {
      var commands = 0;
      final api = await HttpControlApi.connect(
        DesktopSession(
          instanceId: 'update-runtime',
          baseUrl: Uri.parse('http://127.0.0.1:18662'),
          readToken: List.filled(43, 'R').join(),
          writeToken: List.filled(43, 'W').join(),
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        inspectSession: false,
        client: MockClient((request) async {
          commands++;
          expect(
            request.url.toString(),
            'http://127.0.0.1:18662/api/v1/product-update/check',
          );
          expect(request.method, 'POST');
          expect(
            request.headers['Authorization'],
            "Bearer ${List.filled(43, 'W').join()}",
          );
          expect(request.body, '{}');
          return http.Response(
            jsonEncode({
              'checkedAt': '2026-10-05T00:00:00Z',
              'release': {
                'tag': 'v9.8.7',
                'name': 'Private release',
                'url':
                    'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
                'publishedAt': '2026-10-04T00:00:00Z',
              },
            }),
            200,
            headers: {'content-type': 'application/json'},
          );
        }),
      );
      try {
        final result = await api.checkProductUpdate();
        expect(commands, 1);
        expect(result.tag, 'v9.8.7');
        expect(result.checkedAt, DateTime.utc(2026, 10, 5));
      } finally {
        api.close();
      }
    },
  );
  test(
    'Actual default workbench update uses connected ControlApi, never host HTTP',
    () async {
      final api = _UpdateApi();
      var external = 0;
      await http.runWithClient(
        () async {
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: false,
            closeRuntime: () async {},
          );
          try {
            await controller.checkProductUpdate();
            expect(api.checks, 1);
            expect(external, 0);
            expect(controller.productUpdate?.availableVersion, 'v9.8.7');
          } finally {
            controller.dispose();
          }
        },
        () => MockClient((_) async {
          external++;
          throw StateError('Contained fixture denied UI external HTTP');
        }),
      );
    },
  );
}

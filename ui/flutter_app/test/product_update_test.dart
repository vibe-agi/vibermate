import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/product_update_api.dart';

void main() {
  test(
    'release check is bounded, strict, and reports an available version',
    () async {
      final service = GitHubProductUpdateService(
        clientFactory: () => MockClient((request) async {
          expect(
            request.url.toString(),
            'https://api.github.com/repos/vibe-agi/vibermate/releases/latest',
          );
          return http.Response(
            jsonEncode({
              'tag_name': 'v9.8.7',
              'name': 'ViberMate 9.8.7',
              'html_url':
                  'https://github.com/vibe-agi/vibermate/releases/tag/v9.8.7',
              'published_at': '2026-09-26T00:00:00Z',
              'draft': false,
              'prerelease': false,
            }),
            200,
          );
        }),
        channelResolver: () async => ProductInstallChannel.homebrew,
        clock: () => DateTime.utc(2026, 9, 26, 1),
      );
      final result = await service.check();
      expect(result.state, ProductUpdateState.available);
      expect(result.channel, ProductInstallChannel.homebrew);
      expect(result.availableVersion, 'v9.8.7');
      expect(result.releaseName, 'ViberMate 9.8.7');
      expect(result.checkedAt, DateTime.utc(2026, 9, 26, 1));
    },
  );

  test(
    'network and untrusted release responses degrade without throwing',
    () async {
      for (final response in [
        http.Response('offline', 503),
        http.Response('x' * (300 * 1024), 200),
        http.Response(
          jsonEncode({
            'tag_name': 'v9.8.7',
            'name': 'ViberMate 9.8.7',
            'html_url': 'https://example.com/untrusted',
            'published_at': '2026-09-26T00:00:00Z',
            'draft': false,
            'prerelease': false,
          }),
          200,
        ),
      ]) {
        final result = await GitHubProductUpdateService(
          clientFactory: () => MockClient((_) async => response),
          channelResolver: () async => ProductInstallChannel.manual,
        ).check();
        expect(result.state, ProductUpdateState.unavailable);
        expect(result.availableVersion, isNull);
      }
    },
  );

  test('the published current version is not reported as an update', () async {
    final result = await GitHubProductUpdateService(
      clientFactory: () => MockClient(
        (_) async => http.Response(
          jsonEncode({
            'tag_name': 'v0.1.13',
            'name': 'ViberMate 0.1.13',
            'html_url':
                'https://github.com/vibe-agi/vibermate/releases/tag/v0.1.13',
            'published_at': '2026-09-22T00:00:00Z',
            'draft': false,
            'prerelease': false,
          }),
          200,
        ),
      ),
      channelResolver: () async => ProductInstallChannel.manual,
    ).check();
    expect(result.state, ProductUpdateState.current);
  });

  test('semantic release comparison does not use lexical ordering', () {
    expect(
      compareProductVersions(
        parseProductVersion('v0.10.0')!,
        parseProductVersion('0.9.9')!,
      ),
      greaterThan(0),
    );
    expect(parseProductVersion('latest'), isNull);
  });
}

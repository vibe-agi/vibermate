import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/product_update_api.dart';

class _ReleaseApi implements ControlApi {
  _ReleaseApi({this.tag = 'v9.8.7', this.failure});
  final String tag;
  final Object? failure;
  int checks = 0;
  @override
  Future<ProductUpdateMetadata> checkProductUpdate() async {
    checks++;
    if (failure != null) throw failure!;
    return ProductUpdateMetadata(
      checkedAt: DateTime.utc(2026, 9, 26, 1),
      tag: tag,
      name: 'ViberMate release',
      url: Uri.parse('https://github.com/vibe-agi/vibermate/releases/tag/$tag'),
      publishedAt: DateTime.utc(2026, 9, 26),
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

void main() {
  test(
    'Runtime release metadata compares locally and preserves local install channel',
    () async {
      final api = _ReleaseApi();
      final service = ControlProductUpdateService(
        api: api,
        channelResolver: () async => ProductInstallChannel.homebrew,
        clock: () => DateTime.utc(2026, 9, 26, 2),
      );
      final result = await service.check();
      expect(result.state, ProductUpdateState.available);
      expect(result.channel, ProductInstallChannel.homebrew);
      expect(result.availableVersion, 'v9.8.7');
      expect(result.releaseName, 'ViberMate release');
      expect(result.checkedAt, DateTime.utc(2026, 9, 26, 1));
      expect(api.checks, 1);
    },
  );
  test(
    'Unavailable Runtime never falls back, and local channel remains independent',
    () async {
      for (final failure in [
        StateError('old server 404'),
        StateError('owner unauthorized'),
        StateError('Runtime offline'),
      ]) {
        final api = _ReleaseApi(failure: failure);
        final result = await ControlProductUpdateService(
          api: api,
          channelResolver: () async => ProductInstallChannel.manual,
        ).check();
        expect(result.state, ProductUpdateState.unavailable);
        expect(result.availableVersion, isNull);
        expect(api.checks, 1);
      }
    },
  );
  test('Older release is not reported as an update', () async {
    final result = await ControlProductUpdateService(
      api: _ReleaseApi(tag: 'v0.1.13'),
      channelResolver: () async => ProductInstallChannel.manual,
    ).check();
    expect(result.state, ProductUpdateState.current);
  });
  test('Semantic comparison and bounded version grammar remain local', () {
    expect(
      compareProductVersions(
        parseProductVersion('v0.10.0')!,
        parseProductVersion('0.9.9')!,
      ),
      greaterThan(0),
    );
    for (final tag in ['latest', 'v1.2.3-beta', 'v2147483648.0.1']) {
      expect(parseProductVersion(tag), isNull);
    }
  });
}

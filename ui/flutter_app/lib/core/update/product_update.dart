import 'dart:convert';
import 'dart:typed_data';

import 'package:http/http.dart' as http;

import 'install_channel.dart';
import 'product_version.dart';

export 'install_channel_types.dart';

enum ProductUpdateState { current, available, unavailable }

final class ProductUpdateResult {
  const ProductUpdateResult({
    required this.state,
    required this.channel,
    required this.checkedAt,
    this.availableVersion,
    this.releaseName,
    this.releaseUrl,
    this.publishedAt,
  });

  final ProductUpdateState state;
  final ProductInstallChannel channel;
  final DateTime checkedAt;
  final String? availableVersion;
  final String? releaseName;
  final Uri? releaseUrl;
  final DateTime? publishedAt;
}

abstract interface class ProductUpdateService {
  Future<ProductUpdateResult> check();
}

typedef ProductUpdateClientFactory = http.Client Function();
typedef ProductInstallChannelResolver =
    Future<ProductInstallChannel> Function();

final class GitHubProductUpdateService implements ProductUpdateService {
  GitHubProductUpdateService({
    ProductUpdateClientFactory? clientFactory,
    ProductInstallChannelResolver? channelResolver,
    DateTime Function()? clock,
  }) : _clientFactory = clientFactory ?? http.Client.new,
       _channelResolver = channelResolver ?? detectProductInstallChannel,
       _clock = clock ?? DateTime.now;

  static final _latest = Uri.https(
    'api.github.com',
    '/repos/vibe-agi/vibermate/releases/latest',
  );
  static const _maximumResponseBytes = 256 * 1024;
  static const _timeout = Duration(seconds: 8);

  final ProductUpdateClientFactory _clientFactory;
  final ProductInstallChannelResolver _channelResolver;
  final DateTime Function() _clock;

  @override
  Future<ProductUpdateResult> check() async {
    final checkedAt = _clock().toUtc();
    ProductInstallChannel channel;
    try {
      channel = await _channelResolver();
    } on Object {
      channel = ProductInstallChannel.manual;
    }
    final client = _clientFactory();
    try {
      final request = http.Request('GET', _latest)
        ..followRedirects = false
        ..headers['Accept'] = 'application/vnd.github+json'
        ..headers['X-GitHub-Api-Version'] = '2022-11-28';
      final response = await client.send(request).timeout(_timeout);
      if (response.statusCode != 200 ||
          (response.contentLength ?? 0) > _maximumResponseBytes) {
        throw const FormatException();
      }
      final bytes = BytesBuilder(copy: false);
      await for (final chunk in response.stream.timeout(_timeout)) {
        if (bytes.length + chunk.length > _maximumResponseBytes) {
          throw const FormatException();
        }
        bytes.add(chunk);
      }
      final value = jsonDecode(
        const Utf8Decoder(allowMalformed: false).convert(bytes.takeBytes()),
      );
      if (value is! Map ||
          value['draft'] != false ||
          value['prerelease'] != false) {
        throw const FormatException();
      }
      final tag = value['tag_name'];
      final url = value['html_url'];
      final published = value['published_at'];
      final name = value['name'];
      final available = tag is String ? parseProductVersion(tag) : null;
      final current = parseProductVersion(productVersionLabel);
      final releaseUrl = url is String ? Uri.tryParse(url) : null;
      final publishedAt = published is String
          ? DateTime.tryParse(published)?.toUtc()
          : null;
      if (available == null ||
          current == null ||
          releaseUrl == null ||
          releaseUrl.scheme != 'https' ||
          releaseUrl.host != 'github.com' ||
          releaseUrl.path != '/vibe-agi/vibermate/releases/tag/$tag' ||
          publishedAt == null ||
          (name != null &&
              (name is! String ||
                  name.isEmpty ||
                  name.length > 256 ||
                  name.contains(RegExp(r'[\x00-\x1f\x7f]'))))) {
        throw const FormatException();
      }
      return ProductUpdateResult(
        state: compareProductVersions(available, current) > 0
            ? ProductUpdateState.available
            : ProductUpdateState.current,
        channel: channel,
        checkedAt: checkedAt,
        availableVersion: tag,
        releaseName: name as String?,
        releaseUrl: releaseUrl,
        publishedAt: publishedAt,
      );
    } on Object {
      return ProductUpdateResult(
        state: ProductUpdateState.unavailable,
        channel: channel,
        checkedAt: checkedAt,
      );
    } finally {
      client.close();
    }
  }
}

List<int>? parseProductVersion(String value) {
  final match = RegExp(r'^v?([0-9]+)\.([0-9]+)\.([0-9]+)$').firstMatch(value);
  if (match == null) return null;
  final parts = [
    for (var index = 1; index <= 3; index++) int.tryParse(match.group(index)!),
  ];
  if (parts.any((part) => part == null || part > 2147483647)) return null;
  return parts.cast<int>();
}

int compareProductVersions(List<int> left, List<int> right) {
  for (var index = 0; index < 3; index++) {
    final compared = left[index].compareTo(right[index]);
    if (compared != 0) return compared;
  }
  return 0;
}

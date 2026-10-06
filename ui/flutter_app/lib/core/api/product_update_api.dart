import '../bootstrap/product_install_channel.dart';
import '../update/product_version.dart';
import 'control_api.dart';

export '../bootstrap/product_install_channel_contract.dart';

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

typedef ProductInstallChannelResolver =
    Future<ProductInstallChannel> Function();

// Only the connected Runtime performs the fixed release query. Host HTTP is
// deliberately absent: unavailable/old servers cannot revive a direct client.
final class ControlProductUpdateService implements ProductUpdateService {
  ControlProductUpdateService({
    required ControlApi api,
    ProductInstallChannelResolver? channelResolver,
    DateTime Function()? clock,
  }) : _api = api,
       _channelResolver = channelResolver ?? detectProductInstallChannel,
       _clock = clock ?? DateTime.now;
  final ControlApi _api;
  final ProductInstallChannelResolver _channelResolver;
  final DateTime Function() _clock;
  @override
  Future<ProductUpdateResult> check() async {
    final started = _clock().toUtc();
    ProductInstallChannel channel;
    try {
      channel = await _channelResolver();
    } on Object {
      channel = ProductInstallChannel.manual;
    }
    try {
      final metadata = await _api.checkProductUpdate();
      final available = parseProductVersion(metadata.tag);
      final current = parseProductVersion(productVersionLabel);
      if (available == null ||
          current == null ||
          metadata.url.toString() !=
              'https://github.com/vibe-agi/vibermate/releases/tag/${metadata.tag}') {
        throw const FormatException();
      }
      return ProductUpdateResult(
        state: compareProductVersions(available, current) > 0
            ? ProductUpdateState.available
            : ProductUpdateState.current,
        channel: channel,
        checkedAt: metadata.checkedAt,
        availableVersion: metadata.tag,
        releaseName: metadata.name.isEmpty ? null : metadata.name,
        releaseUrl: metadata.url,
        publishedAt: metadata.publishedAt,
      );
    } on Object {
      return ProductUpdateResult(
        state: ProductUpdateState.unavailable,
        channel: channel,
        checkedAt: started,
      );
    }
  }
}

List<int>? parseProductVersion(String value) {
  final match = RegExp(r'^v?([0-9]+)\.([0-9]+)\.([0-9]+)$').firstMatch(value);
  if (match == null) return null;
  final parts = [for (var i = 1; i <= 3; i++) int.tryParse(match.group(i)!)];
  if (parts.any((v) => v == null || v > 2147483647)) return null;
  return parts.cast<int>();
}

int compareProductVersions(List<int> left, List<int> right) {
  for (var i = 0; i < 3; i++) {
    final c = left[i].compareTo(right[i]);
    if (c != 0) return c;
  }
  return 0;
}

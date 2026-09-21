import '../../core/api/control_models.dart';

enum SettingsDestination { preferences, access, users, safety, networkExits }

enum ServerConnectionSecurity { unavailable, loopbackHttp, remoteHttp, https }

/// Read-only setup projection, shared by the guide and the security summary.
/// An address describes how to connect; it never proves certificate trust or
/// that the server is not also listening on another interface.
final class RuntimeConnectionGuide {
  RuntimeConnectionGuide({
    required String connectedTarget,
    RuntimeServerAccess? advertised,
  }) {
    final connected = _httpOrigin(connectedTarget);
    if (connected != null) {
      address = connected;
      usesConnectedOrigin = true;
    } else {
      address = advertised == null
          ? null
          : _httpOrigin(
              '${advertised.transport}://${advertised.preferredTarget}',
            );
      usesConnectedOrigin = false;
    }
  }

  late final Uri? address;
  late final bool usesConnectedOrigin;

  bool get available => address != null;
  String? get serverURL => address?.origin;
  String? get webURL => address == null ? null : '${address!.origin}/';

  ServerConnectionSecurity get security {
    final uri = address;
    if (uri == null) return ServerConnectionSecurity.unavailable;
    if (uri.scheme == 'https') return ServerConnectionSecurity.https;
    return _isLoopback(uri.host)
        ? ServerConnectionSecurity.loopbackHttp
        : ServerConnectionSecurity.remoteHttp;
  }

  static Uri? _httpOrigin(String value) {
    try {
      final uri = Uri.tryParse(value);
      if (uri == null ||
          (uri.scheme != 'http' && uri.scheme != 'https') ||
          uri.host.isEmpty ||
          uri.userInfo.isNotEmpty ||
          uri.port < 1 ||
          uri.port > 65535) {
        return null;
      }
      return Uri.parse(uri.origin);
    } on FormatException {
      return null;
    }
  }

  static bool _isLoopback(String host) {
    if (host == 'localhost' || host == '::1' || host == '[::1]') return true;
    final parts = host.split('.');
    return parts.length == 4 &&
        parts.first == '127' &&
        parts.every((part) {
          final value = int.tryParse(part);
          return value != null && value >= 0 && value <= 255;
        });
  }
}

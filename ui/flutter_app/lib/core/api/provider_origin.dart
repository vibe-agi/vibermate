import 'dart:convert';
import 'dart:io';

/// Mirrors the canonical syntax of the daemon's ProviderOrigin boundary.
///
/// Cleartext DNS names are accepted here because the daemon resolves and
/// verifies the actual peer is local/private before any request bytes are
/// written. The daemon remains the authority for that network check.
bool isCanonicalProviderOrigin(String value) {
  if (value.isEmpty ||
      value.trim() != value ||
      utf8.encode(value).length > 2048 ||
      value.contains(RegExp(r'[\u0000-\u001f\u007f\\?#%]'))) {
    return false;
  }
  final parsed = Uri.tryParse(value);
  if (parsed == null ||
      !const {'http', 'https'}.contains(parsed.scheme) ||
      parsed.host.isEmpty ||
      parsed.userInfo.isNotEmpty ||
      parsed.hasQuery ||
      parsed.hasFragment ||
      parsed.host.endsWith('.') ||
      parsed.host.contains('%')) {
    return false;
  }
  if (parsed.hasPort && parsed.port == (parsed.scheme == 'https' ? 443 : 80)) {
    return false;
  }
  if (parsed.path == '/' ||
      (parsed.path.isNotEmpty &&
          (parsed.path.endsWith('/') ||
              parsed.normalizePath().path != parsed.path))) {
    return false;
  }
  if (!parsed.host.contains(':')) {
    final labels = parsed.host.split('.');
    if (labels.any(
      (label) =>
          label.isEmpty ||
          label.length > 63 ||
          label.startsWith('-') ||
          label.endsWith('-') ||
          !RegExp(r'^[a-z0-9-]+$').hasMatch(label),
    )) {
      return false;
    }
  }
  if (parsed.scheme == 'http') {
    final address = InternetAddress.tryParse(parsed.host);
    if (address != null && !_isPrivateCleartextAddress(address)) {
      return false;
    }
  }
  return parsed.toString() == value;
}

bool isCleartextProviderOrigin(String value) =>
    Uri.tryParse(value)?.scheme == 'http';

bool _isPrivateCleartextAddress(InternetAddress address) {
  if (address.isLoopback || address.isLinkLocal) return true;
  final bytes = address.rawAddress;
  if (address.type == InternetAddressType.IPv4) {
    return bytes[0] == 10 ||
        (bytes[0] == 172 && bytes[1] >= 16 && bytes[1] <= 31) ||
        (bytes[0] == 192 && bytes[1] == 168) ||
        (bytes[0] == 100 && bytes[1] >= 64 && bytes[1] <= 127);
  }
  return bytes.isNotEmpty && (bytes[0] & 0xfe) == 0xfc;
}

import 'dart:convert';

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
    final privateLiteral = _privateCleartextLiteral(parsed.host);
    if (privateLiteral == false) {
      return false;
    }
  }
  return parsed.toString() == value;
}

bool isCleartextProviderOrigin(String value) =>
    Uri.tryParse(value)?.scheme == 'http';

// Returns null for a DNS name, true for a private/local literal, and false for
// a public or malformed IP literal. Peer resolution remains a daemon concern.
bool? _privateCleartextLiteral(String host) {
  final ipv4 = _parseIPv4(host);
  if (ipv4 != null) {
    return ipv4[0] == 10 ||
        ipv4[0] == 127 ||
        (ipv4[0] == 169 && ipv4[1] == 254) ||
        (ipv4[0] == 172 && ipv4[1] >= 16 && ipv4[1] <= 31) ||
        (ipv4[0] == 192 && ipv4[1] == 168) ||
        (ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127);
  }
  if (RegExp(r'^[0-9.]+$').hasMatch(host)) {
    return false;
  }
  if (!host.contains(':')) {
    return null;
  }
  final ipv6 = _parseIPv6(host);
  if (ipv6 == null) {
    return false;
  }
  final loopback = ipv6.take(15).every((byte) => byte == 0) && ipv6[15] == 1;
  return loopback ||
      (ipv6[0] & 0xfe) == 0xfc ||
      (ipv6[0] == 0xfe && (ipv6[1] & 0xc0) == 0x80);
}

List<int>? _parseIPv4(String value) {
  final parts = value.split('.');
  if (parts.length != 4) return null;
  final bytes = <int>[];
  for (final part in parts) {
    if (part.isEmpty ||
        (part.length > 1 && part.startsWith('0')) ||
        !RegExp(r'^[0-9]+$').hasMatch(part)) {
      return null;
    }
    final byte = int.tryParse(part);
    if (byte == null || byte > 255) return null;
    bytes.add(byte);
  }
  return bytes;
}

List<int>? _parseIPv6(String value) {
  if (value.isEmpty || value.indexOf('::') != value.lastIndexOf('::')) {
    return null;
  }
  final compressed = value.contains('::');
  final halves = value.split('::');
  if (halves.length > 2) return null;
  final left = halves.first.isEmpty ? <String>[] : halves.first.split(':');
  final right = !compressed || halves.last.isEmpty
      ? <String>[]
      : halves.last.split(':');
  final words = <int>[];

  bool appendWords(List<String> parts) {
    for (var index = 0; index < parts.length; index += 1) {
      final part = parts[index];
      if (part.contains('.')) {
        if (index != parts.length - 1) return false;
        final ipv4 = _parseIPv4(part);
        if (ipv4 == null) return false;
        words
          ..add((ipv4[0] << 8) | ipv4[1])
          ..add((ipv4[2] << 8) | ipv4[3]);
        continue;
      }
      if (part.isEmpty ||
          part.length > 4 ||
          !RegExp(r'^[0-9a-fA-F]+$').hasMatch(part)) {
        return false;
      }
      words.add(int.parse(part, radix: 16));
    }
    return true;
  }

  if (!appendWords(left)) return null;
  final leftCount = words.length;
  if (!appendWords(right)) return null;
  final missing = 8 - words.length;
  if ((!compressed && missing != 0) || (compressed && missing < 1)) {
    return null;
  }
  if (compressed) {
    words.insertAll(leftCount, List<int>.filled(missing, 0));
  }
  if (words.length != 8) return null;
  return [
    for (final word in words) ...[word >> 8, word & 0xff],
  ];
}

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import '../api/control_api.dart';
import '../api/control_models.dart';

final class DesktopRuntimeException implements Exception {
  const DesktopRuntimeException(this.message, {this.reason});

  final String message;
  final String? reason;

  @override
  String toString() => reason == null ? message : '$message ($reason)';
}

final class DesktopRuntime {
  DesktopRuntime._({required this.api, required Process daemon})
    : _daemon = daemon;

  static const _maximumBootstrapBytes = 16 * 1024;
  static const _flutterOrigin = 'vibermate://desktop';
  static const applicationId = 'io.vibermate.desktop';

  final ControlApi api;
  final Process _daemon;
  bool _closed = false;

  bool get isClosed => _closed;

  int get daemonPid => _daemon.pid;

  Future<int> get exitCode => _daemon.exitCode;

  static Future<DesktopRuntime> start({
    String? daemonPath,
    String? cacheDirectory,
    String? dataDirectory,
    String? homeDirectory,
  }) async {
    final executable = await _resolveDaemonPath(daemonPath);
    final paths = await _runtimePaths(
      cacheDirectory: cacheDirectory,
      dataDirectory: dataDirectory,
      homeDirectory: homeDirectory,
    );
    final process = await Process.start(
      executable,
      [
        '--app-cache-dir=${paths.cache}',
        '--data-dir=${paths.data}',
        '--webview-origin=$_flutterOrigin',
        '--parent-lifetime-fd=0',
        '--bootstrap-fd=1',
      ],
      mode: ProcessStartMode.normal,
      runInShell: false,
    );
    unawaited(process.stderr.drain<void>());
    try {
      final lines = process.stdout
          .transform(const Utf8Decoder(allowMalformed: false))
          .transform(const LineSplitter());
      final iterator = StreamIterator<String>(lines);
      final progress = await _nextFrame(
        iterator,
        timeout: const Duration(seconds: 5),
        frame: 1,
      );
      if (requireString(progress, 'schema', 'bootstrap.progress') !=
              'vibermate-daemon-progress-v1' ||
          requireString(progress, 'phase', 'bootstrap.progress') !=
              'runtime_starting') {
        throw const DesktopRuntimeException(
          'Desktop bootstrap progress contract did not match',
        );
      }
      final descriptor = await _nextFrame(
        iterator,
        timeout: const Duration(seconds: 120),
        frame: 2,
      );
      final schema = requireString(
        descriptor,
        'schema',
        'bootstrap.descriptor',
      );
      if (schema == 'vibermate-daemon-failure-v1') {
        throw DesktopRuntimeException(
          'Desktop runtime could not be started',
          reason: requireString(descriptor, 'reason', 'bootstrap.failure'),
        );
      }
      final validated = _DaemonDescriptor.fromJson(descriptor);
      if (validated.pid != process.pid) {
        throw const DesktopRuntimeException(
          'Desktop bootstrap PID did not match child process',
        );
      }
      final session = await _exchangeSession(validated);
      final api = await HttpControlApi.connect(session);
      return DesktopRuntime._(api: api, daemon: process);
    } catch (_) {
      await process.stdin.close();
      process.kill(ProcessSignal.sigterm);
      rethrow;
    }
  }

  static Future<JsonObject> _nextFrame(
    StreamIterator<String> iterator, {
    required Duration timeout,
    required int frame,
  }) async {
    final available = await iterator.moveNext().timeout(
      timeout,
      onTimeout: () => throw DesktopRuntimeException(
        frame == 1
            ? 'Desktop bootstrap progress deadline exceeded'
            : 'Desktop bootstrap readiness deadline exceeded',
      ),
    );
    if (!available) {
      throw const DesktopRuntimeException(
        'Desktop sidecar exited before bootstrap',
      );
    }
    final line = iterator.current;
    if (utf8.encode(line).length > _maximumBootstrapBytes) {
      throw const DesktopRuntimeException(
        'Desktop bootstrap exceeded its size limit',
      );
    }
    try {
      return requireObject(jsonDecode(line), 'bootstrap.frame');
    } on FormatException {
      throw const DesktopRuntimeException(
        'Desktop bootstrap was not valid JSON',
      );
    }
  }

  static Future<DesktopSession> _exchangeSession(
    _DaemonDescriptor descriptor,
  ) async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 5);
    try {
      final uri = descriptor.baseUrl.resolve('/api/v1/auth/sessions');
      final request = await client
          .postUrl(uri)
          .timeout(const Duration(seconds: 5));
      request
        ..followRedirects = false
        ..headers.set(
          HttpHeaders.authorizationHeader,
          'Bootstrap ${descriptor.bootstrapNonce}',
        );
      final response = await request.close().timeout(
        const Duration(seconds: 5),
      );
      final builder = BytesBuilder(copy: false);
      var length = 0;
      await for (final chunk in response.timeout(const Duration(seconds: 5))) {
        length += chunk.length;
        if (length > _maximumBootstrapBytes) {
          throw const DesktopRuntimeException(
            'Desktop session exceeded its size limit',
          );
        }
        builder.add(chunk);
      }
      if (response.statusCode != HttpStatus.created ||
          response.headers.value(HttpHeaders.cacheControlHeader) !=
              'no-store' ||
          response.headers.contentType?.mimeType != 'application/json') {
        throw const DesktopRuntimeException(
          'Desktop bootstrap capability was rejected',
        );
      }
      final bytes = builder.takeBytes();
      final session = DesktopSession.fromJson(jsonDecode(utf8.decode(bytes)));
      if (session.baseUrl != descriptor.baseUrl ||
          session.instanceId != descriptor.instanceId) {
        throw const DesktopRuntimeException(
          'Desktop session did not match bootstrap descriptor',
        );
      }
      return session;
    } on FormatException {
      throw const DesktopRuntimeException('Desktop session was not valid JSON');
    } finally {
      client.close(force: true);
    }
  }

  static Future<String> _resolveDaemonPath(String? explicitPath) async {
    final configured =
        explicitPath ?? Platform.environment['VIBERMATE_DAEMON_PATH'];
    final candidate = configured == null || configured.isEmpty
        ? File(
            Platform.resolvedExecutable,
          ).parent.uri.resolve('vibermated').toFilePath()
        : configured;
    final file = File(candidate);
    if (!file.isAbsolute || !await file.exists()) {
      throw DesktopRuntimeException(
        'Packaged Desktop sidecar is unavailable',
        reason: candidate,
      );
    }
    return file.resolveSymbolicLinks();
  }

  static Future<({String cache, String data})> _runtimePaths({
    String? cacheDirectory,
    String? dataDirectory,
    String? homeDirectory,
  }) async {
    final home = homeDirectory ?? Platform.environment['HOME'];
    if (home == null || !Directory(home).isAbsolute) {
      throw const DesktopRuntimeException(
        'macOS home directory is unavailable',
      );
    }
    final cache = Directory(
      cacheDirectory ?? '$home/Library/Caches/$applicationId',
    );
    final data = Directory(
      dataDirectory ?? '$home/Library/Application Support/$applicationId',
    );
    if (!cache.isAbsolute || !data.isAbsolute) {
      throw const DesktopRuntimeException(
        'Desktop runtime paths must be absolute',
      );
    }
    await cache.create(recursive: true);
    await data.create(recursive: true);
    return (
      cache: await cache.resolveSymbolicLinks(),
      data: await data.resolveSymbolicLinks(),
    );
  }

  Future<void> close() async {
    if (_closed) return;
    _closed = true;
    await api.close();
    try {
      await _daemon.stdin.close();
    } on Object {
      // An unexpectedly exited child may have already closed the pipe.
    }
    try {
      await _daemon.exitCode.timeout(const Duration(seconds: 2));
    } on TimeoutException {
      _daemon.kill(ProcessSignal.sigterm);
      try {
        await _daemon.exitCode.timeout(const Duration(milliseconds: 250));
      } on TimeoutException {
        _daemon.kill(ProcessSignal.sigkill);
      }
    }
  }
}

final class _DaemonDescriptor {
  const _DaemonDescriptor({
    required this.instanceId,
    required this.pid,
    required this.baseUrl,
    required this.bootstrapNonce,
  });

  factory _DaemonDescriptor.fromJson(JsonObject value) {
    if (requireString(value, 'schema', 'bootstrap.descriptor') !=
        'vibermate-daemon-bootstrap-v1') {
      throw const DesktopRuntimeException(
        'Desktop bootstrap schema did not match',
      );
    }
    final apiVersions = requireStringList(
      value,
      'apiVersions',
      'bootstrap.descriptor',
    );
    final eventVersions = requireStringList(
      value,
      'eventVersions',
      'bootstrap.descriptor',
    );
    final nonce = requireString(
      value,
      'bootstrapNonce',
      'bootstrap.descriptor',
    );
    final baseUrl = Uri.tryParse(
      requireString(value, 'baseUrl', 'bootstrap.descriptor'),
    );
    if (apiVersions.length != 1 ||
        apiVersions.single != 'v1' ||
        eventVersions.isNotEmpty ||
        !RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(nonce) ||
        baseUrl == null ||
        baseUrl.scheme != 'http' ||
        baseUrl.host != '127.0.0.1' ||
        !baseUrl.hasPort ||
        baseUrl.path.isNotEmpty ||
        baseUrl.hasQuery ||
        baseUrl.hasFragment) {
      throw const DesktopRuntimeException(
        'Desktop bootstrap contract did not match',
      );
    }
    return _DaemonDescriptor(
      instanceId: requireString(value, 'instanceId', 'bootstrap.descriptor'),
      pid: requireInteger(value, 'pid', 'bootstrap.descriptor', minimum: 1),
      baseUrl: baseUrl,
      bootstrapNonce: nonce,
    );
  }

  final String instanceId;
  final int pid;
  final Uri baseUrl;
  final String bootstrapNonce;
}

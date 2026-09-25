import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import '../api/control_api.dart';
import '../api/control_models.dart';
import 'desktop_daemon_environment.dart';
import 'desktop_daemon_lifecycle.dart';
import 'desktop_storage.dart';

final class DesktopRuntimeException implements Exception {
  const DesktopRuntimeException(this.message, {this.reason});

  final String message;
  final String? reason;

  @override
  String toString() => reason == null ? message : '$message ($reason)';
}

final class DesktopRuntime {
  DesktopRuntime._({
    required this.api,
    required Process daemon,
    required this.executable,
    required this.cacheDirectory,
    required this.dataDirectory,
    required this.storage,
    this.storageNotice,
  }) : _daemon = daemon;

  static const _maximumBootstrapBytes = 16 * 1024;
  static const _flutterOrigin = 'vibermate://desktop';
  static const applicationId = 'io.vibermate.desktop';

  final ControlApi api;
  final Process _daemon;
  final String executable;
  final String cacheDirectory;
  final String dataDirectory;
  final DesktopStorage? storage;
  final String? storageNotice;
  Future<void>? _closeFuture;
  bool _closed = false;

  bool get isClosed => _closed;

  int get daemonPid => _daemon.pid;

  Future<int> get exitCode => _daemon.exitCode;

  static Future<DesktopRuntime> start({
    String? daemonPath,
    String? cacheDirectory,
    String? dataDirectory,
    String? homeDirectory,
    String? remoteServerListenAddress,
  }) async {
    final executable = await _resolveDaemonPath(daemonPath);
    final paths = await _runtimePaths(
      cacheDirectory: cacheDirectory,
      dataDirectory: dataDirectory,
      homeDirectory: homeDirectory,
    );
    final selection = paths.selection;
    try {
      final runtime = await _launch(
        executable,
        paths.cache,
        paths.data,
        storage: paths.storage,
        remoteServerListenAddress: remoteServerListenAddress,
      );
      if (selection?.previous != null) {
        try {
          await paths.storage!.save(DesktopStorageSelection(paths.data));
        } catch (_) {
          await runtime.close();
          rethrow;
        }
      }
      return runtime;
    } catch (error) {
      if (error is DesktopRuntimeException &&
          error.reason == 'runtime_already_active') {
        rethrow;
      }
      final previous = selection?.previous;
      if (previous == null) rethrow;
      // This fallback is only for an UNCOMMITTED migration. A missing external
      // volume on later launches must not silently fork the user's history.
      if (!await File('$previous/runtime.db').exists()) rethrow;
      await paths.storage!.save(DesktopStorageSelection(previous));
      return _launch(
        executable,
        paths.cache,
        previous,
        storage: paths.storage,
        remoteServerListenAddress: remoteServerListenAddress,
        storageNotice: 'settings.storage.rolled_back',
      );
    }
  }

  static Future<DesktopRuntime> _launch(
    String executable,
    String cache,
    String data, {
    DesktopStorage? storage,
    String? remoteServerListenAddress,
    String? storageNotice,
  }) async {
    if (storage != null &&
        await storage.settings.exists() &&
        !await File('$data/runtime.db').exists()) {
      throw const DesktopStorageFailure('storage_location_unavailable');
    }
    final arguments = <String>[
      '--app-cache-dir=$cache',
      '--data-dir=$data',
      '--webview-origin=$_flutterOrigin',
      '--parent-lifetime-fd=0',
      '--bootstrap-fd=1',
      if (remoteServerListenAddress != null)
        '--remote-server-listen=$remoteServerListenAddress',
    ];
    final process = await Process.start(
      executable,
      arguments,
      mode: ProcessStartMode.normal,
      runInShell: false,
      environment: desktopDaemonEnvironment(Platform.environment),
      includeParentEnvironment: false,
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
      return DesktopRuntime._(
        api: api,
        daemon: process,
        executable: executable,
        cacheDirectory: cache,
        dataDirectory: data,
        storage: storage,
        storageNotice: storageNotice,
      );
    } catch (_) {
      await const DesktopDaemonLifecycle.production().close(
        _IODesktopDaemonProcess(process),
      );
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

  static Future<
    ({
      String cache,
      String data,
      DesktopStorage? storage,
      DesktopStorageSelection? selection,
    })
  >
  _runtimePaths({
    String? cacheDirectory,
    String? dataDirectory,
    String? homeDirectory,
  }) async {
    // Packaged acceptance uses Foundation's standard fixed-user-home override
    // to isolate App data without replacing the login HOME required by the
    // macOS Keychain. Production launches normally have no such override.
    final home =
        homeDirectory ??
        Platform.environment['CFFIXED_USER_HOME'] ??
        Platform.environment['HOME'];
    if (home == null || !Directory(home).isAbsolute) {
      throw const DesktopRuntimeException(
        'macOS home directory is unavailable',
      );
    }
    final cache = Directory(
      cacheDirectory ?? '$home/Library/Caches/$applicationId',
    );
    final defaultData = '$home/Library/Application Support/$applicationId';
    final storage = dataDirectory == null ? DesktopStorage(defaultData) : null;
    final selection = await storage?.read();
    final data = Directory(dataDirectory ?? selection!.directory);
    if (!cache.isAbsolute || !data.isAbsolute) {
      throw const DesktopRuntimeException(
        'Desktop runtime paths must be absolute',
      );
    }
    await cache.create(recursive: true);
    if (dataDirectory != null ||
        (selection?.directory == defaultData &&
            !await storage!.settings.exists())) {
      await data.create(recursive: true);
    }
    return (
      cache: await cache.resolveSymbolicLinks(),
      data: await data.exists() ? await data.resolveSymbolicLinks() : data.path,
      storage: storage,
      selection: selection,
    );
  }

  Future<void> prepareStorageMove(String target) async {
    if (storage == null) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
    await DesktopStorage.validateTarget(dataDirectory, target);
    await _prepareOfflineStorageOperation();
  }

  Future<void> prepareStorageBackup(String target) async {
    if (storage == null) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
    await DesktopStorage.validateTarget(dataDirectory, target);
    await _prepareOfflineStorageOperation();
  }

  Future<void> prepareStorageRestore(
    ({String backup, String target}) selection,
  ) async {
    if (storage == null) {
      throw const DesktopStorageFailure('storage_target_invalid');
    }
    await DesktopStorage.validateTarget(dataDirectory, selection.target);
    final verified = await Process.run(
      executable,
      ['verify-backup', '--source=${selection.backup}'],
      includeParentEnvironment: false,
      environment: desktopDaemonEnvironment(Platform.environment),
    );
    if (verified.exitCode != 0) {
      throw DesktopStorageFailure(_storageCommandFailure(verified));
    }
    await _prepareOfflineStorageOperation();
  }

  Future<void> _prepareOfflineStorageOperation() async {
    final dashboard = await api.loadDashboard();
    final wasOnline = dashboard.status.offlineHold.canEnter;
    final hold = wasOnline
        ? await api.enterOfflineHold(dashboard.status.offlineHold)
        : dashboard.status.offlineHold;
    try {
      if (hold.state != 'held' ||
          hold.activeActions != 0 ||
          hold.activeEgress != 0) {
        throw const DesktopStorageFailure('storage_in_use');
      }
      String? cursor;
      do {
        final page = await api.captures(cursor: cursor, limit: 100);
        if (page.items.any((capture) => capture.running)) {
          throw const DesktopStorageFailure('storage_in_use');
        }
        cursor = page.nextCursor;
      } while (cursor != null);
    } catch (_) {
      if (wasOnline) await api.resumeOfflineHold(hold);
      rethrow;
    }
  }

  Future<void> moveStorage(String target) async {
    await close();
    await _runStorageCommand('move-data', [
      '--source=$dataDirectory',
      '--target=$target',
      '--app-cache-dir=$cacheDirectory',
    ]);
    await storage!.save(
      DesktopStorageSelection(target, previous: dataDirectory),
    );
  }

  Future<void> backupStorage(String target) async {
    await close();
    await _runStorageCommand('backup-data', [
      '--source=$dataDirectory',
      '--target=$target',
    ]);
  }

  Future<void> restoreStorage(
    ({String backup, String target}) selection,
  ) async {
    await close();
    await _runStorageCommand('restore-data', [
      '--source=${selection.backup}',
      '--target=${selection.target}',
    ]);
    await storage!.save(
      DesktopStorageSelection(selection.target, previous: dataDirectory),
    );
  }

  Future<void> _runStorageCommand(
    String command,
    List<String> arguments,
  ) async {
    final result = await Process.run(
      executable,
      [command, ...arguments],
      includeParentEnvironment: false,
      environment: desktopDaemonEnvironment(Platform.environment),
    );
    if (result.exitCode != 0) {
      throw DesktopStorageFailure(_storageCommandFailure(result));
    }
  }

  String _storageCommandFailure(ProcessResult result) {
    final code = result.stderr.toString().trim();
    return const {
          'storage_target_invalid',
          'storage_in_use',
          'storage_copy_failed',
          'storage_validation_failed',
          'backup_validation_failed',
          'backup_incompatible',
        }.contains(code)
        ? code
        : 'storage_copy_failed';
  }

  Future<void> close() => _closeFuture ??= _close();

  Future<void> _close() async {
    if (_closed) return;
    _closed = true;
    try {
      await api.close();
    } finally {
      await const DesktopDaemonLifecycle.production().close(
        _IODesktopDaemonProcess(_daemon),
      );
    }
  }
}

final class _IODesktopDaemonProcess implements DesktopDaemonProcess {
  const _IODesktopDaemonProcess(this.process);

  final Process process;

  @override
  Future<int> get exitCode => process.exitCode;

  @override
  Future<void> closeInput() => process.stdin.close();

  @override
  bool terminate() => process.kill(ProcessSignal.sigterm);

  @override
  bool kill() => process.kill(ProcessSignal.sigkill);
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

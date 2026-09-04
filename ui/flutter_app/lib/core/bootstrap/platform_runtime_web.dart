import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import '../api/control_api.dart';
import '../api/control_models.dart';
import 'runtime_connection.dart';
import 'terminal_command.dart';

const bool platformRuntimeRequiresLogin = true;
const _maximumLoginResponseBytes = 16 * 1024;
const _requestTimeout = Duration(seconds: 10);

String platformRuntimeTargetLabel() =>
    RuntimeServerLocation.fromPageUri(Uri.base).displayLabel;

bool platformRuntimeUsesPlaintext() =>
    !RuntimeServerLocation.fromPageUri(Uri.base).encrypted;

Future<RuntimeConnection> connectPlatformRuntime({
  RuntimeLoginAttempt? login,
  String? daemonPath,
}) async {
  final location = RuntimeServerLocation.fromPageUri(Uri.base);
  final baseUrl = location.baseUrl;
  final client = http.Client();
  try {
    if (login == null) {
      final state = await _send(
        client,
        baseUrl,
        'GET',
        '/api/v1/server/web-auth',
      );
      if (state.statusCode != 200) {
        throw const RuntimeConnectionException('web_auth_unavailable');
      }
      final value = _decodeObject(state.bytes, 'webAuth');
      requireFields(
        value,
        'webAuth',
        required: const {'schema', 'setupRequired'},
      );
      if (requireString(value, 'schema', 'webAuth') !=
          'vibermate-web-auth-v1') {
        throw const RuntimeConnectionException('web_auth_response_invalid');
      }
      final setupRequired = value['setupRequired'];
      if (setupRequired is! bool) {
        throw const RuntimeConnectionException('web_auth_response_invalid');
      }
      throw RuntimeLoginRequired(
        setupRequired ? 'setup_required' : 'credentials_required',
      );
    }

    final endpoint = switch (login.mode) {
      RuntimeLoginMode.signIn => '/api/v1/server/web-sessions',
      RuntimeLoginMode.setup => '/api/v1/server/web-setup',
      RuntimeLoginMode.recover => '/api/v1/server/web-recovery',
    };
    final body = switch (login.mode) {
      RuntimeLoginMode.signIn => {
        'schema': 'vibermate-web-login-v1',
        'username': login.username,
        'password': login.password,
      },
      RuntimeLoginMode.setup => {
        'schema': 'vibermate-web-setup-v1',
        'recoveryKey': login.recoveryKey,
        'username': login.username,
        'password': login.password,
      },
      RuntimeLoginMode.recover => {
        'schema': 'vibermate-web-recovery-v1',
        'recoveryKey': login.recoveryKey,
        'newPassword': login.password,
      },
    };
    final response = await _send(client, baseUrl, 'POST', endpoint, body: body);
    if (response.statusCode == 429) {
      throw const RuntimeLoginRequired('rate_limited');
    }
    if (response.statusCode == 401) {
      throw RuntimeLoginRequired(
        login.mode == RuntimeLoginMode.recover
            ? 'recovery_rejected'
            : 'credentials_rejected',
      );
    }
    if (response.statusCode == 409) {
      throw RuntimeLoginRequired(
        _problemCode(response.bytes) == 'web_setup_required'
            ? 'setup_required'
            : 'setup_changed',
      );
    }
    if (response.statusCode == 422) {
      throw RuntimeLoginRequired(
        login.mode == RuntimeLoginMode.setup
            ? 'setup_invalid'
            : login.mode == RuntimeLoginMode.recover
            ? 'recovery_invalid'
            : 'credentials_invalid',
      );
    }
    if (response.statusCode != 201) {
      throw const RuntimeConnectionException('web_login_unavailable');
    }

    var active = _decodeSession(response.bytes, baseUrl);
    final principal = _principal(response.bytes);
    final sessionEnded = Completer<void>();
    Timer? expiryTimer;
    var closed = false;

    void notifySessionEnded() {
      if (!closed && !sessionEnded.isCompleted) sessionEnded.complete();
    }

    void scheduleExpiry() {
      expiryTimer?.cancel();
      final remaining = active.expiresAt.difference(DateTime.now().toUtc());
      if (remaining <= Duration.zero) {
        scheduleMicrotask(notifySessionEnded);
      } else {
        expiryTimer = Timer(remaining, notifySessionEnded);
      }
    }

    final api = await HttpControlApi.connect(
      active,
      client: client,
      browserManagedHeaders: true,
      inspectSession: false,
      selfScoped: !principal.owner,
      onSessionInvalidated: notifySessionEnded,
    );
    scheduleExpiry();

    Future<void> closeRuntime() async {
      if (closed) return;
      closed = true;
      expiryTimer?.cancel();
      await api.close();
    }

    Future<void> changePassword(
      String currentPassword,
      String newPassword,
    ) async {
      final changed = await _send(
        client,
        baseUrl,
        'PATCH',
        '/api/v1/server/web-account/password',
        token: active.writeToken,
        body: {
          'schema': 'vibermate-web-password-v1',
          'currentPassword': currentPassword,
          'newPassword': newPassword,
        },
      );
      if (changed.statusCode == 401) {
        if (_problemCode(changed.bytes) == 'web_session_invalid') {
          notifySessionEnded();
          throw const RuntimeLoginRequired('session_expired');
        }
        throw const RuntimeLoginRequired('current_password_rejected');
      }
      if (changed.statusCode == 422) {
        throw const RuntimeLoginRequired('new_password_invalid');
      }
      if (changed.statusCode != 201) {
        throw const RuntimeConnectionException('password_change_unavailable');
      }
      final nextPrincipal = _principal(changed.bytes);
      if (nextPrincipal.id != principal.id ||
          nextPrincipal.role != principal.role) {
        throw const RuntimeConnectionException('web_session_response_invalid');
      }
      active = _decodeSession(changed.bytes, baseUrl);
      api.replaceSession(active);
      scheduleExpiry();
    }

    Future<void> signOut() async {
      try {
        await _send(
          client,
          baseUrl,
          'DELETE',
          '/api/v1/server/web-sessions/current',
          token: active.writeToken,
        );
      } finally {
        await closeRuntime();
      }
    }

    return RuntimeConnection(
      api: api,
      terminalCommands: PackagedTerminalCommandService(),
      close: closeRuntime,
      isClosed: () => closed,
      serverManagement: principal.owner,
      terminalManagement: false,
      rootTrustManagement: false,
      targetLabel: location.displayLabel,
      webSessionEnded: sessionEnded.future,
      webPrincipal: principal,
      changePassword: changePassword,
      signOut: signOut,
    );
  } on RuntimeLoginRequired {
    client.close();
    rethrow;
  } on RuntimeConnectionException {
    client.close();
    rethrow;
  } on Object {
    client.close();
    throw const RuntimeConnectionException('web_login_unavailable');
  }
}

Future<({int statusCode, List<int> bytes})> _send(
  http.Client client,
  Uri baseUrl,
  String method,
  String path, {
  Map<String, Object?>? body,
  String? token,
}) async {
  final request = http.Request(method, baseUrl.resolve(path))
    ..followRedirects = false
    ..headers['accept'] = 'application/json, application/problem+json';
  if (body != null) {
    request.headers['content-type'] = 'application/json';
    request.body = jsonEncode(body);
  }
  if (token != null) request.headers['authorization'] = 'Bearer $token';
  final response = await client.send(request).timeout(_requestTimeout);
  final bytes = <int>[];
  await for (final chunk in response.stream.timeout(_requestTimeout)) {
    bytes.addAll(chunk);
    if (bytes.length > _maximumLoginResponseBytes) {
      throw const RuntimeConnectionException('web_session_response_invalid');
    }
  }
  return (statusCode: response.statusCode, bytes: bytes);
}

JsonObject _decodeObject(List<int> bytes, String path) {
  try {
    return requireObject(
      jsonDecode(utf8.decode(bytes, allowMalformed: false)),
      path,
    );
  } on Object {
    throw const RuntimeConnectionException('web_session_response_invalid');
  }
}

String? _problemCode(List<int> bytes) {
  try {
    final value = jsonDecode(utf8.decode(bytes, allowMalformed: false));
    if (value case {'code': final String code}) return code;
  } on Object {
    // A malformed problem remains an unavailable response at the caller.
  }
  return null;
}

DesktopSession _decodeSession(List<int> bytes, Uri baseUrl) {
  final value = _sessionObject(bytes);
  final readToken = requireString(value, 'readToken', 'webSession');
  final writeToken = requireString(value, 'writeToken', 'webSession');
  final expiresAt = requireTimestamp(value, 'expiresAt', 'webSession');
  if (!RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(readToken) ||
      !RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(writeToken) ||
      readToken == writeToken ||
      !expiresAt.isAfter(DateTime.now().toUtc())) {
    throw const RuntimeConnectionException('web_session_response_invalid');
  }
  return DesktopSession(
    baseUrl: baseUrl,
    readToken: readToken,
    writeToken: writeToken,
    instanceId: requireString(value, 'instanceId', 'webSession'),
    expiresAt: expiresAt,
  );
}

RuntimeWebPrincipal _principal(List<int> bytes) {
  final value = _sessionObject(bytes);
  final principal = requireObject(value['principal'], 'webSession.principal');
  requireFields(
    principal,
    'webSession.principal',
    required: const {'id', 'username', 'role'},
  );
  final role = switch (requireString(
    principal,
    'role',
    'webSession.principal',
  )) {
    'owner' => RuntimeWebRole.owner,
    'member' => RuntimeWebRole.member,
    _ => throw const RuntimeConnectionException('web_session_response_invalid'),
  };
  return RuntimeWebPrincipal(
    id: requireString(principal, 'id', 'webSession.principal'),
    username: requireString(principal, 'username', 'webSession.principal'),
    role: role,
  );
}

JsonObject _sessionObject(List<int> bytes) {
  final value = _decodeObject(bytes, 'webSession');
  requireFields(
    value,
    'webSession',
    required: const {
      'schema',
      'instanceId',
      'apiVersion',
      'principal',
      'readToken',
      'writeToken',
      'expiresAt',
    },
  );
  if (requireString(value, 'schema', 'webSession') !=
          'vibermate-web-session-v1' ||
      requireString(value, 'apiVersion', 'webSession') != 'v1') {
    throw const RuntimeConnectionException('web_session_response_invalid');
  }
  return value;
}

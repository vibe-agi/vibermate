import 'dart:convert';

import 'package:http/http.dart' as http;

import '../api/control_api.dart';
import '../api/control_models.dart';
import 'runtime_connection.dart';
import 'terminal_command.dart';

const bool platformRuntimeRequiresLogin = true;
const _maximumLoginResponseBytes = 16 * 1024;

String platformRuntimeTargetLabel() =>
    RuntimeServerLocation.fromPageUri(Uri.base).displayLabel;

bool platformRuntimeUsesPlaintext() =>
    !RuntimeServerLocation.fromPageUri(Uri.base).encrypted;

Future<RuntimeConnection> connectPlatformRuntime({
  String? accessKey,
  String? daemonPath,
}) async {
  final current = Uri.base;
  final location = RuntimeServerLocation.fromPageUri(current);
  if (accessKey == null || accessKey.isEmpty) {
    throw const RuntimeLoginRequired('access_key_required');
  }
  if (!RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(accessKey)) {
    throw const RuntimeLoginRequired('access_key_invalid');
  }
  final baseUrl = location.baseUrl;
  final client = http.Client();
  try {
    final request =
        http.Request('POST', baseUrl.resolve('/api/v1/server/admin-sessions'))
          ..followRedirects = false
          ..headers['accept'] = 'application/json, application/problem+json'
          ..headers['content-type'] = 'application/json'
          ..body = jsonEncode({
            'schema': 'vibermate-server-admin-login-v1',
            'accessKey': accessKey,
          });
    final response = await client
        .send(request)
        .timeout(const Duration(seconds: 10));
    final bytes = <int>[];
    await for (final chunk in response.stream.timeout(
      const Duration(seconds: 10),
    )) {
      bytes.addAll(chunk);
      if (bytes.length > _maximumLoginResponseBytes) {
        throw const RuntimeConnectionException('admin_login_response_invalid');
      }
    }
    if (response.statusCode == 401) {
      throw const RuntimeLoginRequired('access_key_rejected');
    }
    if (response.statusCode != 201) {
      throw const RuntimeConnectionException('admin_login_unavailable');
    }
    final value = requireObject(
      jsonDecode(utf8.decode(bytes, allowMalformed: false)),
      'adminSession',
    );
    requireFields(
      value,
      'adminSession',
      required: const {
        'schema',
        'instanceId',
        'apiVersion',
        'readToken',
        'writeToken',
        'expiresAt',
      },
    );
    if (requireString(value, 'schema', 'adminSession') !=
            'vibermate-server-admin-session-v1' ||
        requireString(value, 'apiVersion', 'adminSession') != 'v1') {
      throw const RuntimeConnectionException('admin_login_response_invalid');
    }
    final readToken = requireString(value, 'readToken', 'adminSession');
    final writeToken = requireString(value, 'writeToken', 'adminSession');
    final expiresAt = requireTimestamp(value, 'expiresAt', 'adminSession');
    if (!RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(readToken) ||
        !RegExp(r'^[A-Za-z0-9_-]{43}$').hasMatch(writeToken) ||
        readToken == writeToken ||
        !expiresAt.isAfter(DateTime.now().toUtc())) {
      throw const RuntimeConnectionException('admin_login_response_invalid');
    }
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: baseUrl,
        readToken: readToken,
        writeToken: writeToken,
        instanceId: requireString(value, 'instanceId', 'adminSession'),
        expiresAt: expiresAt,
      ),
      client: client,
      browserManagedHeaders: true,
      inspectSession: false,
    );
    return RuntimeConnection(
      api: api,
      terminalCommands: PackagedTerminalCommandService(),
      close: api.close,
      isClosed: () => false,
      serverManagement: true,
      terminalManagement: false,
      rootTrustManagement: false,
      targetLabel: location.displayLabel,
    );
  } on RuntimeLoginRequired {
    client.close();
    rethrow;
  } on RuntimeConnectionException {
    client.close();
    rethrow;
  } on Object {
    client.close();
    throw const RuntimeConnectionException('admin_login_unavailable');
  }
}

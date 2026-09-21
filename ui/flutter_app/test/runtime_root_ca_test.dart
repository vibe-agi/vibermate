import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/public_certificate_exporter_contract.dart';
import 'package:vibermate_app/core/bootstrap/public_certificate_exporter_io.dart'
    as native;
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/runtime_root_ca_panel.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

Map<String, Object?> caPayload({int seed = 0}) {
  // Dart verifies PEM boundaries and the digest. Go integration tests verify
  // actual X.509 CA constraints, signatures and strict HTTPS connections.
  final der = List<int>.generate(80, (index) => index + seed);
  return {
    'schema': 'vibermate-runtime-root-ca-v1',
    'certificatePem':
        '-----BEGIN CERTIFICATE-----\n${base64Encode(der)}\n-----END CERTIFICATE-----\n',
    'fingerprint': crypto.sha256.convert(der).toString(),
    'notBefore': '2026-01-01T00:00:00Z',
    'notAfter': '2036-01-01T00:00:00Z',
  };
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('Root contract accepts exactly one unified public CA', () {
    final valid = caPayload();
    final ca = RuntimeRootCertificate.fromJson(valid, 'rootCA');
    expect(ca.fileName, 'vibermate-ca.crt');
    expect(ca.fingerprint, valid['fingerprint']);
    expect(ca.notBefore, DateTime.utc(2026));
    expect(ca.notAfter, DateTime.utc(2036));
  });

  final valid = caPayload();
  final invalidPayloads = <String, Map<String, Object?>>{
    'old HTTPS CA': {...valid, 'schema': 'vibermate-server-https-ca-v1'},
    'old leaf': {...valid, 'schema': 'vibermate-server-certificate-v1'},
    'wrong digest': {...valid, 'fingerprint': '0' * 64},
    'uppercase digest': {
      ...valid,
      'fingerprint': (valid['fingerprint']! as String).toUpperCase(),
    },
    'private key field': {...valid, 'privateKeyPem': 'forbidden'},
    'pending field': {...valid, 'pending': valid},
    'hosts field': {
      ...valid,
      'dnsNames': ['localhost'],
    },
    'bundle': {
      ...valid,
      'certificatePem': '${valid['certificatePem']}${valid['certificatePem']}',
    },
    'trailing key': {
      ...valid,
      'certificatePem':
          '${valid['certificatePem']}-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n',
    },
    'leading key': {
      ...valid,
      'certificatePem':
          '-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n${valid['certificatePem']}',
    },
    'oversized': {...valid, 'certificatePem': ' ' * (64 * 1024 + 1)},
    'empty': {...valid, 'certificatePem': ''},
    'reversed validity': {...valid, 'notAfter': '2020-01-01T00:00:00Z'},
    'invalid validity': {...valid, 'notBefore': 'not a date'},
  };
  for (final entry in invalidPayloads.entries) {
    test('Root contract rejects ${entry.key}', () {
      expect(
        () => RuntimeRootCertificate.fromJson(entry.value, 'rootCA'),
        throwsA(isA<ControlContractException>()),
      );
    });
  }

  test('native export uses only the Root CA method and filename', () async {
    const channel = MethodChannel('io.vibermate.desktop/public-certificate');
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    final calls = <MethodCall>[];
    var accepted = true;
    messenger.setMockMethodCallHandler(channel, (call) async {
      calls.add(call);
      return accepted;
    });
    addTearDown(() => messenger.setMockMethodCallHandler(channel, null));
    const exporter = native.PlatformPublicCertificateExporter();
    final ca = RuntimeRootCertificate.fromJson(caPayload(), 'rootCA');
    expect(await exporter.save(ca), isTrue);
    accepted = false;
    expect(await exporter.save(ca), isFalse);
    expect(calls.map((call) => call.method), everyElement('saveRuntimeRootCA'));
    expect(
      calls.map((call) => call.arguments),
      everyElement({
        'certificatePem': ca.certificatePem,
        'fileName': 'vibermate-ca.crt',
      }),
    );
  });

  test('Root CA API uses only the authenticated read channel', () async {
    var calls = 0;
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('https://localhost:9667'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'root-ca-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
      inspectSession: false,
      browserManagedHeaders: true,
      client: MockClient((request) async {
        calls++;
        expect(request.method, 'GET');
        expect(request.url.path, '/api/v1/server/root-ca');
        expect(request.url.query, isEmpty);
        expect(request.body, isEmpty);
        expect(request.headers['authorization'], 'Bearer ${'R' * 43}');
        return http.Response(
          jsonEncode(caPayload()),
          200,
          headers: {'content-type': 'application/json'},
        );
      }),
    );
    addTearDown(api.close);
    expect((await api.runtimeRootCA()).fileName, 'vibermate-ca.crt');
    expect(calls, 1);
  });

  for (final language in AppLanguage.values) {
    for (final dark in [false, true]) {
      for (final width in [390.0, 1200.0]) {
        testWidgets('only Root CA controls fit $width px ($language, $dark)', (
          tester,
        ) async {
          await tester.binding.setSurfaceSize(Size(width, 900));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = _RootCAApi();
          final exporter = _RecordingExporter();
          final controller = _controller(api, exporter);
          addTearDown(controller.dispose);
          final copy = AppCopy.forLanguage(language);
          await tester.pumpWidget(_panel(controller, copy, dark: dark));
          await tester.pumpAndSettle();
          expect(find.text(copy('settings.runtime_ca.title')), findsOneWidget);
          expect(find.text(api.certificate.fingerprint), findsOneWidget);
          expect(find.textContaining('2036-01-01'), findsOneWidget);
          expect(find.byType(TextField), findsNothing);
          expect(find.byType(OutlinedButton), findsOneWidget);
          for (final key in [
            'server-certificate-settings-panel',
            'server-certificate-download',
            'server-certificate-hosts-input',
            'server-certificate-stage',
            'server-certificate-pending-download',
            'server-certificate-apply',
          ]) {
            expect(find.byKey(Key(key)), findsNothing);
          }
          final button = find.byKey(const Key('runtime-root-ca-download'));
          await tester.ensureVisible(button);
          await tester.tap(button);
          await tester.pumpAndSettle();
          expect(
            exporter.saved.single.certificatePem,
            api.certificate.certificatePem,
          );
          expect(exporter.saved.single.fileName, 'vibermate-ca.crt');
          expect(
            api.calls,
            2,
            reason: 'download must re-read the current Root',
          );
          expect(find.text(copy('settings.runtime_ca.saved')), findsOneWidget);
          expect(tester.takeException(), isNull);
        });
      }
    }
  }

  testWidgets('load errors can be retried without stale downloads', (
    tester,
  ) async {
    final api = _RootCAApi()..fail = true;
    final exporter = _RecordingExporter();
    final controller = _controller(api, exporter);
    addTearDown(controller.dispose);
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(_panel(controller, copy));
    await tester.pumpAndSettle();
    expect(find.text(copy('settings.runtime_ca.load_error')), findsOneWidget);
    expect(find.byKey(const Key('runtime-root-ca-download')), findsNothing);
    api.fail = false;
    await tester.tap(find.byKey(const Key('runtime-root-ca-refresh')));
    await tester.pumpAndSettle();
    expect(find.text(api.certificate.fingerprint), findsOneWidget);
    api.fail = true;
    await tester.tap(find.byKey(const Key('runtime-root-ca-download')));
    await tester.pumpAndSettle();
    expect(find.text(copy('settings.runtime_ca.save_error')), findsOneWidget);
    expect(exporter.saved, isEmpty);
    await tester.tap(find.byKey(const Key('runtime-root-ca-refresh')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('runtime-root-ca-download')), findsNothing);
    expect(find.text(api.certificate.fingerprint), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'download re-reads replaced Root and handles cancellation or failure',
    (tester) async {
      final api = _RootCAApi();
      final exporter = _RecordingExporter()..accepted = false;
      final controller = _controller(api, exporter);
      addTearDown(controller.dispose);
      final copy = AppCopy.forLanguage(AppLanguage.english);
      await tester.pumpWidget(_panel(controller, copy));
      await tester.pumpAndSettle();
      final original = api.certificate.fingerprint;
      api.certificate = RuntimeRootCertificate.fromJson(
        caPayload(seed: 1),
        'rootCA',
      );
      final button = find.byKey(const Key('runtime-root-ca-download'));
      await tester.tap(button);
      await tester.pumpAndSettle();
      expect(exporter.saved.single.fingerprint, api.certificate.fingerprint);
      expect(find.text(original), findsNothing);
      expect(find.text(api.certificate.fingerprint), findsOneWidget);
      expect(find.text(copy('settings.runtime_ca.saved')), findsNothing);
      exporter.fail = true;
      await tester.tap(button);
      await tester.pumpAndSettle();
      expect(find.text(copy('settings.runtime_ca.save_error')), findsOneWidget);
      exporter.fail = false;
      exporter.accepted = true;
      await tester.tap(button);
      await tester.pumpAndSettle();
      expect(find.text(copy('settings.runtime_ca.save_error')), findsNothing);
      expect(find.text(copy('settings.runtime_ca.saved')), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'download disables duplicate clicks and refresh until completed',
    (tester) async {
      final api = _RootCAApi();
      final exporter = _RecordingExporter();
      final controller = _controller(api, exporter);
      addTearDown(controller.dispose);
      await tester.pumpWidget(
        _panel(controller, AppCopy.forLanguage(AppLanguage.english)),
      );
      await tester.pumpAndSettle();
      final completion = Completer<RuntimeRootCertificate>();
      api.pending = completion.future;
      final button = find.byKey(const Key('runtime-root-ca-download'));
      await tester.tap(button);
      await tester.pump();
      expect(tester.widget<OutlinedButton>(button).onPressed, isNull);
      expect(
        tester
            .widget<IconButton>(
              find.byKey(const Key('runtime-root-ca-refresh')),
            )
            .onPressed,
        isNull,
      );
      expect(api.calls, 2);
      completion.complete(api.certificate);
      await tester.pumpAndSettle();
      expect(exporter.saved, hasLength(1));
      expect(tester.widget<OutlinedButton>(button).onPressed, isNotNull);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('late load completion after unmount is safe', (tester) async {
    final completion = Completer<RuntimeRootCertificate>();
    final api = _RootCAApi()..pending = completion.future;
    final exporter = _RecordingExporter();
    final controller = _controller(api, exporter);
    addTearDown(controller.dispose);
    await tester.pumpWidget(
      _panel(controller, AppCopy.forLanguage(AppLanguage.english)),
    );
    await tester.pumpWidget(const SizedBox());
    completion.complete(api.certificate);
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(exporter.saved, isEmpty);
  });
}

Widget _panel(
  WorkbenchController controller,
  AppCopy copy, {
  bool dark = false,
}) => MaterialApp(
  theme: dark ? ViberTheme.dark() : ViberTheme.light(),
  home: Scaffold(
    body: SingleChildScrollView(
      child: RuntimeRootCAPanel(controller: controller, copy: copy),
    ),
  ),
);

WorkbenchController _controller(_RootCAApi api, _RecordingExporter exporter) =>
    WorkbenchController(
      api: api,
      certificateExporter: exporter,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      serverManagement: true,
      closeRuntime: () async {},
    );

final class _RootCAApi implements ControlApi {
  RuntimeRootCertificate certificate = RuntimeRootCertificate.fromJson(
    caPayload(),
    'rootCA',
  );
  int calls = 0;
  bool fail = false;
  Future<RuntimeRootCertificate>? pending;

  @override
  Future<RuntimeRootCertificate> runtimeRootCA() async {
    calls++;
    if (fail) throw StateError('fixture failure');
    final waiting = pending;
    if (waiting != null) return await waiting;
    return certificate;
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

final class _RecordingExporter implements PublicCertificateExporter {
  final saved = <RuntimeRootCertificate>[];
  bool accepted = true;
  bool fail = false;

  @override
  Future<bool> save(RuntimeRootCertificate certificate) async {
    if (fail) throw StateError('fixture save failure');
    saved.add(certificate);
    return accepted;
  }
}

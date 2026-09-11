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
import 'package:vibermate_app/features/workbench/server_certificate_panel.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

Map<String, Object?> certificatePayload({int seed = 0}) {
  // The Dart contract verifies PEM boundaries and its leaf digest; actual
  // X.509 parsing and HTTPS verification are exercised by the Go integration.
  final der = List<int>.generate(80, (index) => index + seed);
  return {
    'schema': 'vibermate-server-certificate-v1',
    'available': true,
    'mode': 'self_signed_tls',
    'certificatePem':
        '-----BEGIN CERTIFICATE-----\n${base64Encode(der)}\n-----END CERTIFICATE-----\n',
    'fingerprint': crypto.sha256.convert(der).toString(),
    'dnsNames': ['localhost', 'vibermate.example.test'],
    'ipAddresses': ['127.0.0.1', '192.168.1.20', '::1'],
    'notBefore': '2026-01-01T00:00:00Z',
    'notAfter': '2036-01-01T00:00:00Z',
  };
}

Map<String, Object?> caPayload() {
  final certificate = certificatePayload(seed: 2);
  return {
    'schema': 'vibermate-runtime-root-ca-v1',
    for (final key in [
      'certificatePem',
      'fingerprint',
      'notBefore',
      'notAfter',
    ])
      key: certificate[key],
  };
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  test('unified Root contract is one public certificate, not a bundle', () {
    final valid = caPayload();
    final ca = RuntimeServerCA.fromJson(valid, 'ca');
    expect(ca.fileName, 'vibermate-ca.crt');
    expect(ca.unified, isTrue);
    final legacy = RuntimeServerCA.fromJson({
      ...valid,
      'schema': 'vibermate-server-https-ca-v1',
    }, 'legacyCA');
    expect(legacy.unified, isFalse);
    expect(legacy.fileName, 'vibermate-server-ca.crt');
    for (final invalid in [
      {...valid, 'schema': 'vibermate-server-certificate-v1'},
      {...valid, 'fingerprint': '0' * 64},
      {...valid, 'privateKeyPem': 'forbidden'},
      {
        ...valid,
        'certificatePem':
            '${valid['certificatePem']}-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n',
      },
      {
        ...valid,
        'certificatePem':
            '${valid['certificatePem']}${valid['certificatePem']}',
      },
      {...valid, 'notAfter': '2020-01-01T00:00:00Z'},
    ]) {
      expect(
        () => RuntimeServerCA.fromJson(invalid, 'ca'),
        throwsA(isA<ControlContractException>()),
      );
    }
    for (final invalid in [
      {...certificatePayload(), 'ca': valid},
      {...certificatePayload(), 'managed': true, 'issuedByCA': true},
      {
        ...certificatePayload(),
        'managed': true,
        'issuedByCA': 'true',
        'ca': valid,
      },
      {
        ...certificatePayload(),
        'managed': true,
        'ca': {
          ...valid,
          'certificatePem': certificatePayload()['certificatePem'],
          'fingerprint': certificatePayload()['fingerprint'],
        },
      },
      {
        ...certificatePayload(),
        'managed': true,
        'ca': valid,
        'pending': {...certificatePayload(seed: 1), 'ca': valid},
      },
      {
        ...certificatePayload(),
        'managed': true,
        'pending': {...certificatePayload(seed: 1), 'issuedByCA': true},
      },
    ]) {
      expect(
        () => RuntimeServerCertificate.fromJson(invalid, 'certificate'),
        throwsA(isA<ControlContractException>()),
      );
    }
  });

  test(
    'native export distinguishes CA and leaf filenames without keys',
    () async {
      const channel = MethodChannel('io.vibermate.desktop/public-certificate');
      final messenger =
          TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
      final calls = <MethodCall>[];
      messenger.setMockMethodCallHandler(channel, (call) async {
        calls.add(call);
        return true;
      });
      addTearDown(() => messenger.setMockMethodCallHandler(channel, null));
      const exporter = native.PlatformPublicCertificateExporter();
      final leaf = RuntimeServerCertificate.fromJson(
        certificatePayload(),
        'certificate',
      );
      final ca = RuntimeServerCA.fromJson(caPayload(), 'ca');
      expect(await exporter.save(leaf), isTrue);
      expect(await exporter.save(ca), isTrue);
      expect(
        calls.map((call) => call.method),
        everyElement('saveServerCertificate'),
      );
      expect(calls[0].arguments, {
        'certificatePem': leaf.certificatePem,
        'fileName': 'vibermate-server.crt',
      });
      expect(calls[1].arguments, {
        'certificatePem': ca.certificatePem,
        'fileName': 'vibermate-ca.crt',
      });
    },
  );

  test(
    'certificate contract rejects key material and mismatched fingerprint',
    () {
      final valid = certificatePayload();
      final certificate = RuntimeServerCertificate.fromJson(
        valid,
        'certificate',
      );
      expect(certificate.fileName, 'vibermate-server.crt');
      expect(certificate.hosts, contains('192.168.1.20'));
      for (final invalid in [
        {...valid, 'fingerprint': '0' * 64},
        {
          ...valid,
          'certificatePem':
              '${valid['certificatePem']}-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n',
        },
        {...valid, 'privateKeyPem': 'must never be exposed'},
        {...valid, 'notAfter': '2020-01-01T00:00:00Z'},
        {...valid, 'managed': true, 'pending': valid},
        {...valid, 'pending': certificatePayload(seed: 1)},
        {...valid, 'managed': 'true'},
        {
          ...valid,
          'managed': true,
          'pending': {
            ...certificatePayload(seed: 1),
            'privateKeyPem': 'forbidden',
          },
        },
      ]) {
        expect(
          () => RuntimeServerCertificate.fromJson(invalid, 'certificate'),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );

  test('certificate API uses the authenticated read channel', () async {
    final api = await HttpControlApi.connect(
      DesktopSession(
        baseUrl: Uri.parse('https://localhost:9667'),
        readToken: 'R' * 43,
        writeToken: 'W' * 43,
        instanceId: 'certificate-test',
        expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
      ),
      inspectSession: false,
      browserManagedHeaders: true,
      client: MockClient((request) async {
        expect(request.method, 'GET');
        expect(request.url.path, '/api/v1/server/certificate');
        expect(request.url.query, isEmpty);
        expect(request.headers['authorization'], 'Bearer ${'R' * 43}');
        return http.Response(
          jsonEncode(certificatePayload()),
          200,
          headers: {'content-type': 'application/json'},
        );
      }),
    );
    addTearDown(api.close);
    expect((await api.serverCertificate()).available, isTrue);
  });

  test(
    'certificate mutations use write authorization and exact fingerprints',
    () async {
      var requests = 0;
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('https://localhost:9667'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'certificate-write-test',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        inspectSession: false,
        browserManagedHeaders: true,
        client: MockClient((request) async {
          requests++;
          expect(request.method, 'POST');
          expect(request.headers['authorization'], 'Bearer ${'W' * 43}');
          final body = jsonDecode(request.body) as Map<String, Object?>;
          expect(
            body['expectedFingerprint'],
            certificatePayload()['fingerprint'],
          );
          if (requests == 1) {
            expect(request.url.path, '/api/v1/server/certificate/stage');
            expect(body['hosts'], ['192.168.1.30']);
            expect(body['expectedPendingFingerprint'], '');
            return http.Response(
              jsonEncode({
                ...certificatePayload(),
                'managed': true,
                'pending': certificatePayload(seed: 1),
              }),
              200,
            );
          }
          expect(request.url.path, '/api/v1/server/certificate/apply');
          expect(
            body['pendingFingerprint'],
            certificatePayload(seed: 1)['fingerprint'],
          );
          return http.Response(
            jsonEncode({...certificatePayload(seed: 1), 'managed': true}),
            200,
          );
        }),
      );
      addTearDown(api.close);
      final staged = await api.stageServerCertificate(
        RuntimeServerCertificate.fromJson({
          ...certificatePayload(),
          'managed': true,
        }, 'certificate'),
        ['192.168.1.30'],
      );
      final applied = await api.applyServerCertificate(staged);
      expect(applied.pending, isNull);
      expect(applied.fingerprint, staged.pending!.fingerprint);
      expect(requests, 2);
    },
  );

  for (final language in AppLanguage.values) {
    for (final dark in [false, true]) {
      testWidgets('certificate download fits 390 px ($language, $dark)', (
        tester,
      ) async {
        await tester.binding.setSurfaceSize(const Size(390, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _CertificateApi();
        final exporter = _RecordingExporter();
        final controller = _controller(api, exporter);
        addTearDown(controller.dispose);
        await tester.pumpWidget(
          MaterialApp(
            theme: dark ? ViberTheme.dark() : ViberTheme.light(),
            home: Scaffold(
              body: SingleChildScrollView(
                child: ServerCertificatePanel(
                  controller: controller,
                  copy: AppCopy.forLanguage(language),
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('server-certificate-fingerprint')),
          findsOneWidget,
        );
        final button = find.byKey(const Key('server-certificate-download'));
        await tester.ensureVisible(button);
        await tester.tap(button);
        await tester.pumpAndSettle();
        expect(
          exporter.saved.single.certificatePem,
          api.certificate.certificatePem,
        );
        expect(
          api.calls,
          2,
          reason: 'export must re-read the current certificate',
        );
        expect(tester.takeException(), isNull);
        final ca = api.certificate.ca!;
        expect(
          find.text(
            AppCopy.forLanguage(language)(
              'settings.server_certificate.ca_migration',
            ),
          ),
          findsOneWidget,
        );
        final caDownload = find.byKey(const Key('server-ca-download'));
        await tester.ensureVisible(caDownload);
        // Export the authenticated CA snapshot even if a new TLS request
        // would fail during migration. Do not accidentally export the leaf.
        api.fail = true;
        await tester.tap(caDownload);
        await tester.pumpAndSettle();
        expect(api.calls, 2);
        expect(exporter.saved.last, isA<RuntimeServerCA>());
        expect(exporter.saved.last.certificatePem, ca.certificatePem);
        expect(exporter.saved.last.fileName, 'vibermate-ca.crt');
        api.fail = false;
        final original = api.certificate.fingerprint;
        final input = find.byKey(const Key('server-certificate-hosts-input'));
        await tester.ensureVisible(input);
        final example = find.byKey(
          const Key('server-certificate-hosts-example'),
        );
        final field = tester.widget<TextField>(input);
        expect(field.decoration!.hintText, isNull);
        expect(
          field.decoration!.floatingLabelBehavior,
          FloatingLabelBehavior.always,
        );
        expect(example, findsOneWidget);
        expect(
          tester.getTopLeft(example).dy,
          greaterThan(tester.getBottomLeft(input).dy),
        );
        expect(find.descendant(of: input, matching: example), findsNothing);
        // Exercise both empty and populated focus states: the example must
        // never be painted inside the editable field or become its value.
        await tester.enterText(input, '');
        await tester.pumpAndSettle();
        expect(field.controller!.text, isEmpty);
        expect(
          tester.getTopLeft(example).dy,
          greaterThan(tester.getBottomLeft(input).dy),
        );
        FocusManager.instance.primaryFocus?.unfocus();
        await tester.pumpAndSettle();
        expect(
          tester.getTopLeft(example).dy,
          greaterThan(tester.getBottomLeft(input).dy),
        );
        await tester.enterText(input, '192.168.1.30, runtime.example.test');
        await tester.pumpAndSettle();
        expect(field.controller!.text, '192.168.1.30, runtime.example.test');
        expect(
          tester.getTopLeft(example).dy,
          greaterThan(tester.getBottomLeft(input).dy),
        );
        await tester.ensureVisible(
          find.byKey(const Key('server-certificate-stage')),
        );
        await tester.tap(find.byKey(const Key('server-certificate-stage')));
        await tester.pumpAndSettle();
        expect(api.lastHosts, ['192.168.1.30', 'runtime.example.test']);
        expect(api.certificate.fingerprint, original);
        final pending = api.certificate.pending!;
        final download = find.byKey(
          const Key('server-certificate-pending-download'),
        );
        await tester.ensureVisible(download);
        await tester.tap(download);
        await tester.pumpAndSettle();
        expect(exporter.saved.last.fingerprint, pending.fingerprint);
        final apply = find.byKey(const Key('server-certificate-apply'));
        await tester.ensureVisible(apply);
        await tester.tap(apply);
        await tester.pumpAndSettle();
        final confirm = find.byKey(
          const Key('server-certificate-confirm-apply'),
        );
        expect(tester.widget<FilledButton>(confirm).onPressed, isNull);
        await tester.ensureVisible(
          find.byKey(const Key('server-certificate-confirm-trust')),
        );
        await tester.tap(
          find.byKey(const Key('server-certificate-confirm-trust')),
        );
        await tester.pumpAndSettle();
        await tester.ensureVisible(confirm);
        await tester.tap(confirm);
        await tester.pumpAndSettle();
        expect(api.applies, 1);
        expect(api.certificate.fingerprint, pending.fingerprint);
        expect(api.certificate.pending, isNull);
        expect(api.certificate.ca!.fingerprint, ca.fingerprint);
        expect(
          find.text(
            AppCopy.forLanguage(language)(
              'settings.server_certificate.ca_active',
            ),
          ),
          findsOneWidget,
        );
        expect(
          find.byKey(const Key('server-certificate-pending-download')),
          findsNothing,
        );
        expect(tester.takeException(), isNull);
      });
    }
  }

  testWidgets('HTTP mode has no download and load failures can be retried', (
    tester,
  ) async {
    final api = _CertificateApi()..fail = true;
    final controller = _controller(api, _RecordingExporter());
    addTearDown(controller.dispose);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: ServerCertificatePanel(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('could not be loaded'), findsOneWidget);
    api.fail = false;
    api.certificate = const RuntimeServerCertificate(
      available: false,
      mode: 'http',
    );
    await tester.tap(find.byKey(const Key('server-certificate-refresh')));
    await tester.pumpAndSettle();
    expect(find.textContaining('uses HTTP'), findsOneWidget);
    expect(find.byKey(const Key('server-certificate-download')), findsNothing);
    expect(find.byKey(const Key('server-ca-download')), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('unified Root rejects an old pending CA in the UI', (
    tester,
  ) async {
    final api = _CertificateApi();
    api.certificate = RuntimeServerCertificate.fromJson({
      ...certificatePayload(),
      'managed': true,
      'ca': caPayload(),
      'pending': certificatePayload(seed: 1),
    }, 'certificate');
    final controller = _controller(api, _RecordingExporter());
    addTearDown(controller.dispose);
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: SingleChildScrollView(
            child: ServerCertificatePanel(controller: controller, copy: copy),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.text(copy('settings.server_certificate.ca_changed')),
      findsOneWidget,
    );
    expect(
      tester
          .widget<FilledButton>(
            find.byKey(const Key('server-certificate-apply')),
          )
          .onPressed,
      isNull,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('old Server CA is never labeled as a unified Root', (
    tester,
  ) async {
    final api = _CertificateApi();
    api.certificate = RuntimeServerCertificate.fromJson({
      ...certificatePayload(),
      'managed': true,
      'ca': {...caPayload(), 'schema': 'vibermate-server-https-ca-v1'},
    }, 'certificate');
    final controller = _controller(api, _RecordingExporter());
    addTearDown(controller.dispose);
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: SingleChildScrollView(
            child: ServerCertificatePanel(controller: controller, copy: copy),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.text(copy('settings.server_certificate.legacy_ca_title')),
      findsOneWidget,
    );
    expect(
      find.text(copy('settings.server_certificate.ca_title')),
      findsNothing,
    );
    expect(tester.takeException(), isNull);
  });
}

WorkbenchController _controller(
  _CertificateApi api,
  _RecordingExporter exporter,
) => WorkbenchController(
  api: api,
  certificateExporter: exporter,
  terminalCommands: PreviewTerminalCommandService(),
  previewMode: false,
  serverManagement: true,
  closeRuntime: () async {},
);

final class _CertificateApi implements ControlApi {
  RuntimeServerCertificate certificate = RuntimeServerCertificate.fromJson({
    ...certificatePayload(),
    'managed': true,
    'ca': caPayload(),
  }, 'certificate');
  int calls = 0;
  bool fail = false;
  List<String>? lastHosts;
  int applies = 0;
  @override
  Future<RuntimeServerCertificate> stageServerCertificate(
    RuntimeServerCertificate current,
    List<String> hosts,
  ) async {
    lastHosts = hosts;
    certificate = RuntimeServerCertificate.fromJson({
      ...certificatePayload(),
      'managed': true,
      'ca': caPayload(),
      'pending': {...certificatePayload(seed: 1), 'issuedByCA': true},
    }, 'certificate');
    return certificate;
  }

  @override
  Future<RuntimeServerCertificate> applyServerCertificate(
    RuntimeServerCertificate current,
  ) async {
    applies++;
    certificate = RuntimeServerCertificate.fromJson({
      ...certificatePayload(seed: 1),
      'managed': true,
      'ca': caPayload(),
      'issuedByCA': true,
    }, 'certificate');
    return certificate;
  }

  @override
  Future<RuntimeServerCertificate> serverCertificate() async {
    calls++;
    if (fail) throw StateError('fixture failure');
    return certificate;
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

final class _RecordingExporter implements PublicCertificateExporter {
  final saved = <PublicCertificate>[];
  @override
  Future<bool> save(PublicCertificate certificate) async {
    saved.add(certificate);
    return true;
  }
}

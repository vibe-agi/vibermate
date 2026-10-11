import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/egress_profile_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';
import 'egress_default_api_test.dart' show defaultWire;
import 'vless_egress_api_test.dart' show savedWire, syntheticNode;

void main() {
  for (final mode in ['unchanged', 'system', 'replace', 'back-to-saved']) {
    testWidgets(
      'saved custom CA $mode keeps revision and credentials private',
      (tester) async {
        final h = await _mount(tester);
        h.a.description = () async => VlessProfileDescription.fromJson({
          'security': 'tls',
          'trustMode': 'custom_ca',
        }, 'settings');
        await h.open(tester, id: 'profile.fixture');
        expect(h.a.descriptionReads, [('profile.fixture', 1)]);
        expect(
          find.text('A custom CA is saved. It will be kept unless replaced.'),
          findsOneWidget,
        );
        if (mode == 'system' || mode == 'back-to-saved') {
          await _choose(tester, 'vless-tls-mode', 'vless-tls-system');
          if (mode == 'back-to-saved') {
            await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
          }
        } else if (mode == 'replace') {
          await tester.ensureVisible(
            find.byKey(const Key('vless-tls-paste-toggle')),
          );
          await tester.tap(find.byKey(const Key('vless-tls-paste-toggle')));
          await tester.pumpAndSettle();
          await tester.enterText(
            find.byKey(const Key('vless-tls-pem')),
            'fixture new CA',
          );
        }
        await h.save(tester);
        final write = h.a.writes.single;
        expect(write.headers['if-match'], '1');
        final body = jsonDecode(write.body) as Map<String, dynamic>;
        if (mode == 'unchanged' || mode == 'back-to-saved') {
          expect(write.method, 'PUT');
          expect(body['policy']['proxy'], {
            'kind': 'vless',
            'credentialRef': 'secret://egress-vless/fixture',
            'credentialRevision': 1,
          });
          expect(body.containsKey('tls'), isFalse);
        } else {
          expect(write.method, 'POST');
          expect(body['id'], 'profile.fixture');
          expect(body['uri'], '');
          expect(
            body['tls'],
            mode == 'system'
                ? {'mode': 'system'}
                : {'mode': 'custom_ca', 'caPem': 'fixture new CA'},
          );
        }
        _expectSafeSurface(tester);
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets(
    'saved description failure blocks save and retry restores exact mode',
    (tester) async {
      final h = await _mount(tester);
      h.a.description = () async =>
          throw const ControlContractException('invalid fixture');
      await h.open(tester, id: 'profile.fixture');
      expect(find.text('Use system certificates'), findsNothing);
      expect(
        tester
            .widget<FilledButton>(
              find.byKey(const Key('egress-profile-publish')),
            )
            .onPressed,
        isNull,
      );
      expect(h.a.writes, isEmpty);
      h.a.description = () async => VlessProfileDescription.fromJson({
        'security': 'tls',
        'trustMode': 'custom_ca',
      }, 'settings');
      await tester.ensureVisible(find.byKey(const Key('vless-settings-retry')));
      await tester.tap(find.byKey(const Key('vless-settings-retry')));
      await tester.pumpAndSettle();
      expect(
        find.text('A custom CA is saved. It will be kept unless replaced.'),
        findsOneWidget,
      );
      await h.save(tester);
      expect(h.a.writes.single.method, 'PUT');
    },
  );
  for (final action in ['new-runtime', 'aba', 'member', 'dispose']) {
    testWidgets(
      'pending saved description $action cannot revive old trust intent',
      (tester) async {
        final h = await _mount(tester, memberB: action == 'member');
        final pending = Completer<VlessProfileDescription>();
        h.a.description = () => pending.future;
        await tester.tap(
          find.byKey(const Key('egress-profile-edit-profile.fixture')),
        );
        await tester.pump();
        expect(
          tester
              .widget<FilledButton>(
                find.byKey(const Key('egress-profile-publish')),
              )
              .onPressed,
          isNull,
        );
        if (action == 'dispose') {
          await tester.pumpWidget(const SizedBox.shrink());
        } else {
          h.rebuild(() => h.current = h.controllerB);
          await tester.pump();
          if (action == 'aba') {
            h.rebuild(() => h.current = h.controllerA);
            await tester.pump();
          }
        }
        pending.complete(
          VlessProfileDescription.fromJson({
            'security': 'tls',
            'trustMode': 'custom_ca',
          }, 'settings'),
        );
        await tester.pumpAndSettle();
        expect(h.a.writes, isEmpty);
        expect(h.b.writes, isEmpty);
        if (action != 'dispose') {
          expect(
            find.text('A custom CA is saved. It will be kept unless replaced.'),
            findsNothing,
          );
          expect(
            tester
                .widget<FilledButton>(
                  find.byKey(const Key('egress-profile-publish')),
                )
                .onPressed,
            isNull,
          );
        }
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets('saved trust failure is unconfirmed and never replays', (
    tester,
  ) async {
    final h = await _mount(tester);
    h.a.description = () async => VlessProfileDescription.fromJson({
      'security': 'tls',
      'trustMode': 'custom_ca',
    }, 'settings');
    h.a.failureStatus = 503;
    await h.open(tester, id: 'profile.fixture');
    await _choose(tester, 'vless-tls-mode', 'vless-tls-system');
    await h.save(tester);
    expect(h.a.writes, hasLength(1));
    expect(jsonDecode(h.a.writes.single.body)['uri'], '');
    expect(
      find.text(
        'The save outcome is unknown. Refresh and confirm the profile before trying again.',
      ),
      findsOneWidget,
    );
    await tester.pumpAndSettle();
    expect(h.a.writes, hasLength(1));
    _expectSafeSurface(tester);
  });
  for (final action in ['new-runtime', 'aba', 'member', 'dispose']) {
    for (final fail in [false, true]) {
      testWidgets(
        'pending TLS trust save $action ${fail ? 'failure' : 'ack'} cannot change current Runtime',
        (tester) async {
          final h = await _mount(tester, memberB: action == 'member');
          h.a.description = () async => VlessProfileDescription.fromJson({
            'security': 'tls',
            'trustMode': 'custom_ca',
          }, 'settings');
          await h.open(tester, id: 'profile.fixture');
          await _choose(tester, 'vless-tls-mode', 'vless-tls-system');
          h.a.pending = Completer<void>();
          h.a.failureStatus = fail ? 503 : null;
          await h.save(tester);
          expect(h.a.writes, hasLength(1));
          expect(jsonDecode(h.a.writes.single.body)['uri'], '');
          if (action == 'dispose') {
            await tester.pumpWidget(const SizedBox.shrink());
          } else {
            await h.replace(tester, h.controllerB);
            if (action == 'aba') await h.replace(tester, h.controllerA);
          }
          final aReads = h.a.reads, bReads = h.b.reads;
          h.a.pending!.complete();
          await tester.pumpAndSettle();
          expect(h.a.reads, aReads);
          expect(h.b.reads, bReads);
          expect(h.a.writes, hasLength(1));
          expect(h.b.writes, isEmpty);
          expect(
            find.byKey(const Key('egress-profile-mutation-error')),
            findsNothing,
          );
          expect(tester.takeException(), isNull);
        },
      );
    }
  }
  test('unknown VLESS security has an honest generic summary', () {
    final profile = EgressProfileRevision.fromJson(savedWire(), 'profile');
    final copy = AppCopy.forLanguage(AppLanguage.english);
    expect(
      egressPolicySummary(copy, profile.policy),
      isNot(contains('REALITY')),
    );
    expect(egressProfileSummary(copy, profile), isNot(contains('REALITY')));
  });
  testWidgets('TLS node link reveals certificate trust controls', (
    tester,
  ) async {
    final h = await _mount(tester);
    await h.open(tester);
    await h.chooseVless(tester);
    await tester.enterText(
      find.byKey(const Key('egress-profile-vless-uri')),
      'vless://00000000-0000-4000-8000-000000000001@node.invalid:443?security=tls',
    );
    await tester.pump();
    expect(find.text('Server certificate'), findsOneWidget);
  });
  TestWidgetsFlutterBinding.ensureInitialized();
  final reviewDirectory = kIsWeb
      ? null
      : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
  setUpAll(() async {
    if (reviewDirectory == null) return;
    for (final font in {
      viberSystemFontFamily: '/System/Library/Fonts/Helvetica.ttc',
      'Ahem': '/System/Library/Fonts/Helvetica.ttc',
      'Roboto': '/System/Library/Fonts/Helvetica.ttc',
      'PingFang SC': Platform.environment['VIBERMATE_QA_CJK_FONT'],
      'Hiragino Sans GB': '/System/Library/Fonts/Hiragino Sans GB.ttc',
    }.entries) {
      if (font.value == null) continue;
      final loader = FontLoader(font.key)
        ..addFont(File(font.value!).readAsBytes().then(ByteData.sublistView));
      await loader.load();
    }
    await (FontLoader(
      'MaterialIcons',
    )..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'))).load();
  });
  testWidgets('390px Chinese saved TLS certificate frame', (tester) async {
    final h = await _mount(
      tester,
      language: AppLanguage.simplifiedChinese,
      size: const Size(390, 844),
    );
    h.a.description = () async => VlessProfileDescription.fromJson({
      'security': 'tls',
      'trustMode': 'custom_ca',
    }, 'settings');
    await h.open(tester, id: 'profile.fixture');
    await tester.ensureVisible(find.byKey(const Key('vless-tls-file')));
    await tester.pumpAndSettle();
    expect(
      tester.getRect(find.byKey(const Key('egress-profile-publish'))).right,
      lessThanOrEqualTo(390),
    );
    expect(find.text('已保存自有 CA；不更换会继续使用。'), findsOneWidget);
    if (reviewDirectory != null) {
      await tester.runAsync(() async {
        final boundary = tester.renderObject<RenderRepaintBoundary>(
          find.byKey(const Key('vless-visual-review')),
        );
        final image = await boundary.toImage(pixelRatio: 1);
        try {
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          await File(
            '$reviewDirectory/vless-tls-editor-390-zh-system-font.png',
          ).writeAsBytes(bytes!.buffer.asUint8List());
        } finally {
          image.dispose();
        }
      });
    }
    await h.save(tester);
    expect(h.a.writes.single.method, 'PUT');
    expect(tester.takeException(), isNull);
  });
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets(
      '$language import, public edit, replacement keep selected default frozen',
      (tester) async {
        final h = await _mount(tester, language: language);
        final initialDefault = await h.a.transport.egressDefault();
        await h.open(tester);
        expect(
          find.descendant(
            of: find.byKey(const Key('egress-profile-publish')),
            matching: find.text(AppCopy.forLanguage(language)('common.save')),
          ),
          findsOneWidget,
        );
        await h.enterPrivate(tester);
        final field = tester.widget<TextField>(
          find.descendant(
            of: find.byKey(const Key('egress-profile-vless-uri')),
            matching: find.byType(TextField),
          ),
        );
        expect(field.obscureText, isTrue);
        await h.save(tester);
        expect(h.a.writes, hasLength(1));
        final created =
            jsonDecode(h.a.writes.single.body) as Map<String, dynamic>;
        final id = created['id'] as String;
        expect(id, startsWith('profile.custom.'));
        expect(h.a.writes.single.method, 'POST');
        expect(h.a.writes.single.headers['if-match'], '0');
        expect(created['uri'], syntheticNode);
        expect(h.a.profiles[id]!['policy'], savedWire()['policy']);
        _expectSafeSurface(tester);
        await h.open(tester, id: id);
        expect(
          find.text(
            language == AppLanguage.english ? 'Edit network exit' : '编辑网络出口方案',
          ),
          findsOneWidget,
        );
        expect(
          tester
              .widget<TextFormField>(
                find.byKey(const Key('egress-profile-vless-uri')),
              )
              .controller!
              .text,
          isEmpty,
        );
        await tester.enterText(
          find.byKey(const Key('egress-profile-direct-domains')),
          'example.com',
        );
        await tester.enterText(
          find.byKey(const Key('egress-profile-display-name')),
          'Edited exit',
        );
        await _choose(
          tester,
          'egress-profile-resolver-kind',
          'egress-profile-resolver-doh',
        );
        await _choose(
          tester,
          'egress-profile-doh-preset',
          'egress-profile-doh-cloudflare',
        );
        await _choose(
          tester,
          'egress-profile-doh-transport',
          'egress-profile-doh-via-proxy',
        );
        await h.save(tester);
        expect(h.a.writes, hasLength(2));
        final update = h.a.writes.last;
        expect(update.method, 'PUT');
        expect(update.url.path, '/api/v1/egress-profiles/$id');
        expect(update.headers['if-match'], '1');
        final public = jsonDecode(update.body) as Map<String, dynamic>;
        expect(public['policy']['proxy'], {
          'kind': 'vless',
          'credentialRef': 'secret://egress-vless/fixture',
          'credentialRevision': 1,
        });
        expect(public['policy']['directDomains'], 'example.com');
        expect(public['displayName'], 'Edited exit');
        expect(public['policy']['resolver'], {
          'kind': 'doh',
          'dohUrl': 'https://1.1.1.1/dns-query',
          'transport': 'proxy',
        });
        expect(update.body, isNot(contains(syntheticNode)));
        await h.open(tester, id: id);
        await tester.enterText(
          find.byKey(const Key('egress-profile-vless-uri')),
          syntheticNode,
        );
        await h.save(tester);
        expect(h.a.writes, hasLength(3));
        expect(h.a.writes.last.method, 'POST');
        expect(jsonDecode(h.a.writes.last.body)['id'], id);
        expect(h.a.writes.last.headers['if-match'], '2');
        final after = await h.a.transport.egressDefault();
        expect(after.revision, initialDefault.revision);
        expect(after.profile, initialDefault.profile);
        _expectSafeSurface(tester);
        expect(tester.takeException(), isNull);
      },
    );
  }
  for (final action in ['cancel', 'unmount', 'new-runtime', 'aba']) {
    testWidgets('private draft $action sends no mutation', (tester) async {
      final h = await _mount(tester);
      await h.open(tester);
      await h.enterPrivate(tester);
      if (action == 'cancel') {
        await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      } else if (action == 'unmount') {
        await tester.pumpWidget(const SizedBox.shrink());
      } else {
        await h.replace(tester, h.controllerB);
        if (action == 'aba') await h.replace(tester, h.controllerA);
        await h.save(tester);
      }
      await tester.pumpAndSettle();
      expect(h.a.writes, isEmpty);
      expect(h.b.writes, isEmpty);
      expect(tester.takeException(), isNull);
    });
  }
  testWidgets('same controller rebuild preserves private intent', (
    tester,
  ) async {
    final h = await _mount(tester);
    await h.open(tester);
    await h.enterPrivate(tester);
    await h.replace(tester, h.controllerA);
    await h.save(tester);
    expect(h.a.writes, hasLength(1));
    expect(h.b.writes, isEmpty);
  });
  testWidgets('owner draft cannot write after switching to member', (
    tester,
  ) async {
    final h = await _mount(tester, memberB: true);
    await h.open(tester);
    await h.enterPrivate(tester);
    await h.replace(tester, h.controllerB);
    await h.save(tester);
    expect(h.a.writes, isEmpty);
    expect(h.b.writes, isEmpty);
    expect(find.byKey(const Key('egress-profile-add')), findsNothing);
  });
  testWidgets(
    'private acknowledgement after unmount causes no reload or state error',
    (tester) async {
      final h = await _mount(tester);
      h.a.pending = Completer<void>();
      await h.open(tester);
      await h.enterPrivate(tester);
      await h.save(tester);
      final reads = h.a.reads;
      await tester.pumpWidget(const SizedBox.shrink());
      h.a.pending!.complete();
      await tester.pumpAndSettle();
      expect(h.a.reads, reads);
      expect(h.a.writes, hasLength(1));
      expect(tester.takeException(), isNull);
    },
  );
  for (final failure in [false, true]) {
    testWidgets(
      'late private ${failure ? 'failure' : 'ack'} cannot alter replacement controller saving',
      (tester) async {
        final h = await _mount(tester);
        h.a.pending = Completer<void>();
        h.a.failureStatus = failure ? 503 : null;
        await h.open(tester);
        await h.enterPrivate(tester);
        await h.save(tester);
        expect(h.a.writes, hasLength(1));
        await h.replace(tester, h.controllerB);
        h.b.pending = Completer<void>();
        await h.open(tester);
        await h.enterPrivate(tester);
        await h.save(tester);
        final reads = h.b.reads;
        h.a.pending!.complete();
        await tester.pumpAndSettle();
        expect(h.b.reads, reads);
        expect(
          tester
              .widget<FilledButton>(find.byKey(const Key('egress-profile-add')))
              .onPressed,
          isNull,
        );
        expect(
          find.byKey(const Key('egress-profile-mutation-error')),
          findsNothing,
        );
        h.b.pending!.complete();
        await tester.pumpAndSettle();
        expect(h.b.writes, hasLength(1));
        expect(
          tester
              .widget<FilledButton>(find.byKey(const Key('egress-profile-add')))
              .onPressed,
          isNotNull,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }
  for (final status in [422, 409, 503]) {
    testWidgets('private error $status is safe and does not replay', (
      tester,
    ) async {
      final h = await _mount(tester);
      h.a.failureStatus = status;
      await h.open(tester);
      await h.enterPrivate(tester);
      await h.save(tester);
      expect(h.a.writes, hasLength(1));
      expect(
        find.byKey(const Key('egress-profile-mutation-error')),
        findsOneWidget,
      );
      expect(
        find.text(
          status == 422
              ? 'Check the node link and certificate settings. TLS and REALITY + Vision nodes are supported.'
              : 'The save outcome is unknown. Refresh and confirm the profile before trying again.',
        ),
        findsOneWidget,
      );
      _expectSafeSurface(tester);
      expect(tester.takeException(), isNull);
    });
  }
  testWidgets('new VLESS requires a link and uses no placeholder reference', (
    tester,
  ) async {
    final h = await _mount(tester);
    await h.open(tester);
    await h.chooseVless(tester);
    await h.save(tester);
    expect(h.a.writes, isEmpty);
    expect(
      find.byKey(const Key('settings-egress-profile-dialog')),
      findsOneWidget,
    );
    expect(
      find.text(
        'Check the node link and certificate settings. TLS and REALITY + Vision nodes are supported.',
      ),
      findsOneWidget,
    );
  });
  testWidgets('preview refuses private persistence', (tester) async {
    final preview = PreviewControlApi();
    addTearDown(preview.close);
    await expectLater(
      preview.importVlessEgressProfile(
        id: 'profile.fixture',
        expectedRevision: 0,
        displayName: 'Chosen',
        uri: syntheticNode,
        resolver: const TrafficResolverPolicy.system(),
        directDomains: '',
      ),
      throwsA(isA<UnsupportedError>()),
    );
  });
  testWidgets(
    '390px Chinese fields and save remain reachable without overflow',
    (tester) async {
      final h = await _mount(
        tester,
        language: AppLanguage.simplifiedChinese,
        size: const Size(390, 844),
      );
      await h.open(tester);
      await h.enterPrivate(tester);
      expect(
        tester.getRect(find.byKey(const Key('egress-profile-publish'))).right,
        lessThanOrEqualTo(390),
      );
      await tester.ensureVisible(
        find.byKey(const Key('egress-profile-direct-domains')),
      );
      expect(tester.takeException(), isNull);
      if (reviewDirectory != null) {
        await tester.runAsync(() async {
          final boundary = tester.renderObject<RenderRepaintBoundary>(
            find.byKey(const Key('vless-visual-review')),
          );
          final image = await boundary.toImage(pixelRatio: 1);
          try {
            final bytes = await image.toByteData(
              format: ui.ImageByteFormat.png,
            );
            await File(
              '$reviewDirectory/vless-editor-390-zh-system-font.png',
            ).writeAsBytes(bytes!.buffer.asUint8List());
          } finally {
            image.dispose();
          }
        });
      } else if (!kIsWeb) {
        await expectLater(
          find.byType(MaterialApp),
          matchesGoldenFile('goldens/vless-editor-390-zh.png'),
        );
      }
      await h.save(tester);
      expect(h.a.writes, hasLength(1));
      expect(tester.takeException(), isNull);
    },
  );
  testWidgets('owner can choose REALITY and enter a private node link', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              onPressed: () => showEgressProfileEditor(
                context: context,
                copy: AppCopy.forLanguage(AppLanguage.english),
              ),
              child: const Text('Open'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Open'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-proxy-kind')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('egress-profile-proxy-vless')), findsOneWidget);
    await tester.tap(find.byKey(const Key('egress-profile-proxy-vless')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('egress-profile-vless-uri')), findsOneWidget);
  });
}

void _expectSafeSurface(WidgetTester tester) {
  final visible = tester
      .widgetList<Text>(find.byType(Text))
      .map((text) => text.data ?? '')
      .join('\n');
  for (final secret in [
    syntheticNode,
    '00000000-0000-4000-8000-000000000001',
    'secret://egress-vless/fixture',
  ]) {
    expect(visible, isNot(contains(secret)));
  }
}

Future<void> _choose(WidgetTester tester, String picker, String option) async {
  final control = find.byKey(Key(picker));
  await tester.ensureVisible(control);
  await tester.tap(control);
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(Key(option)).last);
  await tester.pumpAndSettle();
}

final class _OwnerApi implements ControlApi {
  ControlApi get transport => this;
  final profiles = <String, Map<String, Object?>>{
    'profile.fixture': savedWire(),
  };
  final writes = <http.Request>[];
  int reads = 0;
  Completer<void>? pending;
  int? failureStatus;
  Future<EgressProfileRevision> _write(
    String method,
    String id,
    int expectedRevision,
    Map<String, Object?> body,
  ) async {
    writes.add(
      http.Request(
          method,
          Uri.parse(
            'http://fixture.invalid/api/v1/egress-profiles/${method == 'POST' ? 'import-vless' : id}',
          ),
        )
        ..headers['if-match'] = '$expectedRevision'
        ..body = jsonEncode(body),
    );
    if (pending != null) await pending!.future;
    if (failureStatus != null) {
      throw ControlProblem(
        status: failureStatus!,
        reasonCode: 'invalid_request',
        messageKey: syntheticNode,
      );
    }
    final profile = savedWire(id: id, revision: expectedRevision + 1)
      ..['displayName'] = body['displayName'];
    if (method == 'PUT') profile['policy'] = body['policy'];
    profiles[id] = profile;
    return EgressProfileRevision.fromJson(profile, 'profile');
  }

  @override
  Future<EgressProfileCatalog> egressProfiles() async {
    reads++;
    return EgressProfileCatalog.fromJson({
      'items': profiles.values.toList(),
    }, 'profiles');
  }

  @override
  Future<EgressDefaultSetting> egressDefault() async {
    reads++;
    return EgressDefaultSetting.fromJson(
      defaultWire(
        profile: EgressProfileRevision.fromJson(savedWire(), 'profile'),
      ),
    );
  }

  @override
  Future<EgressProfileRevision> publishEgressProfile({
    required String id,
    required int expectedRevision,
    required String displayName,
    required TrafficEgressPolicy policy,
  }) => _write('PUT', id, expectedRevision, {
    'displayName': displayName,
    'policy': policy.toJson(),
  });
  @override
  Future<EgressProfileRevision> importVlessEgressProfile({
    required String id,
    required int expectedRevision,
    required String displayName,
    required String uri,
    required TrafficResolverPolicy resolver,
    required String directDomains,
    VlessTLSImportOptions? tls,
  }) => _write('POST', id, expectedRevision, {
    'id': id,
    'displayName': displayName,
    'uri': uri,
    'resolver': resolver.toJson(),
    'directDomains': directDomains,
    if (tls != null)
      'tls': {'mode': tls.mode, if (tls.caPem != null) 'caPem': tls.caPem},
  });
  @override
  Future<VlessProfileDescription> vlessProfileDescription(
    String id,
    int revision,
  ) async {
    descriptionReads.add((id, revision));
    return description();
  }

  final descriptionReads = <(String, int)>[];
  Future<VlessProfileDescription> Function() description = () async =>
      VlessProfileDescription.fromJson({'security': 'reality'}, 'settings');
  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('Unexpected API call');
}

final class _Harness {
  _Harness(this.a, this.b, this.controllerA, this.controllerB)
    : current = controllerA;
  final _OwnerApi a, b;
  final WorkbenchController controllerA, controllerB;
  WorkbenchController current;
  late StateSetter rebuild;
  Future<void> replace(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    rebuild(() => current = controller);
    await tester.pumpAndSettle();
  }

  Future<void> open(WidgetTester tester, {String? id}) async {
    final action = find.byKey(
      Key(id == null ? 'egress-profile-add' : 'egress-profile-edit-$id'),
    );
    await tester.ensureVisible(action);
    await tester.tap(action);
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('egress-profile-display-name')),
      'Chosen exit',
    );
  }

  Future<void> chooseVless(WidgetTester tester) async {
    await tester.tap(find.byKey(const Key('egress-profile-proxy-kind')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('egress-profile-proxy-vless')).last);
    await tester.pumpAndSettle();
  }

  Future<void> enterPrivate(WidgetTester tester) async {
    await chooseVless(tester);
    await tester.ensureVisible(
      find.byKey(const Key('egress-profile-vless-uri')),
    );
    await tester.enterText(
      find.byKey(const Key('egress-profile-vless-uri')),
      syntheticNode,
    );
    await tester.ensureVisible(
      find.byKey(const Key('egress-profile-direct-domains')),
    );
    await tester.enterText(
      find.byKey(const Key('egress-profile-direct-domains')),
      'github.com\n*.github.com\ngithub.com',
    );
    await tester.pumpAndSettle();
  }

  Future<void> save(WidgetTester tester) async {
    await tester.tap(find.byKey(const Key('egress-profile-publish')));
    await tester.pumpAndSettle();
  }
}

Future<_Harness> _mount(
  WidgetTester tester, {
  AppLanguage language = AppLanguage.english,
  Size size = const Size(1100, 1000),
  bool memberB = false,
}) async {
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final a = _OwnerApi(), b = _OwnerApi();
  for (final api in [a, b]) {
    addTearDown(() async {
      if (api.pending case final pending? when !pending.isCompleted) {
        pending.complete();
      }
    });
  }
  final preview = PreviewControlApi(seedCaptures: false);
  addTearDown(preview.close);
  final dashboard = await preview.loadDashboard();
  WorkbenchController controller(_OwnerApi api) {
    final result = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: false,
      closeRuntime: () async {},
      webPrincipal: memberB && identical(api, b)
          ? const RuntimeWebPrincipal(
              id: 'member.fixture',
              username: 'Fixture member',
              role: RuntimeWebRole.member,
            )
          : null,
    );
    result.data = dashboard;
    result.loading = false;
    result.selectSettingsTab(result.settingsDestinations.length - 1);
    addTearDown(result.dispose);
    return result;
  }

  final h = _Harness(a, b, controller(a), controller(b));
  await tester.pumpWidget(
    RepaintBoundary(
      key: const Key('vless-visual-review'),
      child: MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: StatefulBuilder(
            builder: (_, setState) {
              h.rebuild = setState;
              return SettingsView(
                controller: h.current,
                copy: AppCopy.forLanguage(language),
              );
            },
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return h;
}

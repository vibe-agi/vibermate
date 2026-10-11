import 'dart:io';
import 'dart:ui' as ui;
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/captures_view.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final reviewDirectory = kIsWeb
      ? null
      : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
  setUpAll(() async {
    if (reviewDirectory == null) return;
    for (final font in {
      viberSystemFontFamily: '/System/Library/Fonts/Supplemental/Arial.ttf',
      'Menlo': '/System/Library/Fonts/Supplemental/Courier New.ttf',
      'Ahem': '/System/Library/Fonts/Supplemental/Arial.ttf',
      'Roboto': '/System/Library/Fonts/Supplemental/Arial.ttf',
      'PingFang SC': Platform.environment['VIBERMATE_QA_CJK_FONT'],
      'Hiragino Sans GB': '/System/Library/Fonts/Hiragino Sans GB.ttc',
    }.entries) {
      if (font.value == null) continue;
      await (FontLoader(font.key)..addFont(
            File(font.value!).readAsBytes().then(ByteData.sublistView),
          ))
          .load();
    }
    await (FontLoader(
      'MaterialIcons',
    )..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'))).load();
  });
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    final copy = AppCopy.forLanguage(language);
    for (final mode in ['enabled', 'disabled', 'unknown']) {
      testWidgets('SOCKS delivery Settings $mode ${language.name} 390px', (
        tester,
      ) async {
        final api = _SocksApi(
          mode == 'unknown'
              ? null
              : HostNetworkAccess(
                  socksAddress: mode == 'enabled'
                      ? '[2001:db8:1234:5678:abcd:ef01:2345:6789]:32124'
                      : null,
                  vlessAvailable: mode == 'enabled',
                ),
        );
        final controller = await _mount(tester, api, copy, settings: true);
        expect(find.text(copy('settings.access.socks')), findsOneWidget);
        expect(
          find.text(
            mode == 'enabled'
                ? api.access!.socksAddress!
                : copy('settings.access.network.$mode'),
          ),
          findsOneWidget,
        );
        expect(
          find.text(
            copy(
              mode == 'unknown'
                  ? 'settings.access.vless.unknown'
                  : mode == 'enabled'
                  ? 'settings.access.vless.installed'
                  : 'settings.access.vless.absent',
            ),
          ),
          findsOneWidget,
        );
        expect(tester.takeException(), isNull);
        if (mode == 'enabled') {
          await _render(tester, reviewDirectory, 'settings-${language.name}');
        }
        await tester.pumpWidget(const SizedBox.shrink());
        controller.dispose();
        await api.close();
      });
    }
    for (final rotate in [false, true]) {
      testWidgets(
        'SOCKS delivery ${rotate ? 'rotate' : 'create'} ${language.name} 390px clipboard and one-time close',
        (tester) async {
          final api = _SocksApi(
            HostNetworkAccess(
              socksAddress: language == AppLanguage.english
                  ? 'private-capture-proxy-with-a-long-valid-label.example.test:32124'
                  : rotate
                  ? '[2001:db8:1234:5678:abcd:ef01:2345:6789]:32124'
                  : '[fe80::1%en0]:32124',
              vlessAvailable: true,
            ),
          );
          final controller = await _mount(tester, api, copy);
          String? clipboard;
          tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
            SystemChannels.platform,
            (call) async {
              if (call.method == 'Clipboard.setData') {
                clipboard = (call.arguments as Map)['text'] as String;
              }
              return null;
            },
          );
          addTearDown(
            () => tester.binding.defaultBinaryMessenger
                .setMockMethodCallHandler(SystemChannels.platform, null),
          );
          if (rotate) {
            final capture = controller.data!.captures.firstWhere(
              (c) => c.isManual && c.running,
            );
            controller.selectedCaptureKey = capture.key;
            // Open the real detail pane without making unrelated evidence requests.
            await tester.tap(find.text(capture.displayName).first);
            await tester.pumpAndSettle();
            await tester.tap(find.byKey(const Key('manual-capture-rotate')));
            await tester.pumpAndSettle();
            await tester.tap(
              find.byKey(const Key('manual-capture-rotate-confirm')),
            );
          } else {
            await tester.tap(find.byKey(const Key('manual-capture-create')));
            await tester.pumpAndSettle();
            expect(
              find.text(
                '${copy('settings.access.socks')}: ${api.access!.socksAddress}',
              ),
              findsOneWidget,
            );
            await tester.enterText(
              find.byKey(const Key('manual-capture-name')),
              'SOCKS client',
            );
            await tester.tap(
              find.byKey(const Key('manual-capture-create-confirm')),
            );
          }
          await tester.pumpAndSettle();
          expect(
            find.text(copy('capture.manual.delivery.socks_shared')),
            findsOneWidget,
          );
          expect(
            find.text(copy('capture.manual.delivery.socks_remote')),
            findsOneWidget,
          );
          if (!rotate) {
            await _render(tester, reviewDirectory, 'delivery-${language.name}');
          }
          for (final entry in {
            'proxy': 'http://127.0.0.1:43123',
            'socks_host': language == AppLanguage.english
                ? 'private-capture-proxy-with-a-long-valid-label.example.test'
                : rotate
                ? '2001:db8:1234:5678:abcd:ef01:2345:6789'
                : 'fe80::1%en0',
            'socks_port': '32124',
            'username': 'capture',
            'password': api.lastPassword!,
          }.entries) {
            final button = find.byTooltip(
              copy.format('common.copy', {
                'field': copy('capture.manual.delivery.${entry.key}'),
              }),
            );
            await tester.ensureVisible(button);
            await tester.pumpAndSettle();
            final rect = tester.getRect(button);
            expect(rect.left, greaterThanOrEqualTo(0));
            expect(rect.right, lessThanOrEqualTo(390));
            await tester.tap(button);
            await tester.pumpAndSettle();
            expect(clipboard, entry.value);
          }
          await tester.ensureVisible(
            find.text(copy('capture.manual.delivery.socks_advanced')),
          );
          await tester.tap(
            find.text(copy('capture.manual.delivery.socks_advanced')),
          );
          await tester.pumpAndSettle();
          expect(
            find.text(copy('capture.manual.delivery.socks_control')),
            findsOneWidget,
          );
          expect(tester.takeException(), isNull);
          await tester.ensureVisible(
            find.byKey(const Key('manual-capture-delivery-done')),
          );
          await tester.tap(
            find.byKey(const Key('manual-capture-delivery-done')),
          );
          await tester.pumpAndSettle();
          expect(find.text(api.lastPassword!), findsNothing);
          await tester.pumpWidget(const SizedBox.shrink());
          controller.dispose();
          await api.close();
        },
      );
    }
    testWidgets(
      'SOCKS delivery disabled preserves HTTP-only create ${language.name}',
      (tester) async {
        final api = _SocksApi(
          const HostNetworkAccess(socksAddress: null, vlessAvailable: false),
        );
        final controller = await _mount(tester, api, copy);
        await tester.tap(find.byKey(const Key('manual-capture-create')));
        await tester.pumpAndSettle();
        await tester.enterText(
          find.byKey(const Key('manual-capture-name')),
          'HTTP client',
        );
        await tester.tap(
          find.byKey(const Key('manual-capture-create-confirm')),
        );
        await tester.pumpAndSettle();
        expect(find.text('http://127.0.0.1:43123'), findsOneWidget);
        expect(
          find.text(copy('capture.manual.delivery.socks_host')),
          findsNothing,
        );
        expect(
          find.text(copy('capture.manual.delivery.socks_port')),
          findsNothing,
        );
        expect(
          find.text(copy('capture.manual.delivery.socks_udp')),
          findsNothing,
        );
        expect(find.text(api.lastPassword!), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
        await tester.pumpAndSettle();
        expect(find.text(api.lastPassword!), findsNothing);
        await tester.pumpWidget(const SizedBox.shrink());
        controller.dispose();
        await api.close();
      },
    );
  }
}

Future<WorkbenchController> _mount(
  WidgetTester tester,
  _SocksApi api,
  AppCopy copy, {
  bool settings = false,
}) async {
  await tester.binding.setSurfaceSize(const Size(390, 900));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: () async {},
  );
  controller.data = await api.loadDashboard();
  controller.loading = false;
  if (settings) controller.settingsTab = 1;
  final baseTheme = ViberTheme.dark();
  // Explicit null body-family styles otherwise select flutter_test's Ahem.
  // Bind the existing roles only for saved visual review, leaving normal tests
  // and the product theme untouched.
  final visualReview =
      !kIsWeb && Platform.environment['VIBERMATE_UI_REVIEW_DIR'] != null;
  final theme = visualReview
      ? baseTheme.copyWith(
          textTheme: baseTheme.textTheme.apply(
            fontFamily: viberSystemFontFamily,
          ),
          dialogTheme: baseTheme.dialogTheme.copyWith(
            contentTextStyle: baseTheme.dialogTheme.contentTextStyle!.copyWith(
              fontFamily: viberSystemFontFamily,
            ),
          ),
        )
      : baseTheme;
  await tester.pumpWidget(
    RepaintBoundary(
      key: const Key('socks-visual-review'),
      child: MaterialApp(
        theme: theme,
        home: Scaffold(
          body: AnimatedBuilder(
            animation: controller,
            builder: (_, _) => settings
                ? SettingsView(controller: controller, copy: copy)
                : CapturesView(controller: controller, copy: copy),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return controller;
}

Future<void> _render(
  WidgetTester tester,
  String? directory,
  String name,
) async {
  if (directory == null) return;
  await tester.runAsync(() async {
    final boundary = tester.renderObject<RenderRepaintBoundary>(
      find.byKey(const Key('socks-visual-review')),
    );
    final image = await boundary.toImage(pixelRatio: 1);
    try {
      final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
      await File(
        '$directory/socks-$name-390-system-font.png',
      ).writeAsBytes(bytes!.buffer.asUint8List());
    } finally {
      image.dispose();
    }
  });
}

// Complete preview transport underneath; only the new wire metadata is synthetic.
final class _SocksApi implements ControlApi {
  _SocksApi(this.access);
  final HostNetworkAccess? access;
  final delegate = PreviewControlApi();
  String? lastPassword;
  bool delivered = false;
  @override
  Future<DashboardData> loadDashboard() async {
    final data = await delegate.loadDashboard();
    final s = data.status;
    return DashboardData(
      status: RuntimeStatus(
        ready: s.ready,
        productBuild: s.productBuild,
        state: s.state,
        host: s.host,
        schemaRevision: s.schemaRevision,
        storage: s.storage,
        environmentProjection: s.environmentProjection,
        unavailableEnvironments: s.unavailableEnvironments,
        offlineHold: s.offlineHold,
        instanceId: s.instanceId,
        startedAt: s.startedAt,
        stoppedAt: s.stoppedAt,
        stopReasonCode: s.stopReasonCode,
        networkAccess: access,
      ),
      captures: delivered ? [] : data.captures,
      captureNextCursor: data.captureNextCursor,
      environments: data.environments,
      endpoints: data.endpoints,
      accounts: data.accounts,
    );
  }

  @override
  Future<ManualCaptureContext> manualCaptureContext(String id) async {
    final c = await delegate.manualCaptureContext(id);
    return ManualCaptureContext(
      confirmationToken: c.confirmationToken,
      proxyAddress: c.proxyAddress,
      socksAddress: access?.socksAddress,
      environmentId: c.environmentId,
      environmentRevision: c.environmentRevision,
      environmentDigest: c.environmentDigest,
      launchAuthorityDigest: c.launchAuthorityDigest,
      protectedAuthorities: c.protectedAuthorities,
      managedCredentialAuthorities: c.managedCredentialAuthorities,
      defaultTemporarySeconds: c.defaultTemporarySeconds,
      maxTemporarySeconds: c.maxTemporarySeconds,
      root: c.root,
    );
  }

  ManualCaptureGrantStateTag _grant(ManualCaptureGrantStateTag result) {
    final g = result.grant;
    lastPassword = g.proxyPassword;
    delivered = true;
    return ManualCaptureGrantStateTag(
      stateTag: result.stateTag,
      grant: ManualCaptureGrant(
        capture: g.capture,
        proxyAddress: g.proxyAddress,
        socksAddress: access?.socksAddress,
        proxyUsername: 'capture',
        proxyPassword: g.proxyPassword,
        environmentId: g.environmentId,
        assignmentRevision: g.assignmentRevision,
        launchAuthorityDigest: g.launchAuthorityDigest,
        protectedAuthorities: g.protectedAuthorities,
        managedCredentialAuthorities: g.managedCredentialAuthorities,
        root: g.root,
      ),
    );
  }

  @override
  Future<ManualCaptureGrantStateTag> createManualCapture({
    required ManualCaptureContext context,
    required String displayName,
    required String clientClass,
    required String lifetime,
    int? expiresInSeconds,
  }) async => _grant(
    await delegate.createManualCapture(
      context: context,
      displayName: displayName,
      clientClass: clientClass,
      lifetime: lifetime,
      expiresInSeconds: expiresInSeconds,
    ),
  );
  @override
  Future<ManualCaptureStateTag> manualCaptureState(String id) =>
      delegate.manualCaptureState(id);
  @override
  Future<CaptureAssignment> captureAssignment(String key) =>
      delegate.captureAssignment(key);
  @override
  Future<ConversationPage> conversations({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
  }) => delegate.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
  );
  @override
  Future<ActivityPage> activities({
    String? cursor,
    int limit = 50,
    String? captureRunId,
    String? manualCaptureId,
    String? conversationId,
    String? environmentId,
  }) => delegate.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: captureRunId,
    manualCaptureId: manualCaptureId,
    conversationId: conversationId,
    environmentId: environmentId,
  );
  @override
  Future<ManualCaptureGrantStateTag> rotateManualCapture(
    ManualCaptureStateTag current,
  ) async => _grant(await delegate.rotateManualCapture(current));
  @override
  Future<void> close() => delegate.close();
  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('unexpected API call ${invocation.memberName}');
}

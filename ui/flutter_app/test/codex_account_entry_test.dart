import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:file_selector_platform_interface/file_selector_platform_interface.dart';
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_account_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

final _reviewDirectory = kIsWeb
    ? null
    : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
final _reviewKey = GlobalKey();

Future<void> _review(WidgetTester tester, String name) async {
  final directory = _reviewDirectory;
  if (directory == null) return;
  await tester.runAsync(() async {
    final boundary =
        _reviewKey.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage();
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    await Directory(directory).create(recursive: true);
    await File(
      '$directory/$name.png',
    ).writeAsBytes(bytes!.buffer.asUint8List());
    image.dispose();
  });
}

final class _AuthFilePicker extends FileSelectorPlatform {
  XFile? file;
  List<XTypeGroup>? requestedTypes;
  @override
  Future<XFile?> openFile({
    List<XTypeGroup>? acceptedTypeGroups,
    String? initialDirectory,
    String? confirmButtonText,
  }) async {
    requestedTypes = acceptedTypeGroups ?? const [];
    return file;
  }
}

Future<WorkbenchController> _openEditor(
  WidgetTester tester,
  double width,
  AppLanguage language, {
  bool previewMode = true,
  PreviewControlApi? api,
}) async {
  await tester.binding.setSurfaceSize(Size(width, 900));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  api ??= PreviewControlApi(seedCaptures: false);
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: previewMode,
    closeRuntime: api.close,
  );
  addTearDown(controller.dispose);
  await controller.initialize();
  controller.selectEndpoint('target.codex.official');
  final theme = ViberTheme.dark();
  await tester.pumpWidget(
    RepaintBoundary(
      key: _reviewKey,
      child: MaterialApp(
        theme: _reviewDirectory == null
            ? theme
            : theme.copyWith(
                textTheme: theme.textTheme.apply(fontFamily: 'Review UI'),
              ),
        home: Scaffold(
          body: Builder(
            builder: (context) => TextButton(
              onPressed: () => showProviderAccountEditor(
                context,
                controller: controller,
                copy: AppCopy.forLanguage(language),
              ),
              child: const Text('Open editor'),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('Open editor'));
  await tester.pumpAndSettle();
  return controller;
}

void main() {
  if (_reviewDirectory != null && Platform.isMacOS) {
    setUpAll(() async {
      for (final family in [
        'Review UI',
        viberSystemFontFamily,
        'PingFang SC',
        'Hiragino Sans GB',
        'Menlo',
      ]) {
        final font = FontLoader(family)
          ..addFont(
            File(
              '/System/Library/Fonts/Supplemental/Arial Unicode.ttf',
            ).readAsBytes().then(ByteData.sublistView),
          );
        await font.load();
      }
      final icons = FontLoader('MaterialIcons')
        ..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
      await icons.load();
    });
  }

  testWidgets('Codex login opens the browser only after an explicit click', (
    tester,
  ) async {
    const channel = MethodChannel('plugins.flutter.io/url_launcher');
    final calls = <MethodCall>[];
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel, (
      call,
    ) async {
      calls.add(call);
      return true;
    });
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        channel,
        null,
      ),
    );
    final controller = await _openEditor(
      tester,
      1180,
      AppLanguage.english,
      previewMode: false,
    );
    await tester.tap(find.byKey(const Key('codex-oauth-start')));
    await tester.pumpAndSettle();
    expect(calls, isEmpty);
    expect(find.byKey(const Key('codex-oauth-copy')), findsOneWidget);
    final url = tester
        .widget<SelectableText>(find.byKey(const Key('codex-oauth-url')))
        .data!;
    await tester.pump(const Duration(seconds: 4));
    await tester.pumpAndSettle();
    expect(calls, isEmpty, reason: 'status polling must not open a browser');
    await tester.tap(find.byKey(const Key('codex-oauth-open')));
    await tester.pumpAndSettle();
    expect(calls, hasLength(1));
    expect(calls.single.method, 'launch');
    expect(calls.single.arguments['url'], url);
    expect(calls.single.arguments['useWebView'], isFalse);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });

  for (final language in AppLanguage.values) {
    testWidgets('account entry filters services by method in $language', (
      tester,
    ) async {
      final api = PreviewControlApi(seedCaptures: false);
      await api.createUpstreamEndpoint(
        id: 'profile.chatgpt',
        displayName: 'ChatGPT Custom',
        origin: 'https://chatgpt.com',
        backendProtocols: ['openai_responses'],
      );
      await api.createUpstreamEndpoint(
        id: 'profile.chatgpt.manual',
        displayName: 'ChatGPT Manual Only',
        origin: 'https://chatgpt.com',
        backendProtocols: ['openai_chat'],
      );
      final controller = await _openEditor(tester, 390, language, api: api);
      CompactSelectField<String> services() =>
          tester.widget(find.byKey(const Key('account-editor-service')));
      const supported = ['target.codex.official', 'profile.chatgpt'];
      expect(services().items.map((item) => item.value), supported);
      expect(
        find.text(
          language == AppLanguage.english ? 'Import auth file' : '导入授权文件',
        ),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('account-entry-manual')));
      await tester.pumpAndSettle();
      expect(
        services().items.map((item) => item.value),
        containsAll([
          ...supported,
          'target.anthropic.official',
          'target.openai.official',
          'profile.chatgpt.manual',
        ]),
      );
      await tester.tap(find.byKey(const Key('account-editor-service')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('OpenAI API').last);
      await tester.pumpAndSettle();
      expect(services().initialValue, 'target.openai.official');
      await tester.tap(find.byKey(const Key('account-entry-import')));
      await tester.pumpAndSettle();
      expect(services().items.map((item) => item.value), supported);
      expect(services().initialValue, 'target.codex.official');
      expect(
        find.byKey(const Key('account-editor-oauth-unavailable')),
        findsNothing,
      );
      expect(
        find.byKey(const Key('account-editor-load-auth-json')),
        findsOneWidget,
      );
      expect(find.text('Codex auth.json'), findsOneWidget);
      await tester.tap(find.byKey(const Key('account-editor-service')));
      await tester.pumpAndSettle();
      expect(find.text('OpenAI API'), findsNothing);
      expect(find.text('Anthropic API'), findsNothing);
      expect(find.text('ChatGPT Manual Only'), findsNothing);
      await tester.tap(find.text('ChatGPT Custom').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('account-entry-oauth')));
      await tester.pumpAndSettle();
      expect(services().initialValue, 'profile.chatgpt');
      expect(services().items.map((item) => item.value), supported);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    });
  }
  for (final width in [390.0, 1180.0]) {
    testWidgets(
      'Codex account entry separates login, import and manual at $width px',
      (tester) async {
        final controller = await _openEditor(
          tester,
          width,
          AppLanguage.simplifiedChinese,
        );
        expect(find.byKey(const Key('codex-oauth-start')), findsOneWidget);
        expect(find.byKey(const Key('account-editor-secret')), findsNothing);
        expect(find.byKey(const Key('account-editor-save')), findsNothing);
        await _review(tester, 'codex-start-${width.toInt()}');
        final initialCount = controller.data!.accounts.length;
        await tester.tap(find.byKey(const Key('codex-oauth-start')));
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('codex-oauth-open')), findsOneWidget);
        await _review(tester, 'codex-login-${width.toInt()}');
        final authorization = Uri.parse(
          tester
              .widget<SelectableText>(find.byKey(const Key('codex-oauth-url')))
              .data!,
        );
        final callback =
            Uri.parse(authorization.queryParameters['redirect_uri']!).replace(
              queryParameters: {
                'code': 'synthetic-code',
                'state': authorization.queryParameters['state']!,
              },
            );
        expect(controller.data!.accounts.length, initialCount);
        await tester.enterText(
          find.byKey(const Key('codex-oauth-callback')),
          callback.toString(),
        );
        await tester.ensureVisible(find.byKey(const Key('codex-oauth-submit')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('codex-oauth-submit')));
        await tester.pumpAndSettle();
        expect(find.byType(AlertDialog), findsNothing);
        expect(controller.data!.accounts.length, initialCount + 1);
        final account = controller.data!.accounts.singleWhere(
          (value) => value.kind == 'codex_oauth',
        );
        expect(account.linkedEndpointIds, isEmpty);
        expect(account.codexOAuth!.chatgptAccountId, 'workspace-preview');
        expect(find.textContaining('synthetic-access'), findsNothing);
        expect(find.textContaining('synthetic-refresh'), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        controller.dispose();
      },
    );
  }

  testWidgets(
    'cancelled Codex login creates nothing and unlocks manual entry',
    (tester) async {
      final controller = await _openEditor(tester, 390, AppLanguage.english);
      final initialCount = controller.data!.accounts.length;
      await tester.tap(find.byKey(const Key('codex-oauth-start')));
      await tester.pumpAndSettle();
      await tester.ensureVisible(find.byKey(const Key('codex-oauth-cancel')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('codex-oauth-cancel')));
      await tester.pumpAndSettle();
      await tester.ensureVisible(find.byKey(const Key('account-entry-manual')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('account-entry-manual')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('account-editor-secret')), findsOneWidget);
      expect(find.byKey(const Key('codex-oauth-start')), findsNothing);
      await _review(tester, 'codex-manual-390');
      expect(controller.data!.accounts.length, initialCount);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      controller.dispose();
    },
  );

  testWidgets('auth.json file import is explicit, masked and bounded', (
    tester,
  ) async {
    final picker = _AuthFilePicker();
    final original = FileSelectorPlatform.instance;
    FileSelectorPlatform.instance = picker;
    addTearDown(() => FileSelectorPlatform.instance = original);
    final controller = await _openEditor(
      tester,
      390,
      AppLanguage.simplifiedChinese,
    );
    await tester.tap(find.byKey(const Key('account-entry-import')));
    await tester.pumpAndSettle();
    await _review(tester, 'codex-import-390');
    final raw = jsonEncode({
      'auth_mode': 'chatgpt',
      'OPENAI_API_KEY': null,
      'tokens': {
        'id_token': 'synthetic-id',
        'access_token': 'synthetic-access',
        'refresh_token': 'synthetic-refresh',
        'account_id': 'workspace-file',
      },
      'last_refresh': DateTime.now().toUtc().toIso8601String(),
    });
    picker.file = XFile.fromData(
      Uint8List.fromList(utf8.encode(raw)),
      name: 'auth.json',
    );
    final field = tester.widget<TextFormField>(
      find.byKey(const Key('account-editor-codex-auth-json')),
    );
    await tester.tap(find.byKey(const Key('account-editor-load-auth-json')));
    // Browser FileReader uses real events while widget continuations use the
    // fake clock. Advance both until import finishes, with a bounded wait.
    for (
      var attempt = 0;
      attempt < 100 && field.controller!.text != raw;
      attempt++
    ) {
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 50)),
      );
      await tester.pump();
    }
    await tester.pumpAndSettle();
    expect(picker.requestedTypes!.single.extensions, ['json']);
    expect(field.controller!.text, raw);
    final editable = tester.widget<EditableText>(
      find.descendant(
        of: find.byKey(const Key('account-editor-codex-auth-json')),
        matching: find.byType(EditableText),
      ),
    );
    expect(editable.obscureText, isTrue);
    picker.file = XFile.fromData(
      Uint8List(32 * 1024 + 1),
      name: 'too-large.json',
    );
    await tester.tap(find.byKey(const Key('account-editor-load-auth-json')));
    await tester.pumpAndSettle();
    expect(find.textContaining('无法导入此文件'), findsOneWidget);
    expect(
      field.controller!.text,
      raw,
      reason: 'invalid input must not replace the previously selected file',
    );
    await tester.enterText(
      find.byKey(const Key('account-editor-name')),
      'Imported Codex',
    );
    await tester.tap(find.byKey(const Key('account-editor-save')));
    await tester.pumpAndSettle();
    final account = controller.data!.accounts.singleWhere(
      (account) => account.displayName == 'Imported Codex',
    );
    expect(account.kind, 'codex_oauth');
    expect(account.codexOAuth!.chatgptAccountId, 'workspace-file');
    expect(account.linkedEndpointIds, isEmpty);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    controller.dispose();
  });
}

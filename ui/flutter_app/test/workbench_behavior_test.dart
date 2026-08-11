import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';

void main() {
  testWidgets('800px title bar keeps every global command operable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(800, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('offline-hold-command')), findsOneWidget);
    expect(find.byKey(const Key('approval-attention')), findsOneWidget);
    expect(find.byIcon(Icons.science_outlined), findsOneWidget);
    expect(find.byIcon(Icons.refresh), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('keyboard navigation and screen-reader authority stay explicit', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(800, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final semantics = tester.ensureSemantics();
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.bySemanticsLabel(RegExp(r'^Captures\s+⌘1$')), findsOneWidget);
    expect(
      find.bySemanticsLabel(RegExp(r'^Hold network · Online$')),
      findsOneWidget,
    );
    final scaffoldContext = tester.element(find.byType(Scaffold).first);
    expect(Theme.of(scaffoldContext).focusColor, isNot(ViberColors.selection));

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.digit2);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pumpAndSettle();
    expect(find.text('CONVERSATIONS'), findsOneWidget);

    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    expect(FocusManager.instance.primaryFocus, isNotNull);
    expect(tester.takeException(), isNull);
    semantics.dispose();
  });

  testWidgets('Offline hold requires review before global traffic changes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    final command = find.byKey(const Key('offline-hold-command'));
    expect(command, findsOneWidget);
    expect(find.text('Hold network'), findsOneWidget);

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('offline-confirmation')), findsOneWidget);
    expect(find.text('Enter offline hold?'), findsOneWidget);
    expect(find.text('Hold network'), findsOneWidget);
    await tester.tap(find.text('Cancel').last);
    await tester.pumpAndSettle();
    expect(find.text('Hold network'), findsOneWidget);

    await tester.tap(command);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(find.text('Resume online'), findsOneWidget);

    await tester.tap(command);
    await tester.pumpAndSettle();
    expect(find.text('Resume external work?'), findsOneWidget);
    await tester.tap(find.byKey(const Key('offline-confirm-action')));
    await tester.pumpAndSettle();
    expect(find.text('Hold network'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Chinese Offline hold evidence stays operable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('offline-settings-panel')), findsOneWidget);
    expect(find.text('断网保持'), findsOneWidget);
    expect(find.text('在线'), findsOneWidget);
    final action = find.byKey(const Key('offline-settings-action'));
    await tester.ensureVisible(action);
    await tester.tap(action);
    await tester.pumpAndSettle();
    expect(find.text('进入断网保持？'), findsOneWidget);
    expect(find.byKey(const Key('offline-confirm-action')), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'Terminal command install and removal require review and preserve ownership boundaries',
    (tester) async {
      String? copiedCommand;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedCommand =
                (call.arguments as Map<Object?, Object?>)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1000, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.settings_outlined).first);
      await tester.pumpAndSettle();
      final panel = find.byKey(const Key('terminal-command-panel'));
      expect(panel, findsOneWidget);
      await tester.ensureVisible(panel);
      expect(find.text('Not installed'), findsOneWidget);
      expect(find.byKey(const Key('terminal-command-install')), findsOneWidget);
      expect(
        tester
            .widget<IconButton>(
              find.byKey(const Key('managed-run-copy-claude')),
            )
            .onPressed,
        isNull,
      );

      await tester.tap(find.byKey(const Key('terminal-command-install')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('terminal-command-confirmation')),
        findsOneWidget,
      );
      expect(find.text('Install vibermate for Terminal?'), findsOneWidget);
      expect(
        find.textContaining('No existing object will be replaced.'),
        findsOneWidget,
      );
      await tester.tap(find.text('Cancel').last);
      await tester.pumpAndSettle();
      expect(find.text('Not installed'), findsOneWidget);

      await tester.tap(find.byKey(const Key('terminal-command-install')));
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('terminal-command-confirm-action')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Current'), findsOneWidget);
      expect(find.text('Terminal command installed.'), findsOneWidget);
      final copyClaude = find.byKey(const Key('managed-run-copy-claude'));
      await tester.ensureVisible(copyClaude);
      await tester.tap(copyClaude);
      await tester.pumpAndSettle();
      expect(copiedCommand, 'vibermate run -- claude');
      expect(find.text('Claude command copied'), findsOneWidget);

      final remove = find.byKey(const Key('terminal-command-remove'));
      await tester.ensureVisible(remove);
      await tester.tap(remove);
      await tester.pumpAndSettle();
      expect(find.text('Remove the Terminal command?'), findsOneWidget);
      await tester.tap(
        find.byKey(const Key('terminal-command-confirm-action')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Not installed'), findsOneWidget);
      expect(find.text('Owned Terminal command removed.'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese Terminal command review stays usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.settings_outlined).first);
    await tester.pumpAndSettle();
    final panel = find.byKey(const Key('terminal-command-panel'));
    await tester.ensureVisible(panel);
    expect(find.text('终端命令'), findsOneWidget);
    expect(find.text('尚未安装'), findsOneWidget);
    final install = find.byKey(const Key('terminal-command-install'));
    await tester.drag(find.byType(ListView).last, const Offset(0, -120));
    await tester.pumpAndSettle();
    await tester.tap(install);
    await tester.pumpAndSettle();
    expect(find.text('为终端安装 vibermate？'), findsOneWidget);
    expect(
      find.byKey(const Key('terminal-command-confirm-action')),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'wide workbench exposes dense timeline and endpoint-owned accounts',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      expect(find.text('Conversation'), findsOneWidget);
      expect(find.text('100 turns'), findsOneWidget);
      await tester.tap(find.byKey(const Key('conversation-load-earlier')));
      await tester.pumpAndSettle();
      expect(find.text('200 turns'), findsOneWidget);
      await tester.tap(find.byKey(const Key('conversation-load-earlier')));
      await tester.pumpAndSettle();
      expect(find.text('224 turns'), findsOneWidget);
      expect(find.byKey(const Key('conversation-load-earlier')), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.tap(find.byIcon(Icons.hub_outlined).first);
      await tester.pumpAndSettle();
      expect(find.text('Endpoints & accounts'), findsWidgets);
      expect(find.text('Anthropic · Work'), findsOneWidget);
      expect(find.text('Orbit · Team Pool'), findsNothing);

      await tester.tap(find.text('Orbit Relay · Tokyo').first);
      await tester.pumpAndSettle();
      expect(find.text('Orbit · Team Pool'), findsOneWidget);
      expect(find.text('Anthropic · Work'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Workspace default is edited independently from the current Capture Environment',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      final currentScope = find.byKey(const Key('capture-environment-scope'));
      final futureScope = find.byKey(const Key('workspace-default-scope'));
      expect(currentScope, findsOneWidget);
      expect(futureScope, findsOneWidget);
      expect(
        find.descendant(of: currentScope, matching: find.text('Work')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: futureScope, matching: find.text('Work')),
        findsOneWidget,
      );

      await tester.tap(
        find.descendant(
          of: futureScope,
          matching: find.byType(DropdownButtonFormField<String>),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Research').last);
      await tester.pumpAndSettle();

      expect(
        find.descendant(of: currentScope, matching: find.text('Work')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: futureScope, matching: find.text('Research')),
        findsOneWidget,
      );
      expect(
        find.textContaining('This Capture was not changed.'),
        findsOneWidget,
      );

      await tester.tap(
        find.descendant(
          of: futureScope,
          matching: find.byType(DropdownButtonFormField<String>),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('No workspace default').last);
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: futureScope,
          matching: find.text('No workspace default'),
        ),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    '390px Chinese keeps current and future Environment scopes distinct',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: true),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Claude Code').first);
      await tester.pumpAndSettle();

      expect(find.text('当前 Capture'), findsOneWidget);
      expect(find.text('未来运行'), findsOneWidget);
      expect(find.textContaining('之后在 vibermate 中启动'), findsOneWidget);
      expect(
        find.byKey(const Key('capture-environment-scope')),
        findsOneWidget,
      );
      expect(find.byKey(const Key('workspace-default-scope')), findsOneWidget);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'Manual Capture exposes independent Exchanges without inventing Turns',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(
        find.byKey(const Key('capture-row-manual_capture:manual-figma')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Independent exchanges'), findsOneWidget);
      expect(find.text('24 exchanges'), findsOneWidget);
      expect(find.text('24 turns'), findsNothing);
      expect(find.bySemanticsLabel('Turn map'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'global Conversations preserves boundaries and expands real Exchange evidence',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.forum_outlined).first);
      await tester.pumpAndSettle();
      expect(find.text('CONVERSATIONS'), findsOneWidget);
      expect(find.text('CaptureRun boundary'), findsOneWidget);
      expect(find.text('Figma Desktop'), findsWidgets);
      await tester.tap(
        find.byKey(const Key('conversation-row-capture_run:run-1')),
      );
      await tester.pumpAndSettle();
      expect(find.text('200 turns'), findsOneWidget);
      await tester.tap(find.byKey(const Key('conversation-load-earlier')));
      await tester.pumpAndSettle();
      expect(find.text('224 turns'), findsOneWidget);

      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await tester.ensureVisible(turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();
      expect(
        find.text('Continue with the next verified implementation step.'),
        findsOneWidget,
      );
      expect(
        find.text(
          'The runtime evidence is consistent; continue with the next bounded change.',
        ),
        findsOneWidget,
      );
      expect(find.text('Frozen routing and attempt evidence'), findsOneWidget);

      await tester.tap(
        find.byKey(const Key('exchange-full-run-1-exchange-222')),
      );
      await tester.pumpAndSettle();
      expect(find.text('System context'), findsOneWidget);
      expect(find.text('Inspect the current workspace.'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Turn evidence opens the exact frozen Environment as read-only authority',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.forum_outlined).first);
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const Key('conversation-row-capture_run:run-1')),
      );
      await tester.pumpAndSettle();
      final turn = find.byKey(
        const Key('conversation-turn-run-1-exchange-222'),
      );
      await tester.ensureVisible(turn);
      await tester.tap(turn);
      await tester.pumpAndSettle();

      final inspect = find.byKey(
        const Key('exchange-environment-run-1-exchange-222'),
      );
      await tester.ensureVisible(inspect);
      await tester.tap(inspect);
      await tester.pumpAndSettle();

      expect(
        find.byKey(const Key('environment-history-banner')),
        findsOneWidget,
      );
      expect(find.textContaining('work  ·  revision 7'), findsOneWidget);
      expect(find.text('Frozen evidence'), findsOneWidget);
      expect(find.byKey(const Key('environment-edit')), findsNothing);
      expect(
        find.byKey(const Key('environment-history-current')),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese Conversation timeline expands without overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.forum_outlined).first);
    await tester.pumpAndSettle();
    expect(find.text('对话'.toUpperCase()), findsOneWidget);
    await tester.tap(
      find.byKey(const Key('conversation-row-capture_run:run-1')),
    );
    await tester.pumpAndSettle();
    expect(find.text('CaptureRun 边界'), findsOneWidget);

    final turn = find.byKey(const Key('conversation-turn-run-1-exchange-224'));
    await tester.ensureVisible(turn);
    await tester.tap(turn);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 250));
    expect(find.text('正在等待最终响应…'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px manual capture revoke is confirmed and preserves evidence', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.text('RUNNING NOW'), findsOneWidget);
    await tester.tap(find.text('Figma Desktop').first);
    await tester.pumpAndSettle();
    expect(find.text('Revoke proxy login'), findsOneWidget);
    expect(find.text('Independent exchanges'), findsOneWidget);

    await tester.tap(find.text('Revoke proxy login'));
    await tester.pumpAndSettle();
    expect(find.text('Stop this proxy login?'), findsOneWidget);
    expect(
      find.text(
        'New connections using this login will stop. Conversation and Activity evidence remain available.',
      ),
      findsOneWidget,
    );

    await tester.tap(find.text('Revoke login'));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Proxy login revoked. Conversation and Activity evidence were retained.',
      ),
      findsOneWidget,
    );
    expect(find.text('Independent exchanges'), findsOneWidget);
    expect(find.text('Revoke proxy login'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'manual capture creation reviews Environment authority and delivers the password once',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('manual-capture-create')));
      await tester.pumpAndSettle();
      expect(find.text('Create a dedicated proxy login'), findsOneWidget);
      expect(
        find.text(
          'This Environment observes a transparent proxy flow and does not deliver a Root.',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('manual-capture-environment')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Work').last);
      await tester.pumpAndSettle();
      expect(
        find.text(
          'This Environment decrypts selected Agent endpoints. Install the delivered Root only in this client.',
        ),
        findsOneWidget,
      );
      expect(find.text('api.anthropic.com  ·  api.openai.com'), findsOneWidget);

      await tester.enterText(
        find.byKey(const Key('manual-capture-name')),
        'Browser Lab',
      );
      await tester.tap(find.byKey(const Key('manual-capture-create-confirm')));
      await tester.pumpAndSettle();

      expect(find.text('Proxy login created'), findsOneWidget);
      expect(find.text('preview-password-1'), findsOneWidget);
      expect(
        find.text('/Users/mira/Library/Application Support/ViberMate/root.pem'),
        findsOneWidget,
      );
      expect(
        find.text(
          'Copy these values now. The password is shown once and cannot be recovered later.',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
      await tester.pumpAndSettle();
      expect(find.text('Browser Lab'), findsWidgets);
      expect(
        find.text('Manual Capture created with a dedicated proxy login.'),
        findsOneWidget,
      );
      expect(find.text('preview-password-1'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'manual capture credential rotation requires confirmation and retains evidence',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Figma Desktop').first);
      await tester.pumpAndSettle();
      expect(find.text('Independent exchanges'), findsOneWidget);
      await tester.tap(find.byKey(const Key('manual-capture-rotate')));
      await tester.pumpAndSettle();
      expect(find.text('Rotate this proxy credential?'), findsOneWidget);
      expect(find.text('preview-password-3'), findsNothing);

      await tester.tap(find.byKey(const Key('manual-capture-rotate-confirm')));
      await tester.pumpAndSettle();
      expect(find.text('Proxy credential rotated'), findsOneWidget);
      expect(find.text('preview-password-3'), findsOneWidget);

      await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Proxy credential rotated. Conversation and Activity evidence were retained.',
        ),
        findsOneWidget,
      );
      expect(find.text('Independent exchanges'), findsOneWidget);
      expect(find.text('preview-password-3'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese manual capture creation has no overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('manual-capture-create')));
    await tester.pumpAndSettle();
    expect(find.text('创建专属代理登录'), findsOneWidget);
    expect(find.text('此 Environment 仅观察透明代理流量，不会交付 Root。'), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.enterText(
      find.byKey(const Key('manual-capture-name')),
      '窄屏客户端',
    );
    await tester.tap(find.byKey(const Key('manual-capture-create-confirm')));
    await tester.pumpAndSettle();
    expect(find.text('代理登录已创建'), findsOneWidget);
    expect(find.text('preview-password-1'), findsOneWidget);
    expect(find.text('Root 路径'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const Key('manual-capture-delivery-done')));
    await tester.pumpAndSettle();
    expect(find.text('窄屏客户端'), findsWidgets);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Simplified Chinese directory stays usable', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    expect(find.text('正在运行'.toUpperCase()), findsOneWidget);
    expect(find.text('历史记录'.toUpperCase()), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'network approval requires confirmation before the real decision',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      expect(find.text('1 pending'), findsOneWidget);
      await tester.tap(find.byKey(const Key('approval-attention')));
      await tester.pumpAndSettle();

      expect(find.text('Network access confirmation required'), findsOneWidget);
      await tester.tap(
        find.byKey(
          const Key('approval-approval-network-github-allow-once-request'),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('approval-confirmation')), findsOneWidget);
      expect(find.text('Network access confirmation required'), findsOneWidget);
      await tester.tap(find.byKey(const Key('approval-confirm-action')));
      await tester.pumpAndSettle();

      expect(
        find.text('Decision applied. Waiting work was allowed to continue.'),
        findsOneWidget,
      );
      expect(find.text('Nothing is waiting'), findsOneWidget);
      expect(find.text('No approvals'), findsOneWidget);
      expect(find.text('Network access confirmation required'), findsNothing);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('network evidence uses real cursor pagination', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openNetwork(tester);

    await tester.tap(find.byKey(const Key('network-tab-connections')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-connections')),
        matching: find.text('10'),
      ),
      findsOneWidget,
    );
    await tester.ensureVisible(find.byKey(const Key('connections-load-more')));
    await tester.tap(find.byKey(const Key('connections-load-more')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-connections')),
        matching: find.text('18'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('connections-load-more')), findsNothing);

    await tester.ensureVisible(find.byKey(const Key('network-tab-egress')));
    await tester.tap(find.byKey(const Key('network-tab-egress')));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const Key('egress-load-more')));
    await tester.tap(find.byKey(const Key('egress-load-more')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const Key('network-tab-egress')),
        matching: find.text('22'),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const Key('egress-load-more')), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('rule removal stays a draft until atomic save is confirmed', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await _openNetwork(tester);

    await tester.ensureVisible(find.byKey(const Key('network-tab-rules')));
    await tester.tap(find.byKey(const Key('network-tab-rules')));
    await tester.pumpAndSettle();
    expect(find.text('Rule set revision 3  ·  2'), findsOneWidget);
    expect(find.text('allow-anthropic'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rule-remove-allow-anthropic')));
    await tester.pumpAndSettle();
    expect(find.text('allow-anthropic'), findsNothing);
    expect(find.text('Unsaved draft'), findsOneWidget);
    expect(find.text('Rule set revision 3  ·  1'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rules-save')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('rules-save-confirmation')), findsOneWidget);
    expect(find.text('Rule set revision 3  ·  1'), findsOneWidget);

    await tester.tap(find.byKey(const Key('rules-save-confirm')));
    await tester.pumpAndSettle();
    expect(find.text('Connection rule set saved atomically.'), findsOneWidget);
    expect(find.text('Rule set revision 4  ·  1'), findsOneWidget);
    expect(find.text('Unsaved draft'), findsNothing);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    '390px Chinese network workbench and rule editor do not overflow',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: true),
      );
      await tester.pumpAndSettle();
      await _openNetwork(tester);

      expect(find.text('网络控制'), findsOneWidget);
      final rulesTab = find.byKey(const Key('network-tab-rules'));
      final tabStrip = find.ancestor(
        of: rulesTab,
        matching: find.byType(ListView),
      );
      await tester.drag(tabStrip, const Offset(-120, 0));
      await tester.pumpAndSettle();
      await tester.tap(rulesTab);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('rules-mode')), findsOneWidget);

      await tester.tap(find.byKey(const Key('rules-add')));
      await tester.pumpAndSettle();
      expect(find.text('添加规则'), findsWidgets);
      expect(find.byKey(const Key('rule-editor-id')), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.tap(find.text('取消').last);
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Environment edit reviews impact and publishes only Endpoint-owned Account authority',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('environment-edit')), findsNothing);
      await tester.tap(find.byKey(const Key('environment-row-work')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();

      final accountField = find.byKey(
        const Key('environment-route-account-anthropic-direct-anthropic-work'),
      );
      final accountDropdown = tester.widget<DropdownButton<String>>(
        find.descendant(
          of: accountField,
          matching: find.byType(DropdownButton<String>),
        ),
      );
      final accountIds = accountDropdown.items!
          .map((item) => item.value)
          .toList(growable: false);
      expect(accountIds, containsAll(['anthropic-work', 'anthropic-lab']));
      expect(accountIds, isNot(contains('orbit-team')));
      expect(accountIds, isNot(contains('openai-work')));

      await tester.enterText(
        find.byKey(const Key('environment-editor-name')),
        'Work reviewed',
      );
      await tester.tap(find.byKey(const Key('environment-editor-tool-mode')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Ask before tools run').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-editor-recording')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Metadata only').last);
      await tester.pumpAndSettle();
      await tester.ensureVisible(accountField);
      await tester.pumpAndSettle();
      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Anthropic · Lab').last);
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const Key('environment-impact-review')),
        findsOneWidget,
      );
      expect(find.text('Hot switch'), findsWidgets);
      expect(find.text('6 RUNNING CAPTURES'), findsOneWidget);
      expect(find.text('r7'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.text('Work reviewed'), findsWidgets);
      expect(find.textContaining('revision 8'), findsOneWidget);
      expect(
        find.text(
          'Environment published from the reviewed draft and impact boundary.',
        ),
        findsOneWidget,
      );
      expect(find.text('Anthropic · Lab'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('390px Chinese Environment impact stays usable', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-row-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-edit')));
    await tester.pumpAndSettle();
    expect(find.text('每条 Route 只能选择其 Endpoint 所属的账号。'), findsOneWidget);

    await tester.enterText(
      find.byKey(const Key('environment-editor-name')),
      '工作环境',
    );
    await tester.tap(find.byKey(const Key('environment-review')));
    await tester.pumpAndSettle();
    expect(find.text('发布前的运行影响'), findsOneWidget);
    expect(find.text('6 个运行中 CAPTURE'), findsOneWidget);
    expect(find.byKey(const Key('environment-publish')), findsOneWidget);
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const Key('environment-impact-back')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消').last);
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('new observation-only Environment is published at revision one', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.tune).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-create')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-create-name')),
      'Local Observe',
    );
    expect(find.text('local-observe'), findsOneWidget);

    await tester.tap(find.byKey(const Key('environment-create-review')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('environment-create-impact')), findsOneWidget);
    expect(find.text('0 RUNNING CAPTURES'), findsOneWidget);
    await tester.tap(find.byKey(const Key('environment-create-publish')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('environment-row-local-observe')),
      findsOneWidget,
    );
    expect(find.textContaining('revision 1'), findsOneWidget);
    expect(
      find.text(
        'This Environment changes policy and evidence settings while leaving semantic routing transparent.',
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets(
    'Environment adds multiple upstream Endpoints without changing the current default',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.tune).first);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-row-research')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();

      final catalog = find.byKey(const Key('environment-endpoint-catalog'));
      await tester.ensureVisible(catalog);
      await tester.pumpAndSettle();
      await tester.tap(catalog);
      await tester.pumpAndSettle();
      await tester.tap(find.textContaining('Anthropic API').last);
      await tester.pumpAndSettle();

      final accountField = find.byKey(
        const Key('environment-endpoint-account-target.anthropic.official'),
      );
      final accountDropdown = tester.widget<DropdownButton<String>>(
        find.descendant(
          of: accountField,
          matching: find.byType(DropdownButton<String>),
        ),
      );
      final accountIds = accountDropdown.items!
          .map((item) => item.value)
          .toList(growable: false);
      expect(accountIds, containsAll(['anthropic-work', 'anthropic-lab']));
      expect(accountIds, isNot(contains('openai-work')));
      expect(accountIds, isNot(contains('orbit-team')));

      await tester.tap(accountField);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Anthropic · Work').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-add-endpoint')));
      await tester.pumpAndSettle();
      expect(find.text('Anthropic · Work'), findsWidgets);

      await tester.tap(find.byKey(const Key('environment-review')));
      await tester.pumpAndSettle();
      expect(find.text('Hot switch'), findsWidgets);
      await tester.tap(find.byKey(const Key('environment-publish')));
      await tester.pumpAndSettle();
      expect(find.textContaining('revision 4'), findsOneWidget);
      expect(find.text('2 upstream routes'), findsOneWidget);
      expect(find.text('Default'), findsOneWidget);
      expect(find.text('Candidate'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets(
    'Endpoint-owned Account can be created, rotated, and safely deleted',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1180, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(
        const ViberMateApp(previewMode: true, preferChinese: false),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byIcon(Icons.hub_outlined).first);
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('endpoints-add')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('endpoint-editor-name')),
        'Team Relay',
      );
      await tester.enterText(
        find.byKey(const Key('endpoint-editor-origin')),
        'https://relay.team.example',
      );
      await tester.tap(find.byKey(const Key('endpoint-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Upstream Endpoint created. Accounts can now be added under its authority.',
        ),
        findsOneWidget,
      );
      expect(find.text('Team Relay'), findsWidgets);

      await tester.tap(find.byKey(const Key('accounts-add')));
      await tester.pumpAndSettle();
      expect(find.text('Team Relay'), findsWidgets);
      await tester.enterText(
        find.byKey(const Key('account-editor-name')),
        'Team Primary',
      );
      await tester.enterText(
        find.byKey(const Key('account-editor-secret')),
        'sk-ant-preview-one',
      );
      await tester.tap(find.byKey(const Key('account-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text('Account created under the selected upstream Endpoint.'),
        findsOneWidget,
      );
      expect(find.text('Team Primary'), findsOneWidget);

      await tester.tap(find.byIcon(Icons.key_outlined));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('account-editor-secret')),
        'sk-ant-preview-two',
      );
      await tester.tap(find.byKey(const Key('account-editor-save')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Credential replaced with its previous epoch as the CAS boundary.',
        ),
        findsOneWidget,
      );
      expect(find.textContaining('epoch 2'), findsOneWidget);

      await tester.tap(find.byIcon(Icons.delete_outline));
      await tester.pumpAndSettle();
      expect(find.text('Delete Team Primary?'), findsOneWidget);
      await tester.tap(find.byKey(const Key('account-delete-confirm')));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Account and credential deleted. Captured evidence was not removed.',
        ),
        findsOneWidget,
      );
      expect(find.text('Team Primary'), findsNothing);
      expect(find.text('No accounts on this Endpoint'), findsOneWidget);
      expect(tester.takeException(), isNull);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    },
  );

  testWidgets('referenced Account deletion shows exact blocking routes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byIcon(Icons.hub_outlined).first);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('account-delete-anthropic-work')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('account-delete-confirm')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'The runtime refused deletion because Environment routes still reference this Account.',
      ),
      findsOneWidget,
    );
    expect(find.text('Work r7 · route anthropic-direct'), findsOneWidget);
    expect(find.text('Anthropic · Work'), findsWidgets);
    expect(tester.takeException(), isNull);

    await tester.tap(find.text('Confirm').last);
    await tester.pumpAndSettle();
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

Future<void> _openNetwork(WidgetTester tester) async {
  await tester.tap(find.byIcon(Icons.security_outlined));
  await tester.pumpAndSettle();
  expect(find.byKey(const Key('network-tab-approvals')), findsOneWidget);
}

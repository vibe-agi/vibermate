import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/design/agent_client_profile.dart';
import 'package:vibermate_app/core/design/agent_identity.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';

void main() {
  test('Agent client profiles keep native resume and evidence semantics', () {
    expect(
      AgentClientProfile.claude.resumeCommand('session-1'),
      "claude --resume 'session-1'",
    );
    expect(
      AgentClientProfile.codex.resumeCommand('thread-1'),
      "codex resume 'thread-1'",
    );
    expect(
      AgentClientProfile.claude.field('claude.agent_id').family,
      AgentClientEvidenceFamily.agent,
    );
    expect(
      AgentClientProfile.codex.field('codex.reasoning_item_id').family,
      AgentClientEvidenceFamily.request,
    );
    expect(
      AgentClientProfile.claude.field('claude.spawned_agent_id').family,
      AgentClientEvidenceFamily.agent,
    );
    expect(
      AgentClientProfile.claude
          .field('claude.source_tool_assistant_uuid')
          .family,
      AgentClientEvidenceFamily.request,
    );
    expect(
      AgentClientProfile.codex.field('codex.compaction_window_id').family,
      AgentClientEvidenceFamily.session,
    );
    expect(
      AgentClientProfile.codex.field('codex.cli_version').family,
      AgentClientEvidenceFamily.client,
    );
  });

  test('Agent identity resolver accepts exact aliases only', () {
    expect(
      AgentIdentity.resolve(const ['Claude Code']),
      same(AgentIdentity.claudeCode),
    );
    expect(
      AgentIdentity.resolve(const ['/opt/vibermate/bin/codex.js']),
      same(AgentIdentity.codex),
    );
    expect(
      AgentIdentity.resolve(const ['Figma Desktop']),
      same(AgentIdentity.figma),
    );
    expect(AgentIdentity.resolve(const ['Claude-compatible relay']), isNull);
    expect(AgentIdentity.resolve(const ['my-codex-wrapper']), isNull);
  });

  testWidgets('Agent marks render bundled brands and a neutral fallback', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: const Scaffold(
          body: Row(
            children: [
              AgentIdentityMark(
                candidates: ['claude'],
                fallbackLabel: 'Claude',
                fallbackIcon: Icons.terminal,
              ),
              AgentIdentityMark(
                candidates: ['codex-cli'],
                fallbackLabel: 'Codex',
                fallbackIcon: Icons.terminal,
              ),
              AgentIdentityMark(
                candidates: ['figma'],
                fallbackLabel: 'Figma',
                fallbackIcon: Icons.link,
              ),
              AgentIdentityMark(
                candidates: ['Custom Agent'],
                fallbackLabel: 'Custom Agent',
                fallbackIcon: Icons.terminal,
              ),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('agent-logo-claude-code')), findsOneWidget);
    expect(find.byKey(const Key('agent-logo-codex')), findsOneWidget);
    expect(find.byKey(const Key('agent-logo-figma')), findsOneWidget);
    expect(find.byKey(const Key('agent-logo-fallback')), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('Capture and Conversation directories use client identities', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('agent-logo-claude-code')), findsWidgets);
    expect(find.byKey(const Key('agent-logo-codex')), findsWidgets);
    expect(find.byKey(const Key('agent-logo-figma')), findsWidgets);

    await tester.tap(find.bySemanticsLabel(RegExp(r'^Conversations\s+⌘2$')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('conversation-filter')),
      'Claude Code',
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('agent-logo-claude-code')), findsWidgets);
    await tester.enterText(
      find.byKey(const Key('conversation-filter')),
      'Codex',
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('agent-logo-codex')), findsWidgets);
    await tester.enterText(
      find.byKey(const Key('conversation-filter')),
      'Figma Desktop',
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('agent-logo-figma')), findsWidgets);
    expect(tester.takeException(), isNull);
  });
}

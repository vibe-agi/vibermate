@TestOn('vm')
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  final daemon = Platform.environment['VIBERMATE_LIVE_TEST_DAEMON'];
  final command = Platform.environment['VIBERMATE_LIVE_TEST_COMMAND'];
  for (final content in [false, true]) {
    test(
      'packaged ACP command reaches the real Flutter controller (content=$content)',
      () async {
        final home = await Directory.systemTemp.createTemp(
          'vibermate-acp-flutter.',
        );
        DesktopRuntime? runtime;
        Process? process;
        WorkbenchController? controller;
        try {
          runtime = await DesktopRuntime.start(
            daemonPath: daemon,
            homeDirectory: home.path,
            remoteServerListenAddress: '127.0.0.1:0',
          );
          // A controlled real stdio peer; no network, settings files or provider
          // credentials. The shell is explicit test argv, never wrapper inference.
          const agent = r'''
test "$ACP_FIXTURE_SETTING" = editor-owned || exit 70
test -z "$HTTP_PROXY" || exit 71
read -r request || exit 72
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"live-fixture","version":"1"}}}'
read -r request || exit 73
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"editor-session"}}'
read -r request || exit 74
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"editor-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"visible answer"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'
cat >/dev/null
''';
          process = await Process.start(
            command!,
            [
              'acp',
              if (content) '--record-content',
              '--',
              '/bin/sh',
              '-c',
              agent,
            ],
            environment: {
              'HOME': home.path,
              'PATH': '/usr/bin:/bin',
              'ACP_FIXTURE_SETTING': 'editor-owned',
            },
            includeParentEnvironment: false,
            workingDirectory: home.path,
            runInShell: false,
          );
          final diagnostics = process.stderr.transform(utf8.decoder).join();
          final lines = StreamIterator(
            process.stdout
                .transform(utf8.decoder)
                .transform(const LineSplitter()),
          );
          Future<Map<String, dynamic>> exchange(
            int id,
            String method,
            Map<String, Object?> params,
          ) async {
            process!.stdin.writeln(
              jsonEncode({
                'jsonrpc': '2.0',
                'id': id,
                'method': method,
                'params': params,
              }),
            );
            await process.stdin.flush();
            expect(
              await lines.moveNext().timeout(const Duration(seconds: 10)),
              isTrue,
            );
            return jsonDecode(lines.current) as Map<String, dynamic>;
          }

          final init = await exchange(1, 'initialize', {'protocolVersion': 1});
          expect(init['result']['protocolVersion'], 1);
          await exchange(2, 'session/new', {
            'cwd': '${home.path}/native-workspace',
            'mcpServers': [],
          });
          final update = await exchange(3, 'session/prompt', {
            'sessionId': 'editor-session',
            'prompt': [
              {'type': 'text', 'text': 'visible question'},
            ],
          });
          expect(update['method'], 'session/update');
          expect(
            await lines.moveNext().timeout(const Duration(seconds: 10)),
            isTrue,
          );
          expect(jsonDecode(lines.current)['result']['stopReason'], 'end_turn');
          await process.stdin.close();
          expect(
            await lines.moveNext().timeout(const Duration(seconds: 10)),
            isFalse,
          );
          expect(
            await process.exitCode.timeout(const Duration(seconds: 10)),
            0,
            reason: await diagnostics,
          );
          process = null;
          final captures = (await runtime.api.loadDashboard()).captures;
          expect(captures, hasLength(1));
          expect(captures.single.isACP, isTrue);
          expect(captures.single.running, isFalse);
          final record = await (runtime.api as ACPObservationApi)
              .acpObservation(captures.single.key);
          expect(record!.finished, isTrue);
          expect(record.sessions.single.id, 'editor-session');
          expect(record.prompts.single.state, 'completed');
          expect(
            record.prompts.single.userText,
            content ? 'visible question' : '',
          );
          expect(
            record.prompts.single.agentText,
            content ? 'visible answer' : '',
          );
          controller = WorkbenchController(
            api: runtime.api,
            terminalCommands: PackagedTerminalCommandService(
              commandPath: command,
            ),
            previewMode: false,
            terminalManagement: false,
            closeRuntime: () async {},
          );
          await controller.initialize();
          await controller.selectCapture(captures.single.key);
          expect(controller.errorMessage, isNull);
          expect(controller.selectedACP!.sessions.single.id, 'editor-session');
          expect(controller.captureConversations, isEmpty);
        } finally {
          if (process != null) {
            process.kill(ProcessSignal.sigkill);
            await process.exitCode;
          }
          controller?.dispose();
          await runtime?.close();
          await home.delete(recursive: true);
        }
      },
      skip: daemon == null || command == null
          ? 'Set both packaged daemon and CLI live-test paths.'
          : false,
      timeout: const Timeout(Duration(seconds: 60)),
    );
  }
}

import 'dart:convert';
import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/native_tool_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/network_view.dart';
import 'package:vibermate_app/features/workbench/native_tool_approval_row.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

const nativeCapture = 'manual_capture:synthetic';
const nativeApprovalID = '00000000000000000000000000000001';
Map<String, Object?> nativeApprovalFixture() => {
  'id': nativeApprovalID,
  'operationId': '00000000000000000000000000000002',
  'captureKey': nativeCapture,
  'environment': {'id': 'native.synthetic', 'revision': 3, 'digest': 'a' * 64},
  'revision': 1,
  'state': 'pending',
  'expiresAt': '2099-01-01T00:00:00Z',
  'bindingDigest': 'b' * 64,
  'credentialOwner': 'original_client',
  'previewState': 'available',
  'preview': {
    'intents': [
      {
        'toolName': 'read_file',
        'action': 'tool_call',
        'target': 'https://native.example',
        'arguments': {'path': 'frozen.txt'},
        'truncated': false,
      },
    ],
    'truncated': false,
  },
};
void main() {
  test(
    'native preview and decision remain usable while optional summary HTTP waits',
    () async {
      final entered = Completer<void>();
      final history = Completer<http.Response>();
      var decisions = 0;
      final api = await nativeApi((request) async {
        if (request.url.path.endsWith('/activities/summary')) {
          entered.complete();
          return history.future;
        }
        if (request.method == 'POST') {
          decisions++;
          return nativeResponse(nativeResolved('allowed'));
        }
        if (request.url.path.endsWith('/approvals')) {
          return nativeResponse({
            'items': [nativeApprovalFixture()],
            'nextCursor': '',
          });
        }
        return nativeResponse(nativeApprovalFixture());
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      final pendingHistory = controller.loadActivitySummary(
        const ActivitySummaryScope(manualCaptureId: 'synthetic'),
      );
      final rejectedHistory = expectLater(
        pendingHistory,
        throwsA(isA<ControlProblem>()),
      );
      await entered.future;
      controller.setNativeApprovalsVisible(true);
      while (controller.nativeApprovalsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
      final approval = controller.nativeApprovals.single;
      expect(approval.canAllow, isTrue);
      await controller.decideNativeApproval(approval, NativeDecision.allowOnce);
      expect(decisions, 1);
      expect(controller.nativeApprovalError, isNull);
      history.complete(
        http.Response(
          jsonEncode({
            'reasonCode': 'inspection_unavailable',
            'messageKey': 'error.inspection_unavailable',
          }),
          503,
          headers: {'content-type': 'application/json'},
        ),
      );
      await rejectedHistory;
    },
  );

  test('pending traversal restarts to discover later earlier tuples', () async {
    var pages = 0;
    final cursors = <String?>[];
    final later = {
      ...nativeApprovalFixture(),
      'id': '00000000000000000000000000000000',
    };
    final api = await nativeApi((request) async {
      if (!request.url.path.endsWith('/approvals')) {
        return nativeResponse(nativeApprovalFixture());
      }
      cursors.add(request.url.queryParameters['cursor']);
      pages++;
      return nativeResponse({
        'items': pages == 1
            ? [nativeApprovalFixture()]
            : pages == 3
            ? [later]
            : [],
        'nextCursor': pages == 1 ? 'cursorA' : '',
      });
    });
    addTearDown(api.close);
    final controller = nativeController(api);
    addTearDown(controller.dispose);
    await controller.refreshNativeApprovals();
    await controller.refreshNativeApprovals();
    expect(controller.nativePendingCount, 1);
    await controller.refreshNativeApprovals();
    expect(controller.nativePendingCount, 2);
    expect(cursors, [null, 'cursorA', null]);
  });

  for (final choice in NativeDecision.values) {
    for (final committed in [false, true]) {
      for (final reopen in [false, true]) {
        testWidgets(
          'failed native ${choice.wire} recovers by GET committed=$committed reopen=$reopen',
          (tester) async {
            var posted = false;
            var posts = 0;
            var detailReadsAfterFailure = 0;
            final pageCursors = <String?>[];
            final freshRead = Completer<http.Response>();
            final api = await nativeApi((request) async {
              if (request.method == 'POST') {
                posts++;
                posted = true;
                expect(jsonDecode(request.body), {
                  'decision': choice.wire,
                  'bindingDigest': 'b' * 64,
                });
                throw http.ClientException('synthetic response lost');
              }
              if (request.url.path.endsWith('/approvals')) {
                final cursor = request.url.queryParameters['cursor'];
                pageCursors.add(cursor);
                return nativeResponse({
                  'items': cursor == null && !(posted && committed)
                      ? [nativeApprovalFixture()]
                      : [],
                  'nextCursor': cursor == null && !posted ? 'cursorA' : '',
                });
              }
              if (posted) {
                detailReadsAfterFailure++;
                return freshRead.future;
              }
              return nativeResponse(nativeApprovalFixture());
            });
            addTearDown(api.close);
            final controller = nativeController(api)
              ..pendingApprovals = const [];
            addTearDown(controller.dispose);
            await tester.runAsync(() async {
              controller.setNativeApprovalsVisible(true);
              while (controller.nativeApprovalsLoading) {
                await Future<void>.delayed(Duration.zero);
              }
              await controller.refreshNativeApprovals();
            });
            expect(controller.nativeApprovalScanComplete, isTrue);
            expect(controller.nativePendingCount, 1);
            expect(pageCursors, [null, 'cursorA']);
            final old = controller.nativeApprovals.single;
            await tester.pumpWidget(
              MaterialApp(
                theme: ViberTheme.light(),
                home: Scaffold(
                  body: AnimatedBuilder(
                    animation: controller,
                    builder: (context, _) => ListView(
                      children: [
                        for (final approval in controller.nativeApprovals)
                          NativeToolApprovalRow(
                            approval: approval,
                            profileName: 'Synthetic profile',
                            copy: AppCopy.forLanguage(AppLanguage.english),
                            onDecision: (decision) => unawaited(
                              controller.decideNativeApproval(
                                approval,
                                decision,
                              ),
                            ),
                          ),
                      ],
                    ),
                  ),
                ),
              ),
            );
            await tester.tap(
              find.byKey(
                Key('native-approval-$nativeApprovalID-${choice.wire}'),
              ),
            );
            await tester.runAsync(() async {
              while (controller.nativeApprovalError == null) {
                await Future<void>.delayed(Duration.zero);
              }
            });
            await tester.pump();
            expect(controller.nativeApprovals, isEmpty);
            expect(find.textContaining('frozen.txt'), findsNothing);
            expect(
              controller.nativeApprovalAttention,
              isTrue,
              reason:
                  'An unresolved POST must retain the ID for authoritative revalidation',
            );
            expect(controller.nativePendingCount, 1);
            await controller.decideNativeApproval(old, choice);
            expect(
              posts,
              1,
              reason: 'Dropped preview cannot resubmit the mutation',
            );
            await tester.runAsync(() async {
              if (reopen) {
                controller.setNativeApprovalsVisible(false);
                controller.setNativeApprovalsVisible(true);
              } else {
                unawaited(controller.refreshNativeApprovals());
              }
              while (detailReadsAfterFailure == 0 &&
                  controller.nativeApprovalsLoading) {
                await Future<void>.delayed(Duration.zero);
              }
            });
            expect(detailReadsAfterFailure, 1);
            expect(
              controller.nativeApprovals,
              isEmpty,
              reason:
                  'Reopen must await a fresh GET, never restore old preview bytes',
            );
            await tester.runAsync(() async {
              freshRead.complete(
                nativeResponse(
                  committed
                      ? nativeResolved(
                          choice == NativeDecision.allowOnce
                              ? 'allowed'
                              : 'denied',
                        )
                      : nativeApprovalFixture(),
                ),
              );
              while (controller.nativeApprovalsLoading) {
                await Future<void>.delayed(Duration.zero);
              }
            });
            await tester.pump();
            expect(posts, 1, reason: 'Recovery must never retry POST');
            expect(pageCursors, [null, 'cursorA', null]);
            expect(controller.nativeApprovalScanComplete, isTrue);
            expect(controller.nativePendingCount, committed ? 0 : 1);
            expect(controller.nativeApprovalAttention, !committed);
            expect(
              find.byKey(
                const Key('native-approval-$nativeApprovalID-allow-once'),
              ),
              committed ? findsNothing : findsOneWidget,
            );
            if (!committed) {
              expect(
                identical(controller.nativeApprovals.single, old),
                isFalse,
              );
            }
            await tester.pumpWidget(const SizedBox());
          },
        );
      }
    }
  }
  test(
    'hidden pending attention counts selected Capture without retaining preview',
    () async {
      var pages = 0;
      final api = await nativeApi(
        (r) async => nativeResponse(
          r.url.path.endsWith('/approvals')
              ? {
                  'items': ++pages == 1 ? [nativeApprovalFixture()] : [],
                  'nextCursor': '',
                }
              : nativeApprovalFixture(),
        ),
      );
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.pendingApprovals = const [];
      await controller.refreshNativeApprovals();
      await controller.refreshNativeApprovals();
      expect(controller.pendingApprovalCount, 1);
      expect(controller.nativeApprovals, isEmpty);
    },
  );
  testWidgets(
    'Network shows native OFF-recording frozen approval from real HTTP parser',
    (tester) async {
      final fixture = PreviewControlApi();
      final api = await HttpControlApi.connect(
        DesktopSession(
          baseUrl: Uri.parse('http://127.0.0.1:43129'),
          readToken: 'R' * 43,
          writeToken: 'W' * 43,
          instanceId: 'synthetic',
          expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
        ),
        inspectSession: false,
        client: MockClient(
          (r) async => http.Response(
            jsonEncode(
              r.url.path.endsWith('/approvals')
                  ? {
                      'items': [nativeApprovalFixture()],
                      'nextCursor': '',
                    }
                  : nativeApprovalFixture(),
            ),
            200,
            headers: {
              'content-type': 'application/json',
              'cache-control': 'no-store',
            },
          ),
        ),
      );
      addTearDown(api.close);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        closeRuntime: fixture.close,
      );
      addTearDown(controller.dispose);
      controller.selectedCaptureKey = nativeCapture;
      controller.networkData = await fixture.loadNetwork();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: AnimatedBuilder(
              animation: controller,
              builder: (context, _) => NetworkView(
                controller: controller,
                copy: AppCopy.forLanguage(AppLanguage.english),
              ),
            ),
          ),
        ),
      );
      await tester.runAsync(() async {
        while (controller.nativeApprovalsLoading) {
          await Future<void>.delayed(const Duration(milliseconds: 1));
        }
      });
      await tester.pumpAndSettle();
      expect(
        controller.nativeApprovals,
        hasLength(1),
        reason: controller.nativeApprovalError,
      );
      await tester.scrollUntilVisible(
        find.byKey(const Key('native-approval-$nativeApprovalID-allow-once')),
        300,
        scrollable: find.byType(Scrollable).last,
      );
      expect(
        find.byKey(const Key('native-approval-$nativeApprovalID-allow-once')),
        findsOneWidget,
      );
      expect(find.textContaining('frozen.txt'), findsOneWidget);
      expect(find.text("Client's existing login"), findsOneWidget);
    },
  );
  test(
    'native HTTP enforces exact ownership and sends one frozen decision',
    () async {
      final requests = <http.Request>[];
      final api = await nativeApi((request) async {
        requests.add(request);
        return nativeResponse(
          request.method == 'GET'
              ? nativeApprovalFixture()
              : nativeResolved('allowed'),
        );
      });
      addTearDown(api.close);
      final approval = await api.nativeApproval(
        nativeCapture,
        nativeApprovalID,
      );
      final result = await api.decideNativeApproval(
        nativeCapture,
        approval,
        NativeDecision.allowOnce,
      );
      expect(result.state, 'allowed');
      expect(requests.last.headers['if-match'], '1');
      expect(requests.last.headers['authorization'], 'Bearer ${'W' * 43}');
      expect(
        requests.last.headers['Idempotency-Key'] ??
            requests.last.headers['idempotency-key'],
        isNotEmpty,
      );
      expect(jsonDecode(requests.last.body), {
        'decision': 'allow-once',
        'bindingDigest': 'b' * 64,
      });
      await expectLater(
        api.decideNativeApproval(
          'manual_capture:other',
          approval,
          NativeDecision.allowOnce,
        ),
        throwsA(isA<ControlContractException>()),
      );
      expect(requests.length, 2);
    },
  );
  test(
    'native parser rejects alternate authority and sensitive reply arguments',
    () {
      for (final bad in [
        {...nativeApprovalFixture(), 'captureKey': 'manual_capture:other'},
        {...nativeApprovalFixture(), 'credentialOwner': 'managed_account'},
        {...nativeApprovalFixture(), 'id': 'legacy-model-id'},
        {...nativeApprovalFixture(), 'revision': 0},
        {
          ...nativeApprovalFixture(),
          'preview': {
            'intents': [
              {
                'toolName': '',
                'action': 'form_reply',
                'target': 'https://native.example',
                'arguments': {'proof': 'secret'},
                'truncated': false,
              },
            ],
            'truncated': false,
          },
        },
      ]) {
        expect(
          () => NativeApproval.fromJson(bad, nativeCapture),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );
  test('registered ping summary rejects payload and presentation changes', () {
    for (final extra in [
      {'arguments': <String, dynamic>{}},
      {'toolName': 'invented'},
      {'truncated': true},
      {'action': 'unknown_reply'},
    ]) {
      final fixture = nativeApprovalFixture();
      fixture['preview'] = {
        'intents': [
          {
            'toolName': '',
            'action': 'ping_reply',
            'target': 'https://native.example',
            'truncated': false,
            ...extra,
          },
        ],
        'truncated': false,
      };
      expect(
        () => NativeApproval.fromJson(fixture, nativeCapture),
        throwsA(isA<ControlContractException>()),
      );
    }
  });
  test(
    'native refresh removes resolved IDs even when pending page is empty',
    () async {
      var pageCalls = 0;
      var resolved = false;
      final api = await nativeApi(
        (r) async => nativeResponse(
          r.url.path.endsWith('/approvals')
              ? {
                  'items': ++pageCalls == 1 ? [nativeApprovalFixture()] : [],
                  'nextCursor': pageCalls == 1 ? 'cursorA' : '',
                }
              : resolved
              ? nativeResolved('denied')
              : nativeApprovalFixture(),
        ),
      );
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeApprovalsVisible(true);
      while (controller.nativeApprovalsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
      expect(controller.nativeApprovals, hasLength(1));
      expect(controller.nativeApprovalScanComplete, isFalse);
      resolved = true;
      await controller.refreshNativeApprovals();
      expect(controller.nativeApprovals, isEmpty);
      expect(controller.nativeApprovalScanComplete, isTrue);
    },
  );
  test(
    'close discards pending bytes and stale page cannot revive approval',
    () async {
      final held = Completer<http.Response>();
      var requests = 0;
      final api = await nativeApi((r) {
        requests++;
        return held.future;
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeApprovalsVisible(true);
      controller.setNativeApprovalsVisible(false);
      held.complete(
        nativeResponse({
          'items': [nativeApprovalFixture()],
          'nextCursor': '',
        }),
      );
      await Future<void>.delayed(Duration.zero);
      expect(controller.nativeApprovals, isEmpty);
      await controller.decideNativeApproval(
        NativeApproval.fromJson(nativeApprovalFixture(), nativeCapture),
        NativeDecision.allowOnce,
      );
      expect(requests, 1);
      expect(controller.nativeApprovals, isEmpty);
    },
  );
  test(
    'page started during decision cannot restore decided intent after POST returns',
    () async {
      final post = Completer<http.Response>();
      final page = Completer<http.Response>();
      var pages = 0;
      final api = await nativeApi((r) async {
        if (r.method == 'POST') return post.future;
        if (r.url.path.endsWith('/approvals')) {
          return ++pages == 1
              ? nativeResponse({
                  'items': [nativeApprovalFixture()],
                  'nextCursor': '',
                })
              : page.future;
        }
        return nativeResponse(nativeApprovalFixture());
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeApprovalsVisible(true);
      while (controller.nativeApprovalsLoading) {
        await Future<void>.delayed(Duration.zero);
      }
      final decision = controller.decideNativeApproval(
        controller.nativeApprovals.single,
        NativeDecision.allowOnce,
      );
      final late = controller.refreshNativeApprovals();
      post.complete(nativeResponse(nativeResolved('allowed')));
      await decision;
      page.complete(
        nativeResponse({
          'items': [nativeApprovalFixture()],
          'nextCursor': '',
        }),
      );
      await late;
      expect(controller.nativeApprovals, isEmpty);
    },
  );
  test(
    'Capture switch discards old pending response and starts a fresh traversal',
    () async {
      final held = Completer<http.Response>();
      final requests = <http.Request>[];
      final api = await nativeApi((r) async {
        requests.add(r);
        if (requests.length == 1) return held.future;
        return nativeResponse({'items': [], 'nextCursor': ''});
      });
      addTearDown(api.close);
      final controller = nativeController(api);
      addTearDown(controller.dispose);
      controller.setNativeApprovalsVisible(true);
      while (requests.isEmpty) {
        await Future<void>.delayed(Duration.zero);
      }
      await controller.selectCapture('manual_capture:other');
      held.complete(
        nativeResponse({
          'items': [nativeApprovalFixture()],
          'nextCursor': '',
        }),
      );
      await Future<void>.delayed(Duration.zero);
      await controller.refreshNativeApprovals();
      expect(controller.nativeApprovals, isEmpty);
      expect(controller.pendingApprovalCount, isNull);
      expect(
        Uri.decodeComponent(requests.last.url.path),
        contains('manual_capture:other'),
      );
      expect(requests.last.url.queryParameters['cursor'], isNull);
    },
  );
  testWidgets('missing details disables Allow but keeps Deny available', (
    tester,
  ) async {
    final value = {...nativeApprovalFixture(), 'previewState': 'unavailable'}
      ..remove('preview');
    NativeDecision? chosen;
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: NativeToolApprovalRow(
            approval: NativeApproval.fromJson(value, nativeCapture),
            profileName: 'Synthetic profile',
            copy: copy,
            onDecision: (decision) => chosen = decision,
          ),
        ),
      ),
    );
    expect(
      tester
          .widget<FilledButton>(
            find.byKey(
              const Key('native-approval-$nativeApprovalID-allow-once'),
            ),
          )
          .onPressed,
      isNull,
    );
    expect(
      find.text('Details are unavailable. Request a new approval.'),
      findsOneWidget,
    );
    await tester.tap(
      find.byKey(const Key('native-approval-$nativeApprovalID-deny')),
    );
    expect(chosen, NativeDecision.deny);
  });
  for (final language in AppLanguage.values) {
    testWidgets('native row EN/zh actions at390/200 ${language.name}', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(390, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final requests = <http.Request>[];
      final api = await nativeApi((r) async {
        requests.add(r);
        return nativeResponse(
          r.method == 'GET'
              ? nativeApprovalFixture()
              : nativeResolved('allowed'),
        );
      });
      addTearDown(api.close);
      final approval = (await tester.runAsync(
        () => api.nativeApproval(nativeCapture, nativeApprovalID),
      ))!;
      final copy = AppCopy.forLanguage(language);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: MediaQuery(
            data: const MediaQueryData(textScaler: TextScaler.linear(2)),
            child: Scaffold(
              body: SingleChildScrollView(
                child: NativeToolApprovalRow(
                  approval: approval,
                  profileName: 'Synthetic profile',
                  copy: copy,
                  onDecision: (d) => unawaited(
                    api.decideNativeApproval(nativeCapture, approval, d),
                  ),
                ),
              ),
            ),
          ),
        ),
      );
      expect(find.text(copy('native.login')), findsOneWidget);
      await tester.ensureVisible(
        find.byKey(const Key('native-approval-$nativeApprovalID-allow-once')),
      );
      await tester.tap(
        find.byKey(const Key('native-approval-$nativeApprovalID-allow-once')),
      );
      await tester.runAsync(() async {
        while (requests.length < 2) {
          await Future<void>.delayed(const Duration(milliseconds: 1));
        }
      });
      await tester.pumpAndSettle();
      expect(jsonDecode(requests.last.body), {
        'decision': 'allow-once',
        'bindingDigest': 'b' * 64,
      });
      expect(tester.takeException(), isNull);
    });
  }
}

Map<String, Object?> nativeResolved(String state) => {
  ...nativeApprovalFixture(),
  'state': state,
  'revision': 2,
  'previewState': 'unavailable',
}..remove('preview');
http.Response nativeResponse(Object? body) => http.Response(
  jsonEncode(body),
  200,
  headers: {'content-type': 'application/json', 'cache-control': 'no-store'},
);
Future<HttpControlApi> nativeApi(
  Future<http.Response> Function(http.Request) handler,
) => HttpControlApi.connect(
  DesktopSession(
    baseUrl: Uri.parse('http://127.0.0.1:43129'),
    readToken: 'R' * 43,
    writeToken: 'W' * 43,
    instanceId: 'synthetic',
    expiresAt: DateTime.now().toUtc().add(const Duration(hours: 1)),
  ),
  inspectSession: false,
  client: MockClient(handler),
);
WorkbenchController nativeController(ControlApi api) => WorkbenchController(
  api: api,
  terminalCommands: PreviewTerminalCommandService(),
  previewMode: true,
  closeRuntime: () async {},
)..selectedCaptureKey = nativeCapture;

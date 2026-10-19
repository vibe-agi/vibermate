import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/app/vibermate_app.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/evidence_search_dialog.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  for (final status in [409, 422]) {
    testWidgets(
      'search source invalidation discards previous hits and cursor $status',
      (tester) async {
        final api = _InvalidatedSearchApi(status);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.delegate.close,
        );
        addTearDown(controller.dispose);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Builder(
              builder: (context) => TextButton(
                onPressed: () => showEvidenceSearchDialog(
                  context,
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.english),
                ),
                child: const Text('Search'),
              ),
            ),
          ),
        );
        await tester.tap(find.text('Search'));
        await tester.pumpAndSettle();
        await tester.enterText(
          find.byKey(const Key('evidence-search-query')),
          'provider_transport_failed',
        );
        await tester.pump();
        await tester.tap(find.byKey(const Key('evidence-search-submit')));
        await tester.pumpAndSettle();
        expect(
          find.byKey(Key('evidence-search-result-${api.hitID}')),
          findsOneWidget,
        );
        await tester.tap(find.byKey(const Key('evidence-search-load-more')));
        await tester.pumpAndSettle();
        expect(
          find.byKey(Key('evidence-search-result-${api.hitID}')),
          findsNothing,
        );
        expect(
          find.byKey(const Key('evidence-search-load-more')),
          findsNothing,
        );
        await tester.pump();
        await tester.tap(find.byKey(const Key('evidence-search-submit')));
        await tester.pumpAndSettle();
        expect(
          find.byKey(Key('evidence-search-result-${api.hitID}')),
          findsOneWidget,
        );
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  test('preview search excludes bodies and returns failure metadata', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final failures = await api.searchEvidence(
      const EvidenceSearchRequest(query: 'provider_transport_failed'),
    );
    expect(
      failures.items.map((item) => item.activity.id),
      contains('run-1-exchange-217'),
    );
    final workspaces = await api.searchEvidence(
      const EvidenceSearchRequest(query: 'vibermate', limit: 10),
    );
    expect(workspaces.items, isNotEmpty);
    expect(workspaces.items.first.matches, contains('workspace'));
    final bodies = await api.searchEvidence(
      const EvidenceSearchRequest(
        query: 'Continue with the next verified implementation step.',
      ),
    );
    expect(bodies.items, isEmpty);
  });

  testWidgets('390px search opens the exact retained Exchange', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: false),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('evidence-search-open')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Searches retained metadata across all Captures. Message bodies are not searched.',
      ),
      findsOneWidget,
    );
    await tester.enterText(
      find.byKey(const Key('evidence-search-query')),
      'provider_transport_failed',
    );
    await tester.pump();
    await tester.tap(find.byKey(const Key('evidence-search-submit')));
    await tester.pumpAndSettle();

    final result = find.byKey(
      const Key('evidence-search-result-run-1-exchange-217'),
    );
    expect(result, findsOneWidget);
    expect(find.textContaining('Matched: error'), findsOneWidget);
    await tester.tap(result);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
    await tester.pump();
    expect(find.text('Search result'), findsWidgets);
    expect(find.textContaining('run-1-exchange-217'), findsOneWidget);

    await tester.tap(find.text('Dismiss').last);
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('evidence-search-query')),
      'Continue with the next verified implementation step.',
    );
    await tester.pump();
    await tester.tap(find.byKey(const Key('evidence-search-submit')));
    await tester.pumpAndSettle();
    expect(
      find.text('No retained metadata matches these filters.'),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

  testWidgets('390px Chinese search filters stay usable', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      const ViberMateApp(previewMode: true, preferChinese: true),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('evidence-search-open')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('evidence-search-more-filters')));
    await tester.pumpAndSettle();

    expect(find.text('流量策略 ID'), findsOneWidget);
    expect(find.text('账号 ID'), findsOneWidget);
    expect(find.text('模型'), findsOneWidget);
    expect(find.text('工具名称'), findsOneWidget);
    expect(find.text('错误码'), findsOneWidget);
    await tester.tap(find.byKey(const Key('evidence-search-status')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('失败').last);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('evidence-search-submit')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('evidence-search-results')), findsOneWidget);
    expect(find.textContaining('命中：状态'), findsWidgets);
    expect(tester.takeException(), isNull);

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });
}

final class _InvalidatedSearchApi implements ControlApi {
  String? hitID;
  _InvalidatedSearchApi(this.status);
  final int status;
  final delegate = PreviewControlApi();
  @override
  Future<EvidenceSearchPage> searchEvidence(
    EvidenceSearchRequest request,
  ) async {
    if (request.cursor != null) {
      throw ControlProblem(
        status: status,
        reasonCode: status == 409
            ? 'inspection_changed'
            : 'invalid_control_request',
        messageKey: 'error.inspection_changed',
      );
    }
    final page = await delegate.searchEvidence(request);
    hitID = page.items.first.activity.id;
    return EvidenceSearchPage(
      items: [page.items.first],
      nextCursor: 'source-bound-search',
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('${invocation.memberName}');
}

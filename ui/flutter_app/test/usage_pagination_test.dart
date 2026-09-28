import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/usage_dashboard_view.dart';

import 'runtime_usage_fixture.dart';

Map<String, Object?> group(String id, String dimension, int calls) {
  final value = usageGroupPayload(id)
    ..['dimension'] = dimension
    ..['agentApiCalls'] = calls
    ..['succeeded'] = calls
    ..['failed'] = 0
    ..['cost'] = {
      'nanoUsd': calls * 1000000,
      'pricedCalls': calls,
      'partialCalls': 0,
      'unpricedCalls': 0,
    };
  final tokens = tokenUsagePayload();
  for (final key in tokens.keys.toList()) {
    tokens[key] = {
      'tokens': calls * 10,
      'knownCalls': calls,
      'unknownCalls': 0,
    };
  }
  return value..['tokens'] = tokens;
}

RuntimeUsageReport report({RuntimeUsageQuery? query}) {
  final payload = runtimeUsagePayload();
  if (query == null) {
    payload['total'] = group('all', '', 110);
    payload['days'] = [
      {
        ...dayUsagePayload('2026-08-24'),
        'agentApiCalls': 110,
        'succeeded': 110,
        'failed': 0,
        'tokens': (payload['total'] as Map)['tokens'],
        'cost': (payload['total'] as Map)['cost'],
      },
    ];
  } else {
    final root = query.filters.isEmpty;
    final start = query.cursor.isEmpty ? 0 : 50;
    payload['dimension'] = query.groupBy;
    payload['filters'] = [
      for (final e in query.filters.entries)
        {'dimension': e.key, 'id': e.value},
    ];
    payload['total'] = null;
    payload['days'] = <Object?>[];
    payload['groups'] = root
        ? [
            for (var i = start; i < (start == 0 ? 50 : 55); i++)
              group('${query.groupBy}-$i', query.groupBy, 2),
          ]
        : [group('alice', query.groupBy, 2)];
    payload['nextCursor'] = root && start == 0 ? 'page-50' : '';
  }
  return RuntimeUsageReport.fromJson(payload, 'usage');
}

Widget dashboard(UsagePageLoader loadPage) => MaterialApp(
  theme: ViberTheme.light(),
  home: Scaffold(
    body: PersonalUsageDashboard(
      report: report(),
      loading: false,
      error: null,
      onRefresh: () {},
      copy: AppCopy.forLanguage(AppLanguage.english),
      loadPage: loadPage,
    ),
  ),
);

void main() {
  testWidgets('usage pages only 50 groups and keeps the complete subtotal', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final requests = <RuntimeUsageQuery>[];
    await tester.pumpWidget(
      dashboard((query) async {
        requests.add(query);
        return report(query: query);
      }),
    );
    await tester.pumpAndSettle();
    final table = find.byKey(const Key('usage-breakdown-table'));
    expect(tester.widget<DataTable>(table).rows.length, 51);
    expect(requests.single.groupBy, 'project');
    expect(requests.single.snapshot, report().snapshot);
    final next = find.byTooltip('Next page');
    await tester.ensureVisible(next);
    await tester.tap(next);
    await tester.pumpAndSettle();
    expect(tester.widget<DataTable>(table).rows.length, 6);
    expect(requests.last.cursor, 'page-50');
    expect(
      find.descendant(of: table, matching: find.text('110')),
      findsOneWidget,
    );
    final expand = find.byKey(const Key('usage-expand-project-50'));
    await tester.ensureVisible(expand);
    await tester.tap(expand);
    await tester.pumpAndSettle();
    expect(requests.last.groupBy, 'caller');
    expect(requests.last.filters, {'project': 'project-50'});
    expect(requests.last.cursor, isEmpty);
    expect(tester.widget<DataTable>(table).rows.length, 2);
    expect(
      find.descendant(of: table, matching: find.text('110')),
      findsNothing,
    );
    expect(find.text('Selection subtotal'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('usage ignores an old group response after changing dimension', (
    tester,
  ) async {
    final gate = Completer<RuntimeUsageReport>();
    RuntimeUsageQuery? pending;
    await tester.pumpWidget(
      dashboard((query) {
        if (query.groupBy == 'project') {
          pending = query;
          return gate.future;
        }
        return Future.value(report(query: query));
      }),
    );
    await tester.pump();
    final models = find.byKey(const Key('usage-group-models'));
    await tester.ensureVisible(models);
    await tester.tap(models);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('usage-expand-model-0')), findsOneWidget);
    gate.complete(report(query: pending!));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('usage-expand-model-0')), findsOneWidget);
    expect(find.byKey(const Key('usage-expand-project-0')), findsNothing);
    expect(tester.takeException(), isNull);
  });
}

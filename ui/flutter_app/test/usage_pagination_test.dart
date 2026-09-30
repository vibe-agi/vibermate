import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart' show ControlProblem;
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/design/workbench_widgets.dart';
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

RuntimeUsageReport report({RuntimeUsageQuery? query, String? snapshot}) {
  final payload = runtimeUsagePayload();
  if (snapshot != null) payload['snapshot'] = snapshot;
  if (query == null || query.groupBy.isEmpty) {
    final calls = query == null || query.filters.isEmpty ? 110 : 2;
    payload['total'] = group('all', '', calls);
    payload['days'] = [
      {
        ...dayUsagePayload('2026-08-24'),
        'agentApiCalls': calls,
        'succeeded': calls,
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

Widget dashboard(UsagePageLoader loadPage, {RuntimeUsageReport? summary}) =>
    MaterialApp(
      theme: ViberTheme.light(),
      home: Scaffold(
        body: PersonalUsageDashboard(
          report: summary ?? report(),
          loading: false,
          error: null,
          onRefresh: () {},
          copy: AppCopy.forLanguage(AppLanguage.english),
          loadPage: loadPage,
        ),
      ),
    );

void main() {
  mainSnapshotSemantics();
  testWidgets('regrouping to a short report keeps the outer reading position', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(
      dashboard(
        (query) async => report(
          query: query.groupBy == 'model'
              ? RuntimeUsageQuery(
                  from: query.from,
                  until: query.until,
                  timeZone: query.timeZone,
                  groupBy: query.groupBy,
                  filters: const {'project': 'short'},
                  snapshot: query.snapshot,
                )
              : query,
        ),
      ),
    );
    await tester.pumpAndSettle();
    final chip = find.byKey(const Key('usage-group-models'));
    await tester.ensureVisible(chip);
    final before = tester.getTopLeft(chip).dy;
    final viewport = find.byKey(const Key('usage-breakdown-viewport'));
    final height = tester.getSize(viewport).height;
    await tester.tap(chip);
    await tester.pumpAndSettle();
    expect(tester.getSize(viewport).height, height);
    expect(tester.getTopLeft(chip).dy, before);
    expect(tester.takeException(), isNull);
  });
  for (final reason in ['runtime_unavailable', 'usage_snapshot_changed']) {
    testWidgets('failed navigation retains its page and rows: $reason', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1440, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      var fail = false;
      var requests = 0;
      Future<RuntimeUsageReport> load(RuntimeUsageQuery query) async {
        requests++;
        if (fail && query.groupBy.isNotEmpty) {
          throw ControlProblem(
            status: reason == 'usage_snapshot_changed' ? 409 : 503,
            reasonCode: reason,
            messageKey: reason,
          );
        }
        return report(query: query);
      }

      await tester.pumpWidget(dashboard(load));
      await tester.pumpAndSettle();
      fail = true;
      await tester.ensureVisible(find.byTooltip('Next page'));
      await tester.tap(find.byTooltip('Next page'));
      await tester.pumpAndSettle();
      expect(find.text('Page 1'), findsOneWidget);
      expect(find.byKey(const Key('usage-expand-project-0')), findsOneWidget);
      expect(find.text('Page 2'), findsNothing);
      expect(requests, lessThanOrEqualTo(6));
      fail = false;
      if (reason == 'usage_snapshot_changed') {
        // An expired snapshot cannot be paged; only an update continues.
        expect(find.textContaining('Usage changed.'), findsOneWidget);
        final next = tester.widget<IconButton>(
          find.ancestor(
            of: find.byTooltip('Next page'),
            matching: find.byType(IconButton),
          ),
        );
        expect(next.onPressed, isNull);
        return;
      }
      await tester.tap(find.byTooltip('Next page'));
      await tester.pumpAndSettle();
      expect(find.text('Page 2'), findsOneWidget);
      fail = true;
      final update = find.byKey(const Key('usage-breakdown-update'));
      await tester.ensureVisible(update);
      await tester.tap(update);
      await tester.pumpAndSettle();
      expect(find.text('Page 2'), findsOneWidget);
      expect(find.byKey(const Key('usage-expand-project-50')), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets(
    'breakdown keeps its column headings visible while reading rows',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1440, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(dashboard((query) async => report(query: query)));
      await tester.pumpAndSettle();
      await tester.ensureVisible(
        find.byKey(const Key('usage-expand-project-40')),
      );
      await tester.pumpAndSettle();
      expect(
        tester.getTopLeft(find.text('Git project').last).dy,
        greaterThan(0),
      );
      expect(
        tester.getBottomLeft(find.text('Git project').last).dy,
        lessThan(900),
      );
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('drilldown renews an expired snapshot without an error prompt', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    var changed = false;
    final requests = <RuntimeUsageQuery>[];
    Future<RuntimeUsageReport> load(RuntimeUsageQuery query) async {
      requests.add(query);
      if (changed && query.snapshot == 'a' * 64) {
        throw const ControlProblem(
          status: 409,
          reasonCode: 'usage_snapshot_changed',
          messageKey: 'usage_snapshot_changed',
        );
      }
      return report(query: query, snapshot: changed ? 'b' * 64 : 'a' * 64);
    }

    await tester.pumpWidget(dashboard(load));
    await tester.pumpAndSettle();
    changed = true;
    final expand = find.byKey(const Key('usage-expand-project-0'));
    await tester.ensureVisible(expand);
    await tester.tap(expand);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('usage-expand-alice')), findsOneWidget);
    expect(find.textContaining('Usage changed.'), findsNothing);
    expect(find.text('Retry'), findsNothing);
    expect(requests.last.snapshot, 'b' * 64);
    expect(requests.last.filters, {'project': 'project-0'});
    final table = find.byKey(const Key('usage-breakdown-table'));
    expect(
      find.descendant(of: table, matching: find.text('110')),
      findsNothing,
    );
    expect(find.text('Selection subtotal'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('new usage does not evict a reader from a later page', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final requests = <RuntimeUsageQuery>[];
    var newest = 'a' * 64;
    Future<RuntimeUsageReport> load(RuntimeUsageQuery query) async {
      requests.add(query);
      return report(
        query: query,
        snapshot: query.snapshot.isEmpty ? newest : query.snapshot,
      );
    }

    await tester.pumpWidget(dashboard(load));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byTooltip('Next page'));
    await tester.tap(find.byTooltip('Next page'));
    await tester.pumpAndSettle();
    final table = find.byKey(const Key('usage-breakdown-table'));
    final before = tester.getTopLeft(table);
    final count = requests.length;
    newest = 'b' * 64;
    await tester.pumpWidget(
      dashboard(load, summary: report(snapshot: 'b' * 64)),
    );
    await tester.pumpAndSettle();
    expect(find.text('Page 2'), findsOneWidget);
    expect(find.byKey(const Key('usage-expand-project-50')), findsOneWidget);
    expect(tester.getTopLeft(table), before);
    expect(requests.length, count);
    expect(find.text('New usage available'), findsOneWidget);
    final update = find.byKey(const Key('usage-breakdown-update'));
    await tester.ensureVisible(update);
    await tester.tap(update);
    await tester.pumpAndSettle();
    expect(requests.last.snapshot, 'b' * 64);
    expect(requests.last.cursor, 'page-50');
    expect(find.text('Page 2'), findsOneWidget);
    expect(find.text('New usage available'), findsNothing);
  });

  testWidgets('background refresh keeps the table and reading position', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final gate = Completer<RuntimeUsageReport>();
    var refreshing = false;
    RuntimeUsageQuery? pending;
    Future<RuntimeUsageReport> load(RuntimeUsageQuery query) {
      if (refreshing) {
        pending = query;
        return gate.future;
      }
      return Future.value(report(query: query));
    }

    await tester.pumpWidget(dashboard(load));
    await tester.pumpAndSettle();
    final table = find.byKey(const Key('usage-breakdown-table'));
    await tester.ensureVisible(
      find.byKey(const Key('usage-expand-project-10')),
    );
    final tableTop = tester.getTopLeft(table);
    final tableSize = tester.getSize(table);
    refreshing = true;
    final updated = report(snapshot: 'b' * 64);
    await tester.pumpWidget(dashboard(load, summary: updated));
    await tester.pump();
    expect(table, findsOneWidget);
    expect(tester.getTopLeft(table), tableTop);
    expect(tester.getSize(table), tableSize);
    gate.complete(report(query: pending!, snapshot: updated.snapshot));
    await tester.pumpAndSettle();
    expect(tester.getTopLeft(table), tableTop);
    expect(tester.takeException(), isNull);
  });

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

/// Mirrors the Runtime: a snapshot names one complete state of the period, and
/// any request that names an older snapshot is refused once usage changes.
final class _SnapshotServer {
  String current = 'a' * 64;
  final requests = <RuntimeUsageQuery>[];
  bool changeOnEverySummary = false;
  bool unavailable = false;

  var _version = 0;

  void change() {
    _version++;
    current = _version.toRadixString(16).padLeft(64, 'b');
  }

  Future<RuntimeUsageReport> load(RuntimeUsageQuery query) async {
    requests.add(query);
    if (unavailable) {
      throw const ControlProblem(
        status: 503,
        reasonCode: 'runtime_unavailable',
        messageKey: 'runtime_unavailable',
      );
    }
    if (query.snapshot.isNotEmpty && query.snapshot != current) {
      throw const ControlProblem(
        status: 409,
        reasonCode: 'usage_snapshot_changed',
        messageKey: 'usage_snapshot_changed',
      );
    }
    final served = report(query: query, snapshot: current);
    // Continuous writes: new usage lands between a summary and its page.
    if (query.groupBy.isEmpty && changeOnEverySummary) change();
    return served;
  }
}

void mainSnapshotSemantics() {
  testWidgets('an expired snapshot never moves a reader to another page', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final server = _SnapshotServer();
    await tester.pumpWidget(
      dashboard(server.load, summary: report(snapshot: server.current)),
    );
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byTooltip('Next page'));
    await tester.tap(find.byTooltip('Next page'));
    await tester.pumpAndSettle();
    expect(find.text('Page 2'), findsOneWidget);

    server.change();
    await tester.tap(find.byTooltip('Previous page'));
    await tester.pumpAndSettle();
    expect(find.text('Page 2'), findsOneWidget);
    expect(find.byKey(const Key('usage-expand-project-50')), findsOneWidget);
    expect(find.textContaining('Usage changed.'), findsOneWidget);
    final previous = tester.widget<IconButton>(
      find.ancestor(
        of: find.byTooltip('Previous page'),
        matching: find.byType(IconButton),
      ),
    );
    expect(previous.onPressed, isNull);

    final update = find.byKey(const Key('usage-breakdown-update'));
    await tester.ensureVisible(update);
    await tester.tap(update);
    await tester.pumpAndSettle();
    expect(find.text('Page 2'), findsOneWidget);
    expect(server.requests.last.snapshot, server.current);
    expect(find.textContaining('Usage changed.'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('a drill-down that cannot settle keeps the previous level', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final server = _SnapshotServer();
    await tester.pumpWidget(
      dashboard(server.load, summary: report(snapshot: server.current)),
    );
    await tester.pumpAndSettle();
    server
      ..change()
      ..changeOnEverySummary = true;
    final expand = find.byKey(const Key('usage-expand-project-0'));
    await tester.ensureVisible(expand);
    await tester.tap(expand);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('usage-expand-project-0')), findsOneWidget);
    expect(find.byKey(const Key('usage-expand-alice')), findsNothing);
    expect(find.textContaining('Usage changed.'), findsOneWidget);
    expect(find.text('Selection subtotal'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('an unavailable Runtime is shown as an error with retry', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1440, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final server = _SnapshotServer();
    await tester.pumpWidget(
      dashboard(server.load, summary: report(snapshot: server.current)),
    );
    await tester.pumpAndSettle();
    server.unavailable = true;
    await tester.ensureVisible(find.byTooltip('Next page'));
    await tester.tap(find.byTooltip('Next page'));
    await tester.pumpAndSettle();
    expect(find.text('Page 1'), findsOneWidget);
    final notice = find.widgetWithText(
      InlineNotice,
      'Could not load this breakdown.',
    );
    expect(notice, findsOneWidget);
    expect(tester.widget<InlineNotice>(notice).error, isTrue);
    server.unavailable = false;
    await tester.tap(find.descendant(of: notice, matching: find.text('Retry')));
    await tester.pumpAndSettle();
    expect(find.text('Page 2'), findsOneWidget);
    expect(find.text('Could not load this breakdown.'), findsNothing);
    expect(tester.takeException(), isNull);
  });
}

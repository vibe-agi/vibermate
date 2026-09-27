import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/usage_dashboard_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final screenshots = Platform.environment['VIBERMATE_USAGE_SCREENSHOTS'];
  setUpAll(() async {
    if (screenshots == null) return;
    for (final entry in {
      viberSystemFontFamily: '/System/Library/Fonts/SFNS.ttf',
      'Ahem': '/System/Library/Fonts/SFNS.ttf',
      'Roboto': '/System/Library/Fonts/SFNS.ttf',
      'PingFang SC': Platform.environment['VIBERMATE_QA_CJK_FONT'],
    }.entries) {
      if (entry.value == null) continue;
      final loader = FontLoader(entry.key);
      loader.addFont(
        File(
          entry.value!,
        ).readAsBytes().then((bytes) => ByteData.sublistView(bytes)),
      );
      await loader.load();
    }
    final icons = FontLoader('MaterialIcons');
    icons.addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
    await icons.load();
  });

  Future<void> capture(WidgetTester tester, GlobalKey key, String name) async {
    if (screenshots == null) return;
    await tester.runAsync(() async {
      final boundary =
          key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
      final image = await boundary.toImage(pixelRatio: 1);
      final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
      await Directory(screenshots).create(recursive: true);
      await File(
        '$screenshots/$name.png',
      ).writeAsBytes(bytes!.buffer.asUint8List());
      image.dispose();
    });
  }

  for (final member in [false, true]) {
    for (final width in [390.0, 1440.0]) {
      testWidgets(
        'usage cost ${member ? 'member' : 'owner'} layout at $width',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 900));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final api = PreviewControlApi(seedRuntimeUsers: member);
          final controller = WorkbenchController(
            api: api,
            terminalCommands: PreviewTerminalCommandService(),
            previewMode: false,
            serverManagement: true,
            closeRuntime: api.close,
            initialPreferences: WorkbenchPreferences(
              section: WorkbenchSection.usage,
              language: member
                  ? AppLanguage.english
                  : AppLanguage.simplifiedChinese,
            ),
          );
          await controller.initialize();
          final root = GlobalKey();
          var theme = member ? ViberTheme.light() : ViberTheme.dark();
          // Test bindings otherwise substitute Ahem for inherited body text.
          // Real App/Web render the system font; screenshots must do the same.
          if (screenshots != null) {
            theme = theme.copyWith(
              textTheme: theme.textTheme.apply(
                fontFamily: viberSystemFontFamily,
              ),
            );
          }
          final copy = AppCopy.forLanguage(
            member ? AppLanguage.english : AppLanguage.simplifiedChinese,
          );
          await tester.pumpWidget(
            RepaintBoundary(
              key: root,
              child: MaterialApp(
                debugShowCheckedModeBanner: false,
                theme: theme,
                home: member
                    ? Scaffold(
                        body: PersonalUsageDashboard(
                          report: controller.runtimeUsage,
                          loading: false,
                          error: null,
                          onRefresh: () {},
                          copy: copy,
                        ),
                      )
                    : WorkbenchShell(controller: controller),
              ),
            ),
          );
          await tester.pumpAndSettle();
          expect(find.byKey(const Key('usage-estimated-cost')), findsOneWidget);
          expect(tester.takeException(), isNull);
          final name =
              '${member ? 'member-light' : 'owner-dark-zh'}-${width.toInt()}';
          await capture(tester, root, name);
          final selector = find.byKey(const Key('usage-trend-metric'));
          await tester.ensureVisible(selector);
          await tester.pumpAndSettle();
          await tester.tap(
            find.descendant(
              of: selector,
              matching: find.text(copy('usage.cost.short')),
            ),
          );
          await tester.pumpAndSettle();
          expect(find.text(copy('usage.cost.daily')), findsOneWidget);
          expect(tester.takeException(), isNull);
          await capture(tester, root, '$name-cost');
          final projects = find.byKey(const Key('usage-group-projects'));
          await tester.ensureVisible(projects);
          await tester.tap(projects);
          await tester.pumpAndSettle();
          for (final path in [
            'projects/project-one',
            'projects/project-one/branch%3Amain',
            'projects/project-one/branch%3Amain/alice',
          ]) {
            final expand = find.byKey(Key('usage-expand-$path'));
            await tester.ensureVisible(expand);
            await tester.tap(expand);
            await tester.pumpAndSettle();
          }
          final table = find.byKey(const Key('usage-breakdown-table'));
          expect(
            find.descendant(of: table, matching: find.text('gpt-5')),
            findsOneWidget,
          );
          expect(
            find.descendant(of: table, matching: find.text('25,864')),
            findsNWidgets(5),
          );
          expect(
            find.descendant(of: table, matching: find.text('≥ \$0.04')),
            findsNWidgets(5),
          );
          expect(
            find.descendant(
              of: table,
              matching: find.text(copy('usage.total')),
            ),
            findsOneWidget,
          );
          expect(tester.takeException(), isNull);
          if (width < 600) {
            await tester.drag(
              find.byKey(const Key('usage-breakdown-scroll')),
              const Offset(600, 0),
            );
            await tester.pumpAndSettle();
          }
          await capture(tester, root, '$name-projects');
          if (!member) {
            await controller.refreshUsage();
            await tester.pumpAndSettle();
            expect(
              find.descendant(of: table, matching: find.text('gpt-5')),
              findsOneWidget,
            );
          }
          if (!member) {
            await controller.setUsageRange(365);
            await tester.pumpAndSettle();
            expect(find.text(copy('usage.cost.monthly')), findsOneWidget);
            expect(tester.takeException(), isNull);
          }
          controller.dispose();
          await tester.pumpWidget(const SizedBox.shrink());
        },
      );
    }
  }

  testWidgets(
    'unpriced usage is not displayed as zero and refresh errors preserve data',
    (tester) async {
      final api = PreviewControlApi();
      final report = await api.runtimeUsage(
        RuntimeUsageQuery(
          from: '2026-09-21',
          until: '2026-09-28',
          timeZone: 'UTC',
        ),
      );
      final unpriced = RuntimeUsageReport(
        generatedAt: report.generatedAt,
        period: report.period,
        truncated: false,
        days: report.days,
        users: report.users,
        cost: const RuntimeCostEstimate(unpricedCalls: 18),
        pricing: const RuntimePricingInfo(state: 'unavailable'),
      );
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: PersonalUsageDashboard(
              report: unpriced,
              loading: false,
              error: 'Refresh failed',
              onRefresh: () {},
              copy: AppCopy.forLanguage(AppLanguage.english),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: find.byKey(const Key('usage-estimated-cost')),
          matching: find.text('—'),
        ),
        findsOneWidget,
      );
      expect(find.text('Refresh failed'), findsOneWidget);
      expect(
        find.textContaining('Reference prices unavailable'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      await api.close();
    },
  );
}

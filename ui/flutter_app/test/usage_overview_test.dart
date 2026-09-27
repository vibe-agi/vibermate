import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/features/workbench/workbench_shell.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final retain in [false, true]) {
    testWidgets(
      'Capture with content=$retain selects the honest default view',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1280, 800));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(retainConversationBodies: retain);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        await controller.initialize();
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: WorkbenchShell(controller: controller),
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const Key('capture-row-managed_run:run-1')),
        );
        await tester.pumpAndSettle();
        expect(
          find.byKey(const Key('capture-request-overview')),
          retain ? findsNothing : findsOneWidget,
        );
        if (!retain) {
          expect(find.textContaining('No conversation bodies'), findsOneWidget);
          final completed = controller.selectedCapturePage!;
          final activity = completed.items.first;
          controller.selectedCapturePage = ActivityPage(
            items: [
              ActivityRecord(
                id: 'pending-next-request',
                occurredAt: activity.occurredAt,
                title: activity.title,
                status: 'pending',
                reasonCode: null,
                source: activity.source,
                conversation: activity.conversation,
                environment: activity.environment,
                parentRefs: activity.parentRefs,
              ),
              ...completed.items,
            ],
            nextCursor: completed.nextCursor,
          );
          // A real view rebuild with a new pending request must keep Overview.
          await tester.binding.setSurfaceSize(const Size(1440, 800));
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsOneWidget,
          );
          controller.selectedCapturePage = completed;
          final mode = find.byKey(const Key('capture-evidence-mode'));
          await tester.tap(
            find.descendant(of: mode, matching: find.text('Request records')),
          );
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsNothing,
          );
          expect(
            find.byKey(const Key('capture-evidence-mode')),
            findsOneWidget,
          );
          // Refresh and navigating away both rebuild the workspace. The
          // selected view belongs to the Capture, not to that widget lifetime.
          await controller.refresh();
          await tester.pumpAndSettle();
          controller.selectSection(WorkbenchSection.environments);
          await tester.pumpAndSettle();
          controller.selectSection(WorkbenchSection.captures);
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('capture-request-overview')),
            findsNothing,
          );
          expect(controller.showCaptureRequestRecords, isTrue);
        }
        expect(tester.takeException(), isNull);
        controller.dispose();
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  for (final width in [390.0, 1440.0]) {
    testWidgets('Local overview at $width groups without requiring members', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(Size(width, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi(seedRuntimeUsers: false);
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: false,
        serverManagement: true,
        closeRuntime: api.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.usage,
        ),
      );
      await controller.initialize();
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: WorkbenchShell(controller: controller),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('usage-create-runtime-user')), findsNothing);
      expect(find.byKey(const Key('usage-member-details')), findsNothing);
      expect(find.byKey(const Key('usage-range')), findsOneWidget);
      expect(controller.usageRangeDays, 7);
      expect(find.byKey(const Key('usage-estimated-cost')), findsOneWidget);
      expect(find.text('≥ \$0.0460'), findsWidgets);
      final metric = find.byKey(const Key('usage-trend-metric'));
      await tester.ensureVisible(metric);
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(of: metric, matching: find.text('Estimated cost')),
      );
      await tester.pumpAndSettle();
      expect(find.text('Daily estimated cost · USD'), findsOneWidget);
      final basis = find.byKey(const Key('usage-price-basis'));
      await tester.ensureVisible(basis);
      await tester.pumpAndSettle();
      await tester.tap(basis);
      await tester.pumpAndSettle();
      expect(find.textContaining('not subscription charges'), findsOneWidget);
      await tester.tap(
        find.descendant(
          of: find.byType(AlertDialog),
          matching: find.byType(TextButton),
        ),
      );
      await tester.pumpAndSettle();
      for (final group in ['accounts', 'models', 'sources']) {
        final chip = find.byKey(Key('usage-group-$group'));
        await tester.ensureVisible(chip);
        await tester.pumpAndSettle();
        await tester.tap(chip);
        await tester.pumpAndSettle();
        expect(find.byKey(const Key('usage-breakdown-table')), findsOneWidget);
        expect(tester.takeException(), isNull);
      }
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }
}

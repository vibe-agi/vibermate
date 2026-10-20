import 'dart:async';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  for (final reducedMotion in [false, true]) {
    testWidgets(
      'hover and keyboard focus preview activation without saving; reduced motion=$reducedMotion',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _ActivationApi();
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.preview.close,
        );
        addTearDown(controller.dispose);
        await controller.refresh();
        controller.selectEnvironment('work');
        await tester.pumpWidget(
          MaterialApp(
            theme: reducedMotion ? ViberTheme.light() : ViberTheme.dark(),
            builder: (context, child) => MediaQuery(
              data: MediaQuery.of(
                context,
              ).copyWith(disableAnimations: reducedMotion),
              child: child!,
            ),
            home: Scaffold(
              body: AnimatedBuilder(
                animation: controller,
                builder: (_, _) => EnvironmentsView(
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.english),
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        EnvironmentRoute route() => controller.selectedEnvironment!.routes
            .singleWhere((route) => route.id == 'anthropic-direct');
        final original = find.byKey(
          const Key('environment-account-original-anthropic-direct'),
        );
        final managed = find.byKey(
          const Key('environment-account-activate-anthropic-work'),
        );
        await tester.ensureVisible(original);
        await tester.pumpAndSettle();
        final controlSize = tester.getSize(original);
        expect(controlSize, tester.getSize(managed));
        final mouse = await tester.createGesture(
          kind: ui.PointerDeviceKind.mouse,
        );
        await mouse.addPointer(location: Offset.zero);
        await mouse.moveTo(tester.getCenter(original));
        await tester.pump(const Duration(milliseconds: 650));
        expect(api.writes, 0);
        expect(route().accountPolicy.mode, 'fixed');
        expect(tester.getSize(original), controlSize);
        expect(
          find.text('Activate Original request account · keep credentials'),
          findsOneWidget,
        );
        await mouse.removePointer();
        await tester.pumpAndSettle();
        await _focus(tester, original);
        expect(api.writes, 0);
        await tester.sendKeyEvent(LogicalKeyboardKey.space);
        await tester.pump();
        expect(api.writes, 1);
        expect(route().accountPolicy.mode, 'fixed');
        await tester.tap(original);
        await tester.pump();
        expect(
          api.writes,
          1,
          reason: 'Pending activation cannot be submitted twice',
        );
        await api.completeActivation();
        await tester.pumpAndSettle();
        expect(route().accountPolicy.mode, 'original');
        final semantics = tester.ensureSemantics();
        expect(
          tester.getSemantics(original).flagsCollection.isSelected,
          ui.Tristate.isTrue,
        );
        expect(
          tester.getSemantics(managed).flagsCollection.isSelected,
          ui.Tristate.isFalse,
        );
        semantics.dispose();
        expect(tester.getSize(original), controlSize);
        await tester.ensureVisible(managed);
        await tester.pumpAndSettle();
        await tester.tap(managed);
        await tester.pump();
        expect(api.writes, 2);
        await api.completeActivation();
        await tester.pumpAndSettle();
        expect(route().accountPolicy.fixedAccountId, 'anthropic-work');
        expect(route().accountPolicy.mode, 'fixed');
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }
}

Future<void> _focus(WidgetTester tester, Finder target) async {
  final element = tester.element(target);
  bool focused() {
    final context = FocusManager.instance.primaryFocus?.context;
    if (context == null) return false;
    var contains = identical(context, element);
    (context as Element).visitAncestorElements((ancestor) {
      if (identical(ancestor, element)) contains = true;
      return !contains;
    });
    return contains;
  }

  for (var i = 0; i < 64 && !focused(); i++) {
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
  }
  expect(focused(), isTrue, reason: 'Activation is keyboard reachable');
}

final class _ActivationApi extends Fake implements ControlApi {
  final preview = PreviewControlApi(seedCaptures: false);
  int writes = 0;
  Completer<EnvironmentAccountActivation>? _pending;
  (EnvironmentRecord, String, String)? _request;

  @override
  Future<DashboardData> loadDashboard() => preview.loadDashboard();
  @override
  Future<List<ApprovalRecord>> pendingApprovals() => preview.pendingApprovals();
  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) =>
      preview.accountFacts(id, history: history);
  @override
  Future<EnvironmentAccountActivation> activateEnvironmentAccount(
    EnvironmentRecord environment,
    String routeId,
    String accountId,
  ) {
    writes++;
    _request = (environment, routeId, accountId);
    return (_pending = Completer<EnvironmentAccountActivation>()).future;
  }

  Future<void> completeActivation() async {
    final (environment, route, account) = _request!;
    final result = await preview.activateEnvironmentAccount(
      environment,
      route,
      account,
    );
    _pending!.complete(result);
    _pending = null;
    _request = null;
  }
}

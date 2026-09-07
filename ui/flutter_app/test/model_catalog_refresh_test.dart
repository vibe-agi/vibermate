import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  test(
    'upstream catalogs always consult revision-aware server cache',
    () async {
      final api = _ModelCatalogApi();
      final controller = _controller(api);
      addTearDown(controller.dispose);
      addTearDown(api.preview.close);
      final first = await controller.upstreamModels(
        'target.anthropic.official',
        accountId: 'anthropic-work',
      );
      api.includeNewModel = true;
      final second = await controller.upstreamModels(
        'target.anthropic.official',
        accountId: 'anthropic-work',
      );
      expect(api.refreshRequests, [false, false]);
      expect(
        first.models.map((model) => model.id),
        isNot(contains('gpt-6-astra')),
      );
      expect(second.models.map((model) => model.id), contains('gpt-6-astra'));
      await controller.upstreamModels(
        'target.anthropic.official',
        accountId: 'anthropic-work',
        refresh: true,
      );
      expect(api.refreshRequests, [false, false, true]);
    },
  );

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets('new upstream models are searchable after refresh: $language', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1100, 860));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _ModelCatalogApi();
      final controller = _controller(api);
      addTearDown(controller.dispose);
      addTearDown(api.preview.close);
      controller.data = await api.preview.loadDashboard();
      controller.selectEnvironment('work');
      final copy = AppCopy.forLanguage(language);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: EnvironmentsView(controller: controller, copy: copy),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('environment-edit')));
      await tester.pumpAndSettle();
      final mappings = find.byKey(
        const Key('environment-route-model-anthropic-direct'),
      );
      await tester.ensureVisible(mappings);
      await tester.tap(mappings);
      await tester.pumpAndSettle();
      expect(
        find.text(copy('environment.model.request_catalog')),
        findsOneWidget,
      );
      expect(
        find.text(copy('environment.model.catalog_sources')),
        findsOneWidget,
      );
      api.includeNewModel = true;
      await tester.tap(
        find.byTooltip(copy('environment.model.refresh_upstream')),
      );
      await tester.pumpAndSettle();
      expect(api.refreshRequests, [false, true]);
      final field = find.byKey(const Key('environment-model-upstream-0'));
      await tester.enterText(field, 'gpt-6');
      await tester.pumpAndSettle();
      final astra = find.byKey(
        const Key('environment-model-upstream-0-option-gpt-6-astra'),
      );
      expect(astra, findsOneWidget);
      await tester.tap(astra);
      await tester.pumpAndSettle();
      expect(tester.widget<TextField>(field).controller!.text, 'gpt-6-astra');
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pump();
    });
  }

  for (final dismissEarly in [false, true]) {
    testWidgets(
      dismissEarly
          ? 'closing model mappings during discovery is safe'
          : 'typing before discovery completes preserves editing and keyboard selection',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1100, 860));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _ModelCatalogApi()..includeNewModel = true;
        api.waitForCatalog = Completer<void>();
        final controller = _controller(api);
        addTearDown(controller.dispose);
        addTearDown(api.preview.close);
        controller.data = await api.preview.loadDashboard();
        controller.selectEnvironment('work');
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: EnvironmentsView(
                controller: controller,
                copy: AppCopy.forLanguage(AppLanguage.english),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('environment-edit')));
        await tester.pumpAndSettle();
        final mappings = find.byKey(
          const Key('environment-route-model-anthropic-direct'),
        );
        await tester.ensureVisible(mappings);
        await tester.pumpAndSettle();
        await tester.tap(mappings);
        await tester.pump(const Duration(seconds: 1));
        final field = find.byKey(const Key('environment-model-upstream-0'));
        await tester.enterText(field, 'gpt-6');
        await tester.pump();
        final before = tester.widget<TextField>(field).controller!.value;
        if (dismissEarly) {
          Navigator.of(tester.element(field)).pop();
          await tester.pumpAndSettle();
        }
        api.waitForCatalog!.complete();
        await tester.pumpAndSettle();
        if (dismissEarly) {
          expect(field, findsNothing);
        } else {
          expect(
            find.byKey(
              const Key('environment-model-upstream-0-option-gpt-6-astra'),
            ),
            findsOneWidget,
          );
          final after = tester.widget<TextField>(field);
          expect(after.controller!.value, before);
          expect(after.focusNode!.hasFocus, isTrue);
          await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
          await tester.testTextInput.receiveAction(TextInputAction.done);
          await tester.pumpAndSettle();
          expect(
            tester.widget<TextField>(field).controller!.text,
            'gpt-6-astra',
          );
        }
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
        await tester.pump();
      },
    );
  }
}

WorkbenchController _controller(_ModelCatalogApi api) => WorkbenchController(
  api: api,
  terminalCommands: PreviewTerminalCommandService(),
  previewMode: true,
  closeRuntime: api.preview.close,
);

final class _ModelCatalogApi implements ControlApi {
  final preview = PreviewControlApi();
  final refreshRequests = <bool>[];
  bool includeNewModel = false;
  Completer<void>? waitForCatalog;

  @override
  Future<CodeLibraryCatalog> codeLibrary() => preview.codeLibrary();

  @override
  Future<ClientModelCatalog> clientModels(String protocol) =>
      preview.clientModels(protocol);

  @override
  Future<UpstreamModelCatalog> upstreamModels(
    String endpointId, {
    required String accountId,
    bool refresh = false,
  }) async {
    refreshRequests.add(refresh);
    await waitForCatalog?.future;
    final base = await preview.upstreamModels(
      endpointId,
      accountId: accountId,
      refresh: refresh,
    );
    return UpstreamModelCatalog(
      endpointId: base.endpointId,
      endpointRevision: base.endpointRevision,
      accountId: base.accountId,
      accountRevision: base.accountRevision,
      credentialEpoch: base.credentialEpoch,
      observedAt: base.observedAt,
      availabilitySource: base.availabilitySource,
      models: [
        ...base.models,
        if (includeNewModel)
          const UpstreamModel(
            id: 'gpt-6-astra',
            displayName: 'GPT-6-Astra',
            ownedBy: 'fixture',
            verifiedAvailable: true,
            contextLimit: 0,
            outputLimit: 0,
          ),
      ],
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

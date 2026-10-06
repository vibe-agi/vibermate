import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/environment_dry_run_dialog.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

class _UnresolvedDryRunApi implements ControlApi {
  _UnresolvedDryRunApi(this.environment);
  final EnvironmentRecord environment;
  @override
  Future<EnvironmentDryRun> dryRunEnvironment(
    EnvironmentDryRunInput input,
  ) async => EnvironmentDryRun(
    source: input.source,
    publishedRevision: environment.revision,
    draftRevision: 1,
    decision: EnvironmentDryRunDecision(
      environmentId: environment.id,
      environmentRevision: environment.revision,
      destinationKind: 'original',
      providerOrigin: 'https://chatgpt.com',
      routeId: '',
      accountId: '',
      requestedModel: 'synthetic',
      effectiveModel: 'synthetic',
      modelMapped: false,
      networkExitId: '',
      bodyChanged: false,
      protocolChangedTopLevelFields: const [],
      changedHeaderNames: const [],
      changedTopLevelFields: const [],
      unverified: const ['network_exit'],
    ),
  );
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

void main() {
  for (final chinese in [false, true]) {
    testWidgets(
      'Unresolved dry-run actual ${chinese ? "zh" : "EN"} row ignores cached Direct name',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1200, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final fixture = PreviewControlApi(seedCaptures: false);
        final environment = (await fixture.loadDashboard()).environments
            .firstWhere((value) => value.id == 'work');
        final controller = WorkbenchController(
          api: _UnresolvedDryRunApi(environment),
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: fixture.close,
        );
        addTearDown(controller.dispose);
        final draft = EnvironmentDraft(
          environmentId: environment.id,
          baseRevision: environment.revision,
          draftRevision: 1,
          candidateDigest: List.filled(64, 'a').join(),
          candidate: environment,
        );
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: EnvironmentDryRunDialog(
                controller: controller,
                environment: environment,
                reviewedDraft: draft,
                copy: AppCopy.forLanguage(
                  chinese ? AppLanguage.simplifiedChinese : AppLanguage.english,
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const Key('environment-dry-run-run')));
        await tester.pumpAndSettle();
        final result = find.byKey(const Key('environment-dry-run-result'));
        expect(result, findsOneWidget);
        expect(
          find.descendant(
            of: result,
            matching: find.text(
              chinese ? '实际执行时确定' : 'Determined at execution',
            ),
          ),
          findsOneWidget,
        );
        expect(
          find.descendant(
            of: result,
            matching: find.text('Direct · System DNS'),
          ),
          findsNothing,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }
}

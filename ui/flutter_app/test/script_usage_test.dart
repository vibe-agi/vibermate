import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/code_library_view.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

CodeLibraryTransformRevision _script(String id, int revision) =>
    CodeLibraryTransformRevision(
      id: id,
      revision: revision,
      collectionId: 'default',
      displayName: id,
      policy: const TrafficTransformPolicy(
        requestJavaScript: '',
        responseJavaScript: '',
      ),
      publishedAt: DateTime.utc(2026, 11, 13),
    );

/// The same traffic policy with [scripts] on every client flow.
EnvironmentRecord _running(
  EnvironmentRecord environment,
  List<CodeLibraryTransformRevision> scripts, {
  String? state,
}) => environment.copyWith(
  state: state,
  clientEndpoints: [
    for (final endpoint in environment.clientEndpoints)
      endpoint.copyWith(
        protocolPlans: [
          for (final plan in endpoint.protocolPlans)
            plan.copyWith(transforms: scripts),
        ],
      ),
  ],
);

int _flows(EnvironmentRecord environment) => environment.clientEndpoints
    .expand((endpoint) => endpoint.protocolPlans)
    .length;

void main() {
  test('a script lists the traffic policies that run it', () async {
    final api = PreviewControlApi();
    addTearDown(api.close);
    final environments = (await api.loadDashboard()).environments
        .where((environment) => _flows(environment) > 0)
        .toList();
    expect(environments.length, greaterThanOrEqualTo(2));
    final active = _running(environments[0], [
      _script('mask', 3),
    ], state: 'active');
    final disabled = _running(environments[1], [
      _script('mask', 2),
      _script('other', 1),
    ], state: 'disabled');

    final usage = scriptUsage([disabled, active], 'mask');
    expect(usage.map((value) => value.environmentId), [active.id, disabled.id]);
    expect(usage.first.flows, _flows(active));
    expect(usage.first.revisions, {3});
    expect(usage.last.active, isFalse);
    expect(usage.last.revisions, {2});

    expect(
      scriptUsage([disabled, active], 'other').single.environmentId,
      disabled.id,
    );
    expect(scriptUsage([disabled, active], 'unused'), isEmpty);
  });

  testWidgets('the script detail names the traffic policies that run it', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1280, 800));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final api = PreviewControlApi();
    await api.createCodeLibraryCollection(
      id: 'privacy',
      displayName: 'Privacy',
    );
    final published = await api.publishCodeLibraryTransform(
      id: 'transform.mask',
      expectedRevision: 0,
      collectionId: 'privacy',
      displayName: 'Mask secrets',
      policy: const TrafficTransformPolicy(
        requestJavaScript: 'request.setJson(request.json())',
        responseJavaScript: '',
      ),
    );
    final controller = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: api.close,
    );
    addTearDown(api.close);
    await controller.initialize();
    // One traffic policy runs revision 1 of the script.
    final data = controller.data!;
    final running = data.environments
        .where((environment) => _flows(environment) > 0)
        .first;
    controller.data = DashboardData(
      status: data.status,
      captures: data.captures,
      captureNextCursor: data.captureNextCursor,
      environments: [
        for (final environment in data.environments)
          environment.id == running.id
              ? _running(environment, [published], state: 'active')
              : environment,
      ],
      endpoints: data.endpoints,
      accounts: data.accounts,
    );

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: CodeLibraryView(
            controller: controller,
            copy: AppCopy.forLanguage(AppLanguage.english),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final usedBy = find.byKey(const Key('code-library-used-by'));
    expect(usedBy, findsOneWidget);
    final text = tester
        .widget<Text>(find.descendant(of: usedBy, matching: find.byType(Text)))
        .textSpan!
        .toPlainText();
    expect(text, contains('Used by'));
    expect(text, contains(running.name));
    expect(text, contains('client flows'));
    expect(tester.takeException(), isNull);
    // Stop the controller's polling before the test ends.
    await tester.pumpWidget(const SizedBox());
    controller.dispose();
  });
}

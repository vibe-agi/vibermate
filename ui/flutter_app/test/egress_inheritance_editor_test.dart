import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/egress_profile_editor.dart';
import 'package:vibermate_app/features/workbench/environments_view.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

void main() {
  testWidgets(
    'Inherited exit label remains fully visible at390px and200percent',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 1000));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = PreviewControlApi();
      addTearDown(api.close);
      final plan = (await api.loadDashboard()).environments
          .firstWhere((e) => !e.systemOwned)
          .clientEndpoints
          .first
          .protocolPlans
          .first
          .copyWith(egressProfile: EgressChoice.inherit);
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          builder: (context, child) => MediaQuery(
            data: MediaQuery.of(
              context,
            ).copyWith(textScaler: const TextScaler.linear(2)),
            child: child!,
          ),
          home: Scaffold(
            body: EgressProfileButton(
              selectionScope: api,
              plan: plan,
              copy: AppCopy.forLanguage(AppLanguage.english),
              enabled: true,
              loadProfiles: api.egressProfiles,
              onChanged: (_) {},
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final label = find.text('Use global default');
      final paragraph = tester.renderObject<RenderParagraph>(label);
      expect(paragraph.didExceedMaxLines, isFalse);
      await tester.tap(find.byKey(Key('environment-egress-${plan.id}')));
      await tester.pumpAndSettle();
      final option = find.descendant(
        of: find.byKey(const Key('account-egress-inherit')),
        matching: find.text('Use global default'),
      );
      expect(
        tester.renderObject<RenderParagraph>(option).didExceedMaxLines,
        isFalse,
      );
      expect(tester.takeException(), isNull);
    },
  );
  for (final creating in [false, true]) {
    testWidgets(
      'Actual ${creating ? 'new' : 'existing'} Environment editor saves authored Choice not default preview',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 1000));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi();
        addTearDown(api.close);
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        await controller.refresh();
        controller.selectEnvironment('work');
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: AnimatedBuilder(
                animation: controller,
                builder: (context, _) => EnvironmentsView(
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.english),
                ),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        Future<void> tap(Finder finder) async {
          await tester.ensureVisible(finder);
          await tester.pumpAndSettle();
          await tester.tap(finder);
          await tester.pumpAndSettle();
        }

        await tap(
          find.byKey(Key(creating ? 'environment-create' : 'environment-edit')),
        );
        if (creating) {
          await tester.enterText(
            find.byKey(const Key('environment-create-name')),
            'Inherited exit',
          );
          await tap(find.byKey(const Key('environment-client-plan-target')));
          await tap(find.text('https://chatgpt.com').last);
          await tap(find.byKey(const Key('environment-use-original')));
        }
        final button = find.byType(EgressProfileButton).first;
        final before = tester.widget<EgressProfileButton>(button).plan;
        if (creating) {
          expect(before.egressProfile, EgressChoice.inherit);
        }
        await tap(find.byKey(Key('environment-egress-${before.id}')));
        await tap(find.byKey(const Key('account-egress-inherit')));
        await tap(find.byKey(const Key('environment-egress-profile-save')));
        final edited = tester.widget<EgressProfileButton>(button).plan;
        expect(edited.egressProfile, EgressChoice.inherit);
        await tap(
          find.byKey(
            Key(creating ? 'environment-create-review' : 'environment-review'),
          ),
        );
        final draft = controller.reviewedEnvironmentDraft;
        expect(draft, isNotNull);
        final plan = draft!.candidate.clientEndpoints
            .expand((endpoint) => endpoint.protocolPlans)
            .firstWhere((plan) => plan.id == before.id);
        expect(plan.egressProfile.toJson(), {'mode': 'inherit'});
        expect(draft.candidate.clientToolAccessEnabled, isFalse);
        await tap(
          find.byKey(
            Key(
              creating ? 'environment-create-publish' : 'environment-publish',
            ),
          ),
        );
        final published = controller.data!.environments.firstWhere(
          (e) => e.id == draft.candidate.id,
        );
        expect(
          published.clientEndpoints
              .expand((e) => e.protocolPlans)
              .firstWhere((p) => p.id == before.id)
              .egressProfile,
          EgressChoice.inherit,
        );
        expect(published.clientToolAccessEnabled, isFalse);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }
  for (final available in [true, false]) {
    testWidgets(
      'Flow inheritance preview is information and preserves explicit selection available=$available',
      (tester) async {
        final api = PreviewControlApi();
        addTearDown(api.close);
        final environment = (await api.loadDashboard()).environments.firstWhere(
          (e) => !e.systemOwned,
        );
        final plan = environment.clientEndpoints.first.protocolPlans.first
            .copyWith(egressProfile: EgressProfileRevision.direct);
        EgressChoice? saved;
        var reads = 0;
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.light(),
            home: Scaffold(
              body: EgressProfileButton(
                selectionScope: api,
                plan: plan,
                copy: AppCopy.forLanguage(AppLanguage.english),
                enabled: true,
                loadProfiles: api.egressProfiles,
                loadDefault: () async {
                  reads++;
                  if (!available) {
                    throw StateError('default unavailable');
                  }
                  return api.egressDefault();
                },
                onChanged: (choice) => saved = choice,
              ),
            ),
          ),
        );
        await tester.tap(find.byKey(Key('environment-egress-${plan.id}')));
        await tester.pumpAndSettle();
        expect(reads, 1);
        expect(find.text('Use global default'), findsOneWidget);
        expect(
          find.byKey(
            Key(
              available
                  ? 'egress-selection-default-preview'
                  : 'egress-selection-default-unavailable',
            ),
          ),
          findsOneWidget,
        );
        await tester.tap(
          find.byKey(const Key('environment-egress-profile-save')),
        );
        await tester.pumpAndSettle();
        expect(saved, EgressProfileRevision.direct);
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets(
    'Inherited flow opens and saves authored inheritance without selecting Direct',
    (tester) async {
      final api = PreviewControlApi();
      addTearDown(api.close);
      final environment = (await api.loadDashboard()).environments.firstWhere(
        (value) => !value.systemOwned,
      );
      final plan = environment.clientEndpoints.first.protocolPlans.first
          .copyWith(egressProfile: EgressChoice.inherit);
      EgressChoice? saved;
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.light(),
          home: Scaffold(
            body: EgressProfileButton(
              selectionScope: api,
              plan: plan,
              copy: AppCopy.forLanguage(AppLanguage.english),
              enabled: true,
              loadProfiles: api.egressProfiles,
              onChanged: (choice) => saved = choice,
            ),
          ),
        ),
      );
      await tester.tap(find.byKey(Key('environment-egress-${plan.id}')));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      expect(
        find.byKey(const Key('environment-egress-profile-save')),
        findsOneWidget,
      );
      await tester.tap(
        find.byKey(const Key('environment-egress-profile-save')),
      );
      await tester.pumpAndSettle();
      expect(saved, EgressChoice.inherit);
      expect(saved!.toJson(), {'mode': 'inherit'});
    },
  );
}

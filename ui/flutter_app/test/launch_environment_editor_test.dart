import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/launch_environment_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  testWidgets('390px launch overlay edits exact set and delete variables', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
    EnvironmentLaunchPolicy? changed;
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: LaunchEnvironmentEditorButton(
            policy: const EnvironmentLaunchPolicy.empty(),
            copy: copy,
            enabled: true,
            onChanged: (value) => changed = value,
          ),
        ),
      ),
    );

    await tester.tap(find.byKey(const Key('environment-launch-edit')));
    await tester.pumpAndSettle();
    final dialog = find.byKey(const Key('environment-launch-dialog'));
    expect(tester.getSize(dialog).width, lessThanOrEqualTo(342));
    expect(find.textContaining('路由、代理、信任、凭据'), findsOneWidget);

    await tester.tap(find.byKey(const Key('environment-launch-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-name-0')),
      'TEAM_CONTEXT',
    );
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-value-0')),
      'research',
    );
    await tester.tap(find.byKey(const Key('environment-launch-add-delete')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-launch-delete-name-0')),
      'OLD_CONTEXT',
    );
    await tester.tap(find.byKey(const Key('environment-launch-save')));
    await tester.pumpAndSettle();

    expect(changed?.setEnv, {'TEAM_CONTEXT': 'research'});
    expect(changed?.deleteEnv, ['OLD_CONTEXT']);
    expect(dialog, findsNothing);
    expect(find.text('设置 1 · 删除 1'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('reserved launcher authority cannot be saved', (tester) async {
    final copy = AppCopy.forLanguage(AppLanguage.english);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: LaunchEnvironmentEditorButton(
            policy: const EnvironmentLaunchPolicy.empty(),
            copy: copy,
            enabled: true,
            onChanged: (_) {},
          ),
        ),
      ),
    );
    await tester.tap(find.byKey(const Key('environment-launch-edit')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('environment-launch-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-name-0')),
      'OPENAI_API_KEY',
    );
    await tester.enterText(
      find.byKey(const Key('environment-launch-set-value-0')),
      'forbidden',
    );
    await tester.tap(find.byKey(const Key('environment-launch-save')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('environment-launch-dialog')), findsOneWidget);
    expect(find.byKey(const Key('environment-launch-error')), findsOneWidget);
  });
}

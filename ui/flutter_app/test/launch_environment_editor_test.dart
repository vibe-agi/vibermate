import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/launch_environment_snapshot.dart';
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
    expect(find.textContaining('暂无启动快照'), findsOneWidget);

    await tester.tap(find.byKey(const Key('environment-launch-tab-1')));
    await tester.pumpAndSettle();

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
    await tester.tap(find.byKey(const Key('environment-launch-tab-0')));
    await tester.pumpAndSettle();
    await tester.ensureVisible(
      find.byKey(const Key('environment-launch-delete-name-0')),
    );
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
    await tester.tap(find.byKey(const Key('environment-launch-tab-1')));
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

  testWidgets(
    'name snapshots suggest only; rules survive refresh and source change',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1200, 960));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      var calls = 0;
      EnvironmentLaunchPolicy? changed;
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: LaunchEnvironmentEditorButton(
              policy: const EnvironmentLaunchPolicy(
                setEnv: {},
                deleteEnv: ['OLD_SECRET'],
              ),
              copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
              enabled: true,
              onChanged: (value) => changed = value,
              loadSnapshots: () async {
                calls++;
                return [
                  LaunchEnvironmentSnapshot(
                    id: 'run-$calls',
                    deviceName: 'workstation',
                    userLabel: 'alice',
                    executable: 'codex',
                    remote: true,
                    collectedAt: DateTime.utc(2026, 9, 22),
                    names: calls == 1
                        ? ['GH_TOKEN', 'LANG', 'OPENAI_API_KEY', 'PATH']
                        : ['NEW_VARIABLE'],
                  ),
                ];
              },
            ),
          ),
        ),
      );
      await tester.tap(find.byKey(const Key('environment-launch-edit')));
      await tester.pumpAndSettle();
      expect(calls, 1);
      expect(
        tester
            .widget<Checkbox>(find.byKey(const Key('launch-block-GH_TOKEN')))
            .value,
        false,
      );
      expect(
        find.byKey(const Key('launch-block-OPENAI_API_KEY')),
        findsNothing,
      );
      expect(find.textContaining('值留在启动终端'), findsOneWidget);
      await tester.tap(find.byKey(const Key('launch-select-sensitive')));
      await tester.pumpAndSettle();
      expect(
        tester
            .widget<Checkbox>(find.byKey(const Key('launch-block-GH_TOKEN')))
            .value,
        true,
      );
      await tester.tap(find.byKey(const Key('launch-snapshots-refresh')));
      await tester.pumpAndSettle();
      expect(calls, 2);
      expect(
        tester
            .widget<Checkbox>(find.byKey(const Key('launch-block-GH_TOKEN')))
            .value,
        true,
      );
      expect(
        tester
            .widget<Checkbox>(find.byKey(const Key('launch-block-OLD_SECRET')))
            .value,
        true,
      );
      await tester.tap(find.byKey(const Key('environment-launch-save')));
      await tester.pumpAndSettle();
      expect(changed?.deleteEnv, ['GH_TOKEN', 'OLD_SECRET']);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'snapshot failure permits manual rules and refuses runtime-managed names',
    (tester) async {
      await tester.pumpWidget(
        MaterialApp(
          theme: ViberTheme.dark(),
          home: Scaffold(
            body: LaunchEnvironmentEditorButton(
              policy: const EnvironmentLaunchPolicy.empty(),
              copy: AppCopy.forLanguage(AppLanguage.english),
              enabled: true,
              onChanged: (_) {},
              loadSnapshots: () async => throw StateError('offline'),
            ),
          ),
        ),
      );
      await tester.tap(find.byKey(const Key('environment-launch-edit')));
      await tester.pumpAndSettle();
      expect(find.textContaining('Could not load snapshots'), findsOneWidget);
      final input = find.byKey(const Key('environment-launch-delete-name-0'));
      await tester.ensureVisible(input);
      await tester.enterText(input, 'HTTPS_PROXY');
      await tester.tap(find.byKey(const Key('environment-launch-add-delete')));
      await tester.pumpAndSettle();
      expect(
        find.textContaining('This variable is managed by ViberMate'),
        findsOneWidget,
      );
      expect(find.byKey(const Key('launch-block-HTTPS_PROXY')), findsNothing);
      expect(tester.takeException(), isNull);
    },
  );
}

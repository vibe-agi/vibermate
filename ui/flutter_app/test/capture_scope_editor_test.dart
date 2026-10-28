import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/capture_scope_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  // The editor is reached from a phone browser too, so it must fit.
  for (final width in [390.0, 1200.0]) {
    testWidgets('capture scope editor adds, keeps and clears rules at $width', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(Size(width, 760));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copy = AppCopy.forLanguage(AppLanguage.english);
      EnvironmentCaptureScope? changed;
      var calls = 0;
      Future<void> pumpEditor(EnvironmentCaptureScope? scope) =>
          tester.pumpWidget(
            MaterialApp(
              theme: ViberTheme.light(),
              home: Scaffold(
                body: CaptureScopeEditorButton(
                  // Each scenario starts from a fresh editor.
                  key: UniqueKey(),
                  scope: scope,
                  copy: copy,
                  enabled: true,
                  onChanged: (value) {
                    calls++;
                    changed = value;
                  },
                ),
              ),
            ),
          );
      Future<void> open() async {
        await tester.tap(
          find.byKey(const Key('environment-capture-scope-edit')),
        );
        await tester.pumpAndSettle();
      }

      Future<void> save() async {
        await tester.tap(
          find.byKey(const Key('environment-capture-scope-apply')),
        );
        await tester.pumpAndSettle();
      }

      // A new rule gets a fresh identifier; values are stored in lower case.
      await pumpEditor(null);
      await open();
      expect(
        find.byKey(const Key('environment-capture-scope-empty')),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const Key('environment-capture-scope-add')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-capture-scope-value-0')),
        'Corp.Example',
      );
      await tester.enterText(
        find.byKey(const Key('environment-capture-scope-port-0')),
        '443',
      );
      await save();
      expect(calls, 1);
      expect(changed!.rules.single.toJson(), {
        'id': 'rule-1',
        'kind': 'suffix',
        'value': 'corp.example',
        'port': 443,
      });
      expect(changed!.allowUserAdditions, isFalse);

      // An invalid port blocks saving.
      await open();
      await tester.enterText(
        find.byKey(const Key('environment-capture-scope-port-0')),
        '70000',
      );
      await tester.tap(
        find.byKey(const Key('environment-capture-scope-apply')),
      );
      await tester.pumpAndSettle();
      expect(calls, 1);
      expect(find.text('1–65535'), findsOneWidget);
      await tester.tap(find.byTooltip(copy('common.dismiss')).last);
      await tester.pumpAndSettle();
      expect(calls, 1, reason: 'cancelling must not change the scope');

      // Editing an existing scope keeps its identifiers.
      const existing = EnvironmentCaptureScope(
        rules: [
          EnvironmentCaptureScopeRule(
            id: 'lan',
            kind: 'prefix',
            value: '192.168.0.0/16',
          ),
        ],
      );
      await pumpEditor(existing);
      await open();
      await tester.tap(find.byKey(const Key('environment-capture-scope-add')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('environment-capture-scope-value-1')),
        'git.corp.example',
      );
      await save();
      expect(changed!.rules.map((rule) => rule.id), ['lan', 'rule-1']);

      // Removing every rule, with user additions off, clears the scope.
      await pumpEditor(existing);
      await open();
      await tester.tap(
        find.byKey(const Key('environment-capture-scope-remove-0')),
      );
      await tester.pumpAndSettle();
      await save();
      expect(changed, isNull);
      expect(tester.takeException(), isNull);
    });
  }
}

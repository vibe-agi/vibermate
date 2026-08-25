import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/account_header_policy_editor.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  testWidgets('390px Account Header editor builds exact set and delete rules', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 760));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final draft = AccountHeaderPolicyDraft(
      existingSetHeaderNames: const ['X-Previous-Secret'],
      initialDeleteHeaderNames: const ['X-Legacy'],
    );
    final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);

    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Scaffold(
          body: SizedBox(
            width: 390,
            child: SingleChildScrollView(
              child: AccountHeaderPolicyEditor(
                accountKind: 'bearer_token',
                draft: draft,
                copy: copy,
                enabled: true,
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('X-Previous-Secret'), findsOneWidget);
    expect(find.textContaining('Authorization: Bearer'), findsOneWidget);
    await tester.tap(find.byKey(const Key('account-header-add-set')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('account-header-set-name-0')),
      'X-Team',
    );
    await tester.enterText(
      find.byKey(const Key('account-header-set-value-0')),
      'team-a',
    );

    final policy = draft.build(accountKind: 'bearer_token');
    expect(policy.setHeaders, {'X-Team': 'team-a'});
    expect(policy.deleteHeaders, ['X-Legacy']);
    expect(tester.takeException(), isNull);
  });

  test(
    'Account Header draft refuses the driver-owned authentication Header',
    () {
      final draft = AccountHeaderPolicyDraft();
      draft.addSet(name: 'Authorization', value: 'forged');

      expect(
        () => draft.build(accountKind: 'bearer_token'),
        throwsA(isA<ControlContractException>()),
      );
    },
  );
}

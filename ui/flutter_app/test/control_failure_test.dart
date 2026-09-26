import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_failure.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/control_failure_notice.dart';
import 'package:vibermate_app/features/workbench/deletion_dialog.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  const reasons = {
    'environment_upstream_stale': 'error.environment_upstream_stale',
    'provider_account_scope_mismatch': 'error.account_scope_mismatch',
    'provider_account_conflict': 'error.account_conflict',
    'provider_account_in_use': 'error.account_in_use',
    'provider_account_busy': 'error.account_busy',
    'provider_account_disabled': 'error.account_disabled',
    'provider_account_credential_unavailable':
        'error.account_credential_unavailable',
    'provider_account_not_found': 'error.account_not_found',
    'upstream_endpoint_not_found': 'error.upstream_not_found',
    'revision_conflict': 'error.configuration_conflict',
    'environment_preview_stale': 'error.policy_review_stale',
    'invalid_control_request': 'error.configuration_invalid',
    'credential_reconnect_required': 'provider_accounts.refresh.reconnect',
    'credential_refresh_failed': 'provider_accounts.refresh.failed',
    'invalid_runtime_user_policy': 'error.runtime_user_policy_invalid',
    'runtime_user_policy_unavailable':
        'error.runtime_user_policy_unavailable',
  };
  for (final entry in reasons.entries) {
    test(
      '${entry.key} has human-readable bilingual copy and safe diagnostics',
      () {
        final failure = ControlFailure.from(
          ControlProblem(
            status: 422,
            reasonCode: entry.key,
            messageKey: 'untrusted-private-secret',
          ),
        );
        expect(failure.messageKey, entry.value);
        expect(failure.diagnostic, '${entry.key} · HTTP 422');
        for (final language in [
          AppLanguage.english,
          AppLanguage.simplifiedChinese,
        ]) {
          final text = AppCopy.forLanguage(language)(failure.messageKey);
          expect(text, isNotEmpty);
          expect(text, isNot(contains(entry.key)));
          expect(text, isNot(contains('untrusted-private-secret')));
        }
      },
    );
  }
  test(
    'unknown failures never echo credentials or claim that no write occurred',
    () {
      for (final error in <Object>[
        StateError('private-secret'),
        TimeoutException('private-secret'),
        http.ClientException('private-secret'),
        const ControlProblem(
          status: 500,
          reasonCode: 'private-secret@example.com',
          messageKey: 'private-secret',
        ),
      ]) {
        final failure = ControlFailure.from(error);
        expect(failure.messageKey, 'error.control_result_unknown');
        expect(failure.diagnostic, isNot(contains('private-secret')));
      }
      final failure = ControlFailure.from(
        const ControlContractException('private-secret'),
      );
      expect(failure.messageKey, 'error.control_contract');
      expect(failure.diagnostic, 'control_contract_mismatch');
    },
  );
  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    testWidgets(
      'diagnostics are expandable, not the primary error ($language)',
      (tester) async {
        final copy = AppCopy.forLanguage(language);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: ControlFailureNotice(
                copy: copy,
                message: 'error.account_scope_mismatch',
                diagnostic: 'provider_account_scope_mismatch · HTTP 422',
              ),
            ),
          ),
        );
        expect(find.text(copy('error.account_scope_mismatch')), findsOneWidget);
        expect(
          find.text('provider_account_scope_mismatch · HTTP 422'),
          findsNothing,
        );
        await tester.tap(find.text(copy('error.diagnostic_details')));
        await tester.pumpAndSettle();
        expect(
          find.text('provider_account_scope_mismatch · HTTP 422'),
          findsOneWidget,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets('delete failures use the same safe, actionable presentation', (
    tester,
  ) async {
    final copy = AppCopy.forLanguage(AppLanguage.simplifiedChinese);
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.dark(),
        home: DeletionConfirmation(
          title: '删除上游服务',
          subject: 'Fixture',
          consequence: '删除此配置。',
          copy: copy,
          onConfirm: () async => throw const ControlFailure(
            'error.configuration_conflict',
            'revision_conflict · HTTP 409',
          ),
        ),
      ),
    );
    await tester.tap(find.byKey(const Key('deletion-confirm')));
    await tester.pumpAndSettle();
    expect(find.text(copy('error.configuration_conflict')), findsOneWidget);
    expect(find.textContaining('Bad state'), findsNothing);
    expect(tester.takeException(), isNull);
  });
}

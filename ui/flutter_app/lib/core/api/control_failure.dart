import 'dart:async';

import 'package:http/http.dart' as http;

import 'control_api.dart';
import 'control_models.dart';

/// Human-facing control failures never echo request bodies, credentials or
/// arbitrary exception text. The safe protocol code remains available for diagnosis.
final class ControlFailure implements Exception {
  const ControlFailure(this.messageKey, [this.diagnostic]);

  factory ControlFailure.from(Object error) {
    if (error is ControlFailure) return error;
    if (error is ControlProblem) {
      final key = switch (error.reasonCode) {
        'environment_upstream_stale' => 'error.environment_upstream_stale',
        'provider_account_scope_mismatch' => 'error.account_scope_mismatch',
        'provider_account_in_use' => 'error.account_in_use',
        'provider_account_busy' => 'error.account_busy',
        'provider_account_conflict' => 'error.account_conflict',
        'provider_account_disabled' => 'error.account_disabled',
        'provider_account_credential_unavailable' =>
          'error.account_credential_unavailable',
        'provider_account_not_found' => 'error.account_not_found',
        'upstream_endpoint_not_found' => 'error.upstream_not_found',
        'upstream_endpoint_conflict' ||
        'revision_conflict' => 'error.configuration_conflict',
        'environment_preview_stale' ||
        'environment_draft_not_found' => 'error.policy_review_stale',
        'dry_run_flow_not_matched' => 'error.dry_run_flow_not_matched',
        'dry_run_input_invalid' => 'error.dry_run_input_invalid',
        'dry_run_environment_disabled' => 'error.dry_run_environment_disabled',
        'dry_run_selector_failed' => 'error.dry_run_selector_failed',
        'dry_run_transform_failed' => 'error.dry_run_transform_failed',
        'invalid_control_request' => 'error.configuration_invalid',
        'invalid_runtime_user_policy' => 'error.runtime_user_policy_invalid',
        'runtime_user_policy_unavailable' =>
          'error.runtime_user_policy_unavailable',
        'credential_reconnect_required' =>
          'provider_accounts.refresh.reconnect',
        'credential_refresh_failed' => 'provider_accounts.refresh.failed',
        'control_unauthorized' ||
        'server_admin_session_expired' => 'error.control_session_expired',
        _ => 'error.control_result_unknown',
      };
      // Do not trust a server-supplied detail string as display-safe.
      final safeCode =
          RegExp(r'^[a-z][a-z0-9_]{0,127}$').hasMatch(error.reasonCode)
          ? error.reasonCode
          : 'control_error';
      return ControlFailure(key, '$safeCode · HTTP ${error.status}');
    }
    if (error is ControlContractException) {
      return const ControlFailure(
        'error.control_contract',
        'control_contract_mismatch',
      );
    }
    if (error is TimeoutException || error is http.ClientException) {
      return const ControlFailure(
        'error.control_result_unknown',
        'control_connection_failed',
      );
    }
    return const ControlFailure(
      'error.control_result_unknown',
      'control_operation_failed',
    );
  }

  final String messageKey;
  final String? diagnostic;
}

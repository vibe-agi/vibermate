package runtimepersistence

// Explicit reviewed control projection. Never infer authority from source columns.
type controlProjection struct{ table, columns, key string }

var controlProjections = []controlProjection{
	{"capture_environment_assignments", "capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,source,launch_environment_id,launch_environment_revision,launch_environment_digest,protected_authorities_json,managed_authorities_json,launch_authority_digest,updated_at_unix_ms,client_target_origin,client_target_canonical_origin", "capture_kind,capture_id"},
	{"capture_runs", "run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,adapter_id,adapter_revision,adapter_version,adapter_install_shape,adapter_release_sha256,adapter_launch_recipe,adapter_features,process_id,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,observation,first_observed_at_unix_ms,recognition,local_user_label,home_directory,operating_system,operating_system_version,architecture,time_zone,git_json,machine_id,machine_registration_revision,workspace_id,workspace_label,workspace_evidence,workspace_derivation_revision,runtime_user_id,runtime_username,login_session_id,device_name", "run_id"},
	{"client_enrollments", "enrollment_id,binding_id,binding_revision,state,credential_digest,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,consumed_at_unix_ms,machine_registration_id", "enrollment_id"},
	{"code_library_account_selector_heads", "selector_id,current_revision", "selector_id"},
	{"code_library_account_selector_revisions", "selector_id,revision,collection_id,display_name,javascript,published_at_unix_ms", "selector_id,revision"},
	{"code_library_collections", "collection_id,display_name", "collection_id"},
	{"code_library_transform_heads", "transform_id,current_revision", "transform_id"},
	{"code_library_transform_revisions", "transform_id,revision,collection_id,display_name,request_javascript,response_javascript,published_at_unix_ms", "transform_id,revision"},
	{"connection_rule_sets", "id,revision,mode,updated_at_unix_ms", "id"},
	{"connection_rules", "rule_id,priority,decision,match_kind,match_host,match_port", "rule_id"},
	{"egress_profile_heads", "egress_id,current_revision", "egress_id"},
	{"egress_profile_revisions", "egress_id,revision,display_name,policy_json,published_at_unix_ms", "egress_id,revision"},
	{"enrolled_control_principals", "principal_id,binding_id,binding_revision,machine_registration_id,credential_revision,credential_digest,allowed_grant_kinds,state,created_at_unix_ms,updated_at_unix_ms", "principal_id"},
	{"environment_drafts", "environment_id,base_revision,draft_revision,candidate_revision,format_version,payload_json,candidate_digest,updated_at_unix_ms", "environment_id"},
	{"environment_revision_counters", "environment_id,active_revision,draft_revision", "environment_id"},
	{"environment_revisions", "environment_id,revision,name,state,format_version,payload_json,candidate_digest,published_at_unix_ms", "environment_id,revision"},
	{"machine_registrations", "machine_registration_id,machine_id,binding_id,binding_revision,revision,state,display_name,created_at_unix_ms,updated_at_unix_ms", "machine_registration_id"},
	{"manual_captures", "capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms,expires_at_unix_ms,last_observed_at_unix_ms", "capture_id"},
	{"provider_accounts", "account_id,display_name,credential_origin,endpoint_associations,association_revision,note,note_revision,settings_revision,egress_profile_json,automatic_refresh,realm_id,driver_ref,secret_reference,state,revision,created_at_unix_ms,updated_at_unix_ms", "account_id"},
	{"proxy_client_bindings", "binding_id,revision,state,display_name,allowed_ingress_scopes_json,allowed_environment_ids_json,quota_policy_id,allowed_grant_kinds,created_at_unix_ms,updated_at_unix_ms", "binding_id"},
	{"runtime_egress_default", "singleton,revision,egress_id,egress_revision", "singleton"},
	{"runtime_usage_policy", "singleton,enabled,retention_days,revision,collecting_since_unix_ms", "singleton"},
	{"runtime_user_login_sessions", "session_id,user_id,token_digest,machine_id,device_name,created_at_unix_ms,expires_at_unix_ms,revoked_at_unix_ms", "session_id"},
	{"runtime_user_policies", "user_id,all_environments,allowed_environment_ids_json,daily_agent_api_call_warning,daily_token_warning", "user_id"},
	{"runtime_users", "user_id,username,password_hash,state,created_at_unix_ms,updated_at_unix_ms", "user_id"},
	{"tool_approvals", "approval_id,revision,kind,exchange_id,environment_id,environment_revision,environment_digest,route_id,route_revision,subject_refs_json,subject_labels_json,target_host,target_port,aggregate_key,request_count,waiter_count,state,decision,decision_scope,decision_reason,decision_idempotency_key,created_at_unix_ms,expires_at_unix_ms,resolved_at_unix_ms", "approval_id"},
	{"upstream_endpoints", "endpoint_id,display_name,origin,realm_id,backend_protocols_json,capabilities_json,drivers_json,state,revision,created_at_unix_ms,updated_at_unix_ms", "endpoint_id"},
}

var nativeControlProjections = []controlProjection{
	{"native_tool_scopes", "capture_kind,capture_id,revision,owner_epoch,state,replay_barrier,expires_at_unix_ms", "capture_kind,capture_id"},
	{"native_tool_sessions", "session_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,route_id,route_revision,target_origin,protocol_version,revision,owner_epoch,state,catalog_generation,catalog_digest,expires_at_unix_ms", "session_id"},
	{"native_tool_operations", "operation_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,recording_archive_epoch,recording_capture_generation,last_event_kind,last_event_at_unix_ms,maximum_event_at_unix_ms,metadata_expires_at_unix_ms,completed_at_unix_ms,bytes_in,bytes_out,terminal_reason", "operation_id"},
	{"native_tool_bindings", "operation_id,session_id,session_revision,catalog_generation,catalog_digest,route_id,route_revision,owner_epoch,assignment_revision,request_digest,params_digest,group_digest,binding_digest,replay_key,parent_operation_id,server_request_digest,tool_name,cancel_rpc_id_digest", "operation_id"},
	{"native_tool_binding_egress", "operation_id,binding_digest,binding_version,profile_json,selection_source,default_revision", "operation_id"},
	{"native_tool_inbound", "session_id,rpc_id_digest,request_kind,request_digest,operation_id", "session_id,rpc_id_digest,request_kind"},
	{"native_tool_approvals", "approval_id,operation_id,binding_digest,revision,state,created_at_unix_ms,expires_at_unix_ms,resolved_at_unix_ms,idempotency_key,consumed_at_unix_ms", "approval_id"},
	{"native_tool_once", "capture_kind,capture_id,replay_key,operation_id,intent_digest,attempt_id,claimed_at_unix_ms,authority_revision", "capture_kind,capture_id,replay_key"},
}

func controlExtractionProjections() []controlProjection {
	projections := append([]controlProjection(nil), controlProjections...)
	projections = append(projections, nativeControlProjections...)
	return append(projections,
		controlProjection{"native_tool_deliveries", "attempt_id,operation_id,connection_id,payload_class,target_origin,policy_id,policy_revision,rule_id,proxy_id,proxy_revision,selection_source,default_revision,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in,byte_counts_known", "attempt_id"},
		controlProjection{"acp_registrations", "run_id,recording,retention_days,expires_at_unix_ms,archive_epoch,capture_generation", "run_id"},
		controlProjection{"runtime_metadata", "singleton,schema_identity,schema_revision,initialized_at", "singleton"},
		controlProjection{"recording_denial_coverage", "singleton,identity_count,chain_hash", "singleton"},
		controlProjection{"recording_cleanup_state", "singleton,requested_revision,completed_revision,source_generation,cutoff_unix_ms,active_revision,active_cutoff_unix_ms", "singleton"},
		controlProjection{"runtime_storage_state", "singleton,layout_id,archive_epoch,visibility_revision", "singleton"},
		controlProjection{"runtime_usage_legacy_retention_caps", "archive_id,retention_days,after_sequence,through_sequence", "archive_id,retention_days"},
		controlProjection{"recording_capture_fences", "capture_kind,capture_id,generation,deleted", "capture_kind,capture_id"},
		controlProjection{"runtime_usage_retention_rules", "retention_days,through_policy_revision", "retention_days"},
	)
}

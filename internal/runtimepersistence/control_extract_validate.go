package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
)

// All checks run against the transformed private target, with ordinary CHECKs
// enabled. Source frozen CHECKs and required FK closure were checked separately.
// No validator uses current time, mutates lifecycle, or consults optional data.
func validateExtractedControl(ctx context.Context, tx *sql.Tx) error {
	scans := []struct {
		query string
		check func(*sql.Rows) error
	}{
		{`SELECT ` + captureRunColumns + ` FROM capture_runs`, func(r *sql.Rows) error { _, err := scanCaptureRun(r); return err }},
		{`SELECT ` + manualCaptureColumns + ` FROM manual_captures`, func(r *sql.Rows) error { _, err := scanManualCapture(r); return err }},
		{`SELECT ` + providerAccountColumns + ` FROM provider_accounts`, func(r *sql.Rows) error { _, err := scanProviderAccount(r); return err }},
		{`SELECT ` + proxyClientBindingColumns + ` FROM proxy_client_bindings`, func(r *sql.Rows) error { _, err := scanProxyClientBinding(r); return err }},
		{`SELECT ` + clientEnrollmentColumns + ` FROM client_enrollments`, func(r *sql.Rows) error { _, err := scanClientEnrollment(r); return err }},
		{`SELECT ` + runtimeUserProjection, func(r *sql.Rows) error { _, err := scanRuntimeUser(r); return err }},
		{`SELECT egress_id,revision,display_name,policy_json,published_at_unix_ms FROM egress_profile_revisions`, func(r *sql.Rows) error { _, err := scanEgressProfileRevision(r); return err }},
		{`SELECT endpoint_id,display_name,origin,realm_id,backend_protocols_json,capabilities_json,drivers_json,state,revision,created_at_unix_ms,updated_at_unix_ms FROM upstream_endpoints`, func(r *sql.Rows) error { _, err := scanUpstreamEndpoint(r); return err }},
		{`SELECT transform_id,revision,collection_id,display_name,request_javascript,response_javascript,published_at_unix_ms FROM code_library_transform_revisions`, func(r *sql.Rows) error { _, err := scanCodeLibraryTransformRevision(r); return err }},
		{`SELECT selector_id,revision,collection_id,display_name,javascript,published_at_unix_ms FROM code_library_account_selector_revisions`, func(r *sql.Rows) error { _, err := scanCodeLibraryAccountSelectorRevision(r); return err }},
		{`SELECT ` + toolApprovalColumns + ` FROM tool_approvals`, func(r *sql.Rows) error { _, err := scanToolApproval(r); return err }},
		{`SELECT capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,source,launch_environment_id,launch_environment_revision,launch_environment_digest,protected_authorities_json,managed_authorities_json,launch_authority_digest,client_target_origin,client_target_canonical_origin,updated_at_unix_ms FROM capture_environment_assignments`, func(r *sql.Rows) error { _, err := scanCaptureAssignment(r); return err }},
		{`SELECT environment_id,revision FROM environment_revisions`, func(r *sql.Rows) error {
			var id string
			var revision int64
			if err := r.Scan(&id, &revision); err != nil {
				return err
			}
			_, exists, err := loadEnvironmentRevision(ctx, tx, environment.EnvironmentID(id), environment.Revision(revision))
			if !exists {
				return errors.Join(ErrUnsupportedSchema, err)
			}
			return err
		}},
		{`SELECT environment_id FROM environment_revision_counters`, func(r *sql.Rows) error {
			var id string
			if err := r.Scan(&id); err != nil {
				return err
			}
			_, _, _, err := loadEnvironmentAggregate(ctx, tx, environment.EnvironmentID(id))
			return err
		}},
		{`SELECT environment_id FROM environment_drafts`, func(r *sql.Rows) error {
			var id string
			if err := r.Scan(&id); err != nil {
				return err
			}
			_, exists, err := loadEnvironmentDraft(ctx, tx, environment.EnvironmentID(id))
			if !exists {
				return errors.Join(ErrUnsupportedSchema, err)
			}
			return err
		}},
		{`SELECT capture_kind,capture_id FROM native_tool_scopes`, func(r *sql.Rows) error {
			var kind, id string
			if err := r.Scan(&kind, &id); err != nil {
				return err
			}
			ref, err := captureidentity.New(captureidentity.Kind(kind), id)
			if err != nil {
				return err
			}
			_, err = loadNativeScopeFacts(ctx, tx, ref, false)
			return err
		}},
		{`SELECT session_id FROM native_tool_sessions`, func(r *sql.Rows) error {
			var id string
			if err := r.Scan(&id); err != nil {
				return err
			}
			session, err := loadNativeSession(ctx, tx, nativetool.ID(id))
			if err != nil {
				return err
			}
			e, exists, err := loadEnvironmentRevision(ctx, tx, session.environment, session.environmentRevision)
			if err != nil || !exists {
				return errors.Join(ErrUnsupportedSchema, err)
			}
			digest, err := environment.Digest(e)
			if err != nil || digest != session.digest {
				return errors.Join(ErrUnsupportedSchema, err)
			}
			return nil
		}},
	}
	scans = append(scans, struct {
		query string
		check func(*sql.Rows) error
	}{
		`SELECT ` + proxyClientAuthenticationColumns + ` FROM enrolled_control_principals principal JOIN machine_registrations machine ON machine.machine_registration_id=principal.machine_registration_id JOIN proxy_client_bindings binding ON binding.binding_id=principal.binding_id`, func(r *sql.Rows) error { _, err := scanProxyClientAuthentication(r); return err }},
		struct {
			query string
			check func(*sql.Rows) error
		}{`SELECT machine_registration_id,machine_id,binding_id,binding_revision,revision,state,display_name,created_at_unix_ms,updated_at_unix_ms FROM machine_registrations`, validateExtractionMachine})
	for _, scan := range scans {
		rows, err := tx.QueryContext(ctx, scan.query)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = scan.check(rows); err != nil {
				rows.Close()
				return fmt.Errorf("required semantic projection: %w", err)
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	for _, table := range []string{"runtime_metadata", "runtime_usage_policy", "runtime_egress_default", "runtime_storage_state"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE singleton=1`).Scan(&count); err != nil || count != 1 {
			return errors.Join(ErrUnsupportedSchema, err)
		}
	}
	var rules int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM connection_rules`).Scan(&rules); err != nil || rules > connectionpolicy.MaxRules {
		return errors.Join(ErrUnsupportedSchema, err)
	}
	_, ruleErr := (&connectionRuleRepository{}).read(ctx, tx)
	if ruleErr != nil && (!errors.Is(ruleErr, connectionpolicy.ErrNoRuleSet) || rules != 0) {
		return ruleErr
	}
	// Heads and mixed logical relationships intentionally have no SQLite FK.
	for _, query := range []string{
		`SELECT 1 FROM egress_profile_heads h WHERE NOT EXISTS(SELECT 1 FROM egress_profile_revisions r WHERE r.egress_id=h.egress_id AND r.revision=h.current_revision)`,
		`SELECT 1 FROM code_library_transform_heads h WHERE NOT EXISTS(SELECT 1 FROM code_library_transform_revisions r WHERE r.transform_id=h.transform_id AND r.revision=h.current_revision)`,
		`SELECT 1 FROM code_library_account_selector_heads h WHERE NOT EXISTS(SELECT 1 FROM code_library_account_selector_revisions r WHERE r.selector_id=h.selector_id AND r.revision=h.current_revision)`,
		`SELECT 1 FROM native_tool_once WHERE claimed_at_unix_ms<=0`,
		`SELECT 1 FROM native_tool_inbound i JOIN native_tool_operations o ON o.operation_id=i.operation_id JOIN native_tool_sessions s ON s.session_id=i.session_id WHERE i.request_kind<>o.rpc_kind OR s.capture_kind<>o.capture_kind OR s.capture_id<>o.capture_id OR s.environment_id<>o.environment_id OR s.environment_revision<>o.environment_revision OR s.environment_digest<>o.environment_digest`,

		`SELECT 1 FROM manual_captures m WHERE m.owner_kind='proxy_client_binding' AND NOT EXISTS(SELECT 1 FROM proxy_client_bindings b WHERE b.binding_id=m.owner_id)`,
	} {
		if err := extractionNoRows(ctx, tx, query); err != nil {
			return err
		}
	}
	return nil
}

func validateExtractionMachine(rows *sql.Rows) error {
	var id, machine, binding, state, label string
	var bindingRevision, revision, created, updated int64
	if err := rows.Scan(&id, &machine, &binding, &bindingRevision, &revision, &state, &label, &created, &updated); err != nil {
		return err
	}
	mid, err := proxyclient.ParseMachineRegistrationID(id)
	if err != nil {
		return err
	}
	machineID, err := proxyclient.ParseMachineID(machine)
	if err != nil {
		return err
	}
	bid, err := proxyclient.ParseBindingID(binding)
	if err != nil {
		return err
	}
	return (proxyclient.MachineRecord{ID: mid, MachineID: machineID, BindingID: bid, BindingRevision: proxyclient.Revision(bindingRevision), Revision: proxyclient.Revision(revision), State: proxyclient.MachineState(state), DisplayName: label, CreatedAt: fromUnixMillis(created), UpdatedAt: fromUnixMillis(updated)}).Validate()
}

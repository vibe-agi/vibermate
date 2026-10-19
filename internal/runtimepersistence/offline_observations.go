package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

// Explicit offline export validation reuses stored-format decoders. Opaque
// provider wire bytes retain their existing byte/hash contract, not a new
// interpretation of the provider's protocol. Each query streams one row.
func validateOfflineObservationRows(ctx context.Context, tx *sql.Tx) error {
	scans := []struct {
		query string
		check func(*sql.Rows) error
	}{
		{connectionEventSelect, func(rows *sql.Rows) error { _, err := scanConnectionEvent(rows); return err }},
		{egressAttemptSelect, func(rows *sql.Rows) error { _, err := scanEgressAttempt(rows); return err }},
		{`SELECT ` + strings.ReplaceAll(searchActivityColumns, "candidate.", "") + ` FROM runtime_activities`, func(rows *sql.Rows) error { _, err := scanActivityRecord(rows); return err }},
		{`SELECT ` + rawEvidenceColumns + ` FROM runtime_raw_evidence_envelopes`, func(rows *sql.Rows) error {
			record, digest, err := scanRawEvidence(rows)
			if err != nil {
				return err
			}
			_, err = attachEvidenceBody(ctx, tx, record, digest)
			return err
		}},
		{`SELECT ` + nativeContentColumns + ` FROM native_tool_contents`, func(rows *sql.Rows) error {
			_, digest, err := scanNativeContent(rows)
			if err != nil {
				return err
			}
			body, err := loadEvidenceBody(ctx, tx, digest)
			if err != nil {
				return err
			}
			if len(body) == 0 || len(body) > 16<<20 {
				return nativetool.ErrInvalid
			}
			return nil
		}},
		{`SELECT ` + nativeHistoryColumns + ` FROM native_history_snapshots`, func(rows *sql.Rows) error { _, err := scanNativeHistory(rows); return err }},
		{`SELECT ` + nativeIndexColumns + ` FROM native_inspection_index`, func(rows *sql.Rows) error { _, _, err := scanNativeIndex(rows); return err }},
		{`SELECT run_id,recording,retention_days,snapshot,revision,final FROM acp_snapshots`, func(rows *sql.Rows) error {
			var run string
			var policy environment.ContentRecordingPolicy
			var payload []byte
			var revision uint64
			var final bool
			if err := rows.Scan(&run, &policy.Mode, &policy.RetentionDays, &payload, &revision, &final); err != nil {
				return err
			}
			var snapshot acpobservation.Snapshot
			if json.Unmarshal(payload, &snapshot) != nil || snapshot.Revision != revision || snapshot.Final != final || snapshot.ValidatePolicy(policy) != nil {
				return acpobservation.ErrInvalid
			}
			return nil
		}},
		{`SELECT exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,observation_json FROM runtime_usage_observations`, func(rows *sql.Rows) error {
			var exchange, capture, manual, user, raw string
			var at int64
			if err := rows.Scan(&exchange, &capture, &manual, &user, &at, &raw); err != nil {
				return err
			}
			var value runtimeusage.Observation
			if json.Unmarshal([]byte(raw), &value) != nil || value.Validate() != nil || value.ExchangeID != exchange || value.CaptureRunID != capture || value.ManualCaptureID != manual || string(value.UserID) != user || value.OccurredAt.UnixMilli() != at {
				return ErrUnsupportedSchema
			}
			return nil
		}},
		{`SELECT exchange_id,recorded_at_unix_ms FROM runtime_exchange_contents`, func(rows *sql.Rows) error {
			var id string
			var at int64
			if err := rows.Scan(&id, &at); err != nil {
				return err
			}
			limits, err := exchangecontent.DefaultSourceLimits()
			if err != nil {
				return err
			}
			reference, err := loadStoredContentReferenceWithin(ctx, tx, id, time.UnixMilli(at).UTC(), &limits)
			if err != nil {
				return fmt.Errorf("offline semantic reference: %w", err)
			}
			// The existing loader verifies the manifest/root tuple, message/block
			// hashes, transcript links and reconstructed request/response evidence.
			repository := exchangeContentRepository{reads: tx, limits: &limits}
			repository.loadPhysical = repository.physicalContentRow
			_, err = repository.readSourceProjection(ctx, reference, exchangecontent.RequestViewFull)
			if err != nil {
				return fmt.Errorf("offline semantic projection: %w", err)
			}
			return nil
		}},
	}
	for _, scan := range scans {
		rows, err := tx.QueryContext(ctx, scan.query)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = scan.check(rows); err != nil {
				rows.Close()
				return err
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}

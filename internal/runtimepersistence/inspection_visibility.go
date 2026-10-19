package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func (s *ControlStore) ReadInspectionSnapshot(ctx context.Context, refs []captureidentity.Reference) (snapshot recording.VisibilitySnapshot, result error) {
	if len(refs) > 256 {
		return snapshot, ErrInspectionSourceLimit
	}
	requested := make(map[captureidentity.Reference]bool, len(refs))
	for _, ref := range refs {
		if ref.Validate() != nil {
			return snapshot, ErrInspectionUnavailable
		}
		requested[ref] = true
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return snapshot, errors.Join(ErrInspectionUnavailable, err)
	}
	defer finish()
	tx, err := s.store.reads.BeginTx(operation, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, errors.Join(ErrInspectionUnavailable, err)
	}
	defer tx.Rollback()
	var epoch, visibility, policy int64
	var denialHash []byte
	if err = tx.QueryRowContext(operation, `SELECT s.layout_id,s.archive_epoch,s.visibility_revision,p.revision,d.identity_count,d.chain_hash FROM runtime_storage_state s JOIN runtime_usage_policy p ON p.singleton=s.singleton JOIN recording_denial_coverage d ON d.singleton=s.singleton WHERE s.singleton=1`).Scan(&snapshot.Stamp.LayoutID, &epoch, &visibility, &policy, &snapshot.Stamp.DenialCount, &denialHash); err != nil {
		return snapshot, errors.Join(ErrInspectionUnavailable, err)
	}
	if len(denialHash) != 32 {
		return snapshot, ErrInspectionUnavailable
	}
	copy(snapshot.Stamp.DenialHash[:], denialHash)
	if epoch <= 0 || visibility <= 0 || policy <= 0 {
		return snapshot, ErrInspectionUnavailable
	}
	snapshot.Stamp.ArchiveEpoch, snapshot.Stamp.VisibilityRevision, snapshot.Stamp.UsagePolicyRevision = uint64(epoch), uint64(visibility), uint64(policy)
	if !snapshot.Stamp.Valid() {
		return snapshot, ErrInspectionUnavailable
	}
	snapshot.Scopes = make(map[captureidentity.Reference]recording.InspectionCaptureFence, len(requested))
	if len(requested) > 0 {
		predicates := make([]string, 0, len(requested))
		args := make([]any, 0, 2*len(requested))
		for ref := range requested {
			predicates = append(predicates, `(capture_kind=? AND capture_id=?)`)
			args = append(args, ref.Kind, ref.ID)
		}
		rows, err := tx.QueryContext(operation, `SELECT capture_kind,capture_id,generation,deleted FROM recording_capture_fences WHERE `+strings.Join(predicates, " OR "), args...)
		if err != nil {
			return snapshot, errors.Join(ErrInspectionUnavailable, err)
		}
		for rows.Next() {
			var ref captureidentity.Reference
			var generation, deleted int64
			if err = rows.Scan(&ref.Kind, &ref.ID, &generation, &deleted); err != nil {
				rows.Close()
				return snapshot, errors.Join(ErrInspectionUnavailable, err)
			}
			if !requested[ref] || generation <= 0 || deleted < 0 || deleted > 1 {
				rows.Close()
				return snapshot, ErrInspectionUnavailable
			}
			snapshot.Scopes[ref] = recording.InspectionCaptureFence{Generation: uint64(generation), Deleted: deleted == 1}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return snapshot, errors.Join(ErrInspectionUnavailable, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return recording.VisibilitySnapshot{}, errors.Join(ErrInspectionUnavailable, err)
	}
	return snapshot, nil
}
func (s *ControlStore) RecheckInspectionStamp(ctx context.Context, expected recording.InspectionStamp) error {
	if !expected.Valid() {
		return ErrInspectionChanged
	}
	current, err := s.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return err
	}
	if current.Stamp != expected {
		return ErrInspectionChanged
	}
	return nil
}

var _ recording.InspectionVisibility = (*ControlStore)(nil)

type inspectionBoundary struct {
	control recording.InspectionVisibility
	catalog *InspectionCatalog
}

func NewInspectionBoundary(control recording.InspectionVisibility, catalog *InspectionCatalog) recording.InspectionBoundary {
	return &inspectionBoundary{control: control, catalog: catalog}
}
func (b *inspectionBoundary) Begin(ctx context.Context) (recording.InspectionToken, error) {
	if b.control == nil || b.catalog == nil {
		return recording.InspectionToken{}, ErrInspectionUnavailable
	}
	snapshot, err := b.control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return recording.InspectionToken{}, err
	}
	if !snapshot.Stamp.Valid() || snapshot.Stamp.LayoutID != b.catalog.layout {
		return recording.InspectionToken{}, ErrInspectionUnavailable
	}
	token := recording.InspectionToken{Control: snapshot.Stamp, Sources: b.catalog.Version()}
	if err = b.catalog.Check(token.Sources); err != nil {
		return recording.InspectionToken{}, err
	}
	return token, nil
}
func (b *inspectionBoundary) Check(ctx context.Context, token recording.InspectionToken) error {
	if b.control == nil || b.catalog == nil || token.Control.LayoutID != b.catalog.layout {
		return ErrInspectionUnavailable
	}
	if err := b.control.RecheckInspectionStamp(ctx, token.Control); err != nil {
		return err
	}
	return b.catalog.Check(token.Sources)
}

type inspectionMirror struct {
	Generation int64
	Stamp      recording.InspectionStamp
	Policy     runtimeusage.CollectionPolicy
}

func readInspectionMirror(ctx context.Context, tx *sql.Tx, alias string, expected recording.InspectionStamp) (mirror inspectionMirror, result error) {
	ordinal, err := strconv.Atoi(strings.TrimPrefix(alias, "s"))
	if err != nil || ordinal < 0 || ordinal >= 128 || alias != fmt.Sprintf("s%d", ordinal) {
		return mirror, ErrInspectionUnavailable
	}
	var epoch, visibility, policy, enabled, complete int64
	var since sql.NullInt64
	var expectedFences, expectedCurrentCaps, expectedLegacyCaps, actualFences, actualCurrentCaps, actualLegacyCaps int64
	query := `SELECT g.generation,g.layout_id,g.archive_epoch,g.visibility_revision,g.usage_policy_revision,g.usage_enabled,g.usage_retention_days,g.usage_collecting_since_unix_ms,g.complete,g.fence_count,g.current_cap_count,g.legacy_cap_count,
 (SELECT count(*) FROM ` + alias + `.inspection_mirror_fences f WHERE f.mirror_generation=g.generation),
 (SELECT count(*) FROM ` + alias + `.inspection_mirror_usage_caps c WHERE c.mirror_generation=g.generation),
 (SELECT count(*) FROM ` + alias + `.inspection_mirror_legacy_caps c WHERE c.mirror_generation=g.generation)
 FROM ` + alias + `.inspection_mirror_active a JOIN ` + alias + `.inspection_mirror_generations g ON g.generation=a.generation WHERE a.singleton=1`
	if err = tx.QueryRowContext(ctx, query).Scan(&mirror.Generation, &mirror.Stamp.LayoutID, &epoch, &visibility, &policy, &enabled, &mirror.Policy.RetentionDays, &since, &complete, &expectedFences, &expectedCurrentCaps, &expectedLegacyCaps, &actualFences, &actualCurrentCaps, &actualLegacyCaps); err != nil {
		return mirror, errors.Join(ErrInspectionUnavailable, err)
	}
	if mirror.Generation <= 0 || epoch <= 0 || visibility <= 0 || policy <= 0 || enabled < 0 || enabled > 1 || complete != 1 || expectedFences != actualFences || expectedCurrentCaps != actualCurrentCaps || expectedLegacyCaps != actualLegacyCaps {
		return mirror, ErrInspectionUnavailable
	}
	mirror.Stamp.ArchiveEpoch, mirror.Stamp.VisibilityRevision, mirror.Stamp.UsagePolicyRevision = uint64(epoch), uint64(visibility), uint64(policy)
	if _, err = readRecordingDenialCoverage(ctx, tx, alias, expected); err != nil {
		return mirror, err
	}
	mirror.Stamp.DenialCount, mirror.Stamp.DenialHash = expected.DenialCount, expected.DenialHash
	if !mirror.Stamp.Valid() || mirror.Stamp != expected {
		return mirror, ErrInspectionUnavailable
	}
	mirror.Policy.Enabled, mirror.Policy.Revision = enabled == 1, policy
	if since.Valid {
		value := time.UnixMilli(since.Int64).UTC()
		mirror.Policy.CollectingSince = &value
	}
	if mirror.Policy.Validate() != nil {
		return mirror, ErrInspectionUnavailable
	}
	// Completeness counts are necessary but invalid cap/fence tuples must not
	// silently turn a privacy predicate into false and bypass its obligation.
	checks := []struct{ table, predicate string }{
		{"inspection_mirror_fences", `typeof(capture_kind)<>'text' OR capture_kind NOT IN('managed_run','manual_capture') OR typeof(capture_id)<>'text' OR length(CAST(capture_id AS BLOB)) NOT BETWEEN 1 AND 128 OR typeof(capture_generation)<>'integer' OR capture_generation<=0 OR typeof(deleted)<>'integer' OR deleted NOT IN(0,1)`},
		{"inspection_mirror_usage_caps", `typeof(retention_days)<>'integer' OR retention_days NOT BETWEEN 1 AND 365 OR typeof(through_policy_revision)<>'integer' OR through_policy_revision<=0`},
		{"inspection_mirror_legacy_caps", `typeof(archive_id)<>'text' OR length(CAST(archive_id AS BLOB)) NOT BETWEEN 1 AND 128 OR typeof(retention_days)<>'integer' OR retention_days NOT BETWEEN 1 AND 365 OR typeof(after_sequence)<>'integer' OR typeof(through_sequence)<>'integer' OR after_sequence<0 OR through_sequence<=0 OR after_sequence>=through_sequence`},
	}
	for _, check := range checks {
		var invalid int
		query := `SELECT EXISTS(SELECT 1 FROM ` + alias + `.` + check.table + ` WHERE mirror_generation=? AND (` + check.predicate + `))`
		if err = tx.QueryRowContext(ctx, query, mirror.Generation).Scan(&invalid); err != nil || invalid != 0 {
			return mirror, errors.Join(ErrInspectionUnavailable, err)
		}
	}
	return mirror, nil
}

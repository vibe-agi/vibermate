//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// This adapter reads only the already-validated private control copy. It lets
// the production inspection reader validate its complete privacy mirror.
type packagedACPVisibility struct{ control *sql.DB }

func (v packagedACPVisibility) ReadInspectionSnapshot(ctx context.Context, refs []captureidentity.Reference) (recording.VisibilitySnapshot, error) {
	var value recording.VisibilitySnapshot
	var hash []byte
	err := v.control.QueryRowContext(ctx, `SELECT s.layout_id,s.archive_epoch,s.visibility_revision,p.revision,d.identity_count,d.chain_hash FROM runtime_storage_state s JOIN runtime_usage_policy p USING(singleton) JOIN recording_denial_coverage d USING(singleton) WHERE s.singleton=1`).Scan(&value.Stamp.LayoutID, &value.Stamp.ArchiveEpoch, &value.Stamp.VisibilityRevision, &value.Stamp.UsagePolicyRevision, &value.Stamp.DenialCount, &hash)
	if err != nil {
		return value, err
	}
	if len(hash) != 32 {
		return value, errors.New("ACP control denial hash invalid")
	}
	copy(value.Stamp.DenialHash[:], hash)
	value.Scopes = map[captureidentity.Reference]recording.InspectionCaptureFence{}
	for _, ref := range refs {
		var fence recording.InspectionCaptureFence
		err = v.control.QueryRowContext(ctx, `SELECT generation,deleted FROM recording_capture_fences WHERE capture_kind=? AND capture_id=?`, ref.Kind, ref.ID).Scan(&fence.Generation, &fence.Deleted)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return value, err
		}
		value.Scopes[ref] = fence
	}
	return value, nil
}
func (v packagedACPVisibility) RecheckInspectionStamp(ctx context.Context, want recording.InspectionStamp) error {
	got, err := v.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return err
	}
	if got.Stamp != want {
		return recording.ErrInspectionChanged
	}
	return nil
}

func packagedACPScopeReady(ctx context.Context, root string, mode environment.ContentRecordingMode) (ready bool, result error) {
	if mode != environment.ContentRecordingMetadataOnly && mode != environment.ContentRecordingFull {
		return false, errors.New("unsupported ACP fixture recording mode")
	}
	result = withPackagedACPRoles(ctx, root, func(copyRoot string, layout runtimedata.StorageLayout, control, observations *sql.DB) error {
		var count int
		if err := control.QueryRowContext(ctx, `SELECT count(*) FROM acp_registrations WHERE recording=?`, mode).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if count != 1 {
			return errors.New("ACP recording mode has ambiguous registrations")
		}
		var id, state string
		var epoch, generation uint64
		var expiry int64
		if err := control.QueryRowContext(ctx, `SELECT p.run_id,p.archive_epoch,p.capture_generation,p.expires_at_unix_ms,r.state FROM acp_registrations p JOIN capture_runs r USING(run_id) WHERE p.recording=?`, mode).Scan(&id, &epoch, &generation, &expiry, &state); err != nil {
			return err
		}
		if (state != "created" && state != "attached") || expiry <= time.Now().UnixMilli() {
			return errors.New("ACP peer retired before protocol rendezvous")
		}
		ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: id}
		visibility := packagedACPVisibility{control}
		current, err := visibility.ReadInspectionSnapshot(ctx, []captureidentity.Reference{ref})
		if err != nil {
			return err
		}
		fence, exists := current.Scopes[ref]
		if !exists || fence.Deleted || fence.Generation != generation || current.Stamp.ArchiveEpoch != epoch {
			return errors.New("ACP registration is not its current original scope")
		}
		var sourceGeneration uint64
		var cleanupComplete bool
		if err = control.QueryRowContext(ctx, `SELECT source_generation,requested_revision=completed_revision AND active_revision=0 FROM recording_cleanup_state WHERE singleton=1`).Scan(&sourceGeneration, &cleanupComplete); err != nil {
			return err
		}
		if !cleanupComplete {
			return nil
		}
		if len(layout.Archives) != 0 {
			return errors.New("fresh ACP fixture has unexpected archives")
		}
		digest := sha256.Sum256([]byte(layout.ID))
		sourceID := "current-" + hex.EncodeToString(digest[:16])
		catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: copyRoot, LayoutID: layout.ID, Generation: sourceGeneration, Sources: []runtimepersistence.InspectionSource{{ID: sourceID, RelativePath: layout.Observation.RelativePath, Database: runtimepersistence.SchemaState{Identity: layout.Observation.Identity, Revision: layout.Observation.Revision}, Current: true}}})
		if err != nil {
			return err
		}
		defer catalog.Close(context.Background())
		limits, err := exchangecontent.DefaultSourceLimits()
		if err != nil {
			return err
		}
		reader, err := runtimepersistence.NewActivityInspectionReader(catalog, visibility, limits)
		if err != nil {
			return err
		}
		if _, err = reader.ListExchanges(ctx, activity.PageRequest{Limit: 1}); err != nil {
			if errors.Is(err, recording.ErrInspectionUnavailable) || errors.Is(err, recording.ErrInspectionChanged) {
				return nil
			}
			return err
		}
		// The SDK validated the full active mirror and its original control stamp.
		// Witness this exact newly registered Capture, not generic writer readiness.
		return observations.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inspection_mirror_active a JOIN inspection_mirror_fences f ON f.mirror_generation=a.generation WHERE a.singleton=1 AND f.capture_kind='managed_run' AND f.capture_id=? AND f.capture_generation=? AND f.deleted=0)`, id, generation).Scan(&ready)
	})
	if result != nil {
		return false, result
	}
	return ready, nil
}

func waitPackagedACPScope(ctx context.Context, root string, mode environment.ContentRecordingMode) error {
	read, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		ready, err := packagedACPScopeReady(read, root, mode)
		if err == nil && ready {
			return nil
		}
		if read.Err() != nil {
			return read.Err()
		}
		if err != nil && !errors.Is(err, errPackagedACPSourceChanged) {
			return fmt.Errorf("ACP original-scope rendezvous: %w", err)
		}
		select {
		case <-read.Done():
			return read.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
